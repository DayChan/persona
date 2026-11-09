# Terminal: records

Moved unchanged from the [terminal guide](../../integrations/terminal.md), which now keeps the design and the conclusions.
These are the measurements behind it, from the 2026-10-29 spike to the Full view work (2026-11-05); headings name the guide section each came from.

## Section 10: streaming latency and frame intervals (2026-10-29, lab pane w9:p5, Claude 2.1.312)

| Item | Value | Method |
| --- | --- | --- |
| Echo (key sent → frame containing it arrives) | Median 37 ms, fastest about 20 ms, occasional 150–175 ms (two rounds of 12) | Locally through production hub: after `POST /keys` (keep-alive), wait for a frame containing the character in the WebSocket terminal stream. Route: local → hub → node (local) → herdr → observe → hub → local. Phone adds its network round trip and one rendered frame |
| herdr itself | 45–48 ms | tab4: `pane send-text`, then poll `pane read` (CLI starts a process each time, slower than streaming) |
| Streaming frame rate | Median interval 113 ms, p95 239 ms, about 9 fps while outputting; whole passage including thinking pauses averages 6 fps | Ask Claude for 500 characters, record each frame arrival |
| Frame size | Median 117 bytes, maximum 18 KB (full screen) | Same |
| Phone frame processing (parse + wrap + draw) | Median 11.8 ms, p95 16 ms, first full frame 44 ms | `TerminalFrameCostTests`, simulator Debug, 76 recorded frames |
| Rows visible with keyboard | About 20 (27 without keyboard) | iPhone 17 Pro simulator, 13pt |

### Breaking down streaming frame intervals (added same day)

The sources of the 113 ms median interval, measured segment by segment:

| Segment | Result | Method |
| --- | --- | --- |
| Claude Code's own redraw cadence | About one frame per 105–125 ms while streaming. Almost every frame changes only spinner/timer; body redraws in several large full-screen batches (about 20 KB, over 800 Chinese characters per frame) | Local observe, inspect frame content |
| herdr observe | One frame per 16–19 ms when the screen changes quickly (about 60 fps), not the bottleneck; no tunable flags except `--cols/--rows` | Print a line every 10 ms in a shell pane, time local observe |
| node → hub (SSH) | About 3 ms one-way (`ssh -T` round trip: median 5.6 ms; one 20 KB line: median 12.5 ms) | Local → hub → local through `ssh -T`, same clock |
| Local network to hub | RTT about 4 ms | ping |
| herdr frame → client receipt, end to end | One-way median 24 ms, p90 78 ms; same frame count (67 vs 67), no drops or accumulation | Receive local observe and hub WebSocket simultaneously, pair in order |

The 24 ms breakdown:
- Frame-phase difference between two independent observers: 0–16 ms, an artifact of the measurement; the real path has only the node observer.
- SSH about 3 ms, network about 2 ms.
- Remaining few milliseconds: hub JSON processing and WebSocket.

Conclusion: 113 ms is Claude Code's own redraw cadence. Node, hub and WebSocket neither accumulate nor throttle frames, so Claude can't be tuned to one frame per 30 ms.
The tunable end is fast-changing panes (Codex-like 60 fps TUIs, long logs run through `!`, vim paging): herdr emits every 16 ms,
previously forwarded unchanged, exceeding the phone's roughly 12 ms per-frame processing capacity. The node now coalesces with `termMinGap = 30 ms`:
- First frame after quiet is sent immediately (typing echo doesn't wait).
- Subsequent frames within 30 ms merge: concatenate delta bytes; a full frame discards preceding deltas. Send at the end of 30 ms.
- Flush accumulated frames when the stream ends.
- No extra frames while idle. Claude's roughly 110 ms cadence is unaffected.

Retest after deploying #48 (same day after 16:20, lab pane w9:p5):
- Echo: four-round medians 44 / 35 / 30 / 36 ms, matching before deployment (37 ms); the first frame after quiet isn't delayed by coalescing. The 44 ms round was immediately after node restart.
- Claude streaming: direct herdr median 110 ms, hub median 112 ms. 71 frames became 69 at the client; two pairs arriving within 30 ms merged, with the same cadence as before.
- In Claude, `!` running a loop printing every 5 ms: median intervals 101 / 107 ms. Claude redraws command output at its own ~100 ms cadence too, not herdr's 60 fps.
- Thus none of the available sessions (all Claude) really saturated the coalescing window. `TestFrameGateMergesBurstsOnly` guarantees correctness;
  with an Agent redrawing tens of frames per second (Codex-like), recheck using the measurement method under `/tmp` (local observe versus hub WebSocket).

## Section 11: phone control (local test hub → local node → lab pane w9:p5, before and after deployment)

| Item | Result |
| --- | --- |
| Acquire control (subscription → `active`) | 25 ms (node control startup to first frame about 70 ms, included in watch) |
| Echo (key sent → frame containing it) | After control, WebSocket median 24 ms, slowest 37 ms; same route with `POST /keys` median 23 ms, slowest 122 ms. No local network; remeasure after deployment through production hub |
| Direct node stdin write → frame | 13–23 ms (herdr's 16 ms frames are the floor) |
| Restoration | Leaving tab immediate; App background about 5 s; killing App about 2 s; phone disappears without closing connection 27 s (hub ping timeout); hub `kill -9` 0.1 s; node `kill -9` immediate |
| Busy / takeover | Occupied: `busy`, mirrored view; after `takeover`, old client exits, pane becomes phone-sized |

After deploying c6bec7b, retest through production hub (same day after 10:50, local test machine's lab pane w9:p5, two rounds each, 12 trials per round):

| Item | Result |
| --- | --- |
| Acquire control (subscription → `active`) | 75 / 95 ms |
| Echo, typing through WebSocket under control | Median 46 / 40 ms, slowest 80 / 83 ms |
| Echo, `POST /keys` (keep-alive) | Median 44 / 47 ms, slowest 157 / 147 ms |

Similar medians (mostly herdr's 16 ms cadence and hub round trip), but the slow tail halves: typing no longer queues behind separate HTTP requests and herdr processes.
Local-to-hub is LAN with persistent connections; on phone cellular links with requests requeued, the difference should be larger. Measure on a real device after TestFlight.

herdr versions: all six machines (one 0.9.5, five 0.9.3) have `terminal session control` with identical `--takeover`, `--cols`, `--rows` flags.
On 0.9.5, control at original pane size (no resize or occupied-slot takeover) gives full frame, same-size resize gives full frame, stdin close returns `detached`, matching 0.9.3.
Full resize/restoration/busy/takeover tests ran on local 0.9.3. Without `session control`, the node still observes and returns `control: false`;
App receives `unsupported` and only mirrors (`TestTerminalControlOnAnOldNodeOnlyWatches`).

## Section 18: none of herdr's three streams carries program OSC 52 or color queries

In a new temporary tab (`herdr tab create --no-focus`, `tab close` afterward; no existing panes touched), herdr server environment had `SSH_CONNECTION`.
Ran `printf '\033]52;c;<base64>\007'` and a small query/response script in the pane, receiving bytes through all three paths:

| Stream | Program OSC 52 | Queries such as `OSC 10;?` / `CSI ? 996 n` |
| --- | --- | --- |
| `session observe` (with `--cols/--rows`) | No (only OSC 8 appears) | No |
| `session control --takeover` (Persona control) | No | No |
| `terminal attach <terminal_id> --takeover` (read in PTY) | No | No |

All three are VT **redraws** of herdr's pane grid, not raw program output. Internally herdr (libghostty-vt based) handles program OSC 52 through its own clipboard writes
(`OSC 52 clipboard write` queue in `src/pane.rs`, `write_osc52_bytes` in `src/selection.rs`), sending only to full herdr TUI clients (`herdr` / `herdr session attach`, like Moshi),
not pane-level streams.

Not verified: whether full TUI client (`herdr session attach`) always carries OSC 52. Attaching to the user's session adds a client and could affect layout/focus, so not done.
**(Update 2026-11-05: verified in our own temporary session; it does, see section 24.)**

## Section 24: two full clients on one session (herdr 0.9.5, our temporary `persona-fullui-test` session, default untouched; `session stop` + `session delete` afterward)

Python `pty` started two `herdr --session persona-fullui-test` clients (A simulating computer 120×34, B browser). Layout from `herdr pane layout`, screens inspected with pyte:

| Case | Pane layout area (`area` in `pane layout`) |
| --- | --- |
| No client | 120×40 (last size retained) |
| A (120×34) connects | 94×33 (terminal width −26 sidebar, height −1 status row) |
| A + B (80×24) connects | 54×23 (**B's size**) |
| B resizes to 100×30 | 74×29 |
| A resizes to 140×40 | 114×39 (**A's size**) |
| B resizes to 60×20 | 60×18 (below 64 columns uses herdr mobile single-column layout, no sidebar) |
| B exits | 114×39 (A's size restored) |
| A types after B | A's size; B types again and B's size returns |

- **Most recently active client wins** (connect, resize, typing), not smallest/largest. Layout and program-visible dimensions are session-wide—**with browser Full view connected, Claude on the computer reflows at browser size until a computer keypress switches it back**;
  alternating input alternates size. Same class of cost as Single pane control (section 11), explained in first-use hint; default was therefore user-decided, not chosen by us.
- Computer window A only received 55 bytes in the second after B connected; its drawn pane geometry didn't change (pyte observation; pyte isn't full terminal emulation, so only establishes no full-screen redraw was observed) until its next activity. We didn't measure disrupted computer-window layout, only changed dimensions seen by the pane program.
- **Focus (current space/tab) is session-wide**: `tab focus` makes both A/B jump; B opens displaying current tab. Initial focus on the requested pane therefore requires changing shared focus (Initial Focus above).
- **OSC 52**: mouse selection in herdr (SGR press/drag/release) emits `ESC ] 52 ; c ; <base64> BEL`; program-issued OSC 52 also appears unchanged in client output.
  Without a display environment (or nonexistent `WAYLAND_DISPLAY`), this machine emits OSC 52 with or without `SSH_CONNECTION`. Behavior with display/clipboard tools wasn't measured (avoid writing user's real clipboard), so `SSH_CONNECTION` remains set.
  **Real herdr → browser clipboard**: `scripts/test/web_herdr_full_live.py` passes (fake hub `MOCK_TERMINAL_FULL_EXEC` runs real `herdr session attach` in PTY; Chrome drag-selects `LIVE_COPY_MARK`, clipboard reads `LIVE_COPY_MARK`).
- **Startup terminal sequences** (`TERM=xterm-256color`): `?1049h` alternate screen, `>7u` and `?u` kitty keyboard, DA1, `?1016$p`, `?1000/1002/1003/1015/1006h` mouse, `?2004h` bracketed paste, `?1004h` focus events, `?2031h`, `OSC 10;? / 11;?`, `CSI ? 996 n`, `CSI 16 t`, `?2026h` synchronized output.
- herdr `[ui] mobile_width_threshold = 64`: ≤64 columns uses single-column layout, so phone Full view (390px, about 46 columns) has no sidebar, only pane and tab bar.
- `sidebar_collapsed_mode` is herdr **shared config**; Moshi Hide collapsed Herdr sidebar changes it (`POST /gateway/v1/pty?mux=herdr&hideSidebar=…`, warns "Changes Herdr's shared config"). Persona doesn't change user herdr config; collapse sidebar with herdr prefix+b.

## Section 27: character width and CJK alignment (2026-11-05, #2751)

Environment: temporary herdr 0.9.5 session (`herdr --session <name>`), two side-by-side panes; left `cat` displays a bordered table with Chinese, fullwidth punctuation, symbols, Japanese, Korean and emoji. Fake hub `MOCK_TERMINAL_FULL_EXEC="herdr session attach <name>"` forwards real client bytes to headless Chrome 156, JetBrainsMono Nerd Font. `session stop` + `session delete` afterward.

1. **herdr positions the cursor before each wide glyph**: captured `herdr session attach` bytes show `开` as `ESC[6;32H开`, next `ESC[6;34H关` (Chinese example glyphs); after emoji also `ESC[14;38H`. Thus xterm width decisions **don't shift buffer columns** (all `│` columns compared per row: 25 / 26 / 27 / 56 / 83 / 100 / 101 / 174, matching herdr). Misalignment is rendering, not buffer state.
2. **DOM borders really misrender in fullwidth-punctuation rows**. Range measurements for each `│` (cells, minus buffer column):

   | Row | Content | Borders at columns 56 / 83 / 100 / 174 |
   | --- | --- | --- |
   | ASCII, ordinary Chinese, Japanese, Korean, symbols | `Ctrl+B 是 herdr 的前缀键` ("Ctrl+B is herdr's prefix key")… | +0.04 … +0.27 (same every row, single-cell rounding) |
   | `「开关右侧面板」（快捷键）` ("Toggle right panel (shortcut)") | Fullwidth brackets | **−2.84 / −3.81 / −3.78 / −3.67** (rightward drift) |
   | `、：；！？ 逗号顿号` (fullwidth punctuation, "comma and enumeration comma") | Fullwidth punctuation | **−0.85 / −0.84 / −0.82 / −0.70** |

   Per character: `「` `」` `）` `，` each occupy **2.97 cells** (should be 2); `（` `：` 1.97; Chinese ideographs 2.00.
3. **Cause: browser `text-spacing-trim: normal` (Chrome 123+, default)** compresses fullwidth punctuation beside punctuation or at line ends to half width. xterm DOM adds `letter-spacing = cell width × cell count − measured glyph width`, measuring 32 repeats in a hidden element (`WidthCache._measure`): repeated `「「「「…` compresses each to ~1 cell (8.03px), leading to +7.57px spacing. Real `「` beside ideographs isn't compressed, so full 2 cells + spacing becomes 2.97. Context-dependent compression causes jagged borders and accumulated error based on punctuation count. Setting `.xterm` to `text-spacing-trim: space-all` restores those rows to +0.04 … +0.27, matching ASCII.
4. **Not font fallback**: `cjk.ts` adjusts system CJK font to exactly two cells; ideographs without adjacent punctuation consistently occupy 2.00. Resetting `term.options.fontFamily` (clearing width cache) doesn't change drift.
5. **xterm and herdr width tables differ, causing dirty cells rather than shifted columns**. Scanned 8266 characters (Latin-1, Hangul Jamo, all 2000–2BFF, CJK symbols/kana/compatibility, first 512 PUA, FE10–FE6F, FF00–FFEF, assigned 1F000–1FAFF), plus 30+ sequences including ZWJ family, flags, variation selectors, accented letters. herdr widths inferred from following `a` column distance (5268 narrow, 2997 wide), compared with xterm tables:

   | xterm width table | Differences from herdr (8266 characters) |
   | --- | --- |
   | Unicode 6 (xterm default) | 1283 (mostly herdr 2 cells, xterm 1: 😀 🚀 ✅ ⭐ ⚡ ⌚ and other emoji) |
   | Unicode 11 (`addon-unicode11`) | 160 |
   | Unicode 15 | 99 |
   | Unicode 15 + grapheme clusters (`addon-unicode-graphemes`) | 88, including 85 post-Unicode-15 additions (☰ and other trigrams, 2FFC–2FFF, newer emoji from 1FA75), plus 3 byte-reading measurement errors (`ㄷ`, `💪` redraw separately at 2 cells, matching xterm); ZWJ families, flags, `❤️` sequences all match |

   Effect: herdr assumes emoji occupy two cells and doesn't write the second; xterm uses one, leaving old contents in the next cell. Screenshots show dark background after emoji (`rgb(24, 24, 37)`, not page light background). Unicode 15 + graphemes removes it. herdr doesn't query DEC 2027 (grapheme mode), so its output remains unchanged.
6. **WebGL positions correctly, but isn't the fix here**: temporary `WebglAddon` on same page showed straight borders/normal emoji, with cell width rounded to 7px (DOM 7.8px), making text tighter; pixel positions weren't measured. CSS already fixes DOM, while WebGL changes default appearance, so default remains unchanged. Full view has no WebGL switch, none added for this issue.

## Section 28: focus synchronization runs (2026-11-05, #3021)

- **Real herdr 0.9.5, temporary session**: `PERSONA_LIVE_FULL_SESSION=persona-focus-test go test -tags herdrlive -run TestLiveFocusSync ./internal/node/` (`focus_live_test.go`): node focus watcher connected to temporary session; `herdr tab focus` from another API client → `other`; `onFocusRequest` → `persona` with requesting view id; **real full client** (`session attach` in PTY) receives `ctrl+b` then `1` → `client` with client id, first tab; no extra events for 500ms after each step. Passed; session stopped/deleted, default untouched.
- **Fake hub + browser** (`scripts/test/web_herdr_focus_e2e.py`, usage at script top): sidebar selects another session → one `herdr_focus` within ~160ms, no `herdr_open` / `herdr_close`, same xterm element, echo doesn't navigate. `/mock/herdr_focus` simulates this client's focus change → Agent terminal page, sidebar highlighted/visible, no outgoing `herdr_focus`; shell → `/terminal/<machine>/pane%3A…`; external (`other`, another browser) leaves page unchanged with hint, click follows. Chat → terminal doesn't restart; computer change opens its client, previous releases; old hub without `herdr_focus` sends none. Dark/light × wide/390px screenshots.
