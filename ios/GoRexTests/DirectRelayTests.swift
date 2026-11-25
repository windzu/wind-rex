import Foundation
import XCTest

/// Tests direct TLS and stateless relay pairing, path-preserving URLs, TLS pin
/// validation, foreground liveness probes, and pipelined ordered writes
/// (docs/NET-005-direct-relay.md).
nonisolated final class DirectRelayTests: XCTestCase {
    private var samplePin: String { PinnedTLSSession.sha256Pin(of: Data("sample-cert-der".utf8)) }

    @MainActor func testDirectAndRelayPairingCodes() throws {
        let pin = samplePin
        XCTAssertTrue(PinnedTLSSession.validPin(pin))
        XCTAssertFalse(PinnedTLSSession.validPin("short"))

        // Direct TLS with self-signed cert pin and display name.
        let direct = try XCTUnwrap(HostConfig(pairing: "https://203.0.113.10:7443/#k=tokABC_123&p=\(pin)&n=alice-laptop"))
        XCTAssertEqual(direct.url.absoluteString, "https://203.0.113.10:7443")
        XCTAssertEqual(direct.lanToken, "tokABC_123")
        XCTAssertEqual(direct.tlsPin, pin)
        XCTAssertEqual(direct.name, "alice-laptop")

        // Stateless relay URL preserves /h/<host_id> path prefix.
        let relay = try XCTUnwrap(HostConfig(pairing: "https://relay.example.com:7443/h/host_12345678/#k=tokXYZ&p=\(pin)&n=studio"))
        XCTAssertEqual(relay.url.absoluteString, "https://relay.example.com:7443/h/host_12345678")
        XCTAssertEqual(relay.lanToken, "tokXYZ")
        XCTAssertEqual(relay.tlsPin, pin)
        XCTAssertEqual(relay.name, "studio")

        // Relay path without pin (CA-signed TLS) works too.
        let caRelay = try XCTUnwrap(HostConfig(pairing: "https://relay.example.com/h/host_12345678/#k=tokXYZ"))
        XCTAssertEqual(caRelay.url.absoluteString, "https://relay.example.com/h/host_12345678")
        XCTAssertNil(caRelay.tlsPin)

        // Invalid pins, unknown fragment params, or invalid paths are rejected.
        XCTAssertNil(HostConfig(pairing: "https://203.0.113.10:7443/#k=tok&p=badpin"))
        XCTAssertNil(HostConfig(pairing: "https://203.0.113.10:7443/#p=\(pin)"))
        XCTAssertNil(HostConfig(pairing: "https://203.0.113.10:7443/#k=tok&x=1"))
        XCTAssertNil(HostConfig(pairing: "https://relay.example.com/h/short/#k=tok"))
        XCTAssertNil(HostConfig(pairing: "https://relay.example.com/other/host_12345678/#k=tok"))
    }

    @MainActor func testPathPreservingAPIAndRexURLs() throws {
        let directBase = try XCTUnwrap(URL(string: "https://203.0.113.10:7443"))
        XCTAssertEqual(directBase.rexURL("/api/control").absoluteString, "wss://203.0.113.10:7443/api/control")
        XCTAssertEqual(directBase.apiURL("/api/upload").absoluteString, "https://203.0.113.10:7443/api/upload")

        let relayBase = try XCTUnwrap(URL(string: "https://relay.example.com:7443/h/host_12345678"))
        XCTAssertEqual(relayBase.rexURL("/api/control").absoluteString, "wss://relay.example.com:7443/h/host_12345678/api/control")
        XCTAssertEqual(relayBase.rexURL("/api/data").absoluteString, "wss://relay.example.com:7443/h/host_12345678/api/data")
        XCTAssertEqual(relayBase.apiURL("/api/download").absoluteString, "https://relay.example.com:7443/h/host_12345678/api/download")
    }

    @MainActor func testHostConfigDecodesWithoutTLSPin() throws {
        let legacyJSON = #"{"id":"00000000-0000-0000-0000-000000000001","name":"mac","url":"http://192.0.2.10:7297","desktop":false}"#
        let decoded = try JSONDecoder().decode(HostConfig.self, from: Data(legacyJSON.utf8))
        XCTAssertNil(decoded.tlsPin)
        XCTAssertEqual(decoded.url.absoluteString, "http://192.0.2.10:7297")
    }

    /// Returning to foreground keeps a responsive system connection open and
    /// immediately resumes a half-open (non-ponging) system connection with goingAway.
    @MainActor func testForegroundProbeKeepsLiveAndResumesDeadSystemHost() async throws {
        let liveRex = try FakeRex(pongs: true)
        defer { liveRex.stop() }
        liveRex.respond = { m, _ in m == "session.bootstrap" ? ["attach_all": ["sessions": []]] : [:] }
        let liveHost = Host(HostConfig(name: "live", url: try await liveRex.url()))
        liveHost.connect()
        defer { liveHost.stop() }
        await until("live online") { liveHost.status == .online }
        let liveConn = liveHost.control
        await liveHost.probeOrResume(timeout: .milliseconds(500))
        XCTAssertTrue(liveHost.control === liveConn, "live connection should remain intact")
        XCTAssertEqual(liveRex.controls.count, 1)

        let deadRex = try FakeRex(pongs: false)
        defer { deadRex.stop() }
        deadRex.respond = { m, _ in m == "session.bootstrap" ? ["attach_all": ["sessions": []]] : [:] }
        let deadHost = Host(HostConfig(name: "dead", url: try await deadRex.url()))
        deadHost.connect()
        defer { deadHost.stop() }
        await until("dead online") { deadHost.status == .online }
        let firstConn = deadHost.control
        await deadHost.probeOrResume(timeout: .milliseconds(250))
        await until("dead reconnected") { deadRex.controls.count >= 2 }
        XCTAssertFalse(deadHost.control === firstConn)
        XCTAssertEqual(deadRex.tokens.last ?? nil, "t1")
        XCTAssertEqual(deadRex.ends.first ?? nil, .protocolCode(.goingAway))
    }

    /// When the server advertises gorex.ordered_write, TerminalSession pipelines
    /// consecutive writes immediately in strict FIFO order without blocking one RTT per key.
    @MainActor func testPipelinedOrderedWritesDoNotWaitPerKeystroke() async throws {
        let rex = try FakeRex()
        defer { rex.stop() }
        rex.features = [TerminalSession.orderedWriteFeature]
        // Leave write calls unanswered while typing to prove later keystrokes
        // hit the wire without waiting for earlier responses.
        rex.respond = { m, _ in
            if m == "session.bootstrap" { return ["attach_all": ["sessions": []]] }
            if m == "com.superlogical.terminal.write" { return nil }
            return [:]
        }
        let host = Host(HostConfig(name: "fast", url: try await rex.url()))
        host.connect()
        defer { host.stop() }
        await until("online") { host.status == .online }

        let session = TerminalSession(host: host, session: "s", block: "b")
        defer { session.close() }
        for ch in ["a", "b", "c", "d"] {
            session.type(Data(ch.utf8))
        }
        await until("all four pipelined writes arrived") {
            rex.count("com.superlogical.terminal.write") == 4
        }
        let texts = rex.payloads(of: "com.superlogical.terminal.write").compactMap {
            ($0["args"] as? [String: Any])?["data"] as? String
        }
        XCTAssertEqual(texts, ["a", "b", "c", "d"])
    }
}
