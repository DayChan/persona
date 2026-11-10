import Foundation
import Observation

/// Saves transcripts segment by segment: after the app is killed, the next run picks up at the unfinished segment.
@Observable
final class RecordingTranscription {
    static let shared = RecordingTranscription()
    private(set) var current: UUID?
    private(set) var errors: [UUID: String] = [:]
    @ObservationIgnored private let library: RecordingLibrary
    @ObservationIgnored private let transcribe: @Sendable (URL, Locale) async throws -> [RecordingSentence]
    @ObservationIgnored private var worker: Task<Void, Never>?
    @ObservationIgnored private var queued: [UUID] = []

    init(library: RecordingLibrary = .shared,
         transcribe: @escaping @Sendable (URL, Locale) async throws -> [RecordingSentence] = RecordingTranscription.onDevice) {
        self.library = library; self.transcribe = transcribe
    }

    nonisolated static func onDevice(_ url: URL, _ locale: Locale) async throws -> [RecordingSentence] {
        guard let (engine, resolved) = await RecordingAnalyzerEngine.best(for: locale) else {
            throw RecordingSpeechError.unsupportedLanguage
        }
        return try await engine.transcribe(url, locale: resolved)
    }

    func enqueue(_ id: UUID) {
        guard library.entry(id) != nil else { return }
        errors[id] = nil
        if !queued.contains(id) { queued.append(id) }
        kick()
    }

    func resumePending() {
        for entry in library.entries where !entry.active && entry.segments.contains(where: { $0.sentences == nil }) {
            enqueue(entry.id)
        }
    }

    func cancel(_ id: UUID) {
        queued.removeAll { $0 == id }
        if current == id { worker?.cancel() }
    }

    func setLocale(_ id: UUID, _ locale: String) {
        guard current != id else { return }
        library.change(id) { entry in
            entry.locale = locale
            for index in entry.segments.indices { entry.segments[index].sentences = nil }
        }
        enqueue(id)
    }

    private func kick() {
        guard worker == nil else { return }
        worker = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled, !self.queued.isEmpty {
                let id = self.queued.removeFirst()
                self.current = id
                await self.process(id)
                self.current = nil
            }
            self.worker = nil
            self.kickIfNeeded()
        }
    }

    private func kickIfNeeded() { if !queued.isEmpty { kick() } }

    private func process(_ id: UUID) async {
        while !Task.isCancelled, let entry = library.entry(id),
              let segment = entry.segments.first(where: { $0.sentences == nil }) {
            // A segment whose audio is gone (a file the system removed, a broken transfer) has no text to give: it must
            // not hold up the rest of a long recording, as it did when every later segment waited behind it.
            if !FileManager.default.fileExists(atPath: library.audio(id, segment.index).path) {
                library.change(id) { e in
                    if let i = e.segments.firstIndex(where: { $0.index == segment.index }) { e.segments[i].sentences = [] }
                }
                // Not saved (no space, deleted meanwhile): stop here rather than try the same segment for ever.
                guard library.entry(id)?.segments.first(where: { $0.index == segment.index })?.sentences != nil else {
                    errors[id] = library.error ?? String(localized: "The recording info wasn't saved. Keep the app installed and try again.")
                    return
                }
                continue
            }
            do {
                let raw = try await transcribe(library.audio(id, segment.index), Locale(identifier: entry.locale))
                try Task.checkCancellation()
                guard let latest = library.entry(id), latest.locale == entry.locale else { return }
                var saved = latest
                guard let index = saved.segments.firstIndex(where: { $0.index == segment.index }) else { return }
                saved.segments[index].sentences = raw.map {
                    RecordingSentence(start: segment.start + $0.start, end: segment.start + $0.end, text: $0.text)
                }
                try library.save(saved)
            } catch is CancellationError { return }
            catch {
                errors[id] = error.localizedDescription
                return
            }
        }
    }
}
