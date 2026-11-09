# Extension schemas

The JSON Schemas here are the one truth of the extension manifest, the process protocol, the extension points' calls
and the declarative UI (Views and their components) (#1849, phase 3 of #1843). Every language's types are generated
from them instead of being written once per platform:

| File | Contents |
| --- | --- |
| `manifest.schema.json` | The manifest `persona-extension.json` (called `persona-plugin.json` before the rename, same fields): the root, `engines`, `runtime`, `secrets`, `permissions`, `contributes` and each extension point's contributions |
| `protocol.schema.json` | The process protocol: one frame a line (`Frame`, `RPCError`), error codes, `initialize`'s params and answer, `ui.render` / `ui.action`'s params `UIParams`, the badge event, and the `UIContext` / `UIRequest` the apps send the hub to draw a slot and the `UIPlace` the hub lists |
| `stream.schema.json` | Native UI stream protocol 1 (#5956): explicit capability, canonical app WebSocket envelopes, owner-session lifecycle requests, complete keyed snapshots, atomic deltas and bounded interaction values; source runtime activation remains independently gated |
| `view.schema.json` | Views, sections, components (`Component`, told apart by `type`), actions, `ui.action`'s answer, and the `persona-card` in assistant answers (`PersonaCard`, folded into components in #1850) |
| `messaging.schema.json` | The `messaging` extension point (#2138): the manifest's sources (`MessagingItem`), conversations, messages and parts (`MessagingPart`, by `kind`), sends, composer modes, documents, posts, events and errors; see `docs/extensions/extension-messaging.md` |
| `connector.schema.json` | **Plugin-side.** The `connectors` extension point's calls (`docs/protocol/connector-protocol.md`): everything the hub asks a connector program (`connector.status`, `connector.inbox.collect`, `connector.thread.page`, `connector.archive.changes`, `connector.group.*`, `connector.login.step`…), what the program asks back (`connector.secrets.*`, the knowledge base's `connector.posts.*` and `connector.archive.chats`, `connector.credentials.gh_token`, and the hub's own `sent.record`, which puts a send of the connector's in the sent-as-user ledger), `connector.recall` (the hub asks a connector that declares `recall` to take back a message it sent, once the user agreed), the `connector.changed` event, the capabilities, states and error codes |
| `channel.schema.json` | **Plugin-side.** The `channel` extension point's calls (`docs/extensions/channels.md` "4. Contributing a channel"): `channel.receive` and `channel.status` from the program, `channel.send`, `channel.typing` and `channel.edit` from the hub, `cannot_send` |
| `kb.schema.json` | **Plugin-side.** The knowledge base an extension reads and writes directly (`docs/extensions/extensions.md` "Knowledge base RPC"): `kb.list`, `kb.get`, `kb.day`, `kb.search`, `kb.update`, `kb.owner`, and the events `event.kb.changed` / `event.kb.forgotten` |
| `assistant.schema.json` | **Plugin-side.** Reading the user's conversations with the assistant (`docs/extensions/extensions.md` "Assistant conversations"): `assistant.conversations`, `assistant.messages` with the permission `assistant.read` |
| `models.schema.json` | **Plugin-side.** Calling a model through the hub (`docs/extensions/extensions.md` "Model calls"): `model.complete` with the permission `models` |
| `todos.schema.json` | **Plugin-side.** The to-do events an extension subscribed to and was granted `todos.read` is told of (`docs/extensions/extensions.md` "Events"): `event.todo.changed`, `event.todo.decision` |
| `examples/` | Examples: `<definition>.<name>.json` must hold to the schema; those under `examples/invalid/` must not. The plugin-side schemas' are in `examples/plugin/` (and `examples/plugin/invalid/`) |

## Generating

```sh
python3 scripts/gen/extension_schema.py           # after changing a schema
python3 scripts/gen/extension_schema.py --check   # compare only; exit 1 on a difference (tests and CI)
python3 scripts/test/test_extension_schema.py     # --check, and the examples hold to the schema
(cd sdk/go && go test ./...)                      # the Go types read every example and write it back the same
```

| Language | File | Shape |
| --- | --- | --- |
| Go | `sdk/go/wire/wire_gen.go` (its own module `github.com/nwverse/persona/sdk/go`, without dependencies) | Structs; a union is **one flat struct** (every variant's fields together, all optional, `Type` says which), like the hub's `UIElem`; an enum is string constants and a list (`Tones`); `ComponentSince` is the UI version each component came with; `Calls` is the calls and events of the extension points (below) |
| TypeScript | `sdk/ts/src/wire.ts`; the web app has a copy without the plugin-side schemas, `web/src/lib/extension-wire.ts` | Interfaces; a union is the union of its variants (`Component = TextComponent \| …`); an enum is a union of literals plus `string & {}` (values from a newer hub still read); `CALLS` as in Go |
| Swift | `apple/Shared/Generated/ExtensionWire.swift` | Type names with an `Extension` prefix (`ExtensionView`), `nonisolated`, `Codable`, `Hashable`, `Sendable`; legacy models take zero/nil for missing or mistyped fields, while stream envelopes reject missing required and mistyped present fields; a union is an enum, an unknown or unreadable component is `.unknown(type:)`; free JSON is `JSONValue` |
| Kotlin | `android/core/contracts/src/main/kotlin/persona/core/contracts/generated/ExtensionWire.kt` | kotlinx.serialization data classes, also with the `Extension` prefix, every field with a default; a union is a sealed interface plus a serializer reading `type`, an unknown or unreadable one is `Unknown(type, raw)` and written back as it was. Unlike Swift, an object other than a component with a field of the wrong type does not read at all (the hub checks Views against the schema first, so what the apps get holds) |

The generator also copies the schemas as they are to `sdk/go/wire/schema/` (`go:embed` cannot reach outside the
module): `wire.Validate(<definition>, value)` checks against them. The hub checks extensions' Views with it
(`extension.CheckView`), the conformance tests check `initialize`'s answer and every connector call with it, and it
knows the same subset as the validator in `scripts/test/test_extension_schema.py`.
The View semantics both extensions and the assistant check (charts, tables, fields) are in `sdk/go/wire/viewcheck.go`.

## Native UI stream protocol 1

Stage 1 of #5956 defined the contracts for **server-only TSX execution, native client rendering and WebSocket
deltas**. Later stages add the immutable keyed-tree core, process page bridge and manager-owned lifecycle below.
Runtime activation still requires independent sandbox, resource-isolation and egress verification. Existing
process extensions and HTTP render/action/invalidation behavior remain compatible. The hub still advertises UI
version 6; generated client models and transport forwarding do not alone enable client or source-runtime support.

### Negotiation and messages

`UIContext.stream = {protocol: 1, components: [...]}` is an explicit opt-in separate from `ui_version`.
Missing `stream`, even with UI version 7, means snapshot-only. Future handlers must authorize the contribution and
fit the complete View to supported components and UI version. Widgets/system surfaces continue using snapshots.
`Section.key` is optional for legacy Views (`x-since: 7`); stream sections and components require bounded keys.

- `UIStreamRequest` has a required `request` discriminator, `protocol: 1` and `request_id`. Requests are `open`,
  `close`, `suspend`, `resume`, `event` and `snapshot`. Open carries extension/contribution, context and bounded
  opaque state. Close/suspend carry session/epoch; resume/snapshot also carry the displayed version. Event carries
  a typed `interaction`. Authentication supplies the principal and hub identity; JSON cannot claim an owner.
- `UIStreamSnapshot` is `{protocol, session, epoch, version, root_key, view}`. It precedes any delta and is sent again
  after resume, reconnect or recovery. Future WebSocket relaying must identify these payloads independently from
  other traffic; this stage does not register transport event names.
- `UIStreamDelta` is `{protocol, session, epoch, base_version, version, operations}`. Apply only to the matching
  session/epoch and exact base. Changed batches advance exactly one version. Empty batches may retain the version
  for unchanged non-event work, or advance exactly one as an accepted/ambiguous callback fence. Rotate the epoch
  before the positive JSON-safe version range `1..9007199254740991` is exhausted.
- `UIStreamEvent` is `{protocol, session, epoch, version, event_id, key, action, event, value}`. `action` is the opaque
  registered `Action.id` for this node/event slot; a node may expose several distinct actions. It grants no authority.
  `event` is `press`, `change` or `submit`; values are JSON scalars, scalar arrays or scalar maps, not arbitrary nested
  objects. Callbacks stay on the server. Never send execution code, functions, runtime configuration, credentials or
  secret input contents. `event_id` deduplicates accepted interactions before writes; `request_id` only correlates
  lifecycle requests.

`replace` addresses `key` and carries **exactly one** complete `view`, `section` or `item`. Missing fields clear the
old payload's fields; replacement is not a property merge. `insert` carries exactly one `section` or `item` plus
`parent` and optional `before`; omitted `before` appends. `remove` addresses a subtree by key, never the root.
`move` addresses a section/item subtree plus its destination parent/optional sibling. There are no array offsets,
DOM nodes, HTML, arbitrary component properties or executable callback payloads. Root children are Sections;
Section children are Components. Tiles retain the page key namespace but initially update only by replacing their
containing grid/carousel. Validation-only `UIStreamView`/`UIStreamSection`/`UIStreamItem` aliases constrain the
existing native vocabulary without generating a competing tree model.

### Bounds and later semantic gates

| Limit | Contract |
| --- | --- |
| Session/epoch/request/event IDs and node keys | 1–128 Unicode code points |
| Operations per delta | 0–256 |
| Capability component list | At most 64 unique tags, each 1–64 code points |
| Interaction value | At most one container level, 64 entries; map keys 1–128, strings at most 4096 code points; finite numbers within ±(2^53−1) |
| Opaque state | Schema: at most 16384 code points; transport/manager: at most 16 KiB UTF-8 |
| Frames | At most 1 MiB UTF-8; requests/interactions at most 64 KiB UTF-8, checked before JSON parsing |
| Materialized tree | At most 300 total logical nodes and depth 16; schema only bounds each sections/items array |
| Pending work | At most 256 pending operations and 64 events per session; overflow discards deltas and recovers a snapshot, never retries a write |
| Lifecycle defaults | At most 8 sessions per principal; suspended/disconnected state expires after 5 minutes with a sweep no later than 1 minute |

**JSON Schema does not enforce cross-field or topology semantics.** Later manager/reducer work must bound byte size,
depth, global nodes and queues; enforce extension/runtime-wide session totals (required before activation); authorize
owner, extension, contribution, active generation and node/callback; enforce all keys including tiles, uniqueness,
replacement key/kind equality, parent/sibling existence and kinds, no cycles/root removal/tile operations, version
ordering, capability support and complete View semantics. Stage and validate the entire resulting tree before an
atomic commit. Unknown operations and any invalid batch require a full snapshot, never partial application or
skipping. Generated Swift/Kotlin unknown variants are recovery/rejection signals, not permission to draw/apply them.
Schema validation and successful decoding are not authorization or proof of atomicity. Validate raw stream numbers
before float64/JavaScript rounding: near-limit fractions can otherwise become apparently safe integral versions.
Go's raw stream validator retains numeric literals; Python stream fixtures use Decimal. Already-decoded floating
values cannot recover discarded digits.

Suspend stops UI-only timers/polling and bounds/cancels pending UI work, independently of service work. Resume and
reconnect reauthorize and send a fresh full snapshot rather than replaying a stale chain. Close is idempotent for
already closed/unknown owned sessions, and releases effects, timers, callbacks and ephemeral state. Upgrade,
disable, revocation or runtime restart disposes the generation; the client reopens. Lifecycle code now lives in
`server/internal/extmgr/ui_page_*.go`, with thin connection forwarding in `server/internal/hub/ui_stream.go`.

`UIStreamSnapshot.keyed.json` → `UIStreamDelta.operations.json` → `UIStreamSnapshot.after-operations.json` is the
shared native operation trace (root metadata clearing, zero ratio, insert, move, remove, empty section replacement).
Stage 1 checks schemas and serialization, **not trace reduction**. `UIStreamDelta.empty.json` and event fixtures
cover empty batches, null/false/zero/empty strings and collections. Stream-bound Go wrappers preserve required
native component fields (`code.text`, empty table rows, file entries and comments), without changing the public
legacy Component encoder. Legacy `CheckView` re-encodes native Components and still rejects these fields as
missing; that remains a legacy codec limitation, not evidence that required-empty native values are semantically
invalid. General optional empty component properties still have their existing Go `omitempty` behavior.
The native keyed-tree core in `server/internal/extension/uistream` now retains raw JSON property presence and
applies the generated stream contracts before detached semantic decoding. Serialize its `Tree` / `Delta`, not
their generated DTO projections.
`wire.CheckViewJSON` checks the source shape and native semantics without re-encoding Components; legacy
`CheckView` and Component encoding remain unchanged. `examples/traces/UIStreamSnapshot.native-presence.json`
is shared lossless-core evidence, deliberately separate from the legacy DTO round-trip examples.

The core stages batches atomically, bounds the resulting serialized tree, and emits deterministic stable-key diffs.
`Diff` recommends snapshots when deltas are not smaller; it returns a nil delta with that recommendation when even
root replacement would exceed the frame byte budget. Nonempty deltas advance exactly one version to reject gaps;
empty deltas may retain it for unchanged non-event work or advance exactly one to fence accepted interactions.
The manager consumes a revision after every entered or ambiguous callback, so receipt eviction cannot repeat a
no-UI-change write. SDK native revisions also consume every invoked callback and remain independent of emitted
wire revisions: several native renders may coalesce into one wire delta.

### Canonical main-app WebSocket envelopes

`UIPageSnapshot = {generation, version, rootKey, view}` describes source-process renders. Its revision is independent
of the manager-owned wire revision. The manager validates original source bytes, keys, topology and semantics
before fitting; duplicate fields, invalid UTF-8 and lone surrogates cannot disappear in a DTO/fitting round trip.

`UIStreamClientEnvelope = {type: "extension_ui_stream", manager?, request: UIStreamRequest}`. The manager selector
only routes to an authenticated manager; it supplies no ownership. The hub adds its authenticated principal, hub
identity and random connection ID through `extwire`. Both feature `extension_ui_stream_v1` and `context.stream`
are required; no extra app socket is opened. Production stream capability advertisement remains gated by the
independent source-runtime acceptance lanes.

`UIStreamServerEnvelope` uses `type: "extension_ui_stream"` and discriminator `kind`: `snapshot`, `delta`,
`receipt`, `error`, `closed` or `suspended`. Payloads are under their matching names; lifecycle acknowledgments
carry `session`. Optional `request_id` correlates requests, and `manager` identifies the authenticated sender.
A receipt carries `session`, `epoch`, `event_id`, consumed `version`, `status` (`accepted` / `ambiguous` /
`rejected`) and an optional presentation-only `result`. `UIStreamActionResult` is a strict, bounded overlay of native
`ActionResult`: `toast`, `open_url`, extension-owned `push`, `pop`, and correlated `lines`. The complete result is at
most 64 KiB UTF-8 JSON, its push state at most 16 KiB. No result `view`, `items`, HTML, JavaScript, assistant-page
navigation, callback or approval fields are accepted; JSX state owns tree updates. Native push-ownership, URL and
saved-action/confirmation/write gates remain authoritative. Unsafe or malformed results consume the callback's
fence and return `ambiguous` without presentation effects.

The client must apply the fence delta (or recovery snapshot) before consuming `receipt.result`, waiting until its
matching session/epoch has reached `receipt.version`. Deduplicate presentation by `(session, epoch, event_id)`, not
`request_id`: accepted retries return the cached result and status with the new request correlation, never invoke
the callback or issue another delta. Do not repeat a toast, navigation or file-line insertion on that receipt retry.
A result grants no authorization and must not trigger code or another write. Preserve the event's local target when
correlating review/file-line responses. A saved native `Action.push` navigates directly in existing clients without
calling the extension; a callback's returned `result.push` is handled only after its accepted event. Serialize
canonical Tree/Delta/result bytes, not detached lossy generated DTOs. Frames are routed only to their exact
authenticated connection, never the global broadcast/replay log.

Manager sessions default to eight per principal, 64 per extension and 256 total, configurable downward through
manager-only `ui_page_sessions`. Suspended/disconnected sessions expire after five minutes and are swept at most
every minute. Resume/reconnect reauthorizes and queues a full snapshot before deltas. Legacy HTTP clients receive
full fitted snapshots backed by owner-bound server sessions, with opaque state/version checkpoints and no stream
or code; their UI work is suspended after each response. Their fitted HTTP `ActionResult` also carries validated
presentation results; an old checkpoint action retry is rejected as stale, so neither writes nor effects repeat.
HTTP UI versions below 3 omit push/pop and below 5 omit file lines. Widgets/system calls retain full snapshot behavior.

### Source module and client capability boundaries

`componentPage` supplies a renderer factory captured from the page's own SDK module. The trusted transport passes
host-issued invocation-ticket authorization and publication callbacks into that factory. A separately bundled
root must never be mounted by the transport adapter's different Renderer/hook context; sharing only element
symbols does not fix the hook stack. A missing factory fails closed. Snapshot-only widget rendering uses the same
factory and disposes it; full pages keep their own owner-bound sessions.

Current native component declarations are not proof of client implementation. `html` is never a stream/JSX
component. Unadvertised tags fail negotiation, including an explicitly empty tag list. Sections/items update by
stable key; grid/carousel tiles update only through the containing component. Stream result `items`/`view` are
intentionally unsupported: callbacks change JSX state, and file-line results use the correlated `lines` field.
Native Apple/client implementation and visual acceptance remain independent work. Service namespaces and
headless shortcuts keep working while UI sessions are suspended or disposed.

## Plugin-side schemas and calls

A schema file with `"x-side": "plugin"` (`connector.schema.json`, `channel.schema.json`, `kb.schema.json`) describes only what the hub and an
extension's program say to each other. Its types are generated into the SDKs (Go `sdk/go/wire`, TypeScript
`sdk/ts/src/wire.ts`) and not into the apps (Swift, Kotlin, the web copy), which never read these messages; a
definition the apps have may not refer to one of them (the generator stops). Its examples are in `examples/plugin/`,
apart from the top level the apps' own round-trip tests read.

Such a file names its calls and events, with the definitions they hold to:

```json
"x-methods": {"connector.status": {"from": "hub", "params": "ConnectorParams", "result": "ConnectorStatus"}},
"x-events": {"connector.changed": {"from": "program", "params": "ConnectorChangedEvent"}}
```

`from` is who sends it (`hub` or `program`); no `result` is an answer of any JSON (`connector.group.tool`), `Empty`
an answer of `{}`. The names are the wire's: with the namespace (`connector.status`), and an event the hub sends
as `event.<name>`. The generator writes them out as `wire.Calls` (Go) and `CALLS` (TypeScript):

- `wire.ValidateParams(name, raw)` and `wire.ValidateResult(name, raw)` hold a call's params and answer to them.
  A `null` is read as the hub's Go decoding reads it, by what the schema says is there: a null list or object is an
  empty one, a null of any JSON (`meta`) stays null, any other null is absent; an answer of `null` is `{}`. Go writes
  a nil slice, map or pointer as `null`, and both SDKs' programs are held to what the hub accepts, not more.
- A list whose empty value means something (`kb.update`'s `tags` clears them, `posts.put`'s `comments` says there are
  none) has `"x-go-pointer": true`: Go then writes it with `omitzero`, so an empty list is sent and only a nil one is
  left out.
- Params of a call in the connector namespace carry `contribution` (the connector's id; the hub adds it with
  `extension.Targeted`); results do not. Knowledge-base params are strict: no `contribution`, and an unknown field is
  `invalid_params`.
- The conformance checks hold every message both ways to them: `connector/conformance.Check` (Go connectors through
  `connector/serve`), `CheckProgram` (a running connector program of any language) and `channel/conformance.Check`.
- `server/internal/extension/conformance/plugin_schema_test.go` holds each definition a call names to the Go type the
  server puts on the wire, field by field (names, JSON kinds, a required field Go never leaves out), and every
  connector, channel and knowledge-base method the server has to an entry in the schemas; the server's wire fixtures
  (`server/internal/connector/testdata/wire/`) hold to them too. A change on either side fails until the other follows.

In the Go SDK, `ext.Methods{…}.For(<contributions>)` is a namespace's handler from typed methods
(`ext.Method(func(ctx, wire.ConnectorThreadParams) (wire.ConnectorThreadPage, error))`), and `ext.Hub` has the
program's calls (`LoadSecret` / `SaveSecret` / `DeleteSecret`, `ConnectorChanged`, `PutPosts`, `ArchivedChats`,
`ChannelReceive`, `ChannelStatus`, `KBList`, `KBGet`, `KBUpdate`, `KBOwner`…; `ext.CannotSend` is channel.send's
`cannot_send`). `exttest.Start` (`StartWith` to change what `initialize` sends) is a fake hub to call a program from its own tests
(`initialize` carries the manifest's contributions as active, its permissions as granted and the hub's
capabilities, extension point versions only when the test sets them; it refuses what the hub refuses: a connector or
channel the extension does not have, a knowledge-base call or gh token not granted, an unknown method of these
namespaces; an `Answer` replaces its own; secrets in memory, `Answer` for
the knowledge base and the rest, `Hear` for the hub's events, every message held to the schemas, a channel's
messages in `Received`); `exttest.CheckConnectors` makes the hub's read-only calls of each
connector the manifest declares, and `exttest.CheckChannels` holds each channel to the channel protocol.

## The subset

The generator understands only what is below; anything else stops it with an error rather than a guess:

- Each schema file has only `$defs` (the manifest's root also has a `$ref` to `Manifest`, for validation), plus the
  annotations `x-side`, `x-methods` and `x-events` above. **Definition names are global**; a reference to another file
  is `<file>.json#/$defs/<name>`.
- A definition is one of:
  - **A validation-only alias**: `$ref` to an existing named native type, optionally with validation overlays. The
    generator reuses that type and rejects alias cycles; no second model is generated.
  - **A struct**: `"type": "object"` + `properties` + `required`. An object `oneOf` may contain single-field
    `required` alternatives for exactly one payload; TS generates an exclusive union, other models rely on schema
    validation. Validation-only `allOf`, `anyOf` and `not` overlays constrain combinations such as source versus process runtimes in both canonical validators; they do not generate additional models. Known fields keep their types. An object that says
    `additionalProperties: true` or a sub-schema also keeps extra keys (see "Custom contributions" below); one that
    says nothing or `false` is generated as before.
  - **A string enum**: `"type": "string"` + `enum`. The field stays a string in every language (an unknown value
    still reads), with constants and a list besides.
  - **A named scalar**: `"type": "string"` (or integer / number / boolean) without `enum`, such as `ID` or `Host`:
    where it is referenced it is that scalar; `pattern` and the like only validate.
  - **A union told apart by a field**: `oneOf` of `$ref`s only, with `"x-discriminator": "type"`; each variant is a
    struct that has the field as a distinct `const` string.
  - **Free JSON**: `oneOf` with `"x-untagged": true` (such as `runtime.command`, a string or an object): generated
    as free JSON (Go `json.RawMessage`, TS `unknown`, Swift `JSONValue`, Kotlin `JsonElement`). With
    `"x-json-value": true`, TS instead generates the schema's scalar/array/map union (including `null`); the other
    languages retain their native JSON values and require schema validation for bounds.
  - `"x-preserve-required": true` retains required zero/empty fields in Go's flat unions and in Kotlin stream
    serializers, including empty native children under the production codec, without changing legacy encoding.
    Swift stream envelopes reject missing required or mistyped present fields instead of normalizing them.
    Union variants inherit it. `"x-unknown-policy": "recover" | "reject"` documents how generated client
    unknown variants must be handled; it is not a reducer. `const` fields retain literal types in TS.
- Field types: `string` (`format: date-time` is a time: Go `time.Time`, a string elsewhere), `integer`
  (`"x-go-type": "int64"` is 64 bits: Go `int64`, Swift `Int64`, Kotlin `Long`; `Int` on the watch is 32 bits and
  cannot hold a build number), `number`, `boolean`, `array` + `items`, an `object` **without** `properties` +
  `additionalProperties` (a map whose values are `additionalProperties`), `$ref`, or nothing (free JSON).
  A field that is an object with `properties` must be a definition of its own, referenced.
- Optional fields are those not in `required`. Go adds `omitempty` (`omitzero` for a time) and makes an object a
  pointer; a scalar whose absence means something other than its zero value gets `"x-go-pointer": true` (a
  progress's `ratio`, `kb.update`'s `relevance` and `pinned`).
- A component has `"x-since": <UI version>`: it generates tables like `ComponentSince`, by which the hub takes new
  components out for older apps (`Fit`).
- In one union, same-named fields of several variants become one field of Go's flat struct, so their types must be
  the same (enums count as strings); the generator stops otherwise: give the new field another name.
- The validation keywords (`pattern`, `minLength`, `maxLength`, `minimum`, `maximum`, `minItems`, `maxItems`,
  `uniqueItems`, `minProperties`, `maxProperties`, `propertyNames`, `const`, `enum`, `allOf`, `anyOf`, `not`) do not change what is generated; `sdk/go/wire`'s
  `Validate` and the validator in `scripts/test/test_extension_schema.py` both know them. A keyword neither knows
  fails `test_schema_keywords_are_known` (#4012); what an `x-` annotation holds is not checked as keywords.
  Rules a schema cannot say (at most 300 components a page, the same labels in every chart series, field names
  unique, `state` at most 16 KB) stay the hub's hand-written checks.
- A manifest example also has to pass the hub's `extension.Parse` (semantic checks beyond the schema, such as
  `contributes.events` needing `on_startup`): `server/internal/extension/manifest_examples_test.go` requires
  `examples/Manifest.*.json` and `sdk/templates/{go,ts}/persona-extension.json` (placeholders filled in) to install
  and `examples/invalid/Manifest.*.json` not to. A fixture only for type round trips
  (`Manifest.custom-contributions.json`) is listed apart in that test, with why the hub refuses it.

## Custom contributions (#2467)

`Contributes` explicitly allows `additionalProperties`, so the built-in `ui`, `providers` and the rest stay strongly
typed while custom `<extension id>.<extension point id>` keys are not lost in a JSON round trip through the official
types. The generator does the same for every other object that is explicitly open, without looking at whether its
name is `Contributes`.

- Go uses `AdditionalProperties map[string]json.RawMessage`, Swift `additionalProperties: [String: JSONValue]`,
  Kotlin `additionalProperties: Map<String, JsonElement>`. These are only where the code keeps them: encoded, the
  extra keys stay **at the object's own level**, not wrapped in an `additionalProperties`.
- TypeScript keeps the known properties' types and adds `[key: string]: unknown`. A caller reads a dynamic value by
  its own extension point's schema; it is not a built-in contribution.
- Known fields always win: an extra key named `ui`, `providers` and so on, even written into the map by hand, never
  replaces a typed field or brings back one that was left out. `UI` is not `ui`; the extra keys' case, underscores,
  dots and nested `config` / `state` / `plugin` / `extension` keys and values all stay as they are. Swift encodes
  open objects as dictionaries, untouched by a caller's snake_case strategy.
- This is no exemption from authorization or installation. A custom extension point is still checked by the hub
  against `exports`, dependencies, contribution ids and the exported schema; closed objects such as `permissions`
  are not opened.
- `examples/Manifest.custom-contributions.json` is the round-trip fixture of all four languages: built-in UI, several
  custom contribution points, nested JSON, keys differing only in case, a literal `additionalProperties`,
  `__proto__` and `constructor`. The last keys test JSON passing through; **they do not mean these install as
  extension points**. The Go / TS SDKs' manifest and initialize tests read this fixture too.

After changing the generator run `python3 scripts/test/test_extension_schema.py`, `python3 scripts/test/test_sdk_bundle.py`,
the Go SDK's tests, the TS SDK's `bun test`, and build the Kotlin contracts and the matching Swift tests.
`extension_schema.py` also rebuilds `sdk/go/bundle/sdk.tar.gz` deterministically; commit the generated types and the
bundle together, so a new extension does not get old types.

## Assistant cards (#2018)

They still use View and Component, adding `View.fallback` / `pages`, `Action.reply` / `open` / `pop`, `Push.page`
and the `html` component. `AssistantView` is a live card with an owner and a version; `ViewAction` is a stored
submission (pending / sent / failed). The full spec is in [Assistant rich UI](../../docs/assistant/assistant-views.md).

- `wire.ParseAssistantView` checks the raw JSON, takes the one-component shorthand with a fallback, and limits it to
  64 KB and a JSON depth; `wire.CheckAssistantView` then checks the shared component semantics and the assistant's
  rules for actions and pages.
- A root fallback is required; at most 8 named pages, and a detail page cannot nest pages. Every action id is unique
  in the card, `push.page` refers to a root page, and assistant forms cannot have `secret`.
- `wire.StripAssistantFields` is for extensions' Fit: it takes out the assistant's fields, actions and html, leaving
  extensions' existing id / push.ui / state behavior as it was.
- html's `x-since: 4` lets older renderers recognize it; this stage **does not raise extensions' ui_version (still
  3)** and does not run it; each platform takes it up later by capability and after a security review.
- `examples/assistant/*.json` is synthetic data the four platforms' screenshots and round-trip tests share: a
  decision, a comparison, several pages, a progress update, a form, a chart and HTML. A schema-valid card does not
  mean its HTML has passed the execution security review.

The generated files say "do not edit" at the top: do not change them by hand; change the schema and generate. Go's
output goes through `gofmt` (the generator needs it on PATH).

## Adding a component

1. Write a definition `XxxComponent` in `view.schema.json`: `"type": "object"`, `"x-since"` (the new UI version),
   `"type": {"const": "xxx"}` and its fields in `properties`, and `required`. A field with the name of another
   component's field must have the same type (Go is flat).
2. Add it to `Component`'s `oneOf`.
3. Put an example of it in one of the `View.*.json` in `examples/` (a test checks every component has one), and a
   counterexample in `examples/invalid/` for each rule it can break.
4. Run `python3 scripts/gen/extension_schema.py` and the four languages have it; then draw it in each renderer
   (each platform already skips components it does not know).

An action (`Action`) is an object; later page navigation (push / pop), new context (`UIContext`) and new slots
(`Placement` values) are fields or values added to existing definitions, which older apps ignore.
3b (#1850) was added this way: `markdown` / `table` / `empty` / `loading`, a row's icon / avatar / thumbnail, `key`,
a View's `search` / `filter`, a section's `more`, `Action.push`, `ActionResult.push` / `pop`, `InvalidateEvent`
(UI version 3, `docs/extensions/extensions.md` "UI version 3"). How `persona-card` becomes components, with its cases, is in
`sdk/go/wire/personacard.go` and `examples/persona-card/`.
UI version 6 (#5464) was added the same way: the `image` / `grid` / `carousel` components and fields such as
`row.control`, `row.actions` and `spark`. Its numeric fields live in sub-objects of their own (`RowControl`,
`ChartPoint`) rather than in `Component`'s flat struct; `Thumbnail.url` is optional now (exactly one of it and `ref`),
which Swift reads as `String?`. Its examples are `examples/View.v6-*.json`; the showcase's version 6 pages come from
the extensions repository (#5477), and until then `SHOWCASE_UI` in `test_extension_schema.py` stays at 5.

## Adding a call

1. Write its params' and answer's definitions in the extension point's schema file (plugin-side: `"x-side": "plugin"`),
   with `contribution` in a namespaced call's params.
2. Name it in the file's `x-methods` (or `x-events`): who sends it, the definitions.
3. Put an example of a new definition in `examples/plugin/`, run the generator, and map the definition to the
   server's Go type in `plugin_schema_test.go`'s `goTypes`.
