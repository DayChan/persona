# Linked hubs (mesh): one primary

Pair several hubs into a group with **exactly one primary hub**: it serves all Apps, stores all state, and connects all computers. The other hubs are **members**, doing only two things:
forward App requests unchanged to the primary, and hand the node streams of their connected computers to the primary. Computers connect through a preinstalled service (resident node + persona-link connector), not SSH;
a computer reachable through several paths appears only once and uses the best path. The proposal is in [plan-hub-center.md](../archive/operations/plan-hub-center.md) (overview #293, this track's issue #435).
This document covers the implementation: protocol, tradeoffs, and verification.

> The recommended deployment is **one hub only**, with node services on all other computers ([plan-hub-center.md](../archive/operations/plan-hub-center.md) section 0). Linked hubs are for a few cases:
> always-on machines in two locations that cannot reach each other, or several existing hubs you want to combine. "Linked hubs" is under "Advanced" in the admin UI.

```text
 App / Web ──► Primary hub ◄══ Mesh link (WSS, pinned) ══ Member hub ◄── App (connecting to a member also works: it forwards to the primary)
                 │    ▲                                  │    ▲
 Hub connects (service)│    │Computer connects (link)        │    │  Member's computers: primary opens node streams through the member link
                 ▼    │                                  ▼    │
              Computer: resident node + persona-link connector
```

Code in `server/internal/hub/`: `mesh.go` (records, view loop), `mesh_view.go` (records and routing), `mesh_group.go` (primary, term, roles),
`peer_*.go` (pairing, links, dialing, forwarding, node streams, admin APIs, CLI); database in `internal/db/peers.go` (migration 0151) and `internal/db/center.go` (migration 0191);
multiple paths in `internal/devices/paths.go`, deduplication in `internal/devices/paths_group.go`, path selection in `internal/devices/paths_choose.go`, a computer's connection (create, use, switch) in `internal/devices/conn.go`, kind service (dialing connectors) in `internal/devices/service.go`; discovery in `internal/mdns/`.

The first version (#394, peer-to-peer mesh: each computer had an owning hub, events and subscriptions flooded between hubs, credentials shared across the group) has been replaced entirely; see "First version" at the end.

## Machine identity: machine_uid

- On first run the node generates `~/.persona/machine-uid` (UUIDv4, 0600) and keeps using it; `node.info` carries `machine_uid` (`internal/node/machineuid.go`).
  A corrupt file (not UUIDv4) is replaced with a new one.
- Cloned VMs carry the same file: run `personad node reset-uid` there, then restart the resident node.
- Older nodes lack this field: no deduplication, and each path counts as a separate machine.

## Pairing and trust

- **Linking code**: generated on hub B (admin UI **Advanced › Linked hubs › Generate linking code**, or `personad hub pair --peer`), single-use, 10 minutes:
  `persona://peer?hub=<hub_id>&name=<name>&fp=<public-key fingerprint>&tls=<TLS port>&url=<https candidate>…&code=jx_<64 hex digits>`.
  Only the hash is stored (`peer_codes`). A hub without TLS can neither generate a code nor use another hub's (409, with instructions to enable TLS).
- **Redeem** (A receives B's code, `POST /api/v1/admin/peers {"code"}` or `personad hub peer add`):
  1. A signs a linking credential for B (device credential, kind / role both `peer`, `machine_id` stores B's hub_id);
  2. A pins to the code's `fp` and races candidate addresses (LAN → public IPv6 → domain → public IPv4, starting one every 300 ms),
     `POST /api/v1/peer/pair` (`Authorization: Bearer jx_…`), carrying A's hub_id, name, version, fingerprint, TLS port, https candidates, and the credential from step 1;
  3. In one transaction, B consumes the code, signs a credential for A, revokes previous credentials signed for A, and stores A (`RedeemPeerCode`). It returns its own matching fields and A's credential,
     **plus B's group's primary and term**;
  4. A verifies that the returned hub_id matches the hub in the code, **joins B's group** ("Primary" below), and stores B. Failure revokes the step 1 credential.
  The result is mutual pinning and mutual credentials: each side stores the other's fingerprint, addresses, and credential signed for itself (AES-GCM encrypted with `~/.persona/secret.key` in `peers.token`),
  plus the id of the credential it signed for the other (`peers.credential_id`).
- `peer` credentials can only open `GET /api/v1/peer`; `jx_` codes can only call `POST /api/v1/peer/pair`; all other APIs return 403. Invalid / expired / used codes return 401, and public sources are rate-limited under
  existing `security` rules.
- **Disconnect** (`DELETE /api/v1/admin/peers/{hub_id}`, `personad hub peer remove`): first send the other side `unpair` on the link, then delete the local record, revoke locally signed credentials,
  and close the link (4012). The other side does the same on receipt; if unreachable then, its next dial is rejected (401), and the admin UI shows "The other hub does not recognize this hub's credential". A hub with no peers after disconnecting
  becomes its own primary (see below). Directly revoking a linking credential under "Devices" is equivalent to disconnecting that hub.

## Mesh links

- `GET /api/v1/peer`: WebSocket, TLS port only (plaintext always returns 403 `tls_required`, even on loopback; addresses in `mesh.plaintext.allow` are exempt, see "Plaintext links"), pinned by `fp`; `Authorization: Bearer <peer credential signed for me by the other hub>`;
  permessage-deflate. Only paired hubs can open it.
- The link runs the node protocol's JSON-line RPC (`internal/rpc`) and streams (`internal/streams`):
  - One-way: `rec` (record), `stream.data` / `stream.ack` / `stream.close` (streams);
  - Calls: `hello {hub_id,name,version,epoch}` (each side calls once and verifies hub_id), `stream.open {id}`, `unpair`,
    `relay {to,from,ttl,method,params}` (a call forwarded to a particular group member: `creds.import`, `creds.revoked`, `pair.code`, see below).
  - HTTP/1.1 runs on streams: App requests forwarded by members to the primary, and node streams opened by the primary via members (`peer_http.go`, `peer_node.go`). The peer has an `http.Server` accepting only these streams
    (no listening socket), so these requests can only come from authenticated links.
- **One link per pair of hubs**: both dial. The lower hub_id dials immediately; the higher waits 0.5–2 seconds each time. If both connect, keep the one dialed by the lower hub_id and close the other with 4011.
- Heartbeat and reconnect follow persona-link: both sides ping every 10 seconds, disconnect after 5 seconds without pong; exponential backoff from 1 second to at most 1 minute, randomized from half to the full delay; credentials rejected (401 / 403 / 4012) wait 5 minutes.
  These are configured values, not measured. Ping round-trip time is the link RTT (used in admin UI and path selection).
- Close codes: 4011 duplicate link, 4012 unpaired, 4013 cannot send (send queue full, 8192 messages), 1001 hub shutdown.

## Records and routing

Each hub floods a **record** of itself (`hubRecord`, `mesh_view.go`) through the group: hub_id, name, version, start time (epoch), sequence (rev),
TLS port, fingerprint, https candidates, **connected links** (peer hub_id and RTT), **its recognized primary and term**, and each computer in hub.json (local id, config for the primary:
launchers, Claude / Codex settings, but no ssh host, tailcat address, connector address or credentials; path availability `avail`, and RTT for its segment).

- Newer (higher epoch, rev) replaces older; a new record is forwarded to other links. When a link opens, send all records held locally. Own records are rebuilt within 150 ms of machine, link, address, or primary changes;
  unchanged contents are not sent. Also rebuilt every 30 seconds.
- **Routing**: an edge exists only when both hubs' records list each other; from self, shortest paths prioritize "fewest hops → lowest total RTT → lowest next-hop hub_id" (`meshView.routes`). Full pairwise connectivity is unnecessary:
  with A–B and B–C, A reaches C through B (member-to-primary forwarding, primary-to-member stream opening, and `relay` calls all use this route; TTL 8, returns 508 at 0).
- Records are stored under `mesh.records` in `settings` (at most once per minute, and at shutdown); unseen for 7 days means no longer used.

## Primary

(`mesh_group.go`, state in Postgres `mesh_group`, migration 0191; stored by this hub's hub_id, so another hub using the same database (restored backup, shared test DB) is not mistaken for a member.)

- Each hub records **the primary's hub_id and term number**. A never-paired hub is its own primary, term 0. Records carry both; **the higher term wins**, and at equal terms the lower primary hub_id wins
  (only encountered with simultaneous changes); a higher value received is adopted and stored.
- **Join a group = paste the other hub's linking code**: A pastes B's code, changes its primary to B's group's primary (reported in B's response), and takes the largest term seen by either side plus one. Other hubs in A's group
  see the higher term in A's record and join too. Thus "the side pasting the code and its group join the code issuer's group".
- **Set as primary** (admin UI **Advanced › Linked hubs › Hubs in the group › Set as primary**, `POST /api/v1/admin/peers/{hub_id}/center`, `personad hub peer center [<hub_id>] [--confirm]`):
  increment the term and make that hub primary. Only a currently reachable hub (or self) can be selected. If the former primary is online, it sees the higher term in records, becomes a member, and hands credentials to the new primary ("Credentials" below).
  **Former primary unreachable**: confirmation required (`{"confirm": true}`, otherwise 409 `confirm_required`), explaining that the old primary becomes a member when it returns.
- **No automatic switch when the primary is unreachable**: automatic switching could create two primaries during a network partition, each writing separate state. Members return `503 {"code":"center_unreachable","center":{"hub_id","name"}}` to Apps;
  the admin UI's top banner offers a manual switch.
- **Old primary returns**: its term is lower; when a link opens it sees the higher term in the peer's records and becomes a member. A restarting hub with peers waits 10 seconds or until the first link opens after startup
  (`meshSettling`) before connecting its own computers, so an already-replaced old primary does not briefly grab them before learning the change.
- **Leave the group**: a hub with no peers after disconnecting becomes its own primary (term incremented). If the group splits in two and both halves still have peers, the half without the primary stays "Primary unreachable" until a primary is manually selected in the admin UI.
- On role changes: becoming a member stops driving its own computers (the run loop parks, `parkReason`), disconnects App connections it was serving (reconnects are forwarded to the primary), and disables its services (below);
  becoming primary disconnects requests being forwarded to the old primary and begins connecting all computers.

## Members forward Apps

(`peer_http.go`.)

- `/api/` requests received by a member (including WebSocket), except those it answers itself, are forwarded **unchanged** (including `Authorization`) over the link to the primary (hop by hop for multi-hop routes).
  Answered locally: `/api/v1/health`, persona-link's `/api/v1/link`, `/api/v1/link/stream`, hub-to-hub `/api/v1/peer`, `/api/v1/peer/pair`,
  admin `/api/v1/admin/…`, `/api/v1/auth/pair-codes` (`personad hub pair`), and `/api/v1/auth/tokens` for **its own link computers** exchanging pairing codes for credentials
  (codes it issued, carrying a machine id). Web static files and `/admin` pages are not forwarded.
- **Source classification finishes at the member**: public plaintext requests are already rejected by its gate; the member puts the result in `X-Persona-Peer-Source: <lan|public> <client address> <tls|plain>`,
  trusted by the primary (the link endpoints are paired, and the header can only arrive over the link; same-name incoming headers and `X-Forwarded-For` are stripped before forwarding). The primary uses this address as `RemoteAddr`,
  so logs and source-dependent checks use the real client. Loopback at the member counts as LAN (the main token accepts only loopback on the primary's own machine).
- **Rate limiting is at the member**: blocked sources receive 429 directly; when the primary returns 401, the member records one failure for that source (`codeWriter`). The primary does not count forwarded requests again, avoiding double counting.
- Primary changes: members disconnect requests being forwarded (including WebSocket), and Apps reconnect to the new primary.
- **App pairing**: `personad hub pair` on a member asks the primary for a pairing code over the link (`pair.code`); the QR code carries the primary's address, and the App pairs directly to it.
  The member admin UI's "Devices" page returns 409 `member_hub`, pointing to the primary's admin UI.

## Credentials and push

- **Credentials handed to the primary**: once a member can reach the primary (once per primary and term; failures retry after 30 seconds), it hands over all its valid App credentials (role `full` / `answer`: id, name,
  kind, role, hash, creation time, not the credential itself) via `creds.import`. The primary deduplicates by id / hash and stores them in its `device_tokens`. Afterwards only the primary authenticates credentials;
  phones already paired to a member need not re-pair. persona-link and mesh credentials remain on their respective hubs (they open only that hub's links).
- On primary changes, the former primary likewise hands its credentials (including those members originally handed it) to the new primary.
- **Revocations reach every copy**: each hub database has a credential copy; revoking only on one would leave another copy usable when that hub becomes primary or leaves the group. So the hub sends revoked App credentials
  (`creds.revoked`, the entire list) to every reachable hub in the group: on revocation, when a hub becomes reachable, and every 5 minutes (to fill in lost messages).
  Previously unseen credentials also get a revoked record (`kind: tombstone`, excluded from the device list), so an old copy handed over later (`creds.import`, deduplicated by id / hash) cannot revive it.
  Only `full` / `answer` are revoked this way. Revocation on any hub applies group-wide: credentials revoked while a member was outside the group also become invalid on the primary when it rejoins.
- **Only the primary sends push**: device push registration is forwarded by members to the primary, and push payload `hub_id` is the primary's. Members stop pushing Live Activities.

## Services disabled on members

Members retain their data and resume after leaving the group (becoming their own primary): scheduled tasks (`scheduleLoop` does not run due tasks), Inbox collection, Lark events and `/agent`, knowledge base archiving / compression /
project refresh, Moments fetching, and Live Activity push. Member hub-level data such as the assistant's main chat and knowledge base are not merged into the primary (the App sees the primary's data).

## Paths via members (node streams)

(`peer_node.go`. Since 2026-11-04, the computers at each end belong to their respective managers ([machines.md](machines.md) ("Who connects to computers")): the primary's manager asks the primary hub for this stream
(`GET /api/v1/bus/relay?hub=<member>&machine=<id>`); the member hub hands it to its own manager to open (`devices.accept` kind `node`). References below to a "hub" opening a node mean that hub's manager.)
When the primary connects a computer listed only in a member's hub.json (or also listed locally, but the member's path is currently better), it sends an HTTP upgrade request over the link:
`GET /api/v1/peer/node`, headers `Upgrade: persona-node`, `X-Persona-Peer-Node: <member hub_id>/<its machine id>`, `X-Persona-Peer-From: <primary>`, forwarded hop by hop to that member.
The member checks that the request comes from its primary, it is a member, and its hub.json lists the machine, then opens the node using hub.json's method (local node, ssh, tailcat, that machine's persona-link, connector).
Only after opening does it return `101 Switching Protocols`; bytes are then copied unchanged in both directions. Failure returns 502, with `error` holding the reason (displayed by the primary on that machine). The primary runs the node protocol on this byte stream, just as on other paths. The member does not drive the computer itself.

## Multiple paths to one computer

(Since 2026-11-04, in the manager connecting computers: `server/internal/devices/paths.go`, `conn.go`. The hub supplies the group's view in `mesh` of `devices.config`: whether this hub is a member,
which other hubs are reachable and how far away, and computers in member records. The manager deduplicates, selects, and switches paths, reports each machine's paths (`devices.state`), and the hub also uses its report for local-computer availability in records: each computer's `avail` and RTT come from the manager that owns it, and none while that manager is away.)

- **Path**: each entry in the primary's hub.json is a path (`local` / `service` / `link` / `ssh` / `tailcat`; `ssh` with `tailcat_addr` remains one path, trying ssh then tailcat).
  Each computer in each member record is a "via member" path (`hub`).
- **Deduplication**: after a path connects, `node.info` identifies the computer reached (`machine_uid`), remembered in `mesh.path_uids` in `settings` (path → uid). Paths with the same uid form **one machine**;
  paths with unknown uid count separately (unknown until first connection: both connect and bump each other once, then it is known). Older nodes without uid are not merged.
  If the uid changes on connection (a member changed this id to another computer), regroup and assign the path to the computer it actually reaches.
- **Machine id**: if a path is listed in the primary's hub.json, use its config id (with several there, use the first entry; later entries' ids also work and point to the same machine);
  if configured only on a member, use that member's config id, appending `@<slug of member name>` on collision (then the member hub_id's first 6 characters, then a number). Member-computer ids live in
  `mesh.machine_ids` in `settings` and survive restart.
- **Path selection**: local > direct (service / link / ssh / tailcat) > via member; within a class, paths with RTT first, then lower RTT. Availability: link needs its connection; service needs the connector's latest
  info response (or no probe yet); via member needs a reachable member reporting the path available; ssh / tailcat are known only after trying. Failed paths back off by failure count (doubling from 2 seconds to 1 minute),
  but a connection lasting a full minute waits only 2 seconds after it drops.
- **Stickiness**: keep a healthy active path. On failure, select the next usable path immediately. Switch only if another path is **clearly better** (better class, or same class with RTT over 30% lower and at least 5 ms lower) and stays so for
  `mesh.path_switch_seconds` (default 60 seconds): connect via the new path, receive `node.info`, then release the old one (the resident node's new attach displaces the old, which ends itself).
  The App never sees offline in between (interactions and terminals follow reconnect behavior: resumable interactions are reclaimed by hooks, terminals prompt to resubscribe). If switching fails, count a failure for that path and retain the original.
- **RTT**: ping the node on the active path every 30 seconds. Inactive paths: service uses connector-info round-trip time; link uses persona-link connection ping; via member uses link RTT to that member
  plus the member's reported segment. These are measured single values, not percentiles, used only for comparison and not cited elsewhere.
- **Fields for Apps** ([protocol.md](../protocol/protocol.md) ("Data types")): `hub_id` / `hub_name` identify the hub currently used to reach this computer (the primary for direct connections, that member for member routes);
  `route {via: local|service|link|ssh|tailcat|hub, hops: 0|1, rtt_ms}`; with more than one path (or only via member), add `paths`: each path is
  `{via, hub_id, ok, rtt_ms, active}`.
- The admin UI's "Computers" lists each machine's paths; `GET /api/v1/admin/routes` gives the full debug view (routes to every hub and every path to every computer).

## kind service: Hub connects

(`service.go`; connector side in [link.md](../operations/link.md) ("Passive mode").)

- hub.json: `{"id":"example-workstation","name":"Dev machine","kind":"service","address":"<host>:18663","proxy":"socks5://<host>:1080"}`, with optional `proxy`.
  Connector fingerprints and credentials are in Postgres `connector_credentials` (migration 0191), not hub.json: credentials are sealed with the hub's own `~/.persona/secret.key` AES-GCM before storage (the manager seals model-provider keys with its own, [managers.md](../assistant/managers.md) ("The built-in manager's own files"))
  and mail credentials.
- **Pairing**: on the computer, `persona-link pair --listen` (or installer `--node --listen`, menu bar "Let hub connect") prints `persona://connector?addr=…&fp=…&code=…`;
  paste under **Computers › Add computer › Hub connects** in the admin UI (address editable, proxy optional). The hub parses with `link.ParseConnectorLink`, pins by `fp` (TLS 1.2+, ALPN http/1.1,
  SPKI fingerprint only, no name verification), tries addresses in link order, and sends `POST /api/v1/connector/pair {code, token, machine, hub_id, hub_name}`. The hub generates the credential:
  `jc_` + 64 hex digits; the connector stores only its hash. Write hub.json only on success; the machine connects immediately without restart. Wrong / expired code, 401: "Invalid pairing code…"; pin mismatch: error, nothing sent.
- **Connect**: `GET /api/v1/connector/stream` (WebSocket, `Authorization: Bearer`, permessage-deflate); the stream carries node bytes. The hub pings every 10 seconds, disconnects after 5 seconds without pong (configurable with hub.json `link`, passed to the connector at stream open in `X-Persona-Ping`, [link.md](../operations/link.md) ("Heartbeat")).
  503 `node_unavailable`: show the connector's reason as the machine error and back off normally.
- **Probe**: every 30 seconds, `GET /api/v1/connector/info` (no attach, no effect on the active connection); a response whose `machine` matches means available, and the round-trip time is that path's RTT.
  Each hub probes connectors in its own hub.json; members put results in records. If a stream is closed with 4002 while a probe or dial is in flight, or the computer is re-paired, discard that result
  (#2960: a late 200 must not clear "Re-pair", and a late 401 for old credentials must not discard new credentials and make a fresh pairing wait 5 minutes).
- **Backoff**: 401 (connector rejects credentials) suppresses dials and probes for 5 minutes: the connector counts failures by source and blocks for 15 minutes after 10 in a minute, so the hub must not get its own address blocked;
  429 follows `Retry-After`; stream closed with **4002** (connector paired to another hub): mark "Connector already paired to another hub… re-pair" and **stop trying** until re-paired in the admin UI
  (`POST /api/v1/admin/machines/{id}/connector`); **1001** (connector stopped or switched back to active mode) uses normal reconnect backoff.
  Every connector pairing closes old-credential streams with 4002, including re-pairing to this hub: only 4002 on the current pairing's stream counts; if an old pairing's 4002 is read after re-pairing,
  do not clear the new credential or mark "Re-pair" (#3172).
- **Proxy**: `proxy` is `socks5://[user:pass@]host:port` (`golang.org/x/net/proxy`); dialing, pairing, and probing all use it. The admin UI strips the password for display.

## SSH / tailcat: compatibility

- Existing kind `ssh` / `tailcat` entries in hub.json still work; the admin UI no longer adds them (`POST /api/v1/admin/machines` returns 400 `ssh_unsupported` for ssh / tailcat),
  and the computer list marks them "ssh is no longer recommended". Removal comes later.
- For link / service / via-member computers, "Redeploy" now instructs you to update the node service on that computer (pushing updates through the connector is #284).
- **Migrate ssh to service (same id)**:
  1. Install the node service there and let the hub connect: `install.sh --node --listen` (Linux) or the Persona Link menu bar App; get a link from `persona-link pair --listen`;
  2. change this machine's `kind` in hub.json to `service`, replace `ssh_host` with `"address": "<host>:18663"`, restart the hub;
  3. the admin UI shows "Connector not paired… re-pair"; tap "Re-pair" and paste the link.
  If changing the id is fine, it is simpler: add a new id under **Add computer › Hub connects**. Two paths to the same computer merge automatically (using the first id in hub.json);
  once connected, delete the ssh entry and restart.
- **Migrate to link** (Computer connects): add a new id under **Add computer › Computer connects**, or change kind to `link`, remove `ssh_host`, and pair with `personad hub pair --machine <id>`.

## Discovery (mDNS)

- TLS-enabled hubs advertise `_persona-hub._tcp` over mDNS (instance name hub_id); TXT contains only `hub_id`, `name`, `tls` (TLS port), `fp`, `v` (version), never credentials.
  They also browse that type; the admin UI's "Discovered hubs" lists unpaired hubs heard within 10 minutes. Discovery only lists them, never links automatically.
- `internal/mdns`: only the standard library and `golang.org/x/net`, sharing 5353 with system mDNSResponder / avahi; implements only a subset of RFC 6762 / 6763. No sending or receiving on point-to-point interfaces.
- Docker bridge networks cannot send multicast out, so use "Add hub" and enter an address (`network_mode: host` works). Disable with hub.json `mesh.discovery: false`.
- Hubs whose TLS listener is loopback-only (`127.0.0.1` / `::1` / `localhost`) listen but do not advertise: nobody on the LAN can reach them, so advertised addresses would be unusable.
- Probe before manual addition: `POST /api/v1/admin/peers/probe {"address"}` only requests the peer's `GET /api/v1/health` (no redirects, 5 seconds, response at most 64 KB).

## Admin APIs and CLI

Admin UI ([admin.md](../operations/admin.md)):

| API | Purpose |
| --- | --- |
| `GET /api/v1/admin/mesh` | `{"role":"center"\|"member","center":{"hub_id","name","online"},"term":3,"hubs":[{"hub_id","name","role","online","self","paired"}]}`: `hubs` contains every hub in the group, including self |
| `GET /api/v1/admin/peers` | Paired hubs (each with `role`, `state`, `rtt_ms`, `machines`…) and LAN discoveries |
| `POST /api/v1/admin/peers/{hub_id}/center` | Set as primary (hub_id may be self); `{"confirm":true}` when the former primary is unreachable; 409 `confirm_required` / `unreachable`, 404 `not_in_group` |
| `POST /api/v1/admin/peers/code`, `POST /api/v1/admin/peers`, `POST …/probe`, `DELETE …/{hub_id}` | Generate a linking code, join using a peer's code, probe an address, disconnect |
| `GET /api/v1/admin/routes` | Debug: route to every hub and every path to every computer |
| `GET /api/v1/admin/machines` | Adds `hub_id`, `paths`, `address`, `proxy` per machine; kind `hub` means a computer configured only on a member (cannot delete) |
| `POST /api/v1/admin/machines` | kind `link` (Computer connects) or `service` (`pair_link`, optional `address`, `proxy`); starts connecting immediately after adding |
| `POST /api/v1/admin/machines/{id}/connector` | Re-pair a service computer: `{"pair_link","address"?}` |

The CLI on the hub machine uses the same APIs (prefix `/api/v1/admin/cli/`, accepts only loopback + main token): `personad hub pair --peer`, `personad hub peer add '<linking code>'|-`,
`personad hub peer list` (first reports whether this hub is primary or member; other hubs' names, ids and addresses show control characters as `<ESC>` / `<U+XXXX>`), `personad hub peer remove <hub_id>`, `personad hub peer center [<hub_id>] [--confirm]`.
Docker deployment on NAS: `sudo /usr/local/bin/docker exec personad /opt/persona/bin/personad hub …`.

## Plaintext links (optional, #4514)

For hubs on a trusted private network / dedicated line, or with a device terminating TLS between them, links can run without TLS: list allowed plaintext-link addresses in hub.json. Off by default;
without `mesh.plaintext.allow`, behavior is exactly as above. Code in `server/internal/hub/peer_plain.go`.

```json
{"mesh": {"plaintext": {"allow": ["192.0.2.0/24", "198.51.100.7"], "advertise": ["http://<this hub's address>:18660"]}}}
```

- `allow`: each entry is one of the following (case-insensitive):

  | Syntax | Meaning | Inbound (peer connects) | Outbound (this hub connects) |
  | --- | --- | --- | --- |
  | `*` | All addresses, including public (WARNING at startup) | ✓ | ✓ |
  | `192.0.2.0/24`, `2001:db8::/32`, `192.0.2.7` | Network, single address | ✓ | ✓ |
  | `192.0.2.*`, `198.51.*.*` | IPv4 wildcard; `*` only in trailing segments, equivalent to the corresponding network | ✓ | ✓ |
  | `hub.example.com`, `*.example.com` | Host name, domain wildcard (`*.` matches names at any depth below it, not the domain itself; forms such as `*.com` are rejected) | ✗ | ✓ |

  Names control only outbound dialing: inbound connections have addresses, not names, and peers can put whatever they want in reverse DNS. Dialing an address matching a name rule uses whatever address DNS resolves
  (trusting both the name and the DNS answer); for names not matching a name rule, the resolved address itself must match an address rule.

  **Only between these addresses** can pairing and links use `http://` / `ws://`; both directions check it:
  - Inbound: plaintext `POST /api/v1/peer/pair` and `GET /api/v1/peer` requests are allowed only when the source address is in `allow` (the gate's public-plaintext 403 also makes this exception for these two paths).
    Otherwise 403 `tls_required`, including private-network plaintext: pairing previously allowed private-network plaintext, now tightened too. Container gateways (forwarding for everyone) never count;
    receiving plaintext in Docker bridge mode requires `security.proxy_protocol` for the real address, or use only peer-to-self or self-to-peer dialing (one working direction is enough for a pair of hubs).
  - Outbound: peer `http://` candidates are dialed only if the target is in `allow` (table above). All `http://` candidates rank after `https://`;
    when both work, use TLS.
  - Any invalid entry (such as `192.*.2.*`, `192.0.2`) disables plaintext linking entirely, with an explanation in startup logs. Enabling it logs a WARNING at startup.
- `advertise`: addresses other hubs use to reach this one in plaintext (`http://host:port`, no path), included in linking codes (at most 3, skipped by old hubs that do not recognize them) and group records
  (`plain_endpoints`, a separate field). **Never sent to Apps**: `mesh` in hello contains only `https://`. A hub without TLS also includes its plaintext addresses.
- **A hub without TLS** can link with `allow` configured: its linking code has no `fp`, only `http://` addresses, and both sides use plaintext only. A code with `https://` addresses but no `fp` is still incomplete.
  The admin UI's "Linked hubs" page still displays "TLS required"; without TLS, use CLI `personad hub pair --peer` / `personad hub peer add`.
- Risk: plaintext links have neither encryption nor fingerprint checks; linking credentials, App requests forwarded by members to the primary (including App credentials), and node streams can all be viewed and changed by anyone on the path.
  Possessing a linking credential allows impersonating this hub and opening a link. Use only on networks where you trust every device on the path; use TLS across the public internet.

## Config (hub.json)

```json
{"mesh": {"discovery": true, "path_switch_seconds": 60, "plaintext": {"allow": [], "advertise": []}}}
```

All optional; `plaintext` is covered in "Plaintext links". Peer relationships, the primary and term, and connector credentials live in Postgres, not hub.json. The first version's `mesh.takeover_seconds` is unused (harmless if still present).

## Security boundaries

- Linking trust is mutual: paired hubs can send any App request over the link to the primary (with their claimed source) and ask members to open node streams to their computers (members recognize only their own primary).
  Link only your own hubs; compromising one compromises the whole group.
- Links use TLS and fingerprint pinning (except addresses in `mesh.plaintext.allow`, which have neither encryption nor pinning, see "Plaintext links"); linking codes are single-use, 10 minutes; credentials are stored only as hashes (issued) or encrypted (received).
- The forwarding HTTP server has no listener and accepts streams only from authenticated links, so `X-Persona-Peer-*` headers cannot be forged from outside; incoming same-name client headers are removed or overwritten before member forwarding (`X-Persona-Peer-Node` and `-From` removed, `-Source` and `-Ttl` overwritten).
  Older members do not strip these two headers, so upgrade every hub: the primary refuses node streams with `-From` set to itself (valid streams originate only at the primary and can bounce back only during a route change; the origin retries after backoff), but an old member cannot block this when its next hop is not the primary.
- Members do not authenticate credentials: the primary is the sole authority; revocations take effect there immediately (revocation clears the lookup cache; a concurrent lookup may allow up to 30 more seconds), and propagate to the other group hubs ("Credentials and push").
- Connectors: the hub pins to the pairing fingerprint and stores credentials encrypted in the database; connectors open only streams to the resident node.
- mDNS advertises only public information (already in unauthenticated `/api/v1/health`).

## Verification

- In-process end to end (`server/internal/hub/mesh_center_test.go`, two or three hubs: 127.0.0.1, temp directories, TLS, in-process fake nodes, in-memory Store,
  and a fake connector following link.md's "Passive mode" behavior), no database needed:
  - `TestCenterMemberForwardsEverything`: exactly one primary after joining; members do not drive their computers, and the primary connects through them. An App connected to a member (using a member-issued credential already handed to the primary)
    sees both machines, subscribes to items (history + live), sends messages, reads directories, handles interactions on the member's computer (pushed to the App, answered through the member to the node), and takes over the terminal; hello belongs to the primary,
    `mesh.hubs` identifies it; unknown credentials still return 401 through a member; admin API roles.
  - `TestCenterUnreachable`: primary stops, member returns 503 `center_unreachable` (with primary name), does not promote itself.
  - `TestCenterSetAndOldCenterReturns`: set primary while both are online (term incremented); primary stops, switching requires confirmation; old primary restarts with an old term and becomes a member.
  - `TestCenterOneComputerTwoPaths`: one resident node, one direct primary path and one via member: listed once, direct path used; direct fails, switches immediately to member (subscriptions continue);
    once direct returns, switches back once after `path_switch_seconds`, with no further connection in the next 3 seconds.
  - `TestCenterTwoEntriesOneComputer`: two primary hub.json entries reach the same computer: one machine, two paths, second id points to the same machine.
  - `TestCenterServiceConnector`: unpaired prompts to pair; wrong code / fingerprint rejected; after pairing, credentials encrypted in DB, online through connector, subscriptions; connector paired to another hub (4002)
    marks re-pair and stops probing; wrong database credentials show connector rejection.
  - Also `TestMeshDuplicateLink`, `TestMeshPairingCode`; `internal/db`'s `TestPeersAndPeerCodes`, `TestMeshGroupAndConnectors`,
    `TestAppTokensMoveToTheCenter` (require database); `internal/mdns` unit tests.
  - Run: `cd server && go test -count=1 -p 1 -run 'TestCenter|TestMesh|TestPeer' ./internal/hub/`; `PERSONA_MESH_LOG=1` prints each hub's logs.
- Local smoke test with two real hubs (2026-11-02, no real `~/.persona` touched): separate temporary HOMEs, different ports and `public.tls_listen` in hub.json, separate schemas in the test database
  (`database_url` adds `&search_path=<schema>`), `public.advertise` set to `https://localhost:<TLS port>`, `PERSONA_LOCAL_NODE=0`, started with `env -i`.
  A third temporary HOME runs real `personad node --attach` and `persona-link pair --listen 127.0.0.1:<port>` + `persona-link run`. Verified:
  paste connector link in admin API → paired, immediately online via service; App credential lists the machine and reads its directories; after `personad hub pair --peer | personad hub peer add -`,
  `peer list` shows one primary and one member; member-issued credentials handed to the primary work through the member and directly at the primary; `personad hub pair` on the member produces the primary's code; primary stops, member returns 503;
  `peer center --confirm` switches primary; old primary restarts as a member, and the new primary reaches the computer through it (its connector). Headless Chrome inspected the primary's "Computers" and "Linked hubs", and the member's "Overview" and "Computers" (light and dark).
  No herdr / claude started on this computer (would touch the real environment), so sending and terminals were not tested through the real connector (covered by in-process tests).

## Not implemented / known limitations

- Automatic primary failover; pushing node updates through the connector (#284); removing SSH support: all outside this round (plan section 8).
- Switching primary does not migrate primary data: assistant, Inbox, knowledge base, scheduled tasks, push registrations, and interaction history stay on the old hub; the new primary starts with its own data (device credentials are handed over).
  Push works only after Apps re-register with the new primary.
- Revocation synchronizes through links, without a quorum: if a hub is unreachable at revocation and never subsequently connects to a hub that knows it, its copy remains usable. For example, the primary fails permanently and a member that has always been offline is made primary;
  revocation applies only when the old primary returns. Members also stop synchronizing once disconnected and outside the group.
- A new path's destination computer is unknown at first; two paths both connecting bump each other once (remembered afterwards).
- Deleting a computer in the admin UI requires restart to take effect (adding is immediate); changing a service computer's address also requires restart.
- `/api/v1/usage` and phone tools (`phone_*`) count only the primary's own.
- The member's "Overview" computer count uses its own hub.json; computers "Connected by primary" appear offline.

## First version (replaced)

#394 built a peer-to-peer mesh: every hub served Apps, computers were connected by an "owning hub" and other hubs forwarded through it; events, session lists, and interactions flooded through the group; App subscriptions had proxies
on the owning hub; credentials worked group-wide (asking the issuer by hash); pushes went through the credential-issuing hub. After seeing it, the user switched to a single primary (plan-hub-center.md), and these were all removed:
`mesh_events.go`, `peer_clients.go`, `mesh_owner.go`, `mesh_push.go`, `mesh_metrics.go`, `mesh_ids.go`. Machine identity, pairing, links, records and routing, discovery, and admin APIs remain.
