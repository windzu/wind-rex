import Foundation
import Observation

/// Manages saved Relay Fleet configurations, their Keychain secrets, and
/// automatic discovery/sync of online `fleet_auth` hosts into `HostStore`.
@Observable
final class RelayStore {
    struct SyncSummary: Equatable {
        var relay: RelayConfig
        var added: [HostConfig]
        var updated: [HostConfig]
        var onlineCount: Int
        var manualPairingRequiredCount: Int
        var ignoredCount: Int

        var userMessage: String {
            var parts: [String] = []
            if !added.isEmpty {
                let countText = String(localized: "\(added.count) hosts")
                parts.append(String(localized: "Imported \(countText) from \(relay.label)."))
            } else if !updated.isEmpty {
                let countText = String(localized: "\(updated.count) hosts")
                parts.append(String(localized: "Updated \(countText) from \(relay.label)."))
            } else if manualPairingRequiredCount > 0 {
                parts.append(String(localized: "Saved \(relay.label)."))
            } else {
                parts.append(String(localized: "Saved \(relay.label). Online hosts with fleet auto-connect enabled will appear automatically."))
            }
            if manualPairingRequiredCount > 0 {
                parts.append(String(localized: "\(manualPairingRequiredCount) online hosts require an individual QR code."))
            }
            return parts.joined(separator: " ")
        }
    }

    private(set) var relays: [RelayConfig]
    /// Relays currently polling `GET /api/hosts`.
    private(set) var syncing: Set<UUID> = []
    /// Most recently discovered online hosts per relay (including `fleet_auth: false`).
    private(set) var lastDiscovered: [UUID: [DiscoveredRelayHost]] = [:]
    /// Last sync error per relay, cleared on the next successful sync.
    private(set) var lastErrors: [UUID: String] = [:]

    /// Minimum interval between automatic foreground / screen-appear syncs.
    static let foregroundThrottle: TimeInterval = 30

    @ObservationIgnored private let secrets: RelaySecrets
    @ObservationIgnored private let client: RelayDiscoveryClient
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let defaultsKey: String
    @ObservationIgnored private var lastAutoSyncAt: Date?
    @ObservationIgnored private var inFlight: [UUID: Task<SyncSummary, Error>] = [:]

    init(
        secrets: RelaySecrets = KeychainRelaySecrets(),
        client: RelayDiscoveryClient = URLSessionRelayDiscoveryClient(),
        defaults: UserDefaults = .standard,
        defaultsKey: String = "relays"
    ) {
        self.secrets = secrets
        self.client = client
        self.defaults = defaults
        self.defaultsKey = defaultsKey

        var loaded = defaults.data(forKey: defaultsKey)
            .flatMap { try? JSONDecoder().decode([RelayConfig].self, from: $0) } ?? []
        for i in loaded.indices {
            loaded[i].secret = secrets.secret(for: loaded[i].id)
        }
        self.relays = loaded
    }

    /// Saves or updates a relay from a scanned/pasted `#r=` Fleet payload.
    /// Stores the secret exclusively in Keychain (`RelaySecrets`).
    @discardableResult
    func upsert(pairing: RelayPairingPayload) -> RelayConfig {
        if let i = relays.firstIndex(where: { $0.url == pairing.url }) {
            var updated = relays[i]
            if pairing.hasExplicitLabel {
                updated.label = pairing.label
            }
            updated.tlsPin = pairing.tlsPin
            updated.secret = pairing.secret
            relays[i] = updated
            secrets.set(pairing.secret, for: updated.id)
            save()
            return updated
        }
        let config = RelayConfig(
            label: pairing.label,
            url: pairing.url,
            tlsPin: pairing.tlsPin,
            autoSync: true,
            secret: pairing.secret
        )
        relays.append(config)
        secrets.set(pairing.secret, for: config.id)
        save()
        return config
    }

    /// Saves the Relay from a `#r=` Fleet QR and immediately syncs its online hosts.
    @discardableResult
    func pairAndSync(_ pairing: RelayPairingPayload, into hostStore: HostStore) async throws -> SyncSummary {
        let relay = upsert(pairing: pairing)
        return try await sync(relayID: relay.id, into: hostStore)
    }

    /// Polls `GET /api/hosts` for one relay and reconciles `fleet_auth` hosts
    /// into `hostStore`. Concurrent calls for the same relay share one in-flight task.
    @discardableResult
    func sync(relayID: UUID, into hostStore: HostStore) async throws -> SyncSummary {
        if let existing = inFlight[relayID] {
            return try await existing.value
        }
        let task = Task<SyncSummary, Error> { [weak self] in
            guard let self else { throw CancellationError() }
            return try await self.performSync(relayID: relayID, into: hostStore)
        }
        inFlight[relayID] = task
        defer { inFlight[relayID] = nil }
        return try await task.value
    }

    private func performSync(relayID: UUID, into hostStore: HostStore) async throws -> SyncSummary {
        guard let initial = relays.first(where: { $0.id == relayID }) else {
            throw CancellationError()
        }
        let secret = initial.secret ?? secrets.secret(for: relayID) ?? ""
        guard !secret.isEmpty else {
            let err = RelayDiscoveryError.missingSecret
            lastErrors[relayID] = err.localizedDescription
            throw err
        }

        syncing.insert(relayID)
        defer { syncing.remove(relayID) }

        do {
            let discovered = try await client.discoverHosts(
                relayURL: initial.url,
                secret: secret,
                tlsPin: initial.tlsPin
            )
            // Re-read the relay after the async hop so edits, deletions, or
            // newly ignored host IDs made during the round trip are preserved.
            guard let idx = relays.firstIndex(where: { $0.id == relayID }) else {
                throw CancellationError()
            }
            relays[idx].lastSyncAt = .now
            let currentRelay = relays[idx]
            let currentSecret = currentRelay.secret ?? secret
            save()

            lastDiscovered[relayID] = discovered
            lastErrors[relayID] = nil

            let summary = reconcile(
                discovered: discovered,
                relay: currentRelay,
                secret: currentSecret,
                into: hostStore
            )
            ConnectionLog.note(
                "relay \(currentRelay.label): synced \(discovered.count) online (\(summary.added.count) added, \(summary.updated.count) updated, \(summary.manualPairingRequiredCount) manual-only)"
            )
            return summary
        } catch {
            if relays.contains(where: { $0.id == relayID }) {
                lastErrors[relayID] = error.localizedDescription
            }
            ConnectionLog.note("relay \(initial.label): sync failed (\(error.localizedDescription))")
            throw error
        }
    }

    private func reconcile(
        discovered: [DiscoveredRelayHost],
        relay: RelayConfig,
        secret: String,
        into hostStore: HostStore
    ) -> SyncSummary {
        let ignored = Set(relay.ignoredHostIDs)
        var added: [HostConfig] = []
        var updated: [HostConfig] = []
        var manualOnly = 0
        var ignoredCount = 0

        for item in discovered {
            guard item.fleetAuth else {
                manualOnly += 1
                continue
            }
            if ignored.contains(item.id) {
                ignoredCount += 1
                continue
            }
            guard let hostURL = item.hostURL(base: relay.url) else { continue }
            let token = RelayFleet.deriveFleetToken(relaySecret: secret, hostID: item.id)

            // Deduplicate by (relayID, relayHostID) first, then by exact URL.
            let existingHost = hostStore.hosts.first { host in
                guard !host.isDemo else { return false }
                if host.config.relayID == relay.id && host.config.relayHostID == item.id {
                    return true
                }
                return host.config.url == hostURL
            }

            if let existing = existingHost {
                var next = existing.config
                next.relayID = relay.id
                next.relayHostID = item.id
                next.tlsPin = relay.tlsPin
                next.lanToken = token
                // Sync server-side display name changes unless the user manually renamed the host in iOS.
                if next.customName != true {
                    next.name = item.displayName
                }
                if next != existing.config {
                    hostStore.update(next)
                    updated.append(next)
                }
            } else {
                let config = HostConfig(
                    name: item.displayName,
                    url: hostURL,
                    lanToken: token,
                    tlsPin: relay.tlsPin,
                    relayID: relay.id,
                    relayHostID: item.id
                )
                hostStore.add(config)
                added.append(config)
            }
        }

        // Offline hosts absent from `discovered` are intentionally preserved.
        return SyncSummary(
            relay: relay,
            added: added,
            updated: updated,
            onlineCount: discovered.count,
            manualPairingRequiredCount: manualOnly,
            ignoredCount: ignoredCount
        )
    }

    /// Re-polls saved relays into `hostStore`.
    /// When `force` is false (app foreground / view appear), enforces a 30 s
    /// throttle and skips relays with `autoSync == false`.
    /// When `force` is true (pull-to-refresh), polls all saved relays immediately.
    func syncAll(into hostStore: HostStore, force: Bool = false, now: Date = .now) async {
        guard !relays.isEmpty else { return }
        if !force, let last = lastAutoSyncAt, now.timeIntervalSince(last) < Self.foregroundThrottle {
            return
        }
        lastAutoSyncAt = now

        let targets = force ? relays.map(\.id) : relays.filter(\.autoSync).map(\.id)
        guard !targets.isEmpty else { return }

        await withTaskGroup(of: Void.self) { group in
            for id in targets {
                group.addTask { [weak self] in
                    _ = try? await self?.sync(relayID: id, into: hostStore)
                }
            }
        }
    }

    /// Records a user-deleted relay host in its relay's `ignoredHostIDs` so
    /// subsequent auto-syncs respect the deletion.
    func noteHostRemoved(_ config: HostConfig) {
        guard let relayID = config.relayID,
              let relayHostID = config.relayHostID,
              let idx = relays.firstIndex(where: { $0.id == relayID })
        else { return }
        if !relays[idx].ignoredHostIDs.contains(relayHostID) {
            relays[idx].ignoredHostIDs.append(relayHostID)
            relays[idx].ignoredHostIDs.sort()
            save()
        }
    }

    /// Clears `ignoredHostIDs` for a relay so previously deleted hosts can be
    /// re-imported on the next sync.
    func resetIgnoredHosts(for relayID: UUID) {
        guard let idx = relays.firstIndex(where: { $0.id == relayID }) else { return }
        guard !relays[idx].ignoredHostIDs.isEmpty else { return }
        relays[idx].ignoredHostIDs = []
        save()
    }

    /// Updates editable metadata (`label`, `autoSync`) for a saved relay.
    func update(_ relay: RelayConfig) {
        guard let idx = relays.firstIndex(where: { $0.id == relay.id }) else { return }
        var next = relay
        next.secret = relay.secret ?? relays[idx].secret
        relays[idx] = next
        if let secret = next.secret {
            secrets.set(secret, for: next.id)
        }
        save()
    }

    /// Removes a relay and deletes its shared secret from Keychain.
    /// Already-imported hosts remain in `HostStore`.
    func remove(_ relayID: UUID) {
        inFlight[relayID]?.cancel()
        inFlight[relayID] = nil
        relays.removeAll { $0.id == relayID }
        lastDiscovered[relayID] = nil
        lastErrors[relayID] = nil
        secrets.set(nil, for: relayID)
        save()
    }

    func relay(_ id: UUID) -> RelayConfig? {
        relays.first { $0.id == id }
    }

    func hostCount(for relayID: UUID, in hostStore: HostStore) -> Int {
        hostStore.hosts.filter { !$0.isDemo && $0.config.relayID == relayID }.count
    }

    private func save() {
        defaults.set(try? JSONEncoder().encode(relays), forKey: defaultsKey)
    }
}
