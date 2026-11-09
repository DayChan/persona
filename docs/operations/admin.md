# hub admin console (`/admin`)

The hub's built-in web admin console: view the hub's status, add / remove computers, manage device credentials, install extensions, link hubs, change networking and access scope. The address is `<hub address>/admin`,
the API prefix is `/api/v1/admin/`, and it is **authenticated separately** from the app's API (admin password + session cookie; app tokens are not accepted).
The proposal is section 5 of [plan-hub-mesh.md](../archive/operations/plan-hub-mesh.md), overview #293, issue #296. One-click install (#287) and hub version updates (#284) will land here too.

Code: server `server/internal/hub/admin*.go` (extensions in `admin_extensions.go`), `server/internal/db/admin.go` (migration `0161_admin.sql`),
web `web/admin/index.html` + `web/src/admin/` (same project and same design tokens as the web app, with its own entry point).
The admin console is only at the hub's root path: the web app can be mounted under a reverse-proxy subpath ([web.md](../web/web.md) ("Subpath")), but the admin console does not read `X-Forwarded-Prefix` and cannot be opened through a subpath; access the hub's port directly.

## Access scope `admin.access`

`hub.json`:

```json
"admin": {
  "access": "lan",
  "cidrs": ["192.0.2.0/24"],
  "session_hours": 12
}
```

| `access` | Who can open it |
| --- | --- |
| `loopback` | Only the hub machine itself (`127.0.0.1` / `::1`). In Docker that is the container itself, i.e. only via `docker compose exec` |
| `lan` (**default**, also when omitted) | Local + private addresses on the same subnet as the hub + `security.trusted_cidrs` |
| `cidrs` | Local + the subnets listed in `admin.cidrs` |
| `any` | Everything `lan` lets in; in addition, any source **through the TLS port** (`public.tls_listen`). Without TLS it is effectively `lan` |

- **Out of scope, it does not exist**: page paths fall through to the normal web app (like any nonexistent path), and API paths fall through to the app's API (401 without a token, 404 with one),
  so the responses are identical to a hub that never had an admin console; a scanner cannot tell that one exists.
- **Same subnet**: private addresses (RFC 1918, ULA, link-local) within the address prefixes of each of the hub's network interfaces; IPv6 global addresses only count with a prefix of /64 or longer and not on a VPN interface
  (same as [network.md](network.md) ("Source classification"): a phone at home on global IPv6 counts as home too). The interface list refreshes every 30 seconds.
  Another private subnet (another floor or VLAN of the company network) does not count; to let it in, add it to `security.trusted_cidrs` or use `cidrs`.
  Addresses like Tailscale's `100.64.0.0/10` are not private addresses: to administer over a VPN, put the subnet in `cidrs` (or `trusted_cidrs`).
- **Docker bridge network**: inside a container you only see Docker's own subnet (`172.x`), not the LAN the NAS is on. So inside a container (with `/.dockerenv` or
  `/run/.containerenv`), `lan` widens to "any private address", **except the container's gateway** (`containerGateway` in `admin_gateway.go`: the next hop of the default route,
  plus the first address of each of the container's subnets). The reason is that connections arriving through port mapping come in two kinds:
  - On Linux, Docker by default forwards external IPv4 connections with iptables DNAT, so the container sees the real source address and computers on the LAN get in as usual;
  - Connections forwarded by Docker's userland proxy (docker-proxy) all show the gateway (`172.x.0.1`) as the source: connections from the host itself, all IPv6 ones when Docker has no IPv6 enabled
    (including those from the public internet), and **all** of them on Docker Desktop / OrbStack. The gateway address is private, so without excluding it public requests would count as "LAN",
    and someone on the internet could set the admin password on a freshly deployed hub before its owner does.
  So inside a container, requests from the gateway are always out of scope (the hub logs at most one line `admin: … is this container's gateway` every 10 minutes), unless the gateway is listed in
  `security.trusted_cidrs` or in `admin.cidrs` with `access: cidrs` (an explicit choice: it lets in every request coming through port mapping).
  In environments like Docker Desktop / OrbStack (trying a Docker deployment on a dev machine), where every connection comes from the gateway, the admin console cannot be opened under `lan`. Options:
  map the port only to `127.0.0.1` (`127.0.0.1:18660:18660`) and put the gateway in `PERSONA_ADMIN_CIDRS` (then only the local machine can come in through the mapping); or use host networking;
  or manage it from the command line on the hub machine with `docker compose exec hub personad hub admin …`. The entrypoint script prints a one-line hint about this when it detects something like Docker Desktop / OrbStack
  (`host.docker.internal` resolves inside the container).
  The hub's existing `sourceOf` (which decides about the master token and whether plain HTTP is allowed) has the same problem; a separate issue will fix it by reusing `containerGateway`.
- **Forwarding headers only tighten**: the hub never lets anyone in based on `X-Forwarded-For` (same as [network.md](network.md)); but when a request carries `X-Forwarded-For`, `X-Real-IP` or `Forwarded`,
  every address in it must also be in scope (unparseable ones count as out of scope). A request with only `Via`, `X-Forwarded-Proto` / `-Host` / `-Port` / `-Server` and no source address
  is also out of scope (it is a proxy, but we don't know whom it forwards for). So when the hub sits behind a reverse proxy that forwards public requests in, the admin console does not open to the internet just because the proxy's address
  is local. **A proxy that adds no headers cannot be caught** (nginx's default `proxy_pass` is one): a public-facing reverse proxy must either add `X-Forwarded-For`
  or block `/admin` and `/api/v1/admin/` outright. TCP forwarding and TLS passthrough (nginx `stream`) cannot add headers; enable `security.proxy_protocol`
  so the hub gets the real source ([network.md](network.md#proxy-protocol)).
- **Public sources always need TLS**: when `cidrs` lists public subnets, those sources may also only use the TLS port (plain HTTP gets 403 `tls_required`), so the password never travels in cleartext over the internet.
- **Widening only on the hub machine**: the web page can only narrow (`any` → `lan` / `cidrs` / `loopback`, `lan` → `loopback`, `cidrs` → smaller `cidrs` / `loopback`).
  To widen, use the command line `personad hub admin access <loopback|lan|cidrs|any> [--cidrs a,b]` or the deploy script ("Choosing at deploy time" below).
  If narrowing on the web page would leave you outside the new scope, it asks for confirmation first (the API returns 409 `would_lock_you_out` without `force`).
- **Environment variables win**: if `PERSONA_ADMIN_ACCESS` (and `PERSONA_ADMIN_CIDRS`, comma-separated) is set, it takes precedence and is not written back to hub.json (like the other `PERSONA_*`
  overrides, [docker.md](docker.md)); the web page then cannot change it and shows "Set by PERSONA_ADMIN_ACCESS at deploy time".
- Scope changes take effect immediately without a restart: changes from the web page apply directly; after the command line writes hub.json it notifies the running hub over loopback to reload (if the hub is not running, it applies on next start).

## Admin password and sessions

- **First-time setup**: with no password set yet, the first in-scope visit shows **Set an admin password**. Only sources that are in scope by address (let in by `lan` / `cidrs`, including local)
  can do the first-time setup: someone coming over TLS from the internet under `any` sees "open this page on the hub's local network, or run
  `personad hub admin reset-password` on the hub machine", so nobody can claim the password first. If two people set it at the same time, only the one written to the database first succeeds.
- **First-time setup from local loopback needs a one-time setup code**: when a reverse proxy on the same machine (nginx's default `proxy_pass` adds no forwarding headers) forwards public requests in,
  the hub also sees the source as `127.0.0.1`, indistinguishable from someone sitting at the hub machine. So when the source is loopback (judged by the connection's own address, not headers), setting the first password also requires
  a setup code: when the hub starts without a password, it logs a line `admin: first-run setup code XXXX-XXXX` (`journalctl --user -u personad`; on Docker
  `docker compose logs hub`), and after it expires, prints a new one when someone opens the setup page from loopback; you can also fetch one on the hub machine with `personad hub admin setup-code`
  (it asks the running hub over loopback + master token). The code is 8 characters (excluding 0 / O / 1 / I, about 40 bits of randomness), valid for 10 minutes, single use, and kept only in hub memory
  (invalid after a restart; a new one is printed at startup); wrong codes count toward login rate limiting. LAN sources still need no code; once a password is set, sign-in is unaffected.
- **LAN sources on new installs need the setup code too**: hub.json freshly written by `personad hub setup` has `admin.first_setup: "code"`, and then setting the first password from any source needs the code
  (on a shared LAN someone else could claim it first). The installer fetches a code over loopback + master token and prints it as `http://<LAN IP>:<port>/admin#setup=<setup code>`;
  `#setup=` is a fragment, so it is never sent to the server or logged; the setup page reads it, pre-fills it, and then removes it from the address bar. Old hub.json files lack this field, so behavior is unchanged.
- **Password**: at least 8 characters, at most 256 bytes; stored as an argon2id hash (m=19 MiB, t=2, p=1, PHC format) in Postgres's `admin_credentials`;
  the plaintext never touches disk or logs. At most 2 hashes are computed at once, the rest queue (so login requests cannot exhaust memory).
- **Session cookie**: `persona_admin`, a 32-byte random value of which only the SHA-256 is stored; `HttpOnly`, `SameSite=Strict`, `Path=/api/v1/admin/`,
  plus `Secure` over TLS. Lifetime is `admin.session_hours` (default 12 hours, counted from sign-in, not extended). Every request checks the database,
  so signing out, changing the password and `reset-password` invalidate other sessions immediately.
- **CSRF**: each session has a CSRF token, given to the web page by `GET /api/v1/admin/state`; every write (POST / PUT / DELETE) must carry
  an `X-Persona-CSRF` header matching the session's. In addition, writes must be `Content-Type: application/json`; when the browser sends `Sec-Fetch-Site` it must be
  `same-origin`; when it sends `Origin`, its host and port must match the request's `Host`. Sign-in and first-time setup check the last two as well (there is no session yet).
- **Rate limiting**: failed sign-ins (and setup codes) use the `security` thresholds (`auth_failures` failures / `auth_window_seconds` seconds, blocked for `auth_block_seconds` seconds),
  but in a separate table, and **LAN and local loopback count too** (a password is much easier to guess than a device token; loopback may be public requests forwarded by a reverse proxy on the same machine). If the hub machine's owner
  gets locked out for a while because of this, `personad hub admin …` still works. Sources outside the LAN additionally share a combined budget at ten times the threshold, against many addresses guessing together.
  Blocked requests get 429 with `Retry-After`. Admin console failures do not affect the app's auth counters, and vice versa.
- **Forgot the password**: run `personad hub admin reset-password` on the hub machine: with a terminal, enter the new password twice (not echoed); without one, it reads a line from stdin;
  `--clear` removes the password and returns to first-time setup. Both invalidate every signed-in session. In Docker: `docker compose exec hub personad hub admin reset-password`.

## Editing hub.json

Changes made in the admin console are written back to `~/.persona/hub.json`:

- **Atomic writes**: write a temp file in the same directory, `fsync`, `rename` over the original, always mode 0600; only one writer at a time (an in-process lock + flock on the `~/.persona` directory, so the command line and the hub exclude each other too).
- **Unknown fields and order are preserved**: only the keys being changed are touched; other keys (including ones this version doesn't know and ones added by other branches) are kept as is, in the same order, with indentation normalized to two spaces.
- **Environment variable values are not written back**: Docker's `PERSONA_PUBLIC_URL` and the like only override in memory; the admin console changes the values in the file.
- **When it takes effect**:

  | What changes | When it takes effect |
  | --- | --- |
  | hub name `name` | Immediately (the `hub_name` in hello / health is the new one on the next request) |
  | Admin console access scope `admin.access` / `admin.cidrs` | Immediately |
  | Adding a computer (`machines`, kind link / service) | Immediately (starts connecting once added) |
  | Removing a computer, changing a computer's settings (`machines`) | After a hub restart (a removed computer's Persona Link credentials and unused pairing codes are revoked at once) |
  | `public_url`, `public.tls_listen`, `public.advertise` | After a hub restart |

  For changes that need a restart, the API response carries `"restart": {"required": true, "reasons": ["…"]}`, and the top of the web page keeps showing "{count} changes take effect after a hub restart" until it restarts.
  The **Restart hub** button only appears when something supervises the hub's restarts (systemd's `INVOCATION_ID`, inside a container): the hub exits cleanly and systemd / Docker brings it back up.
  When nothing supervises it (run by hand in a terminal), it only tells you to restart.

## Pages

| Page | Path | Contents |
| --- | --- | --- |
| First-time setup / sign-in | `/admin` | Set a password if none is set, otherwise sign in with it |
| Overview | `/admin` | hub name (editable), version, uptime, listening / TLS / fingerprint, candidate addresses, computer and device counts, **Run checks** (the results of `personad hub doctor`); when there are no computers besides the local one, a prompt at the top to **Add computer** |
| Computers | `/admin/machines` | **Add computer** at the top, two kinds: **Computer connects** (kind link: fill in id / name, generates a persona-link pairing link, with install instructions for Mac / Linux / Windows) and **Hub connects** (kind service: paste the link printed by `persona-link pair --listen` on that computer; the address can be edited and a socks5 proxy filled in); ssh computers can no longer be added. Each computer's status (online / offline / error, connection method, node version) and **every route** (method, via which hub, usable or not, RTT, which one is in use, [mesh.md](../server/mesh.md) ("One computer, multiple routes")); "Re-pair" when a service computer's connector has been assigned to another hub; rename (the pencil by its name: written to hub.json and taken at once, without a restart or reconnect; empty is its id), reconnect, delete (computers configured only on a member hub cannot be renamed or deleted here) |
| Devices | `/admin/devices` | Device credential list, revoke, generate the phone pairing QR code; on a member hub it only points you to the primary hub's admin console |
| Extensions | `/admin/extensions` (the old `/admin/plugins` still opens) | This hub's extensions: list, toggle, install (a directory or package on the hub machine, an https link with sha256, upload, remote socket; preview what it can do first, install only after agreeing), upgrade (clearly flagged when permissions grow), roll back, uninstall (optionally keeping data), and the extension market (register markets, browse the catalog, install / upgrade from a market); see "Extensions" below and [extensions.md](../extensions/extensions.md) ("Admin console: extensions") |
| Assistant cards | `/admin/assistant` | The main chat rich UI switch `assistant.persona_view`, what each client actually supports, local previews of example cards ("Assistant cards" below) |
| Usage sources | `/admin/usage` | Subscription quota sources from account-pool gateways (such as relay gateways) (`docs/server/usage-windows.md` ("Account-pool gateways"), #2595): master switch and read interval, source list (name, type, address, status: last read, account / window counts, failure reason; a toggle per source), add / edit (type, name, address, Key) and **Test connection** (reads once without saving, reporting how many accounts, windows and providers it sees), delete (confirmed in place). The Key is write-only; changing the address requires entering it again |
| Network & access | `/admin/network` | Public address / TLS / candidate addresses (editable, take effect after restart), `security` settings (read-only), the admin console's access scope (narrow only), change the admin password |
| Advanced › Linked hubs | `/admin/peers` | Rarely needed (installing a single hub is recommended): hubs in the group (primary / member, online, term, **Make primary**, with a confirmation first when the old primary is unreachable), discovered hubs, generate / enter a link code, link status and RTT, disconnect ([mesh.md](../server/mesh.md)) |

Every page on a member hub says at the top "This hub is a member of a group run by the primary hub "X"" (with an extra sentence and a **Change primary** entry when the primary is unreachable): group matters like devices and linked hubs are handled in the primary's admin console.

Navigation is a list (`web/src/admin/nav.ts`, with an "Advanced" group); adding "Updates" (#284) or "Settings" (#287) later means adding an entry.

## API

Everything is under `/api/v1/admin/`, checked first against the access scope, then the session (except `state`, `setup`, `login`). Errors are `{"error": "…", "code": "…"}`;
not signed in or session expired is 401 `login_required`, a wrong CSRF token is 403 `csrf`. Write request bodies are always JSON (send `{}` when there is no content).

| Endpoint | Purpose |
| --- | --- |
| `GET state` | `{"password_set", "logged_in", "can_setup", "setup_code_required", "csrf", "hub_name", "version", "session_expires_at"}`; `csrf` only after sign-in |
| `POST setup` `{"password", "setup_code"}` | Set the first password and sign in, returns state; 409 `already_set`, 403 `setup_local_only` / `setup_code_required`, 401 `bad_setup_code`, 400 `weak_password`, 429 `rate_limited` |
| `POST setup-code` | Used by `personad hub admin setup-code`: loopback + master token only, returns `{"code", "expires_at"}`; 409 `already_set` once a password is set |
| `POST reload` | Used by `personad hub admin access` after writing hub.json: loopback + master token only, rereads `admin` and `name` |
| `POST login` `{"password"}` | Returns state; 401 `bad_password`, 429 `rate_limited` (`retry_after` seconds) |
| `POST logout` | Sign out; if the session in the database could not be deleted, it still clears the cookie but returns 500 `logout_failed` (a copy of the cookie can still sign in until it expires) |
| `POST password` `{"current", "new"}` | Change the password; all other sessions are invalidated |
| `GET overview` | `hub` (`hub_id`, `name`, `name_set`, `default_name`, `version`, `started_at`, `os`, `arch`, `container`, `can_restart`), `listen`, `public_url`, `tls` (`listen`, `port`, `fp`; null when off), `endpoints`, `machines` (`total`, `online`), `devices`, `restart` |
| `PUT hub` `{"name"}` | Change the hub name (empty string = use the default name), effective immediately |
| `POST doctor` | Run `hub doctor` once: `{"items": [{"level": "ok｜problem｜info", "item", "detail"}], "next": ["…"], "ran_at"}` |
| `POST restart` | Restart the hub (only when `can_restart`); 409 `not_supervised` |
| `GET machines` | `{"machines": [{id, name, kind, icon, ssh_host, online, error, transport, node_version, node_mismatch, os, last_seen, link, state, tailcat, address, proxy, hub_id, paths}], "tls": bool, "restart"}`; `state`: `active` / `pending_add` (written to hub.json, but if the hub is not running it only connects after a restart) / `pending_remove`; kind `hub` is a computer configured only on a member; `paths` has one `{via, hub_id, id, ok, rtt_ms, active, error}` per route; `proxy` excludes the password |
| `POST machines` `{"kind": "link", "id", "name", "icon"}` or `{"kind": "service", "id", "name", "icon", "pair_link", "address"?, "proxy"?}` | Add one; it starts connecting immediately, returns `{"machine", "restart"}`; service first pairs with the connector (credentials stored encrypted in the database) and only writes hub.json on success. Persona Link credentials and unused pairing codes left under a new id by an earlier computer are revoked first. 400 `invalid_id` / `invalid_name` / `invalid_kind` / `invalid_proxy` / `ssh_unsupported` (ssh and tailcat can no longer be added), 409 `exists`, 502 `pair_failed` (`error` is a human-readable reason) |
| `POST machines/{id}/connector` `{"pair_link", "address"?}` | Re-pair a service computer's connector: `{"ok", "address", "version", "restart_required"}` (a changed address needs a restart); 400 `not_service`, 502 `pair_failed` |
| `PUT machines/{id}` `{"name"}` | Rename: written to hub.json and taken at once (no restart, no reconnect; every client and the computer's Persona Link follow); `""` is its id again. Returns `{"ok", "name"}`; 400 `invalid_name` (over 40 characters, or not one line) / `bad_request` (no `name`), 404 `not_found`. See [protocol.md](../protocol/protocol.md) "Renaming a computer" |
| `DELETE machines/{id}` | Remove from hub.json (or from the manager's list that keeps it), returns `{"ok", "restart"}`; one still waiting to be confirmed is removed as rejecting it does. 404 `not_found`; when the manager that keeps it refuses or is away, 409 `default_machine`, 503 `assistant_offline` or 502 `manager_failed`. Its Persona Link credentials and unused pairing codes are revoked first, whatever its kind, which closes its open link (4002); they stay revoked when the removal then fails |
| `POST machines/{id}/reconnect` | Reconnect; 409 `not_active` |
| `POST machines/{id}/link-pair` | Generate a pairing link for a link computer's persona-link: `{"pair_url", "expires_at", "command"}`; 409 `tls_required` / `not_active`, 400 `not_link` |
| `GET devices` | `{"devices": [{id, name, kind, role, created_at, last_seen, machine_id}]}` |
| `DELETE devices/{id}` | Revoke |
| `POST devices/pair` | Phone pairing: `{"pair_url", "url", "expires_at", "fp", "qr": {"size", "rows": ["0101…"]}}` (`rows` is one string of `0` / `1` per row, without the quiet zone) |
| `GET assistant` | `{"persona_view"}`: hub.json's `assistant.persona_view` (the changed value if the admin console changed it) |
| `PUT assistant` `{"persona_view"}` | Write hub.json's `assistant.persona_view` (off = delete the key); other keys in `assistant` and other fields in the file are kept as is; effective on the hub immediately (for the assistant prompt see "Assistant cards" below); missing field 400 `bad_request` |
| `GET network` | `listen`, `public_url`, `tls` (`listen`, `port`, `fp`), `advertise`, `advertise_ipv6`, `advertise_lan`, `endpoints`, `security` (`trusted_cidrs`, `allow_public_plaintext`), `admin` (`access`, `cidrs`, `session_hours`, `from_env`, `container`), `you` (`address`, `tls`, `allowed`: whether each of the four scopes would let you in), `restart` |
| `PUT network/access` `{"access", "cidrs", "force"}` | Narrow the access scope; 403 `widen_cli_only`, 409 `from_env` / `would_lock_you_out`, 400 `invalid_cidr` |
| `PUT network/public` `{"public_url", "tls_listen", "advertise"}` | Change the public address / TLS listener / candidate addresses (omitted fields unchanged), effective after restart |
| `GET mesh` | `{"role": "center｜member", "center": {hub_id, name, online}, "term", "hubs": [{hub_id, name, role, online, self, paired}]}` |
| `POST peers/{hub_id}/center` `{"confirm"}` | Make primary (hub_id may be this hub); 409 `confirm_required` (the old primary is unreachable, needs confirmation) / `unreachable`, 404 `not_in_group` |
| `…/peers`, `…/routes` | Linked hubs: paired hubs (each with `role`), discovered hubs, link codes, disconnect, routes ([mesh.md](../server/mesh.md) ("Admin API and command line")) |
| `devices*` on a member | 409 `member_hub`: devices are managed by the primary |
| `GET extensions` | `{"extensions": [ExtensionDetail], "can_install", "upload_most"}`: each item is the app API's detail ([extensions.md](../extensions/extensions.md) ("hub API")); `upload_most` is the upload limit (bytes) |
| `GET extensions/{id}` | One extension's detail; 404 `not_found` |
| `POST extensions/preview` `{"source", "upgrade"?}` | Preview (source syntax same as `personad extension install`), returns the preview (with `permissions_digest`, and `more_permissions` / `added_permissions` on upgrade); 400 `invalid_plugin` (`error` is a human-readable reason), 409 `busy`, 429 `rate_limited` |
| `POST extensions/upload?name=<file name>&upgrade=<id>` | The request body is the `.tar.gz` / `.tgz` / `.zip` itself (`Content-Type: application/octet-stream`, the only non-JSON write, still needs the CSRF header), at most 200 MB, returns the preview; 413 `too_large`, others as above |
| `POST extensions/install` `{"preview_id", "permissions_digest"}` | Install the previewed package, returns the detail; without a digest 400 `confirm_required`; digest mismatch or expired preview 400 `install_failed` |
| `POST extensions/{id}/upgrade` `{"preview_id", "permissions_digest"?}` | Upgrade; when permissions grow the new digest is required; 400 `upgrade_failed` |
| `POST extensions/{id}/rollback` | Go back to the previous version; 400 `rollback_failed` |
| `PUT extensions/{id}/enabled` `{"enabled"}` | Toggle; ones configured in the manager's manager.json 409 `read_only` |
| `DELETE extensions/{id}` `{"keep_data"}` | Uninstall; built-in ones and ones configured in the manager's manager.json 409 `uninstall_failed` |
| `GET extensions/-/markets`, `POST extensions/-/markets` `{"spec", "token"?}`, `DELETE extensions/-/markets/{id}`, `POST extensions/-/markets/{id}/refresh`, `PUT extensions/-/markets/{id}/token` `{"token"}` | Extension markets, the same set as the app's `/api/v1/extensions/-/markets…` ([extensions.md](../extensions/extensions.md) ("Extension market")); the index is read once before registering, and if it cannot be read 400 `market_failed` (`error` is a human-readable reason); the token is write-only, never echoed |
| `GET extensions/-/catalog?q=&market=&refresh=1`, `POST extensions/-/catalog/preview` `{"market", "extension"}` (the old key `plugin` is also accepted) | The market catalog (`{markets, extensions, platform}`) and previewing from a market (returns the same preview, plus `market`); once you have the preview, use `extensions/install` / `extensions/{id}/upgrade` as usual |
| `GET extensions/-/metrics?window=`, `GET extensions/{id}/metrics?window=`, `GET extensions/{id}/logs?since=&q=&limit=`, `GET extensions/{id}/logs/stream?since=` | Extension health (call counts, error rate, latency, restarts) and logs (followed live via server-sent events), see [extensions.md](../extensions/extensions.md) ("Health") |
| `GET usage-sources` | `{"enabled", "poll_minutes", "limit", "sources": [{id, name, type, url, has_key, enabled, status: {ok_at?, tried_at?, error?, seen?: {accounts, windows, listed, providers}}}]}`; **never includes the Key**, only `has_key`. `error` is the read failure code (`unauthorized`, `forbidden`, `not_found`, `unreachable`, `timeout`, `bad_response`, `too_large`, `redirect`, `server_error`) |
| `PUT usage-sources` `{"enabled"?, "poll_minutes"?}` | Master switch and read interval (1–1440), written back to the manager's manager.json (`usage`), effective immediately, returns the same as above; 400 `invalid_poll` |
| `POST usage-sources` `{"type", "url", "key", "name"?, "enabled"?}` | Add, returns `{"source"}`; 400 `invalid_type` / `invalid_url` (must be http(s), no username or password) / `invalid_name` / `invalid_key` / `key_required` (relay requires a Key), 409 `too_many` (at most 8) |
| `PUT usage-sources/{id}` `{"name"?, "type"?, "url"?, "key"?, "clear_key"?, "enabled"?}` | Edit; without `key` the existing one is kept, **but changing `url` or `type` requires sending `key` again** (400 `key_required`: a saved Key never follows to a new address); 404 `not_found` |
| `DELETE usage-sources/{id}` | Delete; 404 `not_found` |
| `POST usage-sources/test` `{"type", "url", "key"?, "id"?, "clear_key"?}` | Reads once, read-only, saves nothing: `{"ok": true, "accounts", "windows", "listed", "providers"}` or `{"ok": false, "code"}` (a read failure is a result, not an HTTP error). `clear_key: true` tests without a Key, overriding both a supplied and a saved Key, just as saving does. Otherwise, without `key` but with `id` it uses the saved Key, **only against the saved address and type**; only one runs at a time (429 `busy`), at most 25 seconds |
| hubs without the extension base | `extensions*` writes return 503 `no_plugins`; older hubs don't have these routes (404), and the page says to upgrade the hub first |
| Old paths `plugins…` | Routes from before #1844; the hub still accepts them and returns the old field names (`plugins`, `plugin`, `plugin_name`, and `plugins` for a market's count); the page reads the list once and if `extensions` returns 404 it switches the whole page to the old routes (old hubs, `web/src/admin/api.ts`) |

The `usage-sources…` endpoints belong to the manager (`docs/server/usage-windows.md` ("Who owns this")): after authenticating the admin session and checking CSRF, the hub forwards them unchanged with `X-Persona-Admin`; if the manager is unavailable, it returns `503 assistant_offline`.

## Extensions

Installing an extension runs a new program on the hub machine. The admin console can add computers and issue device credentials, so it already has the same authority as the hub's owner; a signed-in session can therefore install extensions too ([extensions.md](../extensions/extensions.md) ("Install, upgrade, roll back, uninstall")),
using the same registry and checks as `personad extension`, plus:

- **Preview, then agree**: installation and upgrades that add permissions must include the preview's `permissions_digest`, ensuring the installed package is the one shown to the user; the page only offers **Agree and install** after receiving a preview.
- **Preview rate limits**: each preview keeps up to 200 MB in the staging directory (`~/.persona/extensions/staging/`) for 10 minutes, so only one runs at a time (409 `busy` while another runs),
  at most 10 in 10 minutes (successes and failures both count; exceeding it returns 429 `rate_limited` and `Retry-After`), so even someone who steals a session cannot fill the disk.
- **Upload**: the browser POSTs the file unchanged; the hub writes it into this preview's staging directory as it arrives (stopping above 200 MB), then removes the original after unpacking; obviously oversized requests are rejected first by `Content-Length`.
- **Audit**: every step goes into `plugin_audit`, actor `admin UI`; a hub log line says `admin: installed plugin …`.
- **Health** (#1857): each list row shows the last 24 hours' error rate and p95; the detail has call / error / latency charts, a table by method, reasons for error exits, and the extension's logs (search, live follow, copy),
  see [extensions.md](../extensions/extensions.md) ("Health", "Admin console: extensions"). Only signed-in sessions can view logs: an extension's own output may reveal more than the app should see.
- **Extension market** (#1330, page `web/src/admin/pages/ExtensionMarket.tsx`, sharing logic and copy with the web app in `web/src/lib/extension-market.ts`): two cards above the extension list (#5017), shown only when extensions can be installed (`can_install`) and the hub has the market API (the entire section is hidden on 404 / 503).
  **Extension markets** lists registered markets: name, whether a token exists, address (separate icons for https / GitHub / git), extension count · time since last read, failure reason (red) and index warnings (orange); each market can be refreshed, have its token added / replaced / removed (write-only, never echoed), or be deleted (confirmed in place; extensions installed from it remain).
  **Add market** takes an address and optional token: placeholders and instructions show three forms (`https://…`, `github:owner/repo[@branch]`, `git:<address>[#branch]`, #1373); the page does not validate the syntax, but passes it unchanged to the hub, which only registers it after reading the index and shows any failure reason in the dialog;
  `git:` markets (and `git@host:path.git` with the prefix omitted) use the hub machine's own SSH credentials; the dialog disables the token field and the list has no "add / replace token" action.
  **Extensions in markets** supports search and filtering by market; each row shows name, version, status (not installed / upgrade to x / installed / built into hub / cannot install), publisher, market, description, tags and the reason installation is unavailable;
  **Install** and **Upgrade** first call `extensions/-/catalog/preview`, opening the same preview as other sources (permissions, red warning and "Added" when permissions increase, minutes remaining); after agreement they use `extensions/install` / `extensions/{id}/upgrade` above (with preview id and digest), then refresh both the installed list and catalog.
  Extensions installed from markets are upgraded, rolled back and uninstalled in the same list as other installed extensions. Screenshots: `/admin/extensions?mock=app`; `&dialog=market` directly opens the preview for the first upgradable extension; `&dialog=market-add` opens Add market (`web/src/admin/mock.ts`).

## Assistant cards

Page `web/src/admin/pages/Assistant.tsx`, API `server/internal/hub/admin_assistant.go` (#2023; design in [assistant-views.md](../assistant/assistant-views.md) ("Prompts and switches")).

- **Switch**: `assistant.persona_view`. Before enabling, a dialog confirms client versions (iPhone must wait for the corresponding TestFlight); disabling asks nothing and returns the assistant to persona-card, supported by every client.
  No hub restart: every hub read of this switch uses `h.assistantPersonaView()` (the changed value if the admin console changed it, otherwise startup `h.cfg`), including Hello. After changing it, the hub sends `manager.updated` to the connected assistant process (`assistantUpdated`, which keeps its own Hello), then requests its tool list again (`assistantToolsFollow`; `view_show` and others appear / disappear with the switch), so card tools and actions, body-card registration and channel forwarding catch up within one second.
  **The assistant prompt does not change immediately**: CLAUDE.md is only rewritten when preparing the assistant directory (opening a new chat, or the app opening the chat page to warm up a new process). The assistant process reads CLAUDE.md and the tool list at startup; an already-running process keeps those startup values until restarted. New code reading this switch must not read `h.cfg.Assistant.PersonaView` directly. `assistant.html_views` is managed separately by the hub (#2019); this page only explains that it runs when this switch is on and that client has passed security acceptance.
- **Client support**: states what the web, Apple, Android and watch / notification clients can do in the current implementation; the version actually installed on each client is what matters.
- **Example previews**: synthetic data from `schema/extension/examples/assistant/`, rendered by the web's shared component (`views/extensions/ViewBody.tsx`). Local to this page: selecting options, filling forms and submitting only change this page's state and show "In a real chat this would send back…",
  never calling chat or action APIs; HTML examples show only the text fallback, with no iframe or scripts. `web/src/admin/assistant-examples.test.tsx` scans the source to ensure the page only calls these two admin APIs.
- Verification: `python3 scripts/test/admin_assistant_e2e.py <web address> [--shots <directory>]` (dev server `/admin/assistant?mock=app`; wide / narrow × light / dark, recording requests made during previews, which must be zero).
  Screenshots: `?dialog=enable` opens the confirmation; `?dialog=example-form` and similar directly select an example.

## Choosing at deploy time

| Deployment | How to choose |
| --- | --- |
| `scripts/deploy.sh` | `--admin-access <scope>` (plus `--admin-cidrs a,b` for `cidrs`) or `PERSONA_ADMIN_ACCESS`; if neither is given and no choice has been made on the hub, asks when a terminal exists, otherwise uses `lan` and prints a reminder |
| `install.sh` (public version) | `curl … \| PERSONA_ADMIN_ACCESS=lan sh` or `sh -s -- --admin-access lan`; if neither is given, asks "Will this machine run the hub?" and then the scope when a terminal (`/dev/tty`) exists; otherwise prints a reminder without writing hub.json (omitted means `lan`) |
| Docker | `PERSONA_ADMIN_ACCESS` in `.env` (plus `PERSONA_ADMIN_CIDRS` for `cidrs`); when unset, the entrypoint prints a reminder and uses hub.json (omitted means `lan`) |

The prompt explains all four options and their risks (see the admin access section of `scripts/deploy/install.sh`; `deploy.sh` uses it unchanged, so the public installer and deployment ask the same question).

## Security trade-offs

- **Why default to `lan`, not `loopback`**: on a NAS or headless server, a local browser is not useful; `lan` already blocks the internet and other subnets.
- **Why `any` also allows LAN plaintext**: `any` is a superset of `lan`; transmitting a password in plaintext on the LAN is the same as under `lan`, a risk that scope already accepts.
- **Why not use the app token**: device tokens can be revoked or stolen and used over the internet; the admin console can add computers, issue credentials and change access scope, with more authority than the app. A separate
  password + scope restriction keeps a leaked token from granting access.
- **Why no multiple admins / two-factor auth**: this is a personal hub with one admin; for tighter security, narrow to `loopback` and access it over an ssh tunnel.
