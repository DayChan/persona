# Persona design system

Snapshot commit `738ba824c`, date 2026-11-10. All line numbers refer to that commit and will drift. Verify before relying on one.

**Before you add or change UI or an interaction** on any client, read the principles below and the parts of this document your change touches, and run the "UI change self-check" at the end. The PR description names the rules it followed and gives a reason for every deviation (`CLAUDE.md`, "Design spec for UI changes").

## Product design principles

Five principles decide product questions before any visual rule applies. Each one comes with a check that a reviewer can run on a PR. Apple's Human Interface Guidelines are a source for this document, not its authority: what Persona takes from them is rewritten here as Persona rules (sections 2 to 6), and `docs/design/HIG_AUDIT.md` records where the code still falls short.

### Principle 1: Defaults first

The common tasks work with no setup, and the default behavior is stable and can be explained in one sentence. Persona is built for everyday people, not only for developers.

- A new install, with only a hub paired, can do the frequent tasks: chat with Persona, start an Agent, answer an Agent's question, approve a plan or tool.
- A default never changes silently between versions. If one has to change, the release says so and the old behavior stays reachable.
- Words a non-developer would not know (hub, node, runtime, launcher, MCP) stay off primary screens and out of error messages, or come with a plain explanation (section 5).

Check: the feature works on a fresh install with untouched settings, and the PR can state its default behavior in one plain sentence.

### Principle 2: Progressive disclosure

Advanced features and options meant for expert choices start hidden or collapsed, and the user turns them on in settings. The primary screen shows only what most people need.

- New advanced options default to off and live in Settings (or behind a "More" or advanced group), not on the main chat or Agents screens.
- Detail opens on request: tool execution stays one summary line until opened (super rule 2, section 4.7), and new visual styles are opt-in choices next to the default Liquid Glass style.
- Expert terms and raw values (model IDs, ports, paths) appear only inside the advanced area that needs them.

Check: with the feature switched off, the primary screens look and behave exactly as before the PR.

### Principle 3: User control

Every setting that changes behavior can be seen, changed and undone by the user. Persona may change its own settings through an Agent, but it says what will change, asks before applying, and keeps a way back.

- Each behavior-changing setting is visible in Settings with its current value. No hidden flags whose only switch is a chat command.
- Reversible actions offer Undo (a toast with Undo, section 4.4). Irreversible ones ask first, with the destructive button styled destructive and Cancel available.
- When Persona or an Agent changes a setting, it first states the effect in plain words (what changes, for which conversations or devices, starting when), waits for the user's confirmation, records the change, and offers a rollback. The model profile switch is the reference pattern: list with a rollback preview, switch, roll back (`docs/assistant/model-profiles.md`).

Check: for each new setting, the PR names where the user sees it, how they change it back, and, if an Agent can change it, the confirmation step and the rollback path.

### Principle 4: Consistency first

Reuse the interaction patterns and components that already exist before inventing one. A new pattern needs a written reason.

- Pages are built from the design-system components (section 3) and tokens (section 2). Never assemble a lookalike of an existing component in a page.
- The same action looks and behaves the same on every screen and, as far as each platform allows, on iOS, Android and web (section 7).
- A new component or interaction pattern goes into the design system with a short note in this document saying why the existing ones did not fit.

Check: the PR lists the components and patterns it reused; anything new carries its reason and a matching update to this document.

### Principle 5: Model and product are separate

Speed, model choice, how Persona presents itself and which features are on are four separate settings. A change to one must not quietly change another.

- Model choice and speed belong to model settings and model profiles (`docs/assistant/model-profiles.md`). Product features never hard-code a model or depend on one model's quirks.
- The avatar, theme color and interface style are three independent choices (`docs/apple/persona.md`). The avatar does not switch models, and switching models does not change the avatar or the UI.
- Feature switches decide what Persona can do, not which model does it. Turning a feature on does not change the model, and switching profiles does not turn features on or off.

Check: the PR names which of the four dimensions it touches, and switching any other dimension leaves its behavior unchanged.

## 1. Purpose, scope and sources of truth

This is the project-level design specification. It describes what the code uses today. Apple (iOS first, then watchOS, macOS, visionOS) is the reference implementation. Web and Android are checked against it. Where the code and an older doc disagree, the code wins and the disagreement is listed in section 8.

Scope: tokens, components, interaction patterns, content, dark mode, Dynamic Type and the accessibility baseline. Out of scope: the frozen Mac layout (see `docs/apple/mac.md`) and server-rendered copy beyond the voice rules in section 5. The compliance review against Apple's Human Interface Guidelines is in `docs/design/HIG_AUDIT.md`.

### 1.1 Where each thing lives

| Concern | Apple | Web | Android |
| --- | --- | --- | --- |
| Tokens | `apple/Shared/Design/Theme.swift`, `apple/Shared/Design/ColorSpec.swift`, `apple/Shared/Design/StyleSpec.swift`, `apple/Shared/Design/Palette.swift`, `apple/Shared/StatusPalette.swift` | `web/src/styles.css`, `web/src/ui/look.ts` | `android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt` (generated), hand-written siblings in the same folder |
| Components | `apple/Persona/DesignSystem/` | `web/src/ui/atoms.tsx`, `web/src/ui/` | the same Android folder |
| Cross-platform token source | none (Swift is read-only for the generator) | generated tokens | `spec/tokens/README.md`, `scripts/assets/gen_tokens.py` |
| Written rules | `docs/apple/ios.md`, `docs/apple/icons.md`, `docs/apple/shell.md`, `docs/apple/sidebar.md` | `docs/web/web.md` | `docs/android/android-design.md` |

Relationship to other docs:

- `CLAUDE.md` ("Design system (iOS)") is the short rule list that agents follow. This document is the longer reference behind it.
- `docs/apple/ios.md` ("Design language") holds the Apple values. Where it differs from this document, this document reflects the code (see DS-01, DS-02, DS-03).
- `docs/apple/icons.md` covers the icon vocabulary and sizing.
- `docs/android/android-design.md` covers Android styles, glass levels and page-writing rules.
- `docs/web/web.md` covers web layout and conventions.
- `docs/SUPER_RULES.md` holds the three product rules cited in section 4. Changes there need administrator approval.

### 1.2 Rules

1. UI code uses only tokens: font level, `Theme.Space`, `Theme.Radius`, `Theme.Size` with `scaledFrame`, `Theme.Motion`, role colors, `Theme.Icon`. A plain `frame` is for lines of 4 pt or less.
2. Never restyle an existing screen directly. A new look is a new `AppStyle` or `ThemeColor`.
3. Button text never wraps (decided 2026-11-10). A button label stays on one line at every text size. When a label would be truncated, work through these in order: (a) shorten the copy, in every language; (b) ask whether a button is the right control here, since long or user-supplied text is content and belongs in a row, a list or the body, and a set of actions that doesn't fit goes into a menu; (c) at accessibility text sizes (`dynamicTypeSize.isAccessibilitySize`) stack the whole button group vertically, primary action first, each label still on one line. `minimumScaleFactor` is a last resort for small overflows such as long translations, never the fix for a label that is too long. HIG-003 and HIG-012 in `docs/design/HIG_AUDIT.md` are tracked against this rule.
4. Use `SecretField`, not `SecureField`, for tokens and keys.
5. Never write `glassEffect` or `.glass` directly. Go through `.surface` and `.themedButton`.
6. Docs are English only. User-visible strings go through the string catalogs (section 5.5).

### 1.3 Enforcement today

| Test or script | What it guards |
| --- | --- |
| `apple/PersonaTests/TokenRatchetTests.swift` with `apple/PersonaTests/TokenBaseline.json` | Per-file counts of hard-coded padding, HStack spacing, corner radius, hex color, animation, level weight, fixed frame and raw SF Symbol. Counts may only go down. Baseline: 339 hits in 64 files, 46% of them hex colors in terminal, Lark and OS-badge palettes. |
| `apple/PersonaTests/TypeScaleTests.swift` | No system font styles or sizes outside `Theme.Font`. |
| `apple/PersonaTests/IconVocabularyTests.swift` | A registered symbol must be written as its `Theme.Icon` name. |
| `apple/PersonaTests/ContinuousMotionTests.swift` | Every unending animation lists where Reduce Motion stops it. |
| `StyleSpecTests` (Apple) | Contrast floors for every style and theme color. |
| `web/src/ui/theme-contrast.test.ts` | AA 4.5:1 for text tokens on seven backgrounds, both schemes. |
| `scripts/app/android_token_baseline.py` | Hard-coded counts in `android/feature` only. It does not scan the design system module. |

Not enforced anywhere: hit-area size, accessibility labels, shadows (inline literals), `.font(.system(size:))` in some Lark and Feishu views, numeric `listRowInsets`, `Spacer(minLength:)` and `lineWidth`.

## 2. Design tokens

Colors are written light/dark. iOS sizes are at the default text size.

### 2.1 Color

#### Roles (Apple)

| Token | Value | Source | Web | Android |
| --- | --- | --- | --- | --- |
| `accent` | from the theme color; blue `#007AFF`/`#0A84FF` | `apple/Shared/Design/Theme.swift:104` | `--accent` `#0285ff`/`#3d9aff` (differs; preset "blue" matches Apple) | Glass accent same as Apple |
| `danger` | `#D93A3A`/`#FF5F57` | `apple/Shared/Design/Theme.swift:136` | `--red` `#bd3038`/`#ff9499` | same as Apple |
| `positive` | `#248A3D`/`#30D158` | `apple/Shared/Design/Theme.swift:137` | `--green` `#107638`/`#6dd697` | same as Apple |
| `caution` / `cautionText` | `#F08A00`/`#FF9F0A`; `#B35400`/`#FF9F0A` | `apple/Shared/Design/Theme.swift:138` | `--orange` `#a64c00`/`#eea66d` (one token) | same as Apple |
| `ai` | `#5B4FD6`/`#9C94FF` | `apple/Shared/Design/Theme.swift:140` | `--ai` same | same |
| `brand` | coral `#D4683F`/`#E8835C`; persona marks only, never tappable things | `apple/Shared/Design/Theme.swift:110` | none | `brand` is the accent (differs) |
| `countBadge` | `#636366` both modes, white text at 4.5:1 | `apple/Shared/Design/Theme.swift:153` | accent fill (differs) | same as Apple |
| `onSolid` | white | `apple/Shared/Design/Theme.swift:154` | `text-white` | n/a |

`persona` is the persona's own primary (falls back to the accent). `pageBackground`, `card` and `cardStroke` come from the active `StyleSpec`.

#### Agent state colors

Defined once in `apple/Shared/StatusPalette.swift:19-36` and shared by the app, watch and Live Activity. State colors never follow style.

| State | Dot | Text | Notes |
| --- | --- | --- | --- |
| Needs you | system blue `#007AFF`/`#0A84FF` | `#0064C8`/`#409CFF` | blue since #6231 |
| Working | `#E8890C`/`#FFB224` | `#A35A00`/`#FFB224` | amber |
| Unread / awaiting reply | `#30A46C`/`#4CC38A` | `#1D7A4C`/`#4CC38A` | green |
| Waiting in the background | working dot at 0.6 | secondary | clock mark |
| Idle / Offline | secondary at 0.55 / 0.3 | secondary | gray |

Web uses amber for Needs you (`web/src/styles.css:233`). Android's generated token is red but its live value is blue. See DS-01.

#### Fills, diff, hues, shell neutrals

| Group | Values | Source |
| --- | --- | --- |
| `Fill` | `well` quaternary system fill, `chip` tertiary, `pressed` secondary, `scrim` black 0.45 dark / 0.2 light | `apple/Shared/Design/Theme.swift:229-240` |
| `Diff` | add `#1D7A4C`/`#4CC38A`, del `#C62B33`/`#FF6369`, changed `#8A6A00`/`#E3B341` | `apple/Shared/Design/Theme.swift:170-176` |
| `Hue` (categories only) | blue, orange, green, purple, indigo, pink, gray | `apple/Shared/Design/Theme.swift:812-822` |
| `Shell` (same in every style) | composer `#FFFFFF`/`#1F1F21`, stroke `#E3E3E3`/`#3A3A3C`, ink `#0D0D0D`/`#F2F2F2`, segment `#E6E6E6`/`#3A3A3C`, drawer `#F9F9F9`/`#171717` | `apple/Shared/Design/Theme.swift:840-873` |
| `ChatBox` | user `#E3EDFD`/`#1C2F47`, persona `#F0F0F3`/`#2A2A2E` | `apple/Shared/Design/ChatLayoutTokens.swift:18-23` |
| `UsageColor` | input, output, cache write, cache read; color-blind checked; same on web | `apple/Shared/Design/UsageColor.swift:8-15` |
| `Syntax` | eight colors, each 4.5:1 on `codeBackground` | `apple/Shared/Design/Theme.swift:888` |

Android mirrors `Shell`, `ChatBox` and the roles hex for hex (`android/core/designsystem/src/main/kotlin/persona/core/designsystem/HomeTokens.kt`).

#### Theme colors and palettes

`ThemeColor` (`apple/Shared/Design/ColorSpec.swift:96-147`) has style, persona, blue, coral, lavender, teal, lark and extension. Web ports the five presets hex for hex in `web/src/ui/look.ts:21-27`.

| Preset | Accent light/dark | Bubble |
| --- | --- | --- |
| Blue | `#007AFF`/`#0A84FF` | `#DCEAFF`/`#1D3557`, primary text |
| Coral | `#D4683F`/`#CD6E45` | `#B24C25`/`#AF4C28`, white text |
| Lavender | `#7A5AF8`/`#8979EB` | `#6759D3`/`#6756D9`, white text |
| Teal | `#27747F`/`#2A9798` | ink-based |
| Lark | `#3370FF`/`#4C88FF` | `#D1E3FF`/`#133063` |

Readability floors (`apple/Shared/Design/PersonaPalette.swift:27`): white on accent 3.5:1, link 3.0:1, bubble text 4.5:1. A persona or extension color that misses a floor is darkened or lightened automatically. Known exception: Lark dark, 3.35:1 (`spec/tokens/known-failures.json`). Web enforces 4.5:1 instead and uses a darker `--accent-ink` for filled controls.

#### Dark and light

Every role color is a light/dark pair (`Color(light:dark:)`, `apple/Shared/StatusPalette.swift:40-46`). The watch takes the dark value only. The user choice (System, Light, Dark) is applied to windows, not with `preferredColorScheme` (`apple/Persona/DesignSystem/ColorSchemeChoice.swift`). Web uses a class on the root element set before first paint (`web/index.html:31`). See section 6.1.

### 2.2 Typography

Apple levels (`apple/Shared/Design/Theme.swift:286-376`). On iOS each is a Dynamic Type text style.

| Level | iOS definition | iOS pt | Mac pt | Line height (x size) | Web equivalent |
| --- | --- | --- | --- | --- | --- |
| `display` | title, bold (`:334`) | 28 | 24 | 1.2 | none; 20-28 px literals |
| `title` | title2, semibold (`:336`) | 22 | 18 | 1.25 | none |
| `heading` | headline (`:338`) | 17 | 13 | 1.3 | 13-14 px semibold literals |
| `body` | body (`:340`) | 17 | 13 | 1.45 | `--chat-body` 15 px (16 on phones) |
| `secondary` | subheadline (`:342`) | 15 | 12 | 1.4 | `--chat-secondary` 14 px (15) |
| `caption` | footnote (`:344`) | 13 | 11 | 1.35 | 11-12.5 px literals |
| `sectionHeader` | footnote, semibold (`:346`) | 13 | 11 | n/a | 10.5 px uppercase in lists |
| `label` | caption (`:348`) | 12 | 11 | 1.3 | 11-11.5 px literals |
| `code` | footnote, monospaced (`:350`) | 13 | 11 | 1.35 | 12.5 px mono |

Notes:

- The names do not match Apple's text-style names (`caption` is footnote, `label` is Apple's caption). Keep the Persona names in code and use the table above in docs.
- Emphasis tokens (`bodyStrong`, `secondaryStrong`, `captionStrong`, `labelStrong` and so on, `:356-365`) are the only way to change weight. `TypeScaleTests` forbids `.weight(` on a level.
- Android Glass matches the sizes (`android/core/designsystem/src/main/kotlin/persona/core/designsystem/Typography.kt:90-105`) with a 12 sp floor. The generated `Tokens.Type` block is stale (DS-12).
- Web has no type tokens: about 3,000 `text-[Npx]` uses across 20 sizes, some below 11 px.
- Mac uses fixed sizes times `Font.scale`, floor 11 pt.

Dynamic Type behavior:

- The user's Small, Standard or Large setting shifts the system size by -1, 0 or +1 (`apple/Persona/DesignSystem/TextSize.swift:6-36`). Web maps the same ratios (12/13, 1, 14/13) through a build-time rewrite. Android has no in-app setting.
- Shapes scale with `scaledFrame`, capped at 1.6x (`apple/Persona/DesignSystem/TextSize.swift:98-108`). `tapTargetFrame` never goes below 44 pt.
- Named caps: `Theme.Shell.topBarLargestTypeSize` is `.xxxLarge` (`apple/Shared/Design/Theme.swift:865`); `compactSwitchLargestTypeSize` is `.large`. Other caps are literal at the call site: `xxLarge` for a glyph inside a 44 pt button, `xLarge` for badges (DS-14).
- Documented fixed-size exceptions: Dynamic Island (`apple/Shared/Design/IslandMetrics.swift`), watch face (`apple/Shared/Design/WatchFaceTokens.swift`), Lark rich content that carries sender sizes.

### 2.3 Spacing

`Theme.Space` (`apple/Shared/Design/Theme.swift:203-223`):

| Token | pt | Meaning |
| --- | --- | --- |
| `flush` | 0 | parts that touch |
| `xxs` | 2 | |
| `xs` | 4 | `lines`: between lines of one row |
| `sm` | 6 | |
| `s` | 8 | `titleToContent` |
| `m` | 12 | |
| `l` | 16 | `block`, `page` (side margin) |
| `xl` | 24 | `section` |
| `xxl` | 32 | |

`sm` (6) is smaller than `s` (8), which reads backwards (DS-17). Web has no spacing tokens (Tailwind 4 px steps; page margin 20/32 px). Android: `xl` 20, `xxl` 24, `xxxl` 32 and no `sm` (DS-05). Off-grid values in use: card padding default 14, watch card 10/12, chip vertical 2.5 (DS-09).

### 2.4 Radius

`Theme.Radius` (`apple/Shared/Design/Theme.swift:181-198`):

| Token | pt | Use | Web | Android |
| --- | --- | --- | --- | --- |
| `thumbnail` | 3 | file thumbnail | literals | missing |
| `badge` | 6 | badges, inline code | chip 6 | 6 |
| `control` | 10 | code blocks, segmented controls | control 8 | 10 |
| `card` | 14 | cards, groups | card 10 | 14 |
| `bubble` | 20 | message bubbles | 14 literal | 20 |
| `panel` | 22 | floating panels | composer 14 | 22 |
| `page` | 55 | page pushed aside as a card | none | 55 |
| `appIcon(side)` | side x 0.2237 | home-screen squircle | mark 8/12 | n/a |

Outside the scale: composer 32 (`Shell.composerRadius`, `apple/Shared/Design/Theme.swift:852`), attach panel 38/60, sidebar rows 6-10, watch card 16, chart 4. Styles differ in color and surface, not in corners (only preview tiles vary). Android varies shapes per style (DS-13). The docs list five radii; the code has seven plus `appIcon` (DS-03).

### 2.5 Sizes

`Theme.Size` (`apple/Shared/Design/Theme.swift:457-570`) always goes through `scaledFrame`.

- Hit target: `tapTarget` 44 (`:507`). Same number under other names: `searchField`, `switchHeight`, `Watch.minRowHeight`, `Sidebar.row` (iOS), `voiceThreadRoom`. Android uses 48 as its platform floor (`Tokens.MinTouch`).
- Dots 6-10, marks 12-18, icons 20-26, wells 28-38, avatars 40-48. Neighbors differ by 1 to 2 pt (DS-10).
- Named by value: `box50` to `box380` (DS-10).
- Widths: `readable` 560 (web chat column 880, pages 760), sidebar 340/400/480, drawer `min(0.82 x width, 380)` (Android the same).
- Sidebar rows: `Theme.Sidebar` (`apple/Shared/Design/SidebarMetrics.swift`), 44 on iOS; 24/28/32 on Mac; web 32 (touch 44 via a hit-area expander).
- Mac-only sizes: `apple/Mac/Design/MacTokens.swift`.

### 2.6 Shadow and elevation

Apple has no shadow tokens. Shadows are inline literals:

| Place | Value | Source |
| --- | --- | --- |
| Clay surface | highlight 0.8, r6, (-4,-4); shadow 0.22, r12, (5,8); white stroke 0.45 | `apple/Persona/DesignSystem/Styling.swift:56-59` |
| Island surface | black 0.13, r20, y10 | `apple/Persona/DesignSystem/Styling.swift:66` |
| Soft button | black 0.12 (0.25 tinted), r6, y3 | `apple/Persona/DesignSystem/Styling.swift:183` |
| Composer | black 0.08, r12, y4 | `apple/Persona/DesignSystem/ShellComposerBar.swift:477` |
| Message action bar | black 0.25, r10, y4 | `apple/Persona/Views/Chat/MessageMenu.swift:461` |
| Drawer card | black 0.18, r24 | `apple/Persona/Views/Assistant/AssistantSidebar.swift:350` |

The Glass style relies on Liquid Glass and has no shadow. The vendored BeautifulUI package has real elevation tokens that Persona does not read. Web has seven shadow tokens (`web/src/styles.css:68-74`). Android has four literals. See DS-08.

### 2.7 Motion

`Theme.Motion` (`apple/Shared/Design/Theme.swift:612-722`).

| Token | Value | Use |
| --- | --- | --- |
| `state` / `standard` | snappy 0.25 s (`:613`) | a state change snaps |
| `layout` | smooth 0.3 s (`:614`) | a layout change glides |
| `expand` | smooth 0.4 s (`:620`) | |
| `reveal` / `conceal` | easeOut 0.28 s / easeIn 0.2 s (`:635`) | |
| `drawer(velocity:)` | interpolating spring 0.32 s, no bounce (`:650`) | drawer and pager |
| `springBack` | spring 0.3 s, bounce 0.25 (`:654`) | |
| `keyboard` | interpolating spring (`:662`) | |
| Needs-you breath | 1 s, scale 1.2 | the only attention loop |

The composer and attach panel own about 40 more one-off tokens (Telegram-derived). Web: 150 ms state, 220 ms glide, 300 ms collapse, one easing curve, no springs. Android live values are 160/320 ms and differ from the generated 250/300 (DS-11).

Reduce Motion:

- In a view: `accessibilityReduceMotion`. Outside a view: `Theme.Motion.reduced` (`apple/Shared/Design/Theme.swift:639`).
- Tokens do not gate themselves. Each caller must. Loops are covered by `ContinuousMotionTests`. Page transitions, the toast and press scales are not (see the audit).
- Web strips all durations to 1 ms but keeps busy spinners turning (`web/src/styles.css:296-306`). Apple stops loops.
- Haptics are not gated; iOS has no per-app switch.

### 2.8 Icons

- One SF Symbol per meaning, registered in `Theme.Icon` (`apple/Shared/Design/Icons.swift`, 350 entries). Draw through `ThemedIcon` (`apple/Persona/DesignSystem/ThemedIcon.swift`) so the style's icon language applies (weight, outline form, substitutions).
- Some states stay filled in every style (`keepsFill`, `apple/Shared/Design/IconSpec.swift`).
- Beside text, size an icon with a font level. In a box, use `Size.mark*`, `icon*`, `well*` or `avatar*` with `scaledFrame`. Details in `docs/apple/icons.md`.
- Raw symbol names still appear in about 87 baselined places (settings rows, Lark, notification actions, controls).
- Web draws about 80 stroke glyphs (`web/src/ui/icons.tsx`, 15 px, stroke 1.9). Android uses Phosphor. There is no shared glyph map.

### 2.9 Styles and theme colors

| Style | Surface | Buttons | Canvas | Icons |
| --- | --- | --- | --- | --- |
| `glass` (default) | glass | glass | system | system |
| `clay` | puffy | soft | wash | system |
| `island` | floating | soft | islands | system |
| `lark` | hairline | bordered | neutral | system |
| `beautiful` | glass | glass | neutral | line glyphs |

Source: `apple/Shared/Design/Theme.swift:11-39`, `apple/Shared/Design/StyleSpec.swift`. Style and theme color are independent axes; `StyleSpecTests` checks every pair. New code never switches on `AppStyle`. It reads the composed spec. Web has one look (the Beautiful style). Android has Glass, Brief and Board, so only Glass is common to both platforms.

## 3. Components

For every component: use the design-system one, not a lookalike. Counts are approximate uses at the snapshot. "Platforms" lists web and Android equivalents.

### 3.1 Composer

- Use: the one message input for Assistant and Agents chats on iPhone, iPad and Mac.
- Anatomy: collapsed line `[+] field [mic] [voice/stop]`; expanded card with the field on top and a row `[+] ... [model/effort] [mic] [send]`. Both shapes share one text field so focus and IME text survive the change.
- Tokens: radius 32, min field height 44, 3 to 8 lines, 22% of viewport (`apple/Shared/Design/Theme.swift:852-857`), fill and stroke from `Shell`.
- States: expanded when focused, non-empty or holding attachments; send hidden when empty and replaced by voice or stop; send gate button when the main conversation is not ready.
- Accessibility: every control labelled; hold-to-talk has a hint and a tap action (`apple/Persona/DesignSystem/ComposerHoldMic.swift:105-110`). Reduce Motion read in the bar.
- Source: `apple/Persona/DesignSystem/ShellComposerBar.swift:11`, `apple/Persona/DesignSystem/ComposerButtons.swift`.
- Platforms: web composer (14 px radius, 28 px buttons, `web/src/views/chat/Composer.tsx:648`); Android `ComposerFrame` (radius 32 in Glass).
- Duplicates: four composer implementations exist (DS-19).

### 3.2 Message bubbles

- User bubble: trailing, radius 20, padding 16 x 12, `ChatBox.user` fill in the unified layout, otherwise the theme's bubble gradient (`apple/Persona/DesignSystem/QuoteViews.swift:295-345`, `apple/Persona/Views/Chat/MessageViews.swift:151`).
- Assistant text: unboxed in Agents chats; boxed in `ChatBox.persona` in Assistant chats. Streaming text re-parses only the tail.
- Quotes sit above the words and jump to the original on tap.
- No shared bubble component exists. Messages, Tasks and Lark rebuild it with different paddings and gutters (DS-20).
- Web: user bubble 14 px radius; replies unboxed. Android: 20 dp in Glass, max width 86%.

### 3.3 Cards and surfaces

| Component | Use | Source |
| --- | --- | --- |
| `.surface(in:)` | floating panels: composer, strips, toasts, notices | `apple/Persona/DesignSystem/Styling.swift:8` |
| `.card()` | grouped-row card, padding default 14 | `apple/Persona/DesignSystem/Components.swift:429` |
| `NoticeBanner` | persistent in-page notice, neutral, caution or danger, one action | `apple/Persona/DesignSystem/Notice.swift:7` |
| `ReadinessCard` | what the main conversation still needs | `apple/Persona/DesignSystem/ReadinessCard.swift:8` |

`.card()` uses the system grouped background, not the style's card color, so it does not follow Clay, Island or Lark (DS-07). Inset wells (`Fill.well`) are rebuilt by hand in 24+ places. Web has no shared card outside the admin app.

### 3.4 Lists and rows

- Page chrome: `.themedPage()`, `.themedGroupedPage()`, `.themedRow()` (`apple/Persona/DesignSystem/Styling.swift:13-22`). Settings uses `SettingsForm` and `SettingsRow`.
- Row height at least 44. Sidebar rows use `Theme.Sidebar`.
- Chip family: `Chip` (label), `FilterChip` (toggle), `LinkBadge`, `CountBadge`, plus 50+ lookalikes in feature code (DS-21).
- Status: `StatusDot` (fixed size 9, labelled), `AgentStateBadge`.
- Row actions: swipe actions and a context menu with the same actions. Agents rows on iOS have no swipe (context menu only, #1244).
- Web rows: 32 px desktop, 44 px touch. Android rows: min 56 dp.

### 3.5 Navigation

- Shell (compact width): Assistant and Agents as pages on a strip, a drawer sidebar, no tab bar. Top bar: persona button, `ShellSwitch`, circle button (`apple/Persona/DesignSystem/ShellComponents.swift:19,258,294`). Re-tapping the selected segment opens its menu.
- Regular width: system tab view and split navigation; the same sidebar in a column.
- Page titles: `.pageTitle`, `.detailTitle` (`apple/Persona/DesignSystem/PageTitle.swift`).
- Side rail for iPhone Duo (`apple/Persona/DesignSystem/ShellSideRail.swift:50`).
- Platforms: web phone shell matches (drawer plus top-bar switch); Android `HomeNavigation` has a 36 dp segment height versus Apple's 44 (DS-18).

### 3.6 Sheets

- System `.sheet` with detents for most things (43 detent sites). Settings is a near-full sheet with a close button.
- Link card: detents 0.6 and large, background stays interactive down to 0.6 (`apple/Persona/Views/Link/LinkSheet.swift:142-146`).
- Edit sheets: `sheetCancel` asks to discard on Mac only; on iOS it discards at once (`apple/Persona/DesignSystem/SheetActions.swift:44`).
- Busy sheets disable interactive dismissal. Detents and drag indicators are inconsistent (DS-22).
- Confirmation sheets for Close and Delete agent exist because a system dialog was too wordy (`apple/Persona/DesignSystem/Components.swift:444`).
- Web: dialog is a centered card, becomes a bottom sheet on phones. Android: `JSheet`.

### 3.7 Buttons

- One style: `.themedButton(prominent:)` (`apple/Persona/DesignSystem/Styling.swift:127`), 185 uses. Glass, bordered or soft per style. Soft buttons have a 44 pt minimum and dim to 0.45 when disabled; glass prominent dims to 0.4.
- Forms use `.formButton` and `FormAction`.
- Icon buttons use a 44 pt frame around a smaller visual (composer round button, viewer chrome).
- Destructive actions use `role: .destructive`. Red is also used for risky-but-not-destructive actions (DS-24).
- Web buttons are 32 px tall (hit area expanded on touch). Android: Large 48, Medium 40, Small 32 dp (DS-06).

### 3.8 Chips, badges, avatars, marks

`Chip` (look neutral, tint or outline), `CountBadge` (99+ cap), `LinkBadge`, `Monogram`, `AgentMark`, `BrandMark`, `SessionAvatar`, `ThreadAvatar`. Chips are text-only and read as their text. Decorative marks are hidden from VoiceOver when adjacent text names them. Sources are in `apple/Persona/DesignSystem/Components.swift` and neighbors.

### 3.9 Empty states

`EmptyStateView` (`apple/Persona/DesignSystem/Components.swift:385`): symbol, title, one-sentence message, optional prominent action. 92 uses. System `ContentUnavailableView` is used in about 22 other places and looks different (DS-15). Search results may use `ContentUnavailableView.search`. Web has no shared empty state outside admin; Android has `JEmptyState`.

### 3.10 Loading and error states

| Case | Component |
| --- | --- |
| Page load | `Loadable` and `LoadableContent` (`apple/Persona/DesignSystem/Components.swift:739-770`); only about 9 call sites |
| Row or button busy | `RowProgress`, `trailingProgress`, small `ProgressView` |
| Load failure on a settings page | `LoadProblem` (`apple/Persona/DesignSystem/SettingsForm.swift:73`), no Retry |
| Persistent notice | `NoticeBanner` |
| Transient result | `Toast` shown by `ToastCapsule` (`apple/Persona/DesignSystem/Toasts.swift:7`) |
| Chat API failure | `APIErrorCard` with details and Retry |

Toasts: error, success or info; one slot, so a new toast replaces the old. Duration 2.5 s, 4 s for errors, 5 s with an action (`apple/Persona/DesignSystem/Toasts.swift:57`). Drawn in a pass-through window and announced to VoiceOver. About 255 call sites. Loading and error shapes are inconsistent across screens (DS-15, DS-16). Web toasts are dark capsules at the bottom (2.8 / 5.2 / 6 s). Android uses Snackbar, per-feature hosts and system Toast (DS-25).

## 4. Interaction patterns

### 4.1 Navigation hierarchy (super rule 3)

`docs/SUPER_RULES.md` rule 3: Assistant and Agents are the only primary home destinations on iOS, Android and web. Settings opens from the sidebar. Everything else (Inbox, To-dos, Scheduled Tasks, Knowledge, Machines, Health, extension pages) is pushed from the drawer. Push by value for new pages; mixing view-based and value-based pushes in one stack is a known trap. An optional Messages page can be switched on and becomes a third primary destination; whether that fits rule 3 needs the rule owner (DS-26).

### 4.2 Gestures

| Gesture | Behavior |
| --- | --- |
| Right drag on Assistant root | opens the drawer, unless a pushed page, a sideways scroller or typed text is under the finger |
| Horizontal drag between pages | pager with 3x resistance past the ends; flick above 300 pt/s or past half way |
| Edge swipe back | system; the pan yields to it |
| Swipe on a message (Agents) | quote, threshold 66 pt, with a VoiceOver "Quote" action |
| Long press on a message | lifted menu with an action bar |
| Hold the mic | cancel by sliding left, lock by sliding up |
| Link card drag | to 0.6 it stops blocking the chat; further down it closes |

Every gesture-only action needs a plain alternative for VoiceOver and Switch Control (see the audit).

### 4.3 Feedback and haptics

`Haptics` (`apple/Persona/App/AppModel.swift:3228-3241`): `tap` (light impact), `success`, `error`, `attention` (warning). About 393 call sites; no-op on visionOS; generators are created per call. One direct UIKit use remains (`apple/Persona/Views/Chat/ChatEdgeScrub.swift:174`).

Suggested mapping, which the code mostly follows: tap for selection and toggles, success for copy, save and completed send, error for a failure paired with an error toast, attention for a refused or newly arrived request. Gaps: copy sometimes gives tap or nothing; plain approval buttons have no haptic; archive has no haptic; there is no documented policy (DS-23). Watch maps success, failure, notification, stop and retry consistently. Android has the same four kinds with far fewer call sites. Web has none.

### 4.4 Confirmation and undo

Severity ladder to adopt:

1. Reversible: do it and offer Undo in a toast (inbox bulk actions, remove from list, folder trash do this).
2. Destructive with one object: `confirmationDialog` with a destructive button that repeats the verb, a message that says what is lost, and Cancel.
3. Needs extra choices (keep or remove the worktree): a content-height sheet.
4. `.alert` for short prompts and text entry.

Today there are seven mechanisms and the same action can use different ones (DS-24). Archive in the sidebar, to-do swipe delete and mail-account delete have no confirmation and no Undo. Only the Mac discards edits with a prompt.

### 4.5 Push, notifications and reminders

- The system prompt appears right after pairing and at launch, with no in-app explanation first (`apple/Persona/App/PushRegistrar.swift:16-33`). Requested options include time-sensitive. When denied, Settings explains and deep-links.
- Foreground: the system banner, suppressed for the visible session. No custom in-app push banner.
- Actions: Allow (needs unlock) and Deny on permissions; Approve and Send Back on plans; Reply on done (`apple/Persona/App/NotificationActions.swift:19-52`). Failures are posted as local notifications.
- Live Activity: Allow and Deny buttons; Allow requires authentication. Stale after 15 minutes.
- Deep links: `persona://` and `jarvis://` custom schemes, no associated domains.
- Reminders are agent tools and Scheduled Tasks, not a UI object.
- Wording differs per surface for the same event (section 5.4).

### 4.6 In-app links card

Links open inside the app in a card that fills the screen. Dragged down to 0.6 it stops blocking the chat. Further down it closes. Speed matters first. Implemented by `apple/Persona/Views/Link/LinkSheet.swift:111`. Header buttons need 44 pt frames (see the audit).

### 4.7 Tool execution collapsed (super rule 2)

Tool execution is one summary line, never opens by itself, shows the latest text segment while running and the result when done, and expands by hand. iOS complies: `isOpen` ignores `live` (`apple/Persona/Views/Chat/ChatLines.swift:293`) and tool rows use `autoExpand: false`. The "latest segment only" half was not confirmed from code reading and needs a screenshot test.

## 5. Content

### 5.1 Voice and tone

Calm, direct, second person, like a capable teammate. Contractions in English ("can't", "didn't"). No exclamation marks, emoji, "Oops" or "Sorry". The assistant says "I" only in its own chat surfaces (greeting, intro, sample prompts). Elsewhere the app is "Persona" and agents are named in the third person. "Please" only when asking for something burdensome. The hub's HTTP errors break these rules (uncontracted, "please" heavy).

### 5.2 Capitalization and punctuation

- Apple buttons, navigation titles, menu items and alert titles: Title Case. Descriptions, footers, empty-state lines, section headings, accessibility labels: sentence case. Web and Android: sentence case. `docs/protocol/i18n.md` and `docs/apple/sidebar.md` disagree on headings (DS-27).
- Agent states: "Needs you", "Working", "Unread", "Seen" in prose; Title Case only as a button or title.
- One ellipsis character. Use it for actions that open another step and for in-progress states.
- Complete sentences end in a period; labels and titles do not. Settings paths use the single angle quote.
- Apostrophes and quotes: pick one style (Apple en is 94% straight apostrophes with curly quotes; DS-28).

### 5.3 Error-message structure

1. What happened: "Couldn't + verb + object". Use "Can't" only for something not possible, not a temporary failure.
2. What is safe: "What you've typed is kept."
3. One next step: "Check the connection and try again."

No HTTP codes, key algorithms, CLI flags or raw error text in the primary line. Do not append raw detail after a colon without a plain lead. One generic fallback: "Something went wrong. Try again."

### 5.4 Buttons, confirmations, notifications

- Verb first, one to three words. "Retry" everywhere (not "Try Again"). "Deny" for permissions.
- Delete destroys data; Remove detaches from a list; Discard throws away unsaved edits; Forget removes a pairing or credential; Clear is bulk reset.
- Confirmation: title "Delete this chat?", body states the loss and whether it can be undone.
- Notifications: title names the actor and the need ("{name} needs your permission", "{name} has a question", "{name} finished"); body is a short call to action. One wording per event on every surface, kept in the hub catalog. Never include secrets or commands.
- Permission purpose strings: "Persona does X when you do Y, for example Z." One or two sentences, no CLI text, correct device name, all three languages.

### 5.5 Glossary and localization

| Term | Rule |
| --- | --- |
| Hub | product term; "the hub machine" for the machine |
| Computer | the user's own machine; "Machine" only for list and monitoring entries |
| Node | never in user-facing English |
| Agent | lowercase mid-sentence; "Agents" for the tab |
| Persona | the product and the main assistant |
| Lark | in en and ja, except where Feishu is a separate option |
| sign in / sign-in | verb / noun |

Full glossary: `docs/protocol/i18n.md`. Languages: en, zh-Hans, ja. Apple strings live in `apple/Shared/Localization/Localizable.xcstrings`; hub strings in the server locale files. Keys with a bracketed suffix are translator-context tags; the English value strips them. Japanese uses no space between Japanese and Latin text (the catalogs do not follow this yet; DS-29). Do not concatenate sentences.

## 6. Dark mode, Dynamic Type, accessibility baseline

### 6.1 Dark mode

- Every color is a light/dark pair; tokens are dynamic colors.
- The Light & Dark choice sets the interface style on every window, so sheets, alerts, the keyboard and web views follow. Windows created later copy it (toast window, drawer host).
- Intentionally dark: watch, Live Activity island, image editor, VNC screen, image viewer chrome. Code colors are the same in both modes.
- Add a view-level `colorScheme` read only for drawn content that a dynamic color cannot express.
- Web: class on the root, set before first paint, plus a `color-scheme` meta.

### 6.2 Dynamic Type

- Text uses `Theme.Font` levels only. Shapes use `scaledFrame`.
- Rows with name plus chips stack at accessibility sizes (`NameWithChips`). Use `firstFittingText` when several candidate labels exist.
- Caps are allowed for glyphs in fixed-size buttons and for top bars that must fit a small iPhone. They must be named, and the control must keep a 44 pt hit area.
- Never shrink to hide truncation of body text. For button labels `minimumScaleFactor` is a last resort only; follow rule 3 in section 1.2 (shorten the copy, reconsider the control, stack the group at accessibility sizes).

### 6.3 Accessibility baseline (the contract every component meets)

1. Hit area at least 44 x 44 pt (Android 48 dp), including icon buttons whose visual is smaller. Use `tapTargetFrame`. This is Persona's rule and is stricter than Apple's: the HIG gives 44 x 44 pt as the iOS default and 28 x 28 pt as the absolute minimum. Persona uses the default as its floor.
2. Every control has a spoken label; icon-only buttons never rely on the symbol name. Decorative marks are hidden when adjacent text says the same.
3. Selected, expanded and busy state is exposed with traits or values, not only color or shape.
4. State never rides on color alone: pair with text or a symbol.
5. A gesture-only action has an accessibility action or visible control.
6. Animations: transitions and press effects respect Reduce Motion; every loop is listed in `ContinuousMotionTests`.
7. Text contrast at least 4.5:1 (3:1 for text of 18 pt or larger, bold text, and non-text marks), verified for each style and theme color. Composite cases (text on a tinted fill) need a test.
8. Layouts survive the largest accessibility size without clipping.
9. Transient messages are announced to VoiceOver.
10. Headers carry the header trait; modal sheets trap focus.

Current gaps against this contract are listed in `docs/design/HIG_AUDIT.md`. No test enforces items 1, 2, 7 (composites) or 8.

## 7. Platform consistency matrix

| Topic | Apple | Web | Android | Verdict |
| --- | --- | --- | --- | --- |
| Styles | 5 | 1 | 3 (Glass shared) | different sets |
| Accent presets | 5 | same hex | same set | consistent |
| Roles (danger, positive, ai) | reference | different hex | same hex | web differs |
| Needs-you color | blue | amber | blue live, red generated | inconsistent (DS-01) |
| Type scale | Dynamic Type, 28/22/17/17/15/13/12 | px literals | sizes match, no in-app size setting | web has no tokens |
| Spacing | 0-32 with `sm` 6 | Tailwind default | `xl` 20, `xxl` 24 | Android differs (DS-05) |
| Radius | 6/10/14/20/22 | 6/8/10 plus literals | 6/10/14/20/22, per-style variants | web and Android differ |
| Min target | 44 | 44 hit area, 28-32 visuals | 48 floor, 32-40 controls | violated on Android and web visuals |
| Motion | token set | 150/220/300 ms | 160/320 ms live | different |
| Reduce Motion | environment value | CSS strip, busy exempt | animator scale only | different policy |
| Toast | capsule, top | dark capsule, bottom | Snackbar, 3 mechanisms | different |
| Haptics | 4 kinds, 393 sites | none | 4 kinds, 35 sites | thin on Android |
| Icons | SF Symbols | custom strokes | Phosphor | no shared map |
| Text size setting | Small/Standard/Large | same ratios | none | missing on Android |
| Tokens source | hand-written | hand-written | generated from stale JSON | drift is silent |

## 8. Inconsistencies and duplicate definitions

| ID | What | Where | Impact | Recommended unification |
| --- | --- | --- | --- | --- |
| DS-01 | Needs-you color: docs say red; code is blue on Apple; amber on web; Android generated token red, live value blue | `docs/apple/ios.md:124` (records red as kept at the user's request on 2026-11-06), `docs/apple/ios.md:920`, `docs/apple/icons.md:81`, `docs/protocol/agent-states.md:10`, `docs/web/web.md:1429`; `apple/Shared/StatusPalette.swift:20`; `web/src/styles.css:233`; `android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt` (generated needs-you) and `android/core/designsystem/src/main/kotlin/persona/core/designsystem/StatusPalette.kt` (hard-coded blue) | Users see different colors for the same state on three platforms; docs mislead | Product owner decides once (code today: Apple blue; ios.md:124 records a user request for red), fix the JSON and regenerate Android, fix the four docs, change web or record it as a deliberate exception |
| DS-02 | Weights documented as bold; code is semibold | `docs/apple/ios.md` (heading, sectionHeader) vs `apple/Shared/Design/Theme.swift:338`, `:346` | Doc misleads new styles | Fix the doc |
| DS-03 | Docs list five radii; code has seven plus `appIcon` | `apple/Shared/Design/Theme.swift:181-198` | `thumbnail` and `page` undocumented | Document all, or fold `thumbnail` into `badge` |
| DS-04 | Duplicate hex values under different names (`#1D7A4C`/`#4CC38A` in Diff.add, Hue.green, unread text; `#2E6FD0`/`#5B9BFF` in loop and Hue.blue; `#B35400`/`#FF9F0A` in cautionText and Hue.orange; system blue literal written twice) | `apple/Shared/Design/Theme.swift:151,171,173,813-815`; `apple/Shared/StatusPalette.swift:24`; `apple/Shared/Design/Styles/Glass.swift:23`; `apple/Shared/Design/Palette.swift:8` | A change in one silently misses the others | Alias to one source; make the blue accent read `Palette.systemBlue` |
| DS-05 | Space scale: same names, different numbers on Android; shared JSON describes the wrong source | `apple/Shared/Design/Theme.swift:203-213`; `android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt:244-253`; `spec/tokens/dimension.json` | Cross-platform layout drifts; 84 Android call sites affected | Adopt the Apple scale and migrate; add a drift check against the Swift file |
| DS-06 | Minimum target: Android Small/Medium buttons 32/40 dp, chips 36, switches 26-30, segment 36 against a 48 floor; web visuals 28 px | `android/core/designsystem/src/main/kotlin/persona/core/designsystem/Controls.kt:100`, `:307-309`, `:462`; `android/core/designsystem/src/main/kotlin/persona/core/designsystem/HomeTokens.kt:39`; `web/src/ui/atoms.tsx:106` | Fails the baseline on two platforms | Inflate hit area to the floor in the component |
| DS-07 | Two rounded-container primitives with different color sources | `apple/Persona/DesignSystem/Styling.swift:8` (style card color) vs `apple/Persona/DesignSystem/Components.swift:429-438` (system grouped color) | Clay, Island, Lark cards do not match neighboring surfaces | Make `.card()` read the style's card color; add an "inset well" component for `Fill.well` blocks |
| DS-08 | No shadow or elevation tokens; shadows are inline literals in six places; BeautifulUI elevation unused | section 2.6 | Inconsistent depth; bypasses the ratchet | Add `Theme.Shadow` from these literals |
| DS-09 | Off-grid magic numbers: card padding 14, chip padding 2.5, watch 10/12, toast and disabled opacity | `apple/Persona/DesignSystem/Components.swift:222,429`; `apple/Shared/Design/Theme.swift:431-451` | Cannot retune globally | Move to tokens; add an opacity scale |
| DS-10 | Overlapping size steps 1 pt apart; value-named boxes; 44 under six names | `apple/Shared/Design/Theme.swift:457-570` | Token choice is arbitrary | Merge neighbors; rename `boxNN` by meaning; alias the 44s to `tapTarget` |
| DS-11 | Two motion sets: curve and spring repeated under several names; Android live motion differs from generated; web has one curve | `apple/Shared/Design/Theme.swift:662-707`; `android/core/designsystem/src/main/kotlin/persona/core/designsystem/PersonaTheme.kt` | Different feel across platforms | Share one curve token; pick one Android set |
| DS-12 | Android `Tokens.Type` is stale and partly unused | `android/core/designsystem/src/main/kotlin/persona/core/designsystem/Tokens.kt` (Type block) vs `android/core/designsystem/src/main/kotlin/persona/core/designsystem/Typography.kt:90-105` | Misleading source of truth | Regenerate from `Theme.Font` |
| DS-13 | Radius philosophy: fixed on Apple, per-style on Android, literals on web (about 480) | `apple/Shared/Design/Theme.swift:178-180`; `android/core/designsystem/src/main/kotlin/persona/core/designsystem/PersonaTheme.kt:54-62`; `web/src/ui/atoms.tsx:973` | Same screen looks different | State the rule; replace web literals with tokens |
| DS-14 | Dynamic Type caps are literal sizes at 36 sites; only two are named | section 6.2 | Inconsistent behavior at large sizes | Name roles (glyph in button, badge, top bar) |
| DS-15 | Empty and loading states in several shapes: `EmptyStateView` vs `ContentUnavailableView` (22), `LoadableContent` (about 9 uses) vs 262 bare `ProgressView`, plain text | `apple/Persona/DesignSystem/Components.swift:385,739`; `apple/Persona/Views/SplitNavigation.swift:216` | Inconsistent look; BeautifulUI never applies to system views | Route empty and failed states through `EmptyStateView` with an error factory and Retry |
| DS-16 | Load errors in four shapes: `LoadProblem` (no Retry), inline red label in caption, label or secondary font, toast | `apple/Persona/DesignSystem/SettingsForm.swift:73`; `apple/Persona/Views/Knowledge/KnowledgeView.swift:84`; `apple/Persona/Recording/RecordingsSection.swift:40` | Different size, color and recoverability | One error component with optional Retry |
| DS-17 | Naming: `sm` (6) below `s` (8); font levels reuse Apple style names with shifted meanings; three suffix schemes in `Size` | `apple/Shared/Design/Theme.swift:206-209` | Easy to pick the wrong token | Document the mapping (section 2.2); consider renaming on a future token pass |
| DS-18 | Same control, different size by platform: shell switch 44 on Apple, 36 dp on Android; search field 44 vs web 32/36 | `apple/Shared/Design/Theme.swift:861`; `android/core/designsystem/src/main/kotlin/persona/core/designsystem/HomeTokens.kt:39`; `web/src/ui/SearchField.tsx:45` | Cross-platform parity | Align to the hit-area floor |
| DS-19 | Four composer implementations plus a page editor; send/stop styling and radius (32 vs 22 vs 20) differ | `apple/Persona/DesignSystem/ShellComposerBar.swift:11`; `apple/Persona/Views/Chat/ComposerParts.swift:7`; `apple/Persona/Views/Chat/PromptComposer.swift:11`; `apple/Persona/Views/Assistant/AssistantView.swift:1023` | Maintenance cost; dead branches on iOS | Make `ShellComposerBar` the only iOS composer; reduce the others to skins or delete |
| DS-20 | Bubble variants: padding 16 x 12 in chat vs 12 x 8 in Messages; gutters 48, 40, 32; strokes 0.08, 0.1, 0.22 | `apple/Persona/DesignSystem/QuoteViews.swift:305`; `apple/Persona/Views/Messages/MessagesViews.swift:420`; `apple/Persona/Views/Chat/MessageViews.swift:178,324` | Visual drift | One bubble component with tokens |
| DS-21 | Chip, filter-chip and button lookalikes hand-built in feature code (about 109 capsule backgrounds, 50+ view types); a second pale user-blue (`ChatBox.user` vs blue bubble) on both Apple and Android | `apple/Persona/Views/Inbox/InboxView.swift:770,1004`; `apple/Persona/Views/Inbox/InboxActions.swift:64`; `apple/Shared/Design/ChatLayoutTokens.swift:18` | Tint, padding and states diverge | Add an accent `Chip` look and a fill-width themed button; pick one user blue |
| DS-22 | Sheet presentation: detents `[.medium, .large]`, `[.fraction(0.6), .large]`, none; drag indicator on 18 of 43 detent sites; toolbar placement mixed | `apple/Persona/Views/Link/LinkSheet.swift:142`; `apple/Persona/Views/Chats/ChatsView.swift:348` | Unpredictable dismissal | A small `themedSheet` helper with a rule per sheet kind |
| DS-23 | Haptics: no policy; copy gives success, tap or none; approval buttons silent; one direct UIKit use; `Haptics` lives in `AppModel.swift`, not the design system | `apple/Persona/App/AppModel.swift:3228`; `apple/Persona/Views/Chat/ChatEdgeScrub.swift:174` | Inconsistent feel; watch cannot reuse | Move to the design system, write the mapping in section 4.3 |
| DS-24 | Seven confirmation mechanisms; destructive role used for non-delete actions; swipe-delete without confirm or Undo | `apple/Persona/Views/Todos/TodosView.swift:125`; `apple/Persona/Views/Mail/MailViews.swift:470`; `apple/Persona/App/AppModel.swift:3063` | Data loss risk; unclear meaning of red | Adopt the ladder in section 4.4; add Undo to archive |
| DS-25 | Feedback channels: Apple one toast model; Android Snackbar plus feature hosts plus system Toast | `apple/Persona/DesignSystem/Toasts.swift:7`; `android/app/src/main/java/persona/android/ui/Root.kt:120` | Different placement and timing | One feedback layer on Android |
| DS-26 | Optional Messages page is a third primary destination against super rule 3 | `apple/Persona/Views/RootView.swift:182`; `apple/Persona/Views/Shell/ShellState.swift:23` | Rule ambiguity | Rule owner decides: opt-in extension page or amend the rule |
| DS-27 | Heading case: sidebar headings sentence case, Apple titles Title Case, with Title Case counted 1143 vs sentence 754 | `docs/protocol/i18n.md`, `docs/apple/sidebar.md` | Mixed catalog | Adopt the split in section 5.2 and normalize in one pass |
| DS-28 | Copy typography: apostrophes mixed, trailing ellipsis missing on 17 of 41 progress strings, "Retry" vs "Try Again", Stop vs End in recording | `apple/Shared/Localization/Localizable.xcstrings`; `apple/Persona/Recording/RecordingsSection.swift:161` | Polish | Apply section 5 |
| DS-29 | Localization defects: Health weight chart title uses the "Sleep" key; Mac microphone string says iPhone; Persona Link prompt is Chinese only | `apple/Persona/Views/Health/HealthTrends.swift:15`; `apple/project.yml:282`; `apple/PersonaLink/Info.plist:50` | User-visible errors | Fix strings (details in the audit) |
| DS-30 | Parallel token sets with silent drift: `ExtensionSurfaceTokens`, `IslandMetrics`, `WatchFaceTokens`, `Theme.Watch`, `WorkspaceSidebar`, `UsageColor` vs `Hue`, BeautifulUI spacing and radii | `apple/Shared/Design/ExtensionSurfaceTokens.swift`, `apple/Shared/Design/WatchFaceTokens.swift`, `apple/Shared/Design/Theme.swift:60-92` | Values copy `Theme.Space` without a link | Keep only what a target cannot compile; add a drift test for the rest |
| DS-31 | Defined but unused tokens: `Space.lines`, `Space.block`, `Size.box140`, `Size.meter`, `Attach.badgeTarget`, `Syntax.all`, `Icon.forkChat`; on Android `Tokens.System`, `Surface`, `Shape`, `Watch` | `apple/Shared/Design/Theme.swift`; `apple/Shared/Design/Icons.swift:138` | Noise | Delete or use |
| DS-32 | `SecureField` still used for secrets on iOS; `HubTag` and `StatusLabel` dead | `apple/Persona/Views/Settings/ConnectorSignInViews.swift:553`; `apple/Persona/Views/Settings/ExtensionAuthViews.swift:206`; `apple/Persona/DesignSystem/HubComponents.swift:16`; `apple/Persona/DesignSystem/Components.swift:348` | Breaks a written rule; dead code | Use `SecretField`; delete the dead views |
| DS-33 | Pages read `AppStyle.current.usesBeautifulUI` and duplicate BeautifulUI forms (about 330 lines) | `apple/Persona/Views/Interactions/InteractionViews.swift:1323`; `apple/Persona/Views/Voice/VoiceBar.swift:135` | Two renderings to test | Move branches into design-system components |
| DS-34 | Reduce Motion not honored by navigation, toast, press and symbol effects | `apple/Persona/Views/Shell/ShellPager.swift:158`; `apple/Persona/DesignSystem/Toasts.swift:74`; `apple/Persona/DesignSystem/Styling.swift:187` | Accessibility | Gate through `Theme.Motion.reduced` |
| DS-35 | Android ratchet skips its own design system module (329 color and 1168 dp literals); doc says no literal colors | `scripts/app/android_token_baseline.py`; `docs/android/android-design.md:130` | Rule not enforced where it matters most | Extend the scan or amend the doc |

## 9. Recommended next steps

1. Fix the documentation and token sources that disagree (DS-01, DS-02, DS-03, DS-05, DS-12, DS-29). Cheap, high trust payoff. Regenerate Android tokens after correcting the JSON.
2. Close the accessibility gaps in the audit: hit areas, labels, Reduce Motion (DS-06, DS-34). Most of the P1 findings in `docs/design/HIG_AUDIT.md` are here.
3. Add the missing token families: shadow, opacity, and named Dynamic Type caps (DS-08, DS-09, DS-14).
4. Unify state components: empty, loading, error with Retry, and the confirmation ladder with Undo for archive (DS-15, DS-16, DS-24).
5. Consolidate duplicates: one composer, one bubble, accent chip and fill-width button, `.card()` on the style color (DS-07, DS-19, DS-20, DS-21).
6. Move haptics into the design system with a written mapping (DS-23).
7. Decide the cross-platform questions that need an owner: needs-you color on web, the third destination (DS-26), heading case (DS-27), per-style radii (DS-13).
8. Make drift visible: extend the Android ratchet to the design system, add a web literal ratchet, and a test that compares the Swift scales with the shared JSON (DS-30, DS-35).
9. Prune dead tokens and views, and replace the remaining `SecureField` uses (DS-31, DS-32).

## UI change self-check

Run this before opening a PR that adds or changes UI or an interaction, and paste the list into the PR with each item marked
done, not applicable, or deviated (with the reason). It is short on purpose. The sections cited hold the detail.

**Principles**

- [ ] Defaults first: works on a fresh install with untouched settings, and the default behavior fits in one plain sentence (principle 1).
- [ ] Progressive disclosure: advanced options are off by default and live in Settings or an advanced group, and the primary screens are unchanged when the feature is off (principle 2).
- [ ] User control: every new behavior-changing setting is visible, changeable and reversible. If Persona or an Agent can change it, the PR names the explanation, the confirmation and the rollback (principle 3).
- [ ] Consistency: lists the existing components and patterns reused. Anything new has its reason written down and an update to this document (principle 4).
- [ ] Model and product separate: names which of speed, model, presentation or features it touches, and changes none of the others (principle 5).

**Rules**

- [ ] Only tokens and design-system components: no literal sizes, colors, fonts, SF Symbol names, `glassEffect` or lookalike components. The token ratchets did not go up (sections 1.2, 1.3).
- [ ] Existing styles and theme colors look pixel-identical; a new look is a new `AppStyle` or `ThemeColor` (section 1.2).
- [ ] Hit areas at least 44 x 44 pt (Android 48 dp); every control has a spoken label; gesture-only actions also have a visible control or an accessibility action (section 6.3).
- [ ] Destructive actions: confirmed if irreversible, offer Undo if reversible (section 4.4).
- [ ] Motion respects Reduce Motion; new loops are listed in `apple/PersonaTests/ContinuousMotionTests.swift` (sections 2.7, 6.3).
- [ ] New copy follows section 5 and ships in `en`, `zh-Hans` and `ja`.
- [ ] Screenshots of each changed screen in light and dark, at the default and at the largest accessibility text size, are attached or linked (iOS, plus watch, Android and web when touched).
- [ ] No new problem of a kind listed in `docs/design/HIG_AUDIT.md`. A PR that fixes a listed finding names its ID.
