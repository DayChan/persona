import Foundation
import Testing
@testable import Persona

/// When the Mac posts a notification for an agent (it has no APNs; docs/apple/mac.md push notifications → system notifications), following the
/// same Settings › Notifications choices the hub uses for the phone (NotifyPrefs).
@MainActor
struct MacAttentionTests {
    private let now = Date(timeIntervalSince1970: 1_000_000)

    private func session(_ id: String, _ state: AgentState, assistant: Bool = false, since: TimeInterval = 0,
                         lastText: String? = "done it") -> Session {
        let last = lastText.map { ",\"last_text\":\"\($0)\"" } ?? ""
        var s = try! JSON.decoder.decode(Session.self, from: Data("""
        {"machine_id":"m","session_id":"\(id)","title":"t-\(id)","cwd":"/w/proj","status":"idle","state":"\(state.rawValue)",
         "started_by_persona":false,"assistant":\(assistant),"pending_interactions":0\(last)}
        """.utf8))
        s.stateSince = now.addingTimeInterval(-since)
        return s
    }

    private func ping(_ old: [Session], _ new: [Session], open: SessionKey? = nil, active: Bool = false,
                      prefs: NotifyPrefs = NotifyPrefs(done: .always), muted: Set<String> = []) -> [String] {
        MacAttention.transitions(old: old, new: new, open: open, appActive: active, prefs: prefs, now: now,
                                 muted: { muted.contains($0.sessionID) }).map { "\($0.0.sessionID):\($0.1.rawValue)" }
    }

    private func interaction(_ id: String, session: String = "a", machine: String = "m",
                             kind: String = "permission", title: String? = nil, createdAt: String? = nil,
                             group: String? = nil) throws -> Interaction {
        let created = createdAt.map { ",\"created_at\":\"\($0)\"" } ?? ""
        let sessionTitle = title.map { ",\"session_title\":\"\($0)\"" } ?? ""
        let groupJSON = group.map { ",\"app_hub_group\":\"\($0)\"" } ?? ""
        return try JSON.decoder.decode(Interaction.self, from: Data("""
        {"id":"\(id)","machine_id":"\(machine)","session_id":"\(session)","kind":"\(kind)"\(created)\(sessionTitle)\(groupJSON)}
        """.utf8))
    }

    /// The new-card banners as interaction ids (#4081).
    private func cards(_ old: [Interaction], _ new: [Interaction], transitioned: Set<SessionKey> = [],
                       open: SessionKey? = nil, active: Bool = false,
                       prefs: NotifyPrefs = NotifyPrefs(done: .always),
                       muted: Set<String> = []) -> [String] {
        MacAttention.newInteractions(old: old, new: new, transitionedKeys: transitioned, open: open,
                                     appActive: active, prefs: prefs,
                                     muted: { muted.contains($0.sessionID) }).map(\.id)
    }

    @Test func needsYouAndFinishedNotify() {
        let out = ping([session("a", .working), session("b", .working)], [session("a", .needsYou), session("b", .unread)])
        #expect(out == ["a:needs_you", "b:unread"])
    }

    @Test func theChatOnScreenStaysQuietWhileTheAppIsInFront() {
        let open = SessionKey(machineID: "m", sessionID: "a")
        #expect(ping([session("a", .working)], [session("a", .unread)], open: open, active: true).isEmpty)
        #expect(ping([session("a", .working)], [session("a", .unread)], open: open, active: false) == ["a:unread"])
    }

    @Test func noNoiseForNewOrUnchangedSessionsOrASecondStop() {
        #expect(ping([session("a", .unread)], [session("a", .unread), session("n", .needsYou)]).isEmpty)
        // Answered, then finished without working in between: the same stop, one ping.
        #expect(ping([session("a", .needsYou)], [session("a", .unread)]).isEmpty)
    }

    @Test func mutedAgentsStayQuiet() {
        #expect(ping([session("a", .working)], [session("a", .needsYou)], muted: ["a"]).isEmpty)
    }

    @Test func aManagerSwitchedOffHereRingsNoBanner() throws {
        // #1863 (the prefs' muted_managers): a session or card without `manager` is the hub's own assistant's.
        var prefs = NotifyPrefs(done: .always)
        var theirs = session("b", .needsYou)
        theirs.manager = "m-2"
        var card = try interaction("x", session: "c")
        card.manager = "m-2"
        let key = { SessionKey(machineID: "m", sessionID: $0) }
        #expect(!MacAttention.managerMuted(prefs, group: "", sessions: [theirs], interactions: [card])(theirs.key), "none switched off")
        prefs.mutedManagers = ["m-2"]
        let muted = MacAttention.managerMuted(prefs, group: "", sessions: [session("a", .needsYou), theirs], interactions: [card])
        #expect(muted(theirs.key) && muted(key("c")) && !muted(key("a")))
        // The banners: the switched-off manager's agent and card ring none, the other's do.
        let rang = MacAttention.transitions(old: [session("a", .working), session("b", .working)], new: [session("a", .needsYou), theirs],
                                            open: nil, appActive: false, prefs: prefs, now: now, muted: muted)
        #expect(rang.map { $0.0.sessionID } == ["a"])
        let other = try interaction("y", session: "a")
        #expect(MacAttention.newInteractions(old: [], new: [card, other], transitionedKeys: [], open: nil, appActive: false,
                                             prefs: prefs, muted: muted).map(\.id) == ["y"])
        // Another group's hub keeps its own prefs: these leave its agents alone.
        var elsewhere = theirs
        elsewhere.group = "g2"
        #expect(!MacAttention.managerMuted(prefs, group: "", sessions: [elsewhere], interactions: [])(elsewhere.key))
        prefs.mutedManagers = ["assistant"]
        #expect(MacAttention.managerMuted(prefs, group: "", sessions: [session("a", .needsYou)], interactions: [])(key("a")), "no stamp: the hub's own assistant")
    }

    @Test func personaAsksButItsRepliesDoNotPing() {
        let old = [session("j", .working, assistant: true), session("k", .working, assistant: true)]
        let new = [session("j", .needsYou, assistant: true), session("k", .unread, assistant: true)]
        #expect(ping(old, new) == ["j:needs_you"])
    }

    @Test func followsTheNotificationSettings() {
        let old = [session("a", .working, since: 30), session("b", .working, since: 300), session("c", .working)]
        let new = [session("a", .unread), session("b", .unread), session("c", .needsYou)]
        #expect(ping(old, new, prefs: NotifyPrefs(input: false, done: .off)).isEmpty)
        // Long Tasks Only: the turn that ran past the threshold (60 s unless the hub says otherwise).
        #expect(ping(old, new, prefs: NotifyPrefs(done: .long)) == ["b:unread", "c:needs_you"])
        var short = NotifyPrefs(done: .long)
        short.longTurnSeconds = 20
        #expect(ping(old, new, prefs: short) == ["a:unread", "b:unread", "c:needs_you"])
    }

    @Test func dockBadgeCountsWaitingCards() throws {
        // Two cards for one already-waiting agent (#4081), one for another, a muted one's, and the Persona
        // conversation's ask: cards are the unit, muted stays out, the Persona conversation keeps counting.
        let list = try [interaction("a1", session: "a"), interaction("a2", session: "a"),
                        interaction("b1", session: "b"), interaction("j1", session: "j")]
        #expect(MacAttention.badge(list, muted: { $0.sessionID == "b" }) == "3")
        #expect(MacAttention.badge([], muted: { _ in false }) == nil)
    }

    /// An agent already in Needs You gets a second card (a sub-agent's authorization, another confirmation): the
    /// state never flips, but the card is new, so it gets its own banner.
    @Test func secondCardForAnAlreadyWaitingAgentPings() throws {
        let a = try interaction("a"), b = try interaction("b")
        #expect(cards([a], [a, b]) == ["b"])
    }

    /// A card already in the last list is never announced again; one answered (gone from the list) is not reported;
    /// a card genuinely new to an empty list is (the watcher only passes that when its flip banner did not take it).
    @Test func oldAndResolvedCardsDoNotPingAgain() throws {
        let a = try interaction("a"), b = try interaction("b")
        #expect(cards([a], [a]).isEmpty)
        #expect(cards([a, b], [a]).isEmpty)
        #expect(cards([], [a]) == ["a"])
    }

    /// The flip-to-needs-you banner already carries that session's oldest waiting card, so it is not posted twice;
    /// two cards landing with the flip still ping the second one.
    @Test func flipBannerAndCardBannerDoNotDoubleUp() throws {
        let key = SessionKey(machineID: "m", sessionID: "a")
        #expect(cards([], [try interaction("a")], transitioned: [key]).isEmpty)
        let a = try interaction("a", createdAt: "2001-09-09T01:46:40Z")
        let b = try interaction("b", createdAt: "2001-09-09T01:46:41Z")
        #expect(cards([], [a, b], transitioned: [key]) == ["b"])
        // A card the old list already had when the state flips is the flip banner's, not reposted.
        #expect(cards([a], [a], transitioned: [key]).isEmpty)
    }

    /// New-card banners follow the same rules as the flip banners: frontmost open chat, muted/quiet, and the
    /// Needs You notification setting.
    @Test func newCardsFollowTheSameMuteFrontAndSettingsRules() throws {
        let open = SessionKey(machineID: "m", sessionID: "a")
        #expect(cards([try interaction("a")], [try interaction("a"), try interaction("b")],
                      open: open, active: true).isEmpty)
        #expect(cards([try interaction("a")], [try interaction("a"), try interaction("b")],
                      open: open, active: false) == ["b"])
        #expect(cards([], [try interaction("a")], muted: ["a"]).isEmpty)
        #expect(cards([], [try interaction("a")], prefs: NotifyPrefs(input: false, done: .off)).isEmpty)
    }

    /// The same hub interaction id in two hub groups is two different cards (`uid` carries the group).
    @Test func sameHubIdInTwoGroupsAreTwoCards() throws {
        let one = try interaction("x", group: "g1")
        let other = try interaction("x", group: "g2")
        #expect(one.uid != other.uid)
        #expect(cards([one], [one, other]) == ["x"])
        #expect(cards([one], [one, other]).count == 1)
    }

    /// When the card event briefly leads the session event, the banner still goes out with a titled stand-in
    /// rather than being dropped; a matching session in the list wins.
    @Test func cardWithoutASessionUsesATitledStandIn() throws {
        let ix = try interaction("a", title: "build")
        let standIn = MacAttention.session(for: ix, in: [session("x", .working)])
        #expect(standIn?.key == ix.sessionKey)
        #expect(standIn?.state == .needsYou)
        #expect(MacAttention.content(standIn!, .needsYou, interaction: ix).title == "build 需要你")
        let real = session("a", .needsYou)
        #expect(MacAttention.session(for: ix, in: [real])?.title == "t-a")
    }

    @Test func text() {
        let t = MacAttention.text(session("a", .needsYou), .needsYou)
        #expect(t.title == "t-a 需要你")
        #expect(t.body == "done it")
        #expect(MacAttention.text(session("a", .unread), .unread).title == "t-a 做完了")
    }

    /// Without a last text yet the banner falls back to a fixed line; the main conversation names Persona,
    /// an agent session names Agent, for both states.
    @Test func fallbackTextNamesPersonaInTheMainConversation() {
        #expect(MacAttention.text(session("j", .needsYou, assistant: true, lastText: nil), .needsYou).body
                == "Persona 停下来等你处理")
        #expect(MacAttention.text(session("j", .unread, assistant: true, lastText: nil), .unread).body
                == "Persona 做完了")
        #expect(MacAttention.text(session("a", .needsYou, lastText: nil), .needsYou).body
                == "Agent 停下来等你处理")
        #expect(MacAttention.text(session("a", .unread, lastText: nil), .unread).body
                == "Agent 做完了")
    }

    /// A needs-you about a permission or a plan carries the hub's category and the interaction, so the banner has the
    /// same Allow / Deny / Approve / Send Back buttons as the phone's push (MacAppDelegate resolves them).
    @Test func needsYouCarriesTheInteractionsButtons() throws {
        let ix = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix1","machine_id":"m","session_id":"a","kind":"permission"}"#.utf8))
        let c = MacAttention.content(session("a", .needsYou), .needsYou, interaction: ix)
        #expect(c.categoryIdentifier == "INTERACTION_PERMISSION")
        #expect(c.userInfo["interaction_id"] as? String == "ix1")
        #expect(c.userInfo["session_id"] as? String == "a")
        #expect(c.threadIdentifier == "m")
        let done = MacAttention.content(session("a", .unread), .unread, interaction: nil)
        #expect(done.categoryIdentifier.isEmpty)
        #expect(done.userInfo["interaction_id"] == nil)
        #expect(done.userInfo["machine_id"] as? String == "m")
    }

    /// The first `.connected` arms at `now`, so the launch settle (MacAttention.armed) can run from that moment.
    @Test func connectedAtStartsWhenTheSocketFirstConnects() {
        #expect(MacAttention.connectedAt(nil, state: .connected, now: now) == now)
    }

    /// Nothing armed before the first connect either: a non-connected state with no value stays nil.
    @Test func connectedAtStaysNilBeforeTheFirstConnection() {
        #expect(MacAttention.connectedAt(nil, state: .idle, now: now) == nil)
        #expect(MacAttention.connectedAt(nil, state: .connecting, now: now) == nil)
        #expect(MacAttention.connectedAt(nil, state: .failed("hub away"), now: now) == nil)
    }

    /// The socket leaving `.connected` (a drop, a failed reconnect attempt, `.idle`) clears the timestamp, even though
    /// it ignores the passed `now`: the next `.connected` starts a fresh settle (#3469).
    @Test func connectedAtClearsWhenTheSocketLeavesConnected() {
        let connected = now.addingTimeInterval(-100)
        #expect(MacAttention.connectedAt(connected, state: .connecting, now: now) == nil)
        #expect(MacAttention.connectedAt(connected, state: .idle, now: now) == nil)
        #expect(MacAttention.connectedAt(connected, state: .failed("gone"), now: now) == nil)
    }

    /// Staying `.connected` keeps the original timestamp; ordinary list refreshes must not extend or restart the
    /// settle window.
    @Test func connectedAtKeepsTheOriginalValueWhileStayingConnected() {
        let connected = now.addingTimeInterval(-100)
        #expect(MacAttention.connectedAt(connected, state: .connected, now: now) == connected)
    }

    /// End-to-end semantics of the two pure functions the watcher drives: a drop and reconnect run the settle a second
    /// time. The first refresh after the reconnect (which folds in everything that changed offline) lands inside the
    /// new settle window, so it cannot be diffed into banners; only a change after the new settle would notify.
    @Test func reconnectRunsTheSettleASecondTime() {
        var at = MacAttention.connectedAt(nil, state: .connected, now: now)
        #expect(at == now)
        #expect(!MacAttention.armed(connectedAt: at, now: now.addingTimeInterval(1)))
        #expect(MacAttention.armed(connectedAt: at, now: now.addingTimeInterval(MacAttention.settle + 0.1)))

        // The socket drops at t + 100 and only comes back at t + 120.
        at = MacAttention.connectedAt(at, state: .connecting, now: now.addingTimeInterval(100))
        #expect(at == nil)
        #expect(!MacAttention.armed(connectedAt: at, now: now.addingTimeInterval(110)))
        at = MacAttention.connectedAt(at, state: .connected, now: now.addingTimeInterval(120))
        #expect(at == now.addingTimeInterval(120))
        // With the old timestamp kept, this would be armed immediately and replay the offline jumps as banners.
        #expect(!MacAttention.armed(connectedAt: at, now: now.addingTimeInterval(121)))
        #expect(MacAttention.armed(connectedAt: at, now: now.addingTimeInterval(120 + MacAttention.settle + 0.1)))
    }

    /// A Needs You banner goes once the agent stops waiting on you, a card's banner once the card leaves the list
    /// (#3513); the others stay.
    @Test func settledBannersAreTakenBack() throws {
        let old = [session("a", .needsYou), session("b", .needsYou), session("c", .needsYou), session("d", .working)]
        let new = [session("a", .working), session("b", .needsYou), session("d", .unread)]
        let ix1 = try interaction("ix1", session: "b"), ix2 = try interaction("ix2", session: "b")
        let other = try interaction("ix1", session: "b", group: "g2")
        #expect(MacAttention.withdrawn(old: old, new: new, oldInteractions: [ix1, ix2, other], newInteractions: [ix2, other])
            == ["m/a#needs_you", "m/c#needs_you", "interaction/ix1"])
        #expect(MacAttention.withdrawn(old: new, new: new, oldInteractions: [ix2], newInteractions: [ix2]).isEmpty)
        #expect(MacAttention.identifier(session("a", .needsYou), .needsYou) == "m/a#needs_you")
        #expect(MacAttention.identifier(other) == "interaction/g2|ix1")
    }

    /// A question the assistant is still looking at (#544) neither banners nor flips the agent to Needs You; the same
    /// card handed to the user arrives then, once.
    @Test func aCardTheAssistantIsLookingAtIsNoNewsUntilHandedOver() throws {
        var looking = try interaction("q", kind: "question")
        looking.delegating = true
        var handed = looking
        handed.delegating = nil
        #expect(cards([], [looking]).isEmpty)
        #expect(cards([looking], [handed]) == ["q"])
        #expect(cards([handed], [handed]).isEmpty)

        let perm = try interaction("p", session: "b")
        #expect(MacAttention.lookingOnly([looking, perm]) == [SessionKey(machineID: "m", sessionID: "a")])
        #expect(MacAttention.lookingOnly([looking, try interaction("p2")]).isEmpty, "another card of its own still waits on you")
        let flip = MacAttention.transitions(old: [session("a", .working)], new: [session("a", .needsYou)], open: nil, appActive: false,
                                            prefs: NotifyPrefs(done: .always), now: now,
                                            looking: MacAttention.lookingOnly([looking]), muted: { _ in false })
        #expect(flip.isEmpty)
    }
}
