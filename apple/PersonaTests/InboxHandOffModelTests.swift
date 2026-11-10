import Foundation
import Testing
@testable import Persona

/// Hand to AI (docs/assistant/inbox.md "Item actions", #1194): Persona's main chat is the default and goes through the
/// item's hand-off, Also Mark as Done follows the target until the user sets it, a retry of the same hand-off keeps its
/// client id and text only while it may have gone, and an older hub gets the text as a plain message.
@MainActor
struct InboxHandOffModelTests {
    private final class Calls {
        var handOffs: [(id: String, text: String, clientID: UUID)] = []
        var opened: [(key: SessionKey, text: String, clientID: UUID)] = []
        var asked: [String] = []
        var started: [String] = []
        var agents: [(key: SessionKey, text: String)] = []
        var agentAccepted = true
        var agentFiled = false
        var agentError: String?
        var agentDelay: Duration?
        /// What `open()` went to.
        var went: [(target: InboxHandOffModel.Target, key: SessionKey)] = []
        var done = 0
        /// What the hand-off throws first, in order; then it answers.
        var failures: [Error] = []
        /// What the plain message throws first, in order; then it goes.
        var askFailures: [Error] = []
        /// The item's preview from the next load on, as when a new message came in.
        var preview: String?
        /// What loading the item throws first, in order; then it loads.
        var loadFailures: [Error] = []
    }

    private static let main = SessionKey(machineID: "server", sessionID: "main-1")
    private static let fresh = SessionKey(machineID: "server", sessionID: "new-1")
    private static let look = String(localized: "It may have reached the computer already. Check the chat before sending it again.")
    /// A failed retry of a hand-off already in doubt, which the same ask never hands over under a new id.
    private static let lookAgain = look + "\n" + String(localized: "To send it anyway, change the instructions or open Hand to AI again.")
    private static let item = """
    {"id":"lark:f1:11","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"Sample Contact","preview":"合入了吗",
     "count":1,"lark":{"feed_id":"f1","is_group":false}}
    """

    private func model(_ calls: Calls, hub: APIClient? = nil) -> InboxHandOffModel {
        InboxHandOffModel(itemID: "lark:f1:11", deps: .init(
            load: { _ in
                try Task.checkCancellation() // as URLSession does
                if !calls.loadFailures.isEmpty { throw calls.loadFailures.removeFirst() }
                var it = try JSON.decoder.decode(InboxItem.self, from: Data(Self.item.utf8))
                if let p = calls.preview { it.preview = p }
                return it
            },
            handOff: { id, text, clientID in
                calls.handOffs.append((id, text, clientID))
                if let hub { return try await hub.inboxHandoff(id, text: text, clientID: clientID).session }
                if !calls.failures.isEmpty { throw calls.failures.removeFirst() }
                return Self.main
            },
            showMain: { calls.opened.append(($0, $1, $2)) },
            plainToMain: {
                calls.asked.append($0)
                if !calls.askFailures.isEmpty { throw calls.askFailures.removeFirst() }
                return Self.main
            },
            startNew: {
                calls.started.append($0)
                return Self.fresh
            },
            sendToAgent: { key, text, _ in
                calls.agents.append((key, text))
                if let delay = calls.agentDelay { try? await Task.sleep(for: delay) }
                return calls.agentAccepted ? InboxHandOffModel.AgentSendResult(delivered: true, error: nil, filed: calls.agentFiled) : .failed(calls.agentError)
            },
            open: { calls.went.append(($0, $1)) },
            markDone: { calls.done += 1 }))
    }

    @Test func theMainChatIsTheDefaultAndLeavesTheItemOpen() async throws {
        let calls = Calls(), m = model(calls)
        #expect(m.target == .main)
        #expect(!m.markDone)
        let went = await m.send(ask: "帮我查一下")
        #expect(went)
        let h = try #require(calls.handOffs.first)
        #expect(calls.handOffs.count == 1 && h.id == "lark:f1:11")
        let parsed = ContextRef.parse(h.text)
        #expect(parsed.refs.count == 1 && parsed.rest == "帮我查一下")
        // The conversation opens on the message as sent, under the id its transcript copy will carry.
        let o = try #require(calls.opened.first)
        #expect(o.key == Self.main && o.text == h.text && o.clientID == h.clientID)
        #expect(calls.done == 0 && calls.started.isEmpty && calls.asked.isEmpty && calls.agents.isEmpty)
    }

    @Test func markAsDoneFollowsTheTargetUntilTheUserSetsIt() async {
        let m = model(Calls())
        m.target = .newChat
        #expect(m.markDone)
        m.target = .agent(SessionKey(machineID: "m", sessionID: "a"))
        #expect(m.markDone)
        m.target = .main
        #expect(!m.markDone)
        m.setMarkDone(true)
        m.target = .newChat
        m.target = .main
        #expect(m.markDone) // the user's choice stays

        let calls = Calls(), checked = model(calls)
        checked.setMarkDone(true)
        _ = await checked.send(ask: "a")
        #expect(calls.done == 1 && calls.opened.count == 1)
    }

    @Test func aNewChatAndAnAgentGetTheTextAndFileTheItem() async throws {
        let calls = Calls(), m = model(calls)
        m.target = .newChat
        let started = await m.send(ask: "a")
        #expect(started && calls.started.count == 1 && calls.done == 1 && calls.handOffs.isEmpty)
        let agent = SessionKey(machineID: "m", sessionID: "a")
        m.target = .agent(agent)
        let sent = await m.send(ask: "b")
        let a = try #require(calls.agents.first)
        #expect(sent && a.key == agent && ContextRef.parse(a.text).rest == "b" && calls.done == 2 && calls.handOffs.isEmpty)
    }

    @Test func handOffListsOnlyAgentsThatCanTakeAMessage() throws {
        let good = try session("good")
        let viewOnly = try session("view-only", viewOnly: true)
        let offline = try session("offline", state: .offline)
        let offlineMachine = try session("machine-offline", machine: "offline")
        let assistant = try session("assistant", assistant: true)
        let got = InboxHandOffModel.agents([good, viewOnly, offline, offlineMachine, assistant], limit: 8) { $0.id == "m" }
        #expect(Set(got.map(\.sessionID)) == ["good"])
        // A source's remembered Agent (#2121) passes the same test, so one that can't take it now counts as gone.
        let takes = [good, viewOnly, offline, offlineMachine, assistant].map { s in InboxHandOffModel.takes(s) { $0.id == "m" } }
        #expect(takes == [true, false, false, false, false])
    }

    @Test func aRefusedAgentSendKeepsTheItemAndSheet() async {
        let calls = Calls(), m = model(calls)
        calls.agentAccepted = false
        calls.agentError = "Finish the draft on the computer first"
        m.target = .agent(SessionKey(machineID: "m", sessionID: "a"))
        let sent = await m.send(ask: "a")
        #expect(!sent)
        #expect(calls.agents.count == 1 && calls.done == 0)
        #expect(m.sent == nil && m.error == "Finish the draft on the computer first")
    }

    @Test func anAgentSendFilesOnlyAfterItsDelayedDelivery() async throws {
        let calls = Calls(), m = model(calls)
        calls.agentDelay = .milliseconds(100)
        m.target = .agent(SessionKey(machineID: "m", sessionID: "a"))
        let sending = Task { await m.send(ask: "a") }
        try await Task.sleep(for: .milliseconds(20))
        #expect(m.sending && calls.done == 0)
        #expect(await sending.value)
        #expect(calls.done == 1 && m.sent?.key == SessionKey(machineID: "m", sessionID: "a"))
    }

    @Test func anAgentSendAlreadyFiledByTheOutboxDoesNotFileAgain() async {
        let calls = Calls(), m = model(calls)
        calls.agentFiled = true
        m.target = .agent(SessionKey(machineID: "m", sessionID: "a"))
        #expect(await m.send(ask: "a"))
        #expect(calls.done == 0)
    }

    private func session(_ id: String, assistant: Bool = false, state: AgentState = .working, viewOnly: Bool = false, machine: String = "m") throws -> Session {
        let source = viewOnly ? #","source_kind":"runtime","capabilities":{"read":true,"send":false}"# : ""
        return try JSON.decoder.decode(Session.self, from: Data("""
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(id)","cwd":"/tmp","status":"idle","state":"\(state.rawValue)","assistant":\(assistant)\(source)}
        """.utf8))
    }

    @Test func aRetryKeepsTheClientIDOnlyWhileTheHandOffMayHaveGone() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 504, message: "Gateway Timeout"),
                          URLError(.timedOut),
                          APIError(status: 503, message: "offline", fields: ["code": "assistant_offline"]),
                          APIError(status: 502, message: "no answer", fields: ["code": "timeout"]),
                          APIError(status: 502, message: "typing broke off", fields: ["code": "internal", "delivered": "unknown"]),
                          APIError(status: 502, message: "typed, then failed", fields: ["code": "internal", "delivered": "yes"]),
                          APIError(status: 502, message: "node gone", fields: ["code": "node_unreachable"]),
                          APIError(status: 202, message: "unknown", fields: ["code": OpReceipt.unknownCode]),
                          APIError(status: 202, message: "pending", fields: ["code": OpReceipt.pendingCode]),
                          APIError(status: 502, message: "Bad Gateway"),
                          APIError(status: 409, message: "busy", fields: ["code": "input_draft", "delivered": "none"])]
        var outcomes: [Bool] = []
        for _ in 0..<12 { outcomes.append(await m.send(ask: "a")) }
        #expect(outcomes == Array(repeating: false, count: 11) + [true])
        let ids = calls.handOffs.map(\.clientID)
        #expect(ids.count == 12)
        // Anything that may have gone, or that the hub kept no answer for, is the same hand-off asked again; once it may
        // have gone, so is every answer after it, even one saying nothing went.
        #expect(Set(ids).count == 1)
        #expect(calls.opened.count == 1 && calls.opened.first?.clientID == ids.last)

        // On a new hand-off, a refusal saying nothing went is final: the hub gives it back for that id.
        calls.failures = [APIError(status: 503, message: "node offline", fields: ["code": "node_unreachable", "delivered": "none"])]
        _ = await m.send(ask: "a")
        _ = await m.send(ask: "a")
        let refused = calls.handOffs.map(\.clientID).suffix(2)
        #expect(Set(refused).count == 2)

        // Another ask is another hand-off, and so is the same ask again after one went.
        calls.failures = [APIError(status: 502, message: "cut off", fields: ["code": "assistant_in_doubt"])]
        _ = await m.send(ask: "b")
        _ = await m.send(ask: "a")
        _ = await m.send(ask: "a")
        let more = calls.handOffs.map(\.clientID).suffix(3)
        #expect(Set(more).count == 3 && !more.contains(ids[11]))
    }

    /// Each answer that leaves open whether the hand-off went keeps its client id on its own, with no try before it in doubt,
    /// and says to look first; on a first try, an answer saying nothing went is final.
    @Test func eachAnswerThatMayHaveGoneKeepsTheClientID() async throws {
        let look = String(localized: "It may have reached the computer already. Check the chat before sending it again.")
        let answers: [Error] = [
            APIError(status: 502, message: "no answer", fields: ["code": "timeout"]),
            APIError(status: 502, message: "cut off", fields: ["code": "assistant_in_doubt", "delivered": "unknown"]),
            APIError(status: 502, message: "cut off", fields: ["code": "assistant_in_doubt"]),
            APIError(status: 502, message: "typing broke off", fields: ["code": "internal", "delivered": "unknown"]),
            APIError(status: 502, message: "typed, then failed", fields: ["code": "internal", "delivered": "yes"]),
            APIError(status: 502, message: "node gone", fields: ["code": "node_unreachable"]),
            APIError(status: 202, message: "unknown", fields: ["code": OpReceipt.unknownCode]),
            APIError(status: 202, message: "pending", fields: ["code": OpReceipt.pendingCode]),
            APIError(status: 502, message: "Bad Gateway"), APIError(status: 504, message: "Gateway Timeout"),
            APIError(status: 500, message: "Internal Server Error"), APIError(status: 524, message: "server error"),
            URLError(.timedOut), URLError(.networkConnectionLost)]
        for answer in answers {
            let calls = Calls(), m = model(calls)
            calls.failures = [answer]
            let first = await m.send(ask: "a")
            #expect(!first && m.error == look, "\(answer)")
            let second = await m.send(ask: "a")
            #expect(second && calls.handOffs.count == 2 && calls.handOffs[0].clientID == calls.handOffs[1].clientID, "\(answer)")
        }

        // On a first try, an answer saying nothing went is final: it says so, and the next try is new.
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 503, message: "node offline", fields: ["code": "node_unreachable", "delivered": "none"])]
        _ = await m.send(ask: "a")
        #expect(m.error == String(localized: "This computer is offline: Persona Link isn't running, or the computer is asleep."))
        _ = await m.send(ask: "a")
        let ids = calls.handOffs.map(\.clientID)
        #expect(ids.count == 2 && ids[0] != ids[1])
    }

    /// Once a try may have gone, every later answer under its client id short of success is in doubt too, even one saying
    /// nothing went: the hub keeps a relay the phone or a proxy cut off as a bare `internal` and gives it back for that id,
    /// a refusal may be the hub's (`op_id_reused`), and after a receipt the hub dropped (`assistant_in_doubt`, which the
    /// phone may never have got) the answer is a new run's, which says nothing about the first try. The id stays, and the
    /// sheet says to look first and how to send it anyway.
    @Test func anAnswerAfterATryInDoubtStaysInDoubt() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [URLError(.networkConnectionLost),
                          APIError(status: 502, message: "context canceled", fields: ["code": "internal"]),
                          APIError(status: 409, message: "operation id reused", fields: ["code": "op_id_reused"]),
                          APIError(status: 503, message: "node offline", fields: ["code": "node_unreachable", "delivered": "none"]),
                          APIError(status: 409, message: "busy", fields: ["code": "input_draft", "delivered": "none"])]
        var errors: [String?] = []
        for _ in 0..<5 {
            _ = await m.send(ask: "a")
            errors.append(m.error)
        }
        #expect(errors == [Self.look] + Array(repeating: Self.lookAgain, count: 4))
        let went = await m.send(ask: "a")
        let ids = calls.handOffs.map(\.clientID)
        #expect(went && ids.count == 6 && Set(ids).count == 1)

        // With no earlier try in doubt, the same answer is final and says what it says.
        let c = Calls(), fresh = model(c)
        c.failures = [APIError(status: 502, message: "context canceled", fields: ["code": "internal"])]
        _ = await fresh.send(ask: "a")
        #expect(fresh.error?.contains("HTTP 502") == true && fresh.error?.contains("context canceled") == false)
        _ = await fresh.send(ask: "a")
        #expect(c.handOffs.count == 2 && c.handOffs[0].clientID != c.handOffs[1].clientID)
    }

    /// After `assistant_in_doubt` the hub drops its receipt, so the next try under that client id runs again at the manager,
    /// and the hub keeps that run's answer: a `delivered: none` (the computer offline by then) comes back for every try with
    /// that id while the first one may be in the chat. The same ask stays in doubt; another ask is a new hand-off.
    @Test func anAnswerAfterTheHubKeptNothingStaysInDoubt() async {
        let calls = Calls(), m = model(calls)
        let offline = APIError(status: 503, message: "node offline", fields: ["code": "node_unreachable", "delivered": "none"])
        calls.failures = [APIError(status: 502, message: "cut off", fields: ["code": "assistant_in_doubt", "delivered": "unknown"]),
                          offline, offline]
        _ = await m.send(ask: "a")
        for _ in 0..<2 {
            let went = await m.send(ask: "a")
            #expect(!went && m.error == Self.lookAgain)
        }
        let ids = calls.handOffs.map(\.clientID)
        #expect(ids.count == 3 && Set(ids).count == 1)
        let other = await m.send(ask: "b")
        #expect(other && calls.handOffs.last?.clientID != ids[0])
    }

    /// A try that never left (no connection, no hub, the manager offline) says nothing about an earlier one under the
    /// same client id, which may have gone: the id stays for the next try, and the sheet says what this one met.
    @Test func aTryThatNeverLeftKeepsTheClientID() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 502, message: "no answer", fields: ["code": "timeout"]),
                          URLError(.notConnectedToInternet), CancellationError(),
                          APIError(status: 503, message: "offline", fields: ["code": "assistant_offline"]),
                          APIError(status: 502, message: "context canceled", fields: ["code": "internal"])]
        var errors: [String?] = []
        for _ in 0..<5 {
            _ = await m.send(ask: "a")
            errors.append(m.error)
        }
        let went = await m.send(ask: "a")
        #expect(went && errors == [String(localized: "It may have reached the computer already. Check the chat before sending it again."),
                                   String(localized: "Can't reach the hub"), nil,
                                   String(localized: "The manager is offline or restarting. Try again shortly."), Self.lookAgain])
        #expect(calls.handOffs.count == 6 && Set(calls.handOffs.map(\.clientID)).count == 1 && calls.opened.count == 1)
    }

    /// A 404 after a try that may have gone is not taken for an older hub: the manager may have lost the item while the
    /// first try is in the chat. The sheet says to look first, and the same ask keeps the hand-off, so it never goes as a
    /// plain message as well; another ask is a new one.
    @Test func aMissingRouteAfterATryInDoubtLooksFirst() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [URLError(.timedOut), APIError(status: 404, message: "no such item", fields: ["code": "not_found"]),
                          APIError(status: 404, message: "no such item", fields: ["code": "not_found"]),
                          APIError(status: 404, message: "404 page not found")]
        _ = await m.send(ask: "a")
        let second = await m.send(ask: "a")
        #expect(!second && calls.asked.isEmpty && m.error == Self.lookAgain)
        let third = await m.send(ask: "a")
        #expect(!third && calls.asked.isEmpty && m.error == Self.lookAgain)
        let ids = calls.handOffs.map(\.clientID)
        #expect(ids.count == 3 && Set(ids).count == 1)
        let other = await m.send(ask: "b")
        #expect(other && calls.asked.count == 1 && calls.handOffs.last?.clientID != ids[0])
    }

    /// An older hub's plain message that may have gone (a timeout) is not sent again by the same ask: the sheet says to look
    /// first, and the same ask keeps the hand-off in doubt.
    @Test func aPlainMessageThatMayHaveGoneIsNotSentAgain() async {
        let calls = Calls(), m = model(calls)
        let look = String(localized: "It may have reached the computer already. Check the chat before sending it again.")
        calls.failures = [APIError(status: 404, message: "404 page not found"), APIError(status: 404, message: "404 page not found")]
        calls.askFailures = [URLError(.timedOut)]
        let first = await m.send(ask: "a")
        #expect(!first && m.error == look && calls.asked.count == 1)
        let second = await m.send(ask: "a")
        #expect(!second && m.error == Self.lookAgain && calls.asked.count == 1)
        #expect(calls.handOffs.count == 2 && calls.handOffs[0].clientID == calls.handOffs[1].clientID)
    }

    /// A new message on the item between the first try and the retry (likely after a 120 s timeout) changes the text the
    /// item builds: the retry still sends the first text under the first client id, which the hub and the manager know.
    @Test func aRetrySendsTheFirstTextAfterTheItemChanged() async throws {
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 502, message: "no answer", fields: ["code": "timeout"])]
        _ = await m.send(ask: "a")
        calls.preview = "又来了一条"
        let went = await m.send(ask: "a")
        #expect(went && calls.handOffs.count == 2)
        let first = try #require(calls.handOffs.first), retry = try #require(calls.handOffs.last)
        #expect(retry.clientID == first.clientID && retry.text == first.text)
        #expect(calls.opened.first?.text == first.text && calls.opened.first?.clientID == first.clientID)

        // Once it went, the next hand-off is new and carries the item as it is now.
        _ = await m.send(ask: "a")
        let next = try #require(calls.handOffs.last)
        #expect(next.clientID != first.clientID && next.text.contains("又来了一条") && next.text != first.text)
    }

    @Test func aFailureSaysWhyAndKeepsTheSheet() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 409, message: "The assistant can't run on a work machine", fields: ["code": "work_machine"])]
        let went = await m.send(ask: "a")
        #expect(!went && m.error == "The assistant can't run on a work machine" && !m.sending && calls.opened.isEmpty && calls.done == 0)
        _ = await m.send(ask: "a")
        #expect(m.error == nil)
    }

    /// An answer that leaves open whether the hand-off went (the node's `delivered: unknown`, a timeout behind the hub or
    /// here, the hub's unknown receipt, the connection dropping after the request went out) says to look before sending
    /// again, and a retry already in doubt how to send it anyway; no connection at all says the hub is out of reach. Neither
    /// was said: those errors show nothing, and the sheet just stopped spinning.
    @Test func aFailureThatMayHaveGoneSaysToLookFirst() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 502, message: "no answer", fields: ["code": "timeout"]),
                          APIError(status: 502, message: "typing broke off", fields: ["code": "internal", "delivered": "unknown"]),
                          APIError(status: 202, message: "unknown", fields: ["code": OpReceipt.unknownCode]),
                          URLError(.timedOut), URLError(.networkConnectionLost), URLError(.notConnectedToInternet)]
        let look = String(localized: "It may have reached the computer already. Check the chat before sending it again.")
        for n in 0..<5 {
            let went = await m.send(ask: "a")
            #expect(!went && m.error == (n == 0 ? look : Self.lookAgain))
        }
        let went = await m.send(ask: "a")
        #expect(!went && m.error == String(localized: "Can't reach the hub"))
        // Whatever says to look first keeps the hand-off's client id, so the hub and the manager send it once.
        #expect(calls.handOffs.count == 6 && Set(calls.handOffs.map(\.clientID)).count == 1)
    }

    /// Loading sent nothing. A gateway response is not evidence of a network outage or an uncertain send.
    @Test func aFailedLoadPreservesTheFailureKind() async {
        let calls = Calls(), m = model(calls)
        calls.loadFailures = [URLError(.timedOut), APIError(status: 502, message: "Bad Gateway"),
                              APIError(status: 404, message: "no such item", fields: ["code": "not_found"]),
                              APIError(status: 503, message: "Request failed", fields: ["code": "assistant_offline"]),
                              URLError(.notConnectedToInternet)]
        var errors: [String?] = []
        for _ in 0..<5 {
            _ = await m.send(ask: "a")
            errors.append(m.error)
        }
        #expect(errors == [String(localized: "The request timed out. Try again later."), "Bad Gateway", "no such item",
                           String(localized: "The manager is offline or restarting. Try again shortly."),
                           String(localized: "Can't reach the hub")] && calls.handOffs.isEmpty)
        let went = await m.send(ask: "a")
        #expect(went && calls.handOffs.count == 1)
    }

    @Test func anOlderHubGetsThePlainMessage() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 404, message: "404 page not found")]
        let went = await m.send(ask: "a")
        #expect(went && calls.asked == calls.handOffs.map(\.text) && calls.opened.isEmpty && calls.done == 0)

        // Checked, the item is filed once the message went, not when it didn't.
        let c = Calls(), checked = model(c)
        checked.setMarkDone(true)
        c.failures = [APIError(status: 405, message: "Method Not Allowed")]
        c.askFailures = [APIError(status: 503, message: "Persona is offline", fields: ["code": "assistant_offline"])]
        let wentAnyway = await checked.send(ask: "a")
        #expect(!wentAnyway && c.done == 0 && checked.error == String(localized: "The manager is offline or restarting. Try again shortly."))
        c.failures = [APIError(status: 405, message: "Method Not Allowed")]
        _ = await checked.send(ask: "a")
        #expect(c.done == 1 && c.asked.count == 2 && c.handOffs[0].clientID != c.handOffs[1].clientID)
    }

    @Test func theHandOffPostsTheTextAndClientID() async throws {
        let hub = FakeHub(), path = "/api/v1/inbox/lark:f1:11/handoff"
        hub.on("POST", path, json: """
        {"item":\(Self.item),"session":{"machine_id":"server","session_id":"main-1"}}
        """)
        let id = UUID()
        let answer = try await hub.client.inboxHandoff("lark:f1:11", text: "看看", clientID: id)
        #expect(answer.session == Self.main && answer.item?.id == "lark:f1:11")
        let sent = try #require(hub.sent("POST", path).first)
        let body = try JSON.decoder.decode([String: String].self, from: try #require(sent.body))
        #expect(body == ["text": "看看", "client_id": id.uuidString.lowercased()])
    }

    /// A retry while the hub still runs the first hand-off gets its receipt (202 pending), not an answer: the hand-off may
    /// still go, so the next try keeps the client id.
    @Test func aHandOffTheHubStillRunsKeepsItsClientID() async throws {
        let hub = FakeHub(), path = "/api/v1/inbox/lark:f1:11/handoff"
        let answers = Answers([#"{"state":"pending"}"#, #"{"session":{"machine_id":"server","session_id":"main-1"}}"#])
        hub.on("POST", path) { _ in Data(answers.next().utf8) }
        let m = model(Calls(), hub: hub.client)
        let first = await m.send(ask: "a")
        #expect(!first && m.error == String(localized: "It may have reached the computer already. Check the chat before sending it again."))
        let second = await m.send(ask: "a")
        #expect(second)
        let ids = try hub.sent("POST", path).map { try JSON.decoder.decode([String: String].self, from: try #require($0.body))["client_id"] }
        #expect(ids.count == 2 && ids[0] != nil && ids[0] == ids[1])
    }

    private final class Answers {
        private var left: [String]
        init(_ answers: [String]) { left = answers }
        func next() -> String { left.removeFirst() }
    }

    /// The hand-off went: an item in the answer this app can't read is left out, not taken for a failure.
    @Test func anUnreadableItemStillAnswersTheConversation() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/inbox/lark:f1:11/handoff", json: """
        {"item":{"id":"lark:f1:11"},"session":{"machine_id":"server","session_id":"main-1"}}
        """)
        let answer = try await hub.client.inboxHandoff("lark:f1:11", text: "看看", clientID: UUID())
        #expect(answer.session == Self.main && answer.item == nil)
    }

    // MARK: The detail page's bar (InboxHandOffBar)

    @Test func sendingMovesNothingAndOpenGoesWhereItWent() async {
        let calls = Calls(), m = model(calls)
        #expect(await m.send(ask: "a"))
        #expect(calls.went.isEmpty)
        m.open()
        #expect(calls.went.count == 1 && calls.went[0].target == .main && calls.went[0].key == Self.main)

        m.target = .newChat
        #expect(await m.send(ask: "b"))
        m.open()
        #expect(calls.went.last?.target == .newChat && calls.went.last?.key == Self.fresh)

        let agent = SessionKey(machineID: "m", sessionID: "a")
        m.target = .agent(agent)
        #expect(await m.send(ask: "c"))
        m.open()
        #expect(calls.went.last?.target == .agent(agent) && calls.went.last?.key == agent && calls.went.count == 3)
    }

    @Test func aFailedSendHasNowhereToOpen() async {
        let calls = Calls(), m = model(calls)
        calls.failures = [APIError(status: 400, message: "no")]
        #expect(!(await m.send(ask: "a")))
        m.open()
        #expect(calls.went.isEmpty && m.sent == nil && m.lastAsk == "a")
    }

    @Test func undoInTheWindowSendsNothing() async throws {
        let calls = Calls(), m = model(calls)
        m.queue(ask: "a", after: .seconds(60))
        #expect(m.queued == "a")
        #expect(m.undo() == "a")
        #expect(m.queued == nil && m.undo() == nil)
        try await Task.sleep(for: .milliseconds(50))
        #expect(calls.handOffs.isEmpty && calls.done == 0)
    }

    @Test func aQueuedAskGoesWhenTheWindowRunsOut() async throws {
        let calls = Calls(), m = model(calls)
        m.queue(ask: "a", after: .milliseconds(10))
        for _ in 0..<200 where calls.handOffs.isEmpty || m.sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(calls.handOffs.count == 1 && ContextRef.parse(calls.handOffs[0].text).rest == "a")
        #expect(m.queued == nil && m.sent?.key == Self.main && calls.went.isEmpty)
    }

    @Test func viewSendsTheQueuedAskAtOnceAndOnlyOnce() async throws {
        let calls = Calls(), m = model(calls)
        m.queue(ask: "a", after: .milliseconds(30))
        #expect(await m.sendQueued())
        #expect(!(await m.sendQueued()))
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls.handOffs.count == 1 && m.queued == nil)
    }

    @Test func aNewAskReplacesTheQueuedOne() async throws {
        let calls = Calls(), m = model(calls)
        m.queue(ask: "a", after: .seconds(60))
        m.queue(ask: "b", after: .milliseconds(10))
        for _ in 0..<200 where calls.handOffs.isEmpty || m.sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(calls.handOffs.count == 1 && ContextRef.parse(calls.handOffs[0].text).rest == "b")
    }

    @Test func theTargetIsRememberedAsAString() {
        let agent = SessionKey(group: "work", machineID: "m", sessionID: "a")
        for t: InboxHandOffModel.Target in [.main, .newChat, .agent(agent), .agent(SessionKey(machineID: "m", sessionID: "b"))] {
            #expect(InboxHandOffModel.Target(stored: t.stored) == t)
        }
        #expect(InboxHandOffModel.Target(stored: "") == .main)
        #expect(InboxHandOffModel.Target(stored: "agent:nonsense") == .main)
    }

    @Test func theChipsAsksAreTheSheetsPresets() {
        #expect(InboxAsk.allCases.count == 3)
        #expect(Set(InboxAsk.allCases.map(\.ask)).count == 3)
        #expect(Set(InboxAsk.allCases.map(\.title)).count == 3)
    }
}

/// The live Agent path through AppModel and the real outbox: a wait is not a final failure, and a later refusal keeps
/// the hub's reason. FakeHub records every request and its operation id.
@MainActor
@Suite(.serialized)
struct InboxHandOffDeliveryTests {
    private let key = SessionKey(machineID: "m", sessionID: "handoff-delivery")

    private func model(_ hub: FakeHub, folder: URL? = nil) -> AppModel {
        let model = AppModel(stateFolder: folder ?? FileManager.default.temporaryDirectory.appending(path: UUID().uuidString))
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        model.linksUpForTests = true
        let json = #"{"machine_id":"m","session_id":"handoff-delivery","title":"t","cwd":"/tmp/p","status":"idle","state":"working"}"#
        let session = try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
        model.applySessions([MachineRef(group: "", id: "m"): [session]])
        return model
    }

    @Test func waitingThenDeliveredUsesOnePendingMessageAndOneID() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        var calls = 0
        hub.on("POST", path) { _ in
            calls += 1
            if calls == 1 { throw URLError(.timedOut) }
            return Data(#"{"ok":true}"#.utf8)
        }
        let model = model(hub)
        defer { model.outboxRetry.values.forEach { $0.cancel() } }
        var result: InboxHandOffModel.AgentSendResult?
        let sending = Task { @MainActor in result = await model.sendForHandOff(key, text: "context") }
        for _ in 0..<300 where model.chat(key).pending.first?.state != .waiting { try await Task.sleep(for: .milliseconds(10)) }
        try #require(model.chat(key).pending.first?.state == .waiting)
        #expect(result == nil && calls == 1, "waiting completed the hand-off before delivery")

        model.flushOutbox()
        for _ in 0..<300 where result == nil { try await Task.sleep(for: .milliseconds(10)) }
        sending.cancel()
        let delivered = try #require(result, "the outbox's delivery did not finish the hand-off")
        #expect(delivered.delivered)
        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        #expect(Set(requests.map { $0.headers[OpRetry.header] ?? "" }).count == 1)
        let pendingID = try #require(model.chat(key).pending.first).id.uuidString
        #expect(requests.first?.headers[OpRetry.header]?.caseInsensitiveCompare(pendingID) == .orderedSame)
        #expect(model.chat(key).pending.first?.state == .sent)
    }

    @Test func aRestoredWaitingHandOffFilesWhenItsRetryIsDelivered() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        var calls = 0
        hub.on("POST", path) { _ in
            calls += 1
            if calls == 1 { throw URLError(.timedOut) }
            return Data(#"{"ok":true}"#.utf8)
        }
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let first = model(hub, folder: folder)
        var firstFilings: [PendingMessage.InboxHandOff] = []
        first.inboxHandOffDoneForTests = { firstFilings.append($0) }
        let item = try JSON.decoder.decode(InboxItem.self, from: Data(#"{"id":"mail:a:1","source":"mail","kind":"mail","account":"a","account_name":"A","title":"t","preview":"p","count":1,"manager":"mgr"}"#.utf8))
        let sending = Task { @MainActor in _ = await first.sendForHandOff(key, text: "context", item: item, file: true) }
        for _ in 0..<300 where first.chat(key).pending.first?.state != .waiting { try await Task.sleep(for: .milliseconds(10)) }
        try #require(first.chat(key).pending.first?.state == .waiting)
        sending.cancel()
        first.saveOutbox()
        try await Task.sleep(for: .milliseconds(600))
        #expect(firstFilings.isEmpty)
        first.outboxRetry.values.forEach { $0.cancel() }

        let restored = model(hub, folder: folder)
        var filings: [PendingMessage.InboxHandOff] = []
        restored.inboxHandOffDoneForTests = { filings.append($0) }
        restored.restoreOutbox()
        #expect(restored.chat(key).pending.first?.inboxHandOff?.itemID == "mail:a:1")
        restored.flushOutbox()
        for _ in 0..<300 where filings.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        restored.outboxRetry.values.forEach { $0.cancel() }
        #expect(filings.count == 1)
        #expect(filings.first?.itemID == "mail:a:1" && filings.first?.manager == "mgr" && filings.first?.filed == true)
        #expect(restored.chat(key).pending.first?.state == .sent)
        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        #expect(Set(requests.map { ($0.headers[OpRetry.header] ?? "").lowercased() }).count == 1)
    }

    @Test func aRefusalReturnsTheComputersReason() async throws {
        let hub = FakeHub()
        let path = "/api/v1/sessions/m/\(key.sessionID)/messages"
        hub.on("POST", path) { _ in
            throw APIError(status: 409, message: "Finish the draft on the computer first",
                           fields: ["code": "input_draft", "delivered": "none"])
        }
        let result = await model(hub).sendForHandOff(key, text: "context")
        #expect(!result.delivered)
        #expect(result.error?.contains("Finish the draft on the computer first") == true)
    }
}
