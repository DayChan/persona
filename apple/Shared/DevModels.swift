import Foundation

// Git / Files models and calls for a session's project (hub → node `git.*` / `fs.list`).

nonisolated struct GitCommit: Codable, Identifiable, Hashable, Sendable {
    var hash: String
    var short: String
    var subject: String
    var body: String?
    var author: String
    var date: Date?
    var refs: String?
    var id: String { hash }

    /// Branch / tag names pointing here ("HEAD -> master, origin/master" → ["master", "origin/master"]).
    var refNames: [String] {
        (refs ?? "").split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }
            .map { $0.hasPrefix("HEAD -> ") ? String($0.dropFirst(8)) : $0 }
            .filter { !$0.isEmpty && $0 != "HEAD" }
    }
}

nonisolated struct GitFile: Codable, Identifiable, Hashable, Sendable {
    var path: String
    var origPath: String?
    var index: String
    var worktree: String
    var added: Int
    var deleted: Int
    var binary: Bool?
    var id: String { path }

    var isUntracked: Bool { index == "?" }
    var isConflicted: Bool { index == "U" || worktree == "U" }
    var isStaged: Bool { !isUntracked && index != "." }
    var hasUnstaged: Bool { !isUntracked && worktree != "." }
    /// The one letter shown in the change list: staged change wins, then the working-tree change.
    var letter: String {
        if isUntracked { return "U" }
        if isConflicted { return "!" }
        return index != "." ? index : worktree
    }
    var name: String { (path as NSString).lastPathComponent }
    var folder: String { (path as NSString).deletingLastPathComponent }

    enum CodingKeys: String, CodingKey {
        case path, index, worktree, added, deleted, binary
        case origPath = "orig_path"
    }
}

nonisolated struct GitStatus: Codable, Hashable, Sendable {
    var isRepo: Bool
    var root: String?
    var branch: String?
    var head: GitCommit?
    var upstream: String?
    var ahead: Int
    var behind: Int
    var files: [GitFile]
    var stashes: Int
    var commits: [GitCommit]
    /// The upstream was deleted on the remote (a merged pull request's branch): ahead and behind are 0 and mean
    /// nothing. Older nodes leave it out.
    var gone: Bool?

    enum CodingKeys: String, CodingKey {
        case root, branch, head, upstream, ahead, behind, files, stashes, commits, gone
        case isRepo = "is_repo"
    }
}

nonisolated struct GitPullResult: Codable, Hashable, Sendable {
    var status: GitStatus
    var updated: Bool
}

nonisolated struct GitDiff: Codable, Hashable, Sendable {
    var path: String
    var diff: String
    var truncated: Bool
}

nonisolated struct GitShow: Codable, Hashable, Sendable {
    var commit: GitCommit
    var files: [GitFile]
    var diff: String
    var truncated: Bool
}

nonisolated struct FileEntry: Codable, Identifiable, Hashable, Sendable {
    var name: String
    var dir: Bool
    var symlink: Bool?
    var size: Int64
    var mtime: Date?
    var id: String { name }
}

nonisolated struct FileListing: Codable, Hashable, Sendable {
    var path: String
    var entries: [FileEntry]
    var truncated: Bool
}

extension APIClient {
    private func mp(_ machine: String) -> String { "/api/v1/machines/\(machine)" }

    func gitStatus(machine: String, cwd: String) async throws -> GitStatus {
        try await get(mp(machine) + "/git/status", query: ["cwd": cwd])
    }
    private struct GitPullRequest: Encodable { var cwd: String }
    func gitPull(machine: String, cwd: String) async throws -> GitPullResult {
        try await post(mp(machine) + "/git/pull", body: GitPullRequest(cwd: cwd), timeout: 100)
    }
    func gitDiff(machine: String, cwd: String, path: String, staged: Bool) async throws -> GitDiff {
        try await get(mp(machine) + "/git/diff", query: ["cwd": cwd, "path": path, "staged": staged ? "1" : "0"])
    }
    func gitShow(machine: String, cwd: String, hash: String) async throws -> GitShow {
        try await get(mp(machine) + "/git/show", query: ["cwd": cwd, "hash": hash])
    }
    private struct CommitsResponse: Decodable { var commits: [GitCommit] }
    func gitLog(machine: String, cwd: String, skip: Int, limit: Int = 50) async throws -> [GitCommit] {
        try await get(mp(machine) + "/git/log", query: ["cwd": cwd, "skip": String(skip), "limit": String(limit)],
                      as: CommitsResponse.self).commits
    }
    func listDir(machine: String, path: String, cwd: String) async throws -> FileListing {
        try await get(mp(machine) + "/fs", query: ["path": path, "cwd": cwd])
    }

    // Rename, move, trash and put back (docs/protocol/protocol.md "Changing files"); paths as in listDir.
    private struct RenameRequest: Encodable { var path, cwd, name: String }
    private struct RenameResponse: Decodable { var path: String }
    /// Renames path in its folder; returns the new path.
    func renameFile(machine: String, path: String, cwd: String, name: String) async throws -> String {
        try await post(mp(machine) + "/files/rename", body: RenameRequest(path: path, cwd: cwd, name: name), as: RenameResponse.self).path
    }
    private struct MoveRequest: Encodable { var paths: [String]; var cwd, dir: String; var conflict: String? }
    struct MovedFile: Decodable, Sendable { var from, to: String }
    private struct MoveResponse: Decodable { var moved: [MovedFile]? }
    /// Moves paths into folder dir; conflict "replace" / "keep" answers a 409 `exists`.
    func moveFiles(machine: String, paths: [String], cwd: String, dir: String, conflict: String? = nil) async throws -> [MovedFile] {
        try await post(mp(machine) + "/files/move", body: MoveRequest(paths: paths, cwd: cwd, dir: dir, conflict: conflict),
                       timeout: 130, as: MoveResponse.self).moved ?? []
    }
    private struct TrashRequest: Encodable { var paths: [String]; var cwd: String }
    struct TrashedFile: Decodable, Sendable {
        var path: String
        var trashID: String
        enum CodingKeys: String, CodingKey { case path, trashID = "trash_id" }
    }
    private struct TrashResponse: Decodable { var trashed: [TrashedFile]? }
    /// Moves paths to the machine's trash; restoreFiles with the ids puts them back.
    func trashFiles(machine: String, paths: [String], cwd: String) async throws -> [TrashedFile] {
        try await post(mp(machine) + "/files/trash", body: TrashRequest(paths: paths, cwd: cwd), timeout: 130, as: TrashResponse.self).trashed ?? []
    }
    private struct RestoreRequest: Encodable {
        var trashIDs: [String]
        enum CodingKeys: String, CodingKey { case trashIDs = "trash_ids" }
    }
    private struct RestoreResponse: Decodable { var restored: [RenameResponse]? }
    func restoreFiles(machine: String, trashIDs: [String]) async throws -> [String] {
        try await post(mp(machine) + "/files/restore", body: RestoreRequest(trashIDs: trashIDs), timeout: 130, as: RestoreResponse.self)
            .restored?.map(\.path) ?? []
    }
}
