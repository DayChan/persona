import Foundation
import Testing
@testable import Persona

/// UI version 3 (docs/extensions/extensions.md「UI 版本 3」, #1850): the schema's types read the new components, push opens a page
/// without asking the extension, answers push and pop, the search, filter and `more` send what they should, and
/// `ui.invalidate` reaches the models drawing that extension.
@MainActor
struct ExtensionUIv3Tests {
    private static let examples = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/examples")

    private static func example(_ name: String) throws -> String {
        try String(contentsOf: examples.appending(path: name), encoding: .utf8)
    }

    private func page(_ hub: FakeHub, state: String? = nil, depth: Int = 1) -> ExtensionViewModel {
        ExtensionViewModel(plugin: "notes", ui: "list", state: state, depth: depth, deps: .init(api: { hub.client }))
    }

    private func bodies(_ hub: FakeHub) throws -> [JSONValue] {
        try hub.sent("POST", "/api/v1/plugins/notes/-/ui/list").map { try JSON.decoder.decode(JSONValue.self, from: $0.body ?? Data()) }
    }

    @Test func theNewComponentsAreRead() throws {
        let v = try JSONDecoder().decode(ExtensionView.self, from: Data(try Self.example("View.components-v3.json").utf8))
        let items = v.sections.flatMap(\.items)
        #expect(!items.contains { if case .unknown = $0 { true } else { false } }, "every component of the example is known")
        #expect(v.search?.action.id == "search" && v.filter?.choices.map(\.label) == ["全部", "工作", "生活"])
        #expect(v.sections[0].more?.action.id == "more" && v.sections[0].items.map(\.key) == ["n1", "n2", "n3"])
        let rows = v.sections[0].items.compactMap(\.row)
        #expect(rows[0].icon == "doc" && rows[1].avatar?.text == "A" && rows[2].thumbnail?.aspect == ExtensionThumbnailAspect.wide)
        #expect(ExtensionUIStyle.icon("doc") == Theme.Icon.pluginDoc && ExtensionUIStyle.icon("hologram") == Theme.Icon.plugins)
        #expect(ExtensionUIStyle.icon("plugin") == Theme.Icon.plugins && ExtensionUIStyle.icon("extension") == Theme.Icon.plugins)
    }

    @Test func aPushOpensItsPageWithoutAskingTheExtension() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/notes/-/ui/list", json: #"{"view":\#(try Self.example("View.components-v3.json"))}"#)
        let m = page(hub)
        await m.load()
        let open = try #require(m.view?.sections[0].items[0].action)
        let before = hub.requests.count
        #expect(await m.perform(open) == .init())
        #expect(hub.requests.count == before, "a push is not sent")
        #expect(m.pushed?.state == #"{"note":"n1"}"# && m.pushed?.title == "周会纪要" && m.pushed?.ui == nil)

        // The page it pushes starts with that state, one level deeper; past eight nothing is pushed.
        let deep = page(hub, depth: ExtensionViewModel.maxDepth)
        await deep.load()
        _ = await deep.perform(open)
        #expect(deep.pushed == nil)
    }

    @Test func aPushKeepsItsHubAndSourceContext() {
        var client = FakeHub().client
        client.group = "group-b"
        let m = ExtensionViewModel(plugin: "notes", ui: "card",
                                   context: .here(launcher: "launcher", machine: "machine", item: "item", size: "compact"),
                                   deps: .init(api: { client }))
        let t = ExtensionPushTarget(model: m, push: ExtensionPush(ui: "page", state: "note", title: "详情"))
        #expect(t.group == "group-b" && t.ui == "page" && t.state == "note" && t.depth == 2)
        #expect(t.context.launcher == "launcher" && t.context.machine == "machine" && t.context.item == "item")
        #expect(t.context.size == nil, "卡片推出来的是整页，不再用 compact 尺寸")
    }

    @Test func aPushedPageStartsWithItsState() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/notes/-/ui/list", json: #"{"view":{"sections":[],"state":"drawn"}}"#)
        let m = page(hub, state: #"{"note":"n1"}"#, depth: 2)
        await m.load()
        #expect(try bodies(hub).first?["state"] == .string(#"{"note":"n1"}"#))
        await m.refresh()
        #expect(try bodies(hub).last?["state"] == .string("drawn"), "then the View's own")
    }

    @Test func anAnswerPushesAndPops() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/notes/-/ui/list") { req in
            let b = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            if b["action"] == .string("save") { return Data(try Self.example("ActionResult.push-pop.json").utf8) }
            return Data(#"{"view":{"sections":[]}}"#.utf8)
        }
        let m = page(hub, depth: 2)
        await m.load()
        let o = await m.perform(ExtensionAction(id: "save", submit: true))
        #expect(o.toast == "已保存" && m.popped && m.pushed?.ui == "notes" && m.pushed?.title == "周会纪要")
    }

    @Test func searchFilterAndMoreSendWhatTheyShould() async throws {
        let hub = FakeHub()
        let view = try Self.example("View.components-v3.json")
        hub.on("POST", "/api/v1/plugins/notes/-/ui/list", json: #"{"view":\#(view)}"#)
        let m = page(hub)
        await m.load()
        #expect(m.values["folder"] == "all" && m.query == "")

        m.query = "周会"
        _ = await m.search()
        #expect(try bodies(hub).last?["action"] == .string("search"))
        #expect(try bodies(hub).last?["values"] == .object(["q": .string("周会"), "folder": .string("all")]))

        _ = await m.filter("work")
        #expect(try bodies(hub).last?["action"] == .string("folder"))
        #expect(try bodies(hub).last?["values"] == .object(["folder": .string("work"), "q": .string("周会")]))
        let sent = hub.requests.count
        _ = await m.filter(m.values["folder"] ?? "")
        #expect(hub.requests.count == sent, "the one picked already: nothing sent")

        _ = await m.more(try #require(m.view?.sections[0].more))
        #expect(try bodies(hub).last?["action"] == .string("more") && m.loadingMore == nil)
    }

    @Test func anOlderSearchsAnswerIsNotShown() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/notes/-/ui/list", json: #"{"view":{"search":{"action":{"id":"s"}},"sections":[]}}"#)
        var client = hub.client
        let m = ExtensionViewModel(plugin: "notes", ui: "list", deps: .init(api: { client }))
        await m.load()
        let a = ResponseGate(), ab = ResponseGate()
        client.transport = { req in
            let body = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return await (body["values"]?["q"] == .string("a") ? a : ab).answer()
        }
        m.query = "a"
        let first = Task { await m.search() }
        await a.waitUntilRequested()
        m.query = "ab"
        let second = Task { await m.search() }
        await ab.waitUntilRequested()
        await ab.finish(Data(#"{"view":{"title":"ab","search":{"action":{"id":"s"}},"sections":[]}}"#.utf8))
        _ = await second.value
        await a.finish(Data(#"{"view":{"title":"a","search":{"action":{"id":"s"}},"sections":[]}}"#.utf8))
        _ = await first.value
        #expect(m.view?.title == "ab", "旧请求后返回，也不能覆盖新搜索")
    }

    @Test func refreshConsumesInvalidationOnlyAfterTheResponse() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/notes/-/ui/list", json: #"{"view":{"title":"before","sections":[]}}"#)
        var client = hub.client
        let m = ExtensionViewModel(plugin: "notes", ui: "list", deps: .init(api: { client }))
        await m.load()
        let gate = ResponseGate()
        client.transport = { _ in await gate.answer() }
        m.invalidate()
        let refresh = Task { await m.refresh() }
        await gate.waitUntilRequested()
        #expect(m.stale, "请求进行中不能清掉任务的失效标记")
        m.invalidate()
        await gate.finish(Data(#"{"view":{"title":"after","sections":[]}}"#.utf8))
        await refresh.value
        #expect(m.stale, "请求期间到达的新事件仍需刷新")
        await m.refresh()
        #expect(!m.stale && m.view?.title == "after")
    }

    @Test func aCancelledRefreshLeavesInvalidationPending() async throws {
        let gate = ResponseGate()
        var client = FakeHub().client
        client.transport = { _ in await gate.answer() }
        let m = ExtensionViewModel(plugin: "notes", ui: "list", deps: .init(api: { client }))
        m.invalidate()
        let refresh = Task { await m.refresh() }
        await gate.waitUntilRequested()
        refresh.cancel()
        await gate.finish(Data(#"{"view":{"sections":[]}}"#.utf8))
        await refresh.value
        #expect(m.stale && m.view == nil && !m.loading)
        await m.refresh()
        #expect(!m.stale && m.view != nil)
    }

    /// What an action or a `more` answers is drawn with it done: drawn while it still ran, the new view's controls stayed
    /// disabled after it ended (#1937). An answer with nothing in it draws the page again, asked while it still runs.
    @Test func anAnswerIsDrawnWithItsControlDone() async throws {
        let hub = FakeHub()
        let seen = DrawnWhile()
        hub.on("POST", "/api/v1/plugins/notes/-/ui/list") { req in
            let b = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            switch b["action"] {
            case .string("install"):
                return Data(#"{"toast":"Installed","view":{"sections":[{"items":[{"type":"button","key":"uninstall","title":"Uninstall","action":{"id":"uninstall","confirm":"Uninstall?"}}]}]}}"#.utf8)
            case .string("touch"): return Data("{}".utf8)
            case .string("more"): return Data(#"{"view":{"sections":[{"items":[{"type":"text","key":"n2","text":"2"}],"more":{"action":{"id":"more"}}}]}}"#.utf8)
            default:
                if seen.model?.running != nil { seen.askedWhileRunning = true }
                return Data(#"{"view":{"sections":[{"items":[{"type":"text","key":"n1","text":"1"}],"more":{"action":{"id":"more"}}}]}}"#.utf8)
            }
        }
        let m = page(hub)
        seen.model = m
        await m.load()

        seen.watch(m)
        let installed = await m.perform(ExtensionAction(id: "install"))
        let uninstall = try #require(m.view?.sections.flatMap(\.items).compactMap { if case .button(let b) = $0 { b.action } else { nil } }.first)
        #expect(installed.toast == "Installed" && seen.drawn == 1 && seen.busy == [nil])
        #expect(m.isEnabled(uninstall), "the new view's button can be used")

        seen.watch(m)
        _ = await m.perform(ExtensionAction(id: "touch"))
        #expect(seen.askedWhileRunning && seen.drawn == 2 && seen.busy == [nil, nil])

        seen.watch(m)
        _ = await m.more(try #require(m.view?.sections[0].more))
        #expect(seen.drawn == 3 && seen.busy == [nil, nil, nil] && m.loadingMore == nil)
    }

    /// Whether a control was still busy each time the page's view was replaced.
    @MainActor private final class DrawnWhile {
        var busy: [String?] = []
        var drawn: Int { busy.count }
        var askedWhileRunning = false
        weak var model: ExtensionViewModel?

        func watch(_ m: ExtensionViewModel) {
            withObservationTracking { _ = m.view } onChange: {
                MainActor.assumeIsolated { self.busy.append(m.running ?? m.loadingMore) }
            }
        }
    }

    @Test func invalidationWhileAnActionRunsRetriesWhenItFinishes() async throws {
        let gate = ResponseGate()
        var client = FakeHub().client
        client.transport = { _ in await gate.answer() }
        let m = ExtensionViewModel(plugin: "notes", ui: "list", deps: .init(api: { client }))
        let action = Task { await m.perform(ExtensionAction(id: "save")) }
        await gate.waitUntilRequested()
        m.invalidate()
        let completed = m.completedOperations
        await m.refresh()
        #expect(m.stale && m.completedOperations == completed)
        await gate.finish(Data(#"{"view":{"sections":[]}}"#.utf8))
        _ = await action.value
        #expect(m.stale && m.completedOperations > completed && m.running == nil)
        await m.refresh()
        #expect(!m.stale)
    }

    @Test func invalidationStaysInItsHubGroup() {
        let hub = FakeHub()
        var a = hub.client, b = hub.client
        a.group = "group-a"
        b.group = "group-b"
        let mine = ExtensionViewModel(plugin: "group-test", ui: "list", deps: .init(api: { a }))
        let other = ExtensionViewModel(plugin: "group-test", ui: "list", deps: .init(api: { b }))
        ExtensionUIRegistry.shared.invalidate(extension: "group-test", ui: nil, group: "group-a")
        #expect(mine.stale && !other.stale)
    }

    private actor ResponseGate {
        private var result: Data?
        private var requested = false
        private var response: CheckedContinuation<Data, Never>?
        private var started: CheckedContinuation<Void, Never>?

        func answer() async -> Data {
            requested = true
            started?.resume()
            started = nil
            if let result { return result }
            return await withCheckedContinuation { response = $0 }
        }

        func waitUntilRequested() async {
            if requested { return }
            await withCheckedContinuation { started = $0 }
        }

        func finish(_ data: Data) {
            result = data
            response?.resume(returning: data)
            response = nil
        }
    }

    @Test func invalidateReachesTheModelsOfThatExtension() async throws {
        let hub = FakeHub()
        let mine = ExtensionViewModel(plugin: "inv-test", ui: "a", deps: .init(api: { hub.client }))
        let other = ExtensionViewModel(plugin: "inv-test", ui: "b", deps: .init(api: { hub.client }))
        let elsewhere = ExtensionViewModel(plugin: "inv-other", ui: "a", deps: .init(api: { hub.client }))
        ExtensionUIRegistry.shared.invalidate(extension: "inv-test", ui: "a")
        #expect(mine.stale && !other.stale && !elsewhere.stale)
        ExtensionUIRegistry.shared.invalidate(extension: "inv-test", ui: nil)
        #expect(other.stale && !elsewhere.stale)

        let e = ServerEvent.decode(Data(#"{"type":"extension_ui","extension":"inv-test","id":"a","invalidate":true}"#.utf8))
        guard case .extensionInvalidate(let ext, let id)? = e else { Issue.record("not decoded"); return }
        #expect(ext == "inv-test" && id == "a")
        let all = ServerEvent.decode(Data(#"{"type":"extension_ui","extension":"inv-test","invalidate":true}"#.utf8))
        guard case .extensionInvalidate(_, nil)? = all else { Issue.record("not decoded"); return }
    }
}
