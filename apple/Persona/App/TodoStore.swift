import Foundation
import Observation

/// Hub-wide todo list management (docs/assistant/todos.md, issue #401):
/// The hub's todos, cached locally in StateCache, refreshed on {"type":"todos"} WS event and (re)connect.
/// Supports 404 gracefully (older hubs without todos support: hides entrance, no error).
@MainActor @Observable
final class TodoStore {
    static let shared = TodoStore()

    private(set) var todos: [TodoItem] = [] {
        didSet {
            byID = Self.index(todos)
            let q = QuietTasks(todos)
            if q != quiet { quiet = q } // read by the badges and the Mac's menu bar: told only when it changes
        }
    }
    private(set) var byID: [String: TodoItem] = [:]
    /// The quiet tasks and their agents (#1731), which stay off the island, notifications and badges.
    private(set) var quiet = QuietTasks()
    private(set) var loading = false
    /// false when GET /api/v1/todos returned 404 (old hub without todos)
    private(set) var isSupported = true
    private(set) var spacesSupported = false
    private(set) var changingSpaces = false
    var error: String?

    @ObservationIgnored private let reads = CoalescedRefresh()
    @ObservationIgnored private var mutationRevision = 0
    @ObservationIgnored private var activeReadRevision: Int?
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var lastPushed: [String: TodoItem]
    @ObservationIgnored private let cacheWriter: (@MainActor ([TodoItem]) -> Void)?
    @ObservationIgnored private let hubListChanged: @MainActor () -> Void
    @ObservationIgnored private let saver = Debouncer(.seconds(1))
    private static let stateName = "todo-items"

    init(cachedTodos: [TodoItem]? = nil,
         cacheWriter: (@MainActor ([TodoItem]) -> Void)? = { StateCache.save(TodoStore.stateName, $0) },
         hubListChanged: @escaping @MainActor () -> Void = {
        WatchSync.shared.push(summary: AppModel.shared.watchSummary()) // its questions name their tasks
    }) {
        let initial = cachedTodos ?? StateCache.load(Self.stateName, as: [TodoItem].self) ?? []
        let initialIndex = Self.index(initial)
        todos = initial
        byID = initialIndex
        quiet = QuietTasks(initial)
        lastPushed = initialIndex
        self.cacheWriter = cacheWriter
        self.hubListChanged = hubListChanged
    }

    private static func index(_ list: [TodoItem]) -> [String: TodoItem] {
        var result: [String: TodoItem] = [:]
        for todo in list { result[todo.id] = todo }
        return result
    }

    var openTodos: [TodoItem] { todos.filter { !$0.isDone } }
    /// The To-dos badge (the drawer, the Mac's sidebar): open todos, quiet tasks left out (#1731, the main conversation
    /// tells about them). The list itself still has them.
    var badgeCount: Int { todos.count(where: { !$0.isDone && !$0.quiet }) }

    func forget() {
        generation += 1
        reads.reset()
        activeReadRevision = nil
        loading = false
        changingSpaces = false
        todos = []
        error = nil
        isSupported = true
        spacesSupported = false
        persist([])
    }

    /// Automatic invalidations share in-flight reads, without adding delay to unclassified todo/feed hints.
    /// The returned task covers this invalidation and at most one dirty follow-up, not an endless event pump.
    @discardableResult
    func reload(api: APIClient? = nil) -> Task<Void, Never> {
        reads.invalidate { [self] in await fetch(api: api) }
    }

    /// Manual refresh shares an eligible read, without starting a parallel GET or adding delay.
    func refresh(api: APIClient? = nil) async {
        let staleRead = activeReadRevision.map { $0 != mutationRevision } ?? false
        await reads.refresh(afterCurrent: staleRead) { [self] in await fetch(api: api) }
    }

    private func confirmWrite(api: APIClient) async {
        mutationRevision += 1
        await reads.refresh(afterCurrent: true) { [self] in await fetch(api: api) }
    }

    private func fetch(api injected: APIClient? = nil) async {
        guard let api = injected ?? AppModel.shared.api else { return }
        let requestedGeneration = generation
        let requestedRevision = mutationRevision
        activeReadRevision = requestedRevision
        loading = true
        defer {
            if generation == requestedGeneration {
                loading = false
                activeReadRevision = nil
            }
        }
        do {
            let list = try await api.todos()
            guard generation == requestedGeneration, mutationRevision == requestedRevision, !Task.isCancelled else { return }
            isSupported = true
            spacesSupported = TodosLogic.supportsSpaces(list)
            apply(list)
            error = nil
            if byID != lastPushed {
                lastPushed = byID
                hubListChanged()
            }
        } catch let apiErr where TodosLogic.isOldHub(apiErr) {
            guard generation == requestedGeneration, mutationRevision == requestedRevision, !Task.isCancelled else { return }
            // Older hub without todos: hide entrance without error
            isSupported = false
            spacesSupported = false
            error = nil
        } catch {
            guard generation == requestedGeneration, mutationRevision == requestedRevision, !Task.isCancelled else { return }
            if let shown = error.shown { self.error = shown }
        }
    }

    private func apply(_ list: [TodoItem]) {
        guard list != todos else { return }
        todos = list
        persist(list)
    }

    private func persist(_ list: [TodoItem]) {
        guard let cacheWriter else { return }
        saver.run { cacheWriter(list) }
    }

    func todo(id: String) -> TodoItem? {
        byID[id]
    }

    func create(title: String, notes: String? = nil, api injected: APIClient? = nil) async throws -> TodoItem {
        guard let api = injected ?? AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        let created = try await api.createTodo(title: title, notes: notes)
        guard generation == targetGeneration else { return created }
        mutationRevision += 1
        var updated = todos
        updated.insert(created, at: 0)
        apply(updated)
        return created
    }

    func update(id: String, title: String? = nil, notes: String? = nil, done: Bool? = nil, api injected: APIClient? = nil) async throws -> TodoItem {
        guard let api = injected ?? AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        let res = try await api.updateTodo(id: id, title: title, notes: notes, done: done)
        guard generation == targetGeneration else { return res }
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == id }) {
            updated[idx] = res
        } else {
            updated.insert(res, at: 0)
        }
        apply(updated)
        return res
    }

    /// Changes a todo's due date, reminder or priority (#1683).
    @discardableResult
    func updatePlan(id: String, _ change: APIClient.TodoPlanChange, api injected: APIClient? = nil) async throws -> TodoItem {
        guard let api = injected ?? AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        let res = try await api.updateTodoPlan(id: id, change)
        guard generation == targetGeneration else { return res }
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == id }) {
            updated[idx] = res
        } else {
            updated.insert(res, at: 0)
        }
        apply(updated)
        return res
    }

    /// `api` is the client to write through (tests pass a fake hub's); the app uses the current hub connection.
    func toggleDone(_ todo: TodoItem, api: APIClient? = nil) {
        let targetGeneration = generation
        let newDone = !todo.isDone
        let original = todos
        // Optimistic update; a read already in flight cannot replace it.
        mutationRevision += 1
        var updated = todos
        if let idx = updated.firstIndex(where: { $0.id == todo.id }) {
            updated[idx].doneAt = newDone ? Date() : nil
            apply(updated)
        }
        Task {
            do {
                _ = try await update(id: todo.id, done: newDone, api: api)
            } catch {
                // Revert on failure: `todos` already holds the optimistic list, so put back the one saved before it.
                guard generation == targetGeneration else { return }
                mutationRevision += 1
                apply(original)
                AppModel.shared.show(error)
            }
        }
    }

    func delete(id: String, api injected: APIClient? = nil) async throws {
        guard let api = injected ?? AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        try await api.deleteTodo(id: id)
        guard generation == targetGeneration else { return }
        mutationRevision += 1
        var updated = todos
        updated.removeAll { $0.id == id }
        apply(updated)
    }

    func linkAgent(todoID: String, machineID: String, sessionID: String, api injected: APIClient? = nil) async throws {
        guard let api = injected ?? AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        do {
            try await api.linkTodoAgent(todoID: todoID, machineID: machineID, sessionID: sessionID)
        } catch {
            if generation == targetGeneration {
                await confirmWrite(api: api)
            }
            throw error
        }
        if generation == targetGeneration {
            await confirmWrite(api: api)
        }
    }

    func unlinkAgent(todoID: String, machineID: String, sessionID: String, api injected: APIClient? = nil) async throws {
        guard let api = injected ?? AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        do {
            try await api.unlinkTodoAgent(todoID: todoID, machineID: machineID, sessionID: sessionID)
        } catch {
            if generation == targetGeneration {
                await confirmWrite(api: api)
            }
            throw error
        }
        if generation == targetGeneration {
            await confirmWrite(api: api)
        }
    }

    /// Membership changes immediately; the hub then supplies the authoritative Agent attachments.
    @discardableResult
    func setSpace(todoID: String, space: TodoItem.Space, linked: Bool, api injected: APIClient? = nil) async throws -> Bool {
        guard !changingSpaces else { throw APIError(status: 409, message: String(localized: "Updating projects. Try again in a moment.")) }
        guard let api = injected ?? AppModel.shared.api else { throw APIError(status: 0, message: String(localized: "Not connected to a hub [Todo Store 04f822]")) }
        let targetGeneration = generation
        changingSpaces = true
        defer { if generation == targetGeneration { changingSpaces = false } }
        mutationRevision += 1
        let original = todos
        let via = "\(space.machineID)/\(space.space)"
        var optimistic = todos
        for index in optimistic.indices {
            guard linked || optimistic[index].id == todoID else { continue }
            optimistic[index].spaces?.removeAll { $0.machineID == space.machineID && $0.space == space.space }
            if !linked || optimistic[index].id != todoID {
                optimistic[index].agents.removeAll { $0.source == "space" && $0.via == via }
            }
            if linked && optimistic[index].id == todoID {
                optimistic[index].spaces = (optimistic[index].spaces ?? []) + [space]
            }
        }
        apply(optimistic)
        do {
            if linked {
                try await api.linkTodoSpace(todoID: todoID, machineID: space.machineID, space: space.space)
            } else {
                try await api.unlinkTodoSpace(todoID: todoID, machineID: space.machineID, space: space.space)
            }
            guard generation == targetGeneration else { return true }
            await confirmWrite(api: api)
            return true
        } catch {
            guard generation == targetGeneration else { throw error }
            mutationRevision += 1
            apply(original)
            if TodosLogic.isLegacySpacesRoute(error) {
                spacesSupported = false
                let upgrade = APIError(status: 404, message: String(localized: "This hub can't attach a whole group yet. Update the hub and try again."))
                self.error = upgrade.message
                throw upgrade
            }
            let message = error.shown
            self.error = message
            await confirmWrite(api: api)
            guard generation == targetGeneration else { throw error }
            self.error = message
            throw error
        }
    }

    func link(_ target: TodoLinkTarget, to todoID: String, api: APIClient? = nil) async throws {
        switch target {
        case .space(let project):
            try await setSpace(todoID: todoID, space: project, linked: true, api: api)
        case .agent(let machine, let session, _):
            try await linkAgent(todoID: todoID, machineID: machine, sessionID: session, api: api)
        }
    }

    func unlink(_ target: TodoLinkTarget, from todoID: String, api: APIClient? = nil) async throws {
        switch target {
        case .space(let project):
            try await setSpace(todoID: todoID, space: project, linked: false, api: api)
        case .agent(let machine, let session, _):
            try await unlinkAgent(todoID: todoID, machineID: machine, sessionID: session, api: api)
        }
    }

    /// Creation failure throws; a later link failure returns a message that preserves the created todo.
    func createAndLink(title: String, target: TodoLinkTarget, api: APIClient? = nil) async throws -> String? {
        if target.isSpace && changingSpaces {
            throw APIError(status: 409, message: String(localized: "Updating projects. Try again in a moment."))
        }
        let created = try await create(title: title, api: api)
        do {
            try await link(target, to: created.id, api: api)
            return nil
        } catch {
            return TodosLogic.createdLinkFailure(error, isSpace: target.isSpace)
        }
    }
}
