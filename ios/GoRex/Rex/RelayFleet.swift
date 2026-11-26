import CryptoKit
import Foundation

/// Cross-platform Relay Fleet protocol helpers (Issue #55 / NET-010):
/// deterministic per-host HMAC-SHA256 token derivation, `#r=` Fleet QR parsing,
/// and authenticated `GET /api/hosts` discovery with optional TLS leaf pinning.
enum RelayFleet {
    nonisolated static let tokenDomainPrefix = "windrex-fleet-v1:"
    nonisolated static let maxDiscoverBodyBytes = 1 << 20

    /// Derives the per-host Bearer token accepted by `transportRelay` hosts
    /// that trust this relay (`relay_trust: true`):
    /// `base64url_no_pad(HMAC-SHA256(key = relaySecret, msg = "windrex-fleet-v1:" + hostID))`.
    nonisolated static func deriveFleetToken(relaySecret: String, hostID: String) -> String {
        let key = SymmetricKey(data: Data(relaySecret.utf8))
        let message = Data("\(tokenDomainPrefix)\(hostID)".utf8)
        let mac = HMAC<SHA256>.authenticationCode(for: message, using: key)
        return Data(mac).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    /// Matches Go `directrelay.ValidHostID` (`^[A-Za-z0-9_-]{8,64}$`).
    nonisolated static func validHostID(_ id: String) -> Bool {
        id.count >= 8 && id.count <= 64 && id.wholeMatch(of: /[A-Za-z0-9_-]+/) != nil
    }

    /// Validates a relay secret carried in `#r=`: non-empty, bounded length,
    /// and free of whitespace or control characters.
    nonisolated static func validRelaySecret(_ s: String) -> Bool {
        guard !s.isEmpty, s.count <= 512 else { return false }
        return !s.unicodeScalars.contains {
            $0.properties.isWhitespace || CharacterSet.controlCharacters.contains($0)
        }
    }
}

/// Parsed Relay Fleet QR / URL payload:
/// `https://<relay-host>[:port]/#r=<relay_secret>[&p=<tls_pin_b64url>][&n=<relay_label>]`
nonisolated struct RelayPairingPayload: Equatable, Hashable, Sendable {
    var url: URL
    var secret: String
    var tlsPin: String?
    var label: String
    var hasExplicitLabel: Bool

    init(url: URL, secret: String, tlsPin: String? = nil, label: String, hasExplicitLabel: Bool = true) {
        self.url = url
        self.secret = secret
        self.tlsPin = tlsPin
        self.label = label
        self.hasExplicitLabel = hasExplicitLabel
    }

    init?(pairing code: String) {
        guard var c = URLComponents(string: code.trimmingCharacters(in: .whitespacesAndNewlines)),
              let scheme = c.scheme?.lowercased(), ["http", "https"].contains(scheme),
              let host = c.host, !host.isEmpty,
              c.user == nil, c.password == nil, c.query == nil,
              c.path.isEmpty || c.path == "/",
              let rawFragment = c.percentEncodedFragment, !rawFragment.isEmpty
        else { return nil }

        let parts = rawFragment.split(separator: "&", omittingEmptySubsequences: false)
        guard !parts.isEmpty else { return nil }

        var secret: String?
        var pin: String?
        var explicitLabel: String?

        for part in parts {
            guard let eq = part.firstIndex(of: "=") else { return nil }
            let rawName = String(part[..<eq])
            let rawValue = String(part[part.index(after: eq)...])
            guard !rawName.isEmpty, !rawValue.isEmpty else { return nil }
            switch rawName {
            case "r":
                guard secret == nil,
                      let decoded = rawValue.removingPercentEncoding,
                      RelayFleet.validRelaySecret(decoded) else { return nil }
                secret = decoded
            case "p":
                guard pin == nil,
                      let decoded = rawValue.removingPercentEncoding,
                      PinnedTLSSession.validPin(decoded) else { return nil }
                pin = decoded
            case "n":
                guard explicitLabel == nil,
                      let decoded = rawValue.replacingOccurrences(of: "+", with: " ").removingPercentEncoding else { return nil }
                let trimmed = decoded.trimmingCharacters(in: .whitespacesAndNewlines)
                if !trimmed.isEmpty { explicitLabel = trimmed }
            default:
                return nil
            }
        }

        guard let secret else { return nil }
        // A TLS leaf certificate pin cannot be verified over plain HTTP.
        if pin != nil && scheme != "https" { return nil }
        c.scheme = scheme
        c.path = ""
        c.fragment = nil
        guard let normalizedURL = c.url else { return nil }

        let fallbackHost = host.trimmingCharacters(in: ["[", "]"])
        self.url = normalizedURL
        self.secret = secret
        self.tlsPin = pin
        self.label = explicitLabel ?? fallbackHost
        self.hasExplicitLabel = explicitLabel != nil
    }
}

/// A saved stateless `windrex-relay` server used for automatic fleet discovery.
/// Metadata lives in `UserDefaults("relays")`; `secret` lives ONLY in Keychain
/// (`RelaySecrets`) and is intentionally excluded from `CodingKeys`.
nonisolated struct RelayConfig: Codable, Identifiable, Hashable, Sendable {
    var id = UUID()
    var label: String
    var url: URL
    var tlsPin: String? = nil
    var autoSync: Bool = true
    /// Host IDs explicitly deleted by the user so future syncs do not re-add them.
    var ignoredHostIDs: [String] = []
    var lastSyncAt: Date? = nil
    /// Relay shared secret; stored in Keychain (`RelaySecrets`), NEVER in UserDefaults.
    var secret: String? = nil

    enum CodingKeys: String, CodingKey {
        case id, label, url, tlsPin, autoSync, ignoredHostIDs, lastSyncAt
    }

    init(
        id: UUID = UUID(),
        label: String,
        url: URL,
        tlsPin: String? = nil,
        autoSync: Bool = true,
        ignoredHostIDs: [String] = [],
        lastSyncAt: Date? = nil,
        secret: String? = nil
    ) {
        self.id = id
        self.label = label
        self.url = url
        self.tlsPin = tlsPin
        self.autoSync = autoSync
        self.ignoredHostIDs = ignoredHostIDs
        self.lastSyncAt = lastSyncAt
        self.secret = secret
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        label = try c.decode(String.self, forKey: .label)
        url = try c.decode(URL.self, forKey: .url)
        tlsPin = try c.decodeIfPresent(String.self, forKey: .tlsPin)
        autoSync = try c.decodeIfPresent(Bool.self, forKey: .autoSync) ?? true
        ignoredHostIDs = try c.decodeIfPresent([String].self, forKey: .ignoredHostIDs) ?? []
        lastSyncAt = try c.decodeIfPresent(Date.self, forKey: .lastSyncAt)
        secret = nil
    }
}

/// One online host entry returned by `GET /api/hosts`.
nonisolated struct DiscoveredRelayHost: Codable, Equatable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
    var path: String?
    var connectedAt: String?
    var fleetAuth: Bool

    enum CodingKeys: String, CodingKey {
        case id, name, path
        case connectedAt = "connected_at"
        case fleetAuth = "fleet_auth"
    }

    init(id: String, name: String, path: String? = nil, connectedAt: String? = nil, fleetAuth: Bool = false) {
        self.id = id
        self.name = name
        self.path = path
        self.connectedAt = connectedAt
        self.fleetAuth = fleetAuth
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        path = try c.decodeIfPresent(String.self, forKey: .path)
        connectedAt = try c.decodeIfPresent(String.self, forKey: .connectedAt)
        // Older relays omit `fleet_auth`; treat missing as false.
        fleetAuth = try c.decodeIfPresent(Bool.self, forKey: .fleetAuth) ?? false
    }

    /// Clean display name falling back to `id` when empty.
    var displayName: String {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? id : trimmed
    }

    /// Builds `<relay-base>/h/<id>` for a valid host identifier.
    func hostURL(base: URL) -> URL? {
        guard RelayFleet.validHostID(id) else { return nil }
        return base.apiURL("/h/\(id)")
    }
}

/// Failure modes when querying a relay's `GET /api/hosts` endpoint.
enum RelayDiscoveryError: LocalizedError, Equatable {
    case missingSecret
    case invalidPin
    case unauthorized
    case discoveryDisabled
    case unexpectedRedirect
    case responseTooLarge
    case httpStatus(Int, String?)
    case invalidResponse

    var errorDescription: String? {
        switch self {
        case .missingSecret:
            return String(localized: "Relay secret is missing. Scan the Relay Fleet QR code again.")
        case .invalidPin:
            return String(localized: "Invalid relay TLS certificate pin.")
        case .unauthorized:
            return String(localized: "Relay rejected the secret. Scan the updated Relay Fleet QR code.")
        case .discoveryDisabled:
            return String(localized: "Host discovery is disabled on this relay server.")
        case .unexpectedRedirect:
            return String(localized: "Relay attempted an unexpected redirect.")
        case .responseTooLarge:
            return String(localized: "Relay host directory response was too large.")
        case .httpStatus(let code, let detail):
            if let detail, !detail.isEmpty {
                return String(localized: "Relay returned HTTP \(code): \(detail)")
            }
            return String(localized: "Relay returned HTTP \(code).")
        case .invalidResponse:
            return String(localized: "Relay returned an invalid host list.")
        }
    }
}

/// Abstraction over `GET /api/hosts` for unit testing with mocked responses.
protocol RelayDiscoveryClient: AnyObject {
    func discoverHosts(relayURL: URL, secret: String, tlsPin: String?) async throws -> [DiscoveredRelayHost]
}

/// Live `URLSession` implementation of `GET <relay-base>/api/hosts` with
/// Bearer authentication, optional leaf TLS SHA-256 pinning, and redirect refusal.
final class URLSessionRelayDiscoveryClient: RelayDiscoveryClient {
    private nonisolated struct HostsResponse: Decodable {
        var hosts: [DiscoveredRelayHost]?
    }

    private nonisolated struct ErrorResponse: Decodable {
        var error: String?
    }

    func discoverHosts(relayURL: URL, secret: String, tlsPin: String?) async throws -> [DiscoveredRelayHost] {
        guard !secret.isEmpty else { throw RelayDiscoveryError.missingSecret }
        if let tlsPin, !tlsPin.isEmpty {
            guard relayURL.scheme?.lowercased() == "https", PinnedTLSSession.validPin(tlsPin) else {
                throw RelayDiscoveryError.invalidPin
            }
        }

        let session = PinnedTLSSession.session(pin: tlsPin, rejectRedirects: true)
        defer {
            if session !== URLSession.shared {
                session.finishTasksAndInvalidate()
            }
        }

        var request = URLRequest(url: relayURL.apiURL("/api/hosts"))
        request.httpMethod = "GET"
        request.timeoutInterval = 10
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request = request.authorized(secret)

        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw RelayDiscoveryError.invalidResponse
        }
        guard data.count <= RelayFleet.maxDiscoverBodyBytes else {
            throw RelayDiscoveryError.responseTooLarge
        }

        switch http.statusCode {
        case 200:
            guard let decoded = try? JSONDecoder().decode(HostsResponse.self, from: data) else {
                throw RelayDiscoveryError.invalidResponse
            }
            return decoded.hosts ?? []
        case 300..<400:
            throw RelayDiscoveryError.unexpectedRedirect
        case 401, 403:
            throw RelayDiscoveryError.unauthorized
        case 404:
            throw RelayDiscoveryError.discoveryDisabled
        default:
            let detail = (try? JSONDecoder().decode(ErrorResponse.self, from: data))?.error
            throw RelayDiscoveryError.httpStatus(http.statusCode, detail)
        }
    }
}
