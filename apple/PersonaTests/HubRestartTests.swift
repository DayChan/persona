import Foundation
import Network
import Testing
@testable import Persona

/// The app riding out a hub restart (docs/operations/hub-restart.md, #1634): where the broadcasts left off and the resume URL,
/// how long a drop stays invisible and how fast it retries, what a resumed reconnect leaves alone, and writes that go
/// again with the same op id.
@MainActor struct HubRestartTests {
    // MARK: Event cursor

    @Test func aHelloStartsTheCursorAndBroadcastsMoveIt() {
        var c = EventCursor()
        #expect(c.queryItems.isEmpty) // nothing to resume from yet
        c = c.afterHello(stream: "s1", seq: 40, resumed: false)
        #expect(c == EventCursor(stream: "s1", seq: 40))
        c = c.afterEvent(41)
        c = c.afterEvent(39) // an older one changes nothing
        c = c.afterEvent(nil) // not a broadcast
        #expect(c == EventCursor(stream: "s1", seq: 41))
        #expect(c.queryItems == [URLQueryItem(name: "event_stream", value: "s1"), URLQueryItem(name: "event_seq", value: "41")])
        #expect(LiveConnection.queryItems(c).map(\.name) == ["features", "event_stream", "event_seq"])
        #expect(LiveConnection.queryItems(EventCursor()) == [URLQueryItem(name: "features", value: "sessions_delta,extension_operations,managers,desktops_link," + HubNaming.feature)])
    }

    @Test func aResumedHelloLeavesTheCursorToTheReplay() {
        let c = EventCursor(stream: "s1", seq: 41)
        // resumed: the replay that follows moves it (a drop in the middle of it resumes from the last that came)
        #expect(c.afterHello(stream: "s1", seq: 60, resumed: true) == c)
        // not resumed on the same stream: the full refresh covers up to the hello's number
        #expect(c.afterHello(stream: "s1", seq: 60, resumed: false) == EventCursor(stream: "s1", seq: 60))
        // another stream (the hub crashed): start over at its number, even a lower one
        #expect(c.afterHello(stream: "s2", seq: 3, resumed: true) == EventCursor(stream: "s2", seq: 3))
        // an older hub numbers nothing: no cursor, and no resume asked for
        #expect(c.afterHello(stream: nil, seq: nil, resumed: false) == EventCursor())
        #expect(EventCursor().afterEvent(7) == EventCursor())
    }

    @Test func framesSayTheirPlaceInTheStream() {
        let hello = FrameMeta.decode(Data(#"{"type":"hello","event_stream":"s1","event_seq":12,"resumed":true,"features":[]}"#.utf8))
        #expect(hello == FrameMeta(type: "hello", eventSeq: 12, eventStream: "s1", resumed: true))
        #expect(FrameMeta.decode(Data(#"{"type":"todos","event_seq":13}"#.utf8)).eventSeq == 13)
        // a terminal frame's `seq` is its own numbering, not the broadcasts'
        #expect(FrameMeta.decode(Data(#"{"type":"terminal","seq":99}"#.utf8)).eventSeq == nil)
        #expect(FrameMeta.decode(Data(#"{"type":"hub_restarting","back_in_ms":3000}"#.utf8)).isRestarting)
        #expect(FrameMeta.decode(Data("not json".utf8)) == FrameMeta())
        // a frame with nothing of the kind is not parsed a second time
        #expect(FrameMeta.read(Data(#"{"type":"sessions","machine_id":"m","sessions":[]}"#.utf8)) == FrameMeta())
        #expect(FrameMeta.read(Data(#"{"type":"todos","event_seq":13}"#.utf8)).eventSeq == 13)
        #expect(FrameMeta.read(Data(#"{"type":"hub_restarting","back_in_ms":3000}"#.utf8)).isRestarting)
        // the event itself still decodes with the new fields beside it
        guard case .hello? = ServerEvent.decode(Data(#"{"type":"hello","event_stream":"s1","event_seq":12,"resumed":true}"#.utf8)) else {
            Issue.record("hello did not decode")
            return
        }
    }

    // MARK: Grace and retries

    @Test func aDropStaysInvisibleForItsGrace() {
        let at = Date(timeIntervalSince1970: 1_000)
        let plain = LinkDrop(at: at, restart: false), restart = LinkDrop(at: at, restart: true)
        #expect(plain.inGrace(at: at.addingTimeInterval(7.9)))
        #expect(!plain.inGrace(at: at.addingTimeInterval(8)))
        #expect(restart.inGrace(at: at.addingTimeInterval(29.9)))
        #expect(!restart.inGrace(at: at.addingTimeInterval(30)))
        // once a grace ran out the titles change at once; any other phase waits the usual moment first
        #expect(ConnectionStatus.showDelay(graceOver: true) == .zero)
        #expect(ConnectionStatus.showDelay(graceOver: false) == ConnectionStatus.delay)
    }

    @Test func aRestartRetriesFastWithoutBackingOff() {
        let d = LinkDrop.retryDelay
        for rand in [0, 0.5, 0.999] {
            #expect((0.15...0.4).contains(d(0, true, 0.1, rand)))
            for attempt in 1...40 { #expect((0.5...1).contains(d(attempt, true, 10, rand))) } // never counted as failing
            // after the quick phase: the ordinary backoff, counted from there
            #expect((0.3...0.6).contains(d(0, true, 31, rand)))
            #expect((0.3...0.6).contains(d(0, false, 0, rand)))
            #expect((0.8...1.2).contains(d(1, false, 1, rand)))
            #expect((3.2...4.8).contains(d(3, false, 5, rand)))
            #expect((16...24).contains(d(12, false, 60, rand)))
        }
    }

    @Test func aResumedReconnectFetchesNothingAgain() {
        #expect(ReconnectPlan(resumed: true).refetch == false)
        #expect(ReconnectPlan(resumed: false).refetch == true)
    }

    // MARK: Writes with an op id

    @Test func aMessageCarriesItsIDAsTheOpID() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/m1/s1/messages", json: #"{"ok":true}"#)
        let id = UUID()
        try await hub.client.send(SessionKey(machineID: "m1", sessionID: "s1"), text: "hi", clientMsgID: id.uuidString, opID: id)
        let req = try #require(hub.sent("POST", "/api/v1/sessions/m1/s1/messages").first)
        #expect(req.headers[OpRetry.header] == id.uuidString.lowercased())
        let body = try JSONSerialization.jsonObject(with: try #require(req.body)) as? [String: Any]
        #expect(body?["client_msg_id"] as? String == id.uuidString)
    }

    @Test func aWriteGoesAgainWithTheSameOpIDThroughAGap() async throws {
        let hub = FakeHub()
        var calls = 0
        hub.on("POST", "/api/v1/interactions/ix1/resolve") { _ in
            calls += 1
            switch calls {
            case 1: throw URLError(.networkConnectionLost) // the old process went with it
            case 2: throw APIError(status: 502, message: "Bad Gateway") // the proxy in front, not the hub
            case 3: return Data(#"{"state":"pending"}"#.utf8) // the hub has the first one still running
            default: return Data(#"{"ok":true}"#.utf8)
            }
        }
        let api = hub.client, op = UUID()
        var waits: [TimeInterval] = []
        let res = try await OpRetry.untilDelivered(wait: { s, _ in waits.append(s) }) {
            try await api.resolve("ix1", Resolution(allow: true), opID: op)
        }
        #expect(res.ok == true)
        let ids = hub.sent("POST", "/api/v1/interactions/ix1/resolve").map { $0.headers[OpRetry.header] }
        #expect(ids == Array(repeating: op.uuidString.lowercased(), count: 4))
        #expect(waits == [0.5, 1, 2])
    }

    @Test func theHubsOwnAnswersAreNotRetried() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/interactions/ix1/resolve") { _ in
            throw APIError(status: 409, message: "prompt changed", fields: ["code": "prompt_changed"])
        }
        let api = hub.client
        await #expect(throws: APIError.self) {
            try await OpRetry.untilDelivered(wait: { _, _ in }) { try await api.resolve("ix1", Resolution(allow: true)) }
        }
        #expect(hub.sent("POST", "/api/v1/interactions/ix1/resolve").count == 1)
        // the hub's 503 with a code (a machine offline) is an answer too, and so is one that says what was delivered
        #expect(!APIError(status: 503, message: "offline", fields: ["code": "node_offline"]).isGap)
        #expect(!APIError(status: 502, message: "x", fields: ["delivered": "unknown"]).isGap)
        #expect(APIError(status: 504, message: "Gateway Timeout").isGap)
        #expect(URLError(.cannotConnectToHost).isGap)
        #expect(URLError(.timedOut).isGap)
        #expect(!URLError(.cancelled).isGap)
    }

    @Test func retriesStopAtTheLimit() async {
        var clock = Date(timeIntervalSince1970: 0)
        var tries = 0
        await #expect(throws: URLError.self) {
            try await OpRetry.untilDelivered(limit: 60, now: { clock }, wait: { s, _ in clock.addTimeInterval(s) }) {
                tries += 1
                throw URLError(.cannotConnectToHost)
            }
        }
        #expect(clock.timeIntervalSince1970 == 60) // waited exactly up to the limit, not past it
        #expect(tries == 19) // waits 0.5 + 1 + 2 + 4 × 14 + 0.5 = 60 between 19 tries
        // the caller can stop it sooner (a message whose drop's grace ran out goes to the outbox)
        tries = 0
        await #expect(throws: URLError.self) {
            try await OpRetry.untilDelivered(keepTrying: { tries < 2 }, wait: { _, _ in }) {
                tries += 1
                throw URLError(.cannotConnectToHost)
            }
        }
        #expect(tries == 2)
    }

    /// The drop's grace runs out in the middle of a wait: the write stops then (the outbox takes it), not after
    /// the 4 s step and one more try (ConnectionTests saw the clock bubble come seconds late).
    @Test func aWriteStopsAsSoonAsTheGraceIsOver() async {
        var tries = 0, grace = true
        let start = Date.now
        Task { @MainActor in try? await Task.sleep(for: .milliseconds(300)); grace = false }
        await #expect(throws: URLError.self) {
            try await OpRetry.untilDelivered(keepTrying: { grace }) {
                tries += 1
                throw URLError(.networkConnectionLost)
            }
        }
        #expect(tries == 1) // the first wait (0.5 s) was cut short, and nothing went again
        #expect(Date.now.timeIntervalSince(start) < 0.5)
    }

    @Test func anUnknownReceiptIsNeverTakenForDone() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/m1/s1/messages", json: #"{"state":"unknown"}"#)
        let id = UUID()
        do {
            try await hub.client.send(SessionKey(machineID: "m1", sessionID: "s1"), text: "hi", clientMsgID: id.uuidString, opID: id)
            Issue.record("an unknown receipt passed as sent")
        } catch let e as APIError {
            #expect(e.code == OpReceipt.unknownCode)
            #expect(!e.isGap) // not sent again on its own
        }
        // a real response must be a 202 to be a receipt; without an op id the same body is just an answer
        #expect(throws: Never.self) { try APIClient.checkReceipt(Data(#"{"state":"unknown"}"#.utf8), status: 200) }
        hub.on("POST", "/api/v1/other", json: #"{"state":"unknown"}"#)
        _ = try await hub.client.post("/api/v1/other")
    }

    @Test func theReceiptOfAnOp() async throws {
        let hub = FakeHub()
        let id = UUID()
        hub.on("GET", "/api/v1/ops/\(id.uuidString.lowercased())", json: #"{"id":"x","state":"done","kind":"message"}"#)
        #expect(try await hub.client.opState(id) == "done")
        #expect(try await hub.client.opState(UUID()) == nil) // no such op (404): nobody knows
    }

    // MARK: The socket through a restart

    /// A real socket on the loopback: the hub says hub_restarting and closes with 1001; the app drops into the
    /// restart's grace (still looking connected), comes back in well under a second, and takes the resumed hello.
    @Test(.timeLimit(.minutes(1))) func aPlannedRestartIsRiddenOut() async throws {
        let hub = try LoopbackHub()
        defer { hub.stop() }
        try await hub.ready()
        let live = LiveConnection()
        live.connect(ServerConfig(url: URL(string: "http://127.0.0.1:\(hub.port)")!, token: "t"))
        try await until { live.state == .connected && live.cursor.seq == 6 }
        #expect(live.cursor == EventCursor(stream: "s1", seq: 6))
        #expect(!live.resumed)

        let before = Date.now
        hub.restart() // hub_restarting, then close 1001
        try await until { live.drop != nil }
        #expect(live.drop?.restart == true)
        #expect(live.inGrace && live.looksConnected && live.takesWrites)
        // the titles: nothing to say within the grace
        let status = ConnectionStatus()
        status.lives = { [live] }
        #expect(status.phase == .online)
        try await until { live.state == .connected }
        #expect(Date.now.timeIntervalSince(before) < 2)
        #expect(live.resumed)
        #expect(live.drop == nil && !live.inGrace)
        try await until { live.cursor.seq == 8 } // the replayed broadcasts move it on
        live.disconnect()
    }

    private func until(_ ok: () -> Bool) async throws {
        for _ in 0..<200 where !ok() { try await Task.sleep(for: .milliseconds(25)) }
        #expect(ok())
        if !ok() { throw CancellationError() }
    }
}

/// A WebSocket "hub" on the loopback (Network.framework): the first connection gets a hello opening stream s1 and two
/// broadcasts; `restart` says hub_restarting and closes with 1001; the next connection gets a resumed hello and the
/// two broadcasts it missed.
private final class LoopbackHub: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "loopback-hub")
    private var connections: [NWConnection] = []
    private var count = 0

    init() throws {
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(NWProtocolWebSocket.Options(), at: 0)
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] c in self?.accept(c) }
    }

    var port: UInt16 { listener.port?.rawValue ?? 0 }

    func ready() async throws {
        listener.start(queue: queue)
        for _ in 0..<200 where listener.port == nil || listener.port?.rawValue == 0 { try await Task.sleep(for: .milliseconds(10)) }
    }

    func stop() {
        listener.cancel()
        queue.sync { connections.forEach { $0.cancel() } }
    }

    private func accept(_ c: NWConnection) {
        connections.append(c)
        count += 1
        let first = count == 1
        c.stateUpdateHandler = { [weak self] state in
            guard case .ready = state, let self else { return }
            if first {
                self.send(c, #"{"type":"hello","features":[],"event_stream":"s1","event_seq":4,"resumed":false}"#)
                self.send(c, #"{"type":"todos","event_seq":5}"#)
                self.send(c, #"{"type":"inbox","event_seq":6}"#)
            } else {
                self.send(c, #"{"type":"hello","features":[],"event_stream":"s1","event_seq":8,"resumed":true}"#)
                self.send(c, #"{"type":"todos","event_seq":7}"#)
                self.send(c, #"{"type":"inbox","event_seq":8}"#)
            }
            self.read(c)
        }
        c.start(queue: queue)
    }

    private func read(_ c: NWConnection) {
        c.receiveMessage { [weak self] _, _, _, error in if error == nil { self?.read(c) } }
    }

    private func send(_ c: NWConnection, _ text: String) {
        let meta = NWProtocolWebSocket.Metadata(opcode: .text)
        c.send(content: Data(text.utf8), contentContext: NWConnection.ContentContext(identifier: "text", metadata: [meta]),
               completion: .idempotent)
    }

    func restart() {
        queue.async { [self] in
            guard let c = connections.last else { return }
            send(c, #"{"type":"hub_restarting","back_in_ms":3000}"#)
            let close = NWProtocolWebSocket.Metadata(opcode: .close)
            close.closeCode = .protocolCode(.goingAway)
            c.send(content: Data("hub restarting".utf8), contentContext: NWConnection.ContentContext(identifier: "close", metadata: [close]),
                   completion: .contentProcessed { _ in c.cancel() })
        }
    }
}
