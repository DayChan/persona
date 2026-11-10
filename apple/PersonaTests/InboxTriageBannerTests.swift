import Foundation
import Testing
@testable import Persona

/// The inbox's banner about new items waiting for triage (#1773): the hub's status as the list brings it, the words
/// (the hub's reason, else the state's), one source named or several, Set Up only when no model is set up and the
/// device may change settings, and nothing from an older hub or when triage is ok.
@MainActor struct InboxTriageBannerTests {
    private func status(_ json: String) throws -> InboxTriageStatus {
        try JSON.decoder.decode(InboxTriageStatus.self, from: Data(json.utf8))
    }

    @Test func theListBringsTheStatus() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox", json: #"""
        {"items":[{"id":"mail:a:1:1","source":"mail","kind":"mail","account":"a","account_name":"a","title":"x","preview":"","count":1}],
         "triage":{"state":"unconfigured","waiting":3,"sources":[{"source":"lark","name":"飞书","state":"unconfigured","waiting":3}]}}
        """#)
        let listing = try await hub.client.inboxListing(connectors: true)
        #expect(listing.items.count == 1)
        let t = try #require(listing.triage)
        #expect(t.state == "unconfigured" && t.waiting == 3 && t.reason == nil)
        #expect(t.sources == [.init(source: "lark", name: "飞书", state: "unconfigured", reason: nil, waiting: 3)])
        #expect(hub.sent("GET", "/api/v1/inbox").first?.query["sources"] == "all")

        // An older hub has no triage: no status, no banner; the items as before.
        hub.on("GET", "/api/v1/inbox", json: #"{"items":[]}"#)
        let old = try await hub.client.inboxListing()
        #expect(old.triage == nil && InboxTriageText.line(old.triage) == nil)
        #expect(try await hub.client.inbox().isEmpty)
    }

    @Test func oneSourceIsNamed() throws {
        let t = try status(#"{"state":"unconfigured","reason":"","waiting":3,"sources":[{"source":"lark","name":"飞书","state":"unconfigured","waiting":3}]}"#)
        #expect(InboxTriageText.line(t) == "有 3 条飞书消息在等分拣：还没配分拣模型")
        // The hub's own reason comes first.
        let paused = try status(#"{"state":"paused","reason":"模型没有额度","waiting":2,"sources":[{"source":"lark","name":"飞书","state":"paused","reason":"模型没有额度","waiting":2}]}"#)
        #expect(InboxTriageText.line(paused) == "有 2 条飞书消息在等分拣：模型没有额度")
        // Blank space around it goes; only blanks count as none.
        let spaced = InboxTriageStatus(state: "timeout", reason: "  模型太慢 \n", waiting: 1, sources: nil)
        let blank = InboxTriageStatus(state: "timeout", reason: "  ", waiting: 1, sources: nil)
        #expect(InboxTriageText.line(spaced) == "有 1 条消息在等分拣：模型太慢")
        #expect(InboxTriageText.line(blank) == "有 1 条消息在等分拣：分拣模型太慢了")
        // A source without a name reads as the general line.
        let unnamed = try status(#"{"state":"behind","waiting":4,"sources":[{"source":"x","state":"behind","waiting":4}]}"#)
        #expect(InboxTriageText.line(unnamed) == "有 4 条消息在等分拣：新消息太多，正在排队")
    }

    @Test func severalSourcesAreCounted() throws {
        let t = try status(#"""
        {"state":"error","reason":"","waiting":7,"sources":[
          {"source":"lark","name":"飞书","state":"error","waiting":5},
          {"source":"telegram","name":"Telegram","state":"behind","waiting":2}]}
        """#)
        #expect(InboxTriageText.line(t) == "有 7 条消息在等分拣：分拣模型出错了")
    }

    @Test func eachStatesWords() throws {
        let words = ["unconfigured": "还没配分拣模型", "paused": "分拣模型暂停了", "error": "分拣模型出错了",
                     "timeout": "分拣模型太慢了", "behind": "新消息太多，正在排队"]
        for (state, why) in words {
            let t = InboxTriageStatus(state: state, reason: nil, waiting: 2, sources: nil)
            #expect(InboxTriageText.line(t) == "有 2 条消息在等分拣：" + why, "\(state)")
        }
        // A state this app does not know, without a reason: the count alone.
        #expect(InboxTriageText.line(.init(state: "later", reason: nil, waiting: 2, sources: nil)) == "有 2 条消息在等分拣")
    }

    @Test func nothingWhenOkOrNothingWaits() {
        #expect(InboxTriageText.line(nil) == nil)
        #expect(InboxTriageText.line(.init(state: "ok", reason: nil, waiting: 0, sources: nil)) == nil)
        #expect(InboxTriageText.line(.init(state: "ok", reason: nil, waiting: 3, sources: nil)) == nil)
        #expect(InboxTriageText.line(.init(state: "behind", reason: nil, waiting: 0, sources: nil)) == nil)
    }

    @Test func configureOnlyWhenUnconfiguredAndAllowed() {
        let unconfigured = InboxTriageStatus(state: "unconfigured", reason: nil, waiting: 1, sources: nil)
        #expect(InboxTriageText.configurable(unconfigured, mayChangeSettings: true))
        #expect(!InboxTriageText.configurable(unconfigured, mayChangeSettings: false), "体验模式: words only")
        for state in ["paused", "error", "timeout", "behind", "ok"] {
            let t = InboxTriageStatus(state: state, reason: nil, waiting: 1, sources: nil)
            let offered = InboxTriageText.configurable(t, mayChangeSettings: true)
            #expect(!offered, "\(state)")
        }
        #expect(!InboxTriageText.configurable(nil, mayChangeSettings: true))
        #expect(InboxTriageText.purpose == "inbox.triage")
    }

    @Test func sourcesNotReadThisRoundAreNamed() throws {
        let t = try status(#"""
        {"state":"ok","waiting":0,"sources":[
          {"source":"ext-chat","name":"示例聊天","state":"ok","waiting":0,"read_error":"timeout","read_reason":"这一轮没读到：超时了"},
          {"source":"ext-tracker","name":"","state":"ok","waiting":0,"read_error":"rate_limited"},
          {"source":"github","name":"GitHub","state":"ok","waiting":0}
        ]}
        """#)
        #expect(t.sources?.map(\.readError) == ["timeout", "rate_limited", nil])
        #expect(InboxTriageText.line(t) == nil, "a failed read is apart from the waiting-for-triage banner")
        #expect(InboxTriageText.unread(t) == "示例聊天：这一轮没读到：超时了\next-tracker：这一轮没读到，列表可能不全")
    }

    @Test func theUnreadNoticeClearsAfterAGoodRead() throws {
        let recovered = try status(#"{"state":"ok","waiting":0,"sources":[{"source":"ext-chat","name":"示例聊天","state":"ok","waiting":0}]}"#)
        #expect(InboxTriageText.unread(recovered) == nil)
        #expect(InboxTriageText.unread(nil) == nil)
        let blank = try status(#"{"state":"ok","waiting":0,"sources":[{"source":"x","state":"ok","waiting":0,"read_error":"  "}]}"#)
        #expect(InboxTriageText.unread(blank) == nil)
    }
}
