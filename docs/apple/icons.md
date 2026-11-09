# Icons (Apple)

Every icon in the Apple apps is a named meaning from the design system, drawn by a design-system view that applies the
current style's icon language. Pages name the meaning and set size and color; they never write an SF Symbol name and never
pick a weight or fill to match a style. The tokens live in `apple/Shared/Design` (`Icons.swift`, `IconSpec.swift`), the
views in `apple/Persona/DesignSystem/ThemedIcon.swift`.

## One meaning, one symbol

`Theme.Icon` (`apple/Shared/Design/Icons.swift`) maps each meaning to one SF Symbol: `Theme.Icon.copy` is `doc.on.doc`
everywhere, and no other meaning uses `doc.on.doc`.

- **A second name for the same meaning is an alias**, written without a literal: `static let reload = retry`. Two
  different meanings never share a symbol: muted is `mic.slash` and an ended call `phone.down`; a group chat is
  `rectangle.3.group.bubble` and a Moments post `newspaper`.
- **A filled form is its own entry** when both forms are in use (`error` / `errorFilled`, `plan` / `planFilled`).
- **Lookup tables name meanings too.** A `var symbol: String { switch … }` returns `Theme.Icon.subagent`, not `"person.2"`.
  Every dotted string that is a real SF Symbol counts, wherever it is written.
- **Another product's icon names keep their mapping.** A table that turns Lark card tokens into symbols draws them as
  Lark does and ends each line with `// icon: <why>`.
- **App Shortcuts take only a literal** (`AppShortcut(systemImageName:)` must be a compile-time constant), so
  `PersonaShortcuts.swift` writes the names with `// icon:`; they match the `Theme.Icon` entries of the same meaning.
- **Files are one table.** `FeishuDocsFileKind.symbol(ext:title:)` gives a file's symbol and hue for Feishu files and
  Lark attachments alike. Code files are `Theme.Icon.code` (`curlybraces`); `</>` (`Theme.Icon.agents`) means Agents only.

Errors use the triangle (`error`, `errorFilled`); the octagon (`critical`) is only a machine's critical alert. Copy is
`doc.on.doc`, Paste `doc.on.clipboard`, Interrupt `stop.fill`, a model or launcher `cube` (`cpu` is only a processor).

## Each style has an icon language

A style's `ComponentSpec.icons` (`IconSpec`) says how its SF Symbols look: a weight, whether filled symbols are drawn as
their outline, and a few symbols swapped for the one closer to the style's own glyphs.

| Style | Weight | Filled symbols | Substitutes |
| --- | --- | --- | --- |
| Liquid Glass, Clay, Island, Lark | the font's | as written | none |
| BeautifulUI (`IconSpec.lineGlyphs`) | `.medium` | outline | AI `sparkles` → `sparkle`; Copy `doc.on.doc` → `square.on.square` |

BeautifulUI draws its own glyphs (`BUIGlyph`) as outlines with a stroke of 2 in a 24-unit box and round caps. SF Symbols
at medium weight have the closest stroke at text sizes, and its copy and AI marks are a pair of rounded squares and one
four-point star, so the substitutes keep a screen to one drawing of each meaning.

Some filled symbols stay filled in every style because the fill is the meaning (`Theme.Icon.keepsFill`): the state dot,
play, pause and stop, dictating, a set filter, a pin, and round buttons drawn as a filled circle (send, hang up, resume).
Symbols whose name ends in `.filled` (`circle.lefthalf.filled`) are a design, not a fill state, and are never changed.

A new style that has its own icons sets `icons` in its `ComponentSpec`; nothing else changes. A style without one keeps
`.system`, so its screens stay pixel-identical when the icon language of another style changes.

## Drawing an icon

| Need | Use |
| --- | --- |
| A symbol in text, on a bare button, in a row | `ThemedIcon(Theme.Icon.x)` with `.font(Theme.Font…)` and `.foregroundStyle(…)` |
| A row's mark on a tinted tile (Settings, the assistant's suggestions and profile, Needs You) | `IconTile(Theme.Icon.x, tint:)` |
| A Settings row that opens a page / a Settings switch with a mark | `SettingsRow(title:symbol:tint:)` / `SettingsSwitchRow(title:symbol:tint:isOn:)`; never a page's own `Toggle` + `Label` |
| A component that takes a symbol name (`Chip`, `EmptyStateView`, BeautifulUI's pills, tool chips, sidebar rows) | pass `Theme.Icon.styled(Theme.Icon.x)`; the design-system components already do |
| A system control that draws its own symbol (`Label(_:systemImage:)` in a menu, a swipe action, a tab) | `Theme.Icon.x` as written |
| A brand, an iPhone app, a Linux distribution | `BrandMark`, `BrandTile`, `BrandBadge` with `Theme.Brand` (never `Image("…")`) |

`ThemedIcon` takes its size and color from the caller like `Image(systemName:)`. Where the symbol must be an `Image`
inside `Text` or as a menu item's icon (the item takes only an `Image`), use `Theme.Icon.image(Theme.Icon.x)`: the
style's symbol, with the weight of the surrounding font. Use `Image(systemName:)` directly only where it is sized by its
frame with `.resizable()`. `TokenRatchetTests` counts
those per file (`rawSymbol`), and the count only goes down.

In the BeautifulUI style every row of a Settings card starts with the same mark: a row that opens a page and a switch row
both draw `IconTile` on BeautifulUI's list row (`BUIListRow`), the switch at the end (#1650). The tile's tint follows
`SettingsRow`'s: `Theme.accent` unless the row has a meaning of its own (`StatusPalette` for an Agent state,
`Theme.brand` for Persona, `Theme.Hue.gray` for a secondary option). The other styles keep the system's switch with the
bare `ThemedIcon` in its color.

Size an icon with a font level (`Theme.Font`) when it sits in text, and with `Theme.Size` + `.scaledFrame(…)` when it
fills a box: `mark*` for marks beside text, `icon*` for row icons, `well*` for round buttons, `avatar*` for avatars and
tiles. Raw point sizes are only for pictures with a fixed layout (share cards, the Live Activity).

## Color

Icons take role colors, never hex values: `.secondary` / `.tertiary` for chrome and accessories, `Theme.accent` for what
can be tapped, `Theme.danger`, `Theme.caution`, `Theme.positive` for outcomes, `AgentState.tone` and `StatusPalette` for
Agent states (Needs you red, Working amber, Unread green), `Theme.ai` for the AI mark. A tile's glyph is white on its
tint. State colors are the same in every style.

## Avatars, state dots and app icons

- **Agent state**: `StatusDot` (sizes `Theme.Size.dot*`, the Needs you dot breathes unless Reduce Motion is on) and
  `AgentStateBadge`. A session's mark is `SessionAvatar`; an Agent's runtime is `AgentMark`.
- **People and conversations**: `ThreadAvatar`, `Monogram`, `PersonalAgentAvatar`.
- **Brand marks** sit in fixed boxes with `Theme.Brand.optical` compensation (`BrandMark`), on a white tile in
  BeautifulUI (`BrandTile`) and in a circle as a source badge (`BrandBadge`).
- **App icons**: `AppIcon`, `AppIcon-PaperLight`, `AppIcon-PaperDark` (iPhone, generated by `make_alt_icons.py`),
  `AppIcon-Vision`, and the watch's `AppIcon`. `AppGlyph` shows the home-screen variant in the app
  (`docs/apple/ios.md`, "Light & Dark").

## Extensions and other targets

The widgets, the Live Activity, the Share and Notification extensions do not compile `Theme`, so their few symbols are
literals in their own files. The watch and the Mac compile `Shared/` and use `Theme.Icon`; the Mac's own names are in
`apple/Mac/Design/MacTokens.swift` and point at `Theme.Icon`.

## Checks

- `IconVocabularyTests`: registered symbols are written by name, a symbol two files use is registered, no symbol has two
  names, retired error symbols stay retired, every brand is an image, every name is a symbol, and every style can draw
  every name (`everyStyleCanDrawEverySymbol`).
- `TokenRatchetTests`: `rawSymbol` per file only goes down.
- `scripts/app/icon_inventory.py` lists every symbol literal, how symbols are drawn (font, weight, scale, rendering mode),
  hand-drawn dots and the asset catalogs. `--json` saves a snapshot and `--diff <snapshot>` compares the working tree with
  it; the last line is a verdict with the counts.
