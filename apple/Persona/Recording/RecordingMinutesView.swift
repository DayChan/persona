#if os(iOS) || os(visionOS)
import SwiftUI
import UIKit

/// A recording's minutes as its page shows them (#2710, docs/apple/recording.md「Minutes」): written by the hub from the
/// transcript once the recording is done and synced, read from `GET /api/v1/kb/recordings/{id}` (`detail.minutes`).
@MainActor @Observable final class RecordingMinutesModel {
    enum State: Equatable {
        /// Not a meeting, and the hub wrote none: no section.
        case none
        /// Still recording or transcribing: minutes come after.
        case waitingForText
        /// The text isn't on the hub yet (offline, not paired).
        case waitingForSync
        /// The hub is writing them.
        case writing
        case ready(RecordingMinutes)
        case failed
        /// A meeting, synced, and the hub says nothing about minutes: it doesn't write them yet (an older hub).
        case unsupported
    }

    private(set) var state: State = .none
    private(set) var asking = false
    /// Bumped by Try Again: the page's polling starts over (its task is keyed on it).
    private(set) var round = 0
    @ObservationIgnored private let api: () -> APIClient?

    init(api: @escaping () -> APIClient? = { AppModel.shared.api }) { self.api = api }

    /// `detail.minutes`, and `detail.meta.minutes_state`: pending, ready, failed, or absent for a note.
    private struct Answer: Decodable {
        struct Meta: Decodable {
            var state: String?
            enum CodingKeys: String, CodingKey { case state = "minutes_state" }
        }
        struct Detail: Decodable {
            var minutes: RecordingMinutes?
            var meta: Meta?
        }
        var detail: Detail
    }

    /// Where `entry`'s minutes are now. `synced`: the hub has this revision of its text.
    func load(_ entry: RecordingEntry, synced: Bool) async {
        let meeting = entry.isMeeting
        if entry.active || !entry.transcribedAll {
            if case .ready = state { return } // a re-transcription keeps showing the minutes it has
            state = meeting ? .waitingForText : .none
            return
        }
        // No speech recognized: nothing goes to the hub, so there is nothing to write minutes from.
        guard RecordingSync.uploadable(entry) else { state = .none; return }
        guard let api = api(), !api.config.isDemo else { state = meeting ? .waitingForSync : .none; return }
        do {
            let answer: Answer = try await api.get("/api/v1/kb/recordings/\(entry.id.uuidString.lowercased())")
            state = Self.state(minutes: answer.detail.minutes, status: answer.detail.meta?.state, meeting: meeting, synced: synced)
        } catch let error as APIError where error.status == 404 {
            state = meeting ? .waitingForSync : .none
        } catch {
            // Out of reach for now: what it showed stays; a meeting with nothing yet waits for the text to get there.
            if case .ready = state { return }
            state = meeting ? (synced ? .writing : .waitingForSync) : .none
        }
    }

    /// The hub's answer as a state: its minutes (`ready`, even with nothing to note); else what `minutes_state` says. A
    /// meeting it says nothing about waits for its text to get there, and once there is from a hub that writes no
    /// minutes (the contract sets `pending` as soon as a meeting is stored).
    nonisolated static func state(minutes: RecordingMinutes?, status: String?, meeting: Bool, synced: Bool) -> State {
        if let minutes, status == "ready" || !minutes.isEmpty { return .ready(minutes) }
        switch status {
        case "failed": return .failed
        case "pending": return .writing
        default: return meeting ? (synced ? .unsupported : .waitingForSync) : .none
        }
    }

    /// Writes them again (`POST /api/v1/recordings/{id}/minutes`): after a failure, or when they missed something.
    func regenerate(_ id: UUID) async {
        guard let api = api(), !asking else { return }
        asking = true
        defer { asking = false }
        do {
            _ = try await api.request("POST", "/api/v1/recordings/\(id.uuidString.lowercased())/minutes")
            state = .writing
            round += 1
        } catch let error as APIError where error.status == 409 {
            // The transcript isn't complete on the hub yet: the minutes come once it is.
            state = .waitingForSync
            round += 1
        } catch {
            AppModel.shared.toast = Toast(String(localized: "Couldn't ask the hub for new minutes: \(error.localizedDescription)"), style: .error)
        }
    }

    /// Whether to ask again soon: while they are on their way.
    var waiting: Bool {
        switch state {
        case .writing, .waitingForSync, .waitingForText: true
        default: false
        }
    }
}

/// The minutes' sections in a recording's page: when and where, who was there, key points, decisions, action items and open questions, each
/// with the time it was said (a tap shows that line of the transcript); Copy and Share. Before they exist, what they wait for.
struct RecordingMinutesSections: View {
    var entry: RecordingEntry
    var model: RecordingMinutesModel
    var jump: (Double) -> Void

    var body: some View {
        switch model.state {
        case .none:
            EmptyView()
        case .waitingForText:
            waiting(String(localized: "Minutes are written once the recording ends and its text is transcribed."), busy: false)
        case .waitingForSync:
            waiting(String(localized: "Minutes are written once the text reaches the hub."), busy: false)
        case .writing:
            waiting(String(localized: "The hub is turning the transcript into key points, decisions, action items and open questions."), busy: true)
        case .unsupported:
            Section("Minutes") {
                Text("This hub doesn't write meeting minutes yet. They come after it's updated.")
                    .font(Theme.Font.caption).foregroundStyle(.secondary)
            }
        case .failed:
            Section("Minutes") {
                Label("The minutes couldn't be written.", systemImage: Theme.Icon.error).foregroundStyle(Theme.danger)
                Button { Task { await model.regenerate(entry.id) } } label: { Text("Try Again").lineLimit(1) }
                    .disabled(model.asking)
            }
        case .ready(let minutes):
            ready(minutes)
        }
    }

    private func waiting(_ detail: String, busy: Bool) -> some View {
        Section("Minutes") {
            HStack(spacing: Theme.Space.s) {
                if busy { ProgressView() }
                Text(busy ? String(localized: "Writing the minutes…") : String(localized: "Minutes come next")).font(Theme.Font.body)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("minutes-waiting")
            Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder private func ready(_ minutes: RecordingMinutes) -> some View {
        Section {
            if let title = minutes.title, !title.isEmpty, title != entry.displayTitle {
                Text(title).font(Theme.Font.bodyStrong)
            }
            let line = [minutes.when, minutes.place].compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: " · ")
            if !line.isEmpty {
                Label(line, systemImage: Theme.Icon.calendarEvent).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            }
            if !minutes.attendees.isEmpty {
                Label(ListFormatter.localizedString(byJoining: minutes.attendees), systemImage: Theme.Icon.people)
                    .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            }
            HStack(spacing: Theme.Space.m) {
                Button { copy(minutes) } label: { Label("Copy", systemImage: Theme.Icon.copy).lineLimit(1) }
                    .themedButton()
                    .accessibilityIdentifier("minutes-copy")
                ShareLink(item: text(minutes), subject: Text(entry.displayTitle)) {
                    Label("Share", systemImage: Theme.Icon.share).lineLimit(1)
                }
                .themedButton()
                .accessibilityIdentifier("minutes-share")
            }
        } header: {
            Text("Minutes")
        }
        if minutes.isEmpty {
            Section { Text("Nothing to note: no points, decisions or action items came up.").font(Theme.Font.secondary).foregroundStyle(.secondary) }
        }
        items(String(localized: "Key Points"), Theme.Icon.keyPoint, minutes.points)
        items(String(localized: "Decisions"), Theme.Icon.decision, minutes.decisions)
        items(String(localized: "Action Items"), Theme.Icon.todo, minutes.actions)
        items(String(localized: "Open Questions"), Theme.Icon.openQuestion, minutes.openQuestions)
    }

    @ViewBuilder private func items(_ heading: String, _ symbol: String, _ list: [RecordingMinutes.Item]) -> some View {
        if !list.isEmpty {
            Section(heading) {
                ForEach(Array(list.enumerated()), id: \.offset) { _, item in row(item, symbol) }
            }
        }
    }

    private func row(_ item: RecordingMinutes.Item, _ symbol: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(symbol).font(Theme.Font.secondary).foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.iconXS)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(item.text).font(Theme.Font.body).textSelection(.enabled)
                if let byline = RecordingMinutes.byline(item) {
                    Text(byline).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: Theme.Space.s)
            if let at = item.at {
                Button { jump(at) } label: {
                    Chip(text: RecordingFormat.clock(at), look: .tint(Theme.accent), compact: true)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(String(localized: "Show in transcript at \(RecordingFormat.clock(at))"))
                .accessibilityIdentifier("minutes-time")
            }
        }
    }

    private func text(_ minutes: RecordingMinutes) -> String {
        minutes.text(title: entry.displayTitle)
    }

    private func copy(_ minutes: RecordingMinutes) {
        UIPasteboard.general.string = text(minutes)
        Haptics.success()
        AppModel.shared.toast = Toast(String(localized: "Minutes copied"), style: .success)
    }
}
#endif
