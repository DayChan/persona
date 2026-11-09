import Foundation
import Observation

// Installing, upgrading and removing extensions, with what the hub is doing meanwhile (docs/protocol/protocol.md「扩展的安装、升级、卸载进度」,
// #3277, #3307).
//
// A hub with `extension_operations` runs each as an operation and pushes it in `extension_operation` events (to a
// socket that declared the feature) as it goes through its steps: an install previews in the background (download,
// verify, unpack, check), stops at needs_consent for the user to agree to the permissions, and goes on with the same
// operation once they do. This store keeps them for the whole app, so the page that started one can be left and come
// back to it, and the market's list marks the row. An older hub answers when it is done: the store then keeps a local
// operation with no steps, so the page still says what is going on and for how long instead of a button that spins.

/// What the hub answers a request made with `async`.
nonisolated struct ExtensionOperationReply: Decodable, Sendable {
    var operation: ExtensionOperation?
}

/// One operation read on its own: while it waits for consent, the preview to agree to.
nonisolated struct ExtensionOperationDetail: Decodable, Sendable {
    var operation: ExtensionOperation
    var preview: ExtensionPreview?

    private enum CodingKeys: String, CodingKey { case preview }

    init(from decoder: Decoder) throws {
        operation = try ExtensionOperation(from: decoder)
        preview = try? decoder.container(keyedBy: CodingKeys.self).decodeIfPresent(ExtensionPreview.self, forKey: .preview)
    }
}

extension APIClient {
    /// The operations going on, waiting for consent, and the ones ended in the last minutes.
    func extensionOperations() async throws -> [ExtensionOperation] {
        struct List: Decodable { var operations: [ExtensionOperation]? }
        let list: List = try await get(extensionPath("/-/operations"))
        return list.operations ?? []
    }

    func extensionOperation(_ id: String) async throws -> ExtensionOperationDetail {
        try await get(extensionPath("/-/operations/\(id)"))
    }

    /// Starts it again from the beginning (the package is in the hub's cache); a new operation.
    func retryExtensionOperation(_ id: String) async throws -> ExtensionOperation {
        try Self.operation(try await request("POST", extensionPath("/-/operations/\(id)/retry"), timeout: 60))
    }

    /// Cancels one that downloads, checks or waits for consent; removes one that ended from the list.
    func deleteExtensionOperation(_ id: String) async throws {
        _ = try await request("DELETE", extensionPath("/-/operations/\(id)"))
    }

    /// Previews a market's extension in the background: download and checks, then it waits for consent.
    func startCatalogPreview(market: String, extension id: String) async throws -> ExtensionOperation {
        struct Body: Encodable {
            var market: String
            var extensionID: String
            var plugin: String
            var background = true
            enum CodingKeys: String, CodingKey { case market, plugin, extensionID = "extension", background = "async" }
        }
        return try Self.operation(try await request("POST", extensionPath("/-/catalog/preview"),
                                                    body: Body(market: market, extensionID: id, plugin: id), timeout: 60))
    }

    /// The user agreed to the preview: the operation it came from goes on to install (or upgrade) it.
    func agreeExtensionInstall(_ id: String, preview: ExtensionPreview) async throws -> ExtensionOperation {
        let path = preview.upgrade == true ? extensionPath("/\(id)/-/upgrade") : extensionPath("/-/install")
        return try Self.operation(try await request("POST", path, body: ExtensionInstallRequest(preview: preview, background: true), timeout: 60))
    }

    private static func operation(_ data: Data) throws -> ExtensionOperation {
        if let op = try? JSON.decoder.decode(ExtensionOperationReply.self, from: data).operation { return op }
        return try JSON.decoder.decode(ExtensionOperation.self, from: data)
    }
}

/// The app's extension operations, from the hub's events and lists and from the requests this app makes.
@MainActor @Observable final class ExtensionOperations {
    static let shared = ExtensionOperations()

    struct Deps {
        var api: () -> APIClient?
        var features: () -> Set<String>
        /// The hub the operations are of (another one: the list starts over).
        var hub: () -> String?
        /// How often an operation being waited for is read again when no event came.
        var poll: Duration = .seconds(2)
        /// How long to wait for one before saying so.
        var patience: TimeInterval = 15 * 60
        static var live: Deps {
            .init(api: { AppModel.shared.api }, features: { AppModel.shared.hubFeatures },
                  hub: { AppModel.shared.config?.url.absoluteString })
        }
    }

    /// By operation id (another manager's: `ExtensionOperation.storeKey`).
    private(set) var operations: [String: ExtensionOperation] = [:]
    @ObservationIgnored private var lastHub: String?
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    /// The hub runs them in steps and pushes them.
    var stepped: Bool { deps.features().hasExtensionFeature("operations") }

    /// The one to show for an extension (of a market, when given) of `manager` (nil: the default's, whose catalog the
    /// market pages show): one going on, else the latest. Another manager's copy of the same extension is not this one.
    func operation(for extensionID: String, market: String? = nil, manager: String? = nil) -> ExtensionOperation? {
        let mine = operations.values.filter {
            $0.extensionID == extensionID && (market == nil || $0.market == nil || $0.market == market) && $0.manager == manager
        }
        return mine.first(where: \.isActive) ?? mine.max { ($0.startedAt ?? .distantPast) < ($1.startedAt ?? .distantPast) }
    }

    /// From an `extension_operation` event or a list: a newer state of the same operation replaces the one kept.
    func received(_ op: ExtensionOperation) {
        resetIfAnotherHub()
        let key = op.storeKey
        if let old = operations[key], let was = old.updatedAt, let now = op.updatedAt, now < was { return }
        if operations[key] != op { operations[key] = op }
    }

    /// Reads what the hub has going on (a page opening, the connection back: events missed are not sent again).
    func reload() async {
        resetIfAnotherHub()
        guard stepped, let api = deps.api(), let list = try? await api.extensionOperations() else { return }
        // The hub's list is the truth for its operations; this app's own (an older hub's) stay.
        operations = operations.filter { $0.value.isLocal }
        list.forEach(received)
    }

    /// Put away: the hub forgets one that ended, cancels one that waits for consent or still checks.
    func dismiss(_ id: String) async {
        guard let op = operations[id] else { return }
        operations[id] = nil
        if !op.isLocal { try? await deps.api()?.deleteExtensionOperation(id) }
    }

    // MARK: - stepped (a hub with operations)

    /// Downloads and checks a market's extension in the background; the result is the operation once it waits for
    /// consent, failed or was canceled.
    func preview(market: String, extension id: String) async throws -> ExtensionOperation {
        guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
        let op = try await api.startCatalogPreview(market: market, extension: id)
        received(op)
        return await follow(op.id) { $0.state != .running }
    }

    /// The preview the operation waits on.
    func consentPreview(_ id: String) async throws -> ExtensionPreview? {
        guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
        let detail = try await api.extensionOperation(id)
        received(detail.operation)
        return detail.preview
    }

    /// The user agreed: the operation installs and the result is how it ended.
    func agree(_ id: String, preview: ExtensionPreview) async throws -> ExtensionOperation {
        guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
        let op = try await api.agreeExtensionInstall(id, preview: preview)
        received(op)
        return await follow(op.id) { !$0.isRunning }
    }

    /// Starts a failed one again; the result once it waits for consent again or ended (a new operation).
    func retry(_ op: ExtensionOperation) async throws -> ExtensionOperation {
        guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
        let next = try await api.retryExtensionOperation(op.id)
        operations[op.id] = nil
        received(next)
        return await follow(next.id) { !$0.isRunning }
    }

    // MARK: - an older hub

    /// Installs or upgrades with the preview agreed to, as the hub answers when it is done; a local operation says
    /// what is going on meanwhile. The result is how it ended.
    func installWithoutSteps(_ id: String, name: String?, preview: ExtensionPreview) async -> ExtensionOperation {
        let upgrade = preview.upgrade == true
        let local = ExtensionOperation(id: Self.localID(), kind: upgrade ? .upgrade : .install, extensionID: id, name: name,
                                       version: preview.manifest.version, from: preview.from, market: preview.market,
                                       startedAt: .now, updatedAt: .now)
        received(local)
        do {
            guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
            if upgrade { _ = try await api.upgradeExtension(id, preview: preview) } else { _ = try await api.installExtension(preview: preview) }
            return finish(local, error: nil)
        } catch {
            return finish(local, error: error)
        }
    }

    /// Removes, as the hub answers when it is done (a hub with operations pushes the steps meanwhile; an older one
    /// gets a local operation). A refusal before anything started (has_dependents) is thrown for the caller to ask.
    func uninstall(_ id: String, name: String?, cascade: Bool, members: Bool) async throws -> ExtensionRemovalResult {
        let local = stepped ? nil : ExtensionOperation(id: Self.localID(), kind: .uninstall, extensionID: id, name: name,
                                                       startedAt: .now, updatedAt: .now)
        if let local { received(local) }
        do {
            guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
            let result = try await api.uninstallExtension(id, cascade: cascade, members: members)
            if let local { _ = finish(local, error: nil) }
            return result
        } catch let e as APIError where e.code == "has_dependents" {
            if let local { operations[local.id] = nil }
            throw e
        } catch {
            if let local { _ = finish(local, error: error) }
            throw error
        }
    }

    // MARK: -

    private func finish(_ local: ExtensionOperation, error: Error?) -> ExtensionOperation {
        var op = local
        op.state = error == nil ? .succeeded : .failed
        op.error = error?.localizedDescription
        op.errorCode = (error as? APIError)?.code
        op.endedAt = .now
        op.updatedAt = .now
        operations[op.id] = op
        return op
    }

    /// Waits until `done` says the hub's operation got where it was going: its events move it on, and it is read
    /// again now and then in case one was missed.
    private func follow(_ id: String, until done: (ExtensionOperation) -> Bool) async -> ExtensionOperation {
        let started = Date.now
        var lastRead = Date.now
        while true {
            if let op = operations[id], done(op) { return op }
            if operations[id] == nil, let fresh = try? await deps.api()?.extensionOperation(id).operation {
                received(fresh)
                continue
            }
            if Date.now.timeIntervalSince(started) > deps.patience || Task.isCancelled {
                var late = operations[id] ?? ExtensionOperation(id: id, kind: .install, extensionID: "")
                late.state = .failed
                late.error = String(localized: "The hub hasn’t said whether it finished after a long wait. Pull down to refresh.")
                late.retryable = false
                return late
            }
            try? await Task.sleep(for: .milliseconds(200))
            let seconds = Double(deps.poll.components.seconds) + Double(deps.poll.components.attoseconds) / 1e18
            if Date.now.timeIntervalSince(lastRead) >= seconds, let api = deps.api() {
                lastRead = .now
                if let fresh = try? await api.extensionOperation(id).operation { received(fresh) }
            }
        }
    }

    private func resetIfAnotherHub() {
        let hub = deps.hub()
        guard hub != lastHub else { return }
        if lastHub != nil { operations = [:] }
        lastHub = hub
    }

    private static func localID() -> String { "local-" + UUID().uuidString }
}

extension ExtensionOperation {
    /// Kept by this app for a hub that does not run operations.
    var isLocal: Bool { id.hasPrefix("local-") }

    /// Its place in `ExtensionOperations`: the id for the default manager's, the id under its manager for another's
    /// (each manager numbers its own).
    var storeKey: String { manager.map { "\($0)\u{0}\(id)" } ?? id }
}

/// The words for an operation and its steps (the hub sends ids; the words are the app's, docs/protocol/protocol.md).
enum ExtensionOperationText {
    static func verb(_ kind: ExtensionOperation.Kind) -> String {
        switch kind {
        case .install: String(localized: "Install")
        case .upgrade: String(localized: "Update [Extension Market Views 94996c]")
        case .uninstall: String(localized: "Uninstall")
        }
    }

    /// A step's name; one this app does not know shows as its id.
    static func step(_ id: String, kind: ExtensionOperation.Kind) -> String {
        switch id {
        case "download": String(localized: "Download package")
        case "verify": String(localized: "Verify checksum")
        case "unpack": String(localized: "Unpack")
        case "check": String(localized: "Check permissions and dependencies")
        case "consent": String(localized: "Waiting for your consent")
        case "tools": String(localized: "Install command-line tools")
        case "install": kind == .upgrade ? String(localized: "Switch to new version") : String(localized: "Install on hub")
        case "start": String(localized: "On [Herdr Notice 56410f]")
        case "nodes": String(localized: "Sync to computers")
        case "stop": String(localized: "Stop extension")
        case "remove": String(localized: "Delete files and records")
        default: id
        }
    }

    /// What a step says under its name: a download's bytes, the computers done, the cache.
    static func detail(_ s: ExtensionOperationStep) -> String? {
        if s.cached == true { return String(localized: "Used the package cached on the hub") }
        if let bytes = s.bytes, bytes > 0 {
            let done = bytes.storageText
            if let total = s.total, total > 0, s.state == .running {
                let all = total.storageText
                return String(localized: "\(done) / \(all)")
            }
            return done
        }
        if let m = s.machines, m.total > 0 {
            var line = String(localized: "\(m.done) of \(m.total) computers")
            if let errors = m.errors, !errors.isEmpty {
                line += " · " + errors.sorted { $0.key < $1.key }.map { "\($0.key)：\($0.value)" }.joined(separator: "；") // i18n-ignore
            }
            return line
        }
        return nil
    }

    /// A program of the tools step: "Uses the one on this machine" when the hub machine's own copy is used; else where it is.
    static func tool(_ t: ExtensionOperationStep.Tool) -> String? {
        if t.reused == true { return String(localized: "Uses the one on this machine") }
        switch t.state {
        case .running: return String(localized: "Installing…")
        case .done: return String(localized: "Installed [Extension Operations 11d8f1]")
        case .failed: return String(localized: "Didn't install")
        case .pending, .skipped: return nil
        }
    }

    /// Installing… / Waiting for your consent / Install didn't finish / Installed / Canceled.
    static func title(_ op: ExtensionOperation) -> String {
        switch (op.state, op.kind) {
        case (.running, .install): String(localized: "Installing…")
        case (.running, .upgrade): String(localized: "Upgrading [Extension Operations bc572b]")
        case (.running, .uninstall): String(localized: "Uninstalling [Extension Operations e557ff]")
        case (.needsConsent, _): String(localized: "Waiting for your consent")
        case (.failed, .install): String(localized: "Install didn’t finish")
        case (.failed, .upgrade): String(localized: "Upgrade didn’t finish")
        case (.failed, .uninstall): String(localized: "Uninstall didn’t finish")
        case (.succeeded, .install): String(localized: "Installed [Extension Market Views a8b6c3]")
        case (.succeeded, .upgrade): String(localized: "Updated [Extension Operations 45cb2f]")
        case (.succeeded, .uninstall): String(localized: "Uninstalled")
        case (.canceled, _): String(localized: "Canceled")
        }
    }

    /// The row's chip in the market's list: Installing / Waiting for You / Install failed.
    static func chip(_ op: ExtensionOperation) -> String? {
        switch (op.state, op.kind) {
        case (.running, .install): String(localized: "Installing")
        case (.running, .upgrade): String(localized: "Upgrading [Extension Operations e4ff1b]")
        case (.running, .uninstall): String(localized: "Uninstalling [Extension Operations ce2a05]")
        case (.needsConsent, _): String(localized: "Waiting for You")
        case (.failed, .install): String(localized: "Install failed")
        case (.failed, .upgrade): String(localized: "Upgrade failed")
        case (.failed, .uninstall): String(localized: "Uninstall failed")
        case (.succeeded, _), (.canceled, _): nil
        }
    }

    /// 1.0.0 → 1.1.0, or the version installed.
    static func versions(_ op: ExtensionOperation) -> String? {
        if op.kind == .upgrade, let from = op.from, let to = op.version { return "\(from) → \(to)" }
        return op.kind == .uninstall ? nil : op.version
    }

    /// Step 2 of 6 (the steps it skips not counted).
    static func progress(_ op: ExtensionOperation) -> String? {
        let shown = op.shownSteps
        guard !shown.isEmpty else { return nil }
        let done = shown.filter { $0.state == .done }.count
        let at = shown.firstIndex { $0.id == op.current?.id }.map { $0 + 1 } ?? done
        return String(localized: "Step \(max(at, 1)) of \(shown.count)")
    }

    /// 12s elapsed / 1m 5s elapsed.
    static func elapsed(since start: Date, now: Date) -> String {
        let s = max(0, Int(now.timeIntervalSince(start)))
        return s < 60 ? String(localized: "\(s)s elapsed") : String(localized: "\(s / 60)m \(s % 60)s elapsed")
    }

    /// What an older hub is doing while the request is out (no steps of its own).
    static func waitingNote(_ kind: ExtensionOperation.Kind) -> String {
        switch kind {
        case .install, .upgrade: String(localized: "The hub is downloading, verifying, unpacking and starting it; a large extension takes a minute or two. You can leave this page and come back to it.")
        case .uninstall: String(localized: "The hub is stopping it and deleting its files and data. You can leave this page and come back to it.")
        }
    }

    /// Who started it, when it was not this device's doing worth saying (the hub's automatic upgrade, the CLI).
    static func actor(_ op: ExtensionOperation) -> String? {
        guard let a = op.actor, !a.isEmpty else { return nil }
        return String(localized: "Started by \(a)")
    }
}
