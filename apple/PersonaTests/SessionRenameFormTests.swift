import Foundation
import Testing
@testable import Persona

@MainActor struct SessionRenameFormTests {
    private func session(host: String, agent: String) throws -> Session {
        let fields: [String: Any] = ["machine_id": "fixture", "session_id": "agent", "title": "Conversation",
                                    "cwd": "/code/project", "status": "idle", "pane_id": host + ":pane",
                                    "slot_name": "Tab", "agent": agent]
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: fields))
    }

    @Test func orcaAgentsHaveOneNameAndNeverSendADuplicateTabRename() throws {
        for agent in ["codex", "claude"] {
            let session = try session(host: "orca", agent: agent)
            #expect(!SessionRenameForm.showsTab(session))
            let body = SessionRenameForm.body(session, tab: "Changed tab", title: "New conversation")
            let json = try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(body)) as? [String: String])
            #expect(json == ["title": "New conversation"])
        }
    }

    @Test func legacyHerdrClaudeKeepsItsTwoIndependentNames() throws {
        let session = try session(host: "herdr", agent: "claude")
        #expect(SessionRenameForm.showsTab(session))
        let body = SessionRenameForm.body(session, tab: "New tab", title: session.title)
        #expect(body.tabName == "New tab" && body.title == nil)
        let unchanged = SessionRenameForm.body(session, tab: "Tab", title: session.title)
        #expect(unchanged.tabName == nil && unchanged.title == nil)
    }
}
