import Foundation
import Testing
@testable import Persona

@MainActor struct RecordingTranscriptionTests {
    @Test func uploadCarriesTextAndTimestampsOnly() throws {
        var entry = RecordingEntry(id: UUID(), createdAt: Date(timeIntervalSince1970: 1_800_000_000), source: .watch,
                                   title: "现场记录", active: false, expectedSegments: 1)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30,
            sentences: [RecordingSentence(start: 12, end: 16, text: "下周检查进度。"), RecordingSentence(start: 20, end: 21, text: "  ")])]
        entry.marks = [12]
        let body = RecordingUpload(entry)
        #expect(body.complete)
        #expect(body.device == "watch")
        #expect(body.segments.map(\.text) == ["下周检查进度。"]) // blank text dropped
        #expect(body.bookmarks.map(\.at) == [12])
        #expect(body.endedAt == entry.createdAt.addingTimeInterval(30))
        let json = String(decoding: try JSON.encoder.encode(body), as: UTF8.self)
        #expect(json.contains("\"started_at\""))
        #expect(!json.contains(".m4a"))
        #expect(!json.contains(entry.id.uuidString))
    }

    @Test func incompleteOrSilentRecordingIsNotUploadable() {
        var entry = RecordingEntry(id: UUID(), createdAt: .now, source: .phone, active: false, expectedSegments: 2)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [])]
        #expect(!RecordingSync.uploadable(entry))
        #expect(!RecordingUpload(entry).complete)
        entry.expectedSegments = 1
        #expect(entry.transcribedAll)
        #expect(!RecordingSync.uploadable(entry))
    }

    @Test func sentencesSplitOnPunctuationAndPause() {
        let raw = RecordingSentenceBuilder.build([
            (text: "第一句。", start: 0, end: 2), (text: "第二", start: 3, end: 4),
            (text: "句话", start: 4, end: 5), (text: "接下来", start: 8, end: 10),
        ])
        #expect(raw.map(\.text) == ["第一句。", "第二句话", "接下来"])
        #expect(raw.map(\.start) == [0, 3, 8])
    }

    @Test func resumesOnlyUnfinishedSegments() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        var entry = try lib.create(source: .phone)
        entry.active = false; entry.expectedSegments = 2
        entry.segments = [
            RecordingSegment(index: 0, start: 0, duration: 30, sentences: [RecordingSentence(start: 0, end: 3, text: "已保存")]),
            RecordingSegment(index: 1, start: 30, duration: 30),
        ]
        try lib.save(entry)
        try Data([0]).write(to: lib.audio(entry.id, 1))
        let runner = RecordingTranscription(library: lib) { url, _ in
            #expect(url.lastPathComponent == "seg-001.m4a")
            return [RecordingSentence(start: 2, end: 4, text: "继续")]
        }
        runner.enqueue(entry.id)
        for _ in 0..<100 where lib.entry(entry.id)?.transcribedAll != true { try await Task.sleep(for: .milliseconds(10)) }
        #expect(lib.entry(entry.id)?.sentences.map(\.text) == ["已保存", "继续"])
        #expect(lib.entry(entry.id)?.sentences.last?.start == 32)
        #expect(RecordingLibrary(root: root).entry(entry.id)?.transcribedAll == true)
    }

    /// A segment whose audio is gone gets no text, and the ones after it are still transcribed (a long recording used to
    /// stop at it for good).
    @Test func missingAudioDoesNotHoldUpTheRest() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        var entry = try lib.create(source: .phone)
        entry.active = false; entry.expectedSegments = 3
        entry.segments = (0..<3).map { RecordingSegment(index: $0, start: Double($0) * 60, duration: 60) }
        try lib.save(entry)
        try Data([0]).write(to: lib.audio(entry.id, 0))
        try Data([0]).write(to: lib.audio(entry.id, 2)) // segment 1's audio is missing
        let runner = RecordingTranscription(library: lib) { url, _ in [RecordingSentence(start: 1, end: 2, text: url.lastPathComponent)] }
        runner.enqueue(entry.id)
        for _ in 0..<100 where lib.entry(entry.id)?.transcribedAll != true { try await Task.sleep(for: .milliseconds(10)) }
        #expect(lib.entry(entry.id)?.transcribedAll == true)
        #expect(lib.entry(entry.id)?.sentences.map(\.text) == ["seg-000.m4a", "seg-002.m4a"])
        #expect(runner.errors[entry.id] == nil)
    }
}
