# iOS / watchOS App

XcodeGen source is `apple/project.yml`, generating `apple/Persona.xcodeproj` (generated files are not committed); old `apple/Persona.xcodeproj` symlinks to it. Scheme compatibility is in [Compatibility migration](../operations/rename.md). iOS 26 / watchOS 10 (the watch App must install on older watches stuck on watchOS 10), Swift 6, default MainActor isolation. Depends on MarkdownUI.
Bundle ID comes from configuration (`app.bundle_id`, watch `<id>.watchkitapp`, `docs/operations/config.md`). The UI supports English, Simplified Chinese and Japanese (`en` / `zh-Hans` / `ja`).

## Agents recently used view

The Agents grouping menu adds Recently used (Mac: View › Group by), still stored locally in `dev.grouping`; defaults and existing grouping choices stay unchanged.
Pinned sessions appear separately once; remaining sessions flatten across projects and machines, newest `updated_at` first, ties by full session id including machine and hub; missing timestamps last.
Working sessions also use last activity, not work-start time; seen, idle and not-running history appears directly, no folding into projects or Idle spaces.
"Used" means messages or work progress, not opening a page: reading a reply does not change its activity time or position. Machine / hub filters, search, quick state filters and row menus remain; collapse / expand actions are hidden in this view.
The existing `AgentsFeed` and scroll protection remain; structure updates only when activity timestamps change relative session order, with reordering paused while browsing to preserve reading position.
Mac's previous / next Agent follows pinned order then recent activity in this mode; other modes retain their order.

## iPhone Agents 分组入口与长列表（#5766）

- 快速筛选下方增加独立 List 单元中的分组菜单，始终显示当前分组名称，不与搜索输入框共用单元。菜单与原 ⋯ 菜单共用 `dev.grouping`，重启后保留选择。已有分组和默认值不变，平铺视图仍叫「最近使用」。
- In the new shell a **Personal Agents ›** pill sits right of the grouping menu (`AgentsPersonalPill`, #140) and opens the personal agents page: on a phone the shell has no toolbar, so this is the only way there. When the two don't fit on one line (large text) they stack. The iPad and Mac keep the toolbar button and show no pill. UI test `PersonalAgentsEntryTests`.
- 分组折叠继续使用 `dev.folded`，搜索时展开匹配内容。
- 长列表首次显示前 20 项并提供「显示全部 N 个」；不超过 25 项时全部显示。适用于最近使用、状态、待办、机器的活跃行，以及项目和工作树的活跃项；快速筛选或搜索时不截断。Mac 不截断，保留 ↑ / ↓ 导航行为。
- 「显示全部」的展开状态与分组折叠分开，不跨重启保存。项目内一个多 Agent tab 仍作为一项，不拆散 tab。
- 行数上限只是展示策略；是否改善帧率，以相同基底、相同数据和相同工具链的实测为准，不从行数减少推导性能收益。

## Information architecture

Expanded Idle spaces in Agents reuse `AgentListOrder.ordered` across normal cards, compact lists and capsules, newest activity first (#2443).
`SpaceGroup.sessions` keeps original tab order, preserving tab numbers and ordering on space pages; capsule-card bottom rows follow displayed order.

| Tab | Content |
| --- | --- |
| Persona | **Persona assistant** (renamed from Chat 2026-10-30): Claude **running headlessly** in `~/.persona/assistant` on the hub machine (`claude -p` + stream-json, outside herdr, token-by-token streaming; tap the centered top-bar avatar, menu Launcher: …, to change launcher / model anytime, switching with `--resume` from the next message; same menu has Session info; home model label sets new-chat model, default configured in Settings). Hub-generated CLAUDE.md explains computer access (ssh), Lark and other work data (lark-* skills), iPhone (phone_*) and mail (mail_*). Home has greeting + available devices + 6 suggestions; sending immediately shows a bubble and Preparing while the session starts in background. Thinking is hidden; tool calls fold into one line "Tools N · Computer, iPhone…". Top bar and sidebar below. Knowledge (`KnowledgeView`, rebuilt 2026-10-31): top type capsules (Projects / Chats / People / Documents / Meetings / Calendar / Recordings, selected count; phone Recordings contains local recordings and controls, transcripts automatically sync to Knowledge, `docs/apple/recording.md`), top-right tag funnel; projects grouped Focus (P0 / P1), Other active, Completed, with Not projects collapsed. Rows: type tile (initials for people/chats, importance color) + name + one sentence + priority / long- or short-term / relevance / fitting tags. Full-text results merge by entry, one row with name and at most two matches, bold query; tap opens the entry. Details: tile, name, badges, sentence, all tags, Ask Persona / Start Agent; then project Agents, vertical newest-first timeline (6 by default), profile, related entries by type (4 each, expandable), code directories, last 7 days' messages, directory path; top-right Tag edits importance / relevance / tags / project type and state. Screenshots `-tab assistant -openAssistantPage knowledge`, add `-showKnowledgeEntry <kind>/<id>`, `-showKnowledgeLower YES` to skip header and inspect timeline, `-showKnowledgeSearch <query>` for search. First chat is **main chat** (`assistant.main`, marked Main chat in history), later new chats are branches; first launch opens main chat. Other pages' Hand to Persona (Agents long-press, Knowledge Ask Persona) sends to main chat (`AppModel.askPersona`, creates one if absent) |
| Development | Search field starts collapsed, pull down to show / scroll up to hide (`docs/lessons/ios.md` ("Layout and components")); phone quick filters are the list's first row and scroll with it, still above the list in wide split layouts. `AgentFilter`: All / Needs you / Unread / Working with counts, immediate, combined with machine filter and search; not remembered, returns to All. Filter keeps the Agents matching when selected (`kept`), so reading does not remove them; they stay in place with gray dots, spaces and rows sorted only by time for sequential reading; reselecting or changing machine recomputes. Unread has Mark all as seen at top (confirmation). Needs-you cards show only under All / Needs you and respect machine filtering. Top-left menu Cards / Compact list (`dev.compact`, remembered; `CompactSlotRow`: state dot, title, tab (Space › Tab outside project grouping), time). Compact list uses one `CompactList` scale (requested 2026-10-31: harmonious sizes): three font levels (group titles = space / Needs you / Idle spaces / state / machine use `heading`, row titles `secondary`, metadata = tab / time / count / idle-tab name `caption`); every row (Agent, N more idle, Collapse, Idle spaces, Mark all as seen) shares height and padding, leading marks (state dot, disclosure, space icon) align, titles align left; right side only time, aligned in a column, then the row's quiet ⋯ (#1244); idle tab names are small text, not capsules; idle spaces are single rows (name, tabs, time), tap lists Agents beneath, no DisclosureGroup (different height and arrow). Space-title ⋯ uses quiet gray `ellipsis` like counts; Needs you is also one row (`CompactInteractionRow`: red type icon, Plan awaiting approval / Claude has a question…, Session · Machine, time, ›; open chat to answer; card mode retains quick-action cards). Single-row groups are capsules from iOS 26 grouped-list corners (custom backgrounds clipped too), like Settings, accepted 2026-10-31. Collapse all / Expand all. Tap space titles to fold (`dev.folded`, remembered; folded row keeps state counts); state / machine titles also fold. Space pages open via title ⋯ › Open space, also Mark all as seen (N). Grouping menu (`DevGrouping`, remembered): Repository (default for never-chosen grouping, #2108, below) / To-do (#1836; each incomplete to-do with Agents gets a section; unattached manually started herdr sessions (`startedByPersona` false) in expanded Manually started, #2917; remaining unattached sessions last in Unassigned, collapsed when to-dos exist unless Needs you; with herdr and other hosts together, non-herdr rows show host, e.g. an alternative host; hubs lacking to-dos use Project) / Project / State (Needs you → Unread → Awaiting reply → Working → Waiting in the background → Waiting → Muted → Seen; Seen collapsed by default, expands directly to all Agents without N more idle (2026-10-31: formerly two taps). Waiting means no new message but `/loop` not due; background commands / subagents count Working, #2228; hub triage waits (`WorkState.hubWaits`, retries / scheduled wake-ups) also Waiting, #2895; Working that only waits in the background has its own section before Waiting, #6045) / Machine (each machine section with online status and state counts, state order then last update, idle folded). Repository (`RepoTree`, like Orca project sidebar): computers outermost, titles at page edge one font level above groups, state counts, tap folds whole computer using shared machine-fold storage; hub order. Repositories within each computer sort naturally like Finder (app2 before app10), cards contain worktrees (⌂ main first, then names; omit worktree level if only main), Agents indented below. Fully idle worktrees fold to Idle; fully idle repos into Idle repositories at that computer's end. Spaces with `parent_space_id` (Orca child workspaces, #3075, web #2073) attach under the top-level parent's worktree rather than infer from own cwd: after Agents, N children row with state counts, default folded, remembered under `<worktree>/children`; expands child spaces, each worktree row then Agents one level below; deeper levels flattened depth-first. Missing parent or cycles fall back to cwd grouping; other modes remain flat. Repository and worktree: when the session carries `repo_root` (the host knows its repository, #2840: Paseo keeps its worktrees under its own folder, so the path names no repository), group by it: a cwd inside it is the main worktree, any other cwd one of its worktrees; otherwise infer from the cwd like the web (`.worktrees/`, `.claude/worktrees/`, `.herdr/worktrees/`, `docs/web/web.md`). If all repo Agents are in one herdr space (usual), that space's pin / mute / to-do / ⋯ acts on the repo; multiple spaces leave only Mark all as seen. Project grouping also starts by computer, omits machine names from space titles, each computer has its own Idle spaces. Hide computer level when only one has Agents or a machine filter is selected. Collapse all folds only spaces/repos to one row, not computers. Quick filters choose Agents before grouping; computer titles use prefilter data, so filters do not add/remove computers. Non-project row metadata includes Space › Tab (plus machine under State). Three groupings share `ChatsView.rank`: Needs you → Unread → Awaiting reply → Working → Waiting → Muted → Seen, newest update within state; muted Working / Unread / Waiting count Muted, Needs you still first. Project mode retains original sessions (not assistant chats): Needs you cards → pinned Agents (once only, status via dot like other rows, excluded from space/state/machine groups; Agents in spaces use state order; `SpaceView` keeps herdr tab order; watch keeps `AgentState.priority`) → pinned spaces → active → just finished → muted spaces with running Agents (dimmed) → Idle spaces (folded, includes muted idle spaces). Search, machine filters. Rows do not swipe on iOS (#1244: on the way from Agents to a chat a sideways drag kept opening a row's buttons): each row ends in a ⋯ (`AgentRowMoreButton`) with the same menu as its long-press (`AgentRowMenu`: Pin / Unpin, Interrupt while it works or needs you, Mark as Seen when unread, Rename…, Mute, …, Close Agent…); the Mac keeps its trackpad swipes. Close is also in the chat ⋯ › Close Agent…; any herdr Agent can close after bottom `CloseAgentSheet` confirmation: Agent, machine/tab, consequences, Stop and close / Cancel. Space-title ⋯ pins space. Pins stored on hub (`/api/v1/pins`). `/loop` Agents show "⟳ Next 12:20" and do not fold into idle; turn-ended Agents with background commands / subagents show amber "⚙ Background N", Working (#2228), not folded, their spaces not idle; when the hub says all that background work only waits (`background_waiting`, #6045: `sleep`, `gh … --watch`, `tail -f`; `Session.waitsInBackground`, still `state` working so stop / queue act as before), it shows as "Waiting in the background" with one mark everywhere, a clock in Working's amber made lighter (`Theme.Icon.backgroundWaiting`, `StatusPalette.backgroundWaiting`; the user's choice, as on the web): the row's dot and BeautifulUI badge, its words on the row (the compact row only when the title leaves room), its own By Status section, its own quick filter (shown while one exists), the folded counts, the chat header's dot and its background pill / folded mark; it is not Working in the Working filter, the state counts or the Machines tally (`Session.countedState`, it counts as idle there); the watch says it too; older hubs never send it; long-press can stop loop. Rename spaces through title ⋯ / space-page Rename space (updates herdr workspace); Agents via long-press / chat ⋯ Rename… (updates both herdr tab and Claude session name, consistent with Mac herdr / claude --resume). Mute Agents by long-press / chat ⋯; spaces via title ⋯ / space-page menu (group-title long-press does not trigger): no push (Agents can override muted space with Enable notifications and vice versa, `Pins.togglingMute`); dimmed (gray state, 🔕, opacity 0.8, weaker than Working but not fainter than idle spaces), running ones stay expanded, nonrunning muted spaces are ordinary idle spaces; excluded from Agents badge (`/api/v1/mutes`). Same menu Sort (`AgentSort`, `dev.sort`, remembered; hidden under State, already state then time) offers Recently updated (#823): rows only by last update, regardless of state; repos within computers, active project spaces and machines sort by newest Agent (muted spaces stay behind); pins, Needs you, idle folding unchanged. Uses `sortDate`: Working Agents use work-start time so each step does not move them. Default State first, unchanged |
| Inbox | Lark messages and email together, one row per item (new messages in a chat, an email thread), AI triage, swipe left to mark handled; source / account / type filters (`docs/assistant/inbox.md`). Lark opens a chat like mobile Lark: others left with avatars, first consecutive group message shows sender; own right without own avatar/name, blue bubbles (#D1E3FF / dark #133063); tap avatar/name for profile, reaction toggles own reaction; long-press menu starts with 6 common emoji (hub `reactions/quick`), tap reacts and closes; ☺︎ or long-press React opens common + all emoji. Moments shares render as cards opening full post, images and comments. Start at the item's first new message; report only messages actually displayed as read. Rendering below. Email: details, reply, compose, Let Claude handle, account management (top-right menu), add account (also ask Persona "Help me add an email account", `mail_account_add` with phone confirmation) |
| To-dos | List/details (sidebar To-dos with incomplete-count badge; new shell sidebar): priority Waiting for you > To review > In progress > Follow-up > Not started, completed at bottom; create title + description, tap circle or swipe right complete, swipe right reopen, swipe left delete. Details edit title/description, show current and past Agent sessions, remove failed starts; bottom Hand to Agent starts new or picks existing session with structured to-do context card |
| Settings | Assistant (no model displayed/configured here; Model configuration › Main chat, #1291; Proactive work and memory pages, `AssistantProactiveViews.swift`: Scheduled tasks row-end toggles, existing edits save on leaving; new + with Meeting preparation / Weekly report / Agent check / End-of-day review templates uses Cancel / Add sheet so trying a template does not create it; Run now / View last result switches to Chat and opens that chat; one-time follow-ups, interval and scheduled tasks share page/editor; run settings model, permission mode, effort, Allow everything confirmation. Memory parses frontmatter, title prefers Chinese MEMORY.md index title, type badges, Markdown details, Edit whole file, swipe delete, read-only index row; About me edits notes, saves on leaving). Claude Code settings (allowlisted per-machine `~/.claude/settings.json`: exit recap, effort, always think, reply language etc., apply to all; then the computer's Live Replies switch, see "An agent's live reply", and Blocked Config Folders); Machines (online, session count, version, launchers, reconnect); Connectors (2026-11-01: This iPhone = old phone-data toggles/permissions; Email = old Inbox ⋯ account management; Lark = hub gateway status and purpose, `ConnectorsViews.swift`, `GET /api/v1/lark/status`; with hub `connectors`, add rows below Lark from `GET /api/v1/connectors` (2026-11-02, GitHub first: status, account, gh login from <machine>, last sync, generic `HubConnectorView`, DEBUG `-openConnectorGitHub YES`; in-App sign-in connectors (Telegram, `signsInHere`) add enable toggle, stepwise login, sign out, delete stored data, `ConnectorSignInSections`, DEBUG `-openConnectorTelegram YES`)); Notifications; Pair again |

Chat page:

- Messages: user bubbles (coral), assistant Markdown (no bubble, reading first), thinking (one folded row), grouped tool steps (>2 fold into "N steps · Run 5 · Read 3",
  always showing the last step), to-do lists (TodoWrite rendered directly), red/green Edit diffs, Bash commands and output, images.
- Expanded work is one card: each step remains a separate lazy-loaded chat row (a turn may have hundreds; one giant view would hang), with each row drawing a card slice
  (`WorkCardSlice`: top corners on first step, bottom corners on Collapse N steps, backgrounds extending halfway into spacing to join neighbors), thin separators starting at the text column; tool rows 44pt high.
  **Liquid Glass (default) has no card** (requested 2026-10-31: cards did not match chat home, `StyleSpec.workList = .timeline`): steps sit directly on the page, with a thin left vertical line from first step
  through Collapse N steps, circular tool icons on it (page background behind icons masks the line), medium-weight tool verbs; intermediate Agent remarks use smaller gray text (`AssistantText(quiet:)`),
  reading as process rather than reply; thinking, folded steps, to-dos, tool details and Collapse start at the text column right of the line. Other styles keep cards (`.card`).
  Background slices extend halfway into spacing (`WorkCardSlice.bridge = Theme.Space.l / 2`): the old hard-coded 7 for 14pt spacing left 2pt white seams after spacing changed to 16.
- One `DiffView` (Claude Edit / MultiEdit / Write supplies old/new text; Codex unified diff), sharing Git row models (`DiffText.Line` / `parse`):
  header filename, directory, +N −N (opens file); only ± and row backgrounds are colored, code uses body color; long lines scroll horizontally with right fade; beyond 40 lines show beginning + Expand all.
  Whole-file Git diffs still use `MonoTextView` for large-diff performance, same models and colors. Tool failures put Error above output instead of covering its first line.
- Image viewer: every full-image entry in the App (chat images, sent images, images viewed this turn, tool results, Files images, notebook outputs, Markdown web images and diagrams,
  Lark messages / cards / Moments / documents, Inbox connector messages, Lark attachments) uses one `ImageViewer` (`ImageViewer.swift`, `ViewerImage` data:
  image bytes, computer file or Lark URL), styled like Photos:
  - Top: circular Back · two-line title capsule (name; "9 / 20 · source or dimensions") · circular ⋯ menu (Copy image, Annotate, Copy path, Open in Files, Info);
    bottom: thumbnail strip for multiple images (current slightly wider, tracks paging centered); circular Share · capsule Save (add-only permission) / Annotate / Info · circular Copy. Icons only, fixed 44pt,
    text scaling capped at xxxLarge, fits iPhone SE. Bars and buttons use design-system `ViewerTopBar` / `ViewerBottomBar` / `ViewerIcon`
    (`DesignSystem/ViewerChrome.swift`), also used by message Share previews, keeping both identical (#4714).
  - Background follows light/dark (`Theme.Viewer.canvas`); single tap hides bars and turns black (`immersive`); Mac single tap does not hide (Close and Esc are in the bars).
  - Gestures: pinch / double-tap zoom, pan when zoomed, swipe to switch, drag down at fitted size to dismiss: image shrinks with finger, background fades, release closes or springs back by distance / velocity (`ZoomMath.pullProgress` / `pullCloses`);
    system zoom-transition drag dismissal disabled via `.interactiveDismissDisabled()` to avoid competing gestures. Thumbnail entry points use zoom transitions.
  - Paging on iOS is a UIKit paging scroll view (`ImagePager.swift`, `ImagePagerView` / `PagerScrollView`), not a page-style `TabView`: SwiftUI updates only hand pictures to existing pages,
    `index` changes once a swipe has come to rest, and the pages move to the nearest whole page whenever they come to rest off one (`PagerMath`, #5678). A page that leaves the screen goes back to fitted.
    `-showViewerDecodeDelay 0.35` makes pictures arrive late (UI test `ImageViewerTests.testSwipesAlwaysEndOnAWholePage`).
  - Annotation (`ImageEditor.swift`): rectangle / brush, 6 colors, undo / clear, then Add to input as next-message attachment (`AppModel.composerInbox`),
    ready to send with a note; only available when opened from a chat. Pending input images can also be tapped to edit before sending.
  - Screenshots: `-openFirstImage YES` / `-openViewedImage N`, `-showViewerChromeHidden YES` hides bars, `-showViewerInfo YES` opens info.
- Background sync (push + pull like IM; only foregrounded and connected): hub pushes which session changed; App pulls increments by cursor.
  Agents (`AgentSync`): changed `rev` in session list / `revs` triggers `after_seq` prefetch into the disk cache only for chats not retained in memory,
  so opening is already current; scope active Agents + sessions used within 24 hours (at most 40, newest first, concurrency 3).
  Retained chats without a subscription owner keep their store and covered sequence unchanged until their next visible subscription replays `since_seq`.
  Eligibility is rechecked when queued work starts; a visible catch-up cannot apply after the last owner leaves or the store is replaced.
  Currently viewed chats rely on their subscriptions without duplicate fetches. When a subscription stops (the session's `rev` moved after the
  chat's last items came, or its `updated_at` is newer than the open chat's newest line, and no items came for 4 seconds), the chat fetches
  after its `covered` and merges silently: at most every 10 seconds, once per `updated_at` (`AgentSyncPacing.catchUpAt`, the rule of the
  web's `web/src/lib/stale-page.ts`). The `rev` catches the last line of a burst whose push was lost, which `updated_at` cannot (#1179). Working Agents fetch at most once per minute (`AgentSync.workingGap`; each fetch decodes and rewrites the entire disk cache),
  immediately at turn end / Needs you (most likely to open); 19-minute real-hub replay: 45 calls/minute → 9 (2026-11-02 battery investigation). Lark (`LarkSync`): hub forwards gateway live events (`lark`), refreshing Inbox Lark chats and open chats
  (polling fallback only: list 90 seconds, chat 60); changed Inbox chats fetch the newest page into `lark-chat-<id>` cache; on hub connection catch up latest 20;
  unread chats open from cache if it reaches the first unread message.
- Settings › Storage (`StorageView`): space used on this phone, size and share bars by category (Agent chats, chat images, Lark images/files, lists/Lark chats, websites,
  network cache, other, App data); swipe left / long-press clears a category, or Clear all caches (settings and pairing retained; websites clear cache, not login).
  Memory copies clear too; background sync refetches. DEBUG `-tab settings -openStorage YES`.
- Lazy images: history pages and pushes omit image bytes (`lazy_images`, reducing pages from MB to hundreds of KB), dimensions only; visible rows fetch through
  `ImagePayload.ref` (`ChatImageLoader`: one download, NSCache + `Caches/chat-images`, immutable images valid forever),
  reserving dimensions before arrival to avoid jumps. Thumbnails and full viewer share it; locally sent images retain inline base64.
  A failed load (hub unreachable, download failed) does not spin forever (#182): tool-result and file-page images (`Base64Image`) say
  "Image didn't load. Tap to retry" on the placeholder, and the viewer's page says so with Retry, keeping its bars (and Close) and the
  page's own backdrop while it is the page shown; downloaded bytes that are not an image are not kept on disk.
  Sent photos (`SentPhotos`, the sending bubble, both `ChatThumbnail`) and the images viewed this turn (`ViewedImageThumb`) use the
  same placeholder (`ImageNotLoaded`: narrow ones fall back to "Retry" or the symbol; bytes already here that don't decode say
  "Can't show this image", without a retry); a file's thumbnail known only by path keeps its symbol (`FileThumbnails` keeps
  that failure for the session, #559). Their thumbnails sit inside the tile's button that opens the viewer, so they record a
  failure in `ImageRetries` and that button asks the hub again instead of opening the viewer, and VoiceOver reads it as the retry.
- Links (`Views/Link/LinkSheet.swift`): chat http(s) opens an in-App bottom card like Telegram: full screen immediately, at 60% pulled down the chat remains usable, further down closes;
  behind a 60% card chat still scrolls and accepts input (`presentationBackgroundInteraction`). Lark docs / Knowledge use native `FeishuDocReader`,
  other websites `WebViewPool`: preload first 3 links in each appearing reply, at most 6 resident WKWebViews (LRU), sharing default disk website data (cache, login),
  requests `returnCacheDataElseLoad`, opening reuses the loaded view. Selected web text gets AI edit (enter request, immediately send to Agent) and
  Add as context (quote strip above input, sent with next message); native Lark docs use title-bar Add as context for the whole document. Message context is
  `<persona-context kind="context|edit" title="…" url="…">selected text</persona-context>`, raw tags hidden.
- This-hub links (#2696, `Shared/HubPageLink.swift`, `App/HubPageRouting.swift`): assistant links to this hub's web pages (#2194) open native App pages, not website cards:
  `/s/<machine>/<session>`, `/t/<machine>/<space>/<slot>/<session>` open Agent chat; `/persona/<id>`, `/persona/<id>` open Persona chat; `?at=<item id>` scrolls and flashes the message
  (same as quote navigation; fetch a few older pages if missing, otherwise stop at latest); `/todos?id=<id>`, `/todos/<id>` present to-do details. Session ids may be unique prefixes (web #2464).
  This hub matches host + port, ignoring http/https: paired group addresses, active socket address, mesh endpoints, hello `public_url`; reverse-proxy subpath must match its prefix.
  Other sites, `/todos` board, `/inbox`, `/machines` and unmatched App routes, other hub groups' to-dos and Persona chats with unknown machine still use website cards. Watch messages render no links.
- Quotes (#986, requested 2026-11-03, Telegram-style; `DesignSystem/QuoteViews.swift`):
  - **Swipe left to quote** (#1058, Agents chats): drag own messages or Agent text replies left, not work rows.
    - Bubble follows finger, reply icon revealed right; past `MessageSwipe.threshold` light haptic and accent-filled icon; farther motion rubber-bands.
    - Release springs back, adds quote strip, focuses input; below threshold does nothing.
    - UIKit `LeftDragRecognizer` immediately fails unless initial motion is horizontal left; outer scroll views (list, shell paging) wait for failure, preserving vertical scroll and left-edge right-swipe Back.
    - It also fails when the finger lands on something that scrolls sideways and still has room to the right (a wide table or code block, #5737, `DrawerPan.Coordinator.sidewaysScroller`): that scrolls; at its right end, on a table that fits, or on a plain reply the drag quotes (`SwipeWideContentTests`, `MOCK_WIDE=1`).
    - Not enabled for assistant chats, whose left swipe belongs to shell paging.
  - **Above input**: long-press Quote, website Add as context and @session become strips inside input above text.
    Each has accent vertical line, title ("Quote Claude", "Quote your message", "Selected text · Page", "Session …"), summary, right ×.
    One quote has two summary lines; multiple stack with one each.
  - **After sending**: same quote block embedded atop own bubble; bubble width equals its widest block. Tap:
    - Same-chat message (`kind="quote"`, `msg="<message id>"`) scrolls to original and flashes (`QuoteFlash`);
    - If not loaded, expand full text;
    - Website excerpts open the page.
  - **Styles**: Liquid Glass and others use Telegram line + text (pale bubble backing); BeautifulUI uses a small card with line on its left edge.
  - **Not quotes**: hub Lark /agent instructions/docs (kind instructions / doc) still fold into one row (`ContextGroup`).
  - **Lark chats**: reply strip also inside input; bubble quote navigates to original.
  - Screenshots `-showSampleQuotes YES` adds website excerpt and session above input; end-to-end `QuoteBubbleTests`.
- Files: message paths and Read / Edit / Write tool paths become tappable file tags, read from the session's computer
  (`GET /api/v1/machines/{mid}/file`, at most 2 MB, iCloud directories denied), rendered Markdown / numbered code / images / PDF / directories.
- Other Agents in the space (Agents): the left part of the header's second row is `SiblingTabs` (restored flat tabs at request 2026-10-31), one capsule each with state-color background
  (Needs you red, Working orange, Unread green, Seen / Idle gray, no blinking dots; kept at the user's request on 2026-11-06 over a neutral fill with a dot, `docs/archive/apple/plan-chat-topbar.md` Q2); current bold outlined.
  A name is one line cut at its end, at most `Theme.Size.tabName` wide (BeautifulUI's chip sizes itself, so it is cut by length, `SiblingTabs.capped`): one long name cannot push the current tab away.
  Shows current and fitting neighbors, horizontal scroll all, and an end fades while there is more to scroll that way (`.edgeFade()`); at the end the space icon and the tab count (not ⋯, the page's menu) opens the full list (dot, name, state,
  current checkmark; red top-right dot when another Agent needs you), first All Agents in space X opens space page; chat ⋯ › All Agents in space same.
  Swipe tab rows left for Close (no full-swipe execution); list rows and capsules also long-press Stop and close Agent. Confirm if working, awaiting interaction, unknown, or holding background / queued / loop tasks; idle and completed close directly.
- Delete chat (#1749, web #801): nonrunning sessions (`Session.canDelete`: Not running / Done / Seen (idle) / Unknown, not starting, not assistant) offer Delete chat… in Agents long-press / chat ⋯ (Mac also Agent › Delete Chat…); system confirmation explains removal from client lists only, computer records retained, reappears when reused (`DeleteSessionText`). `AppModel.deleteSession` calls `DELETE /api/v1/sessions/{mid}/{sid}`, removes locally on success (matches next hub sessions push), exits every open page (split details, separate Mac windows, `AppModel.deletedSession`); failure shows hub message.
- Node menus (#3098, web #2329, `docs/integrations/terminal.md` ("Node menus")): running herdr Agent long-press adds Split pane ▸ Right / Down, Git ▸ Pull / Push / Fetch (pane cwd), Close tab… (system confirms all panes and Agents close); space ⋯ adds Run Agent here… and Git ▸ (workspace cwd). Run Agent here… sheet loads `GET /machines/{mid}/panes`, lists workspace shell panes with no Agent/bound session and directly startable Claude Code launchers (Codex requires first message, use New Agent), Run calls `panes/agent`; if no idle shell, create New terminal tab (`POST /panes`) then select. herdr only (not Orca, herdr-core or alternative hosts); rename retains Rename…. Logic `Views/Chats/NodeActions.swift` (`NodeMenu` pure + `NodeActions` execution/confirmation, `nodeActionPrompts()` on Agents list); menus stay Equatable, no changing push-data reads; failures show hub/node raw text in red toast. `NodeMenuTests`.
- New worktree… (#3099, web #2431, `docs/integrations/terminal.md` ("New worktree…")): herdr space ⋯ sheet `NewWorktreeSheet` calls `worktree/prepare` for repo ("persona · Current branch main"), branch and checkout path (tracks branch like herdr/web until manually edited, clears to resume; immediate `git check-ref-format` validation), Create calls `worktree/create`. Git refusal shown unchanged; old hub/node says "Persona on this computer is too old; update to create worktrees". No phone terminal page to navigate to; after creation sheet becomes New chat prefilled with workspace / checkout directory (`SpacePreset`), starting inside new worktree. Logic `WorktreeForm` (`Views/Chats/NodeActions.swift`), tests `WorktreeFormTests`, `NodeMenuUITests`.
- A new agent's own worktree (#6115, web #6301, `AgentWorktree.swift`): New Agent asks `POST /machines/{mid}/worktree/status` `{cwd}` for the picked folder; when it is in a Git repository (`path` set) a Worktree section offers In a New Worktree, an optional branch (placeholder `persona/<first message or folder>`, as the node names it) and Checks out to `<checkouts>/<slug>`. Start then calls `worktree/add` and starts the agent in the `path` it returns; a refusal shows next to the start button and nothing starts. Close Agent (`CloseAgentSheet`) and Delete Chat ask the same status for the agent's folder; one Persona made (`ours`) adds Keep Worktree (default) / Remove, Delete the branch too, the counts it loses (`dirty`, and `unmerged` with the branch) and I understand these changes will be lost, which the button waits for. `worktree/remove` runs only after the close / delete succeeded, retries `worktree_in_use` for a few seconds, and sends `force` only for a confirmed loss. Delete Chat stays the system alert unless the agent is in one of ours (the computer is asked first, 4 s at most); then it is a sheet with the section. Mock: `MOCK_WORKTREES=1 MOCK_AGENT_WORKTREE=1`; UI test `AgentWorktreeUITests`.
  Reuses `session.close`: mark local copy Not running only after remote confirmation (history/title/content retained); failure keeps confirmation with error, retry/cancel; no false success without hub.
  List directly observes active same-space sessions; closing others neither exits list nor switches current; closing current selects next active tab, otherwise previous, returns when all closed. Actions remain with one tab.
  On 10-29 changed to one-button `SiblingMenu`, hiding tabs/states; user disliked it.
  If entered from that space page, return directly, no extra layer (`spaceAt` in ChatsView tracks space-page depth).
  Space-top machine button ("Work computer ›") opens `MachineSpacesView`: online/state counts, active projects with newest sentence of most urgent Agent,
  idle projects opening spaces; Agents outside herdr spaces listed directly; top-right ⓘ machine details.
  DEBUG `-tab dev -openMachinePage <machine id>`.
- Machines (`docs/server/machines.md` ("App")): all machines in one page, each card online/system/Agent counts (Needs you / Working / Idle), current CPU/memory/network,
  1-hour mini charts, alerts, Redeploy on version mismatch, urgent first. Details show Agents above (open chats), then charts
  (1 / 6 / 24 hours, press to scrub), system info, launchers. Below Engine: What this computer can do (#3018, node self-check by capability, expandable checks; stale generated files offer confirmed Repair, `docs/server/machines.md` ("Self-check")); entries drawer Machines, Settings › Machines, Agents menu Machines (old Machine monitoring merged).
  Subscription Usage below cards (#2889, `docs/server/usage-windows.md` ("UI (iOS / Mac)")): Codex / Claude / gateway accounts, one row per window, hidden with no data.
  DEBUG `-tab dev -openMonitor all|<machine id> [-openMonitorRange 6h|24h] [-openMonitorScrub 0.55]`.
- Away recap: Claude Code away_summary becomes Away recap card, removing "(disable recaps in /config)"; disable in Settings › Claude Code Settings.
- Display logic (assistant and development unified, `chatLines` + `WorkLine`): turns split by messages, one chat segment per message:
  Your message → gray work row "Tools 5 · Thinking 1 · Replies 1 · 4 min 17 sec ›" (name then count) → Agent's last text before next message → next message…
  Active and complete look identical except active spinner, timer and terminal button ("Working 39 sec · Tools 2").
  The queue strip above the composer (`QueueStrip`): each queued message's menu takes it back to edit, copies or deletes it. For a Claude in a terminal pane (`Session.steersQueue`, #6088) the strip also has Send Now (after a confirmation that says a running command moves to the background and a reply being written starts again; `POST …/queue/send-now`, Claude Code's own send-now, which takes the whole queue; the hub's words for `nothing_queued` / `send_now_unsupported` / `send_now_in_doubt` are shown as they come), and each message Move Up / Move Down (`QueueOrder.moved`: the `/queue` `keep` in the new order). UI test `QueueSteerUITests` (`MOCK_QUEUE=1`; `MOCK_SEND_NOW=<code>` makes the mock refuse).
  Multiple queued messages in one turn still pair one-to-one after completion, each with its work/reply (#2512 removed merged-bubble completion); sent images are square thumbnails above message (`SentPhotos`, same as pending),
  stripping Claude "[Image #N]" placeholders from bubbles.
- Landing: Unread chats show only the last line of your last input at top, then this turn's work and answer (`landUnderInput`: jump by id, correct measured position; header collapses to slim bar and layout uses that height); other chats, Persona's conversations included (#1666), restore topmost row from last visit (`chat.read.<session>`,
  cleared when leaving at latest; in Persona's conversations only a transcript row is remembered, never a hub feed line or a day line, which may arrive after the transcript on the next launch). Landings within final screen land at bottom and follow new messages (`ChatView.landed`).
  Thus outputs always follow each input whether folded or expanded, no piled-up inputs or numbering.
  - Work row folds everything except final text: tools, thinking (new Claude Code narration uses thinking), system messages (background completion),
    intermediate replies. Tap lists steps inline in order (thin left line), tap same row to fold; Collapse N steps below last avoids scrolling hundreds back.
    Each expanded step remains independent (`ChatLine.workStep` / `.workEnd`), not a huge view. Folding/unfolding preserves viewed content
    (`toggleWork`, no animation): bottom Collapse keeps inverted list's latest end fixed, reply stationary and folded row at old Collapse position;
    title tap keeps title under finger (jump by id, correct measured offset after layout). DEBUG `-showFoldProbe YES` exercises it and writes movements
    to `Documents/fold.txt` (all should be 0).
    Expanding at latest raises title under the bar to reveal steps (#1173), per-frame increments (`ChatPin.rising`, 0.1 s time constant, at most one frame's movement per frame,
    so batched steps / a stalled frame do not jump), no ↓ while rising; collapsing at latest cannot retain title, pauses one frame then glides down (`ChatPin.glide`, about 0.35 s).
    Mac `NSClipView` clamps beyond-latest offsets on layout, so glide manually sets each frame and restores any displacement in the same layout (`ChatPin+Mac.swift`).
    Tapping thinking/tools/BeautifulUI tool-pill groups within open work keeps both title and tapped row still (no borrowing remaining title rise or replacing persistent hold with normal 1.5 s).
    Reduce Motion disables rise/glide and content fades (`Theme.Motion.reduced`, #1123).
  - Background tasks wake idle Claude (`wake` notice): still the same turn, not split. Wake-ups divide it into segments ending with Agent text;
    the answer is the latest segment's text, so the chat always ends on the newest words (`Turn.pickAnswerAcrossWakes`, #3783; it used to be the longest, and a short reply after a wake showed while streaming, then went into the drawer once its item landed: Summary → Build uploaded now ends on Build uploaded, Summary in the work row).
    Everything before the answer (transitional text, wake itself counted as a tool) folds into one work row; after it a small "🔔 Background command finished · <description> ›" strip, not inline expansion,
    opens bottom `WakeDrawer` showing only results: each task notice and Agent reply in order, intermediate tools/thinking/text folded into one final work row by default (requested 2026-10-31: expanding should not unfold N process segments). While still handling a wake, strip says Background task finished · Processing.
    Tasks completing during work (not waking) remain a work step.
    Pitfall: Claude Code's idle-session away recap closes the turn; a later wake must reopen and merge into it,
    keeping recap in place, or post-wake replies show directly (`ChatBlock.build` `lastRaw` / `reopenedAt`).
  - Away recaps resemble background tasks: small "↺ Away recap · 21 min ago ›" strip opens `RecapCard` / `RecapDrawer` with full text. Consecutive recaps with nothing between merge into "↺ Away recap · 3 times · 3 min ago ›", newest first in drawer (`ChatLines.foldRecaps`, requested 2026-10-31); mock `MOCK_RECAPS=1`.
    On returning, if latest recap is the chat's last item and never closed, it expands at the end as a full card (`RecapStrip`, `ChatLines.openRecap`):
    same plane as Agent replies, newest inverted-list row, scrolling away with content and fully visible when following bottom, not above input
    (#1360: footer placement covered the end of replies when expanded and scrolled down). Tap or type to close, reverting to strip; closures stored per session in
    `closedRecaps`. Earlier folded recaps in that row stay as a strip above the full card.
  - Header auto-collapse only follows user drags and their inertial scroll, not programmatic landing deceleration, which otherwise collapses immediately on opening.
  - Work-row text never truncates; insufficient space uses short format with tool count and duration only.
- Can't reach hub banner appears only after 5 continuous seconds (brief network/foreground reconnects finish sooner; previously flashed), disappears immediately on reconnect.
- Layout: list above, input region (interactions / loop / background / queue / composer) below in VStack, no overlay; keyboard reduces page height, latest messages stay against input.
  A 1pt empty view above keeps the scroll view away from top safe area, eliminating its safe areas (inversion puts them at the wrong end) without ignoring them and manually padding.
- Messages from other sessions (`Item.from`): left gray From name card, Markdown body like replies (hub converts teammate JSON notices to Markdown).
- Inverted list (`chatScroll`: flip ScrollView and each row): latest offset 0, entry/follow/↓ jump exactly to 0,
  not estimated lazy heights (cause of occasional blank screens); older history loads at the opposite end without moving reading position. While browsing (`followsEnd` false), new rows are held outside
  the list (`frozenAt`) until returning to latest, preventing displacement. Never use `defaultScrollAnchor(…, for: .sizeChanges)`: inverted it anchors at the far end,
  and height changes move into unrendered blank regions. Inverted safe areas land wrong: ignore vertical safe areas and restore actual insets via `contentMargins`;
  iOS 26 edge blur measures the uninverted view and covers the page, so disable it (`scrollEdgeEffectHidden`).
- Local cache (`StateCache`, Caches/state): machines / sessions / pending interactions / assistant settings (`dev`), Inbox (`inbox-items`, `InboxStore`),
  mail accounts (`mail-accounts`), latest 80 messages per Lark chat (`lark-chat-<id>`). Cold launch, offline and tab switches display cache before silent refresh; no spinner when cached.
- History: turn-based paging, two complete input/answer turns (`turns=2`) on entry and at top, not item count; top spinner auto-loads,
  no Load earlier button. Latest two turns cached locally (`ChatCache`, Caches/chats; strip images beyond 6 MB), shown first on open/switch then merged with server,
  no blank. Turn start/end, own send and returning to Chat tab jump bottom (completion folds and shortens content, otherwise leaving blank).
  Following (`followsEnd`) starts at bottom; any finger drag disables it (history updates never move position); return bottom / ↓ / own send reenables.
  Only following jumps for new content: bottom edge then bottom sentinel id after row layout (`settleAtEnd`), retry until sentinel really visible
  (one id jump into unlaid-out content can land on older input; the "keeps jumping to earlier input" bug came from treating a passed sentinel as no retry).
  Entry positioning runs once, not again after full-screen terminal. LazyVStack estimated heights can land unrendered when row heights vary greatly; `atBottom` starts false,
  so a residual blank screen still shows Return to bottom. Top paging starts only after a manual list drag (LazyVStack may initially lay out top row,
  formerly cascading into loading all history on entry).
- Rendering: each message, timer, narration, count and reply is an independent LazyVStack `ChatLine`; active turns can have hundreds of steps,
  and a single VStack blocks the page with seconds of blankness.
- Cancellation is not an error: leaving/replaced requests (iOS text "Cancelled") never alert (`Error.shown` / `isCancellation`).
- Input: **assistant and Agent chats in the new shell share `ShellComposerBar`** (#1091, every style). `ChatView`’s `Composer` formerly checked only
  `shellChat` (set only by `AssistantView`), so Agents-pushed chats used `ComposerRow` / `PromptComposer` and looked different;
  now `shellChat || shell != nil` (`usesShellComposer`). Agent + menus additionally have Shortcuts (including /clear);
  BeautifulUI Agents therefore lose Prompt Bar @ and / menus (shortcuts in +).
- **Typing / opens commands** (#2747, matches web `AGENT_COMMANDS`): both non-Prompt-Bar composers (`ShellComposerBar`, `ComposerRow`, all styles),
  Agent drafts consisting of / plus a word show `SlashMenu` above input: /继续 (continue), /提交 (commit), /汇报 (report), /compact, /context, /model when selectable, /clear when allowed,
  same web ordering; command and description per row, quick-message original text right; further typing filters command prefixes/descriptions. Tap fills input, does not send
  (/clear uses existing confirmation); an exact inserted command such as /context does not reopen the list until draft changes. Beyond 5½ rows scroll internally.
  Assistant chats have none, like web. In `Composer` VStack, not overlay, because the composer's own hosting controller makes out-of-bounds overlays untappable (#518).
  + Shortcuts unchanged; `-openSlashMenu YES` screenshots, `PersonaUITests/SlashMenuTests` both layouts.
- **iPad regular width (also open Duo) shares `ShellComposerBar`** (#2779): outside new shell (`RootView.usesShell` compact-only),
  no `shell` environment previously fell back to `ComposerRow`, differing from iPhone. `ChatComposerKind.padCard` (regular-width iOS) now makes Agent, assistant chats
  (`shellChat`) and assistant new chats (`AssistantHome` → `StarterComposer(shell:)`, mode chips in model menu) use it, centered within `ChatView.readableWidth`.
- **Long drafts expand into a full-page editor** (#3167, like ChatGPT): once `ShellComposerBar` text reaches 3 lines (wrapped draft height relative to one line at the input width, not the input's own height, which quotes and the keyboard may compress below 3 lines), an Expand button appears beside + in the card's button row
  (accessibility label Expand input). It opens a full-page sheet (`ComposerPageEditor`, `.page` sizing on iPad, text limited to `ChatView.readableWidth`), with Collapse left and Send right (Interrupt when the Agent is working and the draft is empty),
  and quotes above the text. It edits the same draft (`text` binding, `model.drafts` still the sole source); pending images and files remain in the composer below and send with the message as usual.
  The page owns its cursor (takes the caller's `selection` on opening, returns it on closing; directly binding to the caller sent every cursor update through it with delayed returns, occasionally losing characters on iPad).
  While open, the composer's own field binds to an idle `parkedSelection`, so it cannot move the caller's cursor underneath the sheet. Focus bindings cannot cross into sheets, so the page has its own `@FocusState`:
  requests to focus the composer while open (uploaded file paths via `PathInsert.edit`, ⌘I) forward to the page, without showing a keyboard underneath. The composer is not treated as editing then;
  paths append to the draft, and the page cursor follows them (only when the caller's cursor changed; ⌘I alone does not move it).
  Collapsing, swiping down or replacing the page (chat switch, tree replacement) first commits marked input (`TypedText.commitFocused`, #2203); returning to the composer preserves keyboard and cursor positions.
  Sending from the page (toolbar ⌘⏎ shortcut) is identical to composer Send and happens once; the composer's ⌘⏎ is disabled while the page is open, preventing another send underneath. Hidden line measurement lays out at most 4 lines from the first 600 draft characters, counts trailing newlines, and stops while the page is open.
  The button belongs in the button row, not over the input corner: a corner overlay either covers text or rewraps it when appearing. iOS only; Mac unchanged.
  `ComposerExpandTests` covers assistant / Agent, keyboard opening/closing, paragraph wrapping, cursor transfer both ways, swipe dismissal preserving edits, one hub send with quotes; runs on iPhone / iPad / Duo, landscape with `TEST_RUNNER_PERSONA_ORIENTATION=landscape`. On an unfolded portrait Duo, XCUITest treats the screen as upside-down portrait and synthetic drags miss; `TEST_RUNNER_PERSONA_NO_DRAG=1` skips keyboard dismissal and swipe-down. ⌘⏎ tests require `TEST_RUNNER_PERSONA_HARDWARE_KEYS=1`: Xcode 27.2 + iOS 26.5 simulator delivers synthetic ordinary characters but does not trigger shortcuts (including the composer's existing ⌘⏎).
- Mac and Vision retain `ComposerRow`: Agent and new assistant chats share it (+, glass capsule input, Send, red Interrupt while working and empty,
  no dictation), with permission/effort capsules above (`SessionModeBar`).
- **One dictation and stop button** (#1079): `DesignSystem/ComposerButtons.swift` `ComposerMicButton` (outline mic, solid red pulsing while dictating),
  `ComposerRoundButton.stop` (stop in solid ink circle), 44pt tap target, one shell scaling step (`shellScaled`), used by `ShellComposerBar` and iOS `PromptComposer`.
- BeautifulUI style (Settings › Appearance › Style, `AppStyle.beautiful`, 2026-10-30) uses `PromptComposer` (new assistant `BeautifulStarterComposer`),
  BeautifulUI Prompt Bar (`layout: .stacked`); other styles unchanged above (CLAUDE.md: new appearance as new theme):
  rounded multiline input, toolbar below: + (Photos / Camera / Files), @, /, model, dictation, Send right; working Agent with empty text gets Interrupt.
  Background current style `.surface` (library draws none), buttons 34pt. iOS uses shared dictation/stop: library microphone off (`showsDictation = false`),
  no `onStop`; overlay shared button at library Send. Stop uses even-odd `SendCutout` masking library Send, extending far out
  so menus above input are not clipped (library unchanged, `apple/Packages/BeautifulUI/VENDORED.md`). Pending images `ComposerImages`; drafts, send safety and uploads remain in each composer.
  - Drafts: directly edit `model.drafts` (`Options.text`), no reciprocal bar/host writes that prematurely commit Pinyin marked text.
    Before sending, commit marked input and update draft from text view (`TypedText.commitFocused`, all three composers, #2203: WeChat keyboard sent only half).
    Quotes (`composerContexts`) persist like text in StateCache (`composer-contexts`), surviving App termination; Agents row metadata gets Draft for text or quotes (#4578,
    `AppModel.draftMarks`, changes only when a session gains/loses draft, no row redraw while typing).
  - Paste images (#4573): clipboard image + focused input shows system Paste above (`PasteImageChip`, SwiftUI `PasteButton`); tap adds pending image,
    itself authorizes paste, no Allow paste prompt. Only `hasImages` checks presence, no content reads; after tap or × same copy does not show again. Chats and both assistant starter composers supported.
  - @: recently accessed tool files (12, `@relative/path`, understood by Claude Code and Codex), then recent 8 sessions, selected as
    `<persona-context>` before message, quote capsule above input. Only loaded-step files; folded work never expanded in long chats yields none.
  - /: `SlashCommand.all`, quick words /继续 (continue), /提交 (commit), /汇报 (report), fills request text for explicit Send (formerly menu sent immediately);
    /compact and /context unchanged; /clear when supported, confirmation first.
  - /loop (#2601, web #1600): Claude Agents only, not Codex, internal Codex fork or assistant (`LoopCommand.offered`), in / and + Shortcuts;
    prefixes existing input with `/loop ` (existing text becomes task, cursor after command to enter interval), never sends directly (other + shortcuts still send on tap). A `/loop` draft shows shared `LoopDraftNote`
    explaining "Repeat every 10 minutes: Check CI" / "Repeat at Claude's chosen pace: …", gray usage when task absent;
    PersonaKit `LoopCommand` matches `web/src/lib/loop-command.ts`. Screenshot `-showLoopDraft "/loop 10m Check CI"`.
  - Model: Claude Code `/model` aliases (Default / Opus / Sonnet / Haiku), selection sends `/model <alias>`; absent for Codex, internal Codex fork and headless assistant.
    Assistant home uses launcher list (former model label moved here).
  - Dictation: `Dictation` (`SFSpeechRecognizer` Chinese + `AVAudioEngine`), live recognized text appended to draft; tap again or send stops.
  - Permission/effort above input (`SessionModeBar` / `NewSessionModePicker`) remain, not merged into toolbar; rationale PLAN PromptBar v2.
  - DEBUG `-showPromptMenu mention|command|attach|model` opens after a few seconds for screenshots.
- Model API errors (`APIErrorCard`): Claude Code's final "API Error: 502 …" after retries and hub "API 错误：…" ("API error") notices become error cards,
  not raw body text. First line plain-language cause (`APIErrorInfo.explain`, e.g. "Inference service has no available GPU" / "Login expired; run /login on the computer"),
  then BeautifulUI chips: error-styled status, Temporary failure / Needs attention, consecutive count. Open request ID, session, time and original, all copyable.
  Repeated identical errors separated only by Continue-like retries merge with count (`ChatView.foldAPIErrors`). Retry (sends "继续", "Continue") only when last item, idle Agent and transient.
  Currently text parsing; Claude Code also provides `apiErrorStatus`, `error`, `requestId` fields, preferred once hub reads them. DEBUG `-showErrorDetails YES` opens by default.
  Screenshots mock `MOCK_API_ERRORS=1`, assistant main and review chats.
- Claude suggestions (`SuggestionPill`): session `suggestion`, not working, empty input shows "✦ Suggestion … Use" above (AI purple, not accent), also placeholder;
  tap fills editable input, long-press Send now. Typing hides it. Headless assistant has no TUI/suggestions; mock `MOCK_SUGGESTION=1`, review session.
- Sending clears input immediately and displays Sending bubble (`ChatStore.pending`), request background; same text in transcript
  or queue replaces it, failure red with retry/delete. No waiting on network anywhere.
- Sent images: hub appends paths in `<attached_images>`, App parses square thumbnails above text (file API from session computer,
  memory cache), tap full viewer; tag hidden (legacy "相关图片：…", "Related images", / "（我从手机发来了图片…）", "I sent images from my phone", supported).
- Monospace (files, diffs, terminal, `MonoTextView`): non-scrolling UITextView measured to longest line inside UIScrollView, no wrapping, both-axis scroll and selection.
- Long-press messages (`MessageMenu.swift`): dark WeChat-style action strip above press (Copy / Select text / Quote / Share, own also Edit),
  no lifted message; below if near navigation bar. Tap anywhere or finger-scroll closes (programmatic content-flow scrolling does not).
  Copy whole message; Select text (#3189, formerly Select misleading beside Copy like multi-selection) opens freely selectable text, system Copy only selection, top-right Copy all original. Replies use `SelectableMarkdown`: bold/italic/code/links styled without markers, headings bold, list bullets with indentation/tabs, fenced code monospace indented without fence (closing plain fence same character and at least opener length; ``` inside ````md does not close early); math `$…$`, `\(…\)`, `$$`, four-space code, rules `* * *` / `---`, tables and quotes keep source. Own messages stay plain like bubbles, no Markdown (preserves `__init__.py`, `2*3*4`). Follows Dynamic Type live. `SelectTextTests`, `SelectableMarkdownTests`; Quote adds the message to the composer as a quote; in Select Text, Quote (first in the edit menu, #4549) quotes only the picked words as `part="selection"`, still linked to the message; Edit restores old own message.
  Action icons fixed 22pt high (SF Symbols differ, otherwise text misaligns).
  A message of yours not in the transcript yet (sending, waiting for the network, failed: `PendingBubble`) has the strip too, with Copy / Select text / Share (#6427), so one the computer refused can be copied rather than typed again;
  no Quote (also not by swiping or from Select text: it may never reach the agent), no Edit (a waiting one would still go out besides the edited one), no Rewind. `PendingBubbleMenuTests` (the `SendSafetyTests` mock: refused, Not Now, long-press, Copy, paste into the composer).
  - Rewind here (#2915, web #2436, `Rewind.swift`): Claude Agent own transcript message action adds Rewind,
    chooses Chat only / Code and chat, latter confirms files too. Idle only; working/waiting/not running/non-Claude explains why;
    absent for assistant/sub-Agents. App sends original text (fetch full if truncated), reverse occurrence index of identical text and count after it (`POST …/rewind`),
    node checks screen then /rewind; success notice and restores message into App input if Claude input cleared, failures explain (old hub, full-permission credential required, node refusal).
    Rewound turns refetch on `history` change. Six buttons divide chat width when needed. Mock `MOCK_REWIND=1`,
    review session, `-showRewind bar|ask|run` (bar / picker / directly rewind chat only).
  - Why Select text opens a page: MarkdownUI messages are groups of SwiftUI Text blocks (paragraphs/lists/tables); on iOS Text supports
    whole-block copying, not draggable partial or cross-block selection. Inline selection would require UITextView relayout and lose table/code styles. Technical limitation: keep the separate text page.
  - Share (`ShareCard.swift`): render a card (gradient background + white/dark card: Claude or Me, source "app › leader", time, body,
    Persona mark); same chrome as image viewer (#4714): circular Close · Light / Dark capsule above, circular Share image (system share) ·
    Save (Photos, add-only permission) / Copy image capsule · circular Share text below; card scrolls behind both bars.
    `ImageRenderer` cannot draw ScrollView: separate `.card` Markdown theme wraps code and prevents horizontal table scrolling, otherwise tables and everything after turn blank;
    set `proposedSize` width or height is measured at another width, leaving a large blank bottom. Truncate beyond 3000 characters.
- Agents chat top bar: ① Back / title + subtitle / ⋯; subtitle "● State (working duration in minutes) · Machine · Space › Tab" (since 2026-10-31 calm Seen/Not running states show only dot; tap title opens bottom Session info sheet with Run / Usage / Location, copy id/resume command, swipe down or tap above closes, former under-title pullout removed; Usage below context tokens has "Context remaining · N% left" and thin bar (#3289, web #2430, `ContextUsage.swift`: `context_tokens` / `context_window`, live session pushes, warning at ≥80% used, hidden for unknown window)); state text state-colored,
  omit machine first then location if crowded; the title is one line cut at its end and uses the full width between the buttons (at most `Theme.Size.headerTitle` on wide screens);
  ② second row only for other same-space Agents or loop/background: the tabs take the remaining width on the left, one activity pill sits at the trailing end (`ActivityChips`, trailing also without tabs): neutral fill, as tall as the tabs,
  tinted icons (loop `Theme.loop`, background `Theme.busy`, the folded bar's marks), "⟳ 13:24 │ ⚙ 1" with the next time and count only (VoiceOver reads it in full); at the largest text sizes with both present the loop is only its icon; tap for details.
  Goal, workflow and port rows are unchanged (phase 1, `docs/archive/apple/plan-chat-topbar.md` Q1); ③ at most one status line (`HeaderStatusLine`, `chat.status-line`): while reading a sub-Agent "👁 Viewing sub-Agent · title (read-only)",
  the way back stays the footer's Return to main Agent (`SubagentFooter`); otherwise one running "spinner Sub-Agent title ›" opens it, several "spinner N sub-Agents running · newest title ›" open All sub-Agents;
  no line when none runs (ended / nested ones from the work row's View sub-Agent and ⋯ › All sub-Agents (N)). The Main Agent / sub-Agent strip remains only on the Mac and in hosted headers (#3255).
  Formerly title, companion bar, tabs and activity each had a row. Files/Terminal/Git in top-right ⋯; Files ⋯ › Return to chat; collapsed slim bar has Back, location, small loop/background marks.
  Former left-swipe Files became left-swipe Quote 2026-11-04 (#1058); that day Files first had a standalone folder left of ⋯, then moved into menu at user request (#1205).
- Header auto-collapse (Agents, like Safari toolbar): custom
  `ChromeHeader` (system navigation hidden), floating above list; **finger tracking**: drag toward newer messages slides bar upward by same distance and fades,
  slim Back + "● app › leader · Chat" fades in during latter half; dragging toward history restores it. Release never stays halfway: collapse at latest,
  otherwise snap nearest end (`.smooth`, 0.3 s). Following new content also collapses after user has touched list; tap slim bar / switch tabs expands.
  After 3 idle seconds (`ChatView.chromeIdleSeconds`: no list/header touch, info card closed, no sheet/menu), auto-collapse; header interaction (swiping Agents), expand and tab switch reset timer.
  Invisible layer excluded from accessibility: expanded has no slim bar, collapsed hides full layer while keeping measurement, VoiceOver/tests find one Back (`ChatHeaderTests`).
  - Smooth because collapse lives in separate `@Observable` `ChatChrome`, redrawing only header per frame; floating above fixed-frame list (header-height margins only at oldest end),
    so messages never move a pixel. Former toggle animated system navigation plus two labels independently and changed list height, abrupt.
  - User tracking/interacting and immediately subsequent decelerating only (`onScrollPhaseChange`), not keyboard-driven offset changes.
  - Hidden system navigation disables UIKit edge Back; `SwipeBack.swift` takes `interactivePopGestureRecognizer` delegate to reenable.
- Queued messages: messages sent while Claude works are outside transcript, in Queued above input; each Recall and edit / Copy / Delete.
  Like TUI: ↑ pulls all back to input (transcript popAll), after clearing input requeue kept messages (`POST …/queue {"keep":[…]}`).
- Development list: busy spaces show only Needs you / Working / Unread Agents, idle folded into N more idle, tap expands.
- Return-to-bottom arrow: bottom sentinel determines visibility.
- Terminal/Files/Git in development chats (`Views/Session/`): **no tabs** (requested 2026-10-30: remove Chat · Terminal · Git · Files).
  Terminal in top-right ⋯ (requested 2026-10-31: remove standalone button, also collapsed bar); active work row still has terminal. Files/Git by **left swipe** (Chat → Files → Git, right back),
  or ⋯ Files / Git (change count); Back in Files/Git first returns to chat. Git only in repos, Files when cwd known; absent for assistant.
  - Terminal (`TerminalPanel`, `Views/Session/Terminal/`, `docs/integrations/terminal.md`): view in any state. Button opens **full-page cover from bottom** (`TerminalCover`, requested 2026-10-30), no title/tabs, slim top bar (size left, handle center, settings right),
    drag bar down follows finger, closes past 140pt or fast swipe (handle tap too); terminal size changes only with keyboard, not header (resize changes controlled pane, requiring Claude relayout).
    Moshi-style bottom key bar: low dark capsule, flat 30pt keys, small monospace Ctrl/Esc/Tab, others icons, preserving terminal height. With `terminal_stream`, subscribe live
    (`TerminalFeed`: herdr observe frames applied to pane-sized `TerminalGrid`, dropped frame/new stream restarts from full frame, reconnect with epoch/seq);
    without hub/node/herdr streaming, fetch colored screen each second (`format=ansi`).
    Default Phone size (Settings › Size): with `terminal_control`, control at phone columns×rows, Claude lays out to phone width and keys write directly;
    header Phone size 45×29, occupied asks Take over / View only, lost control shows View only · Take over. Leaving tab/background/disconnect restores computer size (`docs/integrations/terminal.md` section 11). `TerminalView` draws each character in its grid
    (wide characters two cells), box/block characters as lines/rectangles, keeping Chinese/boxes aligned; draw only through last text/cursor row. Wrap to phone width by default,
    top-right Wrap / No wrap · Swipe horizontally (`@AppStorage("terminalWrap")`), light/dark `TerminalTheme` Persona / Solarized / Midnight,
    `terminalFontSize`. Long-press word, drag selection, Copy / Select all; OSC 8 and plain URLs open in App.
    Near top, load older 200-line colored chunks incrementally,
    to herdr's earliest retained point. Locate each chunk by current top 20 lines + loaded count in server buffer (`POST …/screen/before`): Claude redraw duplicates screens in history, choose match nearest loaded-count-from-end; continuing output does not misalign.
    History freezes screen until Return to latest (no history, e.g. fullscreen Claude, does not freeze). herdr retains about 1000 lines/pane (`pane read` caps regardless of --lines, no offsets/scroll API),
    at limit says "herdr keeps only about 1000 recent lines; earlier work is in Chat", whose transcript is complete. Card layout shows this top-left; `TerminalCover` has no space, places above Return to latest (#1133). New Mac Claude fullscreen has no pane scrollback.
    Unreachable hub, stopped stream, failed screen read or over 4 seconds without fresh screen shows `TerminalLinkStatus` atop: Last update N sec ago + Retry in N sec · Reason + Retry;
    backoff 1/2/4/8 seconds then every 15, disappears on recovery. Old content must not look live.
    `TerminalKeyBar` ctrl/alt/⇧ like Moshi: single sticky for next key, fast double locks (tap unlocks, filled key with horizontal line),
    held while pressed (release without another key makes sticky); modifiers affect input typing too, next character immediately sent as chord
    (ctrl,c → `ctrl+c`; shift-only uppercase), input outline/placeholder changes; Send's Enter includes modifiers (shift+enter newline in Claude).
    Then esc/tab/direct ⌃C/gamepad opening arrow+Enter cross; arrows use modifiers too.
    Controls: pinch font 7–20 stored `terminalFontSize`; two-finger horizontal swipe previous/next same-space Agent (herdr tab), vertical adjacent same-computer space;
    double-tap terminal pastes clipboard without Enter, multiline confirmation; while controlling, hold key-bar Paste for photo/file, upload to pane computer then type path (`docs/integrations/terminal.md` section 18 "iOS / Mac"); left input lightning shortcuts (tap fills, double sends, `terminalShortcuts`);
    top-right controls opens Terminal settings: shortcuts, visible keys (ctrl always), Hide key bar when not typing, colors/font/wrap.
    Typing completion from commands previously typed after prompts (`$ `, `❯ `, `➜ `…), visible paths/names, **pane content only, no computer shell history**.
    Dictation uses system keyboard microphone, no App voice input. Key names follow herdr `send-keys`,
    order ctrl+alt+shift+key, herdr encodes (ctrl+shift+a CSI u), unknown names reject entire chord (`invalid_key`); serial sends preserve press order.
    Live-status terminal icon opens full-screen version of same panel.
  - Git (`GitPane`): branch, upstream ↑↓, staged count; changes (status letter, +/− lines, staged/partially staged), diff with old/new numbers and red/green, no wrap, horizontal scroll,
    staged/unstaged switch when both; paged history, commit message and per-file diffs.
    Branch-card Pull N fetches latest upstream in one tap, retained as Pull when synced for active remote checks; disable repeat taps while running, refresh status/files/history and show result.
    Clean-worktree fast-forward only, no auto stash/merge/rebase; existing stashes unchanged; explain no upstream/detached HEAD/uncommitted changes/divergence. Other Git views stay read-only.
  - Files (`FilesPane`): descend from session cwd, breadcrumbs jump levels, directories first, size/modification/Git flags (dot for changes within directory),
    hidden files off by default, menu toggle; tap file viewer.
    The machine page's Files row (below Agents, disabled when offline) opens that computer's home in the same pane (`cwd` is `~`).
  - **Upload, Save to Files** (#4858, `Views/Session/FolderTransfers.swift`, interface in `docs/protocol/protocol.md` "Changing files"): the upload button right of the breadcrumbs offers Choose from Files or Choose from Photos (several at once; photos are saved as files in their original format and name),
    into the current folder under their own names, up to 2 GB; one after another, a row with progress and ✕ per file under the list. A taken name first asks Replace / Keep Both (Keep Both saves "name 2") before anything is sent;
    a folder that cannot be written (outside the home, `~/.persona`, `~/.persona`, `~/Library`), an outdated personad on the computer, and files over the cap each get a one-line reason. Leaving the page lets uploads finish; a pending name question counts as cancel.
    A file's long-press menu (also a VoiceOver action) has Save to Files: downloads to a temporary folder with progress, then the system's location picker; the temporary file is removed either way.
  - **Rename, move, move to the trash** (#315, `Views/Session/FolderEdits.swift`): a file's or folder's long-press menu (and VoiceOver actions) adds Rename (an alert with the name, refused names said before asking),
    Move (a sheet with the browser's folders from its top, opened on the current one; Move Here is off for the folder they are already in, and the folders being moved can't be opened), and Move to Trash (asks first).
    ⋯ › Select turns the list into checkmarks with a bar under it: the count, Move, and the trash button; Done ends it. A trash toast has Undo (puts it back through `files/restore`); there is no permanent delete.
    A move to a taken name asks Replace (the one there goes to the trash) / Keep Both. These show only when hello's `features` has `file_edit` (or the hub sends none).
    On an iPad, files dragged in from another app (Files, Photos) upload into the folder shown, with an accent outline while they are over it.
  - **Auto-refresh** (#3089, web #2439, `App/FsLive.swift`, `Views/Session/FsLiveModifier.swift`, `docs/protocol/protocol.md` ("Automatic file refresh")):
    foreground Files/Git sends WebSocket `fs_watch` (current directory/Git marks, Git state; same cwd combined),
    `fs_changed` debounced 200 ms then reread in place without loading animation (retain older loaded commits if latest unchanged); unsubscribe off foreground (`.onTabVisibility`),
    reread on return and re-send/reread on socket reconnect. Old hubs/nodes lacking `fs_watch` keep pull-to-refresh / ⌘R. Open diffs/file viewers do not refresh yet. Mac shares panels.
- Loop (`/loop`): above input "Looping · Every 30 minutes · Next wake 11:56 (in 4 minutes)", tap task, confirmed Stop loop sends stop;
  each wake is "⟳ Loop wake 21:11 …" row, not user bubble.
- Sub-Agents: Agent-tool children no longer count background tasks; on iPhone / iPad the header's third row is a status line (see the top bar's ③ above); on the Mac and in hosted headers, below chat tabs Main Agent / running sub-Agents (spinner, row if children within a day; completed not chips, View sub-Agent in work), tap same
  message/tool UI read-only, footer "Sub-Agent · Explore · Running, read-only · Return to main Agent"; View sub-Agent below each Agent call.
  Ended/nested children outside row add All sub-Agents N (#1559, web; in ⋯ on iPhone / iPad) at end, bottom tree sheet (`Subagent.tree`, same `subagentTree` sort:
  `parent_id` indentation, missing parents top-level; running first then newest start within level); state dot/description/Running or Finished/type (forks labeled Fork)/start age, tap read-only.
  Mock `MOCK_CHAT_LINES=1`, `-openSession <machine>/a1f3c2d4-0000-4000-8000-0000000c1e01` for screenshots.
  Agents with running children show "👥 Sub-Agents N", not idle. Id `<main session>@<agent id>`, history/live/paging identical to normal.
  Running or finished goes by the chat's own lines (`Subagent.running(in:)`, #5602, as Claude Desktop): done once its Agent call's result is in (whatever the hub's list says; one dropped from the list kept Running for ever), one sent to the background once its notice came; the list's word only until then or when the call is not loaded. A running child's write time and last line are no change for its session's page (`Session.sameForItsPage`), and a tool run whose first call goes to the background keeps its id (an opened run stays open). Task-notice and long-notice rows keep their open state in the chat's `OpenTools`.
  In a work line each Agent / Task call is its subagent's card (`SubagentCard`, #5602, as the web's and Claude Desktop's): its description; under it, while it runs, what it is doing (`last_tool.summary`, else its name, "Starting…" before its first call) and its steps, once over Done / Failed / Stopped · N steps · time (a background one's time runs to its last line, not its receipt). Open: what it was asked, its report (a background one's: its notice's detail), its latest three tool calls as they come (`SubagentRecentSteps`: the card subscribes to the subagent's own conversation while open, as the web's `RecentSteps`; the hub replays a run folded, so the card opens its newest fold when it holds at most 40 calls and fewer than three follow it, a longer one is not fetched for three rows), View Subagent. `-showSubagentDetails YES` opens every card for screenshots; on the mock, `GET /mock/subagent-step` (`MOCK_SUBAGENT_CARDS=1`) moves the background one a step on. A card never folds into a run of chips. While it runs its card stays in view under its folded work line (`ChatLine.agentOut`, same id as its step, so opening the line does not draw it twice); done, it folds with the line. A foreground one with no result stays out only while its line runs. Subagents launched one after another share one line (`SubagentGrid`): "Running 3 agents" while any runs, "Ran 3 agents · 12 steps" after, their cards in a grid two side by side from about 540 pt (iPad, Mac; `Theme.Size.subagentColumn`), one column on a phone; folded, the running ones of a grid stay out together. While the agent waits on subagents it launched in the foreground (running, call unanswered), the running work line says so instead of its counts: "Working 6m · 2 subagents working — <what the newest does>… · +1 more" (`AgentsWorking`, the web's running bar), the count alone where the line is narrow. The plan (TodoWrite) is one line in its work: "Update To-dos  Step 3 of 4 · <the step it is on>" over a thin bar of how far along it is (`PlanBar`); a tap opens the plan sheet (`PlanSheet`), which shows the chat's newest plan whole and follows it. While that plan's line is not on screen (scrolled away, its work line folded, or folded by the hub, whose `fold.tasks` keeps the list), a pill over the composer says "Step 3 of 4" (`PlanPill`) and opens the sheet. A plan's call never joins a run of chips. A finished subagent's report ending in its `agentId` is not a background receipt (it read as Background · Running).
  A subagent's report reads as one line until opened (`FoldedReport`): its first line when that is a heading, else its first 80 characters, and Expand; open, the whole of it and Collapse. In the card, in a background subagent's notice, and in a message from another session that is a subagent of this one handing its report back (its name is the subagent's id, type or description; an older node shows a handback so). Open or not is kept by the card's id on disk (`OpenReports`, StateCache, the latest 400); `-openChatsFresh` folds them all again. A report of one line of 80 characters or fewer is not folded.
- Background workflows (#2935, `workflows` in `docs/protocol/protocol.md`): Claude Workflow tool multi-Agent background scripts keep Agent Working after turn ends while waiting for completion,
  `state` still drives colors/count/watch/to-dos; App label Working (background) (`Session.stateLabel`);
  Agents rows/to-do cards add `WorkflowLine` below last text: "Background workflow ‘parity-audit’ · Verify 2/2 · 169/185+ · 14 min" (multiple add Waiting for N). Counts read done / started with `+` while the run runs (the script starts more as it goes), without it once it ended (`WorkflowRun.counts`). In a narrow row only the name is cut (`lineName`, tail); the phase, counts and time after it stay whole (cut in the middle of the whole line, 169/185+ read 9/185+).
  - **The run's card in the chat** (#3436, the web's `WorkflowCallCard` of #3434): the Workflow call that launched a run (`tool_use_id`) is drawn as `WorkflowCallCard` in `WorkStepView` instead of a tool row. `WorkStepView` is `Equatable` on its item, so the card reads its run itself: `AppModel.workflow(_:call:)` keeps one observable cell per (session, call), set only when that run changes (a push about another run or the rest of the session draws nothing). Head: mark, name, status chip, `169/185+`, a live clock (`Text(timerInterval:)`, ended: how long it took); under it "Now Verify: verify:island" while it runs, its summary (and a failed / stopped run's error) once ended; opened, the progress bar and the phase blocks. Open while it runs, folded to its head once ended; the reader's choice stays in the chat's `OpenTools`.
    While the hub lists the run (running or ended, a day), the call never folds into BeautifulUI's chips and stays in view under its folded work line (`WorkflowCallCard.staysOut`, `ChatLine.agentOut`; `LinesKey.workflowCalls`). A chat opened while a run goes on has that turn folded by the hub (`fold_work`): the fold whose `[ts, until]` holds the run's `started_at` is fetched so the card comes out (`WorkflowRun.foldsHoldingCalls`); one that did not come (hub restarting, offline, not connected) is asked for again after a wait that doubles from 5 s to a minute (`WorkflowCallCard.fetchFolds`, the web's `useWorkflowFolds`), so the card appears without leaving the chat. No such run (older hub, a run over a day old): the call's row as before. The tool's verb / symbol / subject: Run Workflow, `Theme.Icon.workflow`, its `name` (else the one in its script's meta, else its script file's name without `.js` and the `-wf_<run>` Claude Code adds, as the node's `scriptName`).
  - **Phase blocks** (`WorkflowPhaseList`, shared by the chat card and the header): one block per phase (`phase_stats`; an older hub's are counted from the listed agents, declared phases first, then undeclared, then the agents without one), its head `done/started` with `+` while more may start there, lit (`Theme.aiTint`) where the run is; its mark a full AI dot there, a half-strength one (`Theme.aiMuted`) for a block with agents still running that the run moved past, a ring before a phase starts. A block with agents running (or the run in it) is open, one whose agents are all back folds by itself; the reader's tap wins (kept by the block's place and title). In the chat its head, 「n more」 and Show Fewer change what is under the head with the head held in place (`toggleInPlace` + `chatPinned`: the list is upside down, and 12 rows opening under a head pushed it up under the top bar). A phase back with failures gets a red ⚠ instead of ✓, and "n failed".
    Agents in the order they started (never reordered), at most 12 quiet rows a block, running rows always shown (#3434: a running row never folds, so a phase with 60 running lists 60), the quiet rest folded into "n more" where they are left out; a block the hub's newest-60 cap cut into says "n earlier ones not listed" and shows its last 12 rows. A row: a dot (working amber, back gray, failed red, stopped faint), its label, its time to the minute in secondary (tertiary read under 2:1 on white; so do the run's clock and "n earlier ones not listed") (a running one's moves on once a minute, `TimelineView(.periodic)` from its own start); on iPad and Mac also its model and latest tool. A tap opens its conversation read-only like a subagent; on the Mac the pointer over it says Open.
    The progress bar is `MeterBar` in parts (done, running, failed `agents_failed`, stopped once ended), `Theme.Size.bar` thick, with a legend.
  - **Motion only on change** (none on the first draw, none with Reduce Motion, nothing repeating, no spinner): counts `.contentTransition(.numericText(value:))` with `Theme.Motion.state`; the lit phase moves on with `matchedGeometryEffect` and `Theme.Motion.layout`; a new agent's row fades in (`.transition(.opacity)`, `Theme.Motion.reveal`); the status chip and an agent's dot change color with `state`. The run's time is `Text(timerInterval:)`, which the system ticks without drawing the card again; the agents' rows tick once a minute, all on one shared minute, not each second: on the Mac, 60 running rows each with a second clock kept the idle chat at 7.8% CPU (Debug, `ps`, 20 s). With minute rows, over 90 s (a minute tick included): 16 running rows 0.84%, 61 running rows (`MOCK_WORKFLOW=1`, `?step=5`) 0.82%, a chat without a workflow 0.25–0.30%; the difference is the run's one second clock (`docs/lessons/ios.md`). FrameStats needs an unlocked Mac (display links stop while it is locked) and has not been run. No `TimelineView(.animation)` anywhere.
  - **The chat header** (expanded, side, Mac top) has Background workflows above sub-Agents / the status line (`WorkflowSection`): a running `WorkflowCard` shows the same head, "now" line and progress bar, a tap opens the same phase blocks. Ended within the hour below as `WorkflowEndedRow`: name / done / started / time / end state, error line for failed / stopped, a tap opens its progress and phases (the same › turned down as everywhere else; an ended run's time reads "14 min" in the header, the card and the rows alike). Opening and closing them is at once with Reduce Motion. Whole section capped at 38% chat height (`Theme.Size.workflowShare`, #4532), scrolling inside so the chat stays. Screenshot `MOCK_WORKFLOW=crowded` (3 workflows, 10 running Agents) runs `testCrowdedWorkflowsLeaveRoomForTheChat`.
  - Watch: a tiny "workflow 169/185" chip in list / chat, its count rolling when either number changes (`numericText`, watchOS 10) and nothing else moving.
  Background N excludes `background` kind `workflow` (`Session.backgroundCount`); Live Activities unchanged, state already working.
  Lenient parsing: old hubs no `workflows` unchanged, unknown status complete, one bad entry (a run, an agent, a phase's stats) does not lose the rest (`WorkflowTests`, `WorkflowCardTests`). Mock `MOCK_WORKFLOW=1` (with `GET /mock/workflow?step=0..5`: 169→170, a new agent, Audit back with a failure, completed; 5: all 60 listed agents running) runs `PersonaUITests/WorkflowUITests`. Watch screenshots of one session: `-openWatchItem session:<session id>`.
- Background tasks: `run_in_background` commands/subagents not yet reported show N background tasks above input even after turn end, expandable description/command/elapsed,
  tap opens output file.
- Bottom tab bar persistent, no scroll collapse.
- Live status: working Claude spinner "Cogitating… 12s · ↓3.2k", tap output preview, terminal right.
- Terminal needs you (AskUserQuestion/permissions not caught by hooks, `ScreenForm`): parse screen dialog (`ScreenParser.dialog`: ☐ heading, │ quoted question,
  numbered choices and indented descriptions, "Type something.", "Chat about this"), render like question card titled Terminal has a question; choice sends number,
  Other answer… first number for Type something then text+Enter (`Resolution.preKeys`), Chat instead sends Chat about this number; raw terminal,
  keys/input under View terminal; unparseable dialog shows terminal directly. DEBUG `-showSampleScreen YES`.
- Interaction panel above input: questions (`QuestionForm`, iOS grouped list, question + rounded choice group, terminal-style numbered circles become checks, "(Recommended)" shown as Recommended badge but original sent; Other answer… last row becomes input on tap; single-question single-choice submits immediately, multiple title "Heading · 1/2", small Previous / Next / Submit below, auto-next after single choice; `-showSampleQuestion YES`), plans (Markdown + full screen, Approve and run automatically /
  Approve and auto-accept edits / Request changes with feedback), permissions (Allow / Deny / Always allow this session), terminal (screen + parsed choices + keys + text).
- Questions the assistant sees first (#544, iOS and Mac; `docs/assistant/assistant.md` "Assistant answers questions first", the web's rules in `docs/web/web.md`; `Shared/Delegated.swift`, `Views/Interactions/DelegateViews.swift`):
  a card with `delegating: true` shows **Assistant is looking** above its questions and can still be answered (the first answer wins). It gets no haptic or notification
  (`Delegated.announces`; Mac `MacAttention.lookingOnly`, watch `WatchInteractions`), and rings like a new card when the hub sends it again without `delegating`. The hub still counts it as pending, so lists and badges show Needs you meanwhile.
  `delegate` is shown as **What the assistant found** (What / Why now / Recommended / If not / Why you decide / Deadline, empty ones left out); a `recommend` that is a choice's own label (several questions joined with `；`) tags that choice **Assistant's pick** instead.
  `interaction_resolved` with `answered_by: "assistant"` leaves an **Answered by the assistant** card above the input (answer, reason, close button; the latest 50 are kept in memory), also when this device's own answer came second (the hub's `already` reply).
  **Change Answer** (sessions that take messages only) opens a sheet with each question's choices and Other answer…, sends only the questions you changed to `POST /api/v1/interactions/{id}/reanswer`, and `interaction_reanswered` marks it **You changed it** with the assistant's answer.
  Session info lists the session's questions the assistant answered (`GET /api/v1/interactions/history`, newest first); a hub without the route shows none. The history keeps the assistant's answer, so a change shows only on devices that saw it.
  Both requests name the card's or session's `manager` as `?manager=` (the question is no longer pending, so the hub routes by the manager named; docs/protocol/protocol.md "Per-manager routing").
  DEBUG `-openChangeAnswer YES` opens the sheet of the first answered card; mock `MOCK_DELEGATE=1` (`docs/operations/testing.md`).
- Codex / internal Codex fork cards (`agent`, `docs/integrations/codex.md` ("Questions and approvals")): Codex has a question / Codex needs permission, same `QuestionForm`;
  `AgentPermissionForm` shows reason/full command or changes/Deny/Approve, Always approve · Stop asking for commands beginning with … (`always_label`, absent hides),
  Deny with reason… opens input (stops turn and sends reason next message). Claude card unchanged; inability to type (computer draft etc.) keeps card,
  toast explains. BeautifulUI `BUIPermissionForm` asks Approve Bash?, Approve / Always approve (scope explained) / Deny,
  Codex reason above command, input means deny with reason; DEBUG `-showSampleCodexApproval YES`, `-showSampleCodexQuestion YES`,
  `CodexCardsTests` mock `MOCK_CODEX=1`, includes BeautifulUI.
- Input: above (at most 4 photos, resized 1600px; library videos uploaded as files to computer, message file card, protocol.md ("Sending video"); / shortcuts; Send becomes red Interrupt while working).
- Navigation subtitle: Machine · Model · State.

## New shell (ChatGPT-style, 2026-11-01)

Requested 2026-11-01: rearrange the iPhone shell like ChatGPT's iOS App: "chat is Persona (major changes), work is Agents (change the entry point)".
The pages are Assistant and Agents (requested 2026-11-01, formerly Chat and Work); new chats use `AssistantWelcome` (restored at the user's request 2026-11-02; previously no to-dos meant a blank page). The shell is the only navigation at compact iPhone width; it needs no appearance setting or launch argument. Regular-width iPad keeps its sidebar, and Mac / Vision retain shared root tabs.
#392: enabling Show messages on home in Lark settings adds a third Messages page at the top, otherwise two; the regular-width iPad adds a corresponding tab. It only browses chats and reuses existing chat pages; boundaries in [Lark migration](../extensions/lark-migration.md).
#1530: the private extension provides Lark gateway integration; entry points depending on `/api/v1/lark/…` appear only with hub feature `lark` (`LarkFeatures`, `App/LarkFeatures.swift`): Messages page and its switch / tab (stored toggle preserved, restored when the extension is installed), Settings › Connectors › Lark, Lark chats in Translation, native Lark document reader; native Moments reader additionally requires `lark_moments`. Without them, Lark document and Moments links open as ordinary websites; Inbox items with `lark` / `doc` / `moment` blocks only come from the extension and render normally. Empty features (before hello, demo mode), or only `lark_attachments` without `lark` (hubs that integrated Lark themselves before the extension), retain all existing UI. Mock `MOCK_NO_LARK=1` simulates no extension.
Only at iPhone compact width; iPad, open iPhone Duo, Mac and watch remain unchanged (iPad and open Duo use drawer ordering in Persona's sidebar, see iPad). Code in `Persona/Views/Shell/`, components in
`DesignSystem/ShellComponents.swift`, `ShellComposerBar.swift`, colors and sizes in `Theme.Shell` (the same neutral palette across styles).

| Part | Approach |
| --- | --- |
| Bottom | No tab bar |
| Top bar | Avatar · Assistant \| Agents (work shows Agents waiting for you) · right button. Assistant uses floating `AssistantTopBar` over the chat (`shellBar` in the shell); Agents uses `AgentsTopBar` over the system navigation bar, with button positions identical to the pixel. The top of the Agents page on a phone (search field, quick filters, the bar making way) is one component, `AgentsHeader` (#2478): the navigation bar never changes height, and the room for the taller shell bar is the list's own `safeAreaBar` (`Theme.Shell.agentsRoom`, not the navigation container, unchanged on returning from a chat, #340); both bars cap button scaling at xxxLarge (`Theme.Shell.topBarLargestTypeSize`). At rest the field is the list's first row and folds with it; tapped, it floats into the navigation-bar row (with ✕), and the list and the fade of `AgentsTopBar` follow the same animated value: the bar is gone before the field arrives and the sidebar cannot open; cancel restores it (no flash when opening a search result; checked frame by frame by `AgentsTopMotionTests`). The wide split and the Mac keep the system search. No results says "No results for ‘…’", not "Start your first development session" (`ShellSearchTests`). Tapping Assistant again opens the former avatar menu (Knowledge, details, launcher, session info, interrupt…) |
| Top-left avatar | Formerly ☰. Requested 2026-11-01: the old centered avatar no longer fit after the bar change, so it becomes the top-left Chat history entry, displaying the avatar. Both pages (and dragging `ShellMovingBar`) share `ShellPersonaButton`: current avatar's state face (`PersonaDot`, identical to the old center one; five states in `docs/apple/persona.md` ("States")), based on the assistant page's current chat (`assistantCurrent`), identical on both pages and while dragging; tap opens the drawer. Same size as the right circular button (`Theme.Size.tapTarget`, grows with text). When Inbox needs you, a small pale-red bottom-right dot (`InboxDot`, `Theme.inboxDot`, requested 2026-11-04; the previous red tray badge outside the face was too prominent): no number or icon, diameter `Theme.Size.dotXL`, centered at 45° bottom-right and wholly inside the face (`dotShift`), with a thin page-background outline. Pale red avoids the strong system Needs you red and the avatar's top-right thought bubble / z. VoiceOver says Chat history, value "Persona, idle" (avatar name and state), plus "Inbox needs your attention". Chat, interaction, machine, connection and Inbox data are read only inside the button, preventing push-driven top-bar rebuilds (#178; `AgentsTopBar` is Equatable). `ShellAvatarTests`; use mock `MOCK_INBOX=empty` for no badge |
| Pages | One horizontal strip: drawer \| Assistant \| Agents (`ShellPager`, requested 2026-11-01). Drag left from Assistant or right from Agents to follow the finger to the other page; the selected switch segment tracks the finger. Release follows drawer rules (fast flick switches, otherwise past halfway), with the same spring. Tapping switches slides too. `AssistantView` stays mounted; `ChatsView` is created on first switch / drag, then retained, preserving position; at rest the other page is transparent and noninteractive. While dragging, page top bars hide and shell `ShellMovingBar` replaces them (avatar and switch stationary, right button morphs from New chat to New session), as page-local bars would partly cover each other |
| Horizontal gesture ownership | A UIKit pan (`DrawerPan`) decides from actual displacement after a few points: within 45° is horizontal, steeper belongs to list scrolling. Assistant: right opens drawer, left goes to Agents. Agents: its rows have no swipe (#1244; their actions are in each row's ⋯), so a drag pages from anywhere on the page, rows included: right returns to Assistant, left goes on to Messages when it is enabled (#2822). Agents no longer opens the drawer by right swipe; use the avatar. Code blocks and tables that can scroll in that direction get priority; dragging in text being edited never switches pages; pushed pages (system back swipe), search and an open drawer also disable paging |
| Sidebar | Lifted from Persona tab into the shell (`ShellState.drawer`), accessible from both pages. Assistant at root opens it with a full-page right swipe; both pages can tap the avatar (right swipe on Agents returns to Assistant). Main chat is first, displayed as Persona with the current avatar App icon `AppGlyph` (#3203; display name only, session id unchanged, #2004), then a separator, Inbox (count), To-dos, Scheduled tasks, Knowledge, Machines, another separator, then Pinned and Recent history. Main chat appears once, excluded from Pinned, Recent and Automatic chats; its row remains before its session is listed or any other chat exists (tap opens main chat, or New chat if none exists; disabled until assistant configuration arrives). Its right side shows Working / Waiting for you / Unread; long-press offers Compact context / New main chat, Copy session ID, Copy diagnostics, End while running…; it cannot swipe left to archive. #4606 / #4916 moved it above Recent and renamed it Main chat; the owner wanted the #2004 layout, restored by #5110. Bottom theme-colored New chat pill (#1312) + Settings |
| Sidebar destinations | All push onto Assistant's stack (`ShellState.show` → `AssistantView.show`). Inbox and Machines use value-based pushes (`ShellRoute`, `ShellDestinations` registers them and their InboxItem / Lark / machine-detail / session destinations): a value-based push from a view-pushed page inserts the new page underneath it (docs/lessons/ios.md) |
| Settings | Full-screen sheet (`model.shellSettings`, in the model because style changes rebuild the tree and the sheet must return to the same page), top-right × |
| Empty chat | Shared welcome (`AssistantWelcome`: greeting, explanation of new / main chat, online computers and iPhone, six suggestions; Liquid Glass two-column cards, BeautifulUI "You can ask me" list card). The first 2026-11-01 version showed only the input and was blank without to-dos; user restored the old design 2026-11-02. Above input: Agents waiting for you, then Inbox Needs you, at most 3 rows + "N more in Inbox" (`ShellNeedsYou`), hidden if empty; page background beneath this and the input, with welcome scrolling underneath and fading at the top rather than clipping. Rows adapt to space (2026-11-02, #283; 3 rows + input occupied half Duo's outer screen and cut suggestion cards): never cover the first suggestion (`NeedsRoom`: measures its lower edge, page height excluding keyboard, resting input height), reduce rows when needed, minimum 1; ordinary iPhone Liquid Glass 3, 17e BeautifulUI 2, Duo outer / SE 1. If hidden items include Agents, say "N more need you"; if only Agents remain, tap goes to Agents (they are not in Inbox). `AssistantWelcomeTests` guards both layouts and drawer New chat: greeting, explanation and first suggestion visible above input; Needs you does not cover the first suggestion and focusing input does not change row count (also run on Duo outer screen) A first message whose start failed (#1368, as the web decided in #1122) stays where it was sent as the chat's failed bubble (`PendingBubble`): its red mark's menu has Resend and Delete, the reason sits over the input (with the readiness card for a refusal), the input stays empty and takes no new first message until it is resent or deleted, Health waits too. Kept across launches (`FailedStart`). Resend goes with the old op id only when the hub says that start is done or still running (`GET /api/v1/ops/{id}`), as a chat's Resend. `FirstMessageFailedTests` |
| Input | One row: [+] Ask Persona [microphone]; focused or nonempty it grows into a card, text above, then [+] … [Model · Effort] [microphone] [Send] (`ShellComposerBar`, shared with Agent chats, #1091). iOS 长消息（#5685）：行数上限为输入框底边以上、扣除窗口顶部安全区的可用高度 × `Theme.Shell.composerHeightFraction`（22%）÷ 当前字体行高，向下取整并限制在 3–8 行，与 Android 和网页统一；键盘、横屏和窗口尺寸变化都会重新计算。达到上限前只撑高；达到上限后保持高度、内部滚动，文字区右上角出现展开图标，并为其保留右侧空间，底部工具栏不增加按钮。删到上限以下时图标消失、文字右边距恢复。展开页右上角收起、底部发送；收起保留文字和光标。Mac / Vision 的原有高度不变。 Always the same view, preserving focus and Pinyin marked text while morphing. Model / effort is compact text like ChatGPT "6.1 Sol Medium" ("Opus 5.5 High", #1117): no icon or red, model in bold primary text (`secondaryStrong`), effort in secondary text with a small space; truncate long model names at the tail, never effort. Right-side space matches the circular Send / Stop button margins, making Model ↔ microphone and microphone ↔ Send / Stop gaps equal (`SessionModes.composerTitle` / `modelName`). GPT display names drop `gpt-` and word-format suffixes (`gpt-6.1-sol` → `6.1 Sol`), preserving version and variant; directly related model choices use the same format, custom display names take priority, raw model IDs, saved values and requests unchanged (#2200). No App-added Hide keyboard icon or spacer row; system buttons and chat keyboard-dismiss gestures stay. The menu opens upward, nearest the button first: red Allow everything warning (`BypassWarning`, no input label), model, effort, then nested permission mode (`SessionModesMenu`; new chat `StarterModesMenu`, model is launcher). Model selection only for Claude Code in herdr panes (`/model`) and new assistant chats (launcher); existing assistant chats and Codex only show the current model. Codex / the internal Codex fork have no permission mode; app-server Codex has effort (`efforts`), others do not |
| Chat | User bubbles share Agents-chat colors (`Theme.userBubble` from the interface style, #2647; previously shell-specific neutral gray) |
| Slow network | Small pill below the top bar (Waiting for network / Connecting…), shared by both pages; the page switch occupies the original title area |

Routing stays unchanged: all navigation still writes `AppModel.selectedTab` (notifications, Siri, hand to Persona, Inbox rows…); the shell treats `.assistant` / `.chats` as its pages,
`.mail` as "push Inbox on Assistant", `.settings` as "open Settings sheet", then returns to the original page. Foreground page has one source, `ShellState.page`;
writing `selectedTab` forwards immediately to the shell (not through `onChange`), and switch segments and dragging use it too: see `docs/apple/shell.md`.

UI tests always use `launchPinned()` to fix the style and `launchPersona()` for clean Demo Mode startup, without overriding the app's default navigation.
Shell tests: `ShellTests`, `ShellAvatarTests` (top-left avatar and Inbox badge), `ShellPagerTests` (paging), `ShellSwitchTests`, `ShellSearchTests`. DEBUG: `-openShellPage inbox|machines|routines|knowledge|profile`; `-showShellPagerAt 0.3` holds the strip 30% across for screenshots; `-showHomeState empty|error` (new-chat page: nothing needs you / a failed-start notice, for screenshots).

## Foreground-only work (polling and reporting read state)

Foreground-only work requires "the view is in the window and its page is foregrounded" (`Views/TabActivity.swift`): **polling**, **reporting read state**, and a chat's **display subscription**.
Previously the system `TabView` removed the old tab from the window on switching (`onDisappear`, cancellation of `.task`). The new shell's Assistant and Agents pages are a ZStack;
the hidden page is merely transparent and noninteractive, **still mounted**, so `onDisappear` never fires: without a signal it would keep polling and mark messages read for the user.

- Pass down `tabIsActive` (true outside any page): `ShellPageSlide` sets it from `ShellState.page` in the new shell; regular-width iPad and open Duo `TabView` use `TabActivityScope` on each tab's content.
- `.tabActiveTask` / `.tabActiveTask(id:)` cancels when the page goes behind and restarts from scratch when foregrounded. `.onTabVisibility(appear:disappear:)` replaces paired
  `onAppear` / `onDisappear`, each called once; the system briefly remounting the old tab during a switch does not count as appearance.
- Used for Lark chat's 60-second fallback polling and read reporting (catch up on foregrounding), and Machines-page (`MachinesView`) / machine-detail (`MachineDetailView`, including its computer-control and browser sections) polling.
- Shared `ChatView` transcript streams and visible/read ownership use paired visibility edges on iPhone as well as Mac. Hidden chats keep their store and scroll state but unsubscribe; returning resumes with the store's `since_seq`, without recreating the page or downloading all history.
- **Not for** generating replies, Agent tasks, dictation or timers (work-row stopwatch, terminal 250ms tick, header auto-collapse). These continue regardless of the foreground page; releasing a display subscription does not interrupt work on the hub.
- Under system `TabView`, timing matches window removal, so UI and behavior stay unchanged. New polling and read reporting must use these helpers, not direct `onDisappear` / `.task`.

## Persona top bar and sidebar (2026-10-30)

Inspired by dot and ChatGPT App (user-provided screenshots). Code in `Persona/Views/Assistant/AssistantTopBar.swift`, `AssistantSidebar.swift`.

- **Top bar** floats over the chat, with no system navigation bar or solid bar: a downward-fading fog (`TopFog`: page background + material, extending under the status bar), with chat scrolling beneath.
  Chat-list top reserves `AssistantTopBar.height` (70pt); the home page reserves the same via `contentMargins(.top)`. The bar stays fixed while scrolling (unlike the collapsible Agents header).
- Centered **Persona avatar + name capsule** (main and new chats use the avatar name, default Persona; side chats use the chat title). The avatar is the current avatar's **state dot**
  (`PersonaDot`): Idle / Thinking / Working / Needs you / Offline animations, see `docs/apple/persona.md` ("States"). Tap for Knowledge, Details (profile
  `AssistantProfileView`), Return to main chat (side chats); Copy directory (assistant cwd), Copy info (chat name, session ID, machine, cwd, launcher / model,
  `claude --resume` command, convenient for another Agent, `AssistantCopyText`), Copy session ID; launcher submenu, session-info card, Interrupt while working.
  Copy overlays "✓ Copied…" on the name capsule for 3 seconds (not global toast, which would cover the avatar). Slow / offline networks replace the name with spinner + Waiting for network / Connecting… / Updating…,
  and the menu's first item becomes Connection details.
- Left circular **Chat history** button opens the sidebar; right circular **New chat** button (disabled on the new-chat page). Both icon-only.
- **Sidebar** below the main page, 82% of screen width (at most 380pt). The main page covers it, remaining a same-size rounded card when slid right (thin dark-mode border separates it from the sidebar), dimming with progress (`Theme.Fill.scrim`, black 20% light / 45% dark, #334); sidebar stays horizontally fixed and rises only 8pt as revealed (`Theme.Motion.drawerLift`), with no scaling or fading. Tap the card or drag left to close.
  Content (2026-10-31, changed to match Doubao's sidebar from user screenshots): large avatar name (Persona) and a circular Search button on the right (becomes search field + Cancel);
  Main chat is the first row (avatar App icon + Persona, identical to the new-shell drawer, #2004 / #5110), then a separator; actions below: New chat, Scheduled tasks, Knowledge, Health (icon + text, no cards; Health in `docs/assistant/health.md` ("iOS and Mac entry points")); separator; Pinned (main chat excluded;
  long-press other chats to pin, stored locally in `assistant.pinned`) and Recent (remaining chats newest first, first 30 then View all), each with gray ⋯ at the title's right
  (Pinned: Unpin all; Recent: New chat, View all). Each row has one circular marker (pale chat bubble color chosen by session id, adjacent rows different) + one-line title,
  with a right-side status dot only for Needs you / Unread / Working; the last message appears only in long-press preview. Current chat has a rounded gray background. Bottom row: avatar + name + Online / Offline and machine
  (tap for profile), Settings on the right (drawer only; split mode has the tab bar beside it). Search lists only results (N chats). Long-press also copies session ID or ends the chat.
  **Automatic chats** (#3264, `AutoConversations`): when the hub reports origins (`conversation_origin`, session `origin`, `docs/protocol/protocol.md` ("Assistant chat origins")),
  Recent lists only the user's own (`user` / `channel` / absent); Inbox AI twin, scheduled tasks, Lark /agent, derived chats and extension-created chats fold into the first row below Recent, "Automatic chats N",
  collapsed by default, local `assistant.autoOpen`; collapsed still shows waiting count / Working / Unread. Expanded shows small rows per origin (count, status) and All chats,
  opening `AutoConversationsView` (All · Mine · source filters, prefiltered by source); Recent's ⋯ also includes All chats. Same view in iPhone drawer, iPad split and Mac sidebar.
  The currently open automatic chat keeps its Recent row; search still searches all. Old hubs without origins omit this row; scheduled-task chats are still recognized per #2838 and grouped on Scheduled tasks.
- **Gestures** (2026-10-31, rebuilt to match ChatGPT after build 49 feedback "not smooth, not following the finger, small area, swipes do nothing"): right-drag anywhere on the page opens the sidebar,
  using the same state as the button. UIKit pan (`DrawerPan`) decides after roughly ten points, from **actual displacement since touch-down**; within 45° of horizontal belongs to the sidebar
  (`AssistantDrawer.takes`; thumb diagonals typically 30°–40°, vertical scrolling steeper); when closed only rightward motion counts. Previous pan translation / velocity logic
  read translation 0 at that moment and velocity estimated from the last two samples, misreading a 30° swipe as nearly vertical and yielding to the list: "swipes do nothing".
  Horizontally scrollable content under the finger that is not at its left edge (wide code blocks, tables) gets priority; at its left edge, right-drag opens the sidebar. Every page scroll-view pan waits for the sidebar's decision
  (`shouldBeRequiredToFailBy`; it decides within the same few points where scrolling normally begins, so scrolling is not delayed); otherwise a 40° swipe's vertical component crosses the list threshold first,
  which steals it before the sidebar is asked (at the chat bottom it looks like no response); code blocks at their left edge also steal it for bounce. Pushed pages (profile, Knowledge, scheduled tasks) use system back gestures instead.
- **Release** matches system back: above 300pt/s follow velocity direction, otherwise open past halfway (including 0.2-second momentum projection, `settlesOpen`); snap with a non-bouncing spring
  (`Theme.Motion.drawer`) inheriting finger velocity so fast flicks do not pause before moving.
- **Finger tracking**: while dragging, only a small page-card modifier (`DrawerPageMotion`) reads progress, changing offset and opacity (2026-11-01 removed 6% shrinking like ChatGPT: it left top/bottom gaps and lowered the top bar relative to the sidebar); corners use screen radius (`Theme.Radius.page`). Requested 2026-11-02, switched to Codex App layering (#341): main page slides right above a horizontally fixed sidebar beneath, rising slowly from 8pt lower, fully opaque and same-sized throughout (no longer two rectangles moving together side by side). Corners, shadow and thin border fade in as fixed shapes,
  not redrawn per progress. Sidebar stays mounted in its own `UIHostingController` (`DrawerSidebarHost`), UIKit directly adjusts vertical transform and hides it when closed,
  avoiding per-frame SwiftUI. Tab bar fades with UIKit alpha rather than `.toolbarVisibility`: hiding it changes safe areas, relaying out the whole chat on the first drag frame and
  final closing frame. Sidebar reaches the bottom screen edge where the faded tab bar was, with home-indicator clearance. Per-frame main-thread timings before / after are in `docs/operations/testing.md` ("Sidebar dragging").
- Tests: `AssistantTopBarTests` (avatar menu / copying, left/right buttons, button and gesture opening, selecting a chat closes and switches, fixed top bar during long-chat scrolling, connection status; unit tests guard horizontally fixed, same-size, opaque sidebar across screen widths and drag progress, with closing / reopening following the same path).
  `DrawerSwipeTests` (mock `MOCK_DRAWER=1`): ±30° / ±40° swipes from screen center, over bubbles and after list flicking all open; left/right drags over code scroll it,
  then right-drag after returning to the left edge opens the sidebar.
  `-openAssistantSidebar YES` starts with sidebar open (screenshots); a fraction such as `0.5` holds halfway through dragging to inspect card corners and shadows.

## iPhone Duo (2026-10-30)

The iPhone Duo folds: closed it uses the outer screen (about 466×678 points, compact width), open the inner screen (about 669×951 points, regular width), and can also be half-folded, landscape or portrait.
On the outer screen and the inner screen in landscape, **the system places the status, navigation, toolbar and tab bars vertically along the screen edge**; on the inner screen in portrait the bars remain horizontal. Official guides:
`developer.apple.com/iphone-duo`, HIG "Designing for iPhone Duo". Build with Xcode 27 or later (`xcode` configuration, `docs/operations/config.md`);
Apps built with older Xcode do not extend under the status bar and camera on Duo.

- **Two layouts by width, not device model** (`Persona/Views/SplitNavigation.swift`). Compact (ordinary iPhones, closed Duo) keeps the original single
  NavigationStack with no behavior changes; regular (open Duo) uses `NavigationSplitView`: list left, opened page right, like Mail.
  Both share the same `path`, preserving the current page when folding / unfolding; half-folded, the system keeps both columns away from the crease. Agents, Inbox and Settings all use it.
  - But `RootView` swaps the entire tree between the new shell and tabs by width (`usesShell`), losing the Agents page's `path` (view state); iOS briefly changes width when taking a background snapshot,
    so switching Apps on an open Duo could return to "Select an Agent" in the right column (2026-11-04). Also, chats opened by left-column links do not write to `path`. The new Agents page therefore
    reopens `dev.openChat` (saved when opening a chat, cleared on returning to the list), one beat later (the outgoing tree's split view clears the path as it leaves);
    header-switched Agents in the same space are stored in `AppModel.chatSwitched`, and a rebuilt chat page on the same path opens that one directly. `ReturnToChatTests` uses `-swapTreeAt` on iPad to simulate a tree swap.
  - Left-column `NavigationLink(value:)` replaces the right column (the left has its own never-pushed stack, forwarding values to the right); programmatically opened list pages must replace it too:
    `path = NavigationPath([value])` (in single-column mode the list is at the stack bottom, equivalent to append; Agents `openFromList`). Pages opened from details still append.
  - The first right-column page has no back button (the list is beside it): add `.splitDetailPage()` to the destination; Agents' custom chat back button hides for `\.splitDetail` (keeping its space so the title stays centered).
  - Split chat pages do not hide the tab bar (single-column pages do, to fill the screen).
  - Settings entry points all use values (`SettingsPage`); view-based `NavigationLink { page }` pushes the left column's own stack and opens in the narrow column.
- **Persona**: in regular width, the sidebar is `NavigationSplitView`'s left column (no Settings gear; the tab bar is always beside it), with the top-left button showing / hiding it;
  compact still uses a drawer.
- **Readable width**: chats and the Persona home page are at most 760pt wide (`ChatView.readableWidth`), centered when wider; the top bar still spans the full width.
- **No screen dimensions**: the screens have different sizes and ratios; never use `UIScreen.main`. The long-press message action bar stays within the chat area (`MessageMenu.area`
  coordinate space); interaction forms have a maximum height of 55% of their container (`containerRelativeFrame`); image scaling uses the view's `traitCollection.displayScale`.
- **Toolbar buttons need both titles and icons** (`Label("新建会话", systemImage: …)`, "New chat"): vertical bars show only icons, and overflow menus show titles;
  icon-only buttons become blank menu items.
- **Vertical side bars**: when the system places bars at the side (outer screen, inner landscape; non-nil `toolbarVerticalEdge`, iOS 27.1 onward), the Agents chat page no longer draws its own header buttons;
  Back, Terminal, Git, Files and ⋯ go into the system toolbar, arranged below the status bar (`ChatView.sideBarItems`, each with title and icon). The content top keeps only a non-collapsing title
  (tap for session info), same-space Agents, loop / background tasks and the sub-Agent status line (`sideHeader`, the same second and third rows as the custom header). Horizontal bars (ordinary iPhone, inner portrait) retain the custom collapsible header.
  The chat hides the system back button (`navigationBarBackButtonHidden(sideBars)`), leaving only its supplied Back in the side column. Chat destinations must use
  `.splitDetailPage(ownsBack: true)`: an outer `navigationBarBackButtonHidden(false)` overrides the page's `true`, bringing back the system button
  and stacking two Back buttons (2026-11-04, TestFlight).
  With the keyboard open, this header becomes fog only (title, same-space Agents and sub-Agents hide until the keyboard closes): inner landscape leaves only about 390pt above the keyboard,
  and the header, mode buttons and input previously occupied it all, leaving no message visible (2026-11-04). The system draws its bar's title over the content, not in the side
  column, so it is removed while the header shows its own (`toolbar(removing: .title)`; the session's name showed twice, one above the other, 2026-11-08, TestFlight)
  and comes back while typing, when the header is only fog.
  Outside the shell, Persona's top bar remains custom at the content top; its fog extends horizontally under the side status bar (`BarFog`; limiting it to content width leaves a vertical edge in dark mode).
- **The shell's side rail** (2026-11-07, TestFlight: on the outer screen every button was at the top, out of reach of the hand holding the phone): with bars on the side, the shell's
  top bars (Assistant's, Agents' and the moving bar while the strip slides) become a rail in that column (`ShellSideRail`; `ShellView` reads the edge once with `readsVerticalBarEdge` and
  hands it down as `\.shellRailEdge`). Following Apple's iPhone Duo guidance (navigation first, then the prominent action, the tab bar below), the face (the drawer) and the page's
  action (New Chat / New Session) sit low in the column and the switch is a vertical capsule at its foot (`ShellSwitch(axis: .vertical)`: each page's symbol over its name,
  the pill gliding down with the strip), all within the thumb's reach; the status bar keeps the column's top. The pages drop the top room the bar had
  (`AssistantTopBar.room`, Agents' list `room`), so the content starts at the top. Menus (tapping the chosen segment), the badge and the drawer work as before; horizontal bars
  (ordinary iPhone, inner portrait) are unchanged. The rail measures the column as the page's width with the side's safe area less its width without: a `GeometryReader` that
  ignores the safe area reports no inset of its own on that edge.
- **Short outer screen** (about 678pt): on Persona's new-chat page, Needs you rows above the input shrink to the remaining space (see "Empty chat" under "New shell" above).
- **Simulator verification**: iPhone Duo has only the iOS 27.1 runtime (which supports only it). Folding / unfolding / half-folding can only be switched in Xcode Device Hub,
  with no CLI. Screenshots must specify the display: `xcrun simctl io <udid> screenshot --display=1` (outer) / `--display=3` (inner, 2853×2007 in landscape);
  otherwise the inactive screen may be captured (all black); calculate brightness first to confirm the active screen. Launch arguments work normally (`-tab`, `-openSession`, etc.).

## iPad (2026-10-31)

One App for iPhone and iPad (`TARGETED_DEVICE_FAMILY` 1,2). Previously only 1, giving iPad a phone compatibility window with enlarged buttons.

- Like iPhone Duo, layout follows width: regular (iPad full screen, wider split screen) uses `SplitNavigation` with two columns; compact (narrow split screen / Slide Over) uses one stack.
- iPad's left column uses `Theme.Size.sidebar` (340…480, ideal 400; system default is too narrow and wraps all Agents-card titles); Persona's chat list does too, preventing width jumps when switching tabs.
  iPhone Duo sets no width (the system splits at the crease).
- iPad supports all four orientations (required by split screen and Stage Manager); iPhone remains portrait only.
- The sidebar matches the iPhone new shell's drawer (requested 2026-11-05): Persona (main chat) first, then a separator, Inbox, To-dos, Scheduled tasks, Knowledge, Machines, then chat history (#2004 / #5110);
  bottom "New chat" pill + Settings, then the avatar, online status and machines row. Top tabs are only Persona, Agents (and optional Messages); Inbox and Settings are no longer tabs:
  Inbox, To-dos and Machines push into Persona's right column (`ShellDestinations`); Settings is a sheet (top-right ×). Routing still writes `AppModel.selectedTab`;
  `PadRouter` (`RootView.swift`) maps `.mail` to "push Inbox on Persona" and `.settings` to "open Settings sheet". Mac and Vision keep their shared root tabs.
  `IPadSidebarTests` guards this (portrait and landscape: ordering, bottom buttons, no Inbox / Settings tabs, Inbox opens on the right, Settings sheet can close).
- Welcome content is limited to `Theme.Size.readable` (560), not stretched across large screens.
- Detail pages opened from chats (task details, dock Agents, to-do strips, hub links `/todos?id=`) push into the chat's stack at regular width and fill the detail column,
  rather than sheets: `presentationDetents` has no effect at regular width and a sheet becomes a small centered form (feedback 2026-11-05, #2816). Use
  `detailPresentation` (`Views/DetailPresentation.swift`): still a sheet on phones, Mac / visionOS unchanged; replace a page's own `NavigationStack` with
  `DetailStack` (no extra stack when pushed). Where there is no stack to push into (a to-do opened from a session-info sheet), use at least `.presentationSizing(.page)`.
  Tests: `IPadTaskDetailTests` (iPad only, `MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1`).
- Left column reaches the screen top: the split view's left column starts below the status bar and cannot draw into it; that strip used to be window white, making the list appear short of the top (2026-10-31).
  `SidebarTopFill` behind the whole split view colors the strip to the left-column width (system sidebar color `Theme.systemSidebar` for glass, page background for other styles);
  it does not fill when the column is hidden (uses `columnVisibility`; the hidden column still reports its last position). `IPadLayoutTests.testSidebarReachesTheTop` checks pixels.
- Tests: `IPadLayoutTests` (iPad simulator only: native full-screen portrait / landscape, wide enough sidebar, chats open on the right); `InboxSplitTests` (split layout:
  switching between Lark chats shows the new chat's own messages and draft on the right, switching back preserves the draft). XCUITest's own landscape screenshots are rotated;
  inspect landscape by holding with `TEST_RUNNER_PERSONA_HOLD=<seconds>` and capturing with `simctl io screenshot`.

## Home-screen quick actions (#3199)

Long-press the Persona icon: "New chat", "Agents", "Inbox" (`Persona/App/QuickActions.swift`), aligned with Android shortcuts and web manifest shortcuts. Set `shortcutItems` once at launch, identical for everyone, not updated with state.

- Navigation only: Agents switches to Agents, Inbox pushes onto the assistant, New chat follows the drawer's New chat route (`ShellState.show(.newConversation)`), returning already-pushed pages such as Inbox to the root. Without hub pairing, remain on Welcome.
- If the App is open or backgrounded, use the scene delegate's `windowScene(_:performActionFor:)`; on cold launch the item comes through scene connectionOptions. `QuickActionSceneDelegate` handles only these two entry points; SwiftUI still manages the window.
- `QuickActionsTests` long-presses the home-screen icon and taps all three items. Cold launch is untested: SpringBoard launch cannot pass demo-mode arguments. Each simulator return to the home screen waits about 60 seconds for SpringBoard idle; the whole test takes about 7 minutes.

## Share to Persona (#4586)

In another App, Share › Persona sends text, a web link or up to 4 images, with an optional note, to the Persona main chat, like Android's ShareActivity (`PersonaShare/`, target `PersonaShare`).

- The extension connects to the hub itself; the App need not be running: read App-saved pairing from Keychain (`HubStore` "hubs" entry, App keychain group), try hubs in the default group in order, obtain the main chat with `GET /api/v1/assistant`, then send like the input field (images resized to 1600 px, JPEG). No App Group, so no manual group setup on the developer website.
- Failure stays on the panel with a reason: unpaired, invalid pairing, no main chat yet, cannot reach the hub. No SSH tunnels; main chat only, other destinations later.
- Release: the share extension has a new Bundle ID (`<bundle id>.share`), see `docs/operations/release.md`.

## Effort and permission modes

Two small labels above the chat input: permission mode (Manual approval / Auto-edit files / Plan only / Auto / Allow everything) and effort (Auto / Low … Max),
like Claude Code's terminal bottom bar; tap to change (`SessionModeBar`, wording and icons in `SessionModes`). The new shell moves these into the input's bottom-right
"Model · Effort" button (#1091, see the input under "New shell" above). New Agent forms and the assistant home page above its input
also offer selection (unset means "Follow launcher"). Codex / the internal Codex fork sessions do not show them; app-server-driven Codex sessions (hub supplies `efforts`, `Session.setsEffortOnly`)
show effort only, with levels supported by their model (`SessionModes.effortOptions`, `docs/integrations/codex.md` ("Effort")).
For Claude Code sessions in herdr, the effort menu also has a Model section (#3119, matching web #2434; `SessionModes.switchesModel`):
Default / Opus / Sonnet / Haiku; choosing sends `POST …/modes {"model"}`, and the node enters `/model <id>` with message-sending safeguards and checks the screen; failure explains why.
While working, waiting for your answer or not running, this section is disabled and its title explains why; the title notes that the model also becomes the default for new sessions (Claude Code behavior).
The new shell's Model · Effort menu still sends `/model <id>` as a message.

- Display: each transcript user row has `permissionMode`, each reply `effort`; App changes take priority until the updated value appears in the transcript
  (node/modes.go). Assistant chats are an exception (#2416): App values persist in hub session state (`app_mode` / `app_effort`), always override the transcript and survive node restarts
  (previously the main chat's old transcript default locked the mode, and App changes reverted on node restart). Terminal shift+tab changes enter the transcript only on the next message; developer-info cards read the terminal bottom bar for live values.
- New sessions: `--permission-mode` / `--effort`.
- Changing running sessions: the headless assistant sends stdin control requests (`set_permission_mode`, `apply_flag_settings {effortLevel}`), effective on the next request;
  herdr Agents are operated like a person: shift+tab cycles to the target (default → acceptEdits → plan → auto), and the `/effort` picker moves to the level,
  then presses `s` (session-only). **Do not send `/effort <level>`**: it also saves the level as this model's default for all future sessions (`modelSettings` in settings).
- Effort changes wait until the turn ends and the terminal input is empty (otherwise `/effort` appends to someone else's unsent draft); herdr Agents can choose Allow everything only at creation, not in the cycle.
- Assistant chats (Persona page, including the main chat) can choose Allow everything both at creation and while running (#2416); a red warning at the menu top explains consequences (`BypassWarning`). Persona does not start
  claude with `--dangerously-skip-permissions`, so a control request cannot switch to bypassPermissions: stop an idle process, then resume the next message with `--permission-mode bypassPermissions`;
  if answering or holding background tasks, replace the process on the first message after this turn.
- Auto effort (`auto`, the model's default). Tested on Claude Code 2.1.316: `--effort` rejects auto (warning, then uses default),
  settings.json `effortLevel` rejects it too (silently ignored), and the `/effort` picker has no Auto slot; only `/effort auto` accepts it, but also clears
  this model's saved default in settings. The three paths are therefore:
  - New sessions: omit `--effort`. Equivalent to Follow launcher, so new-session forms omit this option (`SessionModes.startEffortLevels`).
  - Headless assistant: `apply_flag_settings {"effortLevel": null}`, the same representation Claude Code itself uses for auto. Transcripts record the actual level;
    the node must not send it back as a new App choice (which would pin auto to that level). Only the App changes assistant effort; once Auto is selected, it remains auto.
  - herdr Agents: open `/effort`, move right from the leftmost slot to the one labeled "The default effort for this model", press `s` (`enter` for the slider picker).
    An approximation: the session is pinned to the current model's default and does not follow later model changes; if organization settings changed the default and the label cannot be found, the node cancels and errors.
  - Display: transcript replies record actual levels, never auto; the Auto label lasts only until the next reply, then shows its level (for example Medium).
  - Selecting auto in Settings › Claude Code Settings removes `effortLevel` (`claudeconfig.go`).
- While the picker is open, herdr reports the Agent blocked; after 1.5 seconds the hub would push it to the phone as a terminal dialog needing attention, so the node suppresses blocked while operating it.

### Claude Code ultracode (#1307)

The server contract is #3427 ([protocol.md](../protocol/protocol.md) ("Claude Code ultracode")): a session-only switch independent of the effort,
off by default, never remembered and never turned back on by itself after a resume. The behavior follows the web's (#3428,
[web.md](../web/web.md) "Claude Code ultracode"). Pure rules are `Shared/Ultracode.swift` (the watch compiles them for its rows' mark), requests
and what this device asked are `AppModel.setUltracode` / `dismissUltracodeLost`, views `Views/Chat/UltracodeViews.swift`.

- **Where**: the hub lists the `ultracode` feature (an older hub shows nothing), the agent is Claude Code (not TRAEX, Codex or a runtime's), its effort can
  change now (it takes messages, no read-only source, not offline), and it is not a Persona conversation (the main chat included: the server refuses those).
- **Session fields**: `ultracode` (omitted is unknown, drawn off; the info card says Unknown), `ultracode_note` (the node's reason), `ultracode_pending`
  (applies next turn) and `ultracode_lost_at`. An odd value never drops the session. `ChatCache` stays at `chats-v10`: the hub only rewrites the text of
  Claude Code's existing `/effort` output notice (same id, still a short notice), so a cached copy keeps the CLI's own confirmation until its turn
  moves on, and the notices from `ultra_effort_*` attachments are new ids that merge in; dropping every cached chat for that was not worth it.
- **Menus**: a section under the thinking effort in the shell composer's model menu (`SessionModesMenu`) and in the shared effort chip (`SessionModeBar`) on Mac / Vision:
  a `Toggle` whose subtitle is the line (what it does, "Applies next turn" while a change this device asked waits, "A change is queued for the next turn"
  for another device's, or the node's reason), and the Claude quota with its 80% warning as the section's header (`Ultracode.menuWords`). A menu
  draws a subtitle only from a second `Text` directly in the label (a `Label`'s two-`Text` title loses it, on the Mac too: the effort levels' details
  are not drawn) and colors no words. An iOS menu cuts a subtitle after three lines but draws a header whole, line by line, so there the node's reason
  goes first in the header and the subtitle keeps what the switch does or that a change waits; a Mac menu wraps a subtitle (`NSMenuItem.subtitle`) and
  draws a header on one line (a line break becomes a space), so there the reason stays in the subtitle and the header's parts are joined with " — ".
  The quota was a disabled item at first: drawn faint (about 2.3:1), it read as something not available. A menu that opens upward lists its items the
  other way round, so on iOS and visionOS the section is declared before the effort (`UltracodeMenuSection.leadsEffort`) and reads under it, next to
  the button without scrolling; a section's header stays on top of it either way. A Mac menu keeps the declared order.
- **Where the switch stands**: what this device asked while the request is out, while the node says it is queued, and until the node's push answers
  (`Ultracode.Ask.answered` is the session as its page drew it when the request returned; the pushes that came meanwhile land first, `landPendingSessions`).
  Grayed only while its own request is out; the node's reason replaces the line but does not gray it. An ask is forgotten at the first update of the
  lists after the session moved past it, or went (`dropSpentUltracodeAsks`, as the web's `useUltracodeAsk`): only hiding it let an old ask come back
  when another device queued a change later, and the switch said "Applies next turn" for what this device had asked long before. A failure, a refusal (`409 node_outdated`,
  `ultracode_unsupported`) too, puts it back and is said once in a toast (the menu has closed); nothing is kept, so asking again gets the node's answer
  again. A refusal never becomes the main chat's readiness card. A queued change never holds a message back (#5510), so sending is unchanged.
- **Marks**: `Theme.Icon.workflow` in `Theme.ai` after the effort on the composer's model button and the shared effort chip (read out as "…, ultracode on"),
  beside an agent row's title (`SlotRow` in both styles, `CompactSlotRow`, the watch's list; not on an offline row, which the node still remembers on;
  where a title can take two lines, `SlotRow`'s and the watch's, the mark is part of the text after its last word, `UltracodeMark.after`),
  and on a Codex thread at its `ultra` effort (no extra words: its effort says Ultra). It comes and goes with `Theme.Motion.state`, at once with Reduce Motion
  (`UltracodeMark.motion`); nothing repeats.
- **After a resume**: one line over the composer, "ultracode off after resume", **Turn Back On** (`{"ultracode": true}`; "Applies next turn" while it waits)
  and × (`{"ultracode_dismiss_lost": true}`, gone at once, back if the hub says no).
- **Info card**: a row "ultracode: On / Off / Unknown" under Thinking Effort; none for an offline agent, whose process is gone (as its row has no mark).
- **New Agent**: a switch under the thinking effort for Claude Code, off each time the sheet opens and never saved with the per-machine effort; only
  `"ultracode": true` goes with `POST /api/v1/sessions`. Grayed with why when the computer's `claude_version` is a known version before 2.1.284 (an unknown
  one may be tried). A start the hub refuses for it fails with "Couldn't start with ultracode: … Retry starts without it" and the switch goes off.
- **Quota**: the Claude subscription's 5-hour and weekly use from `GET /api/v1/usage/windows` (`UsageWindowsStore`): a chat that offers the switch keeps
  it no older than two minutes while it is in front (`keepFresh`, nothing extra while Machines or the Mac's bars poll), New Agent loads it when it shows
  the switch; only with data: an account read on the computers before a gateway's, fresh before stale,
  then the fullest. From 80% a warning, only for a switch that stands on or can simply be turned on.
- **Plan cards and to-dos** (#12, the web's rules in `web/src/lib/ultracode.ts`), only when the hub announces the feature: each task on an open plan card
  has an **ultracode** chip after its permission chip (AI color while on, quiet while off) that changes it before Start, and Edit has a switch per task
  that opens with the card's choices. Start sends `ultracode` only on tasks whose choice differs from the assistant's, so a plain Start still sends no
  `tasks`. On a computer whose known Claude Code is older than 2.1.284 the chip is grayed, a task that was on gets an orange "Starts without ultracode: …"
  line, and it starts with it off. Task cards, folded task lines and to-do rows show `UltracodeMark` after the title while one of the task's Agents
  carries the row mark (`rowMark`); before any Agent exists, task cards and lines show it when the plan task asks for it (to-do rows, as on the web,
  follow the Agents only). Done and failed-to-start to-dos show none. List rows
  (`summary=1`) omit `ultracode`, so `Session.preservingDetails` keeps the value already known. Rules: `apple/Shared/PlanUltracode.swift`; views:
  `apple/Persona/Views/Assistant/Tasks/PlanUltracodeViews.swift`; tests: `PlanUltracodeTests`.
- **Tests**: `UltracodeTests` (FakeHub: decoding with older hubs, the requests, where it shows, graying, the ask, the quota) and `UltracodeUITests`
  against the mock hub with `MOCK_ULTRACODE=1` (`TEST_RUNNER_PERSONA_ULTRACODE_HUB`, screenshots with `TEST_RUNNER_PERSONA_SHOTS`).

## Launcher health in New Agent (#1643)

`Launcher.health` (`apple/Shared/Models.swift`) is the hub's record for the computer the list is for (`docs/server/launcher-health.md` "In the App"); a record it can't read is dropped, never the launcher.
`Launcher.block` / `LauncherChoices` (`Views/NewChat/LauncherAvailability.swift`, `LauncherHealthTests`) decide what is grayed out: only `blocked`, one line per failure class.
In `LauncherPicker` a blocked row is gray with the line (`LauncherBlockLine`) and an info mark; a tap opens `LauncherBlockedHelp` (why, How to Fix, the record under Details, Add Provider → `ProviderWizard`, Try Anyway picks it once the sheet has closed),
never picks it. The form says the line under the launcher row when the launcher that will run is blocked (Model Settings' default, or picked anyway); Start stays enabled. Run Agent Here picks a usable launcher first, lists blocked ones last in its menu under a section titled with the line (a picker menu drops an option's second line), and says it in its footer once picked.
Both read the list with `AppModel.launchersForStart` (fresh, not the cache). Add Provider is offered only for a computer in the hub-level group (the wizard saves there).
`StartingNote` shows a coded start failure (`Session.startErrorCode`) as Didn't Start over the hub's whole sentence. Screenshots: `docs/operations/testing.md` ("Launcher health in New Agent").

## An agent's live reply (#1309)

A Claude session Persona started sends its reply's lines as Claude shows them, before the transcript has them
(`docs/integrations/claude-integration.md` "Live replies (MessageDisplay)"); a runtime's session may send `delta` too. The behavior follows
the web's (`web/src/lib/live-text.ts`, `docs/web/web.md`).

- **Where it draws**: any chat of an agent (iPhone, iPad, the Mac app; not the watch) draws it under the newest items, the way the main chat
  draws the assistant's (`ChatLiveText`, `StreamingText` with its caret; the chat follows the end while it grows, `ChatStreamFollow`; while you
  read history it is held with the rest, `Frozen`). The running turn's words before it go into its work line meanwhile (`ChatLines`'
  `streaming`), as in the main chat, and the live work line stays above it. The main chat's look and rules are unchanged.
  This differs from the web on purpose: the web keeps a live turn's words in the flow with its steps and its live work row under the
  streaming words; here a turn shows only its latest words outside its folded work line, so the words before go into it once the new
  message lands anyway. Folding them when the new message starts streaming, not when it lands, keeps the chat from jumping each time
  the agent's next message comes (the main chat's reason, 2026-11-04); `ChatLinesTests.theRunningTurnsLatestWordsFoldWhileTheNextStream`.
- **Which deltas**: `AppModel.streamsAgentReplies`: a listed session that is not a Persona conversation, or a chat already given an agent's delta.
  An unlisted session's delta is handled as before (the assistant's rules), and its chat in the Agents style draws nothing. Both go through the
  same pacer (`DeltaPacer`, at most 5 draws a second, an empty delta at once).
- **What it shows** (`ChatStore.agentStreamed`, `Shared/LiveText.swift`, a port of `live-text.ts`, compiled for the watch too): the delta as it
  came (`agentLive`) is never drawn as it is, only what of it the newest items lack (`liveText`): the agent's latest message only (its words at
  the end of the transcript, nothing after them), only where a word ends, a cut item counting as its whole block (`full_size`); a late whole copy
  of the message before (its words, then a tool's result) shows nothing. Matching is by Unicode scalars and UTF-8 bytes like the node and the web.
- **Only the newest items touch it**: it is worked out again on a delta and on the newest items (`apply`, `replace`: pushes, the latest page,
  pulls), against the items from the latest exchange's opening on, never before the latest page from the end, with folds opened here folded again
  as the page brought them (`ChatStore.liveTail`). An older page (`prepend`) or an opened fold (`unfold`) does not change it.
- **No gap, no double**: a change that comes while the rows are being rebuilt waits for them and is published in the same pass
  (`agentLiveDue`), so the words leave the live copy as their row comes. `landed` / `clearLive` (the assistant's prefix rule and its 5 s hold) are
  not used for an agent's: the node sends `""` right after the items that replace it. A socket that starts over drops both (`dropLive`).
- **Only while subscribed**: the hub sends `delta` to a chat's subscribers alone, so the `""` that ends the live text while you are away
  from the chat goes to no one, and the hub sends it again on subscribing only while a message is being written. `AppModel.forgetAgentLive`
  drops an agent's live text (and what the pacer kept) when the chat's subscription ends and again when one starts, as the web's chat
  starts with none each time it opens; otherwise the words before a tool came back under a later message of the turn on returning.
- **Setting**: Settings › Claude Code Settings › Live Replies, per computer (`LiveRepliesModel`, `LiveRepliesSection`, the web's `LiveReplies.tsx`):
  `GET` on showing the page and on picking another computer, `PUT` on toggle with the new value shown at once; a failure puts it back and says why
  under the switch; with Apply to All Online Machines on, the other online computers change too (each through its own group's hub). An older hub
  (404) shows no section. On by default; a change reaches the sessions started after it. The copy says what it costs: the computer's terminal
  shows a reply a few lines at a time.
- **Tests**: `LiveTextTests` (the web's cases one by one), `AgentLiveReplyTests` (every order of the last delta, the items and the `""` with and
  without a rebuild between: never a gap or a double; the words after a tool; an older page, an opened fold; `AppModel`'s routing; leaving
  the chat and coming back),
  `LiveRepliesTests` (FakeHub). Screenshots: `MOCK_AGENT_LIVE=1` and `GET /mock/agent_live?step=…` (`scripts/mock/mock_hub.py`), the PR #42 review
  agent `-openSession macbook/a1f3c2d4-0000-4000-8000-000000000006`, the setting with `-tab settings -openClaudeSettings YES`.

## Session triage (Settings › Session triage, #3078)

Below Assistant, the same four toggles as web and Android (`GET / PUT /api/v1/agents/triage`, stored on the hub, consistent across clients, all off by default; meanings and whose tokens are spent in `docs/assistant/agent-work.md` ("Switches")):
master switch and Triage model (opens model configuration `agent.triage`), followed by cards for decisions, automatically continuing after errors, and scheduled wake-ups; disabled while the master switch is off.
Changes save immediately; failure restores the old value and explains why at the top; old hubs (404) show only unsupported. `TriageSettingsModel` / `TriageSettingsView`, shared with Mac.

## Translation (Settings › Translation, 2026-10-31)

With Automatic translation enabled, content not in the target language (Simplified Chinese by default) translates automatically; you can switch back to the original anytime.

- Scope: **Email** (on by default); **Lark chats** default to none (too many messages), translating only chats selected in Settings (`TranslationChatPicker`: pinned + recent chats, searchable),
  and only others' messages. Preferences stay local (`@AppStorage` `translate.*`, `TranslationPrefs`).
- The phone decides whether to translate (NaturalLanguage `NLLanguageRecognizer`, first 1500 characters; fewer than 6 letters skips detection; Simplified / Traditional Chinese count as one language, indistinguishable in short text).
  Results are cached by text in `Translator`; detection does not run inside row bodies.
- Translation runs in the manager (assistant process, `server/internal/assistant/inbox_translate.go`, moved with the Inbox from the hub on 2026-11-04; API forwarded through the hub, 503 if the assistant is absent),
  using the Text and email translation model purpose (user decision: no strong model needed, currently gpt-6.1-terra, about 2 seconds for two sentences).
  Email translates by reference (`POST /api/v1/translate/mail {account_id, uid, mailbox, target}`; the manager asks the hub to read mail, converts HTML with `mail.ReadableText` to paragraph-preserving text,
  body at most 12000 characters, excess marked `clipped`); Lark messages translate in batches (`POST /api/v1/translate {texts≤30, target}`; the App batches rows appearing within 300 ms).
  The manager caches in memory by model and content (model changes retranslate), the App caches for this run.
- **Translate on mail arrival, not opening** (requested 2026-11-01): the App sends Settings › Translation with device preferences to the hub (`PUT /api/v1/devices/{id}/prefs {"translate":{enabled,target,mail}}`,
  on connection and preference changes, `Translator.syncPrefs`). After each mail collection, the manager translates unhandled mail whose script differs from the target in the background (`foreignScript` in `assistant/inbox_translate.go`:
  by writing system; for Chinese targets, under 20% Han characters or any kana counts as foreign; Latin-script languages cannot be distinguished, so French is not translated for an English target), storing it in Inbox-item
  `translations[target language]` (also in the database, survives restarts). The list directly shows translated titles and summaries (translation icon by the title); opening mail retrieves this first via `translate/mail`, immediately.
  Only runs if some device enabled Translation › Email; disabling stops new translations.
- UI: mail titles and bodies become translations, with "Translated to Simplified Chinese · Show original" above the body; Lark bubbles have a gray translation line below (`LarkTranslationLine`).
- Screenshots / tests: `mock_hub.py` mail 504 is English; the Design review (f2) chat has English messages; `/api/v1/translate` returns "【译】原文" ("[Translated] original text");
  UI tests `TranslationTests`; DEBUG `-openTranslationSettings YES` opens Settings.

## Interface language (internationalization, #2457)

iOS, Mac, visionOS, watch and extensions support Simplified Chinese, English and 日本語 (shared rules in `docs/protocol/i18n.md`).

- **Entries**: String Catalog, development language `en`, with English source text or contextual English keys (#5090). Every entry has explicit en / zh-Hans / ja values; Chinese UI, unit tests and pixel / layout baselines retain their `zh-Hans` wording.
  `apple/Shared/Localization/Localizable.xcstrings` is shared across all targets (App, Mac, visionOS and watch compile it through `Shared/`; widgets add it separately in `project.yml`);
  shortcut phrases live in `Persona/Intents/AppShortcuts.xcstrings`; permission explanations in each target's `InfoPlist.xcstrings` (one each for iOS, Mac, visionOS, watch because wording differs). Runtime displays catalog text, not `Info.plist`: update both `Info.plist` and all three catalog languages for permission changes; `check` rejects en mismatches with `Info.plist` and `*UsageDescription` keys absent from the catalog (#4790).
  XcodeGen generates `knownRegions` from catalog languages; do not edit pbxproj manually. `Info.plist` `CFBundleDevelopmentRegion` is `en`: other system languages fall back to English.
- **Code**: SwiftUI `Text("…")`, `Button("…")`, `Label`, `.navigationTitle` already take `LocalizedStringKey`; every other user-facing `String` uses `String(localized: "…")`,
  App Intents use `LocalizedStringResource`. Interpolated sentences are one English entry (`"\(n) items"`), never assembled fragments; English plurals get one / other in the catalog.
  Dates and numbers use `FormatStyle`, never hard-coded `zh_CN` (machine formats still `en_US_POSIX`). When identical English has different Chinese or Japanese meanings, retain distinct contextual keys and explicit en values, or use `String(localized: "action.close", defaultValue: "Close")`.
  Non-UI Chinese (hub / Agent prompts and messages, protocol values, matching hub or terminal text, logs, previews, DEBUG pages) gets `// i18n-ignore` at line end; sample-data-only files add `// i18n-ignore-file: <reason>` within the first 20 lines.
- **After adding wording**: build once with `xcodebuild … SWIFT_EMIT_LOC_STRINGS=YES`, then `python3 scripts/i18n/apple_l10n.py sync <DerivedData>` adds new entries to the catalog (Xcode IDE builds do it automatically; CLI builds do not),
  then fill en, zh-Hans and ja, including symbols, numbers and names. `python3 scripts/i18n/apple_l10n.py check` (run by `scripts/test/test_apple_l10n.py`) checks every translation state, argument number/type, English key, unignored Chinese literal and explicit source-key reference. It also checks that every `Text`, `Button`, `Label`, `.accessibilityLabel`… literal in `Packages/BeautifulUI` has a key in the app's `Localizable.xcstrings`: the library looks its text up in the main bundle, so a missing key shows English in every language (#5574). The library itself is not changed; its keys live in the app's catalog.
  `apple_l10n.py strict <DerivedData>` remains available for compiler extraction. In `Text(condition ? "A" : "B")` (and `.accessibilityValue(…)`, `["A", "B"][i]`) both literals are `LocalizedStringKey`s: SwiftUI's `StringProtocol` overloads are `@_disfavoredOverload`, so the compiler types the literal branches as keys. Its extraction (`.stringsdata`) lists both, e.g. BeautifulUI's `"Show fewer actions"` / `"Show more actions"` (`SelectionActions.swift:220`) and `"Collapsed"` (`StreamingText.swift:244`, `SidebarNav.swift:181`), so each branch needs a catalog key. A branch that is a `String` value (`c ? title : "A"`) makes the whole expression a `String`, which is not looked up. `apple_english_keys_verify.py <base>` verifies that an English-key migration retains the existing translation trees after argument renumbering.
- **Choosing**: Settings › Appearance › Language: Match System (default) / 简体中文 / English / 日本語 (`AppLanguage`, `LanguageStore`, `Shared/Localization/AppLanguage.swift`).
  An in-App setting rather than only system per-App language: docs/protocol/i18n.md requires a common setting; Match System must use docs/protocol/i18n.md rules (French → English, Traditional → Simplified Chinese), which Bundle matching cannot provide;
  watch and extensions lack their own entry points; Mac's system per-App language is deeply buried. Both are the same value: the App writes its result to `AppleLanguages` (also used by the system's per-App language),
  and system-settings changes become a new choice on the next App open.
- **When effective**: Foundation chooses language at process start; `String(localized:)` cannot change at runtime (changing `Bundle`'s class or `AppleLanguages` only affected SwiftUI `Text`, leaving mixed Chinese and English),
  so choices **take effect next time the App opens**, explained in Settings; Mac has "Reopen now". Launch argument `-AppleLanguages (ja)` overrides for this run only, without saving (screenshots, tests).
- **Watch, extensions**: watch follows iPhone, which sends `language` in WatchConnectivity application context; watch writes its own `AppleLanguages`; before receipt, uses its system language.
  Widgets and Live Activities read App Group `persona.language.resolved`: extension processes run for hours, so their text uses `interfaceText(_:)` (`LocalizedStringResource` with that language)
  and `.environment(\.locale, LanguageStore.drawingLocale)`; App language changes affect their next refresh, watch next launch.
- **Tell the hub**: every HTTP request and WebSocket handshake carries `Accept-Language: <code>`; after connection send `{"type":"client_hello","locale":…}` (locale only; features / heartbeat / platform unchanged),
  `POST /api/v1/devices` includes `locale`, using this launch's interface language (`LanguageStore.launched`).
- **Code comments are English** (#5055): `python3 scripts/i18n/apple_comments.py check` (run by `scripts/test/test_apple_comments.py`) checks that comments under apple/ contain no Chinese or Japanese outside 「」; those brackets are reserved for exact localized text matched by code. Document references use English section titles, such as `docs/apple/ios.md` ("Interface language").
- **Tests**: scheme test actions pin `language: zh-Hans`, `region: CN`; unit, UI and pixel / layout tests all run in Chinese; `AppLanguageTests` covers parsing and storage.
- **Not covered yet**: hub-pushed text (Live Activity titles, details and terminal-needs-attention messages, push bodies, assistant replies) is localized by the hub; demo data (chats, names) is Chinese only.

## Design language

- Structure (2026-10-31, design in bui-adopt ARCH-PLAN):
  - `Shared/Design/`: tokens, also compiled by watch. `Theme` (font levels/emphasis, spacing, radius, line height, motion, color roles), `Icons` (`Theme.Icon`, `Theme.Brand`),
    `Palette` (base colors, also widgets/Live Activities), `StyleSpec` (composed value), `ColorSpec` (theme colors, migration), one file per style under `Styles/`.
  - `Persona/DesignSystem/`: iOS-only recipes/components: `Styling` (surfaces/buttons/page backgrounds from `spec.surface` / `spec.buttons`), `Components`
    (Chip, CountBadge, EmptyStateView, Monogram, `Loadable` / `LoadableContent`…), `Notice` (`NoticeBanner`: short notice atop page content,
    tone-colored caution/danger icon (default tone icon or specific meaning, e.g. waiting clock) + secondary gray text + optional button
    (below on phone, right on Mac), pale tone surface like chat `AttentionNote`; not dismissible, disappears when no longer applicable,
    used for Inbox Awaiting triage banner, #1773), `TextSize`.
  - **Add style**: a `ComponentSpec` in `Shared/Design/Styles/` (surface rendering, buttons, font design, component kit, canvas source, strokes, Lark cards,
    its own colors), `AppStyle` case, matching `component`. **Add theme color**: `ColorSpec` (accent, bubbles/text, darkening,
    washes manually tuned or derived), `ThemeColor` case. Other code reads `StyleSpec.current`, no `switch AppStyle` / `ThemeColor`.
    `StyleSpecTests` combines every style/color (including yellow avatar) and checks light/dark contrast.
  - Source checks (PersonaTests): `TypeScaleTests` (no system font styles/hard-coded sizes, emphasis via `labelStrong` rather than `.weight(` on a level,
    vertical grid spacing), `IconVocabularyTests`, `TokenRatchetTests` (per-file hard-coded padding/horizontal spacing/radius/hex colors/animations/fixed dimensions may only decrease;
    after reduction delete `TokenBaseline.json` and rerun to record baseline), `StyleSpecTests`. Debug warns on type checking over 150ms (release machine times out).
- Settings › Appearance (requested 2026-11-01, like Connectors): home group Avatar / Theme Color / Interface Style / Light & Dark / Layout (Light & Dark iPhone/iPad only, Layout iPhone only; App icon merged into Avatar 2026-11-05, no separate row, #3203), current value right, each opens its own page (`AppearanceSettingsView.swift`).
- Appearance has three independent choices (requested 2026-10-31: separate avatar, color and UI components; formerly style controlled colors/components,
  avatar overwrote accent, Clay then a baby avatar lost purple, Claude coral with Island cards impossible):
  - **Avatar** (`PersonaPicker`, `docs/apple/persona.md`): name, face, top-bar state dot/animation, no color change.
  - **Theme color** (`ThemeColorPicker`, `ThemeColor`, `ui.color`, default Match Interface Style): accent (actions, links, selections, tab tint),
    own bubbles/text, Lark links/own bubbles, Clay/Island background washes. Choices Match Interface Style (original colors, pixel-identical before separation),
    Match Avatar (only with avatar palette), System Blue, Coral (former Claude style), Lavender, Teal, Lark Blue.
  - **Interface style** (`StylePicker`, `AppStyle`, `ui.style`, default Liquid Glass): overlays/cards/buttons/fonts/chat components (BeautifulUI), expanded work card/timeline (`workList`), canvas source.
  - Choices compose `StyleSpec` (`StyleSpec.compose`, `StyleSpec.current`), read by Theme/Styling; changing any rebuilds RootView via `AppModel.look`.
  - **Canvas/card ownership**: glass styles (Liquid Glass, BeautifulUI) use system pages/cards; Lark uses its own light gray/white cards, unaffected by theme color;
    Clay/Island originally used colored washes (lavender/misty teal), poorly coupled to components (purple canvas/honey buttons), now **theme-color washes**
    (`ComponentSpec.Canvas.wash` / `.islands`, `ColorSpec.Wash`): Clay pages/cards/soft-block colored shadows, Island pages with white cards (wash cards in dark mode).
    Lavender/teal retain hand-tuned values; others derive from accent (light pages 90% white, cards lighter; dark pages darker than system grouped cells #1C1C1E).
  - **Claude is no longer a style**: only color differed from Liquid Glass, now Coral theme color, no duplicate style cards.
  - **Migration** (`AppearanceChoice`, first settings read without `ui.color`): old style → same style + Match Interface Style; Claude → Liquid Glass + Coral;
    selected avatar (formerly tinting) → Match Avatar. Writes both keys. Three visible changes: old Clay/Island+avatar users get avatar-color washes instead of purple/teal; Lark+avatar links/bubbles get avatar color; old Claude expanded work follows Liquid Glass timeline instead of gray cards.
  - `ThemeColorSettingsPage` sample uses live tokens (own bubble, reply card/link, two buttons), color dots below;
    style preview cards render each style + current theme color (Match Interface Style uses each own colors).

  | Style | Surfaces / buttons | Canvas | Own colors |
  | --- | --- | --- | --- |
  | Liquid Glass | Liquid Glass | System | System blue `#007AFF`, pale blue bubble `#DCEAFF` black text (dark `#1D3557`) |
  | Clay | Solid soft blocks: top-left highlight + bottom-right colored shadow, white stroke, rounded font, radius 24, soft buttons | Theme-color wash | Lavender `#7A5AF8` (dark `#8979EB`), purple bubble `#6759D3` white text, pale purple canvas `#EFEAF7` (dark `#0A0510`) |
  | Island | Floating white cards + large soft shadow, radius 26, soft buttons | Theme-color wash, white cards | Teal `#27747F` (dark `#2A9798`), ink bubble white text (reversed dark), misty teal canvas `#E6F1EF` |
  | Lark | White cards + hairline stroke, no shadow, radius 10, bordered buttons | Light gray `#F5F6F7` (theme-independent) | Lark blue `#3370FF`, pale blue bubble `#D1E3FF` black text |
  | BeautifulUI | Liquid Glass with BeautifulUI components | System | System blue |

  Coral (former Claude): accent `#D4683F` (dark `#CD6E45`), deep coral bubbles `#B24C25` white text.

  Readability floor (fixed once 2026-10-30, figures in persona-p1 themes-fix report; every style×color now must pass): white text on solid buttons/selected capsules ≥3.5:1 (dark accents cannot be too bright),
  own bubble white text ≥4.5:1, accent links on pages ≥3:1 (tightest wash pairing Coral/Clay 3.2:1); colored bubble gradients darken only (`Theme.deepening`), never brighten tops. Clay/Island solid buttons
  (`SoftButtonStyle`) use current tint, at least 44pt high: `.tint(Theme.danger)` Interrupt/Close Agent stays red in every style.
  Background-drawing scroll pages use `.themedGroupedPage()`, List/Form `.themedPage()`, otherwise Clay/Island/Lark canvas jumps on navigation.
  `Theme` reads current composition (Settings previews use `StyleSpec.compose`); panels `.surface(tint:interactive:in:)`,
  buttons `.themedButton(prominent:)`, list pages `.themedPage()`, plain rows `.themedRow()`, never direct `glassEffect` / `.glass`.
  RootView rebuilds on style/color with `AppModel.look` id (static tokens), current tab/chat already remembered. Lark framework colors
  (`LarkColor.blue` links/selections/new-message line, own bubbles) follow theme (Lark Blue restores gateway colors); card surfaces/strokes follow style; named Lark card-content colors unchanged.
  Debug `-ui.style glass|clay|island|lark|beautiful`, `-ui.color style|persona|blue|coral|lavender|teal|lark` (UserDefaults argument domain, Release supported, not DEBUG switches;
  `-ui.style claude` still Liquid Glass + Coral).
- **Light & Dark** (2026-11-03, #710): Settings › Appearance › Light & Dark, Match System default / Light / Dark (`ColorSchemeChoice`, `ui.scheme`, iPhone/iPad only).
  - Window-level `overrideUserInterfaceStyle` (`ColorSchemeChoice.apply`, every window/scene); choices through `AppModel.colorScheme`, new windows (launch,
    extra iPad scene) RootView `onAppear`. Sheets/dialogs/keyboards/WebView `prefers-color-scheme` follow window.
    Not `preferredColorScheme`: resetting nil may leave an open sheet's appearance; window `.unspecified` truly returns to system.
  - No whole-tree rebuild (`look` unchanged): trait-based dynamic colors redraw with window traits; `@Environment(\.colorScheme)` views update too.
    Explicit appearances unaffected (image editor/message menu dark; share cards/terminal own settings).
  - System surfaces still follow system: Dynamic Island/Lock Screen Live Activities, widgets/watch/notifications/launch screen (`LaunchBackground`).
  - App icons follow (requested 2026-11-03: light image for Light, dark for Dark): system chooses home-screen variant, App cannot,
    so Paper (Mochi/main icon) gets alternate sets `AppIcon-PaperLight` (both appearances light image), `AppIcon-PaperDark` (both dark, tinted unchanged),
    generated by `make_alt_icons.py` from `AppIcon`. `AppIconModel.follow` selects corresponding set for Light/Dark,
    Match System restores both-variant set (Paper main icon); avatar changes also respect choice (`AppIconChoice.iconName(for:)`).
    每次切换仍会触发系统的「图标已更改」提示。团子和奶蛙都使用默认图标，仅保留 Paper 的浅色、深色固定变体。
    `AppIconModel` remembers latest appearance: if changed during pending switch/system alert, rechecks and switches again to newest,
    never remains wrong (review of #714); `follow` also on every foreground (launch-argument appearance/interrupted switch),
    matched state does nothing, no alert. 系统仍记着已删除的备用图标时，主动切回默认图标；成功后不重复切换或提示。
    `AppGlyph` uses actually displayed home variant: Paper selected Light/Dark, other/Match System screen trait
    rather than window trait overridden by App. Debug `set-appearance` uses persisted `AppModel.colorScheme` too.
    `ColorSchemeIconTests` (demo) reads `probe.scheme` for actual page appearance, tearDown restores Match System after failure;
    iOS 26.0.1/26.3.1 simulators lack alternate icons (`supportsAlternateIcons` false), so verify home-screen switching on devices.
  - First verify Liquid Glass (lists, glass surfaces/buttons, sheets); other styles already adapt to system dark with same mechanism. Mac/Android lack option:
    Mac `NSApp.appearance` alone leaves List rows light (`MacSnapshot.colorScheme` comments), each window also needs `preferredColorScheme`, separate work.
  - Debug `-ui.scheme light|dark|system` (UserDefaults argument domain like `-ui.style`), `-tab settings -openAppearance scheme`.
- State colors never follow style; one `Shared/StatusPalette.swift` shared by App/watch/Live Activities: Needs you red, Working amber, Unread green, Seen gray, Not running pale gray, like herdr (`docs/protocol/agent-states.md`). Three values each: `dot` (dots/icons/fills), `text` (same hue darkened, small text on white ≥4.5:1), `tint` (pale background), via `AgentState.tone`. Questions/plans/permissions/terminal dialogs all Needs you red; working spinner amber, accompanying text secondary gray. Non-Agent states use their own `Theme` tokens: `Theme.Diff` (diff/Git ±, counts, fills, body black), `caution` (connecting/stale/truncated/version mismatch, triangle not dot), `ai` (✦ AI summary), `loop` (loops/waiting), `positive` (success/connected/online/completed to-dos), `Hue` (categories such as memory types). Permission labels neutral except Allow everything red.
- Agent row (`SlotRow`): state dot + title + time; latest sentence; metadata "Needs you · Claude · Space › Tab · Machine", regular secondary gray. Only Needs you written red; others dots only (Unread also bold title); former colored tab pill/state text/outlined Agent kind crowded first line and repeated status three times. Space counts colored dots + gray numbers.
- Tokens (`Theme.swift`, pages reference only): `Theme.Radius` badge 6/control 10/card 14/bubble 20/panel 22 plus capsule/circle;
  `Theme.Fill` well/chip/pressed (system quaternary/tertiary/secondary fill, visible dark too, no `Color.primary.opacity(…)`), plus drawer `scrim`;
  - `Theme.Font` levels at default size: display 28 bold, title 22, heading 17 bold, body 17, secondary 15, caption 13, sectionHeader 13 bold secondary,
    label 12, code 13 monospace; pages no direct `.subheadline` or `.system(size:)`.
    Mac same levels, Mac scale (#779, user selected body 13 on 2026-11-03): display 24 bold/title 18 semibold/heading 13 bold/body 13/secondary 12/caption 11/sectionHeader 11 bold/label 11/code 11 monospace,
    system fonts multiply `Theme.Font.scale` (`#if os(macOS)` in `Theme.Font`; Mac styles do not follow Dynamic Type, Small/Standard/Large body 12/13/14, one point each, #833; floor 11 so small 11pt levels remain 11, `Theme.Font.pointSize`; iPhone scale always 1). `Theme.Level.size` follows both, Markdown answers/profiles base `Theme.Level.body.size`, no fixed size.
  - Like BeautifulUI, hierarchy mainly weight/color: row titles bold body, section headers bold secondary caption smaller than row titles;
    Markdown H1 title, others heading separated by top whitespace; blocks code.
  - `Theme.Space` 4pt grid (2 4 6 8 12 16 24 32), all vertical `VStack(spacing:)`; horizontal beside button labels not globally changed.
    Page sides `Theme.Space.page` 16, aligned search/filters/group titles/cards/capsules, not individual paddings.
  - `Theme.Size` (requested 2026-10-31): named text-adjacent icons/dots/avatars/tap targets/thumbnails/label widths, default original points,
    `.scaledFrame(width:height:)` / `.scaledFrame(minWidth:…)` follows text up to 1.6× accessibility (44pt buttons not a third of screen).
    Lines/dividers/progress ≤4pt stay unscaled `frame`; geometric sizes unchanged; watch keeps own sizes, not App text sizing.
  - Dynamic Type + Settings › Appearance › Interface Style › Text Size (Small/Standard/Large, `TextSize`, `ui.textSize`) offsets system one level via RootView `.dynamicTypeSize`,
    changing text styles, `@ScaledMetric`, MarkdownUI and BeautifulUI together, ratios intact.
  - Lark messages/cards retain own sizes mapped to levels, similarly scaled by `LarkTextBase.scale` (`Theme.textScale`).
  - Fixed-frame icons cap `dynamicTypeSize(...)`; AI Markdown baseline 17pt (MarkdownUI scales).
  - Exceptions: user-sized terminal grid, Dynamic Island grid, fixed-image share cards, small-circle numbers/badges.
  - Monospace only character comparison/copying: code/commands/paths/branches/hashes/IDs/versions/terminal/tool output. Chinese explanations/values system font
    (session info chooses by row: model/branch/directory/version monospace, others system + monospaced digits; memory editor/Knowledge recent messages system).
  - `Theme.Icon` (`Shared/Design/Icons.swift`): one meaning per SF Symbol, pages `Theme.Icon.copy`, not `"doc.on.doc"`; drawn with
    `ThemedIcon` / `IconTile` so each style's icon language applies (`IconSpec`; rules, exceptions and checks in `docs/apple/icons.md`).
    Symbols used in two files must register; errors triangles (`error` / `errorFilled`), octagon only critical machine alerts (`critical`); Copy `doc.on.doc`,
    Paste `doc.on.clipboard`, Interrupt `stop.fill`, model/launcher `cube` (`cpu` only processor), code files `curlybraces`, `</>` only Agents.
    `IconVocabularyTests` rejects direct registered names, shared unregistered symbols and retired error symbols.
    External names (Lark card icon tokens) retain Lark rendering; line ends `// icon: <reason>`.
  - `Theme.Brand` (Assets Brands, sources/terms `THIRD_PARTY_NOTICES.md`): Claude/OpenAI (Codex)/Lark,
    Gmail/Google G/Linux distributions. Official originals, no recoloring/redrawing except monochrome distro templates tinted distro color. Used in share avatars
    (Claude/Codex, internal Codex fork has no public logo, green terminal remains), Inbox source/watch Lark badges, Gmail/Lark Mail accounts,
    Sign in with Google (Google spec: light white/gray border, dark #131314, not accent), machine `OSBadge`.
  - Connector/mail-provider/iPhone-source marks through `BrandMark` (`apple/Persona/DesignSystem/BrandMark.swift`, #1003),
    no direct `Image(…).resizable()`: fixed `scaledFrame`, padding-free assets (Lark viewBox cropped to bird),
    optical compensation `Theme.Brand.optical` (circles/wide marks fill, full squares 0.88), equal apparent size/center in columns;
    iPhone icons use `AppIconShape` radius + hairline, outlining white Calendar/Health on white cards.
    BeautifulUI uses `BrandTile` white background matching `BUIIconTile`; circular source avatar badges `BrandBadge`
    (circle logos fill, others `Theme.Brand.inCircle`). Boxes: Settings `icon`, detail header `wellM`, email account `wellXS`,
    source `wellS`, sign-in `iconXS`; new marks measure opaque bounds after `rsvg-convert` and crop viewBox whitespace.
  Lock Screen Live Activities capped at `.large` (~160pt height, clips beyond).
- Motion (`Theme.Motion`: state `.snappy` 0.25, layout `.smooth` 0.3): continuous only Needs you dot breathing and system working spinner;
  Reduce Motion (`accessibilityReduceMotion`) stops dot (and loading/preparing placeholders), working dot static outer ring.
  No wobbling icons/spinning gears/pulsing avatars/spinning to-dos. Counts `.numericText`; no `.shadow` outside `.surface` (glass/Clay/Island already shadow,
  doubling otherwise); neighboring glass keys/top Back/⋯ use `GlassEffectContainer`.
  - An extension's View drawn again (#37, as on the web and Android; `Views/Settings/ExtensionMotion.swift`): under the same key, a `metric`, `stat_grid`
    or row value whose numbers alone changed rolls (`.numericText`, counting down when the number fell); any other change of a value or a badge's words fades in,
    and a badge's tone changes color (`Theme.Motion.state`). Blocks whose key is new fade in, gone ones fade out, and chart bars and lines move to the new values
    (`Theme.Motion.layout`). Nothing moves on the first draw or with Reduce Motion; at rest every value is exactly the extension's text. Mock: `MOCK_EXTENSIONS=1`,
    `-openExtension showcase -openExtensionPage motion`; `GET /mock/motion/next` draws its other step (`ExtensionMotionUITests`).
- Voice orb (`DesignSystem/VoiceOrb.swift`, #3506, voice design #3489): centered call-bar ball; pages pass only phase (`VoiceOrb.Phase`: not started/connecting/listening/hearing you/speaking/listening with Agent working/paused/muted/ended) and volume 0…1, no style decisions.
  - Motion: listening slow breath (`Theme.Motion.voiceBreath`, 1.6 s one-way), speaking audio beat (`voiceBeat`, 0.42 s one-way), matches Android `Voice.kt` (`VoiceOrbTests` reads/checks); hearing scales by volume, connecting/Agent working arcs (`voiceSpin` / `voiceLap`). `TimelineView` clock ≤30fps (`voiceFrame`, like Needs you dots/avatar glow), static phases paused; Reduce Motion or Low Power one static frame per phase (like avatar dot), Reduce Motion switches phase without scaling. One of few continuous animations, only during calls.
  - Rendering: Liquid Glass/solid styles highlight sphere, pale avatar accent (`Theme.persona`) listening, accent speaking; BeautifulUI flat disc + five volume bars like dictation, filled accent speaking, white bars (near-black on pale/yellow accent). Connecting arc accent, working arc amber `Theme.busy`; paused/muted/ended gray, `pause` / `voiceMute` icons on nonhighlight gray with white contrast ≥3:1 (`VoiceOrbTests` light/dark).
  - Size `Theme.Size.voiceOrb` 52 including glow/enlargement, sphere 0.7, text-scaled; everything inside frame, glow never enlarges with ball, gradient sphere edge to frame edge, not covering neighboring text/buttons. `voiceLevelBar` / `voiceLevelGap` / `voiceLevelHeight` scale proportionally; `Shared/Design/VoiceTokens.swift` also watch-compiled, no watch voice UI.
  - Speaking with `interrupt` is a button labeled Interrupt voice: stops speech only, not Agent (separate Stop Claude call-bar button); otherwise decorative, `disabled`/no keyboard activation, VoiceOver skipped, status in adjacent text.
  - Call icons `voiceMute` / `voiceUnmute` (same crossed mic, filled unmute, Phone-like toggle), `voiceEnd` (`Theme.danger`), `voiceCaptions` full captions, `voiceTimbre` voice; entry `voiceChannel`.
  - Mac continuous clocks relayout whole window each frame (`docs/lessons/macos.md`); no Mac voice yet, future animation Core Animation.
- One badge/count recipe (`Components.swift`): noninteractive `Chip` neutral (gray/pale: model/version/tab/direct chat),
  tinted (meaning + pale fill: `.tone` state, @me red, Recommended AI purple), outlined (Claude/Codex kind); `CountBadge` white on
  gray `Theme.countBadge` or accent, or state text on tint, 99+ over 99. Interactive filters/tabs/modes/sub-Agents are controls, not these.
- BeautifulUI (`apple/Packages/BeautifulUI`, vendored copy, source/update in `VENDORED.md`, no local library edits):
  **BeautifulUI style only** (`AppStyle.usesBeautifulUI`, Liquid Glass surfaces/theme colors), other styles retain Persona thinking/tool/work/input UI.
  Direct replacement 2026-10-30 (#93/#94) judged ugly by user, reverted and made this theme. In it:
  - `ThinkingState` (✦ + first line, expanded body right of guide).
  - Runs of ≥2 tools in expanded work group into chip row (`ChatLine.toolRun`, `ChatView.runLines`, `ToolRunRow`).
    Per-tool chip: one names target (Read WeatherStore.swift), multiple count (Run 6 commands), edits +N −N from shared `ToolDiffMemo`, failures red.
    Tap expands individual `ToolChipRow`, then tap params/output; single call direct row.
    Background calls stay separate so Background · Done / Failed remains visible (`ChatView.foldsIntoRun`).
    Jumping from background notice expands containing run first.
  - Diff/commands/output/Markdown code stay Persona `DiffView` / `CodeBlock`.
  - `BUITheme.persona(spec)` (`BeautifulUIBridge.swift`) derives colors/radius/type from current `StyleSpec`, injected RootView:
    - Tertiary text also secondaryLabel (tertiaryLabel only 1.7:1 on cards);
    - Code keywords `Theme.Hue.blue`;
    - Fonts ×1.2 (library desktop sizes), code ×1;
    - Entry animations off.
- Haptics: successful send/answer success, new interaction warning, error error.
- Icon: Paper (#709, chosen over several proposals 2026-11-03, replacing night/coral halo/four-point star): hand-drawn little white waving figure,
  heavy black outline/coral blush, Manus-like handmade icon, warm cream background (darker than website paper, clearer edge on light home screens); dark same figure on warm charcoal,
  tinted grayscale black. Same iPhone/watch/Mac image. Generated and selected, not script-drawn: `AppIcon.appiconset` `icon.png` light / `icon-dark.png` dark
  are source assets; `scripts/assets/make_icon.py` derives `icon-tinted.png` (grayscale, outer background pure black), watch light icon (system circular crop),
  Mac sizes and a temporary icon preview; replace both sources and run it plus `make_alt_icons.py` for Settings previews.
- **形象与 App 图标**（#6220；iPhone / iPad 可切换图标，手表和 Mac 保持一个图标）：
  - 只保留团子（`persona`，`AvatarDesign.mochi`，默认）和奶蛙（`nwverse`，hub 自带形象包）。`PersonaSelection` 在读取选择时把旧形象和未知 ID 归一为团子；`PersonaStore` 对本地缓存和 hub 列表使用同一白名单。
  - 团子和奶蛙都使用默认 Paper 图标。面罩、汇流、像素、晶面、软糖的绘制、备用图标和预览资源已删除；未上架的 Spark、Halo 绘制也不保留。
  - 旧偏好不因显示回落而删除，供回装旧版使用。原生迁移由 #6152 独立处理；迁移后的旧形象 ID 仍须经过这层白名单再显示，不能绕过回落策略。
  - `scripts/assets/make_alt_icons.py` 只生成 `AppIcon-PaperLight` / `AppIcon-PaperDark` 和 `AppIcons/app-icon-default` 预览，不再编译形象渲染器。
  - `ASSETCATALOG_COMPILER_ALTERNATE_APPICON_NAMES` 仅列出 Paper 的两个固定外观变体；`AppIconChoiceTests` 检查图标资源与构建配置一致。
  - `AppIconModel` 读取系统的 `alternateIconName`。已删除的备用图标会通过系统接口主动重置，失败时保留错误、下次前台重试；成功后不重复提示。
  - `PersonaSelectionTests` 在 iOS、Mac 验证旧选择回落和默认图标同步；`PersonaPickerTests` 验证两项选择、奶蛙下载、切回团子和旧选择启动后的回落。`persona-icon-note` 为当前图标的无障碍值，系统不能切换时为 `unavailable`。
- Settings top/welcome-name/sidebar-main marks use current avatar App icon (`AppGlyph`, home-screen light/dark via `AppIconChoice.homeScreenLook`, avatar packages own face), changes with avatar (#709; before #1312 always Paper, before #3203 followed separate App icon setting/sidebar hard-coded Paper).
- Brand coral (`Theme.brand`) only Persona marks: chat-home icon, Lock Screen four-point star. Actions use current `Theme.accent`.
  Both AccentColor assets (watch titles/tint, system dialogs) system blue; iPhone/iPad window `tintColor` follows theme (`WindowTint`, UIKit List icons, #1188); Live Activities cannot read style, stroke/buttons fixed system blue, button deep `#0071E3`, white contrast 4.7:1.

## Data flow

- Logic-layer choice (2026-10-31): pages with "load → display → edit → save" use MVVM: an `@Observable` `XxxModel` beside the page,
  with hub access, errors, toasts and navigation passed through `Deps` (`live` connects to `AppModel`); the view keeps only its own state (what is being edited, what is presented). The first is
  `AssistantRoutinesModel`. In event-heavy areas with ordering problems (chat pages, Lark chat pagination), extract pure calculations first: `ChatLines` (messages → rows),
  `AgentListOrder` (Agents list sorting and grouping), then convert to a reducer.
- Unit tests never connect to the hub: replace `APIClient.transport` with `FakeHub` (PersonaTests), which returns fixed JSON by "method path" and records requests; view models use real requests and decoding.
  Demo mode still uses the global `APIClient.local`.
- `AppModel` (`Persona/App`): pairing stored in Keychain; `LiveConnection` (WebSocket, exponential-backoff reconnect, resubscribing with `since_seq` after reconnect);
  one `ChatStore` per session (deduplicated by id, sorted by seq, `buildRows` produces display rows).
- Entering a chat page calls `subscribe`, leaving calls `unsubscribe`; the hub pushes `items` / `screen` / `sessions` / `interaction`.
- Phone tools: hub pushes `tool_call` → `PhoneToolRegistry.call` (writes require confirmation first) → `tool_result`. On connection, `register_phone` declares tools (`docs/apple/phone-tools.md`).
- Push: after pairing, request notification permission and register APNs, `POST /api/v1/devices`; when the hub has `apns.json`, it pushes new interactions and Agent completion (completion waits 1 minute, is skipped if seen on a computer or in the App, and waits for background tasks to finish; push policy in `docs/server/server.md`), with "Title" (no ✅; interrupted or failed is "⚠️ Title"),
  subtitle "Machine · Space › Tab", and the last reply as the body. No banner when the App is foregrounded and showing that session. Simulator automation (`-pairURL`) neither requests notification permission nor registers as a phone.
  Interaction pushes carry buttons (`NotificationActions`, category is the hub's `INTERACTION_<KIND>`): permissions "Allow / Deny", plans "Approve / Request changes" (changes can include typed feedback). Completion category `SESSION_DONE` supports direct "Reply" (#3449): unlock first, then send a normal message to that session in the background (new `client_msg_id`); failure produces a local notification explaining why. When a session appears on screen (`AppModel.show`), its old notifications (same `thread-id` and `machine_id`) are removed from Notification Center.
  Affirmative buttons require unlocking (`.authenticationRequired`); Deny does not. Plan approval from a notification uses `acceptEdits` (the whole plan has not been viewed, so commands still need individual approval),
  and "Approve and run automatically" is App-only. Buttons call resolve directly in the background without opening the App; failure (already handled, cannot reach hub) produces a local notification explaining why. Questions and terminal interactions have no buttons; tap to handle them.
  Settings › Notifications (`NotificationSettingsView`): "When you are needed" toggle, "When an Agent finishes" never / long tasks only / every time, Live Activities (Lock Screen and Dynamic Island / show details on Lock Screen / what each side of Dynamic Island shows, `IslandSettingsView`, `docs/apple/live-activity.md`), stored per device on the hub (`PushRegistrar.deviceID`).
  Tapping a notification opens its session: use the completion-handler version of `UNUserNotificationCenterDelegate`, switch to the main queue to handle it, then call back (the async version returns on a background executor,
  UIKit receives completion off the main thread and a notification-triggered cold launch crashes).
- Watch pairing: iPhone sends pairing again after WCSession activation (sends before activation are lost), also using transferUserInfo for guaranteed delivery; the watch's unpaired page offers "Sync from iPhone"
  (sendMessage request, iPhone replies with pairing); iPhone Settings has "Sync to Apple Watch".
- Watch: iPhone uses WatchConnectivity to sync pairing and a summary (running sessions, pending interactions, Inbox items needing you); while open, the watch directly requests a hub refresh every 8 seconds,
  and answers / sends messages / interrupts directly through the hub too.
  UI hierarchy: each row's main content (question, session name, Inbox title) uses headline; type and location use footnote; messages and plans strip Markdown markers (`WatchText.shown`),
  bubbles fit text width, own messages align right; before any data arrives show "Loading"; when the hub is unreachable, a top row says "Can't reach the hub" + "Retry" (with old data, say how old it is).

## Demo mode (see every feature without a hub, 2026-10-30)

TestFlight has a public link and must pass Beta App Review: previously an unpaired App showed only the pairing page, leaving reviewers and friends who had not set up a hub unable to see anything (potential rejection under 2.1).
The pairing page's second button, "No hub? Try it first", enters demo mode: the App runs on built-in fake data and connects to no server (available in Release, not a DEBUG switch).

- **Below the data layer, no separate pages**: demo pairing uses `ServerConfig.demo` (`https://demo.invalid`, which never resolves, so even missed requests cannot reach the network).
  `APIClient.request` (and `image` / `fullItem`) routes it to `APIClient.local` instead of URLSession; `LiveConnection` opens no WebSocket for it,
  using a local socket made by `LiveConnection.makeLocal`. Both are registered and answered by `DemoHub` (`Persona/App/DemoHub.swift`), returning the same JSON as the hub,
  so pages, `ChatStore` and `AppModel.handle` do not know they are in demo mode. Missing endpoints return 404 "This data is not available in demo mode", with a `DemoHub miss` log in DEBUG.
- **Data**: `scripts/mock/make_demo_data.py` exports `apple/Persona/Resources/Demo.json` from `scripts/mock/mock_hub.py` (the mock hub for UI tests), then adds several Agents
  (covering every state and several spaces), Lark chat messages, email bodies and knowledge entries; the terminal is a Claude screen laid out for phone width (`phone_screen`; 96-column frames wrap every line on a phone).
  Everything is fictional; times are relative (`@rel:<seconds>`, `@unix:<seconds>`) and converted near "now" on load, keeping the demo fresh. Machines-page charts are calculated by `DemoHub` (a port of `mock_hub.metric_at`).
  When mock data changes, rerun the script and commit the JSON; `DemoHubTests` decodes every endpoint through `APIClient` into App models, catching model shape changes here.
- **Side effects**: messages to Agents / Persona echo locally, then after 1.5 seconds the "Agent" replies "This is demo mode…"; answering a question / approving a plan removes the card, the Agent keeps "working" and replies with the same text,
  with an extra "Nothing is actually sent in demo mode" notice; new Persona chats are created locally. Other actions that touch computers, Lark or email (new sessions, interrupt, close, terminal keys, sending email, run now…)
  fail with `APIError(code: "demo")`; `AppModel.show` displays a blue "Nothing is actually sent in demo mode" notice rather than a red error, and pages do not show success messages such as "Session closed".
  Local-state-only actions (seen, pins, mute, scheduled-task toggles, memory, handled Inbox items) work normally and are discarded on exit.
- **Indicator and exit**: the Settings tab has a "Demo" badge; its top says "Demo mode · All data is fictional", followed by "Connect my hub" / "Exit demo mode", hiding the server and watch sections.
  Exiting returns to pairing; successful pairing (QR scan, manual, `persona://pair` link) leaves demo mode automatically. Demo mode is stored in `demo.on` and survives App restarts, always resetting to shipped data on entry.
  Entering / leaving clears demo-session drafts and chat caches (demo session ids are the mock ids and can collide with drafts left by other UI tests on the simulator), and the Inbox; the Persona tab opens the demo main chat.
- Demo mode never requests notification permission, registers APNs, calls `register_phone`, sends pairing to the watch, or changes device credentials.
- Review notes (`app_store.external.review.notes`) explain entering demo mode from pairing.

## What the main chat still needs (#4193, onboarding M1)

With hub feature `assistant_readiness`, the App reads `GET /api/v1/assistant/readiness` (`AssistantReadiness`, `apple/Shared/AssistantReadiness.swift`;
state in `AppModel.readiness`, `AssistantReadinessModel`), using the same presentation as the web (`../web/web.md` ("What the main chat still needs")):

- Query while Persona is foregrounded (`.tabActiveTask`), when machines change, from `warm` responses, after chat creation is refused, and after saving a provider (`readinessChanged()`). Old hubs (no feature or 404) show nothing.
- Above the new-chat input (old `AssistantHome`, new-shell `ShellHome`), a `ReadinessCard` (design-system component, BeautifulUI list rows):
  `setup` shows the first missing step; `rate_limited` shows only a card without blocking Send. In `setup`, a nonempty draft replaces Send with `SendGate` ("Connect a model first", "Add a computer first"…); tap opens that step, retaining the draft without queuing it.
- Actions (`ReadinessRoute`): add / confirm computer first opens Machines (switches to Add Computer after M3 lands); connect model opens provider wizard, replace key opens that provider;
  install / log in to Claude Code, wake computer and `local` sample questions open local instructions (`ReadinessHelpView`, not sent to a model); Try a message queries `?probe=1`. Unknown `action` shows text only.
- Hub-coded refusals to open a chat or send (`no_machine`, `claude_missing`…, `CodedFailure`) show `CodedFailureCard`, original text and request ID under Details, no toast; `assistant_offline` silently retries three times.
- A Persona chat's last turn with `turn_error_code` (`not_logged_in` / `auth_failed` / `model_route`) replaces the final "Not logged in" with a "Model not connected correctly" card and Fix it.

## Welcome screen and post-pairing checklist (#4195, onboarding M2)

- Welcome (`WelcomeView`) shows no commands: the main button is Scan to connect hub (Mac: Paste pairing link), followed by where to find the code (the screen after installing the hub, or Settings › Devices › Connect a new device on a connected device);
  No hub yet? opens the public page from build configuration `publish.getting_started_url` (Info.plist `PersonaGettingStartedURL`, hidden if absent), alongside Try it first and Other ways.
  Manual entry, SSH tunnels and command-line instructions appear only under Other ways in `PairView`. Expired or used codes (401 `invalid_token`) say "This code expired or was already used; generate another", with Scan again on iPhone; externally opened pairing links behave the same (`AppModel.showPairing`).
- After pairing (`AppModel.checklistPending`, after engine selection), when the hub has `assistant_readiness` and the user has not chosen Later, the page becomes `OnboardingChecklistView`:
  Computer, Claude Code, Model and Try a message, using readiness `state` for checkmarks, crosses, spinners and question marks, with the same actions as main-chat cards (`ReadinessRoute`). Later calls dismiss;
  when all are checked, show Get started. Settings top "N steps left to get started" (`ChecklistSettingsRow`, N counts unfinished computer / claude / model steps) returns to the checklist. Old hubs add no post-pairing checklist.

## Connect a new device (#3077)

"Connect a new device" at the top of Settings › Devices follows the web (#2731, `docs/web/web.md` ("Settings")): request a one-time temporary pairing code from the hub (`POST /api/v1/auth/pair-codes`, with `{"web": the hub address}`,
`docs/protocol/protocol.md` ("Temporary pairing")), so the new device does not need `personad hub pair` run on the hub machine. The card (`Views/Settings/ConnectDeviceView.swift`, logic in `ConnectDeviceModel`) has two modes:

- Persona App: pairing QR code (hub-generated `qr.rows`, `QRCodeView`), the temporary code itself (selectable), "Copy pairing link" (paste to pair Mac and Android).
- Browser: a login-link QR code (scan with the phone camera to open directly in the browser) and "Copy login link"; if this device connects to the hub through `127.0.0.1` / `localhost` (SSH tunnel), warn that other devices cannot open it.
- Countdown (every second); at 0 show expired and "Generate again"; while waiting for pairing, read the device list every 3 seconds. When a credential absent when the code was generated appears, show "Connected: name", refresh the device list, and offer "Connect another".
- For watches and devices connected with a temporary code themselves, the hub returns 403; the card shows the hub's message and "Retry". Old hubs without a QR code show only the temporary code and link.

Screenshots: `-openDevices YES -openConnectDevice YES` (`web`: open the Browser mode); mock hub `MOCK_PAIR_SECONDS` shortens validity, `POST /api/v1/auth/tokens` simulates a new device connecting.

## Add and confirm computers (#4196)

Like the web (#3934) and Android: add a computer in the App and confirm that it is yours (`docs/assistant/onboarding.md` 4.2, 5.3; APIs in `docs/protocol/protocol.md` ("Computer pairing codes", "Computer trust")).
One form (`Views/Machines/AddComputerView.swift`, logic in `AddComputerModel`): choose OS (Mac / Linux / Windows), enter a name (Chinese allowed, at most 40 characters, no id) →
`pair-codes` with `add` (added in the hub-level group) → one screen per OS: Linux shows the hub's one-line install command (otherwise falls back to the pairing command and says to install Persona Link first); Mac shows installation instructions and share / copy pairing link;
Windows shows the PowerShell line → countdown (the `ConnectDeviceText` algorithm; after expiry "Generate again" uses `{"machine": id}`), read the machine list every 3 seconds → when it connects and is pending confirmation, the same screen asks
"A computer wants to join: <name> (macOS). Is it yours?", "It's my computer" / "No". Errors use Chinese text by `code` (`AddComputerText.failure`); `tls_required` uses the hub's message, and unknown codes show the hub's `error`.

- Entry points: the Machines page's empty state and toolbar "+", the Agents empty state with no computers, Mac's File › Add Computer…; only shown when the hub has `machines_add` (otherwise the Machines page keeps its original text).
- Other pages can open it with `AppModel.openAddComputer()` (readiness `add_computer`), `AppModel.openConfirmComputer(ref)` (`confirm_computer`; without a machine, use `pendingToConfirm()`: online pending computers first),
  both setting `AppModel.addComputer`, presented by `.addComputerPresentation()` at the root; pages that are themselves sheets use `.addComputerSheet($request)` on themselves.
- Lists and the Machines page: `trust == "pending"` comes first, labeled "Waiting for your confirmation"; online offers "It's my computer" / "No", offline offers "Delete" (buttons only with hub feature `machine_trust`); `error_code` gets Chinese text
  (`AddComputerText.machineProblem`), with the hub's raw message in Machines-page Details. New Agent (and handing a to-do to an Agent, starting one from mail / Knowledge, all through `NewChatView.offered`) excludes pending computers.
- Screenshots: `-showAddComputer YES` (`add:<mac|linux|windows>:<name>` fills and submits; `<machine id>` opens confirmation for that machine; Mac uses the same arguments with `scripts/app/mac_shot.sh`); UI tests `AddComputerTests` (`docs/operations/testing.md` ("Readiness scenarios")).

## Mac signed in (2026-11-01)

Like WeChat's "Mac WeChat signed in": when Persona on a Mac is connected to the hub, the top of the Agents list says "Persona is signed in on Mac" (with a count for multiple Macs), with "Phone muted" on the right when enabled;
not shown during search. Tapping opens a card (`Views/Settings/DesktopPresence.swift`):

- Phone muted: while a Mac is online, this iPhone no longer receives "Needs you" or "Agent finished" pushes; the Mac alerts you instead. Reminders the assistant sends at your request still push. Stored in this device's push settings (`mute_while_desktop`), off by default.
- Each Mac's name and sign-in time, "Sign out of Persona on this Mac": revoke its device credential, and the hub disconnects it immediately; it must pair again to use Persona. A Mac connected directly with a pairing credential cannot be signed out individually.

Placed at the top of the Agents list rather than the shared top bar: a persistent line in the two pages' shared bar would cover content; a list-top row scrolls away, like WeChat. Mac registers as a desktop, not a phone (`docs/apple/mac.md` ("Mac is a desktop, not a phone")).
Screenshots: `-showDesktopSample YES` (fake one Mac with phone muting enabled), `-openDesktopSheet YES` opens the card.

## Tasks dispatched by the main chat notify only there (#1731)

Settings › Assistant › Task notifications toggle (hub `main_tasks_quiet`, on by default, `GET / PUT /api/v1/assistant`; hidden if the hub does not return the field).
When on, tasks dispatched by the main chat are quiet (to-do `quiet`, attached sessions also carry `quiet`); the hub sends messages into the main chat and the assistant tells you. The hub itself sends no push or Live Activity. Clients exclude them here:

- Live Activities: before merging, `AppModel.mergedFleet()` excludes quiet session entries and subtracts their counts (`FleetState.leavingOut`), covering fleet states predating the flag.
- Foreground notifications: `willPresent` shows no banner for quiet sessions.
- Badges: quiet-session questions count toward the assistant, not Agents (handled in the main chat); the drawer / Mac sidebar's To-dos count excludes quiet tasks (`TodoStore.badgeCount`), though the to-do list still includes them.
- Watch: both the phone-synced summary and the watch's directly fetched lists exclude quiet sessions and their questions (watch credentials cannot read to-dos and use only session `quiet`).
- Mac: notifications and Dock badges treat them as muted; the menu bar excludes them.

`AppModel.isQuiet` decides: first the hub's session `quiet`, then the current hub's to-dos (`QuietTasks`: quiet to-dos and their Agents, including ones detached from the to-do). Task cards in the main chat and the Agents-page list still display normally.

### Messages the hub injects into chats (#2358)

Task notices the hub sends to the assistant, along with scheduled tasks, follow-ups, wake-ups and node-restart explanations, are user text in the transcript (protocol in `docs/protocol/protocol.md` ("Messages injected by Persona")).
Apple clients do not draw them as user bubbles or show the model's instructions:

- Recognition: `Injected.of(item)` (`Shared/Injected.swift`) first checks the hub's `item.injected`; on old hubs without the field it recognizes text using the same rules as the hub's `prompts.ParseInjected`,
  tested by `InjectedTests` with shared fixtures `spec/fixtures/injected/cases.json`. App-sent messages (with `client_msg_id`) and messages from another session (`from`) do not count.
- Chat page: `ChatView.rowView` draws `InjectedLine`, a centered capsule like the "Answered" row (`AnsweredLine`), with an icon and `line`. `InjectedLook` determines icons and colors, shared by phone, Mac and watch:
  finished is a green checkmark, waiting for you an accent-colored raised hand, interrupted or failed to start a red warning, scheduled tasks an alarm clock, wake-ups a bell, node restart gray information.
  Tap a task notice for task details: use the hub's `todo_id`, otherwise find the newest same-name task created by this chat; without a match it is not tappable.
  A scheduled-task or follow-up row can be opened to see the task's actual request (`detail`).
- When a turn has multiple messages, each gets its own row; the injected one uses the centered row above.
  Nor is it treated as delivery of a pending bubble (`PendingMessage.arrived`).
- Watch: a small centered line (icon plus `line`), no bubble (screenshots: mock `MOCK_INJECTED_WATCH=1`, watch `-openWatchItem session`).
- Chat-list previews use the assistant's words (`last_text`), which already exclude these messages.
- `ChatCache` bumped to v9: old cached items lack `injected`.

## New conversation layout (#1666)

Settings › Appearance › New Conversation Layout (`ui.newChatLayout`, on by default; a key of its own, not the shell's Layout switch) tidies Persona's main conversation on iPhone and iPad.
Off, the conversation is drawn exactly as before. The Agents chat, the Mac and Vision Pro never use it (`ChatLayoutSettings.layout(assistant:)` returns `ChatLayout.off` there).
`ChatView` reads the choice once and hands rows `\.chatLayout` through the environment, so no row observes UserDefaults (`Persona/Views/Chat/ChatLayout.swift`).

- **Who talks to whom**, from the hub's existing fields only: feed `routed` that Persona wrote (`from` `assistant`, #1542) reads "Persona → @task" (`RoutedExchange`; off, #1542's folded `AgentSentLine`), what the user sent a task (`routed` without `from`) "Me → @task" in the user's box (`UserRoutedExchange`), feed `report` reads "task → @Persona" (`ReportExchange`),
  and injected `system` / `supervise` (the supervision digest) and `followup` (scheduled runs, follow-ups, wake-ups) read "Supervision · Persona → @Persona" (`Injected.isPatrol`, `InjectedLine.patrol`): Persona writing to itself,
  on Persona's side, never where the user's messages are. Task notices, node-restart and Inbox lines keep their centered line. The addressee is in the accent; there is no group name, member list or avatar per speaker.
- **Boxes**: every message sits in a box, the user's pale blue (`Theme.ChatBox.user`) and Persona's, its exchanges' and its notes' pale gray (`Theme.ChatBox.persona`), words in the primary color in both modes (`ChatLayoutTests` checks the contrast). The answer streams in the box it settles in.
- **Folding**: what Persona and a task say to each other shows its first N lines (3 by default) with Expand / Collapse under it (`ExchangeText`, measured against the whole text, so a short one has no button); the setting offers Folded / Show N Lines / Expand All.
  No evidence list under a report (it was not tappable); web links in the words are links (`ExchangeText.linked`).
- **Right-edge navigator** (`ChatEdgeScrub.swift`), like the ChatGPT desktop app's: a slide up or down that starts in the band along the list's right edge (`Theme.Size.edgeBand`, inside the list's side margin, below the top bar) shows one tick per message the user typed
  (not injected lines, other conversations' messages or /loop ticks), a light tap as it opens and a selection tick per message passed, with the picked message's first two lines beside it; letting go brings it to the middle of the list and lights it up.
  It is a UIKit recognizer: it fails at once for a touch outside the band and for a slide more sideways than up-and-down (the shell's pager and the drawer keep those, `DrawerPan`), and the list's scroll view pan waits for it to fail (`shouldBeRequiredToFailBy`); the system's back swipe is at the other edge.
  With more messages than fit, the newest that fit are shown. `-showEdgeScrub N` holds it open on the Nth newest message for screenshots; `-openAppearance chat` opens the settings page.
- Tests: `ChatLayoutTests` (unit: default, launch-argument off, old look outside Persona's conversations, folding, patrol kinds, tick placement, links, contrast); `NewChatLayoutTests` (UI, mock `MOCK_EXCHANGES=1`, see `docs/operations/testing.md`): headers, folding, no evidence list, baseline conversation presentation with `-ui.newChatLayout NO`, a slide along the edge; `ChatFollowTests.testPersonaReopensWhereItWasLeft`: the reading position after a restart.

## Connection status (slow / offline networks)

Like Telegram: network trouble shows no banner; **the page title becomes "small spinner + status"**, then silently returns when recovered (requested 2026-10-29; the old list-top
`ConnectionBanner` was removed).

- Three states (`Persona/App/ConnectionStatus.swift`): **Waiting for network** (`NWPathMonitor` says the phone is offline), **Connecting…** (network available but WebSocket disconnected,
  reconnecting, or connected but ping unanswered for 5 seconds / answered after more than 5 seconds = slow network), **Updating…** (just connected, catching up: complete only after pending interactions and backlog for open chats arrive,
  at most 8 seconds). Unpaired and backgrounded Apps (socket intentionally disconnected) are not problems.
- Debounce: trouble must persist for 1.5 seconds before showing; reconnects completed within a second, such as foregrounding or switching networks, show nothing; restore the title immediately on recovery.
- **A connected socket drops: stay silent first** (invisible hub restart, #1634, `docs/operations/hub-restart.md`): `LiveConnection` records the drop (`drop`),
  and during grace (`inGrace`: 8 seconds normally, 30 seconds after `hub_restarting` or close code 1001) still counts as connected: titles, status rows, Settings dots and the assistant page's device icons remain unchanged,
  messages send normally (failures retry with the same id within grace, without becoming clock bubbles). If grace expires without reconnecting, show the title status immediately, with no additional 1.5 seconds.
  Reconnect timing (`LinkDrop.retryDelay`, `Shared/Reconnect.swift`): during restart first at 150–400 ms, then every 0.5–1 second, no backoff for 30 seconds; ordinary disconnect first at 300–600 ms, then doubling from 1 second to 20 seconds (±20%).
  Resume: remember hello's `event_stream` and the largest received `event_seq` (`EventCursor`, retained across reconnects, cleared on hub change / re-pairing, not persisted); put them in the socket URL on reconnect.
  When hello says `resumed: true` (`ReconnectPlan`), do not refetch interactions, Inbox, to-dos, tasks, Lark or the Agents list, and do not show "Updating…"; just register the phone and resubscribe to open chats (`since_seq`) as usual,
  keeping the answer being generated until the hub's replayed `delta` continues it. Non-`resumed` reconnects do the full refresh as before.
- Slow network: only a timely pong clears "Connecting…" (late pongs still count as slow); ping every 5 seconds while slow so recovery is detected quickly,
  otherwise the title flashes every 20 seconds (observed). The socket still waits for 45 seconds with no messages before declaring it dead and reconnecting.
- Network recovery (path changes from unavailable to available) reconnects immediately without waiting for backoff (up to 20 seconds); switching networks (Wi-Fi ↔ cellular) pings immediately, exposing a dead connection on the old interface within seconds.
- During hub self-update (`hub_update` `restarting` / `confirming`, `docs/operations/hub-update.md`), disconnects are expected: "Connecting…" becomes "Hub updating…", and the opened explanation describes the restart.
- Placement (`ConnectionTitle` / `ConnectionLabel`): center of the navigation bar (`.principal`) in Agents (normally no title, title area empty), Inbox, and Settings › Machines;
  in the Persona tab, the name capsule below the avatar becomes the status (menu adds "Connection details"); the chat's expanded header keeps the session name but replaces the subtitle with status (like Telegram), while its collapsed small capsule is replaced entirely.
  Tap status for an explanation (`ConnectionDetails`: cause, last failure, seconds until retry, last connection, queued message count, "Retry now").
- Offline actions:
  - **Send messages**: when unreachable, make no request; the bubble has a clock and "Sends automatically when connected" (tap the clock for "Retry now / Cancel send"); requests failing offline / on timeout also become waiting,
    with no error alert. After reconnect and "Updating…" completion, send automatically in composition order (for unopened chats, first fetch one page of latest messages; delivered ones appear in the transcript and are removed automatically).
    Resends use the same `client_msg_id`, preventing duplicates. Waiting messages persist (`Outbox.swift`) and survive App termination;
    so do sent ones still waiting for their transcript copy (back as sent, not sent again), unless the chat's cache already has the copy (#1455).
  - **Op id**: messages carry `X-Persona-Op-Id` (the message id, equal to `client_msg_id`); each answer / approval tap gets one;
    the hub executes it once (`docs/protocol/protocol.md` ("Operation receipts")). A duplicate request returning `202 {"state":"pending"|"unknown"}` is not success: pending keeps retrying, unknown is treated as "May already have been delivered".
    Retrying a "May already have been delivered" message first checks `GET /api/v1/ops/{old id}`: `done` means sent, no resend; otherwise resend with a new id as before.
    A message the hub refused with nothing typed (`delivered: "none"`: a draft in the computer's input box, a dialog open…) goes again under a new id too, on Resend and on Clear and Send:
    the hub answers the refused id with that refusal without asking the computer again, so a Resend after clearing the box on the computer kept getting the old alert (#6425).
  - **Approve / answer**: if a request dies in transit (connection refused / reset, timeout, 502 / 503 / 504 not issued by the hub), keep the card submitted and retry with the same op id for at most 60 seconds (`OpRetry`), then restore the card and show a notice.
  - **Other one-shot actions**: never queue them (computer state may already have changed). Offline cards remain unchanged; failure restores the card,
    with a blue "Can't reach the hub / No network; that action did not take effect" notice (at most one per 4 seconds), not a red error.
  - Offline / unreachable-hub errors (`Error.isOffline`) are hidden in pages like timeouts (`Error.shown` is nil); background refresh and history-loading failures show nothing, as the title already explains it.
    The watch lacks this title and keeps showing errors as before.
- Debugging: `-showConnection waiting|connecting|updating` pins a state; add `-openConnectionDetails YES` to open the explanation;
  `-simulateOffline YES` makes NWPathMonitor treat the network as unavailable (the simulator always has the Mac's network). Mock slow / offline switches are in `docs/operations/testing.md`.

<a id="ssh-隧道直连不通时的后备891"></a>

## SSH tunnels (fallback when direct connections fail, #891)

When a device cannot reach the hub address, connect through an SSH server. For the mechanism, server setup and security, see [network.md, "Connect to the hub through an SSH tunnel"](../operations/network.md#经-ssh-隧道连-hub直连不通时的后备); this section covers the App side.

- **Code**: the tunnel itself is in the shared package `apple/Packages/PersonaSSH` (swift-nio-ssh + Network.framework, shared by iOS and Mac, not linked into the watch);
  App-side `Persona/App/SSHTunnels.swift` (one tunnel per hub, `SSHKeys`, routing `AppModel.sshRoute`),
  `Shared/HubTransport.swift` (maps hub addresses to tunnel loopback addresses, `SSHRoute` configuration), `Persona/Views/Settings/SSHTunnelView.swift`.
- **Only outgoing URLs change**: `APIClient.url`, chat-item URLs, `LiveConnection` sockets and port-forwarding address construction pass through `HubTransport.resolve`;
  `ServerConfig`, the pairing registry, `currentConfig` and all comparisons still use the hub's own address, so multi-hub grouping, routing and credential upgrades need no tunnel awareness.
  `HubRoute.healthProbe` uses `direct: true`, always probing the direct route.
- **Routing**: with usable SSH settings in a group (host, user, confirmed fingerprint), `routePicker` always has a value: before every connection, `HubRoute.answering` probes direct routes (even a single address),
  and only if none answer calls `SSHTunnels.open`; "Always use SSH" opens the tunnel first. While tunneled, `watchDirect` probes direct routes every 2 minutes and calls `reroute()` when they work again.
- **Configuration** lives in `PairedHub.ssh` (per-hub pairing registry in Keychain); private keys are separate in Keychain (`ssh.identity`, `AfterFirstUnlockThisDeviceOnly`).
  SSH settings entered on the pairing page are saved with the newly paired hub; the tunnel opened for pairing is handed directly to it (`SSHTunnels.adopt`), and device-credential upgrading continues through it.
- **Status** (consistent with existing wording): connection details add "Route: Direct / Via SSH tunnel (user@host)"; tunnel startup failures show "SSH tunnel: <reason>";
  Settings top says "Hub connected · Via SSH tunnel"; the active hub in Settings › hub has an "SSH" tag and footer "Connected (via SSH tunnel)", with the SSH tunnel row showing Not configured / Fallback / Always / In use.
- **Background**: `suspendLinks` closes every tunnel (`SSHTunnels.closeAll`); foregrounding reroutes and rebuilds each group (about 1 second measured in the simulator).
- **Debugging**: `-seedSSHTunnel <base64 SSHRoute JSON>` seeds SSH settings for the first hub; `-seedSSHKey <base64 32-byte seed>` uses a supplied test key (DEBUG only),
  `-tab settings -openSSHTunnel YES` opens the first hub's SSH tunnel page. End-to-end test setup is in `docs/operations/testing.md` ("SSH tunnels").

## File viewer (2026-10-31)

Files mentioned in chats, tool rows and the chat's Files page all open in `FileViewer` (`Views/Chat/FileViewer.swift`, `Persona/Code/`).

**Link only paths that can actually open.**
- **Previous problem**: every path recognized in messages (`FilePaths`) became a file block, read from the session's computer when tapped. Paths on another computer (such as the hub's `~/.persona/…`), or deleted files, only showed "Can't open this file".
- **Now**:
  1. When a message row appears, pass its paths to `FilePresence`;
  2. A moment later, batch one `GET /api/v1/machines/{mid}/stat` by "machine + session directory", resolving relative paths against the session cwd;
  3. Only file / dir / denied paths become links.
- **Cache**: by (machine, directory, path), existing paths for 30 minutes, missing paths for 2 minutes (the Agent may create them soon), unavailable checks retried after 1 minute.
- **If checks are unavailable**: before the answer arrives, with old nodes lacking the API, or offline, paths remain plain text; uncertain paths are not linked.
- **Counts**: at most 40 candidate paths in body text (`PathMarkdown` / `PathRefs`); file blocks recognize at most 12, displaying the first 6 existing ones.
- **Tool rows** (Read / Edit files) skip checks: the tool just used the file.

**Body paths are links** (#1706, same rules as web #1669, `apple/Packages/PersonaKit/Sources/PersonaKit/PathRefs.swift`, #2095):
- **Recognized**: `~/…`, absolute paths with at least two segments (`/tmp` does not count), relative paths with a directory and a letter-starting extension (`src/a.ts`; not `deepseek/v4.1`).
  Optional line suffixes: `:12`, `:12:5`, `:12-20` (start line), `#L12`; `stat` receives the path without line numbers.
  Outside backticks only ASCII is recognized, stopping at Chinese or full-width punctuation (`/tmp/a.md里`, meaning "in /tmp/a.md"); sentence-final `.` is excluded. A whole backticked span counts as one path (may include Chinese and spaces); if it does not exist, check paths within the span and select existing non-overlapping ones, longer first at the same position.
  URL fragments, `and/or` and version numbers do not count; code blocks, indented code, existing links and formulas in replies remain unchanged.
- **Rendering**: existing paths in replies (`LinkedMarkdown`) become Markdown links (`persona-path:<path>?line=N`, `PathLinkURL`), colored like other links (`.persona` link color); an entire backticked path's inline code is tappable.
  User bubbles (`PathText`) use underlined links in `Theme.userBubbleLink`: accent in light bubbles, bubble text color in colored bubbles (accent is invisible there).
  Before answers or when checks fail, plain text; conversion to a link changes only color, not font size or line layout.
- **Streaming**: the currently written chunk is not recognized (its final word might be an incomplete path); completed paragraphs (`StreamChunk`) are checked and linked normally.
- **Opening**: the chat's `openURL` recognizes `persona-path:` and opens `FileViewer` (`FileRef.line`). With a line number, show source directly (rendered Markdown, JSON trees and tables have no lines), read through that line (at most 8 extra pages), scroll it to the upper third of the screen and highlight it (iPhone `Theme.lineFocus` background, Mac selected row). Directories open as directories.
- **File blocks**: inline paths no longer repeat as capsules below the message; non-inline ones (only in code blocks, beyond the count limit) keep capsules; document cards and image / video / audio capsules remain.
- **Not done**: a long-press menu on individual paths (copy path / open in Files) and Mac hover "path · Line N": links inside SwiftUI `Text` have no individual long-press or hover handling; message long-press opens the whole-message action bar. Copy path is in the file viewer's ⋯ menu.

**Math and diagrams** (#1295, `apple/Packages/PersonaKit/Sources/PersonaKit/MathMarkdown.swift`, `Views/Chat/RenderedMarkdown.swift`): chats, plans, Markdown in Files, and Knowledge person pages render formulas (KaTeX 0.16.22) and Mermaid diagrams (12.3.0), both MIT and bundled under `Resources/Render/` (with their LICENSE files), without networking.
- **Recognized**: ```math (also latex / tex / katex) and ```mermaid code blocks; `$$…$$`, `\[…\]` over standalone lines or as a line's sole formula are block math; `$…$`, `\(…\)` are inline math. `$` immediately followed by whitespace is not an opening delimiter; preceded by whitespace or immediately followed by a digit is not closing (pandoc rules); Chinese or Japanese inside disqualifies it (except `\text{…}`); `\$` is a dollar sign, so "$5 to $10" remains text. Code blocks, inline code and indented code are untouched.
- **Rendering**: a hidden WKWebView loads bundled `render.html` (CSP allows only bundled scripts, styles and fonts; a rule list blocks all network requests), renders one item at a time and snapshots it; content is passed as arguments to `renderMath` / `renderMermaid`, never concatenated into the page. Images are cached in memory and on disk by (kind, source, light/dark, font size). Formulas render black as template images following text color, sharing one image across light/dark; diagrams render once for each mode (Mermaid default / dark themes). Inline math uses MarkdownUI inline images (`![…](persona-math:…)`, `MathInlineImages` attached at the root); block math and diagrams replace code blocks.
- **Failure**: block math and diagrams fall back to code blocks with KaTeX / Mermaid's message below; inline math shows monospace source. Tap diagrams for full-screen viewing (`ImageViewer`).

**Images viewed this turn and document cards** (#1543, `Views/Chat/TurnAttachments.swift`, `ViewedImages.swift`):
- **Viewed images**: images the Agent viewed during the turn (rules in `docs/protocol/protocol.md` ("Images viewed this turn"); `ViewedImages` follows the hub's `transcript.ViewedImages`) appear in their own row between work rows and the reply body (`ChatLine.viewed`). New streaming images only extend this row horizontally; reply text does not reflow and following stays intact.
  - No explanatory text, only thumbnails: multiple images use a horizontally scrollable 64pt-high strip extending to screen edges with a fade on sides containing more; one image uses an 80pt-high thumbnail. Screenshots and webpage screenshots have a small bottom-left marker.
  - Width follows aspect ratio (¾ to 1.6 times height): phone screenshots narrow, desktop screenshots wide; dimensions from lazy-loaded images reserve space before arrival.
  - Names and sources ("Screenshot · Studio", "Home.png · 480×300") appear only in long-press previews, Mac hover and the gallery.
  - Thumbnail sources: embedded images use `ChatImageLoader` (lazy loading, size-based downsampling, memory + disk cache); path-only images use M0 `file/thumb` (`FileThumbnails`).
  - Tapping opens this turn's gallery (`ImageViewer(items:)`): swipe left/right, save, share, copy image; paths additionally offer copy path and "Open in Files".
  - Folded work uses `fold.images`; expanded work uses actual tool calls, with identical results.
- **Document cards**: Word / Excel / PowerPoint / iWork / PDF files mentioned in replies (`FileThumbnails.isDocument`) become cards below reply text, not capsules: first-page thumbnail (M1 `FileThumbnailIcon`), filename and type ("Word document"). Tap for file preview, long-press to copy path. Other files keep capsules.

**Links inside previews**

The file viewer opens http(s) links within its own presentation layer: on iPhone the link card sits above the file preview; on Mac the link panel is inside the file preview window. Closing the link returns to the original file and preserves the reading position; the chat's underlying link drawer must not wait until the file closes to appear. Both chat file blocks and the Files page pass the original `SessionKey` to the preview, so AI editing / adding context inside links still belongs to the original session; standalone previews without a session can also browse websites. Development services on localhost / machine addresses are forwarded through the hub to the computer holding the file; other URL schemes keep the platform's existing policy.

**Display**

| File | Display |
| --- | --- |
| Code, config, logs | Language highlighting, line numbers, monospace without wrapping, horizontal scrolling. Line numbers stay fixed on the left; long-press a line to "Copy line / Copy all" |
| JSON | Source first; after the whole file is read (at most 24 MB) and parsed, switches to a collapsible tree view (key order unchanged), see below |
| Markdown | Rendered by default with the chat's renderer (`.persona` theme, `FittedTable`, `Links`); code blocks use language highlighting. The menu offers "View source" |
| Images, PDFs, directories | As before |
| Video | `video/*` or common video extensions use the native player; buffer the index and the segments needed to start playback, then play while reading, with pause and seeking. Seeking to an uncached position reads segments near the target keyframe directly; failures and unsupported codecs explain why. Closing the preview pauses the player, cancels reads and clears temporary caches |
| Audio | System-playable audio such as mp3, m4a, aac, wav, flac, aiff, caf (`audio/*` or extension, #1298), downloaded to this preview's temporary file before playback (`AudioFileView`): filename, play / pause, skip forward / back 15 seconds, draggable progress and remaining time, 0.75–2× speed; unsupported formats explain why. Closing the preview stops playback and clears the temporary file |
| HTML | .html / .htm default to an isolated preview (`HTMLFileView`, #1297): a separate WKWebView with no persistent website data, **scripts off, network off** (a rule list blocks every load except the page itself and data:). The page cannot navigate itself; tapped links use the App's link card; a line at the top states what is enabled. The menu offers "Allow scripts", "Allow network" (this preview only, reloads after changes) and "View source". Referenced files in the same directory (images, stylesheets and their url() / @import, fonts, scripts) are first read from that computer through the hub and embedded as data: (`HTMLResources`, at most 64 files, 8 MB each, 24 MB total), with networking still off; the top line says how many were read. Local dev-server site previews use port forwarding, not this viewer |
| Jupyter notebooks | .ipynb (nbformat 4, #1296) shows cells after reading the whole file (`NotebookFileView`): Markdown cells use the chat's Markdown style; code cells use the notebook's language and show "In [n]", followed by outputs: stdout / stderr (stderr orange), result text, images, errors (name, message and traceback with color codes stripped). HTML results (pandas tables) render in the same isolated WKWebView as HTML previews (scripts and network disabled, height follows content, `HTMLSnippetView`), with "Show text version" below and automatic text fallback on load failure. Outputs longer than 60 lines are initially folded. The menu offers "View source"; unreadable notebooks (nbformat 3, invalid JSON) show as text with an explanation |
| Tables | CSV, TSV, xlsx use a native table (`TableFileView`, #1294), see "Tables" below; the menu offers "View source" for CSV and "Open with Quick Look" for xlsx |
| Office, iWork, RTF | docx, doc, xlsx, xls, pptx, ppt, pages, numbers, key, rtf use system QuickLook (#1293): downloaded through `…/file/download` to this preview's temporary file, then embedded in the page (iOS `QLPreviewController`, Mac `QLPreviewView`), without another presentation layer. The menu can "Share" the file (open in another App, save to Files). Closing the preview clears the temporary file. `.key` sent as text (PEM private key) still displays as code; iWork documents saved as folders (packages) are still listed as directories |

**Thumbnails on the Files page** (#1293): for images, PDFs, videos and the documents above, when a row appears it requests a thumbnail from the computer holding the file (`GET …/file/thumb`, hub feature `file_thumb`, `docs/protocol/protocol.md` ("File previews")), sized to the row's tile × screen scale, at most three concurrently (`FileThumbnails`). Results are stored in memory and the cache directory by (hub group, machine, path, size, modification time): a changed file changes the key and is requested again; an unchanged one is not. If the machine cannot render it (`no_thumbnail`: Office on Linux, missing tools), or an old hub lacks the feature, the icon stays and no further request is made in this launch. Code and text get no thumbnails. A dark-page document thumbnail is not used; the icon is shown instead (in Mac dark mode, Quick Look gives RTF and text a dark background, making a black square in the list). Items with only a path (file cards in chats; `FileThumbnailIcon` passes size 0 and no time) are stored by path alongside the node's ETag; on the first appearance after each launch, one request uses `If-None-Match`: 304 reuses the old image, while a file regenerated at the same path gets a new one. The card's tile can be enlarged with `width` / `height`, and the image fits proportionally.

**Binary files return only metadata**: when the hub has `file_stream`, the Files page reads with `stream=1` and binary files return only size, type and revision; images and PDFs are then fetched in full through `…/file/download` (no longer capped at 2 MB), QuickLook still downloads to a temporary file, and videos read segments on demand (#706, see "Video" above). Old hubs still return the entire file as base64.

**Compare** (#1299, `apple/Packages/PersonaKit/Sources/PersonaKit/DocumentText.swift`, `Views/Session/CompareView.swift`): changed PDFs, docx, pptx, xlsx and images on the Git page no longer say "No text changes"; they compare the two versions, like the web (#1452):
- **Which versions**: unstaged changes compare the index (INDEX) with the working-tree file; staged changes compare HEAD with the index; files in a commit compare "hash^" with "hash". The old version is written to the node's cache by `GET …/git/blob` (hub feature `git_blob`), then read through `/file/download`; working-tree files are read directly. Added files have no old version; deleted files have no new version. Without this hub feature, the existing text diff remains.
- **Documents**: extract text and split it into units (PDF by page, Word by page break, slides one each including notes, spreadsheets one worksheet each), compare line by line (`CollectionDifference`), and put each change under its page / slide / worksheet with two lines of context on either side; unchanged text folds into "… N unchanged lines". If the text is identical, explain that formatting or images may have changed. At most 20,000 lines per version.
- **Images**: side by side, or the new image over the old one with a draggable divider.
- "Open old version / Open new version" at the bottom opens that version in the Files viewer (QuickLook, tables, PDF…).

**Tables** (#1294, `apple/Packages/PersonaKit/Sources/PersonaKit/TableParsing.swift`, `Views/Chat/TableFileView.swift`):
- **Read**: CSV / TSV parses RFC 4180 (quoted delimiters, newlines, `""`); the Files page keeps its paging and feeds the parser page by page, with a row split at a page end continued by the next page; the next page loads when scrolling within 60 rows of the end. `.csv` uses semicolons if the first row has more semicolons than commas (regions using commas as decimal separators). xlsx downloads fully through `…/file/download`, then reads zip (Compression for deflate) and OOXML itself: shared strings, inline strings, numbers (floating-point tails removed), TRUE / FALSE, error values, and date formats (built-in 14–22, 45–47 and custom formats containing y/m/d/h/s) displayed as `yyyy-MM-dd [HH:mm]`. No third-party library. Old .xls and Numbers still use QuickLook. At most 200,000 rows per worksheet.
- **View**: freeze the first row at the top (as headers) and row numbers on the left (Excel row numbers; the header is row 1); draw only visible columns. Estimate column width from the widest content in the first 300 rows (CJK counts as two cells), clamped to 56–240 pt; drag a header's right edge to resize. Numbers align right. Tap a cell to show its position (B12) and full content below (selectable), with copying; long-press offers "Copy cell / Copy row" (rows are tab-separated, preserving separate cells when pasted into a spreadsheet). Multiple worksheets get a switcher across the top.

Video does not use the first 2 MiB of binary data returned by the old API. `VideoFileModel` uses a two-byte Range request to `GET …/file/download` to discover file length and version, then `VideoFileResource` (`AVAssetResourceLoaderDelegate`) reads the ranges requested by the player. `VideoFileCache` aligns chunks to 256 KiB and writes read ranges into this preview's sparse temporary file; only recorded cached ranges are handed to the player, and logical file length does not mean downloading is complete. MP4 indexes at the end can be requested directly without downloading everything in between; seeking uses the index to find a nearby keyframe, never guesses a byte offset from a time ratio. The player controls prefetching, with a preferred forward buffer of 3 seconds; there is no separate task downloading the whole file from the start.

Authentication goes only in HTTP headers; playback URLs use a local custom scheme containing only a random identifier and filename, never the hub address, remote path or credentials. Later requests carry `If-Match`, and every chunk validates `Content-Range`, length and ETag; changed files, ignored ranges, incomplete chunks and old nodes produce errors, never silently fall back to whole-file transfer, and authenticated requests may not follow redirects. Closing the page cancels all resource requests and network tasks, releases the player, deletes the sparse cache, and checks task and preview generations to prevent late results from reviving it. Actual codec support depends on the system; H.264 / HEVC MP4 is recommended. Nothing is rewritten, transcoded or additionally generated as HLS. Both the hub and the node on the video's computer must support `file_download`.

**JSON tree view**
- Formatted display: keys green, strings, numbers and constants each have their own colors.
- Tap a container row to expand or collapse; the triangle on the left shows its state.
- By default, only the first few levels expand, stopping at about 400 total rows; a 6000-item array starts collapsed.
- Long-press a row to "Copy value / Copy path (`.forecasts[3].rain`)".
- The menu has "Collapse all" and, for smaller trees (up to 50,000 nodes), "Expand all".
- Invalid JSON (JSONL, a half-written file) displays as text with an explanation.

**Languages** (`SyntaxLanguage`)
- Swift, Go, Python, JS / TS (including JSX / TSX), Rust, Shell (bash / zsh);
- YAML, TOML, INI / .env, JSON, SQL;
- C / C++ / Objective-C, Kotlin, Java / Gradle / Groovy / Scala, C#, Ruby, PHP, Lua, Dart, Protobuf, GraphQL, Terraform / HCL, CMake;
- HTML / XML (plist, SVG, storyboard), CSS / SCSS / Less, Dockerfile, Makefile, Markdown source, diff.

Detection order:
1. Extension;
2. Filename (Dockerfile, Makefile, Podfile, .zshrc, .env…);
3. First line (`#!/usr/bin/env python3`, `<?xml`).

**The highlighter is a custom lexer (`SyntaxLexer`), with no new dependencies.**
- Why not tree-sitter: a C grammar library for every language; more than twenty makes a large package.
- Why not highlight.js: needs JavaScriptCore or a WebView, both slow and heavy.
- How it works: a generic loop over UTF-8 bytes; languages are just tables (keywords, types, constants, comment and string syntax). YAML, TOML, HTML, CSS, Dockerfile, Makefile, Markdown and diff each have a small specialized handler.
  - Keywords use byte-hash lookups, without building a String for each word.
  - Block comments, multiline strings, HTML comments and tags, Markdown code blocks and Dockerfile continuation lines carry state to the next line through `SyntaxState`.
- **Colors**: design system `Theme.Syntax` (GitHub colors). Code backgrounds are identical across interface styles and theme colors, so syntax colors do not change either; every color has ≥ 4.5:1 contrast against the code background in light and dark modes (`SyntaxPaletteTests`).
- **Pitfall**: in `emit(s, i, word(…, &i, …))`, Swift reads the argument `i` by position before calling `word`, leaving the macro's `!` outside the token; store `word`'s result in a variable first.

**Performance**
- **Fetching files**: the node reads text in pages of at most 2 MB, ending at a line boundary, and scrolling further reads the next page (`offset` / `next`). JSON is read in full because it must be parsed as a tree.
- **Splitting lines**: split bytes into lines in the background first (`CodeDocument`), then display plain text immediately.
- **Highlighting**: background processing from the top in chunks of 2000 lines; after each chunk, refresh the visible lines. At most 100,000 lines or 8 MB are highlighted; the rest stays plain text with "Only the first N lines are highlighted".
- **Very long lines**: display truncated after 4000 bytes (minified JS / JSON is often one line).
- **Code view** (`CodeLinesView`): creates labels only for visible lines and reuses them while scrolling, so tens of thousands of lines are as light as ten. Not the old "whole document in one UITextView" approach, whose layout cost grows linearly with file size.
- **Markdown**: split between top-level blocks into chunks of about 12 lines (`MarkdownChunks`, never inside code blocks or indented lines), placed in LazyVStack so only visible chunks are laid out. Highlighted code blocks are cached by content.
- **No text selection**: rendered Markdown does not enable text selection. Measurements showed that enabling it slowed opening by about 40% (607 ms versus 435 ms), and long frames when a chunk entered the screen grew from about 40 ms to about 60 ms. To copy, switch to source view or use "Copy content" in the menu.

**Measurements** (2026-10-31)
- **Environment**: iPhone 18 Pro simulator (iOS 27, Mac M1 Ultra), `-O` optimized Debug build, `MOCK_FILES=1` mock hub over local loopback.
- **Method**: `FileViewerPerfTests` with `-showFilePerf YES -showFileAutoScroll YES`, timed from opening the file; scrolling runs at a constant speed inside the App, down for 4 seconds then up for 2 seconds.
- **Why not XCUITest drags**: before each gesture XCUITest captures an accessibility snapshot on the App's main thread, itself causing 40–100 ms long frames.
- The "Scrolling" column below gives frames per second and the longest frame during those scrolling seconds; the simulator runs at 60 Hz.

| File | Size | First page received | On screen | Highlighting finished | Scrolling at 4000 pt/s |
| --- | --- | --- | --- | --- | --- |
| Go, 20,000 lines | 795 KB | 178 ms | 273 ms | 257 ms | 60 fps, longest 16 ms |
| JSON, 6000 entries (two pages) | 2.3 MB | 312 ms | Source 439 ms, tree 494 ms | 460 ms | Tree with 6000 items expanded: 60 fps, longest 22 ms |
| Log (first page 2 MB, 5 MB total) | 5 MB | 306 ms | 432 ms | No highlighting | 60 fps, longest 16 ms |
| Markdown, 4176 lines (with tables and code blocks) | 169 KB | 258 ms | 566 ms (rendered) | 365 ms (source) | 52–58 fps, longest 38–54 ms |

Two more points about Markdown:
- Scrolling at reading speed, 1500 pt/s, gives 52–58 fps with a longest frame of 38–50 ms; slow frames all happen when a new chunk enters the screen (MarkdownUI laying out tables and code blocks).
- At both speeds, the first second of scrolling has one frame of about 0.24 seconds.

Source view, code, JSON and logs all stay at 60 fps.

**Tests**
- **Unit tests**:
  - `SyntaxTests`: tokens in each language, multiline state, detection, wide-character offsets;
  - `SyntaxPaletteTests`: syntax-color contrast;
  - `FileViewerTests`: line splitting, continuing colors across pages, chunked highlighting and limits, JSON parsing, default folding, row text, Markdown chunks, batched path lookups and caching.
- **UI tests** `FileViewerUITests` (requires `MOCK_FILES=1`):
  - File blocks only for existing paths;
  - Go opens with line numbers;
  - JSON is a tree, with expandable arrays;
  - README is rendered.

## Error reports (#5621)

iPhone, iPad and Mac crashes go to the hub's `error_reports` (format and rules: [protocol.md](../protocol/protocol.md) "Error reports"). Everything is in `Shared/ErrorReports.swift` (the report builder, the disk queue and the sender); `AppModel.flushErrorReports` wires it to the connection. There is no UI.

- **Crash**: an uncaught `NSException` handler, installed first at launch (`AppDelegate` on iOS, `applicationWillFinishLaunching` on the Mac), writes the report synchronously (kind `crash`, level `fatal`, `previous_launch` true: it is sent at the next launch) and then hands the exception to the handler that was there before. On the Mac it rarely fires: AppKit catches an exception raised on the main thread (it logs it and the app goes on) unless `NSApplicationCrashOnExceptions` is set, so only exceptions on other threads reach the handler.
- **Content**: the exception's name (`message`) and reason (≤ 1 KB), and `callStackSymbols` as frames, innermost last, at most 64: the symbol (or the image's address and offset) as `function`, the image as `module`, `in_app` for the app's executable (`Persona.debug.dylib` too), PersonaKit and PersonaSSH. There is no `file` (not symbolicated on the device). `release` and `build` come from the bundle, then `os`, `os_version` and `device_model` (the hardware model identifier; the simulated one on a simulator); `source` is `ios` or `mac`. No device id, name or account.
- **Queue and sending**: one JSON file a report in Application Support `error-reports/` (`<time ms>-<id>.json`), at most 50, at most 7 days old. A file, not UserDefaults: it is written right before the app dies. It is sent only to the hub of the group the hub-level pages show (the one picked, else the first), after each hello and each `machines` message; when its hello's `features` has no `error_reports`, the queue is deleted. Batches of ≤ 20 reports and ≤ 240 KB through `APIClient`; 2xx deletes the batch (and `{"enabled": false}` deletes the rest); 4xx drops it, except 408 / 429; 5xx or no answer keeps it for the next time. A retry sends the same `id`.
- **Not yet** (#5621 phase 2): signal crashes, MetricKit crash and hang diagnostics, the Mac's main-thread exceptions (`NSApplicationCrashOnExceptions`, or reporting what AppKit catches), breadcrumbs, the watch (through the phone) and visionOS.

## Extensions and connectors of several managers (#1827)

[managers.md](../assistant/managers.md) ("Grouping"): with more than one installed manager, the hub's merged lists stamp each item with
its `manager`, and every manager lists its own built-ins, so the same id comes once per manager. With one manager, or a hub without a
roster, none of this draws and the pages are as before.

- **Roster**: Settings › Extensions (`ExtensionsModel`) and the Connectors section (`ConnectorsListModel`) read `GET /api/v1/managers`
  with their list, and again on `{"type":"managers"}` for Extensions. `ManagerRoster` (`Persona/Views/Settings/ManagerGroups.swift`)
  has `installed` (the default first, then the roster's order, installing ones left out), `isMulti`, `owner` / `named` (no stamp, or a
  manager the roster doesn't have installed, is the default's) and `groups(items)`. A roster that can't be read keeps the last one; a hub
  without one (404) groups nothing.
- **Grouping**: each manager's items are a section under `ManagerHeading` (dot, name, count, and "Offline · last synced …" while
  offline), empty groups left out; the list's footer goes under the last group. In Connectors the device, mail, Lark and Lark Bot rows
  stay in the Connectors section and the hub's other connectors follow, grouped. Rows are keyed by id and manager (`listKey`).
- **Routing**: navigation carries the manager (`SettingsPage.plugin(id, manager:)`, `.connector(id, manager:)`,
  `.pluginPage(…, manager:)`, `MessagesChatsRoute` / `MessagesThreadRoute`; nil for the default's). Each page's model keeps its
  manager and makes every request through `APIClient.manager(_:)`, which adds `X-Persona-Manager` (nothing for the default): an
  extension's details, switch, settings, secrets, auto-update, provided features, sign-in, its connectors' Show in Inbox; a connector's
  status, sign-in and sign-out, switches, Delete Saved Data / Knowledge, GitHub's sign-in source and Inbox options, and Messages.
  Extension pages get the manager too (`ExtensionPageView(manager:)`, `?manager=` on their actions). Another manager's extension or
  connector page adds that manager's name to its status line (`ManagerRoster.name`); the default's pages are as before.
- **Kept apart**: sign-in states (`ExtensionAuthStore`) and unsaved settings drafts (`ExtensionDrafts.Key`) are kept by id and manager.
  The market (installs, upgrades, uninstall) and `extension_auth` / `extension_operation` pushes are still the default manager's.
- **Tests**: `ManagerGroupsTests` (decoding, roster order and ownership, grouping, one manager, a hub without a roster, the header on
  every request of another manager's extension, sign-in, connector and Messages pages and none on the default's). UI test
  `ManagerGroupsUITests` against the mock hub's three managers (`MOCK_EXTENSIONS=1`, `TEST_RUNNER_PERSONA_MANAGERS_HUB`, shots with
  `TEST_RUNNER_PERSONA_SHOTS`; with `MOCK_MANAGERS=1` add `TEST_RUNNER_PERSONA_MANAGERS_ONE=1`). By hand: `-tab settings -openExtensions
  YES`, or `-openExtension <id> -openExtensionManager m-3fa21c` for 工作's copy.

## DEBUG launch arguments (Debug only; Release checks exclude these strings)

`-pairURL persona://pair?...` (pair without scanning a QR code, no notification permission prompt), `-openSession <machine>/<session>`,
`-showNewChat YES`, `-tab dev|mail|settings`, `-openAssistant <machine>/<session>`, `-newAssistant YES`, `-openAssistantSidebar YES|0.5` (Persona tab sidebar open / halfway open), `-openFirstImage YES` (open the viewer for the first image in the chat), `-showScrollState YES|visible` (chat-list position and following state, readable by UI tests; `visible` draws it), `-openTerminalPane YES`, `-showPad YES`, `-showKeyMods locked,once,held` (set the terminal key bar's ctrl / alt / shift to these states), `-terminalWrap NO`, `-terminalTheme ink|midnight|persona|solarized`, `-terminalInputMode direct|field` (terminal direct typing / input field), `-terminalTipsSeen YES` (skip "Terminal tips"), `-openTerminalTips YES` (show them again), `-focusTerminalInput YES` (shows the keyboard in direct-typing mode), `-openShortcutsPanel YES`, `-openTerminalSettings YES`, `-showLark YES` (go directly to Inbox), `-larkFocus <messageId>` (scroll to that message), `-larkProfile <userId>` (show this person's profile card in the chat), `-larkPicker YES` (show the reaction picker for the last message), `-larkMoments <postId>` (open a Moments post), `-openInboxFirst YES` (open the first unhandled Inbox item), `-larkTop YES` (show the Lark chat from its earliest message, for screenshots of long cards), `-addMail YES` (open Manage email accounts › Add email account), `-tab settings -openHubUpdate YES` (Settings › hub, `docs/operations/hub-update.md`), `-tab settings -openSkills YES` (Settings › Assistant › Skills), `-tab settings -openAssistantPage assistant|routines|routine|intervalTask|followup|bypassTask|newRoutine|memory|memoryFile|memoryEdit|places|place|newPlace|about` (pages under Settings › Assistant; `routine` opens the first scheduled task, `intervalTask` / `followup` the first interval / one-time task, `bypassTask` shows the "Allow everything?" confirmation in the editor, `newRoutine` shows the creation sheet with the "End-of-day review" template, `memoryFile` / `memoryEdit` opens the first memory / its editor; `places` is Saved Places, `place` opens the first place's editor, `newPlace` the new-place sheet). `scripts/app/sim.sh` wraps building, installation, pairing and launch (`SIM=<udid>` changes the simulator; concurrent agents must use separate simulators or overwrite each other's installs).
Check history rendering: `-loadAll YES` (load all history and scroll to the top), `-showFromSeq <seq>` (display from the turn containing this user message; scroll positioning can drift in long histories),
`-dropUsers YES` (hide that turn's user bubbles to fit its process rows and answer on one screen). `-openPane terminal|git|files` opens the chat page's terminal (from the bottom) / Git / Files;
`-simulateTap <machine>/<session>` runs the notification-tap handler 4 seconds after launch; `-tab settings -openNotificationSettings YES` (Settings › Notifications; add `-openIslandSettings YES` for Dynamic Island); `-tab settings -openSentAsYou YES` (Settings › Sent as You, #5278); `-showFleetSample needsYou|working|done` (`-fleetDetails YES`, `-fleetIsland b|c|custom:<left>,<right>`) starts a Live Activity with fake data; `-showTestNotification permission|plan` shows a local notification matching an interaction push 3 seconds after backgrounding (UI tests use it to test buttons); `-openFilePath <path>` (open this file after opening the chat; `-openFileLine <line>` jumps to that line), `-showFilePerf YES` (write file-open timings and per-second frame rates to Caches/file-perf.log; `-showFileAutoScroll YES` scrolls the file at constant speed, `-showFileAutoScrollSpeed 1500` changes the speed); `-showMenu YES` opens the long-press action bar on the last reply (with `-showFromSeq`, that turn's reply); `-openWelcome YES` forgets pairing and exits demo mode at launch, stopping on the pairing page (`DemoModeTests`); `-openChangeAnswer YES` opens Change Answer on the first **Answered by the assistant** card (#544).

`-showVoiceOrbs YES`: without connecting to the hub or opening the microphone, show a sheet listing every phase of the voice orb (fake fluctuating volume) and two large orbs; combine with `-ui.style`, `-ui.scheme` for screenshots (#3506).

Tests that live updates do not interrupt interaction (`LiveUpdateTests`) use two arguments: `-openChatsFresh YES` clears the chat cache on disk at launch (steps pushed during the previous run do not remain);
`-perfCounts YES` counts how often each view body is computed and how long each hub push takes until the main thread is idle, exposing JSON in hidden text `perf.counts` for UI tests
(`PerfSignpost.swift`; the same instrumentation emits os_signpost, so Instruments Points of Interest shows every body and push).

## Lark message rendering (`Persona/Views/Inbox/Lark/`)

Reuses the Lark gateway web app's message rendering (the upstream message renderer), with `parts` forwarded unchanged by the hub:

Sending does not wait for Lark (`LarkOutbox.swift`): tapping Send clears the input and draws the message at the bottom of the chat immediately (a small spinner beside the bubble, like Lark), while the background sends messages one by one in composition order;
after sending, it waits up to 20 seconds for Lark's own copy (same text, sent by me, no earlier than the send time) and replaces it. Ordering applies only to send requests: waiting for copies happens independently alongside them, so the next message does not wait for the previous copy (#851, both chat pages and topic panels). Unsent messages stay in place with a red failure icon beside the bubble (`Theme.Icon.errorFilled`); tapping it can retry, return the text to the input, or delete it. Sending and failed rows share the same list as Lark messages, ordered by composition time (`LarkChatRows`): later sent messages and others' later replies appear below them; retrying does not move a row.
Read receipts (`LarkReadReceipts.swift`): beside my messages, direct chats say "Read / Unread"; groups show a progress ring (a checkmark once everyone has read). Without receipts (not fetched yet, absent from the Lark gateway, or an old hub without the route), nothing is drawn.
Only my visible messages are queried (`POST …/conversations/{id}/read-states`, at most 100 per request), at least 3 seconds apart per conversation; fully read messages are not queried again. When this conversation gets live updates (or the once-a-minute foreground fallback poll), visible messages not fully read are rechecked; after an old hub returns 404, no more queries during this visit.
The same applies to sending the AI twin's draft from the Inbox: tapping Send closes it and marks it handled; failure puts it back among unhandled items, and reopening the reply page restores the same text (`InboxStore.unsentReplies`).

| File | Lark gateway counterpart | Content |
| --- | --- | --- |
| `LarkModels.swift` | contracts `MessageView` / `MessagePart` | Wire types, API |
| `LarkMedia.swift` | `LarkRemoteImage` / `image-viewer` / `message-video` / `chat-file-preview` | Authenticated media loading (memory + disk cache, ImageIO downsampling), emoji directory, full-screen gallery, player, Quick Look |
| `LarkInline.swift` | `card-style.ts` / `card-markdown-text.ts` | Lark palette, inline runs (bold / italic / strikethrough / inline code / links / dashed terminology underline / embedded emoji images), flow layout |
| `LarkMessageContent.swift` | `index.tsx` `renderPart` / `rich-blocks.ts` | Rich-text block tree (paragraphs, ordered / unordered lists, quotes, headings), images, stickers, file cards, video, voice, code blocks (lightweight highlighting), system messages |
| `LarkCard.swift` | `card-renderer.tsx` | Card elements: header theme colors, markdown, column_set (weighted / fixed / auto column widths + bisect / trisect / stretch narrow-screen reflow), table, list, collapsible_panel, note, text_tag, icon, buttons (links / callbacks), select / input / picker (read-only), overflow, img, chart (Swift Charts)… |
| `LarkSummaryCards.swift` | `SummaryCardView` | Forwarded-message bundles (tap for all messages), group / person cards, video meetings, calendar, polls and other summary cards |
| `LarkChatView.swift` | messenger bubbles | Avatar, sender + bot badge, consecutive messages from one sender (by id) grouped, time only in centered separators (day change or gap over 5 minutes, `LarkChatLayout`), quoted replies, reactions, edited marker; cards / images / files have no bubble. Topic groups (`is_topic`) use one card per topic like the Lark gateway web app (`LarkTopicCard.swift`: avatar, name and time on each card, not chat-grouped; content taller than 320 pt folds into "Expand"; below are the latest two replies and "View N earlier replies"; replies load only when the card is visible, at most 3 concurrently, and not again if the reply count has not changed (`LarkTopicReplies`); "React" and "Reply" below; tapping the body or replies opens "Topic details"; recalled and edited topics and recalled replies follow the same "Message Tweaks" recall-protection and edit-history display as chat messages, #1087) |

Key points:
- Inline content becomes one `Text` (`Text("\(a)\(b)")` interpolation); emoji images embed inline through `Text(Image(uiImage:))`, initially showing `[label]` before loading.
  Changes to `LarkMedia.emojiVersion` trigger redraw after loading.
- Card `plain_text` in Markdown contexts must decode entities such as `&nbsp;` and `<br>` / `<a href>` / `<font color>` (markup left by the Lark gateway).
- Card callback buttons are enabled only when `actionMethod == 2` (through the Lark gateway's card-actions API); select / input and other form controls show only their current values.
- Custom `Layout` can receive `.infinity` for `proposal.width` (ScrollView / ideal-size measurement); check `isFinite` before converting to an integer or it crashes.

## Engine selection

Each machine uses one terminal engine: herdr (herdr in the terminal), Herdr App (terminals opened in the desktop App), or Orca; the default is herdr. Only sessions in the selected engine are shown. Choices come from the `engines` reported by the hub (names and descriptions unchanged), never hard-coded. User-facing instructions are in `docs/integrations/engines.md`.

After successful QR-code or manual pairing, first choose an engine for the hub machine; if there is no local machine, use the first in the list. One must be selected to continue; there is no Skip: preselect this machine's current engine (herdr if never selected), and confirm with `PUT /api/v1/machines/{id}/engine`; a save failure keeps this page open and asks to retry (requested 2026-11-01). If the hub does not report engine detection data (old hubs), skip this step to avoid a page that cannot save or exit. Settings › Machines › Machine details › Engine can change it at any time; saved changes while offline take effect on connection. Missing detection data shows "No detection data yet", never treats unknown as not installed.

Both mock hub and demo mode support selection and machine pushes. For screenshots, first open the pairing page with `-openWelcome YES`, then open a `persona://pair` link to the mock hub to enter the real post-pairing flow; settings details keep using `-tab settings -openMachines YES -openMachineDetail server`. No new DEBUG arguments.

The To-dos page (iOS / Mac) and the assistant's tasks share `TodoItem` / `TaskState` in `apple/Shared/Tasks.swift`. The To-dos page uses the state labels, sorting and colors specified in `docs/assistant/todos.md`.


### Orca workspace containers in Agents (#1399)

By Repository uses the manager-owned `/api/v1/workspaces` catalog for Orca containers, including existing workspaces with no Agent. The host's project ID groups independent root workspaces under one project heading; workspace titles are not project names. Workspaces keep their host titles, order and source-local parent IDs; a parent can be in another Orca project on the same computer, in which case the child stays under that parent's tree. Every level has its own child fold, and status counts and Mac keyboard traversal include descendants only while their branches are open. Quiet workspace trees retain their structure. Search and quick filters keep the ancestor path of visible Agents. Other grouping modes retain their existing behavior.

Repository browsing follows Orca's workspace-card geometry: one project heading, a card with its title, an optional Primary badge, then project/branch identity, compact Agent rows with short activity ages (`now`, `5m`, `3h`, `2d`) and a child-count chip. Cross-project children show their repository badge only when their repository differs from the direct parent; descendants in that same repository omit the repeated badge, even under a different outer project heading. Stable project IDs take precedence over path/name fallbacks for older catalogs. Detached checkouts keep the technical label `Detached HEAD` in every interface language. Nested cards advance by twenty points, and selection shades the selected workspace and its ancestors. Detached Git checkouts show the host's short HEAD identity; the node bulk-reads it only when needed, with a bounded optional request and no terminal inspection. Host `unread`, `sidebar_status` and `workspace_kind` values drive workspace presentation separately from Agent state; older nodes fall back to the sessions they report. Reply previews and repeated location metadata stay out of the list. Multiple Agents fold behind a compact icon/count strip. Keyboard traversal follows those exact Agent and child folds, including idle sessions. Conversation navigation, context menus and session actions remain available.

The catalog is read every ten seconds while this list is visible, independently of the Workflow board toggle. Offline discovery uses the manager's existing snapshots. Transient failures retain the last successful catalog; older hubs fall back to the session-derived tree. The list creates no synthetic Agent and starts or stops no process. iOS and Mac share this model and view; this does not update an already installed TestFlight binary.

Clicking a worktree's title or branch opens its first direct Agent in the sidebar's displayed order, including when its Agent strip is folded. Clicking a specific Agent opens that conversation. Selection replaces the previous conversation; it does not open a separate worktree details page. An Agent-free worktree stays selected with the normal empty pane and keeps New Session and New Child Worktree in its context menu. The separate chevrons only fold Agents or children. Explicit workspace IDs take precedence over directories. Provider history without an Orca binding is associated by exact directory only when the catalog has one workspace at that path on that machine and hub; ambiguous history stays unassigned. Read-only history retains its original capabilities.

Right-click a worktree title, branch or direct Agent row on Mac, or long-press it on iPhone, for the same worktree commands: Open Worktree, New Session, New Child Worktree, Copy Path, Copy Worktree Name and, when present, Open Parent Worktree. It uses that exact workspace as the initial code source and parent, including a nested or Agent-free container. The form can change either choice independently; a folder needs a Git code source. Successful creation refreshes the sidebar catalog and opens the existing New Session form for the new child using the server-acknowledged parent. All regions use the owning worktree's identity. An Agent row also retains its session commands. A title with one Agent offers those commands directly; with multiple Agents it offers named submenus so session actions have an explicit target. Each descendant keeps its own menu scope. The UI names these containers worktrees; protocol and accessibility identifiers retain their workspace names for compatibility.

New Session also offers a searchable workspace picker, with full ancestor paths and an explicit Choose by Folder fallback. A chosen workspace sends its source ID and directory to the existing session-start route. New Child Worktree separates Code Source (the repository and base commit) from Parent Worktree (any Orca workspace on the same computer, including empty folders and other projects). On a phone, the selected parent's name has its own line above the ancestor path so a deep breadcrumb cannot hide the selected destination. The node advertises `choose_parent` before the client enables independent parent selection. With `automatic_name`, Create Worktree is ready without entering a branch or path on every supported engine. The native form keeps those overrides in Advanced, collapsed initially. An unnamed create sends `auto_name: true`. Orca receives a generated creature seed without a branch override and owns its prefix, retired-name/collision policy and first-work rename. Other supported hosts use a unique node-generated branch and directory through their existing creation route; subsequent naming follows the host’s capabilities. Explicit branch/path inputs retain their existing validation; older nodes keep the manual form. A created worktree appears through discovery even if the New Session form is cancelled before an Agent starts. Stale explicit launch targets and missing parents are refused rather than silently redirected.
