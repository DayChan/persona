import BeautifulUI
import SwiftUI

@MainActor final class InboxAccountState: ObservableObject {
    @Published var accounts = StateCache.load("mail-accounts", as: [MailAccount].self) ?? []
}

// MARK: - Inbox

/// The unified inbox (docs/assistant/inbox.md): Feishu messages and mail in one list, one row per thing (the new messages in a
/// chat, a mail thread), newest first. No per-chat browsing: what is here is what came in. Filters narrow it by source
/// (Lark, one mail account) and kind (DMs, @Me, Group chat); a row swipes away as Done, and the done ones stay at the bottom
/// for a few days in case of a slip.
struct InboxView: View {
    @State private var path = NavigationPath()
    @StateObject private var accountState = InboxAccountState()

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("InboxView")
        #endif
        SplitNavigation(path: $path, destinations: InboxDestinations(accounts: accountState.accounts)) {
            InboxPage(path: $path, accountState: accountState)
        } empty: {
            SplitPlaceholder(symbol: Theme.Icon.inbox, title: String(localized: "Pick an Item"), message: String(localized: "Tap an item in the list on the left to see its details here."))
        }
        .environment(\.feishuOpen) { path.append($0) }
        .feishuHandlesLinks { path.append($0) }
    }
}

extension InboxView {
    /// A mail account's short name in the filters (the Mac's inbox uses it too).
    static func short(_ email: String) -> String { InboxPage.short(email) }
}

/// The inbox's page itself, in whatever stack shows it: Inbox's own (InboxView), or the shell's Assistant page (pushed from
/// the drawer, docs/apple/ios.md「新外壳」), which passes `embedded` so the page brings its destinations and link handling.
struct InboxPage: View {
    @Environment(AppModel.self) private var model
    @Binding var path: NavigationPath
    @ObservedObject var accountState: InboxAccountState
    var embedded = false
    @StoredValue("inbox.source") private var source = "" // "" all, "feishu", or a mail account id
    @StoredValue("inbox.kind") private var kind = ""     // "" all, agent, dm, mention, group, mail
    @State private var query = ""
    @State private var itemMemo = InboxProjectionMemo()
    private var accounts: [MailAccount] {
        get { accountState.accounts }
        nonmutating set { accountState.accounts = newValue }
    }
    /// Sections shown open: tiers (action, working, fyi, noise) and "done". Collapsed and Done start closed.
    @State private var expanded: Set<String> = ["action", "working", "fyi"]
    @State private var composing: ComposeDraft?
    @State private var editingDraft: InboxItem? // Edit on the AI Twin's draft
    @State private var answering: Interaction? // a card on no session, open by itself (#4059)
    @State private var showingMutes = false     // Muted
    @State private var showingRules = false     // Rules
    @State private var ruling: InboxPolicyTarget? // a row's Rule for This Chat / Sender / Repository…
    @State private var confirming = false // Clear All over many items asks first
    private var store: InboxStore { .shared }

    /// The kind filters, by key.
    static let kinds = [("mention", String(localized: "@Me")), ("dm", String(localized: "DMs")), ("group", String(localized: "Group chat")), ("mail", String(localized: "Mail [Assistant View 3d708e]")), ("agent", "Agent")]

    private var itemProjection: InboxProjection {
        itemMemo(store.items, filter: .init(source: source, kind: kind, query: query), revision: store.itemsRevision)
    }

    private var openItems: [InboxItem] { itemProjection.openItems }

    /// The hub's connectors with items in the list (GitHub…), one filter each; none, no chip.
    private var connectorSources: [InboxProjection.Source] { itemProjection.connectorSources }

    /// Agents (and the assistant) waiting for an answer, an approval or a plan review: they need the user too, so they
    /// head Needs You. Opening one goes to its chat; the Agents tab stays as it is.
    private var waitingAgents: [Interaction] {
        guard source.isEmpty, kind.isEmpty || kind == "agent" else { return [] }
        return model.pendingInteractions.filter { ix in
            !model.isMuted(ix.sessionKey) && (query.isEmpty || [ix.sessionTitle ?? "", ix.summary ?? "", ix.title].contains { $0.localizedCaseInsensitiveContains(query) })
        }
    }

    var body: some View {
        if embedded { // its destinations are the shell's (ShellDestinations)
            page
                .environment(\.feishuOpen) { path.append($0) }
                .feishuHandlesLinks { path.append($0) }
        } else {
            page
        }
    }

    private var page: some View {
            list
                .navigationTitle("Inbox")
                .navigationBarTitleDisplayMode(.inline)
                .pageSearchable(text: $query, prompt: String(localized: "Search Titles, Senders, and Content"))
                .pageToolbar {
                    ToolbarItem(placement: .principal) { ConnectionTitle(title: String(localized: "Inbox")) }
                    if model.isMultiHub {
                        // Each hub has its own inbox (docs/protocol/multi-hub.md「hub 级页面」): whose this is, and the others.
                        ToolbarItem(placement: .topBarLeading) { HubSwitcher(compact: true) }
                    }
                    ToolbarItem(placement: .topBarTrailing) {

                        // Mail accounts are managed in Settings › Connectors › Mail (2026-11-01, with this device's data and Lark).
                        Menu {
                            cleanButton
                            Divider()
                            Button { composing = ComposeDraft(accountID: accounts.first?.id ?? "") } label: { Label("New Email", systemImage: Theme.Icon.compose) }
                                .disabled(accounts.isEmpty)
                            Button { showingMutes = true } label: { Label("Muted [Inbox Mutes View e5e468]", systemImage: Theme.Icon.mute) }
                            Button { showingRules = true } label: { Label("Rules [Inbox Policy]", systemImage: Theme.Icon.inboxRule) }
                        } label: { ThemedIcon(Theme.Icon.moreMenu) }
                        .accessibilityLabel("More")
                        .accessibilityIdentifier("inbox.more")
                    }
                }
                .sheet(item: $composing) { d in ComposeView(draft: d, accounts: accounts) }
                .sheet(item: $editingDraft) { InboxDraftSheet(item: $0, accounts: accounts) }
                .sheet(item: $answering) { SessionlessCardSheet(interaction: $0) }
                .sheet(isPresented: $showingMutes) { InboxMutesView() }
                .sheet(isPresented: $showingRules) { InboxRulesView() }
                .sheet(item: $ruling) { InboxPolicySheet(target: $0) }
                .confirmationDialog(InboxBulk.confirmTitle, isPresented: $confirming, titleVisibility: .visible) {
                    Button(InboxBulk.confirmButton(count: cleanScope.count)) { clean(confirmed: true) }
                        .accessibilityIdentifier("inbox.cleanConfirm")
                    Button("Cancel", role: .cancel) {}
                } message: {
                    Text(InboxBulk.confirmMessage(count: cleanScope.count, filters: filterNames, query: query))
                }
                // A plugin enabled or set up since: its sources' items open as it draws them.
                .task { await InboxItemSlots.shared.reload() }
                .task {
                    store.reload()
                    await loadAccounts()
                    #if DEBUG
                    // `-showInboxMutes YES`: the Muted list (screenshots).
                    if UserDefaults.standard.bool(forKey: "showInboxMutes") { showingMutes = true }
                    // `-showInboxRules YES`: the Rules list. `-openInboxRule chat|sender|repo`: the rule sheet of the first open
                    // item with one, as its menu opens it; `edit`: Rules, then Edit on the newest rule (screenshots).
                    if UserDefaults.standard.bool(forKey: "showInboxRules") || UserDefaults.standard.string(forKey: "openInboxRule") == "edit" {
                        showingRules = true
                    } else if let tag = UserDefaults.standard.string(forKey: "openInboxRule") {
                        for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                        for it in openItems {
                            if let m = it.mutes?.first(where: { $0.tag == tag }) { ruling = InboxPolicyTarget(item: it, option: m); break }
                        }
                    }
                    // `-openInboxFirst YES`: open the first open item (detail screenshots).
                    if UserDefaults.standard.bool(forKey: "openInboxFirst") {
                        for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                        if let first = openItems.first { path = NavigationPath([first]) }
                    }
                    // `-openInboxItem <id>`: open that item (a kind's detail, e.g. a Moments one).
                    if let id = UserDefaults.standard.string(forKey: "openInboxItem") {
                        for _ in 0..<20 where !store.items.contains(where: { $0.id == id }) { try? await Task.sleep(for: .milliseconds(500)) }
                        if let it = store.items.first(where: { $0.id == id }) { path = NavigationPath([it]) }
                    }
                    // `-openInboxAsk <id>`: open that waiting agent's row as a tap does (a sessionless card in its sheet, #4059).
                    if let id = UserDefaults.standard.string(forKey: "openInboxAsk") {
                        for _ in 0..<20 where !model.pendingInteractions.contains(where: { $0.id == id }) { try? await Task.sleep(for: .milliseconds(500)) }
                        if let ix = model.pendingInteractions.first(where: { $0.id == id }) { open(ix) }
                    }
                    #endif
                }
    }

    // MARK: Clear All

    /// The names of the filters on now (Lark, a mailbox, a connector; @Me, DMs…), for what Clear All covers.
    private var filterNames: [String] {
        var names: [String] = []
        if source == "feishu" {
            names.append(String(localized: "Lark [Inbox Event Views 7e4419]"))
        } else if let a = accounts.first(where: { $0.id == source }) {
            names.append(Self.short(a.email))
        } else if let c = connectorSources.first(where: { $0.id == source }) {
            names.append(c.name)
        }
        if let k = Self.kinds.first(where: { $0.0 == kind }) { names.append(k.1) }
        return names
    }

    /// What Clear All covers: the open items the list shows now (its filters and search). The Agents
    /// waiting at the top are not inbox items: they wait for an answer, not to be filed.
    private var cleanScope: [InboxItem] { kind == "agent" ? [] : openItems }

    /// Clear All, with what it covers and how many under the name: files every open item in view as dismissed; from
    /// `InboxBulk.confirmFrom` items on it asks first. One request, Undo after (docs/assistant/inbox.md「全部清理」).
    private var cleanButton: some View {
        let scope = cleanScope
        return Button { clean(confirmed: false) } label: {
            Label("Clean up all", systemImage: Theme.Icon.cleanUpAll)
            Text(InboxBulk.scopeLine(filters: filterNames, query: query, count: scope.count))
        }
        .disabled(scope.isEmpty)
        // A menu item's subtitle is drawn but not read out: the spoken name says what it covers and how many too.
        .accessibilityLabel(Text("Clean up all") + Text(verbatim: String(localized: ", ")) + Text(InboxBulk.scopeLine(filters: filterNames, query: query, count: scope.count)))
        .accessibilityIdentifier("inbox.cleanUpAll")
    }

    private func clean(confirmed: Bool) {
        let scope = cleanScope
        if !confirmed && InboxBulk.needsConfirm(scope.count) {
            confirming = true
            return
        }
        withAnimation(Theme.Motion.standard) { store.clean(scope) }
    }

    private var list: some View {
        let projection = itemProjection
        let openItems = projection.openItems
        let byTier = projection.byTier
        let doneItems = projection.doneItems
        let shownDone = expanded.contains("done") ? projection.sortedDoneItems : []
        let waitingAgents = waitingAgents
        return List {
            filters
                .listRowInsets(EdgeInsets(top: 4, leading: 0, bottom: 8, trailing: 0))
                .listRowSeparator(.hidden)
                .themedRow()
            // New items the hub holds back until the model sorts them (#1773): above the first item.
            if let line = InboxTriageText.line(store.triage) {
                InboxTriageBanner(line: line, configurable: InboxTriageText.configurable(store.triage, mayChangeSettings: !model.isDemo))
                    .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.page, bottom: Theme.Space.s, trailing: Theme.Space.page))
                    .listRowSeparator(.hidden)
                    .listRowBackground(Color.clear)
            }
            if let error = store.error, store.items.isEmpty {
                Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    .themedRow()
            }
            if openItems.isEmpty && waitingAgents.isEmpty && !store.loading {
                EmptyStateView(symbol: "checkmark.seal", title: query.isEmpty ? String(localized: "All Caught Up") : String(localized: "No Matches"),
                               message: query.isEmpty ? String(localized: "New Lark messages and emails show up here.") : String(localized: "Try a different search."))
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            }
            // The AI's triage: Needs You (and what it has not looked at yet), FYI, Collapsed (noise).
            ForEach(InboxItem.Tier.allCases, id: \.self) { tier in
                let items = kind == "agent" ? [] : byTier[tier] ?? []
                let agents = tier == .action ? waitingAgents : []
                if !items.isEmpty || !agents.isEmpty {
                    Section {
                        if expanded.contains(tier.rawValue) || !tier.folds {
                            ForEach(agents) { agentRow($0) }
                            ForEach(items) { row($0) }
                        }
                    } header: {
                        sectionHeader(tier.title, count: items.count + agents.count, key: tier.rawValue,
                                      clear: InboxPage.clearTitle(tier).map { ($0, { store.setDone(items, true) }) })
                    }
                }
            }
            if !doneItems.isEmpty {
                Section {
                    if expanded.contains("done") { ForEach(shownDone) { row($0) } }
                } header: {
                    sectionHeader(String(localized: "Done [Inbox View 59f6c8]"), count: doneItems.count, key: "done", clear: nil)
                }
            }
        }
        .listStyle(.plain)
        .themedPage()
        .refreshable { await store.refresh() }
        .overlay { if store.loading && store.items.isEmpty { ProgressView() } }
    }

    /// A section title that opens / closes the section, with an optional action on the right (List headers take no
    /// context menu, so it is a visible button).
    private func sectionHeader(_ title: String, count: Int, key: String, clear: (String, () -> Void)?) -> some View {
        HStack(spacing: Theme.Space.sm) {
            if InboxItem.Tier(rawValue: key)?.folds == false {
                // In Progress: always open, a plain title (inbox phase 1).
                Text("\(title) · \(count)").font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: 0)
            } else {
            Button {
                withAnimation(Theme.Motion.standard) { if expanded.contains(key) { expanded.remove(key) } else { expanded.insert(key) } }
            } label: {
                HStack(spacing: Theme.Space.sm) {
                    Text("\(title) · \(count)").font(Theme.Font.sectionHeader).foregroundStyle(key == "action" ? Color.primary : .secondary)
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelHeavy).foregroundStyle(.secondary)
                        .rotationEffect(.degrees(expanded.contains(key) ? 90 : 0))
                    Spacer(minLength: 0)
                }
                .lineLimit(1)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            }
            if let clear {
                Button(clear.0) { withAnimation(Theme.Motion.standard) { clear.1() } }
                    .font(Theme.Font.secondary)
                    .lineLimit(1)
                    .buttonStyle(.borderless)
            }
        }
        .textCase(nil)
        .padding(.vertical, Theme.Space.xxs)
    }

    private func agentRow(_ ix: Interaction) -> some View {
        Button { open(ix) } label: {
            InboxAgentRow(interaction: ix, machine: model.machine(ix.machineRef), machineName: model.machine(ix.machineRef)?.title ?? ix.machineID)
        }
        .buttonStyle(.plain)
        .modifier(InboxRowLook())
        .accessibilityIdentifier("inbox.agentRow")
    }

    /// The agent's chat (the assistant's in Chat), where the question / plan / approval waits.
    private func open(_ ix: Interaction) {
        // A card on no session has no chat: it opens by itself (#4059).
        if ix.isSessionless { answering = ix; return }
        model.open(ix.sessionKey) // in its own group (another group's Persona: that group's)
    }

    private func row(_ it: InboxItem) -> some View {
        InboxRow(item: it, showAccount: accounts.count > 1, sending: store.sending.contains(it.id),
                 send: { text in Task { await store.send(it, text: text, changes: it.agent?.changes ?? []) } }, edit: { editingDraft = it })
            .background { NavigationLink(value: it) { EmptyView() }.opacity(0) }
            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                if it.isDone {
                    Button { store.setDone(it, false) } label: { Label("Move Back", systemImage: Theme.Icon.undo) }.tint(Theme.accent)
                } else {
                    Button { store.setDone(it, true) } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done) }.tint(Theme.positive)
                }
            }
            .contextMenu { rowMenu(it) }
            .modifier(InboxRowLook())
    }

    /// A row's long-press menu: Done (or Move Back), then what the item can be muted by — its repository, chat or sender,
    /// the name under each (docs/assistant/inbox.md「静音」) — then a rule for each of those ("Policies").
    @ViewBuilder private func rowMenu(_ it: InboxItem) -> some View {
        if it.isDone {
            Button { store.setDone(it, false) } label: { Label("Move Back", systemImage: Theme.Icon.undo) }
        } else {
            Button { store.setDone(it, true) } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done) }
        }
        if let mutes = it.mutes, !mutes.isEmpty {
            Section {
                ForEach(mutes, id: \.self) { m in
                    Button { store.mute(it, m) } label: {
                        Label(m.title, systemImage: Theme.Icon.mute)
                        Text(m.shortName)
                    }
                }
            }
            Section { InboxRuleButtons(item: it) { ruling = $0 } }
        }
    }

    // MARK: Filters

    /// Sources (all, Lark, each mail account) then kinds, as one row of chips; in the BeautifulUI style its filter chips,
    /// wrapping to a second row (no sideways scrolling at the top of a page, like the Agents list).
    @ViewBuilder private var filters: some View {
        if AppStyle.current.usesBeautifulUI {
            LarkFlowLayout(spacing: Theme.Space.xs, lineSpacing: Theme.Space.sm) {
                filterChip(String(localized: "All"), on: source.isEmpty && kind.isEmpty) { source = ""; kind = "" }
                filterChip(String(localized: "Lark [Inbox Event Views 7e4419]"), on: source == "feishu") { source = source == "feishu" ? "" : "feishu" }
                ForEach(accounts) { a in
                    filterChip(Self.short(a.email), on: source == a.id) { source = source == a.id ? "" : a.id }
                }
                ForEach(connectorSources, id: \.id) { c in
                    filterChip(c.name, on: source == c.id) { source = source == c.id ? "" : c.id }
                }
                ForEach(Self.kinds, id: \.0) { k, title in
                    filterChip(title, on: kind == k) { kind = kind == k ? "" : k }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, Theme.Space.l)
        } else {
            plainFilters
        }
    }

    private func filterChip(_ title: String, on: Bool, action: @escaping () -> Void) -> some View {
        BUIFilterChip(title, isActive: on) {
            withAnimation(Theme.Motion.standard) { action() }
            Haptics.tap()
        }
    }

    private var plainFilters: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.s) {
                chip(String(localized: "All"), on: source.isEmpty && kind.isEmpty) { source = ""; kind = "" }
                chip(String(localized: "Lark [Inbox Event Views 7e4419]"), symbol: Theme.Icon.feishuChats, on: source == "feishu") { source = source == "feishu" ? "" : "feishu" }
                ForEach(accounts) { a in
                    chip(Self.short(a.email), symbol: Theme.Icon.mail, on: source == a.id) { source = source == a.id ? "" : a.id }
                }
                ForEach(connectorSources, id: \.id) { c in
                    chip(c.name, symbol: Theme.Icon.connectors, on: source == c.id) { source = source == c.id ? "" : c.id }
                }
                Divider().scaledFrame(height: Theme.Size.badge)
                ForEach(Self.kinds, id: \.0) { k, title in
                    chip(title, on: kind == k) { kind = kind == k ? "" : k }
                }
            }
            .padding(.horizontal, Theme.Space.l)
        }
    }

    private func chip(_ title: String, symbol: String? = nil, on: Bool, action: @escaping () -> Void) -> some View {
        Button {
            withAnimation(Theme.Motion.standard) { action() }
            Haptics.tap()
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if let symbol { ThemedIcon(symbol).font(Theme.Font.label) }
                Text(title)
            }
            .font(on ? Theme.Font.secondaryStrong : Theme.Font.secondary)
            .lineLimit(1)
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.sm)
            .foregroundStyle(on ? Color.white : Color.primary)
            .background(on ? AnyShapeStyle(Theme.accent) : AnyShapeStyle(Color.secondary.opacity(0.12)), in: .capsule)
        }
        .buttonStyle(.plain)
    }

    /// A section's clear-all on the right: mark all seen (FYI), clear all (Collapsed); none for Needs You and In Progress (the AI is
    /// on those).
    nonisolated static func clearTitle(_ tier: InboxItem.Tier) -> String? {
        switch tier {
        case .action, .working: nil
        case .fyi: String(localized: "Mark All Seen")
        case .noise: String(localized: "Clear All")
        }
    }

    /// The part of an address before the @ (chips stay short), unless that alone says nothing ("c@…").
    static func short(_ email: String) -> String {
        let name = String(email.split(separator: "@").first ?? Substring(email))
        return name.count >= 3 ? name : email
    }

    private func loadAccounts() async {
        guard let api = model.api, let a = try? await api.mailAccounts() else { return }
        accounts = a
        StateCache.save("mail-accounts", a)
    }
}

// MARK: - Row

struct InboxRow: View {
    var item: InboxItem
    var showAccount = false
    var sending = false
    /// Send / Edit on the AI twin's draft (the list only).
    var send: ((String) -> Void)?
    var edit: (() -> Void)?

    @StoredValue(TranslationPrefs.targetKey) private var translateTarget = TranslationPrefs.defaultTarget
    @StoredValue(TranslationPrefs.enabledKey) private var translating = false

    /// Settings › Translation: the hub's translation of foreign mail, done as it came in (subject and preview).
    private var translated: InboxItem.Translated? { translating && TranslationPrefs.mail ? item.translated(translateTarget) : nil }

    /// Where the text column starts inside the row (40 avatar + 12): the separator lines up with it.
    static let textColumn: CGFloat = 52

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            avatar
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.sm) {
                    Text(translated.map(\.subject).flatMap { $0.isEmpty ? nil : $0 } ?? item.title).font(item.isDone ? Theme.Font.body : Theme.Font.heading).foregroundStyle(item.isDone ? .secondary : .primary).lineLimit(1)
                    if translated != nil {
                        ThemedIcon(Theme.Icon.translate).font(Theme.Font.label).foregroundStyle(.tertiary)
                            .accessibilityLabel("Translated")
                    }
                    if let label = item.kindLabel {
                        Chip(text: label, look: item.kind == "mention" || item.kind == "doc" && label == String(localized: "@Me") ? .tone(StatusPalette.needsYou) : .neutral)
                    }
                    Spacer(minLength: 4)
                    Text(LarkTime.short(item.updatedAt)).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize()
                }
                HStack(alignment: .top, spacing: Theme.Space.sm) {
                    if let summary = item.summary, !summary.isEmpty {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            (Text(Image(systemName: Theme.Icon.ai)).foregroundStyle(Theme.ai) + Text(" " + summary))
                                // The hierarchical styles, not `Color.primary`: a Mac list's selected row turns that
                                // white, where the row sets the words' color for these (MacHoverRow).
                                .font(Theme.Font.secondary)
                                .foregroundStyle(item.isDone ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary.opacity(0.9))).lineLimit(2)
                            Text(from).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                    } else {
                        Text(from).font(Theme.Font.secondary).lineHeight(.secondary).foregroundStyle(.secondary).lineLimit(2)
                    }
                    Spacer(minLength: 0)
                    if item.count > 1 && !item.isDone {
                        // @Me is the accent (it is about you); any other count is neutral.
                        CountBadge(count: item.count, look: .solid(item.kind == "mention" ? Theme.accent : Theme.countBadge))
                    }
                }
                if item.doc != nil && !item.isDone {
                    InboxDocRowsPreview(item: item).padding(.top, Theme.Space.xxs)
                }
                if showAccount && !item.isLark {
                    Text(item.accountName).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
                if let mark = InboxEventText.mark(item) {
                    // Inbox phase 1: the suggestion (it replaces the AI agent's draft card, which a hub mirrors it
                    // into), In Progress's state, the 'preparing a suggestion' line; its buttons are in the detail.
                    InboxEventMarkLine(mark: mark).padding(.top, Theme.Space.xxs)
                } else if !item.isDone && item.suggestion == nil && item.handling == nil {
                    if let draft = item.agentDraft, let send {
                        InboxDraftCard(draft: draft, changes: (item.agent?.changes ?? []).map { DocChangeText.line($0, in: item.doc) },
                                       sending: sending, send: { send(draft) }, edit: edit)
                            .padding(.top, Theme.Space.sm)
                    } else if item.agentWorking {
                        HStack(spacing: Theme.Space.sm) {
                            InboxSpinner()
                            Text(item.agent?.state == "queued" ? String(localized: "AI twin is queued") : item.agent?.task.map { String(localized: "AI twin is working: \($0)") } ?? String(localized: "AI twin is working"))
                                .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                        }
                        .padding(.top, Theme.Space.xxs)
                    }
                }
            }
        }
        .padding(.vertical, Theme.Space.m)
        .contentShape(.rect)
    }

    /// Who wrote what (under the AI's summary once it has one).
    private var from: String {
        if let doc = item.doc { return doc.typeName + " · " + item.preview } // the preview names who: Li Lei mentioned you
        if item.moment != nil { return String(localized: "\(item.sender ?? ""): \(item.preview)", comment: "Who wrote it: what they wrote") } // who commented, on the post in the title
        if let c = item.connector { // example-org/example-repo#12 · bob: please take a look
            let who = [item.sender ?? "", item.preview].filter { !$0.isEmpty }.joined(separator: String(localized: ": ", comment: "Separator between a label and its value"))
            return [c.reference ?? "", who].filter { !$0.isEmpty }.joined(separator: " · ")
        }
        if let t = translated?.preview, !t.isEmpty, let sender = item.sender, !sender.isEmpty { return String(localized: "\(sender): \(t)", comment: "Who wrote it: what they wrote") }
        guard let sender = item.sender, !sender.isEmpty, item.lark?.isGroup != false || !item.isLark else { return item.preview }
        return String(localized: "\(sender): \(item.preview)", comment: "Who wrote it: what they wrote")
    }

    private var avatar: some View {
        ZStack(alignment: .bottomTrailing) {
            Group {
            if let doc = item.doc {
                InboxDocIcon(doc: doc)
            } else if item.moment != nil {
                RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
                    .fill((LarkColor.text("orange") ?? Theme.accent).opacity(0.15))
                    .scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
                    .overlay(ThemedIcon(Theme.Icon.moments).font(Theme.Font.bodyStrong).foregroundStyle(LarkColor.text("orange") ?? Theme.accent))
            } else if item.isLark {
                LarkAvatarImage(name: item.title, url: item.avatar, isGroup: item.lark?.isGroup ?? false, size: Theme.Size.avatar)
            } else {
                Monogram(name: item.sender ?? "", fill: Theme.monogramFill)
            }
            }
            .opacity(item.isDone ? 0.6 : 1)
            // The source: Lark's (or a connector's: GitHub's, a template image, in black) own mark on white; mail, or a
            // connector without a mark, a neutral gray, not the accent.
            Group {
                if let brand = item.isLark ? Theme.Brand.lark : item.connector != nil ? ConnectorText.brand(item.source) : nil {
                    BrandBadge(brand)
                } else {
                    ThemedIcon(item.connector != nil ? Theme.Icon.connectors : Theme.Icon.mailFilled)
                        .font(Theme.Font.labelHeavy).imageScale(.small).dynamicTypeSize(...DynamicTypeSize.large)
                        .foregroundStyle(.white)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(Color(uiColor: .systemGray), in: .circle)
                }
            }
            .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
            .overlay(Circle().stroke(Theme.pageBackground, lineWidth: 1.5))
            .offset(x: 4, y: 4)
        }
        .scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
    }
}

/// The AI twin's reply under an inbox row: one tap sends it as it is, Edit opens it in the draft sheet.
struct InboxDraftCard: View {
    var draft: String
    /// Status moves made with it (document items), one line each.
    var changes: [String] = []
    var sending: Bool
    var send: () -> Void
    var edit: (() -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Label("AI Twin's Draft Reply", systemImage: Theme.Icon.ai)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(Theme.ai)
                .lineLimit(1)
            Text(draft)
                .font(Theme.Font.secondary)
                .foregroundStyle(.primary)
                .lineLimit(4)
                .frame(maxWidth: .infinity, alignment: .leading)
            ForEach(changes, id: \.self) { c in
                Label(c, systemImage: Theme.Icon.inProgress)
                    .font(Theme.Font.captionMedium).foregroundStyle(.secondary).lineLimit(1)
            }
            if AppStyle.current.usesBeautifulUI { beautifulActions } else { plainActions }
        }
        .padding(Theme.Space.m)
        .modifier(DraftSurface())
    }

    /// BeautifulUI's RecommendationCard actions: Edit secondary, Send the accent.
    private var beautifulActions: some View {
        HStack(spacing: Theme.Space.s) {
            Spacer(minLength: 0)
            if let edit {
                Button("Edit [Inbox Event Views 37090f]", action: edit).buttonStyle(.bui(.secondary)).lineLimit(1)
            }
            Button(action: send) {
                HStack(spacing: Theme.Space.xs) {
                    if sending { BUISpinner(size: Theme.Size.dotXL) }
                    Text("Send").lineLimit(1)
                }
            }
            .buttonStyle(.bui(.accent))
            .disabled(sending)
            .accessibilityLabel("Send the AI twin's draft reply")
        }
    }

    private var plainActions: some View {
        HStack(spacing: Theme.Space.s) {
            Spacer(minLength: 0)
            if let edit {
                Button(action: edit) {
                    Text("Edit [Inbox Event Views 37090f]").font(Theme.Font.secondaryMedium).lineLimit(1)
                        .padding(.horizontal, Theme.Space.l).padding(.vertical, Theme.Space.sm)
                        .foregroundStyle(Theme.accent)
                        .background(Theme.accent.opacity(0.12), in: .capsule)
                }
                .buttonStyle(.borderless)
            }
            Button(action: send) {
                HStack(spacing: Theme.Space.xs) {
                    if sending { ProgressView().controlSize(.mini).tint(.white) } else { ThemedIcon(Theme.Icon.sendMail).font(Theme.Font.label) }
                    Text("Send")
                }
                .font(Theme.Font.secondaryStrong).lineLimit(1)
                .padding(.horizontal, Theme.Space.l).padding(.vertical, Theme.Space.sm)
                .foregroundStyle(.white)
                .background(Theme.accent, in: .capsule)
            }
            .buttonStyle(.borderless)
            .disabled(sending)
            .accessibilityLabel("Send the AI twin's draft reply")
        }
    }
}

/// The draft's card: the accent's tint with a hairline; in the BeautifulUI style the field gray (RecommendationCard).
struct DraftSurface: ViewModifier {
    func body(content: Content) -> some View {
        if AppStyle.current.usesBeautifulUI {
            content.background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        } else {
            content
                .background(Theme.accent.opacity(0.07), in: .rect(cornerRadius: Theme.Radius.card))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).stroke(Theme.accent.opacity(0.18), lineWidth: 0.5))
        }
    }
}

/// An agent waiting on the user, among the inbox rows: which agent, what it asks, then what it waits for (red, as
/// Needs You is everywhere) and on which machine. The label sits under the title so the title keeps the width.
struct InboxAgentRow: View {
    var interaction: Interaction
    var machine: Machine?
    var machineName: String

    var body: some View {
        let ix = interaction
        // What it asks, by card kind (the plan's title, the terminal dialog's question); older hubs that filled
        // neither keep the old summary / first-question fallback.
        let asks = ix.askLine(includeToolName: false) ?? ix.summary ?? ix.questions?.first?.question
        HStack(alignment: .top, spacing: Theme.Space.m) {
            // The rounded square of the Agents rows (SessionAvatar), with what kind of request it is.
            SessionAvatar(machine: machine, state: .needsYou, size: 40, symbol: ix.kind.symbol, dot: false)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.sm) {
                    Text(ix.sessionTitle?.isEmpty == false ? ix.sessionTitle! : (ix.cwd.map { URL(fileURLWithPath: $0).lastPathComponent } ?? "Agent"))
                        .font(Theme.Font.heading).lineLimit(1)
                    Spacer(minLength: 4)
                    Text(LarkTime.short(ix.createdAt)).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize()
                }
                if let asks, !asks.isEmpty {
                    Text(asks).font(Theme.Font.secondary).foregroundStyle(.primary.opacity(0.9)).lineLimit(2) // see InboxRow
                    (Text(ix.title).fontWeight(.semibold).foregroundStyle(StatusPalette.needsYou.text)
                        + Text(" · " + machineName).foregroundStyle(.secondary))
                        .font(Theme.Font.caption).lineLimit(1)
                } else {
                    Text(ix.title).font(Theme.Font.secondaryStrong).foregroundStyle(StatusPalette.needsYou.text).lineLimit(1)
                    Text(machineName).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
        }
        .padding(.vertical, Theme.Space.m)
        .contentShape(.rect)
    }
}

// MARK: - Detail

/// One inbox item opened: the Feishu chat at its first new message, or the mail, or what a plugin draws for its source
/// (placement inbox_item, `InboxItemSlotContent`); the AI's summary with AI Draft Reply / Hand to AI on top, Done in the
/// toolbar, whoever draws the body.
struct InboxDetailView: View {
    var item: InboxItem
    var accounts: [MailAccount]
    @Environment(\.dismiss) private var dismiss
    @State private var drafting = false
    @State private var handingOff = false
    @State private var ruling: InboxPolicyTarget?
    @State private var slot: InboxItemSlotModel

    init(item: InboxItem, accounts: [MailAccount]) {
        self.item = item
        self.accounts = accounts
        _slot = State(initialValue: InboxItemSlotModel(item: item))
    }

    private var current: InboxItem { InboxStore.shared.items.first { $0.id == item.id } ?? item }

    var body: some View {
        page
            .sheet(isPresented: $drafting) {
                InboxDraftSheet(item: current, accounts: accounts, onSent: { dismiss() })
            }
            .sheet(isPresented: $handingOff) { InboxHandOffSheet(item: current) }
            .sheet(item: $ruling) { InboxPolicySheet(target: $0) }
            #if DEBUG
            // `-showInboxAction draft|handoff`: open that sheet (screenshots).
            .task {
                switch UserDefaults.standard.string(forKey: "showInboxAction") {
                case "draft": drafting = true
                case "handoff": handingOff = true
                default: break
                }
            }
            #endif
            .pageToolbar {
                if let mutes = current.mutes, !mutes.isEmpty {
                    ToolbarItem(placement: .topBarTrailing) {
                        Menu { InboxRuleButtons(item: current) { ruling = $0 } } label: { ThemedIcon(Theme.Icon.moreMenu) }
                            .accessibilityLabel("More")
                            .accessibilityIdentifier("inbox.detail.more")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    if current.isDone {
                        Button { InboxStore.shared.setDone(current, false) } label: { Label("Move Back", systemImage: Theme.Icon.undo) }
                    } else {
                        Button {
                            InboxStore.shared.setDone(current, true)
                            dismiss()
                        } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done) }
                    }
                }
            }
    }

    /// An event (inbox phase 1: recap, a suggestion, the chain) opens as its own page, the source behind 'view original';
    /// anything else as before: the source with the actions over it.
    @ViewBuilder private var page: some View {
        if current.isEvent {
            InboxEventPage(item: current, accounts: accounts, draft: { drafting = true })
                .navigationTitle(current.title)
                .navigationBarTitleDisplayMode(.inline)
        } else {
            InboxItemSlotContent(item: current, slot: slot) { content }
                .safeAreaInset(edge: .top, spacing: 0) {
                    InboxActionBar(item: current, draft: { drafting = true }, handOff: { handingOff = true })
                }
        }
    }

    @ViewBuilder private var content: some View {
        if current.doc != nil {
            InboxDocDetailView(item: current)
        } else if current.connector != nil {
            InboxConnectorDetailView(item: current)
        } else if let m = current.moment {
            LarkMomentsPostView(postID: m.postID, pushed: true) // the post and its comments, as a shared Moments card opens
        } else if let c = item.larkConversation {
            LarkChatView(conversation: c)
        } else if let m = item.mailSummary {
            MailDetailView(summary: m, accounts: accounts)
        } else {
            EmptyStateView(symbol: "questionmark", title: item.title, message: item.preview)
        }
    }
}

/// The pages the inbox opens, in either layout (`SplitNavigation`).
struct InboxDestinations: ViewModifier {
    var accounts: [MailAccount]
    @Environment(\.feishuOpen) private var feishuOpen

    // Links on these pages open in the app (feishuHandlesLinks) wherever the stack is: the shell pushes them onto its
    // own stack, where the inbox page's handler does not reach.
    func body(content: Content) -> some View {
        content
            // Keyed by the item, as on the Mac: in the split the list's links replace the detail column's page, and an
            // unkeyed one is updated in place with the last item's state — a Feishu chat kept the chat before's messages
            // and draft under the new chat's title (#885).
            .navigationDestination(for: InboxItem.self) {
                InboxDetailView(item: $0, accounts: accounts).id($0.id).splitDetailPage().feishuHandlesLinks(feishuOpen)
            }
            .navigationDestination(for: FeishuRoute.self) { FeishuRouteView(route: $0).splitDetailPage().feishuHandlesLinks(feishuOpen) }
            // view original and the chain's links of an event (inbox phase 1).
            .navigationDestination(for: InboxSourceRoute.self) {
                InboxSourcePage(route: $0, accounts: accounts).splitDetailPage().feishuHandlesLinks(feishuOpen)
            }
            .extensionPushDestination()
    }
}

/// An inbox row on the list: in the BeautifulUI style a card of its own on the page (its surface), like the Agents
/// list's capsules, no list background or line; otherwise the plain row with its line from the text's column. The
/// swipe actions stay the list row's either way.
private struct InboxRowLook: ViewModifier {
    func body(content: Content) -> some View {
        if AppStyle.current.usesBeautifulUI {
            content
                .padding(.horizontal, Theme.Space.m)
                .card(padding: 0)
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                .listRowInsets(EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.l, bottom: Theme.Space.xs, trailing: Theme.Space.l))
        } else {
            content
                .listRowInsets(EdgeInsets(top: 0, leading: 16, bottom: 0, trailing: 12))
                .themedRow()
                .alignmentGuide(.listRowSeparatorLeading) { _ in InboxRow.textColumn }
        }
    }
}
