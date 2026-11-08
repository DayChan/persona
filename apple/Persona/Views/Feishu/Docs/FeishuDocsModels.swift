import Foundation

// Wire types of the Lark gateway's document, drive, wiki and docs-notification routes (src/shared/contracts/documents.ts,
// drive-space.ts, wiki.ts, docs-notifications.ts, document-*.ts), plus the pure logic the screens share.
// Times stay strings: some lists send "" (discover), which JSON.decoder would turn into 1970.

// MARK: - Lists

nonisolated struct FeishuDocsIcon: Codable, Hashable, Sendable {
    var type: String
    var value: String
}

/// One row of Recent / My Space / Shared with Me / Favorites / Quick Access / Recommended (`DocumentListItem` + `DriveSpaceItem` + `DriveDiscoverItem`).
nonisolated struct FeishuDocsItem: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var token: String?
    var kind: String?
    var title: String
    var url: String
    var type: Int
    var icon: FeishuDocsIcon?
    var owner: String?
    var ownerAvatarUrl: String?
    var createdAt: String?
    var updatedAt: String?
    var editedAt: String?
    var activityAt: String?
    var mine: Bool?
    var starred: Bool?
    var pinned: Bool?
    var shortcut: Bool?
    var external: Bool?
    var sharedFolder: Bool?
    var size: Double?
    var `extension`: String?
    var supported: Bool?
    // Recommended (discover) only.
    var summary: String?
    var reason: String?
    var views: Int?
    var likes: Int?
    var comments: Int?

    var docKind: FeishuDocsKind { FeishuDocsKind(type: type, url: url, title: title, kind: kind) }
    var isFolder: Bool { docKind == .folder }
    /// The folder token a drive listing takes (`folder=`).
    var folderToken: String { token ?? id }
    var updated: Date? { updatedAt.flatMap(JSON.parseDate) }
    var activity: Date? { activityAt.flatMap(JSON.parseDate) }
    var edited: Date? { editedAt.flatMap(JSON.parseDate) }
}

nonisolated struct FeishuDocsPage: Decodable, Sendable {
    var items: [FeishuDocsItem]
    var nextCursor: String?
    var path: [Crumb]?

    nonisolated struct Crumb: Decodable, Hashable, Sendable {
        var token: String
        var name: String
    }
}

/// What a document is, for its icon, color and how the reader treats it. Types are Lark explorer object types.
nonisolated enum FeishuDocsKind: String, Sendable {
    case folder, doc, sheet, bitable, mindnote, file, slides, wiki, page, whiteboard, other

    init(type: Int, url: String, title: String = "", kind: String? = nil) {
        if kind == "folder" { self = .folder; return }
        switch type {
        case 0: self = .folder
        case 2, 22: self = .doc
        case 3: self = .sheet
        case 8: self = .bitable
        case 11: self = .mindnote
        case 12: self = .file
        case 16: self = .wiki
        case 30: self = .slides
        default: self = Self.from(url: url)
        }
    }

    /// From the link alone (`/docx/`, `/sheets/`, `/base/`…); a wiki link does not say what it holds.
    static func from(url: String) -> FeishuDocsKind {
        let parts = URL(string: url)?.pathComponents.dropFirst() ?? []
        switch parts.first ?? "" {
        case "docx", "docs", "doc": return .doc
        case "sheets", "sheet": return .sheet
        case "base", "bitable": return .bitable
        case "mindnotes", "mindnote": return .mindnote
        case "file": return .file
        case "slides": return .slides
        case "wiki": return .wiki
        case "page": return .page
        case "board", "whiteboard": return .whiteboard
        case "drive": return parts.dropFirst().first == "folder" ? .folder : .other
        default: return .other
        }
    }

    var label: String {
        switch self {
        case .folder: String(localized: "Folder [Feishu Docs Models 7c7802]")
        case .doc: String(localized: "Document")
        case .sheet: String(localized: "Sheet [Feishu Docs Models 22d9e7]")
        case .bitable: String(localized: "Base")
        case .mindnote: String(localized: "Mindnote")
        case .file: String(localized: "Files")
        case .slides: String(localized: "Slides")
        case .wiki: String(localized: "Knowledge")
        case .page: String(localized: "Pages")
        case .whiteboard: String(localized: "Whiteboard")
        case .other: String(localized: "Document")
        }
    }

    var symbol: String {
        switch self {
        case .folder: Theme.Icon.folderFilled
        case .doc: Theme.Icon.fileText
        case .sheet: Theme.Icon.fileSheet
        case .bitable: Theme.Icon.baseFilled
        case .mindnote: Theme.Icon.connectors
        case .file: Theme.Icon.fileOther
        case .slides: Theme.Icon.fileSlides
        case .wiki: "books.vertical.fill"
        case .page: "rectangle.on.rectangle"
        case .whiteboard: "scribble.variable"
        case .other: Theme.Icon.fileOther
        }
    }

    /// Feishu's own colors for each kind (LarkColor names).
    var hue: String {
        switch self {
        case .folder: "yellow"
        case .doc, .other: "blue"
        case .sheet: "green"
        case .bitable: "purple"
        case .mindnote: "wathet"
        case .file: "grey"
        case .slides: "orange"
        case .wiki: "indigo"
        case .page: "turquoise"
        case .whiteboard: "violet"
        }
    }
}

/// A file's icon from its extension (pdf, zip, image…), as Feishu tells uploaded files apart: one table for Feishu's
/// files and Lark's attachments (docs/apple/icons.md). The hue is Feishu's palette name (`LarkColor.hue`).
nonisolated enum FeishuDocsFileKind {
    static func symbol(ext: String?, title: String) -> (symbol: String, hue: String) {
        let e = (ext?.isEmpty == false ? ext! : (title.split(separator: ".").last.map(String.init) ?? "")).lowercased()
        switch e {
        case "pdf": return (Theme.Icon.filePDF, "red")
        case "zip", "rar", "7z", "tar", "gz", "tgz", "bz2": return (Theme.Icon.fileArchive, "orange")
        case "png", "jpg", "jpeg", "gif", "webp", "svg", "heic": return (Theme.Icon.fileImage, "turquoise")
        case "mp3", "wav", "m4a", "aac", "opus", "flac", "ogg": return (Theme.Icon.fileAudio, "violet")
        case "mp4", "mov", "m4v", "webm", "mkv", "avi": return (Theme.Icon.fileVideo, "carmine")
        case "xls", "xlsx", "csv", "ods", "numbers": return (Theme.Icon.fileSheet, "green")
        case "ppt", "pptx", "odp", "key": return (Theme.Icon.fileSlides, "orange")
        case "doc", "docx", "odt", "rtf", "pages": return (Theme.Icon.fileText, "blue")
        case "apk", "ipa", "dmg", "exe", "pkg": return (Theme.Icon.fileApp, "indigo")
        case "md", "txt", "json", "yaml", "yml", "js", "ts", "py", "go", "sh", "swift", "java", "kt", "rs", "c", "cpp", "h", "html",
             "htm", "css", "xml", "sql":
            return (Theme.Icon.code, "indigo")
        default: return (Theme.Icon.fileOther, "grey")
        }
    }
}

// MARK: - Reader

nonisolated struct FeishuDocsAuthority: Codable, Hashable, Sendable {
    var copy: Bool
    var print: Bool
    var export: Bool
}

/// `documents/read?format=markdown`.
nonisolated struct FeishuDocsMarkdownResponse: Decodable, Sendable {
    var type: String
    var token: String
    var title: String
    var url: String
    var permission: String?
    var authority: FeishuDocsAuthority?
    var markdown: String
    var partial: Bool?
}

/// One node of the ProseMirror-style tree `documents/read?format=json` returns.
nonisolated struct FeishuDocsNode: Codable, Hashable, Sendable {
    var type: String
    var attrs: [String: JSONValue]?
    var content: [FeishuDocsNode]?
    var marks: [FeishuDocsMark]?
    var text: String?

    func attr(_ key: String) -> String? { attrs?[key]?.string }
    func number(_ key: String) -> Double? { attrs?[key]?.double }
    func flag(_ key: String) -> Bool { attrs?[key]?.bool ?? (attrs?[key]?.string == "true") }
    var children: [FeishuDocsNode] { content ?? [] }
    var blockID: String? { attr("id") }

    /// All text below this node, mentions by their label.
    var plainText: String {
        if let text { return text }
        if type == "mention" { return attr("label") ?? "" }
        if type == "hardBreak" { return "\n" }
        if type == "equation" { return attr("latex") ?? "" }
        return children.map(\.plainText).joined()
    }
}

nonisolated struct FeishuDocsMark: Codable, Hashable, Sendable {
    var type: String
    var attrs: [String: JSONValue]?
}

/// `documents/read?format=json`: the tree plus what the reader needs around it. `base` / `spreadsheet` are only
/// checked for presence (the Base viewer is out of scope; sheets read as Markdown when export is allowed).
nonisolated struct FeishuDocsView: Decodable, Sendable {
    var title: String
    var url: String
    var token: String
    var content: FeishuDocsNode
    var outline: [FeishuDocsOutlineItem]
    var owner: FeishuDocsPerson?
    var createdAt: String?
    var updatedAt: String?
    var permission: String?
    var authority: FeishuDocsAuthority?
    var reader: String?
    var object: Object?
    var partial: Bool?
    var isBase: Bool
    var isSpreadsheet: Bool

    nonisolated struct Object: Decodable, Hashable, Sendable {
        var kind: String
        var token: String
        var `extension`: String?
        var image: String?
        var empty: Bool?
    }

    enum CodingKeys: String, CodingKey {
        case title, url, token, content, outline, owner, createdAt, updatedAt, permission, authority, reader, object, partial, base, spreadsheet
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        url = try c.decodeIfPresent(String.self, forKey: .url) ?? ""
        token = try c.decodeIfPresent(String.self, forKey: .token) ?? ""
        content = try c.decodeIfPresent(FeishuDocsNode.self, forKey: .content) ?? FeishuDocsNode(type: "doc")
        outline = try c.decodeIfPresent([FeishuDocsOutlineItem].self, forKey: .outline) ?? []
        owner = try c.decodeIfPresent(FeishuDocsPerson.self, forKey: .owner)
        createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt)
        updatedAt = try c.decodeIfPresent(String.self, forKey: .updatedAt)
        permission = try c.decodeIfPresent(String.self, forKey: .permission)
        authority = try c.decodeIfPresent(FeishuDocsAuthority.self, forKey: .authority)
        reader = try c.decodeIfPresent(String.self, forKey: .reader)
        object = try c.decodeIfPresent(Object.self, forKey: .object)
        partial = try c.decodeIfPresent(Bool.self, forKey: .partial)
        isBase = c.contains(.base)
        isSpreadsheet = c.contains(.spreadsheet)
    }
}

nonisolated struct FeishuDocsOutlineItem: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var title: String
    var level: Int
}

nonisolated struct FeishuDocsPerson: Codable, Hashable, Sendable {
    var id: String?
    var name: String
    var avatarUrl: String?
}

/// `documents/header`: where the document sits and who edited it last.
nonisolated struct FeishuDocsHeader: Decodable, Sendable {
    var token: String?
    var type: Int?
    var inMySpace: Bool?
    var path: [FeishuDocsPage.Crumb]?
    var icon: FeishuDocsIcon?
    var editedAt: String?
    var editorName: String?
}

/// `documents/reader/info`: owner, counts.
nonisolated struct FeishuDocsReaderInfo: Decodable, Sendable {
    var owner: FeishuDocsPerson?
    var creator: FeishuDocsPerson?
    var createdAt: String?
    var updatedAt: String?
    var views: Int?
    var visitors: Int?
    var viewsToday: Int?
    var likes: Int?
    var comments: Int?
}

/// `documents/ai-summary`: the cached summary (never generated here).
nonisolated struct FeishuDocsAISummary: Decodable, Sendable {
    var state: String
    var summary: String
}

/// `documents/comments`.
nonisolated struct FeishuDocsComments: Decodable, Sendable {
    var threads: [Thread]

    nonisolated struct Thread: Decodable, Identifiable, Hashable, Sendable {
        var id: String
        var whole: Bool
        var resolved: Bool
        var quote: String
        var anchorBlockId: String?
        var createdAt: String?
        var replies: [Reply]
        var visibility: String?
    }

    nonisolated struct Reply: Decodable, Identifiable, Hashable, Sendable {
        var id: String
        var author: FeishuDocsPerson
        var content: [Segment]
        var imageCount: Int?
        var createdAt: String?
        var edited: Bool?
        var reactions: [Reaction]?

        var text: String { content.map(\.text).joined() }
        var created: Date? { createdAt.flatMap(JSON.parseDate) }
    }

    nonisolated struct Segment: Decodable, Hashable, Sendable {
        var kind: String
        var text: String
        var href: String?
    }

    nonisolated struct Reaction: Decodable, Hashable, Sendable {
        var key: String
        var count: Int
    }
}

/// Open threads first (newest activity first), resolved ones after.
nonisolated func feishuDocsSortedThreads(_ threads: [FeishuDocsComments.Thread]) -> [FeishuDocsComments.Thread] {
    func last(_ t: FeishuDocsComments.Thread) -> Date {
        t.replies.compactMap(\.created).max() ?? t.createdAt.flatMap(JSON.parseDate) ?? .distantPast
    }
    return threads.sorted { a, b in
        if a.resolved != b.resolved { return !a.resolved }
        return last(a) > last(b)
    }
}

// MARK: - Markdown preparation

/// The Markdown export cut into sections that render (and scroll to) on their own.
nonisolated struct FeishuDocsMarkdownBody: Sendable, Equatable {
    nonisolated struct Chunk: Identifiable, Sendable, Equatable {
        var id: String
        var text: String
        /// Set when the chunk is a top-level table (drawn by the reader's own table, not MarkdownUI).
        var table: FeishuDocsMarkdownTable? = nil
    }

    var chunks: [Chunk]
    var outline: [FeishuDocsOutlineItem]
    var images: [String]
}

nonisolated enum FeishuDocsMarkdownExport {
    /// Turns the Lark gateway's export into what the reader shows:
    /// - drops the leading `# Title` and `[Source](<url>)` lines (the reader draws its own title),
    /// - points media at the hub (`http://<lark-gateway>/api/documents/…` → `/api/v1/lark/raw/documents/…`),
    /// - splits at headings outside code fences (and at blank lines in long runs), so one long document is many lazy rows,
    /// - lifts top-level tables into chunks of their own (the reader sizes their columns to the screen),
    /// - collects the headings (the outline) and every image (the gallery).
    static func prepare(_ markdown: String, title: String? = nil, maxLines: Int = 60) -> FeishuDocsMarkdownBody {
        var lines = rewriteMedia(markdown).components(separatedBy: "\n")
        // Leading title and source link.
        while let first = lines.first, first.trimmingCharacters(in: .whitespaces).isEmpty { lines.removeFirst() }
        if let first = lines.first, first.hasPrefix("# "), title == nil || unescape(String(first.dropFirst(2))).trimmingCharacters(in: .whitespaces) == title?.trimmingCharacters(in: .whitespaces) {
            lines.removeFirst()
        }
        while let first = lines.first, first.trimmingCharacters(in: .whitespaces).isEmpty { lines.removeFirst() }
        if let first = lines.first, first.hasPrefix("[Source](") { lines.removeFirst() }

        var chunks: [FeishuDocsMarkdownBody.Chunk] = []
        var outline: [FeishuDocsOutlineItem] = []
        var current: [String] = []
        var currentID = "md-0"
        var fence: String?
        func flush() {
            let text = current.joined(separator: "\n").trimmingCharacters(in: .newlines)
            if !text.trimmingCharacters(in: .whitespaces).isEmpty { chunks.append(.init(id: currentID, text: text)) }
            current = []
        }
        var i = 0
        while i < lines.count {
            let line = lines[i]
            defer { i += 1 }
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if let f = fence {
                current.append(line)
                if trimmed.hasPrefix(f) { fence = nil }
                continue
            }
            if trimmed.hasPrefix("```") || trimmed.hasPrefix("~~~") {
                fence = String(trimmed.prefix(3))
                current.append(line)
                continue
            }
            if line.hasPrefix("|"), i + 1 < lines.count, FeishuDocsMarkdownTable.isDelimiter(lines[i + 1]) {
                var end = i + 2
                while end < lines.count, lines[end].hasPrefix("|") { end += 1 }
                let text = lines[i..<end].joined(separator: "\n")
                if let table = FeishuDocsMarkdownTable(text) {
                    flush()
                    chunks.append(.init(id: "md-\(i)", text: text, table: table))
                    currentID = "md-\(end)"
                    i = end - 1
                    continue
                }
            }
            if let level = headingLevel(line) {
                flush()
                currentID = "md-\(i)"
                let text = unescape(String(line.drop(while: { $0 == "#" }))).trimmingCharacters(in: .whitespaces)
                if !text.isEmpty { outline.append(.init(id: currentID, title: stripInline(text), level: level)) }
                current.append(line)
                continue
            }
            // Long runs without headings: break at a blank line followed by a new top-level block.
            if current.count >= maxLines, trimmed.isEmpty, i + 1 < lines.count, startsTopLevelBlock(lines[i + 1]) {
                flush()
                currentID = "md-\(i + 1)"
                continue
            }
            current.append(line)
        }
        flush()
        return FeishuDocsMarkdownBody(chunks: chunks, outline: outline, images: images(in: chunks.map(\.text).joined(separator: "\n")))
    }

    /// `# ` … `###### ` at the start of a line.
    static func headingLevel(_ line: String) -> Int? {
        let hashes = line.prefix(while: { $0 == "#" }).count
        guard (1...6).contains(hashes), line.dropFirst(hashes).first == " " else { return nil }
        return hashes
    }

    /// A line that starts a block at column 0 that is not a continuation of a list, table or quote above it.
    static func startsTopLevelBlock(_ line: String) -> Bool {
        guard let c = line.first, c != " ", c != "\t", c != "|", c != ">" else { return false }
        return !(line.hasPrefix("- ") || line.hasPrefix("* ") || line.hasPrefix("+ ") || line.range(of: #"^\d+\. "#, options: .regularExpression) != nil)
    }

    /// The Lark gateway writes absolute links to its own `/api/documents/…` media; the phone reaches them through the hub.
    static func rewriteMedia(_ s: String) -> String {
        s.replacingOccurrences(of: #"https?://[A-Za-z0-9.\-]+(:\d+)?/api/(?!v1/)"#, with: "/api/v1/lark/raw/", options: .regularExpression)
    }

    /// Image targets in order: `![alt](<url>)` or `![alt](url)`.
    static func images(in s: String) -> [String] {
        guard let re = try? NSRegularExpression(pattern: #"!\[[^\]]*\]\(<?([^)>\s]+)>?\)"#) else { return [] }
        return re.matches(in: s, range: NSRange(s.startIndex..., in: s)).compactMap { m in
            Range(m.range(at: 1), in: s).map { String(s[$0]) }
        }
    }

    /// Removes Markdown backslash escapes (`\-`, `\+`, `\.`…) for plain display.
    static func unescape(_ s: String) -> String {
        s.replacingOccurrences(of: #"\\([\\`*_{}\[\]()#+\-.!|<>~])"#, with: "$1", options: .regularExpression)
    }

    /// Drops `**`, `*`, `` ` `` and link syntax from a heading for the outline.
    static func stripInline(_ s: String) -> String {
        var t = s.replacingOccurrences(of: #"\[([^\]]*)\]\([^)]*\)"#, with: "$1", options: .regularExpression)
        for mark in ["**", "__", "`", "~~"] { t = t.replacingOccurrences(of: mark, with: "") }
        return t.trimmingCharacters(in: .whitespaces)
    }
}

// MARK: - Node tree helpers

nonisolated enum FeishuDocsTree {
    /// Block types the reader can scroll to as rows (top-level children of `doc`).
    static func blocks(_ doc: FeishuDocsNode) -> [FeishuDocsNode] { doc.children }

    /// The number each item of an ordered list shows. Every docx list item arrives as its own one-item `orderedList`;
    /// consecutive ones keep counting unless one says `restart` (then it starts at its `start`).
    static func orderedNumbers(_ siblings: [FeishuDocsNode]) -> [Int?] {
        var out: [Int?] = []
        var next: Int?
        for n in siblings {
            guard n.type == "orderedList" else { out.append(nil); next = nil; continue }
            let start = Int(n.number("start") ?? 1)
            let value = (n.flag("restart") || next == nil) ? start : next!
            out.append(value)
            next = value + max(1, n.children.filter { $0.type == "listItem" }.count)
        }
        return out
    }

    /// Every image source in document order (the gallery pages through them).
    static func images(_ node: FeishuDocsNode) -> [String] {
        var out: [String] = []
        func walk(_ n: FeishuDocsNode) {
            if n.type == "image", let src = n.attr("src"), !src.isEmpty { out.append(src) }
            n.children.forEach(walk)
        }
        walk(node)
        return out
    }

    /// The headings as an outline, for trees whose `outline` came back empty.
    static func outline(_ doc: FeishuDocsNode) -> [FeishuDocsOutlineItem] {
        doc.children.compactMap { n in
            guard n.type == "heading", let id = n.blockID else { return nil }
            let title = n.plainText.trimmingCharacters(in: .whitespacesAndNewlines)
            return title.isEmpty ? nil : FeishuDocsOutlineItem(id: id, title: title, level: Int(n.number("level") ?? 1))
        }
    }

    /// The top-level block an outline entry lives in (headings nested in callouts or columns scroll to their container).
    static func rowID(for blockID: String, in doc: FeishuDocsNode) -> String? {
        func contains(_ n: FeishuDocsNode) -> Bool { n.blockID == blockID || n.children.contains(where: contains) }
        for (i, n) in doc.children.enumerated() where contains(n) { return rowKey(n, index: i) }
        return nil
    }

    static func rowKey(_ n: FeishuDocsNode, index: Int) -> String { n.blockID ?? "row-\(index)" }
}

/// A table laid out on a grid: each row's slots, with spanned-over positions left empty so columns line up.
nonisolated struct FeishuDocsTableLayout: Equatable, Sendable {
    nonisolated struct Slot: Equatable, Sendable {
        /// Index of the cell in its row's content, nil for a position covered by a cell above or to the left.
        var cell: Int?
        var column: Int
        var colspan: Int
        var header: Bool
    }

    var rows: [[Slot]]
    var columns: Int
    /// The author's column widths (docx `colwidth`, desktop pixels), nil where the table does not say.
    var authorWidths: [Double?]

    /// A plain grid (Markdown tables): no spans, the first row a header when `header`.
    init(rows: Int, columns: Int, header: Bool) {
        self.rows = (0..<rows).map { r in (0..<columns).map { Slot(cell: $0, column: $0, colspan: 1, header: header && r == 0) } }
        self.columns = columns
        self.authorWidths = Array(repeating: nil, count: columns)
    }

    init(_ table: FeishuDocsNode) {
        let tableRows = table.children.filter { $0.type == "tableRow" }
        var covered: [Int: Int] = [:]   // column -> rows still covered by a rowspan from above
        var out: [[Slot]] = []
        var widths: [Int: Double] = [:]
        var columns = 0
        for row in tableRows {
            var slots: [Slot] = []
            var col = 0
            func skipCovered() {
                while let left = covered[col], left > 0 {
                    slots.append(Slot(cell: nil, column: col, colspan: 1, header: false))
                    col += 1
                }
            }
            for (i, cell) in row.children.enumerated() where cell.type == "tableCell" || cell.type == "tableHeader" {
                skipCovered()
                let span = max(1, Int(cell.number("colspan") ?? 1))
                let rowspan = max(1, Int(cell.number("rowspan") ?? 1))
                slots.append(Slot(cell: i, column: col, colspan: span, header: cell.type == "tableHeader"))
                if let ws = cell.attrs?["colwidth"]?.array?.compactMap(\.double), ws.count == span {
                    for (k, w) in ws.enumerated() where widths[col + k] == nil { widths[col + k] = w }
                }
                for k in 0..<span where rowspan > 1 { covered[col + k] = rowspan }
                col += span
            }
            skipCovered()
            columns = max(columns, col)
            // A row's rowspans count down once the row is done (the ones set in this row cover the next rowspan-1 rows).
            for (c, left) in covered { covered[c] = left - 1 }
            out.append(slots)
        }
        // Pad short rows so every row spans every column.
        for r in out.indices {
            let used = out[r].reduce(0) { $0 + $1.colspan }
            for c in used..<max(used, columns) { out[r].append(Slot(cell: nil, column: c, colspan: 1, header: false)) }
        }
        self.rows = out
        self.columns = columns
        self.authorWidths = (0..<columns).map { widths[$0] }
    }

    /// What each column would like to be: the author's width, else its widest single-column cell's text.
    func naturalWidths(text: (_ row: Int, _ slot: Slot) -> String) -> [Double] {
        var content = Array(repeating: 0.0, count: columns)
        for (r, slots) in rows.enumerated() {
            for slot in slots where slot.cell != nil && slot.colspan == 1 && slot.column < columns {
                content[slot.column] = max(content[slot.column], FeishuDocsTableSizing.estimate(text(r, slot)))
            }
        }
        return (0..<columns).map { authorWidths[$0] ?? max(content[$0], FeishuDocsTableSizing.minScrollColumn) }
    }
}

/// How wide each column is drawn. A table whose columns all fit at a readable width fills (at most) the available
/// width and wraps its text; one with too many columns keeps its natural widths and scrolls sideways.
nonisolated enum FeishuDocsTableSizing {
    static let minColumn: Double = 88
    /// When scrolling, a column of short values (row numbers, flags) may stay this narrow.
    static let minScrollColumn: Double = 48
    static let maxScrollColumn: Double = 280

    /// A rough single-line width for cell text: CJK ≈ 15 pt, other characters ≈ 8 pt, plus the cell's padding.
    static func estimate(_ text: String) -> Double {
        let widest = text.split(whereSeparator: \.isNewline).map { line in
            line.unicodeScalars.reduce(0.0) { $0 + ($1.value >= 0x2E80 ? 15 : 8) }
        }.max() ?? 0
        return min(widest + 16, 640)
    }

    static func fit(natural: [Double], available: Double, minColumn: Double = minColumn,
                    maxScrollColumn: Double = maxScrollColumn) -> (widths: [Double], scrolls: Bool) {
        guard !natural.isEmpty, available > 0 else { return (natural, false) }
        let total = natural.reduce(0, +)
        if total <= available { return (natural.map { $0.rounded(.down) }, false) }
        if Double(natural.count) * minColumn <= available {
            // Narrow columns keep their width; the wide ones share what is left equally (max-min fair).
            var remaining = available
            var cap = available
            for (i, v) in natural.sorted().enumerated() {
                let share = remaining / Double(natural.count - i)
                if v <= share { remaining -= v } else { cap = share; break }
            }
            return (natural.map { min($0, cap).rounded(.down) }, false)
        }
        return (natural.map { min(maxScrollColumn, max(minScrollColumn, $0)).rounded(.down) }, true)
    }

    /// The width of a slot that spans `colspan` columns from `column`.
    static func width(_ widths: [Double], column: Int, colspan: Int) -> Double {
        (column..<min(widths.count, column + colspan)).reduce(0) { $0 + widths[$1] }
    }
}

/// A GFM table from the Markdown export: header (nil when every header cell is empty — Feishu exports headerless
/// tables that way) and body rows, each cell inline Markdown.
nonisolated struct FeishuDocsMarkdownTable: Equatable, Sendable {
    var header: [String]?
    var rows: [[String]]

    var columns: Int { max(header?.count ?? 0, rows.map(\.count).max() ?? 0) }
    /// Header first (when there is one), every row padded to the column count.
    var grid: [[String]] {
        let n = columns
        return ((header.map { [$0] } ?? []) + rows).map { $0 + Array(repeating: "", count: max(0, n - $0.count)) }
    }

    init?(_ text: String) {
        let lines = text.components(separatedBy: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        guard lines.count >= 2, lines[0].hasPrefix("|"), Self.isDelimiter(lines[1]) else { return nil }
        let head = Self.cells(lines[0])
        header = head.allSatisfy { $0.isEmpty } ? nil : head
        rows = lines.dropFirst(2).map(Self.cells)
    }

    /// `| --- | :---: |`
    static func isDelimiter(_ line: String) -> Bool {
        let cells = Self.cells(line)
        return !cells.isEmpty && cells.allSatisfy { c in !c.isEmpty && c.allSatisfy { $0 == "-" || $0 == ":" } && c.contains("-") }
    }

    /// Splits a row on unescaped pipes, dropping the outer ones.
    static func cells(_ line: String) -> [String] {
        var t = line.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("|") { t.removeFirst() }
        if t.hasSuffix("|") && !t.hasSuffix("\\|") { t.removeLast() }
        var out: [String] = []
        var cur = ""
        var escaped = false
        for ch in t {
            if escaped { cur.append(ch == "|" ? "|" : "\\\(ch)"); escaped = false; continue }
            if ch == "\\" { escaped = true; continue }
            if ch == "|" { out.append(cur.trimmingCharacters(in: .whitespaces)); cur = ""; continue }
            cur.append(ch)
        }
        if escaped { cur.append("\\") }
        out.append(cur.trimmingCharacters(in: .whitespaces))
        return out
    }

    /// One cell's Markdown as display text: `<br>` → line break, images → [Image].
    static func cellMarkdown(_ s: String) -> String {
        s.replacingOccurrences(of: #"<br\s*/?>"#, with: "\n", options: [.regularExpression, .caseInsensitive])
            .replacingOccurrences(of: #"!\[[^\]]*\]\([^)]*\)"#, with: String(localized: "[Image]"), options: .regularExpression)
    }
}

// MARK: - Errors

/// A failed Lark gateway read: the HTTP status and its `error.code` (DOCUMENT_EXPORT_FORBIDDEN, DOCUMENT_FORMAT_UNSUPPORTED…).
nonisolated struct FeishuDocsFailure: LocalizedError, Sendable {
    var status: Int
    var code: String?

    var errorDescription: String? {
        switch code {
        case "DOCUMENT_EXPORT_FORBIDDEN": String(localized: "The owner doesn't allow exporting this doc.")
        case "DOCUMENT_FORMAT_UNSUPPORTED": String(localized: "This kind of doc can't be read in Persona yet.")
        case "DOCUMENT_NOT_FOUND": String(localized: "This doc doesn't exist or was deleted.")
        case "DOCUMENT_FORBIDDEN", "DOCUMENT_PERMISSION_DENIED": String(localized: "You don't have access to this doc.")
        case "DOCUMENT_TIMEOUT": String(localized: "Lark timed out. Try again later.")
        case "DOCUMENT_INVALID_URL": String(localized: "Unrecognized doc link")
        default:
            switch status {
            case 401: String(localized: "Lark Sign-in Expired")
            case 403: String(localized: "You don't have access.")
            case 404: String(localized: "This content doesn't exist or was deleted.")
            case 502, 503, 504: String(localized: "Lark is unavailable right now. Try again later.")
            default: code ?? HTTPURLResponse.localizedString(forStatusCode: status)
            }
        }
    }

    /// `{"error":{"code":…}}` (the Lark gateway) or `{"error":"…"}` (hub).
    static func code(from data: Data) -> String? {
        guard let v = try? JSON.decoder.decode(JSONValue.self, from: data), let e = v["error"] else { return nil }
        return e["code"]?.string ?? e.string
    }
}
