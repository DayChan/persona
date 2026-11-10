import Foundation
import Testing
@testable import Persona

/// The hub's recording sync (docs/protocol/protocol.md「Recording sync」) as far as the phone needs it: stores the latest revision,
/// remembers deletes (a later PUT gets 410), can be down (503).
private final class FakeRecordingHub: @unchecked Sendable {
    private let lock = NSLock()
    private var _stored: [String: Int64] = [:]
    private var _deleted: Set<String> = []
    private var _calls: [String] = []
    var down = false
    var days = 14
    var refusal: APIError?
    var writesOnly = false

    var stored: [String: Int64] { lock.withLock { _stored } }
    var calls: [String] { lock.withLock { _calls } }
    func forget(_ id: UUID) { lock.withLock { _ = _stored.removeValue(forKey: id.uuidString.lowercased()) } }
    func deleteThere(_ id: UUID) { lock.withLock { _deleted.insert(id.uuidString.lowercased()); _ = _stored.removeValue(forKey: id.uuidString.lowercased()) } }

    func answer(_ r: LocalRequest) throws -> Data {
        try lock.withLock {
            _calls.append("\(r.method) \(r.path)")
            if down { throw APIError(status: 503, message: "知识库暂时不可用") }
            if let refusal, !writesOnly || r.method != "GET" { throw refusal }
            if r.method == "GET", r.path == "/api/v1/recordings" {
                let list = _stored.map { ["id": $0.key, "revision": $0.value, "complete": true, "started_at": "2026-11-05T00:00:00Z"] as [String: Any] }
                return try JSONSerialization.data(withJSONObject: ["days": days, "recordings": list, "deleted": Array(_deleted)])
            }
            let id = String(r.path.split(separator: "/").last ?? "")
            if r.method == "DELETE" {
                _deleted.insert(id)
                let had = _stored.removeValue(forKey: id) != nil
                return Data("{\"deleted\":\(had)}".utf8)
            }
            if _deleted.contains(id) { throw APIError(status: 410, message: "deleted", fields: ["code": "recording_deleted"]) }
            let body = try JSONSerialization.jsonObject(with: r.body ?? Data()) as? [String: Any] ?? [:]
            let revision = (body["revision"] as? NSNumber)?.int64Value ?? 0
            if let have = _stored[id], have > revision {
                return Data("{\"id\":\"\(id)\",\"revision\":\(have),\"status\":\"stale\"}".utf8)
            }
            _stored[id] = revision
            return Data("{\"id\":\"\(id)\",\"revision\":\(revision),\"status\":\"stored\"}".utf8)
        }
    }
}

@MainActor struct RecordingSyncTests {
    private func api(_ hub: FakeRecordingHub) -> APIClient {
        APIClient(config: ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"), transport: { try hub.answer($0) })
    }

    private func setup(now: Date = .now) -> (RecordingLibrary, RecordingSync, FakeRecordingHub, URL) {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let lib = RecordingLibrary(root: root)
        let hub = FakeRecordingHub()
        let api = api(hub)
        return (lib, RecordingSync(library: lib, api: { api }, now: { now }), hub, root)
    }

    private func transcribed(_ lib: RecordingLibrary, createdAt: Date = .now, text: String = "下周检查进度。") throws -> RecordingEntry {
        var entry = RecordingEntry(id: UUID(), createdAt: createdAt, source: .phone, active: false, expectedSegments: 1)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [RecordingSentence(start: 1, end: 3, text: text)])]
        try lib.save(entry)
        return lib.entry(entry.id)!
    }

    @Test func uploadsOnceUntilItChanges() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(hub.stored[entry.id.uuidString.lowercased()] == entry.revision)
        #expect(sync.status(entry) == .synced)
        let puts = hub.calls.filter { $0.hasPrefix("PUT") }.count
        await sync.syncNow()
        #expect(hub.calls.filter { $0.hasPrefix("PUT") }.count == puts, "nothing new: nothing sent")
        // A new revision (another language, a title) goes again; the saved copy's revision only moves forward.
        lib.change(entry.id) { $0.title = "周会" }
        let changed = lib.entry(entry.id)!
        #expect(changed.revision > entry.revision)
        #expect(sync.status(changed) == .pending)
        await sync.syncNow()
        #expect(hub.stored[entry.id.uuidString.lowercased()] == changed.revision)
        // What was sent survives a relaunch.
        let again = RecordingSync(library: lib, api: { nil })
        #expect(again.status(changed) == .synced)
    }

    @Test func offlineQueuesAndRetries() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        hub.down = true
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(sync.status(entry) == .pending)
        #expect(sync.problem != nil)
        hub.down = false
        await sync.syncNow()
        #expect(sync.status(entry) == .synced)
        #expect(sync.problem == nil)
    }

    @Test func configurationRefusalsPauseWithoutLosingPendingUploads() async throws {
        for (status, fields) in [
            (503, ["code": "kb_disabled"]),
            (403, ["code": "kb_read_only"]),
            (503, ["code": "internal", "detail": "the knowledge base is off"])
        ] {
            let (lib, sync, hub, root) = setup()
            defer { try? FileManager.default.removeItem(at: root) }
            let entry = try transcribed(lib)
            hub.refusal = APIError(status: status, message: "Request failed", fields: fields)
            await sync.syncNow()
            #expect(sync.automaticRetryPaused)
            #expect(sync.status(entry) == .pending)
            #expect(hub.stored.isEmpty)
            #expect(sync.problem?.contains("hub") == false)
            let before = hub.calls.count
            lib.change(entry.id) { $0.title = "Updated title" }
            sync.kick()
            await sync.syncNow()
            #expect(hub.calls.count == before, "saving while disabled must not resume automatic requests")
            hub.refusal = nil
            await sync.syncNow(retryPaused: true)
            #expect(sync.status(lib.entry(entry.id)!) == .synced)
            #expect(!sync.automaticRetryPaused)
        }
    }

    @Test func pairingLaterDoesNotNeedAConfigurationRetry() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root), hub = FakeRecordingHub()
        var client: APIClient?
        let sync = RecordingSync(library: lib, api: { client })
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(!sync.automaticRetryPaused && sync.status(entry) == .pending)
        client = api(hub)
        await sync.syncNow()
        #expect(sync.status(entry) == .synced)
    }

    @Test func transportTimeoutKeepsUploadsPendingWithoutClaimingHubIsOffline() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let client = APIClient(config: ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"),
                               transport: { _ in throw URLError(.timedOut) })
        let sync = RecordingSync(library: lib, api: { client })
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(sync.status(entry) == .pending)
        #expect(!sync.automaticRetryPaused)
        #expect(sync.problem == String(localized: "The request timed out. Try again later."))
    }

    @Test func readOnlyAndUnsupportedDeletesStayPendingAndVisible() async throws {
        for refusal in [APIError(status: 403, message: "Read-only", fields: ["code": "kb_read_only"]),
                        APIError(status: 404, message: "Not found")] {
            let (lib, sync, hub, root) = setup()
            defer { try? FileManager.default.removeItem(at: root) }
            let entry = try transcribed(lib)
            await sync.syncNow()
            hub.refusal = refusal
            hub.writesOnly = true
            try sync.delete(entry.id)
            await sync.syncNow()
            #expect(sync.automaticRetryPaused)
            #expect(sync.store.deletes.contains(entry.id))
            #expect(hub.stored[entry.id.uuidString.lowercased()] != nil)
            #expect(sync.problem?.contains(String(localized: "Pending deletions have not been removed from Knowledge yet.")) == true)
            let relaunched = RecordingSync(library: lib, api: { nil })
            #expect(relaunched.store.deletes.contains(entry.id))
            hub.refusal = nil
            await sync.syncNow(retryPaused: true)
            #expect(sync.store.deletes.isEmpty)
            #expect(hub.stored.isEmpty)
        }
    }

    @Test func deleteHereDeletesThereEvenAfterBeingOffline() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        hub.down = true
        try sync.delete(entry.id)
        #expect(lib.entry(entry.id) == nil)
        await sync.syncNow()
        #expect(sync.store.deletes.contains(entry.id))
        hub.down = false
        await sync.syncNow()
        #expect(sync.store.deletes.isEmpty)
        #expect(hub.stored.isEmpty)
        #expect(hub.calls.contains("DELETE /api/v1/recordings/\(entry.id.uuidString.lowercased())"))
    }

    @Test func deletedInTheKnowledgeBaseGoesHereToo() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        hub.deleteThere(entry.id)
        lib.change(entry.id) { $0.title = "改名" } // the next upload gets 410
        await sync.syncNow()
        #expect(lib.entry(entry.id) == nil)
        #expect(lib.isDeleted(entry.id))
    }

    @Test func reconcileResendsWhatTheHubLost() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        hub.forget(entry.id) // a new hub
        let api = api(hub)
        let relaunched = RecordingSync(library: lib, api: { api }) // asks the hub what it has first
        #expect(relaunched.status(entry) == .synced)
        await relaunched.syncNow()
        #expect(hub.stored[entry.id.uuidString.lowercased()] == entry.revision)
    }

    @Test func expiredRecordingsAreDroppedWithTheirAudio() async throws {
        let now = Date.now
        let (lib, sync, hub, root) = setup(now: now)
        defer { try? FileManager.default.removeItem(at: root) }
        hub.days = 3
        let old = try transcribed(lib, createdAt: now.addingTimeInterval(-5 * 86_400))
        let fresh = try transcribed(lib, createdAt: now.addingTimeInterval(-86_400))
        try Data("aac".utf8).write(to: lib.audio(old.id, 0))
        await sync.syncNow()
        #expect(sync.store.days == 3)
        #expect(lib.entry(old.id) == nil)
        #expect(!FileManager.default.fileExists(atPath: lib.folder(old.id).path))
        #expect(lib.entry(fresh.id) != nil)
        #expect(hub.stored[old.id.uuidString.lowercased()] == nil)
        #expect(hub.stored[fresh.id.uuidString.lowercased()] != nil)
    }

    @Test func silentRecordingIsNotUploaded() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib, text: "  ")
        await sync.syncNow()
        #expect(sync.status(entry) == .waiting)
        #expect(!hub.calls.contains { $0.hasPrefix("PUT") })
    }
}
