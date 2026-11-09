# Lock Screen / Dynamic Island Live Activity (Fleet)

The whole phone has a single Live Activity, "Fleet": it rolls up every Agent that needs you, is working, or finished unseen onto the Lock Screen and the Dynamic Island. The manager that talks to computers computes the content (`server/internal/assistant/fleet_live.go`, delivered to the hub over the bus as `fleet.live`); the hub fills in each device's fields and pushes over APNs at a set pace.
The App starts the activity and reports tokens; the Widget extension (source `apple/PersonaWidgets`, module still `PersonaWidgets`) draws it. The design rationale is in the lane notes (an analysis of the ChatGPT Codex / Sand activities);
this page covers only implementation and conventions.

## What it shows

| Phase `phase` | Condition | Color | Symbol |
| --- | --- | --- | --- |
| `needsYou` | A pending interaction (question / plan / permission / terminal), or herdr blocked | Red | Bubble + count |
| `working` | Nothing needs you, something is working | Amber | Gear + count |
| `done` | Only finished-unseen left | Green | Dot + count |

Apple Watch: no separate implementation; `supplementalActivityFamilies([.small])` provides a single-line Smart Stack version (symbol for the most urgent category + "2 need you" + count of the next category).

### Dynamic Island

The compact view used to be "a star on the left, a timer on the right", which said nothing: a timer only makes sense for a single Agent, while Fleet summarizes a group. Now both sides are numbers you can read at a glance:

```
Preset A (default)  ( ⚙3    ▬▬▬▬   ●1 )     left: the most urgent category, right: the next one; when something needs you ( 💬2  ▬▬▬▬  ⚙3 )
Preset B            ( ⚙3    ▬▬▬▬   ⚡12k )   right: tokens output per minute across all Agents (TPM)
Preset C            ( ⚙3    ▬▬▬▬   📊42M )   right: tokens used today
Custom              pick each side: Status Counts / Tokens per Minute / Tokens Today / Requests Today / Persona assistant / Machine Load / Longest Turn Timer / None
```

- "Needs You" always takes the left side (overriding the custom choice), in red; when the right side is set to Status Counts it shows the next category after the left one. The rules are in `IslandCell.sides` (`apple/Shared/FleetState.swift`), pinned by unit tests.
- When sharing with other apps' activities (minimal view, width-limited compact view): only the most urgent category is shown; see the next section.
- Expanded view: top left has the three category counts, top right the chosen number (e.g. "12k/min"), and below that the Agent list and buttons as before. The Lock Screen header is the same: counts + the chosen number; with two numbers chosen, the counts keep only symbol and digits so they don't squeeze into "3 wor…".
- The gear doesn't spin: `symbolEffect(.rotate, options: .repeating)` doesn't animate in a Live Activity (two shots 0.4 s apart differ by ≤1 pixel in the gear area, same as static areas), so the gear is static; the same goes for pulses and other repeating animations.
- No cost display (user decision, 2026-10-29): "equivalent API cost" would mislead subscription users, so only token metrics are offered.
- Settings: **Settings › Notifications › Live Activities › Dynamic Island** (`IslandSettingsView`), topped by three previews: Normally / When an Agent Needs You / Shared with Other Apps; stored on the hub per device (`prefs.island`, `notify.go IslandPrefs`).
  When Machine Load is chosen you can pick which machine; if none is picked it's the busiest one.

### Sharing the Dynamic Island with other apps

The user's screenshot: another app also had a Live Activity (a separate circle on the right, "8分", i.e. "8 min"), and Persona was left with only its left part, "⚙4📊⁚", the rest cut off.

**Rules** (iOS 27; Apple's "Displaying live data with Live Activities" and the HIG Live Activities update of 2026-10, measured in the simulator)

- Only one activity: compact view, one block on each side of the camera. The system gives no width; the island grows as wide as the content: iPhone 17 / 17 Pro allows about 94pt per side at most;
  iPhone 18 Pro / Pro Max has a narrower camera (the island itself is about 95pt, 125pt on the 17), so even 100pt per side fits; anything wider gets cut.
- Several apps have activities: the system picks two (three on some models) for the island, and the system decides which one hugs the island and which gets its own circle; apps have no say.
  - iPhone 18 Pro / Pro Max: both use the **compact view, but width-limited**: 28×28pt per side, with left and right blocks side by side in one place (the one hugging the island to the left of the camera, the other in its own capsule).
    The user's screenshot was exactly this: the "⚙4" and "📊42M" blocks squeezed into 56pt, the second one cut off.
  - Other models with a Dynamic Island (17, 17 Pro, 16 Pro…): both use the **minimal view**, one hugging the island (left), one in its own circle (right). 36.67–45pt wide,
    content shows at most 35pt, the rest is cut; again the system gives no width.
  - Landscape, iPhone Duo (island vertical, two blocks stacked): even with only its own activity it's the width-limited compact view, 28pt per side.
- Several activities from the same app: only the one with the highest `relevanceScore` goes on the island (Fleet is the only one, so this doesn't apply).

**What the App can know**

- Can: iOS 27's `isDynamicIslandLimitedInWidth` (a WidgetKit environment value). When drawing a block, the extension knows it's width-limited (true in all three width-limited cases above, measured).
  Apple's advice: when width-limited, put a single minimal-style graphic in leading and an empty view in trailing; the system hides the empty block.
- Can't: which presentation is current (compact / minimal / expanded are rendered separately by the system, and a view only knows which block it is), whether another app has an activity, whether it hugs the island or has its own circle.
  The App process knows even less; `relevanceScore` only ranks your own activities.
- So every presentation has to make sense on its own, without relying on the other half.

**Approach** (`IslandGlyph`, `IslandSide`, `apple/LiveActivity/FleetIslandViews.swift`, `apple/PersonaWidgets/FleetLiveActivity.swift`)

- The minimal view and the width-limited leading block are the same graphic: the most urgent category (needs you > working > finished unseen), shown in full, unaffected by the style setting; when width-limited, trailing is empty and the system hides it.
  The non-limited compact view shows both sides according to settings, as before.

```
            1 digit       2 digits            3+ digits
Minimal     ⚙3            ⚙12                 99+ (status color)      content ≤ 35pt
Limited 28  ⚙3            (12) status ring    99+ (status color)      inside a 28pt circle
```

- The look is chosen by count, not with `ViewThatFits`: when not width-limited the system gives no width (the view gets "as wide as you want"), so `ViewThatFits` always picks the first.
- All sizes are in `IslandMetrics` (`apple/Shared/Design/IslandMetrics.swift`, also compiled into the extension): limited 28, usable graphic 24 (the width of one line of text inside the circle),
  minimal 45 / content 35, compact side at most about 94, plus the symbol and digit font sizes.
- The settings page gained a Shared with Other Apps preview (`IslandSharedPreview`): the graphic hugging the camera is Persona's, and the gray circle on the right stands for another app.

**Measured** (iOS 27 simulator, each model shot alone / shared / landscape)

| Model | Alone (compact, per side) | Shared with another app | Landscape |
| --- | --- | --- | --- |
| iPhone 18 Pro / Pro Max | Grows with content, even 100pt fits | Compact, width-limited, 28×28 per side | Limited 28 |
| iPhone 17 / 17 Pro | Grows with content, up to about 94pt | Minimal, content up to 35pt | Limited 28 |
| iPhone Duo (iOS 27.1) | — | Limited 28, two blocks stacked | — |

**Verification**: `IslandFitTests` draws the minimal, width-limited and compact-side views with 1 / 2 / 3 / 4-digit counts at their respective sizes and checks that every drawn pixel stays within bounds
(text isn't shrunk or truncated, so anything that doesn't fit overflows and gets caught). In the simulator, reproduce with another app's activity: `scripts/app/island_peer.sh <simulator>` installs a sample app that has only a Live Activity
(`apple/IslandPeer`, "8分"); with `PERSONA_ISLAND_PEER=dev.example.islandpeer`, `LiveActivityShots` takes two extra shots, "shared" and "landscape",
and `PERSONA_FLEET_COUNT=12` makes the fabricated count two digits.

- Not counted: seen (idle), offline and muted Agents, and ones waiting for the next `/loop` wake-up; the assistant (chat tab) only counts when it's asking you. The rules match the Agents list (`hub/liveactivity.go buildFleet`).
- Item order: needs you (earliest request first) → working (longest-running first) → finished unseen (newest first); the push carries at most 3 items, the UI draws 2, and the rest show as "+N".
- Timers run locally on the phone (`Text(timerInterval:)`), starting from the turn start time the hub recorded (`turnTracker`); nothing is pushed for timers.
  For an Agent whose turn is over while its work runs in the background (`background`, #2935) the timer starts when its workflow did (`since`, see "ContentState" below).
- **Background work** (#3437): such a row says 「后台」 (Background) where the place goes and, on the line under its name, what runs (`detail`: "parity-audit · Verify · 169/185"),
  with a static bar (`FleetBar`: `progress`, done of started so far) at the end of that line, under the timer, on the Lock Screen (with details) and in the expanded view alike.
  The bar is drawn, not a linear `ProgressView`: the system track was about 1.2:1 on the black of the island and the Lock Screen, so 3/8 looked like a short orange line with nothing after it. Its track is `FleetPalette.track` (white, or the text color on the Lock Screen, at 0.4: 3.7:1 on black, and the working fill 3.2:1 on it).
  The row is two lines, like a request's, so neither view gets taller than with requests: two such rows are the tallest case (`-showFleetSample background`).
  On the expanded view two two-line rows plus the "+N" under them pass the height limit at the usual 8 pt gap (the "+N" was cut in half), so then the rows are packed (`FleetState.islandPacked`: 4 pt between them, no top inset) and the "+N" stays where it always is, as on the Lock Screen.
  The bar never animates (see "Pitfalls"), and `.numericText` transitions are not used: they could not be checked in the simulator.
- By default the Lock Screen shows only counts and a nameless line like "A permission request is waiting"; Agent names and content appear only after turning on **Settings › Notifications › Show Details on Lock Screen** (stored per device, `prefs.lock_details`;
  the hub fills `details` when pushing to each device). The Dynamic Island is only visible when unlocked, so it always shows details.
- Visuals: Persona's status colors (they don't follow the interface style, which the extension can't read); the outline and the Allow / Approve buttons are system blue, matching the App's accent color. The Lock Screen and expanded view are both limited to about 160pt tall, hence at most two items.
  On the left is Persona's avatar (see "Avatar" below); before 2026-11-02 it was the coral four-pointed star of the App icon.

### Avatar

The top left of the Lock Screen banner, the left side of the compact Dynamic Island and the top left corner of the expanded view show the avatar chosen in **Settings › Appearance › Avatar**, changing with state:

```
Compact            ( 🙂2    ▬▬▬▬   ⚙3 )     left: avatar + count of the most urgent category (digits only; the avatar already says which); right: as before
Expanded           🙂             💬2 ⚙3 ●1    top left: avatar; top right: three category counts, with the chosen number on the line below
Minimal / limited  ⚙3                   when sharing with other apps there's only 28–35pt, and a face would push the count out: with counts it's the most urgent category as before (previous section);
                                        with nothing to count (only the assistant replying) it shows the avatar rather than leaving an empty island (`IslandMinimal`)
```

| Fleet | Avatar state | Look |
| --- | --- | --- |
| Something needs you | Waiting for you | needs-you 状态环，颜色取自共享的 StatusPalette |
| Working | Working | 团子：眼睛侧看，橙色弧线停在圆周；奶蛙：使用形象包的工作中静帧 |
| Only finished-unseen, or nothing | Idle | Eyes open |
| No Agent busy, only the assistant replying (`assistant`) | Thinking | Eyes up, thought bubble |
| Content stale (no push from the hub for 15 minutes, `isStale`) | Offline | Gray, eyes closed |

- "Error": Fleet has no separate error state; the closest is content going stale because the hub disconnected, so stale uses offline (gray).
- "Thinking": Fleet can't tell whether an Agent is thinking or writing, so it's only used while the assistant replies; `assistant` is only pushed to devices whose Dynamic Island shows the assistant, others see idle.
- **No motion, only a crossfade on state change**: TimelineView and repeating symbol animations don't run in Live Activities (see "Pitfalls"), so what's drawn is each state's still frame (the same as in the App under Reduce Motion, `PersonaFace(moving: false)`), with `.transition(.opacity)` on state change. No bitmap frame sequences, no pushes for animation.
- **共享绘制**：`apple/Shared/Design/PersonaFace.swift` 只绘制团子（`mochi`）。App 的 `AssistantAvatar` 用 TimelineView 驱动动画，扩展直接绘制静帧；奶蛙（`nwverse`）使用 App Group 中的形象包图片。
- **旧内容回落**：`FleetLook.supported` 在读取旧 hub 推送和缓存活动内容时归一已删除的 design；其他形象包回落团子，不能从旧图片缓存重新出现。`FleetFace` 只使用当前奶蛙包对应的图片，没有图片时绘制团子。
- **Which one, and what color**: the extension can't read the App's settings. `FleetLook` is the avatar's drawing + the accent color's light / dark values, plus `<id>/<rev>` for avatar packs.
  It lives in the Live Activity content (`FleetState.look`, after build 99):
  - the App includes it when it updates the activity itself;
  - the App tells the hub (`look` in device prefs, a separate PUT), and the hub puts it as-is into this device's pushes;
  - on avatar change, the App immediately updates the activity once with content carrying the new avatar; changed content always makes the system redraw.

  The extension draws the avatar from the content first; only when the content has none (old hub) does it read what the App stored. The App stores the avatar and the avatar pack's images in the App Group container shared with the extension (`FleetLookStore`, files under `Fleet/`,
  group name `group.<bundle id>` by default, see `docs/operations/release.md` ("App Group")); avatar pack images can only go this way, since they don't fit in the content (4 KB for the whole activity's data).
  No keychain: the Live Activity extension can't read or write the keychain on a phone (measured on build 103, see "Why the avatar pack was still wrong in build 103" below).
  After writing, it updates the activity once right away instead of waiting for the next push: the content is unchanged, so the stale date is pushed 1 second later, so the system doesn't treat an identical update as no change (measured in the simulator: switches within 3 seconds, no need to restart the activity).
  Every time the App returns to the foreground it compares again and writes whatever didn't get written before.
  未写入或读取失败时，优先按活动内容里的形象绘制；奶蛙包的图片不可读时回落团子，保持团子的 App 图标配色。缺少 App Group 签名也走这个兜底。
- **Avatar packs (the hub's `~/.persona/personas`, bitmaps)**: after the App downloads an avatar pack from the hub, it takes one image per state: the first frame of the dot animation (how the top bar dot looks frozen under Reduce Motion; 144px in the pack),
  and renders one @3x image per face size used by the activity: compact 22pt (66px), minimal / limited 24pt (72px), expanded and Lock Screen 26pt (78px), scaled straight from the pack's original with high-quality interpolation, lossless PNG.
  Pixels are no longer cut to save bytes (#349: the 4 KB cap dated from the keychain era, when 6 images were crammed into one item; on build 106 images of 36–42px drawn at 26pt were "still a bit blurry"). The portrait is only stored when a state has no frame, scaled from the portrait file that is at least 78px.
  One file per image: `Fleet/faces/<id>_<rev>_3x/<state>_<pt>.png`, `avatar_<pt>.png` (the `_3x` in the directory name means the small 4 KB images stored by older versions get redone). Only the current pack is kept; on a pack change the whole directory is swapped (written alongside, then moved in, so the extension never reads half of two packs).
  The extension reads only the one image for the current presentation's size (`FleetLookStore.face`), decodes it at that size and 3x (`FleetFaces.image`), and crops it to a circle. For why, see "Why build 98 was still wrong" below.
  When the pack has no frame for some state, it uses the portrait with a hint in the style of the built-in avatars: a red ring for needs you, gray for offline. The images exist only on the phone (in the pack the App downloaded and in the App Group container), never in the repository or the installer package; tests use solid-color placeholders.
  图片不可读（尚未写入、写入失败）时绘制团子，不再用面罩换成包的配色；也不读取其他包的图片。目录名仍是形象包的 `<id>/<rev>`。
- **Put into ContentState** (it wasn't before build 98: we thought the keychain was enough, but hub pushes replace the whole content-state, and the hub didn't know which avatar the phone had chosen):
  hence the App tells the hub about the avatar. Content without `look` (old hub) still decodes, and the extension falls back to the `look.json` the App stored; a `look` it can't understand (a future format) drops only that field.
- The preview in **Settings › Notifications › Dynamic Island** (`IslandPreview`) also shows the avatar on the left, reading the same data the extension reads, consistent with the compact view.
- Build 92 drew the default avatar whatever Settings chose: its extensions were signed without keychain groups. `scripts/release/entitlements.py` now signs and checks each bundle (`docs/operations/release.md` ("entitlements")); the investigation is in `docs/archive/apple/live-activity-records.md`.

#### Why build 98 was still wrong

Build 98's signing was correct: the App and widget extension had the right keychain groups, the group names in Info.plist were right, and `entitlements.py check` passed. But the user's screenshot still showed the default avatar + system blue,
even after switching to the built-in Circuit. The same code in the simulator, with the real avatar pack from the hub (copied locally, not checked in), drew the pack's image on the island, so it only happened on the phone.

Already ruled out:
- Signing and group names: the ipa check above.
- Data protection: stored with `AfterFirstUnlock`, so readable on the Lock Screen after the first unlock since boot.

Two explanations remained, impossible to distinguish without a real device:
- **The extension can't read the keychain in a Live Activity**. Apple only says a Live Activity "runs in its own sandbox", can't use the network or location, and says nothing about the keychain. If so, no avatar on the island would work, including built-ins.
- **No redraw happened**. In 98, switching avatars updated the activity with identical content, which the system might treat as unchanged; the hub also only pushes when content changes (at most once per 30 seconds since #248).
  That would leave the island showing what was drawn before the avatar was stored.

Fix (after build 99):
- **Put the avatar in the Live Activity content** (`FleetState.look`), avoiding both explanations:
  - built-in avatars and avatar pack colors no longer rely on the keychain;
  - switching avatars changes the content, so the system redraws;
  - when the App is in the background, hub pushes also carry this phone's avatar.
- **Avatar pack images** still went in the keychain, because they don't fit in the content (4 KB for the whole activity). Build 103's diagnostics proved the extension couldn't read the keychain, so they moved to the App Group container (next section).
- **Image size**: avatar pack images were originally 78px PNGs, decoded by `UIImage(data:)` at 1×, hence 78pt, bigger than the entire compact or minimal presentation.
  Apple says images in a Live Activity must not exceed their presentation's size, or the system may not display the activity. Several third-party Live Activity libraries also cap each image at 4 KB (Apple doesn't document this; the libraries learned it empirically),
  while real pack frames scaled to 78px on iOS were 11–15 KB, and even at 42px 4.4–5.4 KB. At the time they were decoded at 26pt, and images over 4 KB used fewer pixels (about 36–42px fitted). The simulator drew both versions.
  After switching to App Group files (#346, #349), the 4 KB cap was removed and a separate @3x image was stored for each presentation's size (22 / 24 / 26pt); whether the phone can draw images of this size must be verified on a build with #349 installed.

Why the previous verification (#227) didn't catch it:
- Screenshots used mock_hub's placeholder avatar pack: solid-color discs, only a few hundred bytes as PNGs, hitting no size limits.
- The island was only checked in the simulator: its extension can read the keychain and ignores Live Activity image limits, so none of the differences from the phone showed up.
- The signing check only proves the group names are correct, not that the extension on the phone actually read them. At the time there was no way to see what it read.

#### Why the avatar pack was still wrong in build 103

The user asked "why does Circuit work, but the Claude and GPT avatars don't?": built-ins (the recolored Visor and Circuit) were correct, showing that the avatar-in-content path worked; hub avatar packs only drew the default Visor in their color.
The diagnostics page (next section) showed the same avatar in Settings, the hub and the Live Activity content; the avatar (88 bytes) and pack images (21.8 KB) the App wrote to the keychain were successfully written and read back; Extension, Last Run was None.

- **Root cause**: the Live Activity extension can't read or write the keychain on the phone. The signature contains the group and everything on the App side works, but the extension can't even write its own report; the same extension can read and write in the simulator.
  Apple only says Live Activities "run in their own sandbox" and can't use the network or location, without mentioning the keychain. This is an empirical conclusion; from outside we can't tell whether the data-protected keychain, access group or sandbox blocks it. Diagnostics only prove that neither reading nor writing works.
- **21.8 KB**: not one oversized image, but 6 images in one keychain item (5 states + portrait), about 3.6 KB each, all below 4 KB. Now each image is its own file, and the extension only reads the one it needs to draw;
  the portrait is no longer stored when all 5 states have frames.
- **Fix**: avatar pack images, the fallback avatar and extension reports all moved to files in the App Group container (`FleetLookStore`); the keychain path was removed. The App deletes the two items left in the keychain by old versions at launch
  (the App's entitlements temporarily retain the `.look` group for this; the extension's group is removed). Bundling images at build time was considered, but rejected to avoid putting third-party characters' images in the public TestFlight package.

#### Diagnostics

- **Logs**: on both sides, every App Group file write and every failed read is logged to the system log (subsystem `persona`, category `island`), including filename, byte count and error. When what the extension draws changes (or 10 minutes have passed since the last log), it logs
  "extension drew … container … saved … face …": which avatar from the content it drew, whether it has a container, which image it read. In the Mac's Console, select the phone and search `category:island`, or capture a sysdiagnose.
- **Diagnostics page**: **Settings › Notifications › Dynamic Island**, long-press the first preview. It lists the avatar's delivery path in order:
  - conclusion: the first step that went wrong;
  - avatar: in Settings, recorded by the hub, in the currently displayed Live Activity content;
  - App Group name, container path (none means the signature lacks that group), the `look.json` and pack images written this launch (success or error, byte count); each file under `Fleet/` in the container (size, modification time);
  - the avatar last drawn by the extension, whether it had a container, the stored avatar it read, the pack image it read (file, byte count). The extension writes these to the container's `seen.json`, only when changed, at most once every ten minutes.
  - buttons: **Rewrite and Redraw** (redo the container files, hub and activity content), **Copy All** (send the whole text to the developer).

  If the avatar is correct but Extension, Last Run stays None, the extension can't write to the container (if the island shows a pack image, it can still read it); if Container on the App side is None, the signature lacks the App Group.
  A DEBUG build can open it directly: `-openIslandDiagnostics YES -openIslandSettings YES -openNotificationSettings YES -tab settings`.

## ContentState (`FleetState`)

Go `hub.FleetState` and Swift `FleetState` (`apple/Shared/FleetState.swift`) match field for field. `liveactivity_test.go TestFleetStateJSON` and
`FleetStateTests` pin the same JSON on both sides:

```json
{"phase":"working","needsYou":0,"working":1,"unread":0,
 "items":[{"id":"<machine>/<session>","title":"…","place":"machine · space","kind":"working","detail":"","since":1761700000},
          // This turn is stopped, but work continues in the background (Session.background_working, #2935): background=true, detail describes the work ("parity-audit · Verify · 169/185", "后台 2 个任务在跑" = "2 tasks running in the background"); old Apps ignore background and treat it as working as usual
          // progress (#3437): the counts detail prints, for the expanded view's bar; only for a running workflow that has started an agent
          {"id":"…","title":"…","place":"…","kind":"working","detail":"parity-audit · Verify · 169/185","background":true,"progress":{"done":169,"started":185},"since":1761700000}],
 "workingSince":1761700000,"details":false}
```

- **Times are always Unix seconds (numbers)**: ActivityKit decodes a push's `content-state` with the default JSONDecoder, which interprets a `Date` as seconds since 2001.
- For background-work items (`background: true`), `since` uses the earliest `started_at` among running workflows with a known start time (#3433), so time spent waiting after the main turn ended isn't counted as workflow duration. If no such workflow exists, fall back to the turn start time, then the session update time. Foreground turns still use their own start time; the one-line `detail` text is unchanged.
- `progress` `{done, started}` (#3437) is the `agents_done` / `agents_started` of the workflow `detail` names: the first running one (`Session.workflows` is newest first, as the web's workflow line picks it), so the bar and the line beside it never disagree.
  With one workflow running (the usual case) that is also the one the timer measures; with several, the timer covers the oldest, the line and bar the newest. `done` is capped at `started`; no `progress` before the workflow has started an agent, nor for background shell commands / Monitors / subagents.
  **No extra pushes**: `progress` is worked out from the same two numbers `detail` already prints, so the content changes exactly when it did before (`TestBuildFleetProgressAddsNoChanges`), and a change is an ordinary one, coalesced at the usual pace ("Push cadence").
  This needs the counts to stay in `detail` whatever its length: a long workflow name, phase or summary is clipped to leave room for " · done/started" at the end, never the counts (`backgroundDetail`). Clipped from the end as before, a long name lost its counts, `detail` stopped changing while `progress` went on moving, and every finished agent was a push of its own.
- Every field on the Swift side is decoded leniently (unknown phase / kind values don't error), so adding hub fields won't break the activity.
- `attributes-type` is `PersonaActivityAttributes`, and `attributes` is `{"kind":"fleet"}` (used by push-to-start); neither name may change.
- Dynamic Island fields: `left` / `right` (this device's sides, `status|tpm|tokens|requests|assistant|load|timer|none`), `assistant` (assistant replying),
  `stats` (`{tpm, tokens, requests, loads:[{id, name, percent}]}`). The hub only fills the fields each device displays (`FleetState.forDevice`),
  so changes to things it doesn't display consume no pushes. The `fleet` event received by the App carries all numbers, trimmed to its own settings (`FleetState.tailored`).

## Pushes (`hub/liveactivity.go`)

- Trigger: whenever the hub broadcasts `sessions` / `interaction` / `interaction_resolved`, it recomputes the snapshot; if it matches the last push, nothing is pushed.
- See "Push cadence" below: changes to Needs You are pushed immediately (priority 10); other changes are coalesced for 2.5 seconds and sent at most once per 30 seconds per device (priority 5).
  Devices with the App open get no pushes. No alert or category (the interaction push already sounded).
- `stale-date` = now + 15 minutes. Even unchanged content is repushed every 10 minutes to renew it; if the hub disconnects, the activity goes stale and the UI shows "Waiting for hub update…" (P3 buttons are hidden too).
- End: nothing displayable for over 1 minute, or only finished-unseen for over 5 minutes, sends `end` (`dismissal-date` = now) and forgets the activity token.
- Revoking a device credential sends a priority 10 `end` with empty content and immediate dismissal to every running activity registered under that credential, before forgetting its push registrations.
  This also applies when the App is open or its Live Activity preference is off; other credentials' activities keep running. The hub waits for these push attempts to finish before forgetting their targets, because dispatch rechecks the registration.
  Delivery remains best effort: the Apple-side fallback that ends activities when every link's credential is rejected is a separate follow-up.
- Start: with an activity token, send `update`; without one (App never opened, or the previous activity ended) but with a push-to-start token, send `start` once at the beginning of a "round"
  (going from empty / only finished to something needing you or working counts as a new round). If the user swipes away the activity, it won't be reopened in that round. An activity older than 8 hours (the system limit) is considered gone and starts again next round.
  The `start` carries an alert ("2 need you · 3 working"), which is left out while the device is "Phone Muted" with a desktop online or inside its quiet hours (`quiet` in the device's prefs, #5806): the activity still starts, without lighting the screen.
- APNs: `apns-push-type: liveactivity`, `apns-topic: <bundle>.push-type.liveactivity`, addressed to the activity's own token (or push-to-start token),
  in the same environment as device registration (Debug is sandbox). 410 / BadDeviceToken deletes that token.
- One `live_activities` row per device (`device_id`, `activity_id`, `push_token`, `push_to_start_token`, `started_at`), with an in-memory copy and a database write on each change.
- While the App is open, the hub also broadcasts `{"type":"fleet","fleet":{…}}`; the App updates the activity itself (faster than a push, also works in the simulator).

## Push cadence

Each received push redraws both the Dynamic Island and the Lock Screen, so the hub paces pushes by how much a change matters:

| Change | Example | How it's pushed | Maximum Dynamic Island lag behind the hub |
| --- | --- | --- | --- |
| Critical | An Agent starts needing you (question, approval, terminal dialog), or no longer needs you (answered, answered on the computer, gone) | Immediately, priority 10 | 0 (hub-side; the preceding screen-card detection still takes 1.5 / 3 seconds) |
| Ordinary | Working / finished counts, item titles, assistant replying, turn start time | Coalesce 2.5 seconds; if less than `fleetMinGap` (30 seconds) since the last push to that device, wait until then and push the latest state | 30 + 2.5 seconds |
| Numbers only | Tokens, TPM, load | As before: at least `live_metrics_seconds` (60 seconds) per device, only if rounded values changed | 60 seconds |

- The bound is guarded by `TestFleetPacingBound` (a change every 3 seconds for 10 minutes, with questions and answers mixed in) and `TestPushReplay` (19 minutes recorded from a real hub):
  the longest continuous "Dynamic Island differs from hub" was 29.2 seconds; changing 30 seconds to 60 gives 58.8 seconds (`docs/operations/testing.md`, "Push replay").
- **No pushes while the App is open**: the App includes its push device id (`device_id`) in `register_phone`. While connected, the hub sends no APNs to its activity,
  only records "it has drawn this version"; the App updates its activity from `fleet` events. When the App actively disconnects on backgrounding (close 1000 / 1001), the hub considers the activity stopped at the last broadcast version.
  If the network drops, the App is killed, it is kicked after suspension by a ping timeout (not a voluntary close), or the last version is over 10 minutes old (possibly stale), the hub doesn't know what's displayed and immediately pushes the current state.
  Old Apps don't include `device_id` and still receive pushes.
- With the App closed, the 30-second gap cuts pushes on a recorded real hub by about 40%; with it open, the hub sends none. The battery investigation that set these numbers is in `docs/archive/apple/live-activity-records.md`.

## Usage (tokens / TPM / load)

- Source is the transcript: every 10 seconds the node (`node/usage.go`) scans new parts of all local transcripts—main sessions and sub-Agent files under each Claude Code config directory
  (`message.usage` on assistant lines; each content block of one reply repeats the same usage, deduplicated by message id; `<synthetic>` excluded),
  and Codex / compatible Codex forks rollouts (`last_token_usage` on `token_count` events; repeated events whose totals didn't increase are skipped). Accumulates the last 26 hours by "minute × model × launcher"; a reply copied into another file (a fork) is counted once (`docs/server/machines.md` ("Token usage")).
- Only new bytes are read: each file has an offset, an unfinished partial line waits until next time; for a large file first seen, it searches backwards from the tail to 26 hours ago before reading,
  skipping single lines over 16 MB (images). Measured on the hub machine's daily transcripts: first scan 85 ms, subsequent scans about 1 ms each.
- The node reports changed minutes to the hub (`usage` event, full value for each minute, repeated reports just overwrite); the first report after connecting sends everything with `full: true`.
  The node keeps nothing: restarting either side recomputes the same numbers from files. The manager also writes them to Postgres for the Machines page's week of charts.
- The manager (`devices/metrics.go`) aggregates all machines: TPM = output tokens in the last 2–3 minutes (from the beginning of the minute two minutes ago to now) ÷ minutes elapsed; Tokens Today includes cache reads and writes,
  with "today" in the phone's time zone (reported in assistant settings); load = 1-minute load average ÷ core count; machines silent for 2 minutes are excluded from load.
- Before pushing to the Dynamic Island, round to two significant digits (12345 → 12000; load to 5% increments), so small fluctuations don't create new content.

## Push budget

- State changes (needs you / working / finished, items, buttons): changes to Needs You are pushed immediately (priority 10); others coalesce for 2.5 seconds, at most once per 30 seconds per device (priority 5), see "Push cadence".
- Numbers-only changes: the hub checks every 15 seconds. For each device with an activity, it sends a priority 5 update only if more than `notify.live_metrics_seconds` (default 60 seconds) has passed since its last push
  and the rounded numbers it displays actually changed. State pushes also include the latest numbers.
- Each device remembers its last pushed content-state; identical content isn't pushed (for example, the assistant starts replying but that device's island doesn't display the assistant).
- In the foreground the App updates its own activity with `fleet` events (no push budget), and numbers follow every change; the hub doesn't push to that device then (new Apps only, see "Push cadence").

## App (`LiveActivityController`)

- At launch it listens to `pushToStartTokenUpdates` and `activityUpdates` (activities started by the hub); for each activity it listens for `pushTokenUpdates` (tokens rotate) and ending / dismissal,
  reporting all of these to `POST /api/v1/devices/{id}/live-activity`.
- On a `fleet` event in the foreground, or `GET /api/v1/fleet` on returning to the foreground: with content, start (only the foreground can `Activity.request`) or update; when empty, wait 1 minute before ending (otherwise it flickers between rounds). If only the assistant is replying and one island side displays the assistant, it is not empty: keep an existing activity (Thinking), but do not start a new one just for it. After the assistant finishes, end after 1 minute, matching the hub (#4628 / #4590, `LiveActivityController.step`).
  If identical to what's displayed (something this device doesn't display changed), don't update unless less than 5 minutes remain before staleness (each update renews for 15 minutes, `worthUpdating`): every update redraws the island and Lock Screen.
- Tapping the activity itself (compact, Lock Screen): with one Agent, open it (`persona://open?machine=…&session=…`); with several, simply return to the App (`persona://open`), at the page and position where you left (a list stays a list, a chat stays the same chat and line; if the system ended the App, cold launch restores the last chat and the reading position recorded on backgrounding). Each expanded-view row opens its own session. Before 2026-10-29 it always opened the first item; the user reported "tapping the Dynamic Island goes into some Agent".
- When the Lock Screen draws a single request (the one with buttons, or the one nameless line such as "A permission request is waiting" when details are hidden), tapping the Lock Screen opens that request's session (`FleetLink.lockScreen`, #4318): it opens what is drawn, not a guess. With several rows or only counts, as above. The Apple Watch Smart Stack (`.small`) still taps as the whole activity.
- Simulator automation (`-pairURL`) doesn't report tokens, so the hub won't mistake the real device's activity for the simulator's.

## Buttons (`apple/LiveActivity/FleetIntents.swift`)

- The first request actionable with buttons (permission, plan) has them: permission Deny / Allow; plan View (opens chat) / Approve. Rejecting a plan requires feedback, so that's left to the App.
  A card with fields (the password card, adding a mail account) is filled in in the App: the manager leaves its row's `interaction` empty (`server/internal/assistant/fleet_live.go`), so it is not actionable (`FleetState.firstActionable`): another agent's permission or plan behind it takes the buttons, and with none the row shows without them and a tap opens its conversation.
  The Lock Screen and expanded view only show this item + buttons + "+N" (a second item won't fit vertically).
- Allow and Approve use `authenticationPolicy = .requiresAuthentication` (Face ID first on the Lock Screen), Deny uses `.alwaysAllowed`, as with notification buttons.
  Approving a plan from the Lock Screen uses `acceptEdits` (you haven't read the full plan, so commands still require individual permission).
- `LiveActivityIntent`'s `perform` runs in the App process (the system starts it in the background if needed), so the extension only compiles the types, with empty `perform`
  (`WIDGET_EXTENSION` compilation condition). The App implementation is in `FleetActions`: call resolve, immediately remove this item from the activity (optimistic update; the hub push follows),
  and on failure send a local notification explaining why (410 "already handled" still removes it).
- Stale content (`isStale`, no hub push for 15 minutes) has no buttons, to avoid tapping Allow on a request already handled.

## Settings (Settings › Notifications › Live Activities)

- Lock Screen and Dynamic Island (`prefs.live_activity`, on by default): switching off immediately ends the activity in the App, and the hub stops pushing to that device.
- Show Details on Lock Screen (`prefs.lock_details`, off by default): changing it immediately changes the current activity, and the hub's next push fills `details` using the new value.
- Both are stored on the hub per device (`PUT /api/v1/devices/{id}/prefs`), with a copy in the App's UserDefaults (`LiveActivityController.settings`),
  synced from the hub on every return to the foreground. If Persona's Live Activities are disabled in system settings, this section says so and offers Open Settings.
- Disconnecting the hub (forgetting the server) ends the activity: its buttons point to the old hub. Before that the App revokes its credential there, and the hub drops its activity and push-to-start tokens with it, so no new activity is started remotely (`docs/protocol/multi-hub.md` "Push and notifications"). A hub the App couldn't reach keeps them until this device is revoked in Settings › Devices on another device.
- No separate "watch one Agent" activity: Fleet already includes every running Agent, and a second activity would stack two on the Lock Screen; add `kind: agent` if needed later.

## Verification

- Layout: `-showFleetSample needsYou|working|background|done|thinking|assistant|offline` (`thinking`: only the assistant replying, plus one finished-unseen item; `assistant`: only the assistant, nothing countable, so the minimal view draws the avatar; `background`: an Agent whose workflow runs in the background ahead of an ordinary working one (#3437); `offline`: a working activity marked stale immediately; `-fleetDetails YES` shows Lock Screen details, `-fleetIsland b|c|custom:assistant,load` selects the island sides, `-openPersona persona` selects the default Mochi) starts an activity with fabricated data, no hub needed.
  UI test `LiveActivityShots` reads `PERSONA_FLEET_ISLAND` and `PERSONA_PERSONA` (avatar packs need `PERSONA_MOCK_HUB`: mock_hub with `MOCK_PERSONAS=builtin`, downloaded by the App first).
  `testSwitchingPersonaRedrawsTheIsland` reads `PERSONA_PERSONA_SWITCH=nwverse,persona`: while the activity runs, switch through them in **Settings › Appearance › Avatar**, returning home for 3 seconds and capturing the island each time. Two compact shots 0.4 seconds apart (`compact` / `compact2`) check for animation, then another 3 seconds later (`compact3`; just after going home, the island may still be animating and empty).
  `PERSONA_ISLAND_PEER` (another app's activity, landscape) and `PERSONA_FLEET_COUNT` (`-showFleetCount`, changes the number of count digits) are described under "Sharing the Dynamic Island with other apps" above.
  Background work from a hub's JSON: `MOCK_FLEET=working MOCK_WORKFLOW=1 scripts/mock/mock_hub.py` sends the review Agent as a background item (`background`, `detail`, `progress`, the workflow's `since`) the way the manager builds it; open the App paired to it, then `PERSONA_FLEET_SAMPLE=current`.
  In the simulator a stale activity only appears on the Lock Screen (gray avatar + "Waiting for hub update…"), not the Dynamic Island.
  Settings page: `-tab settings -openNotificationSettings YES -openIslandSettings YES`.
  `simctl io screenshot` can't capture the Dynamic Island or Lock Screen; use UI test `LiveActivityShots` for compact / expanded / Lock Screen shots (`docs/operations/testing.md`).
- Push: `xcrun simctl push` doesn't support Live Activities, but the simulator can receive real APNs sandbox pushes. The DEBUG build prints the activity push token in the system log
  (`subsystem == "persona"`); on the hub machine, run `PERSONA_LA_TOKEN=<token> PERSONA_LA_TOPIC=<bundle id> go test ./internal/hub -run TestManualLiveActivityPush`
  to send an update with the hub's APNs key, then capture it with `PERSONA_FLEET_SAMPLE=current` (verified 2026-10-29: Go-generated JSON → APNs → simulator activity displays correctly).
- Buttons: UI test `LiveActivityActionTests` connects to `mock_hub.py`, taps Deny on the Lock Screen, then reads `GET /mock/resolved` to confirm the hub received `allow:false` (passed 2026-10-29).
- Real device only: push-to-start (hub starts an activity after the App is killed), update budget, Watch Smart Stack.

## Pitfalls

- Calling `endAll()` then `request` in the sample collides: the activity being ended still reports `.active`, so no new activity starts. Use `apply` directly (update if present).
- A Lock Screen / expanded view over about 160pt is clipped (top or bottom), with no error: take a screenshot before adding content.
- A linear `ProgressView`'s track is the system fill, about 1.2:1 on the black of the expanded island and the Lock Screen: a partly filled bar looks finished, or like a stray line. Draw the bar with a track of your own (`FleetBar`, #3437).
- `Text(timerInterval:)` takes all available width; give it a fixed width, or it squeezes neighboring text into "3…".
- Compact and minimal Dynamic Island views aren't given a width (`GeometryReader` measures its default size); the island grows with content until the cap clips it. Only a width-limited view gets 28pt.
  So `ViewThatFits` is useless here; choose the look by content and measure with `IslandFitTests`.
- Repeating symbol animations (`.rotate`, `.pulse` with `.repeating`) don't animate in Live Activities, though the App previews do: use static symbols on both sides so the preview doesn't mislead.
- `sips -c <height> <width> --cropOffset 0 0` is treated as a centered crop (offset 0 means omitted); to crop from the top use something like `--cropOffset 1 0`.
- After a UI test locks the simulator, unlock it, or the next `xcodebuild test` reports "test runner hung before establishing connection".
- The first shots after switching the interface language (`PERSONA_UI_LANGUAGE`) can show every text of the activity truncated ("3件作…", "169/1…") with room to spare; the next run draws it right (#3437, iOS 27.0 simulator). Shoot again before treating it as a layout bug.
