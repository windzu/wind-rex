import XCTest

nonisolated final class HostPairingTests: XCTestCase {
    @MainActor func testLegacyTailcatCodesRejectedWithGuidance() {
        for code in ["rexgo://tailcat?a=tcExampleAddr123&n=alice-laptop", "windrex://tailcat?a=tcExampleAddr123&n=alice-laptop"] {
            let result = HostPairing.resolve(code, existing: [])
            guard case .invalid(let why) = result else {
                return XCTFail("expected invalid for \(code), got \(result)")
            }
            XCTAssertFalse(why.isEmpty)
        }
    }

    @MainActor func testDirectTLSRelayAddsAndUpdatesTokenAndPin() {
        let pin1 = PinnedTLSSession.sha256Pin(of: Data("cert-v1".utf8))
        let pin2 = PinnedTLSSession.sha256Pin(of: Data("cert-v2".utf8))
        let code = "https://relay.example.com:7443/h/host_12345678/#k=secret1&p=\(pin1)&n=alice-laptop"
        let first = HostPairing.resolve(code, existing: [])
        guard case .added(let config) = first else {
            return XCTFail("expected added, got \(first)")
        }
        XCTAssertEqual(config.name, "alice-laptop")
        XCTAssertEqual(config.lanToken, "secret1")
        XCTAssertEqual(config.tlsPin, pin1)

        let rotated = "https://relay.example.com:7443/h/host_12345678/#k=secret2&p=\(pin2)&n=alice-laptop"
        let second = HostPairing.resolve(rotated, existing: [config])
        guard case .existing(let updated) = second else {
            return XCTFail("expected existing updated, got \(second)")
        }
        XCTAssertEqual(updated.id, config.id)
        XCTAssertEqual(updated.lanToken, "secret2")
        XCTAssertEqual(updated.tlsPin, pin2)
    }

    @MainActor func testInvalidCodeReturnsGuidance() {
        let result = HostPairing.resolve("not a qr code", existing: [])
        guard case .invalid(let why) = result else {
            return XCTFail("expected invalid, got \(result)")
        }
        XCTAssertFalse(why.isEmpty)
    }
}
