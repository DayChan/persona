# Plan: event-driven delivery evidence

Generalize the shipped CI watch into a persisted condition and evidence layer, extend it from check runs to PR merge and review, and make completion consume external evidence instead of only the Agent's claims.

Status: design only. The first condition source (CI checks) already shipped as a session-scoped watch; this plan builds around it rather than replacing it.

## 1. Problem and evidence

The 2026-11-05 retrospective ran 20-plus Agents on 8 machines and shipped 6 items; most work stopped at "opened a PR", "merged but off by default" or "not deployed" (`docs/lessons/delivery.md:3-4`). Two of the three gaps from that retrospective have since been partially closed, one has not.

1. **Blind timer wakes for CI — largely solved.** PR #1931 (`feat(assistant): wake agents when PR checks finish`, commit `affd2767c`) added a rule-based CI watch: triage extracts an unambiguous PR from a `blocked · external` turn (`assistant/agents_work_ci.go:46-95`, wired in `assistant/agents_work_turns.go:261-265`), the manager polls the PR head's check runs and commit statuses without an Agent-model turn, and only wakes the session after a two-minute double observation of the same fingerprint (`assistant/agents_work_ci.go:241-342`). It is persisted inside `session_work` and delivered through the hub's once-only send guard (`assistant/agents_work_ci.go:441-530`).
2. **"Done" is still a claim, not a fact — unchanged.** The completion judge reads replies and a regex-parsed fixed delivery line (`assistant/delivery.go:56-62`, `assistant/delivery.go:110-150`); the strict `done_needs_delivery` setting remains off by default (`assistant/delivery.go:200-203`; `docs/assistant/todos.md:277-278`). Nothing in the done or delivery path reads the CI watch: `Work.CI` is consumed only by the `agents_work_*` files, never by `assistant/done.go` or the supervision package. A task whose checks are green can still be judged `undelivered` from text, and a task claimed `live` with no external proof can still close.
3. **Other external blockers are still unwatched.** There is no condition for PR merge, review approval, deployment or a time; those keeps the doubling `wake_at` schedule, at most six wakes (`docs/assistant/agent-work.md:170-176`). The CI watch itself is pure backoff polling: the GitHub connector's 60-second notification watch emits only `ScopeInbox` (`connector/connector.go:134-140`, `connector/github/inbox.go:58-70`), so a check that finishes just after a poll is noticed at the next backoff, not at the next notification.

The remaining product gap is therefore narrower than "no event-driven delivery": it is **no general condition model, no evidence that outlives the turn, and no external facts at the done gate**.

## 2. What main already provides (build on this, do not fork it)

| Capability | Where | Reuse in this plan |
| --- | --- | --- |
| Rule-based wait extraction (PR URL/number, CI intent) | `assistant/agents_work_ci.go:22-95` | Generalize the intent classifier; keep its unambiguity rules |
| Typed check snapshot with head binding and fingerprint | `connector/github/pr_checks.go:15-188` | The `checks_green` observation, unchanged |
| In-process connector access with rate-limit reset and extension fallback | `extmgr/github_checks.go:19-45`, consumed by anonymous interface assertion at `assistant/agents_work_ci.go:137-139` | The pattern for every new condition kind — no new bus method |
| Node fallback reading the current branch's PR | node `git.pr` RPC (`node/gitwrite.go:87`, consumed at `assistant/agents_work_ci.go:179-209`) | Same fallback for merge/review when the connector is unavailable |
| Backoff polling inside the 15-second work tick, concurrency cap 3 | `assistant/agents_work_tick.go:72-78`, `assistant/agents_work_ci.go:99-116` | The scheduler new conditions plug into |
| Guarded, idempotent resume with a persisted prepared message | `workGuard` (`assistant/agents_work_tick.go:119-151`), `prepareCI` / `deliverCI` (`assistant/agents_work_ci.go:441-530`) | The only resume path conditions may use |
| Turn/todo invalidation and restart recovery | `ciClosedTodo` (`assistant/agents_work_ci.go:347-397`), documented at `docs/assistant/agent-work.md:261` | Condition lifecycle rules follow the same proofs |
| Privacy boundary: guests and work machines never use the connector | `assistant/agents_work_ci.go:133-135` | Applies to every condition kind |
| Characterization coverage | `assistant/agents_work_ci_test.go` (1528 lines), `connector/github/pr_checks_test.go` | Pins behavior during the later absorption refactor |

Topology constraint: the extension manager runs **in the assistant process** (`assistant/main.go:409`, `svc.SetExtensions(ext)`). Condition observation is an in-process interface, as `ciChecks` already demonstrates — not a hub bus call.

## 3. Goals and non-goals

### Goals

- A durable, multi-kind condition registry that outlives the turn and is shared across the sessions of one to-do, with the CI watch as its first absorbed resident.
- New resume conditions without model turns: `pr_merged` and `pr_approved` first, reusing the connector's authenticated HTTP layer.
- Immutable, revision-bound delivery evidence that the completion judge consults before any text verdict; claims can be downgraded by evidence, never upgraded.
- A notification kick so conditions are re-observed when GitHub reports a change, while backoff polling and a reconciliation sweep remain the correctness backstop.
- Every increment behind a switch that defaults off, independently revertible, with current behavior unchanged while off.

### Non-goals

- **No new automatic authorization.** A satisfied condition ends a wait and nothing else. Pushing code, deploying/releasing, sending external messages, merging or modifying someone else's PR stay user decisions under the existing risky-word rules and approval cards (`docs/assistant/agent-work.md` safety section; the wake prompt still instructs the Agent to stop and ask). Conditions carry no authority to act.
- No inbound webhooks. The hub has no public address for them; the notification poll is the event-like layer. A GitHub App or public ingress is explicitly deferred.
- No replacement of the triage model, completion judge or supervisor; they receive structured facts and keep deciding ambiguous cases.
- No per-source browsing UI; evidence appears on existing session and to-do surfaces only.
- No deploy/mail/chat condition sources in this plan beyond a reserved `deploy_live` slot; to-do due dates (`0442_todo_due.sql`) already cover user-set time scheduling and are not re-built.

## 4. Design overview

```
triage (turn end)
   │  rule-based extraction (today: CI only; generalized intent classifier)
   ├─ checks_green ──▶ existing WorkCI watch (unchanged until phase 5)
   └─ pr_merged / pr_approved ──▶ work_conditions registry (new, manager DB)
                                    │
              condition watcher (existing work tick, in-process ext interface)
                                    │
              connector observation (PRChecks today; PRState / PRReviews added)
                                    ├─ pending   → next_observe_at (notification kick may shorten)
                                    ├─ failed    → evidence row; supervision/surface shows it; no auto-message
                                    └─ satisfied → delivery_evidence row
                                                  → workGuard → prepared message → SendOnce (the CI path)
                                    │
              completion judge (done_needs_delivery on)
                  fresh revision-matching evidence first; text verdict as fallback
```

The registry is not a fourth subsystem: it is the durable fact layer that triage, supervision and delivery all read, and the CI watch migrates into it once the new kinds have proven the shape.

## 5. Data model

Both tables go in the manager database; the latest migration at this writing is `0451_todo_supervision_auto_review.sql`, so the first new migration takes the next free number, checked across every worktree before opening the PR (the migration-number pitfall in `CLAUDE.md`).

### 5.1 `work_conditions` — one registered wait on an external fact

| Column | Type | Notes |
| --- | --- | --- |
| `condition_id` | text PK | `wc_<random hex>` |
| `todo_id` | text NOT NULL DEFAULT '' | Owning to-do; '' for a session with none |
| `machine_id` / `session_id` | text NOT NULL | Registering session; indexed together for turn invalidation |
| `turn_at` | timestamptz NOT NULL | Invalidation key, compared like the CI watch's same-turn checks |
| `source` | text NOT NULL | `github` now; `deploy` reserved |
| `kind` | text NOT NULL | `pr_merged`, `pr_approved`; `checks_green` joins when the CI watch is absorbed |
| `subject` | jsonb NOT NULL | `{"repo":"owner/name","number":12,"ref":"<head sha>"}` |
| `subject_key` | text NOT NULL | Canonical serialization for dedup and indexing |
| `expect` | jsonb NOT NULL DEFAULT '{}' | Source-defined expected shape |
| `state` | text NOT NULL | `watching`, `satisfied`, `failed`, `cancelled` |
| `cursor` | text NOT NULL DEFAULT '' | Last observed revision/etag |
| `observed_at` / `next_observe_at` | timestamptz | Backoff and rate-limit holder; partial index on `(state, next_observe_at)` |
| `satisfied_at` | timestamptz | Set once; a later invalidation opens a new row |
| `created_at` / `updated_at` | timestamptz NOT NULL | |

Partial unique index `(todo_id, source, kind, subject_key) WHERE state = 'watching'` gives cross-session dedup for to-do-scoped waits; rows with `todo_id = ''` include the session id in their key. No foreign key to `todos`: to-do deletion must never block condition writes, and retirement is explicit, mirroring `ciClosedTodo` rather than cascading.

### 5.2 `delivery_evidence` — immutable observations

| Column | Type | Notes |
| --- | --- | --- |
| `id` | bigserial PK | |
| `condition_id` | text NOT NULL, indexed | Remains meaningful after the condition is cancelled |
| `todo_id` | text NOT NULL, indexed | Denormalized so the judge never joins through a cancelled row |
| `source` / `kind` / `verdict` | text NOT NULL | `verdict` is `satisfied` or `failed` |
| `subject` | jsonb NOT NULL | What was queried |
| `revision` | text NOT NULL | Immutable anchor: head SHA, merge commit SHA, review id |
| `url` | text NOT NULL DEFAULT '' | Human-openable proof |
| `detail` | jsonb NOT NULL DEFAULT '{}' | Normalized facts (per-check conclusions, merge state, reviewer states) |
| `observed_at` | timestamptz NOT NULL | |
| `expires_at` | timestamptz NOT NULL | Freshness backstop for sources without revisions |

`UNIQUE (condition_id, revision)` makes replays and duplicate notifications idempotent — the same durability guarantee the CI watch's prepared-message idempotency gives today, applied to facts. Index `(todo_id, observed_at DESC)` serves the judge; freshness is checked in the query rather than with a volatile partial-index predicate.

### 5.3 Lifecycle and recovery

- **Register** at triage when the wait maps to a known kind and subject; dedup hits the watching unique index and adds a subscriber instead of a new row.
- **Observe** on the work tick; `pending` reschedules, `satisfied`/`failed` inserts evidence idempotently and transitions once.
- **Invalidate** on a changed head revision: cancel the row, keep its evidence as the audit trail, register a fresh condition carrying the subscribers over. This generalizes the head-change refusal already in `PRChecks` (`connector/github/pr_checks.go:179-185`).
- **Cancel** on a new turn, to-do completion/retirement, user deferral, or switch-off — the same events that clear a CI watch today.
- **Restart**: watching rows load alongside `session_work`; a pre-restart `satisfied` row is not resumed without the same-turn guard, exactly as a due CI wake is re-validated before `deliverCI`.
- **Prune** on the existing 30-day horizon swept in the work tick.
- **Freshness**: evidence is revision-bound; the done judge accepts it only while the revision still matches the subject and `expires_at` has not passed. A failed re-observe falls back to the text verdict — a source outage never forces either a false `done` or an invented failure.

## 6. Condition source abstraction

### 6.1 Interfaces

Manager side, in the `assistant` package (a new conditions file, named without a committed path until the implementation PR):

```go
type ConditionObserver interface {
    ObserveCondition(ctx context.Context, kind string, subject, expect json.RawMessage) (ConditionObservation, error)
}

type ConditionObservation struct {
    State            string          // satisfied | pending | failed
    Revision         string          // immutable anchor, like PRCheckSnapshot.Fingerprint
    URL              string
    Detail           json.RawMessage
    RateReset        time.Time       // zero: no quota wait; mirrors PRChecks' reset return
    NextObserveAfter time.Duration   // source-suggested backoff
}
```

The production implementation is the in-process extension manager reached through the same anonymous-interface assertion style `ciChecks` uses (`assistant/agents_work_ci.go:137-139`), promoted to a named interface. When the extension manager does not implement it, or the session is a guest or on a work machine, the condition falls back to the scheduled timer wake — the current degradation path.

Connector side, `connector/github` gains two reads beside `PRChecks`, on the same `Get` machinery, token handling and rate-limit accounting:

- `PRState(repo, number)`: merge flag and state, the same fields `ciOver` used pre-migration to end Inbox CI items.
- `PRReviews(repo, number)`: review states with ordering, so `pr_approved` means a surviving APPROVED with no later CHANGES_REQUESTED.

Typed structs are returned, not the rendered text of the MCP tools: conditions run with no conversation and must not spend tokens formatting model-facing output.

### 6.2 Extraction

The CI watch's regex set already isolates PR identity and wait intent (`assistant/agents_work_ci.go:22-42`). The generalized classifier adds two mappings from the same inputs (wait text, line, artifacts): merge intent → `pr_merged`, review/approval intent → `pr_approved`. Anything unmapped keeps the timer wake untouched. Rules stay ahead of any model change; a later optional triage-JSON field follows the prompts golden-test process in `docs/assistant/prompts.md` and is not required for the first PRs.

### 6.3 Notification kick versus polling

Three layers, cheapest first, all feeding one observation entry point so a future webhook is just a fourth layer:

1. **Notification kick.** The connector's `/notifications` watch gains a `ScopeConditions` scope next to the existing three (`connector/connector.go:134-140`), emitted when a notification's repository matches a registered subject's repo; the manager re-observes only matched conditions.
2. **Targeted backoff polling.** Each watching row owns `next_observe_at` with capped exponential backoff, evaluated by the existing work tick — no new goroutine or timer, reusing the cap-three concurrency pattern.
3. **Hourly reconciliation sweep.** Every watching row is re-observed regardless of kicks, making missed or duplicate events harmless.

While the connector reports a low rate limit, layers 2 and 3 suspend and only kicks run, matching `PRChecks`' current stance (`extmgr/github_checks.go:37-39`). Worst-case latency stays minute-scale; the win is wake tokens and fact-bearing resumes, not sub-minute latency.

## 7. State machine and evidenced done

### 7.1 Waiting

A condition-backed session holds no blind `wake_at`; the watcher owns timing. On satisfaction it goes through the full existing path — same-turn guard, todo-retirement proof, persisted prepared message, once-only send — so resume semantics stay identical to the CI watch, including the rule that user activity during an observation wins (`assistant/agents_work_ci.go:250-263`). A failed observation produces evidence and a supervision/snapshot fact but, in the first versions, no automatic Agent message: transient reds get rerun, and waking on each would recreate wake spam. Terminal failure (PR closed unmerged) stops the watch and escalates through the existing six-wake-style card path.

### 7.2 Done gate

With `done_needs_delivery` on, `judgeTaskDone` consults fresh evidence before `judgedDelivery`:

1. Load current-revision, unexpired evidence for the to-do.
2. Map it onto the existing delivery states (`live`/`pr`/`merged`/`off`/`none`, `assistant/delivery.go:21-27`). Evidence may downgrade a claim (says `live`, only a merged PR is provable → `merged`, undelivered) and never upgrades one.
3. No usable evidence — source down, unregistered kind, non-GitHub task — the current model-plus-line verdict runs unchanged.
4. Closure rule unchanged: only `live` and `none` close (`assistant/delivery.go:52`).

### 7.3 Exemptions

Read-only/no-deploy tasks keep the existing `none` classification from the dispatch prompt and need no conditions; manual to-dos never auto-complete (`docs/assistant/todos.md`); user completion and reopen always win, including the no-rejudgment-without-a-new-turn rule; guests, work machines and non-triaged sessions register nothing.

## 8. Merging the three existing logics

| Concern | Today on main | After the merge |
| --- | --- | --- |
| Recognizing a wait | Triage model + `ciWaitIntent` rules | Same; becomes the producer for every condition kind |
| Scheduling checks | `WorkCI` polling for CI; doubling `wake_at`, max 6 otherwise | Registry watcher for registered kinds; timers only for unmapped waits and transient retries |
| Resume send | `workGuard` + `prepareCI`/`deliverCI` | Unchanged; all conditions use it |
| Delivery stage | Settings blob `assistant.todo_stages` | Stays for UI; evidence lives in its own table |
| Completion verdict | Text-only `judgedDelivery` | Evidence-first, text fallback |
| Supervision | Session-state signals, no external facts | Reads condition/evidence state and cites its URLs |
| Safety | Risky words + approval cards | Unchanged; conditions only end waits |

A new `agents.triage.conditions` switch joins the Session Triage group (default off), controlling registration and the watcher; `done_needs_delivery` stays separate, so the evidence gate in the judge requires both switches. Switch-off cancels watching rows in one transaction, mirroring the existing triage switch cleanup.

## 9. Phased rollout

One issue and one PR per phase; phases 1-3 need no client release; rollback is always the switch.

- **Phase 1 — inert registry (one small PR).** Both migrations and store methods; the triage path records `pr_merged`/`pr_approved` rows behind the switch with dedup, restart restore and prune; rows visible in logs and the stats endpoint. No observation calls and no behavior change: timer wakes fire exactly as before, and the CI watch is untouched.
- **Phase 2 — observation and resume for merge/review.** Named observer interface; `PRState` / `PRReviews` in the connector and extension manager; watcher in the work tick with backoff, rate-limit suspension and the hourly sweep; satisfied conditions resume through the guarded prepared-message path.
- **Phase 3 — evidenced done.** `delivery_evidence` consumed by the completion judge under both switches; failed/stale handling; supervision and blocker lines cite evidence; metrics.
- **Phase 4 — notification kick and clients.** `ScopeConditions`; the additive condition block on `WorkState`; evidence chip and to-do evidence section on web, iOS/Mac and Android with three-language strings and dark/light screenshots per `docs/operations/testing.md`; settings entry; the Watch app stays untouched.
- **Phase 5 — absorb the CI watch.** Migrate `WorkCI` rows and scheduling onto the registry with `checks_green` as a native kind, keeping `assistant/agents_work_ci_test.go` green as the characterization suite; only then remove the embedded field. `time_at` and the reserved `deploy_live` slot follow. This is deliberately last so the proven watch is refactored only after the new scheduler has earned trust.

## 10. Testing strategy

Server tests run through `scripts/test/test-server.sh` against the real Postgres container.

- Store/lifecycle: dedup across sessions, cancel on turn change and to-do retirement, prune horizon, idempotent evidence inserts under concurrency.
- Restart recovery: watching rows reload; a satisfied row never double-resumes; due observations respect jitter with no thundering herd.
- Out-of-order events: satisfaction on one SHA, then a new head cancels and re-registers; a late event for the old revision never flips the new condition.
- Duplicate events: repeated notification and identical revision produce one evidence row and one resume, reusing the once-only send guard.
- Stale evidence: expired or wrong-revision evidence is rejected at the done gate; one synchronous re-observe, then text fallback when the source is down.
- Connector contract: fake GitHub over the existing test APIRoot injection with scripted sequences (queued→success, push invalidation, approved→changes-requested, merge then close), plus rate-limit header handling — the patterns already in `connector/github/pr_checks_test.go`.
- Safety matrix: every risky word over a satisfied condition still yields a user card, never an automatic send; guests and work machines always fall back to timers.
- Switch semantics: off means no rows, no observer calls, byte-identical timer behavior; mid-wait on→off cancels cleanly.
- Phase 5 absorption: the existing CI watch suite passes unchanged before and after the migration.

## 11. Client impact

Phases 1-3 change no client payloads; evidence URLs can ride existing artifacts until phase 4. Phase 4 adds one optional `WorkState` object and an optional to-do evidence list, both additive and ignored by old clients — the same approach the delivery blocker field used (`docs/assistant/todos.md` Delivery section). The UI is a state chip on the session row and an evidence section in to-do details, web first; every token and string follows the design system and the en / zh-Hans / ja catalogs. The settings row follows the existing Session Triage pages and hides on a 404.

## 12. Metrics

Extend `GET /api/v1/agents/work/stats` (route in `assistantwire/agents.go`) and the work event stream:

- Conditions registered by source/kind, and the share of external waits covered versus timer fallback.
- Register-to-satisfied latency p50/p90, split by kick, backoff and sweep.
- Wake tokens avoided: counterfactual timer wakes from `check_minutes` minus condition-driven resumes, reported alongside the existing wake counts.
- Evidence-backed versus text-only done judgments, and reopen rate after each.
- Observation failures, rate-limit suspensions, stale-evidence re-observations.

## 13. Open decisions

1. **Absorption timing for the CI watch.** Keeping `WorkCI` and the registry side by side avoids touching a proven feature but means two schedulers and two extraction paths; absorbing it in phase 5 is cleaner but is the riskiest single refactor in the plan. Leaning: keep phase 5, and only start it once phases 2-3 have run stable for a release.
2. **Multi-subscriber resume.** Dedup gives one watching condition per to-do and subject; when several sessions subscribed, satisfaction must decide who resumes — all still on the same turn, or one designated session. The guard filters most stale cases, but the product semantics need a call.
3. **Required-context semantics.** The shipped `PRCheckSnapshot` already settled the neutral/skipped/cancelled question (`connector/github/pr_checks.go:33-39`: neutral/skipped pass, cancelled fails); the open question is whether branch-protection required contexts become a per-repo option, given the extra token scopes and configuration that path needs.
4. **Freshness at the done gate.** Revision binding covers code changes; undecided are the default `expires_at` for non-revision sources and whether the judge may block on one short synchronous re-observe or only accept stored evidence fresh enough. Leaning: 24-hour TTL, one bounded re-observe, then fallback.
