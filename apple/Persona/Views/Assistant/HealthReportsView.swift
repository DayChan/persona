import SwiftUI

// Health Reports (docs/assistant/health.md「体检报告」, #4998): what was uploaded to the health space's reports/. A report opens as the
// record the health conversation wrote or as the original, and deleting takes both. The web's HealthReports.

/// The reports' state and what it asks the hub (MVVM, docs/apple/ios.md「逻辑层的选型」).
@Observable final class HealthReportsModel {
    private(set) var reports: [APIClient.HealthReport] = []
    private(set) var loaded = false
    /// Why the list could not be read; shown only while there is nothing to show.
    private(set) var error: String?
    @ObservationIgnored private let machine: String
    @ObservationIgnored private let deps: AssistantDeps

    init(machine: String, deps: AssistantDeps = .live) {
        self.machine = machine
        self.deps = deps
    }

    func load() async {
        guard let api = deps.api() else { return }
        do {
            reports = try await api.healthReports(machine: machine)
            error = nil
        } catch {
            self.error = error.shown ?? self.error
        }
        loaded = true
    }

    func delete(_ report: APIClient.HealthReport) async {
        guard let api = deps.api() else { return }
        do {
            try await api.deleteHealthReport(name: report.name, machine: machine)
            Haptics.success()
            await load()
        } catch {
            deps.report(error)
        }
    }
}

struct HealthReportsView: View {
    var machine: MachineRef
    @State private var page: HealthReportsModel
    @State private var file: FileRef?
    @State private var deleting: APIClient.HealthReport?

    init(machine: MachineRef) {
        self.machine = machine
        _page = State(initialValue: HealthReportsModel(machine: machine.id))
    }

    var body: some View {
        content
            .themedPage()
            .pageTitle(String(localized: "Checkup reports"))
            .task { await page.load() }
            .refreshable { await page.load() }
            #if DEBUG
            .task(id: page.loaded) { // the menu's items are out of the runtime CLI's reach: straight to what they open
                guard let first = page.reports.first else { return }
                if UserDefaults.standard.bool(forKey: "showReportDelete") { deleting = first }
                if UserDefaults.standard.bool(forKey: "openReportRecord"), let record = first.recordPath { open(record) }
            }
            #endif
            .sheet(item: $file) { FileViewer(ref: $0) }
            .confirmationDialog(
                "Delete this report?",
                isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                titleVisibility: .visible,
                presenting: deleting
            ) { report in
                Button("Delete for good", role: .destructive) { Task { await page.delete(report) } }
                Button("Keep it", role: .cancel) {}
            } message: { _ in
                Text("The original and its summary will be deleted for good. Anything your health conversations and health memory already mention stays; remove that separately.")
            }
    }

    @ViewBuilder private var content: some View {
        if !page.loaded {
            ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if page.reports.isEmpty {
            if let error = page.error {
                EmptyStateView(symbol: Theme.Icon.document, title: String(localized: "Couldn't read checkup reports"), message: error)
            } else {
                EmptyStateView(symbol: Theme.Icon.document, title: String(localized: "No checkup reports yet"),
                               message: String(localized: "Tap \"Upload checkup report\" on the Health page. Reports you upload stay here, and you can view or delete them anytime."))
            }
        } else {
            List(page.reports) { report in row(report) }
        }
    }

    private func open(_ path: String) { file = FileRef(group: machine.group, machineID: machine.id, path: path) }

    @ViewBuilder private func actions(_ report: APIClient.HealthReport) -> some View {
        if let record = report.recordPath {
            Button { open(record) } label: { Label("View summary", systemImage: Theme.Icon.document) }
        }
        if let path = report.path {
            Button { open(path) } label: { Label("Open original", systemImage: Theme.Icon.file) }
        }
        Button(role: .destructive) { deleting = report } label: { Label("Delete", systemImage: Theme.Icon.delete) }
    }

    /// Every action is one tap away, in a menu: a report has two things to open and they read differently.
    private func row(_ report: APIClient.HealthReport) -> some View {
        Menu { actions(report) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.document)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(Theme.Hue.pink)
                    .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                    .background(Theme.Hue.pink.opacity(0.14), in: .circle)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(report.title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                    Text(report.recordPath == nil ? String(localized: "Not summarized yet") : String(localized: "Summarized"))
                        .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: Theme.Space.s)
                if let at = report.modifiedAt {
                    Text(at.formatted(date: .numeric, time: .omitted)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .menuStyle(.button)
        .accessibilityIdentifier("health-report")
        .swipeActions(edge: .trailing) {
            Button { deleting = report } label: { Label("Delete", systemImage: Theme.Icon.delete) }.tint(Theme.danger)
        }
    }
}
