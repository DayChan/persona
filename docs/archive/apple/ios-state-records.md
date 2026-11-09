# App state and redraws (iOS): records

Moved unchanged from the [iOS state guide](../../apple/ios-state.md), which now describes only the current design.
These are the data-flow observations and root causes the staged redraw work started from, its plan with each step's acceptance numbers,
the cost of one push measured after each step, and the review of chat-page "follow latest" before #1046 (reviewed 2026-11-04).
Step names match the guide's "Step 1" to "Step 5" sections.

## Data flow before the staged work

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

## Cost of one push, after each step

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

After step 2 (same test; wait until the page stops redrawing before measuring, see the guide's "Step 2"; two runs per layout):

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
| 1 (#338, merged) | Publish Agents list structure separately from rows (the guide's "Step 1"): each row follows its own Agent (`AgentsFeed.Row`), and the list reorders only on structural changes | A push changing one field in one session: recomputed views ≤ leaf views reading that session + 1; zero list body calls if structure is unchanged |
| 2 (#620, merged) | Split volatile fields: put `rev`, `updated_at`, tokens, ports in separate small objects, read only by the leaf views displaying them (relative time, last line), following `WaitingBadge` | Pushes churning `rev` / `updated_at`: zero views recomputed except the `Text` displaying time |
| 3 (#639, merged) | Move derived data into models: sorts, groups, filter counts, badges computed in the model and published only when results change (`AgentListOrder` moved into `AgentsFeed`) | List body runs only when order / groups / counts change; badges run only when the number changes |
| 4 (#880, merged) | One model-layer policy for "user is interacting": during dragging, inertia, open menus, or keyboard input, merge and defer noncritical updates; critical updates go immediately; maximum delay bounded. Extract `AgentsFeed` holding into reusable `Hold`, with pages registering their state | Existing `LiveUpdateTests`, `ScrollUnderPushTests`, `ScreenTruthTests` all green; no page-specific holding code remains (source check) |
| 5 (#913, merged) | Guardrails: performance test "a push changing one field of one session recomputes at most N views", failing above the limit; source check "non-list views don't read `allSessions`" | Both checks in `PersonaTests` / UI tests |
| 6 (#270, merged, testflight lane) | Hub pushes only semantic changes (`rev` / `updated_at` churn goes via `revs` instead), or sends deltas. Implemented as `sessions_delta`: the hub remembers sessions sent per device and sends only changes and deletions | Session push traffic: 2605 KB/min → 79 KB/min |

## Back to latest before #1046 (reviewed 2026-11-04)

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
