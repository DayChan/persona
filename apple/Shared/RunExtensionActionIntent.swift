#if !WIDGET_EXTENSION
import AppIntents
import Foundation
import SwiftUI

nonisolated struct ExtensionActionEntity: AppEntity, Hashable, Sendable {
    static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Extension Action")
    static let defaultQuery = ExtensionActionQuery()
    var id: String
    var title: String
    var source: String
    var displayRepresentation: DisplayRepresentation { DisplayRepresentation(title: "\(title)", subtitle: "\(source)") }
}

nonisolated struct ExtensionActionQuery: EntityStringQuery {
    func entities(for identifiers: [String]) async throws -> [ExtensionActionEntity] {
        await all().filter { identifiers.contains($0.id) }
    }
    func suggestedEntities() async throws -> [ExtensionActionEntity] { await all() }
    func entities(matching string: String) async throws -> [ExtensionActionEntity] {
        await all().filter { $0.title.localizedCaseInsensitiveContains(string) || $0.source.localizedCaseInsensitiveContains(string) }
    }
    private func all() async -> [ExtensionActionEntity] {
        await ExtensionSurfaceDirectory.refresh()
        return ExtensionSurfaceStore.shared.read().groups.sorted { $0.key < $1.key }.flatMap { group, state in
            state.shortcuts.map { place in
                ExtensionActionEntity(id: ExtensionSurfaceKey(group: group, owner: place.extension, contribution: place.id).id,
                                      title: place.title, source: state.credential.name + " · " + (place.extensionName ?? place.extension))
            }
        }
    }
}

@MainActor struct ExtensionActionChoices: DynamicOptionsProvider {
    @IntentParameterDependency<RunExtensionActionIntent>(\.$action) var intent
    func results() async throws -> [String] {
        guard let id = intent?.action.id, let key = ExtensionSurfaceKey(id: id) else { return [] }
        let place = ExtensionSurfaceStore.shared.read().groups[key.group]?.shortcuts.first { $0.extension == key.owner && $0.id == key.contribution }
        return place?.params?.first { $0.kind == "choice" }?.choices?.map(\.value) ?? []
    }
}

struct RunExtensionActionIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Run Extension Action"
    nonisolated static let description = IntentDescription("Run an action contributed by an extension on the selected hub.")
    nonisolated static var openAppWhenRun: Bool { false }
    @Parameter(title: "Action") var action: ExtensionActionEntity?
    @Parameter(title: "Text") var text: String?
    @Parameter(title: "Number") var number: Double?
    @Parameter(title: "Toggle") var toggle: Bool?
    @Parameter(title: "Choice", optionsProvider: ExtensionActionChoices()) var choice: String?
    @Parameter(title: "JSON Values") var jsonValues: String?
    nonisolated init() {}
    nonisolated static var parameterSummary: some ParameterSummary {
        Summary("Run \(\.$action)") { \.$text; \.$number; \.$toggle; \.$choice; \.$jsonValues }
    }

    #if (os(iOS) || os(visionOS)) && compiler(>=6.4)
    nonisolated func perform() async throws -> some IntentResult & ReturnsValue<String> & ProvidesDialog & ShowsSnippetView {
        let (result, link) = try await run()
        let text = result.text ?? String(localized: "Extension action completed")
        return .result(value: text, dialog: IntentDialog("\(text)"), view: ExtensionActionSnippet(result: result, link: link))
    }
    #else
    nonisolated func perform() async throws -> some IntentResult & ReturnsValue<String> & ProvidesDialog {
        let (result, _) = try await run()
        let text = result.text ?? String(localized: "Extension action completed")
        return .result(value: text, dialog: IntentDialog("\(text)"))
    }
    #endif

    private nonisolated func run() async throws -> (ExtensionShortcutResult, URL?) {
        guard let action else { throw $action.needsValueError() }
        guard let key = ExtensionSurfaceKey(id: action.id),
              ExtensionSystemLink(owner: key.owner, ui: key.contribution, group: key.group).url != nil,
              let group = ExtensionSurfaceStore.shared.read().groups[key.group] else { throw ExtensionActionUnavailable() }
        // No IntentSupport.client fallback: another hub with the same ids must never receive this action.
        let api = APIClient(config: ServerConfig(url: group.credential.url, token: group.credential.token), group: key.group)
        do {
            let features = try await ExtensionSurfaceClient(credential: group.credential)
                .refreshCatalog(group: key.group, expected: group)
            guard features.contains("extension_shortcuts"),
                  let current = ExtensionSurfaceStore.shared.read().groups[key.group], current.generation == group.generation,
                  let place = current.shortcuts.first(where: { $0.extension == key.owner && $0.id == key.contribution }) else {
                throw ExtensionActionUnavailable()
            }
            let values = try ExtensionActionValues.map(place, text: text, number: number, toggle: toggle, choice: choice, json: jsonValues)
            let result = try await api.runExtensionShortcut(place, values: values)
            guard ExtensionSurfaceStore.shared.read().groups[key.group]?.generation == group.generation else { throw ExtensionActionUnavailable() }
            return (result, place.ui.flatMap { ExtensionSystemLink(owner: place.extension, ui: $0, group: key.group).url })
        } catch {
            if let error = error as? APIError, [401, 403].contains(error.status) {
                try? ExtensionSurfaceStore.shared.revoke(key.group, generation: group.generation)
            }
            throw error
        }
    }
}

nonisolated struct ExtensionActionUnavailable: LocalizedError {
    var errorDescription: String? { String(localized: "This extension action is unavailable. Open Persona to sync the selected hub.") }
}

#if (os(iOS) || os(visionOS)) && compiler(>=6.4)
private struct ExtensionActionSnippet: View {
    var result: ExtensionShortcutResult
    var link: URL?
    var body: some View {
        VStack(alignment: .leading, spacing: ExtensionSurfaceTokens.spacing) {
            if let view = result.view { ExtensionSurfaceView(view: view) }
            if let text = result.text { Text(verbatim: text).font(ExtensionSurfaceTokens.body) }
            if let link { Link(destination: link) { Text("Open").lineLimit(1) } }
        }
    }
}
#endif
#endif
