import AuthenticationServices
import SwiftUI

// Add Mail Account › Outlook (docs/extensions/mail.md「Outlook 接入方式」): Microsoft's sign-in for personal Outlook.com and work Microsoft
// 365 mailboxes, with a client ID the user registered in Microsoft Entra themselves (never built into the app): the
// first time, the field for it with the steps; then Sign In with Microsoft, the same flow as Google's.

/// The Outlook part of Add Mail Account: the client ID while the hub has none (or the user changes it), else the sign-in.
struct OutlookSignInSection: View {
    @Environment(AppModel.self) private var model
    @Environment(\.webAuthenticationSession) private var webAuth
    @Binding var error: String?
    var done: () -> Void
    @State private var config: APIClient.MicrosoftConfig?
    @State private var clientID = ""
    @State private var editing = false
    @State private var busy = false

    var body: some View {
        Group {
            if let config, config.configured, !editing {
                signIn(config)
            } else {
                registration
            }
        }
        .task { await load() }
    }

    private func signIn(_ config: APIClient.MicrosoftConfig) -> some View {
        Section {
            Button { Task { await signInWithMicrosoft() } } label: {
                AccountSignInLabel(mark: Theme.Brand.microsoft, title: busy ? String(localized: "Connecting to Outlook…") : String(localized: "Sign In with Microsoft"), busy: busy)
            }
            .buttonStyle(.plain)
            .disabled(busy)
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets())
        } footer: {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text("Works with personal Outlook / Hotmail and work Microsoft 365 accounts: sign in with Microsoft and let Persona send and receive mail. No password needed. If access expires, just sign in again.")
                Button {
                    clientID = config.clientID ?? ""
                    editing = true
                } label: {
                    Text("Change Client ID").lineLimit(1)
                }
                .font(Theme.Font.caption)
            }
        }
    }

    private var registration: some View {
        Section {
            TextField(String(localized: "Application (client) ID"), text: $clientID)
                .font(Theme.Font.code)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
            Button { Task { await save() } } label: {
                if busy { ProgressView() } else { Text("Save Client ID").lineLimit(1) }
            }
            .disabled(busy || clientID.trimmingCharacters(in: .whitespaces).isEmpty)
            if let uri = config?.redirectURI {
                HStack {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("Redirect URI (for registration)").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                        Text(uri).font(Theme.Font.code).lineLimit(1).minimumScaleFactor(0.7).textSelection(.enabled)
                    }
                    Spacer(minLength: Theme.Space.s)
                    Button { UIPasteboard.general.string = uri; Haptics.success() } label: {
                        ThemedIcon(Theme.Icon.copy)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityLabel("Copy redirect URI")
                }
            }
        } header: {
            Text("Register Your Own App First")
        } footer: {
            Text("Outlook sign-in uses an app you register yourself in Microsoft Entra (free; register once and this hub keeps using it):\n1. Open entra.microsoft.com with a Microsoft account, then App registrations → New registration.\n2. For supported account types, choose “Accounts in any organizational directory and personal Microsoft accounts”.\n3. For the redirect URI platform, choose “Public client/native (mobile & desktop)” and enter the address above.\n4. Under Authentication, set “Allow public client flows” to “Yes”.\n5. Under API permissions, add IMAP.AccessAsUser.All and SMTP.Send (delegated permissions).\n6. Enter the “Application (client) ID” from the Overview page here.\nFor detailed steps, see docs/extensions/mail.md, “Outlook Connection”.")
        }
    }

    private func load() async {
        guard let api = model.api else { return }
        do {
            config = try await api.microsoftConfig()
        } catch {
            self.error = error.shown.map { String(localized: "Couldn't load the Outlook sign-in settings: \($0)") }
        }
    }

    private func save() async {
        guard let api = model.api else { return }
        busy = true
        defer { busy = false }
        do {
            config = try await api.setMicrosoftClientID(clientID.trimmingCharacters(in: .whitespaces))
            editing = false
            error = nil
            Haptics.success()
        } catch {
            if let shown = error.shown { self.error = shown; Haptics.error() }
        }
    }

    /// Microsoft's sign-in page in the system browser; whatever it calls back with goes to the hub, which swaps a code
    /// for a refresh token, or says what to do about an error (an admin has to consent, IMAP is off…).
    private func signInWithMicrosoft() async {
        guard let api = model.api else { return }
        busy = true
        defer { busy = false }
        error = nil
        do {
            let start = try await api.microsoftSignInStart()
            let callback = try await webAuth.authenticate(using: start.authURL, callback: .customScheme(start.callbackScheme),
                                                          preferredBrowserSession: .shared, additionalHeaderFields: [:])
            let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems ?? []
            let value = { (name: String) in items.first(where: { $0.name == name })?.value }
            _ = try await api.microsoftSignInFinish(.init(state: value("state") ?? start.state, code: value("code"),
                                                          error: value("error"), errorDescription: value("error_description")))
            Haptics.success()
            done()
        } catch let e as ASWebAuthenticationSessionError where e.code == .canceledLogin {
            return
        } catch {
            if let shown = error.shown { self.error = shown; Haptics.error() }
        }
    }
}
