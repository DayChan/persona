import SwiftUI

/// The Milk Frog on the watch face (`PetComplication`, docs/apple/persona.md "The Milk Frog on the watch"). The watch
/// widget extension compiles this file without Theme, as it does `ExtensionSurfaceTokens`; the watch app's debug
/// preview of the faces reads it too.
nonisolated enum WatchFaceTokens {
    /// The rectangular complication's words, and how far they may shrink to fit two lines.
    static let title = Font.system(.headline)
    static let titleMinScale: CGFloat = 0.8
    /// When a stale snapshot's data is from, under the words.
    static let caption = Font.system(.caption)
    /// The disc drawn before the watch app has written a face (no frog chosen, or not fetched yet).
    static let placeholder = Color.gray.opacity(0.3)
    /// Behind the complication where the face shows a background (watchOS tints or drops it on others).
    static let background = Color.black

    /// `-openWatchItem complication`: every family at a 46 mm face's sizes, on a mock face's grey.
    static let previewSpacing: CGFloat = 8
    static let previewCircular: CGFloat = 50
    static let previewCorner: CGFloat = 32
    static let previewRectangular = CGSize(width: 172, height: 76)
    static let previewRadius: CGFloat = 8
    static let previewWash = Color.white.opacity(0.08)
    static let previewInline = Font.system(.footnote)
}
