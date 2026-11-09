# node on Windows: records

Moved unchanged from the [Windows guide](../../operations/windows.md), which keeps the status table and the design.
These are the checks on real Windows 10 and 11 machines (2026-11-03 to 2026-11-07): what ran, what was found, and what each could not cover.

## Verification record: Codex (2026-11-07, real Windows 11 machine)

- Codex App 26.1027 (MSIX `OpenAI.Codex`, its app-server a bundled `codex.exe … app-server` on stdio), Codex CLI 0.163 from npm (`codex.cmd`). No herdr on the machine; the node ran in tests only.
- The App's bus: `\\.\pipe\codex-ipc` is in the pipe namespace while the App runs; `go test -tags codexipclive -run TestCodexIPCLive ./internal/node/` (read only) connected, said `initialize`, and got
  `no-client-found` asking the owner of a made-up thread id. Nothing was sent to any conversation, and no `codex://` link was opened.
- The handle-table scan found the App's app-server holding 11 rollouts open in a few milliseconds per pass (`RmGetList` took about 40 ms per file, so it was not used).
- A test pipe (`TestCodexPipeUpDoesNotConnect`) showed `os.Stat` connecting to a pipe and `FindFirstFile` not.
- Tests on Windows: the fake App bus on a named pipe (`codex_desktop_windows_test.go`: initialize, owner discovery, start turn, a thread opened then retried, an image, no wait when `codex://` cannot open),
  the handle-table scan against a child holding a rollout open, the `codex.exe` scan against a copy of the test binary named `codex.exe` (its `CODEX_HOME`, herdr pane and `--listen`, and no other variable),
  a running CLI's thread neither resumable nor sendable, the private-socket check, npm's `codex.cmd` resolved to its `codex.exe`. On Linux, the whole `internal/node` package fails only where master fails on the same machine.
- Not verified: sending from the phone into a real App chat end to end (it would write into a conversation of the user's; see [codex.md](../../integrations/codex.md), "Windows", for how to check it);
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
- Found and fixed on the way: pages rendered at half size (go-webview2 passes the rasterization scale wrongly), the pairing window never opened from the panel (WebView2 holds back callbacks while one runs), and the sign-in switch could stay showing its old state after a toggle while the panel was hidden. All three are in [lessons/windows.md](../../lessons/windows.md).
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
