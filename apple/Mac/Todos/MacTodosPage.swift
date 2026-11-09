import BeautifulUI
import SwiftUI

struct MacTodosPage: View {
    @Environment(AppModel.self) private var model
    @State private var store = TodoStore.shared
    @State private var selectedTodoID: String?
    @State private var query = ""
    @State private var showCreate = false
    @State private var creatingTitle = ""
    @State private var creatingNotes = ""
    @State private var expandedDone = false

    @AppStorage("mac.todos.listWidth") private var listWidth: Double = Theme.Size.macTodosListIdeal
    /// List or calendar, and the calendar's day / week / month (#2029): the Mac starts on the week.
    @StoredValue("todos.view") private var viewMode = TodosViewMode.list
    @StoredValue("todos.calendar.mode") private var calendarMode = TodoCalendarMode.week
    @State private var calendarAnchor = Date.now
    @State private var quickAdd: TodoCalendarSlot?
    /// The calendar shows the selected to-do beside it once one is opened from it, until closed.
    @State private var calendarDetail = false
    @State private var dragging: Double?
    @Environment(\.tabIsActive) private var inFront

    private var allTodos: [TodoItem] {
        let lookup = TodosLogic.makeSessionLookup(AppModel.shared.allSessions(inFront: inFront))
        return TodosLogic.sortTodos(store.todos, lookup: lookup)
    }

    private var openList: [TodoItem] {
        allTodos.filter { !$0.isDone && matchesQuery($0) }
    }

    private var doneList: [TodoItem] {
        allTodos.filter { $0.isDone && matchesQuery($0) }
    }

    /// Whether the query narrows the list. The field keeps the raw text verbatim; a whitespace-only
    /// query searches like an empty one (#3797).
    private var isSearching: Bool { Self.isSearching(query) }

    private func matchesQuery(_ todo: TodoItem) -> Bool {
        Self.matches(todo, query: query)
    }

    /// Whether the raw query filters the list: whitespace-only is not a search (#3797).
    static func isSearching(_ rawQuery: String) -> Bool {
        !rawQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// The filter shared by the open and completed sections: a trimmed-empty query matches every todo,
    /// and surrounding spaces never stop a match.
    static func matches(_ todo: TodoItem, query rawQuery: String) -> Bool {
        let q = rawQuery.trimmingCharacters(in: .whitespacesAndNewlines).localizedLowercase
        if q.isEmpty { return true }
        if todo.title.localizedLowercase.contains(q) { return true }
        if let notes = todo.notes, notes.localizedLowercase.contains(q) { return true }
        return false
    }

    /// Whether the done group is expanded for the current query: a non-empty query with done matches reveals the
    /// collapsed group; an empty query (same trim rule as `matchesQuery`) or a query without done matches leaves the
    /// current state untouched, so clearing the search never collapses a group the user opened (#3474).
    /// Pure (no AppKit/UIKit types), so nonisolated despite the target's default MainActor isolation: the unit tests
    /// call it from a synchronous nonisolated context (cf. `nonisolated enum MacPaste`).
    nonisolated enum SearchExpansion {
        static func doneExpanded(query: String, hasDoneMatches: Bool, current: Bool) -> Bool {
            if query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return current }
            return hasDoneMatches ? true : current
        }
    }

    var body: some View {
        Group {
            if viewMode == .calendar { calendarPage } else { listPage }
        }
        .background(Theme.pageBackground)
        .sheet(isPresented: $showCreate) {
            createSheet
        }
        .sheet(item: $quickAdd) { slot in
            TodoQuickAddSheet(slot: slot)
        }
        .task {
            await store.refresh()
            #if DEBUG
            // `-openMacTodo <text>`: select the first todo whose title has the text (detail screenshots).
            if let match = UserDefaults.standard.string(forKey: "openMacTodo"),
               let picked = allTodos.first(where: { $0.title.contains(match) }) {
                selectedTodoID = picked.id
            }
            #endif
            if selectedTodoID == nil {
                selectedTodoID = openList.first?.id ?? doneList.first?.id
            }
        }
        .onChange(of: store.todos) { _, todos in
            // The selected todo can vanish from any path: the row menu, the detail page's delete (its dismiss
            // runs on the page's own stack root and does nothing here), or a push delete from another device.
            // Without this the id keeps pointing at the gone todo and the detail shows its tombstone forever.
            if calendarDetail, let id = selectedTodoID, !todos.contains(where: { $0.id == id }) {
                calendarDetail = false
            }
            selectedTodoID = MacTodosSelection.resolve(
                selectedID: selectedTodoID,
                existing: todos,
                open: openList,
                done: doneList
            )
        }
    }

    /// The calendar (#2029), and the to-do opened from it beside it.
    private var calendarPage: some View {
        HStack(spacing: 0) {
            VStack(spacing: 0) {
                TodosModeBar(view: $viewMode, mode: $calendarMode)
                TodoCalendarView(todos: store.todos, mode: $calendarMode, anchor: $calendarAnchor,
                                 open: { id in
                                     selectedTodoID = id
                                     calendarDetail = true
                                 },
                                 showList: { viewMode = .list })
            }
            .overlay(alignment: .topTrailing) {
                MacTitleRowButton(String(localized: "New To-Do"), symbol: Theme.Icon.add) {
                    quickAdd = TodoCalendarSlot(day: calendarAnchor, time: nil)
                }
                .padding(.trailing, Theme.Space.l)
                .frame(height: Theme.Size.macToolbarHeight)
                .ignoresSafeArea(.container, edges: .top)
            }
            if calendarDetail, let id = selectedTodoID {
                Divider()
                NavigationStack {
                    TodoDetailView(todoID: id)
                        .id(id)
                }
                .frame(width: Theme.Size.sidebar)
                .overlay(alignment: .topLeading) {
                    Button { calendarDetail = false } label: { ThemedIcon(Theme.Icon.close) }
                        .buttonStyle(.plain)
                        .foregroundStyle(.secondary)
                        .padding(Theme.Space.s)
                        .accessibilityLabel(Text("Close"))
                }
            }
        }
    }

    private var listPage: some View {
        HStack(spacing: 0) {
            listColumn
                .frame(width: min(max(dragging ?? listWidth, Theme.Size.macTodosListMin), Theme.Size.macTodosListMax))
            MacPanelResizeHandle { delta in
                dragging = min(max((dragging ?? listWidth) + delta, Theme.Size.macTodosListMin), Theme.Size.macTodosListMax)
            } ended: {
                if let dragging { listWidth = dragging }
                dragging = nil
            }
            detailColumn
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private var listColumn: some View {
        VStack(spacing: 0) {
            TodosModeBar(view: $viewMode, mode: $calendarMode)
            searchField
            List(selection: $selectedTodoID) {
                if !(openList.isEmpty && doneList.isEmpty) {
                    if !openList.isEmpty {
                        Section("Open · \(openList.count)") {
                            ForEach(openList) { todo in
                                row(todo)
                            }
                        }
                    }

                    if !doneList.isEmpty {
                        Section {
                            DisclosureGroup(isExpanded: $expandedDone) {
                                ForEach(doneList) { todo in
                                    row(todo)
                                }
                            } label: {
                                Text("Done · \(doneList.count)")
                                    .font(Theme.Font.sectionHeader)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }
            .listStyle(.sidebar)
            .scrollContentBackground(.hidden)
            // Over the list, not a row of it: a sidebar list's row is one line, and the message was cut to it.
            .overlay(alignment: .top) {
                if openList.isEmpty && doneList.isEmpty {
                    EmptyStateView(
                        symbol: Theme.Icon.todo,
                        title: isSearching ? String(localized: "No matching to-dos") : String(localized: "No to-dos yet"),
                        message: isSearching ? String(localized: "Try a different search") : String(localized: "Write down something to do, hand it to an agent, and track its progress."),
                        action: isSearching ? nil : (String(localized: "New To-Do"), { showCreate = true })
                    )
                }
            }
            // A done-only search must not leave the matching group hidden behind the collapsed disclosure (#3474).
            .onChange(of: query) { _, _ in
                expandedDone = SearchExpansion.doneExpanded(query: query, hasDoneMatches: !doneList.isEmpty, current: expandedDone)
            }
        }
        // In the list column's own title row, at its trailing edge, as the Agents list's buttons (#1223, #2318): as a
        // window toolbar item its glyph came out gray, as if disabled; the open todo's menu keeps the window's edge.
        .overlay(alignment: .topTrailing) {
            MacTitleRowButton(String(localized: "New To-Do"), symbol: Theme.Icon.add) {
                creatingTitle = ""
                creatingNotes = ""
                showCreate = true
            }
            .padding(.trailing, Theme.Space.l)
            .frame(height: Theme.Size.macToolbarHeight)
            .ignoresSafeArea(.container, edges: .top)
        }
    }

    private func row(_ todo: TodoItem) -> some View {
        let lookup = TodosLogic.makeSessionLookup(AppModel.shared.allSessions(inFront: inFront))
        let status = TodosLogic.todoStatus(todo, lookup: lookup)
        let urgent = TodosLogic.mostUrgentAgent(todo, lookup: lookup)
        let activeAgents = todo.agents.filter { $0.releasedAt == nil }
        let ultracode = Ultracode.todoShown(todo, lookup: lookup)

        return Button {
            selectedTodoID = todo.id
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Button {
                    store.toggleDone(todo)
                } label: {
                    Image(systemName: todo.isDone ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                        .font(Theme.Font.secondary)
                        .foregroundStyle(todo.isDone ? Theme.positive : .secondary)
                }
                .buttonStyle(.plain)

                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    HStack {
                        Text(todo.title)
                            .font(Theme.Font.bodyStrong)
                            .foregroundStyle(todo.isDone ? Color.secondary : Color.primary)
                            .strikethrough(todo.isDone)
                            .lineLimit(1)
                        if ultracode { UltracodeMark(font: Theme.Font.labelStrong).transition(.opacity) }
                        Spacer()
                        if let d = todo.updatedAt ?? todo.createdAt {
                            Text(Fmt.relative(d))
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .animation(UltracodeMark.motion, value: ultracode)

                    HStack(spacing: Theme.Space.xs) {
                        StatusDot(state: urgent?.status ?? (status == .done ? .idle : .unknown), size: Theme.Size.dotS)
                        Text(todo.statusLabel(status))
                            .font(Theme.Font.labelStrong)
                            .foregroundStyle(status.todoTone.text)
                        if !activeAgents.isEmpty {
                            Text("· \(activeAgents.count) agents")
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                        if !todo.isDone { ReviewChip(todo: todo, compact: true) }
                    }

                    TodoPlanLine(todo: todo)

                    if let reason = todo.closedReason {
                        Text(reason)
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    } else if let summary = mostUrgentSummary(urgent: urgent) {
                        Text(summary)
                            .font(Theme.Font.caption)
                            .foregroundStyle(urgent?.status == .needsYou ? StatusPalette.needsYou.text : .secondary)
                            .lineLimit(1)
                    }
                }
            }
            .padding(.vertical, Theme.Space.xxs)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .tag(todo.id)
        .contextMenu {
            Button(todo.isDone ? String(localized: "Reopen") : String(localized: "Done [Extension System Presentation c0b3fb]")) {
                store.toggleDone(todo)
            }
            Divider()
            Button(role: .destructive) {
                Task {
                    do { try await store.delete(id: todo.id) } catch { AppModel.shared.show(error) }
                }
            } label: {
                Label("Delete", systemImage: Theme.Icon.delete)
            }
        }
    }

    private func mostUrgentSummary(urgent: TodoMostUrgent?) -> String? {
        guard let urgent else { return nil }
        if urgent.isFailed, let err = urgent.agent.startError, !err.isEmpty {
            return String(localized: "Failed to start: \(err)")
        }
        if let s = urgent.session {
            if let last = s.lastText, !last.isEmpty {
                return Fmt.plain(last)
            }
            return s.displayTitle
        }
        if let t = urgent.agent.title, !t.isEmpty {
            return t
        }
        return nil
    }

    private var searchField: some View {
        HStack(spacing: Theme.Space.xs) {
            Image(systemName: Theme.Icon.search)
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
            TextField(String(localized: "Search to-dos"), text: $query)
                .textFieldStyle(.plain)
            if !query.isEmpty {
                Button { query = "" } label: {
                    Image(systemName: Theme.Icon.clear).foregroundStyle(.tertiary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.xs)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .padding(.horizontal, Theme.Space.s)
        .padding(.top, Theme.Space.s)
    }

    @ViewBuilder private var detailColumn: some View {
        if let id = selectedTodoID {
            NavigationStack {
                TodoDetailView(todoID: id)
                    .id(id) // another todo gets a fresh page, so its title and notes are read from the store again
            }
        } else if openList.isEmpty && doneList.isEmpty {
            // One empty state a page (docs/apple/mac-design.md): the list's says there is nothing, the detail stays plain.
            Color.clear
        } else {
            SplitPlaceholder(symbol: Theme.Icon.todo, title: String(localized: "Select a To-do"), message: String(localized: "Select a to-do in the list on the left to see its details here."))
        }
    }

    private var createSheet: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(String(localized: "Title"), text: $creatingTitle)
                        .onSubmit { add() }
                    TextField(String(localized: "Notes (sent along when handed to an agent)"), text: $creatingNotes, axis: .vertical)
                        .lineLimit(3...8)
                }
            }
            .navigationTitle("New To-Do")
            .sheetCancel(unsaved: !(creatingTitle + creatingNotes).trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) {
                showCreate = false
            }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add [Beautiful U I Bridge 7a8a11]") { add() }
                        .lineLimit(1)
                        .disabled(creatingTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .macSheet()
        }
    }

    private func add() {
        let t = creatingTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        let n = creatingNotes.trimmingCharacters(in: .whitespacesAndNewlines)
        // The sheet closes only once the todo is saved: a failed save keeps the typed title and notes.
        Task {
            do {
                let created = try await store.create(title: t, notes: n.isEmpty ? nil : n)
                showCreate = false
                selectedTodoID = created.id
            } catch {
                AppModel.shared.show(error)
            }
        }
    }
}

/// Pure rule for the Mac todos page's selection after the store's list changes (#3471).
nonisolated enum MacTodosSelection {
    /// The id the detail column should show: the selected id while its todo is still in `existing` (the full
    /// store list, so a search that hides it does not move the selection); otherwise the first open todo, then
    /// the first done todo (the lists as currently filtered and sorted), then nil when nothing remains.
    static func resolve(selectedID: String?, existing: [TodoItem], open: [TodoItem], done: [TodoItem]) -> String? {
        if let selectedID, existing.contains(where: { $0.id == selectedID }) {
            return selectedID
        }
        return open.first?.id ?? done.first?.id
    }
}
