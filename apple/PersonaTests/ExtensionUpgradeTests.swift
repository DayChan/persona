import Foundation
import Testing
@testable import Persona

/// A market plugin's auto-upgrade (docs/extensions/extensions.md「自动升级」): the hub's fields decode, the Update section says what
/// happens to a newer version (and when it waits for the user), the switches go to the hub; a hub without the feature
/// shows no switch.
@MainActor
struct ExtensionUpgradeTests {
    private static let gateway = #"""
    {"id":"example-gateway","name":"Example 网关面板","kind":"ui","status":"running","version":"1.0.0","source":"installed",
     "auto_upgrade":true,
     "upgrade":{"id":"example-gateway","name":"Example 网关面板","market":"m1","from":"1.0.0","to":"1.1.0","state":"needs_permission",
                "added_permissions":[{"key":"network","title":"访问更多网络","detail":"status.example.com"}]},
     "upgrade_note":{"kind":"needs_permission","from":"1.0.0","to":"1.1.0","at":"2026-11-04T14:34:21.439310005Z"},
     "permissions":[]}
    """#

    @Test func aMarketExtensionsUpgradeDecodes() throws {
        let d = try JSON.decoder.decode(HubExtensionDetail.self, from: Data(Self.gateway.utf8))
        let p = d.plugin
        #expect(p.autoUpgrade == true)
        let u = try #require(p.upgrade)
        #expect(u.market == "m1" && u.from == "1.0.0" && u.to == "1.1.0" && u.state == "needs_permission")
        #expect(ExtensionText.upgradeDetail(u) == "要多一项权限：访问更多网络（status.example.com）")
        #expect(ExtensionText.upgradeAction(u) == "去确认")
        #expect(ExtensionText.summary(p) == ExtensionText.state(p) + " · 新版本待确认")
        let note = try #require(p.upgradeNote)
        #expect(note.at.timeIntervalSince1970 > 1_700_000_000)
        #expect(ExtensionText.upgradeNote(note) == nil, "asking is the newer version's line, not news of its own")
        // A plugin from elsewhere (or an older hub) has none of it.
        let plain = try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"x","name":"X","kind":"ui","status":"running"}"#.utf8))
        #expect(plain.autoUpgrade == nil && plain.upgrade == nil && plain.upgradeNote == nil)
    }

    @Test func eachStateSaysWhatHappens() {
        func up(_ state: String, detail: String? = nil) -> ExtensionUpgrade {
            ExtensionUpgrade(id: "p", name: "P", market: "m1", from: "1.0.0", to: "1.1.0", state: state, addedPermissions: nil, detail: detail)
        }
        #expect(ExtensionText.upgradeDetail(up("waiting_idle")) == "扩展空闲时自动升级")
        #expect(ExtensionText.upgradeDetail(up("off")) == "自动升级关着" && ExtensionText.upgradeAction(up("off")) == "去升级")
        #expect(ExtensionText.upgradeDetail(up("skipped")) == "上次升级后起不来，已回到 1.0.0，不再自动升级到这个版本")
        // A version the user rolled back from by hand (#1826) says so, not "didn't start"; the hub sends skipped_by.
        var byUser = up("skipped")
        byUser.skippedBy = "user"
        #expect(ExtensionText.upgradeDetail(byUser) == "你从这个版本回滚到了 1.0.0，不会再自动升级到它")
        #expect(ExtensionText.upgradeAction(byUser) == "去升级")
        let decoded = try? JSON.decoder.decode(ExtensionUpgrade.self, from: Data(
            #"{"id":"p","market":"m1","from":"1.0.0","to":"1.1.0","state":"skipped","skipped_by":"user"}"#.utf8))
        #expect(decoded?.skippedBy == "user")
        #expect(ExtensionText.upgradeDetail(up("failed", detail: "下载失败")) == "没装上：下载失败")
        #expect(ExtensionText.upgradeAction(up("waiting_idle")) == nil && ExtensionText.upgradeAction(up("incompatible")) == nil)
        #expect(ExtensionText.morePermissions([]) == "要的权限有变化")
        let two = [ExtensionPermission(key: "network", title: "访问更多网络", detail: "a.example.com"), ExtensionPermission(key: "hub_api", title: "使用 hub 的接口", detail: nil)]
        #expect(ExtensionText.morePermissions(two) == "要多两项权限：访问更多网络（a.example.com）、使用 hub 的接口")
        let at = Date()
        #expect(ExtensionText.upgradeNote(ExtensionUpgradeNote(kind: "upgraded", from: "0.9.0", to: "1.0.0", at: at)) == "已自动升级到 1.0.0（原来是 0.9.0）")
        #expect(ExtensionText.upgradeNote(ExtensionUpgradeNote(kind: "rolled_back", from: "1.0.0", to: "1.1.0", at: at)) == "1.1.0 起不来，已回到 1.0.0")
    }

    /// Switched off, the extension is not upgraded: its switch shows off (and greyed out) whatever its own setting, which
    /// the hub keeps for when it is on again; a hub from before auto_upgrade_paused shows the switch as it is.
    @Test(arguments: [
        (#""auto_upgrade":true"#, nil, true),
        (#""auto_upgrade":false"#, nil, false),
        (#""auto_upgrade":true,"auto_upgrade_paused":"disabled""#, "disabled", false),
        (#""auto_upgrade":false,"auto_upgrade_paused":"disabled""#, "disabled", false),
    ] as [(String, String?, Bool)])
    func theSwitchShowsOffWhilePaused(fields: String, paused: String?, shown: Bool) throws {
        let p = try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"x","name":"X","kind":"ui","status":"running",\#(fields)}"#.utf8))
        #expect(p.autoUpgradePaused == paused)
        #expect(ExtensionText.autoUpgradeShown(p) == shown)
    }

    @Test func theExtensionsSwitchGoesToTheHub() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/example-gateway/-/", json: Self.gateway)
        var sent: JSONValue?
        hub.on("PUT", "/api/v1/plugins/example-gateway/-/auto-upgrade") { req in
            sent = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(Self.gateway.replacingOccurrences(of: #""auto_upgrade":true"#, with: #""auto_upgrade":false"#).utf8)
        }
        let page = ExtensionModel(id: "example-gateway", deps: .init(api: { hub.client }))
        await page.load()
        await page.setAutoUpgrade(false)
        #expect(sent == .object(["auto": .bool(false)]))
        #expect(page.detail?.plugin.autoUpgrade == false && page.error == nil)
    }

    @Test func theGlobalSwitchIsThereOnlyWhenTheHubHasIt() async {
        let hub = FakeHub()
        let page = ExtensionMarketModel(deps: .init(api: { hub.client }))
        await page.loadAutoUpgrade()
        #expect(page.autoUpgrade == nil, "a hub from before auto-upgrade: no switch")
        hub.on("GET", "/api/v1/plugins/-/upgrades", json: #"{"auto":true,"upgrades":[],"off":[],"last":{}}"#)
        var sent: JSONValue?
        hub.on("PUT", "/api/v1/plugins/-/upgrades") { req in
            sent = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(#"{"auto":false,"upgrades":[],"off":[],"last":{}}"#.utf8)
        }
        await page.loadAutoUpgrade()
        #expect(page.autoUpgrade == true)
        await page.setAutoUpgrade(false)
        #expect(sent == .object(["auto": .bool(false)]) && page.autoUpgrade == false && page.error == nil)
    }
}
