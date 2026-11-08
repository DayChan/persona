import SwiftUI

extension EnvironmentValues {
    /// The session whose workflow runs the chat's Workflow calls draw (`WorkflowCallCard`): the chat's own.
    @Entry var workflowChat: SessionKey? = nil
    /// The chat's actions, for a card to open one of a workflow's agents (`ChatActions.openWorkflowAgent`): the page's one
    /// object, which the environment can tell is the same on every render (a closure it cannot).
    @Entry var chatActions: ChatActions? = nil
}

/// The run a Workflow call launched, in the chat in that call's place (#3436, the web's `WorkflowCallCard`): its head
/// (name, status, 「169/185+」, time), where it is now or what it was for, and opened, its progress bar and phases.
/// Open while it runs, folded to its head once it has ended; what the reader chose stays (`OpenTools`). It reads its
/// run itself, by the call's id (`AppModel.workflow(_:call:)`): the step it is drawn in (`WorkStepView`) is `Equatable`
/// on its item and is not drawn again by the hub's pushes, so a run handed in from there would never move. The hub
/// listing no such run (an older hub, a run over a day old, one not read yet): the call's row as it always was.
struct WorkflowCallCard: View {
    var call: ToolCall
    @Environment(AppModel.self) private var model
    @Environment(\.workflowChat) private var chat
    @Environment(\.chatActions) private var actions
    @Environment(\.openTools) private var openTools
    @Environment(\.chatPin) private var pin
    @State private var openHere: Bool?
    @State private var unfold = Unfolding()

    /// A Workflow call (Claude Code's Workflow tool): drawn as its run's card while the hub lists the run.
    nonisolated static func isWorkflow(_ call: ToolCall) -> Bool { ToolPresentation.baseName(call.name) == "Workflow" }

    /// A Workflow call whose run the hub lists (`listed`: their calls' ids) is its run's card: never folded into a run of
    /// chips (BeautifulUI), and kept in view under its folded work line, running or ended (`ChatLines`).
    nonisolated static func staysOut(_ call: ToolCall, listed: Set<String>) -> Bool {
        isWorkflow(call) && call.use.toolUseID.map(listed.contains) == true
    }

    /// After a fold holding a run's call failed to come, the next try waits this long, doubling up to `foldRetryMax`
    /// (the web's WORKFLOW_FOLD_RETRY_MS): a chat opened while the hub restarts or the phone is offline gets its card
    /// once the hub answers, without the reader leaving and coming back.
    nonisolated static let foldRetry: Double = 5
    nonisolated static let foldRetryMax: Double = 60

    /// Fetches the folds that hold listed runs' calls (`WorkflowRun.foldsHoldingCalls`), and those that did not come
    /// again after a wait, doubled each round, until all have come or the task ends (the chat closed, or the folds to
    /// fetch changed and a new task took over). `fetch` says whether a fold came.
    static func fetchFolds(_ folds: [Item], fetch: (Item) async -> Bool,
                           sleep: (Double) async -> Void = { try? await Task.sleep(for: .seconds($0)) }) async {
        var left = folds
        var wait = foldRetry
        while !left.isEmpty {
            var missed: [Item] = []
            for f in left {
                if Task.isCancelled { return }
                if await !fetch(f) { missed.append(f) }
            }
            left = missed
            if left.isEmpty || Task.isCancelled { return }
            await sleep(wait)
            wait = min(wait * 2, foldRetryMax)
        }
    }

    private var key: String { "workflow-" + call.id }

    private var run: WorkflowRun? {
        guard let chat, let id = call.use.toolUseID else { return nil }
        return model.workflow(chat, call: id)
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("WorkflowCallCard")
        #endif
        if let run {
            card(run)
        } else {
            ToolCallRow(call: call, autoExpand: false, grouped: true)
        }
    }

    private func isOpen(_ run: WorkflowRun) -> Bool { (openTools?.isOpen(key) ?? openHere) ?? run.isRunning }

    private func card(_ run: WorkflowRun) -> some View {
        let open = isOpen(run)
        return VStack(alignment: .leading, spacing: 0) {
            Button {
                Haptics.tap()
                toggleInPlace(pin, key, animation: Theme.Motion.state, unfold: $unfold, closing: open) {
                    if let openTools { openTools.set(key, !open) } else { openHere = !open }
                }
            } label: { head(run, open: open) }
            .buttonStyle(.plain)
            .chatPinned(key)
            .accessibilityLabel(WorkflowLook.spoken(run))
            .accessibilityHint(open ? String(localized: "Collapses the details") : String(localized: "Shows the details"))
            .accessibilityIdentifier("workflow.call")
            if open {
                WorkflowRunBody(run: run, open: { actions?.openWorkflowAgent($0) }, scope: call.id)
                    // The phases' marks under the name (their rows have their own inset).
                    .padding(.leading, WorkCardSlice.inset + WorkflowMark.textLeading - Theme.Space.s)
                    .padding(.trailing, WorkCardSlice.inset)
                    .padding(.bottom, Theme.Space.m)
                    .unfolding(unfold)
                    .transition(.opacity)
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("workflow.card")
    }

    private func head(_ run: WorkflowRun, open: Bool) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.s) {
                WorkflowHead(run: run, onRail: WorkCardSlice.timeline)
                ThemedIcon(Theme.Icon.disclosure)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(open ? 90 : 0))
            }
            WorkflowSubline(run: run, whole: open)
                .padding(.leading, WorkflowMark.textLeading)
        }
        .padding(.vertical, WorkCardSlice.timeline ? Theme.Space.s : Theme.Space.m)
        .padding(.horizontal, WorkCardSlice.inset)
        .contentShape(.rect)
    }
}
