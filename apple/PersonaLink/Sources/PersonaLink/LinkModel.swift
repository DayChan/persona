import AppKit
import Foundation
import Network
import ServiceManagement

/// A hub address persona-link may try, as it reports them (internal/link).
struct LinkEndpoint: Decodable, Hashable, Identifiable {
    var url: String
    var kind: String
    var manual: Bool?
    var id: String { url }
}

/// One status line from `persona-link run` (internal/link Status).
struct LinkStatus: Decodable, Equatable {
    var state: String // unpaired | connecting | connected | waiting | refused
    var machine: String?
    var address: String?
    var kind: String?
    var since: Date?
    var node: String? // online | offline | unknown
    var nodeError: String?
    var error: String?
    var retry: Date?
    var endpoints: [LinkEndpoint]?
    /// What the hub calls itself, and (while connected) how many hubs its mesh has; older hubs send neither.
    var hubName: String?
    var meshHubs: Int?
    /// What the hub calls this computer (docs/operations/link.md "The computer's name"); older hubs say nothing: `machine`,
    /// its id, is shown instead.
    var machineName: String?
    /// active (this Mac dials the hub) | passive (the hub dials this Mac); older helpers say nothing: active.
    var mode: String?
    /// Passive mode: where it listens, the addresses the hub may use, when the hub last came in and from where, and
    /// until when a pairing link is good. Connected: `address` is the hub's address.
    var listen: String?
    var addrs: [String]?
    var lastSeen: Date?
    var from: String?
    var codeExpires: Date?
    /// "system": the hub sits behind an HTTPS reverse proxy and is checked with the system's CAs and its host name, not
    /// a pin (docs/operations/link.md「hub 在反向代理后面」); nil when pinned or with older helpers.
    var trust: String?
    /// The hub still waits for someone to confirm this computer in the phone App or on the web (「是我的电脑」,
    /// docs/assistant/onboarding.md 4.2). Helpers and hubs that do not say yet send nothing: shown as connected.
    var pending: Bool?
    /// The hub's "Needs you" summary (Attention.swift); helpers and hubs that do not send one leave it out. Decoded
    /// apart from the rest (`decode`), so a summary that does not decode loses only itself.
    var attention: LinkAttention? = nil

    var passive: Bool { mode == "passive" }
    var systemTrust: Bool { trust == "system" }
    /// The name to show for this computer: the hub's, else its id.
    var shownMachine: String? { machineName.flatMap { $0.isEmpty ? nil : $0 } ?? machine.flatMap { $0.isEmpty ? nil : $0 } }
    /// This computer can be renamed from here: active mode, connected to a hub that renames computers (it says the name),
    /// and confirmed (a computer still waiting to be confirmed cannot rename itself).
    var renamable: Bool { !passive && state == "connected" && !(machineName ?? "").isEmpty && pending != true }

    enum CodingKeys: String, CodingKey {
        case state, machine, address, kind, since, node, error, retry, endpoints, mode, listen, addrs, from, trust, pending
        case nodeError = "node_error"
        case hubName = "hub_name"
        case meshHubs = "mesh_hubs"
        case machineName = "machine_name"
        case lastSeen = "last_seen"
        case codeExpires = "code_expires"
    }
}

extension LinkStatus {
    /// One status line from the helper. `attention` is decoded apart, and one that does not decode keeps `previous`: it
    /// says nothing about the connection, while a missing summary starts the notifications' baseline anew.
    @MainActor static func decode(_ line: Data, previous: LinkAttention? = nil) throws -> LinkStatus {
        let decoded = try LinkModel.decoder.decode(StatusLine.self, from: line)
        var s = decoded.status
        s.attention = decoded.present && decoded.summary == nil ? previous : decoded.summary
        return s
    }
}

/// A status line in one pass: the status, and its `attention` told apart: missing (or null), a summary, or one that
/// does not decode.
private struct StatusLine: Decodable {
    var status: LinkStatus
    var present = false
    var summary: LinkAttention?

    enum CodingKeys: String, CodingKey { case attention }

    init(from decoder: Decoder) throws {
        status = try LinkStatus(from: decoder)
        let c = try decoder.container(keyedBy: CodingKeys.self)
        guard c.contains(.attention), try !c.decodeNil(forKey: .attention) else { return }
        present = true
        do {
            summary = try c.decode(LinkAttention.self, forKey: .attention)
        } catch {
            NSLog("Persona Link: a Needs you summary that does not decode: %@", String(describing: error))
        }
    }
}

/// Passive mode's part of link.json, as far as the app shows it (never the credential's hash).
struct PassivePairing: Decodable, Equatable {
    var listen: String?
    var hubName: String?
    var machine: String?
    var paired: Bool

    enum CodingKeys: String, CodingKey {
        case listen, machine
        case hubName = "hub_name"
        case tokenHash = "token_sha256"
    }

    init(listen: String? = nil, hubName: String? = nil, machine: String? = nil, paired: Bool) {
        self.listen = listen
        self.hubName = hubName
        self.machine = machine
        self.paired = paired
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        listen = try c.decodeIfPresent(String.self, forKey: .listen)
        hubName = try c.decodeIfPresent(String.self, forKey: .hubName)
        machine = try c.decodeIfPresent(String.self, forKey: .machine)
        paired = !(try c.decodeIfPresent(String.self, forKey: .tokenHash) ?? "").isEmpty
    }
}

/// The resident node's service, as `persona-link node status --json` says.
struct NodeService: Decodable, Equatable {
    var installed: Bool
    var file: String?
    var hasBin: Bool
    var running: String?

    enum CodingKeys: String, CodingKey {
        case installed, file, running
        case hasBin = "has_bin"
    }
}

/// What the menu bar shows: the four states of the link, a link that is up while the node is not or while the hub
/// waits for the user to confirm this computer, and passive mode listening for the hub.
enum LinkLook {
    case connected, awaitingConfirm, nodeMissing, connecting, listening, down, unpaired

    var symbol: String {
        switch self {
        case .connected: "personalhotspot"
        case .awaitingConfirm: "hourglass"
        case .nodeMissing: "exclamationmark.triangle"
        case .connecting: "arrow.triangle.2.circlepath"
        case .listening: "antenna.radiowaves.left.and.right"
        case .down: "personalhotspot.slash"
        case .unpaired: "link.badge.plus"
        }
    }
}

enum LinkText {
    static let kinds = ["lan": "内网", "ipv6": "公网 IPv6", "name": "域名", "ipv4": "公网 IPv4"]

    /// The host and port of a hub URL, and the path a reverse proxy puts the hub under.
    static func host(_ url: String?) -> String {
        guard let url, let c = URLComponents(string: url), let host = c.host else { return url ?? "" }
        let shown = host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
        return (c.port.map { "\(shown):\($0)" } ?? shown) + c.percentEncodedPath
    }

    /// How a hub behind an HTTPS reverse proxy is checked.
    static let systemTrust = "经反向代理 · 系统证书"

    static func duration(since: Date, now: Date) -> String {
        let s = max(0, Int(now.timeIntervalSince(since)))
        switch s {
        case ..<60: return "不到 1 分钟"
        case ..<3600: return "\(s / 60) 分钟"
        case ..<86400: return "\(s / 3600) 小时 \(s % 3600 / 60) 分钟"
        default: return "\(s / 86400) 天 \(s % 86400 / 3600) 小时"
        }
    }

    /// The hub line: its name, and how many hubs its mesh has when that is known.
    static func hub(name: String, mesh: Int?) -> String {
        switch mesh {
        case let n? where n > 1: "hub「\(name)」· 互联组共 \(n) 台 hub"
        case 1: "hub「\(name)」· 没和别的 hub 互联"
        default: "hub「\(name)」"
        }
    }

    static func until(_ date: Date, now: Date) -> String {
        let s = Int(date.timeIntervalSince(now).rounded(.up))
        if s <= 0 { return "马上重试" }
        if s < 60 { return "\(s) 秒后重试" }
        return "\(s / 60) 分钟后重试"
    }

    /// How long ago, in a few words.
    static func ago(_ date: Date, now: Date) -> String {
        let s = max(0, Int(now.timeIntervalSince(date)))
        switch s {
        case ..<60: return "刚刚"
        case ..<3600: return "\(s / 60) 分钟前"
        case ..<86400: return "\(s / 3600) 小时前"
        default: return "\(s / 86400) 天前"
        }
    }

    /// How long a pairing link is still good, in a few words.
    static func left(_ date: Date, now: Date) -> String {
        let s = Int(date.timeIntervalSince(now).rounded(.up))
        if s < 60 { return "不到 1 分钟" }
        return "\(s / 60) 分钟"
    }

    static func mode(_ passive: Bool) -> String { passive ? "让 hub 来连（被动）" : "我去连 hub（主动）" }
}

/// Runs the persona-link helper inside the app bundle, keeps it running, and turns its status lines into state.
@MainActor @Observable final class LinkModel {
    static let shared = LinkModel()

    private(set) var status: LinkStatus?
    private(set) var helperDown: String? // why the helper is not running now
    private(set) var launchAtLogin = SMAppService.mainApp.status == .enabled
    private(set) var loginNote: String?
    /// The candidates in link.json (read here, written only by the helper).
    private(set) var endpoints: [LinkEndpoint] = []
    private(set) var pairedMachine: String?
    /// The mode link.json is in, and passive mode's pairing (kept while in active mode, for switching back).
    private(set) var configMode: String?
    private(set) var passivePairing: PassivePairing?
    /// The resident node's service; nil until asked.
    private(set) var nodeService: NodeService?
    private(set) var nodeNote: String?
    /// A pairing link opened from outside (`persona://link?…`), for the pairing window to fill in; it never pairs by
    /// itself. `incomingNotice`: a link of ours that is not for this app, said in the window instead.
    private(set) var incoming: PairLink?
    private(set) var incomingNotice: String?
    /// How many times pairing ran (`persona-link pair`): only the 「配对」 (Pair) button does it.
    private(set) var pairRuns = 0
    /// The "Web address" setting: where rows and notifications open the web app, over the hub's `web_url`; "" for the
    /// hub's. Read in `start()`, so snapshots never show this Mac's own.
    private(set) var webAddress = ""
    /// The user turned this app's notifications off (System Settings › Notifications); LinkNotifier keeps it current.
    var notificationsOff = false
    /// How the model opens a URL, asks for the web address and keeps it; tests replace them.
    @ObservationIgnored var openURL: (URL) -> Void = { NSWorkspace.shared.open($0) }
    @ObservationIgnored var askForWebAddress: (() -> Void)? // nil: the Web Address window
    @ObservationIgnored var defaults = UserDefaults.standard
    /// A route a row or a notification could not open for want of a web address: opened once one is saved, dropped when
    /// the Web Address window closes without one.
    @ObservationIgnored private(set) var pendingRoute: (route: String, webURL: String?)?

    @ObservationIgnored private var process: Process?
    @ObservationIgnored private var keepStdin: Pipe?
    @ObservationIgnored private var lines = Data()
    @ObservationIgnored private var restartDelay = 1.0
    @ObservationIgnored private var startedAt = Date()
    @ObservationIgnored private var quitting = false
    @ObservationIgnored private let monitor = NWPathMonitor()
    @ObservationIgnored private var sawFirstPath = false

    nonisolated static let helperURL = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/persona-link")
    /// The device's program the app carries (persona-node), for the resident node when this Mac has none
    /// (`persona-link node install --node-bin`, docs/operations/link.md "The device's program").
    nonisolated static let nodeURL = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/persona-node")
    /// Set once the app has set up (or found) the resident node by itself: later it only offers to.
    static let nodeSetUpKey = "nodeServiceSetUp"
    nonisolated static let stateEnvironment = LinkStatePaths.serviceEnvironment(
        home: FileManager.default.homeDirectoryForCurrentUser,
        environment: ProcessInfo.processInfo.environment
    )
    nonisolated static let stateURL = LinkStatePaths.root(
        home: FileManager.default.homeDirectoryForCurrentUser,
        environment: stateEnvironment
    )
    nonisolated static var helperEnvironment: [String: String] {
        var environment = stateEnvironment
        environment["PERSONA_HOME"] = stateURL.path
        environment[LegacyNames.product.uppercased() + "_HOME"] = stateURL.path
        return environment
    }
    static let webAddressKey = "webAddress"
    nonisolated static let configURL = stateURL.appendingPathComponent("link.json")
    nonisolated static let logURL = stateURL.appendingPathComponent("link.log")

    var look: LinkLook {
        if helperDown != nil { return .down }
        switch status?.state {
        case "connected", "listening":
            if status?.pending == true { return .awaitingConfirm }
            if status?.node == "offline" { return .nodeMissing }
            return status?.state == "connected" ? .connected : .listening
        case "connecting", nil: return .connecting
        case "unpaired": return .unpaired
        default: return .down
        }
    }

    /// Passive mode: what the helper says, else what link.json says.
    var passive: Bool { status?.mode.map { $0 == "passive" } ?? (configMode == "passive") }

    func start() {
        webAddress = defaults.string(forKey: Self.webAddressKey) ?? ""
        LinkNotifier.shared.start(open: { [weak self] route, webURL in self?.open(route: route, webURL: webURL) },
                                  allowed: { [weak self] in self?.notificationsOff = !$0 })
        readConfig()
        startHelper()
        Task { await setUpNodeOnce() }
        monitor.pathUpdateHandler = { _ in
            Task { @MainActor in LinkModel.shared.networkChanged() }
        }
        monitor.start(queue: .main)
    }

    /// Used by the snapshots: a status without a helper.
    func show(_ status: LinkStatus?, helperDown: String? = nil, endpoints: [LinkEndpoint] = [], machine: String? = nil,
              mode: String? = nil, passive: PassivePairing? = nil, node: NodeService? = nil, webAddress: String = "",
              notificationsOff: Bool = false) {
        self.status = status
        self.webAddress = webAddress
        self.notificationsOff = notificationsOff
        self.helperDown = helperDown
        self.endpoints = endpoints
        self.pairedMachine = machine
        self.configMode = mode ?? status?.mode
        self.passivePairing = passive
        self.nodeService = node
    }

    private func startHelper() {
        guard !quitting else { return }
        let p = Process()
        p.executableURL = Self.helperURL
        p.environment = Self.helperEnvironment
        p.arguments = ["run", "--parent"] // exits when its stdin closes: when this app goes away, however it does
        let input = Pipe(), output = Pipe()
        p.standardInput = input
        p.standardOutput = output
        p.standardError = logHandle()
        output.fileHandleForReading.readabilityHandler = { handle in
            let chunk = handle.availableData
            guard !chunk.isEmpty else { return }
            Task { @MainActor in LinkModel.shared.consume(chunk) }
        }
        p.terminationHandler = { proc in
            let code = proc.terminationStatus
            Task { @MainActor in LinkModel.shared.helperExited(code) }
        }
        do {
            try p.run()
            process = p
            keepStdin = input
            startedAt = Date()
            helperDown = nil
        } catch {
            helperDown = "后台程序启动不了：\(error.localizedDescription)"
            scheduleRestart()
        }
    }

    private func logHandle() -> FileHandle {
        let fm = FileManager.default
        try? fm.createDirectory(at: Self.logURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let size = try? fm.attributesOfItem(atPath: Self.logURL.path)[.size] as? Int, size > 1 << 20 {
            try? fm.removeItem(at: Self.logURL)
        }
        if !fm.fileExists(atPath: Self.logURL.path) {
            fm.createFile(atPath: Self.logURL.path, contents: nil, attributes: [.posixPermissions: 0o600])
        }
        guard let h = try? FileHandle(forWritingTo: Self.logURL) else { return FileHandle.nullDevice }
        h.seekToEndOfFile()
        return h
    }

    private func consume(_ chunk: Data) {
        lines.append(chunk)
        while let nl = lines.firstIndex(of: 0x0A) {
            let line = lines[lines.startIndex..<nl]
            lines.removeSubrange(lines.startIndex...nl)
            if let s = try? LinkStatus.decode(line, previous: status?.attention) {
                if s.endpoints != status?.endpoints || s.machine != status?.machine || s.mode != status?.mode
                    || s.hubName != status?.hubName || s.state != status?.state { readConfig() }
                setStatus(s)
            }
        }
    }

    private func helperExited(_ code: Int32) {
        process = nil
        keepStdin = nil
        var s = status
        s?.attention = nil // the summary belonged to the helper's connection
        setStatus(s)
        guard !quitting else { return }
        helperDown = "后台程序退出了（\(code)），正在重启"
        if Date().timeIntervalSince(startedAt) > 60 { restartDelay = 1 }
        scheduleRestart()
    }

    /// The helper's status changed: the notifications follow its summary.
    private func setStatus(_ s: LinkStatus?) {
        if s?.attention != status?.attention { LinkNotifier.shared.apply(s?.attention) }
        status = s
    }

    private func scheduleRestart() {
        let delay = restartDelay
        restartDelay = min(restartDelay * 2, 30)
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self] in self?.startHelper() }
    }

    private func signal(_ sig: Int32) {
        if let p = process, p.isRunning { kill(p.processIdentifier, sig) }
    }

    /// The network changed: the helper looks at its link at once instead of waiting for the next heartbeat.
    private func networkChanged() {
        if sawFirstPath { signal(SIGUSR1) }
        sawFirstPath = true
    }

    func reconnect() { signal(SIGUSR2) }

    // MARK: the web app

    /// Where the web app is: the setting, else the hub's `web_url`, else `hubWebURL` (the one a notification carries,
    /// for when there is no summary now).
    func webBase(hubWebURL: String? = nil) -> String? {
        AttentionURL.base(setting: webAddress, webURL: status?.attention?.webURL, hubWebURL)
    }

    /// Opens `route` of the web app in the default browser; without a web address, asks for one and opens it then.
    /// `webURL`: the hub's web address a notification carries.
    /// The latest click wins: one that opens drops a route still waiting for an address.
    func open(route: String, webURL: String? = nil) {
        if openWeb(route: route, webURL: webURL) {
            pendingRoute = nil
        } else {
            pendingRoute = (route, webURL)
            requestWebAddress()
        }
    }

    func requestWebAddress() {
        if let askForWebAddress { askForWebAddress() } else { WebAddressWindow.show(model: self) }
    }

    func forgetPendingRoute() { pendingRoute = nil }

    private func openWeb(route: String, webURL: String?) -> Bool {
        guard let url = AttentionURL.web(base: webBase(hubWebURL: webURL), route: route) else { return false }
        openURL(url)
        return true
    }

    /// `value`: as `AttentionURL.setting` made it ("" for the hub's). A route waiting for it opens now.
    func setWebAddress(_ value: String) {
        webAddress = value
        if webAddress.isEmpty {
            defaults.removeObject(forKey: Self.webAddressKey)
        } else {
            defaults.set(webAddress, forKey: Self.webAddressKey)
        }
        if let p = pendingRoute, openWeb(route: p.route, webURL: p.webURL) { pendingRoute = nil }
    }

    /// System Settings › Notifications at this app.
    func openNotificationSettings() {
        let id = Bundle.main.bundleIdentifier ?? ""
        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=\(id)") {
            openURL(url)
        }
    }

    func setLaunchAtLogin(_ on: Bool) {
        do {
            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            loginNote = nil
        } catch {
            loginNote = "改不了：\(error.localizedDescription)"
        }
        launchAtLogin = SMAppService.mainApp.status == .enabled
        if on && SMAppService.mainApp.status == .requiresApproval {
            loginNote = "要在 系统设置 › 通用 › 登录项 里允许"
        }
    }

    func quit() {
        quitting = true
        process?.terminate()
        NSApp.terminate(nil)
    }

    func stopHelper() {
        quitting = true
        process?.terminate()
    }

    // MARK: pairing and addresses: one-off runs of the helper, which owns link.json

    /// A `persona://` / `persona://` link opened from outside: the pairing window fills in a link for this app and says
    /// what a link for another one is. Nothing is sent to any hub here.
    func openLink(_ url: URL) {
        let parsed = PairLink.parse(url)
        if case .link(let l) = parsed {
            incoming = l
            incomingNotice = nil
        } else if let notice = PairLink.notice(for: parsed) {
            incoming = nil
            incomingNotice = notice
        }
    }

    /// The pairing window took the incoming link into its field.
    func clearIncoming() { incoming = nil }

    /// The notice about a link for another app goes once the user pairs or opens a link for this one.
    func clearNotice() { incomingNotice = nil }

    func pair(link: String, machine: String) async -> (ok: Bool, message: String) {
        pairRuns += 1
        incomingNotice = nil
        var args = ["pair"]
        let id = machine.trimmingCharacters(in: .whitespacesAndNewlines)
        if !id.isEmpty { args += ["--machine", id] }
        args.append("-")
        let r = await Self.runHelper(args, stdin: link)
        readConfig()
        if r.ok { signal(SIGUSR1) }
        return r
    }

    func addAddress(_ raw: String) async -> (ok: Bool, message: String) {
        let r = await Self.runHelper(["address", "add", raw.trimmingCharacters(in: .whitespacesAndNewlines)], stdin: nil)
        readConfig()
        return r
    }

    func removeAddress(_ url: String) async {
        _ = await Self.runHelper(["address", "remove", url], stdin: nil)
        readConfig()
    }

    func readConfig() {
        struct File: Decodable {
            var machine: String?
            var token: String?
            var endpoints: [LinkEndpoint]?
            var mode: String?
            var passive: PassivePairing?
        }
        guard let data = try? Data(contentsOf: Self.configURL), let f = try? JSONDecoder().decode(File.self, from: data) else {
            endpoints = []
            pairedMachine = nil
            configMode = nil
            passivePairing = nil
            return
        }
        endpoints = Self.ranked(f.endpoints ?? [])
        pairedMachine = (f.token ?? "").isEmpty ? nil : f.machine
        configMode = f.mode
        passivePairing = f.passive
    }

    // MARK: passive mode

    /// `persona-link pair --listen`: the link for the hub's admin page (stdout), or why not.
    func pairListen(listen: String) async -> (ok: Bool, message: String, link: String?) {
        var args = ["pair", "--listen"]
        let l = listen.trimmingCharacters(in: .whitespacesAndNewlines)
        if !l.isEmpty { args.append(l) }
        let r = await Self.runHelper(args, stdin: nil, split: true)
        readConfig()
        guard r.ok, PairLink.isConnectorOutput(r.out) else { return (false, r.message, nil) }
        return (true, r.err, r.out)
    }

    /// `persona-link name <name>` ("" is the id again: `--reset`): the running helper asks the hub over its link, and says
    /// what the hub calls this computer now, or why not.
    func rename(_ name: String) async -> (ok: Bool, message: String) {
        let n = name.trimmingCharacters(in: .whitespacesAndNewlines)
        return await Self.runHelper(["name", n.isEmpty ? "--reset" : n], stdin: nil)
    }

    /// `persona-link mode <mode>`: switches without pairing again; the helper follows within a second.
    func setMode(_ mode: String) async -> (ok: Bool, message: String) {
        let r = await Self.runHelper(["mode", mode], stdin: nil)
        readConfig()
        return (r.ok, r.message)
    }

    // MARK: the resident node

    func refreshNodeService() async {
        let r = await Self.runHelper(["node", "status", "--json"], stdin: nil, split: true)
        nodeService = try? JSONDecoder().decode(NodeService.self, from: Data(r.out.utf8))
    }

    /// `persona-link node install --node-bin <the app's persona-node>`: the resident node's launchd agent, unless there
    /// is one.
    func installNode() async {
        nodeNote = nil
        let r = await Self.runHelper(["node", "install", "--node-bin", Self.nodeURL.path], stdin: nil)
        if !r.ok { nodeNote = r.message }
        await refreshNodeService()
    }

    /// The resident node's service is there but not running: set it up again (`node uninstall` keeps the node program
    /// in ~/.persona/bin, `node install` loads the service anew).
    func reinstallNode() async {
        nodeNote = nil
        _ = await Self.runHelper(["node", "uninstall"], stdin: nil)
        await installNode()
        if nodeNote == nil { signal(SIGUSR1) }
    }

    /// The first time the app runs it sets up the resident node itself (one app is the whole node service); after
    /// that it only offers to, so a node service someone removed on purpose stays removed.
    private func setUpNodeOnce() async {
        await refreshNodeService()
        guard !UserDefaults.standard.bool(forKey: Self.nodeSetUpKey), let s = nodeService else { return }
        if !s.installed && FileManager.default.isExecutableFile(atPath: Self.nodeURL.path) { await installNode() }
        if nodeService?.installed == true { UserDefaults.standard.set(true, forKey: Self.nodeSetUpKey) }
    }

    /// The order persona-link tries them in (internal/link ranked).
    static func ranked(_ eps: [LinkEndpoint]) -> [LinkEndpoint] {
        // Same URL may arrive both manually and from the hub; keep the first occurrence like the Go reference (manual
        // ones are ordered first upstream), then rank.
        var seen = Set<String>()
        let unique = eps.filter { seen.insert($0.url).inserted }
        let rank = ["lan": 0, "ipv6": 1, "name": 2]
        return unique.enumerated().sorted { a, b in
            let ra = rank[a.element.kind] ?? 3, rb = rank[b.element.kind] ?? 3
            if ra != rb { return ra < rb }
            let ma = a.element.manual == true, mb = b.element.manual == true
            if ma != mb { return ma }
            return a.offset < b.offset
        }.map(\.element)
    }

    nonisolated static func runHelper(_ args: [String], stdin: String?) async -> (ok: Bool, message: String) {
        let r = await runHelper(args, stdin: stdin, split: true)
        return (r.ok, r.message)
    }

    /// Runs the helper once: `message` is its stdout when it worked, else its stderr (or stdout); `out` and `err` apart.
    nonisolated static func runHelper(_ args: [String], stdin: String?, split: Bool) async
        -> (ok: Bool, message: String, out: String, err: String) {
        await runHelper(executable: helperURL, arguments: args, stdin: stdin, environment: helperEnvironment, split: split)
    }

    /// Runs an executable once with the given environment (testable seam): `message` is its stdout when it worked,
    /// else its stderr (or stdout); `out` and `err` apart.
    nonisolated static func runHelper(executable: URL, arguments: [String], stdin: String?, environment: [String: String],
                                     split: Bool) async
        -> (ok: Bool, message: String, out: String, err: String) {
        await withCheckedContinuation { done in
            let p = Process()
            p.executableURL = executable
            p.environment = environment
            p.arguments = arguments
            let out = Pipe(), err = Pipe(), input = Pipe()
            p.standardOutput = out
            p.standardError = err
            p.standardInput = input
            // The termination handler runs on a background queue while launch/stdin errors throw on the caller's
            // queue, so both finishing paths go through this once-guard: the continuation resumes exactly once.
            let once = Once()
            let finish: @Sendable (Bool, String, String, String) -> Void = { ok, message, o, e in
                guard once.claim() else { return }
                done.resume(returning: (ok, message, o, e))
            }
            p.terminationHandler = { proc in
                let o = (String(data: out.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? "")
                    .trimmingCharacters(in: .whitespacesAndNewlines)
                let e = (String(data: err.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? "")
                    .trimmingCharacters(in: .whitespacesAndNewlines)
                let ok = proc.terminationStatus == 0
                finish(ok, ok ? o : (e.isEmpty ? o : e), o, e)
            }
            do {
                try p.run()
            } catch {
                finish(false, "后台程序启动不了：\(error.localizedDescription)", "", "")
                return
            }
            if let stdin {
                do {
                    try Self.writeAll(input.fileHandleForWriting, Data(stdin.utf8))
                } catch {
                    // The helper quit before reading stdin (broken pipe). Its termination handler may still win
                    // the race; if it does not, say the send failed rather than blaming the launch.
                    finish(false, "给后台程序发送数据失败：\(error.localizedDescription)", "", "")
                }
            }
            try? input.fileHandleForWriting.close()
        }
    }

    /// GUI apps launched from the Finder already run with SIGPIPE ignored; a CLI launch (debugger, xctest) does
    /// not, where a broken-pipe helper write would kill the process. Install the GUI disposition once.
    nonisolated private static let installIgnoredSigpipe: Void = {
        Darwin.signal(SIGPIPE, SIG_IGN)
    }()

    /// Writes every byte to a pipe.
    ///
    /// `FileHandle.write` raises an uncatchable ObjC exception on EPIPE (with SIGPIPE ignored, the write fails
    /// with EPIPE rather than raising a signal), so go through `write(2)` and let a helper that exits before
    /// reading its stdin fail as a normal Swift error.
    nonisolated static func writeAll(_ handle: FileHandle, _ data: Data) throws {
        _ = installIgnoredSigpipe
        let fd = handle.fileDescriptor
        try data.withUnsafeBytes { raw in
            guard let base = raw.bindMemory(to: UInt8.self).baseAddress else { return }
            var sent = 0
            while sent < raw.count {
                let written = Darwin.write(fd, base.advanced(by: sent), raw.count - sent)
                if written < 0 {
                    if errno == EINTR { continue }
                    throw NSError(domain: NSPOSIXErrorDomain, code: Int(errno))
                }
                sent += written
            }
        }
    }

    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        nonisolated(unsafe) let precise = ISO8601DateFormatter()
        precise.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        nonisolated(unsafe) let plain = ISO8601DateFormatter()
        d.dateDecodingStrategy = .custom { decoder in
            let s = try decoder.singleValueContainer().decode(String.self)
            // Go writes up to nanoseconds; ISO8601DateFormatter takes at most milliseconds.
            let trimmed = s.replacingOccurrences(of: #"(\.\d{3})\d+"#, with: "$1", options: .regularExpression)
            if let date = precise.date(from: trimmed) ?? plain.date(from: trimmed) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "bad date \(s)"))
        }
        return d
    }()
}

/// A thread-safe one-shot gate: only the first claim wins.
///
/// A `Process` runs its `terminationHandler` on a background queue while a launch or stdin error throws on the
/// caller's queue, so code that must finish exactly once (a checked continuation) claims it through this gate.
final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var claimed = false

    /// Returns true for exactly one caller, ever; every later claim returns false.
    func claim() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if claimed { return false }
        claimed = true
        return true
    }
}
