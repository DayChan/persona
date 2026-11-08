import PersonaSSH
import SwiftUI

/// Settings › hub › SSH Tunnel (docs/operations/network.md「经 SSH 隧道连 hub」): one paired hub's tunnel, with what it is doing now.
struct HubSSHTunnelPage: View {
    @Environment(AppModel.self) private var model
    let hubID: String

    var body: some View {
        if let hub = model.registry.hubs.first(where: { $0.id == hubID }) {
            SSHTunnelForm(saved: hub.ssh, onSave: { model.setSSH($0, hub: hubID) }) {
                if hub.ssh?.isUsable == true { status(hub) }
            }
            .detailTitle(String(localized: "SSH Tunnel"))
        } else {
            EmptyStateView(symbol: Theme.Icon.sshTunnel, title: String(localized: "This Hub Is Disconnected"), message: String(localized: "Go back to Settings › Hub to pair again."))
        }
    }

    private func status(_ hub: PairedHub) -> some View {
        let tunnels = SSHTunnels.shared
        let routed = tunnels.routed.contains(hub.id)
        let live = model.isLive(hub.group)
        let now: String = routed ? (live ? String(localized: "Connected to the hub via SSH tunnel") : String(localized: "Connecting via SSH tunnel…"))
            : live ? String(localized: "Not in use: direct connection to the hub works") : String(localized: "Not Connected")
        return Section {
            SettingsRow(title: now, symbol: routed ? Theme.Icon.sshTunnel : Theme.Icon.server, tint: routed ? Theme.positive : Theme.Hue.gray)
                .accessibilityIdentifier("ssh.status")
            if case .down(let why) = tunnels.states[hub.id] {
                Text("Last failure: \(why)").font(Theme.Font.caption).foregroundStyle(Theme.danger)
            }
        } header: {
            SettingsHeading(String(localized: "Status"))
        }
    }
}

/// The SSH tunnel's settings, on Settings › hub › SSH Tunnel and on the pairing page (before there is a hub): this device's
/// public key to copy onto the server, the server and the hub's address from there, Always Use SSH. Saving tests the whole
/// way first; the server's key is shown for the user to confirm the first time, and a changed one is refused until the
/// user trusts it again — never taken silently.
struct SSHTunnelForm<Status: View>: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    /// What is kept now (nil: none yet).
    var saved: SSHRoute?
    /// The route to keep (tested, its server's key confirmed); nil: remove it.
    var onSave: (SSHRoute?) -> Void
    /// The pairing page goes back to pairing once it is saved.
    var dismissOnSave = false
    @ViewBuilder var status: () -> Status

    // Empty port and target fields mean their defaults (shown as the prompt).
    @State private var host = ""
    @State private var port = ""
    @State private var user = ""
    @State private var targetHost = ""
    @State private var targetPort = ""
    @State private var always = false
    /// The server's key as confirmed (from what is saved, or just now).
    @State private var pinned: (fingerprint: String, algorithm: String)?
    @State private var loaded = false
    @State private var checking = false
    @State private var problem: String?
    @State private var confirm: Confirm?
    @State private var removing = false
    @State private var key: SSHIdentity?
    @State private var keyProblem: String?
    /// The fields as the form opened: in its Mac sheet Cancel asks first once they differ.
    @State private var opened: [String]?
    private var fields: [String] { [host, port, user, targetHost, targetPort, always ? "1" : "0"] }

    private enum Confirm: Identifiable {
        case unknown(fingerprint: String, algorithm: String)
        case changed(expected: String, actual: String)
        var id: String {
            switch self {
            case .unknown(let fp, _): "unknown" + fp
            case .changed(_, let fp): "changed" + fp
            }
        }
    }

    var body: some View {
        Form {
            status()
            keySection
            Section {
                field(String(localized: "Host"), $host, prompt: "ssh.example.com", id: "ssh.host")
                field(String(localized: "Port"), $port, prompt: "22", digits: true, id: "ssh.port")
                field(String(localized: "Username"), $user, prompt: String(localized: "user or user:target"), id: "ssh.user")
            } header: {
                SettingsHeading(String(localized: "SSH Server"))
            } footer: {
                Text("An SSH server this device can reach and that can reach the hub. For bastion hosts like Warpgate, write the username as user:target.")
            }
            Section {
                field(String(localized: "Address"), $targetHost, prompt: SSHRoute.defaultTargetHost, id: "ssh.targetHost")
                field(String(localized: "Port"), $targetPort, prompt: String(SSHRoute.defaultTargetPort), digits: true, id: "ssh.targetPort")
            } header: {
                SettingsHeading(String(localized: "Tunnel Target"))
            } footer: {
                Text("The hub's address as seen from the SSH server. If the hub runs on that server, use 127.0.0.1:18660.")
            }
            if let pinned {
                Section {
                    Label {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(pinned.algorithm.isEmpty ? String(localized: "Verified [S S H Tunnel View d56a51]") : String(localized: "\(pinned.algorithm) · Verified")).lineLimit(1)
                            Text(pinned.fingerprint).font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1)
                                .truncationMode(.middle).textSelection(.enabled)
                        }
                    } icon: {
                        ThemedIcon(Theme.Icon.hostKeyTrusted).foregroundStyle(Theme.positive)
                    }
                    .accessibilityIdentifier("ssh.hostKey")
                } header: {
                    SettingsHeading(String(localized: "Server Fingerprint"))
                } footer: {
                    Text("Verified on first connection. If the server's fingerprint changes later, the connection is refused, never accepted automatically.")
                }
            }
            Section {
                Toggle(isOn: $always) { Text("Always Use SSH").lineLimit(1) }
                    .accessibilityIdentifier("ssh.always")
            } footer: {
                #if os(macOS)
                Text("When off, the app connects to the hub directly first and only uses the SSH tunnel if that fails. The tunnel retries automatically after it disconnects.")
                #else
                Text("When off, the app connects to the hub directly first and only uses the SSH tunnel if that fails. The tunnel is rebuilt when the iPhone app returns to the foreground.")
                #endif
            }
            if let problem {
                Section {
                    Label {
                        Text(problem).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                    } icon: {
                        ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.danger)
                    }
                    .accessibilityIdentifier("ssh.problem")
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
                .accessibilityIdentifier("ssh.save")
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
            } footer: {
                Text("Signs in to the SSH server with the settings above and opens one forward to the tunnel target.")
            }
            if saved != nil {
                Section {
                    Button(role: .destructive) { removing = true } label: {
                        SettingsRow(title: String(localized: "Delete SSH Tunnel"), symbol: Theme.Icon.delete, tint: Theme.danger)
                    }
                    .modifier(SettingsButtonLook())
                    .bareListRow()
                }
            }
            #endif
        }
        .themedPage()
        #if os(macOS)
        .modifier(MacFormChrome(inSheet: dismissOnSave, unsaved: opened.map { $0 != fields } ?? false,
                                cancel: { dismiss() }, save: saveButton))
        #endif
        .onAppear(perform: load)
        .onChange(of: host) { _, _ in serverChanged() }
        .onChange(of: port) { _, _ in serverChanged() }
        .onChange(of: always) { _, now in alwaysChanged(now) }
        .alert(confirmTitle, isPresented: Binding(get: { confirm != nil }, set: { if !$0 { confirm = nil } }), presenting: confirm) { c in
            switch c {
            case .unknown(let fp, let alg):
                Button("Trust and Save") { Task { await testAndSave(trusting: (fp, alg)) } }
            case .changed(_, let fp):
                Button("Trust New Fingerprint", role: .destructive) { Task { await testAndSave(trusting: (fp, pinned?.algorithm ?? "")) } }
            }
            Button("Cancel", role: .cancel) {}
        } message: { c in
            switch c {
            case .unknown(let fp, let alg):
                Text("\(alg) \(fp)\\n\\nOn the server, run ssh-keygen -lf on its host public key and trust it only if the fingerprints match. If the fingerprint changes later, the connection is refused.")
            case .changed(let expected, let actual):
                Text("Pinned: \(expected)\\nNow: \(actual)\\n\\nThe server may have been reinstalled or changed its key, or someone may be in the middle. Trust it only after confirming the server changed its key.")
            }
        }
        .confirmationDialog("Delete SSH Tunnel?", isPresented: $removing, titleVisibility: .visible) {
            Button("Delete", role: .destructive) { onSave(nil); dismiss() }
        } message: {
            Text("From then on, the app connects to the hub directly only. You'll need to remove the authorized_keys line on the server yourself.")
        }
    }

    private var confirmTitle: String {
        switch confirm {
        case .changed: String(localized: "SSH Server Fingerprint Changed")
        default: String(localized: "Verify SSH Server Fingerprint")
        }
    }

    // MARK: This device's key

    @ViewBuilder private var keySection: some View {
        Section {
            if let key {
                let line = key.publicKeyLine(comment: SSHKeys.comment)
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(line).font(Theme.Font.code).lineLimit(1).truncationMode(.middle).textSelection(.enabled)
                    Text(key.fingerprint).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("ssh.publicKey")
                #if os(macOS)
                // Standard push buttons side by side, as a Mac form has them, not two tinted rows (#1170).
                HStack(spacing: Theme.Space.s) {
                    Spacer(minLength: 0)
                    Button { copy(line) } label: { Label("Copy Public Key", systemImage: Theme.Icon.sshKey).lineLimit(1) }
                        .accessibilityIdentifier("ssh.copyKey")
                    Button { copy(authorizedKeysLine(key)) } label: {
                        Label("Copy authorized_keys Line", systemImage: Theme.Icon.copy).lineLimit(1)
                    }
                    .accessibilityIdentifier("ssh.copyAuthorizedKeys")
                }
                .buttonStyle(.bordered)
                #else
                Button { copy(line) } label: {
                    SettingsRow(title: String(localized: "Copy Public Key"), symbol: Theme.Icon.sshKey, tint: Theme.accent)
                }
                .modifier(SettingsButtonLook())
                .accessibilityIdentifier("ssh.copyKey")
                .bareListRow()
                Button { copy(authorizedKeysLine(key)) } label: {
                    SettingsRow(title: String(localized: "Copy authorized_keys Line"), symbol: Theme.Icon.copy, tint: Theme.accent)
                }
                .modifier(SettingsButtonLook())
                .accessibilityIdentifier("ssh.copyAuthorizedKeys")
                .bareListRow()
                #endif
            } else if let keyProblem {
                Text(keyProblem).font(Theme.Font.secondary).foregroundStyle(Theme.danger)
            }
        } header: {
            SettingsHeading(String(localized: "This Device's Public Key"))
        } footer: {
            Text("Add the “authorized_keys line” to this user's ~/.ssh/authorized_keys on the SSH server. It carries restrict and permitopen, so this key can only forward to the tunnel target below and can't open a shell. The private key stays in this device's keychain.")
        }
    }

    private func authorizedKeysLine(_ key: SSHIdentity) -> String {
        key.authorizedKeysLine(targetHost: targetHostValue, targetPort: Int(targetPort) ?? SSHRoute.defaultTargetPort, comment: SSHKeys.comment)
    }

    #if os(macOS)
    // MARK: Mac buttons

    /// Test Connection and Save as a Mac button: the sheet's default button in its bar, or a standard one at the page's end.
    private var saveButton: some View {
        Button { Task { await testAndSave() } } label: {
            HStack(spacing: Theme.Space.s) {
                if checking { ProgressView().controlSize(.small) }
                Text(checking ? String(localized: "Testing…") : String(localized: "Test Connection and Save")).lineLimit(1)
            }
        }
        .disabled(checking || draft == nil)
        .accessibilityIdentifier("ssh.save")
    }

    /// The page's own buttons at the end of the form (in a sheet, saving is in its bar): standard ones at the trailing
    /// edge, not full-width tinted capsules (#1170).
    @ViewBuilder private var macActions: some View {
        if saved != nil || !dismissOnSave {
        Section {
            HStack(spacing: Theme.Space.s) {
                if saved != nil {
                    Button(role: .destructive) { removing = true } label: { Text("Delete SSH Tunnel…").lineLimit(1) }
                        .buttonStyle(.bordered)
                }
                Spacer(minLength: 0)
                if !dismissOnSave { saveButton.buttonStyle(.borderedProminent) }
            }
        } footer: {
            Text("Signs in to the SSH server with the settings above and opens one forward to the tunnel target.")
        }
        }
    }
    #endif

    private func copy(_ s: String) {
        UIPasteboard.general.string = s
        Haptics.tap()
        model.toast = Toast(String(localized: "Copied [V N C Pi P Diagnostics 8f6f8d]"), style: .success)
    }

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

    private var targetHostValue: String {
        let t = targetHost.trimmingCharacters(in: .whitespaces)
        return t.isEmpty ? SSHRoute.defaultTargetHost : t
    }

    /// A port field: its number, the default when empty, nil when it is not a port.
    private static func portValue(_ s: String, default d: Int) -> Int? {
        let t = s.trimmingCharacters(in: .whitespaces)
        guard !t.isEmpty else { return d }
        guard let p = Int(t), (1...65535).contains(p) else { return nil }
        return p
    }

    /// What the fields say, with the key confirmed for this server; nil while a port is not a number.
    private var draft: SSHRoute? {
        let h = host.trimmingCharacters(in: .whitespaces), u = user.trimmingCharacters(in: .whitespaces)
        guard !h.isEmpty, !u.isEmpty, let p = Self.portValue(port, default: 22),
              let tp = Self.portValue(targetPort, default: SSHRoute.defaultTargetPort) else { return nil }
        return SSHRoute(host: h, port: p, user: u, targetHost: targetHostValue, targetPort: tp, hostKey: pinned?.fingerprint,
                        hostKeyAlgorithm: pinned?.algorithm, always: always)
    }

    private func load() {
        if key == nil {
            do { key = try SSHKeys.identity() } catch { keyProblem = error.localizedDescription }
        }
        guard !loaded else { return }
        loaded = true
        defer { opened = fields }
        guard let saved else { return }
        host = saved.host
        port = saved.port == 22 ? "" : String(saved.port)
        user = saved.user
        targetHost = saved.targetHost == SSHRoute.defaultTargetHost ? "" : saved.targetHost
        targetPort = saved.targetPort == SSHRoute.defaultTargetPort ? "" : String(saved.targetPort)
        always = saved.always
        if let fp = saved.hostKey { pinned = (fp, saved.hostKeyAlgorithm ?? "") }
    }

    /// Another server: the key confirmed was the old one's.
    private func serverChanged() {
        guard loaded, let pinnedFor = saved, pinned != nil else { return }
        if host.trimmingCharacters(in: .whitespaces) != pinnedFor.host || Self.portValue(port, default: 22) != pinnedFor.port { pinned = nil }
    }

    /// Always Use SSH on a tunnel already saved, nothing else changed: kept right away (no test needed for it).
    private func alwaysChanged(_ now: Bool) {
        guard loaded, var kept = saved, kept.isUsable, kept.always != now, var d = draft else { return }
        d.always = kept.always
        guard d == kept else { return }
        kept.always = now
        onSave(kept)
    }

    // MARK: Test and save

    private func testAndSave(trusting key: (fingerprint: String, algorithm: String)? = nil) async {
        guard var route = draft else { return }
        if let key {
            route.hostKey = key.fingerprint
            route.hostKeyAlgorithm = key.algorithm
        }
        checking = true
        problem = nil
        defer { checking = false }
        do {
            let seen = try await SSHTunnels.shared.verify(route)
            route.hostKey = seen.fingerprint
            route.hostKeyAlgorithm = seen.algorithm
            pinned = seen
            onSave(route)
            Haptics.success()
            model.toast = Toast(String(localized: "SSH tunnel saved"), style: .success)
            if dismissOnSave { dismiss() }
        } catch SSHTunnelError.hostKeyUnknown(let fp, let alg) {
            Haptics.attention()
            confirm = .unknown(fingerprint: fp, algorithm: alg)
        } catch SSHTunnelError.hostKeyChanged(let expected, let actual) {
            Haptics.error()
            confirm = .changed(expected: expected, actual: actual)
        } catch {
            Haptics.error()
            problem = Self.advice(error, route)
        }
    }

    /// What went wrong and what to do about it.
    static func advice(_ error: Error, _ route: SSHRoute) -> String {
        guard let e = error as? SSHTunnelError else { return error.localizedDescription }
        let target = route.targetHost.contains(":") ? "[\(route.targetHost)]:\(route.targetPort)" : "\(route.targetHost):\(route.targetPort)"
        return switch e {
        case .notAuthorized:
            String(localized: "The SSH server didn't accept this device's public key. Add the line from Copy authorized_keys Line to ~/.ssh/authorized_keys for \(route.user) on the server (Warpgate: add it to this user's public keys), then try again.")
        case .targetRefused(let why):
            String(localized: "Signed in, but the server couldn't open a forward to \(target) (\(why)). Check the tunnel target, that the hub is on that port, and that the permitopen address in authorized_keys matches this one.")
        case .unreachable(let why):
            String(localized: "Can't reach \(route.host):\(String(route.port)) (\(why)). Check the address and port, and whether this device can reach it.")
        case .handshake(let why):
            String(localized: "SSH handshake failed (\(why)). The server needs to support Ed25519 or ECDSA host keys and AES-GCM encryption.")
        default:
            e.localizedDescription
        }
    }
}

extension SSHTunnelForm where Status == EmptyView {
    init(saved: SSHRoute?, dismissOnSave: Bool = false, onSave: @escaping (SSHRoute?) -> Void) {
        self.init(saved: saved, onSave: onSave, dismissOnSave: dismissOnSave, status: { EmptyView() })
    }
}

#if os(macOS)
/// The SSH form's frame on the Mac: a sheet's size and bar (Cancel, and saving as the default button), or the page's
/// grouped form in the detail column.
private struct MacFormChrome<Save: View>: ViewModifier {
    var inSheet: Bool
    var unsaved: Bool
    var cancel: () -> Void
    var save: Save

    func body(content: Content) -> some View {
        if inSheet {
            content
                .macSheet()
                .sheetCancel(unsaved: unsaved, cancel: cancel)
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) { save }
                }
        } else {
            content.formStyle(.grouped)
        }
    }
}
#endif
