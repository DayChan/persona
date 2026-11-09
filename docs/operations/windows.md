# node on Windows (herdr and Orca engines)

Lets the phone see and drive Claude Code inside herdr or Orca on a Windows computer: that computer runs the resident node (`personad node --attach`) and the connector persona-link (active mode, see [link.md](link.md)),
while the hub lives elsewhere (WSL2 / Docker / another machine). Requirements and steps are in #1090; the overview of Windows support is #1179. Pitfalls we hit are in [lessons/windows.md](../lessons/windows.md).

## Status

Both engines work: **herdr** (native Windows build since 0.9.5, see "herdr" below) can create and resume sessions from the phone, same as macOS / Linux; **Orca** can only view and drive sessions that are already open.
The table below is for Orca; where the herdr engine differs is in the table in the "herdr" section.

| Capability | Windows (Orca) | Notes |
| --- | --- | --- |
| Build | ✅ | `GOOS=windows go build ./...`; process groups, signals, file locks and CPU time are in `internal/osproc` and each package's `_windows.go` |
| Tray icon and panel | ✅ | `install` also starts the tray at sign-in: status, reconnect, restart or install the background service, pairing (active and passive). See "Tray" below |
| Resident node, persona-link active mode | ✅ | `persona-link install` installs a scheduled task that starts at logon, restarts on exit and shows no window ("Install and start at logon" below); there is no one-line install script or installer package (#1179) |
| List Orca sessions and state | ✅ | One snapshot per second, as on macOS |
| Read transcripts and session info | ✅ | `%USERPROFILE%\.claude\projects\<encoded directory>\<id>.jsonl`; the directory encoding is the same as Claude Code's (every non-alphanumeric becomes `-`, `C:\Users\…` → `C--Users-…`) |
| Send messages (multi-line as one), keys, interrupt | ✅ | Bracketed paste works under ConPTY |
| Answer dialogs (AskUserQuestion, permissions) | ✅ | Answered by reading the screen, same as Orca on macOS |
| Close a session | ✅ | |
| Create a session from the phone, resume a Claude session that isn't running | ✅ | Since #5548 Orca opens a terminal in the session's worktree and runs `claude --resume` there, as herdr does; other agents' sessions are refused with why (`orca_resume`) |
| Claude in the desktop app or in a terminal Persona does not drive | ✅ | Listed as running with its own state, transcript streamed; messages go through the session's peer inbox (a named pipe). Questions and permission prompts there stay on the computer. See "Claude outside Persona's terminals" below |
| Machine monitoring (CPU, memory, swap, network, disk, system info) | ✅ | Win32 calls, no processes spawned; the metrics follow the Windows column of [machines.md](../server/machines.md) ("Collection metrics") (#1943) |
| Port forwarding | ❌ | Depends on `ps` / `lsof` / `/proc`; silently unavailable on Windows (#1179) |
| Codex discovery (processes, homes, `--listen` endpoints, which rollouts a process holds open) | ✅ | Win32 calls, no processes spawned: Toolhelp32 and the PEB for `codex.exe`, each one's handle table for its open rollouts ("Codex" below). Compatible Codex forks are still not discovered |
| Codex desktop App chats: list, state, send text and images | ✅ | Through the App's bus, the named pipe `\\.\pipe\codex-ipc` ("Codex" below) |
| Codex CLI running in a plain terminal | ✅ view, state | Seen as running from the rollout it holds open (working / done as on macOS), so never resumed into a second writer; view only, as on macOS. On a `--listen` app-server it joins like anywhere else |
| Resume an exited Codex CLI thread from the phone | ❌ | The Orca engine does not resume Codex on any platform; it works with the herdr engine (herdr table below) |
| Processes the node starts itself: managed launchers, `codex` start | ✅ | `osproc.Exec`: Windows has no exec, so it starts a child in the same console and exits with its exit code ("herdr" below) |
| hub self-update | ❌ | Still `syscall.Exec`; the hub doesn't run natively on Windows (#1179) |
| Updating the resident node through the hub ([node-update.md](node-update.md)) | ❌ | The node reports it as unsupported ("resident node is not managed by launchd/systemd"), and the App shows why; to upgrade, run `persona-link install --personad <new personad.exe>` again |
| Passive mode (hub connects to this machine) | Not verified | Builds, and `install --listen` takes the same path; you have to open the port in Windows Firewall yourself (`install` prints the `New-NetFirewallRule` command) |

## Install and start at logon

```powershell
cd server
go build -o personad.exe .\cmd\personad
go build -o persona-link.exe .\cmd\persona-link
.\persona-link.exe install --personad .\personad.exe --pair '<the persona://link?… link from the hub>'   # skip --pair if already paired
```

No administrator needed. On Windows, `install` does, in order:

1. With `--pair`, pair first; `--listen` switches to passive mode (same as Linux, see [link.md](link.md), "Linux (headless)").
2. If the scheduled task is running, end it first (a running program file can't be replaced) and wait for persona-link to release its run lock. If another persona-link is running (say, a foreground `persona-link run`), it asks you to stop it. A running tray is asked to quit, for the same reason.
3. Copy itself to `~/.persona/bin/persona-link.exe`; copy the personad given by `--personad` to `~/.persona/bin/personad.exe`. Unlike Linux, if one is given it is always replaced:
   Windows can't update the node through the hub yet, so running `install` again is the upgrade. If none is given and there is no `personad.exe`, only persona-link is installed (same as `--no-node`).
4. Put another copy at `~/.persona/bin/persona-link-service.exe`: the same program, with only the subsystem in the PE header changed from console to GUI (`guicopy.go`).
5. Write `~/.persona/persona-link-task.xml` (UTF-16), register the scheduled task "Persona Link" with `schtasks /Create /XML`, start it with `schtasks /Run`, and wait for it to connect to the hub (same as Linux, up to 20 seconds).
6. Install the tray for this user and start it: the sign-in entry, the handlers for `persona://` links and the previous brand's ones and the running tray ("Tray" below). `--no-tray` skips it and removes the sign-in entry.

The "Persona Link" scheduled task:

- **Current user, runs with the logon session** (`InteractiveToken`). A standard user can't register a "run whether logged on or not" task (S4U gets access denied on a real machine; the stored-password kind needs an administrator),
  and the node has to be in the user session to see Orca and its agents anyway. The cost: it isn't there when nobody is logged in to Windows, stops at logoff, and starts again at the next logon.
- **Triggers**: at logon, plus every 5 minutes starting from install. The multiple-instances policy is "ignore new": while the supervisor is running it does nothing; if it's gone it is brought back within 5 minutes.
  The task's own "restart on failure" isn't used: it only covers the task failing to start, not the program exiting non-zero (tested on a real machine). "Repeat" on a logon trigger only starts at the next logon, so a separate time trigger is added.
- No run-time limit (by default the task is ended after 72 hours), runs on battery, isn't stopped when idle ends, normal priority (the default 7 is "below normal", lowering both CPU and disk).
- **What runs is the supervisor** `persona-link-service.exe supervise` (`supervise_windows.go`): it starts persona-link (`run --log ~/.persona/link.log`) with `CREATE_NO_WINDOW`
  and the resident node (`personad.exe node --attach`, log `~/.persona/node-attach.log`, moved to `.1` at 10 MB), and restarts whichever exits after 5 and 10 seconds respectively (same as `RestartSec` on the two Linux units).
  What the supervisor itself does goes to `~/.persona/link-service.log` (persona-link's output when it crashes is there too). Only one supervisor runs per computer (file lock on `~/.persona/link-service.run`).
  It starts the node first and waits for it to recreate `node-attach.sock` (it only checks the file's modification time and doesn't connect: each connection is an attach and would kick off the hub's connection; waits up to 10 seconds) before starting persona-link.
  The other way round, the hub connects the link first and immediately opens a stream to a node that isn't listening yet, briefly reporting this machine offline, and `install` concludes "the hub can't reach this machine's node" (#1984).
- The supervisor puts itself in a Job that "kills every process inside when the last handle closes": when the supervisor goes away (task ended, another install / uninstall, logoff, crash),
  persona-link and the node go with it, so when the scheduled task brings it back it doesn't fight leftover old processes.

**Why a GUI-subsystem copy**: when a scheduled task starts a console program in the user session, Windows opens a console window for it (a Windows Terminal window if Windows Terminal is the default terminal),
which stays on the desktop. A GUI-subsystem program has no window, and the console programs it starts with `CREATE_NO_WINDOW` get a console but no window. It's a copy rather than a separate build: whether `persona-link.exe`
is a release or your own `go build`, the supervisor is the same program. Approaches not used: `conhost --headless` (undocumented), a VBScript wrapper (VBScript is being removed from Windows),
`powershell -WindowStyle Hidden` (still flashes a window).

| Command | On Windows |
| --- | --- |
| `persona-link status` | An extra line "Background service: scheduled task "Persona Link" · state · starts at logon"; the state is whatever schtasks prints in the system language (Running / 正在运行, the Chinese-locale "Running"…), shown but not parsed. Then a line for the tray: running or not, starts at sign-in or not (`tray` in `--json`) |
| `persona-link uninstall [--purge]` | Ends and deletes the scheduled task (the node stops with it), deletes `persona-link-task.xml` and `persona-link-service.exe`; quits the tray and removes its sign-in entry, its link handlers and WebView2's cache. `--purge` is as on Linux and also deletes the supervisor's logs and lock |
| `persona-link name [<new>\|--reset]` | As on Linux: shows or changes what the hub calls this computer ([link.md](link.md), "The computer's name") |
| `persona-link tray [--open <link>]` | Runs the tray (what the sign-in entry runs). A second one shows the running tray's panel, or hands it the link |
| `persona-link node install` / `node uninstall` | Not installed separately: the node is in the same scheduled task; use `install` (with node) / `install --no-node` |
| `persona-link node status [--json]` | Whether the scheduled task includes the node, and the task's state |

## Tray

The tray icon is the Windows counterpart of the Mac menu bar App ([link.md](link.md), "Menu bar App"): it shows the link's state and does what the App's menu does (#299).
It is part of persona-link (`internal/linktray`, `persona-link tray`), not a separate program, so it is always the same version as the connector it manages. Pure Go, no cgo: Win32 for the icon, the menu and the windows, WebView2 (`wailsapp/go-webview2`) for the panel and the pairing window.

- **What runs**: `install` writes "Persona Link" under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` → `"%USERPROFILE%\.persona\bin\persona-link-service.exe" tray` (the GUI-subsystem copy, so no console window) and starts it right away.
  The tray is not in the scheduled task: the supervisor's Job would end it with the task, and `install` starts it outside any Job (`CREATE_BREAKAWAY_FROM_JOB`) so it outlives the terminal that ran `install`.
  The panel's switch **Show in tray at sign-in** (or Settings › Apps › Startup) only changes the sign-in entry; the background service runs either way.
- **One per data directory**: a named mutex; a second `tray` hands its link (or "show the panel") to the running one over `WM_COPYDATA` and exits. `install` asks the running tray to quit before replacing the program and starts the new one afterwards.
- **State is read, never asked**: `link.json`, `link.status.json` and whether the pid in it is a live persona-link (`OpenProcess` and the image name). The data directory is watched (`FindFirstChangeNotification`), so changes show at once;
  otherwise it reads them every second while the panel or the pairing window is open and every 3 seconds when not. The scheduled task's state (schtasks, about 50 ms) is read every 30 seconds and after each action.
- **Reconnect now and network changes**: Windows has no `SIGUSR1` / `SIGUSR2`. persona-link waits on two named events instead, `Local\PersonaLink.<hash of link.json's path>.recheck` and `.reconnect` (`internal/link/poke*.go`), which the tray sets;
  pairing from the tray sets `recheck` so a running persona-link reads the new credential at once. persona-link watches network changes itself with `NotifyIpInterfaceChange` (`internal/netaddr`), as it does with netlink on Linux.
- **Icon**: the Mac App's seven looks (Connected, Needs your OK, Service not running, Connecting, Waiting for hub, Offline, Not paired), drawn at the notification area's size and DPI for a light or dark taskbar (`icons.go`; `LINKTRAY_ICON_SHEET=<dir> go test ./internal/linktray/ -run Icon` writes them out to look at).
  Left click opens the panel; right click is a native menu with the same actions, which works without WebView2.
- **Panel and pairing window**: HTML pages (`internal/linktray/web`; every size and color comes from `tokens.css`) loaded from memory, with no server and nothing fetched. The panel is a flyout over acrylic next to the icon, on the taskbar's side;
  the pairing window (active and passive, as in the Mac App) is a normal window over Mica. Both follow the system's light or dark mode, accent color, transparency setting and the monitor's DPI (per-monitor v2).
  The backdrops need Windows 11 22H2 (build 22621); earlier builds get a solid background. Slow actions (install, restart, pair) run off the UI thread, and their result shows in the panel in the user's words.
- **Rename this computer…**: in the panel (and the right-click menu) while connected to a hub that renames computers and once the computer is confirmed. It opens a name field in the panel; Save asks the running
  persona-link to have the hub rename it, as `persona-link name` does ([link.md](link.md), "The computer's name"), and the panel says the hub's answer. The status line shows the name the hub gives the computer.
- **Notifications**: once when the hub waits for your OK on this computer, and once when a link that was up has been down for a minute.
- **Links**: `persona://` links, and the previous brand's ones that older hubs print, open the pairing window with the link filled in (`HKCU\Software\Classes\<scheme>`, running `tray --open "%1"`).
  A scheme that another program already handles is left alone (a desktop App's phone link must keep working), and uninstall removes only the handlers that are ours.
- **Language**: Windows' display language, en / zh-Hans / ja (`internal/i18n/locales/*/link-tray.json`). The failures it recognizes (hub unreachable, link refused, no background service) are explained in that language, with the raw error after the explanation.
- **WebView2**: Windows 11 always has the runtime. On Windows 10 without it, clicking the icon shows a notification and the native menu. WebView2 keeps the tray's profile (a cache, nothing of the user's) in `%LOCALAPPDATA%\Persona\LinkTray`, and `uninstall` deletes it.
  `PERSONA_TRAY_DEVTOOLS=1` turns on the developer tools (F12); a port number (`PERSONA_TRAY_DEVTOOLS=9333`) also opens remote debugging on that port, for Playwright's `connect_over_cdp`.
- **Pages in a browser**: `LINKTRAY_PAGES=<dir> go test ./internal/linktray/ -run TestWritePages` writes every sample state's panel and the pairing window, in each language and theme, as HTML files.

## hub in Docker Desktop on the same computer

During acceptance, the hub and node were on the same Windows machine: the hub ran in Docker Desktop (WSL 2), node and persona-link on the Windows side.

- With WSL mirrored networking (`networkingMode=mirrored` in `.wslconfig`), ports published by Docker are only reachable on Windows' `127.0.0.1`; LAN and Tailscale addresses can't connect
  (opening them requires changing the Hyper-V firewall, which needs an administrator).
- The local persona-link connects to `127.0.0.1`: if the hub's advertised candidate addresses include no loopback address, add it manually with `persona-link address add https://127.0.0.1:<hub TLS port>`.
- The phone connects over Tailscale: this machine forwards the tailnet port to localhost with `tailscale serve --bg --tcp <port> tcp://127.0.0.1:<port>` (plain TCP forwarding; TLS is still done by the hub itself,
  and the pinned certificate is still the hub's), and `public.advertise` in hub.json lists `https://<this machine's Tailscale address>:<port>`.
- To pair the phone with a new hub, use **Settings › hub › Add Hub** in the App, not the root pairing link: that would replace the phone's existing pairing ([lessons/android.md](../lessons/android.md)).

## Running it manually (for development and acceptance)

On a machine with the scheduled task installed, first run `schtasks /End /TN "Persona Link"` (or `persona-link uninstall`); otherwise the foreground and scheduled-task copies, two persona-links and two nodes, will fight.

```powershell
cd server
go build -o personad.exe .\cmd\personad
go build -o persona-link.exe .\cmd\persona-link
.\personad.exe node --attach            # resident node; keep it open in another window
.\persona-link.exe pair '<the persona://link?… link from the hub>'
.\persona-link.exe run                    # keeps the link up
```

On the hub side: this machine is `kind: link` in `machines` in hub.json, with `public.tls_listen` on; get the pairing link with `personad hub pair --machine <id>` (for a Docker deployment,
`docker compose exec hub personad hub pair --machine <id>`). Once connected, set this machine's engine to Orca in the App (**Settings › Machines › Engine**).

## herdr

herdr has a native Windows build since 0.9.5 (ConPTY panes, [official notes](https://herdr.dev/docs/windows-beta/)). The node drives it only through herdr's socket API, so the herdr engine on Windows behaves as everywhere else:

| Capability | Windows (herdr) | Notes |
| --- | --- | --- |
| List sessions and state, read transcripts | ✅ | Same as macOS / Linux |
| Create a session from the phone, resume a session that isn't running | ✅ | The node types the start command in a new tab, as elsewhere |
| Send messages (multi-line as one), keys, interrupt | ✅ | |
| Answer question / permission cards | ✅ | Via Claude Code hooks, no screen reading |
| Live terminal (`terminal session observe`) | ❌ | herdr has no direct terminal attach on Windows; falls back to reading the screen |
| Codex in a herdr tab: list, send, answer cards | ✅ (tests) | The terminal route as elsewhere ([codex.md](../integrations/codex.md)); not run against a real herdr on Windows |
| Resume an exited Codex thread from the phone | ✅ (tests) | `codex resume <id>` in a new tab through `personad codex-start`, which runs the `codex.exe` behind npm's `codex.cmd` ("Codex" below); not run against a real herdr on Windows |
| Claude in the desktop app or in a terminal Persona does not drive | ✅ | As in the Orca table above |

- **socket**: herdr's API on Windows is a named pipe, named `\\.\pipe\` plus the socket path (`\\.\pipe\C:\Users\<you>\AppData\Roaming\herdr\herdr.sock`);
  the config directory is `%APPDATA%\herdr` (or `herdr` under `$XDG_CONFIG_HOME` if that's set). `socket_windows.go` in `internal/herdr` dials by this rule, matching herdr's own
  `config_dir` / interprocess `GenericNamespaced`; `$HERDR_SOCKET_PATH` still takes precedence. The path itself isn't a file, so `SocketUp` checks whether the pipe exists.
- **Shell in the pane**: herdr opens PowerShell by default on Windows, but the start commands the node types are POSIX shell syntax (`'…'` quoting, `A=1 cmd`). Set the pane shell to Git Bash in herdr's
  `%APPDATA%\herdr\config.toml`:

  ```toml
  [terminal]
  default_shell = "C:/Program Files/Git/bin/bash.exe"
  ```

- **herdr has to stay resident in the user's desktop session**: a `herdr server` started from an ssh session exits with that session. Run `herdr.exe server` from a scheduled task that starts at logon (current user,
  `Interactive`, no time limit); it's the same kind as the `persona-link install` task.
- **`syscall.Exec`**: `personad launch` (managed launchers) and `codex-start` / `tab-start` replace themselves with claude / codex. Windows has no exec, so `osproc.Exec` starts a child in the same
  console (stdin/stdout inherited as-is, so the pane shows that program), catches Ctrl+C itself without exiting (the child in the same console still receives it; only `signal.Notify` works, `Ignore` doesn't block it), and exits with the child's exit code when it ends.
- **Claude Code's first run**: a freshly installed Claude Code first shows the theme-picking onboarding, so the node never sees the input box ("claude did not become ready"). Run
  `claude` manually once on that computer to finish onboarding, or set `"hasCompletedOnboarding": true` in `%USERPROFILE%\.claude.json`.
## Local sockets

The node's `node.sock`, `runtime.sock` and `node-attach.sock` are still AF_UNIX socket files on Windows (available since Windows 10 1803), under `%USERPROFILE%\.persona\`.
They weren't switched to named pipes: Go's `net` supports them directly, so the hooks, persona-link and `--stdio` forwarding code didn't change a line. Listening, connecting, half-close (`CloseWrite`) and replacing a leftover socket file were verified on a real Windows 11 machine.
Limits: the path can't exceed 108 bytes (a very deep `%USERPROFILE%` gives `bind: invalid argument`); permission bits like `0600` mean nothing on Windows, so it relies on the user directory's own ACL.

## Orca

- **Transport**: the Windows build of Orca writes `named-pipe` (`\\.\pipe\orca-<pid>-…`) and `websocket` in `orca-runtime.json` (`%APPDATA%\orca\`), with no `unix`.
  `orcart` picks by platform: on Windows `named-pipe` first, then `unix`; elsewhere only `unix`. Named pipes are dialed with the existing dependency `tailscale/go-winio`. The protocol, token and metadata re-read rules are unchanged.
  The fake runtime used in tests (`orcarttest`) also uses named pipes on Windows, so the `orcart` and `hostorca` tests on Windows exercise the real transport.
- **Identifying panes and sessions**: as on macOS, by `ORCA_PANE_KEY` in the claude / codex process environment (falling back to `ORCA_AGENT_PANE`), then reading `sessions/<pid>.json`.
  Windows has no `ps -E` or `/proc`: `procs_windows.go` lists processes with Toolhelp32 and reads the command line, current directory and environment block from the PEB only for candidates (`claude`, `codex`, `node`, `bun`);
  only the few environment variables needed are kept (Orca's hook token and the like are never kept).
- **In the foreground**: Windows has no terminal foreground process group and no "suspended". Instead, a process counts only if "no ancestor in its parent chain is an agent process in the same pane" (a claude started by claude isn't the one the terminal shows);
  pids get reused, so an ancestor must have started before the process itself. When a pane has several, claude comes first, then by start time (Windows pids don't increase, so they can't be sorted by pid).
- **Screen-reading card**: Orca reports the session as blocked as soon as the AskUserQuestion PreToolUse hook fires, but starting a hook process on Windows takes a few seconds, so the dialog appears late.
  The card the manager built at that moment couldn't read the dialog ("can't read this dialog yet"); after that the manager looks at the screen again every 500 ms (up to 30 seconds) and swaps in a card with options as soon as the dialog appears (`assistant.upgradeScreenCard` in `assistant/fleet_ix.go`, on all platforms).

## Codex

What the node needs to know about Codex on macOS / Linux comes from `ps`, `lsof` and `/proc`; on Windows from Win32 calls, without starting a process ([codex.md](../integrations/codex.md) has the rest).

- **Processes** (`codex_procs_windows.go`): Toolhelp32 lists the processes; only `codex.exe` ones (the CLI's npm shim starts the native `codex.exe`, the Codex App bundles one as its app-server) have their command line and
  `CODEX_HOME` / `HERDR_PANE_ID` read from the PEB, as for Orca. A `codex.exe` of this logon session that cannot be read (an elevated one) makes the scan "could not tell", so a resume refuses rather than guess.
- **Which rollouts a process holds open**: its handle table (`NtQueryInformationProcess(ProcessHandleInformation)`); each File handle is duplicated for a moment, and only disk files are asked their path
  (`GetFinalPathNameByHandle`). A Codex App app-server has about 700 handles; one pass takes 3–10 ms. Asking each rollout who holds it instead (`FileProcessIdsUsingFileInformation`, the Restart Manager's `RmGetList`)
  took about 40 ms per file, seconds for a home with a few hundred rollouts. The scan runs every 30 seconds and again right before a resume.
- **The Codex App** (`codex_desktop_windows.go`): its bus is not `<CODEX_HOME>/ipc/ipc.sock` but one named pipe per user session, `\\.\pipe\codex-ipc` (the App's main process is `ChatGPT.exe`, package `OpenAI.Codex`).
  Frames and methods are the macOS ones. The pipe is looked for in the pipe namespace (`FindFirstFile`), never opened just to see it: `os.Stat` opens it, which takes one of the App's free pipe instances or connects.
  The pipe does not say which Codex home the App uses, so an App chat is one in a home with the App's `.codex-global-state.json`.
  A thread the App has not loaded is opened with `codex://threads/<id>` through `ShellExecute` (no console window), only once `HKEY_CLASSES_ROOT\codex` is a URL protocol with a handler: the package registers it as a packaged app's protocol, with no `shell\open\command`.
- **`personad codex-start`**: npm installs `codex` as `codex.cmd`, and a batch file has `cmd.exe` read its arguments again, the first message among them (quotes, `&`, `%`, line breaks). codex-start runs the `codex.exe` the shim would start
  (with the `CODEX_MANAGED_*` variables its `codex.js` sets); another batch file launcher is refused when there is a message.
- **app-server on `--listen unix://`**: Codex on Windows serves its control socket as an AF_UNIX file too. Windows has no mode bits, so the socket and its directory must be this user's, with an access list that lets nobody else in
  (SYSTEM and Administrators aside); a `unix://C:\…` endpoint is a path, not a URL with a host. Codex's background daemon (`codex app-server daemon start`) does not start from npm's launcher on Windows, so a plain `codex` there runs in its own process.

## Claude outside Persona's terminals

The desktop app's Code page and a `claude` in an ordinary terminal (Windows Terminal, PowerShell) run where neither herdr nor Orca has a pane. The node finds them by their `sessions/<pid>.json` and lists them live; a message from the phone goes through the session's peer inbox, never by starting a second `claude --resume` on the same transcript. Design and the protocol: [claude-integration.md](../integrations/claude-integration.md), "Claude outside Persona's terminals".

- **The inbox is a named pipe**, `\\.\pipe\LOCAL\cc-msg-<32 hex>`, dialed with `tailscale/go-winio`. On Windows it requires an auth line first: the token is in the key file next to `sessions/<pid>.json` (`<pid>.<sha256 of the pipe path, lower-cased>.key`), readable only by the user, as Persona's node is. Without it the session lists read-only, saying why.
- **The desktop app's entrypoint** is `claude-desktop`, or in some builds `claude-desktop-3p`: both are the desktop app (also in the "open elsewhere" refusal's words).
- **A terminal Persona drives is told apart by the claude process's environment**: a pane variable (`HERDR_PANE_ID`, `ORCA_PANE_KEY`…) read from its PEB, naming a pane an engine in use lists with a claude in it, means the engine lists it, so it is not listed twice.
- **Pid reuse**: Claude Code writes `procStart` as the process's creation time (a FILETIME); the node compares it with the running process's (`procinfo.StartMark`), so a reused pid is not taken for the claude.
- **Held messages**: the desktop app often runs sessions in bypass permissions mode, which holds a message from another sender until the person at the computer approves it in the session; the phone says the message is waiting there. `"crossSessionInbound": "accept"` in that Claude Code's settings delivers without asking.

## shell

When the node runs shell scripts on Windows, such as launcher probing and `claude --version`, it uses Git for Windows' bash (`loginShellProgram`: `CLAUDE_CODE_GIT_BASH_PATH`,
otherwise found from `git`'s location and common install directories), the same as Claude Code itself on Windows. It doesn't use a bare `bash`: in the PATH the service gets, `System32\bash.exe` comes first, and that's WSL.
If Git Bash can't be found, these commands don't start (the machine info has no Claude version); they never end up running in WSL.

## Verification record: Codex (2026-11-07, real Windows 11 machine)

- Codex App 26.1027 (MSIX `OpenAI.Codex`, its app-server a bundled `codex.exe … app-server` on stdio), Codex CLI 0.163 from npm (`codex.cmd`). No herdr on the machine; the node ran in tests only.
- The App's bus: `\\.\pipe\codex-ipc` is in the pipe namespace while the App runs; `go test -tags codexipclive -run TestCodexIPCLive ./internal/node/` (read only) connected, said `initialize`, and got
  `no-client-found` asking the owner of a made-up thread id. Nothing was sent to any conversation, and no `codex://` link was opened.
- The handle-table scan found the App's app-server holding 11 rollouts open in a few milliseconds per pass (`RmGetList` took about 40 ms per file, so it was not used).
- A test pipe (`TestCodexPipeUpDoesNotConnect`) showed `os.Stat` connecting to a pipe and `FindFirstFile` not.
- Tests on Windows: the fake App bus on a named pipe (`codex_desktop_windows_test.go`: initialize, owner discovery, start turn, a thread opened then retried, an image, no wait when `codex://` cannot open),
  the handle-table scan against a child holding a rollout open, the `codex.exe` scan against a copy of the test binary named `codex.exe` (its `CODEX_HOME`, herdr pane and `--listen`, and no other variable),
  a running CLI's thread neither resumable nor sendable, the private-socket check, npm's `codex.cmd` resolved to its `codex.exe`. On Linux, the whole `internal/node` package fails only where master fails on the same machine.
- Not verified: sending from the phone into a real App chat end to end (it would write into a conversation of the user's; see [codex.md](../integrations/codex.md), "Windows", for how to check it);
  opening an unloaded thread with `codex://` (it brings the App to the front); Codex in a real herdr on Windows (no herdr here); a CLI on a `--listen unix://` app-server
  (Codex's daemon does not start from npm's launcher on Windows: "this Windows launcher prevents background processes from outliving it").

## Verification record: Claude outside Persona's terminals (2026-11-07, real Windows 11 machine)

- Claude Code 2.1.308 (the desktop app's own) and 2.1.291 (the CLI on PATH). Read only: every running session's `sessions/<pid>.json` had `messagingSocketPath` (a pipe that exists), `peerProtocol: 1`, `peerFeatures` `notify_idle` / `artifact_yield`, and a key file whose name is the sha256 of the lower-cased pipe path; `procStart` equalled each process's creation time.
- End to end on a throwaway `claude` 2.1.291 (its own `CLAUDE_CONFIG_DIR`, an empty directory, a pseudo console; started for the test and closed after): the node's send was taken in 0.15–0.3 s and answered in the transcript ("reply with OK" → "OK"); a second message while it worked was queued and answered next; the transcript reader showed the message from "Persona" with only the user's words. With `--dangerously-skip-permissions` the message was held for approval in the terminal, as the code says.
- Tests: the peer path against a fake inbox on a named pipe (`claudepeer_test.go`), listing, refusals and the echo; Windows start marks (`internal/procinfo`).
- Not verified: sending into a session of the desktop app (only its files were read: no message was sent to a session in use); the phone and web UI against a real hub; macOS and Linux inboxes against a real claude (the code dials a unix socket; the tests run against a fake one on Linux).

## Verification record: tray (2026-11-07, real Windows 11 25H2 machine, build 26200)

- `persona-link tray` with a temporary `PERSONA_HOME` and nothing installed: the shell took the icon (tooltip "Persona Link · 未配对"), and the panel opened next to it on the taskbar's side, 12 px above the work area's edge.
  Inspected over CDP with Playwright (`PERSONA_TRAY_DEVTOOLS`): one CSS pixel per screen pixel at 100 %, the system backdrop on (no solid fallback), the system accent color, the Chinese UI, and no element outside the window.
- From the panel: opened the pairing window (centered on the screen); pasted links (preview of the hub, the fingerprint and the machine name); paired against an unreachable hub (the explained error with the raw one after it);
  on the passive tab, **Generate pairing link** (the link, the addresses, the firewall line; `link.json` and `link-tls/` written in the temporary directory); `tray --open <link>` from a second process (handed to the open window, which switched to the active tab with the link filled in);
  the sign-in switch (the Run entry written, then removed); **Quit**.
- Found and fixed on the way: pages rendered at half size (go-webview2 passes the rasterization scale wrongly), the pairing window never opened from the panel (WebView2 holds back callbacks while one runs), and the sign-in switch could stay showing its old state after a toggle while the panel was hidden. All three are in [lessons/windows.md](../lessons/windows.md).
- Every sample state, language and theme in Edge (Playwright): nothing outside the window and text contrast at least 4.5:1. This check found the Japanese tabs too wide for the pairing window, and the footer's version at 3.3:1 in light mode; both are fixed.
- Tests: `internal/linktray`, `internal/i18n`, the poke tests in `internal/link` and the watch tests in `internal/netaddr` passed on Windows; `cmd/persona-link`, `internal/linktray`, `internal/link` and `internal/netaddr` passed on Linux.
- Not verified: **Install background service** from the panel (it runs `persona-link install`, which would register this machine's scheduled task; `install` itself is covered by the tests and the 2026-11-04 record);
  the notifications (they need a link that connects and then drops); a real sign-in; the real tray in light mode, at other DPIs or with the taskbar on another edge (light mode and the layout were checked in Edge only); Windows 10 without WebView2.
  Known gap: without a version resource in the binary, the taskbar names the pairing window's button "persona-link.exe" rather than its title (release packaging, #1179).

## Verification record: herdr engine (2026-11-04, real Windows 10 machine)

- herdr 0.9.5 (official `herdr-windows-x86_64.zip`), Claude Code 2.1.311 (native `claude.exe`), Git for Windows 2.56 (PortableGit); herdr stays resident via a scheduled task that starts at logon,
  with the pane shell set to Git Bash. The hub was on a Linux machine, running this machine's `personad.exe node --stdio` over ssh.
- On the hub this machine was online: `windows/amd64`, engine herdr, herdr 0.9.5. Through the hub APIs: creating a session (`POST /api/v1/sessions`, with a first message) got a reply;
  a multi-line message was submitted as one and got a reply; asking Claude to call AskUserQuestion, the card reached the hub via the hook, and after answering with `POST /api/v1/interactions/{id}/resolve` Claude replied according to the answer;
  after `/exit` the session went offline, and on the next message the node started it with `--resume` in a new tab, and Claude remembered the earlier answer.
- `personad tab-start`: with the argv from a file, started `claude -p` (replied normally), `cmd /c exit 3` (exit code 3), and a command prefixed with `A=1` (the environment variable came through); the file is deleted after use.
- Tests: `internal/herdr` (`TestPipeName`, `TestSocketPathWindows`, and the RPC / subscription tests with the test herdr switched to listening on a named pipe) and `internal/osproc` (`TestExecExitCode`, `TestExecSurvivesCtrlC`: fails when switched back to `signal.Ignore`)
  passed on this Windows machine; the few tests that use shell scripts posing as the herdr executable are skipped on Windows. On Linux, `internal/herdr`, `internal/osproc`, `internal/node` and `internal/hub` passed.
- Not verified: the App UI (the same hub APIs the App calls were used); Codex (see "Verification record: Codex" below); herdr's live terminal (herdr doesn't support it on Windows).
## Verification record: install and start at logon (2026-11-04, the same real Windows 11 machine)

- Replaced the previously hand-added registry Run entry with `persona-link install --personad personad.exe`: the scheduled task registered and started, connected to the hub within seconds, and the local node came online;
  in the hub's `GET /api/v1/machines` this machine was online with engine Orca, and `GET /api/v1/sessions` had Orca's 10 panes.
- Processes: svchost (scheduled task) → `persona-link-service.exe supervise` → `persona-link.exe run`, `personad.exe node --attach`; each of the two children has a hidden conhost, and there is no window on the desktop.
- Killed persona-link: back after 5 seconds. Killed the node: back after 11 seconds. Killed the supervisor: within 2 seconds persona-link and the node were both gone; the next 5-minute time trigger brought them all back and the machine came back online on the hub.
- A probe task had confirmed earlier: a non-administrator can register an `InteractiveToken` task but not an S4U one; the GUI-subsystem copy and the children it starts with `CREATE_NO_WINDOW` have no console window
  and all run in the user session; `RestartOnFailure` does not restart a program that exits with code 1.
- Tests: `go test ./cmd/persona-link/` passes on Windows and on Linux (`golang:1.27` container); `supervise_windows_test.go` fails when the Job is removed (the child outlives the supervisor).
- Not verified: the logon trigger on an actual logoff and logon (that would close the Orca and Claude sessions in use on this machine; the logon trigger was accepted at registration); passive mode; the phone UI (the phone was locked; the same hub APIs the App calls were used; the App UI was checked during the second step below).

## Verification record (2026-11-03, real Windows 11 machine)

- Orca 1.4.242 (runtime protocol 3), Claude Code 2.1.318 (native `claude.exe`); the hub was this repository's Docker image (Docker Desktop), and the phone reached the hub's TLS port via `adb reverse`.
- `go test -tags orcalive -run OrcaLive ./internal/node/` (read-only): all 10 Claude panes in Orca were recognized, session ids matched `sessions/<pid>.json` one to one, one refresh took about 50 ms.
- Through the hub: list sessions and state, read transcripts, send a multi-line message (submitted as one), answer AskUserQuestion through a screen-reading card option, close a session (only the target terminal closed).
- Real-device App (Android): joined the test hub via **Settings › hub › Add Hub** (existing pairing untouched); Agents showed this Windows machine and the sessions above. From the phone: sent messages, answered
  AskUserQuestion twice by tapping options on the screen-reading card (the transcript shows the option the phone picked), closed the Agent from the session menu (the confirmation named the Orca tab, and only that one terminal closed). The first card couldn't read the dialog, which led to the fix in the "screen-reading card" item above.
- Linux (`golang:1.27` container): `orcart`, `osproc`, `plugin`, `transcript`, the whole `node` package, and the screen-reading card / Orca / engine / link / machine tests in `hub`: passed.
- On Windows, all tests in `orcart`, `osproc`, `transcript` and the Orca / attach / process-scanning tests in `node`: passed. A batch of tests in the `node`, `link` and `persona-link` packages hard-code
  Unix (shell scripts posing as executables, `/tmp`, permission bits) and fail on Windows; unrelated to this change, left for #1179.
