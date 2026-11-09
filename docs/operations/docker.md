# Running the hub with Docker (NAS and similar)

The product is Persona. The migrated version's binary is `personad` (`personad` still works), and the default runtime directory is `~/.persona` (`~/.persona` still works). The `.env` / compose variables in this doc keep the old names used in the actual templates; migrated `PERSONA_*` variables take precedence, and `PERSONA_*` keep working. The user in the image, the database role / database name, containers, and persistent volumes must not be renamed after the product; see [Configuration and compatibility](config.md#命名与兼容).

There are two ways to deploy the hub, with the same features. Pick one based on the hub machine:

| Method | Suits | Install and upgrade |
| --- | --- | --- |
| **Native binary** (`docs/server/server.md` ("Deployment")) | A Linux server; the hub machine also runs Agents itself (`kind: local`) | `./scripts/deploy/deploy.sh` from a dev machine (`systemd --user` service + Postgres container); or install the binary with `install.sh` and set up the service yourself. To upgrade, run `deploy.sh` again |
| **Docker** (this doc) | A NAS (Synology, QNAP, Unraid…) or any machine that is only a hub | `docker compose up -d` (two containers: hub + Postgres). To upgrade, `docker compose pull && docker compose up -d` |

Both run the same `personad` hub; `hub.json`, the database, the protocol, and the App are identical. Switching from one to the other only means moving `~/.persona` and the database.

## How the hub in a container differs

The image has no herdr and no Claude Code, so **the hub machine can't also be a computer that does the work**:
- The `hub.json` written on first start has `"machines": []` (not the native method's "one local machine, `kind: local`"); every computer has to be added.
- The "Persona" assistant runs on one of your computers: set `assistant.machine` to its id in the built-in manager's `data/managers/assistant/manager.json` (`docs/operations/config.md` ("manager.json"); if unset, the first entry in `machines` is used).

Extensions (`docs/extensions/extensions.md`) work as usual; installed ones live in `data/extensions/` and survive an image upgrade. The image lacks a few things the native method has:
- No `bwrap`: an extension that would get a sandbox by default (one not installed from a trusted market, `docs/extensions/extensions.md` ("Sandbox")) still runs, just without a sandbox, and its details and the list say 没有沙箱 (no sandbox);
  it can then read and write all of `data/` (including `hub.json`), so install only ones you trust.
- No `git`: git markets (`git:`, `git@…`, `ssh://…`) can't be added; `github:` and `https://` markets go over HTTPS and work as usual.
- The image is Alpine (musl): TypeScript extensions packed by `personad extension pack` (bun's `bun-linux-*` targets, which need glibc) won't start; Go extensions (`CGO_ENABLED=0`) are unaffected.
- No interpreters such as node or python: an extension whose command is an interpreter (e.g. one run with `node` in the manager's `manager.json`) won't start; write and pack extensions (`personad extension dev` / `pack`) on another computer.

The Lark gateway comes in through the Lark extension, and its Agent token and address are set in that extension's settings. The image has no docker CLI, so the option that reads the token by container name doesn't work; enter the token directly.
For the address use the NAS's LAN address, not `127.0.0.1` (loopback inside the container is the container itself). `lark.token` / `lark.container` in hub.json no longer take effect (`docs/operations/config.md`).

How computers connect:
- **tailcat (recommended)**: the NAS doesn't need to ssh into any computer, and computers can be on any internal network (`docs/server/server.md` ("tailcat hole punching")).
- **ssh**: mount the NAS user's `~/.ssh` read-only into the container (there's a commented-out line in compose). It must contain the private key, the aliases in `config`, and `known_hosts`
  (the hub uses `BatchMode=yes`, so an unknown host just fails instead of prompting).

## The hub and the built-in manager in one container

The hub container runs two programs side by side: the hub (`persona-hub run`) and the built-in manager (`persona-manager assistant` with `PERSONA_ASSISTANT_DEPLOY=service`, [managers.md](../assistant/managers.md) ("The built-in manager as its own service")). Neither starts the other. The image's command goes through `personad supervise` (`server/internal/supervise`), a small supervisor that does the following:

- **Mode check.** It reads `assistant.mode` in `hub.json` when the container starts. A new install's `hub.json` (written on first start) says `service`. Without it (a `hub.json` from before, or `child`), or with `assistant.external`, the supervisor becomes the hub itself (exec), and the hub starts the built-in manager as its child, exactly as before.
- **Binding.** Before it starts the built-in manager, it binds it unless its binding works. The binding is a one-time code made in the hub's database (as `persona-hub manager-code` does), traded over loopback once the hub answers its health check. A failed binding is logged and tried again after 10 seconds, the wait doubling up to 5 minutes while it keeps failing; the hub runs meanwhile.
- **Restarts.** A program that stops is started again, and the other is left running. The pause before the restart starts at 1 second and doubles, up to 30 seconds, while the program keeps stopping within a minute of its start. A hub that exits on purpose (the admin page's restart) comes back the same way. Each program runs in a session of its own. When it stops, even after a crash, whatever is left in that session is killed, its extensions' programs too, so that the program started again doesn't find the old ones still running.
- **Stopping.** On `docker stop` both get SIGTERM and are killed if still running after the hub's shutdown budget and a second more: 9 seconds by default, inside Docker's own stop timeout (10 seconds). With a longer `shutdown_timeout_seconds` in `hub.json`, raise compose's `stop_grace_period` with it, or Docker kills the container first. The supervisor reads it when the container starts, so a change takes effect with the container's next start.
- **Logs.** `docker compose logs hub` has both programs' lines, interleaved as they come. The supervisor's lines start with `supervise:`.

**Older installs keep the hub's child** until you switch. Switch with `docker compose exec hub personad hub manager-mode service`, then `docker compose restart hub`. Step by step, with backups, checks and the way back: [upgrade.md](upgrade.md). To go back, use `manager-mode child` and restart. The supervisor decides when the container starts. If the hub starts again on its own (the admin page's restart) after the mode or its `listen` address changed in `hub.json`, the supervisor stops everything and says why, so that Docker starts the container again and the new mode takes effect.

**Going back to an image from before this one** on an install in service mode (every install made with this image) leaves nothing to run the built-in manager: that image's hub doesn't start it, and it has no supervisor. First `docker compose exec hub personad hub manager-mode child`, then change the image.

### Programs

The image carries `persona-hub` and `persona-manager` beside `personad`, all of one build ([server.md](../server/server.md) ("Programs")). The supervisor runs the hub as `persona-hub run`, also when it becomes the hub (then the hub starts its child as `persona-manager assistant`), and the built-in manager as `persona-manager assistant`, here and in a container of its own. One that is not there, or answers `version -v` otherwise than `personad`, is passed over, the second with a `supervise:` line saying why, and `personad` runs that part (`personad hub run`, `personad assistant`).

`PERSONA_SPLIT_PROGRAMS=0` in `.env` keeps both on `personad`, as older images ran them; compose passes it to the hub's container (and to the commented `manager` service). Then `docker compose up -d`. Commands you run yourself (`docker compose exec hub personad …`) are `personad`'s either way, and the hub, its data and its binding are the same with either program. `docker compose exec hub ps -o args` shows which programs run.

### The built-in manager in a container of its own

`compose.yaml` has a commented `manager` service: the same image running `supervise --manager-only --hub http://hub:18660`. To use it:

1. Uncomment it.
2. Set `PERSONA_BUILTIN_MANAGER: separate` on the hub service, so that the hub's container runs the hub alone.

It shares the hub's data folder (mounted at both of the hub container's paths) and database. It waits while `hub.json` doesn't say `service` (read again before each start), and binds itself to the hub over the compose network (a private address, which the hub counts as local). Like the side-by-side setup, it restarts the manager when it stops. Two built-in managers never run on one data folder: the second one stops with exit status 2 (a lock in `data/managers/assistant/`), and its supervisor tries it again later.

## Image

`ghcr.io/<owner>/persona` (the image name follows the repo: `<owner>` is the account or organization that owns the repo, `persona` is the repo name; put it in `PERSONA_IMAGE` in `.env`), linux/amd64 and linux/arm64, built by `.github/workflows/release.yml`:

| Tag | When it exists |
| --- | --- |
| `<version>` | Push a `v*` tag (`v1.2.0` → `1.2.0`), which also creates the tag's GitHub Release; see `docs/operations/release.md` ("Publishing a release") |
| `latest` | Same, but prerelease tags (containing `-`, e.g. `v1.3.0-rc1`) don't move it |
| `edge`, `sha-<commit>` | Run this workflow manually in Actions (current branch) |

Every run first builds the amd64 image and runs a smoke test with compose (health check, pairing, tailcat key, web app, owner and permissions of `hub.json`); only if that passes is it pushed.

The repo is private, so images on ghcr are private by default too: on the NAS, first run `docker login ghcr.io` (username is the GitHub account, password a Personal Access Token with
`read:packages`; if the NAS's Docker config isn't in the default location, use `docker --config <dir> login`). You can also make it public in the repo's
Packages settings. Postgres uses `postgres:18-alpine` from Docker Hub: on networks that can't reach Docker Hub (common in mainland China), set
`PERSONA_POSTGRES_IMAGE` in `.env` to the same image on a registry mirror or local registry (e.g. `<registry>/library/postgres:18-alpine`).

The image contains:
- `personad` for the image's own platform (`linux/<arch>`, in `/opt/persona/nodes`, `PERSONA_NODE_BUNDLED`): the hub runs it, and **Redeploy** sends it to computers of that platform.
  An image built with a release to fetch from (the build argument `UPDATE_URL`) carries no node builds for the other platforms (macOS, the other Linux architecture) and no persona-node: they come from the release, see "Node builds" below. One built without it carries personad for all four platforms, as before;
- the web app embedded in the binary (`docs/web/web.md`): just open the hub's address;
- the built-in extensions' programs (the WeChat bot, `docs/extensions/wechat.md`), the same version as personad;
- `openssh-client`, certificates, time zone data.

The personad in the image ships no skills (like every binary): the assistant's skills live in `~/.persona/skills/` on the machine that runs the assistant (`docs/operations/hub-update.md` ("Taking skills out of the binary")).
The hub in the image is on the `docker` channel: it never replaces its own binary, and prompts you to pull when a new image exists (see "Upgrading"). At build time the image name (`ghcr.io/<owner>/<repo>`) is written into the binary;
`PERSONA_IMAGE` passed in by compose takes precedence.

## Install

To be clear first: **the NAS is only the hub; the AI doesn't work on the NAS**. The image has no Claude Code, so the assistant and Agents all run on another computer of yours. After installing the hub you still need to add a computer (step 5 below); until then the App can pair and browse, but the main chat can't start.

1. On the NAS, create a directory (e.g. `/volume1/docker/persona`) with a `data` subdirectory inside, both created with the NAS's file manager: if `data/` belongs to you, the hub's files belong to you (if Docker creates `data/` itself, it belongs to root, and then all files belong to 1000; see the table below). Put `deploy/docker/compose.yaml` and `deploy/docker/.env.example` in the directory, rename the latter to `.env`, and fill in two lines:
   - `PERSONA_IMAGE`: the image, `ghcr.io/<owner>/persona:latest` (see "Image" above; for a private image, `docker login ghcr.io` first). An older `.env` that has only the image's former variable keeps working. With neither, `docker compose` stops with `service "hub" has neither an image nor a build context specified`.
   - `PERSONA_PUBLIC_URL`: how the phone reaches this NAS, usually `http://<NAS LAN address>:18660`.
2. Run `docker compose up -d` in that directory.
3. Pair the phone: `docker compose exec hub personad hub pair`, and scan the QR code in the terminal with the App.
4. Set an admin password (optional; needed for adding computers and managing devices): open `<PERSONA_PUBLIC_URL>/admin` in a browser and enter the `first-run setup code` line from `docker compose logs hub` (if it expired, get a new one with `docker compose exec hub personad hub admin setup-code`).
5. Add a computer to do the work: on the web app's (open `PERSONA_PUBLIC_URL` in a browser) **Machines** page, click **Add Computer**, or in the admin UI, **Computers › Add Computer**. Follow the steps to install Persona Link on that computer (`docs/operations/link.md`), and once it connects, confirm it is your computer.

Filled in automatically on first start (no need to put them in `.env`; if you do, your value wins):

| What | When unset | Stored where |
| --- | --- | --- |
| Database password (`PERSONA_DB_PASSWORD`) | Generated as 48 random hex characters on postgres's first start (no database yet). If the database already exists but the password file is gone (only `postgres/` restored, `data/` deleted, `.env` rewritten from the new template), it is not generated, and the log reminds you to put the original password in `PERSONA_DB_PASSWORD`. Leaving the password out requires an image built from this version on; if `PERSONA_IMAGE` is pinned to an earlier version, keep writing it | `data/docker/db-password` (0600); the postgres container only mounts this one subdirectory and can't see the hub's secrets. The `personad` in the image reads it on every run (including `docker compose exec`) and puts it into the database URL |
| `PUID` / `PGID` | The owner of the `data/` directory; a directory Docker created itself belongs to root, in which case 1000 is used | The log prints one line saying which is used |
| `hub.json` | A fresh-install default for containers is written (see below) | `data/hub.json` |

`.env` is read by compose on the host and has already been read when the container starts, so the container can't change it. Generated values therefore go in `data/` and are not written back to `.env`. Backing up `data/` includes the password.

On first start (no `hub.json` in `data/` yet), the entry script writes a `hub.json`, and when the hub reads it, it fills in the token and other defaults (same idea as the fresh-install defaults of `personad hub setup`, `docs/assistant/onboarding.md` 4.1; an existing `hub.json` is not changed at all):

| Field | Value | Why |
| --- | --- | --- |
| `public_url` | `""` | In the bridge network the container can't see the NAS's LAN address, and a guessed one (the container's hostname) is unreachable from the phone. `PERSONA_PUBLIC_URL` in `.env` is authoritative; when unset, the log reminds you, `hub pair` also says it needs to be set, or you can fill it in under **Network** in the admin UI |
| `public.tls_listen` | `":18662"` | Persona Link and LAN discovery only use TLS. Inside the container it binds all addresses; which NAS addresses it's exposed on is decided by the compose port mapping (`PERSONA_TLS_PORT`) |
| `admin.first_setup` | `"code"` | Inside the container any private address counts as LAN (see "Admin UI" below); without a setup code, anyone on the same network could set the admin password first |
| `machines` | `[]` | The image has no herdr or Claude Code; computers are all added from outside |

Other notes:
- `PERSONA_PUBLIC_URL` can also be a VPN address, or an HTTPS address from the NAS's built-in reverse proxy. The reverse proxy must forward WebSocket Upgrade / Connection, disable response buffering, and use a longer read timeout (`docs/server/server.md` ("Lightweight hub and pre-deploy checks")).
- Ports: 18660 is HTTP, 18662 is TLS; compose maps both to all NAS addresses by default. The TLS port's host port must stay 18662 (that's what the hub tells the App); to expose it on only one address, write `PERSONA_TLS_PORT=<NAS LAN address>:18662`. If 18662 is already taken on the NAS, `docker compose up` reports a port conflict: give the hub a different `public.tls_listen` port and map it the same way.
- `PUID` / `PGID`: the container starts as root, changes the `persona` user to this uid / gid (any other user in the image with the same uid is removed first, or ssh would find that user's home directory), hands every file in `data/` it doesn't own to it (e.g. an old `~/.persona` copied in with sudo), then drops privileges;
  `docker compose exec hub personad …` also runs as this user, so the files it creates don't belong to root. It can't be 0. Don't set the user with compose's `user:`:
  then the entry script can't change the user, PUID / PGID have no effect, and ssh won't run (the log has a warning line).
- If you set the database password yourself, use only letters and digits (it goes into the database URL as-is); `openssl rand -hex 24` works. Postgres only uses it when the data directory is first created;
  changing `.env` afterwards (or deleting `data/docker/db-password`) makes the database unreachable. To change it, also run `ALTER USER persona PASSWORD '…'` in the database.
- The hub container has `init: true`: when an ssh started by personad is killed, its ProxyCommand / ProxyJump children are reaped by init, so no zombie processes are left.
- Directories: `data/` is the hub's `~/.persona` (`hub.json`, the built-in manager's `managers/assistant/manager.json`, secrets, tailcat key, the knowledge base `data/data`, the generated database password `data/docker/`), `postgres/` is the database. Both sit next to compose,
  visible in the NAS's file manager; back up these two directories. `postgres/` holds both the hub's database and the built-in manager's (`persona_manager`; `jarvis_manager` on an install still on `jarvis`, [managers.md](../assistant/managers.md) ("The built-in manager's database")): removing it removes the manager's data too. `hub.json` and the secret files are 0600.
- An existing deployment (`.env` has `PERSONA_DB_PASSWORD` and `PUID` / `PGID`, `data/` has `hub.json`) behaves the same after switching to the new `compose.yaml`: it uses the password from `.env`, generates nothing, and doesn't change `hub.json`. The only additions are an empty `data/docker/` directory and the 18662 port mapping (nothing listens on it when `hub.json` hasn't enabled TLS).

You can also connect a computer without the App, via tailcat (when the computer is on another internal network that the NAS can't reach):

```sh
docker compose exec hub personad hub tailcat-key          # on the NAS: the public key the computers trust
~/.persona/bin/personad node tailcat init --hub <public key>     # on that computer: prints its address
./scripts/deploy/node-launchd.sh install                        # on that computer: persistent node
```

Add the address to `machines` in `data/hub.json` (`{"id":"laptop","name":"Laptop","kind":"tailcat","tailcat_addr":"tc…"}`),
then `docker compose restart hub`.

## Environment variables (override hub.json, not written back to the file)

| Variable | Purpose |
| --- | --- |
| `PERSONA_DATABASE_URL` | Database URL (assembled in compose, pointing at the `postgres` service; when `.env` has no password, the `personad` in the image fills it in from `data/docker/db-password`) |
| `PERSONA_PUBLIC_URL` | The address the phone uses to reach the hub, which is what the pairing QR code contains; when unset, `public_url` from `hub.json` is used (written empty on first start) |
| `PERSONA_DB_PASSWORD` | Used by compose: the database password; when unset it's generated on first start, see "Install" above |
| `PERSONA_LISTEN` | The address the hub listens on (default `:18660`; changing it also requires changing the compose port mapping and the image's health check) |
| `PERSONA_LOCAL_NODE=0` | Don't write the "this machine" entry on first start (already set in the image) |
| `PERSONA_ADMIN_ACCESS`, `PERSONA_ADMIN_CIDRS` | Who can open the admin UI (`/admin`): `loopback` / `lan` / `cidrs` (comma-separated ranges) / `any`; see "Admin UI" below. When unset, the entry script prints a reminder and uses `admin.access` from hub.json (`lan` if not set) |
| `PERSONA_NODE_BINARIES` | Where node builds of the hub's version for **Redeploy** are put (default `~/.persona/bin/nodes`, in the image `data/bin/nodes` of the data volume); see "Node builds" |
| `PERSONA_NODE_BUNDLED` | The image's own build (`/opt/persona/nodes`, set in the image) |
| `PERSONA_NODE_PACKAGES` | Where the other node builds are fetched from: a release's `dl/` or a mirror of it, `off` for never (overrides hub.json's `node_packages.url`; see "Node builds") |
| `PERSONA_SPLIT_PROGRAMS=0` | Run the hub and the built-in manager on `personad` rather than as `persona-hub` and `persona-manager`; see "Programs" |
| `PUID` / `PGID`, `TZ` | See above (when `PUID` / `PGID` are unset, the owner of `data/` is used) |
| `PERSONA_IMAGE`, `PERSONA_POSTGRES_IMAGE`, `PERSONA_PORT`, `PERSONA_TLS_PORT` | Used by compose: where to pull the two images from, and which host ports the hub's HTTP / TLS ports map to. `PERSONA_IMAGE` is also passed into the hub container: the hub checks its tags for a newer image (see "Upgrading" below) |
| `PERSONA_REGISTRY_TOKEN` | For a private image, a token that can read it (GitHub: a Personal Access Token with `read:packages`); the hub uses it to check for new images. Not needed for public images |
| `PERSONA_POSTGRES_HEALTHCHECK_*`, `PERSONA_HUB_HEALTHCHECK_*`, `PERSONA_POSTGRES_SYNCHRONOUS_COMMIT`, `PERSONA_POSTGRES_WAL_COMPRESSION`, `PERSONA_POSTGRES_CHECKPOINT_TIMEOUT`, `PERSONA_POSTGRES_WAL_WRITER_DELAY`, `PERSONA_LOG_*` | Used by compose: how often the containers are checked, how Postgres flushes its log, and where container logs go; see "Disk writes" below |

The native method also honors these variables (e.g. systemd's `Environment=`), but normally doesn't need them.

## Disk writes

Some hosts pay heavily for every `fsync`: a NAS volume on a single SSD formatted btrfs (copy-on-write, metadata kept twice) turns each one into several metadata writes, so how often things are flushed matters more than how many bytes are written. Two flushers dominate:

- **Postgres commits.** With `synchronous_commit=on` every commit waits for its WAL to be flushed, and a hub in everyday use commits often (one measured install: about 125 commits a minute).
- **Health checks.** dockerd rewrites and fsyncs a container's state file on every health check run (about 1 MB a run on btrfs): the postgres check runs every 5 s, the hub's every 30 s.

`compose.yaml` takes all of these from `.env`. Every default is what compose did before, or PostgreSQL's own default, so nothing changes until you set one; Postgres settings take effect when the container is recreated (`docker compose up -d`), health checks and logging likewise.

| Variable | Default | What it does and what it costs |
| --- | --- | --- |
| `PERSONA_POSTGRES_HEALTHCHECK_INTERVAL` | `5s` | How often `pg_isready` runs. Longer: fewer state rewrites, but a stuck database is noticed later, and the hub's start waits longer, as compose starts it only once postgres has passed a check (`service_healthy`; the first check runs one interval after postgres starts, so `docker compose up` waits about one interval more) |
| `PERSONA_POSTGRES_HEALTHCHECK_TIMEOUT` | `5s` | How long one check may take |
| `PERSONA_POSTGRES_HEALTHCHECK_RETRIES` | `30` | Failed checks in a row before postgres is unhealthy. Interval × retries is how long a slow start (crash recovery included) may take before `docker compose up` gives up on the hub: keep it at a few minutes |
| `PERSONA_HUB_HEALTHCHECK_INTERVAL`, `PERSONA_HUB_HEALTHCHECK_TIMEOUT`, `PERSONA_HUB_HEALTHCHECK_RETRIES` | `30s`, `5s`, `3` | The same for the image's own hub check (its 60 s start period stays). Only `docker ps` and a separate manager container (`depends_on: hub`) look at it |
| `PERSONA_POSTGRES_SYNCHRONOUS_COMMIT` | `on` | `off`: a commit returns before its WAL is flushed and the WAL writer flushes in batches. A crash of the machine or of Postgres can lose the last commits (at most three times `wal_writer_delay`: 0.6 s by default, 3 s with `1s`), which the hub had already answered for. The database stays consistent: nothing is corrupted or half-written, it just comes back as of slightly earlier. A clean `docker compose stop` or restart loses nothing |
| `PERSONA_POSTGRES_WAL_WRITER_DELAY` | `200ms` | How often the WAL writer flushes. With `synchronous_commit=off` this sets how many commits share one flush and how much a crash can lose; with `on` it matters little |
| `PERSONA_POSTGRES_CHECKPOINT_TIMEOUT` | `5min` | Longest time between checkpoints. Longer: fewer checkpoints, and fewer full page images in the WAL after each, at the cost of a longer crash recovery (replaying up to that much WAL, usually seconds on an SSD). Checkpoints still come sooner when `max_wal_size` (1 GB) fills |
| `PERSONA_POSTGRES_WAL_COMPRESSION` | `off` | `lz4` (or `pglz`, `zstd`) compresses the full page images in the WAL: fewer bytes for a little CPU. The official `postgres:18-alpine` image is built with lz4 and zstd |
| `PERSONA_LOG_DRIVER` | `json-file` | The containers' log driver: `json-file` or `local` (Docker's compact format, compressed). `docker compose logs` reads both. Other drivers reject the size options below, so the containers would not start |
| `PERSONA_LOG_MAX_SIZE`, `PERSONA_LOG_MAX_FILE` | `10m`, `3` | Size of one log file and how many are kept per container |

The Postgres settings are passed on its command line (`postgres -c …`), which wins over `ALTER SYSTEM` and `postgresql.conf`: a setting made with `ALTER SYSTEM` is ignored while compose passes the same one. Check what is in effect with `docker compose exec postgres psql -U <user> -c "SELECT name, setting, source FROM pg_settings WHERE name IN ('synchronous_commit', 'wal_compression', 'checkpoint_timeout', 'wal_writer_delay')"` (`source` says `command line`), and drop old ones with `ALTER SYSTEM RESET <name>`. An invalid value stops postgres from starting; `docker compose logs postgres` names it.

The logging block replaces the Docker daemon's default driver for these two containers. Synology's Container Manager defaults to its own `db` driver and its **Log** tab shows only what that driver keeps: with this compose file the tab stays empty, and `docker compose logs` (or `docker logs`) shows the logs instead. Before, the daemon's default applied, which on most Linux hosts is `json-file` without a size limit.

For a write-sensitive host (btrfs, a single SSD, SD cards), these keep the database consistent and cut its WAL flushes to about one a second and the health check rewrites to two a minute:

```sh
PERSONA_POSTGRES_HEALTHCHECK_INTERVAL=60s
PERSONA_POSTGRES_HEALTHCHECK_RETRIES=5
PERSONA_HUB_HEALTHCHECK_INTERVAL=60s
PERSONA_POSTGRES_SYNCHRONOUS_COMMIT=off
PERSONA_POSTGRES_WAL_WRITER_DELAY=1s
PERSONA_POSTGRES_CHECKPOINT_TIMEOUT=15min
PERSONA_POSTGRES_WAL_COMPRESSION=lz4
PERSONA_LOG_DRIVER=local
```

With them a crash can lose up to about 3 s of the hub's last writes, a stuck database or hub shows up within a few minutes, and `docker compose up` waits up to a minute more for postgres.

## Node builds

**Redeploy** (and the update of every computer after the hub's) sends each computer the node build for its OS and architecture, and persona-node to a device that runs it (`docs/operations/node-update.md` ("Node packages")):

1. a build of the hub's version that you put in the data volume's `data/bin/nodes/` (`personad-<os>-<arch>`, as `scripts/release/publish.sh` publishes it, unpacked; a release's `persona-node-<os>-<arch>` is not used from there, as only one of the hub's own version counts, which a release build never reports on an image hub: devices get personad from there instead);
2. the image's own builds (its platform's, or all four in an image built without `UPDATE_URL`);
3. the release of the hub's version from the publisher's site: the image knows where if it was built with one (the build argument `UPDATE_URL`, a release's `dl/`), or `node_packages.url` in hub.json (or `PERSONA_NODE_PACKAGES` in `.env`) says;
4. when there is no release of the hub's version (an `edge` image, or one built with a version of your own; a build with the default `VERSION=dev` never fetches), the newest release whose node this hub drives (the node protocol, `docs/protocol/protocol.md` ("Node handshake")); the computer's card then shows the build it got, and no update is offered while it runs it. Such a build (another version than the hub's) is never sent by itself: a computer on another build is offered it, and gets it when you choose **Redeploy**;
5. otherwise **Redeploy** is not offered, and the card says why.

A device that runs persona-node gets personad into its file when there is no persona-node of the build it should run; it does everything persona-node does.

Fetched builds are checked before they are used (the release manifest's sha256 of the archive and of the binary, over HTTPS; that it is a build of that platform and kind; nothing is signed, `docs/operations/hub-update.md` ("Security: no signing")) and kept in `data/cache/nodes/` (the hub's version, and other versions until a day after they were last used). Downloads go through `node_packages.proxy` in hub.json when set (an `http://` proxy; the container's `HTTPS_PROXY` is not used). When the site does not answer, the builds found or fetched before are used again.

**Without internet access** (air-gapped, or the publisher's site blocked): put the builds into `data/bin/nodes/` as `personad-<os>-<arch>` (for example from another machine: `curl -fsSLO <site>/dl/<version>/personad-darwin-arm64.gz && gunzip personad-darwin-arm64.gz`, then copy it over and `chmod 755` it; `<version>` is what `docker compose exec hub personad version` prints, the tag's release for a tagged image), and when a build is not the hub's own (a release's, whose version reads like `v1.2.3-<build time>`), put that version in `personad-<os>-<arch>.version` beside it (`"version"` in that release's `manifest.json`), so a computer running it counts as up to date. Or mount a directory of them and point `PERSONA_NODE_BINARIES` at it (in `compose.yaml`'s hub `environment`: `.env` alone does not reach the container); or serve a mirror of the release (`latest.json`, `<version>/manifest.json` and the archives) on your network and set `node_packages.url` (or `PERSONA_NODE_PACKAGES`) to it; it must answer 404 for a file it does not have (any other error counts as the site being away). `PERSONA_NODE_PACKAGES=off` stops fetching altogether.

An image built with `UPDATE_URL` also uses that address for the install commands it hands out: "Add a computer" shows the one-line Linux install (`install.sh --node`), and the manager install command (`docs/assistant/managers.md`) downloads the published personad, which the script says when it is not the hub's own version.

Builds you put in `data/bin/nodes/` win over the image's and the release's, also after you upgrade the image: replace or remove them when you upgrade. A stale one with a `.version` keeps computers on that build and shows them up to date; one without makes every deploy wait for a version the build does not report.

## Upgrading

The hub never replaces its own image; when a new image exists, the App tells you (`docs/operations/hub-update.md` ("Docker")): every hour (or any time via **Check for Updates** in Settings › hub) the hub asks the registry for
the tags of `PERSONA_IMAGE`. If the latest release version (`X.Y.Z`, not counting `-rc`, `edge`, `sha-…`) is newer than its own, the top of the Agents page shows "New hub image: X",
and Settings › hub shows the command to run (copyable):

```sh
docker compose pull && docker compose up -d
```

If `PERSONA_IMAGE` in `.env` is pinned to a version (`…:1.2.0`), `pull` won't fetch anything new: first change the tag to the new version (the App gives the whole line), then run the command above.
Manually built `edge` and `sha-…` images have a `git describe` version that can't be compared, so only the latest release version is shown.
If the image is private and `PERSONA_REGISTRY_TOKEN` isn't set, Settings › hub says it can't check and what to set.
Upgrading from 0.1.1 or earlier, the App won't prompt; switch manually: back then the repo was still called persona and the image was `ghcr.io/<owner>/persona`; from 0.1.2 it's
`ghcr.io/<owner>/persona`, and the old name won't get new versions. Change `PERSONA_IMAGE` in `.env` to the new name and version, replace `compose.yaml` with the new one
(it passes `PERSONA_IMAGE` and `PERSONA_REGISTRY_TOKEN` to the hub, which is what lets later versions be announced), then run the command above.
To set where node builds are fetched from with `PERSONA_NODE_PACKAGES` in `.env` (see "Node builds"), take the new `compose.yaml` as well: an older one does not pass it to the hub (`node_packages` in hub.json works either way).
A new image doesn't change how the built-in manager runs on an existing install. Its `hub.json` keeps the hub's child until you switch (see "The hub and the built-in manager in one container"). An older `compose.yaml` works unchanged with either mode.

## Network

- By default the container is on Docker's bridge network: tailcat hole punching goes through one more NAT layer, but direct connections still work in most cases, and fall back to the relay otherwise. To make direct connections easier, give the hub
  `network_mode: host` (and remove `ports`).
- The hub is HTTP by default and should only be reached on the internal network, over VPN, or behind an HTTPS reverse proxy. Don't expose 18660 directly to the internet.

### Reverse proxy on the same machine

The hub treats its own container's gateway as public internet ([network.md](network.md) ("Source checks and reverse proxies")): plaintext requests from the internet get `403 tls_required` and can't use the main token.
With the two common ways of setting up a reverse proxy on a NAS, the source seen by the hub becomes the gateway, so connect as follows:

- **Proxy in another container** (Caddy, nginx, Nginx Proxy Manager…): don't reach the hub through the host-mapped port (`<NAS address>:18660`). The proxy container is on a different
  Docker network, and when forwarding, the source address is MASQUERADEd to the gateway of the hub's network. Put the hub on the proxy's network too, and have the proxy connect by alias:

  ```yaml
  services:
    hub:
      networks:
        default:            # don't drop this: the hub reaches Postgres through it
        proxy:
          aliases: [persona-hub]
  networks:
    proxy:
      name: <the proxy's network>  # docker network ls
      external: true
  ```

  Write `persona-hub:18660` in the proxy; the hub then sees the proxy container's private address.
- **Proxy running on the host** (the NAS system's built-in reverse proxy): connect to the NAS's own LAN address (`http://<NAS LAN address>:18660`), not `127.0.0.1` / `localhost`:
  the latter goes through docker-proxy and the source is the gateway; the former goes through iptables DNAT and the source is the NAS's own LAN address.
- **Block `/admin` from outside at the proxy**: once set up this way, the hub treats every request coming through the proxy as LAN, and setting the admin password for the first time requires no code from the LAN
  ([admin.md](admin.md) ("First-time setup")). Only let LAN clients open `/admin`, e.g. with Caddy:

  ```caddyfile
  @admin_outside {
  	path /admin /admin/*
  	not client_ip private_ranges
  }
  respond @admin_outside 403
  reverse_proxy persona-hub:18660 {
  	flush_interval -1
  }
  ```

- **Calling hub APIs on the NAS** works the same way: `curl http://127.0.0.1:18660/…` is treated as public internet; use the LAN address instead, or call from inside the container
  (`docker compose exec hub wget -qO- --header "Authorization: Bearer <token>" http://127.0.0.1:18660/api/v1/machines`).

<a id="docker-desktop--orbstack-的明文连接"></a>

### Plaintext connections on Docker Desktop / OrbStack

If the health check is fine but requests from the App, the web app, or an internal reverse proxy return `403 tls_required`, first check the real peer the container sees. Docker Desktop's gvisor networking may show every port-mapped connection as the same public-looking address instead of the Docker network's gateway; local and LAN clients look that way too. The entry script prints a hint about this when it detects a Desktop environment, independently of `PERSONA_ADMIN_ACCESS`.

Keep a client connected and run in the compose directory:

```sh
docker compose exec hub netstat -tn
docker compose logs hub
```

In `netstat`'s local address column, find port 18660 and read the matching **Foreign Address** (drop the port). For short connections, send requests repeatedly and look again. The `Gateway` from `docker network inspect` isn't enough to determine the forwarding source, and the `source` in the auth log is a redacted range, so it can't be used to allow a whole range.

Prefer TLS: a newly installed hub already has TLS on and compose already maps 18662; for older installs, add `public.tls_listen` to `data/hub.json` ([network.md](network.md)). Only for trying out local HTTP:

- First set `PERSONA_PORT=127.0.0.1:18660` in `.env` and run `docker compose up -d` to recreate the port mapping.
- Add the observed remote address to `security.trusted_cidrs` in `data/hub.json` (a single address: `/32` for IPv4, `/128` for IPv6; keep existing entries), then `docker compose restart hub`.
- If you need the admin UI, also set `PERSONA_ADMIN_ACCESS=cidrs` and `PERSONA_ADMIN_CIDRS=<actual forwarding source>/32` (`/128` for IPv6) and run `docker compose up -d`. The admin UI's allowlist and the API's `security.trusted_cidrs` are separate.

When LAN clients need access, you can bind `PERSONA_PORT` to an explicit LAN address and restrict it to controlled clients with the host firewall before trusting the forwarding source. Check Docker's actual forwarding rules to make sure the internet can't get in through that port or a reverse proxy. The forwarding source may also represent internet connections, so adding it to the allowlist just because it's a fixed address would let those through. Keep TLS for the internet, and don't use `allow_public_plaintext` to work around collapsed sources.

## Admin UI

`<PERSONA_PUBLIC_URL>/admin` is the hub's built-in admin UI ([admin.md](admin.md)): set the admin password the first time you open it, then add computers, manage devices, and change networking on the web.

- **Access scope**: chosen in `.env` (`.env.example` explains the four options). Once set, it is authoritative and can't be changed on the web.
- **In a container, `lan` means "any private address", except the gateway**: on a bridge network the container only sees Docker's own range (`172.x`), not the NAS's LAN,
  so it can't tell "same subnet" and is relaxed to any private address. But connections forwarded by Docker's userland proxy (docker-proxy) all show up as the gateway address (`172.x.0.1`),
  including ones from the internet (IPv6 requests when Docker has no IPv6, requests forwarded by a reverse proxy on the host), so the gateway never counts as LAN. On Linux, LAN computers come in through
  iptables DNAT, the container sees their real address, and they can open it as usual. The App's API works the same way: requests from the gateway are treated as internet (can't use the main token; plaintext APIs return `403 tls_required`),
  see [network.md](network.md) ("Source checks and reverse proxies").
- **Docker Desktop / OrbStack**: the forwarding source may be the gateway, or a public-looking address provided by gvisor. Find the real peer as in "Plaintext connections on Docker Desktop / OrbStack" above; for local trial only, bind the port to `127.0.0.1` and put the actual source in `PERSONA_ADMIN_CIDRS`. The API allowlist needs `security.trusted_cidrs` separately.
- **`loopback`** inside a container means the container itself: for uses like `docker compose exec hub wget -qO- http://127.0.0.1:18660/admin`; rarely useful.
- **`any`** requires the hub to have TLS on (`public.tls_listen` in `data/hub.json`; the `hub.json` written on first start already has it, and compose maps 18662; older installs need to add it).
- Forgot the password: `docker compose exec hub personad hub admin reset-password`.
- The **Restart hub** button works in a container: after the hub exits, `restart: unless-stopped` brings it back up.

## Migrating from the native method

1. On the old machine, stop the hub (`systemctl --user stop personad`) and export the database:
   `docker exec persona-postgres pg_dump -U persona -Fc persona > persona.dump`, and the built-in manager's database in the same server: `docker exec persona-postgres pg_dump -U persona -Fc persona_manager > persona_manager.dump`. Both together are the hub's data ([managers.md](../assistant/managers.md) ("The built-in manager's database")); one without the other loses the manager's data or the hub's.
2. Copy the actual runtime directory (the new default is `~/.persona`; existing deployments may still use the compatibility directory `~/.persona`) to `data/` on the NAS (`bin/` can be left out). In `hub.json`, remove the `kind: local` machine (if that machine should keep running Agents,
   change it to `ssh` or `tailcat`). Leave `database_url` alone (the environment variable overrides it).
3. `docker compose up -d postgres`, then import: `docker compose exec -T postgres pg_restore -U persona -d persona --clean --if-exists < persona.dump` (and the manager's: `docker compose exec -T postgres createdb -U persona persona_manager`, then the same `pg_restore -d persona_manager < persona_manager.dump`),
   then `docker compose up -d`. The token is unchanged; paired phones only need the address changed to the new one (or scan again).

## Verification

`.github/workflows/release.yml` builds and smoke-tests the image on explicit workflow dispatch: health check, pairing,
tailcat key, web app, owner of `hub.json`. The workflow passes no `UPDATE_URL`, so the image carries personad for every platform (for the smoke test, `linux/amd64` only); one built with `UPDATE_URL` carries its own platform's and fetches the others ("Node builds").
The only automatic trigger is a `v*` release tag, which publishes the version image and, for stable releases, `latest`, then creates a GitHub Release for the tag. Manual dispatch publishes only `edge`/`sha-*` images and does not create a GitHub Release. Docker installations use the versioned images, so this release publication exception is retained. PRs and branch pushes do not start the workflow.
You can do the same locally (OrbStack / Docker Desktop):

```sh
docker build --build-arg VERSION=dev-local -t persona-hub .
cd deploy/docker && cp .env.example .env    # PERSONA_IMAGE=persona-hub, set PERSONA_PUBLIC_URL to this machine's address
docker compose up -d
```
