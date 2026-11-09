import Foundation
import Testing
@testable import Persona

/// Settings › Extensions and Connectors with several managers (docs/assistant/managers.md "Grouping", #1827): `manager` decodes on
/// extensions and connectors, the lists group under each manager's name (the default first, then the roster's order, empty
/// groups left out, an unknown or missing stamp the default's, nothing with one manager), and every request about another
/// manager's extension or connector names it (`X-Persona-Manager`) while the default's name none. Made-up values.
@MainActor
struct ManagerGroupsTests {
    private static let header = "X-Persona-Manager"

    /// The mock hub's three installed managers (助手 the default, 工作 online, 家里 offline) and one still installing.
    private static let roster = #"""
    {"default":"assistant","managers":[
      {"id":"assistant","name":"助手","online":true,"default":true,"builtin":true},
      {"id":"m-1","name":"工作","online":true},
      {"id":"m-2","name":"家里","online":false,"last_seen":"2026-11-08T05:00:00Z"},
      {"id":"m-3","name":"测试","online":false,"installing":"2099-01-01T00:00:00Z"}]}
    """#

    private static let oneManager = #"{"default":"assistant","managers":[{"id":"assistant","name":"助手","online":true,"default":true}]}"#

    /// The merged list as the hub sends it: the default's first, each manager's built-ins once per manager.
    private static let extensions = #"""
    {"extensions":[
      {"id":"cua","name":"操控电脑","kind":"computer","status":"running","source":"builtin","manager":"assistant"},
      {"id":"notes","name":"Example Notes","kind":"connectors","status":"running","source":"installed","manager":"assistant"},
      {"id":"cua","name":"操控电脑","kind":"computer","status":"disabled","source":"builtin","manager":"m-1"},
      {"id":"cua","name":"操控电脑","kind":"computer","status":"disabled","source":"builtin","manager":"m-2"},
      {"id":"stray","name":"Stray","kind":"connectors","status":"running","source":"installed","manager":"gone"},
      {"id":"old","name":"Old","kind":"connectors","status":"running"}]}
    """#

    private static func detail(_ id: String, enabled: Bool = true) -> String {
        #"{"id":"\#(id)","name":"\#(id)","kind":"connectors","status":"running","enabled":\#(enabled),"auto_upgrade":true,"# +
            #""contributes":{"connectors":["\#(id)"]},"form":{"fields":[{"name":"base_url","type":"string"}]},"config":{},"# +
            #""secrets":[{"name":"api_key","title":"API Key","set":false}],"permissions":[],"# +
            #""contributions":[{"point":"connectors","id":"\#(id)","title":"\#(id)","enabled":true,"can_disable":true}]}"#
    }

    private func client(_ hub: FakeHub) -> APIClient {
        var c = hub.client
        c.extensionsAPI = true
        return c
    }

    private func named(_ requests: [LocalRequest]) -> [String?] { requests.map { $0.headers[Self.header] } }

    // MARK: decoding and the roster

    @Test func managerDecodesOnExtensionsAndConnectors() throws {
        let e = try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"cua","name":"C","kind":"computer","status":"running","manager":"m-1"}"#.utf8))
        #expect(e.manager == "m-1" && e.listKey == "m-1/cua")
        let old = try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"cua","name":"C","kind":"computer","status":"running"}"#.utf8))
        #expect(old.manager == nil && old.listKey == "cua")
        let c = try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"imessage","name":"信息","status":"ok","manager":"m-2"}"#.utf8))
        #expect(c.manager == "m-2" && c.listKey == "m-2/imessage")
        #expect(ConnectorRow.hub(c).id == "m-2/imessage", "two managers' copies of one connector are two rows")
        let bare = try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"github","name":"GitHub","status":"ok"}"#.utf8))
        #expect(bare.manager == nil && ConnectorRow.hub(bare).id == "github")
    }

    @Test func theRosterOrdersAndOwns() throws {
        let roster = ManagerRoster(try JSON.decoder.decode(APIClient.ManagerList.self, from: Data(Self.roster.utf8)))
        #expect(roster.defaultID == "assistant")
        #expect(roster.installed.map(\.id) == ["assistant", "m-1", "m-2"], "one still installing isn't one yet")
        #expect(roster.isMulti)
        #expect(roster.owner("m-1") == "m-1" && roster.owner(nil) == "assistant" && roster.owner("") == "assistant")
        #expect(roster.owner("gone") == "assistant" && roster.owner("m-3") == "assistant", "unknown or installing: the default's")
        #expect(roster.named("m-2") == "m-2" && roster.named("assistant") == nil && roster.named(nil) == nil && roster.named("gone") == nil)

        // Another default: it comes first, the rest in the roster's order.
        let moved = ManagerRoster(managers: roster.managers, defaultID: "m-2")
        #expect(moved.installed.map(\.id) == ["m-2", "assistant", "m-1"])
        #expect(moved.named("assistant") == "assistant" && moved.named(nil) == nil)
        let groups = try #require(moved.groups([("a", "assistant"), ("b", nil), ("c", "m-2")]) { $0.1 })
        #expect(groups.map(\.id) == ["m-2", "assistant"], "the default first, the empty group (m-1) left out")
        #expect(groups[0].items.map(\.0) == ["b", "c"])

        // No default named: the one marked default, else the hub's own assistant.
        #expect(ManagerRoster(managers: [HubManager(id: "x", name: "X", online: true, isDefault: true)], defaultID: "").defaultID == "x")
        #expect(ManagerRoster(managers: [], defaultID: nil).defaultID == "assistant")

        let one = ManagerRoster(try JSON.decoder.decode(APIClient.ManagerList.self, from: Data(Self.oneManager.utf8)))
        #expect(!one.isMulti && one.groups(["a"]) { _ in "assistant" } == nil && one.named("assistant") == nil)
    }

    // MARK: Settings › Extensions

    @Test func theListGroupsUnderEachManager() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions", json: Self.extensions)
        hub.on("GET", "/api/v1/managers", json: Self.roster)
        let page = ExtensionsModel(deps: .init(api: { self.client(hub) }, auth: ExtensionAuthStore()))
        await page.load()
        let groups = try #require(page.groups)
        #expect(groups.map(\.manager.name) == ["助手", "工作", "家里"])
        #expect(groups[0].items.map(\.id) == ["cua", "notes", "stray", "old"], "no stamp, or a manager the roster lacks: the default's")
        #expect(groups[1].items.map(\.listKey) == ["m-1/cua"] && groups[2].items.map(\.listKey) == ["m-2/cua"])
        // The offline one's heading says when it last synced.
        #expect(!groups[2].manager.online && groups[2].manager.lastSeen != nil)
        #expect(ManagersText.status(groups[2].manager, now: .now).text.hasPrefix("离线 · 最后同步于 "))
        // Each row opens its own manager's copy; the default's names none.
        #expect(groups.map { page.manager(of: $0.items[0]) } == [nil, "m-1", "m-2"])
        #expect(Set(page.plugins!.map(\.listKey)).count == page.plugins!.count, "every row has its own identity")
        #expect(named(hub.sent("GET", "/api/v1/extensions")) == [nil], "the list is the merged one")
    }

    @Test func oneManagerOrNoRosterLooksAsBefore() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions", json: #"{"extensions":[{"id":"cua","name":"C","kind":"computer","status":"running","manager":"assistant"}]}"#)
        hub.on("GET", "/api/v1/managers", json: Self.oneManager)
        let page = ExtensionsModel(deps: .init(api: { self.client(hub) }, auth: ExtensionAuthStore()))
        await page.load()
        #expect(page.groups == nil && page.plugins?.count == 1 && page.manager(of: page.plugins![0]) == nil)

        // A hub from before the roster (404): one list, nothing named, whatever the items say.
        let old = FakeHub()
        old.on("GET", "/api/v1/extensions", json: Self.extensions)
        let before = ExtensionsModel(deps: .init(api: { self.client(old) }, auth: ExtensionAuthStore()))
        await before.load()
        #expect(before.groups == nil && before.roster == nil && before.error == nil)
        #expect(before.plugins!.allSatisfy { before.manager(of: $0) == nil })
    }

    @Test func aRosterThatCantBeReadNowKeepsTheLastOne() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions", json: Self.extensions)
        hub.on("GET", "/api/v1/managers", json: Self.roster)
        let page = ExtensionsModel(deps: .init(api: { self.client(hub) }, auth: ExtensionAuthStore()))
        await page.load()
        hub.on("GET", "/api/v1/managers") { _ in throw APIError(status: 502, message: "bad gateway") }
        await page.load()
        #expect(page.groups?.count == 3, "a failed read of the roster doesn't fold the groups")
    }

    /// Every request of another manager's extension page names it; the default's page names none.
    @Test func anotherManagersExtensionNamesItsManager() async throws {
        for manager in ["m-1", nil] as [String?] {
            let hub = FakeHub()
            let base = "/api/v1/extensions/notes/-"
            hub.on("GET", base + "/", json: Self.detail("notes"))
            hub.on("GET", "/api/v1/managers", json: Self.roster)
            for (method, path) in [("PUT", "/enabled"), ("PUT", "/config"), ("PUT", "/auto-upgrade"), ("PUT", "/secrets/api_key"),
                                   ("DELETE", "/secrets/api_key"), ("PUT", "/contributions/connectors/notes")] {
                hub.on(method, base + path, json: Self.detail("notes"))
            }
            let page = ExtensionModel(id: "notes", manager: manager, deps: .init(api: { self.client(hub) }, drafts: ExtensionDrafts()))
            await page.load()
            await page.setEnabled(false)
            page.set(.string("https://notes.example.com"), at: ["base_url"])
            #expect(await page.save())
            await page.setAutoUpgrade(false)
            #expect(await page.setSecret("api_key", value: "secret-value") == .saved)
            await page.clearSecret("api_key")
            let contribution = try #require(page.detail?.contributions?.first)
            await page.setContribution(contribution, enabled: false)
            #expect(page.error == nil)
            // Another manager's page says whose it is: the roster is read (naming none) for its name.
            #expect(page.managerName == (manager == nil ? nil : "工作"))
            #expect(named(hub.sent("GET", "/api/v1/managers")) == (manager == nil ? [] : [nil]))
            let calls = hub.requests.filter { $0.path.hasPrefix(base) }
            #expect(calls.count == 7)
            #expect(named(calls) == Array(repeating: manager, count: 7), "every request names \(manager ?? "no manager")")
        }
    }

    /// The draft of one manager's copy isn't another's.
    @Test func draftsAreKeptPerManager() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions/notes/-/", json: Self.detail("notes"))
        let drafts = ExtensionDrafts()
        let mine = ExtensionModel(id: "notes", manager: "m-1", deps: .init(api: { self.client(hub) }, drafts: drafts))
        await mine.load()
        mine.set(.string("https://work.example.com"), at: ["base_url"])
        let theirs = ExtensionModel(id: "notes", deps: .init(api: { self.client(hub) }, drafts: drafts))
        await theirs.load()
        #expect(theirs.value(["base_url"]) == nil && !theirs.dirty)
        let again = ExtensionModel(id: "notes", manager: "m-1", deps: .init(api: { self.client(hub) }, drafts: drafts))
        await again.load()
        #expect(again.value(["base_url"]) == .string("https://work.example.com"))
    }

    @Test func anotherManagersSignInNamesItAndIsKeptApart() async throws {
        let hub = FakeHub()
        let base = "/api/v1/extensions/lookup/-/auth"
        hub.on("POST", base + "/step", json: #"{"step":"done","done":true}"#)
        hub.on("GET", base, json: #"{"title":"登录","state":"signed_in","account":"me@example.com"}"#)
        hub.on("POST", base + "/logout", json: #"{"title":"登录","state":"signed_out"}"#)
        let store = ExtensionAuthStore()
        store.received("lookup", ExtensionAuth(title: "登录", state: .signedOut))
        let model = ExtensionSignInModel(id: "lookup", manager: "m-1", deps: .init(api: { self.client(hub) }, store: store))
        await model.start()
        #expect(store.auth("lookup", manager: "m-1", else: nil)?.isSignedIn == true)
        #expect(store.auth("lookup", else: nil)?.state == .signedOut, "the default's copy is still signed out")
        await model.logout()
        #expect(store.auth("lookup", manager: "m-1", else: nil)?.state == .signedOut)
        #expect(!hub.requests.isEmpty && named(hub.requests).allSatisfy { $0 == "m-1" })
    }

    // MARK: Settings › Connectors

    @Test func connectorsGroupUnderEachManager() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/connectors", json: #"""
        {"connectors":[
          {"id":"mail","name":"邮件","status":"ok","manager":"assistant"},
          {"id":"github","name":"GitHub","status":"ok","manager":"assistant"},
          {"id":"imessage","name":"信息","status":"ok","manager":"assistant"},
          {"id":"mail","name":"邮件","status":"ok","manager":"m-2"},
          {"id":"imessage","name":"信息","status":"not_configured","manager":"m-2"}]}
        """#)
        hub.on("GET", "/api/v1/managers", json: Self.roster)
        let list = ConnectorsListModel(deps: .init(api: { hub.client }))
        await list.load(listed: true)
        let groups = try #require(list.groups)
        #expect(groups.map(\.id) == ["assistant", "m-2"], "工作 has none: its group is left out")
        #expect(groups[0].items.map(\.id) == ["github", "imessage"] && groups[1].items.map(\.id) == ["imessage"])
        #expect(list.rows(lark: true).map(\.id) == ["mail", "lark"], "mail and Lark keep their own rows; the rest go under the managers")
        #expect(list.manager(of: groups[1].items[0]) == "m-2" && list.manager(of: groups[0].items[0]) == nil)
        #expect(hub.sent("GET", "/api/v1/connectors").first?.query == ["all": "1"])

        // One manager: the section is as it was.
        hub.on("GET", "/api/v1/managers", json: Self.oneManager)
        await list.load(listed: true)
        #expect(list.groups == nil && list.rows(lark: true).map(\.id) == ["mail", "lark", "assistant/github", "assistant/imessage", "m-2/imessage"])

        // A hub without connectors (feature "connectors") isn't asked.
        let old = FakeHub()
        let none = ConnectorsListModel(deps: .init(api: { old.client }))
        await none.load(listed: false)
        #expect(old.requests.isEmpty && none.groups == nil)
    }

    /// Sign-in, the switches and the deletes of another manager's connector name it; the default's name none.
    @Test func anotherManagersConnectorNamesItsManager() async throws {
        for manager in ["m-2", nil] as [String?] {
            let hub = FakeHub()
            let base = "/api/v1/connectors/telegram"
            let connector = #"{"id":"telegram","name":"Telegram","status":"ok","enabled":true,"archive":true,"inbox":true,"capabilities":["login","archive","inbox"]}"#
            hub.on("POST", base + "/login", json: #"{"step":"code","fields":[{"name":"code","label":"Code"}]}"#)
            hub.on("POST", base + "/logout", json: #"{"ok":true}"#)
            hub.on("PUT", base + "/settings", json: connector)
            hub.on("DELETE", base + "/data", json: #"{"chats":1,"people":1,"inbox":1}"#)
            hub.on("DELETE", base + "/archive", json: #"{"chats":1,"people":1}"#)
            var reported: [String] = []
            let m = ConnectorSignInModel(id: "telegram", manager: manager,
                                         deps: ConnectorDeps(api: { hub.client }, report: { reported.append($0.localizedDescription) }))
            await m.start()
            _ = await m.setEnabled(false)
            _ = await m.setFeed(.inbox, on: false)
            await m.deleteData()
            await m.deleteArchive()
            await m.logout()
            #expect(reported.isEmpty)
            #expect(hub.requests.count == 6)
            #expect(named(hub.requests) == Array(repeating: manager, count: 6), "every request names \(manager ?? "no manager")")
        }
    }

    /// An extension's page shows the connectors it contributes from its own manager, and switches them there.
    @Test func anExtensionsConnectorsAreItsManagers() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/connectors", json: #"{"connectors":[{"id":"notes","name":"Notes","status":"ok","inbox":true}]}"#)
        hub.on("PUT", "/api/v1/connectors/notes/settings", json: #"{"id":"notes","name":"Notes","status":"ok","inbox":false}"#)
        let model = ExtensionConnectorsModel(manager: "m-1", deps: .init(api: { hub.client }, report: { _ in }))
        await model.load(ids: ["notes"])
        #expect(model.connectors.map(\.id) == ["notes"])
        await model.setInbox("notes", on: false)
        #expect(model.connectors.first?.inbox == false)
        #expect(named(hub.requests) == ["m-1", "m-1"])
    }

    @Test func theMessagesConnectorOfAnotherManagerNamesIt() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/imessage/chats", json: #"{"chats":[]}"#)
        hub.on("GET", "/api/v1/imessage/chats/c1/messages", json: #"{"chat":{"id":"c1","name":"Someone","members":[],"services":[],"last":"2026-11-08T05:00:00Z"},"messages":[]}"#)
        let chats = MessagesChatsModel(manager: "m-2", deps: .init(api: { hub.client }))
        await chats.load()
        let thread = MessagesThreadModel(chatID: "c1", manager: "m-2", deps: .init(api: { hub.client }))
        await thread.load()
        #expect(named(hub.requests) == ["m-2", "m-2"])
    }

    @Test func aClientNamesOnlyAManager() {
        let api = FakeHub().client
        #expect(api.manager(nil).managerID == nil && api.manager("").managerID == nil && api.manager("m-1").managerID == "m-1")
        #expect(api.manager("m-1").manager(nil).managerID == nil, "the default's again: nothing named")
    }
}
