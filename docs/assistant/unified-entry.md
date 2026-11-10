# Unified main-chat entry point (#930)

Complete all work in the assistant's main chat: say what you want, and Persona breaks it into tasks, assigns suitable Agents, watches progress, and interrupts only when it needs you.
Personal information (calendar, reminders, email, Lark) and work Agents share this entry point. Many tasks can run at once, and the conversation can continue indefinitely.

Interaction prototype: `docs/assistant/prototypes/unified-entry.html`. Open it directly in a browser; `#step=5&theme=dark` jumps to step 5 in dark mode.
The prototype is the UI and interaction standard; compare screen by screen during implementation.

## Boundaries

- **Do not change the Agents page or the top-bar "Assistant | Agents" switch at all**, or move them behind any secondary entry point (requested 2026-11-03). Tasks started from the main chat are ordinary
  Agent sessions that appear normally in Agents; both sides share the same data. This feature changes only the assistant side.
- No separate task system: **a task is a to-do** with attached Agents (`docs/assistant/todos.md`), plus a record of which chat created it.
- No second Lark client or source-specific browsing pages (`docs/extensions/feishu.md`): messages appear in the chat only in relation to tasks.

## Three layers

| Layer | What it is | Data source |
| --- | --- | --- |
| Chat stream | Your conversation with Persona plus hub-inserted system entries (plan cards, task cards, receipts, answered questions, messages routed to tasks), all ordered by time | Transcript + chat activity (see "Chat activity" below) |
| Task dock | A row of pills above the input bar: working / waiting tasks started from this chat; in the main chat, manually started working / waiting Agents follow the tasks. "All N" at the far left (text only) opens the task board | To-dos + attached Agent state; manual Agents come from the Agents page's session list |
| Needs you | When a task's Agent needs approval or an answer, a question card appears at the bottom of the chat for an inline response | Existing interactions (`Interaction`) |

## Interaction specification

### Dispatch: express intent, do not choose a project

- Persona splits multiple requests in one sentence into tasks and passes them to hub through `task_plan`. Each task includes its inferred computer, directory (project), launcher / model, permissions,
  and a one-line rationale. Inference uses knowledge-base project-to-directory mappings, the computer recently working in that directory (`agents_list`), the default launcher, and your past choices.
- Three cost tiers determine whether to show a plan first:

  | Tier | Examples | Behavior |
  | --- | --- | --- |
  | Instant | Add a reminder, create an event, check weather | Not a task; Persona calls phone / mail tools directly. Render a result card (with Undo) in the chat, without adding it to the dock |
  | Auto-start | Read-only: research, read code to answer a question | `task_plan` queues dispatch with `permission: plan`; a launcher unable to honor that mode fails to start. The plan card collapses to "Started N tasks" |
  | Plan first | Change code, start multiple Agents, first start in this project | Show a plan card in the chat, with editable labels for each item; start only after "Start" |

- Tasks you explicitly request in the chat (a direct instruction, "have an Agent do it," "go ahead and start," #2559) also start directly. Persona sets `user_requested: true` for that `task_plan` item, and the card shows progress only.
  Inferred tasks you did not request still wait for Start. If any item in a plan needs to wait, the whole plan waits. "Start tasks I request in chat directly" under Settings › Assistant (`assistant.start_requested_tasks`, on by default) can disable this.
- The plan card offers "Start These Directly," remembered per "project × read-only / file changes." Similar tasks no longer ask. Stored in settings `tasks.trust`; entries can be viewed and deleted in Settings.
- Read-only tasks always retain `permission: plan`, including after card edits and on retries. Health-space plans still wait for Start. Runtime support and the current card labels are described in [To-dos](todos.md#read-only-planned-tasks).
- `task_plan` **does not block**: once the plan is handed to you, the turn ends immediately and you can continue talking about something else. When you tap Start, hub starts the Agents directly; Persona sees the result in the next message's live snapshot.

### Progress tray: one task, one row (#2878)

The approved simplified Assistant presentation places a task's **only card in the Progress tray**, not in the conversation.
Planning, running, blocked progress, PR references and acceptance update that same task row in place. Tapping the row opens
the existing task details. The dispatch answer has only "Dispatched N tasks · See Progress", which expands the tray above
the composer. Historical plan/task/receipt/review/report cards use the same lightweight entry or are omitted from the chat.

**Pending** holds plans that still require Start and acceptance decisions, alongside ordinary questions/approvals. Drafts sent
on the user's behalf are a separate group and require their own explicit Send; Approve all never sends drafts or chooses an
answer to a question. **Progress** also has a separate quiet supervision/scheduled-task group, with inspectable background
records. System housekeeping notices are not conversation messages. Tools and intermediate narration stay manually
inspectable under a single collapsed summary; source metadata, not title/emoji guesses, identifies background turns.

The shared feed API adds stable `task_cards` upserts (fields and compatibility rules in
[the protocol](../protocol/protocol.md#tasks-and-the-chat-feed)). The original event log remains unchanged. Live task/session
state still updates the row even when no new feed event is written. Android implements the conversation projection and
all three trays in #2878; web implements the unique Progress row, dispatch hint and relocated planning/acceptance actions,
retaining its existing turn-source presentation. Apple's independent client work can consume the same additive contract.
The older card-oriented descriptions below document
the legacy presentation and the retained detail actions; they do not require duplicate cards in the simplified chat.

### Task cards

- Once a plan starts, its card collapses to "Started N tasks," followed by N task cards. Task cards **update in place**: state (spinner / orange dot / green check), title,
  "computer · project · model," the current step (the Agent's latest message or a summary of its latest tool call), progress bar, and elapsed time.
- **Current step** (#1108): when an Agent keeps calling tools without writing text (reading code, testing, waiting for CI), `last_text` can remain unchanged for minutes to tens of minutes
  (2026-11-04 statistics from two days of transcripts on one machine: 6069 segments with ≥3 consecutive tool calls between text blocks, median 84 seconds, p90 about 6 minutes, p99 about 20 minutes), making the card look stuck.
  Node therefore extracts the latest tool call from the transcript and summarizes it in one line (`transcript.Activity`: "Running tests," "Reading tasks.go," "Waiting for CI," "Opening a PR," etc.),
  delivering `activity` / `activity_at` with the session list (only when the tool call is newer than the last text block). While the Agent works, the card shows that step and its duration (redrawn every 30 seconds);
  new text replaces it, and once the Agent stops, the last text block is shown. Client rules live in `TaskNow` (iOS), `taskNow` (web, `web/src/lib/todos.ts`), and `TaskNow` (Android `core:domain`).
  All three clients and the snapshot use the same rules. An Agent that has never written text and stopped on a tool call also shows that step (without a timer), not "No activity yet."
- The last message must not disappear because the transcript is too long (#1583). Node reads only the final 384 KB. After an Agent writes text and continues calling tools, tool results and hook receipts quickly exceed 384 KB,
  pushing that text out of the window and leaving `last_text` empty (observed: a 1.5 MB transcript, last text at 665 KB, card showing "No activity yet" for 7 minutes).
  Now, if the tail window contains no text, reuse the last text read (the file is append-only; newly written data since the last read is in the window). With no prior value, search backward in chunks, up to 8 MB.
- Running sub-Agents (`running` in `subagents`) appear on a separate "Sub-Agents running: A, B" line as supplementary information. The main line always shows the main Agent's own text or current step.
  Sub-Agent transcripts are separate files (`<session>/subagents/agent-*.jsonl`) and never enter the main Agent's `last_text` / `activity`.
- Snapshot tasks (`snapshotTasks`) also include the latest text, current working step and duration, and running sub-Agents: "Working, <machine> session_id=…: latest text; now running tests (3 minutes); sub-Agents running: …".
- Link latency (measured on the hub machine 2026-11-04): Claude Code writes a line in 0.1–3 seconds; node rebuilds the list every second and rereads a growing transcript at most every 3 seconds (`metaMinInterval`).
  List changes (including new `activity`) are pushed to hub immediately, hub broadcasts directly, and web replaces on receipt. There is no model-generated summary in this path: cards and snapshots use original text; completion receipts also truncate the Agent's final text block.
- Cards read sessions through `AppModel.sessionWithLine`, not `session(_:)`. To reduce redraws, the latter ignores pushes changing only `last_text` / `updated_at` / `rev`,
  which would leave the card stuck on the message from the last state change (`docs/lessons/ios.md`).
- Progress **does not insert new chat messages**. Only three cases append at the bottom: needs your answer (question card), answered (collapsed line), finished (receipt).
- Card footers offer "Message" (targeting this task; see "Routing" below) and "Stop" (interrupt, with confirmation first). Tapping the card opens task details.
- When the to-do has a PR URL (`pr_url`, #2768), the card adds a "PR #N" line under the current step; tapping it opens the PR in the in-app link sheet, matching "Open PR" on the Feishu task report card (#5033). Cards without a PR are unchanged.
- Task cards from previous days collapse to one line (state dot + title + result); tap to expand.

### Needs you

- When a task's Agent has a pending interaction (question, tool approval, plan approval), a question card appears at the bottom of the main chat. Its header names the task; below it is the current
  `InteractionPanel` content, answered inline. The corresponding task card gains an orange border, and its dock pill becomes "xx · Waiting for you," sorted first.
- Tapping an orange pill scrolls to the question card and briefly flashes its border.
- Multiple waiting items appear at the bottom in arrival order. Answered cards collapse to a line such as "✓ Login crash · Approved," retained as a chat record (`answered` chat-activity entry). Tap to expand the full "question → answer" (multiple questions listed separately, selectable and copyable), tap again to collapse, and tap the task name to open task details (#1231).
- Pushes remain unchanged: the title is the task name; opening the notification goes to the main chat at the question card.

### Receipts

- When a task finishes (an attached Agent ends a turn with no pending interaction), append a green receipt at the bottom: task name, what was done (summary of the Agent's final text block),
  number of changed files, tests, PR, and next actions below: review changes, merge, tell the person who asked, etc.
- Hub generates receipts without waiting for Persona to reply. Persona sees the event in the next message's snapshot and can continue if needed.
- When sending a receipt, hub also uses the judgment model to check whether the task is really done (stopped at "not finished" or "waiting for review" does not count). If complete, automatically mark its to-do done (`done_by = auto`, reopenable); see `docs/assistant/todos.md`, "Automatic completion" (#1092).
- Agent failed to start (#1077): if the machine connection closes (`rpc: connection closed`, temporarily offline), retry once after 3 seconds; fail if it still does not work.
  On failure, hub ends the task's to-do (`done_at` + `start_error`), records the reason in the plan card, and appends a red "Didn't Start" receipt.
  The snapshot reports "Didn't Start"; Inbox and Today cards no longer treat it as working. The App's task card turns red with "Didn't Start" and a direct Remove action; it does not enter the dock.
- Removal: any non-running task (failed to start, finished, stopped, completed, or "Starting" that hub never reported back on) can be removed, following the Agents page's Close interaction.
  Long-press a task card / pill / collapsed row for "Remove task," swipe left on the task board for Remove, or use ⋯ at the top right of task details. Removal deletes the to-do.
  Confirm first if it ever had an Agent (the Agent and transcript remain in Agents); a task that never started is removed directly.
- Web behaves the same (#1142): failed tasks are red with "Didn't Start" and Remove on the card. Right-click a task card / pill / collapsed row for "Remove task…," or use ⋯ at the top right of the task panel.
  Confirmation uses browser `confirm()`. "Starting" tasks left by older hubs are also recognized as "Didn't Start" from the error recorded in the plan card.
- Retry (#1396): failed-start cards have Retry beside Remove, also available in task details. Hub retries that task exactly as specified in the plan
  (machine, directory, prompt, launcher / model, permissions, title), using the same to-do: immediately reopen it, clear the error (including red text on the plan card), and change the card in place to "Starting."
  No extra dock item is added. Repeated taps start only once (hub uses a conditional to-do update to block the second attempt, and clients send no further request until the first returns). If the target machine is offline, reject directly,
  saying "‘Computer’ is offline; retry when it reconnects," leaving the task unchanged. A new failure ends the to-do and writes the new error as usual, but adds no second receipt:
  the failure receipt follows the to-do, showing the latest error and Retry while it has not started, then collapsing to gray "Restarting" / "Started after retry" once reopened. Tap to open the task.
  Web says it only once (#4145): when the chat contains the full task card (today's task), only that card shows the cause and Retry / Remove; the plan card no longer repeats red text below it.
  The failure receipt collapses to one red line, "‘xx’ failed to start; see the task card above" (tap to open the task). Without a full card (earlier-day tasks shown only as a line), keep the cause and Retry as before.
  API: `POST /api/v1/todos/{id}/retry` under "To-dos" in `docs/protocol/protocol.md`.

### Routing

- By default, messages go to Persona, which decides whether to answer, start a task, or forward to an existing task with `agent_send`. A follow-up that only asks about work a task has already done (how far it got, its conclusion, a PR link, why it stopped) is answered from the snapshot and `agent_read`, not sent to the agent: a send queues behind its turn or resumes a closed agent, and the user waits for that extra turn (#2124). The snapshot's line for tasks and the `agent_read` tool description say the same, so the assistant is not pointed at `agent_send` there. When the input contains text, a gray label above it says "Persona decides who handles this."
- To speak directly to a task, type `@` for a list (tasks, computers, projects, people) and select a task, or tap Message on its card. A purple routing label appears above the input,
  "To ‘xx’ ×." The App sends directly to that Agent, bypassing Persona. The chat bubble carries the same label (`routed` chat-activity entry).

### Messages to agents (#1542)

What Persona itself sends to a task's agent (`agent_send` with `task`, `agent_interrupt` with `then`, a supervision rework) is
also a `routed` entry, marked `from: "assistant"`. It is never drawn as the user's bubble: those instructions run to dozens of
lines, and in the user's own color and alignment they read as something the user said.

- **On the task's card**: a row "N instructions sent" lists every message Persona sent to that task (time and first line);
  tapping one opens its whole text with Copy (the web's text is also selectable). Tapping these rows does not open the task.
- **The hand-off sent with the card** (Persona's message to the same task in the same conversation at most 60 seconds after
  the card's `task` entry; `agent_send` with `task` writes the card and then the text) is drawn only on the card, not as a line.
  Only a card drawn in full (from today) takes it in: a task card from another day is a one-line row with no list, so its
  hand-off stays a folded line under it (#1668).
- **Any other message of Persona's** (a rework, a later instruction, one whose task has no card in this chat) is one folded
  line on the left, styled like the answered and review lines: a send icon (a stop when Persona cut the turn short first),
  "Sent to ‘xx’" (tap to open the task) and the first line; tapping the line opens the whole text with Copy.
- What the user sends to a task (its card's Message, or the composer's routing label) has no `from` and keeps the bubble.
- Older entries: migration `0441_feed_routed_from.sql` marks those Persona wrote before the field existed (see "Chat activity").
  Older apps ignore `from` and keep drawing the bubble.
- Apple: `AgentSentLine`, `TaskSentList` (`apple/Persona/Views/Assistant/Tasks/SentViews.swift`), the pure rules in
  `SentToAgents` (`apple/Shared/Tasks.swift`); web: `web/src/views/assistant/Sent.tsx`, `takenIn` / `sentTo` in `web/src/lib/feed.ts`;
  Android (#1623): `SentLine`, `TaskSentList` in `android/feature/chat/.../AssistantTasks.kt`, the pure rules in `SentToAgents`
  (`android/core/domain/.../tasks/SentToAgents.kt`), the chat page filtering the feed with `SentToAgents.shown`; checked by
  `SentToAgentsTest` and `SentToAgentsShotsTest` (every style, light and dark, folded and opened).
  Mock: `MOCK_TASKS=1 MOCK_TASKS_SENT=1 MOCK_TASKS_NO_ASKS=1` (a hand-off after its card, a rework to an older task, the
  user's own word); UI test `SentToAgentTests`.

### Task details

- Tap a task card, dock pill, or Review changes in a receipt to open task details from the bottom, like link cards (`LinkSheet`): initially full-screen;
  pull down to 0.6 so it no longer blocks the main chat (scrolling and typing remain available), then pull farther to close.
  At regular width (iPad, unfolded iPhone Duo), do not float a sheet: the system sheet is a small centered form there. Push task details onto the chat's stack, filling the right detail column,
  like opening an Agent from the Agents left column. ‹ at the top left returns to the chat; switching chats also dismisses the details (#2816, `detailPresentation`).
- On phones, floated details offer "Full Screen" beside ✕ at the top left (#3743). Dismiss the details and push the Agent page onto the assistant page's stack, using the same page as opening it from the Agents tab.
  Back (‹ or a right swipe from the left edge) returns to the main chat without reopening the sheet. Use a push, not `fullScreenCover`: Agents already uses a push, giving the same back gesture
  and stack-based top-bar / shell hiding (`atRoot`). A full-screen cover requires a custom close control, its downward dismissal conflicts with chat scrolling, and presenting it over a sheet returns to that sheet on dismissal.
- The page is this Agent's chat (`ChatView(style: .dev)`, with everything available in Agents). The top has only one layer (#3255; the user found the previous
  "task header + session header + same-space tab bar" too complex). The embedded chat header is the sheet header (`HostedChat`): ✕ on the left (‹ when pushed into the detail column),
  task name in the middle, with "state dot · state · computer · project" below; goal and loop / background tasks are small icons beside the title; ⋯ on the right.
  The top of ⋯ belongs to the task: if session title and task name differ, show the session title as a section heading, then "Remove Task…" and "Open in Agents," followed by all the usual chat-page ⋯ actions.
- Opening from the main chat shows only this Agent: no tab bar for other Agents in the space and no switching here. To see others in the same space, use "Open in Agents"
  (dismiss the sheet, switch to Agents, and open this Agent with the normal tab bar).
- If a task has multiple Agents, add ⌄ beside the title. Tapping the title lists the task's Agents for switching (plus "Session Info"; web does the same, #3333).
- Without an Agent (starting or failed to start), there is no chat page; the sheet uses its own header: state, task name, ⋯, Close.

### Task board

- Tap All at the far right of the dock. Group by **Waiting for you / Working / Completed today / Scheduled**. Each row has a state dot, title, and "computer · project · latest message."
- This is the to-do list ("tasks are to-dos"); future to-do entry points also lead here.

### Chat structure

- Group by day, with a date line above each day's first entry.
- The morning briefing (scheduled task "Morning briefing") posts to the main chat as a Today card: tasks completed overnight, calendar events, messages needing replies, reminders, with an action per row.
- For Inbox messages related to active tasks, Persona drafts replies using task progress and shows a message card in the chat (quoted original, linked task, draft, Send).

### Quiet tasks (#1731)

Settings › Assistant › "Notify main-chat tasks only in the main chat" (`main_tasks_quiet` in settings `assistant`, `assistant.main_tasks_quiet` in `persona_settings`, **on by default**).
When enabled, tasks dispatched from the main chat (`task_plan`; also `agent_start`, below) are **quiet**: to-do `quiet: true`, set at creation and stored in `todos.quiet`. Later setting changes do not affect existing tasks.

- **No interruptions**: hub sends no task pushes for their Agents (completion, needs you, errors), no `notify` events, and no Lark bot / channel cards. They do not enter Live Activities / Dynamic Island (`liveActivities.snapshot` skips them).
  Session lists mark their Agents `quiet: true` (retained even after task completion). Clients exclude them from global badges: web sidebar Overview / To-dos, Android Agents tab and To-dos drawer, and Android notifications (also suppressing ones from older hubs).
  Agents page, main-chat task cards, dock, question cards, and live snapshots remain normal.
- **The main chat speaks instead**: on completion / interruption (`taskTurnEnded`), failed start (`startPlanTask`), or waiting for the user (hub emits `interaction.raised`), the assistant injects a notice into the main chat
  (`prompts/assistant/task_notice.md`: "🔔 任务「xx」做完了（电脑）"—task "xx" finished (computer)—plus the Agent's last text or question, and an explanation that this is a task notice and the assistant decides how to tell the user).
  The main chat replies accordingly, with its reply pushed to the phone normally. This notice is not rendered as a user bubble: hub adds `injected` when delivering it; clients show only a line such as "Task ‘xx’ finished" (`docs/protocol/protocol.md`, "Messages injected by Persona").
- **Coalescing**: wait 15 seconds after a task's first change (`quietNoticeDelay`), retaining only the latest change in that interval and noting "It also waited for the user during this period."
  If the user has answered before a waiting notice is sent, skip it. While the main chat is running, queue the notice between chat turns (the same queue as `conversation_send`, showing "Notice for task ‘xx’" above the input), then inject it after the current turn ends.
  If the queue already has a notice for this task, replace it in place instead of adding another. Implementation: `server/internal/assistant/quiet.go`.
- **`agent_start`**: when called from the main chat with the setting enabled, hub creates a quiet to-do (`origin` = main chat, no plan), attaches the Agent, inserts a task card, and thereafter treats it like a `task_plan` task.
  With the setting off or calls from other chats, it remains a standalone Agent as before.
- **Reporting chats** (#3192): tasks (`task_plan`, `agent_start`) dispatched from chats created with `report_tasks: true` (the extension's Lark /agent) ignore this setting and are always quiet.
  Notices go into **the chat that dispatched them**, not the main chat (the `Reporting` wording follows the instructions at that chat's start). Registered in `assistant/reporters.go` (`docs/extensions/lark-agent.md`).

### Supervision (#3124)

The main chat **supervises** tasks it dispatches (`task_plan`, and to-dos created by `agent_start` in quiet-task mode). The assistant owns their completion and quality, rather than merely relaying "done."

- **Ledger**: each `task_plan` task may include `done_when` (up to 6 acceptance criteria) and `expect_minutes` (estimated minutes). Criteria are appended to the assignment prompt, asking the Agent to provide evidence for each when finished.
  A task may also give `todo_id` (#5279): the to-do it runs on instead of a new one. Only one this chat opened, that is not done and has no Agent on it is taken (a Lark topic's to-do, `docs/assistant/todos.md` "Opening a to-do from a topic"); the check is made when the plan is made and again when it starts, and a task that fails it gets the reason as its `error` and starts nothing. The to-do keeps its title, notes and task card; the plan adds no second card, only the task's `done_when` / `expect_minutes` on its supervision.
  The to-do records its supervisor (the creating chat), criteria, estimated duration, and review state in `todo_supervision` (one row per supervised to-do, deleted with it). To-do JSON adds `supervision` (`docs/protocol/protocol.md`).
  Review states: in progress → `pending_review` → `accepted` (to-do done, `done_by = review`) / `rework` (`round` tracks iterations) / `needs_user` (awaiting user decision) / `cancelled` (to-do done).
- **No automatic closure**: when hub judges a supervised to-do complete (the "Automatic completion" check in `docs/assistant/todos.md`), it does not write `done_at`; it marks it pending review. If judged incomplete (`not_done` / `waiting` / `needs_user` / `unsure`), tell the supervisor "Stopped but not finished." Manual completion is unchanged.
  Completion is about the assigned task and its `done_when`, not whichever side question the Agent answered last. A known peer, cross-conversation or inspection turn cannot enter pending review on its answer alone. Only a fresh, successful `report_to_supervisor` done call with evidence tied to this task can establish the completion candidate from such a turn; the supervisor must still verify it. Unknown-source fallbacks never imply that a new question replaced a finished task. Ordinary delivery turns continue through the existing judge without requiring that tool.
  Ordinary `agent_send` from a conversation other than the task's supervisor carries a `side-conversation` context marker; the supervisor's instructions and explicit task handoffs retain their existing behavior.
- **Supervisor loop** (`server/internal/assistant/supervise_signals.go`, no model calls): every 30 seconds, inspect supervised tasks plus task events and derive signals:
  `review` pending review, `idle` stopped unfinished, `needs_you` waiting for an answer over 3 minutes (immediately for quiet tasks, since the user received no push), `stalled` working without progress (no session update) for more than max(20 minutes, estimate × 1.5), `interrupted` by model-service error, `failed` to start.
  The same signal for the same task is reported only once within the same state segment. A second guard suppresses an unchanged summary for the same task, Agent and signal kind for 30 minutes, regardless of new turn timestamps or elapsed-minute counters. Its hashed fingerprints and timestamps live in Postgres settings (`assistant.supervision_dedup`), survive manager restarts, and are reserved before dispatch; failed dispatch and queue admission release the reservation. An unreadable or unwritable reservation is retried rather than treated as empty history. Changed evidence, questions, summary, task, Agent or status can wake again. Signals from multiple tasks are combined into **one "📋 督办" (supervision) message** in the main chat (`prompts/assistant/supervise.md`: task, signal, criteria, question, Agent's last text, links, and handling instructions).
  The first waits 15 seconds to coalesce; subsequent messages are at most one per 10 minutes, with a 2-minute minimum for decision-waiting signals such as `needs_you` / `idle` / `interrupted` / `failed`. If the main chat is running, queue between turns ("Supervision message" above the input); replace any queued, undelivered supervision message with the merged one.
  Supervised tasks no longer emit "🔔 任务…" (task notices) from `quiet.go`; all appear in supervision messages. Tasks started by other chats are unchanged.
- **Levels and visibility** (#1988): `info` reports update task progress only and never wake the assistant. Other automatic signals are `triage`: they wake it but the web folds the whole turn into a muted supervision record by default, including the input, trace and answer. Expanding the record reveals the original content; transcripts are never deleted. Older supervision messages default to triage too. The fold ends at the next genuine user message, so that user's exchange remains visible.
  `supervision_finish {visibility: silent|user, summary}` gives the assistant an explicit disposition. Silent means finish without a user-facing reply; user explicitly reveals the turn without adding a push. Successful `notify_user`, `ask_user`, and user-facing `task_review` conclusions also promote it, monotonically: a later silent disposition cannot hide them. Server work folds preserve promotion via `fold.supervision_user`.
  Native clients need corresponding whole-turn folding, promotion and `checking` receipt rendering; this change modifies server and web only.
- **No push by default**: when a turn initiated by a supervision message ends normally, send no completion push (`fleet_push.go`) or channel mirror (the snapshot marks the main chat's `SessionView.supervising`). Errors and interruptions still push normally. Normal behavior resumes when the user speaks in the main chat or another message is injected. Use `notify_user` to tell the user and `ask_user` for decisions.
- **Review**: MCP tool `task_review {todo_id, verdict, checks:[{criterion, met, evidence}], note, outcome?, reason?, link?, recommend?}`. `accepted` closes the to-do (then closes the Agent according to "Close Agents for finished tasks").
  `accepted` and `needs_user` send the owner one conclusion card (`docs/extensions/bots.md` ("Conclusions")): `outcome` done / cannot, `reason` one sentence, `link` one link, and for `needs_user` the `recommend`ed option, which "OK" takes.
  `rework` sends the itemized gaps and instructions (`prompts/assistant/supervise_rework.md`) to the task's Agent (`session.send`, first restoring a closed Agent with its original launcher), leaves a `routed` entry in the chat, and judges again when the Agent stops. **At most 2 rounds**; a third `rework` becomes `needs_user`.
  After `needs_user`, no more supervision signals are emitted; the conclusion card asks the user (without the Lark bot, the assistant must ask them). `cancelled` closes the to-do (stored as `done_by = cancelled`, reason "取消：…", meaning "Cancelled: …"). Later Agent turns leave it closed and supervision off; only an explicit user **Reopen** resumes task tracking. Accepted tasks still reopen automatically when their Agent works again. Every review inserts a `review` chat-activity entry and is recorded in ledger `reviews`. Only the supervising chat can review its tasks.
- **Independent review** (#2173, `server/internal/assistant/supervise_autoreview.go`): off unless `manager.json` `agents.review.launcher` is set (`docs/operations/config.md`). When the judge says a supervised task is done, it does not enter `pending_review` at once: a fresh reviewer agent reviews the change first.
  - **What it sees**: the task's title and notes (its ask), its `done_when`, and the change from node `git.review` on the working agent's directory (branch against the default branch, uncommitted when there is no base; per-file diff blocks within budgets). Never the working agent's transcript, its last answer or its directory (`prompts/agents/review.md`). It answers with P0 / P1 / P2 findings with `path:line` evidence and a last line `<review-findings count="N" blocking="M"/>` (M: P0 and P1).
  - **Where it runs**: node `worktree.snapshot` checks out the working agent's `HEAD` detached under the computer's checkouts directory and applies its staged, unstaged and untracked change there (`node/reviewsnapshot.go`). The reviewer starts in that copy with `agents.review`'s launcher, model and permission mode (default `plan`), so what it edits, formats or builds — even after an engine restores it without the read-only mode — stays in the copy. When the run ends the reviewer is closed (`session.close`) and the copy removed (`worktree.remove`, forced); the tick retries both until they are gone. This is what #1808's user-triggered reviewer lacked (read-only after engine recovery could not be guaranteed); a node without `worktree.snapshot` fails the run, and the task waits for review as without one.
  - **What follows**: blocking findings become the next rework round (`prompts/assistant/supervise_review_rework.md`, the same `round` and 2-round limit as `task_review` rework, recorded in `reviews` with `by: "reviewer"`); no blocking findings, or no round left, and the task enters `pending_review` with the result attached: the supervision message shows it under the task, and the to-do's `supervision.auto_review` carries it to the apps (the web shows it under the criteria). A run that cannot get a copy, a reviewer or an answer, or runs past `timeout_minutes`, fails, and the task waits for review as before.
  - **State**: the run is a sub-state of the ledger, not a review state: the task stays in progress (or `rework`) while it runs, so clients that know only the review states are unchanged. `todo_supervision.auto_review` (jsonb) keeps the latest run: `{id, state: running|clean|blocking|failed|cancelled, work, round, judged, machine_id, cwd, session_id, worktree, started_at, ended_at, findings, count, blocking, error}`. Every transition is a compare-and-set on the run's id while it runs, so a turn end, the 30-second tick after a manager restart, and a cancellation (the agent works again, the supervisor decided, the to-do closed) each decide it once.
  - **The reviewer session** is linked to the to-do in `todo_agents` with `source: "review"`, `via` the run's id, released from the start: it is never one of the task's agents (judge, rework, closing). It is left out of the session lists the apps get, kept quiet (no pushes, badges or Live Activity), and its turns are not triaged.
- **Assignment deduplication**: `task_plan` compares new tasks with ongoing to-dos and working / waiting Agents. The same issue / PR number, or the same computer and directory (including nested directories) with similar titles, or very similar titles, yields "Possible duplicate" in the result. The assistant is told to inspect before deciding whether to merge or cancel; dispatch is not blocked.
- **Snapshot**: supervised tasks under "Tasks started from this chat" include "Supervision: normal / stalled N minutes / waiting for an answer N minutes / pending review / rework round N / awaiting your decision / accepted" and a criteria count. Items needing action come first.
- **Assistant workflow** is in its CLAUDE.md (`prompts/assistant/claude_md.md`, "做事方式"): always provide `done_when` when assigning; make decisions independently when possible, nudge or change approach when stalled, sequence resource contention, cancel duplicates; verify PR / CI / screenshots / live status personally before acceptance; tell the user only about decisions they must make and accepted results; split downstream follow-through (deployment, issue closure, TestFlight) into subsequent tasks or follow-ups.
- **In charge of any agent** (#5112, `assistant/agent_control.go`):
  - **Levels of interruption**: `agent_send` queues behind the agent's turn; `agent_interrupt` stops the turn at once (`session.interrupt`: Esc in a herdr pane, the runtime's or Codex's own cancel) and keeps the session; with `then` it sends the new instruction right after (steering). Work already done is not undone. Not for assistant conversations or read-only sources (`capabilities.interrupt` false). An agent that is not working (including one still starting) is not stopped; `then` is still sent (queued if it is busy). Closing an agent is not offered to the model (accepted / cancelled tasks are retired as before).
  - **Adoption**: `agent_supervise`, main conversation only. An agent on no todo becomes a task of the conversation: a new todo (titled after the session by default), the agent linked, the supervisor set, a task card in the feed; from then on it is like a `task_plan` task (signals, pending review, `task_review`). On one of its own tasks it sets `done_when` / `expect_minutes`. Another conversation's task or the user's own todo is refused and stays theirs. If the todo is made but the agent cannot be linked, the todo is closed at once (no empty task is left); one adoption runs at a time. The agent is not told: the answer reminds the assistant to tell it with `agent_send` if it should work to the criteria or report.
  - **Handing work to a running agent**: `agent_send` with `task {title, done_when?, expect_minutes?}`, main conversation only. The agent is adopted as above before the text is sent (the text is the new todo's notes, and its title when `title` is empty); the task card and a `routed` line with the text go into the feed once it is sent. An agent already on one of the conversation's own tasks stays on it when `title` is empty or that task's title (no second todo; `done_when` / `expect_minutes` are written after the send); another title is refused, so other work is not filed under a task that may be closed without it. An agent on the user's own todo or another conversation's task, a non-main conversation, and a text of only `/exit` / `/quit` are refused before the user is asked to confirm; nothing is sent. If the send fails, the todo made for it is deleted (a closed task would keep the agent and retire it later), unless the machine only did not answer in time (a resume can go on after the wait): then the task stays, with its card. Work the agent passes on follows the task without a new tool when its launcher passes the herdr marker on (`persona_from`) or it dispatches through `botmux dispatch` (`docs/assistant/todos.md` "Following spawned Agents"): every agent it starts that way while it is on the task joins it, for this work or not, and only once the node has written the marker to its pane (sent when the link changes, reconciled every 30 seconds). Any other agent is moved onto the task by a link by hand (`PUT /api/v1/todos/{id}/agents/{mid}/{sid}`). The handing agent stays linked until the task is closed, so a long-lived agent is on one task at a time; when the task is closed, it is retired like any task's agent if Persona started it (the next `agent_send` resumes it), while one the user started by hand is left running (`docs/assistant/todos.md` "Close Agents for completed tasks").
  - **Agents report to their supervisor**: agents get the MCP tool `report_to_supervisor {kind: progress|question|blocked|done, text, evidence?, step?, steps?, urgent?}`. Only an agent of a supervised task has someone to receive it; any other gets "nobody receives this" (not an error). A question with `urgent: false` is no signal: it waits in the queue for the next morning or evening briefing (`docs/extensions/bots.md` ("Questions that can wait")). A report becomes the supervisor signal `report`: `question` / `blocked` / `done` are triage (the 2-minute cadence), while `progress` is info and never creates a main-chat wake; reports of the same kind waiting for the same message are joined (both questions kept, evidence merged) instead of replacing each other; the message lists the kind, the step, what it said and its evidence. `done` does not mark the task pending review (the hub's judge still decides). At most 10 reports per session in 10 minutes. Only Claude Code agents with the Persona MCP have the tool (Codex does not), so task prompts do not mention it.
  - **Progress and the feed** (step 2): the latest report is the task's progress, `supervision.progress {kind, step?, steps?, text, at}` on the todo the apps get. It is a live reading kept in memory only (the next report brings it back after a restart) and written apart from the ledger: no database write, and `updated_at`, which names the review a task waits for, does not move. The snapshot's task line says "它最近向你汇报（N 分钟前）：…，第 3/5 步：…". `question` / `blocked` / `done` reports also go into the supervisor's conversation as a feed item of kind `report` (body: `kind`, `text`, `evidence`, `step`, `steps`), so the user can follow the handoff; progress reports only update the card. An `agent_interrupt` with `then` on an agent of one of the conversation's tasks adds a `routed` line with `interrupted: true` (a bare stop adds nothing). Clients that don't know the `report` kind skip it (web, iOS and Android all draw nothing for an unknown kind); those that don't know `interrupted` show the line as an ordinary message to the task. The web draws the progress under the task card's current step (`progressLine`), the report as "「任务」向 Persona 汇报" with the text and evidence, and the interrupting line with a stop icon. Mock: `MOCK_TASKS=1 MOCK_TASKS_REPORTS=1`. Without a report, the agent's own task list is the default progress (#5302, `Session.plan`, docs/integrations/agent-goal.md "Plan"): the snapshot's task line says "计划 3/5：<current>" and the card has a "3/5 · current" line; a report newer than the plan stands alone.
  - **Whose call it is**: an agent's MCP server has no session id of its own. `personad mcp` passes its pane (`HookPane`) to the node, and **only for the agents' own tools** (`report_to_supervisor`, `todo_decisions`, `todo_decision_add`, `todo_bind_thread`: `callerTools`) the node resolves the session from it (`node/hooks.go` `paneCaller`: the agent the host shows in the pane now, else the hooks' binding). Other tools still call without a session (a connector's approval card is not attached to the agent). An agent run by a runtime without a pane gets "nobody receives this".
- **Attachments** (#2437, `assistant/attachments.go`): pictures and files the user gave the assistant reach the agent through `attachments` on a `task_plan` task, `agent_start` or `agent_send`: absolute paths on the computer the conversation runs on. Only paths the user gave in that conversation are accepted: the directory where Claude Code saved the conversation's pictures (`…/<conversation id>/images/`), the node's `uploads` directory, or a path one of the user's messages carries (the app puts a file sent with Files there). Anything else is refused, so the tool cannot move arbitrary files between computers. On the same computer the paths go as they are. On another one the manager copies each file there first (`file.chunk` from the assistant's node, `file.upload.*` with `dest=terminal` on the agent's: `uploads/terminal/<date>/`, swept after 7 days), at most 8 and 50 MB each, also to computers marked `work`. A `task_plan` task's are checked when the plan is made (`PlanTask.Attachments`, from `AttachFrom`, the conversation's computer) and copied when it starts. A model does not always use the parameter, so a path the prompt or text names counts too when it is where the user's files land (the conversation's pictures, the node's `uploads`, or a file the app uploaded, `<name>-YYYYMMDD-HHMMSS`, that a message of the user's carries; never any other path the user wrote, which may be a repository's file the agent should open where it is). The text then names the copies. The agent's first message (or the sent text) ends with the files, the named ones that could not come and why, and the pictures in an `<attached_images>` block, as a picture sent straight to an agent ends. A copy that fails never stops the hand-off; the tool's answer says what was handed.
- **Clients**: web and Android task cards / to-do details show acceptance criteria and review state; iOS / Mac are separate work (sub-issues of #3124). Clients unaware of `supervision` continue normally, showing pending-review tasks as "Stopped / Unread."

## Data and APIs (hub)

### Task = to-do

Add two columns to `todos`:

- `origin`: the assistant chat that created it (session ID), empty for manually created to-dos. The dock and board include only items whose `origin` equals the current chat (the main chat also includes manual Agents; see "Manual Agents in the main-chat dock" below).
- `plan_id`: the plan that created it, nullable.

### Plans

Table `task_plans`: `id`, `conversation`, `tasks` (JSON: each item's `title`, `notes`, `machine`, `machine_policy`, `cwd`, `launcher`, `model`, `permission`, `ultracode`, `readonly`, `user_requested`, `why`),
`state` (`open` / `started` / `dismissed`), `created_at`, `updated_at`.

- MCP tool `task_plan` (assistant only): accepts a task list. Start directly if every item is read-only, `user_requested` (with `assistant.start_requested_tasks` enabled), or matched by `tasks.trust`; otherwise save an `open` plan and emit chat activity.
  Return the plan ID and each task's state (text). Automatic dispatch is reported as queued, not as proof of an Agent start. `machine_policy` defaults to `fixed` (including old saved plans); only explicit `flexible` tasks may move off an unhealthy machine. The policy survives storage, card edits and retry. Fixed tasks fail on their own target and can retry there after health recovery.
- `POST /api/v1/task-plans/{id}/start`: may include user-edited fields. Create a to-do per task (`origin`, `plan_id`), then start its Agent (`startSession` with `todo_id`).
  With `remember: true`, record this kind in `tasks.trust`. `POST …/dismiss`: do not start.
- `ultracode` (#12): the assistant sets it per task, as on `agent_start`, rather than writing the word into the prompt; the Agent starts with Claude Code's ultracode on (`session.start` `ultracode: true`). On start, `true` / `false` from the card overrides it and leaving it out keeps the assistant's choice.

### Chat activity

Hub-inserted chat entries do not enter Claude's transcript (that would trigger another turn and later be compacted away). Store separately in `assistant_feed`:
`id`, `conversation`, `at`, `kind`, `ref`, `body` (JSON), plus `sent_at` (when a message-card reply was sent, written alongside `body.sent_at` so `since` queries can use an index; 0390).

| kind | Trigger | ref / body |
| --- | --- | --- |
| `plan` | `task_plan` hands you a plan | Plan ID |
| `task` | A task starts | To-do ID |
| `answered` | A task interaction is handled | To-do ID; one-line summary (question, your answer), and full question / answer `items` per question |
| `receipt` | Task finishes / fails to start / is interrupted by a model-service error (`interrupted`, #1516) | To-do ID; summary, changed-file count, tests, PR; interrupted receipts include original error `error` |
| `routed` | You speak directly to a task, or Persona sends its agent work (`agent_send` with `task`, `agent_interrupt` with `then`, a rework) | To-do ID; original text; `interrupted` (agent_interrupt); `from: "assistant"` when Persona wrote it ("Messages to agents"). Migration 0441 marked older ones Persona wrote: an `interrupted` key, the rework prompt's opening line, or a `task` card of the same to-do at most 5 seconds before; the case it can't tell (agent_send with `task` to an agent already on that task, which writes no card) keeps the bubble |
| `message` | AI twin drafts a reply to an Inbox message related to a task | Inbox item ID; task, source, chat, sender, original message, draft |
| `today` | First opening of this chat that day (App calls `POST …/today`) | Date; tasks completed overnight, waiting tasks, open supervised tasks awaiting review (`pending_review`, excluded from waiting / working / done), working count, remaining Lark meetings today, chats mentioning me, other unread counts, unsuccessful items since yesterday at 18:00 (failed-start tasks, failed / skipped scheduled tasks, #4124) |

- The web Today card shows **Waiting for your review** when `pending_review` is nonempty (#4889): the task title for one item, or the count for several. **View** opens the first task, ordered by creation time like waiting tasks. The field is omitted when empty; older clients, including the unchanged iOS and Android Today cards, ignore it. Supervision state `needs_user` keeps its existing behavior.
- `GET /api/v1/assistant/conversations/{id}/feed?since=`; WebSocket `assistant_feed` pushes new entries. The App merges them with the transcript by time.
- Live plan-card / task-card state is not stored in the feed. Cards read current plan / to-do / session state by `ref`, updating in place.

### Hooks

- **Finished**: when `Machine.setSessions` finds an Agent attached to a to-do with `origin` changing from working to done / idle with no pending interaction (at the same point as `notifyFinished`),
  write `receipt`. A task with multiple Agents is finished only when all stop. If an Agent stops on a model API error (`turn_error`), write an `interrupted` receipt without judging completion (#1516).
  Continue on the card uses `POST /api/v1/todos/{id}/retry`; hub sends the Agent "继续" (continue). Once it runs, the task returns to working; a normal turn ending emits another done receipt.
- **Waiting for you**: no feed entry. The App directly uses the attached Agents' `pending_interactions`; handling (`resolve`) writes `answered`.
- **Speak to a task**: `POST /api/v1/assistant/conversations/{id}/route {todo_id, text}`; hub uses `session.send` to send to the task's highest-priority Agent and writes `routed`.
- **Persona learns about it**: the live snapshot adds "Tasks started from this chat," with each task's state, latest text, and changes since the previous snapshot (finished, waiting for you). No extra turn is triggered.

## App (iOS)

- The assistant's `ChatView(style: .assistant)` merges chat activity (inserted by time in `ChatLines`). New row types: plan card, task card, receipt, answered question, routed bubble, Persona's folded message to an agent (without the hand-offs the cards take in, `SentToAgents.shown`).
  Instant phone-tool calls (`phone_reminders_create`, `phone_calendar_create_event`, etc.) are extracted from the "Tools N" row and rendered as result cards.
- `mainFooter` adds a question-card queue (task interactions with task names) and dock above `InteractionPanel`; the input gains `@` and routing labels.
- New components belong in the design system (`apple/Persona/DesignSystem`), using `AgentState.tone` colors and `Theme.Motion` animations.
  No hardcoded values in pages (`CLAUDE.md`, "Design system (iOS)"). Logic lives in an `@Observable` `TasksModel` (dependencies from `Deps`, tests with `FakeHub`);
  chat-activity merging is a pure function covered by unit tests.
- Screenshots: add mock plan, task, and activity data to the mock hub (`scripts/mock/mock_hub.py`). DEBUG argument `-openUnifiedStep <n>` stops at the corresponding prototype step;
  capture light and dark for each step and compare to the prototype.

## Implementation (iOS, step 3)

| Part | Location |
| --- | --- |
| Data types (plans, chat activity, to-dos, task state) and APIs | `apple/Shared/Tasks.swift` |
| To-do list, local cache, ID index | `apple/Persona/App/TodoStore.swift` (`todos` events and reconnect share refresh logic; in-flight refreshes are coalesced with a follow-up fetch) |
| Plans, per-chat activity, routing target, opened task | `apple/Persona/App/TasksStore.swift` (to-dos read from the `TodoStore` index; `assistant_feed` / `task_plan` events update plans and activity; reconnect refetches each chat's feed) |
| Plan cards, task cards, receipts, answered questions, routed bubbles, dock, question cards, routing labels, `@` list | `apple/Persona/Views/Assistant/Tasks/TaskViews.swift` |
| Task details (embedded Agent chat), task board | `apple/Persona/Views/Assistant/Tasks/TaskSheets.swift` |
| Feed inserted into chat at the end of its turn (before the next message) | `ChatLines.mergeFeed`, `ChatLine.Content.feed` |
| Above the input: question card (earliest only, with "N more"), `@` list, dock, routing label; routed messages go directly to the task | `ChatView.taskFooter`, `Composer.sendText` |

- Reuse Agent state colors (`AgentState.tone`: Needs you red, Working amber, Unread green), not the prototype's orange waiting state, to match Agents.
- If dock pills do not fit, truncate the title but never "Waiting for you."
- Task details have one header layer: the embedded Agent page takes over the task name, Close, and task ⋯ (#3255; see "Task details" above).
- Screenshots: `MOCK_TASKS=1`, `MOCK_TASKS_AT`, `-showTaskDetail`, `-showTaskBoard` (`docs/operations/testing.md`).

#### Manual Agents in the main-chat dock (#1113)

Agents started manually in Agents have no to-do, or a to-do with empty `origin`, so no chat's dock sees them. The main chat must watch all tasks, so only its dock and board include them too:

- Included: all Working / Waiting for you Agents (excluding Persona's own chats), minus those already represented by tasks: Agents attached to unfinished tasks (nonempty `origin`),
  and sessions marked by hub with a non-manual to-do (including new tasks not yet refreshed into the to-do list). Agents attached to manually created to-dos still count as manual.
  Tasks from other chats remain excluded from the main chat, and their Agents do not reappear as manual duplicates.
- Ordering: task pills first, manual Agents afterward (waiting first; others by most recent entry into their current state). Their pills add the Agents page's `</>` mark; waiting pills use the same Needs you red and "Waiting for you" as tasks.
- Disappearance: remove from the dock when finished (Unread), idle, or offline. Unlike tasks, Unread manual Agents do not remain in the dock (they remain in Agents).
- "All N" includes them; the board lists them after tasks in Waiting for you / Working. Tapping a pill or board row opens the Agent's chat in the task-details sheet.
- Data: `AppModel.goingAgents` derives from the existing session list and updates only when pill-visible content changes. No new polling, and no dock redraw on every push.
  Filtering: `DockAgent.shown` (`apple/Shared/Tasks.swift`); unit tests `DockAgentTests`; UI tests `TaskDockAgentTests` (`MOCK_TASKS_DOCK=pair` for two pills, otherwise six).
- No chat-stream cards for manual Agents: they were not started from the chat.
- Web does the same (#1275): `dockAgents` (`web/src/lib/todos.ts`) filters and sorts; tapping a pill opens that Agent's chat in the side panel (`AgentPanel`); "All N" goes to To-dos.
- Step 4 (#996 and this step's iOS work):
  - **Day sections**: `ChatLines.daySections` adds a date above each day's first row (Today / Yesterday / month, day, weekday), only in assistant chats.
  - **Old task cards collapse**: non-today `task` entries become one line (`TaskLine`: state, title, progress); tapping opens details.
  - **Instant result cards**: successful `phone_reminders_create` / `phone_calendar_create_event` results render below that turn's work row (`QuickResult`,
    `ChatLines.quickResults`). Undo deletes locally through EventKit (`RemindersTool.remove`, `CalendarTool.remove`), without hub.
  - **Today**: first main-chat display each day calls `POST …/today` (`TasksStore.loadToday`), placing the card at midnight: completed tasks, waiting tasks, working count,
    Lark meetings (Prepare sends a message for you), chats mentioning you (Review); if the App already has reminder permission, also include reminders due today (no permission prompt for this).
  - **Direct-start tasks**: Persona profile's Proactive work section lists remembered kinds, with swipe-left deletion.
- **Message cards link to tasks** (hub): the Inbox's judge (`inbox/judge.md`, which since phase 1 only links items to tasks) sees active tasks. If a message concerns one, it returns `task_id` (only an ID shown to it).
  The AI twin's first message includes that task's progress (`inbox/agent.md`), so the draft states actual progress. Draft submission
  (`inbox_draft`) inserts a `message` entry into the chat that started the task. The App renders `MessageCard`: original message, attached task (live), draft (Edit inline), Send (Inbox sending; send by ID if the item is not local), Later (collapse for now).
  Already-replied cards show "Replied on your behalf" and the sent text.
  - **Sent state on the card** (#1519): when a reply for this item is sent (from a card, Inbox, or any client), hub writes `sent_at` and `sent` (the sent text) into the previously inserted message-card body,
    then rebroadcasts `assistant_feed` with the same ID; clients replace the body by ID. `GET …/feed?since=` also includes cards sent after `since`. Written once, never cleared:
    reopening an item with a new message and having the AI twin draft again creates a new card; the old one remains sent. For cards predating this record, reads backfill from the Inbox item (`done_by=replied`, later than the card), using the draft as text.
    Finding unsent cards (`db.MarkFeedSent`) uses partial index `assistant_feed_unsent` (`kind, ref, at`, predicate: body has no `sent_at`, migration 0392, #2617. Only message cards write `sent_at`; other kinds remain in the index, so it is not small).
    Keep this UPDATE's condition aligned with the index predicate or each reply scans the whole table again.
  - **Send once**: Send includes the card ID (`POST /inbox/{id}/reply {text, card}`). Hub locks per card and never resends a sent card (double taps, one tap on each of two clients, and stale pages all behave the same).
    Drafts submitted after the reply was sent no longer insert cards.
  - **No placeholders**: drafts are sent verbatim with one tap. `inbox/agent.md` and `inbox/draft.md` require omitting unknown information (no more 【】 markers for the user to fill).
    `inbox_draft` rejects drafts containing 【…】 / {{…}} and asks the AI twin to resubmit. Web and Android cards refuse to send drafts still containing 【…】 and ask for edits.
  - iOS still uses local records + the Inbox list item's `done`, not `sent_at` (the list retains processed items only for 3 days; reopening an item makes old cards show Send again). To be fixed.

## Steps

| Step | Scope |
| --- | --- |
| 1 | This document and prototype |
| 2 ✅ | hub: migrations (`todos.origin` / `plan_id`, `task_plans`, `assistant_feed`), `task_plan` tool and prompts, start / dismiss plan APIs, feed APIs and pushes, completion / answer / routing hooks, tasks in snapshots |
| 3 ✅ | iOS: models and `FakeHub`, feed merging, plan cards, task cards, dock, question cards, receipts, `@` and routing, task-details sheet, task board |
| 4 ✅ | Instant result cards, main-chat Today card, day sections and collapsing, `tasks.trust` Settings page (message-card task links are separate work) |
| 5 | Web ✅ (`docs/web/web.md`, "Unified main-chat entry (#930)"); Watch ✅ (questions carry `task_title`, displaying "Task · xx" in Needs you; working / waiting counts already exist in Live Activities and Watch and include task Agents); Android ✅ (`docs/android/android.md`, "Unified main-chat entry (#930)") |

## Decisions (2026-11-03)

1. Auto-start boundaries follow the three tiers above; plan cards can remember "Start this kind directly next time."
2. Tasks are to-dos; To-dos and the task board share one view.
3. Main chat uses day sections; task progress stays out of the chat; previous-day task cards collapse to a line. Distant context belongs in compaction and the knowledge base. Side chats remain, with less prominent entry points.
4. Watch and lock screen show only the dock and Needs you.
5. Polish the interface, UI, and interactions to chart-topping App quality: compare light / dark screenshots to the prototype at every step, with smooth state transitions and animations.
6. Do not change or demote Agents or the top-bar switch.
