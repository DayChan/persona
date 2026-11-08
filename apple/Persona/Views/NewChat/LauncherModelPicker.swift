import SwiftUI

/// New Agent's model: the launcher's list (`LauncherModels`) and what is picked from it. Picking nothing ("") is the
/// default — what the launcher runs, or Model Settings › New Agent's model when that applies — and sends no model.
nonisolated struct LauncherModelChoice: Equatable {
    var list: LauncherModels
    /// The picked model's id; "": the default.
    var picked = ""
    /// Model Settings › New Agent's model when it is what runs with no pick (the launcher is the one it names); "": none.
    var configured = ""
    /// How Default's line names [configured]: Model Settings (New Agent), Inherited (Model Settings itself, where empty inherits).
    var configuredLabel = String(localized: "Model Settings [Launcher Model Picker 8cbcf7]")

    /// A pick can reach the agent: there is a list and the launcher does not pass its own --model.
    var canPick: Bool { list.locked == nil && !list.models.isEmpty }
    /// The model the agent runs when it is given none.
    var defaultModel: AgentModel? { list.models.first(where: \.isDefault) }
    /// What can be picked besides the default.
    var choices: [AgentModel] { list.models.filter { !$0.isDefault } }
    var selected: AgentModel? { picked.isEmpty ? nil : list.models.first { $0.id == picked } }

    /// The `model` a start sends: nil for the default.
    var sent: String? { canPick && selected?.isDefault == false ? picked : nil }

    /// The row's title: the picked model's name, else Default.
    var title: String { selected.map(\.displayName) ?? String(localized: "Default [Prompt Composer 844b8c]") }
    /// The row's second line: the picked model's (`AgentModel.detail`), or what the default runs.
    var detail: String? {
        if let m = selected { return m.detail }
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

    /// The thinking efforts the model that will run takes; nil: not known (every level stays offered).
    var efforts: [String]? {
        let running = selected ?? (configured.isEmpty ? defaultModel : list.models.first { $0.id == configured })
        guard let running else { return nil }
        return running.efforts ?? []
    }

    /// What is typed, as an id to use when no model has exactly that id (Model Settings takes ids the list lacks); nil: none.
    func customModelID(_ query: String) -> String? {
        let id = query.trimmingCharacters(in: .whitespaces)
        guard !id.isEmpty, !id.contains(where: \.isWhitespace), !list.models.contains(where: { $0.id == id }) else { return nil }
        return id
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

/// The list behind New Agent's Model row: Default first, then every model the launcher's agent offers, searchable.
struct LauncherModelPicker: View {
    var choice: LauncherModelChoice
    @Binding var picked: String
    /// Asks the CLI again (pull to refresh).
    var refresh: () async -> Void
    /// Model Settings: an id the list does not have can be used (what is typed), and the current one stays shown.
    var allowsCustom = false
    /// Under Default: what picking nothing means here.
    var defaultFooter = String(localized: "No model chosen: runs with the launcher's own settings.")
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    var body: some View {
        let shown = choice.matching(query)
        List {
            if query.isEmpty {
                Section {
                    row(title: String(localized: "Default [Prompt Composer 844b8c]"), detail: choice.defaultDetail, description: nil, id: "")
                    if allowsCustom, let id = choice.unlisted {
                        row(title: SessionModes.gptModelName(id) ?? id, detail: String(localized: "Current setting, not in the list: \(id)"), description: nil, id: id)
                    }
                } footer: {
                    Text(defaultFooter)
                }
            }
            Section {
                ForEach(shown) { m in
                    row(title: m.displayName, detail: m.detail, description: m.description, id: m.id)
                }
            } header: {
                if !shown.isEmpty { Text("\(shown.count) models") }
            }
            if allowsCustom, let id = choice.customModelID(query) {
                Section {
                    row(title: String(localized: "Use “\(id)”"), detail: nil, description: String(localized: "Not in the list; save this ID as is"), id: id)
                }
            } else if shown.isEmpty && !query.isEmpty {
                ContentUnavailableView.search(text: query)
            }
        }
        .pageSearchable(text: $query, prompt: allowsCustom ? String(localized: "Search or enter a model ID") : String(localized: "Name or ID"))
        .refreshable { await refresh() }
        .themedPage()
        .navigationTitle("Model [Assistant View c98e11]")
        #if os(iOS) || os(visionOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
    }

    private func row(title: String, detail: String?, description: String?, id: String) -> some View {
        Button {
            picked = id
            Haptics.tap()
            dismiss()
        } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.heading).foregroundStyle(.primary).lineLimit(1)
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
}
