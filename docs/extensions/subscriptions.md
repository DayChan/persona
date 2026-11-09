# Source subscriptions

Source subscriptions let the manager pull new information from a connector's read-only tools into the Inbox or knowledge base. The design is tracked in #2036. The optional catalog protocol (#2037) is implemented; storage and running (#2038), destinations (#2039), model drafting (#2040), and client pages (#2041) are follow-up stages. A catalog alone does not turn on polling or store source content.

## Source catalog

A connector may declare `catalog`, without changing connector protocol version 2. It implements `connector.Catalog` in process, or answers `connector.catalog.list` and `connector.catalog.call` through the extension protocol. Schema and SDK types live in `schema/extension/connector.schema.json`, generated into the Go and TypeScript SDKs. `ext.Methods` accepts the generated request and result types like other connector methods.

`catalog.list {}` returns `{sources: [...]}`. Each source has a stable `id`, a display `name`, and `tools`. An id does not change when the name is renamed or translated. Optional fields are:

- `icon`: one of the declarative UI's `NavIcon` values.
- `service`: a `<kind>:<domain>` hint, such as `mail:example.com`, for recognizing a service already read natively. It is a hint, not an account identity or permission.
- `min_interval_seconds`: a positive minimum interval.
- `max_calls_per_hour`: a positive budget shared by all sources of this connector/account, not a separate allowance for each source.
- `recipes`: pull recipes using this source's tools.

Each tool has a short `name`, a `description`, an object `input_schema`, and `read_only: true`. Its optional `title` is for display. Short names are unique within the source; source ids are unique within the connector. Optional `annotations.read_only_hint` and `annotations.destructive_hint` must agree with the read-only declaration: an explicit false read-only hint or true destructive hint refuses the catalog.

`catalog.call {source, tool, args}` returns `{text}`. `args` is an object and defaults to `{}`. The source and tool must still appear in the current read-only catalog before dispatch. A missing or non-read-only tool is refused with `not_read_only`, without invoking it. Upstream `rate_limited` errors retain their `data.retry_after` in seconds, including fractional values. Programs written with the SDK must apply the same refusal in their handler; an `ext.Method` decodes its parameters and does not establish that an arbitrary tool is safe.

The Go SDK's `wire.CheckConnectorCatalogJSON` validates the original wire result, and `wire.CheckConnectorCatalog` validates typed results. Both check schema, identities, limits, read-only declarations, object schemas, and recipe shape/references. Server adapters and `exttest.CheckConnectors` use these shared checks. `connector/conformance.CheckProgram` reads the declared catalog and validates it, without executing catalog tools. A conformance check never polls a real source or invokes a write.

## Existing connectors

Without `catalog`, `connector.ReadCatalog` derives one source from the connector's declared `ReadTools`: its id and name are the connector's, and a tool's `<connector id>_` prefix is removed from the short name. `connector.CallCatalog` calls the existing read tool with its full name through `tools.call`. It never falls back to `actions.do`. A name present in both the read and write lists is refused.

An absent or undeclared catalog does not expose an implementation merely because it has a similarly named method. Existing Inbox/archive capabilities, feed switches, and `poll_seconds` keep their meaning. An older hub filters the unknown `catalog` capability at extension registration and keeps the connector's other capabilities; it can still use its ordinary tools.

## Recipe format

A recipe is a description of one pull, independent of its schedule and enabled destinations:

```json
{
  "mode": "items",
  "tool": "list",
  "args": {"since": "{{since}}"},
  "items_path": "$.items",
  "fields": {
    "id": "$.id",
    "thread": "$.thread",
    "title": "$.title",
    "sender": "$.sender",
    "time": "$.time",
    "body": "$.body",
    "link": "$.link",
    "due_date": "$.due_date"
  },
  "kind": "sub_notice",
  "group_by": "thread"
}
```

Required fields are `mode`, `tool`, `kind`, and `group_by`. The tool must be a short name in this source. `args` defaults to `{}`; it is an object, and strings can contain the runner's `{{since}}` and `{{since_date}}` variables. `items_path` defaults to `$`. Field paths are relative to one item; all are optional at the protocol layer. Supported path syntax is `$`, `.field` (letters/underscore followed by letters, digits, underscore or hyphen), and `[index]`. Wildcards, filters, recursive descent and expressions are not supported.

`mode` is `items` for arriving items or `snapshot` for state such as a balance. Text results can only be snapshots. `group_by` is `thread` or `source`. `kind` is one of `sub_mail`, `sub_message`, `sub_notice`, `sub_deadline`, `sub_event`, `sub_update`, or `sub_digest`.

The catalog stage checks parse, shape and references. The runner must additionally validate filled arguments against the tool schema, enforce the template-variable allowlist, dry-run field mapping, parse titles/times, and prove a repeated result yields nothing new. Missing ids fall back to content hashes and are marked as weak deduplication. A tool changing incompatibly, or repeated missing mapped fields, pauses the subscription for a new recipe.

## Follow-up behavior

Both destination switches start off. Enabling a destination requires a full-access device and confirmation of what is pulled, how often, where it is stored, and which model triages it. Model recipe drafting separately requires consent before a trimmed sample is sent to the Information processing model; the sample is never stored.

The manager will persist subscriptions, seen keys and pending items in its database, use leased runs, and commit seen keys/items/cursors together. Runs re-read a 60-minute overlap. Limits apply per connector: one call at a time, at least 20 seconds apart with jitter, a shared hourly cap, and shared pauses on rate limits or repeated failures. A signed-out or disabled connector pauses subscriptions without deleting them or counting as a source failure.

Inbox delivery reuses admission and triage with generic subscription kinds; knowledge-base delivery uses message-id receipts. Initial Inbox catch-up covers one day, combining more than three caught-up items into a digest while retaining separate future deadlines. Knowledge-base catch-up takes the first page. Snapshots append only when content changes. Destination storage, deletion, status pages and scheduling are not enabled by the catalog capability itself.
