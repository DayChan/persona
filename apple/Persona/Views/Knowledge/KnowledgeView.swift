import BeautifulUI
import MarkdownUI
import PersonaKit
import SwiftUI

/// What Persona knows (the hub's ~/.persona/data, docs/assistant/knowledge.md): projects first, then chats, people, docs, meetings and
/// events, most important and most relevant first. Not a browser of Feishu: every row is Persona' own digest, and the
/// way out is handing it to Persona or an Agent.
///
/// Layout (2026-10-31 redo): a row of kind chips instead of a six-way segmented control, a tag menu instead of a second
/// chip row, projects grouped into Focus / Active / Done / Not a Project, and search hits that open their entry.
struct KnowledgeView: View {
    @Environment(AppModel.self) private var model
    @State private var kind: KBKind = .projects
    @State private var tag: String?
    @State private var lists: [KBKind: KBList] = [:]
    @State private var error: String?
    @State private var query = ""
    @State private var results: [KBResult]?
    /// What `results` were found for (the field may already say more).
    @State private var resultsFor = ""
    @State private var searchError: String?
    @State private var showIgnored = false
    #if DEBUG
    @State private var debugEntry: KBEntry?
    #if os(iOS) || os(visionOS)
    @State private var debugRecording: UUID?
    #endif
    #endif
    #if os(iOS) || os(visionOS)
    @State private var recordings = RecordingLibrary.shared
    #endif
    /// Where entries open beside the list instead of pushing (the Mac's Knowledge section).
    @Environment(\.kbOpenEntry) private var opener

    private var list: KBList? { lists[kind] }
    /// Searching only once there is text besides spaces: `search()` trims the same way and asks for nothing when that
    /// leaves nothing, so the results area would spin for good on a field of spaces (#4086).
    private var searching: Bool { !query.trimmingCharacters(in: .whitespaces).isEmpty }

    /// Recordings on a phone is this device's own list (with what is still recording or transcribing), not the hub's copy.
    private var localRecordings: Bool {
        #if os(iOS) || os(visionOS)
        true
        #else
        false
        #endif
    }

    private var counts: [KBKind: Int] {
        var out = lists.mapValues { $0.items.count }
        #if os(iOS) || os(visionOS)
        out[.recordings] = recordings.entries.count
        #endif
        return out
    }

    private var kindBar: some View {
        KBKindBar(kind: $kind, counts: counts)
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets(top: 2, leading: 0, bottom: 2, trailing: 0))
    }

    var body: some View {
        List {
            // Search covers every kind: the chips (one of them lit) would say otherwise.
            if !searching {
                #if os(macOS)
                kindBar // not in a section: the Mac's inset list gave it an empty header band and a rule (#830)
                #else
                Section { kindBar }
                    .listSectionSpacing(.compact)
                #endif
            }
            if searching {
                searchResults
            } else if kind == .recordings, localRecordings {
                #if os(iOS) || os(visionOS)
                RecordingsSection()
                #endif
            } else if let list {
                content(list)
            } else if let error {
                Section { Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
            } else {
                Section { ProgressView().frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xl) }.listRowBackground(Color.clear)
            }
        }
        .listStyle(.insetGrouped)
        #if os(macOS)
        .modifier(MacBelowTitleRowIfAsked()) // under Knowledge's title in the window: the list starts below it (#1243)
        #endif
        .themedPage()
        .detailTitle(String(localized: "Knowledge")) // pushed in Persona's page: ‹ Knowledge in the title row on the Mac
        .navigationBarTitleDisplayMode(.inline)
        .pageToolbar {
            #if os(macOS)
            MacToolbarMiddle() // the menu by the search field, clear of the title row's Knowledge (#830)
            #endif
            ToolbarItem(placement: .topBarTrailing) { if !(kind == .recordings && localRecordings) { tagMenu } }
        }
        // Always shown: the drawer that hides on scroll folded away as soon as results replaced the list (the chips
        // row goes), taking the typed text out of sight (2026-10-31).
        .pageSearchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: String(localized: "Search profiles and chat history"))
        .task(id: query) { await search() }
        .task(id: "\(kind.rawValue)|\(tag ?? "")") { await load() }
        .onChange(of: kind) { showIgnored = false }
        .refreshable { await load() }
        #if os(macOS)
        .macRefreshes { await load() } // ⌘R: no pull to refresh on the Mac
        #endif
        // Entries open with view-based links, not `navigationDestination(for:)`: this page is itself pushed with
        // `navigationDestination(isPresented:)`, and a value-pushed page on top of that lands *under* it (docs/lessons/ios.md):
        // a hit seemed not to open, and back showed the entry's ⚠️ placeholder (2026-10-31).
        #if DEBUG
        // `-showKnowledgeEntry <kind>/<id>` (with `-openAssistantPage knowledge`): that entry's page, for screenshots;
        // `-showKnowledgeSearch <text>`: searching for it; `-showKnowledgeKind <kind>`: that kind's list.
        // With both, the entry opens as from that search (its matches first).
        .navigationDestination(item: $debugEntry) { e in
            KnowledgeDetailView(entry: e, query: UserDefaults.standard.string(forKey: "showKnowledgeSearch"))
        }
        #if os(iOS) || os(visionOS)
        // `-showRecordingSample YES`: a made-up recording (no microphone); with `-openRecording YES` its page.
        .navigationDestination(item: $debugRecording) { RecordingDetailView(id: $0) }
        #endif
        .onAppear {
            #if os(iOS) || os(visionOS)
            if let id = RecordingSamples.installIfAsked(), UserDefaults.standard.bool(forKey: "openRecording") { debugRecording = id }
            #endif
            if let q = UserDefaults.standard.string(forKey: "showKnowledgeSearch") { query = q }
            if let k = UserDefaults.standard.string(forKey: "showKnowledgeKind").flatMap(KBKind.init(rawValue:)) { kind = k }
            if let ref = UserDefaults.standard.string(forKey: "showKnowledgeEntry"), let slash = ref.firstIndex(of: "/") {
                let id = String(ref[ref.index(after: slash)...])
                let e = KBEntry(kind: String(ref[..<slash]), id: id, name: id, relevance: 0)
                if let opener {
                    opener.open = .init(entry: e, query: UserDefaults.standard.string(forKey: "showKnowledgeSearch"))
                } else {
                    debugEntry = e
                }
            }
        }
        #endif
    }

    // MARK: Lists

    @ViewBuilder private func content(_ list: KBList) -> some View {
        if list.items.isEmpty {
            Section {
                EmptyStateView(symbol: kind.symbol, title: tag == nil ? String(localized: "No \(kind.title) yet") : String(localized: "No \(kind.title) tagged #\(tag!)"),
                               message: String(localized: "The hub archives and condenses your Lark and email conversations, organizes them into projects, people, docs and meetings, and ranks them by importance and relevance."))
            }
            .listRowBackground(Color.clear)
        } else if kind == .projects {
            let active = list.items.filter { !$0.projectDone && !$0.projectIgnored }
            let focus = active.filter { $0.importance == "P0" || $0.importance == "P1" }
            let rest = active.filter { $0.importance != "P0" && $0.importance != "P1" }
            let done = list.items.filter(\.projectDone)
            let ignored = list.items.filter(\.projectIgnored)
            if !focus.isEmpty { Section { rows(focus) } header: { header(String(localized: "Focus"), focus.count) } }
            if !rest.isEmpty { Section { rows(rest) } header: { header(focus.isEmpty ? String(localized: "Active") : String(localized: "Other Active"), rest.count) } }
            if !done.isEmpty { Section { rows(done) } header: { header(String(localized: "Done [Task Sheets f28461]"), done.count) } }
            if !ignored.isEmpty {
                Section {
                    Button { withAnimation(Theme.Motion.standard) { showIgnored.toggle() } } label: {
                        HStack {
                            Text("Not a Project").foregroundStyle(.secondary)
                            Text("\(ignored.count)").foregroundStyle(.tertiary).monospacedDigit()
                            Spacer()
                            ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
                                .rotationEffect(.degrees(showIgnored ? 90 : 0))
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    if showIgnored { rows(ignored) }
                } footer: {
                    Text("These only appeared in notifications, marketing emails or bot chats and won't be updated. Set them back to Active under Tag to restore them.")
                }
            }
        } else {
            // Most people are only someone in a group (no importance yet): folded under the ranked ones, as Not a Project.
            let ranked = tag != nil ? list.items : list.items.filter { !($0.importance ?? "").isEmpty }
            let rest = tag != nil ? [] : list.items.filter { ($0.importance ?? "").isEmpty }
            if !ranked.isEmpty {
                Section { rows(ranked) } header: { header(tag.map { "#" + $0 } ?? String(localized: "By Importance and Relevance"), ranked.count) }
            }
            if !rest.isEmpty {
                Section {
                    Button { withAnimation(Theme.Motion.standard) { showIgnored.toggle() } } label: {
                        HStack {
                            Text(ranked.isEmpty ? String(localized: "All") : String(localized: "Not Yet Organized")).foregroundStyle(.secondary).lineLimit(1)
                            Text("\(rest.count)").foregroundStyle(.tertiary).monospacedDigit()
                            Spacer()
                            ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
                                .rotationEffect(.degrees(showIgnored || ranked.isEmpty ? 90 : 0))
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("kb.rest")
                    if showIgnored || ranked.isEmpty { rows(rest) }
                } footer: {
                    if !ranked.isEmpty {
                        Text("\(kind.title) that have only appeared in groups and aren't tagged yet. Anyone who has talked with you or shows up in important chats gets organized by AI.")
                    }
                }
            }
        }
    }

    private func rows(_ items: [KBEntry]) -> some View {
        ForEach(items) { e in
            KBEntryLink(entry: e) { KBRow(entry: e) }
        }
    }

    private func header(_ title: String, _ count: Int) -> some View {
        HStack(spacing: Theme.Space.sm) {
            Text(title)
            Text("\(count)").foregroundStyle(Color.secondary.opacity(0.7)).monospacedDigit()
        }
    }

    // MARK: Tags

    private var tagMenu: some View {
        Menu {
            Button { tag = nil } label: {
                if tag == nil { Label("All", systemImage: Theme.Icon.done) } else { Text("All") }
            }
            Section("Frequent Tags") {
                ForEach(list?.tags ?? [], id: \.self) { t in
                    Button { Haptics.tap(); tag = t } label: {
                        if tag == t { Label("#" + t, systemImage: Theme.Icon.done) } else { Text("#" + t) }
                    }
                }
            }
        } label: {
            ThemedIcon(tag == nil ? Theme.Icon.filter : Theme.Icon.filterActive)
        }
        .accessibilityLabel(tag.map { String(localized: "Filter by tag, currently #\($0)") } ?? String(localized: "Filter by tag"))
    }

    // MARK: Search

    @ViewBuilder private var searchResults: some View {
        if let results, !results.isEmpty {
            // By kind, the kinds in the order of their best result (the hub ranks across kinds).
            let kinds = results.reduce(into: [String]()) { if !$0.contains($1.entry.kind) { $0.append($1.entry.kind) } }
            ForEach(kinds, id: \.self) { k in
                let these = results.filter { $0.entry.kind == k }
                Section {
                    ForEach(these) { r in
                        KBEntryLink(entry: r.entry, query: resultsFor) {
                            KBHitRow(result: r, query: resultsFor)
                        }
                        .accessibilityIdentifier("kb.hit")
                    }
                } header: { header(KBKind(rawValue: k)?.title ?? k, these.count) }
            }
        } else if results != nil {
            Section {
                ContentUnavailableView.search(text: resultsFor).listRowBackground(Color.clear)
            }
        } else if let searchError {
            Section { Label(searchError, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
        } else {
            Section { ProgressView().frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xl) }.listRowBackground(Color.clear)
        }
    }

    private func search() async {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty, let api = model.api else { results = nil; resultsFor = ""; searchError = nil; return }
        // Coming back from an entry the task runs again for the same text: keep what is shown.
        if q == resultsFor, results != nil { return }
        try? await Task.sleep(for: .milliseconds(300)) // typing
        guard !Task.isCancelled else { return }
        searchError = nil // the last search's error is not this one's: wait for this one (#3377)
        do {
            let found = try await api.kbFind(q)
            guard !Task.isCancelled else { return }
            results = found
            resultsFor = q
            searchError = nil
        } catch {
            guard !Task.isCancelled, let shown = error.shown else { return }
            // The page shows results before an error: the last text's results stayed and hid it (#3377).
            results = nil
            resultsFor = ""
            searchError = shown
        }
    }

    private func load() async {
        guard !(kind == .recordings && localRecordings), let api = model.api else { return }
        do {
            let l = try await api.kbList(kind, tag: tag)
            lists[kind] = l
            error = nil
        } catch {
            self.error = error.shown
        }
    }
}
/// The entry open beside Knowledge's list, where entries open there rather than push: the Mac's Knowledge section puts the
/// list and the open entry side by side (#2318) and sets one on the list. Elsewhere (the phone, Knowledge pushed in
/// Persona's page) there is none and the rows push their entry. A reference, so opening one redraws the rows that
/// change and the detail, not the list's page.
@Observable final class KBOpenEntry {
    struct Open: Hashable {
        var entry: KBEntry
        /// Opened from a search: the places it was found come first.
        var query: String?
    }

    var open: Open?

    func isOpen(_ e: KBEntry) -> Bool { open?.entry.kind == e.kind && open?.entry.id == e.id }
}

extension EnvironmentValues {
    /// Entries open beside the list (`KBOpenEntry`); nil: they push.
    @Entry var kbOpenEntry: KBOpenEntry?
}

/// A row that opens its entry: beside the list where there is a `KBOpenEntry` (the open one selected, in the shell's
/// selected shade), pushed otherwise.
struct KBEntryLink<Label: View>: View {
    var entry: KBEntry
    var query: String? = nil
    @ViewBuilder var label: Label
    @Environment(\.kbOpenEntry) private var opener

    var body: some View {
        if let opener {
            // Read here, not by the list: opening another entry redraws two rows, not the whole list.
            let selected = opener.isOpen(entry)
            let shape = RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
            Button { opener.open = .init(entry: entry, query: query) } label: {
                label.frame(maxWidth: .infinity, alignment: .leading).contentShape(.rect)
            }
            .buttonStyle(.plain)
            .macListRow(selected: selected, in: shape, outset: Theme.Space.s)
            .macHoverShade(in: shape, outset: Theme.Space.s)
            .accessibilityAddTraits(selected ? .isSelected : [])
        } else {
            NavigationLink { KnowledgeDetailView(entry: entry, query: query) } label: { label }
        }
    }
}

/// The six kinds as chips with their icon (and how many, once loaded), all in sight: no sideways scroll view (the
/// navigation stack can take one at the top of a page for the page's own and inset it, docs/lessons/ios.md「布局与组件」, and
/// Events was off the edge). Chips that do not fit drop their icons, then go to a second row.
struct KBKindBar: View {
    @Binding var kind: KBKind
    var counts: [KBKind: Int]

    var body: some View {
        if AppStyle.current.usesBeautifulUI { filterChips } else { plain }
    }

    /// The BeautifulUI style: its filter chips, each kind with its count, wrapping to a second row instead of scrolling
    /// sideways.
    private var filterChips: some View {
        LarkFlowLayout(spacing: Theme.Space.xs, lineSpacing: Theme.Space.sm) {
            ForEach(KBKind.allCases) { k in
                BUIFilterChip(k.title, count: counts[k], isActive: k == kind) {
                    Haptics.tap()
                    withAnimation(Theme.Motion.standard) { kind = k }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var plain: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: Theme.Space.s) { chips(icons: true, padding: Theme.Space.m) }
            HStack(spacing: Theme.Space.sm) { chips(icons: false, padding: Theme.Space.m) }
            HStack(spacing: Theme.Space.xs) { chips(icons: false, padding: Theme.Space.s) }
            LarkFlowLayout(spacing: Theme.Space.sm, lineSpacing: Theme.Space.s) { chips(icons: true, padding: Theme.Space.m) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private func chips(icons: Bool, padding: CGFloat) -> some View {
        ForEach(KBKind.allCases) { k in
            let on = k == kind
            Button {
                Haptics.tap()
                withAnimation(Theme.Motion.standard) { kind = k }
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    if icons { ThemedIcon(k.symbol).font(Theme.Font.captionStrong) }
                    Text(k.title)
                    if let n = counts[k], on { Text(Self.count(n)).monospacedDigit().opacity(0.75) }
                }
                .font(on ? Theme.Font.secondaryStrong : Theme.Font.secondaryMedium)
                .lineLimit(1)
                .fixedSize()
                .padding(.horizontal, padding)
                .padding(.vertical, Theme.Space.s)
                .foregroundStyle(on ? Color.white : Color.primary)
                .background(on ? Theme.accent : Color.primary.opacity(0.06), in: .capsule)
                .contentShape(.capsule)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("kb.kind." + k.rawValue)
            .accessibilityAddTraits(on ? .isSelected : [])
        }
    }

    /// 1985 → "1985" (no grouping comma: the chip is narrow), past 9999 → "10K+".
    static func count(_ n: Int) -> String { n > 9999 ? String(localized: "10K+") : String(n) }
}

/// One ranked entry: a kind tile carrying the importance, the name, its one-line state, then tags and when it changed.
struct KBRow: View {
    var entry: KBEntry

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            KBAvatar(entry: entry)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                    // Two lines: documents and meetings are told apart by the end of their titles.
                    Text(entry.name).font(Theme.Font.heading).lineLimit(2)
                    Spacer(minLength: 4)
                    if let d = entry.updated, d.realDate != nil {
                        Text(Fmt.relative(d)).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1).fixedSize()
                    }
                }
                if let line = entry.oneLine, !line.isEmpty {
                    Text(line).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
                }
                // As many tags as fit whole (a cut "#Manage…" says nothing).
                ViewThatFits(in: .horizontal) {
                    badges(tags: 2)
                    badges(tags: 1)
                    badges(tags: 0)
                }
                .padding(.top, 1)
            }
        }
        .padding(.vertical, Theme.Space.xs)
        .accessibilityElement(children: .combine)
    }

    private func badges(tags: Int) -> some View {
        HStack(spacing: Theme.Space.sm) {
            ImportanceBadge(importance: entry.importance)
            if let k = entry.projectKind { KBPill(text: k, tint: (entry.extra ?? "").hasPrefix("long") ? Theme.Hue.blue : nil) }
            if entry.relevance > 0 { KBPill(text: String(localized: "Relevance \(entry.relevance)"), tint: RelevanceMeter.tint(entry.relevance)) }
            ForEach((entry.tags ?? []).prefix(tags), id: \.self) { t in
                Text("#" + t).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).fixedSize()
            }
        }
    }
}

/// A rounded tile: the kind's symbol, or a person's / chat's first character; tinted by importance.
struct KBAvatar: View {
    var entry: KBEntry
    var size: CGFloat = 36

    var body: some View {
        let tint = ImportanceBadge.color(entry.importance ?? "")
        let kind = KBKind(rawValue: entry.kind)
        ZStack {
            RoundedRectangle(cornerRadius: size * 0.28, style: .continuous).fill(tint.opacity(0.14))
            if kind == .people || kind == .chats, let c = entry.name.first(where: { !$0.isWhitespace && !"#@（(【[「『《〈\"'“‘".contains($0) }) { // i18n-ignore
                Text(String(c).uppercased()).font(.system(size: size * 0.42, weight: .semibold)).foregroundStyle(tint)
            } else {
                ThemedIcon(kind?.symbol ?? Theme.Icon.unchecked).font(.system(size: size * 0.4, weight: .semibold)).foregroundStyle(tint)
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

struct ImportanceBadge: View {
    var importance: String?

    var body: some View {
        let p = importance ?? ""
        if !p.isEmpty {
            KBPill(text: p, tint: Self.color(p), bold: true)
                .accessibilityLabel("Importance \(p)")
        }
    }

    /// P0 red, P1 orange, P2 blue, P3 and unranked grey.
    static func color(_ p: String) -> Color {
        switch p {
        case "P0": Theme.needsYou
        case "P1": Theme.busy
        case "P2": Theme.Hue.blue
        default: Color.secondary
        }
    }
}

/// A small capsule: tinted text on a soft tint of the same color, or neutral.
struct KBPill: View {
    var text: String
    var tint: Color?
    var bold = false

    var body: some View {
        Text(text)
            .font(bold ? Theme.Font.labelHeavy.monospacedDigit() : Theme.Font.labelMedium.monospacedDigit())
            .lineLimit(1)
            .padding(.horizontal, Theme.Space.sm)
            .padding(.vertical, Theme.Space.xxs)
            .foregroundStyle(tint ?? Color.secondary)
            .background((tint ?? Color.primary).opacity(tint == nil ? 0.06 : 0.13), in: .capsule)
            .fixedSize()
    }
}

/// Relevance to the user (0–100): the color its pill takes.
enum RelevanceMeter {
    static func tint(_ value: Int) -> Color? {
        value >= 70 ? Theme.needsYou.opacity(0.9) : value >= 40 ? Theme.busy : nil
    }
}

/// One entry found by a search: its tile and name, then up to two matching lines with what was searched for in bold.
struct KBHitRow: View {
    var result: KBResult
    var query: String

    var body: some View {
        let hits = Array((result.hits ?? []).prefix(2))
        HStack(alignment: .top, spacing: Theme.Space.m) {
            KBAvatar(entry: result.entry, size: 30)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                    Text(Self.marked(result.entry.name, query)).font(Theme.Font.secondaryStrong).lineLimit(1)
                    Spacer(minLength: 0)
                    if result.count > hits.count {
                        Text(result.count >= 99 ? String(localized: "99+ matches") : String(localized: "\(result.count) matches")).font(Theme.Font.label).foregroundStyle(.tertiary)
                            .lineLimit(1).fixedSize()
                    }
                }
                if hits.isEmpty, let line = result.entry.oneLine, !line.isEmpty {
                    Text(line).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
                }
                ForEach(hits, id: \.self) { h in
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text(h.source).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1)
                        Text(Self.marked(h.text, query)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
                    }
                }
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    /// The text with every word searched for in bold primary (the hub matches lines with all the words).
    static func marked(_ text: String, _ query: String) -> AttributedString {
        var a = AttributedString(text)
        for q in query.split(whereSeparator: \.isWhitespace).map(String.init) where !q.isEmpty {
            var from = a.startIndex
            while from < a.endIndex, let r = a[from...].range(of: q, options: .caseInsensitive) {
                a[r].foregroundColor = .primary
                a[r].inlinePresentationIntent = .stronglyEmphasized
                from = r.upperBound
            }
        }
        return a
    }
}

struct KnowledgeDetailView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openURL) private var openURL
    var entry: KBEntry
    /// Opened from a search: the places it was found come first.
    var query: String? = nil
    @State private var detail: KBDetailResponse?
    @State private var error: String?
    @State private var allMatches = false
    @State private var allMoments = false
    @State private var link: LinkTarget?
    @State private var editing = false
    @State private var newAgent = false
    @State private var showRecent = false
    @State private var allLog = false
    @State private var openKinds: Set<String> = []
    #if DEBUG
    @State private var debugDay: KBMatch?
    #endif

    /// `-showKnowledgeLower YES`: the header and Agents left out, so a screenshot reaches the timeline and what follows.
    #if DEBUG
    static var debugLower: Bool { UserDefaults.standard.bool(forKey: "showKnowledgeLower") }
    #else
    static let debugLower = false
    #endif

    /// Timeline lines shown before Show All.
    static let logShown = 6
    /// Related entries of one kind shown before All.
    static let linksShown = 4
    /// Search matches shown before Show All.
    static let matchesShown = 5

    var body: some View {
        List {
            if !Self.debugLower {
                Section { header }
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets(top: 4, leading: 4, bottom: 8, trailing: 4))
            }
            if let d = detail?.detail {
                if let query, let matches = detail?.matches { found(matches, query: query, chat: d.kind == KBKind.chats.rawValue ? d.id : nil) }
                if let facts = ItemFacts(kind: d.kind, meta: d.meta), !facts.isEmpty { factsSection(facts) }
                if !Self.debugLower, let agents = detail?.agents, !agents.isEmpty {
                    Section {
                        ForEach(agents) { s in
                            Button { model.open(s.key) } label: {
                                SessionRow(session: s, machine: model.machine(s.machineRef))
                            }
                            .buttonStyle(.plain)
                        }
                    } header: { sectionHeader(String(localized: "Agents on This Project"), agents.count) }
                }
                if let log = d.log, !log.isEmpty { timeline(log) }
                // Empty once the header's parts are taken out (a summary that is just the one line): no blank card.
                if let sum = d.summary, case let profile = Self.profile(sum, oneLine: shown.oneLine), !profile.isEmpty {
                    Section("Profile") {
                        Markdown(Links.markdown(MathMarkdown.rewrite(profile))).markdownTheme(.persona).textSelection(.enabled)
                            .padding(.vertical, Theme.Space.sm)
                    }
                }
                if let into = d.meta?["merged_into"]?.string, !into.isEmpty { mergedSection(into) }
                if let moments = d.moments, !moments.isEmpty { momentsSection(moments) }
                links(d)
                if let recent = d.recent, !recent.isEmpty {
                    Section {
                        DisclosureGroup(isExpanded: $showRecent) {
                            Text(recent).font(Theme.Font.secondary).textSelection(.enabled)
                        } label: {
                            Label("Messages from the Last 7 Days", systemImage: Theme.Icon.conversation)
                        }
                    }
                }
                Section {
                    Text(d.path).font(Theme.Font.code).foregroundStyle(.secondary).textSelection(.enabled)
                } header: {
                    Text("Folder [Task Views 52daa7]")
                } footer: {
                    Text("Persona, the AI twin and the agents on your computers can all read this folder directly.")
                }
            } else if let error {
                Section { Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
            } else {
                Section { ProgressView().frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xl) }.listRowBackground(Color.clear)
            }
        }
        .listStyle(.insetGrouped)
        .themedPage()
        .detailTitle(KBKind(rawValue: entry.kind)?.title ?? "")
        .navigationBarTitleDisplayMode(.inline)
        .pageToolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { editing = true } label: { Label("Tag", systemImage: Theme.Icon.label) }
                    .accessibilityLabel("Tag")
            }
        }
        #if os(macOS)
        .macLinkPanel($link) // the Mac's panel at the trailing edge; picks go to Persona
        #else
        .sheet(item: $link) { t in
            if let key = model.assistantMain ?? model.assistantCurrent { LinkSheet(target: t, key: key) }
        }
        #endif
        .sheet(isPresented: $editing) {
            KBTagEditor(entry: shown) { await load() }
        }
        #if DEBUG && os(macOS)
        // `-showKBEditor YES` with `-showKnowledgeEntry <kind>/<id>`: the tag editor open (Mac screenshots).
        .onAppear { if UserDefaults.standard.bool(forKey: "showKBEditor") { editing = true } }
        #endif
        #if DEBUG
        // `-showKnowledgeDay YES` with `-showKnowledgeEntry chats/<id> -showKnowledgeSearch <text>`: the day of the
        // first message found, opened at it (screenshots).
        .navigationDestination(item: $debugDay) { m in
            KBDayView(chat: entry.id, chatName: shown.name, day: m.day ?? "", focus: m.n, query: query ?? "")
        }
        .onChange(of: detail?.matches) {
            guard UserDefaults.standard.bool(forKey: "showKnowledgeDay"), debugDay == nil, entry.kind == KBKind.chats.rawValue else { return }
            debugDay = detail?.matches?.first(where: { $0.day != nil })
        }
        #endif
        .sheet(isPresented: $newAgent) {
            NewChatView(initialPrompt: agentPrompt) { s in
                newAgent = false
                model.open(s.key)
            }
        }
        .task { await load() }
        .refreshable { await load() }
        #if os(macOS)
        .macRefreshes { await load() }
        #endif
    }

    // MARK: Header

    private var header: some View {
        let e = shown
        return VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(alignment: .center, spacing: Theme.Space.m) {
                KBAvatar(entry: e, size: 48)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(e.name).font(Theme.Font.title).lineLimit(3)
                    HStack(spacing: Theme.Space.sm) {
                        ImportanceBadge(importance: e.importance)
                        if let k = e.projectKind { KBPill(text: String(localized: "\(k) project"), tint: (e.extra ?? "").hasPrefix("long") ? Theme.Hue.blue : nil) }
                        if let st = projectStatus { KBPill(text: st) }
                        if e.relevance > 0 { KBPill(text: String(localized: "Relevance \(e.relevance)"), tint: RelevanceMeter.tint(e.relevance)) }
                    }
                }
            }
            if let line = e.oneLine, !line.isEmpty {
                Text(line).font(Theme.Font.body).foregroundStyle(.primary.opacity(0.85)).fixedSize(horizontal: false, vertical: true)
            }
            if let tags = e.tags, !tags.isEmpty {
                LarkFlowLayout(spacing: 6, lineSpacing: 6) {
                    ForEach(tags, id: \.self) { t in KBPill(text: "#" + t) }
                }
            }
            HStack(spacing: Theme.Space.m) {
                Button { Task { await ask() } } label: {
                    // White on the prominent fill: the symbol took the tint and vanished into the blue.
                    Label("Ask Persona", systemImage: Theme.Icon.ai).lineLimit(1).frame(maxWidth: Self.buttonsFill)
                        .foregroundStyle(.white)
                }
                .themedButton(prominent: true)
                if entry.kind == KBKind.projects.rawValue {
                    Button { newAgent = true } label: {
                        Label("Start Agent", systemImage: Theme.Icon.newConversation).lineLimit(1).frame(maxWidth: Self.buttonsFill)
                    }
                    .themedButton()
                }
            }
            .controlSize(Self.buttonsSize)
            .padding(.top, Theme.Space.xxs)
        }
    }

    // The phone's two buttons share the row; the Mac's are the regular size, side by side at the leading edge
    // (docs/apple/mac-design.md: no buttons the width of the page, #2318).
    #if os(macOS)
    private static let buttonsFill: CGFloat? = nil
    private static let buttonsSize = ControlSize.regular
    #else
    private static let buttonsFill: CGFloat? = .infinity
    private static let buttonsSize = ControlSize.large
    #endif

    /// A project's status when it is not the usual Active.
    private var projectStatus: String? {
        guard entry.kind == KBKind.projects.rawValue, let st = detail?.detail.meta?["status"]?.string ?? entry.extra?.components(separatedBy: " · ").last else { return nil }
        switch st {
        case "paused": return String(localized: "Pause")
        case "done": return String(localized: "Done [Task Sheets f28461]")
        case "ignored": return String(localized: "Not a Project")
        case "merged": return String(localized: "Merged")
        default: return nil
        }
    }

    private func sectionHeader(_ title: String, _ count: Int) -> some View {
        HStack(spacing: Theme.Space.sm) {
            Text(title)
            Text("\(count)").foregroundStyle(Color.secondary.opacity(0.7)).monospacedDigit()
        }
    }

    // MARK: Moments

    /// Posts in Moments about this project (the hub finds them by the project's keywords and has the model keep the
    /// ones really about it): who, when, comments, how it bears on the project; open one for the text and comments.
    private func momentsSection(_ moments: [KBMomentRef]) -> some View {
        let shownMoments = allMoments ? moments : Array(moments.prefix(Self.linksShown))
        return Section {
            ForEach(shownMoments) { m in
                NavigationLink { KBMomentView(ref: m) } label: { KBMomentRow(moment: m) }
                    .accessibilityIdentifier("kb.moment")
            }
            if moments.count > Self.linksShown {
                Button { withAnimation(Theme.Motion.standard) { allMoments.toggle() } } label: {
                    Text(allMoments ? String(localized: "Collapse") : String(localized: "All \(moments.count) [Knowledge View 7c7192]")).font(Theme.Font.secondaryMedium).lineLimit(1)
                }
            }
        } header: { sectionHeader(String(localized: "Related \(KBMoments.feedName(moments))"), moments.count) }
    }

    /// A project merged into another (same work under two names): where it went.
    private func mergedSection(_ into: String) -> some View {
        Section {
            NavigationLink {
                KnowledgeDetailView(entry: KBEntry(kind: KBKind.projects.rawValue, id: into, name: into, relevance: 0))
            } label: {
                Label("Merged into Another Project", systemImage: Theme.Icon.link).lineLimit(1)
            }
        } footer: {
            Text("This is the same thing as another project, so its timeline, chats and people were moved there. This shows how it looked before the merge.")
        }
    }

    // MARK: Facts

    /// A doc's, meeting's or event's when and link: the document opens in the app (CLAUDE.md「链接在 App 内打开」).
    private func factsSection(_ f: ItemFacts) -> some View {
        Section {
            if let at = f.at {
                LabeledContent(f.whenLabel) {
                    Text(ItemFacts.format(at)).monospacedDigit().lineLimit(1)
                }
            }
            if let url = f.url {
                Button {
                    if model.assistantMain ?? model.assistantCurrent != nil { link = LinkTarget(url: url) } else { openURL(url) }
                } label: {
                    Label(f.openLabel, systemImage: Theme.Icon.openDocument).lineLimit(1)
                }
                .accessibilityIdentifier("kb.openLink")
            }
        }
    }

    // MARK: Found

    /// Where the search is in this entry: each place with what is around it; a message opens its day at it.
    @ViewBuilder private func found(_ matches: [KBMatch], query: String, chat: String?) -> some View {
        let shownMatches = allMatches ? matches : Array(matches.prefix(Self.matchesShown))
        Section {
            if matches.isEmpty {
                // Found by its name, or in a part of it the page shows below.
                Text("“\(query)” in names or profiles").font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
            ForEach(shownMatches) { m in
                if let chat, let day = m.day {
                    NavigationLink {
                        KBDayView(chat: chat, chatName: shown.name, day: day, focus: m.n, query: query)
                    } label: { KBMatchRow(match: m, query: query) }
                    .accessibilityIdentifier("kb.match")
                } else {
                    KBMatchRow(match: m, query: query)
                }
            }
            if matches.count > Self.matchesShown {
                Button { withAnimation(Theme.Motion.standard) { allMatches.toggle() } } label: {
                    Text(allMatches ? String(localized: "Collapse") : String(localized: "Show All \(matches.count) Matches")).font(Theme.Font.secondaryMedium).lineLimit(1)
                }
            }
        } header: {
            HStack(spacing: Theme.Space.sm) {
                Text("Search “\(query)”")
                Text(matches.count >= 40 ? "40+" : "\(matches.count)").foregroundStyle(Color.secondary.opacity(0.7)).monospacedDigit()
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("kb.matches")
        }
    }

    // MARK: Timeline

    /// Newest first, a dot and a rail down the left, when and from where above each line.
    @ViewBuilder private func timeline(_ log: [String]) -> some View {
        let lines = log.reversed().map(KBLogLine.init)
        let shownLines = allLog ? lines : Array(lines.prefix(Self.logShown))
        Section {
            ForEach(Array(shownLines.enumerated()), id: \.offset) { i, l in
                KBTimelineRow(line: l, first: i == 0, last: i == shownLines.count - 1 && (allLog || lines.count <= Self.logShown))
                    .listRowSeparator(.hidden)
                    .listRowInsets(EdgeInsets(top: 0, leading: 16, bottom: 0, trailing: 16))
            }
            if lines.count > Self.logShown {
                Button { withAnimation(Theme.Motion.standard) { allLog.toggle() } } label: {
                    Text(allLog ? String(localized: "Collapse") : String(localized: "Show All \(lines.count) Entries")).font(Theme.Font.secondaryMedium).lineLimit(1)
                }
                .listRowSeparator(.hidden)
            }
        } header: { sectionHeader(String(localized: "Timeline [Knowledge View f1241a]"), lines.count) }
    }

    // MARK: Related

    /// Related entries by kind (projects, chats, people…), a few each with All for the rest; then the code directories.
    @ViewBuilder private func links(_ d: KBDetail) -> some View {
        let links = d.links ?? []
        let dirs = d.meta?["dirs"]?.array?.compactMap(\.string) ?? []
        let byKind = Dictionary(grouping: links, by: \.kind)
        ForEach(KBKind.allCases) { k in
            if let group = byKind[k.rawValue], !group.isEmpty {
                let open = openKinds.contains(k.rawValue)
                Section {
                    ForEach(open ? group : Array(group.prefix(Self.linksShown))) { e in
                        NavigationLink { KnowledgeDetailView(entry: e) } label: { KBLinkRow(entry: e) }
                    }
                    if group.count > Self.linksShown {
                        Button {
                            withAnimation(Theme.Motion.standard) {
                                if open { openKinds.remove(k.rawValue) } else { openKinds.insert(k.rawValue) }
                            }
                        } label: {
                            Text(open ? String(localized: "Collapse") : String(localized: "All \(group.count) [Knowledge View 1f585d]")).font(Theme.Font.secondaryMedium).lineLimit(1)
                        }
                    }
                } header: { sectionHeader(String(localized: "Linked \(k.title)"), group.count) }
            }
        }
        if !dirs.isEmpty {
            Section {
                ForEach(dirs, id: \.self) { dir in
                    Label {
                        Text(dir).font(Theme.Font.secondary.monospaced()).lineLimit(1).truncationMode(.head)
                    } icon: {
                        ThemedIcon(Theme.Icon.folder).foregroundStyle(.secondary)
                    }
                }
            } header: { sectionHeader(String(localized: "Code Folders"), dirs.count) }
        }
    }

    /// The profile without what the header already shows: its title, the one line, the tags line of a person's
    /// generated profile (「重要级 P0 · 与我相关性 95 · …」).
    static func profile(_ md: String, oneLine: String?) -> String {
        let one = oneLine?.trimmingCharacters(in: .whitespaces) ?? ""
        return dropTitle(md).components(separatedBy: "\n").filter { line in
            let t = line.trimmingCharacters(in: .whitespaces)
            return !(t.hasPrefix("重要级 ") && t.contains("相关性")) && (one.isEmpty || t != one) // i18n-ignore
        }
        .joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    static func dropTitle(_ md: String) -> String {
        guard md.hasPrefix("# ") else { return md }
        return md.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: false).dropFirst().joined(separator: "\n")
    }

    /// The entry as last loaded (a linked row or a search hit starts with only its name or id).
    private var shown: KBEntry {
        guard let m = detail?.detail.meta else { return entry }
        var e = entry
        if let n = m["name"]?.string ?? m["title"]?.string, !n.isEmpty { e.name = n }
        if let i = m["importance"]?.string { e.importance = i }
        if let r = m["relevance"]?.int { e.relevance = r }
        if let t = m["tags"]?.array { e.tags = t.compactMap(\.string) }
        if let l = m["one_line"]?.string ?? m["note"]?.string, !l.isEmpty { e.oneLine = l }
        if e.kind == KBKind.projects.rawValue, let k = m["kind"]?.string { e.extra = k + " · " + (m["status"]?.string ?? "active") }
        return e
    }

    private var context: ContextRef {
        let kindTitle = KBKind(rawValue: entry.kind)?.title ?? ""
        var text = detail?.detail.summary ?? entry.oneLine ?? ""
        if let log = detail?.detail.log, !log.isEmpty { text += "\n\n时间线（最近）：\n" + log.suffix(10).joined(separator: "\n") } // i18n-ignore
        if let path = detail?.detail.path { text += "\n\n知识库目录：\(path)" } // i18n-ignore
        return ContextRef(kind: "context", title: "\(kindTitle) · \(shown.name)", url: "", text: text)
    }

    private var agentPrompt: String {
        context.xml + "\n\n"
    }

    private func ask() async {
        await model.askPersona(context.xml + "\n\n说说「\(shown.name)」现在怎么样了：最新进展、在等我的事、接下来该做什么，能替我处理的先处理。") // i18n-ignore
    }

    private func load() async {
        guard let api = model.api else { return }
        do {
            detail = try await api.kbDetail(entry.kind, id: entry.id, query: query)
            error = nil
        } catch {
            // Cancelled (the page went away mid-load) is not an error to show.
            if let shown = error.shown { self.error = shown }
        }
    }
}

/// A Moments post in a project: author, day and comments on a line, how it bears on the project, then the text.
struct KBMomentRow: View {
    var moment: KBMomentRef

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.sm) {
                Text(moment.author).font(Theme.Font.labelStrong).lineLimit(1)
                if let at = moment.at { Text(Fmt.relative(at)).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1) }
                Spacer(minLength: 0)
                if moment.comments > 0 {
                    HStack(spacing: Theme.Space.xxs) {
                        ThemedIcon(Theme.Icon.conversation)
                        Text("\(moment.comments)").monospacedDigit()
                    }
                    .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).fixedSize()
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("\(moment.comments) comments")
                }
            }
            if let note = moment.note, !note.isEmpty {
                Text(note).font(Theme.Font.secondaryMedium).foregroundStyle(Theme.accent).lineLimit(1)
            }
            Text(moment.text).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
        }
        .padding(.vertical, Theme.Space.xxs)
        .accessibilityElement(children: .combine)
    }
}

/// One archived Moments post: its text and every comment as the crawler kept them, long ones in chunks.
struct KBMomentView: View {
    @Environment(AppModel.self) private var model
    var ref: KBMomentRef
    @State private var chunks: [String]?
    @State private var error: String?
    /// The archive's name for the feed, once loaded (the project's row may not have said).
    @State private var feed: String?

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Theme.Space.m) {
                if let note = ref.note, !note.isEmpty {
                    Label(note, systemImage: Theme.Icon.link).font(Theme.Font.secondaryMedium).foregroundStyle(Theme.accent)
                }
                if let chunks {
                    ForEach(Array(chunks.enumerated()), id: \.offset) { _, c in
                        Markdown(c).markdownTheme(.persona)
                    }
                } else if let error {
                    Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger).font(Theme.Font.caption)
                } else {
                    ProgressView().frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xl)
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
        }
        .themedPage()
        .detailTitle(KBMoments.feedName(feed ?? ref.feed))
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        guard let api = model.api else { return }
        do {
            let post = try await api.kbMoment(ref.id)
            if let f = post.post?.feed, !f.isEmpty { feed = f }
            chunks = MarkdownChunks.split(Links.markdown(KBMomentView.dropTitle(post.markdown)), lines: 40)
            error = nil
        } catch {
            if let shown = error.shown { self.error = shown }
        }
    }

    /// The archive's "# <feed> · title" line: the page title says it.
    static func dropTitle(_ md: String) -> String {
        KnowledgeDetailView.dropTitle(md).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// What a doc, meeting or event says about itself besides its tagging: when, and its link.
struct ItemFacts {
    var at: Date?
    var url: URL?
    var whenLabel: String
    var openLabel: String
    var isEmpty: Bool { at == nil && url == nil }

    init?(kind: String, meta: JSONValue?) {
        guard let k = KBKind(rawValue: kind), [.docs, .meetings, .events].contains(k), let meta else { return nil }
        at = meta["at"]?.string.flatMap(Self.parse)
        url = meta["url"]?.string.flatMap { $0.isEmpty ? nil : URL(string: $0) }
        whenLabel = k == .docs ? String(localized: "Mentioned") : k == .meetings ? String(localized: "Meeting Time") : String(localized: "Time")
        openLabel = k == .docs ? String(localized: "Open Document") : k == .meetings ? String(localized: "Open Meeting Notes") : String(localized: "Open Event")
    }

    /// "Fri, Oct 30, 14:00" (24-hour: SwiftUI's hour style is 12-hour in Chinese, docs/lessons/ios.md), the year when not this one.
    static func format(_ d: Date, now: Date = .now) -> String {
        let f = DateFormatter()
        f.locale = .current
        let sameYear = Calendar.current.component(.year, from: d) == Calendar.current.component(.year, from: now)
        // Templates with HH keep the 24-hour clock; zh-Hans renders 「M月d日 EEE HH:mm」 / 「y年M月d日 HH:mm」.
        f.setLocalizedDateFormatFromTemplate(sameYear ? "MMMdEEEHHmm" : "yMMMdHHmm")
        return f.string(from: d)
    }

    /// RFC 3339 with or without fractions (Go writes nanoseconds).
    /// The fraction is dropped (ISO8601DateFormatter does not take nine digits); Go's zero time counts as none.
    static func parse(_ s: String) -> Date? {
        let whole = s.replacingOccurrences(of: #"\.\d+"#, with: "", options: .regularExpression)
        let d = ISO8601DateFormatter().date(from: whole)
        return d.flatMap { $0.timeIntervalSince1970 > 0 ? $0 : nil }
    }
}

/// A place a search found in an entry: where (Profile, Chat History · day), the line with the words in bold, and for a message
/// the one before and after it in grey.
struct KBMatchRow: View {
    var match: KBMatch
    var query: String

    var body: some View {
        let hit = match.hit
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(hit.day.map { String(localized: "Chat History · \(KBDayView.title($0))") } ?? hit.source)
                .font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1)
            if match.day != nil {
                if let b = match.before?.last { context(b) }
                KBMessageText(line: KBMessageLine(match.line), query: query, lineLimit: 4)
                if let a = match.after?.first { context(a) }
            } else {
                Text(KBHitRow.marked(hit.text, query)).font(Theme.Font.secondary).lineLimit(4)
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    private func context(_ raw: String) -> some View {
        let l = KBMessageLine(raw)
        return Text(l.sender.isEmpty ? l.text : String(localized: "\(l.sender): \(l.text)"))
            .font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1)
    }
}

/// One archived message: who and when on a line, then what was said, the searched words in bold.
struct KBMessageText: View {
    var line: KBMessageLine
    var query: String
    var lineLimit: Int? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            if !line.sender.isEmpty || !line.time.isEmpty {
                HStack(spacing: Theme.Space.sm) {
                    Text(line.sender).font(Theme.Font.labelStrong).lineLimit(1)
                    Text(line.time).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.tertiary).lineLimit(1)
                }
            }
            Text(KBHitRow.marked(line.text, query)).font(Theme.Font.secondary).lineLimit(lineLimit)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}

/// One day of a chat's archive, opened at a found message (marked, and scrolled to), with the days around it.
struct KBDayView: View {
    @Environment(AppModel.self) private var model
    var chat: String
    var chatName: String
    @State var day: String
    /// The found message's line in the day file (it may be a continuation line of a longer message).
    var focus: Int?
    var query: String
    @State private var page: KBDay?
    @State private var error: String?

    var body: some View {
        ScrollViewReader { proxy in
            List {
                if let page {
                    Section {
                        ForEach(page.messages) { m in
                            let on = m.n == target(page)
                            KBMessageText(line: KBMessageLine(m.text), query: query)
                                .padding(.vertical, Theme.Space.xxs)
                                .listRowBackground(on ? Theme.accent.opacity(0.12) : nil)
                                .id(m.n)
                                .accessibilityIdentifier(on ? "kb.focus" : "")
                        }
                    } header: {
                        Text("\(page.messages.count) messages")
                    }
                    Section {
                        HStack {
                            if let prev = page.prev, !prev.isEmpty {
                                Button { go(prev) } label: { Label(Self.title(prev), systemImage: Theme.Icon.back).lineLimit(1) }
                            }
                            Spacer()
                            if let next = page.next, !next.isEmpty {
                                Button { go(next) } label: {
                                    Label(Self.title(next), systemImage: Theme.Icon.disclosure).labelStyle(TrailingIcon()).lineLimit(1)
                                }
                            }
                        }
                        .buttonStyle(.borderless)
                        .font(Theme.Font.secondaryMedium)
                    }
                } else if let error {
                    Section { Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
                } else {
                    Section { ProgressView().frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xl) }.listRowBackground(Color.clear)
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .navigationBarTitleDisplayMode(.inline)
            #if os(macOS)
            // The day and its chat in the title bar's row (#855); a principal item would say the day twice.
            .pageTitle(Self.title(day), subtitle: chatName)
            #else
            .navigationTitle(Self.title(day))
            .pageToolbar {
                ToolbarItem(placement: .principal) {
                    VStack(spacing: 0) {
                        Text(Self.title(day)).font(Theme.Font.heading).lineLimit(1)
                        Text(chatName).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
            }
            #endif
            .task(id: day) {
                await load()
                // Laid out first, then to the found message (a few rows above it stay in sight).
                guard let page, let t = target(page), page.day == day else { return }
                try? await Task.sleep(for: .milliseconds(80))
                proxy.scrollTo(t, anchor: .center)
            }
        }
    }

    /// The message the focus line is in: the last one starting at or before it.
    private func target(_ page: KBDay) -> Int? {
        guard let focus, page.day == day else { return nil }
        return page.messages.last(where: { $0.n <= focus })?.n
    }

    private func go(_ d: String) {
        Haptics.tap()
        day = d
    }

    private func load() async {
        guard let api = model.api else { return }
        do {
            page = try await api.kbDay(chat: chat, day: day)
            error = nil
        } catch {
            if let shown = error.shown { self.error = shown }
        }
    }

    /// "2026-10-30" → "Fri, Oct 30" (with the year when it is not this one).
    static func title(_ day: String) -> String {
        guard let d = dayFormat.date(from: day) else { return day }
        let sameYear = Calendar.current.component(.year, from: d) == Calendar.current.component(.year, from: .now)
        return (sameYear ? shortFormat : longFormat).string(from: d)
    }
    private static let dayFormat: DateFormatter = {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd"
        return f
    }()
    private static let shortFormat: DateFormatter = {
        let f = DateFormatter()
        f.locale = .current
        f.setLocalizedDateFormatFromTemplate("MMMdEEE") // zh-Hans: 「M月d日 EEE」
        return f
    }()
    private static let longFormat: DateFormatter = {
        let f = DateFormatter()
        f.locale = .current
        f.setLocalizedDateFormatFromTemplate("yMMMdEEE") // zh-Hans: 「y年M月d日 EEE」
        return f
    }()
}

/// Title, then icon (a "next day ›" button).
private struct TrailingIcon: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: Theme.Space.xs) { configuration.title; configuration.icon }
    }
}

/// One timeline line: a dot on a rail, when and from where in small type, then what happened.
struct KBTimelineRow: View {
    var line: KBLogLine
    var first: Bool
    var last: Bool
    static let rail = Color.primary.opacity(0.1)

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            ZStack(alignment: .top) {
                // The rail joins the dots: none above the newest, none below the oldest shown.
                VStack(spacing: 0) {
                    Rectangle().fill(first ? Color.clear : Self.rail).scaledFrame(width: Theme.Size.rule, height: Theme.Size.markS)
                    Rectangle().fill(last ? Color.clear : Self.rail).frame(width: Theme.Size.rule).frame(maxHeight: .infinity)
                }
                Circle().fill(first ? Theme.accent : Color.secondary.opacity(0.45))
                    .scaledFrame(width: Theme.Size.dotL, height: Theme.Size.dotL)
                    .padding(.top, Theme.Space.m)
            }
            .scaledFrame(width: Theme.Size.markXS)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.sm) {
                    if !line.when.isEmpty { Text(line.shortWhen()).font(Theme.Font.labelStrong.monospacedDigit()) }
                    if !line.source.isEmpty { Text(line.source).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1) }
                }
                .foregroundStyle(first ? Theme.accent : .secondary)
                Text(line.text).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .padding(.vertical, Theme.Space.s)
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
    }
}

/// A related entry: its tile, name and one line.
struct KBLinkRow: View {
    var entry: KBEntry

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            KBAvatar(entry: entry, size: 30)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(entry.name).font(Theme.Font.secondaryMedium).lineLimit(1)
                if let l = entry.oneLine, !l.isEmpty { Text(l).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1) }
            }
        }
    }
}
/// Importance, relevance, tags (and a project's kind / status) set by hand; the hub keeps them over the automatic ones.
struct KBTagEditor: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var entry: KBEntry
    var saved: () async -> Void
    @State private var importance = ""
    @State private var relevance = 50.0
    @State private var tags = ""
    @State private var projectKind = "short"
    @State private var status = "active"
    @State private var saving = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section("Importance") {
                    Picker("Importance", selection: $importance) {
                        ForEach(["P0", "P1", "P2", "P3"], id: \.self) { Text($0).tag($0) }
                    }
                    .pickerStyle(.segmented)
                }
                Section {
                    Slider(value: $relevance, in: 0...100, step: 5)
                    #if os(macOS)
                        .labelsHidden() // its empty label column took the left half of the row
                    #endif
                } header: { Text("Relevance to Me \(Int(relevance))") }
                Section {
                    #if os(macOS)
                    // A grouped Mac form shows a titled field as label + trailing value: the hint read as a label.
                    TextField(String(localized: "Separate with spaces"), text: $tags, prompt: Text("Separate with spaces")).labelsHidden()
                    #else
                    TextField(String(localized: "Separate with spaces"), text: $tags)
                    #endif
                } header: { Text("Tags") }
                if entry.kind == KBKind.projects.rawValue {
                    Section("Project") {
                        Picker("Type", selection: $projectKind) {
                            Text("Short-Term (Request)").tag("short")
                            Text("Long-Term (Mission)").tag("long")
                        }
                        Picker("Status", selection: $status) {
                            Text("Active").tag("active")
                            Text("Pause").tag("paused")
                            Text("Done [Task Sheets f28461]").tag("done")
                            Text("Not a Project").tag("ignored")
                        }
                    }
                }
                if let error {
                    Section { Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
                }
            }
            .navigationTitle(entry.name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await save() } }.disabled(saving)
                }
            }
            .onAppear {
                importance = entry.importance?.isEmpty == false ? entry.importance! : "P2"
                relevance = Double(entry.relevance)
                tags = (entry.tags ?? []).joined(separator: " ")
                if let extra = entry.extra, entry.kind == KBKind.projects.rawValue {
                    let parts = extra.components(separatedBy: " · ")
                    projectKind = parts.first ?? "short"
                    status = parts.count > 1 ? parts[1] : "active"
                }
            }
        }
        .presentationDetents([.medium, .large])
        #if os(macOS)
        // This sheet did not pick up macLook's grouped form (cause unknown): the two-column one cut rows off.
        .macSheet()
        #endif
    }

    private func save() async {
        guard let api = model.api else { return }
        saving = true
        defer { saving = false }
        let isProject = entry.kind == KBKind.projects.rawValue
        let u = KBUpdate(importance: importance, relevance: Int(relevance),
                         tags: tags.split(whereSeparator: \.isWhitespace).map(String.init),
                         status: isProject ? status : nil, projectKind: isProject ? projectKind : nil)
        do {
            try await api.kbUpdate(entry.kind, id: entry.id, u)
            Haptics.success()
            await saved()
            dismiss()
        } catch {
            self.error = error.shown
            Haptics.error()
        }
    }
}

/// An Agent handed to Persona: who it is and where it runs, so Persona can read it, follow up, and file its result
/// under the right project (the knowledge base's project dirs).
enum AgentHandoff {
    static func prompt(_ s: Session, machine: String?) -> String {
        let title = s.title.isEmpty ? (s.cwd as NSString).lastPathComponent : s.title
        var text = "机器：\(machine ?? s.machineID)（\(s.machineID)）\n会话：\(s.sessionID)\n目录：\(s.cwd)\n状态：\(s.state.rawValue)" // i18n-ignore
        if let last = s.lastText, !last.isEmpty { text += "\n最后说的：\(String(last.prefix(800)))" } // i18n-ignore
        let ref = ContextRef(kind: "context", title: "Agent · \(title)", url: "", text: text)
        return ref.xml + "\n\n看一下这个 Agent（agent_read）：它在做什么、做到哪了、有没有在等我。它属于哪个项目就把进展记进项目（kb_update，没登记目录的顺便登记 dirs）；" // i18n-ignore
            + "需要我拍板的列出来，能替我推进的直接推进。" // i18n-ignore
    }
}
