import SwiftUI

// What Persona and a task's agent say to each other in the new conversation layout (#1666, `ChatLayout`): Persona's
// message to the task (feed `routed` with `from` "assistant", #1542) reads "Persona → @task", the agent's report (feed
// `report`) "task → @Persona"; what the user sends a task (`routed` without `from`) "Me → @task".
// Both on Persona's side, in its gray box, folded to the layout's lines with Expand under them. No evidence list under a
// report: the links in its words open where they are. The hub's fields as they are; nothing new from it.

/// Persona to a task's agent (`routed`): its words go to the task, not to the user.
struct RoutedExchange: View {
    var item: FeedItem
    var taskTitle: String
    var layout: ChatLayout
    private var store: TasksStore { .shared }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Button {
                if let id = item.ref, store.todos[id] != nil { store.detailTask = id }
            } label: {
                // Persona stopped the agent's turn and then told it (agent_interrupt with then, #5112).
                ExchangeHeader(identity: item.interrupted ? String(localized: "Interrupted [Chat Layout]") : nil,
                               from: ChatNames.persona, to: taskTitle,
                               icon: item.interrupted ? Theme.Icon.interrupt : nil, tint: Theme.cautionText)
            }
            .buttonStyle(.plain)
            .disabled(item.ref.flatMap { store.todos[$0] } == nil)
            ExchangeText(text: item.text ?? "", shown: layout.shownLines, pinKey: "routed-" + item.id)
                .chatBoxed(Theme.ChatBox.persona)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.trailing, Theme.Space.xxl)
        .chatPinned("routed-" + item.id)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("exchange.routed")
    }
}

/// What the user said straight to a task (its card's Message; `routed` without `from`): the user's own words, on
/// the user's side in the user's box, "Me → @task" over them. Never folded.
struct UserRoutedExchange: View {
    var item: FeedItem
    var taskTitle: String

    var body: some View {
        VStack(alignment: .trailing, spacing: Theme.Space.xs) {
            ExchangeHeader(from: String(localized: "Me"), to: taskTitle)
            Text(ExchangeText.linked(item.text ?? ""))
                .font(Theme.Font.body)
                .lineHeight(.body)
                .foregroundStyle(.primary)
                .tint(Theme.accent)
                .fixedSize(horizontal: false, vertical: true)
                .chatBoxed(Theme.ChatBox.user)
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
        .padding(.leading, Theme.Space.xxl)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("exchange.userRouted")
    }
}

/// A task's agent to Persona (`report`): a question, a block, done, or how far it got.
struct ReportExchange: View {
    var item: FeedItem
    var taskTitle: String
    var layout: ChatLayout
    private var store: TasksStore { .shared }

    var body: some View {
        let kind = item.reportKind ?? ""
        let attention = ReviewText.reportWantsAttention(kind)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Button {
                if let id = item.ref, store.todos[id] != nil { store.detailTask = id }
            } label: {
                ExchangeHeader(from: taskTitle, to: ChatNames.persona, icon: Theme.Icon.reported,
                               tint: attention ? Theme.cautionText : .secondary)
            }
            .buttonStyle(.plain)
            .disabled(item.ref.flatMap { store.todos[$0] } == nil)
            ExchangeText(text: ReviewText.reportLine(kind: kind, text: ExchangeText.withoutHeadingMarks(item.text ?? ""), step: item.step, steps: item.steps),
                         shown: layout.shownLines, pinKey: "report-" + item.id)
                .chatBoxed(Theme.ChatBox.persona)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.trailing, Theme.Space.xxl)
        .chatPinned("report-" + item.id)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("exchange.report")
    }
}

/// The names in the headers. Persona is the assistant's role here, whatever its avatar is called (as the web's
/// "task reported to Persona" line).
enum ChatNames {
    static let persona = "Persona"
}

/// Expand / Collapse under words that are folded: an exchange's (`ExchangeText`) and a long message's (`LineFold`).
/// prefix: the accessibility identifiers, "<prefix>.expand" and "<prefix>.collapse".
struct FoldToggle: View {
    var open: Bool
    var prefix: String
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: Theme.Space.xs) {
                Text(open ? String(localized: "Collapse") : String(localized: "Expand")).lineLimit(1)
                ThemedIcon(Theme.Icon.disclosure)
                    .rotationEffect(.degrees(open ? -90 : 90))
                    .accessibilityHidden(true)
            }
            .font(Theme.Font.captionStrong)
            .foregroundStyle(Theme.accent)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(prefix + (open ? ".collapse" : ".expand"))
    }
}

/// An exchange's words, folded to `shown` lines (nil: all, 0: none until Expand), with Expand / Collapse under them
/// when there is more. Links in them open as links do in the chat.
struct ExchangeText: View {
    var text: String
    var shown: Int?
    /// The row's `chatPinned` key: opening and folding keep its header where it is on screen.
    var pinKey: String
    @State private var open = false
    @State private var full: CGFloat = 0
    @State private var drawn: CGFloat = 0
    @Environment(\.chatPin) private var pin

    var body: some View {
        let limit = open ? nil : shown
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if limit != 0 {
                words
                    .lineLimit(limit)
                    .fixedSize(horizontal: false, vertical: true)
                    .background(alignment: .top) {
                        // The whole text at the same width, unseen: taller than what is drawn means there is more.
                        words.fixedSize(horizontal: false, vertical: true).hidden().accessibilityHidden(true)
                            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { full = $0 }
                    }
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { drawn = $0 }
                    .accessibilityIdentifier("exchange.text")
            }
            if folds { FoldToggle(open: open, prefix: "exchange", action: toggle) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var words: some View {
        Text(Self.linked(text))
            .font(Theme.Font.body)
            .lineHeight(.body)
            .foregroundStyle(.primary)
            .tint(Theme.accent)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// Something to open or fold: folded with words, cut at its lines, or opened.
    private var folds: Bool {
        guard !text.isEmpty, shown != nil else { return false }
        if open || shown == 0 { return true }
        return full > drawn + 1
    }

    private func toggle() {
        Haptics.tap()
        toggleInPlace(pin, pinKey, animation: Theme.Motion.state, closing: open) { open.toggle() }
    }

    nonisolated private static let memo = TextMemo<AttributedString>(limit: 512)

    /// The text as the chat shows an Agent's words (#2363): inline Markdown rendered (bold, italics, code, links; no raw
    /// `**`), line breaks kept, a heading's `#`s dropped, and bare web links made links (the chat's `openURL` opens them in
    /// the link sheet). Blocks stay lines of text so the lines can still be counted and folded.
    nonisolated static func linked(_ text: String) -> AttributedString {
        memo(text) { raw in
            let options = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace,
                                                                  failurePolicy: .returnPartiallyParsedIfPossible)
            var out = (try? AttributedString(markdown: withoutHeadingMarks(raw), options: options)) ?? AttributedString(raw)
            let plain = String(out.characters)
            guard let detector = try? NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue) else { return out }
            let ns = plain as NSString
            for m in detector.matches(in: plain, range: NSRange(location: 0, length: ns.length)) {
                guard let url = m.url, url.scheme == "http" || url.scheme == "https", let r = Range(m.range, in: plain) else { continue }
                let lo = out.characters.index(out.startIndex, offsetBy: plain.distance(from: plain.startIndex, to: r.lowerBound))
                let hi = out.characters.index(out.startIndex, offsetBy: plain.distance(from: plain.startIndex, to: r.upperBound))
                if out[lo..<hi].runs.allSatisfy({ $0.link == nil }) { out[lo..<hi].link = url }
            }
            return out
        }
    }

    /// The same text with the Markdown marks dropped, for a one-line header that can't hold styling or links.
    nonisolated static func plain(_ text: String) -> String { String(linked(text).characters) }

    /// `## Result` → `Result`: inline parsing keeps a heading's marks as text.
    nonisolated static func withoutHeadingMarks(_ text: String) -> String {
        guard text.contains("#") else { return text }
        return text.split(separator: "\n", omittingEmptySubsequences: false).map { line in
            let trimmed = line.drop { $0 == " " }
            let hashes = trimmed.prefix { $0 == "#" }
            guard (1...6).contains(hashes.count), trimmed.dropFirst(hashes.count).first == " " else { return String(line) }
            return String(trimmed.dropFirst(hashes.count + 1))
        }.joined(separator: "\n")
    }
}
