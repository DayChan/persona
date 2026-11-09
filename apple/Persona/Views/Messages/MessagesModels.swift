import Foundation
import Observation

// Messages (docs/plans/plan-connectors.md「五」): one Mac's Messages, read through the hub. The app shows every conversation;
// with Hand to Persona on (the hub's switch, on by default) the real ones also go to the knowledge base, the inbox and the
// assistant. Nothing is marked read, nothing is sent.

/// GET /api/v1/imessage: the connector's status, with the steps on the Mac when it may not read.
nonisolated struct MessagesStatus: Codable, Hashable, Sendable {
    var machine: String
    var status: String
    var error: String?
    var guide: [String]?
    var via: String?
    /// Hand to Persona: the knowledge base, the inbox and the assistant read the real conversations (nil from a hub
    /// before it: they did not).
    var ai: Bool?
}

/// DELETE /api/v1/imessage/data: what the hub no longer keeps.
nonisolated struct MessagesForgotten: Codable, Hashable, Sendable {
    var chats: Int
    var people: Int
    var inbox: Int
}

nonisolated struct MessagesPerson: Codable, Hashable, Sendable {
    var handle: String
    var name: String?

    var shown: String { (name?.isEmpty == false ? name : nil) ?? handle }
}

/// A conversation: a person's chats over iMessage, SMS and RCS, or a group.
nonisolated struct MessagesChat: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var name: String
    var group: Bool?
    var members: [MessagesPerson]
    var services: [String]
    var last: Date
    var preview: String?
    var fromMe: Bool?
    var unread: Int?

    enum CodingKeys: String, CodingKey {
        case id, name, group, members, services, last, preview, unread
        case fromMe = "from_me"
    }

    var isGroup: Bool { group ?? false }
}

nonisolated struct MessagesAttachment: Codable, Hashable, Sendable {
    var kind: String // image | video | audio | file
    var name: String?
    var mime: String?
    var size: Int64?
}

nonisolated struct MessagesMessage: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var at: Date
    var kind: String // message | event
    var fromMe: Bool?
    var sender: MessagesPerson?
    var text: String?
    var service: String?
    var attachments: [MessagesAttachment]?
    var replyTo: String?
    var edited: Bool?
    var unsent: Bool?
    var unread: Bool?
    var undecoded: Bool?

    enum CodingKeys: String, CodingKey {
        case id, at, kind, sender, text, service, attachments, edited, unsent, unread, undecoded
        case fromMe = "from_me", replyTo = "reply_to"
    }

    var isMine: Bool { fromMe ?? false }
    var isEvent: Bool { kind == "event" }
}

nonisolated struct MessagesChatsPage: Codable, Sendable {
    var chats: [MessagesChat]
    var more: Bool?
    var before: String?
}

nonisolated struct MessagesPage: Codable, Sendable {
    var chat: MessagesChat
    var messages: [MessagesMessage]
    var more: Bool?
    var before: String?
}

extension APIClient {
    func messagesStatus() async throws -> MessagesStatus { try await get("/api/v1/imessage") }

    func setMessagesAI(_ on: Bool) async throws -> MessagesStatus {
        struct Body: Encodable { var ai: Bool }
        return try JSON.decoder.decode(MessagesStatus.self, from: await request("PUT", "/api/v1/imessage/settings", body: Body(ai: on)))
    }

    func deleteMessagesData() async throws -> MessagesForgotten {
        try JSON.decoder.decode(MessagesForgotten.self, from: await request("DELETE", "/api/v1/imessage/data"))
    }

    func messagesChats(before: String? = nil) async throws -> MessagesChatsPage {
        var q = ["limit": "50"]
        if let before { q["before"] = before }
        return try await get("/api/v1/imessage/chats", query: q)
    }

    func messages(of chat: String, before: String? = nil) async throws -> MessagesPage {
        var q = ["limit": "50"]
        if let before { q["before"] = before }
        return try await get("/api/v1/imessage/chats/\(chat)/messages", query: q)
    }
}

/// What a failed read means for the page: the Mac needs a permission, is away, or something else went wrong.
nonisolated enum MessagesFailure: Equatable, Sendable {
    case noPermission(String)
    case other(String)

    init?(_ error: Error) {
        guard let shown = error.shown else { return nil }
        self = (error as? APIError)?.code == "no_permission" ? .noPermission(shown) : .other(shown)
    }

    var text: String {
        switch self {
        case .noPermission(let s), .other(let s): s
        }
    }
}

/// Settings › Connectors › Messages › Sessions: the Mac's conversations, newest first, a page at a time.
@Observable final class MessagesChatsModel {
    struct Deps {
        var api: () -> APIClient?
        static var live: Deps { let app = AppModel.shared; return .init(api: { app.api }) }
    }

    private(set) var chats: [MessagesChat] = []
    private(set) var loaded = false
    private(set) var loading = false
    private(set) var failure: MessagesFailure?
    private(set) var more = false
    /// Whose Messages (another manager's connector: every request names it); nil: the default manager's.
    let manager: String?
    @ObservationIgnored private var before: String?
    @ObservationIgnored private let deps: Deps

    init(manager: String? = nil, deps: Deps = .live) {
        self.manager = manager
        self.deps = deps
    }

    private var api: APIClient? { deps.api()?.manager(manager) }

    /// The first page again (on open and on pull to refresh).
    func load() async {
        guard let api, !loading else { return }
        loading = true
        defer { loading = false }
        do {
            let page = try await api.messagesChats()
            chats = page.chats
            more = page.more ?? false
            before = page.before
            failure = nil
        } catch {
            if let f = MessagesFailure(error) { failure = f }
        }
        loaded = true
    }

    /// The next page, when the list reaches its end.
    func loadMore() async {
        guard let api, more, !loading, let before else { return }
        loading = true
        defer { loading = false }
        do {
            let page = try await api.messagesChats(before: before)
            let seen = Set(chats.map(\.id))
            chats += page.chats.filter { !seen.contains($0.id) }
            more = page.more ?? false
            self.before = page.before
        } catch {
            if let f = MessagesFailure(error) { failure = f }
        }
    }

    func filtered(_ query: String) -> [MessagesChat] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return chats }
        return chats.filter { c in
            c.name.localizedCaseInsensitiveContains(q) || c.members.contains { $0.shown.localizedCaseInsensitiveContains(q) || $0.handle.contains(q) }
        }
    }
}

/// One conversation's messages, oldest first; older pages are put in front.
@Observable final class MessagesThreadModel {
    let chatID: String
    private(set) var chat: MessagesChat?
    private(set) var messages: [MessagesMessage] = []
    private(set) var loaded = false
    private(set) var loading = false
    private(set) var failure: MessagesFailure?
    private(set) var more = false
    /// Whose Messages (another manager's connector: every request names it); nil: the default manager's.
    let manager: String?
    @ObservationIgnored private var before: String?
    @ObservationIgnored private let deps: MessagesChatsModel.Deps

    init(chatID: String, manager: String? = nil, deps: MessagesChatsModel.Deps = .live) {
        self.chatID = chatID
        self.manager = manager
        self.deps = deps
    }

    private var api: APIClient? { deps.api()?.manager(manager) }

    func load() async {
        guard let api, !loading else { return }
        loading = true
        defer { loading = false }
        do {
            let page = try await api.messages(of: chatID)
            chat = page.chat
            messages = page.messages
            more = page.more ?? false
            before = page.before
            failure = nil
        } catch {
            if let f = MessagesFailure(error) { failure = f }
        }
        loaded = true
    }

    func loadOlder() async {
        guard let api, more, !loading, let before else { return }
        loading = true
        defer { loading = false }
        do {
            let page = try await api.messages(of: chatID, before: before)
            let seen = Set(messages.map(\.id))
            messages = page.messages.filter { !seen.contains($0.id) } + messages
            more = page.more ?? false
            self.before = page.before
        } catch {
            if let f = MessagesFailure(error) { failure = f }
        }
    }
}
