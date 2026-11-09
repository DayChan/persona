# To-dos (Todos)

Record something to do, hand it to one or several Agents, and see in To-dos which Agents are working on it, what each is doing and how far the overall task has progressed; Agents can also be grouped by to-do on the Agents page.

## Why this is needed

One task often needs several Agents (different computers, workspaces and tabs). An Agents list arranged by project, state or computer does not show which ones are working on the same task, its overall progress, where it is blocked or who it is waiting for. To-dos collect scattered Agents under one goal for progress tracking and coordination.

## Who owns what (since 2026-11-04)

To-dos belong to the assistant (`todos.go`, `todo_decisions.go`, `todo_sessions.go`, `tasks_close.go` in `server/internal/assistant`; formerly in hub, see `docs/assistant/assistant.md`, "Boundary with the hub"):

- **Assistant**: storage (the four tables below, still in the hub's Postgres, no migration, read/written only by the assistant process), every to-do API in "API overview" below, automatic project linking and following spawned sessions, reconciliation of herdr markers, snapshots of Agent titles/start errors, decision records, automatic completion and closing Agents for completed tasks.
  When hub receives a computer's session list it emits `sessions.changed`; when the user handles a card it emits `interaction.resolved`. These behaviors come from those two events.
- **Hub**: only forwards to-do APIs (`assistantwire.Routes`, carrying caller role; read-only roles still get 403), and uses the assistant's snapshot report for App session lists, question cards and pushes (`todos`: which unfinished to-do each Agent works on and which Agents are quiet, `hub/assistant_todos.go`; quiet reports only cover Agents of unfinished quiet tasks or those completed less than 7 days ago (`quietKeep`), keeping the report small; Agents from older tasks still push normally if they work again);
  `POST /api/v1/sessions` and `POST /api/v1/sessions/{mid}/{sid}/messages` with `todo_id` first ask the assistant (`assistant.todo_handoff`): does the to-do exist, is it done (if handoff is disallowed, it supplies 404/409 and the message, echoed by hub), prepend decisions to the Agent's message and project context for new Agents, all in one call. After handoff, ask assistant to link it (`assistant.todo_link`; assistant determines both linked title and to-do title); workspace renames through hub notify assistant (`todos.space_renamed`). Protocol in `docs/protocol/protocol.md`, "Bus".
- **When assistant is disconnected** (hub just started, assistant process restarting): to-do APIs return `503 assistant_offline`, "The assistant is temporarily unavailable. Try again later"; create/send with `todo_id` also return 503 (to-do existence unknown). Session-list to-do/quiet markers keep hub's last report (absent in the first second or two after hub starts before assistant reports). Hub emits no events during this period, so automatic linking, marker reconciliation and decision records wait for the next session list or answer after reconnection; workspaces renamed through hub are not migrated.

## Read-only planned tasks

`task_plan` treats `readonly: true` as a launch constraint. The assistant saves `permission: plan`, overriding an
omitted mode, `default`, or an explicit writable mode. It reapplies this constraint after plan-card edits and at
dispatch, including old saved plans and retries. Tasks without `readonly` keep their requested permissions.
This preserves automatic dispatch for read-only work without inheriting a writable launcher default; waiting for
Start alone would not make an unsafe permission mode read-only.

The node must honor `plan` or refuse the start. Claude Code terminal starts through herdr or Orca pass
`--permission-mode plan`. Codex terminal starts and supported terminal resumes use `--sandbox read-only
--ask-for-approval never`: no approval can escalate past the sandbox. The node retains that constraint for later
resumes, including a message that reopens the session. The terminal policy uses `never` because Codex 0.167 no
longer accepts `untrusted` on its CLI; the separate thread API policy is unchanged.

A Codex launcher with its own `-s` / `--sandbox`, `-a` / `--ask-for-approval`, `--full-auto`, dangerous bypass
(including `--yolo`), or `--approve-for-me` is refused with `readonly_unsupported` and `delivered: none` instead
of guessing which permission wins. Managed launcher arguments are checked too. A launcher argument separator
that would turn the added flags into prompt text is also refused. Non-plan launches retain their original arguments.
An already running Codex cannot receive new CLI flags, so it cannot be adopted by a read-only start.
External runtime starts (including Paseo) still fail closed: their start protocol does not guarantee enforcement,
and a runtime may ignore the mode or fall back to a different provider at dispatch.
Update both the assistant and nodes for this behavior. This is the runtime's plan restriction on task work, not a
promise that the runtime writes no session metadata or plan files, and it does not prevent a later explicit mode
change by the user.

Health-space plans and mixed plans still wait for Start. Existing clients show the Read-only label on a waiting
plan, and collapse a started plan to "Started N tasks"; the saved task's `permission` is `plan`. Displaying both
labels on the clients is separate client work.

## Data model

Server state is stored in the hub's Postgres database (read/written by the assistant, above):

### 1. todos table
- `id` (text, PK): opaque string identifier (e.g. `todo_3f2a1b9c`).
- `title` (text, NOT NULL): to-do title.
- `notes` (text, NOT NULL, DEFAULT ''): detailed description and requirements, sent as context when handed to an Agent.
- `done_at` (timestamptz): completion time; `NULL` while unfinished.
- `created_at` (timestamptz, NOT NULL): creation time.
- `updated_at` (timestamptz, NOT NULL): last modification time.
- `origin` (text, NOT NULL, DEFAULT ''): assistant chat that created the task (session id); empty for manual creation ([unified-entry.md](unified-entry.md)).
- `plan_id` (text, NOT NULL, DEFAULT ''): plan that created it (`task_plans.id`).
- `done_by` (text, NOT NULL, DEFAULT ''): who marked it done: empty = user tapped (or launch failed, see `start_error`), `auto` = assistant automatically completed it after judging it finished ("Automatic completion"), `review` = supervisor completed it after acceptance (#3124), `cancelled` = supervisor cancelled it (#1851). The API continues to send `done_by = review` for cancellations so existing clients retain their review labels and reasons; supervised cancellations also keep `supervision.state = cancelled`.
- `done_reason` (text, NOT NULL, DEFAULT ''): judgment model's reason for automatic completion (e.g. "PR merged and TestFlight released"), cleared when user marks done or reopens.
- `reopened_at` (timestamptz): last time user reopened a completed to-do; assistant only judges automatic completion again after a new turn of work follows it.
- `thread` (jsonb, NULL): the chat thread the to-do is bound to, `{"channel", "chat_id", "thread_id", "title"}` (#5279, "Chat thread" below); `NULL` while it has none. A `CHECK` requires all three keys, and a unique index `todos_thread` over `(thread->>'channel', thread->>'chat_id', thread->>'thread_id') WHERE thread IS NOT NULL` lets a thread belong to one to-do only. One value, read and written as a unit, hence a jsonb column like the ledger's `reviews` rather than four more columns on every to-do read; the binding goes with the row when the to-do is deleted.
- `source` (jsonb, NULL): where the to-do was asked for when that is another chat, `{"channel", "chat_id", "message_id", "anchor"?, "no_report"?, "reported_at"?, "report_text"?, "report_due_at"?, "attempts"?}` (#6146, "Where it came from" below); `NULL` for any other to-do. Migration `0429` (manager side; first numbered 0428, which #6188 also took, so `db.go` renumbers it); a `CHECK` requires the three ids. Not unique (several to-dos may come from one message); one value read and written as a unit, hence jsonb like `thread`. Deleting the to-do deletes it.
- `due_at` (timestamptz) / `due_date` (date): when it is due, a moment or a whole day in the user's time zone; at most one (`CHECK todos_due_one`). `remind_at` (timestamptz): when the phone is pushed about it; `reminded_at` (timestamptz): when that was claimed, cleared whenever `remind_at` changes. `priority` (text, NOT NULL, DEFAULT ''): `''` normal, `high`, `low` (`CHECK`). Migration `0442` (manager side, #1683, "Due date, reminder, priority" below).
- (Not in the table) `stage`, `pr_url`, `delivery`: stage strip, PR and delivery-card location, stored in settings `assistant.todo_stages`, merged when reading to-dos ("Delivery").

### 2. todo_spaces table
Records project workspaces (herdr workspaces) linked as a whole to a to-do:
- `todo_id` (text, NOT NULL, FK → `todos(id)` ON DELETE CASCADE): linked to-do ID.
- `machine_id` (text, NOT NULL): machine ID.
- `space` (text, NOT NULL): project name (session `space_name`, or `space_id` if absent).
- `linked_at` (timestamptz, NOT NULL): link time.
- Constraints:
  - Primary key: `(todo_id, machine_id, space)`.
  - Unique index: `UNIQUE (machine_id, space)`, ensuring a project follows only one to-do at a time.

### 3. todo_agents table
Records associations between to-dos and Agent sessions:
- `todo_id` (text, NOT NULL, FK → `todos(id)` ON DELETE CASCADE): linked to-do ID.
- `machine_id` (text, NOT NULL): machine ID.
- `session_id` (text, NOT NULL): session ID.
- `linked_at` (timestamptz, NOT NULL): link time.
- `released_at` (timestamptz): time active association was released; `NULL` while active.
- `title` (text, NOT NULL, DEFAULT ''): session-title snapshot (still displayable beyond the session-list time window).
- `cwd` (text, NOT NULL, DEFAULT ''): session working-directory snapshot.
- `agent` (text, NOT NULL, DEFAULT ''): launcher/Agent-type snapshot (`claude` / `codex`, etc.).
- `start_error` (text, NOT NULL, DEFAULT ''): asynchronous creation failure reason (viewable/removable in To-dos).
- `source` (text, NOT NULL, DEFAULT 'manual'): association source:
  - `manual`: manually linked or handed to an existing Agent;
  - `space`: linked with the entire workspace project;
  - `spawned`: spawned by a parent Agent linked to the to-do.
- `via` (text, NOT NULL, DEFAULT ''): association path:
  - For source `space`, project key `<machine_id>/<space>`;
  - For source `spawned`, parent Agent identifier `<machine_id>/<session_id>`.
- `dismissed_at` (timestamptz): manual removal timestamp. Removal sets both `released_at` and `dismissed_at`. APIs exclude removed records, and subsequent session updates do not automatically relink them.
- Constraints:
  - Primary key: `(todo_id, machine_id, session_id)`.
  - Partial unique index: `UNIQUE (machine_id, session_id) WHERE released_at IS NULL`, ensuring an Agent actively works for only one to-do at a time.

### 4. todo_decisions table
Decisions the user has made for this task ("Decision records"):
- `id` (text, PK): opaque identifier starting with `dec_`.
- `todo_id` (text, NOT NULL, FK → `todos(id)` ON DELETE CASCADE).
- `question` / `choice` / `reason` (text): question, selected choice, reason (reason may be empty).
- `source` (text, NOT NULL, DEFAULT 'manual'): `manual`, recorded manually in the to-do; `card`, recorded automatically when user answers an Agent question card; `outcome`, conclusions, decisions and lessons extracted from the Agent's last reply when the task finishes ("Record conclusions on completion" below); `agent`, noted by an Agent on the to-do while it works (`todo_decision_add`, below); `thread`, said by the user in the chat thread the to-do is bound to ("Chat thread" below).
- `source_machine` / `source_session` / `source_message` (text): provenance; for `card`, the asking Agent and card (interaction id), empty for manual records.
- `supersedes_id` (text, FK → `todo_decisions(id)` ON DELETE SET NULL): earlier decision this one supersedes.
- `created_at` (timestamptz, NOT NULL).
- Constraint: partial unique index `(todo_id, source_message, question) WHERE source_message <> ''`, recording each question on the same card only once.

### 5. todo_supervision table
Ledger for tasks supervised by the main chat (#3124, `docs/assistant/unified-entry.md`, "Supervision"), one row each; unsupervised to-dos have no row:
- `todo_id` (text, PK, FK → `todos(id)` ON DELETE CASCADE).
- `supervisor` (text, NOT NULL): supervisor, the chat that created it.
- `done_when` (jsonb, NOT NULL, DEFAULT '[]'): acceptance criteria (string array); `expect_minutes` (integer, NOT NULL, DEFAULT 0): expected minutes.
- `state` (text, NOT NULL, DEFAULT ''): `''` in progress, `pending_review`, `rework`, `needs_user`, `accepted`, `cancelled`; `round` (integer): rework round.
- `reviews` (jsonb, NOT NULL, DEFAULT '[]'): each review (`verdict`, per-item `checks`, `note`), at most 20 kept; `updated_at` (timestamptz).

Link/release semantics:
- When an Agent is linked to a new to-do and is active on another, the same transaction marks the original association `released` (records `released_at`); the old to-do shows it as "Previously involved".
- If an Agent previously involved in this to-do is manually linked back, clear `released_at`/`dismissed_at`, and refresh `linked_at` and snapshot information.
- When the user removes an Agent from a to-do, set that row's `released_at`/`dismissed_at` (soft removal, excluded from API results).

## API overview

`/api/v1/todos…` are assistant APIs, forwarded unchanged by hub (`503 assistant_offline` when assistant is disconnected); the last two rows are hub's own APIs, asking assistant for the `todo_id` part.

| Method | Path | Description |
| --- | --- | --- |
| GET | `/api/v1/todos` | All to-dos (`created_at` descending; the apps sort them themselves), each with `due_at` / `due_date`, `remind_at`, `reminded_at`, `priority` when set, and `spaces` and `agents` lists (removed rows filtered out; active first by `linked_at` descending, previously involved after by `released_at` descending); a to-do waiting for the owner also has `ask` ("Waiting for the owner") |
| POST | `/api/v1/todos` | Create `{"title", "notes"?, "due_at"? \| "due_date"?, "remind_at"?, "priority"?}` → `{"todo": Todo}`; nonempty `title` required (400); `due_at` / `remind_at` RFC 3339, `due_date` `YYYY-MM-DD`, `priority` `high` / `normal` / `low` / `""`, anything else (or both due fields) 400; read-only role (watch) 403 |
| PATCH | `/api/v1/todos/{id}` | Edit `{"title"?, "notes"?, "done"?, "due_at"? \| "due_date"?, "remind_at"?, "priority"?}` → `{"todo": Todo}`; only supplied fields change, `null` (or `""`) clears a due date or reminder; either due field replaces the other; a new `remind_at` is pushed again; user completion-state changes clear `done_by` / `done_reason`, and reopening records `reopened_at`; missing 404; read-only role 403 |
| POST | `/api/v1/todos/retitle` | Give short titles to task to-dos previously named from the prompt's first line ("To-do titles") → `{"dry_run", "todos": [{"id", "old_title", "new_title"}]}`; `?dry_run=1` lists without changes; read-only role 403 |
| POST | `/api/v1/todos/{id}/accept` | Verify a to-do from a briefing's Accept button `{"asked_at"}` (the `brief` feed item's `accept[].asked_at`, `docs/protocol/protocol.md`) → `{"ok":true}`; no `asked_at` 400; not the round that waits now (the to-do was reopened or asked again since), already verified or never asked 409; a decision whose recommendation could not reach the task's Agent 502, in the request's language with the reason (nothing taken or recorded, the button works again); the stored asks could not be read or the acceptance could not be saved 503 (nothing verified, the button works again); the ask may have no Lark card (the bot is not set up, or its task reports are off); read-only role 403 |
| POST | `/api/v1/todos/{id}/done-check` | Run an "Automatic completion" judgment read-only → `{"verdict", "reason", "would_close", "skip", "replies", "signals"}`, no database write ("Automatic completion"); missing 404; read-only role 403 |
| DELETE | `/api/v1/todos/{id}` | Delete to-do and associations → `{"ok": true}`; Agent sessions untouched; missing 404; read-only role 403 |
| PUT | `/api/v1/todos/{id}/spaces/{mid}/{space...}` | Link an entire workspace project → `{"ok": true, "linked": N}`; `space` is URL-encoded project name (wildcard path); completed to-do cannot be linked (409); missing 404; read-only role 403 |
| DELETE | `/api/v1/todos/{id}/spaces/{mid}/{space...}` | Unlink the project (physically delete this project's space-linked Agents under the to-do) → `{"ok": true}`; missing 404; read-only role 403 |
| PUT | `/api/v1/todos/{id}/agents/{mid}/{sid}` | Link an existing Agent (no message) → `{"ok": true}`; clears `dismissed_at`; completed to-do cannot be linked (409); missing 404; read-only role 403 |
| DELETE | `/api/v1/todos/{id}/agents/{mid}/{sid}` | Remove Agent (soft removal: sets `released_at` and `dismissed_at`, excluded from API and never automatically relinked) → `{"ok": true}`; missing 404; read-only role 403 |
| PUT | `/api/v1/todos/{id}/thread` | Bind the to-do to a chat thread `{"channel", "chat_id", "thread_id", "title"?}` (replaces the one it had) → `{"todo": Todo}` with `thread`; the three ids are trimmed, required and at most 256 characters (400), the title is trimmed and clipped to 200; another to-do holds the thread 409; missing 404; read-only role 403 ("Chat thread") |
| DELETE | `/api/v1/todos/{id}/thread` | Unbind it → `{"todo": Todo}`; a to-do with no thread stays as it is; missing 404; read-only role 403 |
| GET | `/api/v1/todos/{id}/decisions` | Decision list → `{"decisions": [...]}`, chronological; effective only by default, `?all=1` includes superseded decisions (with `superseded_by`); `[]` when none; missing 404 |
| POST | `/api/v1/todos/{id}/decisions` | Record manually `{"question", "choice", "reason"?, "supersedes_id"?}` → `{"decision": Decision}`; empty question/choice 400, `supersedes_id` not an earlier decision in this to-do 400; read-only role 403 |
| POST | `/api/v1/todos/{id}/decisions` (extension, `todos.write`) | An extension's outcome `{"question", "choice", "reason"?, "ref"?}` (e.g. a scheduler's acceptance evidence) → `{"decision": Decision, "inserted"}`; `source` is always `outcome` (`manual` or another source 400), `source_machine` is `extension:<id>`; `ref` (at most 200 characters, no `#`) becomes `source_message` `ext:<id>:<ref>`, so a replay with the same ref and question writes nothing and answers `inserted: false`; `supersedes_id` 400. A full device may also pass `"source": "outcome"`; `ref` is the extension's only |
| PATCH | `/api/v1/todos/{id}/decisions/{did}` | Mark supersession after the fact `{"supersedes_id"}` (empty string undoes) → `{"decision": Decision}`; same rules; missing 404; read-only role 403 |
| POST | `/api/v1/sessions` | Create session, optional `todo_id`: after successful creation, automatically link within the same request; missing to-do 404, completed 409, read-only role 403 |
| POST | `/api/v1/sessions/{mid}/{sid}/messages` | Send message, optional `todo_id`: automatically link after success; missing to-do 404, completed 409, read-only role 403 |

### Session decoration fields
In session lists (`GET /api/v1/sessions` and WebSocket `sessions` pushes), hub injects to-do fields using the assistant snapshot's report (`todos.links`):
```jsonc
"todo": {
  "id": "todo_weather",
  "title": "Release weather App 1.3"
}
```
Only present when the session is currently associated (`released_at` and `dismissed_at` both `NULL`) and the to-do is unfinished. On release, transfer or completion, the field automatically clears; clients need no frontend join.
The internal `spawn_from` field carried in session reports is stripped before hub pushes to clients.

### Live WebSocket pushes
- `{"type":"todos"}`: broadcast on to-do creation, edit, deletion, whole-project link/unlink, Agent association changes, a change of its chat thread, or when its `ask` comes up, is taken or ends ("Waiting for the owner"); clients refetch `GET /api/v1/todos`.
- Session-change push: when to-do/association changes affect sessions, assistant reports a new snapshot; hub compares which computer's sessions changed and re-pushes that computer's `sessions` event.

### Status codes and roles
- Missing title or malformed parameters: 400 Bad Request.
- Missing to-do: 404 Not Found. Old hubs without to-do APIs return 404; clients hide the entry point without an error popup.
- Linking to a completed to-do: 409 Conflict. Binding a chat thread another to-do holds: 409 Conflict.
- Assistant disconnected: 503 `assistant_offline` (to-do APIs and create/send with `todo_id`).
- Read-only role (e.g. paired Apple Watch, `role: "answer"`): every to-do write API (POST / PATCH / DELETE / PUT), plus create/send with `todo_id`, returns 403 Forbidden.

## Whole-project linking and automatic linking

### Whole-project linking
A herdr workspace can be linked as a whole to an unfinished to-do:
- On linking, batch-link every currently online Agent in the project (state not offline), `source = "space"`, `via = "<machine_id>/<space>"`.
- If the project is already linked elsewhere, unlink it first (remove the old to-do's space-linked Agents belonging to this project), then transfer to the new to-do.
- If an Agent in the to-do was dismissed, whole-project linking clears that dismissed state too.
- Unlinking deletes the workspace association and physically deletes Agent rows under this to-do with `source = "space"` and this project's `via` (manual/spawned Agents remain).
- Workspace writes (link, move, unlink, rename and task deletion) commit and publish their ownership changes in the same order. Session lookup happens outside that write boundary; a superseded request cannot later attach Agents to its former owner. Pending automatic Agent writes recheck current workspace ownership before persistence.

### Automatic linking with a project
When assistant receives a computer's session list (`sessions.changed`), it checks online Agents in whole-linked projects:
1. Agent currently has no other active to-do association (automatic rules do not take Agents away from other to-dos);
2. Target to-do has never had any record of this Agent (active, previously involved or manually removed all count as previously present, so no automatic relinking);
When both hold, automatically link to the to-do (`source = "space"`).
Automatic linking first updates assistant memory and reports to hub immediately (hub pushes that computer's session list again); database writes happen in background and roll back if they fail.
Previously hub linked it itself and included the association in that same session-list push; now it waits for the next push after assistant reports back (one bus round trip).
Automatic linking stops when a to-do is completed and resumes when reopened.

For renames through hub (shared with pin/mute's rename API), hub tells assistant (`todos.space_renamed`), migrating the space name in `todo_spaces` and corresponding `via` in `todo_agents` together. Assistant writes database first, then memory and broadcasts `{"type":"todos"}`. If the new name is already linked to another to-do (unique-index conflict), this project's old to-do link is invalidated and cleaned up, with a log entry. No migration if assistant is disconnected (same as "Known limitations", item 1).

## Following spawned Agents

Child Agents spawned by a linked Agent automatically join the same to-do, tracking their parent hierarchy:
1. **Parent marker**: an active Agent on an unfinished to-do has a `persona_todo_ref` marker on its herdr pane, value `<todo_id>@<machine_id>/<session_id>`.
2. **Dispatcher passes the marker**: after launching a child, the dispatcher reads its own pane marker and writes the value to the child's pane as `persona_from`.
3. **node reports, assistant associates**: when discovering sessions, node copies `persona_from` into `spawn_from` and reports to hub; hub passes the session list unchanged to assistant (`sessions.changed`). Assistant parses to-do ID/parent Agent; if unfinished, child has no active association and no history in that to-do, automatically link it (`source = "spawned"`, `via = "<parent_machine>/<parent_session>"`). Following does not depend on parent being online or on the same computer.
4. **Grandchildren follow**: after the child joins, node also sets `persona_todo_ref` on its pane; further generations follow the same mechanism.
5. **Consume the marker**: after child gets an active association, node's periodic reconciliation automatically clears its pane's `persona_from`, preventing mistaken relinking later.

### Dispatcher contract and example command
After launching a child Agent (on the same or another machine), the dispatch script should copy the marker as follows:
```sh
REF=$(herdr pane get "$HERDR_PANE_ID" | jq -r '.result.pane.tokens.persona_todo_ref // .result.pane.tokens.persona_from // empty')
[ -n "$REF" ] && herdr pane report-metadata "$CHILD_PANE" --source persona --token "persona_from=$REF"
```
Note: for a child on another machine, read the parent pane marker locally, then send the command that writes `$CHILD_PANE` to the target machine's herdr.

### botmux following
botmux sessions have no herdr pane or `persona_from`. node reads each session's Lark group (`raw.chatId`) and thread-root message (`raw.rootMessageId`) from `botmux observe --include-raw`, then reads `orchestrate-dispatch.json` in the botmux data directory (`SESSION_DATA_DIR`, otherwise the directory referenced by `~/.botmux/.data-dir`, otherwise `~/.botmux/data`): keyed by dispatched thread-root messages, with `orchSessionId` identifying the dispatching session (`botmux dispatch --session-id`). These three values report to hub with the session (`chat`, `topic`, `dispatched`) and are stripped before pushing to clients. After the first two rules, assistant tries two more, only for Agents with no active to-do and no previous record in the target to-do:
1. **Follow dispatch**: if the session's thread was dispatched with `botmux dispatch` by a session (on any computer; offline counts if still in its computer's list, i.e. offline no longer than `offlineWindow`) linked to an unfinished to-do, join that same to-do (`source = "spawned"`, `via = "<machine>/<session>"`).
2. **Follow the group**: among unfinished to-dos linked by other online botmux Agents in the same Lark group, if the largest count is at least twice the runner-up (automatic when only one to-do exists), join it (`source = "chat"`, `via` is group id; client shows **Same Group**). Ties or insufficient margin do not link; wrong links can be changed manually. Agents linked by this rule do not vote (otherwise one Agent could spread a to-do across the entire group), nor do Agents on computers with no list report for 10 minutes.

## herdr markers and reconciliation

Under herdr, to-do associations map to pane markers (shown in the sidebar):
- **Written markers**: for Agents linked to unfinished to-dos, node maintains two metadata tokens on their pane:
  - `persona_todo`: to-do title;
  - `persona_todo_ref`: `<todo_id>@<machine_id>/<session_id>`.
- **Clearing markers**: when a to-do completes, is deleted or an Agent is unlinked, node clears its `persona_todo` and `persona_todo_ref`.
- **Source/namespace protection**: all writes/cleanup use `--source persona`. Reconciliation only touches `persona_todo`, `persona_todo_ref` and `persona_from`, never user or other plugin metadata.
- **Periodic reconciliation**: assistant sends expected markers by session ID (node RPC `todo.sync_session_tokens`, via `device.call`): immediately for computers whose associations changed, no resend otherwise; on a new node/hub connection (`connected` in `sessions.changed` changes: node may have restarted, do not touch markers until received), resend all with the first session list on that connection, like hub's former immediate resend after node reconnect. node resolves live panes dynamically only from currently living sessions and host-reported current Agent sessions, compares herdr's markers with expectations, fills missing and removes surplus markers. If two sessions map to one pane, only the live one counts. node reconciles every 30 seconds; after herdr restart, pane-ID change or hot handoff, markers restore on the correct new pane without old exited sessions contaminating it.

## State calculation rules

Clients calculate to-do runtime state live from linked active Agents; it is not persisted. Use the first matching state in this priority order:

| Priority | State | Condition |
| :--- | :--- | :--- |
| 0 | Didn't Start | Agent for a task created from chat failed to launch; assistant ends the to-do (`done_at` + `start_error`, #1077); client shows failure/removal, not active |
| 1 | Done | User marked done, or assistant judged a dispatched task complete (`done_at != null`; client shows **Auto-completed** for `done_by = "auto"`) |
| 2 | Needs You | A linked active Agent is `needs_you` (waiting for question, plan approval or authorization) |
| 2.5 | Interrupted | A linked active Agent is stopped (`unread` / `idle`) with `turn_error`: model-service failure interrupted the turn (disconnect, overload, rate limit, expired login…), unfinished (#1516); red, **Continue** on the card sends "继续" ("continue"). A subscription usage limit is not this (the next row) |
| 2.6 | Usage limit | A linked active Agent is stopped with `turn_error_kind` `usage_limit`, and the reset is not known or not more than 10 minutes past. Not red and not Interrupted: "Usage limit · resumes <time>" when the CLI or the hub will continue it, "Usage limit · resets <time>" when nothing will. An older hub that does not send the kind stays Interrupted |
| 3 | Unread | A linked active Agent is `unread` (task finished, not viewed yet) |
| 4 | Active | A linked active Agent is `working` (working or starting) |
| 5 | Follow up | All linked active Agents are stalled (`idle` seen, `offline` not running, or launch failed) |
| 6 | Not started | No linked active Agents |

- To-do list ordering: unfinished first (same state by `created_at` descending), completed collapsed at bottom.
- Completion means the task is over: its Agents' sessions no longer say they currently work for it; **Reopen** to continue. A chat-dispatched task the hub completed (auto-completed or accepted by its supervisor) also reopens when one of its Agents starts a turn again (sent more with `agent_send`, or by the user; see "Close Agents for completed tasks", #5892). Cancelled tasks stay closed when their Agent works again; only an explicit **Reopen** resumes their task tracking and supervision (#1851).
- User marks done while Agent is still launching (can take minutes on a busy machine; `done_at` written first): not **Didn't Start**, to-do stays done with no `start_error`; Agents that do launch join this completed to-do and are reclaimed by "Close Agents for completed tasks" under existing rules (only after idle long enough) (#2954).

## To-do titles

To-dos created by `task_plan` and `agent_start` (#2775) formerly used the prompt's first line as title, often starting with instructions to the Agent ("ultrathink ultracode 主人 11-05 19:47 的反馈，已授权直接开工：…", meaning feedback from the owner, authorized to start; "秘书派活（主人授权过…）", meaning assistant dispatch, owner already authorized), obscuring the task itself. Now two steps (`assistant/todo_titles.go`):

1. **On creation**, synchronously supply a cleaned title: remove leading keywords such as ultrathink / ultracode / think hard, provenance/timestamps such as "主人 11-05 19:47 的反馈" (owner's feedback), parenthetical notes such as "秘书派活（…）" (assistant dispatch) / "【…】", and "已授权直接开工" (authorized to start); truncate to 20 Chinese characters (20 Japanese, 40 characters for other languages). No model call, no delay to task launch.
2. **In background**, the small-task model (**To-do Title** `todo.title` in model configuration, inheriting the knowledge-organization category, through `models.complete` like chat titles `assistant.title`) reads the prompt and produces a short title explaining the task, replacing step 1. Keep step 1 if model unavailable, errors or empty result. Before replacement, verify title is still step 1's (conditional `UPDATE … WHERE title = old title` in database); never overwrite an intervening user edit. Prompts: `prompts/todo/title.md`, `title_input.md`.

Assistant-supplied titles in `task_plan`: if within the limit and without those prefixes, use as-is without a model; only first-line, overlong or prefixed titles use the two steps.

**Batch retitle existing to-dos**: `POST /api/v1/todos/retitle` selects only task-created to-dos (nonempty `origin`) whose title is still the prompt's first line truncated at dispatch: over the limit and a prefix of `notes` (truncated, with or without "…"). Short titles merely resembling prompts ("【紧急】修登录", "[Urgent] fix login"; "11/05 反馈：修登录", "11/05 feedback: fix login") count as user-created and stay untouched (to-dos do not record who named them; this is the most conservative possible judgment). Use model for each (4 concurrent), or step 1 cleanup without a model; again verify title unchanged before writing.

```sh
curl -X POST '<hub address>/api/v1/todos/retitle?dry_run=1' -H 'Authorization: Bearer <token>'   # Preview changes first
curl -X POST '<hub address>/api/v1/todos/retitle' -H 'Authorization: Bearer <token>'             # Then apply
```

## Automatic completion

Tasks dispatched from assistant chats (to-dos created by `task_plan`, nonempty `origin`) are automatically marked done by assistant, without individual user taps (except tasks supervised by main chat: completion judgment moves to "Pending review", and supervisor must accept with `task_review`, `done_by = review`, see `docs/assistant/unified-entry.md`, "Supervision"). Manually created to-dos (empty `origin`) never auto-complete; only user marking done counts.

**When judged**: all task Agents stop (none working, waiting for user or carrying pending cards), at the same point assistant sends the done receipt to chat (`taskTurnEnded` in `assistant/tasks_follow.go`; one receipt and one judgment per stretch of work). The receipt's `done` is the end of that stretch, not of the task: the apps draw it "<task> stopped, not done yet" while the to-do is open and as done once it is, and the "Today" card lists as finished only to-dos that are done (#6153).

Normal receipts are written before judgment for both supervised and unsupervised tasks. At receipt time, the latest open to-do with a supervision ledger adds `review: {"state":"awaiting","criteria":N}` (the number of `done_when` entries, including zero), unless its effective supervision state is `needs_user`. This names Persona's next follow-up: `superviseJudged` signals review for finished work or idle for unfinished work; `needs_user` means the task is already waiting on the user. An accepted task that was reopened, or a cancelled task the user explicitly reopened, has an ongoing effective state and qualifies again. The receipt does not wait for the model or change the order of supervision signals. Failed / interrupted receipts carry no review. The web keeps the neutral stopped title for an open task and adds "Waiting for Persona's review", with a positive criterion count; completing the to-do hides this historical hint. Older receipts and clients remain compatible (#4893).

**How judged**: stopped is not finished; "not all changes done…" or "waiting for review" must not close. Assistant's judgment model (through hub model configuration, `app.call models.json`; **AI Twin Judge** `inbox.judge`, i.e. `inbox.judge_ai` in the manager's `manager.json`, inheriting `ai` if unset) sees three things: task title, dispatch requirements (to-do `notes`), each active Agent's latest turn (read via `session.items`: user's message and last complete reply; fallback truncated `last_text` in session list if unreadable). Later new requirements from user override original ones (already handled in earlier turns); if later messages merely confirm/answer a question ("OK", "👌", "Use A"), original dispatch requirements still apply (#1116). Model chooses one of five:

| Verdict | Meaning | Auto-complete |
| --- | --- | --- |
| `done` | Requested work finished, reply gives results, user need do nothing more, no waiting for others | Yes |
| `not_done` | Partial, failed, blocked, or only reporting progress/plan | No |
| `waiting` | Own work finished, awaiting external result (review, CI, someone else's reply, release queue) | No |
| `needs_user` | User must decide, supply information, approve, act manually or accept | No |
| `unsure` | Cannot tell | No |

- Caveats do not prevent completion: platforms without screenshots, unmeasured environments and reminders to user are not unfinished work. Routine waits after delivery (TestFlight external-group Beta review, CI rerun) do not count as waiting either; addressing App Store / Mac App Store review feedback then awaiting review outcome does count as `waiting`.
- Prefer leaving open: unavailable model, timeout, invalid JSON or verdict other than `done` leave to-do untouched and only log; receipt still sends.
- No judgment for a turn interrupted by model service (#1516): if an Agent stops on an API error (`turn_error`: Claude Code's last message such as "API Error: …", Codex reported by node from this turn's error, #2185), receipt has `state: "interrupted"` (summary "Model-service error interrupted the task", original error in `error`); no judgment-model call or closing to-do. `done-check` directly returns `not_done` when the last reply is such an error.
- No judgment for a turn with no output either (#2349): latest stopped Agent's turn (node `session.items`, `turns: 1`) contains no assistant text, tool calls or images and no `turn_error` (model service returns 200 with empty content; Claude Code adds an invisible "no visible output" notice, retries, then ends silently). Receipt is `interrupted` (error "这一轮没有任何输出：…", "This turn had no output: …"), notification uses interrupted, no judgment or to-do close. Unreadable items (node unreachable/no items) or user pressing Esc ("已中断" notice, "Interrupted") do not count.
- Cheap signals: final reply has a GitHub PR link and mentions merging (merged / 已合并 / 合入, the latter two meaning merged), or mentions TestFlight plus a build number. Assistant includes these as hints only, never enough alone for completion.
- Prompts: `server/internal/prompts/assistant/task_done.md`, `task_done_input.md` (`docs/assistant/prompts.md`).

**Visible and reversible**: auto-completion writes `done_at`, `done_by = "auto"`, `done_reason`. iOS / Mac / web list/detail show **Auto-completed** beside Done; detail shows reason. **Reopen**, like manual completion, returns to unfinished, clears `done_by`/`done_reason`, records `reopened_at`.

**After reopening**: same stretch of work cannot auto-close again; only when an Agent begins a new turn after `reopened_at` (user sends again, Agent resumes) is completion judged again. After assistant restart the turn's start time is unknown; reopened to-dos are not judged then (prefer leaving open). Database write rechecks (`done_at IS NULL`, `reopened_at` before work start); user completion/reopen during judgment wins.

**After completion**: same path as user marking done: sessions lose to-do field, node reconciliation clears `persona_todo` / `persona_todo_ref`, whole-project linking/spawn following stop, Inbox AI twin no longer associates messages with it; assistant live snapshot marks task "Done (automatically closed after hub judged complete)", omitted after 24 hours.

**Diagnosis**: `POST /api/v1/todos/{id}/done-check` judges the same inputs, returns verdict only without database writes (read-only role 403):

```jsonc
{"verdict": "waiting", "reason": "Waiting for App review", "would_close": false, "skip": "", "replies": 1, "signals": {"merged_pr": false, "build": false}}
```

Nonempty `skip` means automatic flow would not judge this to-do (`not_a_task` manual, `done` completed, `reopened` no new work after reopen), but API still returns model verdict for inspection.
With **Delivery** enabled (next section), response also has `delivery`; `verdict` may be hub's own `undelivered`.

## Delivery

(#3306, lessons in `docs/lessons/delivery.md`) A code-change task is "done" only when live and tested, not when a PR is opened or merged. Judging only whether an Agent says finished leaves many tasks at "PR opened", "merged but off by default" or "not deployed" while showing Done.
Assistant setting `done_needs_delivery` is **off by default** (off retains the old verdict-only behavior). Enable either way:
tell assistant "Turn on 'only done when live and tested'" (`assistant.done_needs_delivery` in `persona_settings`, confirmation card on phone), or `PUT /api/v1/assistant` with `{"done_needs_delivery": true}`;
effective from the next judgment and next dispatched task. When on:

- **State criteria at dispatch**: for non-read-only tasks created by `task_plan`, hub appends delivery criteria to the Agent's first message (dispatch prompt, `prompts/assistant/task_delivery.md`): done = live and tested, with measurements;
  if rules permit self-merge/deploy/enable, finish the whole path; if current user approval is required (merging a repo not owned by user, production deploy, enabling an off-by-default feature, outbound messaging), stop one step before approval and state what needs approval;
  first try to resolve CI/other-person/environment blocks yourself; if impossible, state where and who can unblock. Fixed last line: `交付：<已上线并实测 | 停在 PR | 已合未上线 | 默认关待启用 | 不涉及上线>；卡点：…；谁来解：…` ("Delivery: <live and tested | at PR | merged, not live | off by default, awaiting enablement | no deployment involved>; blocker: …; who can resolve: …"). Read-only tasks omit it.
- **Judge one more thing**: the "Automatic completion" model also returns `delivery` (`live` / `pr` / `merged` / `off` / `none`), `gate` (blocking gate), `unblock` (`user` / `agent` / `other`), taking the Agent's line as authoritative unless contradicted by the body.
  If model omits it, use Agent's line; if neither exists, `none`. If `live` (or not judged) but reply mentions a PR without deployment or TestFlight build-number wording, treat as `pr` / `merged`, unless the Agent's line says live.
- **Only `live` and `none` auto-complete**: if `verdict` is `done` but delivery incomplete, change to `undelivered` and keep to-do open.
  Main-chat-supervised tasks (#3124, already not closed by hub) do not enter pending review; supervision message says "Finished but not live yet: …(at PR, blocked on '…')", for main chat to follow up or ask user approval.
- **To-do shows the blocker**: judgment results except `not_done` are recorded in to-do `delivery` (`{"state", "gate", "unblock", "at"}`, settings `assistant.todo_stages` alongside stage strip, no new table); stage advances at least to its corresponding step (PR → review, merged → merge, off by default/live → deploy).
  Web to-do rows add "At PR · Blocked on '…' · Needs your approval" while open and `state` is `pr` / `merged` / `off`; Android to-do rows (when the Agent is neither working nor waiting for you) and details show the same line (`Todo.hold` in `core:domain`); iOS / Mac do not render it yet (optional field, old clients unaffected).
- **Batch user approvals**: assistant live snapshot lists delivery blockers after each task; open tasks with `unblock = user` get a separate "Waiting for user approval to go live" group (oldest ten).
  Assistant CLAUDE.md tells it to present these together in one message; after user approves in one reply, use `agent_send` to notify the corresponding Agents separately, rather than asking anew for every item.

## Close Agents for completed tasks

Completing a to-do stops Persona's automatic follow-ups for its linked Agents, including when a turn ends after
completion: no transient-error retry, usage-limit continuation, scheduled wake or new triage card. Triage may still
record the turn's work state. The check runs before scheduling and again before sending, including after a manager
restart. It applies to manually linked to-dos too, regardless of whether the Agent can be closed. Released or dismissed
links do not count, and an Agent linked to an open to-do can still receive follow-ups. Deleting a to-do leaves its Agents
unaffected. Direct chat messages and `agent_send` still work; a user-completed to-do stays done until explicitly reopened,
while a hub-completed task reopens on new work as described below.

After a task dispatched from assistant chat (nonempty `origin`) completes—manually or automatically—its Agent no longer occupies a herdr tab:
assistant checks every minute (`assistant/tasks_close.go`, reading hub's App-facing session list through `sessions.list`); once task completion and Agent inactivity both reach 3 minutes (`taskCloseIdle`, measured from the later of completion time and last activity), ask node to close it (`session.retire`, `node/retire.go`):
close its pane (whole tab if it is the only pane, same as **Close** on phone), exiting the Agent process with it.

**What stays**: to-do Agent record (machine, session id, directory) remains, not released; node's launch information (launcher, directory, model, hub `node_sessions`) and transcripts remain.
Results still read from transcripts (receipts, `agent_read`, chat page); Unread Agents may also close.

**How to reuse**: send to a closed Agent—`agent_send`, directly to task from its card (`route`), or chat page—and node first resumes in a new tab using original launcher, directory and workspace (tab named from session title), then sends the message:

| Agent | Resume method |
| --- | --- |
| Claude Code | `<launcher> --resume <id>`, with original permission mode, thinking effort and model |
| Codex | Launcher arguments (with original model substituted) `resume <id> -- <message>`, through `personad codex-start`; message passed as an argument, not through shell or on screen |
| Compatible Codex forks | `<launcher> resume <id> <message>` |

Resume waits for launcher readiness; like chat sends, `route` is not cancelled by phone disconnection, waiting at most 5 minutes. When the Agent starts the turn, its completed to-do (a chat-dispatched task it still belongs to that the hub closed: `done_by` `auto` or an accepted `review`; user-completed and cancelled tasks stay completed) reopens, as **Reopen** does (`reopened_at` recorded): the task shows as in progress, the turn's end gets its interrupted or done receipt and notice as usual, and completion is judged again (#5892). If the to-do is completed again, the Agent closes again after 3 minutes idle.

A cancellation stays final whether the Agent receives a user message, an `agent_send`, or continues after a background job, before or after its idle close. It produces no new task receipt, completion judgment, supervision signal or automatic triage follow-up while the to-do stays closed. This also applies to older cancellations stored as `done_by = review` with a `cancelled` supervision ledger. The user can still explicitly **Reopen** the task.

**Do not close**: working, waiting for user (questions, plans, permissions, any dialog, pending cards), unknown engine state; queued messages, background shells, running sub-Agents, `/loop`, listening ports (development servers); linked to another unfinished to-do; assistant chats; sessions managed by other tools (external Codex app-server, runtime, daemon, pi); not started by Persona (unknown resume method); engines unable to create tabs (herdr-core); on Orca, anything but a Claude session (Orca resumes only Claude, #5548); Claude without transcript, Codex / compatible Codex forks without known launcher.
node rejects these (`not_retirable`), assistant records them and does not ask again for 10 minutes if Agent unchanged (`taskCloseRetry`, then retries because node may have upgraded). Old nodes not recognizing `session.retire` are likewise treated as refusals, so hub-first/node-later upgrades do not close wrongly; after node upgrade, closure resumes within at most 10 minutes.
Agents linked to no task (user-created, `agent_start`, inspection sessions such as herdr-leader) are not managed here.

**`/exit` from the assistant**: `agent_send` with only `/exit` or `/quit` closes a running Agent as **Close** does (its tab goes too) instead of typing it in; to an Agent that is not running it does nothing, so a closed Agent is not resumed in a new tab only to quit there.

**Tabs left with a bare shell** (`node/shelltabs.go`): an Agent Persona started can also end on its own (`/exit` typed by hand, an API error it gives up on, a launcher that quits), leaving its tab with only the login shell and `Resume this session with: claude --resume <id>`.
Every tab Persona opens for an Agent carries the herdr pane token `persona_tab` (the session id when known). Once a minute node looks at such panes with no Agent in them: when only shells are in front and the screen has not changed for 10 minutes (`shellTabIdle`; any output or typing restarts the wait), it closes the pane (the whole tab when it is the only one). The session is untouched: the next message resumes it as above.
Never closed: panes without the token (tabs the user opened, the pane picked for **Run Agent Here**), panes where another program is in front, a session being started or resumed, assistant chats. herdr does not keep pane tokens across a herdr server restart, so tabs opened before one are left alone.

## Decision records

The costliest long-task mistake is an Agent forgetting a decision already made: asking again or silently overturning it (#1618). Decisions scattered across Agent chats disappear when switching Agents or compacting, so collect them by to-do:

- **One decision** = question + choice + reason + time + provenance (asking Agent, answering card).
- **Automatic**: when user answers Claude Code's choice question in App (`question` card, `AskUserQuestion`) and the Agent is linked to an unfinished to-do, record each answered question (`source = "card"`; hub emits `interaction.resolved`, assistant records; nothing recorded if assistant disconnected). Do not record authorization, plan approvals, terminal dialogs or ignored cards; duplicate reports of a card record once.
- **Manual**: **Decisions › Record one** in to-do details.
- **From the Agent** (#5279): an Agent on an open to-do has two MCP tools of its own (`mcp/todo/`, `agentToolNames` in `assistant/agent_control.go`). `todo_decisions` gives the effective decisions with who made each (`by`: `user`, or `agent` for `agent` / `outcome`), to reread after compaction; `todo_decision_add {question, choice, reason?, supersedes_id?}` notes a call the Agent made (`source = "agent"`, its machine and session as provenance), at most 10 in 10 minutes per Agent; it may replace an Agent's decision, never the user's (it is told to ask them). The to-do is always the one the calling Agent works on (node passes its pane's session, as for `report_to_supervisor`); an Agent cannot read or write another to-do's decisions, and outside an open to-do both say there is none. Handoff context marks these as the Agent's, not the user's.
- **Supersession**: a new decision can **Replace** an earlier one (`supersedes_id`), keeping the old one struck through; only unsuperseded decisions are effective. PATCH can mark/undo later corrections. Only earlier decisions in the same to-do can be replaced, preventing cycles.
- **Hand to Agent**: when `POST /api/v1/sessions` `prompt` or `POST …/messages` `text` carries `todo_id` (all clients' handoff does), hub asks assistant (`assistant.todo_handoff`); assistant prepends effective decisions as `<persona-context kind="context" title="待办 · <title> · 已定的决策">` ("To-do · <title> · Established decisions", prompt `server/internal/prompts/todo/decisions.md`), no client change needed; send unchanged without decisions. On read failure (database error), still send and only log.
- Recording/superseding updates to-do `updated_at` and broadcasts `{"type":"todos"}` so clients refetch decisions.
- iOS / Mac (`apple/Persona/Views/Todos/TodoDecisionsSection.swift`, model `TodoDecisionsModel`, wire `apple/Shared/TodoDecisions.swift`): the same Decisions section in to-do details, under the notes: **Add** records one, **Revise** records one that replaces it, the row's ⋯ marks it as replacing an earlier one or undoes that, **Full History** shows the replaced ones struck through. Read again whenever the to-do's `updated_at` changes; hidden on a hub without the route (route-level 404).

Not yet: the Android decision list.

## Questions nobody answered

(#1617, `assistant/ask_todo.go`.) When a card of the assistant's own `ask_user` **expires** unanswered (2 hours, `askUserWait`), the question becomes a to-do, so it waits where the owner looks and a late answer still reaches the conversation that asked. Only an expiry does this: a dismissed card, one withdrawn because the user wrote in the chat (`replied_in_chat`), an answered one and a conversation that stopped waiting behave as before. Agents' question cards keep their own paths.

- **How an expiry is told apart**: the manager's interaction queue ends the card (`await`, history status `expired`) and hands the question a dismiss; `askOnPhone` reads the history and marks that dismiss `reason: "expired"` (`proto.DismissedExpired`; a history that says nothing, or whose end was not written, counts when the card's time is up), which only the manager sets (the apps' answers have their `reason` dropped, `fleet_routes.go`). A hub that keeps the cards itself sends a bare dismiss; it counts as an expiry once the card's `expires_at` has passed.
- **The to-do**: id `todo_ask_<the card's id without ix_>`, so the same card never makes two (a second hand-over, a restarted assistant, two assistants on one store: the id is the primary key). If its record (below) can't be written, no to-do is made and the tool says the user did not answer, as before. Title: the first question. Notes, in the owner's language: when and in which conversation it was asked, each question's options with their descriptions as written (a "(Recommended)" in a label stays), and how to answer. `origin` is the asking conversation; no Agent. The live snapshot lists it on a line of its own ("asked with ask_user, nobody answered, waiting for the user's call": don't ask again, don't go ahead), not among the tasks; the next briefings list it under "Can wait" (`laterQueue`, the same queue Agents' non-urgent questions use) until it is closed or deleted, or for 7 days as that queue keeps anything. The apps show it as an open to-do; there is no new client UI.
- **The record**: what the message needs (the card, the asking machine and conversation, the questions, when) and whether it was told are kept in the assistant's settings (`assistant.ask_todos`), not on the to-do (no new column); records older than 30 days go (such a to-do is an ordinary one from then on). The record list is one setting, written whole: two assistants on one store dedupe the to-do by its id, but the last to write the list wins.
- **The answer goes back**: the first decision the owner records on the open to-do by hand (`POST /api/v1/todos/{id}/decisions`, the apps' decision form; `source = "manual"`) is claimed (written before it is sent, so it goes once; a claim not followed by the send within 5 minutes, say the assistant restarted in between, no longer holds, so the next decision goes) and sent to the asking conversation as one new message (`prompts/assistant/ask_answer.md`: "you asked … at MM-DD HH:MM; the user decided …"), through the queue `conversation_send` uses (now, or after its turn; kept across a restart; in the background, so the request that wrote the decision does not wait on the node); a conversation that is gone gives way to the main conversation of now. The to-do then closes. With several questions asked, the first decision goes and closes it; the message lists every question and says to ask again what the decision did not cover. A reply in the Lark topic the to-do is bound to is its decision too ("Replies in the thread"): the same message goes to the conversation as the bot's own send (so the answer comes back into the topic), never to an Agent, and the to-do closes; if no send goes through, the claim is given back. A second decision (the to-do is closed by then) sends nothing.
- **Closed without a decision**: closing it (`PATCH done: true`) or deleting it tells the asking conversation once that the question was dropped (the same prompt, its "dropped" form). Reopening and closing it again says nothing more; a decision recorded after a reopen still goes back once.
- **The Lark card** of the expired question closes saying "Moved to to-dos" instead of "Ended".
- **Restart**: an `ask_user` waiting while the manager restarts is still cancelled (it lives in memory, #26): it makes no to-do.

## Waiting for the owner

(#2056, `assistant/todo_ask.go`.) A to-do whose result waits for the owner carries that ask on itself, so the apps can show it in the to-do list and take it with one tap, not only the briefing and the Lark card.

- **`ask` on `GET /api/v1/todos`**: `{"outcome"?, "verdict", "recommend"?, "asked_at"}`, the same rows and words as a `brief` item's `accept[]` (`docs/protocol/protocol.md`, "Conversation feed"), read from the same acceptance book: a deployed to-do's "Please verify" (no `outcome`), or a supervised task's conclusion (#1226: `outcome` `done`, `cannot` or `decide`). `verdict` is the verdict line as its Lark card says it, in the request's language (the reason is as the review wrote it); `recommend` only on a decision (`decide`) that recommends an option; `asked_at` the round, which `POST /api/v1/todos/{id}/accept` takes. A to-do with nothing waiting has no `ask`: never asked, verified, taken, acknowledged, asked before its latest reopen, or a decision the review moved on from (rework, cancelled) or whose task closed. A client that does not know the field ignores it.
- **Taking it**: `POST /api/v1/todos/{id}/accept {"asked_at"}` stays the one way, as the briefing's Accept button: a decision's recommendation goes to the task's Agent and is recorded in its decisions (the same path as the Lark card's "Take the recommendation"), a conclusion that only tells is acknowledged, a deployed to-do is verified. An `asked_at` from an earlier round is 409 "already handled" and takes nothing; 502 when the recommendation could not reach the Agent (nothing taken).
- **The push**: a review's `needs_user` with a `recommend` (`task_review`) is pushed to the owner's devices once, besides its Lark card and whether or not the bot is on: a `decide` notification (`notify.request` kind `decide`, `docs/protocol/protocol.md` "Bus") to the devices that take "needs you" pushes (quiet hours make it silent, like a card's), opening the conversation the task came from. Its APNs payload:

  ```jsonc
  {"aps": {"alert": {"title": "Conclusion: <task>", "subtitle": "Persona", "body": "🤔 Needs your decision: <reason>\nRecommended: <option>"},
           "sound": "default", "thread-id": "<origin conversation>", "category": "TODO_DECIDE", "interruption-level": "time-sensitive"},
   "machine_id": "<the assistant's machine>", "session_id": "<origin conversation>",
   "todo": {"id": "<to-do id>", "asked_at": "<RFC 3339, the round>", "recommend": "<option>", "manager": "<manager id>"}}
  ```

  `todo.manager` is the manager whose to-do it is (`assistant` for the assistant the hub starts), as the merged list stamps each item's `manager`; OK names it in `X-Persona-Manager`, so it reaches that manager whichever one is the default. All rounds of one to-do share an `apns-collapse-id`, so a later round's push takes the earlier one's place. Through the push relay `category` and `todo` are sealed with the rest (`docs/operations/push-relay.md` "Encryption"). On the WebSocket (`ws_notify`) it is a `notify` of kind `assistant` with the same `todo`, so a client that does not know decisions shows a plain notification. The app's OK action on `TODO_DECIDE` calls the accept route with `todo.asked_at`. No push for a decision without a recommendation (nothing for OK to take), for a task from a health conversation (the lock screen says nothing of those) or a muted conversation; a deployed to-do's "Please verify" and conclusions that only tell are not pushed either.
- **The supervisor** is told what went out. With a Lark card, as before (don't ask again), plus that a push with OK went too. With the bot off it still asks the owner as before (the hub taking a push says neither that a device got it nor that its app shows OK), and is told that OK on the push may take the recommendation first.
- Not done here: the push is not withdrawn when the decision is taken elsewhere (an OK on it then answers 409, "already handled"); the watch's answer-only credential can neither read `/api/v1/todos` nor call the accept route.

## Chat thread

(#5279, step 2: the binding itself) A to-do can be bound to a chat thread, a topic in a group chat or a conversation, so that the thread and the to-do are known to belong together. The binding itself writes nothing to any channel. What the user says in the thread comes back to the to-do ("Replies in the thread" below), and what happens to the to-do is told in the thread ("Posting into the thread" below). `todo.*` events come in a later step.

- **The binding**: `thread` on the to-do, `{channel, chat_id, thread_id, title}` (the first three name the thread, the title is a label). `channel` is a channel id as `internal/channel` names them (`channel.Feishu` = `"feishu"`); it is trimmed and lower-cased before it is kept, so `Feishu` and `feishu` are one channel. For `feishu`, `chat_id` is the chat's id (`oc_…`) and `thread_id` is the `thread_id` the message events carry (`omt_…`), not the thread's root message id (`om_…`). The server does not check them against the channel (it cannot know a thread exists), so the caller must pass the real ones. At most one thread per to-do and one to-do per thread: binding a thread another to-do holds is refused (409, from memory first, finally by the unique index, so a second assistant on the same database cannot slip past it). Binding again replaces the thread; the old one is free for another to-do. A done to-do can be bound or unbound too (it may be reopened, and the user's tidying should not depend on its state).
- **API**: `PUT` / `DELETE /api/v1/todos/{id}/thread` (full role only), "API overview" above. Both move the to-do's `updated_at` and broadcast `{"type":"todos"}`, unless nothing changed (unbinding a to-do that has no thread, or binding exactly what it already has: nothing is written); `GET /api/v1/todos` returns `thread` with each to-do that has one. Editing a to-do leaves its thread alone.
- **Reverse lookup** for the next step (a reply in a thread finds its to-do): in memory, every `Todo` carries `Thread` and `sameThread(a, b)` (`assistant/todo_thread.go`) compares two bindings; from the store, `DB.TodoOfThread(channel, chat_id, thread_id)` (`internal/db/todo_thread.go`).
- **From the Agent**: an Agent on an open to-do has `todo_bind_thread {channel, chat_id, thread_id, title?, todo_id?}` (`mcp/todo/`, with the decision tools; the node resolves its caller the same way, `callerTools`). It binds the to-do the Agent works on and no other: `todo_id`, if given, must be that to-do's own id. A to-do that already has another thread keeps it (changing it is the user's call, the tool says to ask them); the same thread again only updates its title. Outside an open to-do it says there is none.
- Web (`views/Todos.tsx` `ThreadSection`, `lib/todo-thread.ts`): the to-do's page has a **Topic** section under the notes: one line "Lark topic · <title, or the thread id shortened>" (any other channel: "Topic (<channel>)") and **Unbind**, which asks first and then `DELETE`s the thread; the list row of a bound to-do has a small chat mark. No link to the topic is drawn: the binding holds no message id, and the repo has no verified Lark link that opens a topic by `thread_id`. Binding is not on the web: it would need the bot's recent topics, which the hub has no route for, so an unbound to-do says that a topic started in a `/topics` group opens and binds a new to-do, and that an Agent can bind it (the note does not name the tool). The web is always a full-role device, so Unbind is always there. A hub with no thread route (a route-level 404 on the unbind, as `isLegacySpacesRouteError` reads it) loses the section without a word.

### Replies in the thread

(#5279, step 3.) A message from a group chat (the bot event's `chat_type` is anything but `p2p`) never takes the bot's private-chat road: no commands (a text starting with `/` is a word like any other), no answering the question card or the "Please verify" card, never the chat's current target. It goes by its topic alone (`assistant/feishu_thread.go`). The hub hands the assistant only the owner's messages in a topic of a group chat the owner opted in (`docs/extensions/bots.md` ("Topic group chats")); the assistant's own rule here is the second line of defense.

**Using it in Lark**: (1) in the Open Platform console give the bot `im:message.group_at_msg:readonly` (every message then has to @ the bot) or `im:message.group_msg` (all messages in the group, needs the admin's review), and add the bot to a group chat with you; (2) send `/topics` in that group, the bot answers that it is on; (3) start a topic in the group for a to-do and bind the to-do to it (the to-do's `thread`: `chat_id` of the group, `thread_id` of the topic, "The binding" above); (4) what you write in the topic is a decision of the to-do and goes to its Agent, and the answer, the progress card and the Agent's questions and permission requests come back into the same topic, where the buttons (Stop included) work for you alone. `/topics off` turns the group off.

- **Only the owner**: the message must come from a user whose open_id is the paired owner's, else it is dropped without a word (the hub checks it too, "docs/extensions/bots.md" ("Topic group chats"); the words go to an Agent that acts on them, so this is not left to one line of defense).
- **Lookup**: the to-do whose `thread` is `{feishu, chat_id, thread_id}` of the message, in memory (`sameThread`). A message outside a topic (no `thread_id`) is left alone.
- **No open to-do** (the topic is bound to nothing, or to a to-do that is done): one short hint in the topic (`assistant-feishu.thread_unbound` / `thread_done`), at most once per topic in 10 minutes; nothing is recorded or sent, unless the message asks for a to-do (["Opening a to-do from a topic"](#opening-a-to-do-from-a-topic) below). A done to-do is not reopened by a reply.
- **Open to-do**: the words are written as a decision, `source = "thread"`, `source_message = "feishu-<message_id>"`, question "Reply in the Lark topic" (like a reply to the "Please verify" card, which records "Review feedback"), choice the text as said (trimmed, clipped as every decision is). The same message again writes nothing and is not sent on again (the unique index on `(todo_id, source_message, question)`; the bot also takes each message id once). Without the database nothing is recorded and the words still go on.
- **Who gets it**, settled before anything is written: the Agent on the to-do that most needs it (`pickTaskAgent`, among the ones running: a closed Agent, offline or interrupted, is not counted), else the conversation that opened it (`origin`, when it still exists), else the main conversation; one the bot's switches do not allow (`agents` for an Agent, `main` for a conversation) is left out. It is sent as the bot's own send (`botSendIn`), so the run follows it like any message of the bot; when a send fails (the machine is gone, the node refuses) the next one is tried. With no one left, or all of them failing, the words are still recorded and the topic is told "noted on the to-do, but not passed to anyone" (just "couldn't send it" when nothing was recorded). The text is one line, "To-do "X": the user's new decision in the topic" (the title escaped like a decision's text), then what was said (`prompts/todo/thread_reply.md`); the line says it was recorded as a decision only when it was (not for a message of only a picture, with no database, or when the write failed). A Persona conversation also gets the note that it was said in a topic (`bot/message.md`). A quoted message and a merged forward are not read in (what others said there is not the owner's words); a merged forward gets "can't read this kind of message". Handoff context shows these decisions as "said by the user in the Lark topic".
- **The answer stays in the topic**: the run keeps the chat and thread in its channel values (`ext.chat`, `ext.thread`, kept with the run), and its progress card, answer cards and notes are sent with `chat_id` and `in_thread: true` (`feishu.post` / `feishu.text`, #5903), and so are the question, plan, permission and terminal-dialog cards of the conversation the run follows (`postInteractionCard`: the card is a reply to the run's message, in the topic; with a private run on the same conversation it goes to the owner's DM instead and the topic is told so in a line, and it goes to the DM when the topic refuses it outright). It makes no agent message (`feishu.cot_create` only knows the owner's private chat), and its cards are not bound as reply targets of the private chat. A topic's run and a private chat's run on the same conversation are not folded into one answer. A private chat's run keeps neither field and behaves as ever.
- **Not posted back**: a decision with `source = "thread"` is never posted into the thread (`postable`): it is already there, and the bot's own post must not come back as a reply. The posts below ask `postable`; and a bot's own message is not the owner's (`sender_type` is not `user`), so both the hub and `threadMessage` drop it before anything is recorded.

### Posting into the thread

(#5279, step 4.) A to-do bound to a Lark topic tells the topic, in one short line with a link back to the to-do (`/todos?id=…` on the hub's `public_url`; none when the hub has no address), when:

- **a decision is noted**: from the app, a card answer, an Agent (`todo_decision_add`) or the outcome on completion. Not one the user said in the thread, and not the words the user wrote in the private chat as the answer to a "Please verify" card (`postable`): what is said in the private chat stays in it.
- **an Agent working on it finishes a turn or needs the user**: the same two events the phone's pushes are made of (`notifyFinished`, after its push decision; `tell`, for a card that is not muted), so what is not pushed (a turn the user saw, a muted conversation, a turn asked in the bot and answered there) is not posted either. A card the topic already knows about gets no line: it went into this topic (its run was asked there: same chat and same thread), or a line there says it went to the DM (a private run of the conversation wins, `interactionPlace`). Otherwise the line says where the card is: in the DM, it only says so ("Agent X needs you, the card is in your private chat"; what an approval asks is not for a group); in another topic, only that the Agent needs the user; with no card at all (the cards switch is off, or the chat does not drive that Agent), the line is the notice and carries what is asked, except for a permission (a command or a path is not for a group): then it only says the Agent needs the user. An Agent with no name is "an agent".
- **the to-do is accepted** on its "Please verify" card (or from the Accept button of a briefing, when the ask has no card), **done** (by the user, or by the hub's judge, with its reason), or **closed because its Agent never started** (with the error). Accepted and done together are one line, the done one.

The line is "New decision: …", "3 new decisions: …", "An agent finished a turn: …", "Agent X needs you: …", "The to-do is done", joined with the others of the same minute (`assistant-feishu.thread_post_*`, in the bot's language). Decisions are quoted as "question → choice", at most three, the rest counted.

- **At most one line a minute per to-do.** The first thing of a quiet to-do waits 3 seconds (`threadPostSettle`), so what comes right behind it (the rows of one outcome, a decision and the closing) is one line; what happens after a line goes out waits for the minute (`threadPostEvery`) to be over and is then one line, "N new decisions: …". The clock and the timer are the poster's own (`threadPoster.now` / `after`), which the tests replace. A restart forgets what waits: it is a notice, not a record. A post that fails is lost, not tried again. An outcome decision that arrives more than the 3 seconds after the closing (the model is slow) is a line of its own, after the minute.
- **Every line has its own key** (`uuid` of `feishu.text`, cut to 50 characters; `feishu.post` takes it too, for a reply as well as a send into the chat, cut to 46; only letters, digits, `:`, `_` and `-` are taken, any other key is ignored as if none was given). Without it the hub keys a reply by its parent and words, so two lines that read the same under one anchor (two finished turns) would be one to Feishu, and the second silently dropped. Calls that pass no `uuid` keep the key they had.
- **Only into a chat the bot may send into.** Before a line goes out the assistant reads `feishu.status` and posts only when the thread's `chat_id` is in `chats`: the hub's own rule for `feishu.post` / `feishu.text` (the owner's private chat and the group chats the owner opted in, `docs/extensions/bots.md` ("Topic group chats")). Otherwise nothing is sent and the todo's id is logged. This closes the gap that `todo_bind_thread` lets an Agent write any `chat_id` into a binding: what an Agent binds is never a place the assistant writes to unless the owner opted that chat in. (The hub refuses the send anyway; this is the check before it.)
- **Where it goes**: `feishu.text` with `chat_id` and `in_thread: true`, as a reply to the latest message the owner said in that topic. The binding keeps only the `thread_id`, and Feishu replies into a thread by a message in it, so the assistant remembers that message (`bots.feishu.state`, `topics`, per chat and thread, 30 days, at most 200) each time a message of the topic reaches it, bound or not. With none known (nobody has written in the topic since the assistant has had this state) nothing is sent and the todo's id is logged; the first message the owner writes there fixes it. If the anchor was withdrawn or deleted the line is not turned into a message in the group: the assistant's call carries `no_detour` (the hub's option on `feishu.post` / `feishu.text`: a reply to a gone parent fails with `feishubot.MessageGone`), and it forgets that anchor, the line is lost, and the owner's next message in the topic is the new one.
- **Nothing for a to-do with no thread**, none for one unbound while the line waited. Not sent: the log says why.
- **Private chat as before**: the reports and pushes (`reports`, `feishuAgentDone`, the cards) go out as ever; a bound to-do's topic is told in addition. Whether a bound to-do should be told in the topic only is left for later.

### Telling the extensions

(#5279, step 5.) The extensions are told of the to-dos too, through two events (`todo.changed`, `todo.decision`; [extensions.md](../extensions/extensions.md) "To-do events"): the ones that subscribed and were granted `todos.read` (the read scope that already lets them call `GET /api/v1/todos`), so a connector extension can post into a thread it owns as the user. Nothing needs a binding: they go for every to-do. `assistant/todo_events.go`.

- **`todo.decision`** is sent from `insertDecision`, the one place every decision is written through, so every source is told (the thread's own too, with `source: "thread"`).
- **`todo.changed`** is not hung on the writers. Whenever the to-dos change (`todosChanged`, what tells the apps) a timer of one second starts; at its end the to-dos are compared with what the extensions were last told of (title, done or failed to start, bound thread), and a to-do created, deleted or different is told once. A new way to change a to-do cannot forget it, and several changes inside the second are one event. The baseline is taken when the to-dos are loaded, so nothing is told of the ones found there.

### Opening a to-do from a topic

(#5279, step 5.) In a registered group chat, a new topic opens a to-do by itself and hands it to the main conversation: the same as a bot that auto-starts a session on a new topic, but the session is the to-do (`assistant/feishu_thread_open.go`). The hub's gate and registration decide which groups get this far; the assistant opens only for the owner's own messages (see "Replies in the thread").

- **Shared path**: the to-do, the binding and the hand-over are `openTodoIn`; a request from another chat ("Where it came from" below) comes in through it too, with the first message posted by the bot.
- **When**: the message starts its topic (`isTopicRoot`: it has a `thread_id`, and `root_id` is empty or its own `message_id`), or it is "/ai" (or the full-width "／ai", any case) followed by a space or the end, in a topic bound to nothing. The command is taken off, and what is left is the first words. In a topic whose to-do is open "/ai" is a word like any other (a reply, a decision); in one whose to-do is done it is answered "reopen it in the app" every time (the 10-minute limit of the hint does not apply to a command), and no second to-do is made. A reply that is not "/ai" in a topic bound to nothing gets the hint, which now says to send "/ai" and a line. Checked on a real topic group with the message API (not the event push; the two carry the same fields): the first message has an empty `root_id` and `parent_id` and a `thread_id`, a reply has both set.
- **What is made**: a to-do titled after the first words (as `task_plan`'s are, the model renames it later), the first words as its `notes` (not a decision: no decision is written), `origin` the main conversation, which also supervises it (`superviseTodo`), bound to the topic, with its task card in the main conversation's feed. Only a text or a rich-text message has words (`openingWords`): the bot's @ at the start is taken off (in a group where the bot only gets what @s it every message starts with one), an @ further in is the person's name when the message's mentions give it, and the placeholders of other types (`[语音消息]`, a shared group or card, a location, an unknown type) are not words. A message with no words (a picture, a merged forward, a voice message, a rich text of only pictures, or only "/ai") makes nothing and is answered "say it in a line of text".
- **Whom it goes to is settled first**: the main conversation must be allowed by the bot's `main` switch and exist; with no one to take it nothing is made and the topic is told why. The binding is made right after the to-do; if another to-do holds the topic by then (`ErrThreadTaken`) the to-do just made is deleted (`removeTodo`, the DELETE route's own code) and the topic is told.
- **The reply**: one line in the topic, as a reply with `chat_id` and `in_thread` — "To-do "X" opened and handed to the main conversation", with the to-do's web link when the hub has an http(s) address (as the bot's cards' links) — then the hand-over: the first words, quoted line by line (so that a many-line message cannot pass for the instructions around it), and the to-do's id (`prompts/todo/thread_open.md`), sent as the bot's own send (`botSendIn`) so the run answers into the topic. A hand-over that fails leaves the to-do (bound, so a later reply goes to the main conversation) and says so in the topic.
- **Running an Agent on it**: the hand-over tells the main conversation to give `task_plan` the to-do's id as the task's `todo_id`. That reuses the to-do rather than making one: it must be one the plan's conversation opened, not done, with no Agent on it (an Agent let go or dismissed does not count) and not held by another plan that has started and not yet linked its Agent. It is checked when the plan is made (an error says why, nothing is made) and again when it starts (the task gets the reason as its `error`). The to-do keeps its title and notes, and no second task card is added. A reused to-do has no `plan_id`, so a failed start cannot be retried from the plan card (the to-do is closed with the error as for any failed start; reopen it in the app).
- **After that** a reply in the topic goes the way those of a bound topic go: the running Agent, else the main conversation (the origin).
- **Risks**: a registered group used as a notebook gets a to-do per topic; and a group another bot works in is answered twice (`docs/extensions/bots.md`).

### Where it came from

(#6146) A to-do opened for a request made in another chat (`openFromElsewhere`, `docs/extensions/bots.md` ("Asking from any group")) records that chat and message as its `source`, and tells the outcome there once it is done or accepted. `GET /api/v1/todos` returns `source`; nothing writes it but the opening.

- **One way in, whoever received the request.** `openFromElsewhere(ctx, Source{Channel, ChatID, MessageID, Text, Quote})` (`assistant/feishu_elsewhere.go`) posts the topic's first message in the home topic group, then runs the same path as a topic the owner started (`openTodoIn`: to-do, binding, main conversation) and records the source. The Lark bot's gate calls it first. **With no home topic group** it does not turn the request away (Lark is one channel, not the shape of Persona): the first message goes into the owner's private chat with the bot, the to-do is made by the same `openTodoIn` but bound to no topic, and the source gets an `anchor`, the id of that first message (`openInOwnerChat`). The hand-over to the main conversation says so (`NoTopic` in `todo/thread_open.md`: the answer goes to the private chat, and what the owner says there later is not recorded as a decision). The text of a message the request replied to is someone else's: it goes into the notes under a label that says so, and to the main conversation as an escaped context block of its own titled as written by someone else, never in the quotation of the user's words. Another channel (say an extension that sees the owner's own chats) calls it the same way, and tells the outcome back itself: it is added to `sourceChannels` and `sendSourceReport` (`assistant/todo_source.go`); a request from a channel that is not there is refused before anything is posted.
- **When it is planned.** When the to-do becomes done (the user, the judge or the supervisor), when its Agent never started, or when the user accepts it on its "Please verify" card or from a briefing's Accept button, whichever comes first. A done to-do waits for its outcome (`todo_outcome.go` reads the Agents' last replies and asks a model): the report is planned the moment the recording is through, or after `sourceReportWait` (3 minutes) with what there is; a start that failed waits a moment (`threadPostSettle`) only. Only a to-do that is done (not reopened since) and was done within the last 7 days is reported. Reopening and closing again makes no second report.
- **What is told: one fixed-shape line, and the owner sees it first.** The original chat is not the owner's own: nothing of the to-do's inside leaves it. The line is the first line of the latest outcome note (the `#o` row `todo_outcome.go` writes, or the receipt's text when no model wrote it), without the links after it, and cleaned (`cleanReport`): no web address or markdown link, no backtick code, no path (words joined by `/`), no markup (`<...>`, an `<at ...>` among it) and no `@`-mention; at most 600 characters; with no outcome note, or nothing (or only punctuation) left of it, only "Done."; for a start that failed only "Couldn't get this done." (three languages). Not told: decisions, the Agents' replies, the judge's reason, the error of a start, the words of the topic. The text is fixed when it is planned: the topic gets a line, "Will be posted under the original message in 10 minutes: <text> (send /noreply to cancel)" (a to-do with an `anchor` has no topic: the same line goes into the private chat as a reply under the anchor, ending "reply to this with /noreply to cancel"), and the report goes out when the 10 minutes (`sourceReportDelay`) are over. The text and the time are kept with the todo (`source.report_text`, `report_due_at`), so a restart in between keeps both. If the topic cannot be shown the line (the todo was unbound from it, or nothing in it is known to reply under) nothing is planned. A to-do unbound from its topic by the time the report is due tells nothing (there is no place left to say `/noreply`); one with an `anchor` is not affected, its notice is in the private chat. The claim is made in the store: it checks that the planned time kept there has come and returns the planned text kept there, which is what is sent, so an assistant whose own copy of the plan is stale (another one planned it afresh) neither tells early nor tells an old text.
- **Once, and retried only if it may pass.** `source.reported_at` is written before the send (a conditional update, so two assistants on one store cannot both win) and the send carries a key made of the to-do and the message, so Feishu drops a repeat. A look at every to-do with a report to make runs when the todos are loaded and every 10 minutes (`sweepSourceReports`): it plans what is not planned, tells what is due, and retries a try that failed for a reason that may pass (no answer from Feishu, a rate limit, a server error): the claim is given back, and the hub allows a source two replies, so a report has two tries in all (`source.attempts`). A report that can never go (the hub refuses the chat, the original was withdrawn or deleted, Feishu says no) or has used its tries keeps its claim, is not tried again, and the topic is told "Couldn't post the outcome where it was asked: <why>". One that could not be told because the bot was not ready claims nothing and is found by the next sweep. A to-do reopened between the plan and the time cancels it (the text and the time are dropped, `ClearTodoSourcePlan`); closed again, the text is worked out afresh, the topic is shown a new notice, and the 10 minutes start again, so what goes out is always the last text the topic was shown.
- **Don't report back.** "/noreply" (also the full-width slash, any case, and nothing else in the message besides the bot's own @, which a group that only sends what @s the bot starts every message with) said in the topic sets `source.no_report`; said in the 10 minutes after the notice it cancels the report. Decided by the text alone, never by a model; recorded as a `source = "thread"` decision of the to-do ("Don't report back to where it was asked"); not passed to any Agent; works while the to-do is done and not yet told. After the report went out it says so. On a to-do with no source it says there is nothing to stop. The topic's first message says it. A to-do with an `anchor` has no topic to say it in: the owner replies `/noreply` to the anchor (the bot's first message in the private chat), to the notice, or to any answer under them (the reply's `root_id` or `parent_id` is the anchor); it is recorded as a `source = "manual"` decision ("Reply in the Lark chat") and not passed on. A `/noreply` in the private chat that replies to none of those is an ordinary message.

## Record conclusions on completion

Step 3 of the three-loop process (#2820): Agent conclusions automatically settle into the to-do/knowledge base, without user opening Agent; next dispatch for the same project includes them (`server/internal/assistant/todo_outcome.go`).

- **When**: main-chat-dispatched task (nonempty `origin`) becomes done: main-chat acceptance (`accepted` in `task_review`, `reviewDoneTodo`; all main-chat tasks are supervised, awaiting acceptance rather than self-closing), automatic completion (`autoDoneTodo`, unsupervised tasks), or user marks done (`done: true` in `PATCH /api/v1/todos/{id}`). Excludes manual to-dos, tasks ended by launch failure and review-cancelled tasks (`cancelled`). Runs in background, not slowing completion.
- **Extract**: read last-turn replies from all still-linked Agents (fallback session-list `last_text`; ignore model-service-interrupted turns), use model (`kb.project`, prompts `server/internal/prompts/todo/outcome.md`, `outcome_input.md`) to extract one-sentence conclusion (at most 120 characters), PR/deploy links (at most 3), decisions and lessons (at most 5 each); invent nothing not in replies. Reply reads get at most 1 minute, model 90 seconds, decision/timeline writes another 45 seconds, so a slow model does not delay writes.
  Without model (unconfigured, quota, unparsable reply), conclusion falls back to receipt text (latest Agent reply compressed to one line, at most 120 characters), links only GitHub PRs from replies, no extracted decisions/lessons.
- **Decision section**: record conclusion ("Completion conclusion" + conclusion/links), each decision (question/choice/reason), each lesson ("Lessons learned") separately, `source = "outcome"`, provenance latest Agent (`source_machine` / `source_session`).
  Once per completion: `source_message` is `outcome:<to-do id>:<completion time>#<index>`; duplicate triggers (two paths together, again after hub restart) check database first. Write conclusion first; only successful writer proceeds with decisions, lessons and timeline (one wins with two assistants sharing database); deleted to-do gets no writes. Reopen then complete is a new occurrence. Web provenance says "Recorded when Agent finished"; handoff includes these in established decisions, marked "Recorded when Agent finished, not decided by user".
- **Knowledge base**: project for each Agent working directory (`kb.Engine.ProjectsForWork`: first registered code `dirs`; if none match, match directory name to projects with no registered code dirs by name/slug/alias, never name-match projects with registered dirs),
  append `结论：<conclusion> <at most two links>` ("Conclusion: …") to `log.md` (at most 200 characters per line: keep links whole, truncate conclusion to remaining space), source `待办 · <title>` ("To-do · <title>", remove ` · ` from title, at most 60 characters), one line per project. Not limited by `kb.agent_turn`'s one-per-Agent-per-30-minutes rule. If knowledge base not open, retry once after 30 seconds; otherwise only record decisions.
- **Project context at dispatch**: for a new Agent, resolve project by target directory as above, prepend project summary and latest 5 `待办 ·` conclusions (newest first; only source beginning `待办 · ` and body beginning `结论：`, not same-named group-chat records) as `<persona-context kind="context" title="项目 · <name> · 背景">` ("Project · <name> · Background", prompt `server/internal/prompts/todo/project.md`); at most two projects per directory. Send unchanged without project, both summary/conclusions absent, or knowledge base closed.
  - Assistant `task_plan` (`startTaskAgent`, retry includes decisions too), `agent_start`: prepend directly; tab names still use original title/prompt.
  - App **Hand to Agent → New** through hub: `POST /api/v1/sessions` with `todo_id`/`prompt` asks assistant (`assistant.todo_handoff` with `new: true`, `machine_id`, `cwd`; expand `~/` and empty cwd using that computer's home), project context before decisions.
    Clients' Claude starts in background then sends first message (`async`, empty prompt): hub remembers "session newly created for this to-do" (memory, 15 minutes), treats its first message as new Agent's first input with context/decisions; recognizes iOS / Android first messages even without `todo_id`. Forget only after successful send; failed sends (rejected/nothing entered) retain context on retry; answer-only devices do not count. If hub restarted between, send unchanged.
  - Handoff to an existing Agent (message) includes decisions only, not project context.

Not yet: manual editing/undo of conclusions (currently **Replace** in decisions), Lark reporting (#2768), iOS / Android decision lists.

## Due date, reminder, priority

#1683. A to-do can be due at a moment (`due_at`) or on a day (`due_date`, read in the user's time zone: due by the end of it), have a reminder (`remind_at`) and a priority. The assistant's `remindLoop` (`todo_plan.go`) sleeps until the next reminder (at most a minute, woken when the to-dos change) and pushes each one once when it is due:

- It claims the reminder in the store first (`reminded_at` set only while `remind_at` is still that moment, not yet pushed and the to-do not done), then pushes it like `notify_user`: to every device (opening the main conversation) and to the Feishu bot and the channels with pushes on. A claimed push that is lost is not retried; a reminder is never pushed twice, by a restart or a second manager.
- The push's title is the to-do's, its body the due date (or "was due" once it has passed), the first line of the notes, and when it went out more than two minutes late (the manager was down), the time it was for.
- A done to-do is not reminded; changing `remind_at` re-arms it; a mesh member on standby leaves it to the center, as with scheduled tasks.

## The assistant's todo tools

#1683. The assistant has the user's to-dos as MCP tools (`todo_tools.go`, descriptions in `prompts/mcp/todo/`), so it can note what the user asks it to, or a batch of deadlines from a mail, without a hub token:

| Tool | What it does | Confirmed on the phone |
| --- | --- | --- |
| `todo_list` | Open to-dos by default, earliest due first (none last), filtered by state, due before / after, overdue, with or without agents, words | — |
| `todo_get` | One in full: notes, agents, the task it is, decisions in force | — |
| `todo_create` | `title`, `notes`, `due` (a day, a moment in the user's time zone or RFC 3339, `2h` / `3d`), `remind` (a moment, not past), `priority`; an open to-do with the same title and due date is returned instead of made twice | No |
| `todo_update` | Title, notes, due, remind, priority; `""` clears | No |
| `todo_complete` / `todo_reopen` | Mark done / open again | Only for a task, or a to-do agents work on (completing closes its agents) |
| `todo_delete` | Delete for good | Always |

A to-do it makes is the user's own (no `origin`): work for an agent still goes through `task_plan`. In a health conversation, anything that writes asks first (`confirmOutbound`, `health.md`). The generated CLAUDE.md tells the assistant to use `todo_create` when the user says to note a to-do or to remind them of something.

## Interaction design across clients

### 1. Sidebar and main navigation
- Sidebar has a **To-dos** entry with unfinished-count badge.

### 2. To-do list and details
- List: search bar, unfinished list, collapsed completed group at bottom. Each row shows title, state dot, linked-Agent count and latest message from the highest-priority Agent. Tap circle or swipe left to mark done.
- Details: edit title/notes directly (iOS: notes can be dictated with the composer's mic, in details and in the New To-Do sheet, #2188); current state; linked projects (whole-project links) and active Agents (state dot, title, machine/workspace labels, latest-message summary, update time, tap to chat; beyond session time window use snapshot and mark "Ended"; launch failures show reason/removal); collapsed "Previously involved" list; persistent bottom **Hand to Agent** and **Done / Reopen** buttons; delete in top-right more menu.
- **Decisions** section under notes (web): effective decisions chronologically (question, choice, reason, time, provenance), each can **Change decision** (write a replacement) or ⋯ **Which decision did this replace?** / **Undo replacement**; when superseded records exist, **View full history**, with old records struck through. Hide whole section if hub unsupported (route 404).
- **Projects** section above Agents: each row "project · machine", with **Remove**; projects without online Agents show "No Agents currently": only sessions in current hub group, on connected computers, not offline count (same-ID computers in other groups do not; disconnected computer sessions marked unknown by hub rather than offline do not count either, matching web). Agent source says "With project <name>" or "Dispatched by <parent title>"; missing project source shows only "With project", missing parent only "Spawned", manual links unlabelled.

- **Due date, reminder, priority** (#1683): under a row's status, a flag for high priority, the due date ("Due Thu, Nov 12, 18:00", or the day alone; red and "Was due" once it has passed) and a bell while a reminder is still to be pushed. Open to-dos are ordered with what needs the user (needs you, unread; on the web also interrupted and awaiting review) first, then by due date, earliest first, then those without one as before (iOS / Mac `TodosLogic.sortTodos`, web `sortTodos`, Android `ordered`). The detail edits them: iOS / Mac a section of switches and pickers under the title (due date with All Day, Remind Me, Priority), saved shortly after each change (`TodoPlanSection`); the web a row of date / time / datetime inputs and a priority menu (`TodoPlanEditor`); Android shows the line on the list only so far. Checked by `TodoPlanTests` (iOS UI, mock hub) and `scripts/test/web_todo_plan_e2e.py`.

### 3. Hand to Agent
- **Start a new one** or **Hand to an existing one**.
- Context follows Inbox's hand-to-AI format: `<persona-context kind="context" title="待办 · <title>" url="">notes</persona-context>` (title means "To-do · <title>"), immediately followed by user's task instructions.
- New session carries `todo_id`; message to existing session carries `todo_id` and `client_msg_id`.

### 4. Agents view integration
- Agents grouping supports **By To-do** (#1836; iOS / Mac defaults by repository when no grouping chosen, #2108): each unfinished to-do with active Agents is a separate section; header shows title/state and opens details. Unlinked manually started herdr Agents (`started_by_persona` false) have a separate expanded **Manually started** section (web: **Started in terminal**, #3057), before **Unassigned** (web: **Not linked to a to-do**; web #2608, iOS / Mac #2917: without spawn relationships or linked space no automatic rule recognizes them; link through **Link to To-do…** in row menu; spawned herdr Agents whose parent is not linked cannot be distinguished on any client and count here). Remaining unlinked Agents go in final **Unassigned** section (collapsed by default when to-dos exist, expanded if any Needs you). Web remembers each browser's grouping, keeping whatever was chosen (default By To-do if never chosen, #2812; By Machine before, #1990). When list contains multiple host types, non-herdr rows label their host.
- Web **By To-do** is a three-level tree (#2526): project › to-do › Agent. Project comes from linked Agents' repository (same `repoOf` as By Repository, merged across computers; choose repository with most Agents if several); spawned Agents (`source = "spawned"`, `via` points to another Agent in same to-do) nest beneath parent, staying top-level if parent absent or cycle. Each node has state "Needs you / Working / Done", highest priority below it (Needs you > Working > Done, interrupted counts Needs you). Completed to-dos and done branches with last activity over 1 day ago default collapsed; Manually started/Unassigned last, grouped by project inside. Unfinished to-dos on left of To-dos page also group by project, no-Agent items last. Android By To-do also uses project › to-do › Agent (#3156), without spawned indentation/one-day folding yet.
- Source label on every Agent row (#2526): botmux **Lark Group**, tapping opens a thread link in Lark when present; herdr **Terminal**; other hosts unlabelled. Quick filters add Lark Group/Terminal for just that type. iOS / Mac have labels/filters too; Android too (filters #3154, labels #3132, labels under time, #3043). The web draws it as an icon at the row's end instead of a word (`docs/web/web.md` "Source marks").
- Other grouping modes add to-do-title labels to linked Agents.
- By-project headers show linked to-do labels opening details; menu **Link to To-do…**, **Move to Another To-do…**, **Remove from To-do**; picker selects unfinished to-do or creates then links. Agent groups outside herdr workspaces (no workspace id) have no labels or these menu items.
- Hide whole-project entry before list loads or when nonempty list has no `spaces` arrays; successful empty list shows it. Preserve prior support state during refresh/ordinary error; reassess after successful refresh/reconnect. Hide entry and ask for hub upgrade on whole-project API route-not-found 404; other 404s are ordinary errors. Old hub responses/local caches still decode. Picker resets input whenever opened or target project changes; session's own `todo` marker is authoritative.
- Agent quick menu (web, iOS / Mac, Android): unlinked **Link to To-do…**; linked **Move to Another To-do…** and **Unlink**. Relink directly to new to-do; hub releases old association simultaneously (#2348).
- Chat top-right more menu and session-info popup show "To-do: X", tap to open; web also shows it below top title with **Change To-do…** (#1836).

### 5. Siri and Shortcuts (iOS / macOS)
- **New To-do** (`AddTodoIntent`): say "Create a to-do with Persona" or use in Shortcuts / Action button; title required, notes optional, writes directly in background to the App's currently selected hub group, without opening App. See `docs/apple/siri.md`.

## Design trade-offs and reasons

1. **Not another Lark Tasks or Reminders**: Persona to-dos drive and track Agent workflows, focusing on which Agents share one task and their progress, not complex subtasks, deadlines, priority trees, calendar reminders or multi-party collaboration.
2. **No stored state**: overall state derives entirely from linked Agents' live runtime states (questioning, working, finished unread, etc.). Agent state changes frequently; derivation avoids dual-write consistency problems and large synchronization overhead.
3. **One active to-do**: `UNIQUE (machine_id, session_id) WHERE released_at IS NULL` ensures each Agent serves only one current to-do at a time. Assigning other work automatically marks the previous task "Previously involved", keeping context and execution goal clear.
4. **Automatic completion only for chat-dispatched tasks, judged by model**: these have explicit requirements (dispatch prompt) and deliverables (Agent's final reply), so requiring individual user completion taps is a burden; manual to-dos often represent the user's own plans, whose completion hub cannot know. Stopped is not finished, so do not close by state alone; judgment model decides, leaving open when uncertain. Wrong auto-close (user thinks finished) costs much more than missed close (one extra tap), hence reversible, reasoned, and not reclosed by the same work after reopening.

## Known limitations

1. **Direct renames inside herdr**: renaming workspace in herdr terminal rather than hub API acts as a new project; previously linked Agents remain, but later Agents in that workspace will not auto-link.
2. **Marker overwrite across multi-hub groups**: a computer's persistent node serves only one long-lived connection at a time. If independent hubs in different linked-hub groups connect alternately to one node, later hub's expected markers overwrite earlier hub's.
3. **Empty-workspace rename does not migrate**: when renaming through hub with no active sessions, hub cannot resolve old workspace name; `todo_spaces` does not automatically migrate, like pin/mute.
4. **`/clear` recreates session**: Agent `/clear` creates a new session uuid underneath; new session does not auto-link to old to-do, and original session row stays listed.
5. **Assistant disconnected**: to-do APIs and create/send with `todo_id` return 503; workspace renames through hub do not migrate and answered cards are not recorded as decisions ("Who owns what").
