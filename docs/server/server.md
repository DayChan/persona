# Server: personad

`personad` is the new name; `personad` remains a compatibility alias. New default runtime directory: `~/.persona`; old `~/.persona` remains supported. Existing systemd / launchd, database / container / volume, and release-file identities in this document are retained rather than rebuilt for the rename. Migration-version differences: [Configuration and compatibility](../operations/config.md#命名与兼容).

Go, single binary, `server/cmd/personad`. Modes: `docs/server/architecture.md`; protocol: docs/protocol/protocol.md.

## Programs

The server's code ships as these programs (`server/cmd/`), all of one build (`version -v` matches):

- `personad` (alias `jarvisd`): everything, and the compatibility entry. Every command it ever had keeps working (`personad hub …`, `personad assistant`, `personad manager …`), and the services installed before the programs below run it. Its device commands (`node …`, `hook`, `mcp`, `ask`, `notify`, `tool`, `launch`, `codex-start`, `tab-start`, `office`, `sysmon`, `node-update-check`, `node-update-watch`) are `internal/nodecmd`'s, its hub commands `internal/hubcli`'s, its managers' commands `internal/managercli`'s.
- `persona-hub`: the hub alone, `internal/hubcli`: `persona-hub [run]`, `switch`, `setup`, `pair`, `peer`, `doctor`, `admin`, `manager-mode`, `manager-code`, `assistant-token`, `update-watch`; `persona-hub hub <command>` works too (what the edge and older scripts run).
- `persona-manager`: the managers alone, `internal/managercli`: `assistant` (the built-in one; a service of its own with `PERSONA_ASSISTANT_DEPLOY=service`, bound by `join --builtin`, [managers.md](../assistant/managers.md) ("The built-in manager as its own service")), `join`, `run`, `install`, `uninstall`, `status` (one made in the app; `persona-manager manager <command>` works too), `extension …`, `voice`, `tailcat-key`, and `node …`: this computer's node, when the manager runs it as itself. The manager starts the node as `persona-node` or `personad` of its build beside it, and as itself only when neither is there or with `PERSONA_SPLIT_PROGRAMS=0` (`devices.Machine.localNode`, under `persona-node` below).
- `persona-node`: the device's program, `internal/nodecmd` alone. It takes the same commands and arguments and gives the same output and exit codes as `personad`, and links neither the hub, the manager nor the database (`cmd/persona-node/main_test.go` guards that on every platform it ships for). The node writes `<its own binary> hook` / `mcp` into the settings it manages, so either program serves what it wrote. The node of the hub's computer, which the manager starts, runs as the first program beside the manager's that is the same build (`version -v`): `persona-node`, then `personad`; else as the manager's program itself (`devices.Machine.localNode`). So a manager run as `persona-manager` starts `personad node --stdio`, and the hook and MCP entries the node writes keep naming `personad`. `PERSONA_SPLIT_PROGRAMS=0` in the manager's environment keeps the node on the manager's program. Nothing puts a `persona-node` on the hub's computer yet (a later change), and the hub's self-update does not swap one: one put there by hand of another build is passed over, with a line in the manager's log.
- `persona-link` (directory alias `cmd/persona-link`): the connector, `docs/operations/link.md`.
- `personad-wechat`: the WeChat bot, a built-in extension's program (`docs/extensions/wechat.md`).

The hub and the managers apart:

- **Each without the other's code.** `TestPersonaHubDoesNotLinkTheManager` (`cmd/persona-hub`) lets the hub's program link only the hub's own packages and the shared ones (`internal/depguard`: `HubOwn`, `Shared`); `TestPersonaManagerDoesNotLinkTheHub` (`cmd/persona-manager`) keeps the hub's own packages out of the manager's. Both check the four release platforms and Windows. The two talk over the bus only. The hub's one exception for now (`HubExceptions`, which may only shrink): `internal/extension` and what it pulls in, for the looks it serves, until extensions are a part of their own.
- **Released together.** The release, a direct deploy (`scripts/deploy/deploy.sh`) and the Docker image put `persona-hub` and `persona-manager` beside `personad`, and the hub's self-update swaps them with it (`update.ProgramNames`, [hub-update.md](../operations/hub-update.md) ("Programs swapped together with personad")): two more downloads and `.prev` copies per update. `install.sh` installs the two with `personad` on the computer that runs the hub (`--hub`, a `hub.json`, or the two there already), each downloaded and checked before any is put in place; other computers get neither. A release that lacks one leaves the file there as it was, unless a service runs it: then the self-update and `install.sh` refuse it ([hub-update.md](../operations/hub-update.md) ("A release without `persona-hub` or `persona-manager`")).
- **Setup writes services that run them, on Linux.** `hub setup` writes the hub's systemd unit to run `persona-hub run` and `manager install --builtin` (which setup runs) the built-in manager's to run `persona-manager assistant`, each when that program is beside `personad` and the same build (`version -v`, `update.SplitBeside`); otherwise, or with `PERSONA_SPLIT_PROGRAMS=0` in their environment, `personad hub` and `personad assistant` as before. Setup's report says when it passed `persona-hub` over and why, and `manager install --builtin` prints the same for `persona-manager`. A rerun of setup (the install command with `--hub` runs it again) moves a service it wrote to the program it would write now and restarts it, and its report says so, and that the built-in manager restarted with the hub when the hub starts it as its child. The opt-out stays in the units it was given to (`Environment=PERSONA_SPLIT_PROGRAMS=0`), so what the hub and the manager start stays on `personad` too; a later rerun without it moves the services back to the programs. The drop-in `90-persona-compat.conf` that `hub-systemd.sh` adds over a unit setup wrote is moved with it; another drop-in that sets `ExecStart` (one of your own) is left, and setup says which program the service really runs and how to change it.
- **Macs stay on personad, for now.** On a Mac the hub's launchd agent (`com.persona.hub`) and the built-in manager's (`com.persona.manager.assistant`) run `personad hub` and `personad assistant`, with the programs beside `personad` or not (the one exception: `persona-manager install --builtin` with no `personad` of its build to run, [managers.md](../assistant/managers.md) ("The built-in manager as its own service")); setup's report and `manager install --builtin` say so when they are there. macOS holds what an agent starts, the node of the hub's computer among them, to the agent's program for its privacy permissions (Full Disk Access), so the agents move to `persona-hub` and `persona-manager` in a later change, together with that node. A rerun moves an agent that runs either program (one edited by hand) back to `personad`; an agent of your own that runs this data directory's `persona-hub` is still taken as the hub's and left as it is. `PERSONA_SPLIT_PROGRAMS` means something on Linux and in Docker only.
- **Some services stay on personad.** The hub's unit, or the built-in manager's unit or agent, under the former name keeps its program (`compatibility_service.go`, `manager/compatibility.go`), and so do the managers made in the app (`manager install`: `personad manager run`). Nothing else moves an install: a deploy keeps the program the hub's unit runs (`scripts/deploy/hub-systemd.sh`, [hub-restart.md](../operations/hub-restart.md)), and so does a self-update.
- **A hub run as persona-hub stays persona-hub.** It starts the built-in manager as `persona-manager assistant` beside it when that is the same build (`version -v`), else as the `personad` beside it (`hub.managerBinary`). A self-update replaces `personad` (the release's main download, `hub.ServerBinary`) with the two programs and goes on as the swapped `persona-hub`: the restart, the edge's hand-over and every way back (the new process, the edge, the watchdog) run it (`run` in `pending.json`; a build that does not know `run` brings the hub back as `personad hub run`, the same hub). `hub switch` without `--binary` goes on with the program the edge's active slot runs, from either program, so while a slot serves it never moves a hub between `personad` and `persona-hub`; with nothing serving it names `personad` (`hub.ServerBinary`), as before. The edge refuses any switch while it backs off a failing slot, so that default only reaches a switch it takes once a slot is up again; on a hub that runs `persona-hub` it moves the hub to `personad` until its service restarts, so give `--binary` there.
- **One container in Docker.** The hub image runs the hub and the built-in manager as two processes under `personad supervise`, each restarted on its own, as `persona-hub run` and `persona-manager assistant` unless `PERSONA_SPLIT_PROGRAMS=0` ([docker.md](../operations/docker.md) ("Programs")).
- **Two services on a new install.** `hub setup` installs the hub's service and the built-in manager's own (`persona-manager-assistant.service` running `persona-manager assistant`, or on a Mac the launchd agent `com.persona.manager.assistant` running `personad assistant`, each with `PERSONA_ASSISTANT_DEPLOY=service`), and binds the second to the first with a one-time code. Existing installs keep the hub's child until `hub setup --manager-service` ([upgrade.md](../operations/upgrade.md): every install type, step by step). See [managers.md](../assistant/managers.md) ("The built-in manager as its own service").

## Code

| Package | Content |
| --- | --- |
| `internal/proto` | Wire types |
| `internal/rpc` | Bidirectional hub↔node JSON-lines RPC |
| `internal/transcript` | Claude Code JSONL → `proto.Item`, incremental reading and pagination |
| `internal/herdr` | Herdr socket API client (CLI fallback) and event subscription |
| `internal/herdrcore` | JSON-RPC client and events for herdr-core (hc behind Herdr desktop), `docs/plans/plan-orca-support.md`, “herdr-core host” |
| `internal/node` | Sessions, launch, send, screen sampling, hook / MCP Unix sockets, `runtime.sock` (`runtimes.go`, `docs/integrations/runtimes.md`) |
| `internal/hub` | HTTP / WebSocket, machine connections, interaction queue, phone-tool forwarding, APNs |
| `internal/assistant` | personad assistant for Persona tab; separate process connected through bus, hub child by default; `docs/assistant/assistant.md`, “Process” |
| `internal/assistantwire` | Assistant / hub messages: types, methods, error codes, assistant route table; both import it, hub does not import `internal/assistant` |
| `internal/bus` | Service-to-hub bus client: dialing, handshake, reconnect, keepalive; `docs/protocol/protocol.md`, “Bus” |
| `internal/netaddr` | Local addresses, Linux netlink flags and address-change notifications; not proof of public reachability |
| `internal/db` | Postgres connection pool, embedded SQL migrations in two sets (`migrations/hub/`, `migrations/manager/`; “Migrations” below), table access |
| `internal/mail` | IMAP/SMTP, encrypted account storage, mail HTTP APIs and MCP tools (`docs/extensions/mail.md`) |
| `internal/mcpserver` | personad mcp: stdio MCP server for Claude |
| `internal/nodecmd` | The device's command line (`personad` and `persona-node` both dispatch it) |
| `internal/nodeclient` | The client side of `node.sock` for hook / mcp / ask / notify / tool |
| `internal/apns` | APNs client (.p 8 key, provider token, single push), shared by direct hub and relay |
| `internal/pushcrypto` | Push-body encryption with phone public key (HPKE), `docs/operations/push-relay.md` |
| `internal/relay` | Publisher-run personad relay, `docs/operations/push-relay.md` |
| `internal/tunnel` | Tailcat node key / endpoint, hub dialing, new-binary transfer over tailcat (“Tailcat hole punching” below) |

## Client traffic diagnostics

The hub writes privacy-safe `traffic` minute buckets to its normal journal. These are
transport diagnostics, not application state or a delivery receipt. No extra API,
database table, request body, device name, literal dynamic path, query, bearer token,
or push token is introduced. Credential and registration IDs are independently
hashed with a fixed domain separator; use the device registry's credential binding
when joining them, never a display-name match.

- Authenticated registered-client requests have `action=started` counts on receipt
  and `action=completed` counts on return, grouped by the ServeMux route template,
  platform, surface and response status. `source=app|widget|liveactivity` is inferred
  from bounded client headers and User-Agent markers; `platform_basis` and
  `source_basis` retain the evidence. Native Apple UAs use the enrolled device kind
  or iPhone push binding when their OS is absent. Browser UAs count as `web` even
  on phones; curl/Python/Go clients stay diagnostic rather than borrowing a phone
  credential's platform. Raw UAs/headers never enter logs. `native-default` means
  App is inferred from a native UA with no extension marker, not hardware/process proof.
  Sum **started only** for request volume; completed
  counters supply duration and response-size measurements. Failed authentication is
  still covered by the separate rate-limited security log, not attributed to a device.
- `body_bytes` and `max_body_bytes` count application response bytes before gzip,
  or WebSocket application-frame bytes after a successful write/read. They are not
  TLS, compression or physical wire-byte measurements. WebSocket ping/pong buckets
  count attempts/replies and the latter's elapsed time; the persisted device-session
  history retains connection duration and observed close reasons.
- Push buckets distinguish alert, background categories and Live Activity
  start/update/end, with logical attempt and accepted/failed actions. A successful
  transport retry produces one terminal result for the logical send; do not add
  attempts and accepts together. Acceptance does not prove iOS delivery or wake-up.
- Completed minute buckets are emitted at most once per minute per key; the final
  partial bucket is emitted on orderly shutdown. Active key cardinality is bounded,
  with overflow counts retained without identities. An abrupt process loss can
  lose the still-buffered minute; telemetry cannot reconstruct earlier unlogged
  successful HTTP requests or Live Activity updates.

`python3 scripts/diag/hub_traffic_report.py --since <UTC start> --until <UTC end>
--identity-file <private identity JSON> --output <new private report JSON>` reads
minute buckets and device-session history without changing production state or waiting.
The identity file contains `{"identity":"<16-hex opaque credential hash>"}`, never a
bearer/push token. Specify `--unit`/`--container` for an installation whose service or
container name differs from discovery; sanitized exports can use `--journal-file`
and `--connections-file`. Collect two minutes after the end so the last bucket has
flushed. The output separates platforms/surfaces/types/routes/hours, start vs completion
counts, and incomplete/unknown attribution; connection reasons remain observed reasons.

Live Activity settings refresh only the affected device's projected content.
Same-value retries and non-activity preferences do not force a fleet-wide resend.
Foreground suppression applies to both activity updates and push-to-start; a start
suppressed in the foreground remains eligible after the last matching phone socket
leaves. Important interaction alerts and the existing urgent activity fast path
remain unchanged.

## Connecting to herdr

- Node directly uses herdr Unix socket (`$HERDR_SOCKET_PATH`, otherwise `$XDG_CONFIG_HOME/herdr/herdr.sock`, default `~/.config/herdr/herdr.sock`). Each call opens a connection, writes one `{"id","method","params"}` line, reads one response; 64 MB line cap, 10-second timeout. Herdr CLI is a thin client for the same API, with an extra ping per call; result shape identical. Socket failure or old herdr unknown method (`invalid_request` + `unknown variant`) falls back to CLI. Method names follow `herdr api schema --json`; pane run = `pane.send_input` + `keys:["Enter"]`; `pane.process_info` requires `pane_id`, since target would return focused pane.
- `events.subscribe` uses a persistent connection: workspace / tab / pane add, remove, update events plus `pane.agent_status_changed` per Agent pane (herdr subscribes to status by pane only). Events trigger `pollAgents` snapshot after 50 ms coalescing, at most one list rebuild per second. Entering / leaving blocked bypasses that limit, snapshot after 50 ms (“Card fast lane” below). Normally 10-second fallback poll. Without event stream, or with an internal Codex-variant pane whose status changes only spinner characters in title without `pane.updated`, poll each second.
- Subscription pitfalls: a second subscribe on the same connection is silently ignored; reopen with full pane set when it changes. Herdr closes lagging subscribers; `events_lost` may not arrive, so reconnect on closure with exponential backoff up to 30 seconds, snapshot first to fill gaps. Herdr 0.9 event names use dots like subscription (`pane.agent_status_changed`); older versions use underscores (`pane_agent_status_changed`); accept both.
- Local measurements (herdr 0.9.3, 7 Agents): no events over 30 stable seconds; tab rename → `sessions.changed` 80–140 ms, previously up to 1 second. Integration: `./scripts/test/test-herdr.sh`, isolated herdr session, `docs/operations/testing.md` (“Herdr integration testing”).

### Card fast lane (#751)

Question, permission, and plan cards, including appearance, answer, replacement, disappearance, pass straight through every hop without throttling, coalescing, or batching. Other changes remain throttled.

| Hop | Fast path |
| --- | --- |
| Herdr detects Agent waiting for user | Herdr itself, measured 0–320 ms |
| Node reports hub | Enter / leave blocked events bypass one-per-second list rebuild; snapshot and `sessions.changed` after 50 ms coalescing |
| Hub creates card | No delay for unhooked sessions; hooked sessions wait 1.5 s (`docs/protocol/protocol.md`, “Terminal dialogs”). Read screen, parse, broadcast interaction immediately |
| Push to app | WebSocket immediately queues per connection; APNs sends immediately; Needs you Dynamic Island changes are not coalesced or limited to 30 s interval |
| App renders | Apply to model on receipt; Needs you / questions land even while Agents list updates are held (`landCritical`, `docs/apple/ios-state.md`); chat reads model directly |

Measurements (2026-11-03, isolated herdr + real Claude Code 2.1.317, no Persona hook; node / hub in same process linked like SSH, `hub/cardlatency_live_test.go`):

| | Before | After |
| --- | --- | --- |
| Terminal dialog appears → app receives card | 4.0–10.4 s, 6 runs | 0–0.35 s, 8 runs; screen sampled every 25 ms, similar uncertainty |
| Herdr detection → hub knows blocked | 2.2–8.9 s | 0–0.16 s |
| Hub card creation | 1.5 s | 0 ms |
| Answer on computer → card disappears | 8.5 s | 0.17–0.59 s |
| Answer one question → next card, two questions in one AskUserQuestion | No new card | 0.61–0.72 s, including keys |

Sources of delay:
- After #611 merged herdr / Herdr App engines, the select waking `pollAgents` consumed the herdr event flag, then used it to find changed engines. None found, no snapshot; only 10-second fallback remained.
- Herdr 0.9 dotted events were not recognized by underscore-only node code, losing status-event identification.
- Hub always waited 1.5 s per screen card for a hook card that unhooked sessions would never send.

Measurement: `HERDR_SOCKET_PATH=<isolated herdr socket> PERSONA_LIVE_CARD_PANE=<Claude pane> go test -tags herdrlive -run TestLiveCardLatency ./internal/hub/`
(`PERSONA_LIVE_CARD_TWO=1` asks two questions; `PERSONA_LIVE_CARD_TRACE=1` prints hop timeline). Without local Go, cross-compile test binary on hub and copy back.
App hop guarded by UI tests: `ScreenTruthTests.testWhatNeedsTheUserShowsUnderTheFinger` (within 1 s even with finger held on list), `LiveUpdateTests.testAScreenCardInTheOpenConversation` (appears within 1 s, disappears within 1 s of computer answer).

## Connecting to herdr-core

Each machine's node uses one engine (`node/engine.go`): herdr default, herdr-core (`node/hostherdrcore.go`), or Orca (`node/hostorca.go` below). User chooses per machine in app; hub sends `engine.use` on connection (`docs/protocol/protocol.md`, “Engines”). Herdr mode is unchanged and does not touch herdr-core, except `node.info` checks availability once. Selected but stopped herdr-core returns No Terminal immediately, without dialing / logging. Socket, methods, mapping, and first-phase omissions: `docs/plans/plan-orca-support.md` (“herdr-core host”).
Integration: `go test -tags hclive -run HerdrCoreLive ./internal/node/ -v`, own isolated herdr-core plus read-only default-instance listing.

## Connecting to Orca

For Orca machines, node controls terminals through desktop runtime Unix socket (`internal/orcart` client, `node/hostorca.go` host). Read `orca-runtime.json` for socket / token; Orca changes token on every launch, so reread on connection failure / unauthorized; `status.get` checks protocol version first.
No socket push: node polls `terminal.list` + `worktree.ps` each second, mapping Claude panes via `ORCA_PANE_KEY` in process environment.
Stopped Orca returns No Terminal immediately, no log. Mapping / first-phase omissions: `docs/plans/plan-orca-support.md` (“Orca source”).
Read-only integration: `go test -tags orcalive -run OrcaLive ./internal/node/ -v`.

## Deployment

Two equivalent hub deployment modes: **native binary** (below, `systemd --user` + Postgres container) and **Docker** (hub + Postgres containers, for NAS / hub-only computers, `docs/operations/docker.md`). Nodes remain `~/.persona/bin/personad` regardless of hub deployment.

New users without repository: `curl -fsSL <website>/install.sh | sh -s -- --hub`, then personad hub setup ([onboarding.md](../assistant/onboarding.md), 4.1). Writes `hub.json` (new-install defaults only for new files), creates same-name / same-volume Postgres as `db.sh` or uses existing configured database, installs same socket-activation units as `hub-systemd.sh` (Mac LaunchAgent `com.persona.hub`), waits for hub, binds and installs the built-in manager's own service (a new install's; `--manager-service` for an existing hub), prints only problematic self-checks, then phone QR code and admin URL with #setup=. Safe to rerun: retain existing values and do not restart unchanged services. `--json` gives one line per step for Persona Link.

```sh
./scripts/deploy/deploy.sh                    # Build linux / darwin × amd64 / arm64, install native binary on each machine, restart hub
./scripts/deploy/deploy.sh <machine id> <machine id> # Install selected machines only
```

- Binary: each machine `~/.persona/bin/personad`. Hub launches node through `ssh <alias> ~/.persona/bin/personad node --stdio`; no persistent Mac service required. Platform from configured os / arch; if absent, query machine `uname -s` / uname -m. Run version after installation; failure reports error, not installed. All four binaries also live in hub `bin/nodes/` (`personad-<os>-<arch>`, with `persona-node-<os>-<arch>` and its `.version` from `deploy.sh`); app Redeploy selects machine's platform and the kind of program its node runs, and the manager fetches the release's build where the hub has none (`docs/operations/config.md`, `docs/operations/node-update.md` ("Node packages")).
- Hub: `systemd --user` personad on hub machine; HTTP listen default `:18660`, TLS via `public.tls_listen`, e.g. :18662. Expose only TLS publicly. IPv6 inbound, firewall, TUN return-path troubleshooting: [Network](../operations/network.md). Logs: journalctl --user -u personad -f. Docker hub is not managed by `deploy.sh`: omit hub machine from machines, or deploy only computers with `deploy.sh` <machine id>. Upgrade pulls image; Redeploy uses the image's own platform's build and fetches the others from the release (`docs/operations/docker.md` ("Node builds")).
- Replacing node binary does not reconnect existing hub links automatically. `deploy.sh` <hub machine id> restarts hub, or Reconnect on app Machines page.
- Admin scope (`<hub address>/admin`, [admin.md](../operations/admin.md)): `deploy.sh --admin-access <loopback|lan|cidrs|any>`, plus `--admin-cidrs a,b` for cidrs. If omitted and never chosen on hub, ask in terminal; without terminal default lan with warning. Later use personad hub admin access to view / change, `personad hub admin reset-password` to reset admin password. First-run local setup code in hub logs, `admin: first-run setup code …`, or `personad hub admin setup-code` ([admin.md](../operations/admin.md), “First-run setup”).

### Lightweight hub and predeployment checks

A lightweight hub, e.g. 2 cores / 2 GB, can run hub + Postgres only, with Agents on execution computers. Before adding those, explicitly set `"machines": []` in `hub.json` to avoid default local node when omitted. Initially disable `inbox.agent.enabled` / `kb.enabled` in the built-in manager's `managers/assistant/manager.json`; enable after data-source integration as needed. Deployment guidance only, not a load-tested minimum hardware requirement.

For Linux user services to survive SSH logout, administrator runs loginctl enable-linger <user>.
After Docker installation, verify `postgres:18-alpine` can pull; reachable OS repositories do not prove Docker Hub reachable. Failed pulls must not count as initialized DB / deployed hub, and never print `hub.json` tokens / database URLs.
If Docker Hub is unreachable in China, try official Docker image from AWS public registry, `public.ecr.aws/docker/library/postgres:18-alpine`; after successful pull retag `postgres:18-alpine` before database script.

Use HTTPS reverse proxy on a separate public domain; hub / database listen only on loopback. Nginx must forward WebSocket Upgrade / Connection, disable response buffering, extend read timeout, and avoid token-bearing query strings in access logs. For existing-domain subpaths (location /<prefix>/ strips prefix before hub), add proxy_set_header X-Forwarded-Prefix /<prefix> so web works ([web.md](../web/web.md), “Subpaths”). Immediate requests after Nginx reload may hit old workers; wait for config activation before verifying cert, auth APIs, WSS 101. Free cloud-console certificates must be reapplied / installed before expiry; existing certbot timers do not renew them.

PowerShell piping shell scripts may append CR, causing remote `$'\r': command not found`. Upload UTF-8 files or base64-encode and decode remotely; do not rely on pipeline newline encoding.

## NAS (Docker) deployment and upgrades (compatibility example)

This records existing deployment operations. `~/.persona`, personad, container / image names, and backup filenames retain old compatibility identities, not new-install defaults. Upgrade / rollback must use actual deployed paths / services; do not globally replace names and run commands.

Another option is `deploy/docker` compose ([docker.md](../operations/docker.md)); this section records two manually run containers on the current NAS.

Synology DSM cannot use `deploy.sh` (no `systemd --user`; Docker requires sudo); hub and Postgres both run in Docker.
`<nas>` is developer-machine SSH alias, `<LAN address>` NAS private IPv4, `<commit>` deployment commit. Run NAS commands as hub user with passwordless sudo.

DSM pitfalls: full path `sudo /usr/local/bin/docker`, absent from noninteractive SSH PATH; `/tmp` noexec, no binaries there. System `pg_dump` 11 cannot dump Postgres 18; back up inside container. Without SFTP, scp fails; upload via ssh … 'cat > file'.

### Build and upload (developer machine, repository)

```sh
git worktree add --detach /tmp/persona-build <commit>
(cd /tmp/persona-build/server && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --always)-$(date +%Y%m%d%H%M)" -o /tmp/personad-linux-amd64 ./cmd/personad)
git worktree remove /tmp/persona-build
ssh <nas> 'umask 077; mkdir -p ~/.persona/bin && cat > ~/.persona/bin/personad.new && chmod 755 ~/.persona/bin/personad.new' < /tmp/personad-linux-amd64
```

### Initial deployment (NAS)

1. Postgres: `persona-postgres` container, `persona-pgdata` volume, published only at 127.0.0.1:18661. Creation password is only for initialization; generate a separate random actual password, set with ALTER ROLE, write only to hub.json.

   ```sh
   D="sudo /usr/local/bin/docker"
   $D volume create persona-pgdata
   $D run -d --name persona-postgres --restart unless-stopped -p 127.0.0.1:18661:5432 \
     -v persona-pgdata:/var/lib/postgresql -e POSTGRES_USER=persona -e POSTGRES_DB=persona \
     -e POSTGRES_PASSWORD="$(python3 -c 'import secrets;print(secrets.token_hex(24))')" \
     --log-driver json-file --log-opt max-size=20m --log-opt max-file=3 postgres:18-alpine
   until $D exec persona-postgres pg_isready -h 127.0.0.1 -U persona -d persona >/dev/null; do sleep 1; done
   export PW=$(python3 -c 'import secrets;print(secrets.token_hex(24))')
   echo "ALTER ROLE persona WITH PASSWORD '$PW';" | $D exec -i persona-postgres psql -U persona -d persona -q
   ```

2. `~/.persona/hub.json` (0600; machines initially empty, later fill per `docs/operations/config.md`):

   ```sh
   python3 - <<'PY'
   import json, os, secrets
   cfg = {
       "listen": ":18660",
       "token": secrets.token_hex(24),
       "public_url": "http://<LAN address>:18660",
       "database_url": "postgres://persona:%s@127.0.0.1:18661/persona?sslmode=disable" % os.environ["PW"],
       "machines": [],
       "public": {"tls_listen": ":18662", "advertise_ipv6": True, "advertise_lan": True},
       "security": {"trusted_cidrs": [], "allow_public_plaintext": False},
   }
   fd = os.open(os.path.expanduser("~/.persona/hub.json"), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
   with open(fd, "w") as f:
       json.dump(cfg, f, indent=2)
   PY
   unset PW
   ```

3. Runtime image `personad-runtime:alpine3.22`: no bundled personad (mount binary from `~/.persona/bin`), includes SSH client for computers and `netcat-openbsd` for SSH ProxyCommand via SOCKS (step 7; busybox nc lacks proxy support). `~/.persona/docker/Dockerfile`:

   ```dockerfile
   FROM alpine:3.22
   ARG UID
   ARG GID=100
   ARG USER_NAME
   ARG HOME_DIR
   RUN apk add --no-cache openssh-client ca-certificates tzdata netcat-openbsd \
    && adduser -D -H -u "$UID" -G users -h "$HOME_DIR" -s /bin/sh "$USER_NAME" \
    && mkdir -p "$HOME_DIR" && chown "$UID:$GID" "$HOME_DIR"
   ENV HOME=$HOME_DIR
   WORKDIR $HOME_DIR
   USER $UID:$GID
   ENTRYPOINT ["/opt/persona/bin/personad"]
   CMD ["hub"]
   ```

   ```sh
   cd ~/.persona/docker && $D build --network host --build-arg UID=$(id -u) --build-arg GID=$(id -g) \
     --build-arg USER_NAME=$(id -un) --build-arg HOME_DIR=$HOME -t personad-runtime:alpine3.22 .
   ```

4. Personad container (save as `~/.persona/docker/run-personad.sh` for recreation): host network, `--init`, current user's UID. Read/write `~/.persona`, read-only `~/.persona/bin` → `/opt/persona/bin`, mounted `~/.ssh`; container HOME matches NAS.

   ```sh
   mv ~/.persona/bin/personad.new ~/.persona/bin/personad
   REAL_HOME=$(readlink -f "$HOME")
   $D run -d --name personad --restart unless-stopped --network host --init \
     --user "$(id -u):$(id -g)" -e TZ="$(readlink /etc/localtime | sed 's#.*/zoneinfo/##')" \
     --log-driver json-file --log-opt max-size=20m --log-opt max-file=5 \
     -v "$REAL_HOME/.persona:$HOME/.persona" -v "$REAL_HOME/.persona/bin:/opt/persona/bin:ro" \
     -v "$REAL_HOME/.ssh:$HOME/.ssh" \
     personad-runtime:alpine3.22 hub
   ```

5. Checks:

   ```sh
   $D exec persona-postgres psql -U persona -d persona -tAc "select max(version), count(*) from schema_migrations"
   curl -s http://127.0.0.1:18660/api/v1/health    # ok, version, fp
   openssl s_client -connect 127.0.0.1:18662 </dev/null 2>/dev/null | openssl x509 -pubkey -noout |
     openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='   # Must equal fp
   (umask 077; $D exec personad /opt/persona/bin/personad hub pair > ~/.persona/pair-$(date +%Y%m%d-%H%M%S).txt)  # Contains token, valid 10 minutes
   ```

6. Public direct connection: DSM firewall permits `18662/tcp`, not 18660; router permits inbound IPv6; [Network](../operations/network.md).

7. Proxy-only computers, e.g. an internal development computer reachable through NAS SOCKS5: keep `kind: ssh`, proxy in NAS `~/.ssh/config` mounted into container, `hub.json` alias only. Address, account, proxy port are private configuration, only on NAS:

   ```
   Host <alias>
     HostName <computer LAN address>
     User <account>
     HostKeyAlias <alias>
     IdentityFile ~/.ssh/<hub-specific key>
     IdentitiesOnly yes
     ProxyCommand nc -X 5 -x 127.0.0.1:<SOCKS port> %h %p
   ```

   - This nc is container `netcat-openbsd`; NAS nc may not support -X. Test inside container too, e.g. temporary same-image `--entrypoint sh`, running hub's `ssh -T -o BatchMode=yes … <alias> '~/.persona/bin/personad node --stdio'`.
   - Hub uses `BatchMode=yes`; prepopulate ~/.ssh/known_hosts. Prefer public key from an already trusted connection to that computer, indexed by `HostKeyAlias`; do not blindly accept first connection.
   - Restrict key on computer `authorized_keys` to node only: `restrict,command="~/.persona/bin/personad node --stdio" ssh-ed25519 …`.
   - Dockerfile changes require image and container rebuild. Timestamp-tag old image for rollback, build, then `$D stop personad && $D rm personad && ~/.persona/docker/run-personad.sh`. Docker restart still uses old image.

### Upgrade

Follow Build and Upload to transfer new binary as `~/.persona/bin/personad.new`, then on NAS:

```sh
~/.persona/docker/upgrade.sh ~/.persona/bin/personad.new
```

Script steps:

1. Verify new binary runs on NAS with version.
2. Stop container.
3. Copy current binary to ~/.persona/bin/personad.bak-<time>.
4. Container `pg_dump -Fc` → ~/.persona/backups/persona-<time>.dump, 0600. Dump after stopping service to match old binary's final state. Dump the built-in manager's database too, `persona_manager` in the same server ("The default manager's own database"), or the whole server with `pg_dumpall`: once it moved, the dump of `persona` no longer holds the manager's data.
5. Replace binary with mv; start container.
6. Wait for `/api/v1/health`; verify new version and unchanged TLS public-key fingerprint.
7. Print latest three migrations and logs since start, hiding known Lark gateway errors.
8. Print rollback command. Failed health / wrong version / changed fingerprint returns nonzero.

### Rollback

```sh
~/.persona/docker/rollback.sh --check ~/.persona/bin/personad.bak-<time> ~/.persona/backups/persona-<time>.dump   # Verify only, leave hub untouched
~/.persona/docker/rollback.sh ~/.persona/bin/personad.bak-<time> ~/.persona/backups/persona-<time>.dump
sudo /usr/local/bin/docker exec persona-postgres dropdb -U persona persona_before_rollback_<time>   # After confirming rollback succeeded
```

Script steps:

1. Restore dump into a new database; restoration failure changes nothing. (The built-in manager's own database is restored on its own, from its own dump: rolling the hub back does not roll it back.)
2. Stop container.
3. Rename original database persona_before_rollback_<time>, retaining writes made after dump only there.
4. Rename restored database persona.
5. Restore old binary, start container, wait for health.

### Logs

```sh
sudo /usr/local/bin/docker logs -f personad
sudo /usr/local/bin/docker restart personad    # After hub.json changes
```

Old hub versions log repeated Lark gateway token-read failures when `hub.json` lacks lark; operation unaffected. Lark gateway integration now belongs to its extension: enable agent API on gateway, fill extension address / token per extension instructions. Hub container has no Docker CLI and cannot read other containers' environment.

## Computers hub cannot reach (persona-link)

For laptops with changing networks, kind link in `hub.json`: hub does not SSH; persona-link (Mac menu-bar Persona Link) connects outbound to TLS port, then streams to persistent node, which must be installed (next section). Pair with `personad hub pair --machine <id>`; NAS: sudo /usr/local/bin/docker exec personad /opt/persona/bin/personad hub pair --machine <id>. Details: [link.md](../operations/link.md).

## Hub connects inbound (kind service)

For fixed-address servers / workstations hub can reach, kind service: computer connector in passive `persona-link pair --listen` mode listens on TLS. Hub dials with paired fingerprint pinning and streams to persistent node, no SSH, optionally `socks5://` proxy. Paste printed link into admin Computers › Add Computer › Hub Connects; effective immediately. Credentials encrypted in Postgres. [mesh.md](mesh.md) (“kind service”), [link.md](../operations/link.md) (“Passive mode”).
Existing SSH computers remain usable, but are no longer recommended or addable in admin; migration: [mesh.md](mesh.md) (“SSH / tailcat: compatibility”).

## Linked hubs

Prefer one hub and node services elsewhere. If several hubs are needed, pair into a group (`personad hub pair --peer` / personad hub peer add, or admin Advanced › Linked Hubs). Exactly one **primary** serves all apps / computers; **members** forward app requests and hand their computers to primary. personad hub peer list shows primary; peer center changes it. Each computer appears once via best route. Links use TLS only. Design, protocol, verification: [mesh.md](mesh.md).

## Restart without interrupting clients ([hub-restart.md](../operations/hub-restart.md))

- `personad.socket` owns listener (socket activation, `hub-systemd.sh`); `systemctl --user restart personad` leaves port open, queues connections for new process. Without `LISTEN_FDS` (Docker, launchd, manual), hub listens itself. Rollback: `PERSONA_HUB_SOCKET=0 scripts/deploy/hub-systemd.sh` **before** restoring old binary without `LISTEN_FDS` support, otherwise port conflict. Full stop: `systemctl --user stop personad.socket personad`.
- SIGTERM planned shutdown sends `hub_restarting`, hands machines / sessions as the managers reported them to next hub (`hub_settings` `hub.devices_warm`), suppresses shutdown-induced offline broadcasts, then hands off event log (settings `hub.event_log`) so next hub continues sequence.
- Persistent node on hub machine (next section) keeps assistant Claude outside hub cgroup, surviving restart. Deploy with `node-launchd.sh restart-idle`; replace binary only when node idle.

## Persistent node (Mac, `scripts/deploy/node-launchd.sh`)

Hub launches node via SSH (`personad node --stdio`), which launches assistant Claude. SSH cannot read macOS **login keychain**; Claude keys stored there through `apiKeyHelper` `security find-generic-password` give assistant 401. Herdr Agents do not, since herdr runs in login session.

- Run `scripts/deploy/node-launchd.sh install` on Mac: installs user LaunchAgent `com.persona.node`, persistent `personad node --attach` after login, listening ~/.persona/node-attach.sock.
- Hub still SSH-launches `personad node --stdio`; detecting persistent listener, it forwards SSH I/O only (Relay in `internal/node/attach.go`). Persistent node handles connection; its assistant Claude / key helper inherit login session and keychain access. Hub unchanged, cannot distinguish.
- Without persistent node (not installed, exited, stale socket), stdio handles itself as before.
- One connection at a time: new hub reconnect replaces old connection's node; waiting hooks ask new one, as with replaced SSH node. Only one persistent node.
- `deploy.sh` replaces binary then `node-launchd.sh restart`; SSH can control login-session launchd. Hub reconnect needed for new node (restart hub / app Reconnect).
- Agent runtime `~/.persona/runtime.sock` belongs to persistent process, not individual hub connection (`node/runtimehost.go`). Opens at persistent startup; runtimes such as DSH plugin need not wait for hub. Hub connection replacement does not disconnect runtimes; new connection immediately lists sessions (`docs/protocol/protocol.md`, “Runtime ↔ node”).
- `scripts/deploy/node-launchd.sh uninstall` returns to SSH-launched node. Log ~/.persona/node-attach.log.

### Scheduling and privacy on the Mac (#5944)

- **ProcessType**: both installers (`node-launchd.sh`, `persona-link node install` in `cmd/persona-link/nodesvc.go`) write `<key>ProcessType</key><string>Interactive</string>`. Without it launchd applies its default limits (`launchctl print gui/<uid>/com.persona.node` shows `spawn type = daemon`): the node and the Claude it starts are throttled like a background job. One such node got 15.45 CPU seconds in 139 s (scheduling priority 4 instead of 31), and Claude did not start in time. With it the spawn type is `interactive`. It grants no privacy (TCC) permission.
- **Which agent**: a new install writes `com.persona.node`. An existing installation keeps its recorded agent label, because both installers leave an installed agent as it is. The doctor looks at the supported label whose plist is in `~/Library/LaunchAgents`. When both supported definitions exist, it picks the one launchd has loaded: that is the running node, its spawn type is what the check is about, and it is the one `--fix` must reload; loading the other would start a second node next to it. Under one of the agents (`XPC_SERVICE_NAME`) that one counts as loaded. If it can't tell (both loaded, or neither), or neither plist is there, it uses `com.persona.node`. `--fix` rewrites and reloads the agent it checked, under its own label, and never writes a plist under the other one.
- **Older plists are not rewritten silently.** `personad node doctor` reports `launchd-process-type` (warn) when the plist has no `ProcessType=Interactive`, or when it has one but launchd still runs what it loaded before (spawn type not `interactive`). `personad node doctor --fix`, run on that Mac, writes the key (only that key changes, the file keeps its permissions) and, when launchd has the agent loaded, runs `launchctl bootout` and `launchctl bootstrap` so the running node gets it. This restarts the resident node, which interrupts running assistant conversations; the hub reconnects. An agent that is not loaded stays unloaded and gets the new plist at the next login. The web page's **Fix** (`node.doctor.fix`) never does this: it runs inside the node that a reload would stop. A doctor running under the agent (`XPC_SERVICE_NAME` is either label) doesn't either. If the bootstrap keeps failing, the report says so and gives the `launchctl bootstrap` command that loads the node back.
  `bootstrap` returns before the new node listens, so after a reload `--fix` waits (at most 15 s) until the sockets that answered before (`node.sock`, `runtime.sock`) answer again, and only then checks again. A node that is not back by then still fails those checks, marked as right after the reload, with the agent's `StandardErrorPath` as the place to look. The text report says separately that the plist was written and that the agent was reloaded; a reload that failed is never reported as done.
  Inside the node (the web page, or a claude the node started) the hint says to run the command in a terminal on that Mac, and a `--fix` run there says it left the agent alone, rather than skipping it silently.
- **Privacy-protected folders**: Claude Code reads its settings, its skills, commands and agents (following symlinks) and the working directory before `SessionStart`. If any of these is in Desktop, Documents or Downloads and the program running the node isn't allowed there, the read is refused, or it waits on a consent prompt nobody answers. Claude then stops at "Loading commands and agents" and no hook fires. The `path-access` check of `node doctor` reads those paths in a child process: the same binary in `node doctor --preflight-child` mode, in its own process group, with cwd `/`. The child is killed as a group once a single read has taken 3 s, or once the whole run has taken 15 s (what was not read by then is counted, not blamed). The check names the blocked path and gives the two ways out: grant Full Disk Access to the program that runs the node, or move the directory out of the protected folder. It never starts claude.
  - Only a refusal with `operation not permitted` (EPERM) or a hang inside Desktop, Documents or Downloads is put down to macOS privacy. A hang anywhere else is as likely a mount that stopped answering (network disk, FUSE), so the hint is about the mount; under `/Volumes` it also says that macOS asks before reading a network or removable volume.
  - Which program needs Full Disk Access depends on what started the node: for the LaunchAgent it is the node binary itself (`~/.persona/bin/personad`), for an SSH-started node it is `sshd-keygen-wrapper`, and for a terminal it is the terminal app. File errors on the phone (`explainDenied`) use the same wording.
  - `personad node doctor` asks the running node over `node.sock` (`{"kind":"preflight"}`), so the reads count against the node's own permissions and include its sessions' working directories (at most 20: the assistant's first, then the most recently used). If no node answers (none running, in standby, or older), it reads Claude Code's own directory from the doctor's process and says why: on a Mac that result reflects the terminal's or SSH's permissions, which may differ from the node's. The command line waits 23 s for the node's answer, longer than the node's longest run (15 s plus up to 3 s to stop the child), so a slow run is never taken for a node that does not answer.
  - `node.doctor` runs the check too (the web page shows it). The assistant's readiness only wants Claude Code's version from it, so it passes `"preflight": false` and neither waits for the reads nor makes them on every ask.
  - Settings and links in Claude Code's own directory affect every start, so a block there fails the check. A block in one working directory only warns. A link that points nowhere is listed but isn't a problem.

## Tailcat hole punching (`internal/tunnel`, `devices/tailcat.go`)

Computers unreachable by hub SSH (NAT, other LAN, no public IP) use [tailcat](https://github.com/tailscale/tailcat): Tailscale data plane (WireGuard + DERP relay + hole punching), no control plane, account, root, TUN, or exposed ports; all in personad userspace netstack.

- **Fallback order**: machines with `ssh_host` and `tailcat_addr` try SSH first (`ConnectTimeout=15`), then immediately tailcat. For 5 minutes after SSH failure, reconnect tries tailcat first, avoiding SSH timeout. While on tailcat, test `ssh <host> true` every 5 minutes; restored SSH disconnects / reconnects to SSH, except while questions / plans / permissions await phone because reconnect cancels them. If return fails immediately (SSH login works, node silent, e.g. `node.info` not received within 40 seconds on slow link), next probe interval doubles up to 32×, reset after 10 stable SSH minutes, avoiding five-minute disconnect loops. `kind: "tailcat"` uses tailcat only. Machine transport shows current route; `tailcat_path` direct / relay, queried each minute, also encouraging direct-path rediscovery.
- **Disconnection detection**: userspace TCP lacks keepalive (SSH has `ServerAliveInterval`), so sleeping / disconnected peers do not close automatically. Hub pings node each minute, reconnects after two missed replies; old unknown method counts as response. Any traffic during minute (read data, or each 32 KB chunk of large writes) counts alive, suppressing ping: large pages on slow relays take long and queued ping could misdiagnose. Ping waits in separate goroutine; blocked dead-connection write cannot delay decision. Closing underlying connection on disconnect causes blocked writes to fail.
- **Node**: only persistent node (node-launchd.sh install: Mac launchd, Linux systemd user service) opens tailcat. With `~/.persona/tailcat.json`, listens on tailcat address at 18662 (node protocol, same one-connection/new-replaces-old rule as SSH) and 18663 (binary upload). Network / DERP startup failures retry with background backoff; SSH forwarding continues.
- **Pairing**: hub `personad hub tailcat-key` prints the public key of the built-in manager, which dials the computers, first creating `~/.persona/managers/assistant/tailcat-hub.json` (an existing `~/.persona/tailcat-hub.json` is copied there once, [managers.md](../assistant/managers.md) ("The built-in manager's own files")). Computer personad node tailcat `init --hub` <public key> generates `~/.persona/tailcat.json` (node private key, preshared key, chosen DERP region, allowed hub keys) and prints address for hub.json tailcat_addr. Restart persistent node and hub. Rerun init adds hubs without address change; node tailcat addr reprints.
- **Security**: node answers only listed hub keys. Tailcat empty allowlist permits everyone, so files with no hub key are refused. Other keys cannot handshake. Only ports 18662–18663 allowed. Address includes preshared key: secret like hub token, only in hub.json, never logs. Both key files 0600; broader permissions repaired.
- **Revocation / rotation**: allowlist read only at node startup; tailcat does not expel already connected peers. Remove hub from computer `tailcat.json` hubs, `scripts/deploy/node-launchd.sh restart` the persistent node. Leaked address: delete `tailcat.json`, reinit to rotate preshared / node private key, update hub address, restart hub. Rotate hub key: delete `~/.persona/managers/assistant/tailcat-hub.json` and the copy left in `~/.persona`, restart hub, obtain new `hub tailcat-key`, `init --hub` on every computer, remove old keys from hubs, restart persistent nodes.
- **Stable address**: address stores DERP region ID, not embedded relay server. Choose nearest region once during init and persist; choosing every startup would change address.
- **Deployment**: Redeploy over tailcat sends binary to 18663: one `{"size","sha256"}` line + gzip. Node verifies size / SHA-256 and local version execution before replacing, replies ok, exits for launchd / systemd restart. One upload at a time; second returns Uploading immediately. Abort after 60 seconds without data; hub restart mid-upload cannot block later deployment. Initial `deploy.sh` still requires SSH or manual binary transfer.
- **Cost**: binary approximately 19 → 36 MB, gzip 8 → 15 MB; cold start ~7 seconds fetching DERP map / selecting region, first dial ~6.5 seconds. Public relays have no SLA and limit relay bandwidth; custom derper via `--derp-map-url` / `tailcat.derp_map_url` below. Tailcat API stability not guaranteed; run integration tests on dependency upgrades.
- **Self-hosted relay (derper)**: WireGuard already encrypts end-to-end, but public relay sees communicating keys, peer IPs, volume. When computers / hub share LAN, run LAN-bound derper on hub machine so discovery / fallback never leave LAN:
  1. Build: GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go install tailscale.com/cmd/derper@<version>; match `tailscale.com` version in `server/go.mod` / tailcat.
  2. IP-only certificate: self-signed, pinned by map `CertName` `sha256-raw:<certificate DER sha256>`, no CA. Generate long-lived openssl cert with subjectAltName=IP:<LAN IP>, store <certdir>/<LAN IP>.crt / .key. Do not use derper's automatically generated missing-cert certificate (`docs/lessons/server.md`).
  3. Systemd user service: `derper -a <LAN IP>:<DERP port> -certmode manual -hostname <LAN IP> -certdir <certdir> -c <private key file> -http-port -1 -stun-port <STUN port> -home blank`. IP in -a also binds STUN there; always supply -c.
  4. DERP map not served by derper; tailcat fetches HTTP GET. Separate static server, e.g. `python3 -I -m http.server <port> --bind <LAN IP> --directory <directory>`, serves `derpmap.json`: one custom region ID 900–999, one node, `HostName` / IPv4 LAN IP, `IPv6: "none"`, `CertName` / DERPPort / STUNPort. Before switching, fetch map on every computer and compare `openssl s_client` certificate SHA-256 to CertName.
  5. Switch hub `tailcat.derp_map_url`; computer node tailcat `init --hub` <key> `--derp-map-url` <map URL>. Keys unchanged but new-map region reselection changes address; update each `tailcat_addr`, restart node / hub. Map certificate / port changes also require both restart.
  6. Verify machine `tailcat_path` still direct; node log DERP region 9xx; personad peers connect only to derper port, not public relays (`ss -tnp` / `lsof -i`).
- **Hub ↔ node only**: phone / web still need reachable hub public_url.
- Integration (internet required): `PERSONA_TEST_TAILCAT=1 go test ./internal/tunnel -run EndToEnd`, real relay dialing / authentication / upload, plus commands in `internal/devices/tailcat_e2e_test.go` header (two real personad versions: SSH fails → tailcat online → deploy new version and reconnect). `PERSONA_TAILCAT_DEBUG=1` enables otherwise muted Tailscale logs, several lines per second during hole punching.

## Database (Postgres)

Hub Docker container `persona-postgres`, `postgres:18-alpine`, listening only `127.0.0.1:18661`, `persona-pgdata` volume, --restart unless-stopped. `scripts/deploy/db.sh` idempotently creates / starts it, initially generates password into `hub.json` `database_url`; `deploy.sh` <hub machine id> runs it first each time. It never replaces a database (#6149): a `database_url` already in `hub.json` is never rewritten, and the container started is the one made with that URL's user. On a hub set up before the rename, that is `jarvis-postgres` (volume `jarvis-pgdata`, user and database `jarvis`), even when a `persona-postgres` exists as well. A container is made only when there is neither a `database_url` nor a hub database container or volume; otherwise db.sh stops and says what to restore. The throwaway test database is `<database>_test` (`persona_test`, or `jarvis_test` on such a hub). Moving a database to the new names is the operator step in [persona-cutover.md](../operations/persona-cutover.md), never db.sh's. Hub startup applies outstanding migrations, `schema_migrations` records, advisory lock prevents concurrency.

Without Docker, use system Postgres (Ubuntu postgresql, version 16 tested, localhost only). Create persona user and persona / `persona_test` databases, both owned by persona; test database for test-server.sh. Put `postgres://persona:<password>@127.0.0.1:5432/persona?sslmode=disable` in 0600 hub.json. Without Docker, `db.sh` only checks configured address with `pg_isready`; `deploy.sh` / `test-server.sh` unchanged. Replace docker exec … psql below with `psql "<database_url>"`.

| Table | Content |
| --- | --- |
| `live_activities` | Per-device activity / push-to-start tokens and start time, `docs/apple/live-activity.md` |
| devices | APNs registrations; 410 / BadDeviceToken uses a persisted registration compare-and-delete, never another slot's newer token. Enrollment publishes only after ownership/retirement commit. JSONB prefs are not overwritten on registration |
| `phone_tools` | iPhone's last declared phone_* tools, visible to Claude even offline |
| `node_sessions` | Persona-started sessions per machine: pane, launcher, directory. Node uses hub `state.load` / `state.put`, Mac no direct DB. `state.load` reads last 60 days via `node_sessions_started` index (machine_id, started_at), not entire machine history |
| `mail_accounts` | Nonsensitive JSONB + AES-GCM ciphertext; the manager's `secret.key` excluded from DB |
| interactions | Questions / plans / permissions / terminal interactions and outcomes resolved / dismissed / expired / cancelled. Hub restart cancels leftover pending through partial `interactions_pending` index, no history scan. Completed rows currently retained indefinitely; retention #2615 |
| `hub_settings` | The hub's own settings: persistent `hub_id`, the user's locale, look, mutes and pins, the managers' default and machine owners, mesh records, update settings, the event log and machines handed to the next process |
| settings | A manager's settings (the assistant, inbox rules, models and launchers, extensions, the computers' engines and warm start) |
| `pair_tokens` | Temporary token hashes, expiration / redemption records, surviving restart |
| `manager_outbox` | A manager's pushes and the apps' live updates made while the hub was away, in order, until the hub took them (bounded; [hub-restart.md](../operations/hub-restart.md) ("The manager while the hub is away")) |
| `handed_over` | The hub's record of what each manager handed it lately (pushes, outbox items) by manager and id, so an item that comes again is delivered once, also by the next hub process (two hours) |

Connect: `ssh <hub machine> 'docker exec -it persona-postgres psql -U persona -d persona'`.
Tests: `./scripts/test/test-server.sh` syncs to hub and runs all; local without Docker skips DB tests. `PERSONA_TEST_DATABASE_URL` pointing `persona_test` is only a starting point: each test binary creates adjacent private database and deletes afterward (`server/internal/db/dbtest`), isolating concurrent runs. Persona role needs database creation (container superuser; system Postgres grant CREATEDB). Without privilege, fallback queues on persona_test.

Legacy `phone_tools.json`, `devices.json`, `mail_accounts.json`, `sessions.json` auto-import on first start, renamed *.migrated.

### Migrations

The migrations are two sets, one per side (`server/internal/db/migrations/`):

- `hub/`: the hub's own tables: devices and their tokens, pairing, phone tools, Live Activities, device sessions, client operations, lock tests, peers and the mesh, connector credentials, the admin page, bus identities and the managers' install codes, machine trust, what the managers handed it (`handed_over`).
- `manager/`: a manager's: interactions, mail, the computers' sessions and metrics, session views and work, the inbox, the knowledge base, todos, task plans and the assistant's feed and views, extensions and their markets, secrets and metrics, the record of the extensions' hub tokens (`extension_tokens`), the workspace board, the outbox it keeps while the hub is away (`manager_outbox`).

`db.Open(ctx, url, sets)` applies the pending migrations of the sets it is given: the hub opens its database with `db.HubSet`, a manager with `db.ManagerSet` (tests that use both sides' tables: `db.AllSets`). While the default manager shares the hub's database, both sets run there, each when its side starts. Non-default managers have databases of their own (`docs/assistant/managers.md`) and get only the manager set.

`schema_migrations` has a row per migration a part applied, with `part` `hub` or `manager`. Rows from before the split have `part` NULL and stand for both parts, so a database migrated by an older build runs nothing again, whichever side opens it first, and a build from before the split still reads and adds records there. Three early migrations are in both sets and frozen that way: `0001_init.sql` made tables for both sides and is split into each side's part; `0071_sent_messages.sql` and `0241_sent_echo.sql` make `sent_messages`, which each side keeps in its own database (the hub's sends from the apps, the manager's from the inbox). On a database both sides share, that table is one.

Settings are each side's own too: the hub's in `hub_settings` (`0421_hub_settings.sql`, which on a database shared with the default manager copies the hub's keys out of `settings` once and leaves the rows there for an older build), a manager's in `settings`. On such a shared database the hub also keeps its keys in `settings` for a hub build from before `hub_settings`: each write goes to both tables in one transaction, and at start (or once the table appears, on a fresh install) the hub takes up the keys an older build wrote there later than it did (an event log hand-over too: it stays in both tables until a hub takes it, which removes both; its own that an older build took meanwhile is dropped), and writes its other values in effect back (`db.sharedWithOlderHub`; the shared table is locked first, as every write of a hub key starts there). A hand-over this build left before the shared table existed (a fresh install) was never an older build's to take and is resumed. A known gap: a build from between `hub_settings` and this mirroring (#5710 alone) that takes a hand-over and then crashes leaves the mirrored copy, which is resumed. A switch back starts from current values, and switching forward again keeps what the older build changed. `db.DB.Setting` and the rest use the table of the side the database was opened for (`db.HubSet` alone: `hub_settings`). Neither side reads the other's keys; what one needs of the other comes over the bus: the mutes go in every manager's `manager.hello` answer and with `fleet.mutes` (once more on its first word after the handshake, which the answer cannot overtake, and after each change; not sent while the hub cannot read them, so a manager keeps the last ones), the computers waiting to be confirmed with the devices handover, and the hub hands its next process the machines as the managers reported them (`hub.devices_warm`) instead of reading the manager's warm start. The inbox's rules (`inbox.rules`, `inbox.agent_rules`) are the manager's settings (listed among its `persona_settings`). `TestSettingsKeysAreOneSidesOwn` (`internal/hub`) fails when the hub and a manager package use the same literal key.

The manager's setting `connector_chat_rules` (in `settings`, not `hub_settings`) holds the rules the user made from the Sent as you ledger, "don't ask again in this chat" (#5278, `docs/extensions/connectors.md` "Sent as the user", Per-chat rules): a JSON array of at most 500 `{rule, at, row?, summary?}`, written whole, read at start. A value that is not a list is taken as none (the gate asks) and the next change replaces it; an element of a list that does not read as a rule (a number, a text, a list, `null`) is left out and said in the log, the others are kept, and the next change writes the list without it; one that reads as a rule no gate could run (a confirm, a pattern, `{}`) stays, neither listed nor applied. To go back to a gate that asks as manager.json says, `DELETE FROM settings WHERE key = 'connector_chat_rules'` on the manager's database and restart the manager; it needs no migration and is safe to leave behind when switching to an older build, which does not read it.

Switching back: a database the hub and the default manager share (every install before the split, and a fresh one after it, since both sets run there) can be opened again by a build from before the split. A database that got only one set (a manager's own, made by a build with the split) cannot: a build from before records 0001 as done and then misses the other side's tables. Don't switch such a manager below the split.

### The default manager's own database

The built-in manager (the assistant the hub starts) used to keep its state in the hub's database. Now it has one of its own, and its data moves there once (`internal/assistant/owndb.go`, `internal/db/copy.go`).

- **Where.** `database_location` in its `managers/assistant/manager.json` decides; without it the default applies.
  - `hub_server`: a second database in the hub's Postgres server, named after the hub's with `_manager` (for example `persona_manager`). This is the default. The built-in manager's data lives in the hub's Postgres server: removing that server removes it too, and a backup of the hub must have both databases, `persona` and `persona_manager` (a `pg_dump` of each, or `pg_dumpall`). `manager.json` keeps only the name (`database_name`); the address is the hub's `database_url` with it at each start, so moving the server keeps it found.
  - `container`: a Postgres container of its own (`persona-mgr-assistant-<hash>-pg`, volume `…-data`; the hash is of its data directory, so two hubs or two users on one Docker never share one; one already there under that name is this data directory's own and is taken: adopted when it has the moved data, used when empty), as a manager made in the app has. Without Docker it falls back to the hub's server.
  - Its `manager.json` keeps where: `database_name` for the hub's server (the address is the hub's `database_url` with that name, at each start), `database_url` and `database_container` for a container.
- **The move, once, at its start, before it serves anything.**
  - It makes or starts its database and checks there is room for the copy. When the free space can be told (a Postgres on this machine, its container's volume), it refuses if there is too little and says so; for a Postgres in a container or on another machine it cannot be told, and the copy goes on.
  - It waits until nothing else uses its tables in the hub's database: every manager running there holds a share of an advisory lock (on a connection of its own, checked every 30 s), and connections of a build from before carry no `application_name`. It waits up to a minute; otherwise it tries again at the next start. Limits: a `database_url` that sets `application_name` hides an older build; an older build with no connection open at that moment is not seen; a pooler in transaction mode (PgBouncer) breaks session advisory locks.
  - It copies the manager set's tables from one REPEATABLE READ snapshot, in foreign-key order, as COPY text with pg_dump's date and number formats, in one transaction on its own database, which must hold none of the manager's data yet. It logs progress for big tables, checks each table's row count against the snapshot, and sets sequences as they were.
  - The same transaction records the copy in its own database (`manager_moved` 'in'), so the data and the record go together; then the hub's database records that the data left (`manager_moved` 'out'), and its `manager.json` says so (`moved_to_own_database`).
- **On a fresh install** the hub's database never gets the manager's tables: the manager starts on its own database, with nothing to copy (unless its own cannot be made at that first start: it then runs on the hub's, and moves at a later one).
- **When the move cannot happen.** No CREATE DATABASE right, Docker down, too little room, or another process still in the tables: it runs on the hub's database for that start, says so once, and tries again at the next one.
- **Once it moved,** it never runs on the hub's database again, and never copies over its own: a start whose `manager.json` does not know of the move (another data directory, a restored one) finds its own database filled and runs there. If its own database cannot be reached, or comes back empty (lost, made again), it does not start; nor when the hub's database says the data left for a database this data directory does not know. A manager still on the hub's database when another start moved the data stops.
- **Switching back.** The hub's database keeps the manager's tables as they were at the move, untouched. An older build switched back finds its data as of then: what the manager made after the move is not there. Forward again, it runs on its own database as it was, and what the older build wrote in the meantime stays in the hub's database only.
- **Its own database truly lost** (no backup): it does not start, as above. To start again from the hub's stale copy, delete the record that the data left (`DELETE FROM manager_moved WHERE direction = 'out'` in the hub's database) and the `moved_to_own_database` and `database_*` keys from its `manager.json`; the next start copies that copy into a new database of its own. What was written after the move is gone. If the manager had rotated its `secret.key` already, first copy `~/.persona/secret.key` to `managers/assistant/secret.key.prev`: the copy's secrets are sealed with the hub's key, and the next start seals them again with its own.
- **Cleaning up.** Once no older build will run, `personad assistant drop-shared-tables` lists the manager's old tables in the hub's database, and with `--yes` drops them (the hub's own tables stay). It checks first that its own database has its data, keeps `manager_moved` (its 'out' keeps a start that does not know the move off the stale copy), and changes nothing else (no migration). It is never automatic: after it, an older build switched back starts without the manager's data, and a hub build from before `hub_settings` without the hub keys it kept in `settings`.
- **Its own key.** In the start that moves its data (or the first start on its own database), before it serves anything, the manager rotates its `secret.key` (a copy of the hub's on an upgraded install) to a key of its own and seals its secrets in its own database again ([managers.md](../assistant/managers.md) ("Its own secret.key")). Back up its `secret.key` with its database: a database backup from before the rotation needs the hub's key as `secret.key.prev` to open.


Adding a migration:

1. Decide whose table it is: the side whose store reads and writes it. A table both sides need is a sign that one side should ask the other over the bus instead (as the manager learns the computers waiting to be confirmed from the hub's handover, `devicewire.Config.Pending`, rather than reading `machine_trust`).
2. Put the file in exactly that set's directory, named `NNNN_what.sql` with a number neither set has: look in both `hub/` and `manager/`, on `main` and in every worktree (`docs/lessons/workflow.md`). Two lines once took 421 at the same time (`hub/0421_hub_settings.sql`, `manager/0421_machine_usage.sql`); that pair is frozen in the guard, harmless since each set records its own part. Write it idempotently (`IF NOT EXISTS`) where you can.
3. Guards: `TestMigrationSets` (`internal/db`) fails for a file in both sets or at the top level, and for a number used by both sets outside the frozen exceptions above; `TestStoresTouchTheirOwnTables` (`internal/hub`) fails when a store interface of the hub reaches a table outside the hub set, or one of the managers' (assistant, devices, extmgr, extension, mail, agents) a table outside the manager set. `scripts/ci/migration_freeze.py` (from `scripts/ci/guards.sh` on every pull request, and from `scripts/check-fast.sh` when a migration changes) fails when a file already on the base branch is edited, deleted or renamed. A comment-only or whitespace-only edit passes; add a new migration instead.

## Configuration (hub `~/.persona/`, 0600, never committed)

| File | Content |
| --- | --- |
| `hub.json` | listen, token, `public_url`, public (TLS / candidate addresses), security (trust / auth rate limits, [network.md](../operations/network.md#hubjson-网络配置)), name display name, admin access scope ([admin.md](../operations/admin.md)), `database_url`, machines; notify (notification timing, `docs/operations/config.md`); `assistant.external`, `persona_view`, `html_views` |
| `managers/assistant/manager.json` | The built-in manager's own sections (`docs/operations/config.md` ("manager.json")): `assistant.owner` / `notes` / `machine` / `launcher`, `ai`, `agents`, `inbox`, `kb`, `hook_timeout_minutes`, lark, `lark_gate`, `connector_gate`, github, imessage, google, microsoft, bots, extensions / plugins, `trusted_extension_markets`, usage, `sent_ledger_days`, `sent_recall_hours`; e.g. `google.client_id` (Gmail iOS OAuth client), `google.proxy` (Gmail HTTP proxy, e.g. http://127.0.0.1:<port>, `docs/extensions/mail.md`), `github.proxy` (all GitHub connector requests, same format) |
| `tls/hub.key`, `tls/hub.crt` | Automatically managed ECDSA P-256 identity key / self-signed cert, directory 0700, files 0600, private key outside DB. Rotation requires device re-pairing |
| `secret.key` | The hub's own 32-byte key (peer tokens, connector credentials). The built-in manager's key, which seals its secrets (mail, model providers, extensions), is in `managers/assistant/` with its `tailcat-hub.json` and `handles.key`: a copy of the hub's on an upgraded install until it rotates to its own after the move ([managers.md](../assistant/managers.md) ("The built-in manager's own files", "Its own secret.key")) |
| `apns.json`, `apns_key.p8` | Push config / key. Present → direct APNs; absent → per-phone registered relay (`docs/operations/push-relay.md`); no relay → no push |

Each computer ~/.persona also contains node-generated hooks (`claude-hooks/` / `assistant-hooks/` plugins, the Agents' `claude-live-replies/` plugin, `claude-settings.json` / `assistant-settings.json` for older Claude), `mcp.json`, `node.sock`, uploads/.

Pairing: hub `~/.persona/bin/personad hub pair` calls running hub via loopback HTTP, prints temporary link, QR, expiry, TLS public-key fingerprint if enabled. Listen must accept IPv4 loopback. Token valid 10 minutes, single device-token redemption, at most 2-minute post-redemption grace; protocol policies. `--legacy` prints main token / old QR offline, not for public networks. New QR parameters: [Protocol](../protocol/protocol.md#临时配对).

## Push policy (`hub/push.go`, `hub/notify.go`)

- Registration ownership: the newest authenticated registration supersedes an older ID only when both share a nonempty credential owner, topic and APNs environment, plus the same nonempty encryption public key or exact APNs token. Names and addresses never prove installation identity. Commit registration and retirement before making the target sendable; preserve notification settings on same-ID registration.
- Lifecycle: reconcile at startup, on enrollment and every five minutes. Retire revoked owners and registrations idle for at least 90 days, measured from true enrollment, owner activity or APNs/relay acceptance (coalesced writes), not relay/prefs metadata updates. Existing rows receive migration-time observation grace. A genuinely returning inactive installation may enroll again; a revoked installation may re-pair under a different live owner only with matching scoped installation proof. Stale superseded writes stay fenced; a fresh post-retirement enrollment with the same owner and current installation proof may atomically replace the historical canonical ID, preserving preferences and the longest start budget. This keeps an existing client's cached ID usable without trusting old `updated_at` as enrollment history. Old-version late writes, route regressions, unconditional deletes and activity saves cannot bypass those fences. Maintenance skips busy transports/enrollments and aborts inactivity cleanup if recent acceptance fails to persist; an idle pass has a shared five-second database deadline.
- Retiring a duplicate removes its activity row and transfers the longest existing Live Activity start deadline to the canonical ID, so identity rollover cannot reset the budget. Registration ownership conflicts return localized HTTP 409; storage failures remain retryable failures, never success. APNs acceptance does not prove OS delivery or a client wake.

- Needs you (question / plan / permission / terminal): immediate, `time-sensitive`, `category: INTERACTION_<KIND>` with app-registered buttons. Hook-blocked dialogs do not appear in computer TUI and cannot be answered there, so no delay.
- Completed: follows Ovis (`github.com/devswha/ovis` `server/push.ts`, MIT); push is for users away from the computer:
  - Working → done / idle waits `notify.done_delay_seconds`, 60 seconds, before deciding. Resumed work remains same turn with original start time; idle (seen in herdr) or app `seen_at` suppresses push.
  - Running background commands / sub-Agents defer push until reports are processed and that Claude turn ends; no standalone `WokeByTask` turn push.
  - Duration = first working to final completion. Per-device `prefs.done` off / long / always; long receives turns at least `long_turn_seconds`; `prefs.input` off suppresses Needs you.
  - Hidden Agents and assistant turns already pushed by `notify_user` are skipped. Hub restart measures ongoing turns from first post-restart observation.
- Extension notifications (`POST /api/v1/notify`, `hub/notify_api.go`): extension picks active / `time-sensitive`, filtered by device input and hidden status. Decision / rate limits belong to extension; `docs/extensions/extensions.md` (“Sending notifications”).

<a id="关停hubshutdowngo"></a>

## Shutdown (`hub/shutdown.go`)

This investigation confirmed exit-path defects, not the NAS database-wait cause. BaseContext propagated cancellation to WebSockets, but HTTP Shutdown does not wait for hijacked connections. Device-session cleanup and WebSocket handlers had no wait relationship. Final pgxpool.Close waited indefinitely for borrowed connections. Idle WebSockets do not continuously borrow DB connections; online phones / timeout logs alone do not prove row locks or connection leaks.

Now close plaintext / TLS listeners first, send registered WebSockets 1001 going away. Allow up to 500 ms handshake, then cancel contexts, force-close nonresponders, wait for handlers / write loops. Connections registering device sessions are tracked too; no new wait objects after accepting stops. Then finish HTTP, device sessions, lock-screen tests, wait node connections, close pool. Device disconnects due to hub shutdown record hub_stopped, not phone-disconnection count.

`shutdown_timeout_seconds` defines total budget, default 8 seconds for absent / nonpositive. Each phase 2 seconds bounded by total; handshake 500 ms. Exit policy, not performance estimate: total allows four full phase budgets without unbounded accumulation. NAS / real phone networks unmeasured. External terminator grace must cover configured total.

Phase timeout logs phase / error and continues cleanup. Even noncancellable cleanup cannot prevent Run returning with error at total budget; command entry exits process. Pool cannot force-reclaim borrowed connections; after CloseContext timeout, background close may wait but no longer blocks command exit.

SSH side has no pool. Hub CommandContext already terminates local node / SSH subprocess on cancellation; this change adds process / pipe wait bounds, without node protocol / remote changes. App messages / capability negotiation unchanged; existing iOS needs no change.

Tests use dedicated database, temporary config, local fake Lark service, real Run. Client stays connected after hello then hub context cancels. Assert shutdown return, device stop record, 1001 close, no surviving WebSocket handlers / write loops. Separate nonresponsive-handshake test. Pool test holds borrowed connection, verifies CloseContext timeout, then returns it and observes close completion. Total-budget test uses noncancellable cleanup.

Local measurement 2026-10-31, milliseconds from context cancellation to Run return. Five samples per scenario, fewer than 10—not a performance baseline. Nearest-rank percentiles.

| Client | Samples | min | p50 | p90 | max | mean |
| --- | --- | --- | --- | --- | --- | --- |
| Reads and responds to close | 5 | 3.063 | 3.868 | 7.107 | 7.107 | 4.441 |
| Does not read or respond | 5 | 506.592 | 508.780 | 510.670 | 510.670 | 508.478 |

Validation passed: server build, relevant hub / db tests, full hub-package tests. Sandbox Go could not obtain worktree VCS status, so GOFLAGS=-buildvcs=false disabled only build-version embedding. First test accidentally triggered existing read-only Lark-container probing; subsequently isolated with local fake service. Container unchanged.

## Traffic pushed to the app (2026-11-02, battery investigation)

Foreground app keeps WebSocket to hub, receiving change broadcasts. A 19-minute real-hub recording (`docs/operations/testing.md`, “Push replay”) showed 105 messages / 2.6 MB per minute; sessions full-machine lists accounted for 37 messages / 2.6 MB, almost all bytes. Continuous cellular reception prevents radio idle.

- New apps receive `sessions_delta` (`hub/sessions_push.go`, `docs/protocol/protocol.md`, “Session deltas”): initial snapshot excluding Codex history, then changed sessions only. Replay: session push 2605 → 79 KB/min, -97%; all messages 2616 → 90 KB; initial connection 602 → 227 KB.
- Stop identical full-list broadcasts for old apps (`broadcastSessions`). Questions appearing / answered, viewing sessions, Codex cards often rebroadcast unchanged machine lists. `sessionsSent` stores last emitted encoded bytes per session; `sameSessions` skips identical. Recording / enqueue share lock, so remembered list equals last app received. Replay 37 → 30 messages/min but only 6% fewer bytes; remaining changes are real rev / `updated_at` / last-text changes needing delta protocol.
- Decode and re-encode only what changed (#2250): the hub (`fleet.sessions`) and the manager (`sessions.changed`) decode only the sessions whose bytes differ from the last list (`proto.SessionListDecoder`); the hub keeps each session's row with the bytes, rev and manager it came from and pushes the same row again while they stay (`Machine.encodeKept`), projects the whole-list message once per kind of older app and works out a delta once per group of delta apps in sync. Wire bytes are unchanged; `proto.DecodeChangedSessions = false` goes back to whole lists. One changed session of 150, bytes in to messages queued for 5 delta and 2 older apps: 2.69 → 0.70 ms, 1.32 MB → 431 KB, 571 → 43 allocations; the manager's ingestion 1.12 → 0.28 ms. The price is memory: for that 150-session machine the hub keeps about 470 KiB more and the manager about 440 KiB (the last list's bytes and decoded sessions, and the hub's row map).
- Disconnect lagging connections: full send queue, 512 messages behind, previously dropped one message; now disconnect like `sendJSON` and reload full snapshot after reconnect. Once duplicates suppressed, dropped message cannot rely on a later identical list to repair it.
- Live Activity cadence: `docs/apple/live-activity.md` (“Push cadence”).

## Self-update (`hub/selfupdate.go`, `docs/operations/hub-update.md`)

`channel=release` hubs check release site's `latest.json` every hour. New version waits until assistant / Agents idle, replaces binary / restarts, rolls back if unable to start.
- Files under `~/.persona/update/`: `<build>/` downloads, node binaries in `nodes/` later copied to `~/.persona/bin/nodes/` (the whole `<build>/` is removed once the update fails, is confirmed or goes back, and a hub start removes any other `<build>/` left behind); `pending.json` ongoing update, deleted after confirmation; `rolledback.json` latest rollback, recorded by old version, marked bad then deleted; `watch.log` watchdog output, `hub.log` rollback hub output without service manager.
- Previous binary ~/.persona/bin/personad.prev. Manual restore: `cp ~/.persona/bin/personad.prev ~/.persona/bin/personad && systemctl --user restart personad`; launchd uses launchctl kickstart -k. Disable auto-update in app afterward to avoid reinstall (rolled-back builds already excluded automatically).
- Disable in app Settings or `PUT /api/v1/hub/update/settings {"auto": false}`.
- Notifications: confirmed auto-update pushes Hub Updated to X; recorded rollback pushes Hub Update Failed + reason (`updater.notify`, all devices, thread `hub_update`). Manual updates no separate push.
- Direct `deploy.sh` / main-session scripts use compile-time build number newer than current public release, so no downgrade. Direct deployment during update is not undone by watchdog.

## Operations

- Offline machine: app shows last SSH error line. NAT computers need reverse tunnel first (`docs/operations/config.md`).
- Online but every task immediately `rpc: connection closed`: check launcher / assistant APIs both returning 502. RPC closure does not close SSH stdout automatically. Connection loop must also await `peer.Done()`, cancel old SSH subprocess and reconnect, or stale closed peer / online status persist. `TestMachineReconnectReplacesClosedPeer` uses real subprocess.
- Manual API: LAN `curl -H "Authorization: Bearer $TOKEN" http://<hub address>:18660/api/v1/machines`; public uses device token, HTTPS, key pinning; [Network](../operations/network.md#命令行验证).
- Public auth failures including WS handshake log auth source=… path=… status=… code=… failures=… suppressed=…. Mask sources IPv4 /24, IPv6 /64; fixed API paths only, no tokens. Rate limit 429 / `Retry-After`; `security.auth_log_interval_seconds` controls log throttling.
- Network issues: read-only personad hub doctor on hub machine, [Network troubleshooting](../operations/network.md#网络排查).
- Admin failed login: admin: wrong password from <masked source>. Password set / change, scope changes, computer add / remove, device revocation, pairing-code generation each `admin: …` log, no passwords / codes. Admin 401 / 404 uses request IDs too.
- Request IDs: `X-Request-Id` on every response, reusing app `X-Client-Request-Id` if present. Hub logs only ≥400 failures / >2-second slow requests, `[<id>] method path -> status (duration)`, no query / body / token. App 5xx adds “(Request <id>)”; find corresponding `journalctl --user -u personad` line.
- Compression (#2149, `gzip.go`): `Accept-Encoding: gzip` gets gzip for >1 KB JSON / JS / CSS / HTML / SVG / plain text (Lark chat 2.9 MB → 70 KB). App, web, Go clients decompress automatically. WebSocket upgrade, persistent connections, Range, HEAD, event-stream, images / binaries, already encoded / cookie-setting responses unchanged. Admin `/admin` and `/api/v1/admin/` uncompressed to avoid cookie / CSRF size leakage (BREACH). Layer outside `logRequests`; handlers still receive logging statusWriter. Debug `curl --compressed` or `-H 'Accept-Encoding: gzip'` and inspect Content-Encoding. The web client built into personad keeps its hashed assets gzipped already and sends them as stored (`web.go`, `docs/web/web.md` "Precompressed assets"); `<hub data dir>/web` is still compressed as it is sent. The compressible types and the 1 KB minimum live in `server/internal/webasset`, shared with the build's precompress step.
- Unreachable herdr socket / restart: node logs `herdr: … failed` at most once per 30 seconds, with failure count, then reachable again; SSH stderr enters hub log.
