import BeautifulUI
import SwiftUI

public struct TodoDetailRoute: Hashable, Sendable, Identifiable {
    public var id: String
    public init(id: String) { self.id = id }
}

struct TodosView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var store = TodoStore.shared
    @State private var query = ""
    @State private var showCreate = false
    @State private var creatingTitle = ""
    @State private var creatingNotes = ""
    /// The New To-Do sheet's notes mic (#2188).
    @State private var dictation = Dictation()
    @State private var expandedDone = false
    /// DEBUG screenshots: `-openTodoTitled <text>` opens the first todo whose title has the text.
    @State private var debugOpen: TodoDetailRoute?
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

    private func matchesQuery(_ todo: TodoItem) -> Bool {
        if query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return true }
        let q = query.localizedLowercase
        if todo.title.localizedLowercase.contains(q) { return true }
        if let notes = todo.notes, notes.localizedLowercase.contains(q) { return true }
        return false
    }

    var body: some View {
        List {
            Section {
                searchRow
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets())
            .listRowSeparator(.hidden)

            if openList.isEmpty && doneList.isEmpty {
                emptySection
            } else {
                if !openList.isEmpty {
                    Section {
                        ForEach(openList) { todo in
                            NavigationLink(value: TodoDetailRoute(id: todo.id)) {
                                TodoRow(todo: todo, onToggleDone: { store.toggleDone(todo) })
                            }
                            .swipeActions(edge: .leading) {
                                Button {
                                    store.toggleDone(todo)
                                } label: {
                                    Label("Done [Extension System Presentation c0b3fb]", systemImage: Theme.Icon.done)
                                }
                                .tint(Theme.positive)
                            }
                            .swipeActions(edge: .trailing) {
                                Button(role: .destructive) {
                                    Task { do { try await store.delete(id: todo.id) } catch { AppModel.shared.show(error) } }
                                } label: {
                                    Label("Delete", systemImage: Theme.Icon.delete)
                                }
                            }
                        }
                    } header: {
                        HStack(spacing: Theme.Space.sm) {
                            Text("Open · \(openList.count)")
                                .font(Theme.Font.sectionHeader)
                                .foregroundStyle(.secondary)
                        }
                    }
                }

                if !doneList.isEmpty {
                    Section {
                        DisclosureGroup(isExpanded: $expandedDone) {
                            ForEach(doneList) { todo in
                                NavigationLink(value: TodoDetailRoute(id: todo.id)) {
                                    TodoRow(todo: todo, onToggleDone: { store.toggleDone(todo) })
                                }
                                .swipeActions(edge: .leading) {
                                    Button {
                                        store.toggleDone(todo)
                                    } label: {
                                        Label("Reopen", systemImage: Theme.Icon.undo)
                                    }
                                    .tint(Theme.accent)
                                }
                                .swipeActions(edge: .trailing) {
                                    Button(role: .destructive) {
                                        Task { do { try await store.delete(id: todo.id) } catch { AppModel.shared.show(error) } }
                                    } label: {
                                        Label("Delete", systemImage: Theme.Icon.delete)
                                    }
                                }
                            }
                        } label: {
                            HStack {
                                Text("Done · \(doneList.count)")
                                    .font(Theme.Font.sectionHeader)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }
        }
        .themedPage()
        .detailTitle(String(localized: "To-dos"))
        .navigationBarTitleDisplayMode(.inline)
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) {
                Button {
                    creatingTitle = ""
                    creatingNotes = ""
                    showCreate = true
                } label: {
                    ThemedIcon(Theme.Icon.add)
                }
                .accessibilityLabel("New To-Do")
            }
        }
        .sheet(isPresented: $showCreate) {
            createSheet
        }
        .refreshable {
            await store.refresh()
        }
        .task {
            await store.refresh()
            #if DEBUG
            // `-showDoneTodos YES`: the Done section starts open (screenshots of done todos).
            if UserDefaults.standard.bool(forKey: "showDoneTodos") { expandedDone = true }
            if let match = UserDefaults.standard.string(forKey: "openTodoTitled"),
               let todo = store.todos.first(where: { $0.title.contains(match) }) {
                debugOpen = TodoDetailRoute(id: todo.id)
            }
            #endif
        }
        .navigationDestination(item: $debugOpen) { r in TodoDetailView(todoID: r.id) }
    }

    private var searchRow: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.search)
                .font(Theme.Font.secondaryMedium)
                .foregroundStyle(.secondary)
            TextField(String(localized: "Search to-dos"), text: $query)
                .textFieldStyle(.plain)
            if !query.isEmpty {
                Button { query = "" } label: {
                    ThemedIcon(Theme.Icon.clear).foregroundStyle(.tertiary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .scaledFrame(height: Theme.Size.avatar)
        .background(Theme.Fill.chip, in: .capsule)
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.xs)
    }

    @ViewBuilder private var emptySection: some View {
        Section {
            EmptyStateView(
                symbol: Theme.Icon.todo,
                title: query.isEmpty ? String(localized: "No to-dos yet") : String(localized: "No matching to-dos"),
                message: query.isEmpty ? String(localized: "Write down something to do, hand it to an agent, and track its progress.") : String(localized: "Try a different search"),
                action: query.isEmpty ? (String(localized: "New To-Do"), { showCreate = true }) : nil
            )
        }
        .listRowBackground(Color.clear)
        .listRowSeparator(.hidden)
    }

    private var createSheet: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(String(localized: "Title"), text: $creatingTitle)
                        .lineLimit(1)
                        .onSubmit { add() }
                    HStack(alignment: .top, spacing: Theme.Space.s) {
                        TextField(String(localized: "Notes (sent along when handed to an agent)"), text: $creatingNotes, axis: .vertical)
                            .lineLimit(3...8)
                        FieldMicButton(dictation: dictation, text: $creatingNotes, id: "todo.new.notes.mic")
                            .font(Theme.Font.body)
                            .foregroundStyle(Theme.accent)
                    }
                }
            }
            .themedPage()
            .onDisappear(perform: stopDictation) // cancelled, swiped away or added
            .navigationTitle("New To-Do")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { showCreate = false }
                        .lineLimit(1)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add [Beautiful U I Bridge 7a8a11]") { add() }
                        .lineLimit(1)
                        .disabled(creatingTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
        .presentationDetents([.medium])
    }

    private func add() {
        let t = creatingTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        stopDictation() // what is saved is what the field shows
        let n = creatingNotes.trimmingCharacters(in: .whitespacesAndNewlines)
        // The sheet closes only once the todo is saved: a failed save keeps the typed title and notes.
        Task {
            do {
                _ = try await store.create(title: t, notes: n.isEmpty ? nil : n)
                showCreate = false
            } catch {
                AppModel.shared.show(error)
            }
        }
    }

    private func stopDictation() {
        if dictation.isListening { dictation.stop() }
    }
}

struct TodoRow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.tabIsActive) private var inFront
    var todo: TodoItem
    var onToggleDone: () -> Void

    var body: some View {
        let lookup = TodosLogic.makeSessionLookup(AppModel.shared.allSessions(inFront: inFront))
        let status = TodosLogic.todoStatus(todo, lookup: lookup)
        let urgent = TodosLogic.mostUrgentAgent(todo, lookup: lookup)
        let activeAgents = todo.agents.filter { $0.releasedAt == nil }
        let ultracode = Ultracode.todoShown(todo, lookup: lookup)

        HStack(alignment: .top, spacing: Theme.Space.m) {
            Button(action: onToggleDone) {
                ThemedIcon(todo.isDone ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                    .font(Theme.Font.title)
                    .foregroundStyle(todo.isDone ? Theme.positive : .secondary)
            }
            .buttonStyle(.plain)
            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)

            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .firstTextBaseline) {
                    UltracodeMark.after(todo.title, on: ultracode, font: Theme.Font.labelStrong)
                        .font(Theme.Font.bodyStrong)
                        .foregroundStyle(todo.isDone ? Color.secondary : Color.primary)
                        .strikethrough(todo.isDone)
                        .lineLimit(2)
                        .animation(UltracodeMark.motion, value: ultracode)
                    Spacer(minLength: Theme.Space.xs)
                    if let d = todo.updatedAt ?? todo.createdAt {
                        Text(Fmt.relative(d))
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }

                HStack(spacing: Theme.Space.sm) {
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
                        .lineLimit(2)
                } else if let summary = mostUrgentSummary(urgent: urgent) {
                    Text(summary)
                        .font(Theme.Font.caption)
                        .foregroundStyle(urgent?.status == .needsYou ? StatusPalette.needsYou.text : .secondary)
                        .lineLimit(2)
                } else if let notes = todo.notes, !notes.isEmpty {
                    Text(notes)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                } else {
                    Text("Not handed to an agent yet")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            .padding(.vertical, Theme.Space.xs)
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
}
