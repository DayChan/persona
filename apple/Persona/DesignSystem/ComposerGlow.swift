import SwiftUI
#if os(iOS) && canImport(MarqueeGlow)
import MarqueeGlow
#endif

/// Settings › Appearance › Glow While Replying (off by default): while the assistant is generating a reply, a band of
/// color travels around the chat's composer (MarqueeGlow, github.com/szupzj18/MarqueeGlow). Only the iOS app links the
/// package; Mac and Vision compile these sources too (`os(iOS)` as well as `canImport`, docs/apple/mac.md), and there the
/// switch is hidden and the modifier draws nothing. With Reduce Motion the glow stays still.
nonisolated enum ComposerGlowPrefs {
    static let enabledKey = "ui.composerGlow"

    /// Whether this build can draw the glow (the package is linked).
    static var isAvailable: Bool {
        #if os(iOS) && canImport(MarqueeGlow)
        true
        #else
        false
        #endif
    }
}

extension View {
    /// The composer's glow while a reply is generating, in the composer's shape.
    func composerGlow(_ isActive: Bool) -> some View {
        #if os(iOS) && canImport(MarqueeGlow)
        marqueeGlow(isActive: isActive, colors: MarqueeGlowColors(Theme.Shell.composerGlow),
                    lineWidth: Theme.Size.rule, cornerRadius: Theme.Shell.composerRadius)
        #else
        self
        #endif
    }
}
