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
            ThemedIcon(Theme.Icon.recording).font(Theme.Font.heading).foregroundStyle(Theme.accent)
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

/// Start / pause / bookmark / stop for a recording on this phone (the watch has its own).
struct RecordingCaptureControls: View {
    @State private var capture = RecordingCapture.shared
    @State private var locale = "zh-CN"
    #if os(iOS)
    @StoredValue(RecordingInput.storageKey) private var inputRaw = RecordingInput.phone.rawValue
    @State private var hasBluetooth = false
    #endif

    var body: some View {
        #if os(iOS) // Bluetooth input is iPhone-only (`RecordingInput`); visionOS records with its own microphone
        controls
            .task { // the Microphone picker shows only while a Bluetooth device (glasses, headset) is on the route
                refreshBluetooth()
                for await _ in NotificationCenter.default.notifications(named: AVAudioSession.routeChangeNotification) { refreshBluetooth() }
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
            HStack(spacing: Theme.Space.s) {
                Text(RecordingFormat.clock(capture.elapsed)).font(Theme.Font.title.monospacedDigit())
                Spacer()
                Text(capture.state == .paused ? String(localized: "Paused") : String(localized: "Recording [Recordings Section 5254d3]")).font(Theme.Font.secondary).foregroundStyle(Theme.danger)
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
                Button { _ = capture.stop() } label: { Label("Stop [Car Play Voice Screen c7b24e]", systemImage: Theme.Icon.interrupt).lineLimit(1) }
                    .themedButton().tint(Theme.danger).accessibilityIdentifier("recording-stop")
            }
            Text("Recording continues if you leave this page or lock the screen. Tap End when you're done.").font(Theme.Font.caption).foregroundStyle(.secondary)
        }
        if let error = capture.error { Text(error).font(Theme.Font.secondary).foregroundStyle(Theme.danger) }
        if let notice = capture.notice { Text(notice).font(Theme.Font.secondary).foregroundStyle(.secondary) }
    }

    #if os(iOS)
    private func refreshBluetooth() {
        hasBluetooth = RecordingInput.bluetoothConnected
    }
    #endif
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

    var body: some View {
        Group {
            if let entry = library.entry(id) { content(entry) } else {
                EmptyStateView(symbol: Theme.Icon.recording, title: String(localized: "Recording Deleted"), message: String(localized: "This recording and its text in Knowledge have both been deleted."))
            }
        }
        .themedPage()
        .navigationTitle("Record [Recordings Section ef2821]")
        .navigationBarTitleDisplayMode(.inline)
        .confirmationDialog("Delete This Recording?", isPresented: $deleting, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                transcription.cancel(id)
                do { try sync.delete(id); dismiss() } catch { failure = String(localized: "The recording wasn't deleted. Try again later.") }
            }
        } message: { Text("The audio on this device and the text in Knowledge will both be deleted.") }
        .alert("Couldn't Finish", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            Button("OK [Recordings Section de32e2]") { failure = nil }.lineLimit(1)
        } message: { Text(failure ?? "") }
    }

    private func content(_ entry: RecordingEntry) -> some View {
        List {
            Section {
                Text(entry.displayTitle).font(Theme.Font.heading)
                HStack(spacing: Theme.Space.xs) {
                    Text("\(entry.sourceTitle) · \(RecordingFormat.clock(entry.duration)) ·")
                    RecordingStatusText(entry: entry)
                }
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                if !entry.marks.isEmpty {
                    Label(String(localized: "Bookmarks \(entry.marks.map(RecordingFormat.clock).joined(separator: "、"))"), systemImage: Theme.Icon.pin)
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                if transcription.current == id { ProgressView().tint(Theme.accent) }
            } footer: {
                Text("Only the text and timestamps go into Knowledge; the original audio isn't uploaded. After the retention period (\(sync.store.days) days) it's cleared from this device and Knowledge.")
            }
            Section("Transcript") {
                if entry.sentences.isEmpty {
                    Text("No transcript yet.").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                ForEach(Array(entry.sentences.enumerated()), id: \.offset) { _, sentence in
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        Text(RecordingFormat.clock(sentence.start)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
                        Text(sentence.text).font(Theme.Font.body).textSelection(.enabled)
                    }
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
}
#endif
