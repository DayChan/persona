import Foundation

/// Where Hand to AI last sent an item from each source (#2121; docs/assistant/inbox.md "Item actions";
/// spec/fixtures/inbox/handoff_memory.json): the repository, else the chat, else the sender (the item's `mutes`), in the
/// hub group. The next item from that source starts on that target, marked Last time; nothing is sent until the user
/// sends. Local to the device: ids only, the `limit` most recent sources, never synced, gone with the group (`forget`).
struct InboxHandOffMemory {
    static let limit = 200
    static let base = "inbox.handOffBySource"
    /// Most specific first: an item from a repository goes by the repository, not by who wrote it.
    static let tags = ["repo", "chat", "sender"]

    struct Entry: Codable, Equatable {
        var source: String
        /// `InboxHandOffModel.Target.stored`.
        var target: String
    }

    /// What to preselect for an item: the target, whether it is the remembered one (Last time), and whether a
    /// remembered Agent was gone so the main chat stands in (said once).
    struct Resolved: Equatable {
        var target: InboxHandOffModel.Target
        var lastTime: Bool
        var gone: Bool
    }

    var group: String
    var defaults: UserDefaults = .standard

    private var name: String { PinsStore.name(Self.base, group) }

    /// The item's source key ("github|repo:example/weather"); nil when it has none of the three (a Lark doc, …).
    static func key(source: String, mutes: [InboxItem.MuteOption]?) -> String? {
        for tag in tags {
            if let value = mutes?.first(where: { $0.tag == tag && !$0.value.isEmpty })?.value { return "\(source)|\(tag):\(value)" }
        }
        return nil
    }

    static func key(_ item: InboxItem) -> String? { key(source: item.source, mutes: item.mutes) }

    /// Latest first, one per source.
    var entries: [Entry] {
        guard let data = defaults.data(forKey: name), let list = try? JSON.decoder.decode([Entry].self, from: data) else { return [] }
        return list
    }

    func recall(_ key: String) -> String? { entries.first { $0.source == key }?.target }

    func remember(_ key: String, target: InboxHandOffModel.Target) {
        var list = entries.filter { $0.source != key }
        list.insert(Entry(source: key, target: target.stored), at: 0)
        guard let data = try? JSON.encoder.encode(Array(list.prefix(Self.limit))) else { return }
        defaults.set(data, forKey: name)
    }

    /// The remembered target while it is still there. A remembered Agent no longer listed resolves to the main chat
    /// (`gone`); nothing remembered, or a value that doesn't read, is nil: the place's own default.
    static func resolve(_ stored: String?, isListed: (SessionKey) -> Bool) -> Resolved? {
        guard let stored, stored == "main" || stored == "new" || stored.hasPrefix("agent:") else { return nil }
        let target = InboxHandOffModel.Target(stored: stored)
        if stored.hasPrefix("agent:") {
            guard case .agent(let key) = target else { return nil } // "agent:" with no session
            if !isListed(key) { return Resolved(target: .main, lastTime: false, gone: true) }
        }
        return Resolved(target: target, lastTime: true, gone: false)
    }

    /// What to preselect for the item in this group, if anything.
    func resolved(for item: InboxItem, isListed: (SessionKey) -> Bool) -> Resolved? {
        guard let key = Self.key(item) else { return nil }
        return Self.resolve(recall(key), isListed: isListed)
    }

    /// A group that is gone forgets where its items went.
    static func forget(group: String, defaults: UserDefaults = .standard) {
        defaults.removeObject(forKey: PinsStore.name(base, group))
    }
}
