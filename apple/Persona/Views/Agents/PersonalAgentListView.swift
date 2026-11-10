import SwiftUI

/// Opens the personal agents page from the Agents tab.
struct PersonalAgentsRoute: Hashable {}

/// The Lark gateway's personal agents (grok_bot, muse, …) with a view menu like the Lark gateway's (Group By / Sort By / Display /
/// New Group / Reset to Default / View All), search, and in a row's long-press menu pins, custom sections and Remove from
/// List, with a folded Removed (N) section at the end (#138). Prefs are shared with the web through the hub. Re-pulls on the hub's
/// `agents` events.
struct PersonalAgentListView: View {
    @Environment(AppModel.self) private var model
    @State private var list = PersonalAgentListModel()
    @State private var paginationArmed = false
    private var sessions: [PersonalAgentSession] { (list.page?.rows ?? []).filter { !list.going.contains($0.id) } }
    @State private var sources: [PersonalAgentSourceStatus] = []
    @State private var error: String?
    private var loaded: Bool { list.page?.loaded == true }
    private var shownError: String? { list.page?.error ?? error }
    @State private var query = ""
    @State private var naming = false
    @State private var newSection = ""
    @State private var showRemoved = false
    private var store: PersonalAgentPrefsStore { .shared }

    private var prefs: PersonalAgentPrefs { store.prefs }
    private var listQuery: SessionListQuery {
        SessionListQuery(legacy: AgentListLoadingPolicy.personal(prefs, query: query),
                         state: prefs.view.showMode == .unread ? "unread" : "",
                         provider: prefs.view.providerFilter.count == 1 ? prefs.view.providerFilter[0] : "", text: query)
    }
    private var groups: [PersonalAgentGroup] { PersonalAgentArrange.arrange(sessions, prefs: prefs, query: list.page?.paged == true ? "" : query) }
    private var allProviders: [String] {
        var seen: [String] = []
        for s in sources.flatMap({ ($0.providers ?? []).map(\.provider) + ($0.accounts ?? []).map(\.provider) }) where !seen.contains(s) { seen.append(s) }
        for s in sessions where !seen.contains(s.provider) { seen.append(s.provider) }
        return seen
    }

    private func update(_ change: (inout PersonalAgentPrefs) -> Void) { store.update(model.api, change) }

    var body: some View {
        List {
            if let error = shownError {
                Section { Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger) }
            }
            ForEach(sources.flatMap { $0.accounts ?? [] }, id: \.accountId) { a in
                if let e = a.lastError, !e.isEmpty {
                    Section {
                        Label("\(a.displayName ?? a.provider): \(e)", systemImage: Theme.Icon.error)
                            .font(Theme.Font.caption).foregroundStyle(Theme.cautionText)
                    }
                }
            }
            ForEach(groups) { g in
                Section {
                    ForEach(g.sessions) { s in
                        // No swipe actions (the owner asked, #138): pin, groups and Remove from List are in the long-press menu.
                        NavigationLink(value: s) { PersonalAgentRow(session: s, pinned: prefs.isPinned(s.id)) }
                            .contextMenu { rowMenu(s) }
                    }
                } header: {
                    if !g.label.isEmpty {
                        HStack(spacing: Theme.Space.xs) {
                            if g.id == "pinned" { ThemedIcon(Theme.Icon.pinned).imageScale(.small) }
                            Text(g.label)
                            Spacer()
                            if list.page?.paged != true, g.unread > 0 { Text("\(g.unread)").monospacedDigit() }
                        }
                    }
                }
            }
            if list.page?.hasMore == true {
                ProgressView()
                    .frame(maxWidth: .infinity)
                    .accessibilityIdentifier("personalAgents.nextPage")
                    .task(id: paginationArmed) { if paginationArmed { await list.page?.next() } }
            }
            if loaded && sessions.isEmpty && error == nil {
                let hints = sources.compactMap { $0.setupHint ?? $0.error }
                if hints.isEmpty {
                    Text("No personal agent sessions").foregroundStyle(.secondary)
                } else {
                    ForEach(hints, id: \.self) { hint in
                        Label(hint, systemImage: Theme.Icon.info).foregroundStyle(.secondary)
                    }
                }
            } else if loaded && groups.isEmpty && error == nil {
                Text("No matching sessions").foregroundStyle(.secondary)
            }
            if !list.removed.isEmpty { removedSection }
        }
        .navigationTitle("Personal Agents")
        .pageSearchable(text: $query, prompt: String(localized: "Search Chats [Personal Agent List View feb955]"))
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) { viewMenu }
        }
        .alert("New Group", isPresented: $naming) {
            TextField(String(localized: "Group name"), text: $newSection)
            Button("Cancel", role: .cancel) { newSection = "" }
            Button("Create") {
                let n = newSection
                update { $0.addSection(n) }
                newSection = ""
            }
        } message: { Text("Long-press a session to move it into this group.") }
        .overlay { if !loaded && error == nil { ProgressView() } }
        .refreshable { await list.page?.reload(); await load() }
        .task(id: listQuery) {
            paginationArmed = false
            if !query.isEmpty { try? await Task.sleep(for: .milliseconds(250)) }
            guard !Task.isCancelled else { return }
            await load()
        }
        .onChange(of: model.api?.config) { _, _ in Task { await load() } }
        .onChange(of: model.api?.group) { _, _ in Task { await load() } }
        .onScrollPhaseChange { _, phase in if phase != .idle { paginationArmed = true } }
        .task {
            for await _ in PersonalAgentSync.shared.changes() {
                await load() // prefs/source status and changed credentials follow the hub; an unchanged row baseline is reused
                // a delta only updates rows already shown, so an empty page (a source that started answering) reads afresh
                if list.page?.rows.isEmpty == true { await list.page?.reload() } else { list.page?.invalidate() }
            }
        }
    }

    private var viewMenu: some View {
        Menu {
            Picker(selection: Binding(get: { prefs.view.groupBy }, set: { g in update { $0.view.group = g.rawValue } })) {
                ForEach(AgentGroupBy.allCases, id: \.self) { Text($0.label).tag($0) }
            } label: { Label("Group By [Personal Agent List View 72148c]", systemImage: Theme.Icon.groupBy) }
                .pickerStyle(.menu)
            Picker(selection: Binding(get: { prefs.view.sortBy }, set: { v in update { $0.view.sort = v.rawValue } })) {
                ForEach(AgentSortBy.allCases, id: \.self) { Text($0.label).tag($0) }
            } label: { Label("Sort By", systemImage: Theme.Icon.upDown) }
                .pickerStyle(.menu)
            Menu {
                Picker("Display", selection: Binding(get: { prefs.view.showMode }, set: { v in update { $0.view.show = v.rawValue } })) {
                    ForEach(AgentShow.allCases, id: \.self) { Text($0.label).tag($0) }
                }
                .pickerStyle(.inline)
                if allProviders.count > 1 {
                    Section("Source") {
                        ForEach(allProviders, id: \.self) { p in
                            Toggle(PersonalAgentArrange.providerLabel(p), isOn: Binding(
                                get: { prefs.view.providerFilter.isEmpty || prefs.view.providerFilter.contains(p) },
                                set: { _ in let all = allProviders; update { $0.toggleProvider(p, all: all) } }))
                        }
                    }
                }
            } label: { Label("Display", systemImage: Theme.Icon.view) }
            Divider()
            Button { naming = true } label: { Label("New Group", systemImage: Theme.Icon.newGroup) }
            if !prefs.sections.isEmpty {
                Menu {
                    ForEach(prefs.sections) { sec in
                        Button(role: .destructive) { update { $0.removeSection(sec.id) } } label: { Text(sec.name) }
                    }
                } label: { Label("Delete Group", systemImage: Theme.Icon.deleteGroup) }
            }
            Divider()
            Button { update { $0.resetView() } } label: { Label("Reset to Default", systemImage: Theme.Icon.resetDefaults) }
                .disabled(prefs.view.isDefault)
            Button {
                query = ""
                update { $0.view.show = nil; $0.view.providers = nil }
            } label: { Label("View All", systemImage: Theme.Icon.listView) }
        } label: {
            ThemedIcon(prefs.view.isDefault ? Theme.Icon.filter : Theme.Icon.filterActive)
        }
        .accessibilityLabel("Filter and sort")
    }

    /// Removed (N), folded: what Remove from List took out, each with Restore. A new message brings one back by itself.
    private var removedSection: some View {
        Section {
            DisclosureGroup(isExpanded: $showRemoved) {
                ForEach(list.removed) { s in
                    HStack(spacing: Theme.Space.m) {
                        PersonalAgentRow(session: s)
                        Button { restore(s) } label: { Text("Restore").lineLimit(1) }
                            .themedButton()
                            .accessibilityIdentifier("personalAgents.restore")
                    }
                }
            } label: {
                // On the label: on the group it would name every row inside too.
                Label("Removed (\(list.removed.count))", systemImage: Theme.Icon.hide).lineLimit(1)
                    .accessibilityIdentifier("personalAgents.removed")
            }
        } footer: {
            if showRemoved { Text("Removed chats stay with their source and come back on their own when a new message arrives.") }
        }
    }

    @ViewBuilder private func rowMenu(_ s: PersonalAgentSession) -> some View {
        Button { update { $0.togglePin(s.id) } } label: {
            prefs.isPinned(s.id) ? Label("Unpin [Personal Agent List View c92179]", systemImage: Theme.Icon.unpin) : Label("Pin [Personal Agent List View 173f88]", systemImage: Theme.Icon.pin)
        }
        if !prefs.sections.isEmpty {
            Menu {
                ForEach(prefs.sections) { sec in
                    Button { update { $0.assign(s.id, to: prefs.assign[s.id] == sec.id ? nil : sec.id) } } label: {
                        if prefs.assign[s.id] == sec.id { Label(sec.name, systemImage: Theme.Icon.done) } else { Text(sec.name) }
                    }
                }
                if prefs.assign[s.id] != nil {
                    Button("Remove from Group") { update { $0.assign(s.id, to: nil) } }
                }
            } label: { Label("Move to Group", systemImage: Theme.Icon.folder) }
        }
        Divider()
        Button(role: .destructive) { remove(s) } label: { Label("Remove from List", systemImage: Theme.Icon.hide) }
    }

    private static func title(_ s: PersonalAgentSession) -> String { s.title.isEmpty ? s.sessionKey : s.title }

    /// The row goes at once; the toast offers Undo; a refusal brings it back and says why.
    private func remove(_ s: PersonalAgentSession) {
        guard let api = model.api else { return }
        Task {
            do {
                try await list.remove(s, api: api)
                model.toast = Toast(String(localized: "Removed “\(Self.title(s))”. It comes back when it gets a new message."), style: .success,
                                    action: Toast.Action(title: String(localized: "Undo")) { restore(s) })
            } catch {
                model.toast = Toast(String(localized: "Couldn't remove from the list: \(error.localizedDescription)"), style: .error)
            }
        }
    }

    private func restore(_ s: PersonalAgentSession) {
        guard let api = model.api else { return }
        Task {
            do {
                try await list.restore(s, api: api)
                model.toast = Toast(String(localized: "Restored “\(Self.title(s))”"), style: .success)
            } catch {
                model.toast = Toast(String(localized: "Couldn't restore: \(error.localizedDescription)"), style: .error)
            }
        }
    }

    private func load() async {
        guard let api = model.api else { return }
        async let p: Void = store.load(api)
        do {
            list.connect(api)
            async let st = api.personalAgentSources()
            await list.page?.select(listQuery)
            if let failure = list.page?.error { throw APIError(status: 0, message: failure) }
            sources = (try? await st) ?? sources
            error = nil
        } catch {
            // Say it plainly instead of showing stale rows as if they were current (§6 step 1).
            self.error = String(localized: "Can't connect to personal agents: \(error.localizedDescription)")
        }
        await list.loadRemoved(api)
        _ = await p
    }
}

private struct PersonalAgentRow: View {
    let session: PersonalAgentSession
    var pinned = false

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            if pinned { ThemedIcon(Theme.Icon.pinned).imageScale(.small).foregroundStyle(.orange).accessibilityLabel("Pinned [Personal Agent List View fb47db]") }
            PersonalAgentAvatar(session: session, size: 32)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(session.title.isEmpty ? session.sessionKey : session.title).lineLimit(1)
                Text(session.rankTime, format: .relative(presentation: .named))
                    .font(Theme.Font.label).foregroundStyle(.secondary)
            }
            .padding(.leading, session.parentSessionKey == nil ? 0 : Theme.Space.l)
            Spacer(minLength: Theme.Space.s)
            if !session.canSend {
                ThemedIcon(Theme.Icon.locked).foregroundStyle(.secondary)
            }
            if session.unreadCount > 0 {
                CountBadge(count: session.unreadCount, look: .tone(StatusPalette.unread))
            }
        }
    }
}
