import Foundation

/// A new conversation's first message (#1349): one op id for it, the same on every try, so the hub starts the
/// conversation once however often the request arrives. On a hub with `assistant_start_ops`, a try that died on the way
/// (no network, a timeout, the hub restarting, `202 pending`) goes again with that id for up to `OpRetry.limit`; an older
/// hub gets no id and no such retries, since a repeat there would start a second conversation. Either way the assistant
/// restarting (`assistant_offline`: nothing reached it) is waited out a few times without a word
/// (docs/assistant/onboarding.md 4.6).
enum AssistantStart {
    static let feature = "assistant_start_ops"

    /// `start` sends the request with the op id it is given (nil: none). `restartWait` sleeps before the n-th try after
    /// `assistant_offline`, `gapWait` between tries through a gap (tests pass ones that don't sleep).
    static func run(receipts: Bool, op: UUID = UUID(),
                    restartWait: (Int) async throws -> Void = { try await Task.sleep(for: .seconds(2 * $0)) },
                    gapWait: @escaping (TimeInterval, _ keepTrying: @escaping () -> Bool) async -> Void = { await OpRetry.sleep($0, while: $1) },
                    _ start: (UUID?) async throws -> Session) async throws -> Session {
        var restarts = 0
        while true {
            do {
                guard receipts else { return try await start(nil) }
                return try await OpRetry.untilDelivered(wait: gapWait) { try await start(op) }
            } catch where CodedFailure.passesBySelf(error) && restarts < 3 {
                restarts += 1
                try await restartWait(restarts)
            }
        }
    }
}
