import Foundation
import Observation

/// One filtered, frozen list window. HTTP work is serialized; changing the query cancels and fences the old work.
/// Delta upserts outside the window do not become rows. Their overlay only prevents a later frozen page resurrecting
/// old values, and is released when the snapshot is exhausted or the query resets.
@MainActor @Observable final class SessionListModel<Row: Decodable & Equatable & Sendable> {
    typealias Page = SessionListPage<Row>
    typealias Fetch = @MainActor (SessionListQuery, String?, String?) async throws -> Page
    private(set) var rows: [Row] = []
    private(set) var counts: SessionListCounts?
    private(set) var total = 0
    private(set) var hasMore = false
    private(set) var loading = false
    private(set) var loaded = false
    private(set) var paged = false
    private(set) var error: String?
    private(set) var query = SessionListQuery()
    @ObservationIgnored private(set) var revision: String?
    @ObservationIgnored private var snapshotRevision: String?
    @ObservationIgnored private var cursor: String?
    @ObservationIgnored private var overlays: [String: Row] = [:]
    @ObservationIgnored private var removed = Set<String>()
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var recoveries = 0
    @ObservationIgnored private var dirty = false
    @ObservationIgnored private var task: Task<Void, Never>?
    @ObservationIgnored private var taskID: UUID?
    @ObservationIgnored private let refreshes = CoalescedRefresh()
    @ObservationIgnored private var hintTask: Task<Void, Never>?
    @ObservationIgnored private let waitForHints: @MainActor () async throws -> Void
    @ObservationIgnored private var id: (Row) -> String
    @ObservationIgnored private var removalID: (String) -> String?
    @ObservationIgnored private var fetch: Fetch
    @ObservationIgnored private var fetchCounts: @MainActor (SessionListQuery) async throws -> SessionListCounts
    @ObservationIgnored var changed: (([Row]) -> Void)?

    init(id: @escaping (Row) -> String, removalID: @escaping (String) -> String?,
         fetch: @escaping Fetch, counts: @escaping @MainActor (SessionListQuery) async throws -> SessionListCounts,
         waitForHints: @escaping @MainActor () async throws -> Void = { try await Task.sleep(for: .milliseconds(150)) }) {
        self.id = id
        self.removalID = removalID
        self.fetch = fetch
        self.fetchCounts = counts
        self.waitForHints = waitForHints
    }

    func select(_ query: SessionListQuery) async {
        if self.query != query { reset(query) }
        await load()
    }

    func cancel() { reset(query) }

    private func reset(_ query: SessionListQuery) {
        recoveries = 0
        generation += 1
        refreshes.reset()
        hintTask?.cancel()
        hintTask = nil
        task?.cancel()
        task = nil
        taskID = nil
        self.query = query
        rows = []
        counts = nil
        loaded = false
        loading = false
        paged = false
        total = 0
        hasMore = false
        revision = nil
        snapshotRevision = nil
        cursor = nil
        overlays = [:]
        removed = []
        error = nil
        dirty = false
    }

    func load() async {
        guard !loaded || task != nil else { return }
        let gen = generation
        await refreshes.refresh { [weak self] in
            await self?.read(generation: gen, refresh: false)
        }
    }

    /// Manual refresh absorbs the queued hint and shares the serialized read without discarding the paging window.
    func refresh() async {
        let gen = generation
        let hadHint = hintTask != nil
        hintTask?.cancel()
        hintTask = nil
        await refreshes.refresh(afterCurrent: hadHint) { [weak self] in
            await self?.read(generation: gen, refresh: true)
        }
    }

    func reload() async {
        reset(query)
        await load()
    }

    func next() async {
        guard loaded, paged, hasMore, task == nil else { return }
        await run(first: false)
    }

    /// Preserve the short hint-merging window, then single-flight with one dirty follow-up. Later hints do not
    /// move the pending deadline; there is no refresh cooldown or maximum request rate.
    func invalidate() {
        guard hintTask == nil else { return }
        let gen = generation
        hintTask = Task { [weak self] in
            guard let self else { return }
            do { try await self.waitForHints() } catch { return }
            guard self.generation == gen, !Task.isCancelled else { return }
            self.hintTask = nil
            self.refreshes.invalidate { [weak self] in
                await self?.read(generation: gen, refresh: true)
            }
        }
    }

    private func read(generation gen: Int, refresh: Bool) async {
        // Loading another frozen page is serialized with the refresh, but is not itself an invalidation.
        while let task {
            await task.value
            guard generation == gen, !Task.isCancelled else { return }
            // Let the operation's owner finish cleanup and any 409 recovery before joining its next work.
            // Clearing its task ID here can make that owner skip the recovery tail.
            await Task.yield()
        }
        guard generation == gen, !Task.isCancelled else { return }
        if !loaded { await run(first: true) }
        else if refresh {
            if paged { await synchronizeRead() }
            else { await run(first: true) } // Legacy hubs still return their complete list.
        }
    }

    private func run(first: Bool) async {
        let gen = generation
        let readID = UUID()
        dirty = false
        loading = true
        let work = Task { [weak self] in
            guard let self else { return }
            do {
                let page = try await self.fetch(self.query, first ? nil : self.cursor, nil)
                try Task.checkCancellation()
                guard self.generation == gen else { return }
                if first {
                    self.rows = self.unique(page.sessions)
                    self.paged = page.isPaged
                    if !self.paged { self.counts = nil }
                    self.snapshotRevision = page.revision
                    self.revision = page.revision
                } else {
                    guard page.revision == self.snapshotRevision else { throw APIError(status: 409, message: "List snapshot changed") }
                    let ids = Set(self.rows.map(self.id))
                    let added = page.sessions.compactMap { row -> Row? in
                        let key = self.id(row)
                        guard !ids.contains(key), !self.removed.contains(key) else { return nil }
                        return self.overlays[key] ?? row
                    }
                    self.rows += self.unique(added)
                }
                if first { self.total = page.total ?? self.rows.count }
                self.cursor = page.nextCursor
                self.hasMore = page.hasMore == true && page.nextCursor != nil
                self.loaded = true
                self.error = nil
                self.changed?(self.rows)
                // Render the first page before waiting for aggregates. A counts revision is never a row baseline.
                if self.paged {
                    let counts = try await self.fetchCounts(self.query)
                    try Task.checkCancellation()
                    guard self.generation == gen else { return }
                    self.counts = counts
                }
                if !self.hasMore { self.overlays = [:]; self.removed = [] }
            } catch {
                if self.generation == gen, !Task.isCancelled { self.error = error.localizedDescription }
                if self.generation == gen, (error as? APIError)?.status == 409, self.recoveries < 1 {
                    self.recoveries += 1
                    self.loaded = false
                    self.cursor = nil
                    self.revision = nil
                    self.snapshotRevision = nil
                    self.overlays = [:]
                    self.removed = []
                    self.dirty = true
                }
            }
        }
        task = work
        taskID = readID
        await work.value
        guard generation == gen, taskID == readID else { return }
        task = nil
        taskID = nil
        loading = false
        if dirty {
            dirty = false
            if !loaded { await run(first: true) }
            else if paged { await synchronizeRead() }
        }
    }

    func synchronize() async {
        guard paged else { return }
        await refresh()
    }

    private func synchronizeRead() async {
        guard paged, let since = revision, task == nil else { return }
        let gen = generation
        let readID = UUID()
        dirty = false
        let work = Task { [weak self] in
            guard let self else { return }
            do {
                var next: String?
                var upserts: [String: Row] = [:]
                var removals = Set<String>()
                var target: String?
                var total = self.total
                repeat {
                    let page = try await self.fetch(self.query, next, since)
                    try Task.checkCancellation()
                    guard self.generation == gen else { return }
                    if let target, target != page.revision { throw APIError(status: 409, message: "List delta changed") }
                    target = page.revision
                    for row in page.sessions { upserts[self.id(row)] = row }
                    for token in page.remove ?? [] {
                        guard let key = self.removalID(token) else { throw APIError(status: 0, message: "Invalid list removal") }
                        removals.insert(key)
                    }
                    total = page.total ?? total
                    next = page.hasMore == true ? page.nextCursor : nil
                    if page.hasMore == true && next == nil { throw APIError(status: 0, message: "Missing list cursor") }
                } while next != nil
                let counts = try await self.fetchCounts(self.query)
                try Task.checkCancellation()
                guard self.generation == gen else { return }
                let rows = self.rows.compactMap { row -> Row? in
                    let key = self.id(row)
                    return removals.contains(key) ? nil : upserts[key] ?? row
                }
                if rows != self.rows { self.rows = rows; self.changed?(rows) }
                if self.hasMore {
                    for key in removals { self.overlays[key] = nil; self.removed.insert(key) }
                    for (key, row) in upserts where !removals.contains(key) { self.overlays[key] = row; self.removed.remove(key) }
                }
                self.total = total
                self.counts = counts
                self.revision = target // only the last successful delta page advances the checkpoint
                self.error = nil
            } catch {
                guard self.generation == gen, !Task.isCancelled else { return }
                self.error = error.localizedDescription
                if (error as? APIError)?.status == 409, self.recoveries < 1 {
                    self.recoveries += 1
                    self.loaded = false
                    self.overlays = [:]
                    self.removed = []
                    self.dirty = true
                }
            }
        }
        task = work
        taskID = readID
        await work.value
        guard generation == gen, taskID == readID else { return }
        task = nil
        taskID = nil
        if dirty {
            dirty = false
            if !loaded { await run(first: true) } else { await synchronizeRead() }
        }
    }

    private func unique(_ rows: [Row]) -> [Row] {
        var seen = Set<String>()
        return rows.filter { seen.insert(id($0)).inserted }
    }
}
