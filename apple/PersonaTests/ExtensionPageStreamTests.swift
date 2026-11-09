import Foundation
import Testing
@testable import Persona

@MainActor @Suite(.serialized) struct ExtensionPageStreamTests {
    final class Socket: LocalSocket {
        var deliver: ((Data) -> Void)?
        var sent: [ExtensionStreamJSON] = []
        var supports = true
        func open(deliver: @escaping (Data) -> Void) {
            self.deliver = deliver
            emit(#"{"type":"hello","features":\#(supports ? "[\"extension_ui_stream_v1\"]" : "[]")}"#)
        }
        func receive(_ message: [String: Any]) {
            guard let data = try? JSONSerialization.data(withJSONObject: message), let raw = try? ExtensionStreamJSON.parse(data) else { return }
            sent.append(raw)
        }
        func close() { deliver = nil }
        func emit(_ string: String) { deliver?(Data(string.utf8)) }
        var request: ExtensionStreamJSON? { sent.last { $0["type"]?.string == "extension_ui_stream" }?["request"] }
        func snapshot(version: Int64 = 1, epoch: String = "epoch", title: String = "Initial") {
            let id = request?["request_id"]?.string ?? ""
            emit(#"{"type":"extension_ui_stream","kind":"snapshot","manager":"owner","request_id":"\#(id)","snapshot":{"protocol":1,"session":"session","epoch":"\#(epoch)","version":\#(version),"root_key":"page","view":{"title":"\#(title)","sections":[{"key":"main","items":[{"type":"button","key":"button","title":"Use","action":{"id":"callback","confirm":"Confirm saved action"}}]}]}}}"#)
        }
        func reviewSnapshot(version: Int64 = 1, present: Bool = true) {
            var view: [String: Any] = ["title": "Review", "sections": []]
            if present {
                view["review"] = ["count": 2, "name": "summary", "buttons": [
                    ["title": "Approve", "action": ["id": "review-submit", "values": ["event": "approve"]]]
                ], "discard": ["id": "review-discard", "confirm": "Discard saved drafts?"]]
            }
            let frame: [String: Any] = ["type": "extension_ui_stream", "kind": "snapshot", "manager": "owner",
                "request_id": request?["request_id"]?.string ?? "", "snapshot": [
                    "protocol": 1, "session": "session", "epoch": "epoch", "version": version,
                    "root_key": "page", "view": view]]
            if let data = try? JSONSerialization.data(withJSONObject: frame) { deliver?(data) }
        }
        func delta(version: Int64 = 2, base: Int64 = 1, epoch: String = "epoch") {
            emit(#"{"type":"extension_ui_stream","kind":"delta","manager":"owner","delta":{"protocol":1,"session":"session","epoch":"\#(epoch)","base_version":\#(base),"version":\#(version),"operations":[]}}"#)
        }
        func receipt(_ event: String, version: Int64 = 2, request: String = "retry", result: String = #"{"toast":"Once"}"#) {
            emit(#"{"type":"extension_ui_stream","kind":"receipt","manager":"owner","request_id":"\#(request)","receipt":{"session":"session","epoch":"epoch","event_id":"\#(event)","version":\#(version),"status":"accepted","result":\#(result)}}"#)
        }
    }

    private func setup(_ socket: Socket, context: ExtensionUIContext = .here()) -> (LiveConnection, ExtensionPageStream) {
        LiveConnection.makeLocal = { socket }
        let connection = LiveConnection(); connection.usesExtensionUIStream = true; connection.connect(.demo)
        let page = ExtensionPageStream(connection: connection, owner: "example", contribution: "page", context: context, state: "")
        page.setVisible(true)
        return (connection, page)
    }
    private func savedAction(_ page: ExtensionPageStream) throws -> ExtensionAction {
        let view = try #require(page.tree).nativeView
        guard case .button(let button) = view.sections[0].items[0] else { throw ExtensionStreamJSON.Failure.malformed }
        return button.action
    }

    @Test func opensWithExplicitStateAndHonestCapabilities() throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket()
        let context = ExtensionUIContext.here(launcher: "test-launcher", machine: "test-computer", item: "test-item", size: "full")
        let (connection, page) = setup(socket, context: context); defer { page.close(); connection.disconnect() }
        #expect(socket.request?["request"]?.string == "open")
        #expect(socket.request?["state"] == .string(""))
        #expect(ExtensionPageStream.uiVersion == 6)
        #expect(socket.request?["context"]?["ui_version"] == .number("6"))
        #expect(context.uiVersion == 6 && ExtensionUIContext.here().uiVersion == 6)
        #expect(socket.request?["context"]?["launcher"] == .string("test-launcher"))
        #expect(socket.request?["context"]?["machine"] == .string("test-computer"))
        #expect(socket.request?["context"]?["item"] == .string("test-item"))
        #expect(socket.request?["context"]?["size"] == .string("full"))
        let locale = try #require(context.locale)
        let tz = try #require(context.tz)
        #expect(socket.request?["context"]?["locale"] == .string(locale))
        #expect(socket.request?["context"]?["tz"] == .string(tz))
        #expect(socket.request?["context"]?["stream"]?["components"]?.array?.contains(.string("html")) == false)
        socket.snapshot()
        #expect(page.ready && page.tree?.version == 1)
    }

    @Test func reviewSummarySurvivesUpdatesAndFreezesWithItsOwnActionValues() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); LiveConnection.makeLocal = { socket }
        let connection = LiveConnection(); connection.usesExtensionUIStream = true; connection.connect(.demo)
        let model = ExtensionViewModel(plugin: "example", ui: "page", context: .here().forPage(),
            deps: .init(api: { nil }, connection: { connection }))
        defer { model.cancelConfirmation(); model.stream?.close(); connection.disconnect() }
        model.setVisible(true); socket.reviewSnapshot()
        let stream = try #require(model.stream)
        let review = try #require(model.view?.review)
        #expect(model.view?.wantsCodePage == true && review.count == 2)
        model.values[review.name] = "Original summary"
        socket.delta()
        #expect(model.values[review.name] == "Original summary")
        let execute = model.prepared(review.buttons[0].action,
            with: [review.name: model.values[review.name] ?? ""], displayed: stream.displayedRevision)
        model.values[review.name] = "Edited after tap"
        let task = Task { await execute() }
        for _ in 0..<10 where socket.request?["request"]?.string != "event" { await Task.yield() }
        let event = try #require(socket.request?["interaction"]?["event_id"]?.string)
        #expect(socket.request?["interaction"]?["value"]?[review.name] == .string("Original summary"))
        #expect(socket.request?["interaction"]?["value"]?["event"] == .string("approve"))
        #expect(socket.request?["interaction"]?["event"] == .string("submit"))
        socket.receipt(event, version: 3)
        socket.delta(version: 3, base: 2)
        #expect(await task.value.toast == "Once")
        #expect(model.values[review.name] == "Edited after tap")
        let discard = try #require(review.discard)
        let cancel = model.prepared(discard, displayed: stream.displayedRevision)
        let cancelling = Task { await cancel() }
        for _ in 0..<10 where model.confirmation == nil { await Task.yield() }
        #expect(model.confirmation?.chosen.isEmpty == true)
        model.cancelConfirmation()
        #expect(await cancelling.value.cancelled)
        #expect(socket.sent.filter { $0["request"]?["request"]?.string == "event" }.count == 1)
        let approving = model.prepared(discard, displayed: stream.displayedRevision)
        let approved = Task { await approving() }
        for _ in 0..<10 where model.confirmation == nil { await Task.yield() }
        model.approveConfirmation()
        for _ in 0..<10 where socket.request?["interaction"]?["action"] != .string(discard.id) { await Task.yield() }
        #expect(socket.request?["interaction"]?["event"] == .string("press"))
        #expect(socket.request?["interaction"]?["value"]?.object?.isEmpty == true)
        let discardEvent = try #require(socket.request?["interaction"]?["event_id"]?.string)
        socket.receipt(discardEvent, version: 4)
        socket.delta(version: 4, base: 3)
        #expect(await approved.value.toast == "Once")
        #expect(socket.sent.filter { $0["request"]?["request"]?.string == "event" }.count == 2)
        stream.refresh(); socket.reviewSnapshot(version: 5, present: false)
        #expect(model.view?.review == nil && model.values[review.name] == nil)
        #expect(model.view?.wantsCodePage == false)
    }

    @Test func receiptWaitsForEmptyFenceAndRetryDoesNotRepeatEffect() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot()
        let frozen = try page.freeze(savedAction(page), values: ["draft": "saved"])
        var result: ExtensionUIStreamActionResult?
        let task = Task { result = try await page.perform(frozen) }
        await Task.yield()
        #expect(socket.request?["interaction"]?["action"] == .string("callback"))
        #expect(socket.request?["interaction"]?["value"]?["draft"] == .string("saved"))
        socket.receipt(frozen.interaction.eventId)
        #expect(result == nil && page.tree?.version == 1 && page.ready)
        socket.delta()
        try await task.value
        #expect(result?.toast == "Once" && page.tree?.version == 2)
        socket.receipt(frozen.interaction.eventId, request: "another-correlation")
        #expect(page.tree?.version == 2 && result?.toast == "Once")
        #expect(socket.sent.filter { $0["request"]?["request"]?.string == "event" }.count == 1)
    }

    @Test func invalidInitialTreeReleasesItsAllocatedSessionBeforeReopening() {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        let request = socket.request?["request_id"]?.string ?? ""
        socket.emit(#"{"type":"extension_ui_stream","kind":"snapshot","manager":"owner","request_id":"\#(request)","snapshot":{"protocol":1,"session":"session","epoch":"epoch","version":1,"root_key":"page","view":{"sections":[{"key":"duplicate","items":[{"type":"text","key":"duplicate","text":"Synthetic invalid topology"}]}]}}}"#)
        #expect(page.tree == nil && !page.ready)
        let closes = socket.sent.filter { $0["request"]?["request"]?.string == "close" }
        #expect(closes.count == 1 && closes.first?["request"]?["session"] == .string("session"))
        #expect(socket.request?["request"]?.string == "open")
    }

    @Test func transientRebuildRetriesOnlyASnapshotAndRetainsNativeMode() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot()
        socket.emit(#"{"type":"extension_ui_stream","kind":"closed","manager":"owner","session":"session"}"#)
        let opening = try #require(socket.request?["request_id"]?.string)
        socket.emit(#"{"type":"extension_ui_stream","kind":"error","manager":"owner","request_id":"\#(opening)","error":{"code":"offline","message":""}}"#)
        let requests = socket.sent.count
        try await Task.sleep(for: .milliseconds(1_100))
        #expect(page.hasMountedNative && page.supported == true && !page.ready)
        #expect(socket.sent.count == requests + 1 && socket.request?["request"]?.string == "open")
        #expect(socket.request?["request_id"]?.string != opening)
        socket.snapshot(epoch: "replacement")
        #expect(page.ready && page.tree?.epoch == "replacement")
        let eventRequests = socket.sent.filter { $0["request"]?["request"]?.string == "event" }
        #expect(eventRequests.isEmpty)
    }

    @Test func hiddenPendingOpenRetiresEvenWhenReplyOmitsOptionalManager() {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); LiveConnection.makeLocal = { socket }
        let connection = LiveConnection(); connection.usesExtensionUIStream = true; connection.connect(.demo)
        let page = ExtensionPageStream(connection: connection, owner: "example", contribution: "page", context: .here(), state: "", manager: "owner")
        defer { page.close(); connection.disconnect() }
        page.setVisible(true)
        let request = socket.request?["request_id"]?.string ?? ""
        page.setVisible(false)
        socket.emit(#"{"type":"extension_ui_stream","kind":"snapshot","request_id":"\#(request)","snapshot":{"protocol":1,"session":"late","epoch":"epoch","version":1,"root_key":"page","view":{"sections":[]}}}"#)
        #expect(page.tree == nil && !page.ready)
        #expect(socket.request?["request"]?.string == "close" && socket.request?["session"] == .string("late"))
        #expect(socket.sent.last?["manager"] == .string("owner"))
    }

    @Test func eventCorrelatedFullSnapshotCommitsBeforeItsReceipt() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot()
        let frozen = try page.freeze(savedAction(page), values: ["range": "1d"])
        let task = Task { try await page.perform(frozen) }
        await Task.yield()
        let eventRequest = try #require(socket.request?["request_id"]?.string)
        // The real stateful showcase emits a full snapshot carrying the event's request_id.
        socket.snapshot(version: 2, title: "Stateful callback")
        #expect(page.ready && page.tree?.version == 2 && page.tree?.view["title"] == .string("Stateful callback"))
        socket.receipt(frozen.interaction.eventId, request: eventRequest)
        #expect(try await task.value?.toast == "Once")
        page.setVisible(false)
        socket.emit(#"{"type":"extension_ui_stream","kind":"snapshot","manager":"owner","request_id":"\#(eventRequest)","snapshot":{"protocol":1,"session":"session","epoch":"epoch","version":3,"root_key":"page","view":{"sections":[]}}}"#)
        #expect(page.tree?.version == 2 && !page.ready)
    }

    @Test func frozenActionCannotBorrowNewerVersionAndResumeIsSnapshotFirst() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot(); let frozen = try page.freeze(savedAction(page), values: [:])
        socket.delta()
        do { _ = try await page.perform(frozen); Issue.record("A stale action was submitted") } catch {}
        let eventRequests = socket.sent.filter { $0["request"]?["request"]?.string == "event" }
        #expect(eventRequests.isEmpty)
        page.setVisible(false); #expect(socket.request?["request"]?.string == "suspend")
        page.setVisible(true); #expect(socket.request?["request"]?.string == "resume" && !page.ready)
        socket.delta(version: 3, base: 2); #expect(page.tree?.version == 2 && !page.ready)
        socket.snapshot(version: 3); #expect(page.tree?.version == 3 && page.ready)
    }

    @Test func malformedAtomicBatchAndEpochMismatchRecoverSnapshot() throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot(); let before = page.tree
        socket.emit(#"{"type":"extension_ui_stream","kind":"delta","manager":"owner","delta":{"protocol":1,"session":"session","epoch":"epoch","base_version":1,"version":2,"operations":[{"op":"remove","key":"button"},{"op":"remove","key":"missing"}]}}"#)
        #expect(page.tree == before && !page.ready && socket.request?["request"]?.string == "snapshot")
        socket.snapshot(version: 2); #expect(page.ready)
        socket.delta(version: 3, base: 2, epoch: "new-epoch")
        #expect(!page.ready && socket.request?["request"]?.string == "snapshot")
        socket.snapshot(version: 3, epoch: "new-epoch")
        #expect(page.ready && page.tree?.epoch == "new-epoch")
    }

    @Test func otherManagerAndSessionCannotChangePage() throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot(); let before = page.tree; let count = socket.sent.count
        socket.emit(#"{"type":"extension_ui_stream","kind":"delta","manager":"foreign","delta":{"protocol":1,"session":"session","epoch":"epoch","base_version":1,"version":2,"operations":[]}}"#)
        socket.emit(#"{"type":"extension_ui_stream","kind":"closed","manager":"owner","session":"foreign"}"#)
        socket.emit(#"{"type":"extension_ui_stream","kind":"snapshot","manager":"owner","snapshot":{"protocol":1,"session":"foreign","epoch":"epoch","version":1,"root_key":"page","view":{"sections":[]}}}"#)
        socket.emit(#"{"type":"extension_ui_stream","kind":"error","manager":"owner","error":{"code":"rejected","message":""}}"#)
        #expect(page.tree == before && socket.sent.count == count && page.ready)
    }

    @Test func rejectsAnOldDisplayedRevisionBeforeScheduling() throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot()
        let displayed = try #require(page.displayedRevision)
        let action = try savedAction(page)
        socket.delta()
        #expect(throws: (any Error).self) { try page.freeze(action, values: [:], displayed: displayed) }
    }

    @Test func anInitialLegacyProcessRejectionKeepsHTTPAvailable() {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        var legacy = false; page.onLegacy = { legacy = true }
        let request = socket.request?["request_id"]?.string ?? ""
        socket.emit(#"{"type":"extension_ui_stream","kind":"error","manager":"owner","request_id":"\#(request)","error":{"code":"rejected","message":""}}"#)
        #expect(legacy && page.supported == false && page.tree == nil)
    }

    @Test func modelConfirmationCancelsAndRejectsStaleConsent() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); LiveConnection.makeLocal = { socket }
        let connection = LiveConnection(); connection.usesExtensionUIStream = true; connection.connect(.demo)
        let model = ExtensionViewModel(plugin: "example", ui: "page", context: .here().forPage(),
            deps: .init(api: { nil }, connection: { connection }))
        defer { model.cancelConfirmation(); model.stream?.close(); connection.disconnect() }
        model.setVisible(true); socket.snapshot()
        let stream = try #require(model.stream)
        let action = try savedAction(stream)
        let cancel = model.prepared(action, with: ["draft": "original"], displayed: stream.displayedRevision)
        let cancelled = Task { await cancel() }
        for _ in 0..<10 where model.confirmation == nil { await Task.yield() }
        #expect(model.confirmation?.chosen["draft"] == "original")
        model.cancelConfirmation()
        #expect(await cancelled.value == .init(cancelled: true))
        #expect(model.running == nil)
        let stale = model.prepared(action, displayed: stream.displayedRevision)
        let staleResult = Task { await stale() }
        for _ in 0..<10 where model.confirmation == nil { await Task.yield() }
        socket.delta()
        model.approveConfirmation()
        let rejected = await staleResult.value
        #expect(rejected.error != nil)
        let eventRequests = socket.sent.filter { $0["request"]?["request"]?.string == "event" }
        #expect(eventRequests.isEmpty)
    }

    @Test func modelApprovalUsesSavedValuesAndReceiptFence() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); LiveConnection.makeLocal = { socket }
        let connection = LiveConnection(); connection.usesExtensionUIStream = true; connection.connect(.demo)
        let model = ExtensionViewModel(plugin: "example", ui: "page", context: .here().forPage(),
            deps: .init(api: { nil }, connection: { connection }))
        defer { model.cancelConfirmation(); model.stream?.close(); connection.disconnect() }
        model.setVisible(true); socket.snapshot()
        let stream = try #require(model.stream)
        let action = try savedAction(stream)
        let execute = model.prepared(action, with: ["draft": "saved"], displayed: stream.displayedRevision)
        let task = Task { await execute() }
        for _ in 0..<10 where model.confirmation == nil { await Task.yield() }
        let frozen = try #require(model.confirmation)
        model.values["draft"] = "changed after consent"
        model.approveConfirmation()
        for _ in 0..<10 where socket.request?["request"]?.string != "event" { await Task.yield() }
        #expect(socket.request?["interaction"]?["value"]?["draft"] == .string("saved"))
        #expect(socket.request?["interaction"]?["event_id"] == .string(frozen.interaction.eventId))
        socket.receipt(frozen.interaction.eventId)
        #expect(stream.tree?.version == 1 && model.running != nil)
        socket.delta()
        let result = await task.value
        #expect(result.toast == "Once" && result.error == nil && stream.tree?.version == 2)
        #expect(model.running == nil)
    }

    @Test func closedNativeSessionCannotTurnIntoAnHTTPWriteFallback() {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        var legacy = false; page.onLegacy = { legacy = true }
        socket.snapshot()
        socket.emit(#"{"type":"extension_ui_stream","kind":"closed","manager":"owner","session":"session"}"#)
        let request = socket.request?["request_id"]?.string ?? ""
        socket.emit(#"{"type":"extension_ui_stream","kind":"error","manager":"owner","request_id":"\#(request)","error":{"code":"rejected","message":""}}"#)
        #expect(page.hasMountedNative && page.supported == true && !page.ready && !legacy)
        socket.emit(#"{"type":"hello","features":[]}"#)
        #expect(page.supported == true && !legacy && !page.ready)
    }

    @Test func hostGateRequiresExplicitConsentAndExactSavedAction() async throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot()
        var resolved: [String] = []
        page.resolveHostConfirmation = { resolved.append($0) }
        func host(_ id: String, session: String = "session", confirm: String = "Confirm saved action") {
            socket.emit(#"{"type":"interaction","interaction":{"id":"\#(id)","machine_id":"","session_id":"\#(session)","kind":"permission","tool_name":"extension.ui","tool_input":{"id":"callback","confirm":"\#(confirm)"},"summary":"\#(confirm)"}}"#)
        }
        let unconfirmed = try page.freeze(savedAction(page), values: [:])
        let noConsent = Task { try? await page.perform(unconfirmed) }
        await Task.yield(); host("no-consent")
        #expect(resolved.isEmpty)
        page.setVisible(false); _ = await noConsent.value
        page.setVisible(true); socket.snapshot()
        let confirmed = try page.freeze(savedAction(page), values: [:])
        let consent = Task { try await page.perform(confirmed, confirmed: true) }
        await Task.yield()
        host("wrong-session", session: "foreign")
        host("changed-consent", confirm: "Different consent")
        #expect(resolved.isEmpty)
        host("saved-consent"); host("saved-consent")
        for _ in 0..<10 where resolved.isEmpty { await Task.yield() }
        #expect(resolved == ["saved-consent"])
        socket.delta(); socket.receipt(confirmed.interaction.eventId)
        #expect(try await consent.value?.toast == "Once")
    }

    @Test func stackSectionIdentitySurvivesKeyedReorder() {
        let a = ExtensionSection(key: "a")
        let b = ExtensionSection(key: "b")
        let before = [a, b].enumerated().map { ExtensionViewStack.SectionEntry(index: $0.offset, section: $0.element).id }
        let after = [b, a].enumerated().map { ExtensionViewStack.SectionEntry(index: $0.offset, section: $0.element).id }
        #expect(before == Array(after.reversed()))
    }

    @Test func olderSnapshotCannotRegressTheCommittedRevision() throws {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        socket.snapshot(); socket.delta()
        page.refresh()
        socket.snapshot(version: 1, title: "Late stale snapshot")
        #expect(page.tree?.version == 2 && page.tree?.view["title"] == .string("Initial") && !page.ready)
        #expect(socket.request?["request"]?.string == "snapshot")
        socket.snapshot(version: 3)
        #expect(page.ready && page.tree?.version == 3)
    }

    @Test func oldServerDoesNotOpenStreamSession() {
        let previous = LiveConnection.makeLocal; defer { LiveConnection.makeLocal = previous }
        let socket = Socket(); socket.supports = false
        let (connection, page) = setup(socket); defer { page.close(); connection.disconnect() }
        #expect(page.supported == false && socket.request == nil)
    }
}
