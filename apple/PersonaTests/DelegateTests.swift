import Foundation
import Testing
@testable import Persona

/// The assistant answering agents' questions first (#544, docs/assistant/assistant.md "Assistant answers questions first"):
/// a card it is still looking at, the context it filled in when it left a question to the user, and the questions it
/// answered, which the user may answer again (the web's lib/delegate.ts and its store).
@MainActor
struct DelegateTests {
    private func q(_ question: String, _ labels: [String], multi: Bool = false) -> Question {
        Question(question: question, header: nil, multiSelect: multi, options: labels.map { QuestionOption(label: $0) })
    }

    private func card(_ id: String = "ix_1", session: String = "s", questions: [Question]? = nil, delegating: Bool? = nil,
                      delegate: Delegate? = nil) -> Interaction {
        var ix = Interaction(id: id, machineID: "m", sessionID: session, kind: .question,
                             questions: questions ?? [q("用哪个库？", ["pgx", "sqlc"])])
        ix.delegating = delegating
        ix.delegate = delegate
        return ix
    }

    private func session(_ id: String = "s", send: Bool) -> Session {
        let json = #"{"machine_id":"m","session_id":"\#(id)","title":"t","cwd":"/tmp/p","status":"idle","state":"idle","source_kind":"codex_app_server","capabilities":{"read":true,"send":\#(send)}}"#
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    private let deadline = Date(timeIntervalSince1970: 1_794_132_000) // 2026-11-08T10:00:00Z

    // MARK: The wire

    @Test func aCardSaysTheAssistantIsLookingAndWhatItFilledIn() throws {
        let json = #"{"id":"ix_1","machine_id":"m","session_id":"s","kind":"question","delegating":true,"delegate":{"what":"换数据库驱动","why_now":"第一批查询要按它写","recommend":"pgx","cost":"要重写查询","reason":"影响上线时间","deadline":"2026-11-08T10:00:00Z"}}"#
        let ix = try JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
        #expect(ix.isDelegating)
        #expect(ix.delegate == Delegate(what: "换数据库驱动", whyNow: "第一批查询要按它写", recommend: "pgx", cost: "要重写查询",
                                        reason: "影响上线时间", deadline: deadline))
        // The snapshot cache writes cards back: the context survives it.
        #expect(try JSON.decoder.decode(Interaction.self, from: JSON.encoder.encode(ix)) == ix)
        // An odd context never drops the card; only questions are ever delegated.
        let odd = try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"x","machine_id":"m","session_id":"s","kind":"question","delegate":{"what":3,"recommend":"A"}}"#.utf8))
        #expect(odd.delegate == Delegate(recommend: "A") && !odd.isDelegating)
        var permission = ix
        permission.kind = .permission
        #expect(!permission.isDelegating)
    }

    @Test func theResolvedEventCarriesTheAssistantsAnswer() {
        let answered = Data(#"{"type":"interaction_resolved","id":"ix_1","answered_by":"assistant","reason":"现有代码都用 pgx","answers":{"用哪个库？":"pgx","n":3}}"#.utf8)
        guard case .interactionResolved(let id, let assistant)? = ServerEvent.decode(answered) else { Issue.record("not resolved"); return }
        #expect(id == "ix_1")
        #expect(assistant == AssistantAnswer(answers: ["用哪个库？": "pgx"], reason: "现有代码都用 pgx"))
        guard case .interactionResolved(_, let none)? = ServerEvent.decode(Data(#"{"type":"interaction_resolved","id":"ix_2"}"#.utf8)) else {
            Issue.record("not resolved"); return
        }
        #expect(none == nil)
        let changed = Data(#"{"type":"interaction_reanswered","id":"ix_1","resolution":{"answers":{"用哪个库？":"sqlc"}}}"#.utf8)
        guard case .interactionReanswered(let rid, let answers)? = ServerEvent.decode(changed) else { Issue.record("not reanswered"); return }
        #expect(rid == "ix_1" && answers == ["用哪个库？": "sqlc"])
    }

    // MARK: The context and the recommended options

    @Test func theContextIsListedInAFixedOrderWithoutEmptyFields() {
        let lines = Delegated.lines(Delegate(what: "换数据库驱动", whyNow: "  ", recommend: "pgx", cost: "要重写查询", reason: "影响上线时间",
                                             deadline: deadline))
        #expect(lines.map(\.field) == [.what, .recommend, .cost, .reason, .deadline])
        #expect(lines[0].label == "这件事" && lines[0].text == "换数据库驱动")
        #expect(lines[4].text == deadline.formatted(date: .abbreviated, time: .shortened))
        #expect(Delegated.lines(nil).isEmpty)
        #expect(Delegated.lines(Delegate(what: " ", deadline: Date(timeIntervalSince1970: 0))).isEmpty)
    }

    @Test func theRecommendationShowsOnTheOptionsItNames() {
        #expect(Delegated.recommendedLabels([q("用哪个库？", ["pgx", "sqlc"])], "sqlc") == [["sqlc"]])
        #expect(Delegated.recommendedLabels([q("改哪些？", ["A", "B", "C"], multi: true)], "A, C") == [["A", "C"]])
        let two = [q("库？", ["pgx", "sqlc"]), q("先测吗？", ["是", "否"])]
        #expect(Delegated.recommendedLabels(two, "pgx；否") == [["pgx"], ["否"]])
        #expect(Delegated.recommendedLabels(two, "pgx") == [[], []], "one answer for two questions names none")
        #expect(Delegated.recommendedLabels([q("用哪个库？", ["pgx", "sqlc"])], "先别换，等压测") == [[]])
        #expect(Delegated.recommendedLabels([q("用哪个库？", ["pgx", "sqlc"])], nil) == [[]])
        // On the options, the recommendation is not listed again in the context; in its own words, it is.
        let onOptions = Delegated.context(card(delegate: Delegate(what: "换驱动", recommend: "pgx")))
        #expect(onOptions.lines.map(\.field) == [.what] && onOptions.picks == [["pgx"]])
        let ownWords = Delegated.context(card(delegate: Delegate(recommend: "先别换")))
        #expect(ownWords.lines.map(\.field) == [.recommend] && ownWords.picks == [[]])
    }

    // MARK: Change Answer's form

    @Test func theFormStartsFromTheAnswersSoFar() {
        let qs = [q("库？", ["pgx", "sqlc"]), q("改哪些？", ["A", "B"], multi: true), q("备注？", ["无"])]
        let start = Delegated.initialPicks(qs, ["库？": "pgx", "改哪些？": "A, B", "备注？": "周五前"])
        #expect(start.picked == [0: ["pgx"], 1: ["A", "B"], 2: []])
        #expect(start.custom == [2: "周五前"])
        #expect(Delegated.formAnswers(qs, picked: [0: ["sqlc"], 1: ["B", "A"]], custom: [2: " 下周 "])
                == ["库？": "sqlc", "改哪些？": "B, A", "备注？": "下周"])
    }

    @Test func onlyAnswersThatSaySomethingElseGo() {
        #expect(Delegated.changedAnswers(before: ["库？": "pgx"], after: ["库？": " pgx "]).isEmpty)
        #expect(Delegated.changedAnswers(before: ["库？": "pgx"], after: ["库？": "sqlc"]) == ["库？": "sqlc"])
        #expect(Delegated.changedAnswers(before: ["库？": "pgx"], after: ["库？": ""]).isEmpty, "an emptied answer is no answer")
        let multi = [q("改哪些？", ["A", "B", "C"], multi: true)]
        #expect(Delegated.changedAnswers(before: ["改哪些？": "A, B"], after: ["改哪些？": "B, A"], questions: multi).isEmpty)
        #expect(Delegated.changedAnswers(before: ["改哪些？": "A, B"], after: ["改哪些？": "A, C"], questions: multi) == ["改哪些？": "A, C"])
        // A single choice whose label has a comma, and words of the user's own, are compared as written.
        #expect(!Delegated.changedAnswers(before: ["库？": "A, B"], after: ["库？": "B, A"], questions: [q("库？", ["A, B", "B, A"])]).isEmpty)
        #expect(!Delegated.changedAnswers(before: ["改哪些？": "先 A, 再 B"], after: ["改哪些？": "再 B, 先 A"], questions: multi).isEmpty)
    }

    @Test func aChangeOfAnswerSaysWhyItDidNotGo() {
        #expect(Delegated.reanswerError(APIError(status: 403, message: "x")) == "这台设备只能按卡片上的选项回答，不能改答")
        #expect(Delegated.reanswerError(APIError(status: 409, message: "x")) == "这个问题不是助手代答的，不能在这里改答")
        #expect(Delegated.reanswerError(APIError(status: 400, message: "x")) == "先选一个答案")
        #expect(Delegated.reanswerError(APIError(status: 502, message: "node unreachable")) == "新答案没能发给 Agent：node unreachable")
        #expect(Delegated.reanswerError(APIError(status: 503, message: "x", fields: ["code": "assistant_offline"])) == "助手不在线，稍后再试")
        #expect(Delegated.reanswerError(APIError(status: 404, message: "404 page not found")) == "这个 hub 还不支持改答，先更新 hub")
        #expect(Delegated.reanswerError(APIError(status: 404, message: "gone", fields: ["code": "not_found"])) == "没能改答：gone")
        #expect(Delegated.reanswerError(APIError(status: 500, message: "boom")) == "没能改答：boom")
    }

    // MARK: The app

    @Test func aLookingCardIsNoNewsUntilItIsHandedOver() {
        let looking = card(delegating: true)
        #expect(!Delegated.announces(looking, replacing: nil))
        let handed = card(delegate: Delegate(recommend: "pgx"))
        #expect(Delegated.announces(handed, replacing: looking))
        #expect(!Delegated.announces(handed, replacing: handed), "the same card again")
        #expect(Delegated.announces(card(), replacing: nil))

        let model = AppModel()
        model.handle(.interaction(looking), group: "")
        #expect(model.pendingInteractions.map(\.isDelegating) == [true])
        model.handle(.interaction(handed), group: "")
        #expect(model.pendingInteractions.count == 1)
        #expect(!model.pendingInteractions[0].isDelegating && model.pendingInteractions[0].delegate?.recommend == "pgx")
    }

    @Test func aQuestionTheAssistantAnsweredStaysInItsChat() {
        let model = AppModel()
        let looking = card(delegating: true)
        model.handle(.interaction(looking), group: "")
        model.handle(.interactionResolved("ix_1", assistant: AssistantAnswer(answers: ["用哪个库？": "pgx"], reason: "现有代码都用 pgx")), group: "")
        #expect(model.pendingInteractions.isEmpty)
        let kept = model.delegated(for: looking.sessionKey)
        #expect(kept.map(\.answers) == [["用哪个库？": "pgx"]] && kept.first?.reason == "现有代码都用 pgx")
        // Answered by anyone else: nothing is kept.
        model.handle(.interaction(card("ix_2")), group: "")
        model.handle(.interactionResolved("ix_2", assistant: nil), group: "")
        #expect(model.delegated(for: looking.sessionKey).count == 1)
        // Another group's question, by its group.
        var other = card()
        other.group = "work"
        model.handle(.interaction(other), group: "work")
        model.handle(.interactionResolved("ix_1", assistant: AssistantAnswer(answers: ["用哪个库？": "sqlc"], reason: "")), group: "work")
        #expect(model.delegated(for: other.sessionKey).map(\.id) == ["work|ix_1"])
        #expect(model.delegated(for: looking.sessionKey).map(\.id) == ["ix_1"])
        model.dropDelegated("ix_1")
        #expect(model.delegated(for: looking.sessionKey).isEmpty)
    }

    @Test func atMostFiftyAreKeptTheOldestPutAwayFirst() {
        let model = AppModel()
        for n in 0...Delegated.kept {
            model.handle(.interaction(card("ix_\(n)")), group: "")
            model.handle(.interactionResolved("ix_\(n)", assistant: AssistantAnswer(answers: [:], reason: "")), group: "")
        }
        let kept = model.delegated(for: card().sessionKey)
        #expect(kept.count == Delegated.kept && kept.first?.id == "ix_1" && kept.last?.id == "ix_\(Delegated.kept)")
    }

    @Test func thisDevicesAnswerThatCameSecondShowsTheAssistants() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/interactions/ix_1/resolve", json: #"""
        {"ok":true,"already":true,"status":"resolved","resolution":{"answers":{"用哪个库？":"pgx"},"answered_by":"assistant","reason":"现有代码都用 pgx"}}
        """#)
        let model = AppModel()
        model.apiForTests = hub.client
        let ix = card(session: "")
        model.replaceInteractions("", [ix])
        await model.resolve(ix, Resolution(answers: ["用哪个库？": "sqlc"]))
        #expect(model.pendingInteractions.isEmpty)
        #expect(model.delegated(for: ix.sessionKey).map(\.answers) == [["用哪个库？": "pgx"]])
        #expect(model.toast == nil, "no Already Answered toast: the card says it")
    }

    @Test func changeAnswerSendsWhatChangedAndShowsIt() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/interactions/ix_1/reanswer", json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        let two = [q("用哪个库？", ["pgx", "sqlc"]), q("先测吗？", ["是", "否"])]
        model.handle(.interaction(card(questions: two)), group: "")
        model.handle(.interactionResolved("ix_1", assistant: AssistantAnswer(answers: ["用哪个库？": "pgx", "先测吗？": "是"], reason: "")), group: "")
        let d = try #require(model.delegated(for: card().sessionKey).first)

        #expect(await model.reanswer(d, answers: ["用哪个库？": "sqlc"]) == nil)
        let body = try #require(hub.sent("POST", "/api/v1/interactions/ix_1/reanswer").first?.body)
        #expect(try JSONDecoder().decode([String: [String: String]].self, from: body) == ["answers": ["用哪个库？": "sqlc"]])
        #expect(Delegated.latest(d, model.reanswered[d.id]) == ["用哪个库？": "sqlc", "先测吗？": "是"])
        // Another device changes the other question: the hub says only that one, and the first change stays.
        model.handle(.interactionReanswered(id: "ix_1", answers: ["先测吗？": "否"]), group: "")
        #expect(model.reanswered[d.id] == ["用哪个库？": "sqlc", "先测吗？": "否"])
    }

    @Test func aChangeOfAnswerThatFailedChangesNothing() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/interactions/ix_1/reanswer") { _ in
            throw APIError(status: 503, message: "assistant offline", fields: ["code": "assistant_offline"])
        }
        let model = AppModel()
        model.apiForTests = hub.client
        model.handle(.interaction(card()), group: "")
        model.handle(.interactionResolved("ix_1", assistant: AssistantAnswer(answers: ["用哪个库？": "pgx"], reason: "")), group: "")
        let d = try #require(model.delegated(for: card().sessionKey).first)
        #expect(await model.reanswer(d, answers: ["用哪个库？": "sqlc"]) == "助手不在线，稍后再试")
        #expect(model.reanswered[d.id] == nil)
    }

    @Test func changeAnswerTakesWhatSendingDoes() {
        let model = AppModel()
        let d = DelegatedAnswer(interaction: card(), answers: [:], reason: "", at: nil)
        #expect(!model.canChangeAnswer(d), "its session not known yet")
        model.applySessions([MachineRef(id: "m"): [session(send: false)]])
        #expect(!model.canChangeAnswer(d))
        model.applySessions([MachineRef(id: "m"): [session(send: true)]])
        #expect(model.canChangeAnswer(d))
    }

    @Test func sessionInfoListsTheQuestionsTheAssistantAnswered() async {
        let row = { (id: String, at: String, kind: String, resolution: String) in
            #"{"interaction":{"id":"\#(id)","machine_id":"m","session_id":"s","kind":"\#(kind)","questions":[{"question":"用哪个库？","options":[{"label":"pgx"}]}]},"status":"resolved","resolution":\#(resolution),"resolved_at":"\#(at)"}"#
        }
        let rows = [
            row("a", "2026-11-07T10:00:00Z", "question", #"{"answers":{"用哪个库？":"pgx"},"answered_by":"assistant","reason":"现有代码都用 pgx"}"#),
            row("b", "2026-11-07T11:00:00Z", "question", #"{"answers":{"用哪个库？":"sqlc"}}"#),
            row("c", "2026-11-07T12:00:00Z", "question", #"{"answers":{"用哪个库？":"sqlc"},"answered_by":"assistant"}"#),
            row("d", "2026-11-07T13:00:00Z", "permission", #"{"allow":true,"answered_by":"assistant"}"#),
            #"{"interaction":{"id":5}}"#,
        ]
        let hub = FakeHub()
        hub.on("GET", "/api/v1/interactions/history", json: #"{"interactions":["# + rows.joined(separator: ",") + "]}")
        let page = DelegatedHistoryModel()
        let key = SessionKey(machineID: "m", sessionID: "s")
        await page.load(key, api: hub.client)
        #expect(page.rows?.map(\.interaction.id) == ["c", "a"])
        #expect(page.rows?.last?.reason == "现有代码都用 pgx" && page.rows?.last?.answers == ["用哪个库？": "pgx"])
        #expect(hub.sent("GET", "/api/v1/interactions/history").first?.query == ["machine_id": "m", "session_id": "s", "limit": "500"])
        // A hub without the route: no section and no error.
        let old = DelegatedHistoryModel()
        await old.load(key, api: FakeHub().client)
        #expect(old.rows?.isEmpty == true && old.error == nil)
    }

    /// With several managers the hub routes an answered question by the manager a request names (docs/protocol/protocol.md
    /// "Per-manager routing"): Change Answer names the card's, Session Info's history the session's.
    @Test func answeredQuestionsGoToTheirOwnManager() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/interactions/ix_1/reanswer", json: #"{"ok":true}"#)
        hub.on("GET", "/api/v1/interactions/history", json: #"{"interactions":[{"interaction":{"id":"h","machine_id":"m","session_id":"s","kind":"question","questions":[{"question":"用哪个库？","options":[{"label":"pgx"}]}]},"status":"resolved","resolution":{"answers":{"用哪个库？":"pgx"},"answered_by":"assistant"},"resolved_at":"2026-11-07T10:00:00Z"}]}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        let json = #"{"id":"ix_1","machine_id":"m","session_id":"s","kind":"question","manager":"m-2","questions":[{"question":"用哪个库？","options":[{"label":"pgx"},{"label":"sqlc"}]}]}"#
        let ix = try JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
        #expect(ix.manager == "m-2")
        model.handle(.interaction(ix), group: "")
        model.handle(.interactionResolved("ix_1", assistant: AssistantAnswer(answers: ["用哪个库？": "pgx"], reason: "")), group: "")
        let d = try #require(model.delegated(for: ix.sessionKey).first)
        #expect(await model.reanswer(d, answers: ["用哪个库？": "sqlc"]) == nil)
        #expect(hub.sent("POST", "/api/v1/interactions/ix_1/reanswer").first?.query == ["manager": "m-2"])

        let owned = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"m","session_id":"s","manager":"m-2"}"#.utf8))
        #expect(owned.manager == "m-2" && session(send: true).manager == nil)
        let page = DelegatedHistoryModel()
        await page.load(ix.sessionKey, manager: owned.manager, api: hub.client)
        #expect(hub.sent("GET", "/api/v1/interactions/history").first?.query["manager"] == "m-2")
        #expect(page.rows?.first?.interaction.manager == "m-2", "changed from Session Info, it names the same manager")

        // One manager, or an older hub: nothing named, the default manager answers.
        let plain = FakeHub()
        plain.on("POST", "/api/v1/interactions/ix_1/reanswer", json: #"{"ok":true}"#)
        model.apiForTests = plain.client
        var unowned = d
        unowned.interaction.manager = nil
        #expect(await model.reanswer(unowned, answers: ["用哪个库？": "sqlc"]) == nil)
        #expect(plain.sent("POST", "/api/v1/interactions/ix_1/reanswer").first?.query.isEmpty == true)
    }

    /// The BeautifulUI card tags the options the assistant would pick; one the agent itself marked Recommended keeps that tag.
    @Test func theCardTagsTheAssistantsPick() {
        let card = BUIQuestionForm.card(q("用哪个库？", ["pgx (Recommended)", "sqlc", "gorm"]), picks: ["pgx (Recommended)", "sqlc"])
        #expect(card.choices.map(\.tag) == ["推荐", "助手推荐", nil])
        #expect(BUIQuestionForm.card(q("用哪个库？", ["pgx", "sqlc"])).choices.allSatisfy { $0.tag == nil })
    }

    /// The watch's own pull leaves out a question the assistant is still looking at and buzzes once it is handed over.
    @Test func theWatchWaitsUntilTheQuestionIsHandedOver() {
        #expect(WatchInteractions.leavingMuted([card(delegating: true)]).isEmpty)
        let handed = WatchInteractions.leavingMuted([card()])
        #expect(WatchInteractions.newOnes(in: handed, alreadyShown: []).map(\.id) == ["ix_1"])
        #expect(WatchInteractions.newOnes(in: handed, alreadyShown: handed).isEmpty)
        // Only questions are delegated: a permission card marked so still shows.
        var permission = card(delegating: true)
        permission.kind = .permission
        #expect(WatchInteractions.leavingMuted([permission]).map(\.id) == ["ix_1"])
    }
}
