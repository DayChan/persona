import SwiftUI

/// A container has one display name; it never borrows an Agent's tab or conversation title.
struct RenameWorktreeSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var workspace: HostWorkspace
    var renamed: () async -> Void
    @State private var name = ""
    @State private var busy = false
    @State private var failure: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    WorktreeInput(title: String(localized: "worktree.name", defaultValue: "Worktree name"), text: $name)
                        .accessibilityIdentifier("worktree.rename.name")
                }
                if let failure { Section { Text(failure).foregroundStyle(Theme.danger) } }
            }
            #if os(macOS)
            .formStyle(.grouped)
            #endif
            .themedPage()
            .navigationTitle(String(localized: "worktree.rename", defaultValue: "Rename Worktree…"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() }.disabled(busy) }
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await save() } } label: {
                        Text("Save")
                            #if os(iOS) || os(visionOS)
                            .foregroundStyle(.white)
                            #endif
                    }
                        .formButton(prominent: true)
                        .disabled(busy || name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        .accessibilityIdentifier("worktree.rename.save")
                }
            }
            .disabled(busy)
        }
        .onAppear { name = workspace.title }
        .interactiveDismissDisabled(busy)
        #if os(macOS)
        .macSheet()
        #endif
    }

    private func save() async {
        guard let api = model.api(workspace.group) else { return }
        busy = true
        defer { busy = false }
        do {
            try await api.nodeAction(machine: workspace.machineID, "worktree/rename",
                                     ["workspace_id": workspace.sourceID, "kind": workspace.source, "name": name])
            await renamed()
            dismiss()
        } catch { failure = error.shown ?? error.localizedDescription }
    }
}

nonisolated enum SessionRenameForm {
    /// Only the legacy herdr/Claude dialog edits both a tab and the conversation. Other runtimes expose one name.
    static func showsTab(_ session: Session) -> Bool {
        session.paneID != nil && (session.hostKind ?? "herdr") == "herdr" && (session.agent ?? "claude") == "claude"
    }

    static func body(_ session: Session, tab: String, title: String) -> APIClient.RenameBody {
        APIClient.RenameBody(tabName: showsTab(session) && tab != (session.slotName ?? "") ? tab : nil,
                             title: title != session.title ? title : nil)
    }
}
