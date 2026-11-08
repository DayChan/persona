import SwiftUI

/// The password card (`SecretRun`): why the password is needed (the computer is in the card's title,
/// `Interaction.secretRunTitle`), every command verbatim (read-only), one password field, Decline and Run. The password
/// leaves only with Run, inside the answer; this view holds it no longer than it is shown: it is cleared on Run and
/// Decline and when the card goes away. Another card never reuses it (`PermissionForm` gives each card its own view).
/// Without commands in `tool_input` the card's summary stands in for them (it repeats the computer, the purpose and
/// every command).
/// Only the iOS app (iPhone, and iPad running it) takes the password. The Mac and Vision Pro show the same card with a
/// pointer to the iPhone and Decline alone: no field, no Run.
struct SecretRunForm: View {
    @Environment(AppModel.self) private var model
    var interaction: Interaction
    @State private var password = ""
    @State private var sending = false

    /// The card's one field, `password` (the hub declares it).
    private var field: InteractionField? { interaction.fields?.first }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if let request = SecretRunRequest(interaction.toolInput) {
                if !request.purpose.isEmpty {
                    Text(request.purpose)
                        .font(Theme.Font.secondary).lineHeight(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                commands(request.commands)
            } else {
                codeBox(Text(interaction.summary ?? ""))
            }
            #if os(iOS)
            passwordField(field)
            Text("Used once, never saved. The assistant never sees it.")
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            #else
            Text("Fill this in on your iPhone to allow it")
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            #endif
            HStack(spacing: Theme.Space.m) {
                Button { submit(allow: false) } label: {
                    Text("Decline").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton()
                #if os(iOS)
                Button { submit(allow: true) } label: {
                    Text("Run").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
                .disabled(password.isEmpty)
                .accessibilityIdentifier("secret-run.run")
                #endif
            }
            .disabled(sending)
            // The panel's scroll area clips at the form's edge: the glass buttons' rims, drawn just outside them, stay inside.
            .padding(.horizontal, Theme.Space.xxs)
            .padding(.bottom, Theme.Space.xxs)
        }
        .onDisappear { password = "" }
    }

    /// The commands as they will run: one per line, wrapped rather than cut, selectable, never editable.
    private func commands(_ list: [String]) -> some View {
        codeBox(VStack(alignment: .leading, spacing: Theme.Space.s) {
            ForEach(Array(list.enumerated()), id: \.offset) { _, command in
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                    Text(verbatim: "$").foregroundStyle(.secondary).accessibilityHidden(true)
                    Text(verbatim: command)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        })
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("secret-run.commands")
    }

    /// What the card runs, read-only, in the code face on the code background.
    private func codeBox(_ content: some View) -> some View {
        content
            .font(Theme.Font.code)
            .textSelection(.enabled)
            .padding(Theme.Space.m)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }

    #if os(iOS)
    /// Named by its placeholder (the field's label): the note under it says how the password is used, so the card
    /// keeps its Run in sight under three wrapped commands. Return only puts the keyboard away: the commands run with
    /// the computer's password, so they run on a tap on Run, never on a key pressed while typing.
    private func passwordField(_ field: InteractionField?) -> some View {
        // The password of the user's computer, not this app's: never offered to the system's passwords (#1598).
        SecretField(field?.label ?? String(localized: "Password"), text: $password,
                    identifier: "interaction.field." + (field?.key ?? "password"))
            .interactionField()
    }
    #endif

    private func submit(allow: Bool) {
        guard !sending, !allow || !password.isEmpty else { return }
        sending = true
        let resolution = allow ? Resolution(allow: true, values: [field?.key ?? "password": password]) : Resolution(allow: false)
        password = ""
        Task {
            await model.resolve(interaction, resolution)
            sending = false
        }
    }
}
