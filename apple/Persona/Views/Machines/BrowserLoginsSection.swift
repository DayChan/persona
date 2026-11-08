import SwiftUI
import UniformTypeIdentifiers

/// Browser Sign-ins on a Mac's page (#1304, docs/plans/plan-ego-browser.md「导入 cookies」; the web's BrowserLogins): the sites whose
/// sign-ins ego lite keeps (how many cookies, until when, never what), clearing a site's or all, and importing a cookie
/// file (cookies.txt, or an extension's JSON). The file is read here, its sites ticked by the user (none at first), and
/// only the ticked ones go to the hub, which passes them to the Mac and keeps nothing. The Mac must be online.
struct BrowserLoginsSection: View {
    @Environment(AppModel.self) private var model
    let ref: MachineRef
    var online: Bool
    @State private var sites: [BrowserCookieSite]?
    @State private var error: String?
    @State private var busy = false
    @State private var picking = false
    @State private var file: PickedFile?
    @State private var clearing: Clearing?

    struct PickedFile: Identifiable {
        var name: String
        var sites: [CookieFileSite]
        var bad: Int
        var ticked: Set<String> = []
        var id: String { name }
        var cookies: [BrowserCookie] { sites.filter { ticked.contains($0.site) }.flatMap(\.cookies) }
    }

    enum Clearing: Identifiable {
        case site(String), all
        var id: String { if case .site(let s) = self { s } else { "*" } }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                Text("Browser Sign-ins").font(Theme.Font.heading)
                if let sites { Text("\(sites.count)").font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary) }
                Spacer(minLength: Theme.Space.s)
                Button { picking = true } label: {
                    Label("Import", systemImage: Theme.Icon.file).lineLimit(1)
                }
                .themedButton()
                .disabled(busy || !online)
                .accessibilityIdentifier("browser-logins-import")
            }
            .padding(.horizontal, Theme.Space.xs)
            VStack(alignment: .leading, spacing: .zero) {
                content
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .card()
            Group {
                #if os(macOS)
                // read(_:) runs on this machine on every platform, so on the Mac the file is parsed locally, not on a phone.
                Text("Only sites and counts are listed, never cookie contents. Imported files are read on this machine, and only the sites you check go through the hub straight to the target machine. The hub keeps nothing, and the assistant can't see or import them.")
                #else
                Text("Only sites and counts are listed, never cookie contents. Imported files are read on your phone, and only the sites you check go through the hub straight to this Mac. The hub keeps nothing, and the assistant can't see or import them.")
                #endif
            }
            .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, Theme.Space.xs)
        }
        .task(id: online) {
            await load()
            #if DEBUG
            // `-openCookieFile <path>`: as if that file were picked (UI tests and screenshots; the system's picker is
            // not theirs to drive).
            if let path = UserDefaults.standard.string(forKey: "openCookieFile"), file == nil, sites != nil { read(URL(fileURLWithPath: path)) }
            #endif
        }
        .fileImporter(isPresented: $picking, allowedContentTypes: [.plainText, .json, .text, .data]) { result in
            if case .success(let url) = result { read(url) }
        }
        .sheet(item: $file) { _ in
            CookiePickSheet(file: $file, busy: busy) { await importTicked() }
        }
        .confirmationDialog(clearTitle, isPresented: Binding(get: { clearing != nil }, set: { if !$0 { clearing = nil } }), titleVisibility: .visible) {
            Button("Clear [Image Editor bce237]", role: .destructive) {
                let what = clearing
                Task { await clear(what) }
            }
            .accessibilityIdentifier("browser-logins-clear-confirm")
        } message: {
            Text(clearing.map { if case .all = $0 { String(localized: "All of ego lite's cookies on this Mac will be deleted, and every site will need you to sign in again. This includes your own sign-ins in ego.") } else { String(localized: "This site will be signed out in ego lite on this Mac. This includes your own sign-in in ego.") } } ?? "")
        }
    }

    private var clearTitle: String {
        switch clearing {
        case .site(let s): String(localized: "Clear Sign-in for \(s)?")
        case .all: String(localized: "Clear Sign-ins for All Sites?")
        case nil: ""
        }
    }

    @ViewBuilder private var content: some View {
        if !online {
            Text("This Mac must be online to view and import.").font(Theme.Font.secondary).foregroundStyle(.secondary)
        } else if let error {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text(error).font(Theme.Font.secondary).foregroundStyle(Theme.danger).fixedSize(horizontal: false, vertical: true)
                Button("Try Again") { Task { await load() } }
                    .lineLimit(1)
                    .themedButton()
                    .accessibilityIdentifier("browser-logins-retry")
            }
            .padding(.vertical, Theme.Space.s)
        } else if let sites {
            if sites.isEmpty {
                Text("No sites are signed in to ego lite yet. Import a cookie file exported from another computer or browser, and the assistant can open those sites as you.")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            } else {
                ForEach(Array(sites.enumerated()), id: \.element.id) { i, s in
                    if i > 0 { Divider() }
                    row(s)
                }
                Divider()
                Button(role: .destructive) { clearing = .all } label: {
                    Text("Clear All").font(Theme.Font.secondary).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.danger)
                .disabled(busy)
                .padding(.vertical, Theme.Space.s)
                .accessibilityIdentifier("browser-logins-clear-all")
            }
        } else {
            HStack(spacing: Theme.Space.s) {
                ProgressView().controlSize(.small)
                Text("Reading").font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
        }
    }

    private func row(_ s: BrowserCookieSite) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.siteLogin).foregroundStyle(.secondary).scaledFrame(width: Theme.Size.icon)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(s.domain).font(Theme.Font.body).lineLimit(1).accessibilityIdentifier("browser-logins-site")
                Text(Self.detail(count: s.count, expires: s.expires)).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: Theme.Space.s)
            Button("Clear [Image Editor bce237]") { clearing = .site(s.domain) }
                .lineLimit(1)
                .themedButton()
                .disabled(busy)
                .accessibilityIdentifier("browser-logins-clear")
        }
        .padding(.vertical, Theme.Space.s)
    }

    static func detail(count: Int, expires: Date?) -> String {
        if let expires {
            let date = expires.formatted(date: .numeric, time: .omitted)
            return String(localized: "\(count) cookies · expire by \(date)")
        }
        return String(localized: "\(count) cookies · end when the browser closes")
    }

    // MARK: Calls

    private func load() async {
        sites = nil
        error = nil
        guard online, let api = model.api(ref.group) else { return }
        do { sites = try await api.browserCookies(machine: ref.id).sites } catch { self.error = error.shown }
    }

    private func read(_ url: URL) {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        do {
            let text = try String(contentsOf: url, encoding: .utf8)
            let parsed = try CookieFile.parse(text)
            file = PickedFile(name: url.lastPathComponent, sites: CookieFile.sites(parsed.cookies), bad: parsed.bad)
        } catch {
            model.toast = Toast(error.localizedDescription, style: .error)
        }
    }

    private func importTicked() async {
        guard let cookies = file?.cookies, !cookies.isEmpty, let api = model.api(ref.group) else { return }
        busy = true
        defer { busy = false }
        do {
            let a = try await api.importBrowserCookies(machine: ref.id, cookies)
            sites = a.sites
            file = nil // the file's contents are gone once sent
            let skipped = a.skipped ?? 0
            let imported = a.imported ?? 0
            model.toast = Toast(skipped > 0 ? String(localized: "Imported \(imported); skipped \(skipped) expired or malformed") : String(localized: "Imported \(imported) cookies"), style: .success)
        } catch {
            if let m = error.shown { model.toast = Toast(String(localized: "Couldn't import: \(m)"), style: .error) }
        }
    }

    private func clear(_ what: Clearing?) async {
        guard let what, let api = model.api(ref.group) else { return }
        busy = true
        defer { busy = false; clearing = nil }
        do {
            let site: String? = if case .site(let s) = what { s } else { nil }
            let a = try await api.clearBrowserCookies(machine: ref.id, site: site)
            sites = a.sites
            let removed = a.removed ?? 0
            model.toast = Toast(String(localized: "Cleared \(removed) cookies"), style: .success)
        } catch {
            if let m = error.shown { model.toast = Toast(String(localized: "Couldn't clear: \(m)"), style: .error) }
        }
    }
}

/// The sites of a picked cookie file to tick (none at first), and Import N.
private struct CookiePickSheet: View {
    @Binding var file: BrowserLoginsSection.PickedFile?
    var busy: Bool
    var run: () async -> Void

    var body: some View {
        NavigationStack {
            List {
                if let f = file {
                    Section {
                        ForEach(f.sites) { s in
                            let none = s.cookies.isEmpty
                            Button {
                                guard !none else { return }
                                if file?.ticked.contains(s.site) == true { file?.ticked.remove(s.site) } else { file?.ticked.insert(s.site) }
                            } label: {
                                HStack(spacing: Theme.Space.m) {
                                    ThemedIcon(f.ticked.contains(s.site) ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                                        .foregroundStyle(f.ticked.contains(s.site) ? Theme.accent : Color.secondary)
                                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                        Text(s.site).font(Theme.Font.body).foregroundStyle(.primary).lineLimit(1)
                                        Text(Self.detail(s)).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                                    }
                                }
                            }
                            .buttonStyle(.plain) // the label in its own colors, not the tint; the whole row takes the tap
                            .contentShape(.rect)
                            .disabled(none)
                            .opacity(none ? 0.5 : 1)
                            .accessibilityIdentifier("browser-logins-pick")
                        }
                    } header: {
                        Text(f.bad > 0 ? String(localized: "\(f.name) · \(f.sites.count) sites · skipped \(f.bad) unreadable lines") : String(localized: "\(f.name) · \(f.sites.count) sites"))
                    } footer: {
                        Text("Nothing is checked at first; only what you check is imported. They go into ego lite's default profile on this Mac, so your own browsing in ego uses these sign-ins too.")
                    }
                }
            }
            .navigationTitle("Choose Sites to Import")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { file = nil }.lineLimit(1).disabled(busy)
                }
                ToolbarItem(placement: .confirmationAction) {
                    let n = file?.cookies.count ?? 0
                    Button(n > 0 ? String(localized: "Import \(n)") : String(localized: "Import")) { Task { await run() } }
                        .lineLimit(1)
                        .disabled(busy || n == 0)
                        .accessibilityIdentifier("browser-logins-confirm")
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    static func detail(_ s: CookieFileSite) -> String {
        let count = s.cookies.count
        let date = s.expires.map { Date(timeIntervalSince1970: $0).formatted(date: .numeric, time: .omitted) }
        switch (date, s.expired > 0) {
        case let (date?, true): return String(localized: "\(count) cookies · expire by \(date) · \(s.expired) expired, not imported")
        case let (date?, false): return String(localized: "\(count) cookies · expire by \(date)")
        case (nil, true): return String(localized: "\(count) cookies · \(s.expired) expired, not imported")
        case (nil, false): return String(localized: "cookies.count", defaultValue: "\(count) cookies")
        }
    }
}
