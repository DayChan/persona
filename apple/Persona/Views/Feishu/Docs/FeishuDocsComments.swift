import SwiftUI

// MARK: - Comments

/// Every comment thread of a document: the quoted text and its replies; open threads first, resolved ones on their own
/// tab. Where the hub's Feishu plugin can write comments (`LarkFeatures.docComments`, #1290) each thread has Reply and
/// Resolve / Reopen, and the box at the bottom comments on the whole document, answers the thread picked, or comments
/// on the passage the reader sent here; typing @ finds people to mention.
struct FeishuDocsCommentsView: View {
    var url: String
    var title: String
    /// Opened to comment on a passage of the document (the reader's Comment): the box starts there.
    var passage: FeishuDocsCommentsModel.Target? = nil
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss
    @State private var model: FeishuDocsCommentsModel?
    @State private var resolved = false
    @FocusState private var focused: Bool

    private var writes: Bool { app.larkFeatures.docComments }

    var body: some View {
        let model = model
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Theme.Space.m) {
                Picker("Status", selection: $resolved) {
                    Text("Open \(model?.threads(resolved: false).count ?? 0)").tag(false)
                    Text("Resolved \(model?.threads(resolved: true).count ?? 0)").tag(true)
                }
                .pickerStyle(.segmented)
                .padding(.bottom, Theme.Space.xs)
                if let model {
                    let shown = model.threads(resolved: resolved)
                    if model.threads == nil {
                        FeishuDocsListState(loading: model.loadError == nil, error: model.loadError, empty: (Theme.Icon.conversation, "", ""),
                                            retry: { await model.load() })
                    } else if shown.isEmpty {
                        EmptyStateView(symbol: Theme.Icon.conversation, title: resolved ? String(localized: "No resolved comments") : String(localized: "No open comments"),
                                       message: resolved ? String(localized: "Resolved comments are kept here.") : (writes ? String(localized: "Comments on this doc in Lark show up here. [Feishu Docs Comments af32fa]") : String(localized: "Comments on this doc in Lark show up here. [Feishu Docs Comments 5274c8]")))
                            .padding(.top, Theme.Space.xxl)
                    } else {
                        ForEach(shown) { t in
                            FeishuDocsThreadCard(thread: t, actions: writes ? actions(model, t) : nil)
                        }
                    }
                }
            }
            .padding(Theme.Space.l)
        }
        .scrollDismissesKeyboard(.interactively)
        .themedPage()
        .safeAreaInset(edge: .bottom) {
            if writes, let model { composer(model) }
        }
        .navigationTitle("Comments")
        .navigationBarTitleDisplayMode(.inline)
        #if os(macOS)
        .macSheetDone { dismiss() }
        #else
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button { dismiss() } label: { Label("Close", systemImage: Theme.Icon.close) }
            }
        }
        #endif
        .refreshable { await model?.load() }
        .onChange(of: model?.writeError) { _, message in
            guard let message else { return }
            Haptics.error()
            app.toast = Toast(message, style: .error)
        }
        .task {
            LarkMedia.shared.loadEmojiCatalog()
            guard self.model == nil, let api = app.api else { return }
            let m = FeishuDocsCommentsModel(url: url, api: api, target: passage ?? .document)
            #if DEBUG
            // Screenshots: `-showDocCommentDraft <text>` is in the box when it appears (not focused: its @ is looked up at the end);
            // `-showDocCommentReply <thread id>` answers that thread.
            let d = UserDefaults.standard
            if let draft = d.string(forKey: "showDocCommentDraft") { m.draft = draft }
            #endif
            self.model = m
            if passage != nil { focused = true }
            await m.load()
            #if DEBUG
            if let id = d.string(forKey: "showDocCommentReply"), let t = m.threads?.first(where: { $0.id == id }) { m.reply(to: t); focused = true }
            #endif
        }
    }

    private func actions(_ model: FeishuDocsCommentsModel, _ t: FeishuDocsComments.Thread) -> FeishuDocsThreadCard.Actions {
        FeishuDocsThreadCard.Actions(
            replying: { if case .thread(let id, _, _) = model.target { id == t.id } else { false } }(),
            resolving: model.resolving.contains(t.id),
            reply: {
                model.reply(to: t)
                focused = true
            },
            setResolved: { r in Task { await model.setResolved(t, r) } })
    }

    @ViewBuilder private func composer(_ model: FeishuDocsCommentsModel) -> some View {
        @Bindable var model = model
        LarkComposer(placeholder: placeholder(model.target), text: $model.draft, focused: $focused,
                     replyTo: replyBar(model.target), cancelReply: { model.target = .document },
                     quote: passageBar(model), boxID: "docComments.box", sendID: "docComments.send",
                     findPeople: { [model] q in try await model.people(q) }, mentions: $model.mentions) {
            Task { await model.send() }
        }
        .disabled(model.sending)
        .padding(.top, Theme.Space.s)
    }

    private func placeholder(_ t: FeishuDocsCommentsModel.Target) -> String {
        switch t {
        case .document: String(localized: "Comment on the doc")
        case .thread: String(localized: "Reply")
        case .passage: String(localized: "Comment on This")
        }
    }

    /// Comment on This over the box while it comments on a passage; × goes back to commenting on the document.
    private func passageBar(_ model: FeishuDocsCommentsModel) -> (heading: String, summary: String, removeLabel: String, remove: () -> Void)? {
        guard case .passage(let quote, _) = model.target else { return nil }
        return (String(localized: "Comment on This"), quote, String(localized: "Don't comment on this"), { model.target = .document })
    }

    private func replyBar(_ t: FeishuDocsCommentsModel.Target) -> LarkReplyTo? {
        guard case .thread(let id, let name, let text) = t else { return nil }
        return LarkReplyTo(id: id, senderName: name, text: text)
    }
}

struct FeishuDocsThreadCard: View {
    /// What the card can do where comments can be written; nil: read only.
    struct Actions {
        var replying: Bool
        var resolving: Bool
        var reply: () -> Void
        var setResolved: (Bool) -> Void
    }

    var thread: FeishuDocsComments.Thread
    var actions: Actions? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.sm) {
                if thread.whole {
                    Text("Doc comments").font(Theme.Font.label).foregroundStyle(.secondary)
                } else if !thread.quote.isEmpty {
                    Text(thread.quote)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                        .padding(.leading, Theme.Space.s)
                        .overlay(alignment: .leading) { Capsule().fill(LarkColor.hue("yellow")).frame(width: Theme.Size.bar) }
                }
                Spacer(minLength: 0)
                if thread.resolved { FeishuDocsTag(text: String(localized: "Resolved"), hue: "green") }
                if thread.visibility != nil { FeishuDocsTag(text: String(localized: "Limited [Feishu Docs Comments 6af1b6]"), hue: "grey") }
            }
            ForEach(thread.replies) { r in
                HStack(alignment: .top, spacing: Theme.Space.s) {
                    LarkAvatarImage(name: r.author.name, url: r.author.avatarUrl, size: Theme.Size.wellXS)
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        HStack(spacing: Theme.Space.sm) {
                            Text(r.author.name).font(Theme.Font.captionStrong).lineLimit(1)
                            Text(FeishuTime.short(r.created)).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1)
                            if r.edited == true { Text("Edited").font(Theme.Font.label).foregroundStyle(.tertiary) }
                        }
                        Text(replyText(r)).font(Theme.Font.secondary).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                        if let n = r.imageCount, n > 0 {
                            Label("\(n) images", systemImage: Theme.Icon.photo).font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                        if let rs = r.reactions, !rs.isEmpty {
                            HStack(spacing: Theme.Space.sm) {
                                ForEach(rs, id: \.key) { re in
                                    HStack(spacing: Theme.Space.xs) {
                                        LarkEmojiImage(key: re.key, size: 16)
                                        Text("\(re.count)").font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary)
                                    }
                                    .padding(.horizontal, Theme.Space.sm).padding(.vertical, Theme.Space.xxs)
                                    .background(Color.primary.opacity(0.05), in: .capsule)
                                }
                            }
                        }
                    }
                }
            }
            if let actions { footer(actions) }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(LarkColor.cardBackground, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                .strokeBorder(actions?.replying == true ? LarkColor.blue : LarkColor.cardStroke,
                              lineWidth: actions?.replying == true ? Theme.Size.line : Theme.Size.hairline)
        }
        .animation(Theme.Motion.state, value: actions?.replying)
    }

    /// Reply and Resolve / Reopen, under the replies (Feishu's thread footer).
    private func footer(_ a: Actions) -> some View {
        HStack(spacing: Theme.Space.l) {
            if !thread.resolved {
                Button(action: a.reply) {
                    Label("Reply", systemImage: Theme.Icon.reply).lineLimit(1)
                }
                .accessibilityIdentifier("docComments.reply." + thread.id)
            }
            Button { a.setResolved(!thread.resolved) } label: {
                if a.resolving {
                    ProgressView().controlSize(.small)
                } else {
                    Label(thread.resolved ? String(localized: "Reopen") : String(localized: "Resolve"), systemImage: thread.resolved ? Theme.Icon.undo : Theme.Icon.done).lineLimit(1)
                }
            }
            .disabled(a.resolving)
            .accessibilityIdentifier("docComments.resolve." + thread.id)
            Spacer(minLength: 0)
        }
        .font(Theme.Font.captionStrong)
        .foregroundStyle(.secondary)
        .buttonStyle(.plain)
        .padding(.leading, Theme.Size.wellXS + Theme.Space.s) // under the replies' text, past the avatars
    }

    private func replyText(_ r: FeishuDocsComments.Reply) -> AttributedString {
        var out = AttributedString()
        for seg in r.content {
            var s = AttributedString(seg.text)
            switch seg.kind {
            case "mention": s.foregroundColor = LarkColor.blue
            case "link":
                s.foregroundColor = LarkColor.blue
                if let h = seg.href { s.link = URL(string: h) }
            default: break
            }
            out += s
        }
        return out
    }
}

// MARK: - Commenting on a passage

extension EnvironmentValues {
    /// The reader's Comment on This (#1290): opens the comments with the box on that passage (its text, the block it is in when
    /// the reader knows). Nil where comments cannot be written.
    @Entry var feishuDocComment: ((String, String?) -> Void)? = nil
}

/// A paragraph of a document the user can comment on: a long press offers Copy (when the document lets its text be
/// copied), Comment on This and Select Text to Comment… (the paragraph in a text view, where Comment comments on the selected words). It takes
/// the place of the text selection, whose menu takes no items of ours.
struct FeishuDocsCommentable: ViewModifier {
    var text: String
    var blockID: String?
    var copyable: Bool
    var comment: (String, String?) -> Void
    @State private var picking = false

    func body(content: Content) -> some View {
        content
            .contextMenu {
                if copyable {
                    Button { UIPasteboard.general.string = text; Haptics.success() } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
                }
                Button { comment(text, blockID) } label: { Label("Comment on This", systemImage: Theme.Icon.conversation) }
                Button { picking = true } label: { Label("Select Text to Comment…", systemImage: Theme.Icon.select) }
            }
            .sheet(isPresented: $picking) {
                PickTextSheet(text: text, actions: [PickAction(title: String(localized: "Comments"), icon: Theme.Icon.conversation, kind: "comment")]) { picked, _ in
                    comment(picked, blockID)
                }
            }
    }
}
