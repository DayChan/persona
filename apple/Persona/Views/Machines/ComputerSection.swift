import SwiftUI

/// Control Computer on a Mac's page while the hub's plugin cua is on (docs/extensions/computer-use.md): the switch that lets the assistant
/// operate it through Cua Driver, and whether it can now. What is missing comes with what to do about it: Install Cua Driver
/// when it is not there, Grant Access when a permission is missing (macOS asks on the Mac's own screen), with the node's
/// progress while it works. The node is asked again when the page opens, and every few seconds while a job runs.
struct ComputerSection: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    let ref: MachineRef
    var computer: MachineComputer
    var online: Bool
    /// What the hub said since the page opened (newer than the machine list's).
    @State private var fresh: MachineComputer?
    @State private var saving = false
    @State private var asking = false
    @State private var checking = false
    /// This device remembers the Mac's Screen Sharing sign-in (#1322), and unlocks it with one tap (#1507 a-2).
    @State private var screenKept = false
    @State private var oneTap = false
    @State private var askOneTap = false
    /// 「扩展开着时不自动锁屏」 asked about before it is turned on (#1507 b2).
    @State private var askNoLock = false
    /// The apps always allowed for the assistant here (#1507).
    @State private var apps: [ComputerApp] = []

    private var shown: MachineComputer { fresh ?? computer }

    /// Asked again every few seconds: a job runs, or macOS's dialogs wait on the Mac after a grant.
    private var watching: Bool {
        guard online, shown.enabled, let job = shown.job else { return false }
        return job.running || (job.kind == "grant" && job.state == "done" && !shown.missingPermissions.isEmpty)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("Control Computer").font(Theme.Font.heading).padding(.horizontal, Theme.Space.xs)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Toggle(isOn: Binding(get: { shown.enabled }, set: { on in Task { await set(on) } })) {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("Let the Assistant Control This Computer").font(Theme.Font.body).lineLimit(1).minimumScaleFactor(0.8)
                        Text(shown.backend == "lcu" ? String(localized: "Sees the screen, clicks and types through LCU. Confirmations happen on your phone and the web") : String(localized: "Sees the screen, taps, types and takes screenshots through Cua Driver"))
                            .font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                }
                .disabled(saving || !online)
                .accessibilityIdentifier("computer-switch")
                if shown.enabled {
                    Divider()
                    state
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .card()
        }
        .task(id: online && shown.enabled) {
            if online && shown.enabled { await refresh() }
        }
        // Only while the page is in front: not behind another tab or with the app in the background.
        .tabActiveTask(id: watching && phase == .active) {
            guard phase == .active else { return }
            while watching, !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                await refresh()
            }
        }
        .task(id: model.screenOpen == nil) { // the drawer may have saved the sign-in, or unlocked the Mac
            screenKept = ScreenCredentials.load(ref) != nil
            oneTap = ScreenUnlock.isOn(ref)
            if model.screenOpen == nil, online, shown.enabled { await refresh() }
        }
        .confirmationDialog("Turn On \(Self.oneTapTitle)?", isPresented: $askOneTap, titleVisibility: .visible) {
            Button(String(localized: "action.turnOn", defaultValue: "Turn On")) {
                ScreenUnlock.set(true, for: ref)
                oneTap = true
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("When this Mac is locked, tap One-Tap Unlock and verify with \(Self.oneTapAuthName) (or your passcode) to type the saved login password on its lock screen. Once unlocked it won't lock again by itself; tap Lock when you're done.")
        }
    }

    // MARK: State

    @ViewBuilder private var state: some View {
        let c = shown
        if !online {
            line(String(localized: "Machine is offline. You can control it once it's connected."), ok: false)
        } else if let job = c.job, job.running, job.kind == "grant" {
            grantGuide(c, asking: true)
        } else if let job = c.job, job.running {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                HStack(spacing: Theme.Space.s) {
                    ProgressView().controlSize(.small)
                    Text(job.step ?? String(localized: "Installing…"))
                        .font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("computer-status")
                if job.kind == "install" { action(String(localized: "Cancel"), "cancel", identifier: "computer-cancel") }
            }
        } else if !c.installed {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if let job = c.job, job.kind == "install", job.state == "failed" {
                    line(job.error.map { String(localized: "Didn't install: \($0)") } ?? String(localized: "Couldn't install: the installer failed"), ok: false)
                } else {
                    line(c.backend == "lcu" ? String(localized: "LCU isn't installed on this Mac yet") : String(localized: "Cua Driver isn't installed on this Mac yet"), ok: false)
                }
                Text(c.backend == "lcu"
                     ? String(localized: "Downloads the LCU release from GitHub and verifies it. It doesn't register Claude / Codex approval screens or install ChatGPT; install the ChatGPT desktop app yourself first.")
                     : String(localized: "Downloads the installer from Cua's website, verifies it, installs it in Applications and turns off its usage statistics."))
                    .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                action(c.job?.state == "failed" ? String(localized: "Reinstall") : (c.backend == "lcu" ? String(localized: "Install LCU") : String(localized: "Install Cua Driver")), "install", prominent: true, identifier: "computer-install")
            }
        } else if c.desktop == "locked" {
            // Locked: nothing gets through to its apps (Cua refuses, screen_locked), so unlocking comes first. Its live
            // screen shows the lock screen, where the password is typed (docs/extensions/computer-use.md「锁屏」).
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                line(String(localized: "This Mac is locked. Unlock it to control it."), ok: false)
                if !c.screenSharing {
                    Text("Unlock it on that Mac. With its Screen Sharing on, you can also unlock it remotely here over VNC.")
                        .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                } else if oneTap {
                    Text("Tap One-Tap Unlock; after you verify, the saved password is typed on its lock screen.")
                        .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    HStack(spacing: Theme.Space.s) {
                        Button { Task { await unlockNow() } } label: {
                            Label("One-Tap Unlock", systemImage: Theme.Icon.locked).font(Theme.Font.labelStrong).lineLimit(1)
                        }
                        .themedButton(prominent: true)
                        .accessibilityIdentifier("computer-unlock-now")
                        look(prominent: false)
                    }
                } else {
                    Text("Tap Unlock to open its screen, switch to Take Over, and type that Mac's login password on the lock screen.")
                        .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    Button { model.openScreen(ref) } label: {
                        Label("Unlock", systemImage: Theme.Icon.screen).font(Theme.Font.labelStrong).lineLimit(1)
                    }
                    .themedButton(prominent: true)
                    .accessibilityIdentifier("computer-unlock")
                }
                if c.screenSharing, screenKept { unlockRow(locked: true) }
            }
        } else if c.daemon, !c.missingPermissions.isEmpty {
            grantGuide(c, asking: false)
        } else if c.ready {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                line(String(localized: "Ready to control") + (c.version.map { " · \(c.backend == "lcu" ? "LCU" : "Cua Driver") \($0)" } ?? ""), ok: true)
                if c.screenSharing {
                    HStack(spacing: Theme.Space.s) {
                        look(prominent: false)
                        if screenKept { ScreenForgetButton(ref: ref, kept: $screenKept) }
                    }
                } else {
                    Self.noScreenSharing
                }
                if c.screenSharing, screenKept { unlockRow(locked: false) }
                noLockRow(c)
                if !apps.isEmpty { allowedApps }
            }
        } else {
            line(c.note ?? String(localized: "Not sure yet whether it can be controlled"), ok: false)
        }
    }

    /// The way to Cua Driver's two permissions (#1280): what is allowed already, checked; the rest as numbered steps in
    /// System Settings' own words; Grant Access (macOS's dialogs), Check Again, and VNC to do it from the phone.
    private func grantGuide(_ c: MachineComputer, asking: Bool) -> some View {
        let steps = GrantGuide.steps(c)
        let failed = c.job.flatMap { $0.kind == "grant" && $0.state == "failed" ? $0.error : nil }
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                if asking {
                    ProgressView().controlSize(.small)
                } else {
                    ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.caution)
                }
                Text(failed.map { String(localized: "Couldn't request permission: \($0)") } ?? GrantGuide.header(c, asking: asking))
                    .font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("computer-status")
            ForEach(Array(steps.enumerated()), id: \.element.id) { _, step in
                Label {
                    Text(step.text).font(Theme.Font.label).foregroundStyle(step.done ? Color.secondary : Color.primary)
                        .fixedSize(horizontal: false, vertical: true)
                } icon: {
                    if step.done {
                        ThemedIcon(Theme.Icon.success).foregroundStyle(Theme.positive)
                    } else {
                        Text("\(GrantGuide.number(of: step, in: steps))").font(Theme.Font.labelStrong.monospacedDigit()).foregroundStyle(Theme.accent)
                    }
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("computer-step-\(step.id)")
                .accessibilityValue(step.done ? String(localized: "Done [Task Sheets f28461]") : String(localized: "To do"))
            }
            HStack(spacing: Theme.Space.s) {
                if !asking { action(String(localized: "Grant Access"), "grant", prominent: !c.screenSharing, identifier: "computer-grant") }
                if c.screenSharing { look(prominent: true) }
                Button { Task { await recheck() } } label: {
                    if checking { ProgressView().controlSize(.small) } else { Text("Check Again [Browser Section 953cf0]").font(Theme.Font.labelStrong).lineLimit(1) }
                }
                .themedButton()
                .disabled(checking)
                .accessibilityIdentifier("computer-recheck")
            }
            if !c.screenSharing { Self.noScreenSharing }
        }
    }

    private func line(_ text: String, ok: Bool) -> some View {
        Label {
            Text(text).font(Theme.Font.secondary).foregroundStyle(ok ? Color.primary : Color.secondary)
                .fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(ok ? Theme.Icon.success : Theme.Icon.error).foregroundStyle(ok ? Theme.positive : Theme.caution)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("computer-status")
    }

    private func action(_ title: String, _ what: String, prominent: Bool = false, identifier: String) -> some View {
        Button { Task { await run(what) } } label: {
            Text(title).font(Theme.Font.labelStrong).lineLimit(1)
        }
        .themedButton(prominent: prominent)
        .disabled(asking)
        .accessibilityIdentifier(identifier)
    }

    /// VNC: the Mac's live screen, where macOS's dialogs can be answered from the phone too.
    private func look(prominent: Bool) -> some View {
        Button {
            model.openVNC(ref)
        } label: {
            Label("VNC", systemImage: Theme.Icon.screen).font(Theme.Font.labelStrong).lineLimit(1)
        }
        .themedButton(prominent: prominent)
        .accessibilityIdentifier("computer-screen")
    }

    static let noScreenSharing = Text("Turn on Screen Sharing in System Settings › General › Sharing on that Mac, and you can see its screen here and tap Allow remotely.")
        .font(Theme.Font.label).foregroundStyle(.secondary)

    // MARK: Calls

    private func recheck() async {
        checking = true
        defer { checking = false }
        await refresh()
    }

    private func refresh() async {
        guard let api = model.api(ref.group) else { return }
        if let c = try? await api.computer(machine: ref.id) { fresh = c }
        if let a = try? await api.computerApps(machine: ref.id) { apps = a }
    }

    #if os(macOS)
    static let oneTapTitle = String(localized: "One-Tap Unlock from This Mac")
    #else
    static let oneTapTitle = String(localized: "One-Tap Unlock from This iPhone")
    #endif

    /// deviceOwnerAuthentication is Touch ID (or the password) on macOS, Face ID (or the password) on iOS.
    #if os(macOS)
    static let oneTapAuthName = String(localized: "Touch ID")
    #else
    static let oneTapAuthName = String(localized: "Face ID")
    #endif

    /// One-Tap Unlock on this device (it needs the sign-in kept), and Lock while it is on (#1507 a-2).
    private func unlockRow(locked: Bool) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Toggle(isOn: Binding(get: { oneTap }, set: { on in
                if on {
                    askOneTap = true
                } else {
                    ScreenUnlock.set(false, for: ref)
                    oneTap = false
                }
            })) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(Self.oneTapTitle).font(Theme.Font.body).lineLimit(1).minimumScaleFactor(0.8)
                    Text("When it's locked, verify once and the saved password is typed on its lock screen").font(Theme.Font.label).foregroundStyle(.secondary)
                }
            }
            .accessibilityIdentifier("computer-one-tap")
            if oneTap, !locked {
                Button { Task { await run("lock") } } label: {
                    Label("Lock", systemImage: Theme.Icon.locked).font(Theme.Font.labelStrong).lineLimit(1)
                }
                .themedButton()
                .disabled(asking)
                .accessibilityIdentifier("computer-lock")
            }
        }
        .padding(.top, Theme.Space.xs)
    }

    static let noLockWarning = String(localized: "While the extension is on, this Mac won't lock when idle, so anyone sitting at it can use it. This turns itself off after 30 minutes without the hub.")

    /// Don't Auto-Lock While On for this Mac (the plugin's no_auto_lock; asked about first): its node holds caffeinate.
    private func noLockRow(_ c: MachineComputer) -> some View {
        Toggle(isOn: Binding(get: { c.noAutoLock }, set: { on in
            if on { askNoLock = true } else { Task { await setNoLock(false) } }
        })) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text("Don't Auto-Lock While On").font(Theme.Font.body).lineLimit(1).minimumScaleFactor(0.8)
                Text("Doesn't change System Settings; turns off after 30 minutes without the hub").font(Theme.Font.label).foregroundStyle(.secondary)
            }
        }
        .disabled(saving)
        .accessibilityIdentifier("computer-no-lock")
        .padding(.top, Theme.Space.xs)
        .confirmationDialog("Turn On “Don't Auto-Lock While On”?", isPresented: $askNoLock, titleVisibility: .visible) {
            Button(String(localized: "action.turnOn", defaultValue: "Turn On")) { Task { await setNoLock(true) } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(Self.noLockWarning)
        }
    }

    private func setNoLock(_ on: Bool) async {
        guard let api = model.api(ref.group) else { return }
        saving = true
        defer { saving = false }
        do {
            fresh = try await api.setComputerNoAutoLock(machine: ref.id, on)
            Haptics.success()
        } catch {
            if let m = error.shown { model.toast = Toast(on ? String(localized: "Couldn't turn on: \(m)") : String(localized: "Couldn't turn off: \(m)"), style: .error) }
        }
    }

    /// Face ID (or the passcode), then the live screen signs in and types the kept password on the lock screen.
    private func unlockNow() async {
        let name = model.machine(ref)?.name ?? ref.id
        guard await ScreenUnlock.authenticate(name) else { return }
        model.openScreen(ref, unlock: true)
    }

    /// The apps always allowed here (#1507): each can be taken back, and is asked about again the next time.
    private var allowedApps: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text("Apps Allowed for the Assistant").font(Theme.Font.label).foregroundStyle(.secondary)
            ForEach(apps) { app in
                HStack(spacing: Theme.Space.s) {
                    Text(app.name).font(Theme.Font.secondary).lineLimit(1)
                    Spacer(minLength: Theme.Space.s)
                    Button { Task { await remove(app) } } label: {
                        Text("Remove [Task Sheets 6135d4]").font(Theme.Font.labelStrong).lineLimit(1)
                    }
                    .themedButton()
                    .accessibilityIdentifier("computer-app-remove")
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("computer-app-\(app.bundleID)")
            }
        }
        .padding(.top, Theme.Space.xs)
    }

    private func remove(_ app: ComputerApp) async {
        guard let api = model.api(ref.group) else { return }
        do {
            apps = try await api.removeComputerApp(machine: ref.id, bundleID: app.bundleID)
            Haptics.success()
        } catch {
            if let m = error.shown { model.toast = Toast(String(localized: "Couldn't remove: \(m) [Computer Section 5005cd]"), style: .error) }
        }
    }

    private func set(_ on: Bool) async {
        guard let api = model.api(ref.group) else { return }
        saving = true
        defer { saving = false }
        do {
            fresh = try await api.setComputer(machine: ref.id, enabled: on)
            Haptics.success()
        } catch {
            if let m = error.shown { model.toast = Toast(on ? String(localized: "Couldn't turn on: \(m)") : String(localized: "Couldn't turn off: \(m)"), style: .error) }
        }
    }

    private func run(_ what: String) async {
        guard let api = model.api(ref.group) else { return }
        asking = true
        defer { asking = false }
        do {
            fresh = try await api.computerJob(machine: ref.id, what)
        } catch {
            if let m = error.shown { model.toast = Toast(m, style: .error) }
        }
    }
}

/// The steps to Cua Driver's permissions on that Mac (#1280), checked against macOS 26's System Settings: Privacy &
/// Security › Accessibility and › Screen & System Audio Recording (Screen Recording before), where the app is listed as Cua Driver (com.trycua.driver).
nonisolated enum GrantGuide {
    struct Step: Hashable, Identifiable {
        var id: String
        var done: Bool
        var text: String
    }

    /// What is allowed already first, then what is left to do, then Check Again.
    static func steps(_ c: MachineComputer) -> [Step] {
        let accessibility = c.accessibility == "granted", screen = c.screenRecording == "granted"
        var done: [Step] = [], todo: [Step] = []
        if accessibility {
            done.append(Step(id: "accessibility", done: true, text: String(localized: "Accessibility: Cua Driver is allowed")))
        } else {
            todo.append(Step(id: "accessibility", done: false, text: String(localized: "On that Mac, open System Settings › Privacy & Security › Accessibility and turn on Cua Driver")))
        }
        if screen {
            done.append(Step(id: "screen", done: true, text: String(localized: "Screen & System Audio Recording: Cua Driver is allowed")))
        } else {
            todo.append(Step(id: "screen", done: false, text: accessibility
                ? String(localized: "On that Mac, open System Settings › Privacy & Security › Screen & System Audio Recording (Screen Recording on older macOS) and turn on Cua Driver")
                : String(localized: "In Screen & System Audio Recording in the same place (Screen Recording on older macOS), turn on Cua Driver too")))
            todo.append(Step(id: "prompt", done: false, text: String(localized: "If macOS asks to quit and reopen Cua Driver, or whether it may access the screen directly, choose Allow")))
        }
        todo.append(Step(id: "recheck", done: false, text: String(localized: "Come back here and tap Check Again")))
        return done + todo
    }

    /// A step's number among the ones left to do.
    static func number(of step: Step, in steps: [Step]) -> Int {
        (steps.filter { !$0.done }.firstIndex(of: step) ?? 0) + 1
    }

    static func header(_ c: MachineComputer, asking: Bool) -> String {
        if asking { return String(localized: "A permission prompt is open on that Mac. Follow the steps below to allow it.") }
        let missing = [c.accessibility, c.screenRecording].filter { $0 != "granted" }.count
        return missing == 2 ? String(localized: "Cua Driver needs two more permissions") : String(localized: "Cua Driver needs one more permission")
    }
}
