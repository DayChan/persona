import SwiftUI

// Persona's main conversations on iPhone and iPad always use the new conversation layout (#2477).
// Task exchanges initially show three lines; individual messages can still be expanded. Agent conversations,
// the Mac and Vision Pro keep their platform-specific presentation (docs/apple/ios.md "New conversation layout").

nonisolated enum ChatFold: Sendable {
    case lines
}

/// Platform-specific presentation, not a user preference.
nonisolated struct ChatLayout: Equatable, Sendable {
    let on: Bool
    let fold: ChatFold = .lines
    let lines: Int = ChatLayoutSettings.defaultLines

    static let off = ChatLayout(on: false)

    var shownLines: Int? { lines }
}

extension EnvironmentValues {
    @Entry var chatLayout: ChatLayout = .off
}

/// Fixed policy. Legacy UserDefaults keys are deliberately neither read nor written; keeping their stored values
/// lets older app versions retain their preferences without affecting this version. No row observes UserDefaults.
@MainActor final class ChatLayoutSettings {
    static let shared = ChatLayoutSettings()
    nonisolated static let defaultLines = 3

    let on = true
    let fold: ChatFold = .lines
    let lines = defaultLines

    /// Accept a defaults domain so regression tests can prove old persisted and launch-argument values are ignored.
    init(defaults: UserDefaults = .standard) {}

    func layout(assistant: Bool) -> ChatLayout {
        #if os(iOS)
        return ChatLayout(on: assistant)
        #else
        return .off
        #endif
    }
}

extension Injected {
    /// Persona writing to itself: the supervision digest (`system` / `supervise`) and scheduled runs, follow-ups and
    /// wake-ups (`followup`). Drawn under the Supervision identity, "Persona → @Persona", in the new layout.
    var isPatrol: Bool { kind == "followup" || (kind == "system" && state == "supervise") }
}

/// The box a message of the new layout sits in (`Theme.ChatBox`).
struct ChatBoxed: ViewModifier {
    var fill: Color
    var on = true

    func body(content: Content) -> some View {
        if on {
            content
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.m)
                .background(fill, in: .rect(cornerRadius: Theme.Radius.bubble, style: .continuous))
        } else {
            content
        }
    }
}

extension View {
    /// In its box; `on` false preserves the baseline conversation presentation.
    func chatBoxed(_ fill: Color, on: Bool = true) -> some View { modifier(ChatBoxed(fill: fill, on: on)) }
}

/// Who talks to whom over a message of the new layout: "Persona → @task", "task → @Persona", with the identity
/// before it when it has one (Supervision). One line; the addressee in the accent.
struct ExchangeHeader: View {
    var identity: String? = nil
    var from: String
    var to: String
    var icon: String? = nil
    var tint: Color = .secondary

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            if let icon { ThemedIcon(icon).font(Theme.Font.labelStrong).foregroundStyle(tint) }
            if let identity {
                Text(identity).font(Theme.Font.captionStrong).foregroundStyle(.primary).lineLimit(1).fixedSize()
                Text(verbatim: "·").foregroundStyle(.tertiary)
            }
            Text("\(from) → \(Text(verbatim: "@" + to).foregroundStyle(Theme.accent))")
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .font(Theme.Font.captionMedium)
        .foregroundStyle(.secondary)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("exchange.header")
    }
}
