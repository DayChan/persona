import SwiftUI

/// A symbol in one of the viewer bars' round buttons or capsules (`ImageViewer`, the share card's preview
/// `MessageShareSheet`): the same tap-sized square everywhere. `enabled: false` draws it dimmed, for a placeholder that
/// is not a button yet (Share before the picture is in).
struct ViewerIcon: View {
    var symbol: String
    var enabled = true
    var body: some View {
        ThemedIcon(symbol)
            .font(Theme.Font.heading)
            .fontWeight(.medium)
            .foregroundStyle(enabled ? AnyShapeStyle(Color.primary) : AnyShapeStyle(.tertiary))
            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
            .contentShape(.rect)
    }
}

/// The top bar over a full-screen picture, as Photos has it (docs/apple/ios.md「图片查看器」): a round button on each side,
/// what the page is about in between. The sides carry their own surface (`viewerRound()`); a side with nothing to
/// offer passes `ViewerBarSpacer()` so the middle stays centered.
struct ViewerTopBar<Leading: View, Center: View, Trailing: View>: View {
    @ViewBuilder var leading: Leading
    @ViewBuilder var center: Center
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            leading
            center.frame(maxWidth: .infinity)
            trailing
        }
        .padding(.horizontal, Theme.Space.page)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge) // must fit an iPhone SE's width
    }
}

/// The bottom bar under a full-screen picture, as Photos has it: a round button on each side (Share on the left) and
/// the main actions in a glass capsule between them, their buttons touching. Icons only, a fixed width per button, so
/// it fits an iPhone SE. `ImageViewer` and the share card's preview both use it, so the two always look alike.
struct ViewerBottomBar<Leading: View, Actions: View, Trailing: View>: View {
    @ViewBuilder var leading: Leading
    @ViewBuilder var actions: Actions
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            leading.viewerRound()
            Spacer(minLength: 0)
            HStack(spacing: .zero) { actions }
                .buttonStyle(.plain)
                .padding(.horizontal, Theme.Space.xs)
                .chromeSurface(in: Capsule())
            Spacer(minLength: 0)
            trailing.viewerRound()
        }
        .padding(.horizontal, Theme.Space.page)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge) // must fit an iPhone SE's width
    }
}

/// An empty side of a viewer bar, as wide as a round button, so the bar's middle stays centered.
struct ViewerBarSpacer: View {
    var body: some View {
        Color.clear
            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
            .accessibilityHidden(true)
    }
}

extension View {
    /// One of the viewer bars' round buttons: a plain button on the chrome's glass circle.
    func viewerRound() -> some View {
        buttonStyle(.plain).chromeSurface(in: Circle())
    }
}
