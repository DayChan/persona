import SwiftUI

// Drawing the code page's blocks (ExtensionCodePage.swift): one lazy stack, each block a row; the tabs over the page,
// a single file's lines, the phone's directory pages.

/// The blocks in a lazy stack (or a plain one for the few rows over a split). A tree's tap and a diff's controls go
/// to the page's model with the values the protocol names (docs/extensions/extensions.md「UI 版本 4」).
struct CodeBlockStack: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    var blocks: [CodePageBlock]
    @Bindable var model: ExtensionViewModel
    var state: CodePageState
    var look: CodeDiffLook
    @Binding var link: LinkTarget?
    var scrolls = true
    /// Drilling in (the phone): a directory opens a page of its own, a file the main sections.
    var drills = false
    /// The phone's way to the main sections (the diffs, a file), a row at the top.
    var openMain: (() -> Void)? = nil
    var mainTitle = ""
    /// The diffs' look (unified / side by side) on a wide display.
    var lookPicker: Binding<String>? = nil
    @Environment(AppModel.self) private var app

    var body: some View {
        content
            .confirmationDialog(pendingConfirm?.0.action.confirm ?? "", isPresented: confirming, titleVisibility: .visible) {
                if let (b, go) = pendingConfirm {
                    Button(role: b.tone == "danger" ? .destructive : nil) { go() } label: { Text(b.title).lineLimit(1) }
                        .accessibilityIdentifier("plugin-ui-confirm")
                }
                Button(String(localized: "Cancel"), role: .cancel) {}
            }
    }

    @ViewBuilder private var content: some View {
        if scrolls {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: Theme.Space.flush) { rows }
                        .padding(.vertical, Theme.Space.s)
                }
                .scrollDismissesKeyboard(.interactively)
                .onChange(of: state.scrollTo) { _, path in
                    guard let path, openMain == nil else { return }
                    withAnimation(Theme.Motion.layout) { proxy.scrollTo(Self.diffAnchor(path), anchor: .top) }
                    state.scrollTo = nil
                }
            }
        } else {
            VStack(alignment: .leading, spacing: Theme.Space.flush) { rows }
        }
    }

    static func diffAnchor(_ path: String) -> String { "diff-anchor:" + path }

    @ViewBuilder private var rows: some View {
        if let lookPicker {
            Picker(String(localized: "Diff View"), selection: lookPicker) {
                ForEach(CodeDiffLook.allCases) { Text($0.title).tag($0.rawValue) }
            }
            .pickerStyle(.segmented)
            .fixedSize()
            .padding(.horizontal, Theme.Space.page)
            .padding(.bottom, Theme.Space.s)
            .accessibilityIdentifier("code-diff-look")
        }
        ForEach(blocks) { b in
            block(b)
            if case .gap(let id) = b, id.hasPrefix("gap:"), let lines = model.fileLines[String(id.dropFirst(4))] {
                CodeFileBlock(code: .init(key: "context:" + id, path: lines.path,
                                         text: lines.lines.joined(separator: "\n"), startLine: lines.start),
                              model: model, link: $link, fills: false)
                    .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
                    .accessibilityIdentifier("extension-file-context")
            }
            if case .gap(let id) = b, id == CodePageBlocks.openMainSlot, let openMain {
                Button(action: openMain) {
                    HStack(spacing: Theme.Space.s) {
                        ThemedIcon(Theme.Icon.code).foregroundStyle(Theme.accent)
                        Text(mainTitle).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                        Spacer(minLength: 0)
                        ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.caption).foregroundStyle(.tertiary)
                    }
                    .padding(.horizontal, Theme.Space.m)
                    .scaledFrame(minHeight: Theme.Size.box50)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .card(padding: 0)
                .padding(.horizontal, Theme.Space.page)
                .accessibilityIdentifier("code-open-main")
            }
        }
    }

    @ViewBuilder private func block(_ b: CodePageBlock) -> some View {
        switch b {
        case .legacy(_, let title, let footer, let items, let more):
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if let title, !title.isEmpty {
                    Text(title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                        .padding(.horizontal, Theme.Space.xs)
                }
                VStack(alignment: .leading, spacing: Theme.Space.m) {
                    ForEach(items.enumerated().map { offset, item in
                        ExtensionParts.Item(id: item.key ?? String(offset), component: item)
                    }) { entry in
                        if case .divider = entry.component { Divider() } else {
                            ExtensionItemRow(item: entry.component, model: model, link: $link).frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    if let more { ExtensionMoreRow(more: more, model: model, link: $link) }
                }
                .card()
                if let footer, !footer.isEmpty {
                    Text(footer).font(Theme.Font.label).foregroundStyle(.secondary).padding(.horizontal, Theme.Space.xs)
                }
            }
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.s)
        case .header(_, let h):
            CodeHeaderView(header: h).padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
        case .breadcrumb(_, let c):
            CodeBreadcrumbView(crumbs: c.crumbs) { a in run(a) }
                .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.xs)
        case .treeRow(_, let tree, let row, let t):
            treeRow(tree: tree, row: row, t: t)
        case .treeEmpty:
            Text(String(localized: "No files")).font(Theme.Font.secondary).foregroundStyle(.secondary).padding(Theme.Space.page)
        case .code(_, let c):
            CodeFileBlock(code: c, model: model, link: $link, fills: false)
                .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
        case .diffHeader(_, let key, let d, let folded):
            CodeDiffFileHeader(path: d.path, oldPath: d.oldPath, status: d.status, additions: d.additions,
                               deletions: d.deletions, folded: folded) {
                withAnimation(Theme.Motion.state) { state.toggle(diff: key, collapsed: d.collapsed == true && !(d.hunks ?? []).isEmpty) }
            }
            .clipShape(UnevenRoundedRectangle(topLeadingRadius: Theme.Radius.control, topTrailingRadius: Theme.Radius.control))
            .padding(.horizontal, Theme.Space.page)
            .id(Self.diffAnchor(d.path))
        case .hunk(_, let d, let i, let header):
            CodeHunkHeader(header: header, expand: d.expand.map { a in { run(a, ["path": d.path, "hunk": String(i), "direction": "up"]) } })
                .padding(.horizontal, Theme.Space.page)
        case .diffLine(_, let key, let d, let line, let language, let digits):
            CodeDiffRow(line: line, language: language, digits: digits,
                        comment: d.comment == nil ? nil : { compose(key, line) })
                .padding(.horizontal, Theme.Space.page)
        case .diffPair(_, let key, let d, let left, let right, let language, let digits):
            CodeDiffPairRow(left: left, right: right, language: language, digits: digits,
                            comment: d.comment == nil ? nil : { compose(key, $0) })
                .padding(.horizontal, Theme.Space.page)
        case .diffNote(_, let text):
            Text(text).font(Theme.Font.secondary).foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(Theme.Space.m)
                .background(Theme.Fill.chip.opacity(0.5))
                .padding(.horizontal, Theme.Space.page)
        case .diffLoad(_, let d):
            Button { if let a = d.load { run(a, ["path": d.path]) } } label: {
                HStack(spacing: Theme.Space.s) {
                    if model.running == d.load?.id { ProgressView().controlSize(.small) }
                    Text(String(localized: "Load Diff")).lineLimit(1)
                }
                .frame(maxWidth: .infinity)
                .padding(Theme.Space.m)
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
            .background(Theme.Fill.chip.opacity(0.5))
            .padding(.horizontal, Theme.Space.page)
            .accessibilityIdentifier("code-diff-load-\(d.path)")
        case .diffMore(_, let d, let i):
            Button { if let a = d.expand { run(a, ["path": d.path, "hunk": String(i), "direction": "down"]) } } label: {
                Label(String(localized: "Show Lines Below"), systemImage: Theme.Icon.expand).font(Theme.Font.caption).lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xs)
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
            .background(Theme.Fill.chip)
            .padding(.horizontal, Theme.Space.page)
        case .inlineThread(let id, _, let t):
            inlineThread(id: id, t)
        case .inlineComposer(let id, let d, let anchor):
            inlineComposer(id: id, d: d, anchor: anchor)
        case .threadTitle(_, let title, let resolved):
            HStack(spacing: Theme.Space.s) {
                Text(title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                if resolved { Chip(text: String(localized: "Resolved"), look: .tint(Theme.positive)).lineLimit(1) }
            }
            .padding(.horizontal, Theme.Space.page + Theme.Space.xs)
            .padding(.top, Theme.Space.m)
            .padding(.bottom, Theme.Space.xs)
        case .comment(_, let c):
            CodeCommentView(author: c.author, avatar: c.avatar?.url, at: c.at, markdown: c.body ?? "",
                            badge: c.badge.map { ($0.text, $0.tone) }, event: c.event == true, link: $link)
                .padding(.horizontal, Theme.Space.page + Theme.Space.xs)
                .padding(.vertical, Theme.Space.s)
        case .reply(let id, let action, let placeholder):
            CodeComposerView(placeholder: placeholder, text: draft(id),
                             buttons: [(String(localized: "Reply"), nil, action.id, { send(action, id: id, ["body": draftText(id)]) })],
                             running: model.running, identifier: "code-reply-\(id)")
                .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
        case .composer(_, let c):
            CodeComposerView(placeholder: c.placeholder ?? "", text: Binding(get: { model.values[c.name] ?? "" }, set: { model.values[c.name] = $0 }),
                             buttons: c.buttons.map { b in (b.title, b.tone, b.action.id, { composerRun(b, name: c.name) }) },
                             running: model.running, identifier: "code-composer-\(c.name)")
                .padding(.horizontal, Theme.Space.page).padding(.vertical, Theme.Space.s)
        case .gap:
            Color.clear.frame(height: Theme.Space.l)
        }
    }

    // MARK: The tree

    @ViewBuilder private func treeRow(tree: String, row: CodeTree.Row, t: ExtensionFileTreeComponent) -> some View {
        let n = row.node
        if drills && n.isDir {
            CodeTreeRowView(row: row, selected: false, drills: true) {
                withAnimation(Theme.Motion.layout) { state.enter(n.path, tree: tree) }
                if n.unloaded, let e = t.expand { run(e, ["path": n.path]) }
            }
            .padding(.horizontal, Theme.Space.s)
        } else {
            CodeTreeRowView(row: row, selected: t.selected == n.path, drills: drills) { tapTree(tree: tree, node: n, t: t) }
                .padding(.horizontal, Theme.Space.s)
        }
    }

    private func tapTree(tree: String, node n: CodeTreeNode, t: ExtensionFileTreeComponent) {
        if n.isDir {
            withAnimation(Theme.Motion.state) { state.toggle(dir: n.path, tree: tree) }
            if n.unloaded, let e = t.expand { run(e, ["path": n.path]) }
            return
        }
        state.drilledFile = true
        if let a = t.action {
            run(a, ["path": n.path])
            if drills { openMain?() }
        } else {
            // No action: the page shows the file's diff (scrolled to it; the phone opens the diffs first).
            state.scrollTo = n.path
            if drills { openMain?() }
        }
    }

    // MARK: Inline comments

    private func compose(_ key: String, _ line: CodeDiffLine) {
        guard let a = line.anchor else { return }
        withAnimation(Theme.Motion.state) { state.compose(.init(diff: key, side: a.side, line: a.line)) }
    }

    private func inlineThread(id: String, _ t: ExtensionThread) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if t.outdated == true || t.resolved == true {
                HStack(spacing: Theme.Space.s) {
                    if t.outdated == true { Chip(text: String(localized: "Outdated [Extension Code Stack 1148b6]"), look: .neutral).lineLimit(1) }
                    if t.resolved == true { Chip(text: String(localized: "Resolved"), look: .tint(Theme.positive)).lineLimit(1) }
                }
            }
            ForEach(Array(t.comments.enumerated()), id: \.offset) { _, c in
                CodeCommentView(author: c.author, avatar: c.avatar?.url, at: c.at, markdown: c.body ?? "",
                                badge: c.badge.map { ($0.text, $0.tone) }, event: c.event == true, link: $link)
            }
            if let r = t.reply {
                CodeComposerView(placeholder: String(localized: "Reply…"), text: draft(id),
                                 buttons: [(String(localized: "Reply"), nil, r.id, { send(r, id: id, ["body": draftText(id)]) })],
                                 running: model.running, identifier: "code-thread-reply-\(t.id ?? id)")
            }
        }
        .padding(Theme.Space.m)
        .background(Theme.pageBackground)
        .overlay(alignment: .leading) { Rectangle().fill(Theme.accent).frame(width: Theme.Size.line * 2) }
        .padding(.horizontal, Theme.Space.page)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(String(localized: "Comments on line \(t.line)"))
    }

    private func inlineComposer(id: String, d: ExtensionDiffComponent, anchor: CodePageState.Anchor) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text(anchor.side == "old" ? String(localized: "Comment on old line \(anchor.line)") : String(localized: "Comment on line \(anchor.line)"))
                .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            CodeComposerView(placeholder: String(localized: "Write a comment…"), text: draft(id),
                             buttons: [(String(localized: "Cancel"), nil, "cancel", { withAnimation(Theme.Motion.state) { state.compose(nil) } }),
                                       (String(localized: "Comments"), nil, d.comment?.id ?? "comment", {
                                           guard let a = d.comment else { return }
                                           send(a, id: id, ["path": d.path, "side": anchor.side, "line": String(anchor.line),
                                                            "body": draftText(id)]) { state.compose(nil) }
                                       })],
                             running: model.running, identifier: "code-inline-comment")
        }
        .padding(Theme.Space.m)
        .background(Theme.pageBackground)
        .overlay(alignment: .leading) { Rectangle().fill(Theme.accent).frame(width: Theme.Size.line * 2) }
        .padding(.horizontal, Theme.Space.page)
    }

    private func draft(_ id: String) -> Binding<String> {
        Binding(get: { state.drafts[id] ?? "" }, set: { state.drafts[id] = $0 })
    }

    private func draftText(_ id: String) -> String { state.drafts[id] ?? "" }

    // MARK: Actions

    /// Sends what was written; an empty box sends nothing. What was written goes once it went through.
    private func send(_ a: ExtensionAction, id: String, _ values: [String: String], done: (() -> Void)? = nil) {
        guard !(values["body"] ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        let execute = model.prepared(a, with: values, displayed: displayed)
        Task {
            let o = await execute()
            if o.error == nil && !o.cancelled {
                state.drafts[id] = nil
                done?()
            }
            ExtensionOutcome.show(o, app: app, link: $link)
        }
    }

    private func composerRun(_ b: ExtensionComposerButton, name: String) {
        let saved = [name: model.values[name] ?? ""]
        let go = { run(b.action, saved) }
        if b.action.confirm?.isEmpty == false, !model.managesConfirmation { pendingConfirm = (b, go) } else { go() }
    }

    @State private var pendingConfirm: (ExtensionComposerButton, () -> Void)?

    /// Asks first for a composer's button that says so.
    private var confirming: Binding<Bool> {
        Binding(get: { pendingConfirm != nil }, set: { if !$0 { pendingConfirm = nil } })
    }

    private func run(_ a: ExtensionAction, _ values: [String: String] = [:]) {
        let execute = model.prepared(a, with: values, displayed: displayed)
        Task { ExtensionOutcome.show(await execute(), app: app, link: $link) }
    }
}

// MARK: Header and path

/// A detail page's head: its title large, the state badges, the facts in a wrapping row, the author's avatar.
struct CodeHeaderView: View {
    var header: ExtensionHeaderComponent

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                if let a = header.avatar { CodeAvatarView(url: a.url, text: a.text ?? header.title) }
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(header.title).font(Theme.Font.title).foregroundStyle(Color.primary).textSelection(.enabled)
                    if let s = header.subtitle, !s.isEmpty {
                        Text(s).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
                    }
                }
            }
            if let badges = header.badges, !badges.isEmpty {
                HStack(spacing: Theme.Space.s) {
                    ForEach(Array(badges.prefix(4).enumerated()), id: \.offset) { _, b in
                        Chip(text: b.text, look: b.tone == nil || b.tone == "neutral" ? .neutral : .tint(ExtensionUIStyle.tone(b.tone)))
                            .lineLimit(1)
                    }
                }
            }
            if let facts = header.facts, !facts.isEmpty {
                CodeFlow(spacing: Theme.Space.m) {
                    ForEach(Array(facts.prefix(8).enumerated()), id: \.offset) { _, f in
                        HStack(spacing: Theme.Space.xs) {
                            if let i = f.icon { ThemedIcon(ExtensionUIStyle.icon(i)).foregroundStyle(.secondary) }
                            if let l = f.label, !l.isEmpty, f.icon == nil { Text(l).foregroundStyle(.secondary) }
                            Text(f.value).foregroundStyle(Color.primary)
                        }
                        .font(Theme.Font.caption)
                        .lineLimit(1)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel([f.label, f.value].compactMap { $0 }.joined(separator: String(localized: ": ")))
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
    }
}

/// Views in rows that wrap, left to right.
struct CodeFlow: Layout {
    var spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, row: CGFloat = 0, widest: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > 0, x + size.width > width { y += row + spacing / 2; x = 0; row = 0 }
            x += size.width + spacing
            row = max(row, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: min(widest, width), height: y + row)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, row: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(.unspecified)
            if x > bounds.minX, x + size.width > bounds.maxX { y += row + spacing / 2; x = bounds.minX; row = 0 }
            s.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            row = max(row, size.height)
        }
    }
}

/// `repo / src / foo.go`: the parts before the last take a tap when the extension says where they go.
struct CodeBreadcrumbView: View {
    var crumbs: [ExtensionCrumb]
    var run: (ExtensionAction) -> Void

    var body: some View {
        // One line when it fits; a long path scrolls sideways (see `CodeTabsBar` for why not always a scroll view).
        ViewThatFits(in: .horizontal) {
            line
            ScrollView(.horizontal) { line }
                .scrollIndicators(.hidden)
                .scrollEdgeEffectHidden(true, for: .all)
                .contentMargins(.vertical, Theme.Space.flush, for: .scrollContent)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(String(localized: "Path"))
    }

    private var line: some View {
        HStack(spacing: Theme.Space.xs) {
            ForEach(Array(crumbs.enumerated()), id: \.offset) { i, c in
                if i > 0 { Text("/").foregroundStyle(.tertiary).accessibilityHidden(true) }
                if let a = c.action, i < crumbs.count - 1 {
                    Button(c.title) { run(a) }.buttonStyle(.plain).foregroundStyle(Theme.accent).lineLimit(1)
                } else {
                    Text(c.title).foregroundStyle(i == crumbs.count - 1 ? Color.primary : Color.secondary).lineLimit(1)
                }
            }
        }
        .font(Theme.Font.code)
        .fixedSize(horizontal: true, vertical: false)
    }
}

// MARK: Tabs

/// The page's tabs: one row of them (scrolling sideways when they do not fit), the picked one filled, each with its
/// count; picking one sends the tabs' action with `{name: value}` (and the search).
struct CodeTabsBar: View {
    @Environment(\.extensionDisplayedRevision) private var displayed
    var tabs: ExtensionTabs
    @Bindable var model: ExtensionViewModel
    @Binding var link: LinkTarget?
    @Environment(AppModel.self) private var app

    var body: some View {
        // One row when it fits (no scroll view: the first one on a page under the navigation bar gets the bar's height
        // as its inset and drew its row out of sight); a row that scrolls sideways when it does not.
        ViewThatFits(in: .horizontal) {
            row
            ScrollView(.horizontal) { row }
                .scrollIndicators(.hidden)
                .scrollEdgeEffectHidden(true, for: .all)
                .contentMargins(.vertical, Theme.Space.flush, for: .scrollContent)
                .scrollClipDisabled()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
    }

    private var row: some View {
        let current = model.values[tabs.name] ?? tabs.value
        return HStack(spacing: Theme.Space.s) {
            ForEach(tabs.items, id: \.value) { t in
                let picked = t.value == current
                Button {
                    if let displayed {
                        let before = model.values[tabs.name]
                        model.values[tabs.name] = t.value
                        var chosen = [tabs.name: t.value]
                        if model.view?.search != nil { chosen["q"] = model.query }
                        let execute = model.prepared(tabs.action, with: chosen, displayed: displayed)
                        Task {
                            let outcome = await execute()
                            if outcome.error != nil || outcome.cancelled { model.values[tabs.name] = before }
                            ExtensionOutcome.show(outcome, app: app, link: $link)
                        }
                    } else { Task { ExtensionOutcome.show(await model.pickTab(t.value), app: app, link: $link) } }
                } label: {
                    HStack(spacing: Theme.Space.xs) {
                        Text(t.title?.isEmpty == false ? t.title! : t.value).lineLimit(1)
                        if let b = t.badge, !b.isEmpty {
                            Text(b).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                    .font(picked ? Theme.Font.labelStrong : Theme.Font.label)
                    .padding(.horizontal, Theme.Space.m)
                    .scaledFrame(minHeight: Theme.Size.avatar)
                    .background(picked ? Theme.Fill.well : Color.clear, in: .capsule)
                    .contentShape(.capsule)
                }
                .buttonStyle(.plain)
                .foregroundStyle(picked ? Color.primary : Color.secondary)
                .accessibilityAddTraits(picked ? [.isButton, .isSelected] : .isButton)
                .accessibilityValue(t.badge ?? "")
                .accessibilityIdentifier("code-tab-\(t.value)")
            }
            Spacer(minLength: 0)
        }
        .fixedSize(horizontal: true, vertical: false)
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.xs)
    }
}

// MARK: A file's code

/// A file's code: the viewer's lines view (line numbers, colors, both-way scrolling, only the lines on screen drawn),
/// filling the column (`fills`) or a block of its own height in a stack; Load More when the extension cut it.
struct CodeFileBlock: View {
    @Environment(\.extensionDisplayedRevision) private var displayedRevision
    var code: ExtensionCodeComponent
    @Bindable var model: ExtensionViewModel
    @Binding var link: LinkTarget?
    var fills: Bool
    @State private var snippet = CodeSnippetModel()
    @Environment(AppModel.self) private var app

    /// A block in a stack shows at most this many lines (it scrolls inside itself past them).
    static let blockLines = 24

    private var displayed: ExtensionCodeComponent {
        guard let key = code.key, let lines = model.fileLines[key], lines.path == code.path else { return code }
        var value = code
        let original = code.text.components(separatedBy: "\n")
        let start = code.startLine ?? 1
        if lines.start == start + original.count, original.count + lines.lines.count <= 20_000 {
            value.text = (original + lines.lines).joined(separator: "\n")
        } else { value.text = lines.lines.joined(separator: "\n"); value.startLine = lines.start }
        if let total = lines.total { value.truncated = (value.startLine ?? 1) + value.text.components(separatedBy: "\n").count - 1 < total }
        return value
    }

    var body: some View {
        VStack(spacing: Theme.Space.flush) {
            if let src = snippet.current(displayed) {
                CodeLines(source: src, version: src.version)
                    .accessibilityLabel(code.path.map { String(localized: "Code of \($0)") } ?? String(localized: "Code"))
                    .accessibilityIdentifier("code-file")
                    .frame(height: fills ? nil : CGFloat(min(src.lineCount, Self.blockLines)) * src.lineHeight + Theme.Space.xl * 2)
                    .frame(maxHeight: fills ? .infinity : nil)
                if displayed.truncated == true, let load = code.load {
                    Button {
                        var v = ["from": String((displayed.startLine ?? 1) + src.lineCount)]
                        if let p = code.path { v["path"] = p }
                        let execute = model.prepared(load, with: v, displayed: displayedRevision)
                        Task { ExtensionOutcome.show(await execute(), app: app, link: $link) }
                    } label: {
                        HStack(spacing: Theme.Space.s) {
                            if model.running == load.id { ProgressView().controlSize(.small) }
                            Text(String(localized: "Load More")).lineLimit(1)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(Theme.Space.m)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.accent)
                    .background(Theme.codeBackground)
                    .accessibilityIdentifier("code-load-more")
                }
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity)
                    .frame(height: fills ? nil : Theme.Size.box150)
                    .frame(maxHeight: fills ? .infinity : nil)
                    .background(Theme.codeBackground)
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: fills ? 0 : Theme.Radius.control, style: .continuous))
        .onChange(of: displayed, initial: true) { _, c in snippet.update(c) }
        .task(id: CodeSnippetInput(displayed)) { await snippet.load(displayed) }
    }
}

/// A code component's text for `CodeLines`: the viewer's file source, numbered from `start_line`, the extension's
/// `highlight` lines marked.
@MainActor final class CodeSnippetSource: CodeLineSource {
    let code: ExtensionCodeComponent
    let input: CodeSnippetInput
    private let file: CodeFileSource
    var version = 0
    var changed: ((Range<Int>?) -> Void)? {
        get { file.changed }
        set { file.changed = newValue }
    }

    init(_ code: ExtensionCodeComponent, document: CodeDocument) {
        self.code = code
        input = CodeSnippetInput(code)
        file = CodeFileSource(document)
    }

    private init(_ code: ExtensionCodeComponent, file: CodeFileSource) {
        self.code = code
        input = CodeSnippetInput(code)
        self.file = file
    }

    func updatingPresentation(_ code: ExtensionCodeComponent) -> CodeSnippetSource {
        CodeSnippetSource(code, file: file)
    }

    var document: CodeDocument { file.doc }
    var lineCount: Int { file.lineCount }
    var columns: Int { file.columns }
    var gutterColumns: Int { max(2, String(lineCount + (code.startLine ?? 1) - 1).count) }
    /// One line's height in the lines view (its own rule: the font's line height and a quarter).
    var lineHeight: CGFloat {
        let f = CodeMetrics.font
        #if os(macOS)
        return ceil((f.ascender - f.descender + f.leading) * 1.25) // NSFont has no lineHeight
        #else
        return ceil(f.lineHeight * 1.25)
        #endif
    }

    func line(_ i: Int) -> NSAttributedString {
        let a = file.line(i)
        guard let h = code.highlight, (h.start...max(h.start, h.end)).contains(i + (code.startLine ?? 1)) else { return a }
        let m = NSMutableAttributedString(attributedString: a)
        m.addAttribute(.backgroundColor, value: UIColor(Theme.lineFocus), range: NSRange(location: 0, length: m.length))
        return m
    }

    func gutter(_ i: Int) -> NSAttributedString {
        NSAttributedString(string: String(i + (code.startLine ?? 1)), attributes: SyntaxStyle.gutter)
    }

    func tap(_ i: Int) -> Bool { false }
    func menu(_ i: Int) -> UIMenu? { file.menu(i) }
}

// MARK: Narrow pages

/// The way back up on a narrow code page: a chevron and where it goes.
struct CodeBackRow: View {
    var title: String
    var back: () -> Void

    var body: some View {
        Button(action: back) {
            HStack(spacing: Theme.Space.xs) {
                ThemedIcon(Theme.Icon.back)
                Text(title).lineLimit(1)
                Spacer(minLength: 0)
            }
            .font(Theme.Font.body)
            .foregroundStyle(Theme.accent)
            .padding(.horizontal, Theme.Space.page)
            .scaledFrame(minHeight: Theme.Size.avatar)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Back to \(title) [Extension Code Stack bcc83f]"))
        .accessibilityIdentifier("code-back")
    }
}
