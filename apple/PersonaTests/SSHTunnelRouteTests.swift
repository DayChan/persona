import Foundation
import Testing
@testable import Persona

/// The app's side of the SSH tunnel (docs/operations/network.md「经 SSH 隧道连 hub」): a routed hub keeps its own address and only
/// the URLs put on the wire change; pairings saved before keep decoding; picking the way asks the hub's own addresses
/// first. The tunnel itself is tested against a real sshd in apple/Packages/PersonaSSH.
@Suite(.serialized)
struct SSHTunnelRouteTests {
    let hub = URL(string: "https://hub.example.com/persona")!
    let tunnel = URL(string: "http://127.0.0.1:52001")!

    @Test func routedRequestsGoThroughTheTunnel() {
        // Only this suite's hub: other suites route their own hubs meanwhile (suites interleave).
        defer { HubTransport.route(hub, via: nil) }
        let api = APIClient(config: ServerConfig(url: hub, token: "t"))
        #expect(api.url("/api/v1/machines").absoluteString == "https://hub.example.com/persona/api/v1/machines")
        HubTransport.route(hub, via: tunnel)
        // The path under the hub's address and the query stay; scheme, host and port are the tunnel's.
        #expect(api.url("/api/v1/sessions", query: ["machine": "mac"]).absoluteString
                == "http://127.0.0.1:52001/persona/api/v1/sessions?machine=mac")
        // The probe that asks whether the hub answers at its own address never goes through the tunnel.
        #expect(api.url("/api/v1/health", direct: true).host() == "hub.example.com")
        // A WebSocket stays a WebSocket (plain, the tunnel is TCP).
        let ws = URL(string: "wss://hub.example.com/persona/api/v1/ws?features=sessions_delta")!
        #expect(HubTransport.resolve(ws, hub: hub).absoluteString == "ws://127.0.0.1:52001/persona/api/v1/ws?features=sessions_delta")
        // Another hub is left alone; a trailing slash is the same hub.
        let other = URL(string: "http://hub-b.example.com:18660")!
        #expect(HubTransport.resolve(other.appending(path: "x"), hub: other).host() == "hub-b.example.com")
        #expect(HubTransport.via(URL(string: "https://hub.example.com/persona/")!) == tunnel)
        HubTransport.route(hub, via: nil)
        #expect(api.url("/api/v1/machines").host() == "hub.example.com")
    }

    @Test func closingOneTunnelLeavesTheRouteAnotherOneSet() {
        // A hub with two ways through: the one taking over routes first, the one it replaces closes afterwards
        // (AppModel.only), and must not take the new route with it.
        let other = URL(string: "http://127.0.0.1:52002")!
        defer { HubTransport.route(hub, via: nil) }
        HubTransport.route(hub, via: tunnel)
        HubTransport.route(hub, via: other)
        HubTransport.unroute(hub, from: tunnel)
        #expect(HubTransport.via(hub) == other)
        HubTransport.unroute(hub, from: other)
        #expect(HubTransport.via(hub) == nil)
    }

    @Test func pairingsSavedBeforeKeepDecoding() throws {
        // A registry written before the tunnel: no ssh, the same hub.
        let old = #"{"hubs":[{"id":"h1","config":{"url":"http://hub-b.example.com:18660","token":"t"},"group":"","addedAt":0}],"groupNames":{}}"#
        let r = try JSONDecoder().decode(HubRegistry.self, from: Data(old.utf8))
        #expect(r.hubs.first?.ssh == nil)
        // A route round-trips; a sparse one fills in the defaults.
        var hub = r.hubs[0]
        hub.ssh = SSHRoute(host: "ssh.example.com", user: "me:hub", hostKey: "SHA256:abc", hostKeyAlgorithm: "ED25519", always: true)
        let back = try JSONDecoder().decode(PairedHub.self, from: JSONEncoder().encode(hub))
        #expect(back.ssh == hub.ssh)
        let sparse = try JSONDecoder().decode(SSHRoute.self, from: Data(#"{"host":"h","user":"u"}"#.utf8))
        #expect(sparse.port == 22 && sparse.targetHost == "127.0.0.1" && sparse.targetPort == 18660 && !sparse.always)
        // Not used before its server's key is confirmed.
        #expect(!sparse.isUsable)
        #expect(SSHRoute(host: "h", user: "u", hostKey: "SHA256:x").isUsable)
        #expect(SSHRoute(host: "h", port: 2222, user: "u").label == "u@h:2222")
    }

    @Test func aSingleAddressIsAskedBeforeTheTunnel() async {
        let c = HubRoute.Candidate(config: ServerConfig(url: hub, token: "t"))
        // `pick` takes a single place without asking; `answering` asks, and says when nobody answers.
        let asked = Counter()
        #expect(await HubRoute.pick([c], probe: { _ in await asked.add(); return false }) == c)
        #expect(await asked.n == 0)
        #expect(await HubRoute.answering([c], probe: { _ in await asked.add(); return false }) == nil)
        #expect(await asked.n == 1)
        #expect(await HubRoute.answering([c], probe: { _ in true }) == c)
        // Several, none answering: `pick` still hands the socket the first, `answering` nothing.
        let d = HubRoute.Candidate(config: ServerConfig(url: tunnel, token: "t"))
        #expect(await HubRoute.pick([c, d], probe: { _ in false }) == c)
        #expect(await HubRoute.answering([c, d], probe: { _ in false }) == nil)
        #expect(await HubRoute.answering([c, d], probe: { $0.url == self.tunnel }) == d)
    }

    @Test func connectionDetailsSayTheWay() {
        let now = Date.now
        let through = ConnectionDetails.facts(phase: .online, link: .connected, nextTry: nil, lastOnline: now, rtt: nil, outbox: 0,
                                              now: now, ssh: .init(server: "me@ssh.example.com", routed: true))
        #expect(through.first == "线路：经 SSH 隧道（me@ssh.example.com）")
        let down = ConnectionDetails.facts(phase: .connecting, link: .connecting, nextTry: nil, lastOnline: nil, rtt: nil, outbox: 0,
                                           now: now, ssh: .init(server: "me@ssh.example.com", routed: false, problem: "SSH 服务器的指纹变了，已拒绝连接"))
        #expect(down.prefix(2) == ["线路：直连", "SSH 隧道：SSH 服务器的指纹变了，已拒绝连接"])
        // No tunnel set up: the lines are what they were.
        #expect(ConnectionDetails.facts(phase: .online, link: .connected, nextTry: nil, lastOnline: now, rtt: nil, outbox: 0, now: now).isEmpty)
    }
}

private actor Counter {
    var n = 0
    func add() { n += 1 }
}
