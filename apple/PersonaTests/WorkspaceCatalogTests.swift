import Foundation
import Testing
@testable import Persona

@MainActor struct WorkspaceCatalogTests {
    @Test func refreshScopesParentsToTheHubAndRetainsTheLastSuccessfulSnapshot() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/workspaces", json: #"{"workspaces":[{"machine_id":"m","source":"orca","source_id":"child","parent_source_id":"root","title":"Child","cwd":"/work/child","index":2}]}"#)
        let catalog = WorkspaceCatalog()
        await catalog.refresh(["work"]) { _ in hub.client }
        #expect(catalog.revision == 1)
        let item = try #require(catalog.workspaces.first)
        #expect(item.key == "work|m/orca:child")
        #expect(item.parentKey == "work|m/orca:root")
        await catalog.refresh(["work"]) { _ in hub.client }
        #expect(catalog.revision == 1, "an unchanged catalog does not invalidate the list projection")
        hub.on("GET", "/api/v1/workspaces") { _ in throw APIError(status: 503, message: "unavailable") }
        await catalog.refresh(["work"]) { _ in hub.client }
        #expect(catalog.workspaces == [item])
        #expect(catalog.revision == 1)
        await catalog.refresh([]) { _ in nil }
        #expect(catalog.workspaces.isEmpty, "a removed hub cannot leave its workspace containers behind")
        #expect(catalog.revision == 2)
    }

    @Test func oldHubFallsBackWithoutInventingWorkspaceOrAgentRows() async {
        let hub = FakeHub()
        let catalog = WorkspaceCatalog()
        await catalog.refresh([""]) { _ in hub.client }
        #expect(catalog.workspaces.isEmpty)
    }

    /// Hold the real FakeHub response after it was read, so later requests can finish first without scheduler sleeps.
    private actor Responses {
        private var count = 0
        private var pending: [Int: CheckedContinuation<Void, Never>] = [:]
        private var arrivals: [Int: CheckedContinuation<Void, Never>] = [:]

        func hold() async {
            count += 1
            let id = count
            await withCheckedContinuation { continuation in
                pending[id] = continuation
                arrivals.removeValue(forKey: id)?.resume()
            }
        }

        func started(_ id: Int) async {
            if pending[id] != nil { return }
            await withCheckedContinuation { arrivals[id] = $0 }
        }

        func release(_ id: Int) { pending.removeValue(forKey: id)!.resume() }
    }

    private func delayed(_ hub: FakeHub, responses: Responses) -> APIClient {
        var client = hub.client
        let answer = client.transport!
        client.transport = { request in
            let data = try await answer(request)
            await responses.hold()
            return data
        }
        return client
    }

    private func answer(_ hub: FakeHub, title: String) {
        hub.on("GET", "/api/v1/workspaces", json: """
        {"workspaces":[{"machine_id":"m","source":"orca","source_id":"w","title":"\(title)","cwd":"/work/w"}]}
        """)
    }

    @Test func anOlderPollingResponseCannotUndoARenameRefresh() async {
        let hub = FakeHub(), responses = Responses(), catalog = WorkspaceCatalog()
        answer(hub, title: "Original")
        await catalog.refresh(["work"]) { _ in hub.client }
        let client = delayed(hub, responses: responses)
        let polling = Task { await catalog.refresh(["work"]) { _ in client } }
        await responses.started(1)
        answer(hub, title: "Renamed")
        let renamed = Task { await catalog.refresh(["work"]) { _ in client } }
        await responses.started(2)
        await responses.release(2)
        await renamed.value
        #expect(catalog.workspaces.map(\.title) == ["Renamed"])
        let revision = catalog.revision
        await responses.release(1)
        await polling.value
        #expect(catalog.workspaces.map(\.title) == ["Renamed"])
        #expect(catalog.revision == revision, "a superseded response cannot republish the old snapshot")
    }

    @Test func removingAHubRejectsItsPendingResponseAndPreservesTheOtherHubsNewerSnapshot() async {
        let kept = FakeHub(), removed = FakeHub(), responses = Responses(), catalog = WorkspaceCatalog()
        answer(kept, title: "Kept original"); answer(removed, title: "Removed")
        await catalog.refresh(["kept", "removed"]) { $0 == "kept" ? kept.client : removed.client }
        let pendingClient = delayed(removed, responses: responses)
        let polling = Task {
            await catalog.refresh(["kept", "removed"]) { $0 == "kept" ? kept.client : pendingClient }
        }
        await responses.started(1)
        answer(kept, title: "Kept renamed")
        await catalog.refresh(["kept"]) { _ in kept.client }
        #expect(catalog.workspaces.map(\.group) == ["kept"])
        #expect(catalog.workspaces.map(\.title) == ["Kept renamed"])
        let revision = catalog.revision
        await responses.release(1)
        await polling.value
        #expect(catalog.workspaces.map(\.group) == ["kept"])
        #expect(catalog.workspaces.map(\.title) == ["Kept renamed"])
        #expect(catalog.revision == revision, "a removed hub cannot restore its rows or overwrite a retained hub")
    }

    @Test func unchangedMultiHubRefreshDoesNotReorderOrRepublishSnapshots() async {
        let a = FakeHub(), b = FakeHub(), catalog = WorkspaceCatalog()
        answer(a, title: "A"); answer(b, title: "B")
        await catalog.refresh(["a", "b"]) { $0 == "a" ? a.client : b.client }
        let revision = catalog.revision
        await catalog.refresh(["a", "b"]) { $0 == "a" ? a.client : b.client }
        #expect(catalog.workspaces.map(\.group) == ["a", "b"])
        #expect(catalog.revision == revision)
        a.on("GET", "/api/v1/workspaces") { _ in throw APIError(status: 503, message: "unavailable") }
        await catalog.refresh(["a", "b"]) { $0 == "a" ? a.client : b.client }
        #expect(catalog.workspaces.map(\.title) == ["A", "B"], "one failed hub retains its last successful snapshot")
        #expect(catalog.revision == revision)
    }
}
