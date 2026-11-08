import SwiftUI

/// What This Computer Can Do on a machine's page (#3018, the web's NodeHealth, docs/server/machines.md「自检」): the node's
/// self-check, first as features (Approval Cards on Phone, Status and Notifications, Terminal and Engine, …) with Ready /
/// Partly Working / Problem / Off, then every check behind Show All Checks. It checks when the page opens, when the
/// computer comes online and on Check Again; Repair (after one confirmation) has the node write its own generated files again and re-check. No polling.
struct NodeHealthSection: View {
    @Environment(AppModel.self) private var app
    var ref: MachineRef
    var machine: Machine
    @State private var model: NodeHealthModel?
    @State private var showsAll = false
    @State private var confirmsFix = false
    @State private var deploying = false

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("What This Computer Can Do").font(Theme.Font.heading)
                .padding(.horizontal, Theme.Space.xs)
            VStack(alignment: .leading, spacing: 0) { content }
                .frame(maxWidth: .infinity, alignment: .leading)
                .card(padding: 0)
        }
        .task(id: Key(ref: ref, online: machine.online)) {
            let m = model ?? makeModel()
            model = m
            showsAll = false
            guard machine.online else { m.reset(); return }
            await m.check()
        }
        .confirmationDialog("Regenerate Persona's Files", isPresented: $confirmsFix, titleVisibility: .visible) {
            Button("Repair") { Task { await fix() } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(fixMessage)
        }
    }

    nonisolated private struct Key: Hashable, Sendable { var ref: MachineRef; var online: Bool }

    private func makeModel() -> NodeHealthModel {
        NodeHealthModel(deps: .init(
            check: { [ref, app] in
                guard let api = app.api(ref.group) else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                return try await api.nodeDoctor(machine: ref.id)
            },
            fix: { [ref, app] in
                guard let api = app.api(ref.group) else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                return try await api.fixNodeDoctor(machine: ref.id)
            }))
    }

    private var fixMessage: String {
        let files = model?.report.map { NodeHealth.fixable($0).map(\.name).joined(separator: "、") } ?? ""
        return String(localized: "Rewrites the hooks, MCP config and skill files Persona generated on this computer for the current version (\(files)), then checks again. Your own Claude Code settings aren't changed.")
    }

    // MARK: Content

    @ViewBuilder private var content: some View {
        if !machine.online {
            line(String(localized: "The machine is offline. Once it's connected you can check what this computer can do."))
        } else if let model {
            switch model.load {
            case .idle, .loading:
                HStack(spacing: Theme.Space.s) {
                    ProgressView().controlSize(.small)
                    Text("Checking this computer…").font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.m)
            case .unavailable(let why):
                unavailable(why)
            case .error(let text):
                HStack(spacing: Theme.Space.m) {
                    Text("Check failed: \(text)").font(Theme.Font.secondary).foregroundStyle(Theme.danger)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    recheckButton(model)
                }
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.m)
            case .report(let report):
                reportView(report, model: model)
            }
        } else {
            line(String(localized: "Checking this computer…"))
        }
    }

    private func line(_ text: String) -> some View {
        Text(text).font(Theme.Font.secondary).foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
    }

    @ViewBuilder private func reportView(_ report: DoctorReport, model: NodeHealthModel) -> some View {
        let features = NodeHealth.derive(report, engine: machine.engine)
        let summary = NodeHealth.summarize(features)
        let fixable = NodeHealth.fixable(report)
        let info = NodeHealth.info(report)
        HStack(alignment: .center, spacing: Theme.Space.m) {
            ThemedIcon(summary.attention == 0 ? Theme.Icon.successFilled : Theme.Icon.errorFilled)
                .font(Theme.Font.heading)
                .foregroundStyle(summary.attention == 0 ? Theme.positive : Theme.caution)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(summary.title).font(Theme.Font.bodyStrong)
                if !summary.off.isEmpty {
                    Text(summary.off).font(Theme.Font.label).foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("machine.health.summary")
            recheckButton(model)
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        if !fixable.isEmpty {
            HStack(spacing: Theme.Space.m) {
                Text("\(fixable.count) files Persona generated are out of date or missing")
                    .font(Theme.Font.label).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Button { confirmsFix = true } label: {
                    if model.fixing {
                        ProgressView().controlSize(.small)
                    } else {
                        Label("Repair", systemImage: Theme.Icon.repair).font(Theme.Font.labelStrong).lineLimit(1)
                    }
                }
                .themedButton(prominent: true)
                .disabled(model.busy)
                .accessibilityLabel(model.fixing ? String(localized: "Repairing") : String(localized: "Repair"))
                .accessibilityIdentifier("machine.health.fix")
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.bottom, Theme.Space.m)
        }
        // Everything works and nothing to repair: the summary says it, the features wait behind Show All Checks, so the
        // section is two rows instead of a screen (#4973).
        let folded = summary.attention == 0 && fixable.isEmpty && info.isEmpty && !showsAll
        if !folded {
            Divider()
            ForEach(features) { f in
                if f.id != features.first?.id { Divider().padding(.leading, Theme.Space.l) }
                HealthFeatureRow(feature: f)
            }
        }
        ForEach(info, id: \.name) { c in
            Divider().padding(.leading, Theme.Space.l)
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.info).font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(c.detail).font(Theme.Font.secondary)
                    if !c.hint.isEmpty { Text(c.hint).font(Theme.Font.label).foregroundStyle(.secondary) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .accessibilityElement(children: .combine)
        }
        Divider()
        Button {
            withAnimation(Theme.Motion.layout) { showsAll.toggle() }
        } label: {
            HStack(spacing: Theme.Space.s) {
                Text(showsAll ? String(localized: "Hide All Checks") : String(localized: "Show All Checks (\(report.checks.count))"))
                    .font(Theme.Font.secondaryMedium).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                ThemedIcon(showsAll ? Theme.Icon.collapse : Theme.Icon.expand)
                    .font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .foregroundStyle(Theme.accent)
        .accessibilityIdentifier("machine.health.all")
        if showsAll {
            ForEach(report.checks, id: \.name) { c in
                Divider().padding(.leading, Theme.Space.l)
                DoctorCheckRow(check: c)
            }
        }
    }

    private func recheckButton(_ model: NodeHealthModel) -> some View {
        Button { Task { await model.check() } } label: {
            if model.busy && !model.fixing {
                ProgressView().controlSize(.small)
            } else {
                Label("Check Again [Lark Chat View c25fb8]", systemImage: Theme.Icon.retry).font(Theme.Font.labelStrong).lineLimit(1)
            }
        }
        .themedButton()
        .disabled(model.busy)
        .accessibilityLabel(model.busy && !model.fixing ? String(localized: "Checking") : String(localized: "Check Again [Lark Chat View c25fb8]"))
        .accessibilityIdentifier("machine.health.recheck")
    }

    @ViewBuilder private func unavailable(_ why: NodeHealth.Unavailable) -> some View {
        switch why {
        case .oldNode:
            HStack(spacing: Theme.Space.m) {
                Text(NodeHealth.deployReason(machine).map { String(localized: "This computer's node is too old. Update it to run the self-check here. \($0)") } ?? String(localized: "This computer's node is too old. Update it to run the self-check here."))
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if NodeHealth.canDeploy(machine) {
                    let busy = deploying || machine.deploying == true
                    Button { Task { await deploy() } } label: {
                        if busy {
                            ProgressView().controlSize(.small)
                        } else {
                            Text("Redeploy").font(Theme.Font.labelStrong).lineLimit(1)
                        }
                    }
                    .themedButton()
                    .disabled(busy)
                    .accessibilityLabel(busy ? String(localized: "Deploying") : String(localized: "Redeploy"))
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
        case .oldHub:
            line(String(localized: "This hub version doesn't support the self-check yet. Update the hub to see it here."))
        case .forbidden:
            line(String(localized: "This device can only answer questions. The self-check needs a device with full access."))
        case .offline:
            line(String(localized: "This computer just went offline. Check again once it's connected."))
        }
    }

    // MARK: Actions

    private func fix() async {
        guard let model else { return }
        let r = await model.fix()
        if r.failed {
            Haptics.error()
            app.toast = Toast(r.text, style: .error)
        } else {
            Haptics.success()
            app.toast = Toast(r.text, style: .success)
        }
    }

    /// The same Redeploy as the machine card's (`DeployRow`): the hub installs its build and reconnects.
    private func deploy() async {
        deploying = true
        defer { deploying = false }
        do {
            try await app.api(ref.group)?.deploy(machine: ref.id)
            Haptics.tap()
            await app.refresh()
        } catch {
            if let m = error.shown { app.toast = Toast(String(localized: "Deploy failed: \(m)"), style: .error) }
        }
    }
}

/// One feature: a dot in its state's color, what it is (or why it is not ready), and its state.
private struct HealthFeatureRow: View {
    var feature: NodeHealth.Feature

    var body: some View {
        HStack(alignment: .center, spacing: Theme.Space.m) {
            Circle().fill(Self.color(feature.state)).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(feature.name).font(Theme.Font.body).lineLimit(1)
                Text(feature.state == .ready || feature.note.isEmpty ? feature.detail : feature.note)
                    .font(Theme.Font.label).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Chip(text: feature.state.label, look: Self.look(feature.state))
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("machine.health.feature.\(feature.id)")
    }

    static func color(_ s: NodeHealth.FeatureState) -> Color {
        switch s {
        case .ready: Theme.positive
        case .partial: Theme.caution
        case .broken: Theme.danger
        case .off: Color.secondary
        }
    }

    static func look(_ s: NodeHealth.FeatureState) -> Chip.Look {
        switch s {
        case .ready: .tint(Theme.positive)
        case .partial: .tint(Theme.caution.opacity(0.14), text: Theme.cautionText)
        case .broken: .tint(Theme.danger)
        case .off: .neutral
        }
    }
}

/// One check of the report: its status, its name, what was found and, when it is not fine, what to do and what differs.
private struct DoctorCheckRow: View {
    var check: DoctorCheck

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            Chip(text: check.statusLabel, look: look)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(check.name).font(Theme.Font.code).textSelection(.enabled)
                if !check.detail.isEmpty {
                    Text(check.detail).font(Theme.Font.label).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if check.status != .ok {
                    if !check.hint.isEmpty {
                        Text(check.hint).font(Theme.Font.label).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    ForEach(Array(check.items.prefix(5)), id: \.self) { item in
                        Text(item).font(Theme.Font.code).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("machine.health.check.\(check.name)")
    }

    private var look: Chip.Look {
        switch check.status {
        case .ok: .tint(Theme.positive)
        case .warn: .tint(Theme.caution.opacity(0.14), text: Theme.cautionText)
        case .fail: .tint(Theme.danger)
        }
    }
}
