import SwiftUI

/// A task's detail, risen from the bottom like a link (docs/assistant/unified-entry.md「任务详情」): opens full, pulled down to
/// 0.6 the conversation behind stays usable. Its agent's page is the Agents page's own chat (`ChatView(style: .dev)`),
/// so everything that page does works here. That chat's header is the sheet's only one (`HostedChat`, #3255): ✕, the
/// task's title over the agent's state and place, and ⋯ with the task's own actions; no strip of the space's other
/// agents (「在 Agents 里打开」 in ⋯ has them). A task with several agents switches between them from the title's ⌄.
/// An agent no task stands for (the main conversation's dock, #1113) opens in the same sheet under its own title. In
/// regular width (iPad) the same page is pushed into the detail column instead (`detailPresentation`, #2816), its ✕ a ‹.
struct TaskDetailSheet: View {
    private var todoID: String?
    private var agentKey: SessionKey?
    @State private var detent: PresentationDetent = .large
    @State private var picked: SessionKey?
    @State private var removing: TodoItem?
    @Environment(\.dismiss) private var dismiss
    /// Pushed into the chat's stack (regular width): its back goes back, and the agent's page is the column's.
    @Environment(\.detailPushed) private var pushed
    private var store: TasksStore { .shared }

    init(todoID: String) { self.todoID = todoID }
    init(agent: SessionKey) { agentKey = agent }

    /// What an agent no task stands for says about itself (in ⋯, and the header while it has no page).
    static var agentNote: String { String(localized: "Agent · Not started from a chat") }

    var body: some View {
        DetailStack {
            Group {
                if let agentKey {
                    agentContent(agentKey)
                } else if let todo = todoID.flatMap({ store.todos[$0] }) {
                    content(todo)
                } else {
                    EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "This task is gone"), message: String(localized: "It may have been deleted."))
                }
            }
            // The agent's page hides the bar (it draws its own header), and so does the page while it has none.
            .toolbarVisibility(.hidden, for: .navigationBar)
        }
        .confirmTaskRemoval($removing)
        .presentationDetents([.fraction(0.6), .large], selection: $detent)
        .presentationDragIndicator(.visible)
        .presentationBackgroundInteraction(.enabled(upThrough: .fraction(0.6)))
    }

    @ViewBuilder private func content(_ todo: TodoItem) -> some View {
        let sessions = store.sessions(todo)
        let key = picked.flatMap { p in sessions.first { $0.key == p }?.key } ?? store.leadSession(todo)?.key
        let state = store.state(todo)
        // Top-aligned: when the agent's page asks for more than the sheet has (several tall cards waiting), what gives
        // is its bottom, never the header with its close.
        GeometryReader { geo in
        VStack(spacing: 0) {
            if let key {
                agentPage(key, host: hosted(title: todo.title, note: nil, agents: sessions, menu: state.isRemovable ? AnyView(
                    TaskRemoveMenuItem(todo: todo, state: state, pending: $removing)) : nil))
            } else {
                // No agent yet (opening, or it did not start): the task's own header over why.
                header(title: todo.title, line: nil, showsMenu: state.isRemovable) { TaskMark(state: state).scaledFrame(width: Theme.Size.markM) } menu: {
                    TaskRemoveMenuItem(todo: todo, state: state, pending: $removing)
                }
                Divider()
                Group {
                    if let failure = store.startFailure(todo) ?? todo.agents.first?.startError {
                        EmptyStateView(symbol: Theme.Icon.errorFilled, title: String(localized: "The agent didn't start"), message: failure,
                                       action: (title: String(localized: "Remove Task"), run: { store.askRemove(todo, pending: $removing) }))
                    } else {
                        EmptyStateView(symbol: Theme.Icon.working, title: String(localized: "Starting the agent"), message: String(localized: "Once it's up, its chat appears here."))
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .frame(width: geo.size.width, height: geo.size.height, alignment: .top) // the sheet's height, not the page's wish
        .clipped()
        }
    }

    /// The page's header while it has no agent's page under it: ✕ (‹ pushed), a mark, the title (and a line), ⋯.
    private func header<Mark: View, Items: View>(title: String, line: String?, showsMenu: Bool = false,
                                                 @ViewBuilder mark: () -> Mark,
                                                 @ViewBuilder menu: () -> Items) -> some View {
        HStack(spacing: Theme.Space.s) {
            if pushed { closeButton }
            mark()
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.heading).lineLimit(2)
                if let line { AgentMarkLine(text: line) }
            }
            Spacer(minLength: Theme.Space.s)
            if showsMenu {
                Menu { menu() } label: {
                    ThemedIcon(Theme.Icon.more).font(Theme.Font.secondaryStrong)
                        .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                }
                .themedButton()
                .clipShape(.circle)
                .accessibilityLabel("More")
            }
            if !pushed { closeButton }
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.top, Theme.Space.l) // below the sheet's grabber
        .padding(.bottom, Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

extension TaskDetailSheet {
    /// The agent's page under its own title, said in ⋯ to be an agent of its own.
    @ViewBuilder private func agentContent(_ key: SessionKey) -> some View {
        let session = AppModel.shared.session(key)
        GeometryReader { geo in
        VStack(spacing: 0) {
            if session != nil {
                agentPage(key, host: hosted(title: nil, note: Self.agentNote, agents: [], menu: nil))
            } else {
                header(title: String(localized: "Agent"), line: Self.agentNote) { EmptyView() } menu: { EmptyView() }
                Divider()
                EmptyStateView(symbol: Theme.Icon.agents, title: String(localized: "This agent is gone"), message: String(localized: "It may have been closed."))
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .frame(width: geo.size.width, height: geo.size.height, alignment: .top)
        .clipped()
        }
    }

    /// What the agent's page takes from the sheet for its header (`HostedChat`).
    private func hosted(title: String?, note: String?, agents: [Session], menu: AnyView?) -> HostedChat {
        HostedChat(title: title, note: note, closes: !pushed, leave: { dismiss() }, agents: agents,
                   select: { picked = $0 }, menu: menu,
                   openInAgents: { key in
                       // The sheet (or the pushed page) goes, and the Agents page opens on the agent.
                       store.detailTask = nil
                       store.detailAgent = nil
                       AppModel.shared.open(key)
                   },
                   expand: pushed ? nil : { store.expand($0) })
    }

    /// The agent's own page (the Agents page's chat), its header the sheet's. Pushed, it is the detail column's page
    /// as it is beside the Agents list (`SplitDetail`): the tab bar stays, and its leading button goes back.
    @ViewBuilder private func agentPage(_ key: SessionKey, host: HostedChat) -> some View {
        if pushed {
            ChatView(key: key, style: .dev).id(key)
                .environment(\.splitDetail, SplitDetail(isFirstPage: true))
                .environment(\.hostedChat, host)
        } else {
            ChatView(key: key, style: .dev).id(key)
                .environment(\.hostedChat, host)
        }
    }

    /// ✕ at the sheet's trailing edge; ‹ at the pushed page's leading one.
    private var closeButton: some View {
        Button { dismiss() } label: {
            ThemedIcon(pushed ? Theme.Icon.back : Theme.Icon.close).font(Theme.Font.secondaryStrong)
                .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
        }
        .themedButton()
        .clipShape(.circle)
        .accessibilityLabel(pushed ? String(localized: "Back [Task Sheets 572cf4]") : String(localized: "Close"))
    }
}

/// The Agents mark and a line after it, tight: what says an agent is no task's on the board and in its sheet.
private struct AgentMarkLine: View {
    var text: String

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.agents)
            Text(text).lineLimit(1)
        }
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
    }
}

/// All: the conversation's tasks by where they are — waiting for the user, going on, done, stopped.
struct TaskBoardSheet: View {
    var conversation: String
    @State private var removing: TodoItem?
    @Environment(\.dismiss) private var dismiss
    private var store: TasksStore { .shared }

    var body: some View {
        NavigationStack {
            let rows = store.tasks(of: conversation).map { (todo: $0, state: store.state($0)) }
            let agents = store.agents(of: conversation)
            List {
                if rows.isEmpty && agents.isEmpty {
                    EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "No tasks yet"), message: String(localized: "Tell Persona what needs doing in a chat, and it'll turn it into a task."))
                        .listRowBackground(Color.clear)
                }
                section(StateName.needsYou.text, rows.filter { $0.state == .needsYou }, agents: agents.filter(\.isWaiting))
                section(StateName.working.text, rows.filter { $0.state == .working || $0.state == .notStarted }, agents: agents.filter { !$0.isWaiting })
                section(StateName.unread.text, rows.filter { $0.state == .unread })
                section(StateName.followUp.text, rows.filter { $0.state == .followup })
                section(String(localized: "Done [Task Sheets f28461]"), rows.filter { $0.state == .done })
                section(String(localized: "Didn't Start"), rows.filter { $0.state == .failed })
            }
            .themedGroupedPage()
            .navigationTitle("Tasks")
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { dismiss() } label: { ThemedIcon(Theme.Icon.close) }
                        .accessibilityLabel("Close")
                }
            }
        }
        .confirmTaskRemoval($removing)
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
    }

    /// A section's tasks, then (in the main conversation) the agents no task stands for in the same state.
    @ViewBuilder private func section(_ title: String, _ rows: [(todo: TodoItem, state: TaskState)], agents: [DockAgent] = []) -> some View {
        if !rows.isEmpty || !agents.isEmpty {
            Section {
                ForEach(rows, id: \.todo.id) { row in
                    Button {
                        store.openAfterBoard = row.todo.id // opened once the board is down (one sheet at a time)
                        store.showBoard = false
                    } label: {
                        HStack(spacing: Theme.Space.m) {
                            TaskMark(state: row.state).scaledFrame(width: Theme.Size.markM)
                            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                Text(row.todo.title).font(Theme.Font.body).lineLimit(1)
                                Text(subtitle(row.todo)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                            Spacer(minLength: Theme.Space.s)
                            ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    // The Agents page's Close, for a task: swiped from the trailing edge, only when nothing of it runs.
                    .swipeActions(edge: .trailing) {
                        if row.state.isRemovable {
                            Button { store.askRemove(row.todo, pending: $removing) } label: {
                                Label("Remove [Task Sheets 6135d4]", systemImage: Theme.Icon.delete)
                            }
                            .tint(Theme.danger)
                        }
                    }
                    .contextMenu { TaskRemoveMenuItem(todo: row.todo, state: row.state, pending: $removing) }
                }
                ForEach(agents) { agent in agentRow(agent) }
            } header: {
                Text("\(title) · \(rows.count + agents.count)")
            }
        }
    }

    private func agentRow(_ agent: DockAgent) -> some View {
        Button {
            store.openAgentAfterBoard = agent.key // opened once the board is down (one sheet at a time)
            store.showBoard = false
        } label: {
            HStack(spacing: Theme.Space.m) {
                StatusDot(state: agent.state, size: Theme.Size.dotL).scaledFrame(width: Theme.Size.markM)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(agent.title).font(Theme.Font.body).lineLimit(1)
                    AgentMarkLine(text: agentSubtitle(agent))
                }
                Spacer(minLength: Theme.Space.s)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("task-board.agent")
    }

    /// Where the agent is and what it said last, like a task's line: machine · project · agent · last words; while it
    /// waits for the user, what it asks instead (#3998).
    private func agentSubtitle(_ agent: DockAgent) -> String {
        let model = AppModel.shared
        let session = model.session(agent.key)
        let asks = session?.state == .needsYou ? TaskAsk.line(model.pendingInteractions.filter { $0.sessionKey == agent.key }) : nil
        let last = asks ?? session?.lastText?.trimmingCharacters(in: .whitespacesAndNewlines)
        return [model.machine(agent.key.machineRef)?.name ?? agent.key.machineID, session?.folderName, session?.agentLabel, last]
            .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
    }

    private func subtitle(_ todo: TodoItem) -> String {
        let lead = store.leadSession(todo)
        let place = TaskPlace.line(todo, lead: lead)
        if let failure = store.startFailure(todo) { return place.isEmpty ? failure : place + " · " + failure }
        // Waiting for the user: what it asks (#3998), before its last words.
        if store.state(todo) == .needsYou, let asks = TaskAsk.line(store.interactions(todo)) {
            return place.isEmpty ? asks : place + " · " + asks
        }
        guard let now = TaskNow(lead: lead, working: store.state(todo) == .working) else { return place }
        return place + " · " + now.text
    }
}
