import Foundation
import Testing
@testable import Persona

@MainActor struct PersonalAgentEntryTests {
    private let sessions = #"{"sessions":[{"id":"bridge:a","source":"bridge","sessionKey":"a","provider":"muse","title":"Example conversation","canSend":true,"unreadCount":0,"rankTimeMs":1800000000000}]}"#

    private func sources(_ hub: FakeHub, setup: String? = nil, enabled: Bool = true, accounts: Bool = true) {
        let state = setup.map { #", "setup":"\#($0)""# } ?? ""
        let rows = accounts ? #"[{"accountId":"example","provider":"muse"}]"# : "[]"
        hub.on("GET", "/api/v1/agents", json: #"{"sources":[{"source":"bridge","enabled":\#(enabled)\#(state),"accounts":\#(rows)}]}"#)
    }

    @Test func unknownAvailabilityStaysHiddenUntilBothReadsFinish() async {
        let hub = FakeHub(), entry = PersonalAgentEntryModel()
        let api = hub.client
        sources(hub)
        hub.on("GET", "/api/v1/agents/sessions") { request in
            #expect(!entry.isAvailable(for: api))
            #expect(request.query["summary"] == "1")
            return Data(sessions.utf8)
        }
        #expect(!entry.isAvailable(for: api))
        #expect(!entry.isAvailable(for: nil))
        await entry.refresh(api)
        #expect(entry.isAvailable(for: api))
    }

    @Test(arguments: ["missing", "disabled", "no_accounts"])
    func incompleteSetupHidesEvenStaleSessions(_ setup: String) async {
        let hub = FakeHub(), entry = PersonalAgentEntryModel()
        sources(hub, setup: setup)
        hub.on("GET", "/api/v1/agents/sessions", json: sessions)
        await entry.refresh(hub.client)
        #expect(!entry.isAvailable(for: hub.client))
        #expect(hub.sent("GET", "/api/v1/agents/sessions").isEmpty)
    }

    @Test func noAccountOrDisabledSourceHidesTheEntry() async {
        let hub = FakeHub(), entry = PersonalAgentEntryModel()
        hub.on("GET", "/api/v1/agents/sessions", json: sessions)
        sources(hub, accounts: false)
        await entry.refresh(hub.client)
        #expect(!entry.isAvailable(for: hub.client))
        sources(hub, enabled: false)
        await entry.refresh(hub.client)
        #expect(!entry.isAvailable(for: hub.client))
        hub.on("GET", "/api/v1/agents", json: #"{"sources":[]}"#)
        await entry.refresh(hub.client)
        #expect(!entry.isAvailable(for: hub.client))
    }

    @Test func emptySessionsHideAndLaterSessionsShowWithoutRecreatingTheModel() async {
        let hub = FakeHub(), entry = PersonalAgentEntryModel()
        sources(hub)
        hub.on("GET", "/api/v1/agents/sessions", json: #"{"sessions":[],"has_more":false,"total":0,"revision":"1"}"#)
        await entry.refresh(hub.client)
        #expect(!entry.isAvailable(for: hub.client))
        hub.on("GET", "/api/v1/agents/sessions", json: sessions)
        await entry.refresh(hub.client)
        #expect(entry.isAvailable(for: hub.client))
        hub.on("GET", "/api/v1/agents/sessions", json: #"{"sessions":[]}"#)
        await entry.refresh(hub.client)
        #expect(!entry.isAvailable(for: hub.client))
    }

    @Test func unavailableSiblingSourceDoesNotHideAConfiguredSource() async {
        let hub = FakeHub(), entry = PersonalAgentEntryModel()
        hub.on("GET", "/api/v1/agents", json: #"{"sources":[{"source":"old","enabled":true,"setup":"missing","accounts":[]},{"source":"bridge","enabled":true,"accounts":[{"accountId":"example","provider":"muse"}]}]}"#)
        hub.on("GET", "/api/v1/agents/sessions", json: sessions)
        await entry.refresh(hub.client)
        #expect(entry.isAvailable(for: hub.client))
    }

    @Test func errorsAndChangingHubHidePreviouslyAvailableEntry() async {
        let hub = FakeHub(), entry = PersonalAgentEntryModel()
        sources(hub)
        hub.on("GET", "/api/v1/agents/sessions", json: sessions)
        await entry.refresh(hub.client)
        #expect(entry.isAvailable(for: hub.client))
        var other = hub.client
        other.group = "other"
        #expect(!entry.isAvailable(for: other))
        other = hub.client
        other.config.token = "changed"
        #expect(!entry.isAvailable(for: other))
        hub.on("GET", "/api/v1/agents/sessions") { _ in throw APIError(status: 502, message: "Unavailable") }
        await entry.refresh(hub.client)
        #expect(!entry.isAvailable(for: hub.client))
    }

    @Test func hubEventsRefreshHiddenEntryAndHideItAfterLastSessionLeaves() async throws {
        let hub = FakeHub(), entry = PersonalAgentEntryModel()
        sources(hub)
        hub.on("GET", "/api/v1/agents/sessions", json: #"{"sessions":[]}"#)
        let watch = Task { await entry.watch(hub.client) }
        defer { watch.cancel() }
        try await wait { !hub.sent("GET", "/api/v1/agents/sessions").isEmpty }
        #expect(!entry.isAvailable(for: hub.client))
        hub.on("GET", "/api/v1/agents/sessions", json: sessions)
        PersonalAgentSync.shared.received([])
        try await wait { entry.isAvailable(for: hub.client) }
        hub.on("GET", "/api/v1/agents/sessions", json: #"{"sessions":[]}"#)
        PersonalAgentSync.shared.received(["bridge:a"])
        try await wait { !entry.isAvailable(for: hub.client) }
    }

    private func wait(_ condition: () -> Bool) async throws {
        let deadline = ContinuousClock.now.advanced(by: .seconds(3))
        while !condition(), ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(10)) }
        #expect(condition())
    }
}
