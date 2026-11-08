# State model of the new shell (which page is in front)

Behind the new shell's (`docs/apple/ios.md` ("New shell")) "Assistant | Agents" switcher, the two-page strip, the drawer, and each page's navigation stack: who is in charge, how things sync, and how it's tested.
Reworked 2026-11-02, prompted by a user report and a string of patches.

## Background

The user's words (2026-11-02): "After viewing an Agent's content, I go back and want to switch to the assistant. I tap the Assistant / Agents switcher tab at the top and find that tapping Agents
doesn't actually switch to the Agents screen… It feels like the UI state management has an underlying design problem, which is why it seems we keep patching it."

Earlier patches: #174 search field overlapping the top bar, #182 nav bar left over after swiping back from knowledge search, #190 swiping left / right to switch pages, #178 a badge causing rebuilds, #203 avatar,
56b2ead moving the filter bar into the list, `ShellBarAtRest` / `NoRowSwipe`. Each fixed one symptom; "which page is current" itself had no single source of truth.

## Reproduction (before the change)

`ShellRaceTests` simulates a finger with coordinate taps (`ShellFinger`, not tapping accessibility elements): Agents → open an Agent's chat → go back (back button / swipe right;
tap before the back animation finishes, and after 0.15 s, 0.4 s, 1 s; leave while the chat is streaming) → tap Assistant → tap Agents. Each step reads the DEBUG probe
(`probe.shell`: which page the strip rests on, the selected segment, the App's tab, the drawer, whether both pages are at root, connection state) and `ShellTrace` (recent taps, routes, swipe events).

- Reproduced in 4 of 12 rounds: the probe says "strip rests on assistant, tab=agents", the Agents segment is bold, and the strip doesn't move.
- The trace reads: `switch chats` → `tab assistant->chats`, then no more shell body and no `route`. So the tap reached the switcher,
  and `AppModel.selectedTab` changed, but the shell's `.onChange(of: model.selectedTab)` didn't fire (SwiftUI didn't re-evaluate the shell's body), and the strip never got the instruction to slide.
- Once diverged, you can't tap your way back: the switcher decides which segment is "selected" from `selectedTab`, and the selected segment is a Menu (tap again to open this page's options).
  The assistant top bar passed an empty menu to the Agents segment, so every later tap on Agents opened an empty menu, which looks like the tap did nothing.
- Dragging diverges too: `ShellPagerTests` failed randomly 7 times in a 10-run matrix, with the probe at "strip rests on Agents, tab=assistant" and, again, no `route` in the trace after the drag settles.
- Fast repeated taps: when idle, `slide(to:)` only starts moving on the next run loop; a tap in the opposite direction during that window was blocked by "already there", and once it started moving it slid to the previously tapped page, leaving tab and strip reversed.
- Taps during a slide (about 0.4 s) were lost: the two pages' own top bars are transparent at that point, and the stand-in `ShellMovingBar` accepts no touches at all.

## Before the change: five sources for "which page is current"

```
 notifications / Siri / Inbox / drawer … (≈30 sites)        switcher (Binding)
                 │                                                │
                 ▼                                                ▼
        AppModel.selectedTab  ──── ShellView.onChange ────►  route ──► ShellPager.slide(to:)
          ▲        │        (leaks: doesn't fire if body isn't re-evaluated)  │
          │        │                                                     ▼
          │        └─► switcher's selected state, which segment is a Menu   ShellPager.position / page
          │                                                  │  pill, both pages' position / opacity / hit testing,
   pager.settled ◄─── drag release ◄──────────────────────────┘  top bar, DrawerPan direction, tabIsActive(#211)
```

| Source | Written by | Read by | How it syncs with the others |
| --- | --- | --- | --- |
| `AppModel.selectedTab` | ~30 routing sites, the switcher's Binding, drag settle | Switcher (selected state, Menu or button), the shell's `onChange` | `onChange` → `route` → `pager.slide`; leaks |
| `ShellPager.position` (`page` derived from it) | Drag, `slide`, settle animation | Pill, both pages' position / opacity / hit testing / z-order, top bar, `DrawerPan`, `tabIsActive` | Callback writes `selectedTab` on settle |
| Each page's navigation stack | `AssistantView`'s and `ChatsView`'s own `path` | Only reports "at root or not" to the shell (`chatAtRoot` / `workAtRoot`) | `onChange(of: path.isEmpty)` |
| Drawer `AssistantDrawer` | Drag, avatar button, rows in the drawer | `DrawerPan`, layout | Independent of pages; only looks at "is the current page at root" |
| `tabIsActive` (#211) | `ShellPageSlide`, from `pager.page` | Polling, read reporting | Follows the strip, not the selection |

Where they disagree or race: only a leaky `onChange` connects the first two rows; the switcher reads tab while the pill reads position, so after a divergence one control says two things;
`slide`'s async start and its "already at target" check; taps during a slide have nowhere to land.

**The page not in front** always stays in the window (the two pages are one ZStack, and Agents is never torn down after first use): transparent, `allowsHitTesting(false)`,
`accessibilityHidden`. But its UIKit parts (glass buttons, the Menu's UIButton, the navigation bar) came back into the accessibility tree after a pushed page was swiped back,
and XCUITest could tap them (`ShellSwitchTests` once tapped the hidden switcher that way); real finger taps never landed on it. So now the top bar of the page not in front isn't drawn at all (`ShellBarAtRest`).
When a page is covered by a pushed page (the assistant pushing Inbox or machines, Agents pushing a chat), its top bar is outside the window, a slide animation attached to it never finishes, and the strip's `settle` (`.removed`) waits forever,
leaving both pages "sliding" and ignoring taps (2026-11-03, entering an Agent chat from Inbox's Needs you, `ShellInboxAgentTests`). So the pages' top bars appear and disappear without animation;
only the switcher pill in `ShellMovingBar` follows position (`shellPillFollowsStrip`), while the pills in the pages' top bars stay on `shell.page`; whether the switcher's selected segment opens a menu also looks only at `moving`, not `pager.page`.
Nothing in a page's top bar reads `position` or the `pager.page` derived from it. `settle` also has a 1.2 s fallback (`settle lost`).

**Top bars**: each page draws its own (`AssistantTopBar`, `AgentsTopBar`), and during a slide the shell draws a `ShellMovingBar` as a stand-in; only one exists at any moment.
All three use `.shellTopBarScaled()`, so the font stops growing at `Theme.Shell.topBarLargestTypeSize` (xxxLarge) (#340). If the stand-in still used `.shellScaled()`, it would be larger than the pages' top bars at accessibility sizes, and the top bar would jump when a slide starts and stops. `ShellTopBarScaleTests` checks all three.
## Now: `ShellState.page` is the single source

```
 notifications / Siri / Inbox / drawer …   switcher (incl. the stand-in during a slide)   drag release (pager.settled)
            │                                   │                            │
   AppModel.selectedTab = x                     │                            │
            │ didSet, immediately (tabRouter)   │                            │
            ▼                                   ▼                            ▼
   ShellState.route(x)  ───────────────►  ShellState.select(page)  ◄─────────┘
                                          │  page = p (single source)
                                          │  pager.slide(to: p) (idempotent; a pending slide is replaced, one in progress turns around)
                                          │  AppModel.selectedTab = matching tab (for others to read)
                                          ▼
     Reads page: switcher's selected state and Menu, which page takes touches / is read by VoiceOver, tabIsActive, whether the drawer can be pulled (is the current page at root), probe
     Reads position: pill, both pages' slide position, top bars giving way during a slide
```

- **One source**: `ShellState.page` (0 assistant, 1 Agents, 2 Messages once the user turns it on). Only two methods change it: `select` (switcher, drag settle) and `route` (anywhere that writes `AppModel.selectedTab`).
  Both finish **immediately**, without waiting for a redraw: the `didSet` of `AppModel.selectedTab` calls `tabRouter` directly (`attach` when the shell appears, `detach` when it disappears),
  no more `onChange`. `.mail` pushes Inbox and returns to the assistant page; `.settings` opens the settings sheet and sets the tab back to the page in front. With **Old Layout** turned on, the shell stops taking routes and leaves them to the tab bar.
- **The strip follows page**: `ShellPager.slide(to:)` remembers where it's going (`goal`), and the same target again does nothing; a slide that hasn't started is replaced by the new one, and one in progress turns around;
  when there's no displacement to animate it stops right away (so it never stays `moving` waiting for an animation that won't end). While dragging, the finger is in charge; whichever page the release settles on gets `select`ed.
- **The switcher reads only page**: the selected state is page; a segment is a Menu (tap again for options) only when the strip is at rest on that page, and both segments are plain buttons during a slide.
  A tap calls `select` directly, not through a Binding. The switcher on the stand-in `ShellMovingBar` during a slide also takes taps: taps during the animation aren't lost, and the last one wins.
  If the strip stops while a finger is down (the tap lands just as the animation ends), `ShellMovingBar` stays until that finger lifts (`ShellPager.lingering`; a `Watcher` on the window reports finger down / up).
  Before, it was removed as soon as the strip stopped and the press on its switcher went nowhere; animations are slower under BeautifulUI, and `testQuickTapsEndOnTheLast` hit this almost every time (the third tap was lost).
- **The page not in front**: a page takes touches and is read by VoiceOver only when "the strip is at rest on it and page is this page"; `tabIsActive` follows page (half a drag isn't a page change).
  Only the front page's top bar is drawn; during a slide there's only the shell's.
- **Navigation stacks and the drawer** stay in their own pages: each page's `path` has its own rules (search results collapse the search before pushing, the space page's depth table…). The shell only needs "is the current page at root",
  and `ShellState.atRoot` picks it by page. That value arrives via SwiftUI state and occasionally lags: swiping right from the left edge inside a chat pushed on Agents, the strip took the gesture
  and flipped to the assistant while the chat was still pushed (3 of 24 runs in the matrix). So the strip's and drawer's drags ask UIKit again when they start: if the navigation stack under the finger has a pushed page, they yield to its back gesture
  (`DrawerPan.pushed`).

### Why there's still one top bar per page

The main session's direction was "the shell draws a single one". We didn't do that: the content of the two top bars belongs to each page (the assistant's chat menu reads the current chat and connection details, the Agents list options are bound to
`ChatsView`'s state, and during search the Agents top bar gives way to the system search field). Moving them into the shell would mean lifting all of that state up, a big change that also runs into #178's rebuild problem.
Now only one top bar exists at a time: the front page's when at rest, the shell's during a slide. The user sees and can tap only one, so the effect is the same.
## Launch

The ovis lane found that the chat page's `appeared()` ran several times in a row at launch (a DEBUG switch hooked into it sent its message repeatedly). We counted across launch modes, `-openSession`, restoring the last chat,
connected to a hub (`-pairURL`), and the connection notice opening / closing (`appeared` in `probe.chat` plus `ShellTrace`): the chat being opened appeared only once on both master
and this branch, and navigation didn't bounce back and forth (one route, the strip moving into place without animation). The extra appearance was **another chat**: the shell built both pages, so launching into Agents
also built the assistant page, and its chat "appeared" once behind the scenes: subscribing, `show`, marking as seen, everything hung on `onAppear` ran, though the user never saw it. The Old Layout's
TabView only builds a tab when it's shown, so it doesn't have this problem.

Now each page is built only when first selected or dragged in (`ShellState.assistantBuilt` / `agentsBuilt`), and the shell starts on the App's tab page right away
(`ShellState(page:)`, without waiting for the first route in `onAppear`). `ShellLaunchTests` asserts for each launch mode above: after launch only the front page's chat appears,
exactly once, and the page behind has no chat.

## Connection status and toasts

The "Connecting…" pill on a weak network used to float below the top bar, right on top of the Agents filter bar and the pulled-down search field; the global toast floated at the top of the window, and in the new shell it covered the avatar, switcher and new-chat buttons,
so tapping it dismissed it and swallowed the tap meant for the switcher (2026-11-02). At the time both went into a row under the status bar, making room by changing the window's safe area.
Later the toast went back to an overlay first (2026-11-04, #1053: "Archived" pushed the sidebar and page down and back up), and connection status became an overlay too (2026-11-04, #413; the user chose it from screen recordings of three approaches):
when that row collapsed, SwiftUI re-laid out the Agents navigation stack, the list read an inset of 0 for a few frames and jumped about 210pt in one frame, and the navigation bar sometimes (2 of 6 runs) took that as a scroll and collapsed and re-expanded the pulled-down search field.
As long as room is made through the window's safe area, it can't be fixed cleanly (the approaches tried and frame-by-frame data are in #413), so we no longer make room.

- **Both float; nothing moves**: connection status (Waiting for Network / Connecting… / Updating…) and toasts are drawn in a separate window of the shell (`ToastWindow`, one level above the shell window),
  right below the top bar, connection status on top and the toast below it; the drawer, both pages, pushed pages, and sheets presented by the shell are all beneath it. Appearing and disappearing never changes any page's layout, safe area, or list position.
- **No swallowed taps**: apart from the toast's button (Undo), that window takes no touches, so the filter bar, search field and switcher under the pill and toast stay tappable; which also means they can't be tapped away: connection status follows the connection, and a toast leaves on its own when time is up.
  Since VoiceOver won't move to a window that takes no touches, a toast is announced when it appears (`AccessibilityNotification.Announcement`).
- **Appears immediately**: nothing moves, so there's no need to wait for the finger to lift; as soon as `ConnectionStatus.shown` is set it goes to `ShellNotices` (`onShown`), without relying on a view redraw.
- **One-off results are toasts**: the result of something just done (archived, deleted, pinned, copied, submitted, send failed, deploy failed…, i.e. every `AppModel.toast`),
  short (info / success 2.5 s, error 4 s), one at a time, the new one replacing the old. It looks like the design system's `ToastCapsule`; connection status is `ShellLinkPill` (the same capsule:
  Liquid Glass, or the raised capsule under the BeautifulUI style). The keyboard is at the bottom and these are at the top, so they're never covered.
- **Said once**: while a page whose own title shows connection status is in front (an agent's conversation, a subagent's included; Inbox and Machines pushed in the shell, `ConnectionTitle`),
  it registers with `.titleSaysLink()` and the shell's pill stays away (`ShellNotices.pill`), so "Connecting…" is not said again under the title; back on a page whose title doesn't show it (Assistant, the Agents list) the pill shows as before.
- Old Layout, iPad and iPhone Duo open: the toast floats in a toast window of the same kind (`ToastWindowHook` on `RootTabs`, no connection pill; nothing to tap but its button),
  over the image viewer, the share card and every other cover or sheet. Before #1492 it was `RootView`'s overlay, under all of them: Saved to Photos never showed there. Connection status is in the title.
- Mac and Apple Vision Pro: the toast is `ToastOverlay` in `RootView` (the same `ToastCapsule`, tap to dismiss).
  Before the shell or the tabs are on screen (the welcome page, the engine step and the checklist after pairing), and while an extension's page takes the app's place, there is no `ToastWindow` either, so `ToastOverlay` is used there too: otherwise an error from opening a pairing link on the welcome page (hub out of reach, incomplete link) would never show (#4273).

DEBUG: `-showConnectionCycle connecting:7:4` (from second 7, show Connecting… for 4 seconds, then recover; `waiting` / `connecting` / `updating`),
`-showToasts "5:info:text;5.4:error:text"` (pop toasts at those times).

| Item | Definition | Test |
| --- | --- | --- |
| Connection status doesn't move layout | Three connection states, both pages: when it appears and after it goes away, the avatar, switcher, new-chat button, filter bar, search field, input field and on-screen rows don't move, and the pill sits below the top bar buttons | `ShellStatusRowTests.testTheLinkFloatsAndMovesNothingInEveryState` |
| Search field doesn't flicker | Connection status comes and goes 12 times; the pulled-down / collapsed search field doesn't change, rows don't move, the navigation container's safe area doesn't change | `ShellRoomScrollTests` |
| Toasts don't push layout | When a toast appears and disappears, the first row of the Agents list (`probe.agents-scroll`), the new-chat button and the drawer rows don't move, and no connection status is dragged in | `ShellToastTests` |
| Toasts don't swallow taps | While a toast is showing, taps on the switcher and drawer rows under it still work | `ShellToastTests` |
| Toasts aren't covered by the keyboard | With the keyboard up, the toast is entirely on screen and doesn't intersect the keyboard | `ShellToastTests.testAToastWithTheKeyboardUpIsAboveIt` |
| The finger comes first | While a finger is down, Connecting… doesn't expand until it lifts; Waiting for Network expands immediately | `testTheRowWaitsForTheFingerUnlessUrgent` |

## Consistency and timeliness (acceptance criteria)

The "true state" here lives in the App (not in hub data): the segment the user last tapped, the page a drag settled on, the last route.

| Item | Definition | Limit | Test |
| --- | --- | --- | --- |
| Consistent | At rest, page the strip rests on == page selected in the switcher (`page`) == the App's tab; no field is allowed to lag | — | `agentsDrawn` / `assistantDrawn`: all three must match |
| Timely | After a tap or route, the strip comes to rest on the target page | Animation about 0.4 s; tests allow 1.5 s including probe read overhead | `ShellFinger.switchTo`, `expectShell` |
| No swallowed taps | Taps during an animation, repeated taps, and taps during a back animation all count; it ends on the last page tapped | Same as above | `testQuickTapsEndOnTheLast`, `testBackThenSwitchAtEveryPace` |
| Converges | After any combination of taps, drags, routes, and entering / leaving chats, it returns to the three items above being consistent | Same as above | Each case in `ShellRaceTests`, `ShellPagerTests` |

A tap before the back animation finishes may land on the chat page that's sliding away (same as a real finger); in that case the test allows 2.5 s and requires the following tap to work normally.

It shares one set of probes and assertions with the msg-render lane (ScreenProbe, `docs/apple/ios-state.md` ("Probes")): the shell's state is `probe.shell`, which needs `-probes YES` at launch;
on the test side, `shellPage` / `shellValue` / `shellRoom` / `shellTrace` in `ShellPageProbe.swift` read from it (#499; the old `shell-pager-probe` has been deleted).

## Tests

| Test | Scenario |
| --- | --- |
| `ShellRaceTests` | The user's flow this time: go back after viewing an Agent's chat (button / swipe right, tap after 0–1 s), leave while streaming, fast repeated taps, drag right after a tap, route in from an empty chat's "Needs You" and then switch |
| `ShellInboxAgentTests` | Tap into an Agent chat from "Needs You" in Inbox (pushed on the assistant) → back → switch to the assistant (still Inbox, with its own navigation bar) → back → switch to Agents again: probe consistent at every step, the switcher isn't on the pushed page, Inbox can scroll and be tapped, no `settle lost` |
| `ShellSwitchTests` | #182 etc.: switching after pages from the drawer (knowledge base, Inbox, scheduled tasks, machines) are pushed and popped, with coordinate taps |
| `ShellSearchTests` | #174: search giving way, switching after cancel |
| `ShellPagerTests` | #190 swiping left / right to flip pages, swiping right on the filter bar to flip back after 56b2ead |
| `ShellAvatarTests` | #203: the avatar and drawer on both pages, switching between them with coordinate taps |
| `ShellTests` | Switching, drawer, Old Layout toggle |
| `ShellLaunchTests` | Launching into a chat (`-openSession`, restoring the last one, connected to a hub, while the connection notice opens / closes) and launching on the assistant: only the front chat appears, exactly once |

The switcher is always tapped with `ShellFinger` (`PersonaUITests/ShellPageProbe.swift`): at launch it reads the positions of both segments once from the assistant page's switcher, then always taps those two coordinates,
and after each tap asserts the three-way consistency and the time limit; on failure it prints the probe and the recent `ShellTrace`.

### Optional Messages page (#392)

**Show Messages on Home** in Lark settings is off by default; turning it on adds a third page to the shell, reusing the same `ShellState.page`, router, top bar switcher, and drag model. The Messages page is created only when first selected or dragged in; the settings sheet doesn't switch the current Messages page back to the assistant. Turning the switch off while on the Messages page returns to the assistant immediately, and a stale `.feishu` route also goes to the assistant, so no invisible page is left behind. `LarkMessagesShellTests` covers round trips across the three pages, reversing a drag to cancel, turning it off and on again, launching on the Messages page, and returning from settings.
