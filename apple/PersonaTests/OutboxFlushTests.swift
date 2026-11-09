import Testing
@testable import Persona

/// Which chats' waiting messages go out, by group (#4704): a group's messages go once its own link is up, whatever the
/// hub-level group's link is doing, and a group that is sending already or out of reach keeps no other group back.
struct OutboxFlushTests {
    private let home = SessionKey(machineID: "mac", sessionID: "a")
    private let homeB = SessionKey(machineID: "mac", sessionID: "b")
    private let work = SessionKey(group: "work", machineID: "box", sessionID: "c")

    @Test func aGroupSendsWhileTheHubLevelOneIsDown() {
        let out = OutboxFlush.batches([home, work], ready: { $0 == "work" }, busy: [])
        #expect(out == ["work": [work]])
    }

    @Test func everyGroupThatIsUpGetsItsOwnBatch() {
        let out = OutboxFlush.batches([work, homeB, home], ready: { _ in true }, busy: [])
        #expect(out == ["": [home, homeB], "work": [work]])
    }

    @Test func aGroupSendingAlreadyHoldsBackOnlyItself() {
        let out = OutboxFlush.batches([home, work], ready: { _ in true }, busy: [""])
        #expect(out == ["work": [work]])
    }

    @Test func nothingGoesWhileNoGroupIsUp() {
        #expect(OutboxFlush.batches([home, work], ready: { _ in false }, busy: []).isEmpty)
    }
}

/// A message that keeps coming back waiting while its group's link is up gives up after a few tries (#1923): a chat
/// deleted on another device left it waiting, sent again every 10 seconds, forever.
struct OutboxWaitTests {
    @Test func aWaitOnAnUpLinkCountsTowardTheLimit() {
        #expect(OutboxWait.next(.waiting, stalls: 0, linkUp: true) == .init(outcome: .waiting, stalls: 1))
    }

    @Test func theLastAllowedWaitFailsTheMessage() {
        let next = OutboxWait.next(.waiting, stalls: OutboxWait.connectedTries - 1, linkUp: true)
        #expect(next.outcome == .failed)
        #expect(next.gaveUp)
        #expect(next.stalls == nil)
    }

    /// Offline is what waiting is for: no limit, and the run starts again once the link is back.
    @Test func aWaitWhileTheLinkIsDownNeverFailsAndResetsTheRun() {
        let next = OutboxWait.next(.waiting, stalls: OutboxWait.connectedTries - 1, linkUp: false)
        #expect(next == .init(outcome: .waiting, stalls: nil))
    }

    @Test func aSendEndsTheRun() {
        #expect(OutboxWait.next(.sent, stalls: 3, linkUp: true) == .init(outcome: .sent, stalls: nil))
    }
}
