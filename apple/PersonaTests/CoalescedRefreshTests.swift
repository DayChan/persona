import Foundation
import Testing
@testable import Persona

/// A bounded scheduler drain, not a wall-clock delay or an unbounded polling loop.
@MainActor
func waitForRefreshTest(_ condition: @MainActor () -> Bool) async throws {
    for _ in 0..<10_000 {
        if condition() { return }
        await Task.yield()
    }
    try #require(condition(), "The refresh did not reach its expected checkpoint")
}

@MainActor
private final class RefreshOperationGate {
    private(set) var starts = 0
    private var pending: [CheckedContinuation<Void, Never>] = []
    func read() async {
        starts += 1
        await withCheckedContinuation { pending.append($0) }
    }
    func finish() throws {
        let next = try #require(pending.first)
        pending.removeFirst()
        next.resume()
    }
    func close() {
        let remaining = pending
        pending = []
        for next in remaining { next.resume() }
    }
}

@MainActor
struct CoalescedRefreshTests {
    @Test func continuousInvalidationsShareReadsAndTheFinalDirtyReadLands() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        var firstFinished = false
        let first = reads.invalidate { await gate.read() }
        let observer = Task { await first.value; firstFinished = true }
        defer { observer.cancel() }
        try await waitForRefreshTest { gate.starts == 1 }
        for _ in 0..<50 { reads.invalidate { await gate.read() } }
        #expect(gate.starts == 1)
        try gate.finish()
        try await waitForRefreshTest { gate.starts == 2 }
        var final = first
        for _ in 0..<50 { final = reads.invalidate { await gate.read() } }
        #expect(gate.starts == 2)
        try gate.finish()
        try await waitForRefreshTest { firstFinished && gate.starts == 3 }
        // This caller ended despite more events arriving during its follow-up.
        #expect(firstFinished)
        try gate.finish()
        await final.value
        #expect(gate.starts == 3)
    }

    @Test func manualReadJoinsInFlightAndTheNextReadAddsNoDelay() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        let automatic = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 1 }
        var manualEntered = false
        let manual = Task {
            manualEntered = true
            await reads.refresh { await gate.read() }
        }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        #expect(gate.starts == 1)
        try gate.finish()
        await automatic.value
        await manual.value
        #expect(gate.starts == 1)
        let nextAutomatic = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 2 }
        try gate.finish()
        await nextAutomatic.value
        #expect(gate.starts == 2)
    }

    @Test func manualJoinDoesNotReplaceThePendingDirtyOperation() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        let first = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 1 }
        var reconciled = 0
        let dirty = reads.invalidate { await gate.read(); reconciled += 1 }
        var entered = false
        var weakerLoads = 0
        let manual = Task {
            entered = true
            await reads.refresh { weakerLoads += 1 }
        }
        defer { manual.cancel() }
        try await waitForRefreshTest { entered }
        try gate.finish()
        try await waitForRefreshTest { gate.starts == 2 }
        try gate.finish()
        await first.value
        await dirty.value
        await manual.value
        #expect(reconciled == 1 && weakerLoads == 0)
    }

    @Test func writeConfirmationCannotJoinAPreWriteRead() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        let beforeWrite = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 1 }
        var confirmed = false
        var confirmationEntered = false
        let confirmation = Task {
            confirmationEntered = true
            await reads.refresh(afterCurrent: true) { await gate.read() }
            confirmed = true
        }
        defer { confirmation.cancel() }
        try await waitForRefreshTest { confirmationEntered }
        // An automatic invalidation arriving after the write shares the queued confirmation.
        reads.invalidate { await gate.read() }
        #expect(!confirmed && gate.starts == 1)
        try gate.finish()
        try await waitForRefreshTest { gate.starts == 2 }
        #expect(!confirmed)
        try gate.finish()
        await confirmation.value
        await beforeWrite.value
        #expect(confirmed && gate.starts == 2)
    }

    @Test func cancellingOneWaiterDoesNotCancelTheSharedRead() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        let automatic = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 1 }
        var manualEntered = false
        let manual = Task {
            manualEntered = true
            await reads.refresh { await gate.read() }
        }
        try await waitForRefreshTest { manualEntered }
        manual.cancel()
        await manual.value
        #expect(gate.starts == 1)
        try gate.finish()
        await automatic.value
        #expect(gate.starts == 1)
    }

    @Test func cancellingAnInvalidationWaitDoesNotDropItsDirtyRead() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        let first = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 1 }
        let cancelled = reads.invalidate { await gate.read() }
        cancelled.cancel()
        await cancelled.value
        #expect(gate.starts == 1)
        try gate.finish()
        try await waitForRefreshTest { gate.starts == 2 }
        var manualEntered = false
        let manual = Task {
            manualEntered = true
            await reads.refresh { await gate.read() }
        }
        defer { manual.cancel() }
        try await waitForRefreshTest { manualEntered }
        try gate.finish()
        await manual.value
        await first.value
        #expect(gate.starts == 2)
    }

    @Test func aCompletedWriteStillConfirmsWhenItsCallerIsAlreadyCancelled() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        let caller = Task { await reads.refresh(afterCurrent: true) { await gate.read() } }
        caller.cancel()
        await caller.value
        try await waitForRefreshTest { gate.starts == 1 }
        try gate.finish()
    }

    @Test func resetBeforeAQueuedOperationStartsDoesNotReadTheOldHub() async {
        let reads = CoalescedRefresh()
        var oldReads = 0
        var newReads = 0
        let old = reads.invalidate { oldReads += 1 }
        reads.reset()
        let new = reads.invalidate { newReads += 1 }
        await old.value
        await new.value
        #expect(oldReads == 0 && newReads == 1)
        reads.reset()
    }

    @Test func resetEndsWaitsWithoutWaitingForACancellationIgnoringOperation() async throws {
        let gate = RefreshOperationGate()
        let reads = CoalescedRefresh()
        defer { reads.reset(); gate.close() }
        let old = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 1 }
        reads.reset()
        await old.value
        let new = reads.invalidate { await gate.read() }
        try await waitForRefreshTest { gate.starts == 2 }
        try gate.finish() // Old operation finishes while the new one is still pending.
        var joinedEntered = false
        let joined = Task {
            joinedEntered = true
            await reads.refresh { await gate.read() }
        }
        defer { joined.cancel() }
        try await waitForRefreshTest { joinedEntered }
        #expect(gate.starts == 2)
        try gate.finish()
        await new.value
        await joined.value
        #expect(gate.starts == 2)
    }
}
