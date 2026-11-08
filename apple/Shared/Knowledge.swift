import Foundation

/// The knowledge base under the hub's ~/.persona/data (docs/assistant/knowledge.md): projects, chats, people, docs, meetings and
/// events, each ranked by importance (P0–P3) and relevance to the user, with free tags.
nonisolated enum KBKind: String, CaseIterable, Identifiable, Sendable {
    case projects, chats, people, docs, meetings, events, recordings
    var id: String { rawValue }
    var title: String {
        switch self {
        case .projects: String(localized: "Project")
        case .chats: String(localized: "Session")
        case .people: String(localized: "People")
        case .docs: String(localized: "Document")
        case .meetings: String(localized: "Meetings")
        case .events: String(localized: "Event [Feishu Doc Renderer a14f23]")
        case .recordings: String(localized: "Record [Recordings Section ef2821]")
        }
    }
    var symbol: String {
        switch self {
        case .projects: Theme.Icon.space
        case .chats: Theme.Icon.feishuChats
        case .people: Theme.Icon.people
        case .docs: Theme.Icon.document
        case .meetings: "video"
        case .events: "calendar"
        case .recordings: Theme.Icon.recording
        }
    }
}

nonisolated struct KBEntry: Codable, Identifiable, Hashable, Sendable {
    var kind: String
    var id: String
    var name: String
    var oneLine: String?
    var importance: String?
    var relevance: Int
    var tags: [String]?
    var updated: Date?
    var extra: String?

    enum CodingKeys: String, CodingKey {
        case kind, id, name, importance, relevance, tags, updated, extra
        case oneLine = "one_line"
    }

    /// Projects: "short · active" → Short-term / Long-term and the status.
    var projectKind: String? {
        guard kind == KBKind.projects.rawValue, let extra else { return nil }
        return extra.hasPrefix("long") ? String(localized: "Long-term") : String(localized: "Short-term")
    }
    var projectDone: Bool { kind == KBKind.projects.rawValue && (extra ?? "").hasSuffix("done") }
    /// Marked "not a project" (by hand, or because it only came from notifications).
    var projectIgnored: Bool { kind == KBKind.projects.rawValue && (extra ?? "").hasSuffix("ignored") }
}

nonisolated struct KBList: Decodable, Sendable {
    var items: [KBEntry]
    var tags: [String]?
}

nonisolated struct KBDetail: Decodable, Sendable {
    var kind: String
    var id: String
    var path: String
    var meta: JSONValue?
    var summary: String?
    var log: [String]?
    var recent: String?
    var links: [KBEntry]?
    /// A project's Moments posts (the hub's LinkMoments), newest first.
    var moments: [KBMomentRef]?
}

/// A Moments post a project shows: who, when, how many comments, the text clipped, how it bears on the project.
nonisolated struct KBMomentRef: Decodable, Identifiable, Hashable, Sendable {
    var id: String
    var author: String
    var text: String
    var at: Date?
    var comments: Int
    var note: String?
    /// What the feed it is from is called, when the hub says (`KBMoments.feedName`).
    var feed: String?
}

/// One archived Moments post: the crawler's Markdown (text and every comment), and what the archive knows of it.
nonisolated struct KBMomentPost: Decodable, Sendable {
    var markdown: String
    var post: Meta?

    nonisolated struct Meta: Decodable, Sendable {
        /// What the feed it is from is called (the connector that keeps it says); nil or empty: Moments.
        var feed: String?
    }
}

/// What the company feed is called on a page: the archive's name for it, else Moments (no tenant's own name is built in).
nonisolated enum KBMoments {
    static var defaultName: String { String(localized: "Moments") }

    static func feedName(_ feed: String?) -> String {
        let f = feed?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return f.isEmpty ? defaultName : f
    }

    /// The name of the feed a project's posts are from: theirs when they all say the same, else Moments.
    static func feedName(_ moments: [KBMomentRef]) -> String {
        let names = Set(moments.map { feedName($0.feed) })
        return names.count == 1 ? names.first! : defaultName
    }
}

nonisolated struct KBDetailResponse: Decodable, Sendable {
    var detail: KBDetail
    var agents: [Session]?
    var matches: [KBMatch]?
}

/// A change made in the app; tagging set here is kept over the automatic one.
nonisolated struct KBUpdate: Encodable, Sendable {
    var importance: String?
    var relevance: Int?
    var tags: [String]?
    var status: String?
    var projectKind: String?
    var note: String?
    var log: String?
    enum CodingKeys: String, CodingKey {
        case importance, relevance, tags, status, note, log
        case projectKind = "project_kind"
    }
}

/// One entry a search found (`/api/v1/kb-search?results=1`): the entry, how many lines matched, the first few.
nonisolated struct KBResult: Decodable, Identifiable, Hashable, Sendable {
    var entry: KBEntry
    var count: Int
    var hits: [KBHit]?
    var inName: Bool?
    var id: String { entry.kind + "/" + entry.id }
    enum CodingKeys: String, CodingKey {
        case entry, count, hits
        case inName = "in_name"
    }
}

/// A place in an entry matching the search it was opened from; a message comes with the ones around it.
nonisolated struct KBMatch: Decodable, Identifiable, Hashable, Sendable {
    var path: String
    var n: Int
    var line: String
    var day: String?
    var before: [String]?
    var after: [String]?
    var id: String { path + "#\(n)" }
    var hit: KBHit { KBHit(path: path, line: line, n: n) }
}

/// One day of a chat's archive.
nonisolated struct KBDay: Decodable, Sendable {
    struct Message: Decodable, Identifiable, Hashable, Sendable {
        var n: Int
        var text: String
        var id: Int { n }
    }
    var day: String
    var messages: [Message]
    var prev: String?
    var next: String?
}

/// An archived message, `14:03 Alice: text` (continuation lines after a newline): when, who, what.
nonisolated struct KBMessageLine: Hashable, Sendable {
    var time: String
    var sender: String
    var text: String

    init(_ raw: String) {
        let t = raw.hasPrefix("- ") ? String(raw.dropFirst(2)) : raw
        let parts = t.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: false)
        if parts.count == 2, parts[0].count == 5, parts[0].dropFirst(2).first == ":", parts[0].first?.isNumber == true,
           let colon = parts[1].range(of: ": ") {
            time = String(parts[0])
            sender = String(parts[1][..<colon.lowerBound])
            text = String(parts[1][colon.upperBound...])
        } else {
            time = ""
            sender = ""
            text = t
        }
    }
}

nonisolated struct KBHit: Decodable, Hashable, Sendable {
    var path: String
    var line: String
    var n: Int?

    /// The entry a hit is in: `chats/<id>/summary.md` → (chats, id), `docs/<id>.json` → (docs, id); nil for anything else.
    var entry: (kind: KBKind, id: String)? {
        let parts = path.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
        guard parts.count >= 2, let kind = KBKind(rawValue: parts[0]) else { return nil }
        let id = parts.count == 2 && parts[1].hasSuffix(".json") ? String(parts[1].dropLast(5)) : parts[1]
        return id.isEmpty ? nil : (kind, id)
    }

    /// The day of a message hit ("2026-10-30"), nil for the other files.
    var day: String? {
        guard path.contains("/messages/") else { return nil }
        return ((path as NSString).lastPathComponent as NSString).deletingPathExtension
    }

    /// Which file the line is from, said plainly.
    var source: String {
        let file = (path as NSString).lastPathComponent
        if let day { return String(localized: "Chat History · \(day)") }
        switch file {
        case "summary.md": return String(localized: "Profile")
        case "log.md": return String(localized: "Timeline [Knowledge View f1241a]")
        case "meta.json": return String(localized: "Info")
        default: return file.hasSuffix(".json") ? String(localized: "Info") : file
        }
    }

    /// The line without its markdown list marker, JSON key and quotes.
    var text: String {
        var t = line.trimmingCharacters(in: .whitespaces)
        for p in ["- ", "* ", "## ", "# "] where t.hasPrefix(p) { t = String(t.dropFirst(p.count)) }
        if t.hasPrefix("\""), let colon = t.range(of: "\": ") {
            t = String(t[colon.upperBound...])
        }
        t = t.trimmingCharacters(in: CharacterSet(charactersIn: " ,\""))
        return t
    }
}

/// A project's timeline line, `- 2026-10-30 23:16 · Agent · title · machine · what was said`: when, from where, what.
nonisolated struct KBLogLine: Hashable, Sendable {
    var when: String
    var source: String
    var text: String

    init(_ raw: String) {
        var t = raw.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("- ") { t = String(t.dropFirst(2)) }
        let parts = t.components(separatedBy: " · ")
        if parts.count >= 2, parts[0].first?.isNumber == true {
            when = parts[0]
            text = parts.last ?? ""
            source = parts.count > 2 ? parts[1..<(parts.count - 1)].joined(separator: " · ") : ""
        } else {
            when = ""
            source = ""
            text = t
        }
    }

    /// "2026-10-30 23:16" → "10-30 23:16" (the year only when it is not this one).
    func shortWhen(now: Date = .now) -> String {
        let year = String(Calendar.current.component(.year, from: now))
        return when.hasPrefix(year + "-") ? String(when.dropFirst(5)) : when
    }
}

extension APIClient {
    func kbList(_ kind: KBKind, tag: String? = nil) async throws -> KBList {
        try await get("/api/v1/kb/\(kind.rawValue)", query: tag.map { ["tag": $0] } ?? [:])
    }
    /// `query`: opened from a search, the places it is in come back as `matches`.
    func kbDetail(_ kind: String, id: String, query: String? = nil) async throws -> KBDetailResponse {
        try await get("/api/v1/kb/\(kind)/\(Self.kbID(id))", query: query.map { ["q": $0] } ?? [:])
    }
    func kbMoment(_ id: String) async throws -> KBMomentPost {
        try await get("/api/v1/kb/moments/\(Self.kbID(id))")
    }
    func kbDay(chat: String, day: String) async throws -> KBDay {
        try await get("/api/v1/kb/chats/\(Self.kbID(chat))/days/\(Self.kbID(day))")
    }
    func kbFind(_ q: String) async throws -> [KBResult] {
        struct R: Decodable { var results: [KBResult] }
        let r: R = try await get("/api/v1/kb-search", query: ["q": q, "results": "1"])
        return r.results
    }
    nonisolated static func kbID(_ id: String) -> String {
        id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(["/"])) ?? id
    }
    func kbUpdate(_ kind: String, id: String, _ u: KBUpdate) async throws {
        _ = try await request("PUT", "/api/v1/kb/\(kind)/\(id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(["/"])) ?? id)", body: u)
    }
    func kbSearch(_ q: String) async throws -> [KBHit] {
        struct R: Decodable { var hits: [KBHit] }
        let r: R = try await get("/api/v1/kb-search", query: ["q": q])
        return r.hits
    }
}
