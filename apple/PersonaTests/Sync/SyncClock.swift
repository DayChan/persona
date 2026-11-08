import Foundation
import Synchronization

/// The shared sync replay's clock (`AppModel.clock`): time moves only when a case's `wait` moves it (`advance`), so a
/// wait of minutes takes none. Sleepers whose deadline it passes wake in deadline order.
nonisolated final class SyncClock: Clock {
    struct Instant: InstantProtocol {
        var offset: Duration
        func advanced(by d: Duration) -> Instant { Instant(offset: offset + d) }
        func duration(to other: Instant) -> Duration { other.offset - offset }
        static func < (a: Instant, b: Instant) -> Bool { a.offset < b.offset }
    }

    private struct Sleeper {
        let id: UUID
        let deadline: Instant
        let wake: CheckedContinuation<Void, Error>
    }

    private struct State {
        var now = Instant(offset: .zero)
        var sleepers: [Sleeper] = []
    }

    private let state = Mutex(State())

    var now: Instant { state.withLock { $0.now } }
    var minimumResolution: Duration { .zero }

    func sleep(until deadline: Instant, tolerance: Duration?) async throws {
        let id = UUID()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (wake: CheckedContinuation<Void, Error>) in
                if Task.isCancelled { return wake.resume(throwing: CancellationError()) }
                let due = state.withLock { s in
                    if deadline <= s.now { return true }
                    s.sleepers.append(Sleeper(id: id, deadline: deadline, wake: wake))
                    return false
                }
                if due { wake.resume() }
            }
        } onCancel: {
            let wake = state.withLock { s in s.sleepers.firstIndex { $0.id == id }.map { s.sleepers.remove(at: $0).wake } }
            wake?.resume(throwing: CancellationError())
        }
    }

    /// Moves the time on by `d` and wakes whoever is due.
    func advance(by d: Duration) {
        let due = state.withLock { s in
            s.now = s.now.advanced(by: d)
            let due = s.sleepers.filter { $0.deadline <= s.now }.sorted { $0.deadline < $1.deadline }
            s.sleepers.removeAll { $0.deadline <= s.now }
            return due
        }
        for s in due { s.wake.resume() }
    }
}
