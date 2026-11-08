import Foundation
import Testing
@testable import Persona

/// Picking the way to a hub that has tunnels (docs/operations/network.md「经 SSH 隧道连 hub」, "Connecting to hub through
/// a local SOCKS5 proxy"): the same steps for the SSH tunnel, the local proxy and the in-app tunnel. `HubWay.next` is
/// pure, so each test walks through the steps by hand, telling it what each one came to.
struct HubWayTests {
    private let tunnel = HubWay.Option(always: false, recent: false)

    @Test func theDirectWayFirstWhenItAnswers() {
        var seen = HubWay.Seen()
        #expect(HubWay.next([tunnel], seen) == .askDirect(warming: []))
        seen.direct = true
        #expect(HubWay.next([tunnel], seen) == .direct)
    }

    @Test func theTunnelWhenTheDirectWayDoesNot() {
        var seen = HubWay.Seen(direct: false)
        #expect(HubWay.next([tunnel], seen) == .open(0))
        seen.opened[0] = true
        // Through it only because the hub did not answer: the direct way is watched.
        #expect(HubWay.next([tunnel], seen) == .through(0, watchDirect: true))
        seen.opened[0] = false
        #expect(HubWay.next([tunnel], seen) == .none)
    }

    @Test func alwaysUseGoesThroughTheTunnelWithoutAskingFirst() {
        let always = HubWay.Option(always: true, recent: false)
        var seen = HubWay.Seen()
        #expect(HubWay.next([always], seen) == .open(0))
        seen.opened[0] = true
        #expect(HubWay.next([always], seen) == .through(0, watchDirect: false))
        // A tunnel that does not come up: the direct way after all.
        seen.opened[0] = false
        #expect(HubWay.next([always], seen) == .askDirect(warming: []))
        seen.direct = true
        #expect(HubWay.next([always], seen) == .direct)
    }

    @Test func theDirectWayIsTakenAgainOnceItAnswers() {
        // Through the tunnel because the hub did not answer; every `recheck` the direct way is asked again.
        #expect(HubWay.recheck == .seconds(120))
        let used = HubWay.Option(always: false, recent: true)
        #expect(HubWay.next([used], HubWay.Seen(opened: [0: true], direct: false)) == .through(0, watchDirect: true))
        // It answers: the socket picks its way again, the tunnel starting meanwhile as it was the last way...
        var seen = HubWay.Seen()
        #expect(HubWay.next([used], seen) == .askDirect(warming: [0]))
        seen.opened[0] = true
        seen.direct = true
        // ...and straight to the hub it is; the tunnel closes.
        #expect(HubWay.next([used], seen) == .direct)
    }

    @Test func eachWayInTurn() {
        // Three tunnels, none Always: in order once the hub does not answer, except that the one used last time comes
        // first (it was started while the hub was asked).
        let ways = [tunnel, HubWay.Option(always: false, recent: true), tunnel]
        var seen = HubWay.Seen()
        #expect(HubWay.next(ways, seen) == .askDirect(warming: [1]))
        seen.direct = false
        seen.opened[1] = false
        #expect(HubWay.next(ways, seen) == .open(0))
        seen.opened[0] = false
        #expect(HubWay.next(ways, seen) == .open(2))
        seen.opened[2] = true
        #expect(HubWay.next(ways, seen) == .through(2, watchDirect: true))
        // An Always way that comes up wins over everything, the others not even tried.
        let always = [tunnel, HubWay.Option(always: true, recent: false)]
        #expect(HubWay.next(always, HubWay.Seen()) == .open(1))
        #expect(HubWay.next(always, HubWay.Seen(opened: [1: true])) == .through(1, watchDirect: false))
    }
}
