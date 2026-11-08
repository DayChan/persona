import AppKit
import SwiftUI

/// `--snapshot <dir>`: the menu panel and the pairing window in every state, light and dark, as PNGs. They are drawn
/// offscreen from the app's own views, so no screen recording permission is needed and nothing personal shows up.
@MainActor enum Snapshots {
    static let now = Date(timeIntervalSince1970: 1_793_500_000)

    static func render(to dir: URL) {
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let endpoints = [
            LinkEndpoint(url: "https://192.0.2.10:18662", kind: "lan"),
            LinkEndpoint(url: "https://[2001:db8:1::10]:18662", kind: "ipv6"),
            LinkEndpoint(url: "https://hub.example.com:18662", kind: "name", manual: true),
        ]
        let proxied = [LinkEndpoint(url: "https://hub.example.com:443/prefix", kind: "name")]
        func status(_ state: String, _ change: (inout LinkStatus) -> Void = { _ in }) -> LinkStatus {
            var s = LinkStatus(state: state, machine: "macbook", endpoints: endpoints)
            change(&s)
            return s
        }
        func passive(_ state: String, _ change: (inout LinkStatus) -> Void = { _ in }) -> LinkStatus {
            var s = LinkStatus(state: state, machine: "mac-mini", hubName: "家里", mode: "passive", listen: ":18663",
                               addrs: ["192.0.2.20:18663", "[2001:db8:1::20]:18663"])
            change(&s)
            return s
        }
        let pairedPassive = PassivePairing(listen: ":18663", hubName: "家里", machine: "mac-mini", paired: true)
        let nodeInstalled = NodeService(installed: true, file: "~/Library/LaunchAgents/com.persona.node.plist", hasBin: true, running: "loaded")
        let menus: [(String, LinkStatus?, String?)] = [
            ("connected", status("connected") {
                $0.address = endpoints[0].url; $0.kind = "lan"; $0.since = now.addingTimeInterval(-7_380); $0.node = "online"
                $0.hubName = "家里"; $0.meshHubs = 3
            }, nil),
            ("connected-one-hub", status("connected") {
                $0.address = endpoints[0].url; $0.kind = "lan"; $0.since = now.addingTimeInterval(-7_380); $0.node = "online"
                $0.hubName = "家里"; $0.meshHubs = 1
            }, nil),
            // A hub from before hub names: the menu as it always was.
            ("connected-older-hub", status("connected") {
                $0.address = endpoints[0].url; $0.kind = "lan"; $0.since = now.addingTimeInterval(-7_380); $0.node = "online"
            }, nil),
            ("node-missing", status("connected") {
                $0.hubName = "家里"; $0.meshHubs = 1
                $0.address = endpoints[1].url; $0.kind = "ipv6"; $0.since = now.addingTimeInterval(-95); $0.node = "offline"
                $0.nodeError = "本机的常驻 node 没在运行（~/.persona/node-attach.sock 连不上）：让 personad node --attach 一直跑（scripts/deploy/node-launchd.sh install）"
            }, nil),
            ("connecting", status("connecting"), nil),
            ("waiting", status("waiting") {
                $0.hubName = "家里"
                $0.retry = now.addingTimeInterval(12)
                $0.error = "内网 192.0.2.10:18662：不可达；公网 IPv6 [2001:db8:1::10]:18662：超时；域名 hub.example.com:18662：域名解析不了"
            }, nil),
            ("refused", status("refused") { $0.error = "hub 不认这台的凭据（可能撤销了）：重新配对" }, nil),
            ("unpaired", LinkStatus(state: "unpaired"), nil),
            ("helper-down", nil, "后台程序退出了（1），正在重启"),
            // Passive mode: the hub dials this Mac.
            ("passive-connected", passive("connected") {
                $0.address = "192.0.2.10"; $0.since = now.addingTimeInterval(-7_380); $0.node = "online"
            }, nil),
            ("passive-listening", passive("listening") {
                $0.lastSeen = now.addingTimeInterval(-180); $0.from = "192.0.2.10"; $0.node = "online"
            }, nil),
            ("passive-node-missing", passive("listening") {
                $0.lastSeen = now.addingTimeInterval(-20); $0.from = "192.0.2.10"; $0.node = "offline"
                $0.nodeError = "本机的常驻 node 没在运行（~/.persona/node-attach.sock 连不上）：让 personad node --attach 一直跑"
            }, nil),
            ("passive-unpaired", LinkStatus(state: "unpaired", mode: "passive", listen: ":18663", codeExpires: now.addingTimeInterval(500)), nil),
            ("passive-waiting", passive("waiting") {
                $0.retry = now.addingTimeInterval(8); $0.error = ":18663：端口被占用了"
            }, nil),
            // Last, so the indexes above stay: a hub behind an HTTPS reverse proxy, checked with the system's CAs.
            ("connected-proxy", LinkStatus(state: "connected", machine: "macbook", address: proxied[0].url, kind: "name",
                                           since: now.addingTimeInterval(-600), node: "online", endpoints: proxied, hubName: "家里",
                                           meshHubs: 1, trust: "system"), nil),
            // Paired from a link the phone made: the hub waits for 「是我的电脑」 (pending).
            ("awaiting-confirm", status("connected") {
                $0.address = endpoints[0].url; $0.kind = "lan"; $0.since = now.addingTimeInterval(-40); $0.node = "online"
                $0.hubName = "家里"; $0.meshHubs = 1; $0.pending = true
            }, nil),
        ]
        for dark in [false, true] {
            let mode = dark ? "dark" : "light"
            for (name, s, down) in menus {
                let m = LinkModel()
                m.show(s, helperDown: down, endpoints: endpoints, machine: "macbook",
                       passive: s?.passive == true && s?.state != "unpaired" ? pairedPassive : nil, node: nodeInstalled)
                png(MenuView(model: m, now: now), dark: dark, to: dir.appendingPathComponent("menu-\(name)-\(mode).png"))
            }
            // The resident node's service is not there yet (removed, say): the menu offers to set it up.
            let noNode = LinkModel()
            noNode.show(menus[3].1, endpoints: endpoints, machine: "macbook",
                        node: NodeService(installed: false, file: nil, hasBin: false, running: nil))
            png(MenuView(model: noNode, now: now), dark: dark, to: dir.appendingPathComponent("menu-no-node-service-\(mode).png"))
            let paired = LinkModel()
            paired.show(menus[0].1, endpoints: endpoints, machine: "macbook")
            png(PairView(model: paired, result: (true, "配对好了：这台是 hub 上的「macbook」，凭据存在 ~/.persona/link.json。")),
                dark: dark, to: dir.appendingPathComponent("pair-paired-\(mode).png"))
            let fresh = LinkModel()
            fresh.show(LinkStatus(state: "unpaired"))
            png(PairView(model: fresh, link: "persona://link?url=http%3A%2F%2F192.0.2.10%3A18660&tls=18662&machine=macbook&token=jp_…",
                         result: (false, "配对失败：内网 192.0.2.10:18662：hub 回了 HTTP 401")),
                dark: dark, to: dir.appendingPathComponent("pair-error-\(mode).png"))
            png(PairView(model: paired, newAddress: "hub.example", addressError: "\"hub.example:x\" 的端口不对"),
                dark: dark, to: dir.appendingPathComponent("pair-address-error-\(mode).png"))
            // Passive mode's pairing: the link made, to paste into the hub's admin page.
            let fresh2 = LinkModel()
            fresh2.show(LinkStatus(state: "unpaired", mode: "passive", listen: ":18663"), mode: "passive")
            let inviteLink = "persona://connector?addr=192.0.2.20%3A18663&addr=%5B2001%3Adb8%3A1%3A%3A20%5D%3A18663&fp=Wm9vb3ZlcnRoZW1vb25hbmRiYWNrYWdhaW4xMjM0NTY&code=jc_ZXhhbXBsZWNvZGVvbmx5"
            png(PairView(model: fresh2, tab: "passive", invite: (inviteLink, PairView.inviteNote(link: inviteLink, stderr:
                "防火墙：只对 hub 放行 TCP 18663，别对所有人开（例如 ufw：sudo ufw allow from <hub 的地址> to any port 18663 proto tcp；firewalld、nftables、路由器同理）。"))),
                dark: dark, to: dir.appendingPathComponent("pair-passive-link-\(mode).png"))
            // Passive mode on, active mode paired before: back to it without pairing again.
            let both = LinkModel()
            both.show(menus[9].1, endpoints: endpoints, machine: "macbook", mode: "passive", passive: pairedPassive)
            png(PairView(model: both, tab: "active"), dark: dark, to: dir.appendingPathComponent("pair-switch-back-\(mode).png"))
            png(PairView(model: both, tab: "passive"), dark: dark, to: dir.appendingPathComponent("pair-passive-paired-\(mode).png"))
            let viaProxy = LinkModel()
            viaProxy.show(menus.last!.1, endpoints: proxied, machine: "macbook")
            png(PairView(model: viaProxy), dark: dark, to: dir.appendingPathComponent("pair-proxy-\(mode).png"))
            // A `persona://link?…` link opened from outside: the window filled in, waiting for 「配对」 (Pair).
            let opened = LinkModel()
            opened.show(LinkStatus(state: "unpaired"))
            opened.openLink(URL(string: "persona://link?url=http%3A%2F%2F192.0.2.10%3A18660&token=jp_ZXhhbXBsZQ&machine=macbook&hub=h_example&fp=Wm9vb3ZlcnRoZW1vb24&tls=18662")!)
            png(PairView(model: opened), dark: dark, to: dir.appendingPathComponent("pair-opened-link-\(mode).png"))
            // The phone's link (persona://pair?…) opened here: said, nothing filled in.
            let other = LinkModel()
            other.show(LinkStatus(state: "unpaired"))
            other.openLink(URL(string: "persona://pair?url=http%3A%2F%2F192.0.2.10%3A18660&token=jp_ZXhhbXBsZQ")!)
            png(PairView(model: other), dark: dark, to: dir.appendingPathComponent("pair-other-app-link-\(mode).png"))
            // A link without a machine (pasted from `personad hub pair`): the machine id under 「高级」 (Advanced).
            let fresh3 = LinkModel()
            fresh3.show(LinkStatus(state: "unpaired"))
            png(PairView(model: fresh3), dark: dark, to: dir.appendingPathComponent("pair-empty-\(mode).png"))
            // Renamed on the hub: the menu shows the name and offers to rename it; the rename window, after a rename.
            let renamed = LinkModel()
            renamed.show(status("connected") {
                $0.address = endpoints[0].url; $0.kind = "lan"; $0.since = now.addingTimeInterval(-7_380); $0.node = "online"
                $0.hubName = "家里"; $0.meshHubs = 1; $0.machineName = "工作室的 Mac"
            }, endpoints: endpoints, machine: "macbook", node: nodeInstalled)
            png(MenuView(model: renamed, now: now), dark: dark, to: dir.appendingPathComponent("menu-renamed-\(mode).png"))
            png(RenameView(model: renamed), dark: dark, to: dir.appendingPathComponent("rename-\(mode).png"))
            png(RenameView(model: renamed, name: "Studio", result: (true, "好了：这台电脑在 hub 上叫「Studio」，App、网页和管理界面里马上就会变。")),
                dark: dark, to: dir.appendingPathComponent("rename-done-\(mode).png"))
            png(icons(dark: dark), dark: dark, to: dir.appendingPathComponent("icons-\(mode).png"), plain: true)
        }
    }

    /// The menu bar icons as they look there: the template symbol in the bar's text color.
    static func icons(dark: Bool) -> some View {
        let looks: [(LinkLook, String)] = [(.connected, "已连上"), (.awaitingConfirm, "等你确认"), (.connecting, "连接中"),
                                           (.listening, "等 hub 来连"), (.down, "离线或出错"), (.unpaired, "未配对"),
                                           (.nodeMissing, "后台服务没在运行")]
        return HStack(spacing: 18) {
            ForEach(looks, id: \.1) { look, title in
                VStack(spacing: 6) {
                    Image(systemName: look.symbol)
                        .font(.system(size: 14, weight: .regular))
                        .frame(height: 22)
                    Text(title).font(.caption2).fixedSize()
                }
            }
        }
        .foregroundStyle(dark ? Color.white : Color.black.opacity(0.85))
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(dark ? Color(white: 0.17) : Color(white: 0.93))
    }

    static func png<V: View>(_ view: V, dark: Bool, to url: URL, plain: Bool = false) {
        let appearance = NSAppearance(named: dark ? .darkAqua : .aqua)!
        let root = Group {
            if plain {
                AnyView(view)
            } else {
                AnyView(view.background(Color(nsColor: .windowBackgroundColor)))
            }
        }
        .environment(\.colorScheme, dark ? .dark : .light)
        let hosting = NSHostingView(rootView: root)
        hosting.appearance = appearance
        let size = hosting.fittingSize
        let window = NSWindow(contentRect: NSRect(origin: NSPoint(x: -20_000, y: -20_000), size: size),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = appearance
        window.contentView = hosting
        hosting.frame = NSRect(origin: .zero, size: size)
        window.orderFrontRegardless()
        hosting.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.3))
        hosting.display()
        if let rep = hosting.bitmapImageRepForCachingDisplay(in: hosting.bounds) {
            hosting.cacheDisplay(in: hosting.bounds, to: rep)
            try? rep.representation(using: .png, properties: [:])?.write(to: url)
        }
        window.orderOut(nil)
    }
}
