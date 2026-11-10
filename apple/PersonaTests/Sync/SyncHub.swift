import Foundation
@testable import Persona

// The hub of a sync scenario (spec/fixtures/sync/README.md, #1112): the case's state over time, the reads a device makes
// answered from it, its writes held until the case's `reply`, and the receipts later retries get.

/// What a case says the hub holds, changed by its `hub` steps.
@MainActor
final class SyncHub {
    let machine: String
    let sessionID: String
    let assistant: Bool
    private(set) var history: String?
    private(set) var lines: [JSONValue] = []
    private(set) var toSeq = 0
    private(set) var interactions: [JSONValue] = []
    private(set) var session: [String: JSONValue] = [:]
    private(set) var eventStream: String?
    private(set) var eventSeq = 0
    private(set) var pageTurns: Int?
    /// Interactions the hub raised again (the "no revived cards" rule lets them be open again).
    private(set) var raised: Set<String> = []
    private var restarts = 0

    init(chat: JSONValue, start: JSONValue) {
        machine = chat["machine_id"]?.string ?? "m1"
        sessionID = chat["session_id"]?.string ?? "s1"
        assistant = chat["assistant"]?.bool ?? false
        history = start["history"]?.string
        lines = start["lines"]?.array ?? []
        toSeq = start["to_seq"]?.int ?? (lines.last?["seq"]?.int ?? 0)
        interactions = start["interactions"]?.array ?? []
        session = start["session"]?.object ?? [:]
        eventStream = start["event_stream"]?.string
        eventSeq = start["event_seq"]?.int ?? 0
        pageTurns = start["page_turns"]?.int
    }

    /// A `hub` step, its keys in the README's order.
    func apply(_ change: JSONValue) {
        if let restart = change["restart"] {
            restarts += 1
            if restart["resumable"]?.bool != true {
                eventStream = (eventStream ?? "ev") + "-restart\(restarts)"
                eventSeq = 0
            }
        }
        if let rewrite = change["rewrite"] {
            history = rewrite["history"]?.string
            lines = (rewrite["lines"]?.array ?? []).map(Self.writtenNow)
            toSeq = rewrite["to_seq"]?.int ?? (lines.last?["seq"]?.int ?? 0)
        }
        if let more = change["append"]?.array?.map(Self.writtenNow) {
            lines += more
            toSeq = max(toSeq, more.last?["seq"]?.int ?? toSeq)
        }
        if let to = change["to_seq"]?.int { toSeq = to }
        if let end = change["end"], let id = end["id"]?.string {
            interactions.removeAll { $0["id"]?.string == id }
            eventSeq += 1
        }
        if let raise = change["raise"] {
            interactions.append(raise)
            if let id = raise["id"]?.string { raised.insert(id) }
            eventSeq += 1
        }
        if let fields = change["session"]?.object { session.merge(fields) { _, new in new } }
    }

    /// A line the transcript gets during the case is written then: its time is now, not the case's made-up date (the
    /// app ties a sent bubble to a line by when each was written). Frames carrying it get the same time (`stamped`).
    static func writtenNow(_ line: JSONValue) -> JSONValue {
        guard case .object(var o) = line, let id = o["id"]?.string else { return line }
        let at = stamps[id] ?? ISO8601DateFormatter().string(from: .now)
        stamps[id] = at
        o["ts"] = .string(at)
        return .object(o)
    }

    /// The times given to lines written during the case, by item id.
    static var stamps: [String: String] = [:]

    /// A frame's items with the times their lines got when the hub wrote them.
    static func stamped(_ v: JSONValue) -> JSONValue {
        switch v {
        case .object(var o):
            if let id = o["id"]?.string, o["seq"] != nil, let at = stamps[id] { o["ts"] = .string(at); return .object(o) }
            for (k, x) in o { o[k] = stamped(x) }
            return .object(o)
        case .array(let a): return .array(a.map(stamped))
        default: return v
        }
    }

    // MARK: What the hub sends

    var sessionJSON: JSONValue {
        var s: [String: JSONValue] = [
            "machine_id": .string(machine), "session_id": .string(sessionID), "title": .string("Chat"), "cwd": .string("/home/user/project"),
            "assistant": .bool(assistant), "updated_at": .string("2026-01-02T03:04:05Z"), "rev": .number(100), "state": .string("idle"),
        ]
        s.merge(session) { _, new in new }
        if s["status"] == nil {
            s["status"] = .string(s["state"]?.string == "working" ? "working" : (s["state"]?.string == "needs_you" ? "waiting" : "idle"))
        }
        return .object(s)
    }

    func hello(resumed: Bool) -> JSONValue {
        var h: [String: JSONValue] = ["type": .string("hello"), "server_version": .string("test"), "features": .array(Self.features.map { .string($0) }),
                                      "resumed": .bool(resumed), "event_seq": .number(Double(eventSeq))]
        if let eventStream { h["event_stream"] = .string(eventStream) }
        return .object(h)
    }

    /// What the hub's hello offers that these cases touch (server/internal/proto Features).
    static let features = ["error_codes", "client_hello", "heartbeat", "app_ping", "client_msg_id", "client_ops", "send_refusals",
                           "screen_dialog", "resumable_interactions", "sessions_delta", "event_stream"]

    var machinesFrame: JSONValue {
        .object(["type": .string("machines"), "machines": .array([.object(["id": .string(machine), "name": .string("Laptop"), "kind": .string("local"), "online": .bool(true)])])])
    }

    var sessionsFrame: JSONValue {
        .object(["type": .string("sessions"), "machine_id": .string(machine), "sessions": .array([sessionJSON])])
    }

    /// The latest page: the last `page_turns` exchanges, else the whole transcript.
    var latestPage: [JSONValue] {
        guard let turns = pageTurns else { return lines }
        var starts: [Int] = []
        for (i, l) in lines.enumerated() where l["role"]?.string == "user" && l["kind"]?.string == "text" { starts.append(i) }
        guard starts.count > turns else { return lines }
        return Array(lines[starts[starts.count - turns]...])
    }

    /// Every subscription gets a backlog: the latest page, or everything after its cached cursor, even if empty.
    func backlogFrame(since: Int?) -> JSONValue {
        let after = since.flatMap { $0 > 0 ? $0 : nil }
        let page = after.map { cursor in lines.filter { ($0["seq"]?.int ?? 0) > cursor } } ?? latestPage
        var f: [String: JSONValue] = ["type": .string("items"), "machine_id": .string(machine), "session_id": .string(sessionID),
                                      "items": .array(page), "to_seq": .number(Double(toSeq)), "backlog": .bool(true)]
        if let after {
            f["from_seq"] = .number(Double(after))
            f["more_after"] = .bool(false)
        } else {
            f["has_more"] = .bool(page.count < lines.count)
        }
        if let history { f["history"] = .string(history) }
        return .object(f)
    }

    // MARK: Reads

    /// A read answered from the hub's state now; nil when this hub does not serve the path (the device's own business).
    func read(_ req: LocalRequest) -> Data? {
        let base = "/api/v1/sessions/\(machine)/\(sessionID)"
        switch req.path {
        case "/api/v1/sessions": return Self.json(["sessions": .array([sessionJSON])])
        case "/api/v1/machines": return Self.json(["machines": machinesFrame["machines"] ?? .array([])])
        case "/api/v1/interactions": return Self.json(["interactions": .array(interactions)])
        case base: return Self.json(["session": sessionJSON])
        case base + "/items":
            if let after = req.query["after_seq"].flatMap(Int.init) {
                let items = lines.filter { ($0["seq"]?.int ?? 0) > after }
                var r: [String: JSONValue] = ["items": .array(items), "from_seq": .number(Double(after)), "to_seq": .number(Double(toSeq)),
                                              "more_after": .bool(false)]
                if let history { r["history"] = .string(history) }
                return Self.json(r)
            }
            let page = latestPage
            var r: [String: JSONValue] = ["items": .array(page), "has_more": .bool(page.count < lines.count), "to_seq": .number(Double(toSeq))]
            if let history { r["history"] = .string(history) }
            return Self.json(r)
        default: return nil
        }
    }

    /// The README's name for a read's path (`/items`, `/interactions`, `/sessions`, `/ops`).
    static func readName(_ path: String) -> String {
        if path.hasSuffix("/items") { return "/items" }
        if path.hasPrefix("/api/v1/ops/") { return "/ops" }
        if path == "/api/v1/interactions" { return "/interactions" }
        if path.hasPrefix("/api/v1/sessions") { return "/sessions" }
        return path
    }

    static func json(_ object: [String: JSONValue]) -> Data {
        (try? JSONEncoder().encode(JSONValue.object(object))) ?? Data("{}".utf8)
    }
}

/// A write held until the case's `reply`: the request, and the continuation its answer resumes.
@MainActor
final class HeldWrite {
    let device: String
    /// The case's name for it: a send's message id, a card's interaction id.
    let write: String
    let op: String?
    let continuation: CheckedContinuation<Data, Error>

    init(device: String, write: String, op: String?, continuation: CheckedContinuation<Data, Error>) {
        self.device = device
        self.write = write
        self.op = op
        self.continuation = continuation
    }
}

/// A reply the case gave a write: what the request gets (or the connection breaking).
struct SyncReply {
    var status: Int
    var body: JSONValue
    var cut: Bool

    init(_ step: JSONValue) {
        status = step["status"]?.int ?? 200
        body = step["body"] ?? .object([:])
        cut = step["cut"]?.bool ?? false
    }

    init(receipt: JSONValue) {
        status = receipt["status"]?.int ?? 200
        body = receipt["body"] ?? .object([:])
        cut = false
    }

    var ok: Bool { !cut && (200..<300).contains(status) }

    /// As the app's transport gets it: the body, or the error a real response with that status makes.
    func result() -> Result<Data, Error> {
        if cut { return .failure(URLError(.networkConnectionLost)) }
        let data = (try? JSONEncoder().encode(body)) ?? Data("{}".utf8)
        if ok { return .success(data) }
        return .failure(APIError(status: status, message: APIError.message(in: data) ?? "HTTP \(status)",
                                 fields: APIError.fields(in: data), requestID: "sync", details: body))
    }
}
