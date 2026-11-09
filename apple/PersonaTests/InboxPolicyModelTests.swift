import Foundation
import Testing
@testable import Persona

/// Inbox rules (docs/assistant/inbox.md "Policies"): what the hub lists decodes; an item's rule is picked sender before
/// chat before repository; the rule sheet prefills, saves (POST for a new one, PATCH for one it has, neither part
/// removes it), moves the items it reaches at once and puts them back on failure; the Rules list removes optimistically.
@MainActor
struct InboxPolicyModelTests {
    private static let policies = #"""
    {"policies":[
      {"id":"p1","source":"lark","source_name":"飞书","tag":"chat","value":"c1","name":"周末徒步群","tier":"noise","created_at":"2026-11-04T01:00:00Z"},
      {"id":"p2","source":"lark","source_name":"飞书","tag":"sender","value":"u1","name":"王经理","tier":"action","twin":"first","created_at":"2026-11-06T01:00:00Z"},
      {"id":"p3","source":"mail","source_name":"","tag":"sender","value":"u1","name":"","twin":"never"},
      {"id":"p4","source":"github","tag":"repo","value":"example-org/example-repo","name":"example-org/example-repo","tier":"later","created_at":"2026-11-05T01:00:00Z"}
    ]}
    """#

    private static func list() throws -> [InboxPolicy] {
        struct List: Decodable { var policies: [InboxPolicy] }
        return try JSON.decoder.decode(List.self, from: Data(policies.utf8)).policies
    }

    private static func item(_ id: String, source: String = "lark", tier: String? = "fyi", status: String? = "fyi", done: Bool = false,
                             mutes: String = #"[{"tag":"chat","value":"c1","name":"周末徒步群"},{"tag":"sender","value":"u1","name":"王经理"}]"#) throws -> InboxItem {
        let json = #"{"id":"\#(id)","source":"\#(source)","kind":"group","account":"a","account_name":"飞书","title":"x","preview":"","count":1,"#
            + (tier.map { #""tier":"\#($0)","# } ?? "") + (status.map { #""status":"\#($0)","# } ?? "")
            + (done ? #""done_at":"2026-11-06T01:00:00Z","# : "") + #""urgent":true,"mutes":\#(mutes)}"#
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    /// A fake inbox: the items the model reads and shows changed, the toasts it shows.
    private final class Inbox {
        var items: [InboxItem]
        var toasts: [Toast] = []
        init(_ items: [InboxItem]) { self.items = items }
    }

    private func deps(_ hub: FakeHub, _ inbox: Inbox) -> InboxPolicyModel.Deps {
        .init(api: { hub.client }, items: { inbox.items }, show: { changed in
            let byID = Dictionary(changed.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
            inbox.items = inbox.items.map { byID[$0.id] ?? $0 }
        }, toast: { inbox.toasts.append($0) })
    }

    @Test func policiesDecodeAndReadForPeople() throws {
        let list = try Self.list()
        #expect(list.map(\.tier) == [.noise, .action, .unset, .unset]) // "later": a tier a newer hub added
        #expect(list.map(\.twin) == [.unset, .first, .never, .unset])
        #expect(list[1].summary == "始终需要你 · 分身优先")
        #expect(list[0].detail == "飞书 · 会话 · 收起")
        #expect(list[2].sourceName == nil && list[2].name == "u1") // empty: the source's id, the value
        #expect(list[2].detail == "mail · 发件人 · 分身不碰")
        #expect(list[3].detail == "github · 仓库")
        #expect(list[0].createdAt != nil && list[2].createdAt == nil)
        let it = try Self.item("i")
        #expect(it.mutes?.map(\.policyTitle) == ["为这个会话设规则…", "为这个发件人设规则…"])
        #expect(InboxPolicyTarget(item: it, option: try #require(it.mutes?.first)).line == "飞书 · 会话")
        #expect(InboxPolicyTarget(policy: list[1]).line == "飞书 · 发件人")
    }

    @Test func anItemsRuleIsTheSendersBeforeItsChats() throws {
        let list = try Self.list()
        let it = try Self.item("i")
        #expect(InboxPolicy.applying(to: it, in: list)?.id == "p2")
        #expect(InboxPolicy.applying(to: it, in: list.reversed())?.id == "p2") // the order of the list does not matter
        #expect(InboxPolicy.applying(to: it, in: Array(list.prefix(1)))?.id == "p1")
        #expect(InboxPolicy.applying(to: try Self.item("g", source: "github"), in: list) == nil) // another source's rule
    }

    @Test func aNewRulePrefillsFromTheListAndPosts() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/policies", json: #"{"policies":[]}"#)
        var body: [String: String] = [:]
        hub.on("POST", "/api/v1/inbox/policies") { req in
            body = try JSONDecoder().decode([String: String].self, from: req.body ?? Data())
            return Data(#"{"policy":{"id":"p9","source":"lark","tag":"chat","value":"c1","name":"周末徒步群","tier":"noise"},"applied":2}"#.utf8)
        }
        let first = try Self.item("a")
        let same = try Self.item("b")
        let working = try Self.item("w", status: "working")
        let done = try Self.item("d", done: true)
        let untriaged = try Self.item("u", tier: nil, status: nil)
        let other = try Self.item("o", mutes: #"[{"tag":"chat","value":"c2","name":"别的群"}]"#)
        let inbox = Inbox([first, same, working, done, untriaged, other])
        let m = InboxPolicyModel(target: InboxPolicyTarget(item: first, option: try #require(first.mutes?.first)), deps: deps(hub, inbox))
        #expect(!m.canSave) // until the rules are read
        await m.load()
        #expect(m.loaded && m.policy == nil && m.tier == .unset && !m.canSave) // nothing to save for a new rule yet
        m.tier = .noise
        #expect(m.canSave)
        #expect(await m.save())
        #expect(body == ["item_id": "a", "tag": "chat", "tier": "noise", "twin": ""])
        // Only the open, triaged items of that chat the AI is not on moved at once.
        #expect(inbox.items.map(\.tier) == ["noise", "noise", "fyi", "fyi", nil, "fyi"])
        #expect(inbox.items[0].status == "noise" && inbox.items[0].reason == "按你的规则：周末徒步群" && inbox.items[0].urgent == false)
        #expect(inbox.items[2] == working && inbox.items[3] == done && inbox.items[5] == other)
        #expect(inbox.toasts.map(\.message) == ["已为“周末徒步群”保存规则"])
    }

    @Test func aRuleListThatFailsStartsFromNothing() async throws {
        let hub = FakeHub() // no list: an older hub
        let it = try Self.item("a")
        let m = InboxPolicyModel(target: InboxPolicyTarget(item: it, option: try #require(it.mutes?.first)), deps: deps(hub, Inbox([it])))
        await m.load()
        #expect(m.loaded && m.policy == nil && m.tier == .unset && m.twin == .unset)
    }

    @Test func anExistingRulePatchesAndRemoves() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/policies", json: Self.policies)
        var bodies: [[String: String]] = []
        hub.on("PATCH", "/api/v1/inbox/policies/p1") { req in
            bodies.append(try JSONDecoder().decode([String: String].self, from: req.body ?? Data()))
            return Data(#"{"policy":null,"applied":0}"#.utf8)
        }
        let it = try Self.item("a")
        let inbox = Inbox([it])
        let m = InboxPolicyModel(target: InboxPolicyTarget(item: it, option: try #require(it.mutes?.first)), deps: deps(hub, inbox))
        await m.load()
        #expect(m.policy?.id == "p1" && m.tier == .noise && m.twin == .unset)
        m.twin = .never
        #expect(await m.save())
        #expect(await m.save(remove: true))
        #expect(bodies == [["tier": "noise", "twin": "never"], ["tier": "", "twin": ""]])
        #expect(hub.sent("POST", "/api/v1/inbox/policies").isEmpty)
        #expect(inbox.toasts.map(\.message) == ["已为“周末徒步群”保存规则", "已移除“周末徒步群”的规则"])
        #expect(inbox.items[0].tier == "noise") // removing a rule leaves the tier it gave
    }

    @Test func anEditFromTheRulesListNeedsNoReadAndCanSaveNeither() async throws {
        let hub = FakeHub()
        hub.on("PATCH", "/api/v1/inbox/policies/p2", json: #"{"applied":0}"#)
        let p2 = try #require(try Self.list().first { $0.id == "p2" })
        let m = InboxPolicyModel(target: InboxPolicyTarget(policy: p2), deps: deps(hub, Inbox([])))
        #expect(m.loaded && m.tier == .action && m.twin == .first)
        await m.load()
        #expect(hub.sent("GET", "/api/v1/inbox/policies").isEmpty)
        m.tier = .unset
        m.twin = .unset
        #expect(m.canSave) // an existing rule set to neither: Save removes it
        #expect(await m.save())
        #expect(hub.sent("PATCH", "/api/v1/inbox/policies/p2").count == 1)
    }

    @Test func aFailedSavePutsTheItemsBack() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/policies", json: #"{"policies":[]}"#)
        let it = try Self.item("a")
        let inbox = Inbox([it])
        var during: String?
        hub.on("POST", "/api/v1/inbox/policies") { _ in
            during = inbox.items.first?.tier
            throw APIError(status: 400, message: "这一条不能这样设规则")
        }
        let m = InboxPolicyModel(target: InboxPolicyTarget(item: it, option: try #require(it.mutes?.first)), deps: deps(hub, inbox))
        await m.load()
        m.tier = .action
        #expect(await !m.save())
        #expect(during == "action") // shown while the hub was asked
        #expect(inbox.items == [it])
        #expect(inbox.toasts.map(\.message) == ["规则没保存成功：这一条不能这样设规则"])
        #expect(inbox.toasts.first?.style == .error)
        #expect(!m.busy && m.tier == .action) // the choice stays, to try again
    }

    @Test func theRulesListSortsNewestFirstAndRemoves() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/policies", json: Self.policies)
        hub.on("DELETE", "/api/v1/inbox/policies/p2", json: #"{"ok":true}"#)
        var toasts: [Toast] = []
        let m = InboxRulesModel(deps: .init(api: { hub.client }, toast: { toasts.append($0) }))
        await m.load()
        #expect(m.state.value?.map(\.id) == ["p2", "p4", "p1", "p3"])
        let p2 = try #require(m.state.value?.first)
        await m.remove(p2)
        #expect(m.state.value?.map(\.id) == ["p4", "p1", "p3"])
        #expect(hub.sent("DELETE", "/api/v1/inbox/policies/p2").count == 1)
        #expect(toasts.map(\.message) == ["已移除“王经理”的规则"])
    }

    @Test func aFailedRemovePutsTheRuleBack() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/policies", json: Self.policies)
        var during: [String]?
        var m: InboxRulesModel!
        hub.on("DELETE", "/api/v1/inbox/policies/p1") { _ in
            during = m.state.value?.map(\.id)
            throw APIError(status: 400, message: "坏了")
        }
        var toasts: [Toast] = []
        m = InboxRulesModel(deps: .init(api: { hub.client }, toast: { toasts.append($0) }))
        await m.load()
        let p1 = try #require(m.state.value?.first { $0.id == "p1" })
        await m.remove(p1)
        #expect(during == ["p2", "p4", "p3"]) // gone while the hub was asked
        #expect(m.state.value?.map(\.id) == ["p2", "p4", "p1", "p3"])
        #expect(toasts.map(\.message) == ["规则没移除成功：坏了"])
    }

    @Test func anOlderHubFailsTheFirstLoadInWords() async {
        let hub = FakeHub() // no route: 404
        var toasts: [Toast] = []
        let m = InboxRulesModel(deps: .init(api: { hub.client }, toast: { toasts.append($0) }))
        await m.load()
        guard case .failed(let said) = m.state else { Issue.record("expected a failure, got \(m.state)"); return }
        #expect(said.hasPrefix("规则没加载出来："))
        #expect(toasts.isEmpty)
    }

    @Test func aFailedReloadKeepsTheListAndSaysSo() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/policies", json: Self.policies)
        var toasts: [Toast] = []
        let m = InboxRulesModel(deps: .init(api: { hub.client }, toast: { toasts.append($0) }))
        await m.load()
        hub.on("GET", "/api/v1/inbox/policies") { _ in throw APIError(status: 400, message: "坏了") }
        await m.load()
        #expect(m.state.value?.count == 4)
        #expect(toasts.map(\.message) == ["规则没加载出来：坏了"])
    }
}
