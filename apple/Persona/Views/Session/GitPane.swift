import PersonaKit
import SwiftUI

/// The Git tab of a session: branch and sync state, working-tree changes with their diffs, and the history.
struct GitPane: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    let cwd: String
    @Binding var status: GitStatus?
    @State private var error: String?
    @State private var older: [GitCommit] = []
    @State private var noMore = false
    @State private var loadingMore = false
    @State private var diff: GitDiffTarget?
    @State private var commit: GitCommit?
    @State private var pullModel = GitPullModel()
    @State private var revision = 0

    var body: some View {
        List {
            if let s = status, s.isRepo {
                Section { header(s) }
                changes(s)
                history(s)
            } else if let error {
                ContentUnavailableView("Can't Read Git Status", systemImage: Theme.Icon.error, description: Text(error))
                    .listRowBackground(Color.clear)
            } else if status?.isRepo == false {
                ContentUnavailableView("Not a Git Repository", systemImage: Theme.Icon.gitBranch, description: Text(cwd))
                    .listRowBackground(Color.clear)
            } else {
                HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
            }
        }
        .listStyle(.insetGrouped)
        .themedPage()
        .refreshable { await load() }
        #if os(macOS)
        .pageToolbar { MacRefreshItem(id: [machineID, cwd]) { await load() } } // no pull to refresh on the Mac
        #endif
        .task(id: cwd) { await load() }
        // Live refresh (#3089): the status again when the hub says it moved, in place (the commits loaded below stay).
        .fsLive(group: group, machineID: machineID, cwd: cwd, paths: [], git: true) { change in
            if change.git { Task { await load(live: true) } }
        }
        .sheet(item: $diff) { t in GitDiffSheet(machineID: machineID, group: group, cwd: cwd, root: status?.root, target: t) }
        .sheet(item: $commit) { c in GitCommitSheet(machineID: machineID, group: group, cwd: cwd, commit: c) }
        #if os(macOS) && DEBUG
        .onAppear {
            // `-macGitSheet diff|commit`: a made-up change / commit's sheet, to see its size on the Mac (demo mode has
            // no repositories, so it shows its error state). The change and the commit are the ones scripts/mock/mock_hub.py
            // (MOCK_FILES=1) answers for, so paired with it the sheets show a real diff.
            switch UserDefaults.standard.string(forKey: "macGitSheet") {
            case "diff":
                diff = GitDiffTarget(file: GitFile(path: "Sources/App/main.swift", index: "M", worktree: "M", added: 2, deleted: 1), staged: false)
            case "commit":
                commit = GitCommit(hash: String(repeating: "0", count: 32) + "a1b2c3d0", short: "a1b2c3d", subject: "Show the city name",
                                   author: "Sample Dev")
            default: break
            }
        }
        #endif
    }

    private func header(_ s: GitStatus) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.gitBranch).foregroundStyle(Theme.accent)
                Text(s.branch ?? String(localized: "Detached HEAD")).font(Theme.Font.heading).lineLimit(1)
                Spacer(minLength: Theme.Space.xs)
                if s.ahead > 0 { sync(Theme.Icon.send, s.ahead, .secondary) }
                if s.upstream != nil { pullButton(s) }
            }
            HStack(spacing: Theme.Space.sm) {
                if let up = s.upstream {
                    Label(up, systemImage: Theme.Icon.gitUpstream).lineLimit(1)
                    if s.ahead == 0 && s.behind == 0 { Text("· Up to date") }
                } else {
                    Label("No upstream branch", systemImage: Theme.Icon.gitNoUpstream).lineLimit(1)
                }
                if s.stashes > 0 { Text("· \(s.stashes) stashes") }
            }
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            if let root = s.root {
                Text(Fmt.path(root, home: nil)).font(Theme.Font.label.monospaced()).foregroundStyle(.tertiary).lineLimit(1).truncationMode(.head)
            }
            if let message = pullModel.message {
                Label(message, systemImage: pullModel.failed ? Theme.Icon.error : Theme.Icon.success)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(pullModel.failed ? Theme.danger : Theme.positive)
                    .accessibilityIdentifier("git-pull-result")
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    private func pullButton(_ s: GitStatus) -> some View {
        Button {
            Task { await pull() }
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if pullModel.isPulling { ProgressView().controlSize(.mini) }
                Chip(text: pullModel.isPulling ? String(localized: "Pulling") : s.behind > 0 ? String(localized: "Pull \(s.behind)") : String(localized: "Pull"),
                     symbol: pullModel.isPulling ? nil : Theme.Icon.down,
                     look: .tint(s.behind > 0 ? Theme.cautionText : Theme.accent))
                    .monospacedDigit()
                    .lineLimit(1)
            }
            .padding(.vertical, Theme.Space.s)
            .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .disabled(pullModel.isPulling || loadingMore || s.branch == nil)
        .accessibilityIdentifier("git-pull")
        .accessibilityLabel("Pull Latest Code")
        .accessibilityValue(pullModel.isPulling ? String(localized: "Pulling") : String(localized: "\(s.behind) commits behind"))
        .help("Safely pulls the latest upstream code without merging or overwriting local changes")
    }

    private func sync(_ symbol: String, _ n: Int, _ color: Color) -> some View {
        Chip(text: "\(n)", symbol: symbol, look: color == .secondary ? .neutral : .tint(color)).monospacedDigit()
    }

    @ViewBuilder private func changes(_ s: GitStatus) -> some View {
        Section {
            if s.files.isEmpty {
                Label("Working tree is clean", systemImage: Theme.Icon.success).foregroundStyle(.secondary)
            }
            ForEach(s.files) { f in
                Button { diff = GitDiffTarget(file: f, staged: f.isStaged && !f.hasUnstaged) } label: { GitFileRow(file: f) }
                    .buttonStyle(.plain)
                    .contextMenu {
                        Button { UIPasteboard.general.string = f.path } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
                    }
            }
        } header: {
            if !s.files.isEmpty {
                let added = s.files.reduce(0) { $0 + $1.added }, deleted = s.files.reduce(0) { $0 + $1.deleted }
                HStack {
                    Text("Changes \(s.files.count)")
                    Spacer()
                    Text("+\(added)").foregroundStyle(Theme.Diff.add)
                    Text("−\(deleted)").foregroundStyle(Theme.Diff.del)
                }
                .monospacedDigit()
            }
        }
    }

    @ViewBuilder private func history(_ s: GitStatus) -> some View {
        Section(String(localized: "git.commits", defaultValue: "Commits")) {
            ForEach(s.commits + older) { c in
                Button { commit = c } label: { GitCommitRow(commit: c, isHead: c.hash == s.head?.hash) }
                    .buttonStyle(.plain)
            }
            if !noMore && s.commits.count >= 30 {
                Button {
                    Task { await loadMore(after: s.commits.count + older.count) }
                } label: {
                    HStack(spacing: Theme.Space.sm) {
                        if loadingMore { ProgressView().controlSize(.mini) }
                        Text("Load Earlier Commits").lineLimit(1)
                    }
                    .frame(maxWidth: .infinity)
                }
                .disabled(loadingMore)
            }
        }
    }

    private func pull() async {
        guard let api = model.api(group), !cwd.isEmpty, !pullModel.isPulling else { return }
        revision += 1
        if let fresh = await pullModel.pull(machine: machineID, cwd: cwd, deps: .init(api: api)) {
            status = fresh
            older = []
            noMore = false
            error = nil
            Haptics.success()
        } else {
            await load()
        }
    }

    /// live: an automatic refresh (#3089): the older commits already loaded stay while the newest one is the same,
    /// and a failure leaves what is shown.
    private func load(live: Bool = false) async {
        guard let api = model.api(group), !cwd.isEmpty, !pullModel.isPulling else { return }
        let started = revision
        do {
            let fresh = try await api.gitStatus(machine: machineID, cwd: cwd)
            guard started == revision else { return }
            let sameHistory = live && fresh.commits.first?.hash == status?.commits.first?.hash
            if fresh != status { status = fresh }
            if !sameHistory {
                older = []
                noMore = false
            }
            error = nil
        } catch {
            if status == nil && !live { self.error = error.shown }
        }
    }

    private func loadMore(after skip: Int) async {
        guard let api = model.api(group), !pullModel.isPulling else { return }
        loadingMore = true
        defer { loadingMore = false }
        if let more = try? await api.gitLog(machine: machineID, cwd: cwd, skip: skip) {
            older += more
            noMore = more.count < 50
        }
    }
}

struct GitDiffTarget: Identifiable, Hashable {
    var file: GitFile
    var staged: Bool
    var id: String { file.path + (staged ? "#staged" : "") }
}

/// Status letter badge colored like the editors do.
struct GitBadge: View {
    var letter: String

    var body: some View {
        Text(letter)
            .font(Theme.Font.labelHeavy.monospaced()).dynamicTypeSize(...DynamicTypeSize.xLarge)
            .foregroundStyle(color)
            .scaledFrame(width: Theme.Size.iconXS, height: Theme.Size.iconXS)
            .background(color.opacity(0.14), in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
    }

    var color: Color {
        switch letter {
        case "A", "U": Theme.Diff.add
        case "D", "!": Theme.Diff.del
        case "R", "C": .secondary
        default: Theme.Diff.changed
        }
    }
}

struct GitFileRow: View {
    var file: GitFile
    /// The working tree's changes say staged / partly staged; a commit's files have no stage (their `index` is only
    /// the change kind, for the badge).
    var showsStage = true

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            GitBadge(letter: file.letter)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(file.name).font(Theme.Font.secondaryMedium).lineLimit(1)
                HStack(spacing: Theme.Space.xs) {
                    if showsStage, file.isStaged { Text(file.hasUnstaged ? String(localized: "Partly staged") : String(localized: "Staged")).foregroundStyle(.primary) }
                    if let orig = file.origPath { Text("← " + (orig as NSString).lastPathComponent) }
                    if !file.folder.isEmpty { Text(file.folder).truncationMode(.head) }
                }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            }
            Spacer(minLength: 6)
            if file.binary == true {
                Text("Binary").font(Theme.Font.label).foregroundStyle(.secondary)
            } else if file.added + file.deleted > 0 {
                HStack(spacing: Theme.Space.xs) {
                    Text("+\(file.added)").foregroundStyle(Theme.Diff.add)
                    Text("−\(file.deleted)").foregroundStyle(Theme.Diff.del)
                }
                .font(Theme.Font.label.monospacedDigit())
            }
        }
        .contentShape(.rect)
    }
}

struct GitCommitRow: View {
    var commit: GitCommit
    var isHead = false

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(commit.subject).font(Theme.Font.secondaryMedium).lineLimit(2)
            HStack(spacing: Theme.Space.sm) {
                Text(commit.short).font(Theme.Font.code).foregroundStyle(Theme.accent)
                Text(commit.author).lineLimit(1)
                if let d = commit.date { Text("· " + Fmt.relative(d)).lineLimit(1) }
            }
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            let refs = commit.refNames
            if !refs.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: Theme.Space.xs) {
                        // A local branch outlined, a remote one (origin/…) plain: refs are facts, not buttons.
                        ForEach(refs, id: \.self) { r in Chip(text: r, look: r.contains("/") ? .neutral : .outline) }
                    }
                }
            }
        }
        .contentShape(.rect)
    }
}

/// The diff of one changed file; switches between the unstaged and staged halves when it has both.
struct GitDiffSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machineID: String
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    let cwd: String
    /// The repository's root: the working tree's version of a file is read there (comparing, `CompareView`).
    var root: String? = nil
    @State var target: GitDiffTarget
    @State private var diff: GitDiff?
    @State private var error: String?

    /// A picture or a document the hub can give the versions of: compared instead of a text diff (docs/apple/ios.md「对比」).
    private var compares: DocumentText.Kind? {
        guard model.features(group).contains("git_blob"), root != nil else { return nil }
        return DocumentText.kind(target.file.name)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if target.file.isStaged && target.file.hasUnstaged {
                    SegmentedChoice("", [(false, String(localized: "Unstaged")), (true, String(localized: "Staged"))], selection: $target.staged)
                    .padding(.horizontal, Theme.Space.l)
                    .padding(.vertical, Theme.Space.s)
                }
                if let kind = compares, let root {
                    CompareFileView(machine: MachineRef(group: group, id: machineID), cwd: cwd, root: root, file: target.file.path,
                                    versions: .change(staged: target.staged), kind: kind)
                        .id(target)
                } else if let diff {
                    DiffText(diff: diff.diff, truncated: diff.truncated)
                } else if let error {
                    ContentUnavailableView {
                        Label("Can't Read Changes", systemImage: Theme.Icon.error)
                    } description: {
                        Text(error)
                    } actions: {
                        Button("Retry [Components b8784c]") { Task { await load() } }.lineLimit(1).themedButton()
                    }
                } else {
                    ProgressView().frame(maxHeight: .infinity)
                }
            }
            .navigationTitle(target.file.name)
            .navigationSubtitle(target.file.folder.isEmpty ? String(localized: "Repository root") : target.file.folder)
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } }
                ToolbarItem(placement: .primaryAction) {
                    Menu {
                        Button { UIPasteboard.general.string = diff?.diff } label: { Label("Copy Diff", systemImage: Theme.Icon.copy) }
                        Button { UIPasteboard.general.string = target.file.path } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
                    } label: { ThemedIcon(Theme.Icon.more) }
                }
            }
            .task(id: target) { await load() }
            .onHubReconnect(group) { if error != nil { Task { await load() } } }
            #if os(macOS)
            .macLargeSheet(bars: true)
            #endif
        }
    }

    /// Reads the diff of the half shown. A sheet has nothing else that loads it again: a timeout, no network or no hub
    /// is said with Retry too, not left as a spinner (#4170); a cancelled load (the other half picked) says nothing.
    private func load() async {
        let t = target
        diff = nil
        error = nil
        guard let api = model.api(group) else { error = String(localized: "Not connected to the hub [App Model 93d955]"); return }
        do {
            let d = try await api.gitDiff(machine: machineID, cwd: cwd, path: t.file.path, staged: t.staged)
            if t == target { diff = d }
        } catch {
            if t == target, !error.isCancellation { self.error = error.shown ?? error.localizedDescription }
        }
    }
}

/// One commit: message, author, the files it touched, and each file's diff.
struct GitCommitSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machineID: String
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    let cwd: String
    let commit: GitCommit
    @State private var show: GitShow?
    @State private var error: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        Text(commit.subject).font(Theme.Font.heading)
                        if let body = (show?.commit.body ?? commit.body), !body.isEmpty {
                            Text(body).font(Theme.Font.body).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                        HStack(spacing: Theme.Space.sm) {
                            Text(commit.author)
                            if let d = commit.date { Text("· " + d.formatted(date: .abbreviated, time: .shortened)) }
                        }
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        Button {
                            UIPasteboard.general.string = commit.hash
                            Haptics.success()
                        } label: {
                            Label(commit.hash, systemImage: Theme.Icon.id).font(Theme.Font.label.monospaced()).lineLimit(1).truncationMode(.middle)
                        }
                        .buttonStyle(.borderless)
                    }
                    .padding(.vertical, Theme.Space.xxs)
                }
                if let show {
                    let chunks = DiffText.split(show.diff)
                    Section("\(show.files.count) files") {
                        ForEach(show.files) { f in
                            NavigationLink {
                                Group {
                                    if model.features(group).contains("git_blob"), let kind = DocumentText.kind(f.name) {
                                        CompareFileView(machine: MachineRef(group: group, id: machineID), cwd: cwd, root: "", file: f.path,
                                                        versions: .commit(commit.hash), kind: kind)
                                    } else {
                                        DiffText(diff: chunks[f.path] ?? "", truncated: show.truncated && chunks[f.path] == nil)
                                    }
                                }
                                .navigationTitle(f.name)
                                .navigationBarTitleDisplayMode(.inline)
                            } label: {
                                GitFileRow(file: f, showsStage: false)
                            }
                        }
                    }
                } else if let error {
                    Text(error).foregroundStyle(Theme.danger)
                    Button("Retry [Components b8784c]") { Task { await load() } }.lineLimit(1)
                } else {
                    HStack { Spacer(); ProgressView(); Spacer() }
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .navigationTitle(commit.short)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } } }
            .task { await load() }
            .onHubReconnect(group) { if error != nil { Task { await load() } } }
            #if os(macOS)
            .macLargeSheet(bars: true)
            #endif
        }
    }

    /// Reads the commit's files and diff; a failure of any kind but a cancellation is said, with Retry (#4170).
    private func load() async {
        error = nil
        guard let api = model.api(group) else { error = String(localized: "Not connected to the hub [App Model 93d955]"); return }
        do { show = try await api.gitShow(machine: machineID, cwd: cwd, hash: commit.hash) }
        catch { if !error.isCancellation { self.error = error.shown ?? error.localizedDescription } }
    }
}

extension View {
    /// `action` once each time the group's socket answers again after being down or stalled (a stall keeps the state
    /// connected while the title says Connecting…). The Git sheets read once when they open, so one that failed meanwhile
    /// would wait for Retry with the hub already back (#4385). No polling.
    func onHubReconnect(_ group: String, _ action: @escaping () -> Void) -> some View {
        modifier(HubReconnectAction(group: group, action: action))
    }
}

private struct HubReconnectAction: ViewModifier {
    @Environment(AppModel.self) private var model
    let group: String
    let action: () -> Void

    func body(content: Content) -> some View {
        content.onChange(of: model.isLive(group) && model.socket(group)?.stalled != true) { was, now in if !was && now { action() } }
    }
}

/// A unified diff with old / new line numbers, colored lines, no wrapping (scroll sideways), rendered lazily.
@MainActor private enum GitDiffMemo {
    struct Content {
        let lines: [DiffText.Line]
        let text: NSAttributedString
    }

    private static var values: [String: Content] = [:]

    static func content(_ diff: String) -> Content {
        if let value = values[diff] { return value }
        let lines = DiffText.parse(diff)
        let value = Content(lines: lines, text: DiffText.attributed(lines))
        if value.text.length <= 1_000_000 {
            if values.count >= 8 { values.removeAll(keepingCapacity: true) }
            values[diff] = value
        }
        return value
    }
}

struct DiffText: View {
    var diff: String
    var truncated = false

    nonisolated struct Line: Identifiable, Sendable {
        enum Kind { case add, del, context, hunk, info }
        var id: Int
        var kind: Kind
        var text: String
        var old: Int?
        var new: Int?
    }

    var body: some View {
        let content = GitDiffMemo.content(diff)
        if content.lines.isEmpty {
            ContentUnavailableView("No Text Changes", systemImage: Theme.Icon.document, description: Text("It may be a binary file, a permissions-only change, or a change that was undone."))
        } else {
            VStack(spacing: 0) {
                MonoTextView(text: content.text, inset: UIEdgeInsets(top: 6, left: 0, bottom: 6, right: 12))
                if truncated {
                    Text("Change too large; showing the first 512 KB").font(Theme.Font.label).foregroundStyle(.secondary).padding(Theme.Space.s)
                }
            }
            .background(Theme.codeBackground)
        }
    }

    /// Gutter (old / new line numbers, +/−) and the line, colored; lines are padded to the longest so the
    /// added / removed backgrounds run the full width.
    static func attributed(_ lines: [Line]) -> NSAttributedString {
        let font = MonoText.font(11)
        let width = (lines.map(\.text.count).max() ?? 0) + 2
        let out = NSMutableAttributedString()
        func num(_ n: Int?) -> String { let s = n.map(String.init) ?? ""; return String(repeating: " ", count: max(0, 5 - s.count)) + s }
        for (i, l) in lines.enumerated() {
            let (mark, fg, bg): (String, UIColor, UIColor) = switch l.kind {
            case .add: ("+", UIColor(Theme.Diff.add), UIColor(Theme.Diff.add).withAlphaComponent(0.13))
            case .del: ("−", UIColor(Theme.Diff.del), UIColor(Theme.Diff.del).withAlphaComponent(0.12))
            case .hunk: (" ", .secondaryLabel, .quaternarySystemFill)
            case .info: (" ", .secondaryLabel, .clear)
            case .context: (" ", .label, .clear)
            }
            let gutter = num(l.old) + num(l.new) + " " + mark + " "
            out.append(NSAttributedString(string: gutter, attributes: [.font: font, .foregroundColor: UIColor.tertiaryLabel, .backgroundColor: bg]))
            let body = l.text.padding(toLength: max(width, l.text.count), withPad: " ", startingAt: 0)
            let color: UIColor = l.kind == .add || l.kind == .del || l.kind == .context ? .label : fg
            #if os(macOS)
            // An NSTextView fills a background to the view's edge from the line break: the last line needs one too.
            let end = "\n"
            #else
            let end = i < lines.count - 1 ? "\n" : ""
            #endif
            out.append(NSAttributedString(string: body + end, attributes: [.font: font, .foregroundColor: color, .backgroundColor: bg]))
        }
        return out
    }

    nonisolated static func parse(_ diff: String) -> [Line] {
        var out: [Line] = []
        var old = 0, new = 0
        for raw in diff.components(separatedBy: "\n") {
            let id = out.count
            if raw.hasPrefix("diff --git") || raw.hasPrefix("index ") || raw.hasPrefix("--- ") || raw.hasPrefix("+++ ") {
                continue
            }
            if raw.hasPrefix("@@") {
                // @@ -12,7 +12,9 @@ context
                let parts = raw.split(separator: " ")
                if parts.count > 2 {
                    old = Int(parts[1].dropFirst().split(separator: ",").first ?? "") ?? 0
                    new = Int(parts[2].dropFirst().split(separator: ",").first ?? "") ?? 0
                }
                out.append(Line(id: id, kind: .hunk, text: raw))
            } else if raw.hasPrefix("+") {
                out.append(Line(id: id, kind: .add, text: String(raw.dropFirst()), new: new)); new += 1
            } else if raw.hasPrefix("-") {
                out.append(Line(id: id, kind: .del, text: String(raw.dropFirst()), old: old)); old += 1
            } else if raw.hasPrefix(" ") {
                out.append(Line(id: id, kind: .context, text: String(raw.dropFirst()), old: old, new: new)); old += 1; new += 1
            } else if raw.hasPrefix("\\") || raw.hasPrefix("Binary") || raw.hasPrefix("new file") || raw.hasPrefix("deleted file")
                        || raw.hasPrefix("similarity") || raw.hasPrefix("rename ") || raw.hasPrefix("old mode") || raw.hasPrefix("new mode") {
                out.append(Line(id: id, kind: .info, text: raw))
            }
        }
        return out
    }

    /// Splits a multi-file diff (git show) into per-file diffs keyed by the new path.
    static func split(_ diff: String) -> [String: String] {
        var res: [String: String] = [:]
        var path: String?
        var buf: [String] = []
        func flush() { if let path { res[path] = buf.joined(separator: "\n") } }
        for line in diff.components(separatedBy: "\n") {
            if line.hasPrefix("diff --git ") {
                flush()
                buf = []
                // diff --git a/x b/x → x (paths with spaces keep working since b/ is taken from the end)
                if let r = line.range(of: " b/", options: .backwards) { path = String(line[r.upperBound...]) } else { path = nil }
            }
            buf.append(line)
        }
        flush()
        return res
    }
}
