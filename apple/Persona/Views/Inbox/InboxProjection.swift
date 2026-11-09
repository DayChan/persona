import Foundation

/// One filtered inbox snapshot. Its rows retain the hub's order; only Done has a separate display order.
@MainActor
final class InboxProjection {
    nonisolated struct Filter: Equatable {
        var source = ""
        var kind = ""
        var query = ""
        // Searching and the Moments @Me alias use the current localization, not a cached language's result.
        var localeIdentifier = Locale.current.identifier
        var mentionLabel = String(localized: "@Me")

        func matches(_ item: InboxItem) -> Bool {
            if !source.isEmpty && item.account != source { return false }
            if !kind.isEmpty && !(item.kind == kind || kind == "group" && item.kind == "bot" || kind == "mention" && (item.kind == "doc" || item.kind == "moment" && item.kindLabel == mentionLabel)) { return false }
            if query.isEmpty { return true }
            return item.title.localizedCaseInsensitiveContains(query)
                || (item.sender ?? "").localizedCaseInsensitiveContains(query)
                || item.preview.localizedCaseInsensitiveContains(query)
                || (item.summary ?? "").localizedCaseInsensitiveContains(query)
        }
    }

    nonisolated struct Source: Identifiable, Equatable, Sendable {
        let id: String
        let name: String
    }

    let openItems: [InboxItem]
    let byTier: [InboxItem.Tier: [InboxItem]]
    let doneItems: [InboxItem]
    let connectorSources: [Source]
    // Closed Done sections never sort. Expanding, closing and expanding again reuse this snapshot's order.
    private(set) lazy var sortedDoneItems = doneItems.sorted {
        ($0.doneAt ?? .distantPast) > ($1.doneAt ?? .distantPast)
    }

    init(items: [InboxItem], filter: Filter) {
        var open: [InboxItem] = []
        var tiers: [InboxItem.Tier: [InboxItem]] = [:]
        var done: [InboxItem] = []
        var sources: [Source] = []
        var seen = Set<String>()
        for item in items {
            // Source chips include archived and currently filtered-out items, first occurrence wins.
            if item.connector != nil && seen.insert(item.account).inserted {
                sources.append(Source(id: item.account, name: item.accountName))
            }
            guard filter.matches(item) else { continue }
            if item.isDone {
                done.append(item)
            } else {
                open.append(item)
                tiers[item.triage, default: []].append(item)
            }
        }
        openItems = open
        byTier = tiers
        doneItems = done
        connectorSources = sources
    }
}

/// Page-local, unobserved derived data: lookup is synchronous so edits, optimistic filing and rollback paint immediately.
@MainActor
final class InboxProjectionMemo {
    private var items: [InboxItem] = []
    private var filter: InboxProjection.Filter?
    private var projection: InboxProjection?
    private var revision: UInt64?

    func callAsFunction(_ items: [InboxItem], filter: InboxProjection.Filter, revision: UInt64? = nil) -> InboxProjection {
        if let projection, self.filter == filter {
            if let revision, self.revision == revision { return projection }
            if revision == nil, self.revision == nil, self.items == items {
                self.items = items
                return projection
            }
        }
        let fresh = InboxProjection(items: items, filter: filter)
        // The store's revision avoids retaining its COW storage or scanning unchanged payloads on redraw.
        // Standalone callers without a complete mutation stamp retain the full-value comparison fallback.
        self.items = revision == nil ? items : []
        self.revision = revision
        self.filter = filter
        projection = fresh
        return fresh
    }
}

/// One manager's part of the inbox on a hub with several (#1850; the web's inbox groups, docs/assistant/managers.md
/// "Grouping"): under its name its own sections — Needs You with its waiting agents first, In Progress, FYI, Collapsed
/// — and its own Done. With one manager, or a hub before managers, there is one share with no manager, and the page is
/// as it was.
nonisolated struct InboxShare: Identifiable {
    var manager: HubManager?
    var byTier: [InboxItem.Tier: [InboxItem]]
    var agents: [Interaction]
    /// Its done items in the list's order (a page sorts them only while the section is open, `mine`).
    var done: [InboxItem]
    var id: String { manager?.id ?? "" }
    /// What waits in it: open items and waiting agents.
    var openCount: Int { byTier.values.reduce(agents.count) { $0 + $1.count } }

    /// The shares in the roster's order (the default first), empty ones left out.
    static func make(byTier: [InboxItem.Tier: [InboxItem]], done: [InboxItem], agents: [Interaction], roster: ManagerRoster?) -> [InboxShare] {
        guard let roster, roster.isMulti else { return [InboxShare(manager: nil, byTier: byTier, agents: agents, done: done)] }
        var tiers: [String: [InboxItem.Tier: [InboxItem]]] = [:]
        for tier in InboxItem.Tier.allCases {
            for it in byTier[tier] ?? [] { tiers[roster.owner(it.manager), default: [:]][tier, default: []].append(it) }
        }
        let doneBy = Dictionary(grouping: done) { roster.owner($0.manager) }
        let agentsBy = Dictionary(grouping: agents) { roster.owner($0.manager) }
        return roster.installed.compactMap { m in
            let share = InboxShare(manager: m, byTier: tiers[m.id] ?? [:], agents: agentsBy[m.id] ?? [], done: doneBy[m.id] ?? [])
            return share.openCount == 0 && share.done.isEmpty ? nil : share
        }
    }

    /// Those of `items` that are this share's.
    func mine(_ items: [InboxItem], _ roster: ManagerRoster?) -> [InboxItem] {
        guard let manager, let roster else { return items }
        return items.filter { roster.owner($0.manager) == manager.id }
    }
}

/// Which inbox sections are open: Needs You, In Progress and FYI start open, Collapsed and Done closed, in every
/// manager's group alike (#1850). Only what the user flipped is kept, so a group that shows up later starts the same way.
nonisolated struct InboxFolds: Equatable {
    private static let startOpen: Set<String> = ["action", "working", "fyi"]
    private var flipped: Set<String> = []

    /// `section`: a tier or "done"; `manager`: its group's, nil with one group.
    func isOpen(_ section: String, manager: String? = nil) -> Bool {
        guard InboxItem.Tier(rawValue: section)?.folds != false else { return true } // In Progress stays open
        return Self.startOpen.contains(section) != flipped.contains(Self.key(section, manager))
    }

    mutating func toggle(_ section: String, manager: String? = nil) {
        guard InboxItem.Tier(rawValue: section)?.folds != false else { return }
        let key = Self.key(section, manager)
        if flipped.contains(key) { flipped.remove(key) } else { flipped.insert(key) }
    }

    private static func key(_ section: String, _ manager: String?) -> String { manager.map { "\($0)\u{0}\(section)" } ?? section }
}
