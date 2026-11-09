# Mac: records

Moved unchanged from the [Mac guide](../../apple/mac.md), which keeps the feature checklist itself.
These are how the Mac acceptance checklist was reviewed (2026-11-01 to 2026-11-03, #403) and the roles and checklist-writing rules used for its real-window checks, with the baseline run that validated them.

## Acceptance checklist: review history

All reviewed on 2026-11-01: five parallel groups checked offscreen screenshots (demo and `scripts/mock/mock_hub.py` fake data, light/dark) and unit tests (`PersonaMacTests`), fixing what they could; a reviewer then tried to disprove every check, unchecking disproved items and stating what was and was not observed.
After review: 106 [x], 118 [ ]. Each unchecked item explains why: "check at the Mac" means unlock and click/type/look, "not done" means unimplemented, "not on Mac" means intentionally unavailable.
From the evening of 2026-11-02, real-window checks continued on Mac mini (#403): isolated acceptance copy (separate bundle id + `CFFIXED_USER_HOME`) + demo / `scripts/mock/mock_hub.py`, cua-driver clicking/typing/viewing real windows, plus offscreen captures and mock request logs. New checks explain real-window observations, partial ones name remaining work. Now 151 [x], 71 [ ].
Awaiting your confirmation: the `-showSampleScreen` Terminal Is Asking sample previously contained names resembling internal tools/services; commit "chore: the sample terminal dialog uses made-up names only" replaced all with invented names; old screenshots may retain old text.

### Roles for real-window acceptance (since 2026-11-03)

Two roles:

- **Development session** (coding Agent):
  - Read issue, change code;
  - Prepare tested window: build, launch isolated acceptance copy and mock hub;
  - Write acceptance checklist;
  - Review executor Fail / Uncertain items from returned screenshots, and if needed inspect personally after its run ends.
- **Executor** (another cheaper model): operate via cua, capture, compare against expectations, return per-item report and evidence.
  - Invoked through the machine's configured headless-Agent channel in an independent context; development reads only its report.
  - Channel, commands and parameters are machine-local in `CLAUDE.local.md`.

Why: about 70% of #403's overnight tokens went to cua operation, screenshots and reading images, accumulating in the growing development context and reread every step.

**While executor runs**:
- Development does not touch UI; only one Agent uses cua at a time;
- Run batches in background: about 5 items took 12–14 minutes (2026-11-03); split larger sets and run sequentially within one background command.

**When development inspects UI**, conserve tokens too:
- Read text via accessibility tree with `query`;
- View resized/cropped screenshots;
- Use offscreen capture when possible, not real windows.

**Writing acceptance checklists**: the executor sees only the checklist, not development context.
- Supply tested pid and window_id.
- Boundaries: only this window and its menus; never click wallpaper (triggers Show Desktop); no foreground delivery unless an item requests it; no file changes.
- Observable expectations: which button/text appears, never "looks normal".
- State conditions for conditional items: avatar-menu Interrupt appears only while working, Connection Details only when not online.
- If accessibility and screenshot disagree, visible screenshot wins.
- Save one screenshot proving each item's conclusion at that moment (menu open, state changed).
- One report line per item: number, Pass / Fail / Uncertain, evidence. Append immediately to a report file after each item: the channel outputs only at completion, so timeout killing loses everything (2026-11-03 batch hung on the last item for 15 minutes, losing two prior results).
- Per-item timeout (e.g. 4 minutes); report Uncertain from current evidence and continue.
- Fixed scrolling amounts (up 3 times, specified distance), not "scroll until item appears": executor may spend minutes searching back and forth.
- Sheet text input must specify foreground delivery: background accessibility insertion displays text without triggering App search (see `docs/lessons/testing.md`).
  Also specify how to locate sheet fields (same lesson); omission once cost 8 minutes stuck on a sheet.
- Context menus: first `bring_to_front` the copy, then foreground right-click. Foreground delivery otherwise restores the prior App and closes the menu, leading to "no context menu" (false terminal-menu failure 2026-11-03).
- Describe subtle controls and locations: terminal Close is the centered handle atop the sheet; omission led to clicking Window Size at top-left and reporting no close.
- Do not use sheet-screenshot pixel coordinates (2026-11-04): macOS 26 SwiftUI `SheetPresentationWindow` bounds differ from cua captures
  (main-window area scaled into the image); derived screen coordinates miss controls, falsely suggesting broken buttons (#1132).
  Specify the element under `AXSheet` in the main window accessibility tree: AXPress if supported; for real mouse, read frame and click center in screen coordinates (`scope:"desktop"`).
- Gesture-based sheet controls (double-click modifier lock, single/double quick commands) need real mouse: accessibility double_click is two AXPress calls, interpreted as two slow clicks;
  get element frame under `AXSheet` and click screen-coordinate center with `scope:"desktop"` (`count:2` for double-click; two Uncertain reports from this on 2026-11-04).
- Terminal scrollback tests use non-takeover terminal (mock only `MOCK_TERMINAL_STREAM=1`): takeover intentionally sends wheel to the program (`terminal_input.scroll`),
  which the mock logs without scrolling, causing "cannot scroll back" (2026-11-03 s5c).

**Baseline before switching roles** (2026-11-03): executor repeated 3 known results: suggestion pills (Pass), message context action bar (Fail, #501), avatar menu (Pass).
- First run: 1 matched, 2 Uncertain, all evidence correct.
  - Action bar: accessibility listed Copy / Select / Quote / Share, but they were not visible.
  - Avatar menu: checklist incorrectly made conditional items mandatory.
- Revised using the guidance above; second run matched all 3. Executor touched no other windows.
