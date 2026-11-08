import Foundation
import Testing
@testable import Persona

@MainActor
struct ExtensionManagementTests {
    private let path = "/api/v1/plugins/example/-/"
    private let detail = #"{"id":"example","name":"示例扩展","kind":"ui","status":"running","enabled":true,"config":{"title":"original"},"contributions":[{"point":"ui","id":"page","title":"示例页面","enabled":true,"can_disable":true},{"point":"connectors","id":"source","enabled":true,"can_disable":false,"via":"connector"}]}"#

    private func model(_ hub: FakeHub) -> ExtensionModel {
        ExtensionModel(id: "example", deps: .init(api: { hub.client }))
    }

    @Test func oldHubsDoNotInventContributionSwitches() throws {
        let old = try JSON.decoder.decode(HubExtensionDetail.self, from: Data(#"{"id":"example","name":"示例","kind":"ui","status":"running","contributes":{"ui":["page"]}}"#.utf8))
        #expect(old.contributions == nil)
        let current = try JSON.decoder.decode(HubExtensionDetail.self, from: Data(detail.utf8))
        #expect(current.contributions?.map(\.id) == ["ui/page", "connectors/source"])
        #expect(current.contributions?.last?.via == "connector" && current.contributions?.last?.canDisable == false)
    }

    @Test func switchingOneContributionPreservesTheConfigDraft() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: detail)
        hub.on("PUT", path + "contributions/ui/page", json: detail.replacingOccurrences(of: #""title":"示例页面","enabled":true"#, with: #""title":"示例页面","enabled":false"#))
        let m = model(hub)
        await m.load()
        m.draft["title"] = .string("正在编辑")
        let contribution = try #require(m.detail?.contributions?.first)
        await m.setContribution(contribution, enabled: false)
        #expect(m.detail?.contributions?.first?.enabled == false)
        #expect(m.draft["title"] == .string("正在编辑") && m.dirty)
        let body = try JSON.decoder.decode(JSONValue.self, from: try #require(hub.sent("PUT", path + "contributions/ui/page").first?.body))
        #expect(body == .object(["enabled": .bool(false)]))
        #expect(m.changingContribution == nil)
        await m.setContribution(try #require(m.detail?.contributions?.last), enabled: false)
        #expect(hub.sent("PUT", path + "contributions/connectors/source").isEmpty)
    }

    @Test func disablingDependentsRequiresASecondConfirmation() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: detail)
        let response = detail
        hub.on("PUT", path + "enabled") { request in
            let body = try JSON.decoder.decode(JSONValue.self, from: request.body ?? Data())
            if body["cascade"] != .bool(true) {
                throw APIError(status: 409, message: "依赖的扩展要一起停用", fields: ["code": "has_dependents"],
                               details: .object(["dependents": .array([.object(["id": .string("child"), "name": .string("示例子扩展")])])]))
            }
            return Data(response.replacingOccurrences(of: #""status":"running","enabled":true"#, with: #""status":"disabled","enabled":false"#).utf8)
        }
        let m = model(hub)
        await m.load()
        await m.setEnabled(false)
        #expect(m.detail?.plugin.enabled == true && m.disableConfirmation?.dependents == ["示例子扩展"])
        #expect(hub.sent("PUT", path + "enabled").count == 1, "拒绝后不能自动发 cascade")
        await m.confirmDisable()
        #expect(m.detail?.plugin.enabled == false && m.disableConfirmation == nil)
        #expect(hub.sent("PUT", path + "enabled").count == 2)
    }

    @Test func aBundleSendsEveryPermissionDigest() throws {
        let preview = try decodePreview(#"{"depends":[{"id":"base","state":"missing"}],"bundle":[{"preview_id":"base-preview","permissions_digest":"base-digest","manifest":{"id":"base","name":"基础扩展"},"permissions":[]}] }"#)
        #expect(preview.canConfirm)
        let body = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(ExtensionInstallRequest(preview: preview)))
        #expect(body["bundle"] == .array([.object(["preview_id": .string("base-preview"), "permissions_digest": .string("base-digest")])]))
        #expect(body["preview_id"] == .string("main-preview") && body["permissions_digest"] == .string("main-digest"))
        let old = try decodePreview("{}")
        let oldBody = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(ExtensionInstallRequest(preview: old)))
        #expect(old.canConfirm && oldBody["bundle"] == nil)
    }

    @Test func unresolvedOrDisabledDependenciesCannotBeConfirmed() throws {
        #expect(try !decodePreview(#"{"unresolved":["base"]}"#).canConfirm)
        #expect(try !decodePreview(#"{"depends":[{"id":"base","state":"missing"}]}"#).canConfirm)
        #expect(try !decodePreview(#"{"depends":[{"id":"base","state":"disabled"}]}"#).canConfirm)
    }

    @Test func detailsExposeDependenciesAndPackMembers() throws {
        let d = try JSON.decoder.decode(HubExtensionDetail.self, from: Data(#"{"id":"pack","name":"示例合集","kind":"extension_pack","status":"running","depends":[{"id":"base","name":"基础扩展","state":"old","version":">=2.0.0","installed":"1.0.0"}],"extension_pack":[{"id":"member","version":">=1.0.0"}]}"#.utf8))
        #expect(d.depends?.first?.description == "需要 >=2.0.0，当前 1.0.0")
        #expect(d.extensionPack?.first?.id == "member")
    }

    @Test func configDraftSurvivesViewReconstructionAndReload() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: detail)
        hub.on("PUT", path + "config", json: detail)
        let drafts = ExtensionDrafts()
        let deps = ExtensionModel.Deps(api: { hub.client }, drafts: drafts)
        let original = ExtensionModel(id: "example", deps: deps)
        await original.load()
        original.draft["title"] = .string("未保存的草稿")
        original.numberText["count"] = "-"
        #expect(original.dirty && !original.canSave)
        let rebuilt = ExtensionModel(id: "example", deps: deps)
        await rebuilt.load()
        #expect(rebuilt.draft["title"] == .string("未保存的草稿") && rebuilt.numberText["count"] == "-")
        await rebuilt.load()
        #expect(rebuilt.draft["title"] == .string("未保存的草稿"))
        let other = ExtensionModel(id: "example", deps: .init(api: { hub.client }, drafts: ExtensionDrafts()))
        await other.load()
        #expect(other.draft["title"] == .string("original"))
        rebuilt.numberText = [:]
        #expect(await rebuilt.save())
        #expect(drafts.values.isEmpty)
    }

    @Test func anExpiredMemberOrUnresolvedMemberBlocksTheWholeBundle() throws {
        let expired = try decodePreview(#"{"bundle":[{"preview_id":"member","permissions_digest":"digest","manifest":{"id":"member"},"permissions":[],"expires_at":"2020-01-01T00:00:00Z"}]}"#)
        #expect(!expired.canConfirm)
        #expect(expired.expirationBoundary == expired.bundle?.first?.expiresAt)
        let unresolved = try decodePreview(#"{"bundle":[{"preview_id":"member","permissions_digest":"digest","manifest":{"id":"member"},"permissions":[],"unresolved":["missing"]}]}"#)
        #expect(!unresolved.canConfirm)
    }

    @Test func uninstallRequiresConfirmationAndReportsOnlyRemovedMembers() async throws {
        let hub = FakeHub()
        let catalog = #"{"plugins":[{"market":"market","id":"pack","name":"示例合集","version":"1.0.0","installed":"1.0.0","from_this_market":true}]}"#
        hub.on("GET", "/api/v1/plugins/-/catalog", json: catalog)
        hub.on("GET", "/api/v1/plugins/pack/-/", json: #"{"id":"pack","name":"示例合集","kind":"extension_pack","status":"running"}"#)
        hub.on("DELETE", "/api/v1/plugins/pack/-/") { request in
            guard request.query["cascade"] == "1" else {
                throw APIError(status: 409, message: "需要确认", fields: ["code": "has_dependents"],
                               details: .object(["dependents": .array([.object(["name": .string("外部依赖者")])])]))
            }
            #expect(request.query["members"] == "1")
            return Data(#"{"removed_members":["one"]}"#.utf8)
        }
        let m = MarketExtensionModel(market: "market", id: "pack", deps: .init(api: { hub.client }))
        await m.load()
        #expect(m.isPack)
        #expect(await !m.uninstall(members: true))
        #expect(m.removingMembers && m.uninstallConfirmation?.dependents == ["外部依赖者"])
        #expect(hub.sent("DELETE", "/api/v1/plugins/pack/-/").count == 1)
        #expect(await m.uninstall(cascade: true, members: m.removingMembers))
        #expect(m.removalMessage == "合集已卸载；成功卸载 1 个成员")
    }

    // An uninstall can succeed with cleanup warnings (#4466): every one is kept, in order, repeats and long paths
    // included; an older hub's answer (no warnings, null, []) has none (#4819).
    @Test func removalResultsKeepEveryCleanupWarning() throws {
        let long = "请手动删除 /tmp/example-extension/" + String(repeating: "long-directory/", count: 24) + "data"
        let warnings = ["清理 /tmp/example-one 失败", long, "清理 /tmp/example-one 失败"]
        let body = try JSONSerialization.data(withJSONObject: ["ok": true, "warnings": warnings, "removed_members": ["one"]])
        let result = try JSON.decoder.decode(ExtensionRemovalResult.self, from: body)
        #expect(result.cleanupWarnings == warnings)
        #expect(result.removedMembers == ["one"])
        for legacy in [#"{"ok":true}"#, #"{"ok":true,"warnings":null}"#, #"{"ok":true,"warnings":[]}"#, #"{"removed_members":["one"]}"#] {
            let old = try JSON.decoder.decode(ExtensionRemovalResult.self, from: Data(legacy.utf8))
            #expect(old.cleanupWarnings.isEmpty, "\(legacy)")
        }
    }

    private func installedHub(_ answer: @escaping FakeHub.Route) -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: #"{"plugins":[{"market":"market","id":"example","name":"示例扩展","version":"1.0.0","installed":"1.0.0","from_this_market":true}]}"#)
        hub.on("GET", path, json: detail)
        hub.on("DELETE", path, answer)
        return hub
    }

    @Test func anUninstallWithWarningsIsDoneAndKeepsThemUntilAcknowledged() async throws {
        let warnings = ["请手动删除 /tmp/example-extension/data", "请手动删除 /tmp/example-extension/package"]
        let hub = installedHub { _ in try JSONSerialization.data(withJSONObject: ["ok": true, "warnings": warnings]) }
        let m = MarketExtensionModel(market: "market", id: "example", deps: .init(api: { hub.client }))
        await m.load()
        #expect(await m.uninstall())
        #expect(m.error == nil)
        #expect(m.removalMessage == "已卸载")
        let notice = try #require(m.removalNotice)
        #expect(notice.warnings == warnings)
        #expect(notice.title == "已卸载")
        #expect(hub.sent("DELETE", path).count == 1)
        m.acknowledgeRemoval()
        #expect(m.removalNotice == nil)
    }

    @Test func anUninstallWithoutWarningsOrThatFailedShowsNoNotice() async throws {
        for legacy in [#"{"ok":true}"#, #"{"ok":true,"warnings":null}"#, #"{"ok":true,"warnings":[]}"#] {
            let hub = installedHub { _ in Data(legacy.utf8) }
            let m = MarketExtensionModel(market: "market", id: "example", deps: .init(api: { hub.client }))
            await m.load()
            #expect(await m.uninstall())
            #expect(m.removalNotice == nil, "\(legacy)")
        }
        let failing = installedHub { _ in throw APIError(status: 500, message: "卸载失败") }
        let m = MarketExtensionModel(market: "market", id: "example", deps: .init(api: { failing.client }))
        await m.load()
        #expect(await !m.uninstall())
        #expect(m.removalNotice == nil)
        #expect(m.error != nil || m.operation?.state == .failed)
    }

    private func decodePreview(_ extra: String) throws -> ExtensionPreview {
        var json = try #require(try JSONSerialization.jsonObject(with: Data(extra.utf8)) as? [String: Any])
        json["preview_id"] = "main-preview"
        json["permissions_digest"] = "main-digest"
        json["manifest"] = ["id": "pack", "name": "示例合集"]
        json["permissions"] = []
        return try JSON.decoder.decode(ExtensionPreview.self, from: JSONSerialization.data(withJSONObject: json))
    }
}
