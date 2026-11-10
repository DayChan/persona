import Foundation
import Testing
@testable import Persona

/// The main conversation's readiness (docs/assistant/onboarding.md 4.4, 5.1, 5.2; docs/protocol/protocol.md「Main chat readiness」) against FakeHub:
/// decoding, the step a card shows, what holds the send button, warm's answer, dismiss, coded refusals as cards.
@MainActor
struct AssistantReadinessTests {
    static let notLoggedIn = #"""
    {"version":1,"mode":"setup","ready":false,"next":"model","machine":{"id":"mbp","name":"示例的 MacBook"},"launcher":"claude",
     "steps":[{"id":"computer","state":"ok","machine":"mbp"},{"id":"claude","state":"ok","version":"2.1.308"},
              {"id":"model","state":"problem","code":"not_logged_in","machine":"mbp","launcher":"claude","title":"Claude Code 还没登录",
               "detail":"在 示例的 MacBook 上登录一次，或者改用 API Key。","action":{"id":"connect_model"},"alt_actions":[{"id":"login_claude"}]},
              {"id":"first_message","state":"todo","code":"never_tried","title":"试一句","detail":"…","action":{"id":"test_assistant"}}],
     "suggestions":[{"id":"what_can_you_do","title":"你能帮我做什么？","local":true}],
     "probe":{"ok":false,"agent":"claude","millis":2140,"error":"Not logged in"},
     "dismissed":false,"checked_at":"2026-11-06T09:00:00Z","something_new":{"x":1}}
    """#

    private func decode(_ json: String) throws -> AssistantReadiness {
        try JSON.decoder.decode(AssistantReadiness.self, from: Data(json.utf8))
    }

    private func steps(_ states: [(String, String, String?)], mode: String, next: String? = nil) -> AssistantReadiness {
        AssistantReadiness(mode: mode, next: next, steps: states.map { .init(id: $0.0, state: $0.1, code: $0.2, title: "t-\($0.0)") })
    }

    @Test func decodesAWholeReadiness() throws {
        let r = try decode(Self.notLoggedIn)
        #expect(r.mode == "setup" && r.next == "model" && r.machine?.name == "示例的 MacBook" && r.launcher == "claude")
        #expect(r.steps.map(\.id) == ["computer", "claude", "model", "first_message"])
        let model = try #require(r.step("model"))
        #expect(model.code == "not_logged_in" && model.action?.id == "connect_model" && model.altActions?.map(\.id) == ["login_claude"])
        #expect(r.suggestions?.first?.local == true && r.probe?.millis == 2140 && r.dismissed == false)
        #expect(r.blockingStep?.id == "model" && r.cardStep?.id == "model" && r.stepsLeft == 1 && !r.claudeWorks)
    }

    @Test func unknownValuesStillDecode() throws {
        let r = try decode(#"""
        {"mode":"setup","next":"wifi","steps":[{"id":"computer","state":"ok"},{"id":"claude","state":"weird"},
          {"id":"model","state":"ok"},{"id":"wifi","state":"problem","code":"no_wifi","title":"接上网络","action":{"id":"fix_wifi"}}]}
        """#)
        // an unknown state is not a known pass: it holds the send like todo (One Step Left + its title)
        #expect(r.blockingStep?.id == "claude")
        let r2 = try decode(#"""
        {"mode":"setup","next":"wifi","steps":[{"id":"computer","state":"ok"},{"id":"claude","state":"ok"},
          {"id":"model","state":"ok"},{"id":"wifi","state":"problem","code":"no_wifi","title":"接上网络","action":{"id":"fix_wifi"}}]}
        """#)
        // a newer hub's step that `next` names
        #expect(r2.blockingStep?.id == "wifi")
        #expect(!AssistantReadiness.knows(action: "fix_wifi") && ReadinessRoute.title("fix_wifi") == nil)
        #expect(AssistantReadiness.knows(action: "connect_model") && ReadinessRoute.title("connect_model") != nil)
    }

    @Test func whatHoldsTheSendButton() {
        // full sends; unknown / checking never block; a rate limit shows a card but never turns send
        #expect(steps([("computer", "ok", nil), ("claude", "ok", nil), ("model", "ok", nil)], mode: "full").blockingStep == nil)
        #expect(steps([("computer", "ok", nil), ("claude", "checking", nil), ("model", "unknown", "model_unverified")], mode: "setup").blockingStep == nil)
        let limited = steps([("computer", "ok", nil), ("claude", "ok", nil), ("model", "problem", "rate_limited")], mode: "full")
        #expect(limited.blockingStep == nil && limited.cardStep?.code == "rate_limited")
        let zero = steps([("computer", "todo", "no_machine"), ("claude", "unknown", nil), ("model", "unknown", nil)], mode: "setup")
        #expect(zero.blockingStep?.id == "computer" && zero.stepsLeft == 3)
        #expect(ReadinessRoute.sendTitle(zero.blockingStep!) == "先添加电脑")
        #expect(ReadinessRoute.sendTitle(.init(id: "computer", state: "problem", code: "node_unreachable")) == "先唤醒电脑")
        #expect(ReadinessRoute.sendTitle(.init(id: "computer", state: "problem", code: "machine_pending")) == "先确认电脑")
        #expect(ReadinessRoute.sendTitle(.init(id: "model", state: "problem", code: "auth_failed")) == "先接上模型")
        // chat_only (P1): sends as before
        #expect(steps([("computer", "todo", "no_machine")], mode: "chat_only").blockingStep == nil)
    }

    @Test func refreshAndProbeAndDismissRequests() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/readiness") { req in Data((req.query["probe"] == "1" ? Self.notLoggedIn : Self.notLoggedIn).utf8) }
        hub.on("POST", "/api/v1/assistant/readiness/dismiss", json: #"{"ok":true}"#)
        let m = AssistantReadinessModel()
        await m.refresh(api: hub.client, force: true)
        #expect(m.current?.next == "model" && m.blocking?.id == "model" && m.probe == nil)
        #expect(hub.sent("GET", "/api/v1/assistant/readiness").last?.query["probe"] == nil)

        await m.refresh(api: hub.client, probe: true)
        #expect(hub.sent("GET", "/api/v1/assistant/readiness").last?.query["probe"] == "1")
        #expect(m.probe?.ok == false && m.probe?.error == "Not logged in" && !m.probing)

        // a fresh answer is not asked again on a visit
        let asked = hub.sent("GET", "/api/v1/assistant/readiness").count
        await m.refresh(api: hub.client)
        #expect(hub.sent("GET", "/api/v1/assistant/readiness").count == asked)

        await m.dismiss(api: hub.client)
        let body = try #require(try JSON.decoder.decode(JSONValue.self, from: hub.sent("POST", "/api/v1/assistant/readiness/dismiss").first?.body ?? Data()).object)
        #expect(body["dismissed"] == .bool(true) && m.current?.dismissed == true)
    }

    @Test func probeUsesTheHubsWholeCheckBudget() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/readiness", json: Self.notLoggedIn)
        _ = try await hub.client.assistantReadiness(probe: true)
        #expect(hub.sent("GET", "/api/v1/assistant/readiness").last?.timeout == 130)
        _ = try await hub.client.assistantReadiness()
        #expect(hub.sent("GET", "/api/v1/assistant/readiness").last?.timeout == 30)
    }

    @Test func aFailedProbeSaysSoAndNeverReusesTheLastResult() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/readiness", json: Self.notLoggedIn)
        let m = AssistantReadinessModel()
        await m.refresh(api: hub.client, probe: true)
        #expect(m.probe?.error == "Not logged in" && m.probeError == nil)

        hub.on("GET", "/api/v1/assistant/readiness") { _ in throw URLError(.timedOut) }
        await m.refresh(api: hub.client, probe: true)
        #expect(m.probe == nil, "the previous try's answer cannot stand in for this one")
        #expect(m.probeError?.isEmpty == false && !m.probing, "a timeout still says so")

        hub.on("GET", "/api/v1/assistant/readiness") { _ in throw APIError(status: 502, message: "mock probe failed") }
        await m.refresh(api: hub.client, probe: true)
        #expect(m.probe == nil && m.probeError == "mock probe failed" && !m.probing)

        hub.on("GET", "/api/v1/assistant/readiness", json: Self.notLoggedIn)
        await m.refresh(api: hub.client, probe: true)
        #expect(m.probeError == nil && m.probe?.error == "Not logged in" && !m.probing)
    }

    @Test func onlyTheLatestProbeCanLandOrStopTheSpinner() async {
        let answers = ["old A", "new B", "old C", "new D"].map {
            Data(Self.notLoggedIn.replacingOccurrences(of: "Not logged in", with: $0).utf8)
        }
        let gate = HeldProbes(answers: answers)
        let client = APIClient(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "t"),
                               transport: { _ in await gate.answer() })
        let m = AssistantReadinessModel()

        let a = Task { await m.refresh(api: client, probe: true) }
        await gate.waitForArrival(0)
        let b = Task { await m.refresh(api: client, probe: true) }
        await gate.waitForArrival(1)
        await gate.release(0)
        await a.value
        #expect(m.probing, "an older try cannot stop the newer try's spinner")
        #expect(m.probe == nil, "an older try cannot land while the newer one is still running")
        await gate.release(1)
        await b.value
        #expect(!m.probing && m.probe?.error == "new B")

        let c = Task { await m.refresh(api: client, probe: true) }
        await gate.waitForArrival(2)
        let d = Task { await m.refresh(api: client, probe: true) }
        await gate.waitForArrival(3)
        await gate.release(3)
        await d.value
        #expect(m.probe?.error == "new D")
        await gate.release(2)
        await c.value
        #expect(m.probe?.error == "new D", "a late answer from an older try cannot replace the newer result")
        #expect(!m.probing)
    }

    @Test func anOlderHubAnswering404ShowsNothing() async {
        let hub = FakeHub() // no route: 404
        let m = AssistantReadinessModel()
        await m.refresh(api: hub.client, force: true)
        #expect(m.unsupported && m.current == nil && m.blocking == nil)
    }

    @Test func a404RetiresEveryProbeDisplay() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/readiness") { _ in throw APIError(status: 502, message: "mock probe failed") }
        let m = AssistantReadinessModel()
        await m.refresh(api: hub.client, probe: true)
        #expect(m.probeError == "mock probe failed")

        hub.on("GET", "/api/v1/assistant/readiness") { _ in throw APIError(status: 404, message: "old hub") }
        await m.refresh(api: hub.client, force: true)
        #expect(m.unsupported && m.current == nil)
        #expect(m.probe == nil && m.probeError == nil && !m.probing, "an old hub shows no probe result or error")
    }

    @Test func aCancelledProbeIsNotAVisibleFailure() async {
        let hub = FakeHub()
        let m = AssistantReadinessModel()
        hub.on("GET", "/api/v1/assistant/readiness") { _ in throw URLError(.cancelled) }
        await m.refresh(api: hub.client, probe: true)
        #expect(m.probe == nil && m.probeError == nil && !m.probing)

        hub.on("GET", "/api/v1/assistant/readiness") { _ in throw CancellationError() }
        await m.refresh(api: hub.client, probe: true)
        #expect(m.probe == nil && m.probeError == nil && !m.probing)
    }

    @Test func warmCarriesTheReadiness() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/warm", json: #"{"ok":true,"readiness":\#(Self.notLoggedIn)}"#)
        let r = try await hub.client.warmAssistant(launcher: nil)
        #expect(r?.step("model")?.code == "not_logged_in")
        hub.on("POST", "/api/v1/assistant/warm", json: #"{"ok":true}"#)
        #expect(try await hub.client.warmAssistant(launcher: "claude") == nil)
    }

    @Test func aCoded409IsACardNotQuiet() throws {
        let e = APIError(status: 409, message: "Persona 要在你的一台电脑上干活，现在还没接上电脑。",
                         fields: ["code": "no_machine", "error": "Persona 要在你的一台电脑上干活，现在还没接上电脑。",
                                  "detail": "assistant machine is not configured", "action": "add_computer", "delivered": "none",
                                  "request_id": "req-1"])
        #expect(!e.isQuiet)
        let f = try #require(CodedFailure(e))
        #expect(f.code == "no_machine" && f.action == "add_computer" && f.detail == "assistant machine is not configured" && f.requestID == "req-1")
        // words with 「超时」 in a coded refusal are not a timeout to hide
        let slow = APIError(status: 409, message: "检查 Claude Code 超时了", fields: ["code": "claude_missing", "action": "install_claude"])
        #expect(CodedFailure(slow)?.action == "install_claude")
        // an offline computer gets its wake button; an uncoded error keeps the toast
        #expect(CodedFailure(APIError(status: 502, message: "x", fields: ["code": "node_unreachable"]))?.action == "wake_computer")
        #expect(CodedFailure(APIError(status: 500, message: "boom")) == nil)
        #expect(CodedFailure(APIError(status: 409, message: "x", fields: ["code": "input_draft"])) == nil)
        #expect(CodedFailure.passesBySelf(APIError(status: 503, message: "x", fields: ["code": "assistant_offline"])))
    }

    @Test func aCodedRefusalBecomesTheCardNotAToast() {
        let model = AppModel.shared
        model.toast = nil
        model.readiness.failure = nil
        model.show(APIError(status: 409, message: "那台电脑上没装 Claude Code", fields: ["code": "claude_missing", "action": "install_claude"]))
        #expect(model.readiness.failure?.code == "claude_missing" && model.toast == nil)
        model.readiness.failure = nil
    }

    @Test func turnErrorCodesBecomeFixes() throws {
        #expect(TurnErrorFix(code: "not_logged_in")?.action == "connect_model")
        #expect(TurnErrorFix(code: "auth_failed")?.action == "edit_provider")
        #expect(TurnErrorFix(code: "model_route")?.action == "connect_model")
        #expect(TurnErrorFix(code: "rate_limited") == nil && TurnErrorFix(code: nil) == nil)
        let s = try JSON.decoder.decode(Session.self, from: Data(#"""
        {"machine_id":"mbp","session_id":"s1","title":"","cwd":"","status":"idle","started_by_persona":true,"assistant":true,
         "turn_error":"Not logged in · Please run /login","turn_error_kind":"model","turn_error_code":"not_logged_in"}
        """#.utf8))
        #expect(s.turnErrorCode == "not_logged_in" && s.turnErrorKind == "model" && s.turnError?.hasPrefix("Not logged in") == true)
    }
}

/// Probe answers released individually, so a test controls which overlapping try finishes first.
private actor HeldProbes {
    private let answers: [Data]
    private var arrivals = 0
    private var held: [Int: CheckedContinuation<Void, Never>] = [:]
    private var waiting: [Int: [CheckedContinuation<Void, Never>]] = [:]

    init(answers: [Data]) { self.answers = answers }

    func answer() async -> Data {
        let mine = arrivals
        arrivals += 1
        for waiter in waiting.removeValue(forKey: mine) ?? [] { waiter.resume() }
        await withCheckedContinuation { held[mine] = $0 }
        return answers[mine]
    }

    func waitForArrival(_ index: Int) async {
        if arrivals > index { return }
        await withCheckedContinuation { waiting[index, default: []].append($0) }
    }

    func release(_ index: Int) {
        held[index]?.resume()
        held[index] = nil
    }
}
