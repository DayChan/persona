import AppKit
import SwiftUI

/// The menu bar item: the Mac's stand-in for the Live Activity / Dynamic Island (docs/apple/mac.md「和 iOS 的对应」).
/// The icon carries how many agents need you; the menu lists them (answer, allow or approve right there), what is
/// working, and what finished and is still unread. It stays when the main window is closed.
enum MacMenuBar {
    static let unreadLimit = 5

    struct Sections {
        var needsYou: [Session] = []
        var working: [Session] = []
        var unread: [Session] = []
        var isEmpty: Bool { needsYou.isEmpty && working.isEmpty && unread.isEmpty }
    }

    /// The sessions the menu bar and the Dock menu list. Agents of a task found quiet (#1731) are the main
    /// conversation's to tell, so both menus drop them before grouping into sections.
    static func menuSessions(_ sessions: [Session], quiet: (Session) -> Bool) -> [Session] {
        sessions.filter { !quiet($0) }
    }

    /// Waiting the longest first; finished ones newest first and only the last few. Muted agents are left out of
    /// what needs you (as the badges do) but still show while they work.
    static func sections(_ sessions: [Session], muted: (Session) -> Bool) -> Sections {
        let indexed = Array(sessions.enumerated())
        func pick(_ state: AgentState, newestFirst: Bool) -> [Session] {
            indexed.filter { $0.element.state == state }
                .sorted { a, b in
                    let x = a.element.stateSince ?? .distantPast, y = b.element.stateSince ?? .distantPast
                    if x != y { return newestFirst ? x > y : x < y }
                    return a.offset < b.offset
                }
                .map(\.element)
        }
        return Sections(needsYou: pick(.needsYou, newestFirst: false).filter { !muted($0) },
                        working: pick(.working, newestFirst: false),
                        unread: Array(pick(.unread, newestFirst: true).prefix(unreadLimit)))
    }

    /// The number beside the icon.
    static func count(needsYou: Int) -> String? { needsYou > 0 ? "\(needsYou)" : nil }

    /// The number beside the icon: waiting cards of the menu's Needs You sessions, not sessions — one session waiting
    /// on a second card shows 2 (#4081), the same unit as the Dock badge and the iPhone's Agents tab.
    static func needsYouCount(_ sections: Sections, interactions: [Interaction]) -> Int {
        let keys = Set(sections.needsYou.map(\.key))
        return interactions.filter { keys.contains($0.sessionKey) }.count
    }

    /// One line of the menu: an agent by its title and machine, and for one that needs you what it asks.
    struct Item: Equatable, Identifiable {
        let key: SessionKey
        let label: String
        var interaction: Interaction?
        var id: SessionKey { key }
    }

    /// The menu as it shows: only what it draws, so a push that changes nothing here (a working agent's last line,
    /// its time, a machine's heartbeat) leaves it equal.
    struct Shown: Equatable {
        var paired = false
        var needsYou: [Item] = []
        var working: [Item] = []
        var unread: [Item] = []
        var isEmpty: Bool { needsYou.isEmpty && working.isEmpty && unread.isEmpty }
    }

    static func shown(_ s: Sections, paired: Bool, interactions: [Interaction], machineName: (String) -> String) -> Shown {
        let asking = Dictionary(interactions.map { ($0.sessionKey, $0) }, uniquingKeysWith: { first, _ in first })
        func item(_ x: Session, asks: Bool = false) -> Item {
            Item(key: x.key, label: "\(x.displayTitle) · \(machineName(x.machineID))", interaction: asks ? asking[x.key] : nil)
        }
        return Shown(paired: paired, needsYou: s.needsYou.map { item($0, asks: true) },
                     working: s.working.map { item($0) }, unread: s.unread.map { item($0) })
    }
}

/// What the menu bar item draws, kept by the model's changes outside any view (`start`) and set only when it changes:
/// a `MenuBarExtra` whose label or menu reads `model.allSessions` is drawn again by every push about any agent (the
/// status item's image set and measured anew, the whole menu rebuilt; about a quarter of what each push cost the main
/// thread on every page, #1495), though what it shows (how many need you, the agents' titles) rarely moves.
@MainActor @Observable final class MacMenuBarState {
    static let shared = MacMenuBarState()
    /// The number beside the icon.
    private(set) var needsYou = 0
    /// The menu's items.
    private(set) var shown = MacMenuBar.Shown()

    func start(_ model: AppModel) { observe(model) }

    private func observe(_ model: AppModel) {
        let (next, count): (MacMenuBar.Shown, Int) = withObservationTracking {
            // Agents of quiet tasks (#1731) are the main conversation's to tell: not in the menu at all.
            let sections = MacMenuBar.sections(MacMenuBar.menuSessions(model.allSessions, quiet: { model.isQuiet($0) }),
                                               muted: { model.isMuted($0) })
            let shown = MacMenuBar.shown(sections, paired: model.isPaired,
                                         interactions: model.pendingInteractions,
                                         machineName: names(model.machines))
            // Cards, not sessions: a second card for one Needs You session moves the icon's number (#4081).
            return (shown, MacMenuBar.needsYouCount(sections, interactions: model.pendingInteractions))
        } onChange: {
            // Told before the change lands: read the new values on the next turn of the main queue.
            DispatchQueue.main.async { MainActor.assumeIsolated { self.observe(model) } }
        }
        if count != needsYou { needsYou = count }
        if next != shown { shown = next }
    }

    private func names(_ machines: [Machine]) -> (String) -> String {
        let byID = Dictionary(machines.map { ($0.id, $0.name) }, uniquingKeysWith: { first, _ in first })
        return { byID[$0] ?? $0 }
    }
}

/// The icon in the menu bar: the Persona mark and how many need you.
struct MacMenuBarLabel: View {
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("MacMenuBarLabel")
        #endif
        let n = MacMenuBarState.shared.needsYou
        HStack(spacing: Theme.Space.xxs) {
            Image(systemName: Theme.Icon.ai)
            if let c = MacMenuBar.count(needsYou: n) { Text(c).monospacedDigit() }
        }
        .accessibilityLabel(n > 0 ? String(localized: "Persona, \(n) need you") : "Persona")
        // Always there (unlike the main window): what reopens the main window for the Dock menu and notifications.
        .onAppear { MacShellState.shared.openMain = { openWindow(id: "main") } }
        // Also always there: opens the windows the menus ask for, with or without a main window.
        .modifier(MacWindowOpener())
    }
}

/// The menu itself (`MenuBarExtra`, menu style): native menu items, submenus for the quick answers.
struct MacMenuBarMenu: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("MacMenuBarMenu")
        #endif
        let s = MacMenuBarState.shared.shown
        if !s.paired {
            Text("Not connected to a hub yet")
        } else if s.isEmpty {
            Text("Nothing needs you")
        }
        if !s.needsYou.isEmpty {
            Section(StateName.needsYou.text) {
                ForEach(s.needsYou) { item in needsYouItem(item) }
            }
        }
        if !s.working.isEmpty {
            Section(StateName.working.text) {
                ForEach(s.working) { item in Button(item.label) { open(item.key) } }
            }
        }
        if !s.unread.isEmpty {
            Section(StateName.unread.text) {
                ForEach(s.unread) { item in Button(item.label) { open(item.key) } }
            }
        }
        Divider()
        Button("Open Persona") { show() }
        Button("New Agent Chat…") {
            show()
            MacShellState.shared.newAgent = true
        }
        .disabled(!s.paired)
        Divider()
        Button("Quit Persona") { NSApp.terminate(nil) }
            .keyboardShortcut("q")
    }

    /// An agent that needs you: a submenu with the answers the phone's cards offer in one tap, and Open Chat.
    @ViewBuilder private func needsYouItem(_ item: MacMenuBar.Item) -> some View {
        if let ix = item.interaction, let quick = MacMenuBar.quickAnswers(ix), !quick.isEmpty {
            Menu(item.label) {
                Text(MacMenuBar.summary(ix))
                ForEach(Array(quick.enumerated()), id: \.offset) { _, q in
                    Button(q.title) { Task { await model.resolve(ix, q.resolution) } }
                }
                Divider()
                Button("Open Chat") { open(item.key) }
            }
        } else {
            Button(item.label) { open(item.key) }
        }
    }

    private func open(_ key: SessionKey) {
        show()
        MacShellState.shared.open(key, model)
    }

    private func show() {
        MacShellState.shared.openMain = { openWindow(id: "main") }
        MacShellState.shared.showMain()
    }
}

extension MacMenuBar {
    struct QuickAnswer {
        var title: String
        var resolution: Resolution
    }

    /// The same one-tap answers as the list's cards (InteractionPreviewCard.quickActions): a single short question's
    /// options, Allow / Deny, a plan's Approve (edits go through, commands still ask: you haven't read it in full here), and a
    /// terminal dialog's short options when the hub read it. Anything else needs the chat.
    static func quickAnswers(_ ix: Interaction) -> [QuickAnswer]? {
        switch ix.kind {
        case .question:
            guard let qs = ix.questions, qs.count == 1, let q = qs.first, !q.multiSelect, q.options.count <= 4 else { return nil }
            return q.options.map { o in
                // The card's Recommended tag, in words; the answer keeps the label as it came.
                let (name, recommended) = QuestionOption.display(o.label)
                return QuickAnswer(title: recommended ? String(localized: "\(name) (Recommended)") : name, resolution: Resolution(answers: [q.question: o.label]))
            }
        case .permission:
            let deny = QuickAnswer(title: String(localized: "Deny"), resolution: Resolution(allow: false))
            // Something to fill in first (a password card's password): no Allow without it, as on the phone's cards.
            if ix.requiresInput { return [deny] }
            return [QuickAnswer(title: String(localized: "Allow [Notification Actions ce7ef2]"), resolution: Resolution(allow: true)), deny]
        case .plan:
            return [QuickAnswer(title: String(localized: "Approve [Notification Actions 8cbe69]"), resolution: Resolution(approve: true, mode: "acceptEdits"))]
        case .screen:
            guard ix.dialog != nil, let d = ix.screenDialog else { return nil }
            let plain = d.options.filter { $0.kind == .plain && $0.label.count <= 24 }
            guard (1...4).contains(plain.count) else { return nil }
            return plain.map { QuickAnswer(title: $0.label, resolution: Resolution(option: $0.key)) }
        }
    }

    /// One line on what it asks.
    static func summary(_ ix: Interaction) -> String {
        // Cards whose text the hub did not fill keep the menu bar's generic wording.
        let fallback: String
        switch ix.kind {
        case .question: fallback = String(localized: "Claude Has a Question")
        case .screen: fallback = String(localized: "The terminal is asking you")
        case .plan, .permission: fallback = ""
        }
        return String((ix.askLine(includeToolName: true) ?? fallback).prefix(80))
    }
}
