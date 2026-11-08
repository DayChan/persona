# Codex app-server: multi-instance integration research

## Scope and conclusions

2026-11-01. The goal is to bring **multiple existing app-servers on one machine, with multiple Agents / threads per server**, into Persona while the desktop app keeps working normally and the phone can view chats, send messages, answer questions, and approve requests. app-server is an Agent protocol entry point, not a new terminal engine; users must not have to switch the whole machine away from herdr to use it.

The research phase was source inspection only: no takeover or restart of existing app-servers, no real messages, permission changes, or third-party service installation. See the end of this document for the first implementation on 2026-11-02 and its acceptance boundaries; earlier source line numbers remain pinned to the research baseline. `repos/` is ignored by `.gitignore`; upstream code is not committed to Persona. Personal directories / endpoints must come from configuration or discovery, never hardcoded.

### Research versions

| Subject | Pinned version | Use |
| --- | --- | --- |
| Upstream source | `openai/codex` main, `67f0994b532b8740f14b5b738af7b7b2a915b1d6` (2026-10-31) | `repos/codex/`, read-only; source not built / executed |
| Desktop app protocol comparison | `rust-v0.163.0-alpha.10.2`, `d9151c94a70013f176ff370fa93da02734662e41` | A small amount of startup / protocol material read through Git; no experiments in user processes |
| Existing Persona documentation baseline | codex-cli `0.159.3` (`docs/assistant/assistant.md`) | Do not assume existing clients have every capability in the new main |
| Persona research baseline | `7d44afae9de547078627e3f4b77fafa7230a7153` | Persona line references in this document correspond to this version |

All `codex-rs/…` paths below are relative to the pinned main commit in `repos/codex/`; evidence from version tags is identified separately. Source links can use `https://github.com/openai/codex/blob/<pinned-commit>/<relative-path>#L<line>`, avoiding moving main references.

### Confirmed core findings

1. **Integration is possible if there is a connectable endpoint**: upstream supports multiple connections and threads per server. A running Unix/WebSocket server lets desktop and Persona connect separately. An ordinary stdio process has no "attach a second client" endpoint.
2. **Unix sockets also carry WebSocket**, not newline-delimited JSON. Persona's Unix JSON-lines RPC client cannot dial them directly.
3. **Do not start another app-server and resume the same thread to simulate takeover**: a writer file lock in the same home rejects a second writer. The lock provides no owner endpoint and is not a takeover protocol. Never delete it or force a resume.
4. **There is no generic side-effect-free subscription RPC**: read/list do not load threads; start/resume/fork establish real-time subscriptions. Resume may cold-start, reconfigure, or dispatch an existing queue. Do not resume as a side effect of discovering history.
5. **The same approval can be sent to multiple subscribers**, and the first response consumes the callback; both response and error can consume requests. `serverRequest/resolved` only proves that a request ended, not that it was approved. The phone must remove cards answered on the computer and must not manufacture authorization from notifications.
6. **Model process, home, thread, and connection generation separately**: visibility on disk does not mean this server is running the thread. Reading status from the wrong server can even show another process's unfinished turn as Interrupted.

### Spot checks of the current desktop version (not a guarantee of all main capabilities)

These line references are relative to `rust-v0.163.0-alpha.10.2`, checked with `git show <tag>:<path>`:

- `codex-rs/cli/src/main.rs:583–594` already lists `--listen stdio://|unix://|unix://PATH|ws://IP:PORT|off`, defaulting to stdio; `app-server/src/lib.rs:793–837` dispatches the three transports.
- `app-server-transport/src/transport/unix_socket.rs:176` already uses `accept_hdr_async_with_config` for WebSocket upgrade; this is not new to main.
- `app-server-protocol/src/protocol/common.rs:580,853` already has unsubscribe / loaded/list; no standalone thread/subscribe was found in the method table.
- `rollout/src/writer_lock.rs:17,65–70` already uses a home-based writer lock and `try_lock`; `app-server/tests/suite/v2/thread_resume.rs:319–383` has legacy/paginated regressions for two processes contending for one thread.
- `app-server/src/request_processors/thread_processor.rs:4569–4612` already has warm-resume configuration-difference / subscriber conditions; `thread_queue.rs:430` already tests cold resume dispatching a persisted queue.
- `app-server/src/outgoing_message.rs:473,567–585` already takes the callback on response; `thread_lifecycle.rs:963` replays requests after resume; `thread_resume.rs:5577,5714` tests command / file approval replay.

This is static evidence; the tests were not run. main's protocol continues changing (its method table already has additions relative to the tag). Special eligibility rules such as HumanCommand / user verification require version adaptation. The V2 name does not guarantee identical wire shapes or capabilities; successful initialization is not a promise that every experimental method is supported.

## Connection endpoints: what can and cannot be shared

| Endpoint | Actual semantics and recommendation | main implementation evidence |
| --- | --- | --- |
| stdio | One fixed stdin/stdout and one ConnectionId; no accept/attach. If the desktop process exposes no other endpoint, sync local history only and disable control. Do not seize pipes, inject initialization, or kill and replace the process. | `codex-rs/app-server-transport/src/transport/stdio.rs:52–95` |
| Unix | Recommended protected local endpoint. Default path: `<CODEX_HOME>/app-server-control/app-server-control.sock`; uses WebSocket upgrade/frames, with independent connections. Long paths have secure rendezvous handling; do not guess sockets from strings. | `app-server-transport/src/transport/mod.rs:56–73`; `codex-rs/app-server-transport/src/transport/unix_socket.rs:148–245` |
| TCP WebSocket | Independent handshake and responses per connection; node can dial loopback. main rejects HTTP requests with Origin and refuses non-loopback startup without authentication. Never let a browser / phone dial a bare Codex service directly. Authentication is checked at upgrade; revocation does not imply existing connections become invalid immediately. | `codex-rs/app-server-transport/src/transport/websocket.rs:99–127,160–184`; `websocket-auth/src/lib.rs:286–318` |
| managed daemon | The daemon starts `app-server --listen unix://`, with default instances separated by home; it does not automatically absorb a running desktop stdio process. Connecting to an existing daemon and ensuring one exists (starting it if necessary) are different operations; discovery may only do the former. | `app-server-daemon/src/backend/pid.rs:392–405`; `app-server-daemon/src/lib.rs:793–840` |
| OpenAI remote-control | A separate account / relay authorization system, not an alias for local WebSocket. Depends on a ChatGPT account, backend/managed requirements, etc.; not a shortcut for Persona auto-discovery. `remoteControl/enable` changes an existing controller's state; it does not add a local listener to an external stdio process. | `app-server-transport/src/transport/remote_control/auth.rs:100–143`; `app-server/src/request_processors/remote_control_processor.rs:41–60` |

Unix socket files use mode 0600, with owner/permission checks for directories and rendezvous too (`codex-rs/app-server-transport/src/transport/unix_socket.rs:79–98,325–340,497–503`). Persona must not loosen permissions to connect. Local transports found in source are selected at startup; no RPC was found for adding a local listener while running.

**Windows** (2026-11-07, Codex 0.163 and the Codex App 26.1027): `--listen unix://` serves an AF_UNIX socket file there too (`<CODEX_HOME>\app-server-control\app-server-control.sock`, its directory's access list this user's alone), and the node dials it like a Unix one.
With no mode bits, "private" is the owner and the access list: the socket and its directory must be this user's and grant nothing to anyone but this user, SYSTEM and Administrators (`codex_remote_windows.go`).
`codex app-server daemon start` refuses to run from npm's launcher ("this Windows launcher prevents background processes from outliving it"), so the shared daemon is not there for a plain `codex`.
The desktop App's app-server is stdio here as well; its windows talk over the App's own bus, the named pipe `\\.\pipe\codex-ipc` ([codex.md](codex.md), "Windows").
Which threads a stdio app-server or a CLI holds open comes from each process's handle table instead of `lsof` / `/proc` ([windows.md](../operations/windows.md), "Codex").

**Implementation when the current desktop uses stdio**: if supported by the desktop app, use an official shareable Unix/daemon channel on its next normal launch, letting desktop and node connect to the original server. The desktop must actually support those connection settings. Do not merely add `--listen unix://` to an old desktop launch command while it continues writing requests to stdin. If the desktop has no shared-connection configuration, add a cooperative channel in the desktop / launch integration. Plain TCP byte forwarding cannot replace multi-client JSON-RPC routing, one-time initialization, and response/event/approval distribution. Without that cooperation, Persona is explicitly read-only and must not claim "takeover".

## Threads, state, and send semantics

| API / mechanism | Confirmed behavior | main implementation evidence |
| --- | --- | --- |
| `thread/list` | Persisted summaries with empty turns; defaults filter provider, interactive source, and archived threads. Adds runtime state only for threads loaded in **this process**; not a machine-wide active Agent list. May reconcile metadata/read-repair, so not physically zero-write. | `app-server/src/request_processors/thread_processor.rs:2734–2901,5991–6058`; `rollout/src/recorder.rs:654–676` |
| `thread/loaded/list` | Paginated in-memory thread IDs from this process's ThreadManager; loaded includes idle, not just running; filters some internal/sample threads. Use only to establish runtime ownership for the current server, not cwd / recent files to guess owners. | `thread_processor.rs:3026–3074`; `core/src/thread_manager.rs:1855–1893` |
| `thread/read` | Neither loads nor subscribes; reads persisted records with this process's state overlaid. includeTurns history can flush/persist; ephemeral and unmaterialized threads have different restrictions. Interrupted in the response may only mean this server lacks the active runtime, not that the computer stopped. | `thread_processor.rs:3103–3327`; `thread_lifecycle.rs:1361–1374` |
| `thread/resume` | Warm resume usually reuses the runtime and subscribes, but can shutdown/rebuild if there are no subscribers, it is Idle/non-Running, it accepts direct input, and configuration differs. Cold resume creates a runtime/writer; its idle lifecycle can dispatch an existing queue automatically. Not a discovery API. | `thread_processor.rs:4335–4427,4687–4757`; `codex-rs/app-server/tests/suite/v2/thread_queue.rs:430` |
| Subscription and reconnect | No standalone thread/subscribe; start/resume/fork subscribe, unsubscribe removes the current connection. Warm resume sends an active-turn snapshot/response in listener order, followed by usage and pending requests; not arbitrary historical-event replay. Deltas have no global sequence or replay cursor; fill reconnect gaps using snapshots / history. | `app-server-protocol/src/protocol/common.rs:555–599`; `thread_lifecycle.rs:1019–1083,1144–1264`; `codex-rs/app-server-protocol/src/protocol/v2/item.rs:1542–1547` |
| `turn/start` | Mutations on one thread serialize by thread ID; different threads are independent. start calls `start_or_steer_turn`, potentially inserting into an active turn; successful start does not prove a new turn began. | `app-server/src/request_serialization.rs:25–29,71–74`; `codex-rs/app-server/src/request_processors/turn_processor.rs:690–725` |
| `turn/steer` | Requires the observed `expectedTurnId`; core checks the active turn and rejects review/compaction cases, etc. Do not turn arbitrary failures into start. On disconnect/timeout, verify receipt before resending. | `core/src/session/turn_input.rs:878–908` |
| `turn/interrupt` | app-server checks the turn ID, then submits core Op::Interrupt without a turn ID; not an end-to-end atomic CAS. Do not automatically clear every background command on the thread as a result. | `codex-rs/app-server/src/request_processors/turn_processor.rs:1470–1513` |
| `thread/queue/*` | Experimental. CRUD can affect persisted unloaded threads; queue/start consumes only after this process has loaded the thread and idle admission succeeds; busy preserves the queue. Dispatch uses an in-process per-thread Mutex, so do not claim crash-window exactly-once. | `thread_queue_processor.rs:186–228,253–310`; `ext/queue/src/service.rs:368–447` |
| `thread/archive` / `thread/unarchive` | `{threadId}`. Archive persists a loaded thread, shuts it down in this server (with the sub-Agent threads it spawned), moves the rollouts to `archived_sessions` under the cross-process writer lock (fails while another process writes the thread) and notifies `thread/archived`; unarchive moves them back and answers the thread. Neither is idempotent: archiving an archived thread is `-32600 "no rollout found"`, restoring a restored one `-32600 "no archived rollout found"`. An archived thread can't be resumed. Persona uses them for its native archive (`docs/integrations/codex.md`, "Archive") | `thread_processor.rs` `thread_archive_response` / `thread_unarchive_response`, `prepare_thread_for_removal`; `thread-store/src/local/archive_thread.rs` |
| Sub-Agents | Multiple threads within one server; parentThreadId/sessionId (tree) differ from forkedFromId (copy). Some V2 children in main are parent-managed and cannot receive direct turn input; capabilities must be version-specific. | `core/src/thread_manager.rs:520–539`; `codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs:205–294`; `thread_processor.rs:4092–4142` |

Real-time integration must use **warm resume on the verified original owner, without overrides such as model/cwd/sandbox/developerInstructions**. Recognize that "adding a subscription" is not absolutely side-effect-free; never batch-resume every thread during initial directory discovery. Operating on unloaded history requires explicit authorization to continue/load it. Do not secretly start another server to continue a user's active thread.

## Multiple servers sharing a home: locks are not a takeover protocol

main's `codex-rs/rollout/src/writer_lock.rs:19–87` explicitly calls `File::try_lock()` on `<CODEX_HOME>/thread-writer-locks/<threadId>.lock`, retaining the guard until writing ends; creation and recovery in `thread-store/src/local/live_writer.rs:56–81` acquire it. This is a cross-process writer lock, not just a ThreadManager Mutex, a SQLite lock, or an automatically expiring TTL lease.

Multiple app-servers can therefore share a home while managing different threads, but two processes cannot hold the same thread's writer simultaneously. `codex-rs/app-server/tests/suite/v2/thread_resume.rs:321–326` specifically tests that resume in a second process fails while another has an active writer, succeeding only after the original shuts down. The lock exposes no owner socket. Do not use it to discover routing, delete it, or treat changing home / renaming a thread as a bypass.

Different homes do not share the lock namespace. Path aliases, external rollouts, and non-LocalThreadStore behavior were not tested here. Persona still needs its own owner-conflict detection and fail-closed behavior; upstream locks are not an all-purpose safety net.

## Approvals, questions, and cross-client resolution

- The server sends requests to a snapshot of current thread subscribers (`app-server/src/request_processors/thread_lifecycle.rs:563–592`). Numeric request IDs come from an in-server incrementing counter and are reused after restart; client requests are distinguished by `(ConnectionId,RequestId)` (`outgoing_message.rs:57–62,277–280`).
- `outgoing_message.rs:657–685` consumes the callback under a lock: **the first client to send a response/error wins, not the first to approve**. HumanCommand and user verification have extra eligibility/owner conditions; do not apply those special rules to ordinary approvals. Subscription is not a read-only ACL.
- `serverRequest/resolved` contains only threadId/requestId, no approval result (`codex-rs/app-server-protocol/src/protocol/v2/notification.rs:71–76`). It is sent to current subscribers in listener order (`thread_lifecycle.rs:1314–1328`). The phone removes the card only; it must not fabricate Allow/Approve or elevate permissions from it.
- connect/initialize does not directly replay approvals. Resume replays requests **still pending**, keeping their original IDs (`thread_lifecycle.rs:1230–1259`, `outgoing_message.rs:525–560,688–701`). User verification is not replayed and is canceled if the original owner disconnects. Deduplicate recovered cards; old answered / expired cards must never become approvable again.
- A read-only observer must not automatically return errors or declines for unknown server requests: this can also consume the shared callback first, depriving the desktop of its chance to answer. Persona's existing single-owner unsupported-method error path (`assistant_codex_approve.go:35`) cannot be copied directly to shared connections. Dynamic tools and similar requests belong only to clients with real handlers; ordinary phone approvals respond only after an explicit user tap.
- `connection_cleanup.rs:19–39` cleans up connections asynchronously after closing admission. A non-stdio server does not exit immediately when its last client disconnects; a thread unloads only after a timeout with no subscribers and no activity (`thread_lifecycle.rs:56–60`). Closing stdio stdin exits the main loop (`app-server/src/lib.rs:1196–1218`). Disconnecting Persona must not automatically kill external processes, but cannot promise that the last observer leaving has no lifecycle effect.
- Slow WS clients are disconnected; a full stdio queue waits (`app-server/src/transport.rs:141–178,219–241`). Decode on node's callback thread, bound queues per thread, handle pagination/disk in the background, and never wait for phone approval in the receive path.

## What Persona already has, and what is missing

| Current state | Evidence | Integration implications |
| --- | --- | --- |
| One self-started app-server and one thread per assistant chat | `docs/assistant/assistant.md:218`; `codexConn` in `server/internal/node/assistant_codex.go:33` has only one `threadID` / `turnID` | Reuse connection wrappers, but decouple connection state from thread state |
| Numeric request IDs, pending table, result / error / timeout handling | `assistant_codex.go:89` | Pair request IDs within their own connections; no global numeric ID table |
| initialize, thread/start/resume, start/steer, approvals, streaming output | `assistant_codex_thread.go:16`, `:113`; `assistant_codex_notify.go:61` | Reuse after adding multi-thread routing; do not rewrite the entire protocol |
| initialize returns `codexHome`, currently neither saved nor used | `assistant_codex_thread.go:19`; returned rollout `path` temporarily stored in `n.paths` (`:66`) | One active connection reading another home does not solve directory discovery after restart |
| Codex file lookup only uses default `~/.codex` | `server/internal/node/codex.go:52` | Support multiple homes as with Claude multi-directory discovery; do not hardcode desktop app directory names |
| Offline listing scans Claude projects | `server/internal/node/sessions.go:559` | Reading a Codex rollout by known ID does not mean every desktop Codex history automatically appears in the list |
| Multiple node caches / control tables key by session ID; hub / app / DB use machine + session | `server/internal/node/node.go:43`; `server/internal/proto/session.go:41`; `server/internal/db/migrations/manager/0001_init.sql:4` | Identical IDs from multiple sources collide; source identity must be more than display text |
| Child-thread notifications are ignored | `assistant_codex_notify.go:92` | Do not put multiple threads on one connection and retain this filter |
| Approval ownership uses single-process `p.id`; ID hashes omit machine / connection generation | `assistant_codex_approve.go:80`, `:265` | Route by actual request/thread/turn; never send another Agent's request to the current assistant for approval |
| Any steer error falls back to start; interrupt cleans up background terminals | `assistant_codex_thread.go:138`, `:173` | Do not retain these defaults for external servers: fallback start only for errors proving the turn ended; separate stopping a turn from cleaning all background commands |

Reuse directory discovery, realpath deduplication, and persistence: `server/internal/node/claudedirs.go:176`, `server/internal/devices/claudedirs.go:19`. Extract only allowlisted environment fields (such as `CODEX_HOME`), never send a process's entire environment, tokens, or account configuration to hub or logs. Codex's `find_codex_home` canonicalizes explicit homes (`codex-rs/core/src/config/mod.rs:5239`); Persona should deduplicate canonical directories too.

## Protocol content must support more than text

`UserInput` in upstream main `codex-rs/app-server-protocol/src/protocol/v2/turn.rs:474` includes text (with text_elements), image, localImage, audio, localAudio, skill, and mention. Degrade input fields according to the actual version's capabilities. Upload phone files to the target node through the existing upload flow, then have that node supply accessible paths; phone paths are not computer paths.

ThreadItem in `codex-rs/app-server-protocol/src/protocol/v2/item.rs:238` has optional start / completion times; its payload (`:299`) includes user messages, Agent messages (phase), reasoning, commands, file changes, MCP, dynamic tools, collabAgentToolCall (sender / receiver thread IDs), subagent activity, search, images, and more. Do not overwrite a turn's actual answer with its last text item; hooks / background notifications are not new human input or approvals.

Preserve raw protocol records for unknown methods / items and label them as unsupported events in the UI. Never silently discard them or approve a request just because it is unknown. Dynamic tool `item/tool/call` **asks the client to execute a tool**, unlike approval. An observer cannot pretend to implement the desktop's dynamic tools.

## Integration call sequence (design example, not executed on real sessions)

1. Discover all permitted app-server processes on node: record only pid/start time, executable, `CODEX_HOME`, and configured/enabled endpoints. Canonicalize and deduplicate homes; do not infer process/endpoint ownership from "newest in the same directory". Persist known homes so history remains discoverable offline.
2. Connect independently to **existing, permitted** Unix/WS endpoints. Unix first completes a WebSocket handshake; use stdio only for servers Persona creates and owns. `initialize` once per connection (clientInfo / required and supported capabilities), then `initialized`; retain returned codexHome and version/capability information.
3. Call `thread/loaded/list` on each instance through all pages to build owner routing. Fetch each home's history separately with `thread/list`; handle default provider/source/archive filters and never mistake disk history for loaded runtimes. main's `sourceKinds` include cli/vscode/exec/appServer/subAgent, etc. (`codex-rs/app-server-protocol/src/protocol/v2/thread.rs:1552`); include sub-Agent sources and parent-child relationships.
4. First fetch summaries/status with `thread/read {threadId, includeTurns:false}`; paginate history only when opening a chat. For long chats, use version-supported turns/items pagination or local rollout windows, not a full read(includeTurns:true) for every Agent. Show metadata-only restrictions on fresh/ephemeral threads as visible states, not as nonexistence.
5. When the user chooses live integration/control, verify the thread is still in the original instance's loaded/list, then join that runtime with `thread/resume {threadId, excludeTurns:true}`, without configuration overrides. After the response/snapshot, receive events and still-pending approvals. If unloaded, downgrade to readable history; cold resume requires explicit continuation. Never silently switch to another server after failure.
6. Use `turn/start` when idle and `turn/steer` with observed expectedTurnId when running. Preserve complete inputs including text_elements, images/files/skills/mentions. The original app still owns the same runtime. Serialize sends per thread; distinguish not sent / delivery unknown / explicitly rejected, and never blindly retry timeouts.
7. Register incoming server requests as phone interactions keyed by `(instance, generation, requestId)`; return that request ID only when the user actually answers. Immediately remove cards on `serverRequest/resolved`, retaining history marked "ended elsewhere", without synthesizing approval. Observation-only mode does not answer dynamic-tool requests or send default errors that preempt desktop handling.
8. On disconnect, clear connection-local pending state/temporary deltas, without killing external processes or clearing user background tasks. Reconnect → initialize → verify owner → authorized warm resume → converge snapshots/history. Old requests expire when server/generation changes; integer IDs cannot be reused. Explain upstream unload behavior after the last subscriber leaves in the UI.

If Persona should **create its own** shared service, the official launch form can be `CODEX_HOME=<configured-directory> codex app-server --listen unix://`, or an explicitly private Unix socket. This differs from integrating a running desktop server and requires separate launch/continuation authorization. Running this command once does not constitute takeover of the original stdio process.

## Recommended data model

The following is a design to implement, not an existing API.

- `CodexHome`: machine + canonical home; stores historical sources / known directories. Files are the original records; do not write third-party SQLite databases.
- `CodexServer`: machine + configured / registered instance ID; includes owner (Persona-managed / external), home, transport, endpoint, capabilities, and connection state. PID + process start time identifies only the current runtime generation, not a permanently stable primary key.
- `CodexThread`: conversation identity = machine + home/source + original thread ID; runtime routing separately includes server + connection generation. Do not put PID in permanent conversation IDs.
- Same home/thread loaded in two servers: deduplicate shared history if appropriate, but preserve both runtime routes. Without a verified owner, never pick an arbitrary server automatically for writes. Identical thread IDs from different homes must not merge.
- Interaction identity: machine + server/generation + original server request ID (numbers and strings supported) + thread; include turn/item/approval IDs. Answers must go to the requesting connection. Old responses after disconnection must not go to a new connection after restart.
- UI identifies the source as desktop app / app-server, without fabricating a terminal pane. No terminal means no terminal takeover, keys, or pane close; available actions follow capabilities.
- Connections happen only on the owning node: Unix socket or loopback endpoint; the phone still operates through authenticated hub/node channels. Do not expose upstream ports to the public internet, auto-enable OpenAI remote-control for integration, or widen account / token permissions.
- Classify raw protocol records and logs by data type: authentication / tokens / user-verification proofs do not enter ordinary chats or logs. Commands and messages enter only the user's own session records, never public test fixtures. Unknown events can retain sanitized structure and references to local originals, not indiscriminately persist full JSON.

Prefer retaining existing machine/session routing on the server, generating a reversible mapping or persisted opaque session key for app-server sources and passing original thread/source fields separately. Migrate existing validation, caches, persistence, subscriptions, images, Agent tools, and client types together. Do not merely add a name to the list while old thread IDs still control a different chat with the same ID.

## Implementation and acceptance boundaries

1. **Discovery and read-only history**: discover/persist multiple homes and processes, scan offline directories, label sources correctly. External stdio without a connectable endpoint should still show history, explicitly "View only", without promising immediate sending.
2. **Connections and live routing**: protocol routing for multiple endpoints / threads on one machine, merge history and live output, reconnect recovery, separate observer and executor permissions. Start with synthetic fake servers. Real protocol tests use only owned test instances with independent fake HOME directories, never existing user desktop sessions.
3. **Sending and interactions**: version-supported inputs, start / steer / interrupt, and queues; questions / approvals / resolution by other clients. Multiple clients honor only real user actions; events and recovery must not manufacture approvals.
4. **Client acceptance**: source and capability display on iOS/macOS/Android/web; preserve streaming, refresh, reopen, images, and new results; light/dark screenshots. With no usable Mac, explicitly report unverified; Go passing does not replace UI acceptance.

The validation matrix must include at least: two servers with different homes; same home with different threads; ambiguity for same home/thread; PID / endpoint reuse; duplicate / opposing approvals from two clients on one thread; pending approvals restored after disconnect; late results from old connections; user interjections on the computer; phone resends / request timeouts; sub-Agent threads; incremental / rewritten / partial-line long rollouts; accidentally stopping work when the last observer leaves. Lists and rev pushes must remain lightweight, not reread every thread's full text once a second.

Implementation order and reuse points:

| Stage | Suggested location | Boundaries to handle together |
| --- | --- | --- |
| Multi-directory / multi-server discovery | Add a Codex registry / discovery in node, following `claudedirs.go`; configuration delivery / settings memory patterns in `hub/config.go`, `devices/claudedirs.go` | Server inventory independent of terminal engine choice; home may have no endpoint; insufficient permissions only mark inaccessible |
| Separate connections and threads | Extract transport, RPC pending, per-thread state, and approval routing from `node/assistant_codex.go`; owned assistants retain their existing entry point | Owned/external processes must not share stop / reap ownership; unconnected external stdio must not auto-start replacements |
| Listing and control | Add app-server sources in `node/sessions.go`; route by source before looking for a pane (`send` and `paneFor` in `server/internal/node/send.go`) | app-server sessions must not fall through to Claude resume; hide terminal actions without a pane |
| Persistence and protocol | `proto/session.go`, DB `node_sessions` / settings; hub HTTP/WS and Agent MCP tools | Same-source / cross-source identity, restart recovery, session routing, client compatibility; check migration numbers across lanes first |
| History and live output | Reuse `transcript/codex.go` and File pagination; provide equivalent protocol-history pagination / spans if needed | Original source/thread/turn/item IDs are merge keys; after resubscription, discard partial deltas whose completeness cannot be proven and fill gaps from snapshots, rather than continuing to concatenate blindly |
| Clients | `apple/Shared/Models.swift`, ChatStore, session menus, corresponding Android/web models | source, capabilities, clear read-only reasons; migrate old cache versions; automatic notifications must not overwrite new results |

Do not conflate CLI pseudo-terminals, standalone process/exec command endpoints, and thread Agent commands. `process/exec` executes commands on the computer; it is not a read-only way to attach history. Never call it without a user request.

## Upstream test evidence and outstanding verification

These tests were **read in source only, not executed**. They show upstream regression guarantees, not live validation of Persona integration.

| Test | main location | What it establishes |
| --- | --- | --- |
| `websocket_transport_routes_per_connection_handshake_and_responses` | `codex-rs/app-server/tests/suite/v2/connection_handling_websocket.rs:73` | Separate handshakes and responses for two connections using the same request ID |
| `control_socket_acceptor_upgrades_and_forwards_websocket_text_messages_and_pings` | `app-server-transport/src/transport/unix_socket_tests.rs:107` | WebSocket messages/pings over Unix, not custom newline TCP |
| `thread_loaded_list_paginates` | `app-server/tests/suite/v2/thread_loaded_list.rs:43` | Pagination of multiple loaded threads in one server |
| `thread_resume_rejects_legacy_writer_owned_by_another_process` / paginated counterpart | `app-server/tests/suite/v2/thread_resume.rs:321,326` | Writer exclusion across two processes sharing a home; cannot cold-resume to take over an active thread |
| `thread_resume_replays_pending_command_execution_request_approval` / file-change counterpart | `thread_resume.rs:5833,5970` | reconnect/resume replays pending approval requests with original IDs |
| `thread_resume_keeps_in_flight_turn_streaming` | `thread_resume.rs:5128` | Original turn continues streaming during warm rejoin |
| `cold_thread_resume_dispatches_a_persisted_queued_submission` | `app-server/tests/suite/v2/thread_queue.rs:430` | Resume may start existing work; read/list do not load threads |
| `queue_start_while_active_returns_busy_and_preserves_the_queue` | `thread_queue.rs:701` | Busy does not consume the persisted queue |
| `verification_cancels_when_its_owner_disconnects` | `app-server/src/user_verification_ownership_tests.rs:84` | User-verification ownership cannot be conflated with ordinary approval replay |

Not established: full Persona UI races when two subscribers send opposing decisions; exact replay of arbitrary old events; crash-window exactly-once for cross-process queues; writer locking for external rollouts / network filesystems; whether the current desktop frontend supports switching to a shared endpoint. Verify these with independent test instances, never by probing active user sessions.

## Research-phase validation status

- Cloned directly into `repos/codex`; `git check-ignore repos/codex` passed, and the upstream worktree remained clean.
- Checked main implementation and upstream regression source, and spot-checked current desktop tag transport, writer locks, warm resume, persisted queues, and approval replay; no claim of running upstream tests.
- Checked Persona's existing client / directory / routing source. This research is not a complete implementation; no running-server integration had been added yet.
- Upstream source was not built or run, no real multi-client write tests, no deployment, no TestFlight release.

## First implementation (2026-11-02)

Entry points are `node/codex_registry.go`, `codex_discovery.go`, and `codex_remote.go`. Reuses RPC envelopes/pending from `assistant_codex.go`, connecting external WebSockets through pluggable message writers and dispatch without putting external instances into assistant-process stop/reap.

- Automatically discovers multiple homes, current-user processes/allowlisted fields, and existing listen/control sockets; configuration is in `docs/operations/config.md`. Only open rollout filenames of selected processes identify a stdio source as still running, never recent files or cwd. These sessions (desktop-bundled stdio app-servers running outside the app sidebar; codex terminal UIs running directly in tmux, such as botmux's Codex; sidebar chats get sidebar-based status through app IPC in #380) derive status from their open rollout: a tail ending mid-turn is working (also if a turn exceeds node's 384 KB tail window and no turn event is present—`task_complete` is always within the last few KB when a turn ends); a tail ending in `task_complete` / `turn_aborted` is done (like idle loaded threads on the same app-server). Only threads with no turn yet or no provable work state are unknown, rather than incorrectly exited or permanently working. The app treats unknown as idle, once placing running desktop tasks under "Idle spaces" (#290). An open rollout not written for over a day (using the later of last-line time and file mtime) no longer counts as running and returns to offline: a bridge running for months kept stdio app-servers holding previously loaded threads open, leaving that history perpetually Unread (#2425). Read-only reasons identify the process holding the file: "app-server is running" or "Codex is running" (terminal UI).
- One resolver unifies terminal, history, and protocol entries for the same home/thread. The default legacy home keeps old IDs; non-default homes get `c0de-` opaque IDs. PID and connection generation do not change history identity. Known homes/named instances/per-thread join permissions live in hub settings; default scanning and history display do not depend on the terminal engine.
- One Unix-WebSocket or literal-loopback WS connection per instance, multiple threads per connection; complete loaded/list pagination establishes owners. Multiple owners for the same home/thread reject writes. Disconnect if initialize returns a mismatched home; initialization never overrides desktop skills/models/permissions.
- Ordinary viewing syncs local rollouts. Only **Join live** or the first send requests joining; no batch resume of all discovered threads, automatic cold restore, or substitute process. Sending supports text and uploaded local images; working turns steer with an observed turn ID, and arbitrary failures do not fall back to start. Stopping does not clear background terminals.
- External questions/command/file/MCP tool and permission approvals use existing phone cards without assistant auto-approval policy. Request IDs include source and generation, replay deduplicates, external resolved removes cards and records dismissed. Unknown dynamic tools/forms/user verification receive no automatic replies, and no claimed desktop handler support.
- Apple/Android/web parse optional source capabilities, show read-only reasons, and hide terminal/resume/close actions without capabilities. Sending and retry recheck capabilities. Even if old clients still show buttons, the server rejects read-only or incorrect routes.

Explicit limitations remain: parent-managed child threads do not receive direct turn input; queue management, dynamic-tool execution, and dedicated audio/skill/mention inputs are not exposed. Threads without local rollouts are guaranteed only visible metadata and new live output after joining, not complete paginated history. Unix long-path rendezvous and authenticated WS/remote endpoints are not adapted yet; loosening permissions or widening accounts is not a workaround. `loaded/list` and resume are not atomic, and joining is not strictly zero-side-effect subscription; unproven races and upstream exactly-once retain the limitations above.

Synthetic regressions cover identical IDs across homes, multiple owners, loaded pagination, Unix handshake, legacy ID preservation, restart mapping, read-only zero control, no resend after steer failure, unknown delivery, approval replay/external resolution with zero replies, multi-thread deltas, and payload wrapping. `PERSONA_CODEX_NODE_BINARY=<built-personad> go test ./internal/node -run CodexRemoteNodeProcess` also starts an actual node with independent HOME, driving a test WebSocket through its stdio RPC without touching user sessions.

Upstream source remains read-only; its Rust tests were not run, and no sends/approvals were tried on user desktop threads. If Apple/Android platforms are unavailable, report builds and light/dark screenshots as not run; Go/web tests do not replace them.

### Acceptance record for this round

Validated: full server tests against real test Postgres, repeated Codex/send-related race regressions, protocol driving of an actual node process with independent HOME, and all 16 web tests plus build. The new unknown-event regression covers the old parser overlay and genuinely fails on the old implementation. Synthetic data only; no changes to the shared worktree.

Apple attempted xcodebuild test with an independent source directory/simulator and exited 70: although simctl had iOS 26.0 and the SDK list showed 26.2, Xcode reported the iOS 26.2 platform uninstalled and the destination unavailable. Actual Swift tests executed: 0; no iOS/watchOS light/dark screenshots obtained. Android had no available SDK/Gradle, so no build or tests. No platform installation, signing changes, or TestFlight release.

Real-corpus validation attempted to select Codex snapshots from the user's independent corpus directory, but no eligible files were found. Corpus window/continuity was therefore not run this round; a skip is not a pass. Long history relies on existing fixture/window regressions. UI and real dual-client approval-race limitations remain.
