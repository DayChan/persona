import SwiftUI

/// The checklist after pairing (docs/assistant/onboarding.md 4.5「配对之后：一张清单」, #4195): four rows read from the main
/// conversation's readiness — computer, Claude Code, model, try a message — each with its state and the button of its
/// step. It never blocks: Later puts it off (the hub remembers, `dismissed`), and Settings says how many steps are left.
@MainActor @Observable
final class OnboardingChecklistModel {
    /// One row: its step (nil while the hub has not said), what to call it, and how it looks.
    struct Row: Identifiable, Equatable {
        enum Look: Equatable { case done, todo, problem, checking, unknown }
        var id: String
        var name: String
        var look: Look
        var title: String?
        var detail: String?
        /// The step's own button and a second, quieter one; nil: none, or an id this app does not know.
        var action: String?
        var alt: String?
        var launcher: String?
        var machine: String?
    }

    /// The step ids in their order, and their names.
    static let steps: [(id: String, name: String)] = [
        ("computer", String(localized: "Computers")),
        ("claude", "Claude Code"),
        ("model", String(localized: "Model [Assistant View c98e11]")),
        ("first_message", String(localized: "Try a message")),
    ]

    /// The rows for a readiness. A state this app does not know looks like a step still to do (docs/assistant/onboarding.md 5).
    static func rows(_ r: AssistantReadiness?) -> [Row] {
        steps.map { s in
            guard let step = r?.step(s.id) else { return Row(id: s.id, name: s.name, look: .unknown) }
            let look: Row.Look = switch step.state {
            case "ok", "skipped": .done
            case "checking": .checking
            case "unknown": .unknown
            case "problem": .problem
            default: .todo
            }
            let known = { (id: String?) in AssistantReadiness.knows(action: id) ? id : nil }
            return Row(id: s.id, name: s.name, look: look, title: step.title, detail: step.detail,
                       action: look == .done ? nil : known(step.action?.id),
                       alt: look == .done ? nil : known(step.altActions?.first?.id),
                       launcher: step.launcher, machine: step.machine)
        }
    }

    /// Steps left: computer / claude / model not done yet (trying a message does not count, 5.1).
    static func left(_ r: AssistantReadiness?) -> Int { r?.stepsLeft ?? 0 }

    /// Every row done: the checklist can close with Get started.
    static func allDone(_ r: AssistantReadiness?) -> Bool {
        guard let r else { return false }
        return rows(r).allSatisfy { $0.look == .done }
    }
}

/// The checklist page: after pairing in place of the app (`AppModel.checklistPending`), or from Settings' steps-left row.
struct OnboardingChecklistView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    /// Opened from Settings (a sheet): Done closes it; after pairing, Later also tells the hub.
    var fromSettings = false
    @State private var route: ReadinessRoute?

    private var store: AssistantReadinessModel { model.readiness }
    private var readiness: AssistantReadiness? { store.current }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        Text("A few steps to go").font(Theme.Font.display)
                        Text("Persona works on one of your computers, with a model. Each step below ticks itself once it's done; no need to refresh.")
                            .font(Theme.Font.secondary).lineHeight(.secondary).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.top, Theme.Space.l)
                    VStack(spacing: Theme.Space.s) {
                        ForEach(OnboardingChecklistModel.rows(readiness)) { row in
                            ChecklistRow(row: row, busy: row.id == "first_message" && store.probing, probe: row.id == "first_message" ? store.probe : nil) { action in
                                run(action, row: row)
                            }
                        }
                    }
                }
                .padding(.horizontal, Theme.Space.page)
                .padding(.bottom, Theme.Space.xl)
                .frame(maxWidth: ChatView.readableWidth, alignment: .leading)
                .frame(maxWidth: .infinity)
            }
            .themedGroupedPage()
            .safeAreaInset(edge: .bottom, spacing: 0) { footer }
            .toolbar {
                if fromSettings {
                    ToolbarItem(placement: .confirmationAction) { Button { dismiss() } label: { Text("Done [Extension System Presentation c0b3fb]").lineLimit(1) } }
                }
            }
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
        }
        .modifier(ReadinessRefresh())
        .readinessSheets($route)
        .accessibilityIdentifier("onboarding-checklist")
    }

    private var footer: some View {
        VStack(spacing: Theme.Space.s) {
            if OnboardingChecklistModel.allDone(readiness) {
                Button { close(dismissing: false) } label: {
                    Text("Get started").font(Theme.Font.heading).lineLimit(1).scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.well)
                }
                .themedButton(prominent: true)
                .accessibilityIdentifier("checklist.done")
            } else if !fromSettings {
                Button { close(dismissing: true) } label: {
                    Text("Later [Readiness Card 9d2055]").font(Theme.Font.heading).lineLimit(1).scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.well)
                }
                .themedButton()
                .accessibilityIdentifier("checklist.later")
                Text("You can come back here from the top of Settings.").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).minimumScaleFactor(0.8)
            }
        }
        .padding(.horizontal, Theme.Space.xl)
        .padding(.vertical, Theme.Space.m)
        .frame(maxWidth: ChatView.readableWidth)
        .frame(maxWidth: .infinity)
        .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
    }

    private func close(dismissing: Bool) {
        if dismissing { Task { await store.dismiss(api: model.api) } }
        withAnimation(Theme.Motion.standard) { model.checklistPending = false }
        if fromSettings { dismiss() }
    }

    private func run(_ action: String, row: OnboardingChecklistModel.Row) {
        if action == "test_assistant" {
            Task { await store.refresh(api: model.api, probe: true) }
            return
        }
        let machine = row.machine.flatMap { id in model.machines.first { $0.id == id }?.name } ?? readiness?.machine?.name
        route = ReadinessRoute.route(for: action, launcher: row.launcher, machine: machine, model: model)
    }
}

/// One step of the checklist: its mark (tick, cross, spinner, question mark), name and the hub's words, its buttons.
private struct ChecklistRow: View {
    var row: OnboardingChecklistModel.Row
    var busy: Bool
    var probe: AssistantReadiness.Probe?
    var run: (String) -> Void

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            mark.scaledFrame(width: Theme.Size.wellS, height: Theme.Size.wellS)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(row.name).font(Theme.Font.heading)
                if row.look != .done, let title = row.title, title != row.name {
                    Text(title).font(Theme.Font.secondaryStrong).fixedSize(horizontal: false, vertical: true)
                }
                if row.look != .done, let detail = row.detail, !detail.isEmpty {
                    Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                if let probe {
                    Text(probe.ok == true ? String(localized: "Tried it; it answered: \(probe.answer ?? "")") : String(localized: "Tried a message, it didn't work: \(probe.error ?? "")"))
                        .font(Theme.Font.secondary).foregroundStyle(probe.ok == true ? Theme.positive : Theme.danger)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if row.action != nil || row.alt != nil {
                    HStack(spacing: Theme.Space.m) {
                        if let a = row.action, let t = ReadinessRoute.title(a) {
                            Button { Haptics.tap(); run(a) } label: { Text(t).font(Theme.Font.secondaryStrong).lineLimit(1).minimumScaleFactor(0.8) }
                                .themedButton(prominent: true)
                                .disabled(busy)
                        }
                        if let a = row.alt, let t = ReadinessRoute.title(a) {
                            Button { run(a) } label: { Text(t).font(Theme.Font.secondaryStrong).lineLimit(1).minimumScaleFactor(0.8) }
                                .buttonStyle(.borderless)
                                .foregroundStyle(Theme.accent)
                        }
                    }
                    .padding(.top, Theme.Space.xs)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(Theme.Space.m)
        .card(padding: 0)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("checklist.\(row.id)")
    }

    @ViewBuilder private var mark: some View {
        if busy || row.look == .checking {
            ProgressView().controlSize(.small)
        } else {
            switch row.look {
            case .done: Image(systemName: Theme.Icon.successFilled).font(Theme.Font.title).foregroundStyle(Theme.positive)
            case .problem: Image(systemName: Theme.Icon.errorFilled).font(Theme.Font.title).foregroundStyle(Theme.danger)
            case .todo: Image(systemName: Theme.Icon.unchecked).font(Theme.Font.title).foregroundStyle(Theme.caution)
            case .unknown, .checking: Image(systemName: Theme.Icon.help).font(Theme.Font.title).foregroundStyle(.secondary)
            }
        }
    }
}

/// Settings' top row while the main conversation still needs something: N steps left, back to the checklist.
struct ChecklistSettingsRow: View {
    @Environment(AppModel.self) private var model
    @State private var open = false

    var body: some View {
        let n = OnboardingChecklistModel.left(model.readiness.current)
        if n > 0 {
            Section {
                Button { open = true } label: {
                    HStack(spacing: Theme.Space.m) {
                        ThemedIcon(Theme.Icon.todo).font(Theme.Font.heading).foregroundStyle(Theme.caution)
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text("\(n) steps to go").font(Theme.Font.heading).foregroundStyle(Color.primary).lineLimit(1)
                            Text("Connect a computer and a model so Persona can answer").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                        }
                        Spacer(minLength: 0)
                        ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("settings.checklist")
            }
            .sheet(isPresented: $open) { OnboardingChecklistView(fromSettings: true) }
        }
    }
}
