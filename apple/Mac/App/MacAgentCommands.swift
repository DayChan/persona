import AppKit
import SwiftUI

/// The Agent menu (docs/apple/mac.md「菜单和快捷键」): what the key window's chat (`MacShellState.focusedChat`) can be told
/// from the keyboard. Questions and terminal dialogs need their choices, a plan sent back needs words: those stay in
/// the chat's card; the menu says yes to a tool or a plan, no to a tool.
enum MacAgentCommands {
    /// ⇧⌘⏎: allow the tool, or approve the plan with edits auto-accepted (commands still ask; the same as the
    /// notification's Approve — Approve & Auto-Run stays a choice in the card). A tool with something to fill in first
    /// (a password card) is allowed only from its card.
    static func approve(_ ix: Interaction) -> Resolution? {
        switch ix.kind {
        case .permission: ix.requiresInput ? nil : Resolution(allow: true)
        case .plan: Resolution(approve: true, mode: "acceptEdits")
        case .question, .screen: nil
        }
    }

    /// ⇧⌘⌫: deny the tool.
    static func reject(_ ix: Interaction) -> Resolution? {
        ix.kind == .permission ? Resolution(allow: false) : nil
    }

    /// ⌥⌘1…9: the options a key can pick on its own (a single question's choices, a terminal dialog's).
    /// A tool or a plan has Approve / Deny above; several questions, a multi-select or a dialog asking for text need the card.
    static func choices(_ ix: Interaction) -> [MacMenuBar.QuickAnswer] {
        switch ix.kind {
        case .question:
            guard let qs = ix.questions, qs.count == 1, let q = qs.first, !q.multiSelect else { return [] }
            return q.options.prefix(9).map { o in
                // The card's Recommended tag, in words: a menu item is text only. The answer keeps the label as it came.
                let (name, recommended) = QuestionOption.display(o.label)
                return .init(title: recommended ? String(localized: "\(name) (Recommended)") : name, resolution: Resolution(answers: [q.question: o.label]))
            }
        case .screen:
            guard ix.dialog != nil, let d = ix.screenDialog else { return [] }
            return d.options.filter { $0.kind == .plain }.prefix(9).map { .init(title: $0.label, resolution: Resolution(option: $0.key)) }
        case .permission, .plan:
            return []
        }
    }

    /// ⌃⌘N: the next agent or Persona conversation that needs you, longest waiting first, wrapping around.
    /// The picking is shared with the iPhone chat's post-resolution entry (`NextNeedsYou`, #4348).
    static func nextNeedsYou(after current: SessionKey?, in sessions: [Session], muted: (Session) -> Bool) -> SessionKey? {
        NextNeedsYou.next(after: current, in: sessions, muted: muted)
    }
}

/// The menu itself.
struct MacAgentMenu: Commands {
    let model: AppModel

    var body: some Commands {
        CommandMenu("Agent") {
            let key = MacShellState.shared.focusedChat(model)
            let ix = key.flatMap { model.interactions(for: $0).first }
            let current = key.flatMap { model.session($0) }
            let terminalOff = current.map { $0.hasSource && !$0.canTerminal } ?? false
            Button("Interrupt [Composer Buttons f9d193]") { if let key { Task { await model.interrupt(key) } } }
                .keyboardShortcut(".", modifiers: .command)
                .disabled(current?.state != .working || current?.canInterrupt != true)
            Button("Approve Pending Request") {
                if let ix, let r = MacAgentCommands.approve(ix) { Task { await model.resolve(ix, r) } }
            }
            // ⇧⌘⏎: ⌘⏎ is the composer's send (ComposerParts).
            .keyboardShortcut(.return, modifiers: [.command, .shift])
            .disabled(ix.flatMap(MacAgentCommands.approve) == nil || ix.map(model.canAnswer) != true)
            // ⇧⌘⌫, not the docs' first idea ⌘⌫: that one deletes to the line's start while typing in the composer,
            // and a menu shortcut would take it.
            Button("Deny") {
                if let ix, let r = MacAgentCommands.reject(ix) { Task { await model.resolve(ix, r) } }
            }
            .keyboardShortcut(.delete, modifiers: [.command, .shift])
            .disabled(ix.flatMap(MacAgentCommands.reject) == nil || ix.map(model.canAnswer) != true)
            // ⌥⌘1…9 (⌘1…6 are the sections): pick an option of the question or terminal dialog on screen.
            let choices = ix.map(MacAgentCommands.choices) ?? []
            Menu(String(localized: "Answer")) {
                ForEach(Array(choices.enumerated()), id: \.offset) { i, c in
                    Button(c.title) { if let ix { Task { await model.resolve(ix, c.resolution) } } }
                        .keyboardShortcut(KeyEquivalent(Character("\(i + 1)")), modifiers: [.command, .option])
                }
            }
            .disabled(choices.isEmpty || ix.map(model.canAnswer) != true)
            Divider()
            Button("Next Needing You") {
                if let next = MacAgentCommands.nextNeedsYou(after: key, in: model.sessionsNow,
                                                            muted: { model.isMuted($0) || model.isQuiet($0) }) {
                    MacShellState.shared.open(next, model)
                    // The chat shows in the main window: bring it up (a chat window may be the key one, or none open).
                    MacShellState.shared.showMain()
                }
            }
            .keyboardShortcut("n", modifiers: [.command, .control]) // ⌥⌘N is Inbox's New Email
            // Quiet tasks (#1731) are the main conversation's to tell: they never enable the jump either.
            .disabled(!model.allSessions.contains { $0.state == .needsYou && !model.isMuted($0) && !model.isQuiet($0) })
            Button("Open in New Window") { if let key { MacShellState.shared.chatWindow = key } }
                .disabled(key == nil)
            // Only an Agent in a herdr pane has a terminal (the Persona conversation runs headless).
            if !terminalOff {
                Button("Open Terminal in New Window") { if let key { MacShellState.shared.terminalWindow = key } }
                    .disabled(current.map { $0.assistant || $0.paneID == nil || !$0.canTerminal } ?? true)
            }
            if current?.canAttach == true {
                Button("Attach Live") { if let key { Task { await model.attach(key) } } }.lineLimit(1)
            }
            // The row menu's Hand to Persona: the Agent's state and last answer go to the main conversation (AgentHandoff).
            Button("Hand to Persona") {
                if let key, let s = model.latest(key) { // its last line as it is now
                    MacShellState.shared.willOpen() // Knowledge / Machines make way for the Persona page
                    Task { await model.askPersona(AgentHandoff.prompt(s, machine: model.machine(s.machineRef)?.name)) }
                }
            }
            .keyboardShortcut("j", modifiers: .command)
            .disabled(key.flatMap { model.session($0) } == nil || key.flatMap { model.session($0)?.assistant } == true)
            // ⌘I: back to the chat's composer from its messages, the inspector or the link panel, as the web's Mod+I
            // (web/src/lib/shortcuts.ts `focus-input`). The chat's composer listens for `composerFocus`.
            Button("Focus Message Field") { if let key { model.composerFocus = key } }
                .keyboardShortcut("i", modifiers: .command)
                .disabled(key == nil)
            Button("Show Session Info") { ask(.inspector) }
                .keyboardShortcut("i", modifiers: [.command, .option])
                .disabled(key == nil)
            // The chat page's Terminal | Chat (#3183). ⇧⌘J: ⌘J (the web's toggle) is Hand to Persona here; a ⌃ key would be the
            // terminal's own when it has the keyboard.
            if !terminalOff {
                Button("Switch Terminal / Chat") { ask(.terminal) }
                    .keyboardShortcut("j", modifiers: [.command, .shift])
                    .disabled(current.map { $0.assistant || !$0.canTerminal } ?? true)
            }
            Divider()
            // The chat page's own dialogs (ChatView's ⋯ menu, the composer's /clear), so only for an Agent's chat.
            let agent = key.flatMap { model.session($0) }.flatMap { $0.assistant ? nil : $0 }
            Button("Rename… [Assistant Sidebar ec4cd0]") { ask(.rename) }
                .disabled(agent?.canTerminal != true)
            Button("Clear Context…") { ask(.clear) }
                .disabled(agent.map { !$0.canSend || !$0.canTerminal || $0.paneID == nil || $0.isCodex || $0.isTraex || $0.state == .working } ?? true)
            if !terminalOff {
                Button("Close Agent…") { ask(.close) }
                    .disabled(agent?.status.isLive != true || agent?.canTerminal != true)
            }
            Button("Delete Chat…") { ask(.delete) }
                .disabled(agent?.canDelete != true)
            Divider()
            Button("Mark as Seen") { if let key { model.markSeen(key) } }
                .disabled(key == nil || model.session(key!)?.state != .unread)
            Button(key.map { model.isPinned($0) } == true ? String(localized: "Unpin [Personal Agent List View c92179]") : String(localized: "Pin [Personal Agent List View 173f88]")) { if let key { model.togglePin(key) } }
                .disabled(key == nil)
            Button(key.map { model.isMuted($0) } == true ? String(localized: "Unmute [Chat View 3834f9]") : String(localized: "Block")) { if let key { model.toggleMute(key) } }
                .disabled(key == nil)
            Divider()
            Button("Copy Session ID [Assistant Sidebar f24fb3]") { if let key { copy(key.sessionID, String(localized: "Session ID Copied")) } }
                .disabled(key == nil)
            if !terminalOff {
                Button("Copy Resume Command") {
                    if let key, let s = model.session(key), s.canTerminal {
                        copy(MacAgentCommands.resumeLine(cwd: s.cwd, command: s.resumeCommand, id: s.sourceThreadID ?? key.sessionID), String(localized: "Resume command copied"))
                    }
                }
                .disabled(current?.canTerminal != true)
            }
        }
    }

    private func ask(_ kind: MacShellState.ChatRequest.Kind) {
        let shell = MacShellState.shared
        if let key = shell.focusedChat(model) { shell.chatRequest = .init(key: key, kind: kind, window: shell.keyWindow) }
    }

    private func copy(_ text: String, _ toast: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        model.toast = Toast(toast, style: .success)
    }
}

extension MacAgentCommands {
    /// `cd '<folder>' && claude --resume <id>`: the folder single-quoted, so a space, `;` or `$(…)` in it stays a path.
    static func resumeLine(cwd: String, command: String, id: String) -> String {
        Session.resumeLine(cwd: cwd, command: command, id: id)
    }
}
