import SwiftUI

/// New Agent's model: the launcher's list (`LauncherModels`) and what is picked from it. Picking nothing ("") is the
/// default — what the launcher runs, or Model Settings › New Agent's model when that applies — and sends no model. An ID
/// the list doesn't have can be typed (#1644, Claude Code and Codex take any `--model`): it stays picked and goes as it is.
nonisolated struct LauncherModelChoice: Equatable {
    var list: LauncherModels
    /// The picked model's id; "": the default.
    var picked = ""
    /// Model Settings › New Agent's model when it is what runs with no pick (the launcher is the one it names); "": none.
    var configured = ""
    /// How Default's line names [configured]: Model Settings (New Agent), Inherited (Model Settings itself, where empty inherits).
    var configuredLabel = String(localized: "Model Settings [Launcher Model Picker 8cbcf7]")

    /// A pick can reach the agent: the launcher does not pass its own --model, and there is a list to pick from or an ID
    /// can be typed (Claude Code, Codex: their list may be empty).
    var canPick: Bool { list.locked == nil && (!list.models.isEmpty || takesTyped) }
    /// An ID the list doesn't have can be typed in: "Other Model…" (#1644).
    var takesTyped: Bool { list.takesTypedModel }
    /// New Agent shows the locked row: the launcher passes its own --model where a model could otherwise be picked
    /// or typed (an empty Claude Code / Codex list too; `takesTyped` is off once it is locked).
    var showsLocked: Bool { list.locked != nil && (!list.models.isEmpty || LauncherModels.takesTypedModel(agent: list.agent)) }
    /// The model the agent runs when it is given none.
    var defaultModel: AgentModel? { list.models.first(where: \.isDefault) }
    /// What can be picked besides the default: the agent's own models, then the IDs typed before.
    var choices: [AgentModel] { list.models.filter { !$0.isDefault } }
    /// The IDs typed before that ran here, most recent first (`custom`, remembered by the hub).
    var remembered: [AgentModel] { list.models.filter(\.custom) }
    var selected: AgentModel? { picked.isEmpty ? nil : list.models.first { $0.id == picked } }

    /// The `model` a start sends: nil for the default; a typed ID the list lacks goes as it is when the agent takes one.
    var sent: String? {
        guard list.locked == nil, !picked.isEmpty else { return nil }
        if let m = selected { return m.isDefault ? nil : picked }
        return takesTyped && ModelID.isValid(picked) ? picked : nil
    }

    /// The typed ID that is picked (not in the list) and will be sent; nil: none.
    var typed: String? { selected == nil && sent != nil ? picked : nil }

    /// The row's title: the picked model's name, the typed ID, else Default.
    var title: String {
        selected.map(\.displayName) ?? typed.map { SessionModes.gptModelName($0) ?? $0 } ?? String(localized: "Default [Prompt Composer 844b8c]")
    }
    /// The row's second line: the picked model's (`AgentModel.detail`), that a typed ID is one, or what the default runs.
    var detail: String? {
        if let m = selected { return m.custom ? String(localized: "Typed model ID") : m.detail }
        if let id = typed { return id != title ? String(localized: "Typed model ID: \(id)") : String(localized: "Typed model ID") }
        return defaultDetail
    }
    /// What runs with no pick: Model Settings' model, or the one the agent defaults to.
    var defaultDetail: String? {
        if !configured.isEmpty { return String(localized: "\(configuredLabel): \(SessionModes.gptModelName(configured) ?? configured)") }
        return defaultModel.map { model in
            if let resolved = model.resolved { return SessionModes.gptModelName(resolved) ?? resolved }
            return model.displayName
        }
    }

    /// The thinking efforts the model that will run takes; nil: not known (every level stays offered): a typed ID's.
    var efforts: [String]? {
        if unlisted != nil { return nil }
        let running = selected ?? (configured.isEmpty ? defaultModel : list.models.first { $0.id == configured })
        guard let running, !running.custom else { return nil }
        return running.efforts ?? []
    }

    /// What is typed, as an id to use when no model has exactly that id and the hub would take it (`ModelID`); nil: none.
    func customModelID(_ query: String) -> String? {
        let id = ModelID.cleaned(query)
        guard ModelID.isValid(id), !list.models.contains(where: { $0.id == id }) else { return nil }
        return id
    }

    /// Why what is typed can't be used as a model ID; nil: it can, it is listed, or nothing is typed.
    func customProblem(_ query: String) -> String? {
        let id = ModelID.cleaned(query)
        guard !id.isEmpty, !list.models.contains(where: { $0.id == id }) else { return nil }
        return ModelID.problem(id)?.text
    }

    /// The picked id when the list does not have it (Model Settings' from before, or one typed in); nil: none.
    var unlisted: String? { !picked.isEmpty && selected == nil ? picked : nil }

    /// Models whose name, id, resolved model or description has the query.
    func matching(_ query: String) -> [AgentModel] {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return choices }
        return choices.filter { m in
            [m.displayName, m.name, m.id, m.resolved ?? "", m.description ?? ""].contains { $0.localizedCaseInsensitiveContains(q) }
        }
    }
}

extension AgentModel {
    /// A separate name is a custom alias and takes precedence over GPT id formatting. Stored/wire fields stay raw.
    nonisolated var displayName: String {
        let raw = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let bareID = Fmt.model(id) ?? id
        guard raw.isEmpty || raw.caseInsensitiveCompare(id) == .orderedSame
                || raw.caseInsensitiveCompare(bareID) == .orderedSame else { return name }
        return SessionModes.gptModelName(id) ?? (raw.isEmpty ? id : name)
    }

    /// Its second line: the id when the displayed name is another word for it, and the model an alias stands for.
    nonisolated var detail: String? {
        var parts: [String] = []
        if id != displayName { parts.append(id) }
        if let r = resolved, r != name, r != id { parts.append("→ \(r)") }
        return parts.isEmpty ? nil : parts.joined(separator: " ")
    }
}

/// The list behind New Agent's Model row: Default first, then every model the launcher's agent offers, the IDs typed
/// before (each can be forgotten), and "Other Model…" for one the list doesn't have (#1644); searchable.
struct LauncherModelPicker: View {
    var choice: LauncherModelChoice
    @Binding var picked: String
    /// Asks the CLI again (pull to refresh).
    var refresh: () async -> Void
    /// Model Settings: an id the list does not have can be used (what is typed), and the current one stays shown.
    var allowsCustom = false
    /// New Agent: "Other Model…" opens a field for a model ID (`TypedModelSheet`), and a typed pick stays shown.
    var offersTyping = false
    /// Forgets an ID typed before (`DELETE …/models/custom`); nil: those rows can't be forgotten here.
    var forget: ((String) async -> Void)? = nil
    /// Why the last Forget didn't go through.
    var forgetError: String? = nil
    /// Under Default: what picking nothing means here.
    var defaultFooter = String(localized: "No model chosen: runs with the launcher's own settings.")
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var typing = false
    /// What "Other Model…" gave back: picked once its sheet is gone, then this page closes too.
    @State private var typedPick: String?

    var body: some View {
        let shown = choice.matching(query)
        let own = shown.filter { !$0.custom }
        let remembered = shown.filter(\.custom)
        let typingOffered = offersTyping && choice.takesTyped
        List {
            if query.isEmpty {
                Section {
                    row(title: String(localized: "Default [Prompt Composer 844b8c]"), detail: choice.defaultDetail, description: nil, id: "")
                    if allowsCustom || typingOffered, let id = choice.unlisted {
                        row(title: SessionModes.gptModelName(id) ?? id,
                            detail: typingOffered ? String(localized: "Typed model ID: \(id)") : String(localized: "Current setting, not in the list: \(id)"),
                            description: nil, id: id, mono: typingOffered)
                    }
                } footer: {
                    Text(defaultFooter)
                }
            }
            Section {
                ForEach(own) { m in
                    row(title: m.displayName, detail: m.detail, description: m.description, id: m.id)
                }
            } header: {
                if !own.isEmpty { Text("\(own.count) models") }
            }
            if !remembered.isEmpty {
                Section {
                    ForEach(remembered) { m in
                        rememberedRow(m)
                    }
                } header: {
                    Text("Typed Before")
                } footer: {
                    if let forgetError {
                        Text(forgetError).foregroundStyle(Theme.danger).accessibilityIdentifier("newChat.model.forgetError")
                    } else if forget != nil {
                        Text("Model IDs you typed that ran on this computer. Swipe left on one to forget it.")
                    }
                }
            }
            if allowsCustom || typingOffered, let id = choice.customModelID(query) {
                Section {
                    row(title: String(localized: "Use “\(id)”"), detail: nil,
                        description: allowsCustom ? String(localized: "Not in the list; save this ID as is") : String(localized: "Not in the list; this start uses the ID as typed"), id: id)
                }
            } else if allowsCustom || typingOffered, let problem = choice.customProblem(query), shown.isEmpty {
                Section {
                    Label(problem, systemImage: Theme.Icon.error)
                        .font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
                        .accessibilityIdentifier("newChat.model.problem")
                }
            } else if shown.isEmpty && !query.isEmpty {
                ContentUnavailableView.search(text: query)
            }
            if typingOffered, query.isEmpty {
                Section {
                    Button { typing = true } label: {
                        Label { Text(choice.list.models.contains { !$0.isDefault && !$0.custom } ? String(localized: "Other Model…") : String(localized: "Enter Model ID…")).lineLimit(1) }
                            icon: { ThemedIcon(Theme.Icon.keyboard) }
                    }
                    .accessibilityIdentifier("newChat.model.other")
                } footer: {
                    Text("A model the list doesn't show, by its ID, such as one your gateway serves.")
                }
            }
        }
        .pageSearchable(text: $query, prompt: allowsCustom || typingOffered ? String(localized: "Search or enter a model ID") : String(localized: "Name or ID"))
        .refreshable { await refresh() }
        .themedPage()
        .navigationTitle("Model [Assistant View c98e11]")
        #if os(iOS) || os(visionOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .sheet(isPresented: $typing, onDismiss: {
            guard let id = typedPick else { return }
            typedPick = nil
            picked = id
            dismiss()
        }) {
            TypedModelSheet(initial: choice.unlisted ?? "", note: String(localized: "Used for this start only. Once it runs, it's listed under Typed Before.")) { id in
                typedPick = id
            }
        }
    }

    /// `mono`: a model ID typed by hand, drawn as the field and the other lists draw one.
    private func row(title: String, detail: String?, description: String?, id: String, mono: Bool = false) -> some View {
        Button {
            picked = id
            Haptics.tap()
            dismiss()
        } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(mono ? Theme.Font.heading.monospaced() : Theme.Font.heading).foregroundStyle(.primary)
                        .lineLimit(1).truncationMode(mono ? .middle : .tail)
                    if let detail {
                        Text(detail).font(Theme.Font.label.monospaced()).foregroundStyle(.secondary)
                            .lineLimit(1).truncationMode(.middle)
                    }
                    if let description, !description.isEmpty {
                        Text(description).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(2)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if id == picked {
                    ThemedIcon(Theme.Icon.done).foregroundStyle(Theme.accent).fontWeight(.semibold)
                }
            }
            .contentShape(.rect)
        }
        .accessibilityIdentifier("newChat.model.\(id.isEmpty ? "default" : id)")
    }

    /// An ID typed before: picked like the others; swiped (or right-clicked on a Mac) it can be forgotten.
    @ViewBuilder private func rememberedRow(_ m: AgentModel) -> some View {
        let r = row(title: m.displayName, detail: m.id != m.displayName ? m.id : nil, description: nil, id: m.id, mono: true)
        if let forget {
            r.swipeActions(edge: .trailing, allowsFullSwipe: true) {
                Button(role: .destructive) { Task { await forget(m.id) } } label: { Label("Forget", systemImage: Theme.Icon.delete) }
                    .accessibilityIdentifier("newChat.model.forget.\(m.id)")
            }
            .contextMenu {
                Button(role: .destructive) { Task { await forget(m.id) } } label: { Label("Forget", systemImage: Theme.Icon.delete) }
            }
        } else {
            r
        }
    }
}

/// "Other Model…" (#1644): a model ID typed by hand, checked as it is typed with the hub's rule (`ModelID`, the reason
/// under the field), and the IDs typed before on this computer and launcher under it: tap one to use it, swipe it to
/// forget it. New Agent's picker lists those itself and only asks for the field.
struct TypedModelSheet: View {
    /// The computer and launcher whose typed IDs are listed (and can be forgotten); nil: the field alone.
    var machine: MachineRef? = nil
    var launcher: String? = nil
    var initial = ""
    /// Under the rule: what using it does here.
    var note: String? = nil
    var use: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var text = ""
    @State private var models = LauncherModelsModel()
    @FocusState private var focused: Bool

    private var id: String { ModelID.cleaned(text) }
    /// Why the field's ID can't be used; nil while empty (the rule is shown instead) or fine.
    private var problem: String? { id.isEmpty ? nil : ModelID.problem(id)?.text }
    private var canUse: Bool { !id.isEmpty && problem == nil && models.list?.locked == nil }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(String(localized: "Model ID"), text: $text, prompt: Text(verbatim: "vendor/model-name"))
                        .font(Theme.Font.body.monospaced())
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .submitLabel(.done)
                        .focused($focused)
                        .onSubmit(commit)
                        .accessibilityIdentifier("typedModel.field")
                } footer: {
                    if let problem {
                        Label(problem, systemImage: Theme.Icon.error)
                            .foregroundStyle(Theme.cautionText)
                            .accessibilityIdentifier("typedModel.problem")
                    } else {
                        Text([ModelID.rule, note].compactMap { $0 }.joined(separator: "\n"))
                            .accessibilityIdentifier("typedModel.rule")
                    }
                }
                if let locked = models.list?.locked {
                    Section {
                        Label(locked, systemImage: Theme.Icon.locked).foregroundStyle(.secondary)
                            .accessibilityIdentifier("typedModel.locked")
                    }
                }
                if !models.remembered.isEmpty, let machine, let launcher {
                    Section {
                        ForEach(models.remembered) { m in
                            Button {
                                Haptics.tap()
                                use(m.id)
                                dismiss()
                            } label: {
                                HStack(spacing: Theme.Space.m) {
                                    Text(m.displayName).font(Theme.Font.body.monospaced()).foregroundStyle(.primary).lineLimit(1).truncationMode(.middle)
                                    Spacer(minLength: Theme.Space.s)
                                    if m.id == id { ThemedIcon(Theme.Icon.done).foregroundStyle(Theme.accent).fontWeight(.semibold) }
                                }
                                .contentShape(.rect)
                            }
                            .disabled(models.list?.locked != nil)
                            .accessibilityIdentifier("typedModel.remembered.\(m.id)")
                            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                                Button(role: .destructive) { Task { await models.forget(m.id, machine: machine, launcher: launcher) } } label: {
                                    Label("Forget", systemImage: Theme.Icon.delete)
                                }
                            }
                            .contextMenu {
                                Button(role: .destructive) { Task { await models.forget(m.id, machine: machine, launcher: launcher) } } label: {
                                    Label("Forget", systemImage: Theme.Icon.delete)
                                }
                            }
                        }
                    } header: {
                        Text("Typed Before")
                    } footer: {
                        if let error = models.forgetError {
                            Text(error).foregroundStyle(Theme.danger).accessibilityIdentifier("typedModel.forgetError")
                        } else {
                            Text("Model IDs you typed that ran on this computer. Swipe left on one to forget it.")
                        }
                    }
                }
            }
            .themedPage()
            .navigationTitle("Other Model")
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: false) { dismiss() }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button(action: commit) { Text("Use [Typed Model Sheet 5d2e7a]").lineLimit(1) }
                        .bold()
                        .disabled(!canUse)
                        .accessibilityIdentifier("typedModel.use")
                }
            }
        }
        #if os(macOS)
        .macSheet()
        #endif
        .presentationDetents([.medium, .large])
        .onAppear {
            if text.isEmpty { text = initial }
            focused = true
        }
        .task {
            if let machine, let launcher { await models.load(machine, launcher: launcher) }
        }
    }

    private func commit() {
        guard canUse else { return }
        Haptics.tap()
        use(id)
        dismiss()
    }
}
