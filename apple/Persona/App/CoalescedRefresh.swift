import Foundation

/// One shared read at a time, with one latest dirty follow-up and finite per-request waits.
/// No cooldown: unclassified todo/session hints may carry critical state. The owner fences publication
/// when its hub or local mutation revision changes.
@MainActor
final class CoalescedRefresh {
    private struct Pending {
        var ticket: UInt64
        var operation: @MainActor () async -> Void
    }

    private struct Waiter {
        var ticket: UInt64
        /// An automatic caller also waits for one follow-up covering invalidations seen during its read.
        /// Later events cannot extend that follow-up indefinitely.
        var followsDirty: Bool
        var continuation: AsyncStream<Void>.Continuation
    }

    private var epoch: UInt64 = 0
    private var nextTicket: UInt64 = 0
    private var pending: Pending?
    private var active: (ticket: UInt64, task: Task<Void, Never>)?
    private var waiters: [UUID: Waiter] = [:]

    /// Invalidations during a read keep just one latest pending read. Cancelling the returned wait
    /// does not cancel the shared read or drop its dirty follow-up.
    @discardableResult
    func invalidate(operation: @escaping @MainActor () async -> Void) -> Task<Void, Never> {
        let stream = enqueue(manual: false, afterCurrent: false, operation: operation)
        return Task { for await _ in stream {} }
    }

    /// Manual reads may join an in-flight read. A write confirmation must use `afterCurrent`, so a
    /// GET started before the write can never stand in for its confirmation. Neither adds a delay.
    func refresh(afterCurrent: Bool = false, operation: @escaping @MainActor () async -> Void) async {
        // A completed write still needs confirmation even if its UI caller stopped waiting.
        guard afterCurrent || !Task.isCancelled else { return }
        let stream = enqueue(manual: true, afterCurrent: afterCurrent, operation: operation)
        for await _ in stream {}
    }

    /// Ends all old waits immediately, even if the old transport ignores cancellation. A new generation
    /// starts independently; the owner must reject the old operation's eventual publication.
    func reset() {
        epoch &+= 1
        active?.task.cancel()
        active = nil
        pending = nil
        let old = waiters.values
        waiters = [:]
        for waiter in old { waiter.continuation.finish() }
    }

    private func enqueue(manual: Bool, afterCurrent: Bool,
                         operation: @escaping @MainActor () async -> Void) -> AsyncStream<Void> {
        let ticket: UInt64
        if manual, !afterCurrent, let pending {
            // Joining a queued dirty read must not replace its operation with a weaker load-only read.
            ticket = pending.ticket
        } else if manual, !afterCurrent, let active {
            ticket = active.ticket
        } else {
            nextTicket &+= 1
            ticket = nextTicket
            pending = Pending(ticket: ticket, operation: operation)
        }
        let id = UUID()
        let pair = AsyncStream<Void>.makeStream()
        waiters[id] = Waiter(ticket: ticket, followsDirty: !manual, continuation: pair.continuation)
        pair.continuation.onTermination = { [weak self] _ in
            Task { @MainActor in _ = self?.waiters.removeValue(forKey: id) }
        }
        schedule()
        return pair.stream
    }

    private func schedule() {
        guard active == nil, let pending else { return }
        self.pending = nil
        let requestedEpoch = epoch
        let task = Task { [self] in
            guard epoch == requestedEpoch, !Task.isCancelled else { return }
            await pending.operation()
            guard epoch == requestedEpoch else { return }
            active = nil
            finishWaiters(through: pending.ticket)
            schedule()
        }
        active = (pending.ticket, task)
    }

    private func finishWaiters(through ticket: UInt64) {
        for (id, var waiter) in waiters where waiter.ticket <= ticket {
            if waiter.followsDirty, let pending {
                waiter.ticket = pending.ticket
                waiter.followsDirty = false
                waiters[id] = waiter
            } else {
                waiters.removeValue(forKey: id)
                waiter.continuation.finish()
            }
        }
    }
}
