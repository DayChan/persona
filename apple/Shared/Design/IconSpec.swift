import SwiftUI
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

/// How a style draws SF Symbols (docs/apple/icons.md): the weight, whether filled symbols stay filled, and a few symbols
/// swapped for the one closer to the style's own glyphs. Pages never read this: they draw through `ThemedIcon`,
/// `IconTile` or `Theme.Icon.styled(_:)`, which apply it. A style without an icon language uses `.system`, which
/// changes nothing, so its screens stay pixel-identical.
nonisolated struct IconSpec: Sendable, Equatable {
    /// The weight of a symbol that sits in text or on a bare button; nil keeps the font's.
    var weight: Font.Weight?
    /// Filled symbols are drawn as their outline (`Theme.Icon.outline(_:)`), except where the fill is the meaning.
    var outline = false
    /// A symbol drawn as another one, for meanings the style's own glyphs draw differently.
    var substitutes: [String: String] = [:]

    /// The system's look: the names as written.
    static let system = IconSpec()

    /// BeautifulUI: outline glyphs with a stroke of 2 in a 24-unit box and round caps (`BUIGlyph`). Medium is the SF
    /// weight whose stroke is closest to that at text sizes; its AI mark is one four-point star, its copy two rounded squares.
    static let lineGlyphs = IconSpec(
        weight: .medium,
        outline: true,
        substitutes: [Theme.Icon.ai: "sparkle", Theme.Icon.copy: "square.on.square"]
    )
}

nonisolated extension Theme.Icon {
    /// Filled symbols whose fill is what they say: a state dot, a playing or listening control, a set filter, a pin,
    /// and round buttons drawn as a filled circle (send, hang up, resume). They stay filled in every style.
    static let keepsFill: Set<String> = [
        dot, pause, interrupt, run, playMedia, dictating, voiceUnmute, voiceResume, voiceEnd, filterActive, pinned, submit,
    ]

    /// The outline form of a filled symbol (`hand.raised.fill` → `hand.raised`); other names, and names whose outline
    /// SF Symbols does not have (an extension's own icon), unchanged.
    static func outline(_ name: String) -> String {
        guard !keepsFill.contains(name), name.hasSuffix(".fill") || name.contains(".fill.") else { return name }
        let plain = name.split(separator: ".").filter { $0 != "fill" }.joined(separator: ".")
        return SymbolCheck.exists(plain) ? plain : name
    }

    /// The name to draw for a meaning in the current style (`ComponentSpec.icons`). For components that take a symbol
    /// name (BeautifulUI's pills, tiles, empty states, tool chips); views draw `ThemedIcon`.
    static func styled(_ name: String, in spec: IconSpec = StyleSpec.current.icons) -> String {
        if let s = spec.substitutes[name] { return s }
        return spec.outline ? outline(name) : name
    }
}

/// Whether SF Symbols has a name on this OS, remembered: `outline` asks on every draw.
private nonisolated enum SymbolCheck {
    nonisolated(unsafe) private static var known: [String: Bool] = [:]
    private static let lock = NSLock()

    static func exists(_ name: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if let k = known[name] { return k }
        #if canImport(UIKit)
        let k = UIImage(systemName: name) != nil
        #else
        let k = NSImage(systemSymbolName: name, accessibilityDescription: nil) != nil
        #endif
        known[name] = k
        return k
    }
}
