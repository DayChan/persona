import SwiftUI

extension HostWorkspace {
    nonisolated var preset: SpacePreset { SpacePreset(machineID: machineRef.path, cwd: cwd, workspaceID: sourceID, spaceName: title, host: source) }

    /// Empty containers and deep descendants use the workspace identity, never an Agent's inferred space.
    nonisolated var creationGroup: SpaceGroup {
        SpaceGroup(id: key, machineID: machineID, spaceID: sourceID, group: group, name: title, index: index ?? 0, sessions: [])
    }

    nonisolated func createdWorkspace(_ made: WorktreeCreated) -> HostWorkspace {
        HostWorkspace(machineID: machineID, source: source, sourceID: made.workspaceID, title: made.branch,
                      cwd: made.path, branch: made.branch, parentSourceID: made.parentWorkspaceID ?? sourceID, group: group)
    }
}

struct WorkspaceChoiceRow: View {
    var workspace: HostWorkspace
    var catalog: [HostWorkspace]

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(workspace.title).foregroundStyle(Color.primary).lineLimit(1)
            Text(workspace.breadcrumb(in: catalog)).font(Theme.Font.caption).foregroundStyle(Color.secondary).lineLimit(2)
            Text(workspace.cwd).font(Theme.Font.caption).foregroundStyle(Color.secondary).lineLimit(1).truncationMode(.middle)
        }
    }
}

/// Callers scope choices to one machine and source; names and directories are labels, never the selected identity.
struct WorkspacePicker: View {
    var workspaces: [HostWorkspace]
    var selectedID: String?
    var allowDirectory = false
    var onPick: (HostWorkspace?) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    private var shown: [HostWorkspace] {
        workspaces.filter { query.isEmpty || ($0.breadcrumb(in: workspaces) + " " + $0.cwd).localizedStandardContains(query) }
            .sorted { $0.breadcrumb(in: workspaces).localizedStandardCompare($1.breadcrumb(in: workspaces)) == .orderedAscending }
    }

    var body: some View {
        List {
            if allowDirectory {
                Button { onPick(nil); dismiss() } label: {
                    Label(String(localized: "workspace.useDirectory", defaultValue: "Choose by Folder"), systemImage: Theme.Icon.folder)
                }
            }
            ForEach(shown) { workspace in
                Button { onPick(workspace); dismiss() } label: {
                    HStack(spacing: Theme.Space.m) {
                        WorkspaceChoiceRow(workspace: workspace, catalog: workspaces)
                        Spacer(minLength: Theme.Space.xs)
                        if selectedID == workspace.sourceID { Image(systemName: Theme.Icon.success).foregroundStyle(Theme.accent) }
                    }
                }
                .disabled(!workspace.available)
                .accessibilityIdentifier("workspace.choice")
            }
        }
        .themedGroupedPage()
        .navigationTitle(String(localized: "workspace.choose", defaultValue: "Choose Worktree"))
        .searchable(text: $query)
    }
}
