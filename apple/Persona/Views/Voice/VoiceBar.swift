import BeautifulUI
import SwiftUI

/// The voice conversation's bar (docs/assistant/voice.md 6.2): the composer turned into it in place, in the composer's own shape
/// (`ComposerSurface`, so every style's difference stays in there). Its top row: Mute, the orb (a tap while Persona
/// speaks stops the voice: Interrupt Voice), what the voice does, Stop. Under it the line about the bound conversation
/// (`VoiceBarLine`) with its buttons, Stop Claude among them, and ⋯: the line's buttons that do not fit, Type a Line,
/// All Captions, Voice and Model. Every button is one line. It reads the call itself; the page (ChatView) only asks whether a
/// call is bound to it, and the orb alone reads the call's levels.
struct VoiceBar: View {
    let call: VoiceConversation
    let key: SessionKey
    /// Who works in the conversation: 「Claude」, 「Codex」, the persona's name for its own.
    let label: String

    @Environment(AppModel.self) private var model
    @Environment(\.buiTheme) private var theme
    @State private var typing = false
    @State private var typed = ""
    @State private var showCaptions = false
    @State private var showModel = false
    @State private var voices: VoiceVoices?

    var body: some View {
        let session = model.session(key)
        let cards = pendingCards
        let line = VoiceBarLine.make(phase: call.phase, link: call.link, runs: call.runs, session: session, cards: cards,
                                     muted: call.muted, asking: call.askingID, notice: call.notice, label: label,
                                     dismissed: call.dismissed, resendable: Set(call.runs.map(\.id).filter { call.canResend($0) }))
        let working = [.working, .paused, .waiting].contains(line.kind)
        let status = VoiceBarStatus.make(phase: call.phase, link: call.link, muted: call.muted, working: working,
                                         tool: call.tools.last?.label)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            controls(status)
            second(line, cards: cards)
                .padding(.leading, Theme.Space.s)
                .padding(.trailing, Theme.Space.xs)
        }
        .padding(.horizontal, Theme.Space.xs)
        .padding(.top, Theme.Space.xs)
        .padding(.bottom, Theme.Space.s)
        // As tall as its words need: offered less (cards over it take the page), the line was cut to one and the state
        // beside the orb shrank. The cards scroll instead.
        .fixedSize(horizontal: false, vertical: true)
        .modifier(ComposerSurface())
        .shellScaled()
        .modifier(VoiceFeedback(call: call, kind: line.kind, text: line.text, cards: cards.map(\.id)))
        .sheet(isPresented: $showCaptions) { VoiceTranscriptSheet(call: call, persona: model.persona.name) }
        .modelConfigurationSheet(isPresented: $showModel, purposeID: "plugin." + call.extensionID)
        // The voice's model may have changed in the sheet: the call picks it up on a new connection.
        .onChange(of: showModel) { _, open in
            guard !open, let api = model.api(key.group) else { return }
            Task {
                if case .ready(let picked) = await VoiceConversation.resolveModel(api, call.extensionID) { call.change(model: picked) }
            }
        }
        .alert("Type a Line", isPresented: $typing) {
            TextField(String(localized: "What to say"), text: $typed)
            Button("Send") {
                call.say(typed)
                typed = ""
            }
            Button("Cancel", role: .cancel) { typed = "" }
        } message: {
            Text("Same as saying it; the voice handles it.")
        }
        .task(id: call.model ?? "") {
            guard let api = model.api(key.group) else { return }
            voices = try? await api.voiceVoices(call.extensionID, model: call.model)
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
            if abs(VoiceBarRoom.shared.height - h) >= 1 { VoiceBarRoom.shared.height = h }
        }
        .onDisappear { VoiceBarRoom.shared.height = 0 }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("voice-bar")
    }

    /// The bound conversation's pending cards (with `-showVoiceSample`'s made-up one in DEBUG, as the page shows it).
    private var pendingCards: [Interaction] {
        var cards = model.interactions(for: key)
        #if DEBUG
        cards += VoiceSample.cards(key)
        #endif
        return cards
    }

    // MARK: The top row

    /// Mute, the orb, the state and Stop on one row; where the state does not fit between them (the accessibility sizes:
    /// Voice Paused was cut, or Speaking wrapped mid-word), it goes under the row instead.
    private func controls(_ status: VoiceBarStatus) -> some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: Theme.Space.s) {
                muteButton
                orb(status)
                statusText(status).fixedSize()
                Spacer(minLength: 0)
                endButton
            }
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.s) {
                    muteButton
                    orb(status)
                    Spacer(minLength: 0)
                    endButton
                }
                statusText(status)
                    .padding(.leading, Theme.Space.s)
            }
        }
    }

    private func orb(_ status: VoiceBarStatus) -> some View {
        VoiceOrb(phase: status.orb, level: { [levels = call.levels, speaking = status.orb == .speaking] in
            Double(speaking ? levels.output : levels.input)
        }, interrupt: { [call] in
            Haptics.tap()
            call.interruptSpeech()
        })
    }

    private func statusText(_ status: VoiceBarStatus) -> some View {
        Text(status.text)
            .font(Theme.Font.secondaryStrong)
            .foregroundStyle(AppStyle.current.usesBeautifulUI ? theme.colors.ink : Color.primary)
            .lineLimit(1)
            .accessibilityIdentifier("voice-status")
    }

    private var muteButton: some View {
        Button {
            Haptics.tap()
            call.muted.toggle()
        } label: {
            ThemedIcon(call.muted ? Theme.Icon.voiceUnmute : Theme.Icon.voiceMute)
                .font(Theme.Font.heading)
                .fontWeight(.medium)
                .foregroundStyle(call.muted ? Theme.accent : ComposerInk.icon(theme))
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .background { if call.muted { Circle().fill(Theme.Fill.chip) } }
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(call.muted ? String(localized: "Unmute [Inbox Mutes View 8d0173]") : String(localized: "Mute [Voice Bar 7db2e1]"))
        .accessibilityIdentifier("voice-mute")
    }

    /// Stop: the phone's red, round like the composer's send.
    private var endButton: some View {
        Button {
            Haptics.tap()
            VoiceCalls.shared.end(.user, from: call.surface)
        } label: {
            ThemedIcon(Theme.Icon.voiceEnd)
                .font(Theme.Font.bodyHeavy)
                .foregroundStyle(Theme.onSolid)
                .scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
                .background(Theme.danger, in: .circle)
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Stop [Car Play Voice Screen c7b24e]")
        .accessibilityHint("Ends the voice chat; what was handed over still gets done")
        .accessibilityIdentifier("voice-end")
    }

    // MARK: The second line

    /// On one line when the words and the buttons fit beside each other; else the words over the buttons, and the
    /// buttons that still do not fit in ⋯.
    private func second(_ line: VoiceBarLine, cards: [Interaction]) -> some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: Theme.Space.s) {
                words(line).lineLimit(1).fixedSize()
                Spacer(minLength: Theme.Space.s)
                ForEach(line.actions) { action in button(action, cards: cards) }
                more(line, overflow: [], cards: cards)
            }
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                    words(line).lineLimit(4)
                    Spacer(minLength: 0)
                    if line.actions.isEmpty { more(line, overflow: [], cards: cards) }
                }
                if !line.actions.isEmpty { actionRow(line, cards: cards) }
            }
        }
        .animation(Theme.Motion.state, value: line.kind)
    }

    private func actionRow(_ line: VoiceBarLine, cards: [Interaction]) -> some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: Theme.Space.s) {
                ForEach(line.actions) { action in button(action, cards: cards) }
                Spacer(minLength: 0)
                more(line, overflow: [], cards: cards)
            }
            if line.actions.count > 1 {
                HStack(spacing: Theme.Space.s) {
                    button(line.actions[0], cards: cards)
                    Spacer(minLength: 0)
                    more(line, overflow: Array(line.actions.dropFirst()), cards: cards)
                }
            }
            HStack(spacing: Theme.Space.s) {
                Spacer(minLength: 0)
                more(line, overflow: line.actions, cards: cards)
            }
        }
    }

    /// The line's mark, its words, and how long it has been so.
    private func words(_ line: VoiceBarLine) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
            if let mark = Self.mark(line.kind) {
                ThemedIcon(mark.symbol).font(Theme.Font.labelStrong).foregroundStyle(mark.color)
            } else if [.working, .handing, .waiting, .paused].contains(line.kind) {
                Circle().fill(Theme.busy).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
            }
            VoiceLineText(text: line.text, since: line.since)
                .font(Theme.Font.secondary)
                .foregroundStyle([.needsTap, .asking, .notSent, .offline].contains(line.kind) ? Color.primary : Color.secondary)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("voice-line")
    }

    private static func mark(_ kind: VoiceBarLine.Kind) -> (symbol: String, color: Color)? {
        switch kind {
        case .needsTap, .asking: (Theme.Icon.needsYou, Theme.caution)
        case .notSent: (Theme.Icon.errorFilled, Theme.caution)
        case .offline: (Theme.Icon.offline, Theme.caution)
        case .muted: (Theme.Icon.voiceMute, Color.secondary)
        case .ready, .handing, .working, .waiting, .paused: nil
        }
    }

    private func button(_ action: VoiceBarLine.Action, cards: [Interaction]) -> some View {
        Button { perform(action, cards: cards) } label: {
            Text(title(action)).lineLimit(1)
        }
        .font(Theme.Font.secondaryStrong)
        .controlSize(.small)
        .themedButton()
        .tint(action == .stopAgent ? Theme.danger : Theme.accent)
        .fixedSize()
        .accessibilityIdentifier("voice-line-" + action.name)
    }

    /// ⋯: the line's buttons that did not fit, then what the call offers besides.
    private func more(_ line: VoiceBarLine, overflow: [VoiceBarLine.Action], cards: [Interaction]) -> some View {
        Menu {
            if !overflow.isEmpty {
                Section {
                    ForEach(overflow) { action in
                        Button(role: action == .stopAgent ? .destructive : nil) { perform(action, cards: cards) } label: {
                            Label(title(action), systemImage: Self.symbol(action))
                        }
                    }
                }
            }
            Section {
                Button { typing = true } label: { Label("Type a Line", systemImage: Theme.Icon.keyboard) }
                Button { showCaptions = true } label: { Label("All Captions", systemImage: Theme.Icon.voiceCaptions) }
                voicePicker
                Button { showModel = true } label: { Label("Model [Assistant View c98e11]", systemImage: Theme.Icon.model) }
            }
        } label: {
            ThemedIcon(Theme.Icon.more)
                .font(Theme.Font.bodyStrong)
                .foregroundStyle(ComposerInk.icon(theme))
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellXS)
                .contentShape(.rect)
        }
        .menuStyle(.button)
        .buttonStyle(.plain)
        .accessibilityLabel("More")
        .accessibilityIdentifier("voice-more")
    }

    /// Voice: the extension's voices for the call's model; picking one keeps it on this device and picks the call up
    /// with it.
    @ViewBuilder private var voicePicker: some View {
        let list = voices?.voices ?? []
        if list.isEmpty {
            Label("Voice", systemImage: Theme.Icon.voiceTimbre)
        } else {
            Picker(selection: Binding(get: { call.voice ?? voices?.default ?? "" }, set: { picked in
                VoiceTimbre.choose(picked, group: key.group, extensionID: call.extensionID)
                call.change(voice: picked)
            })) {
                ForEach(list, id: \.id) { v in Text(v.name ?? v.id).tag(v.id) }
            } label: {
                Label("Voice", systemImage: Theme.Icon.voiceTimbre)
            }
            .pickerStyle(.menu)
        }
    }

    private func title(_ action: VoiceBarLine.Action) -> String {
        switch action {
        case .stopAgent: String(localized: "Stop \(label)")
        case .resend: String(localized: "Resend")
        case .clearAndSend: String(localized: "Clear and Send")
        case .returnPlan: String(localized: "Send Back as Feedback")
        case .dismiss: String(localized: "Never Mind")
        }
    }

    private static func symbol(_ action: VoiceBarLine.Action) -> String {
        switch action {
        case .stopAgent: Theme.Icon.interrupt
        case .resend, .clearAndSend: Theme.Icon.retry
        case .returnPlan: Theme.Icon.reply
        case .dismiss: Theme.Icon.close
        }
    }

    private func perform(_ action: VoiceBarLine.Action, cards: [Interaction]) {
        Haptics.tap()
        switch action {
        case .stopAgent:
            Task { await model.interrupt(key) }
        case .resend(let run):
            call.resend(run)
        case .clearAndSend(let run):
            call.resend(run, clearingDraft: true)
        case .returnPlan(let card, _, let run):
            // The app's own answer to the card (decision 5: the user tapped it), with the refused words as the feedback.
            call.dismiss(run: run)
            guard let plan = cards.first(where: { $0.id == card }), let answer = action.resolution else { return }
            Task { await model.resolve(plan, answer) }
        case .dismiss(let run):
            call.dismiss(run: run)
        }
    }
}

/// The line's words, and how long it has been so (「Claude 在跑测试 · 2 分钟」) as one paragraph, so they wrap together at
/// large text sizes; redrawn once a minute, and nothing about the time under a minute.
private struct VoiceLineText: View {
    let text: String
    let since: Date?

    var body: some View {
        if let since {
            TimelineView(.everyMinute) { context in Text(Self.words(text, since: since, now: context.date)) }
        } else {
            Text(text)
        }
    }

    static func words(_ text: String, since: Date, now: Date) -> String {
        let seconds = now.timeIntervalSince(since)
        guard seconds >= 60 else { return text }
        return text + " · " + Duration.seconds(seconds).formatted(.units(allowed: [.hours, .minutes], width: .abbreviated))
    }
}

/// The call's cues, taps and VoiceOver words (docs/assistant/voice.md 1.2 A / I / M, 6.6): connected (a cue up and a light tap),
/// the voice's connection dropping (a low cue), a new card (a cue and a tap that asks); VoiceOver hears what asks for
/// the user or stops the voice, never the captions (they would talk over the voice and leak into the microphone).
private struct VoiceFeedback: ViewModifier {
    let call: VoiceConversation
    let kind: VoiceBarLine.Kind
    let text: String
    let cards: [String]

    func body(content: Content) -> some View {
        content
            .onChange(of: call.phase) { old, new in
                if new.isLive, !old.isLive { cue(.ready) }
                if case .reconnecting = new, !Self.reconnecting(old) { cue(.reconnecting) }
            }
            .onChange(of: call.link) { old, new in
                if new == .reconnecting, old != .reconnecting { cue(.reconnecting) }
            }
            .onChange(of: cards) { old, new in
                if !Set(new).isSubset(of: Set(old)) { cue(.attention) }
            }
            .onChange(of: kind) { _, new in
                if new.announced { AccessibilityNotification.Announcement(text).post() }
            }
    }

    private func cue(_ cue: Theme.VoiceCue) {
        call.play(cue)
        VoiceHaptics.play(cue.haptic)
    }

    private static func reconnecting(_ phase: VoicePhase) -> Bool {
        if case .reconnecting = phase { true } else { false }
    }
}

/// The voice bar's height while it is on screen: the pending cards leave room for it in a call (`PendingCards.cap`). Its
/// own object, so only the cards read it, and only in a call.
@MainActor @Observable final class VoiceBarRoom {
    static let shared = VoiceBarRoom()
    var height: CGFloat = 0
}

/// `Theme.VoiceHaptic` on the hand (`Haptics`).
enum VoiceHaptics {
    static func play(_ haptic: Theme.VoiceHaptic) {
        switch haptic {
        case .none: break
        case .tap: Haptics.tap()
        case .attention: Haptics.attention()
        case .failure: Haptics.error()
        }
    }
}
