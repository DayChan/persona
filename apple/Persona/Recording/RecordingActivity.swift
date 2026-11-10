#if os(iOS)
import ActivityKit
import Foundation
import Observation
import OSLog

/// The phone's half of the recording Live Activity (docs/apple/live-activity.md「Recording」): one activity while this phone
/// records, any recording (a meeting's or a note), started with it and ended with it. It follows `RecordingCapture` (state,
/// a call's interruption) and the recording's entry (its title once the calendar is read, bookmarks); the time is counted
/// by the system from `runningSince`, so a recording that runs on needs no updates.
@MainActor final class RecordingActivity {
    static let shared = RecordingActivity()
    typealias Content = RecordingActivityAttributes.ContentState

    private let capture = RecordingCapture.shared
    private let library = RecordingLibrary.shared
    private var started = false
    /// What the activity shows now, and for which recording.
    private var shown: (id: UUID, content: Content)?
    private let log = Logger(subsystem: "persona", category: "recording-activity")

    /// From `MeetingRecorder.start()` at launch.
    func start() {
        guard !started else { return }
        started = true
        observe()
    }

    /// Brings the activity in step now, rather than on the next run loop: an `AudioRecordingIntent` must have it showing
    /// by the time it returns.
    func refresh() { sync() }

    private func observe() {
        withObservationTracking { sync() } onChange: {
            Task { @MainActor in RecordingActivity.shared.observe() }
        }
    }

    private func sync() {
        let state = capture.state
        let interrupted = capture.interrupted
        guard let id = capture.recordingID, state == .recording || state == .paused, let entry = library.entry(id) else {
            endAll()
            return
        }
        let previous = shown?.id == id ? shown?.content : nil
        let content = Self.content(entry: entry, recording: state == .recording, interrupted: interrupted,
                                   elapsed: capture.timeline, previous: previous, now: .now)
        if let previous {
            guard previous != content else { return }
            shown = (id, content)
            if let activity = activity(id) { update(activity, content) } else { request(id, content) }
        } else {
            shown = (id, content)
            request(id, content)
        }
    }

    /// What the activity shows for `entry`. Recording on, the time keeps counting from the start it had (`previous`), so
    /// the many saves of a running recording (each segment, each transcript) change nothing on screen.
    nonisolated static func content(entry: RecordingEntry, recording: Bool, interrupted: Bool, elapsed: Double,
                                    previous: Content?, now: Date) -> Content {
        let phase: Content.Phase = recording ? .recording : interrupted ? .interrupted : .paused
        let keep = recording && previous?.phase == .recording
        let since = recording ? (keep ? previous?.runningSince : nil) ?? now.timeIntervalSince1970 - elapsed : nil
        return Content(phase: phase, runningSince: since, elapsed: keep ? previous?.elapsed ?? elapsed : elapsed,
                       title: entry.displayTitle, meeting: entry.isMeeting, marks: entry.marks.count)
    }

    /// While recording, a higher relevance than the Fleet activity's (1 at most): the island shows the recording.
    nonisolated static let relevance: Double = 2

    private func activity(_ id: UUID) -> Activity<RecordingActivityAttributes>? {
        Activity<RecordingActivityAttributes>.activities.first {
            $0.attributes.recordingID == id.uuidString && ($0.activityState == .active || $0.activityState == .stale)
        }
    }

    private func request(_ id: UUID, _ content: Content) {
        // One at a time: another left over (an app killed while recording) goes.
        for other in Activity<RecordingActivityAttributes>.activities
        where other.attributes.recordingID != id.uuidString && (other.activityState == .active || other.activityState == .stale) {
            end(other)
        }
        guard activity(id) == nil else { return }
        do {
            _ = try Activity.request(attributes: RecordingActivityAttributes(recordingID: id.uuidString),
                                     content: ActivityContent(state: content, staleDate: nil, relevanceScore: Self.relevance),
                                     pushType: nil)
        } catch {
            // Live Activities off for Persona in Settings: the recording goes on without one.
            log.notice("recording activity not started: \(error.localizedDescription, privacy: .public)")
        }
    }

    private func update(_ activity: Activity<RecordingActivityAttributes>, _ content: Content) {
        nonisolated(unsafe) let a = activity
        let state = ActivityContent(state: content, staleDate: nil, relevanceScore: Self.relevance)
        Task { await a.update(state) }
    }

    private func endAll() {
        shown = nil
        for activity in Activity<RecordingActivityAttributes>.activities
        where activity.activityState == .active || activity.activityState == .stale {
            end(activity)
        }
    }

    private func end(_ activity: Activity<RecordingActivityAttributes>) {
        nonisolated(unsafe) let a = activity
        Task { await a.end(nil, dismissalPolicy: .immediate) }
    }
}
#endif
