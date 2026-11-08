import Foundation

/// Wire readers accept both generations. Writers without a hello keep the pre-rename contract;
/// capability selection belongs to each hub link, never to a process-wide or persisted switch.
nonisolated enum HubNaming {
    static let feature = "persona_contracts_v1"
    static var productPattern: String { "(?:persona|" + NSRegularExpression.escapedPattern(for: LegacyNames.product) + ")" }
    static var contextPattern: String { productPattern + "-context" }
    static var filePattern: String { productPattern + "-file" }

    static func accepts(scheme: String?) -> Bool {
        guard let scheme = scheme?.lowercased() else { return false }
        return scheme == "persona" || scheme == LegacyNames.urlScheme
    }

    #if !WIDGET_EXTENSION
    static func operationHeaders(_ id: UUID) -> [String: String] {
        let value = id.uuidString.lowercased()
        return [OpRetry.header: value, LegacyNames.operationIDHeader: value]
    }
    #endif

    /// Rewrite protocol tag names only, not product mentions, paths, URLs or JSON values in the user's text.
    /// Sanitized context bodies cannot close either generation's block (ContextText).
    static func outbound(_ text: String, canonical: Bool) -> String {
        guard !canonical else { return text }
        return text.replacingOccurrences(of: #"(<\s*/?\s*)persona-(context|file)(?=[\s/>])"#,
                                         with: "$1" + LegacyNames.product + "-$2", options: .regularExpression)
    }

    /// Dynamic compatibility keys stay outside the canonical Codable keys, so local snapshots encode canonical names.
    struct Field: CodingKey {
        let stringValue: String
        var intValue: Int? { nil }
        init(_ name: String) { stringValue = name }
        init?(stringValue: String) { self.init(stringValue) }
        init?(intValue: Int) { return nil }
    }
}
