import Foundation
import Testing
@testable import Persona

/// Extension Market (ExtensionMarketModel, MarketExtensionModel) and a plugin's declarative UI (ExtensionViewModel, ExtensionLauncherModel):
/// what the hub sends decodes; the catalog is searched and filtered by market; markets are added (the hub's reason
/// comes back), refreshed, given a token and removed; installing and upgrading go preview → the digest agreed to;
/// removing is only for a plugin from this market; a View's fields go back on submit, a secret never prefilled; a
/// launcher section that says unsupported is hidden without an error.
@MainActor
struct ExtensionMarketModelTests {
    private static let markets = #"""
    [{"id":"m1","spec":"https://plugins.example.com/","name":"Example 插件","has_token":false,"plugins":2,"warnings":[],
      "fetched_at":"2026-11-04T06:00:00Z","added_at":"2026-11-01T08:00:00Z"},
     {"id":"m2","spec":"github:example/team","name":"团队插件","has_token":true,"plugins":1,"warnings":["第 3 个插件：没有 manifest"],
      "error":"读不到索引"}]
    """#

    private static func catalog(_ plugins: String) -> String {
        #"{"markets":"# + markets + #","plugins":["# + plugins + #"],"platform":"linux-amd64"}"#
    }

    private static let calendar = #"""
    {"market":"m1","market_name":"Example 插件","id":"example-calendar","name":"Example 日历","version":"0.9.0","tags":["日历"],
     "permissions":[{"key":"runs_code","title":"在 hub 机器上运行程序"}]}
    """#
    private static let gateway = #"""
    {"market":"m1","id":"example-gateway","name":"Example 网关面板","version":"1.1.0","installed":"1.0.0","installed_source":"installed",
     "from_this_market":true,"upgradable":true,"tags":[]}
    """#
    private static let windows = #"""
    {"market":"m2","id":"team-windows","name":"Windows 工具","version":"0.2.0","problem":"没有这台 hub 能用的包"}
    """#
    private static let notes = #"""
    {"market":"m1","id":"example-notes","name":"Example Notes","version":"1.2.0","installed":"1.2.0","installed_source":"installed"}
    """#

    private static let detail = #"{"id":"example-calendar","name":"Example 日历","kind":"connectors","status":"running"}"#

    private func market(_ hub: FakeHub) -> ExtensionMarketModel { ExtensionMarketModel(deps: .init(api: { hub.client })) }

    @Test func theCatalogDecodesAndSaysHowEachStands() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog([Self.calendar, Self.gateway, Self.windows, Self.notes].joined(separator: ",")))
        let page = market(hub)
        await page.load()
        let items = try #require(page.items)
        #expect(page.error == nil && items.count == 4 && page.markets.count == 2 && page.platform == "linux-amd64")
        #expect(items.map(ExtensionMarketText.step) == [.install, .upgrade, .unavailable, .upToDate])
        #expect(ExtensionMarketText.state(items[1]) == "可升级 · 1.0.0 → 1.1.0")
        #expect(items[0].id == "m1/example-calendar" && items[0].marketName == "Example 插件")
        #expect(page.markets[1].hasToken && page.markets[1].error == "读不到索引" && page.markets[1].warnings == ["第 3 个插件：没有 manifest"])
        #expect(page.markets[0].fetchedAt != nil)
    }

    /// The official market reads Official Extensions on its chip and its plugins, whatever its index calls it (#5017); others keep theirs.
    @Test func theOfficialMarketReadsOfficialExtensions() async throws {
        let hub = FakeHub()
        let json = #"""
        {"markets":[{"id":"o1","spec":"github:example/extensions","name":"Example 扩展","official":true,"has_token":false},
                    {"id":"m1","spec":"https://plugins.example.com/","name":"Example 插件","has_token":false}],
         "plugins":[{"market":"o1","market_name":"Example 扩展","id":"a","name":"A","version":"1.0.0"},
                    {"market":"m1","market_name":"Example 插件","id":"b","name":"B","version":"1.0.0"}]}
        """#
        hub.on("GET", "/api/v1/plugins/-/catalog", json: json)
        let page = market(hub)
        await page.load()
        let items = try #require(page.items)
        #expect(page.markets.map(\.title) == ["官方扩展", "Example 插件"])
        #expect(items.map(\.marketName) == ["官方扩展", "Example 插件"])
    }

    @Test func searchAndMarketGoToTheHub() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog(Self.calendar))
        let page = market(hub)
        page.query = "  日历 "
        page.market = "m1"
        await page.load(refresh: true)
        let q = hub.sent("GET", "/api/v1/plugins/-/catalog").last?.query
        #expect(q == ["q": "日历", "market": "m1", "refresh": "1"])
        // A market that is gone is no longer the filter.
        page.market = "gone"
        await page.load()
        #expect(page.market == nil)
    }

    @Test func addingAMarketSaysWhyTheHubRefused() async throws {
        let hub = FakeHub()
        var sent: JSONValue?
        hub.on("POST", "/api/v1/plugins/-/markets") { req in
            sent = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            if sent?["token"] == nil { throw APIError(status: 400, message: "私有的，要填 token", fields: ["code": "market_failed"]) }
            return Data(#"{"id":"m3","spec":"github:example/private","name":"私有","has_token":true,"plugins":0}"#.utf8)
        }
        let page = market(hub)
        #expect(await page.addMarket(spec: " ", token: "") == "填市场的地址")
        #expect(await page.addMarket(spec: "github:example/private", token: " ") == "私有的，要填 token")
        #expect(sent == .object(["spec": .string("github:example/private")]))
        #expect(await page.addMarket(spec: "github:example/private", token: "t0ken") == nil)
        #expect(sent == .object(["spec": .string("github:example/private"), "token": .string("t0ken")]))
        #expect(page.markets.map(\.id) == ["m3"] && page.markets[0].hasToken)
    }

    @Test func marketsAreRefreshedGivenATokenAndRemoved() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/markets", json: #"{"markets":"# + Self.markets + "}")
        hub.on("POST", "/api/v1/plugins/-/markets/m2/refresh", json: #"{"id":"m2","spec":"github:example/team","name":"团队插件","has_token":true,"plugins":3}"#)
        var token: JSONValue?
        hub.on("PUT", "/api/v1/plugins/-/markets/m2/token") { req in
            token = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(#"{"id":"m2","spec":"github:example/team","name":"团队插件","has_token":false,"plugins":3}"#.utf8)
        }
        hub.on("DELETE", "/api/v1/plugins/-/markets/m1", json: #"{"ok":true}"#)
        let page = market(hub)
        await page.loadMarkets()
        #expect(page.markets.count == 2)
        await page.refreshMarket("m2")
        #expect(page.markets[1].plugins == 3 && page.markets[1].error == nil && page.busyMarket == nil)
        #expect(await page.setToken("m2", token: "") == nil)
        #expect(token == .object(["token": .string("")]) && !page.markets[1].hasToken)
        page.market = "m1"
        await page.removeMarket("m1")
        #expect(page.markets.map(\.id) == ["m2"] && page.market == nil && page.error == nil)
    }

    @Test func installingGoesThroughThePreviewsDigest() async throws {
        let hub = FakeHub()
        var installed = false
        hub.on("GET", "/api/v1/plugins/-/catalog") { _ in
            Data(Self.catalog(installed ? #"{"market":"m1","id":"example-calendar","name":"Example 日历","version":"0.9.0","installed":"0.9.0","installed_source":"installed","from_this_market":true}"# : Self.calendar).utf8)
        }
        var previewBody: JSONValue?, installBody: JSONValue?
        let expires = Date.now.addingTimeInterval(3600).ISO8601Format()
        hub.on("POST", "/api/v1/plugins/-/catalog/preview") { req in
            previewBody = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(#"{"preview_id":"pv1","manifest":{"id":"example-calendar","name":"Example 日历","version":"0.9.0"},"permissions":[{"key":"runs_code","title":"在 hub 机器上运行程序"}],"permissions_digest":"d1","expires_at":"\#(expires)","market":"m1"}"#.utf8)
        }
        hub.on("POST", "/api/v1/plugins/-/install") { req in
            installBody = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            installed = true
            return Data(Self.detail.utf8)
        }
        hub.on("DELETE", "/api/v1/plugins/example-calendar/-/", json: #"{"ok":true}"#)
        let page = MarketExtensionModel(market: "m1", id: "example-calendar", deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.step == .install && !page.canUninstall)
        #expect(await page.uninstall() == false, "nothing to remove yet")
        await page.prepare()
        let pv = try #require(page.preview)
        #expect(previewBody == .object(["market": .string("m1"), "plugin": .string("example-calendar")]))
        #expect(pv.permissions.map(\.key) == ["runs_code"] && pv.upgrade != true)
        #expect(await page.confirm())
        #expect(installBody == .object(["preview_id": .string("pv1"), "permissions_digest": .string("d1")]))
        #expect(page.preview == nil && page.step == .upToDate && page.canUninstall)
        #expect(await page.uninstall())
        #expect(hub.sent("DELETE", "/api/v1/plugins/example-calendar/-/").count == 1)
    }

    @Test func anUpgradeListsWhatItAsksForInAddition() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog(Self.gateway))
        hub.on("POST", "/api/v1/plugins/-/catalog/preview", json: #"""
        {"preview_id":"pv2","manifest":{"id":"example-gateway","version":"1.1.0"},"permissions":[],"permissions_digest":"d2","upgrade":true,"from":"1.0.0",
         "more_permissions":true,"added_permissions":[{"key":"network","title":"访问网络","detail":"status.example.com"}]}
        """#)
        var upgraded: JSONValue?
        hub.on("POST", "/api/v1/plugins/example-gateway/-/upgrade") { req in
            upgraded = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(#"{"id":"example-gateway","name":"Example 网关面板","kind":"ui","status":"running"}"#.utf8)
        }
        let page = MarketExtensionModel(market: "m1", id: "example-gateway", deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.step == .upgrade && page.canUninstall)
        await page.prepare()
        #expect(page.preview?.addedPermissions?.map(\.detail) == ["status.example.com"] && page.preview?.from == "1.0.0")
        #expect(await page.confirm())
        #expect(upgraded == .object(["preview_id": .string("pv2"), "permissions_digest": .string("d2")]))
    }

    @Test func aRefusedPreviewIsSaid() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog(Self.windows))
        hub.on("POST", "/api/v1/plugins/-/catalog/preview") { _ in
            throw APIError(status: 400, message: "没有这台 hub 能用的包", fields: ["code": "invalid_plugin"])
        }
        let page = MarketExtensionModel(market: "m2", id: "team-windows", deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.step == .unavailable)
        await page.prepare()
        #expect(page.preview == nil && page.error == "没有这台 hub 能用的包" && !page.working)
    }

    @Test func theExtensionListSaysWhetherThereAreMarkets() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins", json: #"{"plugins":[],"can_install":true,"markets":true}"#)
        let page = ExtensionsModel(deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.markets && page.plugins?.isEmpty == true)
        hub.on("GET", "/api/v1/plugins", json: #"{"plugins":[]}"#)
        await page.load()
        #expect(!page.markets, "an older hub: no market")
        hub.on("GET", "/api/v1/plugins/example-gateway/-/", json: #"""
        {"id":"example-gateway","name":"Example 网关面板","kind":"ui","status":"running",
         "ui":[{"plugin":"example-gateway","id":"home","title":"概况","placement":"plugin_page"},
               {"plugin":"example-gateway","id":"panel","title":"面板","placement":"launcher_section"}]}
        """#)
        let detail = ExtensionModel(id: "example-gateway", deps: .init(api: { hub.client }))
        await detail.load()
        #expect(detail.detail?.ui.map(\.id) == ["home"])
    }
}

/// A plugin's View (docs/extensions/extensions.md「声明式 UI」).
@MainActor
struct ExtensionViewModelTests {
    private static let view = #"""
    {"view":{"title":"Example 网关","state":"tab=overview","sections":[
      {"title":"概况","items":[{"type":"metric","label":"余额","value":"¥ 182.40","tone":"positive"},
                              {"type":"progress","label":"今日","ratio":0.93,"tone":"danger"},{"type":"divider"},
                              {"type":"row","title":"工作","badge":{"text":"有效","tone":"positive"},"action":{"id":"key:work"}}]},
      {"items":[{"type":"field","name":"note","label":"备注","kind":"text","value":"hi"},
                {"type":"field","name":"token","label":"Token","kind":"secret","value":"leaked"},
                {"type":"field","name":"auto","label":"提醒","kind":"toggle"},
                {"type":"field","name":"plan","label":"套餐","kind":"choice","value":"team","choices":[{"value":"solo","title":"个人"},{"value":"team"}]},
                {"type":"button","title":"保存","action":{"id":"save","submit":true}},
                {"type":"link","title":"后台","url":"https://example.com/console"}]}]}}
    """#

    @Test func aViewDecodesAndItsFieldsStartAsTheySay() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/home", json: Self.view)
        let page = ExtensionViewModel(plugin: "example-gateway", ui: "home", deps: .init(api: { hub.client }))
        await page.load()
        let v = try #require(page.view)
        #expect(v.title == "Example 网关" && v.sections.count == 2 && v.sections[0].items.map(\.type) == ["metric", "progress", "divider", "row"])
        #expect(v.sections[0].items[1].progress?.ratio == 0.93 && v.sections[0].items[3].row?.badge?.tone == "positive")
        #expect(v.sections[1].items[3].field?.choices?.map(\.label) == ["个人", "team"])
        #expect(page.values == ["note": "hi", "token": "", "auto": "false", "plan": "team"], "a secret is never prefilled")
        #expect(ExtensionUIStyle.tone("danger") == Theme.danger && ExtensionUIStyle.tone("bogus") == .secondary)
        // Drawing sends no action and no values; the context says where and for whom (docs/extensions/extensions.md「UI 插槽」, context).
        let body = try JSON.decoder.decode(JSONValue.self, from: hub.requests.last?.body ?? Data())
        #expect(body["action"] == nil && body["values"] == nil && body["state"] == nil)
        #expect(body["context"]?["ui_version"] == .number(6) && body["context"]?["platform"] == .string("ios"))
        #expect(body["context"]?["tz"]?.string == TimeZone.current.identifier && body["context"]?["locale"]?.string?.isEmpty == false)
        #expect(body["context"]?["launcher"] == nil && body["context"]?["machine"] == nil)
    }

    @Test func submitTakesTheFieldsAndTheStateAlong() async throws {
        let hub = FakeHub()
        var bodies: [JSONValue] = []
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/home") { req in
            let b = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            bodies.append(b)
            switch b["action"]?.string {
            case "save": return Data(#"{"toast":"已保存"}"#.utf8)
            case "key:work": return Data(#"{"open_url":"https://example.com/console/keys"}"#.utf8)
            case "bad": return Data(#"{"open_url":"http://example.com/plain"}"#.utf8)
            default: return Data(Self.view.utf8)
            }
        }
        let page = ExtensionViewModel(plugin: "example-gateway", ui: "home", context: ExtensionUIContext.here(launcher: "work"), deps: .init(api: { hub.client }))
        await page.load()
        page.values["token"] = "s3cret"
        page.values["auto"] = "true"
        let saved = await page.perform(ExtensionAction(id: "save", submit: true))
        #expect(saved == .init(toast: "已保存"))
        #expect(bodies[1]["context"]?["launcher"] == .string("work"))
        var sent = bodies[1]
        if case .object(var o) = sent { o["context"] = nil; sent = .object(o) }
        #expect(sent == .object(["state": .string("tab=overview"), "action": .string("save"),
                                      "values": .object(["note": .string("hi"), "token": .string("s3cret"), "auto": .string("true"), "plan": .string("team")])]))
        let opened = await page.perform(ExtensionAction(id: "key:work"))
        #expect(opened.url?.absoluteString == "https://example.com/console/keys")
        #expect(bodies[2]["values"] == nil, "only a submit sends the fields")
        #expect(await page.perform(ExtensionAction(id: "bad")).url == nil, "only https opens")
        // Nothing in the answer: drawn again.
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/home") { req in
            let b = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            bodies.append(b)
            return Data((b["action"] == nil ? Self.view : "{}").utf8)
        }
        let before = bodies.count
        #expect(await page.perform(ExtensionAction(id: "noop")) == .init())
        #expect(bodies.count == before + 2 && bodies.last?["action"] == nil)
    }

    @Test func aFailureIsSaidAndCanBeRetried() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/home") { _ in
            throw APIError(status: 502, message: "插件没有回答", fields: ["code": "plugin_failed"])
        }
        let page = ExtensionViewModel(plugin: "example-gateway", ui: "home", deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.view == nil && page.error == "插件没有回答" && !page.unsupported)
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/home", json: Self.view)
        await page.load()
        #expect(page.view != nil && page.error == nil)
    }

    /// #1339: a section without hosts lists itself on every launcher; drawn for one whose gateway is not its own, the
    /// plugin says 404 unsupported and the section is not shown — no error either.
    @Test func anUnsupportedLauncherSectionIsHidden() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/ui", json: #"""
        {"ui":[{"plugin":"example-gateway","id":"panel","title":"Example 网关","placement":"launcher_section"},
               {"plugin":"example-gateway","id":"elsewhere","title":"别的网关","placement":"launcher_section"}]}
        """#)
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/panel", json: #"{"view":{"sections":[{"items":[{"type":"metric","label":"余额","value":"1"}]}]}}"#)
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/elsewhere") { _ in
            throw APIError(status: 404, message: "not this plugin's gateway", fields: ["code": "unsupported"])
        }
        let page = ExtensionLauncherModel(launcher: "work", deps: .init(api: { hub.client }))
        await page.load()
        #expect(hub.sent("GET", "/api/v1/plugins/-/ui").last?.query == ["placement": "launcher_section", "launcher": "work"])
        #expect(page.shown.map(\.place.id) == ["panel"])
        let hidden = try #require(page.models["example-gateway/elsewhere"])
        #expect(hidden.unsupported && hidden.error == nil)
        // Another 404 (the plugin gone) is an error, still shown.
        hub.on("POST", "/api/v1/plugins/example-gateway/-/ui/elsewhere") { _ in
            throw APIError(status: 404, message: "no such plugin", fields: ["code": "not_found"])
        }
        let other = ExtensionViewModel(plugin: "example-gateway", ui: "elsewhere", deps: .init(api: { hub.client }))
        await other.load()
        #expect(!other.unsupported && other.error == "no such plugin")
    }
}
