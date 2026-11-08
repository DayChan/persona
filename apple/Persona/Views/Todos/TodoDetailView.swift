import BeautifulUI
import SwiftUI

struct TodoDetailView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.tabIsActive) private var inFront
    @Environment(\.dismiss) private var dismiss
    var todoID: String

    @State private var store = TodoStore.shared
    @State private var text = TodoEditorText()
    @FocusState private var titleFocused: Bool
    @State private var isEditingNotes = false
    @State private var editingNotesText = ""
    @State private var showHandoff = false
    @State private var showDeleteConfirm = false
    @State private var showReleased = false
    /// The title the field was last filled with: only a change from it is the user's edit. A title changed elsewhere
    /// while the field had focus is not taken into it, and must not be written back over (#3330).
    @State private var titleBaseline = ""
    /// The title being saved, so one edit is not sent twice.
    @State private var savingTitle: String?
    /// The PR its work is in, opened in the app's link sheet (docs/apple/ios.md, links open inside the app).
    @State private var link: LinkTarget?
    @State private var decisions = TodoDecisionsModel()

    private var currentTodo: TodoItem? {
        store.todo(id: todoID)
    }

    var body: some View {
        Group {
            if let todo = currentTodo {
                content(todo)
            } else {
                VStack(spacing: Theme.Space.l) {
                    EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "This to-do doesn't exist or was deleted"), message: "")
                }
                .themedPage()
            }
        }
        .detailTitle(currentTodo?.title ?? String(localized: "To-Do Details"))
        .navigationBarTitleDisplayMode(.inline)
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button(role: .destructive) {
                        showDeleteConfirm = true
                    } label: {
                        Label("Delete To-Do", systemImage: Theme.Icon.delete)
                    }
                } label: {
                    ThemedIcon(Theme.Icon.more)
                }
            }
        }
        .sheet(isPresented: $showHandoff) {
            if let todo = currentTodo {
                TodoHandoffSheet(todo: todo)
            }
        }
        .sheet(item: $decisions.form) { form in
            TodoDecisionSheet(supersedes: form.supersedes) { question, choice, reason in
                await decisions.add(question: question, choice: choice, reason: reason, supersedes: form.supersedes)
            }
        }
        // The hub moves the todo's updated_at when a decision is written, here or anywhere (an answered card too).
        .tabActiveTask(id: "\(todoID) \(currentTodo?.updatedAt?.timeIntervalSince1970 ?? 0)") {
            decisions.select(todoID)
            #if DEBUG
            // `-showDecisionHistory YES`: the revised ones are shown from the start, for screenshots.
            if UserDefaults.standard.bool(forKey: "showDecisionHistory"), decisions.all == nil { decisions.showAll = true }
            #endif
            await decisions.load()
        }
        #if os(macOS)
        .macLinkPanel($link)
        #else
        .sheet(item: $link) { target in
            LinkSheet(target: target, key: model.assistantMain ?? model.assistantCurrent)
        }
        #endif
        .alert("Delete To-Do", isPresented: $showDeleteConfirm) {
            Button("Delete", role: .destructive) {
                Task { await deleteTodo() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Delete this to-do? Its linked agent sessions won't be deleted.")
        }
        .onChange(of: currentTodo) { _, todo in
            // Another todo (the Mac list picked it), or an edit made elsewhere: take the stored text, but never over a
            // field the user is typing in.
            if let todo {
                text.reseed(from: todo, editingTitle: titleFocused, editingNotes: isEditingNotes)
                if !titleFocused { titleBaseline = todo.title }
            }
        }
        // Return is not the only way out of the title: tapping elsewhere or leaving the page saves it too (#3330).
        .onChange(of: titleFocused) { _, focused in
            if !focused { commitTitle() }
        }
        .onDisappear { commitTitle() }
        .onAppear {
            #if DEBUG
            if UserDefaults.standard.bool(forKey: "showTodoHandoff") {
                showHandoff = true
            }
            #endif
            if let todo = currentTodo {
                text = TodoEditorText(todo)
                titleBaseline = todo.title
            }
        }
    }

    /// Saves the edited title if the user changed it. An emptied title goes back to the stored one rather than staying
    /// blank.
    private func commitTitle() {
        guard let todo = currentTodo else { return }
        let t = text.title.trimmingCharacters(in: .whitespacesAndNewlines)
        // Nothing typed (or only spaces around it): show the stored title, which may have changed elsewhere meanwhile.
        guard !t.isEmpty, t != titleBaseline, t != todo.title else {
            if !titleFocused, savingTitle == nil { text.title = todo.title; titleBaseline = todo.title }
            return
        }
        // Return and the focus change that follows it both land here: send each new title once.
        guard t != savingTitle else { return }
        savingTitle = t
        Task {
            do { _ = try await store.update(id: todo.id, title: t) } catch { model.show(error) }
            if savingTitle == t { savingTitle = nil }
        }
    }

    /// Deletes the todo and leaves its page; a failed delete keeps the page and shows the error.
    private func deleteTodo() async {
        do {
            try await store.delete(id: todoID)
            dismiss()
        } catch {
            report(error)
        }
    }

    /// Shows a failed write's error, from a plain method rather than inside the async block.
    private func report(_ failure: any Error) {
        AppModel.shared.show(failure)
    }

    private func content(_ todo: TodoItem) -> some View {
        let sessions = model.allSessions(inFront: inFront)
        let lookup = TodosLogic.makeSessionLookup(sessions)
        let status = TodosLogic.todoStatus(todo, lookup: lookup)
        let activeAgents = todo.agents.filter { $0.releasedAt == nil }
        let releasedAgents = todo.agents.filter { $0.releasedAt != nil }

        return ScrollViewReader { proxy in List {
            Section {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    TextField(String(localized: "To-do title"), text: $text.title)
                        .font(Theme.Font.title)
                        .focused($titleFocused)
                        .onSubmit { commitTitle() }

                    HStack(spacing: Theme.Space.sm) {
                        Chip(text: todo.statusLabel(status), look: .tone(status.todoTone))
                        Text("·")
                            .foregroundStyle(.secondary)
                        Text("\(activeAgents.count) agents")
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                        if let d = todo.createdAt {
                            Text("·")
                                .foregroundStyle(.secondary)
                            Text("Created \(Fmt.relative(d))")
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                        }
                    }

                    if todo.autoDone {
                        // The hub closed it (docs/assistant/todos.md「自动完成」): say why, and how to undo it.
                        Text(todo.autoDoneReason.map { String(localized: "The agent stopped and the work looked finished, so it was marked done: \($0). If it isn't, tap “Reopen”.") }
                             ?? String(localized: "The agent stopped and the work looked finished, so it was marked done. If it isn't, tap “Reopen”."))
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }

            if todo.supervision != nil {
                // A task the main conversation supervises (#3124): its criteria as the latest review found them.
                Section { ReviewChecklist(todo: todo).padding(.vertical, Theme.Space.xs) }
            }

            stageSection(todo)

            Section {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    HStack {
                        Text("Notes")
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                        Spacer()
                        Button(isEditingNotes ? String(localized: "Done [Extension System Presentation c0b3fb]") : String(localized: "Edit [Message Menu 051836]")) {
                            if isEditingNotes {
                                let n = editingNotesText.trimmingCharacters(in: .whitespacesAndNewlines)
                                Task {
                                    do {
                                        _ = try await store.update(id: todo.id, notes: n)
                                        text.notes = n
                                        isEditingNotes = false
                                    } catch {
                                        // Still editing: what was typed stays in the field, and the error says why it did not save.
                                        model.show(error)
                                    }
                                }
                            } else {
                                editingNotesText = text.notes
                                isEditingNotes = true
                            }
                        }
                        .font(Theme.Font.captionStrong)
                        .lineLimit(1)
                        .foregroundStyle(Theme.accent)
                    }

                    if isEditingNotes {
                        TextField(String(localized: "To-do notes"), text: $editingNotesText, axis: .vertical)
                            .lineLimit(3...8)
                            .textFieldStyle(.plain)
                            .padding(Theme.Space.s)
                            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                    } else if !text.notes.isEmpty {
                        Text(text.notes)
                            .font(Theme.Font.body)
                            .foregroundStyle(Color.primary)
                    } else {
                        Text("No notes yet")
                            .font(Theme.Font.body)
                            .foregroundStyle(.tertiary)
                    }
                }
                .padding(.vertical, Theme.Space.xs)
                #if DEBUG
                .id(Self.notesRow)
                #endif
            }

            TodoDecisionsSection(todo: todo, lookup: lookup, decisions: decisions)

            TodoSpacesSection(todo: todo, sessions: sessions, machineName: machineName, hubGroup: model.hub?.group,
                              machineOnline: { model.machine(model.hubRef($0), inFront: inFront)?.online ?? false })

            Section {
                if activeAgents.isEmpty {
                    Text("No agents are running. Tap “Hand to Agent” below to assign one.")
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                        .padding(.vertical, Theme.Space.xs)
                } else {
                    ForEach(activeAgents, id: \.sessionID) { a in
                        agentRow(a, todo: todo, isReleased: false, lookup: lookup)
                    }
                }
            } header: {
                Text("Agents · \(activeAgents.count)")
                    .font(Theme.Font.sectionHeader)
                    .foregroundStyle(.secondary)
            }

            if !releasedAgents.isEmpty {
                Section {
                    DisclosureGroup(isExpanded: $showReleased) {
                        ForEach(releasedAgents, id: \.sessionID) { a in
                            agentRow(a, todo: todo, isReleased: true, lookup: lookup)
                        }
                    } label: {
                        Text("Earlier · \(releasedAgents.count)")
                            .font(Theme.Font.sectionHeader)
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
        .themedPage()
        .safeAreaInset(edge: .bottom) {
            bottomBar(todo)
        }
        #if DEBUG
        // `-showDecisionHistory YES` also scrolls the notes to the top once the decisions are read, so the whole
        // Decisions section under them is in the screenshot.
        .onChange(of: decisions.all != nil) { _, read in
            if read, UserDefaults.standard.bool(forKey: "showDecisionHistory") { proxy.scrollTo(Self.notesRow, anchor: .top) }
        }
        #endif
        }
    }

    #if DEBUG
    private static let notesRow = "todo.notes"
    #endif

    /// How far the hub says the work got (`stage`, #2776) and the PR its work is in (`pr_url`, #2768), when it has
    /// either; a hub that sends neither draws nothing here.
    @ViewBuilder private func stageSection(_ todo: TodoItem) -> some View {
        let stage = todo.todoStage
        let pr = todo.prLink
        if stage != nil || pr != nil {
            Section {
                VStack(alignment: .leading, spacing: Theme.Space.m) {
                    if let stage {
                        TodoStageBar(stage: stage)
                    }
                    if let pr {
                        Button { link = LinkTarget(url: pr) } label: {
                            Label(todo.prLabel ?? String(localized: "View PR"), systemImage: Theme.Icon.pullRequest)
                                .font(Theme.Font.captionStrong)
                                .lineLimit(1)
                        }
                        .themedButton()
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }
        }
    }

    /// The machine's name as the other pages show it (`model.machine(...)?.title`, the same as the Agents rows): the hub's
    /// own machines, since a todo belongs to the hub (`AppModel.hubRef`).
    private func machineName(_ id: String) -> String {
        model.machine(model.hubRef(id), inFront: inFront)?.title ?? id
    }

    private func agentRow(_ a: TodoItem.Agent, todo: TodoItem, isReleased: Bool, lookup: @escaping TodosLogic.SessionLookup) -> some View {
        let key = SessionKey(group: model.hub?.group ?? "", machineID: a.machineID, sessionID: a.sessionID)
        let s = lookup(a.machineID, a.sessionID)
        let isFailed = a.startError.map { !$0.isEmpty } ?? false
        let title = a.title ?? s?.displayTitle ?? String(localized: "Agent Session")
        let subtitle: String = {
            if isFailed, let err = a.startError {
                return String(localized: "Failed to start: \(err)")
            }
            if isReleased {
                return String(localized: "Ended")
            }
            if let s, let last = s.lastText, !last.isEmpty {
                return Fmt.plain(last)
            }
            return a.cwd ?? ""
        }()

        return Button {
            dismiss()
            openTodoAgentChat(key, in: model)
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                if isFailed {
                    ThemedIcon(Theme.Icon.errorFilled)
                        .foregroundStyle(Theme.danger)
                        .font(Theme.Font.body)
                        .padding(.top, Theme.Space.xs)
                } else {
                    StatusDot(state: isReleased ? .idle : (s?.state ?? .offline), size: Theme.Size.dotL)
                        .padding(.top, Theme.Space.xs)
                }

                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack {
                        Text(title)
                            .font(Theme.Font.bodyStrong)
                            .lineLimit(1)
                        Spacer()
                        if let d = a.linkedAt {
                            Text(Fmt.relative(d))
                                .font(Theme.Font.label)
                                .foregroundStyle(.secondary)
                        }
                    }

                    let place = [machineName(a.machineID), s?.spaceName, s?.slotLabel].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
                    if !place.isEmpty {
                        Text(place)
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }

                    if let source = TodosLogic.agentSource(a, lookup: lookup) {
                        Text(source)
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }

                    Text(subtitle)
                        .font(Theme.Font.secondary)
                        .foregroundStyle(isFailed ? Theme.danger : s?.state == .needsYou ? StatusPalette.needsYou.text : .secondary)
                        .lineLimit(2)
                }
                ThemedIcon(Theme.Icon.disclosure)
                    .font(Theme.Font.label)
                    .foregroundStyle(.tertiary)
                    .padding(.top, Theme.Space.xs)
            }
            .padding(.vertical, Theme.Space.xs)
        }
        .buttonStyle(.plain)
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) {
                Task {
                    do {
                        try await store.unlinkAgent(todoID: todo.id, machineID: a.machineID, sessionID: a.sessionID)
                    } catch {
                        model.show(error)
                    }
                }
            } label: {
                Label(isFailed ? String(localized: "Remove [Task Sheets 6135d4]") : String(localized: "Unlink"), systemImage: isFailed ? Theme.Icon.delete : Theme.Icon.link)
            }
        }
        #if os(macOS)
        // A Mac list only swipes with a two-finger trackpad gesture: the row's menu unlinks too (#3891).
        .contextMenu {
            Button(role: .destructive) {
                Task {
                    do {
                        try await store.unlinkAgent(todoID: todo.id, machineID: a.machineID, sessionID: a.sessionID)
                    } catch {
                        model.show(error)
                    }
                }
            } label: {
                Label(isFailed ? String(localized: "Remove [Task Sheets 6135d4]") : String(localized: "Unlink"), systemImage: isFailed ? Theme.Icon.delete : Theme.Icon.link)
            }
        }
        #endif
    }

    private func bottomBar(_ todo: TodoItem) -> some View {
        HStack(spacing: Theme.Space.m) {
            Button {
                showHandoff = true
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.handToAI)
                    Text("Hand to Agent").lineLimit(1).minimumScaleFactor(0.8)
                }
                .font(Theme.Font.heading)
                .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.wellS)
            }
            .themedButton(prominent: true)
            .controlSize(.large)

            Button {
                store.toggleDone(todo)
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(todo.isDone ? Theme.Icon.undo : Theme.Icon.done)
                    Text(todo.isDone ? String(localized: "Reopen") : String(localized: "Done [Extension System Presentation c0b3fb]")).lineLimit(1).minimumScaleFactor(0.8)
                }
                .font(Theme.Font.heading)
                .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.wellS)
            }
            .themedButton(prominent: false)
            .controlSize(.large)
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
        .background(Theme.pageBackground)
    }
}

/// Opens an Agent's chat from a todo. On Mac the todos section gives way first, as it does for every other way into a
/// chat (`MacShellState.willOpen`); the hand-off sheet and the todo's Agent rows share this.
@MainActor func openTodoAgentChat(_ key: SessionKey, in model: AppModel) {
    #if os(macOS)
    MacShellState.shared.willOpen()
    #endif
    model.open(key)
}
