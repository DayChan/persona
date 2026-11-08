import Foundation

extension AppModel {
    /// Registry reads that failed before first unlock must not clear an existing protected system snapshot.
    func syncExtensionSurfaces() {
        guard !registryUnreadable, !isDemo else { return }
        let configs = Dictionary(uniqueKeysWithValues: registry.groups.compactMap { group in registry.primary(group).map { (group, $0.config) } })
        let names = Dictionary(uniqueKeysWithValues: registry.groups.map { ($0, groupName($0)) })
        let devices = Dictionary(uniqueKeysWithValues: registry.groups.compactMap { group in registry.primary(group)?.deviceID.map { (group, $0) } })
        try? ExtensionSurfaceSync.credentials(configs, names: names, devices: devices, capabilities: extensionSurfaceCapabilities)
    }

    func refreshExtensionSurfaces() {
        guard !registryUnreadable, !isDemo else { return }
        syncExtensionSurfaces()
        ExtensionSurfaceForegroundRefresh.start()
    }
}

/// Foreground signals can overlap registry changes. Coalesce read-only refreshes; the store fences late replies.
@MainActor private enum ExtensionSurfaceForegroundRefresh {
    static var task: Task<Void, Never>?
    static func start() {
        guard task == nil else { return }
        task = Task {
            await ExtensionSurfaceSync.refresh()
            task = nil
        }
    }
}
