import ActivityKit
import Foundation

/// The Live Activity of a recording on this phone (docs/apple/live-activity.md「Recording」): the Lock Screen and the Dynamic
/// Island while it records, with Stop and Bookmark. Local only (`pushType: nil`): the hub never starts or updates it.
/// Its name is the system's identity of the activity: never rename it.
nonisolated struct RecordingActivityAttributes: ActivityAttributes {
    nonisolated struct ContentState: Codable, Hashable, Sendable {
        enum Phase: String, Codable, Hashable, Sendable {
            case recording
            /// Paused by the user.
            case paused
            /// Paused by a call or another app: resumes by itself, or when Persona is opened.
            case interrupted
        }

        var phase: Phase
        /// While recording: when the recorded time would have been zero (Unix seconds; the default decoder counts from
        /// 2001), so the system counts the time without updates. Paused, the time stands at `elapsed`.
        var runningSince: Double?
        var elapsed: Double
        var title: String
        var meeting: Bool
        var marks: Int

        var runningSinceDate: Date? { runningSince.map { Date(timeIntervalSince1970: $0) } }
    }

    /// The recording's id (`RecordingEntry.id`): what Stop and Bookmark act on.
    var recordingID: String
}

/// The links the recording activity opens: its Recordings page.
nonisolated enum RecordingLink {
    /// persona://record: the Recordings page (the app's own scheme, `HubNaming`).
    static let page = URL(string: "persona://record")!
    /// In a local notification's user info: tapping it opens the Recordings page.
    static let notificationKey = "persona_recording"

    static func matches(_ url: URL) -> Bool {
        HubNaming.accepts(scheme: url.scheme) && url.host() == "record"
    }
}
