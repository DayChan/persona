import SwiftUI

/// What Hand to AI can be asked in one tap: the chip's short title and the ask it sends after the item.
enum InboxAsk: CaseIterable {
    case summarize, background, handle

    var title: String {
        switch self {
        case .summarize: String(localized: "Summarize & Suggest Reply")
        case .background: String(localized: "Look Into It")
        case .handle: String(localized: "Handle It")
        }
    }

    var ask: String {
        switch self {
        case .summarize: String(localized: "Summarize it, tell me what to do, and suggest a reply")
        case .background: String(localized: "Look into the background, then tell me")
        case .handle: String(localized: "Handle it for me, but ask before replying")
        }
    }

    var symbol: String {
        switch self {
        case .summarize: Theme.Icon.ai
        case .background: Theme.Icon.search
        case .handle: Theme.Icon.handToAI
        }
    }
}

/// Hand to AI along the bottom of an opened item, iMessage's bar (docs/assistant/inbox.md "Item actions"): one row of
/// one-tap asks, then [target ▾] [Hand to AI…] [↑]. A chip or a send goes to the target at once, behind a toast with Undo
/// and View for `InboxHandOffModel.undoWindow`; the target is a menu (Main Chat, New Chat, the latest three Agents,
/// More…) and the last one chosen is remembered. The page's content gets the bar's height as its bottom inset
/// (`safeAreaInset`), and the bar rides up with the keyboard.
struct InboxHandOffBar: View {
    @Environment(AppModel.self) private var model
    let item: InboxItem
    /// The Draft Reply chip (AI Draft Reply's sheet); nil where the item can't be answered from here.
    var draft: (() -> Void)?
    var draftTitle = String(localized: "Draft Reply [Inbox Chip]")
    @State private var handOff: InboxHandOffModel
    @StoredValue(InboxHandOffModel.lastTargetKey) private var stored = InboxHandOffModel.Target.main.stored
    @State private var text = ""
    /// The queued ask was typed: Undo puts it back in the field.
    @State private var typed: String?
    /// The send just went: "Sent to …" with View, for a moment.
    @State private var justSent = false
    /// The target is the one this item's source went to last time (#2121): marked Last time until another is chosen.
    @State private var lastTime = false
    /// The Agent this source went to last time is gone: the main chat stands in, and the toast line says so once.
    @State private var lastGone = false
    @FocusState private var focused: Bool

    init(item: InboxItem, draft: (() -> Void)?, draftTitle: String? = nil) {
        self.item = item
        self.draft = draft
        if let draftTitle { self.draftTitle = draftTitle }
        _handOff = State(initialValue: InboxHandOffModel.live(item, model: .shared))
    }

    /// The Agents the menu offers, latest first: three in the menu, the rest under More…. Read unwatched
    /// (`sessionsNow`): the bar is always on screen and must not be drawn again by every push about any agent.
    private var agents: [Session] {
        InboxHandOffModel.agents(model.sessionsNow, limit: Self.listed) { model.machine($0)?.online == true }
    }
    static let inMenu = 3
    static let listed = 12

    private var trimmed: String { text.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        VStack(spacing: Theme.Space.s) {
            if let toast {
                ToastCapsule(toast: toast)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("handoff-toast")
            } else {
                chips.transition(.opacity)
            }
            if lastGone {
                Label(String(localized: "The Agent this went to last time has ended, so it goes to Main Chat"), systemImage: Theme.Icon.info)
                    .font(Theme.Font.caption)
                    .foregroundStyle(Theme.cautionText)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityIdentifier("handoff-last-gone")
            }
            bar
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
        .frame(maxWidth: Theme.Size.readable)
        .frame(maxWidth: .infinity)
        // What scrolls under it stays under it: the page's text never runs through the chips.
        .background(.bar, ignoresSafeAreaEdges: .bottom)
        .animation(Theme.Motion.standard, value: toastKey)
        .onAppear(perform: preselect)
        // However it went (the Undo window ran out, View, Retry): it says so, with View, and this source remembers it.
        .onChange(of: handOff.sending) { was, now in
            guard was, !now, let sent = handOff.sent else { return }
            Haptics.success()
            justSent = true
            lastGone = false
            if let key = InboxHandOffMemory.key(item) { memory.remember(key, target: sent.target) }
        }
        .task(id: justSent) {
            guard justSent else { return }
            try? await Task.sleep(for: InboxHandOffModel.undoWindow)
            justSent = false
        }
    }

    // MARK: Chips

    private var chips: some View {
        SidewaysScroll {
            HStack(spacing: Theme.Space.s) {
                ForEach(InboxAsk.allCases, id: \.self) { a in
                    FilterChip(title: a.title, symbol: a.symbol, on: false) { queue(a.ask, typed: false) }
                        .accessibilityIdentifier("handoff-chip-\(a)")
                    if a == .summarize, let draft {
                        FilterChip(title: draftTitle, symbol: Theme.Icon.draftReply, on: false, action: draft)
                            .accessibilityIdentifier("handoff-chip-draft")
                    }
                }
            }
        }
        .disabled(handOff.sending)
    }

    // MARK: The bar

    private var bar: some View {
        HStack(spacing: Theme.Space.s) {
            targetMenu
            TextField(String(localized: "Hand to AI…"), text: $text)
                .font(Theme.Font.body)
                .lineLimit(1)
                .focused($focused)
                .submitLabel(.send)
                .onSubmit(sendTyped)
                .accessibilityIdentifier("handoff-field")
            ComposerRoundButton(symbol: Theme.Icon.send, label: String(localized: "Send"), id: "handoff-send", action: sendTyped)
                .disabled(trimmed.isEmpty || handOff.sending)
                .opacity(trimmed.isEmpty ? 0.35 : 1)
        }
        .padding(.leading, Theme.Space.s)
        .padding(.trailing, Theme.Space.xxs)
        .padding(.vertical, Theme.Space.xxs)
        .modifier(ComposerSurface())
    }

    private var targetMenu: some View {
        Menu {
            Button { choose(.main) } label: { Label(String(localized: "Main Chat"), systemImage: Theme.Icon.persona) }
            Button { choose(.newChat) } label: { Label(String(localized: "New Chat [Inbox Hand-Off]"), systemImage: Theme.Icon.newConversation) }
            let all = agents
            if !all.isEmpty {
                Section(String(localized: "Agents")) {
                    ForEach(all.prefix(Self.inMenu)) { agentButton($0) }
                    if all.count > Self.inMenu {
                        Menu {
                            ForEach(all.dropFirst(Self.inMenu)) { agentButton($0) }
                        } label: { Label(String(localized: "More…"), systemImage: Theme.Icon.more) }
                    }
                }
            }
        } label: {
            HStack(spacing: Theme.Space.xs) {
                ThemedIcon(symbol(handOff.target)).font(Theme.Font.label)
                Text(name(handOff.target)).lineLimit(1)
                if lastTime {
                    Chip(text: String(localized: "Last time [Inbox Hand-Off]"), look: .tint(Theme.accent), compact: true)
                        .fixedSize()
                        .accessibilityHidden(true)
                }
                ThemedIcon(Theme.Icon.choices).font(Theme.Font.label).foregroundStyle(.secondary)
            }
            .font(Theme.Font.secondaryStrong)
            .foregroundStyle(Theme.accent)
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, Theme.Space.sm)
            .background(Theme.accent.opacity(0.12), in: .capsule)
            // Room for the Last time mark next to the name: at the usual width the name was squeezed to "…".
            .frame(maxWidth: lastTime ? Theme.Size.box200 : Theme.Size.box120, alignment: .leading)
            .fixedSize(horizontal: false, vertical: true)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Hand To"))
        .accessibilityValue(lastTime ? String(localized: "\(name(handOff.target)), last time") : name(handOff.target))
        .accessibilityIdentifier("handoff-target")
    }

    private func agentButton(_ s: Session) -> some View {
        Button { choose(.agent(s.key)) } label: {
            Label {
                Text(s.displayTitle)
                Text(model.machine(s.machineRef)?.title ?? s.machineID)
            } icon: { ThemedIcon(Theme.Icon.agents) }
        }
    }

    // MARK: Target

    /// The remembered target, if it is still there: an Agent no longer listed goes back to the main chat.
    private var remembered: InboxHandOffModel.Target {
        let t = InboxHandOffModel.Target(stored: stored)
        if case .agent(let key) = t, !agents.contains(where: { $0.key == key }) { return .main }
        return t
    }

    private var memory: InboxHandOffMemory { InboxHandOffMemory(group: model.hubGroup) }

    /// This source's last target when there is one (#2121), else the bar's last target.
    private func preselect() {
        if let r = memory.resolved(for: item, isListed: { InboxHandOffModel.takes($0, model: model) }) {
            handOff.target = r.target
            lastTime = r.lastTime
            lastGone = r.gone
        } else {
            handOff.target = remembered
        }
    }

    private func choose(_ t: InboxHandOffModel.Target) {
        handOff.target = t
        stored = t.stored
        lastTime = false
        lastGone = false
    }

    private func name(_ t: InboxHandOffModel.Target) -> String {
        switch t {
        case .main: String(localized: "Main Chat")
        case .newChat: String(localized: "New Chat [Inbox Hand-Off]")
        case .agent(let key): model.session(key)?.displayTitle ?? String(localized: "Agent")
        }
    }

    private func symbol(_ t: InboxHandOffModel.Target) -> String {
        switch t {
        case .main: Theme.Icon.persona
        case .newChat: Theme.Icon.newConversation
        case .agent: Theme.Icon.agents
        }
    }

    // MARK: Sending

    private func sendTyped() {
        let ask = trimmed
        guard !ask.isEmpty, !handOff.sending else { return }
        text = ""
        focused = false
        queue(ask, typed: true)
    }

    private func queue(_ ask: String, typed: Bool) {
        self.typed = typed ? ask : nil
        justSent = false
        handOff.queue(ask: ask)
        Haptics.tap()
    }

    /// What the toast says: waiting for Undo, sending, sent, or why it failed.
    private var toast: Toast? {
        let to = name(handOff.target)
        if handOff.queued != nil {
            let words = handOff.markDone ? String(localized: "Handing to \(to)… · marking done") : String(localized: "Handing to \(to)…")
            return Toast(words, style: .info,
                         action: Toast.Action(title: String(localized: "View [Inbox Hand-Off]")) { view() },
                         secondary: Toast.Action(title: String(localized: "Undo")) { undo() })
        }
        if handOff.sending { return Toast(String(localized: "Handing to \(to)…"), style: .info) }
        if let error = handOff.error {
            return Toast(error, style: .error, action: handOff.lastAsk.map { ask in
                Toast.Action(title: String(localized: "Retry")) { Task { _ = await handOff.send(ask: ask) } }
            })
        }
        if justSent, let sent = handOff.sent {
            let to = name(sent.target)
            let words = handOff.markDone ? String(localized: "Handed to \(to) · marked done") : String(localized: "Handed to \(to)")
            return Toast(words, style: .success,
                         action: Toast.Action(title: String(localized: "View [Inbox Hand-Off]")) { handOff.open() })
        }
        return nil
    }

    /// What the toast is showing, for its animation.
    private var toastKey: Int {
        if handOff.queued != nil { return 1 }
        if handOff.sending { return 2 }
        if handOff.error != nil { return 3 }
        return justSent ? 4 : 0
    }

    private func undo() {
        let back = handOff.undo()
        if let typed, back == typed { text = typed }
        typed = nil
    }

    /// View while it waits: sends now and opens where it went.
    private func view() {
        Task { if await handOff.sendQueued() { handOff.open() } }
    }
}
