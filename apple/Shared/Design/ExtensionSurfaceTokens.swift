import SwiftUI

/// System surfaces have a deliberately small, app-independent token vocabulary, also available on watchOS 10.
nonisolated enum ExtensionSurfaceTokens {
    static let actionIcon = "puzzlepiece.extension" // icon: widgets compile this file without Theme; the same symbol as Theme.Icon.skills
    static let spacing: CGFloat = 8
    static let compactSpacing: CGFloat = 4
    static let chartHeight: CGFloat = 56
    static let chartLine: CGFloat = 2
    static let chartBar: CGFloat = 12
    static let chartRadius: CGFloat = 4
    static let chartWash = 0.1
    static let label = Font.system(.caption)
    static let body = Font.system(.body)
    static let value = Font.system(.title2, design: .rounded, weight: .semibold)
    static let ink = Color.primary
    static let secondaryInk = Color.secondary
    static let surface = Color.clear
}
