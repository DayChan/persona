import SwiftUI

// The Mac inbox (docs/assistant/inbox.md; docs/apple/mac.md「主窗口」): the sidebar's inbox entries (`MacInboxFilters`), the list column
// (`MacInboxList`) and the detail column (`MacInboxDetail`). The list is the iPhone's: the same rows, sections Needs You /
// FYI / Collapsed / Done, the AI Twin's draft cards. Mac ways on top: system selection (⇧ / ⌘ for several), ↑↓ to move,
// E or ⌫ files the selected as Done and selects the next, ⏎ or a double click opens it in a window of its own (an
// agent row: its chat), a right-click menu with everything the row can do, swipe left on the trackpad for Done.

/// The inbox's filters column: sources (All, Lark, each mail account, each connector with items) and kinds (@Me, DMs, Group chat, Mail, Agent), each with
/// what still waits in it; picking one sets the list's filter. The shell's rows (`MacSidebarRow`), not a sidebar
/// `List`: its words went light gray whenever the window was not the active one (2026-11-02 user: hard to read).
struct MacInboxFilters: View {
    @Environment(AppModel.self) private var model
    var state = MacInboxState.shared

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                MacSidebarHeading(title: String(localized: "Source")) // the page's title says Inbox above it (#830)
                ForEach(state.sources) { row($0) }
                MacSidebarHeading(title: String(localized: "Type"))
                ForEach(state.kinds) { row($0) }
            }
            .padding(.horizontal, Theme.Space.s)
            .padding(.bottom, Theme.Space.s)
        }
    }

    private func row(_ f: MacInboxFilter) -> some View {
        let n = state.count(f, model: model)
        return MacSidebarRow(icon: f.icon, title: f.title, selected: state.filter == f, badge: n > 0 ? .word("\(n)") : nil,
                             identifier: "mac.inbox.filter.\(f.id)") { state.filter = f }
            .help(f.title)
    }
}

/// The list column.
struct MacInboxList: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openWindow) private var ownOpenWindow
    @Environment(\.shellOpenWindow) private var shellOpenWindow
    /// The window scene's: the page is in a hosting view of its own (`MacPageStack`), whose own one opens nothing.
    private var openWindow: OpenWindowAction { shellOpenWindow ?? ownOpenWindow }
    @Bindable var state = MacInboxState.shared
    private var store: InboxStore { .shared }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("MacInboxList")
        #endif
        let sections = state.sections(model)
        List(selection: $state.selection) {
            // New items the hub holds back until the model sorts them (#1773): the column's first row, not selectable.
            if let line = InboxTriageText.line(store.triage) {
                InboxTriageBanner(line: line, configurable: InboxTriageText.configurable(store.triage, mayChangeSettings: !model.isDemo))
                    .listRowSeparator(.hidden)
                    .padding(.bottom, Theme.Space.xs)
            }
            if let error = store.error, store.items.isEmpty {
                Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
            }
            ForEach(sections.tiers, id: \.tier) { t in
                Section {
                    if state.expanded.contains(t.tier.rawValue) || !t.tier.folds {
                        ForEach(t.agents) { agentRow($0) }
                        ForEach(t.items) { row($0) }
                    }
                } header: {
                    header(t.tier.title, count: t.items.count + t.agents.count, key: t.tier.rawValue,
                           clear: InboxPage.clearTitle(t.tier).map { ($0, { store.setDone(t.items, true) }) })
                }
            }
            if !sections.done.isEmpty {
                Section {
                    if state.expanded.contains("done") { ForEach(sections.done) { row($0) } }
                } header: {
                    header(String(localized: "Done [Inbox View 59f6c8]"), count: sections.done.count, key: "done", clear: nil)
                }
            }
        }
        .listStyle(.inset)
        .alternatingRowBackgrounds(.disabled)
        .scrollContentBackground(.hidden) // the page's color (MacInboxPage)
        .overlay {
            if sections.isEmpty && !store.loading {
                EmptyStateView(symbol: Theme.Icon.success, title: state.query.isEmpty ? String(localized: "All Caught Up") : String(localized: "No Matches"),
                               message: state.query.isEmpty ? String(localized: "New Lark messages and emails show up here.") : String(localized: "Try a different search."))
            } else if store.loading && store.items.isEmpty {
                ProgressView()
            }
        }
        .contextMenu(forSelectionType: MacInboxSelection.self) { ids in
            menu(ids)
        } primaryAction: { ids in
            openAll(ids)
        }
        .onKeyPress(characters: ["e", "E"], phases: .down) { _ in
            // Agent requests cannot be filed: an agents-only selection lets E fall through instead of swallowing it
            // and doing nothing (#3479).
            guard state.multiSelection.canMarkDone else { return .ignored }
            state.toggleDone(state.selection, model: model)
            return .handled
        }
        .onDeleteCommand { state.toggleDone(state.selection, model: model) }
        // A selected row can expire out of the list (the hub stops returning an item, an agent stops waiting): prune
        // the selection on refresh so the detail's Selected count and the multi-select action match what is still there.
        .onChange(of: store.items) { _, items in
            state.pruneStale(items: items, interactions: model.pendingInteractions)
        }
        .onChange(of: model.pendingInteractions) { _, interactions in
            state.pruneStale(items: store.items, interactions: interactions)
        }
        .pageSearchable(text: $state.query, placement: .toolbar, prompt: String(localized: "Search Titles, Senders, and Content"))
        .pageToolbar { toolbar }
        .sheet(isPresented: $state.showAccounts, onDismiss: { Task { await state.loadAccounts(model) } }) {
            MailAccountsView().macSheet()
        }
        .sheet(item: $state.composing) { d in ComposeView(draft: d, accounts: state.accounts).macSheet() }
        .confirmationDialog(InboxBulk.confirmTitle, isPresented: $state.confirming) {
            Button(InboxBulk.confirmButton(count: state.openInView.count)) { state.clean(confirmed: true) }
                .accessibilityIdentifier("mac.inbox.cleanConfirm")
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(InboxBulk.confirmMessage(count: state.openInView.count, filters: state.scopeFilters, query: state.query))
        }
        .sheet(item: $state.editingDraft) { InboxDraftSheet(item: $0, accounts: state.accounts).macSheet() }
        .sheet(isPresented: $state.showRules) { InboxRulesView() }
        .sheet(item: $state.ruling) { InboxPolicySheet(target: $0) }
        // A plugin enabled or set up since: its sources' items open as it draws them.
        .task { await InboxItemSlots.shared.reload() }
        .task {
            store.reload()
            await state.loadAccounts(model)
            #if DEBUG
            // `-openInboxFirst YES`: select the first open item; `-openInboxMatch <text>`: the first whose title has the
            // text (detail screenshots); `-addMail YES`: Manage Accounts; `-showCleanUpConfirm YES`: the clean-up question.
            if UserDefaults.standard.bool(forKey: "addMail") { state.showAccounts = true }
            // `-showInboxRules YES`: Rules. `-openInboxRule chat|sender|repo`: the rule sheet of the first open item with
            // one, as its menu opens it; `edit`: Rules, then Edit on the newest rule.
            if UserDefaults.standard.bool(forKey: "showInboxRules") || UserDefaults.standard.string(forKey: "openInboxRule") == "edit" {
                state.showRules = true
            } else if let tag = UserDefaults.standard.string(forKey: "openInboxRule") {
                for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                for it in store.open {
                    if let m = it.mutes?.first(where: { $0.tag == tag }) { state.ruling = InboxPolicyTarget(item: it, option: m); break }
                }
            }
            if UserDefaults.standard.bool(forKey: "showCleanUpConfirm") {
                for _ in 0..<20 where state.openInView.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                if !state.openInView.isEmpty { state.confirming = true }
            }
            let match = UserDefaults.standard.string(forKey: "openInboxMatch")
            if UserDefaults.standard.bool(forKey: "openInboxFirst") || match != nil {
                for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                if let first = store.items.first(where: { match == nil ? !$0.isDone : $0.title.contains(match!) }) {
                    state.selection = [.item(first.id)]
                }
            }
            #endif
        }
    }

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        MacToolbarMiddle() // at the narrowest window these buttons covered Inbox (#830)
        ToolbarItemGroup(placement: .primaryAction) {
            // ⌘R is View › Refresh's, which refreshes the inbox while it is on screen.
            Button { Task { await store.refresh() } } label: { Label("Refresh", systemImage: Theme.Icon.retry) }
                .help("Refresh (⌘R)")
            // ⇧⌘E is the Inbox menu's (MacInboxMenu), which holds the same action.
            Button { state.clean() } label: {
                Label("Clean up all", systemImage: Theme.Icon.cleanUpAll)
            }
            .disabled(state.openInView.isEmpty)
            .help(String(localized: "Clean up all: \(state.cleanUpScope) (⇧⌘E)"))
            .accessibilityIdentifier("mac.inbox.cleanUpAll")
            Button { state.composing = ComposeDraft(accountID: state.accounts.first?.id ?? "") } label: {
                Label("New Email", systemImage: Theme.Icon.compose)
            }
            .disabled(state.accounts.isEmpty)
            .keyboardShortcut("n", modifiers: [.command, .option])
            .help(state.accounts.isEmpty ? String(localized: "Add an email account in Manage Accounts first") : String(localized: "New email (⌥⌘N)"))
            Button { state.showAccounts = true } label: { Label("Manage Accounts", systemImage: Theme.Icon.mailAccounts) }
                .help("Manage Accounts")
            Button { state.showRules = true } label: { Label("Rules [Inbox Policy]", systemImage: Theme.Icon.inboxRule) }
                .help("Rules [Inbox Policy]")
                .accessibilityIdentifier("mac.inbox.rules")
        }
    }

    /// A section title that opens / closes the section, with its count, and an optional action on the right.
    private func header(_ title: String, count: Int, key: String, clear: (String, () -> Void)?) -> some View {
        HStack(spacing: Theme.Space.sm) {
            if InboxItem.Tier(rawValue: key)?.folds == false {
                // In Progress: always open, a plain title (inbox phase 1).
                HStack(spacing: Theme.Space.xs) {
                    Text(title).foregroundStyle(.secondary)
                    Text("\(count)").foregroundStyle(.secondary).monospacedDigit()
                    Spacer(minLength: 0)
                }
                .font(Theme.Font.sectionHeader)
                .lineLimit(1)
            } else {
            Button {
                withAnimation(Theme.Motion.standard) { state.toggle(key) }
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    Image(systemName: Theme.Icon.disclosure).font(Theme.Font.labelHeavy).foregroundStyle(.secondary)
                        .rotationEffect(.degrees(state.expanded.contains(key) ? 90 : 0))
                    Text(title).foregroundStyle(key == "action" ? Color.primary : .secondary)
                    Text("\(count)").foregroundStyle(.secondary).monospacedDigit()
                    Spacer(minLength: 0)
                }
                .font(Theme.Font.sectionHeader)
                .lineLimit(1)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .help(state.expanded.contains(key) ? String(localized: "Collapse") : String(localized: "Expand"))
            }
            if let clear {
                Button(clear.0) { withAnimation(Theme.Motion.standard) { clear.1() } }
                    .font(Theme.Font.secondary)
                    .lineLimit(1)
                    .buttonStyle(.link)
            }
        }
        .textCase(nil)
        // AppKit's floating group banner adds its own fill and full-width separator.
        // Keep the section headers inline, on the same page color as the rows.
        .background(MacInlineListHeaders())
    }

    private func agentRow(_ ix: Interaction) -> some View {
        MacInboxAgentRow(interaction: ix)
            .macOptionClick { MacShellState.shared.chatWindow = ix.sessionKey } // ⌥-click: its conversation's window
            .tag(MacInboxSelection.agent(ix.id))
            .macHoverRow(.agent(ix.id))
    }

    private func row(_ it: InboxItem) -> some View {
        InboxRow(item: it, showAccount: state.accounts.count > 1, sending: store.sending.contains(it.id),
                 send: { text in Task { await store.send(it, text: text, changes: it.agent?.changes ?? []) } },
                 edit: { state.editingDraft = it })
            .macOptionClick { openWindow(id: MacInboxItemWindow.id, value: it.id) } // ⌥-click: as ⏎ / a double click
            .tag(MacInboxSelection.item(it.id))
            .macHoverRow(.item(it.id))
            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                if it.isDone {
                    Button { store.setDone(it, false) } label: { Label("Move Back", systemImage: Theme.Icon.undo) }.tint(Theme.accent)
                } else {
                    Button { state.toggleDone([.item(it.id)], model: model) } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done) }.tint(Theme.positive)
                }
            }
    }

    // MARK: Menu

    @ViewBuilder private func menu(_ ids: Set<MacInboxSelection>) -> some View {
        let items = ids.compactMap { s -> InboxItem? in if case .item(let id) = s { return state.item(id) } else { return nil } }
        let agents = ids.compactMap { s -> Interaction? in
            if case .agent(let id) = s { return model.pendingInteractions.first { $0.id == id } } else { return nil }
        }
        if items.count == 1, let it = items.first {
            Button { openWindow(id: MacInboxItemWindow.id, value: it.id) } label: { Label("Open in New Window", systemImage: Theme.Icon.newWindow) }
            Divider()
            if !it.isDone {
                Button { state.drafting = it; state.selection = [.item(it.id)] } label: {
                    Label(it.agentDraft == nil ? String(localized: "AI Draft Reply") : String(localized: "View Draft"), systemImage: Theme.Icon.draftReply)
                }
                Button { state.handingOff = it; state.selection = [.item(it.id)] } label: { Label("Hand to AI", systemImage: Theme.Icon.handToAI) }
                Divider()
            }
        }
        if !items.isEmpty {
            let allDone = items.allSatisfy(\.isDone)
            Button { state.toggleDone(Set(items.map { .item($0.id) }), model: model) } label: {
                Label(allDone ? String(localized: "Move Back to Inbox") : items.count > 1 ? String(localized: "Mark All \(items.count) as Done") : String(localized: "Done [Inbox View 59f6c8]"),
                      systemImage: allDone ? Theme.Icon.undo : Theme.Icon.done)
            }
        }
        if items.count == 1, let it = items.first {
            Divider()
            Button { copy(it.title + "\n" + (it.summary ?? it.preview)) } label: { Label("Copy Contents", systemImage: Theme.Icon.copy) }
            if let url = it.doc.map { $0.link ?? $0.url }, let u = URL(string: url) {
                Button { NSWorkspace.shared.open(u) } label: { Label("Open in Lark [Feishu Doc Reader 76d0eb]", systemImage: Theme.Icon.openExternal) }
            }
            if let mutes = it.mutes, !mutes.isEmpty {
                Divider()
                InboxRuleButtons(item: it) { state.ruling = $0 }
            }
        }
        if agents.count == 1, let ix = agents.first {
            Button { state.open(ix, model: model) } label: { Label("Open Chat", systemImage: Theme.Icon.conversation) }
        }
    }

    private func openAll(_ ids: Set<MacInboxSelection>) {
        for id in ids {
            switch id {
            case .item(let i): openWindow(id: MacInboxItemWindow.id, value: i)
            case .agent(let a):
                if let ix = model.pendingInteractions.first(where: { $0.id == a }) { state.open(ix, model: model) }
            }
        }
    }

    private func copy(_ s: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(s, forType: .string)
        model.toast = Toast(String(localized: "Copied [Mac Hover 07d6bb]"), style: .success)
    }
}

extension View {
    /// The iOS sheets (forms) in a Mac sheet: a size to start from, as Mail's compose sheet has.
    /// Forms in them are grouped (System Settings' look): the Mac's default two-column form put the iPhone's section
    /// rows (presets, pickers) in the label column and cut them off.
    func macSheet() -> some View {
        formStyle(.grouped)
            .frame(minWidth: Theme.Size.macSheetMinWidth, minHeight: Theme.Size.macSheetMinHeight)
    }

    /// A list row that reacts to the pointer (a light fill under it), as Mac lists with row actions do, and shows it is
    /// selected with the shell's gray well.
    func macHoverRow(_ id: MacInboxSelection) -> some View { modifier(MacHoverRow(id: id)) }
}

/// An Agent's request in the list, with its machine (`namedMachine`): the row is not drawn again by each heartbeat.
private struct MacInboxAgentRow: View {
    @Environment(AppModel.self) private var model
    let interaction: Interaction

    var body: some View {
        let machine = model.namedMachine(interaction.machineRef)
        InboxAgentRow(interaction: interaction, machine: machine, machineName: machine?.name ?? interaction.machineID)
    }
}

/// The system's selection stays (↑↓, ⇧ / ⌘, E and ⌫ go by it) but is not what shows: in the active window it filled the
/// row with the accent, and what the row draws in its own colors went under it (the AI Twin's draft header, its Edit
/// button, the sparkles: 2026-11-02). The page's color over it, the well on that, and the row's words in their usual
/// colors (selected, the system turned them white).
private struct MacHoverRow: ViewModifier {
    var id: MacInboxSelection
    @State private var hovering = false

    func body(content: Content) -> some View {
        // Read here, not by the list: there every ↑ / ↓ grouped the whole inbox again.
        let selected = MacInboxState.shared.selection.contains(id)
        content
            // The label color as such: the system's selected-row white comes with the hierarchical styles.
            .foregroundStyle(Theme.macLabel)
            .environment(\.backgroundProminence, .standard)
            .padding(.horizontal, Theme.Space.xs)
            .background(selected ? Theme.Fill.macSelected : hovering ? Theme.Fill.macHover : Color.clear,
                        in: .rect(cornerRadius: Theme.Radius.control))
            .onHover { hovering = $0 }
            .listRowBackground(Theme.pageBackground)
    }
}
