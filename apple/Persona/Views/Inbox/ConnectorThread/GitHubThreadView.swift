import MarkdownUI
import SwiftUI

// A GitHub pull request or issue opened from the inbox, as GitHub's own thread page: a header (repository and number,
// title, state, who opened it, branches, labels, Open in GitHub), the description in Markdown, then the timeline —
// comments, reviews in their state's color, comments on a line with the diff above them, CI checks (a failure with
// the end of its log) and the gray lines of what happened. Older entries load between the description and the
// timeline, as GitHub's 「Load more」. With a reply action, a box at the bottom: the comment goes out as github_comment
// as the user wrote it, without a card (#1190). AI Draft Reply / Hand to AI stay in the inbox's bar above.

struct GitHubThreadView: View {
    var model: ConnectorThreadModel
    var item: InboxItem
    @Environment(AppModel.self) private var app
    @State private var position = ScrollPosition(idType: String.self)
    @State private var link: LinkTarget?
    @State private var quoted: ThreadMessage?
    @State private var text = ""
    @State private var selection: TextSelection?
    @State private var olderFailed = false
    @FocusState private var focused: Bool

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Theme.Space.m) {
                if let chat = model.chat {
                    GitHubThreadHeader(chat: chat, connector: model.connector, item: item) { open($0) }
                    GitHubDescription(chat: chat, connector: model.connector)
                }
                older
                ForEach(GitHubThread.entries(model.messages)) { entry in
                    Group {
                        switch entry {
                        case .message(let m):
                            if m.isEvent {
                                GitHubEventLine(message: m)
                            } else {
                                GitHubCommentCard(connector: model.connector, message: m, canReply: model.chat?.reply != nil,
                                                  quote: { quote($0) }, open: { open($0) })
                            }
                        case .checks(let checks):
                            GitHubChecksCard(checks: checks) { open($0) }
                        }
                    }
                    .id(entry.id)
                }
            }
            .scrollTargetLayout()
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.m)
        }
        .scrollPosition($position)
        .scrollDismissesKeyboard(.interactively)
        .themedGroupedPage()
        .overlay {
            if !model.loaded { ProgressView() }
        }
        .safeAreaInset(edge: .top) {
            if let line = model.failure {
                Label(line, systemImage: Theme.Icon.error)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .surface(in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.top, Theme.Space.xs)
                    .accessibilityIdentifier("thread.notice")
            }
        }
        .safeAreaInset(edge: .bottom) {
            if model.chat?.reply != nil {
                ThreadComposer(placeholder: String(localized: "Comment on #\(String(model.thread.split(separator: "#").last ?? ""))"), text: $text, selection: $selection,
                               focused: $focused, quote: quoted.map { ($0.isMine ? String(localized: "Quoting yourself") : String(localized: "Quoting \($0.sender.name)"), $0.summary) },
                               sending: model.sending, cancelQuote: { quoted = nil }, send: send)
            }
        }
        .task {
            if !model.loaded { await model.load() }
        }
        #if os(macOS)
        .macLinkPanel($link)
        #else
        .sheet(item: $link) { t in
            if let key = app.assistantMain ?? app.assistantCurrent { LinkSheet(target: t, key: key) }
        }
        #endif
    }

    /// Between the description and the timeline: the entries before the oldest shown.
    @ViewBuilder private var older: some View {
        if model.hasOlder {
            Button {
                Task {
                    let ok = await model.loadOlder()
                    olderFailed = !ok && model.hasOlder
                }
            } label: {
                Group {
                    if model.loadingOlder {
                        ProgressView()
                    } else {
                        Label(olderFailed ? String(localized: "Reload Earlier Activity") : String(localized: "Show Earlier Activity"), systemImage: olderFailed ? Theme.Icon.retry : Theme.Icon.expand)
                            .font(Theme.Font.captionStrong)
                            .lineLimit(1)
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, Theme.Space.s)
            }
            .buttonStyle(.borderless)
            .accessibilityIdentifier("github.older")
        }
    }

    private func open(_ raw: String) {
        if let url = URL(string: raw) { link = LinkTarget(url: url) }
    }

    private func quote(_ m: ThreadMessage) {
        quoted = m
        focused = true
    }

    private func send() {
        let words = GitHubThread.quoting(quoted, reply: text.trimmingCharacters(in: .whitespacesAndNewlines))
        Task {
            if await model.reply(words, to: nil) {
                text = ""
                quoted = nil
                Haptics.success()
                withAnimation(Theme.Motion.standard) { position.scrollTo(edge: .bottom) }
            } else {
                Haptics.attention()
            }
        }
    }
}

/// The pull request's or issue's header: repository and number, title, state, who opened it, branches, labels, and
/// Open in GitHub.
struct GitHubThreadHeader: View {
    var chat: ThreadChat
    var connector: String
    var item: InboxItem
    var open: (String) -> Void

    private var pr: Bool { chat.kind == "pr" }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.sm) {
                BrandMark(Theme.Brand.github, side: Theme.Size.iconS)
                Text(GitHubThread.reference(chat.id)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                    .truncationMode(.middle)
                if let label = item.kindLabel {
                    Chip(text: label, look: item.kind == "mention" ? .tone(StatusPalette.needsYou) : .neutral)
                }
            }
            Text(chat.name).font(Theme.Font.title).textSelection(.enabled)
            HStack(spacing: Theme.Space.s) {
                Chip(text: GitHubThread.stateTitle(chat.state), symbol: stateSymbol, look: .tint(stateColor))
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("Status: \(GitHubThread.stateTitle(chat.state))") // not the symbol's name
                    .accessibilityIdentifier("github.state")
                if let a = chat.author {
                    ThreadAvatar(connector: connector, name: a.name, ref: a.avatar, size: Theme.Size.iconS)
                    Text("Opened by \(a.name)").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            if let b = chat.branch {
                Label {
                    Text("\(b.head) → \(b.base)").font(Theme.Font.code).lineLimit(1).truncationMode(.middle)
                } icon: {
                    ThemedIcon(Theme.Icon.gitBranch).font(Theme.Font.caption)
                }
                .foregroundStyle(.secondary)
                .accessibilityLabel("Merge \(b.head) into \(b.base)")
            }
            if let labels = chat.labels, !labels.isEmpty {
                FlowLayoutRow {
                    ForEach(labels, id: \.name) { GitHubLabelChip(label: $0) }
                }
            }
            if let url = chat.url {
                Divider().padding(.vertical, Theme.Space.xs)
                Button { open(url) } label: {
                    Label("Open in GitHub", systemImage: Theme.Icon.safari).font(Theme.Font.secondaryMedium).lineLimit(1)
                }
                .buttonStyle(.borderless)
                .accessibilityIdentifier("github.open")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.l)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("github.header")
    }

    private var stateSymbol: String {
        switch chat.state {
        case "merged": Theme.Icon.merged
        case "closed": pr ? Theme.Icon.pullRequest : Theme.Icon.success
        default: pr ? Theme.Icon.pullRequest : Theme.Icon.issueOpen
        }
    }

    /// GitHub's: open green, merged and a finished issue purple, a closed pull request red, a draft gray.
    private var stateColor: Color {
        switch chat.state {
        case "merged": Theme.Hue.purple
        case "closed": pr ? Theme.danger : Theme.Hue.purple
        case "draft": Theme.Hue.gray
        default: Theme.positive
        }
    }
}

/// A label in its repository's color: a soft fill of it, the words in the primary color (a light label's own color
/// would not read as text).
struct GitHubLabelChip: View {
    var label: ThreadChat.Label

    var body: some View {
        let color = label.color.flatMap(PersonaPalette.hex).map(Color.init(hex:)) ?? Theme.Hue.gray
        Chip(text: label.name, look: .tint(color.opacity(0.28), text: .primary))
    }
}

/// The description, as the first comment: who opened it, in Markdown.
struct GitHubDescription: View {
    var chat: ThreadChat
    var connector: String

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let a = chat.author {
                GitHubByline(connector: connector, person: a, detail: String(localized: "description"), at: nil)
            }
            if let body = chat.body, !body.isEmpty {
                GitHubMarkdown(text: body)
            } else {
                Text("No description").font(Theme.Font.secondary).foregroundStyle(.tertiary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.m)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("github.description")
    }
}

/// Who wrote it, then what they did (a review's state, in its color) and when.
struct GitHubByline: View {
    var connector: String
    var person: ThreadPerson
    var detail: String?
    var detailSymbol: String? = nil
    var detailColor: Color = .secondary
    var at: Date?

    var body: some View {
        HStack(spacing: Theme.Space.sm) {
            ThreadAvatar(connector: connector, name: person.name, ref: person.avatar, size: Theme.Size.iconS)
            Text(person.name).font(Theme.Font.captionStrong).lineLimit(1)
            if let detail {
                Label {
                    Text(detail).lineLimit(1)
                } icon: {
                    if let detailSymbol { ThemedIcon(detailSymbol) }
                }
                .labelStyle(.titleAndIcon)
                .font(Theme.Font.captionMedium)
                .foregroundStyle(detailColor)
            }
            Spacer(minLength: Theme.Space.s)
            if let at {
                Text(at, format: .relative(presentation: .named)).font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
    }
}

/// A comment, a review, or a comment on a line (the file and line, the diff above it, what it answers).
struct GitHubCommentCard: View {
    var connector: String
    var message: ThreadMessage
    var canReply: Bool
    var quote: (ThreadMessage) -> Void
    var open: (String) -> Void

    private var m: ThreadMessage { message }
    private var words: String { m.text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "" }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            GitHubByline(connector: connector, person: m.sender, detail: detail, detailSymbol: reviewSymbol, detailColor: reviewColor, at: m.at)
            if let code = m.code { GitHubCodeRef(code: code) }
            if let q = m.replyTo {
                QuoteCard(heading: q.from ?? String(localized: "Comments"), summary: q.text.flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "(Original comment)"), tint: LarkColor.blue,
                          ink: .primary) {}
            }
            if !words.isEmpty { GitHubMarkdown(text: words) }
            if m.edited == true {
                Text("Edited").font(Theme.Font.caption).foregroundStyle(.tertiary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.m)
        .overlay(alignment: .leading) {
            if m.kind == "review", m.review == "approved" || m.review == "changes_requested" {
                // the review's state along the card's edge, as GitHub's colored timeline badge
                UnevenRoundedRectangle(topLeadingRadius: Theme.Radius.card, bottomLeadingRadius: Theme.Radius.card, style: .continuous)
                    .fill(reviewColor)
                    .frame(width: Theme.Size.bar)
            }
        }
        .contentShape(.rect)
        .contextMenu { menu }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("github.entry.\(m.id)")
    }

    private var detail: String? {
        switch m.kind {
        case "review": GitHubThread.reviewTitle(m.review)
        case "code": String(localized: "commented on code")
        default: nil
        }
    }

    private var reviewSymbol: String? {
        guard m.kind == "review" else { return nil }
        switch m.review {
        case "approved": return Theme.Icon.successFilled
        case "changes_requested": return Theme.Icon.changesRequested
        case "dismissed": return Theme.Icon.skipped
        default: return Theme.Icon.conversation
        }
    }

    private var reviewColor: Color {
        guard m.kind == "review" else { return .secondary }
        switch m.review {
        case "approved": return Theme.positive
        case "changes_requested": return Theme.cautionText
        default: return .secondary
        }
    }

    @ViewBuilder private var menu: some View {
        if canReply {
            Button { quote(m) } label: { Label("Quote Reply", systemImage: Theme.Icon.reply) }
        }
        if !words.isEmpty {
            Button {
                UIPasteboard.general.string = words
                Haptics.success()
            } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
        }
        if let u = m.url {
            Button { open(u) } label: { Label("Open in GitHub", systemImage: Theme.Icon.safari) }
        }
    }
}

/// The file and line a comment is on, and the last lines of the diff before it.
struct GitHubCodeRef: View {
    var code: ThreadMessage.Code

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Label {
                Text(code.line.map { "\(code.path):\($0)" } ?? code.path).lineLimit(1).truncationMode(.head)
            } icon: {
                ThemedIcon(Theme.Icon.code)
            }
            .font(Theme.Font.code)
            .foregroundStyle(.secondary)
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, Theme.Space.xs)
            .frame(maxWidth: .infinity, alignment: .leading)
            if let hunk = code.hunk, !hunk.isEmpty {
                Divider()
                ForEach(Array(GitHubThread.hunkLines(hunk).enumerated()), id: \.offset) { _, line in
                    Text(line.text.isEmpty ? " " : line.text)
                        .font(Theme.Font.code)
                        .foregroundStyle(line.mark == "+" ? Theme.Diff.add : line.mark == "-" ? Theme.Diff.del : .primary)
                        .lineLimit(1)
                        .padding(.horizontal, Theme.Space.s)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(line.mark == "+" ? Theme.Diff.addFill : line.mark == "-" ? Theme.Diff.delFill : .clear)
                }
            }
        }
        .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
        .clipShape(.rect(cornerRadius: Theme.Radius.badge, style: .continuous))
        .accessibilityElement(children: .combine)
    }
}

/// The checks that ran one after another, as one card: a line of how they went, each check, and a failed one's end
/// of the log (the first failure's open).
struct GitHubChecksCard: View {
    var checks: [ThreadMessage]
    var open: (String) -> Void
    @State private var shown: Set<String>?

    /// Lines of a failed check's log shown.
    static let logLines = 40

    var body: some View {
        let failed = checks.filter { $0.check?.state == "failure" }
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.sm) {
                ThemedIcon(Self.symbol(worst)).foregroundStyle(Self.color(worst))
                Text(GitHubThread.checksSummary(checks)).font(Theme.Font.captionStrong).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                if let at = checks.last?.at {
                    Text(at, format: .relative(presentation: .named)).font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1)
                }
            }
            ForEach(checks) { m in
                if let c = m.check {
                    row(m.id, c)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.m)
        .onAppear { if shown == nil { shown = Set(failed.prefix(1).map(\.id)) } }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("github.checks")
    }

    private var worst: String {
        let states = checks.compactMap { $0.check?.state }
        return ["failure", "pending", "cancelled"].first { states.contains($0) } ?? "success"
    }

    @ViewBuilder private func row(_ id: String, _ c: ThreadMessage.Check) -> some View {
        let hasLog = !(c.log ?? "").isEmpty
        let isOpen = shown?.contains(id) == true
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Self.symbol(c.state)).font(Theme.Font.caption).foregroundStyle(Self.color(c.state))
                Text(c.name).font(Theme.Font.secondary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                if hasLog {
                    Button {
                        withAnimation(Theme.Motion.reveal) {
                            var s = shown ?? []
                            if isOpen { s.remove(id) } else { s.insert(id) }
                            shown = s
                        }
                    } label: {
                        Label(isOpen ? String(localized: "Hide Logs") : String(localized: "Logs"), systemImage: isOpen ? Theme.Icon.collapse : Theme.Icon.expand)
                            .font(Theme.Font.label).lineLimit(1)
                    }
                    .buttonStyle(.borderless)
                } else if let u = c.url {
                    Button { open(u) } label: {
                        ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel("Open \(c.name) [Git Hub Thread View eb8679]")
                }
            }
            if hasLog, isOpen, let log = c.log {
                Text(log.components(separatedBy: "\n").suffix(Self.logLines).joined(separator: "\n"))
                    .font(Theme.Font.code)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .padding(Theme.Space.s)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
                    .transition(.opacity)
                    .accessibilityIdentifier("github.log")
            }
        }
    }

    static func symbol(_ state: String) -> String {
        switch state {
        case "success": Theme.Icon.successFilled
        case "failure": Theme.Icon.errorFilled
        case "pending": Theme.Icon.time
        default: Theme.Icon.skipped
        }
    }

    static func color(_ state: String) -> Color {
        switch state {
        case "success": Theme.positive
        case "failure": Theme.danger
        case "pending": Theme.caution
        default: .secondary
        }
    }
}

/// A gray line of what happened (labeled, merged, renamed), with a dot on the timeline's edge.
struct GitHubEventLine: View {
    var message: ThreadMessage

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.dot).font(Theme.Font.label).imageScale(.small).foregroundStyle(.tertiary)
            (Text(message.text ?? "") + Text("  ") + Text(message.at, format: .relative(presentation: .named)).foregroundStyle(.tertiary))
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal, Theme.Space.m)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("github.entry.\(message.id)")
    }
}

/// A comment's Markdown, in the chat's theme.
struct GitHubMarkdown: View {
    var text: String

    var body: some View {
        Markdown(text)
            .markdownTheme(.persona)
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}
