import Foundation
import Observation
import Testing
@testable import Persona

@MainActor
private final class TodoObservationFlag { var changed = false }

/// Captures the FakeHub answer at request start, then deliberately ignores cancellation until released.
@MainActor
private final class TodoReadGate {
    let hub = FakeHub()
    private(set) var reads = 0
    private var pending: [Int: (Result<Data, Error>, CheckedContinuation<Data, Error>)] = [:]
    private var closed = false
    var client: APIClient {
        var api = hub.client
        api.transport = { [self] request in try await receive(request) }
        return api
    }
    private func receive(_ request: LocalRequest) async throws -> Data {
        guard !closed else { throw CancellationError() }
        guard request.method == "GET", request.path == "/api/v1/todos" else {
            return try await hub.client.transport!(request)
        }
        let result: Result<Data, Error>
        do { result = .success(try await hub.client.transport!(request)) }
        catch { result = .failure(error) }
        reads += 1
        let index = reads
        return try await withCheckedThrowingContinuation { pending[index] = (result, $0) }
    }
    func answer(_ index: Int) throws {
        let removed = pending.removeValue(forKey: index)
        let item = try #require(removed)
        item.1.resume(with: item.0)
    }
    func close() {
        closed = true
        let remaining = pending.values
        pending = [:]
        for item in remaining { item.1.resume(throwing: CancellationError()) }
    }
}

@MainActor
struct TodoCacheTests {
    @Test func requestsDuringARefreshCoalesceIntoOneFollowup() async {
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget() }
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"first","title":"First"}]}"#)
        var client = hub.client
        client.transport = { request in
            let result = try await hub.client.transport!(request)
            if request.path == "/api/v1/todos", hub.sent("GET", request.path).count == 1 {
                await MainActor.run {
                    store.reload(api: hub.client)
                    store.reload(api: hub.client)
                }
                hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"second","title":"Second"}]}"#)
            }
            return result
        }
        let reload = store.reload(api: client)
        await reload.value
        #expect(hub.sent("GET", "/api/v1/todos").count == 2)
        #expect(store.todos.map(\.id) == ["second"])
        #expect(store.byID["first"] == nil)
        #expect(store.byID["second"]?.title == "Second")
    }

    @Test func taskReadersShareCachedAndMutatedTodos() async throws {
        let cached = TodoItem(id: "cached", title: "Cached")
        let store = TodoStore(cachedTodos: [cached], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        let hub = FakeHub()
        #expect(tasks.todos == ["cached": cached])
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"New"}}"#)
        _ = try await store.create(title: "New", api: hub.client)
        #expect(tasks.todos["new"]?.title == "New")
        #expect(tasks.todos == store.byID)
        hub.on("PATCH", "/api/v1/todos/new", json: #"{"todo":{"id":"new","title":"Edited"}}"#)
        _ = try await store.update(id: "new", title: "Edited", api: hub.client)
        #expect(tasks.todos["new"]?.title == "Edited")
        #expect(tasks.todos == store.byID)
        hub.on("DELETE", "/api/v1/todos/new", json: "{}")
        try await store.delete(id: "new", api: hub.client)
        #expect(tasks.todos["new"] == nil)
        #expect(tasks.todos == store.byID)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"remote","title":"Remote"}]}"#)
        await store.reload(api: hub.client).value
        #expect(tasks.todos.keys.sorted() == ["remote"])
        #expect(tasks.todos == store.byID)
        tasks.forget()
        #expect(tasks.todos["remote"]?.title == "Remote")
        store.forget()
        #expect(tasks.todos.isEmpty && store.byID.isEmpty)
    }

    /// Calls the two stores manually in didConnect order; this does not execute AppModel.didConnect.
    @Test func reconnectionRefreshesTwoShownFeedsButOnlyOneTodoList() async {
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        for conversation in ["one", "two"] {
            hub.on("GET", "/api/v1/assistant/conversations/\(conversation)/feed", json: #"{"items":[]}"#)
            await tasks.load(conversation, refreshTodos: false, api: hub.client)?.value
        }
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        let reload = store.reload(api: hub.client)
        let feeds = tasks.reconnected(api: hub.client)
        await reload.value
        for feed in feeds { await feed.value }
        #expect(hub.sent("GET", "/api/v1/todos").count == 1)
        #expect(hub.sent("GET", "/api/v1/assistant/conversations/one/feed").count == 2)
        #expect(hub.sent("GET", "/api/v1/assistant/conversations/two/feed").count == 2)
    }

    /// Exercises TodoStore.reload alone; AppModel's todos event wiring is outside this test's scope.
    @Test func oneReloadRequestsOneList() async {
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"event","title":"Event"}]}"#)
        await store.reload(api: hub.client).value
        #expect(hub.sent("GET", "/api/v1/todos").count == 1)
        #expect(tasks.todos["event"]?.title == "Event")
    }

    @Test func onlyChangedHubListsPushTheWatchSummary() async throws {
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"t","title":"Local"}}"#)
        _ = try await store.create(title: "Local", api: hub.client)
        #expect(pushes == 0)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Hub"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
        _ = try await store.update(id: "t", title: "Edited", api: hub.client)
        #expect(pushes == 1)
        hub.on("DELETE", "/api/v1/todos/t", json: "{}")
        try await store.delete(id: "t", api: hub.client)
        #expect(pushes == 1)
    }

    @Test func observationFollowsTheSharedIndex() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        let observed = TodoObservationFlag()
        withObservationTracking { _ = tasks.todos["t"] } onChange: {
            MainActor.assumeIsolated { observed.changed = true }
        }
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"t","title":"New"}}"#)
        _ = try await store.create(title: "New", api: hub.client)
        #expect(observed.changed)
        #expect(tasks.todos["t"]?.title == "New")
    }

    @Test func aHubEchoAfterALocalEditPushesTheWatchSummary() async throws {
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Hub"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
        _ = try await store.update(id: "t", title: "Edited", api: hub.client)
        #expect(pushes == 1)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Edited"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 2)
    }

    @Test func cachedTodosAndForgetKeepTheLastPushedComparison() async {
        var pushes = 0
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Cached")], cacheWriter: nil,
                              hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Cached"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 0)
        store.forget()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New"}]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 1)
        store.forget()
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        await store.refresh(api: hub.client)
        #expect(pushes == 2)
    }

    @Test func forgetDiscardsAnOldResponseWithoutPersistingIt() async throws {
        var writes: [[TodoItem]] = []
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: { writes.append($0) }, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        var old = hub.client
        old.transport = { _ in
            await MainActor.run { store.forget() }
            return Data(#"{"todos":[{"id":"old","title":"Old","spaces":[]}]}"#.utf8)
        }
        await store.refresh(api: old)
        #expect(store.todos.isEmpty && store.byID.isEmpty)
        #expect(store.error == nil && store.isSupported && !store.spacesSupported && !store.loading)
        #expect(pushes == 0)
        // Observe the real debounce through an in-memory writer, never the application cache file.
        for _ in 0..<150 where writes.isEmpty { try await Task.sleep(for: .milliseconds(20)) }
        #expect(writes == [[]]) // Only forget's empty snapshot, never the old response.
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New","spaces":[]}]}"#)
        await store.reload(api: hub.client).value
        #expect(store.byID.keys.sorted() == ["new"])
        #expect(store.spacesSupported)
        #expect(pushes == 1)
    }

    @Test(arguments: [404, 500]) func forgetDiscardsOldFetchErrors(_ status: Int) async {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()
        var old = hub.client
        old.transport = { _ in
            await MainActor.run {
                store.forget()
                store.error = "Current hub error"
            }
            throw APIError(status: status, message: "Old hub error")
        }
        await store.refresh(api: old)
        #expect(store.todos.isEmpty && store.byID.isEmpty)
        #expect(store.isSupported && !store.spacesSupported && !store.loading)
        #expect(store.error == "Current hub error")
    }

    @Test(arguments: ["linkAgent", "unlinkAgent", "setSpace"])
    func membershipFollowupDiscardsAnOldResponseAfterForget(_ operation: String) async throws {
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        let hub = FakeHub()
        var client = hub.client
        client.transport = { request in
            if request.method == "GET" {
                await MainActor.run { store.forget() }
                return Data(#"{"todos":[{"id":"old","title":"Old","spaces":[]}]}"#.utf8)
            }
            return Data("{}".utf8)
        }
        switch operation {
        case "linkAgent": try await store.linkAgent(todoID: "t", machineID: "m", sessionID: "s", api: client)
        case "unlinkAgent": try await store.unlinkAgent(todoID: "t", machineID: "m", sessionID: "s", api: client)
        default: try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: client)
        }
        #expect(store.todos.isEmpty && store.byID.isEmpty)
        #expect(store.isSupported && !store.spacesSupported)
        #expect(store.error == nil && pushes == 0)
    }

    @Test func writeOperationsDiscardOldResponsesAndRollbacksAfterForget() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()

        // 1. create in-flight when forget happens
        var createClient = hub.client
        createClient.transport = { _ in
            await MainActor.run { store.forget() }
            return Data(#"{"todo":{"id":"created","title":"Created"}}"#.utf8)
        }
        _ = try await store.create(title: "Created", api: createClient)
        #expect(store.todos.isEmpty)
        #expect(store.byID["created"] == nil)

        // 2. update in-flight when forget happens
        var updateClient = hub.client
        updateClient.transport = { _ in
            await MainActor.run { store.forget() }
            return Data(#"{"todo":{"id":"updated","title":"Updated"}}"#.utf8)
        }
        _ = try await store.update(id: "updated", title: "Updated", api: updateClient)
        #expect(store.todos.isEmpty)
        #expect(store.byID["updated"] == nil)

        // 3. toggleDone in-flight when forget happens and PATCH fails (revert must not restore original)
        let originalItem = TodoItem(id: "orig", title: "Original", doneAt: nil)
        let storeWithItem = TodoStore(cachedTodos: [originalItem], cacheWriter: nil, hubListChanged: {})
        var toggleClient = hub.client
        toggleClient.transport = { _ in
            await MainActor.run { storeWithItem.forget() }
            throw APIError(status: 500, message: "Server error")
        }
        storeWithItem.toggleDone(originalItem, api: toggleClient)
        // Give background Task a chance to hit the catch block
        for _ in 0..<50 where !storeWithItem.todos.isEmpty {
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(storeWithItem.todos.isEmpty)
        #expect(storeWithItem.byID.isEmpty)

        // 4. setSpace in-flight when forget happens and link fails (revert must not restore original)
        let spaceStore = TodoStore(cachedTodos: [originalItem], cacheWriter: nil, hubListChanged: {})
        var spaceClient = hub.client
        spaceClient.transport = { _ in
            await MainActor.run { spaceStore.forget() }
            throw APIError(status: 500, message: "Link failure")
        }
        do {
            _ = try await spaceStore.setSpace(todoID: "orig", space: .init(machineID: "m", space: "sp"), linked: true, api: spaceClient)
        } catch {}
        #expect(spaceStore.todos.isEmpty)
        #expect(spaceStore.byID.isEmpty)
    }

    @Test func reloadDoesNotWaitForSlowPriorGenerationFetch() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()

        let slowFetchStarted = AsyncStream<Void>.makeStream()
        let allowSlowFetchToFinish = AsyncStream<Void>.makeStream()

        var slowClient = hub.client
        slowClient.transport = { request in
            if request.method == "GET" && request.path == "/api/v1/todos" {
                slowFetchStarted.continuation.yield()
                // Wait for explicit signal before finishing old fetch
                for await _ in allowSlowFetchToFinish.stream { break }
                return Data(#"{"todos":[{"id":"old","title":"Old"}]}"#.utf8)
            }
            return Data("{}".utf8)
        }

        // Start old reload
        let oldTask = store.reload(api: slowClient)
        for await _ in slowFetchStarted.stream { break }

        // Switch hub
        store.forget()
        #expect(store.todos.isEmpty)

        // New hub reload starts immediately without waiting for oldTask
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New"}]}"#)
        let newTask = store.reload(api: hub.client)
        await newTask.value

        #expect(store.todos.map(\.id) == ["new"])
        #expect(store.byID["new"]?.title == "New")

        // Now allow old fetch to finish and verify it does not overwrite new hub's todos
        allowSlowFetchToFinish.continuation.yield()
        await oldTask.value

        #expect(store.todos.map(\.id) == ["new"])
        #expect(store.byID["new"]?.title == "New")
        #expect(store.byID["old"] == nil)
    }

    @Test func loadingRemainsTrueWhenOldGenerationFetchCompletesDuringNewFetch() async throws {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let hub = FakeHub()

        let oldFetchStarted = AsyncStream<Void>.makeStream()
        let allowOldFetchToFinish = AsyncStream<Void>.makeStream()

        var oldClient = hub.client
        oldClient.transport = { request in
            if request.method == "GET" && request.path == "/api/v1/todos" {
                oldFetchStarted.continuation.yield()
                for await _ in allowOldFetchToFinish.stream { break }
                return Data(#"{"todos":[{"id":"old","title":"Old"}]}"#.utf8)
            }
            return Data("{}".utf8)
        }

        let oldTask = store.reload(api: oldClient)
        for await _ in oldFetchStarted.stream { break }
        #expect(store.loading == true)

        store.forget()
        #expect(store.loading == false)

        let newFetchStarted = AsyncStream<Void>.makeStream()
        let allowNewFetchToFinish = AsyncStream<Void>.makeStream()

        var newClient = hub.client
        newClient.transport = { request in
            if request.method == "GET" && request.path == "/api/v1/todos" {
                newFetchStarted.continuation.yield()
                for await _ in allowNewFetchToFinish.stream { break }
                return Data(#"{"todos":[{"id":"new","title":"New"}]}"#.utf8)
            }
            return Data("{}".utf8)
        }

        let newTask = store.reload(api: newClient)
        for await _ in newFetchStarted.stream { break }
        #expect(store.loading == true)

        // Old fetch returns while new fetch is still in flight
        allowOldFetchToFinish.continuation.yield()
        await oldTask.value

        // store.loading must remain true because old fetch generation mismatch did not clear loading
        #expect(store.loading == true)

        // New fetch finishes and clears loading
        allowNewFetchToFinish.continuation.yield()
        await newTask.value
        #expect(store.loading == false)
        #expect(store.todos.map(\.id) == ["new"])
    }

    @Test func sustainedTodoEventsShareReadsAndLandTheCompleteFinalSnapshot() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"initial","title":"Initial"}]}"#)
        var firstFinished = false
        let first = store.reload(api: gate.client)
        let observer = Task { await first.value; firstFinished = true }
        defer { observer.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"middle","title":"Middle"}]}"#)
        for _ in 0..<50 { store.reload(api: gate.client) }
        #expect(gate.reads == 1)
        try gate.answer(1)
        try await waitForRefreshTest { gate.reads == 2 }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"final","title":"Final"}]}"#)
        var final = first
        for _ in 0..<50 { final = store.reload(api: gate.client) }
        #expect(gate.reads == 2)
        try gate.answer(2)
        try await waitForRefreshTest { firstFinished && gate.reads == 3 }
        #expect(store.byID.keys.sorted() == ["middle"])
        try gate.answer(3)
        await final.value
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 3)
        #expect(store.todos.map(\.id) == ["final"])
        #expect(!store.loading && store.error == nil)
    }

    @Test func manualAndAutomaticTodoReadersShareTheInFlightGet() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"shared","title":"Shared"}]}"#)
        let automatic = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        var manualEntered = false
        let manual = Task {
            manualEntered = true
            await store.refresh(api: gate.client)
        }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        #expect(gate.reads == 1 && store.loading)
        try gate.answer(1)
        await automatic.value
        await manual.value
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 1)
        #expect(store.byID["shared"]?.title == "Shared")
    }

    @Test(arguments: ["create", "update", "delete"])
    func localWritesRejectTheLatePreWriteSnapshot(_ operation: String) async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Before")], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Before"}]}"#)
        let oldRead = Task { await store.refresh(api: gate.client) }
        defer { oldRead.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        switch operation {
        case "create":
            gate.hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"Created"}}"#)
            _ = try await store.create(title: "Created", api: gate.client)
        case "update":
            gate.hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
            _ = try await store.update(id: "t", title: "Edited", api: gate.client)
        default:
            gate.hub.on("DELETE", "/api/v1/todos/t", json: "{}")
            try await store.delete(id: "t", api: gate.client)
        }
        let afterWrite = store.todos
        try gate.answer(1)
        await oldRead.value
        #expect(store.todos == afterWrite)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 1)
        switch operation {
        case "create": #expect(store.byID["new"]?.title == "Created")
        case "update": #expect(store.byID["t"]?.title == "Edited")
        default: #expect(store.todos.isEmpty)
        }
    }

    @Test func aManualRefreshAfterALocalWriteDoesNotJoinTheStaleRead() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [TodoItem(id: "t", title: "Before")], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Before"}]}"#)
        let old = Task { await store.refresh(api: gate.client) }
        defer { old.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        gate.hub.on("PATCH", "/api/v1/todos/t", json: #"{"todo":{"id":"t","title":"Edited"}}"#)
        _ = try await store.update(id: "t", title: "Edited", api: gate.client)
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t","title":"Confirmed"}]}"#)
        var manualEntered = false
        var manualFinished = false
        let manual = Task {
            manualEntered = true
            await store.refresh(api: gate.client)
            manualFinished = true
        }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        #expect(gate.reads == 1 && !manualFinished)
        try gate.answer(1)
        await old.value
        try await waitForRefreshTest { gate.reads == 2 }
        #expect(store.byID["t"]?.title == "Edited" && !manualFinished)
        try gate.answer(2)
        await manual.value
        #expect(store.byID["t"]?.title == "Confirmed")
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test(arguments: ["linkAgent", "unlinkAgent", "setSpace"])
    func membershipConfirmationUsesANewGetAndIgnoresThePreWriteGet(_ operation: String) async throws {
        let gate = TodoReadGate()
        var pushes = 0
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: { pushes += 1 })
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"old","title":"Before"}]}"#)
        let oldRead = Task { await store.refresh(api: gate.client) }
        defer { oldRead.cancel() }
        try await waitForRefreshTest { gate.reads == 1 }
        let method = operation == "unlinkAgent" ? "DELETE" : "PUT"
        let path = operation == "setSpace" ? TodosLogic.spacePath(todoID: "t", machineID: "m", space: "p") : "/api/v1/todos/t/agents/m/s"
        gate.hub.on(method, path, json: "{}")
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"After","spaces":[]}]}"#)
        let write = Task {
            switch operation {
            case "linkAgent": try await store.linkAgent(todoID: "t", machineID: "m", sessionID: "s", api: gate.client)
            case "unlinkAgent": try await store.unlinkAgent(todoID: "t", machineID: "m", sessionID: "s", api: gate.client)
            default: _ = try await store.setSpace(todoID: "t", space: .init(machineID: "m", space: "p"), linked: true, api: gate.client)
            }
        }
        defer { write.cancel() }
        try await waitForRefreshTest { gate.hub.sent(method, path).count == 1 }
        // Let the completed in-memory write enqueue its confirmation; no simulated time advances.
        for _ in 0..<20 { await Task.yield() }
        #expect(gate.reads == 1)
        try gate.answer(1)
        try await waitForRefreshTest { gate.reads == 2 }
        #expect(store.byID["old"] == nil && pushes == 0)
        try gate.answer(2)
        try await write.value
        await oldRead.value
        #expect(store.byID.keys.sorted() == ["new"])
        #expect(pushes == 1 && !store.loading)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test func aNewTodoEventRecoversAfterAReadError() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos") { _ in throw APIError(status: 500, message: "Temporary failure") }
        let failed = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        try gate.answer(1)
        await failed.value
        #expect(store.error != nil && !store.loading)
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"recovered","title":"Recovered"}]}"#)
        let next = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 2 }
        try gate.answer(2)
        await next.value
        #expect(store.error == nil && store.byID["recovered"] != nil)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 2)
    }

    @Test func aCancelledViewWaitDoesNotCancelAnotherTodoReader() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"shared","title":"Shared"}]}"#)
        let automatic = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        var viewEntered = false
        let view = Task {
            viewEntered = true
            await store.refresh(api: gate.client)
        }
        try await waitForRefreshTest { viewEntered }
        view.cancel()
        await view.value
        #expect(store.loading && gate.reads == 1)
        try gate.answer(1)
        await automatic.value
        #expect(store.byID["shared"] != nil && !store.loading)
        #expect(gate.hub.sent("GET", "/api/v1/todos").count == 1)
    }

    @Test func forgetEndsTodoWaitsBeforeTheOldTransportReturns() async throws {
        let gate = TodoReadGate()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        defer { store.forget(); gate.close() }
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"old","title":"Old"}]}"#)
        let old = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 1 }
        store.forget()
        await old.value // This ends while the cancellation-ignoring transport is still held.
        gate.hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"New"}]}"#)
        let new = store.reload(api: gate.client)
        try await waitForRefreshTest { gate.reads == 2 }
        try gate.answer(1)
        for _ in 0..<20 { await Task.yield() }
        #expect(store.loading && store.todos.isEmpty)
        try gate.answer(2)
        await new.value
        #expect(store.byID.keys.sorted() == ["new"] && !store.loading)
    }
}
