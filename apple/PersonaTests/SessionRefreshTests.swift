import Foundation
import Testing
@testable import Persona

@MainActor
struct SessionRefreshTests {
    nonisolated private struct Row: Decodable, Equatable, Sendable {
        var id: String
        var text: String
    }
    private typealias Page = SessionListPage<Row>

    private final class Gate {
        private(set) var starts = 0
        private var pending: [CheckedContinuation<Page, any Error>] = []
        func read() async throws -> Page {
            starts += 1
            return try await withCheckedThrowingContinuation { pending.append($0) }
        }
        func finish(_ page: Page) throws {
            let next = try #require(pending.first)
            pending.removeFirst()
            next.resume(returning: page)
        }
        func fail() throws {
            let next = try #require(pending.first)
            pending.removeFirst()
            next.resume(throwing: APIError(status: 409, message: "List snapshot changed"))
        }
        func close() {
            let remaining = pending
            pending = []
            for next in remaining { next.resume(throwing: CancellationError()) }
        }
    }

    private func page(_ text: String, revision: String? = nil, more: Bool? = nil) -> Page {
        Page(sessions: [Row(id: "row", text: text)], remove: [], hasMore: more,
             nextCursor: more == true ? "cursor" : nil, revision: revision,
             total: revision == nil ? nil : 100)
    }

    private func counts() -> SessionListCounts {
        SessionListCounts(total: 100, machine: [:], project: [:], group: [:], state: [:], revision: "counts")
    }

    @Test func aBurstDuringALegacyReadProducesOnlyOneTrailingRead() async throws {
        let gate = Gate()
        var hints = 0
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, _, _ in
            try await gate.read()
        }, counts: { _ in counts() }, waitForHints: { hints += 1 })
        defer { list.cancel(); gate.close() }
        let load = Task { await list.load() }
        try await waitForRefreshTest { gate.starts == 1 }
        for _ in 0..<50 { list.invalidate() }
        try await waitForRefreshTest { hints == 1 }
        await Task.yield()
        #expect(gate.starts == 1)
        try gate.finish(page("old"))
        try await waitForRefreshTest { gate.starts == 2 }
        var entered = false
        let manual = Task { entered = true; await list.refresh() }
        defer { manual.cancel() }
        try await waitForRefreshTest { entered }
        try gate.finish(page("latest"))
        await manual.value
        await load.value
        #expect(gate.starts == 2 && list.rows.first?.text == "latest")
    }

    @Test func manualRefreshAbsorbsAHintButDoesNotJoinThePreHintDelta() async throws {
        let gate = Gate()
        let hints = AsyncStream<Void>.makeStream()
        var calls = 0
        var hintEntered = false
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, _, since in
            calls += 1
            if since == nil { return page("initial", revision: "r0", more: false) }
            if calls == 2 { return try await gate.read() }
            return page("latest", revision: "r2", more: false)
        }, counts: { _ in counts() }, waitForHints: {
            hintEntered = true
            for await _ in hints.stream { break }
        })
        defer { list.cancel(); gate.close(); hints.continuation.finish() }
        await list.load()
        let beforeHint = Task { await list.synchronize() }
        try await waitForRefreshTest { gate.starts == 1 }
        list.invalidate()
        try await waitForRefreshTest { hintEntered }
        var manualEntered = false
        let manual = Task { manualEntered = true; await list.refresh() }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        #expect(calls == 2)
        try gate.finish(page("old", revision: "r1", more: false))
        await manual.value
        await beforeHint.value
        #expect(calls == 3 && list.revision == "r2" && list.rows.first?.text == "latest")
    }

    @Test func aLoadJoiningExpiredPaginationDoesNotSkipItsBaselineRecovery() async throws {
        let gate = Gate()
        var calls = 0
        let list = SessionListModel<Row>(id: { $0.id }, removalID: { $0 }, fetch: { _, cursor, _ in
            calls += 1
            if cursor != nil { return try await gate.read() }
            return page(calls == 1 ? "initial" : "recovered", revision: calls == 1 ? "r0" : "r2", more: calls == 1)
        }, counts: { _ in counts() }, waitForHints: {})
        defer { list.cancel(); gate.close() }
        await list.load()
        let next = Task { await list.next() }
        try await waitForRefreshTest { gate.starts == 1 }
        var entered = false
        let joined = Task { entered = true; await list.load() }
        defer { joined.cancel() }
        try await waitForRefreshTest { entered }
        try gate.fail()
        await next.value
        await joined.value
        #expect(calls == 3 && list.loaded && list.revision == "r2")
        #expect(list.rows.first?.text == "recovered" && !list.loading)
    }
}
