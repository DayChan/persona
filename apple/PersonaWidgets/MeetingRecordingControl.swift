import AppIntents
import SwiftUI
import WidgetKit

/// Control Center / Lock Screen / Action Button: start recording a meeting (`StartMeetingRecordingIntent`) without opening
/// the app; its Live Activity then shows the time and Stop. The kind is the control's identity: never change it.
struct MeetingRecordingControl: ControlWidget {
    static let kind = "persona.control.meeting-recording"

    var body: some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: Self.kind) {
            ControlWidgetButton(action: StartMeetingRecordingIntent()) {
                Label("Record a Meeting", systemImage: RecordingSymbol.recordingAction)
            }
        }
        .displayName("Record a Meeting")
        .description("Starts recording a meeting in Persona, named after the calendar event happening now.")
    }
}
