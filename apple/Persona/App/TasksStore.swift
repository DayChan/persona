import Foundation
import Observation

/// Task presentation for the hub-level group's assistant conversations (docs/assistant/unified-entry.md): todos come from
/// TodoStore's shared index; plans and each conversation's feed stay here. Feeds refresh when shown and on
/// reconnect, with assistant_feed and task_plan events keeping them current. A task's agents come from AppModel's
/// sessions and interactions, so a card follows them like the Agents page does.
@MainActor @Observable
final class TasksStore {
    static let shared = TasksStore(viewStore: .shared)

    var todos: [String: TodoItem] { todoStore.byID }
    private(set) var plans: [String: TaskPlan] = [:]
    /// Conversation id → its feed, oldest first.
    private(set) var feeds: [String: [FeedItem]] = [:]
    /// Conversation id → the task the next message goes straight to (Message “xx”), set by @ or Message.
    var routes: [String: String] = [:]
    /// The task whose detail sheet is open, and whether the board is.
    var detailTask: String?
    /// The agent (not a task's, #1113) whose page the detail sheet shows instead.
    var detailAgent: SessionKey?
    /// The agent whose page the assistant page pushes full screen (the sheet's Full Screen): the page takes it and clears it.
    var fullAgent: SessionKey?
    var showBoard = false
    /// A task or an agent picked on the board, opened when the board has gone down.
    var openAfterBoard: String?
    var openAgentAfterBoard: SessionKey?
    /// Message cards (inbox item ids) put off with Later, the ones sent from their card, and the ones whose Send the hub
    /// refused as handled elsewhere (409 inbox_item_done).
    private(set) var laterMessages: Set<String> = []
    private(set) var sentMessages: Set<String> = []
    private(set) var filedMessages: Set<String> = []
    /// Quick results (QuickResult.callID) the user took back with Undo.
    private(set) var undone: Set<String> = []
    /// Plans being started or dismissed (their card shows it and takes no second tap).
    private(set) var busyPlans: Set<String> = []
    var error: String?

    @ObservationIgnored private let todoStore: TodoStore
    @ObservationIgnored private let viewStore: AssistantViewsStore
    @ObservationIgnored private var generation: UInt = 0
    @ObservationIgnored private var loading: [String: Task<Void, Never>] = [:]
    /// Conversation id → the day its Today card was asked for (once a day).
    @ObservationIgnored private var todayAsked: [String: String] = [:]

    init(todoStore: TodoStore = .shared, viewStore: AssistantViewsStore = AssistantViewsStore()) {
        self.todoStore = todoStore
        self.viewStore = viewStore
    }

    /// Entering or leaving Demo Mode, or another hub: nothing of the other side may show.
    func forget() {
        generation &+= 1
        for task in loading.values { task.cancel() }
        loading = [:]
        viewStore.forget()
        plans = [:]
        feeds = [:]
        routes = [:]
        detailTask = nil
        detailAgent = nil
        fullAgent = nil
        showBoard = false
        busyPlans = []
    }

    /// Full Screen in the detail sheet: the sheet goes down and the agent's page is pushed onto the assistant page, the
    /// page an agent opened from the Agents tab is. Its back comes to the conversation, not to the sheet.
    func expand(_ key: SessionKey) {
        detailTask = nil
        detailAgent = nil
        fullAgent = key
    }

    // MARK: Loading

    func reloadTodos(api: APIClient? = nil) {
        todoStore.reload(api: api)
    }

    /// Pulls a conversation's feed (what came since the newest item it has, page after page: FeedPaging) and the todos.
    @discardableResult
    func load(_ conversation: String, refreshTodos: Bool = true, api injected: APIClient? = nil) -> Task<Void, Never>? {
        if let task = loading[conversation] { return task }
        guard let api = injected ?? AppModel.shared.api else { return nil }
        if refreshTodos { reloadTodos(api: injected) }
        let first = feeds[conversation]?.last?.at
        let ticket = generation
        let task = Task {
            defer { if generation == ticket { loading[conversation] = nil } }
            var since = first
            for _ in 0..<FeedPaging.maxPages {
                guard let res = try? await api.feed(conversation: conversation, since: since),
                      !Task.isCancelled, generation == ticket else { return }
                // The cards' snapshot comes whole with every page: what an event missed comes back here.
                viewStore.receive(res.views ?? [:], conversation: conversation, group: api.group)
                viewStore.receiveActions(res.viewActions ?? [:], conversation: conversation, group: api.group)
                viewStore.receiveBlocks(res.viewBlocks ?? [], conversation: conversation, group: api.group)
                let items = res.items.filter { $0.conversation == conversation }
                for item in items { viewStore.receive(item, group: api.group) }
                for (id, plan) in res.plans ?? [:] where plan.conversation == conversation && plans[id] != plan { plans[id] = plan }
                merge(items, into: conversation)
                guard let next = FeedPaging.next(after: res.items, since: since) else { return }
                since = next
            }
        }
        loading[conversation] = task
        return task
    }

    /// The (re)connect: every conversation shown so far, again.
    @discardableResult
    func reconnected(api: APIClient? = nil) -> [Task<Void, Never>] {
        feeds.keys.compactMap { load($0, refreshTodos: false, api: api) }
    }

    func received(_ item: FeedItem, group: String = "") {
        viewStore.receive(item, group: group)
        merge([item], into: item.conversation)
        if item.kind == .task || item.kind == .receipt || item.kind == .review || item.kind == .report { reloadTodos() }
    }

    func received(_ plan: TaskPlan) {
        if plans[plan.id] != plan { plans[plan.id] = plan }
    }

    private func merge(_ items: [FeedItem], into conversation: String) {
        guard !items.isEmpty else {
            if feeds[conversation] == nil { feeds[conversation] = [] }
            return
        }
        var list = feeds[conversation] ?? []
        let have = Set(list.map(\.id))
        let new = items.filter { !have.contains($0.id) }
        guard !new.isEmpty else { return }
        list += new
        list.sort { ($0.at, $0.id) < ($1.at, $1.id) }
        feeds[conversation] = list
    }

    // MARK: What a conversation shows

    func feed(_ conversation: String) -> [FeedItem] { feeds[conversation] ?? [] }

    /// The tasks the conversation opened, newest first.
    func tasks(of conversation: String) -> [TodoItem] {
        todos.values.filter { $0.origin == conversation }
            .sorted { ($0.createdAt ?? .distantPast) > ($1.createdAt ?? .distantPast) }
    }

    /// The agents going on that the conversation's dock and board show after its tasks (#1113): in the main
    /// conversation the ones no task stands for (opened by hand on the Agents page); in the others none.
    func agents(of conversation: String) -> [DockAgent] {
        let model = AppModel.shared
        guard let main = model.assistantMain?.sessionID, conversation == main else { return [] }
        let group = model.hub?.group ?? ""
        let tasks = todos.values.filter { !($0.origin ?? "").isEmpty && $0.doneAt == nil }
        let keys = Set(tasks.flatMap { t in
            t.activeAgents.map { SessionKey(group: group, machineID: $0.machineID, sessionID: $0.sessionID) }
        })
        let manual = todos.values.filter { ($0.origin ?? "").isEmpty }.map(\.id)
        return DockAgent.shown(model.goingAgents, conversation: conversation, main: main, group: group,
                               taskAgents: keys, manualTodos: Set(manual))
    }

    /// The task (a todo from an assistant conversation, not done) an agent works for.
    func task(of key: SessionKey) -> TodoItem? {
        guard key.group == (AppModel.shared.hub?.group ?? "") else { return nil }
        return todos.values.first { t in
            t.origin != nil && t.doneAt == nil && t.activeAgents.contains { $0.machineID == key.machineID && $0.sessionID == key.sessionID }
        }
    }

    /// The task's agents as the app knows them now (the Agents page's sessions), with their last line and step: a card
    /// follows each new step of an agent that only runs tools (`AppModel.sessionWithLine(_:)`).
    func sessions(_ todo: TodoItem) -> [Session] {
        let model = AppModel.shared
        let group = model.hub?.group ?? ""
        return todo.activeAgents.compactMap { model.sessionWithLine(SessionKey(group: group, machineID: $0.machineID, sessionID: $0.sessionID)) }
    }

    func state(_ todo: TodoItem) -> TaskState {
        let state = TaskState(todo: todo, agents: sessions(todo).map(\.state))
        // A hub from before #1077 leaves the todo open when the agent did not start; its plan still says why.
        if state == .notStarted, todo.agents.isEmpty, planError(todo) != nil { return .failed }
        return state
    }

    /// Why the task's agent did not start: the todo's own word, else its plan's (nil while it may still start).
    func startFailure(_ todo: TodoItem) -> String? {
        if let e = todo.startError, !e.isEmpty { return e }
        return todo.agents.isEmpty ? planError(todo) : nil
    }

    private func planError(_ todo: TodoItem) -> String? {
        guard let plan = todo.planID.flatMap({ plans[$0] }),
              let error = plan.tasks.first(where: { $0.todoID == todo.id })?.error, !error.isEmpty else { return nil }
        return error
    }

    /// The task's agent to open first: the one that waits for the user, then the one working, then the latest.
    func leadSession(_ todo: TodoItem) -> Session? {
        sessions(todo).min { a, b in
            a.state.priority != b.state.priority ? a.state.priority < b.state.priority : (a.updatedAt ?? .distantPast) > (b.updatedAt ?? .distantPast)
        }
    }

    /// What the task's agents are asking the user, oldest first.
    func interactions(_ todo: TodoItem) -> [Interaction] {
        let keys = Set(sessions(todo).map(\.key))
        return AppModel.shared.pendingInteractions.filter { keys.contains($0.sessionKey) }
    }

    /// The dock: the conversation's tasks still going on, or finished and not looked at; what waits first.
    func dock(_ conversation: String) -> [TodoItem] {
        let rows: [(todo: TodoItem, state: TaskState)] = tasks(of: conversation).map { ($0, state($0)) }
        let going = rows.filter { $0.state != .done && $0.state != .followup && $0.state != .failed }
        let sorted = going.sorted { a, b in
            if a.state.priority != b.state.priority { return a.state.priority < b.state.priority }
            return (a.todo.createdAt ?? .distantPast) > (b.todo.createdAt ?? .distantPast)
        }
        return sorted.map(\.todo)
    }

    /// The main conversation's Today card, once a day: the hub makes it on the first ask and answers it after that.
    func loadToday(_ conversation: String) {
        let day = Date.now.formatted(.iso8601.year().month().day())
        guard todayAsked[conversation] != day, let api = AppModel.shared.api else { return }
        todayAsked[conversation] = day
        Task {
            if let item = try? await api.today(conversation: conversation) { received(item) } else { todayAsked[conversation] = nil }
        }
    }

    // MARK: Actions

    /// Send on a message card: the draft (as edited) goes out through the inbox, which files the item as done. The
    /// card's id goes along, so the hub sends once per card and nothing for an item handled since (#4797).
    func sendMessage(_ card: FeedItem, itemID: String, text: String) async {
        guard messageState(card, itemID: itemID) == .open else { return }
        let inbox = InboxStore.shared
        if let item = Self.cardItem(itemID) {
            // A refusal comes back as the hub's words; the reload brings the item as the hub has it (handled elsewhere).
            if await inbox.send(item, text: text, card: card.id) { sentMessages.insert(itemID) } else { inbox.reload() }
            return
        }
        guard let api = AppModel.shared.api else { return }
        do {
            try await api.inboxReply(itemID, text: text, card: card.id)
            sentMessages.insert(itemID)
            Haptics.success()
            inbox.reload()
        } catch let e as APIError where e.code == "inbox_item_done" {
            filedMessages.insert(itemID) // handled elsewhere before this card heard of it: nothing went out
            inbox.reload()
        } catch {
            AppModel.shared.toast = Toast(String(localized: "Reply not sent: \(error.localizedDescription)"), style: .error)
        }
    }

    func later(_ itemID: String) { laterMessages.insert(itemID) }

    enum MessageState { case open, sent, filed }

    /// A message card's inbox item: the default manager's, as the feed and the reply without the item go to it (each
    /// manager numbers its own items).
    private static func cardItem(_ itemID: String) -> InboxItem? {
        InboxStore.shared.items.first { $0.id == itemID && AppModel.shared.pushedBy($0.manager) == nil }
    }

    func messageState(_ card: FeedItem, itemID: String) -> MessageState {
        Self.messageState(card, inbox: Self.cardItem(itemID),
                          sentHere: sentMessages.contains(itemID), refused: filedMessages.contains(itemID))
    }

    /// Where a message card's reply stands: sent (from the card here, the hub's `sent_at`, or replied in the inbox), filed
    /// (the hub's `filed_at`, a refused Send, or the inbox has it done some other way: Done, Ignore, read elsewhere), or
    /// still open. The inbox list only keeps items done in the last 3 days, so the card's own fields cover older ones;
    /// an item the inbox lists as open was moved back since, and sends again (#4797).
    nonisolated static func messageState(_ card: FeedItem, inbox item: InboxItem?, sentHere: Bool, refused: Bool) -> MessageState {
        if sentHere || card.sentAt != nil { return .sent }
        if let item { return item.isDone ? (item.doneBy == "replied" ? .sent : .filed) : .open }
        return card.filedAt != nil || refused ? .filed : .open
    }

    /// Undo on a quick result: the reminder or event comes off the phone.
    func undo(_ result: QuickResult) async {
        do {
            switch result.kind {
            case .reminder: _ = try RemindersTool.remove(id: result.itemID)
            case .event: _ = try CalendarTool.remove(id: result.itemID)
            }
            undone.insert(result.callID)
            Haptics.success()
        } catch {
            AppModel.shared.toast = Toast(String(localized: "Couldn't undo: \(error.localizedDescription)"), style: .error)
        }
    }

    func start(_ plan: TaskPlan, edits: [PlanTask]? = nil, remember: Bool = false) async {
        guard let api = AppModel.shared.api, !busyPlans.contains(plan.id) else { return }
        busyPlans.insert(plan.id)
        defer { busyPlans.remove(plan.id) }
        do {
            received(try await api.startPlan(plan.id, edits: edits, remember: remember))
            reloadTodos()
            Haptics.success()
        } catch {
            self.error = error.localizedDescription
            AppModel.shared.toast = Toast(String(localized: "Didn't start: \(error.localizedDescription) [Tasks Store 38bbb7]"), style: .error)
        }
    }

    /// Remove on a task that does not run: the todo goes (its agents and their chats stay on the Agents page), and with
    /// it the task's card, pill and line.
    func remove(_ todo: TodoItem) async {
        guard let api = AppModel.shared.api else { return }
        do {
            try await todoStore.delete(id: todo.id, api: api)
            for (conversation, id) in routes where id == todo.id { routes[conversation] = nil }
            if detailTask == todo.id { detailTask = nil }
            Haptics.success()
        } catch {
            AppModel.shared.toast = Toast(String(localized: "Couldn't remove: \(error.localizedDescription) [Tasks Store ab04c7]"), style: .error)
        }
    }

    func dismiss(_ plan: TaskPlan) async {
        guard let api = AppModel.shared.api, !busyPlans.contains(plan.id) else { return }
        busyPlans.insert(plan.id)
        defer { busyPlans.remove(plan.id) }
        do {
            received(try await api.dismissPlan(plan.id))
        } catch {
            AppModel.shared.toast = Toast(error.localizedDescription, style: .error)
        }
    }

    /// Sends text straight to the task the conversation is routed to; false when there was none (send it as usual).
    func sendRouted(_ conversation: String, text: String) -> Bool {
        guard let todoID = routes[conversation], let api = AppModel.shared.api else { return false }
        routes[conversation] = nil
        Task {
            do {
                received(try await api.route(conversation: conversation, todoID: todoID, text: text))
            } catch {
                AppModel.shared.toast = Toast(String(localized: "Couldn't deliver: \(error.localizedDescription)"), style: .error)
            }
        }
        return true
    }
}

/// How a conversation's feed is read (#4642): the hub answers at most `pageSize` items after `since`, oldest first
/// (tasks.go `feedPage`), so a feed longer than that comes in pages, each after the newest of the one before, as the
/// web's `loadFeed` (#2171 / #2272) and the watch's cards read it. Without this a cold start showed only the oldest
/// 500, and the first push after it moved `since` past the rest for good.
nonisolated enum FeedPaging {
    static let pageSize = 500
    /// A bound on one load: a hub that kept answering full pages does not keep it going forever.
    static let maxPages = 40

    /// Where the next page starts after `page`, the answer to a request after `since`; nil when this was the last:
    /// a page that is not full, or one whose newest item is no later than `since` (it would come back the same).
    static func next(after page: [FeedItem], since: Date?) -> Date? {
        guard page.count >= pageSize, let newest = page.map(\.at).max() else { return nil }
        if let since, newest <= since { return nil }
        return newest
    }
}
