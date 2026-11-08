# Plan: Orca support

Status: in progress. Steps 0 and 1 are complete. The first Orca and herdr-core phases of step 2 are both written ("Orca source", "herdr-core host"), not merged. Completed steps are checked under "Implementation order" below. Once all steps are done, merge the design into
`docs/server/architecture.md` / `docs/server/server.md` and delete this plan.

## Background

Persona currently drives only Agents running in herdr. Claude Code running elsewhere (an Orca terminal, an ordinary terminal) appears as "Not running" in the app:
history is visible, status is not, and sending a message starts another `claude --resume` in herdr, with both processes writing the same session.

We want Orca integration without scattering `if orca` everywhere: first extract a "host" layer, implemented separately by herdr and Orca. Adding tmux later becomes another implementation.

## Two concepts

Use these terms consistently in this document and code:

- **Host**: where the terminal runs, such as herdr or Orca. The host lists panes, reads screens, sends input, and opens/closes panes.
- **Agent**: what runs in the pane, such as Claude Code, Codex, or an internal Codex variant. The Agent determines transcript location, session-ID recognition, state detection, and dialog appearance.

These are currently mixed: the internal Codex variant is actually "an Agent that herdr cannot recognize inside a herdr host", but was integrated with patches in herdr code paths (`node/traex.go`).
After separation, "Codex in Orca" and "an internal Codex variant in herdr" are just combinations from two tables: hosts and Agents.

## Current state: tightly coupled to herdr

- node holds `*herdr.Client` directly (`node/node.go`), and its internal session model is `map[string]herdr.Agent`, with no interface.
- Every feature calls herdr directly: snapshots and event streams (`placement.go`, `herdrevents.go`), session recognition (`setAgents`, `sessionFromProcess`),
  launch (`start` in `control.go`, `waitReady` in `claudeready.go`), sending and pre-send checks (`prompt`, `checkPane`, `ensureSubmitted`),
  mode / thinking-effort changes (`modes.go`), rename/close, and terminal observe / control / scroll (`terminal.go`).
- Pane IDs are untyped: in-memory bindings `n.panes`, hub database `node_sessions.pane`, and hook `HERDR_PANE_ID` all assume herdr panes.
- `proto.Session.SpaceKind` is reserved already, but node writes only `"herdr"`; the app decodes but never reads it, grouping only by "machine + space ID".
- Many app strings hardcode herdr (new chats, spaces, terminal "about 1000 lines", etc.).

## Host interface

Code lives in `server/internal/host` (interfaces, `PaneRef`, kind-keyed host `Registry`), with the herdr implementation in `server/internal/node/hostherdr.go`
(the only node file referencing the herdr client). Every host implements the core; optional capabilities use Go interface assertions. Methods follow actual node call sites,
and each herdr implementation method wraps one herdr call (`Close` first counts panes in the tab, then closes).

```go
type Host interface {
    Kind() string                                        // "herdr" | "orca"
    Version(ctx) string
    Snapshot(ctx) (*Layout, error)                       // spaces, slots, agent panes, every pane
    Agents(ctx) ([]Agent, error)                         // agent panes only (herdr without snapshot)
    Agent(ctx, PaneRef) (*Agent, error)                  // one pane's agent + state
    Read(ctx, PaneRef, ReadOpts) (string, error)         // visible | recent | recent-unwrapped, plain | ANSI
    SendText(ctx, PaneRef, text string) error
    SendKeys(ctx, PaneRef, keys ...string) error         // Persona-wide key names; each host translates
    Prompt(ctx, PaneRef, text string) error              // host's own submit; ErrNoAgent → caller types + enter
    PaneProcesses(ctx, PaneRef) ([]int, error)           // foreground pids, claude first
    PaneEnv() string                                     // env var naming the pane: "HERDR_PANE_ID" / "ORCA_PANE_KEY"
}

// Optional capabilities.
type Watcher    interface { Watch(ctx, panes []PaneRef, changed func()) error }
type Launcher   interface { Spaces(ctx) ([]Space, error); CreateSpace(ctx, cwd, label) (*Created, error)
                            CreateSlot(ctx, space, cwd, label) (*Created, error); Run(ctx, PaneRef, cmd) error
                            NameAgent(ctx, PaneRef, name) error }
type Organizer  interface { RenameSpace(ctx, space, name) error; RenameSlot(ctx, slot, name) error
                            Close(ctx, PaneRef, space, slot string) error } // the whole slot when it is the last pane
type Streamer   interface { CanObserve() bool; PaneSize(ctx, PaneRef) (cols, rows int, err error)
                            Observe(ctx, PaneRef, cols, rows int, fn func(Frame)) error }
type Controller interface { CanControl() bool
                            Control(ctx, PaneRef, cols, rows int, takeover bool, fn func(Frame)) (Control, error) }
```

- **Pane reference `PaneRef`**: host kind + host-local ID. Stored and transmitted as `<kind>:<id>`; herdr panes have no prefix (herdr IDs already contain colons,
  such as `w1:p2`). Prefixes not in `host.Kinds` (fixed known kinds: herdr, orca, herdr-core, botmux, independent of local registrations) are treated as herdr. A known kind with no local host reports "no <kind> host on this machine" on lookup, never sends to herdr, so hub database, `proto.Session`, and hook pane values need no migration.
  node always stores this string (`n.panes`, `Agent.PaneID`), and `Registry.Pane` resolves host and ID at call time.
- **Session model**: node uses `map[string]host.Agent` internally (same fields as the old herdr Agent; `PaneID` is a `PaneRef` string). Panes in host-returned
  `Agent` and `Created` already use that host's `PaneRef`.
- **Key names**: retain the app's current herdr notation (`shift+tab`, `ctrl+c`, `enter`, etc.) as Persona-wide names. herdr forwards unchanged;
  Orca converts to bytes (for example `shift+tab` → `\x1b[Z`).
- **Errors**: hosts annotate their errors with `ErrNoAgent` / `ErrAttachBusy` / `ErrTakenOver` using `host.Mark` (tested with `errors.Is`), preserving error text. Missing host capabilities return `ErrUnsupported` from node, with text beginning `unsupported` (hub uses this prefix to fall back to screen reads).
- **Layout `Layout`**: spaces, slots, recognized Agent panes, and all panes. When known, hosts also supply Agent type, state, and session ID (herdr-integrated `agent_session`).
- **Still herdr-specific in step 1**: new sessions, space renaming, and tab renames recorded in `starts` use the default host (herdr), since space IDs do not yet carry host kind
  (step 3). Event streams subscribe only to herdr (`watchHerdr`, one `herdrEvents`); `node.info` gets `herdr_version` from the herdr host version.
  Listing takes a snapshot from each host and combines them, retaining a host's last Agents if it does not respond; only total failure counts as failure.

## Shared layer above hosts

These parts currently sit in herdr code paths; after extraction all hosts share them.

| Shared layer | Responsibility |
| --- | --- |
| Session recognition | Prefer a session ID supplied by the host; otherwise scan claude / codex process environments, find the pane via each host's `PaneEnv()`, then read `sessions/<pid>.json` (Codex uses thread names, the internal Codex variant uses an owner file). `personad hook` reads pane variables for every host |
| State detection | Use host-reported state when available; otherwise infer from the screen (the current approach for the internal Codex variant), retain existing corrections such as `codexTurnOver`, then let hub map to unified states (`docs/protocol/agent-states.md`) |
| Pre-send checks | If the host reads colored screens: full draft / gray-text detection and extra Enter (`checkPane`, `ensureSubmitted`); plain-text only: degrade like current Codex handling, checking dialogs only |
| Capability reporting | Each session carries supported host capabilities (live terminal, takeover, create, rename, close); the app hides unavailable buttons |

Replace node's `n.herdr` with kind-keyed hosts (`n.hosts`): listing combines their panes, and each operation dispatches by `PaneRef` kind.

Changes required in hub and app:

- Write host kind to `SpaceKind`; include host kind in app space-group keys and rename APIs (otherwise identical space IDs merge into one group and rename targets herdr).
- Hide buttons by capability; generalize herdr-specific wording, showing truly herdr-only text (such as about 1000 history lines) only for that host.
- Store prefixed `PaneRef` in `node_sessions.pane`.

## Orca source

Based on and checked against Orca 1.4.245 source.

### Connection

- Read `~/Library/Application Support/orca/orca-runtime.json` (0600): Unix socket path and `authToken` (Windows Orca writes `named-pipe` transport,
  `\\.\pipe\orca-<pid>-…`, not `unix`; see [windows.md](../operations/windows.md), "Orca"). Orca changes tokens on every launch;
  reread on connection failure or `unauthorized`. When Orca is not running, the file is absent and the host reports unavailable.
- Protocol: one JSON per line, request `{id, authToken, method, params}`, response `{id, ok, result | error}`. Slow calls may first send `{"_keepalive":true}`; skip it.
- Any process running as the same user can connect, without `ORCA_*` environment variables, including nodes started over ssh or kept running by launchd.
- This socket protocol has no public documentation, only an internal version (`RUNTIME_PROTOCOL_VERSION`, currently 3). Check capabilities with `status.get` at startup; disable if incompatible.
  Connect directly to the socket, not a new `orca` CLI process each time (starting a Node process every second while polling is too heavy).

### Phase 1 (written 2026-11-01, not merged)

Orca is this machine's third "engine" ("One engine per machine" below): engine name `orca`, display name "Orca". Code: client `server/internal/orcart`,
host `server/internal/node/hostorca.go`, fake test runtime `server/internal/orcart/orcarttest`.

- **Pane ID**: Orca pane key `<tabId>:<leafId>` (`worktree.ps` reports Agents by it; terminal process `ORCA_PANE_KEY` also contains it), stored as
  `orca:<tabId>:<leafId>`. The host resolves handles required by `terminal.*` (`term_<uuid>`) by pane key from the latest `terminal.list`.
  On expired handles (`terminal_handle_stale`), relist: reads follow normally; writes (send, keys, close) retry only if it is still the same terminal process (`incarnationId` unchanged).
  If the process changed, reject without input. Reads, writes, and closes all carry `expectedIncarnationId`; Orca rejects a handle whose process changed, preventing writes to its replacement.
- **Connection**: open a connection per call (the socket supports multiple requests per connection, but we do not need that). Before the first call and after Orca changes runtime (`runtimeId` changes), call
  `status.get`: `runtimeProtocolVersion` below 3 or `minCompatibleRuntimeClientVersion` above 3 is incompatible, all calls error,
  and `node.info` marks Orca unavailable with a `note`. Missing metadata, no local transport for the platform (macOS / Linux `unix`; Windows `named-pipe`, then `unix`), or missing/refused socket / pipe means "not running" (`host.ErrNotRunning`):
  immediately answer "No terminal", with no logs if never connected. On connection failure or `unauthorized`, reread metadata and retry once (restart changes socket and token).
  If reply `_meta.runtimeId` differs from metadata, reread next time. Orca responds to unreadable requests (bad JSON, too large) with ID `unknown`; treat as rejection of this request.
  Metadata lookup: if `$ORCA_USER_DATA_PATH` is set, use only that. Otherwise check two locations and read the most recently written when both exist (an old runtime may leave a stale file):
  desktop app directory (macOS `~/Library/Application Support/orca/`, Windows `%APPDATA%\orca\`, Linux `$XDG_CONFIG_HOME/orca/`),
  and orcad directory (remote runtime started here over SSH by Orca on another computer, platform-independent: `$ORCA_USER_DATA`, otherwise `$XDG_DATA_HOME/Orca`, otherwise `~/.orca`;
  Orca 1.4.246's orcad, #2070). If unreadable, rediscover next call so Orca can start after node.
- **Timeouts**: `terminal.list`, `worktree.ps` 3 seconds (each refresh); `status.get`, `terminal.read`, `terminal.agentStatus` 5 seconds; `terminal.send` 30 seconds
  (Agent prompts use Orca-side long polling with keepalives); `terminal.close` 10 seconds.
- **No push**: does not implement `Watcher`. Orca machines have no event stream; node polls a snapshot every second, not the slow 10-second snapshot used with herdr event streams.
  Keep it lightweight: normally omit layouts in `terminal.list` (`includeVisualLayouts:false`; Orca says this is expensive), requesting them only when the terminal set or a worktree's `topologyRevisions` changes,
  or after 30 seconds. Pass the previous `afterSnapshotId` to `worktree.ps`, which returns only `unchanged` if unchanged. Scan processes at most every 1.5 seconds,
  in two steps (`ps -o pid=,stat=,etime=,args=` to find claude / codex, then `ps -E` only for them). `Agents` after a failed snapshot reuses that attempt's result.
  Local measurement: about 0.2 seconds per refresh (10 terminals).

### Mapping

| Persona requirement | Orca implementation | Phase 1 |
| --- | --- | --- |
| List panes | `terminal.list {includeVisualLayouts, includePreview:false}` (layouts requested only under the "No push" rules above): local only (`executionHostId` is `local`; terminals on SSH targets have no node there to manage them), excluding `orphaned` | ✅ |
| Identify Agents | Requires a **foreground** claude / codex process in the pane (`ORCA_PANE_KEY` matches the pane key; fall back to `ORCA_AGENT_PANE`): not suspended (`ps` stat does not start with `T`), in the terminal foreground process group (stat contains `+`; Linux compares pgrp and tpgid in `/proc/<pid>/stat`), without `HERDR_PANE_ID` / `HC_TERMINAL_ID` (claude in herdr / herdr-core nested inside Orca inherits the outer `ORCA_PANE_KEY` but belongs to its own host). Orca's `agentIdentity` and Agent rows can remain after exit, so neither alone suffices. Only if process scanning fails does listing fall back to both `agentIdentity` and a matching Agent row with `hasLivePty` (all writes then reject) | ✅ |
| State | Agent row for this pane key in `worktree.ps` (matching type, `updatedAt` no earlier than 2 seconds before process start, or it may belong to the previous Agent): prefer `mainAgent.state` (main Agent's own state; row `state` includes children, remaining working if the main turn ended but background children run). Keep working / blocked / done, map waiting to blocked. Without a usable row, ask `terminal.agentStatus` (3-second cache): working unchanged, permission → blocked, idle → done; null `status` → unknown | ✅ Polling |
| Map pane to session | Foreground claude pid for the pane → `sessions/<pid>.json` (shared "process identifies session" path). macOS uses two `ps` calls (above), Linux reads `/proc`, Windows reads process PEB ([windows.md](../operations/windows.md), "Orca"). Extract only `ORCA_PANE_KEY` / `ORCA_AGENT_PANE` and the two nested-host exclusion variables; never retain, log, or send environments to hub (they also contain Orca's hook token) | ✅ |
| Spaces / slots | Space is worktree (`worktreeId`), named from branch (strip `refs/heads/`), falling back to path basename, ordered by `worktree.ps` (Orca sidebar order). Slots are tabs in `visualLayouts`, in layout-tree reading order (`first`, then `second` for splits), named by tab title or index if empty | ✅ |
| Read screen | `terminal.read {terminal, screen:true}`: plain text, current screen only; rejoin soft wraps using `tailRows[].wrapped`; error on `source: screen-unavailable`. Host declares `host.PlainScreen`; `recent` also returns only the current screen | ✅ Plain text, no history |
| Send message | Do not use Orca's `agentPrompt`: it bracket-pastes only to Agents it has "seen working". Newly launched claude receives raw text + `\r`, submitting a multiline message at its first newline (tested 2026-11-01). Rejections with `accepted:false` (`no-agent` / `permission`) require `requireAgentStatus:"sendable"`, which Orca always rejects alongside "text + Enter", so it is unusable here too. Follow herdr's `agent.prompt`: first verify a foreground Agent (process table has foreground claude / codex in the pane, not inside a host nested in it; `terminal.agentStatus` is asked too: permission status returns `dialog_open`, an error means not foreground; its `isRunningAgent` is not required since #5463, because Orca reports false while Claude Code has background tasks and still takes input; failed process scan means not foreground; refusals return `not_delivered` with no input and log the check that refused), then send bracketed paste ourselves: `terminal.send {text: ESC[200~ + text + ESC[201~}` (no `enter`; replace ESC in text with `<ESC>`, like Orca, to prevent premature paste termination). Wait 150 ms plus 10 ms per line, capped at 1 second; verify foreground Agent again, then send `\r` separately. Any failure after paste returns `delivered:unknown` (text may remain in input). Always pass `client: {id:"persona", type:"desktop"}` (`client.id` required; absent yields `invalid_argument`). Pre-send checks only detect dialogs (no color to distinguish drafts from gray text) | ✅ |
| Session not running | When a session is no longer an Agent pane (exited, suspended, backgrounded), herdr resumes it in a new pane before sending. Orca (and herdr-core) engines cannot create sessions, so reject with no input: if its last pane still exists, `agent_not_in_front` ("Claude is not in the foreground; it may be suspended"); if gone, `not_running` ("…Reopen the Orca session on your computer"). Previously this looked up a launcher and returned the confusing `unsupported: orca cannot do this` | ✅ |
| Type without Enter | `terminal.send {text}` without `enter`: raw bytes, no paste, with the same foreground-Agent check (not permission: free-form dialog input needs it). Orca does not declare `TextTyper` | ✅ |
| Special keys | Key names → xterm bytes: `enter` `\r`, `esc` `\x1b`, `shift+tab` `\x1b[Z`, `backspace` `\x7f`, `ctrl+<letter>` control characters, `ctrl+space` `\x00`, `alt+<character>` prefixed ESC. Modified arrows / Home / End / Insert / Delete / PageUp / PageDown / F1–F12 use xterm parameters (1 + shift 1 + alt 2 + ctrl 4, e.g. `alt+left` `\x1b[1;3D`, `ctrl+left` `\x1b[1;5D`). Send each key separately via `terminal.send`, 40 ms apart (cancel via caller ctx; concatenating `esc` with a following sequence can make an alt key). Reject unknown keys before sending any. Printable keys (letters, digits, space) first verify foreground Agent; Enter, esc, arrows, ctrl+c do not (stuck panes must remain interruptible/closable) | ✅ |
| No input delivered | Foreground check fails; `terminal.send` returns `accepted:false` (permission → `dialog_open`, others → `not_delivered`); pre-write errors such as `terminal_handle_stale` (including incarnation mismatch), `terminal_not_writable`, `terminal_gone`, `terminal_exited`, `invalid_argument`, `runtime_busy`, `request_too_large` also return `not_delivered` (all `delivered:none`, safe to resend). Uncertain-write errors such as `runtime_error` remain errors | ✅ |
| Close | Like herdr, send ctrl+c (`\x03`) twice, then `terminal.close` (Orca closes the pane, or tab if it was the last pane). An already absent terminal counts as closed | ✅ |
| Rename | Orca owns worktree and tab names: `RenameSpace` / `RenameSlot` report `unsupported` ("Orca worktrees and tabs cannot be renamed from the phone yet") | ❌ |
| Recognize hook pane | With no `HERDR_PANE_ID` or `HC_TERMINAL_ID`, `personad hook` reads `ORCA_PANE_KEY` (fallback `ORCA_AGENT_PANE`) and reports `orca:<tabId>:<leafId>`. node records these only on Orca-engine machines (herdr machines continue ignoring Orca panes). claude launched in Orca usually lacks Persona hooks (only Persona's own launches add them through `--settings`) | ✅ |
| New sessions, mode / thinking-effort changes | Implements `host.Launcher`; `terminal.create` supports creation in registered worktrees or their subdirectories and attempts to register unregistered Git repositories automatically; mode menu needs further expansion | ✅ New sessions supported |
| Live terminal, colored screen reads, takeover dimensions | Only over paired encrypted WebSocket; hub falls back to screen reads | Phase 3 |
| Push changes | Not on the socket (all `*.subscribe` methods are streaming) | — |

Two options for phase 3: propose an Orca "colored screen read" method callable on the socket (preferred), or implement Orca pairing and encrypted WebSocket in Persona.

### Findings

- **When state rows disappear**: after an Agent exits, Orca removes its row from `worktree.ps` (retaining only recovery rows, which `worktree.ps` does not list). But `agentIdentity`
  and row removal need not synchronize with the process, so Agent identification is process-based.
- **`terminal.send` rejection is not an error**: it returns `{send:{accepted:false, bytesWritten:0, refusedReason}}`; inspect `accepted`. However, `no-agent` / `permission`
  appear only with `requireAgentStatus:"sendable"`; without it, Orca writes anyway. Orca directly rejects `requireAgentStatus` together with "text + Enter"
  (requiring two steps: text, then Enter), but that loses bracketed paste, so we must verify foreground Agent via `terminal.agentStatus` before sending.
- **Bracketed-paste conditions**: requires `agentPrompt`, text, `enter`, `client.type: desktop`, and a foreground Agent recognized by Orca **and previously seen working**.
  The reply includes a `send.prompt` receipt. Newly launched claude that has not worked yet does not qualify; Orca writes raw text followed by `\r`. Persona therefore performs bracketed paste itself (see "Send message").
  Text without `enter` is written raw. `client` requires `id` (Persona uses `"persona"`, as Orca CLI does); `type` alone returns `invalid_argument`.
- **Default screen reads switch sources**: `terminal.read` without `screen` reads the output-stream tail (`source: stream`); always pass `screen:true`.

### Cautions

- The token in `orca-runtime.json` grants full Orca control; claude process environments also contain Orca's hook token (`ORCA_AGENT_HOOK_TOKEN`). node reads only required variables, never logs or sends them to hub.
  The client never prints tokens; tests check logs contain no token.
- `ORCA_TERMINAL_HANDLE` may be absent (set only when a handle was preallocated). Recognize panes only by `ORCA_PANE_KEY`, falling back to `ORCA_AGENT_PANE`, whose name Claude Code does not strip.
- Orca installs its hooks globally in `~/.claude/settings.json`. claude outside Orca also runs them, but scripts exit when Orca variables are absent.
  Persona leaves these hooks alone.
- `~/Library/Application Support/orca/agent-hooks/last-status.json` also has per-pane session IDs (`entries[paneKey].providerSession.id`),
  but is an internal Orca file; do not use it.
- The internal Codex variant in Orca can be listed (pane recognized through `ORCA_PANE_KEY`), but phase 1 rejects sends (host sends only to recognized claude / codex processes).
- Live integration checks are read-only: `go test -tags orcalive -run OrcaLive ./internal/node/ -v` uses a read-only client (allowing only `status.get`, `terminal.list`,
  `worktree.ps`, `terminal.read`, `terminal.agentStatus`; all other methods reject before sending) to list the active Orca once, print panes, status, pid, and session ID, and compare against
  `sessions/<pid>.json`, without printing screen content. Orca-mutating calls (send, close, create) are tested only against fake runtimes: `orcarttest` validates every request against Orca's
  parameter schema (`src/shared/rpc-contract`), returning and recording `invalid_argument` for invalid requests; tests verify none occur.
  node's test package points `ORCA_USER_DATA_PATH` at a nonexistent directory, preventing every test from touching real Orca.

### Structured sessions (#2072)

Since Orca 1.4.24x, "structured sessions" wrap Claude Code / Codex in Orca's own chat UI, without terminals. `terminal.list` omits them;
their rows in `worktree.ps` have pane keys `structured-agent-session-<session-id>:<uuid>`. Phase 1 recognized only terminals, leaving these invisible in Persona.
They are now integrated by the Orca bridge in node (`server/internal/node/orcabridge.go`, client `server/internal/orcart/session.go`) through the
runtime protocol (`docs/integrations/runtimes.md`), like the Paseo bridge but inside node (`net.Pipe` connects to `RuntimeHost`):

- **Activation**: enabled for the Orca engine, closed when switching away. Lists only structured sessions with `agentType: claude` in local worktrees (`hostId: local`);
  this version does not list structured Codex sessions.
- **Session ID**: Claude's own ID (`providerSession.id` from `agentSession.history`); history reads the usual Claude transcript.
  Sessions with no first message lack this ID and appear only after sending one (within 10 seconds). Space is worktree (`worktreeId`), named from branch as for terminal sessions;
  child worktrees carry `parent_space_id` and collapse under their parent on the web, like terminal sessions (#2073).
- **State**: poll `worktree.ps` every 1.5 seconds, using row `mainAgent.state` (working / blocked / done); push to hub only when the list changes.
- **Actions**: send text/images (messages sent while working queue after this turn, `delivery: queue-if-active`), interrupt (`agentSession.cancel`),
  close (`agentSession.close`, like Orca's close button; records remain), change permission mode and thinking effort (`agentSession.setOption`; Orca has no plan mode,
  so plan returns `unsupported`). No terminal, screen reads, keys, or live terminal.
- **Cards**: when blocked, read `agentSession.currentRequests`, mapping pending approvals to permission cards (if `detail` is tool-input JSON,
  show commands/files like hook cards), plan approvals to plan cards, and questions to question cards. Return answers with Orca option IDs and `expectedRevision`.
  Cards disappear from the phone if answered on the computer first.
- **Write envelope**: every write carries `envelope {sessionId, clientOperationId, expectedRuntimeFence, payloadFingerprint}`.
  `clientOperationId` is `<13-digit-milliseconds>-<32-hex-digits>`; the same message (same `client_msg_id`) reuses it, so Orca returns the original result on resend, never sends twice.
  `payloadFingerprint` is SHA-256 of canonical JSON `{method, sessionId, fields}` (sorted keys, no spaces, escaping as JavaScript `JSON.stringify`).
  Approvals and questions use `agentSession.respondTo:approval` / `respondTo:question`. `expectedRuntimeFence` comes from `page.fence` in `history`
  (no longer compared since 1.4.246, but must be a positive integer). `refusal` with `ok:false` means nothing was done; `agent_session_operation_unknown` means completion is unknown.
- **Socket has request/response methods only**: `agentSession.subscribe*` are streaming methods available only on paired WebSocket, so this uses polling.
- **Live checks** (2026-11-05, Orca 1.4.246, newly created test sessions closed afterward): `setOption`, `send` (same operation ID resend does not send twice),
  Bash approval card content and deny (file not created), answering an already answered approval, `cancel` (working → stopped), and `close` all matched the behavior above.

## herdr-core host

herdr-core (command `hc`) is the terminal runtime behind the Herdr desktop app. It has the same author as herdr, but is a rewrite and incompatible: different socket, `terminal.*` methods,
and environment variables. It is therefore a separate host, kind `herdr-core`, storing panes as `herdr-core:<address>` (such as `herdr-core:t7`).
Code: client `server/internal/herdrcore`, host `server/internal/node/hostherdrcore.go`. Checked against herdr-core 0.1.16 source and `hc schema`.

### One engine per machine

Each machine selects one "engine" (a set of hosts): herdr (default, one host), herdr-core, orca ("Orca source" above), and
`herdr-both`, added 2026-11-03 (herdr + herdr-core side by side, "Combined engine" below). Users choose per machine in the app (during pairing, **Settings › Machines**).
hub persists the choice and sends `engine.use` when node connects (API/storage in `docs/protocol/protocol.md`, "Engines"; node code `server/internal/node/engine.go`).

- **Why not combine by default**: initially both hosts registered together. Review found this slowed herdr: slow herdr-core delayed herdr refresh/send too (up to about 4 seconds);
  if herdr died while herdr-core remained, stale herdr Agents stayed online; herdr-core events triggered unnecessary herdr snapshots; an internal Codex variant in herdr-core
  forced herdr to poll every second. Selecting only one registers only that host and avoids all four. The combined engine is explicit and addresses them individually with parallelism and per-host isolation.
- **herdr selected**: node's host table, refresh, polling, and event stream remain exactly as before; no herdr-core client or subscription. The sole exception is `node.info`,
  which checks herdr-core availability once to fill `engines`.
- **On connection**: for a machine with a saved choice, hub's first call is `engine.use`. node waits for the engine to be settled (`engine.use`, hub listing sessions directly, or at most 3 seconds)
  before polling/subscribing, so herdr-core machines never briefly flash herdr sessions. Machines using herdr (no saved choice) receive no `engine.use`.
  With herdr-core selected, `node.info` does not query herdr either; the herdr version comes from `herdr --version`.
- **Runtime switching**: `engine.use` replaces the host table, discards old Agents, space names, and process caches (invalidating refreshes begun before the switch), ends every terminal stream
  (pushes `terminal.end`), stops old event streams and starts new ones, then pushes the session list. Each event stream has its own live marker; stopped old streams cannot clear new-stream state.
- **Operations without a host**: operations not deriving a host from a pane, such as creating sessions or renaming spaces, use `defaultHost` / `defaultHostKind`: herdr-core when selected,
  returning `unsupported` for unavailable capabilities, not "No herdr on this machine". Combined mode always defaults to classic herdr. herdr-selected machines are unchanged.
- **Nested herdr**: with herdr-core selected, a terminal whose foreground is classic herdr is not an Agent, even if inner claude OSC 7501 escapes through.
- **Nested ptys** (#5463): processes in a pty a program in the pane made (tmux, zellij, screen, `script`, a node-pty) inherit its `ORCA_PANE_KEY` and are each "in front" of that pty while the pane itself may show a shell. They are not the pane's: the scan walks up from each candidate to the first ancestor on another terminal (the pty's owner) and drops the candidate when that owner carries the same pane key; in an Orca pane the owner is Orca's terminal host, which has none (seen on a Mac 2026-11-06: `claude → bash → bash → login → orca-terminal-host`, versus `claude → bash → bash → tmux server` with the key). No owner found: not taken. Not by `TMUX=`: Orca's own Claude agent teams set it in a real pane. Without this, an agent's `tmux new-session -d` workers running `claude -p` took the pane from the claude that started them, which was then listed as interrupted.
  Combined mode registers such terminals under herdr-core with no Agent; the inner claude belongs to a herdr pane and is not counted twice.

### Combined engine (herdr-both, 2026-11-03)

Reason: Herdr App can connect remotely to another machine's terminals (`herdr remote-client-bridge`), so classic herdr (local terminals) and
herdr-core (Herdr App remote access) can both run on one machine. Selecting either hides the other's sessions. `herdr-both` registers both,
isolating the four issues in "Why not combine by default":

- **Refreshes do not wait for each other**: `refreshHosts` changes from sequential to per-host parallel, with each call bounded by its client timeout (herdr-core list: 2 seconds).
  One stuck host does not block the other; partial refresh replaces only that host's Agent cache, retaining the other's last result.
- **Events do not trigger each other**: each watcher host has its own `watcherSignals` (live marker + kick channel, `herdrevents.go`). The polling loop selects all kicks,
  adding only the emitting host to that round's due list; hosts without events retain their own slow-snapshot cadence (10 seconds). herdr-core events no longer trigger herdr snapshots.
- **Online state is per-host**: Agent cache is `map[hostKind][]Agent`; a host returning `not_running` clears only its own group.
- **The internal Codex variant does not slow the other host**: `hostHasTraex(kind)` counts that variant only in the given host's panes and decides whether that host needs a snapshot every second.
- **Default host is herdr**: create / continue / rename use herdr; pane actions still route by PaneRef kind. herdr-core pane restrictions (no color, no text-only typing,
  no live terminal) are exactly the same as when herdr-core alone is selected.
- Orca is not combined: no event stream, one-second polling, heaviest model. Combining it would be separate work later.

### Connection

- Socket location follows `hc` rules: `$HC_SOCKET`, otherwise top-level `socket` in `$XDG_CONFIG_HOME/herdr-core/config.toml`, otherwise `<session>.sock` in the runtime directory
  (`$XDG_RUNTIME_DIR/herdr-core`, or `/run/user/<uid>/herdr-core` if it exists), otherwise state directory
  `$XDG_STATE_HOME/herdr-core/<session>/<session>.sock` (usually `~/.local/state/herdr-core/default/default.sock` on macOS). Session name is `$HC_SESSION`, default `default`.
- Protocol: JSON-RPC 2.0, one message per line. Ordinary calls open a fresh connection; `terminal.list` and `doc.get` have 2-second limits (every refresh), sending 20 seconds
  (`terminal.send` waits for 3 seconds without typing on the computer), keys 8 seconds, others 5 seconds. Errors carry stable `data.kind` (`not_found`, `blocked`, etc.).
- `events.subscribe` does not use framed mode: after its response, the ordinary connection sends only notifications (`event`, `heartbeat` every 15 seconds, `missed` plus a new snapshot when behind).
  No message for 45 seconds means disconnected; reconnect with backoff. Only `terminal.attach` needs `hello {framed:true}`.
- Only a missing socket file or refused connection (no listener) means not running (`host.ErrNotRunning`): immediately answer "No terminal", clear sessions, no logs.
  Connection timeouts and similar failures mean it is still there but busy; retain the last sessions. If previously connected, log one line at the start of each outage and one on recovery.
  Opening Herdr later requires no node restart.

### Mapping

| Persona requirement | herdr-core implementation | Phase 1 |
| --- | --- | --- |
| List panes | `terminal.list`: local terminals only (address has no `/`) with `state: live` | ✅ |
| Agent type | `status.app` (program's own OSC 7501 value, unchanged): `claude-code` → claude, `codex` → codex, others are not Agents (left to internal-variant detection). If no status yet, check foreground `argv[0]` for `claude` / `codex`. Foreground shell, classic herdr, or no foreground process is not an Agent: herdr-core retains an exited Agent's last status until the terminal's own program ends | ✅ |
| State | `status.state`: working / blocked / done / idle unchanged; `error` and claude without status → unknown (error says an error occurred, not what it is doing now) | ✅ |
| Map pane to session | Foreground `foreground.pid` is claude's own pid; read `sessions/<pid>.json`; herdr-core reports no session ID | ✅ |
| Spaces / slots | Herdr-created terminals have labels `herdr.space`, `herdr.tab` (space/tab IDs in layout document `doc.get {name:"herdr-app"}`), and `where` (space / tab display name). Use document names, then `where`, then directory basename / index; indices follow document order. Non-Herdr terminals (`hc run`) group by directory into space `dir:<directory>`, with empty slot | ✅ |
| Read screen | `terminal.read {terminal, lines:0}`: plain text only, soft wraps already joined, current screen only. History reads make herdr-core send wheel events to full-screen programs and withhold user keys, so `recent` also returns only the current screen, without scrolling back | ✅ Plain text, no history |
| Send message | First `terminal.get` verifies an Agent remains (reject after exit so the shell cannot execute the message as a command), then `terminal.send {terminal, text}`: paste then Enter (bracketed paste if enabled), waiting for 3 seconds without computer typing. Rejection delivers no input: `blocked` → `dialog_open`; `status_mismatch` / `unavailable` / `finished` / `not_found` → `not_delivered` (all `delivered:none`, safe to resend) | ✅ |
| Special keys | `terminal.keys`: names match Persona (`enter`, `esc`, `shift+tab`, `ctrl+u`, `up`, etc.), forwarded unchanged | ✅ |
| Type without Enter | Not possible; see below. "Text + Enter" and `/effort` use `terminal.send`; other text input (including dialog options requiring keys to open an input field first) rejects before any keys (`cannot_type`, Chinese explanation) | Partial |
| Recognize hook pane | All terminal processes have `HC_TERMINAL_ID` (the address); without `HERDR_PANE_ID`, `personad hook` reports `herdr-core:<address>` | ✅ |
| Push changes | One `events.subscribe` connection covers all terminals; any change (except size, keep, and other-machine changes) immediately requests a snapshot | ✅ |
| Live terminal | Not implemented; see below. hub falls back to screen reads | ❌ |
| Close | Like herdr, send ctrl+c twice, then `terminal.kill` (hangup → terminate → force kill, returning terminal state after at most about 3 seconds). Herdr App also closes panes with only `terminal.kill`; when the terminal ends as `killed`, the app removes the pane (or tab if last), without `terminal.rm`. We likewise only kill, retaining the ended record. Ended or absent (`not_found`) counts as closed; still `live` after kill is an error | ✅ |
| Rename | Space and tab names belong to Herdr App's layout document; do not rename from the phone. `RenameSpace` / `RenameSlot` return `unsupported` ("Herdr App spaces and tabs cannot be renamed from the phone yet"). Session titles (`/rename`) work normally | ❌ |
| Create, takeover | Not in phase 1 (no Launcher / Controller) | Later |

### Findings

- **Are addresses reused?** No. Addresses use an ever-increasing counter (`t1`…`t9`, `t10`, `t1b`, etc.), never recycled when terminals are deleted, persisted in the state directory across restarts.
  Only clearing the state directory (or detecting a cloned disk) restarts from `t1`. Pane IDs therefore use the address directly, without `created_ms`: clearing state is a rare manual operation,
  and session recognition checks `sessions/<pid>.json` first, using old bindings only when process identification fails.
- **Can it read colors?** No. `terminal.read` is plain text; only the first `terminal.attach` frame has color. The host declares `host.PlainScreen`:
  colored reads return `unsupported`; when the app requests color, node sends plain text instead (line endings converted to `\r\n`). Claude pre-send checks only dialogs
  (`screen.Choosing`), not drafts, since draft text cannot be distinguished from gray hints without color. herdr's path remains unchanged (it does not declare `PlainScreen`).
  Extra Enter (`ensureSubmitted`) and suggested replies (`suggestion.go`) are skipped without color, and unnecessary: `terminal.send` adds Enter itself.
- **Can it paste without Enter?** No. `terminal.send` always adds Enter; `terminal.keys` accepts key names only (single letters/digits and some punctuation, not arbitrary text).
  Raw input exists only in the framed `terminal.attach` stream, and attach without `observe` resizes the terminal to our dimensions. `SendText` therefore returns `unsupported`,
  and the host declares `host.TextTyper` with `CanTypeText: false`. node sends "text + Enter" and `/effort` as one `terminal.send`; other key requests requiring text
  reject before any keys (including free-form dialog options, which previously opened the input first and failed when typing, leaving a half-open field).
- **What format is the stream?** `terminal.attach {observe:true}` first sends a complete redraw (`0x18` followed by VT sequences), then raw program PTY output, not delta frames;
  it redraws only when backlog exceeds 4 MB. node's current frames are a subset of herdr's grid redraws, and the app's custom VT subset may not handle arbitrary raw program output,
  so phase 1 has no `Streamer`; hub falls back to screen reads.
- **Can keep terminals close?** Yes, but ctrl+c alone is insufficient. Herdr App Agent terminals run `sh -ic '"$@"; exec "$0" -i' sh <agent>` with `keep`:
  the shell continues after Agent exit and the terminal remains `live`. `keep` starts a replacement shell only if a program is "lost" (killed by signal, holder gone, etc.); normal exit with an exit code and
  `terminal.kill` are final (herdr-core `terminals.rs` tests `keep_preserves_losses…`, `terminal.keep` documentation). Always follow two ctrl+c sends
  with `terminal.kill`. Tested on an owned instance: after ctrl+c terminal stayed `live` with a foreground shell; after kill it was `finished`,
  with no revival after the keep grace period (`TestHerdrCoreLiveCloseKeep`).

### Cautions

- herdr-core has no hooks; status comes only from the program's own OSC 7501 (Claude Code sends it). Programs that do not emit it have no status.
- herdr-core does not discard `terminal.send` / `terminal.keys` abandoned by the caller: they wait for 3 seconds without computer typing and may arrive after Persona times out.
  Key timeout was shortened to 8 seconds to reduce this window; dialog answers do not recheck revision just before delivery.
- Known minor differences (not addressed yet): our parser and `hc` may differ on malformed `config.toml`; a snapshot can take about 4 seconds (`terminal.list` + `doc.get`,
  affecting only herdr-core-selected machines); pane IDs omit core identity, so clearing state restarts addresses at `t1` (process-first recognition limits impact).
- `terminal.list` also lists other machines connected to this core (addresses contain `/`); they have their own node, so skip them here.
- Live checks use only owned instances: point `HC_SOCKET`, `HC_SESSION`, `XDG_STATE_HOME`, and `XDG_CONFIG_HOME` under `/tmp`, then `hc server stop` afterward.
  On the default instance, use read-only calls only (`terminal.list`, `terminal.get`, `doc.get`); terminal-ending actions such as close are tested only on owned instances. See `server/internal/node/hostherdrcore_live_test.go` (`-tags hclive`).

## Implementation order

0. [x] **Add tests** (completed 2026-11-01, `server/internal/node/herdr_*_test.go`, `herdrfake_test.go`): characterization tests for node/herdr interactions. One shared fake herdr socket records every call from node in order;
   each test asserts "given this herdr state, what sessions are listed and what is the herdr call sequence?" Drive tests through node's request entry point, not internal fields.
   Rerun unchanged after step 1 to prove behavior stayed the same. Tests only, no business-code changes.
1. [x] **Move only herdr, preserve behavior** (merged 2026-11-01, `server/internal/host` + `node/hostherdr.go`; unit tests and `scripts/test/test-herdr.sh` live integration passed;
   there were no Claude panes in herdr at deployment, so `live_panes_check.py` real-pane comparison remains unrun; rerun once panes exist, observe for a day or two before step 2): define interfaces, move all herdr calls behind them, implement `PaneRef` and prefixed storage together. Existing and corpus tests all pass;
   step 0 tests may change only the "Node adapters" block in `herdrfake_test.go`; not one line of the three `herdr_*_test.go` files, or behavior preservation is not proven.
   After deployment, compare real panes with `scripts/diag/live_panes_check.py` to confirm session recognition is unchanged. Merge and deploy separately, run a day or two before proceeding.
2. [x] **Orca phase 1** (merged 2026-11-01; temporary Orca terminals tested for listing, single/multiline sends, rejection while suspended, and closing): discovery, status, sends, keys, plain-text screen reads, screen-based dialog answers, close, as the third engine ("Orca source" above).
   Test with a fake Orca socket (`orcarttest`, validating Orca parameter rules); read-only checks against active Orca confirmed all session IDs matched `sessions/<pid>.json`.
   Another host: [x] **herdr-core phase 1** (merged 2026-11-01, one engine per machine): "herdr-core host" above. Close supported (2026-11-01); rename omitted because Herdr App owns it. Later: paste without Enter (await upstream method), live terminal, create.
   [x] **herdr + herdr-core combined** (2026-11-03, `herdr-both`, "Combined engine" above): multiple hosts in the table, parallel refresh no longer waiting for the slowest.
3. [ ] **App**: engine selection is done (pairing, **Settings › Machines**, options from hub `engines`, 2026-11-01); capability-based button hiding and generic wording are not.
4. [ ] Later: Orca phase 2 (create, modes), phase 3 (live terminal).

## Fixes deferred until after step 1 merges

Issues discovered while adding tests, with tests pinning current behavior. Fixing now would change the before/after-refactor baseline, so address separately after step 1, updating corresponding tests too:

- Switching to `bypassPermissions`, `tuiSetMode` gives up after 6 shift+tab presses, leaving Claude in a different mode (not the original). Restore the original on failure, or verify the target exists in the cycle first (`node/modes.go`).
- Fixed (#4873): when `pane.list` fails, `close()` closes only the requested pane (herdr host `Close`, `node/hostherdr.go`). herdr closes the tab itself if that was its last pane, preserving other split panes otherwise.
- Four existing `catchup_test.go` tests fail with `CLAUDE_CONFIG_DIR` set: they assume Claude config under HOME. Clear this variable in tests (new `herdrNode` already does).
- `TestTerminalControlEndsWhenTakenOrForgotten` in `terminal_test.go` changes global `termLease` while a still-running `controlLoop` reads it: occasional `-race` failures (seen once by the engine-selection worker with `-count=10`; not reproduced in 30 runs each on branch and master). Wait for `controlLoop` before test completion, or make the lease an instance field.
- Other suspicious behavior (not necessarily needing fixes): fallback from failed herdr snapshot to `agent.list` retains old space / slot names; `editQueue` clears lines based on transcript queue, not what the TUI actually restores.

## Open decisions

- Report capabilities per session or per host on each machine (leaning toward the latter, with sessions carrying only host kind).
- ~~Displaying one session in two hosts simultaneously~~: settled in #2576. It is listed once, owned by one host, and its row carries `also_open_in` (`docs/integrations/engines.md` "Host sets").
- Sessions outside any host but with a live process (ordinary terminal): mark "Running elsewhere" and disable sending? `sessions/<pid>.json` alone can detect this, independent of host abstraction, so it can be done first.
