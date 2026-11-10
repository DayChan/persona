# Agent goals

#2420. Claude Code's `/goal <condition>` and Codex's thread goal both mean "give the Agent a completion condition and don't stop until it is met". In Claude Code, each time the Agent is about to stop, a
Stop hook checks whether the condition holds; if not, it keeps going. Codex records a goal and its status (active, paused, blocked, usage limit reached, complete).
Before this, Persona showed the `/goal …` you typed only as an ordinary message. After scrolling a while you could not find it, and you never saw whether it was met, cleared, or why it was not met (how things were before this section).

## What the user sees

- **Top of the chat** (the expanded title on the phone, the sidebar title on iPad / split view, the row above the chat on Mac, below the top bar on Android): when there is a goal, an extra "Goal" bar appears: icon + first line of the goal + status
  (Active / Achieved / Paused / Blocked / Usage Limit). Tapping it opens the details: the full goal, status, the reason from the latest check (Claude Code), tokens and time used,
  and the token budget (Codex). With no goal the row does not appear and the screen looks as before.
- **In the chat**: when a goal is met, a "Goal achieved" notice is inserted (with the check's reason for Claude Code). When a Codex goal becomes paused, blocked, or hits its usage limit, a notice is inserted for each.
  Setting and clearing already produce two command-output notices in Claude Code, "Goal set: …" and "Goal cleared: …"; those stay as they are.
- On the phone, when the title is collapsed (it collapses by default as you read down), the collapsed title bar has a goal icon on the right (colored by status); tapping it opens the details directly.
- The web chat page shows it the same way in the activity row at the top, and opens the same details.
- An achieved goal stays visible (status "Achieved") until a new goal is set or it is cleared; once cleared the row disappears.

## Formats of the two sources (checked 2026-11-05 against real transcripts / rollouts on this machine)

### Claude Code (transcript jsonl)

Goal status is written as a `type: "attachment"` line whose `attachment.type` is `goal_status`:

| When | Fields |
| --- | --- |
| Set (`/goal <condition>`) | `{condition, met: false, sentinel: true}`, written before the command line |
| About to stop, check says not met | `{condition, met: false, reason}` |
| Check says met | `{condition, met: true, reason, iterations, durationMs, tokens}`; the hook then clears itself |
| Cleared (`/goal clear`) | `{condition, met: true, sentinel: true}`, followed by the command output `Goal cleared: <condition>` |

- Commands and output: newer versions write `type: "system", subtype: "local_command"`, older versions write a user line; the output is `<local-command-stdout>Goal set: <condition>` /
  `<local-command-stdout>Goal cleared: <condition>`. Some older versions have only these two lines and no `goal_status`, so both are recognized: "Goal set" sets an active goal,
  "Goal cleared" clears it.
- Setting a goal also writes an `isMeta` user line ("A session-scoped Stop hook is now active…"). It is meant for the model and stays hidden as before.

### Codex (rollout jsonl)

`type: "event_msg"`, `payload.type: "thread_goal_updated"`; `payload.goal` is the full current state:
`{threadId, objective, status, tokensUsed, timeUsedSeconds, tokenBudget?, createdAt, updatedAt}`. `status` is
`active` / `paused` / `blocked` / `usage_limited` / `budget_limited` / `complete` (same as the `thread_goals` table in `~/.codex/goals_*.sqlite`).
A new line is written when usage changes too. A `null` `goal` means cleared. The local corpus (Codex 0.166) only contains `active`; the other values come from the table's constraints and have not been observed.

## How it works

- **Parsing** (`server/internal/transcript`): `Meta.Goal` (`proto.GoalInfo`) updates with each line read, and the latest one wins. Claude Code's `met`
  maps to `complete`, `met: false` to `active`, and the check's reason goes into `reason`. Achievement and Codex's non-active statuses produce a `notice` in the chat, only when the status changes.
- **node**: a goal may be set at the start of a transcript hundreds of MB long, which the quick meta (reading only the last 384 KB) cannot see. So `bgScan` reads it, together with `/loop` and background tasks:
  the whole file once, then only appended data, parsing only lines containing a marker (`goal_status`, `Goal set: `, `Goal cleared`, `thread_goal_updated`).
  The session list carries `goal`, and the list signature (`listKey` / `listKeySized` in `node/listkey.go`) includes the goal, status, and reason, so any change triggers a push.
- **Protocol**: `Session.goal`, see `docs/protocol/protocol.md`. Fields are only added; old clients that don't know it simply don't show it.
- **Clients**: `GoalBar` on iOS / Mac (`Views/Session/SessionPanes.swift`) and the `GoalSheet` details; the activity row in `views/chat/Activity.tsx` on the web.
  Android (#2888): `Session.goal` (`GoalInfo`) in `core:contracts`, `ChatGoalBar` below the chat's top bar (`feature/chat/ChatGoal.kt`, the design system's `JActivityBar`), tapping opens `ChatGoalSheet`;
  the achieved / stopped notices in the chat are `notice` items generated by the hub and show as ordinary notices. The top bar does not collapse, so there is no goal icon like in the iOS collapsed title.
  The structure of chat items is unchanged, so the `ChatCache` version does not need a bump.

## Plan

#5302. Besides a goal, a Claude Code agent keeps a plan: the `TodoWrite` list (each call writes the whole list, items
`{content, status, activeForm}`) or, with the newer task tools, items `TaskCreate` adds and `TaskUpdate` edits by id
(`TaskList` reads the whole list back). The session carries it counted, so the main conversation and the apps see how
far along an agent is without reading its transcript and without the agent reporting (#5271's report, when there is
one, is more specific).

- **Parsing** (`transcript/plan.go`): `Meta.Plan` (`proto.PlanInfo {done, total, current, at}`) is rebuilt from the
  plan tools' successful results with the same logic as the folded runs' task lists (`fold_tasks.go`
  `foldTaskSnapshots`): `TodoWrite` replaces the todo items, `TaskList` the task items, `TaskCreate` / `TaskUpdate`
  merge into an item by id, a `deleted` task goes, and a failed call changes nothing. `done` counts the `completed`
  items, `total` all of them, `current` is the first `in_progress` item's `activeForm` (else its subject) on one line,
  clipped to 120 characters. With no item in progress `current` is empty: the next pending item is not what the agent
  is doing. A list whose items are all done stays as "N/N" until the next one; an empty list is no plan. `at` moves only
  when done, total or current change. Subagents' calls (sidechain lines) and Codex rollouts are left out (Codex's
  `update_plan` may follow later).
- **node**: a `TodoWrite` is often far back in a long turn, so the plan is read by the same `bgScan` as the goal. Its
  markers are the call lines (`"name":"TodoWrite"`, `"TaskCreate"`, `"TaskUpdate"`, `"TaskList"`); a result line does
  not name its tool, so the scan also parses lines holding the id of a call still waiting for its result
  (`Meta.PlanPending`). Big first reads stay in `scanBackgroundLater`; a resident node's next hub connection goes on
  with the last one's scans (`server/internal/node/carry.go`), so its first list already has the plan. `Session.plan` is
  set for agents in panes, Claude background sessions and runtime sessions whose conversation is a Claude transcript
  here (#5809), not for other runtimes; the list's key has done, total and current, so a step is pushed when it
  happens.
- **Snapshot** (`prompts/assistant/snapshot_plan.md`): a task's line and the agents' rows say "计划 3/5：<current>";
  a task whose agent reported to the supervisor after the plan last changed shows only the report.
- **Web**: the task card has a line "3/5 · current" under the step (`data-plan-line`, `lib/plan.ts`) unless a newer
  `supervision.progress` stands; an agent row (sidebar, Agents page) has the same line while the agent works or items
  are left. iOS and Android follow separately.
- Not persisted: it is read from the transcript on each scan, and after a node restart by the first full scan.

## Screenshots

Mock hub with `MOCK_GOAL=1`: on the MacBook, "Add a home screen widget to the weather app" has an active goal (with the reason from the latest check), and the goal of "Review the changes in PR #42" is achieved;
with `MOCK_GOAL=stopped`, the widget goal has used up its token budget.
Plans: `MOCK_TASKS=1 MOCK_AGENT_PLANS=1` gives two working agents a plan (one of them a task's agent).

## Out of scope

- No setting, pausing, or clearing goals in the App: send `/goal …` in the terminal or in the composer (Claude Code commands are sent through as usual).
