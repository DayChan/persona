import SwiftUI

/// One personal agent session: its messages (oldest at the top, "Load Earlier" for older pages), tool calls as compact
/// rows, and a send box. A send carries a fresh clientId; a retry reuses it, so the hub never sends twice. The bridge
/// does not stream: replies arrive through the hub's `agents` events (it re-announces at 4s/15s/45s after a send).
struct PersonalAgentSessionView: View {
    @Environment(AppModel.self) private var model
    @State var session: PersonalAgentSession
    @State private var messages: [PersonalAgentMessage] = []
    @State private var cursor: String?
    @State private var hasMore = false
    @State private var loadingOlder = false
    @State private var error: String?
    @State private var draft = ""
    @State private var pending: [Pending] = []
    /// The newest message id when /read was last called: every reload while open marks read (the list's
    /// unreadCount may be stale), but not again for the same tail, so the read's own change event does not loop.
    @State private var readTail: String?

    struct Pending: Identifiable, Hashable {
        var id: String // clientId
        var text: String
        var sentAt: Date
        var state: State
        enum State: Hashable { case sending, sent, failed(String) }
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Theme.Space.m) {
                    if hasMore {
                        Button(loadingOlder ? String(localized: "Loading… [Personal Agent Session View 4927a5]") : String(localized: "Load Earlier")) { Task { await loadOlder() } }
                            .lineLimit(1)
                            .disabled(loadingOlder).frame(maxWidth: .infinity)
                    }
                    if let error {
                        Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    }
                    ForEach(messages) { m in PersonalAgentBubble(message: m) }
                    ForEach(pending) { p in pendingBubble(p) }
                    Color.clear.frame(height: Theme.Size.line).id("bottom")
                }
                .padding(Theme.Space.l)
            }
            .onChange(of: messages.count + pending.count) { _, _ in
                withAnimation { proxy.scrollTo("bottom", anchor: .bottom) }
            }
        }
        .safeAreaInset(edge: .bottom) { composer }
        .navigationTitle(session.title)
        .navigationBarTitleDisplayMode(.inline)
        .pageToolbar {
            ToolbarItem(placement: .principal) {
                HStack(spacing: Theme.Space.s) {
                    PersonalAgentAvatar(session: session, size: 24)
                    Text(session.title).font(Theme.Font.heading).lineLimit(1)
                }
            }
        }
        .task { await reload(markRead: true) }
        .task {
            for await _ in PersonalAgentSync.shared.changes(of: session.id) { await reload(markRead: false) }
        }
    }

    @ViewBuilder private var composer: some View {
        VStack(spacing: Theme.Space.xs) {
            if !session.canSend {
                Text("You can't send messages to this session from Persona. Reply on the Lark gateway's web page.")
                    .font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            HStack(alignment: .bottom, spacing: Theme.Space.s) {
                TextField(String(localized: "Message \(PersonalAgentArrange.providerLabel(session.provider)) [Personal Agent Session View 72a8a3]"), text: $draft, axis: .vertical)
                    .lineLimit(1...6)
                    .textFieldStyle(.roundedBorder)
                    .disabled(!session.canSend)
                Button {
                    let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !text.isEmpty else { return }
                    draft = ""
                    let p = Pending(id: UUID().uuidString.lowercased(), text: text, sentAt: Date(), state: .sending)
                    pending.append(p)
                    Task { await send(p) }
                } label: {
                    ThemedIcon(Theme.Icon.submit).font(Theme.Font.title)
                }
                .disabled(!session.canSend || draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                // Match the main chat composer (ComposerRow): ⌘⏎ sends. SwiftUI does not fire the shortcut while
                // the button is disabled, and keyboardShortcut is cross-platform (iPad hardware keyboards get it too).
                .keyboardShortcut(.return, modifiers: .command)
            }
        }
        .padding(.horizontal, Theme.Space.l).padding(.vertical, Theme.Space.s)
        .background(.bar)
    }

    @ViewBuilder private func pendingBubble(_ p: Pending) -> some View {
        VStack(alignment: .trailing, spacing: Theme.Space.xxs) {
            PersonalAgentBubble(message: PersonalAgentMessage(externalId: "local:" + p.id, role: "user",
                                                              blocks: [PersonalAgentBlock(text: p.text)]))
            switch p.state {
            case .sending: Text("Sending… [Personal Agent Session View 364970]").font(Theme.Font.label).foregroundStyle(.secondary)
            case .sent: Text("Sent, waiting for a reply").font(Theme.Font.label).foregroundStyle(.secondary)
            case .failed(let why):
                HStack {
                    Text("Failed: \(why)").font(Theme.Font.label).foregroundStyle(Theme.danger).lineLimit(2)
                    // The reason gives way; the buttons keep their one-line width.
                    Button("Retry [Components b8784c]") { Task { await send(p) } }.font(Theme.Font.label).lineLimit(1).fixedSize()
                    Button("Discard") { pending.removeAll { $0.id == p.id } }.font(Theme.Font.label).lineLimit(1).fixedSize()
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }

    private func send(_ p: Pending) async {
        guard let api = model.api else { return }
        set(p.id, .sending)
        do {
            let r = try await api.sendPersonalAgent(session.id, clientId: p.id, text: p.text)
            if r.outbox.status == "failed" { set(p.id, .failed(r.outbox.error ?? String(localized: "Send failed"))) } else { set(p.id, .sent) }
            await reload(markRead: false)
        } catch {
            set(p.id, .failed(error.localizedDescription))
        }
    }

    private func set(_ id: String, _ state: Pending.State) {
        if let i = pending.firstIndex(where: { $0.id == id }) { pending[i].state = state }
    }

    private func reload(markRead: Bool) async {
        guard let api = model.api else { return }
        do {
            let page = try await api.personalAgentMessages(session.id)
            if let s = page.session { session = s }
            // Older pages already loaded stay; the newest page replaces the tail it overlaps.
            let newest = Set(page.messages.map(\.id))
            let kept = messages.filter { !newest.contains($0.id) && ($0.position ?? .max) < (page.messages.first?.position ?? .max) }
            messages = kept + page.messages
            if cursor == nil || kept.isEmpty { cursor = page.nextCursor; hasMore = page.hasMore }
            settlePending()
            error = nil
            let tail = page.messages.last?.id ?? ""
            if markRead || tail != readTail {
                readTail = tail
                try? await api.markPersonalAgentRead(session.id)
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// The stored copy of a sent message has no `local` field (observed 2026-10-31): a sent bubble goes away once a
    /// user message with the same words (as the agent chat compares them, `PendingMessage.same`) shows up after it was
    /// sent.
    private func settlePending() {
        pending.removeAll { p in
            guard p.state == .sent else { return false }
            let since = Int64((p.sentAt.timeIntervalSince1970 - 60) * 1000)
            return messages.contains { $0.role == "user" && PendingMessage.same($0.plainText, p.text) && ($0.createTimeMs ?? 0) >= since }
                || messages.contains { $0.local?.clientId == p.id }
        }
    }

    private func loadOlder() async {
        guard let api = model.api, let c = cursor, !loadingOlder else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        do {
            let page = try await api.personalAgentMessages(session.id, cursor: c)
            let have = Set(messages.map(\.id))
            messages = page.messages.filter { !have.contains($0.id) } + messages
            cursor = page.nextCursor
            hasMore = page.hasMore
        } catch {
            self.error = error.localizedDescription
        }
    }
}

struct PersonalAgentBubble: View {
    let message: PersonalAgentMessage
    private var mine: Bool { message.role == "user" }

    var body: some View {
        VStack(alignment: mine ? .trailing : .leading, spacing: Theme.Space.xs) {
            ForEach(Array(message.blocks.enumerated()), id: \.offset) { _, b in block(b) }
        }
        .frame(maxWidth: .infinity, alignment: mine ? .trailing : .leading)
    }

    @ViewBuilder private func block(_ b: PersonalAgentBlock) -> some View {
        switch b.type {
        case "text":
            Text(b.text ?? "")
                .textSelection(.enabled)
                .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s)
                .background(RoundedRectangle(cornerRadius: Theme.Radius.bubble)
                    .fill(mine ? Theme.accent.opacity(0.18) : Theme.systemCard))
        case "reaction" where !(b.emoji ?? "").isEmpty:
            HStack(spacing: Theme.Space.xs) {
                Text(b.emoji ?? "").font(Theme.Font.title)
                Text("Reacted").font(Theme.Font.label).foregroundStyle(.secondary)
            }
            .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xxs)
            .background(Capsule().fill(Theme.Fill.chip))
        case "tool-call", "tool-result":
            DisclosureGroup {
                Text(Self.summary(b.raw)).font(Theme.Font.label.monospaced()).textSelection(.enabled)
            } label: {
                Label([b.name ?? b.type, b.status, b.summary].compactMap { $0 }.joined(separator: " · "),
                      systemImage: b.type == "tool-call" ? Theme.Icon.unknownTool : "checkmark.rectangle")
                    .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
            .padding(.horizontal, Theme.Space.m)
        default:
            Text("[\(b.type)] " + Self.summary(b.raw)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(3)
        }
    }

    static func summary(_ v: JSONValue) -> String {
        guard let d = try? JSONEncoder().encode(v), let s = String(data: d, encoding: .utf8) else { return "" }
        return s.count > 2000 ? String(s.prefix(2000)) + "…" : s
    }
}
