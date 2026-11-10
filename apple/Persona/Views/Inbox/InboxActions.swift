import SwiftUI

// What can be done with one inbox item besides reading it (docs/assistant/inbox.md): AI Draft Reply (a reply in the user's voice,
// edited and sent from here), Hand to AI (the item as <persona-context> to the assistant or an agent), Done.

/// The strip under the navigation bar of an opened item: the AI's summary, its Thread (`InboxLineSection`) and the two
/// actions.
struct InboxActionBar: View {
    var item: InboxItem
    var draft: () -> Void
    var handOff: () -> Void
    @Environment(\.openURL) private var openURL
    @State private var line: InboxTree?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let s = item.summary, !s.isEmpty {
                (Text(Image(systemName: Theme.Icon.ai)).foregroundStyle(Theme.ai) + Text(" " + s))
                    .font(Theme.Font.secondary)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            InboxLineSection(tree: line)
            HStack(spacing: Theme.Space.s) {
                // A connector's thread that cannot be answered (a CI run) has only Hand to AI.
                if item.connector.map({ $0.canReply == true }) ?? true {
                    action(item.agentDraft == nil ? String(localized: "AI Draft Reply") : String(localized: "View Draft"), symbol: Theme.Icon.draftReply, prominent: true, perform: draft)
                } else if let open = item.connector?.openURL, let url = Self.systemURL(open, draft: item.agentDraft) {
                    // Answered in the source's own app (Reply in Messages: Messages with the number, and the AI twin's draft;
                    // Reply in Telegram: the conversation in Telegram; else Open).
                    let sms = url.scheme == "sms"
                    action(Self.openTitle(item, sms: sms), symbol: sms ? Theme.Icon.messagesApp : Theme.Icon.openExternal, prominent: true) {
                        openURL(url)
                    }
                }
                action(String(localized: "Hand to AI"), symbol: Theme.Icon.handToAI, prominent: false, perform: handOff)
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .background(.bar)
        .overlay(alignment: .bottom) { Divider() }
        .inboxLine(item, tree: $line)
    }

    /// What the button that opens the item's `open_url` says: where the user answers it, else just Open.
    nonisolated static func openTitle(_ item: InboxItem, sms: Bool) -> String {
        if sms { return String(localized: "Reply in Messages") }
        if let app = ConnectorText.profile(item.source).app { return String(localized: "Reply in \(app)") }
        return String(localized: "Open")
    }

    /// The item's `open_url` for the system; an sms: one carries the draft as its body.
    nonisolated static func systemURL(_ raw: String, draft: String?) -> URL? {
        guard var url = URL(string: raw) else { return nil }
        if url.scheme == "sms", let draft, !draft.isEmpty,
           let body = draft.addingPercentEncoding(withAllowedCharacters: .alphanumerics),
           let withBody = URL(string: raw + "&body=" + body) {
            url = withBody
        }
        return url
    }

    private func action(_ title: String, symbol: String, prominent: Bool, perform: @escaping () -> Void) -> some View {
        Button(action: perform) {
            Label(title, systemImage: symbol)
                .font(Theme.Font.secondaryStrong)
                .lineLimit(1)
                .minimumScaleFactor(0.8)
                .frame(maxWidth: .infinity)
                .padding(.vertical, Theme.Space.s)
                .foregroundStyle(prominent ? Color.white : Theme.accent)
                .background(prominent ? AnyShapeStyle(Theme.accent) : AnyShapeStyle(Theme.accent.opacity(0.12)), in: .capsule)
        }
        .buttonStyle(.plain)
    }
}

// MARK: - AI Draft Reply

/// The drafted reply, editable; Rewrite asks again with an instruction; Send replies in the Feishu chat or by mail and
/// files the item as done.
struct InboxDraftSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let item: InboxItem
    var accounts: [MailAccount]
    /// Fired on the main actor only after the reply actually went out (InboxStore.send returns true).
    /// The inbox detail presents this sheet and pops itself on send success (#4258); the list leaves it nil.
    var onSent: (() -> Void)?
    @State private var text = ""
    @State private var instruction = ""
    @State private var loading = false
    @State private var error: String?
    @FocusState private var editing: Bool
    /// The AI twin's proposed status moves the user keeps (all of them to start with).
    @State private var kept: Set<InboxItem.DocChange>?
    private var proposed: [InboxItem.DocChange] { item.agentDraft == nil ? [] : item.agent?.changes ?? [] }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if let a = item.agent, item.agentDraft != nil {
                    agentNote(a)
                }
                if !proposed.isEmpty { changesList }
                ZStack(alignment: .topLeading) {
                    TextEditor(text: $text)
                        .focused($editing)
                        .scrollContentBackground(.hidden)
                        .padding(.horizontal, Theme.Space.m)
                        .padding(.top, Theme.Space.s)
                        .disabled(loading)
                        .opacity(loading ? 0.4 : 1)
                    if loading {
                        HStack(spacing: Theme.Space.s) {
                            ProgressView()
                            Text("AI is drafting…").foregroundStyle(.secondary)
                        }
                        .padding(Theme.Space.xl)
                    }
                }
                if let error {
                    Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, Theme.Space.l).padding(.bottom, Theme.Space.sm)
                }
                Divider()
                HStack(spacing: Theme.Space.s) {
                    TextField(String(localized: "Ask AI to change the tone, key points…"), text: $instruction, axis: .vertical)
                        .lineLimit(1...3)
                        .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s)
                        .background(Color.secondary.opacity(0.12), in: .rect(cornerRadius: Theme.Radius.card))
                        .onSubmit { Task { await load() } }
                    Button { Task { await load() } } label: {
                        Label("Rewrite", systemImage: Theme.Icon.retry).labelStyle(.titleAndIcon).lineLimit(1)
                    }
                    .buttonStyle(.bordered)
                    .disabled(loading)
                }
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.m)
            }
            .themedPage()
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await send() } } label: {
                        Text("Send").lineLimit(1)
                    }
                    .disabled(loading || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && keptChanges.isEmpty)
                }
            }
            .task {
                guard text.isEmpty else { return }
                if let u = InboxStore.shared.unsentReplies[item.key] { text = u } else if let d = item.agentDraft { text = d } else { await load() }
            }
        }
        .presentationDetents([.medium, .large])
    }

    /// What the AI twin found, and a way into its conversation.
    private func agentNote(_ a: InboxItem.Agent) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            if let note = a.note, !note.isEmpty {
                (Text(Image(systemName: Theme.Icon.ai)).foregroundStyle(Theme.accent) + Text(" " + note))
                    .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(4)
            }
            if let key = a.sessionKey {
                Button {
                    model.openAssistantConversation(model.inHub(key), manager: item.manager)
                    dismiss()
                } label: {
                    Label("See How the AI Twin Handled It", systemImage: Theme.Icon.list).font(Theme.Font.captionMedium).lineLimit(1)
                }
                .buttonStyle(.borderless)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.m)
        .background(Theme.accent.opacity(0.06))
    }

    // Spelled out instead of one nested ternary: Swift 6.2 (Xcode 26.0) cannot type-check that in reasonable time.
    private var title: String {
        if item.moment != nil { return String(localized: "Comment on Moments") }
        if item.doc != nil { return String(localized: "Comment on \(item.title)") }
        if let connector = item.connector {
            return String(localized: "Comment on \(connector.reference?.split(separator: "/").last.map(String.init) ?? item.title)")
        }
        return item.isLark ? String(localized: "Replying to \(item.title)") : String(localized: "Reply to Email")
    }

    private var keptChanges: [InboxItem.DocChange] { proposed.filter { (kept ?? Set(proposed)).contains($0) } }

    /// The status moves the AI twin proposes, each one a toggle; the kept ones are made when the reply is sent.
    private var changesList: some View {
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            Text("Also apply when sending").font(Theme.Font.labelStrong).foregroundStyle(.secondary)
            ForEach(proposed, id: \.self) { c in
                let on = (kept ?? Set(proposed)).contains(c)
                Button {
                    var k = kept ?? Set(proposed)
                    if on { k.remove(c) } else { k.insert(c) }
                    kept = k
                } label: {
                    Label(DocChangeText.line(c, in: item.doc), systemImage: on ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                        .font(Theme.Font.caption).lineLimit(1)
                        .foregroundStyle(on ? Color.primary : Color.secondary)
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Theme.Space.l)
        .padding(.vertical, Theme.Space.s)
    }

    private func load() async {
        guard let api = model.api?.manager(model.itemManager(item.manager)) else { return }
        loading = true
        defer { loading = false }
        do {
            text = try await api.inboxDraft(item.id, instruction: instruction)
            instruction = ""
            error = nil
        } catch {
            if let shown = error.shown { self.error = shown }
        }
    }

    /// Closes at once: the store files the item and sends in the background (a failure puts it back, with this text).
    /// The sheet always closes immediately; when opened from the inbox detail, the detail pops itself only if the
    /// send succeeded — a failure rolls the item back and keeps the draft for another try, so no kick to the list.
    private func send() async {
        let body = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let it = item, changes = keptChanges
        let onSent = onSent
        Task {
            if await InboxStore.shared.send(it, text: body, changes: changes) { onSent?() }
        }
        dismiss()
    }
}

// MARK: - Hand to AI

/// Hands the item to Persona (its main conversation by default, which may write back to the item, or a new one) or to a
/// running agent, as a <persona-context> plus what to do with it (`InboxHandOffModel`), and opens where it went. The Mac
/// and the iPhone's source pages (which have their own reply composer at the bottom) use it; an event's page has
/// `InboxHandOffBar` instead.
struct InboxHandOffSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let item: InboxItem
    @State private var instruction = ""
    @State private var handOff: InboxHandOffModel
    /// Where this item's source went last time (#2121): preselected and marked Last time.
    @State private var lastTarget: InboxHandOffModel.Target?
    /// That was an Agent that has ended: the main chat stands in, and the section says so.
    @State private var lastGone = false

    init(item: InboxItem) {
        self.item = item
        _handOff = State(initialValue: InboxHandOffModel.live(item, model: .shared))
    }

    private static let presets = InboxAsk.allCases.map(\.ask)

    /// The latest Agents, and the one this source went to last time even when it is older than those.
    private var agents: [Session] {
        let all = InboxHandOffModel.agents(model.allSessions, limit: .max) { model.machine($0)?.online == true }
        var shown = Array(all.prefix(8))
        if case .agent(let key)? = lastTarget, !shown.contains(where: { $0.key == key }), let s = all.first(where: { $0.key == key }) {
            shown.insert(s, at: 0)
        }
        return shown
    }

    private var memory: InboxHandOffMemory { InboxHandOffMemory(group: model.hubGroup) }

    /// "Last time" after a target this source went to last time.
    @ViewBuilder private func lastTimeMark(_ target: InboxHandOffModel.Target) -> some View {
        if lastTarget == target {
            Chip(text: String(localized: "Last time [Inbox Hand-Off]"), look: .tint(Theme.accent), compact: true)
        }
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Instructions") {
                    #if os(macOS)
                    // A grouped Mac form shows a titled field as label + trailing value: the request took the right half.
                    TextField(Self.presets[0], text: $instruction, prompt: Text(Self.presets[0]), axis: .vertical)
                        .labelsHidden().lineLimit(2...6)
                    #else
                    TextField(Self.presets[0], text: $instruction, axis: .vertical).lineLimit(2...6)
                    #endif
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: Theme.Space.s) {
                            ForEach(Self.presets, id: \.self) { p in
                                Button { instruction = p } label: {
                                    Text(p).font(Theme.Font.caption).lineLimit(1)
                                        .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.sm)
                                        .background(Color.secondary.opacity(0.12), in: .capsule)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }
                Section {
                    Picker("Hand To", selection: $handOff.target) {
                        HStack(spacing: Theme.Space.s) {
                            Label("Persona (Main Chat)", systemImage: Theme.Icon.persona).lineLimit(1)
                            lastTimeMark(.main)
                        }
                        .tag(InboxHandOffModel.Target.main)
                        HStack(spacing: Theme.Space.s) {
                            Label("Persona (New Chat)", systemImage: Theme.Icon.ai).lineLimit(1)
                            lastTimeMark(.newChat)
                        }
                        .tag(InboxHandOffModel.Target.newChat)
                        ForEach(agents) { s in
                            HStack(spacing: Theme.Space.s) {
                                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                    Text(s.displayTitle).lineLimit(1)
                                    Text(model.machine(s.machineRef)?.title ?? s.machineID).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                                }
                                lastTimeMark(.agent(s.key))
                            }
                            .tag(InboxHandOffModel.Target.agent(s.key))
                        }
                    }
                    .pickerStyle(.inline)
                    .labelsHidden()
                } header: {
                    Text("Hand To")
                } footer: {
                    if lastGone {
                        Text("The Agent this went to last time has ended, so it goes to Main Chat").foregroundStyle(Theme.cautionText)
                            .accessibilityIdentifier("handoff-last-gone")
                    }
                }
                Section {
                    Toggle("Also Mark as Done", isOn: Binding(get: { handOff.markDone }, set: { handOff.setMarkDone($0) }))
                        .disabled(handOff.sending)
                }
                if let error = handOff.error { Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
            }
            .onAppear {
                guard let r = memory.resolved(for: item, isListed: { InboxHandOffModel.takes($0, model: model) }) else { return }
                handOff.target = r.target
                lastTarget = r.lastTime ? r.target : nil
                lastGone = r.gone
            }
            .navigationTitle("Hand to AI")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await send() } } label: {
                        if handOff.sending { ProgressView() } else { Text("Send").lineLimit(1) }
                    }
                    .disabled(handOff.sending)
                }
            }
        }
    }

    private func send() async {
        let ask = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        guard await handOff.send(ask: ask.isEmpty ? Self.presets[0] : ask) else { return }
        if let key = InboxHandOffMemory.key(item) { memory.remember(key, target: handOff.target) }
        Haptics.success()
        handOff.open()
        dismiss()
    }

    /// The item as context the chat shows as a chip, then the ask.
    static func message(_ it: InboxItem, ask: String) -> String {
        let title = it.connector != nil ? "\(it.accountName) · \(it.title)" : it.isLark ? String(localized: "Lark · \(it.title)") : String(localized: "Email · \(it.title)")
        var text = it.context?.isEmpty == false ? it.context! : [it.sender ?? "", it.preview].filter { !$0.isEmpty }.joined(separator: "：") // i18n-ignore: context for the agent
        if let l = it.lark {
            let account = l.olAccount.map { "，在飞书账号 \($0)（lark_messages 传 account=\($0)）" } ?? "" // i18n-ignore: context for the agent
            text += "\n\n（飞书会话 feedId \(l.feedID)\(account)，可用飞书工具读更多上下文或回复）" // i18n-ignore: context for the agent
        }
        if let c = it.connector, let url = c.url { text += "\n\n（\(it.accountName)：\(url)）" } // i18n-ignore: context for the agent
        if let m = it.mail, let uid = m.uids.last {
            text += "\n\n（邮件 account_id \(m.accountID)，mailbox \(m.mailbox)，uid \(uid)，可用 mail_read 读全文、mail_send 回复）" // i18n-ignore
        }
        let ref = ContextRef(kind: "context", title: title, url: "", text: text)
        return ref.xml + "\n\n" + ask
    }
}
