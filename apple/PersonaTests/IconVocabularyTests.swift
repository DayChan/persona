import Foundation
import Testing
import UIKit
@testable import Persona

/// SF Symbols go through `Theme.Icon` (Shared/Design/Icons.swift): one meaning, one symbol (docs/apple/icons.md). These read
/// the sources next to this file (apple/Persona, apple/PersonaWatch, apple/Shared) and look at what is passed as
/// `systemName:`, `systemImage:` or `symbol:`, and at every dotted string that names an SF Symbol (lookup tables such as
/// `var symbol: String { switch … }`). A line ending in `// icon: <why>` is left alone (another product's icon names,
/// drawn as it draws them).
struct IconVocabularyTests {
    /// Symbols that used to stand for an error next to the triangle; the vocabulary has one.
    private static let retired: Set<String> = ["exclamationmark.circle", "exclamationmark.circle.fill", "xmark.octagon.fill"]

    private static let ios = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    private static let argument = try! NSRegularExpression(pattern: #"\b(systemName|systemImage|symbol):\s*"#)
    private static let dotted = try! NSRegularExpression(pattern: #""([a-z0-9]+(?:\.[a-z0-9]+)+)""#)
    private static let symbolLiteral = try! NSRegularExpression(pattern: #""([a-z0-9]+(?:\.[a-z0-9]+)*)""#)

    private struct Use { var file: String; var line: Int; var symbol: String; var text: String }

    /// Every (name, symbol) written as a literal in Theme.Icon; an alias (`static let reload = retry`) has none.
    private static func entries() throws -> [(name: String, symbol: String)] {
        // Theme.Icon only; Theme.Brand below it names images, not symbols.
        let src = try String(contentsOf: ios.appending(path: "Shared/Design/Icons.swift"), encoding: .utf8)
            .components(separatedBy: "enum Brand")[0]
        let re = try NSRegularExpression(pattern: #"static let (\w+) = "([^"]+)""#)
        return re.matches(in: src, range: NSRange(src.startIndex..., in: src)).map {
            (String(src[Range($0.range(at: 1), in: src)!]), String(src[Range($0.range(at: 2), in: src)!]))
        }
    }

    private static func registry() throws -> [String: String] {
        Dictionary(try entries().map { ($0.symbol, $0.name) }, uniquingKeysWith: { a, _ in a })
    }

    private static func files() -> [URL] {
        ["Persona", "PersonaWatch", "Shared"].flatMap { dir in
            (FileManager.default.enumerator(at: ios.appending(path: dir), includingPropertiesForKeys: nil)?
                .compactMap { $0 as? URL } ?? [])
                .filter { $0.pathExtension == "swift" && $0.lastPathComponent != "Icons.swift" }
        }
    }

    /// Where an argument that starts at `start` ends: the first `,` or `)` outside brackets and strings.
    private static func argumentEnd(_ s: [Character], from start: Int) -> Int {
        var depth = 0, i = start
        while i < s.count {
            switch s[i] {
            case "\"":
                i += 1
                while i < s.count, s[i] != "\"" { i += 1 }
            case "(", "[", "{": depth += 1
            case ")", "]", "}":
                if depth == 0 { return i }
                depth -= 1
            case "," where depth == 0: return i
            default: break
            }
            i += 1
        }
        return i
    }

    nonisolated(unsafe) private static var known: [String: Bool] = [:]
    private static func isSymbol(_ s: String) -> Bool {
        if let k = known[s] { return k }
        let k = UIImage(systemName: s) != nil
        known[s] = k
        return k
    }

    /// Symbol literals in symbol arguments (ternaries included) and dotted symbol names anywhere.
    private static func uses() throws -> [Use] {
        var out: [Use] = []
        for url in files() {
            let lines = try String(contentsOf: url, encoding: .utf8).components(separatedBy: "\n")
            for (n, line) in lines.enumerated() where !line.contains("// icon:") {
                let ns = line as NSString
                var found: Set<String> = []
                for m in argument.matches(in: line, range: NSRange(location: 0, length: ns.length)) {
                    let chars = Array(ns.substring(from: m.range.upperBound))
                    let arg = String(chars[..<argumentEnd(chars, from: 0)])
                    for l in symbolLiteral.matches(in: arg, range: NSRange(arg.startIndex..., in: arg)) {
                        found.insert(String(arg[Range(l.range(at: 1), in: arg)!]))
                    }
                }
                // A dotted string anywhere else that is a real SF Symbol: a symbol table outside an argument.
                for m in dotted.matches(in: line, range: NSRange(location: 0, length: ns.length)) {
                    let s = ns.substring(with: m.range(at: 1))
                    if isSymbol(s) { found.insert(s) }
                }
                for s in found.sorted() {
                    out.append(Use(file: url.lastPathComponent, line: n + 1, symbol: s, text: line.trimmingCharacters(in: .whitespaces)))
                }
            }
        }
        #expect(out.count > 50, "the app's sources are not under \(ios.path)")
        return out
    }

    /// A symbol that has a meaning in the vocabulary is written as that meaning.
    @Test func registeredSymbolsUseTheirName() throws {
        let reg = try Self.registry()
        let found = try Self.uses().filter { reg[$0.symbol] != nil }
            .map { "\($0.file):\($0.line): \"\($0.symbol)\" → Theme.Icon.\(reg[$0.symbol]!)" }
        #expect(found.isEmpty, "write these as Theme.Icon:\n\(found.joined(separator: "\n"))")
    }

    /// A symbol two files use is a shared meaning: give it a name in Icons.swift.
    @Test func sharedSymbolsAreRegistered() throws {
        var files: [String: Set<String>] = [:]
        for u in try Self.uses() { files[u.symbol, default: []].insert(u.file) }
        let found = files.filter { $0.value.count > 1 }.map { "\($0.key): \($0.value.sorted().joined(separator: ", "))" }.sorted()
        #expect(found.isEmpty, "used in more than one file, add to Theme.Icon:\n\(found.joined(separator: "\n"))")
    }

    /// One symbol, one name: a second name for the same meaning is an alias (`static let reload = retry`), and two
    /// meanings never share a symbol.
    @Test func oneNamePerSymbol() throws {
        let names = Dictionary(grouping: try Self.entries(), by: \.symbol).filter { $0.value.count > 1 }
            .map { "\($0.key): \($0.value.map(\.name).joined(separator: ", "))" }.sorted()
        #expect(names.isEmpty, "one symbol has several names; alias one meaning or give the other its own symbol:\n\(names.joined(separator: "\n"))")
    }

    /// A style that draws outlines (BeautifulUI, `IconSpec.lineGlyphs`) has an outline for every filled symbol, and its
    /// substitutes are symbols; the other styles draw every name as written.
    @Test func everyStyleCanDrawEverySymbol() throws {
        let names = try Self.entries().map(\.symbol)
        for style in AppStyle.allCases {
            let spec = style.component.icons
            let missing = names.map { Theme.Icon.styled($0, in: spec) }.filter { UIImage(systemName: $0) == nil }
            #expect(missing.isEmpty, "\(style): no such symbol for \(missing)")
            if !spec.outline && spec.substitutes.isEmpty && spec.weight == nil {
                #expect(names.allSatisfy { Theme.Icon.styled($0, in: spec) == $0 })
            }
        }
        #expect(AppStyle.allCases.filter { $0.component.icons != .system } == [.beautiful], "only BeautifulUI has its own icon language")
    }

    /// The error symbols the vocabulary replaced do not come back, in tables either.
    @Test func retiredSymbolsStayRetired() throws {
        var found: [String] = []
        for url in Self.files() {
            let lines = try String(contentsOf: url, encoding: .utf8).components(separatedBy: "\n")
            for (n, line) in lines.enumerated() where !line.contains("// icon:") {
                for s in Self.retired where line.contains("\"\(s)\"") { found.append("\(url.lastPathComponent):\(n + 1): \(s)") }
            }
        }
        #expect(found.isEmpty, "use Theme.Icon.error / errorFilled:\n\(found.joined(separator: "\n"))")
    }

    /// Every brand mark is in the app's asset catalog, an OS mark for each distribution included, and Persona's own logo
    /// for each Avatar's icon (`AppGlyph`).
    @Test @MainActor func everyBrandIsAnImage() {
        let families: [OSFamily] = [.ubuntu, .debian, .fedora, .redhat, .centos, .rocky, .alma, .arch, .alpine, .opensuse, .nixos, .linux]
        let names = [Theme.Brand.claude, Theme.Brand.openAI, Theme.Brand.trae, Theme.Brand.lark, Theme.Brand.google, Theme.Brand.gmail]
            + families.compactMap(Theme.Brand.os) + AppIconChoice.allCases.map(\.preview)
        #expect(names.count == 19)
        let missing = names.filter { UIImage(named: $0) == nil }
        #expect(missing.isEmpty, "not in Assets.xcassets: \(missing)")
    }

    /// Every name in the vocabulary is a real SF Symbol on this OS.
    @Test func everyNameIsASymbol() throws {
        let missing = try Self.registry().keys.filter { UIImage(systemName: $0) == nil }.sorted()
        #expect(missing.isEmpty, "not SF Symbols: \(missing)")
    }
}

