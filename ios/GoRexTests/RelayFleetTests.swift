import Foundation
import XCTest

/// In-memory RelaySecrets for isolated RelayStore unit tests.
final class MemoryRelaySecrets: RelaySecrets {
    var secrets: [UUID: String] = [:]
    func secret(for relay: UUID) -> String? { secrets[relay] }
    func set(_ secret: String?, for relay: UUID) { secrets[relay] = secret }
}

/// Mocked GET /api/hosts discovery client for deterministic RelayStore sync tests.
final class MockRelayDiscoveryClient: RelayDiscoveryClient {
    struct Call: Equatable {
        let relayURL: URL
        let secret: String
        let tlsPin: String?
    }

    var calls: [Call] = []
    var respond: (URL, String, String?) async throws -> [DiscoveredRelayHost] = { _, _, _ in [] }

    func discoverHosts(relayURL: URL, secret: String, tlsPin: String?) async throws -> [DiscoveredRelayHost] {
        calls.append(Call(relayURL: relayURL, secret: secret, tlsPin: tlsPin))
        return try await respond(relayURL, secret, tlsPin)
    }
}

/// Tests Issue #55 Part 2 (iOS): HMAC-SHA256 derived fleet token cross-platform
/// vector, `#r=` Fleet QR parsing, Keychain secret isolation, mocked
/// `GET /api/hosts` sync, offline preservation, `ignoredHostIDs`, rename sync,
/// and pre-#55 `HostConfig` backward compatibility.
nonisolated final class RelayFleetTests: XCTestCase {
    private var samplePin: String { PinnedTLSSession.sha256Pin(of: Data("relay-leaf-cert-der".utf8)) }

    private func isolatedDefaults() -> (UserDefaults, String) {
        let suite = "RelayFleetTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        return (defaults, suite)
    }

    /// Pinned cross-platform HMAC-SHA256 test vector matching Go
    /// `directrelay.DeriveFleetToken("test-relay-secret", "host-abc-123")`.
    @MainActor func testDeriveFleetTokenPinnedVector() {
        let token = RelayFleet.deriveFleetToken(
            relaySecret: "test-relay-secret",
            hostID: "host-abc-123"
        )
        XCTAssertEqual(token, "3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik")
        XCTAssertEqual(token.count, 43)
        XCTAssertFalse(token.contains("+"))
        XCTAssertFalse(token.contains("/"))
        XCTAssertFalse(token.contains("="))

        // Different host IDs or secrets derive distinct 43-char tokens.
        let otherHost = RelayFleet.deriveFleetToken(relaySecret: "test-relay-secret", hostID: "host-xyz-999")
        let otherSecret = RelayFleet.deriveFleetToken(relaySecret: "other-secret", hostID: "host-abc-123")
        XCTAssertNotEqual(token, otherHost)
        XCTAssertNotEqual(token, otherSecret)
        XCTAssertEqual(otherHost.count, 43)
        XCTAssertEqual(otherSecret.count, 43)
    }

    /// `#r=` Fleet QR URLs parse cleanly with optional `p=` and `n=`, reject
    /// invalid payloads, and remain strictly disjoint from `#k=` single-host codes.
    @MainActor func testFleetQRParsingAndDisjointnessFromSingleHost() throws {
        let pin = samplePin

        // Full Fleet QR with pin and percent-encoded label.
        let full = try XCTUnwrap(RelayPairingPayload(
            pairing: "https://relay.example.com:7443/#r=my_relay_secret_01&p=\(pin)&n=Home%20Relay\n"
        ))
        XCTAssertEqual(full.url.absoluteString, "https://relay.example.com:7443")
        XCTAssertEqual(full.secret, "my_relay_secret_01")
        XCTAssertEqual(full.tlsPin, pin)
        XCTAssertEqual(full.label, "Home Relay")
        XCTAssertTrue(full.hasExplicitLabel)

        // Minimal Fleet QR (CA-signed TLS, no pin, no explicit label).
        let minimal = try XCTUnwrap(RelayPairingPayload(
            pairing: "HTTPS://relay.example.com#r=secret-123"
        ))
        XCTAssertEqual(minimal.url.absoluteString, "https://relay.example.com")
        XCTAssertEqual(minimal.secret, "secret-123")
        XCTAssertNil(minimal.tlsPin)
        XCTAssertEqual(minimal.label, "relay.example.com")
        XCTAssertFalse(minimal.hasExplicitLabel)

        // Form-encoded `+` and UTF-8 percent-encoded labels decode cleanly.
        let formEncoded = try XCTUnwrap(RelayPairingPayload(
            pairing: "https://relay.example.com:7443/#r=sec+ret&n=Lab+Cluster"
        ))
        XCTAssertEqual(formEncoded.secret, "sec+ret")
        XCTAssertEqual(formEncoded.label, "Lab Cluster")

        // HostPairing.resolve routes `#r=` to `.relayFleet` and `#k=` to `.added`/`.existing`.
        guard case .relayFleet(let resolvedRelay) = HostPairing.resolve(
            "https://relay.example.com:7443/#r=secret-123&p=\(pin)&n=Lab",
            existing: []
        ) else {
            return XCTFail("expected .relayFleet outcome")
        }
        XCTAssertEqual(resolvedRelay.label, "Lab")
        XCTAssertEqual(resolvedRelay.tlsPin, pin)

        guard case .added(let singleHost) = HostPairing.resolve(
            "https://relay.example.com:7443/h/host_12345678/#k=hostToken123&p=\(pin)&n=alice-laptop",
            existing: []
        ) else {
            return XCTFail("expected single-host .added outcome")
        }
        XCTAssertEqual(singleHost.name, "alice-laptop")
        XCTAssertEqual(singleHost.lanToken, "hostToken123")

        // `#r=` is rejected by HostConfig(pairing:) so old single-host path never misinterprets it.
        XCTAssertNil(HostConfig(pairing: "https://relay.example.com:7443/#r=secret-123"))

        // Malformed Fleet QR payloads are rejected.
        for bad in [
            "",
            "https://relay.example.com:7443",
            "https://relay.example.com:7443/#",
            "https://relay.example.com:7443/#r=",
            "https://relay.example.com:7443/#p=\(pin)",
            "https://relay.example.com:7443/#k=singleHostToken",
            "https://relay.example.com:7443/#r=secret&k=singleHostToken",
            "https://relay.example.com:7443/#r=secret&p=invalidpin",
            "https://relay.example.com:7443/#r=secret&x=unknown",
            "https://relay.example.com:7443/h/host_12345678/#r=secret",
            "https://relay.example.com:7443/?r=secret",
            "https://user:pass@relay.example.com:7443/#r=secret",
            "http://relay.example.com:7443/#r=secret&p=\(pin)",
            "ftp://relay.example.com/#r=secret",
        ] {
            XCTAssertNil(RelayPairingPayload(pairing: bad), bad)
        }
    }

    /// `RelayConfig` JSON persistence never writes `secret` to UserDefaults;
    /// secrets live exclusively in `RelaySecrets` (Keychain).
    @MainActor func testRelaySecretNeverStoredInUserDefaults() throws {
        let (defaults, suite) = isolatedDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }

        let secrets = MemoryRelaySecrets()
        let store = RelayStore(secrets: secrets, client: MockRelayDiscoveryClient(), defaults: defaults)
        let payload = try XCTUnwrap(RelayPairingPayload(
            pairing: "https://relay.example.com:7443/#r=super-secret-relay-key&p=\(samplePin)&n=Home"
        ))
        let saved = store.upsert(pairing: payload)

        XCTAssertEqual(secrets.secrets[saved.id], "super-secret-relay-key")
        let persisted = String(decoding: try XCTUnwrap(defaults.data(forKey: "relays")), as: UTF8.self)
        XCTAssertFalse(persisted.contains("super-secret-relay-key"), persisted)
        XCTAssertFalse(persisted.contains("secret"), persisted)

        // Reloading from UserDefaults hydrates the secret back from RelaySecrets.
        let reloaded = RelayStore(secrets: secrets, client: MockRelayDiscoveryClient(), defaults: defaults)
        XCTAssertEqual(reloaded.relays.count, 1)
        XCTAssertEqual(reloaded.relays.first?.secret, "super-secret-relay-key")
        XCTAssertEqual(reloaded.relays.first?.tlsPin, samplePin)

        // Removing the relay deletes its secret from RelaySecrets too.
        reloaded.remove(saved.id)
        XCTAssertNil(secrets.secrets[saved.id])
        XCTAssertTrue(reloaded.relays.isEmpty)
    }

    /// `DiscoveredRelayHost` tolerates missing `fleet_auth` (defaulting to `false`)
    /// and falls back to `id` when `name` is empty.
    @MainActor func testDiscoveredHostDecodingToleratesMissingFleetAuth() throws {
        let json = """
        {
          "hosts": [
            {"id": "host-abc-123", "name": "alice-laptop", "connected_at": "2026-11-26T10:00:00Z", "fleet_auth": true},
            {"id": "host-legacy-01", "name": "", "connected_at": "2026-11-26T10:01:00Z"}
          ]
        }
        """
        struct Wrapper: Decodable { let hosts: [DiscoveredRelayHost] }
        let decoded = try JSONDecoder().decode(Wrapper.self, from: Data(json.utf8))
        XCTAssertEqual(decoded.hosts.count, 2)
        XCTAssertTrue(decoded.hosts[0].fleetAuth)
        XCTAssertEqual(decoded.hosts[0].displayName, "alice-laptop")
        XCTAssertFalse(decoded.hosts[1].fleetAuth)
        XCTAssertEqual(decoded.hosts[1].displayName, "host-legacy-01")
    }

    /// Mocked `GET /api/hosts` sync imports `fleet_auth: true` hosts with derived
    /// HMAC tokens in `LANTokens`, skips `fleet_auth: false` hosts, preserves
    /// offline hosts, syncs server-side renames without overwriting custom iOS
    /// names, and respects `ignoredHostIDs` after user deletion (and restores on reset).
    @MainActor func testFleetSyncLifecycleOfflinePreservationAndIgnoredHosts() async throws {
        let savedHosts = UserDefaults.standard.data(forKey: "hosts")
        defer {
            if let savedHosts { UserDefaults.standard.set(savedHosts, forKey: "hosts") }
            else { UserDefaults.standard.removeObject(forKey: "hosts") }
        }
        UserDefaults.standard.removeObject(forKey: "hosts")

        let (defaults, suite) = isolatedDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }

        let lanTokens = MemoryLANTokens()
        let hostStore = HostStore(tokens: lanTokens)
        defer { hostStore.hosts.forEach { $0.stop() } }

        let relaySecrets = MemoryRelaySecrets()
        let mockClient = MockRelayDiscoveryClient()
        let relayStore = RelayStore(secrets: relaySecrets, client: mockClient, defaults: defaults)

        var onlineHosts: [DiscoveredRelayHost] = [
            DiscoveredRelayHost(id: "host-abc-123", name: "alice-laptop", fleetAuth: true),
            DiscoveredRelayHost(id: "dev-box-0001", name: "dev-box-1", fleetAuth: true),
            DiscoveredRelayHost(id: "locked-box-1", name: "vault-node", fleetAuth: false),
        ]
        mockClient.respond = { _, _, _ in onlineHosts }

        let pin = samplePin
        let payload = try XCTUnwrap(RelayPairingPayload(
            pairing: "https://relay.example.com:7443/#r=test-relay-secret&p=\(pin)&n=Home%20Relay"
        ))

        // 1. Initial scan imports both fleet_auth hosts and skips fleet_auth: false.
        let first = try await relayStore.pairAndSync(payload, into: hostStore)
        XCTAssertEqual(first.added.count, 2)
        XCTAssertEqual(first.manualPairingRequiredCount, 1)
        XCTAssertEqual(hostStore.hosts.map(\.config.name), ["alice-laptop", "dev-box-1"])

        let alice = try XCTUnwrap(hostStore.hosts.first { $0.config.relayHostID == "host-abc-123" })
        XCTAssertEqual(alice.config.url.absoluteString, "https://relay.example.com:7443/h/host-abc-123")
        XCTAssertEqual(alice.config.tlsPin, pin)
        XCTAssertEqual(alice.config.relayID, first.relay.id)
        XCTAssertEqual(alice.config.lanToken, "3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik")
        XCTAssertEqual(lanTokens.tokens[alice.id], "3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik")

        // Verify UserDefaults["hosts"] does not leak derived tokens.
        let persistedHosts = String(decoding: try XCTUnwrap(UserDefaults.standard.data(forKey: "hosts")), as: UTF8.self)
        XCTAssertFalse(persistedHosts.contains("3tFr5S1FzWIelzutS1758Y4jbi00QWxKIIQuCYVYtik"))

        // 2. Subsequent sync: dev-box-1 goes offline (absent from /api/hosts),
        //    alice-laptop is renamed on server to "alice-studio", gpu-node-1 joins.
        onlineHosts = [
            DiscoveredRelayHost(id: "host-abc-123", name: "alice-studio", fleetAuth: true),
            DiscoveredRelayHost(id: "gpu-node-001", name: "gpu-node-1", fleetAuth: true),
        ]
        let second = try await relayStore.sync(relayID: first.relay.id, into: hostStore)
        XCTAssertEqual(second.added.map(\.name), ["gpu-node-1"])
        XCTAssertEqual(second.updated.map(\.name), ["alice-studio"])
        // Offline host dev-box-1 MUST stay in HostStore!
        XCTAssertEqual(hostStore.hosts.map(\.config.name), ["alice-studio", "dev-box-1", "gpu-node-1"])

        // 3. Custom iOS rename (via HostStore.rename choke point, as HostSection uses)
        //    sets customName = true and is preserved even when server reports another name.
        hostStore.rename(alice.id, to: "My Mac")
        XCTAssertEqual(hostStore.host(alice.id)?.config.customName, true)
        onlineHosts = [
            DiscoveredRelayHost(id: "host-abc-123", name: "alice-server-name", fleetAuth: true),
            DiscoveredRelayHost(id: "gpu-node-001", name: "gpu-node-1", fleetAuth: true),
        ]
        _ = try await relayStore.sync(relayID: first.relay.id, into: hostStore)
        XCTAssertEqual(hostStore.host(alice.id)?.config.name, "My Mac")

        // 4. Deleting gpu-node-1 through HostStore.remove triggers the onRemove
        //    choke point -> recorded in ignoredHostIDs -> next sync does NOT re-add it.
        hostStore.onRemove = { [weak relayStore] config in
            relayStore?.noteHostRemoved(config)
        }
        let gpu = try XCTUnwrap(hostStore.hosts.first { $0.config.relayHostID == "gpu-node-001" })
        hostStore.remove(gpu.id)
        XCTAssertEqual(relayStore.relay(first.relay.id)?.ignoredHostIDs, ["gpu-node-001"])

        let afterDelete = try await relayStore.sync(relayID: first.relay.id, into: hostStore)
        XCTAssertEqual(afterDelete.ignoredCount, 1)
        XCTAssertNil(hostStore.hosts.first { $0.config.relayHostID == "gpu-node-001" })

        // 5. Resetting ignored hosts allows gpu-node-1 to be re-imported on sync.
        relayStore.resetIgnoredHosts(for: first.relay.id)
        let afterReset = try await relayStore.sync(relayID: first.relay.id, into: hostStore)
        XCTAssertEqual(afterReset.added.map(\.name), ["gpu-node-1"])
        XCTAssertNotNil(hostStore.hosts.first { $0.config.relayHostID == "gpu-node-001" })

        // 6. Auto-sync throttles within 30s unless forced (pull-to-refresh).
        let callsBefore = mockClient.calls.count
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        await relayStore.syncAll(into: hostStore, force: false, now: t0)
        XCTAssertEqual(mockClient.calls.count, callsBefore + 1)
        await relayStore.syncAll(into: hostStore, force: false, now: t0.addingTimeInterval(10))
        XCTAssertEqual(mockClient.calls.count, callsBefore + 1, "should throttle within 30s")
        await relayStore.syncAll(into: hostStore, force: true, now: t0.addingTimeInterval(10))
        XCTAssertEqual(mockClient.calls.count, callsBefore + 2, "pull-to-refresh forces sync")
        await relayStore.syncAll(into: hostStore, force: false, now: t0.addingTimeInterval(45))
        XCTAssertEqual(mockClient.calls.count, callsBefore + 3, "should sync again after 30s")
    }

    /// Live HTTP round-trip test against FakeHTTP verifying Bearer Authorization
    /// header, 200 decoding, 401 unauthorized, and 404 discovery disabled.
    @MainActor func testDiscoveryClientHTTPStatusesAndBearerHeader() async throws {
        let http = try FakeHTTP()
        defer { http.stop() }
        let base = try await http.url()
        let client = URLSessionRelayDiscoveryClient()

        http.respond = { req in
            if req.header("Authorization") != "Bearer relay-secret-xyz" {
                return (401, ["error": "unauthorized"])
            }
            return (200, [
                "hosts": [
                    ["id": "host-abc-123", "name": "alice-laptop", "path": "/h/host-abc-123", "fleet_auth": true],
                ],
            ])
        }

        let hosts = try await client.discoverHosts(relayURL: base, secret: "relay-secret-xyz", tlsPin: nil)
        XCTAssertEqual(hosts.count, 1)
        XCTAssertEqual(hosts.first?.id, "host-abc-123")
        XCTAssertTrue(hosts.first?.fleetAuth == true)
        XCTAssertEqual(http.requests.first?.header("Authorization"), "Bearer relay-secret-xyz")
        XCTAssertTrue(http.requests.first?.head.hasPrefix("GET /api/hosts ") == true)

        do {
            _ = try await client.discoverHosts(relayURL: base, secret: "wrong-secret", tlsPin: nil)
            XCTFail("expected unauthorized error")
        } catch let err as RelayDiscoveryError {
            XCTAssertEqual(err, .unauthorized)
        }

        http.respond = { _ in (404, ["error": "host discovery disabled"]) }
        do {
            _ = try await client.discoverHosts(relayURL: base, secret: "relay-secret-xyz", tlsPin: nil)
            XCTFail("expected discoveryDisabled error")
        } catch let err as RelayDiscoveryError {
            XCTAssertEqual(err, .discoveryDisabled)
        }

        // Rejects plain HTTP when a TLS certificate pin is specified.
        do {
            _ = try await client.discoverHosts(relayURL: base, secret: "relay-secret-xyz", tlsPin: samplePin)
            XCTFail("expected invalidPin error for http:// with tlsPin")
        } catch let err as RelayDiscoveryError {
            XCTAssertEqual(err, .invalidPin)
        }
    }

    /// Pre-#55 `HostConfig` JSON without `relayID`, `relayHostID`, or `customName`
    /// continues to decode cleanly.
    @MainActor func testPre55HostConfigBackwardCompatibility() throws {
        let pre55JSON = """
        [
          {
            "id": "00000000-0000-0000-0000-000000000010",
            "name": "alice-laptop",
            "url": "https:\\/\\/relay.example.com:7443\\/h\\/host_12345678",
            "desktop": false,
            "tlsPin": "\(samplePin)"
          }
        ]
        """
        let decoded = HostConfig.decodeSaved(Data(pre55JSON.utf8))
        XCTAssertEqual(decoded.dropped, 0)
        XCTAssertEqual(decoded.hosts.count, 1)
        let host = try XCTUnwrap(decoded.hosts.first)
        XCTAssertEqual(host.name, "alice-laptop")
        XCTAssertNil(host.relayID)
        XCTAssertNil(host.relayHostID)
        XCTAssertNil(host.customName)
    }

    /// Real Keychain round-trip for `KeychainRelaySecrets` when Keychain is available.
    @MainActor func testKeychainRelaySecretsRoundTrip() throws {
        let kc = KeychainRelaySecrets()
        let id = UUID()
        let status = kc.store("relay-secret-1", for: id)
        if status == errSecMissingEntitlement {
            throw XCTSkip("no Keychain in this test process (\(status))")
        }
        XCTAssertEqual(status, errSecSuccess)
        XCTAssertEqual(kc.secret(for: id), "relay-secret-1")
        kc.set("relay-secret-2", for: id)
        XCTAssertEqual(kc.secret(for: id), "relay-secret-2")
        kc.set(nil, for: id)
        XCTAssertNil(kc.secret(for: id))
    }

    /// NET-011: private-LAN IP filtering, candidate sanitization, and DiscoveredRelayHost
    /// / LANDirectHint decoding.
    @MainActor func testLANDirectIPFilteringAndDecoding() throws {
        for ok in ["10.0.13.20", "172.16.0.1", "172.31.255.254", "192.168.1.5", "fd12:3456::1", "::ffff:10.0.8.20"] {
            XCTAssertTrue(RelayFleet.isPrivateLANIP(ok), "expected private LAN IP: \(ok)")
        }
        for bad in ["127.0.0.1", "::1", "169.254.1.2", "fe80::1", "100.64.0.1", "100.111.76.128", "198.18.0.1", "203.0.113.10", "example.com", ""] {
            XCTAssertFalse(RelayFleet.isPrivateLANIP(bad), "expected rejected IP: \(bad)")
        }

        let sanitized = RelayFleet.sanitizeLANAddrs([
            "203.0.113.10:7443",
            "127.0.0.1:7443",
            "198.18.0.1:7443",
            "100.111.76.128:7443",
            "10.0.13.20:0",
            "10.0.13.20:7443",
            "10.0.13.20:7443",
            "[::ffff:10.0.8.20]:7443",
            "192.168.1.10:7443",
            "[fd12::1]:7443",
            "172.16.0.5:7443",
        ])
        XCTAssertEqual(sanitized, [
            "10.0.13.20:7443",
            "10.0.8.20:7443",
            "192.168.1.10:7443",
            "[fd12::1]:7443",
        ])

        let pin = samplePin
        let json = """
        {
          "id": "host_lan_0001",
          "name": "dev-box-1",
          "path": "/h/host_lan_0001",
          "fleet_auth": true,
          "lan_addrs": ["10.0.13.20:7443", "203.0.113.9:7443"],
          "direct_pin": "\(pin)"
        }
        """
        let decoded = try JSONDecoder().decode(DiscoveredRelayHost.self, from: Data(json.utf8))
        XCTAssertEqual(decoded.lanAddrs, ["10.0.13.20:7443"])
        XCTAssertEqual(decoded.directPin, pin)

        let badPinJSON = """
        {
          "id": "host_lan_0002",
          "name": "bad-pin",
          "lan_addrs": ["10.0.13.20:7443"],
          "direct_pin": "not-a-valid-pin"
        }
        """
        let decodedBad = try JSONDecoder().decode(DiscoveredRelayHost.self, from: Data(badPinJSON.utf8))
        XCTAssertTrue(decodedBad.lanAddrs.isEmpty)
        XCTAssertNil(decodedBad.directPin)

        let hintJSON = """
        {"ok": true, "lan_addrs": ["10.0.13.20:7443", "198.18.0.1:7443"], "direct_pin": "\(pin)"}
        """
        let hint = try JSONDecoder().decode(LANDirectHint.self, from: Data(hintJSON.utf8))
        XCTAssertEqual(hint.lanAddrs, ["10.0.13.20:7443"])
        XCTAssertEqual(hint.directPin, pin)
    }

    /// NET-011: Host uses LAN direct when probe succeeds and falls back to relay when off-LAN.
    @MainActor func testHostLANDirectUpgradeAndFallback() async throws {
        let relayPin = samplePin
        let directPin = PinnedTLSSession.sha256Pin(of: Data("host-lan-cert-der".utf8))
        let relayURL = try XCTUnwrap(URL(string: "https://relay.example.com:7443/h/host_lan_0001"))
        let lanURL = try XCTUnwrap(URL(string: "https://10.0.13.20:7443"))

        let host = Host(HostConfig(
            name: "dev-box-1",
            url: relayURL,
            lanToken: "fleet-token-123",
            tlsPin: relayPin
        ))
        defer { host.stop() }
        XCTAssertTrue(host.isRelayHost)
        XCTAssertEqual(host.activeBaseURL, relayURL)
        XCTAssertEqual(host.activeTLSPin, relayPin)
        XCTAssertEqual(host.activeRoute, .relay)

        // Probe returns nil (off-LAN): activeBaseURL stays on relay.
        host.probeLAN = { _, _, _ in nil }
        host.updateLANHint(addrs: ["10.0.13.20:7443"], pin: directPin)
        host.promoteToLANIfReachable()
        try? await Task.sleep(for: .milliseconds(30))
        XCTAssertEqual(host.activeBaseURL, relayURL)
        XCTAssertEqual(host.activeTLSPin, relayPin)

        // Probe succeeds (on-LAN): promoteToLANIfReachable switches activeBaseURL & activeTLSPin to LAN direct.
        host.probeLAN = { _, _, _ in lanURL }
        host.promoteToLANIfReachable()
        try? await Task.sleep(for: .milliseconds(50))
        XCTAssertEqual(host.activeBaseURL, lanURL)
        XCTAssertEqual(host.activeTLSPin, directPin)
        XCTAssertEqual(host.activeRoute, .lanDirect("10.0.13.20:7443"))
    }
}
