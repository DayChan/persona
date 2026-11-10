#if os(iOS)
import UIKit

/// The home screen icon's long-press menu (#3199): New Chat, Agents and Inbox, as Android's shortcuts and the web app's
/// manifest have. They only open a page; with no hub paired the app stays on its welcome page. Record a Meeting starts
/// recording too (docs/apple/recording.md「Meeting recordings」), with or without a hub: the text syncs once there is one.
enum QuickAction: String, CaseIterable {
    case meetingRecording = "persona.meeting-recording"
    case newConversation = "persona.new-conversation"
    case agents = "persona.agents"
    case inbox = "persona.inbox"

    var item: UIApplicationShortcutItem {
        let (title, symbol): (String, String) = switch self {
        case .meetingRecording: (String(localized: "Record a Meeting"), Theme.Icon.recording)
        case .newConversation: (String(localized: "New Chat [Quick Actions ea4b29]"), Theme.Icon.compose)
        case .agents: ("Agents", Theme.Icon.agents)
        case .inbox: (String(localized: "Inbox"), Theme.Icon.inbox)
        }
        return UIApplicationShortcutItem(type: rawValue, localizedTitle: title, localizedSubtitle: nil,
                                         icon: UIApplicationShortcutIcon(systemImageName: symbol))
    }

    /// Set at launch: the menu is the same for everyone, so it needs no updating.
    static func publish() {
        UIApplication.shared.shortcutItems = allCases.map(\.item)
    }

    /// The item the menu was tapped with: true when it was one of these.
    @MainActor @discardableResult
    static func perform(_ item: UIApplicationShortcutItem) -> Bool {
        guard let action = QuickAction(rawValue: item.type) else { return false }
        let model = AppModel.shared
        model.shellSettings = false
        switch action {
        case .meetingRecording:
            Task { await MeetingRecorder.shared.startMeeting(inForeground: true) }
        case .newConversation:
            model.selectedTab = .assistant
            // As the drawer's New Chat: the shell takes Assistant back to its root (a pushed inbox included), the old tabs only
            // need the conversation gone.
            if let shell = model.tabRouter as? ShellState {
                shell.show(.newConversation)
                shell.drawer.close()
            } else {
                model.assistantCurrent = nil
            }
        case .agents: model.selectedTab = .chats
        case .inbox: model.selectedTab = .mail
        }
        return true
    }
}

/// The scene's delegate only for the menu: a tap while the app runs or is in the background comes here; one that
/// launched it comes with the scene's connection options. SwiftUI still owns the window.
final class QuickActionSceneDelegate: NSObject, UIWindowSceneDelegate {
    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options connectionOptions: UIScene.ConnectionOptions) {
        if let item = connectionOptions.shortcutItem { Task { @MainActor in QuickAction.perform(item) } }
    }

    func windowScene(_ windowScene: UIWindowScene, performActionFor shortcutItem: UIApplicationShortcutItem,
                     completionHandler: @escaping (Bool) -> Void) {
        Task { @MainActor in completionHandler(QuickAction.perform(shortcutItem)) }
    }
}
#endif
