import Foundation

// The password card's model (`SecretRun`, `Interaction.isSecretRun`: apple/Shared/InteractionText.swift). The Agent never
// sees the password; it gets back the exit codes and the redacted output.

extension Interaction {
    /// The card's title names the computer the password goes to, so the form under it starts with why.
    var secretRunTitle: String {
        guard let machine = SecretRunRequest(toolInput)?.machine, !machine.isEmpty else {
            return String(localized: "Run With Your Password")
        }
        return String(localized: "Run on \(machine) With Your Password")
    }
}

/// What the card asks to run, from its `tool_input` (also the tool call's input in the chat).
nonisolated struct SecretRunRequest: Sendable {
    /// The computer's name as the hub knows it (`machine_name`), else the id the Agent gave (`machine`).
    var machine: String
    /// Why the password is needed, in the Agent's words.
    var purpose: String
    /// The commands verbatim, in the order they run.
    var commands: [String]

    /// What the chat's tool row names the call by: why, else where.
    var subject: String { purpose.isEmpty ? machine : purpose }

    /// Nil without any command: the card then shows its `summary`, which repeats everything verbatim.
    init?(_ input: JSONValue?) {
        let commands = (input?["commands"]?.array ?? []).compactMap(\.string)
        guard let input, !commands.isEmpty else { return nil }
        func text(_ key: String) -> String? {
            input[key]?.string.flatMap { $0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : $0 }
        }
        self.commands = commands
        machine = text("machine_name") ?? text("machine") ?? ""
        purpose = text("purpose") ?? ""
    }
}

/// How a `secret_run` call went, read from the tool's result: one JSON object for every outcome once a card was shown
/// (server/internal/assistant/secret_run.go). Errors before a card (an unknown computer, a node without the
/// capability) are plain text and do not parse.
nonisolated struct SecretRunResult: Decodable, Sendable {
    enum Status: String, Decodable, Sendable {
        /// Every command exited 0.
        case ran
        /// A command exited non-zero, timed out or could not start, or the run failed after the user approved.
        case failed
        /// The user tapped Decline.
        case declined
        /// Nobody filled the card in before it expired.
        case expired
        /// Answered where the password cannot be typed (the assistant, a channel), or the password never arrived.
        case refused
    }

    /// One command that ran (`proto.SecretRunResult`).
    struct CommandResult: Decodable, Sendable {
        var command: String
        var exitCode: Int
        var timedOut: Bool
        /// stdout and stderr together, at most its last few thousand characters; never the password (`outputWithheld`).
        var output: String
        /// Only the end of the output was kept.
        var truncated: Bool
        /// The output contained the password, so none of it came back (`output` is empty): where a masked password
        /// sits could give a weak one away. Older results have no such field.
        var outputWithheld: Bool?

        enum CodingKeys: String, CodingKey {
            case command, output, truncated
            case exitCode = "exit_code", timedOut = "timed_out", outputWithheld = "output_withheld"
        }
    }

    /// How one command of the card went.
    enum Outcome: Equatable, Sendable {
        case exited(Int)
        case timedOut
        /// Never started: a command before it failed, or nothing ran at all.
        case notRun
    }

    struct Row: Sendable {
        var command: String
        var outcome: Outcome
        /// Without the newlines it starts and ends with: a trailing one drew an empty last line in the block.
        var output: String
        var truncated: Bool
        /// The output contained the password and was withheld whole (`CommandResult.outputWithheld`).
        var withheld: Bool
    }

    var status: Status
    /// The computer's name as the tool reported it.
    var machine: String?
    /// Only for `ran` and `failed` (absent otherwise); the commands after the first failure are left out.
    var results: [CommandResult]?

    /// The result, or nil when `text` is not the tool's JSON (a plain-text error, a status this app does not know):
    /// the chat then shows the text as it shows any tool's.
    static func parse(_ text: String) -> SecretRunResult? {
        memo(text) { text in
            // A boundary with a fallback: anything that does not decode is shown raw.
            try? JSONDecoder().decode(SecretRunResult.self, from: Data(text.utf8))
        }
    }

    private static let memo = TextMemo<SecretRunResult?>(limit: 64)

    /// How the chat reads a finished call's result item.
    enum Reading {
        case parsed(SecretRunResult)
        /// The page carries only the result's start (`Item.fullSize`), which is never whole JSON: read item `id`'s
        /// whole text first.
        case whole(id: String)
        /// Not the tool's JSON: shown as it is.
        case raw
    }

    static func reading(_ result: Item) -> Reading {
        if result.fullSize != nil { return .whole(id: result.id) }
        return parse(result.cutText).map(Reading.parsed) ?? .raw
    }

    /// Every command the card listed, in order: the ones that ran with how they ended, then the rest as not run. A
    /// result beyond the card's list (no input to read) is still shown.
    func rows(commands: [String]) -> [Row] {
        var rows = (results ?? []).map { r in
            let withheld = r.outputWithheld ?? false
            return Row(command: r.command, outcome: r.timedOut ? .timedOut : .exited(r.exitCode),
                       output: withheld ? "" : r.output.trimmingCharacters(in: .newlines), truncated: r.truncated && !withheld,
                       withheld: withheld)
        }
        if commands.count > rows.count {
            rows += commands[rows.count...].map { Row(command: $0, outcome: .notRun, output: "", truncated: false, withheld: false) }
        }
        return rows
    }
}
