import SwiftUI

/// Successful manager snapshots survive a transient read failure. No synthetic Agent is ever added to the inventory.
@MainActor @Observable final class WorkspaceCatalog {
    private(set) var workspaces: [HostWorkspace] = []
    private(set) var revision: UInt64 = 0
    @ObservationIgnored private var groups: [String] = []
    @ObservationIgnored private var requestRevision: [String: UInt64] = [:]
    @ObservationIgnored private var generation: UInt64 = 0

    func refresh(_ groups: [String], api: (String) -> APIClient?) async {
        guard !Task.isCancelled else { return }
        generation &+= 1
        let generation = generation
        var seen = Set<String>()
        let requestedGroups = groups.filter { seen.insert($0).inserted }
        self.groups = requestedGroups
        requestRevision = requestRevision.filter { seen.contains($0.key) }
        for group in requestedGroups { requestRevision[group] = generation }
        publish(Dictionary(grouping: workspaces, by: \.group))
        for group in requestedGroups {
            guard !Task.isCancelled else { return }
            guard requestRevision[group] == generation, let client = api(group) else { continue }
            do {
                var rows = try await client.workspaces()
                guard !Task.isCancelled else { return }
                // Polling and creation/rename refreshes can overlap. An older response must neither undo a newer
                // snapshot nor restore a hub removed while its request was in flight.
                guard requestRevision[group] == generation else { continue }
                for i in rows.indices { rows[i].group = group }
                var snapshots = Dictionary(grouping: workspaces, by: \.group)
                snapshots[group] = rows
                publish(snapshots)
            } catch {
                // Old hubs and an unavailable manager retain the existing session-only/last-good view.
            }
        }
    }

    private func publish(_ snapshots: [String: [HostWorkspace]]) {
        let next = groups.flatMap { snapshots[$0] ?? [] }
        if next != workspaces {
            workspaces = next
            revision &+= 1
        }
    }
}

struct WorkspaceCatalogPolling: ViewModifier {
    @Environment(AppModel.self) private var model
    let catalog: WorkspaceCatalog
    let enabled: Bool
    let covered: Bool

    func body(content: Content) -> some View {
        let groups = model.groupIDs
        content.tabActiveTask(id: [String(enabled), String(covered)] + groups) {
            guard enabled, !covered else { return }
            while !Task.isCancelled {
                await catalog.refresh(groups) { model.api($0) }
                do { try await Task.sleep(for: .seconds(10)) } catch { return }
            }
        }
    }
}
