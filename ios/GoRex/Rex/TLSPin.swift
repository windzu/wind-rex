import CryptoKit
import Foundation
import Security

/// Creates a URLSession whose TLS server trust verifies that the leaf
/// certificate's SHA-256 digest matches a pinned base64url fingerprint.
/// Used for direct TLS hosts and self-hosted relays using self-signed
/// certificates (docs/NET-005-direct-relay.md).
enum PinnedTLSSession {
    /// Whether s is a valid base64url-encoded SHA-256 digest (43 chars, 32 bytes).
    nonisolated static func validPin(_ s: String) -> Bool {
        guard s.count == 43 else { return false }
        var b64 = s.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
        b64 += "="
        guard let d = Data(base64Encoded: b64) else { return false }
        return d.count == 32
    }

    /// Computes the base64url (unpadded) SHA-256 digest of a DER certificate.
    nonisolated static func sha256Pin(of der: Data) -> String {
        let digest = SHA256.hash(data: der)
        return Data(digest).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    /// Builds a URLSession that accepts only servers presenting a leaf
    /// certificate whose SHA-256 pin equals `pin`.
    static func session(pin: String) -> URLSession {
        URLSession(configuration: .default, delegate: Delegate(pin: pin), delegateQueue: nil)
    }

    nonisolated private final class Delegate: NSObject, URLSessionDelegate, @unchecked Sendable {
        let pin: String
        init(pin: String) { self.pin = pin }

        func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                        completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
            guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
                  let trust = challenge.protectionSpace.serverTrust,
                  let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
                  let leaf = chain.first else {
                completionHandler(.performDefaultHandling, nil)
                return
            }
            let der = SecCertificateCopyData(leaf) as Data
            if PinnedTLSSession.sha256Pin(of: der) == pin {
                completionHandler(.useCredential, URLCredential(trust: trust))
            } else {
                completionHandler(.cancelAuthenticationChallenge, nil)
            }
        }
    }
}
