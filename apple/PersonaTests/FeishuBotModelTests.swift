import Foundation
import Testing
@testable import Persona

@MainActor struct FeishuBotModelTests {
    private static let status = """
    {"enabled":true,"app_id":"cli_a","domain":"https://open.feishu.cn","has_secret":true,"from_config":false,
     "features":{"main":true,"cards":true,"all_cards":false,"pushes":false,"sync":false,"agents":false},
     "connected":true,"bot_name":"Persona","owner":{"name":"我","paired_at":1}}
    """

    private func model(_ hub: FakeHub, toasts: BotRecorder<[String]> = BotRecorder([])) -> FeishuBotModel {
        FeishuBotModel(deps: .init(api: { hub.client }, toast: { toasts.value.append($0.message) }))
    }

    @Test func brandMigrationDoesNotRewriteTheConfiguredBotName() async throws {
        for name in ["Persona", "Persona", "我的助手"] {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/bots/feishu", json: Self.status.replacingOccurrences(of: "\"bot_name\":\"Persona\"", with: "\"bot_name\":\"\(name)\""))
            let m = model(hub)
            await m.load()
            #expect(m.status?.botName == name)
        }
    }

    @Test func loadsAndSummarizes() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.status)
        let m = model(hub)
        await m.load()
        #expect(m.status?.botName == "Persona")
        #expect(m.appID == "cli_a" && m.domain == "feishu" && m.appSecret.isEmpty)
        #expect(!m.credentialsChanged)
        #expect(m.stateText == "已连接")
        #expect(FeishuBotModel.summary(m.status) == "已连接")
        var unpaired = try #require(m.status)
        unpaired.owner = nil
        #expect(FeishuBotModel.summary(unpaired) == "待配对")
        unpaired.enabled = false
        #expect(FeishuBotModel.summary(unpaired) == "未打开")
        #expect(FeishuBotModel.domainChoice("https://larksuite.com") == "lark")
    }

    /// A hub from before 0.1.12 replaces the whole `features` object it is sent (no `reports` in its status): a switch sends
    /// every feature to it, as it always did. "all cards" brings "cards" along, and turning "cards" off takes "all cards" with it.
    @Test func anOlderHubThatReplacesTheObjectGetsEverySwitch() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.status)
        var bodies: [[String: Any]] = []
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:])
            return Data(Self.status.utf8)
        }
        let m = model(hub)
        await m.load()
        #expect(m.features.hubMergesFeatures == false)
        await m.set(\.cards, false)
        var f = try #require(bodies.last?["features"] as? [String: Bool])
        #expect(f["cards"] == false && f["all_cards"] == false && f["main"] == true)
        #expect(f.count == 7) // main cards all_cards pushes sync agents reports; no board, the hub doesn't know it
        await m.set(\.allCards, true)
        f = try #require(bodies.last?["features"] as? [String: Bool])
        #expect(f["cards"] == true && f["all_cards"] == true)
        #expect(bodies.allSatisfy { $0["app_secret"] == nil })
    }

    /// The status of a hub that has the waiting-for-you board (#6052) and the nudge setting (#6084).
    private static let newerStatus = status
        .replacingOccurrences(of: "\"agents\":false}", with: "\"agents\":false,\"reports\":true,\"board\":false}")
        .replacingOccurrences(of: "\"connected\":true", with: "\"nudge_after\":30,\"connected\":true")

    /// A hub from before leaves both out: nothing to show for them, and nothing of them sent back.
    @Test func anOlderHubHasNeitherTheBoardNorTheNudge() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.status)
        var bodies: [[String: Any]] = []
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:])
            return Data(Self.status.utf8)
        }
        let m = model(hub)
        await m.load()
        #expect(!m.hubKnowsBoard && !m.hubKnowsNudge)
        #expect(m.nudgeAfter == 60 && !m.features.board)
        await m.set(\.pushes, true)
        let f = try #require(bodies.last?["features"] as? [String: Bool])
        #expect(f["pushes"] == true && f["board"] == nil && f["only_conclusions"] == nil)
        #expect(bodies.last?["nudge_after"] == nil)
    }

    /// The status of a hub that also has "only conclusions" (#1226).
    private static func newestStatus(onlyConclusions: Bool) -> String {
        newerStatus.replacingOccurrences(of: "\"board\":false}", with: "\"board\":false,\"only_conclusions\":\(onlyConclusions)}")
    }

    /// The switch shows what the hub has, on or off.
    @Test func onlyConclusionsReadsWhatTheHubHas() async throws {
        for on in [true, false] {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/bots/feishu", json: Self.newestStatus(onlyConclusions: on))
            let m = model(hub)
            await m.load()
            #expect(m.hubKnowsOnlyConclusions)
            #expect(m.features.onlyConclusions == on)
        }
    }

    /// Turning it on or off sends that switch alone, and the page shows what the hub answered; another switch does not
    /// carry it along.
    @Test func onlyConclusionsGoesAlone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newestStatus(onlyConclusions: false))
        var bodies: [[String: Any]] = []
        var hubOn = false
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:]
            bodies.append(body)
            hubOn = (body["features"] as? [String: Bool])?["only_conclusions"] ?? hubOn
            return Data(Self.newestStatus(onlyConclusions: hubOn).utf8)
        }
        let m = model(hub)
        await m.load()
        await m.set(\.onlyConclusions, true)
        #expect(bodies.count == 1 && bodies[0].count == 1)
        #expect(bodies[0]["features"] as? [String: Bool] == ["only_conclusions": true])
        #expect(m.features.onlyConclusions && m.error == nil)
        await m.set(\.reports, false)
        #expect(bodies.last?["features"] as? [String: Bool] == ["reports": false])
        await m.set(\.onlyConclusions, false)
        #expect(bodies.last?["features"] as? [String: Bool] == ["only_conclusions": false])
        #expect(!m.features.onlyConclusions)
    }

    /// A hub from before #1226 (it has the board and the nudge, not this switch) leaves it out: the page has no switch
    /// for it, reads it as off, and sends nothing of it with another switch.
    @Test func aHubWithoutOnlyConclusionsHidesItAndSendsNothingOfIt() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus)
        var bodies: [[String: Any]] = []
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:])
            return Data(Self.newerStatus.utf8)
        }
        let m = model(hub)
        await m.load()
        #expect(m.hubKnowsBoard && !m.hubKnowsOnlyConclusions && !m.features.onlyConclusions)
        await m.set(\.pushes, true)
        #expect(bodies.last?["features"] as? [String: Bool] == ["pushes": true])
        // Encoding the whole object (as for a hub that replaces it) leaves the unknown switch out too.
        let whole = try JSONSerialization.jsonObject(with: JSONEncoder().encode(m.features)) as? [String: Bool]
        #expect(whole?["only_conclusions"] == nil && whole?["board"] == false)
    }

    /// A hub with the board but not the nudge setting: the board switch shows, the picker does not.
    @Test func aHubWithTheBoardButNotTheNudgeShowsOnlyTheBoard() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus.replacingOccurrences(of: "\"nudge_after\":30,", with: ""))
        let m = model(hub)
        await m.load()
        #expect(m.hubKnowsBoard && !m.hubKnowsNudge)
        #expect(m.nudgeAfter == 60)
    }

    /// A hub that merges per switch gets the changed switch alone, whatever this page last saw of the others.
    @Test func aSwitchGoesAlone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus)
        var bodies: [[String: Any]] = []
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:])
            return Data(Self.newerStatus.replacingOccurrences(of: "\"board\":false", with: "\"board\":true").utf8)
        }
        let m = model(hub)
        await m.load()
        #expect(m.hubKnowsBoard && m.hubKnowsNudge && m.features.hubMergesFeatures && !m.features.board)
        await m.set(\.board, true)
        #expect(bodies.count == 1 && bodies[0].count == 1)
        #expect(bodies[0]["features"] as? [String: Bool] == ["board": true])
        #expect(m.features.board)
        await m.set(\.pushes, true)
        #expect(bodies.last?["features"] as? [String: Bool] == ["pushes": true]) // the board is not put back or sent
    }

    /// The page was opened with the board off; the board is turned on elsewhere; "Push to Lark" is turned on here. The
    /// request names "pushes" only, so the hub keeps the board as it is now (#74).
    @Test func aStaleModelDoesNotPutBackASwitchChangedElsewhere() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus) // board off
        var hubBoard = false
        var bodies: [[String: Any]] = []
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:]
            bodies.append(body)
            // The hub as it merges: the switches in the request over what it has now.
            let sent = body["features"] as? [String: Bool] ?? [:]
            let pushes = sent["pushes"] ?? false
            let board = sent["board"] ?? hubBoard
            return Data(Self.newerStatus
                .replacingOccurrences(of: "\"pushes\":false", with: "\"pushes\":\(pushes)")
                .replacingOccurrences(of: "\"board\":false", with: "\"board\":\(board)").utf8)
        }
        let m = model(hub)
        await m.load()
        #expect(!m.features.board)
        hubBoard = true // turned on in the web app after this page loaded
        await m.set(\.pushes, true)
        let sent = try #require(bodies.last?["features"] as? [String: Bool])
        #expect(sent == ["pushes": true])
        #expect(m.features.pushes && m.features.board) // the page shows what the hub answered
    }

    /// "all cards" and "cards" go together, and the hub does not tie them, so the request names both.
    @Test func theCardSwitchesNameTheirPartner() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus)
        var bodies: [[String: Any]] = []
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:])
            return Data(Self.newerStatus.utf8)
        }
        let m = model(hub)
        await m.load()
        await m.set(\.cards, false)
        #expect(bodies.last?["features"] as? [String: Bool] == ["cards": false, "all_cards": false])
        await m.set(\.allCards, true)
        #expect(bodies.last?["features"] as? [String: Bool] == ["all_cards": true, "cards": true])
        await m.set(\.allCards, false)
        #expect(bodies.last?["features"] as? [String: Bool] == ["all_cards": false])
        await m.set(\.cards, true)
        #expect(bodies.last?["features"] as? [String: Bool] == ["cards": true])
        await m.set(\.sync, true)
        #expect(bodies.last?["features"] as? [String: Bool] == ["sync": true])
        await m.set(\.agents, true)
        #expect(bodies.last?["features"] as? [String: Bool] == ["agents": true])
        await m.set(\.main, false)
        #expect(bodies.last?["features"] as? [String: Bool] == ["main": false])
        await m.set(\.reports, false)
        #expect(bodies.last?["features"] as? [String: Bool] == ["reports": false])
        await m.set(\.board, true)
        #expect(bodies.last?["features"] as? [String: Bool] == ["board": true])
    }

    /// The nudge delay goes alone, 0 included (0 is off, not absent); the picker lists the usual delays and the one in force.
    @Test func theNudgeDelayGoesAlone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus)
        var bodies: [[String: Any]] = []
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any] ?? [:])
            return Data(Self.newerStatus.replacingOccurrences(of: "\"nudge_after\":30", with: "\"nudge_after\":0").utf8)
        }
        let m = model(hub)
        await m.load()
        #expect(m.nudgeAfter == 30 && m.nudgeChoices == [0, 15, 30, 60, 120, 240, 480])
        await m.setNudgeAfter(0)
        #expect(bodies.count == 1 && bodies[0]["nudge_after"] as? Int == 0 && bodies[0].count == 1)
        #expect(m.nudgeAfter == 0 && m.error == nil)
    }

    @Test func aDelayFromTheAPIIsListedAndLabelled() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus.replacingOccurrences(of: "\"nudge_after\":30", with: "\"nudge_after\":90"))
        let m = model(hub)
        await m.load()
        #expect(m.nudgeChoices == [0, 15, 30, 60, 90, 120, 240, 480])
        #expect([0, 15, 60, 90, 120].map(FeishuBotModel.nudgeLabel) == ["关闭", "15 分钟", "1 小时", "90 分钟", "2 小时"])
    }

    /// A refused delay (out of range) leaves the page as it was and shows the hub's words.
    @Test func aRefusedNudgeDelayLeavesThePage() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.newerStatus)
        hub.on("PUT", "/api/v1/bots/feishu") { _ in throw APIError(status: 400, message: "最多 8640 分钟") }
        let m = model(hub)
        await m.load()
        await m.setNudgeAfter(9999)
        #expect(m.nudgeAfter == 30 && m.error?.contains("最多 8640 分钟") == true)
    }

    /// Saving credentials sends the secret once; what is typed is kept while a load arrives, and the hub's error shows.
    @Test func savesCredentialsAndShowsTheHubsError() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/bots/feishu", json: Self.status)
        let toasts = BotRecorder<[String]>([])
        let m = model(hub, toasts: toasts)
        await m.load()
        m.appID = "cli_b"
        m.appSecret = "sec"
        await m.load()
        #expect(m.appID == "cli_b" && m.appSecret == "sec") // not overwritten while typing
        hub.on("PUT", "/api/v1/bots/feishu") { _ in throw APIError(status: 400, message: "连不上这个应用") }
        await m.saveCredentials()
        #expect(m.error?.contains("连不上这个应用") == true)
        #expect(m.appSecret == "sec")
        hub.on("PUT", "/api/v1/bots/feishu") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any]
            #expect(body?["app_id"] as? String == "cli_b" && body?["app_secret"] as? String == "sec" && body?["domain"] as? String == "feishu")
            return Data(Self.status.replacingOccurrences(of: "cli_a", with: "cli_b").utf8)
        }
        await m.saveCredentials()
        #expect(m.error == nil && m.appSecret.isEmpty && m.appID == "cli_b")
        #expect(toasts.value == ["应用凭证已验证并保存"])
    }
}

/// A reference cell for what a closure records.
final class BotRecorder<T>: @unchecked Sendable {
    var value: T
    init(_ value: T) { self.value = value }
}
