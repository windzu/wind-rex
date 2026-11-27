import CryptoKit
import Foundation
import Network

/// Cross-platform Relay Fleet protocol helpers (Issue #55 / NET-010, NET-011):
/// deterministic per-host HMAC-SHA256 token derivation, `#r=` Fleet QR parsing,
/// authenticated `GET /api/hosts` discovery with optional TLS leaf pinning, and
/// private-LAN Direct TLS hint validation.
enum RelayFleet {
    nonisolated static let tokenDomainPrefix = "windrex-fleet-v1:"
    nonisolated static let maxDiscoverBodyBytes = 1 << 20
    nonisolated static let maxLANAddrs = 4

    /// Reports whether `host` is a literal routable private LAN IP:
    /// RFC1918 IPv4 (`10/8`, `172.16/12`, `192.168/16`) or IPv6 ULA (`fc00::/7`).
    /// Rejects loopback, link-local, CGNAT (`100.64/10`), proxy TUN (`198.18/15`),
    /// public IPs, and hostnames.
    nonisolated static func isPrivateLANIP(_ host: String) -> Bool {
        let trimmed = host.trimmingCharacters(in: .whitespacesAndNewlines)
            .trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        guard !trimmed.isEmpty else { return false }
        if let v4 = IPv4Address(trimmed) {
            return isRFC1918(v4)
        }
        if let v6 = IPv6Address(trimmed) {
            if let mapped = v6.asIPv4 {
                return isRFC1918(mapped)
            }
            let bytes = [UInt8](v6.rawValue)
            return bytes.count == 16 && (bytes[0] & 0xfe) == 0xfc
        }
        return false
    }

    nonisolated private static func isRFC1918(_ v4: IPv4Address) -> Bool {
        let b = [UInt8](v4.rawValue)
        guard b.count == 4 else { return false }
        if b[0] == 10 { return true }
        if b[0] == 172 && (b[1] >= 16 && b[1] <= 31) { return true }
        if b[0] == 192 && b[1] == 168 { return true }
        return false
    }

    /// Validates, normalizes, deduplicates, and caps `host:port` private LAN
    /// Direct TLS candidates to `maxLANAddrs`.
    nonisolated static func sanitizeLANAddrs(_ raw: [String]) -> [String] {
        guard !raw.isEmpty else { return [] }
        var seen = Set<String>()
        var out: [String] = []
        for item in raw {
            let trimmed = item.trimmingCharacters(in: .whitespacesAndNewlines)
            guard let (host, port) = splitHostPort(trimmed),
                  port >= 1 && port <= 65535,
                  isPrivateLANIP(host) else {
                continue
            }
            let norm: String
            if let v6 = IPv6Address(host), let mapped = v6.asIPv4 {
                norm = "\(mapped):\(port)"
            } else if host.contains(":") {
                norm = "[\(host)]:\(port)"
            } else {
                norm = "\(host):\(port)"
            }
            if seen.insert(norm).inserted {
                out.append(norm)
                if out.count >= maxLANAddrs { break }
            }
        }
        return out
    }

    nonisolated private static func splitHostPort(_ s: String) -> (String, Int)? {
        if s.hasPrefix("[") {
            guard let close = s.firstIndex(of: "]") else { return nil }
            let after = s.index(after: close)
            guard after < s.endIndex, s[after] == ":" else { return nil }
            let host = String(s[s.index(after: s.startIndex)..<close])
            guard let port = Int(s[s.index(after: after)...]) else { return nil }
            return (host, port)
        }
        guard let colon = s.lastIndex(of: ":"), s.firstIndex(of: ":") == colon else { return nil }
        let host = String(s[..<colon])
        guard let port = Int(s[s.index(after: colon)...]) else { return nil }
        return (host, port)
    }

    nonisolated static func lanURL(for addr: String) -> URL? {
        guard sanitizeLANAddrs([addr]).first == addr else { return nil }
        return URL(string: "https://\(addr)")
    }

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
    var lanAddrs: [String]
    var directPin: String?

    enum CodingKeys: String, CodingKey {
        case id, name, path
        case connectedAt = "connected_at"
        case fleetAuth = "fleet_auth"
        case lanAddrs = "lan_addrs"
        case directPin = "direct_pin"
    }

    init(
        id: String,
        name: String,
        path: String? = nil,
        connectedAt: String? = nil,
        fleetAuth: Bool = false,
        lanAddrs: [String] = [],
        directPin: String? = nil
    ) {
        self.id = id
        self.name = name
        self.path = path
        self.connectedAt = connectedAt
        self.fleetAuth = fleetAuth
        if let directPin, PinnedTLSSession.validPin(directPin) {
            let clean = RelayFleet.sanitizeLANAddrs(lanAddrs)
            self.lanAddrs = clean
            self.directPin = clean.isEmpty ? nil : directPin
        } else {
            self.lanAddrs = []
            self.directPin = nil
        }
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        path = try c.decodeIfPresent(String.self, forKey: .path)
        connectedAt = try c.decodeIfPresent(String.self, forKey: .connectedAt)
        // Older relays omit `fleet_auth`, `lan_addrs`, and `direct_pin`.
        fleetAuth = try c.decodeIfPresent(Bool.self, forKey: .fleetAuth) ?? false
        let rawAddrs = try c.decodeIfPresent([String].self, forKey: .lanAddrs) ?? []
        let rawPin = try c.decodeIfPresent(String.self, forKey: .directPin)?.trimmingCharacters(in: .whitespacesAndNewlines)
        if let rawPin, PinnedTLSSession.validPin(rawPin) {
            let clean = RelayFleet.sanitizeLANAddrs(rawAddrs)
            lanAddrs = clean
            directPin = clean.isEmpty ? nil : rawPin
        } else {
            lanAddrs = []
            directPin = nil
        }
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

/// Authenticated `GET /api/v1/lan-hint` payload and pinned LAN probe helpers
/// (docs/NET-011-lan-direct-upgrade.md).
nonisolated struct LANDirectHint: Decodable, Equatable, Sendable {
    var lanAddrs: [String]
    var directPin: String?

    enum CodingKeys: String, CodingKey {
        case lanAddrs = "lan_addrs"
        case directPin = "direct_pin"
    }

    init(lanAddrs: [String] = [], directPin: String? = nil) {
        if let directPin, PinnedTLSSession.validPin(directPin) {
            let clean = RelayFleet.sanitizeLANAddrs(lanAddrs)
            self.lanAddrs = clean
            self.directPin = clean.isEmpty ? nil : directPin
        } else {
            self.lanAddrs = []
            self.directPin = nil
        }
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let rawAddrs = try c.decodeIfPresent([String].self, forKey: .lanAddrs) ?? []
        let rawPin = try c.decodeIfPresent(String.self, forKey: .directPin)?.trimmingCharacters(in: .whitespacesAndNewlines)
        self.init(lanAddrs: rawAddrs, directPin: rawPin)
    }
}

enum LANDirectProbe {
    nonisolated static let hintPath = "/api/v1/lan-hint"

    /// Fetches private-LAN Direct TLS hints from an authenticated relay or direct host.
    nonisolated static func fetchHint(baseURL: URL, auth: String?, tlsPin: String?, timeout: TimeInterval = 5) async -> LANDirectHint? {
        let session = await MainActor.run { PinnedTLSSession.session(pin: tlsPin, rejectRedirects: true) }
        defer {
            if session !== URLSession.shared {
                session.finishTasksAndInvalidate()
            }
        }
        var request = URLRequest(url: baseURL.apiURL(hintPath))
        request.httpMethod = "GET"
        request.timeoutInterval = timeout
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request = request.authorized(auth)

        guard let (data, response) = try? await session.data(for: request),
              let http = response as? HTTPURLResponse,
              http.statusCode == 200,
              data.count <= 64 * 1024,
              let hint = try? JSONDecoder().decode(LANDirectHint.self, from: data),
              !hint.lanAddrs.isEmpty,
              hint.directPin != nil else {
            return nil
        }
        return hint
    }

    /// Races authenticated pinned `GET /api/v1/lan-hint` across sanitized private-LAN
    /// candidates and returns the first `https://<ip>:<port>` URL answering HTTP 200.
    nonisolated static func probeFirstReachable(
        addrs: [String],
        pin: String,
        auth: String?,
        timeout: TimeInterval = 0.4
    ) async -> URL? {
        guard PinnedTLSSession.validPin(pin) else { return nil }
        let urls = RelayFleet.sanitizeLANAddrs(addrs).compactMap(RelayFleet.lanURL(for:))
        guard !urls.isEmpty else { return nil }

        return await withTaskGroup(of: URL?.self) { group in
            for url in urls {
                group.addTask {
                    await probeOne(url: url, pin: pin, auth: auth, timeout: timeout)
                }
            }
            for await result in group {
                if let winner = result {
                    group.cancelAll()
                    return winner
                }
            }
            return nil
        }
    }

    nonisolated private static func probeOne(url: URL, pin: String, auth: String?, timeout: TimeInterval) async -> URL? {
        let session = await MainActor.run { PinnedTLSSession.session(pin: pin, rejectRedirects: true) }
        defer {
            if session !== URLSession.shared {
                session.finishTasksAndInvalidate()
            }
        }
        var request = URLRequest(url: url.apiURL(hintPath))
        request.httpMethod = "GET"
        request.timeoutInterval = timeout
        request.cachePolicy = .reloadIgnoringLocalCacheData
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request = request.authorized(auth)

        guard let (_, response) = try? await session.data(for: request),
              let http = response as? HTTPURLResponse,
              http.statusCode == 200 else {
            return nil
        }
        return url
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
