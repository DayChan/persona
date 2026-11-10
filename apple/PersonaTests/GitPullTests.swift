import Foundation
import Testing
@testable import Persona

@MainActor
struct GitPullTests {
    private static let status = #"{"is_repo":true,"root":"/work/sample","branch":"main","ahead":0,"behind":0,"files":[],"stashes":1,"commits":[]}"#

    @Test func postsDirectoryAndDecodesFreshStatus() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/git/pull") { request in
            let body = try JSONSerialization.jsonObject(with: request.body!) as! [String: String]
            #expect(body == ["cwd": "/work/sample"])
            #expect(request.query.isEmpty)
            return Data("{\"status\":\(Self.status),\"updated\":true}".utf8)
        }
        let result = try await hub.client.gitPull(machine: "mac", cwd: "/work/sample")
        #expect(result.updated)
        #expect(result.status.branch == "main")
        #expect(result.status.stashes == 1)
        #expect(hub.sent("POST", "/api/v1/machines/mac/git/pull").count == 1)
    }

    /// A deleted upstream (#1884) comes as `gone`; an older node leaves it out.
    @Test func decodesDeletedUpstream() throws {
        let gone = #"{"is_repo":true,"branch":"feature","upstream":"origin/feature","gone":true,"ahead":0,"behind":0,"files":[],"stashes":0,"commits":[]}"#
        #expect(try JSON.decoder.decode(GitStatus.self, from: Data(gone.utf8)).gone == true)
        #expect(try JSON.decoder.decode(GitStatus.self, from: Data(Self.status.utf8)).gone == nil)
    }

    @Test func reportsUpdatedAndAlreadyCurrent() async throws {
        let hub = FakeHub()
        let model = GitPullModel()
        hub.on("POST", "/api/v1/machines/mac/git/pull", json: "{\"status\":\(Self.status),\"updated\":true}")
        let status = await model.pull(machine: "mac", cwd: "/work/sample", deps: .init(api: hub.client))
        #expect(status?.isRepo == true)
        #expect(model.message == "已拉取最新代码")
        #expect(!model.failed && !model.isPulling)
        hub.on("POST", "/api/v1/machines/mac/git/pull", json: "{\"status\":\(Self.status),\"updated\":false}")
        _ = await model.pull(machine: "mac", cwd: "/work/sample", deps: .init(api: hub.client))
        #expect(model.message == "已经是最新代码")
    }

    @Test func failureIsVisibleAndCanRetry() async throws {
        let hub = FakeHub()
        // as the hub answers a refusal (#1888): a 409 in the app's language, the node's words as the detail
        hub.on("POST", "/api/v1/machines/mac/git/pull") { _ in
            throw APIError(status: 409, message: "工作区有未提交的改动，请先在电脑上提交或 stash，再拉取。", fields: ["code": "uncommitted_changes"])
        }
        let model = GitPullModel()
        let failed = await model.pull(machine: "mac", cwd: "/work/sample", deps: .init(api: hub.client))
        #expect(failed == nil)
        #expect(model.failed && !model.isPulling)
        #expect(model.message?.contains("未提交") == true)
        hub.on("POST", "/api/v1/machines/mac/git/pull", json: "{\"status\":\(Self.status),\"updated\":true}")
        _ = await model.pull(machine: "mac", cwd: "/work/sample", deps: .init(api: hub.client))
        #expect(!model.failed && model.message == "已拉取最新代码")
    }

    @Test func ignoresSecondTapWhilePulling() async throws {
        let model = GitPullModel()
        var release: CheckedContinuation<GitPullResult, Never>?
        var calls = 0
        let deps = GitPullModel.Deps { _, _ in
            calls += 1
            return await withCheckedContinuation { release = $0 }
        }
        let task = Task { await model.pull(machine: "mac", cwd: "/work/sample", deps: deps) }
        for _ in 0..<100 where release == nil { await Task.yield() }
        let continuation = try #require(release)
        #expect(model.isPulling)
        #expect(model.message == nil)
        let duplicate = await model.pull(machine: "mac", cwd: "/work/sample", deps: deps)
        #expect(duplicate == nil && calls == 1)
        continuation.resume(returning: GitPullResult(status: try JSON.decoder.decode(GitStatus.self, from: Data(Self.status.utf8)), updated: true))
        _ = await task.value
        #expect(!model.isPulling && calls == 1)
    }
}
