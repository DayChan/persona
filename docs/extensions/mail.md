# Mail

`server/internal/mail`: lets both Claude and the App send and receive the user's email. It uses standard IMAP + SMTP with no third-party relay service.
Gmail uses "Sign In with Google" (OAuth2 + XOAUTH2) and additionally supports searching with Gmail search syntax (`X-GM-RAW`). Credentials are encrypted and stored in the hub's `dataDir` (default `~/.persona`).

## Capabilities

| Capability | Notes |
| --- | --- |
| Multiple accounts | Each account has its own IMAP / SMTP hosts (filled in automatically from domain presets); Gmail, QQ, 163, Outlook, iCloud… can all be attached at once |
| Test connection | When adding an account, actually log in to IMAP and SMTP once; only save on success. On failure return the error and store nothing |
| Mailbox list | `LIST`, with `RETURN (SPECIAL-USE STATUS)` when the server supports it (needs LIST-EXTENDED or IMAP4rev2), mapped to the `inbox/sent/drafts/trash/archive/junk` roles with unread and total counts. For servers that don't support it, send a plain `LIST`, guess roles from names, and `STATUS` each mailbox for counts |
| Inbox browsing | Paged by UID descending (`before_uid`), `limit` messages at a time; when viewing a single account the result carries `next_before_uid` (the `before_uid` of the next page). Summaries are fetched newest first in batches of 50; when this operation has less than 6 s left before the 20 s timeout, no further batch is fetched and the page ends early (`has_more` + `next_before_uid`). This keeps 163 from timing out when old mail is slow to fetch |
| Search | Gmail uses `X-GM-RAW` (supports `from:`, `subject:`, `has:attachment`, `newer_than:7d`, etc.); other providers use `SEARCH TEXT`. When the server returns 0 results or refuses (NO / BAD), fall back to matching headers locally (`headers_only`): from `before_uid` backwards, fetch ENVELOPEs 100 at a time; every space-separated word must appear in the subject, or in the name or address of the sender or a To / Cc recipient (case-insensitive, body not checked). One page looks at most at 2000 messages and 6 s (counted from the start of this IMAP operation, including connecting, SELECT, and the server's empty search; retries after connection errors don't restart the clock; when time is up, finish the current batch and stop; at least one batch per page). If not done, `has_more`, and `next_before_uid` is where to continue (`messages` may be empty then). 163's server does no keyword search, so it relies entirely on this step |
| Summary | Fetch only a 2048-byte body snippet instead of the whole message; the summary is the first 160 characters of the body |
| Unified inbox | Without `account_id`, fetch every account's INBOX concurrently and merge by date descending |
| Reading | Fetch the full text with `BODY.PEEK`; go-message parses plain text / HTML / attachment metadata; it does **not** mark the message as read |
| Attachment download | A stable ID tied to MIME order, metadata, and decoded content; reads are authorized by account, mailbox, and UID; a plain `BODY.PEEK` is decoded as a stream into a temp file, with HTTP Range support; doesn't mark as read and needs no extra IMAP extensions |
| Read / starred | Only changes the two flags `\Seen` and `\Flagged` |
| Sending | go-message builds the MIME, UTF-8, with Chinese subjects and names encoded per RFC 2047; supports `in_reply_to` / `references` for threading |
| Keeping a sent copy | For non-Gmail accounts (SMTP doesn't save a copy automatically), `APPEND` to `\Sent` after sending; failure is only logged and doesn't affect the send result |
| MCP tools | `mail_accounts`, `mail_search`, `mail_read` (lists each attachment's id), `mail_save_attachment` (saves an attachment as a file on the agent's machine, see "Saving attachments"), `mail_send`; sending always asks the user to approve on the phone first |

HTTP routes and JSON fields match the "Mail API" section of `docs/protocol/protocol.md` exactly; passwords never appear in responses
(`json:"-"` + a separate storage file).

## Gmail: Sign In with Google (OAuth2 + XOAUTH2)

Google no longer lets third-party clients sign in to Gmail with the Google account password (Help Center, "Add Gmail to another email client": use
"Sign in with Google" instead). So Gmail goes through OAuth like Apple Mail, Thunderbird, Spark and similar clients:
tap "Sign In with Google" in the App → the system browser opens Google to pick an account and authorize → back to the App → the account is added, with no password to type.
(OAuth was removed once on 2026-10-24: back then users had to create their own Google Cloud client and swap refresh tokens by hand.
On 2026-10-25 the user asked to go back to a button that jumps to Google authorization, and the app-specific password entry was removed from the App.)

### Flow

```
App                         hub                                Google
 │ POST oauth/google/start   │                                   │
 │──────────────────────────▶│ create state + PKCE verifier (in memory, 15 minutes)
 │◀── auth_url, scheme ──────│                                   │
 │ ASWebAuthenticationSession opens auth_url ───────────────────▶│ pick account, consent
 │◀──────────── com.googleusercontent.apps.<id>:/oauth2redirect?code&state
 │ POST oauth/google/finish {state, code}                         │
 │──────────────────────────▶│ code + verifier → token ─────────▶│
 │                           │◀── refresh_token, access_token, id_token
 │                           │ email from id_token, verify via XOAUTH2 IMAP login + Gmail API, store encrypted
 │◀── {account} ─────────────│                                   │
```

- Scopes: `https://mail.google.com/` (the only scope IMAP / SMTP accept) + `openid email` (to get the email address).
  `access_type=offline` + `prompt=select_account consent`: a refresh token every time, and the account can be switched.
- The state is not kept in memory: the PKCE verifier and creation time are encrypted with `secret.key` (AES-GCM) and put entirely into the state, so a hub restart / redeploy while the user is on the Google page doesn't matter
  (hit in testing on 2026-10-25: another agent deployed three times in a row, the in-memory state was lost, and the callback reported "start again"). Within a process each state can be used only once.
- The client type is **iOS**: no client secret, and the callback uses a custom scheme built from the reversed client ID (Google dropped custom schemes for Android / Chrome apps; iOS still supports them).
  So the web version can't add Gmail (only the App can receive the callback); choosing Gmail in **Settings › Connectors** there says to add it on the phone. IMAP / Lark Mail can be added on the web.
  The token exchange only sends `client_id` + `code_verifier`, so it can be done on the hub, and the refresh token never passes through the phone.
  ASWebAuthenticationSession intercepts the callback scheme, so no URL scheme needs registering in Info.plist, and changing the client ID doesn't require a new App release.
- The hub gets the id_token directly from Google's token endpoint (TLS), so per the OIDC spec it doesn't need signature verification; only `email` / `email_verified` are read.
- Credentials: `{client_id, refresh_token}` are encrypted with AES-GCM and stored in `mail_accounts.oauth_enc`; the access token is cached in memory only and refreshed 1 minute before expiry.
- Receiving: IMAP `AUTHENTICATE XOAUTH2`; the temporary connection for Gmail search uses XOAUTH2 too.
- **Gmail traffic goes through a proxy**: the author's hub machine can only reach a few of Google's IPv4 addresses (`imap.gmail.com` / `smtp.gmail.com` mostly resolve to 173.194.x.x,
  and 443 / 993 / 465 / 587 all time out; HTTPS only works because it goes over IPv6). `google.proxy` in the manager's `manager.json` (`http://[user:pass@]host:port`)
  sends Gmail accounts' IMAP / SMTP (HTTP CONNECT tunnel), Google token exchange, and the Gmail API all through the proxy; other providers still connect directly.
- **Sending uses the Gmail API** (`POST gmail.googleapis.com/gmail/v1/users/me/messages/send`, raw MIME), not SMTP:
  this machine can't reach `smtp.gmail.com` (both 465 / 587 time out, tested 2026-10-25), while HTTPS to Google works; the `https://mail.google.com/` scope also covers the Gmail API
  (the Gmail API is enabled in the project). Bcc works by prepending a `Bcc:` header to the raw message, which Gmail strips on delivery; Gmail files sent mail into "Sent" itself.
  The check when adding an account also switched from SMTP to the Gmail API's `users/me/profile` (and verifies the email address matches).
- When the refresh token is invalid (the user revoked access, changed the Google password, or hasn't used it in six months), the error is "Google authorization expired or was revoked":
  tap "Sign In with Google" again in the App and pick the same account; the credentials are updated in place (the account ID stays the same).
- Gmail accounts added earlier with an app-specific password keep working (the server still supports `auth:"password"`); the apps offer that entry only on a hub without Google sign-in (below).

### One-time setup: Google Cloud OAuth client

Open-source mail clients (Thunderbird, K-9, FairEmail, Mailspring, GNOME Online Accounts) all have the project register a client, hard-code it in the source,
and pass Google's verification for restricted scopes; Thunderbird's source explicitly says "don't copy these values, register your own". Persona is for personal use, so creating your own is enough.

The client we created uses the consent screen "Persona", external / in production (unverified: at sign-in you see "Google hasn't verified this app"; click "Advanced → Go to Persona").
The client ID is only written in the manager's `manager.json` (`google.client_id`, `docs/operations/config.md` ("manager.json")), never in the repo or docs.
Publishing to production requires the branding page to link a home page, privacy policy, and terms of service, with the domain listed under "Authorized domains", so we built a static site (repo `site/`, domain in `site/wrangler.jsonc`).

Steps (for reference when recreating):

1. Create a project at <https://console.cloud.google.com/projectcreate> (e.g. `persona-mail`).
2. "Google Auth Platform" → Branding: app name `Persona`, user type "External".
3. Under "Data access", add the scope `https://mail.google.com/` (a restricted scope).
4. Under "Branding", fill in the home page / privacy policy / terms of service (`https://<site domain>/`, `/privacy/`, `/terms/`), and add the site's root domain to authorized domains.
   Without these, the "Publish app" button under "Audience" is grayed out, and the page doesn't say what is missing.
5. Under "Audience", **publish the app (In production)**. While in Testing, refresh tokens expire after 7 days; after publishing they don't.
   An unverified app is limited to 100 users, and Google shows "This app isn't verified" at sign-in; click "Advanced → Continue". For personal use there's no need for verification / the CASA security assessment.
6. "Clients" → create an OAuth client of type **iOS**, with the Bundle ID and Team ID from `app.bundle_id` / `app.team_id` in the config, which yields
   `<digits>-<string>.apps.googleusercontent.com`.
7. Write it into the built-in manager's `~/.persona/managers/assistant/manager.json` on the hub machine: `"google": {"client_id": "…apps.googleusercontent.com"}`, and restart the hub (`scripts/deploy/deploy.sh <hub machine id>`).
   An iOS client's client ID is public information, not a secret.

Without `google.client_id` the hub can't start Google sign-in (#2442). iOS / Mac, web and Android ask `GET /api/v1/mail/oauth/google/config` when the add-account page opens; on `{"configured": false}`
they don't show "Sign In with Google" but say that an admin adds `google.client_id` here, and offer Gmail address + app-specific password instead (`auth: password`, `imap.gmail.com:993` / `smtp.gmail.com:465`).
A hub too old to answer `config` (404) counts as configured. `POST …/google/start` on such a hub answers 400 with `code: google_not_configured` and the same explanation in the client's language;
every language's text keeps the phrase "not configured", which older Android apps match (below).

### Android

The hub's OAuth client is the iOS type, the callback is the reversed-client-ID custom scheme, and the hub builds the authorization URL and exchanges the token, so Android only needs to open the URL and
catch the callback (`android/…/mail/GoogleSignIn.kt`): the system browser opens `auth_url`, and `ui/mail/OAuthRedirectActivity` receives
`<scheme>:/oauth2redirect?code&state`, hands it to the hub's `finish`, then brings the App back to the foreground. Only this flow's `state` is accepted (callbacks forged by other apps are ignored);
the flow survives process death: the awaited `state` is stored in SharedPreferences, so if the process is killed while the browser is in front, the callback still completes.

- Android can't register arbitrary schemes at runtime: the intent filter's scheme comes from `android.google_oauth_scheme` in the build config `~/.config/persona/config.json`
  (`scripts/app/android.sh` passes it to gradle; the env var `PERSONA_GOOGLE_OAUTH_SCHEME` also works), never in the repo. When unset it is a placeholder;
  the App uses `PackageManager.queryIntentActivities` to check whether it can catch the `callback_scheme` the hub returns, and if not, explains why
  and falls back to "Gmail + app-specific password" (the hub still supports `auth: password`). A hub without `google.client_id` gets the same form from the start, read from `google/config` like iOS and web.
  On a hub too old to answer `config`, the app still finds out after the tap, from the `start` error containing "not configured" (case-insensitive); app versions before this read only that error.
- Not verified on a real device: whether Google allows a callback to an iOS client's custom scheme when initiated from an Android browser (custom schemes are already disabled for the Android client type).
  If not, either add an Android / Web type client on the hub (Web needs a client secret and an https callback), or keep using app-specific passwords.
- When the user returns from the browser directly (without authorizing), no callback within 1.5 s of returning to the page counts as cancelled.

## Outlook: Sign In with Microsoft (OAuth2 + XOAUTH2, 2026-11-03)

Personal Outlook.com / Hotmail and work or school Microsoft 365 mailboxes all use this: in the App, **Add Mail Account › Outlook**, tap "Sign In with Microsoft" →
the system browser opens Microsoft sign-in and authorization → back to the App → the account is added, no password. It works like Gmail (previous section), with these differences:

- **The client ID is not compiled in**: each user uses an app they register in Microsoft Entra themselves (free, steps below). The first time you tap "Outlook" the App asks for its
  client ID; the manager encrypts it with its own `secret.key` and stores it in Postgres (`mail.microsoft_client` in the settings table), and this hub reuses it from then on. `microsoft.client_id` in the manager's `manager.json`
  can provide a default (the one entered in the App takes priority). The client ID appears in every sign-in URL and is not a secret.
- **Redirect URI**: the fixed custom scheme `persona-mail-oauth://outlook`, the same for every hub (to change it, set `microsoft.redirect_uri` in the manager's `manager.json` and change it in Entra too).
  The App catches this scheme with ASWebAuthenticationSession, which needs no registration in Info.plist, so **no per-hub App release is needed**.
  We didn't use `msauth.<bundle id>://auth`: it follows the App's bundle ID, which differs for everyone who builds it themselves, while a fixed scheme is the same for everyone.
- **Public client + PKCE**, with authority `common` (personal accounts and accounts from any organization can sign in), and no client secret; the token exchange happens on the hub, and the refresh token never passes through the phone.
  Scopes: `https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send offline_access openid email`.
- The email address comes from the ID token's `email`, falling back to `preferred_username` (the work account's sign-in name).
- **Sending and receiving**: IMAP `outlook.office365.com:993`, SMTP `smtp.office365.com:587` (STARTTLS), both XOAUTH2. Exchange puts sent mail into
  "Sent" itself, so the hub doesn't append another copy.
- **Refresh tokens rotate**: Microsoft issues a new one on every refresh (and the old one becomes invalid), so the hub encrypts and stores it back into `oauth_enc` right away (Google doesn't rotate).
- Credentials: `{provider: "microsoft", client_id, refresh_token}` encrypted in `mail_accounts.oauth_enc`, same as Gmail.
- Code: `server/internal/mail/microsoft.go`; App: `apple/Persona/Views/Mail/MailOutlookViews.swift`.
- API: `GET` / `PUT /api/v1/mail/oauth/microsoft/config` (`{configured, client_id, source: app|config, redirect_uri}`; PUT `{client_id}`, an empty string clears it),
  `POST …/microsoft/start` (`{login_hint?}` → `{auth_url, callback_scheme, state}`), `POST …/microsoft/finish` (the callback's `{state, code}` or
  `{state, error, error_description}` → `{account}`). A callback carrying an error is still passed to the hub: it turns Microsoft's error code into what to do (see below).
- Not yet verified with a real account (on 2026-11-03 it was only tested against a fake token endpoint and fake IMAP / SMTP); the user will verify by signing in.

### Registering an app in Microsoft Entra (once, click along)

1. Open <https://entra.microsoft.com> and sign in with your Microsoft account (a personal account works too; the first visit creates a "Default Directory" automatically).
   If you can't get in or don't see the menu, open <https://portal.azure.com> and search for "App registrations"; it is the same thing.
2. On the left, "Identity" → "Applications" → "App registrations" → "New registration".
3. Fill in three fields, then click "Register":
   - Name: anything, e.g. `Persona Mail`;
   - "Supported account types": choose "**Accounts in any organizational directory (Any Microsoft Entra ID tenant - Multitenant) and personal Microsoft accounts (e.g. Skype, Xbox)**";
   - "Redirect URI": platform "**Public client/native (mobile & desktop)**", value `persona-mail-oauth://outlook` (use whatever the App shows; you can tap to copy it).
4. After registering, copy the "**Application (client) ID**" from the "Overview" page (looks like `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`).
5. On the left, "Authentication": check that `persona-mail-oauth://outlook` is listed under "Mobile and desktop applications"; scroll to "Advanced settings", set "**Allow public client flows**" to "Yes", and click "Save".
6. On the left, "API permissions" → "Add a permission":
   - under "APIs my organization uses", search for `Office 365 Exchange Online` → "Delegated permissions" → tick `IMAP.AccessAsUser.All` and `SMTP.Send` → "Add permissions";
   - "Add a permission" again → "Microsoft Graph" → "Delegated permissions" → tick `offline_access`, `openid`, `email` (the default `User.Read` can stay).
   A personal account's directory sometimes can't find Exchange Online: skipping this step still works (the scopes are requested dynamically at sign-in, and the consent page lists these permissions anyway);
   for work accounts, add them: when an admin does "Grant admin consent", these are the permissions they look at.
7. Back in the App: **Settings › Connectors › Mail › Add Mail Account › Outlook**, enter the client ID and save, then tap "Sign In with Microsoft".

### When something goes wrong (these are the hints the hub returns)

| Situation | What the hint says to do |
| --- | --- |
| Work account requires admin consent (`AADSTS65001` / `90094` / `90095`) | Ask your IT admin to find this app under "Enterprise applications" in Entra → "Permissions" → "Grant admin consent", or to allow user consent; or use a personal account instead |
| The user cancelled on the consent page (`AADSTS65004` / `access_denied`) | Sign in again and consent |
| Wrong client ID, or "personal and organizational accounts" wasn't chosen at registration (`AADSTS700016`) | Check the client ID and "Supported account types" |
| Redirect URI mismatch (`AADSTS50011`) | Add the redirect URI the App shows under "Authentication › Mobile and desktop applications" |
| "Allow public client flows" not enabled (`AADSTS7000218`) | Turn it on under "Authentication › Advanced settings" |
| No permission to read mail (the returned scope has no IMAP) | Add `IMAP.AccessAsUser.All` and `SMTP.Send` under "API permissions" and sign in again |
| IMAP login rejected (`AUTHENTICATE failed`) | Personal account: in outlook.com "Settings › Mail › Forwarding and IMAP", turn on "Let devices and apps use IMAP"; work account: ask your admin to enable IMAP for your mailbox in the Exchange admin center |
| SMTP auth disabled (`5.7.139 SmtpClientAuthentication is disabled`) | Ask your admin to turn on "Authenticated SMTP" for your mailbox in the Microsoft 365 admin center; receiving is unaffected |
| Authorization expired or revoked (`invalid_grant` on refresh: `AADSTS70008`, `50173`, MFA required again, etc.) | Tap "Sign In with Microsoft" again in the App and pick the same mailbox; the account is updated in place (ID unchanged) |

## Letting Persona add it (2026-11-01)

The App's "Add Mail Account" page dropped "AI one-tap add" (paste some text for the model to recognize; `POST /api/v1/ai/mail-account` is gone too). Now you just tell Persona
"add a mail account for me": the assistant adds it through `mail.accounts` of `persona_settings` (`hub/self_settings.go`, assistant only), a card on the phone asks the user for the password / authorization code,
and after confirmation the hub connects once with `AddAccount` before saving, the same path as the App's form. The password never passes through the model or enters the chat; the interaction history only stores `<redacted>`.
Cards that need input have no quick-allow button in the notification; the watch and Siri prompt you to fill it in on the iPhone; an approval missing required fields doesn't consume the card. Gmail can still only be signed in from the App.

## Lark Mail

Choose "Lark" in the App and fill in only the address and password (`provider: "feishu"`). The address is on the company's own domain, so the server can't be guessed from it. Lark Mail has two sets of servers with the same ports (IMAP 993, SMTP 465, both SSL):

| | Overseas | Mainland China |
| --- | --- | --- |
| Incoming IMAP | `imap.larkoffice.com` | `imap.feishu.cn` |
| Outgoing SMTP | `smtp.larkoffice.com` | `smtp.feishu.cn` |

`larkoffice.com` resolves overseas; from mainland China the TLS handshake takes seconds and often times out, and `feishu.cn` is the reverse when overseas (#1720). So when adding the account, both sets are tried **at the same time**, and the one that logs in first is saved (`pickFeishuServers`): a hub in mainland China ends up on `feishu.cn`, an overseas one on `larkoffice.com`, with no choice for the user. If both fail, the error from the set that connected is reported first (e.g. a wrong authorization code); only when neither connects are both errors reported together.
Whether overseas Lark accounts can log in to the mainland servers hasn't been verified, so the default isn't simply switched to `feishu.cn`. Accounts already added don't switch sets automatically; if one is slow in mainland China, delete it and add it again.

When Lark is chosen, host / port in the request are ignored. The password is the login password generated for third-party clients in Lark Mail's settings (the server calls it an "authorization code"; a wrong one reports `wrong authorization code`).

## Other providers

Servers are filled in automatically from the domain; explicitly passed `imap_host` / `imap_port` / `smtp_host` / `smtp_port` take precedence:

| Domain | IMAP | SMTP |
| --- | --- | --- |
| gmail.com / googlemail.com | imap.gmail.com:993 TLS | smtp.gmail.com:465 TLS |
| qq.com / foxmail.com / vip.qq.com | imap.qq.com:993 TLS | smtp.qq.com:465 TLS |
| 163.com / 126.com / yeah.net / 188.com / vip.163.com / vip.126.com | imap.<domain>:993 TLS | smtp.<domain>:465 TLS |
| outlook.com / hotmail.com / live.com | outlook.office365.com:993 TLS | smtp-mail.outlook.com:587 STARTTLS |
| icloud.com / me.com | imap.mail.me.com:993 TLS | smtp.mail.me.com:587 STARTTLS |
| yahoo.com / yahoo.co.jp | imap.mail.yahoo.com:993 TLS | smtp.mail.yahoo.com:465 TLS |

For QQ / 163 / 126 and similar, the password is an **authorization code**, not the login password: first enable IMAP/SMTP in the webmail settings and generate an authorization code.
The QQ and NetEase presets carry the steps to make that authorization code (`Guide` in `preset.go`, text in `i18n/locales/*/mail-preset.json`, #5123): a rejected IMAP / SMTP login and the assistant's add-mail card show them.
The add-mail forms on iOS, the web and Android recognize the same domains under Other IMAP and show the same steps with a button to that web mail (`CodeGuide` / `mailCodeGuide`); change their domain lists together with `preset.go`.
163 and 126 require the client to send IMAP `ID` (RFC 2971) before login; the server side already does.

Security mode: without `imap_security`, IMAP infers it from the port (993 = TLS, 143 = STARTTLS, anything else plaintext, which is only for local testing);
without it, SMTP infers from the port (465 = TLS, 587/25 = STARTTLS, anything else TLS). Pass it explicitly for corporate mail with unusual ports.

An address can have only one account. Adding the same address again (case differences included) makes the hub check for duplicates first, skip the connection test, and immediately return 409 (`ErrAccountExists`),
asking you to delete the existing account first; to change servers or the authorization code, delete and re-add. It checks again after the test, to block two requests adding at the same time (#1766).

## Credential storage

| File | Permissions | Contents |
| --- | --- | --- |
| the manager's `secret.key` (the built-in manager's `~/.persona/managers/assistant/secret.key`) | 0600 | 32-byte random master key, created on first start (or copied once from `~/.persona/secret.key`, [managers.md](../assistant/managers.md) ("The built-in manager's own files")), never stored in the database or returned to clients |
| Postgres `mail_accounts` table | — | Non-sensitive fields such as host, port, display name (jsonb); `password_enc` (password) and `oauth_enc` (Gmail's client_id + refresh token) are ciphertext |

- Algorithm AES-256-GCM, with a random 12-byte nonce prepended to the ciphertext, the whole thing base64.
- Every save re-encrypts with a new nonce (the same plaintext never produces the same ciphertext).
- The whole set of accounts is replaced in one transaction.
- When moving machines / reinstalling the hub, if `secret.key` is lost, every password has to be re-entered.
- `chmod 700` is recommended for the hub's `~/.persona` directory.

API responses only carry `status` / `error`; the password or refresh token can never be retrieved.

### Moving an account

An account can move from one manager to another (docs/assistant/managers.md). Each manager has its own Postgres database and
its own `secret.key`, so the sealed secrets cannot be copied as ciphertext. The hub asks the old manager for
`extensions.source_export` (source `mail:<account id>`): `(*Service).ExportAccount` gives the whole account, ID, name,
servers and credentials (password or OAuth client ID and refresh token), in plaintext. The hub passes it as it is to
the new manager's `extensions.source_import`: `ImportAccount` keeps the ID, refuses an address or ID it already has
(`source_exists`), skips the connection test and seals the secrets again with its own key. Then the hub calls the old
manager's `extensions.source_release`, which deletes the account and its secrets there (not the mail already filed
into the knowledge base). The secrets cross the bus in plaintext once; the hub never stores or logs the bundle, and
the managers never put it in logs or error messages.

## Connections and timeouts

- Each account caches one logged-in IMAP connection, closed after 10 minutes unused (swept every 30 s). That is longer than both the Inbox mail round (3 minutes) and the knowledge base mail round (6 minutes),
  so the next round reuses the previous round's login instead of redoing TCP + TLS + LOGIN every round (before #2627 it was 2 minutes, and the connection was always already closed when a round started); RFC 3501 servers' idle auto-logout is no shorter than 30 minutes.
- A connection idle for more than 1 minute is sent `NOOP` before reuse; with no reply in 5 s it is closed and reconnected, so an idle connection silently forgotten by NAT or a firewall doesn't make the round hang the full 20 s before timing out.
- Commands on one connection are serialized per account (IMAP's `SELECT` state is per connection; concurrent use would clash).
  Waiting in the queue uses the caller's ctx: on cancel or timeout it leaves the queue and reports `ctx.Err()`, without running or touching the connection (before #2344, abandoned requests still waited their turn, then closed the account's connection on the way out, forcing every live request behind them to reconnect). The operation's own 20 s only starts once it gets its turn, so a slow mailbox ahead doesn't eat into its time.
- Network operations time out after 20 s; on transport-level errors (not the service's NO/BAD) the connection is dropped and retried once, to survive servers that disconnect on their own.
- Account `status` (#4722): a rejected login (wrong password / authorization code, Google or Microsoft authorization revoked or expired, XOAUTH2 answered with NO) is recorded as `error`, with `error` holding the reason (truncated to 300 characters, no password); the next successful login (or signing in again to add the same mailbox) sets it back to `ok`.
  Temporary errors such as timeouts, connection failures, DNS, server busy (`UNAVAILABLE`), or token endpoint 5xx don't change the status; a NO on some command after login (folder doesn't exist) doesn't count either. It is kept in memory only, not in the database: after a hub restart it shows `ok` first, and is flagged again if the next read round (at most 3 minutes) fails.
- All the work for a page has to fit in those 20 s (`withConnAt` hands the deadline to `listOne`, unchanged across retries): local header matching runs at most until 6 s after the operation starts, and summaries are fetched 50 per batch, with no further batch once less than 6 s remain before the deadline. A page that hits the limit ends early with `next_before_uid` instead of timing out.
- Gmail search sometimes needs a separate temporary connection to send `X-GM-RAW` (go-imap/v2's strongly typed client can't express this extension command),
  closed right after use; on failure it falls back to standard `SEARCH TEXT`.

## Saving attachments

`mail_save_attachment {account_id, uid, mailbox?, attachment_id, dir?}` lets an agent keep an attachment (a statement's PDF, CSV,
xlsx or encrypted zip) as a file it can read, unzip or move with its own tools, for example to collect monthly statements for
parsing. `mail_read` lists each attachment as `- <name> (<type>, <size> bytes) id=<id>`; the id is the same one the HTTP
download route takes.

- **Answered on the agent's machine**: the node there intercepts the call (`server/internal/node/mailattachment.go`, from MCP,
  a runtime or `personad tool`) and asks its manager for the bytes with `mail.attachment` (`docs/protocol/protocol.md`). The
  manager applies the checks of `mcp.call` first (never a work machine; an assistant call only from an assistant conversation;
  a guest conversation only when it was given the tool), then hands the bytes over only while the mail connector offers
  `mail_save_attachment` to that caller (it is on and ok). The bytes come from the same lookup as the download
  (`FetchAttachment` in `attachment.go`, `BODY.PEEK`, so the message is not marked as read). A call that reaches the
  hub's mail service itself (no node in between) is refused.
- **At most 25 MB** (`mail.MaxSavedAttachment` = `proto.MaxMailAttachment`): the bytes cross the node's connection in one
  message, base64 in JSON, well under its 64 MiB line. Larger attachments are refused before anything is written.
- **Where files go**: `<data dir>/attachments/mail/<dir>/<file>` (`~/.persona/attachments/mail` by default), folders 0700 and
  files 0600. `dir` must be a plain relative subfolder (`bills/2026-10`): no absolute path, `~`, `..`, hidden folders,
  backslashes, colons or control characters; it is checked before anything is fetched. Folders are created one by one and
  each must resolve inside the root, so a symlink pointing out of it is refused.
- **File names**: the sender's name is cleaned to one plain name (path parts dropped, control characters and `:` replaced,
  leading dots removed, cut to 200 bytes on a character boundary keeping the extension; empty becomes `attachment`).
  Files are created with `O_EXCL`, so an existing file (or a symlink at that name) is never written through:
  the next free `name (2).ext` … `name (99).ext` is used instead.
- The node writes into its own data directory, which the agent sandbox leaves readable (only its secrets are closed).

## Known limitations

- **No IDLE push**: new mail waits for the App to refresh (polling or IMAP IDLE could be added later).
- **Sending doesn't support attachments yet**: attachments on incoming mail can be inspected and downloaded, but outgoing mail still can't carry them.
- **No delete / move / archive**: only read and starred can be changed for now.
- **Summaries only use the first 2 KB of the body**: if the body starts with a very long base64 inline image, the summary may be empty or meaningless.
- **gzip-compressed bodies** (`Content-Encoding: gzip`) are not supported; such messages will be missing their body.
- **Keeping a sent copy for non-Gmail accounts is best effort**: an `APPEND` failure is only logged; some providers don't allow APPEND to `Sent`.
- **Saved attachments are at most 25 MB and are not cleaned up**: they stay under `<data dir>/attachments/mail` until the user
  or an agent deletes them; encrypted zips are saved as they are (unpacking them needs the password, which the tool never asks for).
- **MCP sending must wait for approval on the phone**: nothing is sent without it; but once approved it really goes out, with no undo.
- **One connection per account**: the unified inbox fetches accounts concurrently, but a single account is serialized; with many accounts and complex searches the first load is slower.
- **Mailboxes whose server does no keyword search can only match headers**: 163 returns 0 results for any term (tested 2026-11-03 with `TEXT` / `BODY` / `SUBJECT` / `FROM`, Chinese and English alike), so you can only search by subject, sender, and To / Cc recipients; words in the body can't be found. One page looks at most at 2000 messages; 163 is slow to fetch, so in practice a page only gets through a few hundred, and older mail continues via `next_before_uid`. Merged multi-account search has no continuation point (UIDs are per account), so `mail_search` has the Agent search that account on its own. The cost: when a server that can search genuinely finds nothing, the headers are also checked locally (up to 2000 ENVELOPEs per page); "continue" then only matches headers of older mail, since the server's full-text search has already run.
- **IMAP `ID` is only sent when the server advertises the `ID` capability** (163/126 need it); don't rely on it for client identification.
- **`LIST`'s RETURN options are only sent when the server advertises LIST-EXTENDED (or IMAP4rev2)**: QQ Mail advertises none of the three, and a `LIST` with `RETURN` gets no reply and hangs until the 20 s timeout (both listing folders and finding `Sent` after sending were hit, tested 2026-10-30).
- Mail content enters Claude's context as is, which means handing the messages to the model; for sensitive mailboxes, consider a separate account that only gets the mail it needs.
