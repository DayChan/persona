import SwiftUI

/// The ChatGPT-style shell on an iPhone (docs/apple/ios.md「New shell」): Assistant (the Persona assistant) and Agents (the Agents list)
/// under one top switch, the drawer on the left (Inbox, Scheduled Tasks, Knowledge, Machines, the conversations, Settings), Settings as a sheet.
///
/// The pages read this from the environment (`\.shell`): nil means a regular-width iPad / iPhone Duo,
/// Mac or Vision Pro, which keep their platform navigation.
///
/// Which page is in front has one answer, `page` (docs/apple/shell.md): the switch's segments, which page takes taps and is
/// read by VoiceOver, `tabIsActive` and the drawer all read it, and the strip's position follows it. It changes only
/// through `select` (the switch, a drag that settles) and `route` (anything that sets `AppModel.selectedTab`), both at
/// once, not when a view is next drawn: the shell's `onChange(of: selectedTab)` was skipped now and then, the strip
/// stayed on Assistant with the tab on Agents, and Agents became the menu of an empty list of options (2026-11-02).
@MainActor @Observable final class ShellState: TabRouter {
    /// The drawer is the shell's (it used to be the Persona tab's own): it slides both pages aside.
    let drawer = AssistantDrawer()
    /// Assistant and Agents side by side: a drag sideways moves from one to the other (`ShellPager`).
    let pager = ShellPager()
    #if os(iOS) || os(visionOS)
    /// The status row's link state and toasts (`ShellStatusRow`).
    let notices = ShellNotices()
    #endif
    /// The page in front: 0 = Assistant, 1 = Agents, 2 = optional Messages.
    private(set) var page = 0
    /// Agents is built the first time it is selected or dragged in (TabView builds a tab when shown), then kept. Not
    /// before: hidden, its controls are still in the accessibility tree (its glass and list are UIKit).
    private(set) var agentsBuilt = false
    /// Assistant likewise. Built while the app opened on Agents, its conversation appeared there unseen: subscribed, shown,
    /// marked seen, and whatever hangs off `onAppear` ran a second time at launch (2026-11-02, `ShellLaunchTests`).
    private(set) var assistantBuilt = false
    private(set) var feishuBuilt = false
    var feishuAtRoot = true
    /// What the drawer or the home page asked the Assistant page to show; `serial` makes the same request count twice.
    private(set) var request: (page: ShellPage, serial: Int)?
    /// Each page says whether it is at its root: the drawer's drag only opens there (pushed pages swipe back).
    var chatAtRoot = true {
        didSet {
            #if DEBUG
            if chatAtRoot != oldValue { ShellTrace.shared.note("chatRoot \(chatAtRoot)") }
            #endif
        }
    }
    var workAtRoot = true {
        didSet {
            #if DEBUG
            if workAtRoot != oldValue { ShellTrace.shared.note("workRoot \(workAtRoot)") }
            #endif
        }
    }
    @ObservationIgnored private weak var model: AppModel?
    /// The first route lands on its page without sliding there.
    @ObservationIgnored private var routed = false

    /// `page`: where the app opens (its tab), so the page that is not in front is not built at launch.
    init(page: Int = 0, pageCount: Int = 2) {
        let page = min(max(0, page), max(1, pageCount) - 1)
        self.page = page
        pager.setPageCount(pageCount)
        feishuBuilt = page == 2
        agentsBuilt = page == 1
        assistantBuilt = page == 0
        pager.slide(to: page, animated: false)
        pager.settled = { [unowned self] p in select(p) }
        pager.began = { [unowned self] in
            agentsBuilt = true; assistantBuilt = true
            if pager.pageCount > 2 { feishuBuilt = true }
        }
    }

    /// The page in front, with the root of the page in front (pushed pages swipe back instead of opening the drawer).
    var atRoot: Bool { page == 2 ? feishuAtRoot : page == 1 ? workAtRoot : chatAtRoot }

    func setPageCount(_ count: Int) {
        pager.setPageCount(count)
        if page >= pager.pageCount { select(0, animated: false) }
    }

    static func tab(for page: Int) -> AppTab {
        #if os(iOS) || os(visionOS)
        if page == 2 { return .feishu }
        #endif
        return page == 1 ? .chats : .assistant
    }
    static func index(for tab: AppTab) -> Int {
        #if os(iOS) || os(visionOS)
        if tab == .feishu { return 2 }
        #endif
        return tab == .chats ? 1 : 0
    }

    func show(_ page: ShellPage) {
        request = (page, (request?.serial ?? 0) + 1)
    }

    /// Takes the app's tab from now on: every `AppModel.selectedTab` lands here as it is set.
    func attach(to model: AppModel) {
        self.model = model
        model.tabRouter = self
        route(model.selectedTab, in: model)
        #if os(iOS) || os(visionOS)
        // The status row takes the link's state as it is set, like the tab (the toasts float, `ToastWindow`).
        model.connection.onShown = { [weak notices] in notices?.linkChanged($0) }
        notices.linkChanged(model.connection.shown)
        #endif
    }

    func detach(from model: AppModel) {
        // Only while this shell is still the one on screen: a new one may have taken over already.
        guard model.tabRouter === self else { return }
        model.tabRouter = nil
        #if os(iOS) || os(visionOS)
        model.connection.onShown = nil
        #endif
    }

    /// `p` in front (0 = Assistant, 1 = Agents): the switch, a drag that settled there, a route. The strip slides there and
    /// the app's tab says so. Taps that come while the strip still moves are not lost: the last one is where it ends.
    func select(_ p: Int, animated: Bool = true) {
        let p = min(max(0, p), pager.pageCount - 1)
        if p == 2 { feishuBuilt = true } else if p == 1 { agentsBuilt = true } else { assistantBuilt = true }
        if page != p {
            page = p
            #if DEBUG
            ShellTrace.shared.note("select \(p)")
            #endif
        }
        pager.slide(to: p, animated: animated && routed)
        routed = true
        let tab = Self.tab(for: p)
        if let model, model.selectedTab != tab { model.selectedTab = tab }
    }

    /// Routes that are not one of the two pages land on Assistant (the inbox) or in the sheet (Settings), and the tab goes
    /// back to the page in front.
    func route(_ t: AppTab, in model: AppModel) {
        #if DEBUG
        ShellTrace.shared.note("route \(t)")
        #endif
        switch t {
        case .assistant: select(0)
        case .chats: select(1)
        #if os(iOS) || os(visionOS)
        case .feishu: select(pager.pageCount > 2 ? 2 : 0)
        #endif
        case .mail:
            show(.inbox)
            select(0)
        case .settings:
            model.shellSettings = true
            select(page)
        }
    }
}

/// Pages the Assistant page pushes for the shell.
enum ShellPage: Hashable, Sendable {
    case newConversation, profile, inbox, todos, routines, knowledge, machines, health
    /// Recordings (meeting recordings started from the drawer, a Control, Siri…; docs/apple/recording.md): this device's.
    case recordings
    /// An extension's main-navigation page (nav_page) from the drawer.
    case extensionPage(ExtensionNavRoute)
}

/// What the Assistant page pushes by value for the shell (the inbox and the machines push values of their own on top).
enum ShellRoute: Hashable, Sendable {
    case inbox, todos, machines
    case extensionPage(ExtensionNavRoute)
}

extension EnvironmentValues {
    @Entry var shell: ShellState?
    /// The switch's pill follows the strip's position (only in `ShellMovingBar`).
    @Entry var shellPillFollowsStrip = false
}

#if DEBUG
/// What happened to the shell lately (switch taps, routes, slides, settles), for UI tests that chase a lost tap: the
/// probe's label.
///
/// `note` writes without reading (#1204): it is called from bodies too (RootView's builds a `ShellView`, whose throwaway
/// `ShellState` slides its pager), and the stored property's accessors would make that body depend on the trace it
/// just wrote and draw itself again, for ever. Only the probe, which reads `events`, follows it.
@MainActor @Observable final class ShellTrace {
    static let shared = ShellTrace()
    var events: [String] {
        access(keyPath: \.events)
        return recent
    }
    @ObservationIgnored private var recent: [String] = []
    @ObservationIgnored private var serial = 0
    /// Each event says when, in seconds since the trace began: a test that waited too long tells what took the time.
    @ObservationIgnored private let start = Date()

    func note(_ s: String) {
        serial += 1
        withMutation(keyPath: \.events) {
            recent.append("\(serial) @\(String(format: "%.2f", Date().timeIntervalSince(start))) \(s)")
            if recent.count > 24 { recent.removeFirst(recent.count - 24) }
        }
    }
}
#endif
