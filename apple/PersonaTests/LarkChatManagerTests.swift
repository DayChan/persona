import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// An inbox item's chat reads, sends and summarizes through the item's manager (docs/assistant/managers.md "Grouping"):
/// every request names it, a person or direct chat opened from it stays on it, the user it reads as, the messages it
/// keeps, its emoji and its messaging source are its own, the pictures and media the chat loads by URL name it too, and
/// what opens inside it reads through the chat's group of hubs. A Moments post pushed from an item keeps the item's
/// manager. The default's chat names none, as before. Made-up values.
@MainActor
struct LarkChatManagerTests {
    private static let header = "X-Persona-Manager"
    private static let messages = "/api/v1/lark/conversations/f1/messages"

    /// A client like the app's for the hub-level group, on a host of its own (the messaging source is kept per host).
    private func client(_ hub: FakeHub) -> APIClient {
        var c = hub.client
        c.config = ServerConfig(url: URL(string: "https://chat-manager.invalid")!, token: "t")
        return c
    }

    private func fakeHub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", Self.messages, json: #"{"messages":[],"has_older":false}"#)
        hub.on("POST", Self.messages, json: "{}")
        hub.on("POST", "/api/v1/assistant/conversations", json: #"{"session":{"machine_id":"server","session_id":"s-1"}}"#)
        return hub
    }

    /// A model paired with one hub per group ("" the hub-level one), each answered by its own FakeHub, on hosts of their
    /// own (no messaging source kept for them).
    private func model(_ hubs: [String: FakeHub]) throws -> AppModel {
        let run = UUID().uuidString.lowercased()
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "lark-chat-manager-" + run))
        model.registry = HubRegistry(hubs: hubs.keys.sorted().map { group in
            PairedHub(id: "box-\(group)-\(run)", config: ServerConfig(url: URL(string: "https://lark-\(group)-\(run).invalid")!, token: "t"), group: group)
        })
        model.buildLinks()
        for (group, hub) in hubs { try #require(model.link(group)).transportForTests = hub.client.transport }
        return model
    }

    // MARK: requests

    @Test func theChatReadsAndSendsThroughItsItemsManager() async throws {
        let hub = fakeHub()
        let api = try #require(LarkChatView.client(client(hub), manager: "m-2", account: 2))
        _ = try await api.larkMessages("f1")
        _ = try await api.larkSend("f1", text: "On my way")
        let read = hub.sent("GET", Self.messages), sent = hub.sent("POST", Self.messages)
        #expect(read.map { $0.headers[Self.header] } == ["m-2"])
        #expect(sent.map { $0.headers[Self.header] } == ["m-2"])
        #expect(read.first?.query["ol_account"] == "2", "still the chat's account")
    }

    @Test func theSummaryStartsOnTheChatsManager() async throws {
        let hub = fakeHub()
        // The app's client while the Persona page shows yet another manager: the chat's names its own.
        let api = try #require(LarkChatView.client(client(hub).showing("m-3"), manager: "m-2", account: nil))
        let s = try await api.showing(nil).startConversation(prompt: "Summarize this chat")
        #expect(hub.sent("POST", "/api/v1/assistant/conversations").map { $0.headers[Self.header] } == ["m-2"])
        #expect(s.manager == "m-2", "the conversation is the chat's manager's")
    }

    @Test func theDefaultsChatNamesNone() async throws {
        let hub = fakeHub()
        // As the app's client is while the Persona page shows another manager.
        let api = try #require(LarkChatView.client(client(hub).showing("m-2"), manager: nil, account: nil))
        _ = try await api.larkMessages("f1")
        _ = try await api.larkSend("f1", text: "On my way")
        _ = try await api.showing(nil).startConversation(prompt: "Summarize this chat")
        let all = hub.sent("GET", Self.messages) + hub.sent("POST", Self.messages) + hub.sent("POST", "/api/v1/assistant/conversations")
        #expect(all.count == 3 && all.allSatisfy { $0.headers[Self.header] == nil }, "the default's names none")
    }

    /// Two managers may be signed in as two users: the user one's chat reads as (which messages are mine, whom a send
    /// expects) is never the other's, the default's included.
    @Test func eachManagersChatKeepsItsOwnUser() async throws {
        let hub = FakeHub(), me = "/api/v1/messaging/chat/me"
        hub.on("GET", me) { r in Data((r.headers["X-Persona-Manager"] == "m-2" ? #"{"user_id":"u-2"}"# : #"{"user_id":"u-1"}"#).utf8) }
        var api = hub.client
        api.config = ServerConfig(url: URL(string: "https://chat-user-\(UUID().uuidString.lowercased()).invalid")!, token: "t")
        let named = try #require(LarkChatView.client(api, manager: "m-2", account: nil))
        let home = try #require(LarkChatView.client(api, manager: nil, account: nil))
        let read = try await named.messagingUser("chat", refresh: true)
        let homeRead = try await home.messagingUser("chat")
        let homeRefreshed = try await home.messagingUser("chat", refresh: true)
        let namedKept = try await named.messagingUser("chat")
        #expect(read == "u-2")
        #expect(homeRead == "u-1", "the default's asks for its own")
        #expect(homeRefreshed == "u-1")
        #expect(namedKept == "u-2", "the default's doesn't replace the named one's")
        #expect(hub.sent("GET", me).count == 3)
    }

    // MARK: pages opened from the chat

    @Test func theRouteToAChatCarriesItsManager() {
        let chat = LarkConversation(id: "oc_1", name: "Ada", isGroup: false)
        #expect(FeishuRoute.chat(chat, manager: "m-2").manager == "m-2")
        #expect(FeishuRoute.chat(chat).manager == nil, "the default's: nothing named")
        #expect(FeishuRoute.chat(chat, manager: "m-2") != FeishuRoute.chat(chat), "two managers' copies are two pages")
    }

    /// A person opened from the chat's document or from another profile, on a stack outside the chat.
    @Test func theRouteToAPersonCarriesItsManager() {
        let ada = LarkPersonRef(id: "ou_1", name: "Ada")
        #expect(FeishuRoute.profile(ada, manager: "m-2").manager == "m-2")
        #expect(FeishuRoute.profile(ada).manager == nil, "the default's: nothing named")
        #expect(FeishuRoute.profile(ada, manager: "m-2") != FeishuRoute.profile(ada), "two managers' copies are two pages")
    }

    // MARK: messages kept on disk

    @Test func aNamedManagersPageKeepsItsOwnMessages() {
        let chat = LarkConversation(id: "oc_1", name: "Ada", isGroup: false)
        #expect(LarkMessage.cacheName(chat, manager: nil) == "lark-chat-" + chat.key, "the default's: the one background sync keeps")
        #expect(LarkMessage.cacheName(chat, manager: "m-2") != LarkMessage.cacheName(chat), "never the default's copy")
        #expect(LarkMessage.cacheName(chat, manager: "m-2") != LarkMessage.cacheName(chat, manager: "m-3"))
        #expect(LarkMessage.cacheName(chat, manager: "m-2").hasPrefix("lark-chat-"), "cleared with the rest when what chats show changes")
    }

    // MARK: pictures and media

    #if os(iOS)
    /// The picture view itself, in a window: its bytes are kept on disk only under the URL that names the chat's manager,
    /// so it draws only when it asks for that one (the unnamed path has nothing on disk and no hub to answer it).
    @Test func aPictureInTheChatLoadsThroughItsManager() async throws {
        let path = "/api/v1/lark/raw/images/chat-manager-" + UUID().uuidString.lowercased()
        let named = LarkMedia.url(path, manager: "m-2")
        let kept = URL.cachesDirectory.appending(path: "lark-media", directoryHint: .isDirectory)
            .appending(path: LarkMedia.cacheKey(named, size: nil))
        try FileManager.default.createDirectory(at: kept.deletingLastPathComponent(), withIntermediateDirectories: true)
        try UIGraphicsImageRenderer(size: CGSize(width: 2, height: 2)).pngData { _ in }.write(to: kept)
        defer { try? FileManager.default.removeItem(at: kept) }

        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 200, height: 200))
        window.rootViewController = UIHostingController(rootView: LarkRemoteImage(url: path).frame(width: 40, height: 40)
            .environment(\.larkManager, "m-2"))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        for _ in 0..<150 where LarkMedia.shared.cached(named) == nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(LarkMedia.shared.cached(named) != nil, "loaded through the chat's manager")
        #expect(LarkMedia.shared.cached(path) == nil, "never through the default's")
    }
    #endif

    @Test func thePlayerStreamsThroughTheChatsManager() throws {
        let hub = try #require(URL(string: "https://hub.example.com"))
        let path = "/api/v1/lark/media/video_1"
        #expect(LarkMedia.streamURL(path, hub: hub, manager: "m-2").absoluteString == "https://hub.example.com/api/v1/lark/media/video_1?manager=m-2")
        #expect(LarkMedia.streamURL(path, hub: hub, manager: "m 2&x").absoluteString == "https://hub.example.com/api/v1/lark/media/video_1?manager=m%202%26x")
        #expect(LarkMedia.streamURL(path, hub: hub, manager: nil).absoluteString == "https://hub.example.com/api/v1/lark/media/video_1", "the default's: none named")
        #expect(LarkMedia.streamURL(path, hub: hub, manager: "").absoluteString == "https://hub.example.com/api/v1/lark/media/video_1")
        #expect(LarkMedia.streamURL("https://cdn.example.com/v.mp4", hub: hub, manager: "m-2").absoluteString == "https://cdn.example.com/v.mp4", "a CDN's: as it is")
    }

    @Test func thePlayerKeepsTheMediaPathsQuery() throws {
        let hub = try #require(URL(string: "https://hub.example.com"))
        let path = "/api/v1/messaging/chat/media/video_1?account=2"
        #expect(LarkMedia.streamURL(path, hub: hub, manager: "m-2").absoluteString
                == "https://hub.example.com/api/v1/messaging/chat/media/video_1?account=2&manager=m-2", "the account still named")
        #expect(LarkMedia.streamURL("/api/v1/lark/media/video_1?manager=m-3", hub: hub, manager: "m-2").absoluteString
                == "https://hub.example.com/api/v1/lark/media/video_1?manager=m-3", "one that names a manager already: named once")
        // A host no other suite routes through a tunnel: streamURL reads HubTransport's routes, which suites share.
        let under = try #require(URL(string: "https://player-query.example.com/persona/"))
        #expect(LarkMedia.streamURL(path, hub: under, manager: "m-2").absoluteString
                == "https://player-query.example.com/persona/api/v1/messaging/chat/media/video_1?account=2&manager=m-2", "a hub under a path")
        #expect(LarkMedia.streamURL(path, hub: hub, manager: nil).absoluteString
                == "https://hub.example.com/api/v1/messaging/chat/media/video_1?account=2", "the default's: its query a query, not %3F in the path")
        #expect(!LarkMedia.streamURL("//cdn.example.com/v.mp4", hub: hub, manager: "m-2").absoluteString.contains("manager="),
                "another host's: url(_:manager:) leaves it alone")
    }

    // MARK: emoji and the messaging source

    /// Each manager's Lark has its own emoji and quick reactions: a named manager's chat reads its own, naming it, and the
    /// default's stay the ones read without a name.
    @Test func eachManagerReadsItsOwnEmoji() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/lark/emojis") { r in
            Data((r.headers[Self.header] == "m-2"
                  ? #"{"emojis":{"wave":{"label":"Wave","url":"/api/v1/lark/raw/emoji/wave"}},"groups":[{"title":"Work","keys":["WAVE"]}]}"#
                  : #"{"emojis":{"ok":{"label":"OK","url":"/api/v1/lark/raw/emoji/ok"}},"groups":[{"title":"Home","keys":["OK"]}]}"#).utf8)
        }
        hub.on("GET", "/api/v1/lark/reactions/quick") { r in Data((r.headers[Self.header] == "m-2" ? #"{"keys":["WAVE"]}"# : #"{"keys":["OK"]}"#).utf8) }
        let media = LarkMedia(), api = client(hub)
        await media.loadEmojiCatalog(manager: "m-2", using: api)?.value
        await media.loadEmojiCatalog(manager: nil, using: api)?.value
        #expect(hub.sent("GET", "/api/v1/lark/emojis").map { $0.headers[Self.header] } == ["m-2", nil], "the default's names none")
        #expect(media.catalog("m-2").quickReactions == ["WAVE"] && media.catalog("m-2").groups.map(\.title) == ["Work"])
        #expect(media.emoji("WAVE", manager: "m-2")?.label == "Wave" && media.emoji("OK", manager: "m-2") == nil)
        #expect(media.catalog(nil).quickReactions == ["OK"] && media.catalog(nil).groups.map(\.title) == ["Home"], "the default's are its own")
        #expect(media.emoji("OK")?.label == "OK" && media.emoji("WAVE") == nil)
    }

    /// Each manager has its own extensions, so its own messaging source: a named manager's chat reads the list through it,
    /// once, and goes through that source, never the default's; the default's stays the hub's.
    @Test func aNamedManagersChatGoesThroughItsOwnSource() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/messaging") { r in
            Data((r.headers[Self.header] == "m-2" ? #"{"sources":[{"extension":"work-chat","id":"work","name":"Work"}]}"# : #"{"sources":[]}"#).utf8)
        }
        for path in ["/api/v1/messaging/work/emojis", "/api/v1/messaging/chat/emojis"] { hub.on("GET", path, json: #"{"emojis":{},"groups":[]}"#) }
        var api = hub.client
        api.config = ServerConfig(url: URL(string: "https://source-\(UUID().uuidString.lowercased()).invalid")!, token: "t")
        MessagingSources.set("chat", for: api.config.url) // the hub's, as its hello made it
        defer { MessagingSources.set(nil, for: api.config.url) }
        let named = try #require(LarkChatView.client(api, manager: "m-2", account: nil))
        let home = try #require(LarkChatView.client(api, manager: nil, account: nil))
        #expect(named.messagingSource == nil, "not the default's before its own is read")
        await named.readMessagingSource(features: ["messaging"])
        await named.readMessagingSource(features: ["messaging"])
        #expect(hub.sent("GET", "/api/v1/messaging").map { $0.headers[Self.header] } == ["m-2"], "read through it, once")
        #expect(named.messagingSource == "work" && home.messagingSource == "chat")
        _ = try await named.larkEmojis()
        _ = try await home.larkEmojis()
        #expect(hub.sent("GET", "/api/v1/messaging/work/emojis").map { $0.headers[Self.header] } == ["m-2"])
        #expect(hub.sent("GET", "/api/v1/messaging/chat/emojis").map { $0.headers[Self.header] } == [nil])

        let other = try #require(LarkChatView.client(api, manager: "m-3", account: nil))
        await other.readMessagingSource(features: [])
        #expect(hub.sent("GET", "/api/v1/messaging").count == 1 && other.messagingSource == nil, "a hub without the feature: the old API")
    }

    /// An empty list (an extension restarting lists no source for a moment) is not kept: the next open asks again.
    @Test func anEmptySourceListIsAskedForAgain() async throws {
        let hub = FakeHub()
        var asked = 0
        hub.on("GET", "/api/v1/messaging") { _ in
            asked += 1
            return Data((asked > 1 ? #"{"sources":[{"extension":"work-chat","id":"work","name":"Work"}]}"# : #"{"sources":[]}"#).utf8)
        }
        var api = hub.client
        api.config = ServerConfig(url: URL(string: "https://empty-source-\(UUID().uuidString.lowercased()).invalid")!, token: "t")
        let named = try #require(LarkChatView.client(api, manager: "m-2", account: nil))
        await named.readMessagingSource(features: ["messaging"])
        #expect(named.messagingSource == nil, "none listed yet: the old API")
        await named.readMessagingSource(features: ["messaging"])
        #expect(asked == 2, "asked again")
        #expect(named.messagingSource == "work")
    }

    @Test func thePlayerStreamsThroughTheHubsTunnel() throws {
        let hub = try #require(URL(string: "https://player-tunnel.example.com/persona/"))
        let tunnel = try #require(URL(string: "http://127.0.0.1:52101"))
        HubTransport.route(hub, via: tunnel)
        defer { HubTransport.route(hub, via: nil) }
        let path = "/api/v1/messaging/chat/media/video_1?account=2"
        #expect(LarkMedia.streamURL(path, hub: hub, manager: nil).absoluteString
                == "http://127.0.0.1:52101/persona/api/v1/messaging/chat/media/video_1?account=2", "the default's")
        #expect(LarkMedia.streamURL(path, hub: hub, manager: nil).absoluteString
                == APIClient(config: ServerConfig(url: hub, token: "t")).url("/api/v1/messaging/chat/media/video_1", query: ["account": "2"]).absoluteString,
                "where APIClient's requests to that hub go")
        #expect(LarkMedia.streamURL(path, hub: hub, manager: "m-2").absoluteString
                == "http://127.0.0.1:52101/persona/api/v1/messaging/chat/media/video_1?account=2&manager=m-2", "a named manager's")
        #expect(LarkMedia.streamURL("https://cdn.example.com/v.mp4", hub: hub, manager: nil).absoluteString == "https://cdn.example.com/v.mp4",
                "a CDN's: straight to it")
    }

    #if os(iOS)
    // MARK: pages inside the chat

    /// What opens inside a chat (here a person's profile) reads through the chat's group of hubs, naming its manager, never
    /// through the hub-level group's.
    @Test func aProfileInTheChatReadsThroughItsGroup() async throws {
        let home = FakeHub(), other = FakeHub(), path = "/api/v1/lark/users/ou_1"
        for hub in [home, other] { hub.on("GET", path, json: #"{"name":"Ada"}"#) }
        let model = try model(["": home, "g2": other])
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 700))
        window.rootViewController = UIHostingController(rootView: LarkProfileView(person: LarkPersonRef(id: "ou_1", name: "Ada"))
            .environment(\.larkManager, "m-2").environment(\.larkGroup, "g2").environment(model))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        for _ in 0..<150 where other.sent("GET", path).isEmpty && home.sent("GET", path).isEmpty { try await Task.sleep(for: .milliseconds(20)) }
        #expect(other.sent("GET", path).map { $0.headers[Self.header] } == ["m-2"], "the chat's group, naming its manager")
        #expect(home.sent("GET", path).isEmpty, "never the hub-level group's")
    }

    /// A Moments post an inbox item opens is pushed, so it inherits nothing from the inbox: what it draws (here an emoji in
    /// the post) is still its manager's, from that manager's catalog under the URL that names it.
    @Test func aPushedMomentsPostDrawsItsManagersEmoji() async throws {
        let manager = "m-" + String(UUID().uuidString.lowercased().prefix(6)) // a catalog of its own in LarkMedia.shared
        let picture = "/api/v1/lark/raw/emoji/moments-" + UUID().uuidString.lowercased()
        let hub = FakeHub()
        hub.on("GET", "/api/v1/lark/emojis", json: #"{"emojis":{"wave":{"label":"Wave","url":"\#(picture)"}},"groups":[]}"#)
        hub.on("GET", "/api/v1/lark/reactions/quick", json: #"{"keys":[]}"#)
        hub.on("GET", "/api/v1/lark/moments/posts/7", json: #"{"id":"7","userName":"Ada","content":{"segments":[{"kind":"emoji","key":"WAVE"}]}}"#)
        hub.on("GET", "/api/v1/lark/moments/posts/7/comments", json: #"{"comments":[]}"#)
        let named = LarkMedia.url(picture, manager: manager)
        let kept = URL.cachesDirectory.appending(path: "lark-media", directoryHint: .isDirectory)
            .appending(path: LarkMedia.cacheKey(named, size: nil))
        try FileManager.default.createDirectory(at: kept.deletingLastPathComponent(), withIntermediateDirectories: true)
        try UIGraphicsImageRenderer(size: CGSize(width: 2, height: 2)).pngData { _ in }.write(to: kept)
        defer { try? FileManager.default.removeItem(at: kept) }

        let model = try model(["": hub])
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 700))
        window.rootViewController = UIHostingController(rootView: NavigationStack {
            LarkMomentsPostView(postID: "7", pushed: true, manager: manager)
        }.environment(model))
        window.makeKeyAndVisible()
        defer { window.isHidden = true; window.rootViewController = nil }
        for _ in 0..<150 where LarkMedia.shared.cached(named) == nil { try await Task.sleep(for: .milliseconds(20)) }
        #expect(hub.sent("GET", "/api/v1/lark/moments/posts/7").map { $0.headers[Self.header] } == [manager])
        #expect(LarkMedia.shared.cached(named) != nil, "the post's emoji are its manager's")
    }

    // MARK: opening a chat

    /// `view` on screen; hidden again by `close`.
    private func show(_ view: some View, model: AppModel) -> UIWindow {
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 390, height: 700))
        window.rootViewController = UIHostingController(rootView: NavigationStack { view }.environment(model))
        window.makeKeyAndVisible()
        return window
    }

    private func close(_ window: UIWindow) { window.isHidden = true; window.rootViewController = nil }

    /// The default's chat asks for its display settings (with its draft and emoji) before it paints its cached messages: an
    /// unread chat painted from the cache waits for its scroll there, and they don't wait with it.
    @Test func theDefaultsChatReadsItsSettingsBeforeItsCachePaints() async throws {
        let id = "oc_paint_" + UUID().uuidString.lowercased().prefix(8)
        var chat = LarkConversation(id: id, name: "Ada", isGroup: false)
        chat.unread = 1
        chat.readPosition = "10"
        let cache = LarkMessage.cacheName(chat)
        StateCache.save(cache, [10, 11].map { LarkMessage(id: "m\($0)", position: String($0), senderName: "Ada", isMine: false, type: "TEXT",
                                                         text: "Hello", recalled: false, replyCount: 0) })
        defer { StateCache.remove(cache) }
        for _ in 0..<100 where LarkMessage.loadCached(cache) == nil { try await Task.sleep(for: .milliseconds(20)) }
        try #require(LarkMessage.loadCached(cache)?.count == 2, "the cache the chat paints from")

        let hub = FakeHub()
        var displayAt: Date?, messagesAt: Date?
        hub.on("GET", "/api/v1/lark/message-display") { _ in
            if displayAt == nil { displayAt = .now }
            throw APIError(status: 404, message: "not here")
        }
        hub.on("GET", "/api/v1/lark/conversations/\(id)/messages") { _ in
            if messagesAt == nil { messagesAt = .now }
            return Data(#"{"messages":[],"has_older":false}"#.utf8)
        }
        let window = show(LarkChatView(conversation: chat), model: try model(["": hub]))
        defer { close(window) }
        for _ in 0..<150 where messagesAt == nil { try await Task.sleep(for: .milliseconds(20)) }
        let display = try #require(displayAt), messages = try #require(messagesAt)
        #expect(messages.timeIntervalSince(display) > 0.2, "asked for before the cache's scroll, not after it")
    }

    /// A named manager's chat reads its own messaging source before anything else it asks for, so that its settings, draft,
    /// emoji and messages all go through that source and never through the old API.
    @Test func aNamedManagersChatReadsItsSourceFirst() async throws {
        let manager = "m-" + String(UUID().uuidString.lowercased().prefix(6)), id = "oc_source_" + UUID().uuidString.lowercased().prefix(8)
        let hub = FakeHub()
        hub.on("GET", "/api/v1/messaging") { r in
            Data((r.headers[Self.header] == manager ? #"{"sources":[{"extension":"work-chat","id":"work","name":"Work"}]}"# : #"{"sources":[]}"#).utf8)
        }
        hub.on("GET", "/api/v1/messaging/work/me", json: #"{"user_id":"u-2"}"#) // whose messages are mine
        hub.on("GET", "/api/v1/messaging/work/conversations/\(id)/messages", json: #"{"messages":[]}"#)
        let model = try model(["": hub]), link = try #require(model.link("")), answer = try #require(hub.client.transport)
        link.received(HubHello(features: ["messaging"]))
        // The list comes slowly, so a read that doesn't wait for it would go first.
        link.transportForTests = { request in
            if request.path == "/api/v1/messaging" { try await Task.sleep(for: .milliseconds(300)) }
            return try await answer(request)
        }
        let window = show(LarkChatView(conversation: LarkConversation(id: id, name: "Ada", isGroup: false), manager: manager), model: model)
        defer { close(window) }
        let mine = { hub.requests.filter { $0.headers[Self.header] == manager } }
        for _ in 0..<150 where !mine().contains(where: { $0.path.hasSuffix("/messages") }) { try await Task.sleep(for: .milliseconds(20)) }
        #expect(mine().first?.path == "/api/v1/messaging", "its source first")
        #expect(mine().filter { $0.path == "/api/v1/messaging" }.count == 1)
        #expect(!mine().contains { $0.path.hasPrefix("/api/v1/lark/") }, "nothing through the old API")
        #expect(mine().contains { $0.path == "/api/v1/messaging/work/conversations/\(id)/messages" })
    }
    #endif
}
