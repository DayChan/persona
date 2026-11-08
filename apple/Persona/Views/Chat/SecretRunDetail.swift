import SwiftUI

/// A password run's row once opened (`ToolDetail` routes `secret_run` here, as it routes ask_user): the result read as
/// the tool's JSON (`SecretRunDetail`), from its whole text when the page carries only its start; anything else (still
/// running, a plain-text error, text that is not the tool's) shows the commands, and `ToolDetail` or the raw result
/// under them says the rest.
struct SecretRunToolDetail: View {
    var call: ToolCall
    @Environment(\.loadFullText) private var loadFullText
    @Environment(\.rowMemoScope) private var memoScope
    /// The result read from its whole text, once fetched (`SecretRunResult.Reading.whole`), with its item's id.
    @State private var whole: (id: String, result: SecretRunResult)?

    /// Results read from their whole text, by chat and item: reopening the row or scrolling back to it does not fetch
    /// them again.
    private static var wholes: [String: SecretRunResult] = [:]

    var body: some View {
        let request = SecretRunRequest(call.input)
        if let result = call.result, result.isError != true {
            switch SecretRunResult.reading(result) {
            case .parsed(let parsed):
                SecretRunDetail(request: request, result: parsed)
            case .whole(let id):
                if let read = (whole?.id == id ? whole?.result : nil) ?? Self.wholes[RowMemo.key(memoScope, id)] {
                    SecretRunDetail(request: request, result: read)
                } else {
                    raw(request, result).task(id: id) { await readWhole(id) }
                }
            case .raw:
                raw(request, result)
            }
        } else {
            SecretRunCommands(request: request)
        }
    }

    /// The commands, then the result as any tool's (a cut one with its whole text a tap away).
    private func raw(_ request: SecretRunRequest?, _ result: Item) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            SecretRunCommands(request: request)
            ResultBlock(text: result.cutText, isError: false, cut: result.fullSize.map { (result.id, $0) })
        }
    }

    private func readWhole(_ id: String) async {
        // A boundary with a fallback: a whole text that does not load or parse leaves the raw result, which offers it.
        guard let loadFullText, let text = try? await loadFullText(id), let read = SecretRunResult.parse(text) else { return }
        if Self.wholes.count >= 32 { Self.wholes.removeAll() }
        Self.wholes[RowMemo.key(memoScope, id)] = read
        whole = (id, read)
    }
}

/// A finished password run: how it went and on which computer, then every command the card listed with its exit code
/// and its output (withheld whole when it contained the password). Commands after a failure, and all of them when nothing
/// ran, say so. The card is gone by now: this is what the chat keeps of it.
struct SecretRunDetail: View {
    var request: SecretRunRequest?
    var result: SecretRunResult

    var body: some View {
        // The tool's name for the computer, else the card's (an empty one says nothing).
        let machine = [result.machine, request?.machine].compactMap { $0 }.first { !$0.isEmpty }
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.s) {
                Chip(text: result.status.label, symbol: result.status.symbol, look: result.status.look)
                if let machine { MachineLabel(name: machine) }
            }
            .accessibilityElement(children: .combine)
            if let note = result.note(machine: machine.map(MachineLabel.shown)) {
                Text(note).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            ForEach(Array(result.rows(commands: request?.commands ?? []).enumerated()), id: \.offset) { _, row in
                CommandOutcome(row: row)
            }
        }
    }

    private struct CommandOutcome: View {
        var row: SecretRunResult.Row

        var body: some View {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                    Text(verbatim: "$ " + row.command)
                        .font(Theme.Font.code)
                        .foregroundStyle(row.outcome == .notRun ? Color.secondary : Color.primary)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .textSelection(.enabled)
                    outcome
                }
                // Its last lines: the output is already its end, and a failure says why at the bottom.
                if !row.output.isEmpty { CodeBlock(text: row.output, maxLines: 10, keepsEnd: true) }
                if row.withheld {
                    Text("The output contained the password, so all of it was withheld")
                        .font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                if row.truncated {
                    Text("Only the end of the output was kept").font(Theme.Font.label).foregroundStyle(.secondary)
                }
            }
        }

        @ViewBuilder private var outcome: some View {
            switch row.outcome {
            case .exited(let code): Chip(text: String(localized: "Exit code \(code)"), look: .tint(code == 0 ? Theme.positive : Theme.danger))
            case .timedOut: Chip(text: String(localized: "Timed out"), look: .tint(Theme.danger))
            case .notRun: Chip(text: String(localized: "Did not run"))
            }
        }
    }
}

/// A password run's commands without a result to read (still running, an error, text that is not the tool's): what
/// the card asked to run and where, as a Bash row shows its command.
struct SecretRunCommands: View {
    var request: SecretRunRequest?

    var body: some View {
        if let request {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                if !request.machine.isEmpty { MachineLabel(name: request.machine) }
                CodeBlock(text: request.commands.map { "$ " + $0 }.joined(separator: "\n"), maxLines: 12)
            }
        }
    }
}

/// The computer a run is on, beside its status or above its commands. The Agent's call names it by id or name (only the
/// card's input carries `machine_name`): shown by the name the app knows it by.
private struct MachineLabel: View {
    var name: String

    var body: some View {
        Label { Text(Self.shown(name)).lineLimit(1) } icon: { ThemedIcon(Theme.Icon.machine) }
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)
    }

    /// The name the app knows the computer by, for an id or a name.
    static func shown(_ name: String) -> String { ComputerSteps.machineName(name, in: AppModel.shared.machines) }
}

extension SecretRunResult {
    /// Why nothing ran, when the status alone does not say it; `machine` is the computer's name as the app shows it.
    /// A run that failed before any command says so in general terms: the tool's `message` is written for the Agent,
    /// in English.
    func note(machine: String?) -> String? {
        switch status {
        case .ran, .declined:
            return nil
        case .failed:
            guard (results ?? []).isEmpty else { return nil }
            guard let machine, !machine.isEmpty else { return String(localized: "Couldn't run; nothing was executed") }
            return String(localized: "Couldn't run on \(machine); nothing was executed")
        case .expired:
            return String(localized: "Not filled in before the card expired")
        case .refused:
            return String(localized: "Answered somewhere the password can't be typed")
        }
    }
}

extension SecretRunResult.Status {
    var label: String {
        switch self {
        case .ran: String(localized: "Ran")
        case .failed: String(localized: "Failed")
        case .declined: String(localized: "Declined")
        case .expired: String(localized: "Expired")
        case .refused: String(localized: "Refused")
        }
    }

    var symbol: String? {
        switch self {
        case .ran: Theme.Icon.successFilled
        case .failed: Theme.Icon.errorFilled
        case .declined, .expired, .refused: nil
        }
    }

    var look: Chip.Look {
        switch self {
        case .ran: .tint(Theme.positive)
        case .failed: .tint(Theme.danger)
        case .declined, .expired, .refused: .neutral
        }
    }
}
