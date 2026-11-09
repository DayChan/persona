import Foundation

/// Bounded diagnostic attribution, not authentication or a device identity. Only fixed platform/surface names
/// and the public app version are sent: no bundle ids, accounts, host names or installation identifiers.
nonisolated struct RequestClientMetadata: Equatable, Sendable {
    enum Platform: String, Sendable {
        case iOS = "ios", macOS = "macos", watchOS = "watchos", visionOS = "visionos", other

        static var current: Self {
            #if os(watchOS)
            .watchOS
            #elseif os(visionOS)
            .visionOS
            #elseif os(iOS)
            .iOS
            #elseif os(macOS)
            .macOS
            #else
            .other
            #endif
        }
    }

    enum Surface: String, Sendable {
        case app, widget, intent, systemExtension = "extension"

        static func detect(isExtension: Bool, extensionPoint: String?) -> Self {
            if extensionPoint == "com.apple.widgetkit-extension" { return .widget }
            return isExtension ? .systemExtension : .app
        }
    }

    static let headerName = "X-Persona-Client"
    static let current = RequestClientMetadata()
    let value: String
    var headers: [String: String] { [Self.headerName: value, LegacyNames.clientHeader: value] }

    init(platform: Platform = .current, surface: Surface? = nil, version: String? = nil) {
        let bundle = Bundle.main
        let extensions = bundle.object(forInfoDictionaryKey: "NSExtension") as? [String: Any]
        let detected = Surface.detect(isExtension: extensions != nil || bundle.bundleURL.pathExtension == "appex",
                                      extensionPoint: extensions?["NSExtensionPointIdentifier"] as? String)
        let caller = platform.rawValue + "-" + (surface ?? detected).rawValue
        let publicVersion = version ?? Self.version(in: bundle)
        // A slash belongs only between caller and version. Sanitize before constructing any HTTP header.
        let token = publicVersion.utf8.prefix(96).map { byte -> UInt8 in
            switch byte {
            case 65...90, 97...122, 48...57, 46, 95, 43, 45: byte
            default: 45
            }
        }
        value = caller + "/" + (token.isEmpty ? "unknown" : String(decoding: token, as: UTF8.self))
    }

    func apply(to request: inout URLRequest) {
        for (name, value) in headers {
            request.setValue(value, forHTTPHeaderField: name)
        }
    }

    private static func version(in bundle: Bundle) -> String {
        let release = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "unknown"
        guard let build = bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String, !build.isEmpty else { return release }
        return release + "+" + build
    }
}
