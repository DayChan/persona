#if os(iOS) || os(visionOS)
import SwiftUI
#if os(iOS)
import AVFoundation
#endif

/// Knowledge › Record (docs/apple/recording.md): start a recording, see this device's recordings (the watch's arrive here) with
/// where each one is (recording, transcribing, synced), open one for its transcript. The text goes to the knowledge base
/// by itself (`RecordingSync`); nothing is added to a composer.
struct RecordingsSection: View {
    @State private var library = RecordingLibrary.shared
    @State private var transcription = RecordingTranscription.shared
    @State private var sync = RecordingSync.shared

    var body: some View {
        Section {
            RecordingCaptureControls()
        } footer: {
            Text("Audio stays on this device; watch recordings come back here. After on-device transcription the text syncs to Knowledge automatically and is kept for the last \(sync.store.days) days, so the Assistant and agents can find it with a Knowledge search.")
        }
        Section {
            if library.entries.isEmpty {
                EmptyStateView(symbol: Theme.Icon.recording, title: String(localized: "No Recordings Yet"),
                               message: String(localized: "Record meetings, dictation and on-site notes with your phone or Apple Watch; the transcribed text goes into Knowledge."))
                    .listRowBackground(Color.clear)
            }
            ForEach(library.entries) { entry in
                NavigationLink { RecordingDetailView(id: entry.id) } label: { RecordingRow(entry: entry) }
                    .accessibilityIdentifier("recording-row")
            }
        } header: {
            if !library.entries.isEmpty {
                HStack(spacing: Theme.Space.sm) {
                    Text("Recent Recordings")
                    Text("\(library.entries.count)").foregroundStyle(Color.secondary.opacity(0.7)).monospacedDigit()
                }
            }
        } footer: {
            if let problem = sync.problem ?? library.error {
                Label(problem, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger)
                    .accessibilityIdentifier("recording-sync-problem")
            }
        }
        .task { PhoneRecordingReceiver.shared.start(); transcription.resumePending(); sync.kick(reconcile: true) }
    }
}

/// One recording: where it came from, how long, and where it is on its way to the knowledge base.
struct RecordingRow: View {
    var entry: RecordingEntry

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(entry.isMeeting ? Theme.Icon.meeting : Theme.Icon.recording).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.icon)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(entry.displayTitle).font(Theme.Font.body).lineLimit(1)
                HStack(spacing: Theme.Space.xs) {
                    Text("\(entry.sourceTitle) · \(RecordingFormat.clock(entry.duration)) ·")
                    RecordingStatusText(entry: entry)
                }
                .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        }
    }
}

/// Recording / Transcribing / Synced…: the one word for where a recording is.
struct RecordingStatusText: View {
    var entry: RecordingEntry
    @State private var transcription = RecordingTranscription.shared
    @State private var sync = RecordingSync.shared

    var body: some View {
        let (text, tone) = Self.status(entry, transcription: transcription, sync: sync)
        Text(text).foregroundStyle(tone).accessibilityIdentifier("recording-status")
    }

    static func status(_ entry: RecordingEntry, transcription: RecordingTranscription, sync: RecordingSync) -> (String, Color) {
        if entry.active { return (String(localized: "Recording [Recordings Section e35a14]"), Theme.danger) }
        if let error = transcription.errors[entry.id] { return (error, Theme.danger) }
        if transcription.current == entry.id { return (String(localized: "Transcribing"), Theme.accent) }
        if !entry.receivedAll { return (entry.expectedSegments == 0 ? String(localized: "No audio recorded") : String(localized: "Waiting for Apple Watch"), .secondary) }
        switch sync.status(entry) {
        case .synced: return (entry.transcribedAll ? String(localized: "Synced to Knowledge") : String(localized: "Partly synced"), Theme.positive)
        case .uploading: return (String(localized: "Syncing"), Theme.accent)
        case .rejected: return (String(localized: "Not accepted by Knowledge"), Theme.danger)
        case .expired: return (String(localized: "Past retention"), .secondary)
        case .pending: return (entry.transcribedAll ? String(localized: "Waiting to sync") : String(localized: "Waiting to transcribe"), .secondary)
        case .waiting: return (entry.transcribedAll ? String(localized: "No speech detected") : String(localized: "Waiting to transcribe"), .secondary)
        }
    }
}

/// Start / pause / bookmark / stop for a recording on this phone (the watch has its own). Meeting Recording leads: one tap,
/// named after the calendar event happening now (`MeetingRecorder`, docs/apple/recording.md「Meeting recordings」).
struct RecordingCaptureControls: View {
    @State private var capture = RecordingCapture.shared
    @State private var library = RecordingLibrary.shared
    @StoredValue(RecordingDefaults.localeKey) private var locale = RecordingDefaults.locale
    #if os(iOS)
    @StoredValue(RecordingInput.storageKey) private var inputRaw = RecordingInput.phone.rawValue
    @State private var hasBluetooth = false
    /// The calendar's meeting on now (read again each minute while nothing records).
    @State private var meetingNow: RecordingMeeting?
    @State private var calendarAskable = false
    #endif

    var body: some View {
        #if os(iOS) // Bluetooth input is iPhone-only (`RecordingInput`); visionOS records with its own microphone
        controls
            .task { // the Microphone picker shows only while a Bluetooth device (glasses, headset) is on the route
                refreshBluetooth()
                for await _ in NotificationCenter.default.notifications(named: AVAudioSession.routeChangeNotification) { refreshBluetooth() }
            }
            .task(id: capture.state == .idle) {
                while capture.state == .idle, !Task.isCancelled {
                    meetingNow = await MeetingCalendar.meetingOffMain()
                    calendarAskable = MeetingCalendar.canAsk
                    try? await Task.sleep(for: .seconds(60))
                }
            }
        #else
        controls
        #endif
    }

    /// The microphone a recording starts with: the Bluetooth one only when chosen and still connected.
    private var input: RecordingInput {
        #if os(iOS)
        hasBluetooth ? (RecordingInput(rawValue: inputRaw) ?? .phone) : .phone
        #else
        .phone
        #endif
    }

    @ViewBuilder private var controls: some View {
        if capture.state == .idle {
            #if os(iOS)
            meetingStart
            #endif
            Picker("Recording Language", selection: $locale) {
                Text("Chinese").tag("zh-CN")
                Text("English").tag("en-US")
                Text("Japanese").tag("ja-JP")
            }.font(Theme.Font.body)
            #if os(iOS)
            if hasBluetooth {
                Picker("Microphone", selection: $inputRaw) {
                    ForEach(RecordingInput.allCases, id: \.rawValue) { Text($0.title).tag($0.rawValue) }
                }.font(Theme.Font.body)
            }
            #endif
            Button { let chosen = input; Task { await capture.start(locale: locale, input: chosen) } } label: {
                Label("Start Recording", systemImage: Theme.Icon.recording).lineLimit(1)
            }.accessibilityIdentifier("recording-start")
        } else if capture.state == .starting {
            ProgressView(String(localized: "Waiting for microphone access")).font(Theme.Font.secondary)
            Button("Cancel") { capture.cancelStarting() }.lineLimit(1)
        } else {
            recordingNow
        }
        if let error = capture.error { Text(error).font(Theme.Font.secondary).foregroundStyle(Theme.danger) }
        if let notice = capture.notice { Text(notice).font(Theme.Font.secondary).foregroundStyle(.secondary) }
    }

    #if os(iOS)
    /// Meeting Recording, and what it will be called: the calendar's meeting on now, or a way to let it read the calendar.
    @ViewBuilder private var meetingStart: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Button {
                Task { await MeetingRecorder.shared.startMeeting(inForeground: true, openPage: false) }
            } label: {
                Label("Meeting Recording", systemImage: Theme.Icon.recording)
                    .font(Theme.Font.bodyStrong)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity)
            }
            .themedButton(prominent: true)
            .accessibilityIdentifier("recording-meeting-start")
            if let meeting = meetingNow, let title = meeting.title {
                Label(String(localized: "Calendar: \(title) · \(Self.span(meeting))"), systemImage: Theme.Icon.calendarEvent)
                    .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    .accessibilityIdentifier("recording-meeting-now")
            } else if calendarAskable {
                Button {
                    Task {
                        _ = await MeetingCalendar.requestAccess()
                        calendarAskable = MeetingCalendar.canAsk
                        meetingNow = await MeetingCalendar.meetingOffMain()
                    }
                } label: {
                    Label("Name Recordings After Calendar Meetings", systemImage: Theme.Icon.calendarEvent).font(Theme.Font.caption).lineLimit(1)
                }
                .buttonStyle(.borderless)
            } else {
                Text("Named after the calendar event happening now, if there is one.").font(Theme.Font.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, Theme.Space.xs)
    }

    /// 14:00–15:00.
    static func span(_ meeting: RecordingMeeting) -> String {
        guard let start = meeting.start, let end = meeting.end else { return "" }
        return (start..<max(end, start)).formatted(.interval.hour().minute())
    }
    #endif

    /// A recording in progress: what it is, its time and state, and pause / bookmark / stop.
    @ViewBuilder private var recordingNow: some View {
        let entry = capture.recordingID.flatMap(library.entry)
        if let entry {
            HStack(spacing: Theme.Space.s) {
                if entry.isMeeting { Chip(text: String(localized: "Meeting"), symbol: Theme.Icon.meeting, look: .tint(Theme.accent)) }
                Text(entry.displayTitle).font(Theme.Font.heading).lineLimit(1)
            }
            .accessibilityIdentifier("recording-now-title")
        }
        HStack(spacing: Theme.Space.s) {
            Text(RecordingFormat.clock(capture.elapsed)).font(Theme.Font.title.monospacedDigit())
            Spacer()
            Text(capture.interrupted ? String(localized: "Paused by a call") : capture.state == .paused ? String(localized: "Paused") : String(localized: "Recording [Recordings Section 5254d3]"))
                .font(Theme.Font.secondary).foregroundStyle(capture.interrupted ? Theme.cautionText : Theme.danger)
                .accessibilityIdentifier("recording-now-state")
        }
        if capture.interrupted {
            Text("It carries on by itself when the call ends, or when you come back to Persona.").font(Theme.Font.caption).foregroundStyle(.secondary)
        }
        #if os(iOS)
        if hasBluetooth, RecordingInput(rawValue: inputRaw) == .bluetooth, let name = capture.inputName {
            Text("Microphone: \(name)").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        #endif
        HStack(spacing: Theme.Space.m) {
            Button { capture.togglePause() } label: {
                Label(capture.state == .paused ? String(localized: "Continue") : String(localized: "Pause"), systemImage: capture.state == .paused ? Theme.Icon.run : Theme.Icon.pause).lineLimit(1)
            }.themedButton()
            Button { capture.addMark() } label: { Label("Bookmark", systemImage: Theme.Icon.pin).lineLimit(1) }.themedButton()
                .accessibilityIdentifier("recording-bookmark")
            Button { stop() } label: { Label("Stop [Car Play Voice Screen c7b24e]", systemImage: Theme.Icon.interrupt).lineLimit(1) }
                .themedButton().tint(Theme.danger).accessibilityIdentifier("recording-stop")
        }
        if let marks = entry?.marks, !marks.isEmpty {
            Label(String(localized: "Bookmarks \(ListFormatter.localizedString(byJoining: marks.map(RecordingFormat.clock)))"), systemImage: Theme.Icon.pin)
                .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        Text("Recording continues if you leave this page or lock the screen. Tap End when you're done.").font(Theme.Font.caption).foregroundStyle(.secondary)
    }

    private func stop() {
        #if os(iOS)
        MeetingRecorder.shared.stop(recordingID: nil)
        #else
        _ = capture.stop()
        #endif
    }

    #if os(iOS)
    private func refreshBluetooth() {
        hasBluetooth = RecordingInput.bluetoothConnected
    }
    #endif
}

/// Recordings as a page of its own (the drawer's Meeting Recording, the Control, Siri, the activity's tap): what Knowledge ›
/// Recordings shows, one step closer.
struct RecordingsPage: View {
    #if DEBUG
    @State private var debugRecording: UUID?
    private static var sampleOpened = false
    #endif

    var body: some View {
        List { RecordingsSection() }
            .listStyle(.insetGrouped)
            .themedPage()
            .navigationTitle(String(localized: "Recordings"))
            .navigationBarTitleDisplayMode(.inline)
            #if DEBUG
            // `-showMeetingSample minutes|pending`: the made-up finished meeting's page (screenshots, UI tests).
            .navigationDestination(item: $debugRecording) { RecordingDetailView(id: $0) }
            .onAppear {
                // Once per launch: back from it, the page appears again.
                if ["minutes", "pending"].contains(UserDefaults.standard.string(forKey: "showMeetingSample")), !Self.sampleOpened {
                    Self.sampleOpened = true
                    debugRecording = RecordingSamples.pastMeeting
                }
            }
            #endif
    }
}

/// One recording's transcript, with re-transcribing in another language and delete (here and in the knowledge base).
struct RecordingDetailView: View {
    var id: UUID
    @Environment(\.dismiss) private var dismiss
    @State private var library = RecordingLibrary.shared
    @State private var transcription = RecordingTranscription.shared
    @State private var sync = RecordingSync.shared
    @State private var deleting = false
    @State private var failure: String?
    /// The transcript line a timestamp jumped to, lit for a moment.
    @State private var lit: Int?
    @State private var minutes = RecordingMinutesModel()

    var body: some View {
        Group {
            if let entry = library.entry(id) { ScrollViewReader { proxy in content(entry, proxy: proxy) } } else {
                EmptyStateView(symbol: Theme.Icon.recording, title: String(localized: "Recording Deleted"), message: String(localized: "Deleted from this device. Removal from Knowledge may still be pending."))
            }
        }
        .themedPage()
        // Each new revision (more text, the end) may bring or change the minutes; while they are on their way, ask again.
        .task(id: "\(library.entry(id)?.revision ?? 0)|\(minutes.round)") { await followMinutes() }
        .navigationTitle("Record [Recordings Section ef2821]")
        .navigationBarTitleDisplayMode(.inline)
        .confirmationDialog("Delete This Recording?", isPresented: $deleting, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                transcription.cancel(id)
                do { try sync.delete(id); dismiss() } catch { failure = String(localized: "The recording wasn't deleted. Try again later.") }
            }
        } message: { Text("The audio on this device will be deleted now. Its text in Knowledge is removed after sync succeeds; if Knowledge is off or read-only, that deletion stays pending.") }
        .alert("Couldn't Finish", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            Button("OK [Recordings Section de32e2]") { failure = nil }.lineLimit(1)
        } message: { Text(failure ?? "") }
    }

    private func content(_ entry: RecordingEntry, proxy: ScrollViewProxy) -> some View {
        List {
            Section {
                Text(entry.displayTitle).font(Theme.Font.heading)
                if let meeting = entry.meeting { RecordingMeetingFacts(meeting: meeting) }
                HStack(spacing: Theme.Space.xs) {
                    Text("\(entry.sourceTitle) · \(RecordingFormat.clock(entry.duration)) ·")
                    RecordingStatusText(entry: entry)
                }
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                if !entry.marks.isEmpty {
                    Label(String(localized: "Bookmarks \(ListFormatter.localizedString(byJoining: entry.marks.map(RecordingFormat.clock)))"), systemImage: Theme.Icon.pin)
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                if transcription.current == id { ProgressView().tint(Theme.accent) }
            } footer: {
                Text("Only the text and timestamps go into Knowledge; the original audio isn't uploaded. After the retention period (\(sync.store.days) days) it's cleared from this device and Knowledge.")
            }
            RecordingMinutesSections(entry: entry, model: minutes) { jump(to: $0, in: entry, proxy: proxy) }
            Section("Transcript") {
                if entry.sentences.isEmpty {
                    Text("No transcript yet.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                ForEach(Array(entry.sentences.enumerated()), id: \.offset) { index, sentence in
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        Text(RecordingFormat.clock(sentence.start)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
                        Text(sentence.text).font(Theme.Font.body).textSelection(.enabled)
                    }
                    .id(RecordingDetailView.anchor(index))
                    .listRowBackground(lit == index ? Theme.accentTint : nil)
                    .accessibilityIdentifier(lit == index ? "recording-sentence-lit" : "recording-sentence")
                }
            }
            if !entry.active {
                Section {
                    Picker("Transcription Language", selection: Binding(get: { entry.locale }, set: { transcription.setLocale(id, $0) })) {
                        Text("Chinese").tag("zh-CN"); Text("English").tag("en-US"); Text("Japanese").tag("ja-JP")
                    }.disabled(transcription.current == id)
                    if transcription.current == id {
                        Button("Stop Transcribing") { transcription.cancel(id) }.lineLimit(1)
                    } else if !entry.transcribedAll {
                        Button("Resume Transcribing") { transcription.enqueue(id) }.lineLimit(1)
                    }
                    Button("Delete Recording", role: .destructive) { deleting = true }.lineLimit(1)
                        .accessibilityIdentifier("recording-delete")
                }
            }
        }
    }

    private func followMinutes() async {
        while !Task.isCancelled, let entry = library.entry(id) {
            await minutes.load(entry, synced: sync.status(entry) == .synced)
            guard minutes.waiting else { return }
            try? await Task.sleep(for: .seconds(15))
        }
    }

    /// A transcript line's scroll anchor.
    static func anchor(_ index: Int) -> String { "sentence-\(index)" }

    /// To the transcript line said at `seconds` (a timestamp in the minutes), lit for two seconds.
    private func jump(to seconds: Double, in entry: RecordingEntry, proxy: ScrollViewProxy) {
        guard let index = RecordingMinutes.sentenceIndex(at: seconds, in: entry.sentences) else { return }
        withAnimation(Theme.Motion.layout) { proxy.scrollTo(Self.anchor(index), anchor: .center) }
        lit = index
        Task {
            try? await Task.sleep(for: .seconds(2))
            if lit == index { withAnimation(Theme.Motion.standard) { lit = nil } }
        }
    }
}

/// A meeting recording's meeting: when it was booked, where, and who was invited (from the calendar event it is named after).
struct RecordingMeetingFacts: View {
    var meeting: RecordingMeeting

    var body: some View {
        if let start = meeting.start, let end = meeting.end {
            Label((start..<max(end, start)).formatted(.interval.month().day().hour().minute()), systemImage: Theme.Icon.calendarEvent)
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
        }
        if let location = meeting.location {
            Label(location, systemImage: Theme.Icon.place).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
        }
        if !meeting.attendees.isEmpty {
            Label(ListFormatter.localizedString(byJoining: meeting.attendees), systemImage: Theme.Icon.people)
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
        }
    }
}
#endif
