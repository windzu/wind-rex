import Foundation

/// Resolves a scanned pairing QR payload (direct TLS/relay or LAN)
/// into a single outcome shared by the empty-state home scanner and Remote Hosts.
enum HostPairing {
    enum Outcome: Equatable {
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
        existing hosts: [HostConfig]
    ) -> Outcome {
        guard let c = HostConfig(pairing: code) else {
            return .invalid(invalidMessage)
        }
        if let existing = hosts.first(where: { $0.url == c.url }) {
            var updated = existing
            updated.lanToken = c.lanToken ?? existing.lanToken
            updated.tlsPin = c.tlsPin ?? existing.tlsPin
            return .existing(updated)
        }
        return .added(c)
    }
}
