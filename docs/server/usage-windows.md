# Subscription usage windows (#2432)

Subscriptions like Codex and Claude Code have "5-hour" and "weekly" usage windows. This page explains how Persona collects, merges and draws them, and why some sources are not used.
The UI is at the bottom of the web sidebar (`docs/web/web.md` ("Subscription usage")), on the iOS Machines page and at the bottom of the Mac section column ("UI (iOS / Mac)" below). The data comes from the node on each computer, and from account-pool gateways the manager queries (such as relay gateways, see "Account-pool gateways" below, #2595).
Collection, merging and the gateways all belong to the **manager** (the one that connects computers, the assistant process `personad assistant`, see "Who owns this" below); the hub only forwards the API.

## Read local files only, never touch credentials

Principle: **local first, read-only, never read or send any token**. Sources that need the user's login credentials to query are never used, as spelled out below.

| Source | How it is obtained | Used? |
| --- | --- | --- |
| Codex rollout | Every `token_count` event in `~/.codex/sessions/…/rollout-*.jsonl` carries `rate_limits`: `primary` / `secondary` each have `used_percent`, `window_minutes`, `resets_at` (seconds), plus `plan_type`, `limit_id`. The node reads the tail (512 KB) of the most recent few rollouts and takes the last event whose `limit_id` is `codex` (or unset). Per-model quotas such as `premium` do not count | Yes |
| Claude Code status line | Claude Code puts `rate_limits` (`five_hour` / `seven_day`, each with `used_percentage`, `resets_at`) in the **JSON input of the status line command** (see its changelog), and writes it to neither the transcript nor any other file | Yes, but the user has to wire it up (next section) |
| Codex account usage API (ChatGPT's `wham/usage`) | Needs the login token in `~/.codex/auth.json` | **No**. Moshi uses it; Persona does not read `auth.json` |
| Claude account usage API (`/api/oauth/usage`) and the `anthropic-ratelimit-*` response headers | Needs Claude's OAuth token (Keychain / `.credentials.json`); the headers also need a request of your own | **No**, same reason |
| Others (Kimi, Grok, Antigravity, GLM / MiniMax API key plans) | All need an account API or a key | Not doing |

Moshi's approach (`moshi-hook usage`) is local files plus these account APIs, with each machine querying on its own and reporting. Persona takes the local-file half, aggregated by the manager instead, and skips the API half.

### Wiring in Claude Code's usage (optional)

Claude Code only exposes usage in the status line command's input, so Persona cannot read it out of thin air. **Persona never edits your `settings.json`**; if you want to see Claude's usage,
wrap your status line command so it drops `rate_limits` into the node's directory `~/.persona/usage/claude.json` (for multiple config directories use `claude-<name>.json`; each file counts as one source):

```sh
#!/bin/sh
# Status line command wrapper: save rate_limits for Persona, then pass the input unchanged to the original status line command
payload=$(cat)
dir="$HOME/.persona/usage"
mkdir -p "$dir" && chmod 700 "$dir"
printf '%s' "$payload" | jq -ec 'select(.rate_limits) | {rate_limits}' > "$dir/claude.json.tmp" 2>/dev/null \
  && mv "$dir/claude.json.tmp" "$dir/claude.json"
printf '%s' "$payload" | <your original status line command>
```

The file keeps only `rate_limits` (no session, directory or cost). The node uses its modification time as the sample time; `resets_at` is read as seconds, and milliseconds and RFC 3339 strings are accepted too.
The gateway's `rate_limits.spend_limit` (an amount of money) is not a window and is not read.

> Not verified: the status line field format comes from Claude Code's changelog ("5-hour and 7-day windows with `used_percentage` and `resets_at`"),
> and the unit tests use samples written from it; the unit of `resets_at` has not been checked against real status line input from a Claude session.

## Collection and merging

- **node** (`server/internal/node/usagewindows.go`): reads every 5 minutes (the first time 20 seconds after connecting to the hub, so the hub's `node.configure` delivers the switch first).
  Each source (one Codex home, one `claude*.json`) keeps its last sample, kept even when the source goes quiet, and dropped after 3 days. Windows whose `resets_at` has passed are dropped (those numbers belong to the previous period);
  a source whose windows have all passed is not listed. `usage.windows` returns this cache directly (reading once if it is missing or expired).
- **Off**: `machines[].usage_windows` in `hub.json` (`docs/operations/config.md`); with `false`, that computer's node reads no files and the manager does not ask it; unset means on.
  If the node is older than this version (no `usage.windows`), the manager treats it as having no data.
- **Manager** (`server/internal/devices/usagewindows.go`): on `GET /api/v1/usage/windows` it asks the online machines (in parallel, 8 seconds each; repeated requests within 30 seconds reuse the previous merged result),
  remembers each machine's last answer (in memory only, forgotten after 3 days or when the machine is removed from hub.json), then merges:
  - **Each account appears once**: two samples from the same source (Codex / Claude) are the same account if any one window has "the same length and the same reset time (to the second)". The reset time belongs to the account and reads the same on any computer, so no account id is needed and no credentials are touched.
  - For several samples of the same account, each window length takes the window from the **newest sample**, so a computer unused for a long time cannot bring back a 5-hour window that has already rolled over.
  - Samples older than 30 minutes are marked `stale` (sorted last, faded in the UI and labeled "out of date"); windows older than 3 days or past their reset time are dropped.
    Each account also says when it turns stale if nothing newer comes, `stale_at` (the newest sample's time plus its source's threshold: 30 minutes for a computer, 6 hours for a gateway). A client keeping an answer while refreshes fail judges it again with `stale || now >= stale_at`, so old numbers fade and read "out of date" however long ago it last heard from the hub, and no client hard-codes the thresholds (#1905).
  - **Every account has its own id** (clients use it as the row key, #1904): a digest of the longest plan window's length and reset time, stable within a week. For an account an account-pool gateway reports, its gateway account id goes into the digest too (two accounts of one gateway are never one, even with the same reset time); for an account whose longest window has no reset time, where it was read does (the computer and its place in that computer's answer, or the gateway account). Neither is sent out, only the digest. Two accounts that still share one (only per-model windows, resetting together) get `-2`, `-3`… in the order of where they were read.
  - Accounts with the same name are numbered by id ("Codex Pro", "Codex Pro 2"), so the numbering does not depend on which computer answered first.
  - Sorted by the fullest window, highest first.

API (read-only, not usable with extension tokens, `testdata/route_scopes.txt`):

```json
{"accounts": [{
  "id": "codex-1a2b3c4d", "provider": "codex", "name": "Codex Pro", "plan": "pro",
  "sampled_at": "2026-11-05T09:08:06Z", "stale": false, "stale_at": "2026-11-05T09:38:06Z",
  "local": true, "machines": ["MacBook"],
  "windows": [{"label": "5h", "window_minutes": 300, "used_percent": 22.0, "resets_at": "2026-11-05T13:08:06Z"}]
}], "checked_at": "2026-11-05T09:08:20Z"}
```

`local` says the numbers were read on one of the computers, `machines` names those computers (their names in hub.json now, #1907), `sources` names the account-pool gateways that reported it (below).

With no data, `accounts` is `[]`; old hubs lack this route (404), and the web treats that as no data.

## Account-pool gateways (relay…, #2595)

An account-pool gateway maintains several Codex / Claude / other subscription accounts for you, and **already knows how much of each account's 5-hour and weekly quota is used** (for example, a relay stores the response headers that upstream returns with ordinary requests).
There is no need for every computer to query these numbers again: the manager asks the gateway, reads the numbers it has stored, and merges them into the sidebar. Accounts the computers cannot see (kept in the gateway, not logged in locally) show up too.

### Why it is a built-in source, not an extension

The principle in `docs/extensions/extensions.md` is "the open-source repository holds only generic capabilities and extension points; anything that only makes sense for one company or one service becomes an extension". The trade-offs here:

- **The existing `quota` extension point does not fit**: it answers "how much budget does this launcher key have left in the gateway", using the launcher's key and returning `quota.Report`; what we need here is the subscription windows of the **upstream accounts the gateway maintains**, using the gateway's admin key, a completely different shape.
- **A new extension point needs the whole set**: manifest fields, schema-generated types on four platforms, permissions, a process host, a package in the market. The gateway needs only "one address + one key, read every 5 minutes, map the numbers to windows", a few hundred lines.
- So: **a source is a very small concept** (`usage.sources[]` in the manager's `manager.json`, one `type` per source), with two built-in adapters (in the manager):
  - `generic`: **a generic format defined by Persona itself**; any account-pool gateway that implements this one GET can plug in (below).
  - `relay`: **the admin API of an account-pool relay gateway**, with two read-only endpoints ("What is read" below); any relay implementing them can connect, and the adapter just maps them to the same shape.
    Older files used the name of the first connected relay as the type; they remain readable (every type except `generic` is treated as `relay`), and editing the source in the admin UI writes `relay` next time.
- If a third or fourth gateway with a different shape really shows up, the adapters get pulled out into an extension point then (`usagesource.Fetch` is the single entry, so nothing else changes). **This is a trade-off, not dogma**: the core `relay` adapter is named for its API shape, not a specific service, and works out of the box without installing an extension.

### Configuration

The manager's `manager.json` (the built-in manager's `~/.persona/managers/assistant/manager.json`, `docs/operations/config.md` ("manager.json")); the key lives only here (0600), never in the API, logs or the bus. This section belongs to the manager: it reads and writes it beside the extension sections, under a lock on the file's directory (`managerconf.Edit`), so two writes never overwrite each other:


```json
"usage": {
  "poll_minutes": 5,
  "sources": [
    {"id": "src-1a2b3c4d", "name": "Relay", "type": "relay", "url": "https://gateway.example.com", "key": "<admin key>"},
    {"id": "src-5e6f7a8b", "name": "Team pool", "type": "generic", "url": "https://pool.example.com/persona/usage", "enabled": false}
  ]
}
```

| Key | Meaning |
| --- | --- |
| `usage.enabled` | `false` is the master switch: no gateway is queried any more (what computers read themselves is unaffected). Unset means on |
| `usage.poll_minutes` | How often each source is read, default 5, 1 to 1440 |
| `usage.sources[]` | At most 8. `id` is assigned by the manager (`src-…`, generated by the admin UI), `name` is the source name in the sidebar (at most 40 characters), `type` is `relay` / `generic`, `url` is an http(s) address (no username or password in it), `key` is sent as `Authorization: Bearer`, `enabled: false` disables this one |

The admin UI (`/admin/usage`, `docs/operations/admin.md`) adds, edits, deletes, toggles and has "Test connection"; changes take effect immediately with no restart. If you edit `manager.json` by hand, the manager adopts the version on disk the next time this page is opened.

### What is read and how it maps

**Read-only, never touching upstream**:

- `relay`: `GET /api/admin/accounts?view=compact&limit=500` (the account list: `id`, `name`, `provider`, `status`, `schedulable`, `cooldown_until`, `last_error`, `quota_summary`, **no email, credentials or proxy address**;
  when the answer carries `next_after_id` / `next_after_priority`, the next page is read with `after_id` / `after_priority`, up to 2000 accounts; older gateways that ignore `view` / `limit` and return the whole list or a bare array are accepted too, taking only these fields),
  then `POST /api/admin/accounts/usage {"ids": […], "refresh": false}` for accounts that are not disabled (at most 100 per call, 500 accounts in total). One read round yields both the sidebar samples and the "gateway's account list" below; the list is not read separately. `refresh` is hard-coded to `false`: the gateway only reads its database and never asks upstream
  (`refresh=true` would make the gateway probe upstream from its single egress address, using up the whole pool's probe quota). No other route is called, let alone any write route.
- **Prefer a group key**: relay group admin keys (for example `onboarder` / `operator` groups) only see their group's accounts (including `/accounts/usage`, see that relay's access-control docs) and cannot read other groups; far narrower than a deployment key.
  If an account is moved to another group / deleted between the two calls and the usage endpoint returns 404, this round fails (the UI says "This address is not the gateway's management API, or the gateway is too old" and the next round retries).
- Timeouts (15 seconds per request, 45 seconds per source per round), answers capped at 8 MB, no redirects followed (the key is never carried to another address), errors keep only a code, never the gateway's text, which also never goes into logs (the log writes one line `usage source <id>: <code>` only when the code changes).

Mapping (`server/internal/usagesource`):

| Gateway | Sidebar |
| --- | --- |
| `provider` (`codex`, `claude`, `kimi`…) | The same; `codex` / `claude` can merge with the same account read on a computer |
| Account `name` | Account name (a name someone gave it in the gateway, easier to recognize than "Codex Pro" from a computer; the gateway's name wins when merging) |
| `plan_display_name` (or `plan` if missing) | Plan, written next to the name (not repeated if the name already contains it) |
| Window `kind` | Window length: `five_hour` / `5h` → 5h, `weekly` / `7d` → 7d, `hourly`, `daily`, `monthly` (30 days), `<n>m|h|d|w`; kinds like `weekly_fable`, `gemini_argon_weekly` that use the length as one of their words are recognized too. **Kinds with no readable length (`credits`, `blocked`…) are not time windows and are not drawn** |
| `scope: "model"` / `scoped_model` | Per-model quota: the model name is shown next to the window of the same length ("7d Fable"); it **does not affect account sorting and is not used to identify the same account** (one model being used up does not mean the account is) |
| `used_ratio` (0–1) | Percent used, rounded to 1e-9 so 0.29 reads as exactly 29 (without `used_ratio` but with `remaining` and `limit`, computed from those; windows with neither are not drawn) |
| `resets_at` | Reset time |
| Window `checked_at` (the newest, or the entry's if missing) | Sample time |
| `unsupported`, an account with no drawable window, `status: disabled` | Not listed |
| `stale`, `error` | Marked out of date; accounts with `error` show "The gateway couldn't read this account just now; these are the numbers it had stored" in the popover (the error text is not shown) |

Relay numbers come from response headers returned with ordinary requests, so **they stop updating while an account sits idle**, and its samples age more slowly than a computer's: they are marked out of date only after 6 hours (30 minutes for computer samples), and the sample time shown in the UI is the numbers' real age.
The values of each `kind` have not been checked against a real relay: the mapping table follows the first connected relay's public window names, and unit tests use samples written from its JSON fields (see "Limitations").

### Merging with what computers read

The manager puts the gateway's accounts together with each computer's samples and merges them by the existing rule ("Collection and merging" above): **same source (provider), same length, and the same reset time to the second** means the same account, with no account id needed.
A few additions:

- Two accounts in the same gateway (different `id`) are **never merged**, even if their reset times happen to match; the same upstream account seen by two gateways (or a gateway and a computer) is merged.
- A merged account takes the newest sample's numbers and lists its sources: the computers that read it, by name (`machines`), and the gateway's name; the line under the popover title reads "MacBook · Relay"; with only one source, just that one. Never a word like "this computer": the clients run on phones and other computers, where it would point at the wrong device (#1907). Against an older hub that only sends `local`, every client says "Computer" (zh-Hans 电脑, ja コンピューター) instead.
- When a gateway's **read fails, its last answer is kept and marked out of date**, so accounts do not disappear; dropped after 3 days. `problem` says why, from the source's error code, and the popover words it (the admin page's "Usage sources" gives the exact reason):
  - `rejected` (`unauthorized`, `forbidden`: the gateway turned the key away): "The gateway turned the key away; update it in Admin → Usage sources. These are its last numbers";
  - `unreachable` (`unreachable`, `timeout`): "The gateway can't be reached; these are its last numbers";
  - `failed` (any other code: `not_found`, `server_error`, `bad_response`, `too_large`, `redirect`, `bad_config`): "Couldn't read the gateway; these are its last numbers".

  The source failing replaces a relay's own per-account `error` (it is the newer news). Clients show no text for a value they don't know, but still fade the account.
- Sorting is unchanged: by the fullest of the account's plan windows, highest first, out-of-date ones last. With no data at all (and no sources), the whole block is not drawn.

### The gateway's account list (#4576)

The sidebar's usage bars only draw accounts with time windows; extensions (for example, an account page in a relay usage extension's sidebar) want **every account in the gateway**: disabled, quarantined, needing re-login, Kiro-credits-only, all of them.
The same read round (above) keeps a list on the side, stored in the manager's memory; extensions read it with the hub method `usage.gateway_accounts {}` (permission `usage_gateways`, `docs/extensions/extensions.md` ("Usage")). It only returns what was last read and **never queries the gateway for it**.

```json
{"sources": [{"id": "src-…", "name": "Relay", "type": "relay", "checked_at": "…", "error": "", "listed": 71, "complete": true}],
 "accounts": [{"source": "src-…", "account_id": "<gateway's id>", "name": "", "provider": "kiro", "plan": "free", "plan_raw": "", "plan_display_name": "",
   "status": "active", "schedulable": true, "reason": "", "cooldown_until": null,
   "windows": [{"kind": "monthly", "scope": "account", "scoped_model": "", "used_ratio": 0.18, "limit": 50, "remaining": 41, "resets_at": "…"}],
   "checked_at": "…", "cached": true, "stale": false}]}
```

- Only enabled sources of type `relay` are listed; sources not read yet (just added, or the hub just restarted or switched and memory is empty) are listed too, with `checked_at` `null`, no accounts and `error` `pending`: that means "not known yet", not "the gateway has no accounts", and consumers should treat it as no data (the usage extension then falls back to reading each launcher). With no sources it returns `{"sources": [], "accounts": []}`.
- `status` is the relay's as-is (`active`, `cooldown`, `exhausted`, `refresh_required`, `quarantined`, `expired`, `disabled`); when the gateway does not say, `schedulable` is treated as `true`.
- `reason` is the part of `last_error` before the first `:`, kept **only when it is a short word matching `^[a-z_]{1,32}$`** (`quarantine`, `sign_in_required`, cooldown reasons); the text after it may be upstream's own words (addresses, messages containing accounts) and is neither forwarded nor logged.
- **Windows are passed through as-is, whatever the `kind`**: `five_hour`, `weekly`, `monthly`, `kiro_bonus`, `kiro_free_trial`, `kiro_overage` (no numbers when uncapped), per-model windows (`scope: "model"`)… Missing numbers are `null`, never 0.
- **Disabled accounts are included too**: the usage endpoint does not read them, so their windows come from `quota_summary` in the account list (`kind`, `scoped_model`, `used_ratio`, `resets_at`), marked `stale: true`. Accounts beyond 500 that the usage endpoint did not read are handled the same way.
- `error` is only a `usagesource` error code; when the last read failed, the list is kept with every account marked `stale`. When a source changes its address or key, the list is cleared until the next round.
- `listed` is the number of distinct account ids read this round (an account appearing on two pages counts once), matching the number of this source's entries in `accounts`.
- At most 2000 accounts; when the limit is hit, the gateway returns a page cursor already used, or a page after the first fails, `complete` is `false` (a failure on the first page fails the whole round).
- The sidebar samples (`GET /api/v1/usage/windows`) are as before: disabled accounts and those with no drawable window are still not listed, and length-less kinds like `kiro_bonus` are still not drawn.

### Generic format (`type: "generic"`)

Any account-pool gateway that wants to feed usage into the sidebar implements one GET: the manager reads this full address with `Authorization: Bearer <key>` (omitted if no key is set) and expects JSON back (the same as the answer of `GET /api/v1/usage/windows`, so one Persona hub can feed another):

```json
{"accounts": [{
  "id": "acct-1",
  "provider": "codex",
  "name": "Team seat 3", "plan": "Pro",
  "sampled_at": "2026-11-05T09:08:06Z",
  "windows": [{"label": "5h", "window_minutes": 300, "used_percent": 22.0, "resets_at": "2026-11-05T13:08:06Z", "model": ""}]
}]}
```

`id` is optional (a stable identifier for the account in the gateway, used to guarantee that two accounts are never merged); `label` (`5h`, `7d`, `45m`) is used if its length can be read, otherwise `window_minutes`; `used_percent` is 0–100, kept as sent and clamped when out of range;
`sampled_at` defaults to the time of reading; each source has at most 500 accounts and each account at most 32 windows; accounts with an empty `provider` are dropped.

### Scheduler capacity view

Besides the sidebar, the same in-memory data feeds Agent scheduling: `devices.CapacitySnapshot` builds a `proto.CapacitySnapshot` of one `ProviderCapacity` per upstream provider
(known account count, schedulable count, earliest recovery, all-stale flag) from the gateway inventory and the merged windows. A gateway account counts once, from the inventory
(it is authoritative for the accounts it lists; a sidebar account with a gateway source is skipped); accounts only the computers saw fill the rest, an exhausted plan window making
one unavailable until its reset. It is read-only like everything here — the call performs no gateway or machine read beyond the windows' own short poll cache — and carries no
credential or gateway-side account id. How the launcher picker consumes it is in `docs/server/launcher-health.md` ("Capacity-aware notes").


## Who owns this (2026-11-05)

User decision: the hub is only a communication center, and computers belong to the manager (`docs/server/machines.md` ("Who connects to computers")), so subscription usage moved to the manager as-is, with no design change; the routes, fields and errors the App sees are unchanged.

| Part | Code |
| --- | --- |
| Manager: asks each node for `usage.windows`, merges accounts, reads account-pool gateways (it holds the keys), keeps the gateway's account list (`devices.usage_gateway_accounts`; the extensions' `usage.gateway_accounts` is served by `extmgr/extension_usage.go` asking it in the same process), the API for the admin UI's "Subscription usage sources" page | `server/internal/devices/usagewindows.go`, `usagegateways.go`, `admin_usage.go` (moved from the hub as-is, receiver changed from `Hub` to `Devices`); edits to `manager.json` go through `managerconf.Edit` (`server/internal/managerconf`, under a lock on its directory) |
| Route tables shared by both sides | `server/internal/devicewire/` (`UsageRoutes`, `AdminUsageRoutes`), the admin UI marker `assistantwire.AdminHeader` |
| hub: forwarding | `server/internal/hub/devices_routes.go` (`usageRoutesRelayed`, `adminUsageRoutesRelayed`) |

- The hub forwards `GET /api/v1/usage/windows` as an `http.request`, like the manager's other routes (still not usable with extension tokens, `testdata/route_scopes.txt`).
- `/api/v1/admin/usage-sources…` is forwarded only from the hub's admin UI route group: the hub first checks the admin session (plus CSRF for writes), then adds `X-Persona-Admin: 1` (the header is always stripped if a client sends it); the manager returns 403 without this marker, and the App's API has none of these routes.
- The gateway's key exists only in `manager.json` (0600) and in the one request header sent to the gateway: the hub does not read this section, the key never crosses the bus, and it is in no answer or log. When the manager runs on another machine (`assistant.external`) it uses its `manager.json` there, like the extension sections.
- **When the manager is away**: these routes all return `503 assistant_offline` "助手暂时连不上，稍后再试" ("The assistant can't be reached right now; try again later") (every client keeps its last answer and ages it by `stale_at`, see "Collection and merging"; only a 404 from an older hub without the route counts as no data); once it is back, the first request asks each computer again, and the gateways finish their first round within seconds.

## UI (web)

- **Bottom of the sidebar**: one thin bar each for the two fullest accounts (name, percentage of the fullest window), warning color at ≥ 80%; out-of-date ones fade; the hover tooltip shows the source. **With no data the whole block is not drawn**, and the sidebar looks as before.
- **Opened**: one row per window per account: window length (5h / 7d), percent used, a bar, the reset countdown, a pace hint; the sample time ("12 seconds ago") sits right of the account title.
  The line under the account title gives the plan and sources ("MacBook · Relay"); per-model windows show the model name next to the length; when the gateway cannot read, a one-line explanation (see "Account-pool gateways"). Esc or clicking elsewhere closes it.
- **Reset countdown**: under 1 hour reads `Resets in 45m`; within today, the time `Resets at 13:05`; longer, `Resets in 2d 3h`.
- **Pace** (`pace` in `lib/usage-windows.ts`): the window length is read from the label (`5h`, `7d`, falling back to `window_minutes`); expected progress = elapsed time / window length.
  More than 5 percentage points above expected is "Ahead of pace", more than 5 below is "Behind pace", nothing in between; a tick on the bar marks the expected position.
  No judgment and no tick in the first 5% of a window (in the first few minutes, using even a little counts as "ahead").
- Refreshes once a minute; no requests while the page is not in the foreground. A failed refresh keeps the last answer (iOS / Mac and Android do the same), which turns out of date at its `stale_at`; only a 404 (an older hub) hides the block.

## UI (iOS / Mac, #2889)

Same rules as the web (pure functions in `apple/Shared/UsageWindows.swift`, unit tests `UsageWindowsTests` share the web's cases; views in `Persona/Views/Machines/UsageWindowsViews.swift`):

- **iOS**: on the Machines page (**Machines** in the drawer / Settings / Agents menu), a **Subscription Usage** section below the cards lists every window of every account directly (no hover on a phone, so no two layers of thin bar + popover).
  The sample time sits right of the account title, the next line gives the plan and sources, and there is a one-line explanation when the gateway cannot read; one row per window: length, used, ahead / behind pace, reset countdown, with a bar carrying the pace tick below. With no data the whole section is not drawn.
  It asks once a minute while the page and the App are in the foreground (`tabActiveTask`), and also on pull to refresh. DEBUG: `-tab dev -openMonitor all`.
- **Mac**: at the bottom of the section column, above the connection status, one thin bar each for the two fullest accounts (`MacUsageFooter`), warning color at ≥ 80%, out-of-date ones faded, hover tooltip shows the source; clicking opens the same detail popover (Esc or clicking elsewhere closes it).
  It asks once a minute while the App is in the foreground; the Machines page does not draw it again.
- Demo mode (`DemoHub`) has one Claude and one Codex account; the mock hub's `MOCK_USAGE=gateways` shows a mix of computer and gateway accounts.
- With multiple hub groups, each group is asked separately and the results are sorted together (the same account in two hub groups appears once in each; no merging across groups); old hubs (404) count as no data.

## Limitations

- Claude's usage requires the user to wire up the status line (above); otherwise only Codex shows.
- Codex numbers only update when Codex is used; if no one on the machine has used it, the numbers stay at the last value. The sample time in the UI shows how old they are.
- After the manager restarts (it restarts along with the hub), the last answers of machines that have gone offline are lost until they come back online and are asked again; the gateway's last answer is also in memory, so after a restart the sidebar lacks its accounts until the first round (within seconds) reads them.
- **The different window types have not been verified against a real relay** (no gateway is running on this dev machine): the adapter follows the first connected relay's public API documentation (account read views, access control);
  the tests use a fake relay HTTP service (account list, usage, errors, expiry, the group key's 404, an old gateway's bare array). Once it is really connected, use **Test connection** in the admin UI to check the account and window counts.
  On 2026-11-06 (#4576) the account list was checked read-only against a real relay: the paging parameters and the shapes of `quota_summary` and `last_error` match what is written here; all 71 accounts (including 5 disabled ones) were in the list, and Kiro's `monthly` / `kiro_bonus` came through as-is.
- The same Claude account on a computer (the status line's `resets_at`, seconds) and in a gateway (time from response headers or the usage endpoint, possibly with fractional seconds) merges into one only if the reset times land on the same second; one second apart, it shows as two accounts. Not checked against real data.
- Windows whose `kind` in the gateway has no readable length are not drawn (mapping table above); if a relay adds new `kind` words later (e.g. `three_hour`), add them to `usagesource.KindMinutes`.
