import Foundation
import Testing
@testable import Persona

@Suite(.serialized)
@MainActor
struct TodosLogicTests {
    private func session(_ id: String, state: AgentState, updated: TimeInterval = 0) -> Session {
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp","status":"idle","state":"\(state.rawValue)",
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    @Test func todoStatusPriorityOrder() {
        // 1. Done: done_at != nil
        var tDone = TodoItem(id: "1", title: "Done", doneAt: Date(), createdAt: Date(), updatedAt: Date())
        #expect(TodosLogic.todoStatus(tDone) == .done)

        // 6. Not started: no agent is attached
        let tNotStarted = TodoItem(id: "2", title: "NotStarted", doneAt: nil, createdAt: Date(), updatedAt: Date(), agents: [])
        #expect(TodosLogic.todoStatus(tNotStarted) == .notStarted)

        // 2. Needs you: an agent needs the user
        let sNeedsYou = session("s1", state: .needsYou)
        let sWorking = session("s2", state: .working)
        let sUnread = session("s3", state: .unread)
        let sIdle = session("s4", state: .idle)
        let lookup = TodosLogic.makeSessionLookup([sNeedsYou, sWorking, sUnread, sIdle])

        let a1 = TodoItem.Agent(machineID: "m", sessionID: "s1")
        let a2 = TodoItem.Agent(machineID: "m", sessionID: "s2")
        let a3 = TodoItem.Agent(machineID: "m", sessionID: "s3")
        let a4 = TodoItem.Agent(machineID: "m", sessionID: "s4")

        let tNeedsYou = TodoItem(id: "3", title: "NeedsYou", agents: [a1, a2, a3, a4])
        #expect(TodosLogic.todoStatus(tNeedsYou, lookup: lookup) == .needsYou)

        // 3. Unread: an agent finished and nobody has looked at it
        let tUnread = TodoItem(id: "4", title: "Unread", agents: [a2, a3, a4])
        #expect(TodosLogic.todoStatus(tUnread, lookup: lookup) == .unread)

        // 4. Working: an agent is working
        let tWorking = TodoItem(id: "5", title: "Working", agents: [a2, a4])
        #expect(TodosLogic.todoStatus(tWorking, lookup: lookup) == .working)

        // 5. Follow-up: every agent is idle
        let tFollowUp = TodoItem(id: "6", title: "FollowUp", agents: [a4])
        #expect(TodosLogic.todoStatus(tFollowUp, lookup: lookup) == .followup)

        // A failed start counts as idle
        let aFailed = TodoItem.Agent(machineID: "m", sessionID: "s_failed", startError: "failed to start")
        let tFailed = TodoItem(id: "7", title: "Failed", agents: [aFailed])
        #expect(TodosLogic.todoStatus(tFailed, lookup: lookup) == .followup)
    }

    @Test func sortTodosOrder() {
        let now = Date()
        let tNeedsYou = TodoItem(id: "needs", title: "Needs", doneAt: nil, createdAt: now.addingTimeInterval(-100), agents: [TodoItem.Agent(machineID: "m", sessionID: "s_needs")])
        let tWorkingNew = TodoItem(id: "work_new", title: "WorkNew", doneAt: nil, createdAt: now.addingTimeInterval(-10), agents: [TodoItem.Agent(machineID: "m", sessionID: "s_work")])
        let tWorkingOld = TodoItem(id: "work_old", title: "WorkOld", doneAt: nil, createdAt: now.addingTimeInterval(-50), agents: [TodoItem.Agent(machineID: "m", sessionID: "s_work")])
        let tDone = TodoItem(id: "done", title: "Done", doneAt: now, createdAt: now.addingTimeInterval(-5), agents: [])

        let sNeeds = session("s_needs", state: .needsYou)
        let sWork = session("s_work", state: .working)
        let lookup = TodosLogic.makeSessionLookup([sNeeds, sWork])

        let sorted = TodosLogic.sortTodos([tDone, tWorkingOld, tNeedsYou, tWorkingNew], lookup: lookup)
        #expect(sorted.map(\.id) == ["needs", "work_new", "work_old", "done"])
    }

    @Test func todoAgentGroupsAndUnassigned() {
        let s1 = session("s1", state: .working)
        let s2 = session("s2", state: .idle)
        let sUnassigned = session("s_unassigned", state: .working)

        let t1 = TodoItem(id: "t1", title: "Todo 1", doneAt: nil, createdAt: Date(), agents: [TodoItem.Agent(machineID: "m", sessionID: "s1")])
        var s2WithTodo = s2
        s2WithTodo.todo = SessionTodoRef(id: "t1", title: "Todo 1")

        let groups = TodosLogic.buildTodoAgentGroups(
            sessions: [s1, s2WithTodo, sUnassigned],
            todos: [t1],
            isMuted: { _ in false }
        )

        #expect(groups.count == 2)
        #expect(groups[0].key == "todo:t1")
        #expect(groups[0].name == "Todo 1")
        #expect(groups[0].sessions.map(\.sessionID) == ["s1", "s2"])

        #expect(groups[1].key == "todo:none")
        #expect(groups[1].name == "未归属")
        #expect(groups[1].folded)
        #expect(groups[1].sessions.map(\.sessionID) == ["s_unassigned"])
    }

    /// Agents opened by hand in herdr with no todo get "Started Manually", open, before Unassigned; Persona's own and other hosts' stay
    /// in Unassigned (#2917).
    @Test func manualHerdrAgentsGetTheirOwnSection() throws {
        func agent(_ id: String, kind: String?, byPersona: Bool, state: AgentState = .working) throws -> Session {
            let kindField = kind.map { ",\"space_kind\":\"\($0)\"" } ?? ""
            let json = #"{"machine_id":"m","session_id":"\#(id)","title":"\#(id)","cwd":"/tmp","status":"idle","state":"\#(state.rawValue)","started_by_persona":\#(byPersona)\#(kindField)}"#
            return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        }
        let onTodo = try agent("on_todo", kind: "herdr", byPersona: false)
        let manualIdle = try agent("manual_idle", kind: "herdr", byPersona: false, state: .idle)
        let manualNeeds = try agent("manual_needs", kind: "herdr", byPersona: false, state: .needsYou)
        let spawned = try agent("spawned", kind: "herdr", byPersona: true)
        let other = try agent("other", kind: nil, byPersona: false)
        let todo = TodoItem(id: "t1", title: "Todo 1", doneAt: nil, createdAt: Date(), agents: [TodoItem.Agent(machineID: "m", sessionID: "on_todo")])

        let groups = TodosLogic.buildTodoAgentGroups(
            sessions: [onTodo, manualIdle, manualNeeds, spawned, other], todos: [todo], isMuted: { _ in false })
        #expect(groups.map(\.key) == ["todo:t1", "todo:manual", "todo:none"])
        #expect(groups[1].name == "手动开的")
        #expect(!groups[1].folded)
        #expect(groups[1].sessions.map(\.sessionID) == ["manual_needs", "manual_idle"])
        #expect(groups[2].folded)
        #expect(Set(groups[2].sessions.map(\.sessionID)) == ["spawned", "other"])

        // Nothing opened by hand: no empty section.
        let none = TodosLogic.buildTodoAgentGroups(sessions: [onTodo, spawned], todos: [todo], isMuted: { _ in false })
        #expect(none.map(\.key) == ["todo:t1", "todo:none"])
    }

    /// The host from the pane ref, as the hub's host.ParsePane: herdr's ids go bare and may hold a colon (#1836).
    @Test func hostKindFromPaneRef() throws {
        func host(_ pane: String?) throws -> String? {
            let field = pane.map { ",\"pane_id\":\"\($0)\"" } ?? ""
            let json = #"{"machine_id":"m","session_id":"s","title":"t","cwd":"/tmp","status":"idle","state":"idle"\#(field)}"#
            return try JSON.decoder.decode(Session.self, from: Data(json.utf8)).hostKind
        }
        #expect(try host("botmux:abc") == "botmux")
        #expect(try host("orca:t1:l2") == "orca")
        #expect(try host("w1:p1") == "herdr")
        #expect(try host("p1") == "herdr")
        #expect(try host("herdr:p1") == "herdr")
        #expect(try host(":botmux:x") == "herdr")
        #expect(try host(nil) == nil)
    }

    @Test func formatTodoContextOutput() {
        let xml = TodosLogic.formatTodoContext(title: "测试 <&> \"待办\"", notes: "说明内容")
        #expect(xml == "<persona-context kind=\"context\" title=\"待办 · 测试 &lt;&amp;&gt; &quot;待办&quot;\" url=\"\">说明内容</persona-context>")
    }

    /// An older hub answers GET /api/v1/todos with 404: the store's refresh hides the entrance and shows no error.
    @Test func refreshHidesTheEntranceOnAnOldHub() async {
        let hub = FakeHub()  // no todos route, so the fake hub answers 404
        let store = TodoStore.shared
        store.forget()
        await store.refresh(api: hub.client)
        #expect(store.isSupported == false)
        #expect(store.error == nil)
    }

    /// A write that fails (the fake hub has no PATCH route, so it answers 404) puts the list back as it was: the optimistic
    /// "done" is undone, not left showing.
    @Test func toggleDoneRollsBackWhenTheWriteFails() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t1","title":"Rollback"}]}"#)
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let before = store.todos
        let todo = try #require(store.todo(id: "t1"))

        store.toggleDone(todo, api: hub.client)
        #expect(store.todo(id: "t1")?.isDone == true)  // the optimistic change shows at once
        var waited = 0
        while store.todo(id: "t1")?.isDone == true, waited < 200 {
            try await Task.sleep(for: .milliseconds(10))
            waited += 1
        }
        #expect(store.todos == before)
    }

    /// Only a 404 from GET /api/v1/todos means an old hub (the sidebar entrance hides); other failures keep it shown.
    @Test func only404MarksAnOldHub() {
        #expect(TodosLogic.isOldHub(APIError(status: 404, message: "no route")))
        #expect(!TodosLogic.isOldHub(APIError(status: 500, message: "boom")))
        #expect(!TodosLogic.isOldHub(URLError(.notConnectedToInternet)))
    }

    /// The detail page reads a todo's text from the store when it shows that todo: another todo's text never lands in
    /// the fields (the Mac list picks a different todo on the same page).
    @Test func editorShowsTheTextOfTheTodoItIsGiven() {
        let a = TodoItem(id: "a", title: "Todo A", notes: "Notes A")
        let b = TodoItem(id: "b", title: "Todo B", notes: nil)
        var text = TodoEditorText(a)
        text.reseed(from: b, editingTitle: false, editingNotes: false)
        #expect(text == TodoEditorText(title: "Todo B", notes: ""))
    }

    /// A field the user is typing in keeps their text; the stored text fills the other field.
    @Test func editorKeepsTheFieldBeingEdited() {
        let a = TodoItem(id: "a", title: "Todo A", notes: "Notes A")
        let stored = TodoItem(id: "a", title: "Renamed elsewhere", notes: "Notes elsewhere")
        var text = TodoEditorText(a)
        text.title = "Still typing"
        text.reseed(from: stored, editingTitle: true, editingNotes: false)
        #expect(text == TodoEditorText(title: "Still typing", notes: "Notes elsewhere"))
    }

    /// 「按待办」 needs the hub's todos: an old hub does not offer it, and a stored choice of it falls back to 「按项目」.
    @Test func todoGroupingNeedsTheHubsTodos() {
        #expect(!DevGrouping.offered(supportsTodos: false).contains(.todo))
        #expect(DevGrouping.offered(supportsTodos: true).contains(.todo))
        #expect(DevGrouping.resolved(.todo, supportsTodos: false) == .space)
        #expect(DevGrouping.resolved(.todo, supportsTodos: true) == .todo)
        #expect(DevGrouping.resolved(.state, supportsTodos: false) == .state)
    }

    /// A session takes a todo's actions only on the selected hub: on another hub group the request would go to the
    /// wrong hub and find no todo.
    /// The Agents of the todos are read one by one on the hub's group; an Agent that is not known is skipped.
    @Test func linkedSessionsAreReadPerAgentOnTheHubGroup() {
        let known = session("s1", state: .working)
        let todo = TodoItem(id: "t", title: "T", agents: [TodoItem.Agent(machineID: "m", sessionID: "s1"),
                                                      TodoItem.Agent(machineID: "m", sessionID: "gone")])
        var asked: [SessionKey] = []
        let found = TodosLogic.linkedSessions(of: [todo], hubGroup: "work") { key in
            asked.append(key)
            return key.sessionID == "s1" ? known : nil
        }
        #expect(found.map(\.sessionID) == ["s1"])
        #expect(asked.count == 2)
        #expect(asked.allSatisfy { $0.group == "work" })
    }

    @Test func todoActionsStayOnTheSelectedHub() {
        #expect(TodosLogic.isOnTodosHub(sessionGroup: "", hubGroup: nil))
        #expect(TodosLogic.isOnTodosHub(sessionGroup: "work", hubGroup: "work"))
        #expect(!TodosLogic.isOnTodosHub(sessionGroup: "home", hubGroup: "work"))
        #expect(!TodosLogic.isOnTodosHub(sessionGroup: "home", hubGroup: nil))
    }

    @Test func todoPresentationPreservesLabelsOrderAndColors() {
        let states: [TaskState] = [.needsYou, .unread, .working, .followup, .notStarted, .done, .failed]
        #expect(states.map(\.todoLabel) == ["需要你", "完成未看", "工作中", "待跟进", "未开始", "已完成", "没开起来"])
        #expect(states.map(\.todoRank) == [0, 1, 2, 3, 4, 5, 6])
        #expect(TaskState.allCases.sorted { $0.todoRank < $1.todoRank } == states)
        let tones = [StatusPalette.needsYou, StatusPalette.unread, StatusPalette.working,
                     StatusPalette.idle, StatusPalette.idle, StatusPalette.idle, StatusPalette.idle]
        for (state, tone) in zip(states, tones) {
            #expect(state.todoTone.dot == tone.dot)
            #expect(state.todoTone.text == tone.text)
            #expect(state.todoTone.tint == tone.tint)
        }
    }

    @Test func missingActiveAgentNeedsFollowup() {
        let todo = TodoItem(id: "missing", title: "Missing", agents: [.init(machineID: "m", sessionID: "gone")])
        #expect(TodosLogic.todoStatus(todo, lookup: TodosLogic.makeSessionLookup([])) == .followup)
    }

    @Test func failedAgentCountsAsIdleEvenWithAWorkingSession() {
        let todo = TodoItem(id: "failed", title: "Failed", agents: [
            .init(machineID: "m", sessionID: "s", startError: "failed to start")
        ])
        let lookup = TodosLogic.makeSessionLookup([session("s", state: .working)])
        #expect(TodosLogic.todoStatus(todo, lookup: lookup) == .followup)
    }

    @Test func releasedAgentsDoNotAffectTheState() {
        let todo = TodoItem(id: "released", title: "Released", agents: [
            .init(machineID: "m", sessionID: "s", releasedAt: Date())
        ])
        let lookup = TodosLogic.makeSessionLookup([session("s", state: .needsYou)])
        #expect(TodosLogic.todoStatus(todo, lookup: lookup) == .notStarted)
    }

    @Test func startingAgentStillCountsAsWorking() {
        var starting = session("s", state: .idle)
        starting.starting = true
        let todo = TodoItem(id: "starting", title: "Starting", agents: [.init(machineID: "m", sessionID: "s")])
        #expect(TodosLogic.todoStatus(todo, lookup: TodosLogic.makeSessionLookup([starting])) == .working)
    }

    @Test func legacyTodoItemsCacheDecodes() throws {
        // The old Codable model omitted nil agents and optional fields, and had no origin or plan_id.
        let data = Data(#"""
        [{"id":"empty","title":"Empty"},
         {"id":"null","title":"Null","agents":null},
         {"id":"full","title":"Full","notes":"Notes","done_at":"2026-01-02T03:04:05Z",
          "created_at":"2026-01-01T03:04:05Z","updated_at":"2026-01-02T03:04:05Z",
          "agents":[{"machine_id":"m","session_id":"s","title":"Agent","cwd":"/tmp",
                     "agent":"codex","linked_at":"2026-01-01T03:04:05Z",
                     "released_at":"2026-01-02T03:04:05Z","start_error":"failed"}]}]
        """#.utf8)
        let todos = try JSON.decoder.decode([TodoItem].self, from: data)
        #expect(todos.count == 3)
        #expect(todos[0].agents.isEmpty)
        #expect(todos[1].agents.isEmpty)
        let todo = todos[2]
        #expect(todo.id == "full" && todo.title == "Full" && todo.notes == "Notes")
        #expect(todo.isDone && todo.createdAt != nil && todo.updatedAt != nil)
        #expect(todo.origin == nil && todo.planID == nil)
        let agent = try #require(todo.agents.first)
        #expect(agent.machineID == "m" && agent.sessionID == "s" && agent.title == "Agent")
        #expect(agent.cwd == "/tmp" && agent.agent == "codex" && agent.startError == "failed")
        #expect(agent.linkedAt != nil && agent.releasedAt != nil)
    }

    @Test func spacesCapabilityDistinguishesMissingNullAndEmpty() throws {
        func decode(_ extra: String) throws -> TodoItem {
            try JSON.decoder.decode(TodoItem.self, from: Data("{\"id\":\"t\",\"title\":\"T\"\(extra)}".utf8))
        }
        let legacy = try decode("")
        let null = try decode(",\"spaces\":null")
        let modern = try decode(",\"spaces\":[]")
        #expect(legacy.spaces == nil && null.spaces == nil)
        #expect(modern.spaces == [])
        #expect(TodosLogic.supportsSpaces([]))
        #expect(!TodosLogic.supportsSpaces([legacy, null]))
        #expect(TodosLogic.supportsSpaces([legacy, modern]))
        let restored = try JSON.decoder.decode([TodoItem].self, from: JSON.encoder.encode([legacy, modern]))
        #expect(restored == [legacy, modern])
        #expect(restored[0].agents.isEmpty)
    }

    @Test func spacesAndAgentOriginsDecodeWithoutChangingExistingFields() throws {
        let data = Data(#"""
        {"id":"t","title":"T","origin":"conversation","plan_id":"plan",
         "spaces":[{"machine_id":"m","space":"项目/a b","linked_at":"2026-01-01T00:00:00Z"}],
         "agents":[{"machine_id":"m","session_id":"s","source":"space","via":"m/项目/a b"},
                   {"machine_id":"m","session_id":"old"}]}
        """#.utf8)
        let todo = try JSON.decoder.decode(TodoItem.self, from: data)
        #expect(todo.origin == "conversation" && todo.planID == "plan")
        #expect(todo.spaces?.first?.space == "项目/a b")
        #expect(todo.spaces?.first?.linkedAt != nil)
        #expect(todo.agents[0].source == "space" && todo.agents[0].via == "m/项目/a b")
        #expect(todo.agents[1].source == nil && todo.agents[1].via == nil)
    }

    @Test func projectsMatchBothMachineAndExactNameAndIgnoreDoneTodos() {
        let space = TodoItem.Space(machineID: "m", space: "项目/a b")
        let done = TodoItem(id: "done", title: "Done", doneAt: Date(), spaces: [space])
        let open = TodoItem(id: "open", title: "Open", spaces: [space])
        #expect(TodosLogic.findTodoForSpace([done, open], machineID: "m", space: "项目/a b")?.id == "open")
        #expect(TodosLogic.findTodoForSpace([open], machineID: "other", space: "项目/a b") == nil)
        #expect(TodosLogic.findTodoForSpace([open], machineID: "m", space: "项目/a") == nil)
        #expect(TodosLogic.findTodoForSpace([open], machineID: "", space: "项目/a b") == nil)
        #expect(TodosLogic.findTodoForSpace([open], machineID: "m", space: "") == nil)
    }

    @Test func projectPathsEncodeSpacesSlashesAndChineseOnce() async throws {
        let path = "/api/v1/todos/t%2F1/spaces/m%20one/%E4%B8%AD%E6%96%87%20a%2Fb"
        #expect(TodosLogic.spacePath(todoID: "t/1", machineID: "m one", space: "中文 a/b") == path)
        #expect(APIClient.encodedPath(path) == path)
        let hub = FakeHub()
        hub.on("PUT", path, json: "{}")
        hub.on("DELETE", path, json: "{}")
        try await hub.client.linkTodoSpace(todoID: "t/1", machineID: "m one", space: "中文 a/b")
        try await hub.client.unlinkTodoSpace(todoID: "t/1", machineID: "m one", space: "中文 a/b")
        #expect(hub.sent("PUT", path).count == 1)
        #expect(hub.sent("DELETE", path).count == 1)
    }

    @Test func projectOriginUsesOnlyViaAndNeverAnotherProject() {
        var unrelated = session("s", state: .working)
        unrelated.spaceName = "wrong-project"
        let lookup = TodosLogic.makeSessionLookup([unrelated])
        var agent = TodoItem.Agent(machineID: "m", sessionID: "s", source: "space", via: "m/project/sub")
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == "随项目 project/sub")
        agent.via = "project"
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == "随项目 project")
        agent.via = nil
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == "随项目")
        agent.via = "m/"
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == "随项目")
        agent.source = "manual"
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == nil)
        agent.source = nil
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == nil)
    }

    @Test func spawnedOriginUsesTheParentOrSaysDerived() {
        var parent = session("parent", state: .offline)
        parent.machineID = "remote"
        parent.title = "Parent task"
        let lookup = TodosLogic.makeSessionLookup([parent])
        var agent = TodoItem.Agent(machineID: "m", sessionID: "child", source: "spawned", via: "remote/parent")
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == "由 Parent task 派出")
        agent.via = "remote/missing"
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == "派生")
        agent.via = nil
        #expect(TodosLogic.agentSource(agent, lookup: lookup) == "派生")
    }

    /// `hasLiveAgent` on the default group with every computer connected.
    private func live(_ project: TodoItem.Space, _ sessions: [Session]) -> Bool {
        TodosLogic.hasLiveAgent(in: project, sessions: sessions, hubGroup: nil, machineOnline: { _ in true })
    }

    @Test func anotherGroupsComputerWithTheSameIDDoesNotCount() {
        let project = TodoItem.Space(machineID: "m", space: "project")
        var other = session("s", state: .working)
        other.spaceName = "project"
        other.group = "home"
        #expect(!TodosLogic.hasLiveAgent(in: project, sessions: [other], hubGroup: nil, machineOnline: { _ in true }))
        #expect(!TodosLogic.hasLiveAgent(in: project, sessions: [other], hubGroup: "work", machineOnline: { _ in true }))
        #expect(TodosLogic.hasLiveAgent(in: project, sessions: [other], hubGroup: "home", machineOnline: { _ in true }))
    }

    @Test func aDisconnectedComputersAgentsDoNotCount() {
        let project = TodoItem.Space(machineID: "m", space: "project")
        var active = session("s", state: .unknown) // what the hub makes of a disconnected computer's sessions
        active.spaceName = "project"
        #expect(!TodosLogic.hasLiveAgent(in: project, sessions: [active], hubGroup: nil, machineOnline: { $0 != "m" }))
        #expect(TodosLogic.hasLiveAgent(in: project, sessions: [active], hubGroup: nil, machineOnline: { $0 == "m" }))
    }

    @Test func projectWithoutALiveAgentRemainsVisibleAsEmpty() {
        let project = TodoItem.Space(machineID: "m", space: "project")
        var active = session("s", state: .working)
        active.spaceName = "project"
        #expect(live(project, [active]))
        active.state = .offline
        #expect(!live(project, [active]))
        #expect(!live(project, []))
    }

    @Test func projectPresenceUsesAllOnlineSessionsAndTheHubKey() {
        let project = TodoItem.Space(machineID: "m", space: "project")
        var active = session("s", state: .working)
        active.spaceID = "project"
        active.spaceName = ""
        active.todo = SessionTodoRef(id: "other", title: "Other")
        #expect(live(project, [active]))
        active.spaceName = nil
        #expect(live(project, [active]))
        active.machineID = "other"
        #expect(!live(project, [active]))
        active.machineID = "m"
        active.state = .offline
        #expect(!live(project, [active]))
    }

    @Test func sessionMarkerControlsUnlinkAndCurrentHighlightWithoutTheList() {
        let target = TodoLinkTarget.agent(machineID: "m", sessionID: "s", currentTodoID: "from-session")
        #expect(target.currentTodoID(in: []) == "from-session")
        #expect(target.currentTodoID(in: [TodoItem(id: "other", title: "Other")]) == "from-session")
        var draft = TodoLinkDraft()
        draft.reset(currentTodoID: target.currentTodoID(in: []))
        #expect(draft.selectedTodoID == "from-session")
    }

    @Test func linkDraftResetsOnEachOpeningAndProjectChange() {
        let first = TodoLinkTarget.space(.init(machineID: "m", space: "first"))
        let second = TodoLinkTarget.space(.init(machineID: "m", space: "second"))
        #expect(first.id != second.id)
        var draft = TodoLinkDraft(selectedTodoID: "old", creating: true, newTitle: "stale", busy: true)
        draft.reset(currentTodoID: "current")
        #expect(draft == TodoLinkDraft(selectedTodoID: "current"))
        draft.newTitle = "typed again"
        draft.creating = true
        draft.reset(currentTodoID: second.currentTodoID(in: []))
        #expect(draft == TodoLinkDraft())
    }

    @Test func spaces404HidesEntriesAndRefreshRecomputesSupport() async throws {
        let hub = FakeHub()
        let store = TodoStore.shared
        store.forget()
        #expect(!store.spacesSupported)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        await store.refresh(api: hub.client)
        #expect(store.spacesSupported)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"T","spaces":[]}]}"#)
        await store.refresh(api: hub.client)
        #expect(store.spacesSupported)
        let original = store.todos
        let routeError = hubResponseError(#"{"error":"Not Found","code":"not_found","retryable":false}"#)
        hub.on("PUT", "/api/v1/todos/t/spaces/m/p") { _ in throw routeError }
        await #expect(throws: APIError.self) {
            try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: hub.client)
        }
        #expect(!store.spacesSupported)
        #expect(store.error == "这台 hub 还不支持整组挂，升级 hub 后再试")
        #expect(store.todos == original)
        await store.refresh(api: hub.client)
        #expect(store.spacesSupported)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Legacy"}]}"#)
        await store.refresh(api: hub.client)
        #expect(!store.spacesSupported)
    }

    @Test func spaceWriteIsOptimisticAndRollsBackWithAnError() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"T","spaces":[]}]}"#)
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let original = store.todos
        var client = hub.client
        client.transport = { request in
            if request.method == "PUT" {
                await MainActor.run {
                    #expect(TodosLogic.findTodoForSpace(store.todos, machineID: "m", space: "p")?.id == "t")
                    #expect(store.changingSpaces)
                }
                throw APIError(status: 500, message: "write failed")
            }
            return try await hub.client.transport!(request)
        }
        await #expect(throws: APIError.self) {
            try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: client)
        }
        #expect(store.todos == original)
        #expect(store.error?.contains("write failed") == true)
        #expect(!store.changingSpaces)
    }

    @Test func successfulSpaceWritesReloadAndUnlinkKeepsManualAndSpawnedAgents() async throws {
        let hub = FakeHub()
        let before = #"{"todos":[{"id":"t","title":"T","spaces":[{"machine_id":"m","space":"p"}],"agents":[{"machine_id":"m","session_id":"space","source":"space","via":"m/p"},{"machine_id":"m","session_id":"manual","source":"manual"},{"machine_id":"m","session_id":"child","source":"spawned","via":"m/parent"}]}]}"#
        let after = #"{"todos":[{"id":"t","title":"T","spaces":[],"agents":[{"machine_id":"m","session_id":"manual","source":"manual"},{"machine_id":"m","session_id":"child","source":"spawned","via":"m/parent"}]}]}"#
        hub.on("GET", "/api/v1/todos", json: before)
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        var client = hub.client
        client.transport = { request in
            if request.method == "DELETE" {
                await MainActor.run {
                    #expect(store.todos[0].spaces == [])
                    #expect(store.todos[0].agents.map(\.sessionID) == ["manual", "child"])
                }
                hub.on("GET", "/api/v1/todos", json: after)
                return Data("{}".utf8)
            }
            return try await hub.client.transport!(request)
        }
        #expect(try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: false, api: client))
        #expect(store.todos[0].spaces == [])
        #expect(store.todos[0].agents.map(\.sessionID) == ["manual", "child"])
        #expect(hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test func createdTodoSurvivesALinkFailureAndTheMessageSaysSo() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        hub.on("POST", "/api/v1/todos") { _ in
            hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"Created","spaces":[]}]}"#)
            return Data(#"{"todo":{"id":"new","title":"Created","spaces":[]}}"#.utf8)
        }
        hub.on("PUT", "/api/v1/todos/new/spaces/m/p") { _ in throw APIError(status: 500, message: "link failed") }
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let message = try await store.createAndLink(title: "Created", target: .space(.init(machineID: "m", space: "p")), api: hub.client)
        #expect(store.todo(id: "new")?.title == "Created")
        #expect(message == "待办已创建，但挂到待办失败：link failed")
    }

    /// Reproduce request(_:_:)'s checked response mapping, including the hub's mixed-type JSON bodies.
    private func hubResponseError(_ body: String, status: Int = 404) -> APIError {
        let data = Data(body.utf8)
        return APIError(status: status,
                        message: APIError.message(in: data) ?? HTTPURLResponse.localizedString(forStatusCode: status),
                        fields: APIError.fields(in: data))
    }

    @Test(arguments: [500, 0]) func refreshKeepsSpacesSupportDuringRequestsAndOrdinaryFailures(_ status: Int) async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"T","spaces":[]}]}"#)
        let store = TodoStore.shared
        store.forget()
        #expect(!store.spacesSupported)
        await store.refresh(api: hub.client)
        #expect(store.spacesSupported)
        let original = store.todos
        var client = hub.client
        client.transport = { _ in
            await MainActor.run {
                #expect(store.loading)
                #expect(store.spacesSupported)
                #expect(store.todos == original)
            }
            if status == 500 { throw APIError(status: 500, message: "refresh failed") }
            throw URLError(.timedOut)
        }
        await store.refresh(api: client)
        #expect(store.spacesSupported)
        #expect(store.isSupported)
        #expect(store.todos == original)
        #expect(!store.loading)
    }

    @Test func get404AndForgetStillClearSpacesSupport() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        let store = TodoStore.shared
        store.forget()
        #expect(!store.spacesSupported)
        await store.refresh(api: hub.client)
        #expect(store.spacesSupported)
        let routeError = hubResponseError(#"{"error":"Not Found","code":"not_found","retryable":false}"#)
        hub.on("GET", "/api/v1/todos") { _ in throw routeError }
        await store.refresh(api: hub.client)
        #expect(!store.isSupported && !store.spacesSupported)
        #expect(store.error == nil)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        await store.refresh(api: hub.client)
        #expect(store.spacesSupported)
        store.forget()
        #expect(!store.spacesSupported)
    }

    @Test func spacesRouteDetectionUsesRawHubFieldsNotTheDisplayMessage() {
        var legacy = hubResponseError(#"{"error":"Not Found","code":"not_found","retryable":false}"#)
        #expect(legacy.fields == ["error": "Not Found", "code": "not_found", "retryable": "false"])
        legacy.message = "localized display text"
        #expect(TodosLogic.isLegacySpacesRoute(legacy))
        var business = hubResponseError(#"{"error":"todo not found","code":"not_found","retryable":false}"#)
        #expect(business.fields["error"] == "todo not found")
        business.message = "Not Found"
        #expect(!TodosLogic.isLegacySpacesRoute(business))
        let plain = hubResponseError("404 page not found\n")
        #expect(plain.fields.isEmpty)
        #expect(TodosLogic.isLegacySpacesRoute(plain))
        #expect(!TodosLogic.isLegacySpacesRoute(APIError(status: 500, message: "Not Found")))
        #expect(!TodosLogic.isLegacySpacesRoute(URLError(.timedOut)))
    }

    @Test(arguments: [true, false]) func plainTextSpaces404RollsBackAndRequestsAnUpgrade(_ linked: Bool) async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"T","spaces":[{"machine_id":"m","space":"p"}]}]}"#)
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let original = store.todos
        let error = hubResponseError("404 page not found\n")
        hub.on(linked ? "PUT" : "DELETE", "/api/v1/todos/t/spaces/m/p") { _ in throw error }
        await #expect(throws: APIError.self) {
            try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: linked, api: hub.client)
        }
        #expect(!store.spacesSupported)
        #expect(store.error == "这台 hub 还不支持整组挂，升级 hub 后再试")
        #expect(store.todos == original)
    }

    @Test(arguments: [true, false]) func businessSpaces404PreservesSupportAndShowsTheError(_ linked: Bool) async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"T","spaces":[{"machine_id":"m","space":"p"}]}]}"#)
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let original = store.todos
        let error = hubResponseError(#"{"error":"todo not found","code":"not_found","retryable":false}"#)
        hub.on(linked ? "PUT" : "DELETE", "/api/v1/todos/t/spaces/m/p") { _ in throw error }
        await #expect(throws: APIError.self) {
            try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: linked, api: hub.client)
        }
        #expect(store.spacesSupported)
        #expect(store.error != nil)
        #expect(store.error == error.shown)
        #expect(store.todos == original)
    }

    @Test func createdTodoRemainsWhenTheSpacesRouteNeedsAnUpgrade() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"Created"}}"#)
        let error = hubResponseError(#"{"error":"Not Found","code":"not_found","retryable":false}"#)
        hub.on("PUT", "/api/v1/todos/new/spaces/m/p") { _ in throw error }
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        #expect(store.spacesSupported)
        let message = try await store.createAndLink(title: "Created", target: .space(.init(machineID: "m", space: "p")), api: hub.client)
        #expect(message == "待办已创建，但挂到待办失败：这台 hub 还不支持整组挂，升级 hub 后再试")
        #expect(store.todo(id: "new")?.title == "Created")
        #expect(!store.spacesSupported)
    }

    @Test func businessSpaceFailureRefreshesTheAuthoritativeList() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Stale","spaces":[{"machine_id":"m","space":"p"}]}]}"#)
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let failure = hubResponseError(#"{"error":"todo not found","code":"not_found","retryable":false}"#)
        hub.on("DELETE", "/api/v1/todos/t/spaces/m/p") { _ in
            hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"fresh","title":"Fresh","spaces":[]}]}"#)
            throw failure
        }
        await #expect(throws: APIError.self) {
            try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: false, api: hub.client)
        }
        #expect(store.todos.map(\.id) == ["fresh"])
        #expect(store.spacesSupported)
        #expect(store.error == failure.shown)
        #expect(hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test func missingSpaceRouteDoesNotRefreshAndReenableControls() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let failure = hubResponseError(#"{"error":"Not Found","code":"not_found","retryable":false}"#)
        hub.on("PUT", "/api/v1/todos/t/spaces/m/p") { _ in throw failure }
        await #expect(throws: APIError.self) {
            try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: hub.client)
        }
        #expect(hub.sent("GET", "/api/v1/todos").count == 1)
        #expect(!store.spacesSupported)
    }

    @Test func createdTodoAndItsFailureMessageSurviveALinkTimeout() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        hub.on("POST", "/api/v1/todos") { _ in
            hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"Created","spaces":[]}]}"#)
            return Data(#"{"todo":{"id":"new","title":"Created","spaces":[]}}"#.utf8)
        }
        hub.on("PUT", "/api/v1/todos/new/spaces/m/p") { _ in throw URLError(.timedOut) }
        let store = TodoStore.shared
        await store.refresh(api: hub.client)
        let message = try await store.createAndLink(title: "Created", target: .space(.init(machineID: "m", space: "p")), api: hub.client)
        #expect(message == "待办已创建，但挂到待办失败：\(URLError(.timedOut).localizedDescription)")
        #expect(store.todo(id: "new")?.title == "Created")
    }

    @Test func projectCreationChecksAnInFlightMembershipWriteBeforePosting() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        let store = TodoStore.shared
        var client = hub.client
        client.transport = { request in
            if request.method == "PUT" {
                do {
                    _ = try await store.createAndLink(title: "Blocked", target: .space(.init(machineID: "m", space: "other")), api: hub.client)
                    Issue.record("A second project creation must be rejected before POST")
                } catch let error as APIError {
                    #expect(error.status == 409)
                    #expect(error.message == "正在更新项目，请稍后再试")
                }
                #expect(hub.sent("POST", "/api/v1/todos").isEmpty)
                return Data("{}".utf8)
            }
            return try await hub.client.transport!(request)
        }
        try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: client)
        #expect(hub.sent("POST", "/api/v1/todos").isEmpty)
    }

    @Test(arguments: [true, false]) func failedAgentMembershipWritesRefreshBeforeThrowing(_ linked: Bool) async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"fresh","title":"Fresh"}]}"#)
        let failure = APIError(status: 500, message: "write failed")
        hub.on(linked ? "PUT" : "DELETE", "/api/v1/todos/t/agents/m/s") { _ in throw failure }
        let store = TodoStore.shared
        await #expect(throws: APIError.self) {
            if linked { try await store.linkAgent(todoID: "t", machineID: "m", sessionID: "s", api: hub.client) }
            else { try await store.unlinkAgent(todoID: "t", machineID: "m", sessionID: "s", api: hub.client) }
        }
        #expect(store.todos.map(\.id) == ["fresh"])
        #expect(hub.sent("GET", "/api/v1/todos").count == 1)
    }

    /// A task the hub closed (docs/assistant/todos.md「自动完成」) reads 「自动完成」 with its reason; reopened or closed by the user, it does not.
    @Test func autoDoneTodo() throws {
        let json = """
        {"id":"t","title":"统一连接器 logo 尺寸","done_at":"2026-11-04T08:00:00Z","origin":"conv","done_by":"auto",
         "done_reason":" 已合并 PR 并发了 TestFlight ","agents":[]}
        """
        let auto = try JSON.decoder.decode(TodoItem.self, from: Data(json.utf8))
        #expect(auto.autoDone && auto.isDone)
        #expect(TodosLogic.todoStatus(auto) == .done)
        #expect(auto.statusLabel(.done) == "自动完成")
        #expect(auto.autoDoneReason == "已合并 PR 并发了 TestFlight")

        var reopened = auto
        reopened.doneAt = nil
        #expect(!reopened.autoDone && reopened.autoDoneReason == nil)
        #expect(reopened.statusLabel(.followup) == "待跟进")

        let byUser = TodoItem(id: "u", title: "自己点的", doneAt: Date())
        #expect(!byUser.autoDone && byUser.statusLabel(.done) == "已完成" && byUser.autoDoneReason == nil)
    }

    /// #1683: what needs the user stays on top; the rest go by due date (a due day counts to its end), those without
    /// one after in the old order; done ones last.
    @Test func sortTodosByDueDate() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "Asia/Shanghai")!
        let now = Date()
        let needs = TodoItem(id: "needs", title: "Needs", createdAt: now, agents: [TodoItem.Agent(machineID: "m", sessionID: "s_needs")])
        let dayLater = TodoItem(id: "day", title: "Day", createdAt: now, dueDate: "2026-11-12")
        let morning = TodoItem(id: "morning", title: "Morning", createdAt: now,
                               dueAt: cal.date(from: DateComponents(year: 2026, month: 11, day: 12, hour: 9)))
        let overdue = TodoItem(id: "overdue", title: "Overdue", createdAt: now, dueDate: "2020-01-01")
        let none = TodoItem(id: "none", title: "None", createdAt: now.addingTimeInterval(-10))
        let noneNew = TodoItem(id: "none_new", title: "NoneNew", createdAt: now)
        let done = TodoItem(id: "done", title: "Done", doneAt: now, createdAt: now, dueDate: "2020-01-01")
        let lookup = TodosLogic.makeSessionLookup([session("s_needs", state: .needsYou)])
        let sorted = TodosLogic.sortTodos([done, none, dayLater, noneNew, morning, overdue, needs], lookup: lookup, calendar: cal)
        #expect(sorted.map(\.id) == ["needs", "overdue", "morning", "day", "none_new", "none", "done"])
        #expect(TodosLogic.isOverdue(overdue, calendar: cal) && !TodosLogic.isOverdue(done, calendar: cal))
        #expect(TodosLogic.dueBy(dayLater, calendar: cal) == cal.date(from: DateComponents(year: 2026, month: 11, day: 12, hour: 23, minute: 59, second: 59)))
    }

    /// #1683: the plan decodes from the hub (absent from an older one), and a change is sent as the hub reads it: a
    /// cleared field as null, an untouched one left out.
    @Test func todoPlanDecodesAndEncodes() throws {
        let json = """
        {"id":"t","title":"Renew","due_date":"2026-12-01","remind_at":"2026-11-30T01:00:00Z","reminded_at":"2026-11-30T01:00:05Z","priority":"high"}
        """
        let t = try JSON.decoder.decode(TodoItem.self, from: Data(json.utf8))
        #expect(t.dueDate == "2026-12-01" && t.dueAt == nil && t.remindAt != nil && t.remindedAt != nil && t.priority == "high")
        let old = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"o","title":"Old"}"#.utf8))
        #expect(old.dueAt == nil && old.dueDate == nil && old.remindAt == nil && old.priority == nil)

        func body(_ c: APIClient.TodoPlanChange) throws -> [String: Any] {
            try JSONSerialization.jsonObject(with: JSON.encoder.encode(c)) as? [String: Any] ?? [:]
        }
        let cleared = try body(APIClient.TodoPlanChange(due: .some(.none), remindAt: .some(nil)))
        #expect(cleared.keys.sorted() == ["due_at", "remind_at"] && cleared["due_at"] is NSNull && cleared["remind_at"] is NSNull)
        let day = try body(APIClient.TodoPlanChange(due: .day("2026-12-01"), priority: "low"))
        #expect(day["due_date"] as? String == "2026-12-01" && day["priority"] as? String == "low" && day["due_at"] == nil && day["remind_at"] == nil)
        #expect(try body(APIClient.TodoPlanChange()).isEmpty)
    }
}
