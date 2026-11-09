# Agent work state: working / blocked on a named prerequisite / done (#1686)

The user's words (2026-11-04): "Persona isn't proactive enough right now. I see lots of unread sessions… Every session should either be working, explicitly waiting for a prerequisite,
or done… Minimize waiting time as much as possible."

## Why

The unified states in `docs/protocol/agent-states.md` describe the **host's perspective**: Needs you / Working / Unread / Seen. "Unread" means only that a turn ended and the user has not read it,
not how far the session has progressed **in its work**. The Agents page ends up with a row of green dots that must each be opened and read to find out:

- Which tasks are done and ready to wrap up;
- Which are waiting for a user decision ("Should I push to beta?", "Which of D2 / D4 / D10?"), yet show only a green dot in the list;
- Which are waiting for an external event (deployment completion before testing, CI, an OAuth link), with nothing to wake them when the event occurs;
- Which stopped only because of a transient error such as `API Error: 503`, and simply need a "continue" message;
- Authorization links that expire within minutes and are already invalid by the time the user sees them.

## Model

In work terms, each session is in exactly one of three states (`proto.Session.work.phase`):

| phase | Meaning | List preview |
| --- | --- | --- |
| `working` | The host is running (`state = working`) | Keep using `activity` / `last_text` |
| `blocked` | Stopped, waiting for a **named** prerequisite (`on`) | "What it is waiting for" |
| `done` | The task is complete and delivered | One-line conclusion + artifacts |

A `blocked` prerequisite has four types (`work.on`):

| on | Examples | Who advances it |
| --- | --- | --- |
| `user` | Choose a plan, decide whether to push to beta, provide information, click an authorization link, expired login | User: Persona turns the question and options into a "Needs you" card; one tap sends the answer back to the Agent. |
| `agent` | "Wait for the neighboring Agent to merge, then rebase" | Persona: wakes it when that Agent stops. |
| `external` | Wait for deployment / CI / review / a time; background commands and `/loop` wake themselves | Persona: wakes it on schedule to check for itself (no wake needed for background commands or `/loop`). |
| `transient` | `API Error: 503 / 529 overloaded / Connection error` | Persona: automatically sends "continue" after backoff, with a limit. |

Relationship to unified states: `state` values **do not change** (old Apps display them as before); the algorithm simply also considers work state:

| Work state (this turn) | `state` | Explanation |
| --- | --- | --- |
| `done` | `idle` | No longer occupies "Unread"; conclusion is in `work.line` and included in pushes. |
| `blocked` · `user`, with options | `needs_you` | Persona created a question card (`pending_interactions` increases by one). |
| `blocked` · `agent` / `external` / `transient`, with Persona waiting (`wake_at` or a target Agent) | `idle` | New Apps show "Waiting" + what it is waiting for; old Apps show Seen. |
| Not triaged, triage failed, or unclear (`unsure`) | Unchanged (`unread`) | Better to leave a green dot than hide something not understood. |

"Unread" then remains only while triage is pending (a dozen or so seconds after a turn ends) or when the model cannot tell.

## Responsibilities (in the manager since 2026-11-04)

Triage belongs to the **manager** (currently just one: the assistant process `personad assistant`, `server/internal/assistant/agents_*.go`). The hub is only a transport
(`hub/agents_link.go`; bus methods are in `docs/protocol/protocol.md`, "Bus" → "Agents"):

| Component | Responsibilities |
| --- | --- |
| Manager | Post-turn triage (rules + model `agent.triage`, through the hub's `app.call models.json`), automatic continuation, wakeups, cards and answers, four switches (settings `agents.triage`), tables `session_work` / `session_work_events`, statistics; labels for new Agents (`agent.title`), and launchers / models for new and resumed Agents ("Starting" below). |
| hub | Reports what happened: turn starts / ends (`session.turn_started` / `session.turn_ended`, already sent), a user views a session in the App (`session.seen`). Displays the manager's snapshot: session-list `work` (snapshot `work`), its cards (snapshot `interactions`, displayed, pushed, and expired like assistant questions, with answers returned through `manager.action`). Messages to Agents forward to the node through `device.call session.send`. |

- **Session-list `work`**: the manager's snapshot reports each session's latest turn's work state (`work.turn_at` is when that turn ended). The hub displays it only if the session is still on that turn.
  Working (`{"phase":"working"}`), the Agent asking its own question (its cards precede the manager's), and a terminal dialog are reported by the hub from the current session state without waiting for the manager. No `work` when the main switch is off.
- **Starting**: before `POST /api/v1/sessions`, the hub asks the manager for `agents.start_params`. It fills launcher, model, thinking effort, and fingerprint from **Model Settings › Agents** (the launcher / model selected for this start override them; `docs/assistant/model-config.md`),
  then derives a tab label from the first message. Agents started in the background (`async`) or from Lark cards request their names later through `agents.tab_name`. Before sending to a stopped session with an unknown launcher (resuming it), ask `agents.resume_params`.
  `POST /api/v1/tabs/autoname` also belongs to the manager.

**When the manager is disconnected** (the hub just started and it has not handshaken; the second or two during its restart; it crashed and is being relaunched):

- Starting an Agent or resuming a session with an unknown launcher: if the manager has never connected since hub startup, wait for it (until 60 seconds after hub startup, no longer than the request's own deadline). Requests queued on the port and App replays arrive immediately after a hub restart,
  so they still get parameters from the manager. If it connected before and is now absent, do not wait.
- If it does not arrive, the Agent cannot start: since 2026-11-04, the manager connects to computers ([machines.md](../server/machines.md), "Who connects to computers"). Without it nothing can reach the machine, so starts return `503 assistant_offline`,
  as do messages to stopped sessions (resuming them). Previously, the hub applied **Model Settings › Agents** itself and could still start them.
- Triage pauses: turn-end events are not sent to it (the hub does not queue them). Due continuations / wakeups wait until it returns, with the usual rechecks before sending. On connection / reconnection, catch up: the next check (within 15 seconds) begins triaging turns that ended during the outage,
  remain unread, and are within 24 hours (up to 3 at a time, with further batches each minute; the same applies at hub startup, because its bus opens last and nodes connect first). Do not catch up turns already seen during the outage (in the App or herdr on the computer), as before after hub restarts.
  Cards for sessions that resumed working during the outage are removed at the next check (every 15 seconds).
- The session list retains its last reported `work` (still only for sessions remaining on that turn). If a wakeup is over 5 minutes late, it no longer counts as "Waiting" (`wake_at` is removed from the list, returning to Unread).
- Its cards remain visible and answerable: the hub records the answer first and delivers it after the manager returns and reports a snapshot. If the manager temporarily cannot accept it (for example, it has not restored its state), retain it and retry after 10 seconds until it accepts,
  says the card no longer exists, or the card's expiry is reached. If the hub also restarts, this answer is lost and the card reappears.
- Switch / statistics endpoints (`/api/v1/agents/triage`, `/api/v1/agents/work/stats`, `/api/v1/tabs/autoname`) return `503 assistant_offline`.
- Manager-only restart: cards still held by the hub remain, without another push. As when triage lived in the hub, remove them only on answer, resumed Agent work, or expiry.
- Hub restart (the manager is its child process and restarts with it): for the second or two before the manager's first snapshot, the list has no `work` (triaged sessions briefly return to Unread). Cards reappear after the computer reconnects (same id, another push, as with previous hub restarts).
  Remove a card only if the Agent has progressed (resumed work or a later turn). A session merely looking different just after reconnection (seen and now idle, waiting for its own question, or a restarted node not yet having read the transcript timestamp) does not count. Retain the card and restore it at the next check (`cardBack`).
  This card also stays pending in interaction history: at startup the hub marks pending interactions left by the previous process as cancelled, except those referenced by `session_work.interaction_id` (`db.ExpireStaleInteractions`). The manager marks as cancelled only those it does not restore.
  Previously (before 2026-11-06), every hub restart removed decision cards (including safety-critical deployment and merge cards): sessions looked "seen" or "on another turn" in the first seconds after reconnection, so the manager forgot the cards, and interaction history had already marked them cancelled.

If a card is answered but the message cannot reach the Agent (its computer just disconnected), recreate the card with a new id and push again so the user can answer again (previously the App's answer attempt returned an error).
If the manager's snapshot still reports a card whose answer was delivered to it (but not accepted), the hub restores the card after 30 seconds.

## Triage

### When

- A turn ends (working → done / idle in `setSessions`, at the same point as the "Completed" push). If it remains inactive for `triage_settle_seconds` (default 15 seconds), begin triage.
  If work resumes during that interval (a Stop hook continues it, or another message arrives immediately), do not triage that turn.
- After every manager connection and once per minute afterward, catch up stopped sessions whose turns ended within 24 hours, are still unread, and have not yet been triaged (including turns that ended after deployment or while the manager was disconnected; see "Responsibilities").
- Do not triage assistant chats (`assistant`, including AI twins and Lark /agent) or sessions outside the host (`offline`). Muted sessions are still triaged, but no cards are created.

### Rules first, then the model

Apply in order; on a match, do not call the model:

1. Pending interactions / host `blocked`: already Needs you, `blocked · user`, with the interaction's question as `line`.
2. This turn was interrupted by a model API error (`turn_error`):
   - Source: Claude Code writes the error as its last words (`API Error: …`); the hub recognizes the prefix of `last_text`. Codex does not write it as a reply. Its error is in this turn's
     `task_complete.error` (rollout) or `turn/completed` with `status: failed` + `turn.error` (app-server). The node reads and reports `turn_error` directly,
     plus `turn_error_kind` based on the Codex error type (`codex_error_info`) (#2185, "Codex error types" below).
   - Use `turn_error_kind` when present; otherwise use the text:
   - A subscription's usage limit (`usage_limit`) → an `external` wait for its reset, or a card when the reset is not known ("Usage limits" below); never `transient`;
   - Transient (5xx, `overloaded`, `Connection error`, `timed out`, `socket hang up`, `ECONNRESET`, `fetch failed`, `no eligible upstream`, 429) → `transient`, schedule automatic continuation;
   - Other errors (`Failed to authenticate`, `OAuth token has expired`, `Invalid API key`, `Not logged in · Please run /login`—Claude Code's synthetic reply when not logged in—401 / 403, selected model unavailable) → `blocked · user`, with a card "The model service needs your attention: <original text>" and options "Fixed, continue" / "Leave it for now".

   **Codex error types** (`transcript/codex_error.go`; `server_overloaded` in rollout and `serverOverloaded` in app-server mean the same thing):

   | Codex reports | `turn_error_kind` |
   | --- | --- |
   | `server_overloaded`, `internal_server_error`, `rate_limit_exceeded` | `transient` |
   | `http_connection_failed`, `response_stream_connection_failed`, `response_stream_disconnected`, `response_too_many_failed_attempts`: no status code, or 408 / 429 / 5xx | `transient` |
   | Same types, other status codes (401, 403, 400…) | `user` |
   | `usage_limit_exceeded` | `usage_limit` ("Usage limits" below) |
   | `context_window_exceeded`, `unauthorized`, `model_not_found`, policy errors, and future new types | `user` (continuation cannot resolve them) |
   | `other`, no type | Not reported; use text |

   Stopping with Esc (`turn_aborted`, `status: interrupted`) is not an error. The node rereads a changing rollout at most every 3 seconds. If the file is written again during throttling,
   omit the cached `turn_error` (the continued turn may already have completed normally; an old error would continue it again). Better to be a few seconds late.
3. Background commands / sub-Agents still running (`background`, `subagents` contain running entries) → `external`, "N background tasks running"; do not register a wakeup (results wake it themselves).
4. `/loop` running (`loop`) → `external`, "Scheduled loop, next at HH:MM"; do not register a wakeup.
5. Otherwise: model.

### Model

- Purpose `agent.triage`, "Session Triage" (**Settings › Model Settings**, under "Information filtering"; defaults to inheriting the inexpensive Inbox triage model).
- Input: Agent name, directory / project, the user's request for this turn (`lastTurn` ask), final reply (first and last 1500 characters), other working Agents on the same computer (name + short id), current time.
- JSON output (`server/internal/prompts/agents/triage.md`):

  ```json
  {"phase":"done|blocked|unsure", "on":"user|agent|external",
   "line":"At most 30 characters: conclusion or what it is waiting for",
   "questions":[{"question":"…","header":"…","options":[{"label":"…","description":"…"}],"multi_select":false}],
   "link":{"url":"https://…","minutes":10},
   "wait":{"what":"What it is waiting for","then":"What to do afterward","check_minutes":10,"agent":"<short id>","risky":false},
   "artifacts":[{"kind":"pr|commit|doc|build|url","ref":"…"}]}
  ```

- Decision principles in the prompt:
  - An optional offer at the end such as "Would you also like me to…?" does not mean blocked. If the task is complete, it is `done`; include the offer in the conclusion.
  - If the next step is **pushing code, deploying / releasing, sending messages / comments to others, or merging / modifying someone else's MR / PR**, always classify it as `blocked · user` and ask whether to proceed, regardless of what the Agent says it is waiting for (see "Safety boundary").
  - If uncertain whether the task is complete, use `unsure`, not `done`.

### Where the conclusion goes

- The `session_work` table ("Storage" below), sent with the session list as `work`;
- When done: add `完成：<结论>` ("Completed: <conclusion>") to the knowledge-base project timeline for this directory (`kbAgentDone` used to record the last words of every turn; now it records the conclusion when available);
- Replace the "Completed" push body with the conclusion; the main chat's live snapshot adds "Done" and "Waiting" sections ("What Persona knows" below).

## Automatic actions

### Transient errors: automatic continuation

- Backoff: 30 seconds, 2 minutes, 8 minutes; up to 3 attempts (`agents.triage.max_retries`). Reset after a normal (non-error) turn completion.
- If all 3 fail: a `blocked · user` card, "The model service keeps failing after 3 automatic retries: <original text>", with options "Try Again" / "Leave it for now".
- Send exactly `继续` ("Continue"), the same text sent by "Retry" on the App's error card (`continueText`).

### Usage limits: continue at the reset

A subscription's usage limit gets no backoff "Continue" (each would be refused at once): Persona continues the work about a minute after the limit resets, unless the CLI goes on by itself ("Usage limits" below).

### Waiting: scheduled wakeups

- `external`: register `wake_at` using the model's `check_minutes` (2 minutes to 4 hours, default 10 minutes); double the interval for each further wait (up to 1 hour), with at most 6 wakeups (`max_wakes`).
  When the wait itself names CI and a GitHub PR is identifiable, Persona first polls its checks without another model turn. The active GitHub connector reads the PR head's check runs and commit statuses, including all pages, and checks the head again; the node's current-branch PR reader is the fallback when it can prove the result complete and the PR matches. Empty, incomplete, or still-running checks never count as complete; an empty set that persists until the original scheduled wake falls back to that wake. Rate limits defer polling, and source errors are retried with backoff; after three consecutive attempts where no source yields usable checks, the ordinary scheduled wake takes over. A valid pending check or explicit rate limit resets that error count and remains monitored. After all reported checks finish, Persona waits two minutes and verifies the same check set on the same head before waking the agent. This observation window reduces early wakes, but a provider can register checks later and no finite window proves it will not. Polling backs off to a capped interval and continues while checks are available, including after a long offline period; it falls back to the scheduled wake if neither source can read the PR; work machines and guest conversations use scheduled wakes to preserve their privacy boundary. The wake contains a localized result and bounded, escaped failing check names explicitly marked as untrusted data, without logs. The watch and its pending message are persisted, and the hub's once-only send guard avoids duplicate messages on retry; an uncertain delivery is not resent. A new turn, a user message or viewing that turn in an app cancels it, and the wake switch controls it.
  For other waits, or when no check source works, Persona sends a wakeup message (`prompts/agents/wake.md`): what the previous turn said it was waiting for, what to do afterward, and a request to check for itself. If ready, continue; if not, say how much longer.
  Triage the resulting turn as usual; register again if still waiting. After 6 wakeups without completion, show a card "Still waiting for 'X' after 6 wakeups", with options "Keep waiting" / "Stop waiting".
- `agent`: the model identifies the target among the working Agents shown to it (short id). Wake this Agent when the target's turn ends (or it goes offline), with `check_minutes` as a fallback.

A GitHub extension may opt into machine-readable polling with the read tool `github_ci_snapshot_v1`, called with `{"repo":"owner/repo","number":12}`. Its JSON response has `version: 1`, `host: "github.com"`, matching `repo` and `number`, `head` and `head_after` containing the same full commit hash, `complete: true`, and both `check_runs` and `statuses` arrays. Each check run has `id`, `name`, `status`, and `conclusion`; each commit status has `context` and `state`. The extension must read every page of both sources, then verify the head again; it must not include logs. Unknown versions or fields, missing arrays, duplicate items, mismatched targets or heads, and incomplete results are rejected. An extension offering only the existing human-readable `github_checks` does not provide this contract: Persona uses the node fallback or scheduled wake instead.

### User decisions: cards

- Persona creates a `question` interaction (`tool_name: persona_triage`, `summary` is `line`; the manager reports it in snapshot `interactions`, and the hub displays, pushes, and expires it accordingly), using the model's questions and options. The App, web, Android, Watch, Lark cards, Inbox's "Agents waiting for you", and main-chat task dock already render and answer them; no client changes needed.
  Push is time-sensitive, like other questions.
- Normal selections (or typing an "Other answer") reach the manager through `manager.action`, which formats them as a message (`prompts/agents/answer.md`: `关于你上面的问题：Q → A`, "Regarding your question above: Q → A"), and sends it to that Agent with `session.send`.
- Triage cards advertise `can_cancel:true`. Closing one commits its turn as deferred and clears its scheduled wakeup without sending an Agent message or interrupting the task. A model-generated single-choice option with `action:"defer"` has the same effect when selected; its label remains the answer stored in interaction history. Only options actually offered on the card carry this intent, not arbitrary free text. Legacy persisted pause labels have a narrow compatibility path.
- A deferred turn stays quiet after refresh, reconnect, and manager restart; a genuinely new turn can ask a new question. Repeated owner snapshots replay the original terminal outcome instead of registering the same question again. Persistence failures remain retryable rather than acknowledging a durable close.
- For authorization links (`link`), the auth relay goes first ([auth-relay.md](../operations/auth-relay.md)): when an extension's `auth_relay` contribution names the link and it was printed by the Agent's own command on one of your computers, the extension completes it with the account it is signed in with, the Agent is told to go on and no card goes up. Otherwise the card's first question contains the link and validity period (plus one line on why Persona didn't do it, when it tried), with options "Authorized, continue" / "Not now". Push as soon as triage finishes (15 seconds after the turn stops plus a second or two for the model), not after the 60-second "Completed" push delay.
- Remove a card (without sending anything) when the Agent resumes work (you replied directly on the computer or in the App), a new turn starts, the session disappears, or 24 hours pass without an answer.

## Usage limits

A subscription's 5-hour or weekly usage limit (#1710) is no failure to report and nothing for the user to fix: the turn waits for the limit to reset, then goes on.

- **Where the facts come from** (`proto.Session`: `turn_error_kind: "usage_limit"`, `turn_error_resets_at`, `turn_error_resumes`, `turn_error_after_work`): the agent's own record first (Claude Code's refused reply with `error: "rate_limit"` and `quotaLimits.resetsAt`; the reset of a Codex rollout's own rate-limit window), the words as the fallback for an older node or CLI (`transcript.UsageLimitText` / `UsageLimitReset`, completed on the session lists by `decorateLimit`): Claude Code's "You've hit your … limit · resets 4:20pm (Area/City)". A time without a time zone is never read (Codex's "try again at 3:05 PM" is some machine's local time), and a subagent's limit line (`isSidechain`) says nothing of the session.
- **Only a subscription's limit has a reset.** A gateway's or an API key's 429 (no `quotaLimits`, "Request rejected (429) · … quota reached") keeps its old handling: transient, "Continue" with backoff.
- **Triage** (a rule, before transient errors): with its reset known, `blocked · external` with `resets_at` and no `wait` (the error is the session's `turn_error`; an external wait with nothing named is one that ends by itself, as every client's list reads it); the line says when: "continues by itself at 16:20" when the CLI said so, else "resets at 16:20" (the user's time zone; with month and day, `01-02 16:20`, when the reset is on another day). Without a known reset: a card, "The subscription's usage limit is used up, and it didn't say when it resets: …", with options "Try again" / "Leave it". Never `transient`.
- **The CLI goes on by itself** (Claude Code's "Usage limit reached · continuing automatically at …"): no wake and no card, also when its reset is not known (the line then says it goes on after the reset). Typing into the session would cancel its own continuation. Claude Code writes the notice once, at the first refused turn; the node keeps its word while nothing in the transcript's tail took it back (a message typed, a reply, its "Automatic continue …" notices), however far the wait's request lines push the notice out of the tail it reads.
- **Otherwise Persona continues it** about a minute after the reset, spread over 30 seconds so that the agents of one account do not all start in the same second (`limitContinueAt`), with `prompts/agents/limit_continue.md`; event `limit_continued`. It is the `auto_continue` switch's, not `wake`'s: off (or turned off before the continuation went), the line stays and nothing is sent; the lists then name the line as the wait's `wait`, so they keep the session for the user instead of showing a wait that ends by itself. The apps show the continuation as one line ("Usage limit reset: Persona told it to go on", `injected` state `limit_continue`). None of a wait's other checks apply (risky next step, `check_minutes`): it goes on with what the limit stopped. Right before sending, the session is read again: when it now says it goes on by itself, nothing is sent; when its limit now resets later, the continuation moves to then.
- **At most 3 continuations without a productive turn** (`maxLimitContinues`): a reset read too early is refused again at once. Then a card, "It was continued automatically 3 times after the usage limit reset, and hit the limit again each time", with options "Try again" / "Leave it"; the card keeps `resets_at`, so a turn refused again while it is up (a background task's report woke it) is the user's too. A turn that did some work before the limit refused it again (`turn_error_after_work`), or a later reset (the next window: the earlier reset was read right), starts the count again; an ordinary external wait never takes over a usage limit's count, nor the other way round.
- **The user's actions cancel it** as any automatic action ("Avoid conflicting with manual user actions"): a message, looking at it in herdr, queued input, a pending card.
- **No misleading notices while it waits** (`limitTakenUp`: the reset is known and at most 10 minutes past, and the CLI said it goes on by itself, or Persona's continuation or its card is set): no "Completed" or "Interrupted" push for the turn. The turn stays followed, so the end after the reset is the one pushed, timed from the turn's start. A task dispatched from the main chat gets no receipt meanwhile; it is looked at again when the wait is over, so a turn that did not go on after all (its continuation cancelled) still gets its "Interrupted" receipt. A limit nothing takes up (the switches off and a CLI that does not go on by itself, or no reset known) is told as the turn ends, as a usage limit ("Usage limit reached; continue it once the limit resets: …"), never as "the model service failed". For the main assistant, a task waiting for the reset reads as `usage_limit` in its snapshot, not `interrupted`. A sign-in that is gone ("Please run /login · …", "Login expired · …") is no usage limit: it reads as interrupted (`turn_error_code` `auth_failed` / `not_logged_in`), never as "Completed".
- **Scheduled tasks** (`watchScheduledRun`): a run whose conversation stopped on a usage limit with a known reset is retried a minute or so after the reset instead of after 3 minutes; that wait is not cut by the watcher's 30 minutes, and `retryStillDue` decides at that time as before (a retry that fails again is not retried); a conversation that went on meanwhile (its turn is no longer the one the limit stopped) is not retried, so what the run does is not done twice. A conversation that goes on by itself is not retried. The failure push is unchanged.
- **Known limits**: the waits are kept in `session_work` like any wakeup and come back after a restart, but only for turns that ended within the last two days (a weekly limit can be longer); a scheduled run's retry waits in memory and is lost when the manager restarts.

## Safety boundary

Persona **sends to Agents on its own** only four kinds of message, none introducing new intent:

1. Continuation after a transient error: like "Retry" on the App's error card, it resumes the interrupted task; likewise the continuation once a subscription's usage limit reset (`prompts/agents/limit_continue.md`: go on with what you were doing, without redoing what is done);
2. A wakeup after a registered wait: asks only whether the awaited event is ready, and explicitly says `接下来要推代码、部署、给别人发消息、合并或改别人的 MR 时先停下来问用户，不要自己做` ("Before pushing code, deploying, messaging others, or merging / modifying someone else's MR, stop and ask the user; do not do it yourself");
3. The answer the user selected on a card;
4. After the auth relay completed the authorization link it stopped at ([auth-relay.md](../operations/auth-relay.md)): go on with what it was doing.

Any next step involving **pushing code, deploying / releasing, messaging others, or touching someone else's MR / PR** can only become "Waiting for your decision":

- The prompt requires the model to classify these next steps as `blocked · user` (also when `wait.risky` is true);
- Code fallback: if `wait.then` / `line` contains push, deploy, `部署` (deploy), `发布` (release), `上线` (go live), merge, `合并` (merge), `发消息` (send a message), `回复` (reply), `评论` (comment), testflight, or release, do not register a wakeup. Instead, ask on a card, "After 'X', it plans to 'Y'. Should it proceed?";
- When the user chooses "Do it" on the card, the Agent receives the user's answer, which counts as user authorization.

## Avoid conflicting with manual user actions

**Immediately before** each automatic action (continue, wake, create a card), reread the session and proceed only if all conditions hold:

- It is still the triaged turn (`updated_at` unchanged), stopped (not working), with no pending interaction or queued input (`queued`);
- The host is `done`, not `idle`: if you focused this tab in herdr (watching it at the computer), leave it alone and only record work state. Hosts that keep reporting `idle` after stopping therefore get no automatic actions (better not to interfere);
- The computer is online.

Sending a message yourself or typing on the computer starts the session working: all retries, wakeups, and cards for that turn become invalid. Triage again after the new turn ends.
The same applies to "Retry" on an App error card (work begins, invalidating queued automatic retries). Persona records continuations and wakeups in `session_work`, sending each only once per turn.

## Switches (requested 2026-11-05: all off by default)

Triage calls a model after every stopped turn; continuation and wakeups also make the Agent run another turn on its own model. All consume the user's tokens. Therefore **everything defaults to off**.
Users enable them under **Settings › Session Triage** (web, Android, iOS / Mac). Four switches are stored in settings `agents.triage` (read and written only by the manager):

| Switch | Controls | Whose tokens it uses |
| --- | --- | --- |
| `enabled` Session Triage | Main switch: triage after a turn stops, show what it is waiting for, and stop counting completed work as Unread | Triage model (**Settings › Model Settings › Session Triage**, `agent.triage`, defaults to the Inbox triage model) |
| `decide` Create decision cards | Turn user decisions into "Needs you" cards | No extra tokens |
| `auto_continue` Continue automatically on errors | Automatically send continuation after transient model API errors, and once a subscription's usage limit resets | Agent's model (resumes the interrupted turn) |
| `wake` Scheduled wakeups | Wake Agents waiting for deployment / CI / another Agent so they check for themselves | Agent's model (one turn per wakeup) |
| `reply_reminder` Remind when seen, unanswered (#5096) | One push when an Agent asking the user has waited `reply_reminder_minutes` (default 30) since they looked ([agent-states.md](../protocol/agent-states.md) "Awaiting your reply") | None: rules only. **On by default**, independent of the main switch |

- Subswitches work only when the main switch is on. Disabling a switch takes effect immediately: Persona-created cards are removed, and queued retries / wakeups are not sent.
- `GET / PUT /api/v1/agents/triage` (`{enabled, auto_continue, wake, decide, reply_reminder, reply_reminder_minutes}`, forwarded by the hub to the manager); changes broadcast `{"type":"agents.triage"}`. A PUT without `reply_reminder` / `reply_reminder_minutes` (or with minutes 0) keeps their values (older apps don't know them); negative minutes go back to the default 30. While the saved switches cannot be read, such a PUT saves the reminder's defaults.
- A `GET` whose first read of the database fails answers `503` with an `error` in the request's language (Chinese, English or Japanese) saying why, instead of returning the defaults as if they were the saved switches (#4553). Failures are not cached: the next request reads again. Switches once read or saved stay cached in memory; `manager.json`'s initial values apply only when the database really has no setting. Triage itself still falls back to the defaults when its own read fails, and `PUT` saves as before.
- `agents.triage` in the manager's `manager.json` (`docs/operations/config.md` ("manager.json"); in hub.json before 2026-11-06) now retains limits (`max_retries` default 3, `max_wakes` default 6, `settle_seconds` default 15);
  its four switches are initial values only when settings have never been saved in the App (omitted means off). The manager reads this section itself and lays it over the hub's handshake reply (`assistantwire.Hello.Agents`); the hub no longer reads or sends it.

## Storage

A process can be redeployed and cleared at any time, so triage results, retry counts, wakeup times, and cards are persisted (`0331_session_work.sql`, read and written only by the manager):

- `session_work`: one row per session, with the latest turn's work state (`work` JSON), turn-end time `turn_at`, next action `wake_at`, retry / wakeup counts, and card id.
  On startup the manager restores recent turns and, beyond the usual 48-hour window, CI watches with a pending wake, including prepared messages, and recently retired turns. The 30-day cleanup retains active CI watches until they finish or are cancelled, and retains recent retirement markers even when their original turn is older. A todo that ends while its agent's turn is settling records its retirement and, when available, that stopped turn's node timestamp, preventing it from registering a new wake without comparing the node's clock to the manager's. Before registering or sending a CI wake, the manager also checks the persisted todo state, so a failed retirement-state write cannot revive a watch for a still-linked closed todo. If todo state cannot be read during triage, the turn stays untriaged and the usual backfill retries it after ten minutes while it remains unread and within the 24-hour catch-up window; a later read failure for an existing watch postpones its next attempt. A later turn-start event, a newer node turn verified against the retired turn, or a reopened todo permits new work. The watch persists proof of the closed todo versions it was checked against, so it survives another restart; a later completion invalidates the proof. If the retired turn's identity and a new turn-start event are both unavailable, the manager conservatively waits for another start. Continue due actions and restore unanswered cards only if still the same stopped turn, with the same id; wait for the computer to connect before restoring cards.
- `session_work_events`: one row per triage, automatic continuation, wakeup, card creation, card answer, and wait completion (30 days), for evaluation below.

## What Persona knows

The main chat's live snapshot (`prompts/assistant/snapshot*.md`):

- Each Agent under "Needs you" includes what it is waiting for;
- Adds "Waiting" (Persona is waiting for it: retrying, deployment, another Agent) and "Done (within 24 hours)": name + conclusion;
- "Unread" contains only sessions not yet successfully triaged.

For tasks dispatched from the main chat (`internal/assistant/tasks_follow.go`), if the Agent stops on a transient error and Persona will still continue automatically (`work.on = transient` with `wake_at`), do not yet write an "Interrupted" receipt. Wait until retries are exhausted. The same holds while an Agent waits for a usage limit to reset ("Usage limits"); the snapshot shows its task as waiting for the reset, not as interrupted.

## Protocol

`proto.Session` adds `work` (`docs/protocol/protocol.md`, "Data types"):

```json
"work": {"phase":"blocked","on":"external","line":"Wait for deployment, then run production tests",
         "wait":"Deployment completes","then":"Run production tests","wake_at":"…","attempts":1,"max":6,
         "agent":{"machine_id":"…","session_id":"…","label":"…"},
         "link":{"url":"…","expires_at":"…"},
         "artifacts":[{"kind":"pr","ref":"https://github.com/…/pull/1"}],
         "turn_at":"…","at":"…","by":"rule|model"}
```

- While working, only `{"phase":"working"}`; pending interactions use `{"phase":"blocked","on":"user","line":<question>}`; sessions not triaged omit `work`.
- `resets_at`: the turn stopped on a subscription's usage limit that resets then ("Usage limits"): on `external` the wait for it, on the card after the automatic continuations when it resets.
- `GET /api/v1/agents/work/stats?hours=24`: evaluation metrics (below).

## Evaluation

`GET /api/v1/agents/work/stats` (forwarded by the hub to the manager) and manager logs (prefixed `assistant:` in hub logs, `triage <machine>/<session>: done (model 1.2s): …`):

- Distribution of post-turn triage results (done / blocked types / unsure / failure), split by rule and model;
- "Unread" count: every 5 minutes the manager samples the number of non-assistant sessions with `state = unread` (`sample` in `session_work_events`), reporting average, maximum, and current count for the interval;
- Wait duration: median / p90 from turn end to user action (viewed, replied, answered a card), split by "Done", "Waiting for your decision", and "Not triaged";
- Automatic continuation: number sent and proportion followed by normal completion; wakeups: count and proportion completed after waking;
- Cards: created, answered, wait before answer, and removed (because you replied directly).

Pre-launch baseline (evening of 2026-11-04, non-assistant sessions on the hub): 5 unread (4 within 24 hours), with a median wait already around 10 minutes.

## Clients

- Web (#1837): the list preview (`sessionPreview`: overview, Machines page, knowledge-base details, sidebar hover tooltip) switches to work state when the session is stopped and the hub has triaged it:
  "✓ Conclusion" for completed work, "What it is waiting for (wake in N minutes / retrying now)" for waits. Sessions the hub will wait for go into "Waiting" (`hubWaits`, `web/src/lib/work.ts`).
  Four switches under **Settings › General › Session triage**, with a footer link to **Model settings › Session triage** to choose the model. Screenshots use a fake hub with `MOCK_WORK=1`.
  A subscription usage limit (`turn_error_kind` `usage_limit`) is not shown as Interrupted: task cards, agent rows and the chat say "Usage limit · resumes <time>" when the CLI or the hub will continue it, and "Usage limit · resets <time>" when nothing will (the user's time zone; the date as well when that is another day). An older hub that does not send the fields keeps the interrupted text. Screenshots use `MOCK_USAGE_LIMIT=1`.
- Android (#1837): the Agents list and knowledge-base "Agents working on this project" rows use the same preview (`Session.workLine`, `WorkLine.kt` in `core:domain`). Sessions the hub waits for go into "Waiting";
  **Settings › AI › Session triage** has the main switch, triage model (link to model settings), and three action switches.
- iOS / Mac (#2895): Agents-list and space-page card previews (`Session.listPreview`) switch to the same line when the session is stopped and the hub has triaged it (`Session.workLine`, `apple/Shared/Models.swift`).
  Host-provided reasons for needing the user (botmux) and failure notices still take precedence. Sessions the hub waits for go into "Waiting", not collapsed into idle (`WorkState.hubWaits`, `Session.isWaiting`). Unit tests: `SessionWorkTests`.
  **Settings › Session Triage** (below the assistant, `TriageSettingsView` / `TriageSettingsModel`) has the main switch, triage model (opens `agent.triage` in Model Settings), and three action switches (disabled when the main switch is off). Failed changes revert to the old value with an explanation; old hubs (404) report unsupported. Unit tests: `TriageSettingsTests`; DEBUG `-tab settings -openTriageSettings YES`.
- Watch: list rows do not show last words; work previews are not displayed yet.

## Steps

| Step | Scope |
| --- | --- |
| 1 | This document; hub (moved to the manager since 2026-11-04, see "Responsibilities"): `session_work` table, triage (rules + model), automatic continuation, wakeups, cards, `state` algorithm, `work` field, statistics endpoint; snapshot; task interruption receipts |
| 2 | Move switches into App settings, all off by default; web displays `work.line`, Waiting, and switches (#1837) |
| 3 | Android (#1837); iOS / Mac work previews (#2895) and settings page (#3078) |
