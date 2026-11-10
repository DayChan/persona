import Foundation
import Observation
#if canImport(UIKit)
import UIKit
#endif

/// The hub's side of a recording (docs/protocol/protocol.md「Recording sync」, #3235): what `PUT /api/v1/recordings/{id}` takes.
/// Text only: the audio never leaves the device.
nonisolated struct RecordingUpload: Encodable, Sendable {
    struct Bookmark: Encodable, Sendable { var at: Double; var label = "" }
    struct Segment: Encodable, Sendable { var start: Double; var end: Double; var text: String }
    /// The calendar event the device matched (docs/assistant/meeting-minutes.md「Field contract」): a hint for the hub, which
    /// prefers its own `meetings` / `events`. No event id: the hub can't see this calendar.
    struct CalendarHint: Encodable, Sendable {
        var title: String?
        var attendees: [String]?
        var location: String?
    }
    var revision: Int64
    var startedAt: Date
    var endedAt: Date?
    var duration: Double
    var language: String
    var device: String
    var title: String
    var bookmarks: [Bookmark]
    var segments: [Segment]
    var complete: Bool
    /// "meeting" (the hub writes it minutes) or "note".
    var kind: String
    /// Only when the device matched a calendar event.
    var calendar: CalendarHint?

    enum CodingKeys: String, CodingKey {
        case revision, duration, language, device, title, bookmarks, segments, complete, kind, calendar
        case startedAt = "started_at", endedAt = "ended_at"
    }

    init(_ entry: RecordingEntry) {
        revision = entry.revision
        startedAt = entry.createdAt
        endedAt = entry.active ? nil : entry.createdAt.addingTimeInterval(entry.duration)
        duration = entry.duration
        language = entry.locale
        device = entry.source.rawValue
        title = entry.title
        bookmarks = entry.marks.map { Bookmark(at: $0) }
        segments = entry.sentences.compactMap { s in
            let text = s.text.trimmingCharacters(in: .whitespacesAndNewlines)
            return text.isEmpty ? nil : Segment(start: s.start, end: s.end, text: text)
        }
        complete = entry.transcribedAll
        kind = entry.isMeeting ? "meeting" : "note"
        if let meeting = entry.meeting, meeting.eventID != nil || meeting.title != nil {
            calendar = CalendarHint(title: meeting.title, attendees: meeting.attendees.isEmpty ? nil : meeting.attendees,
                                    location: meeting.location)
        }
    }
}

/// Keeps the knowledge base's copy of each recording's transcript in step with this device: uploads every new revision
/// (partial transcripts too), deletes what is deleted here, retries with backoff while the hub is away, and drops
/// recordings (audio included) once they are older than the hub's retention (`days`, 14 by default).
/// What was sent is kept in `sync.json` beside the recordings, so a relaunch only sends what changed.
@Observable
final class RecordingSync {
    static let shared = RecordingSync()

    nonisolated struct Store: Codable, Equatable, Sendable {
        /// The revision the hub has (stored, or newer than ours: stale).
        var synced: [UUID: Int64] = [:]
        /// Revisions the hub refused (400): not sent again until the recording changes.
        var rejected: [UUID: Int64] = [:]
        /// Deleted here, the hub not told yet.
        var deletes: Set<UUID> = []
        /// The hub's retention in days (`GET /api/v1/recordings`).
        var days = RecordingSync.defaultDays
        /// `days` came from the hub at least once: until then nothing here is dropped for its age (the hub may keep
        /// recordings longer than the default).
        var daysKnown = false
    }

    enum Status: Equatable {
        /// Nothing to upload yet (no text).
        case waiting
        case pending
        case uploading
        case synced
        case rejected
        case expired
    }

    nonisolated static let defaultDays = 14

    private(set) var store: Store
    private(set) var uploading: UUID?
    /// Configuration refusals need a foreground/manual retry, not a timer or another transcript save.
    private(set) var automaticRetryPaused = false
    private var lastProblem: String?
    /// Pending deletes are durable and visible even after a relaunch; a local delete is not a remote acknowledgement.
    var problem: String? {
        guard !store.deletes.isEmpty else { return lastProblem }
        return [lastProblem, String(localized: "Pending deletions have not been removed from Knowledge yet.")].compactMap { $0 }.joined(separator: " ")
    }

    @ObservationIgnored private let library: RecordingLibrary
    @ObservationIgnored private let api: () -> APIClient?
    @ObservationIgnored private let file: URL
    @ObservationIgnored private let now: () -> Date
    @ObservationIgnored private var worker: Task<Void, Never>?
    @ObservationIgnored private var again = false
    /// `worker` is only waiting out a backoff: a new reason to sync cuts it short.
    @ObservationIgnored private var backingOff = false
    @ObservationIgnored private var failures = 0
    @ObservationIgnored private var lastReconcile: Date?
    @ObservationIgnored private var started = false

    init(library: RecordingLibrary = .shared, api: @escaping () -> APIClient? = { AppModel.shared.api },
         now: @escaping () -> Date = { .now }) {
        self.library = library; self.api = api; self.now = now
        file = library.root.appendingPathComponent("sync.json")
        store = (try? Data(contentsOf: file)).flatMap { try? JSONDecoder().decode(Store.self, from: $0) } ?? Store()
    }

    /// From `PhoneRecordingReceiver.start`: every save is a reason to sync, and so is coming back to the foreground.
    func start() {
        guard !started else { return }
        started = true
        library.didChange = { [weak self] _ in self?.kick() }
        #if canImport(UIKit) && !os(watchOS)
        NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable _ in
            Task { @MainActor in RecordingSync.shared.kick(reconcile: true) }
        }
        #endif
        purgeExpired()
        kick(reconcile: true)
    }

    func status(_ entry: RecordingEntry) -> Status {
        if isExpired(entry) { return .expired }
        if uploading == entry.id { return .uploading }
        if let have = store.synced[entry.id], have >= entry.revision { return .synced }
        if store.rejected[entry.id] == entry.revision { return .rejected }
        return Self.uploadable(entry) ? .pending : .waiting
    }

    /// Text worth sending: at least one sentence (an empty transcript is noise in the knowledge base).
    nonisolated static func uploadable(_ entry: RecordingEntry) -> Bool {
        entry.sentences.contains { !$0.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    }

    func isExpired(_ entry: RecordingEntry) -> Bool {
        store.daysKnown && !entry.active && entry.createdAt < now().addingTimeInterval(-Double(store.days) * 86_400)
    }

    /// Deleted by the user: gone here at once (audio and text), and from the knowledge base when the hub is reachable.
    func delete(_ id: UUID) throws {
        try library.remove(id)
        store.deletes.insert(id)
        forget(id)
        save()
        kick()
    }

    /// Runs a pass soon (several saves in a row become one upload). `reconcile`: also ask the hub what it has, at most
    /// every ten minutes.
    func kick(reconcile: Bool = false) {
        if reconcile, automaticRetryPaused { automaticRetryPaused = false; lastReconcile = nil }
        guard !automaticRetryPaused else { return }
        if reconcile, lastReconcile.map({ now().timeIntervalSince($0) > 600 }) ?? true { lastReconcile = nil }
        if backingOff { worker?.cancel(); worker = nil; backingOff = false }
        guard worker == nil else { again = true; return }
        worker = Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            guard !Task.isCancelled else { return }
            await self?.runPasses()
        }
    }

    /// One pass now, for tests (no debounce, no retry timer).
    func syncNow(retryPaused: Bool = false) async {
        if retryPaused { automaticRetryPaused = false; lastReconcile = nil }
        await pass()
    }

    private func runPasses() async {
        repeat {
            again = false
            await pass()
        } while again && !automaticRetryPaused && !Task.isCancelled
        worker = nil
        guard needsWork, !automaticRetryPaused, api()?.config.isDemo == false else { failures = 0; return }
        // The hub was away or refused for now: try again later, longer each time (5 s … 10 min).
        failures += 1
        let delay = min(600, 5 * pow(2, Double(min(failures, 8) - 1)))
        backingOff = true
        worker = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled, let self else { return }
            self.backingOff = false
            self.worker = nil
            self.kick()
        }
    }

    private var needsWork: Bool {
        !store.deletes.isEmpty || library.entries.contains { status($0) == .pending }
    }

    private func pass() async {
        guard !automaticRetryPaused else { return }
        purgeExpired()
        guard let api = api(), !api.config.isDemo else {
            lastProblem = api == nil ? nil : String(localized: "Demo mode doesn't sync recordings")
            return
        }
        if lastReconcile == nil {
            guard await reconcile(api) else { return }
            lastReconcile = now()
        }
        for id in store.deletes.sorted(by: { $0.uuidString < $1.uuidString }) {
            guard await sendDelete(id, api) else { return }
        }
        for entry in library.entries where status(entry) == .pending {
            guard await upload(entry, api) else { return }
        }
        lastProblem = nil
    }

    /// `GET /api/v1/recordings`: the hub's retention, what it already has, and what was deleted elsewhere.
    private func reconcile(_ api: APIClient) async -> Bool {
        struct Have: Decodable { var id: String; var revision: Int64 }
        struct Answer: Decodable { var days: Int?; var recordings: [Have]?; var deleted: [String]? }
        do {
            let answer: Answer = try await api.get("/api/v1/recordings")
            if let days = answer.days, (1...365).contains(days) { store.days = days; store.daysKnown = true }
            let have = Dictionary((answer.recordings ?? []).compactMap { r in UUID(uuidString: r.id).map { ($0, r.revision) } },
                                  uniquingKeysWith: max)
            // The hub's word replaces ours: one it lost (a new hub) goes again, one it has newer is not resent.
            store.synced = have.filter { library.entry($0.key) != nil }
            for id in (answer.deleted ?? []).compactMap(UUID.init(uuidString:)) where library.entry(id) != nil {
                deletedOnHub(id)
            }
            save()
            purgeExpired()
            return true
        } catch {
            note(error)
            return false
        }
    }

    private func upload(_ entry: RecordingEntry, _ api: APIClient) async -> Bool {
        struct Answer: Decodable { var revision: Int64?; var status: String? }
        uploading = entry.id
        defer { uploading = nil }
        let body = RecordingUpload(entry)
        do {
            let data = try await api.request("PUT", "/api/v1/recordings/\(entry.id.uuidString.lowercased())", body: body, timeout: 60)
            let answer = try? JSON.decoder.decode(Answer.self, from: data)
            // Deleted while it was on its way: the delete that follows removes it there too.
            guard library.entry(entry.id) != nil else { return true }
            switch answer?.status {
            case "expired":
                if let latest = library.entry(entry.id), isExpired(latest) { purge(latest.id) }
                store.synced[entry.id] = body.revision // the hub will not keep it; nothing more to send
            default: // stored, stale (it has a newer one)
                store.synced[entry.id] = max(body.revision, answer?.revision ?? 0)
            }
            store.rejected[entry.id] = nil
            save()
            return true
        } catch let error as APIError where error.status == 410 {
            deletedOnHub(entry.id)
            save()
            return true
        } catch let error as APIError where error.status == 400 || error.status == 413 {
            store.rejected[entry.id] = body.revision
            save()
            lastProblem = String(localized: "Knowledge didn't accept a recording: \(error.message)")
            return true
        } catch {
            note(error)
            return false
        }
    }

    private func sendDelete(_ id: UUID, _ api: APIClient) async -> Bool {
        do {
            _ = try await api.request("DELETE", "/api/v1/recordings/\(id.uuidString.lowercased())")
        } catch let error as APIError where error.status == 410 {
            // The hub confirms it was already deleted. An unsupported 404 does not confirm a remote deletion.
        } catch {
            note(error)
            return false
        }
        store.deletes.remove(id)
        save()
        return true
    }

    /// Deleted in the knowledge base (the web, another device): the text there is the truth, so it goes here too.
    private func deletedOnHub(_ id: UUID) {
        RecordingTranscription.shared.cancel(id)
        try? library.remove(id)
        forget(id)
    }

    /// Past the retention: the hub drops it, and the audio here goes with it.
    func purgeExpired() {
        let old = library.entries.filter(isExpired)
        guard !old.isEmpty else { return }
        for entry in old { purge(entry.id) }
        save()
    }

    private func purge(_ id: UUID) {
        RecordingTranscription.shared.cancel(id)
        try? library.remove(id)
        forget(id)
    }

    private func forget(_ id: UUID) {
        store.synced[id] = nil
        store.rejected[id] = nil
    }

    private func note(_ error: Error) {
        if error.isCancellation { return }
        if let e = error as? APIError {
            automaticRetryPaused = e.code == "kb_disabled" || e.code == "kb_read_only"
                || (400..<500).contains(e.status) && e.status != 408 && e.status != 429
            if e.businessMessage != nil {
                lastProblem = e.localizedDescription
                // Use the shared classification, including a request id, without raw internal diagnostics.
            } else if e.status == 404 {
                lastProblem = String(localized: "Recording sync isn't supported by this hub. Update the hub, then reopen the app to retry.")
            } else if e.status == 401 || e.status == 403 {
                lastProblem = String(localized: "Recording sync needs access. Check this device's permissions and pairing, then reopen the app to retry.")
            } else {
                lastProblem = String(localized: "Recording sync couldn't finish. Try again after checking the hub's diagnostics.")
            }
        } else if error.isTimeout {
            lastProblem = String(localized: "The request timed out. Try again later.")
        } else if error.isOffline {
            lastProblem = String(localized: "Can't reach the hub. Recording text will sync automatically later.")
        } else {
            lastProblem = String(localized: "Recording sync couldn't finish. Try again after checking the hub's diagnostics.")
        }
    }

    private func save() {
        do { try JSONEncoder().encode(store).write(to: file, options: .atomic) } catch { lastProblem = String(localized: "Sync record wasn't saved") }
    }
}
