import Foundation
@testable import Persona

// One device of a sync scenario: the real AppModel, reached through the demo config's socket and requests (as Demo
// Mode is), and what its chat page would draw, projected onto the README's view.

/// The device's end of the socket: the case's frames go in through `deliver`, what the app sends is recorded.
@MainActor
final class SyncSocket: LocalSocket {
    private(set) var deliver: ((Data) -> Void)?
    private(set) var drop: (() -> Void)?
    private(set) var closed = false
    var onReceive: (([String: Any]) -> Void)?

    func open(deliver: @escaping (Data) -> Void) { self.deliver = deliver }

    func open(deliver: @escaping (Data) -> Void, dropped: @escaping () -> Void) {
        self.deliver = deliver
        drop = dropped
    }

    func receive(_ message: [String: Any]) { onReceive?(message) }
    func close() { closed = true }

    func send(_ frame: JSONValue) {
        guard let data = try? JSONEncoder().encode(frame) else { return }
        deliver?(data)
    }
}

/// What a device drew, in the README's terms.
struct SyncView: Equatable, CustomStringConvertible {
    struct Message: Equatable {
        var from: String
        var text: String
        var state: String?
    }

    var messages: [Message] = []
    /// open / answering (answered ones are not drawn).
    var cards: [String: String] = [:]
    var status: String?
    var live: String?

    var description: String {
        let m = messages.map { "\($0.from)[\($0.state ?? "-")] \($0.text)" }.joined(separator: " | ")
        return "messages: \(m); cards: \(cards.sorted { $0.key < $1.key }); status: \(status ?? "nil"); live: \(live ?? "nil")"
    }
}

@MainActor
final class SyncDevice {
    let name: String
    let key: SessionKey
    /// What it keeps on disk (the snapshot, the outbox, the chat cache): a folder of its own, so it starts from nothing whatever the
    /// simulator's app saved (#1637), and a relaunch finds what it wrote.
    let folder: URL
    private(set) var model: AppModel
    private(set) var socket: SyncSocket?
    /// Sockets the app made so far (a reconnect makes a new one).
    private(set) var made = 0
    /// What the device asked for, in order: `{"get": "/items", "after_seq": 4}`, `{"ws": "subscribe", "since_seq": 4}`,
    /// `{"post": "messages", "id": "m1"}`, `{"post": "resolve", "id": "ix1"}`.
    var requests: [[String: JSONValue]] = []
    /// Requests checked so far (the next `requests` expectation starts after them).
    var requestsChecked = 0
    /// The case's messages sent here: id and words, in order.
    private(set) var sends: [(id: String, text: String)] = []
    /// The op id each sent message first went with (later ones are new ops).
    private(set) var firstOp: [String: UUID] = [:]
    /// The op id each sent message went with last: what the hub ties its transcript line to (`client_msg_id`).
    private(set) var lastOp: [String: UUID] = [:]
    private(set) var discarded: Set<String> = []
    /// Cards this device must never show open again: its answer went through, or the hub said they ended.
    var settledCards: Set<String> = []
    /// Interactions as last seen here, to answer by id.
    private(set) var knownCards: [String: Interaction] = [:]
    let owner = UUID()
    /// While the case sets the device up, every subscribe gets the hub's backlog, including a cached cursor.
    var answersSubscribe: ((Int?) -> JSONValue)?

    init(name: String, key: SessionKey, folder: URL) {
        self.name = name
        self.key = key
        self.folder = folder
        model = AppModel(stateFolder: folder)
    }

    /// A new model, as a relaunched app: what the old one stored stays, the old one's socket goes.
    func relaunch() {
        for link in model.links { link.live.disconnect() }
        model = AppModel(stateFolder: folder)
    }

    /// Talks to the scenario's hub over a socket and a transport of its own (`AppModel.linkHooksForTests`), so several
    /// devices play side by side. Demo Mode's hub, which AppModel's init installs for the whole process, answers
    /// nothing: a request that still goes there is a failure of the case (`SyncRun.stray`).
    func connect() {
        let name = name
        model.linkHooksForTests = (transport: { req in try await SyncRun.http(req, from: name) },
                                   socket: { [weak self] in self?.newSocket() ?? SyncSocket() })
        LiveConnection.makeLocal = nil
        APIClient.local = { req in try await SyncRun.stray(req) }
        model.connectOnly(.demo)
    }

    private func newSocket() -> SyncSocket {
        let s = SyncSocket()
        s.onReceive = { [weak self] m in self?.received(m) }
        socket = s
        made += 1
        return s
    }

    private func received(_ m: [String: Any]) {
        guard let type = m["type"] as? String, type != "client_hello", type != "ping", type != "app_ping" else { return }
        var r: [String: JSONValue] = ["ws": .string(type)]
        if let since = m["since_seq"] as? Int { r["since_seq"] = .number(Double(since)) }
        requests.append(r)
        if type == "subscribe", let backlog = answersSubscribe?(m["since_seq"] as? Int) {
            Task { @MainActor in self.socket?.send(backlog) }
        }
    }

    /// Notes an HTTP request the way the README names it; returns the case's name for a write (nil for a read).
    func note(_ req: LocalRequest) -> String? {
        let name = SyncHub.readName(req.path)
        if req.method == "GET" {
            var r: [String: JSONValue] = ["get": .string(name)]
            if let a = req.query["after_seq"].flatMap(Int.init) { r["after_seq"] = .number(Double(a)) }
            requests.append(r)
            return nil
        }
        if req.path.hasSuffix("/resolve") {
            let id = req.path.components(separatedBy: "/").dropLast().last ?? ""
            requests.append(["post": .string("resolve"), "id": .string(id)])
            return id
        }
        if req.path.hasSuffix("/messages") {
            let op = req.headers["X-Persona-Op-Id"].flatMap { UUID(uuidString: $0) }
            let text = (try? JSONDecoder().decode(JSONValue.self, from: req.body ?? Data()))?["text"]?.string ?? ""
            let id = sends.last { $0.text == text }?.id ?? "?"
            if let op { lastOp[id] = op }
            if op == firstOp[id] {
                requests.append(["post": .string("messages"), "id": .string(id)])
            } else {
                requests.append(["post": .string("messages"), "new_op": .bool(true), "message": .string(id)])
            }
            return id
        }
        requests.append(["post": .string(req.path)])
        return req.path
    }

    /// The case's JSON with its message ids where the hub would have this device's op ids: a line's `client_msg_id`
    /// is the id the send went with, which the app picks (a UUID), not the case.
    func translated(_ v: JSONValue) -> JSONValue {
        switch v {
        case .object(var o):
            for (k, x) in o {
                if k == "client_msg_id", let id = x.string, let op = lastOp[id] ?? firstOp[id] {
                    o[k] = .string(op.uuidString)
                } else {
                    o[k] = translated(x)
                }
            }
            return .object(o)
        case .array(let a): return .array(a.map(translated))
        default: return v
        }
    }

    // MARK: The user

    func send(id: String, text: String) {
        let before = Set(model.chat(key).pending.map(\.id))
        sends.append((id, text))
        model.sendNow(key, text: text)
        if let msg = model.chat(key).pending.first(where: { !before.contains($0.id) }) {
            firstOp[id] = msg.id
            lastOp[id] = msg.id
        }
    }

    /// The pending message the case calls `id` (by its words).
    func pending(_ id: String) -> PendingMessage? {
        guard let text = sends.last(where: { $0.id == id })?.text else { return nil }
        return model.chat(key).pending.first { $0.text == text }
    }

    func retry(_ id: String) {
        guard let msg = pending(id) else { return }
        model.retry(key, msg)
    }

    func discard(_ id: String) {
        discarded.insert(id)
        guard let msg = pending(id) else { return }
        model.discard(key, msg)
    }

    func answer(_ id: String, _ resolution: JSONValue) throws {
        noteCards()
        guard let ix = knownCards[id] else {
            throw SyncUnplayable(description: "\(name): cannot answer unloaded card \(id)")
        }
        let data = try JSONEncoder().encode(resolution)
        let r = try JSONDecoder().decode(Resolution.self, from: data)
        let model = model
        Task { @MainActor in await model.resolve(ix, r) }
    }

    func noteCards() {
        for ix in model.interactions(for: key) { knownCards[ix.id] = ix }
    }

    // MARK: What it drew

    var view: SyncView {
        noteCards()
        let store = model.chat(key)
        let session = model.session(key)
        var v = SyncView()
        let queued = session?.queued ?? []
        let pending = (store.arriving + store.pending).filter { p in !queued.contains { PendingMessage.same($0, p.text) } }
        let status = session?.status ?? .unknown
        let failed = session?.failureReason == nil ? nil : (session?.stateSince ?? .distantFuture)
        let awaiting = ChatAwaitingTurn.of(pending: store.arriving + store.pending, last: store.blocks.last,
                                          status: status, failedSince: failed, now: .now)
        var liveTurn: String?
        if case .turn(let turn) = store.blocks.last,
           !ChatAwaitingTurn.workIsForPending(pending, status: status, stateSince: session?.stateSince,
                                             newestLine: store.newestLineAt),
           status.isActive || awaiting.turnID == turn.id { liveTurn = turn.id }
        var streaming = !store.liveText.isEmpty && (session?.assistant == true || store.liveIsAgents)
        if !store.liveIsAgents, case .assistant(let item) = store.rows.last,
           let text = item.text, text.hasPrefix(store.liveText) { streaming = false }
        let lines = ChatLines(openWork: [], foldSupervision: session?.assistant == true, active: status.isActive)
            .build(store.blocks, live: liveTurn, streaming: streaming)
        let placed = ChatLines.placeFailed(lines, pending, writtenAt: store.writtenAt)
        func bubble(_ p: PendingMessage) -> SyncView.Message {
            SyncView.Message(from: "user", text: p.text, state: p.state == .failed ? "failed" : "pending")
        }
        for line in lines {
            v.messages += (placed.before[line.id] ?? []).map(bubble)
            guard case .row(let row) = line.content else { continue }
            switch row {
            case .user(let item): v.messages.append(.init(from: "user", text: item.text ?? "", state: "shown"))
            case .assistant(let item): v.messages.append(.init(from: "agent", text: item.text ?? "", state: nil))
            default: break
            }
        }
        v.messages += placed.rest.map(bubble)
        v.messages += queued.map { SyncView.Message(from: "user", text: $0, state: "pending") }
        for ix in model.interactions(for: key) { v.cards[ix.id] = "open" }
        // Drawn is drawn: a card the page lists can be tapped, whatever answer is on its way.
        for id in knownCards.keys where model.isAnswering(id) && v.cards[id] == nil { v.cards[id] = "answering" }
        v.status = session?.state.rawValue
        v.live = store.liveText.isEmpty ? nil : store.liveText
        return v
    }

    /// The README's three rules on what is drawn now; each broken one, said.
    func invariants(_ v: SyncView) -> [String] {
        var out: [String] = []
        for s in sends where !discarded.contains(s.id) {
            let n = v.messages.filter { $0.from == "user" && $0.text == s.text }.count
            if n == 0 { out.append("\(name): \(s.id) (\"\(s.text)\") is missing") }
            if n > 1 { out.append("\(name): \(s.id) (\"\(s.text)\") shows \(n) times") }
        }
        for id in settledCards where v.cards[id] == "open" { out.append("\(name): card \(id) is open again") }
        return out
    }
}
