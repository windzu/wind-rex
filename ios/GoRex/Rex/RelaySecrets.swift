import Foundation
import Security

/// Shared secrets for saved Relay Fleet configs (RelayConfig.secret), by relay:
/// the Keychain in the app, a dictionary in tests. The relays list in
/// UserDefaults never holds a secret.
protocol RelaySecrets: AnyObject {
    func secret(for relay: UUID) -> String?
    /// Keeps the relay's secret; nil forgets it.
    func set(_ secret: String?, for relay: UUID)
}

/// Relay shared secrets in the Keychain, a generic password per relay: service
/// wiki.wind.windrex.relay-secret, account the relay's id.
final class KeychainRelaySecrets: RelaySecrets {
    static let service = "wiki.wind.windrex.relay-secret"

    private func item(_ relay: UUID) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: Self.service,
         kSecAttrAccount as String: relay.uuidString.lowercased()]
    }

    func secret(for relay: UUID) -> String? {
        var q = item(relay)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
        return String(data: d, encoding: .utf8)
    }

    func set(_ secret: String?, for relay: UUID) {
        store(secret, for: relay)
    }

    /// Replaces the relay's item; returns the status of the last Keychain call,
    /// for a test to tell a simulator without a Keychain.
    @discardableResult
    func store(_ secret: String?, for relay: UUID) -> OSStatus {
        let deleted = SecItemDelete(item(relay) as CFDictionary)
        guard let secret, !secret.isEmpty else { return deleted == errSecItemNotFound ? errSecSuccess : deleted }
        var add = item(relay)
        add[kSecValueData as String] = Data(secret.utf8)
        // Auto-sync runs on foreground resume after first unlock; the secret
        // stays on this device only.
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        return SecItemAdd(add as CFDictionary, nil)
    }
}
