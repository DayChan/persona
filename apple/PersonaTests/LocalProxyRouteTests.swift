import Foundation
import Testing
@testable import Persona

/// The local proxy route (docs/operations/network.md "Connecting to hub through a local SOCKS5 proxy"): what is kept with
/// the pairing and what it defaults to, and the forward itself, through a SOCKS5 proxy to a hub started here on the
/// loopback. Picking the way is `HubWay`'s, shared with the SSH and in-app tunnels (HubWayTests).
@Suite(.serialized)
struct LocalProxyRouteTests {
    // MARK: What is kept

    @Test func aSavedProxyKeepsItsLoginAndDefaults() throws {
        let sparse = try JSONDecoder().decode(LocalProxyRoute.self, from: Data(#"{"port":1080}"#.utf8))
        #expect(sparse == LocalProxyRoute(port: 1080))
        #expect(sparse.host == "127.0.0.1" && sparse.login == nil && sparse.targetHost == nil && sparse.targetPort == nil && !sparse.always)
        let full = LocalProxyRoute(host: "::1", port: 7891, username: " me ", password: "s3cret", targetHost: "hub.internal.test",
                                   targetPort: 8080, always: true)
        #expect(try JSONDecoder().decode(LocalProxyRoute.self, from: JSONEncoder().encode(full)) == full)
        #expect(full.label == "[::1]:7891")
        #expect(full.login?.username == "me" && full.login?.password == "s3cret")
        // Pairings saved before the proxy decode as they were; one with a proxy keeps it, password and all.
        let old = #"{"hubs":[{"id":"h1","config":{"url":"http://hub-b.example.com:18660","token":"t"},"group":"","addedAt":0}],"groupNames":{}}"#
        var hub = try JSONDecoder().decode(HubRegistry.self, from: Data(old.utf8)).hubs[0]
        #expect(hub.localProxy == nil)
        hub.localProxy = full
        #expect(try JSONDecoder().decode(PairedHub.self, from: JSONEncoder().encode(hub)).localProxy == full)
        #expect(!LocalProxyRoute(port: 0).isUsable)
        #expect(!LocalProxyRoute(port: 1080, targetPort: 70000).isUsable)
        #expect(!LocalProxyRoute(host: " ", port: 1080).isUsable)
    }

    @Test func theHubsAddressDefaultsToThePairedOne() {
        let route = LocalProxyRoute(port: 1080)
        let lan = URL(string: "http://192.0.2.20:18660")!
        #expect(route.targetHost(for: lan) == "192.0.2.20" && route.targetPort(for: lan) == 18660)
        #expect(route.targetPort(for: URL(string: "http://hub.lan")!) == 80)
        // An https address: the forward carries plain HTTP, so the hub's HTTP port.
        let tls = URL(string: "https://hub.example.com/persona")!
        #expect(route.targetHost(for: tls) == "hub.example.com" && route.targetPort(for: tls) == LocalProxyRoute.hubHTTPPort)
        #expect(route.targetHost(for: URL(string: "http://[2001:db8::5]:18660")!) == "2001:db8::5")
        let set = LocalProxyRoute(port: 1080, targetHost: " 198.51.100.5 ", targetPort: 8080)
        #expect(set.targetHost(for: lan) == "198.51.100.5" && set.targetPort(for: lan) == 8080)
        #expect(set.dial(for: lan) == LocalProxyForward.Dial(proxyHost: "127.0.0.1", proxyPort: 1080, targetHost: "198.51.100.5", targetPort: 8080))
        #expect(LocalProxyRoute(port: 1080, targetHost: "2001:db8::5").dial(for: lan).target == "[2001:db8::5]:18660")
        // The proxy's host as lists write it, brackets and all.
        #expect(LocalProxyRoute(host: "[::1]", port: 1080).dial(for: lan).proxyHost == "::1")
    }

    @Test func aProxyElsewhereIsToldApart() {
        for host in ["127.0.0.1", "127.1.2.3", "localhost", "::1", "[::1]", " LOCALHOST "] {
            #expect(LocalProxyRoute(host: host, port: 1080).isOnThisDevice, "\(host)")
        }
        for host in ["192.0.2.10", "proxy.example.com", "127.example.com", "1270.0.0.1", "::2"] {
            #expect(!LocalProxyRoute(host: host, port: 1080).isOnThisDevice, "\(host)")
        }
    }

    @Test func theHealthCheckKeepsTheHubsPath() {
        let local = URL(string: "http://127.0.0.1:52001")!
        #expect(LocalProxies.healthURL(hub: URL(string: "https://hub.example.com/persona/")!, via: local)?.absoluteString
                == "http://127.0.0.1:52001/persona/api/v1/health")
        #expect(LocalProxies.healthURL(hub: URL(string: "http://192.0.2.20:18660")!, via: local)?.absoluteString
                == "http://127.0.0.1:52001/api/v1/health")
    }

    // MARK: The forward

    private func hubAndProxy(login: (username: String, password: String)? = nil, answer: FakeHealthHub.Answer = .hub(id: "hub-test"),
                             eager: Bool = false) async throws -> (FakeHealthHub, FakeSOCKS5) {
        let hub = try FakeHealthHub(answer), proxy = try FakeSOCKS5(login: login, eager: eager)
        try await hub.start()
        try await proxy.start()
        return (hub, proxy)
    }

    /// The paired address is one nothing answers at: only the proxy can get there.
    private let paired = URL(string: "http://hub-behind-proxy.invalid:18660")!

    @MainActor @Test func testConnectionReachesTheHubThroughTheProxy() async throws {
        let (hub, proxy) = try await hubAndProxy(login: ("me", "s3cret"))
        defer { hub.stop(); proxy.stop() }
        let route = LocalProxyRoute(port: proxy.port, username: "me", password: "s3cret", targetHost: "127.0.0.1", targetPort: hub.port)
        try await LocalProxies().verify(route, hub: paired, hubID: "hub-test")
        #expect(proxy.requests.first == FakeSOCKS5.Asked(host: "127.0.0.1", port: hub.port, username: "me"))
        var wrong = route
        wrong.password = "wrong"
        await #expect(throws: SOCKS5Error.loginRejected) { try await LocalProxies().verify(wrong, hub: self.paired) }
    }

    /// Test Connection keeps only a way that works afterwards: what answered there is said, each in its own words.
    @MainActor @Test func testConnectionSaysWhatAnswered() async throws {
        func check(_ answer: FakeHealthHub.Answer, hubID: String? = "hub-test") async throws -> LocalProxyCheck? {
            let (hub, proxy) = try await hubAndProxy(answer: answer)
            defer { hub.stop(); proxy.stop() }
            do {
                try await LocalProxies().verify(LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: hub.port), hub: paired, hubID: hubID)
                return nil
            } catch let e as LocalProxyCheck {
                #expect(e.errorDescription?.contains("127.0.0.1:\(hub.port)") == true)
                return e
            }
        }
        #expect(try await check(.hub(id: "hub-test")) == nil)
        // The hub sees the proxy as the internet: it answers health, then refuses every plain HTTP request.
        #expect(try await check(.internet(id: "hub-test")).map { "\($0)" }?.hasPrefix("seenAsInternet") == true)
        // A redirect is an answer, not followed off the proxy (followed, it would pass here).
        #expect(try await check(.redirect).map { "\($0)" }?.hasPrefix("notAHub") == true)
        #expect(try await check(.notAHub).map { "\($0)" }?.hasPrefix("notAHub") == true)
        // Another hub: refused while the paired hub's id is known, taken while it is not.
        #expect(try await check(.hub(id: "another-hub")).map { "\($0)" }?.hasPrefix("otherHub") == true)
        #expect(try await check(.hub(id: "another-hub"), hubID: nil) == nil)
    }

    /// Proxies like ssh -D, mihomo and Xray say yes to the CONNECT before they try the address: when nothing listens
    /// there, that is said, not "not a hub".
    @MainActor @Test func aProxyThatSaysYesFirst() async throws {
        let proxy = try FakeSOCKS5(eager: true)
        try await proxy.start()
        defer { proxy.stop() }
        let nothing = try await LoopbackServer.closedPort()
        await #expect(throws: LocalProxyCheck.noAnswer("127.0.0.1:\(nothing)")) {
            try await LocalProxies().verify(LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: nothing), hub: self.paired)
        }
    }

    @MainActor @Test func theHubsRequestsGoThroughTheForward() async throws {
        let (hub, proxy) = try await hubAndProxy()
        defer { hub.stop(); proxy.stop() }
        let proxies = LocalProxies()
        let route = LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: hub.port)
        let local = try await proxies.open("h1", route: route, hub: paired)
        defer { proxies.close("h1") }
        #expect(HubTransport.via(paired) == local && proxies.routed.contains("h1") && proxies.states["h1"] == .up)
        // The app's own requests to the hub's address, as APIClient makes them: they land on the hub behind the proxy.
        let api = APIClient(config: ServerConfig(url: paired, token: "t"))
        let url = api.url("/api/v1/health")
        #expect(url.host() == "127.0.0.1" && url.port == local.port)
        let (data, resp) = try await APIClient.uncachedSession.data(from: url)
        #expect((resp as? HTTPURLResponse)?.statusCode == 200 && String(decoding: data, as: UTF8.self).contains(#""hub_id":"hub-test""#))
        // A WebSocket to the hub goes to the same place.
        let ws = HubTransport.resolve(URL(string: "ws://hub-behind-proxy.invalid:18660/api/v1/ws")!, hub: paired)
        #expect(ws.absoluteString == "ws://127.0.0.1:\(local.port!)/api/v1/ws")
        proxies.close("h1")
        #expect(HubTransport.via(paired) == nil && !proxies.routed.contains("h1"))
        #expect((try? await APIClient.uncachedSession.data(from: url)) == nil) // the listener is gone
    }

    @MainActor @Test func aProxyThatStoppedWorkingIsNotTaken() async throws {
        let (hub, proxy) = try await hubAndProxy()
        defer { hub.stop() }
        let proxies = LocalProxies()
        let route = LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: hub.port)
        let local = try await proxies.open("h1", route: route, hub: paired)
        defer { proxies.close("h1") }
        // Picked again while it is still there: the same forward, the same address.
        #expect(try await proxies.open("h1", route: route, hub: paired) == local)
        proxy.stop() // the app serving it quit
        try await Task.sleep(for: .milliseconds(100))
        do {
            _ = try await proxies.open("h1", route: route, hub: paired)
            Issue.record("took a proxy that is not there")
        } catch SOCKS5Error.unreachable {}
        #expect(HubTransport.via(paired) == nil && !proxies.routed.contains("h1"))
        guard case .down? = proxies.states["h1"] else {
            Issue.record("no reason kept: \(String(describing: proxies.states["h1"]))")
            return
        }
    }

    /// Settings saved while a start with the old ones is still under way: the new start takes its place at once, and
    /// the old one, failing later, leaves the new route alone.
    @MainActor @Test func newSettingsReplaceAStartUnderWay() async throws {
        let (hub, proxy) = try await hubAndProxy()
        let slow = try ScriptedServer(reply: Array("HTTP/1.1 400 Bad Request\r\n\r\n".utf8), after: .milliseconds(500))
        try await slow.start()
        defer { hub.stop(); proxy.stop(); slow.stop() }
        let proxies = LocalProxies()
        defer { proxies.close("h1") }
        let old = Task { @MainActor in
            try await proxies.open("h1", route: LocalProxyRoute(port: slow.port, targetHost: "127.0.0.1", targetPort: hub.port), hub: self.paired)
        }
        try await Task.sleep(for: .milliseconds(100))
        let start = ContinuousClock.now
        let local = try await proxies.open("h1", route: LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: hub.port), hub: paired)
        #expect(ContinuousClock.now - start < .milliseconds(450)) // did not wait for the old one
        _ = try? await old.value
        try await Task.sleep(for: .milliseconds(100))
        #expect(HubTransport.via(paired) == local && proxies.routed.contains("h1"))
    }

    /// A connection through the forward ends cleanly both ways: its writing ended, it still gets the answer; the
    /// forward stopping cuts one still open; none is kept afterwards.
    @Test func connectionsThroughTheForwardEnd() async throws {
        let echo = try EchoServer(), proxy = try FakeSOCKS5()
        try await echo.start()
        try await proxy.start()
        defer { echo.stop(); proxy.stop() }
        let forward = LocalProxyForward(dial: .init(proxyHost: "127.0.0.1", proxyPort: proxy.port, targetHost: "127.0.0.1", targetPort: echo.port))
        let port = try await forward.listen(preferring: 0)
        let queue = DispatchQueue(label: "forward-ends")
        // Half closed: the client is done writing, the answer still comes back, then the end.
        let a = try #require(await Wire.dial("127.0.0.1", port, queue: queue))
        await Wire.write(a, Array("last words".utf8))
        a.send(content: nil, contentContext: .finalMessage, isComplete: true, completion: .idempotent)
        #expect(await Wire.read(a, 10).map { String(decoding: $0, as: UTF8.self) } == "last words")
        #expect(await Wire.readSome(a) == nil)
        a.cancel()
        for _ in 0..<50 where forward.openConnections > 0 { try await Task.sleep(for: .milliseconds(20)) }
        #expect(forward.openConnections == 0) // both ways ended: gone
        // Still open when the forward stops: cut.
        let b = try #require(await Wire.dial("127.0.0.1", port, queue: queue))
        await Wire.write(b, Array("ping".utf8))
        #expect(await Wire.read(b, 4) != nil)
        #expect(forward.openConnections == 1)
        forward.stop()
        #expect(await Wire.readSome(b) == nil)
        #expect(forward.openConnections == 0)
        b.cancel()
    }

    @MainActor @Test func theDirectWayIsAskedEveryRecheckUntilItAnswers() async throws {
        let (hub, proxy) = try await hubAndProxy()
        defer { hub.stop(); proxy.stop() }
        let proxies = LocalProxies()
        _ = try await proxies.open("h1", route: LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: hub.port), hub: paired)
        defer { proxies.close("h1") }
        // 120 s scaled down: the hub's own address answers from the third ask on.
        var asked = 0
        var moved = false
        let start = ContinuousClock.now
        proxies.watchDirect("h1", every: .milliseconds(100), answers: { asked += 1; return asked >= 3 }, moveBack: { moved = true })
        for _ in 0..<100 where !moved { try await Task.sleep(for: .milliseconds(20)) }
        #expect(moved && asked == 3)
        #expect(ContinuousClock.now - start >= .milliseconds(300))
        // Closed meanwhile (the group moved on): the watch ends without moving anything.
        var late = false
        proxies.watchDirect("h1", every: .milliseconds(50), answers: { true }, moveBack: { late = true })
        proxies.close("h1")
        try await Task.sleep(for: .milliseconds(200))
        #expect(!late)
    }

    // MARK: Picking the way, in the app (AppModel.tunnelRoute carrying out HubWay's steps)

    @MainActor private func model(_ hub: PairedHub) -> AppModel {
        let model = AppModel()
        model.registry = HubRegistry(hubs: [hub])
        return model
    }

    @MainActor @Test func theLocalProxyIsAWayToTheHub() {
        var hub = PairedHub(id: "rt-kinds", config: ServerConfig(url: URL(string: "http://rt-kinds.invalid:18660")!, token: "t"))
        hub.ssh = SSHRoute(host: "ssh.example.com", user: "me", hostKey: "SHA256:x")
        hub.localProxy = LocalProxyRoute(port: 1080)
        // The SSH tunnel first, then the local proxy (no in-app tunnel core in this build).
        #expect(model(hub).hubTunnels(hub.group).map(\.kind) == [.ssh, .localProxy])
        hub.ssh = nil
        #expect(model(hub).hubTunnels(hub.group).map(\.kind) == [.localProxy])
    }

    /// A hub only the proxy reaches: the socket goes to the hub's own config, routed through the forward, and the
    /// direct way is asked again every recheck. With Always Use, it is not.
    @MainActor @Test func aHubOnlyTheProxyReachesGoesThroughIt() async throws {
        let (fake, proxy) = try await hubAndProxy()
        defer { fake.stop(); proxy.stop() }
        for always in [false, true] {
            let id = always ? "rt-always" : "rt-fallback"
            var hub = PairedHub(id: id, config: ServerConfig(url: URL(string: "http://\(id).invalid:18660")!, token: "t"))
            hub.localProxy = LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: fake.port, always: always)
            defer { LocalProxies.shared.close(id) }
            #expect(await model(hub).tunnelRoute(hub.group) == hub.config)
            #expect(HubTransport.via(hub.config.url)?.host() == "127.0.0.1" && LocalProxies.shared.routed.contains(id))
            #expect(LocalProxies.shared.watchesDirect(id) == !always, "always: \(always)")
        }
    }

    /// The hub answers directly: straight to it, and the proxy started meanwhile (it was the last way) closes.
    @MainActor @Test func theDirectWayClosesTheProxyStartedMeanwhile() async throws {
        let (fake, proxy) = try await hubAndProxy()
        defer { fake.stop(); proxy.stop() }
        var hub = PairedHub(id: "rt-direct", config: ServerConfig(url: URL(string: "http://127.0.0.1:\(fake.port)")!, token: "t"))
        hub.localProxy = LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: fake.port)
        defer { LocalProxies.shared.close(hub.id) }
        _ = try await LocalProxies.shared.open(hub.id, route: hub.localProxy!, hub: hub.config.url)
        #expect(LocalProxies.shared.recent.contains(hub.id))
        #expect(await model(hub).tunnelRoute(hub.group) == hub.config)
        #expect(HubTransport.via(hub.config.url) == nil && !LocalProxies.shared.routed.contains(hub.id))
    }

    /// The hub's other way took over (the SSH tunnel routed it after the proxy): the proxy closing leaves that route.
    @MainActor @Test func closingTheProxyLeavesTheWayThatTookOver() async throws {
        let (fake, proxy) = try await hubAndProxy()
        defer { fake.stop(); proxy.stop() }
        let url = URL(string: "http://rt-takeover.invalid:18660")!, ssh = URL(string: "http://127.0.0.1:52003")!
        defer { HubTransport.route(url, via: nil) }
        _ = try await LocalProxies.shared.open("rt-takeover", route: LocalProxyRoute(port: proxy.port, targetHost: "127.0.0.1", targetPort: fake.port), hub: url)
        HubTransport.route(url, via: ssh)
        LocalProxies.shared.close("rt-takeover")
        #expect(HubTransport.via(url) == ssh)
    }
}
