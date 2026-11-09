import BeautifulUI
import SwiftUI

// The composer's dictation and stop buttons, the shell's (docs/apple/ios.md「新外壳」): every composer draws these two, so an
// agent's chat and the assistant's look and behave alike (#1079). Each is a size up like the shell's chrome
// (`shellScaled`), wherever it sits. A field outside the composer dictates with the same tap (`FieldMicButton`).

/// Speech to text into the draft: what is heard goes after what was typed; a second tap ends it. The composer owns the
/// `Dictation` (sending, stopping and leaving the page end it too).
struct ComposerMicButton: View {
    let dictation: Dictation
    @Binding var text: String
    var id = "composer-mic"
    @Environment(\.buiTheme) private var theme

    var body: some View {
        Button { dictation.toggle(into: $text) } label: {
            ThemedIcon(dictation.isListening ? Theme.Icon.dictating : Theme.Icon.dictate)
                .font(Theme.Font.heading)
                .fontWeight(.medium)
                .foregroundStyle(dictation.isListening ? Theme.accent : ComposerInk.icon(theme))
                .symbolEffect(.pulse, isActive: dictation.isListening)
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(dictation.isListening ? String(localized: "Stop Dictation") : String(localized: "Dictate"))
        .accessibilityIdentifier(id)
        .shellScaled()
    }
}

/// The mic of a text field outside the composer (a to-do's notes, #2188): the composer's tap, drawn like the text buttons
/// beside it (the caller gives the font and color). `willStart` runs just before listening starts, for a field that has
/// to open first. The view owns the `Dictation`: saving, cancelling and leaving end it.
struct FieldMicButton: View {
    let dictation: Dictation
    @Binding var text: String
    var id: String
    var willStart: () -> Void = {}

    var body: some View {
        Button {
            if !dictation.isListening { willStart() }
            dictation.toggle(into: $text)
        } label: {
            ThemedIcon(dictation.isListening ? Theme.Icon.dictating : Theme.Icon.dictate)
                .symbolEffect(.pulse, isActive: dictation.isListening)
                .padding(.horizontal, Theme.Space.xs)
                .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .accessibilityLabel(dictation.isListening ? String(localized: "Stop Dictation") : String(localized: "Dictate"))
        .accessibilityIdentifier(id)
    }
}

extension Dictation {
    /// A mic's tap: a second one ends it; otherwise what is heard goes after what `text` holds, and why it could not
    /// start shows as a toast.
    func toggle(into text: Binding<String>) {
        if isListening { stop(); return }
        let typed = text.wrappedValue
        let base = typed.isEmpty || typed.last?.isWhitespace == true ? typed : typed + " "
        Task {
            if let problem = await start(update: { heard in text.wrappedValue = base + heard }) {
                AppModel.shared.toast = Toast(problem, style: .error)
            }
        }
    }
}

/// The composer's round button: a symbol in an ink circle, like ChatGPT's send (the style's accent tints nothing here).
struct ComposerRoundButton: View {
    var symbol: String
    var label: String
    var id: String
    var action: () -> Void
    @Environment(\.buiTheme) private var theme

    var body: some View {
        Button(action: action) {
            ThemedIcon(symbol)
                .font(Theme.Font.bodyHeavy)
                .foregroundStyle(AppStyle.current.usesBeautifulUI ? theme.colors.canvas : Theme.Shell.inkText)
                .scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
                .background(AppStyle.current.usesBeautifulUI ? theme.colors.ink : Theme.Shell.ink, in: .circle)
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
        .shellScaled()
        .transition(.scale.combined(with: .opacity))
    }
}

extension ComposerRoundButton {
    /// Interrupts the agent: shown in send's place while it works and nothing is typed.
    static func stop(id: String = "composer-stop", _ action: @escaping () -> Void) -> ComposerRoundButton {
        ComposerRoundButton(symbol: Theme.Icon.interrupt, label: String(localized: "Interrupt [Composer Buttons f9d193]"), id: id, action: action)
    }
}

/// Voice Chat from the composer (docs/assistant/voice.md 6.1, `Theme.Icon.voiceChannel`): in send's place a round ink button like
/// send; beside stop (the agent works) a plain icon like dictation's.
struct ComposerVoiceButton: View {
    var round: Bool
    var id = "composer-voice"
    var action: () -> Void
    @Environment(\.buiTheme) private var theme

    var body: some View {
        if round {
            ComposerRoundButton(symbol: Theme.Icon.voiceChannel, label: String(localized: "Voice Chat"), id: id, action: action)
        } else {
            Button(action: action) {
                ThemedIcon(Theme.Icon.voiceChannel)
                    .font(Theme.Font.heading)
                    .fontWeight(.medium)
                    .foregroundStyle(ComposerInk.icon(theme))
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .contentShape(.circle)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Voice Chat")
            .accessibilityIdentifier(id)
            .shellScaled()
            .transition(.scale.combined(with: .opacity))
        }
    }
}

/// The ink of the composer's icons: the text color, or in the BeautifulUI style its quieter second ink.
enum ComposerInk {
    static func icon(_ theme: BUITheme) -> Color { AppStyle.current.usesBeautifulUI ? theme.colors.ink2 : Color.primary }
}
