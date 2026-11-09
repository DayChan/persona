import Foundation
import Testing
@testable import Persona

/// The inbox_item slot (docs/extensions/extensions.md「收件箱条目」, #1682): which sources' items a plugin draws, the item's body as
/// the plugin's View (`context.item`, its buttons), today's body whenever that cannot be (quietly, or with a line and
/// Retry when the plugin failed), and View in Session (persona://inbox/<id>/source) back to today's body.
@MainActor struct InboxItemSlotTests {
    private static let list = #"""
    {"ui":[{"plugin":"larkext","plugin_name":"飞书","id":"event","title":"事件","placement":"inbox_item","size":"full",
            "connector":"lark"},
           {"plugin":"other","id":"nowhere","title":"没有连接器","placement":"inbox_item"},
           {"plugin":"example-gateway","id":"usage","title":"用量","placement":"nav_page"}]}
    """#

    private static let event = #"""
    {"view":{"state":"page=1","sections":[
      {"items":[{"type":"row","title":"涉及的人","value":"示例同事、你"},{"type":"row","title":"时间","value":"10:01–10:05"}]},
      {"title":"消息","items":[{"type":"row","title":"示例同事","subtitle":"周四下午可以吗","value":"10:05"}]},
      {"items":[{"type":"field","name":"reply","label":"回复","kind":"text"},
                {"type":"button","title":"回复","action":{"id":"reply","submit":true}},
                {"type":"button","title":"在会话里看","action":{"id":"open"}}]}]}}
    """#

    private func item(_ id: String, source: String) throws -> InboxItem {
        try JSON.decoder.decode(InboxItem.self, from: Data(#"""
        {"id":"\#(id)","source":"\#(source)","kind":"group","account":"a","account_name":"a","title":"周四评审改期","preview":"","count":2}
        """#.utf8))
    }

    private func slots(_ hub: FakeHub, features: Set<String> = ["plugin_ui_slots"]) -> InboxItemSlots {
        InboxItemSlots(deps: .init(api: { hub.client }), features: { features })
    }

    private func listing(_ hub: FakeHub, _ json: String = Self.list) {
        hub.on("GET", "/api/v1/plugins/-/ui") { req in
            #expect(req.query["placement"] == "inbox_item", "only the inbox_item slot")
            return Data(json.utf8)
        }
    }

    @Test func theSourcesAnExtensionDraws() async throws {
        let hub = FakeHub()
        listing(hub)
        let s = slots(hub)
        let lark = try item("lark:F1:1051", source: "lark"), mail = try item("mail:acc1:t1:501", source: "mail")
        #expect(!s.plain(lark) && !s.plain(mail), "nothing known yet: an item waits for the list")
        await s.reload(for: "https://hub-a.example")
        #expect(s.places?.map(\.id) == ["event"], "only inbox_item contributions that name their connector")
        #expect(s.place(for: "lark")?.plugin == "larkext" && s.place(for: "mail") == nil)
        #expect(!s.plain(lark) && s.plain(mail))
        #expect(InboxItemSlotModel(item: mail, slots: s).phase == .plain, "known not to be a plugin's: no spinner first")
        #expect(InboxItemSlotModel(item: lark, slots: s).phase == .asking)
    }

    @Test func anOlderHubIsNotAsked() async throws {
        let hub = FakeHub()
        listing(hub)
        let s = slots(hub, features: ["plugins", "plugin_manager"])
        await s.reload(for: "https://hub-a.example")
        let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: s, deps: .init(api: { hub.client }))
        #expect(slot.phase == .plain)
        await slot.start()
        #expect(slot.phase == .plain && hub.requests.isEmpty, "a hub without UI slots is never asked")
    }

    @Test func aHubNotHeardFromYetIsAsked() async throws {
        // Opened at launch, before the hub's hello: not settled as today's detail for want of its features.
        let hub = FakeHub()
        listing(hub)
        hub.on("POST", "/api/v1/plugins/larkext/-/ui/event", json: Self.event)
        let s = slots(hub, features: [])
        let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: s, deps: .init(api: { hub.client }))
        #expect(slot.phase == .asking)
        await slot.start()
        #expect(slot.phase == .plugin)
    }

    @Test func aHubWithoutTheSlotKeepsTodaysDetail() async throws {
        // A hub with UI slots but not this one lists nothing for a placement it does not know.
        let hub = FakeHub()
        listing(hub, #"{"ui":[]}"#)
        let s = slots(hub)
        let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: s, deps: .init(api: { hub.client }))
        await slot.start()
        #expect(slot.phase == .plain && slot.page == nil)
        #expect(hub.sent("GET", "/api/v1/plugins/-/ui").count == 1 && hub.requests.count == 1, "nothing drawn")

        // One that cannot answer at all: the same, and nothing waits on it after.
        let broken = FakeHub()
        broken.on("GET", "/api/v1/plugins/-/ui") { _ in throw APIError(status: 500, message: "boom") }
        let b = slots(broken)
        let other = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: b, deps: .init(api: { broken.client }))
        await other.start()
        #expect(other.phase == .plain && b.places == [])
        #expect(InboxItemSlotModel(item: try item("lark:F2:2", source: "lark"), slots: b).phase == .plain)
    }

    @Test func theExtensionDrawsTheItemAndTakesItsButtons() async throws {
        let hub = FakeHub()
        listing(hub)
        var bodies: [JSONValue] = []
        hub.on("POST", "/api/v1/plugins/larkext/-/ui/event") { req in
            bodies.append(try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data()))
            if bodies.last?["action"] == .string("reply") { return Data(#"{"toast":"已回复"}"#.utf8) }
            return Data(Self.event.utf8)
        }
        let s = slots(hub)
        let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: s, deps: .init(api: { hub.client }))
        await slot.start()
        #expect(slot.phase == .plugin)
        let page = try #require(slot.page)
        #expect(page.view?.sections.count == 3 && slot.failure == nil)

        // The context says which item, with the app's UI version and platform.
        let drawn = try #require(bodies.first)
        #expect(drawn["context"]?["item"] == .string("lark:F1:1051") && drawn["context"]?["ui_version"] == .number(6))
        #expect(drawn["context"]?["platform"] == .string("ios") && drawn["action"] == nil)

        // A button: the same item, the plugin's state, the page's fields when it submits.
        page.values["reply"] = "周四可以"
        let reply = try #require(page.view?.sections[2].items[1].action)
        let o = await page.perform(reply)
        #expect(o.toast == "已回复")
        let pressed = try #require(bodies.last)
        #expect(pressed["action"] == .string("reply") && pressed["context"]?["item"] == .string("lark:F1:1051"))
        #expect(pressed["state"] == .string("page=1") && pressed["values"] == .object(["reply": .string("周四可以")]))

        // The item changes (new messages, Done): drawn again.
        let before = bodies.count
        await slot.changed()
        #expect(bodies.count == before + 1 && bodies.last?["action"] == nil)
    }

    @Test func refusedOrEmptyDrawsFallBackQuietly() async throws {
        // 403 (a device that may only read, an item of another source), 404 (the item gone, the connector not the
        // plugin's now), unsupported (the plugin does not draw this item): today's body, nothing said.
        for refusal in [APIError(status: 403, message: "forbidden", fields: ["code": "forbidden"]),
                        APIError(status: 404, message: "not its business", fields: ["code": "unsupported"]),
                        APIError(status: 404, message: "no such item", fields: ["code": "not_found"])] {
            let hub = FakeHub()
            listing(hub)
            hub.on("POST", "/api/v1/plugins/larkext/-/ui/event") { _ in throw refusal }
            let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: slots(hub),
                                          deps: .init(api: { hub.client }))
            await slot.start()
            #expect(slot.phase == .plain && slot.page == nil && slot.failure == nil, "\(refusal.status) \(refusal.code ?? "")")
        }
    }

    @Test func aFailedExtensionSaysSoAndTriesAgain() async throws {
        // 502 (the plugin failed, a view the hub cannot draw), a hub out of reach, an answer without a view: today's body
        // under a line saying why, with Retry.
        for failure in [APIError(status: 502, message: "插件出错了", fields: ["code": "plugin_failed"]),
                        APIError(status: 0, message: "连不上 hub")] {
            let hub = FakeHub()
            listing(hub)
            hub.on("POST", "/api/v1/plugins/larkext/-/ui/event") { _ in throw failure }
            let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: slots(hub),
                                          deps: .init(api: { hub.client }))
            await slot.start()
            #expect(slot.phase == .plain && slot.failure == failure.message && slot.place?.pluginName == "飞书")

            // Retry once the plugin is back: its page.
            hub.on("POST", "/api/v1/plugins/larkext/-/ui/event", json: Self.event)
            await slot.retry()
            #expect(slot.phase == .plugin && slot.failure == nil && slot.page?.view != nil)
        }
        let hub = FakeHub()
        listing(hub)
        hub.on("POST", "/api/v1/plugins/larkext/-/ui/event", json: #"{}"#)
        let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: slots(hub), deps: .init(api: { hub.client }))
        await slot.start()
        #expect(slot.phase == .plain && slot.failure != nil, "no view in the answer: the plugin drew nothing")
    }

    @Test func theSourceLink() throws {
        func item(_ s: String) -> String? { URL(string: s).flatMap(InboxSourceLink.item) }
        #expect(item("persona://inbox/lark:F1:1051/source") == "lark:F1:1051")
        #expect(item("persona://inbox/mail%3Aacc1%2Ft1%3A501/source") == "mail:acc1/t1:501", "the id is path-escaped")
        #expect(item("PERSONA://Inbox/lark:F1:1051/source") == "lark:F1:1051")
        #expect(item("persona://inbox/x/source") == "x", "item IDs are opaque, not restricted to known connectors")
        for not in ["persona://inbox/lark:F1:1051", "persona://inbox/lark:F1:1051/other", "persona://inbox//source",
                    "persona://chat/lark:F1:1051/source", "https://inbox/lark:F1:1051/source", "otherapp://inbox/x/source"] {
            #expect(item(not) == nil, "\(not)")
        }
        // What a button may open: https anywhere; the item's own source only on that item's page.
        let source = try #require(URL(string: "persona://inbox/lark:F1:1051/source"))
        #expect(ExtensionViewModel.opens(try #require(URL(string: "https://example.com/x")), item: nil))
        #expect(ExtensionViewModel.opens(source, item: "lark:F1:1051"))
        #expect(!ExtensionViewModel.opens(source, item: "lark:F2:7") && !ExtensionViewModel.opens(source, item: nil))
        #expect(!ExtensionViewModel.opens(try #require(URL(string: "http://example.com/x")), item: "lark:F1:1051"))
    }

    @Test func openInChatShowsTheItemsOwnSource() async throws {
        let hub = FakeHub()
        listing(hub)
        hub.on("POST", "/api/v1/plugins/larkext/-/ui/event") { req in
            let b = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            switch b["action"]?.string {
            case "open": return Data(#"{"open_url":"persona://inbox/lark:F1:1051/source"}"#.utf8)
            case "other": return Data(#"{"open_url":"persona://inbox/lark:F9:9/source"}"#.utf8)
            case "web": return Data(#"{"open_url":"https://example.com/thread"}"#.utf8)
            default: return Data(Self.event.utf8)
            }
        }
        let slot = InboxItemSlotModel(item: try item("lark:F1:1051", source: "lark"), slots: slots(hub), deps: .init(api: { hub.client }))
        await slot.start()
        let page = try #require(slot.page)

        // View in Session: the item's own source, today's body; Back brings the plugin's page back as it was.
        let open = await page.perform(ExtensionAction(id: "open"))
        let url = try #require(open.url)
        #expect(slot.open(url) && slot.showsSource && slot.phase == .plugin)
        slot.showsSource = false
        #expect(slot.page === page)

        // Another item's source is never opened; an https page is the page's to open.
        #expect(await page.perform(ExtensionAction(id: "other")).url == nil)
        let web = try #require(await page.perform(ExtensionAction(id: "web")).url)
        #expect(!slot.open(web) && !slot.showsSource)
    }

    @Test func anotherHubIsAskedAfresh() async throws {
        let hub = FakeHub()
        listing(hub)
        let s = slots(hub)
        await s.reload(for: "https://hub-a.example")
        #expect(s.place(for: "lark") != nil)
        listing(hub, #"{"ui":[]}"#)
        await s.reload(for: "https://hub-b.example")
        #expect(s.places == [] && s.place(for: "lark") == nil, "nothing of the last hub's stays")
        // A list that cannot be read keeps what there was.
        listing(hub)
        await s.reload()
        hub.on("GET", "/api/v1/plugins/-/ui") { _ in throw APIError(status: 0, message: "offline") }
        await s.reload()
        #expect(s.place(for: "lark")?.id == "event")
    }

    @Test func thePlaceAndContextCode() throws {
        let place = try JSON.decoder.decode(ExtensionUIPlace.self, from: Data(#"{"plugin":"p","id":"u","title":"t","placement":"inbox_item","connector":"lark"}"#.utf8))
        #expect(place.connector == "lark" && ExtensionUIPlace.slots.contains("inbox_item"))
        let ctx = try JSON.decoder.decode(JSONValue.self, from: JSONEncoder().encode(ExtensionUIContext.here(item: "lark:F1:1051")))
        #expect(ctx["item"] == .string("lark:F1:1051") && ctx["machine"] == nil && ctx["launcher"] == nil)
    }
}
