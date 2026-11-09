import Foundation
import Testing
@testable import Persona

/// A launcher's /model list with the IDs typed before (#1644): reading it, and Forget (`DELETE …/models/custom`): the row
/// goes at once and comes back where it was when the hub says no.
@MainActor @Suite struct LauncherModelsModelTests {
    private let path = "/api/v1/machines/box/launchers/claude/models"
    private let list = """
        {"agent":"claude","models":[
          {"id":"default","name":"Default","default":true},
          {"id":"opus","name":"Opus 5.5"},
          {"id":"vendor/model-x","name":"vendor/model-x","custom":true},
          {"id":"~vendor/model-latest","name":"~vendor/model-latest","custom":true}
        ]}
        """
    private let box = MachineRef(id: "box")

    private func loaded(_ hub: FakeHub) async -> LauncherModelsModel {
        let m = LauncherModelsModel(deps: .init(api: { _ in hub.client }))
        await m.load(box, launcher: "claude")
        return m
    }

    @Test func readsTheListWithTheTypedIDs() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        let m = await loaded(hub)
        #expect(m.key == "box|claude" && !m.loading && m.error == nil)
        #expect(m.list?.models.count == 4)
        #expect(m.remembered.map(\.id) == ["vendor/model-x", "~vendor/model-latest"])
    }

    @Test func forgetSendsTheIDAndDropsTheRow() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom", json: #"{"ok":true}"#)
        let m = await loaded(hub)
        await m.forget("vendor/model-x", machine: box, launcher: "claude")
        #expect(m.remembered.map(\.id) == ["~vendor/model-latest"])
        #expect(m.forgetError == nil)
        let sent = hub.sent("DELETE", path + "/custom")
        #expect(sent.count == 1 && sent.first?.query == ["model": "vendor/model-x"])
    }

    @Test func aRefusedForgetPutsTheRowBackAndSaysWhy() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom") { _ in throw APIError(status: 403, message: "只读设备不能改") }
        let m = await loaded(hub)
        await m.forget("vendor/model-x", machine: box, launcher: "claude")
        #expect(m.list?.models.map(\.id) == ["default", "opus", "vendor/model-x", "~vendor/model-latest"])
        #expect(m.forgetError?.contains("只读设备不能改") == true)
        #expect(m.forgetError?.contains("vendor/model-x") == true)
    }

    /// Optimistic: the row is gone while the hub is still asked.
    @Test func theRowGoesBeforeTheHubAnswers() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        let m = await loaded(hub)
        let during = Seen()
        hub.on("DELETE", path + "/custom") { _ in
            during.ids = m.remembered.map(\.id)
            return Data(#"{"ok":true}"#.utf8)
        }
        await m.forget("~vendor/model-latest", machine: box, launcher: "claude")
        #expect(during.ids == ["vendor/model-x"])
        #expect(m.remembered.map(\.id) == ["vendor/model-x"])
    }

    /// Only typed IDs, and only for the launcher on screen.
    @Test func forgetsOnlyTypedIDsOfThisList() async {
        let hub = FakeHub()
        hub.on("GET", path, json: list)
        hub.on("DELETE", path + "/custom", json: #"{"ok":true}"#)
        let m = await loaded(hub)
        await m.forget("opus", machine: box, launcher: "claude")
        await m.forget("vendor/model-x", machine: MachineRef(id: "other"), launcher: "claude")
        #expect(m.list?.models.count == 4)
        #expect(hub.sent("DELETE", path + "/custom").isEmpty)
    }

    @Test func anOlderHubHasNoListAndNoError() async {
        let hub = FakeHub()
        hub.on("GET", path) { _ in throw APIError(status: 404, message: "not found") }
        let m = await loaded(hub)
        #expect(m.list == nil && m.error == nil && !m.loading)
    }

    @Test func aFailedReadSaysWhyAndAnotherLauncherStartsOver() async {
        let hub = FakeHub()
        hub.on("GET", path) { _ in throw APIError(status: 502, message: "claude 没登录") }
        let m = await loaded(hub)
        #expect(m.list == nil && m.error == "claude 没登录")
        m.select(box, launcher: "codex")
        #expect(m.key == "box|codex" && m.error == nil && m.list == nil)
    }
}

/// What a FakeHub route saw while the request was on its way.
@MainActor private final class Seen { var ids: [String]? }
