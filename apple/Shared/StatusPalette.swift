import SwiftUI

/// The agent-state colors (docs/protocol/agent-states.md): the one table the app, the watch and the Live Activity draw states
/// from. Built into all three targets, so it holds only SwiftUI and the hex helpers they share (no app types).
/// `dot` marks a state (dots, symbols, filled shapes), `text` is the same hue dark enough for small text on the page
/// (≥ 4.5:1 on white), `tint` is the soft background behind either.
nonisolated struct StatusTone: Sendable {
    let dot: Color
    let text: Color
    let tint: Color

    init(dot: Color, text: Color, tint: Color? = nil) {
        self.dot = dot
        self.text = text
        self.tint = tint ?? Color.secondary.opacity(0.08)
    }
}

nonisolated enum StatusPalette {
    static let needsYou = StatusTone(dot: Palette.systemBlue, text: Color(light: 0x0064C8, dark: 0x409CFF))
    /// Working.
    static let working = StatusTone(dot: Color(light: 0xE8890C, dark: 0xFFB224), text: Color(light: 0xA35A00, dark: 0xFFB224))
    /// Unread.
    static let unread = StatusTone(dot: Color(light: 0x30A46C, dark: 0x4CC38A), text: Color(light: 0x1D7A4C, dark: 0x4CC38A))
    static let awaitingReply = unread
    /// Waiting in the Background (#6045): working, but only waiting on background commands. Working's amber, lighter (its
    /// mark is a clock, `Theme.Icon.backgroundWaiting`); its words the page's secondary (a lighter amber is too faint for
    /// small text).
    static let backgroundWaiting = StatusTone(dot: working.dot.opacity(0.6), text: .secondary, tint: working.dot.opacity(0.08))
    /// Seen / Unknown.
    static let idle = StatusTone(dot: Color.secondary.opacity(0.55), text: .secondary, tint: Color.secondary.opacity(0.12))
    /// Not running.
    static let offline = StatusTone(dot: Color.secondary.opacity(0.3), text: .secondary, tint: Color.secondary.opacity(0.08))
}

extension Color {
    /// A light / dark pair. The watch is always dark, so it takes the dark value.
    nonisolated init(light: UInt32, dark: UInt32) {
        #if os(watchOS)
        self.init(hex: dark)
        #else
        self.init(uiColor: UIColor { $0.userInterfaceStyle == .dark ? UIColor(hex: dark) : UIColor(hex: light) })
        #endif
    }

    nonisolated init(hex: UInt32) {
        self.init(red: Double((hex >> 16) & 0xff) / 255, green: Double((hex >> 8) & 0xff) / 255, blue: Double(hex & 0xff) / 255)
    }
}

#if !os(watchOS)
extension UIColor {
    nonisolated convenience init(hex: UInt32) {
        self.init(red: CGFloat((hex >> 16) & 0xff) / 255, green: CGFloat((hex >> 8) & 0xff) / 255, blue: CGFloat(hex & 0xff) / 255, alpha: 1)
    }
}
#endif
