# User-input navigation

## Status, scope and change control

**Interaction contract for user-input navigation (#2061).** This is not a release or acceptance report. It specifies the
shared behavior for navigating the user's inputs in a conversation, with separate touch and desktop-pointer controls.
Changes to this contract or its super-rule reference require repository-owner review before merge. Implementation and
runtime-verification claims are scoped explicitly below.

Read [the super rules](../SUPER_RULES.md) first. **Before changing any behavior or visual effect described here, update
this specification and obtain review from the repository owner.** That includes reveal/fade behavior, viewport ownership,
gesture boundaries, previews, highlighting, paging and fallback behavior. Follow the normal issue and PR workflow and
any additional approval gate; an implementation task or another Agent cannot supply the owner's review.

Implementation evidence inspected on 2026-11-09:

| Area | Evidence and remaining boundary |
| --- | --- |
| iOS and server, #2035 | Read-only inspection of an actively edited, unmerged implementation. Independent user-input indexing, bounded landing windows and request fencing are present in source. This does not establish build, simulator, device or server acceptance. |
| iOS appearance | The inspected unmerged source now includes passive scroll reveal, a first-visible-row input-interval highlight, token-driven idle fade and a gesture preview containing the input plus an available short answer. These are source observations, not simulator/device acceptance. |
| Desktop web, #2060 | [PR #2080](https://github.com/nwverse/persona/pull/2080) is merged and deployed. `NavRail.tsx`, `nav-rail.ts` and the `ChatView.tsx` loaded-message adapter provide the fine-pointer rail baseline. [#2168](https://github.com/nwverse/persona/issues/2168) tracks the bell-shaped mark profile and rail/card hover contract below; these supersede the original card linger without changing the rail's idle fade. The independent index remains gated on #2035 being merged. |
| Other clients and layouts | No implementation claim for native Mac, Vision Pro, Android or watch. The inspected iOS integration is confined to Persona's new conversation layout on iPhone/iPad; existing layouts remain unchanged. |

The current [iOS layout guide](../apple/ios.md#new-conversation-layout-1666) describes the merged baseline, including its
older tick-range limitation. #2035's unmerged column keeps every fetched mark selectable instead. Reconcile the module
guide after implementation and owner review; unmerged source does not silently redefine the shipped baseline.

## Vocabulary and invariants

- **Input / mark:** one genuine human input in the displayed session, not every transcript item or model-facing user role.
  A text input and its attached images share one mark; a photo-only human input still has a mark.
- **Index:** ordered lightweight marks with stable input identities, transcript positions and bounded preview text.
  It is independent of the loaded conversation bodies and of the live synchronization cursor.
- **Rail:** the right-edge column of marks. Oldest inputs are above newer inputs. Position expresses order in the fetched
  index, not elapsed time, body length or a percentage of the full transcript.
- **Viewport owner:** the last genuine input starting above the reading anchor in the usable conversation viewport.
  The web anchor is the scroller's top plus the shared `activeOffset` token (40 CSS pixels), excluding the header and
  composer. Before the first input, the first available input owns the view. An exchange extends to the next genuine
  input, so a long answer keeps its input highlighted across multiple screens. Missing anchors remain unknown. The
  inspected iOS source maps the first source-ordered visible row (visibility threshold 0.01) to its genuine input's
  sequence and binary-searches the prepared marks; it does not infer ownership from the dragged preview.
- **Preview selection:** the mark being hovered, dragged over or selected with the keyboard. It may differ from the
  viewport owner; previewing must not scroll the conversation or change reading ownership.
- **Reading window:** a bounded, contiguous body window around a selected old input, isolated from the live tail.

A preview shows the genuine input and a short available, extractive answer excerpt; it does not fetch bodies or ask a
model to summarize while the pointer/finger moves. The web implementation and the inspected unmerged iOS source both
carry an optional short answer. The desktop card prefers the answer, then visible plain follow-up text in the same
exchange when no answer is available; never borrow a later injected/cross-session reply, tool payload or private reasoning.

A navigation action is read-only. It must not send a message, resume a runtime, edit the composer, open another session,
mark an unloaded range as read or claim that skipped bodies have been synchronized.

## Appearance, disappearance and highlighting

| State / trigger | Required effect |
| --- | --- |
| Idle conversation | Keep the rail unobtrusive; no open preview and no conversation-layout shift. |
| Normal conversation scrolling or touch in the central reading area | Reveal the rail and its viewport-owner highlight only. Keep normal scrolling, tapping and selection behavior. No preview selection or jump is caused by this touch. |
| Pointer enters the rail, or touch begins a qualifying edge drag | Reveal/retain the rail; show the selected mark's preview. Keep viewport-owner and preview-selection states distinguishable. |
| Pointer remains over the rail or preview card, edge drag is active, keyboard focus is inside, or a landing is pending | Do not fade the interactive control out from under the user. A loading state must not look like a completed jump. |
| Pointer leaves both the rail and preview card after hover | Dismiss the hover preview immediately in the leave handler. Do not wait for an idle timer or opacity transition. An active captured drag or keyboard selection retains its own interaction ownership. |
| Scroll activity ends | Briefly retain the rail, then fade it after the shared idle delay without moving the conversation. This delay applies to the rail, not hover-preview dismissal. Cancellation closes the preview immediately. |
| Session changes, view becomes hidden, or interaction is cancelled | Close previews and retire obsolete selections, loading completions and fade timers. Cancellation does not jump. |

Use design-system tokens for rail geometry, hit areas, highlight, preview surface, idle delay and motion. Reuse existing
motion tokens where suitable; add missing tokens before UI code. The inspected web geometry/timing token is
`NAV_RAIL_METRICS`: a 2,000 ms quiet delay for the rail followed by a 200 ms opacity transition (disabled under Reduce Motion).
Hover, focus and an active drag retain rail visibility. A hover preview closes immediately when the pointer leaves both
the rail and card hit zones; no timer delays that dismissal. The inspected iOS source uses `Theme.Motion.edgeScrubLinger` and `Theme.Motion.state` for passive
fade; its scroll phase/visibility observers reveal and update marks without accepting a central scrub gesture.
Simulator/device and accessibility acceptance remain separate from these source observations.

Highlight the viewport owner's mark, not merely the nearest visible user bubble: the owning input can be above the loaded
viewport while its answer is visible. If ownership cannot be established from the available index/window, show it as
unknown rather than highlighting a guessed input from another interval. Highlighting must survive folded work, images,
layout changes and explicit older/newer window paging. Use more than color alone to distinguish ownership and selection.
A landing briefly highlights the actual destination input; it must not flash an unrelated nearest row.

## Touch interaction

### Gesture ownership

| Gesture origin and direction | Owner / result |
| --- | --- |
| Central conversation area, vertical movement | The conversation scroll view. The rail may appear; this is not scrubbing and never selects a destination. |
| Central text/link/control, tap or long press | The existing text, link or control interaction. Rail reveal must not swallow that action. |
| Right-edge band, predominantly vertical movement beyond direction slop | The navigator. Movement previews inputs; release commits one destination. |
| Right-edge band, predominantly horizontal movement | The shell pager or drawer, not the navigator. |
| Left-edge system back gesture | The system navigation gesture; the rail does not compete. |
| Header, composer, other panels or outside the conversation | Their existing controls; the rail must not install a whole-screen drag target. |

The inspected iOS recognizer tests the **starting location**, accepts a single touch in the token-sized right band below
the header, and fails immediately outside it. It fails when horizontal displacement wins before vertical activation.
The list pan waits for the edge recognizer to fail, so an accepted vertical edge drag does not scroll the list as well.
This preserves the pager/drawer and the opposite-edge back gesture. Do not broaden that capture area merely because the
rail has been revealed by a central touch. Browser touch controls must follow the same ownership contract rather than
turning every touch into a desktop hover.

### Preview and commit

1. Open from the available index; do not wait for conversation bodies. If the independent index is unavailable, use the
   already loaded, filtered inputs and make the limited coverage clear.
2. During the drag, show a compact preview of the selected **user input** beside the rail. The inspected iOS overlay uses
   at most two input lines and two optional answer lines, with a localized photo label when there is no input text.
3. An opening haptic and a selection haptic may acknowledge activation and a changed input; do not tick on every movement
   callback over the same input. Haptics are feedback, not the only way to discover or operate the navigator.
4. Movement changes the preview only. Older-index prefetch is allowed under the rule below; body loading is not.
5. Release commits the selected identity. An interrupted/cancelled gesture closes without a jump. A later selection or a
   new gesture supersedes an earlier pending landing.
6. Resolve an image/text group to the rendered line that actually contains the input identity. Center that line in the
   usable viewport, stop following latest content and briefly highlight the destination.

The current iOS overlay is non-hit-testing; the edge recognizer owns touch interaction. Merely making the rail visible
must not turn its preview into an invisible overlay that blocks conversation gestures.

## Desktop pointer and keyboard interaction

**Source inspected from #2060; independent-index integration remains gated on #2035.** Desktop refers to a fine pointer,
not merely a wide viewport. The current web implementation deliberately renders no navigator on touch-only browsers;
it does not implement the iOS edge recognizer or claim touch conformance.

- Scrolling reveals the rail with the viewport-owner highlight. Hovering over a mark reveals a compact preview containing
  the user input **and a short available answer excerpt**. This excerpt is extractive transcript text, not a new model
  summary. Absence of an answer must not substitute another input's answer.
- Pointer movement/hover and dragging preview the candidate without moving the conversation or fetching body windows.
  Clicking a mark commits it. Releasing an active rail drag commits the final selected identity once; a synthetic click
  after that release must not commit it again.
- Keep the preview within the usable viewport, near the selected mark, readable over the conversation. Moving into a
  preview that is part of the same control must not cause a flickering hover gap. Do not intercept links, text selection,
  the native scrollbar, composer controls or the page's normal wheel/trackpad scrolling.
- Once a drag is accepted, retain ownership if the pointer briefly leaves the rail. Pointer cancellation, a lost active
  interaction, session replacement or Escape cancels without landing. A blur/leave that is not a release must not commit.
- Keyboard focus reveals and retains the control. Provide an accessible way to move to the previous/next indexed input,
  preview it, commit with Enter/Space and cancel with Escape. Keep these commands scoped to the navigator so composer
  editing shortcuts remain unchanged. Returning focus after landing must not trap the user in the preview.

Answer snippets are optional on the wire and bounded. Render plain text, not executable markup or a full tool trace.
The desktop marks form a bell-shaped focus profile: lengths taper smoothly from the current/preview input toward
short, faint tails, rather than a column of uniformly long lines. Current position retains an independently longer,
thicker foreground mark; hovering also changes the selected mark. `NAV_RAIL_METRICS` supplies the width, thickness,
spread and opacity tokens, including short-list handling. Equal vertical pitch preserves input order, not body length.

The card sits to the left of the right-edge rail, centered vertically on its input mark and clamped into the usable
viewport. Use the navigation-preview radius token (16 CSS pixels), a fine role-colored border, a broad soft shadow,
20-pixel padding and a paragraph gap. The light surface is white with dark input text and muted gray body text; dark
mode uses its corresponding surface, border, foreground, body and shadow roles. Existing app typography is retained.

The preview uses plain text capped at 120 characters for the input and 200 for the response excerpt. The input is bold
and single-line truncated; the muted answer/follow-up body renders at most two lines. The card's hover hit box reaches
the rail through their small visual gap, so the pointer can move into the card without a grace-period timer. Leaving
both hit zones dismisses the card immediately. Clicking/selecting card text must not initiate scrubbing or jump to an
input. The current web focus stays on the `nav` landmark (`tabIndex=0`),
with Arrow keys, Home/End, Enter/Space and
local Escape. Per-mark labels and `aria-current` distinguish destination and viewport owner; `aria-describedby` ties
selection to the visible preview, while keyboard-only polite announcements avoid mouse-hover chatter. The invisible
edge remains Tab-reachable. Pointer capture freezes stable target identities and suppresses the release's synthetic
click. Body scrolling binary-searches cached offsets; content mutation/resize invalidates them. A bounded moving mark
window and rail-wheel paging retain access to every loaded input without expanding transcript DOM.

## Index, filtering and incremental prefetch

### Independent index contract

The inspected server advertises `session_navigation_v1` and proxies
`GET /api/v1/sessions/{mid}/{sid}/navigation` to `session.navigation`. It accepts `limit`, optional exclusive `before_seq`
and optional `history`; the default page requests the **most recent 40 genuine inputs**, returned oldest first.
The parser clamps the page limit to 20–100 and applies a parsing budget. Therefore a sparse transcript can return fewer
than 40 marks, or an empty page, while `has_more` and an advancing `next_before_seq` still indicate older index coverage.

Each message carries `id`, `seq`, bounded `text`, optional `ts`, `has_image` and `answer`. The page carries `messages`,
`has_more`, `next_before_seq` and a transcript `history` identity. The inspected previews normalize whitespace and cap
text/answer excerpts at 160 Unicode code points; attached-image paths and injected context are not preview content.
The answer extraction stops at the next user input or wake boundary and must not attribute a different exchange's reply.

The iOS initial warm-up tries to collect 40 marks by following explicit cursors, with **at most 12 index requests and a
10-second warm-up budget**. These are observed unmerged implementation bounds, not a universal promise that every first
page will contain 40 marks. The client keeps partial results and the remaining cursor when that budget ends. It does not
fetch body pages as a substitute. An unsupported/unknown hub or older runtime falls back to loaded-input navigation;
an absent remote cursor in this fallback does not mean the entire transcript index is complete.

### Eligible inputs and deduplication

Include real human text inputs, photo-only inputs and human queued/absorbed-mid-turn messages once the parser exposes
them as user inputs. Do not exclude a genuine human message merely because it was queued while work was running.
Exclude automatic injections/supervision, wake/follow-up ticks, messages with cross-session provenance (`from`), `/loop`
inputs/ticks, tool results and tool-owned images. Strip embedded model context and attachment transport metadata from
previews without discarding the genuine human words alongside them.

Deduplicate by stable input identity and transcript position **within the session and history generation**, never by text:
two human inputs with identical words at different positions remain two marks. Text plus same-input images contributes
one mark, preferably using the text item's identity; a photo-only group uses its containing input/line. Merge overlapping
pages without duplicate ticks and sort by transcript sequence with a deterministic identity tie-breaker. Do not use
wall-clock timestamps as the authoritative order.

The independent server index is the intended authority. The inspected iOS loaded-line fallback separately checks injected
classification, cross-session provenance and a literal leading `/loop` prefix, and groups text/images by sequence.
Its normalization and wake/loop edge-case parity with the server still require tests; do not claim complete parity from
these separate filters alone.

### Older-direction prefetch

While previewing, prefetch **one older index page** when the picked mark is in the earlier half of the fetched marks and
the interaction is moving toward older inputs, or has just entered that half. In zero-based oldest-first order the
inspected iOS predicate is `picked < count / 2` plus `movingOlder || enteredOlderHalf`. It does not fetch on a newer-directed
movement simply because the finger remains in that half.

- Keep only one index request in flight and retain the cursor on failure, so deliberate later movement can retry.
- A queued movement callback may use only the cursor it observed, not consume a new cursor from a page that just arrived.
- Page arrival is not a fresh user movement. It must not recursively fetch the full index. The budgeted initial warm-up
  above is the separate, bounded exception.
- Preserve the selected input identity, preview and pointer/finger position when older marks are prepended or new marks
  arrive. Do not let a new page change the candidate under a stationary finger.
- Dense rails may sample **drawn** ticks for legibility, but all fetched inputs must remain addressable. Do not silently
  drop older inputs from hit testing. This is an index, not just the newest marks that fit onscreen.
- Stop automatic paging when a cursor fails to advance. Treat an empty page with an advancing cursor as sparse progress,
  not as end-of-history or permission to spin without a budget.

## Landing, unloaded bodies and isolated history

1. **Loaded target:** resolve the stable identity in the currently displayed source and jump locally. Do not request
   another body window just to redraw an already available input.
2. **Indexed but unloaded target:** on commit only, request
   `GET /api/v1/sessions/{mid}/{sid}/items?around_seq=<seq>&limit=80&lazy_images=1&fold_work=1&history=<history>`.
   Show bounded loading feedback while retaining the current reading position. Do not first download all pages between
   latest and the target. Hovering or dragging alone must not issue this request.
3. **Validate before landing:** the response must belong to the selected session/history and still-current selection;
   the target identity or its same-sequence human input must exist. Wait for its rendered line to be available before
   scrolling. A missing target is an unavailable destination, not a reason to jump to a neighboring unrelated item.
4. **Separate reading source:** keep the contiguous returned window in a separate store. Do not merge a distant window
   across the gap into the live store, advance its covered/since sequence, replace its tail cache or disturb subscriptions
   and unread accounting. The inspected iOS store is created with `cachesTail: false`.
5. **Explicit window paging:** use returned `from_seq`, `to_seq`, `has_more`, `more_after` and `history` to load older/newer
   pages of that reading window. These flags describe its boundaries, not live catch-up completion. Check advancing
   cursors and the same window identity. Do not loop forward to latest in the background. The inspected iOS older page
   requests 80 items and forward page uses the existing bounded 500-item path; folded expansion remains history-scoped.
6. **Return to latest:** switch back to the untouched live source and retire pending window, landing and unfold responses.
   This is an explicit reading action, not an implicit side effect of a preview or a new live message.

`around_seq` returns a bounded contiguous span containing the target, with full same-sequence siblings retained; a page
can therefore exceed its nominal item count at a sibling boundary. The span covers only that window, never its omitted
prefix/suffix. Sequence numbers alone are not sufficient to cross a transcript rewrite: the `history` token fences it.
The inspected server reports changed history as a conflict and a missing target as unavailable. On changed history,
retire obsolete marks/windows and refresh the index; do not reuse an old sequence as though it still named the same input.

Fence every async completion by hub/credential generation where applicable, displayed session, transcript history,
visibility owner, reading-window identity and selection revision. The inspected iOS model fences session/live-store
resets, history, activation and competing selections; full credential-replacement acceptance still needs verification.
A hidden retained chat must not finish a late jump. A failure keeps the last valid reading position and offers a localized
retry/error state rather than flashing success. Do not equate filtered-empty, budget-limited, offline and unsupported
states, or repeatedly retry an unsupported endpoint on each movement.

## Performance and accessibility

- Keep high-frequency pointer/gesture state in the overlay, not the conversation's rendering graph. Derive the index and
  merge pages on data changes, not by reparsing or scanning all bodies on every pointer frame.
- Use bounded index payloads and lazy images; decode body/index responses off the UI thread where the platform supports
  it. No model call, full transcript download or image-byte prefetch is needed to show a mark preview.
- Cache geometry and use a monotonic hit-test mapping. The inspected iOS column uses binary search; dense drawing is
  sampled without truncating the selectable index. Measure long-history frame cost separately from network latency.
- Idle reveal/fade work must stop with view/scene inactivity. Give waits and retry work explicit budgets and a clear
  outcome; do not create an unbounded task per movement or a self-perpetuating cursor loop.
- Respect Reduce Motion; remove nonessential movement while retaining immediate state feedback. Do not rely solely on
  animation, color, hover, haptics or a narrow physical touch band for access.
- Expose a localized navigator label, the input preview, selected/current state and meaningful ordinal/coverage information.
  Do not announce a total history count until the index is complete. Provide screen-reader previous/next and commit
  actions, plus the keyboard path above; accessibility focus prevents idle fade.
- Use design-system hit-area and text-size tokens, support Dynamic Type/zoom, keep previews readable in dark/light modes
  and avoid covering the composer. All user-facing labels and errors ship through en, zh-Hans and ja localization.

These are acceptance requirements. The inspected iOS overlay identifiers and haptics alone do not establish a complete
screen-reader alternative; desktop keyboard, per-mark labels, current state and selected-description behavior are
implemented in #2060. Browser assertions do not substitute for testing with a physical screen reader.

## Source map and verification boundaries

Paths below identify inspected code, **not merged availability**. New #2035 files are deliberately plain-text references
until that implementation lands; do not add broken links or waive path checks to imply merged availability.

| Responsibility | Source / status |
| --- | --- |
| iOS edge recognizer, passive viewport bookkeeping, mark filtering, stable dense column and input/answer overlay | `apple/Persona/Views/Chat/ChatEdgeScrub.swift` — inspected unmerged edits; `EdgeScrubViewport` maps visible rows to prepared input intervals. |
| iOS activation, release landing, rendered-group identity, highlight and reading-source switch | `apple/Persona/Views/Chat/ChatView.swift` — inspected unmerged edits. |
| iOS index warm-up, half-prefetch, history/window isolation and fencing | apple/Persona/Views/Chat/SessionNavigationModel.swift — new unmerged file, absent from the documentation branch baseline. |
| iOS wire decoder and index/window requests | `apple/Shared/APIClient.swift` — inspected unmerged edits; decodes the optional answer retained by `EdgeMark` and rendered in the gesture preview. |
| Live coverage and non-tail-caching reading store | `apple/Shared/ChatStore.swift`; edge geometry tokens in `apple/Shared/Design/ChatLayoutTokens.swift` — inspected unmerged edits. |
| Hub forwarding, around/history query and node dispatch | `server/internal/hub/session_routes.go`, `server/internal/hub/http.go`, `server/internal/node/node.go` — inspected unmerged edits. |
| Lightweight wire marks and parser index/window selection | server/internal/proto/navigation.go; server/internal/transcript/navigation.go; server/internal/node/navigation.go — new unmerged files. |
| iOS navigation tests | apple/PersonaTests/SessionNavigationTests.swift — new unmerged tests for forty-mark warm-up, sparse budgets/cursors, older-half prefetch, stable selection, request deduplication, stale/session replies and isolated windows. Read, not executed by this documentation task. |
| Server navigation tests | server/internal/transcript/navigation_test.go; server/internal/node/navigation_test.go; server/internal/hub/navigation_test.go — new unmerged tests for index filtering/paging, bounded windows and forwarding. Read relevant coverage, not executed by this documentation task. |
| Desktop rail, preview and pointer/keyboard selection | `web/src/views/chat/NavRail.tsx` — right-edge transient overlay, stable pointer capture, preview and scoped keyboard control. |
| Desktop loaded-message projection and adapter | `web/src/lib/nav-rail.ts`, `web/src/views/chat/ChatView.tsx` — `NavRailSource` keeps selection separate from loaded marks. No remote index or isolated around window is claimed before #2035 integration. |
| Desktop geometry/timing and browser acceptance | `web/src/ui/nav-rail-metrics.ts`; `web/e2e/user-navigation.mjs` — merged in #2080; mock-only wide/narrow, light/dark, hover, click/release, keyboard/Tab, idle fade and 240-input reachability. |
| Desktop regressions | `web/src/lib/nav-rail.test.ts`, `web/src/views/chat/NavRail.test.tsx` — model, filtering, moving window, cached viewport ownership, stable prepends, focus/fade and StrictMode empty-to-loaded frame cleanup. |

Any change to this interaction must re-verify:

- [ ] Touch central scroll/reveal does not scrub; vertical edge drag does; horizontal pager/drawer and left back swipe survive.
- [ ] Reveal, idle fade and destination/viewport-owner highlights work during long replies, image groups, folding and window paging.
- [ ] Desktop hover/drag previews input plus available answer; hover exit dismisses the card immediately without a timer; click/release commits once; cancellation, keyboard and focus behavior conform.
- [ ] Recent forty-mark warm-up is independent of bodies; sparse pages and older-half paging stay bounded; a page arriving under the pointer preserves identity.
- [ ] Injected, wake, cross-session and loop entries are excluded consistently; queued human and photo-only inputs survive; repeated text is not deduplicated away.
- [ ] Unloaded commit loads one around window, not the intervening history; live coverage/cache/unread state remain unchanged; latest restores the live source.
- [ ] Slow/error/missing/stale/unsupported cases and late replies after hide/session/credential replacement never land in the wrong conversation.
- [ ] Actual light/dark screenshots, touch/pointer interaction and keyboard/screen-reader/Reduce Motion checks pass on the affected clients; long-history performance is measured.
- [ ] Exact implementation paths and module docs stay current; the repository owner reviews the specification and any applicable approval gate is satisfied.

Run the [documentation checks](../operations/testing.md) and privacy scan for documentation changes. Those results
validate documentation, **not** runtime acceptance of the unmerged iOS/server implementation. Web runtime evidence is
recorded separately in [PR #2080](https://github.com/nwverse/persona/pull/2080#issuecomment-6733695670).
