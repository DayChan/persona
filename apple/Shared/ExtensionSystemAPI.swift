import Foundation

/// System-level placements exist only on the new paths; reuses the schema types without pulling the app's view models into system extensions.
#if !WIDGET_EXTENSION
nonisolated extension APIClient {
    func extensionWidgets() async throws -> [ExtensionWidgetPlace] {
        let answer: ExtensionWidgetPlaces = try await get("/api/v1/extensions/-/widgets")
        return answer.widgets
    }

    func extensionWidget(_ place: ExtensionWidgetPlace, family: String) async throws -> ExtensionWidgetAnswer {
        guard place.families.contains(family), ExtensionWidgetFamily.all.contains(family) else {
            throw APIError(status: 400, message: String(localized: "This extension doesn't support this widget size"))
        }
        let request = ExtensionWidgetRequest(context: .here(size: family))
        return try JSON.decoder.decode(ExtensionWidgetAnswer.self, from: try await self.request(
            "POST", "/api/v1/extensions/\(place.extension)/-/widget/\(place.id)", body: request))
    }

    func reportExtensionWidgets(device: String, widgets: [ExtensionDeviceWidget]) async throws {
        _ = try await request("PUT", "/api/v1/devices/\(device)/widgets", body: ExtensionDeviceWidgets(widgets: widgets))
    }

    func extensionShortcuts() async throws -> [ExtensionShortcutPlace] {
        let answer: ExtensionShortcutPlaces = try await get("/api/v1/extensions/-/shortcuts")
        return answer.shortcuts
    }

    func runExtensionShortcut(_ place: ExtensionShortcutPlace, values: [String: String]) async throws -> ExtensionShortcutResult {
        try JSON.decoder.decode(ExtensionShortcutResult.self, from: try await request(
            "POST", "/api/v1/extensions/\(place.extension)/-/shortcut/\(place.id)",
            body: ExtensionShortcutRequest(values: values, context: .here()), timeout: 30))
    }
}
#endif

nonisolated extension ExtensionWidgetAnswer {
    var expiration: Date? {
        JSON.parseDate(expiresAt)
    }
}
