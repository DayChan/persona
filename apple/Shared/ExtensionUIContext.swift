import Foundation

/// Shared declarative UI context; system extensions need only Shared, not the app's view models or keychain.
nonisolated extension ExtensionUIContext {
    static func here(launcher: String? = nil, machine: String? = nil, item: String? = nil, size: String? = nil) -> Self {
        Self(launcher: launcher, machine: machine, item: item, size: size, tz: TimeZone.current.identifier,
             locale: appLocale, platform: platform, uiVersion: version)
    }

    static let version = 3

    static var platform: String {
        #if os(macOS)
        ExtensionPlatform.mac
        #else
        // The protocol has only four platforms for now; watchOS and visionOS use Apple mobile's ios.
        ExtensionPlatform.ios
        #endif
    }

    static var appLocale: String {
        #if WIDGET_EXTENSION
        let lang = LanguageStore.shared.groupResolved ?? Bundle.main.preferredLocalizations.first ?? "en"
        #else
        let lang = Bundle.main.preferredLocalizations.first ?? Locale.current.language.languageCode?.identifier ?? "zh-Hans"
        #endif
        guard let region = Locale.current.region?.identifier, !lang.contains("-\(region)") else { return lang }
        return lang + "-" + region
    }
}
