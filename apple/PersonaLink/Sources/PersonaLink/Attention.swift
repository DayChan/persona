import Foundation

/// The hub's "Needs you" summary, as persona-link puts it in its status (`attention`, docs/operations/link.md "Menu bar
/// App"). Absent: no summary on this connection yet, or a hub that sends none; the menu then shows no section.
struct LinkAttention: Decodable, Equatable {
    /// The web app's "Needs you" count, muted sessions included. `items` holds the latest ones (by `since`, newest
    /// first), `more` counts the older rest.
    var needsYou: Int
    var more = 0
    /// Where the hub's web app is: its https public URL (sub-path included), else in active mode the hub address the
    /// link went to; nil when the manager knows neither.
    var webURL: String?
    var items: [Item]

    /// One session that needs you, by its oldest card (which may be a question the assistant is still deciding on).
    struct Item: Decodable, Equatable, Identifiable {
        var machineID: String
        var sessionID: String
        var title = ""
        var place = ""
        var kind = "" // permission | plan | question | screen
        var detail = ""
        /// The session is muted: listed dimmed.
        var muted = false
        /// It makes a notification: the phone's push rule, worked out by the manager (not for a muted session, nor for a
        /// question the assistant is still deciding on). Missing: false.
        var notify = false
        /// The card; nil for a session waiting in a terminal dialog without one.
        var interactionID: String?
        /// The web app's path for the session (`/s/<machine>/<session>`, percent-encoded).
        var route = ""

        /// The notification's identifier: the web app's tag for the same event (`<agentKey>#needs`), one per session.
        var id: String { "\(machineID)/\(sessionID)#needs" }
        /// The card it waits on, as notifications remember it: "" for a terminal dialog without one.
        var card: String { interactionID ?? "" }

        enum CodingKeys: String, CodingKey {
            case title, place, kind, detail, muted, notify, route
            case machineID = "machine_id"
            case sessionID = "session_id"
            case interactionID = "interaction_id"
        }
    }

    enum CodingKeys: String, CodingKey {
        case more, items
        case needsYou = "needs_you"
        case webURL = "web_url"
    }

    /// How many sessions the menu does not list when it shows the first `shown` items.
    func hidden(shown: Int) -> Int { max(0, items.count - shown) + more }

    /// The count beside the menu bar icon; nil when nothing needs you.
    static func countText(_ n: Int) -> String? {
        n <= 0 ? nil : n > 99 ? "99+" : String(n)
    }
}

// Keys the hub may leave out take the defaults above (a synthesized init would require every non-optional key, so one
// missing `more` or an `items: null` would drop the whole summary). In extensions, so the memberwise initializers stay.
extension LinkAttention {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        needsYou = try c.decode(Int.self, forKey: .needsYou)
        more = try c.decodeIfPresent(Int.self, forKey: .more) ?? 0
        webURL = try c.decodeIfPresent(String.self, forKey: .webURL)
        items = try c.decodeIfPresent([Item].self, forKey: .items) ?? []
    }
}

extension LinkAttention.Item {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        machineID = try c.decode(String.self, forKey: .machineID)
        sessionID = try c.decode(String.self, forKey: .sessionID)
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        place = try c.decodeIfPresent(String.self, forKey: .place) ?? ""
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
        detail = try c.decodeIfPresent(String.self, forKey: .detail) ?? ""
        muted = try c.decodeIfPresent(Bool.self, forKey: .muted) ?? false
        notify = try c.decodeIfPresent(Bool.self, forKey: .notify) ?? false
        interactionID = try c.decodeIfPresent(String.self, forKey: .interactionID)
        route = try c.decodeIfPresent(String.self, forKey: .route) ?? ""
    }
}

/// Which notifications to post and take back as summaries arrive: one per session (`Item.id`), when a card first
/// `notify`s on a connection, also when it was listed without notifying before (the assistant handed you its question);
/// none for the cards already notifying in a connection's first summary (the web app's baseline rule), nor for a card
/// whose notification is shown; a session that left the summary, or whose notification is about a card it no longer
/// waits on, takes its notification back (a newer card's own notification follows when it goes out).
///
/// A post goes out later (`LinkNotifier` may wait for the permission prompt) or not at all: it counts as shown only once
/// `adopt` takes it in, and `isCurrent` says whether it is still due.
struct AttentionNotifications {
    /// A session of the last summary: the card it waits on ("" without one), whether it notifies now, and whether that
    /// card notified on this connection already (in the first summary too, as a post made for it, or as a notification
    /// shown), so it does not notify twice.
    struct Entry: Equatable {
        var card: String
        var notify: Bool
        var notified: Bool
    }

    /// The last summary's sessions on this connection; nil until its first summary.
    private(set) var known: [String: Entry]?
    /// The last summary was capped (`more` above 0): a session missing from it may still wait beyond the cap.
    private(set) var capped = false
    /// The notifications shown, with the card each is about: taken in by `adopt` (a post that went out, or one found
    /// delivered at launch) and not taken back.
    private(set) var shown: [String: String] = [:]
    /// Counts connections whose summary went away: a post queued on an earlier one is not made.
    private(set) var connection = 0

    struct Changes: Equatable {
        var post: [LinkAttention.Item] = []
        /// Notifications to take back, including the ones a post replaces (same identifier).
        var withdraw: [String] = []
        /// The connection the posts belong to (`isCurrent`).
        var connection = 0

        /// What to take back at once: all but the ones a post replaces, which go only if that post does not go out.
        /// Removing and adding are both asynchronous, so a removal made first could land after the add.
        var withdrawNow: [String] {
            let posting = Set(post.map(\.id))
            return withdraw.filter { !posting.contains($0) }
        }
    }

    /// `nil`: the connection has no summary (it dropped, or the hub sends none): its next summary is a baseline again.
    mutating func apply(_ attention: LinkAttention?) -> Changes {
        guard let attention else {
            if known != nil {
                known = nil
                capped = false
                connection += 1
            }
            return Changes(connection: connection)
        }
        var current: [String: Entry] = [:]
        for item in attention.items where current[item.id] == nil {
            let before = known?[item.id]
            current[item.id] = Entry(card: item.card, notify: item.notify,
                                     notified: item.notify || shown[item.id] == item.card
                                         || before?.card == item.card && before?.notified == true)
        }
        capped = attention.more > 0
        if capped {
            // A session missing from a capped list keeps its card, so its notification stays and its coming back is no
            // new card; it is not known to wait, so a post still waiting for it is not made.
            for (id, entry) in known ?? [:] where current[id] == nil {
                current[id] = Entry(card: entry.card, notify: false, notified: entry.notified)
            }
            for (id, card) in shown where current[id] == nil { current[id] = Entry(card: card, notify: false, notified: true) }
        }
        var changes = Changes(connection: connection)
        if let known {
            changes.post = attention.items.filter { item in
                item.notify && known[item.id].map { $0.card == item.card && $0.notified } != true
            }
        }
        changes.withdraw = shown.filter { id, card in current[id]?.card != card }.keys.sorted()
        for id in changes.withdraw { shown[id] = nil }
        known = current
        return changes
    }

    /// Takes in notifications shown, with the card each is about (nil: it does not say): those an earlier run left, at
    /// launch, and each post once it went out. Returns those to take back now: the ones that do not say and, once a
    /// summary arrived, those whose session waits on another card or is missing from a summary that is not capped.
    mutating func adopt(_ delivered: [(id: String, card: String?)]) -> [String] {
        var gone: [String] = []
        for (id, card) in delivered {
            guard let card, known.map({ $0[id].map { $0.card == card } ?? capped }) ?? true else {
                shown[id] = nil
                gone.append(id)
                continue
            }
            shown[id] = card
            let notify = known?[id]?.notify ?? false
            known?[id] = Entry(card: card, notify: notify, notified: true)
        }
        return gone.sorted()
    }

    /// A post queued on `connection` is still due: same connection, the session still waits on this card and notifies,
    /// and no notification about it is shown yet.
    func isCurrent(_ item: LinkAttention.Item, connection: Int) -> Bool {
        connection == self.connection && known?[item.id].map { $0.card == item.card && $0.notify } == true
            && shown[item.id] != item.card
    }
}

/// Where a row or a notification opens the web app.
enum AttentionURL {
    /// The web app's address: the app's "Web address" setting, else the first of the hub's `web_url`s that is not blank.
    static func base(setting: String?, webURL: String?...) -> String? {
        ([setting] + webURL).lazy.compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }.first { !$0.isEmpty }
    }

    /// `base` + `route`: an http(s) address with or without a sub-path or slashes at either end, and the web app's path.
    /// The route `/` (or empty) is the web app's root, which ends in a slash, as a reverse proxy's sub-path wants.
    /// Anything but an http(s) address with a host gives nil.
    static func web(base: String?, route: String) -> URL? {
        guard let base, var c = URLComponents(string: base), let host = c.host, !host.isEmpty,
              ["https", "http"].contains(c.scheme?.lowercased()) else { return nil }
        let prefix = c.percentEncodedPath.split(separator: "/")
        c.percentEncodedPath = ""
        c.query = nil
        c.fragment = nil
        guard let origin = c.string else { return nil }
        let path = route.split(separator: "/")
        return URL(string: origin + "/" + (prefix + path).joined(separator: "/") + (path.isEmpty && !prefix.isEmpty ? "/" : ""))
    }

    /// The "Web address" setting as typed, made usable: "" (use the hub's), an address with its scheme (https when it
    /// has none), or nil when it is no http(s) address.
    static func setting(_ typed: String) -> String? {
        let text = typed.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty { return "" }
        let full = text.contains("://") ? text : "https://" + text
        return web(base: full, route: "/") == nil ? nil : full
    }
}

/// What a notification says and carries; `LinkNotifier` turns it into a request.
struct AttentionNote: Equatable {
    var id: String
    var title: String
    var subtitle: String
    var body: String
    /// Read back when the notification is clicked (the route, and the hub's web address at the time) and at launch (the
    /// card it is about).
    var userInfo: [String: String]

    static let routeKey = "route"
    static let webURLKey = "web_url"
    static let cardKey = "card"

    init(_ item: LinkAttention.Item, webURL: String?) {
        id = item.id
        title = item.title.isEmpty ? (item.place.isEmpty ? linkText("Needs You") : item.place) : item.title
        subtitle = item.title.isEmpty ? "" : item.place
        body = item.line
        userInfo = [Self.routeKey: item.route, Self.cardKey: item.card]
        if let webURL { userInfo[Self.webURLKey] = webURL }
    }
}

extension LinkAttention.Item {
    /// The card in a few words, its kind first: "Permission: Bash: npm test", "Plan needs approval" (the Live
    /// Activity's wording, apple/Shared/FleetState.swift).
    var line: String {
        switch kind {
        case "permission": detail.isEmpty ? linkText("Needs permission") : linkText("Permission: \(detail)")
        case "plan": detail.isEmpty ? linkText("Plan needs approval") : linkText("Plan: \(detail)")
        case "question": detail.isEmpty ? linkText("Has a question") : linkText("Question: \(detail)")
        case "screen": detail.isEmpty ? linkText("Terminal Needs You") : linkText("Terminal: \(detail)")
        default: detail.isEmpty ? linkText("Needs You") : detail
        }
    }

    var symbol: String {
        switch kind {
        case "permission": "hand.raised"
        case "plan": "list.bullet.clipboard"
        case "question": "questionmark.bubble"
        case "screen": "terminal"
        default: "exclamationmark.bubble"
        }
    }
}

/// Text from this app's String Catalog (Resources/Localizable.xcstrings, en / zh-Hans / ja).
func linkText(_ key: String.LocalizationValue) -> String {
    String(localized: key, bundle: .module)
}
