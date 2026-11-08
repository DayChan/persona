# Direct public access to hub

Phones connect directly to hub over the public internet, preferring IPv6; users configure their own VPN. Hub provides a separate TLS listener and retains HTTP for LAN / old clients. **Expose only the TLS port publicly**; clients must verify HTTPS / WSS before sending tokens.

This document reflects existing `server/internal/hub/` and `server/internal/netaddr/` implementations. Existing iOS / Watch clients do not yet gain self-signed certificate pinning from this and still use their original LAN / VPN addresses. Separate DDNS and doctor implementations are explained below. If direct routes all fail, iOS / macOS apps can also [connect to hub through an SSH tunnel](#经-ssh-隧道连-hub直连不通时的后备).

<a id="hubjson-网络配置"></a>

## hub.json network configuration

Merge these fields into `~/.persona/hub.json` on the hub machine, preserving existing token, database, and machine configuration, then restart hub:

```json
{
  "listen": ":18660",
  "public_url": "http://hub.example.com:18660",
  "public": {
    "tls_listen": ":18662",
    "advertise": ["https://hub.example.com:18662"],
    "advertise_ipv6": true,
    "advertise_lan": true
  },
  "security": {
    "trusted_cidrs": [],
    "allow_public_plaintext": false
  }
}
```

Domains are examples; replace with your configuration. HTTP addresses should be reachable over LAN / VPN. `18662` is only this document's example TLS port; no TLS port is built in by default. Avoid HTTP / Postgres port conflicts. Configuration changes need a restart; certificate refresh behavior is described next.

| Field | Default and behavior |
| --- | --- |
| `listen` | HTTP default `:18660`, retained for LAN / VPN. `hub pair` also requires IPv4 loopback access |
| `public_url` | QR `url`, also added to candidates; keep an address old clients can reach |
| `public.tls_listen` | Empty string disables TLS; setting it adds separate HTTPS / WSS listening |
| `public.cert_file` / `public.key_file` | Both empty means managed self-signed certificates; custom certificates require both file paths, preferably absolute |
| `public.advertise` | Additional candidates; full HTTPS URLs recommended, also accepts domains, IPs, or hosts with ports. Missing scheme / port is filled from current listener. Advertising does not start listeners, open firewalls, or update DNS |
| `public.advertise_ipv6` | Unset or `true` automatically advertises local global IPv6 candidates |
| `public.advertise_lan` | Unset or `true` automatically advertises LAN IPv4 / ULA candidates, delivered only after authentication |
| `public.ddns` | Omitted disables it; use a domain to recover hub after prefix changes; see [configuration](config.md#ddnspublicddns) |

Both listeners share header timeout, idle-connection timeout, and header-size limits. No whole-request read / write timeout, avoiding truncating WebSockets or large-file transfers.

The two `advertise_*` switches control only discovery, not explicitly configured `public_url` / `public.advertise`. With TLS enabled, discovered candidates use HTTPS and the TLS port; explicit HTTP URLs remain unchanged.

The following values are policy defaults from `config_security.go`, **not measured throughput, performance, or protection claims**. Zero / negative integers use defaults.

| `security` field | Default | Meaning |
| --- | --- | --- |
| `trusted_cidrs` | `[]` | Additional trusted source subnets, treated as LAN on match; include only networks you truly control |
| `allow_public_plaintext` | `false` | Allows public plaintext API when enabled, with a startup warning; does not remove the public main-token prohibition |
| `auth_failures` | `10` | Ban source after this many failures within a counting window |
| `auth_window_seconds` | `60` | Window from the source's first failure in this round, seconds |
| `auth_block_seconds` | `900` | Ban duration after threshold, seconds |
| `auth_max_sources` | `4096` | Source counter capacity; clear expired entries when full, then evict unbanned entries with earliest expiry first |
| `auth_cache_entries` | `2048` | Device-token lookup cache capacity, including invalid tokens; code fixes TTL at 30 seconds |
| `auth_log_interval_seconds` | `30` | Minimum security-error logging interval, with skipped counts aggregated in `suppressed` |
| `proxy_protocol` | Omitted; reads no PROXY headers | Preserves real source classification behind same-machine port forwarding / TCP passthrough; see [PROXY protocol](#proxy-protocol) below |

Public IPv4 counts per address; IPv6 aggregates by /64. Ban checks precede token database lookup, so even valid tokens from banned sources get `429` and a seconds-valued `Retry-After`. The threshold-triggering failure still returns `401`; subsequent requests return `429`. LAN / VPN sources are not counted.

## TLS and public-key pinning

Without custom certificate files, hub creates ECDSA P-256 identity key `hub.key` and self-signed `hub.crt` in `~/.persona/tls/`. Directory permissions 0700, files 0600. The certificate contains only `<hub_id>.persona.invalid`, no local hostname or LAN addresses. Minimum TLS version 1.2.

Managed certificates last 365 days, renewed with the same key when roughly less than 30 days remain; also reissued if not yet valid or SAN mismatches. Hub certificate refresh timer is 2 minutes, a scheduling configuration, **not a measured refresh delay**. Custom certificates are reread, but hub does not obtain or renew them; no automatic ACME flow currently.

Run on the hub machine:

```sh
~/.persona/bin/personad hub pair
```

With TLS enabled, the printed "Public key fingerprint" is pin `fp`: SHA-256 of certificate SubjectPublicKeyInfo DER, unpadded base64url, 43 characters. QR codes carry it too. `hub pair` generates a new temporary token; protect links / QR codes as credentials. It is not a read-only fingerprint command.

Pinning-capable clients obtain `fp` through trusted scanning, verify the public key in TLS handshake, and check `hub_id`. Already-paired clients can read `hello` or `/api/v1/endpoints` over a trusted authenticated LAN channel. **The `fp` in public unauthenticated health responses cannot establish initial trust.**

Replacing a certificate while keeping the private key preserves the pin. Replacing the private key changes it, requiring all pinned devices to re-pair. A running hub refuses detected key replacements and logs them; restart required. Deleting `tls/` is not a normal renewal step. No `fp_next` announcement or seamless key rotation currently.

Default pairing tokens last 10 minutes and redeem a device token once. After redemption they remain at most 2 more minutes, never beyond original expiry. This supports old iOS pairing; see [protocol](../protocol/protocol.md#临时配对). Only `hub pair --legacy` prints the main token; public sources always reject it.

Computer links (persona-link, [link.md](link.md)) also use only this TLS port and pin the fingerprint obtained at pairing, including on LAN; hub rejects their plaintext requests except loopback.
TLS terminates at hub, which authenticates link credentials and forwards the connection unchanged to the computer-connection manager (2026-11-04, [machines.md](../server/machines.md), "Who connects computers").
Forwarding uses `GET /api/v1/bus/relay` with the manager's own bus token (same as the bus: only `Authorization: Bearer jb_…`, TLS required for public sources; device credentials cannot open it, nor can it open app endpoints).

## Candidate addresses and DDNS

Candidates sort `ipv6`, `name`, `lan`, `ipv4`, HTTPS first within each type. Linux reads flags and lifetimes through netlink, normally excluding temporary, deprecated, tentative, DAD-failed, and expired global IPv6, virtual interfaces, link-local, and documentation prefixes. ULA is `lan`. Public IPv4 is advertised only by explicit configuration; no automatic NAT discovery or inbound public-IPv4 verification currently.

On non-Linux platforms without flags, or ordinary interface enumeration after netaddr failure, hub cannot guarantee temporary IPv6 exclusion. `netaddr.Stable` rejects unknown flags itself, but hub's fallback enumeration accepts them into candidates; do not call these "verified stable addresses."

Address-change notifications trigger rescans, with periodic fallback. Candidates mean an address was found locally or configured, **not that it is publicly reachable**. `seen_at` is collection time, not connectivity-test time. Clients obtain lists through authenticated `/api/v1/endpoints` and WS `hello`; only connections declaring `endpoints` receive address-change events.

Broadband reconnection may change IPv6 prefixes, invalidating existing connections / literal addresses. Recover with a domain: `public.ddns` makes hub update DNS (see [configuration](config.md#ddnspublicddns)), or use an external DDNS tool for AAAA records. Either way, add the HTTPS domain URL with TLS port to `public.advertise` so the app receives it; `advertise` alone does not update DNS.

### Client-added candidates

Hub's advertised list may be incomplete: discovery may miss addresses on physical interfaces classified as virtual, or `public.advertise` may be unconfigured. Clients can add candidates:

- Manual candidates stay on the client, saved per pairing. New hub lists replace only hub-supplied candidates, not manual ones, client-disabled settings, or user ordering.
- Clients may let users reorder candidates and try in that exact order: displayed order equals attempt order, without per-network memory or hidden adjustments. New hub candidates append; removed hub candidates disappear.
- Same security requirements as hub-supplied candidates: HTTPS pins the public-key fingerprint from trusted pairing and checks `hub_id`. Pairings without fingerprints (hub itself has no TLS, behind a reverse proxy) use system CA / hostname verification for HTTPS; see "Reverse-proxy subpaths (Android)" below. Plain HTTP is LAN-only; check the actual peer address again before sending a token.
- Manual candidates do not prove reachability, just as `seen_at` is not a connectivity test. Clients may probe unauthenticated `GET /api/v1/health` only as evidence "This address reaches the already-pinned hub," never deriving initial trust from response fingerprints.

Android implementation: [android.md](../android/android.md#连接地址设置--连接诊断); computer links: [link.md](link.md).

## Allowing IPv6 ingress

First confirm a physical hub interface has global IPv6, keeping a stable address suitable for long-term advertisement. Do not use a privacy temporary address as a fixed service address. Prefer randomly generated stable interface identifiers to easily guessed fixed suffixes.

Modem / router menus vary, usually "IPv6 firewall," "Inbound rules," or "IPv6 access control." Configure each layer in the actual topology:

1. If the modem routes, add an inbound allow rule for hub's global IPv6 with **TCP destination port = TLS port**. If another router firewall follows, allow both layers. With bridged modem, inspect the dialing router instead.
2. Target the hub device or its stable IPv6, allowing only example TCP 18662. IPv6 direct access normally uses firewall rules, not copied IPv4 NAT port mapping. After prefix changes, verify rules target the current address.
3. Keep other inbound ports closed: no HTTP 18660, Postgres 18661, or other management services. If equipment only supports disabling the entire IPv6 firewall, first ensure every home host has inbound protection; protecting hub alone does not secure the network.
4. Hub's host firewall must also allow the TCP TLS port, retaining default-deny for public ingress. Keep SSH management / LAN HTTP under existing trusted-source rules. Verify IPv6 rules, not just IPv4.

Docker published ports also need container-forwarding rule checks; ufw's inbound list alone does not prove a port is unexposed. Keep database loopback-bound. Preserve a working management connection while changing firewalls.

After allowing ingress, disable phone Wi-Fi and test on cellular, confirming IPv6 support there. Accessing hub's own address from hub follows a local path and cannot prove modem rules work. Public reachability, latency, and carrier idle-connection lifetime were not measured for this document.

<a id="来源判断与反向代理"></a>

## Source classification and reverse proxies

Hub trusts only connection source and local receiving addresses, **not `X-Forwarded-For` or `Forwarded` for trust decisions**; the sole exception is PROXY headers from configured upstreams (see [PROXY protocol](#proxy-protocol) below). Loopback, private / ULA, link-local, IPv6 prefixes on the same local-interface link (/64 or longer), and `trusted_cidrs` matches are local-network sources. Shared carrier IPv4 space is not automatically trusted; only connections arriving on local VPN interfaces or explicitly whitelisted count as LAN.

**Container-gateway exception**: in containers, the gateway (default-route next hop, first address in each subnet, `server/internal/hub/admin_gateway.go`) counts as public, not local, unless in `trusted_cidrs`. Docker userspace proxy (docker-proxy) may make LAN and public connections alike appear from the gateway; without exclusion, public requests could use main tokens / plaintext. LAN connections through Linux iptables DNAT retain source addresses and are unaffected, as are `--network host` deployments.

**Docker Desktop / OrbStack forwarded sources** are not guaranteed gateways: Docker Desktop gvisor networking may make local and LAN clients share one public-looking address. Hub uses the actual peer, so plaintext APIs return `403 tls_required`; whitelisting only `docker network inspect`'s gateway may not help. While connecting, inspect the remote address for container port 18660 with `docker compose exec hub netstat -tn`, then follow [Docker troubleshooting](docker.md#docker-desktop--orbstack-的明文连接). Prefer hub TLS. For local-only trials, bind host ports to `127.0.0.1`, then whitelist the observed forwarded source as a single-address CIDR in `security.trusted_cidrs`. That source may represent all forwarded clients; a whitelist does not restore original identity.

Local TLS-terminating reverse proxies make source loopback; remote ones make it the proxy address. This affects public plaintext / main-token restrictions, health redaction, and rate limits. Forwarded headers cannot restore these checks. A reverse proxy must handle public TLS, access control, rate limiting, and prevent public access through it to local-only pairing-code endpoints. Prefer hub's own TLS listener. TCP-only forwarding without TLS termination (port forwarding, tunneling, nginx `stream` SNI passthrough to TLS port) can use PROXY protocol to convey real sources.

### PROXY protocol

**Risk**: with a **same-machine** TCP-forwarding layer (for example, a public tunnel to localhost, then nginx `stream` SNI passthrough to hub TLS), every public connection appears from `127.0.0.1` and is treated as local:

- admin UI bypasses `admin.access` (loopback always allowed), even publicly with `cidrs` configured;
- main token works publicly;
- only public authentication failures count, so public token / admin-password guessing is unthrottled;
- logs show only `127.0.0.0/24`, concealing real sources.

TLS passthrough cannot add HTTP headers, and forwarding headers must not grant access. PROXY protocol ([specification](https://www.haproxy.org/download/2.9/doc/proxy-protocol.txt)) prepends a text line (v1) or binary header (v2) before TLS handshake, stating whose connection is forwarded. Hub supports v1 and v2, trusting only configured upstreams:

```json
{
  "security": {
    "proxy_protocol": {
      "from": ["127.0.0.1/32"],
      "listeners": ["tls"]
    }
  }
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `from` | Required | Trusted upstream CIDRs or single addresses, as seen by hub (typically `127.0.0.1/32` for same-machine). Include only the proxy itself; `0.0.0.0/0` / `::/0` rejected |
| `listeners` | `["tls"]` | Applies to `tls` (`public.tls_listen`) / `http` (`listen`). If `from` includes loopback, `http` is forbidden: local `personad hub pair`, `hub admin`, and plugins use plaintext loopback without headers and would be rejected |

Without `proxy_protocol`, hub reads no PROXY headers and behaves exactly as before. Invalid configuration (empty / nonaddress `from`, unknown `listeners`, HTTP with loopback) prevents startup; `personad hub doctor` identifies the field. TLS-only without `public.tls_listen` logs a notice. Restart after changing.

**Rules** (`server/internal/proxyproto`):

- **Connections from `from` require valid headers**; header source becomes the peer for source classification, admin access, main-token restrictions, rate limits, and logs. Missing, invalid, or timed-out headers close without response; otherwise bypassing the proxy (or a misconfigured proxy without PROXY support) would again become loopback.
- **PROXY headers from other sources are not accepted**: hub does not read them; bytes remain the connection prefix, causing TLS failure or HTTP 400. Existing clients unaffected; sources outside `from` cannot impersonate addresses.
- **v2 `LOCAL`** (proxy-originated connections, such as health checks) uses the socket's source, the upstream address, per specification.
- **v1 `UNKNOWN`, v2 `PROXY` with `UNSPEC` family** (upstream does not know the source, e.g. received over Unix socket) are rejected. Although the specification permits socket-address fallback, that would use upstream loopback and trust forwarded traffic as local. v2 UDP / Unix families also rejected.
- **Loopback header source with nonloopback upstream** (another machine forwarding its local programs) uses the upstream address; that machine's "local" is not hub-local.
- **Timeout / size**: complete header within 5 seconds (unmeasured policy default); v1 at most 107 bytes (spec limit), v2 address + TLV at most 4096 bytes, TLVs discarded after reading. Read on the connection's own goroutine at first read / write / peer lookup, never in Accept, so slow / silent connections do not block others.
- **Local address remains the socket's**, not header destination, which belongs to the upstream (proxy listener, possibly another machine). Hub uses local address to determine entry through a local VPN interface (`100.64.0.0/10` classification). Proxied shared-space sources are therefore public—stricter, never looser; add `trusted_cidrs` if needed.
- Apply equally to systemd `personad.socket` listeners and hub-created listeners.

**Preserve the source through every hop**: upstream headers contain the source it sees. If it is the public entry point, enable PROXY on its connection to hub (`proxy_protocol on;` in nginx `stream`). If another forwarding layer (tunnel client, another port forward) connects to it, it again sees loopback; that layer must also send PROXY, and the upstream must receive, trust, and pass it onward. nginx `stream` example (requires `ngx_stream_realip_module`, `--with-stream_realip_module` in `nginx -V`; example addresses / ports):

```nginx
stream {
    map $ssl_preread_server_name $backend {
        hub.example.com 127.0.0.1:18662;   # hub TLS port
        default         127.0.0.1:8444;
    }
    server {
        listen 127.0.0.1:8443 proxy_protocol;  # Previous hop also sends PROXY; omit proxy_protocol if nginx is the entry point
        set_real_ip_from 127.0.0.1;            # Trust only the previous hop; omit if nginx is the entry point
        ssl_preread on;
        proxy_pass $backend;
        proxy_protocol on;                     # Pass client address to hub
    }
}
```

See the previous layer's docs for enabling PROXY (often "proxy protocol" / "send-proxy", v1 / v2 options; hub accepts both). Other backends in `map` also receive headers; unsupported services need a separate `server` without `proxy_protocol on;`.

**Self-check after configuration**:

1. Restart hub; logs include `hub: PROXY protocol on the TLS listener, from 127.0.0.1/32`; `security.proxy_protocol` is "OK" in `personad hub doctor`.
2. Connect publicly on phone cellular. Authentication-failure / admin-login source logs should no longer be `127.0.0.0/24`. With `admin.access: cidrs`, public `/admin` opens the ordinary web app.
3. `proxy protocol: refused a connection from …` logs (at most one per minute, cumulative count) mean an upstream sent no header. Check `proxy_protocol on;` took effect, or other programs bypass the proxy to TLS.
4. `passed on a connection from loopback` (logged once) means source remains loopback: previous hop failed to pass it through. Fix the full chain above.

**Important**: once enabled, every connection from `from` to this port needs a header. Other local programs connecting directly to TLS (`https://127.0.0.1:<TLS-port>`) are rejected; use plaintext or a LAN address. In Docker with userspace docker-proxy port mapping, every mapped connection may appear from the gateway. Adding that gateway to `from` means this port is proxy-only.

<a id="反向代理的子路径android"></a>

### Reverse-proxy subpaths (Android)

Hub can live under a reverse-proxy subpath (`public_url: https://<hub-address>/<prefix>`, with the proxy stripping the prefix before forwarding to hub's plaintext port). Hub then has no TLS itself and no `fp` in `hub pair` links; the proxy uses a public-CA certificate. iOS and web already verify HTTPS through system trust. Web requires proxy `X-Forwarded-Prefix: /<prefix>`; hub adjusts `index.html` `<base>`, placing APIs, WebSocket, images, and routes under it ([web.md](../web/web.md), "Subpaths"; admin UI only at root). Android:

- **Prefixed addresses**: pairing and hub candidates may have path prefixes; every request (REST, WebSocket `/api/v1/ws`, media, installer downloads) appends under it (`hubUrl` / `hubPath` / `hubResolve` in `core/hub`). Hub `/api/…` references also gain the prefix; same-origin checks additionally require being inside it. Candidate keys include prefix (`https://host:443/persona`); root keys unchanged. Manual candidates still stop at the port.
- **HTTPS without `fp` uses system trust**: pairings with `fp` still accept only pinned public keys, never fallback to system CA. Without `fp`, HTTPS uses system trust (`network_security.xml`: system CA only, not user-installed certificates) and hostname verification, consistently for pairing, persistent connections, Settings tests, and update downloads.
- **Trade-off**: pinning trusts one public key; system trust accepts every system CA, any of which can impersonate hub by issuing for its domain (compromised / misissuing CA, device management installing system certificates). Same as browser access: better than plaintext, weaker than pinning. Prefer hub TLS + `fp` when available. Pairing codes appear only when scanned / pasted; hub identity still checked by `hub_id`.

Persona Link on computers (active mode) likewise accepts HTTPS pairing without `fp`: preserves prefix, verifies system CA / hostname; `fp` pairings remain pinned without fallback. Hub allows plaintext link requests only from same-machine or LAN proxies carrying `X-Forwarded-Proto: https`; see [link.md](link.md), "Hub behind a reverse proxy."

After pairing failures, Android Connection diagnostics shows the latest attempt: addresses, failed stage (link parsing / address connection / TLS certificate / HTTP request / pairing-code validation / hub response), error text, time, and per-address probe results. Memory-only, no pairing code.

Public plaintext APIs default to `403 tls_required`, except `GET /api/v1/health`. This refusal happens after the request arrives and cannot retract an already-plaintext token; clients must choose the protocol before requesting.

<a id="经-ssh-隧道连-hub直连不通时的后备"></a>

## Connecting to hub through SSH (fallback when direct access fails)

iOS / macOS apps can reach hub through an SSH server (#891): use when the device has no VPN route and hub has no public entry point, but an externally accessible SSH endpoint exists (jump host, bastion such as Warpgate, own server). No hub changes. Another option beyond direct access / embedded Tailscale / embedded tailcat in [#868](https://github.com/nwverse/persona/issues/868).

```text
App ──HTTP / WebSocket──▶ 127.0.0.1:<random-port> (inside app process)
     ══SSH (direct-tcpip, one channel per connection)══▶ SSH server ──TCP──▶ hub (tunnel target, default 127.0.0.1:18660)
```

- **In-process implementation** (`apple/Packages/PersonaSSH`, swift-nio-ssh + Network.framework), not system ssh. The TestFlight Mac app is sandboxed, unable to call `/usr/bin/ssh` or read `~/.ssh`. Listen on a local loopback port (prefer the same next time), opening one `direct-tcpip` channel per incoming connection for HTTP / WebSocket; one SSH connection per hub.
- **Addresses unchanged inside the app**: pairing, candidates, and comparisons still use hub addresses. Only outbound requests (`APIClient`, WebSocket, port forwarding) are rewritten to loopback by `HubTransport`.
- **Keys**: app-generated Ed25519, one per device; private key only in that device's keychain (`ThisDeviceOnly`: no sync, restore to other devices, or logs).
- **Routing**: each (re)connect tries existing direct routes first (pairing address / group candidates, `GET /api/v1/health`, 3 seconds), using SSH only if all fail. While tunneled, retry direct every 2 minutes; switch back and close the tunnel once reachable. "Always use SSH" prefers the tunnel, falling back to direct if it cannot start.
- **Disconnects**: next request redials a broken SSH connection; failures back off 1, 2, 4… up to 30 seconds, with requests failing immediately during backoff (socket has its own backoff too). TCP keepalive (after 20 seconds idle, every 5 seconds, 3 probes) detects silent loss; network-unavailable signals immediately close / redial. A channel failing to open within 10 seconds marks SSH dead.
- **Disconnect / unpair** (#2379): disconnecting a group closes all its hub tunnels before registry changes; removing one hub closes only its tunnel; unpair calls `closeAll`, also clearing `HubTransport` routes. Otherwise local listeners, routes, and 120-second `watchDirect` remain (on Mac until quit), rewriting later direct re-pair requests to an old tunnel.
- **Pairing tunnels**: initially dial under ID `pairing`, then `adopt` under the new hub ID after success. State updates resolve the tunnel object's current ID (`SSHTunnels.key(of:in:)`), exposing failures in connection details and **Settings › SSH tunnels**.
- **iOS backgrounding**: close tunnels with socket; foreground reselects routes / rebuilds (about 1 second measured in simulator). Silent-push wakeups build tunnels on demand too.
- **Watch unsupported**: Watch connects directly and cannot tunnel. If hub is SSH-only, direct data-fetch features fail, but pushes unaffected (they do not use this connection). Web / Android currently unsupported.

### Settings

In the app: **Settings › hub › SSH tunnels**, separately per paired hub; before pairing, choose "Connect through SSH tunnel" on the pairing page.

| Setting | Description |
| --- | --- |
| Host, port | SSH server, default port 22 |
| Username | Sent unchanged; Warpgate target login uses `user:target` |
| Tunnel target | Hub address as seen by SSH server; default `127.0.0.1:18660` (hub on this server) |
| Always use SSH | Off: only if direct fails; on: always prefer SSH |

"Copy authorized_keys line" copies a restricted line to paste into that server user's `~/.ssh/authorized_keys`:

```text
restrict,port-forwarding,permitopen="127.0.0.1:18660" ssh-ed25519 AAAA… persona-<device-name>
```

`restrict` disables shell, pty, agent / X11 forwarding, and `~/.ssh/rc`; `port-forwarding` restores only forwarding, `permitopen` permits only the tunnel target. Server `sshd_config` must not use `AllowTcpForwarding no` or `DisableForwarding yes` (they override authorized_keys options). A dedicated restricted user is better: under `Match User <user>`, add `PermitOpen 127.0.0.1:18660`, `X11Forwarding no`, `PermitTTY no`, `ForceCommand /usr/bin/false` (does not affect `-N` forwarding).

"Test connection and save" logs in and opens one target-forwarding channel, saving only on success.

### Host keys

On first connection, the app asks confirmation of server fingerprint (`ED25519 SHA256:…`, matching `ssh-keygen -lf <host-public-key>`), then pins it. Changed fingerprints always refuse connection; Settings / connection details say "SSH server fingerprint changed; connection refused." Accept only after the user selects "Trust new fingerprint" in the old / new comparison opened by "Test connection and save"; no silent replacement. Changing SSH host / port requires confirmation again.

Supported algorithms (swift-nio-ssh): key exchange `curve25519-sha256`, `ecdh-sha2-nistp256/384/521`; Ed25519 / ECDSA host keys (**no RSA host keys**); AES-128-GCM / AES-256-GCM encryption (OpenSSH aes128-gcm / aes256-gcm). Current OpenSSH defaults include these.

### Security notes

- **Tunnel terminates at the SSH server**: hub sees the SSH server as source (loopback if colocated, otherwise its LAN address), **treated as LAN**: main token, plaintext HTTP, full health fields available ([source classification](#来源判断与反向代理)).
- Acceptable after SSH authentication: only holders of this device key allowed by the server can reach hub; SSH is encrypted and the app pins the host key. But **the server-side key must be restricted with `permitopen` to hub's port**. An unrestricted key can jump to any LAN address or open a shell. The app-provided authorized_keys line includes restrictions; do not paste only "Copy public key."
- Plaintext HTTP exists only on device loopback (inside app / local machine) and SSH-server-to-hub hop (loopback stays on server; LAN target uses LAN, same as direct LAN access).
- Lost device: remove its `persona-<device-name>` line from server authorized_keys and revoke hub credentials in **Settings › Devices**.

### Troubleshooting

| Symptom (Settings / connection details) | Check |
| --- | --- |
| Cannot connect to SSH server | Host / port, device reachability (try another network) |
| First connection to this SSH server requires fingerprint confirmation | **Settings › hub › SSH tunnels › Test connection and save** |
| SSH server fingerprint changed; connection refused | Verify `ssh-keygen -lf` on server; trust only after confirming intentional key change, otherwise do not connect |
| SSH server rejected this device's public key | Correct user's authorized_keys, permissions (`StrictModes`), Warpgate public key for this user |
| SSH server cannot forward to hub (administratively prohibited) | `permitopen` address differs from target, or server prohibits TCP forwarding |
| SSH server cannot forward to hub (connect failed) | Wrong target from SSH server's perspective, or hub not listening there |
| SSH handshake failed (no common algorithm) | RSA-only host key or chacha20-only server; add Ed25519 host key and enable AES-GCM |

## Connecting to hub through an in-app tunnel

When hub lives on a private network the phone must join first, and access to that network can be packaged as a library, the iOS app can link it and establish an in-process tunnel. Only Persona's hub traffic uses it; no system VPN is created and other apps are unaffected.

```text
App ──HTTP / WebSocket──▶ 127.0.0.1:<port> (tunnel-core forwarding, inside app process)
     ══Tunnel core (userspace)══▶ Private network ──TCP──▶ hub (target_host:target_port)
```

- **Availability**: hub has an installed, enabled extension contributing `tunnels` ([extensions.md](../extensions/extensions.md), "In-app tunnels"). The tunnel name (its Settings row title), `target_host`, and `target_port` come from the extension. The app refreshes them on every hello and stores them in pairing information.
- **Core outside the repository**: a directory on the build machine, selected by `app.tunnel_core` in `config.json` ([config.md](config.md)). `scripts/persona_config.py xcconfig` writes it to `Config.xcconfig`; only physical-device builds link it. Without configuration, the feature is absent: simulator and unconfigured builds show "This app build has no private-network tunnel core" on hub's tunnel page and skip it during routing.
- **Routing** matches SSH tunnels: direct first, tunnel only if all fail, retry direct every 2 minutes while tunneled. "Always use tunnel" prefers the tunnel. If the same hub also has SSH configured, try SSH first.
- **Connect only on demand**: start the core on first use, never connect automatically, and disconnect after the last forward closes. Disconnect when iOS backgrounds; reroute on return.
- **Sign-in**: accounts and nodes belong in the core's own control panel (loopback web page, **Settings › hub › Tunnel › Sign in and select node**). Data stays in app `Application Support/tunnel`, excluded from backups and never uploaded to hub. The panel opens sign-in outside the app. If the user copies a link externally and returns, the app offers clipboard text to the core (`PersonaTunnelOfferLink`); only recognized text is accepted, then the panel refreshes.
- **Security**: hub sees an address from the tunnel-side network and treats it as LAN ([source classification](#来源判断与反向代理)), just like a direct connection from that network.

### Core directory and C API

The directory selected by `app.tunnel_core` contains:

| File | Description |
| --- | --- |
| `libpersonatunnel.a` | Static library for arm64 iOS devices |
| `module.modulemap` | Defines Swift module `PersonaTunnelCore`, referring to the header below |
| Header | Declares the functions below |

Link flags: `-lpersonatunnel -lresolv -framework Security -framework CoreFoundation`. All functions block; the app calls them off the main thread. Free returned `char *` with `PersonaTunnelFree`; an `error:` prefix indicates failure.

| Function | Description |
| --- | --- |
| `int PersonaTunnelAPIVersion(void)` | API version, currently 1; do not use if it differs from the app |
| `char *PersonaTunnelStart(char *dataDir, char *probeHost, char *probePort)` | Start core without connecting; returns control-panel URL with a one-time token. Probe `probeHost:probePort` to determine tunnel connectivity |
| `char *PersonaTunnelConnect(int timeoutSeconds)` | Connect, or leave an existing connection unchanged; without sign-in returns `error: not signed in` |
| `void PersonaTunnelDisconnect(void)` | Disconnect, retaining core and sign-in data |
| `char *PersonaTunnelStatus(void)` | JSON: `state` (e.g. `connected`), `logged_in`, `has_server`, `server_name?`, `error?`, `needs_mfa?` |
| `char *PersonaTunnelForward(char *target, int port)` | Open loopback forwarding through the tunnel to `host:port`, preferring `port` (0: any), returning `127.0.0.1:<port>` |
| `void PersonaTunnelStopForward(char *target)` | Close forwarding to `target` |
| `char *PersonaTunnelOfferLink(char *text)` | User-provided external text; returns `accepted` if the core used it to complete a sign-in step, otherwise `ignored` |
| `void PersonaTunnelFree(char *p)` | Free strings returned above |

## Connecting to hub through a local SOCKS5 proxy

When the hub sits on a private network this device reaches only through another app that serves a local SOCKS5 proxy (for example a VPN-like client listening on `127.0.0.1:<port>` instead of taking the system VPN slot), the iOS and macOS apps can go through that proxy (#1248). Only Persona's hub traffic uses it.

```text
App ──HTTP / WebSocket──▶ 127.0.0.1:<port> (inside app process)
     ══SOCKS5 (one CONNECT per connection)══▶ local proxy (another app) ──TCP──▶ hub (hub address, default the paired address)
```

- **Settings**: **Settings › hub › Local Proxy**, per paired hub: proxy host (default `127.0.0.1`) and port, optional username and password (RFC 1929), the hub's address as the proxy's network sees it, and **Always Use the Proxy**. The hub's address defaults to the paired address's host, with its port for an `http` address (80 when it names none) and the hub's HTTP port 18660 for an `https` one.
- **In-process forward** (`apple/Persona/App/LocalProxies.swift`, SOCKS5 client in `apple/Persona/App/SOCKS5.swift`): the app listens on a loopback port (the same one next time when it is free); each connection made there dials the proxy, does one SOCKS5 CONNECT to the hub's address, then copies bytes both ways. Nothing is read from the connection before the proxy has connected, and bytes the hub sends right after the proxy's reply stay on the connection. The forward carries plain HTTP, like the SSH and in-app tunnels, and `HubTransport` sends REST, WebSocket and port-forward traffic to it. A host name goes to the proxy unresolved (address type domain), so the proxy's network resolves it.
- **Routing** matches the other tunnels (`HubWay` in `apple/Persona/App/HubTunnels.swift`): direct first, the proxy only if every direct address fails, retry direct every 2 minutes while proxied; **Always Use the Proxy** prefers it and goes direct when it can't connect. Each time the way is picked, the proxy is asked for the hub once, so a proxy whose app quit is not taken (that asks only the proxy: many proxies, such as `ssh -D`, mihomo and Xray, accept a CONNECT before they try the address). A hub with several ways tries the SSH tunnel, then the local proxy, then the in-app tunnel. Settings saved while an earlier start is still under way replace it.
- **Test Connection and Save** connects through the proxy to the hub's address, then sends `GET /api/v1/health` through a forward of its own, without following redirects (they would leave the proxy). It saves only when the hub answers `{"ok": true}` with its `version`, which the hub leaves out for a source it counts as the internet, where it refuses plain HTTP; and, once the app knows the paired hub's `hub_id`, only when that hub answers.
- **Credentials** are kept with the pairing in the keychain (`PairedHub.localProxy`), like the SSH tunnel's settings.
- **iOS backgrounding**: forwards close with the socket and the way is picked again on return. The watch connects directly.
- **Not supported**: HTTP CONNECT proxies, SOCKS4, UDP.
- **Security**: the forward carries plain HTTP, even for an `https` pairing, so the proxy sees Persona's traffic to the hub and this device's credential in it. What is trusted is whatever process holds that loopback port at the time (SOCKS5 does not authenticate the proxy), so use it only with a proxy app you trust, as with a VPN app. A proxy host that is not on this device gets a warning on the page: the traffic and the proxy's password would cross the network unencrypted. The forward's own loopback port has no access control either (as with the SSH tunnel's). The hub classifies the source address the proxy's network gives it as usual ([source classification](#来源判断与反向代理)).

| Symptom (Settings › hub › Local Proxy, connection details) | Check |
| --- | --- |
| Can't reach the proxy at … (Connection refused) | The proxy app is running and its SOCKS5 port is the one set |
| … didn't answer as a SOCKS5 proxy | The port is the proxy's SOCKS5 port, not its HTTP proxy port |
| The proxy asks for a username and password / didn't accept the username or password | Username and password on this page |
| The proxy couldn't connect to … (connection refused, 5) | The hub's address and port as the proxy's network sees them, and that the hub listens there (proxies that wait for the connection before answering) |
| Got no answer from … through the proxy | The same, for proxies that accept the CONNECT first and then close it |
| Reached … through the proxy, but it doesn't look like a hub | The address is not the hub's HTTP port (an HTTPS port, a redirect to https, another service) |
| The hub at … treats connections through the proxy as coming from the internet | The proxy reaches the hub at a public address: use its address inside the LAN or VPN |
| … is another hub, not the one this device is paired with | The hub's address points at another hub |

DEBUG builds: `-seedLocalProxy <base64 of the LocalProxyRoute JSON>` sets the first hub's proxy, and `-tab settings -openLocalProxy YES` opens its page (screenshots).

<a id="网络排查"></a>

## Network troubleshooting

Run `personad hub doctor` on the hub machine first. Read-only: checks hub.json plaintext `listen`, `public.tls_listen`, `security.allow_public_plaintext`, TLS certificate; asks running hub for candidates; lists each local IPv6 as publishable / excluded with reasons; on Linux checks public-IPv6 routes through TUN / virtual interfaces and ufw / nft rules; ends with next steps: which TLS port to allow, which URL to test on cellular. **No external reachability probes**: local access to its own address cannot prove public ingress.

TUN proxy automatic routes may send public-connection responses into the proxy, causing timeout or interrupted handshakes. **Not measured**. On a hub authorized for troubleshooting, inspect IPv6 routes to the phone source and capture on physical / TUN interfaces: did requests arrive, did replies leave correctly? Temporarily disable TUN for comparison. If implicated, preserve direct reply routing for inbound connections in proxy / policy routing; `trusted_cidrs` cannot fix return routes.

| Symptom | Check |
| --- | --- |
| Works on same network, cellular timeout | Phone IPv6; modem / router / host ingress rules; expired advertised addresses; TUN return route |
| `403 tls_required` | TLS port + HTTPS / WSS; never send tokens to public HTTP |
| `403 master_token_local_only` | Use temporary pairing or device tokens publicly |
| `429 rate_limited` | Wait `Retry-After`, stop repeated reconnects with invalid tokens |
| Public-key mismatch | Changed key, wrong hub, TLS-terminating proxy; re-pair through a trusted channel, never skip pinning |
| `hub pair` cannot connect locally | Hub running, HTTP supports IPv4 loopback, command configuration matches running instance |
| TLS inaccessible after enabling `security.proxy_protocol` | `proxy protocol: refused …` logs: upstream not sending headers, or direct bypass ([PROXY protocol](#proxy-protocol)) |

<a id="命令行验证"></a>

## Command-line verification

For self-signed certificates, pin a trusted public key before calling APIs. curl `sha256//` uses standard base64, not QR base64url. Set `FP` to a fingerprint from trusted scanning / local `hub pair`, `TOKEN` to the device token, and run on your own external test machine:

```sh
PIN=$(printf '%s' "$FP" | tr '_-' '/+')=
curl --pinnedpubkey "sha256//$PIN" --insecure \
  -H "Authorization: Bearer $TOKEN" \
  https://hub.example.com:18662/api/v1/endpoints
```

Here `--insecure` skips system CA / hostname checks, while `--pinnedpubkey` still verifies the public key; use both together. Never obtain `FP` from the same unverified public request. IPv6 literal URLs require brackets, e.g. `https://[2001:db8::1234]:18662` (documentation address, not reachable). Response fields: [protocol](../protocol/protocol.md#候选地址).
