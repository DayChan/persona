import Foundation

/// A way through to a paired hub when its own addresses do not answer: its SSH tunnel
/// (docs/operations/network.md「经 SSH 隧道连 hub」), its local SOCKS5 proxy ("Connecting to hub through a local SOCKS5
/// proxy") or, in builds that have the in-app tunnel core for a hub whose extension contributes one, its in-app tunnel
/// (「经 App 内隧道连 hub」). A hub may have several; each is tried in turn, in that order.
struct HubTunnel: Hashable {
    enum Kind: Hashable {
        case ssh
        case localProxy
        case inApp
    }

    let hub: PairedHub
    let kind: Kind

    /// Through it every time, not only when the hub's own addresses do not answer.
    var always: Bool {
        switch kind {
        case .ssh: hub.ssh?.always == true
        case .localProxy: hub.localProxy?.always == true
        case .inApp: hub.inAppTunnel?.always == true
        }
    }

    /// The group went through it last time (kept while the app was in the background).
    var recent: Bool {
        switch kind {
        case .ssh: SSHTunnels.shared.recent.contains(hub.id)
        case .localProxy: LocalProxies.shared.recent.contains(hub.id)
        case .inApp: InAppTunnels.shared.recent.contains(hub.id)
        }
    }

    /// Up and routing the hub's requests: true; it could not be made: false.
    func open() async -> Bool {
        switch kind {
        case .ssh:
            guard let route = hub.ssh else { return false }
            return (try? await SSHTunnels.shared.open(hub.id, route: route, hub: hub.config.url)) != nil
        case .localProxy:
            guard let route = hub.localProxy else { return false }
            return (try? await LocalProxies.shared.open(hub.id, route: route, hub: hub.config.url)) != nil
        case .inApp:
            guard let route = hub.inAppTunnel else { return false }
            return (try? await InAppTunnels.shared.open(hub.id, route: route, hub: hub.config.url)) != nil
        }
    }

    func close() {
        switch kind {
        case .ssh: SSHTunnels.shared.close(hub.id)
        case .localProxy: LocalProxies.shared.close(hub.id)
        case .inApp: InAppTunnels.shared.close(hub.id)
        }
    }

    func watchDirect(answers: @escaping @MainActor () async -> Bool, moveBack: @escaping @MainActor () -> Void) {
        switch kind {
        case .ssh: SSHTunnels.shared.watchDirect(hub.id, answers: answers, moveBack: moveBack)
        case .localProxy: LocalProxies.shared.watchDirect(hub.id, answers: answers, moveBack: moveBack)
        case .inApp: InAppTunnels.shared.watchDirect(hub.id, answers: answers, moveBack: moveBack)
        }
    }
}

extension AppModel {
    // MARK: Tunnels to the hub (docs/operations/network.md「经 SSH 隧道连 hub」「经 App 内隧道连 hub」)

    /// The group's tunnels ready to use, each paired hub's SSH one, then its local proxy, then its in-app one.
    func hubTunnels(_ group: String) -> [HubTunnel] {
        registry.members(group).flatMap { h -> [HubTunnel] in
            var out: [HubTunnel] = []
            if h.ssh?.isUsable == true { out.append(HubTunnel(hub: h, kind: .ssh)) }
            if h.localProxy?.isUsable == true { out.append(HubTunnel(hub: h, kind: .localProxy)) }
            if InAppTunnel.available, h.inAppTunnel?.isUsable == true { out.append(HubTunnel(hub: h, kind: .inApp)) }
            return out
        }
    }

    /// Where a group's socket goes next when it has a tunnel, step by step as `HubWay.next` says: straight to the hub
    /// when one of its addresses answers (the tunnels close), else through a tunnel; tunnels set to be used every time
    /// first. Nothing answering at all: the first address, for the socket's own retries.
    func tunnelRoute(_ group: String) async -> ServerConfig? {
        let candidates = HubRoute.candidates(registry, group: group)
        let tunnels = hubTunnels(group)
        let options = tunnels.map { HubWay.Option(always: $0.always, recent: $0.recent) }
        var seen = HubWay.Seen()
        var direct: HubRoute.Candidate?
        while true {
            switch HubWay.next(options, seen) {
            case .open(let i):
                seen.opened[i] = await tunnels[i].open()
            case .askDirect(let warm):
                // Through a tunnel last time (back from the background, a dropped socket): it starts while the direct
                // way is asked, so an address that does not answer costs no wait (up to the probe's 3 s) on top.
                let warming: Task<[Int: Bool], Never>? = warm.isEmpty ? nil : Task { @MainActor in
                    var tried: [Int: Bool] = [:]
                    for i in warm {
                        tried[i] = await tunnels[i].open()
                        if tried[i] == true { break }
                    }
                    return tried
                }
                direct = await HubRoute.answering(candidates)
                seen.direct = direct != nil
                if let tried = await warming?.value { seen.opened.merge(tried) { $1 } }
            case .direct:
                for t in tunnels { t.close() }
                return direct?.config
            case .through(let i, let watchDirect):
                let t = only(tunnels[i], of: tunnels)
                return watchDirect ? through(t, group) : t.hub.config
            case .none:
                return candidates.first?.config
            }
        }
    }

    /// `t` carries its hub's traffic: the hub's other tunnel closes (both route the same hub address in HubTransport, so
    /// the one left open would later clear the route of the one in use).
    private func only(_ t: HubTunnel, of tunnels: [HubTunnel]) -> HubTunnel {
        for o in tunnels where o.hub.id == t.hub.id && o.kind != t.kind { o.close() }
        return t
    }

    /// The group goes through `t`, asking the direct way again every so often.
    private func through(_ t: HubTunnel, _ group: String) -> ServerConfig {
        t.watchDirect(answers: { [weak self] in
            guard let self else { return false }
            return await HubRoute.answering(HubRoute.candidates(self.registry, group: group)) != nil
        }, moveBack: { [weak self] in self?.link(group)?.live.reroute() })
        return t.hub.config
    }

    /// Every tunnel of every hub closes (the app went to the background, or there is no hub left).
    func closeAllTunnels() {
        SSHTunnels.shared.closeAll()
        LocalProxies.shared.closeAll()
        InAppTunnels.shared.closeAll()
    }

    /// One paired hub's tunnels close (it is removed, or its group is).
    func closeTunnels(_ id: String) {
        SSHTunnels.shared.close(id)
        LocalProxies.shared.close(id)
        InAppTunnels.shared.close(id)
    }
}

/// How a group's next connection picks its way when its hubs have tunnels
/// (docs/operations/network.md「经 SSH 隧道连 hub」): the tunnels set to Always Use first, in order, the first that
/// comes up taken; then the hub's own addresses, while the tunnels used last time start meanwhile; an address
/// answering: straight to the hub; else the first of those to have come up, then the other tunnels in order. Going
/// through a tunnel only because the direct way did not answer, the direct way is asked again every `recheck`. Pure:
/// `AppModel.tunnelRoute` carries out each step and tells it what came of it, and the tests go through the steps by
/// hand.
nonisolated enum HubWay {
    /// How often a group that went through a tunnel because its hub did not answer asks the direct way again.
    static let recheck: Duration = .seconds(120)

    /// A tunnel as the pick sees it.
    struct Option: Hashable, Sendable {
        /// Set to Always Use.
        var always: Bool
        /// Used the last time (kept while the app was in the background).
        var recent: Bool
    }

    /// What the pick learned so far.
    struct Seen: Equatable, Sendable {
        /// By tunnel index: whether it came up.
        var opened: [Int: Bool] = [:]
        /// Whether one of the hub's own addresses answered; nil before they were asked.
        var direct: Bool?
    }

    enum Step: Equatable, Sendable {
        /// Bring up tunnel `i`.
        case open(Int)
        /// Ask the hub's own addresses, bringing up these tunnels meanwhile, in order, until one comes up.
        case askDirect(warming: [Int])
        /// Straight to the hub; every tunnel closes.
        case direct
        /// Through tunnel `i`; `watchDirect`: asking the direct way again every `recheck`.
        case through(Int, watchDirect: Bool)
        /// No way: the first address, for the socket's own retries.
        case none
    }

    static func next(_ options: [Option], _ seen: Seen) -> Step {
        for i in options.indices where options[i].always {
            switch seen.opened[i] {
            case true?: return .through(i, watchDirect: false)
            case nil: return .open(i)
            case false?: continue
            }
        }
        let fallback = options.indices.filter { !options[$0].always }
        let warm = fallback.filter { options[$0].recent }
        guard let direct = seen.direct else { return .askDirect(warming: warm) }
        if direct { return .direct }
        for i in warm + fallback.filter({ !options[$0].recent }) {
            switch seen.opened[i] {
            case true?: return .through(i, watchDirect: true)
            case nil: return .open(i)
            case false?: continue
            }
        }
        return .none
    }
}
