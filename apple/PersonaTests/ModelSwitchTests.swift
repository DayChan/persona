import Foundation
import Testing
@testable import Persona

/// Switching models in the effort capsule (#3119, the web's #2434): which sessions offer it, when it waits, and what goes to the hub.
struct ModelSwitchTests {
    private func session(_ extra: String = "", state: String = "idle") throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"s1","status":"idle","state":"\#(state)","pane_id":"p1"\#(extra)}"#.utf8))
    }

    @Test func onlyClaudeCodeInAPane() throws {
        #expect(SessionModes.switchesModel(try session()))
        #expect(SessionModes.switchesModel(try session(#","agent":"claude""#)))
        #expect(!SessionModes.switchesModel(try session(#","agent":"codex""#)))
        #expect(!SessionModes.switchesModel(try session(#","agent":"traex""#)))
        #expect(!SessionModes.switchesModel(try session(#","assistant":true"#)))
        let noPane = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"s1","status":"idle","state":"idle"}"#.utf8))
        #expect(!SessionModes.switchesModel(noPane))
        #expect(!SessionModes.switchesModel(nil))
    }

    @Test func waitsWhileItWorksOrAsks() throws {
        #expect(SessionModes.modelBusy(try session()) == nil)
        #expect(SessionModes.modelBusy(try session(state: "unread")) == nil)
        #expect(SessionModes.modelBusy(try session(state: "working")) != nil)
        #expect(SessionModes.modelBusy(try session(state: "needs_you")) != nil)
        #expect(SessionModes.modelBusy(try session(state: "offline")) != nil)
    }

    @MainActor @Test func thePickItRunsSendsNothing() {
        #expect(SessionModes.modelPatch("opus", current: "claude-opus-5-5-20260101") == nil)
        #expect(SessionModes.modelPatch("sonnet", current: "claude-opus-5-5-20260101") == "sonnet")
        #expect(SessionModes.modelPatch("default", current: nil) == nil)
        #expect(SessionModes.modelPatch("haiku", current: nil) == "haiku")
    }

    /// A session running a typed model (#1644) is not on Default: picking Default switches back.
    @MainActor @Test func defaultSwitchesBackFromATypedModel() {
        #expect(SessionModes.modelPatch("default", current: "vendor/model-x") == "default")
        #expect(SessionModes.modelPatch("default", current: "~vendor/model-latest[1m]") == "default")
        #expect(SessionModes.modelPatch("opus", current: "vendor/model-x") == "opus")
    }

    /// What the menus tick: an alias for Claude's tiers, Default while nothing is known, the ID itself otherwise.
    @MainActor @Test func aTypedModelTicksNoAlias() {
        #expect(PromptItems.runningModelID(nil) == "default")
        #expect(PromptItems.runningModelID("") == "default")
        #expect(PromptItems.runningModelID("<synthetic>") == "default")
        #expect(PromptItems.runningModelID("claude-opus-5-5-20260101") == "opus")
        #expect(PromptItems.runningModelID("Sonnet") == "sonnet")
        #expect(PromptItems.runningModelID("vendor/model-x") == "vendor/model-x")
        #expect(PromptItems.unlistedModel("claude-haiku-4-5") == nil)
        #expect(PromptItems.unlistedModel(nil) == nil)
        #expect(PromptItems.unlistedModel("~vendor/model-latest[1m]") == "~vendor/model-latest[1m]")
        // The model it runs is a row of its own, by its ID, the one ticked.
        #expect(PromptItems.runningModels("vendor/model-x").map(\.id) == PromptItems.claudeModels.map(\.id) + ["vendor/model-x"])
        #expect(PromptItems.runningModels("vendor/model-x").last?.name == "vendor/model-x")
        #expect(PromptItems.runningModels(nil).map(\.id) == PromptItems.claudeModels.map(\.id))
        let menu = PromptItems.composerModels(running: "vendor/model-x").map(\.id)
        #expect(menu == PromptItems.claudeModels.map(\.id) + ["vendor/model-x", PromptItems.otherModel.id])
        #expect(PromptItems.composerModels(running: "claude-opus-5-5").map(\.id) == PromptItems.claudeModels.map(\.id) + [PromptItems.otherModel.id])
    }

    @Test func modesCarryTheModelAlone() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        let key = SessionKey(machineID: "box", sessionID: "s1")
        try await hub.client.setModes(key, model: "sonnet")
        try await hub.client.setModes(key, effort: "high")
        let sent = hub.sent("POST", "/api/v1/sessions/box/s1/modes")
        #expect(sent.count == 2)
        let a = try #require(sent.first?.body), b = try #require(sent.last?.body)
        let first = try #require(try JSONSerialization.jsonObject(with: a) as? [String: String])
        #expect(first == ["model": "sonnet"])
        let second = try #require(try JSONSerialization.jsonObject(with: b) as? [String: String])
        #expect(second == ["effort": "high"]) // an older caller's body is unchanged
    }

    /// "Other Model…" (#1644): a typed ID goes through …/modes like a listed one; the list it comes with is the
    /// session's launcher's.
    @Test func aTypedModelGoesThroughModes() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/modes", json: #"{"ok":true}"#)
        try await hub.client.setModes(SessionKey(machineID: "box", sessionID: "s1"), model: "~vendor/model-latest[1m]")
        let body = try #require(hub.sent("POST", "/api/v1/sessions/box/s1/modes").first?.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: String] == ["model": "~vendor/model-latest[1m]"])
        #expect(SessionModes.launcher(try session()) == "claude")
        #expect(SessionModes.launcher(try session(#","launcher":"cc_gateway""#)) == "cc_gateway")
    }

    /// The composer menus that type `/model` (the shell's, BeautifulUI's): "Other Model…" opens the field instead (its
    /// ID goes through …/modes, never as a message), the running model does nothing, another row is typed.
    @MainActor @Test func theComposerMenuOffersOtherModelAsAnAction() {
        #expect(!PromptItems.claudeModels.contains { $0.id == PromptItems.otherModel.id })
        #expect(PromptItems.modelPick(PromptItems.otherModel.id, running: "claude-opus-5-5") == .typeID)
        #expect(PromptItems.modelPick(PromptItems.otherModel.id, running: "vendor/model-x") == .typeID)
        #expect(PromptItems.modelPick("opus", running: "claude-opus-5-5") == .keep)
        #expect(PromptItems.modelPick("sonnet", running: "claude-opus-5-5") == .send("/model sonnet"))
        #expect(PromptItems.modelPick("default", running: nil) == .keep)
        #expect(PromptItems.modelPick("default", running: "vendor/model-x") == .send("/model default"))
        #expect(PromptItems.modelPick("vendor/model-x", running: "vendor/model-x") == .keep)
    }
}
