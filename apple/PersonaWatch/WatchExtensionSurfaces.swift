import Foundation
import SwiftUI

nonisolated struct WatchExtensionDestination: Identifiable {
    var link: ExtensionSystemLink
    var id: String { link.url?.absoluteString ?? "" }
}

private nonisolated struct WatchExtensionLoad: Hashable {
    var link: ExtensionSystemLink
    var config: ServerConfig?
    var features: [String]?
}

extension WatchModel {
    /// This is the wrist's own configuration, not the phone's registry; empty replaces and clears all groups.
    func syncExtensionSurfaces() {
        try? ExtensionSurfaceSync.credentials(configs, names: names, devices: extensionDeviceIDs,
                                             answerOnly: true, capabilities: extensionCapabilities)
        if let destination = extensionSurface, configs[destination.link.group] == nil { extensionSurface = nil }
    }
    func refreshExtensionSurfaces() async {
        syncExtensionSurfaces()
        await ExtensionSurfaceSync.refresh()
    }
    func openExtensionSurface(_ url: URL) {
        guard let link = ExtensionSystemLink(url: url), configs[link.group] != nil else { return }
        extensionSurface = WatchExtensionDestination(link: link)
    }
}

/// A complication tap opens only its group's widget projection, never a full page or interactive controls.
struct WatchExtensionSurfacePage: View {
    var destination: WatchExtensionDestination
    @Environment(WatchModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var view: ExtensionView?
    @State private var family = "large"
    @State private var failed = false
    @State private var needsSync = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if let view { ExtensionSurfaceView(view: view, family: family) }
                else if failed { Text("This extension page is unavailable").font(Theme.Font.body) }
                else { ProgressView() }
                if needsSync { Text("Open Persona on iPhone to sync extension capabilities.").font(Theme.Font.caption) }
                Text("Open Persona on iPhone to use extension controls").font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            .padding(Theme.Space.s)
        }
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button { dismiss() } label: { Text("Done").lineLimit(1) }
            }
        }
        .task(id: WatchExtensionLoad(link: destination.link, config: model.configs[destination.link.group],
                                    features: model.extensionCapabilities[destination.link.group])) {
            view = nil; failed = false; needsSync = false
            let store = ExtensionSurfaceStore.shared
            let link = destination.link
            guard let config = model.configs[link.group], let group = store.read().groups[link.group],
                  group.credential.url == config.url, group.credential.token == config.token else { failed = true; return }
            let client = ExtensionSurfaceClient(credential: group.credential)
            if let cached = store.read().projection(link) {
                family = cached.family
                view = cached.cache?.answer.view
            }
            do { _ = try await client.refreshCatalog(group: link.group, expected: group) }
            catch is ExtensionSurfaceCapabilitiesUnknown { if !Task.isCancelled { needsSync = true } }
            catch { }
            guard !Task.isCancelled else { return }
            guard model.configs[link.group] == config,
                  store.read().groups[link.group]?.generation == group.generation,
                  let projection = store.read().projection(link) else { view = nil; failed = true; return }
            let cache = await client.widget(projection.key, family: projection.family)
            guard !Task.isCancelled else { return }
            guard model.configs[link.group] == config,
                  store.read().groups[link.group]?.generation == group.generation else { view = nil; return }
            family = projection.family
            view = cache?.answer.view
            failed = view == nil
        }
    }
}
