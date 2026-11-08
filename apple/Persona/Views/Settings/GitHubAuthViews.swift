import SwiftUI

// Settings › Connectors › GitHub's sign-in (docs/extensions/connectors.md「二 · 7」): where the token comes from — one of the user's
// computers' gh login, or the device flow with the GitHub CLI's built-in OAuth App — chosen here and kept on the hub (hub.json's
// github.auth.from_machine is only the default). Choosing one leaves the other, so there is no Sign Out. When the token
// in use lacks a scope the connector needs, the page says which and what does not work.

/// GET /api/v1/connectors/github/auth.
nonisolated struct GitHubAuth: Codable, Equatable, Sendable {
    nonisolated struct Missing: Codable, Equatable, Sendable {
        var scope: String
        var affects: [String]
    }

    nonisolated struct Machine: Codable, Equatable, Sendable, Identifiable {
        var id: String
        var name: String
        var state: String // signed_in | not_signed_in | no_gh | too_old | offline | error
        var account: String?
        var scopes: [String]?
        var missing: [Missing]?
        var error: String?
    }

    var source: String // machine | device
    var machine: String?
    var machines: [Machine]
    var scopes: [String]?
    var scopesKnown: Bool?
    var missing: [Missing]?

    enum CodingKeys: String, CodingKey {
        case source, machine, machines, scopes, missing
        case scopesKnown = "scopes_known"
    }

    /// The scopes a token lacks, as people read them, with the language's list separator: repo, notifications.
    static func names(_ missing: [Missing]) -> String { missing.map(\.scope).joined(separator: String(localized: "、", comment: "Separator between items in a list")) }
}

extension APIClient {
    func githubAuth() async throws -> GitHubAuth { try await get("/api/v1/connectors/github/auth") }

    /// Chooses where GitHub's token comes from: a machine's gh login, or the device flow (machine nil).
    func setGitHubAuth(machine: String?) async throws -> GitHubAuth {
        struct Body: Encodable, Sendable { var source: String; var machine: String? }
        let body = Body(source: machine == nil ? "device" : "machine", machine: machine)
        return try JSON.decoder.decode(GitHubAuth.self, from: await request("PUT", "/api/v1/connectors/github/auth", body: body))
    }
}

/// GitHub's sections under its status: the switch, where to sign in from, the device flow when chosen, what its
/// token cannot do, and deleting what it brought in.
struct GitHubConnectorSections: View {
    var connector: HubConnector
    var reload: () async -> Void
    @Environment(AppModel.self) private var app
    @State private var model = ConnectorSignInModel(id: "github")
    @State private var auth: GitHubAuth?
    /// The row being chosen (a machine's id, "device"), while the hub applies it.
    @State private var choosing: String?

    var body: some View {
        ConnectorSwitchSection(connector: connector, model: model, reload: reload)
        sourceSection
        if auth?.source == "device", connector.isEnabled, !connector.isSignedIn {
            ConnectorSignInSteps(name: connector.name, model: model, reload: { await reload(); await load() },
                                 footer: String(localized: "Device-code authorization is stored encrypted on the hub; it's forgotten when you switch to gh on a computer."))
        }
        if let auth, let missing = auth.missing, !missing.isEmpty, connector.isSignedIn {
            scopeSection(auth, missing)
        }
        ConnectorFeedsSection(connector: connector, model: model, reload: reload)
        ConnectorDataSection(connector: connector, model: model, reload: reload, showsLogout: false)
    }

    private var sourceSection: some View {
        Section {
            if let auth {
                ForEach(auth.machines) { m in
                    row(key: m.id, icon: Theme.Icon.machine, title: m.name, subtitle: subtitle(m),
                        warn: m.state != "signed_in" || !(m.missing ?? []).isEmpty, selected: auth.source == "machine" && auth.machine == m.id) {
                        choose(machine: m.id)
                    }
                }
                row(key: "device", icon: Theme.Icon.deviceCode, title: String(localized: "Device Code Sign-In"), subtitle: String(localized: "No computer needed: enter a code on GitHub to authorize"),
                    warn: false, selected: auth.source == "device") {
                    choose(machine: nil)
                }
            } else {
                HStack(spacing: Theme.Space.s) {
                    ProgressView()
                    Text("Checking gh sign-in on your computers…").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                }
            }
        } header: {
            SettingsHeading(String(localized: "Sign In From"))
        } footer: {
            Text("Pick a computer to use its gh sign-in; device code sign-in doesn't depend on any computer. Switching signs out of the previous one.")
        }
        .task(id: app.config) { await load() }
    }

    private func row(key: String, icon: String, title: String, subtitle: String, warn: Bool, selected: Bool,
                     action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(icon).foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    Text(subtitle).font(Theme.Font.label).foregroundStyle(warn ? Theme.cautionText : Color.secondary).lineLimit(2)
                }
                Spacer(minLength: Theme.Space.s)
                if choosing == key {
                    ProgressView()
                } else if selected {
                    ThemedIcon(Theme.Icon.done).foregroundStyle(Theme.accent)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(choosing != nil)
        .accessibilityIdentifier("github.source." + key)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func subtitle(_ m: GitHubAuth.Machine) -> String {
        switch m.state {
        case "signed_in":
            let who = m.account.map { String(localized: "gh signed in · \($0)") } ?? String(localized: "gh signed in")
            if let missing = m.missing, !missing.isEmpty { return String(localized: "\(who) · missing \(GitHubAuth.names(missing)) scope") }
            return who
        case "not_signed_in": return String(localized: "gh not signed in")
        case "no_gh": return String(localized: "gh not installed")
        case "too_old": return String(localized: "personad is too old to read gh sign-in")
        case "offline": return String(localized: "Offline [Machine Entity be1b4f]")
        default: return m.error.map { String(localized: "Can't read gh sign-in: \($0)") } ?? String(localized: "Can't read gh sign-in")
        }
    }

    @ViewBuilder private func scopeSection(_ auth: GitHubAuth, _ missing: [GitHubAuth.Missing]) -> some View {
        let names = GitHubAuth.names(missing)
        let flags = missing.map(\.scope).joined(separator: ",")
        let machine = auth.machines.first { $0.id == auth.machine }?.name ?? String(localized: "This computer")
        Section {
            Label {
                Text(auth.source == "device"
                     ? String(localized: "Device code sign-in is missing the \(names) scope: switch to another sign-in method, then sign in with a device code again.")
                     : String(localized: "gh on \(machine) is missing the \(names) scope: run gh auth refresh -s \(flags) on that computer, or use device code sign-in."))
                    .font(Theme.Font.body).textSelection(.enabled)
            } icon: {
                ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.caution)
            }
            .accessibilityIdentifier("github.missingScopes")
            ForEach(missing.flatMap(\.affects), id: \.self) { a in
                Text(a).font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        } header: {
            SettingsHeading(String(localized: "Missing Permissions"))
        }
    }

    private func load() async {
        guard let api = app.api else { return }
        do {
            auth = try await api.githubAuth()
        } catch {
            app.show(error)
        }
    }

    private func choose(machine: String?) {
        guard let api = app.api else { return }
        choosing = machine ?? "device"
        Task {
            defer { choosing = nil }
            do {
                auth = try await api.setGitHubAuth(machine: machine)
                model.cancel()
                await reload()
            } catch {
                app.show(error)
            }
        }
    }
}
