# Mac design language

The Mac app (`PersonaMac`) adds a layer of conventions on top of the iOS design system (`docs/apple/ios.md`, "Design language"): the same tokens, styles, and theme colors,
with rules only for **layout**, **density**, and **what to leave out** on Mac. The goal is the quiet, compact, keyboard-and-mouse-first feel of Mail, Notes, Things, and the Codex desktop app,
not an enlarged iPhone screen. The default style (Liquid Glass) and all other styles follow these layouts; each style's surfaces and colors still come from its `ComponentSpec`.

The native Mac app is frozen ([mac.md](mac.md)): these rules apply to bug fixes in its existing screens, and no new Mac design work is planned (#2214). Development mockups are not stored in the repository.

## Four principles

1. **The window title bar is the title row for every column**. The top 52pt row (`Theme.Size.macToolbarHeight`) of each column (sections, list, detail) contains that column's
   title, ‹ back button, and actions. There is no second title or second row of buttons inside the column. A pushed page puts its name in this row (`detailTitle`),
   with ‹ before the name, rather than the system's circular glass back button beside the traffic lights.
2. **Dense, not cramped**. Body text is 13pt (`Theme.Font.body`), with rows spaced on a 4pt grid. Small headings and a hairline separate groups, not large empty areas.
   The amount visible on one screen should be close to Mail, not a doubled-up iPhone layout.
3. **Content determines width**. Reading pages—documents, settings, knowledge details—have a reading width (60–80 characters per body-text line), centered in the detail column rather than filling a 1000pt window.
   Times and counts in list rows stay near the content, not at the far end of the window.
4. **Say each thing once**. A page has one empty state. Show status in one way (a dot or text, not both). Do not draw iPhone navigation cues on Mac (trailing › chevrons,
   large full-row buttons, or large round buttons floating over content).

## Components

| Component | Rule |
| --- | --- |
| Section column (sidebar) | Row metrics come from `Theme.Sidebar` (`apple/Shared/Design/SidebarMetrics.swift`, rules in `docs/apple/sidebar.md`): the system's Sidebar icon size picks 24 / 28 / 32pt rows with 14 / 16 / 20pt icons, never the text's own height, so rows are as tall in every language; rounded selection block using `Theme.Fill.macSelected` (`Theme.Sidebar.radius`); Needs you counts use a red badge, other counts use secondary gray text |
| List column | Row: status dot + title + time / one-line summary / metadata, no ›; group headings use `sectionHeader` (11pt bold, secondary color), with `Theme.Space.l` between groups; nested groups (machine › space) use indentation and font weight, not blank space |
| Detail column | Pushed page: title row is ‹ + name; reading content is width-limited and centered (60–80 characters per line; add a width token to `MacTokens.swift` on first use); actions use regular size (`controlSize(.regular)`), grouped left or right, not stretched across a row |
| Cards | Use only when a group of information needs a boundary (machine monitoring, pending questions); `Theme.Fill.well` background and `Theme.Radius.card`, no stacked shadows; cards at the same level on a page use the same treatment |
| Buttons | Title-row buttons are 28pt backgroundless icon buttons (background appears on hover, `MacTitleRowButton`); within a page, one primary action is solid and the rest are bordered; destructive actions use red text |
| Selection, hover, focus | The open item remains visible and selected in the list (not collapsed or moved because its status changed); clickable rows use `Theme.Fill.macHover` on hover; keyboard focus uses the system focus ring |
| Empty states | Empty list: one `EmptyStateView` in the list column, blank detail column with the window background, no extra "Select an item"; populated list with no selection: one subdued "Select a…" line in the detail column |
| Light and dark mode | Use only `Theme` role colors, `Theme.Fill`, and the system `labelColor` family; distinguish cards and dividers with fills in dark mode, not shadows |

## Boundary with iOS

- These rules apply only on Mac: in views under `apple/Mac/`, or in `#if os(macOS)` branches of shared views. The iOS appearance must not change by a single pixel.
- Add missing values as tokens first (Mac-only tokens go in `apple/Mac/Design/MacTokens.swift`); no literals in pages (`TokenRatchetTests` allows only reductions).
- Do not change structures whose performance has already been measured: section pages stay mounted (`MacPageStack`, #390), and the toolbar changes one frame later. Compare changes with `-showPerfRun sections` (`docs/apple/mac.md`, "Switching sections: persistent pages").

## Reference layouts

The changes this language called for, per screen. With the app frozen, the ones not made yet are not planned.

| Screen | Changes |
| --- | --- |
| Knowledge | List + detail, width-limited detail, regular-sized "Ask Persona" and "Open Agent" buttons side by side |
| Machine detail | "‹ Server" title row, no round back button beside the traffic lights |
| Agents | Tighter machine group headings, plan-card summary aligned with its title, small backgroundless icons in the title row |
| Settings | The open subpage remains selected in the list, with its detail title in the title row |
| To-dos | Only one empty state in the list column, with a normal clickable "+" |

How they were made: actual window screenshots (demo mode) as base images, redrawn to these rules through an OpenAI-compatible image-editing API (`/v1/images/edits`, model `gpt-image-2.5-sunburst`).
Prompts must specify **exactly which parts to change** ("split the content into a 340pt list column and a detail column", "left-align the summary with the title"). With style-only instructions ("more native, more polished"),
both `gpt-image-2` and `sunburst` return almost the original image. Mockups set a direction, not a pixel specification: tokens determine the values.
