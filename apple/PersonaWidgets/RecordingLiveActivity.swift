import ActivityKit
import SwiftUI
import WidgetKit

/// Draws the recording activity (docs/apple/live-activity.md「Recording」): on the Lock Screen its name, what it is (a meeting,
/// bookmarks, paused by a call), the time and Bookmark / Stop; in the Dynamic Island a red mark and the time, opened up the
/// same buttons. The time is the system's (`Text(timerInterval:)`), so a recording that runs on needs no updates.
/// Colors are fixed (`Palette`): the extension builds neither the app's Theme nor its style.
struct RecordingLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: RecordingActivityAttributes.self) { context in
            RecordingLockScreen(state: context.state, id: context.attributes.recordingID)
                // The lock screen is cut at about 160 pt: larger text would push the buttons off it.
                .dynamicTypeSize(...DynamicTypeSize.large)
                .activitySystemActionForegroundColor(Palette.recordingRed)
                .environment(\.locale, LanguageStore.drawingLocale)
                .widgetURL(RecordingLink.page)
        } dynamicIsland: { context in
            let state = context.state
            let id = context.attributes.recordingID
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    RecordingMark(phase: state.phase, size: IslandMetrics.expandedFace)
                        .padding(.leading, IslandMetrics.edge)
                }
                // The time in the corner, wide enough for "1:23:45": beside a title in the center region it was cut to "…".
                DynamicIslandExpandedRegion(.trailing) {
                    RecordingTime(state: state)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(state.phase == .recording ? Palette.recordingRed : .white)
                        .frame(minWidth: IslandMetrics.recordingExpandedTimerWidth, alignment: .trailing)
                        .padding(.trailing, IslandMetrics.edge)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: IslandMetrics.rowGap) {
                        VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                            Text(state.title).font(.headline).lineLimit(1)
                            RecordingSubtitle(state: state).font(.caption).foregroundStyle(.white.opacity(0.7)).lineLimit(1)
                        }
                        .padding(.horizontal, IslandMetrics.gap)
                        RecordingButtons(id: id, onDark: true)
                    }
                    .environment(\.locale, LanguageStore.drawingLocale)
                }
            } compactLeading: {
                IslandWidth { _ in
                    RecordingMark(phase: state.phase, size: IslandMetrics.face).padding(.leading, IslandMetrics.edge)
                }
            } compactTrailing: {
                IslandWidth { limited in
                    if !limited {
                        RecordingTime(state: state)
                            .font(.system(size: IslandMetrics.timerNumber, weight: .semibold))
                            .foregroundStyle(state.phase == .recording ? Palette.recordingRed : .white)
                            // `Text(timerInterval:)` takes all the width it is offered.
                            .frame(width: IslandMetrics.recordingTimerWidth, alignment: .trailing)
                            .padding(.trailing, IslandMetrics.edge)
                    }
                }
            } minimal: {
                RecordingMark(phase: state.phase, size: IslandMetrics.limitedContent)
            }
            .widgetURL(RecordingLink.page)
            .keylineTint(Palette.recordingRed)
        }
        .supplementalActivityFamilies([.small])
    }
}

/// The recording's mark: a red disc with a waveform while it records, a pause sign while it doesn't.
struct RecordingMark: View {
    var phase: RecordingActivityAttributes.ContentState.Phase
    var size: CGFloat

    var body: some View {
        ZStack {
            Circle().fill(Palette.recordingRed.opacity(phase == .recording ? 1 : 0.35))
            Image(systemName: phase == .recording ? RecordingSymbol.recording : RecordingSymbol.paused)
                .font(.system(size: size * 0.48, weight: .bold))
                .foregroundStyle(.white)
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// The recorded time: counted by the system while recording, standing still while paused.
struct RecordingTime: View {
    var state: RecordingActivityAttributes.ContentState

    var body: some View {
        Group {
            if state.phase == .recording, let since = state.runningSinceDate {
                Text(timerInterval: since...Date.distantFuture, countsDown: false, showsHours: true)
            } else {
                Text(Duration.seconds(Int(state.elapsed)),
                     format: .time(pattern: state.elapsed >= 3600 ? .hourMinuteSecond : .minuteSecond))
            }
        }
        .monospacedDigit()
        .multilineTextAlignment(.trailing)
        .lineLimit(1)
    }
}

/// Under the name: a meeting, its bookmarks; or why it isn't recording.
struct RecordingSubtitle: View {
    var state: RecordingActivityAttributes.ContentState

    var body: some View {
        switch state.phase {
        case .interrupted: Text("Paused by a call · Open Persona to continue")
        case .paused: Text("Paused")
        case .recording:
            if state.marks > 0 {
                Text(state.meeting ? "Meeting · \(state.marks) bookmarks" : "Recording · \(state.marks) bookmarks")
            } else {
                Text(state.meeting ? "Meeting recording" : "Recording on iPhone")
            }
        }
    }
}

/// Bookmark and Stop: neutral and red, white words on the red (as the Fleet activity's Allow on blue).
struct RecordingButtons: View {
    var id: String
    var onDark = false

    var body: some View {
        HStack(spacing: IslandMetrics.buttonGap) {
            Button(intent: AddRecordingBookmarkIntent(recordingID: id)) { label("Bookmark", RecordingSymbol.bookmark) }
                .buttonStyle(.bordered)
                .tint(onDark ? .white : .primary)
            Button(intent: StopRecordingIntent(recordingID: id)) { label("Stop", RecordingSymbol.stop) }
                .buttonStyle(.borderedProminent)
                .tint(Palette.recordingButton)
                .foregroundStyle(.white)
        }
        .buttonBorderShape(.capsule)
    }

    private func label(_ text: LocalizedStringKey, _ symbol: String) -> some View {
        Label(text, systemImage: symbol)
            .font(.subheadline.weight(.semibold))
            .lineLimit(1)
            .minimumScaleFactor(0.8)
            .frame(maxWidth: .infinity)
    }
}

/// The Lock Screen: mark, name and what it is, the time; under them the buttons. The watch's Smart Stack (`.small`) gets
/// a glance: mark, name, time, no buttons (they wouldn't fit).
struct RecordingLockScreen: View {
    var state: RecordingActivityAttributes.ContentState
    var id: String
    @Environment(\.activityFamily) private var family

    var body: some View {
        if family == .small { glance } else { full }
    }

    private var glance: some View {
        HStack(spacing: IslandMetrics.rowGap) {
            RecordingMark(phase: state.phase, size: IslandMetrics.face)
            VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                Text(state.title).font(.footnote.weight(.semibold)).lineLimit(1)
                RecordingTime(state: state).font(.footnote).foregroundStyle(state.phase == .recording ? Palette.recordingRed : .secondary)
            }
            Spacer(minLength: 0)
        }
        .padding(IslandMetrics.rowGap)
    }

    private var full: some View {
        VStack(alignment: .leading, spacing: IslandMetrics.rowGap + IslandMetrics.gap) {
            HStack(spacing: IslandMetrics.rowGap + IslandMetrics.gap) {
                RecordingMark(phase: state.phase, size: IslandMetrics.recordingMark)
                VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                    Text(state.title).font(.headline).lineLimit(1)
                    RecordingSubtitle(state: state).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: IslandMetrics.gap)
                RecordingTime(state: state)
                    .font(.title2.weight(.semibold))
                    .foregroundStyle(state.phase == .recording ? Palette.recordingRed : .secondary)
            }
            RecordingButtons(id: id)
        }
        .padding(IslandMetrics.lockPadding)
    }
}

/// The recording activity's symbols (the extension doesn't build `Theme.Icon`): the app's own for the same meanings.
enum RecordingSymbol {
    /// The app's recording symbol (`Theme.Icon.recording`): the Control's.
    static let recordingAction = "waveform.circle"
    /// On the red mark the disc is drawn already: the bare waveform.
    static let recording = "waveform"
    static let paused = "pause.fill"
    /// The app's bookmark (`Theme.Icon.pin`).
    static let bookmark = "pin"
    static let stop = "stop.fill"
}
