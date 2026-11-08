import Foundation
import Network

// Loopback servers for the local proxy's tests (docs/operations/network.md "Connecting to hub through a local SOCKS5
// proxy"): a SOCKS5 proxy that really connects where it is asked (with or without a login, or refusing), a hub that
// answers its health check, an echo server, and servers that answer wrong or not at all. Each serves any number of
// connections, so a stray connection (a port probe) changes nothing the tests look at.

/// A listener on 127.0.0.1 (a free port) that hands each connection to `serve`.
final class LoopbackServer: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "loopback-server")
    private let lock = NSLock()
    private var connections: [NWConnection] = []

    init(serve: @escaping @Sendable (NWConnection) -> Void) throws {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] c in
            guard let self else { return }
            self.lock.withLock { self.connections.append(c) }
            c.start(queue: self.queue)
            serve(c)
        }
    }

    var port: Int { Int(listener.port?.rawValue ?? 0) }

    func start() async throws {
        listener.start(queue: queue)
        for _ in 0..<200 where port == 0 { try await Task.sleep(for: .milliseconds(10)) }
        if port == 0 { throw URLError(.cannotConnectToHost) }
    }

    /// The listener and every connection it took close.
    func stop() {
        listener.cancel()
        lock.withLock { connections }.forEach { $0.cancel() }
    }

    /// A port nothing listens on: one taken and given back.
    static func closedPort() async throws -> Int {
        let s = try LoopbackServer { $0.cancel() }
        try await s.start()
        let p = s.port
        s.stop()
        try await Task.sleep(for: .milliseconds(50))
        return p
    }
}

/// Reads and writes on a test server's connection.
enum Wire {
    /// Exactly `n` bytes; nil when the connection ended first.
    static func read(_ c: NWConnection, _ n: Int) async -> [UInt8]? {
        await withCheckedContinuation { (cont: CheckedContinuation<[UInt8]?, Never>) in
            c.receive(minimumIncompleteLength: n, maximumLength: n) { data, _, _, _ in
                if let data, data.count == n { cont.resume(returning: [UInt8](data)) } else { cont.resume(returning: nil) }
            }
        }
    }

    /// Whatever comes next (up to 64 KB); nil at the end.
    static func readSome(_ c: NWConnection) async -> Data? {
        await withCheckedContinuation { (cont: CheckedContinuation<Data?, Never>) in
            c.receive(minimumIncompleteLength: 1, maximumLength: 64 << 10) { data, _, _, _ in
                cont.resume(returning: data.flatMap { $0.isEmpty ? nil : $0 })
            }
        }
    }

    static func write(_ c: NWConnection, _ bytes: some Sequence<UInt8>) async {
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            c.send(content: Data(bytes), completion: .contentProcessed { _ in cont.resume() })
        }
    }

    /// A TCP connection to 127.0.0.1:`port`, ready; nil when it does not come up.
    static func dial(_ host: String, _ port: Int, queue: DispatchQueue) async -> NWConnection? {
        guard let p = NWEndpoint.Port(rawValue: UInt16(port)) else { return nil }
        let c = NWConnection(host: NWEndpoint.Host(host), port: p, using: .tcp)
        let up = await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
            let once = Once()
            c.stateUpdateHandler = { state in
                switch state {
                case .ready: once.run { cont.resume(returning: true) }
                case .failed, .waiting, .cancelled: once.run { cont.resume(returning: false) }
                default: break
                }
            }
            c.start(queue: queue)
        }
        if !up { c.cancel() }
        return up ? c : nil
    }

    /// Copies one way until the end, then ends the other side's writing.
    static func pump(_ from: NWConnection, _ to: NWConnection) {
        from.receive(minimumIncompleteLength: 1, maximumLength: 64 << 10) { data, _, complete, error in
            if let data, !data.isEmpty {
                to.send(content: data, completion: .contentProcessed { e in
                    if e == nil, !complete { pump(from, to) } else { to.send(content: nil, contentContext: .finalMessage, isComplete: true, completion: .idempotent) }
                })
            } else if complete || error != nil {
                to.send(content: nil, contentContext: .finalMessage, isComplete: true, completion: .idempotent)
            } else {
                pump(from, to)
            }
        }
    }
}

final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func run(_ f: () -> Void) {
        let first = lock.withLock { () -> Bool in
            defer { done = true }
            return !done
        }
        if first { f() }
    }
}

/// A SOCKS5 proxy on the loopback (RFC 1928, with RFC 1929 when `login` is set): CONNECTs really go where they ask,
/// unless `refuse` says the reply code to answer with. `eager`: it says yes before it tries the address and closes
/// the connection when that fails, as ssh -D, mihomo and Xray do. What each handshake asked for is kept.
final class FakeSOCKS5: @unchecked Sendable {
    struct Asked: Equatable {
        var host: String
        var port: Int
        var username: String?
    }

    let login: (username: String, password: String)?
    let refuse: UInt8?
    let eager: Bool
    private let lock = NSLock()
    private var asked: [Asked] = []
    private let upstream = DispatchQueue(label: "fake-socks5-upstream")
    private var server: LoopbackServer!

    init(login: (username: String, password: String)? = nil, refuse: UInt8? = nil, eager: Bool = false) throws {
        self.login = login
        self.refuse = refuse
        self.eager = eager
        server = try LoopbackServer { [weak self] c in
            Task { await self?.serve(c) }
        }
    }

    var port: Int { server.port }
    var requests: [Asked] { lock.withLock { asked } }

    func start() async throws { try await server.start() }
    func stop() { server.stop() }

    private func serve(_ c: NWConnection) async {
        guard let head = await Wire.read(c, 2), head[0] == 5, let methods = await Wire.read(c, Int(head[1])) else { return c.cancel() }
        var username: String?
        if let login {
            guard methods.contains(2) else { await Wire.write(c, [5, 0xFF]); return c.cancel() }
            await Wire.write(c, [5, 2])
            guard let v = await Wire.read(c, 2), let user = await Wire.read(c, Int(v[1])), let pl = await Wire.read(c, 1),
                  let pass = await Wire.read(c, Int(pl[0])) else { return c.cancel() }
            let ok = String(decoding: user, as: UTF8.self) == login.username && String(decoding: pass, as: UTF8.self) == login.password
            await Wire.write(c, [1, ok ? 0 : 1])
            guard ok else { return c.cancel() }
            username = login.username
        } else {
            guard methods.contains(0) else { await Wire.write(c, [5, 0xFF]); return c.cancel() }
            await Wire.write(c, [5, 0])
        }
        guard let req = await Wire.read(c, 4), req[0] == 5, req[1] == 1 else { return c.cancel() }
        let host: String
        switch req[3] {
        case 1:
            guard let a = await Wire.read(c, 4) else { return c.cancel() }
            host = a.map(String.init).joined(separator: ".")
        case 3:
            guard let n = await Wire.read(c, 1), let name = await Wire.read(c, Int(n[0])) else { return c.cancel() }
            host = String(decoding: name, as: UTF8.self)
        case 4:
            guard let a = await Wire.read(c, 16) else { return c.cancel() }
            host = IPv6Address(Data(a))?.debugDescription ?? "?"
        default:
            return c.cancel()
        }
        guard let p = await Wire.read(c, 2) else { return c.cancel() }
        let port = Int(p[0]) << 8 | Int(p[1])
        lock.withLock { asked.append(Asked(host: host, port: port, username: username)) }
        if let refuse {
            await Wire.write(c, [5, refuse, 0, 1, 0, 0, 0, 0, 0, 0])
            return c.cancel()
        }
        // A name in the bound address, as some proxies answer: the client reads it to its end.
        let name: [UInt8] = Array("proxy.test".utf8)
        var reply: [UInt8] = [5, 0, 0, 3, UInt8(name.count)]
        reply += name
        reply += [0x04, 0x38]
        if eager { await Wire.write(c, reply) }
        guard let target = await Wire.dial(host, port, queue: upstream) else {
            if !eager { await Wire.write(c, [5, 5, 0, 1, 0, 0, 0, 0, 0, 0]) }
            return c.cancel()
        }
        if !eager { await Wire.write(c, reply) }
        Wire.pump(c, target)
        Wire.pump(target, c)
    }
}

/// A hub on the loopback that answers `GET …/api/v1/health` as `answer` says, and closes each connection after its
/// answer. The hub's health leaves `version` out for a source it counts as the internet (server/internal/hub/hello.go).
final class FakeHealthHub: @unchecked Sendable {
    enum Answer {
        /// A hub that takes this source as its LAN.
        case hub(id: String)
        /// A hub that counts this source as the internet: no `version`.
        case internet(id: String)
        /// A web page.
        case notAHub
        /// A redirect to this same server's health elsewhere, which would pass when followed.
        case redirect
    }

    private var server: LoopbackServer!

    init(_ answer: Answer = .hub(id: "hub-test")) throws {
        server = try LoopbackServer { [weak self] c in
            let port = self?.port ?? 0
            Task {
                var request = Data()
                while !request.contains(Data("\r\n\r\n".utf8)), let more = await Wire.readSome(c) { request.append(more) }
                let line = String(decoding: request.prefix(while: { $0 != 13 }), as: UTF8.self)
                let health = line.hasPrefix("GET ") && line.contains("/api/v1/health ")
                let moved = line.hasPrefix("GET /moved/")
                var status = "404 Not Found", body = "<html>not here</html>", extra = ""
                switch answer {
                case .hub(let id) where health:
                    (status, body) = ("200 OK", #"{"ok":true,"hub_id":"\#(id)","version":"test"}"#)
                case .internet(let id) where health:
                    (status, body) = ("200 OK", #"{"ok":true,"hub_id":"\#(id)"}"#)
                case .redirect where moved:
                    (status, body) = ("200 OK", #"{"ok":true,"hub_id":"hub-test","version":"test"}"#)
                case .redirect where health:
                    status = "301 Moved Permanently"
                    extra = "Location: http://127.0.0.1:\(port)/moved/api/v1/health\r\n"
                default:
                    break
                }
                let head = "HTTP/1.1 \(status)\r\nContent-Type: application/json\r\n\(extra)Content-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n"
                await Wire.write(c, Array((head + body).utf8))
                c.send(content: nil, contentContext: .finalMessage, isComplete: true, completion: .contentProcessed { _ in c.cancel() })
            }
        }
    }

    var port: Int { server.port }
    func start() async throws { try await server.start() }
    func stop() { server.stop() }
}

/// Sends back whatever it reads.
final class EchoServer: @unchecked Sendable {
    private var server: LoopbackServer!

    init() throws {
        server = try LoopbackServer { c in Wire.pump(c, c) }
    }

    var port: Int { server.port }
    func start() async throws { try await server.start() }
    func stop() { server.stop() }
}

/// Takes connections and answers with `reply` (or never, when nil), `after` the first bytes came.
final class ScriptedServer: @unchecked Sendable {
    private var server: LoopbackServer!

    init(reply: [UInt8]?, after delay: Duration = .zero) throws {
        server = try LoopbackServer { c in
            guard let reply else { return }
            Task {
                _ = await Wire.readSome(c)
                try? await Task.sleep(for: delay)
                await Wire.write(c, reply)
            }
        }
    }

    var port: Int { server.port }
    func start() async throws { try await server.start() }
    func stop() { server.stop() }
}
