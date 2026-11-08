import Foundation
import Network

/// Why a connection through a SOCKS5 proxy did not come about (docs/operations/network.md "Connecting to hub through a
/// local SOCKS5 proxy"): the proxy out of reach, or a handshake it broke off or refused (RFC 1928, RFC 1929). Each says
/// it in a sentence for Settings › hub › Local Proxy and the connection details.
nonisolated enum SOCKS5Error: LocalizedError, Equatable, Sendable {
    /// No TCP connection to the proxy (nothing listens there, a wrong host); the system's reason.
    case unreachable(String)
    /// Its first answer is not SOCKS5's (an HTTP proxy, another service on that port); the version byte it sent.
    case notSOCKS5(UInt8)
    /// The proxy took none of the ways to sign in offered: no login, or no login and a username and password.
    case noAcceptableMethod(loginOffered: Bool)
    /// The proxy turned the username and password down (RFC 1929).
    case loginRejected
    /// The proxy could not, or would not, connect to the target: its reply code (RFC 1928 section 6).
    case connectFailed(UInt8)
    /// The proxy said something SOCKS5 does not allow there (a method that was not offered, an unknown address type).
    case malformed
    /// The proxy closed the connection before the handshake was over.
    case closed
    /// The handshake did not finish in time.
    case timedOut
    /// A username or password longer than SOCKS5 carries (255 bytes each).
    case loginTooLong
    /// A target host name longer than SOCKS5 carries (255 bytes), or an empty one.
    case badHost

    var errorDescription: String? {
        switch self {
        case .unreachable(let why): String(localized: "Can't reach the proxy (\(why))")
        case .notSOCKS5: String(localized: "The proxy didn't answer as a SOCKS5 proxy")
        case .noAcceptableMethod(let loginOffered):
            loginOffered ? String(localized: "The proxy accepts neither no login nor a username and password")
                : String(localized: "The proxy asks for a username and password")
        case .loginRejected: String(localized: "The proxy didn't accept the username or password")
        case .connectFailed(let code): String(localized: "The proxy couldn't connect to the hub (\(Self.reply(code)))")
        case .malformed: String(localized: "The proxy's answer doesn't follow SOCKS5")
        case .closed: String(localized: "The proxy closed the connection")
        case .timedOut: String(localized: "The proxy didn't answer in time")
        case .loginTooLong: String(localized: "The username and password can each be at most 255 bytes")
        case .badHost: String(localized: "The hub's address must be 1 to 255 bytes long")
        }
    }

    /// A CONNECT reply code by its RFC 1928 name, the code with it: protocol words, as the SSH tunnel shows the server's
    /// reasons.
    static func reply(_ code: UInt8) -> String {
        let name = switch code {
        case 1: "general SOCKS server failure"
        case 2: "connection not allowed by ruleset"
        case 3: "network unreachable"
        case 4: "host unreachable"
        case 5: "connection refused"
        case 6: "TTL expired"
        case 7: "command not supported"
        case 8: "address type not supported"
        default: "unassigned reply"
        }
        return "\(name), \(code)"
    }
}

/// The client side of a SOCKS5 handshake (RFC 1928) with an optional username and password (RFC 1929), up to the
/// CONNECT that makes the connection carry the target's bytes. Only bytes here, no network: it says what to write and
/// how many bytes to read next (`SOCKS5Dial` carries that out on a connection), so tests drive it byte by byte. Every
/// read is of an exact length, so the bytes the target sends right after the proxy's reply stay on the connection.
nonisolated struct SOCKS5Handshake: Sendable {
    enum Step: Equatable, Sendable {
        /// Write these bytes, then read exactly `then` bytes and hand them to `read(_:)`.
        case write([UInt8], then: Int)
        /// Read exactly this many bytes and hand them to `read(_:)`.
        case read(Int)
        /// The proxy connected: what the connection reads and writes from now on is the target's.
        case done
    }

    static let version: UInt8 = 5
    static let noLogin: UInt8 = 0
    static let usernamePassword: UInt8 = 2
    static let noAcceptable: UInt8 = 0xFF

    private enum Phase { case method, login, reply, replyNameLength, replyAddress, done }
    private var phase = Phase.method
    private var expected = 2
    private let greeting: [UInt8]
    private let loginRequest: [UInt8]?
    private let connectRequest: [UInt8]

    /// `host`: a name the proxy resolves (its network's DNS), or an IPv4 / IPv6 literal (brackets allowed).
    init(host: String, port: Int, login: (username: String, password: String)?) throws {
        guard (1...65535).contains(port) else { throw SOCKS5Error.badHost }
        connectRequest = try Self.connectRequest(host: host, port: UInt16(port))
        if let login {
            let user = Array(login.username.utf8), pass = Array(login.password.utf8)
            guard user.count <= 255, pass.count <= 255 else { throw SOCKS5Error.loginTooLong }
            var request: [UInt8] = [1, UInt8(user.count)]
            request += user
            request.append(UInt8(pass.count))
            request += pass
            loginRequest = request
            greeting = [Self.version, 2, Self.noLogin, Self.usernamePassword]
        } else {
            loginRequest = nil
            greeting = [Self.version, 1, Self.noLogin]
        }
    }

    /// The greeting: the methods to sign in offered (no login, and a username and password when there is one).
    func start() -> Step { .write(greeting, then: 2) }

    /// The bytes the last step asked for; what to do next. Throws when the proxy refuses, or breaks the protocol.
    mutating func read(_ bytes: [UInt8]) throws -> Step {
        guard bytes.count == expected, phase != .done else { throw SOCKS5Error.malformed }
        switch phase {
        case .method:
            guard bytes[0] == Self.version else { throw SOCKS5Error.notSOCKS5(bytes[0]) }
            switch bytes[1] {
            case Self.noLogin:
                return send(connectRequest, then: 4, .reply)
            case Self.usernamePassword:
                // Not offered, yet some proxies (mihomo) answer it to every client once they ask for a login.
                guard let loginRequest else { throw SOCKS5Error.noAcceptableMethod(loginOffered: false) }
                return send(loginRequest, then: 2, .login)
            case Self.noAcceptable:
                throw SOCKS5Error.noAcceptableMethod(loginOffered: loginRequest != nil)
            default:
                throw SOCKS5Error.malformed
            }
        case .login:
            // RFC 1929's reply is version 1; some proxies answer 5. Only the status counts.
            guard bytes[1] == 0 else { throw SOCKS5Error.loginRejected }
            return send(connectRequest, then: 4, .reply)
        case .reply:
            guard bytes[0] == Self.version else { throw SOCKS5Error.malformed }
            guard bytes[1] == 0 else { throw SOCKS5Error.connectFailed(bytes[1]) }
            switch bytes[3] {
            case 1: return expect(4 + 2, .replyAddress)
            case 4: return expect(16 + 2, .replyAddress)
            case 3: return expect(1, .replyNameLength)
            default: throw SOCKS5Error.malformed
            }
        case .replyNameLength:
            return expect(Int(bytes[0]) + 2, .replyAddress)
        case .replyAddress:
            phase = .done
            return .done
        case .done:
            throw SOCKS5Error.malformed
        }
    }

    private mutating func send(_ bytes: [UInt8], then n: Int, _ next: Phase) -> Step {
        phase = next
        expected = n
        return .write(bytes, then: n)
    }

    private mutating func expect(_ n: Int, _ next: Phase) -> Step {
        phase = next
        expected = n
        return .read(n)
    }

    /// CONNECT to `host:port`: an IPv4 or IPv6 literal as its address, anything else as a name for the proxy to resolve.
    static func connectRequest(host: String, port: UInt16) throws -> [UInt8] {
        let h = host.trimmingCharacters(in: .whitespaces).trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        var out: [UInt8] = [version, 1, 0]
        if let v4 = IPv4Address(h), !h.contains(":") {
            out += [1] + Array(v4.rawValue)
        } else if h.contains(":"), let v6 = IPv6Address(h) {
            out += [4] + Array(v6.rawValue)
        } else {
            let name = Array(h.utf8)
            guard (1...255).contains(name.count) else { throw SOCKS5Error.badHost }
            out += [3, UInt8(name.count)] + name
        }
        return out + [UInt8(port >> 8), UInt8(port & 0xFF)]
    }
}

/// SOCKS5 on a real connection: dials the proxy, carries out a `SOCKS5Handshake` and hands back the connection, which
/// carries the target's bytes from then on.
nonisolated enum SOCKS5Dial {
    /// How long dialling the proxy and the whole handshake may take.
    static let timeout: TimeInterval = 10

    /// The connection, ready (started on `queue`, its state handler still the dial's own: replace it). Throws a
    /// `SOCKS5Error`.
    static func connect(host: String, port: Int, handshake: SOCKS5Handshake, queue: DispatchQueue,
                        timeout: TimeInterval = SOCKS5Dial.timeout) async throws -> NWConnection {
        guard (1...65535).contains(port), let p = NWEndpoint.Port(rawValue: UInt16(port)) else {
            throw SOCKS5Error.unreachable(String(cString: strerror(EINVAL)))
        }
        let tcp = NWProtocolTCP.Options()
        tcp.noDelay = true
        let conn = NWConnection(host: NWEndpoint.Host(host.trimmingCharacters(in: .whitespaces)), port: p,
                                using: NWParameters(tls: nil, tcp: tcp))
        let clock = Deadline()
        let timer = DispatchWorkItem { @Sendable in if clock.expire() { conn.cancel() } }
        queue.asyncAfter(deadline: .now() + timeout, execute: timer)
        do {
            try await ready(conn, queue: queue)
            var handshake = handshake
            var step = handshake.start()
            while step != .done {
                switch step {
                case .write(let bytes, let n):
                    try await send(bytes, on: conn)
                    step = try handshake.read(try await receive(n, on: conn))
                case .read(let n):
                    step = try handshake.read(try await receive(n, on: conn))
                case .done:
                    break
                }
            }
            guard clock.finish() else { throw SOCKS5Error.timedOut }
            timer.cancel()
            return conn
        } catch {
            timer.cancel()
            conn.cancel()
            throw clock.expired ? SOCKS5Error.timedOut : error as? SOCKS5Error ?? .closed
        }
    }

    /// The connection up; a refused or impossible one (`.waiting`: it would only try again later) fails at once.
    private static func ready(_ conn: NWConnection, queue: DispatchQueue) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            let once = RunOnce()
            conn.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready:
                    once.run { cont.resume() }
                case .waiting(let e), .failed(let e):
                    once.run { cont.resume(throwing: SOCKS5Error.unreachable(reason(e))) }
                    conn.cancel()
                case .cancelled:
                    once.run { cont.resume(throwing: SOCKS5Error.closed) }
                default:
                    break
                }
            }
            conn.start(queue: queue)
        }
    }

    private static func send(_ bytes: [UInt8], on conn: NWConnection) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            conn.send(content: Data(bytes), completion: .contentProcessed { @Sendable error in
                if error == nil { cont.resume() } else { cont.resume(throwing: SOCKS5Error.closed) }
            })
        }
    }

    /// Exactly `n` bytes, or `.closed` when the proxy ends the connection first.
    private static func receive(_ n: Int, on conn: NWConnection) async throws -> [UInt8] {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<[UInt8], Error>) in
            conn.receive(minimumIncompleteLength: n, maximumLength: n) { @Sendable data, _, _, _ in
                if let data, data.count == n { cont.resume(returning: [UInt8](data)) } else { cont.resume(throwing: SOCKS5Error.closed) }
            }
        }
    }

    /// The system's words for a connection that did not come up ("Connection refused").
    static func reason(_ e: NWError) -> String {
        if case .posix(let code) = e { return String(cString: strerror(code.rawValue)) }
        return e.debugDescription
    }

    /// The dial's time limit: whichever of finishing and running out comes first wins, from any thread.
    private final class Deadline: @unchecked Sendable {
        private let lock = NSLock()
        private var state = 0 // 0 running, 1 finished, 2 expired

        var expired: Bool { lock.withLock { state == 2 } }

        /// Out of time, unless it finished: true when this call ended it.
        func expire() -> Bool { lock.withLock { if state == 0 { state = 2; return true }; return false } }

        /// Finished in time: false when the time ran out first.
        func finish() -> Bool { lock.withLock { if state == 0 { state = 1 }; return state == 1 } }
    }
}
