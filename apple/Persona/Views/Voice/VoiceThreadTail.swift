import SwiftUI

// What a voice call puts into the conversation's page beside the bar (docs/assistant/voice.md 6.2): its captions as the thread's
// last rows, the mark on the card the voice is asking about, and what steps aside while it is on. Each reads the call
// itself, so the page (ChatView, the hot path: docs/apple/ios-state.md) only asks `VoiceCalls.isBound` and is not drawn again
// as words come in.

extension VoiceCalls {
    /// The call bound to `key`, live or ended.
    func call(on key: SessionKey) -> VoiceConversation? {
        guard let call = current, call.key == key else { return nil }
        return call
    }

    /// A call bound to `key` is on (not ended): the page's footer keeps out of its way.
    func isLive(on key: SessionKey) -> Bool {
        guard let call = call(on: key) else { return false }
        return !call.isOver
    }
}

/// The call's captions at the end of the thread (`VoiceThreadTail`): temporary rows, on this device only, not saved
/// (docs/assistant/voice.md 1.2 C). What the user says and what the voice says, the last few, in the order they were said (a late
/// caption of the user's is put before the answer it came in: `VoiceConversation.caption`, #5673); ⋯ › All Captions
/// has them all. While the chat holds back what arrives (the user reads further up, `ChatView.frozen`), it stays as it
/// was then, as the streamed words do.
struct VoiceThreadTail: View {
    let key: SessionKey
    /// The chat holds back what arrives: the rows stay as they were drawn.
    var held: Bool
    @Environment(AppModel.self) private var model
    @State private var snapshot: [VoiceCaption]?

    /// How many captions the thread shows.
    static let shown = 6

    var body: some View {
        let live = VoiceCalls.shared.call(on: key).map { Array($0.captions.suffix(Self.shown)) } ?? []
        let captions = held ? snapshot ?? live : live
        if !captions.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Label("Voice · This call only", systemImage: Theme.Icon.voiceChannel)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                ForEach(captions) { caption in row(caption) }
            }
            .padding(.leading, Theme.Space.m)
            .overlay(alignment: .leading) {
                Capsule().fill(Theme.persona.opacity(0.35)).frame(width: Theme.Size.bar)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("voice-tail")
            .onChange(of: held, initial: true) { _, now in snapshot = now ? live : nil }
        }
    }

    private func row(_ caption: VoiceCaption) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            Text(caption.isUser ? String(localized: "You") : model.persona.name)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(caption.isUser ? Color.secondary : Theme.persona)
                .lineLimit(1)
                .fixedSize()
            Text(caption.text)
                .font(Theme.Font.secondary)
                .foregroundStyle(caption.final ? Color.primary : Color.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .accessibilityElement(children: .combine)
    }
}

/// Keeps a following chat at its newest end as captions come in, as `ChatStreamFollow` does for streamed words: a new
/// line, or one that grew by a few dozen characters. A modifier of its own, so only it reads the captions.
struct VoiceTailFollow: ViewModifier {
    let key: SessionKey
    let follow: () -> Void

    private struct Mark: Equatable {
        /// The rows the tail shows: a new line, appended or put before another (#5673), even once the call keeps no more.
        var shown: [String]
        var length: Int
    }

    func body(content: Content) -> some View {
        let captions = VoiceCalls.shared.call(on: key)?.captions ?? []
        let mark = Mark(shown: captions.suffix(VoiceThreadTail.shown).map(\.id), length: (captions.last?.text.count ?? 0) / 40)
        content.onChange(of: mark) { _, _ in follow() }
    }
}

/// What steps aside while a call is on (docs/assistant/voice.md 6.2「通话期间」): the main conversation's task dock and route chip.
struct VoiceCallHides<Content: View>: View {
    let key: SessionKey
    @ViewBuilder var content: Content

    var body: some View {
        if !VoiceCalls.shared.isLive(on: key) { content }
    }
}

/// On the card the voice is asking about (the `ask` frame): Asking by Voice, so a tap and an answer said aloud are not
/// taken for two questions (docs/assistant/voice.md 1.2 H).
struct VoiceAskMark: View {
    let interaction: Interaction

    var body: some View {
        if let call = VoiceCalls.shared.call(on: interaction.sessionKey), !call.isOver, call.askingID == interaction.id {
            // The words where the card's title row has room for them, the mark alone where it has not (large text):
            // a chip that keeps its width pushed the row, the card and the page out past the screen.
            ViewThatFits(in: .horizontal) {
                Chip(text: String(localized: "Asking by Voice"), symbol: Theme.Icon.voiceChannel, look: .tint(Theme.persona))
                ThemedIcon(Theme.Icon.voiceChannel)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(Theme.persona)
                    .accessibilityLabel("Asking by Voice")
            }
            .accessibilityIdentifier("voice-asking")
        }
    }
}
