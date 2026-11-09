import Foundation
import Testing
@testable import Persona

/// The plugins' UI slots (docs/extensions/extensions.md「UI 插槽」): what the hub lists and the app keeps (entries, cards, badges),
/// the new items (chart, stat_grid), refresh_after, and the plugin_ui event.
@MainActor struct ExtensionSlotsModelTests {
    private static let all = #"""
    {"ui":[{"plugin":"example-gateway","plugin_name":"Example 网关面板","id":"usage","title":"用量","placement":"nav_page","size":"full",
            "icon":"chart","badge":{"text":"3","tone":"danger"}},
           {"plugin":"example-gateway","id":"alerts","title":"提醒","placement":"nav_page","icon":"sparkles"},
           {"plugin":"other","id":"future","title":"以后的位置","placement":"toolbar_action"},
           {"plugin":"example-gateway","id":"home","title":"概况","placement":"plugin_page"},
           {"plugin":"example-gateway","id":"today","title":"今天的用量","placement":"overview_card","size":"compact","opens":"usage"},
           {"plugin":"example-gateway","id":"plain","title":"不能点的卡片","placement":"overview_card","size":"compact"}]}
    """#

    private func slots(_ hub: FakeHub, features: Set<String> = ["plugin_ui_slots"]) -> ExtensionSlots {
        hub.on("GET", "/api/v1/plugins/-/ui") { req in
            #expect(req.query["placement"] == nil, "one list of every slot")
            return Data(Self.all.utf8)
        }
        return ExtensionSlots(deps: .init(api: { hub.client }), features: { features })
    }

    @Test func entriesCardsAndBadges() async throws {
        let hub = FakeHub()
        let s = slots(hub)
        await s.reload()
        #expect(s.pages.map(\.id) == ["usage", "alerts"], "a placement this app does not know is left out")
        #expect(s.pages[0].pluginName == "Example 网关面板" && s.pages[0].badge == ExtensionBadge(text: "3", tone: "danger"))
        #expect(s.cards.map(\.id) == ["today", "plain"])
        #expect(ExtensionSlots.icon("chart") == Theme.Icon.pluginChart && ExtensionSlots.icon("sparkles") == Theme.Icon.plugins)
        #expect(s.opened(by: s.cards[0])! == ("usage", "用量") && s.opened(by: s.cards[1]) == nil)
        #expect(s.total.count == 3 && !s.total.dot)

        // The plugin's badge changes (plugin_ui): a word only is a dot on Settings; cleared, nothing.
        s.received(plugin: "example-gateway", id: "usage", badge: ExtensionBadge(text: "新"))
        #expect(s.pages[0].badge?.text == "新" && s.total.count == 0 && s.total.dot)
        s.received(plugin: "example-gateway", id: "alerts", badge: ExtensionBadge(text: "2"))
        #expect(s.total.count == 2 && !s.total.dot, "numbers add up; words make no dot when there is a number")
        s.received(plugin: "example-gateway", id: "usage", badge: nil)
        s.received(plugin: "example-gateway", id: "alerts", badge: nil)
        s.received(plugin: "nobody", id: "usage", badge: ExtensionBadge(text: "9"))
        #expect(s.total.count == 0 && !s.total.dot)

        // A card's model draws compact, and is kept while the card is listed.
        let m = s.model(for: s.cards[0])
        #expect(m.context.size == "compact" && s.model(for: s.cards[0]) === m)
    }

    @Test func anOlderHubIsNotAsked() async throws {
        let hub = FakeHub()
        let s = slots(hub, features: ["plugins", "plugin_manager"])
        await s.reload()
        #expect(s.pages.isEmpty && s.cards.isEmpty && hub.sent("GET", "/api/v1/plugins/-/ui").isEmpty)
    }

    @MainActor private final class PendingReply {
        private var reply: CheckedContinuation<Data, Never>?
        private var started: CheckedContinuation<Void, Never>?

        func response() async -> Data {
            await withCheckedContinuation { continuation in
                reply = continuation
                started?.resume()
                started = nil
            }
        }

        func waitForRequest() async {
            if reply != nil { return }
            await withCheckedContinuation { started = $0 }
        }

        func finish(_ json: String) {
            reply?.resume(returning: Data(json.utf8))
            reply = nil
        }
    }

    @Test func theCurrentExtensionRoutesAndBadgeEvents() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions/-/ui", json: Self.all.replacingOccurrences(of: "plugin", with: "extension"))
        var api = hub.client
        api.extensionsAPI = true
        let client = api
        let s = ExtensionSlots(deps: .init(api: { client }), features: { ["extensions", "extension_ui_slots"] })
        await s.reload(for: "https://first.example")
        #expect(s.pages.map(\.owner) == ["example-gateway", "example-gateway"])
        #expect(s.cards.map(\.id) == ["today", "plain"])
        #expect(hub.sent("GET", "/api/v1/extensions/-/ui").count == 1)
        #expect(hub.sent("GET", "/api/v1/plugins/-/ui").isEmpty)
        let data = Data(#"{"type":"extension_ui","extension":"example-gateway","id":"usage","badge":{"text":"4","tone":"positive"}}"#.utf8)
        guard case .pluginBadge(let owner, let id, let badge, _)? = ServerEvent.decode(data) else {
            Issue.record("The extension badge event was not decoded"); return
        }
        s.received(plugin: owner, id: id, badge: badge)
        #expect(s.total.count == 4 && s.pages[0].badge?.tone == "positive")
        #expect(hub.sent("GET", "/api/v1/extensions/-/ui").count == 1, "a badge push does not reload the catalog")
    }

    @Test func aLateReplyCannotRestoreThePreviousHubsSlots() async {
        let pending = PendingReply()
        var old = FakeHub().client
        old.transport = { _ in await pending.response() }
        let newer = FakeHub()
        newer.on("GET", "/api/v1/plugins/-/ui", json: #"{"ui":[{"plugin":"new-owner","id":"new-page","title":"New page","placement":"nav_page"}]}"#)
        var current = old
        let s = ExtensionSlots(deps: .init(api: { current }), features: { ["extension_ui_slots"] })
        let first = Task { await s.reload(for: "https://first.example") }
        await pending.waitForRequest()
        current = newer.client
        await s.reload(for: "https://second.example")
        pending.finish(Self.all)
        await first.value
        #expect(s.pages.map(\.owner) == ["new-owner"] && s.cards.isEmpty)
        #expect(s.total.count == 0 && !s.total.dot)
    }

    @Test func aLateReplyCannotRestoreSlotsAfterFeatureRemoval() async {
        let pending = PendingReply()
        var api = FakeHub().client
        api.transport = { _ in await pending.response() }
        let client = api
        var features: Set<String> = ["extension_ui_slots"]
        let s = ExtensionSlots(deps: .init(api: { client }), features: { features })
        let first = Task { await s.reload(for: "https://first.example") }
        await pending.waitForRequest()
        features = []
        await s.reload(for: "https://first.example")
        pending.finish(Self.all)
        await first.value
        #expect(s.pages.isEmpty && s.cards.isEmpty && s.cardPages.isEmpty)
        #expect(s.total.count == 0 && !s.total.dot)
    }

    @Test func aNewerReloadWinsOnTheSameHub() async {
        let pending = PendingReply()
        var slow = FakeHub().client
        slow.transport = { _ in await pending.response() }
        let fresh = FakeHub()
        fresh.on("GET", "/api/v1/plugins/-/ui", json: #"{"ui":[]}"#)
        var current = slow
        let s = ExtensionSlots(deps: .init(api: { current }), features: { ["extension_ui_slots"] })
        let first = Task { await s.reload(for: "https://first.example") }
        await pending.waitForRequest()
        current = fresh.client
        await s.reload(for: "https://first.example")
        pending.finish(Self.all)
        await first.value
        #expect(s.pages.isEmpty && s.cards.isEmpty)
    }

    private static let chartView = #"""
    {"view":{"refresh_after":300,"sections":[{"items":[
      {"type":"stat_grid","stats":[{"label":"今天","value":"1.2M","detail":"+12% 比昨天","tone":"caution"},{"label":"费用","value":"¥36.20"}]},
      {"type":"chart","kind":"bar","title":"近 7 天","unit":"tokens","stacked":true,"series":[
        {"name":"输入","points":[{"label":"11-01","value":1200000,"display":"1.2M"},{"label":"11-02","value":800000}]},
        {"name":"输出","tone":"positive","points":[{"label":"11-01","value":300000},{"label":"11-02","value":250000}]}]},
      {"type":"hologram","beam":{"color":"red"}},
      {"type":"field","name":"note","label":"备注","kind":"text","value":"plugin"}]}]}}
    """#

    @Test func chartsStatsAndItemsThisAppDoesNotKnow() async throws {
        let hub = FakeHub()
        var draws = 0
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/today") { _ in draws += 1; return Data(Self.chartView.utf8) }
        let page = ExtensionViewModel(plugin: "example-gateway", ui: "today", context: ExtensionUIContext.here(machine: "macbook", size: "compact"),
                                   deps: .init(api: { hub.client }))
        await page.load()
        let v = try #require(page.view, "an item of a kind this app does not know does not lose the view")
        let items = v.sections[0].items
        #expect(items.map(\.type) == ["stat_grid", "chart", "hologram", "field"])
        #expect(items[0].statGrid?.stats.map(\.value) == ["1.2M", "¥36.20"] && items[0].statGrid?.stats[0].tone == "caution")
        let chart = try #require(items[1].chart)
        #expect(chart.kind == "bar" && chart.stacked == true && chart.unit == "tokens" && chart.series.count == 2)
        #expect(chart.series[0].points[0].display == "1.2M" && chart.series[0].points[1].display == nil)
        #expect(chart.series[1].tone == "positive" && chart.series[1].points.map(\.value) == [300000, 250000])
        #expect(ExtensionUIStyle.series(nil) == nil && ExtensionUIStyle.series("positive") == Theme.positive)
        #expect(ExtensionUIStyle.series("bogus") == ExtensionUIStyle.series("neutral"), "a tone this app does not know is neutral")

        // The context: the machine, the card's size, the app's time zone, platform and UI version.
        let body = try JSON.decoder.decode(JSONValue.self, from: hub.sent("POST", "/api/v1/plugins/example-gateway/-/ui/today")[0].body ?? Data())
        #expect(body["context"]?["machine"] == .string("macbook") && body["context"]?["size"] == .string("compact"))
        #expect(body["context"]?["ui_version"] == .number(3))

        // refresh_after: due that long after it was drawn; drawn again, what the user typed stays.
        #expect(v.refreshAfter == 300)
        let due = try #require(page.refreshDue)
        #expect(abs(due.timeIntervalSince(try #require(page.drawnAt)) - 300) < 0.01)
        page.values["note"] = "typed"
        await page.refresh()
        #expect(draws == 2 && page.values["note"] == "typed")
    }

    @Test func refreshAfterHasAFloor() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/p/-/ui/u", json: #"{"view":{"refresh_after":5,"sections":[]}}"#)
        let page = ExtensionViewModel(plugin: "p", ui: "u", deps: .init(api: { hub.client }))
        await page.load()
        #expect(abs(try #require(page.refreshDue).timeIntervalSince(try #require(page.drawnAt)) - 30) < 0.01, "never sooner than 30 s")
        hub.on("POST", "/api/v1/plugins/p/-/ui/u", json: #"{"view":{"sections":[]}}"#)
        await page.load()
        #expect(page.refreshDue == nil, "no refresh_after: never by itself")
    }

    @Test func theExtensionUIEvent() throws {
        let set = ServerEvent.decode(Data(#"{"type":"plugin_ui","plugin":"example-gateway","id":"usage","badge":{"text":"3","tone":"danger"}}"#.utf8))
        guard case .pluginBadge(let plugin, let id, let badge, _)? = set else { Issue.record("not decoded"); return }
        #expect(plugin == "example-gateway" && id == "usage" && badge == ExtensionBadge(text: "3", tone: "danger"))
        let cleared = ServerEvent.decode(Data(#"{"type":"plugin_ui","plugin":"example-gateway","id":"usage","badge":null}"#.utf8))
        guard case .pluginBadge(_, _, let none, _)? = cleared else { Issue.record("not decoded"); return }
        #expect(none == nil)
    }

    @Test func compactNumbers() {
        #expect(DataChart.compact(1_200_000) == "1.2M" && DataChart.compact(17_000_000) == "17M")
        #expect(DataChart.compact(1204) == "1.2k" && DataChart.compact(36_200) == "36k" && DataChart.compact(214) == "214")
        #expect(DataChart.compact(0.62) == "0.62" && DataChart.compact(-3.5) == "-3.5" && DataChart.compact(2_500_000_000) == "2.5B")
    }

    private static let segmentedView = #"""
    {"view":{"state":"range=7d","sections":[{"items":[
      {"type":"segmented","name":"range","value":"7d","action":{"id":"range"},"choices":[{"value":"1d","title":"今天"},{"value":"7d","title":"7 天"}]},
      {"type":"field","name":"note","label":"备注","kind":"text","value":"x"}]}]}}
    """#

    @Test func segmentedSendsItsPickAtOnce() async throws {
        let hub = FakeHub()
        var bodies: [JSONValue] = []
        hub.on("POST", "/api/v1/plugins/p/-/ui/u") { req in
            let b = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            bodies.append(b)
            guard let picked = b["values"]?["range"]?.string else { return Data(Self.segmentedView.utf8) }
            return Data(Self.segmentedView.replacingOccurrences(of: #""value":"7d""#, with: #""value":"\#(picked)""#).utf8)
        }
        let page = ExtensionViewModel(plugin: "p", ui: "u", deps: .init(api: { hub.client }))
        await page.load()
        let seg = try #require(page.view?.sections[0].items[0].segmented)
        #expect(page.values["range"] == "7d")
        #expect(await page.choose(seg.name, "7d", action: seg.action) == .init() && bodies.count == 1, "the one picked already: nothing sent")
        _ = await page.choose(seg.name, "1d", action: seg.action)
        #expect(bodies.last?["action"] == .string("range") && bodies.last?["values"] == .object(["range": .string("1d")]),
                "only its own value without submit")
        #expect(bodies.last?["state"] == .string("range=7d"))
        #expect(page.values["range"] == "1d" && page.view?.sections[0].items[0].segmented?.value == "1d", "the plugin's answer is the view")
    }

    @Test func aMachineTheHubDoesNotHaveDrawsNothing() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/p/-/ui/gpu") { _ in throw APIError(status: 404, message: "没有这台机器") }
        let section = ExtensionViewModel(plugin: "p", ui: "gpu", context: ExtensionUIContext.here(machine: "gone"), deps: .init(api: { hub.client }))
        await section.load()
        #expect(section.unsupported && section.error == nil)
        let page = ExtensionViewModel(plugin: "p", ui: "gpu", deps: .init(api: { hub.client }))
        await page.load()
        #expect(!page.unsupported && page.error != nil, "elsewhere a 404 is an error to show")
    }
}
