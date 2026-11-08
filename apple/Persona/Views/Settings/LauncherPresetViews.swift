import SwiftUI

/// Add Provider › From Extension (docs/extensions/extensions.md「模型供应商」): the providers the hub's plugins offer (and its built-in
/// ones, by their display name), then only the name, the key and maybe another of the provider's models; the address and
/// the kind come from the preset.
struct LauncherPresetSections: View {
    @Bindable var model: LauncherPresetModel

    var body: some View {
        Section {
            ForEach(model.presets) { p in row(p) }
        } header: {
            Text("Provider")
        } footer: {
            if let p = model.selected {
                // A built-in one is the hub's own, not an extension called "builtin" (#3621).
                if p.isBuiltin {
                    Text("The address, connection type and models are already filled in; once added, you can edit it like one you set up yourself.")
                } else {
                    Text("The address, connection type, and models come from the “\(p.plugin)” extension. Once added, you can edit it like one you set up yourself.")
                }
            }
        }
        Section {
            TextField(String(localized: "Name, e.g. jv_team"), text: $model.name)
                .font(Theme.Font.body.monospaced())
                .textInputAutocapitalization(.never).autocorrectionDisabled()
                .accessibilityIdentifier("launcher.preset.name")
        } header: {
            Text("Name [Add Computer View 677331]")
        } footer: {
            if let problem = model.nameProblem {
                Text(problem).foregroundStyle(Theme.danger)
            } else {
                Text("Letters, digits, _, and -. This is the name you pick the provider by.")
            }
        }
        Section {
            SecretField("API Key", filled: String(localized: "Filled in; tap Save [Extension Market Views 94348c]"), text: $model.apiKey, identifier: "launcher.preset.key")
        } header: {
            Text("Key")
        } footer: {
            Text([model.selected?.keyHint, String(localized: "The key is stored encrypted on the hub and never sent back to clients.")].compactMap { $0 }.joined(separator: "\n"))
        }
        if let p = model.selected {
            Section {
                if p.choices.isEmpty {
                    TextField(String(localized: "Default Model"), text: $model.model).textInputAutocapitalization(.never).autocorrectionDisabled()
                } else {
                    Picker("Default Model", selection: $model.model) {
                        ForEach(p.choices) { m in Text(m.label).tag(m.id) }
                    }
                }
            } header: {
                Text("Default Model")
            } footer: {
                if let e = p.modelsError, !e.isEmpty {
                    Text("Couldn't load the extension's model list (\(e)). These are the models it declares.")
                } else {
                    Text("Choices for each use are managed in “Model Configuration”. Uses without their own choice get the default here.")
                }
            }
        }
        if let error = model.error {
            Section { Label(error, systemImage: Theme.Icon.error).font(Theme.Font.label).foregroundStyle(Theme.danger) }
        }
    }

    private func row(_ p: LauncherPreset) -> some View {
        let picked = model.selectedID == p.id
        return Button { model.selectedID = p.id } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(p.title).font(Theme.Font.body).foregroundStyle(.primary).lineLimit(1)
                    if let d = p.description, !d.isEmpty {
                        Text(d).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                    }
                    Text(LauncherPresetText.subtitle(p)).font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1)
                }
                Spacer(minLength: Theme.Space.s)
                if picked {
                    ThemedIcon(Theme.Icon.done).font(Theme.Font.labelStrong).foregroundStyle(Theme.accent)
                }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(picked ? .isSelected : [])
        .accessibilityIdentifier("launcher.preset.\(p.id)")
    }
}
