# Hub restarts invisible to clients

User, 2026-11-04: every time the hub is deployed or restarted, Persona on iPhone, Mac, web and Android disconnects, and the assistant's background tasks running in the main chat (Monitor, `run_in_background`) get stopped too.
Requirement: **when the server restarts, clients notice nothing at all**. Issue #1574.

Goal: in a normal deployment, no client shows a disconnect notice, no message is lost, and no running chat is interrupted.

## Before the change

Verified on 2026-11-04 against master `2fe55f92`, before the work below. Code locations refer to the current files and functions (checked 2026-11-06; after `2fe55f92`, machine connections (spawning nodes, disconnect handling) moved from hub/machine.go and hub/machine_conn.go to `devices/`; `hub/machine.go` still exists, and `onEvent`, which forwards node events, stayed there); the behavior described is still that before the change.

### What a restart does

- `scripts/deploy/deploy.sh:200-215` deploys the hub: `db.sh` → copy the node binary → `install_bin` (`personad.new` then `mv`, an atomic replace) → extension programs → `hub-systemd.sh`.
  `install_bin` also restarts this machine's resident node (`deploy.sh:126`, `:137`, `node-launchd.sh restart`).
- `scripts/deploy/hub-systemd.sh:34-55` rewrites `~/.config/systemd/user/personad.service` every time (`Restart=always`, `RestartSec=3`, no `KillMode`,
  i.e. control-group), then runs `systemctl --user restart personad`. There is no health check beyond `is-active`.
- On SIGTERM (`signal.NotifyContext` in `main` of `cmd/personad/main.go`): the hub's ctx is cancelled at once, and every machine connection is killed at the same moment (the connections started by `startDevices(ctx)` in `runHub` of `hub/hub.go`).
  The shutdown order in `serveHTTP` of `hub/public.go`: close the listener first → `stopWebSockets` sends each client 1001 "hub stopping" and force-closes after 0.5 s (`stopWebSockets` in `hub/shutdown.go`)
  → `http.Server.Shutdown` (2 s per step, 8 s total budget, `DefaultShutdownTimeout` / `shutdownStepTimeout` in `shutdown.go`) → wait for node connections → close the database. **The listener closes before draining**, so the port is closed during the drain.
- The process listens itself with `net.Listen` (`serveHTTP` in `public.go`, the TLS port in `startPublic`); there is no `SO_REUSEPORT`, `LISTEN_FDS` or socket activation.
  The new process only listens after `db.Open` (migrations), extensions, the knowledge base and everything else is up (`runHub` in `hub.go`).
- Measured (journal of the restart on this dev machine at 19:30 on 2026-11-04): stopping began at 19:30:29.88, finished at 30.93, and the new process started listening at 31.14: **the port was closed for about 1.3 s**,
  and connections in that window were refused (connection refused).

### Each kind of connection at the moment of restart

| Connection | During restart | Location |
| --- | --- | --- |
| Client WebSocket (`/api/v1/ws`) | Receives a 1001 close; reconnects in the next 1.3 s are refused, and it only connects once the new process is up | `stopWebSockets` in `shutdown.go`, `serveWS` in `http.go` (upgrades during shutdown get 503) |
| In-flight HTTP requests | `Shutdown` waits at most 2 s, then force-closes; new requests in that 1.3 s are refused | `serveHTTP` in `public.go` |
| Nodes on each computer | As soon as the hub's ctx is cancelled, the `personad node --stdio` / `ssh` subprocesses are killed (SIGKILL, `WaitDelay` 2 s in `open` of `devices/machine.go`), and **at the same time** the machine is marked offline, `machines` is broadcast, and non-resumable interactions on that machine are ended (`detach` in `devices/conn.go`); clients receive these broadcasts before they are disconnected | `command`, `open` in `devices/machine.go` |
| Local node | A child process of the hub (`node --stdio`), killed together with the hub by systemd's cgroup | `command` in `devices/machine.go` (the `local` branch) |
| Extensions (the Lark extension and others) | Child processes of the hub (stdio RPC), killed with the hub's cgroup and respawned by the new hub; login state lives in each extension's own directory, so a restart only loses the hub-side session and readiness state | `spawn` in `extension/host/host.go` |
| Connector programs (telegram, wechat) | Same, restarted with a 5 s backoff | `Conn.Run` in `connector/proc/proc.go` (`restartFirst`) |
| persona-link (computers of kind link) | Receives 1001, reconnects with 1–60 s exponential backoff, status text "hub restarted" | `backoff` in `link/client.go`, `summary` in `link/client_race.go` |

After the new hub comes up: the local node connects within 0.2 s; a Mac over ssh takes 2–8 s (measured: one at +2.4 s, another at +8 s). During that time the machine shows as offline and its session list is empty.

### How clients detect a disconnect and recover

| | iOS / Mac (`apple/Shared/LiveConnection.swift`) | Web (`web/src/lib/store.ts`) | Android (`core/hub/.../OkHttpHubConnection.kt`) |
| --- | --- | --- | --- |
| Detecting a disconnect | receive fails; ping every 20 s, a pong 5 s late counts as stuck, disconnect after 45 s without messages (`:266-269`, `:383-411`) | `onclose`; a watchdog every 10 s, reconnect after 70 s without messages (`:217-245`) | `onClosing` / `onFailure`, 15 s hello timeout, 45 s without messages in the foreground (`:411-486`) |
| Reconnecting | Starts at 1 s, doubling to 20 s, no jitter (`:468-477`) | Starts at 600 ms, doubling to 15 s, ±20% jitter (`:222`) | Starts at 1 s, doubling to 20 s, ×0.8–1.0; reset only after sync completes (`:340-378`) |
| UI | A non-online state must last 1.5 s before "Connecting…" / "Updating…" shows (`ConnectionStatus.swift:39`, `:127-151`); during a hub self-update it shows "Hub updating…" | Every disconnect switches to "Connecting" at once, and from the third failure on "Can't reach the hub. Retrying…" (`App.tsx:206-212`, `Sidebar.tsx:429`); no grace period | Leaving Online is published after a 1.5 s delay (`:797-813`), a timeout shows "Connecting" immediately, and once connected it first shows "Updating…" (`:511-513`) |
| After reconnecting | Connection snapshot (machines, sessions, interactions) + refetch interactions, Inbox, to-dos, tasks, etc.; chats resume from `since_seq` | After `hello`, `refresh()` refetches 7 endpoints + Inbox, extension slots, terminals; chats resume from `since_seq` | Back to Online, refetches machines, sessions, interactions; chats resume from `since_seq` |
| Writes during the disconnect | Messages go into a persistent outbox and are resent after connecting (`client_msg_id`); approvals / answers are removed optimistically and restored on failure, with no retry and no op id | Sending a message fails immediately ("not sent: can't reach the hub"); a failed approval shows "not delivered"; no op id | Messages go through a persistent outbox with `client_msg_id` and `X-Persona-Op-Id`, checked against receipts; approvals carry no op id and are not retried |

What they share: **global live events have no sequence number**, so broadcasts during the disconnect (`todos`, `inbox`, `lark`, `revs`, `interaction_resolved`…) are all lost and can only be recovered by a full refetch after reconnecting;
only chat content can resume from `since_seq` (`subscribe` and `sendBacklog` in `onClientMessage` of `http.go`, `docs/protocol/protocol.md` ("Continuity of items")). The hub already has idempotent receipts (`X-Persona-Op-Id`, `hub/clientops.go`,
`docs/protocol/protocol.md` ("Operation receipts")) and `client_msg_id` dedup, but only Android's message sending uses an op id.

What clients see during one restart: the web switches to "Connecting" immediately (connected again after about 1.3 s, with a full refetch); iOS / Android only show something after 1.5 s, so usually a brief flash of "Connecting…" or "Updating…";
on the Mac, ssh machines go offline for 2–8 s, and their sessions disappear from the list and then reappear.

### Work in progress inside the hub

- The assistant (the Persona tab's main chat, side chats, scheduled tasks, the AI twin, Lark /agent, bots, channel extensions: `hub/assistant*.go`; the AI twin is the manager's `assistant/inbox_agent.go`; Lark /agent lives in the Lark extension (since #1571, opening chats through the hub's public API);
  the Lark bot is `botSend` in `assistant/feishu_message.go`; channel extensions are `channelForward` in `assistant/channels_plugin.go`) all goes through `assistant.send` (bots and channel extensions now use it only to open new chats; continuing uses `session.send_once` via `sendOnce`), and **the node on the assistant's machine** starts a headless claude:
  `zsh -ic "<launcher> \"$@\"" … -p --input-format stream-json --output-format stream-json --include-partial-messages` (`spawnClaude` in `node/assistant_start.go`),
  in its own process group (`Setpgid`), with pipes for stdin / stdout.
- Streaming output: `readAssistant` accumulates `text_delta` in memory in `p.partial` and sends a `session.delta` every 80 ms (`readAssistant`, `emitDelta` in `node/assistant.go`); the hub forwards it to subscribers
  (`onEvent` in `hub/machine.go`, the `session.delta` branch). Text being generated exists only in memory; finished messages are in Claude Code's own transcript.
- When the hub restarts: the node's connection drops → `Node.Serve` returns → `reapAssistants` stops all assistant processes (`reapAssistants` in `node/assistant_reap.go`); the local node is killed with the hub by the cgroup anyway.
  **A resident node (`node --attach`) is no different**: one `Node` per hub connection, and a new connection stops the old one (`Attach` in `node/attach.go`), taking its assistant processes with it.
- Background commands from Monitor and `run_in_background` are child processes of claude and die with it. The node remembers each chat's background tasks (`background_tasks_changed` → `state.put`);
  after a restart, `recoverAssistantTasks` sends those chats a message "personad just restarted… background tasks stopped" (`node/assistant.go`, `prompts/assistant/lost_tasks.md`) so it can set them up again.
- What the user sees: the answer in progress stops halfway (the streamed part is gone, only the blocks already written to the transcript remain), and the next message continues with `--resume`; background tasks are gone, and the chat gets a "just restarted" message.
  Agents in herdr are unaffected (herdr is a separate process; only the helper subprocesses observing it disconnect).

## Design

### Zero gap: systemd socket activation

The listening socket is handed to `personad.socket` (`ListenStream` is hub.json's `listen`, plus a second one if `public.tls_listen` is configured), and the hub takes it from `LISTEN_FDS` at startup
(`hub/listen.go`: matched by port; if found it is used, otherwise the hub calls `net.Listen` itself). `systemctl --user restart personad` only restarts the service while the socket stays with systemd:
between the old process stopping and the new one starting, new connections wait in the backlog (instead of being refused), and are accepted as soon as the new process calls `Accept`.

Why this one:

- **SO_REUSEPORT + old and new processes side by side**: the new process must start before the old one stops, but a systemd service has only one main process, so either the deploy script starts the new process outside the unit or it switches to `Type=notify` and changes the main PID;
  a big change to how deployment works; and with two processes accepting connections at once, WebSockets can land on the one that is exiting.
- **Passing the fd ourselves (tableflip-style re-exec)**: can wait until the new process is ready before stopping the old one, but needs `Type=notify` + `NotifyAccess=all` + `MAINPID` and handling child processes in the cgroup,
  a large surface for errors; the only gain is saving the new process's ~0.2 s startup, and clients cannot see those 0.2 s spent queued in the backlog.
- **Socket activation**: standard, a few lines of code, no effect on Docker / launchd / running by hand (without `LISTEN_FDS` the hub listens itself); rolling back means deleting the socket unit.
  Cost: during the ~1 s while the old process stops, connections wait and get no immediate response, which is far within the clients' grace periods.

Every hub process takes the passed sockets first thing (`claimInherited` in `hub/listen.go`, at the top of `Serve`, `Run` and `RunSlot`), before anything can start another process: `LISTEN_*` are cleared and the sockets made close-on-exec, and so is anything else the process was handed past stderr (`osproc.SealInherited`).
Until 2026-11-08 a hub without its edge took them only when it first listened, and `runHub` could start the built-in manager before that: the manager, and through it every node, extension and agent it started, then held the hub's listening socket for as long as it lived (several pids on `ss -tlnp` for the hub's port).
The manager's and the node's run entries (`managercli.Assistant`, `manager run`, `node`, `node --attach`) seal what they were handed as well, so a socket an older hub still leaks stops there. Not in `main`: the sandbox's launcher and the source validator are the same binaries and hand descriptors on by exec on purpose.

`hub-systemd.sh` writes both units: the first time (or when the address changes) it stops the service once to hand the port to the socket (only this once is there a ~1 s gap); after that it always just runs `restart personad`.
`TimeoutStopSec=20` (headroom beyond the 8 s shutdown budget).

### Blue-green switching: the hub's edge (`internal/hubedge`)

Modeled on an account-pool relay gateway's approach: a resident edge process holds the port, with two slots behind it. The trigger was a problem reproduced on Linux on 2026-11-05: when self-update `syscall.Exec`'d the new binary in place, the listening socket passed by systemd was lost.
`LISTEN_FDS` was cleared at startup, and the fd duplicated by `net.FileListener` had close-on-exec set, so the new process had to `bind` itself while the port was still held by `personad.socket`; it exited with `address already in use`,
and had to wait for systemd's `Restart=always` to bring it back, needlessly burning one more start count. The self-update path also skipped the planned shutdown, so clients never got `hub_restarting`.

Now `personad hub run` (Linux, macOS) is an **edge**:

- The edge holds the hub's ports: the socket from systemd, or its own listener if there is none. It never lets go while the service runs.
- The hub itself runs as a **slot**: `personad hub slot`, a child process of the edge, listening on `~/.persona/run/slot-<a|b>-<port>.sock`. The edge runs its slots from its own program (`persona-hub hub slot` for an edge started as `persona-hub run`); a switch names the binary for the next one. `hub switch` without `--binary` names the program the active slot runs, whichever program runs the command (`persona-hub switch` or `personad hub switch`), so while a slot serves a switch never moves a hub between `personad` and `persona-hub`; a slot that went back to `<program>.prev` after a failed switch names `<program>`. With nothing serving it names `personad` (`hub.ServerBinary`), as before the hub could run as `persona-hub`. While the edge backs off a slot that keeps failing it refuses any switch, whatever binary it names, so that default only reaches a switch the edge takes once a slot is up again (the edge starting the slot again, or another switch handing over). On a hub that runs `persona-hub` it moves the hub to `personad` until its service restarts, so give `--binary` there.
- The edge forwards every connection byte for byte, first writing a PROXY v2 header stating the origin. So the slot's `RemoteAddr` / `LocalAddr` are still the client's address and the address it connected to,
  and trust decisions by origin (loopback, LAN, VPN interface, the user's own PROXY protocol upstream) work as before.
- TLS certificates, public-key pinning and the database belong to the slot; the edge only forwards TCP.

One switch (self-update, `personad hub switch`, deploy.sh), in order:

1. The new binary starts as the **standby** slot: it reports its version and build number, self-checks the config, verifies it can reach the database (`db.Check`, no migration), then waits.
   Meanwhile the old slot keeps serving; if the version is wrong, it fails to start, or it cannot reach the database, the new one is killed and the old one keeps running with no interruption at all.
2. The edge starts to **hold**: new connections queue at the edge instead of being refused. Then it tells the old slot to **release**, and it stops through the planned-shutdown flow: `hub_restarting`, hand over machines and the event log, 1001.
3. It tells the new slot to **activate**: migrate the database, take over machines, start listening, report ready. The edge switches routing over and releases the queued connections to it.
4. If the new slot fails to take over (migration failure, exit, timeout), it switches back to the previous version: `--prev`, or during self-update the edge swaps back the files recorded in the pending marker.

Other points:

- A connection still waiting, not yet accepted, in the old slot's socket queue when the old slot is released: the edge hands the bytes the client already sent (up to 64 KiB) unchanged to the next slot, and the client cannot tell.
  This replay does not happen when a slot crashes, because it may already have processed a request it had not answered.
- A slot exits unexpectedly: the edge holds connections and restarts it with backoff; after 5 failed starts in a row, the edge exits itself and leaves it to the service manager.
- The LAN address set in `public.tls_listen` is no longer on this machine (DHCP changed it): the edge does not hold that port, the slot cannot get its socket either (`ErrNoSocket`),
  so it starts normally with TLS off, the same as when the hub listens itself (`docs/assistant/onboarding.md` 4.1).
- The edge itself does not update automatically with the hub; it changes only on service restart (the unit changed, or `hub switch` finds the edge protocol older than the new binary, exit code 3).
  So the edge must stay very thin: forwarding, queuing, two slots, a control socket (`~/.persona/run/hub-edge.sock`, 0600).

Why not two hubs serving at once (that gateway's parallel switch): the hub has many things that can only exist once at a time: machine connections, assistant processes, bots, connectors, extensions, and database migrations.
Old and new coexist only during the standby self-check, with a 1–2 second hold at handover. Measured (NAS, 2026-11-06): one switch took 0.68 seconds, with 607 requests across 4 concurrent streams during it and 0 failures.

Cases that bypass the edge: Windows, Docker (image updates), `PERSONA_HUB_EDGE=0`, a data directory path that is too long (Unix socket address limit). In these the hub listens itself as before.
The in-place exec problem on this path is fixed too: the fds from systemd are kept (close-on-exec), the flag is cleared before exec, and `LISTEN_FDS` / `LISTEN_PID` are passed along (`execKeepingListeners` in `listen.go`: it holds `syscall.ForkLock` for reading from clearing the flag to the exec, so no process started meanwhile inherits the sockets, and sets the flag again when the exec fails).

### Planned shutdown (`hub/restart.go`)

SIGTERM no longer cancels the hub's ctx directly; it runs `beginStop` first:

1. Mark `stopping`;
2. Send every client `{"type":"hub_restarting","back_in_ms":3000}`, waiting up to 300 ms for it to go out;
3. Hand the `NodeInfo` and session lists of the machines online at this moment, as the managers reported them, to the next hub (its own settings, `hub_settings` `hub.devices_warm`), and write what each manager has (`hub.machine_entries`; a machine connected at this moment is last seen now);
4. Then cancel the ctx and stop in the original order: close the listener → close WebSockets with 1001 (reason changed to "hub restarting") → drain HTTP → wait for node connections → **hand over the event log** → close the database.

During shutdown, node connections drop, machines go offline and their interactions end; all of this is caused by the shutdown itself, so it is **no longer broadcast** (`detach` / `markOffline` check `stopping`).
Previously clients received a string of "machine offline" and "interaction ended" just before being disconnected, then saw it all come back after reconnecting.

### Event resume (`hub/eventlog.go`)

- Every message sent to every client through `broadcastTo` (`machines`, `interaction`, `interaction_resolved`, `todos`, `inbox`, `lark`, `revs`, `task_plan`,
  `assistant_feed`, `fleet`, `hub_update`, `plugin_ui`, `agents`, `mail_accounts`, `assistant_schedule`…) carries an `event_seq`, incremented by one per message within a stream.
  The hub keeps the most recent ones in memory (at most 2000 messages, 8 MB, 15 minutes).
- Per-connection messages do not go into the log: the connection snapshot (`machines`, `sessions`, pending interactions, `desktops`) is complete state in itself; `sessions_delta` is computed per connection; chat `items` have
  `since_seq` and terminals have `epoch` / `after_seq`, each resuming on its own.
- On reconnect the client sends `GET /api/v1/ws?event_stream=<stream>&event_seq=<last one>`. Under the same lock that registers the connection (broadcasts are numbered and sent under this lock too), the hub first sends `hello`
  (with `event_stream`, `event_seq`, `resumed`); when `resumed: true` it immediately replays what was missed, and only then live events: nothing is missed and nothing repeats.
  If the stream does not match, the missed events are no longer in the log, or there are more than 400 of them (the client's send queue is 512), `resumed: false` and the client does a full refresh as before.
- Across restarts: on shutdown the log (stream id, sequence number, recent events) is written to settings `hub.event_log`, and the new process **takes** it at startup (`DELETE … RETURNING`, read only once) and continues numbering.
  It is reused only if handed over within 10 minutes. After a crash there is no handover; the new process opens a new stream and clients do a full refresh, so they never get duplicate sequence numbers.
- Approval cards that need filling in (elicitation) are sent only to clients that declared the capability; during the reconnect replay the client has not sent `client_hello` yet, so the replay skips them and the pending-interactions snapshot after connecting fills them in.

### Machines and sessions (`devices/warm.go`; on the hub side `startDevices` in `hub/devices_reports.go`)

Since 2026-11-04 computers are connected by the manager ([machines.md](../server/machines.md) ("Who connects computers")): the manager is a child process of the hub and also receives SIGTERM when the hub stops.
It first hands the `NodeInfo` and session lists of the machines online at this moment to its next process (its own settings `hub.warm_machines`, plus each machine's engines `engines`), then disconnects them (without reporting them offline).
When the hub shuts down it waits for the manager to exit (as it used to wait for node connections, within the shutdown budget).

The new hub first **takes** its own `hub.devices_warm` (what the stopping hub saw the managers report, if handed over within 2 minutes; the hub never reads the manager's settings) and shows the machines in hub.json accordingly; until the manager's first report, calls to these machines (subscription replay, sending messages…)
wait for it (up to 30 s) instead of returning "offline" immediately. The new manager **takes** this record on startup, restores `NodeInfo` and session lists for the machines online at the time, shows them online as usual **for 30 s**, and reconnects the nodes in the meantime:
clients that reconnect do not see the Mac go offline and come back. If a node has not connected after 30 s, it is shown offline the old way (sessions marked unknown) and broadcast.
This warm start is display only: when the hub's self-update confirms "everything that was online has reconnected" before updating each node, it waits for the node to really connect (`connected` reported by the manager), the same as the hub's own warm start used to.
On the restart that upgrades to the split settings (#5457), the stopping hub has not written `hub.devices_warm` yet: for those seconds the hub shows the machines offline until the manager reports, while the manager's own warm start works as before.
The 30 seconds end for every manager's machines alike: those no manager reported by then show offline (sessions unknown) even when another manager is back.

Beyond the warm start, the hub keeps what each manager last reported of its machines (`hub_settings` `hub.machine_entries`, [managers.md](../assistant/managers.md) ("What the hub keeps of each manager")). A machine that is not shown as before (the hub crashed, or was stopped for longer than 2 minutes) shows offline with what it last was until its manager reports it: host name, OS, versions, and `last_seen`; the machines of a manager that stays away keep showing so.

### Text being generated

The hub remembers each session's last `session.delta` (cleared by an empty string, and when the machine really goes offline); when a client subscribes, the current `delta` follows right after the replayed items:
a reconnected client immediately sees the answer continue, without waiting for the next token.

### Chats not interrupted: hosting by the resident node (`node/assistant_keep.go`)

The assistant's claude no longer belongs to a single hub connection:

- The hub machine runs a **resident node** (`scripts/deploy/node-launchd.sh install`; on Linux the systemd user service `personad-node`, in its own cgroup).
  The hub still starts `personad node --stdio`, which only relays when it finds the resident node listening (`Relay` in `node/attach.go`). A hub restart only kills the relay process.
- In the resident node, when a connection ends, its Claude and Codex chats are handed to the process-level `assistantKeeper`; the next connection's `Node`
  takes them over (`adopt`) before loading state, and from then on the chats' output (streamed text, turn ends, background task changes) is reported to the new `Node`, that is, the new hub.
  Codex approvals resume with the same interaction ID and expiry through the next connection; disconnects do not imply refusal or permission. Ordinary and connector approvals remain separate. Explicit cancellation, expiry, and the bounded reconnect window still stop waiting.
  On takeover, the background task list and the text being generated are reported again. `Attach` waits for the previous connection to finish handing over before serving the next, so two `Node`s never compete.
- Output buffering: claude's stdout is always read by the same reader goroutine (it never blocks for lack of a hub); text being generated is in `p.partial`, finished text in the transcript;
  deltas from the 1–2 seconds without a hub cannot be sent, so after takeover one complete current text is sent instead (a delta is "the full text so far" anyway).
- Monitor and background commands are child processes of claude, and they keep running as long as claude does, so they **continue automatically after a hub restart** and wake the chat as usual when they finish.
  `recoverAssistantTasks` only sends "just restarted" when the chat process is really gone (for example, the node itself restarted).
- **The node updating itself** (a deployment swapped the binary): `deploy.sh` now uses `node-launchd.sh restart-idle`, which sends SIGHUP to the node's main process only
  (`systemctl --user kill -s HUP --kill-who=main`; on the Mac `launchctl kill SIGHUP`). The node waits until no chat is answering or waiting on background tasks,
  stops idle chat processes, and `exec`s the new binary (same PID, invisible to systemd / launchd). Idle chats `--resume` on their next message. Old resident nodes do not understand SIGHUP; they simply exit and are restarted, like the old `restart`.

Machines without a resident node (where the hub starts `node --stdio` directly over ssh and it handles everything itself) behave as before: a hub restart still interrupts chats, and background tasks still get "just restarted".

### Clients tolerate short disconnects

The shared client approach (done on web and Android; for iOS / Mac see the issue at the end):

- **Silent reconnect**: after a disconnect, do not change the UI at first. On `hub_restarting` or close code 1001: reconnect after 150–400 ms, then every 0.5–1 s, keeping the UI "online" for **30 s**;
  other disconnects: first retry after 300–600 ms, keeping the UI online for **8 s**, then the usual backoff and notices. If it reconnects within the grace period, nothing in the UI changes (no "Connecting" flash, no list refresh).
- **Resume**: remember hello's `event_stream` and the largest `event_seq` received, and send them on reconnect; with `resumed: true` there is no full refetch, only the usual resubscription of open chats (`since_seq`);
  if `resumed` is not true, do a full refresh as before.
- **Queued writes, idempotent replay**: sending messages and answers / approvals carry `X-Persona-Op-Id` (one UUID per logical operation, equal to `client_msg_id` when sending a message; the hub also honors this header for interrupt, close, rename,
  mode switch and new session, so clients can adopt it step by step).
  On network errors or a 502 / 503 / 504 not issued by the hub itself, stay "sending" and resend with the **same id** once the connection is back, for up to 60 s; when the WebSocket is down as well (the hub is redeploying), the web keeps waiting up to the 5-minute limit of "Hub is updating" and sends as soon as the hub returns (#4174); the hub returns the first result for an op id and never executes it twice.
  Real errors from the hub (with a `code`) still fail immediately.

### Nodes and extensions

- node: between the local resident node and the hub there is only a relay; after a hub restart it reconnects within 0.2 s and takes over the chats; ssh computers still reconnect in 2–8 s, hidden from clients by the warm start.
- A node that disconnects and comes right back (a resident node `exec`ing a new binary while idle takes about 2 s, the relay process is replaced): the running hub keeps showing that machine unchanged for **5 s**
  (`reconnectGrace`, `detach` in the manager's `devices/conn.go`); if it comes back nothing is reported, and only if it is still gone after 5 s is it shown offline as before. persona-link computers are the exception: a dropped link means the computer left, so they go offline at once.
- While a resident node is `exec`ing, its socket file still exists but nobody listens: `Relay` in `node --stdio` waits up to 8 s on "connection refused" (`dialResident`)
  instead of taking the connection itself, which would land the assistant's chats in the hub's cgroup again. Without a socket file (no resident node installed) it handles things itself immediately, as before.
  Node-side state (session state, background tasks) is in Postgres and loaded with `state.load` after connecting; the hub re-subscribes the subscribed sessions with `session.watch` (`since_seq`).
- Interactions (questions, approvals): resumable ones still wait for the hook to reconnect and reclaim them (`resumeGrace`, 3 minutes); shutdown no longer ends them first, broadcasts, then restores them.
- Extensions, connector programs: still child processes of the hub, restarted with it (login state lives in each extension's own directory, and the new hub launches them as soon as it is up). Requests to an extension fail until it is up, as before;
  the extension status clients see is updated by the new hub's `plugin_ui` pushes and the connection snapshot. Moving extensions out of the hub's cgroup would mean handling old and new hubs connected to the same extension at once; not done this time.
- The assistant's MCP tools (`personad mcp`) go to the hub through the node's `node.sock`. That socket belongs to the `Node` serving the current hub connection, and nobody listens during the one or two seconds of handover:
  `tools/list` already retries until the node is up (#1584); `tools/call` uses `mcpserver.callNode`, which retries every 0.5 s for up to 15 s only when it "cannot connect to the socket" (nothing has been sent yet). Calls already sent that hit the old hub's shutdown
  (including ones where the hub asks the phone to do something over WebSocket and waits for `tool_result`) fail, and claude gets a tool error and decides whether to retry. No automatic retry, because calls like sending an email must not happen twice.

### The manager while the hub is away (`assistant/outbox.go`, `hub/outbox_relay.go`)

A manager that is not the hub's child keeps running when the hub stops: one made in the app, or the assistant with `assistant.external`. It keeps doing its own work: sources, triage, scheduled tasks, and the computers it reaches directly (this machine, ssh, tailcat). What it would have handed the hub meanwhile is kept, not lost.

- **What is kept.** The pushes it decides on (`notify.request`: an agent finished, a card that needs you, a conversation's `notify_user`, a reminder) and the apps' live updates (`ws.broadcast`), its extensions' included.
  - They go into its own database, table `manager_outbox`, byte for byte and in the order made.
  - Items are kept while the hub is not connected, and while older items still wait, so nothing overtakes them.
  - A push or update whose sending got no answer from the hub is kept too: the bus dropped, the call timed out, or the write failed. It may have arrived.
- **Bounds.** Each kind is bounded on its own, so live updates never crowd out pushes: at most 1000 pushes and 200 updates, oldest out first.
  - A push older than 24 hours is no news any more.
  - A live update older than 10 minutes goes too. Apps of a hub that was away longer start a new event stream and load everything again ("Event resume"), and an old update would only take them back.
- **What is not kept.** What the hub shows of the manager is reported whole on every connection anyway: the snapshot, the machines' sessions, the cards, the Live Activity.
- **Handing over.** On every connection the manager first reports its state, its extensions (once they started, at most a minute) and its cards: a card's push needs the card, and an extension's update the extension. Only then does it send the outbox, oldest first, in batches (100 items, 4 MiB), with `manager.outbox {items:[{id, at, notification | message}]}` → `{done:[id…]}`, and it forgets what the hub took.
- **Each item once.** The hub delivers each item as it would have then, only about the manager's own machines, sessions and cards, as live. It delivers each id once per manager:
  - It records what it took in its database (`handed_over`, kept for two hours), so neither it nor the next hub process delivers an item again. That covers an answer that was lost, and a push sent live that also comes from the outbox. `notify.request` checks the same records.
  - An item the hub refused, such as a card's push whose card was resolved, is answered done like the rest (the manager forgets it) but not recorded: if a manager sends it again, it is looked at again.
  - A hub from before `manager.outbox` answers `unknown_method`. The manager then sends the items one by one, as `notify.request` and `ws.broadcast`, without the hub's check for repeats.
- **When the outbox is used.** A manager the hub starts as its child stops with the hub. Its outbox only matters when the hub's connection drops while both run, or when the database still has items from an earlier run; those are handed over at its next start.
- **The computers' own side.** A resident node keeps its scripts' notifications (`personad notify`) until its manager takes them (`spooldedupe.go`); that is the device's own buffering, step 4.

## Deployment and rollback

Two deployment changes:

| Change | How | Rollback |
| --- | --- | --- |
| Hand the hub's listener to `personad.socket` | `scripts/deploy/hub-systemd.sh` (deploy.sh runs it on every deployment): write `personad.socket`, add `Wants/After=personad.socket` and `TimeoutStopSec=20` to the service; stop the service briefly the first time to hand the port to the socket | `PERSONA_HUB_SOCKET=0 scripts/deploy/hub-systemd.sh`: remove the socket unit, stop the socket, let the hub listen itself; or manually `systemctl --user stop personad; systemctl --user disable --now personad.socket; rm ~/.config/systemd/user/personad.socket; systemctl --user daemon-reload; systemctl --user start personad` |
| Install a resident node on the hub machine | `scripts/deploy/node-launchd.sh install` (once), then deploy.sh uses `restart-idle` | `scripts/deploy/node-launchd.sh uninstall`; on the hub's next reconnect it goes back to starting `node --stdio` itself |

Unchanged: Docker deployment (`deploy/docker/`, no `LISTEN_FDS`, still listens itself), the hub on Mac (launchd), the database and data.

After blue-green switching ("Blue-green switching" above):

- Before replacing the hub binary, deploy.sh saves the old one as `personad.prev` (and `persona-hub.prev`, `persona-manager.prev` for the two programs it puts beside it).
- When the unit has not changed and the hub is running, `hub-systemd.sh` first runs `personad hub switch --binary <personad> --prev …` (the program it means, not the default):
  - Exit code 0: switch complete, restart nothing;
  - Exit code 1: the new version could not take over, the old one keeps serving; the script restores `personad.prev` and deployment fails;
  - Other exit codes: no edge (the first deployment of this version, an old binary), or an older edge protocol; restart the service the old way.
- The unit keeps the program it runs ([../server/server.md](../server/server.md) ("Programs")). The script reads the unit's `ExecStart` as systemd does and as the hub's self-update does (`unit_program`; one table of forms tests both): the last one in `[Service]`, the drop-ins' after the unit's, spaces around the key's `=`, the first word unquoted (double or single quotes), `%h`, `%%` and `$$` replaced, compared by real path.
  - A unit that runs this data directory's `persona-hub` stays `persona-hub run`, and the switch is `persona-hub hub switch --binary persona-hub --prev persona-hub.prev`; when that fails, `personad`, `persona-hub` and `persona-manager` all go back to their `.prev`. Without `persona-hub.prev` (the script run outside deploy.sh) there would be nothing to go back to, so the service restarts on the build on disk instead of switching.
  - Every other unit is written for `personad` as before.
  - It stops with an error and leaves the unit as it is when the unit runs a `persona-hub` that is not there, or when its `ExecStart` names a `persona-hub` other than this data directory's (a wrapper such as `sh -c '…/persona-hub run'`, another copy, a bare name).
  - A `.prev` goes back through a symlink onto the file it names (an install under the former name keeps `personad` as a link to that file), so both names run the old build. A program whose `.prev` could not be put back (a full disk, a directory left where its copy goes) is named in the error, still on the new build; the others go back all the same.
  - The script never moves an install from one program to the other.
- Mac (launchd) also tries switching first, then `kickstart` if that fails. It finds the agent that runs this data directory's `personad hub` or its `persona-hub` (`run`, or no arguments; the program is `Program` when the agent has one, else `ProgramArguments[0]`, as launchd runs it) and switches with that program, given with `--binary`.
- Rollback: run `PERSONA_HUB_EDGE=0 scripts/deploy/hub-systemd.sh` on the hub machine. It writes this variable into the unit's `Environment=`, and after the service restarts the hub listens itself;
  later deployments preserve it until you run it again with `PERSONA_HUB_EDGE=1`. When running manually, set this environment variable directly.

Edge caveats:

- `systemctl --user stop personad` stops the edge, which first releases the slot and then exits (`TimeoutStopSec=20` is enough).
  A stuck slot is killed only after 20 seconds; when killed, extensions in their own process groups may survive, so check `ps` before the next start.
- Both slots write to the same log during handover, with `[slot a]` / `[slot b]` before each line; the edge's own lines start with `hub edge:`.

Socket activation caveats:

- **Roll back the socket before switching to an old binary**: an old hub that does not understand `LISTEN_FDS` calls `net.Listen` itself while the socket holds the port, fails to start (`address already in use`) and keeps restarting.
  Run `PERSONA_HUB_SOCKET=0 scripts/deploy/hub-systemd.sh` first, then replace the binary.
- **`systemctl --user stop personad` does not keep it stopped**: the socket is still listening, so the next connection starts the service again (normal socket activation behavior).
  To really stop: `systemctl --user stop personad.socket personad`.

## Verification

- Blue-green switching:
  - `internal/hubedge` tests (Linux / macOS): the test binary plays the slot itself. Covers source addresses, zero failed requests during switching, concurrency across 6 consecutive switches, a new version that cannot start,
    rollback after failed takeover, a slot requesting its own switch, rejection of a wrong build number, rollback of an unconfirmed update, and restart after a crash.
  - `TestUpdateHandsOverToTheEdge` and others in `hub`; `scripts/test/test_hub_switch_deploy.py` (hub-systemd.sh's three outcomes).
  - End-to-end test with real personad: private Postgres, v1 → `hub switch` → v2, with continuous requests to `/api/v1/health`, then try a binary that cannot start.
- Unit / integration tests (`server/`): `TestEventLogSince`, `TestHelloResumes`, `TestStampEvent`, `TestListenInherited` (pass fd 3 to a child like systemd),
  `TestEventLogHandOver`, `TestRestartIsPlanned` (actually runs `hub.Run` twice: the client first receives `hub_restarting` and 1001 "hub restarting", then the next hub continues the same stream with `resumed: true`),
  `TestResidentNodeKeepsConversations` in `node` (the chat process survives the connection ending, and the next connection's `Node` receives its output after takeover).
  Web: `src/lib/reconnect.test.ts`, `store-reconnect.test.ts`, `ops.test.ts`; for Android see `docs/android/android.md`.
- Repeated restarts: run `scripts/test/hub_restart_seamless.py --restarts N --converse` on the hub machine:
  - Request `/api/v1/health` every 100 ms; any refused / failed request is a failure;
  - A WebSocket client following the web client's behavior: each broadcast's `event_seq` must follow the previous one (no loss or duplicates), every reconnect after the first connection must be `resumed` on the same stream,
    and the time from disconnect to the next hello must stay within the client's grace period (default 8 s);
  - `--converse`: open a new **side chat** (leave the main chat alone), have it run `sleep 75` in the background while outputting 1 through N line by line; restart during output, then check that every number is present,
    the background command's result came back, and the chat received no "just restarted" message.
  - The last line is `PASS` / `FAIL` / `TIMEOUT` and the numbers.

After socket activation and the resident node were deployed (#1617), repeated restarts with a streaming side chat passed on a real hub: no failed health probe, every reconnect resumed on the same stream with no lost or duplicated event, and each disconnect lasted about a second, well inside the clients' grace period.
Before the change a restart refused connections for about 1.3 s and killed the local node's assistant processes. The numbers are in `docs/archive/operations/hub-restart-records.md`.

## iOS / Mac (#1634)

Same rules as web and Android; the Apple implementation (shared by iOS and Mac):

- Pure logic in `apple/Shared/Reconnect.swift`: `LinkDrop` (8 s / 30 s grace, reconnect timing), `EventCursor` (resume sequence), `FrameMeta` (reads `event_seq`, `event_stream`, `resumed`, `hub_restarting` while decoding),
  `ReconnectPlan` (no refetch after reconnect when `resumed`), `OpRetry` (retry with the same op id, at most 60 s; `Error.isGap` determines whether the request might not have reached the hub).
- `LiveConnection` records disconnects (`drop`, `inGrace`, `graceOver`), and `ConnectionStatus` treats it as connected during the grace period; close codes come from `URLSessionWebSocketTask.closeCode` (closing it ourselves on a heartbeat timeout does not count as 1001).
- When `resumed`, `AppModel.didConnect` only registers the phone and resubscribes; sending messages (`SendSafety.sendPending`) and answers (`AppModel.resolve`) carry op ids. For UI behavior see `docs/apple/ios.md` ("Connection status").
- Verification: `PersonaTests/HubRestartTests` (includes a loopback WebSocket using Network.framework: `hub_restarting` + 1001 → grace, reconnect within 1 second, `resumed`);
  mock hub `MOCK_RESTART=1` (`GET /mock/restart?gap_ms=…&missed=…&drop_post=1` simulates a planned restart, `docs/operations/testing.md`) with a simulator to inspect the UI and requests.
