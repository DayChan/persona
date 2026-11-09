import ServiceManagement
import SwiftUI

/// The menu bar panel: one status block, then menu-like rows.
struct MenuView: View {
    let model: LinkModel
    var now: Date? = nil // fixed in snapshots
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider().padding(.vertical, 6)
            StatusBlock(model: model, now: now)
                .padding(.horizontal, 14)
                .padding(.bottom, 4)
            Divider().padding(.vertical, 6)
            if let attention = model.status?.attention {
                NeedsYouSection(model: model, attention: attention)
                Divider().padding(.vertical, 6)
            }
            VStack(spacing: 2) {
                if !model.passive { // passive mode: the hub dials, there is nothing to dial again from here
                    MenuRow(title: "立即重连", symbol: "arrow.clockwise", disabled: !canReconnect) { model.reconnect() }
                }
                MenuRow(title: model.look == .unpaired ? "粘贴配对链接…" : "配对 / 修改连接…", symbol: "link") {
                    openWindow(id: "pair")
                    NSApp.activate(ignoringOtherApps: true)
                }
                if model.status?.renamable == true {
                    MenuRow(title: "给这台电脑改名…", symbol: "pencil") {
                        openWindow(id: "rename")
                        NSApp.activate(ignoringOtherApps: true)
                    }
                }
                if let node = model.nodeService, !node.installed {
                    MenuRow(title: "安装本机 node 服务", symbol: "square.and.arrow.down") {
                        Task { await model.installNode() }
                    }
                } else if model.look == .nodeMissing {
                    // Installed but not running (or not reachable): set the service up again.
                    MenuRow(title: "重新安装", symbol: "arrow.down.circle") {
                        Task { await model.reinstallNode() }
                    }
                }
                if let note = model.nodeNote {
                    Text(note).font(.caption).foregroundStyle(.secondary).padding(.leading, 32).padding(.trailing, 8)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if model.status?.attention != nil || !model.webAddress.isEmpty {
                    MenuRow(title: linkText("Set Web Address…"), symbol: "globe") { model.requestWebAddress() }
                }
                LoginRow(model: model)
            }
            .padding(.horizontal, 6)
            Divider().padding(.vertical, 6)
            MenuRow(title: "退出 Persona Link", symbol: "power", shortcut: "⌘Q") { model.quit() }
                .keyboardShortcut("q")
                .padding(.horizontal, 6)
                .padding(.bottom, 6)
        }
        .frame(width: 300)
        .task {
            if now == nil { await model.refreshNodeService() } // not in snapshots: they show what they were given
        }
        .task {
            if now == nil && model.status?.attention != nil { await LinkNotifier.shared.refreshAuthorization() }
        }
    }

    private var canReconnect: Bool {
        model.helperDown == nil && model.status != nil && model.status?.state != "unpaired"
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: model.look.symbol)
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(StatusBlock.tint(model.look))
                .frame(width: 20)
            Text("Persona Link").font(.headline)
            Spacer(minLength: 8)
            StatePill(look: model.look)
        }
        .padding(.horizontal, 14)
        .padding(.top, 12)
    }
}

struct StatePill: View {
    let look: LinkLook

    var body: some View {
        Text(title)
            .font(.caption.weight(.semibold))
            .lineLimit(1)
            .padding(.horizontal, 8)
            .padding(.vertical, 2)
            .foregroundStyle(StatusBlock.tint(look))
            .background(StatusBlock.tint(look).opacity(0.14), in: Capsule())
    }

    private var title: String {
        switch look {
        case .connected: "已连上"
        case .awaitingConfirm: "等你确认"
        case .nodeMissing: "后台服务没在运行"
        case .connecting: "连接中"
        case .listening: "等 hub 来连"
        case .down: "离线"
        case .unpaired: "未配对"
        }
    }
}

/// What the link is doing, in two or three short lines.
struct StatusBlock: View {
    let model: LinkModel
    var now: Date?

    static func tint(_ look: LinkLook) -> Color {
        switch look {
        case .connected: .green
        case .awaitingConfirm, .nodeMissing: .orange
        case .connecting, .listening: .blue
        case .down: .red
        case .unpaired: .secondary
        }
    }

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            let t = now ?? context.date
            VStack(alignment: .leading, spacing: 5) {
                ForEach(Array(lines(at: t).enumerated()), id: \.offset) { _, line in
                    Label {
                        Text(line.text)
                            .font(line.primary ? .body : .callout)
                            .foregroundStyle(line.primary ? Color.primary : Color.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                            .textSelection(.enabled)
                    } icon: {
                        Image(systemName: line.symbol)
                            .foregroundStyle(line.tint ?? Color.secondary)
                            .frame(width: 16)
                    }
                    .labelStyle(StatusLabelStyle())
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    struct Line {
        var symbol: String
        var text: String
        var primary = false
        var tint: Color? = nil
    }

    func lines(at now: Date) -> [Line] {
        if let down = model.helperDown {
            return [Line(symbol: "exclamationmark.octagon", text: down, primary: true, tint: .red)]
        }
        guard let s = model.status else {
            return [Line(symbol: "hourglass", text: "正在启动…", primary: true)]
        }
        if s.passive { return passiveLines(s, at: now) }
        var out = activeLines(s, at: now)
        if s.state != "unpaired" && out.count > 0 { // which mode, second: the first line is what matters most
            // Third when the second says what to do about the first (confirm on the phone, set the service up again).
            let explained = s.state == "connected" && (s.pending == true || s.node == "offline")
            out.insert(Line(symbol: "arrow.up.right.circle", text: LinkText.mode(false)), at: min(explained ? 2 : 1, out.count))
        }
        return out
    }

    func activeLines(_ s: LinkStatus, at now: Date) -> [Line] {
        let machine = s.shownMachine.map { Line(symbol: "laptopcomputer", text: "这台是 hub 上的「\($0)」") }
        // Which hub, from hubs that say (older ones do not: no line then); its mesh only while connected.
        let hubName = s.hubName.flatMap { $0.isEmpty ? nil : $0 }
        let hub = hubName.map { Line(symbol: "server.rack", text: LinkText.hub(name: $0, mesh: s.state == "connected" ? s.meshHubs : nil)) }
        switch s.state {
        case "connected" where s.pending == true:
            // Paired from a link the phone or the web made: the hub keeps this computer out until it is confirmed there.
            return [Line(symbol: "hourglass", text: "等你在手机上确认", primary: true, tint: .orange),
                    Line(symbol: "iphone", text: "在手机 App 里点「是我的电脑」"),
                    Line(symbol: "point.3.connected.trianglepath.dotted",
                         text: "\(LinkText.kinds[s.kind ?? ""] ?? "地址") · \(LinkText.host(s.address))")]
                + [hub, machine].compactMap { $0 }
        case "connected":
            var out = [Line(symbol: "checkmark.circle.fill", text: "已连上 hub", primary: true, tint: .green),
                       Line(symbol: "point.3.connected.trianglepath.dotted",
                            text: "\(LinkText.kinds[s.kind ?? ""] ?? "地址") · \(LinkText.host(s.address))")]
            if s.systemTrust { out.append(Line(symbol: "lock.shield", text: LinkText.systemTrust)) }
            if let hub { out.append(hub) }
            if let since = s.since {
                out.append(Line(symbol: "clock", text: "已连接 \(LinkText.duration(since: since, now: now))"))
            }
            switch s.node {
            case "online":
                out.append(Line(symbol: "checkmark.circle", text: "本机 node 在线", tint: .green))
            case "offline":
                // The first line says it: what is wrong is on this computer, not the link.
                out[0] = Line(symbol: "exclamationmark.triangle", text: "这台电脑上的 Persona 后台服务没在运行", primary: true, tint: .orange)
                out.insert(Line(symbol: "info.circle", text: "连上了 hub，但 hub 用不上这台电脑：点下面的「重新安装」"), at: 1)
                if let e = s.nodeError, !e.isEmpty { out.append(Line(symbol: "info.circle", text: e)) }
            default:
                out.append(Line(symbol: "ellipsis.circle", text: "hub 正在连本机的 node…"))
            }
            return out + [machine].compactMap { $0 }
        case "connecting":
            return [Line(symbol: "arrow.triangle.2.circlepath", text: "连接中：正在找 hub…", primary: true, tint: .blue)] + [hub, machine].compactMap { $0 }
        case "unpaired":
            return [Line(symbol: "link.badge.plus", text: "还没连上 hub", primary: true),
                    Line(symbol: "info.circle", text: "在手机或网页的「添加电脑」里拿到配对链接，点「粘贴配对链接…」，或者直接点开那条链接")]
        case "refused":
            return [Line(symbol: "lock.slash", text: s.error ?? "hub 不认这台的凭据：重新配对", primary: true, tint: .red)] + [hub, machine].compactMap { $0 }
        default: // waiting
            var out = [Line(symbol: "personalhotspot.slash", text: "离线" + (s.retry.map { " · " + LinkText.until($0, now: now) } ?? ""),
                            primary: true, tint: .red)]
            // persona-link joins the failures of the addresses it tried with 「；」: one line each.
            for part in (s.error ?? "").split(separator: "；") where !part.isEmpty {
                out.append(Line(symbol: "xmark.circle", text: String(part)))
            }
            return out + [hub, machine].compactMap { $0 }
        }
    }
}

extension StatusBlock {
    /// Passive mode: the hub dials this Mac. Which mode and where it listens first, then what the hub did last.
    func passiveLines(_ s: LinkStatus, at now: Date) -> [Line] {
        let listen = s.listen ?? model.passivePairing?.listen ?? ":18663"
        let mode = Line(symbol: "arrow.down.left.circle", text: "\(LinkText.mode(true)) · 监听 \(listen)")
        let machine = s.shownMachine.map { Line(symbol: "laptopcomputer", text: "这台是 hub 上的「\($0)」") }
        let hub = s.hubName.flatMap { $0.isEmpty ? nil : $0 }.map { Line(symbol: "server.rack", text: LinkText.hub(name: $0, mesh: nil)) }
        let seen = s.lastSeen.map {
            Line(symbol: "clock", text: "上一次 hub 连进来：\(LinkText.ago($0, now: now))" + (s.from.map { " · 来自 \($0)" } ?? ""))
        }
        var node: [Line] = []
        if s.node == "offline" {
            node.append(Line(symbol: "exclamationmark.triangle", text: "hub 连进来了，但本机的 node 没在运行", tint: .orange))
            if let e = s.nodeError, !e.isEmpty { node.append(Line(symbol: "info.circle", text: e)) }
        }
        switch s.state {
        case "connected":
            var out = [Line(symbol: "point.3.connected.trianglepath.dotted", text: "hub 已连进来 · 来自 \(s.address ?? "")", primary: true, tint: .green), mode]
            if let hub { out.append(hub) }
            if let since = s.since { out.append(Line(symbol: "clock", text: "已连接 \(LinkText.duration(since: since, now: now))")) }
            out.append(s.node == "online" ? Line(symbol: "checkmark.circle", text: "本机 node 在线", tint: .green) : node.first
                ?? Line(symbol: "ellipsis.circle", text: "hub 正在连本机的 node…"))
            return out + [machine].compactMap { $0 }
        case "listening":
            return [Line(symbol: "antenna.radiowaves.left.and.right", text: "在监听，等 hub 连进来", primary: true, tint: .blue), mode]
                + [hub, seen].compactMap { $0 } + node + [machine].compactMap { $0 }
        case "unpaired":
            var out = [Line(symbol: "link.badge.plus", text: "在监听，等 hub 来配对", primary: true), mode]
            if let until = s.codeExpires, until > now {
                out.append(Line(symbol: "hourglass", text: "配对链接还剩 \(LinkText.left(until, now: now))：粘贴到 hub 管理界面「电脑 › 添加电脑 › hub 来连」"))
            } else {
                out.append(Line(symbol: "info.circle", text: "点「配对 / 修改连接…」生成给 hub 的配对链接"))
            }
            return out
        default: // waiting: cannot listen
            var out = [Line(symbol: "personalhotspot.slash", text: "监听不了" + (s.retry.map { " · " + LinkText.until($0, now: now) } ?? ""),
                            primary: true, tint: .red), mode]
            for part in (s.error ?? "").split(separator: "；") where !part.isEmpty {
                out.append(Line(symbol: "xmark.circle", text: String(part)))
            }
            return out + [hub, machine].compactMap { $0 }
        }
    }
}

struct StatusLabelStyle: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            configuration.icon
            configuration.title
        }
    }
}

/// A row that looks and behaves like a menu item: highlighted under the pointer, one line.
struct MenuRow: View {
    let title: String
    let symbol: String
    var shortcut: String? = nil
    var disabled = false
    let action: () -> Void

    var body: some View {
        MenuItem(disabled: disabled, action: action) { hover in
            HStack(spacing: 8) {
                Image(systemName: symbol).frame(width: 16)
                Text(title).lineLimit(1)
                Spacer(minLength: 8)
                if let shortcut { Text(shortcut).foregroundStyle(Color.menuSecondary(hover)) }
            }
        }
    }
}

/// A menu item's look and behavior around its content: highlighted under the pointer (`content` gets whether it is).
struct MenuItem<Content: View>: View {
    var disabled = false
    let action: () -> Void
    @ViewBuilder let content: (_ hover: Bool) -> Content
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            content(hover && !disabled)
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
                .foregroundStyle(hover && !disabled ? Color.white : (disabled ? Color.secondary : Color.primary))
                .background(hover && !disabled ? Color.accentColor : Color.clear, in: RoundedRectangle(cornerRadius: 5))
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .onHover { hover = $0 }
    }
}

extension Color {
    /// Secondary text in a menu item, highlighted or not.
    static func menuSecondary(_ hover: Bool) -> Color { hover ? .white.opacity(0.8) : .secondary }
}

struct LoginRow: View {
    let model: LinkModel

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            MenuRow(title: "登录时启动", symbol: model.launchAtLogin ? "checkmark.square" : "square") {
                model.setLaunchAtLogin(!model.launchAtLogin)
            }
            if let note = model.loginNote {
                Text(note).font(.caption).foregroundStyle(.secondary).padding(.leading, 32)
            }
        }
    }
}

/// The hub's "Needs you" summary: the first sessions waiting on you, each opening in the web app, then how many more.
struct NeedsYouSection: View {
    let model: LinkModel
    let attention: LinkAttention

    /// The rows the menu lists; the rest are counted in the last row.
    static let shown = 5

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 8) {
                Text(linkText("Needs You")).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                Spacer(minLength: 8)
                if let count = LinkAttention.countText(attention.needsYou) {
                    Text(count)
                        .font(.caption.weight(.semibold).monospacedDigit())
                        .foregroundStyle(.white)
                        .padding(.horizontal, 6)
                        .background(Color.red, in: Capsule())
                }
            }
            .padding(.horizontal, 8)
            .padding(.bottom, 2)
            if attention.items.isEmpty && attention.more == 0 {
                Label(linkText("Nothing needs you right now"), systemImage: "checkmark.circle")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .labelStyle(StatusLabelStyle())
                    .padding(.horizontal, 8)
                    .padding(.vertical, 2)
            }
            ForEach(attention.items.prefix(Self.shown)) { item in
                AttentionRow(item: item) { model.open(route: item.route) }
            }
            let hidden = attention.hidden(shown: Self.shown)
            if hidden > 0 {
                MenuRow(title: linkText("And \(hidden) More…"), symbol: "ellipsis.circle") { model.open(route: "/") }
            }
            if model.notificationsOff {
                note(linkText("Notifications are off for Persona Link."), symbol: "bell.slash")
                MenuRow(title: linkText("Turn On Notifications…"), symbol: "gearshape") { model.openNotificationSettings() }
            }
            if model.webBase() == nil {
                note(linkText("The hub does not say where its web app is: set its web address to open these."), symbol: "globe")
            }
        }
        .padding(.horizontal, 6)
    }

    private func note(_ text: String, symbol: String) -> some View {
        Label {
            Text(text).fixedSize(horizontal: false, vertical: true)
        } icon: {
            Image(systemName: symbol).frame(width: 16)
        }
        .labelStyle(StatusLabelStyle())
        .font(.caption)
        .foregroundStyle(.secondary)
        .padding(.horizontal, 8)
        .padding(.top, 4)
    }
}

/// One session in "Needs you": its title, the card in a few words, where it runs; a muted one dimmed, with a muted bell.
/// The hub clips title and place to 80 characters and the detail to 60: an English title fits its three lines; a longer
/// one ends in an ellipsis, the whole of it in the tooltip.
struct AttentionRow: View {
    let item: LinkAttention.Item
    let action: () -> Void

    var body: some View {
        MenuItem(action: action) { hover in
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Image(systemName: item.symbol).frame(width: 16)
                VStack(alignment: .leading, spacing: 1) {
                    HStack(alignment: .firstTextBaseline, spacing: 4) {
                        Text(item.title.isEmpty ? linkText("Needs You") : item.title)
                            .lineLimit(3)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        if item.muted {
                            Image(systemName: "bell.slash").font(.caption)
                                .accessibilityLabel(linkText("Muted"))
                        }
                    }
                    Text(item.line).font(.callout).lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                        .foregroundStyle(Color.menuSecondary(hover))
                    if !item.place.isEmpty {
                        Text(item.place).font(.caption).lineLimit(1).truncationMode(.middle)
                            .foregroundStyle(Color.menuSecondary(hover))
                    }
                }
            }
            .opacity(item.muted && !hover ? 0.6 : 1)
        }
        .help([item.title, item.line, item.place].filter { !$0.isEmpty }.joined(separator: "\n"))
    }
}
