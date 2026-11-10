#if os(iOS)
import AVFoundation
import Foundation
import OSLog
import UIKit
import UserNotifications

/// "We're meeting now, record it" in one step (docs/apple/recording.md「Meeting recordings」): the drawer's Meeting Recording,
/// the Recordings page, the home screen's menu, the Control, Siri / Shortcuts / the Action button. Names the recording after
/// the calendar event happening now, keeps the Live Activity in step, picks a recording a call paused up again when the app
/// comes back, and holds the app open after a stop in the background until the last text is transcribed and sent.
@MainActor final class MeetingRecorder {
    static let shared = MeetingRecorder()

    /// The language a recording starts in (`RecordingDefaults`): the Recordings page's picker remembers it.
    static var locale: String { UserDefaults.standard.string(forKey: RecordingDefaults.localeKey) ?? RecordingDefaults.locale }

    /// The microphone was never allowed: the app has to be in front to ask.
    static var needsForeground: Bool { AVAudioApplication.shared.recordPermission != .granted }
    static var inForeground: Bool { UIApplication.shared.applicationState == .active }

    private let capture = RecordingCapture.shared
    private var started = false
    private let log = Logger(subsystem: "persona", category: "meeting-recorder")

    /// From `PhoneRecordingReceiver.start` at launch.
    func start() {
        guard !started else { return }
        started = true
        RecordingActivity.shared.start()
        capture.onInterruptionNotResumed = { MeetingRecorder.notifyPaused() }
        // A call that ended while the app was away (or never said so): coming back carries on, unless another app's audio
        // (a VoIP call without CallKit, still going) holds the route: taking the session would cut into that call.
        NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable _ in
            Task { @MainActor in
                guard !AVAudioSession.sharedInstance().isOtherAudioPlaying else { return }
                RecordingCapture.shared.resumeAfterInterruption()
            }
        }
    }

    enum Outcome: Equatable {
        case started
        /// Something records already (this one or a note): left alone.
        case alreadyRecording
        case failed(String)
    }

    /// Starts a meeting recording named after the calendar event happening now (a dated "Meeting" without one). In front
    /// (`inForeground`), the Recordings page opens (unless the caller opened it: `openPage`), and the calendar is asked
    /// for once if it never was, after the recording has begun, so the prompt costs no seconds of the meeting.
    @discardableResult
    func startMeeting(inForeground: Bool, openPage: Bool? = nil) async -> Outcome {
        if openPage ?? inForeground { Self.openRecordings() }
        guard capture.state == .idle else { return .alreadyRecording }
        let meeting = await MeetingCalendar.meetingOffMain() ?? RecordingMeeting()
        await capture.start(locale: Self.locale, input: RecordingInput.chosen, title: meeting.title ?? "", meeting: meeting)
        guard capture.state == .recording, let id = capture.recordingID else {
            return .failed(capture.error ?? String(localized: "Can't open the microphone. Try again later."))
        }
        RecordingActivity.shared.refresh()
        Haptics.success()
        // Not awaited: an intent must not wait on the user answering the calendar prompt.
        if inForeground, meeting.eventID == nil, MeetingCalendar.canAsk {
            Task {
                guard await MeetingCalendar.requestAccess(), let found = await MeetingCalendar.meetingOffMain() else { return }
                RecordingLibrary.shared.change(id) { entry in
                    if entry.title.isEmpty { entry.title = found.title ?? "" }
                    entry.meeting = found
                }
            }
        }
        return .started
    }

    /// Stops the recording (the activity's Stop: `id` is the one it shows; a stale one stops nothing).
    func stop(recordingID: String?) {
        guard let id = capture.recordingID, recordingID == nil || recordingID == id.uuidString else { return }
        _ = capture.stop()
        finishInBackground(id)
    }

    func addMark(recordingID: String?) {
        guard let id = capture.recordingID, recordingID == nil || recordingID == id.uuidString else { return }
        capture.addMark()
        Haptics.tap()
    }

    /// Stopped while the app is away (the Lock Screen's Stop): the audio session ends, and with it the app's right to run.
    /// Hold on (30 s at most) for the last segment's text and its upload, so the minutes don't wait for the app's next launch.
    func finishInBackground(_ id: UUID) {
        guard !Self.inForeground else { return }
        AppDelegate.inBackground("recording-finish", done: {}) {
            for _ in 0..<30 {
                try? await Task.sleep(for: .seconds(1))
                let done = await MainActor.run { () -> Bool in
                    guard let entry = RecordingLibrary.shared.entry(id) else { return true }
                    let sync = RecordingSync.shared.status(entry)
                    return RecordingTranscription.shared.current != id && entry.transcribedAll && sync != .pending && sync != .uploading
                }
                if done { break }
            }
        }
    }

    /// The Recordings page in front: the iPhone's shell or the iPad's sidebar pushes it on the Persona page (the old tabs
    /// have no route there; the recording runs anyway). Launched by the home screen's menu or a Control, the app has no
    /// shell yet: it tries again for a few seconds.
    static func openRecordings() {
        let model = AppModel.shared
        model.shellSettings = false
        model.selectedTab = .assistant
        if !route(model) {
            Task {
                for _ in 0..<10 {
                    try? await Task.sleep(for: .milliseconds(500))
                    if route(model) { return }
                }
            }
        }
    }

    private static func route(_ model: AppModel) -> Bool {
        if let shell = model.tabRouter as? ShellState {
            shell.show(.recordings)
            shell.drawer.close()
            return true
        }
        if let pad = model.tabRouter as? PadRouter {
            pad.show(.recordings)
            return true
        }
        return false
    }

    /// The system ended a call but the recording could not pick up again in the background: say so, since nothing else will.
    static func notifyPaused() {
        guard !inForeground else { return }
        let content = UNMutableNotificationContent()
        content.title = String(localized: "Recording Paused")
        content.body = String(localized: "A call or another app paused the recording. Open Persona to continue it.")
        content.sound = .default
        content.userInfo = [RecordingLink.notificationKey: true]
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "recording-paused", content: content, trigger: nil))
    }

    /// A watch recording's first segment or end arrived: one made during a calendar event is that meeting's (the watch
    /// has no calendar). Recordings already named or tagged stay as they are.
    static func tagWatchRecording(_ id: UUID, at date: Date) {
        guard let entry = RecordingLibrary.shared.entry(id), entry.meeting == nil, entry.title.isEmpty,
              let meeting = MeetingCalendar.meeting(at: date) else { return }
        RecordingLibrary.shared.change(id) { entry in
            entry.title = meeting.title ?? ""
            entry.meeting = meeting
        }
    }
}
#endif
