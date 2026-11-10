import Foundation
import Testing
@testable import Persona

/// Renaming, moving and trashing in the file browser (docs/protocol/protocol.md "Changing files", #315): what is sent, and
/// what the user is told.
@MainActor
struct FolderEditsTests {
    private func body(_ req: LocalRequest) throws -> [String: Any] {
        try #require(JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any])
    }

    @Test func sendsTheRequestsTheHubTakes() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/m1/files/rename", json: #"{"path": "~/proj/b.txt"}"#)
        hub.on("POST", "/api/v1/machines/m1/files/move", json: #"{"moved": [{"from": "~/proj/b.txt", "to": "~/d/b.txt"}]}"#)
        hub.on("POST", "/api/v1/machines/m1/files/trash", json: #"{"trashed": [{"path": "~/d/b.txt", "trash_id": "t1"}]}"#)
        hub.on("POST", "/api/v1/machines/m1/files/restore", json: #"{"restored": [{"path": "~/d/b.txt"}]}"#)
        let api = hub.client

        #expect(try await api.renameFile(machine: "m1", path: "~/proj/a.txt", cwd: "/w", name: "b.txt") == "~/proj/b.txt")
        let rename = try body(try #require(hub.sent("POST", "/api/v1/machines/m1/files/rename").first))
        #expect(rename["path"] as? String == "~/proj/a.txt" && rename["cwd"] as? String == "/w" && rename["name"] as? String == "b.txt")

        let moved = try await api.moveFiles(machine: "m1", paths: ["~/proj/b.txt"], cwd: "/w", dir: "~/d", conflict: "keep")
        #expect(moved.map(\.to) == ["~/d/b.txt"])
        let move = try body(try #require(hub.sent("POST", "/api/v1/machines/m1/files/move").first))
        #expect(move["paths"] as? [String] == ["~/proj/b.txt"] && move["dir"] as? String == "~/d" && move["conflict"] as? String == "keep")

        let trashed = try await api.trashFiles(machine: "m1", paths: ["~/d/b.txt"], cwd: "/w")
        #expect(trashed.map(\.trashID) == ["t1"])
        #expect(try await api.restoreFiles(machine: "m1", trashIDs: ["t1"]) == ["~/d/b.txt"])
        let restore = try body(try #require(hub.sent("POST", "/api/v1/machines/m1/files/restore").first))
        #expect(restore["trash_ids"] as? [String] == ["t1"])
    }

    @Test func movesWithoutAPolicyLeaveItOut() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/m1/files/move", json: #"{"moved": []}"#)
        _ = try await hub.client.moveFiles(machine: "m1", paths: ["a"], cwd: "", dir: "~")
        let move = try body(try #require(hub.sent("POST", "/api/v1/machines/m1/files/move").first))
        #expect(move["conflict"] == nil)
    }

    @Test func namesAreCheckedBeforeAsking() {
        #expect(FolderEdits.nameProblem("notes.txt") == nil)
        #expect(FolderEdits.nameProblem(".env") == nil)
        for bad in ["", "  ", ".", "..", "a/b"] {
            #expect(FolderEdits.nameProblem(bad) != nil, "\(bad)")
        }
    }

    @Test func refusalsInTheUsersWords() {
        let readOnly = APIError(status: 409, message: "x is read-only", fields: ["code": "read_only"])
        #expect(FolderEdits.message(readOnly).contains("~/.persona"))
        let exists = APIError(status: 409, message: "taken", fields: ["code": "exists", "name": "a.txt"])
        #expect(FolderEdits.message(exists).contains("a.txt"))
        let outdated = APIError(status: 409, message: "personad on box is too old", fields: ["code": "node_outdated"])
        #expect(FolderEdits.message(outdated) == "personad on box is too old")
        let oldHub = APIError(status: 404, message: "404 page not found")
        #expect(FolderEdits.message(oldHub) != oldHub.message)
        let gone = APIError(status: 404, message: "~/a is not there", fields: ["code": "not_found"])
        #expect(FolderEdits.message(gone) != gone.message)
        let other = APIError(status: 502, message: "~/a is on another disk", fields: ["code": "internal"])
        #expect(FolderEdits.message(other).contains("HTTP 502"))
        #expect(!FolderEdits.message(other).contains("~/a"))
    }

    @Test func folderNames() {
        #expect(FolderEdits.folderName("~") == "~")
        #expect(FolderEdits.folderName("~/proj/") == "proj")
        #expect(FolderEdits.folderName("/") == "/")
        #expect(FolderEdits.folderName("/Users/me/Downloads") == "Downloads")
    }
}
