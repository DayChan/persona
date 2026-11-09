import Foundation
import SwiftUI

/// How far a todo's work got, as the hub sends it in `TodoItem.stage` (#2776): the six steps, in order. A hub that
/// sends nothing, or a name this build does not know (an older or a newer hub), has none, and no stage bar is drawn.
nonisolated enum TodoStage: String, CaseIterable, Sendable {
    case plan, develop, review, merge, deploy, verify

    /// The step's name on the bar (App copy is Chinese, docs/protocol/i18n.md).
    var label: String {
        switch self {
        case .plan: String(localized: "Plan [Todo Models 6f949a]")
        case .develop: String(localized: "Develop")
        case .review: String(localized: "Review [Todo Models 747dd8]")
        case .merge: String(localized: "Merge")
        case .deploy: String(localized: "Deploy")
        case .verify: String(localized: "Verified [Todo Models 0a1b6f]")
        }
    }

    /// How far along the six it is: 1 … 6.
    var stepNumber: Int { (Self.allCases.firstIndex(of: self) ?? 0) + 1 }

    /// How many steps the bar has.
    static var stepCount: Int { allCases.count }

    /// The stage the hub named; none for nothing or a name this build does not know.
    init?(hub: String?) {
        guard let hub, let stage = TodoStage(rawValue: hub) else { return nil }
        self = stage
    }
}

nonisolated extension TodoItem {
    var isDone: Bool { doneAt != nil }

    /// The stage the hub sent (`stage`), none when it sent none or one this build does not know (#2776).
    var todoStage: TodoStage? { TodoStage(hub: stage) }

    /// The pull request its work is in (`pr_url`, #2768), when the hub sent a web address the in-app link sheet opens.
    var prLink: URL? {
        guard let raw = prURL?.trimmingCharacters(in: .whitespacesAndNewlines), !raw.isEmpty,
              let url = URL(string: raw), let scheme = url.scheme?.lowercased(),
              scheme == "http" || scheme == "https" else { return nil }
        return url
    }

    /// "PR #128" from the pull request's address, the way a task's receipt names it (#2768).
    var prLabel: String? {
        guard let last = prLink?.absoluteString.split(separator: "/").last, Int(last) != nil else { return nil }
        return "PR #\(last)"
    }

    /// The state's label for this todo: "Auto-completed" for one the hub closed (docs/assistant/todos.md), "Accepted" /
    /// "Canceled" for one its supervisor closed after a review (#3124).
    func statusLabel(_ state: TaskState) -> String {
        guard state == .done else { return state.todoLabel }
        if reviewDone { return supervision?.state == "cancelled" ? String(localized: "Canceled") : String(localized: "Accepted") }
        return autoDone ? String(localized: "Auto-completed") : state.todoLabel
    }

    /// Why the hub closed it, when it did.
    var autoDoneReason: String? {
        guard autoDone, let r = doneReason?.trimmingCharacters(in: .whitespacesAndNewlines), !r.isEmpty else { return nil }
        return r
    }
}

nonisolated extension TaskState {
    var todoLabel: String {
        switch self {
        case .done: StateName.done.text
        case .needsYou: StateName.needsYou.text
        case .unread: StateName.unread.text
        case .working: StateName.working.text
        case .followup: StateName.followUp.text
        case .notStarted: String(localized: "Not started")
        case .failed: String(localized: "Didn't Start")
        }
    }

    var todoRank: Int {
        switch self {
        case .needsYou: 0
        case .unread: 1
        case .working: 2
        case .followup: 3
        case .notStarted: 4
        case .done: 5
        case .failed: 6
        }
    }

    var todoTone: StatusTone {
        switch self {
        case .done: StatusPalette.idle
        case .needsYou: StatusPalette.needsYou
        case .unread: StatusPalette.unread
        case .working: StatusPalette.working
        case .followup: StatusPalette.idle
        case .notStarted: StatusPalette.idle
        case .failed: StatusPalette.idle
        }
    }
}

nonisolated struct TodoMostUrgent: Sendable {
    var agent: TodoItem.Agent
    var session: Session?
    var status: AgentState
    var isFailed: Bool
}

nonisolated struct TodoAgentGroup: Identifiable, Sendable {
    var key: String
    var name: String
    var sessions: [Session]
    var todoID: String?
    var todoStatus: TaskState?
    /// Starts folded (Unassigned below todos with nothing in it waiting for you, #1836).
    var folded = false

    var id: String { key }
}

nonisolated enum TodosLogic {
    /// A successfully loaded empty list allows creating the first todo; nonempty lists need an explicit array.
    /// The store keeps support unknown until its first successful response.
    static func supportsSpaces(_ todos: [TodoItem]) -> Bool {
        todos.isEmpty || todos.contains { $0.spaces != nil }
    }

    /// Modern hub JSON errors always carry a code; older hubs without this route return plain-text 404s.
    /// Reads the raw fields (the status, `code` and the router's literal "Not Found"), not `message`, which is text for people.
    static func isLegacySpacesRoute(_ error: Error) -> Bool {
        guard let api = error as? APIError, api.status == 404 else { return false }
        let code = api.fields["code"] ?? ""
        return code.isEmpty || (code == "not_found" && api.fields["error"] == "Not Found")
    }

    static func findTodoForSpace(_ todos: [TodoItem], machineID: String, space: String) -> TodoItem? {
        guard !machineID.isEmpty, !space.isEmpty else { return nil }
        return todos.first { todo in
            !todo.isDone && (todo.spaces ?? []).contains { $0.machineID == machineID && $0.space == space }
        }
    }

    /// Each path component follows encodeURIComponent, including slashes inside a project name.
    static func spacePath(todoID: String, machineID: String, space: String) -> String {
        "/api/v1/todos/\(pathComponent(todoID))/spaces/\(pathComponent(machineID))/\(pathComponent(space))"
    }

    /// One segment of a todo route, as `encodeURIComponent` encodes it.
    static func pathComponent(_ value: String) -> String {
        let allowed = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()")
        return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
    }

    static func agentSource(_ agent: TodoItem.Agent, lookup: SessionLookup? = nil) -> String? {
        switch agent.source {
        case "space":
            let via = agent.via ?? ""
            let name = via.firstIndex(of: "/").map { String(via[via.index(after: $0)...]) } ?? via
            return name.isEmpty ? String(localized: "With project") : String(localized: "With project \(name)")
        case "spawned":
            if let via = agent.via, let slash = via.firstIndex(of: "/"),
               let parent = lookup?(String(via[..<slash]), String(via[via.index(after: slash)...])),
               !parent.title.isEmpty {
                return String(localized: "Spawned by \(parent.title)")
            }
            return String(localized: "Spawned")
        case "chat": return String(localized: "Same Group")
        default: return nil
        }
    }

    /// Whether a project of a todo has an Agent running on it (else the detail says 「当前没有 Agent」): a live session
    /// in that project, on the todos' hub group (`isOnTodosHub`: another group's computer may share the id and the
    /// project's name), on a computer that is connected (as the web: a disconnected computer's sessions turn unknown,
    /// not offline).
    static func hasLiveAgent(in space: TodoItem.Space, sessions: [Session], hubGroup: String?,
                             machineOnline: (String) -> Bool) -> Bool {
        guard machineOnline(space.machineID) else { return false }
        return sessions.contains { session in
            guard session.machineID == space.machineID, session.state.isLive,
                  isOnTodosHub(sessionGroup: session.group, hubGroup: hubGroup) else { return false }
            let name = session.spaceName.flatMap { $0.isEmpty ? nil : $0 } ?? session.spaceID
            return name == space.space
        }
    }

    static func createdLinkFailure(_ error: Error, isSpace: Bool) -> String {
        isSpace
            ? String(localized: "To-do created, but adding the project failed: \(error.localizedDescription)")
            : String(localized: "To-do created, but linking failed: \(error.localizedDescription)")
    }

    /// Whether a session may take a todo's actions here. The todos are the selected hub's (`AppModel.hub`), so a
    /// session on another hub group would send its request to the wrong hub and find no todo.
    static func isOnTodosHub(sessionGroup: String, hubGroup: String?) -> Bool {
        sessionGroup == (hubGroup ?? "")
    }

    /// The Agents of the given todos, read one by one through `session` (a view reads one session's own cell, not the
    /// whole list). Only Agents on `hubGroup` can be linked (`isOnTodosHub`), so the keys are built on that group.
    static func linkedSessions(of todos: [TodoItem], hubGroup: String, session: (SessionKey) -> Session?) -> [Session] {
        todos.flatMap { todo in
            todo.agents.compactMap { a in
                session(SessionKey(group: hubGroup, machineID: a.machineID, sessionID: a.sessionID))
            }
        }
    }

    /// GET /api/v1/todos answered 404: an older hub without todos. The entrance is hidden and nothing is shown as an error.
    public static func isOldHub(_ error: Error) -> Bool {
        (error as? APIError)?.status == 404
    }

    public typealias SessionLookup = @Sendable (_ machineID: String, _ sessionID: String) -> Session?

    public static func makeSessionLookup(_ sessions: [Session]) -> SessionLookup {
        var map = [String: Session]()
        for s in sessions {
            map["\(s.machineID)/\(s.sessionID)"] = s
        }
        let immutableMap = map
        return { mid, sid in immutableMap["\(mid)/\(sid)"] }
    }

    /**
     * TodoItem status, first match wins:
     * 1. Done: the user marked it done (done_at != nil)
     * 2. Needs you: an attached agent needs the user (a question or an approval)
     * 3. Unread: an attached agent finished and the user has not looked yet
     * 4. Working: an attached agent is working or starting
     * 5. Follow-up: every attached agent is idle (seen, not running, or failed to start)
     * 6. Not started: no attached agent
     */
    public static func todoStatus(_ todo: TodoItem, lookup: SessionLookup? = nil) -> TaskState {
        let states: [AgentState] = todo.agents.filter { $0.releasedAt == nil }.map { agent in
            if let error = agent.startError, !error.isEmpty { return .idle }
            guard let session = lookup?(agent.machineID, agent.sessionID) else { return .offline }
            if session.state == .needsYou || session.state == .unread { return session.state }
            return session.starting == true ? .working : session.state
        }
        return TaskState(todo: todo, agents: states)
    }

    /**
     * Sorts the todo list:
     * - open todos first; what needs the user (needs you, unread) on top
     * - then by due date, earliest first (an overdue one before the rest), those without one after (#1683)
     * - then by status priority (working > follow-up > not started), newer first, by creation time
     * - done todos at the bottom
     */
    public static func sortTodos(_ todos: [TodoItem], lookup: SessionLookup? = nil, calendar: Calendar = .current) -> [TodoItem] {
        todos.sorted { a, b in
            let aDone = a.isDone
            let bDone = b.isDone
            if aDone != bDone { return !aDone }

            let aStat = todoStatus(a, lookup: lookup)
            let bStat = todoStatus(b, lookup: lookup)
            if (aStat.todoRank <= 1 || bStat.todoRank <= 1) && aStat.todoRank != bStat.todoRank {
                return aStat.todoRank < bStat.todoRank
            }
            if !aDone {
                switch (dueBy(a, calendar: calendar), dueBy(b, calendar: calendar)) {
                case let (x?, y?) where x != y: return x < y
                case (.some, nil): return true
                case (nil, .some): return false
                default: break
                }
            }
            if aStat.todoRank != bStat.todoRank { return aStat.todoRank < bStat.todoRank }

            let tA = a.createdAt?.timeIntervalSince1970 ?? 0
            let tB = b.createdAt?.timeIntervalSince1970 ?? 0
            if tA != tB { return tA > tB }
            return a.id < b.id
        }
    }

    /// The day of a todo's `due_date` ("yyyy-MM-dd") at its start in `calendar`'s time zone; nil without a readable one.
    public static func dueDay(_ todo: TodoItem, calendar: Calendar = .current) -> Date? {
        guard let s = todo.dueDate else { return nil }
        let parts = s.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return nil }
        return calendar.date(from: DateComponents(year: parts[0], month: parts[1], day: parts[2]))
    }

    /// The moment a todo is due by: its `due_at`, or the end of its due day; nil without either.
    public static func dueBy(_ todo: TodoItem, calendar: Calendar = .current) -> Date? {
        if let at = todo.dueAt { return at }
        guard let day = dueDay(todo, calendar: calendar),
              let next = calendar.date(byAdding: .day, value: 1, to: day) else { return nil }
        return next.addingTimeInterval(-1)
    }

    /// An open todo whose due moment has passed.
    public static func isOverdue(_ todo: TodoItem, now: Date = .now, calendar: Calendar = .current) -> Bool {
        guard !todo.isDone, let by = dueBy(todo, calendar: calendar) else { return false }
        return by < now
    }

    /**
     * Finds the most urgent attached agent of a todo, with its summary
     */
    public static func mostUrgentAgent(_ todo: TodoItem, lookup: SessionLookup? = nil) -> TodoMostUrgent? {
        let active = todo.agents.filter { $0.releasedAt == nil }
        if active.isEmpty { return nil }

        func score(_ st: AgentState, isFailed: Bool) -> Int {
            if isFailed { return 1 }
            switch st {
            case .needsYou: return 4
            case .unread: return 3
            case .working: return 2
            default: return 0
            }
        }

        var best: TodoMostUrgent?
        var bestScore = -1

        for a in active {
            let isFailed = a.startError.map { !$0.isEmpty } ?? false
            let s = lookup?(a.machineID, a.sessionID)
            let st = isFailed ? .offline : (s?.state ?? .offline)
            let sc = score(st, isFailed: isFailed)
            if sc > bestScore || best == nil {
                bestScore = sc
                best = TodoMostUrgent(agent: a, session: s, status: st, isFailed: isFailed)
            }
        }

        return best
    }

    private static func sessionRank(_ s: Session, isMuted: (Session) -> Bool) -> Int {
        let state = s.state
        let waiting = (state == .idle || state == .unknown) && (s.loop != nil || !(s.background ?? []).isEmpty || (s.subagents ?? []).contains(where: \.running) || !s.runningWorkflows.isEmpty)
        let muted = isMuted(s)
        if state == .needsYou { return 0 }
        if muted && (state == .working || state == .unread || waiting) { return 4 }
        if state == .unread { return 1 }
        if state == .working { return 2 }
        if waiting { return 3 }
        return 5
    }

    /// An agent opened by hand in herdr: not started by Persona (nor spawned through it).
    static func isManualHerdr(_ s: Session) -> Bool {
        s.spaceKind == "herdr" && !s.startedByPersona
    }

    private static func byDate(_ a: Session, _ b: Session) -> Bool {
        if a.sortDate != b.sortDate { return a.sortDate > b.sortDate }
        if a.sessionID != b.sessionID { return a.sessionID < b.sessionID }
        return a.machineID < b.machineID
    }

    /**
     * Pure grouping for the Agents sidebar, by todo:
     * - one section per open todo with attached agents; the header shows the todo title and status, and tapping it opens the todo
     * - agents with no todo opened by hand in herdr get their own section, "Started Manually", open, before Unassigned (#2608, #2917)
     * - the other agents with no todo go into the last section, "Unassigned": folded below todos unless one of them needs you
     *   (unread alone stays folded, many lists always have some); with no todo above it is the whole list, open (#1836)
     * - pinning, muting and the "Recent (Not Running)" rules match the "By Status" grouping
     */
    public static func buildTodoAgentGroups(
        sessions: [Session],
        todos: [TodoItem],
        isMuted: (Session) -> Bool
    ) -> [TodoAgentGroup] {
        let live = sessions.filter { $0.state.isLive }
        let lookup = makeSessionLookup(live)

        let openTodos = sortTodos(todos.filter { !$0.isDone }, lookup: lookup)

        var liveByKey = [String: Session]()
        for s in live {
            liveByKey["\(s.machineID)/\(s.sessionID)"] = s
        }

        var assignedSessionKeys = Set<String>()
        var groups: [TodoAgentGroup] = []

        for todo in openTodos {
            var todoSessions: [Session] = []
            let activeAgents = todo.agents.filter { $0.releasedAt == nil }

            for a in activeAgents {
                let key = "\(a.machineID)/\(a.sessionID)"
                if let s = liveByKey[key], !assignedSessionKeys.contains(key) {
                    todoSessions.append(s)
                    assignedSessionKeys.insert(key)
                }
            }

            for s in live {
                let key = "\(s.machineID)/\(s.sessionID)"
                if s.todo?.id == todo.id, !assignedSessionKeys.contains(key) {
                    todoSessions.append(s)
                    assignedSessionKeys.insert(key)
                }
            }

            if !todoSessions.isEmpty {
                todoSessions.sort { a, b in
                    let rA = sessionRank(a, isMuted: isMuted)
                    let rB = sessionRank(b, isMuted: isMuted)
                    if rA != rB { return rA < rB }
                    return byDate(a, b)
                }

                let st = todoStatus(todo, lookup: lookup)
                groups.append(TodoAgentGroup(
                    key: "todo:\(todo.id)",
                    name: todo.title,
                    sessions: todoSessions,
                    todoID: todo.id,
                    todoStatus: st
                ))
            }
        }

        let byRank = { (a: Session, b: Session) -> Bool in
            let rA = sessionRank(a, isMuted: isMuted)
            let rB = sessionRank(b, isMuted: isMuted)
            if rA != rB { return rA < rB }
            return byDate(a, b)
        }
        let rest = live.filter { !assignedSessionKeys.contains("\($0.machineID)/\($0.sessionID)") }

        // Opened by hand in herdr and on no todo: no auto-link rule knows them (not spawned, their space is on no
        // todo), so they get a section of their own, open, before Unassigned (#2608, #2917).
        let manual = rest.filter(isManualHerdr).sorted(by: byRank)
        if !manual.isEmpty {
            groups.append(TodoAgentGroup(key: "todo:manual", name: String(localized: "Started Manually"), sessions: manual, todoID: nil, todoStatus: nil))
        }

        let unassigned = rest.filter { !isManualHerdr($0) }
        if !unassigned.isEmpty {
            let sortedUnassigned = unassigned.sorted(by: byRank)
            groups.append(TodoAgentGroup(
                key: "todo:none",
                name: String(localized: "Unassigned"),
                sessions: sortedUnassigned,
                todoID: nil,
                todoStatus: nil,
                folded: !groups.isEmpty && !sortedUnassigned.contains { $0.state == .needsYou }
            ))
        }

        return groups
    }

    /**
     * Format of the context attached when a todo is handed to an Agent (the same as the inbox's)
     * `<persona-context kind="context" title="…" url="">notes</persona-context>`, the title 「待办 · <title>」
     */
    public static func formatTodoContext(title: String, notes: String?) -> String {
        let t = title
            .replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "\"", with: "&quot;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
        let body = ContextText.inBlock((notes ?? "").trimmingCharacters(in: .whitespacesAndNewlines))
        return "<persona-context kind=\"context\" title=\"待办 · \(t)\" url=\"\">\(body)</persona-context>" // i18n-ignore: context sent to the agent
    }
}

// MARK: - APIClient extensions for Todos

extension APIClient {
    struct TodoResponse: Decodable, Sendable {
        var todo: TodoItem
    }

    func linkTodoSpace(todoID: String, machineID: String, space: String) async throws {
        _ = try await request("PUT", TodosLogic.spacePath(todoID: todoID, machineID: machineID, space: space))
    }

    func unlinkTodoSpace(todoID: String, machineID: String, space: String) async throws {
        _ = try await request("DELETE", TodosLogic.spacePath(todoID: todoID, machineID: machineID, space: space))
    }

    public struct CreateTodoRequest: Encodable, Sendable {
        var title: String
        var notes: String?
    }

    public func createTodo(title: String, notes: String? = nil) async throws -> TodoItem {
        let body = CreateTodoRequest(title: title.trimmingCharacters(in: .whitespacesAndNewlines), notes: notes?.trimmingCharacters(in: .whitespacesAndNewlines))
        return try await post("/api/v1/todos", body: body, as: TodoResponse.self).todo
    }

    public struct UpdateTodoRequest: Encodable, Sendable {
        var title: String?
        var notes: String?
        var done: Bool?
    }

    public func updateTodo(id: String, title: String? = nil, notes: String? = nil, done: Bool? = nil) async throws -> TodoItem {
        let encId = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        let body = UpdateTodoRequest(title: title, notes: notes, done: done)
        let data = try await request("PATCH", "/api/v1/todos/\(encId)", body: body)
        return try JSON.decoder.decode(TodoResponse.self, from: data).todo
    }

    /// A change to a todo's due date, reminder or priority (#1683): nil leaves a field as it is; `.none` / a nil
    /// reminder clear it (sent as JSON null).
    nonisolated public struct TodoPlanChange: Encodable, Sendable, Equatable {
        nonisolated public enum Due: Sendable, Equatable {
            case none
            /// Due at a moment.
            case at(Date)
            /// Due on a day, "yyyy-MM-dd" in the user's time zone.
            case day(String)
        }

        public var due: Due?
        public var remindAt: Date??
        /// "" normal, "high", "low".
        public var priority: String?

        public init(due: Due? = nil, remindAt: Date?? = nil, priority: String? = nil) {
            self.due = due
            self.remindAt = remindAt
            self.priority = priority
        }

        enum CodingKeys: String, CodingKey {
            case priority
            case dueAt = "due_at", dueDate = "due_date", remindAt = "remind_at"
        }

        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            switch due {
            case nil: break
            case .some(.none): try c.encodeNil(forKey: .dueAt)
            case .some(.at(let d)): try c.encode(d, forKey: .dueAt)
            case .some(.day(let day)): try c.encode(day, forKey: .dueDate)
            }
            if let remindAt {
                if let d = remindAt { try c.encode(d, forKey: .remindAt) } else { try c.encodeNil(forKey: .remindAt) }
            }
            if let priority { try c.encode(priority, forKey: .priority) }
        }
    }

    public func updateTodoPlan(id: String, _ change: TodoPlanChange) async throws -> TodoItem {
        let encId = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        let data = try await request("PATCH", "/api/v1/todos/\(encId)", body: change)
        return try JSON.decoder.decode(TodoResponse.self, from: data).todo
    }

    public func deleteTodo(id: String) async throws {
        let encId = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        _ = try await request("DELETE", "/api/v1/todos/\(encId)")
    }

    public func linkTodoAgent(todoID: String, machineID: String, sessionID: String) async throws {
        let encTodo = todoID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? todoID
        let encMid = machineID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? machineID
        let encSid = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        _ = try await request("PUT", "/api/v1/todos/\(encTodo)/agents/\(encMid)/\(encSid)")
    }

    public func unlinkTodoAgent(todoID: String, machineID: String, sessionID: String) async throws {
        let encTodo = todoID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? todoID
        let encMid = machineID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? machineID
        let encSid = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        _ = try await request("DELETE", "/api/v1/todos/\(encTodo)/agents/\(encMid)/\(encSid)")
    }
}

/// The text a todo's detail page edits: its title and notes. The page takes the stored text when it shows a todo, and
/// keeps what the user is typing, so one todo's text never lands in another todo's fields.
nonisolated struct TodoEditorText: Equatable {
    var title: String
    var notes: String

    init(title: String = "", notes: String = "") {
        self.title = title
        self.notes = notes
    }

    init(_ todo: TodoItem) {
        title = todo.title
        notes = todo.notes ?? ""
    }

    /// Takes the stored text of `todo`, except in a field the user is editing.
    mutating func reseed(from todo: TodoItem, editingTitle: Bool, editingNotes: Bool) {
        if !editingTitle { title = todo.title }
        if !editingNotes { notes = todo.notes ?? "" }
    }
}

/// The session marker remains authoritative even before the todo list is available.
nonisolated enum TodoLinkTarget: Equatable, Sendable {
    case agent(machineID: String, sessionID: String, currentTodoID: String?)
    case space(TodoItem.Space)

    var id: String {
        switch self {
        case .agent(let machine, let session, _): "agent/\(machine)/\(session)"
        case .space(let project): "space/\(project.machineID)/\(project.space)"
        }
    }

    var isSpace: Bool { if case .space = self { true } else { false } }

    func currentTodoID(in todos: [TodoItem]) -> String? {
        switch self {
        case .agent(_, _, let current): current
        case .space(let project): TodosLogic.findTodoForSpace(todos, machineID: project.machineID, space: project.space)?.id
        }
    }
}

nonisolated struct TodoLinkDraft: Equatable {
    var selectedTodoID = ""
    var creating = false
    var newTitle = ""
    var busy = false

    mutating func reset(currentTodoID: String?) {
        self = TodoLinkDraft(selectedTodoID: currentTodoID ?? "")
    }
}
