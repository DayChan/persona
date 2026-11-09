# Plan: Discord connector (the user's own bot)

Status: design settled (2026-11-05), revised after review before anything shipped (2026-11-08: REST first, the Gateway only from D3), D1 merged (2026-11-09, nwverse/persona-extensions#181, `discord` 0.1.0), D2 next. Tracking issue: #917 (#2828 before the 2026-11-08 repository rebuild); #1106, #1426, #1730 and #2057 below refer to the history before that rebuild.
Discord is a **connector** (a data source: knowledge base, Inbox, assistant tools), not a channel (an entry point for talking to Persona, `docs/extensions/channels.md`);
making the same bot a channel too is a separate issue. The first version is **read-only**. The framework is in `docs/extensions/connectors.md` (capability list, Inbox, knowledge base, gates) and `docs/protocol/connector-protocol.md` (protocol v2);
extension manifest, permissions and sandbox are in `docs/extensions/extensions.md`. This doc only covers what is specific to Discord. The code lives in the public extension repository `nwverse/persona-extensions` (`extensions/discord/`), like Telegram (#2057).
When done, the design is merged into `docs/extensions/connectors.md` and this file moves to `docs/archive/`.

## What we can see: Discord only allows your own bot

Of the ways to read Discord messages, only the last two are allowed by the terms and usable by ordinary people:

| Route | What it sees | Usable? |
| --- | --- | --- |
| User token / self-bot (using your own account, like Telegram) | Everything you can see, including DMs | **No**: violates the Terms of Service and Community Guidelines (automated user accounts); gets the account banned |
| OAuth message reading (`messages.read`, `dm_channels.read`) | The user's DMs and group DMs | **No**: only for partners Discord approves |
| Your own bot (bot token) | Channels it has permission for in servers it was added to; DMs others send the bot | Yes: adding a bot to a server needs the "Manage Server" permission, so it's your own server or one whose admin agreed |
| A user-installed app (User Install) message command "Send to Persona" | When the user right-clicks a message **anywhere** (DMs, group DMs, servers the bot isn't in) and picks it, the full text of that one message | Yes: the command interaction carries the full message (`data.resolved.messages`), no Message Content intent needed; interactions come over the Gateway, no public address needed |
| Data package (Settings › Data & Privacy › Request Data) | Only messages you sent; can take up to 30 days | Yes, but it's only half of each conversation; later |

So Persona's Discord = **server channels your own bot can see** + **messages you click "Send to Persona" on**. DMs between someone else and the user are invisible unless the user clicks; that's Discord's boundary and we don't work around it.

## Shape: the `discord` extension

- `extensions/discord/` in the extension repository: a Go process extension on the hub (`runtime`) contributing one connector, called over the plugin protocol (`server/internal/connector/proc`), like Telegram.
  A Go program is still how a new extension ships (the extension repository's development guide, "Add a program extension").
- **REST is the source of truth; the Gateway is not part of D1.** Each archive call and each tool call reads what it needs from Discord's REST API (the bot's servers, their channels and active threads, cached for two minutes so a run's three calls share it) and compares each conversation's `last_message_id` with the cursor.
  Nothing is kept up from events, so nothing drifts: a missed event, a changed role or a restart cannot leave stale state behind. The cost is a few requests per server per pass and the hub's poll interval as latency (`poll_seconds: 300`), which the knowledge base does not mind.
  Everything that decides what is read is in the cursor the hub keeps; the only thing in memory is which conversations to leave alone for a while, and losing it repeats some requests, never skips a message.
  (A first version kept archived-thread scans in memory across calls; the code review found four ways that could skip threads: a server joined later, a cursor the hub could not keep, a held channel, a failing server.)
  A Gateway connection in D1 would not even make the archive faster: `connector.changed` kicks the hub's knowledge-base loop, but the loop still only reads connectors whose poll interval has passed (`server/internal/assistant/kb_sources.go`).
- The Gateway comes with D3, which needs it: interactions of a user-installed app arrive over the Gateway (or a public interactions URL, which most hubs don't have). From then on it is a doorbell and the interactions channel only:
  its failure delays things back to polling and adds a note to the status, it never takes the connector down. D2 may use it the same way to see mentions sooner.
- A small REST client of its own, no Discord library: per-route buckets from `X-RateLimit-*`, one retry of a short 429, and a longer wait answered as `rate_limited` instead of waited out, so no call outlives the hub's two-minute call timeout.
  (disgo was tried first: its Gateway client races between heartbeat and close under `go test -race`, its message search retries for minutes while Discord indexes, `after=0` is silently dropped, and it logs bodies.)
- Uses only HTTPS (and WSS from D3), so it **can run in the sandbox**: `permissions.network` lists Discord's hosts, and outbound traffic goes through the hub's egress proxy (which tunnels WebSockets too).
- Being HTTP only, D1 and D2 can later become a TypeScript source extension (`docs/archive/extensions/source-extension-migration-2026-11-07.md`) as soon as those run in production; only D3 waits for controlled outbound WebSocket support (#1166) there.
- Doesn't run on a node: programs on a node can't call hub methods (`docs/extensions/extensions.md` ("Programs that run on a computer")), so they can't be connectors.

What the manifest looks like when all steps are done (D1 has `status`, `tools`, `archive`, `login` and only `discord.com`; D3 adds the Gateway's hosts):

```json
"connectors": [{
  "id": "discord", "name": "Discord",
  "capabilities": ["status", "tools", "inbox", "archive", "login", "threads", "fallback"],
  "poll_seconds": 300,
  "kinds": ["mention", "saved"],
  "kind_descriptions": {
    "mention": "Messages in a Discord server that @ the user or reply to the user",
    "saved": "A message the user sent to Persona from Discord (\"Send to Persona\")"
  }
}],
"configuration": {"triage_rules": "… (required, #1730)", "history_days": "How many days to backfill the first time, default 30"},
"permissions": {"network": ["discord.com", "gateway.discord.gg", "*.discord.gg", "cdn.discordapp.com", "media.discordapp.net"]}
```

## Login and credentials

The user creates their own Discord application; both the bot and the app belong to the user (the developer on Discord's side is the user):

1. Create an application in the Discord Developer Portal: on the Bot page turn on the **Message Content intent** and turn off **Public Bot** (only you can invite it); on the Installation page check **User Install**.
   A personal bot is tiny, so the Message Content intent needs no review (only large apps do).
2. Enter the bot token under **Settings › Connectors › Discord**: `login.step` has a single step, a field to fill in (the first of the three step types in `docs/extensions/connectors.md` ("Login supports 'open a web page to authorize'")), with instructions for the Developer Portal.
3. The extension asks `GET /applications/@me` with the token: 401 means the token is wrong. The answer has the bot, the owner (for a team-owned app the team's `owner_user_id`: `owner` is then a pseudo-user) and the application's flags.
   The owner is "me", used to tell which messages I sent and who @'d me.
4. Signing in never waits on the rest of the setup: with the Message Content intent off (neither `GATEWAY_MESSAGE_CONTENT` nor `GATEWAY_MESSAGE_CONTENT_LIMITED` in the flags), or with the bot in no server yet,
   the sign-in still succeeds and the status says what to do (`no_permission`, its guide), and turns `ok` by itself once it is done; nothing to type again.
   The invite link (`scope=bot`, permissions only "View Channels" + "Read Message History", `integration_type=0`) is in that guide and in the answer of `discord_channels`; the apps show a guide only when the state is not ok.
   D3 adds the "Install to my account" link (`integration_type=1`, `scope=applications.commands`).
5. The token is encrypted via `connector.secrets.*` into the hub's database (`connector_secrets`); the App only shows the bot's name; it's deleted on sign-out. Tokens never appear in logs.

The Gateway (from D3) asks for `GUILDS`, `GUILD_MESSAGES` and `MESSAGE_CONTENT` (and `GUILD_MESSAGE_REACTIONS` if D2 uses it for "done"). Not `GUILD_MEMBERS` or `GUILD_PRESENCES`.

## Knowledge base: what to read

- **Conversations**: one conversation per channel / thread / forum post, `discord-<channel id>`, named "Server #channel" or "Server #channel › thread", `kind: group`.
  The bot's DMs with "me" are not collected (they only contain tests), nor are DMs others send the bot.
- **Which channels**: every text channel, announcement channel, thread and forum post the bot can read. To exclude a channel, don't give the bot "View Channels" on it in Discord. Persona has no separate channel picker.
  From 2026-11-16 Discord leaves channels the bot cannot view out of `GET /guilds/{id}/channels`; before that, such a channel answers 403 and is left alone until its next message or six hours (refused requests count toward Discord's IP ban).
  A conversation that shows nothing though it has messages (no "Read Message History", or its newest messages deleted) keeps its position and is tried again on its next message or after six hours, so history is read once the permission is given and a deleted message never delays the next one.
- **One conversation failing** (a 5xx, a 400, a route's rate limit, a message that can't be decoded) holds up only itself: the others are read, the status stays ok, and it is tried again on its next message or after 15 minutes. Only the network, a refused token or a global rate limit end a call.
  A message that can't be decoded is left out (paging goes by what Discord answered, so it never ends a read early); a page with none that can is not read past.
- **First time**: backfill `history_days` days (default 30) per channel, at most 500 messages per channel; `GET /channels/{id}/messages?before=` returns 100 per page.
  Threads are backfilled if active, or archived within that window: each channel's archived threads are listed when its last listing is older than 50 minutes (a thread archives after an hour of quiet at the earliest),
  the first time over the window, then since an hour before its last listing. A listing goes a page per call and is kept per channel in the cursor (when it started, how far back it got); it moves back past a thread once that thread was read
  (a thread held back for failing, being refused or showing nothing is kept as owed in the cursor and read on its own, so it never stops its channel's listing),
  so a server joined later is listed over the window, a listing the hub could not keep is done again, a channel that can't be listed now holds up only itself, and a large forum costs a request per call rather than its whole list again each call.
  An archived thread that shows nothing (its messages deleted: the listing itself needs "Read Message History") gets its position anyway.
  Archived private threads are not read: Discord lists them only to moderators.
- **After that**: each pass reads the channels whose `last_message_id` moved past the cursor, `after=` the cursor (pages come newest first: the next anchor is a page's largest id; `after=0` must be sent as 1).
  The cursor (`archive.changes`' `cursor`, stored by the hub) is "conversation → last message id; channel → when its archived threads were last listed completely, and how far a listing going on got". One that can't be read starts over, and a position older than the window reads on from the window's start rather than everything since (the archive was off for months):
  the knowledge base keeps a receipt per message id and drops replays (`Ingest` in `server/internal/kb/update.go`), so starting over costs requests, never duplicates. Channel ids are global, so another bot's positions stay valid. Entries of conversations gone for longer than the window are dropped.
- **Message Content off**: Discord then sends messages with empty content over REST too. Each pass first reads the application's flags; while the intent is off, no cursor moves, so nothing is skipped: once it is back on, the next pass reads from where it stopped.
  Turning it off in the seconds between that check and a read is not caught: a per-page "content withheld" check would misread legitimately empty messages and make the status flap, which costs more than it saves.
- **Budget**: a call reads at least one conversation, then stops after about 25 seconds, `limit` conversations or about 2 MB (source extensions carry 8 MB per frame) and answers `more`; the hub calls again (three calls per run).
  Conversations read before (furthest behind first) and those never read take turns, so neither a busy server nor a newly joined one holds up the other.
- **System messages**: a new thread is noted; joins, boosts and pins are left out (noise for the model). Attachments, stickers, polls, forwards, link cards with their fields and bots' component messages are archived as text from D1 on.
- **Messages**: sender display name, body (mentions written as `@name`, `#channel`, custom emoji as `:name:`), the quoted reply, embed titles and links; messages "I" sent are marked `mine`.
  Edits and deletions are not synced in the first version (item 3 of "Decided by the user").
- **No people records** (item 2 of "Decided by the user"; Discord's developer policy forbids profiling users): messages carry only the sender's name, **no `sender_id` and no `sender_handles`**.
  The knowledge base only creates people and records identity hints when a message has `sender_id` (`notePerson` in `server/internal/kb/archive.go`), and person notes during compaction only cover people already in the conversation
  (`notePersonNote` in `server/internal/kb/compress.go`), so the hub needs no change; the extension's tests guard that no message carries either field.
- **Attachments** (D4): `archive.media` refs are message and attachment ids, and the file is fetched fresh when the hub asks (Discord's attachment links are signed and expire). They apply to messages archived from D4 on: the knowledge base keeps the first archived version of a message.
- **"Send to Persona" messages** go into the knowledge base too: into the conversation of their channel, `discord-<channel id>` (also for DMs and channels the bot can't see; such conversations contain only the messages the user clicked),
  named from the channel info in the interaction. Until the hub fetches them, they're kept in the extension's own data directory (`PERSONA_EXTENSION_DATA`), so an extension restart loses nothing.

## Inbox: what counts as "needs me"

`inbox.collect` answers from memory and is called whether or not the knowledge base is on, so D2 has a poller of its own (every one to two minutes, the same discovery and "read after" as the archive, its own positions in `PERSONA_EXTENSION_DATA`);
the Gateway, once D3 brings it, only makes it sooner. For the first look back (8 days), one `messages/search?mentions=<me>` per server is cheaper than reading every channel. "Reacted to it" needs a REST read per message without the Gateway, so it may wait for D3.
D3's "Send to Persona" messages wait in the data directory until a later cursor shows the hub ingested them (a sequence number with a random epoch, so a reinstall can't take new ones as acknowledged), with a cap on their age (nothing drains them while the knowledge base is off).

| kind | What | Tier |
| --- | --- | --- |
| `mention` | In a server: @ me, a reply to me, @ a role I'm in | Action (action) |
| `mention` | @everyone / @here | FYI (fyi) |
| `saved` | A message I clicked "Send to Persona" on | Action (action) |

- **Done**: I later posted in the same channel / thread, reacted to the message, or handled it in Persona. Discord exposes no read state to bots, so read state isn't used.
- Anything older than 8 days no longer enters the Inbox (like Telegram). Messages sent by bots don't enter the Inbox.
- `triage_rules` is required configuration (#1730); with the `fallback` capability, every item carries `extra.rule_tier`. `thread` is the channel (thread) id; several items from the same channel merge into one row.
- When "Send to Persona" is clicked, the extension replies within 3 seconds with a message only I can see (ephemeral): "Sent to Persona".

## Assistant read-only tools

| Tool | What it does |
| --- | --- |
| `discord_channels` | Servers and channels the bot can see, most recently active first |
| `discord_messages` | Messages in a channel / thread, paging back in time |
| `discord_search` | Search messages in a server (`GET /guilds/{id}/messages/search`); while Discord is still indexing (returns 202), says so and points the assistant at the knowledge base, which has the archived messages (the extension does not ask for `kb.read` to search it itself) |

Tool descriptions and parameter docs live in the extension's own `prompts/mcp/` (the rules in `docs/assistant/prompts.md`). Message content is treated as untrusted data (prompt injection); tool results carry no tokens and no user ids.
Each message starts with a line of its time, place, sender and id; the place (a thread's name is anyone's words) and the sender's display name are quoted with "(the user)" outside the quotes, what it says follows on indented lines,
and server, channel and thread names stay on one line everywhere, so neither a name nor a message's text can pass for the user or for another message. The tools' notes (attachments, replies) are English; the archive's follow the user's language.
`discord_messages` reads any conversation the bot may read, archived threads and forum posts too (a search finds them; discovery lists only active ones).

## Conversation page

`threads`: Discord conversations in the Inbox open as a conversation page in the App (`thread.page`, one page fetched live over REST), drawn as group-chat bubbles (`kind: group`), with avatars and images via `thread.media`.
The first version has no input box, only "Reply in Discord", which opens `https://discord.com/channels/<server id>/<channel id>/<message id>` (for DMs the server id is `@me`).

## Network

Some networks can't reach Discord directly (if the hub is on one, both REST and the Gateway fail):

- **Not sandboxed** (extensions from a trusted market run without the sandbox by default): the extension's secrets can include `proxy` (`http://`, `https://`, `socks5://`, optionally with username and password, which is why it's a secret rather than config); REST, Gateway and CDN all go through it.
- **Sandboxed**: the extension can only go out through the hub's egress proxy, which in turn uses the hub process's own `HTTPS_PROXY`. Per-extension upstreams are a later hub change (#1106); until then the whole hub has to go through the proxy.
- It doesn't run on a node (see "Shape"), so you can't "pick a computer that can reach Discord" to run it. For that, run a proxy on that computer and send the hub through it.

## Discord's rules

The parts of Discord's Developer Policy and Developer Terms that concern us:

- **No user profiling** (Policy §16: don't use API data to profile Discord users, their identities, or their relationships): so no people are created from Discord senders and no identity hints are recorded (the "Knowledge base" section).
- **No training models on message content** (Policy §21): Persona only gives messages to models for summarizing, triage and answering (like every other source), never for training.
- **Delete on request**: the user can "Delete stored knowledge base content" under **Settings › Connectors › Discord**; signing out stops collection and deletes the token.
- **Encryption at rest**: the token is encrypted in the database; the knowledge base is files on the hub machine (same for all sources) and relies on the hub machine's disk encryption, as stated in `docs/extensions/connectors.md`.
- **Rate limits**: 50 requests per second globally, per-route buckets from response headers. More than 10,000 invalid requests (401, 403, 429; not 429s with `X-RateLimit-Scope: shared`) in 10 minutes gets the IP temporarily banned,
  so on 401 it stops asking anything until the user signs in again, and a channel that returns 403 is left alone for six hours.

## Status and self-healing

- The hub hides a connector's tools and stops archiving while its status is not `ok` (`server/internal/connector/registry.go`, `server/internal/extmgr/kb_sources.go`), so a non-ok status is only for what needs the user or will not pass by itself:
  - `not_signed_in`: no token, or Discord refused it (401). Nothing asks Discord until a new sign-in.
  - `no_permission`: Message Content off, or the bot is in no server (the guide has the invite link).
  - `rate_limited`: a long global rate limit, until it ends.
  - `error`: the health check could not reach Discord (the guide mentions the proxy).
- A failure of one conversation, one call or one request never changes the status; the call answers its error and the hub tries again at its next poll.
- A health check (`GET /applications/@me` and the bot's first server) runs every 10 minutes while ok and every minute while not: whatever the user fixed in Discord shows within a minute, without a restart.
  A definite answer (token refused, Message Content off, no server) changes the status at once; a failure that is not (the network, Discord failing) only the second time in a row.
- **From D3 on**, "Message Content off" and "in no server" must not make the status non-ok: "Send to Persona" needs neither, and a non-ok status stops the Inbox. They become ok with nothing to archive, and the hint moves to `discord_channels`
  (or the apps learn to show a connector's guide while ok, a small generic change in this repository).

## Testing

- Fake Discord: `httptest` serves the REST API with synthetic servers, channels, messages and errors (and, from D3, the Gateway and interactions); no real account needed.
- The extension SDK's protocol checks (`exttest.Check`, `exttest.CheckConnectors`).
- Guarded: messages have no `sender_id` / `sender_handles`; tokens never appear in logs, status or tool results; no cursor moves while Message Content is off or history can't be read; a refused token stops all requests;
  a transient failure doesn't change the status; one failing conversation doesn't hold up the others; a deleted newest message doesn't delay the next one; an emptied archived thread doesn't keep the look from completing;
  a long gap reads only the window; every call makes progress within its time and size limits; `@everyone` is FYI and items I later posted after or reacted to count as done (D2).
- The real-account path (create the app, invite, archive, tools; from D3 "Send to Persona" getting the full text in a DM) is checked by hand, and the PR states truthfully what was checked.

## Steps

One PR per step; PRs in the extension repository write `Refs nwverse/persona#917`:

| Step | What |
| --- | --- |
| D1 | Login, status and health check, knowledge base over REST (first backfill, then what moved), read-only tools, `proxy`, tests |
| D2 | Inbox (`mention`, deciding "done", `triage_rules`), with its own poller |
| D3 | The Gateway (doorbell + interactions) and the "Send to Persona" message command (`saved`): first verify with a real account that the interaction in a DM carries the full message text |
| D4 | Conversation page, attachments |

This repository: this plan (the first PR); one row in the table in `docs/extensions/connectors.md` after D1 merges (done); the Discord brand mark (all three platforms, or wait for #1426, data-driven brands).

## Not doing / later

- **Not doing**: self-bot / user token, OAuth message reading ("What we can see").
- **Later**: sending messages as the bot (as an action, through the gate with `confirm_always`, at most 2000 characters per message); data package import; the same bot doubling as a channel (DMing the bot is talking to the main chat, like the Lark bot);
  syncing edits and deletions. Each gets its own issue.

## Decided by the user (2026-11-05)

1. Scope: only "your own bot + Send to Persona", no self-bot.
2. People in the knowledge base: archive chats only, don't create people from Discord senders (Developer Policy §16). The extension just doesn't send `sender_id` / `sender_handles`; no toggle is added to the knowledge base.
3. Edits and deletions: not synced in the first version; rely on the existing "Delete stored knowledge base content"; syncing deletions is for later.
4. The first version is read-only; sending as the bot is decided later.
5. Where: the plan and tracking issue live in this repository (#917), the code in `nwverse/persona-extensions`.
