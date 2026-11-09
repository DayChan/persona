import SwiftUI

// An inbox item of one of the hub's connectors (GitHub…), drawn the same for every source from the item's
// `connector` block (docs/plans/plan-connectors.md「六、App 侧」). No pages for browsing the source: the thread as the hub
// read it, and the page itself in the app's link sheet.

/// A connector's item opened: a chat app's conversation as its own page (ConnectorThreadView, a connector with
/// `threads`: Telegram), a GitHub pull request or issue as GitHub's thread (GitHubThreadView); otherwise, or while
/// that cannot be read (a CI run, a rate limit), what the hub collected about it.
struct InboxConnectorDetailView: View {
    var item: InboxItem
    @State private var thread: ConnectorThreadModel

    init(item: InboxItem) {
        self.item = item
        _thread = State(initialValue: ConnectorThreadModel(connector: item.source, thread: item.connector?.thread ?? ""))
    }

    var body: some View {
        if thread.loaded && (thread.unavailable || thread.chat.map { !$0.isChat && !$0.isGitHub } == true) {
            InboxConnectorTextView(item: item, problem: thread.failure, reread: reread)
        } else if thread.chat?.isGitHub == true {
            GitHubThreadView(model: thread, item: item)
                .navigationTitle(item.accountName)
                .navigationBarTitleDisplayMode(.inline)
        } else {
            ConnectorThreadView(model: thread, source: item.accountName)
                .navigationTitle(thread.chat?.name ?? item.title)
                .navigationSubtitle(ThreadLayout.subtitle(thread.chat, source: item.accountName))
                .navigationBarTitleDisplayMode(.inline)
        }
    }

    /// The text page's Retry reads the conversation page again first and says whether that page now takes the text's
    /// place (read and a conversation); nil when there is no page to read (a CI run) or it was already read.
    private var reread: (() async -> Bool)? {
        guard thread.unavailable, !thread.thread.isEmpty else { return nil }
        return {
            await thread.load()
            return !thread.unavailable && thread.chat.map { $0.isChat || $0.isGitHub } == true
        }
    }
}

/// What the hub collected about a connector's item (the description and the newest comments, read for triage) and
/// Open in GitHub. The action bar above it drafts the reply, which goes where `reply_to` says.
struct InboxConnectorTextView: View {
    var item: InboxItem
    /// Why the conversation page could not be read (a rate limit), said over the text.
    var problem: String? = nil
    /// Reads the conversation page again (it could not be read the first time), true when it was read and replaces
    /// this page; nil when there is none to read.
    var reread: (() async -> Bool)? = nil
    @Environment(AppModel.self) private var model
    @State private var context: String?
    @State private var failure: String?
    @State private var link: LinkTarget?
    @State private var retrying = false

    private var connector: InboxItem.Connector? { item.connector }
    private var showsProblem: Bool { problem?.isEmpty == false && connector?.thread.isEmpty == false }

    var body: some View {
        List {
            Section {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    HStack(spacing: Theme.Space.sm) {
                        if let brand = ConnectorText.brand(item.source) {
                            BrandMark(brand, side: Theme.Size.iconS)
                        }
                        Text([item.accountName, connector?.reference ?? ""].filter { !$0.isEmpty }.joined(separator: " · "))
                            .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                        if let label = item.kindLabel {
                            Chip(text: label, look: item.kind == "mention" ? .tone(StatusPalette.needsYou) : .neutral)
                        }
                    }
                    Text(item.title).font(Theme.Font.title).textSelection(.enabled)
                }
                .padding(.vertical, Theme.Space.xs)
                if let url = connector?.url.flatMap(URL.init(string:)) {
                    Button { link = LinkTarget(url: url) } label: {
                        Label(ConnectorText.tightCJK(String(localized: "Open in \(item.accountName) [Inbox Connector Views 0e0e38]")), systemImage: Theme.Icon.safari).lineLimit(1)
                    }
                }
            } footer: {
                if let to = connector?.replyTo, connector?.canReply == true {
                    Text("Replies go to: \(to).")
                }
            }
            // Why its conversation page could not be read (a rate limit); an item without one (a CI run) has nothing to say.
            if showsProblem, let problem {
                Section {
                    Label(problem, systemImage: Theme.Icon.error).font(Theme.Font.label).foregroundStyle(.secondary)
                    if reread != nil { retryButton }
                }
            }
            Section("Content") {
                if let context {
                    // Nothing collected: the item's preview, as the web shows it, rather than a spinner that never ends.
                    Text(context.isEmpty ? item.preview : context).font(Theme.Font.secondary).textSelection(.enabled)
                } else if let failure {
                    Text(failure).font(Theme.Font.label).foregroundStyle(Theme.danger)
                    if !(showsProblem && reread != nil) { retryButton }
                } else {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
        }
        .themedPage()
        .navigationTitle(item.accountName)
        .navigationBarTitleDisplayMode(.inline)
        .task(id: item.id) { await load() }
        .refreshable { await retry() }
        #if os(macOS)
        .macLinkPanel($link)
        #else
        .sheet(item: $link) { t in
            if let key = model.assistantMain ?? model.assistantCurrent { LinkSheet(target: t, key: key) }
        }
        #endif
    }

    /// Retry in place of leaving and coming back: the conversation page first (read, it replaces this page), then the text.
    private var retryButton: some View {
        Button { Task { await retry() } } label: { Label("Retry [Components b8784c]", systemImage: Theme.Icon.retry).lineLimit(1) }
            .formButton()
            .disabled(retrying)
    }

    private func retry() async {
        guard !retrying else { return }
        retrying = true
        defer { retrying = false }
        failure = nil // the section shows it is reading again
        if let reread, await reread() { return }
        await load()
    }

    private func load() async {
        do {
            context = try await model.api?.manager(item.manager).inboxItem(item.id).context ?? ""
            failure = nil
        } catch {
            if error.isCancellation { return }
            // No network or no answer says so too: a nil failure left the section spinning for good.
            if let shown = error.shown {
                failure = String(localized: "Couldn't read the content: \(shown)")
            } else if error.isOffline {
                let why = String(localized: "Can't reach the hub")
                failure = String(localized: "Couldn't read the content: \(why)")
            } else {
                failure = String(localized: "The hub didn't respond. Try again later.") // a timeout: the hub may be up but slow
            }
        }
    }
}
