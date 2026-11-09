import SwiftUI

// Settings › Connectors › GitHub › Inbox (docs/assistant/inbox.md「连接器：GitHub」): which CI failures come in, and repositories
// that bring nothing in. Kept on the hub (PUT /api/v1/connectors/github/settings {"options": …}).

/// GitHub's inbox options, as GET /api/v1/connectors gives them.
nonisolated struct GitHubInboxOptions: Codable, Equatable, Sendable {
    var ci: String
    var mutedRepos: [String]
    var ignoreBranches: [String]?

    enum CodingKeys: String, CodingKey {
        case ci
        case mutedRepos = "muted_repos", ignoreBranches = "ignore_branches"
    }

    init?(_ json: JSONValue?) {
        guard let json, let data = try? JSON.encoder.encode(json),
              let o = try? JSON.decoder.decode(GitHubInboxOptions.self, from: data) else { return nil }
        self = o
    }

    /// The CI choices, most quiet first: (value, title).
    static let ciModes: [(String, String)] = [("off", String(localized: "state.off", defaultValue: "Off")), ("default_branch", String(localized: "Default Branch Only")), ("mine", String(localized: "Plus My PRs")), ("all", String(localized: "All"))]
}

extension APIClient {
    /// Saves GitHub's inbox options; the hub answers with the connector as it lists it.
    func setGitHubInboxOptions(_ options: GitHubInboxOptions) async throws -> HubConnector {
        struct Body: Encodable, Sendable { var options: GitHubInboxOptions }
        return try JSON.decoder.decode(HubConnector.self, from: await request("PUT", "/api/v1/connectors/github/settings", body: Body(options: options)))
    }
}

/// The Inbox section of GitHub's page: the CI failures picker and the muted repositories.
struct GitHubInboxSettings: View {
    @Environment(AppModel.self) private var model
    @State var options: GitHubInboxOptions
    /// Whose GitHub (another manager's: the change names it); nil: the default manager's.
    var manager: String? = nil
    var saved: (HubConnector) -> Void
    @State private var newRepo = ""
    @State private var error: String?

    var body: some View {
        Section {
            Picker("CI Failures", selection: Binding(get: { options.ci }, set: { options.ci = $0; save() })) {
                ForEach(GitHubInboxOptions.ciModes, id: \.0) { value, title in Text(title).tag(value) }
            }
            .pickerStyle(.menu)
            ForEach(options.mutedRepos, id: \.self) { repo in
                Label(repo, systemImage: Theme.Icon.muted).lineLimit(1)
            }
            .onDelete { idx in
                options.mutedRepos.remove(atOffsets: idx)
                save()
            }
            HStack {
                TextField(String(localized: "Mute repository: owner/name"), text: $newRepo)
                    #if os(macOS)
                    .labelsHidden() // the title is the prompt (#1221)
                    #endif
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .onSubmit(add)
                Button(action: add) { Text("Add [Beautiful U I Bridge 7a8a11]").lineLimit(1) }
                    .disabled(!newRepo.contains("/"))
            }
            if let error {
                Text(error).font(Theme.Font.label).foregroundStyle(Theme.danger)
            }
        } header: {
            Text("Inbox")
        } footer: {
            Text(footer)
        }
    }

    private var footer: String {
        let ci = switch options.ci {
        case "off": String(localized: "CI failures don't go to the Inbox.")
        case "default_branch": String(localized: "Only CI failures on the default branch go to the Inbox, as Needs you.")
        case "all": String(localized: "CI failures on every branch count as Needs you.")
        default: String(localized: "CI failures on the default branch and your open PRs count as Needs you; other branches are FYI.")
        }
        let auto = (options.ignoreBranches ?? ["train/*"]).joined(separator: String(localized: "、", comment: "Separator between items in a list"))
        return ci + String(localized: " Failures on the same branch are grouped into one item, reruns included; it's marked done automatically once a run passes, the branch is deleted, or the PR is merged. Automated branches (\(auto)) aren't flagged.")
            + String(localized: " @mentions, review requests, and requested changes from people count as Needs you; ones from bots are FYI. Muted repositories send nothing to the Inbox, and existing items are marked done.")
    }

    private func add() {
        let repo = newRepo.trimmingCharacters(in: .whitespaces)
        guard repo.contains("/"), !options.mutedRepos.contains(repo) else { return }
        options.mutedRepos.append(repo)
        newRepo = ""
        save()
    }

    private func save() {
        let o = options
        Task {
            do {
                if let c = try await model.api?.manager(manager).setGitHubInboxOptions(o) {
                    error = nil
                    saved(c)
                }
            } catch {
                self.error = error.shown.map { String(localized: "Couldn't save: \($0) [Git Hub Inbox Settings 902c6a]") }
            }
        }
    }
}
