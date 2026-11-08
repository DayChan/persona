import Foundation
import Network
import Testing
@testable import Persona

/// The SOCKS5 client the local proxy route dials with (docs/operations/network.md "Connecting to hub through a local
/// SOCKS5 proxy"): the handshake byte by byte (RFC 1928, RFC 1929), then on real connections to proxies started here
/// on the loopback: no login, the right login, a wrong password, a CONNECT the proxy refuses, a proxy that is not
/// there, one that is not SOCKS5, one that never answers.
@Suite(.serialized)
struct SOCKS5HandshakeTests {
    private let queue = DispatchQueue(label: "socks5-tests")

    /// Bytes put together from typed parts: array literals added up inside `#expect` take the type checker seconds
    /// (docs/lessons/ios.md, "Minimum compatible compiler").
    private func bytes(_ parts: [UInt8]...) -> [UInt8] { parts.flatMap { $0 } }
    private func utf8(_ s: String) -> [UInt8] { Array(s.utf8) }

    // MARK: The handshake, byte by byte

    @Test func theGreetingOffersALoginOnlyWhenThereIsOne() throws {
        #expect(try SOCKS5Handshake(host: "hub.test", port: 18660, login: nil).start() == .write([5, 1, 0], then: 2))
        #expect(try SOCKS5Handshake(host: "hub.test", port: 18660, login: ("me", "pw")).start() == .write([5, 2, 0, 2], then: 2))
    }

    @Test func theConnectNamesTheHubAsItIsWritten() throws {
        // A name goes to the proxy unresolved: its network's DNS knows the hub, this device's may not.
        let named = bytes([5, 1, 0, 3, 17], utf8("hub.internal.test"), [0x48, 0xE4])
        #expect(try SOCKS5Handshake.connectRequest(host: "hub.internal.test", port: 18660) == named)
        #expect(try SOCKS5Handshake.connectRequest(host: "192.0.2.5", port: 80) == [5, 1, 0, 1, 192, 0, 2, 5, 0, 80])
        let v6 = try SOCKS5Handshake.connectRequest(host: "[2001:db8::1]", port: 443)
        #expect(v6.prefix(4) == [5, 1, 0, 4] && v6.count == 4 + 16 + 2)
        #expect(Array(v6[4..<8]) == [0x20, 0x01, 0x0d, 0xb8] && v6[19] == 1 && Array(v6.suffix(2)) == [1, 0xBB])
        #expect(throws: SOCKS5Error.badHost) { try SOCKS5Handshake.connectRequest(host: " ", port: 80) }
        #expect(throws: SOCKS5Error.badHost) { try SOCKS5Handshake.connectRequest(host: String(repeating: "a", count: 256), port: 80) }
        #expect(throws: SOCKS5Error.badHost) { try SOCKS5Handshake(host: "hub.test", port: 0, login: nil) }
    }

    @Test func withoutALoginTheHandshakeGoesStraightToTheConnect() throws {
        var h = try SOCKS5Handshake(host: "192.0.2.5", port: 18660, login: nil)
        let connect: [UInt8] = [5, 1, 0, 1, 192, 0, 2, 5, 0x48, 0xE4]
        #expect(try h.read([5, 0]) == .write(connect, then: 4))
        // The reply's bound address is read to its end, whatever its kind, and not a byte more.
        #expect(try h.read([5, 0, 0, 1]) == .read(6))
        #expect(try h.read([127, 0, 0, 1, 0x04, 0x38]) == .done)
        var named = try SOCKS5Handshake(host: "192.0.2.5", port: 18660, login: nil)
        _ = try named.read([5, 0])
        #expect(try named.read([5, 0, 0, 3]) == .read(1))
        #expect(try named.read([4]) == .read(6))
        #expect(try named.read(bytes(utf8("prox"), [0, 1])) == .done)
        var v6 = try SOCKS5Handshake(host: "192.0.2.5", port: 18660, login: nil)
        _ = try v6.read([5, 0])
        #expect(try v6.read([5, 0, 0, 4]) == .read(18))
    }

    @Test func aLoginGoesOnlyWhenTheProxyPicksIt() throws {
        let login = bytes([1, 2], utf8("me"), [6], utf8("s3cret"))
        let connect = bytes([5, 1, 0, 3, 8], utf8("hub.test"), [0x48, 0xE4])
        var h = try SOCKS5Handshake(host: "hub.test", port: 18660, login: ("me", "s3cret"))
        #expect(try h.read([5, 2]) == .write(login, then: 2))
        #expect(try h.read([1, 0]) == .write(connect, then: 4))
        // A proxy that wants no login skips it.
        var open = try SOCKS5Handshake(host: "hub.test", port: 18660, login: ("me", "s3cret"))
        #expect(try open.read([5, 0]) == .write(connect, then: 4))
        // An empty password is sent as one; a login longer than SOCKS5 carries is refused before anything is sent.
        var empty = try SOCKS5Handshake(host: "hub.test", port: 18660, login: ("me", ""))
        #expect(try empty.read([5, 2]) == .write(bytes([1, 2], utf8("me"), [0]), then: 2))
        #expect(throws: SOCKS5Error.loginTooLong) {
            try SOCKS5Handshake(host: "hub.test", port: 18660, login: ("me", String(repeating: "x", count: 256)))
        }
    }

    @Test func whatTheProxySaysNoToIsNamed() throws {
        func after(_ login: (username: String, password: String)?, _ replies: [[UInt8]]) throws {
            var h = try SOCKS5Handshake(host: "hub.test", port: 18660, login: login)
            for r in replies { _ = try h.read(r) }
        }
        #expect(throws: SOCKS5Error.loginRejected) { try after(("me", "wrong"), [[5, 2], [1, 1]]) }
        #expect(throws: SOCKS5Error.noAcceptableMethod(loginOffered: false)) { try after(nil, [[5, 0xFF]]) }
        #expect(throws: SOCKS5Error.noAcceptableMethod(loginOffered: true)) { try after(("me", "pw"), [[5, 0xFF]]) }
        #expect(throws: SOCKS5Error.connectFailed(5)) { try after(nil, [[5, 0], [5, 5, 0, 1]]) }
        #expect(throws: SOCKS5Error.connectFailed(2)) { try after(("me", "pw"), [[5, 2], [1, 0], [5, 2, 0, 1]]) }
        // Not SOCKS5 at all: an HTTP proxy's "HTTP/1.1 400", a SOCKS4 one.
        #expect(throws: SOCKS5Error.notSOCKS5(0x48)) { try after(nil, [self.utf8("HT")]) }
        #expect(throws: SOCKS5Error.notSOCKS5(4)) { try after(nil, [[4, 0]]) }
        // A login picked when none was offered (mihomo answers so to everyone once it asks for one): asks for it.
        #expect(throws: SOCKS5Error.noAcceptableMethod(loginOffered: false)) { try after(nil, [[5, 2]]) }
        // An address type that does not exist, bytes after the end.
        #expect(throws: SOCKS5Error.malformed) { try after(nil, [[5, 0], [5, 0, 0, 9]]) }
        #expect(throws: SOCKS5Error.malformed) { try after(nil, [[5, 0], [5, 0, 0, 1], [0, 0, 0, 0, 0, 0], [0]]) }
        #expect(SOCKS5Error.reply(5) == "connection refused, 5")
        #expect(SOCKS5Error.reply(2) == "connection not allowed by ruleset, 2")
        #expect(SOCKS5Error.reply(42) == "unassigned reply, 42")
    }

    // MARK: On real connections

    /// Writes `text` on `c` and reads it back from the echo server behind the proxy.
    private func echoes(_ c: NWConnection, _ text: String) async -> Bool {
        await Wire.write(c, Array(text.utf8))
        guard let back = await Wire.read(c, text.utf8.count) else { return false }
        return String(decoding: back, as: UTF8.self) == text
    }

    @Test func throughAProxyWithoutALogin() async throws {
        let echo = try EchoServer(), proxy = try FakeSOCKS5()
        try await echo.start()
        try await proxy.start()
        defer { echo.stop(); proxy.stop() }
        let c = try await SOCKS5Dial.connect(host: "127.0.0.1", port: proxy.port,
                                             handshake: SOCKS5Handshake(host: "localhost", port: echo.port, login: nil), queue: queue)
        defer { c.cancel() }
        #expect(await echoes(c, "ping through the proxy"))
        #expect(proxy.requests == [FakeSOCKS5.Asked(host: "localhost", port: echo.port, username: nil)])
    }

    @Test func throughAProxyWithTheRightLogin() async throws {
        let echo = try EchoServer(), proxy = try FakeSOCKS5(login: ("me", "s3cret"))
        try await echo.start()
        try await proxy.start()
        defer { echo.stop(); proxy.stop() }
        let c = try await SOCKS5Dial.connect(host: "127.0.0.1", port: proxy.port,
                                             handshake: SOCKS5Handshake(host: "127.0.0.1", port: echo.port, login: ("me", "s3cret")), queue: queue)
        defer { c.cancel() }
        #expect(await echoes(c, "signed in"))
        #expect(proxy.requests.last?.username == "me")
    }

    @Test func aWrongPasswordIsTurnedDown() async throws {
        let proxy = try FakeSOCKS5(login: ("me", "s3cret"))
        try await proxy.start()
        defer { proxy.stop() }
        await #expect(throws: SOCKS5Error.loginRejected) {
            _ = try await SOCKS5Dial.connect(host: "127.0.0.1", port: proxy.port,
                                             handshake: SOCKS5Handshake(host: "127.0.0.1", port: 18660, login: ("me", "wrong")), queue: queue)
        }
        // No login at all where one is asked: said so, not "refused".
        await #expect(throws: SOCKS5Error.noAcceptableMethod(loginOffered: false)) {
            _ = try await SOCKS5Dial.connect(host: "127.0.0.1", port: proxy.port,
                                             handshake: SOCKS5Handshake(host: "127.0.0.1", port: 18660, login: nil), queue: queue)
        }
        #expect(proxy.requests.isEmpty) // never got as far as asking for the hub
    }

    @Test func aConnectTheProxyRefusesSaysWhy() async throws {
        let strict = try FakeSOCKS5(refuse: 2)
        try await strict.start()
        defer { strict.stop() }
        await #expect(throws: SOCKS5Error.connectFailed(2)) {
            _ = try await SOCKS5Dial.connect(host: "127.0.0.1", port: strict.port,
                                             handshake: SOCKS5Handshake(host: "hub.internal.test", port: 18660, login: nil), queue: queue)
        }
        // A hub address where nothing listens: the proxy's own connection is refused, and it says so.
        let proxy = try FakeSOCKS5()
        try await proxy.start()
        defer { proxy.stop() }
        let nothing = try await LoopbackServer.closedPort()
        await #expect(throws: SOCKS5Error.connectFailed(5)) {
            _ = try await SOCKS5Dial.connect(host: "127.0.0.1", port: proxy.port,
                                             handshake: SOCKS5Handshake(host: "127.0.0.1", port: nothing, login: nil), queue: queue)
        }
    }

    @Test func aProxyThatIsNotThereFailsAtOnce() async throws {
        let nothing = try await LoopbackServer.closedPort()
        let start = ContinuousClock.now
        do {
            _ = try await SOCKS5Dial.connect(host: "127.0.0.1", port: nothing,
                                             handshake: SOCKS5Handshake(host: "127.0.0.1", port: 18660, login: nil), queue: queue)
            Issue.record("dialled a port nothing listens on")
        } catch let SOCKS5Error.unreachable(why) {
            #expect(!why.isEmpty)
        }
        // Refused is an answer: no waiting for the dial's time limit.
        #expect(ContinuousClock.now - start < .seconds(5))
    }

    @Test func aServerThatIsNotSOCKS5OrSilent() async throws {
        let http = try ScriptedServer(reply: Array("HTTP/1.1 400 Bad Request\r\n\r\n".utf8))
        let silent = try ScriptedServer(reply: nil)
        try await http.start()
        try await silent.start()
        defer { http.stop(); silent.stop() }
        await #expect(throws: SOCKS5Error.notSOCKS5(0x48)) {
            _ = try await SOCKS5Dial.connect(host: "127.0.0.1", port: http.port,
                                             handshake: SOCKS5Handshake(host: "127.0.0.1", port: 18660, login: nil), queue: queue)
        }
        let start = ContinuousClock.now
        await #expect(throws: SOCKS5Error.timedOut) {
            _ = try await SOCKS5Dial.connect(host: "127.0.0.1", port: silent.port,
                                             handshake: SOCKS5Handshake(host: "127.0.0.1", port: 18660, login: nil), queue: queue, timeout: 0.5)
        }
        #expect(ContinuousClock.now - start < .seconds(5))
    }
}
