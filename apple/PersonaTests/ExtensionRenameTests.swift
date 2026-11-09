import Foundation
import Testing
@testable import Persona

/// Plugins became extensions (#1844, #1859): a hub that says `extensions` is asked under /api/v1/extensions and answers
/// with the new keys; an older one keeps its /api/v1/plugins and old keys (docs/extensions/extensions.md「命名和兼容」).
@MainActor
struct ExtensionRenameTests {
    private func newHub(_ hub: FakeHub) -> APIClient {
        var c = hub.client
        c.extensionsAPI = true
        return c
    }

    @Test func pathsFollowTheHub() {
        let hub = FakeHub()
        #expect(hub.client.extensionPath("") == "/api/v1/plugins")
        #expect(hub.client.extensionPath("/-/markets") == "/api/v1/plugins/-/markets")
        #expect(newHub(hub).extensionPath("") == "/api/v1/extensions")
        #expect(newHub(hub).extensionPath("/p/-/ui/u") == "/api/v1/extensions/p/-/ui/u")
    }

    /// Before this launch's hello, requests go where the group's hub took them at its last hello: a hub from #1844 on is
    /// not asked on its old path at launch.
    @Test func theLastHelloDecidesUntilTheNextOne() {
        let group = "test-ext-" + UUID().uuidString
        defer { UserDefaults.standard.removeObject(forKey: "hubExtensionsAPI." + group) }
        let config = ServerConfig(url: URL(string: "https://fake.invalid")!, token: "t")
        func link() -> HubLink {
            HubLink(group: group, config: config, pins: PinsStore(deps: .init(api: { nil }, report: { _ in }, suffix: group)))
        }
        let first = link()
        #expect(!first.api.extensionsAPI, "never heard of: the old path, which every hub has")
        first.received(HubHello(features: ["plugins", "extensions"]))
        #expect(first.api.extensionsAPI)
        let next = link()
        #expect(next.api.extensionsAPI, "the next launch, before its hello")
        next.received(HubHello(features: ["plugins"]))
        #expect(!next.api.extensionsAPI && !link().api.extensionsAPI, "a hub rolled back")
    }

    @Test func aNewHubsListAndDetail() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions", json: #"{"extensions":[{"id":"voice","name":"语音","kind":"voice","status":"running"}],"can_install":true,"markets":true}"#)
        hub.on("GET", "/api/v1/extensions/voice/-/", json: #"""
        {"id":"voice","name":"语音","kind":"voice","status":"running","enabled":true,
         "ui":[{"extension":"voice","extension_name":"语音","id":"stats","title":"统计","placement":"plugin_page"}]}
        """#)
        let c = newHub(hub)
        let page = ExtensionsModel(deps: .init(api: { c }))
        await page.load()
        #expect(page.error == nil && page.plugins?.map(\.id) == ["voice"])
        let d = try await c.plugin("voice")
        #expect(d.ui.map(\.owner) == ["voice"] && d.ui.first?.ownerName == "语音")
        #expect(hub.sent("GET", "/api/v1/plugins").isEmpty, "the old path is not used")
    }

    @Test func uiPlacesWithEitherKeys() throws {
        let new = try JSON.decoder.decode(ExtensionUIPlace.self, from: Data(
            #"{"extension":"gw","extension_name":"网关","id":"usage","title":"用量","placement":"nav_page","badge":{"text":"3"}}"#.utf8))
        let old = try JSON.decoder.decode(ExtensionUIPlace.self, from: Data(
            #"{"plugin":"gw","plugin_name":"网关","id":"usage","title":"用量","placement":"nav_page","badge":{"text":"3"}}"#.utf8))
        #expect(new.owner == old.owner && new.owner == "gw" && new.ownerName == "网关" && old.ownerName == "网关" && new.badge == old.badge)
    }

    @Test func marketsAndCatalogWithEitherKeys() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions/-/catalog", json: #"""
        {"markets":[{"id":"m1","spec":"github:example/extensions","name":"Example","has_token":false,"extensions":4}],
         "extensions":[{"market":"m1","id":"example-calendar","name":"Example 日历","version":"1.0.0"}]}
        """#)
        hub.on("POST", "/api/v1/extensions/-/catalog/preview") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: String]
            #expect(body == ["market": "m1", "extension": "example-calendar"], "the new hub's request body")
            return Data(#"{"preview_id":"pv","manifest":{"id":"example-calendar"},"permissions":[],"permissions_digest":"d"}"#.utf8)
        }
        let c = newHub(hub)
        let catalog = try await c.pluginCatalog()
        #expect(catalog.markets.first?.plugins == 4 && catalog.plugins.map(\.pluginID) == ["example-calendar"])
        _ = try await c.previewCatalogExtension(market: "m1", plugin: "example-calendar")

        let old = try JSON.decoder.decode(ExtensionMarket.self, from: Data(#"{"id":"m1","spec":"s","name":"n","has_token":true,"plugins":2}"#.utf8))
        #expect(old.plugins == 2 && old.hasToken)
    }

    @Test func theNewEvent() {
        let set = ServerEvent.decode(Data(#"{"type":"extension_ui","extension":"gw","id":"usage","badge":{"text":"3"}}"#.utf8))
        guard case .pluginBadge(let ext, let id, let badge, _)? = set else { Issue.record("not decoded"); return }
        #expect(ext == "gw" && id == "usage" && badge?.text == "3")
    }

    @Test func featuresUnderEitherName() {
        #expect(Set(["extension_ui_slots"]).hasExtensionFeature("ui_slots"))
        #expect(Set(["plugin_ui_slots"]).hasExtensionFeature("ui_slots"))
        #expect(Set(["plugin_manager"]).hasExtensionFeature("manager"))
        #expect(!Set(["extensions"]).hasExtensionFeature("manager"))
    }

    @Test func aChannelsSettingsLink() {
        #expect(ChannelText.page("settings/extensions/wechat") == .plugin("wechat"))
        #expect(ChannelText.page("settings/plugins/wechat") == .plugin("wechat"))
        #expect(ChannelText.page("settings/extensions/a/b") == nil)
    }
}
