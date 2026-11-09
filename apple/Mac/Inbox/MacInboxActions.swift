import SwiftUI

/// What can be done with an opened inbox item (#831; the iPhone's `InboxActionBar` over the item): the AI's summary,
/// the AI Twin at work, its Thread (`InboxLineSection`; another item opens through `openItem`), and the actions as
/// labelled buttons: AI Draft Reply / View Draft (or, where the source is answered
/// in its own app, Reply in Messages / Reply in Telegram / Open), Hand to AI, and Done (Move Back once filed, the others staying as
/// on the iPhone). A column at the detail's trailing edge; where the detail is too narrow for one, a strip over the item.
struct MacInboxActions: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openURL) private var openURL
    var item: InboxItem
    /// A column at the trailing edge (the buttons one under another), or a strip over the item (side by side).
    var column: Bool
    /// A document is open beside the item: the strip starts with Show List / Hide List (the list folds away for it).
    var reading = false
    /// Whether the Show List / Hide List toggle is offered. A standalone item window has no list of its own and must
    /// not fold the main window's list, so the toggle is hidden there (#3477).
    var showsListToggle = true
    var draft: () -> Void
    var handOff: () -> Void
    var openItem: ((InboxItem) -> Void)?
    /// The source at a chain entry's message, pushed in the detail's stack (inbox phase 1).
    var openSource: ((InboxSourceRoute) -> Void)?
    @State private var line: InboxTree?
    /// Inbox phase 1: the suggestion's buttons, and its editor (edit).
    @State private var suggestion: InboxSuggestionModel
    @State private var editing = false

    init(item: InboxItem, column: Bool, reading: Bool = false, showsListToggle: Bool = true,
         draft: @escaping () -> Void, handOff: @escaping () -> Void, openItem: ((InboxItem) -> Void)? = nil,
         openSource: ((InboxSourceRoute) -> Void)? = nil) {
        self.item = item
        self.column = column
        self.reading = reading
        self.showsListToggle = showsListToggle
        self.draft = draft
        self.handOff = handOff
        self.openItem = openItem
        self.openSource = openSource
        _suggestion = State(initialValue: InboxSuggestionModel.live(item.id, manager: item.manager))
    }

    var body: some View {
        bar.inboxLine(item, tree: $line)
            .sheet(isPresented: $editing) {
                if let s = item.currentSuggestion {
                    InboxSuggestionEditor(item: item, suggestion: s, model: suggestion).macSheet()
                }
            }
            #if DEBUG
            // `-showInboxSuggestionEdit YES`: the suggestion's editor (screenshots).
            .task { if column, UserDefaults.standard.bool(forKey: "showInboxSuggestionEdit"), item.currentSuggestion != nil { editing = true } }
            #endif
    }

    @ViewBuilder private var bar: some View {
        if column {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    summary
                    VStack(spacing: Theme.Space.s) { buttons(iconsOnly: false) }
                }
                .padding(Theme.Space.l)
            }
            .background(Theme.Fill.macPanel) // the chat's Session Info column's
        } else {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                summary
                // The words while they fit; a narrow strip keeps the icons (each says what it does on hover) rather
                // than cutting the words to 「查…」「交给…」.
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: Theme.Space.s) { listButton; buttons(iconsOnly: false) }.fixedSize()
                    HStack(spacing: Theme.Space.s) { listButton; buttons(iconsOnly: true) }
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.s)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.bar)
            .overlay(alignment: .bottom) { Divider() }
        }
    }

    /// While a document is read: brings the folded list back beside it, or folds it again.
    @ViewBuilder private var listButton: some View {
        if reading, showsListToggle {
            let state = MacInboxState.shared
            action(state.listWhileReading ? String(localized: "Hide List") : String(localized: "Show List"), Theme.Icon.list, iconsOnly: true,
                   help: state.listWhileReading ? String(localized: "Hide the list to give the document more room") : String(localized: "Show the Inbox list (it also comes back when you close the document)")) {
                state.listWhileReading.toggle()
            }
        }
    }

    /// The AI's summary of the item, and the AI Twin at work when it is.
    @ViewBuilder private var summary: some View {
        let s = item.summary ?? ""
        if !s.isEmpty {
            (Text(Image(systemName: Theme.Icon.ai)).foregroundStyle(Theme.ai) + Text(" " + s))
                .font(Theme.Font.secondary)
                .lineLimit(column ? nil : 3)
                .fixedSize(horizontal: false, vertical: column)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        if item.isEvent {
            // recap, the suggestion with its buttons, the chain and related items (inbox phase 1); the source is the item beside.
            InboxEventBlocks(item: item, model: suggestion, sourceShown: true, compact: !column, edit: { editing = true },
                             openItem: openItem, openSource: openSource)
        } else if item.agentWorking {
            HStack(spacing: Theme.Space.sm) {
                ProgressView().controlSize(.mini)
                Text(item.agent?.state == "queued" ? String(localized: "AI twin is queued")
                     : item.agent?.task.map { String(localized: "AI twin is working: \($0)") } ?? String(localized: "AI twin is working"))
                    .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(column ? 3 : 1)
            }
        }
        InboxLineSection(tree: line, openItem: openItem)
    }

    @ViewBuilder private func buttons(iconsOnly: Bool) -> some View {
        // A connector's thread that cannot be answered here (a CI run, a text message) is answered in its own app.
        if item.currentSuggestion?.kind == "reply" {
            // A suggested reply has its own send / edit in the card (inbox phase 1): no second way to the same draft.
        } else if item.connector.map({ $0.canReply == true }) ?? true {
            // Another suggestion has its own buttons in the card: draft reply steps back to a plain button.
            action(item.agentDraft == nil ? String(localized: "AI Draft Reply") : String(localized: "View Draft"), Theme.Icon.draftReply,
                   prominent: item.currentSuggestion == nil, iconsOnly: iconsOnly,
                   help: item.agentDraft == nil ? String(localized: "Have AI draft a reply in your voice") : String(localized: "Your AI twin drafted a reply. Review it before sending."), perform: draft)
        } else if let open = item.connector?.openURL, let url = InboxActionBar.systemURL(open, draft: item.agentDraft) {
            let sms = url.scheme == "sms"
            action(InboxActionBar.openTitle(item, sms: sms), sms ? Theme.Icon.messagesApp : Theme.Icon.openExternal,
                   prominent: true, iconsOnly: iconsOnly, help: String(localized: "Reply in its own app")) { openURL(url) }
        }
        action(String(localized: "Hand to AI"), Theme.Icon.handToAI, iconsOnly: iconsOnly, help: String(localized: "Hand this to Persona or an agent"), perform: handOff)
        if item.isDone {
            action(String(localized: "Move Back"), Theme.Icon.undo, iconsOnly: iconsOnly, help: String(localized: "Move Back to Inbox")) { InboxStore.shared.setDone(item, false) }
        } else {
            action(String(localized: "Done [Inbox View 59f6c8]"), Theme.Icon.done, iconsOnly: iconsOnly, help: String(localized: "Mark as done (press E in the list)")) {
                MacInboxState.shared.toggleDone([.item(item.key)], model: model)
            }
        }
    }

    @ViewBuilder private func action(_ title: String, _ symbol: String, prominent: Bool = false, iconsOnly: Bool,
                                     help: String, perform: @escaping () -> Void) -> some View {
        let button = Button(action: perform) {
            Label(title, systemImage: symbol)
                .lineLimit(1)
                .minimumScaleFactor(0.8) // 「在 Telegram 里回复」 in the column
                .frame(maxWidth: column ? .infinity : nil)
        }
        .themedButton(prominent: prominent)
        .help(help)
        if iconsOnly { button.labelStyle(.iconOnly) } else { button }
    }
}

extension View {
    /// The item's actions (`MacInboxActions`) beside it when the detail has room for a column, over it otherwise.
    func macInboxActions(_ item: InboxItem, reading: Bool = false, showsListToggle: Bool = true,
                         draft: @escaping () -> Void, handOff: @escaping () -> Void,
                         openItem: ((InboxItem) -> Void)? = nil, openSource: ((InboxSourceRoute) -> Void)? = nil) -> some View {
        modifier(MacInboxActionsHost(item: item, reading: reading, showsListToggle: showsListToggle,
                                     draft: draft, handOff: handOff, openItem: openItem, openSource: openSource))
    }
}

private struct MacInboxActionsHost: ViewModifier {
    var item: InboxItem
    /// A document is open beside the item: the actions are a row over it, the column's width is the document's.
    var reading: Bool
    /// The strip offers Show List / Hide List only when embedded in the page that owns the list (#3477).
    var showsListToggle: Bool
    var draft: () -> Void
    var handOff: () -> Void
    var openItem: ((InboxItem) -> Void)?
    var openSource: ((InboxSourceRoute) -> Void)?
    @State private var wide = true
    private var room: Bool { wide && !reading }

    /// Siblings, as the link panel and Session Info are: the item narrows rather than running under the column. One
    /// structure for both, so crossing the width keeps the item (its scroll position, what was pushed).
    func body(content: Content) -> some View {
        HStack(spacing: 0) {
            VStack(spacing: 0) {
                if !room {
                    MacInboxActions(item: item, column: false, reading: reading, showsListToggle: showsListToggle,
                                    draft: draft, handOff: handOff, openItem: openItem, openSource: openSource)
                }
                content.frame(maxWidth: .infinity, maxHeight: .infinity)
            }
            if room {
                Divider()
                MacInboxActions(item: item, column: true, draft: draft, handOff: handOff, openItem: openItem, openSource: openSource)
                    .frame(width: Theme.Size.macInboxActionsWidth)
            }
        }
        .onGeometryChange(for: Bool.self) {
            $0.size.width >= Theme.Size.macInboxActionsWidth + Theme.Size.macInboxActionsPageMin
        } action: { wide = $0 }
    }
}
