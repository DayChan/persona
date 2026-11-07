import Foundation
import PersonaKit

/// Keeps the recent agents' conversations current while the app is open and online, the way an IM client syncs:
/// the hub pushes a tiny "this transcript changed" (each session's `rev`, in the session list and the `revs` event)
/// and the app pulls only what is new (`after_seq`) — into the chat's store when it is in memory, else into the disk
/// cache a chat paints from. Opening a chat then shows its latest messages at once. Chats on screen are live over
/// their own subscription and are left alone, unless that subscription stops bringing items (`checkStall`).
@MainActor
final class AgentSync {
    static let shared = AgentSync()

    /// Worth keeping current: every live agent, and any session used within this window.
    static let recentWindow: TimeInterval = 24 * 3600
    static let maxSessions = 40
    static let concurrency = 3
    /// A working agent's chat is pulled at most this often (`AgentSyncPacing`, in PersonaKit).
    nonisolated static let workingGap = AgentSyncPacing.workingGap

    private var known: [SessionKey: Int64] = [:]
    /// What has been pulled, by rev ("<machine>/<session>" → rev); kept across launches, beside the chats' disk cache it
    /// describes (`StateCache`). Not in UserDefaults: it changes with nearly every push, and each write to UserDefaults
    /// draws every `@AppStorage` view in the app again; the Agents list re-laid itself out under the finger
    /// (docs/archive/apple/live-updates.md「滚动时被迫刷新」).
    private var synced: [String: Int64]
    private var queue: [SessionKey] = []
    private var running = Set<SessionKey>()
    private var scheduled = false
    private var lastPull: [SessionKey: Date] = [:]
    /// When a plan is due for the working agents held back by `workingGap` (nil: none waiting).
    private var replanAt: Date?
    private let saver = Debouncer(.seconds(2))
    private static let stateName = "agent-sync"

    private init() {
        synced = StateCache.load(Self.stateName) ?? [:]
        // Where it was kept before 2026-11-01: the recent chats are pulled once more.
        UserDefaults.standard.removeObject(forKey: "agentSync.revs")
    }

    /// The disk cache was cleared: pull everything recent again.
    func forget() {
        synced = [:]
        save()
        schedule()
    }

    /// A machine's full session list arrived.
    func sessions(_ list: [Session]) {
        for s in list { if let r = s.rev { known[s.key] = r } }
        schedule()
    }

    /// Only some transcripts grew (the light `revs` event).
    func revs(machine: MachineRef, _ changed: [String: Int64]) {
        for (sid, r) in changed { known[SessionKey(group: machine.group, machineID: machine.id, sessionID: sid)] = r }
        schedule()
    }

    /// After a burst of events, look once.
    func schedule() {
        guard !scheduled else { return }
        scheduled = true
        Task {
            try? await Task.sleep(for: .milliseconds(300))
            scheduled = false
            plan()
        }
    }

    private func plan() {
        let model = AppModel.shared
        // Each group's agents while its own socket is up (docs/protocol/multi-hub.md).
        let up = Set(model.groupIDs.filter { model.isLive($0) })
        guard !up.isEmpty else { return }
        let cutoff = Date.now.addingTimeInterval(-Self.recentWindow)
        let recent = model.sessionsByMachine.values.joined()
            .filter { up.contains($0.group) }
            .filter { $0.status != .offline || ($0.updatedAt ?? .distantPast) > cutoff }
            .sorted { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
            .prefix(Self.maxSessions)
        var due: [SessionKey] = []
        var later: Date?
        let now = Date.now
        for s in recent {
            checkStall(s.key) // chats on screen only
            guard let rev = known[s.key] ?? s.rev, synced[s.id] != rev else { continue }
            if model.isSubscribed(s.key) {
                synced[s.id] = rev // on screen: its subscription brings what is new
            } else if !running.contains(s.key) {
                if let at = Self.heldUntil(working: s.state == .working, lastPull: lastPull[s.key], now: now) {
                    later = min(later ?? at, at)
                } else {
                    due.append(s.key)
                }
            }
        }
        queue = due // newest first; replaces an older plan
        pump()
        save()
        if let later { replan(at: later) }
    }

    /// When a chat with news is pulled (`AgentSyncPacing.heldUntil`).
    nonisolated static func heldUntil(working: Bool, lastPull: Date?, now: Date) -> Date? {
        AgentSyncPacing.heldUntil(working: working, lastPull: lastPull, now: now)
    }

    // MARK: Chats on screen

    /// When each chat on screen last asked for what its subscription lost, the `updatedAt` it asked about, when it is
    /// looked at again, and those asking now (`checkStall`).
    private var lastCatchUp: [SessionKey: Date] = [:]
    private var caughtUpFor: [SessionKey: Date] = [:]
    private var stallChecks: [SessionKey: Date] = [:]
    private var catchingUp = Set<SessionKey>()

    /// A chat on screen whose session is newer than its last arrival's update while its subscription stays quiet:
    /// the stream has stopped (a node whose transcript watch ended, a lost push). Asks for what came after the
    /// chat's `covered` and merges it (`AgentSyncPacing.catchUpAt`); looks again when its quiet or pacing ends. Not while
    /// its group's socket is down (in the background, offline): no push is due then, and the subscription asks for what
    /// it missed (`since_seq`) when the socket is back, whose next list looks again.
    private func checkStall(_ key: SessionKey) {
        let model = AppModel.shared
        guard model.isSubscribed(key), model.isLive(key.group), let store = model.existingChat(key),
              !catchingUp.contains(key)
        else { return }
        let updatedAt = model.latest(key)?.updatedAt // `session(_:)` keeps an older one on purpose
        guard let at = AgentSyncPacing.catchUpAt(updatedAt: updatedAt, arrivedUpdatedAt: store.arrivedUpdatedAt,
                                                lastArrival: store.arrivedAt, lastPull: lastCatchUp[key],
                                                pulledFor: caughtUpFor[key])
        else { return }
        guard at <= .now else { return lookAgain(key, at: at) }
        guard let api = model.api(key.group) else { return }
        lastCatchUp[key] = .now
        catchingUp.insert(key)
        ChatTrace.shared.note(key, "catch up: no items after covered=\(store.covered)")
        Task {
            do {
                // Not asked again for this write once one page held all of it: the write may have added no line. After
                // more pages (`fill`, `reload`, which keep their failures to themselves) the next look asks once more.
                if try await Self.pullInto(store, key, api) == .none { caughtUpFor[key] = updatedAt }
            } catch {
                ChatTrace.shared.note(key, "catch up failed: \(error.localizedDescription)")
            }
            catchingUp.remove(key)
            checkStall(key) // a newer write meanwhile, or a failure: when to ask again
        }
    }

    /// Looks at a chat on screen again at `date`: one wait per chat, the earliest wins.
    private func lookAgain(_ key: SessionKey, at date: Date) {
        if let due = stallChecks[key], due <= date { return }
        stallChecks[key] = date
        Task {
            try? await Task.sleep(for: .seconds(max(date.timeIntervalSinceNow, 0.05)))
            guard stallChecks[key] == date else { return }
            stallChecks[key] = nil
            checkStall(key)
        }
    }

    private func replan(at date: Date) {
        if let replanAt, replanAt <= date { return }
        replanAt = date
        Task {
            try? await Task.sleep(for: .seconds(max(date.timeIntervalSinceNow, 0.05)))
            guard replanAt == date else { return }
            replanAt = nil
            plan()
        }
    }

    private func pump() {
        while running.count < Self.concurrency, !queue.isEmpty {
            let key = queue.removeFirst()
            guard let rev = known[key] else { continue }
            running.insert(key)
            lastPull[key] = .now
            Task {
                let ok = await pull(key)
                running.remove(key)
                if ok { synced[key.path] = rev; save() } // `Session.id` of the same session
                pump()
            }
        }
    }

    private func save() {
        let snapshot = synced
        saver.run { StateCache.save(Self.stateName, snapshot) }
    }

    /// Brings one session up to date; false when the hub could not be reached (it is tried again on the next change).
    private func pull(_ key: SessionKey) async -> Bool {
        let model = AppModel.shared
        guard let api = model.api(key.group) else { return false }

        do {
            if let store = model.existingChat(key), !store.items.isEmpty {
                try await Self.pullInto(store, key, api)
                return true
            }
            try await Self.pullToDisk(key, api)
            return true
        } catch {
            return false
        }
    }

    /// A store in memory: what came after its `covered` goes into it (filling a hole, or starting over, when the batch
    /// says so). Returns what the first batch left to do.
    @discardableResult
    private static func pullInto(_ store: ChatStore, _ key: SessionKey, _ api: APIClient) async throws -> ChatStore.Gap {
        let asked = AppModel.shared.latest(key)?.updatedAt
        let r = try await api.items(key, afterSeq: store.covered)
        let gap = store.apply(r.items, span: r.span, moreAfter: r.moreAfter == true)
        if !r.items.isEmpty { store.arrivedUpdatedAt = asked }
        switch gap {
        case .none: break
        case .fill: await AppModel.shared.fill(key)
        case .reload: await AppModel.shared.reload(key)
        }
        return gap
    }

    /// No store in memory: update the disk cache the chat will open from (decoding and writing off the main thread).
    /// What comes after the cache's `covered` is added when it continues it; anything else (a capped catch-up, a
    /// replaced transcript) writes the latest page instead.
    private nonisolated static func pullToDisk(_ key: SessionKey, _ api: APIClient) async throws {
        let cached = await Task.detached(priority: .utility) { ChatCache.load(key) }.value
        if let cached, let covered = cached.covered ?? cached.items.last?.seq {
            let r = try await api.items(key, afterSeq: covered)
            let sameFile = r.history == nil || cached.history == nil || r.history == cached.history
            if sameFile && r.moreAfter != true && (r.fromSeq ?? covered) <= covered {
                if r.items.isEmpty && r.toSeq == nil { return }
                await Task.detached(priority: .utility) {
                    let have = Set(cached.items.map(\.id))
                    let all = ChatStore.ordered(cached.items + ChatStore.withImageRefs(r.items, key).filter { !have.contains($0.id) })
                    let tail = ChatCache.tail(all, turns: 2)
                    ChatCache.save(key, items: tail, hasMore: cached.hasMore || tail.count < all.count,
                                   history: r.history ?? cached.history, covered: max(covered, r.toSeq ?? all.last?.seq ?? covered))
                }.value
                return
            }
        }
        let page = try await api.items(key, beforeSeq: nil)
        await Task.detached(priority: .utility) {
            ChatCache.save(key, items: ChatStore.withImageRefs(page.items, key), hasMore: page.hasMore,
                           history: page.history, covered: page.toSeq)
        }.value
    }
}
