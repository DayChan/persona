import Foundation
import XCTest
@testable import PersonaLink

/// The "Needs you" summary in persona-link's status: decoding, which notifications to post and take back, and where a
/// row or a notification opens the web app.
@MainActor
final class AttentionTests: XCTestCase {
    private func status(_ json: String) throws -> LinkStatus {
        try LinkStatus.decode(Data(json.utf8))
    }

    private func item(_ session: String, card: String? = "ix-1", notify: Bool = true, muted: Bool = false) -> LinkAttention.Item {
        LinkAttention.Item(machineID: "laptop", sessionID: session, kind: "permission", muted: muted, notify: notify,
                           interactionID: card, route: "/s/laptop/\(session)")
    }

    private func summary(_ items: LinkAttention.Item..., more: Int = 0) -> LinkAttention {
        LinkAttention(needsYou: items.count + more, more: more, items: items)
    }

    // MARK: decoding

    func testDecodesTheContractsStatusField() throws {
        let s = try status(#"""
        {"state":"connected","machine":"laptop","attention":{"rev":"9f2c41d0","needs_you":3,"more":2,
         "web_url":"https://hub.example.com/persona","items":[
          {"machine_id":"laptop","session_id":"s1","title":"Fix the login redirect","place":"Laptop · web › 2",
           "kind":"permission","detail":"Bash: npm test","muted":true,"notify":false,"interaction_id":"ix-9",
           "since":"2026-11-09T07:00:00.123456789Z","route":"/s/laptop/s1"}]}}
        """#)
        let a = try XCTUnwrap(s.attention)
        XCTAssertEqual(a.needsYou, 3)
        XCTAssertEqual(a.more, 2)
        XCTAssertEqual(a.webURL, "https://hub.example.com/persona")
        XCTAssertEqual(a.items, [LinkAttention.Item(machineID: "laptop", sessionID: "s1", title: "Fix the login redirect",
                                                    place: "Laptop · web › 2", kind: "permission", detail: "Bash: npm test",
                                                    muted: true, interactionID: "ix-9", route: "/s/laptop/s1")])
        XCTAssertEqual(a.items[0].id, "laptop/s1#needs")
    }

    func testAbsentFieldMeansNoSummaryAndAnEmptyOneIsNothingNeedsYou() throws {
        XCTAssertNil(try status(#"{"state":"connected"}"#).attention)
        let empty = try XCTUnwrap(try status(#"{"state":"connected","attention":{"rev":"r","needs_you":0,"more":0,"items":[]}}"#).attention)
        XCTAssertEqual(empty, LinkAttention(needsYou: 0, items: []))
        XCTAssertNil(LinkAttention.countText(empty.needsYou))
        // Only needs_you and each item's ids are required: the rest take their defaults.
        XCTAssertEqual(try status(#"{"state":"connected","attention":{"needs_you":0,"items":null}}"#).attention, empty)
        let bare = try status(#"{"state":"connected","attention":{"needs_you":1,"items":[{"machine_id":"laptop","session_id":"s1"}]}}"#)
        XCTAssertEqual(bare.attention?.items, [LinkAttention.Item(machineID: "laptop", sessionID: "s1")])
        XCTAssertEqual(bare.attention?.items.first?.notify, false, "an item that does not say it notifies does not")
    }

    func testASummaryThatDoesNotDecodeIsLeftOutNotTheStatusLine() throws {
        let s = try status(#"""
        {"state":"connected","machine":"laptop","attention":{"needs_you":"two","items":[{"session_id":"s1"}]}}
        """#)
        XCTAssertEqual(s.state, "connected")
        XCTAssertEqual(s.machine, "laptop")
        XCTAssertNil(s.attention)
    }

    func testATerminalDialogHasNoCardAndUnknownFieldsAreIgnored() throws {
        let s = try status(#"""
        {"state":"connected","attention":{"rev":"r","needs_you":1,"more":0,"items":[
          {"machine_id":"laptop","session_id":"s2","title":"t","place":"p","kind":"screen","detail":"","muted":false,
           "route":"/s/laptop/s2","future_field":1}]}}
        """#)
        let item = try XCTUnwrap(s.attention?.items.first)
        XCTAssertNil(item.interactionID)
        XCTAssertNil(s.attention?.webURL)
    }

    func testHiddenCountsTheRowsLeftOutAndWhatTheHubCapped() {
        let a = LinkAttention(needsYou: 23, more: 3, items: (1...20).map { item("s\($0)") })
        XCTAssertEqual(a.hidden(shown: NeedsYouSection.shown), 18)
        XCTAssertEqual(summary(item("a"), item("b")).hidden(shown: NeedsYouSection.shown), 0)
        XCTAssertEqual(LinkAttention.countText(23), "23")
        XCTAssertEqual(LinkAttention.countText(100), "99+")
    }

    // MARK: notifications

    func testTheFirstSummaryOfAConnectionIsABaseline() {
        var n = AttentionNotifications()
        XCTAssertEqual(n.apply(summary(item("a"), item("b"))), .init())
        let next = n.apply(summary(item("a"), item("b"), item("c")))
        XCTAssertEqual(next.post.map(\.id), ["laptop/c#needs"])
        XCTAssertEqual(next.withdraw, [])
        XCTAssertEqual(n.apply(summary(item("a"), item("b"), item("c"))), .init(), "the same summary again changes nothing")
    }

    func testOnlyAnItemThatNotifiesMakesANotification() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        // Muted, or a question the assistant is still deciding on: listed, no notification.
        XCTAssertEqual(n.apply(summary(item("m", notify: false, muted: true), item("d", notify: false))).post, [])
        // `notify` decides, not `muted`.
        XCTAssertEqual(n.apply(summary(item("m", notify: false, muted: true), item("d", notify: false), item("x", muted: true)))
            .post.map(\.id), ["laptop/x#needs"])
    }

    func testACardThatStartsToNotifyNotifiesOnceEvenWhenItWasListedBefore() {
        // The assistant hands you the question it was deciding on: same card, `notify` turns true.
        var n = AttentionNotifications()
        _ = n.apply(summary(item("d", notify: false), item("b")))
        let handed = n.apply(summary(item("d"), item("b")))
        XCTAssertEqual(handed.post.map(\.id), ["laptop/d#needs"], "d notifies now; b was notifying in the baseline")
        XCTAssertTrue(n.isCurrent(handed.post[0], connection: handed.connection))
        XCTAssertEqual(n.adopt([("laptop/d#needs", "ix-1")]), [], "it went out")
        XCTAssertEqual(n.apply(summary(item("d"), item("b"))).post, [], "the same summary again changes nothing")

        // Muted and unmuted again, or a baseline card muted and unmuted: it notified on this connection already.
        _ = n.apply(summary(item("d", notify: false, muted: true), item("b", notify: false, muted: true)))
        let unmuted = n.apply(summary(item("d"), item("b")))
        XCTAssertEqual(unmuted, .init(connection: unmuted.connection))
        XCTAssertEqual(n.shown, ["laptop/d#needs": "ix-1"])
    }

    func testACardWhoseNotificationIsShownDoesNotNotifyAgainWhicheverIsFoundFirst() {
        // At launch, the notification an earlier run left and the first summary arrive in either order.
        let delivered: [(id: String, card: String?)] = [("laptop/d#needs", "ix-1")]
        var adoptFirst = AttentionNotifications()
        XCTAssertEqual(adoptFirst.adopt(delivered), [])
        _ = adoptFirst.apply(summary(item("d", notify: false)))
        var summaryFirst = AttentionNotifications()
        _ = summaryFirst.apply(summary(item("d", notify: false)))
        XCTAssertEqual(summaryFirst.adopt(delivered), [])
        XCTAssertEqual(summaryFirst.known, adoptFirst.known)
        XCTAssertEqual(adoptFirst.apply(summary(item("d"))).post, [])
        XCTAssertEqual(summaryFirst.apply(summary(item("d"))).post, [])

        // With none shown, the same turn notifies.
        var none = AttentionNotifications()
        _ = none.apply(summary(item("d", notify: false)))
        XCTAssertEqual(none.apply(summary(item("d"))).post.map(\.id), ["laptop/d#needs"])
    }

    func testASessionBeyondACappedListKeepsItsNotificationAndDoesNotNotifyAgain() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        n.send(summary(item("old")))
        // Newer sessions push it beyond the cap: it may still wait, so its notification stays.
        let capped = n.send(summary(item("new1"), item("new2"), more: 1))
        XCTAssertEqual(capped.post.map(\.id), ["laptop/new1#needs", "laptop/new2#needs"])
        XCTAssertEqual(capped.withdraw, [])
        // Back in the list with the same card: no second notification.
        XCTAssertEqual(n.apply(summary(item("new1"), item("old"), more: 1)).post, [])
        // Once nothing is cut, what is missing is gone.
        XCTAssertEqual(n.apply(summary(item("new1"))).withdraw, ["laptop/new2#needs", "laptop/old#needs"])
    }

    func testACappedFirstSummaryKeepsANotificationFoundAtLaunchWhicheverArrivesFirst() {
        let capped = summary(item("new1"), item("new2"), more: 1)
        let delivered: [(id: String, card: String?)] = [("laptop/old#needs", "ix-1")]
        var adoptFirst = AttentionNotifications()
        XCTAssertEqual(adoptFirst.adopt(delivered), [])
        XCTAssertEqual(adoptFirst.apply(capped).withdraw, [])
        var summaryFirst = AttentionNotifications()
        _ = summaryFirst.apply(capped)
        XCTAssertEqual(summaryFirst.adopt(delivered), [], "old may wait beyond the cap")
        XCTAssertEqual(summaryFirst.shown, adoptFirst.shown)
        XCTAssertEqual(summaryFirst.known, adoptFirst.known)
        for var n in [adoptFirst, summaryFirst] {
            XCTAssertEqual(n.apply(summary(item("old"), item("new1"), more: 1)).post, [], "back in the list: no new card")
            XCTAssertEqual(n.apply(summary(item("new1"))).withdraw, ["laptop/old#needs"], "nothing cut: missing is gone")
        }
    }

    func testANewerCardOfTheSameSessionReplacesItsNotification() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        XCTAssertEqual(n.send(summary(item("a", card: nil))).post.map(\.interactionID), [nil])
        let replaced = n.send(summary(item("a", card: "ix-2")))
        XCTAssertEqual(replaced.post.map(\.interactionID), ["ix-2"])
        XCTAssertEqual(replaced.withdraw, ["laptop/a#needs"])
        XCTAssertEqual(replaced.withdrawNow, [], "the newer one replaces it; the older goes only if the newer does not go out")
        XCTAssertEqual(n.shown, ["laptop/a#needs": "ix-2"])
    }

    func testASessionThatLeftTheSummaryTakesItsNotificationBack() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        n.send(summary(item("a"), item("b")))
        let answered = n.apply(summary(item("b")))
        XCTAssertEqual(answered.withdraw, ["laptop/a#needs"])
        XCTAssertEqual(answered.post, [])
        XCTAssertEqual(n.apply(LinkAttention(needsYou: 0, items: [])).withdraw, ["laptop/b#needs"])
        XCTAssertEqual(n.shown, [:])
    }

    func testAReconnectStartsANewBaselineAndTakesBackWhatWasAnsweredMeanwhile() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        n.send(summary(item("a"), item("b")))
        XCTAssertEqual(n.apply(nil).withdraw, [], "the summary went away with the connection: nothing is taken back yet")
        let first = n.apply(summary(item("b"), item("c")))
        XCTAssertEqual(first.post, [], "c was already waiting when the link came back: no notification")
        XCTAssertEqual(first.withdraw, ["laptop/a#needs"])
    }

    func testNotificationsLeftFromAnEarlierRunGoWhenTheSummaryLacksThem() {
        var before = AttentionNotifications()
        XCTAssertEqual(before.adopt([("laptop/old#needs", "ix-1"), ("laptop/b#needs", "ix-1")]), [],
                       "no summary yet: nothing to judge them by")
        XCTAssertEqual(before.apply(summary(item("b"))).withdraw, ["laptop/old#needs"])
        XCTAssertEqual(before.shown, ["laptop/b#needs": "ix-1"])

        // Found only after the first summary arrived: the ones it lacks go at once.
        var after = AttentionNotifications()
        _ = after.apply(summary(item("b")))
        XCTAssertEqual(after.adopt([("laptop/old#needs", "ix-1"), ("laptop/b#needs", "ix-1")]), ["laptop/old#needs"])
        XCTAssertEqual(after.shown, ["laptop/b#needs": "ix-1"])
    }

    func testANotificationAboutACardTheSessionNoLongerWaitsOnGoesAtTheBaseline() {
        // After a restart: the session waits on a newer card than the one its notification shows.
        var restart = AttentionNotifications()
        XCTAssertEqual(restart.adopt([("laptop/a#needs", "ix-1"), ("laptop/b#needs", nil)]), ["laptop/b#needs"],
                       "a notification that does not say its card cannot be judged: it goes")
        XCTAssertEqual(restart.apply(summary(item("a", card: "ix-2"))), .init(post: [], withdraw: ["laptop/a#needs"]),
                       "the baseline posts nothing, but the stale one goes")

        // After a reconnect: the same, with the notification this run posted.
        var reconnect = AttentionNotifications()
        _ = reconnect.apply(summary())
        reconnect.send(summary(item("a")))
        _ = reconnect.apply(nil)
        XCTAssertEqual(reconnect.apply(summary(item("a", card: "ix-2"))).withdraw, ["laptop/a#needs"])
        XCTAssertEqual(reconnect.shown, [:])

        // A muted session's newer card posts nothing, so the older card's notification goes.
        var muted = AttentionNotifications()
        _ = muted.apply(summary())
        muted.send(summary(item("a")))
        XCTAssertEqual(muted.apply(summary(item("a", card: "ix-2", notify: false, muted: true))),
                       .init(post: [], withdraw: ["laptop/a#needs"]))
    }

    func testAPostThatWaitedIsDroppedOnceItsCardIsAnsweredOrReplaced() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        let waiting = n.apply(summary(item("a"), item("b")))
        _ = n.apply(summary(item("a", card: "ix-2")))
        XCTAssertEqual(waiting.post.map { n.isCurrent($0, connection: waiting.connection) }, [false, false],
                       "a has a newer card, b was answered")
        XCTAssertTrue(n.isCurrent(item("a", card: "ix-2"), connection: waiting.connection))
    }

    func testAPostThatWaitedIsDroppedOnceItNoLongerNotifies() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        let waiting = n.apply(summary(item("a")))
        XCTAssertTrue(n.isCurrent(waiting.post[0], connection: waiting.connection))
        _ = n.apply(summary(item("a", notify: false, muted: true)))
        XCTAssertFalse(n.isCurrent(waiting.post[0], connection: waiting.connection))
    }

    func testAPostThatWaitedIsDroppedOnceItsSessionIsMissingFromACappedList() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        let waiting = n.apply(summary(item("a")))
        let capped = n.apply(summary(item("b"), more: 1))
        XCTAssertEqual(capped.withdraw, [])
        XCTAssertFalse(n.isCurrent(waiting.post[0], connection: waiting.connection), "it is not known to wait any more")
        XCTAssertTrue(n.isCurrent(capped.post[0], connection: capped.connection))
    }

    func testAPostThatWaitedIsDroppedOnceTheConnectionWentAway() {
        var n = AttentionNotifications()
        _ = n.apply(summary())
        let waiting = n.apply(summary(item("a")))
        _ = n.apply(nil)
        let baseline = n.apply(summary(item("a")))
        XCTAssertEqual(baseline.post, [], "the same card in the new connection's first summary is its baseline")
        XCTAssertNotEqual(baseline.connection, waiting.connection)
        XCTAssertFalse(n.isCurrent(waiting.post[0], connection: waiting.connection))
    }

    func testOnlyAPostThatWentOutCountsAsShown() {
        // A newer card's post that is then dropped leaves nothing stale: the older notification it was to replace goes.
        var replaced = AttentionNotifications()
        _ = replaced.apply(summary())
        replaced.send(summary(item("a")))
        let replacing = replaced.apply(summary(item("a", card: "ix-2")))
        XCTAssertEqual(replacing.withdraw, ["laptop/a#needs"])
        XCTAssertEqual(replacing.withdrawNow, [])
        _ = replaced.apply(summary(item("a", card: "ix-2", notify: false)))
        XCTAssertFalse(replaced.isCurrent(replacing.post[0], connection: replacing.connection))
        XCTAssertEqual(replaced.shown, [:])

        // A post its connection dropped before it went out: on the next connection, a card listed without notifying in
        // the baseline notifies when it starts to.
        var dropped = AttentionNotifications()
        _ = dropped.apply(summary())
        let queued = dropped.apply(summary(item("a")))
        _ = dropped.apply(nil)
        _ = dropped.apply(summary(item("a", notify: false)))
        XCTAssertFalse(dropped.isCurrent(queued.post[0], connection: queued.connection))
        XCTAssertEqual(dropped.apply(summary(item("a"))).post.map(\.id), ["laptop/a#needs"])

        // A post that failed: nothing to take back later, and no second try on this connection.
        var failed = AttentionNotifications()
        _ = failed.apply(summary())
        _ = failed.apply(summary(item("a")))
        XCTAssertEqual(failed.apply(summary(item("a"), item("b", notify: false))).post, [])
        XCTAssertEqual(failed.apply(summary()).withdraw, [])

        // One that went out after the summary moved on goes as soon as it is shown; the newer card's post is due.
        var late = AttentionNotifications()
        _ = late.apply(summary())
        let slow = late.apply(summary(item("a")))
        let newer = late.apply(summary(item("a", card: "ix-2")))
        XCTAssertEqual(late.adopt([("laptop/a#needs", slow.post[0].card)]), ["laptop/a#needs"])
        XCTAssertEqual(late.shown, [:])
        XCTAssertTrue(late.isCurrent(newer.post[0], connection: newer.connection))

        // Posted twice while the first waited (the session left and came back): the second is not made once the first
        // is shown.
        var twice = AttentionNotifications()
        _ = twice.apply(summary())
        let first = twice.apply(summary(item("a")))
        _ = twice.apply(summary())
        let second = twice.apply(summary(item("a")))
        XCTAssertTrue(twice.isCurrent(first.post[0], connection: first.connection))
        XCTAssertEqual(twice.adopt([("laptop/a#needs", "ix-1")]), [])
        XCTAssertFalse(twice.isCurrent(second.post[0], connection: second.connection))
    }

    func testASummaryThatDoesNotDecodeKeepsTheBaseline() throws {
        let line = { (attention: String) in Data(#"{"state":"connected","attention":\#(attention)}"#.utf8) }
        let a = #"{"machine_id":"laptop","session_id":"a","interaction_id":"ix-1","notify":true,"route":"/s/laptop/a"}"#
        let b = #"{"machine_id":"laptop","session_id":"b","interaction_id":"ix-1","notify":true,"route":"/s/laptop/b"}"#
        var n = AttentionNotifications()
        var last: LinkAttention?
        func receive(_ data: Data) throws -> AttentionNotifications.Changes {
            last = try LinkStatus.decode(data, previous: last).attention
            return n.apply(last)
        }
        _ = try receive(line(#"{"needs_you":1,"items":[\#(a)]}"#))
        XCTAssertEqual(try receive(line(#"{"needs_you":"two","items":[]}"#)), .init(), "a bad summary changes nothing")
        XCTAssertEqual(last?.items.map(\.sessionID), ["a"], "the menu keeps the last good one")
        XCTAssertEqual(try receive(line(#"{"needs_you":2,"items":[\#(a),\#(b)]}"#)).post.map(\.sessionID), ["b"])
        // A line without the field (an older hub, or a link that reconnects) has no summary.
        XCTAssertNil(try LinkStatus.decode(Data(#"{"state":"connected"}"#.utf8), previous: last).attention)
        XCTAssertNil(try LinkStatus.decode(Data(#"{"state":"connected","attention":null}"#.utf8), previous: last).attention)
    }

    func testTheNoteNamesTheSessionAndCarriesItsRoute() {
        var i = item("a")
        i.title = "Fix the login redirect"
        i.place = "Laptop · web › 2"
        let note = AttentionNote(i, webURL: "https://hub.example.com/persona")
        XCTAssertEqual(note.id, "laptop/a#needs")
        XCTAssertEqual(note.title, "Fix the login redirect")
        XCTAssertEqual(note.subtitle, "Laptop · web › 2")
        XCTAssertEqual(note.body, i.line)
        XCTAssertEqual(note.userInfo, ["route": "/s/laptop/a", "card": "ix-1", "web_url": "https://hub.example.com/persona"])
        // Without a title the place takes its line, and no subtitle repeats it.
        i.title = ""
        let untitled = AttentionNote(i, webURL: nil)
        XCTAssertEqual(untitled.title, "Laptop · web › 2")
        XCTAssertEqual(untitled.subtitle, "")
        XCTAssertEqual(untitled.userInfo, ["route": "/s/laptop/a", "card": "ix-1"])
    }

    // MARK: the web app's address

    func testWebURLJoinsASubPathAndTheRouteWithOneSlash() {
        let route = "/s/laptop/abc%20def"
        for base in ["https://hub.example.com/persona", "https://hub.example.com/persona/", "https://hub.example.com//persona//"] {
            XCTAssertEqual(AttentionURL.web(base: base, route: route)?.absoluteString,
                           "https://hub.example.com/persona/s/laptop/abc%20def", base)
        }
        XCTAssertEqual(AttentionURL.web(base: "https://hub.example.com/persona", route: "s/laptop/abc")?.absoluteString,
                       "https://hub.example.com/persona/s/laptop/abc")
        XCTAssertEqual(AttentionURL.web(base: "https://hub.example.com", route: "/s/laptop/abc")?.absoluteString,
                       "https://hub.example.com/s/laptop/abc")
    }

    func testWebURLKeepsThePortAndIPv6AndDropsQueryAndFragment() {
        XCTAssertEqual(AttentionURL.web(base: "https://[2001:db8:1::10]:18662/", route: "/s/m/s")?.absoluteString,
                       "https://[2001:db8:1::10]:18662/s/m/s")
        XCTAssertEqual(AttentionURL.web(base: "http://192.0.2.10:18660/p?x=1#top", route: "/s/m/s")?.absoluteString,
                       "http://192.0.2.10:18660/p/s/m/s")
    }

    func testTheWebAppsRootEndsInASlashUnderASubPath() {
        XCTAssertEqual(AttentionURL.web(base: "https://hub.example.com/persona", route: "/")?.absoluteString,
                       "https://hub.example.com/persona/")
        XCTAssertEqual(AttentionURL.web(base: "https://hub.example.com", route: "")?.absoluteString, "https://hub.example.com/")
    }

    func testOnlyHTTPAddressesWithAHostOpen() {
        for base in [nil, "", "hub.example.com", "file:///etc/hosts", "javascript:alert(1)", "https://", "wss://hub.example.com"] {
            XCTAssertNil(AttentionURL.web(base: base, route: "/s/m/s"), String(describing: base))
        }
    }

    func testTheSettingIsHTTPSWithoutASchemeAndRejectsOtherSchemes() {
        XCTAssertEqual(AttentionURL.setting("  "), "")
        XCTAssertEqual(AttentionURL.setting(" hub.example.com/persona "), "https://hub.example.com/persona")
        XCTAssertEqual(AttentionURL.setting("http://192.0.2.10:18660"), "http://192.0.2.10:18660")
        XCTAssertNil(AttentionURL.setting("file:///tmp"))
        XCTAssertNil(AttentionURL.setting("https://"))
    }

    func testTheSettingWinsOverTheHubsWebURL() {
        let hub = "https://hub.example.com/persona"
        XCTAssertEqual(AttentionURL.base(setting: "https://web.example.com", webURL: hub), "https://web.example.com")
        XCTAssertEqual(AttentionURL.base(setting: " ", webURL: hub), hub)
        XCTAssertNil(AttentionURL.base(setting: "", webURL: ""))
    }

    func testANotificationOpensAtTheWebURLItCarriedWhenThereIsNoSummaryNow() {
        let m = LinkModel()
        m.show(LinkStatus(state: "connecting"))
        XCTAssertNil(m.webBase())
        XCTAssertEqual(m.webBase(hubWebURL: "https://hub.example.com/persona"), "https://hub.example.com/persona")
        m.show(LinkStatus(state: "connecting"), webAddress: "https://web.example.com")
        XCTAssertEqual(m.webBase(hubWebURL: "https://hub.example.com/persona"), "https://web.example.com")
        // A summary whose web_url is blank does not hide the one the notification carried.
        var blank = LinkStatus(state: "connected")
        blank.attention = LinkAttention(needsYou: 0, webURL: " ", items: [])
        m.show(blank)
        XCTAssertEqual(m.webBase(hubWebURL: "https://hub.example.com/persona"), "https://hub.example.com/persona")
    }

    // MARK: opening a row or a notification

    private func model(webAddress: String = "", webURL: String? = nil) -> (LinkModel, opened: () -> [String], asked: () -> Int) {
        let m = LinkModel()
        var status = LinkStatus(state: "connected", mode: "passive")
        if let webURL { status.attention = LinkAttention(needsYou: 0, webURL: webURL, items: []) }
        m.show(status, webAddress: webAddress)
        let suite = "PersonaLinkTests.\(UUID().uuidString)"
        m.defaults = UserDefaults(suiteName: suite)!
        addTeardownBlock { UserDefaults().removePersistentDomain(forName: suite) }
        var opened: [String] = [], asked = 0
        m.openURL = { opened.append($0.absoluteString) }
        m.askForWebAddress = { asked += 1 }
        return (m, { opened }, { asked })
    }

    func testANotificationWithoutAWebAddressAsksForOneAndOpensOnceItIsSaved() {
        let (m, opened, asked) = model()
        m.open(route: "/s/laptop/a", webURL: nil)
        XCTAssertEqual(opened(), [])
        XCTAssertEqual(asked(), 1)
        m.setWebAddress("https://web.example.com/persona")
        XCTAssertEqual(opened(), ["https://web.example.com/persona/s/laptop/a"])
        m.setWebAddress("https://web.example.com")
        XCTAssertEqual(opened().count, 1, "the route opened once")
    }

    func testAClickThatOpensDropsTheRouteWaitingForAnAddress() {
        let (m, opened, _) = model()
        m.open(route: "/s/laptop/a")
        // The hub's web address arrives while the Web Address window is open; the row is clicked again.
        var status = LinkStatus(state: "connected", mode: "passive")
        status.attention = LinkAttention(needsYou: 0, webURL: "https://hub.example.com", items: [])
        m.show(status)
        m.open(route: "/s/laptop/a")
        XCTAssertEqual(opened(), ["https://hub.example.com/s/laptop/a"])
        m.setWebAddress("https://web.example.com")
        XCTAssertEqual(opened().count, 1, "saving the address does not open it again")
    }

    func testClosingTheWebAddressWindowDropsTheWaitingRoute() {
        let (m, opened, _) = model()
        m.open(route: "/s/laptop/a")
        m.forgetPendingRoute()
        m.setWebAddress("https://web.example.com")
        XCTAssertEqual(opened(), [])
    }

    func testANotificationOpensAtTheHubsAddressAndTheSettingWins() {
        let (hub, hubOpened, hubAsked) = model(webURL: "https://hub.example.com/persona")
        hub.open(route: "/s/laptop/a")
        XCTAssertEqual(hubOpened(), ["https://hub.example.com/persona/s/laptop/a"])
        XCTAssertEqual(hubAsked(), 0)

        let (set, setOpened, _) = model(webAddress: "https://web.example.com", webURL: "https://hub.example.com/persona")
        set.open(route: "/s/laptop/a", webURL: "https://old.example.com")
        XCTAssertEqual(setOpened(), ["https://web.example.com/s/laptop/a"])
    }
}

private extension AttentionNotifications {
    /// `apply`, with every post due going out at once, as `LinkNotifier` does when nothing makes it wait.
    @discardableResult
    mutating func send(_ attention: LinkAttention?) -> Changes {
        let changes = apply(attention)
        for item in changes.post where isCurrent(item, connection: changes.connection) {
            XCTAssertEqual(adopt([(item.id, item.card)]), [])
        }
        return changes
    }
}
