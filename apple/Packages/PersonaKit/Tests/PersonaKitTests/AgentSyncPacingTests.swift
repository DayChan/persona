import Foundation
import Testing
@testable import PersonaKit

/// Chats not on screen are pulled at most once a minute while their agent works, and at once when its turn ends
/// (`AgentSyncPacing.heldUntil`): each pull decodes and rewrites the chat's whole disk cache.
struct AgentSyncPacingTests {
    let t0 = Date(timeIntervalSinceReferenceDate: 1000)

    @Test func aWorkingAgentWaitsForItsTurn() {
        #expect(AgentSyncPacing.heldUntil(working: true, lastPull: nil, now: t0) == nil) // never pulled: now
        let held = AgentSyncPacing.heldUntil(working: true, lastPull: t0, now: t0.addingTimeInterval(10))
        #expect(held == t0.addingTimeInterval(AgentSyncPacing.workingGap))
        #expect(AgentSyncPacing.heldUntil(working: true, lastPull: t0, now: t0.addingTimeInterval(AgentSyncPacing.workingGap)) == nil)
    }

    @Test func aTurnEndingIsPulledAtOnce() {
        #expect(AgentSyncPacing.heldUntil(working: false, lastPull: t0, now: t0.addingTimeInterval(1)) == nil)
    }
}

/// A chat on screen whose subscription stopped bringing items asks for them (`AgentSyncPacing.catchUpAt`): once its
/// session is newer than the update recorded on arrival and nothing came for `staleQuiet`; not while it is current,
/// not twice for the same `updated_at`, not more often than `stalePullEvery`.
struct ChatCatchUpPacingTests {
    /// The session update recorded when items last came for the chat.
    let line = Date(timeIntervalSinceReferenceDate: 1000)
    /// The session's `updated_at`: the transcript grew since.
    var written: Date { line.addingTimeInterval(30) }

    func at(_ updated: Date?, arrived: Date?, arrival: Date?, pull: Date? = nil, pulledFor: Date? = nil) -> Date? {
        AgentSyncPacing.catchUpAt(updatedAt: updated, arrivedUpdatedAt: arrived, lastArrival: arrival, lastPull: pull, pulledFor: pulledFor)
    }

    @Test func aChatBehindItsSessionAsksOnceTheStreamIsQuiet() {
        let quietEnds = line.addingTimeInterval(AgentSyncPacing.staleQuiet)
        #expect(at(written, arrived: line, arrival: line) == quietEnds)
        // items came a moment ago (an empty batch, a push): it waits for the quiet from then
        let pushed = written.addingTimeInterval(1)
        #expect(at(written, arrived: line, arrival: pushed) == pushed.addingTimeInterval(AgentSyncPacing.staleQuiet))
        // no session update was known at arrival: ask conservatively
        #expect(at(written, arrived: nil, arrival: line) == quietEnds)
    }

    @Test func aBurstWriteWithALostPushAsksAfterQuiet() {
        let burst = line.addingTimeInterval(0.3)
        let quietEnds = line.addingTimeInterval(AgentSyncPacing.staleQuiet)
        #expect(at(burst, arrived: line, arrival: line) == quietEnds)
        #expect(at(burst, arrived: burst, arrival: line) == nil)
    }

    @Test func aCurrentChatDoesNotAsk() {
        // the list still describes the update known when items arrived
        #expect(at(line, arrived: line, arrival: line) == nil)
        #expect(at(line.addingTimeInterval(-60), arrived: line, arrival: line) == nil)
        // no word from the list
        #expect(at(nil, arrived: line, arrival: line) == nil)
        // nothing came for the chat yet (its backlog is on its way): not quiet, just early
        #expect(at(written, arrived: line, arrival: nil) == nil)
    }

    @Test func itAsksOncePerWriteAndNotTooOften() {
        // asked about this write already: it added no line
        #expect(at(written, arrived: line, arrival: line, pulledFor: written) == nil)
        // a newer write is asked about, but not within `stalePullEvery` of the last ask
        let asked = line.addingTimeInterval(20)
        #expect(at(written, arrived: line, arrival: line, pull: asked, pulledFor: line.addingTimeInterval(10))
                == asked.addingTimeInterval(AgentSyncPacing.stalePullEvery))
    }
}
