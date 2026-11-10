import Foundation
import Testing
@testable import Persona

/// Settings › Sent as You (#5278, docs/extensions/connectors.md "Sent as the user"), the cases of the web page's `sent.test.ts`: the page as
/// the hub sends it, where a send went, what it said, how the gate let it through, who asked, paging and refreshing the
/// list, the connector chips, the hub's refusals in plain words, and the model over a fake hub, including the reads that come
/// back late. Made-up values; the tests run in Simplified Chinese.
@MainActor
struct SentModelTests {
    private func obj(_ json: String) -> [String: JSONValue] {
        (try? JSON.decoder.decode([String: JSONValue].self, from: Data(json.utf8))) ?? [:]
    }

    private func item(_ id: Int = 1, connector: String = "mail", action: String = "mail_send", target: String = "{}", text: String = "",
                      summary: String = "", origin: SentOrigin = SentOrigin(), through: SentLetThrough = SentLetThrough()) -> SentItem {
        SentItem(id: id, connector: connector, action: action, target: obj(target), text: text, summary: summary, origin: origin, letThrough: through)
    }

    private func ids(_ items: [SentItem]?) -> [Int] { (items ?? []).map(\.id) }

    // MARK: reading the ledger

    @Test func asksForAPageByTheCursorAndTheConnector() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/connectors/sent", json: #"{"items":[],"next":null}"#)
        let api = hub.client
        _ = try await api.sent()
        _ = try await api.sent(before: 41, connector: "mail")
        _ = try await api.sent(before: 0, limit: 200, connector: "")
        let asked = hub.sent("GET", "/api/v1/connectors/sent").map(\.query)
        #expect(asked == [[:], ["before": "41", "connector": "mail"], ["limit": "200"]])
    }

    @Test func takesAPageAsItComes() throws {
        let json = #"""
        {"items":[
          {"id":7,"at":"2026-11-05T12:00:00Z","connector":"github","target":null,"let_through":[],"origin":"no"},
          {"connector":"mail"}, null, 5, {"id":"9"},
          {"id":8,"at":"2026-11-05T12:01:00.123456789Z","connector":"mail","action":"mail_send",
           "target":{"to":["a@example.com"],"subject":"Hi"},"text":"x","summary":"s",
           "origin":{"machine_id":"mac","session_id":"s1","extension":"examplechat"},
           "let_through":{"kind":"card","interaction_id":"ix-1","answered_by":"assistant"},
           "message_ref":"m-1","recalled_at":"2026-11-05T13:00:00Z"}
        ],"next":7}
        """#
        let page = try JSON.decoder.decode(SentPage.self, from: Data(json.utf8))
        // a row with no number for an id is none, and the three objects are objects whatever the hub sent
        #expect(page.items.map(\.id) == [7, 8])
        #expect(page.next == 7)
        let bare = page.items[0]
        #expect(bare.connector == "github" && bare.target.isEmpty && bare.letThrough == SentLetThrough() && bare.origin == SentOrigin())
        #expect(bare.at == Date(timeIntervalSince1970: 1_793_880_000))
        let full = page.items[1]
        #expect(full.origin == SentOrigin(machineID: "mac", sessionID: "s1", extensionID: "examplechat"))
        #expect(full.letThrough == SentLetThrough(kind: "card", interactionID: "ix-1", answeredBy: "assistant"))
        #expect(full.messageRef == "m-1" && full.recalledAt != nil && full.at != nil)
        #expect(SentText.targetText(full) == "a@example.com · Hi")
    }

    @Test func jsonThatIsNotAnObjectIsAnEmptyPageAndAnythingThatIsNotJsonIsAnError() async throws {
        let hub = FakeHub()
        let answer = Flag()
        hub.on("GET", Self.path) { _ in Data((answer.on ? "not json at all" : "null").utf8) }
        let api = hub.client
        #expect(try await api.sent() == SentPage())
        answer.on = true
        await #expect(throws: (any Error).self) { try await api.sent() }
    }

    @Test func theLastPageHasNoNextAndAnEmptyOneIsEmpty() throws {
        let last = try JSON.decoder.decode(SentPage.self, from: Data(#"{"items":[{"id":3}],"next":null}"#.utf8))
        #expect(last.next == nil && last.items.map(\.id) == [3])
        let empty = try JSON.decoder.decode(SentPage.self, from: Data("{}".utf8))
        #expect(empty == SentPage())
    }

    // MARK: where it went

    @Test func mailSaysTheRecipientsAndTheSubject() {
        let two = item(target: #"{"to":["a@example.com","b@example.com"],"subject":"Hi","cc":["c@example.com"]}"#)
        #expect(SentText.targetText(two) == "a@example.com, b@example.com · Hi")
        #expect(SentText.targetText(item(target: #"{"to":["a@example.com"],"subject":""}"#)) == "a@example.com")
    }

    @Test func gitHubSaysTheRepoAndNumberAndAReviewKeepsItsEventForTheDetails() {
        let g = item(connector: "github", action: "github_review", target: #"{"ref":"example-org/app#12","event":"APPROVE"}"#)
        #expect(SentText.targetText(g) == "example-org/app#12")
        #expect(SentText.targetFields(g.target) == [SentField(key: "ref", value: "example-org/app#12"), SentField(key: "event", value: "APPROVE")])
    }

    @Test func larkSaysTheConversationElseTheCallTheNodesGateSaw() {
        let both = item(connector: "lark", target: #"{"conversation":"oc_1","method":"POST","path":"/im/v1/conversations/oc_1/messages"}"#)
        #expect(SentText.targetText(both) == "oc_1")
        #expect(SentText.targetText(item(connector: "lark", target: #"{"method":"POST","path":"/docs/v1/comments"}"#)) == "POST /docs/v1/comments")
        #expect(SentText.targetText(item(connector: "lark")) == "—")
    }

    @Test func anyOtherConnectorSaysTheCommonFieldsItNamedThreeAtMostInTheHubsOrder() {
        #expect(SentText.targetText(item(connector: "chatext", target: #"{"thread":"t-9","chat":"general"}"#)) == "general · t-9")
        #expect(SentText.targetText(item(connector: "chatext", target: #"{"number":4,"repo":"r","subject":"s","chat":"c","extra":"x"}"#)) == "c · s · r")
        #expect(SentText.targetText(item(connector: "chatext", target: #"{"recipient":["ann","bo"]}"#)) == "ann, bo")
    }

    @Test func nothingToShowIsADash() {
        #expect(SentText.targetText(item(connector: "chatext", target: #"{"arg_keys":["body","id"]}"#)) == "—")
        #expect(SentText.targetText(item(connector: "chatext")) == "—")
        #expect(SentText.targetText(item(target: #"{"subject":"only"}"#)) == "only")
        #expect(SentText.targetText(item(connector: "github")) == "—")
    }

    @Test func theDetailsListEveryNonEmptyFieldInAFixedOrderWithNumbersAsTheyAre() {
        let fields = SentText.targetFields(obj(#"{"zzz":"last","to":["b","a"],"n":3,"big":12.0,"ok":false,"empty":"","none":null,"nested":{"b":1,"a":[1.5,"x"]},"list":[],"subject":" S ","cc":["c"]}"#))
        // where it went first (the order the hub's destination fields have, then the other mail and GitHub ones), then the rest by name
        #expect(fields == [
            SentField(key: "to", value: "b, a"),
            SentField(key: "subject", value: "S"),
            SentField(key: "cc", value: "c"),
            SentField(key: "big", value: "12"),
            SentField(key: "n", value: "3"),
            SentField(key: "nested", value: #"{"a":[1.5,"x"],"b":1}"#),
            SentField(key: "ok", value: "false"),
            SentField(key: "zzz", value: "last"),
        ])
        #expect(SentText.targetFields([:]).isEmpty)
        #expect(SentText.fieldText(.string("  x ")) == "x")
        #expect(SentText.fieldText(.number(1.5)) == "1.5")
        #expect(SentText.fieldText(.number(1e300)) == "1e+300")
        // a whole number is written in full as JavaScript writes it below 1e21, and zero has no sign
        #expect(SentText.fieldText(.number(12.0)) == "12")
        #expect(SentText.fieldText(.number(1e18)) == "1000000000000000000")
        #expect(SentText.fieldText(.number(-0.0)) == "0")
        #expect(SentText.fieldText(.number(-7)) == "-7")
    }

    // MARK: what it said

    @Test func theRowShowsTheFirstTwoLinesBlankLinesAndCarriageReturnsSkipped() {
        #expect(SentText.previewLines(item(text: "one\r\n\r\ntwo\nthree")) == ["one", "two"])
        #expect(SentText.previewLines(item(text: "  only  ")) == ["only"])
        #expect(SentText.previewLines(item(text: "a\nb\nc"), most: 3) == ["a", "b", "c"])
        // only \n and \r\n are line breaks, as on the web
        #expect(SentText.previewLines(item(text: "a\rb\u{2028}c")) == ["a\rb\u{2028}c"])
    }

    @Test func theSummaryStandsInWhenThereIsNoTextAndNothingWhenThereIsNeither() {
        #expect(SentText.previewLines(item(text: " \n ", summary: "mail_send to a@example.com")) == ["mail_send to a@example.com"])
        #expect(SentText.previewLines(item()).isEmpty)
    }

    @Test func aTextIsCutOnlyWhenItIsExactlyAsLongAsTheLedgerKeepsCountingCharacters() {
        #expect(SentText.textMost == 4000)
        #expect(SentText.textCut(String(repeating: "x", count: 4000)))
        #expect(!SentText.textCut(String(repeating: "x", count: 3999)))
        // 3999 letters and one emoji are 4000 characters but 4001 UTF-16 units; 2000 emoji are 2000 characters
        #expect(SentText.textCut(String(repeating: "x", count: 3999) + "😀"))
        #expect(!SentText.textCut(String(repeating: "😀", count: 2000)))
        #expect(!SentText.textCut(""))
    }

    // MARK: how the gate let it through

    @Test func aRuleIsSaidAsOneWrittenTheWayConnectorGateHasIt() {
        let rule = SentRule(connector: "lark", action: "POST /im/v1/messages",
                            match: obj(#"{"kind":["a","b"],"chat_id":"oc_1"}"#), decision: "allow")
        let allowed = SentText.allowedBy(SentLetThrough(kind: "rule", rule: rule))
        #expect(allowed == SentAllowed(label: "按规则", tone: .neutral, detail: "lark · POST /im/v1/messages (chat_id=oc_1, kind=a, b) → allow"))
        #expect(SentText.ruleText(SentRule(connector: "github", action: "github_comment", decision: "allow")) == "github · github_comment → allow")
        #expect(SentText.ruleText(nil) == "")
        // a rule the hub did not give is still a rule
        let bare = SentText.allowedBy(SentLetThrough(kind: "rule"))
        #expect(bare.label == "按规则" && bare.detail == "一条网关规则放行了它。")
    }

    @Test func alwaysAllowedInThisChat() {
        let a = SentText.allowedBy(SentLetThrough(kind: "conversation_always"))
        #expect(a.label == "本对话始终允许" && a.tone == .neutral)
    }

    @Test func theUsersOwnAnswerOnACardSaysNoMoreThanThatTheyAgreedOnACard() {
        let a = SentText.allowedBy(SentLetThrough(kind: "card", interactionID: "ix-1"))
        #expect(a == SentAllowed(label: "你在确认卡片上同意", tone: .neutral, detail: "弹出了确认卡片，你点了允许。"))
        // the ledger can't say which device, so no device is named
        #expect(!(a.label + a.detail).contains("手机"))
    }

    @Test func aCardTheAssistantAnsweredSaysSoInOrange() {
        let a = SentText.allowedBy(SentLetThrough(kind: "card", interactionID: "ix-1", answeredBy: "assistant"))
        #expect(a.label == "助手代答" && a.tone == .orange)
    }

    @Test func aCardAnyoneElseAnsweredNamesThemInOrangeAndIsNeverTheUsersConfirmation() {
        let other = SentText.allowedBy(SentLetThrough(kind: "card", answeredBy: "bot-x"))
        #expect(other == SentAllowed(label: "由 bot-x 代答", tone: .orange, detail: "确认卡片由 bot-x 作答。"))
        #expect(other.label != SentText.allowedBy(SentLetThrough(kind: "card")).label)
    }

    @Test func anExtensionsOwnSendIsOrangeWithTheSettingThatLetIt() {
        let withSetting = SentText.allowedBy(SentLetThrough(kind: "auto", setting: "auto_reply"))
        #expect(withSetting.label == "扩展自行发送 · auto_reply" && withSetting.tone == .orange)
        let plain = SentText.allowedBy(SentLetThrough(kind: "auto"))
        #expect(plain.label == "扩展自行发送" && plain.tone == .orange)
    }

    @Test func aKindThePageDoesNotKnowIsShownAsItIsAndNoneAtAllIsADash() {
        #expect(SentText.allowedBy(SentLetThrough(kind: "voice_confirm")) == SentAllowed(label: "voice_confirm", tone: .neutral, detail: ""))
        #expect(SentText.allowedBy(SentLetThrough()).label == "—")
    }

    // MARK: who asked

    @Test func aChatToOpenNeedsBothIds() {
        let both = SentText.originInfo(SentOrigin(machineID: "mac", sessionID: "s1"))
        #expect(both.conversation?.machine == "mac" && both.conversation?.session == "s1" && both.extensionID == "")
        #expect(SentText.originInfo(SentOrigin(machineID: "mac")).conversation == nil)
        #expect(SentText.originInfo(SentOrigin(sessionID: "s1")).conversation == nil)
        #expect(SentText.originInfo(SentOrigin(machineID: " ", sessionID: "s1")).conversation == nil)
    }

    @Test func anExtensionsOwnRecordSaysSoWithTheChatItNamesWhenItNamesOne() {
        let alone = SentText.originInfo(SentOrigin(extensionID: "examplechat"))
        #expect(alone.conversation == nil && alone.extensionID == "examplechat")
        let both = SentText.originInfo(SentOrigin(machineID: "mac", sessionID: "s1", extensionID: "examplechat"))
        #expect(both.conversation?.session == "s1" && both.extensionID == "examplechat")
    }

    @Test func anEmptyOriginIsNoChat() {
        let none = SentText.originInfo(SentOrigin())
        #expect(none.conversation == nil && none.extensionID == "")
    }

    // MARK: the list

    @Test func mergesByIdOneRowEachTheNewerCopyWinsNewestFirst() {
        let merged = SentText.merge([item(9), item(5, summary: "old"), item(2)], [item(5, summary: "new"), item(12)])
        #expect(ids(merged) == [12, 9, 5, 2])
        #expect(merged[2].summary == "new")
    }

    @Test func aRefreshWithNothingOlderLoadedIsThePageAlone() {
        let page = SentPage(items: [item(6), item(5)], next: 5)
        #expect(SentText.refreshed(have: [item(5), item(4)], page: page, loadedOlder: false) == SentRefresh(items: page.items, next: .set(5), loadedOlder: false))
    }

    @Test func aRefreshWithOlderPagesLoadedMergesThePageInAndKeepsTheOlderEndsCursor() {
        let have = [100, 99, 51, 50, 3].map { item($0) }
        let out = SentText.refreshed(have: have, page: SentPage(items: [item(102), item(101), item(100)], next: 100), loadedOlder: true)
        #expect(ids(out.items) == [102, 101, 100, 99, 51, 50, 3])
        #expect(out.next == .keep && out.loadedOlder)
    }

    @Test func aRefreshWhosePageDoesNotReachTheNewestRowHeldStartsOver() {
        let page = SentPage(items: [item(160), item(159)], next: 159)
        #expect(SentText.refreshed(have: [item(100), item(99)], page: page, loadedOlder: true) == SentRefresh(items: page.items, next: .set(159), loadedOlder: false))
    }

    @Test func aFirstPageThatIsTheWholeLedgerMergesWhateverItsIdsAndAnEmptyOneEmptiesTheList() {
        let have = [item(100), item(50)]
        let whole = SentText.refreshed(have: have, page: SentPage(items: [item(200)], next: nil), loadedOlder: true)
        #expect(ids(whole.items) == [200, 100, 50])
        #expect(SentText.refreshed(have: have, page: SentPage(), loadedOlder: true) == SentRefresh(items: [], next: .set(nil), loadedOlder: false))
    }

    // MARK: the connector chips

    @Test func theChipsAreLarkThenTheConnectorsRowsShowedThenTheChosenOneNoneTwice() {
        let all = SentText.connectorChoices(seen: ["mail", "github", "goneext"], selected: "other")
        #expect(all == [
            SentConnector(id: "lark", name: "飞书"), SentConnector(id: "mail", name: "邮件"), SentConnector(id: "github", name: "GitHub"),
            SentConnector(id: "goneext", name: "goneext"), SentConnector(id: "other", name: "other"),
        ])
        #expect(SentText.connectorChoices(seen: ["lark", "mail"], selected: "mail").map(\.id) == ["lark", "mail"])
    }

    @Test func onlyLarkIsOfferedBeforeAnyRowHasShownSoAConnectorThatOnlyReadsHasNoChip() {
        #expect(SentText.connectorChoices(seen: [], selected: nil) == [SentConnector(id: "lark", name: "飞书")])
    }

    @Test func theChosenConnectorsChipStaysWhileItIsChosenThoughNoRowHasShownIt() {
        #expect(SentText.connectorChoices(seen: [], selected: "mail").map(\.id) == ["lark", "mail"])
    }

    @Test func mailGitHubAndLarkAreNamedByTheirUsualWordsAnyOtherConnectorByItsId() {
        #expect(SentText.connectorName("mail") == "邮件")
        #expect(SentText.connectorName("github") == "GitHub")
        #expect(SentText.connectorName("lark") == "飞书")
        #expect(SentText.connectorName("xext") == "xext")
    }

    @Test func collectsTheConnectorsRowsShowInTheOrderTheyFirstShowed() {
        #expect(SentText.seenConnectors(["mail"], [item(connector: "mail")]) == ["mail"])
        let rows = [item(connector: "github"), item(connector: "mail"), item(connector: "github"), item(connector: "")]
        #expect(SentText.seenConnectors(["mail"], rows) == ["mail", "github"])
    }

    // MARK: the hub's refusals

    @Test func noDatabaseIsSaidAsThatNotAsTheHubBeingUnavailable() {
        let p = SentText.problem(APIError(status: 503, message: "no database: there is no ledger", fields: ["code": "no_database"]))
        #expect(p == SentProblem(kind: .noDatabase, text: "这台 hub 没有数据库，所以不记录以你的名义发出的内容。"))
    }

    @Test func a503WithNoSuchCodeIsTheUsualError() {
        let p = SentText.problem(APIError(status: 503, message: "bad gateway", fields: ["code": "node_offline"]))
        #expect(p == SentProblem(kind: .error, text: "bad gateway"))
    }

    @Test func a403IsADeviceWithoutFullAccess() {
        let p = SentText.problem(APIError(status: 403, message: "only a device with full rights", fields: ["code": "forbidden"]))
        #expect(p.kind == .forbidden && p.text.contains("完整权限"))
    }

    @Test func a404WithNoCodeIsAHubFromBeforeTheLedgerAndOneWithACodeIsAnOrdinaryFailure() {
        let old = SentText.problem(APIError(status: 404, message: "404 page not found"))
        #expect(old.kind == .unsupported && old.text.contains("升级 hub"))
        #expect(SentText.problem(APIError(status: 404, message: "no such thing", fields: ["code": "not_found"])).kind == .error)
    }

    @Test func anythingElseIsTheUsualErrorTextAndNoNetworkSaysItCannotReachTheHub() {
        let internalFailure = SentText.problem(APIError(status: 500, message: "boom", fields: ["code": "internal"]))
        #expect(internalFailure.kind == .error && internalFailure.text.contains("HTTP 500") && !internalFailure.text.contains("boom"))
        #expect(SentText.problem(URLError(.timedOut)).text != String(localized: "Can't reach the hub"))
        #expect(SentText.problem(URLError(.notConnectedToInternet)) == SentProblem(kind: .error, text: "连不上 hub"))
    }

    // MARK: the model, over a fake hub

    /// The ledger a fake hub answers from: rows newest first, three to a page, as the hub pages them.
    private final class Ledger: @unchecked Sendable {
        private let lock = NSLock()
        private var rows: [(id: Int, connector: String)] = []
        static let pageSize = 3

        func set(_ rows: [(id: Int, connector: String)]) { lock.withLock { self.rows = rows } }

        func page(before: Int, connector: String) -> Data {
            let hit = lock.withLock { rows.filter { (before == 0 || $0.id < before) && (connector.isEmpty || $0.connector == connector) } }
            let got = Array(hit.prefix(Self.pageSize))
            let items = got.map { #"{"id":\#($0.id),"at":"2026-11-05T12:00:00Z","connector":"\#($0.connector)","action":"a","text":"row \#($0.id)"}"# }
            let next = hit.count > Self.pageSize ? String(got.last!.id) : "null"
            return Data(#"{"items":[\#(items.joined(separator: ","))],"next":\#(next)}"#.utf8)
        }
    }

    private static let path = "/api/v1/connectors/sent"

    private func serving(_ ledger: Ledger) -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", Self.path) { req in ledger.page(before: Int(req.query["before"] ?? "") ?? 0, connector: req.query["connector"] ?? "") }
        return hub
    }

    private func model(_ hub: FakeHub) -> SentModel { SentModel(deps: .init(api: { hub.client })) }

    private func rows(_ ids: [Int], _ connector: String = "mail") -> [(id: Int, connector: String)] { ids.map { ($0, connector) } }

    @Test func readsTheFirstPageThenOlderOnesToTheEnd() async {
        let ledger = Ledger()
        ledger.set(rows([7, 6, 5, 4, 3, 2, 1]))
        let hub = serving(ledger)
        let page = model(hub)
        #expect(page.items == nil && !page.showsChips)
        await page.load()
        #expect(ids(page.items) == [7, 6, 5] && page.next == 5)
        await page.loadMore()
        #expect(ids(page.items) == [7, 6, 5, 4, 3, 2] && page.next == 2)
        await page.loadMore()
        #expect(ids(page.items) == [7, 6, 5, 4, 3, 2, 1] && page.next == nil)
        #expect(hub.sent("GET", Self.path).map(\.query["before"]) == [nil, "5", "2"])
        // nothing older: Load Earlier does nothing
        await page.loadMore()
        #expect(hub.sent("GET", Self.path).count == 3)
    }

    @Test func aRowThatComesTwiceShowsOnce() async {
        let hub = FakeHub()
        let second = Flag()
        hub.on("GET", Self.path) { _ in
            second.on
                ? Data(#"{"items":[{"id":4,"text":"again"},{"id":3}],"next":null}"#.utf8)
                : Data(#"{"items":[{"id":6},{"id":5},{"id":4,"text":"first"}],"next":4}"#.utf8)
        }
        let page = model(hub)
        await page.load()
        second.on = true
        await page.loadMore()
        #expect(ids(page.items) == [6, 5, 4, 3])
        // the newer copy of the row that came twice is the one shown
        #expect(page.items?.first { $0.id == 4 }?.text == "again")
    }

    @Test func theChipsAreWhatTheLoadedRowsShowedAndChoosingOneReadsItsRowsOnly() async {
        let ledger = Ledger()
        ledger.set([(6, "mail"), (5, "github"), (4, "mail"), (3, "github"), (2, "mail")])
        let hub = serving(ledger)
        let page = model(hub)
        await page.load()
        #expect(page.showsChips && page.choices.map(\.id) == ["lark", "mail", "github"])
        await page.choose("github")
        #expect(page.filter == "github" && ids(page.items) == [5, 3])
        #expect(hub.sent("GET", Self.path).last?.query["connector"] == "github")
        // the chosen one again goes back to All
        await page.choose("github")
        #expect(page.filter == nil && ids(page.items) == [6, 5, 4])
        #expect(hub.sent("GET", Self.path).last?.query["connector"] == nil)
    }

    @Test func loadEarlierKeepsTheChosenConnector() async {
        let ledger = Ledger()
        ledger.set((1...9).reversed().map { (id: $0, connector: $0 % 2 == 0 ? "github" : "mail") })
        let hub = serving(ledger)
        let page = model(hub)
        await page.load()
        await page.choose("github") // 8, 6, 4 and then 2
        #expect(ids(page.items) == [8, 6, 4] && page.next == 4)
        await page.loadMore()
        #expect(ids(page.items) == [8, 6, 4, 2] && page.next == nil)
        let asked = hub.sent("GET", Self.path).last?.query
        #expect(asked == ["before": "4", "connector": "github"])
    }

    @Test func aFilteredListThatIsEmptyKeepsItsChipSoThereIsAWayBack() async {
        let ledger = Ledger()
        ledger.set(rows([3, 2, 1]))
        let page = model(serving(ledger))
        await page.load()
        await page.choose("lark")
        #expect(page.items?.isEmpty == true && page.filter == "lark" && page.showsChips)
        await page.choose(nil)
        #expect(ids(page.items) == [3, 2, 1])
    }

    @Test func readingAgainKeepsTheOlderPagesAndTheirCursorAndAddsWhatCameIn() async {
        let ledger = Ledger()
        ledger.set(rows([6, 5, 4, 3, 2, 1]))
        let page = model(serving(ledger))
        await page.load()
        await page.loadMore()
        #expect(ids(page.items) == [6, 5, 4, 3, 2, 1] && page.next == nil)
        ledger.set(rows([7, 6, 5, 4, 3, 2, 1]))
        await page.load()
        #expect(ids(page.items) == [7, 6, 5, 4, 3, 2, 1] && page.next == nil)
    }

    @Test func readingAgainWithNothingOlderLoadedLetsPrunedRowsGo() async {
        let ledger = Ledger()
        ledger.set(rows([3, 2, 1]))
        let page = model(serving(ledger))
        await page.load()
        ledger.set(rows([3, 2]))
        await page.load()
        #expect(ids(page.items) == [3, 2])
    }

    @Test func theHubsRefusalsAreTheProblemAndAFailedFirstReadShowsNoChipsUnderAll() async {
        let hub = FakeHub()
        hub.on("GET", Self.path) { _ in throw APIError(status: 503, message: "no database", fields: ["code": "no_database"]) }
        let page = model(hub)
        await page.load()
        #expect(page.problem?.kind == .noDatabase && page.items == nil && !page.showsChips)
        // under a chip they stay, so a failed read can be left by choosing another one or All
        await page.choose("mail")
        #expect(page.problem?.kind == .noDatabase && page.showsChips)
        await page.choose(nil)
        #expect(page.filter == nil)
    }

    @Test func aHubFromBeforeTheLedgerAnd403() async {
        let old = model(FakeHub()) // no route: 404 without a code
        await old.load()
        #expect(old.problem?.kind == .unsupported)

        let hub = FakeHub()
        hub.on("GET", Self.path) { _ in throw APIError(status: 403, message: "forbidden", fields: ["code": "forbidden"]) }
        let page = model(hub)
        await page.load()
        #expect(page.problem?.kind == .forbidden)
    }

    @Test func aListThatIsThereStaysWhenALaterReadFailsAndRetryReadsAgain() async {
        let ledger = Ledger()
        ledger.set(rows([3, 2, 1]))
        let hub = FakeHub()
        let failing = Flag()
        hub.on("GET", Self.path) { req in
            if failing.on { throw APIError(status: 500, message: "boom") }
            return ledger.page(before: Int(req.query["before"] ?? "") ?? 0, connector: req.query["connector"] ?? "")
        }
        let page = model(hub)
        failing.on = true
        await page.load()
        #expect(page.problem?.kind == .error && page.items == nil)
        failing.on = false
        await page.load() // Retry
        #expect(page.problem == nil && ids(page.items) == [3, 2, 1])
        failing.on = true
        await page.load()
        #expect(page.problem == nil && ids(page.items) == [3, 2, 1])
    }

    @Test func retryGoesBackToTheSpinnerThenReadsAgain() async {
        let ledger = Ledger()
        ledger.set(rows([3, 2, 1]))
        let failing = Flag()
        let hub = FakeHub()
        hub.on("GET", Self.path) { req in
            if failing.on { throw APIError(status: 500, message: "boom") }
            return ledger.page(before: Int(req.query["before"] ?? "") ?? 0, connector: req.query["connector"] ?? "")
        }
        let held = Held(hub)
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        failing.on = true
        await page.load()
        #expect(page.problem?.kind == .error)
        failing.on = false
        let again = Task { await page.retry() } // request 2, held
        await held.gate.waitReached(2)
        #expect(page.problem == nil && page.items == nil, "the failure stays over the spinner")
        await held.gate.open(2)
        await again.value
        #expect(page.problem == nil && ids(page.items) == [3, 2, 1])
    }

    @Test func aFailedLoadEarlierSaysWhyAndKeepsTheList() async {
        let ledger = Ledger()
        ledger.set(rows([6, 5, 4, 3]))
        let hub = FakeHub()
        let failing = Flag()
        hub.on("GET", Self.path) { req in
            if failing.on, req.query["before"] != nil { throw APIError(status: 500, message: "boom") }
            return ledger.page(before: Int(req.query["before"] ?? "") ?? 0, connector: "")
        }
        let page = model(hub)
        await page.load()
        failing.on = true
        await page.loadMore()
        #expect(page.moreError == "boom" && ids(page.items) == [6, 5, 4] && page.next == 4 && !page.loadingMore)
        failing.on = false
        await page.loadMore()
        #expect(page.moreError == nil && ids(page.items) == [6, 5, 4, 3])
    }

    /// A switch a fake hub's closure reads.
    private final class Flag: @unchecked Sendable {
        private let lock = NSLock()
        private var value = false
        var on: Bool {
            get { lock.withLock { value } }
            set { lock.withLock { value = newValue } }
        }
    }

    // MARK: reads that come back late

    /// Requests the test can hold: the n-th request (1 is the first) is answered at once as the hub would then (a failure
    /// too), but only returned once the test lets it go.
    private final class Held: @unchecked Sendable {
        actor Gate {
            private var reached: Set<Int> = [], opened: Set<Int> = []
            private var reachedWaiters: [Int: CheckedContinuation<Void, Never>] = [:]
            private var openWaiters: [Int: CheckedContinuation<Void, Never>] = [:]

            func hold(_ n: Int) async {
                reached.insert(n)
                reachedWaiters.removeValue(forKey: n)?.resume()
                if opened.contains(n) { return }
                await withCheckedContinuation { openWaiters[n] = $0 }
            }
            func waitReached(_ n: Int) async {
                if reached.contains(n) { return }
                await withCheckedContinuation { reachedWaiters[n] = $0 }
            }
            func open(_ n: Int) {
                opened.insert(n)
                openWaiters.removeValue(forKey: n)?.resume()
            }
        }

        let hub: FakeHub
        let gate = Gate()
        private let lock = NSLock()
        private var count = 0
        private var holds: Set<Int> = []

        init(_ hub: FakeHub) { self.hub = hub }

        func hold(_ n: Int) { lock.withLock { _ = holds.insert(n) } }

        var client: APIClient {
            var c = hub.client
            let inner = c.transport!
            c.transport = { [self] req in
                let n = lock.withLock { () -> Int in count += 1; return count }
                let answer: Result<Data, Error>
                do { answer = .success(try await inner(req)) } catch { answer = .failure(error) }
                if lock.withLock({ holds.contains(n) }) {
                    await gate.hold(n)
                    try Task.checkCancellation() // a read the page gave up on ends as a cancelled request does
                }
                return try answer.get()
            }
            return c
        }
    }

    @Test func aReadStartedForAnEarlierFilterIsDroppedWhenItComesBack() async {
        let ledger = Ledger()
        ledger.set([(6, "mail"), (5, "github"), (4, "mail"), (3, "github")])
        let held = Held(serving(ledger))
        held.hold(1)
        let page = SentModel(deps: .init(api: { held.client }))
        let slow = Task { await page.load() } // the whole ledger, held
        await held.gate.waitReached(1)
        await page.choose("github")
        #expect(page.filter == "github" && ids(page.items) == [5, 3])
        await held.gate.open(1)
        await slow.value
        #expect(page.filter == "github" && ids(page.items) == [5, 3] && page.problem == nil)
    }

    /// The same with the chip's own read still on its way when the earlier one comes back: still dropped, not shown under the chip.
    @Test func aReadStartedForAnEarlierFilterIsDroppedThoughTheChipsReadIsNotInYet() async {
        let ledger = Ledger()
        ledger.set([(6, "mail"), (5, "github"), (4, "mail"), (3, "github")])
        let held = Held(serving(ledger))
        held.hold(1)
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        let slow = Task { await page.load() } // the whole ledger, held
        await held.gate.waitReached(1)
        let choosing = Task { await page.choose("github") } // request 2, held
        await held.gate.waitReached(2)
        await held.gate.open(1)
        await slow.value
        #expect(page.filter == "github" && page.items == nil && page.problem == nil)
        await held.gate.open(2)
        await choosing.value
        #expect(ids(page.items) == [5, 3])
    }

    /// A newer read that SwiftUI cancelled (its task went with the page) must not strand an older one that is good.
    @Test func aCancelledNewerReadDoesNotStrandAnOlderGoodOne() async {
        let ledger = Ledger()
        ledger.set(rows([3, 2, 1]))
        let held = Held(serving(ledger))
        held.hold(1)
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        let first = Task { await page.load() }
        await held.gate.waitReached(1)
        let second = Task { await page.load() }
        await held.gate.waitReached(2)
        second.cancel()
        await held.gate.open(2)
        await second.value
        #expect(page.items == nil && page.problem == nil, "a cancelled read says nothing")
        await held.gate.open(1)
        await first.value
        #expect(ids(page.items) == [3, 2, 1] && page.problem == nil)
    }

    @Test func aReadOfTheFirstPageThatANewerOneSupersededIsDropped() async {
        let ledger = Ledger()
        ledger.set(rows([3, 2, 1]))
        let held = Held(serving(ledger))
        held.hold(1)
        let page = SentModel(deps: .init(api: { held.client }))
        let slow = Task { await page.load() } // answered with [3, 2, 1], held
        await held.gate.waitReached(1)
        ledger.set(rows([4, 3, 2, 1]))
        await page.load()
        #expect(ids(page.items) == [4, 3, 2])
        await held.gate.open(1)
        await slow.value
        #expect(ids(page.items) == [4, 3, 2])
    }

    @Test func olderRowsReadForACursorTheListHasMovedOnFromAreDropped() async {
        let ledger = Ledger()
        ledger.set(rows([9, 8, 7, 6, 5, 4]))
        let held = Held(serving(ledger))
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        await page.load() // request 1: [9, 8, 7], next 7
        #expect(page.next == 7)
        let more = Task { await page.loadMore() } // request 2: [6, 5, 4] for before=7, held
        await held.gate.waitReached(2)
        #expect(page.loadingMore)
        ledger.set(rows([12, 11, 10, 9, 8, 7, 6, 5, 4]))
        await page.load() // request 3: [12, 11, 10]: the older page was not in yet, so the list is this page alone
        #expect(ids(page.items) == [12, 11, 10] && page.next == 10)
        await held.gate.open(2)
        await more.value
        #expect(ids(page.items) == [12, 11, 10] && page.next == 10 && !page.loadingMore)
    }

    /// Two reads of the first page in flight, the older back first: it shows until the newer comes, which then replaces it.
    @Test func aNewerReadThatComesBackAfterAnOlderOneIsTaken() async {
        let ledger = Ledger()
        ledger.set(rows([3, 2, 1]))
        let held = Held(serving(ledger))
        held.hold(1)
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        let first = Task { await page.load() } // answered with [3, 2, 1], held
        await held.gate.waitReached(1)
        ledger.set(rows([4, 3, 2, 1]))
        let second = Task { await page.load() } // answered with [4, 3, 2], held
        await held.gate.waitReached(2)
        await held.gate.open(1)
        await first.value
        #expect(ids(page.items) == [3, 2, 1])
        await held.gate.open(2)
        await second.value
        #expect(ids(page.items) == [4, 3, 2] && page.next == 2)
    }

    /// Older rows read under All come back after a chip was chosen whose list has the same cursor: not the chip's rows.
    @Test func olderRowsReadUnderAnEarlierFilterAreDroppedThoughTheCursorIsTheSame() async {
        let ledger = Ledger()
        ledger.set([(9, "mail"), (8, "mail"), (7, "mail"), (6, "github"), (5, "github"), (4, "github"), (2, "mail"), (1, "mail")])
        let held = Held(serving(ledger))
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        await page.load() // request 1: [9, 8, 7], next 7
        let more = Task { await page.loadMore() } // request 2: [6, 5, 4] (github) for before=7, held
        await held.gate.waitReached(2)
        await page.choose("mail") // request 3: [9, 8, 7], next 7 again
        #expect(ids(page.items) == [9, 8, 7] && page.next == 7)
        await held.gate.open(2)
        await more.value
        #expect(page.filter == "mail" && ids(page.items) == [9, 8, 7] && page.next == 7 && !page.loadingMore)
    }

    /// Choosing a chip starts the list over: the spinner, not the rows of the connector before, until its rows come in.
    @Test func choosingAChipEmptiesTheListUntilItsRowsComeIn() async {
        let ledger = Ledger()
        ledger.set([(4, "mail"), (3, "github"), (2, "mail")])
        let held = Held(serving(ledger))
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        await page.load()
        #expect(ids(page.items) == [4, 3, 2])
        let choosing = Task { await page.choose("github") } // request 2, held
        await held.gate.waitReached(2)
        #expect(page.filter == "github" && page.items == nil && page.problem == nil)
        await held.gate.open(2)
        await choosing.value
        #expect(ids(page.items) == [3])
    }

    /// The list under All was read; a chip whose read fails says why, not a spinner for good.
    @Test func aChipWhoseFirstReadFailsSaysWhyAfterAllWasRead() async {
        let ledger = Ledger()
        ledger.set([(4, "mail"), (3, "github")])
        let hub = FakeHub()
        hub.on("GET", Self.path) { req in
            if req.query["connector"] == "github" { throw APIError(status: 500, message: "boom") }
            return ledger.page(before: Int(req.query["before"] ?? "") ?? 0, connector: req.query["connector"] ?? "")
        }
        let page = model(hub)
        await page.load()
        #expect(ids(page.items) == [4, 3])
        await page.choose("github")
        #expect(page.problem?.kind == .error && page.items == nil && page.showsChips)
    }

    /// A read under All that fails after a chip was chosen says nothing: the chip's own read is still on its way.
    @Test func aFailedReadStartedForAnEarlierFilterSaysNothing() async {
        let ledger = Ledger()
        ledger.set([(4, "mail"), (3, "github")])
        let hub = FakeHub()
        hub.on("GET", Self.path) { req in
            if req.query["connector"] == nil { throw APIError(status: 500, message: "boom") }
            return ledger.page(before: Int(req.query["before"] ?? "") ?? 0, connector: req.query["connector"] ?? "")
        }
        let held = Held(hub)
        held.hold(1)
        held.hold(2)
        let page = SentModel(deps: .init(api: { held.client }))
        let all = Task { await page.load() } // request 1: fails, held
        await held.gate.waitReached(1)
        let choosing = Task { await page.choose("github") } // request 2, held
        await held.gate.waitReached(2)
        await held.gate.open(1)
        await all.value
        #expect(page.filter == "github" && page.problem == nil && page.items == nil)
        await held.gate.open(2)
        await choosing.value
        #expect(page.problem == nil && ids(page.items) == [3])
    }
}
