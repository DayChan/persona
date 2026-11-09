import Foundation

/// A message as the watch shows it: plain text without the markup the phone and Claude Code put into messages
/// (`<persona-context>` blocks fold into one mark, 「〔title〕」 or 「〔上下文 N 项〕」, ahead of what the user wrote, the note listing attached photos and Claude's `[Image #n]`
/// placeholders become one 「图片」 mark, sent videos 「视频」, pasted-content tags go) and without Markdown's marks (`##`, `**`, backticks,
/// fences, quotes, link targets; list dashes become •): the watch draws plain text, and the marks were shown as typed.
nonisolated enum WatchText {
    private static let context = try! NSRegularExpression(
        pattern: "<" + HubNaming.contextPattern + #"\b(?:[^>]*?title="([^"]*)")?[^>]*>[\s\S]*?</"# + HubNaming.contextPattern + #">\s*"#)
    /// The marks a phone shows for what the watch cannot draw, in the interface language.
    private static var rules: [(String, String)] {
        let image = String(localized: "[Photo]", comment: "Watch: stands in for an image in a message")
        let video = String(localized: "[Video] [Watch Text ec48e4]", comment: "Watch: stands in for a video in a message")
        let file = String(localized: "[File] [Watch Text f29940]", comment: "Watch: stands in for a file in a message")
        return [
            (#"\s*<attached_images>[\s\S]*?</attached_images>\s*"#, "\n" + image + "\n"),
            // Videos sent as files (<persona-file kind="video" …/>, VideoAttach.swift).
            (#"\s*<"# + HubNaming.filePattern + #"\b[^>]*?kind="video"[^>]*>(?:\s*</"# + HubNaming.filePattern + #">)?\s*"#, "\n" + video + "\n"),
            (#"\s*<"# + HubNaming.filePattern + #"\b[^>]*>(?:\s*</"# + HubNaming.filePattern + #">)?\s*"#, "\n" + file + "\n"),
            ("(" + NSRegularExpression.escapedPattern(for: video) + #"\s*)+"#, video + " "),
            (#"\[Image #\d+\]\s*"#, image),
            (#"</?pasted_content[^>]*>"#, ""),
            ("(" + NSRegularExpression.escapedPattern(for: image) + #"\s*)+"#, image + " "),
            // Markdown, line by line (?m): fences, headings, quotes, rules, then list dashes; inline marks after.
            (#"(?m)^[ \t]*```.*\n?"#, ""),
            (#"(?m)^#{1,6}[ \t]+"#, ""),
            (#"(?m)^[ \t]*>[ \t]?"#, ""),
            (#"(?m)^[ \t]*([-*_][ \t]*){3,}$"#, ""),
            (#"(?m)^([ \t]*)[-*+][ \t]+"#, "$1• "),
            (#"\*\*([^*\n]+)\*\*"#, "$1"),
            (#"__([^_\n]+)__"#, "$1"),
            (#"(?<![*A-Za-z0-9_])\*([^*\s][^*\n]*?)\*(?![*A-Za-z0-9_])"#, "$1"), // *italic*, also between CJK; not 2 * 3 or a*b*c
            (#"`([^`\n]+)`"#, "$1"),
            (#"\[([^\]\n]+)\]\([^)\s]+\)"#, "$1"),
            (#"\n{3,}"#, "\n\n"),
        ]
    }

    static func shown(_ raw: String) -> String {
        // The assistant's cards first: their words, not their JSON (the fence rule below would leave the JSON).
        let text = AssistantCardText.replacingCards(AssistantActionSource.stripping(MessageSource.strippingMetadata(raw)))
        let ns = text as NSString
        let blocks = context.matches(in: text, range: NSRange(location: 0, length: ns.length))
        var t = context.stringByReplacingMatches(in: text, range: NSRange(location: 0, length: ns.length), withTemplate: "")
        if let first = blocks.first {
            let title = first.range(at: 1).location == NSNotFound ? "" : ns.substring(with: first.range(at: 1))
            t = (blocks.count == 1 && !title.isEmpty ? "〔\(title)〕\n" : String(localized: "[\(blocks.count) context items]") + "\n") + t
        }
        for (pattern, with) in rules {
            t = t.replacingOccurrences(of: pattern, with: with, options: .regularExpression)
        }
        return t.replacingOccurrences(of: "&quot;", with: "\"").replacingOccurrences(of: "&lt;", with: "<")
            .replacingOccurrences(of: "&gt;", with: ">").replacingOccurrences(of: "&amp;", with: "&")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// A message on the watch's session page, read once when the page loads: what its bubble draws, not the hub's item.
/// The page kept a whole page of items and read every one on each reload, enough for watchOS to kill the app in the
/// background (#6256).
nonisolated struct WatchMessage: Identifiable, Equatable, Sendable {
    /// How many the page shows, the newest.
    static let limit = 6

    var id: String
    var ts: Date?
    var role: String
    /// The words as the watch shows them (`WatchText.shown`).
    var text: String
    var from: String?
    var clientMsgID: String?
    /// Persona's message for the model (`Injected.of`): drawn as its line.
    var injected: Injected?
    /// Sent from a card's button (`AssistantActionSource`).
    var fromCard: Bool
    /// Model-role user messages with external provenance never become the watch user's bubble.
    var source: MessageSource? = nil

    /// The newest messages of a page that have words to show, oldest first; the agent's work is left out.
    static func latest(_ page: [Item]) -> [WatchMessage] {
        var out: [WatchMessage] = []
        for item in page.reversed() where item.kind == .text {
            let raw = item.text ?? ""
            let source = MessageSource.of(item)
            let text = WatchText.shown(source == nil ? raw : MessageSource.body(item))
            guard !text.isEmpty else { continue }
            out.append(WatchMessage(id: item.id, ts: item.ts, role: item.role, text: text, from: item.from,
                                    clientMsgID: item.clientMsgID, injected: Injected.of(item),
                                    fromCard: item.role == "user" && AssistantActionSource.parse(raw) != nil, source: source))
            if out.count == limit { break }
        }
        return out.reversed()
    }
}
