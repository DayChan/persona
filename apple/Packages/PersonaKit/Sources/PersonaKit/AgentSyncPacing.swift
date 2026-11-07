import Foundation

/// How often a working agent's chat is pulled in the background (`AgentSync`, docs/apple/ios.md「后台同步」): at most once
/// every `workingGap`. Its transcript grows every second or two, and each pull is a request, a decode of the chat's
/// whole disk cache and a write of it again. The moment its turn ends or it needs the user (when it is likely to be
/// opened) it is pulled at once. Opening it brings the rest over its own subscription.
public enum AgentSyncPacing {
    public static let workingGap: TimeInterval = 60

    /// When a chat with news is pulled: now (nil), or once `workingGap` has passed since its last pull while its
    /// agent is still working.
    public static func heldUntil(working: Bool, lastPull: Date?, now: Date) -> Date? {
        guard working, let lastPull, now.timeIntervalSince(lastPull) < workingGap else { return nil }
        return lastPull.addingTimeInterval(workingGap)
    }

    /// A chat on screen follows its session over its own subscription. When that stream stops (a node whose transcript
    /// watch ended, a lost push), nothing else brings the new lines until the chat is opened again. The session list
    /// keeps coming and says when the transcript last grew (`updated_at`): when that is newer than the session update
    /// recorded on the chat's last arrival and no items came for `staleQuiet`, the chat asks for what came after it, at most once every
    /// `stalePullEvery`, and not twice for the same `updated_at` (a write that added no line). The web's rule and
    /// numbers (`web/src/lib/stale-page.ts`).
    public static let staleQuiet: TimeInterval = 4
    public static let stalePullEvery: TimeInterval = 10

    /// When a chat on screen asks for what its subscription may have lost: nil when it is not behind (or already asked
    /// about this `updatedAt`, or nothing came for it yet: its backlog is still on its way), else the moment its quiet
    /// and pacing allow, now or later.
    /// - Parameters:
    ///   - updatedAt: the session's `updated_at` as the list has it now.
    ///   - arrivedUpdatedAt: the session's `updated_at` when the chat last received items (nil: unknown).
    ///   - lastArrival: when items last came for the chat (a push, a page, a pull, streamed words); nil: none yet.
    ///   - lastPull: when the chat last asked this way.
    ///   - pulledFor: the `updatedAt` it last asked about.
    public static func catchUpAt(updatedAt: Date?, arrivedUpdatedAt: Date?, lastArrival: Date?, lastPull: Date?,
                                 pulledFor: Date?) -> Date? {
        guard let updatedAt, updatedAt != pulledFor, let lastArrival else { return nil }
        if let arrivedUpdatedAt, updatedAt <= arrivedUpdatedAt { return nil }
        let quiet = lastArrival.addingTimeInterval(staleQuiet)
        let paced = lastPull?.addingTimeInterval(stalePullEvery) ?? .distantPast
        return max(quiet, paced)
    }
}
