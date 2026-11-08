import AppIntents
import Foundation

/// Dynamic configuration choices carry the hub group in their identity, not just the extension's local id.
nonisolated struct ExtensionWidgetEntity: AppEntity, Hashable, Sendable {
    static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Extension Widget")
    static let defaultQuery = ExtensionWidgetQuery()
    var id: String
    var title: String
    var source: String
    var displayRepresentation: DisplayRepresentation { DisplayRepresentation(title: "\(title)", subtitle: "\(source)") }
}

nonisolated struct ExtensionWidgetQuery: EntityStringQuery {
    func entities(for identifiers: [String]) async throws -> [ExtensionWidgetEntity] {
        let all = await ExtensionSurfaceDirectory.widgets()
        return all.filter { identifiers.contains($0.id) }
    }
    func suggestedEntities() async throws -> [ExtensionWidgetEntity] { await ExtensionSurfaceDirectory.widgets() }
    func entities(matching string: String) async throws -> [ExtensionWidgetEntity] {
        await ExtensionSurfaceDirectory.widgets().filter { $0.title.localizedCaseInsensitiveContains(string) || $0.source.localizedCaseInsensitiveContains(string) }
    }
}

nonisolated enum ExtensionSurfaceDirectory {
    static func refresh(store: ExtensionSurfaceStore = .shared) async {
        await withTaskGroup(of: Void.self) { tasks in
            for (group, state) in store.read().groups {
                tasks.addTask {
                    _ = try? await ExtensionSurfaceClient(credential: state.credential).refreshCatalog(group: group, expected: state, store: store)
                }
            }
        }
    }
    static func widgets() async -> [ExtensionWidgetEntity] {
        await refresh()
        return ExtensionSurfaceStore.shared.read().groups.sorted { $0.key < $1.key }.flatMap { group, state in
            state.widgets.map { place in
                ExtensionWidgetEntity(id: ExtensionSurfaceKey(group: group, owner: place.extension, contribution: place.id).id,
                                      title: place.title, source: state.credential.name + " · " + (place.extensionName ?? place.extension))
            }
        }
    }
}
