import SwiftUI

/// A message Persona typed in for the model (a task's notice, a scheduled run, a wake-up…; `Injected`,
/// docs/protocol/protocol.md「Persona 注入的消息」): one centered line — an icon and `line` — where a user bubble would be,
/// never its words. A task's notice opens the task (in an assistant conversation: the todo the hub named, else the
/// newest one of that title the conversation opened); a scheduled run's line opens under it to what the task asks.
/// The same look as web and Android (#2399).
struct InjectedLine: View {
    var item: Item
    var injected: Injected
    /// The assistant conversation it is in, where its task is looked for; nil elsewhere (an agent's chat).
    var conversation: String?
    @State private var open = false
    @State private var unfold = Unfolding()
    @State private var bottom: CGFloat = 0
    @Environment(\.chatPin) private var pin
    private var store: TasksStore { .shared }
    private var pinKey: String { "injected-" + item.id }

    var body: some View {
        let todo = conversation.flatMap { Self.task(of: injected, todos: Array(store.todos.values), conversation: $0) }
        let detail = injected.detail.flatMap { $0.isEmpty ? nil : $0 }
        VStack(alignment: .leading, spacing: 0) {
            header(todo: todo, expandable: todo == nil && detail != nil)
            if open, let detail {
                Text(detail)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.primary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.bottom, Theme.Space.m)
                    .accessibilityIdentifier("injected-detail")
                    .unfolding(unfold)
                    .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .frame(maxWidth: open ? .infinity : nil, alignment: .leading)
        .onGeometryChange(for: CGFloat.self, of: { $0.frame(in: .global).maxY }) { bottom = $0 }
        .background(Theme.Fill.chip, in: open ? AnyShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous)) : AnyShape(.capsule))
        .frame(maxWidth: .infinity)
        .chatPinned(pinKey)
    }

    private func header(todo: String?, expandable: Bool) -> some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(InjectedLook.icon(injected)).font(Theme.Font.labelStrong).foregroundStyle(InjectedLook.tint(injected))
            Text(injected.line).lineLimit(1).truncationMode(.middle)
            if todo != nil || expandable {
                ThemedIcon(Theme.Icon.disclosure)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(open ? 90 : 0))
                    .accessibilityHidden(true)
            }
        }
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.sm)
        .contentShape(.rect)
        .onTapGesture {
            if let todo {
                Haptics.tap()
                store.detailTask = todo
            } else if expandable {
                toggle()
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("injected-\(injected.kind)")
        .accessibilityAddTraits(todo != nil || expandable ? .isButton : [])
        .accessibilityHint(todo != nil ? String(localized: "Open Task") : expandable ? (open ? String(localized: "Shows the task's request [Injected Line b3e725]") : String(localized: "Shows the task's request [Injected Line 95b98b]")) : "")
    }

    private func toggle() {
        Haptics.tap()
        let opening = !open
        toggleInPlace(pin, pinKey, animation: Theme.Motion.state, unfold: $unfold, closing: open) { open.toggle() }
        #if os(iOS) || os(visionOS)
        // The line as the chat's last row: what the task asks opens under it, past the composer.
        guard opening, let pin else { return }
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(Theme.Motion.revealTime))
            if open { pin.show(pinKey, bottom: bottom) }
        }
        #endif
    }

    /// The task a task notice is about: the hub's todo id when the app knows it, else the newest task of that title
    /// the conversation opened; nil when none is known (the line is not tappable then).
    static func task(of n: Injected, todos: [TodoItem], conversation: String) -> String? {
        guard n.isTaskNotice else { return nil }
        if let id = n.todoID, !id.isEmpty, todos.contains(where: { $0.id == id }) { return id }
        guard let title = n.title, !title.isEmpty else { return nil }
        return todos.filter { $0.title == title && $0.origin == conversation }
            .max { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }?.id
    }
}
