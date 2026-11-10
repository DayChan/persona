import Foundation
import Observation

/// Recordings are not a cache to clear. Each gets its own directory and the index is written atomically; shared by phone and watch.
@Observable
final class RecordingLibrary {
    static let shared = RecordingLibrary()
    nonisolated static let directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("Recordings", isDirectory: true)
    private(set) var entries: [RecordingEntry] = []
    private(set) var error: String?
    @ObservationIgnored nonisolated let root: URL
    @ObservationIgnored private var deleted: Set<UUID> = []
    /// Called after a recording is saved (the phone syncs its text to the knowledge base from here).
    @ObservationIgnored var didChange: ((UUID) -> Void)?

    init(root: URL = RecordingLibrary.directory) {
        self.root = root
        do {
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            if let data = try? Data(contentsOf: root.appendingPathComponent("deleted.json")),
               let ids = try? JSONDecoder().decode(Set<UUID>.self, from: data) { deleted = ids }
            let dirs = try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)
            entries = dirs.compactMap { dir in
                guard UUID(uuidString: dir.lastPathComponent) != nil,
                      let data = try? Data(contentsOf: dir.appendingPathComponent("recording.json")),
                      let entry = try? JSONDecoder().decode(RecordingEntry.self, from: data),
                      entry.id.uuidString == dir.lastPathComponent else { return nil }
                return entry
            }.sorted { $0.createdAt > $1.createdAt }
        } catch { self.error = String(localized: "Can't read the recordings folder") }
    }

    nonisolated func folder(_ id: UUID) -> URL { root.appendingPathComponent(id.uuidString, isDirectory: true) }
    nonisolated func audio(_ id: UUID, _ index: Int) -> URL { folder(id).appendingPathComponent(RecordingSegment.fileName(index)) }
    func entry(_ id: UUID) -> RecordingEntry? { entries.first { $0.id == id } }

    @discardableResult func create(source: RecordingEntry.Source, locale: String = "zh-CN", title: String = "",
                                   meeting: RecordingMeeting? = nil) throws -> RecordingEntry {
        var entry = RecordingEntry(id: UUID(), createdAt: .now, source: source, locale: locale)
        entry.title = title
        entry.meeting = meeting
        try save(entry)
        return entry
    }

    func save(_ entry: RecordingEntry) throws {
        guard !deleted.contains(entry.id) else { throw CocoaError(.fileNoSuchFile) }
        var entry = entry
        // Strictly later than the copy it replaces, even within one millisecond or after the clock went back.
        let floor = (self.entry(entry.id)?.updatedAt ?? entry.updatedAt ?? entry.createdAt).addingTimeInterval(0.001)
        entry.updatedAt = max(Date.now, floor)
        try FileManager.default.createDirectory(at: folder(entry.id), withIntermediateDirectories: true)
        try JSONEncoder().encode(entry).write(to: folder(entry.id).appendingPathComponent("recording.json"), options: .atomic)
        entries.removeAll { $0.id == entry.id }
        entries.append(entry)
        entries.sort { $0.createdAt > $1.createdAt }
        error = nil
        didChange?(entry.id)
    }

    #if DEBUG
    /// Screenshots and UI tests put the same made-up recording back after a run deleted it.
    func restore(_ id: UUID) {
        guard deleted.remove(id) != nil else { return }
        try? JSONEncoder().encode(deleted).write(to: root.appendingPathComponent("deleted.json"), options: .atomic)
    }
    #endif

    /// Whether this id was deleted here (its tombstone stops late watch segments from recreating it).
    func isDeleted(_ id: UUID) -> Bool { deleted.contains(id) }

    func change(_ id: UUID, _ edit: (inout RecordingEntry) -> Void) {
        guard var entry = entry(id) else { return }
        edit(&entry)
        do { try save(entry) } catch { self.error = String(localized: "The recording info wasn't saved. Keep the app installed and try again.") }
    }

    func addSegment(_ id: UUID, _ segment: RecordingSegment) {
        change(id) { entry in
            guard !entry.segments.contains(where: { $0.index == segment.index }) else { return }
            entry.segments.append(segment)
            entry.segments.sort { $0.index < $1.index }
        }
    }

    /// The end message and the audio can arrive in any order; a repeated message never overwrites a transcribed segment.
    func receive(_ transfer: RecordingTransfer) throws {
        guard transfer.valid else { throw CocoaError(.fileReadCorruptFile) }
        // A watch segment arriving late or retried after a delete must not recreate the deleted recording, but is still acknowledged so the watch cleans up.
        if deleted.contains(transfer.id) {
            try? FileManager.default.removeItem(at: folder(transfer.id))
            return
        }
        var entry = entry(transfer.id) ?? RecordingEntry(id: transfer.id, createdAt: transfer.createdAt, source: .watch, locale: transfer.locale)
        entry.marks = Array(Set(entry.marks + transfer.marks)).sorted()
        if var segment = transfer.segment, !entry.segments.contains(where: { $0.index == segment.index }) {
            segment.sentences = nil; segment.delivered = false
            entry.segments.append(segment)
            entry.segments.sort { $0.index < $1.index }
        }
        if let total = transfer.total { entry.active = false; entry.expectedSegments = total }
        try save(entry)
    }

    func remove(_ id: UUID) throws {
        var next = deleted; next.insert(id)
        try JSONEncoder().encode(next).write(to: root.appendingPathComponent("deleted.json"), options: .atomic)
        deleted = next
        if FileManager.default.fileExists(atPath: folder(id).path) { try FileManager.default.removeItem(at: folder(id)) }
        entries.removeAll { $0.id == id }
    }

    /// Hourhand's cleanup after an abnormal exit: keep only finalized segments, delete half-written ones with no tail.
    func recover(source: RecordingEntry.Source) {
        for var entry in entries where entry.active && entry.source == source {
            let known = Set(entry.segments.map { RecordingSegment.fileName($0.index) })
            let files = (try? FileManager.default.contentsOfDirectory(at: folder(entry.id), includingPropertiesForKeys: nil)) ?? []
            for file in files where file.pathExtension == "m4a" && !known.contains(file.lastPathComponent) {
                try? FileManager.default.removeItem(at: file)
            }
            entry.active = false
            entry.expectedSegments = (entry.segments.map(\.index).max() ?? -1) + 1
            do { try save(entry) } catch { self.error = String(localized: "The end of the last recording wasn't saved") }
        }
    }
}
