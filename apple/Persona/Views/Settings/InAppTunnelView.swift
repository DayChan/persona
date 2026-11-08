import SwiftUI
#if !os(macOS)
import UIKit
import WebKit
#endif

/// Settings › hub › the in-app tunnel (docs/operations/network.md「经 App 内隧道连 hub」): one paired hub's tunnel, the account it signs
/// in with, and what it is doing now. Only a hub whose extension contributes a tunnel has the page, titled with the
/// tunnel's name; where the tunnel goes is that extension's setting on the hub, shown here as it is. 「总是走隧道」 is
/// this phone's.
struct HubInAppTunnelPage: View {
    @Environment(AppModel.self) private var model
    let hubID: String

    @State private var checking = false
    @State private var problem: String?
    @State private var core: InAppTunnel.Status?
    @State private var coreProblem: String?
    @State private var panel: URL?

    var body: some View {
        if let hub = model.registry.hubs.first(where: { $0.id == hubID }), let route = hub.inAppTunnel {
            form(hub, route)
                .detailTitle(route.title)
                .task { await watchCore() }
                #if !os(macOS)
                .sheet(isPresented: Binding(get: { panel != nil }, set: { if !$0 { panel = nil } })) {
                    if let panel { InAppTunnelPanelSheet(url: panel, title: route.title) }
                }
                #endif
        } else {
            EmptyStateView(symbol: Theme.Icon.inAppTunnel, title: String(localized: "This hub has no tunnel"),
                           message: String(localized: "Install and turn on an extension that provides a tunnel on the hub, then come back here."))
        }
    }

    private func form(_ hub: PairedHub, _ route: InAppTunnelRoute) -> some View {
        Form {
            if !InAppTunnel.available {
                Section {
                    Label {
                        Text("This build of the app has no tunnel core.").font(Theme.Font.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.caution)
                    }
                    .accessibilityIdentifier("tunnel.missing")
                }
            } else if route.isUsable {
                status(hub)
            }
            account(route)
            Section {
                LabeledContent {
                    Text(verbatim: route.targetHost).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                } label: {
                    Text("Address").lineLimit(1)
                }
                .accessibilityIdentifier("tunnel.targetHost")
                LabeledContent {
                    Text(verbatim: String(route.targetPort)).foregroundStyle(.secondary).lineLimit(1)
                } label: {
                    Text("Port").lineLimit(1)
                }
                .accessibilityIdentifier("tunnel.targetPort")
            } header: {
                SettingsHeading(String(localized: "Tunnel Target"))
            } footer: {
                Text("The hub's address and HTTP port in the network on the other side of the tunnel. Change them in this extension's settings on the hub.")
            }
            Section {
                Toggle(isOn: Binding(get: { route.always }, set: { on in
                    var r = route
                    r.always = on
                    model.setInAppTunnel(r, hub: hubID)
                })) { Text("Always Use the Tunnel").lineLimit(1) }
                    .accessibilityIdentifier("tunnel.always")
            } footer: {
                Text("When off, the hub is tried directly first and the tunnel is used only if that fails. Only Persona's traffic to the hub goes through it; other apps aren't affected. The tunnel disconnects when Persona goes to the background and reconnects when it comes back.")
            }
            if let problem {
                Section {
                    Label {
                        Text(problem).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.danger)
                    }
                    .accessibilityIdentifier("tunnel.problem")
                }
            }
            Section {
                Button { Task { await test(route) } } label: {
                    HStack(spacing: Theme.Space.s) {
                        if checking { ProgressView() }
                        Text(checking ? String(localized: "Testing…") : String(localized: "Test Connection")).lineLimit(1).minimumScaleFactor(0.8)
                    }
                    .frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
                .disabled(checking || !route.isUsable || !InAppTunnel.available)
                .accessibilityIdentifier("tunnel.test")
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            } footer: {
                Text("Connects the tunnel, then reaches the hub through it once.")
            }
        }
        .themedPage()
    }

    private func status(_ hub: PairedHub) -> some View {
        let tunnels = InAppTunnels.shared
        let routed = tunnels.routed.contains(hub.id)
        let live = model.isLive(hub.group)
        let now: String = routed ? (live ? String(localized: "Connected to the hub through the tunnel") : String(localized: "Connecting through the tunnel…"))
            : live ? String(localized: "Not in use: direct connection to the hub works") : String(localized: "Not Connected")
        return Section {
            SettingsRow(title: now, symbol: routed ? Theme.Icon.inAppTunnel : Theme.Icon.server, tint: routed ? Theme.positive : Theme.Hue.gray)
                .accessibilityIdentifier("tunnel.status")
            if case .down(let why) = tunnels.states[hub.id] {
                Text("Last failure: \(why)").font(Theme.Font.caption).foregroundStyle(Theme.danger)
            }
        } header: {
            SettingsHeading(String(localized: "Status"))
        }
    }

    // MARK: The tunnel's account

    @ViewBuilder private func account(_ route: InAppTunnelRoute) -> some View {
        Section {
            SettingsRow(title: String(localized: "Accounts"), symbol: Theme.Icon.siteLogin, tint: Theme.Hue.blue, value: accountValue, look: .oneLine)
                .accessibilityIdentifier("tunnel.account")
            Button { Task { await openPanel(route) } } label: {
                SettingsRow(title: String(localized: "Sign In and Pick a Node"), symbol: Theme.Icon.openExternal, tint: Theme.accent)
            }
            .modifier(SettingsButtonLook())
            .disabled(!InAppTunnel.available)
            .accessibilityIdentifier("tunnel.panel")
            .bareListRow()
            if let coreProblem {
                Text(coreProblem).font(Theme.Font.caption).foregroundStyle(Theme.danger)
            }
        } header: {
            SettingsHeading(String(localized: "Tunnel Account"))
        } footer: {
            Text("The first time, sign in on the panel, then pick a node. The sign-in stays on this device and isn't uploaded to the hub.")
        }
    }

    private var accountValue: String {
        guard let core else { return String(localized: "Not Started") }
        if !core.loggedIn || !core.hasServer { return String(localized: "Not signed in") }
        if core.needsMfa == true { return String(localized: "Needs Two-Step Verification") }
        if core.connected { return core.serverName.map { String(localized: "Connected · \($0)") } ?? String(localized: "Connected [V N C Page 5be032]") }
        return String(localized: "Signed in")
    }

    private func openPanel(_ route: InAppTunnelRoute) async {
        do {
            panel = try await InAppTunnel.start(probe: route)
            coreProblem = nil
        } catch {
            coreProblem = error.localizedDescription
        }
    }

    /// The core's login and tunnel as they change, while the page is open.
    private func watchCore() async {
        while !Task.isCancelled {
            let s = await InAppTunnel.status()
            if s != core { core = s }
            try? await Task.sleep(for: .seconds(2))
        }
    }

    private func test(_ route: InAppTunnelRoute) async {
        checking = true
        problem = nil
        defer { checking = false }
        do {
            try await InAppTunnels.shared.verify(route)
            Haptics.tap()
            model.toast = Toast(String(localized: "Reached the hub through the tunnel"), style: .success)
        } catch {
            problem = error.localizedDescription
        }
    }
}

// The panel is UIKit (a WKWebView in a UIView, UIAlertController for its alerts). The Mac app builds this folder too,
// and the tunnel core is only linked into iOS device builds, so the Mac never opens it.
#if !os(macOS)
/// The tunnel core's own control panel (on loopback, in this process): where its account signs in and a node is
/// picked. A sign-in that opens outside the app may end with a link the user copies there; it is offered to the core
/// from the clipboard when the user comes back (InAppTunnel.offerLink).
struct InAppTunnelPanelSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase
    let url: URL
    let title: String
    @State private var store = InAppTunnelPanelStore()

    var body: some View {
        NavigationStack {
            InAppTunnelPanelView(url: url, store: store)
                .ignoresSafeArea(edges: .bottom)
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button { store.reload() } label: { ThemedIcon(Theme.Icon.reload) }
                            .accessibilityLabel(String(localized: "Reload [Lark Chat Pins View 7bdd5c]"))
                    }
                    ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1) }
                }
        }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { store.pickUpLoginLink() }
        }
    }
}

@MainActor
final class InAppTunnelPanelStore: NSObject, WKNavigationDelegate, WKUIDelegate {
    private(set) var webView: WKWebView?
    private var url: URL?
    /// When the panel last sent a link out of the app (a sign-in), and the clipboard's change count then.
    private var sentOutAt: Date?
    private var sentOutPasteboard = 0

    /// One web view per sheet: a new one would reload the login page, which starts a new authorisation and voids the
    /// one the user is finishing outside the app.
    func makeWebView(url: URL) -> WKWebView {
        if let webView { return webView }
        let webView = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        webView.navigationDelegate = self
        webView.uiDelegate = self
        self.webView = webView
        self.url = url
        webView.load(URLRequest(url: url))
        return webView
    }

    func reload() {
        guard let webView else { return }
        if webView.url == nil, let url { webView.load(URLRequest(url: url)) } else { webView.reload() }
    }

    /// Back within 15 minutes of the panel sending a link out, with something copied since: offered to the core, and
    /// the panel reloaded when it took it. Reading the clipboard makes iOS ask「允许粘贴」 once.
    func pickUpLoginLink() {
        guard let sent = sentOutAt, Date().timeIntervalSince(sent) < 15 * 60 else { return }
        let board = UIPasteboard.general
        guard board.changeCount != sentOutPasteboard, board.hasStrings, let text = board.string else { return }
        let link = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !link.isEmpty, link.count <= 4096 else { return }
        sentOutPasteboard = board.changeCount
        Task {
            guard await InAppTunnel.offerLink(link) else { return }
            sentOutAt = nil
            reload()
        }
    }

    private func sendOut(_ url: URL) {
        sentOutAt = Date()
        sentOutPasteboard = UIPasteboard.general.changeCount
        UIApplication.shared.open(url)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction) async -> WKNavigationActionPolicy {
        guard let url = navigationAction.request.url else { return .allow }
        let host = url.host() ?? ""
        if host == "127.0.0.1" || host == "localhost" || ["about", "blob", "data"].contains(url.scheme ?? "") { return .allow }
        sendOut(url)
        return .cancel
    }

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = navigationAction.request.url { sendOut(url) }
        return nil
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo) async {
        _ = await present(message, cancel: nil, ok: String(localized: "OK [In App Tunnel View f867f3]"))
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo) async -> Bool {
        await present(message, cancel: String(localized: "Cancel"), ok: String(localized: "OK [Interactive Assistant View Card fac2a6]"))
    }

    private func present(_ message: String, cancel: String?, ok: String) async -> Bool {
        guard let root = webView?.window?.rootViewController else { return false }
        return await withCheckedContinuation { done in
            let alert = UIAlertController(title: nil, message: message, preferredStyle: .alert)
            if let cancel { alert.addAction(UIAlertAction(title: cancel, style: .cancel) { _ in done.resume(returning: false) }) }
            alert.addAction(UIAlertAction(title: ok, style: .default) { _ in done.resume(returning: true) })
            Self.topmost(root).present(alert, animated: true)
        }
    }

    private static func topmost(_ c: UIViewController) -> UIViewController {
        var c = c
        while let p = c.presentedViewController { c = p }
        return c
    }
}

/// The panel's web view, moved into whichever container is on screen (it outlives the view: see InAppTunnelPanelStore).
struct InAppTunnelPanelView: UIViewRepresentable {
    let url: URL
    let store: InAppTunnelPanelStore

    func makeUIView(context: Context) -> UIView {
        let container = UIView()
        adopt(into: container)
        return container
    }

    func updateUIView(_ uiView: UIView, context: Context) { adopt(into: uiView) }

    private func adopt(into container: UIView) {
        let webView = store.makeWebView(url: url)
        guard webView.superview !== container else { return }
        webView.frame = container.bounds
        webView.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        container.addSubview(webView)
    }
}
#endif
