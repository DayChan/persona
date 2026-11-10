import SwiftUI

/// Primitive colors that more than one target draws with: the app, the watch, and the widget / Live Activity extension,
/// which builds this file and StatusPalette but not Theme. Roles (accent, brand) point here; nothing else repeats them.
nonisolated enum Palette {
    /// The app icon's coral.
    static let coral = Color(light: 0xD4683F, dark: 0xE8835C)
    static let systemBlue = Color(light: 0x007AFF, dark: 0x0A84FF)
    /// A system blue dark enough for white button labels (4.7:1).
    static let buttonBlue = Color(light: 0x0071E3, dark: 0x0071E3)
    /// System red: a recording in progress (the recording Live Activity's dot, timer and Stop), as the system's own.
    static let recordingRed = Color(light: 0xFF3B30, dark: 0xFF453A)
    /// The same red dark enough for a white label on it (4.6:1): the recording Live Activity's Stop button.
    static let recordingButton = Color(light: 0xD70015, dark: 0xD70015)
}
