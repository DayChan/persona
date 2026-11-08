import BeautifulUI
import SwiftUI

struct TodoHandoffSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var todo: TodoItem

    @State private var mode: Mode = .newAgent
    @State private var instruction = ""
    /// 「新开一个」 presents NewChatView, which picks the computer, the folder and the launcher, and starts the Agent.
    @State private var showNewChat = false

    // Existing Agent
    @State private var selectedSessionKey: SessionKey?

    @State private var isSubmitting = false
    @State private var errorMessage: String?

    enum Mode: String, CaseIterable, Identifiable {
        case newAgent = "新开一个" // i18n-ignore: id; shown via `title`
        case existingAgent = "交给已有的" // i18n-ignore: id; shown via `title`
        var id: String { rawValue }
        var title: String {
            switch self {
            case .newAgent: String(localized: "New Agent [Todo Handoff Sheet d7ba99]")
            case .existingAgent: String(localized: "Existing Agent")
            }
        }
    }

    /// The hub the todo is on (`AppModel.hubRef`): only its Agents can take the todo, and only its computers start one.
    private var hubGroup: String { model.hub?.group ?? "" }

    private var openSessions: [Session] {
        model.allSessions.filter { $0.state.isLive && !$0.assistant && $0.key.group == hubGroup }
    }

    private var contextTag: String { TodosLogic.formatTodoContext(title: todo.title, notes: todo.notes) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("Mode", selection: $mode) {
                        ForEach(Mode.allCases) { m in
                            Text(m.title).tag(m)
                        }
                    }
                    .pickerStyle(.segmented)
                }

                if mode == .newAgent {
                    Section {
                        Text("Next, choose a computer, folder and launcher. This to-do and the notes below are sent together as the first message.")
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.secondary)
                    }
                } else {
                    existingAgentSection
                }

                Section("Instructions") {
                    TextField(String(localized: "Tell the agent what to do…"), text: $instruction, axis: .vertical)
                        .lineLimit(3...8)
                }

                Section("Sent Along") {
                    TodoPreviewCard(title: todo.title, notes: todo.notes)
                }

                if let err = errorMessage {
                    Section {
                        Label(err, systemImage: Theme.Icon.errorFilled)
                            .font(Theme.Font.caption)
                            .foregroundStyle(Theme.danger)
                    }
                }
            }
            .themedPage()
            .navigationTitle("Hand to Agent")
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: !instruction.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || selectedSessionKey != nil) {
                dismiss()
            }
            .safeAreaInset(edge: .bottom) {
                Button {
                    if mode == .newAgent {
                        showNewChat = true
                    } else {
                        Task { await submit() }
                    }
                } label: {
                    HStack(spacing: Theme.Space.s) {
                        if isSubmitting { ProgressView().tint(.white) }
                        ThemedIcon(Theme.Icon.handToAI)
                        Text(mode == .newAgent ? String(localized: "Choose Computer and Launcher") : String(localized: "Hand to Agent"))
                    }
                    .font(Theme.Font.heading)
                    .lineLimit(1)
                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.wellS)
                }
                .themedButton(prominent: true)
                .controlSize(.large)
                .disabled(isSubmitting || !canSubmit)
                .padding(.horizontal, Theme.Space.page)
                .padding(.vertical, Theme.Space.s)
                .background(Theme.pageBackground)
            }
        }
        .presentationDetents([.medium, .large])
        #if os(macOS)
        // A window sheet does not take the main window's grouped forms: the Mac put the labels in a column of their
        // own, cut off at the sheet's left edge, and the text past its right (#1248).
        .macSheet()
        #endif
        .sheet(isPresented: $showNewChat) {
            // The new-chat form itself, with the todo: its context block goes ahead of the first message, the session
            // takes the todo's title, and only this hub's computers are offered.
            NewChatView(initialPrompt: instruction, todoID: todo.id, contextBlock: contextTag,
                        nameHint: todo.title, onlyGroup: hubGroup) { s in
                showNewChat = false
                TodoStore.shared.reload()
                dismiss()
                openTodoAgentChat(s.key, in: model)
            }
        }
    }

    private var canSubmit: Bool {
        mode == .newAgent || selectedSessionKey != nil
    }

    private var existingAgentSection: some View {
        Section {
            if openSessions.isEmpty {
                Text("No running agent sessions")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(openSessions) { s in
                    Button {
                        selectedSessionKey = s.key
                    } label: {
                        HStack {
                            StatusDot(state: s.state, size: Theme.Size.dotL)
                            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                Text(s.displayTitle)
                                    .font(Theme.Font.bodyStrong)
                                    .foregroundStyle(Color.primary)
                                    .lineLimit(1)
                                Text("\(model.machine(s.machineRef)?.title ?? s.machineID) · \(s.folderName)")
                                    .font(Theme.Font.caption)
                                    .foregroundStyle(.secondary)
                            }
                            Spacer()
                            if selectedSessionKey == s.key {
                                ThemedIcon(Theme.Icon.done)
                                    .foregroundStyle(Theme.accent)
                            }
                        }
                    }
                }
            }
        } header: { Text("Choose Existing Agent") }
    }

    private func submit() async {
        guard let key = selectedSessionKey else { return }
        isSubmitting = true
        errorMessage = nil
        defer { isSubmitting = false }

        let instr = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        let fullMessage = instr.isEmpty ? contextTag : "\(contextTag)\n\n\(instr)"
        guard let api = model.api(key.group) else {
            errorMessage = String(localized: "Can't reach this agent's hub. Try again later.")
            return
        }
        do {
            try await api.send(key, text: fullMessage, clientMsgID: UUID().uuidString, todoID: todo.id)
            TodoStore.shared.reload()
            dismiss()
            openTodoAgentChat(key, in: model)
        } catch {
            errorMessage = error.shown
        }
    }
}

struct TodoPreviewCard: View {
    var title: String
    var notes: String?

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.s) {
            Rectangle()
                .fill(Theme.accent)
                .frame(width: Theme.Size.bar)
                .padding(.vertical, Theme.Space.xs)

            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.todo)
                        .font(Theme.Font.caption)
                    Text("To-do · \(title)")
                        .font(Theme.Font.bodyStrong)
                        .lineLimit(1)
                }

                if let notes, !notes.isEmpty {
                    Text(notes)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(3)
                }
            }
            Spacer()
        }
        .padding(Theme.Space.m)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
    }
}
