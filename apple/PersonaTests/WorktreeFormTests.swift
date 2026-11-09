import Foundation
import Testing
@testable import Persona

/// New Worktree…'s form (`WorktreeForm`, docs/integrations/terminal.md「新建 worktree…」), the same cases as the web's worktree.test.ts.
struct WorktreeFormTests {
    @Test func creationRetriesKeepTheirReceiptUntilTheTargetOrParametersChange() {
        var attempt = WorktreeForm.CreationAttempt()
        let body: [String: JSONValue] = ["workspace_id": .string("source"), "auto_name": .bool(true)]
        let first = attempt.operationID(target: "machine", body: body)
        #expect(attempt.operationID(target: "machine", body: body) == first)
        var changed = body
        changed["parent_workspace_id"] = .string("elsewhere")
        let next = attempt.operationID(target: "machine", body: changed)
        #expect(next != first)
        #expect(attempt.operationID(target: "machine", body: changed) == next)
        #expect(attempt.operationID(target: "other-hub|machine", body: changed) != next)
    }

    @MainActor @Test func aLostCreationResponseCanBeRecoveredWithTheSameOperationID() async throws {
        let transport = LostCreationResponse()
        let api = APIClient(config: .demo, transport: { try await transport.reply($0) })
        var attempt = WorktreeForm.CreationAttempt()
        let body: [String: JSONValue] = ["workspace_id": .string("source"), "auto_name": .bool(true)]
        var recovered = 0
        for _ in 0..<2 {
            do {
                let data = try await api.nodeAction(machine: "machine", "worktree/create", body,
                                                    opID: attempt.operationID(target: "machine", body: body))
                #expect(try JSON.decoder.decode(WorktreeCreated.self, from: data).workspaceID == "made")
                recovered += 1
            } catch { #expect(error is URLError) }
        }
        let requests = await transport.requests
        #expect(recovered == 1)
        #expect(requests.count == 2)
        #expect(requests[0].headers == requests[1].headers)
        #expect(requests[0].headers[OpRetry.header] != nil)
    }

    private actor LostCreationResponse {
        var requests: [LocalRequest] = []
        func reply(_ request: LocalRequest) throws -> Data {
            requests.append(request)
            if requests.count == 1 { throw URLError(.networkConnectionLost) }
            return Data(#"{"workspace_id":"made","path":"/wt/made","branch":"generated"}"#.utf8)
        }
    }

    @Test func namesTheFolderTheWayHerdrDoes() {
        #expect(WorktreeForm.slug("Feat/A_b.c") == "feat-a-b-c")
        #expect(WorktreeForm.slug("feat/2431-worktree") == "feat-2431-worktree")
        #expect(WorktreeForm.slug("fix/中文") == "fix")
        #expect(WorktreeForm.slug("  --a//b--") == "a-b")
        #expect(WorktreeForm.slug("中文") == "")
    }

    @Test func thePathIsTheBranchsFolderUnderTheRepositorysDirectory() {
        #expect(WorktreeForm.suggestedPath(base: "/srv/wt/proj", branch: "feat/x") == "/srv/wt/proj/feat-x")
        #expect(WorktreeForm.suggestedPath(base: "/srv/wt/proj/", branch: "feat/x") == "/srv/wt/proj/feat-x")
        #expect(WorktreeForm.suggestedPath(base: "C:\\wt\\proj", branch: "feat/x") == "C:\\wt\\proj\\feat-x")
        #expect(WorktreeForm.suggestedPath(base: "/srv/wt/proj", branch: "中文") == "")
        #expect(WorktreeForm.suggestedPath(base: "", branch: "feat/x") == "")
    }

    @Test func takesWhatGitTakes() {
        for ok in ["feat/x", "fix-1", "release/1.2.3", "a@b", "feat/中文", "  spare  "] {
            #expect(WorktreeForm.branchProblem(ok) == nil, "\(ok)")
        }
    }

    @Test func tellsAnEmptyNameThenWhatGitWouldRefuse() {
        #expect(WorktreeForm.branchProblem("  ") == "请填写分支名")
        for bad in ["-x", "a..b", "a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "a@{b", "@", "/a", "a/", "a//b", "a.",
                    "a.lock", ".a", "a/.b", "a.lock/b"] {
            #expect(WorktreeForm.branchProblem(bad) != nil, "\(bad)")
        }
    }

    @Test func thePathFollowsTheBranchUntilEditedAndAgainWhenEmptied() {
        let base = "/srv/wt/proj"
        var f = WorktreeForm.typeBranch(.init(), base: base, "feat/x")
        #expect(f == .init(branch: "feat/x", path: "/srv/wt/proj/feat-x", pathEdited: false))
        f = WorktreeForm.typeBranch(f, base: base, "feat/xy ")
        #expect(f.path == "/srv/wt/proj/feat-xy")
        f = WorktreeForm.typePath(f, base: base, "/elsewhere/mine")
        f = WorktreeForm.typeBranch(f, base: base, "feat/other")
        #expect(f == .init(branch: "feat/other", path: "/elsewhere/mine", pathEdited: true))
        f = WorktreeForm.typePath(f, base: base, "")
        #expect(f == .init(branch: "feat/other", path: "/srv/wt/proj/feat-other", pathEdited: false))
    }

    @Test func isSentOnlyWithAGoodBranchAndAPath() {
        #expect(WorktreeForm.problem(.init()) == "请填写分支名")
        #expect(WorktreeForm.problem(.init(branch: "a..b", path: "/p", pathEdited: true)) != nil)
        #expect(WorktreeForm.problem(.init(branch: "中文", path: "", pathEdited: false)) == "请填写检出路径")
        #expect(WorktreeForm.problem(.init(branch: "feat/x", path: "/p/x", pathEdited: false)) == nil)
    }

    @Test func automaticNamesRequireTheHostsCapabilityAndNoExplicitPath() {
        #expect(WorktreeForm.problem(.init(), automaticName: true) == nil)
        #expect(WorktreeForm.usesAutomaticName(.init(branch: "  "), supported: true))
        #expect(!WorktreeForm.usesAutomaticName(.init(), supported: false))
        #expect(WorktreeForm.problem(.init(), automaticName: false) != nil)
        #expect(WorktreeForm.problem(.init(path: "/custom", pathEdited: true), automaticName: true) != nil)
        #expect(WorktreeForm.problem(.init(branch: "a..b", path: "/custom", pathEdited: true), automaticName: true) != nil)
        #expect(WorktreeForm.problem(.init(branch: "feature/x", path: "/custom", pathEdited: true), automaticName: true) == nil)
    }

    @Test func olderPrepareResponsesNeverEnableAutomaticNaming() throws {
        let old = try JSON.decoder.decode(WorktreePrepare.self, from: Data(#"{"repoName":"app","path":"/wt"}"#.utf8))
        let current = try JSON.decoder.decode(WorktreePrepare.self, from: Data(#"{"repoName":"app","path":"/wt","automatic_name":true}"#.utf8))
        #expect(!old.automaticName && current.automaticName)
    }

    @Test func anOlderComputerIsToldToUpdate() {
        #expect(WorktreeForm.describe(APIError(status: 404, message: "not found"), what: "没能新建 worktree") == WorktreeForm.needsUpdate)
        #expect(WorktreeForm.describe(APIError(status: 502, message: "unknown method worktree.create"), what: "x") == WorktreeForm.needsUpdate)
        #expect(WorktreeForm.describe(APIError(status: 502, message: "fatal: 'a' is already used by worktree"), what: "没能新建 worktree")
            == "没能新建 worktree：fatal: 'a' is already used by worktree")
    }

    @Test func theRequestNamesTheWorkspaceAndTheHost() throws {
        #expect(WorktreeForm.body(workspaceID: "w1", kind: "herdr") == ["workspace_id": "w1", "kind": "herdr"])
        #expect(WorktreeForm.body(workspaceID: "w1", kind: nil) == ["workspace_id": "w1"])
        let r = try JSON.decoder.decode(WorktreePrepare.self, from: Data(#"{"root":"/r","repoName":"persona","branch":"","path":"/wt/persona"}"#.utf8))
        #expect(r == WorktreePrepare(root: "/r", repoName: "persona", branch: "", path: "/wt/persona"))
        let c = try JSON.decoder.decode(WorktreeCreated.self, from: Data(#"{"workspace_id":"w3","tab_id":"w3:t1","path":"/wt/persona/feat-x","branch":"feat/x"}"#.utf8))
        #expect(c.workspaceID == "w3" && c.path == "/wt/persona/feat-x")
    }
}
