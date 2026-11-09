# Connector protocol (v2)

The protocol between hub and a connector running as its own program. Used by the first-party Telegram extension (#1704); third-party connectors follow this too. Design / relationship to in-hub connectors: `docs/extensions/connectors.md` (“II · 4”). Program side `server/internal/connector/serve`, hub side `server/internal/connector/proc`, conformance tests server/internal/connector/conformance.

**Now the connector namespace of the process-extension protocol** (#1101, `docs/extensions/extensions.md`, “Process-extension protocol”). Connector program is an extension contributing connectors. Hub opens via initialize; prefix every method below with `connector.` (status → `connector.status`), add contribution (connector ID) to params. All other params, results, requirements unchanged. Program-to-hub `secrets.*` and changed also prefixed with contribution. Process startup / stop / restart, config / user secrets are extension-platform responsibilities (`docs/extensions/extensions.md`).

**The schema**: every call, event and answer below is described in `schema/extension/connector.schema.json` (with its
calls in `x-methods` / `x-events`; the knowledge-base calls an extension makes directly are `kb.schema.json`), and the
SDKs' types are generated from it (`sdk/go/wire`, `sdk/ts/src/wire.ts`). A program can be written from the schema and
the SDK alone: `ext.Methods` answers the connector namespace with typed methods, `ext.Hub` has the program's calls,
and `exttest.CheckConnectors` / `exttest.Start` hold it to the protocol against a fake hub. The conformance checks
below hold every message both ways to the schema, and `server/internal/extension/conformance/plugin_schema_test.go`
holds the schema to the server's Go types (schema/extension/README.md, "Plugin-side schemas and calls").

`connector/serve` accepts process-extension initialize + `connector.*` (hub now uses only this), and legacy init + unprefixed methods below (standalone protocol retained one release cycle, removed at next protocol change). Go programs use `serve.MainWith`, also receiving declared user secrets (`Init.Declared`) and data directory under process protocol.

## Transport

- One JSON per line (`internal/rpc`, same as hub↔node):
  - Request `{"id":"h1","method":"status","params":{}}`
  - Response `{"id":"h1","result":{…}}` or `{"id":"h1","error":{"message":"…","code":"…"}}`
  - One-way event `{"event":"changed","params":{"scope":"inbox"}}`
- Stdin / stdout only currently: hub starts program; program reads stdin / writes stdout. Stderr logs collected by hub, **no body text, numbers, tokens**. Program exits when hub closes stdin. Persistent-service WebSocket transport awaits first external connector (`docs/extensions/connectors.md`, decision 3).
- Snake_case fields, RFC 3339 timestamps, absent fields optional.

## Handshake

1. Hub sends init: `{"protocol": 2, "config": {…}}`. Config is connector's section of the manager's manager.json, possibly augmented at runtime, e.g. timezone.
2. Program returns manifest:

   ```json
   {"protocol": 2, "id": "example", "name": "Example", "capabilities": ["status", "tools", "actions", "inbox", "archive", "login"],
    "hosts": ["example.com"], "agent_tools": ["example_read"], "unlisted": ["example_send"], "poll_seconds": 600, "kinds": ["dm", "mention"]}
   ```

   - Protocol mismatch: program `wrong_protocol` error, or hub stops mismatched manifest and explains on settings page.
   - ID must match hub registration.
3. Hub has static manifest independent of program running, so disabled connectors expose capabilities / login. Mismatch logs and hub manifest wins.

Manifest fields:

| Field | Meaning |
| --- | --- |
| capabilities | status, tools, actions (writes below), events, inbox, archive, items (knowledge docs / meetings / events), login, options, threads (Inbox thread opens native chat), snapshot (unread chats / today's meetings for assistant), group (Inbox event grouping, requires inbox, “Event grouping” below), meta (connector tracks each event metadata; hub calls `group.append`, requires group / `meta_description`), fallback (every Inbox item promises `extra.rule_tier`, no method, requires inbox; `docs/extensions/extensions.md` “Generic Inbox rules”), machines (depends on user's computer; `machines.online` on connection, #2830), recall (can take back what it sent: the hub calls `recall` below, only once the user agreed; shown at install, `docs/extensions/extensions.md` “Taking back what it sent: recall”). Hub calls only declared capabilities |
| hosts | Link hostnames for app recognition |
| `agent_tools` | Tools exposed to coding Agents, name globs; omitted means assistant only |
| unlisted | Tools / writes excluded from model tool list, name globs; connector's frontend calls by name, e.g. Telegram tg CLI. Not permissions: direct calls still work, writes still gated, Agent access still `agent_tools` |
| `confirm_always` | Optional write-name globs always requiring phone confirmation. Matching `connector_gate` allow, including *, clamped to confirm; deny unchanged. User app Send via `Registry.Run` bypasses gate, unaffected. Optional field, no version change |
| `poll_seconds` | `archive.changes` polling interval; 0 uses hub default |
| kinds | Inbox item kinds; undeclared kind logs |
| `meta_description` | Required iff meta declared; describes every event-meta field, inserted verbatim into association prompt, max 4000 characters |
| `archive_sources` | Other archive source names besides own ID, max 4, ID format, not another connector ID. Separate one class of chats, e.g. user-related feed posts, or existing knowledge IDs before migration out of hub (“Knowledge base” below) |
| cli | Connector CLI name on assistant PATH, lowercase letters / digits / hyphens, e.g. tg; “Command line” below; omit if absent |

## Hub calls the program

| Method | Parameters → result | Notes |
| --- | --- | --- |
| `status` | `{}` → `{state, account?, via?, error?, guide?, last_sync?, last_event?}` | Memory-only, no network; hub polls every 30 seconds. State ok / `not_configured` / `not_signed_in` / `no_permission` / `rate_limited` / error |
| `tools.list` | `{}` → `{tools: [{name, description, input_schema}]}` | Read-only tools, names prefixed `<id>_` |
| `tools.call` | `{name, args}` → `{text}` | |
| `thread.page` | `{thread, before?, limit}` → `{chat, messages, before?}` | One Inbox thread page, oldest first; empty before means latest, earlier pages use returned cursor; limit max 50. Fields `connector/threads.go`: `ThreadChat` / `ThreadMessage` / `ThreadMedia`… `chat.reply` is reply action with `{chat, text, reply_to}` |
| `thread.media` | `{ref}` → `{mime, data}` | Base64 avatar / media bytes, max 4 MB. Ref immutable; hub persists, app caches forever. Changing avatars include version in ref |
| `actions.list` | `{}` → `{tools: [{name, description, input_schema}]}` | Writes, `<id>_` prefix |
| `actions.describe` | `{name, args}` → `{action: {name, visible}, summary}` | Phone card action / full content; visible = audience. No network. Error means invalid args, returned to caller without card |
| `actions.do` | `{name, args, request_id}` → `{text, message_ref?}` | **Only after hub gate allows**; args identical to card. Hub-generated `request_id` executes once; duplicates return first result (the `message_ref` too). Hub never resends after timeout. `message_ref` is the id, on the other side, of the message the write made, when the service gave one: the hub trims it and keeps it as the sent-as-user ledger row's `message_ref` (one longer than 200 characters is dropped, not cut) (`docs/extensions/connectors.md` "Sent as the user"). Optional and additive (no version change): a program that leaves it out is recorded all the same, a hub that does not know it ignores it. `tools.call` has the same result type and ignores it |
| `recall` | `{action, message_ref, target?, at?}` → `{recalled}` | Only for a connector that declares `recall`, and **only once the user agreed on their own device**: the program never starts one (a `recall` the program sends the hub is `unknown method`). The params are the sent-as-user ledger row's, none of what the message said: `action` (the tool's name, `sent.record`, or `METHOD /path` for a write the node's gate forwarded), `message_ref` (never empty), `target` (the row's, as kept), `at` (when the row was written, RFC 3339). Answer `{"recalled": true}` only when the service confirms the message is no longer there (one that was already gone included; a repeat after success is a success too): `{}`, `false` and anything undecodable are taken as not knowing. Errors: `not_recallable` (it cannot, nothing was sent), `not_signed_in` / `offline` / `rate_limited` (it cannot act now), `recall_unknown` (it may have gone through), a program that does not have the method answers `unknown_method`, and `unknown_method` and `no_capability` count as `not_recallable` to the hub; any other error is the service refusing, in one sentence (the hub keeps the first line, 200 characters). The hub gives the call 30 seconds, then counts it as not known. `message_ref` of a write is `actions.do`'s answer or `sent.record`'s |
| `inbox.collect` | `{}` → `{things: [InboxThing]}` | Memory-only |
| `inbox.reply` | `{thread, item_id?, last?, text, client_id?, changes?}` → `{}` | User pressed Send, itself confirmation. App `client_id` ensures one send on retry, connector derives if absent. `changes: [{ref, field, value}]` first update document-row status (Change Status / AI-twin proposal), allowing empty text |
| `archive.changes` | `{cursor, limit}` → `{batches: [{source?, chat, messages}], next, more}` | Program-owned cursor, stored / returned unchanged, initial empty string. Batches archive as `<source>-<chat.id>`; absent source uses connector ID, otherwise must be `archive_sources`; invalid source drops batch / logs |
| `archive.media` | `{ref}` → `{mime, data}` | Base64 archive media, max 20 MB, stored in chat attachments. Optional; absent yields [Attachment] row |
| `items.list` | `{}` → `{items: [{kind, id, title, url?, at?, where?}]}` | Knowledge items, kind docs / meetings / events; only if items declared |
| `items.doc_title` | `{url}` → `{title}` | Chat document-link title; unreadable empty string |
| `snapshot.get` | `{tz?}` → `{chats?: {at_me: [{name, unread, preview?}], other: [{name, unread}]}, meetings?: [{start, end, title}]}` | Snapshot declared only (#1520). Current user state for assistant live snapshot / Today card: unread chats awaiting user, mentions include latest-text prefix; today's meetings in tz, not ended / declined, start order. Fast: hub waits max 3 seconds, omits on timeout. Unknown portions omitted |
| `group.tool` | `{thread, name, args}` → any JSON | Group declared only, optional `unknown_method` tells model unavailable. Association model can inspect thread: `messages_before` {message,n}, n≤30; `messages_around` {message,n}, n before / after; search {query,n} matching thread messages. Each id / time / sender / truncated body. Raw result to model, remove avatar / media, truncate >8 KB; errors `{"error": …}` |
| `group.append` | `{thread, event, meta, messages, continues?}` → `InboxThing` | Meta declared only, append to one event. Existing event ID / current meta; new event empty ID / null meta, continues refers to previous done event. Return full item with ID / updated meta (“Event grouping”). Hub retries may repeat; deduplicate by message ID |
| `group.fallback` | `{thread, batch, events: [{id, meta, done}]}` → `{assign: [{message, event}], new: [{key, messages, rule_tier, title}]}` | Optional `unknown_method` leaves unclassifiable batch pending next round, like no-fallback Inbox items. On model failure / invalid result, program groups structurally by replies / quotes, topics, same-sender bursts, time gaps. Assign only supplied unfinished events; new creates events; each new message exactly once |
| `login.step` | `{step, values}` → `Prompt` | Stepwise login, empty step starts over |
| `logout` | `{}` → `{}` | |
| `options.get` | `{}` → `{options}` | Options declared (#2830): connector's app-editable JSON settings, e.g. GitHub CI failure types / muted repos. Hub exposes in `GET /api/v1/connectors` |
| `options.set` | `{options}` → `{}` | Options declared; user changed settings. Error rejects, hub does not store. Hub stores accepted values and restores **after every program start** before `options.get` |
| `inbox.settle` | `{threads}` → `{settled: {thread: reason}}` | Settle declared; which still-open hub items are ended under current rules, e.g. older rules / no longer collected thread; hub marks done |
| `machines.online` | `{machine}` → `{}` | Machines declared; user's computer just connected, allowing token-on-computer connector retry. Hub does not await reply |
| `auth.get` / `auth.set` | `{}` / `{source, machine?, machine_name?}` → `{source, machine?, scopes?, scopes_known}` | User chooses auth source, hub stores (currently GitHub only: machine gh login / device-code login). `auth.set` **after each startup** and user change; program does not persist. Scopes present only if GitHub reports token permissions |

Taking back a sent message (#5278): a connector that declares the capability `recall` is a `connector.Recaller` to the hub (`connector.RecallerOf`; one that does not is never offered a recall), and the hub asks it with `recall` above. The ledger rows it can be asked about are the ones with a `message_ref`: a write through `actions.do` (the ref in its answer), a send it recorded with `sent.record`, and the writes the node's gate forwarded for a gate of its own (`docs/extensions/connectors.md` "Sent as the user"). The capability is not something to agree to: it is shown at install and never asks again on an upgrade.

`InboxThing`: required thread (stable item key), kind, label, title, sender, preview, context, at (when the latest step happened), last (latest-step marker; a change means new activity unless `at` is older than the open item's latest step: that is an earlier step read again, neither counted nor allowed to move the item back), url, `open_url`, `reply_to`, `can_reply`, tier (action / fyi / noise, blank for model), why, done (read / replied: item finished), read, `tags: [{key, value, name}]` (repo / chat / sender user-mutable), rules, extra below, group / id / meta (“Event grouping”).

- Rules: user's original sorting instructions for source / account (extension `triage_rules`, `docs/extensions/extensions.md` “Conventional settings”). If present, **use only these**, not global hub `inbox.rules`; empty uses global. Hub defaults remain below, user overrides. Multiline allowed, max 4000 characters, truncate excess. Top-level thing field even migrated extra-bearing connectors. Old hub ignores, uses global.
- Fallback connectors put `extra.rule_tier` on every item; model sorts first, fallback admits unclassifiable items even without tier. Ordinary connector extra reads only this field. Extension connectors without fallback need working sorting model to enable (`docs/extensions/extensions.md` “Generic Inbox rules”).
- Tier is fallback for unavailable / quota-exhausted / failed / timed-out model. External-extension connectors without tier have no fallback: new items sort before admission (`docs/assistant/inbox.md` “AI sorting”); unclassifiable items **do not enter Inbox this round**, are not stored, and are returned again by next memory-only `inbox.collect` without special work. When model recovers they enter. `GET /api/v1/inbox` triage reports waiting count (same section).

**Connectors migrated out of hub** (`connector.MigratableConnectors`, currently lark, `docs/extensions/extensions.md` “App APIs”): thing may include extra source, account, `account_name`, `legacy_id`, avatar, count, `started_at`, `rule_tier`, and old lark / doc / moment block. Hub reads these only for migrated connectors, retaining legacy presentation only with one such block; `rule_tier` alone, e.g. GitHub takeover, remains generic (#2830). Legacy items have original block, no generic connector block; app opens Lark chat / document row / post. Thread derived from block: `chat:<feed>`, non-main chat:<account>:<feed>, `doc:<notice>`, `moment:<post>`, continuing original items without duplicates. New item ID uses legacy_id. Changed block with unchanged last (read position / reread document row) still updates item. `inbox.reply` forwards replies / status changes with original hub `client_id` (`assistant/inbox_migrate.go`).

Since 2026-11-04, assistant-manager process groups / sorts Inbox (`docs/assistant/inbox.md` “Who owns what”). Connectors still talk only to hub; hub forwards `inbox.collect` unchanged and proxies `inbox.reply`, `inbox.settle`, `group.*` for manager. **Protocol unchanged**.

Each messages entry: id, position, `sender_id`, `sender_name`, mine, type, text, at, media, `sender_handles: [{kind, value}]` (raw identity clues; hub stores HMAC only, `docs/assistant/knowledge.md` “Identity clues”).
For `archive.changes`, nonempty `id` is stable within the batch source and chat: live ingestion keeps its first durable version and ignores replay, including edited
bodies with that ID. Distinct IDs remain distinct even when their text is identical. `position` is pagination only. Missing IDs retain legacy append behavior
without replay protection; existing Markdown cannot reconstruct old IDs (`docs/assistant/knowledge.md` "Connector replay and recovery").

Prompt: step, `fields: [{name, label, secret?, placeholder?}]`, hint, error (user-correctable, repeat same step), done, account. Two no-field steps: `kind: "device_code"` (url, `user_code`, interval: show code for website), `kind: "qr"` (url, interval: URL is QR contents scanned by another app, e.g. WeChat #1351). Both clients poll same step without values every interval seconds until new step / done. Expired QR returns new URL in same step. Program supplies URL only; hub generates QR bitmap, replacing any supplied bitmap.

### Event grouping (group, #1680)

Group-capable connectors submit each thread's new messages as a **batch awaiting association**, not one item per thread. Hub model assigns to events (`docs/assistant/inbox.md` “Event grouping”). Only when hub initialize `hub.capabilities` includes `inbox_group` (`docs/extensions/extensions.md`); otherwise retain one-per-thread behavior.

- **Batch**: `inbox.collect` thing has `group: {batch, new}`. Stable batch ID on resubmission, new message IDs transparent to hub. Also thread, context with new text and IDs for model, rules, last; no extra needed. Hub **does not store batch in Inbox**. Once all messages written to events via `group.append`, stop returning batch. If model / fallback / an append failed, resubmit remaining messages next round. Hub associates only one batch per thread at a time; repeated batch / duplicate round submission processed once; other batches wait next round.
- **Two approaches**:
  - **Group only**: hub writes events itself without program call / meta. New ID `<connector id>:<thread>#<first message id>`; thread / kind / label / sender / preview / last / rules / extra from batch. Context includes assigned messages: row containing `id=<id>`, or last batch ID on row, belongs to that message; continuation lines follow it. Heading lines before first ID included in each new event; no IDs means whole context. Existing-event merge appends these rows, updates last / preview from batch. No meta for continues; new event still separate.
  - **Group + meta**: program tracks event metadata; hub calls `group.append` after association, below.
- **Hub owns count / start time**: hub assigns messages, so new count = assigned message count; later batches add newly assigned counts. Ignore `extra.count` because metadata may keep only recent messages and program may not know total. `started_at` read once at creation, immutable thereafter.
- **Event with meta**: `group.append` returns ordinary thing, including migrated extra Lark block / `rule_tier`, plus required Inbox ID (program generates new IDs per rules) and program-owned meta (messages, etc.). Hub stores / returns verbatim to model / program, **never parses**. Context contains event text for AI twin; at / last from latest message. Hub writes by ID; model title / summary / tier, `triaged_by: "model"`. Fallback tier from `group.fallback` `rule_tier` (existing event uses returned thing `rule_tier`), program title, reason By Rules, `triaged_by: "rules"`; this version not resubmitted to model. Unchanged last means retransmission, no store / push.
- **Old hubs**: before #1680, unknown group rejects **entire connector registration**, unknown capability, and `meta_description` prevents manifest load. Extension `engines.persona` must require hub build containing #1680 (`">=YYYYMMDDHHMM"`, build UTC time at merge), so old hubs reject install / upgrade rather than lose connector afterward. Development build 0 skips check.
- **Future capabilities**: since #1680, unknown **extension-provided** connector capabilities log once per extension / capability, remove that capability, keep running. Built-in connectors remain strict. Future capability additions no longer drop entire connector on old hubs with #1680, but already deployed pre-#1680 hubs still do.

## Program calls hub

| | Parameters → result | Notes |
| --- | --- | --- |
| `secrets.load` | `{}` → `{data}` | Connector-owned secrets (session / token), encrypted in hub DB; absent data empty |
| `secrets.save` | `{data}` → `{}` | |
| `secrets.delete` | `{}` → `{}` | |
| `credentials.gh_token` | `{machine, host?}` → `{token}` | Computer gh token (#2830), only extensions allowed `gh_login` (`docs/extensions/extensions.md` “Permissions”), otherwise permission_denied. Missing token errors `no_machine` / offline / `node_too_old` / `no_gh` / not_signed_in. Token memory only, never disk / logs / returned to anyone |
| `posts.put` | `{posts: [{post, comments?}]}` → `{statuses}` | Public feed posts to knowledge post archive below; statuses per post: new, changed (comment count), empty (seen unchanged), refused (no ID / another connector's post) |
| `posts.comments` | `{id, comment_count, comments}` → `{}` | Replace own archived post comments, record count at read time; post unchanged. Used rereading `posts.pending` |
| `posts.pending` | `{limit?}` → `{posts: [{id, comment_count, at}]}` | Changed-comment-count posts needing reread within text retention, newest first, max 500 |
| `archive.chats` | `{source?}` → `{chats: [{id, name, cursor, last_at, messages}]}` | Own source (ID or `archive_sources`) knowledge chats with upstream ID / cursor; initial archive resumes here, no duplicates / gaps |
| `sent.record` (the hub's own name, not `connector.`) | `{target, text, summary?, session_id?, machine_id?, message_ref?, setting?}` → `{}` | Keeps a send the connector made in the user's name outside the write gate in the sent-as-user ledger (#5278). Only for a connector whose manifest has `sends_as_user`, else permission_denied; `text` and `target` (an object) are required; at most 60 calls at once per extension and then one a second, else rate_limited with `retry_after`; the hub sets the row's origin and `let_through` itself (`docs/extensions/extensions.md` "Sent as the user: `sent.record`") |
| changed event | `{scope}` | Chats / items / inbox changed; hub fetches promptly. Unknown scope dropped / logged |

If extension also contributes events (`docs/extensions/extensions.md` “Events”), hub event.<name> is delivered to `serve.Listener` connector Hear(ctx,name,params) in own goroutine, no response. Without implementation, drop as before. WeChat bot uses `agent.needs_you` (`docs/extensions/wechat.md`).

### Knowledge base

`posts.*` / `archive.chats` require archive capability; disabling Add to Knowledge returns error. Touch only connector-owned content.

- **Post archive** (`docs/assistant/knowledge.md` “External posts”): post `{id, feed?, author?, author_id?, anonymous?, categories?, text, images?, reactions?, comment_count, at, mine?}`. Feed names archive heading, absent Posts. Comments `[{author?, reply_to?, text, at}]`; **present, including empty array, means comments read this time**, and hub records current count. Absent updates post only, retaining comments. Zero-comment posts automatically marked read. Connector formats “Me” / “Anonymous” in comments. Max 200 posts per call, 5000 comments each, post / each comment max 64 KB, truncated.
- User-related concerns determined by hub: corresponding post-ID chat under any connector source means related (user posts / replies / mentions, normally archived as chats).
- Hub owns indexing, text-retention cleanup, project links `kb.LinkMoments` for all post sources. Connector owns how much / often to crawl, pagination, and its cursor state.

## Command line

Connectors may give assistant a CLI (#1704; Telegram tg previously built into personad): declare cli, provide `<id>_cli` tool in unlisted so model tool list omits it.

- Logged-in connectors: before every assistant message, `assistant.prepare` clis tells node to write a PATH wrapper, `exec personad tool --cli <id> "$@"`. The default assistant uses `~/.persona/assistant-bin/<cli>`; other directories have separate command bins ([assistant.md](../assistant/assistant.md)). The node keeps wrappers and connector lists per canonical assistant directory, removing that directory's wrappers on logout; sessions using the same directory share its state. CLI can call only **logged-in connector tools** with `<id>_` prefix from its session directory's list, only from Persona-launched assistant processes.
- `personad tool --cli <id> [--format json|text] <words…>`:
  1. Call `<id>_cli {argv, stdin?}`; standalone - reads stdin. Parse only, no external service. One JSON result:
     - `{name, call: {tool, args}, render?}` selects call, render for text output;
     - `{name, exit, stdout?, stderr?}` direct help / usage / invalid-word result, exit code.
  2. Call `call.tool` as assistant named call; writes still hub connector_gate.
  3. `--format text` + render calls `<id>_cli {render, result}`, result previous tool text, → stdout to print.
- Default output one JSON line, original tool text. Failure stderr `{error, kind, retry_after?}`, text format `<name>: …`; usage mistakes too, before anything is called (no tool or connector, an unknown option, `--format` without a value, arguments that are not one JSON object: kind `usage`), while `-h` / `--help` prints the usage on stdout, exit 0. Exit codes: 0 success, 1 failure, 2 usage, 3 unavailable (node / hub unreachable, logged out), 4 rate limit with seconds `retry_after`, 5 user / `connector_gate` denial, 130 interrupted (Ctrl-C / SIGTERM before result; pending approval card withdrawn; approved executing write may have happened, verify before retry; not node / hub failure).
  Codes follow hub / node categories in `mcp.call` `error_kind` / `retry_after` (`docs/protocol/protocol.md`), not wording. Gate denial, logged-out connector, `rate_limited` state / program error carry categories.
  Only old hub / node without category uses wording: The user declined / (`connector_gate`) do not allow → 5; is not available / Persona hub error → 3; `限流中，N 秒` / `限流，N 秒` / `还要 N 秒` (“rate limited, N seconds” / “N seconds remaining”) → 4, `retry_after` N. Rate-limited status without seconds also parses state.error this way.
- `personad tool <tool> [<JSON args> | -]` calls directly, same restrictions / output.

## Errors

`error.code`: `not_initialized` (method before init), `no_capability` (undeclared capability), `unknown_method`, wrong_protocol.
`recall` answers with `not_recallable` and `recall_unknown` besides (above). `tools.call` / `actions.do` upstream rate limits return `code: rate_limited` and optional `data: {retry_after}` seconds, fractional allowed, hub rounds up. CLI exits 4 / reports `retry_after` without required message wording (#3779, optional code, no version change).
Other errors have only human-readable message, **no secrets or body text**.

## Requirements

- **Writes only in response to `actions.do`**: program may declare actions, but hub invokes `actions.do` only after `connector_gate` (phone confirmation by default). Program cannot initiate writes. The same holds for `recall`, which deletes: the hub calls it only after the user agreed to that recall. Hub truncates `actions.describe` summary with `connector.cardMost`; card shows recipient / full content. (Since 2026-11-03, #988, Telegram subprocess-only, replacing former “writes not in protocol” rule; checked with Ovis.)
- Secrets only via `secrets.*`, never local files / logs.
- Logs / errors contain no bodies, numbers, tokens.
- Status / `inbox.collect` memory-only, no network. Program owns polling loop, emits changed for new data.

## Versioning

Current 2. Changed field meaning / removal increments; optional additions do not. Hub accepts identical protocol only; first-party programs built alongside personad necessarily match.

## Conformance tests

`conformance.Check(t, c)` places Go connector on program side via `serve.Serve` / pipe pair, calls every declared capability directly and over wire, requiring byte-identical JSON. Lost fields / changed types fail. Test both legacy init / bare methods and initialize / connector.*. Process initialization also checks manifest contributions / unknown connector no_contribution. `extension/conformance` checks overall process protocol (`docs/extensions/extensions.md` “Conformance tests”). GitHub, Messages, Telegram tests all run this. `TestWireTypesAreTagged` requires explicit snake_case wire names. Third-party Go programs can wire `serve.Serve` similarly with same tests.

### Other languages: `CheckProgram`

Without Go implementation to compare, `conformance.CheckProgram(t, manifest, run, init, opts...)` (`server/internal/connector/conformance/program.go`, method checks `program_wire.go`) exercises running program from hub side. For each manifest connector, call declared capabilities in hub order, **strictly decode** each response to hub Go type: `DisallowUnknownFields` catches extra fields, nonsnake_case keys, non-RFC 3339 dates; separate key check because Go matching is case-insensitive. Every response, the program's own calls and its events are also held to `connector.schema.json` (`wire.ValidateResult` / `ValidateParams`), and so are the params the hub sends. Then enforce hub invariants:

| Method | Decode type | Additional requirements |
| --- | --- | --- |
| initialize | `plugin.InitializeResult` | ID / version match; connector contributions field-identical to manifest except capability order |
| status | `connector.Status` | Known state; ok has no error; other states except `not_configured` require error / guide |
| `tools.list`, `actions.list` | `serve.ToolsResult` | Unique <id>_-prefixed names, descriptions, `type: object` `input_schema`; tools / actions names distinct |
| `tools.call` | `serve.CallResult` | Each read tool with empty args returns result or explanatory rejection |
| `actions.describe` | `serve.DescribeResult` | Each write with empty args rejects or returns summary / `action.visible` |
| `inbox.collect` | `serve.CollectResult` | Twice: nonempty unique stable threads, declared kinds, known tier / done; unfinished has at / last; `can_reply` has `reply_to`; tags key / value |
| `thread.page` | `connector.ThreadPage` | First Inbox thread, oldest first, ≤limit; unique nonempty IDs / at; non-event non-mine has sender name; text or media; `chat.reply` own action with chat / text. If before, earlier page no duplicates / newer messages |
| `thread.media` | `connector.Media` | Once only if page has ref |
| `archive.changes` | `serve.ChangesResult` | Follow next until more false, then query final cursor again. With data next nonempty, never resets empty / stalls; no duplicate message IDs per chat; id / at / text or media (knowledge append-only, live replay dedup by source/chat/id, drops undated / empty); `sender_handles` kind / value |
| `items.list` | `serve.ItemsResult` | Kind docs / meetings / events, id / title |
| `login.step` | `connector.Prompt` | Fields, done, `device_code`, or qr; QR url / interval / step, `WithLogin` polls until done. Input step nonempty name (empty restarts); done requires account |
| logout | `{}` | Only last after `WithLogin`; status no longer ok, secrets deleted via `secrets.delete` |
| `connector.changed` event | `serve.ChangedEvent` | Own contribution, events declared, known scope |
| program `secrets.*` calls | `{contribution, data?}` | Own contribution present |

Manifest also checked: capabilities implemented by `connector/proc.Conn` plus promise-only fallback; fallback means each nongroup-batch inbox item has known `extra.rule_tier` / tier (#1899). No duplicate capabilities; kinds only with inbox; `kind_descriptions` keys declared. **Never call `actions.do`** (writes only after gate), `recall` (a recall deletes a message) or `inbox.reply` (user Send); a manifest may declare `recall`, which `CheckProgram` accepts.
`WithLogin(values)` fills fields each step, requires ok afterward; fixture backends only, never real credentials; leaked supplied secret fails. `RequireData()` rejects empty `inbox.collect` / `thread.page` / `archive.changes`, preventing empty implementations passing.

For your program: first `extension/conformance` start / stop / handshake, then fresh process `CheckProgram`; internal packages cannot be imported by other repos, so tests live here:

```go
m, _ := extension.Parse(manifestJSON)                                // persona-plugin.json
progconf.Check(t, m, progconf.Program(t, "bin/my-connector"), plugin.InitializeParams{})
conformance.CheckProgram(t, m, progconf.Program(t, "bin/my-connector"), plugin.InitializeParams{},
	conformance.WithLogin(map[string]string{"token": fixtureToken}), conformance.RequireData())
```

**Wire fixtures**: `server/internal/connector/testdata/wire/<program>/*.json` contains the program's JSON response for each method, using **synthetic data** with no real accounts, names, group names, or addresses. Examples use only `https://chat.example.com`, `user_example_a`, and `127.0.0.1`, together with a `persona-plugin.json`. `TestExampleChatWireFixtures` strictly decodes every fixture according to the table above and checks its invariants; a fixture absent from the table also fails. `TestCheckProgramCatchesDrift` feeds fixtures to a pure-Go fake program and checks that field-name, order, and cursor changes make `CheckProgram` fail. Thus Go type changes such as renames or field removals fail even when CI has no Node, reminding the program side to follow. The current set uses the neutral `examplechat` connector (Example Chat): a chat-service connector signing in with a service URL and application key, with direct-chat, group, and mention items, and sends gated by `confirm_always`. Its shapes follow a real chat-connector extension's output, but its names, tools, and wording are examples. The extension's own repository keeps fixtures of its real output and checks them with the live tests below.
`schema/extension/examples/Manifest.examplechat.json` must be byte-identical to the fixtures' `persona-plugin.json` (`TestExampleManifestFollowsTheWireFixtures`).

**Cross-repository live tests**: `TestConnectorProgramFixtureServerLive` runs any external connector that signs in with a service URL and application key (`base_url`, `app_key`) against a fixture server for that service. `PERSONA_TEST_CONNECTOR_PROGRAM` points to the connector startup script named by the extension manifest's `runtime.command`; `PERSONA_TEST_CONNECTOR_FIXTURE_SERVER` points to the fixture-server script. The test runs the server with Node, `PORT=0`, bound to loopback. Node and both environment variables are required; missing either variable uses `t.Skip`, leaving existing tests and CI unaffected:

```sh
cd server
PERSONA_TEST_CONNECTOR_PROGRAM=<extension directory>/<runtime.command> \
PERSONA_TEST_CONNECTOR_FIXTURE_SERVER=<fixture service script> \
go test ./internal/connector/conformance -run FixtureServerLive -v
```

Without a fixture server, if the program has a fake backend, set only `PERSONA_TEST_CONNECTOR_PROGRAM` and use `TestConnectorProgramLive` here (`internal/` imports are restricted to this repository). The caller sets the fake-backend environment variables, which the program inherits. Login steps receive no values; the fake backend marks the QR step scanned itself:

```sh
cd server
PERSONA_TEST_CONNECTOR_PROGRAM=<repository>/<extension>/bin/<program> go test ./internal/connector/conformance -run ConnectorProgramLive -v
```

Package `persona-plugin.json` is one level above bin. Fixture service prints **one JSON line**: `base_url`, `outbound_sends_enabled`, accounts, each key for read / events and separate send_key. Test uses `base_url` / first account key for login `base_url` / `app_key`, never send_key. Nonloopback `base_url` or `outbound_sends_enabled` not explicitly false rejects login.
Outbound sends remain disabled; synthetic accounts / test keys only, never real accounts. First `extension/conformance.Check` for lifecycle / handshake / errors, then fresh program CheckProgram.
