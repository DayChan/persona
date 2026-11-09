import BeautifulUI
import SwiftUI

// Settings › Managers (docs/assistant/managers.md): the list, one manager's page, and the install code sheet. The logic and words
// are in ManagersModel.swift.

/// The dot before a manager's status: online, offline, installing (its rows here, the headings of grouped pages).
func managerDot(_ tone: ManagersText.Tone) -> some View {
    Circle()
        .fill(tone == .online ? Theme.positive : tone == .installing ? Theme.accent : Theme.Hue.gray)
        .scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
        .accessibilityHidden(true)
}

/// Settings › Managers: every manager of the hub, and New Manager.
struct ManagersView: View {
    @Environment(AppModel.self) private var app
    @State private var page: ManagersModel
    @State private var naming = false
    @State private var newName = ""

    init(model: ManagersModel = ManagersModel()) { _page = State(initialValue: model) }

    var body: some View {
        SettingsForm {
            if page.unsupported {
                Section { Text("This hub's version doesn't support managers yet. Update the hub and come back.").foregroundStyle(.secondary) }
            } else if let list = page.managers {
                Section {
                    ForEach(list) { m in
                        NavigationLink(value: SettingsPage.manager(m.id)) { ManagerRow(manager: m, now: page.now) }
                            .accessibilityIdentifier("managers.row.\(m.id)")
                    }
                } footer: {
                    Text("Each manager is a whole assistant, running wherever you choose.")
                }
                Section {
                    Button {
                        newName = ""
                        naming = true
                    } label: {
                        Label("New Manager", systemImage: Theme.Icon.add).lineLimit(1)
                    }
                    .modifier(SettingsButtonLook())
                    .disabled(page.busy)
                    .accessibilityIdentifier("managers.new")
                }
            } else if let e = page.error {
                Section { Text(e).foregroundStyle(.secondary) }
            } else {
                Section { ProgressView().frame(maxWidth: .infinity) }
            }
        }
        .themedPage()
        .detailTitle(String(localized: "Managers"))
        .task { await page.load() }
        .refreshable { await page.load() }
        .onChange(of: app.managersRevision) { Task { await page.load() } }
        // The rows' "code expires in n min" and "last synced …" move on without a push.
        .task {
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(15))
                page.tick()
            }
        }
        .alert("New Manager", isPresented: $naming) {
            TextField("For example: Work", text: $newName)
            Button("Create") { Task { await page.create(name: newName) } }
                .disabled(!ManagersText.validName(newName))
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("You get an install code. Install it on the computer that will run it within 10 minutes.")
        }
        .managerCodeSheet(page)
        .managerFailures(page)
        #if DEBUG
        .task { // `-showManagerCode YES`: New Manager made, its code sheet open (screenshots, with the fake hub)
            guard UserDefaults.standard.bool(forKey: "showManagerCode") else { return }
            try? await Task.sleep(for: .seconds(1))
            await page.create(name: "书房") // i18n-ignore: a DEBUG sample name
        }
        #endif
    }
}

/// A manager in the list: its name and the Default chip, its status, and where it runs.
private struct ManagerRow: View {
    var manager: HubManager
    var now: Date

    var body: some View {
        let status = ManagersText.status(manager, now: now)
        let place = ManagersText.whereItRuns(manager)
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.managers)
                .scaledFrame(width: Theme.Size.icon)
                .foregroundStyle(manager.online || manager.installing != nil ? Theme.accent : Theme.Hue.gray)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                NameWithChips {
                    Text(manager.name).lineLimit(1)
                } chips: {
                    if manager.isDefault { Chip(text: String(localized: "Default [Prompt Composer 844b8c]"), look: .tint(Theme.accent)) }
                }
                HStack(spacing: Theme.Space.sm) {
                    managerDot(status.tone)
                    Text(status.text).lineLimit(1)
                }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                if !place.isEmpty {
                    Text(place).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
    }
}

/// Settings › Managers › one manager: where it runs and when it was there, and what can be done with it.
struct ManagerDetailView: View {
    let id: String
    @Environment(AppModel.self) private var app
    @State private var page: ManagersModel
    @State private var renaming = false
    @State private var newName = ""
    @State private var confirmMove = false
    @State private var confirmDelete = false

    init(id: String, model: ManagersModel = ManagersModel()) {
        self.id = id
        _page = State(initialValue: model)
    }

    var body: some View {
        let m = page.manager(id)
        SettingsForm {
            if page.unsupported {
                Section { Text("This hub's version doesn't support managers yet. Update the hub and come back.").foregroundStyle(.secondary) }
            } else if let m {
                facts(m)
                actions(m)
            } else if page.managers != nil {
                Section { Text("This manager is no longer there").foregroundStyle(.secondary) }
            } else if let e = page.error {
                Section { Text(e).foregroundStyle(.secondary) }
            } else {
                Section { ProgressView().frame(maxWidth: .infinity) }
            }
        }
        .themedPage()
        .detailTitle(m?.name ?? String(localized: "Managers"))
        .task { await page.load() }
        .refreshable { await page.load() }
        .onChange(of: app.managersRevision) { Task { await page.load() } }
        .task {
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(15))
                page.tick()
            }
        }
        .alert("Rename [Hubs View 83c37e]", isPresented: $renaming) {
            TextField("For example: Work", text: $newName)
            Button("Save") { if let m { Task { await page.rename(m, to: newName) } } }
                .disabled(!ManagersText.validName(newName))
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog("Install Elsewhere?", isPresented: $confirmMove, titleVisibility: .visible) {
            Button("Install Elsewhere", role: .destructive) { if let m { Task { await page.newCode(m) } } }
        } message: {
            Text("Installing elsewhere gives you a new install code. Once it's installed with that code, the manager where it ran before stops working right away.")
        }
        .confirmationDialog(deleteTitle(m), isPresented: $confirmDelete, titleVisibility: .visible) {
            Button(m?.installing != nil ? String(localized: "Cancel Install") : String(localized: "Delete"), role: .destructive) {
                guard let m else { return }
                // Back to the list: this page has nothing left to show. Only if the path is still the one this page was on
                // (the user may have gone elsewhere while the hub answered).
                let here = app.settingsPath
                Task {
                    if await page.delete(m), app.settingsPath == here, !here.isEmpty { app.settingsPath.removeLast() }
                }
            }
            Button("Keep", role: .cancel) {}
        } message: {
            Text(m?.installing != nil ? String(localized: "Its install code will stop working.") : String(localized: "Its token stops working right away and its connections are closed."))
        }
        .managerCodeSheet(page)
        .managerFailures(page)
    }

    private func deleteTitle(_ m: HubManager?) -> String {
        guard let m else { return "" }
        return m.installing != nil ? String(localized: "Cancel installing “\(m.name)”?") : String(localized: "Delete “\(m.name)”?")
    }

    @ViewBuilder private func facts(_ m: HubManager) -> some View {
        let status = ManagersText.status(m, now: page.now)
        let place = ManagersText.whereItRuns(m)
        Section {
            Group {
                statusRow(status)
                if m.isDefault { info(String(localized: "Default Manager"), String(localized: "Yes [V N C Pi P Diagnostics b5141d]")) }
                if !place.isEmpty { info(String(localized: "Runs On"), place) }
                if let v = m.version, !v.isEmpty { info(String(localized: "Version"), v, code: true) }
                if m.online, let at = m.since { info(String(localized: "Connected Since"), at.formatted(.relative(presentation: .named, unitsStyle: .wide))) }
                if !m.online, let at = m.lastSeen { info(String(localized: "Last Synced"), at.formatted(.relative(presentation: .named, unitsStyle: .wide))) }
            }
            .bareListRow()
        }
    }

    @ViewBuilder private func actions(_ m: HubManager) -> some View {
        let blocked = ManagersText.deleteBlocked(m)
        Section {
            #if os(macOS)
            // docs/apple/mac-design.md: a detail page's buttons are regular-size and grouped, not a row each.
            HStack(spacing: Theme.Space.s) {
                actionButtons(m, blocked: blocked)
                    .buttonStyle(.bordered)
                    .controlSize(.regular)
                Spacer(minLength: 0)
            }
            #else
            actionButtons(m, blocked: blocked)
            #endif
        } footer: {
            if let blocked { Text(blocked) }
        }
        .disabled(page.busy)
    }

    /// Rename, Make Default, Install Elsewhere / New Install Code, Delete / Cancel Install: the ones this manager has.
    @ViewBuilder private func actionButtons(_ m: HubManager, blocked: String?) -> some View {
        let installing = m.installing != nil
        if !installing {
            Button {
                newName = m.name
                renaming = true
            } label: { Label("Rename [Hubs View 83c37e]", systemImage: Theme.Icon.edit).lineLimit(1) }
            .accessibilityIdentifier("manager.rename")
        }
        if !installing, !m.isDefault {
            Button { Task { await page.makeDefault(m) } } label: {
                Label("Make Default", systemImage: Theme.Icon.makeDefault).lineLimit(1)
            }
            .accessibilityIdentifier("manager.makeDefault")
        }
        if !m.builtin {
            Button {
                if installing { Task { await page.newCode(m) } } else { confirmMove = true }
            } label: {
                Label(installing ? String(localized: "New Install Code") : String(localized: "Install Elsewhere"), systemImage: Theme.Icon.install).lineLimit(1)
            }
            .accessibilityIdentifier("manager.newCode")
        }
        let removeTitle = installing ? String(localized: "Cancel Install") : String(localized: "Delete")
        #if os(macOS)
        Button(role: .destructive) { confirmDelete = true } label: {
            Label(removeTitle, systemImage: Theme.Icon.delete).lineLimit(1)
                .foregroundStyle(blocked == nil ? Theme.danger : Theme.Hue.gray) // red says what it does
        }
        .disabled(blocked != nil)
        .accessibilityIdentifier("manager.delete")
        #else
        if blocked != nil {
            // Not a button that looks live: the footer says why it cannot go.
            Label(removeTitle, systemImage: Theme.Icon.delete)
                .lineLimit(1)
                .foregroundStyle(.secondary)
                .accessibilityIdentifier("manager.delete")
        } else {
            Button(role: .destructive) { confirmDelete = true } label: {
                // The whole row red, icon too (a list row's icon takes the accent otherwise).
                Label(removeTitle, systemImage: Theme.Icon.delete).lineLimit(1)
                    .foregroundStyle(Theme.danger)
            }
            .accessibilityIdentifier("manager.delete")
        }
        #endif
    }

    /// The status with its dot, drawn like the other facts rows in either style.
    @ViewBuilder private func statusRow(_ status: (text: String, tone: ManagersText.Tone)) -> some View {
        if AppStyle.current.usesBeautifulUI {
            BUIListRow(String(localized: "Status"), value: status.text) { EmptyView() } trailing: { managerDot(status.tone) }
        } else {
            LabeledContent(String(localized: "Status")) {
                HStack(spacing: Theme.Space.sm) {
                    managerDot(status.tone)
                    Text(status.text).font(Theme.Font.body).lineLimit(1)
                }
            }
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

/// The install code (and commands), its countdown, and once it ran out New Install Code.
private struct ManagerCodeSheet: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    var page: ManagersModel

    var body: some View {
        NavigationStack {
            SettingsForm {
                if let s = page.shown {
                    if page.expired {
                        Section {
                            Button { Task { await page.renewShown() } } label: {
                                Label("New Install Code", systemImage: Theme.Icon.install).lineLimit(1)
                            }
                            .disabled(page.busy)
                            .accessibilityIdentifier("managerCode.renew")
                        } header: {
                            Text("The install code has expired").textCase(nil)
                        }
                    } else {
                        let service = s.code.commands?.service, docker = s.code.commands?.docker
                        let commands = service != nil || docker != nil
                        Section {
                            copyRow(s.code.code, wraps: false)
                                .accessibilityIdentifier("managerCode.code")
                        } header: {
                            Text("Install Code").textCase(nil)
                        } footer: {
                            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                                Text("Code expires in \(ConnectDeviceText.clock(page.secondsLeft))")
                                    .font(Theme.Font.caption.monospacedDigit())
                                    .accessibilityIdentifier("managerCode.countdown")
                                if !commands { Text("Install the manager with this code on the computer that will run it. Once installed, it shows up in the list.") }
                            }
                        }
                        if let service {
                            Section { copyRow(service, wraps: true) } header: { Text("As a Service").textCase(nil) } footer: {
                                if docker == nil { commandsNote }
                            }
                        }
                        if let docker {
                            Section { copyRow(docker, wraps: true) } header: { Text("With Docker").textCase(nil) } footer: { commandsNote }
                        }
                    }
                }
            }
            .themedPage()
            .navigationTitle(page.shown.map { String(localized: "Install “\($0.name)”") } ?? "")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1) }
            }
        }
        .presentationDetents([.large])
        .presentationDragIndicator(.visible)
        .task(id: page.shown?.code) {
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(1))
                page.tick()
            }
        }
    }

    private var commandsNote: some View {
        Text("Run one of these on the computer that will run it. Once installed, it shows up in the list. The code works once.")
    }

    /// The text (a command wraps so all of it can be read before it is pasted; the code is one short line) and Copy.
    private func copyRow(_ text: String, wraps: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            Text(text)
                .font(Theme.Font.code)
                .lineLimit(wraps ? nil : 1)
                .truncationMode(.middle)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button {
                UIPasteboard.general.string = text
                app.toast = Toast(String(localized: "Copied [Mac Hover 07d6bb]"), style: .success)
            } label: {
                Label("Copy [Message Menu 63d90d]", systemImage: Theme.Icon.copy).lineLimit(1)
            }
            .buttonStyle(.borderless)
        }
    }
}

private extension View {
    /// The install code sheet, while the model has a code on screen.
    func managerCodeSheet(_ page: ManagersModel) -> some View {
        sheet(isPresented: Binding(get: { page.shown != nil }, set: { if !$0 { page.shown = nil } })) {
            ManagerCodeSheet(page: page)
        }
    }

    /// A change that did not go through: a toast in plain words.
    func managerFailures(_ page: ManagersModel) -> some View {
        modifier(ManagerFailures(page: page))
    }
}

private struct ManagerFailures: ViewModifier {
    @Environment(AppModel.self) private var app
    var page: ManagersModel

    func body(content: Content) -> some View {
        content.onChange(of: page.failure) { _, why in
            guard let why else { return }
            app.toast = Toast(why, style: .error)
            page.failure = nil
        }
    }
}
