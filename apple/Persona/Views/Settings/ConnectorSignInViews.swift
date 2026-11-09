import PersonaKit
import SwiftUI

/// What the connector page needs from the app (tests: a FakeHub client).
struct ConnectorDeps {
    var api: () -> APIClient?
    var report: (Error) -> Void
    /// One step of the sign-in: a connector's login (the default), or a channel's binding (`ChannelBinding`).
    var login: (APIClient, _ id: String, _ step: String, _ values: [String: String]) async throws -> ConnectorPrompt = { api, id, step, values in
        try await api.connectorLogin(id, step: step, values: values)
    }

    static var live: ConnectorDeps {
        let model = AppModel.shared
        return ConnectorDeps(api: { model.api }, report: { model.show($0) })
    }
}

/// Settings › Connectors › Telegram's own part (a connector the user signs in to here, `HubConnector.signsInHere`): its switch,
/// the sign-in a step at a time (the hub says what each step asks for: docs/archive/extensions/plan-telegram.md), signing out and
/// deleting what it brought in.
@Observable final class ConnectorSignInModel {
    let id: String
    /// Whose copy (another manager's: every request names it, docs/assistant/managers.md "Grouping"); nil: the default's.
    let manager: String?
    /// The step on screen; nil when not signing in.
    private(set) var prompt: ConnectorPrompt?
    /// A QR step's code as a picture for Save QR Code, drawn once per code.
    private(set) var qrImage: UIImage?
    var values: [String: String] = [:]
    private(set) var busy = false
    /// What the last delete removed, said for people.
    private(set) var deleted: String?
    /// What the last Delete Saved Knowledge removed, said for people.
    private(set) var archiveDeleted: String?
    @ObservationIgnored private let deps: ConnectorDeps

    init(id: String, manager: String? = nil, deps: ConnectorDeps = .live) {
        self.id = id
        self.manager = manager
        self.deps = deps
    }

    /// The client for this connector's requests: its manager named on each.
    private var api: APIClient? { deps.api()?.manager(manager) }

    /// Every field of the step on screen is filled in.
    var canSend: Bool {
        guard let fields = prompt?.fields, !busy else { return false }
        return fields.allSatisfy { !(values[$0.name] ?? "").trimmingCharacters(in: .whitespaces).isEmpty }
    }

    func start() async { await send(step: "", values: [:]) }

    /// From the first step again (a step this app cannot draw: maybe the hub sends another kind the next time).
    func restart() async {
        cancel()
        await start()
    }

    /// Sends the step on screen; the answer is the next one, the same one with what was wrong, or done.
    @discardableResult
    func next() async -> Bool {
        guard let step = prompt?.step else { return false }
        return await send(step: step, values: values)
    }

    /// A code on screen (GitHub's device flow, a QR code to scan): asks again every interval the hub gave until the user
    /// has entered or scanned it (true: signed in) or the step changes; cancelled with the page.
    func waitForAuthorization() async -> Bool {
        while let p = prompt, p.polls, !Task.isCancelled {
            try? await Task.sleep(for: .seconds(max(p.interval ?? 5, 1)))
            if Task.isCancelled || prompt?.step != p.step { return false }
            if await send(step: p.step ?? "", values: [:]) { return true }
        }
        return false
    }

    func cancel() {
        prompt = nil
        qrImage = nil
        values = [:]
    }

    /// True when signing in is done (the page reloads the connector).
    @discardableResult
    private func send(step: String, values: [String: String]) async -> Bool {
        guard let api else { return false }
        busy = true
        defer { busy = false }
        do {
            let p = try await deps.login(api, id, step, values)
            if p.done == true {
                cancel()
                return true
            }
            if p.step != prompt?.step { self.values = [:] } // a new step: its fields start empty
            if !p.isQRCode {
                qrImage = nil
            } else if let rows = p.qr?.rows, rows != prompt?.qr?.rows || qrImage == nil {
                qrImage = QRCodeView.image(rows)
            }
            prompt = p
        } catch {
            deps.report(error)
        }
        return false
    }

    func setEnabled(_ on: Bool) async -> HubConnector? {
        guard let api else { return nil }
        do {
            return try await api.setConnector(id, enabled: on)
        } catch {
            deps.report(error)
            return nil
        }
    }

    func logout() async {
        guard let api else { return }
        busy = true
        defer { busy = false }
        do {
            try await api.connectorLogout(id)
            cancel()
        } catch {
            deps.report(error)
        }
    }

    func deleteData() async {
        guard let api else { return }
        busy = true
        defer { busy = false }
        do {
            let r = try await api.deleteConnectorData(id)
            deleted = String(localized: "Deleted. Knowledge: chats \(r.chats ?? 0), people \(r.people ?? 0). Inbox: items \(r.inbox ?? 0).")
        } catch {
            deps.report(error)
        }
    }

    func setFeed(_ feed: ConnectorFeed, on: Bool) async -> HubConnector? {
        guard let api else { return nil }
        do {
            return try await api.setConnector(id, feed: feed, on: on)
        } catch {
            deps.report(error)
            return nil
        }
    }

    func deleteArchive() async {
        guard let api else { return }
        busy = true
        defer { busy = false }
        do {
            let r = try await api.deleteConnectorArchive(id)
            archiveDeleted = String(localized: "Deleted. Knowledge: chats \(r.chats ?? 0), people \(r.people ?? 0).")
        } catch {
            deps.report(error)
        }
    }
}

/// The sections of a connector the user signs in to here, under its status on `HubConnectorView`.
struct ConnectorSignInSections: View {
    var connector: HubConnector
    var reload: () async -> Void
    @State private var model: ConnectorSignInModel

    /// `manager`: whose copy the page is (nil: the default manager's).
    init(connector: HubConnector, manager: String? = nil, reload: @escaping () async -> Void, deps: ConnectorDeps = .live) {
        self.connector = connector
        self.reload = reload
        _model = State(initialValue: ConnectorSignInModel(id: connector.id, manager: manager, deps: deps))
    }

    var body: some View {
        ConnectorSwitchSection(connector: connector, model: model, reload: reload)
        if connector.isEnabled && !connector.isSignedIn {
            if connector.signsInHere {
                ConnectorSignInSteps(name: connector.name, model: model, reload: reload,
                                     footer: ConnectorText.signInFooter(connector))
            } else {
                // Just switched on: its program has not started yet (the page asks again every second).
                Section {
                    Text("You can sign in once \(connector.name) is on.").foregroundStyle(.secondary)
                        .accessibilityIdentifier("connector.starting")
                }
            }
        }
        ConnectorFeedsSection(connector: connector, model: model, reload: reload)
        ConnectorDataSection(connector: connector, model: model, reload: reload, showsLogout: true)
    }
}

extension HubConnector {
    var isEnabled: Bool { enabled ?? true }
    var isSignedIn: Bool { status == "ok" || status == "rate_limited" }
}

/// Sync <name>: a connector's switch.
struct ConnectorSwitchSection: View {
    var connector: HubConnector
    var model: ConnectorSignInModel
    var reload: () async -> Void

    var body: some View {
        Section {
            Toggle(isOn: Binding(get: { connector.isEnabled }, set: { on in
                Task {
                    _ = await model.setEnabled(on)
                    await reload()
                }
            })) {
                Text(connector.storesData ? String(localized: "Sync \(connector.name)") : String(localized: "Enable \(connector.name)")).lineLimit(1)
            }
            .accessibilityIdentifier("connector.enabled")
        } footer: {
            Text(footer)
        }
    }

    private var footer: String {
        let reads = ConnectorText.profile(connector.id).reads ?? String(localized: "its content")
        return switch (connector.isEnabled, connector.storesData) {
        case (true, true): String(localized: "When it's off, \(connector.name) is no longer read. Your sign-in and the data already saved are kept.")
        case (true, false): String(localized: "When it's off, \(connector.name) stops. Your sign-in is kept.")
        case (false, false): String(localized: "Turn it on to sign in. [Connector Sign In Views 823277]")
        case (false, true): String(localized: "Turn it on to sign in. Once you're signed in, \(reads) will be added to Knowledge and the Inbox, and the Assistant can search them too.")
        }
    }
}

/// Save to Knowledge and Show in Inbox, for a connector that has them (the hub lists `archive`, `inbox`), and Delete Saved Knowledge.
struct ConnectorFeedsSection: View {
    var connector: HubConnector
    var model: ConnectorSignInModel
    var reload: () async -> Void
    @State private var confirmDelete = false

    var body: some View {
        if connector.archive != nil || connector.inbox != nil {
            Section {
                if let on = connector.archive {
                    toggle(String(localized: "Save to Knowledge"), on: on, feed: .archive).accessibilityIdentifier("connector.archive")
                }
                if let on = connector.inbox {
                    toggle(String(localized: "Show in Inbox"), on: on, feed: .inbox).accessibilityIdentifier("connector.inbox")
                }
                if connector.archive != nil {
                    Button(role: .destructive) { confirmDelete = true } label: { Text("Delete Saved Knowledge").lineLimit(1) }
                        .disabled(model.busy)
                        .accessibilityIdentifier("connector.deleteArchive")
                }
            } footer: {
                Text(model.archiveDeleted ?? footer)
            }
            .confirmationDialog("Delete what \(connector.name) saved to Knowledge?", isPresented: $confirmDelete, titleVisibility: .visible) {
                Button("Delete", role: .destructive) {
                    Task {
                        await model.deleteArchive()
                        await reload()
                    }
                }
            } message: {
                Text("Chats and people from \(connector.name) are deleted from Knowledge, and “Save to Knowledge” is turned off. The Inbox and the assistant's searches of \(connector.name) aren't affected.")
            }
        }
    }

    private var footer: String {
        var lines: [String] = []
        if connector.archive != nil {
            lines.append(connector.archive == true ? String(localized: "Save to Knowledge: content from \(connector.name) is saved to Knowledge, where the assistant and AI twin can search it. When off, nothing new is saved; what's saved stays.")
                         : String(localized: "Not saving to Knowledge: nothing new is saved. The Inbox and the assistant's searches of \(connector.name) work as usual."))
        }
        if connector.inbox == false {
            lines.append(String(localized: "Not showing in Inbox: nothing new arrives; existing items stay."))
        }
        return lines.joined(separator: "\n")
    }

    private func toggle(_ title: String, on: Bool, feed: ConnectorFeed) -> some View {
        Toggle(isOn: Binding(get: { on }, set: { value in
            Task {
                _ = await model.setFeed(feed, on: value)
                await reload()
            }
        })) {
            Text(title).lineLimit(1)
        }
    }
}

/// Sign Out (when it shows) and Delete Saved Data.
struct ConnectorDataSection: View {
    var connector: HubConnector
    var model: ConnectorSignInModel
    var reload: () async -> Void
    var showsLogout: Bool
    @State private var confirmLogout = false
    @State private var confirmDelete = false

    private var logout: Bool { showsLogout && (connector.isSignedIn || connector.via != nil) }

    var body: some View {
        if logout || connector.storesData {
            sections
        }
    }

    private var sections: some View {
        Section {
            if logout {
                Button(role: .destructive) { confirmLogout = true } label: { Text("Sign Out [Connector Sign In Views 3ab8cc]").lineLimit(1) }
                    .disabled(model.busy)
                    .accessibilityIdentifier("connector.logout")
            }
            if connector.storesData {
                Button(role: .destructive) { confirmDelete = true } label: { Text("Delete Saved Data").lineLimit(1) }
                    .disabled(model.busy)
                    .accessibilityIdentifier("connector.deleteData")
            }
        } footer: {
            if let deleted = model.deleted {
                Text(deleted)
            } else if connector.storesData {
                Text("Deletes its chats and people in Knowledge and its Inbox items, and turns off syncing. The sign-in isn't affected.")
            }
        }
        .confirmationDialog("Sign out of \(connector.name)? [Connector Sign In Views b5b019]", isPresented: $confirmLogout, titleVisibility: .visible) {
            Button("Sign Out [Connector Sign In Views 3ab8cc]", role: .destructive) {
                Task {
                    await model.logout()
                    await reload()
                }
            }
        } message: {
            Text(ConnectorText.logoutMessage(connector))
        }
        .confirmationDialog("Delete data saved from \(connector.name)?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                Task {
                    await model.deleteData()
                    await reload()
                }
            }
        } message: {
            Text("Chats and people from \(connector.name) in Knowledge and its Inbox items are deleted, and syncing is turned off. Project summaries leave them out the next time they update.")
        }
    }
}

/// Signing in a step at a time (the hub says what each step asks for): the step's card, then its button on its own
/// under the card (Next, or the page to enter a code on), so the card keeps its rounded corners.
struct ConnectorSignInSteps: View {
    /// The connector's (or channel's) name, in the buttons and waiting lines.
    var name: String
    var model: ConnectorSignInModel
    var reload: () async -> Void
    var footer: String
    /// The section's heading and the first button: Sign In for a connector, Bind / Bind with QR Code for a channel.
    var heading = String(localized: "Sign In")
    var start: String?

    var body: some View {
        if let p = model.prompt {
            Section {
                if let hint = p.hint, !hint.isEmpty {
                    Text(hint).font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                // Above the fields: below them the keyboard covers it (the field keeps the focus).
                if let e = p.error, !e.isEmpty {
                    Text(e).font(Theme.Font.label).foregroundStyle(Theme.danger)
                        .accessibilityIdentifier("connector.signInError")
                }
                if p.isDeviceCode || p.isPairCode {
                    deviceCode(p)
                } else if p.isQRCode {
                    qrCode(p)
                } else if p.cannotDraw {
                    Text(p.cannotDrawNote(name: name))
                        .font(Theme.Font.secondary).foregroundStyle(Theme.cautionText)
                        .accessibilityIdentifier("connector.cannotDraw")
                } else {
                    ForEach(p.fields ?? []) { f in
                        field(f)
                    }
                }
            } header: {
                SettingsHeading(heading)
            }
            .task(id: p.pollKey) {
                if await model.waitForAuthorization() { await reload() }
            }
            Section {
                if p.isDeviceCode {
                    if let s = p.url, let url = URL(string: s) {
                        #if os(macOS)
                        // Standard buttons at the trailing end on the Mac, not bars the width of the form (#1221).
                        HStack {
                            Spacer(minLength: 0)
                            Link(destination: url) {
                                Label("Open \(name) to Enter Code", systemImage: Theme.Icon.openExternal).lineLimit(1)
                            }
                            .formButton(prominent: true)
                            .accessibilityIdentifier("connector.openVerification")
                        }
                        .listRowBackground(Color.clear)
                        #else
                        Link(destination: url) {
                            Label("Open \(name) to Enter Code", systemImage: Theme.Icon.openExternal).lineLimit(1).frame(maxWidth: .infinity)
                        }
                        .themedButton(prominent: true)
                        .listRowBackground(Color.clear)
                        .accessibilityIdentifier("connector.openVerification")
                        #endif
                    }
                } else if p.isPairCode {
                    EmptyView() // nothing to press: the code goes to the bot from the chat app
                } else if p.cannotDraw {
                    FormAction {
                        Button {
                            Task { await model.restart() }
                        } label: {
                            Text("Start Over").lineLimit(1).formActionWidth()
                        }
                        .formButton(prominent: true)
                        .disabled(model.busy)
                        .accessibilityIdentifier("connector.restart")
                    }
                } else if p.isQRCode {
                    if let image = model.qrImage {
                        FormAction {
                            ShareLink(item: Image(uiImage: image), preview: SharePreview(String(localized: "QR Code"), image: Image(uiImage: image))) {
                                Label("Save QR Code", systemImage: Theme.Icon.share).lineLimit(1).formActionWidth()
                            }
                            .formButton(prominent: false)
                            .accessibilityIdentifier("connector.shareQR")
                        }
                    }
                } else {
                    FormAction {
                        Button {
                            Task {
                                await model.next()
                                await reload() // the status above follows (credentials saved: api_id shown; signed in)
                            }
                        } label: {
                            Text(model.busy ? String(localized: "Verifying…") : String(localized: "Next [Inbox Line acfc4e]")).lineLimit(1).formActionWidth()
                        }
                        .formButton(prominent: true)
                        .disabled(!model.canSend)
                        .accessibilityIdentifier("connector.next")
                    }
                }
            } footer: {
                Text(footer)
            }
        } else {
            Section {
                FormAction {
                    Button {
                        Task { await model.start() }
                    } label: {
                        Text(start ?? String(localized: "Sign In to \(name)")).lineLimit(1).formActionWidth()
                    }
                    .formButton(prominent: true)
                    .disabled(model.busy)
                    .accessibilityIdentifier("connector.signIn")
                }
            } header: {
                SettingsHeading(heading)
            } footer: {
                Text(footer)
            }
        }
    }

    /// The code to enter on the sign-in page (GitHub's device flow): large, copyable; the page asks the hub again on
    /// its own until the user has entered it.
    @ViewBuilder private func deviceCode(_ p: ConnectorPrompt) -> some View {
        HStack {
            Text(p.userCode ?? "").font(Theme.Font.display).monospaced().textSelection(.enabled).lineLimit(1)
                .minimumScaleFactor(0.6)
                .accessibilityIdentifier(p.isPairCode ? "connector.pairCode" : "connector.userCode")
            Spacer(minLength: Theme.Space.s)
            Button {
                UIPasteboard.general.string = p.userCode
                Haptics.success()
            } label: {
                Label("Copy [Message Menu 63d90d]", systemImage: Theme.Icon.copy).labelStyle(.iconOnly)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(p.isPairCode ? String(localized: "Copy Pairing Code [Connector Sign In Views 43e714]") : String(localized: "Copy Code [Connector Sign In Views 85a0b9]"))
        }
        HStack(spacing: Theme.Space.s) {
            ProgressView()
            Text(p.isPairCode ? String(localized: "Waiting for you to send it to the bot…") : String(localized: "Waiting for you to authorize on \(name)…")).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    /// The QR code to scan with the other app: the page asks the hub again on its own until it is scanned and
    /// confirmed there. On the phone that runs the other app, Save QR Code below puts it in the photo library to scan from.
    @ViewBuilder private func qrCode(_ p: ConnectorPrompt) -> some View {
        QRCodeView(rows: p.qr?.rows ?? [])
            .scaledFrame(width: Theme.Size.box200, height: Theme.Size.box200)
            .frame(maxWidth: .infinity)
            .accessibilityIdentifier("connector.qrCode")
        HStack(spacing: Theme.Space.s) {
            ProgressView()
            Text("Waiting for you to scan and confirm…").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
        }
    }

    @ViewBuilder private func field(_ f: ConnectorPrompt.Field) -> some View {
        #if os(iOS) || os(visionOS)
        if Self.stacked(f) {
            // A label and a placeholder too long for one line: the title above, the field the row's width below. Left to
            // LabeledContent, the row wrapped on its own and the field in it never took the focus (a long token hint).
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(f.label).lineLimit(1)
                input(f).multilineTextAlignment(.leading)
            }
        } else {
            LabeledContent(f.label) { input(f).multilineTextAlignment(.trailing) }
        }
        #else
        LabeledContent(f.label) { input(f).multilineTextAlignment(.trailing) }
        #endif
    }

    @ViewBuilder private func input(_ f: ConnectorPrompt.Field) -> some View {
        let text = Binding(get: { model.values[f.name] ?? "" }, set: { model.values[f.name] = $0 })
        let prompt = Text(f.placeholder ?? f.label)
        #if os(iOS) || os(visionOS)
        if f.secret == true {
            // A token, an API hash, another service's password: never offered to the system's passwords (#1598).
            SecretField(f.placeholder ?? f.label, filled: String(localized: "Filled in"), text: text, identifier: "connector.field." + f.name)
        } else {
            TextField(f.label, text: text, prompt: prompt)
                .keyboardType(Self.keyboard(f.name))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier("connector.field." + f.name)
        }
        #else
        Group {
            if f.secret == true {
                SecureField(f.label, text: text, prompt: prompt)
            } else {
                TextField(f.label, text: text, prompt: prompt)
                    .keyboardType(Self.keyboard(f.name))
            }
        }
        .textInputAutocapitalization(.never)
        .autocorrectionDisabled()
        .accessibilityIdentifier("connector.field." + f.name)
        #endif
    }

    /// Whether a field's label and placeholder together are wider than a phone's row holds on one line, in widths of a
    /// CJK character (a Latin one about half).
    static func stacked(_ f: ConnectorPrompt.Field) -> Bool {
        let width = (f.label + (f.placeholder ?? f.label)).reduce(0.0) { $0 + ($1.isASCII ? 0.55 : 1) }
        return width > 16
    }

    private static func keyboard(_ name: String) -> UIKeyboardType {
        switch name {
        case "phone": .phonePad
        case "code", "api_id": .numberPad
        case "base_url": .URL
        default: .asciiCapable
        }
    }
}

