import SwiftUI

// The code-hosting components drawn with the design system (#2961, docs/extensions/extensions.md「UI 版本 4」). Each is drawn
// from plain values (ExtensionCodeModels.swift works them out); `ExtensionCodePage.swift` maps the wire's
// components onto them and wires their actions to the page's model.

// MARK: Diff lines

/// How a diff is laid out: one column (removed and added lines one under the other), or the old and new sides beside
/// each other. Side by side only where there is room (iPad, Mac); the phone always has one column.
enum CodeDiffLook: String, CaseIterable, Identifiable {
    case unified, split
    var id: String { rawValue }
    var title: String { self == .unified ? String(localized: "Unified") : String(localized: "Side by Side") }
}

/// What a diff line's colors are.
enum CodeDiffInk {
    static func fill(_ k: CodeDiffLine.Kind) -> Color {
        switch k {
        case .add: Theme.Diff.addFill
        case .del: Theme.Diff.delFill
        case .context, .note: .clear
        }
    }

    static func mark(_ k: CodeDiffLine.Kind) -> String {
        switch k {
        case .add: "+"
        case .del: "−"
        case .context, .note: " "
        }
    }

    static func markColor(_ k: CodeDiffLine.Kind) -> Color {
        switch k {
        case .add: Theme.Diff.add
        case .del: Theme.Diff.del
        case .context, .note: .secondary
        }
    }

    /// What VoiceOver says for a line: its kind, its number, its text.
    static func spoken(_ l: CodeDiffLine) -> String {
        switch l.kind {
        case .add: String(localized: "Added line \(l.new ?? 0): \(l.text)")
        case .del: String(localized: "Removed old line \(l.old ?? 0): \(l.text)")
        case .context: String(localized: "Line \(l.new ?? 0): \(l.text)")
        case .note: l.text
        }
    }
}

/// A line's text colored by the file's language (the viewer's lexer, one line at a time), plain without one.
enum CodeLineInk {
    @MainActor static func text(_ s: String, language: SyntaxLanguage?) -> Text {
        guard let language, !s.isEmpty, s.utf8.count <= CodeDocument.maxLineBytes else { return Text(verbatim: s) }
        let doc = CodeDocument(text: s, language: language)
        doc.highlight { _ in }
        guard let tokens = doc.tokens(0), !tokens.isEmpty else { return Text(verbatim: s) }
        var out = AttributedString(s)
        let utf8 = s.utf8
        for t in tokens {
            let a = Int(t.start), b = Int(t.start + t.length)
            guard a < b, b <= utf8.count,
                  let color = SyntaxStyle.attributes(t.kind)[.foregroundColor] as? UIColor else { continue }
            let lo = utf8.index(utf8.startIndex, offsetBy: a), hi = utf8.index(utf8.startIndex, offsetBy: b)
            guard let r = Range(lo..<hi, in: out) else { continue }
            out[r].foregroundColor = Color(uiColor: color)
        }
        return Text(out)
    }
}

/// One line of a diff in one column: the old and the new line number, the mark, the text (wrapped: the page scrolls
/// one way only); its numbers take a tap to comment on it when the extension takes comments.
struct CodeDiffRow: View {
    var line: CodeDiffLine
    var language: SyntaxLanguage?
    /// The gutter's width in digits (the widest number in the file).
    var digits: Int
    var comment: (() -> Void)?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.flush) {
            gutter
            Text(CodeDiffInk.mark(line.kind))
                .foregroundStyle(CodeDiffInk.markColor(line.kind))
                .padding(.horizontal, Theme.Space.xs)
                .accessibilityHidden(true)
            CodeLineInk.text(line.text, language: language)
                .foregroundStyle(line.kind == .note ? Color.secondary : Color.primary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
        }
        .font(Theme.Font.code)
        .padding(.trailing, Theme.Space.s)
        .background(CodeDiffInk.fill(line.kind))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(CodeDiffInk.spoken(line))
        .accessibilityAction(named: String(localized: "Comment on This Line")) { comment?() }
    }

    @ViewBuilder private var gutter: some View {
        let numbers = HStack(spacing: Theme.Space.xs) {
            Text(line.old.map(String.init) ?? "").frame(width: CodeGutter.width(digits), alignment: .trailing)
            Text(line.new.map(String.init) ?? "").frame(width: CodeGutter.width(digits), alignment: .trailing)
        }
        .foregroundStyle(.tertiary)
        .padding(.horizontal, Theme.Space.xs)
        if let comment, line.anchor != nil {
            Button(action: comment) { numbers.contentShape(.rect) }
                .buttonStyle(.plain)
                .help(String(localized: "Comment on This Line"))
        } else {
            numbers
        }
    }
}

/// The old and new sides of a diff beside each other, one row of each.
struct CodeDiffPairRow: View {
    var left: CodeDiffLine?
    var right: CodeDiffLine?
    var language: SyntaxLanguage?
    var digits: Int
    var comment: ((CodeDiffLine) -> Void)?

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.flush) {
            cell(left, number: left?.old)
            Divider()
            cell(right, number: right?.new ?? (right?.kind == .note ? nil : right?.new))
        }
        .font(Theme.Font.code)
        .accessibilityElement(children: .contain)
    }

    @ViewBuilder private func cell(_ l: CodeDiffLine?, number: Int?) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
            Group {
                if let l, let comment, l.anchor != nil {
                    Button { comment(l) } label: { Text(number.map(String.init) ?? "").contentShape(.rect) }
                        .buttonStyle(.plain)
                        .help(String(localized: "Comment on This Line"))
                } else {
                    Text(number.map(String.init) ?? "")
                }
            }
            .foregroundStyle(.tertiary)
            .frame(width: CodeGutter.width(digits), alignment: .trailing)
            if let l {
                CodeLineInk.text(l.text, language: language)
                    .foregroundStyle(l.kind == .note ? Color.secondary : Color.primary)
                    .textSelection(.enabled)
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Theme.Space.xs)
        .frame(maxWidth: .infinity, alignment: .leading)
        .frame(maxHeight: .infinity, alignment: .top)
        .background(l.map { CodeDiffInk.fill($0.kind) } ?? Theme.Fill.chip)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(l.map(CodeDiffInk.spoken) ?? String(localized: "Empty"))
        .accessibilityAction(named: String(localized: "Comment on This Line")) { if let l, l.anchor != nil { comment?(l) } }
    }
}

enum CodeGutter {
    /// A digit's width in the code font at the current text size.
    @MainActor static var charWidth: CGFloat { ("0" as NSString).size(withAttributes: [.font: CodeMetrics.font]).width }

    /// A gutter column's width for so many digits, in the code font (scaled with the text size).
    @MainActor static func width(_ digits: Int) -> CGFloat {
        CGFloat(max(digits, 2)) * charWidth + Theme.Space.xxs
    }

    static func digits(_ lines: [CodeDiffLine]) -> Int {
        String(lines.reduce(0) { max($0, $1.old ?? 0, $1.new ?? 0) }).count
    }
}

/// A hunk's header line (`@@ -1,4 +1,6 @@ func x`) and the way to show the lines around it.
struct CodeHunkHeader: View {
    var header: String
    /// Shows the lines above it (nil: the extension cannot).
    var expand: (() -> Void)?

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            if let expand {
                Button(action: expand) {
                    ThemedIcon(Theme.Icon.expand)
                        .scaledFrame(width: Theme.Size.iconS, height: Theme.Size.iconS)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
                .accessibilityLabel(String(localized: "Show Lines Above"))
            }
            Text(verbatim: header).lineLimit(1).truncationMode(.tail).foregroundStyle(.secondary)
            Spacer(minLength: 0)
        }
        .font(Theme.Font.code)
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.xs)
        .background(Theme.Fill.chip)
    }
}

// MARK: Diff file header

/// What happened to a file, as a word and a color: added / modified / deleted / renamed.
enum CodeFileStatus {
    static func word(_ s: String?) -> String? {
        switch s {
        case "added": String(localized: "Added")
        case "modified": String(localized: "Edit [Inbox Event Views 37090f]")
        case "deleted": String(localized: "Delete")
        case "renamed": String(localized: "Rename [Chats View 0d0cba]")
        default: nil
        }
    }

    static func color(_ s: String?) -> Color {
        switch s {
        case "added": Theme.Diff.add
        case "deleted": Theme.Diff.del
        case "renamed": Theme.accent
        default: Theme.Diff.changed
        }
    }

    static func letter(_ s: String?) -> String? {
        switch s {
        case "added": "A"
        case "modified": "M"
        case "deleted": "D"
        case "renamed": "R"
        default: nil
        }
    }
}

/// `+12 −3`, in the diff colors.
struct CodeChangeCounts: View {
    var additions: Int?
    var deletions: Int?

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            if let a = additions { Text("+\(a)").foregroundStyle(Theme.Diff.add) }
            if let d = deletions { Text("−\(d)").foregroundStyle(Theme.Diff.del) }
        }
        .font(Theme.Font.caption.monospacedDigit())
        .lineLimit(1)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([additions.map { String(localized: "\($0) lines added") }, deletions.map { String(localized: "\($0) lines removed") }].compactMap { $0 }.joined(separator: String(localized: ", ")))
    }
}

/// A diff's file header: folding, the path (an old one before it when renamed), what happened, how much.
struct CodeDiffFileHeader: View {
    var path: String
    var oldPath: String?
    var status: String?
    var additions: Int?
    var deletions: Int?
    var folded: Bool
    var toggle: () -> Void

    var body: some View {
        Button(action: toggle) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(folded ? Theme.Icon.disclosure : Theme.Icon.expand)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.iconS, height: Theme.Size.iconS)
                if let l = CodeFileStatus.letter(status) {
                    Text(l).font(Theme.Font.code).foregroundStyle(CodeFileStatus.color(status))
                }
                VStack(alignment: .leading, spacing: Theme.Space.flush) {
                    if let oldPath, !oldPath.isEmpty, oldPath != path {
                        Text(oldPath).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                    }
                    Text(path).font(Theme.Font.code).foregroundStyle(Color.primary).lineLimit(1).truncationMode(.head)
                }
                Spacer(minLength: Theme.Space.s)
                CodeChangeCounts(additions: additions, deletions: deletions)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .background(Theme.Fill.chip)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([path, CodeFileStatus.word(status)].compactMap { $0 }.joined(separator: String(localized: ", ")))
        .accessibilityValue(folded ? String(localized: "Collapsed [Readiness Card ee6046]") : String(localized: "Expanded"))
        .accessibilityAddTraits(.isButton)
        .accessibilityAction { toggle() }
    }
}

// MARK: File tree

/// One row of a file tree: indented by its depth, a folder's arrow, the name, what changed, its comments.
struct CodeTreeRowView: View {
    var row: CodeTree.Row
    var selected: Bool
    /// Drilling in (iPhone): a directory has a chevron and no arrow.
    var drills = false
    var tap: () -> Void

    var body: some View {
        let n = row.node
        Button(action: tap) {
            HStack(spacing: Theme.Space.s) {
                if !drills {
                    Group {
                        if n.isDir {
                            ThemedIcon(row.open ? Theme.Icon.expand : Theme.Icon.disclosure)
                                .foregroundStyle(.secondary)
                        } else {
                            Color.clear
                        }
                    }
                    .scaledFrame(width: Theme.Size.iconS, height: Theme.Size.iconS)
                }
                ThemedIcon(n.isDir ? Theme.Icon.folder : Theme.Icon.file)
                    .foregroundStyle(n.isDir ? Theme.accent : Color.secondary)
                    .scaledFrame(width: Theme.Size.iconS, height: Theme.Size.iconS)
                Text(n.name).font(Theme.Font.secondary).foregroundStyle(Color.primary).lineLimit(1).truncationMode(.middle)
                Spacer(minLength: Theme.Space.xs)
                if let c = n.entry?.comments, c > 0 {
                    Label("\(c)", systemImage: Theme.Icon.message).labelStyle(.titleAndIcon)
                        .font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                }
                if n.entry?.additions != nil || n.entry?.deletions != nil {
                    CodeChangeCounts(additions: n.entry?.additions, deletions: n.entry?.deletions)
                }
                if let s = n.entry?.status, CodeFileStatus.letter(s) != nil {
                    Circle().fill(CodeFileStatus.color(s)).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
                }
                if let b = n.entry?.badge, !b.isEmpty { Chip(text: b, look: .neutral).lineLimit(1) }
                if drills && n.isDir {
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.caption).foregroundStyle(.tertiary)
                }
            }
            .padding(.leading, drills ? Theme.Space.m : Theme.Space.s + CGFloat(row.depth) * Theme.Space.l)
            .padding(.trailing, Theme.Space.m)
            .scaledFrame(minHeight: Theme.Size.avatar)
            .background {
                if selected { RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).fill(Theme.Fill.well) }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityText)
        .accessibilityValue(n.isDir && !drills ? (row.open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]")) : "")
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction { tap() }
        .accessibilityIdentifier("code-tree-\(n.path)")
    }

    private var accessibilityText: String {
        let n = row.node
        var parts = [n.isDir ? String(localized: "Folder \(n.name)") : n.name]
        if let w = CodeFileStatus.word(n.entry?.status) { parts.append(w) }
        if let a = n.entry?.additions { parts.append(String(localized: "\(a) lines added")) }
        if let d = n.entry?.deletions { parts.append(String(localized: "\(d) lines removed")) }
        if let c = n.entry?.comments, c > 0 { parts.append(String(localized: "\(c) comments")) }
        return parts.joined(separator: String(localized: ", "))
    }
}

// MARK: Comments

/// The relative time of an RFC 3339 instant ("3 hours ago"), or the text as it came when it is no instant.
enum CodeTime {
    static func relative(_ s: String?, now: Date = .now) -> String? {
        guard let s, !s.isEmpty else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let date = f.date(from: s) ?? {
            f.formatOptions = [.withInternetDateTime]
            return f.date(from: s)
        }()
        guard let date else { return s }
        let r = RelativeDateTimeFormatter()
        r.unitsStyle = .short
        return r.localizedString(for: date, relativeTo: now)
    }
}

/// One comment: the author's avatar, name, when, a badge (Approved), the Markdown body; an event (`event: true`) is one
/// small line instead.
struct CodeCommentView: View {
    var author: String
    var avatar: String?
    var at: String?
    var markdown: String
    var badge: (text: String, tone: String?)?
    var event: Bool
    @Binding var link: LinkTarget?

    var body: some View {
        if event {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.dot).font(Theme.Font.caption).foregroundStyle(.tertiary)
                Text("\(author) \(markdown)").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                if let t = CodeTime.relative(at) { Text(t).font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1) }
                Spacer(minLength: 0)
            }
            .accessibilityElement(children: .combine)
        } else {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                CodeAvatarView(url: avatar, text: author)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.s) {
                        Text(author).font(Theme.Font.labelStrong).lineLimit(1)
                        if let t = CodeTime.relative(at) { Text(t).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1) }
                        if let badge {
                            Chip(text: badge.text, look: badge.tone == nil || badge.tone == "neutral" ? .neutral : .tint(ExtensionUIStyle.tone(badge.tone)))
                                .lineLimit(1)
                        }
                        Spacer(minLength: 0)
                    }
                    ExtensionMarkdown(text: markdown, link: $link)
                }
            }
            .accessibilityElement(children: .combine)
        }
    }
}

/// An author's picture, or their initials while there is none (as an extension row's avatar).
struct CodeAvatarView: View {
    var url: String?
    var text: String

    var body: some View {
        Group {
            if let url, let u = URL(string: url), u.scheme == "https" {
                ExtensionRemoteImage(url: u, fallback: text)
            } else {
                Monogram(name: text, size: Theme.Size.wellL, fill: Theme.Fill.chip, font: Theme.Font.labelStrong, letters: 2)
            }
        }
        .scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
        .clipShape(.circle)
        .accessibilityHidden(true)
    }
}

// MARK: Composer

/// A box to write in (Markdown, several lines) and the buttons that send what is written: Comment / Approve / Request Changes.
struct CodeComposerView: View {
    var placeholder: String
    @Binding var text: String
    var buttons: [(title: String, tone: String?, id: String, run: () -> Void)]
    var running: String?
    var identifier: String

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            TextField(placeholder, text: $text, axis: .vertical)
                .lineLimit(3...10)
                .font(Theme.Font.body)
                .padding(Theme.Space.m)
                .background(Theme.Fill.chip, in: RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous))
                .accessibilityLabel(placeholder.isEmpty ? String(localized: "Type here") : placeholder)
                .accessibilityIdentifier(identifier)
            HStack(spacing: Theme.Space.s) {
                Spacer(minLength: 0)
                ForEach(Array(buttons.enumerated()), id: \.offset) { i, b in
                    // The extension's first button is its usual one (Comment): filled; the others (Approve, Request Changes) say
                    // their tone in their words only, so that a destructive one never stands out the most.
                    let primary = i == 0 && buttons.count > 1 || buttons.count == 1
                    Button(action: b.run) {
                        HStack(spacing: Theme.Space.xs) {
                            if running == b.id { ProgressView().controlSize(.small) }
                            if primary {
                                Text(b.title).lineLimit(1).minimumScaleFactor(0.8)
                            } else {
                                Text(b.title).lineLimit(1).minimumScaleFactor(0.8).foregroundStyle(ExtensionUIStyle.ink(b.tone))
                            }
                        }
                    }
                    .themedButton(prominent: primary)
                    .disabled(running != nil)
                    .accessibilityIdentifier("plugin-ui-action-\(b.id)")
                }
            }
        }
    }
}
