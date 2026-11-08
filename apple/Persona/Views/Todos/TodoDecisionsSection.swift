import SwiftUI

/// A todo's Decisions (#1618), under its notes as on the web: the current ones, oldest first, and on request the revised
/// ones struck through. Add records one; Revise records one that replaces it; the row's menu marks it as revising an
/// earlier one, or undoes that. The model, its reload and the form are the page's (`TodoDetailView`): a modifier on a
/// list section reaches each of its rows.
struct TodoDecisionsSection: View {
    let todo: TodoItem
    let lookup: TodosLogic.SessionLookup
    let decisions: TodoDecisionsModel

    var body: some View {
        if !decisions.unsupported {
            Section {
                rows
            } header: {
                header
            }
        }
    }

    private var header: some View {
        HStack(spacing: Theme.Space.s) {
            Text("Decisions · \(decisions.current.count)")
                .font(Theme.Font.sectionHeader)
                .foregroundStyle(.secondary)
            Spacer()
            Button { decisions.form = TodoDecisionForm() } label: {
                Label("Add [Todo Decisions]", systemImage: Theme.Icon.add)
                    .font(Theme.Font.captionStrong)
                    .lineLimit(1)
            }
            .buttonStyle(.borderless)
            .foregroundStyle(Theme.accent)
            .textCase(nil)
            .accessibilityIdentifier("todo.decisions.add")
        }
    }

    @ViewBuilder private var rows: some View {
        if let failed = decisions.failed, decisions.all == nil {
            HStack(spacing: Theme.Space.m) {
                Text("Couldn't load decisions: \(failed)")
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                Spacer(minLength: 0)
                Button { Task { await decisions.load() } } label: {
                    Text("Retry").lineLimit(1)
                }
                .font(Theme.Font.captionStrong)
                .buttonStyle(.borderless)
            }
            .padding(.vertical, Theme.Space.xs)
        } else if decisions.all == nil {
            HStack(spacing: Theme.Space.s) {
                ProgressView()
                Text("Loading decisions…")
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
            }
            .padding(.vertical, Theme.Space.xs)
        } else if decisions.shown.isEmpty {
            Text("Decisions made on this to-do go here. Your answers to an agent's questions are recorded automatically, and so are the outcome, decisions and pitfalls when a task is finished; you can add your own. The current ones go along when you hand it to an agent.")
                .font(Theme.Font.secondary)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.vertical, Theme.Space.xs)
                .accessibilityIdentifier("todo.decisions.empty")
        } else {
            ForEach(decisions.shown) { d in
                row(d)
            }
            if decisions.revisedCount > 0 {
                Button {
                    withAnimation(Theme.Motion.standard) { decisions.showAll.toggle() }
                } label: {
                    Text(decisions.showAll ? String(localized: "Current Only")
                                           : String(localized: "Full History (\(decisions.revisedCount) Revised)"))
                        .font(Theme.Font.secondaryMedium)
                        .lineLimit(1)
                }
                .accessibilityIdentifier("todo.decisions.history")
            }
        }
    }

    private func row(_ d: TodoDecision) -> some View {
        let off = !d.isCurrent
        return VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Text(d.question)
                    .font(Theme.Font.bodyStrong)
                    .strikethrough(off)
                    .foregroundStyle(off ? .tertiary : .primary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if !off {
                    actions(d)
                }
            }
            Text("Chose: \(d.choice)")
                .font(Theme.Font.body)
                .strikethrough(off)
                .foregroundStyle(off ? .tertiary : .primary)
            if !d.reason.isEmpty {
                Text("Reason: \(d.reason)")
                    .font(Theme.Font.secondary)
                    .strikethrough(off)
                    .foregroundStyle(off ? .tertiary : .secondary)
            }
            Text(meta(d))
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)
        }
        .padding(.vertical, Theme.Space.xs)
        .accessibilityIdentifier("todo.decision." + d.id)
    }

    /// When, where it came from, and whether it was revised or revises another.
    private func meta(_ d: TodoDecision) -> String {
        var parts = [d.createdAt.map { TodoDecisionsLogic.when($0) }, TodoDecisionsLogic.sourceLabel(d, todo: todo, lookup: lookup)]
            .compactMap { $0 }
        if !d.isCurrent {
            parts.append(String(localized: "Revised [Todo Decisions]"))
        } else if !d.supersedesID.isEmpty, let replaced = decisions.decision(d.supersedesID) {
            parts.append(String(localized: "Revises “\(replaced.choice)”"))
        }
        return parts.joined(separator: " · ")
    }

    @ViewBuilder private func actions(_ d: TodoDecision) -> some View {
        let candidates = decisions.candidates(for: d)
        Button { decisions.form = TodoDecisionForm(supersedes: d) } label: {
            Text("Revise [Todo Decisions]").lineLimit(1)
        }
        .font(Theme.Font.captionStrong)
        .foregroundStyle(Theme.accent)
        .buttonStyle(.borderless)
        .accessibilityIdentifier("todo.decision.revise." + d.id)
        if !candidates.isEmpty || !d.supersedesID.isEmpty {
            Menu {
                ForEach(candidates) { c in
                    Button { Task { await decisions.markSupersedes(d, old: c.id) } } label: {
                        Text("Revises “\(c.question) → \(c.choice)”").lineLimit(1)
                    }
                }
                if !d.supersedesID.isEmpty {
                    Divider()
                    Button { Task { await decisions.markSupersedes(d, old: "") } } label: {
                        Text("Undo Revision").lineLimit(1)
                    }
                }
            } label: {
                ThemedIcon(Theme.Icon.more)
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, Theme.Space.xs)
                    .contentShape(.rect)
            }
            #if os(macOS)
            .menuStyle(.button)
            .buttonStyle(.borderless)
            .menuIndicator(.hidden)
            #endif
            .fixedSize()
            .accessibilityLabel("More")
            .accessibilityIdentifier("todo.decision.more." + d.id)
        }
    }
}

/// Records a decision on a todo, or one that revises `supersedes` (its question filled in). It closes once saved; a
/// failed save keeps what was typed.
struct TodoDecisionSheet: View {
    @Environment(\.dismiss) private var dismiss
    let supersedes: TodoDecision?
    let save: (_ question: String, _ choice: String, _ reason: String) async -> Bool
    @State private var question: String
    @State private var choice = ""
    @State private var reason = ""
    @State private var saving = false
    @FocusState private var focus: Field?

    private enum Field { case question, choice }

    init(supersedes: TodoDecision?, save: @escaping (_ question: String, _ choice: String, _ reason: String) async -> Bool) {
        self.supersedes = supersedes
        self.save = save
        _question = State(initialValue: supersedes?.question ?? "")
    }

    private func blank(_ s: String) -> Bool { s.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    private var ready: Bool { !blank(question) && !blank(choice) }
    private var unsaved: Bool { !blank(choice) || !blank(reason) || question != (supersedes?.question ?? "") }

    var body: some View {
        NavigationStack {
            Form {
                if let supersedes {
                    Section {
                        Text("The earlier choice “\(supersedes.choice)” stays, struck through; only the new one goes to agents.")
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.secondary)
                    }
                }
                Section("Question [Todo Decisions]") {
                    TextField(String(localized: "What needs deciding…"), text: $question, axis: .vertical)
                        .lineLimit(1...3)
                        .focused($focus, equals: .question)
                }
                Section("Choice [Todo Decisions]") {
                    TextField(String(localized: "What was decided…"), text: $choice, axis: .vertical)
                        .lineLimit(1...4)
                        .focused($focus, equals: .choice)
                }
                Section("Reason (Optional)") {
                    TextField(String(localized: "Why, and what was ruled out…"), text: $reason, axis: .vertical)
                        .lineLimit(2...6)
                }
            }
            .themedPage()
            .navigationTitle(supersedes == nil ? String(localized: "Record a Decision") : String(localized: "Revise [Todo Decisions]"))
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: unsaved) { dismiss() }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await submit() } } label: {
                        if saving {
                            ProgressView()
                        } else {
                            Text(supersedes == nil ? String(localized: "Record [Todo Decisions]") : String(localized: "Revise [Todo Decisions]"))
                                .lineLimit(1)
                        }
                    }
                    .disabled(!ready || saving)
                    .accessibilityIdentifier("todo.decision.save")
                }
            }
            .onAppear { focus = supersedes == nil ? .question : .choice }
        }
        .presentationDetents([.medium, .large])
        .interactiveDismissDisabled(saving)
        #if os(macOS)
        .macSheet()
        #endif
    }

    private func submit() async {
        guard ready, !saving else { return }
        saving = true
        let saved = await save(question, choice, reason)
        saving = false
        if saved { dismiss() }
    }
}
