import Foundation
import Testing
@testable import Persona

/// The password card (secret_run): what the card shows from its input, and how the chat's tool row reads the tool's
/// JSON result back, command by command.
@MainActor
struct SecretRunTests {
    private let commands = ["sudo -S -k -p '' cp backup.plist /Library/LaunchDaemons/",
                            "sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/backup.plist",
                            "launchctl print system/backup"]

    private var input: JSONValue {
        .object(["machine": .string("studio"), "machine_name": .string("Mac Studio"), "purpose": .string("Install the backup job"),
                 "commands": .array(commands.map { .string($0) }), "delivery": .string("stdin")])
    }

    private func call(_ name: String = "mcp__persona__secret_run", result: String?) -> ToolCall {
        let use = Item(id: "1#0", seq: 1, ts: Date(timeIntervalSince1970: 1), role: "assistant", kind: .toolUse, text: nil,
                       toolUseID: "t1", toolName: name, toolInput: input, isError: nil, image: nil)
        let res = result.map {
            Item(id: "2#0", seq: 2, ts: Date(timeIntervalSince1970: 2), role: "user", kind: .toolResult, text: $0,
                 toolUseID: "t1", toolName: nil, toolInput: nil, isError: nil, image: nil)
        }
        return ToolCall(use: use, result: res)
    }

    @Test func theCardShowsTheComputerPurposeAndCommands() throws {
        let request = try #require(SecretRunRequest(input))
        #expect(request.machine == "Mac Studio")
        #expect(request.purpose == "Install the backup job")
        #expect(request.commands == commands)

        // No display name: the id the Agent gave. No commands: nothing to draw, the card falls back to its summary.
        let byID = try #require(SecretRunRequest(.object(["machine": .string("studio"), "machine_name": .string(" "),
                                                          "commands": .array([.string("true")])])))
        #expect(byID.machine == "studio")
        #expect(SecretRunRequest(.object(["machine": .string("studio"), "commands": .array([])])) == nil)
        #expect(SecretRunRequest(nil) == nil)
    }

    @Test func theCardIsAPasswordCardThatCannotBeQuickAllowed() throws {
        let raw = Data(#"""
        {"id":"ix_1","machine_id":"server","session_id":"s1","kind":"permission","tool_name":"secret_run",
         "tool_input":{"machine":"studio","commands":["true"]},
         "fields":[{"key":"password","label":"Password","secret":true}]}
        """#.utf8)
        let ix = try JSONDecoder().decode(Interaction.self, from: raw)
        #expect(ix.isSecretRun)
        #expect(ix.title == String(localized: "Run on \("studio") With Your Password")) // no display name: the id
        var named = ix
        named.toolInput = .object(["machine": .string("studio"), "machine_name": .string("Mac Studio"), "commands": .array([.string("true")])])
        #expect(named.title == String(localized: "Run on \("Mac Studio") With Your Password"))
        named.toolInput = nil
        #expect(named.title == String(localized: "Run With Your Password"))
        #expect(ApproveFirstIntent.resolution(for: ix) == nil) // Siri and the Action button never allow it
        // Without its field (a malformed card) it still needs filling in: no quick Allow sends it without a password.
        var bare = ix
        bare.fields = nil
        #expect(bare.requiresInput)
        #expect(ApproveFirstIntent.resolution(for: bare) == nil)
        var bash = ix
        bash.toolName = "Bash"
        #expect(!bash.isSecretRun)
    }

    @Test func theRowIsNamedForAPasswordRunWithItsPurpose() {
        let c = call(result: nil)
        #expect(ToolPresentation.verb(c.name) == String(localized: "Run With Password"))
        #expect(ToolPresentation.verb("mcp__\(LegacyNames.product)__secret_run") == ToolPresentation.verb(c.name))
        #expect(ToolPresentation.subject(c) == "Install the backup job")
        #expect(ToolPresentation.symbol(c.name) == Theme.Icon.credentials)
    }

    @Test func aRunWhereEveryCommandExitedZero() throws {
        let text = #"""
        {"status":"ran","machine":"Mac Studio","message":"Ran 3 commands.","results":[
         {"command":"sudo -S -k -p '' cp backup.plist /Library/LaunchDaemons/","exit_code":0,"output":"","timed_out":false,"truncated":false},
         {"command":"sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/backup.plist","exit_code":0,"output":"","timed_out":false,"truncated":false},
         {"command":"launchctl print system/backup","exit_code":0,"output":"…\tstate = running\n","timed_out":false,"truncated":true}]}
        """#
        let result = try #require(SecretRunResult.parse(text))
        #expect(result.status == .ran)
        #expect(result.machine == "Mac Studio")
        let rows = result.rows(commands: commands)
        #expect(rows.map(\.outcome) == [.exited(0), .exited(0), .exited(0)])
        #expect(rows.map(\.command) == commands)
        #expect(rows[2].output == "…\tstate = running") // the trailing newline would draw an empty last line
        #expect(rows.map(\.truncated) == [false, false, true])
    }

    @Test func commandsAfterAFailureDidNotRun() throws {
        let failed = try #require(SecretRunResult.parse(#"""
        {"status":"failed","machine":"Mac Studio","results":[
         {"command":"sudo -S -k -p '' cp backup.plist /Library/LaunchDaemons/","exit_code":0,"timed_out":false,"output":"","truncated":false},
         {"command":"sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/backup.plist","exit_code":5,"timed_out":false,
          "output":"Bootstrap failed: 5: Input/output error\n","truncated":false}]}
        """#))
        #expect(failed.status == .failed)
        let rows = failed.rows(commands: commands)
        #expect(rows.map(\.outcome) == [.exited(0), .exited(5), .notRun])
        #expect(rows[1].output == "Bootstrap failed: 5: Input/output error")
        #expect(rows[2].command == commands[2])

        let timedOut = try #require(SecretRunResult.parse(#"""
        {"status":"failed","results":[{"command":"sleep 999","exit_code":-1,"output":"","timed_out":true,"truncated":false}]}
        """#))
        #expect(timedOut.rows(commands: ["sleep 999", "true"]).map(\.outcome) == [.timedOut, .notRun])

        // Approved, but the run itself failed: no command results (the tool leaves `results` out).
        let noRun = try #require(SecretRunResult.parse(#"{"status":"failed","machine":"Mac Studio","message":"The computer went offline."}"#))
        #expect(noRun.rows(commands: commands).allSatisfy { $0.outcome == .notRun })
    }

    /// A run that failed before any command says so in the user's language, naming the computer when it can; the
    /// tool's English message for the Agent never shows. A failure with command results needs no note: the rows say it.
    @Test func aRunThatFailedBeforeAnyCommandSaysSoInTheUsersLanguage() throws {
        let noRun = try #require(SecretRunResult.parse(#"""
        {"status":"failed","machine":"Mac Studio","message":"node studio: dial tcp: connection refused"}
        """#))
        let note = try #require(noRun.note(machine: "Mac Studio"))
        #expect(note == String(localized: "Couldn't run on \("Mac Studio"); nothing was executed"))
        #expect(!note.contains("dial tcp"))
        #expect(noRun.note(machine: nil) == String(localized: "Couldn't run; nothing was executed"))
        #expect(noRun.note(machine: "") == noRun.note(machine: nil))

        let failed = try #require(SecretRunResult.parse(#"""
        {"status":"failed","machine":"Mac Studio","message":"1 command failed.","results":[
         {"command":"false","exit_code":1,"timed_out":false,"output":"","truncated":false}]}
        """#))
        #expect(failed.note(machine: "Mac Studio") == nil)
        let ran = try #require(SecretRunResult.parse(#"{"status":"ran","machine":"Mac Studio","results":[]}"#))
        #expect(ran.note(machine: "Mac Studio") == nil)
        let expired = try #require(SecretRunResult.parse(#"{"status":"expired","message":"The card expired."}"#))
        #expect(expired.note(machine: nil) == String(localized: "Not filled in before the card expired"))
    }

    @Test func nothingRanWhenDeclinedExpiredOrRefused() throws {
        var labels: Set<String> = []
        for status in ["declined", "expired", "refused"] {
            let result = try #require(SecretRunResult.parse(#"{"status":"\#(status)","machine":"Mac Studio","message":"Nothing ran."}"#))
            #expect(result.status.rawValue == status)
            #expect(result.rows(commands: commands).map(\.outcome) == [.notRun, .notRun, .notRun])
            labels.insert(result.status.label)
        }
        labels.formUnion([SecretRunResult.Status.ran.label, SecretRunResult.Status.failed.label])
        #expect(labels.count == 5) // every outcome reads differently
    }

    /// An output that contained the password comes back withheld whole: the row says so and shows none of it, even
    /// text the field says should not be there. A result from before the field reads as not withheld.
    @Test func anOutputThatHeldThePasswordIsWithheldWhole() throws {
        let result = try #require(SecretRunResult.parse(#"""
        {"status":"ran","machine":"Mac Studio","message":"Ran 2 commands.","results":[
         {"command":"sudo -S -k -p '' cat /etc/backup.conf","exit_code":0,"timed_out":false,"output":"not for the screen",
          "truncated":true,"output_withheld":true},
         {"command":"launchctl print system/backup","exit_code":0,"timed_out":false,"output":"state = running\n","truncated":false}]}
        """#))
        let rows = result.rows(commands: [])
        #expect(rows.map(\.withheld) == [true, false])
        #expect(rows[0].output.isEmpty)
        #expect(!rows[0].truncated) // nothing is shown, so nothing was cut either
        #expect(rows[1].output == "state = running")
    }

    /// An error before any card (plain text) and a status this app does not know are shown raw, as any tool's output.
    @Test func anythingElseStaysRaw() {
        #expect(SecretRunResult.parse(#"machine "nas" has no secret_run capability; update its node"#) == nil)
        #expect(SecretRunResult.parse(#"{"status":"queued"}"#) == nil)
        guard case .raw = SecretRunResult.reading(resultItem(#"machine "nas" is not reachable"#)) else {
            Issue.record("a plain-text error should read raw"); return
        }
    }

    /// A result longer than the page carries comes cut (`full_size`, the hub's cut line at the end): its start is never
    /// whole JSON, so the row reads the item's whole text instead of falling back to the raw start.
    @Test func aResultThePageCutIsReadFromItsWholeText() throws {
        let long = String(repeating: "launchctl: service output line\n", count: 300) // ~9 KB, past the page's 4 KB
        let whole = #"{"status":"failed","machine":"Mac Studio","message":"…","results":["#
            + #"{"command":"sudo -S -k -p '' launchctl bootstrap system /Library/LaunchDaemons/backup.plist","exit_code":5,"timed_out":false,"output":"#
            + String(decoding: try JSONEncoder().encode(long), as: UTF8.self) + #","truncated":true}]}"#
        let start = String(decoding: whole.utf8.prefix(4096), as: UTF8.self) + "\n…（已截断，点开看全文，共 \(whole.utf8.count) 字节）" // i18n-ignore: the hub's cut line
        let cut = resultItem(start, fullSize: whole.utf8.count)
        #expect(SecretRunResult.parse(cut.cutText) == nil)
        guard case .whole(let id) = SecretRunResult.reading(cut) else { Issue.record("a cut result should ask for its whole text"); return }
        #expect(id == cut.id)

        let read = try #require(SecretRunResult.parse(whole))
        let rows = read.rows(commands: [commands[1], commands[2]])
        #expect(rows.map(\.outcome) == [.exited(5), .notRun])
        #expect(rows[0].truncated)
        #expect(rows[0].output.hasPrefix("launchctl: service output line"))

        guard case .parsed = SecretRunResult.reading(resultItem(whole)) else { Issue.record("an uncut result parses as it is"); return }
    }

    private func resultItem(_ text: String, fullSize: Int? = nil) -> Item {
        Item(id: "2#0", seq: 2, ts: Date(timeIntervalSince1970: 2), role: "user", kind: .toolResult, text: text,
             toolUseID: "t1", toolName: nil, toolInput: nil, isError: nil, fullSize: fullSize, image: nil)
    }
}
