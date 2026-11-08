import Foundation
import Testing
@testable import Persona

/// A new conversation's first message goes once (#1349): one op id on every try, tries through a gap only on a hub that
/// keeps receipts for it, and never again after an answer that says it may have started.
@MainActor
struct AssistantStartTests {
    private static let path = "/api/v1/assistant/conversations"
    private static let started = Data(#"{"session":{"session_id":"s1","status":"working","state":"working","started_by_persona":true,"assistant":true,"pending_interactions":0}}"#.utf8)

    private func run(_ hub: FakeHub, receipts: Bool) async throws -> Session {
        let api = hub.client
        return try await AssistantStart.run(receipts: receipts, restartWait: { _ in }, gapWait: { _, _ in }) { op in
            try await api.startConversation(prompt: "Plan a weekend trip.", main: true, opID: op)
        }
    }

    private func ops(_ hub: FakeHub) -> [String?] { hub.sent("POST", Self.path).map { $0.headers[OpRetry.header] } }

    @Test func aLostAnswerIsAskedAgainWithTheSameOpID() async throws {
        let hub = FakeHub()
        var calls = 0
        hub.on("POST", Self.path) { _ in
            calls += 1
            if calls == 1 { throw URLError(.networkConnectionLost) }
            return Self.started
        }
        let session = try await run(hub, receipts: true)
        #expect(session.sessionID == "s1")
        let sent = ops(hub)
        #expect(sent.count == 2)
        #expect(sent[0] != nil && sent[0] == sent[1])
    }

    @Test func aStartStillRunningIsWaitedFor() async throws {
        let hub = FakeHub()
        var calls = 0
        hub.on("POST", Self.path) { _ in
            calls += 1
            return calls == 1 ? Data(#"{"state":"pending"}"#.utf8) : Self.started
        }
        #expect(try await run(hub, receipts: true).sessionID == "s1")
        #expect(Set(ops(hub)).count == 1 && ops(hub).count == 2)
    }

    @Test func theAssistantRestartingKeepsTheOpID() async throws {
        let hub = FakeHub()
        var calls = 0
        hub.on("POST", Self.path) { _ in
            calls += 1
            if calls == 1 { throw APIError(status: 503, message: "offline", fields: ["code": "assistant_offline"]) }
            return Self.started
        }
        #expect(try await run(hub, receipts: true).sessionID == "s1")
        #expect(Set(ops(hub)).count == 1 && ops(hub).count == 2)
    }

    /// It may have started: asking again would only replay that, and a new op id could start a second one.
    @Test func anAnswerInDoubtIsNotAskedAgain() async throws {
        for answer in [
            { () throws -> Data in throw APIError(status: 502, message: "in doubt", fields: ["code": "assistant_in_doubt", "delivered": "unknown"]) },
            { () throws -> Data in Data(#"{"state":"unknown"}"#.utf8) },
        ] {
            let hub = FakeHub()
            hub.on("POST", Self.path) { _ in try answer() }
            await #expect(throws: APIError.self) { try await run(hub, receipts: true) }
            #expect(ops(hub).count == 1)
        }
    }

    /// An older hub keeps no receipt for a start: a repeat would start a second conversation, so none goes.
    @Test func withoutReceiptsAGapIsNotAskedAgain() async throws {
        let hub = FakeHub()
        hub.on("POST", Self.path) { _ in throw URLError(.networkConnectionLost) }
        await #expect(throws: URLError.self) { try await run(hub, receipts: false) }
        #expect(ops(hub) == [nil])
    }
}
