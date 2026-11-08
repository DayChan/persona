import BeautifulUI
import SwiftUI

/// The same picker links an individual Agent or an entire project, including a newly created todo.
struct TodoLinkAgentSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let target: TodoLinkTarget
    @State private var store = TodoStore.shared
    @State private var draft = TodoLinkDraft()

    init(session: Session) {
        target = .agent(machineID: session.machineID, sessionID: session.sessionID, currentTodoID: session.todo?.id)
    }

    init(space: TodoItem.Space) { target = .space(space) }

    private var currentTodoID: String? { target.currentTodoID(in: store.todos) }
    private var title: String {
        if target.isSpace { return currentTodoID == nil ? String(localized: "Add to To-Do") : String(localized: "Move to Another To-Do") }
        return currentTodoID == nil ? String(localized: "Link to To-Do") : String(localized: "Move to Another To-Do")
    }

    private var openTodos: [TodoItem] {
        let appModel: AppModel = model
        let group = appModel.hub?.group ?? ""
        let sessions = TodosLogic.linkedSessions(of: store.todos, hubGroup: group, session: appModel.session)
        return TodosLogic.sortTodos(store.openTodos, lookup: TodosLogic.makeSessionLookup(sessions))
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    if case .space(let project) = target {
                        Text("Choose an open to-do to add the whole “\(project.space)” project to:")
                    } else {
                        Text("Choose a to-do to link this agent to:")
                    }
                }
                .font(Theme.Font.secondary)
                .foregroundStyle(.secondary)
                .listRowBackground(Color.clear)

                Section {
                    if openTodos.isEmpty {
                        Text("No open to-dos").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                    ForEach(openTodos) { todo in
                        Button { draft.selectedTodoID = todo.id } label: {
                            HStack {
                                Text(todo.title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                                Spacer()
                                if draft.selectedTodoID == todo.id {
                                    ThemedIcon(Theme.Icon.done).foregroundStyle(Theme.accent)
                                }
                            }
                        }
                    }
                }

                Section {
                    if draft.creating {
                        TextField(String(localized: "New to-do title…"), text: $draft.newTitle)
                            .font(Theme.Font.body)
                            .onSubmit { createAndLink() }
                        Button("Create and Add") { createAndLink() }
                            .lineLimit(1)
                            .disabled(draft.newTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    } else {
                        Button { draft.creating = true; draft.newTitle = "" } label: {
                            Label("New To-Do…", systemImage: Theme.Icon.add).lineLimit(1)
                        }
                    }
                    if let currentTodoID {
                        Button(target.isSpace ? String(localized: "Remove from To-Do") : String(localized: "Unlink"), role: .destructive) {
                            submit { try await store.unlink(target, from: currentTodoID) }
                        }
                        .lineLimit(1)
                    }
                }
            }
            .accessibilityIdentifier("todo.link.list")
            .disabled(draft.busy)
            .themedPage()
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }.lineLimit(1).disabled(draft.busy)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(target.isSpace ? String(localized: "Add [Todo Link Agent Sheet ecf9c1]") : String(localized: "Link [Todo Link Agent Sheet 748cd8]")) {
                        submit { try await store.link(target, to: draft.selectedTodoID) }
                    }
                    .lineLimit(1)
                    // the todo it is on already: nothing to move (linking it again would only rewrite its source, #2348)
                    .disabled(draft.selectedTodoID.isEmpty || draft.selectedTodoID == currentTodoID || draft.busy)
                }
            }
        }
        .onAppear { draft.reset(currentTodoID: currentTodoID) }
        .onChange(of: target.id) { _, _ in draft.reset(currentTodoID: currentTodoID) }
        .presentationDetents([.medium, .large])
    }

    private func submit(_ operation: @escaping @MainActor () async throws -> Void) {
        draft.busy = true
        Task {
            defer { draft.busy = false }
            do { try await operation(); dismiss() }
            catch { AppModel.shared.show(error) }
        }
    }

    private func createAndLink() {
        let title = draft.newTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return }
        submit {
            if let message = try await store.createAndLink(title: title, target: target) {
                AppModel.shared.toast = Toast(message, style: .error)
            }
        }
    }
}
