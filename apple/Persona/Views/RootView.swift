import MarkdownUI
import BeautifulUI
import SwiftUI

struct RootView: View {
    @Environment(AppModel.self) private var model
    /// The system's text size, before Small / Standard / Large moves it.
    @Environment(\.dynamicTypeSize) private var systemTypeSize
    /// How far down the toasts outside the shell's window start: below the navigation bar's buttons, as the shell's do,
    /// not on them (the owner asked, #138: a toast covered Back). The Mac's stay at the top of the window.
    private var toastClearance: CGFloat {
        #if os(iOS)
        ShellNavigationClearance.needed(at: model.textSize.applied(to: systemTypeSize))
        #else
        0
        #endif
    }
    @Environment(\.horizontalSizeClass) private var widthClass
    #if DEBUG
    /// `-swapTreeAt <seconds>`: the width class goes compact for a moment that long after launch, as iOS may set it for
    /// its snapshots when the app goes to the background (iPhone Duo open): the tree swap that follows, on any simulator.
    @State private var flipped = false
    #else
    private let flipped = false
    #endif

    /// The ChatGPT-style shell (docs/apple/ios.md「新外壳」): an iPhone-width screen, unless Settings › Appearance › Old Layout is on.
    private var usesShell: Bool {
        #if os(visionOS)
        // Apple Vision Pro keeps the tabs, as the system's ornament (VisionShell), whatever the window's width.
        return false
        #elseif os(iOS)
        return !model.oldLayout && (widthClass == .compact || flipped)
        #else
        return false
        #endif
    }

    /// The shell is on screen, and with it its toast window (`ToastWindow`): not on the welcome page, the engine step or
    /// the checklist after pairing (the same branches as `body`).
    private var shellShown: Bool { usesShell && model.isPaired && model.pairingEngineMachine == nil && !model.showsChecklist }

    /// The tabs are on screen on an iPhone or iPad (Old Layout, iPad, iPhone Duo open), and with them a toast window of
    /// their own (`ToastWindowHook`): the same branches as `body`, with no extension page in the app's place.
    private var tabsToastWindow: Bool {
        #if os(iOS)
        !usesShell && model.isPaired && model.pairingEngineMachine == nil && !model.showsChecklist && model.extensionOpen == nil
        #else
        false
        #endif
    }

    /// The toasts are drawn in this view's overlay: where no toast window is up (the welcome page, the engine step, the
    /// checklist, an extension page in the app's place, the Mac, Apple Vision Pro).
    private var overlayToasts: Bool { model.extensionOpen != nil || !(shellShown || tabsToastWindow) }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("RootView")
        #endif
        Group {
            if let ref = model.pairingEngineMachine {
                PairingEngineView(ref: ref)
            } else if model.isPaired, model.showsChecklist {
                // Just paired: what the main conversation still needs, before the app (#4195).
                OnboardingChecklistView()
            } else if model.isPaired {
                #if os(visionOS)
                RootTabs().visionShell()
                #elseif os(iOS)
                if usesShell { ShellView() } else { RootTabs().background { ToastWindowHook() } }
                #else
                RootTabs()
                #endif
            } else {
                WelcomeView()
            }
        }
        .task(id: model.checklistPending && model.hubFeatures.contains(AssistantReadiness.feature)) {
            if model.checklistPending { await model.refreshReadiness() }
        }
        // The plugins' main-navigation entries and cards, for Settings' badge (docs/extensions/extensions.md「UI 插槽」).
        .task(id: ExtensionSlots.source(model)) {
            let hub = ExtensionSlots.source(model).hub
            await ExtensionSlots.shared.reload(for: hub)
            // Which inbox sources a plugin draws (placement inbox_item): known before an item is opened.
            await InboxItemSlots.shared.reload(for: hub)
        }
        .extensionThemes()
        .extensionSystemPresentation()
        // A new style, Theme Color or persona rebuilds the whole tree (tokens are static); the open tab and chat are remembered.
        .id(model.look)
        #if DEBUG
        .task {
            let at = UserDefaults.standard.double(forKey: "swapTreeAt")
            guard at > 0 else { return }
            try? await Task.sleep(for: .seconds(at))
            flipped = true
            try? await Task.sleep(for: .seconds(1))
            flipped = false
        }
        // LookBlink drives UIWindowScene overrides, so it only exists on iOS and visionOS.
        #if os(iOS) || os(visionOS)
        .task { await LookBlink.run() }
        #endif
        #endif
        .tint(Theme.accent)
        .fontDesign(model.style.fontDesign)
        .dynamicTypeSize(model.textSize.applied(to: systemTypeSize))
        .environment(\.shellBaseTypeSize, model.textSize.applied(to: systemTypeSize))
        .buiTheme(.persona())
        // No scroll bars anywhere (2026-10-31, the user: they look bad); the environment reaches every ScrollView,
        // List and sheet below. UIKit scroll views (MonoTextView, the terminal, web views) turn theirs off themselves.
        .scrollIndicators(.hidden)
        .markdownInlineImageProvider(MathInlineImages()).markdownImageProvider(WebMarkdownImages()) // inline formulas in every Markdown (MathMarkdown)
        // On iOS the shell and the tabs show the toasts in a window of their own (`ToastWindow`), over their covers and
        // sheets too: drawn here, the tabs' Saved to Photos was under the image viewer and the share card (#1492). Where
        // there is no such window they are drawn here: a pairing link that failed on the welcome page said nothing (#4273).
        .overlay(alignment: .top) { if overlayToasts { ToastOverlay().padding(.top, toastClearance) } }
        #if os(iOS) || os(visionOS)
        // Settings › Appearance › Light & Dark on this scene's window (and any other open one) as it first shows.
        .onAppear { model.colorScheme.apply() }
        // The Theme Color on the windows too, for what UIKit draws in the window's tint (Settings' row icons, `WindowTint`).
        .onChange(of: model.look, initial: true) { WindowTint.apply() }
        #endif
        #if DEBUG
        .overlay(alignment: .bottomTrailing) { PerfCountProbe() }
        // UI tests see the small window (VNC in picture in picture, #1343), which is the system's.
        .overlay(alignment: .topLeading) {
            if model.vncPiP != nil {
                Color.clear.frame(width: Theme.Size.hairline, height: Theme.Size.hairline)
                    .accessibilityElement().accessibilityLabel("VNC 小窗开着").accessibilityIdentifier("vnc-pip-on") // i18n-ignore
            }
        }
        .screenProbe("connection", ConnectionShown(shown: "\(model.connection.shown)", network: model.connection.networkUp))
        .htmlCardSample()
        .voiceOrbSample()
        .healthTrendsSample()
        #endif
        .alert("Claude Wants to Act on Your iPhone", isPresented: Binding(
            get: { model.pendingConfirmation != nil },
            // Gone without an answer: Deny for the one this alert showed. Its own button may have answered it already,
            // and the next one may be on screen by the time this comes: that one is not touched (#4456).
            set: { [shown = model.pendingConfirmation?.id] in if !$0, let shown { model.phoneConfirmations.answer(shown, false) } }
        ), presenting: model.pendingConfirmation) { c in
            Button("Allow [Notification Actions ce7ef2]") { model.phoneConfirmations.answer(c.id, true) }
            Button("Deny", role: .cancel) { model.phoneConfirmations.answer(c.id, false) }
        } message: { c in
            Text(c.summary)
        }
        .sendRefusalAlert(model)
        // Add Computer / Confirm Computer from anywhere (the readiness card's add_computer / confirm_computer, the Agents list).
        .addComputerPresentation()
        .loadsChannels() // the names under a message that came in from a channel (「来自 微信」)
        #if os(iOS) || os(visionOS)
        .onChange(of: LarkHomePreference.shared.shows) { _, enabled in
            if !enabled, model.selectedTab == .feishu { model.selectedTab = .assistant }
        }
        #endif
    }
}

@MainActor private struct RootTabs: View {
    @Environment(AppModel.self) private var model
    #if os(iOS)
    @State private var pad = PadRouter()
    #endif

    /// iPad (and iPhone Duo open) without Old Layout: Inbox and Settings are in Persona's sidebar, as in the iPhone's drawer,
    /// not tabs (docs/apple/ios.md「iPad」, 2026-11-05 user). Old Layout keeps the four tabs; Apple Vision Pro keeps them too.
    private var padSidebar: Bool {
        #if os(iOS)
        !model.oldLayout
        #else
        false
        #endif
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("RootTabs")
        #endif
        @Bindable var model = model
        TabView(selection: $model.selectedTab) {
            AssistantRootTab()
            AgentsRootTab()
            #if os(iOS) || os(visionOS)
            if LarkHomePreference.shared.shows { FeishuRootTab() }
            #endif
            if !padSidebar {
                InboxRootTab()
                SettingsRootTab()
            }
        }
        #if os(iOS)
        .modifier(PadRoutes(router: pad, enabled: padSidebar))
        #endif
    }
}

#if os(iOS)
/// Where the iPad's routes to Inbox and Settings land without their tabs (`RootTabs.padSidebar`): every route in the app still
/// sets `AppModel.selectedTab` (notifications, Siri, the inbox's rows…). `.mail` opens the inbox on the Persona tab's page,
/// beside its sidebar; `.settings` the sheet, over the tab that was in front. As the iPhone's shell does (`ShellState.route`).
@MainActor @Observable final class PadRouter: TabRouter {
    /// What the Persona tab is asked to show; `serial` makes the same request count twice.
    private(set) var request: (page: ShellPage, serial: Int)?
    /// The tab in front, for Settings to go back to.
    @ObservationIgnored private var front = AppTab.assistant

    func show(_ page: ShellPage) {
        request = (page, (request?.serial ?? 0) + 1)
    }

    func attach(to model: AppModel) {
        model.tabRouter = self
        route(model.selectedTab, in: model)
    }

    func detach(from model: AppModel) {
        guard model.tabRouter === self else { return }
        model.tabRouter = nil
    }

    func route(_ t: AppTab, in model: AppModel) {
        guard !model.oldLayout else { return detach(from: model) }
        switch t {
        case .mail:
            show(.inbox)
            front = .assistant
            model.selectedTab = .assistant
        case .settings:
            model.shellSettings = true
            model.selectedTab = front
        default:
            front = t
        }
    }
}

/// The iPad's routes (`PadRouter`) and its Settings sheet, on the tabs.
private struct PadRoutes: ViewModifier {
    @Environment(AppModel.self) private var model
    let router: PadRouter
    var enabled: Bool

    func body(content: Content) -> some View {
        if enabled {
            @Bindable var model = model
            content
                .environment(\.padRouter, router)
                .sheet(isPresented: $model.shellSettings) {
                    // As the shell's: the look is its identity too (the sheet outlives RootView's rebuild for a new look).
                    SettingsView()
                        .environment(\.settingsSheet, true)
                        .id(model.look)
                        // The page's height, not the form's: Settings is a long list.
                        .presentationSizing(.page)
                }
                .onAppear { router.attach(to: model) }
                .onDisappear { router.detach(from: model) }
        } else {
            content
        }
    }
}

extension EnvironmentValues {
    /// Set on the iPad's tabs when Inbox and Settings are in Persona's sidebar (`RootTabs.padSidebar`); nil otherwise.
    @Entry var padRouter: PadRouter?
}
#endif

/// A tab's page, drawn again only by what it reads itself. Each tab's badge (the agents waiting, the inbox's Needs You) is read
/// by its `Tab`, and the hub changes them with nearly every push: without this the `Tab` built its page again each time,
/// the page ran its body (the Agents list's toolbar handed to UIKit again: the open ⋯ menu went back to its top while
/// choosing a machine; the open chat laid out again under the finger), TestFlight 70, docs/lessons/ios.md. The pages
/// take nothing from here, so nothing is lost.
private struct TabPage<Page: View>: View, Equatable {
    @ViewBuilder var page: () -> Page

    nonisolated static func == (a: Self, b: Self) -> Bool { true }

    var body: some View { page() }
}

@MainActor private struct AssistantRootTab: TabContent {
    @Environment(AppModel.self) private var model

    var body: some TabContent<AppTab> {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AssistantRootTab")
        #endif
        Tab("Persona", systemImage: Theme.Icon.ai, value: AppTab.assistant) {
            TabPage {
                AssistantView()
                    .modifier(TabActivityScope(tab: .assistant))
                #if PERF_SIGNPOST
                    .modifier(PerfTabMount("assistant"))
                #endif
            }
            .equatable()
        }
        .badge(model.badges.assistant)
    }
}

@MainActor private struct AgentsRootTab: TabContent {
    @Environment(AppModel.self) private var model

    var body: some TabContent<AppTab> {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AgentsRootTab")
        #endif
        Tab("Agents", systemImage: Theme.Icon.agents, value: AppTab.chats) {
            TabPage {
                ChatsView()
                    .modifier(TabActivityScope(tab: .chats))
                #if PERF_SIGNPOST
                    .modifier(PerfTabMount("agents"))
                #endif
            }
            .equatable()
        }
        .badge(model.badges.agents)
    }
}

@MainActor private struct InboxRootTab: TabContent {
    var body: some TabContent<AppTab> {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("InboxRootTab")
        #endif
        Tab(String(localized: "Inbox"), systemImage: Theme.Icon.inbox, value: AppTab.mail) {
            TabPage {
                InboxView()
                    .modifier(TabActivityScope(tab: .mail))
                #if PERF_SIGNPOST
                    .modifier(PerfTabMount("inbox"))
                #endif
            }
            .equatable()
        }
        .badge(InboxStore.shared.actionCount)
    }
}

@MainActor private struct SettingsRootTab: TabContent {
    @Environment(AppModel.self) private var model

    var body: some TabContent<AppTab> {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("SettingsRootTab")
        #endif
        Tab(String(localized: "Settings"), systemImage: Theme.Icon.settings, value: AppTab.settings) {
            TabPage {
                SettingsView()
                    .modifier(TabActivityScope(tab: .settings))
                #if PERF_SIGNPOST
                    .modifier(PerfTabMount("settings"))
                #endif
            }
            .equatable()
        }
        .badge(badge) // Demo Mode is left from Settings; the plugins' entries are on it (docs/extensions/extensions.md「UI 插槽」, badges)
    }

    /// Demo; else what the plugins' main-navigation entries' badges add up to (only words: a dot).
    private var badge: Text? {
        if model.isDemo { return Text("Demo") }
        let total = ExtensionSlots.shared.total
        if total.count > 0 { return Text(total.count > 99 ? "99+" : "\(total.count)") }
        return total.dot ? Text("•") : nil
    }
}

#if DEBUG
/// What the titles say about the link (`probe.connection`): Waiting for Network / Connecting… / Updating…, or nothing (online).
nonisolated struct ConnectionShown: Encodable, Equatable {
    var shown: String
    var network: Bool
}
#endif

#if os(iOS) || os(visionOS)
@MainActor private struct FeishuRootTab: TabContent {
    var body: some TabContent<AppTab> {
        Tab(String(localized: "Messages [Quote Views 4da199]"), systemImage: Theme.Icon.feishuChats, value: AppTab.feishu) {
            TabPage { LarkConversationsView().modifier(TabActivityScope(tab: .feishu)) }.equatable()
        }
    }
}
#endif
