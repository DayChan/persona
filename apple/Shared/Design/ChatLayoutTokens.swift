import SwiftUI

// The new conversation layout's tokens (#1666, docs/apple/ios.md "New conversation layout"): the message boxes and the
// right-edge navigator of Persona's main conversation. A file of their own, away from Theme.swift. Shared/, so the
// watch compiles them; it draws none of it.

nonisolated extension Theme {
    /// Lightweight provenance rows are not user bubbles, regardless of the conversation layout preference.
    enum MessageSource {
        static let previewLines = 3
        static let foldCharacters = 240
    }

    /// Every message of the new layout sits in a box: the user's pale blue, Persona's and its agents' pale gray. Their
    /// words stay the primary text color in both modes (dark: deep boxes under light words), well past 4.5:1.
    enum ChatBox {
        /// The user's own messages.
        static let user = Color(light: 0xE3EDFD, dark: 0x1C2F47)
        /// Persona's answers, its messages to a task's agent, an agent's reports and Persona's notes to itself.
        static let persona = Color(light: 0xF0F0F3, dark: 0x2A2A2E)
        /// The hex values above, for the contrast test (`ChatLayoutTests`).
        static let userHex: (light: UInt32, dark: UInt32) = (0xE3EDFD, 0x1C2F47)
        static let personaHex: (light: UInt32, dark: UInt32) = (0xF0F0F3, 0x2A2A2E)
    }
}

nonisolated extension Theme.Size {
    /// The band along the conversation's right edge where a slide up or down opens the navigator (`ChatEdgeScrub`):
    /// inside the list's side margin, so no message's own touch is taken.
    static let edgeBand: CGFloat = 16
    /// A tick of the navigator, one per message the user typed, and the one under the finger.
    static let edgeTick: CGFloat = 10
    static let edgeTickPicked: CGFloat = 22
    /// The room between two ticks: at most (a short conversation spreads no further) and at least (more messages than
    /// fit show only the newest).
    static let edgeTickGapMax: CGFloat = 14
    static let edgeTickGapMin: CGFloat = 5
}
