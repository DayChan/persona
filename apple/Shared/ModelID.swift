import Foundation

/// A model ID typed by hand (#1644, docs/assistant/model-config.md "Typing a model ID"): the hub's `proto.ValidModelID`,
/// the same rule the hub, the manager and the node hold every model to, so the apps say why before the hub refuses.
/// A letter or digit first (or `~` and then one: a gateway's "latest" alias), then only letters, digits and
/// `. _ - / : @ [ ]`, at most 256 bytes. The web's twin is web/src/lib/model-id.ts; both are tested on the server's cases.
nonisolated enum ModelID {
    /// proto.MaxModelIDLen, in UTF-8 bytes (what the hub counts).
    static let maxLength = 256

    /// The hub takes a typed ID where its lists lack it (`typed_models`): an older one dropped an unlisted pick for the
    /// Persona chat's next message without a word, so that chip offers "Other Model…" only with it.
    static let feature = "typed_models"

    /// Why an ID breaks the rule; the first thing wrong, read left to right.
    enum Problem: Equatable, Sendable {
        case empty
        /// The first character is not a letter or digit (nor `~` and then one).
        case start
        case space
        /// A line break or another control character.
        case control
        /// A character the rule does not take (quotes, `$`, `;`, a non-ASCII letter…).
        case character(String)
        case tooLong

        /// What the field says under the ID.
        var text: String {
            switch self {
            case .empty: String(localized: "Enter a model ID")
            case .start: String(localized: "Start with a letter or digit (or ~ and then one)")
            case .space: String(localized: "A model ID can't contain spaces")
            case .control: String(localized: "A model ID can't contain line breaks or control characters")
            case .character(let c): String(localized: "“\(c)” can't be in a model ID. Use letters, digits and . _ - / : @ [ ]")
            case .tooLong: String(localized: "Too long: at most \(ModelID.maxLength) bytes")
            }
        }
    }

    /// The rule as one line, under a field that is still empty or fine.
    static var rule: String {
        String(localized: "Letters, digits and . _ - / : @ [ ], starting with a letter or digit; a gateway's latest alias can start with ~.")
    }

    static func isValid(_ id: String) -> Bool { problem(id) == nil }

    /// What is wrong with `id` as it is (nothing trimmed); nil: the hub takes it.
    static func problem(_ id: String) -> Problem? {
        let bytes = Array(id.utf8)
        guard !bytes.isEmpty else { return .empty }
        for (i, c) in bytes.enumerated() {
            if alnum(c) { continue }
            if i == 0, c == UInt8(ascii: "~"), bytes.count > 1, alnum(bytes[1]) { continue }
            if i > 0, punctuation.contains(c) { continue }
            if c == UInt8(ascii: " ") || c == UInt8(ascii: "\t") { return .space }
            if c < 0x20 || c == 0x7f { return .control }
            if i == 0 { return .start }
            return .character(character(in: id, at: i))
        }
        return bytes.count > maxLength ? .tooLong : nil
    }

    /// What a field holds as an ID: the spaces and line breaks around it dropped (a pasted ID often ends in one).
    static func cleaned(_ text: String) -> String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    /// The punctuation allowed after the first character.
    private static let punctuation: Set<UInt8> = Set(Array("._-/:@[]".utf8))

    private static func alnum(_ c: UInt8) -> Bool {
        (c >= UInt8(ascii: "a") && c <= UInt8(ascii: "z")) || (c >= UInt8(ascii: "A") && c <= UInt8(ascii: "Z"))
            || (c >= UInt8(ascii: "0") && c <= UInt8(ascii: "9"))
    }

    /// The whole character the byte at `offset` starts (every byte before it is ASCII, so it starts one).
    private static func character(in id: String, at offset: Int) -> String {
        let utf8 = id.utf8
        guard let i = utf8.index(utf8.startIndex, offsetBy: offset, limitedBy: utf8.endIndex),
              let c = id[i...].first else { return "" }
        return String(c)
    }
}
