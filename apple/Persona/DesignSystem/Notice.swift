import SwiftUI

/// A notice at the top of a page's content (the inbox's waiting-for-triage line): a symbol in its tone's color, a sentence in
/// secondary words, and optionally one action, on a soft wash of the tone — the look of the chat's notes
/// (`AttentionNote`, `StartingNote`), on the style's surface. Not dismissable: it goes when what it says stops being so.
/// The action sits under the words on the phone (a narrow row would squeeze both) and at the trailing end on the Mac.
struct NoticeBanner<Action: View>: View {
    enum Tone {
        /// Information about the content, with nothing waiting on the user.
        case neutral
        /// Something waits on the user or is held up (a warning, not an error).
        case caution
        /// Something failed.
        case danger
    }

    var text: String
    var tone: Tone = .caution
    /// What it is about (`Theme.Icon.time`: something waits); nil: the tone's own (info, a warning, an error).
    var symbol: String? = nil
    /// Three lines by default; notices that enumerate every source pass nil so none is clipped.
    var lineLimit: Int? = 3
    @ViewBuilder var action: Action

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(symbol ?? defaultSymbol)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(color)
            #if os(macOS)
            words.frame(maxWidth: .infinity, alignment: .leading)
            button
            #else
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                words
                button
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            #endif
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
    }

    private var words: some View {
        Text(text)
            .font(Theme.Font.secondary)
            .foregroundStyle(.secondary)
            .lineLimit(lineLimit)
    }

    private var button: some View {
        action
            .font(Theme.Font.secondaryStrong)
            .lineLimit(1)
            .buttonStyle(.borderless) // in a list row: only the button takes its taps
    }

    private var defaultSymbol: String {
        switch tone {
        case .neutral: Theme.Icon.info
        case .caution: Theme.Icon.error
        case .danger: Theme.Icon.errorFilled
        }
    }

    private var color: Color {
        switch tone {
        case .neutral: .secondary
        case .caution: Theme.caution
        case .danger: Theme.danger
        }
    }
}

extension NoticeBanner where Action == EmptyView {
    init(text: String, tone: Tone = .caution, symbol: String? = nil, lineLimit: Int? = 3) {
        self.init(text: text, tone: tone, symbol: symbol, lineLimit: lineLimit) { EmptyView() }
    }
}
