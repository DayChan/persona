# daemon: a runtime the node connects to

A runtime (`docs/integrations/runtimes.md`) normally connects to the local node's `~/.persona/runtime.sock` itself. That socket is 0600, so the runtime must be on the same machine and run as the same system user as the node.

Some Agent runtimes cannot do that. They are resident services (daemons) with their own service port, for example:

- DSH running as a different system user;
- an Agent running in a container;
- a multi-user platform built on top of DSH;
- a service on another machine.

These services are connected the other way around: **the daemon opens a WebSocket endpoint, and the node connects to it according to its config**. Once connected, they speak the same runtime protocol as before (`docs/protocol/protocol.md` ("Runtime ↔ node")); the data model is unchanged, there is just one more way to connect. The hub and App need no changes, and Persona contains no code for any specific daemon: integrators implement this spec on their side.

Supported since 2026-11-02 (issue #325). The node-side code is in `server/internal/node/daemons.go`; JS daemons use the SDK's listen mode (`integrations/runtime-sdk`, see the "SDK" section below).

```
 App ── hub ── node ──┬── ~/.persona/runtime.sock ◄── runtimes on this machine, same user (DSH plugin, pi extension…)
                      └── ~/.persona/daemons.json ──► ws(s)://<daemon>/…  daemon (other user / container / machine)
```

## Spec (what a daemon implements)

1. **Endpoint**: one WebSocket address (`ws://` or `wss://`); the daemon picks the path.
2. **Handshake**:
   - The node's request carries `Authorization: Bearer <token>` and the subprotocol `persona-runtime.v1`.
   - Reply 401 when the token is wrong.
   - The daemon must answer `persona-runtime.v1` in `Sec-WebSocket-Protocol`; the node treats an endpoint that does not as not a daemon and does not connect.
   - No extensions (compression) are needed.
3. **Frames**: each WebSocket text message is one JSON object, matching one line on runtime.sock one to one:
   - request `{id, method, params}`;
   - response `{id, result}` or `{id, error: {message, code}}`;
   - event `{event, params}`.

   A single message is at most 64 MiB. A trailing newline is optional.
4. **Protocol**: exactly the same as on runtime.sock.
   - The daemon sends `runtime.hello` first, then pushes `runtime.sessions`, `session.items`, `session.delta`, and sends `interaction.create` for questions.
   - It answers the node's `session.*` requests as usual.
   - Capabilities are declared in `caps` as usual.
5. **Connection**:
   - After a disconnect the node reconnects, starting at 1 second and doubling each time, up to 1 minute; if a connection stayed up for more than 30 seconds before dropping, it starts from 1 second again.
   - The node sends a ping every 30 seconds and disconnects and reconnects if there is no pong within 15 seconds.
   - When a new connection arrives at the same endpoint, the daemon closes the old one and keeps the new one (this is what happens after a node restart or a network drop).
   - One endpoint serves only one node: for several Persona users, give each one a token (or an endpoint).
6. **Session ids and interaction ids are the daemon's own**:
   - The node adds a namespace when forwarding to the hub and strips it when forwarding back, so the daemon never sees prefixed ids.
   - Session ids follow the runtime rule (`[A-Za-z0-9][A-Za-z0-9_.-]`); with the prefix added, the total length must not exceed 128.

## Config (on the node's machine)

The config file is `daemons.json` next to `runtime.sock`, i.e. `~/.persona/daemons.json`, mode 0600:

```json
{
  "daemons": [
    {
      "id": "acme",
      "name": "Acme",
      "url": "wss://<daemon address>/persona/runtime",
      "token_file": "~/.config/acme/persona-token",
      "agent": "dsh",
      "launcher": "acme"
    }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `id` | Required; lowercase letters, digits, `_`, `-`, at most 32, no `.`. Used as this daemon's runtime id and `space_kind`, and as the session id prefix. You choose the name, so two daemons, or a daemon and a local runtime, that report the same name of their own never collide |
| `name` | The name shown in the App; its sessions' `agent_label` and cards use it. If empty, the name the daemon reports is used; if the daemon reports none either, `id` |
| `url` | `ws://` / `wss://` (`http://` / `https://` work the same). No username or password in the address |
| `token` / `token_file` | One or the other. `token_file` is reread on every connection, so changing the token needs no restart. If the file is readable by more than just you, or `token` is written inline, the log warns |
| `agent` | Optional: which Agent its sessions run; when set, it overrides what the daemon reports |
| `launcher` | Optional: when it can start sessions (`caps.start`), the name of this entry in the new-session form, default `id`. The launcher name the daemon reports is not used |
| `disabled` | When `true`, disabled but the entry is kept |

- After the file changes, the node rereads it within 10 seconds, no restart needed:
  - newly added daemons are connected;
  - changed ones are disconnected and reconnected;
  - removed ones are disconnected, their sessions disappear from the list, and their id is no longer reserved.
- Invalid entries are logged and skipped; the rest connect normally. When the same `id` appears twice, neither entry is connected.
- Who connects:
  - when there is a resident node (`personad node --attach`), it connects, and the daemon connection stays up while the hub reconnects;
  - without a resident node, each hub connection's own node connects.
- The node log records connects, disconnects and why a connection failed (token rejected, not a WebSocket, no subprotocol in the reply). Each reason is logged only once, and token values are never logged.

## Names do not collide

- A daemon's runtime id, display name and launcher name all come from the config; those fields reported in `runtime.hello` are rewritten.
- An id written in the config is reserved:
  - A runtime on the local socket that says hello with this id is rejected with error code `runtime_conflict`.
  - Conversely, when the id is already held by a connected local runtime, the daemon's hello is rejected; the node disconnects it and reconnects once that runtime is gone. The log states the reason.
  - Name clashes among runtimes on the local socket are decided by `instance` (`docs/protocol/protocol.md` ("Runtime ↔ node")): another process using the same id is rejected. A daemon's `instance` is set by the node (`daemon:<id>`); what the daemon reports does not count, so when it reconnects the old connection is closed instead of being treated as another process.
- Session ids:
  - The daemon's session `s1` is `<id>.s1` in the hub and App; `space_id` also gets the prefix and becomes `<id>:<original value>`.
  - Two daemons, or a daemon and the local machine, with the same session id never get mixed up, and the App never groups them into the same space.
- Interaction ids: the id the daemon gives is replaced by a stable value computed from the daemon's `id` and the original id. If it asks again with the same id after reconnecting, the question still attaches to the original card on the phone.

## Isolation: what a daemon can and cannot do

A daemon is not necessarily trustworthy, e.g. a multi-tenant platform, or something in a container that got prompt-injected. So the node adds a layer between it and the runtime:

| Can | Cannot |
| --- | --- |
| List, push and page through **its own** sessions | See or claim other sessions. When the node does not know who owns a session it asks every runtime; ids without this daemon's prefix are answered on the node side and never reach the daemon |
| Answer send message, interrupt, rename, close, start (as declared in `caps`) | Declare other capabilities: only start / send / interrupt / rename / close are allowed. `caps.assistant` is removed, so a daemon cannot run Persona's assistant (`docs/integrations/runtimes.md` ("Assistant")); new capabilities added to the protocol later must also be allowed individually in `daemonCaps` |
| Ask the phone (questions, plans, permissions); the card shows the name from the config | Use `mcp.*` (phone tools, Persona tools): `mcp.tools` returns an empty list, `mcp.call` returns `unsupported` |
| | Claim a local terminal pane through `pane_id`, or start a command in the local terminal engine through `resume_argv`: both fields are dropped, and `features` does not tell it `resume_argv` exists |
| | Reply `code: handover` to a send so the node hands the message to another runtime: the node turns it into `not_running` and sends nothing; `features` does not tell it `handover` exists either |

## SDK

`createRuntime` in `integrations/runtime-sdk` gains a `listen` option. The driver needs no changes; the same runtime works with both ways of connecting:

```js
import { createRuntime } from '<Persona>/integrations/runtime-sdk/index.js'

// The SDK opens its own endpoint (default 127.0.0.1, path /persona/runtime; port 0 picks any free port):
const rt = createRuntime(driver, { runtime: 'acme', name: 'Acme', listen: { port: 7311, token } })
const addr = await rt.start()

// Or mount it on the daemon's existing HTTP server:
const rt2 = createRuntime(driver, { runtime: 'acme', listen: true, token: () => readToken() })
server.on('upgrade', (req, socket, head) => { if (req.url === '/api/persona') rt2.accept(req, socket, head) })
```

- The handshake (constant-time token comparison, subprotocol check) and server-side WebSocket framing (fragmentation, ping, 64 MiB limit, rejecting unmasked frames) are all in `lib/listen.js`, with no other package dependencies.
- `token` can be a function, called once per connection.
- `runtime`, `name` and `launcher` in `runtime.hello` must still be filled in; the node replaces them with the config values.
- The DSH plugin gains a `listen` setting (`docs/integrations/dsh.md` ("Config")): when DSH runs as another user or in a container, it can be connected as a daemon without writing code.

Daemons not written in JS implement the "Spec" section above themselves: one WebSocket endpoint, one JSON object per message.

## Tests

- node unit tests (`go test ./internal/node -run TestDaemon`):
  - config validation, and no token in error messages;
  - names come from the config, session ids and `space_id` get the prefix, dangerous fields are dropped, only the specified capabilities are allowed;
  - pushes and questions carry the hub's ids;
  - unknown session ids are not sent to the daemon, `mcp.*` is rejected;
  - reserved ids, checked in both directions;
  - no connection on a wrong token or a missing subprotocol, connects after switching to a token file;
  - config changes take effect without a restart;
  - the daemon reconnects after a disconnect, and under a resident node the daemon connection stays up while the hub reconnects;
  - conversion between messages and lines.
- SDK unit tests (`cd integrations/runtime-sdk && npm test`, `test/listen.test.js`):
  - both listen mode and mounting on an existing HTTP server;
  - rejection on a wrong token, subprotocol or path, with no token in the logs;
  - a new connection replaces the old one;
  - fragmented messages, a ping in the middle of a message, unmasked frames.
- End to end:
  - `PERSONAD=<personad> node integrations/runtime-sdk/e2e/daemon.mjs`: uses a real `personad node`, the echo example as the daemon, and the script plays the hub. Covers: launcher name, start, session id prefix, row fields, sending (a resend goes in only once), permission cards, streamed output, paging, interrupt, rejected terminal calls, a local runtime grabbing a reserved id, the same runtime on the socket and as a daemon at once, a wrong token, config changes taking effect, reconnecting after a daemon restart, removal from the config, hub reconnect under a resident node.
  - `PERSONA_E2E_DAEMON=1 DSH_CHECKOUT=<built dsh source> PERSONAD=<personad> node integrations/dsh/e2e/run.mjs`: uses a real `dsh web` with the DSH plugin as the daemon (`listen` in its config), runs the full runtime acceptance (`conformance()`: text turn, approve, deny with a reason, question, streamed output, interrupt, continuous pushes), plus closing from the phone (archives in DSH) and "the daemon cannot get phone tools". Without `PERSONA_E2E_DAEMON=1`, the same script checks DSH connected the original socket way.

Once a new daemon is connected, write its own end-to-end test based on `e2e/daemon.mjs`: replace the part that starts the echo daemon with starting your own service.

## Limits

- Only WebSocket is supported, not raw TCP or HTTP over a unix socket; add them if needed. WebSocket already works over `wss://` and through reverse proxies that forward Upgrade.
- A daemon cannot be an assistant backend (`caps.assistant`) and has no `mcp.*`: the DSH plugin running as a daemon has no phone tools. Allow them individually once there is a real use case, with the trust boundary written down.
- For sessions a daemon lists as `offline`, the node does not resume them in the local terminal on its behalf (no `resume_argv`).
