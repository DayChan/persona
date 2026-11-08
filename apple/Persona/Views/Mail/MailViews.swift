import AuthenticationServices
import SwiftUI
import WebKit

extension APIClient {
    struct AccountsResponse: Decodable { var accounts: [MailAccount] }
    struct MessagesResponse: Decodable {
        var messages: [MailSummary]
        var hasMore: Bool
        enum CodingKeys: String, CodingKey { case messages; case hasMore = "has_more" }
    }
    struct MessageResponse: Decodable { var message: MailMessage }
    struct MailboxesResponse: Decodable { var mailboxes: [Mailbox] }

    func mailAccounts() async throws -> [MailAccount] { try await get("/api/v1/mail/accounts", as: AccountsResponse.self).accounts }
    func mailMessages(account: String?, mailbox: String = "INBOX", beforeUID: UInt32? = nil, query: String = "") async throws -> MessagesResponse {
        try await get("/api/v1/mail/messages", query: ["account_id": account ?? "", "mailbox": mailbox, "limit": "40",
                                                         "before_uid": beforeUID.map { String($0) } ?? "", "q": query])
    }
    func mailMessage(account: String, uid: UInt32, mailbox: String) async throws -> MailMessage {
        try await get("/api/v1/mail/messages/\(account)/\(uid)", query: ["mailbox": mailbox], as: MessageResponse.self).message
    }
    struct FlagsBody: Encodable { var mailbox: String; var seen: Bool?; var flagged: Bool? }
    func mailFlags(account: String, uid: UInt32, _ body: FlagsBody) async throws {
        try await post("/api/v1/mail/messages/\(account)/\(uid)/flags", body: body)
    }
    struct SendMail: Encodable {
        var accountID: String
        var to: [MailAddress]
        var cc: [MailAddress]
        var subject: String
        var text: String
        var inReplyTo: String?
        /// Space-separated Message-IDs, the way the hub's `SendRequest` takes them.
        var references: String?
        enum CodingKeys: String, CodingKey {
            case to, cc, subject, text, references
            case accountID = "account_id", inReplyTo = "in_reply_to"
        }
    }
    func sendMail(_ m: SendMail) async throws { try await post("/api/v1/mail/send", body: m, timeout: 60) }
    struct AddAccount: Codable {
        var name: String
        var email: String
        var provider: String?
        var auth: String?
        var username: String?
        var password: String?
        var imapHost: String?
        var imapPort: Int?
        var smtpHost: String?
        var smtpPort: Int?
        var smtpSecurity: String?
        enum CodingKeys: String, CodingKey {
            case name, email, provider, auth, username, password
            case imapHost = "imap_host", imapPort = "imap_port", smtpHost = "smtp_host", smtpPort = "smtp_port", smtpSecurity = "smtp_security"
        }
    }
    struct AccountResponse: Decodable { var account: MailAccount }
    func addMailAccount(_ a: AddAccount) async throws -> MailAccount {
        try await post("/api/v1/mail/accounts", body: a, timeout: 60, as: AccountResponse.self).account
    }
    struct GoogleStart: Decodable {
        var authURL: URL
        var callbackScheme: String
        var state: String
        enum CodingKeys: String, CodingKey { case authURL = "auth_url", callbackScheme = "callback_scheme", state }
    }
    struct GoogleFinish: Encodable { var state: String; var code: String?; var error: String? }
    /// Whether this hub can start "Sign in with Google" (google.client_id in the manager's manager.json). An older hub has no
    /// such route (404): nil, and the page offers the button as before.
    struct GoogleConfig: Decodable { var configured: Bool }
    func googleConfig() async throws -> GoogleConfig? {
        do {
            return try await get("/api/v1/mail/oauth/google/config", as: GoogleConfig.self)
        } catch let e as APIError where e.status == 404 {
            return nil
        }
    }
    /// Begins Gmail's "Sign in with Google": the hub builds the consent URL (PKCE) for the app to open.
    func googleSignInStart() async throws -> GoogleStart {
        try await post("/api/v1/mail/oauth/google/start", as: GoogleStart.self)
    }
    /// Hands the callback's code to the hub, which exchanges it, checks IMAP / SMTP and saves the account.
    func googleSignInFinish(_ f: GoogleFinish) async throws -> MailAccount {
        try await post("/api/v1/mail/oauth/google/finish", body: f, timeout: 60, as: AccountResponse.self).account
    }
    func deleteMailAccount(_ id: String) async throws { try await delete("/api/v1/mail/accounts/\(id)") }

    /// Outlook's "Sign in with Microsoft" (docs/extensions/mail.md「Outlook 接入方式」): the client ID the user registered in
    /// Microsoft Entra (kept on the hub) and the redirect URI to register with it.
    struct MicrosoftConfig: Codable, Equatable {
        var configured: Bool
        var clientID: String?
        var source: String?
        var redirectURI: String
        enum CodingKeys: String, CodingKey { case configured, source, clientID = "client_id", redirectURI = "redirect_uri" }
    }
    struct MicrosoftFinish: Encodable {
        var state: String
        var code: String?
        var error: String?
        var errorDescription: String?
        enum CodingKeys: String, CodingKey { case state, code, error, errorDescription = "error_description" }
    }
    func microsoftConfig() async throws -> MicrosoftConfig { try await get("/api/v1/mail/oauth/microsoft/config") }
    /// Keeps the client ID on the hub ("" forgets it).
    func setMicrosoftClientID(_ id: String) async throws -> MicrosoftConfig {
        try JSON.decoder.decode(MicrosoftConfig.self, from: await request("PUT", "/api/v1/mail/oauth/microsoft/config", body: ["client_id": id]))
    }
    func microsoftSignInStart() async throws -> GoogleStart {
        try await post("/api/v1/mail/oauth/microsoft/start", as: GoogleStart.self)
    }
    /// Hands the callback to the hub (also an error: it says what to do about it, e.g. an admin has to consent).
    func microsoftSignInFinish(_ f: MicrosoftFinish) async throws -> MailAccount {
        try await post("/api/v1/mail/oauth/microsoft/finish", body: f, timeout: 60, as: AccountResponse.self).account
    }

}

struct MailDetailView: View {
    @Environment(AppModel.self) private var model
    var summary: MailSummary
    var accounts: [MailAccount]
    @State private var message: MailMessage?
    @State private var error: String?
    @State private var reply: ComposeDraft?
    @State private var askClaude = false
    @State private var webHeight: CGFloat = 300
    /// Settings › Translation: the mail in the target language (shown instead of the original until Show Original).
    @State private var translation: MailTranslation?
    @State private var showOriginal = false
    @State private var translating = false
    @State private var translateError: String?
    @AppStorage(TranslationPrefs.targetKey) private var target = TranslationPrefs.defaultTarget
    private var translated: MailTranslation? { showOriginal ? nil : translation }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                Text(translated.flatMap { $0.subject.isEmpty ? nil : $0.subject } ?? (summary.subject?.isEmpty == false ? summary.subject! : String(localized: "(No subject)")))
                    .font(Theme.Font.title)
                HStack(alignment: .top, spacing: Theme.Space.m) {
                    Monogram(name: summary.from?.display ?? "", fill: Theme.accent.opacity(0.15), ink: Theme.accent)
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text(summary.from?.display ?? "").font(Theme.Font.secondaryStrong)
                        Text(summary.from?.email ?? "").font(Theme.Font.label).foregroundStyle(.secondary)
                        if let to = message?.to, !to.isEmpty {
                            Text("To: \(Self.recipients(to))").font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2)
                        }
                    }
                    Spacer()
                    Text(summary.date.map { $0.formatted(date: .abbreviated, time: .shortened) } ?? "").font(Theme.Font.label).foregroundStyle(.secondary)
                }
                Divider()
                translationBar
                if let m = message {
                    if let t = translated {
                        Text(t.text).font(Theme.Font.body).lineHeight(.body).textSelection(.enabled)
                        if t.clipped == true {
                            Text("This email is long, so only the beginning was translated. See the original for the full text.").font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } else if let text = m.text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                        Text(text).font(Theme.Font.body).lineHeight(.body).textSelection(.enabled)
                    } else if let html = m.html, !html.isEmpty {
                        HTMLView(html: html, height: $webHeight).frame(height: webHeight)
                    }
                    if let atts = m.attachments, !atts.isEmpty {
                        VStack(alignment: .leading, spacing: Theme.Space.sm) {
                            ForEach(Array(atts.enumerated()), id: \.offset) { _, a in
                                Label("\(a.filename ?? String(localized: "Attachment"))  \(Int64(a.size ?? 0).storageText)", systemImage: Theme.Icon.attachment)
                                    .font(Theme.Font.caption)
                            }
                        }
                        .card(padding: 12)
                    }
                } else if let error {
                    Text(error).foregroundStyle(Theme.danger)
                } else {
                    ProgressView().frame(maxWidth: .infinity).padding(40)
                }
            }
            .padding(Theme.Space.xl)
        }
        .pageToolbar {
            ToolbarItemGroup(placement: .bottomBar) {
                Button { askClaude = true } label: { Label("Ask Claude [Mail Views c931e0]", systemImage: Theme.Icon.ai) }
                Spacer()
                Button {
                    reply = ComposeDraft.reply(to: summary, message: message)
                } label: { ThemedIcon(Theme.Icon.reply) }
                    .disabled(message == nil)
            }
        }
        .sheet(item: $reply) { d in ComposeView(draft: d, accounts: accounts) }
        .sheet(isPresented: $askClaude) {
            NewChatPrefilled(prompt: String(localized: "Use mail_read to read the email (account_id: \(summary.accountID), mailbox: \(summary.mailbox), uid: \(summary.uid), subject: \(summary.subject ?? "")), summarize the key points, and draft a suggested reply."))
        }
        #if DEBUG && os(macOS)
        // `-showMailAskClaude YES` (with an inbox mail open): the 「让 Claude 处理」 sheet.
        .onAppear { if UserDefaults.standard.bool(forKey: "showMailAskClaude") { askClaude = true } }
        #endif
        .task {
            guard let api = model.api else { return }
            do {
                message = try await api.mailMessage(account: summary.accountID, uid: summary.uid, mailbox: summary.mailbox)
                if let message { await translateIfNeeded(message) }
                if !summary.seen { try? await api.mailFlags(account: summary.accountID, uid: summary.uid, .init(mailbox: summary.mailbox, seen: true)) }
            } catch {
                self.error = error.shown
            }
        }
    }
}

extension MailDetailView {
    /// The recipients' names in one line, joined with the language's separator.
    static func recipients(_ to: [MailAddress]) -> String {
        to.map(\.display).joined(separator: String(localized: ", ", comment: "Separator between mail recipients"))
    }

    /// While translating, or once translated: what happened and the way back to the original (or to the translation).
    @ViewBuilder var translationBar: some View {
        if translating {
            HStack(spacing: Theme.Space.s) {
                ProgressView().controlSize(.small)
                Text("Translating to \(TranslationPrefs.name(of: target))…").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        } else if translation != nil {
            HStack(spacing: Theme.Space.s) {
                Label(showOriginal ? String(localized: "Original") : String(localized: "Translated to \(TranslationPrefs.name(of: target))"), systemImage: Theme.Icon.translate)
                    .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                Button(showOriginal ? String(localized: "Show Translation") : String(localized: "Show Original")) {
                    withAnimation(Theme.Motion.state) { showOriginal.toggle() }
                }
                .font(Theme.Font.captionMedium)
                .lineLimit(1)
                .buttonStyle(.borderless)
            }
        } else if let translateError {
            Label("Couldn't translate: \(translateError)", systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
        }
    }

    /// Settings › Translation is on for mail and this one is in another language: ask the hub.
    func translateIfNeeded(_ m: MailMessage) async {
        guard TranslationPrefs.translatesMail, translation == nil else { return }
        let body = m.text?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false ? m.text! : MailText.stripped(m.html ?? "")
        guard Translator.shared.isForeign((summary.subject ?? "") + "\n" + body, target: target) else { return }
        translating = true
        defer { translating = false }
        do {
            translation = try await Translator.shared.mail(account: summary.accountID, uid: summary.uid, mailbox: summary.mailbox, to: target)
            translateError = nil
        } catch {
            translateError = error.shown
        }
    }
}

/// A mail's HTML as rough text, enough to tell its language.
nonisolated enum MailText {
    static func stripped(_ html: String) -> String {
        var s = html
        for block in ["style", "script", "head"] {
            s = s.replacingOccurrences(of: "<\(block)[^>]*>[\\s\\S]*?</\(block)>", with: " ", options: [.regularExpression, .caseInsensitive])
        }
        s = s.replacingOccurrences(of: "<[^>]+>", with: " ", options: .regularExpression)
        return s.replacingOccurrences(of: "&nbsp;", with: " ")
    }
}

/// New chat sheet with the first message filled in (used by mail's "Ask Claude").
struct NewChatPrefilled: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var prompt: String

    var body: some View {
        NewChatView(initialPrompt: prompt) { s in
            dismiss()
            model.selectedTab = .chats
            model.openChat = s.key
        }
    }
}

#if os(iOS) || os(visionOS)
struct HTMLView: UIViewRepresentable {
    var html: String
    @Binding var height: CGFloat

    func makeCoordinator() -> Coordinator { Coordinator(height: $height) }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.defaultWebpagePreferences.allowsContentJavaScript = false
        let v = WKWebView(frame: .zero, configuration: config)
        v.scrollView.isScrollEnabled = false
        v.isOpaque = false
        v.backgroundColor = .clear
        v.navigationDelegate = context.coordinator
        return v
    }

    func updateUIView(_ v: WKWebView, context: Context) {
        let page = """
        <html><head><meta name="viewport" content="width=device-width, initial-scale=1">
        <style>body{font:-apple-system-body;margin:0;color:CanvasText;background:transparent;word-wrap:break-word}
        img{max-width:100%;height:auto} table{max-width:100%}
        @media (prefers-color-scheme: dark){body{color:#eee} a{color:#7ab}}</style></head><body>\(html)</body></html>
        """
        context.coordinator.openURL = context.environment.openURL
        if context.coordinator.loaded != html {
            context.coordinator.loaded = html
            v.loadHTMLString(page, baseURL: nil)
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var height: Binding<CGFloat>
        var loaded: String?
        /// The page's link handling (the inbox's: web pages in the in-app link sheet, other schemes to the system).
        var openURL: OpenURLAction?
        init(height: Binding<CGFloat>) { self.height = height }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            webView.evaluateJavaScript("document.body.scrollHeight") { value, _ in
                if let h = value as? CGFloat { Task { @MainActor in self.height.wrappedValue = h + 20 } }
            }
        }

        func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
            if action.navigationType == .linkActivated, let url = action.request.url {
                if let openURL { openURL(url) } else { await UIApplication.shared.open(url) }
                return .cancel
            }
            return .allow
        }
    }
}
#endif

struct ComposeDraft: Identifiable {
    let id = UUID()
    var accountID: String
    var to = ""
    var cc = ""
    var subject = ""
    var body = ""
    var inReplyTo: String?
    var references: [String]?

    static func reply(to s: MailSummary, message: MailMessage?) -> ComposeDraft {
        var d = ComposeDraft(accountID: s.accountID)
        d.to = s.from?.email ?? ""
        let subj = s.subject ?? ""
        d.subject = subj.lowercased().hasPrefix("re:") ? subj : "Re: " + subj
        d.inReplyTo = message?.messageID ?? s.messageID
        d.references = (message?.references ?? []) + [message?.messageID ?? s.messageID].compactMap { $0 }
        let quoted = (message?.text ?? "").split(separator: "\n", omittingEmptySubsequences: false).prefix(40).map { "> " + $0 }.joined(separator: "\n")
        d.body = String(localized: "\\n\\nOn \(s.date.map { $0.formatted() } ?? ""), \(s.from?.display ?? "") wrote:\\n") + quoted
        return d
    }
}

struct ComposeView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State var draft: ComposeDraft
    var accounts: [MailAccount]
    @State private var sending = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Picker("From", selection: $draft.accountID) {
                    ForEach(accounts) { a in Text(a.email).tag(a.id) }
                }
                TextField(String(localized: "To (comma-separated)"), text: $draft.to).keyboardType(.emailAddress).textInputAutocapitalization(.never)
                TextField(String(localized: "Cc"), text: $draft.cc).keyboardType(.emailAddress).textInputAutocapitalization(.never)
                TextField(String(localized: "Subject"), text: $draft.subject)
                Section {
                    #if os(macOS)
                    // A grouped Mac form shows a titled field as label + trailing value: the body took the right half.
                    TextField(String(localized: "Message [Mail Views d661c3]"), text: $draft.body, prompt: Text("Message [Mail Views d661c3]"), axis: .vertical).labelsHidden().lineLimit(10...40)
                    #else
                    TextField(String(localized: "Message [Mail Views d661c3]"), text: $draft.body, axis: .vertical).lineLimit(10...40)
                    #endif
                }
                if let error { Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
            }
            .themedPage()
            .navigationTitle("New Email")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await send() } } label: {
                        if sending { ProgressView() } else { Label("Send", systemImage: Theme.Icon.sendMail) }
                    }
                    .disabled(sending || draft.to.isEmpty)
                }
            }
        }
    }

    private func addresses(_ s: String) -> [MailAddress] {
        s.split(whereSeparator: { $0 == "," || $0 == "，" || $0 == ";" }) // i18n-ignore: parsing what was typed
            .map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
            .map { MailAddress(name: nil, email: $0) }
    }

    private func send() async {
        guard let api = model.api else { return }
        sending = true
        defer { sending = false }
        do {
            try await api.sendMail(.init(accountID: draft.accountID, to: addresses(draft.to), cc: addresses(draft.cc),
                                         subject: draft.subject, text: draft.body, inReplyTo: draft.inReplyTo,
                                         references: draft.references?.joined(separator: " ")))
            Haptics.success()
            model.toast = Toast(String(localized: "Sent"), style: .success)
            dismiss()
        } catch {
            self.error = error.shown
        }
    }
}

struct MailAccountsView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var accounts: [MailAccount] = []
    @State private var adding = false
    /// A sheet of its own (with Done), or a page pushed in Settings › Connectors › Mail.
    var inSheet = true

    var body: some View {
        if inSheet {
            NavigationStack { page.toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } } } }
        } else {
            page
        }
    }

    private var page: some View {
            SettingsForm {
                Section {
                    ForEach(accounts) { a in
                        HStack {
                            MailProviderMark(provider: a.provider ?? "")
                            VStack(alignment: .leading) {
                                Text(a.email).font(Theme.Font.heading)
                                Text(a.status == "error" ? (a.error ?? String(localized: "Error")) : a.auth == "oauth2" ? (a.provider == "outlook" ? String(localized: "Outlook · Microsoft account") : String(localized: "Gmail · Google account")) : (a.provider == "feishu" ? String(localized: "Lark Mail") : (a.imapHost ?? "")))
                                    .font(Theme.Font.label).foregroundStyle(a.status == "error" ? Theme.danger : .secondary)
                            }
                        }
                        #if os(macOS)
                        // A Mac form has no swipe to delete: the row's menu removes the account (#1221).
                        .contextMenu {
                            Button("Delete Account", role: .destructive) {
                                Task { await delete([a.id]) }
                            }
                        }
                        #endif
                    }
                    .onDelete { idx in
                        let ids = idx.map { accounts[$0].id }
                        Task { await delete(ids) }
                    }
                }
                Section {
                    Button { adding = true } label: { Label("Add Mail Account", systemImage: "plus.circle.fill") }
                }
            }
            .themedPage()
            .detailTitle(String(localized: "Mail [Mail Views 730752]"))
            .navigationBarTitleDisplayMode(.inline)
            .sheet(isPresented: $adding, onDismiss: { Task { await load() } }) { AddMailAccountView() }
            .task { await load() }
            #if DEBUG
            .onAppear { if UserDefaults.standard.bool(forKey: "addMail") { adding = true } }
            #endif
    }

    private func load() async { accounts = (try? await model.api?.mailAccounts()) ?? [] }

    /// Deletes the accounts, says why when one could not be (the row comes back with the reload), then reloads.
    private func delete(_ ids: [String]) async {
        for id in ids {
            do {
                try await model.api?.deleteMailAccount(id)
            } catch {
                model.show(error)
            }
        }
        await load()
    }
}

/// The Other IMAP footer for a QQ / NetEase address: where its authorization code is made, and a button to that web mail (#5123).
private struct CodeGuideSteps: View {
    let guide: CodeGuide
    @Environment(\.openURL) private var openURL

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            switch guide.provider {
            case .qq:
                Text("Servers fill in automatically. The password is an authorization code, not your QQ password: on a computer, open QQ Mail on the web, go to Settings › Account & Security › Security Settings, turn on “POP3/IMAP/SMTP/Exchange/CardDAV service”, verify as prompted, then tap “Generate authorization code” and paste the 16-character code above.")
            case .netease:
                Text("Servers fill in automatically. The password is an authorization code, not your login password: on a computer, open NetEase Mail on the web, go to Settings › POP3/SMTP/IMAP and turn on “IMAP/SMTP service”. After you verify with your phone as prompted, the code is shown: paste it above.")
            }
            Button { openURL(guide.webMail) } label: {
                Label(guide.provider == .qq ? String(localized: "Open QQ Mail on the web") : String(localized: "Open NetEase Mail on the web"), systemImage: Theme.Icon.safari)
                    .lineLimit(1)
            }
            .font(Theme.Font.caption)
            .accessibilityIdentifier("mail.codeGuide.open")
        }
    }
}

/// An account's provider: Gmail's and Lark's own marks, an envelope for any other IMAP server.
struct MailProviderMark: View {
    var provider: String

    var body: some View {
        Group {
            switch provider {
            case "gmail": BrandMark(Theme.Brand.gmail, side: Theme.Size.wellXS)
            case "outlook": BrandMark(Theme.Brand.microsoft, side: Theme.Size.wellXS)
            case "feishu": BrandMark(Theme.Brand.lark, side: Theme.Size.wellXS)
            default: Image(systemName: "envelope.circle.fill").resizable().scaledToFit().foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
            }
        }
    }
}

/// A provider's sign-in button as Google's branding guidelines draw it (developers.google.com/identity/branding-guidelines),
/// which Microsoft's allow too: the mark on white with a gray outline in light mode, on #131314 in dark mode; not the
/// app's accent.
struct AccountSignInLabel: View {
    @Environment(\.colorScheme) private var scheme
    var mark: String
    var title: String
    var busy = false

    var body: some View {
        let dark = scheme == .dark
        HStack(spacing: Theme.Space.m) {
            if busy { ProgressView().controlSize(.small) } else {
                BrandMark(mark, side: Theme.Size.iconXS)
            }
            Text(title).font(Theme.Font.heading).lineLimit(1)
        }
        .foregroundStyle(Color(hex: dark ? 0xE3E3E3 : 0x1F1F1F))
        .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.avatarXL)
        .background(Color(hex: dark ? 0x131314 : 0xFFFFFF), in: .capsule)
        .overlay(Capsule().stroke(Color(hex: dark ? 0x8E918F : 0x747775), lineWidth: 1))
        .contentShape(.capsule)
    }
}

struct AddMailAccountView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.webAuthenticationSession) private var webAuth
    typealias Kind = MailAccountForm.Kind
    @State private var form = MailAccountForm()
    @State private var saving = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Picker("Type", selection: $form.kind) { ForEach(Kind.allCases) { Text($0.title).tag($0) } }
                    .pickerStyle(.segmented)
                    .listRowBackground(Color.clear)
                if form.kind == .gmail && form.gmailAppPassword {
                    // #2442: this hub has no Google OAuth client, so there is nothing to sign in with: say who sets it
                    // up, and add the account with an app password meanwhile. The banner shows three lines at most:
                    // the rest is in the footer.
                    Section {
                        NoticeBanner(text: String(localized: "Google sign-in isn't set up on this hub: an admin adds google.client_id to manager.json."))
                            .listRowBackground(Color.clear)
                            .listRowInsets(EdgeInsets())
                    }
                    Section {
                        TextField(String(localized: "Gmail address"), text: $form.email).keyboardType(.emailAddress).textInputAutocapitalization(.never).autocorrectionDisabled()
                        SecretField(String(localized: "App password"), filled: String(localized: "Filled in. Tap Connect."), text: $form.password, identifier: "mail.password")
                    } footer: {
                        Text("Once google.client_id is in the manager's manager.json and the hub restarts, “Sign In with Google” appears here (a one-time setup). Until then, add the account with an app password: turn on 2-Step Verification for the Google account, then create a 16-character password under Google Account › Security › App passwords and enter it here (not your Google password).")
                    }
                } else if form.kind == .gmail {
                    Section {
                        Button { Task { await signInWithGoogle() } } label: {
                            AccountSignInLabel(mark: Theme.Brand.google, title: saving ? String(localized: "Connecting to Gmail…") : String(localized: "Sign In with Google"), busy: saving)
                        }
                        .buttonStyle(.plain)
                        .disabled(saving)
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets())
                    } footer: {
                        Text("Choose an account on Google and let Persona send and receive mail. No password needed. You can revoke access anytime under “Third-party apps & services” in your Google Account; if access expires, just sign in again.")
                    }
                } else if form.kind == .outlook {
                    OutlookSignInSection(error: $error) { dismiss() }
                } else {
                    Section {
                        TextField(String(localized: "Email address"), text: $form.email).keyboardType(.emailAddress).textInputAutocapitalization(.never).autocorrectionDisabled()
                        // The mail server's password or a code made for one client, handed to the hub (kept there,
                        // encrypted): not this app's password, so not offered to the system's passwords (#1598).
                        SecretField(String(localized: "Password / app password"), filled: String(localized: "Filled in. Tap Connect."), text: $form.password, identifier: "mail.password")
                    } footer: {
                        switch form.kind {
                        case .gmail, .outlook: EmptyView()
                        case .feishu: Text("Lark Mail: the servers are always imap.larkoffice.com:993 and smtp.larkoffice.com:465 (SSL), so just enter your address and password. Use the third-party client password from your Lark Mail settings.")
                        case .imap:
                            if let guide = form.codeGuide {
                                CodeGuideSteps(guide: guide)
                            } else {
                                Text("Servers fill in automatically for QQ, 163, iCloud and similar. Most mail providers in China need an authorization code instead of your login password. For Outlook, use the “Outlook” option above.")
                            }
                        }
                    }
                }
                if form.kind == .imap {
                    Section("Servers (Optional, Auto-Detected)") {
                        TextField(String(localized: "IMAP server"), text: $form.imapHost).textInputAutocapitalization(.never).autocorrectionDisabled()
                        TextField(String(localized: "IMAP port"), text: $form.imapPort).keyboardType(.numberPad)
                        TextField(String(localized: "SMTP server"), text: $form.smtpHost).textInputAutocapitalization(.never).autocorrectionDisabled()
                        TextField(String(localized: "SMTP port"), text: $form.smtpPort).keyboardType(.numberPad)
                    }
                }
                if let error { Section { Text(error).foregroundStyle(Theme.danger).font(Theme.Font.caption) } }
                Section {
                } footer: {
                    // 2026-11-01: 「AI 一键添加」 (paste and recognize) is gone; Persona adds accounts itself (mail_account_add).
                    #if os(macOS)
                    // On macOS the add-account confirmation card appears in the Mac app, not on a phone (#4070).
                    Label("You can also just tell Persona “Add a mail account for me”, give it the address and password, and confirm in the app.", systemImage: Theme.Icon.ai)
                    #else
                    Label("You can also just tell Persona “Add a mail account for me”, give it the address and password, and confirm on your phone.", systemImage: Theme.Icon.ai)
                    #endif
                }
            }
            .themedPage()
            .navigationTitle("Add Mail Account")
            .navigationBarTitleDisplayMode(.inline)
            // Ask before offering "Sign in with Google" (#2442); an older hub or a failed read keeps the button.
            .task { form.gmailAppPassword = (try? await model.api?.googleConfig())?.configured == false }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                if form.request != nil {
                    ToolbarItem(placement: .confirmationAction) {
                        Button { Task { await save() } } label: { if saving { ProgressView() } else { Text("Connect [V N C Page a55741]") } }
                            .disabled(saving || form.email.isEmpty || form.password.isEmpty)
                    }
                }
            }
        }
    }

    /// Gmail: Google's consent page in the system browser, then the hub swaps the code for a refresh token.
    private func signInWithGoogle() async {
        guard let api = model.api else { return }
        saving = true
        defer { saving = false }
        error = nil
        do {
            let start = try await api.googleSignInStart()
            let callback = try await webAuth.authenticate(using: start.authURL, callback: .customScheme(start.callbackScheme),
                                                          preferredBrowserSession: .shared, additionalHeaderFields: [:])
            let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems ?? []
            let value = { (name: String) in items.first(where: { $0.name == name })?.value }
            if value("error") == "access_denied" { return }
            _ = try await api.googleSignInFinish(.init(state: value("state") ?? start.state, code: value("code"), error: value("error")))
            Haptics.success()
            dismiss()
        } catch let e as ASWebAuthenticationSessionError where e.code == .canceledLogin {
            return
        } catch {
            if let shown = error.shown { self.error = shown; Haptics.error() }
        }
    }

    private func save() async {
        guard let api = model.api, let req = form.request else { return }
        saving = true
        defer { saving = false }
        do {
            _ = try await api.addMailAccount(req)
            Haptics.success()
            dismiss()
        } catch {
            self.error = error.shown
        }
    }
}
