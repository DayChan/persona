import SwiftUI

/// Browser (ego lite) on a Mac's page while the hub's plugin ego is on (docs/plans/plan-ego-browser.md): the switch that lets
/// the assistant drive its ego lite, and how far that is: Install ego lite when it is not there; its first-run setup as
/// steps (open it on the Mac or over its live screen, import a browser's data, finish); Ready in green, with what is left
/// (its usage statistics) ticked or not. The node is asked again when the page opens, and every few seconds while a job
/// runs or the setup is under way.
struct BrowserSection: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    let ref: MachineRef
    var browser: MachineBrowser
    var online: Bool
    @State private var fresh: MachineBrowser?
    @State private var saving = false
    @State private var asking = false
    @State private var checking = false

    private var shown: MachineBrowser { fresh ?? browser }

    /// Asked again every few seconds: a job runs, or the setup was opened on the Mac and is not done yet.
    private var watching: Bool {
        guard online, shown.enabled else { return false }
        if shown.job?.running == true { return true }
        return shown.installed && !shown.setUp && shown.job?.kind == "open"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("Browser").font(Theme.Font.heading).padding(.horizontal, Theme.Space.xs)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Toggle(isOn: Binding(get: { shown.enabled }, set: { on in Task { await set(on) } })) {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("Let the Assistant Use This Browser").font(Theme.Font.body).lineLimit(1).minimumScaleFactor(0.8)
                        Text("Opens pages, taps, fills forms and takes screenshots through ego lite").font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                }
                .disabled(saving || !online)
                .accessibilityIdentifier("browser-switch")
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
    }

    // MARK: State

    @ViewBuilder private var state: some View {
        let b = shown
        if !online {
            line(String(localized: "Machine is offline. Available once it's connected."), ok: false)
        } else if let job = b.job, job.running, job.kind == "install" {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                HStack(spacing: Theme.Space.s) {
                    ProgressView().controlSize(.small)
                    Text(job.step ?? String(localized: "Installing…")).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("browser-status")
                action(String(localized: "Cancel"), "cancel", identifier: "browser-cancel")
            }
        } else if !b.installed {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if let job = b.job, job.kind == "install", job.state == "failed" {
                    line(job.error.map { String(localized: "Didn't install: \($0)") } ?? String(localized: "Couldn't install: the installation failed"), ok: false)
                } else {
                    line(String(localized: "ego lite isn't installed on this Mac yet"), ok: false)
                }
                Text("Downloads the installer from ego's website, checks Citro Labs' signature and Apple's notarization, then installs it in Applications.")
                    .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                action(b.job?.state == "failed" ? String(localized: "Reinstall") : String(localized: "Install ego lite"), "install", prominent: true, identifier: "browser-install")
            }
        } else if !b.setUp {
            setUpGuide(b)
        } else if !b.ready {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                line(b.note ?? String(localized: "ego-browser isn't working yet"), ok: false)
                recheckButton
            }
        } else {
            readyList(b)
        }
    }

    /// Its first-run setup, done on the Mac: numbered steps in ego's own words, Open ego lite, VNC, Check Again.
    private func setUpGuide(_ b: MachineBrowser) -> some View {
        let steps = BrowserGuide.setUpSteps(b)
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                if b.job?.running == true { ProgressView().controlSize(.small) } else {
                    ThemedIcon(Theme.Icon.error).foregroundStyle(Theme.caution)
                }
                Text(b.job?.kind == "open" ? String(localized: "ego lite is open on that Mac. Follow the steps below to finish its first-run setup.") : String(localized: "ego lite is installed. Its first-run setup still needs to be finished."))
                    .font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("browser-status")
            stepList(steps)
            HStack(spacing: Theme.Space.s) {
                action(String(localized: "Open ego lite"), "open", prominent: !b.screenSharing, identifier: "browser-open")
                if b.screenSharing { look(prominent: true) }
                recheckButton
            }
        }
    }

    /// Ready in green, then what else there is: the browser data imported, its usage statistics.
    private func readyList(_ b: MachineBrowser) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            line(b.version.map { String(localized: "Ready · the assistant can use this browser · ego lite \($0)") } ?? String(localized: "Ready · the assistant can use this browser"), ok: true)
            stepList(BrowserGuide.readySteps(b))
            if b.analytics == "on" { recheckButton }
        }
    }

    private func stepList(_ steps: [GrantGuide.Step]) -> some View {
        ForEach(steps) { step in
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
            .accessibilityIdentifier("browser-step-\(step.id)")
            .accessibilityValue(step.done ? String(localized: "Done [Task Sheets f28461]") : String(localized: "To do"))
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
        .accessibilityIdentifier("browser-status")
    }

    private func action(_ title: String, _ what: String, prominent: Bool = false, identifier: String) -> some View {
        Button { Task { await run(what) } } label: {
            Text(title).font(Theme.Font.labelStrong).lineLimit(1)
        }
        .themedButton(prominent: prominent)
        .disabled(asking)
        .accessibilityIdentifier(identifier)
    }

    private var recheckButton: some View {
        Button { Task { await recheck() } } label: {
            if checking { ProgressView().controlSize(.small) } else { Text("Check Again [Browser Section 953cf0]").font(Theme.Font.labelStrong).lineLimit(1) }
        }
        .themedButton()
        .disabled(checking)
        .accessibilityIdentifier("browser-recheck")
    }

    /// VNC: the Mac's live screen, where ego's setup is done from the phone.
    private func look(prominent: Bool) -> some View {
        Button {
            model.openVNC(ref)
        } label: {
            Label("VNC", systemImage: Theme.Icon.screen).font(Theme.Font.labelStrong).lineLimit(1)
        }
        .themedButton(prominent: prominent)
        .accessibilityIdentifier("browser-screen")
    }

    // MARK: Calls

    private func refresh() async {
        guard let api = model.api(ref.group) else { return }
        if let b = try? await api.browser(machine: ref.id) { fresh = b }
    }

    private func recheck() async {
        checking = true
        defer { checking = false }
        await refresh()
    }

    private func set(_ on: Bool) async {
        guard let api = model.api(ref.group) else { return }
        saving = true
        defer { saving = false }
        do {
            fresh = try await api.setBrowser(machine: ref.id, enabled: on)
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
            fresh = try await api.browserJob(machine: ref.id, what)
        } catch {
            if let m = error.shown { model.toast = Toast(m, style: .error) }
        }
    }
}

/// ego lite's first-run setup and what is left once it is ready, as steps (checked on 0.6.2, 2026-11-04).
nonisolated enum BrowserGuide {
    static func setUpSteps(_ b: MachineBrowser) -> [GrantGuide.Step] {
        // Build the platform-specific line first: a conditional compilation directive can't sit
        // between elements of an array literal (it is only valid in statement or result-builder position).
        #if os(macOS)
        let open = String(localized: "Tap Open ego lite and it opens on that Mac (with Screen Sharing on, you can use VNC to control it remotely from this Mac)")
        #else
        let open = String(localized: "Tap Open ego lite and it opens on that Mac (with Screen Sharing on, you can use VNC to work on it from your phone)")
        #endif
        return [
            GrantGuide.Step(id: "open", done: false, text: open),
            GrantGuide.Step(id: "import", done: false, text: String(localized: "In the first-run setup, choose to import data from Chrome (or another browser): your site sign-ins come along. macOS asks for the keychain password several times; enter that Mac's login password each time. You can skip importing")),
            GrantGuide.Step(id: "finish", done: false, text: String(localized: "Finish the first-run setup; it installs the ego-browser command")),
            GrantGuide.Step(id: "recheck", done: false, text: String(localized: "Come back here and tap Check Again")),
        ]
    }

    static func readySteps(_ b: MachineBrowser) -> [GrantGuide.Step] {
        var out = [GrantGuide.Step(id: "setup", done: true, text: String(localized: "First-run setup is done"))]
        out.append(b.imported
            ? GrantGuide.Step(id: "import", done: true, text: String(localized: "Data from another browser was imported; the assistant can use those sites' sign-ins"))
            : GrantGuide.Step(id: "import", done: true, text: String(localized: "No data was imported from another browser: sign in to the sites you need in ego lite first")))
        switch b.analytics {
        case "on":
            out.append(GrantGuide.Step(id: "analytics", done: false, text: String(localized: "Turn off ego's usage statistics: in ego lite on that Mac, open Settings › Sync and services and turn off “Automatically sends usage statistics and crash reports to Citro Labs”")))
        case "off":
            out.append(GrantGuide.Step(id: "analytics", done: true, text: String(localized: "ego's usage statistics are off")))
        default:
            break
        }
        return out
    }
}
