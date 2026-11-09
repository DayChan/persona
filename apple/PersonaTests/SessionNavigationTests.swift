import Foundation
import Testing
@testable import Persona

/// A controllable async hub. Delayed replies deliberately ignore cancellation to exercise the model's fences.
@MainActor private final class NavigationTestHub {
    typealias Route = @MainActor (LocalRequest) async throws -> Data
    var routes: [String: Route] = [:]
    var requests: [LocalRequest] = []
    var pending: [String: CheckedContinuation<Data, Error>] = [:]

    var client: APIClient {
        var api = APIClient(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "test"))
        api.transport = { [self] request in try await answer(request) }
        return api
    }
    func answer(_ request: LocalRequest) async throws -> Data {
        requests.append(request)
        guard let route = routes[request.path] else { throw APIError(status: 404, message: "No test route") }
        return try await route(request)
    }
    func hold(_ name: String) async throws -> Data {
        try await withCheckedThrowingContinuation { pending[name] = $0 }
    }
    func release(_ name: String, json: String) {
        pending.removeValue(forKey: name)?.resume(returning: Data(json.utf8))
    }
    func fail(_ name: String) {
        pending.removeValue(forKey: name)?.resume(throwing: APIError(status: 503, message: "Temporary failure"))
    }
    func sent(_ path: String) -> [LocalRequest] { requests.filter { $0.path == path } }
}

@MainActor struct SessionNavigationTests {
    private let navigationPath = "/api/v1/sessions/m/session/navigation"
    private let itemsPath = "/api/v1/sessions/m/session/items"
    private var key: SessionKey { SessionKey(machineID: "m", sessionID: "session") }

    private func item(_ seq: Int) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: "user", kind: .text, text: "Input \(seq)",
             toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
    }
    private func index(_ range: Range<Int>, more: Bool = true, before: Int? = nil, history: String = "history-a") -> String {
        let messages = range.map { #"{"id":"\#($0)#0","seq":\#($0),"text":"Input \#($0)"}"# }.joined(separator: ",")
        let cursor = before ?? range.lowerBound
        return #"{"messages":[\#(messages)],"has_more":\#(more),"next_before_seq":\#(cursor),"history":"\#(history)"}"#
    }
    private func page(_ seq: Int, more: Bool = true, after: Bool = true, history: String = "history-a") -> String {
        #"{"items":[{"id":"\#(seq)#0","seq":\#(seq),"role":"user","kind":"text","text":"Input \#(seq)"}],"has_more":\#(more),"more_after":\#(after),"from_seq":\#(seq),"to_seq":\#(seq),"history":"\#(history)"}"#
    }
    private func windowPage(_ items: [Item], from: Int, to: Int, more: Bool = true,
                            history: String = "history-a") throws -> String {
        let encoded = String(decoding: try JSON.encoder.encode(ChatStore.ordered(items)), as: UTF8.self)
        return #"{"items":\#(encoded),"has_more":\#(more),"more_after":true,"from_seq":\#(from),"to_seq":\#(to),"history":"\#(history)"}"#
    }
    private func photo(_ seq: Int, part: Int) -> Item {
        var image = item(seq)
        image.id = "\(seq)#\(part)"
        image.kind = .image
        image.text = nil
        var payload = ImagePayload(mime: "image/png", data: "")
        payload.lazy = true
        image.image = payload
        return image
    }
    private func live(_ key: SessionKey? = nil, seq: Int = 1000, history: String = "history-a") -> ChatStore {
        let store = ChatStore(key: key ?? self.key, cachesTail: false)
        store.apply([item(seq)], span: ItemSpan(history: history, from: nil, to: seq), backlog: true, hasMore: true)
        return store
    }
    private func waitFor(_ condition: @MainActor () -> Bool) async throws {
        let deadline = ContinuousClock.now + .seconds(2)
        while !condition(), ContinuousClock.now < deadline { try await Task.sleep(for: .milliseconds(1)) }
        let ready = condition()
        try #require(ready)
    }

    @Test func initialIndexHasFortyMarksWithoutLoadingBodies() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in Data(index(100..<140).utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(model.marks.count == 40)
        #expect(model.marks.first?.seq == 100 && model.marks.last?.seq == 139)
        #expect(hub.sent(navigationPath).first?.query == ["limit": "40"])
        #expect(hub.sent(itemsPath).isEmpty)
        #expect(model.window == nil)
        let scrub = EdgeScrubState()
        #expect(scrub.open(model.marks, height: 300, top: 60, at: 0))
        #expect(scrub.column?.first == 0 && scrub.column?.count == 40)
        #expect(scrub.pickedMark?.id == "100#0")
    }

    @Test func sparseInitialPagesWarmFortyWithExplicitCursorsIncludingEmptyPages() async {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { request in
            switch request.query["before_seq"] {
            case "100": return Data(index(80..<80, before: 80).utf8)
            case "80": return Data(index(40..<80).utf8)
            default: return Data(index(100..<102).utf8)
            }
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(model.marks.count == 42)
        #expect(hub.sent(navigationPath).map { $0.query["before_seq"] } == [nil, "100", "80"])
        #expect(hub.sent(itemsPath).isEmpty && model.hasMore && model.nextBeforeSeq == 40)
    }

    @Test func emptyInitialWarmUpStopsAtItsPageBudgetAndKeepsTheCursor() async {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { request in
            let cursor = Int(request.query["before_seq"] ?? "1000")! - 1
            return Data(index(cursor..<cursor, before: cursor).utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(hub.sent(navigationPath).count == SessionNavigationModel.initialPageLimit)
        #expect(model.hasMore && model.nextBeforeSeq == 1000 - SessionNavigationModel.initialPageLimit)
        #expect(!model.hasCompleteIndex && model.marks.isEmpty)
        // A later, deliberate older-half movement can continue from the saved budget cursor.
        await model.prefetch(picked: 0, count: 2, movingOlder: true, enteredOlderHalf: false, beforeSeq: model.nextBeforeSeq)
        #expect(hub.sent(navigationPath).count == SessionNavigationModel.initialPageLimit + 1)
    }

    @Test func initialWarmUpStopsOnANonAdvancingCursor() async {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in Data(index(100..<102, before: 100).utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(hub.sent(navigationPath).count == 2)
        #expect(model.hasMore && model.nextBeforeSeq == nil && model.marks.count == 2)
    }

    @Test func earlierHalfPrefetchesOnlyTowardOlderOrOnEntry() {
        #expect(!SessionNavigationModel.shouldPrefetch(picked: 20, count: 40, movingOlder: true, enteredOlderHalf: false))
        #expect(SessionNavigationModel.shouldPrefetch(picked: 19, count: 40, movingOlder: true, enteredOlderHalf: false))
        #expect(SessionNavigationModel.shouldPrefetch(picked: 19, count: 40, movingOlder: false, enteredOlderHalf: true))
        #expect(!SessionNavigationModel.shouldPrefetch(picked: 19, count: 40, movingOlder: false, enteredOlderHalf: false))
        #expect(!SessionNavigationModel.shouldPrefetch(picked: nil, count: 40, movingOlder: true, enteredOlderHalf: true))
    }

    @Test func prependAndLatestAppendKeepIdentityPreviewAndFingerMapping() throws {
        let marks = (100..<140).map { EdgeMark(id: "\($0)", text: "Input \($0)", seq: $0) }
        let scrub = EdgeScrubState()
        #expect(scrub.open(marks, height: 600, top: 60, at: 0))
        let column = try #require(scrub.column), finger = column.y(of: 19)
        scrub.move(to: finger)
        let selected = try #require(scrub.pickedMark)
        let older = (60..<100).map { EdgeMark(id: "\($0)", text: "Input \($0)", seq: $0) }
        scrub.update(older + marks)
        #expect(scrub.pickedMark == selected && scrub.picked == 59)
        #expect(scrub.column?.y(of: 59) == finger)
        #expect(!scrub.move(to: finger))
        #expect(!scrub.enteredOlderHalf)
        let newer = (140..<145).map { EdgeMark(id: "\($0)", text: "Input \($0)", seq: $0) }
        scrub.update(older + marks + newer)
        #expect(scrub.pickedMark == selected)
        #expect(!scrub.move(to: finger))
        #expect(scrub.column?.index(at: -1000) == 0)
        #expect(scrub.column?.index(at: 1000) == 84)
        // Recomputed half after prepend: 59 is now in the newer half of eighty-five.
        #expect(!SessionNavigationModel.shouldPrefetch(picked: scrub.picked, count: scrub.marks.count,
                                                     movingOlder: true, enteredOlderHalf: false))
    }

    @Test func olderRequestsDeduplicateAndFailureCanRetrySameCursor() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { request in
            if request.query["before_seq"] != nil { return try await hub.hold("older") }
            return Data(index(100..<140).utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        let first = Task { await model.loadIndex(older: true) }
        try await waitFor { hub.pending["older"] != nil }
        await model.loadIndex(older: true)
        #expect(hub.sent(navigationPath).count == 2)
        hub.fail("older")
        await first.value
        #expect(model.nextBeforeSeq == 100 && model.marks.count == 40)
        let retry = Task { await model.loadIndex(older: true) }
        try await waitFor { hub.pending["older"] != nil }
        hub.release("older", json: index(60..<100))
        await retry.value
        #expect(model.marks.count == 80 && model.nextBeforeSeq == 60)
        #expect(model.error == nil)
        #expect(hub.sent(navigationPath).last?.query["before_seq"] == "100")
        await model.prefetch(picked: 1, count: 40, movingOlder: true, enteredOlderHalf: false, beforeSeq: 100)
        #expect(hub.sent(navigationPath).count == 3) // queued callbacks cannot consume the new cursor
    }

    @Test func emptyOlderPageAdvancesExplicitCursorWithoutDiscardingMarks() async {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { request in
            switch request.query["before_seq"] {
            case "100": return Data(index(80..<80, before: 80).utf8)
            case "80": return Data(index(40..<80, more: false).utf8)
            default: return Data(index(100..<140).utf8)
            }
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        await model.loadIndex(older: true)
        #expect(model.hasMore && model.nextBeforeSeq == 80 && model.marks.count == 40)
        await model.loadIndex(older: true)
        #expect(!model.hasMore && model.marks.count == 80)
    }

    @Test func refreshingLatestKeepsOlderPagesAndTheirCursor() async {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        var newest = 100..<140
        hub.routes[navigationPath] = { request in
            Data(index(request.query["before_seq"] == nil ? newest : 60..<100).utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        await model.loadIndex(older: true)
        newest = 105..<145
        await model.loadIndex(refresh: true)
        #expect(model.marks.count == 85 && model.marks.first?.seq == 60 && model.marks.last?.seq == 144)
        #expect(model.nextBeforeSeq == 60)
    }

    @Test func disjointLatestRefreshCannotClaimAnUnfetchedIndexGapIsComplete() async {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        var newest = false
        hub.routes[navigationPath] = { _ in Data(index(newest ? 200..<240 : 100..<140, more: newest).utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(model.hasCompleteIndex)
        newest = true
        await model.loadIndex(refresh: true)
        #expect(model.marks.count == 80 && model.nextBeforeSeq == 200)
        #expect(model.hasMore && !model.hasCompleteIndex)
        #expect(hub.sent(itemsPath).isEmpty)
    }

    @Test func optionalIndexHistoryIsOmittedThenWindowPagesReuseTheirReturnedToken() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in
            Data(index(100..<140).replacingOccurrences(of: ",\"history\":\"history-a\"", with: "").utf8)
        }
        hub.routes[itemsPath] = { request in Data(page(request.query["after_seq"] == nil ? 100 : 600).utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        _ = await model.select(try #require(model.marks.first), loaded: false)
        #expect(model.window != nil && hub.sent(itemsPath).first?.query["history"] == nil)
        await model.loadWindow(older: false)
        #expect(hub.sent(itemsPath).last?.query["history"] == "history-a")
    }

    @Test func switchingSessionsDiscardsLateIndexResponse() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in try await hub.hold("index") }
        let opening = Task { await model.activate(key, api: hub.client, supported: true, live: source) }
        try await waitFor { hub.pending["index"] != nil }
        let next = SessionKey(machineID: "m", sessionID: "other")
        hub.routes["/api/v1/sessions/m/other/navigation"] = { _ in Data(index(200..<240).utf8) }
        await model.activate(next, api: hub.client, supported: true, live: live(next))
        hub.release("index", json: index(100..<140))
        await opening.value
        #expect(model.marks.first?.seq == 200 && model.marks.count == 40)
    }

    @Test func unloadedBodyWaitsForReleaseAndNeverChangesLiveCoverage() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in Data(index(100..<140).utf8) }
        hub.routes[itemsPath] = { _ in Data(page(100).utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        let scrub = EdgeScrubState()
        scrub.open(model.marks, height: 600, top: 60, at: 0)
        scrub.move(to: try #require(scrub.column).y(of: 2))
        #expect(hub.sent(itemsPath).isEmpty && model.window == nil)
        let target = try #require(model.marks.first)
        let selected = await model.select(target, loaded: false)
        #expect(selected?.id == "100#0")
        let reading = try #require(model.window)
        #expect(reading !== source && reading.items.map(\.seq) == [100])
        #expect(reading.covered == 0) // isolated display data is never a live synchronization baseline
        #expect(source.items.map(\.seq) == [1000] && source.covered == 1000 && source.history == "history-a")
        #expect(source.hasMore)
        #expect(hub.sent(itemsPath).first?.query == ["around_seq": "100", "limit": "80", "lazy_images": "1", "fold_work": "1", "history": "history-a"])
        model.returnToLatest()
        #expect(model.window == nil && source.covered == 1000 && source.items.count == 1)
    }

    @Test func loadedSelectionDoesNotFetchBody() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live(seq: 100)
        hub.routes[navigationPath] = { _ in Data(index(100..<140).utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(await model.select(try #require(model.marks.first), loaded: true)?.id == "100#0")
        #expect(hub.sent(itemsPath).isEmpty && model.window == nil)
    }

    @Test func targetRequestsDeduplicateFailAndRetry() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in Data(index(100..<140).utf8) }
        hub.routes[itemsPath] = { _ in try await hub.hold("target") }
        await model.activate(key, api: hub.client, supported: true, live: source)
        let target = try #require(model.marks.first)
        let first = Task { await model.select(target, loaded: false) }
        try await waitFor { hub.pending["target"] != nil }
        #expect(await model.select(target, loaded: false) == nil)
        #expect(hub.sent(itemsPath).count == 1)
        hub.fail("target")
        #expect(await first.value == nil)
        #expect(!model.loadingTarget && model.window == nil)
        let retry = Task { await model.select(target, loaded: false) }
        try await waitFor { hub.pending["target"] != nil }
        hub.release("target", json: page(100))
        #expect(await retry.value?.id == "100#0")
        #expect(hub.sent(itemsPath).count == 2)
    }

    @Test func latestAndSessionSwitchFenceDelayedTargetReplies() async throws {
        for switchSession in [false, true] {
            let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
            hub.routes[navigationPath] = { _ in Data(index(100..<140).utf8) }
            hub.routes[itemsPath] = { _ in try await hub.hold("target") }
            await model.activate(key, api: hub.client, supported: true, live: source)
            let target = try #require(model.marks.first)
            let selecting = Task { await model.select(target, loaded: false) }
            try await waitFor { hub.pending["target"] != nil }
            if switchSession {
                let next = SessionKey(machineID: "m", sessionID: "other")
                await model.activate(next, api: hub.client, supported: false, live: live(next))
            } else { model.returnToLatest() }
            hub.release("target", json: page(100))
            #expect(await selecting.value == nil)
            #expect(model.window == nil && source.covered == 1000)
        }
    }

    @Test func liveRewriteAndStaleHistoryCannotPublishOldWindow() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in Data(index(100..<140).utf8) }
        hub.routes[itemsPath] = { _ in try await hub.hold("target") }
        await model.activate(key, api: hub.client, supported: true, live: source)
        let target = try #require(model.marks.first)
        let selecting = Task { await model.select(target, loaded: false) }
        try await waitFor { hub.pending["target"] != nil }
        source.replace(with: [item(10)], hasMore: false, span: ItemSpan(history: "history-b", from: nil, to: 10))
        hub.release("target", json: page(100))
        #expect(await selecting.value == nil && model.window == nil)
        hub.routes[navigationPath] = { _ in Data(index(10..<50, history: "history-b").utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        hub.routes[itemsPath] = { _ in throw APIError(status: 409, message: "History changed", fields: ["code": "stale_history"]) }
        #expect(await model.select(try #require(model.marks.first), loaded: false) == nil)
        #expect(model.window == nil && model.history == "history-b" && source.covered == 10)
    }

    @Test func firstBacklogHistoryStartsANewWarmWithoutWaitingForTheOldIndexSlot() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel()
        let source = ChatStore(key: key, cachesTail: false)
        var calls = 0
        hub.routes[navigationPath] = { _ in
            calls += 1
            if calls == 1 { return try await hub.hold("before-backlog") }
            return Data(index(200..<240).utf8)
        }
        let first = Task { await model.activate(key, api: hub.client, supported: true, live: source) }
        try await waitFor { hub.pending["before-backlog"] != nil }
        let resets = source.resets
        source.apply([item(1000)], span: ItemSpan(history: "history-a", from: nil, to: 1000), backlog: true, hasMore: true)
        #expect(source.history == "history-a" && source.resets == resets)
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(model.marks.count == 40 && model.marks.first?.seq == 200)
        hub.release("before-backlog", json: index(100..<140))
        await first.value
        #expect(model.marks.count == 40 && model.marks.first?.seq == 200 && calls == 2)
    }

    @Test func indexHistoryRefusalDropsTheWindowAndWarmsWithFreshToken() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        var rewritten = false
        hub.routes[navigationPath] = { request in
            if request.query["before_seq"] != nil, request.query["history"] == "history-a" {
                rewritten = true
                throw APIError(status: 409, message: "History changed", fields: ["code": "stale_history"])
            }
            if rewritten {
                return Data(index(request.query["before_seq"] == nil ? 200..<202 : 160..<200,
                                  more: request.query["before_seq"] == nil, history: "history-b").utf8)
            }
            return Data(index(100..<140).utf8)
        }
        hub.routes[itemsPath] = { _ in Data(page(100).utf8) }
        await model.activate(key, api: hub.client, supported: true, live: source)
        _ = await model.select(try #require(model.marks.first), loaded: false)
        #expect(model.window != nil)
        await model.loadIndex(older: true)
        let requests = hub.sent(navigationPath)
        #expect(requests.count == 4 && requests[1].query["history"] == "history-a")
        #expect(requests[2].query["history"] == nil && requests[3].query["history"] == "history-b")
        #expect(model.window == nil && model.history == "history-b" && model.marks.count == 42)
        #expect(model.error == nil && source.covered == 1000)
    }

    @Test func refreshedShortHistoryWarmsUnderItsReplacementGeneration() async {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        var rewritten = false
        hub.routes[navigationPath] = { request in
            if rewritten {
                return Data(index(request.query["before_seq"] == nil ? 200..<202 : 160..<200,
                                  more: request.query["before_seq"] == nil, history: "history-b").utf8)
            }
            return Data(index(100..<140).utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        rewritten = true
        await model.loadIndex(refresh: true)
        #expect(model.history == "history-b" && model.marks.count == 42 && model.marks.first?.seq == 160)
        #expect(hub.sent(navigationPath).count == 3)
        #expect(hub.sent(navigationPath).last?.query["history"] == "history-b")
    }

    @Test func windowBackwardPagingUsesActualItemsNotExclusiveCoverageAndKeepsImageSiblings() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        let around = (41...80).map { item($0) } + [photo(41, part: 1), photo(41, part: 2)]
        let older = (1...40).map { item($0) } + [photo(40, part: 1), photo(40, part: 2)]
        hub.routes[navigationPath] = { _ in Data(index(41..<81).utf8) }
        hub.routes[itemsPath] = { request in
            if let before = request.query["before_seq"] {
                // Exercise the server's exclusive predicate: the old before=40 bug would really omit seq 40.
                let cutoff = Int(before)!
                let items = cutoff > 1 ? older.filter { $0.seq < cutoff } : [item(0), photo(0, part: 1)]
                return Data(try windowPage(items, from: cutoff > 1 ? 0 : -1, to: cutoff - 1, more: cutoff > 1).utf8)
            }
            return Data(try windowPage(around, from: 40, to: 80).utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        _ = await model.select(try #require(model.marks.first), loaded: false)
        await model.loadWindow(older: true)
        #expect(hub.sent(itemsPath).last?.query["before_seq"] == "41")
        #expect(model.window?.items.filter { $0.seq == 40 }.map(\.id) == ["40#0", "40#1", "40#2"])
        #expect(model.window?.items.filter { $0.seq == 41 }.map(\.id) == ["41#0", "41#1", "41#2"])
        await model.loadWindow(older: true)
        #expect(hub.sent(itemsPath).last?.query["before_seq"] == "1")
        #expect(model.window?.items.filter { $0.seq == 0 }.map(\.id) == ["0#0", "0#1"])
        #expect(model.window?.items.filter { $0.kind == .text }.map(\.seq) == Array(0...80))
        #expect(model.window?.covered == 0 && source.covered == 1000 && source.items.map(\.seq) == [1000])
    }

    @Test func staleFoldExpansionInvalidatesItsWindowAndWarmsRatherThanLeavingOldHistory() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        var rewritten = false
        var folded = item(101)
        folded.role = "assistant"
        folded.kind = .fold
        folded.text = nil
        folded.fold = Fold(items: 3, tools: 1, thoughts: 1, until: nil, throughSeq: 103, throughID: "103#0")
        hub.routes[navigationPath] = { _ in Data(index(rewritten ? 200..<240 : 100..<140,
                                                      history: rewritten ? "history-b" : "history-a").utf8) }
        hub.routes[itemsPath] = { request in
            if request.query["from_seq"] != nil {
                #expect(request.query["history"] == "history-a")
                rewritten = true
                throw APIError(status: 409, message: "History changed", fields: ["code": "stale_history"])
            }
            return Data(try windowPage([item(100), folded], from: 99, to: 103).utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        _ = await model.select(try #require(model.marks.first), loaded: false)
        let fold = try #require(model.window?.items.first { $0.kind == .fold })
        do {
            try await model.unfold(fold)
            Issue.record("Expected the history refusal to propagate after invalidating the old window")
        } catch {
            #expect((error as? APIError)?.code == "stale_history")
        }
        #expect(model.window == nil && model.history == "history-b" && model.marks.count == 40)
        #expect(source.items.map(\.seq) == [1000] && source.covered == 1000)
    }

    @Test func windowPagesAreExplicitBoundedAndHistoryScoped() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in Data(index(100..<140).utf8) }
        hub.routes[itemsPath] = { request in
            if request.query["before_seq"] != nil { return Data(page(50, more: false).utf8) }
            if request.query["after_seq"] != nil { return Data(page(600, more: false).utf8) }
            return Data(page(100).utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        _ = await model.select(try #require(model.marks.first), loaded: false)
        #expect(hub.sent(itemsPath).count == 1) // more_after never starts an automatic catch-up chain
        await model.loadWindow(older: true)
        await model.loadWindow(older: false)
        #expect(model.window?.items.map(\.seq) == [50, 100, 600])
        #expect(source.items.map(\.seq) == [1000] && source.covered == 1000)
        let newer = try #require(hub.sent(itemsPath).last)
        #expect(newer.query["after_seq"] == "100" && newer.query["limit"] == "500")
        #expect(newer.query["history"] == "history-a")
        #expect(hub.sent(itemsPath).count == 3)
    }

    @Test func legacyHubAndUnsupportedRuntimeKeepLoadedLineFallback() async throws {
        for supported in [false, true] {
            let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
            hub.routes[navigationPath] = { _ in throw APIError(status: 502, message: "Runtime navigation unsupported", fields: ["code": "unsupported"]) }
            await model.activate(key, api: hub.client, supported: supported, live: source)
            #expect(model.marks.isEmpty && !model.hasMore && model.window == nil && !model.hasCompleteIndex)
            let fallback = EdgeScrubState.marks(source.items.map { ChatLine.row(.user($0)) })
            #expect(fallback.count == 1)
            #expect(await model.select(try #require(fallback.first), loaded: true)?.id == "1000#0")
            #expect(hub.sent(itemsPath).isEmpty)
            #expect(hub.sent(navigationPath).count == (supported ? 1 : 0))
        }
    }

    @Test func passiveSelectionUsesPredecessorIntervalsAndNoFalseOldestClamp() {
        let marks = [10, 20, 40].map { EdgeMark(id: "\($0)", text: "Input", seq: $0) }
        #expect(EdgeScrubState.index(at: nil, marks: marks) == nil)
        #expect(EdgeScrubState.index(at: 9, marks: marks) == nil)
        #expect(EdgeScrubState.index(at: 10, marks: marks) == 0)
        #expect(EdgeScrubState.index(at: 19, marks: marks) == 0)
        #expect(EdgeScrubState.index(at: 20, marks: marks) == 1)
        #expect(EdgeScrubState.index(at: 39, marks: marks) == 1)
        #expect(EdgeScrubState.index(at: 80, marks: marks) == 2)
    }

    @Test func passiveVisibilityReconcilesImageReflowAndEarlierLoadedInputs() {
        let viewport = EdgeScrubViewport(), scrub = EdgeScrubState()
        var answer = item(11)
        answer.role = "assistant"
        answer.text = "First answer"
        var system = item(1)
        system.kind = .notice
        system.role = "system"
        let lines: [ChatLine] = [.row(.notice(system)), .row(.user(item(10))), .row(.assistant(answer)), .row(.user(item(20)))]
        viewport.rebuild(lines, history: "history-a")
        viewport.setIndex([EdgeMark(id: "20#0", text: "Second input", seq: 20)], history: "history-a")
        #expect(viewport.marks.map(\.seq) == [10, 20]) // loaded history before the remote index remains addressable
        viewport.setVisible(["1#0"])
        scrub.passive(viewport.marks, revision: viewport.revision, seq: viewport.currentSequence,
                      height: 600, top: 60, scrolling: true)
        #expect(scrub.isOpen && !scrub.isGestureActive && scrub.picked == nil)
        viewport.setVisible(["11#0", "20#0"])
        scrub.refreshPassive(viewport.marks, revision: viewport.revision, seq: viewport.currentSequence)
        #expect(scrub.pickedMark?.seq == 10) // the visible answer belongs to its preceding input, not the next one
        viewport.setVisible(["20#0"])
        scrub.refreshPassive(viewport.marks, revision: viewport.revision, seq: viewport.currentSequence)
        #expect(scrub.pickedMark?.seq == 20)
        var photo = item(10)
        photo.id = "10#1"
        photo.kind = .image
        photo.text = nil
        viewport.rebuild([.row(.notice(system)), .userImages(id: "img-10#1", items: [photo])] + Array(lines.dropFirst()), history: "history-a")
        viewport.setVisible(["11#0"])
        scrub.refreshPassive(viewport.marks, revision: viewport.revision, seq: viewport.currentSequence)
        #expect(scrub.pickedMark?.seq == 10 && scrub.pickedMark?.answer == "First answer")
        #expect(!scrub.move(to: 100)) // passive observation does not own a navigation gesture
    }

    @Test func stoppingScrollFadesPassiveButCannotInterruptAnEdgeGesture() async throws {
        let marks = (0..<40).map { EdgeMark(id: "\($0)", text: "Input", seq: $0) }
        let scrub = EdgeScrubState()
        scrub.passive(marks, revision: 1, seq: 20, height: 600, top: 60, scrolling: true)
        #expect(scrub.isOpen && !scrub.isGestureActive && scrub.picked == 20)
        scrub.stopPassive(after: .milliseconds(1))
        try await waitFor { !scrub.isOpen }
        scrub.passive(marks, revision: 1, seq: 20, height: 600, top: 60, scrolling: true)
        scrub.stopPassive(after: .milliseconds(1))
        scrub.open(marks, height: 600, top: 60, at: 200)
        let picked = scrub.pickedMark
        try await Task.sleep(for: .milliseconds(10))
        scrub.passive(marks, revision: 2, seq: 0, height: 600, top: 60, scrolling: true)
        scrub.stopPassive(after: .milliseconds(1))
        #expect(scrub.isGestureActive && scrub.isOpen && scrub.pickedMark == picked)
    }

    @Test func indexAnswerIsOptionalAndLocalAnswersStopAtInjectedBoundaries() async throws {
        let hub = NavigationTestHub(), model = SessionNavigationModel(), source = live()
        hub.routes[navigationPath] = { _ in
            Data(index(100..<140).replacingOccurrences(of: "\"Input 100\"", with: "\"Input 100\",\"answer\":\"Brief answer\"").utf8)
        }
        await model.activate(key, api: hub.client, supported: true, live: source)
        #expect(model.marks.first?.answer == "Brief answer" && model.marks.last?.answer == nil)
        var answer = item(11)
        answer.role = "assistant"
        answer.text = "Human input's answer"
        var injected = item(12)
        injected.injected = Injected(kind: "system", state: "supervise", line: "Synthetic supervision")
        var supervisionAnswer = item(13)
        supervisionAnswer.role = "assistant"
        supervisionAnswer.text = "Must not become the user's preview"
        let local = EdgeScrubState.marks([.row(.user(item(10))), .row(.assistant(answer)),
                                          .row(.user(injected)), .row(.assistant(supervisionAnswer))])
        #expect(local.count == 1 && local.first?.answer == "Human input's answer")
    }

    @Test func imageOnlyInputsAndTextWithImagesHaveOneFallbackMarkPerSequence() {
        var photo = item(1)
        photo.id = "1#1"
        photo.kind = .image
        photo.text = nil
        var photoOnly = photo
        photoOnly.id = "2#0"
        photoOnly.seq = 2
        let lines: [ChatLine] = [.userImages(id: "img-1#1", items: [photo]), .row(.user(item(1))),
                                 .userImages(id: "img-2#0", items: [photoOnly])]
        let marks = EdgeScrubState.marks(lines)
        #expect(marks.map(\.id) == ["1#0", "img-2#0"])
        #expect(marks.first?.text == "Input 1")
        #expect(marks.last?.text == String(localized: "Photo [Chat Layout]"))
        #expect(lines.last?.holds("2#0") == true)
    }
}
