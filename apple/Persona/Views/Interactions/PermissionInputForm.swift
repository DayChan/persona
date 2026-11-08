import SwiftUI

struct PermissionInputForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @State private var values: [String: String] = [:]
    @State private var sending = false

    private var complete: Bool {
        (interaction.fields ?? []).allSatisfy {
            $0.optional == true || !(values[$0.key] ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text(interaction.summary ?? String(localized: "Confirm settings change"))
                .font(Theme.Font.secondary)
            ForEach(interaction.fields ?? []) { field in
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    Text(field.label).font(Theme.Font.labelStrong)
                    Group {
                        if field.secret == true {
                            // A key or a password handed to the Agent, not this app's: never offered to the system's
                            // passwords (#1598).
                            SecretField(field.placeholder ?? field.label, filled: String(localized: "Filled in"), text: binding(field.key),
                                        identifier: "interaction.field." + field.key)
                                .onSubmit { submit(allow: true) }
                        } else {
                            TextField(field.placeholder ?? field.label, text: binding(field.key))
                                .onSubmit { submit(allow: true) }
                        }
                    }
                    .interactionField()
                    .accessibilityIdentifier("interaction.field." + field.key)
                }
            }
            HStack(spacing: Theme.Space.m) {
                Button { submit(allow: false) } label: { Text("Deny").lineLimit(1).frame(maxWidth: .infinity) }
                    .themedButton()
                Button { submit(allow: true) } label: { Text("Fill In & Allow").lineLimit(1).frame(maxWidth: .infinity) }
                    .themedButton(prominent: true)
                    .disabled(!complete)
            }
            .disabled(sending)
        }
        .onDisappear { values = [:] }
        .onChange(of: interaction.id) { values = [:] }
    }

    private func binding(_ key: String) -> Binding<String> {
        Binding(get: { values[key] ?? "" }, set: { values[key] = $0 })
    }

    private func submit(allow: Bool) {
        guard !sending, !allow || complete else { return }
        sending = true
        let resolution = Resolution(allow: allow, values: allow ? values : nil)
        values = [:]
        Task {
            await model.resolve(interaction, resolution)
            sending = false
        }
    }
}

extension View {
    /// A card's text field (`PermissionInputForm`, `SecretRunForm`): body text on the well, as typed.
    func interactionField() -> some View {
        font(Theme.Font.body)
            .textFieldStyle(.plain)
            .autocorrectionDisabled()
            .padding(Theme.Space.m)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control))
    }
}
