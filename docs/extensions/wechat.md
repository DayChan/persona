# WeChat bot: talk to Persona from personal WeChat

The **WeChat** extension in the public [Persona extension marketplace](https://github.com/nwverse/persona-extensions)
connects personal WeChat to Persona. Its extension, connector and channel IDs remain `wechat`.
Install it from **Settings › Extensions**, then bind the account from **Settings › Connectors › WeChat**
or **Settings › Channels › WeChat**.

It uses Tencent's public **iLink** API (`ilinkai.weixin.qq.com`): QR authorization provides a bot token,
`getupdates` long-polls incoming messages, and replies use the sender's `context_token`.
The protocol follows Tencent's MIT-licensed [openclaw-weixin](https://github.com/Tencent/openclaw-weixin);
source, notices and protocol tests live with the extension, not in this repository.

## Ownership

The extension is installed and upgraded independently of personad. The hub no longer bundles a WeChat
executable or registers a WeChat connector when no extension is installed. Clients discover its connector
and channel from the installed manifest, just like any other extension.

| Job | Owner |
| --- | --- |
| QR binding and connection status | Extension point `connectors`: connector `wechat`, capabilities `status` and `login` |
| Token, cursor and the bound user's latest `context_token` | `connector.secrets`, encrypted by the manager with its `secret.key` in Postgres |
| Receiving messages, sending replies and showing typing | Extension point `channel`: `channel.receive`, `channel.send`, `channel.typing` |
| Target conversation, commands, pending questions and completed turns | The assistant's shared channel pipeline (`docs/extensions/channels.md`) |
| iLink network calls | The extension, with its declared Tencent network permission |

The channel protocol passes incoming messages to the manager, which hands chat work to the assistant.
The extension does not need a full hub token, subscribe to conversation events or poll the chat itself.
The connector switch controls reception.

## Existing installations and handoff

The extension reads the existing encrypted `connector_secrets` row under connector ID `wechat`, including
the bot token, polling cursor and `context_token`. Installing a compatible extension does not require a
bound user to scan another QR code. Connector switches and channel targets retain their existing IDs too.
Do not sign out or uninstall as part of migration: those actions can delete credentials.

**Never run two iLink pollers with the same bot token.** Publish and verify the extension first, then have
an operator approve a handoff that stops the bundled runtime before starting the marketplace runtime.
Do not remove the bundled version from a deployed hub before the compatible extension is available.
If authorization has actually expired or been revoked, a new QR authorization is still required.

Two manager-side compatibility mechanisms remain; neither registers a built-in WeChat extension:

- `server/internal/extmgr/channels_migrate.go` moves legacy `target`, `runs` and `asks` fields once from
  encrypted connector state into shared channel settings/state. It preserves token and cursor fields.
- `server/internal/extmgr/bot_identities.go` reads the installed connector's bound user ID to report
  duplicate bot credentials across managers. It never reports tokens, cursors or context tokens.

## Binding

Enable the installed WeChat connector, select **Sign In to WeChat**, and scan the QR code shown in the
settings page. Confirm in WeChat; the status changes to connected when authorization completes.
On the same phone, save the QR code to Photos and select it from WeChat's scanner instead.

- The scanning account is the bound user (`ilink_user_id`). Only that user's messages reach Persona.
  Other senders receive a bound-user-only notice, at most once per ten minutes per sender.
- A verification-code step can follow scanning. Expired QR codes are refreshed automatically, with a
  bounded retry count; starting again after an expired login obtains a fresh code.
- Signing out tells iLink to go offline and removes the active token. Previously used tokens are retained
  in the extension's compatible sealed state for subsequent authorization.
- iLink error `-14` means authorization is invalid, for example after binding elsewhere or revocation;
  the connector becomes signed out and requests another QR login.

## Message flow

1. The extension long-polls `getupdates`, persists the cursor and deduplicates message IDs. It stores the
   bound user's latest `context_token`. Messages from other senders are never passed to the manager.
2. Bound-user text, WeChat-transcribed voice, quotes and supported decrypted images become a channel
   message. Unsupported files and videos are described in `unsupported` rather than silently discarded.
3. The shared pipeline handles deduplication, commands, answers to pending questions, and the selected
   conversation. It waits for the matching turn and requests typing indications while work continues.
4. The assistant splits replies to the channel's text limit and sends them through `channel.send`.
   The extension filters unsupported Markdown before sending to iLink. Without a usable `context_token`,
   sending returns `cannot_send`; a connected account alone is not proof a reply can be delivered.

## Questions, approvals and commands

WeChat has text interactions (`asks: text`), not interactive cards. Shared `channel.AskText` and
`channel.ReadAnswer` behavior applies:

| Interaction | Reply |
| --- | --- |
| Tool approval | Allow or deny; localized English, Chinese and Japanese equivalents are accepted |
| Plan | `1` approves and runs, `2` approves with automatic file edits; denial sends it back |
| Question | Option number, space-separated numbers for multiple choices, or a written answer |
| Terminal dialog with readable options | Option number |
| Phone-only form or unreadable terminal interaction | Complete it in the app |

Only conversations driven by this channel are forwarded: its target, conversations opened with `/new`,
and conversations waiting for its answer. When several questions are pending, the reply answers the
most recently sent one. A question that cannot be sent remains answerable in the app.

Commands come from the shared pipeline: `/help`, `/new [first message]`, `/main`, `/stop`, `/status`.
The target can also be selected under **Settings › Channels › WeChat**. `pushes` and `sync` remain
separate channel preferences, off by default. Group chats, outbound voice/files, Inbox/history import
and `/agents` are not supplied by this channel.

## Verification

Protocol, QR-login, secret compatibility and channel transport tests run in the extension repository.
Manager tests here cover shared channel behavior, old-state migration and bound-user identity reports.
App QR rendering can be checked with `QRSignInTests` and the mock hub's `MOCK_WECHAT=signed_out`
fixture (`docs/operations/testing.md`); that fixture represents a hub with an installed extension.

A production migration requires additional operator evidence: a published signed marketplace index,
a single extension poller, retained connected status without re-scanning, and one inbound/outbound
round trip in a test conversation (for example one opened with `/new`). Unit tests and a connected
status do not replace that live check. Record verified results in the migration issue.
