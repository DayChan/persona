import Foundation
import Observation

/// A full native page belongs to one authenticated app connection. Private frames are never
/// broadcast/replayed. Lifecycle and receipt fencing are separate from the legacy HTTP model.
@MainActor @Observable final class ExtensionPageStream {
    typealias Raw = ExtensionStreamJSON
    static let feature = "extension_ui_stream_v1"
    static let uiVersion = 6
    // These are renderer implementations, not every component declared in the generated vocabulary.
    static let components: Set<String> = ["text", "metric", "progress", "row", "button", "link", "field", "divider",
        "chart", "stat_grid", "segmented", "markdown", "table", "empty", "loading", "header", "breadcrumb",
        "file_tree", "code", "diff", "comment_thread", "composer", "image", "grid", "carousel"]

    private(set) var tree: ExtensionStreamTree?
    private(set) var ready = false
    private(set) var supported: Bool?
    private(set) var visible = false
    private(set) var recoveryIssue: String?
    @ObservationIgnored private(set) var hasMountedNative = false
    @ObservationIgnored var onView: ((ExtensionView) -> Void)?
    @ObservationIgnored var onFailure: ((String) -> Void)?
    @ObservationIgnored var onLegacy: (() -> Void)?
    @ObservationIgnored var resolveHostConfirmation: ((String) async throws -> Void)?
    @ObservationIgnored private var answeredHostConfirmations: [String: Date] = [:]
    @ObservationIgnored private var legacyPage = false
    @ObservationIgnored private let connection: LiveConnection
    @ObservationIgnored private let owner: String
    @ObservationIgnored private let contribution: String
    @ObservationIgnored private let context: ExtensionUIContext
    @ObservationIgnored private let state: String
    @ObservationIgnored private(set) var manager: String?
    @ObservationIgnored private var observer: UUID?
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var snapshotRequest: String?
    @ObservationIgnored private var snapshotRetries = 0
    @ObservationIgnored private var requests: [String: String] = [:]
    @ObservationIgnored private var events: [String: Pending] = [:]
    private struct EffectKey: Equatable { let session: String; let epoch: String; let event: String }
    @ObservationIgnored private var effects: [EffectKey] = []
    @ObservationIgnored private var receipts: [String: ExtensionUIStreamReceipt] = [:]

    struct Frozen: Sendable {
        let generation: Int
        let interaction: ExtensionUIStreamEvent
        let action: ExtensionAction
        let chosen: [String: String]
    }
    private struct Pending {
        let frozen: Frozen
        let confirmed: Bool
        let continuation: CheckedContinuation<ExtensionUIStreamActionResult?, Error>
    }

    init(connection: LiveConnection, owner: String, contribution: String, context: ExtensionUIContext,
         state: String, manager: String? = nil) {
        self.connection = connection; self.owner = owner; self.contribution = contribution
        self.context = context; self.state = state; self.manager = manager
        supported = connection.extensionStreamFeatures.map { $0.contains(Self.feature) }
        observer = connection.observeExtensionStream { [weak self] signal in self?.receive(signal) }
    }

    func setVisible(_ value: Bool) {
        guard visible != value else { return }
        if !value, tree == nil, let snapshotRequest { connection.retireExtensionStreamOpen(snapshotRequest, manager: manager) }
        visible = value; generation += 1; ready = false; snapshotRequest = nil; snapshotRetries = 0
        finishEvents(error: Raw.Failure.version)
        if value {
            if supported == true { requestSnapshot(resume: true) }
        } else if let tree {
            send(.suspend(.init(protocol: 1, requestId: UUID().uuidString, session: tree.session, epoch: tree.epoch)))
        }
    }

    func close(requestID: String = UUID().uuidString) {
        if tree == nil, let snapshotRequest { connection.retireExtensionStreamOpen(snapshotRequest, manager: manager) }
        visible = false; generation += 1; ready = false; snapshotRequest = nil
        finishEvents(error: Raw.Failure.version)
        if let tree { send(.close(.init(protocol: 1, requestId: requestID, session: tree.session, epoch: tree.epoch))) }
        tree = nil; receipts = [:]; effects = []
        if let observer { connection.removeExtensionStreamObserver(observer); self.observer = nil }
    }

    #if DEBUG && os(macOS)
    private enum CaptureCloseFailure: Error { case timeout, rejected }

    func closeForCapture() async throws {
        guard let tree else { close(); return }
        let requestID = UUID().uuidString
        let session = tree.session
        let owner = manager
        var acknowledged = false
        var rejected = false
        let handle = connection.observeExtensionStream { signal in
            if case .disconnected = signal { rejected = true; return }
            guard case .frame(let data) = signal, let raw = try? Raw.parse(data),
                  raw["request_id"]?.string == requestID,
                  (try? ExtensionStreamSchema.check(raw, definition: "UIStreamServerEnvelope")) != nil,
                  raw["manager"]?.string == nil || raw["manager"]?.string == owner else { return }
            if raw["kind"]?.string == "closed", raw["session"]?.string == session { acknowledged = true }
            else if raw["kind"]?.string == "error" { rejected = true }
        }
        defer { connection.removeExtensionStreamObserver(handle) }
        close(requestID: requestID)
        let deadline = ContinuousClock.now.advanced(by: .seconds(5))
        while !acknowledged, !rejected, ContinuousClock.now < deadline {
            try await Task.sleep(for: .milliseconds(100))
        }
        if rejected { throw CaptureCloseFailure.rejected }
        guard acknowledged else { throw CaptureCloseFailure.timeout }
    }
    #endif

    func refresh() { guard visible, supported == true else { return }; requestSnapshot() }

    var displayedRevision: ExtensionDisplayedRevision? {
        tree.map { .init(session: $0.session, epoch: $0.epoch, version: $0.version) }
    }

    func freeze(_ action: ExtensionAction, values: [String: String], event: String? = nil,
                displayed: ExtensionDisplayedRevision? = nil) throws -> Frozen {
        guard visible, ready, let tree, displayed == nil || displayed == displayedRevision else { throw Raw.Failure.version }
        let slots = tree.slots(action: action.id)
        guard slots.count == 1, let slot = slots.first,
              try JSONDecoder().decode(ExtensionAction.self, from: slot.saved.data) == action else { throw Raw.Failure.topology }
        let interaction = ExtensionUIStreamEvent(protocol: 1, session: tree.session, epoch: tree.epoch, version: tree.version,
            eventId: UUID().uuidString, key: slot.key, action: action.id, event: event ?? slot.event,
            value: .object(values.mapValues(JSONValue.string)))
        let raw = try Raw.parse(JSONEncoder().encode(interaction), limit: 65_536)
        try ExtensionStreamSchema.check(raw, definition: "UIStreamEvent")
        return Frozen(generation: generation, interaction: interaction, action: action, chosen: values)
    }

    func canPresent(_ frozen: Frozen) -> Bool {
        visible && ready && frozen.generation == generation && tree?.session == frozen.interaction.session && tree?.epoch == frozen.interaction.epoch
    }
    func isCurrent(_ frozen: Frozen) -> Bool { canPresent(frozen) && tree?.version == frozen.interaction.version }

    func perform(_ frozen: Frozen, confirmed: Bool = false) async throws -> ExtensionUIStreamActionResult? {
        guard frozen.generation == generation, visible, ready, let tree,
              tree.session == frozen.interaction.session, tree.epoch == frozen.interaction.epoch,
              tree.version == frozen.interaction.version, events.count < 64 else { throw Raw.Failure.version }
        let id = UUID().uuidString
        let request = ExtensionUIStreamRequest.event(.init(protocol: 1, requestId: id, interaction: frozen.interaction))
        return try await withCheckedThrowingContinuation { continuation in
            events[frozen.interaction.eventId] = Pending(frozen: frozen, confirmed: confirmed, continuation: continuation)
            requests[id] = frozen.interaction.eventId
            send(request)
            deadline(id: id, generation: generation)
        }
    }

    private func receive(_ signal: LiveConnection.ExtensionStreamSignal) {
        switch signal {
        case .disconnected:
            if supported != false { supported = nil }
            generation += 1; ready = false; snapshotRequest = nil
            finishEvents(error: Raw.Failure.version)
        case .hello(let features):
            let declared = features.contains(Self.feature)
            supported = (declared || hasMountedNative) && !legacyPage
            if visible, supported == true, declared { requestSnapshot(resume: true) }
            else if hasMountedNative && !declared { onFailure?("negotiation") }
        case .frame(let data): receive(data)
        case .hostInteraction(let interaction): receiveHostConfirmation(interaction)
        }
    }

    /// Consent is the user's choice for the exact frozen action, not a grant inferred from a source
    /// frame. The host still owns its gate, contribution/grant checks and final callback execution.
    private func receiveHostConfirmation(_ interaction: Interaction) {
        answeredHostConfirmations = answeredHostConfirmations.filter { $0.value > Date() }
        guard visible, ready, interaction.kind == .permission, interaction.toolName == "extension.ui",
              interaction.sessionID == tree?.session, interaction.secret != true, !interaction.requiresInput,
              interaction.expiresAt.map({ $0 > Date() }) ?? true,
              answeredHostConfirmations[interaction.id] == nil, answeredHostConfirmations.count < 64,
              let input = interaction.toolInput, let data = try? JSONEncoder().encode(input),
              let raw = try? Raw.parse(data, limit: 65_536),
              (try? ExtensionStreamSchema.check(raw, definition: "Action", file: "view.schema.json")) != nil,
              let action = try? JSONDecoder().decode(ExtensionAction.self, from: data),
              action.confirm?.isEmpty == false, interaction.summary == action.confirm,
              let resolve = resolveHostConfirmation else { return }
        let pending = events.values.filter { $0.confirmed && $0.frozen.action == action && isCurrent($0.frozen) }
        guard pending.count == 1, let frozen = pending.first?.frozen else { return }
        answeredHostConfirmations[interaction.id] = interaction.expiresAt ?? Date().addingTimeInterval(30)
        Task { [weak self] in
            guard let self, self.events[frozen.interaction.eventId] != nil, self.isCurrent(frozen) else { return }
            do { try await resolve(interaction.id) }
            catch { if self.canPresent(frozen) { self.onFailure?("confirmation") } }
        }
    }

    /// Exposed internally for canonical contract tests; real frames arrive only from the owning connection.
    func receive(_ data: Data) {
        guard supported == true else { return }
        var failedSnapshotRequest: String?
        var failedMount: (session: String, epoch: String)?
        do {
            let raw = try Raw.parse(data)
            let incomingManager = raw["manager"]?.string
            let correlation = raw["request_id"]?.string
            let correlated = correlation.map { $0 == snapshotRequest || requests[$0] != nil } ?? false
            let session = raw[raw["kind"]?.string ?? ""]?["session"]?.string ?? raw["session"]?.string
            guard correlated || (tree != nil && session == tree?.session) else { return }
            if let manager, let incomingManager { guard manager == incomingManager else { return } }
            else if manager == nil, correlated { manager = incomingManager }
            if correlated, correlation == snapshotRequest { failedSnapshotRequest = correlation }
            try ExtensionStreamSchema.check(raw, definition: "UIStreamServerEnvelope")
            let envelope = try JSONDecoder().decode(ExtensionUIStreamServerEnvelope.self, from: data)
            switch envelope {
            case .snapshot(let frame):
                let unsolicited = correlation == nil && snapshotRequest == nil && ready && tree?.session == frame.snapshot.session
                // A stateful callback may fall back to a full snapshot correlated to its event
                // request. It is not an open/recovery reply and must retain its frozen ownership.
                let eventSnapshot = correlation.flatMap { requests[$0] }.flatMap { events[$0] }.map {
                    canPresent($0.frozen) && $0.frozen.interaction.session == frame.snapshot.session &&
                        $0.frozen.interaction.epoch == frame.snapshot.epoch
                } ?? false
                guard visible, correlated && correlation == snapshotRequest || unsolicited || eventSnapshot else {
                    // An old mount completing after suspension must still release its allocation.
                    if tree == nil { send(.close(.init(protocol: 1, requestId: UUID().uuidString, session: frame.snapshot.session, epoch: frame.snapshot.epoch))) }
                    return
                }
                if tree == nil { failedMount = (frame.snapshot.session, frame.snapshot.epoch) }
                let staged = try ExtensionStreamTree(snapshot: raw["snapshot"]!, components: Self.components)
                if let tree, tree.session != staged.session || (tree.epoch == staged.epoch &&
                    (staged.version < tree.version || staged.rootKey != tree.rootKey ||
                     (staged.version == tree.version && staged.view != tree.view))) {
                    throw Raw.Failure.version
                }
                if let tree, tree.epoch != staged.epoch { finishEvents(error: Raw.Failure.version) }
                let native = try staged.nativeView
                hasMountedNative = true
                tree = staged; ready = true; snapshotRequest = nil; snapshotRetries = 0; recoveryIssue = nil
                onView?(native)
                #if DEBUG && os(macOS)
                MacExtensionSnapshotBatch.observeNativeCommit(owner: owner, contribution: contribution, version: staged.version, stream: self)
                #endif
                drainReceipts()
            case .delta:
                guard visible, ready, var staged = tree else { return }
                try staged.apply(raw["delta"]!)
                let native = try staged.nativeView
                tree = staged; onView?(native)
                #if DEBUG && os(macOS)
                MacExtensionSnapshotBatch.observeNativeCommit(owner: owner, contribution: contribution, version: staged.version, stream: self)
                #endif
                drainReceipts()
            case .receipt(let frame):
                let receipt = frame.receipt
                guard visible, let tree, receipt.session == tree.session, receipt.epoch == tree.epoch,
                      events[receipt.eventId] != nil, !effects.contains(effectKey(receipt)) else { return }
                if let result = raw["receipt"]?["result"] {
                    guard result.data.count <= 65_536, (result["push"]?["state"]?.string?.utf8.count ?? 0) <= 16_384 else { throw Raw.Failure.bounds }
                }
                guard receipts.count < 64 else { throw Raw.Failure.bounds }
                receipts[receipt.eventId] = receipt
                if !ready || receipt.version > tree.version + 1 { if snapshotRequest == nil { requestSnapshot() } }
                drainReceipts()
            case .error(let frame):
                guard correlated else { return }
                #if DEBUG && os(macOS)
                MacExtensionSnapshotBatch.observeNativeFailure(owner: owner, contribution: contribution, code: frame.error.code)
                #endif
                if let correlation, let event = requests.removeValue(forKey: correlation) {
                    events.removeValue(forKey: event)?.continuation.resume(throwing: Raw.Failure.version)
                }
                if correlation == snapshotRequest {
                    snapshotRequest = nil; ready = false
                    // A hub feature is not proof that a legacy process implements component pages.
                    // This is only an initial read-only mount: never fall back/retry an entered write.
                    if !hasMountedNative, tree == nil, events.isEmpty, ["rejected", "negotiation", "unsupported"].contains(frame.error.code) {
                        legacyPage = true; supported = false; onLegacy?(); return
                    }
                    recoveryIssue = frame.error.code
                    if ["expired", "stale"].contains(frame.error.code) {
                        if let tree { send(.close(.init(protocol: 1, requestId: UUID().uuidString, session: tree.session, epoch: tree.epoch))) }
                        tree = nil; generation += 1; finishEvents(error: Raw.Failure.version); retrySnapshot()
                    } else if ["manager_unavailable", "offline", "not_ready", "inactive", "timeout"].contains(frame.error.code) {
                        retrySnapshot()
                    } else { onFailure?(frame.error.code) }
                } else if visible { requestSnapshot() }
            case .closed(let frame):
                guard tree?.session == frame.session else { return }
                generation += 1; ready = false; tree = nil; snapshotRequest = nil
                finishEvents(error: Raw.Failure.version)
                if visible { requestSnapshot() }
            case .suspended: break // A late acknowledgment must not overwrite a resumed generation.
            case .unknown: throw Raw.Failure.unsupported
            }
        } catch {
            // Never skip a malformed operation or synthesize a new interaction to retry a write.
            recoveryIssue = (error as? Raw.Failure).map { String(describing: $0) } ?? "decode"
            if let failedMount {
                send(.close(.init(protocol: 1, requestId: UUID().uuidString, session: failedMount.session, epoch: failedMount.epoch)))
            }
            if let failedSnapshotRequest, failedSnapshotRequest == snapshotRequest { snapshotRequest = nil }
            if visible, snapshotRequest == nil { requestSnapshot() }
        }
    }

    private func effectKey(_ receipt: ExtensionUIStreamReceipt) -> EffectKey {
        EffectKey(session: receipt.session, epoch: receipt.epoch, event: receipt.eventId)
    }
    private func drainReceipts() {
        guard visible, ready, let tree else { return }
        for receipt in receipts.values.sorted(by: { $0.version < $1.version }) {
            guard receipt.session == tree.session, receipt.epoch == tree.epoch, receipt.version <= tree.version else { continue }
            receipts[receipt.eventId] = nil
            guard let pending = events.removeValue(forKey: receipt.eventId) else { continue }
            requests = requests.filter { $0.value != receipt.eventId }
            let key = effectKey(receipt)
            if !effects.contains(key) {
                effects.append(key); if effects.count > 64 { effects.removeFirst() }
                if receipt.status == "accepted" { pending.continuation.resume(returning: receipt.result) }
                else { pending.continuation.resume(throwing: Raw.Failure.version) }
            } else { pending.continuation.resume(returning: nil) }
        }
    }
    private func finishEvents(error: Error) {
        let pending = Array(events.values); events = [:]; requests = [:]; receipts = [:]
        for event in pending { event.continuation.resume(throwing: error) }
    }
    private func retrySnapshot() {
        guard visible, snapshotRetries < 3 else { onFailure?("snapshot"); return }
        snapshotRetries += 1
        let expectedGeneration = generation
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(1))
            guard let self, self.visible, self.generation == expectedGeneration, !self.ready else { return }
            self.requestSnapshot()
        }
    }

    private func requestSnapshot(resume: Bool = false) {
        guard visible, supported == true, connection.state == .connected, snapshotRequest == nil else { return }
        ready = false
        let id = UUID().uuidString; snapshotRequest = id
        if let tree {
            if resume { send(.resume(.init(protocol: 1, requestId: id, session: tree.session, epoch: tree.epoch, version: tree.version))) }
            else { send(.snapshot(.init(protocol: 1, requestId: id, session: tree.session, epoch: tree.epoch, version: tree.version))) }
        } else {
            var context = context
            context.uiVersion = Self.uiVersion
            context.stream = .init(protocol: 1, components: Self.components.sorted())
            send(.open(.init(protocol: 1, requestId: id, extension: owner, contribution: contribution, context: context, state: state)))
        }
        deadline(id: id, generation: generation)
    }
    private func send(_ request: ExtensionUIStreamRequest) {
        guard connection.state == .connected else { return }
        let envelope = ExtensionUIStreamClientEnvelope(type: "extension_ui_stream", manager: manager, request: request)
        guard let data = try? JSONEncoder().encode(envelope), data.count <= 65_536,
              let raw = try? Raw.parse(data, limit: 65_536), (try? ExtensionStreamSchema.check(raw, definition: "UIStreamClientEnvelope")) != nil else { return }
        connection.send(encodable: envelope)
    }
    private func deadline(id: String, generation: Int) {
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(30))
            guard let self, generation == self.generation else { return }
            if self.snapshotRequest == id { self.snapshotRequest = nil; self.onFailure?("timeout"); self.retrySnapshot() }
            if let event = self.requests.removeValue(forKey: id) {
                self.events.removeValue(forKey: event)?.continuation.resume(throwing: Raw.Failure.version)
                self.requestSnapshot()
            }
        }
    }
}
