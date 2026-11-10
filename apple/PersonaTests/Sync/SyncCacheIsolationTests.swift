import Foundation
import Testing
@testable import Persona

/// Different simulated phones and runs can use the same hub/session/history. Their disk must still be their own.
@MainActor
@Suite(.serialized)
struct SyncCacheIsolationTests {
    private let key = SessionKey(machineID: "m1", sessionID: "s1")
    private let root = FileManager.default.temporaryDirectory.appending(path: "sync-cache-\(UUID().uuidString)")

    private func items(_ values: [JSONValue]) throws -> [Item] {
        try JSON.decoder.decode([Item].self, from: JSONEncoder().encode(JSONValue.array(values)))
    }

    /// Finish an old writer strictly AFTER removal, without relying on its two-second debounce or scheduler timing.
    /// Reusing its disk reproduces both extra transcript rows and an unacknowledged pending copy: a repeated item ID
    /// updates metadata, but only newly added items acknowledge pending messages.
    @Test(arguments: ["sent_on_other_device_once", "restart_resumable_send_in_flight"])
    func aLateWriterCannotSeedTheNextRun(_ name: String) throws {
        defer { try? FileManager.default.removeItem(at: root) }
        let c = try #require(SyncScenarioTests.cases[name])
        let start = try items(try #require(c["start"]?["hub"]?["lines"]?.array))
        var appended = try items((c["steps"]?.array ?? []).flatMap { $0["hub"]?["append"]?.array ?? [] })
        #expect(start.count == 2)
        #expect(appended.count == 2)
        appended[0].clientMsgID = UUID().uuidString
        let old = AppModel(stateFolder: root.appending(path: "old"))
        let oldFolder = old.stateFolder
        let nextFolder = root.appending(path: "next")
        let completeOldWrite = {
            ChatCache.save(self.key, items: start + appended, hasMore: false, history: "h1", covered: 6, in: oldFolder)
        }
        ChatCache.remove(key, in: oldFolder)
        ChatCache.remove(key, in: nextFolder)
        #expect(ChatCache.load(key, in: oldFolder) == nil)
        completeOldWrite()

        // The former shared-disk setup: removal alone cannot stop a previously queued writer from filling it again.
        let shared = AppModel(stateFolder: oldFolder).chat(key)
        shared.apply(start, span: ItemSpan(history: "h1", from: nil, to: 3), backlog: true)
        #expect(shared.items.count == 4)
        #expect(shared.covered == 6)

        let next = AppModel(stateFolder: nextFolder).chat(key)
        #expect(next.items.isEmpty)
        #expect(next.covered == 0)
        next.apply(start, span: ItemSpan(history: "h1", from: nil, to: 3), backlog: true)
        #expect(next.items == start) // also the complete expected transcript in both_devices_answer_once
        #expect(next.covered == 3)

        let pending = PendingMessage(text: try #require(appended.first?.text), images: [])
        shared.pending = [pending]
        next.pending = [pending]
        appended[0].clientMsgID = pending.id.uuidString
        shared.apply(appended, span: ItemSpan(history: "h1", from: 3, to: 6))
        next.apply(appended, span: ItemSpan(history: "h1", from: 3, to: 6))
        #expect(shared.pending.map(\.id) == [pending.id]) // the old setup's duplicate, even with new receipt metadata
        #expect(next.pending.isEmpty)
        #expect(next.items == start + appended)
    }

    @Test func devicesHaveIndependentCachesAndRelaunchTheirOwnCopy() throws {
        defer { try? FileManager.default.removeItem(at: root) }
        let c = try #require(SyncScenarioTests.cases["sent_on_other_device_once"])
        let start = try items(try #require(c["start"]?["hub"]?["lines"]?.array))
        let appended = try items((c["steps"]?.array ?? []).flatMap { $0["hub"]?["append"]?.array ?? [] })
        let a = SyncDevice(name: "a", key: key, folder: root.appending(path: "a"))
        let b = SyncDevice(name: "b", key: key, folder: root.appending(path: "b"))
        ChatCache.save(key, items: start + appended, hasMore: true, history: "h1", covered: 6, in: a.folder)
        ChatCache.save(key, items: start, hasMore: false, history: "h1", covered: 3, in: b.folder)
        #expect(a.model.chat(key).items == start + appended)
        #expect(b.model.chat(key).items == start)
        a.relaunch()
        b.relaunch()
        #expect(a.model.chat(key).covered == 6)
        #expect(a.model.chat(key).hasMore)
        #expect(b.model.chat(key).covered == 3)
        #expect(!b.model.chat(key).hasMore)
        ChatCache.removeGroup(key.group, in: b.folder)
        #expect(ChatCache.load(key, in: b.folder) == nil)
        #expect(ChatCache.load(key, in: a.folder)?.items == start + appended)
        ChatCache.remove(key, in: a.folder)
        #expect(ChatCache.load(key, in: a.folder) == nil)
    }

    @Test func defaultCacheRetainsItsPathAndFormat() throws {
        let unique = SessionKey(machineID: "cache-path", sessionID: UUID().uuidString)
        defer { ChatCache.remove(unique) }
        let c = try #require(SyncScenarioTests.cases["both_devices_answer_once"])
        let start = try items(try #require(c["start"]?["hub"]?["lines"]?.array))
        ChatCache.save(unique, items: start, hasMore: false, history: "h1", covered: 3)
        let base = try #require(FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first)
        let file = base.appending(path: ChatCache.folder).appending(path: "\(unique.machineID)_\(unique.sessionID).json")
        let data = try Data(contentsOf: file)
        let entry = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(Set(entry.keys) == ["items", "hasMore", "history", "covered"])
        #expect(ChatStore(key: unique).items == start)
        #expect(ChatStore(key: unique, cacheFolder: nil).covered == 3)
    }
}
