import Foundation

/// "The user is in the middle of something": what a page shows holds still, and what came meanwhile lands when it
/// lets go (docs/apple/ios-state.md「第 4 步」). One policy for every page: the page only says what it is doing (`set`), the
/// data it shows says what is critical, and how long anything waits is decided here.
/// - Scrolling: held from the first move until the list has rested `settle`; what waits lands after `cap` at most,
///   even while it still scrolls.
/// - Out of sight (a page pushed over it, another tab): held with no cap; what waits lands as soon as it is seen.
/// - Critical changes are never held: the owner lands those at once, alone, and the rest goes on waiting.
///
/// Not observable: holding or letting go draws nothing. Only what the owner publishes when it lands does.
@MainActor final class Hold {
    enum Reason: Hashable, Sendable {
        /// A drag, a glide, until the list has rested `settle`.
        case scrolling
        /// The page cannot be seen.
        case outOfSight
        /// The page is kept behind the one in front (the Mac's sections, `MacPageStack`): held with no cap, as out of
        /// sight, and told apart from it so the two can come and go on their own.
        case behind
    }

    /// How long a list rests after a scroll before what came meanwhile lands.
    static let settle: Duration = .milliseconds(500)
    /// The longest a change waits while the list is being scrolled.
    static let cap: Duration = .seconds(5)

    /// Lands everything that waits (the owner shows its latest, then calls `landed`).
    var land: () -> Void = {}
    /// For the scroll timeline (`PerfScroll`): "Agents list holds", "Agents list lets go".
    let name: String
    let settle: Duration
    let cap: Duration
    private(set) var reasons: Set<Reason> = []
    /// Invalidates an asynchronous landing permission when a new gesture or visibility edge is registered.
    private(set) var activity: UInt64 = 0
    /// Since when something waits (nil: what shows is the latest).
    private(set) var waitingSince: Date?
    private var settleTask: Task<Void, Never>?
    private var capTask: Task<Void, Never>?

    init(_ name: String, settle: Duration = Hold.settle, cap: Duration = Hold.cap) {
        self.name = name
        self.settle = settle
        self.cap = cap
    }

    var held: Bool { !reasons.isEmpty }

    /// The page's state. Scrolling starts at once and ends only after the list has rested `settle` (a move in between
    /// keeps it going); out of sight starts and ends at once.
    func set(_ reason: Reason, _ on: Bool) {
        activity &+= 1
        if reason == .scrolling {
            settleTask?.cancel()
            settleTask = nil
            if !on, reasons.contains(.scrolling) {
                settleTask = Task { [weak self] in
                    try? await Task.sleep(for: self?.settle ?? Self.settle)
                    guard !Task.isCancelled, let self else { return }
                    self.settleTask = nil
                    self.apply(.scrolling, false)
                }
                return
            }
        }
        apply(reason, on)
    }

    /// A change came. False: nothing holds it, show it now. True: it waits (the owner may still land its critical
    /// part), and lands after `cap` if the list is being scrolled.
    func wait() -> Bool {
        guard held else { return false }
        if waitingSince == nil { waitingSince = .now }
        capIfScrolling()
        return true
    }

    /// What shows is the latest again: nothing waits.
    func landed() {
        waitingSince = nil
        capTask?.cancel()
        capTask = nil
    }

    private func apply(_ reason: Reason, _ on: Bool) {
        let was = held
        if on { reasons.insert(reason) } else { reasons.remove(reason) }
        if !reasons.contains(.scrolling) { capTask?.cancel(); capTask = nil } // out of sight alone has no cap
        capIfScrolling()
        guard held != was else { return }
        #if (os(iOS) || os(visionOS)) && (DEBUG || PERF_SIGNPOST)
        PerfScroll.mark("\(name) \(held ? "holds" : "lets go")")
        #endif
        if !held, waitingSince != nil { land() }
    }

    /// What waits while the list is being scrolled lands after `cap` at most, however it started waiting.
    private func capIfScrolling() {
        guard reasons.contains(.scrolling), waitingSince != nil, capTask == nil else { return }
        capTask = Task { [weak self] in
            try? await Task.sleep(for: self?.cap ?? Self.cap)
            guard !Task.isCancelled, let self else { return }
            self.capTask = nil
            if self.held, self.waitingSince != nil { self.land() }
        }
    }
}
