import Foundation
import Observation

// Settings › Sent as You (#5278, docs/extensions/connectors.md "Sent as the user"): what Persona wrote in the user's name through the
// connectors, newest first, from GET /api/v1/connectors/sent. The words and rules follow the web page (web/src/lib/sent.ts,
// views/settings/SentSettings.tsx): where a send went, how the gate let it through, who asked, paging and refreshing the
// list, the connector chips, and the hub's refusals in plain words. The page is SentView.swift.

// MARK: - The ledger as the hub sends it

/// The connector_gate rule that allowed a send (`let_through.kind` "rule").
nonisolated struct SentRule: Decodable, Hashable, Sendable {
    var connector = ""
    /// The tool's name, or for Lark "<METHOD> <path>"; "*" for any.
    var action = ""
    /// The body fields the rule matched.
    var match: [String: JSONValue] = [:]
    var decision = ""

    init(connector: String = "", action: String = "", match: [String: JSONValue] = [:], decision: String = "") {
        (self.connector, self.action, self.match, self.decision) = (connector, action, match, decision)
    }

    private enum CodingKeys: String, CodingKey { case connector, action, match, decision }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        connector = c.text(.connector)
        action = c.text(.action)
        match = (try? c.decodeIfPresent([String: JSONValue].self, forKey: .match)) ?? [:]
        decision = c.text(.decision)
    }
}

/// How the gate let a send through: a rule, "conversation_always", a card or "auto" (a kind the page does not know is
/// shown as it is).
nonisolated struct SentLetThrough: Decodable, Hashable, Sendable {
    var kind = ""
    var rule: SentRule?
    /// card: the card the user was asked on.
    var interactionID = ""
    /// card: empty when the user answered, "assistant" when the assistant answered it for them, else who did.
    var answeredBy = ""
    /// auto: the extension setting that let it send without asking.
    var setting = ""

    init(kind: String = "", rule: SentRule? = nil, interactionID: String = "", answeredBy: String = "", setting: String = "") {
        (self.kind, self.rule, self.interactionID, self.answeredBy, self.setting) = (kind, rule, interactionID, answeredBy, setting)
    }

    private enum CodingKeys: String, CodingKey {
        case kind, rule, setting
        case interactionID = "interaction_id", answeredBy = "answered_by"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = c.text(.kind)
        rule = try? c.decodeIfPresent(SentRule.self, forKey: .rule)
        interactionID = c.text(.interactionID)
        answeredBy = c.text(.answeredBy)
        setting = c.text(.setting)
    }
}

/// The conversation that asked: both ids when the request named one; `extensionID` for a send an extension recorded.
nonisolated struct SentOrigin: Decodable, Hashable, Sendable {
    var machineID = ""
    var sessionID = ""
    var extensionID = ""

    init(machineID: String = "", sessionID: String = "", extensionID: String = "") {
        (self.machineID, self.sessionID, self.extensionID) = (machineID, sessionID, extensionID)
    }

    private enum CodingKeys: String, CodingKey {
        case machineID = "machine_id", sessionID = "session_id", extensionID = "extension"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        machineID = c.text(.machineID)
        sessionID = c.text(.sessionID)
        extensionID = c.text(.extensionID)
    }
}

/// One row of the ledger (db.SentAsUser). Read as leniently as the web page reads it: a field of the wrong type is
/// its empty value, and a row with no number for an id (which paging and the de-duplication need) is no row.
nonisolated struct SentItem: Decodable, Hashable, Identifiable, Sendable {
    var id: Int
    var at: Date?
    var connector = ""
    var action = ""
    /// Where it went; the fields depend on the connector (docs/extensions/connectors.md "Sent as the user").
    var target: [String: JSONValue] = [:]
    /// What it said, at most `SentText.textMost` characters.
    var text = ""
    /// One line: the card's first line, or the action and where it went.
    var summary = ""
    var origin = SentOrigin()
    var letThrough = SentLetThrough()
    var messageRef = ""
    var recalledAt: Date?

    init(id: Int, at: Date? = nil, connector: String = "", action: String = "", target: [String: JSONValue] = [:], text: String = "",
         summary: String = "", origin: SentOrigin = SentOrigin(), letThrough: SentLetThrough = SentLetThrough(), messageRef: String = "",
         recalledAt: Date? = nil) {
        (self.id, self.at, self.connector, self.action, self.target) = (id, at, connector, action, target)
        (self.text, self.summary, self.origin, self.letThrough, self.messageRef, self.recalledAt) = (text, summary, origin, letThrough, messageRef, recalledAt)
    }

    private enum CodingKeys: String, CodingKey {
        case id, at, connector, action, target, text, summary, origin
        case letThrough = "let_through", messageRef = "message_ref", recalledAt = "recalled_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        if let n = try? c.decode(Int.self, forKey: .id) {
            id = n
        } else if let d = try? c.decode(Double.self, forKey: .id), d == d.rounded(), abs(d) < 1e15 {
            id = Int(d)
        } else {
            throw DecodingError.dataCorruptedError(forKey: .id, in: c, debugDescription: "a row needs a number for an id")
        }
        at = JSON.parseDate(c.text(.at))
        connector = c.text(.connector)
        action = c.text(.action)
        target = (try? c.decodeIfPresent([String: JSONValue].self, forKey: .target)) ?? [:]
        text = c.text(.text)
        summary = c.text(.summary)
        origin = (try? c.decodeIfPresent(SentOrigin.self, forKey: .origin)) ?? SentOrigin()
        letThrough = (try? c.decodeIfPresent(SentLetThrough.self, forKey: .letThrough)) ?? SentLetThrough()
        messageRef = c.text(.messageRef)
        recalledAt = JSON.parseDate(c.text(.recalledAt))
    }
}

/// A page of rows; `next` is the `before` of the page after it, nil on the last.
nonisolated struct SentPage: Decodable, Hashable, Sendable {
    var items: [SentItem] = []
    var next: Int?

    init(items: [SentItem] = [], next: Int? = nil) {
        (self.items, self.next) = (items, next)
    }

    private enum CodingKeys: String, CodingKey { case items, next }

    /// An element that is not a row is skipped, not an error for the whole page.
    private nonisolated struct Lossy: Decodable {
        var item: SentItem?
        init(from decoder: Decoder) throws { item = try? SentItem(from: decoder) }
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        items = ((try? c.decodeIfPresent([Lossy].self, forKey: .items)) ?? []).compactMap(\.item)
        next = try? c.decodeIfPresent(Int.self, forKey: .next)
    }
}

private extension KeyedDecodingContainer {
    /// A string field: empty when it is missing or not a string.
    nonisolated func text(_ key: Key) -> String { (try? decodeIfPresent(String.self, forKey: key)) ?? "" }
}

extension APIClient {
    /// `GET /connectors/sent?before=&limit=&connector=`: `before` is a row id (what `next` gave), `limit` defaults to 50 on the
    /// hub (at most 200), `connector` keeps one connector's rows. Full-access devices only.
    func sent(before: Int? = nil, limit: Int? = nil, connector: String? = nil) async throws -> SentPage {
        var query: [String: String] = [:]
        if let before, before > 0 { query["before"] = String(before) }
        if let limit, limit > 0 { query["limit"] = String(limit) }
        if let connector, !connector.isEmpty { query["connector"] = connector }
        let data = try await request("GET", "/api/v1/connectors/sent", query: query)
        do {
            return try JSON.decoder.decode(SentPage.self, from: data)
        } catch {
            // JSON that is not an object (null, a list) is an empty page, as the web reads it; anything else is an error.
            if (try? JSONSerialization.jsonObject(with: data, options: .fragmentsAllowed)) != nil { return SentPage() }
            throw error
        }
    }
}

// MARK: - The page's words and rules

/// One filter chip: the id the hub calls the connector, and the name it is shown under.
struct SentConnector: Equatable, Identifiable {
    var id: String
    var name: String
}

/// One field of a send's target, for the details.
struct SentField: Equatable, Identifiable {
    var key: String
    var value: String
    var id: String { key }
}

/// How the gate let a send through, for the badge and the details.
struct SentAllowed: Equatable {
    enum Tone: Equatable {
        case neutral
        /// The user did not decide: the assistant (or someone else) answered the card, or the extension sent on its own.
        case orange
    }
    var label: String
    var tone: Tone
    var detail: String
}

/// Why the list cannot be read, in plain words.
struct SentProblem: Equatable {
    enum Kind: Equatable { case noDatabase, forbidden, unsupported, error }
    var kind: Kind
    var text: String
}

/// What reading the first page again leaves (`SentText.refreshed`).
struct SentRefresh: Equatable {
    enum Cursor: Equatable {
        /// Keep the cursor the list has: it is the older end's.
        case keep
        case set(Int?)
    }
    var items: [SentItem]
    var next: Cursor
    var loadedOlder: Bool
}

enum SentText {
    /// The most characters of a send's text the ledger keeps (connector.SentTextMost): a text of exactly this was cut.
    static let textMost = 4000
    /// The id of the connector the node's Lark gate writes under: its sends are in the ledger though no connector is registered for them.
    static let lark = "lark"

    // MARK: Failures

    /// Why the list can't be read. The hub's own answers come before the usual text, which would read a 503 as "can't reach
    /// the hub" (`no_database` is the hub saying it has no ledger) and a 404 as "not found": 403 is a device without full
    /// rights, and a 404 with no code is a hub from before the ledger.
    static func problem(_ error: Error) -> SentProblem {
        if let e = error as? APIError {
            if e.status == 503 && e.code == "no_database" {
                return SentProblem(kind: .noDatabase, text: String(localized: "This hub runs without a database, so what was sent as you isn't recorded."))
            }
            if e.status == 403 {
                return SentProblem(kind: .forbidden, text: String(localized: "Only a device with full access can read this list. Open Persona on one of your own devices that has it."))
            }
            if e.status == 404 && e.code == nil {
                return SentProblem(kind: .unsupported, text: String(localized: "This hub's version doesn't record what was sent as you yet. Update the hub and come back."))
            }
        }
        return SentProblem(kind: .error, text: error.isOffline ? String(localized: "Can't reach the hub") : error.localizedDescription)
    }

    // MARK: The list

    /// `have` and `incoming` together, one row per id (the newer copy wins), newest first.
    static func merge(_ have: [SentItem], _ incoming: [SentItem]) -> [SentItem] {
        var byID: [Int: SentItem] = [:]
        for it in have { byID[it.id] = it }
        for it in incoming { byID[it.id] = it }
        return byID.values.sorted { $0.id > $1.id }
    }

    /// What reading the first page again leaves. Nothing older loaded: the page alone (rows the hub pruned go). Older pages
    /// loaded: the page merged in and the cursor kept (it is the older end's); unless the page does not reach back to the
    /// newest row held (more came in than a page holds, so merging would leave a hole), then the page alone and the older
    /// pages go.
    static func refreshed(have: [SentItem], page: SentPage, loadedOlder: Bool) -> SentRefresh {
        let oldest = page.items.last?.id ?? 0
        let reaches = !page.items.isEmpty && (page.next == nil || oldest <= (have.first?.id ?? 0))
        if !loadedOlder || !reaches { return SentRefresh(items: page.items, next: .set(page.next), loadedOlder: false) }
        return SentRefresh(items: merge(have, page.items), next: .keep, loadedOlder: true)
    }

    // MARK: Connectors (the filter)

    /// A connector's name: mail, GitHub and Lark (its writes through the node's gate are in the ledger though no connector is
    /// registered for them) by their usual words, any other, an extension's, as its id, which is what the ledger calls it.
    static func connectorName(_ id: String) -> String {
        switch id {
        case "mail": String(localized: "Mail [Assistant View 3d708e]")
        case "github": "GitHub"
        case lark: String(localized: "Lark [Inbox Event Views 7e4419]")
        default: id
        }
    }

    /// The filter's chips: Lark, the connectors rows have shown (so one with no record is never offered: Messages only
    /// reads, and the hub's list of connectors can't say which ones write) and the chosen one, so its chip never goes while
    /// it is chosen. No duplicates, in that order. A connector whose rows are all on pages not loaded yet gets its chip when
    /// they are.
    static func connectorChoices(seen: [String], selected: String?) -> [SentConnector] {
        var out: [SentConnector] = []
        for id in [lark] + seen + (selected.map { [$0] } ?? []) where !id.isEmpty && !out.contains(where: { $0.id == id }) {
            out.append(SentConnector(id: id, name: connectorName(id)))
        }
        return out
    }

    /// The ids of `items`' connectors added to `seen`, in the order they first showed.
    static func seenConnectors(_ seen: [String], _ items: [SentItem]) -> [String] {
        var out = seen
        for it in items where !it.connector.isEmpty && !out.contains(it.connector) { out.append(it.connector) }
        return out
    }

    // MARK: A row

    /// A target field's value as text: a list joined, a number or boolean as it is (12, not 12.0), anything nested as JSON.
    static func fieldText(_ v: JSONValue?) -> String {
        switch v {
        case nil, .null?: return ""
        case .string(let s)?: return s.trimmingCharacters(in: .whitespacesAndNewlines)
        case .number(let n)?: return numberText(n)
        case .bool(let b)?: return b ? "true" : "false"
        case .array(let a)?: return a.map { fieldText($0) }.filter { !$0.isEmpty }.joined(separator: ", ")
        case .object?:
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
            return (try? encoder.encode(v)).map { String(decoding: $0, as: UTF8.self) } ?? ""
        }
    }

    /// A whole number without a decimal point in full (as JavaScript prints one below 1e21), any other as Swift writes it.
    static func numberText(_ n: Double) -> String {
        if n == 0 { return "0" }
        if n.isFinite, n == n.rounded(), abs(n) < 1e21 { return String(format: "%.0f", n) }
        return "\(n)"
    }

    /// The arguments connectors usually name their destination by, in the order the hub keeps them (extmgr targetFields).
    private static let common = ["chat", "thread", "channel", "conversation", "recipient", "to", "reply_to", "subject", "repo", "number"]

    /// The order the details list a target's fields in (the hub's JSON objects have none): where it went first, then the rest
    /// of what the connectors above name, then any other field by name.
    private static let detailOrder = common + ["ref", "cc", "bcc", "event", "method", "path", "command", "account_id", "arg_keys"]

    /// Where a send went, in a few words: mail the recipients and subject, GitHub the repo#number, Lark the conversation (else
    /// the method and path the node's gate saw); any other connector's the common fields it named, up to three. "—" when
    /// there is nothing to say (a connector that gave only the names of its arguments).
    static func targetText(_ item: SentItem) -> String {
        func f(_ key: String) -> String { fieldText(item.target[key]) }
        switch item.connector {
        case "mail":
            if !f("to").isEmpty { return f("subject").isEmpty ? f("to") : "\(f("to")) · \(f("subject"))" }
        case "github":
            if !f("ref").isEmpty { return f("ref") }
        case lark:
            if !f("conversation").isEmpty { return f("conversation") }
            let call = [f("method"), f("path")].filter { !$0.isEmpty }.joined(separator: " ")
            if !call.isEmpty { return call }
        default: break
        }
        let shown = common.map(f).filter { !$0.isEmpty }.prefix(3)
        return shown.isEmpty ? "—" : shown.joined(separator: " · ")
    }

    /// The target's fields for the details in a fixed order (`detailOrder`, then the rest by name), empty values left out.
    static func targetFields(_ target: [String: JSONValue]) -> [SentField] {
        let rank = { (key: String) in detailOrder.firstIndex(of: key) ?? detailOrder.count }
        return target.keys
            .sorted { (rank($0), $0) < (rank($1), $1) }
            .map { SentField(key: $0, value: fieldText(target[$0])) }
            .filter { !$0.value.isEmpty }
    }

    /// The first `most` lines of the text (blank ones skipped) for the row; with no text, the one-line summary. Empty:
    /// nothing was recorded.
    static func previewLines(_ item: SentItem, most: Int = 2) -> [String] {
        let lines = item.text.split(whereSeparator: { $0 == "\n" || $0 == "\r\n" })
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
        if !lines.isEmpty { return Array(lines.prefix(most)) }
        let summary = item.summary.trimmingCharacters(in: .whitespacesAndNewlines)
        return summary.isEmpty ? [] : [summary]
    }

    /// Whether the ledger cut the text: it is exactly as long as the ledger keeps (counted in characters of the text, as the
    /// hub counts them, not in UTF-16 units or grapheme clusters).
    static func textCut(_ text: String) -> Bool { text.unicodeScalars.count >= textMost }

    /// A rule in a line, the way connector_gate writes it: `lark · POST /im/v1/messages (chat_id=…) → allow`.
    static func ruleText(_ rule: SentRule?) -> String {
        guard let rule else { return "" }
        let head = [rule.connector, rule.action].filter { !$0.isEmpty }.joined(separator: " · ")
        let match = rule.match.keys.sorted().map { "\($0)=\(fieldText(rule.match[$0]))" }.joined(separator: ", ")
        return [head, match.isEmpty ? "" : "(\(match))", rule.decision.isEmpty ? "" : "→ \(rule.decision)"]
            .filter { !$0.isEmpty }.joined(separator: " ")
    }

    /// How the gate let it through: the badge's words and tone (orange where the user did not decide: the assistant or
    /// someone else answered the card, or the extension sent on its own) and a sentence for the details. A kind this page
    /// does not know is shown as it is.
    static func allowedBy(_ lt: SentLetThrough) -> SentAllowed {
        switch lt.kind {
        case "rule":
            return SentAllowed(label: String(localized: "By a rule"), tone: .neutral,
                               detail: ruleText(lt.rule).isEmpty ? String(localized: "A gate rule allowed it.") : ruleText(lt.rule))
        case "conversation_always":
            return SentAllowed(label: String(localized: "Always allowed in this chat"), tone: .neutral,
                               detail: String(localized: "You chose “Always allow” for this in the chat, so no card was shown."))
        case "card":
            // `answeredBy` is empty when the user answered (in the app, on the phone, the Mac, the web or a chat channel, which
            // the ledger does not say), "assistant" when the assistant did, and anyone else is named: not the user's own word.
            let by = lt.answeredBy
            if by == "assistant" {
                return SentAllowed(label: String(localized: "Answered by the assistant"), tone: .orange,
                                   detail: String(localized: "The assistant answered the card on your behalf."))
            }
            if !by.isEmpty {
                return SentAllowed(label: String(localized: "Answered by \(by)"), tone: .orange, detail: String(localized: "The card was answered by \(by)."))
            }
            return SentAllowed(label: String(localized: "Confirmed by you on a card"), tone: .neutral,
                               detail: String(localized: "You were asked on a confirmation card and allowed it."))
        case "auto":
            let setting = lt.setting
            if setting.isEmpty {
                return SentAllowed(label: String(localized: "Sent by the extension itself"), tone: .orange,
                                   detail: String(localized: "The extension sent it on its own: no rule and no card."))
            }
            return SentAllowed(label: String(localized: "Sent by the extension itself · \(setting)"), tone: .orange,
                               detail: String(localized: "The extension sent it on its own, allowed by its setting \(setting): no rule and no card."))
        default:
            return SentAllowed(label: lt.kind.isEmpty ? "—" : lt.kind, tone: .neutral, detail: "")
        }
    }

    /// Who asked: the conversation to open (both ids named) and the extension when the row is one an extension recorded (what
    /// it names of the conversation is its own word). Neither: no conversation.
    static func originInfo(_ o: SentOrigin) -> (conversation: (machine: String, session: String)?, extensionID: String) {
        let mid = o.machineID.trimmingCharacters(in: .whitespacesAndNewlines)
        let sid = o.sessionID.trimmingCharacters(in: .whitespacesAndNewlines)
        return (!mid.isEmpty && !sid.isEmpty ? (mid, sid) : nil, o.extensionID.trimmingCharacters(in: .whitespacesAndNewlines))
    }
}

// MARK: - The page's state

/// Settings › Sent as You: the first page, older pages on request, the connector filter, and what a read that comes back
/// late is worth. The list starts over when the filter changes (what comes in for the old one is dropped); a read of the
/// first page that was superseded by a newer one, and a read of older rows for a cursor the list has moved on from, are
/// dropped too.
@Observable final class SentModel {
    struct Deps {
        var api: () -> APIClient?
        static var live: Deps { .init(api: { AppModel.shared.api }) }
    }

    /// Nil until the first page came in.
    private(set) var items: [SentItem]?
    /// The `before` of the page after the list, nil when it holds the oldest row.
    private(set) var next: Int?
    /// Why the first page could not be read (a list that is there stays when a later read fails).
    private(set) var problem: SentProblem?
    /// The connector the list is of, nil for all.
    private(set) var filter: String?
    private(set) var loadingMore = false
    /// Why the last Load Earlier failed.
    private(set) var moreError: String?
    /// The connectors the loaded rows have shown, in the order they first showed (the chips).
    private(set) var seen: [String] = []

    /// Moves when the filter changes: a read that started under an earlier one is dropped.
    @ObservationIgnored private var generation = 0
    /// Reads of the first page started under this filter, and the newest of them whose answer was taken: an answer is taken
    /// when it is newer than that (an older one that comes back after a newer one is dropped, and a newer one that was
    /// cancelled or failed does not strand an older one that is good).
    @ObservationIgnored private var reads = 0
    @ObservationIgnored private var applied = 0
    /// Older pages were loaded: reading the first page again merges instead of replacing.
    @ObservationIgnored private var loadedOlder = false
    /// The first page has come in once under this filter.
    @ObservationIgnored private var hasFirst = false
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    var choices: [SentConnector] { SentText.connectorChoices(seen: seen, selected: filter) }

    /// The hub group of the client the ledger is read through: what the chats its rows name belong to.
    var group: String {
        guard let api = deps.api() else { return "" }
        return api.group
    }

    /// Nothing to filter by while the ledger is empty and every connector is shown, or when the unfiltered list can't be read;
    /// under a filter the chips stay, so a failed read can be left by choosing another one or All.
    var showsChips: Bool { filter != nil || (problem == nil && !seen.isEmpty) }

    /// Reads the first page: on opening, on pull to refresh, on Retry.
    func load() async {
        guard let api = deps.api() else { return }
        reads += 1
        let (mine, gen, connector) = (reads, generation, filter)
        do {
            let page = try await api.sent(connector: connector)
            guard gen == generation, mine > applied else { return }
            applied = mine
            hasFirst = true
            problem = nil
            seen = SentText.seenConnectors(seen, page.items)
            let out = SentText.refreshed(have: items ?? [], page: page, loadedOlder: loadedOlder)
            loadedOlder = out.loadedOlder
            items = out.items
            if case .set(let cursor) = out.next { next = cursor }
        } catch {
            // A cancelled read says nothing; a list that is there stays when a later read fails: only the first read's failure is shown.
            guard !error.isCancellation, !Task.isCancelled, gen == generation, mine > applied, !hasFirst else { return }
            problem = SentText.problem(error)
        }
    }

    /// Retry: the first page again, with the spinner where the failure was.
    func retry() async {
        problem = nil
        await load()
    }

    /// Chooses a connector's chip (nil: All; the chosen one again: All) and reads its first page.
    func choose(_ connector: String?) async {
        let connector = connector == filter ? nil : connector
        guard connector != filter else { return }
        generation += 1
        reads = 0
        applied = 0
        filter = connector
        items = nil
        next = nil
        problem = nil
        moreError = nil
        loadingMore = false
        loadedOlder = false
        hasFirst = false
        await load()
    }

    /// Load Earlier: the page after the list, added by id.
    func loadMore() async {
        guard let api = deps.api(), let asked = next, !loadingMore else { return }
        let (gen, connector) = (generation, filter)
        loadingMore = true
        moreError = nil
        defer { if gen == generation { loadingMore = false } }
        do {
            let page = try await api.sent(before: asked, connector: connector)
            guard gen == generation, next == asked else { return }
            loadedOlder = true
            seen = SentText.seenConnectors(seen, page.items)
            items = SentText.merge(items ?? [], page.items)
            next = page.next
        } catch {
            guard !error.isCancellation, gen == generation else { return }
            moreError = SentText.problem(error).text
        }
    }
}
