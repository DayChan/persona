import SwiftUI

/// On the Mac, what the pointer is over shows it (#2113): a row gets the shade the shell's sidebar and Inbox rows hover
/// with (`Theme.Fill.macHover`), a message or a code block its actions. Nothing changes on the iPhone.
extension View {
    /// A shade in `shape` under the view while the pointer is over it (Mac only), `outset` past its edges (a list row
    /// whose content sits inside the row's insets).
    func macHoverShade(in shape: some Shape, outset: CGFloat = 0) -> some View { modifier(MacHoverShade(shape: shape, outset: outset)) }

    /// `actions` in a row under the view, shown while the pointer is over either (Mac only): an answer's Copy / Quote,
    /// as Codex's desktop app shows them under its replies. The row's room is always there, so nothing moves.
    func macHoverFooter(@ViewBuilder _ actions: @escaping () -> some View) -> some View {
        modifier(MacHoverFooter(actions: actions))
    }

    /// `actions` inside a code block's top trailing corner while the pointer is over it (Mac only). On the block's own
    /// color, opaque, with a hairline: over a long line cut at the edge they hid its end instead of lying on it (the
    /// glass let the words show through).
    func macHoverActions(@ViewBuilder _ actions: @escaping () -> some View) -> some View {
        modifier(MacHoverActions(actions: actions))
    }
}

private struct MacHoverShade<S: Shape>: ViewModifier {
    var shape: S
    var outset: CGFloat
    @State private var hovering = false

    func body(content: Content) -> some View {
        #if os(macOS)
        content
            .background { if hovering || MacHover.showAll { shape.fill(Theme.Fill.macHover).padding(-outset) } }
            .onHover { hovering = $0 }
        #else
        content
        #endif
    }
}

private struct MacHoverActions<Actions: View>: ViewModifier {
    var actions: () -> Actions
    @State private var hovering = false

    func body(content: Content) -> some View {
        #if os(macOS)
        content
            .overlay(alignment: .topTrailing) {
                if hovering || MacHover.showAll {
                    HStack(spacing: Theme.Space.xxs) { actions() }
                        .buttonStyle(.plain)
                        .padding(.horizontal, Theme.Space.xxs)
                        .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
                        .overlay {
                            RoundedRectangle(cornerRadius: Theme.Radius.badge, style: .continuous)
                                .strokeBorder(Theme.Fill.chip, lineWidth: Theme.Size.hairline)
                        }
                        .padding([.top, .trailing], Theme.Space.xs)
                        .transition(.opacity)
                }
            }
            .onHover { inside in withAnimation(Theme.Motion.standard) { hovering = inside } }
        #else
        content
        #endif
    }
}

private struct MacHoverFooter<Actions: View>: ViewModifier {
    var actions: () -> Actions
    @State private var hovering = false

    func body(content: Content) -> some View {
        #if os(macOS)
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            content
            HStack(spacing: Theme.Space.xxs) { actions() }
                .buttonStyle(.plain)
                .opacity(hovering || MacHover.showAll ? 1 : 0)
                .allowsHitTesting(hovering || MacHover.showAll)
        }
        .contentShape(.rect)
        .onHover { inside in withAnimation(Theme.Motion.standard) { hovering = inside } }
        #else
        content
        #endif
    }
}

/// A hover action: a symbol the size of the toolbar's, its name as the tooltip.
struct MacHoverButton: View {
    var title: String
    var symbol: String
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            ThemedIcon(symbol)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                .contentShape(.rect)
        }
        .help(title)
        .accessibilityLabel(title)
    }
}

enum MacHover {
    /// `-showMacHover YES` (DEBUG, Mac): every row and message as if the pointer were over it, for screenshots (the
    /// offscreen snapshot has no pointer).
    #if os(macOS) && DEBUG
    static let showAll = UserDefaults.standard.bool(forKey: "showMacHover")
    #else
    static let showAll = false
    #endif

    /// Puts `text` on the pasteboard and says so.
    @MainActor static func copy(_ text: String, said: String = String(localized: "Copied [Mac Hover 07d6bb]")) {
        #if os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        AppModel.shared.toast = Toast(said, style: .success)
        #endif
    }
}
