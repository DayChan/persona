import Foundation

/// A phone capability Claude can call through the hub (`phone_*` MCP tools).
struct PhoneTool {
    var spec: ToolSpec
    var source: PhoneDataSource
    /// Writes (creating events, reminders, …) ask the user in the app before running.
    var confirmation: ((JSONValue) -> String)?
    var run: (JSONValue) async throws -> [ToolContent]
}

/// Groups of tools the user can switch on or off in Settings › Connectors › This iPhone (was Phone Data).
enum PhoneDataSource: String, CaseIterable, Identifiable, Codable {
    case contacts, calendar, reminders, location, health, photos, device, clipboard, notifications

    var id: String { rawValue }

    /// The sources this device has tools for, in order (Settings › Phone Data).
    static var onThisDevice: [PhoneDataSource] {
        #if os(macOS)
        allCases.filter { $0 != .health } // no health data on a Mac (PhoneToolRegistry.healthTools)
        #else
        allCases
        #endif
    }

    var title: String {
        switch self {
        case .contacts: String(localized: "Contacts")
        case .calendar: String(localized: "Calendar")
        case .reminders: String(localized: "Reminders")
        case .location: String(localized: "Location")
        case .health: String(localized: "Health")
        case .photos: String(localized: "Photos")
        case .device: String(localized: "Device Info")
        case .clipboard: String(localized: "Clipboard")
        case .notifications: String(localized: "Notifications")
        }
    }

    var symbol: String {
        switch self {
        case .contacts: Theme.Icon.person
        case .calendar: "calendar"
        case .reminders: "checklist"
        case .location: "location"
        case .health: "heart"
        case .photos: Theme.Icon.photos
        case .device: "iphone"
        case .clipboard: Theme.Icon.paste
        case .notifications: Theme.Icon.notifications
        }
    }

    /// The iPhone app the data comes from (its icon in Settings › Connectors › This iPhone); nil: no app of its own, `symbol`.
    var appIcon: String? {
        switch self {
        case .contacts: Theme.Brand.iosContacts
        case .calendar: Theme.Brand.iosCalendar
        case .reminders: Theme.Brand.iosReminders
        case .location: Theme.Brand.iosMaps
        case .health: Theme.Brand.iosHealth
        case .photos: Theme.Brand.iosPhotos
        case .device, .clipboard, .notifications: nil
        }
    }

    var detail: String {
        switch self {
        case .contacts: String(localized: "Look up contacts' phone numbers, emails, and companies")
        case .calendar: String(localized: "View events, and create them once you confirm")
        case .reminders: String(localized: "View and create reminders")
        case .location: String(localized: "Current location and address")
        case .health: String(localized: "Summaries of steps, heart rate, sleep, and more; saves once you confirm")
        case .photos: String(localized: "Recent photos and screenshots")
        case .device: String(localized: "Battery, storage, OS version, network")
        case .clipboard: String(localized: "Read and write the clipboard")
        case .notifications: String(localized: "Let Claude send you local notifications")
        }
    }

    private var defaultsKey: String { "phoneSource.\(rawValue)" }

    /// The switch was set once (turned on or off), not left at its default.
    var hasChoice: Bool { UserDefaults.standard.object(forKey: defaultsKey) != nil }

    /// On and allowed: what the switch in Settings shows and what is declared to the hub (2026-11-01: the switch says both,
    /// no separate Allowed label). One turned on before it was allowed stays off until it is.
    var isActive: Bool { isEnabled && PhoneAuthorization.status(self).isUsable }

    var isEnabled: Bool {
        get { UserDefaults.standard.object(forKey: defaultsKey) as? Bool ?? true }
        nonmutating set { UserDefaults.standard.set(newValue, forKey: defaultsKey) }
    }
}

enum PhoneToolError: LocalizedError {
    case denied(String)
    case invalidArgument(String)
    case unavailable(String)

    /// Where the user turns a source on. iOS shows its prompt only once, so a source refused before can only be
    /// allowed in the iOS Settings app.
    nonisolated static let grantHint = "请在 iPhone 的 Persona › 设置 › 连接器 › 这台 iPhone 里打开；以前拒绝过的，到 iOS 设置 › Persona 里允许。" // i18n-ignore

    var errorDescription: String? {
        switch self {
        case .denied(let s): "没有权限：\(s)。\(Self.grantHint)" // i18n-ignore
        case .invalidArgument(let s): "参数错误：\(s)" // i18n-ignore
        case .unavailable(let s): s
        }
    }
}
