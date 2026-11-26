import Foundation

/// Resolves a scanned pairing QR payload (tailcat, direct TLS/relay, or LAN)
/// into a single outcome shared by the empty-state home scanner and Remote Hosts.
enum HostPairing {
    enum Outcome: Equatable {
        case pairTailcat(HostConfig)
        case existing(HostConfig)
        case added(HostConfig)
        case invalid(String)
    }

    static let invalidMessage = String(
        localized: "Not a WindRex pairing code. Scan the WindRex QR code shown in Settings › Server on your Mac."
    )

    /// Resolves `code` against existing `hosts` without mutating any store.
    static func resolve(
        _ code: String,
        existing hosts: [HostConfig],
        transportForURL: (URL) -> HostConfig.Transport? = { _ in nil }
    ) -> Outcome {
        if let c = HostConfig(tailcat: code) {
            if let addr = c.tailcat?.address,
               let existing = hosts.first(where: { $0.tailcat?.address == addr }) {
                return .existing(existing)
            }
            return .pairTailcat(c)
        }
        guard var c = HostConfig(pairing: code) else {
            return .invalid(invalidMessage)
        }
        if let existing = hosts.first(where: { $0.url == c.url }) {
            var updated = existing
            updated.lanToken = c.lanToken ?? existing.lanToken
            updated.tlsPin = c.tlsPin ?? existing.tlsPin
            return .existing(updated)
        }
        c.transport = transportForURL(c.url)
        return .added(c)
    }
}
