# Avatar (persona)

Here `persona` means the avatar (name / picture / status dot), not a separate product. The bundled catalog contains only **Mochi** (`persona`, the default) and **Milk Frog** (`nwverse`, the hub's downloadable pack). The former Visor, Circuit, Pixel, Facet and Jelly drawings are retired. Their IDs remain reserved only for compatibility; they are not selectable and saved choices display Mochi. Pack manifests and protocol fields retain their `persona` names; see [Naming and configuration](../operations/config.md#naming-and-configuration).

An avatar = Persona's **name**, **picture**, the **animated status dot** in Persona's top bar, and the **App icon** (the logo inside the App and the home screen icon), optionally with a **color scheme**.
You pick it in **Settings › Appearance › Avatar**, and once picked all of these change together: the face and name in the top bar (only the face, top left, in the new shell), the sidebar card, the profile page, the input field's "Ask xxx…",
the Dynamic Island and Lock Screen Live Activity, the logo in front of the main chat in the sidebar and at the top of Settings, and the home screen icon. The App icon has no separate setting (requested 2026-11-05: merge into one "avatar", #3203); see "App icon" below.

**An avatar doesn't change the App's colors** (requested 2026-10-31: keep "avatar, theme color and UI components" as separate choices). Settings has three independent choices:
avatar (this document), theme color (`ThemeColor`), and interface style (`AppStyle`, which only governs how components are drawn); see `docs/apple/ios.md` ("Design language").
An avatar's color scheme is only used when the theme color is set to **Match Avatar**; previously picking an avatar overrode the interface style's colors (pick Clay, then Milk Baby, and the purple was gone).

- **Built-in drawing**: only **Mochi** (id `persona`), the waving white dumpling from the App icon. Its paper-colored base, black outline, eyes, orange blush and raised hand use the icon's own light / dark colors, not the selected theme color. Small sizes omit motion lines and highlights and enlarge the eyes and blush.
- **Restoring a choice**: the hub stores the raw choice and timestamp, but `current.effective` resolves an unavailable or unknown ID to `persona`. Clients use that effective ID, reject retired IDs in downloaded manifests, and normalize retired local drawing settings to Mochi. An extension cannot reclaim a retired ID and unexpectedly replace an earlier choice.
- **Android**: `PersonaFace.kt` in `core/designsystem` draws only `PersonaDesign.Mochi`, with the same five states and icon coordinates. `AppStyleStore.personaId` caches the choice.
- **Web**: `web/src/views/assistant/MochiFace.tsx` draws the SVG in the `--mochi-*` light / dark colors from `styles.css`. Web Animations (`face-motion.ts`) stop under Reduce Motion. The selected pack supplies the in-app brand mark; the packaged icon fallback is always Mochi (`web/src/lib/look-icon.ts`).
- On **Android**, what follows the avatar: the top bar face, the sidebar / chat list, the floating capsule, and the cards in Settings (all through `PersonaFace` / `LocalPersonaPack`). What doesn't: the launcher icon (only Mochi is packaged, see extensions.md ("Avatars")),
  the small notification icon, and the icons of the Quick Settings tile and widgets (`ic_stat`: the system requires monochrome vector resources, so it can only be the packaged image).
- **Hub built-in avatars**: avatar packs compiled into personad (`server/internal/hub/personas/<id>/`), present on every hub with no manual install; the phone downloads and caches one when it's selected.
  Currently there is only **Milk Frog** (id `nwverse`): a tiered avatar whose look changes with the user's level on the NWVERSE contribution board, see "Tiered avatars" below.
- **Other avatars**: placed in `~/.persona/personas/<id>/` on the hub machine; the phone downloads and caches one when it's selected. When the id matches a built-in avatar, the directory wins (to try new artwork on one hub first).
- **Uploaded avatars** (#2404): made in the apps from an image of the user's own, see "Uploading an image" below. No terminal needed.
- **Extension avatars** (#3200): an extension declares them in its manifest's `looks`, and the hub serves them as avatar packs (also listed in `/api/v1/personas`); see [extensions.md](../extensions/extensions.md) ("Avatars").
- **The selected avatar is stored on the hub and synced across platforms** (#3200): `GET /api/v1/looks` is the unified list of built-in avatars, avatar packs, and extension avatars plus the current choice; `PUT /api/v1/looks/current` changes it and broadcasts `look`.
  The per-platform local keys mentioned below (`persona.id`, `AppStyleStore.personaId`, `persona.persona`) become caches, migrated up the first time a new hub is connected; rules in extensions.md ("Avatars").
  The App icon is merged into the avatar: places that can only use a packaged icon pick by the avatar's `app_icon`.
- Demo mode has only the default avatar (`AppModel.persona`).
## Why avatars live on the hub and not in the App bundle

Avatars are often third-party characters found online (sometimes with another platform's watermark still on the image), while TestFlight is a public link and the repo is shown to friends. So:

- **Third-party characters stay out of the repo and the App**: neither the originals nor the generated assets go into the repo or the App bundle; they're installed only on your own hub.
- **Official avatars with confirmed rights go into the repo and are compiled into personad** (on 2026-11-03 the user confirmed Milk Frog's rights are fine and wants it available to all Persona users):
  the finished pack and the generation parameters (`scripts/assets/personas/<id>.json`) go into the repo, so self-deployed hubs from source, release builds, and Docker hubs all include it;
  the raw generated images (about 1.6 MB each) stay out of the repo. The App bundle still doesn't contain it: the phone downloads it from the hub like any other avatar.
- **What the repo does contain**: the protocol, loading code, generation scripts, and original work: the default avatars and the demo avatar `demo` that the script draws on the fly (no images stored).
- **Working directory for generation**: keep it outside the repo (or in the uncommitted `.lane/`).
- **Release checks run as usual**: `check_bundle_privacy.py` and the privacy hooks run as normal.
- **To change this later**: to put another avatar into the repo or the App, confirm the rights first, then update this section.

## Uploading an image (#2404)

The quickest way to a custom avatar, without a terminal: the **Upload** card at the end of the avatar picker, on the web in **Settings › Persona › Avatar** and on the profile page, and **New from Photo** in **Settings › Appearance › Avatar** on iPhone, iPad and Mac (Android: #2407). Pick an image, crop it square with a circular preview, give it a name, and the hub makes a package of it (`POST /api/v1/looks/uploads`, `docs/protocol/protocol.md`). The apps offer it only when the hub lists the `look_uploads` feature.

- **What the hub makes** (`server/internal/hub/look_uploads.go`): `personas/u-<8 hex>/` in its own data directory, written beside the others under a dot name and moved into place in one step, then checked like any package. The avatar at 64, 128, 256 and 512 px (square: the apps crop it to a circle), and one **still frame per dot state** at 144 px, drawn from the image the way frame 0 of a generated package marks the state: a thought bubble with the first dot lit (thinking), an orange arc from twelve o'clock along the rim (working), the red ring (needs you), gray with z's rising to the top right (offline). The marks are drawn at 3× and scaled down; the z's sit on a light halo so they read on a dark picture. No animation (a still frame is also what Reduce Motion shows of any package), no `colors` (so **Match Avatar** is not offered for it), `app_icon_fallback` is Mochi.
- **Owned by the hub**: the packages are files in the hub's own directory and the choice is the hub's setting, so these are routes of the hub's own (`hub_routes_allowed.txt`), not a manager's: a manager may run on another computer and could not write there.
- **Marked as uploaded**: the manifest's `source.kind` is `upload`, and `GET /api/v1/looks` lists them with `"uploaded": true`. Only these can be deleted from the apps (`DELETE /api/v1/looks/uploads/{id}`); a package placed by hand, a built-in one and an extension's are refused. Deleting the chosen one brings the default back on every device.
- **Bounds**: images up to 8 MB and 40 million pixels, at least 64 px a side; PNG, JPEG, WebP or the first frame of a GIF; names 1–40 characters; at most 50 uploaded avatars. The apps scale the crop down before sending (1024 px is plenty: the largest size kept is 512).
- **Privacy**: the image goes only to your own hub, like any package there (see "Why avatars live on the hub" above). The original is not kept: only the sizes and frames made from it.
- **Web** (`web/src/views/assistant/LookUpload.tsx`, details in [web.md](../web/web.md) "Avatar from an image"): the browser decodes the image upright (EXIF orientation applied, a GIF's first frame) and sends the crop drawn at its own size, at most 1024 px, as a PNG, so what the crop box showed is what the hub gets. The request goes over XHR to show the bytes sent. Deleting asks first, in a dialog that says when the look is the current one; Delete sits in the uploaded look's corner, on hover and keyboard focus, always on touch screens.
- **iPhone, iPad and Mac** (#2405, `apple/Persona/Views/Settings/AvatarUploadModel.swift`, `AvatarSources.swift`, `AvatarCropView.swift`): with `look_uploads` in the hub's features, a **New from Photo** card ends the grid in **Settings › Appearance › Avatar** (an older hub, or Demo Mode: no card, the page as before). The phone opens Photos (held, the card's own menu also has Files); the Mac opens an image file, whose open panel also reaches Photos (its right-click menu has the Photos picker). That menu is the card's own (a `Menu` with a primary action on the phone, `macBoundedContextMenu` on the Mac), never a `.contextMenu`: the grid is one Form row, which takes a `.contextMenu` as the whole row's, and a long press on any avatar lifted the grid and offered Photos and Files. The image is decoded upright (its EXIF orientation applied, the first frame of an animated one, at most 4096 px) and shown under a square with a circle over it: drag to move, pinch to zoom (the Mac also has a slider; VoiceOver swipes up and down), never under 64 px of the image or past 8×. A name (required, cut at 40 characters as the hub counts them, Unicode scalars) and an optional description (80). **Upload** sends the crop square, in sRGB, at most 1024 px, PNG (a photo whose PNG is over 1.5 MB goes as JPEG; transparency stays PNG), with `choose: true`, then reads the list again, downloads the package and chooses it on this device. The list is read after the hub answered (`PersonaStore.reload`): a refresh already under way, which the hub's `personas` broadcast joins, may have asked before the avatar existed, and is waited for and followed by a new one. When the avatar was made but this device couldn't get it (no list, a file that did not come), the sheet stays open and says so; its button becomes **Try Again**, which only downloads it again (the image is never sent twice), and closing the sheet then loses nothing: the avatar is in the list. One upload at a time; no Cancel or swipe-down while it runs. A refusal shows the hub's `error` under the fields as it is; no answer at all shows "Couldn't reach the hub…". The **Upload** button has its own key (`avatar.upload`): Japanese says 作成, which leaves the title whole.
- **Deleting in the apps**: an uploaded avatar's card has a delete button (a trash can on the style's round button surface, `chromeSurface`, as the image viewer's) in its top trailing corner, always shown, on the iPhone, iPad and Mac alike, as the web's; the corner that answers is at least 44 pt at every text size (`tapTargetFrame`), though the trash can drawn shrinks with Small text; VoiceOver also has **Delete Avatar** as an action on the card. Both ask first, in one confirmation for the whole grid that names the avatar and deletes exactly that one. Not a context menu: the grid is one row of a Form, and iOS and the Mac give a row one menu, so a long press on one card lifted the whole grid and ran another card's menu (it deleted the wrong avatar, #2405). The button lies over the card, not in its label, so it never chooses the card; it goes while that avatar is being deleted. When it was the one chosen on this device, the default is chosen here (the hub does the same for the others), and the list is read again after the delete, as after an upload; one already gone from the hub (404) counts as deleted. The footer under the grid says how to make and delete them, only when the hub makes them. A card of an uploaded avatar shows its description, or "From your image", where other packages show their colors.

## Pack format (format 1)

```
~/.persona/personas/doudou/
  persona.json
  avatar/64.png  avatar/128.png  avatar/256.png  avatar/512.png
  dot/idle.png  dot/thinking.png  dot/working.png  dot/needs_you.png  dot/offline.png   # APNG
  preview.png    # overview for humans from the generation script; unused by the App (the hub still serves it)
```

```json
{
  "format": 1,
  "id": "doudou",
  "name": "Doudou",
  "subtitle": "A round little yellow fellow",
  "avatar": { "sizes": { "64": "avatar/64.png", "128": "avatar/128.png", "256": "avatar/256.png", "512": "avatar/512.png" }, "safe": 1.0 },
  "dot": {
    "size": 144,
    "states": {
      "idle":      { "file": "dot/idle.png", "fps": 8, "frames": 24 },
      "thinking":  { "file": "dot/thinking.png", "fps": 8, "frames": 16 },
      "working":   { "file": "dot/working.png", "fps": 10, "frames": 12 },
      "needs_you": { "file": "dot/needs_you.png", "fps": 6, "frames": 12 },
      "offline":   { "file": "dot/offline.png", "fps": 4, "frames": 12 }
    }
  },
  "colors": {
    "light": { "primary": "#F2C230", "secondary": "#7FAE5A", "accent": "#94600A", "bubble": "#8A5800", "bubble_text": "#FFFFFF" },
    "dark":  { "primary": "#F2C230", "secondary": "#7FAE5A", "accent": "#B47A0C", "bubble": "#8F5C00", "bubble_text": "#FFFFFF" }
  }
}
```

| Field | Description |
| --- | --- |
| `format` | 1. Neither hub nor App accepts any other value (an incompatible format change later bumps it, and old Apps simply don't see new packs) |
| `id` | `a-z0-9-`, at most 32 characters, matching the directory name. `persona` and the five retired IDs (`persona-visor`, `persona-circuit`, `persona-pixel`, `persona-facet`, `persona-blob`) are reserved and rejected as pack IDs, including extension packs. `nwverse` belongs to the bundled hub pack |
| `name` / `subtitle` | `name` is used in the top bar, sidebar, profile page, and input field; `subtitle` only in Settings |
| `avatar.sizes` | Side length (pixels) → file, square PNGs. The App picks the smallest one that's big enough for "display points × screen scale" and crops it to a circle. `safe` is the ratio of the circle holding the important content to the inscribed circle (1 = the whole inscribed circle is safe; the script's images are already round) |
| `dot.size` | Frame side length (pixels). The top bar shows it at 46 pt; 144 px is exactly 48 pt @3x |
| `dot.states` | One APNG per state for five states (a plain PNG when there's only one frame); `idle` is required, and the App uses `idle` for any missing state. `fps` is only used when the file doesn't specify per-frame durations; `frames` is only for humans |
| `colors` | One set of `#RRGGBB` per appearance, see "Color scheme" below. The whole block can be omitted (change only the face, not the colors) |
| `about` | Optional, a few words (no more than 10; Milk Frog's is "NWVERSE level"): the second line of the card in **Settings › Appearance › Avatar**, replacing the color swatches (built-in avatars show "Follows the theme color" there); on Android, the option's description line |
| `app_icon_fallback` | Optional; the only supported built-in icon is `persona` (Mochi). Missing, retired or unknown values fall back to Mochi. The logo inside the App still uses the pack's own picture |
| `tiers` | Optional, only for tiered avatars: where the level comes from, the default tier, and each tier's manifest, see "Tiered avatars" below. The hub doesn't send it to phones |

**Validation rules**

- **Hub side** (`server/internal/hub/persona.go`): packs that don't pass don't appear in the list, and the reason is logged. It checks:
  - file names must be plain relative paths inside the pack: no `..`, no components starting with a dot, no following symlinks;
  - each file ≤ 4 MB, the whole pack ≤ 16 MB;
  - `idle` is required;
  - state names must be one of the five.
- **App side** (`PersonaManifest.problem`): checks the same rules again.
- **Script**: `make_persona.py check` checks these rules together with the contrast floor.
## App icon

The avatar decides the in-app logo; there is no separate App icon picker. Only Mochi's packaged home screen icon remains. Downloadable hub and extension packs retain their own in-app picture but use Mochi for surfaces that require a bundled icon. Retired or unknown icon fallbacks also resolve to Mochi.

- **Packaged icon**: Mochi uses the primary `AppIcon`, plus its fixed Light / Dark variants. The preview is read from the same art by `scripts/assets/make_alt_icons.py`; no retired drawing is generated. The former Mac face-rendering helper was removed with the retired icons (#6220).
- **The logo inside the App** (`AppGlyph`: the main chat row in the sidebar, the top of Settings, the welcome / pairing pages): built-in avatars draw their icon; avatar packs draw the pack's own picture (cropped to the icon's rounded square).
- **Home screen icon** (`AppIconModel.follow`): checked once when an avatar is chosen, when light / dark changes, and every time the App returns to the foreground; if it doesn't match, it calls `setAlternateIconName`, and the system shows its "icon changed" alert once (unavoidable); if it matches, nothing happens and nothing pops up.
  While an avatar pack is still downloading, it switches to the icon the pack declares in the list (`PersonaStore.appIcon`), so it doesn't switch back to the default first and then back again. Demo mode uses Mochi's. The watch and Mac have only one icon.
- **Platform limits**: iOS home screen icons can only be the ones packaged at build time; images downloaded at runtime can't be home screen icons. Avatars from the hub and from extensions fully apply inside the App (picture, dot, name, colors, the in-App logo);
  the home screen icon uses the built-in avatar named in its `app_icon_fallback`, or the default if none. A line of small text under **Settings › Appearance › Avatar** explains this.
- **Migrating old settings: the avatar wins**. The system remembers the home screen icon and the App doesn't store it, so there's no data to migrate: the first time the App returns to the foreground after updating, `follow` finds the icon doesn't match the avatar and switches to the avatar's icon (one alert).
  - Mochi + default icon (the vast majority): already matches; nothing changes.
  - Mochi + Peach / p. / Aura: these three have been removed from the bundle (they don't correspond to any avatar), so the system can't find that icon set and the home screen already shows the primary icon; `follow` recognizes the removed icon and doesn't switch or alert.
  - A retired drawing or icon choice resolves to Mochi; existing downloaded packs remain available when still installed.
  - Mochi with light-only / dark-only chosen: still the light-only / dark-only set as before.

## The Milk Frog on the watch

With Milk Frog chosen on the phone (`WatchSync.push(persona:)`), the watch's list starts with a row showing the frog and what the agents are doing; tapping it opens a page where the frog moves. With any other avatar the watch is unchanged (#6272).

| State | When (first match wins) | The frog |
| --- | --- | --- |
| Offline | No hub answered | Alone in the rain under a leaf |
| Needs you | An agent is waiting; counts agents, not cards | Rings a little bell |
| Working | An agent is working | Types on a laptop balanced on its belly |
| Offline | A hub is out of reach and none of the above | Same as above: a hub it cannot see might not be calm |
| Finished | An agent finished and is unread | Lifts a DONE sign |
| Asleep / Idle | Nothing going on: 23:00 to 07:00 / otherwise | Sleeps / sips its milk |

`WatchPet.status` in `apple/Shared/WatchPet.swift` holds these rules. Tapping the frog buzzes; when idle it glances at you (`tap`, drawn on the idle pose), otherwise the state's clip plays again. With the wrist down the system stops the animation and the page draws the clip's `still` frame; with Reduce Motion it draws that frame too (#2943).

**On the watch face** (`PetComplication`, a second widget in the watch's WidgetKit extension `PersonaWatchWidgets`, beside the extension widget): circular, corner, rectangular and inline complications show the round face of the state, and the rectangular one its words. The face is still; the frog moves only in the app. The complication needs no network, credentials or strings of its own: after each refresh the watch app writes `WatchPetSnapshot` (state, words, the data's time) and copies the faces into `Pet/` in the watch-only App Group container (`PERSONA_WATCH_APP_GROUP`, read through the `PersonaAppGroup` Info.plist key), and reloads the complication's timeline (kind `pet`, not the extension widget's) only when what the face shows changes or every 20 minutes at most. While the frog is chosen, the app asks watchOS for a background refresh about every 30 minutes. watchOS decides when one comes, so a snapshot older than an hour shows the frog in the rain with "Not updated lately" rather than an old calm. Signing needs nothing beyond the watch group's account steps the extension already has (`docs/operations/release.md` "App Group").

**Where the art lives.** Like the rest of Milk Frog, `pet/` sits in the built-in package and the App bundle has none of it. The watch fetches it directly with the existing `GET /api/v1/personas/nwverse/{file…}`: first `pet/pet.json`, then every file named there, by SHA-256, shown only once all of them are here. A hub without `pet/` answers 404 and the watch shows no frog. A tier's rev and `bytes` cover only that tier's files, so phones never download `pet/`.

`scripts/assets/make_watch_pet.py` builds `pet/` from the finished frames, which stay out of the repo. Each clip is an animated PNG of 288 px at 10 to 12 fps; the whole of `pet/` is about 8 MB of personad. Keeping the frog's body fixed from frame to frame is what keeps these small and free of flicker, so new clips should warp or composite a still rather than regenerate frames.

## Tiered avatars (Milk Frog)

One avatar with several looks: the hub picks a tier for the phone based on the user's level. To the phone it's just a regular format 1 pack, and a tier change is that pack's rev changing,
so old App versions, Android, and the web all work without a new release.

```
server/internal/hub/personas/nwverse/
  persona.json                     # manifest of the default tier (unhatched) + tiers: old hubs without tiering use it as a plain pack
  tiers/<tier>/persona.json        # this tier's manifest, with files written from the pack root: tiers/<tier>/avatar/64.png …
  tiers/<tier>/avatar/{64,128,256}.png  tiers/<tier>/dot/<state>.png
```

```json
"tiers": {
  "source": {"kind": "nwverse-board", "url": "https://nwverse.github.io/contribution/data/board.json"},
  "default": "unhatched",
  "levels": [{"key": "egg", "name": "Frog Egg", "when": {"newbie": "pending"}, "manifest": "tiers/egg/persona.json"},
             {"key": "tadpole", "name": "Tadpole", "when": {"level": 0}, "manifest": "tiers/tadpole/persona.json"}, "…"]
}
```

| Tier | Condition on the board | Look | Motion |
| --- | --- | --- | --- |
| Unhatched (default) | Not signed in to GitHub, not on the board, or haven't passed the newcomer review | A cream-white egg blinking through a crack, on warm gray | Wobble: rocks on its base, wobbles hard while working, hops when calling you |
| Frog Egg | Newcomer review in progress | A little yellow blob peeking out of the eggshell, hearts rising on cream | Peek: pokes its head out again and again, bounces while working, shoots up when calling you |
| Tadpole | Passed the newcomer review (level 0) | Round head with a little tail, blowing bubbles on pale aqua | Swim: drifts side to side, swims hard while working, bobs up and down when calling you |
| Little Milk Frog | Magic ≥ 50 (level 1) | Small with black bead eyes, little flowers on a mint meadow | Hop: hops now and then, hops in place while working, big jump when calling you |
| Milk Frog | ≥ 150 (level 2) | Standard standing pose with bulging green eyes, soft rotating light on yellow-green | Breathing (the default motion) |
| Laughing Milk Frog | ≥ 400 (level 3) | A 3D Milk Frog following the reference image (pear-shaped body, small green-ringed eyes on the sides, beak): laughing open-mouthed with a pink tongue, hands on its belly; orange-gold rays, sparks and a ring of flame on a dark base | Shakes with laughter, lunges forward punching while working; the working arc is a comet |
| Cosmic Milk Frog | ≥ 1000 (level 4) | A 3D Milk Frog following the reference image: squatting with peace signs in both hands, tongue out, white socks (no logo drawn); deep-space nebula, twinkling stars and a neon ring | Floats, pulses as if charging while working; the working arc is a comet |

Low tiers are on light bases and soft; high tiers are on dark bases, glow, and move more (`THEMES`, `MOTIONS` in `make_persona.py`). The state cues (thinking bubble, working arc, red ring for needs you, graying out when offline)
are the same in every tier; only how the character itself moves changes, so states stay recognizable. The name is always "Milk Frog", and the card's second line is the `about` "NWVERSE level".

**How the hub picks a tier** (`server/internal/hub/persona_tiers.go`):

- **Who you are**: the account signed in to the GitHub connector (sign-in is only used to recognize which person on the board you are; the board is public and fetching it needs no sign-in); `personas.github_login` in hub.json can override it.
- **The board**: fetched every 10 minutes with an ETag (the board publishes every 30 minutes and GitHub Pages caches for 10 minutes; unchanged means 304). After startup it waits 1 minute first so the GitHub connector can recognize the account.
- **Mapping**: when `level` has a value, take the highest tier not above it (if the board adds levels later, show the highest one here first); before hatching, go by `newbie.status`; if you're not on the board, the default tier.
- **Not signed in → default tier; board unreachable → keep the last tier**. The last tier is stored in Postgres (setting `persona_tiers`), so it doesn't flash back to the default tier after a redeploy.
- **A tier change broadcasts `personas`**: phones refetch the list and switch only once the new tier's pack is fully downloaded (iOS and Android download alongside and move it into place in one go; the web preloads every file first); if the download fails, the old one stays.
  Old Apps that haven't upgraded don't get the broadcast and switch the next time they connect to the hub or open Settings.
- **An incomplete tier** (missing files) gets the default tier instead; the phone never gets half an avatar.
## States (the top bar dot)

In the iPhone shell it's the button at the top left of both pages that opens the drawer (`ShellPersonaButton`, docs/apple/ios.md ("New shell")).

| State | `dot.states` key | When (`AppModel.personaState`) | Generation script's animation |
| --- | --- | --- | --- |
| Idle | `idle` | Default | Breathing (slight scaling, up and down), blinks once at the end of each loop |
| Thinking | `thinking` | The chat is running but hasn't written text or called a tool yet: the last item is a user message or thinking | Chin in hand, eyes raised, gently swaying; three dots light up in turn in a thought bubble at the top right |
| Working | `working` | The chat is running and is already writing or calling tools | Two alternating poses plus bouncing; an arc in the primary color circles along the edge |
| Needs you | `needs_you` | This chat has a pending interaction (question, approval) | Two-beat waving; a red "needs you" ring around the outside breathes |
| Offline | `offline` | The hub is unreachable, or the machine Persona runs on is offline | Asleep, faded to gray overall, z's floating at the top right |

Animation conventions:

- **Frame 0 alone must show which state it is**. When Reduce Motion or Low Power Mode is on, only frame 0 is shown, without motion.
- Keep frame rate and frame count modest: 4–10 fps, 12–24 frames per loop.
- Design trade-off (2026-10-31): state colors (needs-you red, the working arc) are drawn into the frames, and the App no longer overlays anything. That way an avatar's five states are entirely up to the pack, and adding a new avatar needs no code changes.

## Why APNG + UIImageView

| Option | Trade-off |
| --- | --- |
| Lottie | Needs vector animation and a third-party library; AI generates bitmaps, which can't be converted |
| GIF | No partial transparency, only 256 colors, fuzzy circular edges |
| Video (HEVC with alpha) | Heavy player, not worth it for many small instances, and hard to generate |
| SwiftUI redrawing every frame | The dot lives in the top bar permanently; running layout and drawing every frame drains battery |
| Sprite sheet (one big image) | Workable, but not a standard format; opening the file doesn't show the animation |
| **APNG** (chosen) | Decoded natively by ImageIO, supports partial transparency, animates directly in browsers / Finder, and Pillow can write it |

Playback uses `UIImageView.animationImages` (`PersonaFramesView`):

- Frame switching is done by Core Animation in the render server; the App runs no code per frame.
- It stops when the view leaves the window.
- Only the states in use are decoded, with at most 6 states cached (`PersonaStore.frames`).

One state at 144 px × 24 frames is about 2 MB decoded; a whole pack is about 1–2 MB.

The built-in Mochi drawing doesn't use bitmaps; `AssistantAvatar` draws the same five states:

- Idle blinks every 3.6 seconds: a periodic TimelineView plus a short phase animation, with no redraws between blinks.
- Thinking, working, and needs you animate at 15 fps.
- Offline is gray and still.
- Mochi's five states: idle looks like the icon and blinks every 3.6 seconds; thinking raises its eyes to a thought bubble at the top right (three orange dots, motion lines tucked away);
  working scans its eyes side to side, bounces gently, lights its motion lines in turn, and an orange arc runs along the circular edge; needs you has a breathing red ring around the outside and sways side to side (waving);
  offline turns gray, closes its eyes, and has no motion lines. Frame 0 of each state is recognizable on its own.
- Drawing is in `Shared/Design/PersonaFace.swift` (one frame at a given moment); `AssistantAvatar` only provides the clock. The Lock Screen / Dynamic Island Live Activity uses the same code to draw still frames (`docs/apple/live-activity.md` ("Avatar")).

## Color scheme → design tokens

`colors` is one of the theme color options: when **Settings › Appearance › Theme Color** is set to **Match Avatar** (shown only if the current avatar has a color scheme), it becomes the App's colors
(`ColorSpec(persona:over:)`), combinable with any interface style like other theme colors (`StyleSpec.compose`), and everything reads tokens as usual.
Items the pack doesn't provide use the current interface style's own colors. With any other theme color selected, only the avatar's `primary` is still used (the avatar's glow).
Android (#3048) reads `accent`, `bubble`, `bubble_text` (**Settings › Interface Style › Theme Color › Match Avatar**, `ThemeColors.persona`); accent is likewise pushed toward black / white until it clears the floor, and the bubble is adjusted when text on it falls below 4.5:1; `primary` isn't read yet.

The web (#2980) reads only `accent`: when **Settings › General › Appearance › Theme Color** is set to **Match Avatar**, it replaces the accent token group, and bubbles use a pale tint derived from it (web bubbles use dark text), see `docs/web/web.md` ("Theme color and text size").

| persona.json | token | Where it's used |
| --- | --- | --- |
| `accent` | `StyleSpec.accent` → `Theme.accent` | Tappable things: buttons, links, selected states, tab tint |
|  | BeautifulUI's `accent` / `accentTint` |  |
|  | Links and "my bubble" in Lark pages | Follows like any other theme color, including the "Lark" style (for Lark blue, choose the theme color **Lark Blue**) |
|  | Background and cards of Clay and Island | A pale wash computed from accent (`ColorSpec.Wash(accent:)`) |
| `bubble` / `bubble_text` | `StyleSpec.userBubble` / `userBubbleText` | Your own messages; colored bubbles with white text are darkened slightly, like each style's own |
| `primary` | `StyleSpec.personaPrimary` → `Theme.persona` | Decoration only: the avatar's glow, the card background in Settings. Present whatever theme color is chosen; equals accent when absent |
| `secondary` | none | Only used at generation time: the pale tint of the dot background |

**Contrast floor**: like every theme color, it must pass on every interface style (`StyleSpecTests` checks the hardest case, a yellow avatar, combined with all styles). When a pack's colors fall short, the App pushes them toward black (or white) until they pass (`PersonaPalette.meeting`), so a pack can't make button text unreadable.

- White text on the primary color ≥ 3.5:1;
- the primary color as a link against the page background ≥ 3:1 (light against white, dark against black);
- text on bubbles ≥ 4.5:1.

How the generation script picks colors:

- The primary color is the hue covering the most area on the character (`primary`); the secondary is the next hue at least 40° away from it (`secondary`).
- `accent` and `bubble` use the primary hue, darkened just enough to clear the floor.
- Colors like yellow that can't carry white text become honey / amber.
- For other colors, provide them manually with `--accent-light` / `--accent-dark`.

**Switching avatars is like switching styles**: RootView rebuilds the whole view tree from `AppModel.look` (interface style + theme color + avatar colors) (`Theme.persona` is a static token). The open tab and chat are remembered; the Settings navigation path lives in `AppModel.settingsPath`, so the avatar picker page **Settings › Appearance › Avatar** (`PersonaPicker`) is still open after the rebuild.
## On the phone (`PersonaStore`)

- **Cache directory**: `Application Support/Personas/<id>/<rev>/`. Downloads are first written alongside to `.<rev>.part` and moved into place in one go once complete, so half a pack is never used. `index.json` is the last list.
- **Launch**: shows the selected avatar straight from disk, without waiting for the hub.
- **Refreshing the list**: once each time the hub connects, Settings opens, or a `personas` broadcast arrives. If the rev changed, the new one is downloaded (swapped in only once the whole pack is down), then old revs nobody uses are deleted.
- **Gone from the hub**: the default avatar is used, but the user's choice is kept and restored when the pack comes back.
- **Old hub** (no such endpoint, 404): Settings shows "This hub can't host avatars yet. Update the hub and check again."

## Adding a new avatar

1. **Prepare 1–3 images of the character**: full body, a close-up of the head, and a characteristic action. Keep them outside the repo, or in `.lane/`.
2. **Generate poses**: uses Codex's own sign-in; no API key needed.

   ```sh
   python3 scripts/assets/make_persona.py generate --work ~/personas/doudou \
     --describe "a round yellow 3D character with green-ringed eyes" a.jpg b.jpg c.jpg
   ```

   This draws 8 PNGs with transparent backgrounds into `<work>/poses/`:
   - `avatar`, `blink` (blinking, edited from avatar);
   - `think`;
   - `work_a` / `work_b` (two beats of the same action);
   - `wave_a` / `wave_b` (two beats of waving);
   - `sleep`.

3. **Review `poses/`**: redraw the ones you don't like with `--only think,sleep`.
4. **Package** (offline):

   ```sh
   python3 scripts/assets/make_persona.py build --work ~/personas/doudou --id doudou --name Doudou --subtitle "A round little yellow fellow"
   ```

   - The pack is in `<work>/package/doudou/`; look at `preview.png` first: frames of the five states, and colors in light and dark.
   - If positioning is off, fine-tune per pose in `<work>/framing.json`, e.g. `{"think": {"scale": 1.05, "dx": 0.02, "dy": -0.01}}` (units are fractions of the side length).
   - If colors are off, use `--primary`, `--accent-light`, `--accent-dark`.
5. **Install on the hub**: `python3 scripts/assets/make_persona.py install ~/personas/doudou/package/doudou`. It unpacks alongside first and then swaps the whole thing in; the hub machine reads `~/.config/persona/config.json` by default.
6. **Choose it on the phone**: **Settings › Appearance › Avatar**.

Each tier of a **tiered avatar** (Milk Frog) also uses these 8 poses, with all parameters in `scripts/assets/personas/nwverse.json` (each tier's description, poses, `framing`, background theme, colors):

```sh
# <work>/refs/<tier>.png: each tier's SVG from index.html in nwverse/contribution rendered as a 1024 transparent PNG (face, frog as identity references)
python3 scripts/assets/make_persona.py tiers generate --spec scripts/assets/personas/nwverse.json --work ~/personas/nwverse \
  --model gpt-6-astra --proxy http://127.0.0.1:7897      # all 7 tiers drawn at once, one codex per tier, about 20 minutes
python3 scripts/assets/make_persona.py tiers build --spec scripts/assets/personas/nwverse.json --work ~/personas/nwverse \
  --out server/internal/hub/personas/nwverse              # package into the repo, writes <work>/review.html
```

- **A tier can have its own reference images**: a tier's `refs` replaces the spec's `refs`, and `identity` and `style` switch to wording suited to photos ("the photo is this character; draw from it",
  rather than "render a flat icon in 3D"). The Laughing and Cosmic tiers are drawn from the 3D Milk Frog reference images the user provided (requested 2026-11-04; brand logos on the reference images aren't drawn); reference images go in `<work>/refs/` and stay out of the repo.
- **review.html** is a single file (all images embedded) showing each tier's picture, five states (animated), light and dark, and colors: send it to people first, and commit after they approve.
- **Packs compiled into personad must be small**: animations use a shared 256-color palette, no 512 avatar is produced, and the whole pack stays under 5 MB (about 3 MB now), since it's downloaded on every hub update.
- Flat reference images must be drawn in 3D: the spec's `style` must say "3D, soft vinyl toy texture, keep the shape and colors", or Codex draws it flat.
- **Horizontal centering**: by default it centers on the top of the head (head horizontally in the middle), so poses with a tail or leaning to one side end up off-center overall (the tadpole was off by 13% of the circle). The spec's `"center": "weight"`
  centers horizontally on the whole character's center of mass, computing a pose and the second beat edited from it together so nothing jumps between beats.
- **Vertical centering**: by default the top of the head is placed 12% below the top of the circle, which suits "head and upper body, body cropped" compositions; characters entirely inside the circle end up too high this way (the midpoints of Frog Egg, Tadpole, and Cosmic Milk Frog are around 0.43).
  A tier's `"vertical": "ground"` stands every pose on the same ground line and centers the whole character vertically when idle (the shell doesn't jump up when the egg ducks inside); `"weight"` puts each pose's center of mass at the circle's center height (the head doesn't jump when the tadpole flicks its tail).
- **Each tier's motion**: a tier's `motion` picks one of `MOTIONS` (`breathe`, `rock`, `peek`, `swim`, `hop`, `shake`, `float`); if omitted it's `breathe` (other avatar packs are unchanged).

Generate and package in one step: `make_persona.py new --id doudou --name Doudou a.jpg b.jpg c.jpg`. You can skip AI too: put your own 8 transparent PNGs with the names above into `<work>/poses/` and run `build` directly. Only `avatar` is required; missing poses fall back to it.

On first run the script installs Pillow and numpy into `~/.cache/persona/persona-venv`.
## Generating images with Codex (measured 2026-10-31, codex-cli 0.153)

- **Image tool**: the built-in `image_gen` tool in `codex exec` (feature `image_generation`, stable).
  - It uses Codex's own ChatGPT sign-in, not `OPENAI_API_KEY`, and doesn't touch other gateways' keys.
  - Generated images land in `$CODEX_HOME/generated_images/<thread>/`. The prompt tells it to copy each image into the working directory right after generating it, saved by name.
- **Transparent background**: asking for "fully transparent background" in the prompt gets real RGBA, 1254×1254, about half the pixels transparent, no watermark. The script checks the transparency ratio.
- **Consistency**:
  - All reference images are passed with `-i`. `-i` takes multiple values, so the prompt must come from stdin.
  - The blink, the second beat of the action, and the second beat of the wave are edits of the first image, not fresh drawings, so the two frames line up.
  - All 8 are drawn in one `exec`, so earlier images are in context.
- **Model**: a ChatGPT sign-in can only use some models; the model in the user's config reports "not supported when using Codex with a ChatGPT account". Switch `--model` to one that works (`gpt-6-astra` did at the time).
- **Network**:
  - Must be able to reach chatgpt.com.
  - Chat requests honor a `socks5h://` proxy, but the HTTP client used by `image_gen` and model list refresh doesn't, reporting "connection failed: error sending request".
  - Provide an HTTP CONNECT proxy: `--proxy http://host:port`.
- **Time**: about 2–3 minutes per image, around twenty minutes for 8; several avatars can run at once (2026-10-31: four in parallel, 32 images in 6 minutes).
  - If it's much slower (over ten minutes per image, or everything stalls when several run in parallel), measure the tunnel's throughput first: `curl -x <proxy> -o /dev/null -w '%{speed_download}' <large file>`.
    Each image is about 1.6 MB, and every request also carries the reference images and context; this happens when egress is only a dozen or so KB/s (on 2026-10-31 the hub machine's internet access was down to 11 KB/s for a while).
    Use `ssh -D` on another machine with fast internet that can reach chatgpt.com; the CONNECT proxy doesn't need to change.

## Tests

| Test | Covers |
| --- | --- |
| `server/internal/hub/persona_test.go` | Only complete packs are listed; rev changes with content; files don't escape the pack directory; nothing without a token |
| `server/internal/hub/persona_tiers_test.go` | Built-in packs first, a directory with the same id wins, a broken directory doesn't block built-in packs; tier changes from the board (case-insensitive, levels above the top take the highest tier, 304, keep on board failure, back to default when not on the board) with broadcast; not signed in means default tier; last tier kept across restarts; an incomplete tier gives the default tier; `tiers` rules; every pack built into personad passes validation |
| `apple/PersonaTests/PersonaTests.swift` | Parsing persona.json; packs the hub would reject are rejected by the App too; colors pulled to the floor; as the theme color **Match Avatar**, uses the avatar's primary, bubble, and Lark link colors (Lark style too), and the style's own for anything missing; downloading whole packs, swapping in by rev and deleting old ones; a failed download changes nothing; APNG frame decoding |
| `apple/PersonaTests/AvatarUploadTests.swift` | New from Photo only with `look_uploads` (not on older hubs, not in Demo Mode); `source.kind` read leniently; pictures loaded upright, too small or not an image refused; the crop square inside the image, at most 1024 px, never under 64 px; JPEG for large photos, PNG for transparency; names cut by Unicode scalars; the request body (trimmed name, no blank subtitle, `choose`), one upload at a time, the new avatar downloaded and chosen here, also when a refresh was under way as the hub made it; made but not downloaded: Try Again downloads it without a second upload; the hub's refusals shown as worded; deleting the chosen one brings the default back, the list read again after a delete, a refused delete changes nothing, 404 counts as deleted |
| `apple/PersonaUITests/AvatarUploadUITests.swift` | Starts `MOCK_LOOK_UPLOADS=demo python3 scripts/mock/mock_hub.py` (`PERSONA_MOCK_HUB`): real touches on the crop sheet (a drag and a flick move the picture, the sheet does not scroll), Upload closes it with the new avatar chosen; the delete buttons only on uploaded cards (not on Mochi or the hub's packages); the corner button is 44 pt at the smallest text sizes; the corner button of the avatar made asks about that one, and deleting it takes away that one only (the demo upload stays) and brings the default back; a long press on Mochi opens no menu, New from Photo held opens its own (Photos, Files), a tap opens Photos. With `PERSONA_MOCK_HUB_NO_FILES` (a second mock with `MOCK_LOOK_UPLOAD_FILES=fail`): made but not downloaded, the sheet stays with the error and Try Again, Cancel closes it with the avatar listed but not chosen |
| `apple/PersonaUITests/PersonaPickerTests.swift` | Starts `MOCK_PERSONAS=demo python3 scripts/mock/mock_hub.py`: choosing an avatar from the hub in Settings changes the top bar to its name; choosing Persona again changes it back |
| `apple/PersonaTests` (`tierManifestWithAbout`, `decodesPersonas`), Android `PersonaManifestTest` / `RealtimeContractTest`, `web/src/lib/persona.test.ts` | A tier's manifest and `about`; the `personas` event; the web keeps the old tier until all the new tier's files have arrived |

Seeing Milk Frog in the mock hub: `MOCK_PERSONAS=builtin` (or `demo,builtin`) lists the packs built into personad, `MOCK_PERSONA_TIER=<tier>` pins the tier,
and `GET /mock/persona-tier?tier=<tier>` changes tier at runtime and pushes `personas` (to watch the App change faces without opening Settings).

DEBUG launch arguments for screenshots and recordings:

- `-openPersona <id>`: select this avatar at launch (the pack must already be on the phone, or come down with the list).
- `-showPersonaState idle|thinking|working|needs_you|offline`: pin the shell's top-left avatar to one state.
- `-showAvatarCrop <image path>` (with `-tab settings -openAppearance persona`; `-showAvatarCropName <name>` fills the name): the New from Photo crop sheet with that image. `-showAvatarDelete <id>`: the confirmation before deleting that uploaded avatar, as its corner button opens it. Mock: `MOCK_LOOK_UPLOADS=demo` (one uploaded avatar, `u-demo0001`), `MOCK_LOOK_UPLOADS=off` (a hub without them), `MOCK_LOOK_UPLOAD_FILES=fail` (avatars it makes are listed but their files answer 503: the sheet's Try Again).

Following the CLAUDE.md convention, launch argument names start with `open…` / `show…` and are added to the list in `release.sh`.
- `-tab assistant -openAssistantPage avatars`: every avatar's picture and five states, once in light and once in dark.
