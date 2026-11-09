# WeChat bot: talk to Persona from personal WeChat

When the user DMs a bot in personal WeChat, they are talking to Persona: the message goes into the main chat of the "Persona" tab (or a chat opened with `/new`), and that turn's answer is sent back to WeChat as the bot.
Whatever Persona and the Agents it drives need the user to answer or approve is sent to WeChat as text; replying with a number or text answers it (#1352).

It uses **iLink** (`ilinkai.weixin.qq.com`), the AI bot API Tencent opened up for personal WeChat: the user scans a QR code in WeChat to authorize and gets a bot token, then long-polls for messages and replies with the
`context_token`. The protocol follows Tencent's open-source [openclaw-weixin](https://github.com/Tencent/openclaw-weixin) (MIT, see `THIRD_PARTY_NOTICES.md`).

## Why an extension

Things that only make sense for one service are built as extensions (`docs/extensions/extensions.md`), not added to the hub. This one is a separate program next to personad, `personad-wechat`, registered as a **built-in extension**
`wechat` just like Telegram (shipped in the same build and swapped together on auto-update; if its `version -v` differs from the hub's, it doesn't start). It is a **channel** (`docs/extensions/channels.md`): the program only talks to WeChat.
"Which chat a message goes into, waiting for the turn to finish, sending the answer back, Agent questions" are all the shared pipeline (the same one as the Lark bot; since 2026-11-04 the assistant manages the chat side, and since 2026-11-05 sending/receiving and the WeChat program are in the managers too, see `docs/extensions/channels.md` ("Who owns what")).

| Job | What does it |
| --- | --- |
| QR binding, status and toggle in Settings | Extension point `connectors`; the `wechat` connector has only two capabilities, `status` + `login`; login is a QR step (`kind: qr`, #1351). **Bind** under **Settings › Channels › WeChat** uses the same login |
| Storing the token, cursor, and the owner's latest `context_token` | `connector.secrets` (the manager encrypts it with its `secret.key` into the database), all in one JSON |
| Handing the owner's messages to the hub, sending the hub's replies to WeChat, "typing" | Extension point `channel` (#1466): the program calls the hub's `channel.receive`; the hub calls the program's `channel.send` and `channel.typing` |
| Reaching WeChat | Permission `network: *.weixin.qq.com` (shown only; the hub doesn't enforce it yet) |

The first version (#1447) was stitched together from `hub_api` + `events`: the plugin held a hub token, polled the session every 2 seconds, tracked the turn itself, and wrote questions as text itself. #1466 moved all of that into the hub's channel pipeline;
the plugin no longer holds a hub token or subscribes to events. What used to live in the plugin's state (which chat to send to, pending answers and questions) is migrated the first time the hub starts the new version (`extmgr/channels_migrate.go`), so already-bound users don't need to scan again.

The process follows the connector's toggle: WeChat messages are only received while it is on.

Code: the plugin is `server/plugins/wechat`, a plugin like any other: it speaks the plugin protocol through the Go SDK (`sdk/go`: `ext` and the `schema/extension` wire types) and uses no other code of this repository. `server/plugins/guard_test.go` reads every file, whatever its build constraints, and also fails when the plugin is imported by anything but a program of its own, such as `server/cmd/personad-wechat`: a directory of `server/cmd/` whose code uses only the SDK and the plugin (`_test.go` files may import it). Program entry `server/cmd/personad-wechat` (the old name `personad-wechat` is an alias), built, released and bundled with personad as before;
the iLink protocol is `server/plugins/wechat/ilink` (stateless, no logging, accepts only Tencent's HTTPS origins); the bot is `server/plugins/wechat` (`wechat.go` main loop and storage, `login.go` QR login, `chat.go` receiving, sending, "typing", `program.go` the SDK program, its manifest and `version -v`), with its own texts in `locales/<lang>.json` (en, zh-Hans, ja).
The hub registers the built-in from `server/internal/wechat/spec` (connector and channel, known before the program runs) in `server/internal/extmgr/wechat.go`; `TestWeChatPluginDeclaresWhatTheHubRegisters` holds the two to the same declarations. The pipeline is in `server/internal/assistant/channels*.go` (the extension side is `extmgr/channels_extension.go`), rules in `server/internal/channel`,
and the note added when sending into a chat is `server/internal/prompts/channel/message.md`.

## Binding

**Settings › Connectors › WeChat**: turn on **Enable WeChat**, tap **Sign In to WeChat**, and the page shows a QR code (the hub renders the dot matrix and sends it to the App). Scan it with WeChat, confirm on the phone, and the page switches to connected on its own.
You can't scan your own screen on the same phone: tap **Save QR Code** to save it to Photos, then in WeChat use Scan and pick it from the album.

- The WeChat account that scans is the **owner** (iLink's `ilink_user_id`); only the owner's messages are forwarded to Persona. Anyone else gets one reply, "This bot only works for the person who bound it." (at most once per 10 minutes per person).
- WeChat sometimes asks for a verification code (the phone shows a string of digits); the login page then turns into an input box. An expired QR code is replaced automatically (up to 3 times); if scanning still isn't done 5 minutes after starting, the next poll gets a fresh one.
- Signing out: tells iLink to go offline and deletes the token. The last 10 old tokens are kept and sent on the next authorization so iLink recognizes it as the same bot.
- Bound again elsewhere (iLink returns `errcode -14`): unbinds automatically, and the status says "WeChat's authorization is no longer valid (bound again elsewhere, or revoked). Scan the code again to bind".
- **Sign out** under Settings › Connectors, **Unbind** under Settings › Channels, and automatic unbinding all clear the channel's pending turns and text questions. After binding again, an ordinary message such as "ok" cannot answer a card left from the previous binding; the card itself remains in the App (#1837). The extension manager detects automatic unbinding when the program saves its state without credentials, rather than comparing the localized account label.

## How a message flows

1. The program long-polls `getupdates` (the cursor lives in secrets, so it resumes after a restart) and dedups by message id (keeps the last 200). It records the `context_token` of the owner's latest message: iLink needs it to send anything.
   Messages from anyone else get one reply, "This bot only works for the person who bound it.", and are not handed to the hub.
2. The owner's message is turned into a `channel.Message` and handed to the hub (`channel.receive`): text (voice uses WeChat's own transcription), quotes, images (up to 4, decrypted);
   files and videos can't be converted yet and go into `unsupported` ("File "…"").
3. From here it's all the pipeline (the hub hands it to the assistant, `docs/extensions/channels.md` ("How a message flows")): dedup, commands, answering a pending question, sending into the main chat or a `/new` chat (`client_msg_id` is `wechat-<message id>`),
   tracking the turn (find this message, wait for it to finish, background tasks answering twice, merging, timeouts), and telling the program to show "typing" every 5 seconds while it runs.
4. Sending the answer back: the assistant splits it at 3500 characters and sends it through the hub to the program (`channels.send` → `channel.send`). The program strips Markdown WeChat renders badly (images, strikethrough, level 5–6 headings, italics on Chinese text; bold and code stay) and sends it.
   If the owner has never sent a message (no `context_token`), the program returns `cannot_send`.

## Questions and approvals

WeChat has no cards (the channel capability is `asks: text`). When an Agent (the main chat, or a chat WeChat is driving) needs you to answer or approve, the hub writes the card as text and sends it to WeChat; your next message is the answer
(`channel.AskText` / `channel.ReadAnswer`):

| Card | How to answer in WeChat |
| --- | --- |
| Tool approval | Reply `允许` (allow; also 同意, 可以, 好…) or `拒绝` (deny); English and Japanese forms (allow / yes / deny, 許可 / はい / 拒否…) are accepted too, regardless of the user's language; anything else gets one more prompt |
| Plan | `1` approve and auto-run, `2` approve and auto-accept file edits, `拒绝` (deny) sends it back; anything else sends it back with that text attached |
| Question | Reply with the option number (space-separated for multi-select), or just write the answer; several questions are asked one at a time |
| Dialogs in the terminal (Codex, etc.) | Reply with the option number |
| Things to fill in on the phone, forms (elicitation), terminal screens whose options can't be read | Replies "This one needs the app." |

- Only chats "WeChat is driving" are forwarded: the main chat, chats opened with `/new`, and chats waiting for an answer. Questions from other Agents on your computers stay in the App as before.
- If the owner has never messaged the bot, there is no `context_token` and nothing can be sent. In that case it isn't recorded as "asked in WeChat", and it can still be answered in the App.
- When several are pending at once, a reply answers the most recently sent one.

## Commands

Implemented once in the channel pipeline (assistant): `/help`, `/new [first message]` (later messages go to the new chat), `/main` (back to the main chat), `/stop` (stop the current target's turn and stop waiting for its answer),
`/status` (who messages go to, how many answers and questions are pending). Where messages go can also be chosen under **Settings › Channels › WeChat**.

## Compared with the Lark bot

| | Lark bot (`docs/extensions/bots.md`) | WeChat bot (this doc) |
| --- | --- | --- |
| Where | Sending/receiving in the hub (`hub/bots*.go`), chat logic in the assistant (`assistant/feishu*.go`) | Built-in extension, a separate program that only talks to WeChat (`channel` extension point) |
| Tracking the turn | The assistant's channel pipeline (the same one, `assistant/channels_runs.go`) | Same |
| Binding | Enter a custom app's App ID / Secret, then send a pairing code | Scan a QR code |
| Questions and approvals | Card buttons | Text back and forth |
| Progress | Progress card | "The other party is typing" |
| Driving Agents on your computers (`/agents`) | Yes | No (first version) |
| Phone notification for a turn started here | No | No (same as the Lark bot since #1466) |
| Push / sync the main chat | `pushes` / `sync` | The same two toggles (in channel settings, off by default) |

## Not doing (first version)

Group chats (iLink only has DMs with the bot), sending voice and files, WeChat messages into the Inbox / knowledge base, `/agents`.

## Verification

- `go test ./plugins/...`: the dependency guard; the program against the SDK's fake hub (`exttest.CheckConnectors` / `CheckChannels`, every message held to `schema/extension`); the iLink protocol against a fake iLink server (every QR state, redirects only to Tencent origins, long-poll timeouts, being signed in elsewhere, encryption/decryption, Markdown filtering and splitting);
  the program against a fake iLink and a fake hub (binding, handing the owner's messages to the hub, strangers, unbinding when signed in elsewhere, images and files, dedup on redelivery, `cannot_send` without a `context_token`, "typing"); its `version -v` and locales.
- `go test ./internal/hub -run 'WeChat|Channel'`: the manifest the hub registers (connector, channel, permissions); conformance tests of the program against the process-extension protocol and the channel protocol (`channel/conformance`);
  the shared pipeline against a fake channel program and a fake node (message into the main chat and answer sent back, splitting, dedup, text approvals, already answered in the App, can't send, commands, targets); migration of old state.
- App: run `QRSignInTests` against the mock hub with `MOCK_WECHAT=signed_out` (`docs/operations/testing.md`).
- Real WeChat: needs the user's own WeChat to scan, which neither CI nor unit tests can cover; test on the user's hub after deploying.
