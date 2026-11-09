import AppIntents
import Foundation
import Testing
@testable import Persona

/// Pure logic behind the intents: wording, selection rules and what each interaction kind resolves to.
/// Everything here runs without a hub, which is why it is testable at all.
struct IntentsTests {
    // MARK: Fixtures

    // `Machine` and `Session` declare `init(from:)`, so the memberwise init is gone: build them from JSON,
    // exactly as they arrive from the hub.
    private func decode<T: Decodable>(_ json: String) -> T {
        do { return try JSON.decoder.decode(T.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func machine(_ id: String, online: Bool = true, name: String? = nil) -> Machine {
        decode("""
        {"id":"\(id)","name":"\(name ?? id)","kind":"mac","online":\(online)}
        """)
    }

    private func session(_ id: String, machine: String = "server", status: SessionStatus = .idle,
                         updated: TimeInterval = 0, title: String? = nil) -> Session {
        decode("""
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(title ?? id)","cwd":"/tmp/p",
         "status":"\(status.rawValue)","updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """)
    }

    private func interaction(_ kind: InteractionKind, created: TimeInterval = 0, tool: String? = nil,
                             summary: String? = nil, questions: [Question]? = nil,
                             plan: String? = nil) -> Interaction {
        Interaction(
            id: "ix_\(kind.rawValue)_\(Int(created))",
            machineID: "server",
            sessionID: "s1",
            sessionTitle: "修构建",
            cwd: "/tmp/p",
            kind: kind,
            createdAt: Date(timeIntervalSince1970: created),
            expiresAt: nil,
            toolName: tool,
            toolUseID: nil,
            questions: questions,
            plan: plan,
            toolInput: nil,
            summary: summary,
            screen: nil
        )
    }

    // MARK: Wording

    @Test func sentTextNamesTheSession() {
        #expect(IntentText.sent(to: "修构建") == "已发给 修构建")
        // An untitled session still reads as a sentence instead of 「已发给 」.
        #expect(IntentText.sent(to: "  ") == "已发给 会话")
    }

    @Test func statusCountsAndFirstQuestion() {
        #expect(IntentText.status(working: 2, waiting: 0, first: nil) == "2 个工作中，0 个需要你")
        let text = IntentText.status(working: 0, waiting: 1, first: "要我用 opus 还是 sonnet？")
        #expect(text.hasPrefix("0 个工作中，1 个需要你："))
        #expect(text.contains("要我用 opus 还是 sonnet？"))
    }

    @Test func statusExcerptCollapsesWhitespaceAndClips() {
        #expect(IntentText.excerpt("a\n\n  b\tc") == "a b c")
        let long = String(repeating: "长", count: 200)
        let short = IntentText.excerpt(long, limit: 10)
        #expect(short.hasSuffix("…"))
        #expect(short.count == 11)
    }

    @Test func waitingPromptForEachKind() {
        let question = Question(question: "用哪个分支？", header: nil, multiSelect: false, options: [])
        #expect(IntentText.waitingPrompt(interaction(.question, questions: [question])) == "用哪个分支？")
        // No question text at all: fall back to the summary instead of saying nothing.
        #expect(IntentText.waitingPrompt(interaction(.question, summary: "问了点东西")) == "问了点东西")

        #expect(IntentText.waitingPrompt(interaction(.plan, plan: "1. 改 A\n2. 改 B")) == "计划：1. 改 A 2. 改 B")
        #expect(IntentText.waitingPrompt(interaction(.plan)) == "有个计划在等你批准")

        #expect(IntentText.waitingPrompt(interaction(.permission, summary: "git push origin master")) == "git push origin master")
        #expect(IntentText.waitingPrompt(interaction(.permission, tool: "Bash")) == "Bash 在申请授权")
        #expect(IntentText.waitingPrompt(interaction(.screen)) == "终端在等你输入")
    }

    @Test func approvedTexts() {
        #expect(IntentText.approvedPermission("Bash") == "已允许 Bash")
        #expect(IntentText.approvedPermission(nil) == "已允许")
        #expect(IntentText.approvedPlan == "已批准计划，开始执行")
        #expect(IntentText.needsApp(.question).contains("提问"))
        #expect(IntentText.needsApp(.screen).contains("终端"))
    }

    @Test func phrasesCarryTheAppName() {
        #expect(IntentPhrases.fill(IntentPhrases.askClaude, appName: "Persona") == "用 Persona 问 Claude")
        #expect(IntentPhrases.fill(IntentPhrases.status, appName: "Persona") == "Persona 状态")
        #expect(IntentPhrases.fill(IntentPhrases.approve, appName: "Persona") == "用 Persona 批准第一个")
        #expect(IntentPhrases.fill(IntentPhrases.addTodo, appName: "Persona") == "用 Persona 新建待办")
        // Every template needs the placeholder, otherwise Siri cannot address the app by name.
        for template in [IntentPhrases.askClaude, IntentPhrases.status, IntentPhrases.approve, IntentPhrases.addTodo] {
            #expect(template.contains("%@"))
        }
    }

    // MARK: Todos

    @Test func addTodoCleansTextAndSpeaksTheTitle() {
        #expect(TodoIntentText.cleaned("  买牛奶 \n") == "买牛奶")
        #expect(TodoIntentText.cleaned(" \n ") == nil)
        #expect(TodoIntentText.cleaned(nil) == nil)
        #expect(TodoIntentText.added("买牛奶") == "已记下待办：「买牛奶」")
        #expect(TodoIntentText.added(String(repeating: "字", count: 80)).count == "已记下待办：「」".count + 61)
    }

    @Test func addTodoExplainsAnOldHub() {
        #expect(TodoIntentText.failure(APIError(status: 404, message: "Not Found")) == TodoIntentText.oldHub)
        #expect(TodoIntentText.failure(APIError(status: 401, message: "x")).contains("重新配对"))
        #expect(TodoIntentText.failure(IntentFailure.notPaired).contains("还没配对"))
    }

    /// The intent reads the app's selected hub group under the same key the app writes.
    @MainActor @Test func addTodoUsesTheAppsHubGroup() {
        #expect(IntentText.hubGroupKey == AppModel.hubGroupKey)
    }

    // MARK: Errors

    @Test func failureSentences() {
        #expect(IntentFailure.notPaired.sentence.contains("还没配对"))
        #expect(IntentFailure.noMachine.sentence.contains("没有在线的电脑"))
        #expect(IntentFailure.offline("desktop").sentence.contains("desktop"))
        #expect(IntentFailure.noSession("server").sentence.contains("没有运行中的会话"))
        #expect(IntentFailure.noLauncher("server").sentence.contains("新对话"))
        #expect(IntentFailure.noPending.sentence.contains("没有等你处理"))
    }

    @Test func hubErrorsBecomeReadableSentences() {
        #expect(IntentFailure.sentence(for: APIError(status: 401, message: "unauthorized")).contains("重新配对"))
        #expect(IntentFailure.sentence(for: APIError(status: 410, message: "gone")).contains("已经处理过"))
        #expect(IntentFailure.sentence(for: APIError(status: 500, message: "boom")) == "boom")
        #expect(IntentFailure.sentence(for: URLError(.timedOut), host: "hub:18660").contains("hub:18660"))
        let offline = IntentFailure.sentence(for: URLError(.cannotConnectToHost), host: "hub:18660")
        #expect(offline.contains("连不上 hub"))
        #expect(offline.contains("VPN"))
        // A plain NSError from URLSession takes the same path as a URLError.
        #expect(IntentFailure.sentence(for: NSError(domain: NSURLErrorDomain, code: -1004)).contains("连不上 hub"))
        // Unknown errors keep their own message rather than being flattened.
        #expect(IntentFailure.sentence(for: IntentFailure.noPending) == IntentFailure.noPending.sentence)
    }

    // MARK: Selection

    @Test func machinePrefersTheRememberedOneAndFallsBackToOnline() {
        let machines = [machine("server", online: false), machine("laptop", online: true)]
        #expect(IntentPicker.machine(machines, preferred: "server")?.id == "server")
        #expect(IntentPicker.machine(machines, preferred: "gone")?.id == "laptop")
        #expect(IntentPicker.machine(machines, preferred: nil)?.id == "laptop")
        // Nobody online: still pick something, the intent reports it as offline.
        let allOffline = [machine("server", online: false)]
        #expect(IntentPicker.machine(allOffline, preferred: nil)?.id == "server")
        #expect(IntentPicker.machine([], preferred: "server") == nil)
    }

    @Test func sessionPicksNewestLiveOneOnTheMachine() {
        let sessions = [
            session("old", status: .idle, updated: 10),
            session("newest", status: .done, updated: 30),
            session("dead", status: .offline, updated: 99),
            session("elsewhere", machine: "laptop", status: .working, updated: 42),
        ]
        #expect(IntentPicker.session(sessions, machine: "server")?.sessionID == "newest")
        #expect(IntentPicker.session(sessions, machine: "desktop") == nil)
        #expect(IntentPicker.session([session("dead", status: .offline)], machine: "server") == nil)
    }

    @Test func pendingPicksTheOldestInteraction() {
        let list = [interaction(.permission, created: 30), interaction(.plan, created: 10)]
        #expect(IntentPicker.pending(list)?.kind == .plan)
        #expect(IntentPicker.pending([]) == nil)
    }

    // MARK: Waiting cards (#5257)

    private func waitingAgent(_ id: String, group: String = "", assistant: Bool = false, quiet: Bool = false,
                              space: String? = nil, status: String = "idle", state: String? = nil) -> Session {
        var o: [String: Any] = ["machine_id": "server", "session_id": id, "title": id, "cwd": "/tmp/p", "status": status,
                                "assistant": assistant, "quiet": quiet]
        if let space { o["space_name"] = space }
        if let state { o["state"] = state }
        do {
            var s = try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
            s.group = group
            return s
        } catch { fatalError("bad fixture: \(error)") }
    }

    private func waitingCard(_ id: String, session: String, created: TimeInterval?, group: String = "",
                             kind: InteractionKind = .permission, hubMuted: Bool = false) -> Interaction {
        var ix = Interaction(id: id, machineID: "server", sessionID: session, kind: kind,
                             createdAt: created.map { Date(timeIntervalSince1970: $0) }, summary: "card \(id)")
        ix.group = group
        if hubMuted { ix.muted = true }
        return ix
    }

    /// The mutes of the default group: one agent, one space, and one agent turned back on inside that space.
    private var waitingMutes: [String: Pins] {
        var p = Pins()
        p.sessions = ["server/muted"]
        p.spaces = ["server/loud-off"]
        p.allowed = ["server/back-on"]
        return ["": p]
    }

    private var waitingSessions: [Session] {
        [
            waitingAgent("plain"), waitingAgent("other"), waitingAgent("muted"),
            waitingAgent("in-space", space: "loud-off"), waitingAgent("back-on", space: "loud-off"),
            waitingAgent("task", quiet: true), waitingAgent("persona", assistant: true),
            waitingAgent("muted", group: "work"),
        ]
    }

    private struct WaitingCase {
        let name: String
        let cards: [Interaction]
        let visible: [String]
        let first: String?
    }

    private var waitingCases: [WaitingCase] {
        [
            WaitingCase(name: "a muted agent's older card is skipped",
                        cards: [waitingCard("m", session: "muted", created: 1), waitingCard("p", session: "plain", created: 2)],
                        visible: ["p"], first: "p"),
            WaitingCase(name: "a muted space's card is skipped, an agent turned back on in it is not",
                        cards: [waitingCard("s", session: "in-space", created: 1),
                                waitingCard("b", session: "back-on", created: 2)],
                        visible: ["b"], first: "b"),
            WaitingCase(name: "a quiet task's older card is skipped",
                        cards: [waitingCard("q", session: "task", created: 1), waitingCard("p", session: "plain", created: 2)],
                        visible: ["p"], first: "p"),
            WaitingCase(name: "the hub's muted stamp is skipped even without the mutes list",
                        cards: [waitingCard("h", session: "other", created: 1, hubMuted: true),
                                waitingCard("p", session: "plain", created: 2)],
                        visible: ["p"], first: "p"),
            WaitingCase(name: "Persona's own question counts",
                        cards: [waitingCard("a", session: "persona", created: 1, kind: .question),
                                waitingCard("p", session: "plain", created: 2)],
                        visible: ["a", "p"], first: "a"),
            WaitingCase(name: "mutes are each group's own",
                        cards: [waitingCard("w", session: "muted", created: 1, group: "work"),
                                waitingCard("m", session: "muted", created: 2)],
                        visible: ["w"], first: "w"),
            WaitingCase(name: "everything skipped: nothing waiting",
                        cards: [waitingCard("m", session: "muted", created: 1), waitingCard("q", session: "task", created: 2),
                                waitingCard("s", session: "in-space", created: 3)],
                        visible: [], first: nil),
            WaitingCase(name: "nothing at all", cards: [], visible: [], first: nil),
            WaitingCase(name: "oldest first whatever the order, a card without a time first",
                        cards: [waitingCard("late", session: "plain", created: 30), waitingCard("early", session: "other", created: 10),
                                waitingCard("m", session: "muted", created: 5), waitingCard("undated", session: "plain", created: nil)],
                        visible: ["late", "early", "undated"], first: "undated"),
            WaitingCase(name: "a card whose agent is not in the list still counts unless muted by its key",
                        cards: [waitingCard("m", session: "muted", created: 1), waitingCard("u", session: "unknown", created: 2)],
                        visible: ["u"], first: "u"),
        ]
    }

    @Test func waitingSkipsMutedAndQuietCards() {
        for c in waitingCases {
            let waiting = IntentPicker.waiting(c.cards, sessions: waitingSessions, mutes: waitingMutes)
            #expect(waiting.cards.map(\.id) == c.visible, "\(c.name)")
            #expect(waiting.first?.id == c.first, "\(c.name)")
        }
    }

    /// The card 「Persona 状态」 reads out is the card 「批准第一个待处理」 settles (#5257): each intent's own code, the
    /// status sentence against the approve target.
    @Test func statusReadsOutTheCardApproveSettles() {
        let sessions = waitingSessions, mutes = waitingMutes
        for c in waitingCases {
            let text = PersonaStatusIntent.text(sessions: sessions, interactions: c.cards, mutes: mutes)
            let approved = ApproveFirstIntent.target(c.cards, sessions: sessions, mutes: mutes)
            #expect(approved?.id == c.first, "\(c.name)")
            #expect(text.hasPrefix(IntentText.status(working: 0, waiting: c.visible.count, first: nil)), "\(c.name)")
            if let approved {
                #expect(text.contains(IntentText.waitingPrompt(approved)), "\(c.name)")
                // No skipped card is ever what Siri reads out.
                for skipped in c.cards where !c.visible.contains(skipped.id) {
                    #expect(!text.contains(IntentText.waitingPrompt(skipped)), "\(c.name)")
                }
            } else {
                #expect(text == IntentText.status(working: 0, waiting: 0, first: nil), "\(c.name)")
            }
        }
        // The issue's case: the oldest card of all is a muted agent's plan; picking from every card would approve it.
        let cards = [waitingCard("m", session: "muted", created: 1, kind: .plan), waitingCard("p", session: "plain", created: 2)]
        #expect(IntentPicker.pending(cards)?.id == "m")
        #expect(ApproveFirstIntent.target(cards, sessions: sessions, mutes: mutes)?.id == "p")
        #expect(PersonaStatusIntent.text(sessions: sessions, interactions: cards, mutes: mutes).contains("card p"))
    }

    /// Working is what the island counts (#180): the hub's state, and only the Agents tab's agents.
    @Test func workingCountsOnlyTheAgentsTabsAgentsAtWork() {
        let sessions = [
            waitingAgent("plain", status: "working"),
            waitingAgent("persona", assistant: true, status: "working"),
            waitingAgent("task", quiet: true, status: "working"),
            waitingAgent("muted", status: "working"),
            waitingAgent("in-space", space: "loud-off", status: "working"),
            waitingAgent("back-on", space: "loud-off", status: "working"),
            waitingAgent("muted", group: "work", status: "working"),
            // The hub's state wins over herdr's raw status, both ways.
            waitingAgent("asking", status: "working", state: "needs_you"),
            waitingAgent("derived", status: "idle", state: "working"),
        ]
        // plain, back-on (turned back on in a muted space), the other group's "muted" (mutes are each group's own), derived.
        #expect(IntentPicker.working(sessions, mutes: waitingMutes) == 4)
        #expect(IntentPicker.working([], mutes: waitingMutes) == 0)
        // A hub that does not answer mutes still leaves Persona and quiet tasks out.
        #expect(IntentPicker.working(sessions, mutes: [:]) == 6)
    }

    /// The issue's case: a muted agent's card, a card muted on the interaction itself, an ordinary card, and Persona
    /// answering. Siri says 0 working, 1 waiting, and reads the ordinary card.
    @Test func statusMatchesTheAppForTheIssuesCase() {
        let sessions = [waitingAgent("plain"), waitingAgent("other"), waitingAgent("muted"),
                        waitingAgent("persona", assistant: true, status: "working")]
        let cards = [waitingCard("m", session: "muted", created: 1), waitingCard("h", session: "other", created: 2, hubMuted: true),
                     waitingCard("p", session: "plain", created: 3)]
        let text = PersonaStatusIntent.text(sessions: sessions, interactions: cards, mutes: waitingMutes)
        #expect(text.hasPrefix(IntentText.status(working: 0, waiting: 1, first: nil)))
        #expect(text.contains("card p"))
    }

    // MARK: Approve

    @Test func onlyPermissionAndPlanCanBeResolvedFromSiri() {
        let permission = ApproveFirstIntent.resolution(for: interaction(.permission))
        #expect(permission?.allow == true)
        #expect(permission?.approve == nil)

        let plan = ApproveFirstIntent.resolution(for: interaction(.plan))
        #expect(plan?.approve == true)
        #expect(plan?.mode == "acceptEdits") // never auto-run everything from Siri

        #expect(ApproveFirstIntent.resolution(for: interaction(.question)) == nil)
        #expect(ApproveFirstIntent.resolution(for: interaction(.screen)) == nil)
    }

    /// Same rule as the notification and Live Activity buttons: saying yes from a locked phone needs Face ID.
    @Test func approvingFromSiriNeedsUnlock() {
        #expect(ApproveFirstIntent.authenticationPolicy == .requiresAuthentication)
    }

    @Test func approveRepliesDescribeWhatHappened() {
        #expect(ApproveFirstIntent.doneText(for: interaction(.permission, tool: "Bash")) == "已允许 Bash")
        #expect(ApproveFirstIntent.doneText(for: interaction(.plan)) == IntentText.approvedPlan)
        #expect(ApproveFirstIntent.doneText(for: interaction(.question)).contains("打开 Persona"))
    }

    // MARK: Entities

    @Test func sessionEntityRoundTripsThroughItsIdentifier() {
        let entity = SessionEntity(session("s1", title: "修构建"))
        #expect(entity.id == "server/s1")
        #expect(entity.key == SessionKey(machineID: "server", sessionID: "s1"))
        #expect(SessionEntity(machineID: "server", sessionID: "s1", title: "").id == entity.id)
    }

    @Test func unresolvedSessionKeepsItsIdentifier() {
        let entity = SessionEntityQuery.unresolved("server/s1")
        #expect(entity.id == "server/s1")
        #expect(entity.key == SessionKey(machineID: "server", sessionID: "s1"))

        let other = SessionEntityQuery.unresolved("work|server/s1")
        #expect(other.id == "work|server/s1")
        #expect(other.key == SessionKey(group: "work", machineID: "server", sessionID: "s1"))

        #expect(SessionEntityQuery.unresolved("server").sessionID == "")
    }

    @Test func machineEntityCarriesTheOnlineFlag() {
        #expect(MachineEntity(machine("server", online: false)).online == false)
        #expect(MachineEntity(id: "server", name: "server").online == true)
    }
}
