# App state and redraws (iOS)

"Recently Used" is an independent grouping option in the Agents list (`dev.grouping=recent`): a flat list across projects, including seen and offline history.
It uses `AgentListOrder.recent` to sort by `updatedAt`; other groupings still use `sortDate` (`stateSince` for working sessions).
`AgentsFeed.recent` switches the structural projection's time source; `Shape.byTime` stores only that order: reorder when progress moves a working session past another, but when time advances without changing order, update only the row's timestamp.
While scrolling or covered by another page, the existing Hold / ReaderAnchor still controls when changes land. Opening a chat or marking it seen doesn't hide it in the idle fold.

The hub keeps pushing (measured on a real hub: sessions on one machine every 2 seconds). The UI needs to do three things:
- redraw only the bit that changed;
- not interrupt the user's current action;
- always tell the truth on screen, promptly.

This document covers:
- how data currently travels from push to screen;
- how many views one push actually recomputes (measured);
- the root causes;
- a staged fix, with acceptance criteria for each step;
- how to define and test "true and timely".

Background: `docs/archive/apple/live-updates.md` (three instances of "push interrupts an action").

## Current data flow

```
WebSocket ──decode (background thread)──▶ AppModel.handle(_:) (main thread)
   sessions   ─▶ sessionsByMachine[machine] = whole list (assign only when old != new)
                   didSet ─▶ allSessions (concatenate all), sessionIndex, each session's SessionCell, saveSnapshot (2-second debounce)
   machines   ─▶ machines (assign only if different)─▶ machineChoices (changes only when names or icons change)
   interaction / interaction_resolved ─▶ interactions[id]
   revs       ─▶ AgentSync (fetch chats in the background, write chat cache)
   items / delta ─▶ that chat's ChatStore
   lark / inbox / fleet ─▶ LarkSync, InboxStore, Dynamic Island
                                 │
                                 ▼  @Observable: a view recomputes when any property it read changes
  view body ── reads models directly (model.allSessions, model.machines, model.session(key)…), sorts, groups, and counts inside body
```

- `AppModel` is a 1,300-line `@Observable` holding sessions, machines, pending actions, current tab, drafts, and style.
- A sessions push is "one machine's entire list".
  - A single `Session` struct holds identity and layout fields (id, title, space, slot) as well as fields that change every time (`rev`, `updated_at`, `last_text`, sub-Agents).
  - On the real hub, every `sessions` push contains a new `rev` / `updated_at` (255 pushes in 10 minutes; `rev` changed 320 times, `updated_at` 243), so `old != new` is almost always true.
- `SessionCell`: one observable cell per session. Views of one session (chat pages) follow only that session through `model.session(key)`.
  Views of all sessions read `allSessions` and recompute whenever any session on any machine changes.
- Derived data is computed inside body:
  - Agents list grouping, sorting (`SpaceGroup.build`, `AgentListOrder`), and filter counts are rebuilt from `allSessions` on every body;
  - badges (`agentsWaiting`) are computed properties; their readers recompute on every push.
- Side effects: `didSet` on `machines`, `sessionsByMachine`, `interactions`, and `assistantConfig` all call `saveSnapshot()`, already debounced by 2 seconds (each push only cancels and reschedules a Task).
  The actual storm was `AgentSync` writing UserDefaults on every chat fetch, recomputing every `@AppStorage` view in the App; fixed in #215.

### Who reads what

| Property | Views reading it (files) | Changes on every push? |
| --- | --- | --- |
| `allSessions` | Agents list (via `AgentsFeed` after #215), chat page, assistant page, space page, machine's space page, Inbox actions, session panel (7 files) | Yes |
| `sessionsByMachine` | Assistant profile, machine details, port presence (`PortPresence`), `AgentSync` | Yes |
| `pendingInteractions` / `interactions` | Agents list, Inbox, shared root-tab badges, shell home page | When someone asks / answers |
| `machines` | Settings, Machines page, new chat, Dynamic Island settings, assistant page | Connect / disconnect / `last_seen` |
| `namedMachine(_:)` / `namedMachines` (unobserved `machinesNow`, follows only `machineChoices`) | Chat page (subtitle, computer-control progress), session info | Add / delete / rename |
| `siblings(of:)` (one `SpaceCell` per space) | Chat page, space tab strip | When tabs in that space change |
| `session(key)` (`SessionCell`) | Chat page, row menu | Only when that session changes |

### Cost of one push

`RenderBudgetTests` (mock, page at rest, average of 5 pushes per type): each push is that machine's entire session list, with only one field of one session changed.
The tables retain historical measurements of shell and tab navigation; they are not selectable iPhone modes today. Current compact iPhone screens always use the shell, and regular-width iPad screens retain their sidebar.
- `rev`: the chat grew, but nothing on screen changed (most pushes on the real hub are like this);
- `last`: an Agent's last line changed.

| Page | Layout | One `rev` | One `last` | Views recomputed |
| --- | --- | --- | --- | --- |
| Agents list | New shell | 5 | 5 | `AgentsList`, `SpaceHeader`, `SlotRow`, `StateCounts`, `AssistantSidebar` |
| Agents list | Historical tab navigation | 4 | 4 | Same, without the sidebar |
| Agent chat | New shell | 5 | 5 | `ChatView`, `ChromeHeader`, `SessionModeBar`, `Composer`, `AssistantSidebar` |
| Agent chat | Historical tab navigation | 4 | 4 | Same, without the sidebar |
| Persona chat | Both | 1 | 1 | `AssistantSidebar` (the assistant's session sidebar reads all sessions; in the new shell it exists on the Agents page too) |

How to read this:
- Only views instrumented with `PerfTrace.body` are counted; `List` cells and system controls aren't included, so the actual count is higher;
- `rev` costs as much as `last`: a push with no visible change costs the same as one with a visible change. That's "granularity too coarse" and "volatile and stable data mixed together";
- the chat page recomputes even when the push changes a different session (`ChatView` reads `allSessions` for mentions and quotes, and several layers underneath follow);
- one `SlotRow` each time: rows are already `Equatable`, so only the changed row redraws; the costly part is the layers above it;
- steps 1–3 target zero views for `rev`, and only the row displaying that line for `last`.

After step 1 (same test):

| Page | Layout | One `rev` | One `last` | Views recomputed |
| --- | --- | --- | --- | --- |
| Agents list | New shell | 2–2.6 | 2–2.6 | Changed `SlotRow`, `AssistantSidebar`; one list reorder on the push crossing a whole-minute boundary (below) |
| Agents list | Historical tab navigation | 1–1.6 | 1–1.6 | Changed row; one list reorder on the push crossing a whole-minute boundary |

- Remaining list reorder: the mock sends `updated_at` as "now" every time; crossing a whole-minute boundary changes the space header's "n minutes ago", requiring structure computation (see "Step 1").
  Exactly 1 of 5 pushes does this. Whether it lands in `rev` or `last` depends on the second of the run, so both columns are 2–2.6 (Historical tab navigation 1–1.6).
- `AssistantSidebar` reads all sessions and exists on every new-shell page; left for step 3.
- Chat pages are unchanged from step 0 (new shell 5, Historical tab navigation 4; step 2's work). One isolated run measured 7 Agent chat bubble redraws on a `rev` push (Historical tab navigation 1.4),
  but the full matrix rerun was 0 and it didn't recur. Chat pages don't read `AgentsFeed`; step 2 adds a bubble-count limit and checks again.

After step 2 (same test; wait until the page stops redrawing before measuring, see "Step 2" below; two runs per layout):

| Page | Layout | One `rev` | One `last` | Views recomputed |
| --- | --- | --- | --- | --- |
| Agents list | Both | 1–1.6 | 2–2.6 | `rev`: only that row's timestamp `AgentTimeText`; `last`: that row's `SlotRow` and timestamp; one list reorder on the whole-minute crossing (0.6) |
| Agent chat | Both | 0 | 4 | `last`: `ChatView`, `ChromeHeader`, `SessionModeBar`, `Composer` (this session's last line changed; `SessionCell` publishes) |
| Persona chat | Both | 0 | 0 | |

- The "7 bubbles" came from measuring too early: `rev` was measured first on every page, while the freshly launched App was still loading history and laying out the chat; bubble body work was attributed to the first push.
  Now `RenderBudgetTests` waits for the count to stay unchanged for 1 second before measuring; both runs in both layouts give 0.
- `AssistantSidebar` no longer appears: when collapsed it doesn't even read the "Archived" count (which also traverses all sessions).
- Chat `last` remains 4: the last line is session content, so `SessionCell` still publishes; none of those four views displays it. Step 3 splits what they read more finely.

After step 3 (same test, both layouts):

| Page | Layout | One `rev` | One `last` | Views recomputed |
| --- | --- | --- | --- | --- |
| Agents list | Both | 1 | 2 | `rev`: that row's `AgentTimeText`; `last`: that row's `SlotRow` and timestamp; no reorder on whole-minute boundaries |
| Agent chat | Both | 0 | 0 | |
| Persona chat | Both | 0 | 0 | |

At this point, a push changing one field of one session recomputes only the leaf views displaying that field: the goal of steps 1–3 is met.

## Root causes

| Category | Current state | Consequence |
| --- | --- | --- |
| Granularity too coarse | One machine's entire session list is replaced together; readers of `allSessions` follow any session change | One session changes a line, every list page and the assistant page recomputes |
| Volatile and stable data mixed | `rev`, `updated_at`, `last_text`, title, state, and space share one struct | A view interested only in "did state change?" is woken by `rev` churn |
| Derived data computed in body | Sorts, groups, counts, and badges recomputed from the whole dataset inside body | Computation is expensive, runs even when the result is unchanged; sort keys use volatile fields (before #215, spaces sorted by `updated_at`) |
| Side effects in didSet | `saveSnapshot` is debounced; `AgentSync` writes UserDefaults (fixed) | One write recomputes the whole App |
| Multiple truths | Current page: `AppModel.selectedTab` and shell `ShellState` each hold a copy, synced by `onChange` (the live lane is making it a single source) | Updates can be missed; UI and state disagree |
| Each page handles "user is interacting" itself | Separate menu, long-press, and scroll patches (#131, #178, #215) | Miss one place and the problem returns |

## Plan (staged, one PR per step, numerical acceptance)

| Step | Work | Acceptance (numbers) |
| --- | --- | --- |
| 0 (#243, merged) | Screen probes + truth / timeliness tests + don't hold critical updates + land within 0.5 s / 5 s maximum; measure the baseline | `ScreenTruthTests`, both layouts × both styles, green for 3 consecutive rounds; numbers in "Cost of one push" above |
| 1 (#338, merged) | Publish Agents list structure separately from rows ("Step 1" below): each row follows its own Agent (`AgentsFeed.Row`), and the list reorders only on structural changes | A push changing one field in one session: recomputed views ≤ leaf views reading that session + 1; zero list body calls if structure is unchanged |
| 2 (#620, merged) | Split volatile fields: put `rev`, `updated_at`, tokens, ports in separate small objects, read only by the leaf views displaying them (relative time, last line), following `WaitingBadge` | Pushes churning `rev` / `updated_at`: zero views recomputed except the `Text` displaying time |
| 3 (#639, merged) | Move derived data into models: sorts, groups, filter counts, badges computed in the model and published only when results change (`AgentListOrder` moved into `AgentsFeed`) | List body runs only when order / groups / counts change; badges run only when the number changes |
| 4 (#880, merged) | One model-layer policy for "user is interacting": during dragging, inertia, open menus, or keyboard input, merge and defer noncritical updates; critical updates go immediately; maximum delay bounded. Extract `AgentsFeed` holding into reusable `Hold`, with pages registering their state | Existing `LiveUpdateTests`, `ScrollUnderPushTests`, `ScreenTruthTests` all green; no page-specific holding code remains (source check) |
| 5 (#913, merged) | Guardrails: performance test "a push changing one field of one session recomputes at most N views", failing above the limit; source check "non-list views don't read `allSessions`" | Both checks in `PersonaTests` / UI tests |
| 6 (#270, merged, testflight lane) | Hub pushes only semantic changes (`rev` / `updated_at` churn goes via `revs` instead), or sends deltas. Implemented as `sessions_delta`: the hub remembers sessions sent per device and sends only changes and deletions | Session push traffic: 2605 KB/min → 79 KB/min |

### Step 1: separate list structure from rows

The original plan was a model-level `SessionStore` (one object per session, field-level diff). The most expensive part was actually the Agents list body (grouping, sorting, folding, counting), so split these first in its `AgentsFeed`:
- One `AgentsFeed.Row` (`@Observable`) per Agent; rows (`SlotRow` / `CompactSlotRow`) read it, and a push updates only the changed `Row`s;
- `sessions`, read by list body, is replaced only when structure changes: compare `AgentsFeed.shape`, i.e. sessions with row-only fields removed (`rev`, last line, ports, queue, suggestions, modes, model, seen time; time rounded to minutes; sub-Agents and background tasks reduced to whether any are running);
- during search the list filters by last line, so last line becomes structural (`searching`);
- holding and critical bypass stay: `Row`s and structure land together, and critical updates update only listed rows too (`landCritical`);
- `probe.agents-list` reads each row (`AgentsListProbe`), measuring what's drawn, not the list structure.

The model-level `SessionStore` was to wait until the testflight lane's session deltas (step 6) merged. #270 finished deltas (`sessions_delta`; `SessionPatch` merges them back into `sessionsByMachine` in the App). After steps 1–5, list and chat recomputations were already limited to leaf views, so `SessionStore` wasn't built.

### Step 2: `rev` and `updated_at` wake only the timestamp view

Every step a working Agent takes causes a whole session-list push, where only that session's `rev` (transcript grew) and `updated_at` change:
- `Session.sameApartFromTicks`: treat it as unchanged if all other fields match;
- the model's `SessionCell` (`AppModel.session(_:)`, read by chat pages, top bars, and menus) publishes only when other fields change. Its `rev` and `updatedAt`
  may therefore lag the list: places needing those values (`AgentSync` fetching transcripts by `rev`, sorting, sidebar timestamps) read the whole list (`allSessions`), not the cell;
- likewise for `AgentsFeed.Row.session` in the Agents list; row time is stored separately in `AgentsFeed.Row.time` (`AgentTime`), read only by `AgentTimeText`,
  which refreshes "n minutes ago" itself every 30 s;
- the new shell's collapsed sidebar doesn't read the "Archived" count (like the session list, only read when opened).

Truthfulness: time updates on every push (`AgentTime` lands with `show()` and is held with the list); states, last lines, and questions still go through cells and rows.
`ScreenTruthTests`: 3 rounds × 2 layouts × 2 styles, all green.

### Step 3: derived data into the model layer

- Chat pages don't recompute for the last line: the session cell (`AppModel.session(_:)`) treats the last line as "unchanged" too (`Session.sameForItsPage`), like `rev` and `updated_at`,
  since chat pages, top bars, and menus don't display it. Actions sending "how this session is doing now" (handing to Persona with the last line) read `AppModel.latest(_:)` when tapped:
  unobserved, directly taking the latest whole session from the model. Cell values may be old: **cells for display, `latest` for sending**;
- lists don't reorder with time: `AgentsFeed.shape` no longer includes minute-rounded time in structure; it compares the order determined by time (`Shape.byTime`: ids sorted by `sortDate`).
  Row and space order is determined only by this sequence; when a minute passes without changing order, list body doesn't run. Working Agents sort by when they started working, so each step leaves order unchanged.
  Sessions in the list may therefore have old `updatedAt` values: the only row displaying this, an idle space (`QuietSpaceRow`, `QuietSpaceCardHead`, `CompactQuietSpaceRow`),
  gets the latest from per-row times (`AgentsFeed.time`, accessing feed through the environment);
- badges computed in the model: `AppModel.badges` (`TabBadges`: Persona and Agents counts) recounts when pending items, sessions, or mutes change, publishing only when the numbers change;
  `RootView`, `MacRootView`, and the new shell's `WaitingBadge` read only these two numbers. `LiveUpdateTests.testTheAgentsBadgeIsTheHubs` checks that the badge matches the hub,
  adds one within 2 s of a question, and subtracts one when answered.
- The original plan to move all of `AgentListOrder` into `AgentsFeed` was deferred at the earlier measurement checkpoint.
  Retained shell tabs can also re-evaluate the body on selection or environment changes without a structural revision;
  #1231 below separates projection lifetime from those body evaluations.

- The list's derived data is an immutable `AgentListProjection` (#1231): filters and counts, pins, the Repository/Project tree,
  repository headers, quiet flags and those sections' row entries, built once per published key (`AgentListProjectionCache.Key`:
  the feed's structural `revision`, every hub group's link identity, address and token, and small settings such as grouping,
  filter, search, pins, mutes and the held row). An unchanged retained page reads the published value without touching the
  inventory. The first value, settings changes, critical landings, appended pages and credential changes are derived at once
  on the main actor. Ordinary structural revisions, including what a released hold lands, are derived off the main actor from
  a value snapshot by one serial worker. One derivation runs at a time and only the newest request waits behind it, so a
  stream of revisions faster than one derivation still converges. A result publishes only if no immediate change or
  credential switch superseded it, nothing newer has published, its partitions and settings are still the live ones, and
  the list is not held, or the same scrolling activity authorized a capped landing; a later gesture or visibility edge
  revokes that permission. `ReaderAnchor.willChange` runs just before it publishes.
  Paging counts are overlaid when headers are drawn, so their arrival does not derive the inventory again.

### Step 4: one policy for "user is interacting": `Hold`

- `Hold` (`apple/Persona/App/Hold.swift`) is the sole, page-independent holding policy:
  - two reasons: scrolling (`scrolling`) and not visible (`outOfSight`);
  - scrolling lasts from the first touch until the list has been idle for `Hold.settle` (0.5 s); another touch in between continues the hold;
  - scrolling holds wait at most `Hold.cap` (5 s), then land anyway; invisibility has no cap and lands immediately on return;
  - the data owner decides what is critical: `Hold.wait()` says only "should this wait?"; critical parts still land immediately while waiting (Agents' `critical` / `landCritical` unchanged).
- Pages only register state: the Agents list reports scroll phase (`feed.hold.set(.scrolling, phase != .idle)`) and invisibility (`feed.hold.set(.outOfSight, covered)`) to the feed's hold,
  no longer storing scroll phase or waiting 0.5 s itself. "Not visible" reads the single page state: `AppModel.selectedTab` (also written when the new shell changes pages) and whether the shell pager is moving.
  On Mac, section pages stay in the window (#390, `MacPageStack`); when hidden behind another section they additionally report `.behind` (`PageFrontWatcher`, `tabIsActive`), independent of "not visible", also uncapped.
- `AgentsFeed` now holds only data logic: what is critical and how to land just the critical portion; `Hold` decides when to land.
- One behavioral change (invisible): if a scrolling page is covered (open a chat from the list), once scrolling stops the 5 s cap timer stops; returning to the list lands immediately. Previously the timer kept running and landed on the hidden page when due.
- Menus, keyboard: the plan also made "menu open" and "keyboard input" holding reasons, but no place needed them: row long-press menus are `Equatable` (`AgentRowMenu`) and pushes don't rebuild them;
  typing in Agents search must filter immediately; the chat composer doesn't redraw with pushes. If a page really needs it later, add a `Hold.Reason` and register it.
- Chat `frozen` isn't a hold: new content isn't inserted while reading history because of the reader's position (only inserted upon returning to latest, with a ↓ meanwhile), not `Hold`.
- Checks: `HoldTests` (release only at rest, 5 s cap, no cap while invisible); `HoldSourceTests` scans sources: no self-managed waits (`Task.sleep`, `asyncAfter`) in `.onScrollPhaseChange`,
  and no timers in model-following feeds (`withObservationTracking`) under `Views/`.

### Step 5: guardrails

Two checks preserve steps 1–3; regressions turn tests red:
- `RenderBudgetTests` changes from reporting counts to failing above budget: with the page at rest, a push changes one field in one session (average of 5 per type); views recomputed per push must stay within budget.
  Budgets are the post-step-1–3 numbers: Agents list `rev` 1 (that row's time), `last` 2 (that row and its time), Agent chat and Persona chat both 0;
  one extra call across 5 pushes is allowed (0.2, a row's time happens to cross its refresh boundary); two extra calls count as regression.
- `AllSessionsReadersTests` (PersonaTests, source scan): only Agent-listing views under `Persona/Views` and `Mac` may read `allSessions`; each file's read count and reason are registered in the test,
  and adding or removing a read requires updating the registration:
  - single-session displays read `model.session(_:)` cells;
  - actions needing all sessions only when tapped (@ menus, quoting another session, next / previous Agent, terminal space switching) read `model.sessionsNow`: the same data as `allSessions`, unobserved,
    like step 3's `latest(_:)`: **observed for display, unobserved for actions**;
  - register new lists with a line explaining why they need it.
  - This check sees only `allSessions` written directly in views, not inside model methods: `siblings(of:)` read it, making chat pages and tab strips recompute on every push (2026-11-04, #1337).
    Likewise, places displaying only a machine name should read `namedMachine(_:)`, not `machine(_:)` / `machines`: those change on every machine heartbeat (`last_seen`).
    The chat subtitle and "Operating your computer" read it, recomputing the whole page and all rows every heartbeat (measured on Mac: 20 heartbeat rounds, 30 chat-page calls, 365 bubbles and bodies).

Every step must pass the "Truthfulness and timeliness" tests below, without changing visible interaction behavior. Align with the live lane's `ShellState` (single current-page source): step 4's "user is interacting" reads its page state rather than storing another copy.

## Truthfulness and timeliness (acceptance criteria for every step)

The user's requirement: fewer redraws must not come at the expense of correctness; the screen must tell the truth, promptly.

### True (consistent)

- **Hub** data is the source of truth, not the App's model:
  - mock `GET /mock/truth`: every Agent's state, last line, title, pending actions;
  - REST endpoints for a real hub.
- On-screen state is exported by probes (`probe.<region>`, below):
  - Agents list: each Agent's state and last line, listed questions;
  - connection: "Waiting for Network / Connecting… / Updating…" in the title.
- Consistency: at rest, every exported probe value equals the hub's. Only "noncritical" fields may lag, and their delay is bounded (table below).

### Timely (bounded)

| Update | Examples | Limit | While scrolling / dragging |
| --- | --- | --- | --- |
| Critical | An Agent starts / stops needing you, a question or approval arrives / is answered, an Agent goes offline, a machine goes offline or errors | On screen within 1 s after push | Never held, shown immediately, and only this critical portion (below). The list may still move under the finger: a card appears at the top, a space moves up; that is the cost of immediate display |
| Noncritical | Last line, order changes, time, tokens | Within 0.5 s after the finger stops; at most 5 s even during continuous scrolling | Merged and deferred |
| Page invisible | Covered by a chat, on another page | Lands immediately upon return | No redraw |

`Hold` owns what waits and for how long ("Step 4" above). A critical update during a hold lands only its critical portion (`AgentsFeed.landCritical`):
- Land: states of the Agents starting / stopping needing you or going offline, updating already-listed rows; questions; machines;
- don't land: last lines, times, order; still wait for the finger to stop or the 5 s cap;
- why: initially everything held landed together; changed last lines altered row heights and re-laid out the whole list while scrolling, producing a measured 104 pt jump in Historical tab navigation (`ScrollUnderPushTests` failed, 10 list redraws during scrolling).

Why wait 0.5 s after stopping:
- consecutive flicks pause only half a second to a second between them;
- if changes land between flicks, `List` lays out only when the next finger touches, causing a jump.

- `PerfScroll` separately labels this jump `landed`: within 0.3 s of page release, or within the first 0.5 s of the next scroll after release at rest. The update landed at rest as designed, so it isn't counted as a scrolling interruption.
- But the user sees it: stop, updates land, then content moves at the next finger touch (occasionally hundreds of pt on the mock).
- The cause is `List`'s estimated row heights: landing changes offscreen row heights, and UIKit re-lays out with actual heights only at the next scroll. The earlier 1.5 s delay avoided this; now it's back to 0.5 s,
  with the root fix in steps 1–3: landing changes only affected rows, without rebuilding list structure and row heights wholesale.

### Convergence

After any push sequence combined with any actions ends, the screen returns to hub state within the limit.

- Actions include scrolling, dragging, opening menus, switching pages, entering / leaving chats, foreground / background, disconnect / reconnect.
- Missed updates have a fallback: refetch sessions after reconnect (`refresh` after `hello`). Things happening on the hub while offline must appear after reconnect too.

### Snapshot refresh ownership (#1634)

Todo and session invalidations use `apple/Persona/App/CoalescedRefresh.swift`: one active read and one latest dirty follow-up, without a fixed cooldown or a new freshness delay. Hints received during a read merge; the trailing read reconciles them. A fast read that finishes before the next hint may still produce one GET per hint. Single-flight is not a request-rate quota, and reducing full todo bodies further depends on a compatibility-preserving manager-side delta/conditional protocol.

Manual reads share an eligible active or queued read. Post-write confirmation cannot join a GET started before the write; todo publication is fenced by hub generation and local mutation revision. A cancelled UI waiter does not cancel another owner's shared read. Reset ends old waits and rejects late old-generation data. Session refresh preserves its frozen paging window and delta checkpoint; a manual refresh absorbs the existing short hint-merging task without swallowing its last invalidation.

Critical paths and the timeliness table above remain unchanged. Unmarked hints are not guessed critical or discarded. Request metadata uses a bounded execution-surface/version header, not a device/account/bundle identifier or authentication claim. Default URLSession gzip negotiation remains enabled; video byte-range requests retain their intentional identity encoding.

### Display work and foreground polling (#1634)

Display-only clocks and decorative frames run only while mounted, scroll-visible, on the selected tab and in an active iOS/visionOS scene. Return uses the current timestamp, not a frozen business clock. Status-ring interpolation belongs to Core Animation, while the built-in face keeps its original drawing and nominal cadence with a periodic schedule. Relative-time/elapsed labels do not keep hidden periodic timers alive. Recording, transfer and other explicitly background-capable operations use their own lifecycle.

`tabActiveTask` also cancels foreground polling at scene inactivity. `onTabVisibility` deliberately retains its original tab semantics: changing every disappearance callback to scene-driven disappearance would clear voice-call continuation state. Mac window-focus behavior is not treated as phone backgrounding.

### No swallowed actions

- During a deferred reorder, a tap must target the row the user saw: order stays unchanged while held, and the tap acts on the displayed row.
- Landing happens after 0.5 s at rest, not on finger-down.

### Keep the row being read in place

- When updates land with the list stopped mid-way (order changes, spaces move up/down, a card inserted at the top, an earlier row's last line grows to two lines), the row the reader is looking at stays put; changes happen above and below it.
- Implementation: `ReaderAnchor`. Each row reports its window position; before changing the list, `AgentsFeed` records the row being read (the one crossing the center line) and its position, then scrolls back by the displacement after layout:
  - correct on every position report for the next 1 s: after reordering, `List` takes time to lay out earlier rows at real heights; correcting only once can leave tens of pt of error;
  - if reordering moves the row entirely offscreen (its space sinks far below), its cell disappears and stops reporting: first scroll to it with `ScrollViewReader`, then calibrate against its reported position;
  - keep the same reader row until the user scrolls: choosing the center row again for every consecutive reorder can select a neighbor and drag the list away.
- Don't preserve it at the very top: new arrivals should be visible there. Don't preserve it with a finger down or while sliding either (only critical updates land then, above).
- Why (#339): after reordering, `List` preserves scroll offset, not content. Earlier spaces change rank and the reader's whole screen becomes a different space; the order is true, but what they were reading is gone.
- Acceptance: `LiveUpdateTests.testScrolledListHoldsStillThroughUpdates` prints `TRUTH reader row moved … pt`; both layouts must stay within 2 pt (before the fix, the row disappeared entirely in Historical tab navigation).

### Visible staleness

While offline, reconnecting, or filling data gaps, the title shows "Waiting for Network / Connecting… / Updating…" (`ConnectionStatus`, shown after 1.5 s to avoid flashing).

## Probes and assertions (shared by msg-render and live)

App side: `apple/Persona/App/ScreenProbe.swift`, DEBUG, mounted only with `-probes YES`:

- `view.screenProbe("<region>", page:, value)`: `value` is `Encodable & Equatable`.
  - Mounts a 1×1 element with `accessibilityIdentifier("probe.<region>")`;
  - `accessibilityValue` is single-line JSON: `{"area","seq","at","page","v"}`.
- Fields:
  - `seq`: increments each time content changes;
  - `at`: last change time in milliseconds. The simulator and Mac share a clock, so tests compare it directly with their push time, including changes while a finger remains down;
  - `page`: which page this region belongs to in the new shell. Both pages are built; tests select by it.
- Regions:
  - `agents-list`: `{states, last, asks}`;
  - `agents-scroll`: position at the top of the Agents list, read from UIKit every frame (`AgentsScrollProbe`), in window coordinates:
    - `now`: current positions `{nav, search, inset, row, container, safe}`: navigation bar y, search-field height (0 when collapsed), top list inset, first-row y, navigation-container y, top safe area;
    - `left`: positions in the frame before pushing another page;
    - `back`: frame-by-frame record of the latest return, from its start through 1 second afterward, each item `{min, max}`; `rowStep` is the largest first-row jump between adjacent frames, `n` the return count, `frames` the frame count.
    - See `AgentsReturnTests`: after return, `row`, `nav`, and `search` equal `left`, with ranges ≤ 1pt; `container` and `safe` unchanged.
    - `room`: frame-by-frame record of the latest expansion / collapse of the shell status row, from start through 1.2 s afterward, same fields as `back`, plus `to` (space made, 0 when collapsed); `row` follows the first on-screen row at the start, so newly exposed rows above aren't counted as jumps.
      See `ShellRoomScrollTests` (#413).
  - `connection`: `{shown, network}`;
  - `chat`: `{key}`, identifying the open chat (`page` is its page);
  - `shell`: new shell (`ShellShown`, `docs/apple/shell.md`), `{strip, selected, tab, position, moving, lingering, drawerOpen, chatRoot, workRoot, status, opened, folded, room, trace}`:
    where the strip rests (`moving` during a slide), selected switcher page, App tab, status-row content (`none` / `link` / `toast`), expansion/collapse counts, space made; `trace` holds recent shell events.

Test side: `apple/PersonaUITests/ScreenProbe.swift`:

- `app.probe(area, as:, page:)`, `app.probeSeq`, `app.probeChanged` (`at` converted to `Date`);
- `eventually(what, within:, poll:) { differences }`: repeatedly reads until there are no differences, returning elapsed time; timeout fails with the final differences. `within: 0` means "consistent now";
- `differences(shown:truth:deferrable:)`: compares item by item, excluding fields in `deferrable`.

Tests: `ScreenTruthTests` (`docs/operations/testing.md` ("Truthfulness and timeliness")).

## Chat diagnostics (Copy Diagnostics, #3783)

Probes exist only in DEBUG. When a reply on TestFlight "shows while streaming, disappears after completion, returns only after reopening the chat", afterward it's hard to tell whether it moved into a drawer, was frozen and waiting, or was overwritten.
So each chat keeps a structure-only record (`apple/Persona/App/ChatTrace.swift`) in both DEBUG and TestFlight:

- Records:
  - each item batch: count, line-number range, counts by type, span, store handling (`gap`), `covered`, `resets`;
  - when streaming text starts and clears, and when the reply lands in items (`landed`);
  - the last 5 chat rows after each rebuild, by type and line number: `a118` is an answer, `u101` a message, `work(5,live)` a work row, `wakes(tail=2,answer=112,live)` a background-task strip, including drawer row count and answer line;
  - concurrent `live` (streaming-text length), `streaming`, `running`, `frozen`, `withheld` (waiting item count while frozen);
  - following-state transitions (`hold back` / `let go`, with mode, near, touching), plus chat opens and closes.
- No message text, names, links, or item ids.
- Keeps the last 4 chats, latest 240 lines each, in `Caches/state/chat-trace.json`. Survives the App being killed.
- Copy it out: long-press a chat in the shell drawer or regular-width sidebar › **Copy Diagnostics**.

Reading it:
- Last row contains `a<reply line number>`: the reply was drawn.
- `wakes(tail>0,answer=<earlier line number>)`: the reply moved into the drawer.
- `frozen=1 withheld>0`: the chat is frozen and the reply is waiting.
- `items … resets=` changed, or `reload` appeared: data was rebuilt.

## Back to latest: chat-page "follow latest" (reviewed 2026-11-04; #1046 implemented the plan below)

Agent chats and Persona chats share one `ChatView`: an inverted list (latest at offset 0), with the bottom-right ↓ ("Jump to Latest") and "follow latest" handled here.
Below are the old behavior, measured problems, and a single-source-of-truth plan compared with Vercel `useScrollToBottom`, followed by the implementation (#1046). Reproductions use `ScrollToBottomTests` (each scenario records a
`probe.chat-rows` timeline: button, following, withholding, sentinel, distance from latest, finger, pin).

### Multiple truths before the change

| State | Meaning | Written by |
| --- | --- | --- |
| `atBottom` | The 1 pt sentinel at the latest end is on screen (`onScrollVisibilityChange`, threshold 0.01) | Sentinel; `landed()` forces false when more than one screen from latest (#597) |
| `scratch.atNewest` / `newestGap` | Scroll geometry: whether offset is at latest, gap in pt | `onScrollGeometryChange` |
| `followsEnd` | Intent: whether new content should move the list with it | About ten sites, table below |
| `frozen` | Withholding: while reading above, items after the snapshot, streaming text, and pending bubbles stay out of the list | Snapshot when `followsEnd` turns false, clear when true; cleared by `store.resets` |
| `settled` | No ↓ for the first 0.4 s after entering a chat (avoid flashing during landing) | `settleAtEnd`; reset on page / session changes |
| `userScrolled` / `scratch.userDragging` | User has dragged / finger down or inertia active | Scroll phase |
| `scratch.landingInFlight`, `entry` | Landing at "last read position" or "start of latest answer" | Entering the chat |
| `pin.holding` | An expanded / collapsed heading is being held under the finger (1.5 s) | `ChatPin` |

↓ checks only `!atBottom && settled`, not `followsEnd`; withholding checks `followsEnd`. Two different states decide the two behaviors.
(The Agents list's `Hold` doesn't govern chat pages: their "user is interacting" state is the table above.)

### Old `followsEnd` transitions

| From | To | Trigger |
| --- | --- | --- |
| Following | Not following | Finger drag (`tracking` / `interacting`, even 1 pt); landing at saved position (`landEntry`); expanding or collapsing a work row (`changeWork`, #862) |
| Not following | Following | Sentinel appears with no held pin; new content arrives while `atBottom` and geometry are both at latest, no drag, no landing, no held pin (`contentArrived`); scrolling stops at bottom (`userScrolled`); landing ends at bottom (`landed`); tap ↓ (`jumpToEnd`); send a message (`store.pending` grows, or own message enters transcript without scrolling since send); view sub-Agent; switch from files / Git back to chat; switch session; collapsed content fits one screen (#862) |
| New content while following | — | `contentArrived` → `scrollToEnd()` (no movement if already latest; `ChatStreamFollow` every 40 characters) |
| Keyboard opens / closes | — | List height and offset change: `scrolled(by:)` ignores height changes rather than counting them as user scroll; the inverted list stays at latest itself |

### Measurements (both layouts, mock; `ScrollToBottomTests`)

| # | Scenario | Result |
| --- | --- | --- |
| 1 | Drag upward 40 pt during streaming, then release | **Wrong**: immediately stops following, `frozen`, ↓ appears, 34 pt from latest. All later streaming text and steps are held until dragged exactly to bottom or ↓ tapped. Same in both layouts |
| 2 | Read several screens up, then flick downward | Stops 1332 pt from latest, ↓ visible, not following: correct. "Flick nearly to bottom" not tested: mock history too long to reach in one flick |
| 3 | At latest, expand a running work row, then collapse | Expand: heading stays under the finger, steps push latest 1820 pt away, ↓ appears, stops following, **withholds**—later steps in this turn no longer enter the list; the open work row freezes at expansion time (**wrong**: this is the turn being read). Collapse: returns to latest and follows, correct |
| 4 | Keyboard opens / closes | New shell: stays latest, following, no ↓ or flash: correct. Historical tab navigation + Liquid Glass: composer missing from accessibility tree (#1006), not tested |
| 5 | Send a message while reading above | **Not tested**: after scrolling up, tests couldn't find the Agent chat composer in either layout; cause not investigated |
| 6 | Push while reading above, then tap ↓ | While reading, ↓ stays visible, not following, items arrive in the background but are held: correct. 0.11 s after tapping ↓, at latest, following, ↓ gone: correct |
| 7 | Leave and reopen chat | **Not tested**: after returning to the list, tests couldn't find a tappable Agent row (selector issue), skipped |
| 8 | Background for 6 s, then return | Returns latest, following, no ↓ or flash: correct |
| 9 | Less than one screen (new chat), drag once | ↓ never appears, following uninterrupted: correct (can't scroll, phase never enters `tracking`) |
| 10 | Several pending cards (#939) | At rest, no ↓, following: correct. That chat has only two entries and can't scroll up; "↓ covered by cards" not tested |

### Problems

1. **No "near bottom" tolerance**: once the 1 pt sentinel leaves the screen, it's not at bottom; a finger touch (`tracking`) immediately stops following and withholds. Drag up a few dozen pt to peek and the streaming answer stops,
   requiring an exact drag to bottom or a ↓ tap. Vercel uses 100 pt tolerance: within it counts as bottom and growing content still follows. (Measurement #1)
2. **↓ and "following" are two truths**: ↓ reads the sentinel, withholding reads `followsEnd`. Usually aligned, but only through patch after patch (`landed` #597, `contentArrived` #754,
   ignoring the sentinel while pin holds). The lazy list can recycle the sentinel, and visibility arrives a frame later than offsets changed directly by pin.
3. **Expanding a running work row freezes it**: expansion means "user takes over the list"; not following means withholding, so the turn being read stops gaining steps. The person expanding it wants those new steps. (Measurement #3)
   Possible fixes: withhold only items inserted beyond latest that would push current reading away; or expansion stops following without withholding (pin already keeps the heading still).
4. **"User scrolling" is decided at finger touch**: `tracking` immediately means not following, unlike Vercel's "decide after scrolling ends"; a light touch without real scrolling counts as takeover. Decide after scrolling stops,
   based on whether the final position is within tolerance.
5. **Several "resume following" conditions decide independently** (about ten sites in the right column above): adding one easily misses a guard (`pin.holding`, `landingInFlight`, `userDragging`).

### Plan: one truth + one function

Follow Vercel's approach, but use our existing gesture information to distinguish user from programmatic scrolling (Vercel can't distinguish them):

- **Single truth `follow: Follow`** (`@State`, mirrored in `scratch` for synchronous callback reads so closures don't see stale values, like Vercel's state + ref):
  `.following` (move with content), `.reading(since: Date)` (user reading elsewhere, withhold), `.landing` (landing), `.holding` (pin holding a heading).
- **Geometry decides bottom proximity**: `nearNewest = newestGap ≤ 100 pt` (same tolerance as Vercel). The sentinel is only a fallback before geometry is ready, no longer independently driving ↓.
- **Transitions at only three entry points**:
  - user scrolling ends (phase returns from `tracking`/`decelerating` to `idle`, our "finger" signal absent in Vercel): `nearNewest` → `.following`, otherwise → `.reading`;
    no transition while the finger is down (wait for release), regardless of content changes;
  - programmatic actions: tap ↓, send, switch session / return to chat → `.following`; landing → `.landing` → decide by `nearNewest` when finished; expand / collapse work row → `.holding` → decide by `nearNewest` when finished;
  - content grows (new items, streaming text): only `.following` calls `scrollToEnd`; `.holding` leaves it to pin.
- **Derived**: ↓ = `follow != .following && !nearNewest && settled`; withhold = `follow == .reading` (`.holding` doesn't withhold: problem 3).
- Put it in a small pure type (`ChatFollow`); all transitions are its methods, unit-tested for every case above. `ChatView` only reports gestures, geometry, and programmatic actions.

Acceptance: change `ScrollToBottomTests` from recording to assertions (expected outcomes in the table above), both layouts × both styles; existing `ChatFollowTests`, `RunningChatTests`,
`ScreenTruthTests` remain green.

### Implementation (#1046)

`ChatFollow` (`apple/Packages/PersonaKit/Sources/PersonaKit/ChatFollow.swift`, pure value type, #2095) is the single truth. `ChatView` changes it only through `setFollow` (no write-back if unchanged: geometry reports every frame,
and each write recomputes the whole page). `atBottom`, `followsEnd`, `scratch.landingInFlight`, and the sentinel visibility callback are removed, along with the #597 `landed` and #754 `contentArrived` patches.

| Input | Source | Behavior |
| --- | --- | --- |
| `scrolled(gap:)` | `onScrollGeometryChange` | Updates only `near` (≤ 100 pt from latest); records `draggedAway` if the finger is down and leaves tolerance |
| `touched()` | Scroll phase enters `tracking` / `interacting` | Records finger on list; no transitions afterward |
| `released()` | Scroll phase returns to `idle` (previously not `animating`) | Counts only after `touched`: `near` → `following`, otherwise `reading` |
| `follow()` | Tap ↓, send, own message enters transcript, switch session / sub-Agent, return to chat, collapsed content fits a screen (#686) | `following` |
| `read()` | Jump to quoted message, tool call, or debug scroll upward | `reading` |
| `land()` / `landed()` | Land at saved position or start of latest answer | `landing`, decide by `near` at finish; if user takes over mid-way, release position wins |
| `hold()` / `endHold()` | Expand / collapse work row (`changeWork`), ends after `ChatPin.holdTime` (1.5 s) | `holding`, then **restore previous state** |
| `hold(anchored: true)` | Expand work row while following | `holding`, uncapped: heading stays put (at latest, moves below top bar, #1173), running turn keeps growing; user release decides by position, ↓ / send resumes following, collapse resumes following and scrolls to latest |

Derived: following `follows = mode == .following && !touching`; withholding `holdsBack = mode == .reading || touching || draggedAway`; ↓ `arrow = !near && (mode != .following || draggedAway)`,
plus `settled` after entering the chat. `frozen` snapshots are taken and cleared with `holdsBack`.

Differences from the plan above:
- **After `holding`, restore the previous state rather than decide by `near`**: expanding a running work row at latest pushes latest several screens away; deciding by `near` would become `reading` and withhold again (problem 3).
- **Expanding a work row while following stays `holding` (anchored), without resuming following** (user decision 2026-11-04): #1173 moves an expanded heading at latest below the top bar, with steps below it;
  resuming following would scroll the heading offscreen after 1.5 s, while withholding would freeze the turn. So neither follow nor withhold: `ChatPin.hold(keep:)` keeps the heading pinned, new steps grow below it, and ↓ appears when they extend offscreen.
  - In the inverted list, growth newer than the heading pushes it upward; pinning adjusts offset every time. When many cached steps lay out together, the lazy list later measures them taller than estimated, pushes the heading outside the list, and it stops reporting position,
    so offset can no longer be calculated (#1173's own test also failed on master with Historical tab navigation + BeautifulUI). Recovery: if the heading stops reporting, move by content growth (`ChatPin.watch`);
    if still lost, scroll to its id (`ChatView.keepHeader`, making the lazy list lay that row out accurately), then pin from there.
  - Expanding a step below a persistently pinned heading initially replaced the heading's pin with that step's short 1.5 s pin; afterward neither was pinned. Now rows newer than the heading don't get separate pins (the pinned heading keeps the interval stable);
    older rows pin normally, then resume the heading pin; collapsing or expanding another row converts the persistent pin to 1.5 s from now (`ChatPin.unkeep`).
    (`RunningChatTests.testARunningTurnNeitherBlanksNorJumps`'s reported "56 pt up/down jump" was a probe reading the wrong position; the screen didn't move pixel by pixel, see `docs/lessons/ios.md`.)
- **Withhold while the finger is down (including inertia after release), then decide by position** (`holdsBack` includes `touching`): in an inverted list, growth at latest pushes content under the finger upward,
  causing 23 pt and 39 pt movement during a light drag while streaming. Release within tolerance resumes following (problem 1), bringing held content in together. Leaving tolerance separately records `draggedAway` to control ↓.

### Scrolling during streaming (2026-11-04, user: "scrolling becomes unstable while output is streaming")

Measurement: decode simulator recordings frame by frame, calculate content displacement relative to the previous frame, last-row distance from the composer, and "large change then reversal within a frame" jumps. In the App, `-scrollTrace YES`
records UIKit / AppKit offset, content height, finger / inertia, and follow state each frame (`ScrollTrace`). Cases: `StreamingScrollTests` (mock `/mock/say?preset=markdown`:
code blocks, tables, lists, delta every 80 ms like the hub; `&late=600` clears first and sends the committed message 600 ms later). A locked Mac can't record the screen and has no real trackpad:
`MacScrollLab` sends synthetic two-finger scroll and inertia to the list, rendering the window every 0.1 s (`docs/apple/mac.md`). Tools live in `scripts/diag/stream-scroll/`.

| Symptom (master) | Cause | Fix |
| --- | --- | --- |
| On the streaming-completion frame, the whole reply disappears, screen rests on the previous message, returns next frame | Streaming text clears as the committed message arrives, but rows rebuild in the background and arrive next turn | Clear streaming text in the same turn the rows arrive (`ChatStore.endLive`) |
| Real hub sends "clear" before the committed message: reply disappears until it arrives (610 ms in mock) | Hub clears at turn end and next request start; the transcript row may be read later | If the message hasn't arrived when clearing, retain streaming text until it arrives, at most 5 s (`ChatStore.clearLive`) |
| In a running turn, each streamed sentence grows upward two lines, then drops three when complete | Only the latest sentence is exposed; previous sentence remains while the new one streams and moves into the work row only at completion | Move the previous sentence into the work row as soon as a new one starts streaming (`ChatLines.lines(streaming:)`), letting the new one grow in place |
| Drag upward during streaming: on finger-down the whole streaming reply vanishes, removing 368 pt of content; on Mac, at 80 pt it jumps to latest, inertia reaches 380 then is pulled back | Streaming text and pending messages aren't drawn while new content is withheld | Preserve them as they looked then (`Frozen.text` / `pending`), drawn by the original `ChatLiveText` (`held`): a separate rendering branch gives both rows id `live`, so the lazy list retains the original growing row and each delta shifts content under the finger by 9–30 pt |
| With a finger down, streaming pushes content under it upward (23, 39 pt in one light drag) | Withholding starts only beyond 100 pt | Withhold on finger-down (`ChatFollow.holdsBack` includes `touching`), resume on release |
| Mac: a light swipe is pulled back to latest by streaming while inertia continues | SwiftUI reports idle once two fingers leave the trackpad, ignoring inertia | Decide from scroll events (`MacWheelWatch`): finger, inertia, and wheel movement within half a second all count as user scrolling |

While reading history above, streaming already moves nothing by even a pixel (withheld); measured before and after in all scenarios. A newly opened code block first lays out an empty gray box, then grows as content arrives, making its final distance from the composer jump; not changed.

Unit tests `ChatFollowStateTests` cover every entry point in the table; FINDINGs in `ScrollToBottomTests` became assertions (at each moment: no ↓ at latest, no withholding while following), with several timestamped screenshots per scenario.
