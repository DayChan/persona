import Foundation
import Testing
@testable import Persona

/// Completing an owner decision is a todo edit, not a recommendation or runtime answer.
@MainActor struct TodoDecisionCompletionTests {
    private let asked = "2026-11-09T11:00:00.123456789Z"
    private let completed = #"{"todo":{"id":"decision","title":"Choose a cache","done_at":"2026-11-10T01:00:00Z"}}"#

    private func row(recommend: String? = "Keep the current cache") -> BriefBody.Row {
        .init(id: "decision", title: "Choose a cache", askedAt: asked, outcome: "decide", recommend: recommend)
    }

    private func model(_ hub: FakeHub, failures: BriefCardTests.Failures? = nil,
                       taken: BriefCardTests.Counter? = nil) -> BriefActions {
        BriefActions(api: { _ in hub.client }, failed: { failures?.messages.append($0) },
                     taken: { taken?.n += 1 }, complete: { id, api in
                         _ = try await api.updateTodo(id: id, done: true)
                     })
    }

    @Test func theHubAskParsesAndAnIdleAgentStillNeedsADecision() throws {
        let todo = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"decision","title":"Choose a cache","ask":{"outcome":"decide","verdict":"Needs your call","recommend":"Keep the current cache","asked_at":"2026-11-09T11:00:00.123456789Z"}}"#.utf8))
        let ask = try #require(todo.openAsk)
        #expect(ask.askedAt == asked)
        #expect(ask.row(for: todo).isDecision && ask.row(for: todo).take == .recommendation)
        #expect(TaskState(todo: todo, agents: [.idle]) == .needsYou)
        let encoded = try JSON.encoder.encode(todo)
        #expect(try JSON.decoder.decode(TodoItem.self, from: encoded).ask == ask)
        var done = todo
        done.doneAt = .now
        #expect(done.openAsk == nil && TaskState(todo: done, agents: [.idle]) == .done)
    }

    @Test(arguments: [#"null"#, #"{"outcome":"decide"}"#, #""bad""#, #"{"asked_at":42}"#, #"{"asked_at":""}"#])
    func aMalformedAskLeavesTheTodoVisible(ask: String) throws {
        let todo = try JSON.decoder.decode(TodoItem.self, from: Data((#"{"id":"decision","title":"Choose a cache","ask":"# + ask + "}").utf8))
        #expect(todo.id == "decision" && todo.openAsk == nil)
    }

    @Test func completionSendsOnlyOneTodoPatchAndNoFollowUp() async throws {
        let hub = FakeHub(), taken = BriefCardTests.Counter()
        hub.on("PATCH", "/api/v1/todos/decision", json: completed)
        let actions = model(hub, taken: taken), decision = row()
        await actions.completeDirectly(decision, group: "")
        #expect(actions.outcome(accepting: decision) == .completed && taken.n == 1)
        #expect(hub.requests.count == 1)
        let body = try #require(hub.sent("PATCH", "/api/v1/todos/decision").first?.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: Bool] == ["done": true])
        await actions.completeDirectly(decision, group: "")
        await actions.accept(decision, group: "")
        #expect(hub.requests.count == 1, "completion blocks both another completion and a recommendation")
    }

    @Test func aDecisionNeedsNoRecommendationToComplete() async {
        let hub = FakeHub()
        hub.on("PATCH", "/api/v1/todos/decision", json: completed)
        let actions = model(hub), decision = row(recommend: nil)
        #expect(decision.take == nil && decision.isDecision)
        await actions.completeDirectly(decision, group: "")
        #expect(actions.outcome(accepting: decision) == .completed && hub.requests.count == 1)
    }

    @Test(arguments: [403, 404, 409, 500, 503])
    func failuresKeepTheCardAndAllowRetry(status: Int) async {
        let hub = FakeHub(), failures = BriefCardTests.Failures(), taken = BriefCardTests.Counter()
        hub.on("PATCH", "/api/v1/todos/decision") { _ in throw APIError(status: status, message: "Completion unavailable") }
        let actions = model(hub, failures: failures, taken: taken), decision = row()
        await actions.completeDirectly(decision, group: "")
        #expect(actions.outcome(accepting: decision) == nil && failures.messages.count == 1 && taken.n == 0)
        hub.on("PATCH", "/api/v1/todos/decision", json: completed)
        await actions.completeDirectly(decision, group: "")
        #expect(actions.outcome(accepting: decision) == .completed && taken.n == 1)
    }

    @Test func unavailableHubShowsAnErrorInsteadOfSilentlyDoingNothing() async {
        let failures = BriefCardTests.Failures()
        let actions = BriefActions(api: { _ in nil }, failed: { failures.messages.append($0) }, taken: {})
        let decision = row()
        await actions.completeDirectly(decision, group: "")
        #expect(failures.messages.count == 1 && actions.outcome(accepting: decision) == nil)
    }

    @Test func twoConcurrentActionsDoNotCompleteThenResumeTheAgent() async {
        let hub = FakeHub()
        hub.on("PATCH", "/api/v1/todos/decision", json: completed)
        let actions = model(hub), decision = row()
        async let first: Void = actions.completeDirectly(decision, group: "")
        async let second: Void = actions.accept(decision, group: "")
        _ = await (first, second)
        #expect(hub.requests.count == 1)
    }

    @Test func completionNeverAppliesToAnOrdinaryVerificationOrUnknownRound() async {
        let hub = FakeHub(), actions = model(hub)
        await actions.completeDirectly(.init(id: "decision", title: "Verify", askedAt: asked), group: "")
        await actions.completeDirectly(.init(id: "decision", title: "Decide", outcome: "decide"), group: "")
        #expect(hub.requests.isEmpty)
    }

    @Test func aSuccessfulCompletionUpdatesTheSharedTaskIndexAndPendingCount() async throws {
        let hub = FakeHub(), decision = row()
        let ask = TodoAsk(outcome: "decide", recommend: decision.recommend, askedAt: asked)
        let todo = TodoItem(id: decision.id, title: decision.title, ask: ask)
        let store = TodoStore(cachedTodos: [todo], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store)
        hub.on("PATCH", "/api/v1/todos/decision", json: completed)
        let actions = BriefActions(api: { _ in hub.client }, failed: { _ in }, taken: {}, complete: { id, api in
            _ = try await store.update(id: id, done: true, api: api)
        })
        #expect(tasks.state(todo) == .needsYou)
        await actions.completeDirectly(decision, group: "")
        let finished = try #require(tasks.todos[decision.id])
        #expect(finished.doneAt != nil && finished.openAsk == nil && tasks.state(finished) == .done)
        let tray = ActionTrayContent(tasks: [.init(id: finished.id, state: tasks.state(finished))], own: [], taskAgents: [])
        #expect(tray.pending.isEmpty && tray.count(.pending) == 0)
    }

    @Test func anotherHubOrRoundDoesNotInheritCompletion() async {
        let hub = FakeHub()
        hub.on("PATCH", "/api/v1/todos/decision", json: completed)
        let actions = model(hub), decision = row()
        await actions.completeDirectly(decision, group: "first")
        #expect(actions.outcome(accepting: decision, group: "first") == .completed)
        #expect(actions.outcome(accepting: decision, group: "second") == nil)
        var newer = decision
        newer.askedAt = "2026-11-10T02:00:00Z"
        #expect(actions.outcome(accepting: newer, group: "first") == nil)
    }
}
