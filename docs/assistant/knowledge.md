# Knowledge base (`~/.persona/data`)

For Persona and the AI twin to "work in my place", they first need to know as much as I do: who is who, what each group does, where each project stands, and what concerns me.
This doc explains how Lark and email data become a knowledge base with **files as the primary store and a database as the index**, shared by Persona, the AI twin and Agents.
(Direction decided 2026-10-30: all data ultimately settles in `.persona`; files first, database for indexing; text retained 365 days, attachments 30 days.)
Since 2026-11-04 the knowledge base belongs to the manager (the assistant process `personad assistant`); since 2026-11-05 sources (connectors) also belong to the manager and feed the knowledge base in the same process (`server/internal/extmgr`, `docs/extensions/extensions.md` ("Who owns what")); see below.

## Who owns what (2026-11-04)

As part of moving hub business logic into managers (`docs/assistant/assistant.md` ("Boundary with the hub")), the entire knowledge base moved into the manager (currently there is just one, the assistant):

- **The manager runs the knowledge base** (`server/internal/assistant/kb*.go`): the engine (`server/internal/kb`: archiving, model-based compression and organization, indexing, timezone, people / groups / projects, attaching Moments posts to projects),
  its loop (every 2 minutes, or 5 seconds after a source has new data), and its state (files in the data directory, the `kb_entities` index table, `handles.key`; same layout as before, only the manager writes).
  The assistant's `kb_*` tools, knowledge base in live snapshots, and knowledge directory in the assistant directory's CLAUDE.md are local manager calls (no longer `app.call kb.*`).
- **Sources** (in the manager's `internal/extmgr` since 2026-11-05; the methods below are unchanged but now called directly in-process, without the bus; attachments also no longer have a 4 MB limit): data delivered by connectors and plugins (chat messages, posts…). The knowledge base asks sources (`app.call connectors.*`, data in and out):
  `connectors.archives` (which connectors to read now: signed in with "Include in knowledge base" on, their polling intervals, other archive names, attachment support; and those with documents / meetings / events),
  `connectors.changes` (a connector's changes since a cursor), `connectors.archive_media` (attachments for a message; when the manager is elsewhere, up to 4 MB, since one bus message must not occupy a slow link longer than a heartbeat; larger ones leave `[附件]` ("[attachment]") on the line), `connectors.items` / `connectors.doc_title` (documents, meetings, events).
  **The manager owns the cursor** (`cursors/<connector>.json`, same file as before), which advances only after writing the data. For Moments posts submitted by plugin programs (`posts.*`, `archive.chats`), the hub first checks
  whether "Include in knowledge base" is on, then forwards them unchanged to the manager (`kb.posts_put`, `kb.posts_comments`, `kb.posts_pending`, `kb.archived_chats`).
- **When the hub's own features need the knowledge base**, they ask the manager over the bus through one hub function, `kbAsk` (`hub/kb.go`; those features are also moving into the manager, becoming local calls afterwards):
  chat context for the Inbox AI twin (`kb.context`), deleting a source's data (`kb.forget`). Triage after an Agent's turn stops also moved into the assistant (`docs/assistant/agent-work.md` ("Who owns what")); the project owning its directory is read directly in the assistant, no longer via `kb.projects`.
  Events seen by the hub are one-way: new source data (connector live events, enabling "Include in knowledge base", enabling "Hand to Persona" for "Information", `kb.kick`),
  and an Agent finishing a turn (before deciding notifications, `kb.agent_turn`, the project timeline entry).
- **Models**: the knowledge base uses the manager's model configuration (`kb.compress`, `kb.project`, `kb.items`, `kb.dedupe`, `kb.moments`, defaulted by `kb.ai` in its `manager.json`),
  resolved at every call, all in one place, `assistant/kb_model.go`.
- **Where data lives**: by default the manager is a hub child process on the same machine. Its knowledge base is the data directory's `~/.persona/data` (`kb_dir` in its `manager.json` can move it), and its `handles.key` is in its own directory, `~/.persona/managers/assistant/` (copied there once from `~/.persona`, [managers.md](managers.md) ("The built-in manager's own files")).
  Storage **follows the manager's `manager.json`**: its `kb` section (`enabled`, `text_days`, `attachment_days`, `extra_dirs`, and `ai` for the models; `docs/operations/config.md` ("manager.json")). Since 2026-11-06 the hub neither reads it nor hands it over in its handshake response (`docs/protocol/protocol.md` ("Handshake")), except raw, once, to a built-in manager it did not start (`legacy_config`, [managers.md](managers.md) ("Configuration"));
  a manager that runs elsewhere reads the `manager.json` on its own machine. With `enabled: false`, the manager does not open the knowledge base and its sources provide nothing
  (`connectors.archives` is empty; other source methods and plugin posts reply "The knowledge base is off for this manager"); source deletion does not ask the knowledge base.
  When hub.json sets `assistant.external` (manager elsewhere, `docs/assistant/assistant.md` ("Processes")), the knowledge base is in **that machine's** data directory, and its `kb` section is in the `manager.json` there: the original `data/` on the hub machine stops updating.
  To continue using it, copy the entire directory, and the hub machine's `~/.persona/managers/assistant/handles.key` into the same place on that machine (or into its `~/.persona` before the manager first starts there; otherwise future identity-hint hashes will not match); `extra_dirs` refers to paths on that machine; the index table stays in shared Postgres and need not move.
  The app's knowledge API still goes to the hub, which forwards it unchanged; the interface stays the same. CLAUDE.md still calls it "the directory on the hub" (accurate when the manager is on the hub machine).
- **When it opens**: on the manager's first need (first loop iteration, tool call or request), requiring all three: the hub's `kb` settings have arrived (handshake),
  the user's timezone can be read (archives are placed in that timezone first, avoiding writes in another timezone), and the data directory lock (`data/.lock`, only one manager process writes at a time).
  Before opening: `kb_*` tools say "Not ready yet; try again later"; app APIs return `503 kb_unavailable`; plugin posts get the same error (resubmit next round); source deletion gets 503;
  Agent-finished entries wait in the manager (latest per Agent, at most 200) and are written once open. On disconnect from the hub, the manager closes the knowledge base and releases the lock,
  preventing a process the hub no longer recognizes (token replaced) from writing alongside its replacement; it reopens on reconnect. Changed hub `kb` settings (handshake after hub restart) trigger reopening with the new settings.
- **When the manager is disconnected** (restart, crash, newly started hub):
  - Archiving waits; **nothing is lost**: connectors redeliver by cursor (on return the manager reads from the last written position, reading every due connector in its first round); plugin post calls
    wait up to 30 seconds for the manager (plugins start with the hub, before the manager; manager restarts), then return `assistant_offline` if still absent; the plugin stops this round there and rereads next round;
    documents, meetings and events are reread normally next round (30 minutes).
    Agent-finished entries are **queued by the hub** (latest per Agent, at most 200) and sent when the manager connects. `kb.kick` is not queued (the first round after return reads anyway).
  - `kb_*` tools fail like the assistant's other tools; app `/api/v1/kb…` returns `503 assistant_offline`, "The assistant is temporarily unreachable; try again later";
    "Delete stored data", "Delete stored knowledge content", and `DELETE /api/v1/imessage/data` return the same 503, leaving the knowledge data undeleted (the connector / "Include in knowledge base" is already disabled; tap again);
    Inbox items are deleted normally (without waiting on the knowledge base). For a very large source deletion the hub waits up to 10 minutes.
  - The Inbox AI twin cannot start (the assistant is unreachable anyway); triage prompts carry no project name; the plugin's `/agent` cannot get chat context (503).
- **Timezone**: when opening (above), the knowledge base places archives according to the manager's stored user timezone (`timezone` in `assistant` settings; it does not open if unreadable), so restarting does not reorganize everything;
  every archive round rechecks it. If settings cannot be read (database did not respond), that round neither archives nor moves files.
- **Visible differences**: only those under "disconnected" above. The old connector interface the hub read one chat at a time (`connector.Chats`; no connector implements it since the Lark gateway path was removed)
  is no longer read; plugin posts never used it.

## Recordings (#3235)

Transcribed text from phone recordings (and watch recordings relayed through the phone) is synced by the phone via `PUT /api/v1/recordings/{id}` ([protocol.md](../protocol/protocol.md) ("Recording sync"), [recording.md](../apple/recording.md)); audio is not uploaded.

- One item per recording, keyed by the phone's UUID; each upload replaces the whole record, and a `revision` below the stored one is not written (out-of-order and replayed uploads retain only the latest). With `complete: false`, it is stored and searchable but no model is called.
- For completed transcripts (`complete: true`) whose current revision has not been summarized, each manager round selects at most 3 for the model (same purpose as chat compression, `kb.compress`, prompt `prompts/kb/recording.md`; if no model is available, stop this round and retry next round):
  produces a summary, one-line description, title (used if the user has not named it), tags and mentioned projects; projects are linked normally (a "Recording · Title" project timeline entry). At most 24k characters go to the model (keeping the beginning and end).
- Retention follows assistant setting `recording_days` (default 14 days, independent of `text_days`): each round deletes directories and index rows whose start time predates retention; tombstones are retained equally long.
- `kb_list` / `kb_get` / `kb_search` and the app's Knowledge page gain the `recordings` category; `kb_get` includes summary, bookmarks and full transcription (up to 12k characters). Recording content cannot be edited via `kb_update` (the phone is authoritative); only tags can change.

## Extra notes directories (`kb.extra_dirs`)

`kb.extra_dirs` in the manager's `manager.json` lists user-maintained notes directories on the manager's machine (the hub machine by default), for example a git repo of facts, lessons and decisions synced by its own tools.
After searching the knowledge base, `kb_search` also searches `.md` files in these directories (skipping dot-prefixed directories such as `.git`); matching paths are absolute. The assistant's CLAUDE.md lists these directories
and tells it to read their AGENTS.md / CLAUDE.md before reading or writing. Persona does not write, sync or index them.

## Why files

- Files and grep are natural for Claude Code: the assistant, AI twin and Agents in herdr need no new API; `rg keyword ~/.persona/data` searches directly.
- People can read and edit directly too (an edited `summary.md` is treated as existing context on the next compression).
- Postgres holds only the index needed for sorting and filtering (`kb_entities`: type, name, importance, relevance, tags, path, updated time); it can be rebuilt from files if lost.

## Directory layout

```
~/.persona/data/
  README.md                 # Directory guide (generated by the knowledge base), for AI
  chats/<chat id>/          # One directory per chat: Lark lark-<feedId>, mail mail-<account>-<other address>
    meta.json               # Name, type (dm / group / bot / mail), members, linked people and projects, tags, archive cursor
    messages/YYYY-MM-DD.md  # Full text, one file per day, appended to (imported history merged in); deleted after 365 days
    attachments/YYYY-MM-DD/ # Images, files; deleted after 30 days (per source: kb.attachment_days_by_source)
    summary.md              # Compressed context (AI-maintained, rolling updates)
    .receipts.json          # Live-ingestion source/chat message IDs and original times (bookkeeping, not searched)
    .ingest.json            # Pending live-ingestion recovery intent, removed after completion
  people/<person id>/       # One directory per person: Lark lark-<userId>, mail mail-<address>
    meta.json               # Name, chats, linked projects, tags
    summary.md              # Who they are, relationship to me, recent conversations with me
  projects/<slug>/          # One directory per project
    meta.json               # Name, type (short requirement / long mission), status, members, related chats / docs / meetings / Agent directories
    summary.md              # Goal, current state, next steps, risks, my role
    log.md                  # Timeline: append an entry for each update (date · source · one line)
  docs/<id>.json            # Documents / meetings / events: title, link, time, tags, linked projects
  meetings/<id>.json
  events/<id>.json
  recordings/<uuid>/        # One directory per recording (synced from phone, #3235, "Recordings" below)
    meta.json               # Start / end, duration, language, source device, bookmarks, revision, transcription complete, tags, linked projects
    segments.json           # Timestamped segments (start / end in seconds from the beginning)
    transcript.md           # One line per segment, with clock times in the user's timezone; searched by kb_search
    summary.md              # Model summary after transcription completes
  recordings/.deleted.json  # Deleted recording UUIDs (tombstones); late uploads within retention return 410
  cursors/<connector>.json  # A connector's archive cursor (bookkeeping, not searched)
  history/state/<chat id>.json # A chat's history import progress ("History import" below; bookkeeping, not searched)
```

## Tags

Every chat, person, project, document, meeting and event has three fields, generated by AI during compression and editable by people:

| Field | Values | Meaning |
| --- | --- | --- |
| `importance` | `P0` / `P1` / `P2` / `P3` | Importance: P0 critical (manager, core project, production incident), P1 important, P2 normal, P3 ignorable |
| `relevance` | 0–100 | Relevance to me: directly addressed / my responsibility → high; merely seen in a group → low |
| `tags` | String array | No preset list; AI generates 2–6 short terms, preferring existing tags (`kb_tags` returns existing tags and counts) |

A value the model writes that doesn't fit is dropped on its own, not with the rest of its answer. An `importance` other than `P0`–`P3` is ignored. A `relevance` written as a numeric string (`"80"`) or a fraction counts as that number, rounded. Anything else, including a number outside 0–100, is ignored and the existing score stays (`server/internal/kb/score.go`, #1099). Before this, a quoted score failed the chat's whole summary, a project's rewrite, a recording's title and summary, or a batch of up to 40 tagged items.

## Pipeline (manager background, `server/internal/kb`, loop in `assistant/kb.go`)

1. **Archive**: every 2 minutes (5 seconds after a Lark live event), inspect the 100 recently active chats in Lark's Messages list, reading only when the list timestamp is newer than the last archive.
   Fetch incrementally by cursor (`afterPosition`); a newly seen chat starts with its latest 100 messages. Append to that day's `messages/*.md` (dates and times in the user's timezone, see "Timezone" below).
   Download images / files / rich-text images to `attachments/<date>/` (up to 20 MB each), leaving `[附件](相对路径)` ("attachment; relative path") in the line; record Lark document links in `docs/`.
   Topic groups read only the newest topic page. Every 6 minutes, read each email account's Inbox messages with UID above the last one (40 per page, at most 5 pages per round; with multiple pages, take the newest first, then fill the middle page by page and continue next time if unfinished),
   reading at most 10 full bodies per round (summaries for the rest), into `mail-<account>-<other address>` chats. Cursors are in `cursors/mail.json` (per-account UIDs, plus `<account>#v` for UIDVALIDITY,
   `#t` for the latest archived message time, `#f` / `#h` for unfilled gaps; on UIDVALIDITY change, restart from the newest page and only take messages later than `#t`; before 2026-11-03 it was `mail-cursors.json`, used as fallback when the new file is absent).
   Connectors (`docs/plans/plan-connectors.md`, implementing `connector.Archive`) read chats with new messages "since last time" by cursor every 10 minutes,
   with cursor `cursors/<connector>.json` and chat id `<connector>-<chat>`. Older history comes in separately ("History import", step 5).
   - **GitHub**: only PRs / issues involving me (`involves:@me`, last 30 days initially), not entire repositories.
   - One PR / issue per chat, `github-<owner>-<repo>-<number>` (`kind` is `pr` / `issue`), in this order: description (starts with `【标题】`, "[Title]"); comments; reviews (`（review）批准了`, "(review) approved"); inline comments ("(file:line)…").
   - Bots are marked `(bot)`. Each entry is at most 4000 characters. Skip the round when the request quota is low.
2. **Compress**: once a chat has 50 new messages or has been quiet for 10 minutes, send the old `summary.md` + new messages (up to 24k characters, keeping the latest) to a small model,
   producing a new summary (fixed sections: what this is / progress / to-dos and unresolved issues / about me), one line, tags, mentioned projects and people; write `summary.md` and `meta.json`.
   At most 8 chats per round, most relevant first. Direct-chat / email tags propagate to the other person; a person's `summary.md` is assembled from metadata and direct-chat context without a model call.
   When the model declines the batch (a `refusal` stop reason, e.g. a safety classifier), it would decline it again: the batch is marked covered without a new summary and the chat continues with later messages.
   Any other failure puts the chat off for 10 minutes, doubled with each failure in a row up to 6 hours (`compress_failures` / `compress_retry_at` in `meta.json`, cleared on success);
   a paused model connection does not count. Before this, a chat whose batch the model refused was sent again every round, every two minutes for days.
3. **Evolution (evo)**: merge projects mentioned in compression into `projects/` (names or aliases equal after stripping punctuation and case are merged; otherwise create), append progress to `log.md`
   and mark dirty. Only aliases belonging to this project are accepted: names / aliases of other unmerged projects, single characters or two-letter abbreviations, and generic terms are rejected; at most 12 automatic aliases per project
   (#2546: previously, several projects accumulated hundreds of aliases after a week and crossed into each other). Manual aliases from `kb_update` have no limit; `remove_aliases` deletes them. If several unmerged projects match a name,
   exact names outrank aliases; ties use most recently updated, not directory order.
   Aliases collected before these rules are pruned when the knowledge base opens (`PruneProjectAliases`): another unmerged project's name or slug, the project's own name, repeats, generic ones,
   and everything past the first 12 go; names of projects merged into it (directly or through another merged project) stay and do not count. An alias two unmerged projects share stays on both (whose it is cannot be told),
   and projects whose aliases are locked (set by hand) are not touched.
   Rewrite at most 3 dirty project summaries per round (old summary + latest 40 timeline entries + summaries of up to 8 related chats). Person / chat / project associations are written into each
   `meta.json` bidirectionally. Long-term projects (`long`) never revert to short-term.
4. **Documents / meetings / events**: every 30 minutes read Lark calendar (yesterday through 8 days ahead, busy events) and the latest 30 meetings, fill titles for 10 documents (first line of Markdown export),
   and send new or changed items to the model in batches of 40 for tags and project links.
5. **History import**: the live archive only reads forward from each connector's cursor, so a chat's messages from before it began come in through a separate import
   ("History import" below). The old backfill (`Engine.Backfill`, a few pages backwards per round for the most relevant chats) no longer ran once the knowledge base moved into
   the manager and has been removed; `oldest` / `backfill_done` in old `meta.json` files are still read and otherwise ignored.
5b. **Moments** (2026-11-01, provided by the Lark plugin, formerly hub kb_moments.go): my posts and posts involving me in notifications each become `moments-<post id>` chats, with the body as the first message
   and comments afterwards (incremental by time), compressed, tagged and linked normally. All comments are read initially, so no backfill.
5c. **All Moments** (2026-11-01, provided by the Lark plugin, formerly hub kb_moments_crawl.go; `kb/moments.go`, off by default): every 30 minutes page through each tab (recommended + categories),
   storing one post per `moments/posts/<id>.md` (body + all comments) and `<id>.json` (author, category, likes, comment count, whether it involves me),
   one line per post in `moments/index.jsonl`, latest round stats in `moments/stats.json`. **Archive only, no model calls**; posts involving me also remain normal `chats/moments-<id>` chats.
   - The API sorts by popularity, not time. Each round reads the top `head_pages` (3 × 50) of each tab for new posts, stopping on a page with neither new posts nor changed comment counts;
     then reads `pages_per_tab` (10) from the previous stopping point. At the bottom, mark the tab finished (`moments/tabs.json`; invalid page tokens restart from the top).
     A post counts only on its first sighting in a round. This approaches full coverage over time but does not guarantee no omissions.
   - First round measured (2026-11-01): 12 tabs, 114 pages, 190 requests, 3 minutes 24 seconds, 5073 posts, 9.2 MB (comments read for only 76 posts).
   - Comments: reread only if the count changed (or never read), new posts first, at most `comments_per_round` (150) posts per round (posts without comments consume no slots); remaining ones continue next round. Requests are spaced by `delay_ms` (300).
     Stop the round on rate limiting (429 / rate).
   - Search (`kb_search`) reads these posts too, but after all other content, never displacing chats or projects. Expire posts by `text_days`.
   - Switch: the Lark extension's `moments_crawl` setting (`docs/plans/plan-extensions.md`); the old `"kb": {"moments": {"enabled": true, "interval_minutes": 30, "head_pages": 3, "pages_per_tab": 10, "comments_per_round": 150, "delay_ms": 300, "max_days": 365}}` in `hub.json` is no longer read.
     It reads company-wide Moments with your account, so it is off by default.
   - **External posts** (#1444): post archiving is generic; any connector declaring `archive` can write through connector protocol `posts.put` (`docs/protocol/connector-protocol.md` ("Knowledge base")),
     with `posts.pending` listing posts whose comments need rereading (submit through `posts.comments`). Posts record source (`source`) and feed name (`feed`, used in archive titles); other connectors cannot overwrite them.
     The archive remains under `moments/` (`GET /api/v1/kb/moments/{id}` unchanged). Indexing, daily text-retention cleanup and project linking ("Project Moments" below) belong to the knowledge base (manager); the hub only forwards plugin submissions.
     Once the plugin takes over the Lark connector, hub crawling stops and the plugin crawls (paging state in its own data directory; on first takeover it pages from the top, with existing posts merely "seen");
     posts involving me are archived normally by the plugin into `moments-<id>` chats (manifest `archive_sources: ["moments"]`), initially reading each post's knowledge-base position through `archive.chats` before continuing.
   - Only the last year (requested 2026-11-01, to avoid overloading upstream): posts before `max_days` (default 365, or `kb.text_days` if shorter) are not stored
     and their comments are not read. A full page older than a year marks the tab `done`; subsequent rounds only read its head pages for new posts. The feed is popularity-ranked, not strictly chronological,
     so stop only when the **whole page** is old; mixed pages continue normally. Clean archives older than `max_days` once daily.
6. **Cleanup**: once daily, delete `messages/*.md` older than 365 days and `attachments/*` older than 30 days (days configured in the `kb` section of the manager's `manager.json`;
   attachments by each chat's source when `kb.attachment_days_by_source` names it, `Engine.AttachmentDaysFor`).
   Messages outside text retention are never written: initial chat archives, initial email ingestion and imported history all stop at midnight on the earliest retained day (`kb.Engine.textCutoff`,
   using the same `text_days` as cleanup's `Engine.Prune`),
   otherwise another initially archived chat writes expired daily files again after cleanup (observed in production 2026-11-02: 47 returned 6 seconds after deletion).

### Connector replay and recovery (#1343)

Live connector batches go through `Engine.Ingest`. A nonempty `SourceMessage.ID` is a stable identity within the exact source and chat, recorded in that chat's
`.receipts.json`. `Position` is a pagination token, not an identity. Repeated or reordered IDs add no line and change neither `messages` nor `pending`; distinct IDs
with identical timestamp, sender and text remain distinct. The archive is append-only: the first durable version of an ID wins, so redelivering an edited source
message does not update its old text or attachments. Sources that need to archive revisions must give those revisions distinct IDs.

Ingestion installs `.ingest.json` before replacing any affected day file. It holds complete intended day contents (preserving all original bytes), receipts and chat
metadata, including both counters. Each file is replaced through a temporary file, flushed and renamed. Recovery repeats those replacements and removes the intent
only after all succeed. A writable `NewStore` finishes pending intents before returning; `Ingest`, history, compression and archive metadata updates also recover
before reading counters. Until recovery succeeds, other writes to that chat refuse to overwrite the pending snapshots, timezone conversion waits, and cleanup
keeps the files. A read-only open refuses an unfinished intent because it cannot repair it. Remove the filesystem obstruction and retry the operation or reopen
the writable store; retain the intent for recovery. The directory has one owning writer, with concurrent callers serialized by the Store's locks. Operations needing
both persistence locks acquire metadata before message files, matching project commit's frozen-input check when it reads the timeline.

The connector checkpoint advances only after every batch on its page succeeds, using atomic replacement so a failed replacement preserves the previous cursor.
A failed later batch, an interrupted ingestion, or failure between ingestion and checkpoint can therefore replay a page without adding any already receipted ID.
This transaction covers archive text, identity and chat metadata; people/document indexing and change notifications remain derived, best-effort work. A replay
refreshes the chat's database index from its durable metadata. Compression compares both the latest timestamp and pending count before clearing work or advancing
its coverage boundary; distinct IDs arriving during a model call can share the same timestamp or minute. A refusal uses the same snapshot gate: it settles an
unchanged batch without a summary, but leaves concurrent work pending and eligible without adding a refusal cooldown. Ingesting a new ID in a previously
settled minute moves coverage back to that minute so later processing can read it. Other model failures retain their backoff; paused or cancelled calls do not
settle pending work.

New attachment filenames use a full SHA-256 digest of the raw message ID, plus the attachment index and bounded extension, under `attachments/<day>/messages/`.
The separate subdirectory also protects old filenames that happen to resemble the new hashes.
This avoids the older sanitized-name collisions from punctuation, case-insensitive filesystems and truncated long IDs. Distinct raw IDs cannot overwrite each
other's saved attachment through those filename collisions. Missing IDs use a fresh random filename per attachment, preserving append behavior without inventing
a durable source identity. Existing filenames and Markdown links stay untouched; a known receipted ID skips downloads on replay, including after restart. The
first newly receipted delivery from a legacy unreceipted archive may create a new hashed file alongside its old file; it does not overwrite or relink that old file.
Attachment retention still deletes the same day directories.

Compatibility is explicit: messages without IDs keep the previous append behavior, and a retry may append them again; a `Position` or text hash cannot safely
invent an identity. Existing Markdown has no IDs to reconstruct. The first newly receipted delivery can therefore repeat an older unreceipted line with matching
text. Direct `AppendMessages` writes and imported history keep their existing behavior and do not reconstruct live receipts; history's formatted-text matching
remains described below. No public message wire fields or Markdown line format change. Receipts expire with their original text day during pruning (original
instants keep them correct across timezone changes). Pruning does not decrement `messages` or `pending`, matching existing behavior. Forgetting a source removes
its entire chat directories, including receipts and any completed recovery state, so importing it again is allowed.

### History import

A chat source that can read a conversation backwards hands its older history to the knowledge base page by page (`server/internal/kb/history.go`). The driver that picks the chats,
paces the reads and stops on errors (`kb.history` in `manager.json`, off by default) and its assistant tools come in a follow-up change; this part is how a page is written and
how far each chat got is kept.

- **Writing** (`Engine.WriteHistory`): messages in any order go through the same line format as the live archive (the owner as `我`, an unknown sender as `?`), then
  `Store.MergeMessages` puts each into the day file it belongs to, in time order. A day file it touches is rewritten atomically, and every line it held stays byte for byte and in
  its order (headers, blank lines, hand edits, lines out of order or doubled); new lines go in front of the first existing message of a later minute. Day files carry no message
  id, so a duplicate is the same minute, sender and text, where an attachment counts the same however it was written (a saved link, `[附件]` or the placeholders below):
  the boundary with the live archive and a page read twice after a restart add nothing. A property test over thousands of
  random files and a fuzz target check that no existing line changes or goes missing.
- **Archive only**: imported lines are searchable and counted in `messages`, and their senders are noted as people, but they add nothing to `pending` (no compression, tagging
  or project linking), never move the live archive's `cursor` or `last_at`, note no documents and reach neither the Inbox nor reminders. Messages past `text_days` are skipped.
  `meta.json` records `history_mode` (`full` / `anchor`).
- **Attachments**: only images are downloaded (rich-text images included), and only within the source's attachment days; video, files and voice become `[视频]`, `[文件]`,
  `[语音]` ("video", "file", "voice"), anything else `[附件]`. A source whose history comes with its images needs `kb.attachment_days_by_source` set first, or old images are
  neither downloaded nor kept.
- **Anchor mode** (`AnchorFilter`): for a group read backwards, keep each anchor (the owner's own message, a mention of the owner; the caller decides) with the 50 messages on
  either side; overlapping windows merge, everything else is counted and dropped. Each gap next to a kept window becomes one line `[……此处省略 N 条]` ("N messages left out
  here", sender `·`), stamped with the oldest message it stands for so it sorts between the windows. The filter holds at most 50 messages, and its state is saved with the
  chat's progress, so an import resumes mid-window. Sources that can search for anchors build the windows by position instead and write the gaps with `OmissionLine`.
- **Progress** (`Store.HistoryState` / `PutHistoryState`): one `history/state/<chat id>.json` per chat (mode, cursor, read and written counts, state `running` / `done` /
  `paused` / `error` / `unreadable`, last error, the anchor filter's state), each write atomic. The contract the driver follows: it saves the state after every page (a crash
  between writing a page and saving its state rereads the page, which adds nothing); a temporary failure (pacing, timeout) is recorded as `paused`, one that needs a person
  (login expired, refused) as `error`, and a conversation the account left or that was dissolved as `unreadable`: skipped for good, not a failure. Search, lists and cleanup
  ignore `history/`; forgetting a source removes its chats' states.
- Page sizes are not exact (around a position a source may return more than asked for), so readers go on from the returned cursor or position, never from a count.

### Timezone (2026-11-01)

Archive dates and times (daily files, each line's "- 15:04", timeline "- 2006-01-02 15:04", Moments timestamps) are in the user's local time. The user's timezone is the one reported by the phone to assistant settings
(since 2026-11-04 the manager reads its own settings, falling back to its machine's timezone if never reported; previously `Hub.userLocation`). Before, writes used the hub machine's timezone; with the hub in UTC and the user in UTC+8:

- Messages before 8 AM went into the previous day;
- Every line was 8 hours early, and AI read the wrong time too.

Files record clock times, not timezones, so each file declares its timezone and all data moves once on a timezone change (`kb/zone.go`):

- **Chats:** `messages/.zone` records the timezone name. Without this file, the data predates timezone tracking and was written in the hub's timezone.
  - `Store.Rezone` converts each line to an instant using the old timezone, then regroups by day and rewrites clock times in the new one.
  - Write first to `messages.rezone/`, then swap with `messages/`, so each chat is entirely new or entirely old.
  - Interrupted work resumes next time: before the swap, original files remain untouched; after it, only `messages.old/` needs deletion. The same data is never converted twice.
- **Timelines:** `log.md`'s heading includes the timezone (`# 时间线 · Asia/Shanghai`, "Timeline"; bare `# 时间线` means the hub's timezone). Rewrite the whole file once.
- **When to move:** call `Rezone` when the manager starts (before connecting to the hub, `Service.StartKB`) and before each archive round; do not move if the user timezone cannot be read. Once all data is moved, the same timezone does not trigger another traversal.
  If the phone changes timezone (travel), the next round moves everything to it.
- **Reads and writes use the same timezone:**
  - Before reading or writing a chat, ensure it is in the current timezone;
  - `Since` (messages since the last compression) converts each line back to an instant in the current timezone before comparing with the starting point, so date boundaries neither omit nor duplicate messages.
- **Effect on old data:** after the first deployment, every chat's daily files are regrouped in the user's timezone.
  - Lines at UTC 0–16 (Beijing 8–24) remain on the same day; UTC 16–24 (Beijing 0–8 next day) move to the next;
  - Clock times increase by 8 hours;
  - Timelines likewise.
  - Attachment directories `attachments/<date>/` stay put (messages link by path), but cleanup dates inferred from directory names may differ by up to a day.
  - Existing Moments Markdown is not rewritten; the next comment refresh writes in the new timezone.
  - Times AI wrote into compressed profiles and project summaries are prose and unchanged.

## Identity hints (2026-11-03, connector v2 · V4, #687)

The same person in Lark, email, Messages, Telegram and GitHub has separate records (`people/<source>-<id>`). To recognize them as one person later, connectors attach sender hints to messages
(`kb.SourceMessage.SenderHandles`, in memory only): email address, Messages email or phone number, GitHub login, Telegram user, Lark user id.

- After normalization (lowercase emails, phone numbers reduced to `+` and digits), the knowledge base uses the hub key for HMAC-SHA256, stores `<kind>:<32 hex digits>`, append-only,
  in a separate `handles.json` under the person's directory (`kb/handles.go`). Raw numbers and addresses never touch disk.
- **Hidden from the model**: absent from `meta.json`; `kb_get`, search and prompts only read `meta.json` / `summary.md` / `log.md`, never `handles.json` (guarded by tests).
- Key: the manager's own `handles.key` (the built-in manager's in `~/.persona/managers/assistant/`, copied there once from `~/.persona`, [managers.md](managers.md) ("The built-in manager's own files")), on the manager's machine (hub machine by default), 32 random bytes, 0600, generated on first use; never in the database or logs. Losing it only means future hashes no longer match.
- The same email in email and Messages gets the same hash. **Merging records into one person is not part of this step**: a separate future design must address splitting wrong merges and who confirms them.

## Project deduplication

The same work often has different names in different groups. Previously the model only saw project names during compression, so each group grew its own project (2026-11-01: "Codex integration proposal" and
"multi-window single agent support", 10 people overlapping, no shared words in the names). Now prevent this from both ends (`kb/dedupe.go`):

- **At the source**: "known projects" during compression includes each project's one-line description (`kb/known_projects`); prompts require recognizing the same work by that description, using its original name,
  and adding this group's name to aliases.
- **The shown form is not a name** (#5824): known projects are listed as 「名字（别名、…）：一句话」 and models sometimes copied the whole line as the name, so lookups
  found nothing and a new project 「X（a、b）」 appeared; dedupe then merged the real X into it, and the copy's name was shown and copied again (2026-11-06: about 15 such projects in a day).
  The compression prompt now says the name is only the name; `FindProject` retries without the one line and trailing bracket groups (not when what is left is too generic, like 「无」);
  a new project drops bracket groups that list aliases; dedupe never asks about a project and one named in its shown form.
- **Periodic merging**: every 30 minutes, find pairs among active projects: shared people are at least 30% of the smaller side and at least 3, shared chats / documents,
  or at least two matching non-generic name terms. Rank by score, ask the model about at most 4 pairs per round (`kb/dedupe`: both names, aliases, one-line descriptions, goals, profiles and source groups),
  "when unsure, treat as different". Even if the model says they are the same, merge only with `sure: true` and no uncertainty words such as `存疑` / `不确定` ("doubtful" / "uncertain") in the reason; otherwise record them as different.
  Judged pairs go in `projects/.dedupe.json`; different pairs are not asked again for 14 days, pairs the user unmerged (`split`) never.
- **Merge** (`MergeProjects`): the survivor absorbs the other's name and aliases (as aliases; reject collisions with a third unmerged project; the merged project's name is always accepted, possibly one beyond 12, while its existing aliases only fill up to 12), people, chats, documents, code directories, keywords and Moments;
  timelines interleave by time and add "Merged project …"; references from chats, people and documents change to the survivor; mark it dirty for profile rewriting next round.
  The merged project remains on disk (`status: merged`, `merged_into`) but is excluded from lists, prompts and Agent directory ownership; looking up the old name finds the survivor;
  opening it in the app shows "Merged" and a link to the survivor. The manually tagged (`locked`) side always survives.
- **Manual**: send `merge_into: <other project>` through `kb_update` / `PUT /api/v1/kb/projects/<id>` (other fields do not apply simultaneously).
- **Unmerge** (`kb/unmerge.go`): send `unmerge: true` for the merged project (slug or name) through `kb_update` / `PUT /api/v1/kb/projects/<id>` (other fields do not apply simultaneously;
  not part of the extension protocol). The merge records on the merged project what it gave the survivor (`merge`: aliases, people, chats, items, code directories, keywords, Moments posts,
  its own Moments posts the merged project's newer ones pushed past the cap, the chats / people / items it moved from the merged project, and the survivor's pinned facts and locks before a manual merge).
  Unmerging takes exactly that back, points the moved chats, people and items
  at the merged project again, restores its status, removes its lines from the survivor's timeline (its own timeline keeps them), marks both dirty for profile rewriting,
  and records the pair as `split` so dedupe never merges it again (a verdict a running dedupe pass reaches afterwards does not replace it). Pinned facts and locks changed since the merge stay. A chain comes apart from its last merge back.
  When several projects went into the same survivor, what a project merged there later (and still merged) brought too stays on the survivor (and keeps naming it), and goes on that project's record, so unmerging it later takes it off.
  Moments posts pushed past the cap come back as far as the room allows, the newest first; those that still do not fit wait on the records of the projects still merged there.
  Merges made before the record (no `merge`) cannot tell which shared people, chats and items the survivor already had, and dedupe pairs projects by exactly those:
  they stay on the survivor as well (take them off with `remove_people` / `remove_chats`), the merged project's chats, people and items name it again besides the survivor,
  and only the merged project's name and aliases leave the survivor.

## Project Moments

Rather than sending every full Moments archive post (5c above) to the model, projects find relevant posts (`kb/moments_link.go`):

- Project profile length follows project size (limits relaxed as requested 2026-11-04, #1001): short-term projects up to 1500 characters, long-term directions up to 2500 (`prompts/kb/project.md`);
  existing overlong profiles are shortened on their next rewrite. `kb.project` allows 8000 output tokens, doubling and retrying once on truncation (#1000), with room for "body + thinking".
- While rewriting the profile, the model also produces `keywords` (3–8 terms used in public discussion: product names, features, public names; not internal codenames or overly generic terms).
  After keywords were added, active projects touched within 60 days but lacking keywords are marked dirty at manager startup and filled over a few rounds.
- Every 30 minutes, for the 3 most relevant projects with keywords: rank posts from the last year by keyword hits, comment count and recency; take the top 20 unjudged posts
  with the project profile and ask the model (`kb/moments_link`) whether each discusses this work and for one line explaining the relationship. Keywords are noisy (165 archived posts mention
  codex, almost none related to the "Codex integration proposal"), so model checking is essential. Ignore ASCII keywords shorter than 3 characters or Chinese keywords shorter than 2 ("AB" matches half the posts).
- Judged posts go into `projects/<slug>/moments-judged.json` and are not asked again; newly crawled posts become candidates next round. Relevant posts go into `meta.json`'s
  `moments` (up to 50, newest first); those within two weeks also add a timeline entry ("Moments · Author") and mark the project dirty. Rewrites include the 5 most-commented posts
  (the prompt requires user feedback and outside voices in the current state or risks).
- Used in project details' **Related Moments** (author, time, comment count, relationship; open full text and comments through `GET /api/v1/kb/moments/{id}`),
  and listed when `kb_get` fetches a project.

Posts involving me (5b above) remain normal chats: first compression reads the entire post (within a year before ingestion); previously it only read the preceding 30 days, leaving most older posts never compressed. Posts older than a year no longer create chats.

## Projects that are not projects

The model may mistake product names in notification emails (each App Store app, subscription services) for projects. So only chats **with real people talking** can create projects
(`kb.HasPeople`: not bot chats; email either has a reply from me or a sender unlike automated noreply / newsletter / support addresses),
and prompts (`server/internal/prompts/kb/`, `docs/assistant/prompts.md`) explicitly exclude notifications, marketing and billing. At startup the manager marks projects sourced only from these chats and never manually tagged as `ignored`:
no more updates, snapshots or prompts; the app groups them under **Not projects**. Changing their status in tags restores them.

## Who uses it

- **Persona (main chat and other chats)**: CLAUDE.md describes the layout and requires "keep context flowing"; MCP tools `kb_search` (full text) / `kb_list` (sorted list) /
  `kb_get` (profile + last 7 days' messages) / `kb_update` (create projects, edit profiles, tag, link, add timeline entries; manual tags lock and are not overwritten by compression);
  live snapshots include the 6 most important projects and 8 chats. These four tools belong to the assistant (parameter defaults, limits and model-facing copy in `assistant/tools_apps.go`),
  and since 2026-11-04 so does the knowledge base, so they call the engine directly (formerly bus calls to hub `kb.search` / `kb.list` / `kb.get` / `kb.update`).
- **Main chat**: hub settings `assistant.main` stores its session id (set by `POST /api/v1/assistant/conversations` with `main: true`,
  cleared by `PUT /api/v1/assistant {"main":"-"}`). When requesting a snapshot per message, node passes the session id to `assistant.context`; main-chat snapshots start with "This is the main chat".
- **AI twin**: Agent prompts end with this chat's profile and its first 3 projects (`<persona-context>`), requiring new conclusions to be written back to the project timeline through `kb_update`.
- **Scheduled tasks** (morning briefing, evening review and user-created): every run's prompt ends with instructions to review important projects first, report by project and record new progress.
- **Agent finishes**: after an Agent in herdr ends a turn (before notification decisions), its final words go into the timeline of the project owning its directory
  ("Agent · Title · Machine", once per Agent within 30 minutes), triggering a summary rewrite. Once the hub decides the turn stopped, it sends `kb.agent_turn` to the manager,
  queuing it while disconnected (see "Who owns what" above).
- **Task finishes** (#2820): when a task dispatched by the main chat completes (automatically or by user action), the assistant extracts a one-line conclusion and links from the Agent's final reply and adds them to the project timeline for the working directory ("To-do · Title";
  if no registered code directory matches, use the directory name to find a project without registered directories). This bypasses the 30-minute limit above; the next Agent started in this project's directory receives the project description and latest 5 "To-do ·" conclusions as
  `<persona-context>` before its first message. See `docs/assistant/todos.md` ("Record conclusions on completion").
- **Agents**: project `dirs` stores `<machine>:<path>`; `GET /api/v1/kb/projects/<slug>` returns Agents running there. Starting an Agent from a project in the app,
  or asking Persona, includes the project profile as context.

## Configuration

The manager's `manager.json` (`~/.persona/managers/assistant/manager.json` for the built-in manager, `docs/operations/config.md` ("manager.json")):

```json
"kb": {
  "enabled": true,
  "text_days": 365,
  "attachment_days": 30,
  "attachment_days_by_source": { "<source>": 3650 },
  "ai": { "base_url": "<gateway address>", "api_key_file": "<key file>", "model": "<model>" }
}
```

If `kb.ai` is omitted, use `inbox.triage_ai`, then `ai`. `attachment_days_by_source` overrides `attachment_days` for the sources it names ("History import" above). `enabled: false` disables everything. Only the manager reads this section ("Who owns what" above; the hub neither reads it nor hands it over), and its model configuration takes `kb.ai` as the default for the `kb.*` purposes.

<a id="扩展整理知识库1908"></a>

## Extensions organizing the knowledge base (#1908)

Extensions can call `kb.list`, `kb.recent`, `kb.get`, `kb.day`, `kb.search`, `kb.update`, `kb.owner` directly over RPC without a full hub token.
The hub checks the extension's actually granted `kb.read` / `kb.write`, then forwards to the manager owning the knowledge base, including when it runs on another machine.
`kb.write` does not include read permission. Legacy `hub_api` HTTP and RPC remain available, but internal calls such as `kb.forget` and `kb.posts_*` are not exposed to extensions.
For parameters, pagination, errors and event subscriptions, see [Extensions](../extensions/extensions.md#知识库-rpc1908).

### Corrections, locks and unlinking

Metadata of all six entity types can include `pinned` (user-confirmed facts) and `locked_fields` (protected fields). `kb.update`, the original HTTP PUT and assistant `kb_update` share write logic:

- Omitted or `null` `tags` keeps the current value; explicit `[]` clears; a nonempty array replaces. After explicit clearing, metadata retains `tags: []`, allowing clients to replace cached tags with the latest empty value.
- Only explicit updates change `pinned`; an empty string clears it. Compression and project rewrites receive it as read-only input; the model cannot overwrite stored facts. Person corrections also feed related chat compression.
- Manual writes lock fields touched by default, including person `note`, summaries, project descriptions and associations. Background writes reread current protected fields at write time so corrections made during a model call cannot be overwritten by stale objects.
- `lock: false` adds no locks in this update, letting extensions choose continued background maintenance; it does not remove existing locks. Legacy `locked` tag protection remains effective.
- `remove_people`, `remove_chats`, `remove_projects`, `remove_aliases` remove associations or project aliases, not entities. A batch adds first then removes, so removal wins; existing reverse associations are removed too. `remove_aliases` applies to projects.
- Automatic deduplication skips manually protected projects and associations. Explicit merging retains existing merge precedence, updates locked reverse associations and concatenates both pinned values; the source remains a merged record with its original protected metadata readable.

For example, remove a project member while recording a confirmed fact:

```json
{"kind":"projects","id":"example","remove_people":["person-a"],"pinned":"This project is maintained jointly by the team."}
```

This is not a cross-file transaction and adds neither `If-Match` nor a revision log. Write failures return errors; do not automatically retry a failure as a new user confirmation.

### Who holds a field (#6100)

Locks used to say only "a person wrote this"; an extension's write counted as the user's, so a curation extension could overwrite what the user corrected.
Now every locked field has a holder, and writes follow **the user > an extension > the built-in pipeline** (`kb/attribution.go`):

- **Holder**: a locked field (in `locked_fields`, the legacy `locked` for importance / relevance / tags, or a non-empty `pinned`) is the user's unless the entity's `provenance` names an extension for it:
  `"provenance": {"summary": {"by": "ext:<extension id>", "at": "…", "evidence": [{"chat": "<chat id>", "day": "2026-01-02", "n": 12}]}}`. A field nobody holds is free.
- **The built-in pipeline** writes only free fields (as before: background writes keep `locked_fields`, `pinned` and now `provenance` as they are on disk).
- **An extension** (`kb.update` over RPC, attributed by the manager; the extension cannot name itself) may write free fields and fields it already holds. A field the user or another extension holds, on the entity or on the other side of a link it adds or removes,
  refuses the **whole** update with `conflict` and the holders; nothing is written. The fields it writes with a lock (the default) become its own, with its `evidence`. With `lock: false` nothing is held: the value is a suggestion the pipeline may replace.
  Its importance / relevance are held through `locked_fields`, never the user's legacy `locked`. `unlock` hands fields it holds back to the pipeline (unlocking `pinned` clears it). `merge_into` is refused when anything on the project folded away is held by someone else.
- **The user** (app, assistant `kb_update`, HTTP `PUT`) writes anything, as before, and takes over every locked field touched: its provenance is dropped, so the extension can no longer change it.
- The answer carries `before`: each written field's previous value (`summary` the old text), so the extension can keep its own undo log; undo is another `kb.update` with those values, plus `unlock` when the field was free before.
- **Without extensions nothing changes**: no write adds `provenance`; the user's writes only remove entries that exist. The legacy `hub_api` HTTP `PUT` from an extension token still counts as the user.

### Built-in organization and extensions side by side (#6100)

An extension that curates (a daily and weekly pass: triage, sourced summaries, deterministic checks, proposals for review, merges and splits) runs **on top of** the built-in pipeline, not instead of it:

- **Raw data stays with the built-in pipeline.** Archiving, the per-day message files, compression (`compress`), documents / meetings / events (`items`), project evolution and timelines (`project_commit`), deduplication (`dedupe`) keep running every round, installed extension or not.
  Every source (Lark, mail, WeChat, iMessage, GitHub, calendar, documents, recordings…) lands in the same entities, so an extension reads all of them the same way: `kb.recent {since, until}` is one feed of what came in across kinds and sources (chats carry `source`), `kb.day` a chat's text with line numbers, `kb.get` anything else.
  Extensions never write raw messages.
- **Extension results go in through `kb.update`** (summaries, notes, goals, tags, pinned facts, links and their removal, aliases, timeline lines with `source`, merges), with evidence pointing at the day file and line they rest on.
- **The pipeline yields per field, by holder, not by a global switch.** A summary the extension holds is not rewritten by compression; held links are not restored; deduplication skips protected projects. Everything the extension doesn't hold keeps being maintained.
  The extension decides how much it takes over (it can write with `lock: false` to suggest, or hand fields back with `unlock`); the user decides the boundary: granting `kb.read` only (a shadow run that writes nothing), or also `kb.write`, the extension's own settings (sources, kinds, what it may apply without asking), and any edit of their own, which wins.
- **Order**: the pipeline runs continuously (every 2 minutes, 5 seconds after new data); a curation extension runs its daily pass after the day has closed in the user's time zone (reading `kb.recent` for that day) and its weekly pass on top of the daily ones; between passes it may follow `kb.changed`.
  A field the extension wrote then holds until the user edits it or the extension unlocks it.
- **Conflicts**: the user wins over any extension, an extension over the pipeline; between two extensions the first holder keeps the field. A `conflict` answer is a question for the user, not an error to retry: the extension puts it in its review queue (proposals, review and undo are the extension's, kept in its own data directory).
- **Scheduling** is the extension's own: `on_startup` with its own timer, remembering the last pass in its data directory and catching up after a restart. **Model calls** for extensions are #6102.

### Owner information and change notifications

`kb.owner` returns `{name, aliases, handles:[{kind,value}]}`. Name comes from `assistant.owner`; aliases are edited by fully authorized users through existing
`PUT /api/v1/assistant` field `owner_aliases` (omitted keeps, `[]` clears). An extension with only `kb.write` cannot change the global owner identity.
Handles combine configured email addresses whose knowledge archiving remains enabled and sender identities explicitly marked `Mine` in later archives; they are neither reverse-engineered from other people's identity hashes nor guessed from historical "me" messages.

Ordinary people's raw identity hints still never touch disk. Owner identity uses a separate private encrypted record isolated from ordinary entities, full-text search and profile snapshots; its key comes from the manager's
`handles.key`, using a distinct encryption purpose. Without the key, observed identities stay only in memory and must be synced again after restart. Deleting a source clears that source's observations;
an identity jointly confirmed by other sources remains. Disabling an email archive source also stops returning its live account identity.
Corrupt ciphertext, a mismatched key, or existing ciphertext with a missing key causes owner reads and identity cleanup to fail, while normal knowledge-base functions remain available; new observations never overwrite an unreadable record.

Extensions declaring `events` subscriptions to `kb.changed`, `kb.forgotten` and granted `kb.read` receive body-free notifications:
`{kind,id,what}` (`what`: `metadata`, `summary`, `messages`, `log`) or `{source}`. Manual and background writes share the notification path;
forgotten is sent only after source files and index deletion succeed. Notifications are best-effort, not a durable change log; disconnects or congestion can lose them, so extensions should list/get again at startup to reconcile state.

## App API

Since 2026-11-04 the hub forwards these APIs unchanged to the manager (`assistantwire.Routes`, `assistant/kb_routes.go`), preserving paths and fields; disconnected managers return `503 assistant_offline`,
and a disabled knowledge base (`kb.enabled: false`) returns 503 as before.

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/kb/{kind}?tag=` | Sorted list (`items`) and common tags (`tags`); kind is projects / chats / people / docs / meetings / events / recordings |
| `GET /api/v1/kb/{kind}/{id}` | `detail` (meta, summary, project timeline, last 7 days' chat messages); projects also include `agents` |
| `PUT /api/v1/kb/{kind}/{id}` | Edit (fields as in `kb_update`); create a project if absent |
| `GET /api/v1/kb-search?q=&messages=0` | Full-text search (by line, for older apps; assistant `kb_search` does the same search through bus `kb.search`) |
| `GET /api/v1/kb-search?q=&results=1` | App search: grouped and sorted by entry (`results`: `entry`, `count`, first 3 `hits` lines, `in_name`), see "Search" below |
| `GET /api/v1/kb/{kind}/{id}?q=` | Open from search: adds `matches` (every hit in this entry; messages include `day` and two neighboring messages each in `before` / `after`), up to 40 |
| `GET /api/v1/kb/moments/{id}` | One archived Moments post (`post`, `markdown`: body + all comments), opened from a project's Related Moments |
| `GET /api/v1/kb/chats/{id}/context` | What the knowledge base knows about a chat (`chat_id`, `chat_name`, `kind`, `summary`, first three projects as `projects: [{name, summary}]`), same context the hub puts in prompts; read-only, used by plugin /agent (#1484) |
| `GET /api/v1/kb/chats/{id}/days/{day}` | All messages for a chat day (`messages[].n` is the file line number), and neighboring days with messages (`prev` / `next`) |

## Search

Reworked after feedback on 2026-10-31: "I find results but can't locate them after opening; going back errors too" (`kb/find.go`):

- Results are entries, not lines: find every line containing all keywords (profiles, timelines, records and all chats, ~420k lines / 25 MB; first check the whole file for all keywords, with 8 concurrent readers, ~150 ms on real data),
  group by entry, name matches first, then importance, relevance, hit count and updated time. Previously the first 60 lines in directory traversal order were often filled by a few group profiles.
- Matching ignores case and Unicode normalization (`fold`: lowercase, then NFC), so a precomposed "café" finds the decomposed `cafe` + U+0301 that macOS text and file names often carry, and the other way round; `Find`, `Matches` and `Search` all compare folded text, while hits show the line as written. Already composed text passes norm's quick check uncopied: warm search is unchanged, cold ~+15% (one quick-check pass per file read).
- Search as you type (#2632): the Engine remembers data read by the previous search, then only `stat`s and rereads changes: directory listings (directory mtime),
  original and lowercase file content (mtime + size, up to 64 MB total; excess is read every time), and entries of each kind (mtime + size of all JSON files).
  Files and directories changed within 2 seconds are not cached, avoiding invisible same-timestamp rewrites with unchanged size. On synthetic ~33 MB archives (37k files), median warm search ~360 ms → 85 ms, cold ~350 ms → 270 ms (`go test -bench BenchmarkFind ./internal/kb`, median of 5); cache ~35 MB, results identical to uncached (`find_test.go`). 35 MB is a lower bound: the synthetic data has almost no uppercase, so original and lowercase are the same string stored once; files with uppercase (English names, Build, etc.) need both, roughly 2× text size. With uppercase on every line, the same archive reaches the 64 MB cap (36k of 37k files cached, others reread); warm search remains ~85 ms.
- Find only counts lines in the files opening an entry shows (`Matches`): profile, timeline and metadata (`summary.md`, `log.md`, `meta.json`; a recording's `transcript.md`), a chat's day files, a doc's JSON. Bookkeeping under an entry (`moments-judged.json`, `handles.json`, any other file) is not searched, so every result with hits shows them once opened (`TestFindAgreesWithMatches`). A limit of zero or less gives no results.
- Opening an entry shows **Search "term"** at the top: every hit in this entry, with one message before and after each message hit. Tapping opens that day (`KBDayView`), scrolls to and highlights the message, and allows paging to adjacent days.
- Going back no longer errors: the Knowledge page itself is pushed by `navigationDestination(isPresented:)`; using `NavigationLink(value:)` inside to push an entry
  inserts the new page beneath it (`docs/lessons/ios.md` ("Navigation")), making the tap appear ineffective and exposing the entry page's yellow warning only on Back. Knowledge links now all use view-based navigation.
  Returning reruns `.task(id: query)`; do not search again if the same term already has results, and cancellation is not an error.

## Pages (checked with real data 2026-10-31)

- All six type pills fit in one row (drop icons first, then wrap if needed), not a horizontal scroll view (the navigation bar mistakes it for the page's scroll view, also pushing events offscreen).
- The list's tag menu only offers common tags for that kind (`kb.KindTags`); previously it used chat and project tags, which filtered out everything in people / documents.
- Nearly two thousand people, mostly untagged group members: organized ones first, the rest under **Not organized yet**, expanded on demand. Chats likewise.
- Names allow two lines (documents and meetings differ in the second half of their titles).
- Document / meeting / event details show time and **Open document** (in-app), with source chat among associations; project associations include documents, meetings and events (up to 30 each, newest first).
- If a profile is empty after removing content already in the page header, do not show an empty card.
