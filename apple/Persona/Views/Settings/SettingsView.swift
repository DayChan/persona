import BeautifulUI
import SwiftUI
import VisionKit

struct SettingsView: View {
    @Environment(\.shell) private var shell
    /// Shown as a sheet without the shell (the iPad's, `PadRoutes`): the same × to close it.
    @Environment(\.settingsSheet) private var inSheet
    @Environment(AppModel.self) private var model
    @State private var confirmUnpair = false
    @State private var showPair = false
    @State private var storage: Int64?
    @State private var plugins: [HubExtension]?
    /// Where links in the plugins' cards open (docs/extensions/extensions.md「UI 插槽」).
    @State private var pluginLink: LinkTarget?
    @StoredValue(TranslationPrefs.enabledKey) private var translating = false
    @StoredValue(TranslationPrefs.targetKey) private var translateTarget = TranslationPrefs.defaultTarget

    /// The hub row's value: an update out or under way, else its version.
    private var hubSummary: String {
        guard let s = model.hubUpdate else { return "" }
        if s.working { return s.stateTitle }
        if s.latest?.newer == true { return s.channel == "docker" ? String(localized: "New Image") : String(localized: "Update Available") }
        return s.version
    }

    nonisolated private struct SlotsKey: Hashable, Sendable { var source: ExtensionSlots.Source; var depth: Int }

    /// Translation's row value: off, or the target language.
    private var translationSummary: String { translating ? TranslationPrefs.name(of: translateTarget) : String(localized: "state.off", defaultValue: "Off") }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("SettingsView")
        #endif
        // By value, every page: in the split (iPhone Duo open) they open beside the list, not inside its column.
        SplitNavigation(path: Bindable(model).settingsPath, destinations: SettingsDestinations()) {
            List {
                Section {
                    HStack(spacing: Theme.Space.l) {
                        AppGlyph(size: 54)
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text("Persona").font(Theme.Font.title)
                            if model.isDemo {
                                Text("Demo mode · All data is made up").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                            } else {
                                if model.isMultiHub {
                                    // Several groups of hubs: the one the hub-level settings below are about, to pick another.
                                    HubSwitcher()
                                } else {
                                    Text(model.config?.url.absoluteString ?? "").font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1)
                                }
                                HStack(spacing: Theme.Space.sm) {
                                    Circle().fill(model.live.looksConnected ? Theme.positive : Theme.caution).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                                    Text(linkState).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                                }
                            }
                        }
                    }
                    .padding(.vertical, Theme.Space.xs)
                }
                // Again once the hub's features are in (right after pairing they come after this page asked).
                .task(id: model.hubFeatures.contains(AssistantReadiness.feature)) {
                    if !model.isDemo { await model.refreshReadiness(force: false) }
                }
                #if os(iOS) || os(visionOS)
                // On the section that is always there: the plugins' one is empty until they are read.
                .task(id: SlotsKey(source: ExtensionSlots.source(model), depth: model.settingsPath.count)) {
                    await ExtensionSlots.shared.reload()
                }
                #endif

                // What the main conversation still needs (#4195): N steps left, back to the checklist. Settings may be the
                // first page that asks (the app opened on it).
                if !model.isDemo { ChecklistSettingsRow() }

                #if os(iOS) || os(visionOS)
                // The plugins' cards and pages (docs/extensions/extensions.md「UI 插槽」): at the top, where the main navigation's
                // entries are on an iPhone (the Mac has them in its sidebar).
                ExtensionSlotsSettingsSection(link: $pluginLink)
                #endif

                if model.isDemo {
                    DemoSection(pair: { showPair = true })
                }

                AppearanceSection()

                Section {
                    Group {
                        NavigationLink(value: ModelLaunchersRoute()) {
                            SettingsRow(title: String(localized: "Model Providers"), symbol: Theme.Icon.cpu, tint: Theme.Hue.purple)
                        }
                        .settingsListRow(ModelLaunchersRoute())
                        NavigationLink(value: SettingsPage.modelConfiguration) {
                            SettingsRow(title: String(localized: "Model Settings [Launcher Model Picker 8cbcf7]"), symbol: Theme.Icon.model, tint: Theme.ai)
                        }
                        .settingsListRow(SettingsPage.modelConfiguration)
                        NavigationLink(value: SettingsPage.assistant) {
                            // No model here: the assistant's model is Model Settings › Main Chat (#1291).
                            SettingsRow(title: String(localized: "Assistant"), symbol: Theme.Icon.ai, tint: Theme.ai)
                        }
                        .settingsListRow(SettingsPage.assistant)
                        .accessibilityIdentifier("settings-assistant")
                        NavigationLink(value: SettingsPage.triage) {
                            SettingsRow(title: String(localized: "Session Triage"), symbol: Theme.Icon.triage, tint: Theme.ai)
                        }
                        .settingsListRow(SettingsPage.triage)
                        .accessibilityIdentifier("settings-triage")
                        NavigationLink(value: SettingsPage.claude) {
                            SettingsRow(title: String(localized: "Claude Code Settings"), symbol: Theme.Icon.options, tint: Theme.Hue.orange)
                        }
                        .settingsListRow(SettingsPage.claude)
                        NavigationLink(value: SettingsPage.codex) {
                            SettingsRow(title: String(localized: "Codex Settings"), symbol: Theme.Icon.options, tint: Theme.Hue.gray)
                        }
                        .settingsListRow(SettingsPage.codex)
                    }
                    .bareListRow()
                }

                ChannelsEntryRow()

                ConnectorsSection()

                // What Persona sent in the user's name through the connectors (#5278, docs/extensions/connectors.md "Sent as the user").
                if !model.isDemo {
                    Section {
                        Group {
                            NavigationLink(value: SettingsPage.sent) {
                                SettingsRow(title: String(localized: "Sent as You"), symbol: Theme.Icon.sentAsYou, tint: Theme.Hue.orange)
                            }
                            .settingsListRow(SettingsPage.sent)
                            .accessibilityIdentifier("settings.sent")
                        }
                        .bareListRow()
                    }
                }

                // Extensions (docs/extensions/extensions.md): only with a hub that has the plugin base.
                if model.hubFeatures.hasExtensionFeature("manager") {
                    Section {
                        Group {
                            NavigationLink(value: SettingsPage.plugins) {
                                SettingsRow(title: String(localized: "Extensions"), symbol: Theme.Icon.plugins, tint: Theme.Hue.purple,
                                            value: ExtensionText.listSummary(plugins), look: .oneLine)
                            }
                            .settingsListRow(SettingsPage.plugins)
                            .accessibilityIdentifier("settings-plugins")
                        }
                        .bareListRow()
                    }
                    .task(id: model.settingsPath.count) { plugins = (try? await model.api?.plugins()) ?? plugins }
                }

                Section {
                    Group {
                        NavigationLink(value: MachineListRoute()) { MachinesSettingsRow() }
                        .settingsListRow(MachineListRoute())
                        NavigationLink(value: SettingsPage.devices) {
                            SettingsRow(title: String(localized: "Devices"), symbol: Theme.Icon.credentials, tint: Theme.Hue.gray)
                        }
                        .settingsListRow(SettingsPage.devices)
                        NavigationLink(value: SettingsPage.notifications) {
                            SettingsRow(title: String(localized: "Notifications"), symbol: Theme.Icon.notifications, tint: Theme.needsYou)
                        }
                        .settingsListRow(SettingsPage.notifications)
                        NavigationLink(value: SettingsPage.translation) {
                            SettingsRow(title: String(localized: "Translation"), symbol: Theme.Icon.translate, tint: Theme.accent, value: translationSummary, look: .oneLine)
                        }
                        .settingsListRow(SettingsPage.translation)
                        NavigationLink(value: SettingsPage.storage) {
                            SettingsRow(title: String(localized: "Storage"), symbol: "internaldrive", tint: Theme.Hue.gray, value: storage?.storageText ?? "", look: .digits)
                        }
                        .settingsListRow(SettingsPage.storage)
                    }
                    .bareListRow()
                }

                #if os(iOS) || os(visionOS)
                // A Mac pairs with no watch (WatchSync does nothing there): no button that would claim it sent something.
                if !model.isDemo { watchSection }
                #endif

                if !model.isDemo {
                    Section {
                        Group {
                            // Every hub paired, by group: add one, rename, disconnect one (docs/protocol/multi-hub.md). Also with
                            // one hub: it is where a second one is added (the only row the single-hub page gained).
                            NavigationLink(value: SettingsPage.hubs) {
                                SettingsRow(title: "hub", symbol: Theme.Icon.hubGroup, tint: Theme.Hue.green,
                                            value: model.isMultiHub ? String(localized: "\(model.groupIDs.count) Groups") : model.groupName(model.hub?.group ?? ""),
                                            look: .oneLine)
                            }
                            .settingsListRow(SettingsPage.hubs)
                            .accessibilityIdentifier("settings.hubs")
                            NavigationLink(value: SettingsPage.hub) {
                                SettingsRow(title: String(localized: "Hub Version"), symbol: Theme.Icon.server, tint: Theme.Hue.blue, value: hubSummary, look: .oneLine)
                            }
                            .settingsListRow(SettingsPage.hub)
                            .accessibilityIdentifier("settings.hub")
                            // The hub's managers, each a whole assistant (docs/assistant/managers.md).
                            NavigationLink(value: SettingsPage.managers) {
                                SettingsRow(title: String(localized: "Managers"), symbol: Theme.Icon.managers, tint: Theme.ai)
                            }
                            .settingsListRow(SettingsPage.managers)
                            .accessibilityIdentifier("settings.managers")
                            Button { showPair = true } label: { SettingsRow(title: String(localized: "Pair Again"), symbol: "qrcode.viewfinder", tint: Theme.accent) }
                                .modifier(SettingsButtonLook())
                            Button(role: .destructive) { confirmUnpair = true } label: {
                                SettingsRow(title: model.isMultiHub ? String(localized: "Disconnect All Hubs") : String(localized: "Disconnect and Forget Server"), symbol: Theme.Icon.end, tint: Theme.danger)
                                    #if os(macOS)
                                    .foregroundStyle(Theme.danger) // a plain row on the Mac: red says what it does
                                    #endif
                            }
                            .modifier(SettingsButtonLook())
                        }
                        .bareListRow()
                    } header: {
                        SettingsHeading(String(localized: "Server"))
                    }
                }

                Section {
                    Group {
                        let version = "\(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "") (\(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? ""))"
                        // The commit this build was made from (stamped at build time, `project.yml`): which one is on the phone.
                        let commit = Bundle.main.object(forInfoDictionaryKey: "PersonaCommit") as? String ?? String(localized: "Unknown [Session Modes 4d8c1c]")
                        if AppStyle.current.usesBeautifulUI {
                            BUIListRow(String(localized: "Version"), value: version)
                            BUIListRow(String(localized: "Build")) { EmptyView() } trailing: {
                                Text(commit).font(Theme.Font.code).foregroundStyle(.secondary).textSelection(.enabled)
                            }
                        } else {
                            LabeledContent("Version", value: version)
                            LabeledContent("Build") {
                                Text(commit)
                                    .font(Theme.Font.code)
                                    .textSelection(.enabled)
                            }
                        }
                    }
                    .bareListRow()
                } header: {
                    SettingsHeading(String(localized: "About"))
                }
            }
            .accessibilityIdentifier("settings-list")
            #if os(macOS)
            // Below the section's title in the title bar's row, not scrolling through it (#1243); inside the page's
            // background, which still reaches up behind the title.
            .modifier(MacBelowTitleRow())
            // The page open beside the list, for its row's selection (`settingsListRow`).
            .environment(\.macSettingsOpen, model.settingsPath.firstPageOnly)
            #endif
            .themedPage()
            .pageToolbar {
                    // As a sheet over the shell (docs/apple/ios.md「新外壳」): ChatGPT's × to close it.
                    if shell != nil || inSheet {
                        ToolbarItem(placement: .topBarTrailing) {
                            Button { model.shellSettings = false } label: { Label("Close", systemImage: Theme.Icon.close) }
                                .foregroundStyle(Color.primary)
                                .accessibilityIdentifier("settings-close")
                        }
                    }
                }
                .navigationTitle("Settings")
            .task { await model.refreshAssistant() }
            // Measured each time the page shows and whenever a page closes (back from Storage after clearing; in the
            // split the list never leaves the screen).
            .onAppear(perform: measureStorage)
            .onChange(of: model.settingsPath) { measureStorage() }
            #if DEBUG
            // With `-tab settings`: `-openAssistantPage assistant|routines|routine|newRoutine|followups|memory|memoryFile|
            // memoryEdit|about`, `-openStorage YES` (Storage), `-openNotificationSettings YES` (Notifications), `-openMachines YES` (Machines;
            // with `-openMachineDetail <id>` that machine's page), `-openDevices YES` (Devices), `-openHubUpdate YES` (hub),
            // `-openManagers YES` (Managers; `-openManagerPage <id>` one of them, `-showManagerCode YES` a new one's install code),
            // `-openSentAsYou YES` (Sent as You),
            // `-openSkills YES` (Skills), `-openExtensions YES` (Extensions; `-openExtension <id>` one of them, with `-openExtensionManager <id>`
            // another manager's copy; the old `-openPlugin…` names too), `-openChannels YES`
            // (Channels; `-openChannel <id>` one of them).
            .onAppear(perform: openDebugPage)
            #endif
            .confirmationDialog("After disconnecting, you'll need to scan the code to pair again.", isPresented: $confirmUnpair, titleVisibility: .visible) {
                Button("Disconnect", role: .destructive) { model.unpair() }
            }
            .sheet(isPresented: $showPair) { PairView() }
            #if os(iOS) || os(visionOS)
            .pluginLinks($pluginLink)
            .extensionPushes(ExtensionSlots.shared.cardPages)
            #endif
            #if os(macOS) && DEBUG
            // `-tab settings -openPairSheet YES` (Demo Mode, or paired: Pair Again): the Connect Hub sheet, for screenshots
            // (scripts/app/mac_shot.sh; with `-openPairSSH YES` its SSH Tunnel sheet over it).
            .onAppear { if UserDefaults.standard.bool(forKey: "openPairSheet") { showPair = true } }
            #endif
        } empty: {
            SplitPlaceholder(symbol: Theme.Icon.settings, title: String(localized: "Settings"), message: String(localized: "Choose an item on the left."))
                #if os(macOS)
                // As System Settings: the window opens on the first page, not on an empty pane.
                .onAppear { if model.settingsPath.isEmpty { model.settingsPath.append(SettingsPage.persona) } }
                #endif
        }
    }

    private func measureStorage() {
        Task { storage = await Task.detached(priority: .utility) { StorageUsage.scan().values.reduce(0, +) }.value }
    }

    /// The header's line about the link: connected (and how, when it goes through the hub's SSH tunnel), or why not.
    private var linkState: String {
        // While the hub restarts into a new version, the same words as the connection title (Hub updating…).
        guard model.live.looksConnected else {
            let phase = model.connection.phase
            return phase == .online ? String(localized: "Not Connected") : ConnectionLabel.text(phase, hubRestarting: model.hubUpdate?.restarting == true)
        }
        let tunneled = model.hub.flatMap { model.tunnel($0.group) }?.routed == true
        if let t = model.hub.flatMap({ model.inAppTunnelState($0.group) }), t.routed { return String(localized: "Connected to hub · via \(t.route.title)") }
        if model.hub.flatMap({ model.localProxyState($0.group) })?.routed == true {
            return String(localized: "Connected to hub · via \(String(localized: "Local Proxy"))")
        }
        return tunneled ? String(localized: "Connected to hub · via SSH tunnel") : String(localized: "Connected to hub")
    }

    #if DEBUG
    /// Once per launch: a new look rebuilds the tree (and this view's state), and the route would undo a page opened since.
    @MainActor private static var debugOpened = false

    private func openDebugPage() {
        guard !Self.debugOpened else { return }
        let d = UserDefaults.standard
        // Appended with their own types: a path of AnyHashable finds no destination.
        var p = NavigationPath()
        // `-openExtension…` / `-openMarketExtension`; the `-openPlugin…` names from before Extensions still work (#1859).
        let str = { (name: String) in d.string(forKey: name) ?? d.string(forKey: name.replacingOccurrences(of: "Extension", with: "Plugin")) }
        let flag = { (name: String) in d.bool(forKey: name) || d.bool(forKey: name.replacingOccurrences(of: "Extension", with: "Plugin")) }
        if d.bool(forKey: "openMachines") {
            p.append(MachineListRoute())
            if let id = d.string(forKey: "openMachineDetail") { p.append(MachineDetailRoute(id: id)) }
        }
        else if d.bool(forKey: "openStorage") { p.append(SettingsPage.storage) }
        else if d.bool(forKey: "openNotificationSettings") { p.append(SettingsPage.notifications) }
        else if d.string(forKey: "openAssistantPage") != nil { p.append(SettingsPage.assistant) }
        else if d.bool(forKey: "openDevices") { p.append(SettingsPage.devices) }
        else if d.bool(forKey: "openHubs") || d.bool(forKey: "openSSHTunnel") || d.bool(forKey: "openLocalProxy") {
            p.append(SettingsPage.hubs)
            // `-openSSHTunnel YES` / `-openLocalProxy YES`: the first hub's Settings › hub › SSH Tunnel / Local Proxy (screenshots).
            if d.bool(forKey: "openSSHTunnel"), let first = model.registry.hubs.first { p.append(SettingsPage.sshTunnel(first.id)) }
            if d.bool(forKey: "openLocalProxy"), let first = model.registry.hubs.first { p.append(SettingsPage.localProxy(first.id)) }
        }
        else if d.bool(forKey: "openHubUpdate") { p.append(SettingsPage.hub) }
        else if d.bool(forKey: "openManagers") || d.string(forKey: "openManagerPage") != nil || d.bool(forKey: "showManagerCode") {
            // Settings › Managers (› one by its id; `-showManagerCode YES` makes one and opens its code sheet)
            p.append(SettingsPage.managers)
            if let id = d.string(forKey: "openManagerPage") { p.append(SettingsPage.manager(id)) }
        }
        else if d.bool(forKey: "openSentAsYou") { p.append(SettingsPage.sent) }
        else if d.bool(forKey: "openSkills") { p.append(SettingsPage.skills) }
        else if flag("openExtensions") || str("openExtension") != nil { // Settings › Extensions (› one (› a page of it))
            p.append(SettingsPage.plugins)
            if let id = str("openExtension") {
                // `-openExtensionManager <id>`: another manager's copy of it.
                let manager = d.string(forKey: "openExtensionManager")
                p.append(SettingsPage.plugin(id, manager: manager))
                if let ui = str("openExtensionPage") { p.append(SettingsPage.pluginPage(plugin: id, ui: ui, title: "", manager: manager)) }
            }
        }
        else if d.bool(forKey: "openChannels") || d.string(forKey: "openChannel") != nil { // Settings › Channels (› one)
            p.append(SettingsPage.channels)
            if let id = d.string(forKey: "openChannel") { p.append(SettingsPage.channel(id)) }
        }
        else if flag("openExtensionMarket") || flag("openExtensionMarkets") || str("openMarketExtension") != nil {
            // Settings › Extensions › Extension Market (› Markets, or › <market>/<extension>)
            p.append(SettingsPage.plugins)
            p.append(SettingsPage.pluginMarket)
            if flag("openExtensionMarkets") { p.append(SettingsPage.pluginMarkets) }
            if let m = str("openMarketExtension"), let slash = m.firstIndex(of: "/") {
                p.append(SettingsPage.marketExtension(market: String(m[..<slash]), id: String(m[m.index(after: slash)...])))
            }
        }
        else if d.bool(forKey: "openCodexSettings") { p.append(SettingsPage.codex) }
        else if d.bool(forKey: "openClaudeSettings") { p.append(SettingsPage.claude) } // with its Blocked Config Folders
        else if d.bool(forKey: "openConnectorMail") || d.bool(forKey: "addMail") { p.append(SettingsPage.mail) }
        else if d.bool(forKey: "openConnectorFeishu") { p.append(SettingsPage.feishu) }
        else if d.bool(forKey: "openConnectorGitHub") { p.append(SettingsPage.connector("github")) }
        else if d.bool(forKey: "openConnectorTelegram") { p.append(SettingsPage.connector("telegram")) }
        else if let id = d.string(forKey: "openConnectorPlugin") { p.append(SettingsPage.connector(id)) } // a plugin's connector by its id
        else if d.bool(forKey: "openConnectorMessages") || d.string(forKey: "openMessagesChat") != nil { // Messages (› Session › one)
            p.append(SettingsPage.connector("imessage"))
            if d.bool(forKey: "openMessagesChats") || d.string(forKey: "openMessagesChat") != nil { p.append(MessagesChatsRoute()) }
            if let id = d.string(forKey: "openMessagesChat") { p.append(MessagesThreadRoute(id: id, name: "")) }
        }
        else if d.bool(forKey: "openConnectorBot") { p.append(SettingsPage.feishuBot) }
        else if d.bool(forKey: "openConnectorDevice") { p.append(SettingsPage.phoneData) }
        else if d.bool(forKey: "openTranslationSettings") { p.append(SettingsPage.translation) }
        else if d.bool(forKey: "openTriageSettings") { p.append(SettingsPage.triage) }
        else if d.string(forKey: "openAppearance") == "chat" { p.append(SettingsPage.chatLayout) } // New Conversation Layout (#1666)
        else if let a = d.string(forKey: "openAppearance") { // persona (or YES, or icon: the icon is the Avatar's) | color | style | scheme
            p.append(a == "color" ? SettingsPage.themeColor : a == "style" ? SettingsPage.style
                     : a == "scheme" ? SettingsPage.colorScheme : SettingsPage.persona)
        }
        else if d.bool(forKey: "openModelConfiguration") {
            p.append(SettingsPage.modelConfiguration)
            if let id = d.string(forKey: "openModelPurpose") { p.append(ModelPurposeRoute(id: id)) }
        }
        else if let n = d.string(forKey: "openLauncherPage") { p.append(ModelLaunchersRoute()); p.append(ModelLauncherRoute(name: n)) }
        else if d.bool(forKey: "openModelLaunchers") || d.string(forKey: "openModelLauncher") != nil { // Settings › Model (› one)
            p.append(ModelLaunchersRoute())
            if let n = d.string(forKey: "openModelLauncher") { p.append(ModelLauncherRoute(name: n)) }
        }
        guard !p.isEmpty else { return }
        Self.debugOpened = true
        model.settingsPath = p
    }
    #endif

    /// The watch's own credentials (got now where there are none yet), never this phone's (#1185).
    private func syncWatch() async {
        guard model.isPaired else { return }
        guard WatchSync.shared.watchAppInstalled else {
            Haptics.error()
            model.toast = Toast(String(localized: "No Apple Watch with Persona found. Install it first in the Watch app on your iPhone."), style: .error)
            return
        }
        await model.upgradeCredentials()
        WatchSync.shared.push(summary: model.watchSummary())
        if model.watchConfigs().missing?.isEmpty == false {
            Haptics.error()
            model.toast = Toast(String(localized: "Couldn't get credentials for the watch. Check your network and try again; if the hub is too old, update it first."), style: .error)
        } else {
            Haptics.success()
            model.toast = Toast(String(localized: "Sent to Apple Watch. Open Persona on your watch."), style: .success)
        }
    }

    private var watchSection: some View {
        Section {
            Group {
                Button {
                    Task { await syncWatch() }
                } label: {
                    if AppStyle.current.usesBeautifulUI {
                        SettingsRow(title: String(localized: "Sync to Apple Watch"), symbol: Theme.Icon.watch, tint: Theme.accent)
                    } else {
                        Label("Sync to Apple Watch", systemImage: Theme.Icon.watch).lineLimit(1)
                    }
                }
                .modifier(SettingsButtonLook())
            }
            .bareListRow()
        } footer: {
            Text("Persona on Apple Watch connects to the hub directly using your iPhone's pairing. If the watch shows “Not Paired”, tap here, or tap “Sync from iPhone” on the watch.")
        }
    }
}

/// The pages of Settings that are not routes of their own elsewhere.
enum SettingsPage: Hashable {
    case persona, themeColor, style, colorScheme, language, assistant, claude, codex, devices, phoneData, mail, feishu, feishuBot, notifications, storage, translation, modelConfiguration
    /// Settings › Session Triage (docs/assistant/agent-work.md「开关」).
    case triage
    /// Settings › Appearance › New Conversation Layout (#1666, `ChatLayout`).
    case chatLayout
    /// Settings › hub: the hubs paired, by group (docs/protocol/multi-hub.md).
    case hubs
    case hub, skills
    /// A connector of the hub's by its id (GET /api/v1/connectors): GitHub…; `manager`: another manager's copy
    /// (docs/assistant/managers.md "Grouping"), nil the default's.
    case connector(String, manager: String? = nil)
    /// Settings › Extensions, and one plugin by its id (docs/extensions/extensions.md) and manager (another manager's copy of
    /// the id; nil: the default's).
    case plugins
    case plugin(String, manager: String? = nil)
    /// A page a plugin draws (placement plugin_page, docs/extensions/extensions.md「声明式 UI」), titled until its View says
    /// otherwise; `manager`: the plugin's (nil: the default's).
    case pluginPage(plugin: String, ui: String, title: String, manager: String? = nil)
    /// Settings › Extensions › Extension Market, one plugin of a market, and the markets (docs/extensions/extensions.md「扩展市场」).
    case pluginMarket
    case marketExtension(market: String, id: String)
    case pluginMarkets
    /// Settings › Channels, and one channel by its id (docs/extensions/channels.md).
    case channels
    case channel(String)
    /// Settings › hub › SSH Tunnel of one paired hub (its id; docs/operations/network.md「经 SSH 隧道连 hub」).
    case sshTunnel(String)
    case inAppTunnel(String)
    /// Settings › hub › Local Proxy of one paired hub (docs/operations/network.md "Connecting to hub through a local SOCKS5 proxy").
    case localProxy(String)
    /// Settings › Managers, and one manager by its id (docs/assistant/managers.md).
    case managers
    case manager(String)
    /// Settings › Sent as You: what Persona sent in the user's name through the connectors (docs/extensions/connectors.md "Sent as the user").
    case sent

}

/// The pages the settings list opens, in either layout (`SplitNavigation`). MachinesView pushes its cards by value
/// (MachineDetailRoute), and a machine's page its agents (SessionKey).
private struct SettingsDestinations: ViewModifier {
    func body(content: Content) -> some View {
        content
            .navigationDestination(for: SettingsPage.self) { page(for: $0).splitDetailPage() }
            .navigationDestination(for: MachineListRoute.self) { _ in MachinesView().splitDetailPage() }
            .navigationDestination(for: ModelLaunchersRoute.self) { _ in ModelLaunchersView().splitDetailPage() }
            .navigationDestination(for: ModelPurposeRoute.self) { r in ModelPurposeEditorView(purposeID: r.id).splitDetailPage() }
            .navigationDestination(for: ModelLauncherRoute.self) { r in ModelLauncherDetailView(name: r.name).splitDetailPage() }
            .navigationDestination(for: MachineDetailRoute.self) { r in MachineDetailView(ref: r.ref).splitDetailPage() }
            .navigationDestination(for: MessagesChatsRoute.self) { r in MessagesChatListView(manager: r.manager).splitDetailPage() }
            .navigationDestination(for: MessagesThreadRoute.self) { r in MessagesThreadView(id: r.id, name: r.name, manager: r.manager).splitDetailPage() }
            .navigationDestination(for: SessionKey.self) { key in ChatView(key: key).id(key).splitDetailPage(ownsBack: true) }
            .extensionPushDestination()
    }

    @ViewBuilder private func page(for page: SettingsPage) -> some View {
        switch page {
        case .persona: PersonaSettingsPage()
        case .themeColor: ThemeColorSettingsPage()
        case .style: StyleSettingsPage()
        case .language: LanguageSettingsPage()
        case .chatLayout: ChatLayoutSettingsPage()
        #if os(iOS) || os(visionOS)
        case .colorScheme: ColorSchemeSettingsPage()
        #else
        case .colorScheme: EmptyView()
        #endif
        case .assistant: AssistantProfileView()
        case .claude: ClaudeSettingsView()
        case .codex: ClaudeSettingsView(agent: .codex)
        case .devices: DevicesView()
        case .phoneData: PhoneDataView()
        case .mail: MailAccountsView(inSheet: false)
        case .feishu: FeishuConnectorView()
        case .connector(let id, let manager):
            if id == "imessage" { MessagesConnectorView(manager: manager) } else { HubConnectorView(id: id, manager: manager) }
        case .feishuBot: FeishuBotView()
        case .notifications: NotificationSettingsView()
        case .storage: StorageView()
        case .translation: TranslationSettingsView()
        case .triage: TriageSettingsView()
        case .modelConfiguration: ModelConfigurationView()
        case .hubs: HubsView()
        case .sshTunnel(let id): HubSSHTunnelPage(hubID: id)
        case .inAppTunnel(let id): HubInAppTunnelPage(hubID: id)
        case .localProxy(let id): HubLocalProxyPage(hubID: id)
        case .managers: ManagersView()
        case .manager(let id): ManagerDetailView(id: id)
        case .sent: SentView()
        case .hub: HubSettingsPage()
        case .skills: SkillsPage()
        case .plugins: ExtensionsView()
        case .plugin(let id, let manager): ExtensionDetailView(id: id, manager: manager)
        case .pluginPage(let plugin, let ui, let title, let manager): ExtensionPageView(plugin: plugin, ui: ui, title: title, manager: manager)
        case .pluginMarket: ExtensionMarketView()
        case .marketExtension(let market, let id): MarketExtensionDetailView(market: market, id: id)
        case .pluginMarkets: ExtensionMarketsView()
        case .channels: ChannelsView()
        case .channel(let id): ChannelDetailView(id: id)
        }
    }

}

/// Settings in Demo Mode: what it is, and the two ways out.
private struct DemoSection: View {
    @Environment(AppModel.self) private var model
    var pair: () -> Void

    var body: some View {
        Section {
            Group {
                #if os(macOS)
                // Rows like the list's other rows on the Mac (#1224 B4): as buttons they were two small gray capsules
                // at the list's leading edge, the sidebar-style list's own button look.
                Button(action: pair) {
                    SettingsRow(title: String(localized: "Connect My Hub…"), symbol: "qrcode.viewfinder", tint: Theme.accent)
                }
                .modifier(SettingsButtonLook())
                Button(role: .destructive) { model.unpair() } label: {
                    SettingsRow(title: String(localized: "Exit Demo Mode"), symbol: "rectangle.portrait.and.arrow.right", tint: Theme.danger)
                        .foregroundStyle(Theme.danger)
                }
                .modifier(SettingsButtonLook())
                .accessibilityIdentifier("settings.leaveDemo")
                #else
                Button(action: pair) {
                    if AppStyle.current.usesBeautifulUI {
                        SettingsRow(title: String(localized: "Connect My Hub"), symbol: "qrcode.viewfinder", tint: Theme.accent)
                    } else {
                        Label("Connect My Hub", systemImage: "qrcode.viewfinder").lineLimit(1)
                    }
                }
                .modifier(SettingsButtonLook())
                Button(role: .destructive) { model.unpair() } label: {
                    if AppStyle.current.usesBeautifulUI {
                        SettingsRow(title: String(localized: "Exit Demo Mode"), symbol: "rectangle.portrait.and.arrow.right", tint: Theme.danger)
                    } else {
                        Label("Exit Demo Mode", systemImage: "rectangle.portrait.and.arrow.right").lineLimit(1)
                    }
                }
                .modifier(SettingsButtonLook())
                .accessibilityIdentifier("settings.leaveDemo")
                #endif
            }
            .bareListRow()
        } header: {
            SettingsHeading(String(localized: "Demo Mode"))
        } footer: {
            Text("The machines, agents, chats, and inbox you see are demo data. Nothing connects to a server, and messages, approvals, and other actions aren't actually sent. Connecting your own hub exits demo mode automatically.")
        }
    }
}

/// Settings › Connectors › This iPhone: which of the device's data Claude may use (phone_* tools). One switch per source, and
/// the switch is the whole state: on = turned on and allowed (2026-11-01, no Allowed / Tap to Allow labels). Turning one on
/// asks for the permission; refused (or refused before) it stays off and Settings opens where it can be allowed.
struct PhoneDataView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @State private var statuses: [PhoneDataSource: PhoneAuthStatus] = [:]
    @State private var enabled: [PhoneDataSource: Bool] = [:]
    @State private var asking: PhoneDataSource?

    var body: some View {
        List {
            Section {
                ForEach(PhoneDataSource.allCases) { s in
                    HStack(spacing: Theme.Space.m) {
                        SourceIcon(source: s)
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(s.title).lineLimit(1)
                            Text(s.detail).font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                        Spacer(minLength: Theme.Space.s)
                        if asking == s {
                            ProgressView()
                        } else {
                            Toggle(s.title, isOn: Binding(get: { isOn(s) }, set: { v in Task { await set(s, v) } }))
                                .labelsHidden()
                        }
                    }
                    .padding(.vertical, Theme.Space.xxs)
                }
            } footer: {
                Text("Claude can read the data you turn on through phone_* tools; writes such as new events or reminders ask you on the phone first. Turning one on requests system permission; if you denied it before, you'll go to Settings. The hub can reach the phone only while Persona is in the foreground (or just moved to the background). iOS doesn't expose Messages, Notes, or third-party app data.")
            }
        }
        .themedPage()
        .navigationTitle(ConnectorText.device)
        .navigationBarTitleDisplayMode(.inline) // like the other connectors' pages
        .task { refresh() }
        // Back from the system's Settings, where a permission may have been given or taken away.
        .onChange(of: phase) { _, p in if p == .active { refresh() } }
    }

    private func isOn(_ s: PhoneDataSource) -> Bool {
        (enabled[s] ?? s.isEnabled) && (statuses[s] ?? PhoneAuthorization.status(s)).isUsable
    }

    private func refresh() {
        for s in PhoneDataSource.allCases { statuses[s] = PhoneAuthorization.status(s) }
    }

    private func set(_ s: PhoneDataSource, _ on: Bool) async {
        defer { model.registerPhone() } // what the hub may call follows the switch
        guard on else {
            s.isEnabled = false
            enabled[s] = false
            return
        }
        var st = statuses[s] ?? PhoneAuthorization.status(s)
        if st == .notDetermined {
            asking = s
            st = await PhoneAuthorization.request(s)
            asking = nil
            statuses[s] = st
        }
        if st.isUsable {
            s.isEnabled = true
            enabled[s] = true
            Haptics.tap()
        } else {
            // Refused: iOS asks only once, so the rest is in its Settings; the switch stays off until it is allowed there.
            model.toast = Toast(String(localized: "Allow Persona to access \(s.title) in Settings"), style: .info)
            s.isEnabled = true // on its own once allowed there
            enabled[s] = true
            if let url = URL(string: UIApplication.openSettingsURLString) { await UIApplication.shared.open(url) }
        }
    }
}

/// A data source's icon: the iPhone app it comes from, else its symbol on a tile of the same size and shape.
private struct SourceIcon: View {
    var source: PhoneDataSource

    var body: some View {
        if let icon = source.appIcon {
            BrandMark(icon, side: Theme.Size.wellS)
        } else {
            ThemedIcon(source.symbol)
                .font(Theme.Font.heading)
                .foregroundStyle(.white)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Theme.accent.gradient)
                .scaledFrame(width: Theme.Size.wellS, height: Theme.Size.wellS)
                .clipShape(AppIconShape())
                .accessibilityHidden(true)
        }
    }
}

/// Persona's logo beside its name (Settings' header, the welcome page, the sidebar's Main Chat row): the Avatar's home screen
/// icon, in the look the home screen shows it in (#1312). A package's own face stands in for it here, in the icon's
/// shape: in the app it is the logo, on the home screen iOS allows only the icons the app ships (#3203).
struct AppGlyph: View {
    @Environment(AppModel.self) private var model
    @Environment(\.colorScheme) private var pageLook
    @Environment(\.displayScale) private var displayScale
    var size: CGFloat
    /// Scaled with the text size, as a mark in a row (`BrandMark`); the headers' are a fixed size.
    var scaled = false

    var body: some View {
        let p = model.persona
        Group {
            if !p.isBuiltin, let face = model.personas.avatar(p, points: size, scale: displayScale) {
                Image(uiImage: face).resizable().interpolation(.high).scaledToFill()
            } else {
                let icon = p.appIcon
                Image(icon.preview)
                    .resizable()
                    .interpolation(.high)
                    .environment(\.colorScheme, icon.homeScreenLook(model.colorScheme, system: systemLook))
            }
        }
        .modifier(GlyphFrame(side: size, scaled: scaled))
        .clipShape(AppIconShape())
        .overlay(AppIconShape().stroke(Theme.cardStroke, lineWidth: scaled ? Theme.Size.hairline : Theme.Size.line))
        .accessibilityHidden(true) // decoration: Persona's name is beside it
    }

    /// Under Match System the page's look (read here so the glyph redraws when the system's changes); else the screen's.
    private var systemLook: ColorScheme {
        #if os(iOS) || os(visionOS)
        model.colorScheme == .system ? pageLook : AppIconChoice.systemLook
        #else
        pageLook
        #endif
    }

    private struct GlyphFrame: ViewModifier {
        var side: CGFloat
        var scaled: Bool

        func body(content: Content) -> some View {
            if scaled { content.scaledFrame(width: side, height: side) } else { content.frame(width: side, height: side) }
        }
    }
}

/// The first screen (docs/assistant/onboarding.md 4.5「首屏」, #4195): what Persona is, one button that pairs (scan the hub's code;
/// the Mac pastes the link), "No hub yet?" (the public getting-started page, when the build has its address), the demo,
/// and Other ways for the rest — typing the address, an SSH tunnel, the command line. No command on the screen itself.
struct WelcomeView: View {
    @Environment(AppModel.self) private var model
    @State private var pair: PairView.Start?
    @State private var guide: LinkTarget?
    @State private var appear = false
    #if os(macOS)
    private static let pairTitle = String(localized: "Paste Pairing Link")
    private static let pairIcon = Theme.Icon.paste
    private static let pairHint = String(localized: "Setting up the hub gives you a pairing link; or, on a device that's already connected, open Settings › Devices › Connect a new device and copy the link there")
    #else
    private static let pairTitle = String(localized: "Scan to connect a hub")
    private static let pairIcon = "qrcode.viewfinder"
    private static let pairHint = String(localized: "Setting up the hub shows a QR code; or, on a device that's already connected, open Settings › Devices › Connect a new device")
    #endif

    var body: some View {
        VStack(spacing: 0) {
            Spacer()
            AppGlyph(size: 96)
                .scaleEffect(appear ? 1 : 0.8)
                .opacity(appear ? 1 : 0)
            Text("Persona")
                .font(Theme.Font.display)
                .padding(.top, Theme.Space.xl)
            Text("Connect your computers and your phone, and let AI get things done.")
                .font(Theme.Font.title)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.top, Theme.Space.sm)
                .padding(.horizontal, Theme.Space.xl)
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                feature(Theme.Icon.agentChat, String(localized: "Get things done by chatting"), String(localized: "It reads files and runs commands on your computer, and asks you before it acts"))
                feature("bolt.horizontal", String(localized: "See what it's doing, live"), String(localized: "Thinking, tool calls, and the terminal, streamed"))
                #if os(macOS)
                feature(Theme.Icon.click, String(localized: "Decide in one window"), String(localized: "Answer questions, approve plans, and allow tools without switching to the terminal"))
                feature(Theme.Icon.mailAccounts, String(localized: "Bring your mail and iPhone"), String(localized: "Gmail and other mail; calendar, contacts, and photos from Persona on your iPhone"))
                #else
                feature(Theme.Icon.tap, String(localized: "Decide on your phone"), String(localized: "Answer questions, approve plans, and allow tools, even on your watch"))
                feature(Theme.Icon.thisDevice, String(localized: "Bring your phone and mail"), String(localized: "Calendar, reminders, contacts, health, photos, Gmail"))
                #endif
            }
            .padding(.top, 40)
            .padding(.horizontal, Theme.Space.xxl)
            Spacer()
            Button { pair = .scan } label: {
                Label(Self.pairTitle, systemImage: Self.pairIcon).font(Theme.Font.heading).lineLimit(1)
                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.well)
            }
            .themedButton(prominent: true)
            .controlSize(.large)
            .padding(.horizontal, Theme.Space.xl)
            .accessibilityIdentifier("welcome.pair")
            Text(Self.pairHint)
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, Theme.Space.m)
                .padding(.horizontal, Theme.Space.xl)
            VStack(spacing: Theme.Space.s) {
                if let url = GettingStarted.url {
                    link(String(localized: "No hub yet?"), id: "welcome.guide") { guide = LinkTarget(url: url) }
                }
                // Without a hub there is nothing else to see: App Review and friends with the TestFlight link start here.
                link(String(localized: "Try it first (made-up data)"), id: "welcome.demo") { model.enterDemo() }
                link(String(localized: "Other ways"), id: "welcome.other") { pair = .other }
            }
            .padding(.top, Theme.Space.l)
            .padding(.bottom, Theme.Space.m)
        }
        // iPad: a centered column, not buttons from edge to edge.
        .frame(maxWidth: Theme.Size.readable)
        .frame(maxWidth: .infinity)
        .sheet(item: $pair) { PairView(start: $0) }
        .sheet(item: $guide) { LinkSheet(target: $0) }
        .onAppear { withAnimation(.spring(duration: 0.8, bounce: 0.35)) { appear = true } }
        #if DEBUG
        // `-openPairSheet YES` (or `other`): the pairing sheet, for screenshots (scripts/app/mac_shot.sh).
        .onAppear {
            switch UserDefaults.standard.string(forKey: "openPairSheet") {
            case "other": pair = .other
            case "YES", "1", "scan": pair = .scan
            default: break
            }
        }
        #endif
    }

    private func link(_ title: String, id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).font(Theme.Font.secondaryStrong).lineLimit(1).minimumScaleFactor(0.8)
                .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.tapTarget)
        }
        .buttonStyle(.borderless)
        .foregroundStyle(Theme.accent)
        .padding(.horizontal, Theme.Space.xl)
        .accessibilityIdentifier(id)
    }

    private func feature(_ symbol: String, _ title: String, _ detail: String) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.l) {
            ThemedIcon(symbol)
                .font(Theme.Font.title)
                .foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.wellS)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.heading)
                Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        }
    }
}

/// The public 「开始使用」 page (docs/operations/getting-started.md as published): from the build config (`publish.getting_started_url`,
/// Info.plist `PersonaGettingStartedURL`); nil when the build has none, and "No hub yet?" is not shown.
enum GettingStarted {
    static var url: URL? {
        guard let s = NativeStateMigration.infoString("PersonaGettingStartedURL"),
              s.hasPrefix("https://"), let u = URL(string: s) else { return nil }
        return u
    }
}

struct PairView: View {
    /// What the sheet opens on: the scanner (the Mac: pasting the link), or Other ways — typing the address, an SSH
    /// tunnel, the command line (#4195: commands only here).
    enum Start: String, Identifiable { case scan, other; var id: String { rawValue } }

    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var start: Start = .scan
    @State private var url = ""
    @State private var token = ""
    /// The SSH tunnel set up here, for a hub this device cannot reach directly (docs/operations/network.md「经 SSH 隧道连 hub」).
    @State private var ssh: SSHRoute?
    #if os(macOS)
    @State private var editingSSH = false
    #endif
    @State private var connecting = false
    @State private var error: String?
    @State private var scanning = DataScannerViewController.isSupported && DataScannerViewController.isAvailable
    /// The code was refused as expired or used (401 invalid_token): say so, and offer to scan again.
    @State private var expired = false
    @State private var showOther = false

    private var other: Bool { start == .other || showOther }

    var body: some View {
        NavigationStack {
            Form {
                if expired {
                    Section {
                        Label("This code has expired or was already used; get a new one", systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondaryStrong).foregroundStyle(Theme.danger)
                        #if os(iOS) || os(visionOS)
                        Button {
                            expired = false
                            error = nil
                            scanning = DataScannerViewController.isSupported && DataScannerViewController.isAvailable
                        } label: { Label("Scan again", systemImage: "qrcode.viewfinder").lineLimit(1) }
                        .accessibilityIdentifier("pair.rescan")
                        #endif
                    } footer: {
                        Text("Get a new code on the screen from setting up the hub, or on a connected device in Settings › Devices › Connect a new device.")
                    }
                }
                if scanning && !other {
                    Section {
                        QRScanner { code in
                            if let cfg = ServerConfig(pairURL: code) {
                                url = cfg.url.absoluteString
                                token = cfg.token
                                scanning = false
                                Task { await connect() }
                            }
                        }
                        #if os(iOS) || os(visionOS)
                        .scaledFrame(height: Theme.Size.box280)
                        .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
                        .listRowInsets(EdgeInsets())
                        #endif
                    } footer: {
                        #if os(iOS) || os(visionOS)
                        Text("Scan the QR code shown when the hub was set up, or the one from Settings › Devices › Connect a new device on a connected device.")
                        #else
                        Text("Copy the pairing link (given when the hub was set up, or from Settings › Devices › Connect a new device on a connected device), then click Paste pairing link.")
                        #endif
                    }
                }
                if !other {
                    Section {
                        Button { withAnimation(Theme.Motion.standard) { showOther = true } } label: {
                            Label("Other ways", systemImage: Theme.Icon.more).lineLimit(1)
                        }
                        .accessibilityIdentifier("pair.other")
                    } footer: {
                        Text("Type the address, connect through an SSH tunnel, or make a code with a command on the hub's machine.")
                    }
                }
                if other { otherWays }
                if let error { Section { Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption) } }
            }
            .themedPage()
            #if os(macOS)
            .sheet(isPresented: $editingSSH) {
                NavigationStack {
                    SSHTunnelForm(saved: ssh, dismissOnSave: true) { ssh = $0 }.navigationTitle("SSH Tunnel")
                }
            }
            #if DEBUG
            // `-openPairSheet other -openPairSSH YES`: this sheet's SSH Tunnel sheet, for screenshots.
            .onAppear { if UserDefaults.standard.bool(forKey: "openPairSSH") { editingSSH = true } }
            #endif
            #endif
            .modifier(PairSheetChrome(url: $url, token: $token, connecting: connecting, connect: { await connect() }))
        }
    }

    /// Other ways: the address and the one-time code typed in, an SSH tunnel, and how the hub prints a code on its machine.
    @ViewBuilder private var otherWays: some View {
                Section("Enter Manually") {
                    #if os(macOS)
                    // A grouped form puts the label on the left and the field on the right with no box: the prompt
                    // shows where to type.
                    TextField(String(localized: "Server Address"), text: $url, prompt: Text(verbatim: "http://hub.example.com:18660")).autocorrectionDisabled()
                    SecureField(String(localized: "Pairing code"), text: $token, prompt: Text("The code in the pairing link"))
                    #else
                    TextField(String(localized: "Server Address"), text: $url).keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    // A one-time credential, not a password: beside the address it looks like a login form, and iOS
                    // would offer to save it as one ("Save Password?") once the sheet goes (#1598).
                    SecretField(String(localized: "Pairing code"), filled: String(localized: "Filled in. Tap Connect."), text: $token, identifier: "pair.token")
                    #endif
                }
                Section {
                    #if os(macOS)
                    // A sheet of its own over this one, with its own Cancel / Save, not a page pushed inside the sheet: a
                    // pushed page got an iPhone's round back button, and the sheet shrank to the page's minimum width
                    // while the form kept its own, cut off on the right (#1170).
                    LabeledContent {
                        HStack(spacing: Theme.Space.s) {
                            if let ssh { Text(ssh.label).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle) }
                            Button(ssh == nil ? String(localized: "Set Up…") : String(localized: "Edit…")) { editingSSH = true }
                                .accessibilityIdentifier("pair.ssh")
                        }
                    } label: {
                        Label("Connect via SSH Tunnel", systemImage: Theme.Icon.sshTunnel).lineLimit(1)
                    }
                    #else
                    NavigationLink {
                        SSHTunnelForm(saved: ssh, dismissOnSave: true) { ssh = $0 }.navigationTitle("SSH Tunnel")
                    } label: {
                        SettingsRow(title: String(localized: "Connect via SSH Tunnel"), symbol: Theme.Icon.sshTunnel, tint: Theme.Hue.green,
                                    value: ssh.map { $0.label } ?? "", look: .oneLine)
                    }
                    .accessibilityIdentifier("pair.ssh")
                    #endif
                } footer: {
                    Text(ssh == nil ? String(localized: "If this device can't reach the hub's address (different network, no VPN), you can go through an SSH server that can reach the hub.")
                                    : ssh!.always ? String(localized: "Always connect to this hub through the SSH tunnel.") : String(localized: "Connect to this address directly first, and use the SSH tunnel if that fails."))
                }
                Section {
                    Text(verbatim: "personad hub pair")
                        .font(Theme.Font.code).textSelection(.enabled)
                } header: {
                    Text("Command line")
                } footer: {
                    Text("If you use the terminal on the hub's machine: run this line there and it prints a QR code and a pairing link.")
                }
    }
}

/// The pairing sheet's title, Cancel / Connect and the Mac's paste handling, around its form.
private struct PairSheetChrome: ViewModifier {
    @Binding var url: String
    @Binding var token: String
    var connecting: Bool
    var connect: () async -> Void
    @Environment(\.dismiss) private var dismiss

    func body(content: Content) -> some View {
        content
            #if os(macOS)
            // A window sheet does not take the main window's grouped forms: without this the Mac laid the fields out
            // in two columns, the labels flush against the sheet's left edge (2026-11-02).
            .macSheet()
            // ⌘V lands in the focused field (the address field has it when the sheet opens), not on the paste button:
            // a pairing link pasted there pairs as the button does; an address stays an address.
            .onChange(of: url) { _, typed in
                guard let cfg = ServerConfig(pairURL: typed) else { return }
                url = cfg.url.absoluteString
                token = cfg.token
                Task { await connect() }
            }
            #endif
            .navigationTitle("Connect Hub")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await connect() } } label: { if connecting { ProgressView() } else { Text("Connect [V N C Page a55741]") } }
                        .disabled(connecting || token.isEmpty || URL(string: url) == nil)
                }
            }
    }
}

/// What a failed pairing says (#4195).
enum PairError {
    /// The hub refused the one-time code: expired, already used or revoked (401 `invalid_token`; older hubs: the words).
    static func isExpiredCode(_ error: Error) -> Bool {
        guard let api = error as? APIError else { return false }
        return api.code == "invalid_token" || (api.status == 401 && api.message.lowercased().contains("invalid token")) // i18n-ignore: the hub's words
    
    }

    static var expiredText: String { String(localized: "This code has expired or was already used; get a new one") }
}

extension AppModel {
    /// A pairing link opened from outside (the camera, a pasted link) failed: an expired code in words, else as any error.
    func showPairing(_ error: Error) {
        if PairError.isExpiredCode(error) {
            toast = Toast(PairError.expiredText, style: .error)
            Haptics.error()
        } else {
            show(error)
        }
    }
}

extension PairView {
    fileprivate func connect() async {
        guard let u = URL(string: url.trimmingCharacters(in: .whitespaces)) else { return }
        connecting = true
        defer { connecting = false }
        do {
            try await model.pair(ServerConfig(url: u, token: token.trimmingCharacters(in: .whitespaces)), ssh: ssh)
            Haptics.success()
            dismiss()
        } catch {
            if PairError.isExpiredCode(error) {
                // A code already used or out of date (401 invalid_token): not 「invalid token」, and a way to scan again.
                self.error = nil
                expired = true
            } else if ssh != nil {
                if !error.isCancellation { self.error = String(localized: "Couldn't connect: \(error.localizedDescription)") }
            } else {
            #if os(macOS)
            if !error.isCancellation { self.error = String(localized: "Couldn't connect: \(error.localizedDescription). Make sure this Mac can reach the address (for example, on the same network or VPN), or connect through an SSH tunnel.") }
            #else
            if !error.isCancellation { self.error = String(localized: "Couldn't connect: \(error.localizedDescription). Make sure your phone can reach the address (for example, on the same network or VPN), or connect through an SSH tunnel.") }
            #endif
            }
            if !expired { scanning = DataScannerViewController.isSupported && DataScannerViewController.isAvailable } // scan again
            Haptics.error()
        }
    }
}

#if os(iOS) || os(visionOS)
struct QRScanner: UIViewControllerRepresentable {
    var onCode: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let vc = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])], qualityLevel: .balanced,
                                           isHighlightingEnabled: true)
        vc.delegate = context.coordinator
        try? vc.startScanning()
        return vc
    }

    func updateUIViewController(_ vc: DataScannerViewController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let onCode: (String) -> Void
        init(onCode: @escaping (String) -> Void) { self.onCode = onCode }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for item in items {
                if case .barcode(let b) = item, let s = b.payloadStringValue {
                    Haptics.success()
                    onCode(s)
                    return
                }
            }
        }
    }
}
#endif

/// Claude Code's /config options (a whitelist of ~/.claude/settings.json keys) on each machine.
/// Which agent's settings a page edits: Claude Code's settings.json or Codex's config.toml (the node keeps a whitelist
/// of each, claudeconfig.go / codexconfig.go).
enum AgentSettingsKind {
    case claude, codex

    var title: String { self == .claude ? String(localized: "Claude Code Settings") : String(localized: "Codex Settings") }

    var footer: String {
        switch self {
        case .claude: String(localized: "This edits ~/.claude/settings.json on that machine and changes only the settings below. New Claude sessions pick them up; running sessions usually need a restart.")
        case .codex: String(localized: "This edits ~/.codex/config.toml on that machine and changes only the settings below; everything else in the file (model providers, project trust, and so on) stays as is. New Codex sessions pick them up.")
        }
    }
}

struct ClaudeSettingsView: View {
    @Environment(AppModel.self) private var model
    var agent: AgentSettingsKind = .claude
    /// The machine id, "group|id" for another group of hubs (`MachineRef.path`).
    @StoredValue("claudeSettings.machine") private var machineID = ""
    private var ref: MachineRef { MachineRef(path: machineID) }
    @StoredValue("claudeSettings.applyAll") private var applyAll = true
    @State private var settings: [APIClient.ClaudeSetting] = []
    @State private var loading = false
    @State private var error: String?
    @State private var strings: [String: String] = [:]
    /// Live Replies of the machine shown (Claude Code only; nil before it loads, and gone on a hub without it).
    @State private var liveReplies: LiveRepliesModel?

    var body: some View {
        SettingsForm {
            Section {
                Picker(String(localized: "picker.machine", defaultValue: "Machine"), selection: $machineID) {
                    ForEach(model.machines.filter(\.online), id: \.ref) { m in Text(m.title).tag(m.ref.path) }
                }
                Toggle("Apply to All Online Machines", isOn: $applyAll)
            } footer: {
                Text(agent.footer)
            }
            if let error {
                Section { Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
            }
            #if os(macOS)
            // An empty group draws as a blank gap in a Mac form (while loading, or a machine with nothing to set).
            if !settings.isEmpty { Section { ForEach(settings) { s in row(s) } } }
            #else
            Section {
                ForEach(settings) { s in row(s) }
            }
            #endif
            if agent == .claude, let live = liveReplies, live.ref == ref, !live.unsupported {
                LiveRepliesSection(model: live, others: applyAll ? model.machines.filter { $0.online && $0.ref != ref }.map(\.ref) : [])
            }
            if agent == .claude, !machineID.isEmpty { ClaudeDirsSection(ref: ref) }
        }
        .themedPage()
        .detailTitle(agent.title)
        .onAppear { if machineID.isEmpty { machineID = model.hubRef(model.hubMachineID).path } } // first visit: the hub's machine
        .overlay { if loading && settings.isEmpty { ProgressView() } }
        .task(id: machineID) { await load() }
        .task(id: agent == .claude ? machineID : "") { await loadLiveReplies() }
        .refreshable {
            await load()
            await liveReplies?.load()
        }
    }

    /// The machine's Live Replies switch, read again whenever another machine is picked.
    private func loadLiveReplies() async {
        guard agent == .claude, !machineID.isEmpty else { return }
        let live = LiveRepliesModel(ref: ref, deps: .live(model))
        liveReplies = live
        await live.load()
    }

    @ViewBuilder private func row(_ s: APIClient.ClaudeSetting) -> some View {
        let value = s.value ?? s.default
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            switch s.type {
            case "bool":
                Toggle(isOn: Binding(get: { value?.bool ?? false }, set: { v in Task { await set(s.key, .bool(v)) } })) {
                    title(s)
                }
            case "enum":
                Picker(selection: Binding(get: { value?.string ?? "" }, set: { v in Task { await set(s.key, .string(v)) } })) {
                    ForEach(s.options ?? [], id: \.self) { Text(AgentSettingOptions.name($0, key: s.key)).tag($0) }
                } label: { title(s) }
            default:
                title(s)
                TextField(s.default?.string.flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "Not set"),
                          text: Binding(get: { strings[s.key] ?? value?.string ?? "" }, set: { strings[s.key] = $0 }))
                    .onSubmit { Task { await set(s.key, .string(strings[s.key] ?? "")) } }
                    .submitLabel(.done)
                    #if os(macOS)
                    .labelsHidden() // a Mac form shows a field's title as its label; here it is the prompt (#1221)
                    #endif
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    private func title(_ s: APIClient.ClaudeSetting) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.sm) {
                Text(s.title)
                if s.value == nil { Text("Default [Prompt Composer 844b8c]").font(Theme.Font.label).foregroundStyle(.tertiary) }
            }
            Text(s.detail).font(Theme.Font.label).foregroundStyle(.secondary)
        }
    }

    private func load() async {
        if model.machine(ref)?.online != true, let first = model.machines.first(where: \.online) { machineID = first.ref.path }
        let ref = ref
        guard let api = model.api(ref.group) else { return }
        loading = true
        defer { loading = false }
        do {
            settings = agent == .claude ? try await api.claudeSettings(machine: ref.id) : try await api.codexSettings(machine: ref.id)
            error = nil
        } catch {
            self.error = error.shown
        }
    }

    private func set(_ key: String, _ value: JSONValue) async {
        // Every online machine of every group of hubs, each through its own group's hub.
        let targets = applyAll ? model.machines.filter(\.online).map(\.ref) : [ref]
        var failed: [String] = []
        for target in targets {
            guard let api = model.api(target.group) else { continue }
            do {
                let list = agent == .claude ? try await api.setClaudeSetting(machine: target.id, key: key, value: value)
                    : try await api.setCodexSetting(machine: target.id, key: key, value: value)
                if target == ref { settings = list }
            } catch {
                if !error.isCancellation { failed.append(String(localized: "\(model.machine(target)?.title ?? target.id): \(error.localizedDescription)")) }
            }
        }
        if failed.isEmpty {
            Haptics.success()
            model.toast = Toast(targets.count > 1 ? String(localized: "Applied to \(targets.count) machines") : String(localized: "Saved"), style: .success)
        } else {
            Haptics.error()
            error = failed.joined(separator: "\n")
        }
    }
}

/// Settings' Machines row: N / M online. Its own view: the page read `model.machines` for it, and a machine's heartbeat (on every
/// push of a busy hub) drew the whole page and its list again (#1495); now only this row.
private struct MachinesSettingsRow: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        SettingsRow(title: String(localized: "Machines"), symbol: Theme.Icon.machines, tint: Theme.Hue.blue,
                    value: String(localized: "\(model.machines.filter(\.online).count) / \(model.machines.count) Online"))
    }
}

/// A row of Settings' list: the system's label, with what it is set to on the right; in the BeautifulUI style its list
/// row (a tinted tile, the name, the value) at the height the other styles' rows have.
struct SettingsRow: View {
    enum ValueLook { case plain, code, oneLine, digits }
    var title: String
    var symbol: String
    var tint: Color
    var value: String? = nil
    var look: ValueLook = .plain
    /// A brand's own mark (an asset of `Theme.Brand`) in place of the tinted tile, in the BeautifulUI style.
    var brand: String? = nil

    var body: some View {
        if AppStyle.current.usesBeautifulUI {
            BUIListRow(title, value: value.flatMap { $0.isEmpty ? nil : $0 }) {
                if let brand { SettingsBrandMark(asset: brand) } else { IconTile(symbol, tint: tint) }
            }
        } else if let value {
            LabeledContent {
                switch look {
                case .plain: Text(value)
                case .code: Text(value).font(Theme.Font.body.monospaced())
                case .oneLine: Text(value).lineLimit(1)
                case .digits: Text(value).monospacedDigit()
                }
            } label: {
                Label(title, systemImage: symbol)
            }
        } else {
            #if os(macOS)
            // The settings list is a sidebar-style list on the Mac (SplitNavigation), which draws a row that is only a
            // Label as a source list's vibrant label: off its material it came out gray, as if disabled, beside the
            // rows with a value (#1170). Laid out as those rows are, it is drawn as they are.
            LabeledContent { EmptyView() } label: { Label(title, systemImage: symbol) }
            #else
            Label(title, systemImage: symbol)
            #endif
        }
    }
}

/// A switch row of Settings with a mark (#1650): the system's switch with its label; in the BeautifulUI style the list row
/// `SettingsRow` draws there (the tile, the name, its description under it) with the switch at its end, so a card's switch
/// rows and the rows that open a page start with the same mark. `tint` is the tile's, `iconColor` the bare symbol's in the
/// other styles (nil leaves it the list's).
struct SettingsSwitchRow: View {
    var title: String
    var detail: String? = nil
    var symbol: String
    var tint: Color = Theme.accent
    var iconColor: Color? = nil
    /// The name's line limit in the other styles; BeautifulUI's row wraps it.
    var titleLines: Int? = nil
    /// On the switch itself, where UI tests look for it (`app.switches[…]`).
    var accessibilityID: String? = nil
    @Binding var isOn: Bool

    var body: some View {
        if AppStyle.current.usesBeautifulUI {
            BUIListRow(title, detail: detail, detailLines: .max) { IconTile(symbol, tint: tint) } trailing: {
                Toggle(title, isOn: $isOn).labelsHidden().accessibilityIdentifier(accessibilityID ?? "")
            }
            .accessibilityElement(children: .contain)
        } else {
            Toggle(isOn: $isOn) {
                Label {
                    if let detail {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(title).lineLimit(titleLines)
                            Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } else {
                        Text(title).lineLimit(titleLines)
                    }
                } icon: {
                    if let iconColor { ThemedIcon(symbol).foregroundStyle(iconColor) } else { ThemedIcon(symbol) }
                }
            }
            .accessibilityIdentifier(accessibilityID ?? "")
        }
    }
}

/// A brand's mark as a row's leading view: the size of `BUIIconTile`, so the names line up with the other rows'.
struct SettingsBrandMark: View {
    var asset: String
    @Environment(\.buiTheme) private var theme

    var body: some View {
        BrandMark(asset, side: Theme.Size.iconL * max(1, theme.typeScale * 0.9), scaled: false)
    }
}

extension View {
    /// The rows of a `List` section in the BeautifulUI style: a `BUIListRow` has its own height (its padding and minimum),
    /// so the list adds no inset above and below it on top (77.7 pt rows instead of the 52.3 pt the other styles have).
    /// Apply it to the rows (a `Group` of them), not the `Section`: on the section it takes the header's padding too, and
    /// not on a row inside a `NavigationLink`'s label (it does not reach the list there). Other styles keep the list's insets.
    func bareListRow() -> some View { listRowInsets(.vertical, AppStyle.current.usesBeautifulUI ? 0 : nil) }

    /// A row of Settings' list that opens `value`: on the Mac it stays selected while that page is open beside the list
    /// (`MacSettingsRowSelection`, #2318). Nothing elsewhere.
    @ViewBuilder func settingsListRow(_ value: some Hashable) -> some View {
        #if os(macOS)
        modifier(MacSettingsRowSelection(value: value))
        #else
        self
        #endif
    }
}

/// A section's title in Settings: the list's own; in the BeautifulUI style its heading.
struct SettingsHeading: View {
    var text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        if AppStyle.current.usesBeautifulUI { BUISectionHeading(text).textCase(nil) } else { Text(text) }
    }
}

/// Settings' buttons that are rows: the list's tinted button; in the BeautifulUI style a plain row (its tile carries the color).
struct SettingsButtonLook: ViewModifier {
    func body(content: Content) -> some View {
        #if os(macOS)
        // A row like the settings list's other rows on the Mac (#1224 B4), not the sidebar list's small gray capsule.
        content.buttonStyle(.plain)
        #else
        if AppStyle.current.usesBeautifulUI { content.buttonStyle(.plain) } else { content }
        #endif
    }
}

extension EnvironmentValues {
    /// Settings is a sheet of its own (the iPad's, docs/apple/ios.md「iPad」): its × closes it, as over the shell.
    @Entry var settingsSheet = false
}
