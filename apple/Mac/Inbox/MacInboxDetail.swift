import SwiftUI

/// The detail column: what the list has selected. One item opens as on the iPhone (the Feishu chat at its first new
/// message, the mail, the cloud document's rows); an agent row shows what it waits for with the quick answers;
/// several selected offer to file their item rows (an agents-only selection points to their conversations instead).
struct MacInboxDetail: View {
    @Environment(AppModel.self) private var model
    var state = MacInboxState.shared

    var body: some View {
        // Rows can expire out of the list while selected: the count, the single-row fallback and the action only
        // cover what is still there; nothing left falls through to the Pick an Item placeholder (#3795).
        let sel = state.liveSelection(model: model)
        if sel.count > 1 {
            VStack(spacing: Theme.Space.l) {
                // Agent requests are answered in their conversations, not filed: an agents-only selection gets no
                // mark-done button, and a mixed one only files its item rows (#3479).
                switch state.multiSelection {
                case .agentsOnly:
                    EmptyStateView(symbol: Theme.Icon.agents, title: String(localized: "These are Agent requests"),
                                   message: String(localized: "Open their conversations to handle them."))
                case .mixed(let itemCount, let agentCount):
                    EmptyStateView(symbol: Theme.Icon.inbox, title: String(localized: "\(sel.count) Items Selected [Mac Inbox Detail 8aed29]"),
                                   message: String(localized: "\(agentCount) agent requests are handled in their conversations; press E to mark only the other \(itemCount) items as done."))
                    Button("Mark \(itemCount) Items as Done") { state.toggleDone(sel, model: model) }
                        .controlSize(.large)
                        .lineLimit(1)
                case .itemsOnly:
                    EmptyStateView(symbol: Theme.Icon.inbox, title: String(localized: "\(sel.count) Items Selected [Mac Inbox Detail ebed9d]"), message: String(localized: "Press E to mark them all as done."))
                    Button("Mark All as Done") { state.toggleDone(sel, model: model) }
                        .controlSize(.large)
                        .lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .navigationTitle("Inbox")
        } else if case .item(let key)? = sel.first, let item = state.item(key) {
            MacInboxItemDetail(item: item, accounts: state.accounts)
                .id(item.key)
        } else if case .agent(let id)? = sel.first, let ix = model.pendingInteractions.first(where: { $0.id == id }), ix.isSessionless {
            // A card on no session has no chat to open: the whole card, answered here (#4059).
            ScrollView {
                InteractionPanel(interaction: ix)
                    .frame(maxWidth: Theme.Size.readable)
                    .padding(Theme.Space.xl)
            }
            .frame(maxWidth: .infinity)
            .navigationTitle(ix.sessionlessPlace ?? String(localized: "Script"))
        } else if case .agent(let id)? = sel.first, let ix = model.pendingInteractions.first(where: { $0.id == id }) {
            ScrollView {
                InteractionPreviewCard(interaction: ix) { state.open(ix, model: model) }
                    .frame(maxWidth: Theme.Size.readable)
                    .padding(Theme.Space.xl)
            }
            .frame(maxWidth: .infinity)
            .navigationTitle(ix.sessionTitle ?? ix.title)
            .pageToolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button { state.open(ix, model: model) } label: { Label("Open Chat", systemImage: Theme.Icon.conversation) }
                        .help("Open this agent's chat")
                }
            }
        } else {
            SplitPlaceholder(symbol: Theme.Icon.inbox, title: String(localized: "Pick an Item"), message: String(localized: "Select an item in the list on the left to see its details here."))
                .navigationTitle("Inbox")
        }
    }
}

/// One inbox item opened, in the detail column or a window of its own: the content (what a plugin draws for its source
/// when one does, placement inbox_item), and beside it a column with the AI's summary and the item's actions as
/// labelled buttons (`MacInboxActions`: AI Draft Reply / View Draft, Hand to AI, Done / Move Back; over the item when there is no
/// room). Links in it open in the panel at the trailing edge; Feishu pages (a profile, another chat) push in the
/// column's own stack.
struct MacInboxItemDetail: View {
    @Environment(AppModel.self) private var model
    var item: InboxItem
    var accounts: [MailAccount]
    @State private var drafting = false
    @State private var handingOff = false
    @State private var link: LinkTarget?
    @State private var path = NavigationPath()
    @State private var slot: InboxItemSlotModel
    /// The detail runs in a window of its own (`MacInboxItemWindow`), not in the inbox page: it must not drive the
    /// page's shared `reading` state, or its link panel would fold the main window's list (#3477).
    var standalone: Bool

    init(item: InboxItem, accounts: [MailAccount], standalone: Bool = false) {
        self.item = item
        self.accounts = accounts
        self.standalone = standalone
        _slot = State(initialValue: InboxItemSlotModel(item: item))
    }

    /// Whether the detail writes the app-wide `reading` state: an embedded detail does (the page folds its filters
    /// and list for the link panel); a standalone window never touches the main window's layout (#3477).
    static func writesSharedReading(standalone: Bool) -> Bool { !standalone }

    /// Whether the detail takes the list's one-shot AI Draft Reply / Hand to AI signal for its item (`InboxItem.key`) and
    /// opens the sheet: only the inbox page's detail does. A standalone window of the same item took it too, whichever came first, and
    /// opened the sheet in the window behind (#3817); its own buttons still open its sheets.
    static func takesListSignal(_ signalID: String?, itemID: String, standalone: Bool) -> Bool {
        !standalone && signalID == itemID
    }

    private var current: InboxItem { InboxStore.shared.items.first { $0.key == item.key } ?? item }

    var body: some View {
        NavigationStack(path: $path) {
            InboxItemSlotContent(item: current, slot: slot) { content }
                .feishuRoutes()
                .navigationDestination(for: InboxItem.self) {
                    MacInboxItemDetail(item: $0, accounts: accounts, standalone: standalone)
                }
                // An event's chain: the source at an entry's message (inbox phase 1).
                .navigationDestination(for: InboxSourceRoute.self) { InboxSourcePage(route: $0, accounts: accounts) }
        }
        .environment(\.feishuOpen) { route in
            // Documents read beside the item; people and chats replace it (back returns).
            if case .doc(let url, _) = route, let u = URL(string: url) { link = LinkTarget(url: u) } else { path.append(route) }
        }
        .macOpensLinksInPanel($link)
        .macLinkPanel($link, wide: true)
        // Outside the link panel: the actions stay at the window's edge, the document opens between them and the item.
        // While a document is open they are a row over the item; embedded in the page the filters and the list fold
        // away (`reading`). A standalone window keeps that row for its own layout, but gets no Show List button: it
        // does not own the main window's list (#3477).
        // Another item of its Thread pushes in the column's stack, as a Feishu page does (back returns).
        .macInboxActions(current, reading: link != nil, showsListToggle: !standalone,
                         draft: { drafting = true }, handOff: { handingOff = true },
                         openItem: { path.append($0) }, openSource: { path.append($0) })
        .onChange(of: link != nil, initial: true) { _, open in
            if Self.writesSharedReading(standalone: standalone) { MacInboxState.shared.reading = open }
        }
        .onDisappear {
            if Self.writesSharedReading(standalone: standalone), link != nil { MacInboxState.shared.reading = false }
        }
        .navigationTitle(current.title)
        .navigationSubtitle(subtitle)
        .sheet(isPresented: $drafting) { InboxDraftSheet(item: current, accounts: accounts).macSheet() }
        .sheet(isPresented: $handingOff) { InboxHandOffSheet(item: current).macSheet() }
        .onChange(of: MacInboxState.shared.drafting, initial: true) { _, d in
            if Self.takesListSignal(d?.key, itemID: item.key, standalone: standalone) { drafting = true; MacInboxState.shared.drafting = nil }
        }
        .onChange(of: MacInboxState.shared.handingOff, initial: true) { _, d in
            if Self.takesListSignal(d?.key, itemID: item.key, standalone: standalone) { handingOff = true; MacInboxState.shared.handingOff = nil }
        }
        #if DEBUG
        // `-showInboxAction draft|handoff`: open that sheet; `-openInboxLink <url>`: the link panel (screenshots).
        .task {
            switch UserDefaults.standard.string(forKey: "showInboxAction") {
            case "draft": drafting = true
            case "handoff": handingOff = true
            default: break
            }
            if let s = UserDefaults.standard.string(forKey: "openInboxLink"), let u = URL(string: s) { link = LinkTarget(url: u) }
        }
        #endif
    }

    private var subtitle: String {
        if current.doc != nil { return current.doc?.typeName ?? "" }
        if current.isLark { return current.lark?.isGroup == true ? String(localized: "Lark Group [Mac Inbox Detail 98c8c6]") : String(localized: "Lark [Inbox Event Views 7e4419]") }
        return current.accountName
    }

    @ViewBuilder private var content: some View {
        if current.doc != nil {
            InboxDocDetailView(item: current)
        } else if current.connector != nil {
            // GitHub, Telegram…: the thread as the connector draws it, as on iOS and the web (#2611).
            InboxConnectorDetailView(item: current)
        } else if let m = current.moment {
            LarkMomentsPostView(postID: m.postID, pushed: true) // the Moments post and its comments
        } else if let c = item.larkConversation {
            LarkChatView(conversation: c)
        } else if let m = item.mailSummary {
            MailDetailView(summary: m, accounts: accounts)
        } else {
            EmptyStateView(symbol: Theme.Icon.info, title: item.title, message: item.preview)
        }
    }
}

/// An inbox item in a window of its own (⏎ or a double click in the list, 「在新窗口打开」).
struct MacInboxItemWindow: View {
    static let id = "inbox-item"
    @Environment(AppModel.self) private var model
    /// The item's `InboxItem.key`.
    var itemID: String?

    var body: some View {
        Group {
            if let itemID, let item = InboxStore.shared.items.first(where: { $0.key == itemID }) {
                MacInboxItemDetail(item: item, accounts: MacInboxState.shared.accounts, standalone: true)
            } else {
                EmptyStateView(symbol: Theme.Icon.inbox, title: String(localized: "This Item Is No Longer in the Inbox"), message: String(localized: "It may have expired. Check the main window."))
            }
        }
        .macLook(model)
        .frame(minWidth: Theme.Size.macSheetMinWidth, minHeight: Theme.Size.macSheetMinHeight)
    }
}

/// The inbox page until the window's three columns take the pieces over (docs/apple/mac.md「三栏接口」): the filters, the
/// list and the detail side by side, the list's width dragged and remembered.
struct MacInboxPage: View {
    @AppStorage("mac.inbox.listWidth") private var listWidth: Double = Theme.Size.macInboxListIdeal
    @State private var dragging: Double?
    /// The page's whole width: filters, list and detail.
    @State private var total: CGFloat = 0
    private var state: MacInboxState { .shared }
    private var listFolded: Bool { state.reading && !state.listWhileReading && state.query.isEmpty }

    /// The list's width: the one dragged to, narrowed (down to its least) while the detail would get less than
    /// `macDetailMin` (#1200: in the narrowest window the detail was cut off at the window's edge).
    private var listShown: CGFloat {
        // Reading a document: the list at its least, the room is the document's (#2113).
        let reading = MacInboxState.shared.reading
        let wanted = reading ? Theme.Size.macInboxListMin : min(max(dragging ?? listWidth, Theme.Size.macInboxListMin), Theme.Size.macInboxListMax)
        guard total > 0 else { return wanted }
        let filters = reading ? 0 : Theme.Size.macInboxFiltersWidth + Theme.Size.line
        let room = total - filters - Theme.Size.macResizeHitWidth - Theme.Size.macDetailMin
        return max(Theme.Size.macInboxListMin, min(wanted, room))
    }

    var body: some View {
        HStack(spacing: 0) {
            // On the window's white, as the lists beside the sections are on the other pages.
            // Below the title bar's row: the page's title is drawn there over the filters (#1243). Not the list beside
            // them: the title is not over it. Its section headers scroll with the rows (`MacInlineListHeaders`),
            // without AppKit's separately colored floating group banner (#3080).
            // Folded away while a document is read beside the item: the page is the document's (#2113).
            if !MacInboxState.shared.reading {
                MacInboxFilters()
                    .modifier(MacBelowTitleRow())
                    .frame(width: Theme.Size.macInboxFiltersWidth)
                    .frame(maxHeight: .infinity)
                    .transition(.move(edge: .leading).combined(with: .opacity))
                // Up into the title bar's row, as the shell's divider: the list reaches up there and the filters do not,
                // so a divider stopping under the row left a step between them (#1736, as #830).
                Divider().ignoresSafeArea(.container, edges: .top)
            }
            // Folded too while a document is read (2026-11-05 user: picked B), back with the actions row's Show List, a
            // search, or the document closing. Narrowed to nothing rather than left out: the window's toolbar and
            // search field are the list's, and they stay.
            MacInboxList()
                .frame(width: listFolded ? 0 : listShown)
                .clipped()
                .opacity(listFolded ? 0 : 1)
                .allowsHitTesting(!listFolded)
                .accessibilityHidden(listFolded)
            if !listFolded {
                MacPanelResizeHandle { delta in
                    dragging = min(max((dragging ?? listWidth) + delta, Theme.Size.macInboxListMin), Theme.Size.macInboxListMax)
                } ended: {
                    if let dragging { listWidth = dragging }
                    dragging = nil
                }
            }
            MacInboxDetail()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { total = $0 }
        .animation(Theme.Motion.layout, value: state.reading)
        .animation(Theme.Motion.layout, value: listFolded)
        // The pages' color, as Agents and Persona have: the window's own took the wallpaper's tint in dark mode, brown
        // beside the other pages' gray (2026-11-02).
        .background(Theme.pageBackground)
    }
}
