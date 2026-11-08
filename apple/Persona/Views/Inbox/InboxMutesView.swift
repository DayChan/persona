import SwiftUI

/// Muted (the inbox's More menu): the repositories, chats and senders muted from an item's menu, in every source,
/// newest first. A swipe or a long press takes one back; nothing it covered comes back, only what comes in later
/// (docs/assistant/inbox.md「静音」).
struct InboxMutesView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var state: Loadable<[InboxMute]> = .loading

    var body: some View {
        NavigationStack {
            LoadableContent(state: state, retry: { Task { await load() } }) { mutes in
                List {
                    ForEach(mutes) { m in row(m).themedRow() }
                }
                .listStyle(.plain)
                .overlay {
                    if mutes.isEmpty {
                        EmptyStateView(symbol: Theme.Icon.mute, title: String(localized: "Nothing Muted"), message: String(localized: "Touch and hold an item in the Inbox to mute its repo, conversation, or sender."))
                    }
                }
            }
            .themedPage()
            .navigationTitle("Muted [Inbox Mutes View e5e468]")
            #if os(iOS) || os(visionOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1)
                }
            }
            .task { await load() }
        }
    }

    private func row(_ m: InboxMute) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.mute).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(m.name).font(Theme.Font.body).lineLimit(1)
                Text([m.sourceName ?? m.source, m.tagName].joined(separator: " · "))
                    .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, Theme.Space.xs)
        .accessibilityIdentifier("inbox-mute")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            Button { unmute(m) } label: { Label("Unmute [Inbox Mutes View 8d0173]", systemImage: Theme.Icon.unmute) }.tint(Theme.accent)
        }
        .contextMenu {
            Button { unmute(m) } label: { Label("Unmute [Inbox Mutes View 8d0173]", systemImage: Theme.Icon.unmute) }
        }
    }

    private func load() async {
        guard let api = model.api else { return }
        do {
            state = .loaded(try await api.inboxMutes().sorted { ($0.createdAt ?? .distantPast) > ($1.createdAt ?? .distantPast) })
        } catch {
            if error.isCancellation { return } // the sheet closed: not a failure
            if case .loaded = state { model.show(error) } else { state = .failed(Self.said(error)) }
        }
    }

    /// A first load's failure in words: the hub's own message, else no network or no answer in the app's
    /// language (the system's text for those follows the phone's language, not the app's).
    private static func said(_ error: Error) -> String {
        if let shown = error.shown { return shown }
        return error.isOffline ? String(localized: "Can't reach the hub") : String(localized: "The hub didn't respond. Try again later.")
    }

    private func unmute(_ m: InboxMute) {
        guard case .loaded(let list) = state else { return }
        withAnimation(Theme.Motion.standard) { state = .loaded(list.filter { $0.id != m.id }) }
        Task {
            do {
                try await model.api?.unmuteInbox(m.id)
            } catch {
                state = .loaded(list)
                model.show(error)
            }
        }
    }
}
