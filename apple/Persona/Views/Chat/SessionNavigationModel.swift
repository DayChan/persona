import Foundation
import Observation

/// The user-input index and a separate reading window. Live items, subscriptions and covered sequences stay in
/// AppModel's store; distant navigation never claims that the intervening transcript has been downloaded.
@MainActor @Observable final class SessionNavigationModel {
    static let feature = "session_navigation_v1"
    static let initialPageLimit = 12
    static let initialMarkCount = 40

    private(set) var marks: [EdgeMark] = []
    private(set) var hasMore = false
    private(set) var nextBeforeSeq: Int?
    private(set) var history: String?
    private(set) var window: ChatStore?
    private(set) var moreAfter = false
    private(set) var loadingTarget = false
    private(set) var loadingPage = false
    private(set) var error: String?
    /// False for unsupported/unknown hubs even though loaded-line fallback has no remote cursor.
    var hasCompleteIndex: Bool { indexed && !hasMore }

    @ObservationIgnored private var key: SessionKey?
    @ObservationIgnored private var api: APIClient?
    @ObservationIgnored private var live: ChatStore?
    @ObservationIgnored private var liveResets = 0
    @ObservationIgnored private var observedLiveHistory: String?
    @ObservationIgnored private var supported = false
    @ObservationIgnored private var active = false
    @ObservationIgnored private var indexed = false
    @ObservationIgnored private var unavailable = false
    @ObservationIgnored private var generation = UUID()
    @ObservationIgnored private var indexRequest: UUID?
    @ObservationIgnored private var targetRequest: UUID?
    @ObservationIgnored private var targetSeq: Int?
    @ObservationIgnored private(set) var selectionRevision = UUID()
    @ObservationIgnored private var pageRequest: UUID?
    @ObservationIgnored private var windowFrom: Int?
    @ObservationIgnored private var windowTo: Int?
    @ObservationIgnored private var windowHistory: String?

    /// Called when the visible session, advertised capability or live history revision changes.
    func activate(_ key: SessionKey, api: APIClient?, supported: Bool, live: ChatStore) async {
        if self.key != key || self.supported != supported || self.live !== live || liveResets != live.resets
            || observedLiveHistory != live.history {
            invalidate()
            self.key = key
            self.live = live
            liveResets = live.resets
            observedLiveHistory = live.history
            self.supported = supported
        }
        self.api = api
        active = true
        if supported, !indexed { await warmIndex() }
    }

    /// Sparse transcripts can exhaust a server parsing budget before finding forty inputs. Follow explicit cursors
    /// only during first warm-up, with a page and elapsed-time budget; ordinary gesture paging remains one page.
    private func warmIndex() async {
        let revision = generation, liveHistory = live?.history
        let deadline = ContinuousClock.now + .seconds(10)
        await loadIndex()
        for _ in 1..<Self.initialPageLimit {
            guard current(revision, liveHistory: liveHistory), marks.count < Self.initialMarkCount,
                  hasMore, let before = nextBeforeSeq, error == nil, ContinuousClock.now < deadline else { return }
            let remaining = ContinuousClock.now.duration(to: deadline).components
            let timeout = Double(remaining.seconds) + Double(remaining.attoseconds) / 1e18
            guard timeout > 0 else { return }
            await loadIndex(older: true, timeout: timeout)
            guard nextBeforeSeq != before else { return }
        }
    }

    /// A retained, hidden page must not publish a response or finish an old landing.
    func suspend() {
        active = false
        fenceRequests()
    }

    private func fenceRequests() {
        generation = UUID()
        indexRequest = nil
        cancelLanding()
        pageRequest = nil
        loadingPage = false
    }

    func cancelLanding() {
        targetRequest = nil
        targetSeq = nil
        selectionRevision = UUID()
        loadingTarget = false
    }

    func clearSession() {
        suspend()
        invalidate()
        key = nil
        live = nil
        api = nil
    }

    private func invalidate() {
        fenceRequests()
        indexed = false
        unavailable = false
        marks = []
        history = nil
        hasMore = false
        nextBeforeSeq = nil
        window = nil
        windowFrom = nil
        windowTo = nil
        windowHistory = nil
        moreAfter = false
        error = nil
    }

    private func current(_ revision: UUID, liveHistory: String?) -> Bool {
        active && generation == revision && live?.resets == liveResets && live?.history == liveHistory && !Task.isCancelled
    }

    /// Older indexes are prefetched while moving toward the earlier half, never by downloading body history.
    static func shouldPrefetch(picked: Int?, count: Int, movingOlder: Bool, enteredOlderHalf: Bool) -> Bool {
        guard let picked, count > 0 else { return false }
        return picked < count / 2 && (movingOlder || enteredOlderHalf)
    }

    func prefetch(picked: Int?, count: Int, movingOlder: Bool, enteredOlderHalf: Bool, beforeSeq: Int?) async {
        // Queued movement callbacks from before a page arrived cannot consume its new paging cursor.
        guard beforeSeq == nextBeforeSeq,
              Self.shouldPrefetch(picked: picked, count: count, movingOlder: movingOlder, enteredOlderHalf: enteredOlderHalf) else { return }
        await loadIndex(older: true)
    }

    /// One request at a time. Failure does not consume the cursor, so another upward movement can retry it.
    func loadIndex(older: Bool = false, refresh: Bool = false, timeout: TimeInterval = 10) async {
        guard active, supported, !unavailable, let key, let api, indexRequest == nil else { return }
        if !older, indexed, !refresh { return }
        if older, (!indexed || !hasMore || nextBeforeSeq == nil) { return }
        let before = older ? nextBeforeSeq : nil
        let revision = generation, liveHistory = live?.history, request = UUID()
        indexRequest = request
        defer { if indexRequest == request { indexRequest = nil } }
        do {
            let page = try await api.navigation(key, beforeSeq: before, history: older ? history : nil, timeout: timeout)
            guard current(revision, liveHistory: liveHistory) else { return }
            if older, page.history != history {
                invalidate()
                await warmIndex()
                return
            }
            let replacedHistory = !older && indexed && page.history != history
            if replacedHistory { invalidate() }
            let previousNewest = marks.last?.seq
            let disjointRefresh = !older && page.messages.first.map { first in previousNewest.map { first.seq > $0 } ?? false } == true
            var byID = Dictionary(marks.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
            for message in page.messages {
                let text = message.text.trimmingCharacters(in: .whitespacesAndNewlines)
                byID[message.id] = EdgeMark(id: message.id, text: text.isEmpty ? String(localized: "Photo [Chat Layout]") : text,
                                            seq: message.seq, answer: message.answer)
            }
            marks = byID.values.sorted { ($0.seq ?? 0, $0.id) < ($1.seq ?? 0, $1.id) }
            history = page.history
            if older || !indexed || previousNewest == nil || disjointRefresh || (hasMore && nextBeforeSeq == nil) {
                // A refresh beyond the known newest input may have skipped more than forty new inputs. Keep old
                // identities, but traverse again from the refreshed cursor rather than claiming the gap is complete.
                hasMore = page.hasMore
                // A non-advancing cursor is not safe to repeat automatically.
                nextBeforeSeq = page.nextBeforeSeq.flatMap { next in
                    let advances = before.map { next < $0 } ?? true
                    return advances ? next : nil
                }
            }
            indexed = true
            error = nil
            if replacedHistory {
                // Continue a short refreshed index under the new generation, not the superseded request's fence.
                await warmIndex()
            }
        } catch {
            guard current(revision, liveHistory: liveHistory) else { return }
            if let apiError = error as? APIError, apiError.status == 409 || apiError.code == "stale_history" {
                await staleHistory()
                return
            }
            if let apiError = error as? APIError,
               apiError.status == 404 || apiError.status == 501 || apiError.code?.contains("unsupported") == true {
                unavailable = true // a new hub can still proxy an older runtime: loaded-line navigation remains usable
            }
            self.error = error.localizedDescription
        }
    }

    /// Only release calls this. An already drawn mark requires no request; an absent mark gets one bounded window.
    /// Returns the item identity after its rows are ready; the view resolves image groups to their own line identity.
    func select(_ mark: EdgeMark, loaded: Bool) async -> (id: String, revision: UUID)? {
        guard active, let key else { return nil }
        if loadingTarget, targetSeq == mark.seq { return nil }
        // Supersede an earlier landing even if this selection needs no network request.
        cancelLanding()
        if loaded, window != nil || history == nil || history == live?.history { return (mark.id, selectionRevision) }
        guard supported, !unavailable, let seq = mark.seq, let api else { return nil }
        targetSeq = seq
        let revision = generation, liveHistory = live?.history, token = history, request = UUID()
        targetRequest = request
        loadingTarget = true
        defer {
            if targetRequest == request { targetRequest = nil; loadingTarget = false }
        }
        do {
            let page = try await api.navigationWindow(key, aroundSeq: seq, history: token)
            guard current(revision, liveHistory: liveHistory), targetRequest == request else { return nil }
            let target = page.items.first(where: { $0.id == mark.id })
                ?? page.items.first(where: { $0.seq == seq && $0.role == "user" && $0.kind == .text })
                ?? page.items.first(where: { $0.seq == seq && $0.role == "user" })
            guard token == nil || page.history == token, let target else {
                await staleHistory()
                return nil
            }
            let reading = ChatStore(key: key, cachesTail: false)
            reading.merge(page.items, backlog: true, hasMore: page.hasMore)
            let deadline = ContinuousClock.now + .seconds(10)
            while reading.isRebuilding, ContinuousClock.now < deadline {
                try await Task.sleep(for: .milliseconds(20))
            }
            guard current(revision, liveHistory: liveHistory), targetRequest == request, !reading.isRebuilding else { return nil }
            // Replacing a window fences its in-flight before/after/unfold responses without touching the live source.
            pageRequest = nil
            loadingPage = false
            // before_seq is exclusive; the coverage lower bound can precede a real, not-yet-loaded item.
            windowFrom = page.items.first?.seq
            windowTo = page.toSeq ?? page.items.last?.fold?.throughSeq ?? page.items.last?.seq
            moreAfter = page.moreAfter == true
            windowHistory = page.history ?? token
            window = reading
            error = nil
            return (target.id, selectionRevision)
        } catch {
            guard current(revision, liveHistory: liveHistory), targetRequest == request else { return nil }
            if let apiError = error as? APIError, apiError.status == 409 || apiError.code == "stale_history" {
                await staleHistory()
            } else {
                self.error = error.localizedDescription
            }
            return nil
        }
    }

    private func staleHistory() async {
        invalidate()
        if active, supported { await warmIndex() }
    }

    /// Explicit, visibility-owned pagination of the isolated window. It never fills all the way to the live tail.
    func loadWindow(older: Bool) async {
        guard active, let key, let api, let reading = window, pageRequest == nil else { return }
        let cursor = older ? windowFrom : windowTo
        guard let cursor, older ? reading.hasMore : moreAfter else { return }
        let revision = generation, liveHistory = live?.history, token = windowHistory ?? history, request = UUID()
        pageRequest = request
        loadingPage = true
        defer { if pageRequest == request { pageRequest = nil; loadingPage = false } }
        do {
            let page = try await api.navigationWindow(key, beforeSeq: older ? cursor : nil,
                                                     afterSeq: older ? nil : cursor, history: token)
            guard current(revision, liveHistory: liveHistory), pageRequest == request, window === reading else { return }
            guard token == nil || page.history == token else { await staleHistory(); return }
            windowHistory = page.history ?? token
            if older {
                let from = page.items.first?.seq
                guard let from, from < cursor else { return }
                reading.prepend(page.items, hasMore: page.hasMore == true)
                windowFrom = from
            } else {
                let to = page.toSeq ?? page.items.last?.fold?.throughSeq ?? page.items.last?.seq
                guard let to, to > cursor else { return }
                reading.merge(page.items)
                windowTo = to
                moreAfter = page.moreAfter == true
            }
            error = nil
        } catch {
            guard current(revision, liveHistory: liveHistory), pageRequest == request, window === reading else { return }
            if let apiError = error as? APIError, apiError.status == 409 || apiError.code == "stale_history" {
                await staleHistory()
            } else {
                self.error = error.localizedDescription
            }
        }
    }

    func unfold(_ fold: Item) async throws {
        guard active, let key, let api, let reading = window else { return }
        let revision = generation, liveHistory = live?.history
        do {
            let items = try await api.foldedWork(key, fold, history: windowHistory ?? history)
            guard current(revision, liveHistory: liveHistory), window === reading else { return }
            reading.unfold(fold, into: items)
        } catch {
            guard current(revision, liveHistory: liveHistory), window === reading else { return }
            if let apiError = error as? APIError, apiError.status == 409 || apiError.code == "stale_history" {
                await staleHistory()
            }
            throw error
        }
    }

    /// Latest restores the untouched live source and fences late window/landing replies.
    func returnToLatest() {
        fenceRequests()
        window = nil
        windowFrom = nil
        windowTo = nil
        windowHistory = nil
        moreAfter = false
        error = nil
    }
}
