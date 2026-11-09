import Foundation
import Testing
@testable import Persona

@MainActor
struct InboxTests {
    private let json = """
    {"items":[
      {"id":"lark:f1:11","source":"lark","kind":"mention","account":"feishu","account_name":"飞书","title":"发布群","sender":"张三",
       "avatar":"/api/v1/lark/media/a","preview":"今晚能上线吗","count":3,"started_at":"2026-10-27T01:00:00Z","updated_at":"2026-10-27T02:00:00Z",
       "lark":{"feed_id":"f1","is_group":true,"start_position":"11","mention_position":"12"}},
      {"id":"mail:a1:abc:7","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com","title":"年假确认",
       "sender":"HR","preview":"请在周五前回复","count":1,"updated_at":"2026-10-27T03:00:00Z","tier":"fyi","summary":"HR 让你确认年假",
       "done_at":"2026-10-27T04:00:00Z","done_by":"user",
       "mail":{"account_id":"a1","mailbox":"INBOX","uids":[5,7],"message_id":"<m@x>","from":{"name":"HR","email":"hr@x"},"direct":true}}
    ]}
    """

    private struct List: Decodable { var items: [InboxItem] }

    @Test func decodesItemsAndOpensThem() throws {
        let items = try JSON.decoder.decode(List.self, from: Data(json.utf8)).items
        #expect(items.count == 2)
        let chat = items[0], mail = items[1]
        #expect(!chat.isTriaged && chat.triage == .action) // not triaged yet: shown as needing you
        #expect(chat.kindLabel == "@我")
        var read = chat
        read.read = true // read in Feishu while it still needs me: no @ flag
        #expect(read.kindLabel == nil)
        let conv = try #require(chat.larkConversation)
        #expect(conv.id == "f1" && conv.isGroup && conv.focus == "11" && conv.unread == 3)

        #expect(mail.isDone && mail.triage == .fyi && mail.kindLabel == nil)
        let m = try #require(mail.mailSummary)
        #expect(m.uid == 7 && m.accountID == "a1" && m.subject == "年假确认" && m.seen)
        #expect(mail.larkConversation == nil && chat.mailSummary == nil)
    }

    @Test func anItemKeepsItsManagerAndEachManagerGetsItsOwnShare() throws {
        let items = try JSON.decoder.decode(List.self, from: Data("""
        {"items":[
          {"id":"a","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com","title":"A","preview":"","count":1,"manager":"assistant"},
          {"id":"b","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com","title":"B","preview":"","count":1,"manager":"m-work"},
          {"id":"c","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com","title":"C","preview":"","count":1,"manager":"assistant"},
          {"id":"d","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com","title":"D","preview":"","count":1}
        ]}
        """.utf8)).items
        #expect(items.map(\.manager) == ["assistant", "m-work", "assistant", nil])
        let shares = InboxStore.managers(items)
        #expect(shares.map(\.key) == ["assistant", "m-work", nil], "in the order the list has them")
        #expect(shares.map { $0.items.map(\.id) } == [["a", "c"], ["b"], ["d"]])
        let api = FakeHub().client
        #expect(api.manager(items[1].manager).managerID == "m-work" && api.manager(items[3].manager).managerID == nil)
    }

    @Test func aLarkItemCarriesItsReadPosition() throws {
        let item = try JSON.decoder.decode(InboxItem.self, from: Data("""
        {"id":"lark:f2:20","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"t","preview":"p","count":2,
         "lark":{"feed_id":"f2","is_group":false,"start_position":"20","read_position":"18"}}
        """.utf8))
        #expect(item.lark?.readPosition == "18")
        let conv = try #require(item.larkConversation)
        #expect(conv.focus == "20" && conv.readPosition == "18")
    }

    @Test func anOlderHubWithoutReadPositionStillDecodes() throws {
        let item = try JSON.decoder.decode(InboxItem.self, from: Data("""
        {"id":"lark:f3:30","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"t","preview":"p","count":1,
         "lark":{"feed_id":"f3","is_group":false,"start_position":"30"}}
        """.utf8))
        #expect(item.lark?.readPosition == nil)
        let conv = try #require(item.larkConversation)
        #expect(conv.focus == "30" && conv.readPosition == nil)
    }

    @Test func decodesTheAgentsDraft() throws {
        let drafted = try JSON.decoder.decode(InboxItem.self, from: Data("""
        {"id":"x","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"t","preview":"p","count":1,
         "agent":{"state":"drafted","draft":"合了","note":"查到 MR","machine_id":"server","session_id":"s1","started_at":"2026-10-27T01:00:00Z"}}
        """.utf8))
        #expect(drafted.agentDraft == "合了" && !drafted.agentWorking)
        #expect(drafted.agent?.sessionKey == SessionKey(machineID: "server", sessionID: "s1"))
        var running = drafted
        running.agent = .init(state: "running")
        #expect(running.agentDraft == nil && running.agentWorking && running.agent?.sessionKey == nil)
        running.agent = nil
        #expect(running.agentDraft == nil && !running.agentWorking)
    }

    @Test func feishuLinksOpenNatively() throws {
        let doc = try #require(URL(string: "https://open.feishu.cn/docx/ExampleDocument"))
        let post = try #require(URL(string: "https://applink.larkoffice.com/client/moments/detail?postId=7000000000000000001"))
        let minutes = try #require(URL(string: "https://open.feishu.cn/minutes/ExampleRecording"))
        #expect(FeishuLinks.route(doc, lark: .all) == .doc(url: doc.absoluteString, title: nil))
        #expect(FeishuLinks.route(post, lark: .all) == .momentsPost(id: "7000000000000000001"))
        #expect(FeishuLinks.route(minutes, lark: .all) == nil) // a web page in the link sheet
    }

    /// Without the Lark extension (no `lark` in the hub's features) the native readers have nothing to read:
    /// Feishu documents and company-feed posts are ordinary web pages in the link sheet (#1530).
    @Test func feishuLinksWithoutTheExtensionAreWebPages() throws {
        let doc = try #require(URL(string: "https://open.feishu.cn/docx/ExampleDocument"))
        let post = try #require(URL(string: "https://applink.larkoffice.com/client/moments/detail?postId=7000000000000000001"))
        #expect(FeishuLinks.route(doc, lark: .none) == nil)
        #expect(FeishuLinks.route(post, lark: .none) == nil)
        #expect(FeishuLinks.target(doc, lark: .none) == .sheet)
        #expect(FeishuLinks.target(post, lark: .none) == .sheet)
        // Chats but no company feed: documents open natively, posts on the web.
        let chatsOnly = LarkFeatures(chats: true, moments: false)
        #expect(FeishuLinks.route(doc, lark: chatsOnly) == .doc(url: doc.absoluteString, title: nil))
        #expect(FeishuLinks.target(post, lark: chatsOnly) == .sheet)
        // From the hub's hello.
        #expect(FeishuLinks.route(post, lark: LarkFeatures(hub: ["tls", "lark", "lark_moments"])) == .momentsPost(id: "7000000000000000001"))
        #expect(FeishuLinks.route(doc, lark: LarkFeatures(hub: ["tls", "connectors"])) == nil)
    }

    @Test func feishuLinkTargets() throws {
        func target(_ s: String) throws -> FeishuLinks.Target { FeishuLinks.target(try #require(URL(string: s)), lark: .all) }
        // Feishu docs and Moments posts: the native views.
        #expect(try target("https://open.feishu.cn/wiki/ExampleWiki") == .route(.doc(url: "https://open.feishu.cn/wiki/ExampleWiki", title: nil)))
        #expect(try target("https://applink.larkoffice.com/client/moments/detail?postId=7000000000000000001") == .route(.momentsPost(id: "7000000000000000001")))
        // Any other web page, Feishu's own included: the in-app link sheet.
        #expect(try target("https://example.com/a?b=1") == .sheet)
        #expect(try target("HTTP://Example.com") == .sheet)
        #expect(try target("https://open.feishu.cn/minutes/ExampleRecording") == .sheet)
        // Not a web page: the system.
        #expect(try target("mailto:me@example.com") == .system)
        #expect(try target("tel:10086") == .system)
        #expect(try target("lark://client/profile?uid=u1") == .system)
        #expect(try target("/docs/a") == .system) // relative path
        #expect(try target("https:///no-host") == .system)
        #expect(try target("http:") == .system)
    }
}

@MainActor
struct InboxHandOffTests {
    @Test func handOffCarriesTheItemAsContext() throws {
        var it = try JSON.decoder.decode(InboxItem.self, from: Data("""
        {"id":"lark:f1:11","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"Sample Contact","preview":"合入了吗",
         "count":1,"lark":{"feed_id":"f1","is_group":false}}
        """.utf8))
        it.context = "飞书私聊（Sample Contact）的新消息：\n[10-27 17:55] Sample Contact：合入到 1013 release 吗"
        let text = InboxHandOffSheet.message(it, ask: "帮我查一下")
        let parsed = ContextRef.parse(text)
        #expect(parsed.refs.count == 1)
        #expect(parsed.refs[0].title == "飞书 · Sample Contact")
        #expect(parsed.refs[0].text.contains("合入到 1013 release") && parsed.refs[0].text.contains("feedId f1"))
        #expect(parsed.rest == "帮我查一下")
    }
}
