import Foundation
#if os(iOS)
import UIKit
#endif

/// Permissions of the iPhone system integrations (#2276–#2286): alarms (AlarmKit), Apple Music (MusicKit) and Home
/// (HomeKit) ask iOS; messages, calls and shortcuts need nothing from iOS (the user acts in a system sheet or prompt).
/// None of them exists on a Mac or Vision Pro (`PhoneDataSource.iPhoneOnly`).
enum SystemAccess {
    static func status(_ source: PhoneDataSource) -> PhoneAuthStatus {
        #if os(iOS)
        switch source {
        case .alarms: AlarmsAccess.status()
        case .media: MediaAccess.status()
        case .home: HomeAccess.status()
        case .messages, .calls, .shortcuts: .notNeeded
        default: PhoneAuthorization.status(source)
        }
        #else
        .denied
        #endif
    }

    static func request(_ source: PhoneDataSource) async -> PhoneAuthStatus {
        #if os(iOS)
        switch source {
        case .alarms: await AlarmsAccess.request()
        case .media: await MediaAccess.request()
        case .home: await HomeAccess.request()
        case .messages, .calls, .shortcuts: .notNeeded
        default: await PhoneAuthorization.request(source)
        }
        #else
        .denied
        #endif
    }
}

#if os(iOS)
/// What the tools that hand over to iOS need: Persona on screen (iOS opens a call prompt, Maps or Shortcuts only for
/// the app in front, and a compose sheet hangs off Persona's window).
enum Foreground {
    static var isActive: Bool { UIApplication.shared.applicationState == .active }

    /// Throws the sentence the model passes on when Persona is not in front.
    static func require(_ what: String) throws {
        guard isActive else {
            throw ToolFailure(.unavailable, "Persona is not open on the iPhone, so it cannot \(what). "
                + "Ask the user to open Persona and try again.")
        }
    }

    /// The view controller on top of Persona's active window, to present a system sheet from.
    static func topController() -> UIViewController? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let scene = scenes.first { $0.activationState == .foregroundActive } ?? scenes.first
        guard var top = scene?.keyWindow?.rootViewController ?? scene?.windows.first?.rootViewController else { return nil }
        while let next = top.presentedViewController, !next.isBeingDismissed { top = next }
        return top
    }

    /// Opens a URL in another app; false when iOS would not (the app is missing, or Persona is not in front).
    static func open(_ url: URL) async -> Bool {
        await UIApplication.shared.open(url)
    }
}
#endif

/// One-line wording of a time on an approval card: the phone's locale and time zone, not raw ISO 8601 (#1876).
nonisolated enum CardTime {
    static func text(_ date: Date, allDay: Bool = false) -> String {
        allDay ? date.formatted(date: .abbreviated, time: .omitted) : date.formatted(date: .abbreviated, time: .shortened)
    }

    static func range(_ start: Date, _ end: Date, allDay: Bool = false) -> String {
        "\(text(start, allDay: allDay)) – \(text(end, allDay: allDay))"
    }
}
