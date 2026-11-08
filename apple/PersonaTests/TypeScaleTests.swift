import Foundation
import Testing

/// The app's text uses `Theme.Font` levels and its vertical spacing `Theme.Space` (docs/apple/ios.md「设计语言」). These read
/// the sources (apple/Persona, next to this file on the Mac the tests run from) so a system text style or a size written
/// on a page fails here instead of slowly coming back.
struct TypeScaleTests {
    /// Pages that draw at sizes of their own: the share card is a picture with a fixed layout.
    private static let exempt: Set<String> = ["ShareCard.swift"]

    private static let textStyle = try! NSRegularExpression(
        pattern: #"(\.font\(\.|\.system\(\.|(?<![\w.])Font\.)(largeTitle|title|title2|title3|headline|body|callout|subheadline|footnote|caption|caption2)\b"#)
    private static let fixedSize = try! NSRegularExpression(pattern: #"\.system\(size: *[0-9]"#)
    private static let levelWeight = try! NSRegularExpression(pattern: #"Theme\.Font\.\w+\.(weight|bold)\("#)
    private static let stackSpacing = try! NSRegularExpression(pattern: #"VStack\([^)\n]*spacing: *[1-9][0-9.]*[,)]"#)

    private static func sources() throws -> [(name: String, lines: [String])] {
        let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().appending(path: "Persona")
        let files = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: nil)?
            .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" } ?? []
        #expect(files.count > 100, "the app's sources are not at \(dir.path)")
        return try files.filter { !exempt.contains($0.lastPathComponent) }.map {
            ($0.lastPathComponent, try String(contentsOf: $0, encoding: .utf8).components(separatedBy: "\n"))
        }
    }

    private static func hits(_ re: NSRegularExpression, allowed: (String) -> Bool = { _ in false }) throws -> [String] {
        var out: [String] = []
        for (name, lines) in try sources() {
            for (i, line) in lines.enumerated() where !line.trimmingCharacters(in: .whitespaces).hasPrefix("//") {
                let ns = line as NSString
                if re.firstMatch(in: line, range: NSRange(location: 0, length: ns.length)) != nil, !allowed(line) {
                    out.append("\(name):\(i + 1): \(line.trimmingCharacters(in: .whitespaces))")
                }
            }
        }
        return out
    }

    /// `.font(.subheadline)` and the like: use the level (`Theme.Font.secondary`). A different design (a serif quote)
    /// says so with `// type-scale:` on the line.
    @Test func textUsesTheLevels() throws {
        let found = try Self.hits(Self.textStyle) { $0.contains("// type-scale:") }
        #expect(found.isEmpty, "system text styles on a page:\n\(found.joined(separator: "\n"))")
    }

    /// `.system(size: 14)` ignores Dynamic Type and Small / Standard / Large. Allowed for SF Symbols (`Image(systemName:)` or `ThemedIcon(` on
    /// the line) and glyphs sized to a frame, marked `// type-scale: glyph …`.
    @Test func noFixedTextSizes() throws {
        let found = try Self.hits(Self.fixedSize) { $0.contains("Image(systemName") || $0.contains("ThemedIcon(") || $0.contains("// type-scale: glyph") }
        #expect(found.isEmpty, "fixed sizes:\n\(found.joined(separator: "\n"))")
    }

    /// Emphasis is a token (`Theme.Font.labelStrong`), not a weight added to a level on the page.
    @Test func levelsCarryTheirWeight() throws {
        let found = try Self.hits(Self.levelWeight)
        #expect(found.isEmpty, "a weight added to a level:\n\(found.joined(separator: "\n"))")
    }

    /// Sidebar navigation and conversation names read at the same Dynamic Type size as the messages. The shell's
    /// enlarged chrome must not wrap the sidebar as a whole (both already use Theme.Font.body).
    @Test func shellSidebarKeepsTheReadingSize() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        let source = try String(contentsOf: root.appending(path: "Persona/Views/Shell/ShellView.swift"), encoding: .utf8)
        let start = try #require(source.range(of: "private var sidebar: some View {"))
        let end = try #require(source.range(of: "private func go(", range: start.upperBound..<source.endIndex))
        let sidebar = source[start.lowerBound..<end.lowerBound]
        #expect(sidebar.contains("AssistantSidebar("))
        #expect(!sidebar.contains(".shellScaled()"), "sidebar text must not be one Dynamic Type step larger than messages")
    }

    /// Vertical stacks space their lines with `Theme.Space` (4 pt grid); 0 is fine.
    @Test func verticalSpacingIsOnTheGrid() throws {
        let found = try Self.hits(Self.stackSpacing)
        #expect(found.isEmpty, "spacing written as a number:\n\(found.joined(separator: "\n"))")
    }
}
