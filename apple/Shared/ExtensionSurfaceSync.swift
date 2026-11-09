#if !WIDGET_EXTENSION
import Foundation
#if canImport(WidgetKit)
import WidgetKit
#endif

/// The containing app owns credential changes; refresh only updates directories and reports actual configurations.
nonisolated enum ExtensionSurfaceSync {
    static func export(_ configs: [String: ServerConfig], names: [String: String], devices: [String: String] = [:],
                       answerOnly: Bool = false, capabilities: [String: [String]] = [:]) -> [String: ExtensionSurfaceCredential] {
        Dictionary(uniqueKeysWithValues: configs.map { group, config in
            (group, ExtensionSurfaceCredential(url: config.url, token: config.token, deviceID: devices[group], name: names[group] ?? group,
                                               answerOnly: answerOnly, capabilities: capabilities[group].map { Array(Set($0)).sorted() }))
        })
    }

    static func credentials(_ configs: [String: ServerConfig], names: [String: String], devices: [String: String] = [:],
                            answerOnly: Bool = false, capabilities: [String: [String]] = [:],
                            store: ExtensionSurfaceStore = .shared) throws {
        let credentials = export(configs, names: names, devices: devices, answerOnly: answerOnly, capabilities: capabilities)
        guard store.read().groups.mapValues(\.credential) != credentials else { return }
        try store.replaceCredentials(credentials)
        #if os(iOS) || os(watchOS) || os(visionOS)
        WidgetCenter.shared.reloadTimelines(ofKind: ExtensionSystemWidget.kind)
        #endif
    }

    static func refresh(store: ExtensionSurfaceStore = .shared) async {
        #if os(iOS) || os(watchOS) || os(visionOS)
        // Failure to enumerate means unknown, not empty: do not clear the hub's previous full report.
        let configurations = try? await ExtensionWidgetReconciliation.configurations()
        #endif
        for (group, state) in store.read().groups {
            let api = APIClient(config: ServerConfig(url: state.credential.url, token: state.credential.token), group: group)
            do {
                let features = try await ExtensionSurfaceClient(credential: state.credential)
                    .refreshCatalog(group: group, expected: state, store: store)
                guard let catalog = store.read().groups[group], catalog.generation == state.generation else { continue }
                let w = catalog.widgets
                #if os(iOS) || os(watchOS) || os(visionOS)
                guard features.contains("extension_widgets") else { continue }
                if let configurations, let current = store.read().groups[group], current.generation == state.generation {
                    for (key, family) in configurations where key.group == group {
                        guard let place = w.first(where: { $0.extension == key.owner && $0.id == key.contribution && $0.families.contains(family) }),
                              store.read().cache(key, family: family)?.fresh(at: .now) != true else { continue }
                        do {
                            let answer = try await api.extensionWidget(place, family: family)
                            try store.save(ExtensionSurfaceCache(key: key, family: family, locale: ExtensionUIContext.appLocale,
                                                                timeZone: TimeZone.current.identifier, answer: answer), expected: current)
                        } catch {
                            if let error = error as? APIError, [401, 403].contains(error.status) {
                                try? store.revoke(group, generation: current.generation)
                                break
                            }
                        }
                    }
                }
                // The phone migrates legacy watch tokens. An answer-only wrist cannot list /auth/tokens.
                let deviceID = state.credential.deviceID
                if let configurations, let device = deviceID,
                   store.read().groups[group]?.generation == state.generation {
                    let report = store.read().reports(configurations)[group] ?? []
                    try await api.reportExtensionWidgets(device: device, widgets: report)
                }
                #endif
            } catch {
                if let error = error as? APIError, [401, 403].contains(error.status) {
                    try? store.revoke(group, generation: state.generation)
                }
            }
        }
        #if os(iOS) || os(watchOS) || os(visionOS)
        WidgetCenter.shared.reloadTimelines(ofKind: ExtensionSystemWidget.kind)
        #endif
    }
}
#endif
