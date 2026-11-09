import Foundation
import Observation

/// One view's bounded projection cache. Only immutable input crosses to the serial worker.
@MainActor @Observable final class AgentListProjectionCache {
    nonisolated struct Partition: Equatable, Sendable {
        var group: String
        var owner: ObjectIdentifier
        var config: ServerConfig
    }

    nonisolated struct Key: Equatable, Sendable {
        var revision: UInt64
        var partitions: [Partition]
        var settings: AgentListProjection.Settings
        var workspaceRevision: UInt64 = 0
    }

    static func partitions(_ model: AppModel) -> [Partition] {
        model.links.map { Partition(group: $0.group, owner: ObjectIdentifier($0), config: $0.config) }
    }

    /// The body observes publication, not the dictionaries or task bookkeeping mutated during lookup.
    private(set) var publication: UInt64 = 0
    @ObservationIgnored private var value: AgentListProjection?
    @ObservationIgnored private var publishedKey: Key?
    @ObservationIgnored private var requestedKey: Key?
    /// At most one preparation runs; the newest request waits behind it (older waiting ones are dropped), so a stream
    /// of revisions faster than one derivation still publishes, newest last, instead of cancelling every run.
    @ObservationIgnored private(set) var pending: Task<Void, Never>?
    @ObservationIgnored private var queued: (Key, AgentListProjection.Input)?
    @ObservationIgnored private var ready: (Key, AgentListProjection)?
    @ObservationIgnored private var generation: UInt64 = 0
    @ObservationIgnored private let compute: @Sendable (AgentListProjection.Input) async throws -> AgentListProjection

    init(compute: (@Sendable (AgentListProjection.Input) async throws -> AgentListProjection)? = nil) {
        let worker = Worker()
        self.compute = compute ?? { input in try await worker.build(input) }
    }

    /// Initial/explicit/urgent changes retain immediate presentation; ordinary feed revisions prepare off-main.
    /// An unchanged retained-tab body neither captures nor rebuilds the inventory.
    /// `valid`: the key's partitions and settings are still the live ones. `canPublish`: nothing holds the list now.
    func resolve(_ key: Key, immediate: Bool,
                 input: () -> AgentListProjection.Input,
                 valid: @escaping @MainActor (Key) -> Bool,
                 canPublish: @escaping @MainActor (Key) -> Bool,
                 willPublish: @escaping @MainActor () -> Void) -> AgentListProjection {
        let _ = publication
        if publishedKey == key, let value { return value }
        if !immediate, let (readyKey, next) = ready, newer(readyKey), valid(readyKey), canPublish(readyKey) {
            ready = nil
            willPublish()
            publishedKey = readyKey
            value = next
            if readyKey == key { return next }
        }
        if value == nil || immediate || publishedKey?.partitions != key.partitions || publishedKey?.settings != key.settings ||
            publishedKey?.workspaceRevision != key.workspaceRevision {
            invalidate()
            let next = AgentListProjection.build(input())
            publishedKey = key
            requestedKey = key
            value = next
            return next
        }
        if requestedKey != key {
            requestedKey = key
            if pending == nil {
                start(key, input(), valid: valid, canPublish: canPublish, willPublish: willPublish)
            } else {
                queued = (key, input())
            }
        }
        return value!
    }

    private func newer(_ key: Key) -> Bool { key.revision > publishedKey?.revision ?? 0 }

    private func start(_ key: Key, _ captured: AgentListProjection.Input,
                       valid: @escaping @MainActor (Key) -> Bool,
                       canPublish: @escaping @MainActor (Key) -> Bool,
                       willPublish: @escaping @MainActor () -> Void) {
        let generation = generation
        let compute = compute
        pending = Task { [weak self] in
            let prepared: AgentListProjection?
            do { prepared = try await compute(captured) }
            catch { prepared = nil }
            guard !Task.isCancelled, let self, self.generation == generation else { return }
            self.pending = nil
            // The default worker only throws on cancellation. A failed custom worker must not strand an old snapshot:
            // recover from the same immutable input, behind the same live fences.
            let next = prepared ?? AgentListProjection.build(captured)
            // An older completed revision still lands if nothing newer did: the list converges while revisions stream.
            if self.newer(key), valid(key) {
                if canPublish(key) {
                    self.ready = nil
                    willPublish()
                    self.publishedKey = key
                    self.value = next
                    self.publication &+= 1
                } else if self.ready.map({ key.revision > $0.0.revision }) ?? true {
                    self.ready = (key, next)
                }
            }
            if let (nextKey, nextInput) = self.queued {
                self.queued = nil
                self.start(nextKey, nextInput, valid: valid, canPublish: canPublish, willPublish: willPublish)
            }
        }
    }

    /// A hold release asks the body to reconsider a completed value with its current live key/reader state.
    func resume() {
        if ready != nil { publication &+= 1 }
    }

    func invalidate() {
        generation &+= 1
        pending?.cancel()
        pending = nil
        queued = nil
        ready = nil
        requestedKey = nil
        publishedKey = nil
        value = nil
    }

    private actor Worker {
        func build(_ input: AgentListProjection.Input) throws -> AgentListProjection {
            try Task.checkCancellation()
            let result = AgentListProjection.build(input)
            try Task.checkCancellation()
            return result
        }
    }
}
