import SwiftUI

/// Settings › hub › Local Proxy (docs/operations/network.md "Connecting to hub through a local SOCKS5 proxy"): one paired
/// hub's SOCKS5 proxy, with what it is doing now.
struct HubLocalProxyPage: View {
    @Environment(AppModel.self) private var model
    let hubID: String

    var body: some View {
        if let hub = model.registry.hubs.first(where: { $0.id == hubID }) {
            LocalProxyForm(hub: hub.config.url, hubID: hub.hubID, saved: hub.localProxy, onSave: { model.setLocalProxy($0, hub: hubID) }) {
                if hub.localProxy?.isUsable == true { status(hub) }
            }
            .detailTitle(String(localized: "Local Proxy"))
        } else {
            EmptyStateView(symbol: Theme.Icon.localProxy, title: String(localized: "This Hub Is Disconnected"), message: String(localized: "Go back to Settings › Hub to pair again."))
        }
    }

    private func status(_ hub: PairedHub) -> some View {
        let proxies = LocalProxies.shared
        let routed = proxies.routed.contains(hub.id)
        let live = model.isLive(hub.group)
        let now: String = routed ? (live ? String(localized: "Connected to the hub through the local proxy") : String(localized: "Connecting through the local proxy…"))
            : live ? String(localized: "Not in use: direct connection to the hub works") : String(localized: "Not Connected")
        return Section {
            SettingsRow(title: now, symbol: routed ? Theme.Icon.localProxy : Theme.Icon.server, tint: routed ? Theme.positive : Theme.Hue.gray)
                .accessibilityIdentifier("proxy.status")
            if case .down(let why) = proxies.states[hub.id] {
                Text("Last failure: \(why)").font(Theme.Font.caption).foregroundStyle(Theme.danger)
            }
        } header: {
            SettingsHeading(String(localized: "Status"))
        }
    }
}

/// The local proxy's settings: the SOCKS5 proxy another app on this device serves, the login it may ask for, the hub's
/// address as the proxy's network sees it, Always Use the Proxy. Saving asks the hub for its health through the proxy
/// first; nothing is kept that does not get there.
struct LocalProxyForm<Status: View>: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    /// The paired address: what the hub's address defaults to.
    var hub: URL
    /// The hub's own id once it said it: Test Connection makes sure it is this hub that answers.
    var hubID: String?
    /// What is kept now (nil: none yet).
    var saved: LocalProxyRoute?
    /// The route to keep (tested); nil: remove it.
    var onSave: (LocalProxyRoute?) -> Void
    @ViewBuilder var status: () -> Status

    // Empty host and hub address fields mean their defaults (shown as the prompt); the proxy's port has none.
    @State private var host = ""
    @State private var port = ""
    @State private var username = ""
    @State private var password = ""
    @State private var targetHost = ""
    @State private var targetPort = ""
    @State private var always = false
    @State private var loaded = false
    @State private var checking = false
    @State private var problem: String?
    @State private var removing = false

    var body: some View {
        Form {
            status()
            Section {
                field(String(localized: "Host"), $host, prompt: LocalProxyRoute.defaultHost, id: "proxy.host")
                field(String(localized: "Port"), $port, prompt: String(localized: "Required"), digits: true, id: "proxy.port")
                if draft?.isOnThisDevice == false {
                    Label {
                        Text("This proxy isn't on this device: Persona's traffic to the hub, this device's credential and the proxy's password would cross the network unencrypted.")
                            .font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.caution)
                    }
                    .accessibilityIdentifier("proxy.remote")
                }
            } header: {
                SettingsHeading(String(localized: "SOCKS5 Proxy"))
            } footer: {
                Text("A SOCKS5 proxy that another app on this device provides, such as a VPN-like client that serves 127.0.0.1 instead of taking the system VPN slot. Persona's traffic to the hub, this device's credential included, goes through it unencrypted, even for an https hub: you're trusting whatever app holds that port.")
            }
            Section {
                field(String(localized: "Username"), $username, prompt: String(localized: "Optional"), id: "proxy.username")
                passwordField
            } header: {
                SettingsHeading(String(localized: "Proxy Login"))
            } footer: {
                Text("Only if the proxy asks for one. It's kept with this hub's pairing in the keychain.")
            }
            Section {
                field(String(localized: "Address"), $targetHost, prompt: LocalProxyRoute.host(of: hub), id: "proxy.targetHost")
                field(String(localized: "Port"), $targetPort, prompt: String(LocalProxyRoute.defaultTargetPort(for: hub)), digits: true, id: "proxy.targetPort")
            } header: {
                SettingsHeading(String(localized: "Hub Address"))
            } footer: {
                Text("The hub's HTTP address as seen from the proxy. Leave it empty to use the paired address.")
            }
            Section {
                Toggle(isOn: $always) { Text("Always Use the Proxy").lineLimit(1) }
                    .accessibilityIdentifier("proxy.always")
            } footer: {
                Text("When off, the app connects to the hub directly first and only uses the proxy if that fails, then checks every 2 minutes whether the direct connection works again. Only Persona's traffic to the hub goes through the proxy.")
            }
            if let problem {
                Section {
                    Label {
                        Text(problem).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.danger)
                    }
                    .accessibilityIdentifier("proxy.problem")
                }
            }
            #if os(macOS)
            macActions
            #else
            Section {
                Button { Task { await testAndSave() } } label: {
                    HStack(spacing: Theme.Space.s) {
                        if checking { ProgressView() }
                        Text(checking ? String(localized: "Testing…") : String(localized: "Test Connection and Save")).lineLimit(1).minimumScaleFactor(0.8)
                    }
                    .frame(maxWidth: .infinity)
                }
                .themedButton(prominent: true)
                .disabled(checking || draft == nil)
                .accessibilityIdentifier("proxy.save")
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            } footer: {
                Text("Connects through the proxy to the hub's address and checks that the hub answers.")
            }
            if saved != nil {
                Section {
                    Button(role: .destructive) { removing = true } label: {
                        SettingsRow(title: String(localized: "Delete Local Proxy"), symbol: Theme.Icon.delete, tint: Theme.danger)
                    }
                    .modifier(SettingsButtonLook())
                    .bareListRow()
                }
            }
            #endif
        }
        .themedPage()
        #if os(macOS)
        .formStyle(.grouped)
        #endif
        .onAppear(perform: load)
        .onChange(of: always) { _, now in alwaysChanged(now) }
        .confirmationDialog("Delete Local Proxy?", isPresented: $removing, titleVisibility: .visible) {
            Button("Delete", role: .destructive) { onSave(nil); dismiss() }
        } message: {
            Text("Persona stops using this proxy to reach the hub.")
        }
    }

    #if os(macOS)
    // MARK: Mac buttons

    /// The page's own buttons at the end of the form: standard ones at the trailing edge, not full-width tinted
    /// capsules (#1170).
    private var macActions: some View {
        Section {
            HStack(spacing: Theme.Space.s) {
                if saved != nil {
                    Button(role: .destructive) { removing = true } label: { Text("Delete Local Proxy…").lineLimit(1) }
                        .buttonStyle(.bordered)
                }
                Spacer(minLength: 0)
                Button { Task { await testAndSave() } } label: {
                    HStack(spacing: Theme.Space.s) {
                        if checking { ProgressView().controlSize(.small) }
                        Text(checking ? String(localized: "Testing…") : String(localized: "Test Connection and Save")).lineLimit(1)
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(checking || draft == nil)
                .accessibilityIdentifier("proxy.save")
            }
        } footer: {
            Text("Connects through the proxy to the hub's address and checks that the hub answers.")
        }
    }
    #endif

    // MARK: Fields

    @ViewBuilder
    private func field(_ title: String, _ text: Binding<String>, prompt: String, digits: Bool = false, id: String) -> some View {
        #if os(macOS)
        TextField(title, text: text, prompt: Text(verbatim: prompt))
            .autocorrectionDisabled()
            .accessibilityIdentifier(id)
        #else
        LabeledContent {
            TextField(title, text: text, prompt: Text(verbatim: prompt))
                .multilineTextAlignment(.trailing)
                .keyboardType(digits ? .numberPad : .asciiCapable)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier(id)
        } label: {
            Text(title).lineLimit(1)
        }
        #endif
    }

    /// The proxy's password: a hidden field that is never offered to the system's password manager (beside the
    /// username it would look like a login form, #1512).
    @ViewBuilder private var passwordField: some View {
        #if os(macOS)
        SecureField(String(localized: "Password"), text: $password, prompt: Text("Optional"))
            .secretEntry()
            .accessibilityIdentifier("proxy.password")
        #else
        LabeledContent {
            SecretField(String(localized: "Optional"), text: $password, identifier: "proxy.password")
                .multilineTextAlignment(.trailing)
        } label: {
            Text("Password").lineLimit(1)
        }
        #endif
    }

    /// A port field's number; nil when it is not a port.
    static func portValue(_ s: String) -> Int? {
        guard let p = Int(s.trimmingCharacters(in: .whitespaces)), (1...65535).contains(p) else { return nil }
        return p
    }

    /// What the fields say; nil while the proxy's port is missing or a port is not a number.
    private var draft: LocalProxyRoute? {
        guard let p = Self.portValue(port) else { return nil }
        let typedTarget = targetPort.trimmingCharacters(in: .whitespaces)
        var tp: Int?
        if !typedTarget.isEmpty {
            guard let v = Self.portValue(typedTarget) else { return nil }
            tp = v
        }
        let h = host.trimmingCharacters(in: .whitespaces), th = targetHost.trimmingCharacters(in: .whitespaces)
        return LocalProxyRoute(host: h.isEmpty ? LocalProxyRoute.defaultHost : h, port: p,
                               username: username.trimmingCharacters(in: .whitespaces), password: password,
                               targetHost: th.isEmpty ? nil : th, targetPort: tp, always: always)
    }

    private func load() {
        guard !loaded else { return }
        loaded = true
        guard let saved else { return }
        host = saved.host == LocalProxyRoute.defaultHost ? "" : saved.host
        port = String(saved.port)
        username = saved.username
        password = saved.password
        targetHost = saved.targetHost ?? ""
        targetPort = saved.targetPort.map(String.init) ?? ""
        always = saved.always
    }

    /// Always Use the Proxy on a proxy already saved, nothing else changed: kept right away (no test needed for it).
    private func alwaysChanged(_ now: Bool) {
        guard loaded, var kept = saved, kept.isUsable, kept.always != now, var d = draft else { return }
        d.always = kept.always
        guard d == kept else { return }
        kept.always = now
        onSave(kept)
    }

    // MARK: Test and save

    private func testAndSave() async {
        guard let route = draft else { return }
        checking = true
        problem = nil
        defer { checking = false }
        do {
            try await LocalProxies.shared.verify(route, hub: hub, hubID: hubID)
            onSave(route)
            Haptics.success()
            model.toast = Toast(String(localized: "Local proxy saved"), style: .success)
        } catch {
            Haptics.error()
            problem = Self.advice(error, route, hub: hub)
        }
    }

    /// What went wrong and what to do about it.
    static func advice(_ error: Error, _ route: LocalProxyRoute, hub: URL) -> String {
        guard let e = error as? SOCKS5Error else { return error.localizedDescription }
        return switch e {
        case .unreachable(let why):
            String(localized: "Can't reach the proxy at \(route.label) (\(why)). Check that the app providing it is running, and its port.")
        case .notSOCKS5:
            String(localized: "\(route.label) didn't answer as a SOCKS5 proxy. Check the port; HTTP proxies aren't supported.")
        case .connectFailed(let code):
            String(localized: "The proxy couldn't connect to \(route.dial(for: hub).target) (\(SOCKS5Error.reply(code))). Check the hub's address as seen from the proxy.")
        default:
            e.localizedDescription
        }
    }
}
