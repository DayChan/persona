import SwiftUI

// The Mac inbox's state (docs/assistant/inbox.md, docs/apple/mac.md「收件箱」): one per app, shared by the sidebar's filters, the list
// column and the detail column, which the window's three columns hold separately (`MacInboxFilters`, `MacInboxList`,
// `MacInboxDetail`). The filters are the iPhone's (same `inbox.source` / `inbox.kind` defaults), picked from the
// sidebar instead of a row of chips.

/// A row in the list: an inbox item, or an agent waiting on the user (heads Needs You, as on the iPhone).
enum MacInboxSelection: Hashable {
    case item(String)
    case agent(String)
}

/// What a multi-row selection can be filed as. Agent requests are answered in their conversations, not items the
/// inbox can mark Done: a selection made only of them has nothing to file, so the detail must not offer it and E
/// must not be swallowed (#3479).
nonisolated enum MacInboxMultiSelection: Equatable, Sendable {
    /// Only agent requests: nothing can be filed; the user opens their conversations.
    case agentsOnly(agentCount: Int)
    /// Items and agent requests together: only the item rows are filed, the requests stay for their conversations.
    case mixed(itemCount: Int, agentCount: Int)
    /// Only items: every selected row can be filed.
    case itemsOnly(itemCount: Int)

    /// How many of the selected rows can be marked Done.
    var itemCount: Int {
        switch self {
        case .agentsOnly: 0
        case .mixed(let count, _), .itemsOnly(let count): count
        }
    }

    /// Whether E / the detail's button files anything in the selection.
    var canMarkDone: Bool { itemCount > 0 }

    /// Classify a selection by its item rows and its agent request rows.
    static func decide(itemCount: Int, agentCount: Int) -> MacInboxMultiSelection {
        if itemCount > 0 {
            return agentCount > 0 ? .mixed(itemCount: itemCount, agentCount: agentCount) : .itemsOnly(itemCount: itemCount)
        }
        return .agentsOnly(agentCount: agentCount)
    }
}

/// One of the sidebar's inbox entries: All, Lark, each mail account, each connector with items (sources), then @Me / DMs /
/// Group chat / Mail / Agent (kinds).
enum MacInboxFilter: Hashable, Identifiable {
    case all
    case feishu
    case account(MailAccount)
    /// A connector with items in the list (GitHub…), as the iPhone's and the web's source chips (#2611).
    case connector(id: String, name: String)
    case kind(String)

    var id: String {
        switch self {
        case .all: "all"
        case .feishu: "feishu"
        case .account(let a): "account." + a.id
        case .connector(let id, _): "connector." + id
        case .kind(let k): "kind." + k
        }
    }

    /// The kinds, in the iPhone's chip order.
    static var kinds: [(key: String, title: String)] {
        [("mention", String(localized: "@Me")), ("dm", String(localized: "DMs")), ("group", String(localized: "Group chat")),
         ("mail", String(localized: "Mail [Assistant View 3d708e]")), ("agent", "Agent")]
    }

    var title: String {
        switch self {
        case .all: String(localized: "All")
        case .feishu: String(localized: "Lark [Inbox Event Views 7e4419]")
        case .account(let a): InboxView.short(a.email)
        case .connector(_, let name): name
        case .kind(let k): Self.kinds.first { $0.key == k }?.title ?? k
        }
    }

    var icon: String {
        switch self {
        case .all: Theme.Icon.inbox
        case .feishu: Theme.Icon.feishuChats
        case .account: Theme.Icon.mail
        case .connector: Theme.Icon.connectors
        case .kind(let k):
            switch k {
            case "mention": Theme.Icon.mention
            case "dm": Theme.Icon.person
            case "group": Theme.Icon.groupChat
            case "mail": Theme.Icon.mailFilled
            default: Theme.Icon.agents
            }
        }
    }

    /// The `inbox.source` / `inbox.kind` pair this entry stands for.
    var source: String {
        switch self {
        case .feishu: "feishu"
        case .account(let a): a.id
        case .connector(let id, _): id
        default: ""
        }
    }

    var kind: String {
        if case .kind(let k) = self { return k }
        return ""
    }
}

@MainActor @Observable
final class MacInboxState {
    static let shared = MacInboxState()

    /// A document is open beside the selected item (the link panel): the page gives it the filters' and the actions'
    /// columns (#2113, `MacInboxPage`, `macInboxActions`).
    var reading = false {
        didSet { if !reading { listWhileReading = false } }
    }
    /// The list brought back beside a document being read (its button in the item's actions row); folded again when
    /// the document closes.
    var listWhileReading = false

    /// "" all, "feishu", a mail account id or a connector's id (the iPhone's `inbox.source`).
    var source: String = UserDefaults.standard.string(forKey: "inbox.source") ?? "" {
        didSet { UserDefaults.standard.set(source, forKey: "inbox.source") }
    }
    /// "" all, agent, dm, mention, group, mail (the iPhone's `inbox.kind`).
    var kind: String = UserDefaults.standard.string(forKey: "inbox.kind") ?? "" {
        didSet { UserDefaults.standard.set(kind, forKey: "inbox.kind") }
    }
    var query = ""
    var selection: Set<MacInboxSelection> = []
    /// Sections shown open: tiers (action, working, fyi, noise) and "done". Collapsed and Done start closed, as on the iPhone.
    var expanded: Set<String> = ["action", "working", "fyi"]
    var accounts: [MailAccount] = StateCache.load("mail-accounts", as: [MailAccount].self) ?? []
    /// The sheets the list opens: New Email, Manage Accounts, Edit on the AI Twin's draft, Hand to AI.
    var composing: ComposeDraft?
    var showAccounts = false
    var editingDraft: InboxItem?
    var drafting: InboxItem?
    var handingOff: InboxItem?
    /// Rules, and an item's Rule for This Chat / Sender / Repository…
    var showRules = false
    var ruling: InboxPolicyTarget?
    /// Clear All over many items, waiting for the user to say yes.
    var confirming = false

    private var store: InboxStore { .shared }

    // MARK: Filters

    var filter: MacInboxFilter {
        get {
            if !kind.isEmpty { return .kind(kind) }
            if source == "feishu" { return .feishu }
            if let a = accounts.first(where: { $0.id == source }) { return .account(a) }
            if let c = connectorSources.first(where: { $0.source == source }) { return c }
            return .all
        }
        set {
            source = newValue.source
            kind = newValue.kind
        }
    }

    /// The sidebar's entries: sources, then kinds.
    var sources: [MacInboxFilter] { [.all, .feishu] + accounts.map { .account($0) } + connectorSources }

    /// The connectors with items in the list, one entry each (the iPhone's `connectorSources`); none, no entry.
    var connectorSources: [MacInboxFilter] {
        var seen = Set<String>()
        return store.items.compactMap { it in
            guard it.connector != nil, seen.insert(it.account).inserted else { return nil }
            return .connector(id: it.account, name: it.accountName)
        }
    }
    var kinds: [MacInboxFilter] { MacInboxFilter.kinds.map { .kind($0.key) } }

    func matches(_ it: InboxItem, source: String? = nil, kind: String? = nil, query: String? = nil) -> Bool {
        let source = source ?? self.source, kind = kind ?? self.kind, query = query ?? self.query
        if !source.isEmpty && it.account != source { return false }
        if !kind.isEmpty && !(it.kind == kind || kind == "group" && it.kind == "bot" || kind == "mention" && (it.kind == "doc" || it.kind == "moment" && it.kindLabel == String(localized: "@Me"))) { return false }
        if query.isEmpty { return true }
        return [it.title, it.sender ?? "", it.preview, it.summary ?? ""].contains { $0.localizedCaseInsensitiveContains(query) }
    }

    /// Agents (and the assistant) waiting for an answer, an approval or a plan review.
    func waitingAgents(_ model: AppModel, source: String? = nil, kind: String? = nil) -> [Interaction] {
        let source = source ?? self.source, kind = kind ?? self.kind
        guard source.isEmpty, kind.isEmpty || kind == "agent" else { return [] }
        return model.pendingInteractions.filter { ix in
            !model.isMuted(ix.sessionKey) && (query.isEmpty || [ix.sessionTitle ?? "", ix.summary ?? "", ix.title].contains { $0.localizedCaseInsensitiveContains(query) })
        }
    }

    /// What still waits in an entry (the sidebar's count): open items Needs You, plus the waiting agents.
    func count(_ f: MacInboxFilter, model: AppModel) -> Int {
        let items = store.items.filter { !$0.isDone && $0.triage == .action && matches($0, source: f.source, kind: f.kind, query: "") }
        let agents = f.source.isEmpty && (f.kind.isEmpty || f.kind == "agent")
            ? model.pendingInteractions.filter { !model.isMuted($0.sessionKey) }.count : 0
        return (f.kind == "agent" ? 0 : items.count) + agents
    }

    // MARK: Sections

    struct Sections {
        var tiers: [(tier: InboxItem.Tier, items: [InboxItem], agents: [Interaction])]
        var done: [InboxItem]
        var isEmpty: Bool { tiers.isEmpty && done.isEmpty }
    }

    func sections(_ model: AppModel) -> Sections {
        let partition = Dictionary(grouping: store.items.filter { matches($0) }, by: \.isDone)
        let byTier = Dictionary(grouping: partition[false] ?? [], by: \.triage)
        let agents = waitingAgents(model)
        let tiers = InboxItem.Tier.allCases.compactMap { tier -> (InboxItem.Tier, [InboxItem], [Interaction])? in
            let items = kind == "agent" ? [] : byTier[tier] ?? []
            let a = tier == .action ? agents : []
            return items.isEmpty && a.isEmpty ? nil : (tier, items, a)
        }
        let done = (partition[true] ?? []).sorted { ($0.doneAt ?? .distantPast) > ($1.doneAt ?? .distantPast) }
        return Sections(tiers: tiers, done: done)
    }

    /// Clear All: the open items the list shows (its filter and search); the agents waiting are not items.
    var openInView: [InboxItem] { kind == "agent" ? [] : store.items.filter { !$0.isDone && matches($0) } }

    /// What Clear All covers and how many, for its help tag.
    var cleanUpScope: String {
        InboxBulk.scopeLine(filters: scopeFilters, query: query, count: openInView.count)
    }

    var scopeFilters: [String] { filter == .all ? [] : [filter.title] }

    /// Clear All (toolbar, Inbox menu): the open items in view filed as dismissed, in one request with Undo after;
    /// from `InboxBulk.confirmFrom` items on, the list asks first (`confirming`).
    func clean(confirmed: Bool = false) {
        let scope = openInView
        guard !scope.isEmpty else { return }
        if !confirmed && InboxBulk.needsConfirm(scope.count) {
            confirming = true
            return
        }
        withAnimation(Theme.Motion.standard) { store.clean(scope) }
    }

    /// The rows in the order the list shows them (closed sections left out): for moving the selection on.
    func visibleOrder(_ model: AppModel) -> [MacInboxSelection] {
        let s = sections(model)
        var out: [MacInboxSelection] = []
        for t in s.tiers where expanded.contains(t.tier.rawValue) || !t.tier.folds {
            out += t.agents.map { .agent($0.id) } + t.items.map { .item($0.id) }
        }
        if expanded.contains("done") { out += s.done.map { .item($0.id) } }
        return out
    }

    func toggle(_ section: String) {
        guard InboxItem.Tier(rawValue: section)?.folds != false else { return } // In Progress stays open
        if expanded.contains(section) { expanded.remove(section) } else { expanded.insert(section) }
    }

    // MARK: Actions

    func item(_ id: String) -> InboxItem? { store.items.first { $0.id == id } }

    /// The selected rows still present in the list: an `.item` the store no longer returns (it expired or was handled
    /// elsewhere) or an `.agent` interaction that vanished is dropped, so the detail's Selected count and the multi-select
    /// action cover what still exists (#3795).
    func liveSelection(model: AppModel) -> Set<MacInboxSelection> {
        liveSelection(items: store.items, interactions: model.pendingInteractions)
    }

    /// Same check against an explicit list (a store refresh pruning the selection, and tests).
    func liveSelection(items: [InboxItem], interactions: [Interaction]) -> Set<MacInboxSelection> {
        let itemIDs = Set(items.map(\.id))
        let agentIDs = Set(interactions.map(\.id))
        return selection.filter {
            switch $0 {
            case .item(let id): itemIDs.contains(id)
            case .agent(let id): agentIDs.contains(id)
            }
        }
    }

    /// Drop selected rows that expired out of the list (the store refresh / interaction update saw them gone).
    func pruneStale(items: [InboxItem], interactions: [Interaction]) {
        let live = liveSelection(items: items, interactions: interactions)
        if live != selection { selection = live }
    }

    /// The selected items (agent rows are not items).
    var selectedItems: [InboxItem] {
        selection.compactMap { if case .item(let id) = $0 { return item(id) } else { return nil } }
    }

    /// The selected agent requests: they are answered in their conversations, not filed as Done.
    var selectedAgentCount: Int {
        selection.reduce(0) { count, row in
            guard case .agent = row else { return count }
            return count + 1
        }
    }

    /// What the multi-row selection can do, shared by the detail column and the list's E key.
    var multiSelection: MacInboxMultiSelection {
        .decide(itemCount: selectedItems.count, agentCount: selectedAgentCount)
    }

    /// Done (or Move Back when all are done already) on these rows, then the selection moves to the row after them, as
    /// Mail does when a message is deleted.
    func toggleDone(_ ids: Set<MacInboxSelection>, model: AppModel) {
        let items = ids.compactMap { s -> InboxItem? in if case .item(let id) = s { return item(id) } else { return nil } }
        guard !items.isEmpty else { return }
        let done = !items.allSatisfy(\.isDone)
        let order = visibleOrder(model)
        let after = order.lastIndex { ids.contains($0) }.map { order.index(after: $0) } ?? order.endIndex
        let next = order[after...].first { !ids.contains($0) } ?? order[..<(order.lastIndex { ids.contains($0) } ?? 0)].last { !ids.contains($0) }
        withAnimation(Theme.Motion.standard) { store.setDone(items, done) }
        if done, ids == selection { selection = next.map { [$0] } ?? [] }
    }

    /// The agent's chat (the assistant's in Persona), where the question / plan / approval waits.
    func open(_ ix: Interaction, model: AppModel) {
        if model.session(ix.sessionKey)?.assistant == true {
            model.selectHub(ix.group)
            model.assistantCurrent = ix.sessionKey
            model.selectedTab = .assistant
        } else {
            model.openChat = ix.sessionKey
            model.selectedTab = .chats
        }
    }

    func loadAccounts(_ model: AppModel) async {
        guard let api = model.api, let a = try? await api.mailAccounts() else { return }
        accounts = a
        StateCache.save("mail-accounts", a)
        // A connector's id is not a mail account: it stays while the hub's list has not come yet (the cached one may
        // be from before), and while its items are there.
        if !source.isEmpty, source != "feishu", !a.contains(where: { $0.id == source }), store.listed,
           !store.items.contains(where: { $0.connector != nil && $0.account == source }) { source = "" }
    }
}
