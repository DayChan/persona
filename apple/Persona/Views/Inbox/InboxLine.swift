import SwiftUI

// Thread (#2128, docs/assistant/inbox.md「线索」), as on the web (#2352): under an opened item's summary, what it grew out of (its
// parent's chain, up to a todo or an agent session) and what grew out of it (the items right under it). Each row opens
// where it lives: another item, the todo, the agent's chat. Nothing is drawn when the item has neither, nor from a hub
// without GET /api/v1/inbox/tree.

/// The rows of a line, worked out from the hub's tree (pure, for tests).
nonisolated enum InboxLine {
    enum Target: Hashable, Sendable {
        case item(InboxItem)
        case todo(String)
        case session(SessionKey)
    }

    struct Row: Hashable, Sendable, Identifiable {
        var id: String
        var kind: String // inbox | todo | session
        var label: String
        var title: String
        /// Where a tap goes; nil: nowhere left to go (an item no longer in the inbox, a session id without its halves).
        var target: Target?
        /// An ancestor (what it grew out of), or an item under it (Next).
        var up: Bool
    }

    static func label(_ kind: String) -> String {
        switch kind {
        case "todo": String(localized: "To-dos")
        case "session": String(localized: "Agent Session")
        default: String(localized: "Event [Inbox Line c56020]")
        }
    }

    /// The ancestors farthest first (the chain reads top down), then the children oldest first. `todoTitle` /
    /// `sessionTitle` name a todo or a session the app knows; the id stands in otherwise.
    static func rows(_ tree: InboxTree, todoTitle: (String) -> String? = { _ in nil },
                     sessionTitle: (SessionKey) -> String? = { _ in nil }) -> [Row] {
        let ups = tree.ancestors.reversed().map { a -> Row in
            let p = a.parent
            let title: String
            let target: Target?
            switch p.kind {
            case "todo":
                title = todoTitle(p.id) ?? p.id
                target = .todo(p.id)
            case "session":
                title = p.sessionKey.flatMap(sessionTitle) ?? p.id
                target = p.sessionKey.map(Target.session)
            default:
                title = a.item?.title ?? String(localized: "No longer in the Inbox")
                target = a.item.map(Target.item)
            }
            return Row(id: "up:\(p.kind):\(p.id)", kind: p.kind, label: label(p.kind), title: title, target: target, up: true)
        }
        let downs = tree.children.map {
            Row(id: "down:\($0.id)", kind: "inbox", label: String(localized: "Next [Inbox Line acfc4e]"), title: $0.title, target: .item($0), up: false)
        }
        return ups + downs
    }

    static func symbol(_ kind: String) -> String {
        switch kind {
        case "todo": Theme.Icon.todo
        case "session": Theme.Icon.agents
        default: Theme.Icon.inbox
        }
    }
}

/// The Thread section of an opened item (iPhone's action bar, the Mac's actions column): the rows of `tree`, which the
/// host loads with `.inboxLine(_:tree:)` (on a view that is always there: on this section, which is empty until it has
/// rows, the answer never showed). `openItem`: how another item opens here; nil pushes it on the stack the section is in.
struct InboxLineSection: View {
    @Environment(AppModel.self) private var model
    var tree: InboxTree?
    var openItem: ((InboxItem) -> Void)?
    @State private var todo: TodoDetailRoute?

    var body: some View {
        let rows = tree.map {
            InboxLine.rows($0, todoTitle: { id in TodoStore.shared.todos.first { $0.id == id }?.title },
                           sessionTitle: { model.session(model.inHub($0))?.displayTitle })
        } ?? []
        if !rows.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text("Thread").font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                ForEach(rows) { row($0) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("inbox.line")
            .sheet(item: $todo) { r in
                NavigationStack {
                    TodoDetailView(todoID: r.id).macSheetDone { todo = nil }
                }
                #if os(macOS)
                .macSheet()
                #endif
            }
        }
    }

    @ViewBuilder private func row(_ r: InboxLine.Row) -> some View {
        switch r.target {
        case .item(let it)?:
            if let openItem {
                Button { openItem(it) } label: { label(r) }.buttonStyle(.plain)
            } else {
                NavigationLink(value: it) { label(r) }.buttonStyle(.plain)
            }
        case .todo(let id)?:
            Button { todo = TodoDetailRoute(id: id) } label: { label(r) }.buttonStyle(.plain)
        case .session(let key)?:
            Button { openTodoAgentChat(model.inHub(key), in: model) } label: { label(r) }.buttonStyle(.plain)
        case nil:
            label(r)
        }
    }

    private func label(_ r: InboxLine.Row) -> some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(InboxLine.symbol(r.kind))
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.iconXS)
            Text(r.label).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1).fixedSize()
            Text(r.title)
                .font(Theme.Font.secondary)
                .foregroundStyle(r.target == nil ? Color.secondary : Color.primary)
                .lineLimit(1)
                .truncationMode(.tail)
                .frame(maxWidth: .infinity, alignment: .leading)
            if r.target != nil {
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
            }
        }
        .padding(.vertical, Theme.Space.xs)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(String(localized: "\(r.label): \(r.title)"))
    }
}

extension View {
    /// Loads `item`'s Thread into `tree` (GET /api/v1/inbox/tree/inbox/{id}), again when the item is hung elsewhere; an
    /// older hub without the route leaves it nil.
    func inboxLine(_ item: InboxItem, tree: Binding<InboxTree?>) -> some View {
        modifier(InboxLineLoader(item: item, tree: tree))
    }
}

private struct InboxLineLoader: ViewModifier {
    @Environment(AppModel.self) private var model
    var item: InboxItem
    @Binding var tree: InboxTree?

    func body(content: Content) -> some View {
        content.task(id: item.id + "|" + (item.parent.map { $0.kind + ":" + $0.id } ?? "")) {
            guard let api = model.api, let t = try? await api.inboxTree("inbox", item.id) else { return }
            withAnimation(Theme.Motion.layout) { tree = t }
        }
    }
}
