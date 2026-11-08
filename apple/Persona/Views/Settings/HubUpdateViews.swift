import BeautifulUI
import SwiftUI

/// What the hub page needs from the app: the hub, error reporting, and where the status lives for the rest of the app
/// (AppModel.hubUpdate: the line over the Agents list, the connection title). Tests pass a FakeHub's client.
struct HubUpdateDeps {
    var api: () -> APIClient?
    var report: (Error) -> Void
    var publish: (HubUpdateStatus) -> Void

    static var live: HubUpdateDeps {
        let model = AppModel.shared
        return HubUpdateDeps(api: { model.api }, report: { model.show($0) }, publish: { s in if model.hubUpdate != s { model.hubUpdate = s } })
    }
}

/// Settings › hub (docs/operations/hub-update.md「App 里」): the hub's version and channel, checking for and installing a release,
/// what an update waits for, turning auto-update on and off, and the history.
@Observable final class HubUpdateModel {
    private(set) var status: HubUpdateStatus?
    private(set) var history: [HubUpdateRecord] = []
    private(set) var loaded = false
    /// The hub predates updates in the app (no /api/v1/hub/update).
    private(set) var unsupported = false
    private(set) var checking = false
    private(set) var error: String?
    @ObservationIgnored private let deps: HubUpdateDeps
    /// Monotonic tag for history writes: a slower, earlier GET must not overwrite a newer reload's snapshot.
    @ObservationIgnored private var historyGeneration = 0

    init(deps: HubUpdateDeps = .live) { self.deps = deps }

    /// Said when the version could not be read for a timeout or no network: `shown` is nil for those, and the page
    /// had nothing else to show in their place, spinning for good (#3484).
    static var unreachable: String { String(localized: "Can't reach the hub to read its version") }
    static var noHub: String { String(localized: "Not connected to a hub yet") }

    func load() async {
        guard let api = deps.api() else {
            if status == nil { error = Self.noHub }
            loaded = true
            return
        }
        // Claim this load's write generation before the requests fly; a finished status below makes reloadHistory
        // claim a newer one, so a delayed inline snapshot is dropped instead of covering the fresher reload.
        let gen = historyGeneration + 1
        historyGeneration = gen
        // Status and history are independent GETs: issue both at once instead of making history wait for status.
        async let respStatus = api.hubUpdate()
        // try? here (not on respStatus): only the status request's 404 may mark the hub unsupported, never history's.
        async let respHistory = (try? api.hubUpdateHistory()) ?? []
        do {
            // apply before assigning history, as before: it may spawn reloadHistory, which claims the last word.
            apply(try await respStatus)
            let h = await respHistory
            if gen == historyGeneration { history = h }
            error = nil
        } catch let e as APIError where e.status == 404 {
            unsupported = true
        } catch {
            if !error.isCancellation {
                self.error = error.shown ?? (status == nil ? Self.unreachable : self.error)
            }
        }
        loaded = true
    }

    /// A newer status (from a request here, or the `hub_update` event).
    func apply(_ s: HubUpdateStatus) {
        let finished = status?.state != s.state && ["updated", "rolled_back", "failed"].contains(s.state)
        status = s
        deps.publish(s)
        if finished { Task { await reloadHistory() } }
    }

    private func reloadHistory() async {
        guard let api = deps.api() else { return }
        let gen = historyGeneration + 1
        historyGeneration = gen
        if let h = try? await api.hubUpdateHistory(), gen == historyGeneration { history = h }
    }

    func check() async {
        guard let api = deps.api() else { return }
        checking = true
        defer { checking = false }
        do { apply(try await api.checkHubUpdate()) } catch { deps.report(error) }
    }

    /// Starts the update; `now` is the second confirmation's Update Now (it does not wait for idle).
    func update(now: Bool) async {
        guard let api = deps.api() else { return }
        do { apply(try await api.startHubUpdate(now: now)) } catch { deps.report(error) }
    }

    func setAuto(_ on: Bool) async {
        guard let api = deps.api() else { return }
        do { apply(try await api.setHubUpdate(auto: on)) } catch { deps.report(error) }
    }

    /// 「自动更新已开启」 seen: not shown again.
    func noticeSeen() async {
        guard let api = deps.api() else { return }
        do { apply(try await api.setHubUpdate(noticeSeen: true)) } catch { deps.report(error) }
    }
}

/// The page.
struct HubSettingsPage: View {
    @Environment(AppModel.self) private var app
    @State private var model: HubUpdateModel
    @State private var confirmNow = false

    init(model: HubUpdateModel = HubUpdateModel()) { _model = State(initialValue: model) }

    var body: some View {
        SettingsForm {
            if model.unsupported {
                Section {
                    Text("This hub is too old to check or update its version from the app. Update it once with install.sh on the hub machine and it will work.")
                        .foregroundStyle(.secondary)
                }
            } else if let s = model.status {
                versionSection(s)
                updateSection(s)
                if s.canUpdate { autoSection(s) }
                nodesSection
                if !model.history.isEmpty { historySection }
            } else if let e = model.error {
                Section { Text(e).foregroundStyle(.secondary) }
            } else {
                Section { ProgressView().frame(maxWidth: .infinity) }
            }
        }
        .themedPage()
        .detailTitle("hub")
        .task { await model.load() }
        // Before the version was read, pulling down reads it again (a check needs a hub that answered).
        .refreshable { if model.status == nil { await model.load() } else { await model.check() } }
        .onChange(of: app.hubUpdate) { _, s in
            if let s, s != model.status { model.apply(s) }
        }
        .confirmationDialog("Update Now?", isPresented: $confirmNow, titleVisibility: .visible) {
            Button("Update Now", role: .destructive) { Task { await model.update(now: true) } }
        } message: {
            Text("This won't wait for them to finish: anything waiting on the hub is interrupted (the assistant's current turn, uploads in progress, terminal connections). Agents in herdr aren't affected and reconnect once the hub is back.")
        }
    }

    // MARK: Sections

    private func versionSection(_ s: HubUpdateStatus) -> some View {
        Section {
            Group {
                info(String(localized: "Version"), s.version, code: true)
                info(String(localized: "Channels"), s.channelTitle)
                if let at = s.lastCheck { info(String(localized: "Last Checked"), Fmt.relative(at)) }
                Button {
                    Task { await model.check() }
                } label: {
                    HStack {
                        Text(model.checking ? String(localized: "Checking…") : String(localized: "Check for Updates")).lineLimit(1)
                        if model.checking { Spacer(); ProgressView().controlSize(.small) }
                    }
                }
                .disabled(model.checking || s.working)
                .accessibilityIdentifier("hub.check")
            }
            .bareListRow()
        }
    }

    @ViewBuilder private func updateSection(_ s: HubUpdateStatus) -> some View {
        Section {
            if s.channel == "docker" {
                dockerRows(s)
            } else if !s.canUpdate {
                Text(s.whyNot.map { String(localized: "This hub doesn't update itself: \($0).") } ?? String(localized: "This hub doesn't update itself.")).foregroundStyle(.secondary)
            } else if let l = s.latest, l.newer {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text("New Version \(l.version) [Hub Update Views cf4c45]").font(Theme.Font.bodyStrong).lineLimit(1)
                    if let notes = l.notes, !notes.isEmpty { Text(notes).font(Theme.Font.secondary).foregroundStyle(.secondary) }
                    if l.bad {
                        Label("This version failed to start last time and was rolled back; auto-update won't install it again.", systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondary).foregroundStyle(Theme.caution)
                    } else if let why = l.held, s.auto {
                        Label("Auto-update won't install this version: \(why). If you're sure it's fine, update manually.", systemImage: Theme.Icon.error)
                            .font(Theme.Font.secondary).foregroundStyle(Theme.caution)
                            .accessibilityIdentifier("hub.held")
                    }
                }
                if s.working {
                    progressRows(s)
                } else {
                    #if os(macOS)
                    // A standard button at the group's trailing end, not a bar the width of the form (#1221).
                    HStack {
                        Spacer(minLength: 0)
                        Button { Task { await model.update(now: false) } } label: { Text("Update Hub").lineLimit(1) }
                            .formButton(prominent: true)
                            .accessibilityIdentifier("hub.update")
                    }
                    #else
                    Button {
                        Task { await model.update(now: false) }
                    } label: {
                        Text("Update Hub").lineLimit(1).frame(maxWidth: .infinity) // the version is right above
                    }
                    .themedButton(prominent: true)
                    .listRowBackground(Color.clear)
                    .accessibilityIdentifier("hub.update")
                    #endif
                }
            } else if s.working {
                progressRows(s)
            } else {
                Label("Up to Date", systemImage: Theme.Icon.successFilled).foregroundStyle(Theme.positive)
            }
            if let e = s.error, !e.isEmpty, !s.working {
                Text(e).font(Theme.Font.secondary).foregroundStyle(s.state == "rolled_back" ? Theme.caution : Theme.danger)
            }
        } header: {
            SettingsHeading(String(localized: "Update [Hub Update Views 3055a0]"))
        } footer: {
            if s.canUpdate {
                Text("Updates wait until the assistant and all agents are idle before restarting the hub; if the new version doesn't start, it rolls back automatically. Connections drop briefly during the restart.")
            }
        }
    }

    @ViewBuilder private func progressRows(_ s: HubUpdateStatus) -> some View {
        HStack(spacing: Theme.Space.s) {
            ProgressView().controlSize(.small)
            Text(s.stateTitle).lineLimit(1)
            if let p = s.progress, s.state == "downloading" { Text(p).font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1) }
        }
        .accessibilityIdentifier("hub.progress")
        if s.state == "waiting_idle", !s.busy.isEmpty {
            ForEach(s.busy) { b in
                Label {
                    Text(b.title).lineLimit(1)
                    Text(b.machine).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                } icon: {
                    ThemedIcon(Theme.Icon.inProgress).foregroundStyle(Theme.busy)
                }
            }
            Button(role: .destructive) { confirmNow = true } label: { Text("Update Now…").lineLimit(1) }
                .accessibilityIdentifier("hub.updateNow")
        }
    }

    @ViewBuilder private func dockerRows(_ s: HubUpdateStatus) -> some View {
        if let l = s.latest, l.newer {
            Text("New Image \(l.version)").font(Theme.Font.bodyStrong).lineLimit(1)
            if let im = s.image, im.pinned {
                // `pull` alone keeps the version the tag names: the tag changes first.
                Text("On the hub machine (NAS), change PERSONA_IMAGE in the .env next to the compose file to:").foregroundStyle(.secondary)
                copyRow(im.name + ":" + l.version)
                Text("Then run this in that directory:").foregroundStyle(.secondary)
            } else {
                Text("On the hub machine (NAS), run this in the compose file's directory:").foregroundStyle(.secondary)
            }
        } else {
            Text("A Docker hub updates with a new image: when one is out, run this in the compose file's directory").foregroundStyle(.secondary)
        }
        copyRow(Self.dockerCommand)
    }

    private func copyRow(_ text: String) -> some View {
        HStack {
            Text(text).font(Theme.Font.code).lineLimit(1).minimumScaleFactor(0.7).textSelection(.enabled)
            Spacer(minLength: Theme.Space.s)
            Button { UIPasteboard.general.string = text; Haptics.success() } label: {
                ThemedIcon(Theme.Icon.copy)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel("Copy [Message Menu 63d90d]")
        }
    }

    static let dockerCommand = "docker compose pull && docker compose up -d"

    private func autoSection(_ s: HubUpdateStatus) -> some View {
        Section {
            Toggle(isOn: Binding(get: { s.auto }, set: { on in Task { await model.setAuto(on) } })) {
                Text("Update Hub Automatically").lineLimit(1)
            }
            .accessibilityIdentifier("hub.auto")
        } footer: {
            Text("When on, new versions install themselves once the assistant and all agents are idle, and you get a notification afterward.")
        }
    }

    private var nodesSection: some View {
        Section {
            ForEach(app.machines) { m in
                NavigationLink(value: MachineDetailRoute(id: m.id)) {
                    HStack(spacing: Theme.Space.s) {
                        Text(m.name).lineLimit(1)
                        Spacer(minLength: Theme.Space.s)
                        if m.nodeMismatch == true { Chip(text: String(localized: "Mismatch"), look: .tone(StatusPalette.working)) }
                        Text(m.nodeVersion ?? (m.online ? String(localized: "Unknown [Session Modes 4d8c1c]") : String(localized: "Offline [Machine Entity be1b4f]")))
                            .font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                    }
                }
            }
        } header: {
            SettingsHeading(String(localized: "personad on Your Computers"))
        } footer: {
            Text("After the hub updates, it moves personad on your computers to the same version. For any that didn't update, tap it and choose Redeploy.")
        }
    }

    private var historySection: some View {
        Section {
            ForEach(model.history.prefix(10)) { r in
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    HStack(spacing: Theme.Space.s) {
                        Text(r.resultTitle).font(Theme.Font.bodyStrong)
                            .foregroundStyle(r.result == "updated" ? Theme.positive : ["rolled_back", "held"].contains(r.result) ? Theme.caution : Theme.danger)
                            .lineLimit(1)
                        if r.auto { Chip(text: String(localized: "Auto"), look: .outline) }
                        Spacer(minLength: Theme.Space.s)
                        if let at = r.ended ?? r.started { Text(Fmt.relative(at)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1) }
                    }
                    Text("\(r.fromVersion) → \(r.toVersion)").font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                    if let why = r.reason, !why.isEmpty { Text(why).font(Theme.Font.caption).foregroundStyle(.secondary) }
                }
            }
        } header: {
            SettingsHeading(String(localized: "Update History"))
        }
    }

    @ViewBuilder private func info(_ title: String, _ value: String, code: Bool = false) -> some View {
        if AppStyle.current.usesBeautifulUI {
            BUIListRow(title, value: value)
        } else {
            LabeledContent(title) {
                Text(value).font(code ? Theme.Font.code : Theme.Font.body).lineLimit(1).truncationMode(.middle)
            }
        }
    }
}

/// The one quiet line about the hub over the Agents list, like 「Mac 上的 Persona 已登录」 (docs/operations/hub-update.md「App 里」):
/// Auto-update is on (once), a newer version when auto-update is off, an update under way, one that went back, or that the
/// hub or the app is too old. No dialog; a tap opens the hub page.
struct HubNoticeRow: View {
    @Environment(AppModel.self) private var model
    @State private var page = false

    var body: some View {
        if let notice = model.hubNotice {
            HStack(spacing: Theme.Space.s) {
                Group {
                    if case .updating = notice { ProgressView().controlSize(.mini) } else { ThemedIcon(symbol(notice)) }
                }
                .foregroundStyle(tint(notice))
                .scaledFrame(width: Theme.Size.iconS)
                Text(text(notice)).foregroundStyle(.secondary).lineLimit(2)
                Spacer(minLength: Theme.Space.s)
                actions(notice).buttonStyle(.borderless) // in a list row: each button takes only its own taps
            }
            .font(Theme.Font.secondary)
            .contentShape(.rect)
            .onTapGesture { page = true }
            .accessibilityElement(children: .contain) // or the row's id hides its buttons' (docs/lessons/ios.md)
            .accessibilityIdentifier("agents.hubNotice")
            .sheet(isPresented: $page) { NavigationStack { HubSettingsPage().pageToolbar { closeButton } } }
        }
    }

    private var closeButton: some ToolbarContent {
        ToolbarItem(placement: .cancellationAction) {
            Button { page = false } label: { ThemedIcon(Theme.Icon.close) }.accessibilityLabel("Close")
        }
    }

    private func text(_ n: HubNotice) -> String {
        switch n {
        case .appTooOld: String(localized: "This app is too old for this hub. Update it in TestFlight")
        case .hubTooOld: String(localized: "The hub is out of date; some features won't work")
        case .updating(let what): String(localized: "Hub updating: \(what)")
        case .rolledBack(let why, _): why
        case .autoOn: String(localized: "The hub updates itself when idle")
        case .available(let v, _): String(localized: "Hub update available: \(v)")
        case .newImage(let v): String(localized: "New hub image: \(v)")
        }
    }

    private func symbol(_ n: HubNotice) -> String {
        switch n {
        case .appTooOld, .hubTooOld, .rolledBack: Theme.Icon.error
        default: Theme.Icon.hubUpdate
        }
    }

    private func tint(_ n: HubNotice) -> Color {
        switch n {
        case .appTooOld, .hubTooOld, .rolledBack: Theme.caution
        default: Theme.accent
        }
    }

    @ViewBuilder private func actions(_ n: HubNotice) -> some View {
        switch n {
        case .autoOn:
            Button(String(localized: "action.turnOff", defaultValue: "Turn Off")) { Task { await HubUpdateModel().setAuto(false) } }.lineLimit(1).accessibilityIdentifier("hubNotice.autoOff")
            Button("OK [Recordings Section de32e2]") { Task { await HubUpdateModel().noticeSeen() } }.lineLimit(1).accessibilityIdentifier("hubNotice.seen")
        case .available:
            Button("Update [Hub Update Views 3055a0]") { Task { await HubUpdateModel().update(now: false) } }.lineLimit(1).accessibilityIdentifier("hubNotice.update")
            dismiss(n)
        case .rolledBack, .hubTooOld, .newImage: // a Docker hub's new image: the page has the command to run
            dismiss(n)
        case .appTooOld, .updating:
            EmptyView()
        }
    }

    @ViewBuilder private func dismiss(_ n: HubNotice) -> some View {
        if let key = n.dismissKey {
            Button { model.dismissHubNotice(key) } label: { ThemedIcon(Theme.Icon.close) }
                .accessibilityLabel("Don't Show Again")
                .accessibilityIdentifier("hubNotice.dismiss")
        }
    }
}

/// Settings › Assistant › Skills: the assistant's skills on its machine (GET /api/v1/skills), read only for now.
@Observable final class SkillsModel {
    private(set) var skills: [AgentSkill] = []
    private(set) var machine: String?
    private(set) var loaded = false
    private(set) var error: String?
    @ObservationIgnored private let api: () -> APIClient?

    init(api: @escaping () -> APIClient? = { AppModel.shared.api }) { self.api = api }

    static var noHub: String { String(localized: "Not connected to the hub yet") }
    static var unreachable: String { String(localized: "Can't reach the hub") }
    static var machineSlow: String { String(localized: "The computer that runs the assistant didn't respond in time") }

    /// What the page says for a failed load: the hub's own reason when it gave one (409: no machine set for the
    /// assistant, 502: that machine offline), else why nothing came back. A bare 「读不到」 said none of it (#4274).
    /// Nil for a cancelled load (the page went away).
    static func failure(_ error: Error) -> String? {
        if error.isCancellation { return nil }
        // The hub answered, but the assistant's machine did not in time (its skills.list ran past the deadline).
        if let e = error as? APIError, e.status == 502, APIError.isTimeoutText(e.message) { return machineSlow }
        return error.shown ?? unreachable
    }

    func load() async {
        guard let api = api() else {
            error = Self.noHub
            loaded = true
            return
        }
        do {
            let r = try await api.skills()
            (skills, machine, error) = (r.skills, r.machine, nil)
        } catch {
            guard let shown = Self.failure(error) else { return }
            self.error = shown
        }
        loaded = true
    }
}

struct SkillsPage: View {
    @State private var model: SkillsModel

    init(model: SkillsModel = SkillsModel()) { _model = State(initialValue: model) }

    var body: some View {
        SettingsForm {
            if model.loaded && model.skills.isEmpty {
                Section {
                    EmptyStateView(symbol: Theme.Icon.skills, title: model.error == nil ? String(localized: "No Skills Yet") : String(localized: "Couldn't Load Skills"),
                                   message: model.error ?? String(localized: "Put skills in ~/.persona/skills/ on the computer that runs the assistant, one directory each (with a SKILL.md inside)."))
                }
            } else {
                Section {
                    ForEach(model.skills) { s in
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            HStack(spacing: Theme.Space.s) {
                                Text(s.name).font(Theme.Font.bodyStrong).lineLimit(1)
                                Spacer(minLength: Theme.Space.s)
                                Chip(text: s.source == "builtin" ? String(localized: "Built-in") : String(localized: "Mine"), look: s.source == "builtin" ? .outline : .neutral)
                            }
                            if let d = s.description, !d.isEmpty {
                                Text(d).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(3)
                            }
                        }
                    }
                } footer: {
                    Text(model.machine.map { String(localized: "Stored in ~/.persona/skills/ on \($0), one directory each (with a SKILL.md inside). Updating personad leaves them alone; changes take effect from the next new chat.") }
                         ?? String(localized: "Stored in ~/.persona/skills/ on the computer that runs the assistant, one directory each (with a SKILL.md inside). Updating personad leaves them alone; changes take effect from the next new chat."))
                }
            }
        }
        .themedPage()
        .detailTitle("Skills")
        .task { await model.load() }
        .refreshable { await model.load() }
    }
}
