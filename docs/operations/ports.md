# Port forwarding: viewing pages an Agent serves, on your phone

An Agent starts a dev server on one of your computers (vite, next dev, `python -m http.server`…). One tap on the phone shows the page inside the App,
and hot reload keeps working. The reference is Browser preview in Moshi (an SSH / mosh terminal for iOS); we borrow only the interaction.

## At a glance

```
Phone App                             hub                                node (that computer)
WKWebView ─ http://localhost:5173 ─▶ local 127.0.0.1:5173 (App listens)
                                      │ one WebSocket per TCP connection
                                      ▼
                         GET /api/v1/machines/{mid}/ports/5173/connect (device token)
                                      │ opens a stream (stream.*) on the existing hub↔node connection
                                      ▼
                                                       connects to local 127.0.0.1:5173 (or the address it was bound to when scanned)
```

## Discovering ports (node, `server/internal/node/ports.go`)

- **Who owns it**: for each live Agent's pane, take the process the host reports (herdr's foreground process; the first one is claude / codex itself),
  walk all its descendants, and add the processes on the same terminal (tty) (including ones adopted by init after `npm run dev &` and the outer shell exiting).
  Listeners belonging to claude / codex themselves (IDE bridge, MCP) do not count.
- **How it scans**: on Linux it reads `/proc` directly (`/proc/*/stat` for the process tree; LISTEN sockets in `/proc/net/tcp{,6}` are matched to process fds by inode),
  with no child processes. On macOS it uses `ps -Ao pid=,ppid=,tty=,comm=` plus `lsof -a -p <these processes> -nP -iTCP -sTCP:LISTEN -Fpcn`.
  `lsof` exits 1 when one of the pids has exited / is a zombie but still prints results, so results are used as-is; when a scan really fails, the same error is logged only once.
  On macOS one scan costs about 0.17 s CPU (on a machine with about 3,700 processes, `ps` is most of it).
  Windows (#2098) also spawns no child processes: the process tree comes from a Toolhelp snapshot (`CreateToolhelp32Snapshot`), listening sockets and their owning processes come from iphlpapi's
  `GetExtendedTcpTable` (`TCP_TABLE_OWNER_PID_LISTENER`, one table each for IPv4 and IPv6), in `server/internal/node/ports_windows.go`.
  Windows has no controlling terminal, so ownership is by process tree only. After a parent exits, Windows still reports its pid, and pids get reused, so when the parent started later than the child
  (or its start time can't be read) the parent-child link is ignored, so a new process doesn't "adopt" someone else's children.
- **What counts**: for each `pid:port`, send one `GET /` (`Host: localhost:port`, which dev servers' host checks accept). Only ones that answer with HTTP are listed,
  and the home page's `<title>` (up to 60 characters) is captured along the way. One that accepts the connection but says nothing for 3 seconds (next dev compiling for the first time) is listed without a title and asked again on the next scan, up to 5 times.
  Ones that don't answer with HTTP (ssh, databases) are not listed. Results are cached per `pid:port` and dropped when the process or port goes away.
- **How often**: every 15 seconds while there is a live Agent. When an Agent's transcript changes (usually a tool ran; starting / stopping a server is one tool call)
  or a forward finds the port gone, an extra scan runs within 4 seconds. No Agents, no scanning.
- **How it is pushed**: results go in `Session.ports` and travel with the session list. If the port set (port, pid, title) hasn't changed, nothing is pushed (`listKey` includes it),
  so scanning adds no extra pushes (the lesson of #178: don't push what hasn't changed, don't make the UI recompute).

## Recognizing links in replies (App, `apple/Persona/Code/PortLinks.swift`)

- `http(s)://localhost|127.0.0.1|0.0.0.0|[::1]|*.localhost|this machine's hostname (with or without .local):port/path` all count as "a port + path on this machine",
  as does the scheme-less `localhost:3000/x` that servers often print. Links without a port, LAN addresses and other hosts don't count (LAN addresses open as ordinary web pages).
- "This machine" is the machine the session runs on (for assistant chats, the hub machine).
- These show up in: Agent replies (a row of badges under the message), Bash tool output (under the output once expanded), the top of the session (`Session.ports`),
  and when tapping such a link itself in a message or the terminal (`ChatActions.openLink` checks for port links first). These links are no longer preloaded (preloading would open the phone's own localhost).
- A link to one of the hub's own pages (`/s/…`, `/todos`, `/persona/…`; a hub on the agent's machine) is no dev server: it gets no badge and opens as that page (#2696).
- The web (#504) recognizes the same links (`web/src/lib/port-links.ts`) and draws the badges under an agent's finished answer (`views/chat/PortChips.tsx`): `:5173` with the page's
  title, "Stopped" and dimmed once nothing listens there. A port some agent on that machine lists (`Session.ports`) is live; any other is asked about with `GET …/ports/{port}`
  (`lib/port-presence.ts`: a live answer is kept a minute, a gone one 30 s, no answer 20 s). A badge, or the link itself, opens the page in the **Preview** panel ([preview.md](../web/preview.md)),
  only on a hub with `preview` and for an agent's session with a working folder; assistant conversations get none.
## Forwarding

### Why "listen locally on the phone"

| Option | Problem | Verdict |
| --- | --- | --- |
| One subdomain per port (`p5173-mac.<hub domain>`) | Needs wildcard DNS and a wildcard certificate, so DNS has to change | Rejected (a change the user would have to sign off on) |
| hub opens a separate port per forward | New public ports, each one has to be allowed through the firewall | Rejected |
| Path prefix + rewriting (`/api/v1/…/ports/5173/…`) | SPA absolute paths (`/@vite/client`, `/_next/…`), the HMR ws address, Cookie Path and redirect Location all need rewriting; rewriting HTML / JS is unreliable | Rejected |
| **The App listens on the phone's 127.0.0.1 / ::1, one WebSocket to the hub per connection** | Only the App (iOS, Mac) can do it, not the web version (browsers can't listen) | **Chosen** |

Same as Moshi: on the phone the page is `http://localhost:5173/`, exactly as on the computer. Bytes are forwarded untouched, so HTTP / WebSocket (HMR) /
redirects / cookies need no rewriting. No DNS changes, no new ports: it uses the hub address and device token the App already has.

### Phone (`apple/Persona/Code/PortForwarder.swift`)

- One forward per (machine, port): first bind the same port number on `127.0.0.1`; if it's taken (another forward on the phone, or an existing program on the Mac running the simulator), pick a random one.
  Then bind the same number on `::1` (`localhost` resolves to ::1 first; without it WebKit falls back to 127.0.0.1 anyway). Only loopback is bound, so nobody on the LAN can connect.
- Each incoming connection opens a `wss://<hub>/api/v1/machines/{mid}/ports/{port}/connect` (`Authorization: Bearer <device token>`).
  Each side "finishes sending the previous chunk before reading the next", so a slow side holds back a fast one instead of piling up in memory.
- The listener stays up. If it breaks after the App is suspended, it is rebuilt on next open (reusing the old number when possible; the page's HMR reconnects by itself).
- Before opening, ask the hub: if the port is in some Agent's `ports`, open it directly; otherwise `POST …/open` (the user explicitly tapped it). If nothing is listening, show "Nothing is listening on port :N anymore",
  and the badge turns gray.
- The web card is the same one used for ordinary links (`LinkSheet`), with "machine name :port" under the title bar. Its menu has no **Open in Safari** or **Share**
  (an address on the phone's loopback means nothing to other apps). Dev server pages skip the disk cache (`reloadIgnoringLocalCacheData`). A port page's URL is the same every time, so `WebViewPool` hands back the previous WKWebView:
  it is reloaded when taken out (unless it is still loading), otherwise you'd see the page as it was when last closed (2026-11-05, #1934). The refresh button in the top bar (`link-reload`) uses `reloadFromOrigin`
  to fetch again; native Lark pages don't have it.
- The Mac App uses the same code; the sandbox needs `com.apple.security.network.server` (listening on loopback only).
- Android (#2144): `core/hub/PortForwarder.kt` likewise listens on 127.0.0.1 and ::1 with one WebSocket per connection; see `docs/android/android.md` ("Terminal and links").

### hub (`server/internal/hub/ports.go`) and streams (`server/internal/streams`)

- `…/connect` checks first, then upgrades: the port must be in the `ports` of some session on that machine, or the user must have `POST …/open`ed it within 24 hours; otherwise 403.
  Then it registers a stream and asks the node to `port.dial`. If that fails it returns 404 (not yet upgraded, so the App gets the status code); only on success does it upgrade to WebSocket.
- A stream is just `stream.data / stream.ack / stream.close` events on the hub↔node JSON-RPC connection (`docs/protocol/protocol.md`). Each direction allows at most 512 KB
  unacknowledged; at that point the sender waits. A large file from a page can't fill the connection and starve other RPCs, and a slow reader can't make the other end buffer without limit.
  Event handling (`Mux.Event`) never blocks, because it runs in the RPC read loop.
- At most 64 streams per machine at once (a page typically uses about 6); when a machine disconnects all its streams are closed.
- The slow-request log skips `…/connect` (it is long by nature).

## Security boundary

- Auth is the same as other APIs: device token (or pairing token). Read-only devices (the watch) always get 403, even for an Agent's port.
- Only ports on **the configured machine itself** can be reached: the node only dials the address that listener was bound to when scanned (a local one) or 127.0.0.1 / ::1, and accepts no hostnames. It is not a proxy that can open arbitrary intranet addresses.
- The port must belong to an Agent (found by the node's scan) or have been tapped open by the user in the App; guessing a port number gets 403. Tapped-open ports are kept in hub memory for 24 hours
  and cleared on hub restart (just tap again).
- Probing (`GET …/ports/{port}`) only reports "is something listening, is it HTTP, title"; it grants no access.
- Exception: when the `cua` plugin is on for a Mac, that Mac's 5900 (macOS Screen Sharing) is permanently allowed, for "VNC" (see [computer-use.md](../extensions/computer-use.md), "Live view"); the WebSocket accepts the `binary` subprotocol (noVNC may send it).
## Not done yet

- The web version can't use it (see the trade-off above); it shows a dev server through a headless browser on the computer instead ([preview.md](../web/preview.md)).
- https dev servers: bytes are forwarded, but WKWebView rejects their self-signed certificates, so the page won't open.
