import XCTest

nonisolated final class TailnetActivationPolicyTests: XCTestCase {
    private static let samplePin = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

    @MainActor private func directTLSHost() throws -> HostConfig {
        try XCTUnwrap(HostConfig(pairing: "https://203.0.113.10:7443/#k=directToken123&p=\(Self.samplePin)&n=alice-laptop"))
    }

    @MainActor private func relayHost() throws -> HostConfig {
        try XCTUnwrap(HostConfig(pairing: "https://relay.example.com:7443/h/host12345678/#k=relayToken123&p=\(Self.samplePin)&n=bob-desktop"))
    }

    @MainActor private func lanHost() throws -> HostConfig {
        try XCTUnwrap(HostConfig(pairing: "http://192.168.1.2:7297/#k=lanToken123"))
    }

    @MainActor private func tailcatHost() throws -> HostConfig {
        try XCTUnwrap(HostConfig(tailcat: "windrex://tailcat?a=tcABC123&n=carol-server"))
    }

    @MainActor private func tailnetHost() throws -> HostConfig {
        var h = try XCTUnwrap(HostConfig(pairing: "https://alice-laptop.example.ts.net"))
        h.transport = .tailnet
        return h
    }

    /// Direct TLS, self-hosted relay, LAN, and Demo hosts with Built-in Tailscale
    /// off require neither Tailscale nor tailcat, keeping the engine dormant.
    @MainActor func testDirectRelayLANHostsWithTailscaleOffNeedNoEngine() throws {
        XCTAssertFalse(TailnetActivationPolicy.needsTailcat(hosts: []))
        XCTAssertFalse(TailnetActivationPolicy.needsTailscale(hosts: [], tailnetEnabled: false))
        XCTAssertFalse(TailnetActivationPolicy.needsEngine(hosts: [], tailnetEnabled: false))

        let systemHosts = [try directTLSHost(), try relayHost(), try lanHost(), Demo.config()]
        XCTAssertFalse(TailnetActivationPolicy.needsTailcat(hosts: systemHosts))
        XCTAssertFalse(TailnetActivationPolicy.needsTailscale(hosts: systemHosts, tailnetEnabled: false))
        XCTAssertFalse(TailnetActivationPolicy.needsEngine(hosts: systemHosts, tailnetEnabled: false))
    }

    /// Adding a tailcat host activates tailcat and the engine even while
    /// Built-in Tailscale is off.
    @MainActor func testAddingTailcatHostActivatesEngine() throws {
        var hosts = [try directTLSHost(), try relayHost()]
        XCTAssertFalse(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: false))

        var tailcat = try tailcatHost()
        hosts.append(tailcat)
        XCTAssertTrue(TailnetActivationPolicy.needsTailcat(hosts: hosts))
        XCTAssertFalse(TailnetActivationPolicy.needsTailscale(hosts: hosts, tailnetEnabled: false))
        XCTAssertTrue(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: false))

        // Tailcat takes precedence over transport (matching HostConfig.reach).
        tailcat.transport = .tailnet
        XCTAssertTrue(TailnetActivationPolicy.needsTailcat(hosts: [tailcat]))
        XCTAssertFalse(TailnetActivationPolicy.needsTailscale(hosts: [tailcat], tailnetEnabled: false))
        XCTAssertTrue(TailnetActivationPolicy.needsEngine(hosts: [tailcat], tailnetEnabled: false))
    }

    /// Adding a `.tailnet` host requires Tailscale and the engine.
    @MainActor func testAddingTailnetHostActivatesTailscale() throws {
        var hosts = [try directTLSHost(), try lanHost()]
        XCTAssertFalse(TailnetActivationPolicy.needsTailscale(hosts: hosts, tailnetEnabled: false))
        XCTAssertFalse(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: false))

        hosts.append(try tailnetHost())
        XCTAssertFalse(TailnetActivationPolicy.needsTailcat(hosts: hosts))
        XCTAssertTrue(TailnetActivationPolicy.needsTailscale(hosts: hosts, tailnetEnabled: false))
        XCTAssertTrue(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: false))
        XCTAssertTrue(TailnetActivationPolicy.needsTailscale(hosts: hosts, tailnetEnabled: true))
        XCTAssertTrue(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: true))
    }

    /// Enabling Built-in Tailscale in Settings activates the engine even with
    /// zero configured hosts.
    @MainActor func testEnablingBuiltInTailscaleWithZeroHostsActivatesEngine() {
        XCTAssertFalse(TailnetActivationPolicy.needsTailcat(hosts: []))
        XCTAssertTrue(TailnetActivationPolicy.needsTailscale(hosts: [], tailnetEnabled: true))
        XCTAssertTrue(TailnetActivationPolicy.needsEngine(hosts: [], tailnetEnabled: true))
    }

    /// Removing the last tailcat or `.tailnet` host while Built-in Tailscale is
    /// off returns `needsEngine` to false.
    @MainActor func testRemovingLastTailcatOrTailnetHostDeactivatesEngine() throws {
        let direct = try directTLSHost()
        let relay = try relayHost()
        let tailcat = try tailcatHost()
        let tailnet = try tailnetHost()

        var hosts = [direct, relay, tailcat, tailnet]
        XCTAssertTrue(TailnetActivationPolicy.needsTailcat(hosts: hosts))
        XCTAssertTrue(TailnetActivationPolicy.needsTailscale(hosts: hosts, tailnetEnabled: false))
        XCTAssertTrue(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: false))

        hosts.removeAll { $0.id == tailcat.id }
        XCTAssertFalse(TailnetActivationPolicy.needsTailcat(hosts: hosts))
        XCTAssertTrue(TailnetActivationPolicy.needsTailscale(hosts: hosts, tailnetEnabled: false))
        XCTAssertTrue(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: false))

        hosts.removeAll { $0.id == tailnet.id }
        XCTAssertFalse(TailnetActivationPolicy.needsTailcat(hosts: hosts))
        XCTAssertFalse(TailnetActivationPolicy.needsTailscale(hosts: hosts, tailnetEnabled: false))
        XCTAssertFalse(TailnetActivationPolicy.needsEngine(hosts: hosts, tailnetEnabled: false))
    }

    /// Returning from a suspension with only direct/relay/LAN hosts and Built-in
    /// Tailscale off skips TailnetEngine.resume() completely, while still
    /// signalling foreground readiness for open terminals.
    @MainActor func testForegroundSkipsTailnetResumeWhenOnlySystemHostsAndTailscaleOff() async throws {
        let d = UserDefaults.standard
        let (savedHosts, savedOn, savedTailcatHosts, savedTailcatProxy) = (
            d.data(forKey: "hosts"),
            d.object(forKey: TailnetSettings.onKey),
            Host.tailcatHosts,
            Host.tailcatProxy
        )
        defer {
            d.set(savedHosts, forKey: "hosts")
            d.set(savedOn, forKey: TailnetSettings.onKey)
            Host.tailcatHosts = savedTailcatHosts
            Host.tailcatProxy = savedTailcatProxy
        }
        d.removeObject(forKey: "hosts")
        d.set(false, forKey: TailnetSettings.onKey)
        Host.tailcatHosts = { _ in }
        Host.tailcatProxy = { nil }

        let rex = try FakeRex()
        defer { rex.stop() }
        rex.respond = { m, _ in m == "session.bootstrap" ? ["attach_all": ["sessions": []]] : [:] }

        let store = HostStore()
        let direct = HostConfig(name: "alice-laptop", url: try await rex.url())
        store.add(direct)
        defer { store.hosts.forEach { $0.stop() } }
        await until("direct host online") { store.host(direct.id)?.status == .online }

        let beforeReady = try XCTUnwrap(store.host(direct.id)).foregroundReady
        var resumeCalls = 0
        await store.foreground(suspended: true, wasBackground: true) {
            resumeCalls += 1
            return false
        }
        XCTAssertEqual(resumeCalls, 0, "system-only hosts with Built-in Tailscale off must not wake TailnetEngine")
        XCTAssertEqual(try XCTUnwrap(store.host(direct.id)).foregroundReady, beforeReady + 1)

        // Adding a tailcat host wakes TailnetEngine on demand.
        let tailcat = try tailcatHost()
        store.add(tailcat)
        await store.foreground(suspended: true, wasBackground: true) {
            resumeCalls += 1
            return false
        }
        XCTAssertEqual(resumeCalls, 1, "tailcat host must wake TailnetEngine after suspension")

        // Removing the last tailcat host stops subsequent foreground wakeups.
        store.remove(tailcat.id)
        await store.foreground(suspended: true, wasBackground: true) {
            resumeCalls += 1
            return false
        }
        XCTAssertEqual(resumeCalls, 1, "removing last tailcat host must avoid subsequent TailnetEngine wakeups")

        // Enabling Built-in Tailscale in Settings wakes TailnetEngine even with no tailcat/tailnet hosts.
        d.set(true, forKey: TailnetSettings.onKey)
        await store.foreground(suspended: true, wasBackground: true) {
            resumeCalls += 1
            return true
        }
        XCTAssertEqual(resumeCalls, 2, "Built-in Tailscale toggle must wake TailnetEngine on demand")
    }
}
