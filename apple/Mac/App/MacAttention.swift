import AppKit
import Foundation
import UserNotifications

/// What the Mac does outside its window when an agent needs you or finishes (docs/apple/mac.md「和 iOS 的对应」):
/// the Dock badge (how many cards wait on you) and a system notification for each agent that just turned Needs You /
/// Done, plus one for every newly arrived card while it is already in Needs You (#4081, a sub-agent's authorization
/// or a second confirmation for the same agent).
/// While the app runs it watches its own session list (the WebSocket stays open) and follows the same Settings › Notifications
/// choices (`NotifyPrefs`); the hub pushes these to the Mac only while it is not connected (docs/apple/mac.md「远程推送（Mac）」).
enum MacAttention {
    /// The notifications to post for a session-list change: agents that turned needs-you or finished. Not for the
    /// chat on screen while the app is in front, a muted agent, a session the list just learned about, or a finish
    /// right after a needs-you (the same stop). Persona's own conversations ping when they ask, not when they reply.
    /// `looking`: sessions whose every pending card is a question the assistant is still looking at (#544,
    /// `lookingOnly`): the hub counts them as Needs You, but nothing is announced until a card is handed to the user.
    static func transitions(old: [Session], new: [Session], open: SessionKey?, appActive: Bool, prefs: NotifyPrefs,
                            now: Date, looking: Set<SessionKey> = [], muted: (SessionKey) -> Bool) -> [(Session, AgentState)] {
        let before = Dictionary(old.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        let longTurn = TimeInterval(prefs.longTurnSeconds ?? 60) // the hub's default (notify.go)
        return new.compactMap { s in
            guard let was = before[s.key], was.state != s.state,
                  MacAttention.allowsBanner(key: s.key, open: open, appActive: appActive, muted: muted) else { return nil }
            switch s.state {
            case .needsYou:
                return prefs.input && !looking.contains(s.key) ? (s, .needsYou) : nil
            case .unread:
                guard !s.assistant, was.state != .needsYou else { return nil }
                switch prefs.done {
                case .off: return nil
                case .always: return (s, .unread)
                case .long:
                    // How long the turn ran: since the hub saw it start working.
                    guard was.state == .working, let since = was.stateSince, now.timeIntervalSince(since) >= longTurn else { return nil }
                    return (s, .unread)
                }
            default:
                return nil
            }
        }
    }

    /// The shared banner rules for the state-flip notifications and the per-card ones, so the two never drift: the
    /// chat on screen while the app is in front stays quiet, and a muted agent or a quiet task's (#1731) never pings.
    static func allowsBanner(key: SessionKey, open: SessionKey?, appActive: Bool, muted: (SessionKey) -> Bool) -> Bool {
        if appActive && key == open { return false }
        return !muted(key)
    }

    /// The newly arrived cards that each need a banner of their own (#4081): the hub pushes the phone once per card,
    /// and an agent already waiting on one card can ask again (a sub-agent's authorization, a second confirmation)
    /// without leaving Needs You, so `transitions` alone misses the second one. A card whose uid was in the old list is
    /// never reported again, and cards removed (answered) and re-added are judged by the lists as they are now.
    /// `transitionedKeys` names the sessions flipping to Needs You in this change (or within the watcher's short grace
    /// for a state event that beats the card event): their oldest waiting card is what the flip banner already
    /// carries, so it is not posted twice — two cards landing at once still ping the second one. With Needs You
    /// pushes turned off (`prefs.input`) no card pings.
    static func newInteractions(old: [Interaction], new: [Interaction], transitionedKeys: Set<SessionKey>,
                                open: SessionKey?, appActive: Bool, prefs: NotifyPrefs,
                                muted: (SessionKey) -> Bool) -> [Interaction] {
        guard prefs.input else { return [] }
        // A question the assistant is still looking at is no news; handed to the user, it arrives (#544).
        let old = old.filter { !$0.isDelegating }, new = new.filter { !$0.isDelegating }
        let alreadyThere = Set(old.map(\.uid))
        // The card each flip banner shows: the session's oldest still-waiting card, the one pendingInteractions
        // (oldest first) hands that banner in the watcher.
        let announced = Set(transitionedKeys.compactMap { key -> String? in
            new.filter { $0.sessionKey == key }
                .min { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }?.uid
        })
        return new.filter { ix in
            !alreadyThere.contains(ix.uid) && !announced.contains(ix.uid)
                && allowsBanner(key: ix.sessionKey, open: open, appActive: appActive, muted: muted)
        }
    }

    /// This device's push switch per manager (#1863, the prefs' `muted_managers`): a switched-off manager's agents ping
    /// no banner here either. The Dock still counts their cards: the switch is about being told, not about what waits.
    /// The prefs are those of `group`'s hub (the hub-level one): another group's hub keeps its own, so its agents are
    /// left alone. A session or card without `manager` is the hub's own assistant's.
    static func managerMuted(_ prefs: NotifyPrefs, group: String, sessions: [Session], interactions: [Interaction]) -> (SessionKey) -> Bool {
        guard prefs.mutedManagers?.isEmpty == false else { return { _ in false } }
        var manager: [SessionKey: String?] = [:]
        for s in sessions where s.key.group == group { manager[s.key] = s.manager }
        for ix in interactions where ix.sessionKey.group == group && manager[ix.sessionKey] == nil { manager[ix.sessionKey] = ix.manager }
        return { key in
            guard key.group == group, let m = manager[key] else { return false }
            return prefs.mutes(manager: m)
        }
    }

    /// The sessions whose every pending card is a question the assistant is still looking at (#544, the web's
    /// `lookingOnly`).
    static func lookingOnly(_ interactions: [Interaction]) -> Set<SessionKey> {
        var looking = Set<SessionKey>(), other = Set<SessionKey>()
        for ix in interactions { if ix.isDelegating { looking.insert(ix.sessionKey) } else { other.insert(ix.sessionKey) } }
        return looking.subtracting(other)
    }

    /// The Dock badge: waiting cards, not sessions — an agent already asking with a second card counts both (#4081),
    /// the same unit as the iPhone's Agents tab and the menu bar icon. Muted agents and quiet tasks (#1731) stay out
    /// like the sidebar's badges; the chat on screen still counts (a badge is not a banner).
    static func badge(_ interactions: [Interaction], muted: (SessionKey) -> Bool) -> String? {
        let n = interactions.filter { !muted($0.sessionKey) }.count
        return n > 0 ? "\(n)" : nil
    }

    /// The banner of one agent and state: one per session and state, so a second Done for the same agent replaces
    /// the first.
    static func identifier(_ s: Session, _ state: AgentState) -> String { "\(s.machineID)/\(s.sessionID)#\(state.rawValue)" }

    /// The banner of one card (`newInteractions`), named by its uid like the hub push's collapse id.
    static func identifier(_ ix: Interaction) -> String { "interaction/\(ix.uid)" }

    /// The banners to take back (#3513): the Needs You one of an agent no longer waiting on you (answered here or
    /// elsewhere, or gone from the list), and the one of each card that left the list.
    static func withdrawn(old: [Session], new: [Session], oldInteractions: [Interaction], newInteractions: [Interaction]) -> [String] {
        let now = Dictionary(new.map { ($0.key, $0.state) }, uniquingKeysWith: { a, _ in a })
        let pending = Set(newInteractions.map(\.uid))
        return old.filter { $0.state == .needsYou && now[$0.key] != .needsYou }.map { identifier($0, .needsYou) }
            + oldInteractions.filter { !pending.contains($0.uid) }.map(identifier)
    }

    static func text(_ s: Session, _ state: AgentState) -> (title: String, body: String) {
        let title = state == .needsYou ? String(localized: "\(s.displayTitle) needs you") : String(localized: "\(s.displayTitle) is done [Mac Attention b7e215]")
        // No last text yet: name the speaker. The main conversation is Persona, not an agent.
        let actor = s.assistant ? "Persona" : "Agent"
        let body = s.lastText?.replacingOccurrences(of: "\n", with: " ")
            ?? (state == .needsYou ? String(localized: "\(actor) stopped and is waiting for you") : String(localized: "\(actor) is done [Mac Attention b7e215]"))
        return (title, String(body.prefix(200)))
    }
}

extension MacAttention {
    /// The session a card belongs to. An `.interaction` event can briefly beat its session update, so when the list
    /// has no matching session decode a bare Needs You stand-in from the card: better to banner (titled by the card)
    /// than drop it. Returns nil only if that fixed shape ever fails to decode.
    static func session(for ix: Interaction, in sessions: [Session]) -> Session? {
        if let s = sessions.first(where: { $0.key == ix.sessionKey }) { return s }
        let object: [String: Any] = [
            "machine_id": ix.machineID, "session_id": ix.sessionID, "title": ix.sessionTitle ?? "Agent",
            "cwd": ix.cwd ?? "", "status": "idle", "state": AgentState.needsYou.rawValue,
            "started_by_persona": false, "assistant": ix.toolName == "ask_user", "pending_interactions": 1,
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: object),
              let standIn = try? JSON.decoder.decode(Session.self, from: data) else { return nil }
        return standIn
    }

    /// The banner for one agent. The userInfo keys are the hub push's (`machine_id`, `session_id`, `interaction_id`),
    /// so MacAppDelegate opens the chat or resolves a button the same way for both.
    static func content(_ s: Session, _ state: AgentState, interaction: Interaction?) -> UNMutableNotificationContent {
        let (title, body) = text(s, state)
        let c = UNMutableNotificationContent()
        c.title = title
        c.body = body
        c.threadIdentifier = s.machineID
        c.sound = state == .needsYou ? .default : nil
        var info: [String: String] = ["machine_id": s.machineID, "session_id": s.sessionID]
        if state == .needsYou, let interaction {
            c.categoryIdentifier = NotificationActions.category(interaction.kind)
            info["interaction_id"] = interaction.id
        }
        c.userInfo = info
        return c
    }
}

/// Keeps the Dock badge and the notifications in step with AppModel's session list, window open or not.
@MainActor final class MacAttentionWatcher {
    static let shared = MacAttentionWatcher()

    private var last: [Session] = []
    private var lastInteractions: [Interaction] = []
    /// Sessions whose flip to Needs You has not met its card event yet, with the flip time (the short grace below).
    /// The state event can beat the `.interaction` event by one main-queue turn; without this the card would then
    /// ping on top of the flip banner.
    private var flippedAt: [SessionKey: Date] = [:]
    /// When the current connected stretch began (MacAttention.connectedAt); nil until the first connect, and again
    /// while the socket is down, so a reconnect runs the settle instead of diffing the offline list at once.
    private var connectedAt: Date?
    private var asked = false

    func start(_ model: AppModel) {
        observe(model)
    }

    private func observe(_ model: AppModel) {
        let sessions = withObservationTracking {
            _ = model.pendingInteractions.count
            _ = model.config?.url
            _ = model.live.state
            // Muting changes the badge too: read what isMuted reads, and what isQuiet does (a task found quiet, #1731).
            _ = model.allSessions.map { model.isMuted($0) || model.isQuiet($0) }
            return model.allSessions
        } onChange: {
            // Called before the change lands; read the new values on the next turn of the main queue.
            DispatchQueue.main.async { MainActor.assumeIsolated { self.observe(model) } }
        }
        update(model, sessions: sessions)
    }

    private func update(_ model: AppModel, sessions: [Session]) {
        // A quiet task's agent (#1731) is the main conversation's to tell: no banner, no count, like a muted one.
        let mutedKey: (SessionKey) -> Bool = { model.isMuted($0) || model.isQuiet($0) }
        let interactions = model.pendingInteractions
        // One tick per waiting card (the iPhone Agents tab's unit), not per session: a second card moves it (#4081).
        NSApp.dockTile.badgeLabel = model.isPaired ? MacAttention.badge(interactions, muted: mutedKey) : nil
        defer {
            last = sessions
            lastInteractions = interactions
        }
        // Not only once armed: a banner from before is as stale as one posted now.
        let over = MacAttention.withdrawn(old: last, new: sessions, oldInteractions: lastInteractions, newInteractions: interactions)
        if !over.isEmpty {
            UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: over)
            waiting.removeAll { over.contains($0.identifier) }
        }
        let now = Date()
        let startingToConnect = connectedAt == nil && model.live.state == .connected
        connectedAt = MacAttention.connectedAt(connectedAt, state: model.live.state, now: now)
        if startingToConnect {
            // Nothing else may change after the settle time: look again then.
            DispatchQueue.main.asyncAfter(deadline: .now() + MacAttention.settle + 0.1) { [weak self] in
                MainActor.assumeIsolated {
                    self?.last = model.allSessions
                    self?.lastInteractions = model.pendingInteractions
                }
            }
        }
        // Demo Mode's agents are made up.
        guard !model.isDemo, MacAttention.armed(connectedAt: connectedAt, now: now), !last.isEmpty else { return }
        let prefs = LiveActivityController.shared.prefs ?? NotifyPrefs()
        let managerMuted = MacAttention.managerMuted(prefs, group: model.hub?.group ?? "", sessions: sessions, interactions: interactions)
        let bannerMuted: (SessionKey) -> Bool = { mutedKey($0) || managerMuted($0) }
        let ping = MacAttention.transitions(old: last, new: sessions, open: model.visibleSession, appActive: NSApp.isActive,
                                            prefs: prefs, now: now, looking: MacAttention.lookingOnly(interactions), muted: bannerMuted)
        // The flip banner of a Needs You turn already carries that session's oldest card: remember the flip for this
        // turn (cards in the same list) and for one settle-length grace in case the card event lands a turn later.
        for (s, state) in ping where state == .needsYou { flippedAt[s.key] = now }
        flippedAt = flippedAt.filter { now.timeIntervalSince($0.value) <= MacAttention.settle }
        let cards = MacAttention.newInteractions(old: lastInteractions, new: interactions,
                                                 transitionedKeys: Set(flippedAt.keys),
                                                 open: model.visibleSession, appActive: NSApp.isActive,
                                                 prefs: prefs, muted: bannerMuted)
        // The flip has met its card once a card for that session is present now: the flip banner covered it, and the
        // next card of this stop must ping even if it still lands inside the grace.
        let metKeys = flippedAt.keys.filter { key in interactions.contains { $0.sessionKey == key } }
        for key in metKeys { flippedAt[key] = nil }
        guard !ping.isEmpty || !cards.isEmpty else { return }
        var requests = ping.map { s, state in
            UNNotificationRequest(identifier: MacAttention.identifier(s, state),
                                  content: MacAttention.content(s, state, interaction: interactions.first { $0.sessionKey == s.key }),
                                  trigger: nil)
        }
        // One banner per new card, identified by the card's uid like the hub push's collapse id: answering removes
        // the card and a later card (a different uid) pings again, even for the same agent.
        requests += cards.compactMap { ix -> UNNotificationRequest? in
            guard let s = MacAttention.session(for: ix, in: sessions) else { return nil }
            return UNNotificationRequest(identifier: MacAttention.identifier(ix),
                                         content: MacAttention.content(s, .needsYou, interaction: ix), trigger: nil)
        }
        waiting += requests
        if asked {
            postWaiting()
        } else {
            // Asked the first time there is something to say, not at launch; posted once answered (added while
            // the question is open, they would be dropped).
            asked = true
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { @Sendable granted, _ in
                DispatchQueue.main.async {
                    MainActor.assumeIsolated {
                        if granted { MacAttentionWatcher.shared.postWaiting() } else { MacAttentionWatcher.shared.waiting = [] }
                    }
                }
            }
        }
    }

    /// Notifications made before permission was answered, then posted.
    private var waiting: [UNNotificationRequest] = []

    private func postWaiting() {
        let center = UNUserNotificationCenter.current()
        for r in waiting { center.add(r) { @Sendable _ in } }
        waiting = []
    }
}

extension MacAttention {
    /// How long after the socket connects the list counts as the hub's: the refresh and the catch-up land then.
    static let settle: TimeInterval = 3

    /// Notifications start once the hub has answered: before, the list is the cache from the last run, and its
    /// differences from the first refresh are things that happened while the app was closed.
    static func armed(connectedAt: Date?, now: Date) -> Bool {
        guard let connectedAt else { return false }
        return now.timeIntervalSince(connectedAt) >= settle
    }

    /// When the current connected stretch began, from the watcher's stored value and the socket state. The first
    /// `.connected` arms at `now`; leaving `.connected` (a drop, a failed attempt, `.idle`) clears it, so when the
    /// socket reconnects `armed` and the settle run again instead of replaying the offline list's jumps as banners
    /// (which the hub may also have delivered as remote pushes). Staying `.connected` keeps the original value.
    static func connectedAt(_ current: Date?, state: LiveConnection.State, now: Date) -> Date? {
        switch state {
        case .connected:
            return current ?? now
        case .idle, .connecting, .failed:
            return nil
        }
    }
}
