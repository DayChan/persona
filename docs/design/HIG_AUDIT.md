# Apple HIG audit of Persona's Apple clients

Snapshot commit 738ba824c, date 2026-11-10. Line numbers refer to that commit. This is a static code review; nothing was run on a device.

## 1. Summary

**Verdict: broadly conformant, with a consistent set of gaps in secondary controls.** The default iOS path uses system components and tokens: Dynamic Type text styles, semantic colors, `confirmationDialog`, system sheets with detents, Liquid Glass through one `surface()` modifier, and a main composer whose Send button is labeled and sized. No finding rises to P0. The failures cluster in a few repeating patterns: icon-only controls drawn at glyph size, custom gestures and tap handlers with no accessibility path, button labels that shrink or truncate instead of using shorter copy or a stacked group, and destructive swipes without undo. The design system already holds most of the fixes (`tapTargetFrame()`, `Theme.Motion.reduced`, `Theme.Icon`), but they are opt-in and many call sites bypass them.

Counts (85 findings):

| Severity | Count |
|---|---|
| P0 | 0 |
| P1 | 16 |
| P2 | 69 |

| Area | P1 | P2 | Total |
|---|---|---|---|
| Touch targets | 6 | 9 | 15 |
| Dynamic Type | 2 | 5 | 7 |
| Contrast, color, dark mode | 1 | 10 | 11 |
| VoiceOver and labels | 6 | 2 | 8 |
| Reduce Motion | 0 | 7 | 7 |
| SF Symbols, components, navigation | 0 | 18 | 18 |
| Feedback, notifications, copy | 1 | 7 | 8 |
| Watch, widgets, Live Activity, Mac, Vision | 0 | 8 | 8 |
| Layout, safe areas, keyboard | 0 | 3 | 3 |

Most serious problems:

| Rank | ID | Title | Location | Severity |
|---|---|---|---|---|
| 1 | HIG-001 | Message actions (Copy and the rest) open only by long press; VoiceOver gets only Quote | `apple/Persona/Views/Chat/MessageMenu.swift:284` | P1 |
| 2 | HIG-002 | Answer rows on agent question cards are tap gestures with no button trait | `apple/Persona/Views/Interactions/InteractionViews.swift:545` | P1 |
| 3 | HIG-003 | Approve, Deny and Submit labels shrink and truncate; no stacked button group at large type | `apple/Persona/Views/Interactions/InteractionViews.swift:671` | P1 |
| 4 | HIG-004 | Swipe-to-delete on to-dos, places and tasks runs at once with no confirmation or Undo | `apple/Persona/Views/Todos/TodosView.swift:125` | P1 |
| 5 | HIG-005 | Toast Undo is a text-height target in a time-boxed toast VoiceOver cannot reach | `apple/Persona/DesignSystem/Toasts.swift:46` | P1 |

Strengths worth keeping:

- Fonts are SwiftUI text styles, so Dynamic Type works by default, and the in-app Text Size setting layers on top (`apple/Persona/Views/RootView.swift:113`). Dynamic Type caps are documented and tested.
- The main chat composer is well built: labeled Send, 44pt round buttons, hold-to-dictate with a tap fallback and a VoiceOver action.
- Colors are semantic or light/dark pairs; `StatusPalette` and the Role tokens were contrast-tested; `StyleSpecTests` guards some ratios.
- Liquid Glass is applied in one place (`apple/Persona/DesignSystem/Styling.swift:47`), not scattered.
- `StatusDot` labels its state for VoiceOver and falls back to a still ring under Reduce Motion. `MessageLift` is modal, has an escape action and a labeled scrim. Long-running looping motion in the app target is mostly guarded.
- Destructive flows on detail screens use `confirmationDialog` with Cancel; notification settings offer Open Settings; the app respects the system appearance by default.

## 2. Method and scope

HIG pages were fetched from developer.apple.com on 2026-11-10, together with the "What's new" log (latest entry 2026-11-06). Pages consulted: [Accessibility][a11y], [VoiceOver][vo], [Typography][typo], [Color][color], [Dark Mode][dark], [Materials][mat], [Motion][motion], [Buttons][btn], [Toolbars][tb], [Tab bars][tab], [Sheets][sheets], [Alerts][alerts], [Undo and redo][undo], [Playing haptics][haptics], [Notifications][notifs], [Gestures][gest], [Privacy][priv], [Managing notifications][notif], [Writing][writing], [SF Symbols][sym], [Icons][icons], [Widgets][widgets], [Live Activities][la], [Design principles][dp], [Progress indicators][prog], [Entering data][enter], and others linked per finding. Notable current rules: the iOS default control size is 44x44 pt with a 28x28 pt minimum; macOS 28/20; visionOS 60/28; watchOS 44/28 (Accessibility page), and the Buttons page states the 44x44 pt hit region (60x60 pt in visionOS). Text minimum contrast is 4.5:1 up to 17 pt and 3:1 for 18 pt or bold. Not verified against an Apple page: claims about WWDC26 Liquid Glass transparency changes (not used here).

Severity:

- **P0**: an accessibility or HIG failure on a primary flow that blocks or seriously harms a group of users (for example the main composer Send button unreachable or unlabeled for VoiceOver, primary chat text ignoring Dynamic Type, unreadable contrast on primary content, content under the home indicator on a main screen, an irreversible destructive action without confirmation).
- **P1**: a clear violation on a frequently used screen, or a pattern repeated across many screens.
- **P2**: minor, polish, a rare screen, or debatable.

Checked: touch targets, Dynamic Type, dark mode, contrast, VoiceOver labels and traits, Reduce Motion and Reduce Transparency, SF Symbols, system components, navigation, safe areas, feedback and haptics, notifications and permissions, plus the watch app, complications, widgets, Live Activity, Mac and visionOS code.

Limitations: static review only. Hit areas, contrast ratios and heights were computed from code, not measured on a device. Increase Contrast rendering and real safe-area and keyboard behavior were not exercised on a device; the layout, safe-area and keyboard dimension was reviewed by reading the code and yielded three P2 findings (HIG-083 to HIG-085), with no safe-area or keyboard-overlap defect found beyond them. The Mac app is frozen (see `docs/apple/mac-design.md`), so Mac findings matter only for bug fixes. Findings that failed verification are listed in the appendix.

## 3. Checklist

| HIG area | Status | Findings |
|---|---|---|
| Touch targets (44 default, 28 minimum) | Partial | HIG-007, 013-016, 017-025 |
| Dynamic Type | Partial | HIG-003, 012, 026-030 |
| Dark mode | Partial | HIG-037, 038 |
| Contrast and Increase Contrast | Partial | HIG-010, 031-036 |
| Color not the only signal | Partial | HIG-011, 040 |
| VoiceOver labels, traits, announcements | Partial | HIG-001, 002, 006, 008, 009, 021, 041, 042, 075, 076, 081 |
| Reduce Motion | Partial | HIG-043-049 |
| Reduce Transparency | Partial | HIG-039 |
| SF Symbols | Partial | HIG-052, 055, 056 |
| System components | Partial | HIG-050, 051, 053, 054, 057 |
| Navigation patterns | Partial | HIG-053, 060, 061, 063, 067 |
| Sheets and modality | Partial | HIG-062, 065 |
| Layout, safe areas, keyboard avoidance | Partial | HIG-083, 084, 085; no other defect found by static review |
| Destructive actions, undo, confirmation | Partial | HIG-004, 005, 058, 059, 066 |
| Feedback and haptics | Partial | HIG-042, 068, 070, 071 |
| Notifications and permissions | Partial | HIG-069, 072, 073, 074 |
| Writing and localization | Partial | HIG-068, 074, 078 |
| Watch | Partial | HIG-011, 012, 075, 076, 082 |
| Widgets and Live Activity | Partial | HIG-022, 077, 079 |
| Mac (frozen) | Partial | HIG-024, 078, 080, 081 |
| visionOS | Partial | HIG-025 |
| iPad keyboard and pointer | Partial | HIG-064 |

## 4. Findings

Each block lists: Severity, Platform, Location, Violates, Problem, Evidence, Fix.

### P1

#### HIG-001 Message actions open only by long press; VoiceOver gets only Quote
- **Severity**: P1. **Platform**: iOS.
- **Location**: Chat. `apple/Persona/Views/Chat/MessageMenu.swift:284`, `apple/Persona/Views/Chat/MessageMenu.swift:371`, `apple/Persona/Views/Chat/MessageViews.swift:180`, `apple/Persona/Views/Chat/MessageViews.swift:520`.
- **Violates**: [Gestures][gest] and [Accessibility][a11y]: custom gestures must never be the only way, and gestures need alternatives.
- **Problem**: the action bar (Copy and the rest) opens through a custom long-press recognizer. The only accessibility action is Quote, attached to the swipe path, so VoiceOver users cannot copy a message. The lifted overlay is itself accessible (modal, escape action, labeled scrim, `apple/Persona/Views/Chat/MessageLift.swift:162`). Only secondary rows have a `contextMenu`.
- **Evidence**: `.gesture(LongPressAt(changed: menu.pressChanged) { p in ... lift(menu) })`; `.accessibilityAction(named: "Quote")`.
- **Fix**: in the long-press modifier add `.accessibilityAction(named: String(localized: "Message Actions"))` that calls the same `lift(menu)`. Optionally expose Copy as its own named action. Localize the hard-coded "Quote".

#### HIG-002 Tap gestures used as buttons on agent cards and other rows
- **Severity**: P1. **Platform**: iOS.
- **Location**: agent question and permission card, loop bar, calendar. `apple/Persona/Views/Interactions/InteractionViews.swift:373`, `apple/Persona/Views/Interactions/InteractionViews.swift:545`, `apple/Persona/Views/Interactions/InteractionViews.swift:1113`, `apple/Persona/Views/Session/SessionPanes.swift:1221`, `apple/Persona/Views/Todos/Calendar/TodoCalendarGrids.swift:347`.
- **Violates**: [VoiceOver][vo]: label all key and custom elements; [Accessibility][a11y]: label elements so Voice Control and Switch Control work.
- **Problem**: `onTapGesture` on a non-Button view with no `isButton` trait, label or action, so VoiceOver reads static text. The "Other answer" rows are the only route to the free-text answer on the Needs You flow. Other sites do it right (`apple/Persona/Views/Assistant/AssistantSidebar.swift:370`). A repo grep shows 42 `onTapGesture` uses; the exact count of unlabeled ones was not established (one cited site, `apple/Persona/Views/Chat/ToolViews.swift:1099`, is fine).
- **Evidence**: `.contentShape(.rect).onTapGesture { withAnimation(Theme.Motion.standard) { collapsed.toggle() } }`.
- **Fix**: use `Button` with `.buttonStyle(.plain)`, or add `.accessibilityAddTraits(.isButton)` plus a label and an expanded/collapsed value. Label the answer rows "Write your own answer".

#### HIG-003 Primary action buttons are one line and shrink; no stacked layout at large sizes
- **Severity**: P1. **Platform**: iOS, iPadOS.
- **Location**: `apple/Persona/Views/Interactions/InteractionViews.swift:671`, `apple/Persona/Views/Interactions/InteractionViews.swift:722`, `apple/Persona/Views/Interactions/InteractionViews.swift:794`, `apple/Persona/Views/Interactions/ElicitationForm.swift:224`, `apple/Persona/Views/Interactions/SecretRunForm.swift:38`, `apple/Persona/DesignSystem/Components.swift:559`, `apple/Persona/Views/Todos/TodoDetailView.swift:475`.
- **Violates**: [Typography][typo] (Dynamic Type): minimize truncation, switch to stacked layouts at large sizes; [Accessibility][a11y]: allow at least 200% enlargement.
- **Problem**: the project rule "button text never wraps" (which stands) is implemented only as `lineLimit(1)` with `minimumScaleFactor(0.8)` on the permission, plan, elicitation, secret-run, stop-and-close and to-do cards, which is how a user answers a blocked agent. Longer labels shrink 20% then truncate at accessibility sizes; Deny/Allow and Decline/Submit share one row. No stacked fallback exists in these files, although `isAccessibilitySize` is used elsewhere (`apple/Persona/Views/Settings/PersonaSettingsView.swift:156`). The pattern repeats on about 50 labels. Allow and Deny stay distinguishable, so not P0.
- **Evidence**: `Label("Approve & Auto-Run", systemImage: Theme.Icon.run).lineLimit(1).minimumScaleFactor(0.8)`.
- **Fix**: keep the project rule: button text stays on one line and does not wrap (decided 2026-11-10; `docs/design/DESIGN_SYSTEM.md` section 1.2, rule 3). For each label that would truncate, first shorten the copy in every language (for example "Approve & Auto-Run"); then ask whether a button fits, and move long or variable text into the card body with short verbs on the buttons. Add a design-system button-row helper that switches the whole group from `HStack` to `VStack` (primary first, full width) when `dynamicTypeSize.isAccessibilitySize`, each label still `lineLimit(1)`. Keep `minimumScaleFactor` only as a last resort for small overflows. Apply to the cards listed first.

#### HIG-004 Swipe-to-delete with no confirmation and no Undo
- **Severity**: P1. **Platform**: iOS, iPadOS.
- **Location**: `apple/Persona/Views/Todos/TodosView.swift:125`, `apple/Persona/Views/Todos/TodosView.swift:157`, `apple/Persona/App/TodoStore.swift:216`, `apple/Persona/Views/Settings/SavedPlacesView.swift:56`, `apple/Persona/Views/Settings/AssistantProactiveViews.swift:280`.
- **Violates**: [Undo and redo][undo], [Alerts][alerts], [Design principles][dp] ("help people recover from mistakes"): a destructive action should be undoable or confirmed.
- **Problem**: the trailing swipe deletes on first tap; to-dos allow full swipe. The store calls the server and removes the item, with no restore. The same items are confirmed on their detail screens, and memory entries confirm, so the behavior is inconsistent. Context-menu Delete in `apple/Persona/Views/Settings/SavedPlacesView.swift:61` and `apple/Persona/Views/Settings/AssistantProactiveViews.swift:286` is also unconfirmed. Repeating scheduled tasks need a deliberate tap, which lowers risk there.
- **Evidence**: `.swipeActions(edge: .trailing) { Button(role: .destructive) { Task { try await store.delete(id: todo.id) } } ... }`.
- **Fix**: preferred, optimistic removal with an Undo toast and a delayed server commit, as `apple/Persona/Views/Agents/PersonalAgentListView.swift` does. Fallback: `allowsFullSwipe: false` and route swipe and menu Delete through the same `confirmationDialog` as detail. Put it in one shared helper.

#### HIG-005 Toast Undo target is text-height and the toast is time-boxed and unreachable for VoiceOver
- **Severity**: P1. **Platform**: iOS, iPadOS.
- **Location**: `apple/Persona/DesignSystem/Toasts.swift:46`, `apple/Persona/DesignSystem/Toasts.swift:51`, `apple/Persona/DesignSystem/Toasts.swift:57`, `apple/Persona/DesignSystem/Toasts.swift:80`, `apple/Persona/Views/Shell/ShellStatus.swift:176`, `apple/Persona/App/InboxStore.swift:217`, `apple/Persona/Views/Session/FolderEdits.swift:91`, `apple/Persona/Views/Inbox/InboxHandOffBar.swift:278`.
- **Violates**: [Accessibility][a11y]: 44pt default and 28pt minimum controls; minimize auto-dismissing, time-boxed UI. [VoiceOver][vo]: tell VoiceOver about content changes.
- **Problem**: the Undo/Retry action is `Text` with only `contentShape(.rect)`, about 20pt tall, in a toast shown for 5 s (4 s error, 2.5 s otherwise). The toast lives in a pass-through window that takes touches only over the action; the code comment says VoiceOver does not come to that window. The announcement reads only the message, not the action, and the overlay variant with tap-to-dismiss posts none. Inbox filing and folder edits use this toast as their only undo.
- **Evidence**: `Text(action.title).font(Theme.Font.secondaryStrong)....contentShape(.rect)`; `.seconds(toast.action != nil ? 5 : toast.style == .error ? 4 : 2.5)`.
- **Fix**: give the action `tapTargetFrame()` and rebalance row padding. When VoiceOver or Switch Control is running, keep action toasts until dismissed. Append the action to the announcement and expose it with `accessibilityAction(named:)`. Provide an in-app undo route for inbox filing and folder edits.

#### HIG-006 To-do completion checkbox has no label, value or selected state
- **Severity**: P1. **Platform**: iOS.
- **Location**: To-Dos list. `apple/Persona/Views/Todos/TodosView.swift:286`, `apple/Persona/Views/Todos/TodosView.swift:272`, `apple/Persona/Views/Machines/BrowserLoginsSection.swift:231`, `apple/Persona/Views/Assistant/Tasks/TaskViews.swift:165`.
- **Violates**: [VoiceOver][vo]: alternative labels for all key and custom elements, kept current; [Accessibility][a11y]: more than color or glyph alone.
- **Problem**: the button's label is only a `ThemedIcon` swapping circle and filled check. The file's only accessibility modifier is at line 76. VoiceOver reads a symbol-derived name and never says whether the to-do is done. The swipe actions "Done" and "Reopen" offer a labeled alternative through the actions rotor. Two other rows convey state only by the icon swap. `apple/Persona/Views/Session/FilesPane.swift:365` already does it right.
- **Evidence**: `Button(action: onToggleDone) { ThemedIcon(todo.isDone ? Theme.Icon.successFilled : Theme.Icon.unchecked)... }`.
- **Fix**: add a design-system check-row wrapper taking a label and state that sets `accessibilityLabel`, a "done"/"not done" value and `.isSelected`. Migrate the three sites. Do not wrap the whole to-do row in `.combine`, which would swallow the checkbox.

#### HIG-007 To-do checkbox 44pt frame sits outside the Button, so the hit region stays glyph-sized
- **Severity**: P1. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Todos/TodosView.swift:286`, `apple/Persona/Views/Todos/TodosView.swift:292`, `apple/Persona/Views/Todos/TodosView.swift:114`.
- **Violates**: [Buttons][btn]: hit region at least 44x44 pt; [Accessibility][a11y]: 28x28 pt minimum control size on iOS.
- **Problem**: `.scaledFrame(tapTarget)` follows `.buttonStyle(.plain)`, so it only affects layout; the label is a title-size glyph (about 22-26pt) with no `contentShape`. A tap in the 44pt box but off the glyph falls through to the row's `NavigationLink` and opens the detail. `ComposerButtons.swift` and `ViewerChrome.swift` put the frame inside the label. Not verified in a simulator.
- **Evidence**: `.buttonStyle(.plain).scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)`.
- **Fix**: move `tapTargetFrame().contentShape(.rect)` inside the label and delete the outer frame.

#### HIG-008 Icon-only send, reply and interrupt buttons have no accessibility label
- **Severity**: P1. **Platform**: iOS, watchOS.
- **Location**: `apple/Persona/Views/Agents/PersonalAgentSessionView.swift:78`, `apple/Persona/Views/Interactions/InteractionViews.swift:1137`, `apple/Persona/Views/Mail/MailViews.swift:189`, `apple/PersonaWatch/PersonaWatchApp.swift:856`.
- **Violates**: [VoiceOver][vo]: alternative labels for all key interface elements; [Accessibility][a11y]: Voice Control and Switch Control need labeled elements.
- **Problem**: Buttons with only a symbol. VoiceOver reads names like "Up circle fill" or "Stop fill". The main composer is correct, so these are inconsistent with it. Severity differs by site: Mail reply is a frequent action; the personal-agent send is a secondary composer; the terminal send and the watch Interrupt are rarer. Interrupting is recoverable, so no confirmation is needed.
- **Evidence**: `ThemedIcon(Theme.Icon.submit).font(Theme.Font.title)` in a `Button` with no label.
- **Fix**: add localized labels ("Send", "Send to terminal", "Reply", "Interrupt") or use `Label(...).labelStyle(.iconOnly)` as `apple/PersonaWatch/WatchRecordingView.swift:48` does. Consider an icon button helper that requires a title.

#### HIG-009 New replies and Needs You requests are not announced to VoiceOver
- **Severity**: P1. **Platform**: iOS.
- **Location**: Chat, Needs You. `apple/Persona/Views/Chat/ChatView.swift:2528`, `apple/Persona/Views/Interactions/InteractionViews.swift:259`, `apple/Persona/Views/Chat/MessageViews.swift:670`.
- **Violates**: [VoiceOver][vo]: tell VoiceOver when visible content or layout changes.
- **Problem**: the whole app posts six accessibility announcements (toasts, copy confirmation, voice bar, top bar); none in `apple/Persona/Views/Chat` or `apple/Persona/Views/Interactions`. Reply completion, a blocked agent card appearing and a send failure are silent. The thinking indicator has `updatesFrequently`. This is inferred from absence, not run with VoiceOver.
- **Evidence**: `grep -rn AccessibilityNotification apple` lists no chat or interaction file.
- **Fix**: post one announcement when `liveTurnID` goes from set to nil ("Reply ready"), and a layout-changed or announcement when a question or permission card first appears. Never announce per token.

#### HIG-010 Timestamps and meta text use the tertiary label color
- **Severity**: P1. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Assistant/Tasks/SentViews.swift:182`, `apple/Persona/Views/Assistant/Tasks/TodayCard.swift:213`, `apple/Persona/Views/Chats/MachineSpacesView.swift:125`, `apple/Persona/Views/Messages/MessagesViews.swift:391`.
- **Violates**: [Accessibility][a11y] (contrast): 4.5:1 for text up to 17 pt; [Dark Mode][dark]: at least 4.5:1, strive for 7:1 especially in small text.
- **Problem**: tertiary label is about 2:1 against its background (not measured per surface). It colors readable text: sent-row time, date header, relative update time, "Edited". `apple/Persona/Views/BeautifulUIBridge.swift:10` already records it as unfit for the library. About 160 tertiary or quaternary uses exist; the text ones need triage. Decorative chevrons (`apple/Persona/Views/Assistant/Tasks/SentViews.swift:152`) are fine.
- **Evidence**: `Text(AssistantScheduleText.hm(item.at)).font(Theme.Font.caption).foregroundStyle(.tertiary)`.
- **Fix**: add a Theme text-tertiary token mapped to secondary label (or a custom pair at 4.5:1 or more) and use it for text. Keep `.tertiary` for hidden decorative glyphs.

#### HIG-011 Watch Running list shows agent state only as an 8 pt colored dot
- **Severity**: P1. **Platform**: watchOS.
- **Location**: `apple/PersonaWatch/PersonaWatchApp.swift:474`, `apple/PersonaWatch/PersonaWatchApp.swift:471`.
- **Violates**: [Accessibility][a11y]: convey information with more than color alone; [VoiceOver][vo]: labels for custom elements.
- **Problem**: each row marks needs-you, working or done with a colored circle only. The state words appear only inside the detail page. Color-blind and VoiceOver users cannot tell which agent waits on them without opening each row. The detail header (line 793) is fine.
- **Evidence**: `Circle().fill(s.stateTone.dot).frame(width: 8, height: 8)`.
- **Fix**: hide the dot from VoiceOver and add the state word as the row's accessibility value; show a per-state symbol (the iOS Live Activity mapping) next to or instead of the dot.

#### HIG-012 Watch answer and screen-option buttons truncate to one line
- **Severity**: P1. **Platform**: watchOS.
- **Location**: `apple/PersonaWatch/PersonaWatchApp.swift:638`, `apple/PersonaWatch/PersonaWatchApp.swift:684`, `apple/PersonaWatch/PersonaWatchApp.swift:701`, `apple/PersonaWatch/PersonaWatchApp.swift:708` (P2 polish: `apple/PersonaWatch/PersonaWatchApp.swift:661`, `apple/PersonaWatch/PersonaWatchApp.swift:721`).
- **Violates**: [Typography][typo]: minimize truncation, show as much useful text at the largest accessibility size, stack at large sizes.
- **Problem**: the options are the content the user chooses between, taken from a terminal screen; they are `lineLimit(1)` with `minimumScaleFactor(0.8)`, and several use caption2. Options with a shared prefix become identical once clipped, and the user approves agent actions on that basis.
- **Evidence**: `Text("\(opt.key). \(opt.label)").lineLimit(1).minimumScaleFactor(0.8)`.
- **Fix**: button text still never wraps (`docs/design/DESIGN_SYSTEM.md` section 1.2, rule 3). These options are content copied from a terminal, not button labels, so a row of truncated buttons is the wrong control: show each option's full text as content (a list of selectable rows, or the text above with short keyed buttons such as "1" and "2"), drop the scale factor, and raise caption2 to footnote. Keep one-line buttons for short verbs (Allow, Deny, Submit), stacked vertically at accessibility sizes.

#### HIG-013 Composer image-thumbnail remove button has no hit area beyond its glyph
- **Severity**: P1. **Platform**: iOS.
- **Location**: main chat composer. `apple/Persona/Views/Chat/ComposerParts.swift:163`, `apple/Persona/Views/Chat/ComposerParts.swift:167`, `apple/Persona/Views/Chat/ComposerParts.swift:160`, `apple/Persona/DesignSystem/TextSize.swift:116`. Same view in `apple/Persona/Views/Assistant/AssistantView.swift:956` and `apple/Persona/Views/NewChat/NewAgentAttachments.swift:118`.
- **Violates**: [Accessibility][a11y] and [Buttons][btn]: 44 default, 28 minimum hit region.
- **Problem**: the button is a bare palette-styled icon, about 17pt, offset outside the 56pt thumbnail. A miss lands on the thumbnail's own tap (editor). The thumbnail tap has no button trait. `tapTargetFrame()` documents this exact case.
- **Evidence**: `Button { images.remove(at: idx) } label: { ThemedIcon(Theme.Icon.clear).symbolRenderingMode(.palette)... }.offset(x: 5, y: -5)`.
- **Fix**: add `.tapTargetFrame().contentShape(.rect)` to the label and adjust the offset. Add `.isButton` to the thumbnail when it opens the editor.

#### HIG-014 LinkSheet header icons are unframed title-size glyphs
- **Severity**: P1. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Link/LinkSheet.swift:168`, `apple/Persona/Views/Link/LinkSheet.swift:173`, `apple/Persona/Views/Link/LinkSheet.swift:177`, `apple/Persona/Views/Link/LinkSheet.swift:178`, `apple/Persona/Views/Link/LinkSheet.swift:182`.
- **Violates**: [Accessibility][a11y] and [Buttons][btn]: hit region and spacing; [Modality](https://developer.apple.com/design/human-interface-guidelines/modality): obvious dismissal.
- **Problem**: add-context, reload, a More menu and Close are bare icons about 22-26pt in a tight row, so a miss on Close can hit the menu. The sheet opens on every link tap. The pattern repeats (`apple/Persona/Views/Inbox/InboxView.swift:131`, `apple/Persona/Views/Inbox/InboxView.swift:1115`, `apple/Persona/Views/Chat/FileViewer.swift:334`). Close is labeled; the Menu would get only a default symbol description.
- **Evidence**: `Menu { ... } label: { ThemedIcon(Theme.Icon.moreMenu) }` inside `.font(Theme.Font.title)`.
- **Fix**: `tapTargetFrame().contentShape(.rect)` on all four, label the menu "More", or move them into a native toolbar. Extract a shared header icon-button.

#### HIG-015 Dismiss (x) buttons on in-chat banners and cards are 12-24pt
- **Severity**: P1. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Assistant/Tasks/AssistantActionBar.swift:183`, `apple/Persona/Views/Assistant/Tasks/TaskViews.swift:1173`, `apple/Persona/Views/Assistant/MainConversationActionsView.swift:191`, `apple/Persona/Views/Chat/UltracodeViews.swift:130`, `apple/Persona/DesignSystem/ReadinessCard.swift:53`, `apple/Persona/Views/Settings/HubUpdateViews.swift:423`, `apple/Shared/Design/Theme.swift:489`, `apple/Shared/Design/Theme.swift:493`.
- **Violates**: [Accessibility][a11y] and [Buttons][btn].
- **Problem**: the glyph is framed with `Theme.Size.badge` (18) or `Theme.Size.icon` (24) and a matching `contentShape`; two have no frame at all (12-17pt). `scaledFrame` can shrink below the token at Small text. The action-bar close sits 4-8pt from the "All" chip. The Settings notice row (`HubUpdateViews.swift`) is a rarer surface. The `TaskViews.swift:1173` button is labeled "Hand to Persona", a label mismatch for a close glyph.
- **Evidence**: `ThemedIcon(Theme.Icon.close)....scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge).contentShape(.circle)`.
- **Fix**: one design-system dismiss button bundling icon, label, haptic and `tapTargetFrame()`; migrate the six sites. Where 44pt crowds, enlarge only the hit area.

#### HIG-016 BUIFilterChip is a 26pt target used in every style
- **Severity**: P1. **Platform**: iOS.
- **Location**: `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/FilterChips.swift:39`, called from `apple/Persona/Views/Chats/ChatsView.swift:3205`, `apple/Persona/Views/Inbox/InboxView.swift:740`, `apple/Persona/Views/Knowledge/KnowledgeView.swift:370`, `apple/Persona/Views/Chats/SpaceView.swift:102`; also the PromptBar chip remove at `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:555` (24x24, Beautiful style only).
- **Violates**: [Accessibility][a11y]: iOS default 44, minimum 28.
- **Problem**: the package is designed on desktop sizes. The chip is `26 * typeScale` tall, below the 28 minimum, and its hit shape is the capsule. The calls are not gated by `AppStyle`, so this repeats across many list screens in all styles. Other cited package components (ChatPanel, RecordsTable, SidebarNav) are unused in the app and are not counted.
- **Evidence**: `.frame(... height: 26 * ...)` on the chip; `.frame(width: 24, height: 24).contentShape(chipShape)` on the remove button.
- **Fix**: add an iOS-only minimum hit height (28 at least, ideally 44 with `contentShape(.rect)`) inside the package, keeping the drawn capsule. Keep 28 on macOS.

### P2

#### HIG-017 Search-field Clear buttons are glyph-sized
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Assistant/AssistantSidebar.swift:911`, `apple/Persona/Views/Todos/TodosView.swift:190`, `apple/Persona/Views/NewChat/NewChatView.swift:810`, `apple/Persona/Views/Link/LinkSheet.swift:178`.
- **Violates**: [Accessibility][a11y] and [Buttons][btn]; [Color][color]: avoid tertiary for actionable controls.
- **Problem**: bare icon buttons with no frame (about 17pt). Only the To-Do search clear has no label. The drawer search is a hand-built field (the Beautiful variant already uses a 28pt button). `NewChatView` resets a model ID and LinkSheet's is Close (HIG-014), so they are the same pattern, not search fields.
- **Evidence**: `Button { query = "" } label: { ThemedIcon(Theme.Icon.clear).foregroundStyle(.tertiary) }`.
- **Fix**: shared clear button with `tapTargetFrame()`, secondary instead of tertiary, label "Clear Search".

#### HIG-018 Deny-and-send arrow in the permission reason field is a 24pt circle
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Interactions/InteractionViews.swift:829`, `apple/Persona/Views/Interactions/InteractionViews.swift:832`, `apple/Shared/Design/Theme.swift:493`.
- **Violates**: [Accessibility][a11y] and [Buttons][btn].
- **Problem**: the commit action after "Deny and Explain" is a 24pt icon circle with no extra hit area (labeled). Plain Allow and Deny remain full size. The terminal send buttons at `InteractionViews.swift:1141` use `themedButton` and are fine.
- **Evidence**: `.scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon).background(Theme.accent, in: .circle)`.
- **Fix**: keep the visual, add `tapTargetFrame().contentShape(.circle)` or reuse the composer round button.

#### HIG-019 Icon wells at 28pt used as button frames
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Interactions/InteractionViews.swift:345`, `apple/Persona/Views/Interactions/InteractionViews.swift:359`, `apple/Persona/Views/Interactions/InteractionViews.swift:370`, `apple/Persona/Views/Interactions/DelegateViews.swift:95`, `apple/Persona/Views/Chat/PasteImageChip.swift:73`, `apple/Persona/Views/Assistant/AssistantSidebar.swift:1307`, `apple/Persona/Views/Assistant/AssistantSidebar.swift:1331`, `apple/Shared/Design/Theme.swift:495`.
- **Violates**: [Accessibility][a11y]: 44 default; spacing around controls.
- **Problem**: `wellXS` (28) meets the minimum only at default text size; `scaledFrame` shrinks it at Small. Neighbours sit a few points apart. The sidebar menus are 44 wide by 28 high. Shared with macOS, where 28 is the default, so iOS only.
- **Evidence**: `ThemedIcon(Theme.Icon.more)....scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS).contentShape(.rect)`.
- **Fix**: use `tapTargetFrame()` at the sites; treat `wellXS` as a drawn size.

#### HIG-020 Filter chips and dock capsules are 30-32pt tall
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/Components.swift:288`, `apple/Persona/Views/Assistant/Tasks/TaskViews.swift:1098`, `apple/Persona/Views/Assistant/Tasks/AssistantActionBar.swift:159`, `apple/Persona/Views/Assistant/Tasks/AssistantActionBar.swift:178`, `apple/Persona/Views/Inbox/Lark/LarkConversationRow.swift:38`.
- **Violates**: [Accessibility][a11y].
- **Problem**: `FilterChip` is about 32pt and `DockCapsule` about 30pt, with no minimum height or `contentShape`; they meet the 28 minimum at default size. The dock height is deliberate (its doc comment says 28pt), so a visible 44 would undo the design. `SoftButtonStyle` already enforces 44.
- **Evidence**: `.padding(.vertical, Theme.Space.sm)` around subheadline text, `.buttonStyle(.plain)`.
- **Fix**: extend only the hit area (`frame(minHeight: 44)` plus `contentShape(.capsule)` as an overlay, or a height-only `tapTargetFrame`). If it crowds, record the exception in the design system with 8pt gaps.

#### HIG-021 Lark avatar, name and media taps
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:1613`, `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:1624`, `apple/Persona/Views/Inbox/Lark/LarkTopicCard.swift:80`, `apple/Persona/Views/Inbox/Lark/LarkMessageContent.swift:389`, `apple/Persona/Views/Inbox/Lark/LarkMessageContent.swift:402`, `apple/Persona/Views/Inbox/Lark/LarkMessageContent.swift:502`, `apple/Persona/Views/Inbox/Lark/LarkReactionsMoments.swift:271`, `apple/Persona/Views/Inbox/ConnectorThread/ConnectorThreadView.swift:357`, `apple/Persona/Views/Inbox/Lark/LarkCard.swift:645`.
- **Violates**: [Accessibility][a11y] (28pt minimum control size); [Buttons][btn] (always include a press state for a custom button); [VoiceOver][vo].
- **Problem**: the sender name (about 16pt tall, caption) opens a profile via `onTapGesture` with no trait or press state; the 34pt avatar beside it is labeled. Image messages are labeled with the generic lowercase "images" and no button trait; video, Moments images and link cards also lack traits. The long-press menu also reaches the profile. Connector surface.
- **Evidence**: `Text(m.senderName).font(Theme.Font.label).foregroundStyle(.secondary).onTapGesture { showProfile() }`; `.accessibilityLabel("images")`.
- **Fix**: use `Button`/`.buttonStyle(.plain)` with `contentShape(.rect)` and at least 28pt height for the name, or hide the name from VoiceOver since the avatar exposes the action. Localize labels ("Image 1 of N", "Video, play") and add `.isButton`.

#### HIG-022 Live Activity action buttons about 34pt; lock screen capped at Large
- **Severity**: P2. **Platform**: Live Activity.
- **Location**: `apple/PersonaWidgets/FleetLiveActivity.swift:27`, `apple/PersonaWidgets/FleetLiveActivity.swift:329`, `apple/PersonaWidgets/FleetLiveActivity.swift:349`, `apple/PersonaWidgets/FleetLiveActivity.swift:361`, `apple/PersonaWidgets/FleetLiveActivity.swift:366`, `apple/PersonaWidgets/FleetLiveActivity.swift:290`.
- **Violates**: [Accessibility][a11y] (control size); [Typography][typo]; [Live Activities][la].
- **Problem**: Deny, Allow, Approve and View are bordered capsules around subheadline text with no control size, about 34pt (estimated, not rendered), with 10pt gaps, for permission approvals on a lock screen. Allow and Approve require Face ID, which limits the cost of a mistap. The lock screen forces `dynamicTypeSize(...large)` because the surface is cut at about 160pt, a real system limit. Island cells use fixed `.system(size:)` sizes (12-15pt text; the 10pt and 7pt items are symbols). The widget target cannot use the DesignSystem helpers.
- **Evidence**: `.dynamicTypeSize(...DynamicTypeSize.large)`; `.padding(.vertical, 7)` on the View link; `.buttonStyle(.bordered)`.
- **Fix**: `.controlSize(.large)` on the action row and 12pt vertical padding on View; check the height budget. Raise the cap toward xxxLarge by showing fewer rows at large sizes. Give the 7pt unread glyph a relative size.

#### HIG-023 Root cause: tap-target token is not enforced
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Shared/Design/Theme.swift:491`, `apple/Shared/Design/Theme.swift:507`, `apple/Persona/DesignSystem/TextSize.swift:98`, `apple/Persona/DesignSystem/TextSize.swift:116`, `apple/Persona/DesignSystem/ComposerButtons.swift:75`, `apple/Persona/DesignSystem/QuoteViews.swift:170`, `apple/Persona/Views/Todos/TodoSpaceViews.swift:13`.
- **Violates**: [Accessibility][a11y]: 44 default, 28 minimum.
- **Problem**: drawn-size tokens (20-38) and the 44 token share `scaledFrame`, which scales with text up to 1.6x, so a 44 frame is about 35-36pt at the smallest setting (still above 28). About 36 sites call `scaledFrame(width: Theme.Size.tapTarget, ...)` directly; only `tapTargetFrame()` adds a 44 floor. No lint guards it. This is the cause behind HIG-007 to HIG-020.
- **Evidence**: `func tapTargetFrame(...) { scaledFrame(...).frame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget) }`.
- **Fix**: replace the direct calls with `tapTargetFrame()`, add a CI grep, and document in the design system that icon and well tokens are drawn sizes only.

#### HIG-024 Mac composer thumbnail Remove button is 18pt and hover-only
- **Severity**: P2. **Platform**: macOS (frozen).
- **Location**: `apple/Mac/Chat/MacComposerThumb.swift:21`, `apple/Mac/Chat/MacComposerThumb.swift:31`, `apple/Mac/Chat/MacComposerThumb.swift:39`.
- **Violates**: [Accessibility][a11y]: macOS minimum 20x20 (default 28).
- **Problem**: the badge is `Theme.Size.badge` (18) at zero opacity until hover or keyboard focus. It has a label and a focus path.
- **Evidence**: `.scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge) ... .opacity(shown ? 1 : 0)`.
- **Fix**: wrap the glyph in a 24pt frame with `contentShape(.circle)` applied after it, keeping the 18pt circle visible. Only if the Mac app is touched.

#### HIG-025 visionOS reuses the 44pt tap-target token
- **Severity**: P2. **Platform**: visionOS.
- **Location**: `apple/Shared/Design/Theme.swift:507`, `apple/Persona/DesignSystem/TextSize.swift:116`, `apple/Persona/DesignSystem/ComposerButtons.swift:75`, `apple/Persona/DesignSystem/ViewerChrome.swift:14`, `apple/Persona/DesignSystem/Styling.swift:170`.
- **Violates**: [Buttons][btn]: visionOS hit region at least 60x60 pt.
- **Problem**: `tapTarget` is a constant 44 with no visionOS branch and `apple/Vision` overrides nothing. `docs/apple/vision.md` says placement still needs verification.
- **Evidence**: `static let tapTarget: CGFloat = 44`.
- **Fix**: make the token 60 under `#if os(visionOS)` and check dependent layouts. Document it in `docs/apple/vision.md`.

#### HIG-026 Composer attachment chip and menu rows use fixed heights
- **Severity**: P2. **Platform**: iOS (Beautiful style).
- **Location**: `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:566`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:580`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:666`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:714`.
- **Violates**: [Typography][typo]: verify layouts at all sizes, minimize truncation.
- **Problem**: text scales with `@ScaledMetric` while the chip (26), mention and command rows (36) and model rows (30) have fixed heights. At accessibility sizes text overflows and overlaps neighbours (it is not clipped). The highlight offset still matches the rows, but no longer encloses the text. The menus and chip appear on demand; the typing field grows correctly. The "Thinking" 28pt claim could not be located.
- **Evidence**: `.frame(height: 26)`; `.frame(height: 36).offset(y: CGFloat(row) * 36)`.
- **Fix**: scaled `minHeight` via `@ScaledMetric`, and draw the highlight as the row's background.

#### HIG-027 One-line labels shrink to 0.7 where text should wrap
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Todos/TodoStageBar.swift:26`, `apple/Persona/Views/Assistant/OnboardingChecklist.swift:125`, `apple/Persona/Views/Settings/LiveRepliesSection.swift:17`, `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:842`.
- **Violates**: [Typography][typo]: minimize truncation; stack at large sizes.
- **Problem**: about 95 `minimumScaleFactor` uses. The stage bar draws six stage names at 0.7 in one sixth of the width each; the checklist sentence is one line at 0.8. Scaling happens only on overflow (likely at large sizes or long translations), where it can pass below 11pt. Shrinking a numeric cell (`UsageCharts.swift`) is fine.
- **Evidence**: `.lineLimit(1).minimumScaleFactor(0.7)` on `Theme.Font.caption`.
- **Fix**: let sentences wrap; show only the current stage (or stack) when `isAccessibilitySize`; limit shrink to about 0.85.

#### HIG-028 Fixed-size text below 11pt ignores Dynamic Type
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Interactions/InteractionViews.swift:1176`, `apple/Persona/Views/Feishu/Docs/FeishuDocReader.swift:434`.
- **Violates**: [Typography][typo]: use text styles; [Accessibility][a11y]: 11pt iOS minimum.
- **Problem**: the plan-step digit and the Feishu comment count are 9pt bold fixed-size text inside circles that grow with `scaledFrame`. Symbol glyphs at fixed sizes (8pt check, 7pt unread, sidebar icons) are acceptable and are not counted. The `type-scale: glyph` comment convention should cover symbols only.
- **Evidence**: `Text("\(i + 1)").font(.system(size: 9, weight: .bold).monospacedDigit())`.
- **Fix**: use a `Theme.Font` text style; cap with `dynamicTypeSize` if the badge must stay compact.

#### HIG-029 Text containers scale at most 1.6x while text grows to about 3x
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/TextSize.swift:98`, `apple/Persona/DesignSystem/TextSize.swift:161`, `apple/Persona/Views/Todos/Calendar/TodoCalendarGrids.swift:200`, `apple/Shared/Design/Theme.swift:497`.
- **Violates**: [Typography][typo]: verify all sizes including accessibility sizes.
- **Problem**: the week-strip day digit is body text in a 32pt `scaledFrame` circle. At the largest accessibility size the digit (about 53pt) overflows the circle (about 51pt). The cap itself is deliberate. `apple/Persona/Views/Session/GitPane.swift:235` already caps its font and is not a defect.
- **Evidence**: `sizeScale = min(textScale(size), 1.6)`.
- **Fix**: use a minimum-size frame for text containers, or cap the digit with `dynamicTypeSize(...accessibility2)`.

#### HIG-030 Shell top bar and viewer chrome Dynamic Type caps
- **Severity**: P2. **Platform**: iOS, iPadOS.
- **Location**: `apple/Shared/Design/Theme.swift:865`, `apple/Shared/Design/Theme.swift:870`, `apple/Persona/DesignSystem/ShellComponents.swift:377`, `apple/Persona/DesignSystem/ShellComponents.swift:163`, `apple/Persona/DesignSystem/ViewerChrome.swift:34`, `apple/Persona/Views/Chat/ImageViewer.swift:217`, `apple/Persona/Views/Inbox/Lark/LarkConversationsView.swift:289`.
- **Violates**: [Typography][typo] and [Accessibility][a11y]: support enlargement of at least 200%.
- **Problem**: bars cap at xxxLarge (about 1.35x), and the compact switch fallback caps at Large, so bar text stops short of accessibility sizes. The caps are documented and tested; the Large cap is only the second branch of a `ViewThatFits`. Viewer bars are mostly icon-only. Debatable, since the chat content itself scales.
- **Evidence**: `static let topBarLargestTypeSize: DynamicTypeSize = .xxxLarge`.
- **Fix**: keep the caps, add an accessibility-size branch for the switch (single title plus menu), and document the caps in the design system.

#### HIG-031 No Increase Contrast, Reduce Transparency or Differentiate Without Color handling
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Shared/StatusPalette.swift:44`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Tokens/BUIColors.swift:104`, `apple/Shared/Design/Theme.swift:604`.
- **Violates**: [Accessibility][a11y]: provide a higher-contrast scheme when Increase Contrast is on if the palette misses the ratios; [Dark Mode][dark]: test with Increase Contrast.
- **Problem**: a repo grep finds no read of the contrast, transparency or differentiate-without-color settings. Custom colors resolve only on light or dark. System materials adapt by themselves. No token was measured against the ratios, so this is conditional: it rises to P1 if primary text is found under 4.5:1 (see HIG-010, 032, 033).
- **Evidence**: `self.init(uiColor: UIColor { $0.userInterfaceStyle == .dark ? UIColor(hex: dark) : UIColor(hex: light) })`.
- **Fix**: extend the light/dark color initializers to read `accessibilityContrast`, add high-contrast values for secondary text, status tints and hairlines, and add a case to `StyleSpecTests`.

#### HIG-032 Secondary text floor of 3:1 is under 4.5:1
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/BeautifulUIBridge.swift:10`, `apple/Persona/Views/BeautifulUIBridge.swift:22`, `apple/PersonaTests/StyleSpecTests.swift:70`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:476`.
- **Violates**: [Accessibility][a11y]: 4.5:1 for text up to 17pt.
- **Problem**: the library's secondary and tertiary inks are the system secondary label (about 3.4:1 on white, 3.1:1 on grouped gray), and the project states a 3:1 floor. Footnote and caption text at 12-13pt sits under 4.5:1 on light backgrounds. System components do the same, so this is arguably Apple-consistent. No test covers secondary text.
- **Evidence**: `ink2: secondary, ink3: secondary`.
- **Fix**: add a text-secondary token at 4.5:1 on page, card and grouped backgrounds; test it; keep system secondary for icons.

#### HIG-033 White labels on the accent only guaranteed at 3.5:1
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Assistant/AssistantSidebar.swift:1508`, `apple/Persona/Views/Inbox/InboxView.swift:1011`, `apple/Persona/Views/Chats/NodeActions.swift:828`, `apple/Shared/Design/PersonaPalette.swift:26`, `apple/PersonaTests/StyleSpecTests.swift:60`, `apple/Persona/Views/Settings/StorageView.swift:280`.
- **Violates**: [Accessibility][a11y]: 3:1 applies to bold or 18pt text; semibold is not bold.
- **Problem**: 15-17pt semibold white on the accent. Computed ratios: system blue 4.0 (light) and 3.65 (dark); Claude style 3.6; Lark dark 3.35 (a deliberate test exemption). The default Glass accent is the same fill Apple uses under white on prominent buttons. A darker blue exists (4.7:1) but only the Live Activity uses it. White glyph tiles on gray (3.3:1) repeat the pattern. Ratios were computed, not measured.
- **Evidence**: `Label(...).font(Theme.Font.bodyStrong).foregroundStyle(.white) ... .background(Theme.accent, in: .capsule)`.
- **Fix**: add a solid-accent token that reaches 4.5:1 for fills behind white labels; raise the `onAccent` target and its test; drop or fix the Lark exemption.

#### HIG-034 Terminal hint and chip text uses 55-75% foreground opacity
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Session/SessionPanes.swift:569`, `apple/Persona/Views/Session/SessionPanes.swift:450`, `apple/Persona/Views/Session/SessionPanes.swift:513`, `apple/Persona/Views/Session/SessionPanes.swift:540`, `apple/Persona/Views/Session/SessionPanes.swift:586`, `apple/Persona/Views/Session/SessionPanes.swift:603`, `apple/Persona/Views/Session/SessionPanes.swift:636`, `apple/Persona/Views/Session/Terminal/TerminalTheme.swift:37`.
- **Violates**: [Accessibility][a11y]: 4.5:1 for small text.
- **Problem**: recomputed ratios fail only for the Solarized themes (1.9-3.8:1 depending on opacity) and the Persona light theme at 0.55 (3.6:1); Ink and Persona dark pass. The terminal output is fine; hints and key chips are affected. Solarized is opt-in.
- **Evidence**: `.foregroundStyle(Color(hex: side.foreground).opacity(0.55))`.
- **Fix**: add a muted color per terminal theme side that reaches 4.5:1, test every theme, replace the opacity uses.

#### HIG-035 Workspace sidebar palette bypasses tested tokens
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Shared/Design/Theme.swift:61`, `apple/Shared/Design/Theme.swift:63`, `apple/Shared/Design/Theme.swift:66`, `apple/Shared/Design/Theme.swift:67`, `apple/Persona/Views/Chats/RepoTreeRows.swift:42`, `apple/Persona/Views/Chats/RepoTreeRows.swift:194`, `apple/Persona/Views/Chats/RepoTreeRows.swift:206`, `apple/Persona/Views/Chats/RepoTreeRows.swift:310`.
- **Violates**: [Accessibility][a11y] (4.5:1 text, about 3:1 graphics); [Color][color]: prefer semantic colors.
- **Problem**: in light mode unread orange is 1.98:1, success green 2.27:1 and muted meta text 4.35:1 on the sidebar background. Icons sit beside text and the row has an accessibility value, which mitigates the icons; branch and cwd text is slightly under.
- **Evidence**: `static let muted = Color(light: 0x737373, dark: 0xA3A3A3); static let unread = Color(light: 0xFF9800, dark: 0xFFB020)`.
- **Fix**: map unread and success to `Theme.cautionText` and `Theme.Role.positiveColor`; darken muted or alias the shared secondary token.

#### HIG-036 Idle and offline dots and the faded Ultracode age line
- **Severity**: P2. **Platform**: iOS, widgets.
- **Location**: `apple/Persona/Views/Chat/UltracodeViews.swift:171`, `apple/Persona/Views/Chat/UltracodeViews.swift:173`, `apple/Shared/StatusPalette.swift:33`, `apple/Shared/StatusPalette.swift:35`.
- **Violates**: [Accessibility][a11y]: 4.5:1 for small text.
- **Problem**: the quota age line stacks `.secondary` and 0.6 opacity (about 2:1) always, not only when stale. The idle (0.55) and offline (0.3) dots are far under 3:1, but every use found sits beside a label or hides the dot, so they are decorative markers, not a color-only cue.
- **Evidence**: `static let offline = StatusTone(dot: Color.secondary.opacity(0.3), ...)`.
- **Fix**: drop the extra opacity on the age text and add a "stale" word; use solid grays at 3:1 for the dots.

#### HIG-037 App-specific Light and Dark setting
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/ColorSchemeChoice.swift:11`, `apple/Persona/DesignSystem/ColorSchemeChoice.swift:49`, `apple/Persona/App/AppModel.swift:2734`, `apple/Persona/Views/Settings/AppearanceSettingsView.swift:140`.
- **Violates**: [Dark Mode][dark]: avoid an app-specific appearance setting.
- **Problem**: users can force Light or Dark. Default is Match System, and the override is applied on windows, the correct way. Widgets, Live Activity and the watch keep the system look, so they can disagree with the app. A conscious deviation.
- **Evidence**: `for w in scene.windows where w.overrideUserInterfaceStyle != interfaceStyle { w.overrideUserInterfaceStyle = interfaceStyle }`.
- **Fix**: keep it, record it as an accepted deviation, and add a footer saying extensions follow the system.

#### HIG-038 Shell composer, drawer and segment colors are fixed hex pairs
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Shared/Design/Theme.swift:842`, `apple/Shared/Design/Theme.swift:845`, `apple/Shared/Design/Theme.swift:850`, `apple/Persona/DesignSystem/ShellComposerBar.swift:476`, `apple/Persona/Views/Shell/ShellView.swift:26`.
- **Violates**: [Dark Mode][dark]: avoid hard-coded colors; use system backgrounds for elevated levels.
- **Problem**: the ChatGPT-style neutral palette does not use base or elevated system levels, so a composer in a sheet looks the same in both. Both appearances are readable. The Google sign-in button colors (`apple/Persona/Views/Mail/MailViews.swift:561`) and the dark-forced image editor (`apple/Persona/Views/Chat/ImageEditor.swift:72`) are brand and content exceptions. The watch card fill is HIG-082.
- **Evidence**: `Theme.Shell.composer/drawer/segment` are fixed light/dark hex pairs.
- **Fix**: keep as a documented exception, or build from `systemBackground`, `secondarySystemBackground` and `tertiarySystemFill`.

#### HIG-039 No Reduce Transparency fallback on custom blends
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Assistant/AssistantTopBar.swift:649`, `apple/Persona/Views/Assistant/AssistantTopBar.swift:651`, `apple/Persona/Views/NewChat/NewChatView.swift:430`.
- **Violates**: [Dark Mode][dark]: test with Reduce Transparency and Increase Contrast.
- **Problem**: nothing reads `accessibilityReduceTransparency`. System glass and materials adapt. The only custom blend is the top fade (material plus 0.82 page-color wash), which carries no text itself. A "verify on device" item.
- **Evidence**: `Rectangle().fill(.regularMaterial).mask(Self.easedMask(from: solid, over: fade / 2))`.
- **Fix**: manual pass with both settings on; if text sits behind the fade, render a solid page background when reduced.

#### HIG-040 Needs-you dot is color only under Reduce Motion
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/Components.swift:20`, `apple/Persona/DesignSystem/Components.swift:57`, `apple/Persona/Views/Chats/RepoTreeRows.swift:44`, `apple/Persona/Views/Chats/RepoTreeRows.swift:222`, `apple/Persona/Views/Chats/RepoTreeRows.swift:311`.
- **Violates**: [Accessibility][a11y]: more than color alone; [Motion][motion]: motion must not be the only carrier.
- **Problem**: with Reduce Motion on, a needs-you dot is a still circle, the same shape as finished and seen dots. VoiceOver is fine. Chat rows add a "Needs You" chip, so the gap is the repo sidebar and other dot-only surfaces.
- **Evidence**: `state == .needsYou && !reduceMotion` decides the breathing ring.
- **Fix**: in `StatusDot`, draw a static ring or glyph for needs-you when Reduce Motion or differentiate-without-color is on.

#### HIG-041 No VoiceOver focus management when inline editors appear
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Interactions/InteractionViews.swift:545`, `apple/Persona/Views/Interactions/InteractionViews.swift:1113`.
- **Violates**: [VoiceOver][vo]: notify of layout changes.
- **Problem**: tapping "Other answer" swaps in a text field and sets keyboard focus; no layout-changed notification or accessibility focus. Keyboard focus often moves the cursor anyway. The larger problem is the missing button trait (HIG-002). The attach-sheet scrim is already labeled.
- **Evidence**: `withAnimation(...) { writing = true }; otherFocused = true`.
- **Fix**: after HIG-002, post layout-changed or use an accessibility focus state on the field. Check on a device.

#### HIG-042 Busy buttons swap their label for a bare ProgressView
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/Components.swift:559`, `apple/Persona/DesignSystem/Components.swift:677`, `apple/Persona/Views/Interactions/DelegateViews.swift:244`, `apple/Persona/Views/Inbox/InboxActions.swift:367`.
- **Violates**: [VoiceOver][vo]; [Buttons][btn]: show an indicator alongside a label for non-instant actions.
- **Problem**: after activation VoiceOver hears "In progress" instead of the action name and sighted users lose the word on Delete or Stop and Close. Short-lived, and the button is disabled. Two other sites keep the text (`apple/Persona/Views/Assistant/Tasks/BriefCard.swift:291`).
- **Evidence**: `if deleting { ProgressView().tint(Theme.onSolid) } else { Text("Delete") }`.
- **Fix**: shared busy-label helper that keeps the text (hidden or beside the spinner) with the action as the accessibility label.

#### HIG-043 Reduce Motion is not centralized in Theme.Motion
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Shared/Design/Theme.swift:609`, `apple/Shared/Design/Theme.swift:612`, `apple/Shared/Design/Theme.swift:639`, `apple/Persona/Views/Chat/ChatView.swift:3738`.
- **Violates**: [Accessibility][a11y] (Reduce Motion), [Motion][motion].
- **Problem**: `Theme.Motion` is plain animation constants; the helper `reduced` is read in four files, about 25 files read the environment by hand, and several large views (`ChatsView`, `SessionPanes`, `InboxView`) have none. Repeating motion is mostly guarded; the unguarded sites are short snappy animations. The structural risk is that new calls are unprotected by default, and the comment at line 609 overclaims.
- **Evidence**: `static let state = Animation.snappy(duration: 0.25)`.
- **Fix**: add a view modifier and `withMotion` helper that returns nil or a fade when reduced; migrate large-movement sites first; correct the comment.

#### HIG-044 Composer send/mic swap keeps scale and blur under Reduce Motion
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/ShellComposerBar.swift:215`, `apple/Persona/DesignSystem/ShellComposerBar.swift:221`, `apple/Persona/DesignSystem/ShellComposerBar.swift:421`, `apple/Persona/DesignSystem/ComposerButtons.swift:39`, `apple/Persona/DesignSystem/ComposerButtons.swift:155`, `apple/Persona/DesignSystem/ComposerButtons.swift:164`, `apple/Persona/DesignSystem/ComposerButtons.swift:188`.
- **Violates**: [Accessibility][a11y]: reduce zooming and scaling, replace x/y/z transitions with fades, avoid animating into and out of blurs; [Motion][motion].
- **Problem**: under Reduce Motion only the curve changes; the transition still grows from near zero while clearing a 4pt blur, the press shrinks to 0.75 and the mic pulse symbol effect is ungated. The non-telegram branch also uses scale plus opacity. Brief but on every message.
- **Evidence**: `reduceMotion ? Theme.Motion.state : Theme.Motion.composerSwap` changes only the curve.
- **Fix**: use `.opacity` in both branches when reduced; skip the press scale; gate the pulse with `isActive: listening && !reduceMotion`.

#### HIG-045 Message lift menu and attach bar animate scale and blur without a fallback
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Chat/MessageLift.swift:94`, `apple/Persona/Views/Chat/MessageLift.swift:119`, `apple/Persona/Views/Chat/MessageLift.swift:160`, `apple/Persona/Views/Chat/MessageMenu.swift:305`, `apple/Persona/Views/Chat/MessageMenu.swift:393`, `apple/Persona/Views/Chat/AttachSheet.swift:360`, `apple/Persona/Views/Chat/AttachSheet.swift:481`.
- **Violates**: [Accessibility][a11y] (Reduce Motion); [Motion][motion].
- **Problem**: long-press blurs the page, the bar scales in from 0.85 and the drag-back spring bounces. `reduceMotion` is declared at `MessageLift.swift:119` but never read. The attach bar-away state scales and blurs by 10.
- **Evidence**: `.transition(.scale(scale: 0.85, anchor: p.below ? .top : .bottom).combined(with: .opacity))`.
- **Fix**: use the declared flag: opacity transition, bounce-0 spring, no blur or scale when reduced.

#### HIG-046 BeautifulUI loops run without a Reduce Motion check
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/AgentScreen.swift:192`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/AgentScreen.swift:262`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/RecordsTable.swift:540`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:811`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Support/SpinningArc.swift:244`.
- **Violates**: [Accessibility][a11y]: reduce automatic and repetitive animation.
- **Problem**: recording pulse, connecting spinner and a "calculating" dot repeat forever. AgentScreen and RecordsTable have no app caller, so they are latent. The live one is the dictation level meter, a 30 fps timeline (small, only while dictating, carries level information). Spinner, shimmer and others in the package are correctly guarded.
- **Evidence**: `.onAppear { withAnimation(.easeInOut(duration: 0.55).repeatForever()) { pulse = true } }`.
- **Fix**: read the environment; use a static dot or arc when set; pause the level meter timeline. Fix the meter first.

#### HIG-047 Pager and drawer slide with springs regardless of Reduce Motion
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Shell/ShellPager.swift:122`, `apple/Persona/Views/Shell/ShellPager.swift:129`, `apple/Persona/Views/Shell/ShellPager.swift:166`, `apple/Persona/Views/Assistant/AssistantSidebar.swift:99`, `apple/Persona/Views/Assistant/AssistantSidebar.swift:108`, `apple/Persona/Views/SplitNavigation.swift:80`.
- **Violates**: [Accessibility][a11y]: replace x/y slides with fades.
- **Problem**: drag release is fine, but button and tab triggers use the same spring and slide across the screen. The spring has no bounce, lasts 0.32 s and has no parallax, so low severity.
- **Evidence**: `withAnimation(motion, completionCriteria: .removed) { position = target }`.
- **Fix**: when reduced and velocity is zero, set the target without animation or use a short fade; let the drawer animation helper return a short ease when reduced.

#### HIG-048 Scroll jumps and header collapse animate without a Reduce Motion path
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Chat/ChatView.swift:1932`, `apple/Persona/Views/Chat/ChatView.swift:1969`, `apple/Persona/Views/Chat/ChatView.swift:2014`, `apple/Persona/Views/Chat/ChatView.swift:1790`, `apple/Persona/Views/Chat/ChatView.swift:2639`.
- **Violates**: [Accessibility][a11y]; [Motion][motion].
- **Problem**: jump-to-message and jump-to-newest use a 0.25 s animated scroll; the header collapse uses layout animation (dev style only except the snap at 2639). Short and user-initiated, a weak case; `ChatPin` already treats reduced motion as a jump.
- **Evidence**: `withAnimation(Theme.Motion.standard) { position.scrollTo(id: id, anchor: .center) }`.
- **Fix**: use the helper from HIG-043 at these seven sites.

#### HIG-049 Toasts slide in from the top
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/Toasts.swift:72`, `apple/Persona/DesignSystem/Toasts.swift:74`, `apple/Persona/DesignSystem/Toasts.swift:87`, `apple/Persona/Views/Shell/ShellStatus.swift:238`.
- **Violates**: [Accessibility][a11y]: fade instead of x/y transitions.
- **Problem**: `move(edge: .top)` plus opacity with no Reduce Motion read. Brief, small element.
- **Evidence**: `.transition(.move(edge: .top).combined(with: .opacity))`.
- **Fix**: `reduceMotion ? .opacity : ...`; same for the pill scale at `ShellStatus.swift:238`.

#### HIG-050 Beautiful-style composer popovers
- **Severity**: P2. **Platform**: iOS (opt-in style).
- **Location**: `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:286`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:291`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:530`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:666`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:714`, `apple/Persona/Views/Chat/PromptComposer.swift:45`, `apple/Persona/Views/Chat/ChatView.swift:4354`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Support/Glyphs.swift:56`.
- **Violates**: [Accessibility][a11y] (control size, Reduce Motion); [VoiceOver][vo] (layout changes).
- **Problem**: the model, mention and command menus are hand-built popovers with 30-36pt rows, scale-in transitions with no Reduce Motion gate, and no layout-changed announcement or escape action. Controls are 34pt versus the 44pt default composer buttons, and the glyph icons are fixed size. Rows and buttons are labeled. Only the opt-in Beautiful style is affected.
- **Evidence**: `if model.isModelMenuOpen { modelMenu...transition(.scale(scale: 0.95, anchor: .bottomLeading).combined(with: .opacity)) }`.
- **Fix**: 44pt hit areas for rows, opacity-only when reduced, layout-changed on open, an escape action, `@ScaledMetric` icon sizes. Did not verify the scrim claim.

#### HIG-051 BUISegmented has 24pt segments and no picker semantics
- **Severity**: P2. **Platform**: iOS (Beautiful style).
- **Location**: `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/FilterChips.swift:93`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/FilterChips.swift:86`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/FilterChips.swift:106`, `apple/Persona/DesignSystem/Components.swift:374`, `apple/Persona/Views/Settings/AppearanceSettingsView.swift:140`.
- **Violates**: [Accessibility][a11y]; [VoiceOver][vo].
- **Problem**: under the Beautiful style the segmented choice replaces the system picker with a control whose segments are 24pt, whose pill animation is not gated, and which exposes only `isSelected` (no label, position or adjustable role). The label text does scale (11.5pt); only the hit area stays fixed.
- **Evidence**: `.frame(maxWidth: fills ? .infinity : nil, minHeight: 24 * max(1, theme.typeScale * 0.9))`.
- **Fix**: segment height at least 28 (44 via hit shape), container label and value or a picker representation, gate the animation.

#### HIG-052 BUIIcon draws its own glyphs and ignores Dynamic Type
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Packages/BeautifulUI/Sources/BeautifulUI/Support/Glyphs.swift:3`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Support/Glyphs.swift:62`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/PromptBar.swift:497`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/SidebarPieces.swift:114`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/CodeBlock.swift:189`, `apple/Persona/DesignSystem/Components.swift:120`.
- **Violates**: [Icons][icons]: prefer familiar visual metaphors; [SF Symbols][sym]: symbols match the weight of adjacent text; [Typography][typo]: make sure interface icons are easy to view at larger font sizes.
- **Problem**: about 68 call sites draw path glyphs (check, close, plus, search, copy, mic) that all exist as SF Symbols, in a fixed frame, so they ignore Dynamic Type, Bold Text and weight, and bypass `Theme.Icon` and `IconVocabularyTests`. Deliberate (matches the web design), hidden from VoiceOver.
- **Evidence**: `.frame(width: size, height: size).accessibilityHidden(true)`.
- **Fix**: at least `@ScaledMetric`; longer term render common glyphs with `Image(systemName:)` registered in `apple/Shared/Design/Icons.swift`.

#### HIG-053 Custom Assistant and Agents switch replaces the tab bar on iPhone
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/ShellComponents.swift:19`, `apple/Persona/DesignSystem/ShellComponents.swift:73`, `apple/Persona/DesignSystem/ShellComponents.swift:130`, `apple/Persona/DesignSystem/ShellComponents.swift:165`, `apple/Shared/Design/Theme.swift:860`, `apple/Persona/Views/RootView.swift:73`.
- **Violates**: [Accessibility][a11y] (44 default); [VoiceOver][vo]; [Tab bars][tab].
- **Problem**: top-level navigation on iPhone (when the shell is on) is a custom two-segment capsule 72x36pt with only `isSelected` (no container or "1 of 2" semantics). Tapping the selected segment opens a menu, with only a "Chat menu" hint. Swipe paging and the top position are mitigations or debatable. The system `TabView` serves iPad and the shell-off path.
- **Evidence**: `.scaledFrame(width: Theme.Shell.segmentWidth, height: Theme.Shell.switchHeight - Theme.Space.s)`.
- **Fix**: container grouping and per-segment position labels, a 44pt hit area on the 36pt pill, clearer hints; record it as a deliberate deviation.

#### HIG-054 Top fade built from material instead of scroll edge effects
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Assistant/AssistantTopBar.swift:640`, `apple/Persona/Views/Assistant/AssistantTopBar.swift:649`, `apple/Persona/Views/Assistant/AssistantView.swift:160`, `apple/Persona/Views/Chat/ChatView.swift:3118`, `apple/Persona/Views/Chats/ChatsView.swift:3977`.
- **Violates**: [Materials][mat] and [Layout](https://developer.apple.com/design/human-interface-guidelines/layout): use a scroll edge effect instead of a semi-opaque background under floating controls.
- **Problem**: two mechanisms coexist; chat uses a hand-made material fade while `ChatsView` uses the system edge effect style. Cards and pills on materials in the content layer are compliant.
- **Evidence**: `Rectangle().fill(.regularMaterial).mask(...)`.
- **Fix**: move to the system soft top edge effect, or document the fog with a solid reduced-transparency fallback.

#### HIG-055 Symbol vocabulary leaks in unscanned targets
- **Severity**: P2. **Platform**: macOS helper, widgets.
- **Location**: `apple/PersonaLink/Sources/PersonaLink/PairView.swift:145`, `apple/PersonaLink/Sources/PersonaLink/PairView.swift:226`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:163`, `apple/PersonaWidgets/PersonaControls.swift:13`, `apple/PersonaWidgets/FleetLiveActivity.swift:409`, `apple/PersonaTests/IconVocabularyTests.swift:38`.
- **Violates**: [Icons][icons]: use consistent standard symbols (the one-meaning rule is a project rule, see `docs/apple/icons.md`).
- **Problem**: the test scans only three source folders; the Mac helper uses a retired failure symbol, and widgets use raw strings that duplicate `Theme.Icon` entries. PersonaLink is a separate package and may not see `Icons.swift`.
- **Evidence**: `Image(systemName: result.ok ? "checkmark.circle.fill" : "xmark.octagon.fill")`.
- **Fix**: use the app tokens in widgets (Shared is compiled in); extend the test to those targets; give the package a small local copy with a comparison test.

#### HIG-056 Hidden custom spinner and overloaded trash symbol
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Packages/BeautifulUI/Sources/BeautifulUI/Support/Reveal.swift:67`, `apple/Persona/DesignSystem/Components.swift:111`, `apple/Persona/DesignSystem/Components.swift:122`, `apple/Persona/Views/Todos/TodoDetailView.swift:445`, `apple/Persona/Views/Settings/ExtensionSettingsView.swift:225`, `apple/Persona/Views/Chat/MessageViews.swift:331`, `apple/Shared/Design/Icons.swift:109`.
- **Violates**: [VoiceOver][vo] (do not hide meaningful state); [Icons][icons].
- **Problem**: in the Beautiful style the working badge uses an accessibility-hidden spinner and the Needs You and Unread badges are hidden too, so state may be silent in that list unless a row-level label exists (not found). Beautiful is opt-in; the default style is glass (`apple/Shared/Design/ColorSpec.swift:164`), so P2 stands. The trash symbol also stands for Remove, Uninstall and Don't Send, not only deletion of user data.
- **Evidence**: `SpinningArc(...).frame(width: size, height: size).accessibilityHidden(true)`.
- **Fix**: label the whole badge with the state; add distinct uninstall and discard symbols.

#### HIG-057 Number-pad fields cannot be dismissed
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Mail/MailViews.swift:635`, `apple/Persona/Views/Settings/ProviderWizardView.swift:782`, `apple/Persona/Views/Settings/SSHTunnelView.swift:317`, `apple/Persona/Views/Settings/LocalProxyView.swift:203`, `apple/Persona/Views/Settings/ExtensionsViews.swift:609`, `apple/Persona/Views/Settings/ExtensionComponents.swift:538`, `apple/Persona/Views/Settings/ConnectorSignInViews.swift:575`, `apple/Packages/BeautifulUI/Sources/BeautifulUI/Components/FineTuneCard.swift:266`.
- **Violates**: [Entering data][enter]: general guidance on making entry easy; the page does not mention number-pad Done keys, so this is polish.
- **Problem**: number and decimal pads have no Return key and no keyboard toolbar with Done exists anywhere. The mail form's Connect button is in the nav bar, so the keyboard does not hide it, and Form scrolling dismisses by default.
- **Evidence**: `TextField(String(localized: "IMAP port"), text: $form.imapPort).keyboardType(.numberPad)`.
- **Fix**: one shared modifier that adds a keyboard toolbar Done button.

#### HIG-058 Remove and Not Mine on a pending computer are immediate
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Machines/MachinesView.swift:663`, `apple/Persona/Views/Machines/MachinesView.swift:667`, `apple/Persona/Views/Machines/MachinesView.swift:682`, `apple/Persona/Views/Machines/AddComputerView.swift:354`, `apple/Persona/Views/Machines/AddComputerView.swift:720`.
- **Violates**: [Undo and redo][undo], [Design principles][dp].
- **Problem**: both buttons reject the machine at once and show a toast without Undo, next to the prominent "It's Mine". The target is not yet accepted, so no user data is lost, only a re-pair; "Not Mine" is a security rejection where friction is unwanted ([Alerts][alerts]: avoid alerts for common undoable actions).
- **Evidence**: `Button(role: .destructive) { Task { await run(confirm: false) } } label: { Text("Remove") ... }`.
- **Fix**: prefer an Undo toast if the hub can re-add; otherwise keep it immediate.

#### HIG-059 Alert and confirmationDialog are mixed for the same decision
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/DesignSystem/Components.swift:612`, `apple/Persona/Views/Todos/TodoDetailView.swift:85`, `apple/Persona/Views/Session/FilesPane.swift:130`, `apple/Persona/Views/Chat/Rewind.swift:163`, `apple/Persona/Views/Settings/SSHTunnelView.swift:182`, `apple/Persona/Views/Settings/AssistantProactiveViews.swift:599`, `apple/Persona/Views/Settings/SavedPlacesView.swift:209`, `apple/Persona/Views/Settings/ManagersView.swift:181`.
- **Violates**: [Action sheets](https://developer.apple.com/design/human-interface-guidelines/action-sheets), [Design principles][dp] (consistency). Alerts with Cancel are allowed by the HIG, so the issue is project consistency.
- **Problem**: about 55 confirmation dialogs, but delete chat, delete to-do, trash and rewind use `.alert`. Risk acknowledgements like "Allow Everything" are a fair use of an alert.
- **Evidence**: `content.alert("Delete This Chat?", isPresented: ..., Button("Delete", role: .destructive)`.
- **Fix**: write the rule in the design system (dialog for user-initiated destructive actions, alert for blocking notices and input) and migrate the alerts.

#### HIG-060 Row actions are swipe or long-press only on iOS
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Chats/ChatsView.swift:2570`, `apple/Persona/Views/Chats/ChatsView.swift:2597`, `apple/Persona/Views/Settings/AssistantProactiveViews.swift:1063`, `apple/Persona/Views/Settings/AssistantProactiveViews.swift:1066`, `apple/Persona/Views/Assistant/AssistantSidebar.swift:1396`.
- **Violates**: [Gestures][gest] and [Context menus](https://developer.apple.com/design/human-interface-guidelines/context-menus): every action also exists elsewhere.
- **Problem**: Agents rows deliberately have no swipe, so Pin, Interrupt, Close and Mark as Seen live only in a long-press menu. Memory entries delete only by swipe on iOS (the menu copy is macOS only; a Delete in the detail view was not found). Swipe actions reach VoiceOver and Switch Control as custom actions, which limits the harm. Drawer Archive has swipe plus menu.
- **Evidence**: `#if os(macOS)` around the memory context menu.
- **Fix**: drop the macOS guard on the memory menu and add Delete to its detail; add Pin and Mark as Seen to the chat's More menu; add named accessibility actions on rows.

#### HIG-061 Image viewer disables system dismiss, shows a back chevron as Close, no escape action
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Chat/ImageViewer.swift:108`, `apple/Persona/Views/Chat/ImageViewer.swift:131`, `apple/Persona/Views/Chat/ImageViewer.swift:222`, `apple/Persona/DesignSystem/ViewerChrome.swift:20`.
- **Violates**: [Toolbars][tb]: standard Back and Close symbols; [Accessibility][a11y]: gesture alternatives.
- **Problem**: the custom pull-down covers swipe dismissal and Close is labeled, but a modal dismiss looks like a push back chevron (a deliberate Photos-style choice) and no VoiceOver escape action is wired.
- **Evidence**: `Button { dismiss() } label: { ViewerIcon(symbol: Theme.Icon.back) }.accessibilityLabel("Close")`.
- **Fix**: add `accessibilityAction(.escape)`; use `Theme.Icon.close` when modal, or record the exception in `docs/apple/ios.md`.

#### HIG-062 Multi-detent sheets have no grabber
- **Severity**: P2. **Platform**: iOS, iPadOS.
- **Location**: `apple/Persona/Views/Inbox/InboxActions.swift:160`, `apple/Persona/Views/Todos/TodoHandoffSheet.swift:111`, `apple/Persona/Views/Todos/TodoLinkAgentSheet.swift:104`, `apple/Persona/Views/Chats/NodeActions.swift:449`.
- **Violates**: [Sheets][sheets]: include a grabber on resizable sheets.
- **Problem**: of 158 sheets, 43 set detents and 18 show the drag indicator, so resizable sheets look fixed. Done and Cancel placement is already correct (toolbar confirmation and cancellation items).
- **Evidence**: `.presentationDetents([.medium, .large])` with no drag indicator.
- **Fix**: a shared detents modifier that shows the indicator whenever more than one detent is passed.

#### HIG-063 Agents chat replaces the system navigation bar with a custom header
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Chat/ChatView.swift:290`, `apple/Persona/Views/Chat/ChatView.swift:292`, `apple/Persona/Views/Chat/ChatView.swift:2664`, `apple/Persona/Views/Chat/ChatView.swift:3220`, `apple/Persona/Views/SplitNavigation.swift:202`.
- **Violates**: [Toolbars][tb]: use the standard Back button.
- **Problem**: on iPhone the bar is hidden for a hand-built header; both custom back buttons are labeled and sized. A documented choice and a weak deviation. Swipe-back and the title's header trait were not checked on a device.
- **Evidence**: `.toolbarVisibility(sideBars ? .visible : .hidden, for: .navigationBar)`.
- **Fix**: keep; do a device pass for swipe-back and the heading trait.

#### HIG-064 iPad has no menu-bar commands and little pointer support
- **Severity**: P2. **Platform**: iPadOS.
- **Location**: `apple/Mac/App/PersonaMacApp.swift:27`, `apple/Mac/App/PersonaMacApp.swift:127`, `apple/Persona/DesignSystem/ShellComposerBar.swift:213`, `apple/Persona/DesignSystem/Styling.swift:45`, `apple/Persona/DesignSystem/MacHover.swift:33`, `apple/Persona/Views/Session/Terminal/TerminalInput.swift:262`.
- **Violates**: [Design principles][dp] ("Consider a variety of input methods"); the keyboard and pointing pages were not in the digest, so this is a recommendation.
- **Problem**: the Mac app has full commands; the iOS target has none, so the iPad shortcut overlay lacks New Chat, Search, Settings or tab switching. Hover effects are visionOS only; system buttons and glass `.interactive()` still respond, so the gap is custom tappable rows.
- **Evidence**: `.hoverEffect(isEnabled: interactive)` inside a visionOS block only.
- **Fix**: share a `.commands` set with the iOS scene; add `.hoverEffect(.highlight)` to custom rows.

#### HIG-065 Three custom drag-to-dismiss gestures with different thresholds
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Chat/MessageLift.swift:172`, `apple/Persona/Views/Session/SessionPanes.swift:500`, `apple/Persona/Views/Session/SessionPanes.swift:520`, `apple/Persona/Views/Interactions/InteractionViews.swift:1514`.
- **Violates**: [Gestures][gest]: consistent, responsive gestures. Alternatives exist (scrim tap, escape action, labeled close handle), so this is consistency only.
- **Problem**: the lift menu closes at a 44pt downward drag; the terminal cover tracks live with a velocity-aware release (which suits the HIG's guidance to track gestures directly). No shared constant.
- **Evidence**: `DragGesture(minimumDistance: Theme.Space.l).onEnded { if $0.translation.height > Theme.Size.tapTarget { menu.close() } }`.
- **Fix**: one shared dismiss-drag constant; optional visible Close in the terminal grab bar.

#### HIG-066 Swipe Close and Delete are tinted, not marked destructive
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Chats/SpaceView.swift:317`, `apple/Persona/Views/Chats/SpaceView.swift:320`, `apple/Persona/Views/Settings/AssistantProactiveViews.swift:1064`.
- **Violates**: [Buttons][btn]: use roles consistently; destructive roles for destructive actions.
- **Problem**: plain buttons with a danger tint, while the matching context menus use the destructive role. Both flows confirm first, so behavior is safe.
- **Evidence**: `Button { pendingDelete = e } label: { Label("Delete", ...) }.tint(Theme.danger)`.
- **Fix**: `Button(role: .destructive)` and drop the manual tint; keep full swipe off on Close.

#### HIG-067 The iPad TabView set changes with the Feishu preference
- **Severity**: P2. **Platform**: iPadOS, visionOS.
- **Location**: `apple/Persona/Views/RootView.swift:73`, `apple/Persona/Views/RootView.swift:178`, `apple/Persona/Views/RootView.swift:187`, `apple/Persona/Views/Inbox/Lark/LarkHomePreference.swift:13`, `apple/Persona/App/AppModel.swift:3195`.
- **Violates**: [Tab bars][tab]: do not hide tab bar buttons when content is unavailable.
- **Problem**: the Feishu tab appears and disappears with a setting and hub features, so the tab set is not stable. The iPhone uses the shell with no tab bar, so this applies to iPad and visionOS only.
- **Evidence**: `if LarkHomePreference.shared.shows { FeishuRootTab() }`.
- **Fix**: keep the tab fixed with an empty state that explains how to enable it, or fold Feishu into another page. Record the split navigation model in the design system.

#### HIG-068 Errors fall back to localizedDescription with no next step
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/App/AppModel.swift:3161`, `apple/Persona/Views/Agents/PersonalAgentListView.swift:255`, `apple/Persona/Views/Session/FolderTransfers.swift:181`, `apple/Persona/Views/Chat/RenderedMarkdown.swift:346`, `apple/Shared/APIClient.swift:1619`.
- **Violates**: [Writing][writing] (error messages): say how to fix it; [Feedback](https://developer.apple.com/design/human-interface-guidelines/feedback): explain why a command cannot be carried out.
- **Problem**: errors are already filtered (offline and cancellation are quiet, coded failures show a card, hub messages are readable), so raw dumps do not reach users in the cases checked. Residual: unmapped system errors (TLS, decoding, web view) show system English, and no message offers a next step.
- **Evidence**: `toast = Toast(error.localizedDescription, style: .error)`.
- **Fix**: extend the shared error text helper to map common URL, TLS and decoding errors to a sentence plus a next step; keep raw text for logs.

#### HIG-069 Notification permission is requested at launch with no explainer
- **Severity**: P2. **Platform**: iOS, iPadOS.
- **Location**: `apple/Persona/App/PushRegistrar.swift:26`, `apple/Persona/App/PersonaApp.swift:234`, `apple/Persona/App/AppModel.swift:615`, `apple/Persona/App/AppModel.swift:438`.
- **Violates**: [Privacy][priv]: request in context, avoid launch requests unless obviously required; [Managing notifications][notif].
- **Problem**: the first launch after pairing shows the system alert over the first screen, with no screen saying what will be sent. The code comment justifies the early grant (approval pushes are core) and the HIG exception for obviously required features partly applies. Repeated call sites do not re-prompt (the system asks once). The Notification settings page and Open Settings button are good.
- **Evidence**: `try? await center.requestAuthorization(options: [.alert, .sound, .badge, .timeSensitive])`.
- **Fix**: add one explainer step after pairing with a single Continue button that triggers the request; elsewhere only register when already granted. Consider provisional authorization.

#### HIG-070 Haptic types do not match meaning
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/App/AppModel.swift:3236`, `apple/Persona/Views/Chat/MessageViews.swift:586`, `apple/Persona/Views/Settings/HubUpdateViews.swift:273`, `apple/Persona/Views/Settings/LookPickers.swift:36`, `apple/Persona/Views/Shell/ShellPager.swift:97`, `apple/Persona/Views/Assistant/AssistantSidebar.swift:91`.
- **Violates**: [Playing haptics][haptics]: use patterns consistently with their documented meaning; avoid overuse.
- **Problem**: four wrappers (light tap, success, error, warning), no selection feedback except in the edge scrub. Theme selection fires success; page and drawer snaps fire an impact; each call allocates a generator without preparing. Copy firing success is arguable. The system haptics switch still applies.
- **Evidence**: `static func tap() { UIImpactFeedbackGenerator(style: .light).impactOccurred() }`.
- **Fix**: add a prepared selection wrapper for pickers and snaps; keep success and error for outcomes; document the mapping in the design system.

#### HIG-071 Dictation denial is a text-only toast
- **Severity**: P2. **Platform**: iOS, macOS.
- **Location**: `apple/Persona/Views/Chat/PromptComposer.swift:338`, `apple/Persona/Views/Chat/PromptComposer.swift:419`, `apple/Persona/DesignSystem/ComposerHoldMic.swift:41`, `apple/Persona/App/AppModel.swift:3202`.
- **Violates**: [Privacy][priv]; [Writing][writing]: link to a setting instead of describing where it is.
- **Problem**: denial shows "Turn it on in Settings" as an error toast with no action, unlike photos, voice entry and notifications, which offer Open Settings. It does not say whether speech or microphone was denied. The request is in context, which is good. The iOS path is the shared hold-to-dictate button.
- **Evidence**: `return String(localized: "No access to the microphone or speech recognition. Turn it on in Settings › Persona.")`.
- **Fix**: return a typed denial, name the missing permission, and attach an Open Settings toast action (the toast type already supports actions).

#### HIG-072 Time Sensitive is set on every interaction push
- **Severity**: P2. **Platform**: iOS, watchOS.
- **Location**: `server/internal/hub/push_payload.go:85`, `server/internal/hub/push_payload.go:250`, `apple/Persona/Resources/Persona.entitlements:11`, `apple/Persona/App/PushRegistrar.swift:26`, `server/internal/hub/quiethours.go:151`, `apple/Persona/Views/Settings/NotificationSettingsView.swift:174`.
- **Violates**: [Managing notifications][notif]: represent urgency honestly.
- **Problem**: approvals, questions, plans, screens, elicitations and the to-do decision push all break through Focus. Defensible when an agent is blocked on a person, and quiet hours downgrade to passive, but form-fill and recommendation cards are lower urgency. The per-kind selection code was not reviewed. No in-app per-kind setting exists; the view points to system Settings.
- **Evidence**: `"interruption-level": "time-sensitive"`.
- **Fix**: keep time-sensitive for permission and plan approvals; use active for the rest; optionally add a per-kind break-through preference.

#### HIG-073 Notification Deny action is styled destructive
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/App/NotificationActions.swift:23`, `apple/Persona/App/NotificationActions.swift:24`, `apple/Persona/Views/RootView.swift:151`, `apple/Persona/App/PairReceiptActions.swift:78`.
- **Violates**: [Notifications][notifs] (Notification actions): "Prefer nondestructive actions."
- **Problem**: Deny is red, though it loses no data and is the safe answer; the in-app alert uses the cancel role. Allow uses a raw "checkmark" string instead of a `Theme.Icon` token. Revoking a pairing credential (line 78) is truly destructive; "Not Mine" is debatable.
- **Evidence**: `UNNotificationAction(identifier: deny, title: String(localized: "Deny"), options: [.destructive], ...`.
- **Fix**: use no options for Deny and the icon tokens for both.

#### HIG-074 Camera purpose string shows a CLI command
- **Severity**: P2. **Platform**: iOS, macOS, visionOS.
- **Location**: `apple/Persona/Resources/Info.plist:57`, `apple/Mac/Resources/Info.plist:56`, `apple/Vision/Resources/Info.plist:48`, `apple/project.yml:177`, `apple/project.yml:273`, `apple/project.yml:361`.
- **Violates**: [Privacy][priv]: clear purpose string; [Writing][writing]: avoid jargon.
- **Problem**: the camera usage text includes a backticked shell command. The strings are declared in `project.yml` as well as the plists, so editing only one drifts. Whether Mac and Vision need their other usage strings was not checked.
- **Evidence**: `Persona uses the camera to scan the pairing QR code printed by ...`.
- **Fix**: say "scan the QR code on your computer when you connect it, for example when you first pair your hub"; edit `project.yml` and the plists together.

#### HIG-075 Watch pet tap is not exposed to assistive tech
- **Severity**: P2. **Platform**: watchOS.
- **Location**: `apple/PersonaWatch/WatchPetView.swift:131`, `apple/PersonaWatch/WatchPetView.swift:133`, `apple/PersonaWatch/WatchPetView.swift:105`.
- **Violates**: [Accessibility][a11y]: gesture alternatives.
- **Problem**: the clip is decorative (correct) and Reduce Motion is honored; the state is carried by the title text. Only the tap that replays the animation is unreachable by VoiceOver or Switch Control. Decorative, so minimal.
- **Evidence**: `.contentShape(Rectangle()).onTapGesture { tap(status) }`.
- **Fix**: optional `.isButton` with a "Play animation" label, or leave as is.

#### HIG-076 Watch complication faces have no accessibility label or tinted treatment
- **Severity**: P2. **Platform**: watchOS.
- **Location**: `apple/PersonaWatchWidgets/PetComplicationView.swift:32`, `apple/PersonaWatchWidgets/PetComplicationView.swift:38`, `apple/PersonaWatchWidgets/PetComplication.swift:9`.
- **Violates**: [VoiceOver][vo]; [Widgets][widgets]: support tinted appearances.
- **Problem**: circular and corner faces are an unlabeled bitmap (and an unlabeled placeholder) that is the only carrier of agent state. No widget label for the corner family and no rendering mode handling for tinted faces. Rectangular and inline carry text.
- **Evidence**: `Image(uiImage: image).resizable().scaledToFit().clipShape(Circle())`.
- **Fix**: label both with the status title, add a widget label for corner, and branch on the rendering mode.

#### HIG-077 Extension widget has a clear container background
- **Severity**: P2. **Platform**: iOS widgets.
- **Location**: `apple/Shared/Design/ExtensionSurfaceTokens.swift:18`, `apple/Shared/ExtensionSystemWidget.swift:156`, `apple/Shared/ExtensionSystemWidget.swift:101`.
- **Violates**: [Accessibility][a11y] (contrast); [Widgets][widgets].
- **Problem**: small, medium and large widgets draw `.primary` and `.secondary` caption text over the bare wallpaper, so contrast depends on the user's image. One read-only widget; system modes replace the background; ratios are unmeasured.
- **Evidence**: `static let surface = Color.clear`.
- **Fix**: a system background for system families and clear only for accessory families; check light, dark, tinted and clear.

#### HIG-078 PersonaLink menu mixes hard-coded Chinese with localized strings
- **Severity**: P2. **Platform**: macOS helper.
- **Location**: `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:24`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:57`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:105`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:166`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:186`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:287`, `apple/PersonaLink/Sources/PersonaLink/Attention.swift:277`.
- **Violates**: [Layout](https://developer.apple.com/design/human-interface-guidelines/layout): test localizations; [Writing][writing]: one consistent voice.
- **Problem**: the row title is a plain string, so literals (status, state pill, reconnect and quit rows, and more) are never looked up in the catalog. The same panel then alternates languages for English users. Counts of lines with CJK text: 44 in the menu view, 36 in the pairing view, 5 in the rename view.
- **Evidence**: `MenuRow(title: <hard-coded Chinese>, ...)` versus `MenuRow(title: linkText("Set Web Address…"), ...)`.
- **Fix**: route every visible string through `linkText(...)` with English keys and en, zh-Hans and ja entries; changing the parameter to a localized key alone would miss this package's bundle. The owner should decide whether the helper is Chinese-first.

#### HIG-079 Live Activity secondary text is regular weight
- **Severity**: P2. **Platform**: Live Activity.
- **Location**: `apple/PersonaWidgets/FleetLiveActivity.swift:60`, `apple/PersonaWidgets/FleetLiveActivity.swift:69`, `apple/PersonaWidgets/FleetLiveActivity.swift:188`, `apple/PersonaWidgets/FleetLiveActivity.swift:194`, `apple/PersonaWidgets/FleetLiveActivity.swift:220`, `apple/PersonaWidgets/FleetLiveActivity.swift:409`, `apple/PersonaWidgets/FleetLiveActivity.swift:426`.
- **Violates**: [Live Activities][la]: use text at medium weight or heavier.
- **Problem**: the request line, place, hub notice, overflow count and finished summary use caption or caption2 at regular weight (at least nine sites). Contrast is acceptable (white at 0.6-0.65 on the dark island).
- **Evidence**: `Text(FleetText.line(item)).font(.caption).foregroundStyle(secondary).lineLimit(1)`.
- **Fix**: a shared medium-weight caption style, starting with the needs-you request line; use one secondary opacity.

#### HIG-080 PersonaLink menu rows highlight only on hover
- **Severity**: P2. **Platform**: macOS helper.
- **Location**: `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:306`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:286`, `apple/PersonaLink/Sources/PersonaLink/PersonaLinkApp.swift:14`, `apple/PersonaLink/Sources/PersonaLink/MenuView.swift:58`.
- **Violates**: [Buttons][btn]: always include a press state for a custom button; [Accessibility][a11y]: support Full Keyboard Access.
- **Problem**: rows are plain buttons whose accent fill comes only from hover; keyboard focus gets no highlight and there is no arrow-key navigation (only a quit key equivalent). Rows have visible title text, so VoiceOver is fine.
- **Evidence**: `Button(action: action) { content(hover && !disabled) }.buttonStyle(.plain).onHover { hover = $0 }`.
- **Fix**: drive the highlight from hover or focus; optionally add arrow-key handling.

#### HIG-081 Mac Link panel header buttons rely on tooltips
- **Severity**: P2. **Platform**: macOS.
- **Location**: `apple/Mac/Link/MacLinkPanel.swift:183`, `apple/Mac/Link/MacLinkPanel.swift:202`, `apple/Mac/Link/MacLinkPanel.swift:220`.
- **Violates**: [VoiceOver][vo]: labels for custom elements.
- **Problem**: Back, Forward, Reload and More are bare images with only a tooltip (4 of 6 icon buttons; Add context and Close have labels).
- **Evidence**: `Button { web?.goBack() } label: { Image(systemName: Theme.Icon.back) }.help("Back")`.
- **Fix**: add clean accessibility labels or use `Label` with an icon-only style.

#### HIG-082 Watch card fill copies the system row gray
- **Severity**: P2. **Platform**: watchOS.
- **Location**: `apple/Shared/Design/Theme.swift:443`, `apple/PersonaWatch/WatchRecordingView.swift:71`, `apple/PersonaWatch/PersonaWatchApp.swift:552`, `apple/Shared/Design/WatchFaceTokens.swift:17`.
- **Violates**: [Color][color]: avoid hard-coding system color values.
- **Problem**: the card fill is an RGB literal copied from the list row color, so it can drift from system rows. watchOS is always dark; the Increase Contrast rationale is unverified. The black face background is an accepted exception.
- **Evidence**: `static let cardFill = Color(red: 0x22 / 255, green: 0x22 / 255, blue: 0x23 / 255)`.
- **Fix**: use a semantic fill or material, or keep the literal with a note on which watchOS version it was matched to.

#### HIG-083 Lark chat and connector-thread transcripts have no readable-width cap on iPad
- **Severity**: P2. **Platform**: iPadOS.
- **Location**: `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:333`, `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:401`, `apple/Persona/Views/Inbox/ConnectorThread/ConnectorThreadView.swift:49`, `apple/Persona/Views/Inbox/ConnectorThread/GitHubThreadView.swift:49`.
- **Violates**: [Layout](https://developer.apple.com/design/human-interface-guidelines/layout): on wide screens keep text lines to a comfortable reading width rather than stretching to the window edges.
- **Problem**: the main chat, home, onboarding, health and settings pages cap their measure at `ChatView.readableWidth` (760pt). The Lark chat ScrollView and the Connector and GitHub thread ScrollViews only pad horizontally and fill the column. On a 12.9 or 13 inch iPad in full screen or landscape, message text and GitHub comment bodies run well over 100 characters per line. These are secondary inbox flows and the effect shows only on wide iPads.
- **Evidence**: `LarkChatView.swift:401` has only `.padding(.horizontal, Theme.Space.m)` inside the ScrollView at `:333`; `ConnectorThreadView.swift:49` and `GitHubThreadView.swift:49` use `.padding(.horizontal, Theme.Space.page)` with no maxWidth. Capped screens: `apple/Persona/Views/Chat/ChatView.swift:279` (constant at `:3062`), `apple/Persona/Views/Shell/ShellHome.swift:52`, `apple/Persona/Views/Assistant/AssistantView.swift:504`. No cap found in `Lark/` or `ConnectorThread/`.
- **Fix**: wrap the content inside each ScrollView in `.frame(maxWidth: ChatView.readableWidth)` and center it, keeping the background and scroll area full width. Move the constant to `Theme.Size` so every screen shares one source.

#### HIG-084 Lark chat side margins use Space.m (12) instead of Theme.Space.page (16)
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:401`, `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:490`, `apple/Persona/Views/Inbox/Lark/LarkChatView.swift:502`, `apple/Shared/Design/Theme.swift:223`.
- **Violates**: [Layout](https://developer.apple.com/design/human-interface-guidelines/layout): use consistent standard margins so content lines up with the rest of the screen.
- **Problem**: `Theme.Space.page` (16) is the page's left and right edge. The Lark chat list and its banners use `Space.m` (12), 4pt tighter than the rest of the app, while the same file uses `Space.page` elsewhere. Bubbles may be inset on purpose; the banner rows are the clearer inconsistency. Minor and debatable.
- **Evidence**: `Theme.swift:210` gives `m = 12` and `:223` gives `page = l`; `LarkChatView.swift:401`, `:490` and `:502` use `Theme.Space.m` horizontally.
- **Fix**: use `Theme.Space.page` for the banner rows at `:490` and `:502`. For the scroll content at `:401`, use `page` or name a chat-specific inset token if the tighter margin is intended.

#### HIG-085 Terminal Shortcuts panel is offset by a fixed 52pt above the key bar
- **Severity**: P2. **Platform**: iOS.
- **Location**: `apple/Persona/Views/Session/SessionPanes.swift:318`, `apple/Persona/Views/Chat/TerminalKeys.swift:117`.
- **Violates**: [Layout](https://developer.apple.com/design/human-interface-guidelines/layout): respect system-defined safe areas, margins and guides; [Typography][typo]: verify layouts at all text sizes. Hard-coded offsets tied to another view's size are a general layout risk, not a quoted rule.
- **Problem**: the `ShortcutsPanel` overlay is lifted with a literal `.padding(.bottom, 52)` to clear the terminal key bar. The bar height uses `.scaledFrame(height: Theme.Size.tapTarget)` and so grows with Dynamic Type, but the 52 does not, so the panel can overlap the bar at large sizes. Bar height at accessibility sizes was not measured. Secondary UI, small effect.
- **Evidence**: `SessionPanes.swift:318` has `.padding(.bottom, 52)` inside `.overlay(alignment: .bottomLeading)` on the key bar (`:311-319`).
- **Fix**: position the panel against the bar's top edge (an alignment guide from `.bottomLeading` to `.topLeading`, or a VStack above the bar), or use a `@ScaledMetric` or `Theme.Size` token matching the bar height.

## 5. Appendix: findings reviewed and rejected

- Dynamic Type caps applied to small text in cards: five of six cited lines cap SF Symbols inside fixed circles, a documented pattern (`docs/apple/ios.md`); only a single-letter badge caps text, and its frame scales with it.
- Password AutoFill disabled for mail and pairing fields: a documented decision (one-time code and app passwords handed to the hub, not this app's password); the fields are secure, which is what the HIG asks.
- Custom sideways pager and drawer gesture conflicts: visible alternatives exist (calendar Today, Previous and Next buttons, tappable shell switch, drawer button), and no actual conflict was shown.
- Hold-to-dictate as a custom gesture: a plain tap toggles dictation and VoiceOver has a labeled action, so the gesture is not the only way.
- Failed send is only a toast: the main composer uses the outbox with an in-transcript failed bubble, Retry and Discard; the toast path serves secondary one-shot sends.
- Live Activity Reduce Motion and VoiceOver gap: nothing animates in the activity views, and the cited lines were unrelated.
- Dropped sub-claims inside kept findings: Sheets Done/Cancel placement (already correct), Lark and PersonaLink details marked above, the watch interrupt confirmation (interrupt is recoverable), the keyboard hiding Connect (Connect is in the nav bar), unused SearchList and ChatPanel components, and a Reduce Transparency problem on system materials (they adapt).

[a11y]: https://developer.apple.com/design/human-interface-guidelines/accessibility
[vo]: https://developer.apple.com/design/human-interface-guidelines/voiceover
[typo]: https://developer.apple.com/design/human-interface-guidelines/typography
[color]: https://developer.apple.com/design/human-interface-guidelines/color
[dark]: https://developer.apple.com/design/human-interface-guidelines/dark-mode
[mat]: https://developer.apple.com/design/human-interface-guidelines/materials
[motion]: https://developer.apple.com/design/human-interface-guidelines/motion
[btn]: https://developer.apple.com/design/human-interface-guidelines/buttons
[tb]: https://developer.apple.com/design/human-interface-guidelines/toolbars
[tab]: https://developer.apple.com/design/human-interface-guidelines/tab-bars
[sheets]: https://developer.apple.com/design/human-interface-guidelines/sheets
[alerts]: https://developer.apple.com/design/human-interface-guidelines/alerts
[undo]: https://developer.apple.com/design/human-interface-guidelines/undo-and-redo
[haptics]: https://developer.apple.com/design/human-interface-guidelines/playing-haptics
[gest]: https://developer.apple.com/design/human-interface-guidelines/gestures
[priv]: https://developer.apple.com/design/human-interface-guidelines/privacy
[notif]: https://developer.apple.com/design/human-interface-guidelines/managing-notifications
[writing]: https://developer.apple.com/design/human-interface-guidelines/writing
[sym]: https://developer.apple.com/design/human-interface-guidelines/sf-symbols
[icons]: https://developer.apple.com/design/human-interface-guidelines/icons
[widgets]: https://developer.apple.com/design/human-interface-guidelines/widgets
[la]: https://developer.apple.com/design/human-interface-guidelines/live-activities
[dp]: https://developer.apple.com/design/human-interface-guidelines/design-principles
[prog]: https://developer.apple.com/design/human-interface-guidelines/progress-indicators
[enter]: https://developer.apple.com/design/human-interface-guidelines/entering-data
[notifs]: https://developer.apple.com/design/human-interface-guidelines/notifications
