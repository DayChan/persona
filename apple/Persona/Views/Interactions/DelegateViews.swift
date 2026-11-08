import SwiftUI

/// What a question card shows about the assistant (#544, docs/assistant/assistant.md "Assistant answers questions
/// first"): that it is looking at the card, and what it found for you to decide with.
struct DelegateNotes: View {
    var interaction: Interaction

    var body: some View {
        let lines = Delegated.context(interaction).lines
        if interaction.isDelegating || !lines.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if interaction.isDelegating { looking }
                if !lines.isEmpty { context(lines) }
            }
        }
    }

    private var looking: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.view)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text("Assistant is looking")
                    .font(Theme.Font.captionStrong)
                Text("The assistant is deciding whether it can answer this for you and hasn't notified you yet. You can still answer; whichever answer comes first counts.")
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .padding(Theme.Space.m)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .accessibilityElement(children: .combine)
    }

    private func context(_ lines: [DelegateLine]) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Label {
                Text("What the assistant found").lineLimit(1)
            } icon: {
                ThemedIcon(Theme.Icon.ai)
            }
            .font(Theme.Font.captionStrong)
            .foregroundStyle(Theme.ai)
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: Theme.Space.m, verticalSpacing: Theme.Space.xs) {
                ForEach(lines, id: \.field) { line in
                    GridRow {
                        Text(line.label)
                            .foregroundStyle(.secondary)
                            .fixedSize()
                        Text(line.text)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .font(Theme.Font.caption)
                }
            }
        }
        .padding(Theme.Space.m)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }
}

/// The choice the assistant recommends (its `recommend` is that choice's own label).
struct AssistantPickTag: View {
    var body: some View {
        Chip(text: String(localized: "Assistant's pick"), symbol: Theme.Icon.ai, look: .tint(Theme.ai))
    }
}

/// A question the assistant answered, under the chat until it is put away (web `Delegated.tsx`).
struct DelegatedCard: View {
    @Environment(AppModel.self) private var model
    var answer: DelegatedAnswer

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                Label {
                    Text("Answered by the assistant").lineLimit(1)
                } icon: {
                    ThemedIcon(Theme.Icon.ai)
                }
                .font(Theme.Font.labelStrong)
                .foregroundStyle(Theme.ai)
                Spacer(minLength: 0)
                Button {
                    withAnimation(Theme.Motion.standard) { model.dropDelegated(answer.id) }
                } label: {
                    ThemedIcon(Theme.Icon.close)
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text("Close"))
            }
            DelegatedBody(answer: answer, opensChange: true)
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .foregroundStyle(Theme.primaryText)
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        .transition(.move(edge: .bottom).combined(with: .opacity))
    }
}

/// The assistant's answer and reason, your change if you made one, and **Change Answer**.
struct DelegatedBody: View {
    @Environment(AppModel.self) private var model
    var answer: DelegatedAnswer
    var opensChange = false
    @State private var changing = false

    var body: some View {
        let now = Delegated.latest(answer, model.reanswered[answer.id])
        let changed = model.reanswered[answer.id] != nil
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ForEach(answer.interaction.questions ?? []) { q in
                let said = now[q.question] ?? ""
                let first = (answer.answers[q.question] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(q.question)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                    Text(said.isEmpty ? String(localized: "(No answer)") : said)
                        .font(Theme.Font.secondaryStrong)
                    if changed, !first.isEmpty, !Delegated.sameAnswer(q, said, first) {
                        Text("The assistant answered: \(first)")
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                .fixedSize(horizontal: false, vertical: true)
            }
            if !answer.reason.isEmpty {
                Text("Why: \(answer.reason)")
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack(spacing: Theme.Space.s) {
                if changed {
                    Label {
                        Text("You changed it").lineLimit(1)
                    } icon: {
                        ThemedIcon(Theme.Icon.done)
                    }
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(Theme.positive)
                }
                Spacer(minLength: 0)
                if model.canChangeAnswer(answer) {
                    Button { changing = true } label: {
                        Label {
                            Text("Change Answer").lineLimit(1)
                        } icon: {
                            ThemedIcon(Theme.Icon.edit)
                        }
                    }
                    .themedButton()
                    .controlSize(.small)
                }
            }
        }
        .sheet(isPresented: $changing) {
            ReanswerSheet(answer: answer, before: now)
        }
        #if DEBUG
        .task {
            // Screenshots: `-openChangeAnswer YES` opens the form of the chat's card.
            if opensChange, UserDefaults.standard.bool(forKey: "openChangeAnswer") { changing = true }
        }
        #endif
    }
}

/// **Change Answer**: the questions with your answer picked, sending only what you change (web `Delegated.tsx`).
struct ReanswerSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var answer: DelegatedAnswer
    var before: [String: String]
    @State private var picked: [Int: [String]]
    @State private var custom: [Int: String]
    @State private var sending = false
    @State private var error: String?

    init(answer: DelegatedAnswer, before: [String: String]) {
        self.answer = answer
        self.before = before
        let start = Delegated.initialPicks(answer.interaction.questions ?? [], before)
        _picked = State(initialValue: start.picked)
        _custom = State(initialValue: start.custom)
    }

    private var questions: [Question] { answer.interaction.questions ?? [] }
    private var changes: [String: String] {
        Delegated.changedAnswers(before: before,
                                 after: Delegated.formAnswers(questions, picked: picked, custom: custom),
                                 questions: questions)
    }

    var body: some View {
        NavigationStack {
            Form {
                ForEach(Array(questions.enumerated()), id: \.element.question) { i, q in
                    Section {
                        ForEach(q.options, id: \.label) { option(i, q, $0) }
                        TextField("Other answer…", text: customText(i), axis: .vertical)
                            .lineLimit(1...4)
                    } header: {
                        Text(q.question).textCase(nil)
                    } footer: {
                        if i == questions.count - 1 {
                            Text("The agent already went on with the assistant's answer. Your new answer goes to it as a message.")
                        }
                    }
                }
                if let error {
                    Section {
                        Label {
                            Text(error)
                        } icon: {
                            ThemedIcon(Theme.Icon.error)
                        }
                        .foregroundStyle(Theme.danger)
                    }
                }
            }
            .navigationTitle(Text("Change Answer"))
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Text("Cancel").lineLimit(1) }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button { send() } label: {
                        if sending { ProgressView().controlSize(.small) } else { Text("Send").lineLimit(1) }
                    }
                    .disabled(changes.isEmpty || sending)
                }
            }
        }
        #if os(macOS)
        .macSheet()
        #endif
    }

    private func option(_ i: Int, _ q: Question, _ opt: QuestionOption) -> some View {
        let on = picked[i, default: []].contains(opt.label)
        return Button { toggle(i, q, opt.label) } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(QuestionOption.display(opt.label).0)
                        .foregroundStyle(Theme.primaryText)
                    if let d = opt.description, !d.isEmpty {
                        Text(d)
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 0)
                if on {
                    ThemedIcon(Theme.Icon.done)
                        .font(Theme.Font.bodyStrong)
                        .foregroundStyle(Theme.accent)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? .isSelected : [])
    }

    private func toggle(_ i: Int, _ q: Question, _ label: String) {
        var now = picked[i, default: []]
        if q.multiSelect {
            if let at = now.firstIndex(of: label) { now.remove(at: at) } else { now.append(label) }
        } else {
            now = now == [label] ? [] : [label]
        }
        picked[i] = now
        if !now.isEmpty { custom[i] = "" }
    }

    private func customText(_ i: Int) -> Binding<String> {
        Binding {
            custom[i, default: ""]
        } set: { text in
            custom[i] = text
            if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { picked[i] = [] }
        }
    }

    private func send() {
        let sent = changes
        guard !sent.isEmpty else { return }
        sending = true
        error = nil
        Task {
            let failure = await model.reanswer(answer, answers: sent)
            sending = false
            if let failure { error = failure } else { dismiss() }
        }
    }
}

/// Session Info's questions the assistant answered (`GET /interactions/history`, those with `answered_by:
/// "assistant"`). A hub without the history or one that keeps it from this device shows none.
@Observable
final class DelegatedHistoryModel {
    private(set) var rows: [DelegatedAnswer]?
    private(set) var error: String?

    /// `manager`: the session's.
    func load(_ key: SessionKey, manager: String? = nil, api: APIClient?) async {
        guard let api else { return }
        error = nil
        do {
            rows = Delegated.history(try await api.interactionHistory(machine: key.machineID, session: key.sessionID,
                                                                      manager: manager))
        } catch let refused as APIError where refused.status == 403 || refused.status == 404 {
            rows = []
        } catch {
            if let text = error.shown { self.error = text } else { rows = rows ?? [] }
        }
    }
}

struct DelegatedHistorySection: View {
    let page: DelegatedHistoryModel
    let key: SessionKey
    var manager: String?
    let api: APIClient?

    var body: some View {
        if let error = page.error {
            Section {
                Label {
                    Text(error)
                } icon: {
                    ThemedIcon(Theme.Icon.error)
                }
                .font(Theme.Font.caption)
                .foregroundStyle(Theme.danger)
                Button { Task { await page.load(key, manager: manager, api: api) } } label: { Text("Retry").lineLimit(1) }
            } header: {
                Text("Questions the assistant answered")
            }
        } else if let rows = page.rows, !rows.isEmpty {
            Section {
                ForEach(rows) { row in
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        if let at = row.at {
                            Text(Fmt.relative(at))
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                        }
                        DelegatedBody(answer: row)
                    }
                    .padding(.vertical, Theme.Space.xs)
                }
            } header: {
                Text("Questions the assistant answered")
            }
        }
    }
}
