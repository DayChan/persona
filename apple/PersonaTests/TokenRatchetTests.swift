import Foundation
import Testing

/// Literal design values in views (paddings, stack spacing, corner radii, hex colors, animations, weights added to a
/// font level, fixed frames, SF Symbols drawn without `ThemedIcon`) may only go down, file by file, until the tokens cover them. `TokenBaseline.json` next to
/// this file holds the counts; a file over its count fails. When a change lowers counts, the test fails too and says
/// so: delete TokenBaseline.json, rerun to write it again, and commit it with the change.
struct TokenRatchetTests {
    private static let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
    private static let baselineURL = here.appending(path: "TokenBaseline.json")
    /// The token tables themselves (and Shared/Design, the styles), and pictures with fixed layouts.
    private static let exempt: Set<String> = ["Theme.swift", "Icons.swift", "StatusPalette.swift", "ShareCard.swift"]

    static let metrics: [(name: String, pattern: String)] = [
        ("padding", #"\.padding\((?:[^()\n]*,\s*)?-?[0-9]"#),
        ("hstackSpacing", #"HStack\([^)\n]*spacing: *[0-9]"#),
        ("cornerRadius", #"cornerRadius: *[0-9]"#),
        ("hexColor", #"0x[0-9A-Fa-f]{6}\b"#),
        ("animation", #"(?<![\w.]Motion)\.(snappy|smooth|spring|bouncy|easeInOut|easeOut|easeIn)\b"#),
        ("levelWeight", #"Theme\.Font\.\w+\.(weight|bold)\("#),
        ("fixedFrame", #"\.frame\((?:width|height|minWidth|minHeight): *[0-9]"#),
        // An SF Symbol drawn as written, outside the style's icon language: `ThemedIcon` (docs/apple/icons.md).
        ("rawSymbol", #"\bImage\(systemName:"#),
    ]

    static func counts() throws -> [String: [String: Int]] {
        let ios = here.deletingLastPathComponent()
        var out: [String: [String: Int]] = [:]
        let res = try metrics.map { ($0.name, try NSRegularExpression(pattern: $0.pattern)) }
        for dir in ["Persona", "PersonaWatch", "Shared"] {
            let files = FileManager.default.enumerator(at: ios.appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL }
                .filter { $0.pathExtension == "swift" && !exempt.contains($0.lastPathComponent) && !$0.path.contains("/Shared/Design/") } ?? []
            for url in files {
                let text = try String(contentsOf: url, encoding: .utf8)
                let range = NSRange(text.startIndex..., in: text)
                var c: [String: Int] = [:]
                for (name, re) in res {
                    let n = re.numberOfMatches(in: text, range: range)
                    if n > 0 { c[name] = n }
                }
                if !c.isEmpty { out["\(dir)/\(url.path.components(separatedBy: "/\(dir)/").last!)"] = c }
            }
        }
        return out
    }

    @Test func literalDesignValuesOnlyGoDown() throws {
        let now = try Self.counts()
        guard let data = try? Data(contentsOf: Self.baselineURL) else {
            let enc = JSONEncoder()
            enc.outputFormatting = [.prettyPrinted, .sortedKeys]
            try enc.encode(now).write(to: Self.baselineURL)
            Issue.record("wrote \(Self.baselineURL.lastPathComponent); commit it")
            return
        }
        let base = try JSONDecoder().decode([String: [String: Int]].self, from: data)
        var up: [String] = [], down: [String] = []
        for file in Set(now.keys).union(base.keys).sorted() {
            for (m, _) in Self.metrics {
                let a = base[file]?[m] ?? 0, b = now[file]?[m] ?? 0
                if b > a { up.append("\(file) \(m): \(a) → \(b)") }
                if b < a { down.append("\(file) \(m): \(a) → \(b)") }
            }
        }
        #expect(up.isEmpty, "literal design values went up; use Theme tokens:\n\(up.joined(separator: "\n"))")
        #expect(down.isEmpty, "counts went down, good: delete TokenBaseline.json and rerun to record them:\n\(down.joined(separator: "\n"))")
    }
}
