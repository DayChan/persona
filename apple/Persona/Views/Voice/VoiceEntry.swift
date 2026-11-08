import BeautifulUI
import SwiftUI
#if os(iOS) || os(visionOS)
import UIKit
#endif

// How a conversation's page gets into a voice call and back out of one (docs/assistant/voice.md 6.1, 1.2 L, 1.3): starting it from
// the composer's button or the 「+」 menu's Voice Chat, what the menu says where there is none, Open Settings, and the line left
// where the bar was after a call ended by itself (「语音已结束 · 继续」).

extension VoiceSupport {
    /// The group's voice extension, running or not (for Open Settings when it is not ready yet).
    func voiceExtensionID(group: String) -> String? {
        probes[group]?.extensions.first { $0.kind == "voice" && VoiceAvailability.validID($0.id) }?.id
    }
}

@MainActor enum VoiceEntry {
    /// Who works in the conversation, for the bar's words: the agent (「Claude」, 「Codex」…), or the persona's name in
    /// its own conversations.
    static func label(_ session: Session?, model: AppModel) -> String {
        guard let session else { return "Claude" }
        return session.assistant ? model.persona.name : session.agentLabel
    }

    /// Starts a call bound to `key` where the page offers one; otherwise what to say about it.
    @discardableResult static func start(_ key: SessionKey, model: AppModel) -> VoiceUnavailableNote? {
        let availability = VoiceSupport.shared.availability(for: model.session(key), group: key.group)
        guard case .available(let id) = availability else {
            return VoiceUnavailableNote.make(availability, extensionID: VoiceSupport.shared.voiceExtensionID(group: key.group))
        }
        Haptics.tap()
        let recent = VoiceLine.recent(model.chat(key).items)
        var deps = VoiceDeps.live(group: key.group)
        #if DEBUG
        if VoiceSample.silentAudio { deps.audio = { VoiceSample.SilentAudio() } }
        #endif
        Task { await VoiceCalls.shared.start(key, extensionID: id, surface: .phone, recent: recent, deps: deps) }
        return nil
    }

    /// Open Settings. The voice model's use opens over the page (`modelConfigurationSheet`); the caller does that one.
    static func open(_ place: VoiceSettingsPlace, model: AppModel) {
        switch place {
        case .extensions:
            model.settingsPath = NavigationPath([SettingsPage.plugins])
            model.selectedTab = .settings
        case .voiceExtension(let id):
            model.settingsPath = NavigationPath([SettingsPage.plugins, SettingsPage.plugin(id)])
            model.selectedTab = .settings
        case .model:
            break
        case .system:
            #if os(iOS) || os(visionOS)
            if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
            #endif
        }
    }
}

extension View {
    /// The 「+」 menu's Voice Chat where the page offers no call: why, and Open Settings when there is something to set.
    func voiceUnavailableAlert(_ note: Binding<VoiceUnavailableNote?>, model: AppModel) -> some View {
        alert(note.wrappedValue?.title ?? "", isPresented: Binding(get: { note.wrappedValue != nil }, set: { if !$0 { note.wrappedValue = nil } }),
              presenting: note.wrappedValue) { shown in
            if let place = shown.settings {
                Button("Open Settings [Voice Entry 93112c]") { VoiceEntry.open(place, model: model) }
            }
            Button("OK [In App Tunnel View f867f3]", role: .cancel) {}
        } message: { shown in
            Text(shown.message)
        }
    }
}

/// Where the bar was, after a call ended by itself (docs/assistant/voice.md 1.2 L): Voice Ended, why, and Continue (or Retry), Open Settings,
/// Details as the ending allows. It stays until the page goes; Continue picks the call up with its captions and what it
/// handed over (`VoiceCalls.resume`), and the voice says what finished meanwhile.
struct VoiceEndedStrip: View {
    let call: VoiceConversation
    let note: VoiceEndNote
    @Environment(AppModel.self) private var model
    @State private var showDetails = false
    @State private var showModel = false

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.voiceChannel).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                Text("Voice ended · \(note.text)")
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                    .accessibilityIdentifier("voice-ended-text")
                Spacer(minLength: 0)
            }
            HStack(spacing: Theme.Space.m) {
                if let again = note.again {
                    Button {
                        Haptics.tap()
                        Task { await VoiceCalls.shared.resume(call.key) }
                    } label: {
                        Label(again == .resume ? String(localized: "Continue") : String(localized: "Retry [Components b8784c]"), systemImage: Theme.Icon.voiceChannel)
                            .lineLimit(1)
                    }
                    .accessibilityIdentifier("voice-continue")
                }
                if let place = note.settings {
                    Button {
                        if case .model = place { showModel = true } else { VoiceEntry.open(place, model: model) }
                    } label: {
                        Label("Open Settings [Voice Entry 93112c]", systemImage: Theme.Icon.settings).lineLimit(1)
                    }
                    .accessibilityIdentifier("voice-settings")
                }
                if note.details != nil {
                    Button { showDetails = true } label: { Label("Details [Readiness Card 979a33]", systemImage: Theme.Icon.info).lineLimit(1) }
                        .accessibilityIdentifier("voice-details")
                }
                Spacer(minLength: 0)
            }
            .font(Theme.Font.secondaryStrong)
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("voice-ended")
        .onAppear {
            VoiceHaptics.play(note.haptic)
            AccessibilityNotification.Announcement(String(localized: "Voice Ended")).post()
        }
        .alert("Details [Readiness Card 979a33]", isPresented: $showDetails) {
            Button("OK [In App Tunnel View f867f3]", role: .cancel) {}
        } message: {
            Text(note.details ?? "")
        }
        .modelConfigurationSheet(isPresented: $showModel, purposeID: "plugin." + call.extensionID)
    }
}

extension PromptItems {
    /// Voice Chat in the old layout's BeautifulUI prompt bar, beside its attachments (docs/assistant/voice.md 6.1).
    static var voice: PromptSource {
        PromptSource(id: "voice", name: String(localized: "Voice Chat"), detail: String(localized: "What you say goes to this chat"),
                     icon: .symbol(Theme.Icon.voiceChannel), kind: .attach)
    }
}

/// When the composer asks the group's hub about voice again: another group, or the app back in front.
nonisolated struct VoiceProbeID: Hashable, Sendable {
    var group: String
    var active: Bool
}

/// The composer's side of voice (docs/assistant/voice.md 6.1, 6.8): asks the group's hub whether it has a voice extension that
/// speaks protocol 2 (3.1; at most once a minute, the entry keeps the last answer meanwhile, `VoiceSupport`), says why the
/// 「+」 menu's Voice Chat cannot start one, and in DEBUG the screenshot switches (`VoiceSample`).
struct ComposerVoice: ViewModifier {
    let key: SessionKey
    @Binding var note: VoiceUnavailableNote?
    /// The app is in front.
    let active: Bool
    /// The page offers a call now.
    let offered: () -> Bool
    let start: () -> Void
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        content
            .tabActiveTask(id: VoiceProbeID(group: key.group, active: active)) {
                guard active, VoicePlatform.current == .phone, let api = model.api(key.group) else { return }
                await VoiceSupport.shared.refresh(group: key.group, api: api)
            }
            .voiceUnavailableAlert($note, model: model)
            #if DEBUG
            .task {
                guard let request = VoiceSample.request else { return }
                try? await Task.sleep(for: .seconds(2)) // past the launch's back and forth: the page that stays takes it
                // The real path against the hub (the mock's MOCK_VOICE=1): as soon as the page offers a call.
                if request == .open { for _ in 0..<30 where !offered() && !Task.isCancelled { try? await Task.sleep(for: .seconds(1)) } }
                guard !Task.isCancelled, VoiceSample.claim() else { return }
                switch request {
                case .open: start()
                case .sample(.setup): note = VoiceSample.setupNote
                case .sample(let sample): VoiceSample.start(sample, key: key, model: model)
                }
            }
            #endif
    }
}
