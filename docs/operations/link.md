# Persona Link: the computer connects to the hub, or the hub connects to the computer

> Naming (#1159): the connection helper is **Persona Link**, and the command is `persona-link`. The installer creates `persona-link` beside `persona-link` as an alias; both names run the same program,
> and its prompts use whichever name you ran. The executable `~/.persona/bin/persona-link`, download name `persona-link-<os>-<arch>.gz`,
> systemd unit `persona-link.service`, launchd identity and helper inside the Mac App keep their old names, so this page still says `persona-link` when referring to the program below.
> Copyable commands from the hub and admin page also still use `persona-link`, because the hub doesn't know that computer's version, and older versions have no `persona-link`.

The hub can't reliably SSH into computers such as laptops whose IP and network keep changing. Requested 2026-10-31: no Tailscale or reverse tunnels;
instead, the client on the computer connects to the hub and reconnects on its own after a disconnect, and the hub reuses that link to talk to its node. No SSH or VPN is needed anywhere along the path.

The user decided again on 2026-11-02 ([plan-hub-center.md](../archive/operations/plan-hub-center.md), sections 0 and 4): hubs and computers always connect through a preinstalled service on the computer, not SSH (too much permission);
the computer can connect to the hub or accept a connection from it. The recommendation is one hub for the whole household, with a **node service** on every other computer: resident node plus persona-link, installed by one command or one App.

## Two modes

The same persona-link supports both, selected during pairing and changeable anytime (`persona-link mode active|passive`, or the pairing window of the Mac menu bar App or the Windows tray). Each mode stores its pairing separately, so switching back doesn't require pairing again.

| Mode | Who connects to whom | Best for | Requirements |
| --- | --- | --- | --- |
| **Active** (connect to the hub) | persona-link dials the hub's WSS `/api/v1/link` ("Link" below) | Laptops, computers behind NAT or frequently changing networks | The hub has a TLS port (`public.tls_listen`) or sits behind an HTTPS reverse proxy ("[Hub behind a reverse proxy](#hub-behind-a-reverse-proxy)" below); this computer can reach the hub |
| **Passive** (let the hub connect) | The hub dials the TLS port persona-link listens on locally ("Passive mode" below) | Computers with stable addresses reachable by the hub: servers, development workstations, home desktops | The hub can reach this computer's port (directly or through a SOCKS proxy); the computer's firewall opens this port only to the hub |

Choosing: the computer can reach the hub but the hub can't reach it (a laptop, a computer on a company intranet connecting to a home hub) → active; the hub can reach it but it can't reach the hub (the hub is somewhere unreachable from the public internet,
but can enter the computer's network through a proxy), or you don't want the computer initiating outbound connections → passive. If both directions work, either is fine; active needs no open port and is simpler.

## Install the node service

Where to get a pairing link: preferably from **Machines › Add Computer** on the signed-in web app (iOS and Android entry points are in progress). It provides a full copyable command or link for the selected OS;
after the computer connects, confirm **This is my computer** on the same screen ("[Connecting a computer](#connecting-a-computer-mac-example)" below). The admin interface and command-line steps on the hub machine are in that section's "Advanced" subsection.

**Linux** (one command, the "node" role in `scripts/deploy/install.sh`):

```sh
curl -fsSL <site>/install.sh | sh -s -- --node --pair '<pairing link>'   # Active
curl -fsSL <site>/install.sh | sh -s -- --node --listen [:18663] [--addr <address the hub uses to reach this computer>]         # Passive
```

- Downloads persona-node, the device's own program ("[The device's program](#the-devices-program)"), and persona-link (both verified against `SHA256SUMS`) to `~/.persona/bin/`, then hands off to `persona-link install`: installs the resident node's systemd user service
  `personad-node` (running persona-node; one that is there already is left as it is, then switched with `persona-link node switch`) and persona-link's own service, pairs, and waits for connection (passive mode: waits for the listener to start, then prints a link for the hub admin interface).
  A release without persona-node, or a computer that runs a hub or a manager, gets personad instead, as before.
- `--pair` / `--listen` imply `--node`; so do `PERSONA_ROLE=node`, `PERSONA_PAIR`, `PERSONA_LISTEN`. With no options and a terminal, it asks;
  if a hub is already detected on the LAN (mDNS `_persona-hub._tcp`, `persona-link discover`), it first says "One hub is enough for the whole household; install the node service on this computer."
- Without systemd, it still installs the binaries and prints alternative instructions (see "Linux (headless)").
- On Mac, `--node` only installs the resident node's launchd agent and pairs; the menu bar App handles the connection.
- Publishing: `scripts/publish.sh` builds four extra `persona-link-<os>-<arch>.gz` and `persona-node-<os>-<arch>.gz` files alongside personad and adds them to the same `SHA256SUMS`.

**Mac**: install the **Persona Link** menu bar App (`./scripts/app/mac-link.sh install`; a step-by-step guide for others is in [persona-link-install.md](persona-link-install.md)). The App includes persona-node (`Contents/Helpers/persona-node`), and **on first launch** runs
`persona-link node install --node-bin <inside the App>` to install the resident node LaunchAgent `com.persona.node`: when there is no agent yet, it copies persona-node to `~/.persona/bin/persona-node` (unless one is there) and the new agent runs it (on a Mac that runs a hub or a manager, their personad);
an existing agent is left unchanged (for example one from `scripts/deploy/node-launchd.sh install`), and one that runs personad moves over with `persona-link node switch`. Afterwards it only offers **Install local node service** in the menu; a service the user deletes isn't reinstalled automatically.
The agent it writes has `ProcessType=Interactive` (the same as `node-launchd.sh`), so launchd doesn't throttle the node like a background job. Since an existing installation is left unchanged, one written before #5944 lacks it: `personad node doctor` reports it and `personad node doctor --fix` adds it (docs/server/server.md "Scheduling and privacy on the Mac").
App updates don't replace the installed resident node. Nodes supporting the update protocol can be remotely updated by the hub over the existing connection, with backup, rollback and online confirmation; first-time local upgrades for old nodes, progress and installation boundaries are in [node-update.md](node-update.md) (#1158).

**Windows** (Orca engine, [windows.md](windows.md) ("Install and start at logon")): `persona-link install --personad <personad.exe> [--pair '<link>']`, without admin privileges.
Registers the current user's scheduled task **Persona Link**, starting at sign-in and checked every 5 minutes; it runs a windowless supervisor (`persona-link supervise`), which starts
persona-link and the resident node and restarts them on exit. Both are in one task (no separate `node install`). It also installs and starts the tray icon ([windows.md](windows.md), "Tray"; `--no-tray` skips it). There is no one-command installer script or installation package (#1179).

### The device's program

A device runs persona-node (`server/cmd/persona-node`), the node and the commands its Agents run (hook, MCP, ask, notify, launch, …) without the hub or the manager, at `~/.persona/bin/persona-node`. A hub or a manager runs personad at `~/.persona/bin/personad`. The two are separate files, so a computer that has both keeps both.

- **New node services run persona-node** where the computer has it: `persona-link node install` (and `persona-link install`, the Mac App) picks `bin/persona-node`, else `bin/personad`. On a computer that runs a hub or a manager (its data directory has `hub.json` or `managers/*/manager.json`) the node stays on personad: their services run personad too, and it updates with the node.
- **Existing node services keep running personad** until they are switched. `persona-link node switch` (run by `install.sh --node`) does it whole or not at all, holding a lock (`~/.persona/node-switch.lock`) the whole time:
  1. It keeps the service file as it is (`~/.persona/node-switch.backup`, written and synced to disk first).
  2. It points the service at `bin/persona-node`. Only the program changes; the file's other settings stay. When the computer has no persona-node yet but persona-link came with one (the Mac app's `Contents/Helpers/persona-node`), that one is put in place first.
  3. It restarts the service (systemd `daemon-reload` and `restart`; launchd `bootout` and `bootstrap`, the latter tried again for a few seconds). On a Mac it runs only in the desktop's session (`launchctl managername` is `Aqua`, as in Terminal on the Mac); over ssh it is refused before anything changes, and so is undoing an unfinished switch, because launchd there may take the agent away and then refuse to load one again. The node stays on the program it runs.
  4. It waits up to 60 s for the node to answer `ping` on `node.sock` as persona-node, then drops the backup.

  When persona-node does not answer, or the switch is stopped (Ctrl-C, TERM, the terminal closing), the old file comes back, the service restarts on personad, and the command says why. The backup goes only once the service manager has taken the restart (or a node answers all the same): when launchd refuses the old agent's `bootstrap` for longer than the retries, it stays, and the next recovery starts it again. A switch cut off harder (`kill -9`, the computer losing power) leaves the backup: `persona-link node status` says so, and the running `persona-link run` (it looks every 30 s and when it starts, and writes what it did to `link.log`) or the next `persona-link node install`/`switch` puts it back; `persona-link node uninstall` drops it with the service. None of that touches a switch that holds the lock (`node install`, `persona-link install`'s node step and `node uninstall` refuse while one runs), and a backup that is no usable service file (empty, or naming a program that is not there) is dropped rather than put back. `bin/personad` is never touched.
  The switch restarts the node at once: an answer the assistant is writing on that computer at that moment is cut off (Agents in herdr go on).
- **Back to personad**: `persona-link node switch --to personad` does the same the other way. `install.sh --hub` and the manager's install script run it when the node service file names `persona-node` (whoever wrote it, the Mac app included), because a computer that runs a hub or a manager keeps its node on personad, which updates with the node. `personad hub setup` and `personad manager install` run directly do not: run the switch yourself there. A manager joined into a directory outside `~/.persona/managers/` is not seen as one.
- The node writes the hook and MCP configs of the sessions it starts with its own path at every connection, so after a switch they run persona-node, and after a rollback personad again.
- `node.info` says which program runs the node and from which file (`program`, `executable`, [protocol.md](../protocol/protocol.md) "Node handshake"); Redeploy sends the device's file persona-node of the build it should run (the hub's own, or the release's, [node-update.md](node-update.md) "Node packages"), else personad to that same file, which persona-node replaces again later. `node-update-check` prints `program` too, which is how scripts tell the two apart. `persona-link node install --node-bin <persona-node>` (and `persona-link install --node-bin`) puts the device's program in beside a personad the computer may have (unless it runs a hub or a manager, where it is refused).
- The office skill the node gives its sessions runs `office` with whichever program is there (personad, else persona-node).
- Windows has no persona-node build yet: its node runs personad.

**On the hub side**: active-mode computers are kind `link` in hub.json; passive-mode ones are kind `service` (the hub side implemented in #435; paste the link in admin **Computers › Add Computer › Hub connects**).

## Responsibilities

- **persona-link** (`server/cmd/persona-link`, also reachable as `server/cmd/persona-link`; `server/internal/link`): a separate small program doing only three things:
  - Discovery: find the hub among candidate addresses and connect;
  - Reporting: identify the computer, version and address used;
  - Link: maintain the connection and forward streams opened by the hub unchanged to the local resident node.

  It implements no specific feature.
- **node is unchanged**: all features live in `personad node`. This computer needs a resident node (installed together under "Install the node service" above; separately, use `persona-link node install`
  or `scripts/deploy/node-launchd.sh install`); persona-link forwards streams only to its socket `~/.persona/node-attach.sock`. This is the same path that SSH-started `node --stdio` forwarded to
  (`internal/node/attach.go`). Writing a systemd / launchd service for the resident node is packaging, not functionality: the service contents match those written by `scripts/deploy/node-launchd.sh`.
- **hub and manager** (since 2026-11-04 computers belong to the manager, [machines.md](../server/machines.md), "Who connects to computers"):
  - This computer is kind `link` in hub.json; nobody SSHs into it;
  - Persona Link still connects to the hub address; after authenticating, the hub forwards the connection unchanged to the manager (`devices.accept` + `GET /api/v1/bus/relay`, preserving individual messages and close codes);
    the manager speaks the protocol below;
  - As soon as the link says hello, the manager opens a stream to connect to the node (streams Persona Link opens are also relayed through the hub); the node protocol on the stream is exactly the SSH protocol;
  - When the link disconnects, this computer goes offline immediately; while the manager is away the hub returns 503 on `/api/v1/link`, and Persona Link backs off normally and connects when the manager returns.
- **Mac** uses the **Persona Link** menu bar App (`apple/PersonaLink/`), with persona-link packaged as a helper and started and supervised by the App.
  On **Linux** (servers, NAS, Raspberry Pi, no GUI), `persona-link install` installs itself as a systemd user service; see "Linux (headless)";
  on **Windows**, it installs as a sign-in scheduled task supervising both it and the resident node, plus a tray icon that does what the Mac App's menu does; see [windows.md](windows.md), "Install and start at logon" and "Tray".

## Link (active mode)

```text
persona-link ── WSS /api/v1/link ──▶ hub TLS port (public.tls_listen)
  → hello {machine, version, address}          First message; hub closes with 4003 if machine id doesn't match credentials
  ← endpoints {endpoints, hub_name, mesh_hubs, ping_ms?, ping_timeout_ms?, pending?, machine_name?}
                                               Sent on connect and again when hub addresses, linked group, heartbeat, pending status or this computer's name changes
  ← open {id}                                  hub wants to connect to node
      persona-link connects ~/.persona/node-attach.sock first, then opens WSS /api/v1/link/stream?id=<id>, forwarding bytes unchanged both ways
  → open_failed {id, error}                    Can't open (resident node not running, etc.); hub records the reason as this computer's error
  ← node {online, error}                       Whether hub connected to node, displayed in the menu bar
  → rename {id, machine_name}                  Asks the hub to call this computer machine_name ("": its id again); see "The computer's name"
  ← renamed {id, machine_name | code, error}   The hub's answer to that rename: the name it has now, or why not
```

**Route selection**
- Candidate addresses are ordered "LAN → public IPv6 → domain → public IPv4", with manually added addresses first within their category.
- Starts trying the next candidate every 300 ms, several concurrently; the first connection wins and late successes are closed. Each candidate has an 8-second timeout.
- Away from home, even if LAN addresses fail or hang, it waits only another 300 ms before trying the next. These are configured values, not measured results.

**Hub name and linked group**
- The hub includes `hub_name` in `endpoints`: `name` from hub.json, otherwise the short hostname of the hub machine; in Docker (where the hostname is a container id), `hub-<first 6 characters of hub_id>`
  ([config.md](config.md)); and `mesh_hubs`: the number of hubs in its linked group, including itself. Before linked hubs (#294) landed, this was fixed at 1, returned by `meshHubCount()`
  and wired up by the mesh lane; when the group changes the hub sends `endpoints` again (`sendLinkEndpoints`).
- persona-link saves the name as `hub_name` in link.json, so it knows which hub even when disconnected; status includes `hub_name`, and `mesh_hubs` while connected.
- The name is restricted to one printable line (control characters become spaces), up to 80 characters.
- Old hubs send neither: neither field appears in status, the Mac menu stays as before, and `persona-link status` displays the hub id. An `endpoints` message containing a name but no addresses does not clear candidate addresses.

**Pending confirmation** (#4655)
- A computer added through **Add Computer** on the phone / web is pending until the user taps **This is my computer** (`hub/machine_trust.go`). The hub hands pending machine ids to the manager (`pending` in `devices.config`),
  and the manager's `endpoints` to that computer's link carries `pending: true`; once confirmed (or rejected or expired), the hub hands over the config again and the manager sends `endpoints` without `pending`.
- `endpoints` is per machine: every message describes that computer's current state; absent `pending` means confirmed. persona-link updates `pending` in its status line accordingly.
- Old hubs don't send it: status omits `pending` and shows confirmed (Persona Link's status page already handles this in #4198). Passive mode (hub connects to computer) doesn't have it yet.

**The computer's name**
- The hub calls each computer by its hub.json `name` (its id when it has none). The id never changes: pins, mutes, trust and credentials are all by id; only the name is shown.
- Hubs that rename computers (hello feature `machine_rename`) say the name in `endpoints` (`machine_name`) and send `endpoints` again when it changes. persona-link keeps it as `machine_name` in link.json,
  shows it in its status (`machine_name`), in `persona-link status` (with the id in parentheses when they differ) and in the tray / menu bar. Old hubs say nothing: everything shows the id as before.
- It can be renamed from either end, and every side follows without a restart or reconnect:
  - On the computer: `persona-link name <new>` (`--reset`: back to the id; no argument: shows it), the Windows tray's **Rename this computer…**. They ask the running persona-link, which sends `rename` on its link;
    the manager asks the hub (`devices.rename`), the hub writes hub.json and answers, and persona-link reports the answer (`rename` in its status, by the request's id). The request is a file beside link.json
    (`link.rename.json`), taken within half a second; one older than 30 seconds is dropped.
  - On the hub: the admin page (`PUT /api/v1/admin/machines/{id}`) or a signed-in device (`PUT /api/v1/machines/{id}/name`); see [protocol.md](../protocol/protocol.md) "Computers".
- Only a confirmed computer renames itself: one waiting for the user to confirm it (`pending`) is refused (`pending`), so it can't pass itself off as another computer by name. Passive mode has no link to ask on:
  rename it on the hub; the hub's info reads carry the name (`X-Persona-Machine-Name`, URL-escaped) and the connector keeps it in link.json's `passive.machine_name`.
- Pairing hands the hub what this computer's system calls it (`system_name` with the pairing request: the Mac's Computer Name, else the host name). The hub names a computer after it once, when the computer has
  no name of its own yet (added without one, so its name is its id); a name it was given, or one the user set later, is kept. Nothing renames the computer itself.
- Names are one line, at most 40 characters (the hub refuses longer ones with `invalid_name`).

**TLS**
- Pins the hub public-key fingerprint received during pairing (SHA-256 of SubjectPublicKeyInfo); LAN connections also use TLS.
- The hub certificate is self-signed; trust comes from this fingerprint, just like the App's public direct connection ([network.md](network.md), "TLS and public-key pinning").
- If the pairing link has no fingerprint (the hub doesn't serve TLS itself and sits behind an HTTPS reverse proxy), validate the proxy certificate against system-trusted CAs and the hostname; see "[Hub behind a reverse proxy](#hub-behind-a-reverse-proxy)".
  Pairings with a fingerprint accept only the pinned public key and never fall back to system CAs.

**Reporting**
- The first message after connecting is hello, reporting this computer's machine id, persona-link version and chosen address.
- The manager checks that the machine id matches the computer bound to the credentials (the hub includes that binding when forwarding), then immediately opens a stream to the node without waiting for the SSH retry interval.
- This computer gets an extra `link` field in the machine list: `{version, address, since}`.

**Heartbeat**
- Both sides ping every 10 seconds and disconnect after 5 seconds without pong. Dead links are detected in about 15 seconds.
- Slow links can be configured in hub.json's `link` (`ping_seconds`, `ping_timeout_seconds`, [config.md](config.md)): the hub pings accordingly, and when configured the manager includes
  `ping_ms`, `ping_timeout_ms` in `endpoints`; Persona Link adopts them without reconnecting. If absent (old hub, unconfigured) or not both positive, it uses its own values. New connections use their own values until the hub speaks.
  Both sides clamp them between 50 milliseconds and 10 minutes (`link.PingTimes`). Older Persona Link versions don't recognize these fields and retain 10 seconds / 5 seconds.
- Ping queues behind data already being sent on the connection: at only tens of KB per second from hub to computer, a 1 MiB block takes over 30 seconds; any shorter timeout guarantees disconnects, repeating after reconnect.
- Two deadlines heartbeat tuning doesn't control (`docs/lessons/server.md`, "Heartbeat timeouts on slow links"): coder/websocket waits at most 5 seconds for the write lock when sending ping / pong, so it still disconnects while a message larger than the send buffer is being written;
  the hub relay waits at most 1 minute to write one message (`spliceWS`), so a 1 MiB block still times out below roughly 17 KB/s. The hub's link-stop reason includes the ping error to distinguish these cases.
- The hub also pings streams, detecting when the link is alive but a stream's TCP connection silently died.
- Ping runs between hub and Persona Link (the hub owns this end of the connection): without pong, the hub closes it, and the relay to the manager closes with "no pong within …"; the manager treats that as a disconnected link.
  The hub reports each pong round-trip to the manager (`devices.link_rtt`), which supplies the path distance for routing and records sent to the primary hub.
- These are configured values, not measured results.

**Reconnect**
- Exponential backoff: starts at 1 second, doubles up to 1 minute; randomized between half and full delay each time. After a link connected for a full minute disconnects, it starts over.
- Special cases:

  | Case | Wait |
  | --- | --- |
  | Replaced by a new link from the same computer (4001) | 30 seconds |
  | Credentials rejected (401 / 403) or revoked (4002) | 5 minutes, or retry immediately when link.json changes (re-pairing) |
  | Hub restarts (1001) | Normal backoff |
  | Manual **Reconnect now** | No wait |

- Only one persona-link can run per computer (`~/.persona/link.json.run` file lock), or they replace each other repeatedly.

**Network changes**
- Mac: the App uses `NWPathMonitor` and sends helper `SIGUSR1` on a network change:
  - If the link's original local address is gone, reconnect immediately;
  - If still present, ping immediately (3 seconds, or the hub-configured heartbeat timeout if longer), reconnecting on failure;
  - If already disconnected, retry immediately.
- Linux: persona-link watches address changes itself with netlink (`internal/netaddr`); Windows: with `NotifyIpInterfaceChange` (same package).
- **Reconnect now** is `SIGUSR2`. Windows has no such signals: there the two are named events the tray sets ([windows.md](windows.md), "Tray"). Waiting for pong blocks neither this nor the network-change check: on a slow link, ping may wait the hub's entire configured timeout.

**Forwarding**
- Only forwards to `~/.persona/node-attach.sock` and only connects back to the hub address used by this link; stream ids may contain only letters, digits, `-`, `_`. The hub can't make it connect elsewhere.
- Streams enable permessage-deflate compression. SSH previously used `Compression=yes`; see the comment in `devices/machine.go` (the hub-to-manager hop is local loopback and isn't compressed).
- To the resident node, each stream is a new attach connection, replacing the previous one just as with SSH.

## Hub behind a reverse proxy

When the hub doesn't serve TLS itself and is mounted under a reverse proxy's subpath (`public_url` is `https://<domain>/<prefix>`, the proxy has a public-CA certificate and strips the prefix before forwarding to the hub's plaintext port),
the phone App and web already work ([network.md](network.md), "Reverse-proxy subpaths (Android)"), and active-mode Persona Link works too (#2573):

```text
persona-link ── WSS https://<domain>/<prefix>/api/v1/link ──▶ reverse proxy (TLS, public CA) ── strip /<prefix>, plaintext ──▶ hub listen port
```

- **Pairing**: without hub TLS, `personad hub pair --machine <id>` (and **Pairing link** in admin) still returns a link if `public_url` is `https://…`: its `url` is `public_url`,
  with no `fp` or `tls`. persona-link accepts this link, preserving the path prefix and spelling out the port (`https://<domain>:443/<prefix>`, matching candidates sent by the hub);
  link.json stores `"trust": "system"` and no fingerprint. A link with no `fp` and no `https://` `url` is rejected (no usable address).
- **Validation**: every candidate in this pairing is checked against system-trusted CAs and the hostname (TLS 1.2 or later, `http/1.1`), like opening the web app in a browser. Pairings with `fp` still accept only the pinned public key:
  if the proxy certificate doesn't match, the error is "TLS public-key fingerprint differs from pairing", with no fallback to system CAs.
- **Link**: `/api/v1/link`, `/api/v1/link/stream` and pairing's `/api/v1/auth/tokens` are appended after the prefix; heartbeat, reconnect backoff and routing remain as above. The hub's candidates already include the prefix in `public_url`.
- **Hub side**: requests arriving through the proxy are plaintext; the hub accepts links only in these two cases (credentials must still match):
  - Proxy and hub are on the same machine (loopback source);
  - Proxy is on another LAN machine (private source address), and forwarding headers say it received HTTPS: `X-Forwarded-Proto: https` (or `Forwarded: proto=https`).

  Plaintext from public sources is always rejected (`403 tls_required`), whatever the headers; other entry points (App APIs, bus, mesh) are unaffected. If the hub runs in Docker and port mapping makes the source appear as a container gateway, it is treated as public;
  add the gateway to `security.trusted_cidrs` ([network.md](network.md), "Source classification and reverse proxies"). Forwarding headers can be spoofed: on the LAN, spoofing only lets one's own link credentials travel in plaintext; Persona Link itself never sends plaintext.
- **Status**: `persona-link status` adds "Validation: via reverse proxy · system certificates"; `trust` in `--json` is `system` (`pin` for pinning); the running status line carries `"trust": "system"`;
  the menu bar App shows "Via reverse proxy · system certificates" below the connection method, and the pairing window explains it above the address list. Untrusted certificates and hostname mismatches report "Certificate not trusted by the system" and "Certificate hostname doesn't match" respectively.

nginx example (replace placeholders; put `map` in `http { }`, and `location` in the port-443 `server { }`):

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

location /<prefix>/ {
    proxy_pass http://<hub address>:<hub listen port>/;   # Trailing / strips the prefix
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;              # WebSocket: /api/v1/ws, /api/v1/link, /api/v1/link/stream
    proxy_set_header Connection $connection_upgrade;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;            # Allows a plaintext link when proxy is on another LAN machine
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;
    proxy_read_timeout 1h;                               # Long connection: exceed heartbeat (default 10s, configurable in hub.json link) with margin
    proxy_send_timeout 1h;
}
```

**Trade-off**: pinning trusts one hub public key; system trust trusts every CA in the system. Any CA that signs for this domain (a compromised or mistaken CA, or device management able to install roots on this computer) can impersonate the hub,
obtain this computer's link credentials and connect hub streams to its resident node. This is the same as accessing the web app in a browser: better than plaintext, weaker than pinning. The proxy-to-hub hop is plaintext and must be on a trusted network (same machine or LAN).
Prefer the hub's own TLS and fingerprint when possible; when hub TLS is enabled, pairing links always carry `fp` and won't use system trust.

## Passive mode (the hub connects)

The protocol is fixed in [plan-hub-center.md](../archive/operations/plan-hub-center.md), section 4; details are here. The dialing side (#435) follows this specification and has belonged to the manager since 2026-11-04
(`server/internal/devices/service.go`; credentials generated during pairing are handed to the hub for encrypted storage with its key and retrieved when dialing). Implementation: `server/internal/link/`
(`listen.go` for the listener and three endpoints, `passive.go` for pairing codes and links, `tlsid.go` for identity, `ratelimit.go` for rate limiting); CLI in `server/cmd/persona-link/passive.go`.

### Listener and identity

- Listens on `passive.listen` in link.json, default `:18663` (all addresses); change with `persona-link mode passive --listen <address>` or `pair --listen <address>`. Only this one port is opened.
- TLS 1.2 or later, ALPN only `http/1.1`. Identity is the helper's own ECDSA P-256 key and self-signed certificate in `~/.persona/link-tls/` (directory 0700, `link.key` and `link.crt` 0600),
  generated on the first `pair --listen` or first listener start. The certificate contains only `connector.persona.invalid`, no hostname or addresses (anyone connecting to this port can see the certificate).
- Fingerprint format matches the hub's: SHA-256 of the certificate's SubjectPublicKeyInfo DER, unpadded base64url (43 characters). The hub pins the fingerprint from the pairing link and doesn't validate the certificate name.
- Certificate lifetime is one year; with fewer than 30 days left it is renewed with the same key (also while listening, checked during handshakes), keeping the fingerprint. Changing the key (deleting `link-tls/`) requires re-pairing. These are configured values.

### Pairing

The direction is reversed from active mode (the computer may not be able to reach the hub):

1. On the computer: `persona-link pair --listen [<listen address>] [--addr <address>]…` (menu bar App: select **Let the hub connect** in the pairing window, then **Generate pairing link**).
   It switches link.json to passive mode and generates a one-time pairing code (128 random bits, `jc_` prefix, only SHA-256 stored); **stdout contains only the link**, explanations go to stderr:

   ```text
   persona://connector?addr=<host:port>[&addr=<host:port>…]&fp=<helper fingerprint>&code=<one-time code>
   ```

   - `addr` may repeat, best first, always `host:port` (no scheme, IPv6 in brackets, URL-encoded as a whole). Without `--addr`, local addresses plus port are ordered like active-mode hub candidates:
     LAN (private IPv4, ULA) → public IPv6 (excluding temporary, deprecated and tentative addresses on Linux) → public IPv4; exclude loopback, link-local, tunnel and bridge interfaces
     (Docker, VPN, Tailscale…). If all interfaces are virtual (container eth0, NAS interface attached to a virtual switch), fall back to those, still excluding tunnels and bridges.
     If none remain, `pair --listen` errors asking for `--addr`; `install --listen` still installs the service and instructs you to run `pair --listen --addr` later. If `--addr` is supplied, only those addresses are used.
   - Codes expire after 10 minutes and can be used once; another `pair --listen` generates a new code and invalidates the old one. The previously paired hub can keep connecting until the new hub consumes the new code.
2. Paste the link into hub admin **Computers › Add Computer › Hub connects** (addresses can be edited and a proxy supplied). The hub connects with `fp` pinning and exchanges the code for long-term credentials through `POST /api/v1/connector/pair`:
   the hub generates the random token itself; the helper stores only its SHA-256.
3. Afterwards the hub uses that credential to open `stream` to the node, as in active mode.

### Endpoints

Only these three exist: no shell, files or port forwarding; streams can connect only to `~/.persona/node-attach.sock`. All other paths and methods return `404 {"error":"not_found"}`.
Errors are JSON: `{"error":"<code>","message":"<human-readable Chinese>"}`.

| Endpoint | Authentication | Request | Response |
| --- | --- | --- | --- |
| `POST /api/v1/connector/pair` | One-time code | Body ≤ 16 KiB: `{"code","token","machine","hub_id","hub_name"}`; `code`, `token`, `machine` required (`token`: 32–512 printable ASCII characters, no whitespace), `hub_id`, `hub_name` optional but please include them | `200 {"version"}`; invalid fields: `400 bad_request` (not rate-limited, doesn't invalidate the code); wrong, expired or used code: `401 bad_code` (rate-limited) |
| `GET /api/v1/connector/info` | `Authorization: Bearer <credential>` | — | `200 {"machine","version"}`: `machine` is the machine id supplied by the hub during pairing; missing / wrong credential: `401 unauthorized` |
| `GET /api/v1/connector/stream` | Same | WebSocket upgrade; optional `X-Persona-Ping` (heartbeat, below) | Authenticate, connect node-attach.sock, then upgrade: node unreachable: `503 node_unavailable` (`message` gives the reason for the hub to record as this computer's error); non-upgrade request: `400`; after upgrading, see below |

- Successful pairing replaces the previously paired hub ("only trusts the hub it paired with": the group's primary; a new primary re-pairs after a switch). Old credentials stop working immediately, and streams opened with them close with code **4002**.
- Streams are binary messages containing unchanged node bytes in both directions. Accepts permessage-deflate (context takeover, like active-mode streams and SSH's former `Compression=yes`).
  The helper pings every 10 seconds and disconnects after 5 seconds without pong, while answering hub pings normally. When the hub closes a stream, the helper half-closes the node connection (write end), letting the node finish reading and clean up; the stream ends when the node closes.
  With a heartbeat configured in hub.json (`link`), the stream request carries `X-Persona-Ping: <interval milliseconds> <timeout milliseconds>`; the helper uses it on that stream (also clamped to 50 milliseconds–10 minutes);
  absent or not two numbers, it uses its own values. Old helpers ignore the header and retain 10 seconds / 5 seconds.
- Each stream is a new attach to the resident node, replacing the previous one as in active mode and SSH; the helper itself doesn't decide which is newest.
- When the helper stops or changes back to active mode, streams close with **1001**. Passive pairing stays in link.json; switching back to passive makes the same credential usable again.

### Authentication and rate limiting

- Only the credential hash is stored (`passive.token_sha256` in link.json), compared in constant time.
- Failed authentication (wrong code, missing / wrong credential) is counted per source: one IPv4 address or one IPv6 /64. **LAN and loopback count too** (the hub is usually on the LAN, which must not exempt it).
  10 failures within 60 seconds block the source for 15 minutes; blocked sources are rejected before authentication, returning `429 {"error":"rate_limited"}` and `Retry-After` (seconds) even with correct credentials. At most 4096 sources are tracked;
  when full, unblocked ones are evicted first. These are configured values, not measured results.
- A request must arrive whole within 30 seconds, body included (headers within 10): when the body stalls, the helper answers (`400` on pairing) and closes the connection, so a peer that never authenticates cannot hold one longer. An upgraded stream is not bound by this.
- Logs: at most one line per 30 seconds for authentication and TLS handshake failures (with the number omitted); sources are recorded only as /24 or /64.

### Recommendations for hub implementers

- Try `addr` values in link order (or start with only the first), and remember the working one; use edited addresses if the user changes them in admin.
- Back off on 401; don't keep retrying bad credentials (you'll block your own source). On 429, wait for `Retry-After`.
- Stream closed with 4002: another hub paired this computer; mark **Pairing required** and don't retry. On 1001 or connection failure, back off and reconnect normally.
- 503 `node_unavailable`: the link works but the resident node isn't running; show `message` as this computer's error and retry later.
- To check helper liveness and RTT: `GET /api/v1/connector/info`. The helper records the hub's last visit time and source, displayed in the menu bar and `persona-link status`.

### Passive mode in link.json

```json
{"mode":"passive","passive":{"listen":":18663","hub_id":"…","hub_name":"Home","machine":"example-workstation",
 "token_sha256":"…","paired_at":"…","code":{"sha256":"…","expires":"…"}}}
```

- Separate from active-mode fields (`hub`, `machine`, `fp`, `token`, `endpoints`), neither overwrites the other; absent `mode` or `active` means active mode (old files still work).
- `persona-link pair <link>` (active) sets `mode` to `active`, `pair --listen` sets `passive`, and `mode` changes only the mode. A running persona-link checks link.json every second,
  switching when mode changes (closing the link or listener and opening the other); a changed listen address changes the port.
- `persona-link uninstall --purge` also deletes `link-tls/`.

### Security boundary

- Someone able to reach this port can do nothing without a code or credential, only see the certificate (no hostname or addresses) and 404; guesses are rate-limited, and both are random values of at least 128 bits.
- A hub holding the credential can only open streams to the resident node, with exactly the same capabilities as via SSH (the node protocol), no more.
- For 10 minutes until consumed, the pairing link grants pairing authority: don't paste it into an untrusted place. Its `fp` prevents the hub connecting to an impersonating helper.
- Firewall: open this TCP port only to the hub (or its proxy), not everyone. `pair --listen`, `install --listen` and `mode passive` all show a reminder, for example
  `sudo ufw allow from <hub address> to any port 18663 proto tcp`; likewise for routers and cloud security groups. With the Mac application firewall enabled, the system may ask whether to allow
  incoming connections on first listen (not tested).

## Credentials (active mode)

- A kind of device credential: kind `link`, role `link`, bound to that machine in hub.json (`device_tokens.machine_id`, migration 0131).
  - The hub stores only the hash.
  - Can open only `/api/v1/link` and `/api/v1/link/stream`; every other endpoint returns 403.
  - Visible and revocable in App **Settings › Devices**; revocation immediately disconnects the link (4002).
  - Deleting the computer in the admin console revokes them too, and adding a computer from the admin console, an app or
    `personad hub pair --add` first revokes any left under its id, so an earlier computer of that id does not come back as
    the new one.
- Both kinds of pairing code can be exchanged for it:
  - `personad hub pair --machine <id>`: a code specifically for this computer (`pair_tokens.machine_id`). The link is `persona://link?…&machine=<id>`,
    exchangeable only for this computer's link credentials and nothing else. Stdout contains only the link, explanations go to stderr for easy piping.
    If the computer isn't in hub.json yet, add `--add [--name <name>]`: first adds `kind: link` (effective while running, no restart), already confirmed; without `--add`, it asks you to include it.
  - **Add Computer** in a signed-in App / web (`POST /api/v1/auth/pair-codes` with `add`, [protocol.md](../protocol/protocol.md), "Temporary pairing"): the hub adds `kind: link` just like admin's **Add Computer**,
    with a code for that machine. Computers added this way are **pending confirmation** and don't receive Persona-managed keys until the user taps **This is my computer** in the App ([protocol.md](../protocol/protocol.md), "Computer trust").
  - A normal App pairing code (`personad hub pair`) plus a machine id: CLI `persona-link pair --machine <id>`, or fill in **Machine id** in the App.
- The hub needs TLS (`public.tls_listen`) or an `https://` reverse-proxy `public_url`; without either, `--machine` returns 409.
- Failed authentication follows the hub's existing rate limits (public sources counted). Public connections require TLS; LAN plaintext isn't accepted either, except loopback
  or a LAN reverse proxy reporting HTTPS (`X-Forwarded-Proto: https`; see "[Hub behind a reverse proxy](#hub-behind-a-reverse-proxy)").
- Credentials are stored in `~/.persona/link.json` (0600): hub id, machine id, fingerprint (or `"trust": "system"`: no fingerprint, system-CA validation), credential, candidate addresses (hub-provided plus manually added).

## Connecting a computer (Mac example)

Prefer adding it in the App: no admin interface or sign-in to the hub machine needed. Below is active mode on Mac; for Linux, replace steps 2 and 3 with pasting the web app's one-line command into that computer's terminal;
for Windows see [windows.md](windows.md). Instructions for people who don't use terminals are in [getting-started.md](getting-started.md), "Connect a computer".

1. Install Persona Link on the Mac: `./scripts/app/mac-link.sh install` ([persona-link-install.md](persona-link-install.md)). It builds (including persona-node), copies to `/Applications`, and opens;
   first launch installs the resident node (`com.persona.node`), leaving existing installations untouched.
2. In the signed-in web app, open **Machines › Add Computer**, select Mac and enter a name (Chinese is allowed; the hub generates the id from the name). The hub adds `kind: link`, **pending confirmation**, and provides its pairing link
   (`POST /api/v1/auth/pair-codes` with `add`, [protocol.md](../protocol/protocol.md), "Computer pairing codes"). iOS and Android entry points are in progress.
3. Send the link to this Mac (**Copy pairing link** or **Share…**) and paste it into **Menu bar › Pair / Change connection…**.
4. Once it connects, the same web screen asks "A computer wants to join: <name>. Is it yours?"; tap **This is my computer** (`POST /api/v1/machines/{id}/confirm`).
   Before confirmation it receives no Persona-managed keys, isn't considered for assistant-machine selection, and doesn't appear in the computer list for creating Agents; unconfirmed computers are removed after 24 hours ([protocol.md](../protocol/protocol.md), "Computer trust").
   If you closed that screen, its card in the machine list also lets you confirm or delete it.
5. Verify: the menu bar icon becomes connected, the menu says **Local node online**, and the computer is online in App **Settings › Machines**.
6. To connect on startup, enable **Launch at login** in the menu (`SMAppService`).

Links expire after 10 minutes and are single-use; if expired, tap **Regenerate** on the web screen (`{"machine": <id>}` issues a new code and also revokes old Persona Link credentials).
Without hub TLS, the web reports "The hub hasn't enabled a secure connection yet" (409 `tls_required`); see "Advanced" below.

### Advanced

- **Add without the App** (already confirmed): admin **Computers › Add Computer › Computer connects**; or generate a pairing link on the hub machine:
  - Native deployment: `personad hub pair --machine macbook --add --name MacBook` (`--add` does nothing if the computer exists, but still returns a link);
  - Docker on NAS: `sudo /usr/local/bin/docker exec personad /opt/persona/bin/personad hub pair --machine macbook --add`.
- **Hand-edit hub.json**: add a `machines` entry (or change the previous SSH entry) to `{"id":"macbook","name":"MacBook","kind":"link","icon":"laptop"}`, restart the hub, then generate the link with the command above.
- If claude is a shell wrapper function, configure its launcher in `machines[].launchers` in hub.json.
- **CLI pairing**:
  ```sh
  … | "/Applications/Persona Link.app/Contents/Helpers/persona-link" pair -
  ```
- **Passive mode**: replace steps 2 and 3 with **Let the hub connect** in the pairing window, then paste the link into hub admin **Computers › Add Computer › Hub connects** ("Passive mode" above).

For Linux see "Linux (headless)" below: build `persona-link`, then `persona-link install --pair -` pairs, installs and waits for connection in one command.

## Menu bar App (`apple/PersonaLink/`)

**Build**
- `./scripts/app/mac-link.sh build|install|snapshot <directory>`.
- A standalone SwiftPM package (not in `apple/project.yml`, no shared code with the Persona App), built with `xcodebuild`: on this development machine, `swift build`'s default build system reports "Developer directory not found".
- persona-link and persona-node are built into `Contents/Helpers/` (persona-node is for the resident node; see "The device's program").
- Local ad-hoc signing (Sign to Run Locally), no hard-coded team.
- Default bundle id is the placeholder `com.example.persona.link`, overrideable by `mac.link_bundle_id` in `~/.config/persona/config.json` or environment variable
  `PERSONA_LINK_BUNDLE_ID` (old name `PERSONA_LINK_BUNDLE_ID`).
- After building, `vtool` changes the executable's recorded SDK version to the actual SDK used: xcodebuild records Swift packages as deployment target 15.0, making the system draw menu bar panels in the old SDK style
  ([lessons/macos.md](../lessons/macos.md), "Persona Link's panel looks old").
- `UNIVERSAL=1` also builds Intel.

**System settings**
- No Dock icon (`LSUIElement`).
- No sandbox: needs sockets and config in `~/.persona`.
- Info.plist registers both `persona` and `persona` URL schemes (#4198): opening `persona://link?…` in Notes, AirDrop, mail or Safari opens and prefills the pairing window; see "Pairing window" below.
  The App is not signed and notarized: on first launch, right-click it in Finder and choose Open to pass Gatekeeper. The schemes are registered only after the App has been opened once; earlier link taps cannot open it.
  If the Mac App (`PersonaMac`, which also registers both schemes) is installed on the same Mac, the system may send the link there instead: it recognizes `link` and forwards to Persona Link (found by executable `PersonaLink`, with configurable bundle id). If Persona Link is absent, it asks you to install it rather than pairing with that link; Persona on iPhone says "Open this link on the computer you want to add."
- Info.plist includes `NSLocalNetworkUsageDescription`: from macOS 15, LAN connections require local-network permission, and the first prompts the user.
  If denied, LAN candidates report unreachable; enable it in **System Settings › Privacy & Security › Local Network**.

**Helper**
- The App starts it with `persona-link run --parent`.
- Stdout emits one JSON status per line; the App reads the latest.
- Stderr appends to `~/.persona/link.log`; if over 1 MB, it is cleared on the next helper start.
- Uses the resident node's data directory: if the App environment lacks `PERSONA_HOME` / `PERSONA_HOME`, it reads the directory from `~/Library/LaunchAgents/com.persona.node.plist` and passes it to the helper; otherwise defaults to `~/.persona` (`~/.persona` on machines that used the old version) (`LinkStatePaths.swift`).
- `--parent` exits the helper when stdin closes, so any App exit, including a crash, also exits the helper.
- If the helper exits, the App restarts it: exponential delay from 1 second to 30 seconds; after running for a full minute, restart delay resets to 1 second.

**Icons**: template images remain clear on light and dark menu bars.

| State | Icon |
| --- | --- |
| Connected (passive: hub connected) | `personalhotspot` |
| Connected, waiting for confirmation on your phone (`pending`) | `hourglass` |
| Connecting | `arrow.triangle.2.circlepath` |
| Passive listener waiting for hub | `antenna.radiowaves.left.and.right` |
| Offline or error (passive: can't listen) | `personalhotspot.slash` |
| Unpaired | `link.badge.plus` |
| Link connected, but Persona's background service (resident node) on this computer is not running | `exclamationmark.triangle` |

**Menu**
- Second line is the mode: **Connect to hub (active)** or **Let hub connect (passive) · Listening :18663**.
- Active: selected address (LAN / public IPv6 / domain), connection duration, disconnect reason (one line per candidate), local node online state.
- Passive: hub connected (source address, duration, local node state); or listening (when and where the hub last connected); or waiting for pairing (remaining validity of the pairing link);
  or unable to listen (port occupied, etc., retry delay).
- Connected hub and group size: "hub ‘Home’ · 3 hubs in linked group", or "hub ‘Home’ · Not linked to other hubs" when alone; disconnected shows only the name.
  Old hubs sending no name omit the line, preserving the previous menu.
- The first line describes the state plainly: unpaired is "Not connected to hub yet" (menu action "Paste pairing link…"); "Connected to hub"; awaiting confirmation is "Waiting for confirmation on your phone" plus "Tap ‘This is my computer’ in the phone App";
  if the resident node is not running, "Persona's background service on this computer is not running", with **Reinstall** in the menu (`persona-link node uninstall` then `node install`, preserving the node program in `~/.persona/bin`); "Connecting" or "Offline".
  "Waiting for confirmation" uses status-line `pending`: the hub does not yet send this field (#4655), so this period currently shows "Connected".
- **Reconnect now** (not in passive mode, where the hub initiates), **Pair / Change connection…**, **Install local node service** (only when absent), **Launch at login**, **Quit**.

**Pairing window**
- Select **Connect to hub (active)** or **Let hub connect (passive)** at the top, defaulting to the current mode; viewing doesn't switch. Switching happens when pairing (active: paste and pair; passive: generate link),
  or by tapping **Use this** (that mode was paired before, no re-pairing, `persona-link mode`).
- Active: paste the pairing link from **Add Computer** on the phone or web, or open it directly (the URL scheme opens and prefills the window). **Never pairs automatically**: first shows the destination hub (`url` host),
  "Security fingerprint included" when `fp` is present, and this computer's name on the hub. Pairing runs only after the user taps **Pair**: a forwarded or forged link cannot hand this computer to someone else's hub on its own.
  Only accepts host `link`; other links such as `persona://pair?…` show "This link is for the phone / Mac App", without prefilling or taking further action.
  Machine id is under **Advanced** (only when the link lacks `machine`, such as `personad hub pair` without `--machine`). The App calls `persona-link pair`, displaying success or errors in the window. Lists candidate addresses and marks the selected one **In use**;
  manual addresses can be added (`persona-link address add`) and removed.
- Passive: enter listen address (default `:18663`); **Generate pairing link** calls `persona-link pair --listen`, showing the selectable link with **Copy**, its addresses and firewall guidance;
  if already paired, shows which hub and this computer's name.

**Screenshots**
- `./scripts/app/mac-link.sh snapshot <directory>` renders the App's own views offscreen, a light and dark set each:
  - Every menu state;
  - Pairing window;
  - Every icon.
- No screen-recording permission needed, and no unrelated content captured.

## Status line (helper → App)

```json
{"state":"connected","machine":"macbook","hub_name":"Home","mesh_hubs":1,"address":"https://192.0.2.10:18662","kind":"lan",
 "since":"…","node":"online","endpoints":[{"url":"https://192.0.2.10:18662","kind":"lan"}]}
```

- `mode`: `active` / `passive`.
- `state`: `unpaired` / `connecting` / `connected` / `waiting` / `refused`; passive mode uses `unpaired` (listening, waiting for pairing), `listening` (paired, hub not connected),
  `connected` (hub has a stream open; `address` is the hub's address and `since` the stream start time), `waiting` (unable to listen).
- Passive mode also includes `listen` (listen address), `addrs` (local addresses usable by the hub, `host:port`, best first), `last_seen` and `from` (time and address of the hub's last visit),
  `code_expires` (expiration of the `pair --listen` code).
- `node`: `online` / `offline` / `unknown`, reason in `node_error`.
- `pending` (planned, #4655): `true` means the hub is waiting for the user to confirm this computer in the App; absent means confirmed.
- `waiting` / `refused` include `error` (candidate failures separated by "；", a full-width semicolon) and `retry` (next attempt time).
- `hub_name`, `mesh_hubs`: see "Hub name and linked group"; absent with old hubs.
- `machine_name`: what the hub calls this computer (see "The computer's name"); absent with old hubs. `rename`: how the last rename asked of this persona-link went: `{id, name}` or `{id, code, error}`.
- `pending`: meaningful only when `connected`; `true` means the hub is waiting for the user to confirm this computer (see "Pending confirmation"); absent or `false` means confirmed.
- `trust`: `system` when active mode validates the hub through system CAs (reverse proxy, no fingerprint); absent when pinned.
- No credentials included.
- Each line is also written to `~/.persona/link.status.json` (with `pid`, `version`, `updated`; normal exit writes `"state":"stopped"`) for
  `persona-link status` to read, since it can't query the running instance.

## Linux (headless)

On servers, NAS boxes and Raspberry Pis without a GUI, `persona-link` alone is enough.

**Build**
- Pure Go, no cgo: `cd server && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o persona-link ./cmd/persona-link`.
  Builds for `amd64`, `arm64` and 32-bit Raspberry Pi (`GOARCH=arm GOARM=7`); Windows also builds (file locks and signals in platform-specific files), with a tray icon instead of the menu bar App ([windows.md](windows.md), "Tray").
- `personad` also builds for Windows (`GOOS=windows go build ./...`; process groups, signals, file locks and CPU time are platform-specific in `internal/osproc` and each package's `_windows.go`),
  and the Windows resident node works with Orca; `persona-link install` installs a sign-in scheduled task on Windows (the Windows paragraph under "Install the node service"), see [windows.md](windows.md).
- The resident node runs the device's program, persona-node, built from the same commit (`~/.persona/bin/persona-node`; personad where the computer has no persona-node or runs a hub or a manager, "The device's program"); `scripts/deploy/node-launchd.sh install` installs a personad node on Linux as systemd user service `personad-node`.

**One-command installation**

Get the link from signed-in web **Machines › Add Computer** (requires confirmation on the web), or generate it on the hub machine via CLI (already confirmed):

```sh
personad hub pair --machine nas --add             # On hub machine; adds it if absent; stdout contains only the link
./persona-link install --pair - <<< '<link>'       # On this computer; reads link from stdin
```

Passive mode: `./persona-link install --listen [:18663] [--addr <address the hub uses to reach this computer>]`; after installation, prints a link for hub admin.

`install` runs these steps:
1. With `--pair`, pair first (same as `persona-link pair`; no link after `--pair`, or `-`, reads stdin). With `--listen`, switch to passive mode and generate a link if never paired
   (same as `pair --listen`); an existing pairing stays unchanged. These options can't be combined.
2. Copy itself to `~/.persona/bin/persona-link`, unless already there. Running `install` again with a new version upgrades it.
3. If absent, install and start the resident node service (systemd user service `personad-node`, same contents as `scripts/deploy/node-launchd.sh`): runs `~/.persona/bin/persona-node`, else `~/.persona/bin/personad` (the install script puts one there first),
   or `--node-bin <path>` / `--personad <path>` to copy a supplied binary. Existing installations stay unchanged; `--no-node` leaves it alone.
4. Write `~/.config/systemd/user/persona-link.service` (under `XDG_CONFIG_HOME` if set), then `daemon-reload`, `enable`, `restart`.
5. If linger is off, suggest `sudo loginctl enable-linger <user>`: otherwise the user instance is absent when nobody is signed in, services stop at logout and don't start at boot.
6. Wait up to 20 seconds for hub connection and the hub's local-node check, then print a one-line conclusion. Connected: exit 0; not connected: print every candidate's failure reason, exit 1, leaving the service installed and retrying.
   Passive mode waits for listening (or explains why it can't), then prints the pairing link and firewall guidance.

The unit contains neither mode nor listen address: both live in link.json. Changing mode or port requires no service reinstall; the running persona-link switches within a second or two.

Running `install` before pairing starts the service as unpaired; later `persona-link pair` makes it adopt new credentials within seconds, without restart (checks link.json changes every 2 seconds).

Generated unit:

```ini
[Unit]
Description=Persona link: keeps this computer reachable from the Persona hub

[Service]
ExecStart="/home/<user>/.persona/bin/persona-link" run --log "/home/<user>/.persona/link.log"
Environment="PERSONA_HOME=/home/<user>/.persona"
Environment="PERSONA_HOME=/home/<user>/.persona"
Restart=always
RestartSec=5
StandardOutput=null

[Install]
WantedBy=default.target
```

- Paths use this computer's data directory (`~/.persona` for new installs, `~/.persona` on machines using an old version, or `PERSONA_HOME` if set; see [rename.md](rename.md)); both `Environment` lines pass the same directory to the service so it doesn't resolve another.
- Doesn't wait for `network-online.target`: user instances don't have this target, and persona-link retries itself.
- Status lines are for the menu bar App, not journal; logs go to `~/.persona/link.log` (`run --log`; at 1 MB, rotates to `link.log.1`, a configured value).
  On machines without persistent journals, ordinary users can't see user-service logs with `journalctl --user`, so logging doesn't depend on it.
- Sessions entered through `su`, `sudo -u`, `docker exec` lack `XDG_RUNTIME_DIR`, so `systemctl --user` can't find the user instance; install / status / uninstall
  fill in `/run/user/<uid>` if unset and present. This doesn't help if the user instance itself is absent (no linger and nobody signed in); prompts to enable linger or SSH in as that user before installing.
- Installing as root warns that the service runs as root with root's data directory, and the resident node must also run as root (the prompt hard-codes `/root/.persona`, but the actual directory is resolved by the rules above).

**Without systemd** (containers, some NAS systems)

`install` still pairs and copies binaries, then prints copyable alternatives and exits 1:
- Run in the background now: `nohup ~/.persona/bin/persona-link run --log ~/.persona/link.log >/dev/null 2>&1 &`
- At boot: cron `@reboot` with the same command;
- Synology: a user script triggered at startup in Task Scheduler, containing the nohup line above;
- Container: `persona-link run --log …` as the main process, restart policy `unless-stopped`, `~/.persona` mounted as a volume, resident node in the same container;
- supervisord, runit, s6, OpenRC: run `persona-link run --log …` in foreground and restart on exit.

Resident node likewise: `nohup ~/.persona/bin/personad node --attach >> ~/.persona/node-attach.log 2>&1 &`.

Two things to know about a node without systemd:
- The hub cannot update it: remote node updates need launchd or systemd for their rollback watchdog, so the machine shows `can_deploy: false`
  and Redeploy and the automatic node update after a hub update skip it. Upgrade by hand: replace `~/.persona/bin/personad` with the hub's
  own build for that platform (same version as the hub) and restart the node process.
- A container on the hub's own machine may not reach the hub at the hub's LAN address or public name (a NAS's policy routing sends the
  replies elsewhere; a default bridge network has no IPv6). Its bridge's gateway does reach it: pair with the link's `url` host replaced by the
  gateway address, and add `https://<gateway>:<TLS port>` with `persona-link address add` so it stays first ([lessons/server.md](../lessons/server.md)
  "A node service in a Docker container on the hub's own NAS").

**Status, logs, uninstall**

| Command | Action |
| --- | --- |
| `persona-link status` | Mode, link (address, duration; candidate failure reasons when disconnected), hub name and group size, local node, this computer's name on the hub (its id in parentheses when they differ), candidate addresses, systemd service (active / enabled / linger), pid and version, log location. Passive mode instead shows: listen address, listening or hub connected, paired hub and pairing time, hub's last visit time and address, helper fingerprint, local addresses usable by hub. Exit 0 when connected (also listening in passive mode), otherwise 1 |
| `persona-link status --json` | Same for scripts: `running`, `mode`, `paired`, `machine`, `machine_name`, `hub_id`, `hub_name`, `endpoints`, `trust` (`pin` / `system`), `link` (last reported status), `passive` (`listen`, `paired`, `hub_id`, `hub_name`, `machine`, `machine_name`, `paired_at`, `code_expires`, `fp`), `service`, `config`, `log`; `error` if link.json can't be read; no credential or hash |
| `persona-link logs [-f] [-n lines]` | Last 100 lines of `~/.persona/link.log` (change with `-n` / `--lines`); `-f` / `--follow` keeps following, including rotation; without a log file but with an installed service, reads journal. `log` is the same command |
| `persona-link uninstall` | Stop, disable and delete systemd user service (Windows: scheduled task), preserving credentials. Linux / Mac resident-node service remains (`node uninstall` removes it); on Windows the node shares the task and stops too, but `personad.exe` remains |
| `persona-link uninstall --purge` | Also remove credentials (link.json and `.lock` / `.run`), status, logs (including `link.log.1`), passive TLS identity `link-tls/`, `~/.persona/bin/persona-link`, and install-script `persona-link` alias (only if truly an alias); refuses while another persona-link is running (menu bar App, manually started). Revoke hub-side credentials in App **Settings › Devices** |
| `persona-link pair [--machine <id>] [<link>\|-]` | Active pairing, credentials saved to `~/.persona/link.json`. No link or `-` reads stdin; prompts to paste in a terminal and reads one line on Enter. If the link lacks a machine id (normal App code), specify `--machine` |
| `persona-link pair --listen [<listen address>] [--addr <address>]…` | Passive pairing link (only link on stdout, for piping); default listen `:18663`; can't combine with pairing link or `--machine` |
| `persona-link mode [active\|passive] [--listen <address>]` | No arguments: display current mode (with passive listen address); supplied mode: switch, retaining both pairings; `--listen` alone changes passive listen address without switching mode |
| `persona-link node install [--node-bin <path> \| --personad <path>]` / `node switch [--to personad]` / `node status [--json]` / `node uninstall` | Resident node service (Linux systemd `personad-node`, Mac launchd `com.persona.node`); a new one runs `~/.persona/bin/persona-node` when there is one (else personad, and always on a computer that runs a hub or a manager), existing services stay unchanged; `--node-bin` puts the given program in under its own name. `switch` moves an existing service to persona-node (`--to personad`: back), whole or not at all ("The device's program" above). `uninstall` deletes neither program. On Windows the node shares persona-link's task; only `node status` exists |
| `persona-link discover [--wait <seconds>] [--json]` | Hubs heard on the LAN (mDNS), viewing only, no pairing; listens 3 seconds by default, `--wait` allows decimals up to 60; exit 0 if found, otherwise 1 |
| `persona-link run [--parent] [--log <file>]` | Maintain the link in foreground, one JSON status per stdout line (see "Status line"); `--parent` exits on stdin close (used by menu bar App); `--log` writes there (rotates to `<file>.1` at 1 MB), not stderr. Used by systemd and the alternatives under "Without systemd" |
| `persona-link address add <address>` / `address remove <address>` | Manually add / remove a hub candidate (`https://domain:port`, or just domain), stored in link.json; pairing required before adding. `remove` deletes that entry, manual or hub-provided (the hub may send it again on next connect) |
| `persona-link name [<new>\|--reset]` | No argument: what the hub calls this computer (with the id when they differ). A name: asks the running, connected persona-link to have the hub rename this computer and prints the hub's answer; `--reset`: back to the id. Exit 1 with the reason when it can't (not running, offline, passive mode, a hub that doesn't rename computers, not confirmed yet, too long); see "The computer's name" |
| `persona-link version` | Print version (`--version`, `-v` equivalent) |
| `persona-link help` | Print usage (`--help`, `-h` equivalent); unknown commands / options also print usage and exit 2 |

`status` uses:
- Running: whether the file lock `~/.persona/link.json.run` is held (regardless of who started it: systemd, menu bar App, manual). It briefly acquires the lock to check;
  a persona-link starting at that exact moment exits as "already running", then systemd starts it again after 5 seconds.
- Link state: `~/.persona/link.status.json` (see "Status line"); when not running, shows the last report and time.
- **Does not connect to the resident node socket** to check liveness: every connection is a new attach and would replace the hub's current connection. Local-node status comes only from the hub.
- Also works on Mac (the menu bar helper writes the same status file); `install` on Mac only suggests installing the menu bar App.

`scripts/deploy/link-systemd.sh` remains a thin wrapper: `install` / `uninstall` / `status` delegate to `~/.persona/bin/persona-link`; `restart` runs `systemctl --user restart`.

**Verification** (2026-11-02): a clean Debian 12 container (arm64) in local Docker, with a test hub on the host listening only on 127.0.0.1; the container connects through
`host.docker.internal` to its TLS port. Everything was stopped afterwards.
- Without systemd: `install --pair -` pairs, copies and prints alternatives (exit 1); after the suggested `nohup … run --log …`, `status` shows connected and
  `logs` contains logs; starting a resident node makes the local node online and the hub machine list shows this computer online.
- With systemd (`--privileged --cgroupns=host`, image needs `libpam-systemd` or the user instance won't start; `loginctl enable-linger`):
  enter with `docker exec -u` (no `XDG_RUNTIME_DIR`), install and connect with one `install --pair -`; after `scripts/deploy/node-launchd.sh install`, local node is online;
  container restart (equivalent to boot) reconnects automatically; disabling linger makes `status` suggest enabling it; another install (upgrade), install before pair,
  uninstall and uninstall --purge all behave as expected.
- amd64 version ran version and status in an emulated amd64 container. Not tested on a real Raspberry Pi / NAS.

## Use it away from home too

The link uses only the hub's TLS port. Away-from-home use also needs:
- DSM firewall allows 18662/tcp (not 18660).
- Router allows IPv6 inbound to NAS.
- Domain in hub.json `public.advertise` (DDNS points to NAS IPv6). After one connection the hub sends it to persona-link; it can also be manually added in the App.

IPv4-only networks can't reach a home IPv6 address, just as with the phone.

## Troubleshooting

| Symptom | Action |
| --- | --- |
| Passive: "Can't listen on :18663: port in use" | Another program owns it: change port with `persona-link mode passive --listen :18664` (then edit this computer's address in hub admin), or stop that program |
| Passive: hub can't connect, and "Last hub connection" in `status` never changes | Firewall / router doesn't allow the port; hub can't reach the link addresses (edit address or proxy in admin); fingerprint mismatch (deleted `link-tls/`: re-pair) |
| Passive: hub reports 429 | Too many failed authentications from that source (usually retrying old credentials); wait 15 minutes or restart persona-link to clear counters |
| `pair --listen` finds no address | Only loopback, tunnel or temporary addresses: use `--addr <domain or address the hub can reach>` |
| Menu says "Link connected, but hub can't reach local node" | Resident node isn't running: `launchctl print gui/$(id -u)/com.persona.node`; log at `~/.persona/node-attach.log` |
| Mac: sessions started from the phone never come up (Claude stays at "Loading commands and agents", no hook fires) | Run `personad node doctor`: `launchd-process-type` (launchd throttling the node; `--fix` rewrites the agent and reloads it) and `path-access` (a working directory, setting or skill link in Desktop / Documents / Downloads; grant Full Disk Access to the node binary, or move it out) (docs/server/server.md "Scheduling and privacy on the Mac") |
| "Hub rejects this computer's credentials" | Credentials revoked or hub database changed: re-pair |
| Reverse proxy: "Certificate not trusted by the system" / "Certificate hostname doesn't match" | Proxy certificate isn't public-CA signed, expired, or doesn't match `public_url`: fix certificate or `public_url`, then re-pair |
| Reverse proxy: hub returns HTTP 403 | Hub logs show `tls_required`: proxy on another machine lacks `X-Forwarded-Proto: https`, or hub treats its address as public (container gateway, public address); see "[Hub behind a reverse proxy](#hub-behind-a-reverse-proxy)" |
| Reverse proxy: HTTP 404 / 400, or immediate disconnect | Prefix not stripped, WebSocket not forwarded (`Upgrade` / `Connection` headers), or `proxy_read_timeout` too short |
| "This computer opened another link elsewhere" | Same link.json copied to another computer, or two persona-link instances running |
| LAN candidates "unreachable" though network works | Check local-network permission |
| Linux: `install` says "Can't reach your systemd user instance" | Entered through `su` / `sudo -u` / `docker exec` without linger: SSH directly as this user, or first `sudo loginctl enable-linger <user>` |
| Linux: link disconnects at logout and doesn't connect after reboot | Enable linger: `sudo loginctl enable-linger <user>` (`persona-link status`'s background-service line explains) |
| Linux: link connected, local node offline | Resident node isn't running: `systemctl --user status personad-node`; log at `~/.persona/node-attach.log` |

Logs:
- Helper / Linux service: `~/.persona/link.log` (`persona-link logs -f`); passive mode uses `passive: …` (listener start, pairing, stream start / end, rejected requests);
- Hub: `[<machine>] link up …` / `link down …`.

## How passive mode and the node service were verified (2026-11-02)

- Go tests (`server/internal/link/listen_test.go`, `server/cmd/persona-link/passive_test.go`) use a fake hub following section 4: pins the link fingerprint (a different fingerprint fails the handshake),
  pairs with the code and opens authenticated streams to a fake node in a temporary directory (echo); verifies 300 KiB random bytes unchanged both ways, wrong credential 401, correct credentials also 429 after rate limiting, single-use and expired codes,
  other paths 404, old credentials 401 and old streams closing with 4002 after re-pairing, missing node 503, helper disconnecting when hub doesn't pong, occupied port error and retry, switching between active / passive.
  Existing active-mode tests all still pass.
- Real binaries (temporary HOME, listening only on 127.0.0.1): `persona-link pair --listen` + `persona-link run` + real `personad node --attach`; after pairing, the fake hub sends node-protocol
  `ping` and `node.info` on the stream, and the resident node answers both.
- Clean Debian 12 (arm64) container with systemd: `curl … install.sh | sh -s -- --node --listen` (`docker exec -u`, no `XDG_RUNTIME_DIR`), both user services active,
  link printed; a fake hub on the host pairs through a port mapped to 127.0.0.1 and talks to the real node in the container; both services restart themselves after container restart, and original credentials still work.
  On the first run the container's only interface was virtual, leaving no candidate addresses and blocking installation; this led to the "fall back when all interfaces are virtual" behavior.
- `persona-link discover` heard a temporary fake hub broadcast on the LAN (mDNS, stopped afterwards).
- Passive-mode screenshots of the Mac menu bar and pairing window (light and dark, `mac-link.sh snapshot`). The App's first-launch resident-node installation **was not run on a real machine**: this development machine has a real, actively used
  `com.persona.node` that must not be touched; Mac `node install` command sequences (`launchctl bootout` / `bootstrap`) were tested only with fake commands; the plist passed `plutil -lint`.
- Not tested: real hub dialing side (#435 in progress), SOCKS proxy, passive mode on Windows (compiled only; Windows active mode and scheduled task in [windows.md](windows.md)), public IPv6 inbound.
