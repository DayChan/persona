import Foundation
import Observation

/// The unified inbox as the app knows it (docs/assistant/inbox.md): the hub's items, kept on disk so the list paints at once
/// (offline too), refreshed when the hub says `inbox` and on (re)connect. Marking done applies at once and is sent
/// in the background; a failure puts the item back.
@MainActor @Observable
final class InboxStore {
    static let shared = InboxStore()

    private(set) var items: [InboxItem] {
        didSet { itemsRevision &+= 1 }
    }
    /// A synchronous cache stamp for every assignment and in-place optimistic item mutation.
    @ObservationIgnored private(set) var itemsRevision: UInt64 = 0
    /// How triage stands on the hub (new items held back until the model sorts them), with every list; nil from an
    /// older hub, and until the first list of this run (it is not kept on disk: an old count must not show offline).
    private(set) var triage: InboxTriageStatus?
    private(set) var loading = false
    /// A listing came from the hub since launch: what is cached on disk may be from before a source was added.
    private(set) var listed = false
    var error: String?
    @ObservationIgnored private var reloading: Task<Void, Never>?
    @ObservationIgnored private let saver = Debouncer(.seconds(1))
    private static let stateName = "inbox-items"

    private init() {
        items = StateCache.load(Self.stateName, as: [InboxItem].self) ?? []
        teachLarkSync()
    }

    var open: [InboxItem] { items.filter { !$0.isDone } }

    /// Entering or leaving Demo Mode: the other side's items must not show for a moment.
    func forget() {
        items = []
        triage = nil
        error = nil
        saver.run { StateCache.save(Self.stateName, [InboxItem]()) }
    }

    /// What the tab's badge counts: open items that need the user.
    var actionCount: Int { items.filter { !$0.isDone && $0.triage == .action }.count }

    /// Pulls the list (bursts of `inbox` events collapse into one request).
    func reload() {
        guard reloading == nil else { return }
        reloading = Task {
            defer { reloading = nil }
            await fetch { try await $0.inboxListing(connectors: Self.drawsConnectors) }
        }
    }

    /// Pull to refresh: the hub collects Feishu and mail now.
    func refresh() async {
        await fetch { try await $0.refreshInbox(connectors: Self.drawsConnectors) }
    }

    /// The connectors' items (GitHub…) are drawn on iOS and the Mac (#2611, #2736); the watch fetches its own (WatchModel, #2727).
    #if os(iOS) || os(visionOS) || os(macOS)
    private static let drawsConnectors = true
    #else
    private static let drawsConnectors = false
    #endif

    private func fetch(_ load: (APIClient) async throws -> InboxListing) async {
        guard let api = AppModel.shared.api else { return }
        loading = true
        defer { loading = false }
        do {
            let listing = try await load(api)
            apply(listing.items)
            listed = true
            if listing.triage != triage { triage = listing.triage }
            error = nil
        } catch {
            if let shown = error.shown { self.error = shown }
        }
    }

    private func apply(_ list: [InboxItem]) {
        guard list != items else { return }
        items = list
        teachLarkSync()
        let snapshot = list
        saver.run { StateCache.save(Self.stateName, snapshot) }
        WatchSync.shared.push(summary: AppModel.shared.watchSummary())
    }

    /// Mark All Seen / Clear All (a section), Mac's multi-select, and every Undo: one request for them all on the Done request
    /// (docs/assistant/inbox.md「全部清理」). Shown at once; the hub files each as the list showed it, so one that took new
    /// messages since stays open (it goes back here too), and a failure puts them all back. Filing ends with a toast and
    /// its Undo, which brings back what was filed.
    func setDone(_ list: [InboxItem], _ done: Bool, undoable: Bool = true) {
        guard let api = AppModel.shared.api else { return }
        run(list, done: done, action: .done) { targets in
            let r = try await self.fileDone(targets, done, api: api)
            return (r, undoable && !r.changed.isEmpty ? .legacy(r.changed) : nil)
        }
    }

    /// Clear All (the view's open items, its filters and search): filed as dismissed in the hub's bulk request
    /// (`POST /api/v1/inbox/bulk`, docs/assistant/inbox.md「全部清理」); on a hub without it the Done request instead (recorded as
    /// handled there). Same as above: shown at once, put back on a failure, a toast whose Undo sends the hub's `undo`.
    func clean(_ list: [InboxItem]) {
        guard let api = AppModel.shared.api else { return }
        let action = InboxBulk.Action.dismiss
        run(list, done: true, action: action) { targets in
            try await InboxBulk.perform(action, targets,
                                        bulk: { try await api.bulkInbox($0.rawValue, $1) },
                                        legacy: { try await self.fileDone($0, true, api: api) })
        }
    }

    private func run(_ list: [InboxItem], done: Bool, action: InboxBulk.Action,
                     send: @escaping ([InboxItem]) async throws -> (result: InboxBulkResult, undo: InboxBulk.Undo?)) {
        let targets = InboxBulk.targets(list.compactMap { it in items.first { $0.id == it.id } }, done: done)
        guard !targets.isEmpty else { return }
        let before = Dictionary(targets.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        items = InboxBulk.marking(items, ids: Set(before.keys), done: done, now: .now, by: action)
        done ? Haptics.success() : Haptics.tap()
        Task {
            do {
                let (result, undo) = try await send(targets)
                items = InboxBulk.settle(items, before: before, result: result)
                let snapshot = items
                saver.run { StateCache.save(Self.stateName, snapshot) }
                if InboxBulk.changedSince(result) > 0 { reload() }
                let button = undo.map { u in Toast.Action(title: String(localized: "Undo")) { [weak self] in self?.undo(u, done: done) } }
                AppModel.shared.toast = Toast(InboxBulk.message(done: done, result: result, action: action), style: .success, action: button)
            } catch {
                items = InboxBulk.rollback(items, before: before)
                AppModel.shared.show(error)
            }
        }
    }

    /// The toast's Undo: the hub's `undo` as it came (only what that request changed), or the Done request back.
    private func undo(_ undo: InboxBulk.Undo, done: Bool) {
        switch undo {
        case .legacy(let moved):
            setDone(moved, !done, undoable: false)
        case .bulk(let u):
            guard let api = AppModel.shared.api else { return }
            let ids = Set(u.ids)
            run(items.filter { ids.contains($0.id) }, done: false, action: .done) { _ in
                (InboxBulk.result(try await api.undoInboxBulk(u)), nil)
            }
        }
    }

    /// The Done request for many, one at a time on a hub from before it.
    private func fileDone(_ targets: [InboxItem], _ done: Bool, api: APIClient) async throws -> InboxBulkResult {
        do {
            return try await api.setInboxDone(targets, done)
        } catch let e as APIError where e.status == 404 || e.status == 405 {
            return try await setEach(targets, done, api: api) // a hub from before the bulk request
        }
    }

    /// The bulk request one item at a time, for an older hub: no versions there, so each is filed as it is.
    private func setEach(_ targets: [InboxItem], _ done: Bool, api: APIClient) async throws -> InboxBulkResult {
        var changed: [InboxItem] = []
        for it in targets {
            try await api.setInboxDone(it.id, done)
            var moved = it
            moved.doneAt = done ? .now : nil
            moved.doneBy = done ? "user" : nil
            changed.append(moved)
        }
        return InboxBulkResult(changed: changed, unchanged: [], skipped: [])
    }

    func setDone(_ item: InboxItem, _ done: Bool, haptic: Bool = true) {
        guard let i = items.firstIndex(where: { $0.id == item.id }) else { return }
        let before = items[i]
        items[i].doneAt = done ? .now : nil
        items[i].doneBy = done ? "user" : nil
        if haptic { Haptics.tap() }
        Task {
            do {
                try await AppModel.shared.api?.setInboxDone(item.id, done)
            } catch {
                if let j = items.firstIndex(where: { $0.id == item.id }) { items[j] = before }
                AppModel.shared.show(error)
            }
        }
    }

    /// Mute This Repository / Session / Sender (the row's menu): the item and the others of its source it covers are done at once;
    /// the hub keeps the mute, so later ones do not come in. A failure puts them back.
    func mute(_ item: InboxItem, _ option: InboxItem.MuteOption) {
        let before = items
        let covers = { (it: InboxItem) in
            it.source == item.source && !it.isDone && (it.mutes ?? []).contains { $0.tag == option.tag && $0.value == option.value }
        }
        for i in items.indices where covers(items[i]) {
            items[i].doneAt = .now
            items[i].doneBy = "read"
            items[i].reason = String(localized: "Muted: \(option.name)")
        }
        Haptics.success()
        Task {
            do {
                _ = try await AppModel.shared.api?.muteInbox(item.id, tag: option.tag)
                AppModel.shared.toast = Toast(String(localized: "Muted “\(option.name)”. It won’t come to the Inbox anymore."), style: .success)
            } catch {
                items = before
                AppModel.shared.show(error)
            }
        }
    }

    /// A rule's tier shown on the items it reaches before the hub answers, and put back as they were when it fails
    /// (InboxPolicyModel): items are matched by id, ones that left the list meanwhile stay out.
    func showRuleChange(_ changed: [InboxItem]) {
        guard !changed.isEmpty else { return }
        let byID = Dictionary(changed.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        items = items.map { byID[$0.id] ?? $0 }
    }

    /// Items whose reply is being sent from the list (the row shows a spinner, a second tap does nothing).
    private(set) var sending: Set<String> = []

    /// Replies that did not go out, by item: the reply sheet opens with that text again.
    var unsentReplies: [String: String] = [:]

    /// Send on the AI Twin's draft: the hub replies in the chat / by mail / in the document (moving the rows' status
    /// in `changes` first) and files the item as done. Filed at once (the reply takes Feishu / the mail server a few
    /// seconds); a failure puts the item back and keeps the text for the reply sheet. `card`: sent from that message card.
    @discardableResult
    func send(_ item: InboxItem, text: String, changes: [InboxItem.DocChange] = [], card: String? = nil) async -> Bool {
        guard let api = AppModel.shared.api, !sending.contains(item.id) else { return false }
        sending.insert(item.id)
        defer { sending.remove(item.id) }
        let before = items.first { $0.id == item.id }
        if let i = items.firstIndex(where: { $0.id == item.id }) {
            items[i].doneAt = .now
            items[i].doneBy = "replied"
        }
        unsentReplies[item.id] = nil
        Haptics.tap()
        do {
            try await api.inboxReply(item.id, text: text, changes: changes, card: card)
            Haptics.success()
            AppModel.shared.toast = Toast(String(localized: "Sent and marked as done"), style: .success)
            return true
        } catch {
            if let before, let i = items.firstIndex(where: { $0.id == item.id }) { items[i] = before }
            if !text.isEmpty { unsentReplies[item.id] = text }
            if let shown = error.shown {
                AppModel.shared.toast = Toast(String(localized: "Reply not sent: \(shown)"), style: .error)
                Haptics.error()
            }
            return false
        }
    }

    /// The hub's copy of one item, as an answer returned it (acting on its suggestion): replaces the list's, kept on disk.
    func put(_ item: InboxItem) {
        guard let i = items.firstIndex(where: { $0.id == item.id }) else { return }
        // An answer carries no `version` of the listing; keep the one listed so a bulk Done still sees it.
        var fresh = item
        if fresh.version == nil { fresh.version = items[i].version }
        guard items[i] != fresh else { return }
        items[i] = fresh
        let snapshot = items
        saver.run { StateCache.save(Self.stateName, snapshot) }
    }

    /// Status moves being written, by "<item>|<row>|<field>" (the menu shows a spinner).
    private(set) var moving: Set<String> = []

    /// Moves a row's status in its document: shown at once, written by the hub, the item replaced by the hub's fresh
    /// read; a failure puts the old value back.
    func setDocStatus(_ item: InboxItem, row: InboxItem.DocRow, status: InboxItem.DocStatus, to value: String) {
        guard value != status.value, let api = AppModel.shared.api else { return }
        let key = item.id + "|" + row.ref + "|" + status.field
        guard !moving.contains(key), let i = items.firstIndex(where: { $0.id == item.id }) else { return }
        let before = items[i]
        items[i].doc?.set(row: row.ref, field: status.field, to: value)
        moving.insert(key)
        Haptics.tap()
        Task {
            defer { moving.remove(key) }
            do {
                let fresh = try await api.inboxDocStatus(item.id, .init(ref: row.ref, field: status.field, value: value))
                if let j = items.firstIndex(where: { $0.id == item.id }) { items[j] = fresh }
                let snapshot = items
                saver.run { StateCache.save(Self.stateName, snapshot) }
                AppModel.shared.toast = Toast(String(localized: "Changed “\(status.name)” to \(value)"), style: .success)
            } catch {
                if let j = items.firstIndex(where: { $0.id == item.id }) { items[j] = before }
                AppModel.shared.show(error)
            }
        }
    }

    /// Feishu chats in the inbox are the ones background sync keeps fresh (their pages open at once).
    private func teachLarkSync() {
        LarkSync.shared.know(items.compactMap(\.larkConversation))
    }
}

extension InboxItem.Doc {
    mutating func set(row ref: String, field: String, to value: String) {
        guard let r = rows?.firstIndex(where: { $0.ref == ref }), let f = rows?[r].status?.firstIndex(where: { $0.field == field }) else { return }
        rows?[r].status?[f].value = value
    }

    /// The row and column a change names, for showing it ("Status: Active → Done").
    func describe(_ change: InboxItem.DocChange) -> (row: InboxItem.DocRow, status: InboxItem.DocStatus)? {
        guard let row = rows?.first(where: { $0.ref == change.ref }), let st = row.status?.first(where: { $0.field == change.field }) else { return nil }
        return (row, st)
    }
}

extension InboxItem {
    /// The Feishu chat to open, at the first new message.
    var larkConversation: LarkConversation? {
        guard let l = lark else { return nil }
        var c = LarkConversation(id: l.feedID, name: title, avatar: avatar, isGroup: l.isGroup, isTopic: l.isTopic,
                                 unread: isDone ? 0 : count, atMe: kind == "mention")
        c.updatedAt = updatedAt
        c.readPosition = l.readPosition
        c.account = l.olAccount
        // 「飞书 · <name>」 while the hub reads more than one account.
        if let name = accountName.split(separator: " · ", maxSplits: 1).dropFirst().first { c.accountName = String(name) }
        if !isDone, let p = l.startPosition { c.focus = p }
        return c
    }

    /// The newest mail of the thread, for the mail reader.
    var mailSummary: MailSummary? {
        guard let m = mail, let uid = m.uids.last else { return nil }
        return MailSummary(accountID: m.accountID, mailbox: m.mailbox, uid: uid, messageID: m.messageID, subject: title,
                           from: m.from, date: updatedAt, snippet: preview, seen: isDone || read == true)
    }
}

extension MailSummary {
    init(accountID: String, mailbox: String, uid: UInt32, messageID: String?, subject: String?, from: MailAddress?, date: Date?,
         snippet: String?, seen: Bool) {
        self.accountID = accountID
        self.mailbox = mailbox
        self.uid = uid
        self.messageID = messageID
        self.subject = subject
        self.from = from
        self.to = nil
        self.date = date
        self.snippet = snippet
        self.seen = seen
        flagged = false
        hasAttachments = false
    }
}
