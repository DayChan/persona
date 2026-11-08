# Upgrading to the split hub and manager

The hub and the built-in manager now run as two programs. A new install gets them as two services (`personad hub setup`) or as two processes in the hub's container (Docker). An existing install keeps working as it is: the hub still starts the built-in manager as its child (**child mode**) until you switch it to **service mode**, where the built-in manager runs on its own and survives hub restarts. Nothing switches by itself.

This guide takes an existing install to the split layout. A person or an agent can follow it step by step: every step has a check, and a way back. The details are in [managers.md](../assistant/managers.md) ("The built-in manager as its own service"), [docker.md](docker.md) ("The hub and the built-in manager in one container"), [server.md](../server/server.md) ("Programs", "The default manager's own database") and [hub-update.md](hub-update.md).

## Which section is yours

| Your hub | How to tell | Section |
| --- | --- | --- |
| Installed by `personad hub setup` on Linux, with a systemd user instance | `systemctl --user status personad` shows the hub's service | [Native Linux and macOS](#native-linux-and-macos) |
| Installed by `personad hub setup` on a Mac | `launchctl print gui/$(id -u)/com.persona.hub` shows the hub's agent | [Native Linux and macOS](#native-linux-and-macos) |
| Docker (`deploy/docker/compose.yaml`, a NAS) | `docker compose ps` in the compose folder lists `hub` and `postgres` | [Docker](#docker) |
| Run by hand or by your own process manager: `hub setup --no-service`, Linux without a systemd user instance (WSL without systemd, some containers), only the install command without `--hub` | none of the above | [No service manager](#no-service-manager) |
| Windows | The hub doesn't run natively on Windows: it runs in Docker Desktop or in WSL 2 | [Docker](#docker); in WSL 2, [Native](#native-linux-and-macos) with systemd, [No service manager](#no-service-manager) without |

**An install made under the product's former name** keeps former names for its hub service, the binary in `bin/`, its data directory, its Postgres container, and its database and role. Wherever this guide says `personad` (the service or the binary), `~/.persona` or `persona-postgres`, use what your install has; [rolling-upgrade-compatibility.md](../protocol/rolling-upgrade-compatibility.md) lists them. The variables below pick up the data directory; set `PD` from the service's `ExecStart` (below).

## Before you start

**Run the native steps as the hub's own user**, not as root or with `sudo`: the services are that user's. The checks need `curl` and `jq`.

**The values the steps use** (native):

```sh
DIR=${PERSONA_HOME:-$HOME/.persona}                            # the data directory as the services name it
DATA=$(cd -P "$DIR" && pwd)                                     # the same, with symlinks resolved
PD="$DATA/bin/personad"                                         # the binary the hub's service runs
PORT=$(jq -r '.listen // ":18660"' "$DATA/hub.json" | sed 's/.*://')
HUB="http://127.0.0.1:$PORT"
TOKEN=$(jq -r '.token' "$DATA/hub.json")
```

- When the hub's service sets another data directory, use it: `systemctl --user cat personad | grep PERSONA_HOME` (macOS: `plutil -p ~/Library/LaunchAgents/com.persona.hub.plist`). The service's `ExecStart` (macOS: `ProgramArguments`) names the binary for `PD`.
- When `listen` in `hub.json` names one address (`<LAN address>:18660`), the hub doesn't answer on `127.0.0.1`: put that address in `HUB`.

Docker, in the compose folder (`data/hub.json` belongs to the container's user):

```sh
TOKEN=$(sudo jq -r '.token' data/hub.json)
```

**The check this guide uses most:** the built-in manager in the hub's roster (`GET /api/v1/managers`, what **Settings › Managers** shows in the apps). Over plain HTTP the hub takes its own token only from addresses it counts as local, so ask it on its own machine. In Docker, a request through the published port counts as coming from outside (`403 tls_required`), so ask from inside the hub's container.

Native:

```sh
roster() {
  curl -s --max-time 5 -H "Authorization: Bearer $TOKEN" "$HUB/api/v1/managers" \
    | jq -c '.managers[] | select(.id == "assistant") | {online, deploy, version}'
}
```

Docker, instead:

```sh
roster() {
  docker compose exec -T hub wget -qO- --header "Authorization: Bearer $TOKEN" http://127.0.0.1:18660/api/v1/managers \
    | jq -c '.managers[] | select(.id == "assistant") | {online, deploy, version}'
}
```

Both:

```sh
# expect '<text>': waits up to 2 minutes until the roster shows it, prints the roster, fails when it doesn't come.
expect() {
  i=0
  while [ "$i" -lt 120 ]; do roster 2>/dev/null | grep -q "$1" && { roster; return 0; }; sleep 1; i=$((i + 1)); done
  echo "not within 2 minutes: $1"; roster; return 1
}
# managers: one line per built-in manager running on this data directory (native), under either of its paths.
managers() { ps -axo pid=,args= | grep -E "[ ]($DATA|$DIR)/bin/[^ /]+ assistant\$"; }
```

`expect '"online":true,"deploy":"child"'` and `expect '"online":true,"deploy":"service"'` are the two states. Right after a restart the built-in manager can take a moment to come back, which is why `expect` waits. A build from before the split may leave `deploy` empty: check `expect '"online":true'` there.

**Where you are now:**

```sh
"$PD" version                                     # Docker: docker compose exec hub personad version
jq -r '.assistant.mode // "child"' "$DATA/hub.json" # child, unless someone switched already (Docker: sudo jq … data/hub.json)
expect '"online":true'
```

## Backups

Take them before the first step, into one folder:

```sh
BK=~/persona-backup-$(date +%Y%m%d%H%M); mkdir -p "$BK"
```

The ways back below don't restore them: they are for a step that went wrong in a way this guide doesn't cover.

**The databases.** The built-in manager keeps its data in a database of its own, which its `manager.json` names ([server.md](../server/server.md) ("The default manager's own database")):

```sh
jq -r '.database_name // .database_container // "none"' "$DATA/managers/assistant/manager.json" 2>/dev/null || echo none
```

Docker: `sudo jq -r '.database_name // .database_container // "none"' data/managers/assistant/manager.json 2>/dev/null || echo none`.

- `database_name`: a second database in the hub's Postgres server, named after the hub's with `_manager`.
- `database_container`: a Postgres container of its own.
- `none`: the built-in manager's data is still in the hub's database, which a build that has its own database moves at its next start.

Back up every database there is, and restore them together.

- **The hub's Postgres is the container `hub setup` made** (`persona-postgres`):

  ```sh
  docker exec persona-postgres sh -c 'pg_dumpall -U "${POSTGRES_USER:-postgres}"' > "$BK/db.sql"
  grep '^CREATE DATABASE' "$BK/db.sql"
  ```

  **Check:** the hub's database is listed, and so is the name in `database_name` when `manager.json` has one.

- **Another Postgres**, with `database_url` from `hub.json`:

  ```sh
  url=$(jq -r '.database_url' "$DATA/hub.json")
  q=; case $url in *\?*) q="?${url#*\?}" ;; esac
  base=${url%%\?*}; base=${base%/*}
  mdb=$(jq -r '.database_name // empty' "$DATA/managers/assistant/manager.json" 2>/dev/null)
  pg_dump "$url" > "$BK/hub-db.sql"
  [ -z "$mdb" ] || pg_dump "$base/$mdb$q" > "$BK/manager-db.sql"
  ```

  **Check:** each file is there and has `CREATE TABLE` lines.

- **The built-in manager's own container** (`database_container`): `docker exec <that container> sh -c 'pg_dumpall -U "${POSTGRES_USER:-postgres}"' > "$BK/manager-db.sql"`.

- **Docker**, in the compose folder: `docker compose exec -T postgres sh -c 'pg_dumpall -U "${POSTGRES_USER:-postgres}"' > "$BK/db.sql"`. **Check:** `grep '^CREATE DATABASE' "$BK/db.sql"` lists the hub's database, and its `_manager` when `manager.json` names one.

**The data directory.** It has `hub.json`, the binaries you would go back to (`bin/`), the built-in manager's binding and keys (`managers/assistant/`) and the knowledge base:

```sh
tar czf "$BK/data.tgz" -C "$(dirname "$DATA")" "$(basename "$DATA")"     # Docker: sudo tar czf "$BK/data.tgz" data
tar tzf "$BK/data.tgz" | grep -c '/hub.json$'                            # 1 or more
```

**Docker only:** also copy `.env` and `compose.yaml` into `$BK`, and keep the image you run under a tag of your own, so that you can go back to it even when `.env` says `latest`:

```sh
docker tag "$(docker compose images -q hub)" persona-hub:before-split
```

**Restoring** (only when a way back below doesn't bring the hub back): stop the hub and the built-in manager's service if there is one. Put the data directory back (`tar xzf "$BK/data.tgz" -C "$(dirname "$DATA")"`). Restore the databases together, after dropping them or into an empty server: a `pg_dumpall` file with `docker exec -i persona-postgres sh -c 'psql -U "${POSTGRES_USER:-postgres}" -d postgres' < "$BK/db.sql"` (Docker: `docker compose exec -T postgres …`), each `pg_dump` file with the same URL it was taken with (`psql "$url" -f "$BK/hub-db.sql"`, `psql "$base/$mdb$q" -f "$BK/manager-db.sql"`). Then start the hub's service.

## Native Linux and macOS

Restarting the hub's service, used below:

```sh
systemctl --user restart personad                          # Linux
launchctl kickstart -k gui/$(id -u)/com.persona.hub          # macOS
```

`"$PD" hub switch` also works: the hub starts again behind its edge, without a gap (exit 3 means there is no edge: restart the service instead). The steps use `personad` (`$PD`): the services run it, and an install made with the install command doesn't have `persona-hub` and `persona-manager` until its next update. `persona-hub …` and `persona-manager …` are the same commands. Logs: `journalctl --user -u personad -n 100` (macOS: `tail -n 100 "$DATA/hub.log"`).

### Step 1: update to a build with the split

Update as usual: the automatic update, **Update now** in the apps, the install command again with `--hub`, or a direct deploy. The hub's self-update swaps `personad`, `persona-hub` and `persona-manager` together, and rolls them back by itself when the new version doesn't start ([hub-update.md](hub-update.md)). The install command runs `hub setup` again, which keeps an existing install's mode and says how to switch.

**Check:**

```sh
"$PD" hub manager-mode                            # prints: child
"$PD" manager install --builtin --check && echo ready
expect '"online":true,"deploy":"child"'
```

- `manager-mode` must print `child`. An older build doesn't know the command: the update didn't happen.
- `install --builtin --check` must print `ready`. It changes nothing. It fails on a build from before the split (`flag provided but not defined: -builtin`), and on Linux without a systemd user instance (`cannot install its service`); then follow [No service manager](#no-service-manager).
- `hub.json` is unchanged: the same `token`, no `assistant.mode`.

**Way back:** the update's own rollback is automatic. By hand, put `bin/` back and restart the hub's service. Replace each file by a rename, never by writing into it: the running hub's binary can't be written over (`Text file busy`). Put the links in `bin/` back as they were: `bin/` keeps the binary under its former name too, one of the two names a link to the other (which one depends on how the install began), and an update by the install command can turn that around.

```sh
R="$BK/restore"; mkdir -p "$R" && tar xzf "$BK/data.tgz" -C "$R" "$(basename "$DATA")/bin"
for f in "$R/$(basename "$DATA")/bin/"*; do
  if [ -L "$f" ]; then ln -sfn "$(readlink "$f")" "$DATA/bin/${f##*/}"; continue; fi
  [ -f "$f" ] || continue
  cp -p "$f" "$DATA/bin/.restore" && mv -f "$DATA/bin/.restore" "$DATA/bin/${f##*/}"
done
```

When the update left `.prev` copies, `cp -p "$PD.prev" "$PD.new" && mv -f "$PD.new" "$PD"` does the same for `personad`. If `$PD` is itself a link (some installs under the former name), do it on the file the link names.

### Step 2: switch with setup

```sh
"$PD" hub setup --manager-service --json | tee "$BK/setup.json"
jq -c 'select(.step == "manager") | .ok' "$BK/setup.json"      # true
```

Setup checks first that the service can be installed, and changes nothing when it can't. Then it:

1. sets `assistant.mode` to `service`;
2. restarts the hub's service;
3. binds the built-in manager with a one-time code, passed in the environment and never on a command line;
4. installs its service: `persona-manager-assistant.service` on Linux, the launchd agent `com.persona.manager.assistant` on a Mac;
5. waits up to 90 seconds for it on the hub's bus.

Without `--json` it prints one line per step.

**When it fails:**
- **The manager step fails:** setup undoes the switch itself. The mode goes back, the service is removed, the hub restarts into child mode, and setup says so.
- **The service only fails to come online:** setup leaves it and says how to go back.
- **An earlier step fails after the mode was set** (database, service, health): the mode stays `service` with nothing running the built-in manager. Check `"$PD" hub manager-mode`, and take the way back below.

**Check:**

```sh
jq -r '.assistant.mode' "$DATA/hub.json"          # service
systemctl --user is-active persona-manager-assistant   # active; macOS: launchctl print gui/$(id -u)/com.persona.manager.assistant | grep 'state = running'
"$PD" manager status --builtin                    # Binding: hub http://127.0.0.1:<port>, Service: … active
expect '"online":true,"deploy":"service"'
managers                                          # exactly one line
```

- After `systemctl --user restart personad`, `managers` shows the same pid, and `expect '"online":true,"deploy":"service"'` passes again: hub restarts leave the built-in manager running.
- Its log: `journalctl --user -u persona-manager-assistant -n 100` (macOS: `tail -n 100 "$DATA/managers/assistant/manager.log"`).

**Way back to child mode** (same build):

```sh
"$PD" hub manager-mode child
systemctl --user restart personad                  # macOS: launchctl kickstart -k gui/$(id -u)/com.persona.hub
"$PD" manager uninstall --builtin                  # the binding and its data stay
expect '"online":true,"deploy":"child"'
managers                                           # exactly one line
```

`systemctl --user is-active persona-manager-assistant` no longer says `active`. A service left installed doesn't run next to the hub's child: the hub refuses its token and it waits. To go forward again, run step 2 again.

### Step 2, by hand

Instead of setup, with the same result:

1. Set the mode, then restart the hub's service:

   ```sh
   "$PD" hub manager-mode service
   systemctl --user restart personad                # macOS: launchctl kickstart -k gui/$(id -u)/com.persona.hub
   ```

   **Check:** the hub no longer starts its child, and the roster says offline until the next step:

   ```sh
   sleep 30; if [ -z "$(managers)" ]; then echo "no built-in manager runs"; else echo "a built-in manager still runs:"; managers; fi
   expect '"online":false'
   ```

2. Bind and install with a one-time code. It is good for 10 minutes, once, and stays in the environment:

   ```sh
   out=$("$PD" hub manager-code) && code=$(printf '%s\n' "$out" | head -n 1) && [ -n "$code" ] &&
     PERSONA_MANAGER_CODE=$code "$PD" manager join --builtin "$HUB" --install
   ```

   **Check:** as after setup.

**Way back:** as after setup.

### Going back to a build from before the split

1. Go back to child mode (above). A build from before the split doesn't start a service-mode built-in manager, and nothing else would.
2. Put `bin/` back (step 1's way back) and restart the hub's service.

**Check:** `expect '"online":true'`, and `managers` shows one line. The build from just before the split already has the built-in manager's own database and runs on it. An older build, from before that database, finds the manager's data only as it was when it moved: see the known limits. Restore the databases only when the older build doesn't start.

## Docker

The image's command runs `personad supervise`. In service mode it runs the hub and the built-in manager side by side; otherwise it runs the hub alone, which starts its child. Your compose file can stay as it is.

### Step 1: update the image

```sh
docker compose pull && docker compose up -d       # with a version pinned in .env: change PERSONA_IMAGE first
```

**Check:**

- `docker compose ps` shows `hub` healthy.
- `docker compose logs --no-log-prefix hub | grep 'supervise:' | tail -n 1` says `assistant.mode is not service: the hub starts the built-in manager itself, as before`.
- `expect '"online":true,"deploy":"child"'`. `data/hub.json` is unchanged.

**Way back:** the image you tagged (`PERSONA_IMAGE=persona-hub:before-split` in `.env`, or the previous version), then `docker compose up -d`.

### Step 2: service mode

```sh
docker compose exec hub personad hub manager-mode service
docker compose restart hub
```

**Check:**

- `expect '"online":true,"deploy":"service"'`.
- `docker compose exec hub ps -o pid,ppid,args` shows `personad supervise` with two children, `… hub run` and `… assistant`.
- `docker compose logs --no-log-prefix --since 5m hub | grep 'supervise:'` shows `bound the built-in manager to the hub at http://127.0.0.1:18660` the first time.
- When either program stops, the supervisor starts it again and leaves the other running. Its log says so: `supervise: … stopped (…): starting it again in 1s; the other runs on`.

**Way back:**

```sh
docker compose exec hub personad hub manager-mode child
docker compose restart hub
```

**Check:** `expect '"online":true,"deploy":"child"'`, and `docker compose exec hub ps -o pid,ppid,args` shows no `personad supervise`: the hub runs alone, with `… assistant` as its child.

### Optional: the built-in manager in a container of its own

It needs service mode (step 2). The `manager` service is in the `deploy/docker/compose.yaml` of the version you run, commented out. Copy that block into your `compose.yaml` and uncomment it. Keep your own file, with its ports and mounts, rather than replacing it. Then uncomment `PERSONA_BUILTIN_MANAGER: separate` on your `hub` service (add it if your file doesn't have it), and run `docker compose up -d`.

**Check:**

- `docker compose ps` lists `manager`.
- The first time, `docker compose logs manager | grep 'supervise:'` shows `bound the built-in manager to the hub at http://hub:18660`. The loopback binding from step 2 can't reach the hub from that container, so it binds again over the compose network.
- `docker compose logs hub | grep 'supervise:' | tail -n 1` shows `PERSONA_BUILTIN_MANAGER=separate`.
- `docker compose exec hub ps -o args` shows no `assistant`.
- `expect '"online":true,"deploy":"service"'`.

**Way back:** comment both out again, then `docker compose up -d --remove-orphans`.

**Check:** `docker compose exec hub ps -o pid,ppid,args` shows `personad supervise` with both programs again, and `expect '"online":true,"deploy":"service"'`. No new `bound` line appears: the binding over the compose network works from the hub's container too.

### Going back to an image from before the split

1. With the manager container in use, take its way back first. Otherwise it keeps running on the data folder with the newer image.
2. Go back to child mode (step 2's way back). An older image doesn't run the built-in manager in service mode.
3. Put the image you tagged (or the previous version) in `.env`, and the previous `compose.yaml` back if you replaced it. Run `docker compose up -d --remove-orphans`.

**Check:** `hub` is healthy, and `expect '"online":true'`.

## No service manager

A hub run by hand, or by a process manager of your own, has nothing to install the built-in manager's service into. It stays in child mode, which is fully supported:

- **Update** the binaries as you do now, and start the hub again. **Check:** `"$PD" hub manager-mode` prints `child`, and `expect '"online":true,"deploy":"child"'`.
- **Setup won't switch it:** `hub setup --manager-service` can't go with `--no-service`. On Linux without a systemd user instance, `manager install --builtin --check` fails. Nothing changes either way.

**Optional: run the built-in manager yourself.** Run it under a process manager of your own, so that it is restarted when it stops.

1. Set the mode with `"$PD" hub manager-mode service`, then start the hub again. **Check:** as in step 2 by hand above: after half a minute `managers` still prints nothing, and `expect '"online":false'`.

2. Bind it, without installing a service:

   ```sh
   out=$("$PD" hub manager-code) && code=$(printf '%s\n' "$out" | head -n 1) && [ -n "$code" ] &&
     PERSONA_MANAGER_CODE=$code "$PD" manager join --builtin "$HUB"
   ```

3. Run it:

   ```sh
   PERSONA_ASSISTANT_DEPLOY=service PERSONA_HOME="$DATA" "$PD" assistant
   ```

   Restart it on any exit except 2. Exit 2 means it isn't bound, or another one already runs on this data directory, and restarting won't help.

**Check:**

- `expect '"online":true,"deploy":"service"'`.
- `managers` shows one line, and the same pid after a hub restart.
- A second one exits 2. On Linux and macOS, a lock in `managers/assistant/` keeps it out.

**Way back:** `"$PD" hub manager-mode child`, start the hub again, then stop the one you run. **Check:** `expect '"online":true,"deploy":"child"'`, and `managers` shows one line.

## Verify after

Whatever the type, the upgrade is done when all of these hold:

- `GET /api/v1/health` answers 200, and the apps connect.
- `expect` passes for the mode you chose, and **Settings › Managers** in the apps shows the built-in manager online.
- Exactly one built-in manager runs per data directory: `managers` natively, `ps` in the hub's container in Docker.
- Neither its log nor the hub's shows a restart loop. In Docker the supervisor logs each stop and why; natively, the service's own log does.
- The main chat answers a message.

## Known limits

- **Nothing switches by itself.** Existing installs stay in child mode until switched, and child mode stays supported.
- **The services still run `personad`** (`personad hub run`, `personad assistant`). A later release moves them to `persona-hub` and `persona-manager`.
- **The first update that brings `persona-hub` and `persona-manager`:** if it rolls back, only `personad` goes back, because the old watchdog doesn't know the two. They stay behind, don't match the hub's version, and aren't started until the next update. The install command installs `personad` alone; the next update brings the other two.
- **A service left installed after going back to child mode** waits, refused by the hub, instead of running next to its child. Short overlaps remain while a refused service shuts down and a child starts. Remove it with `manager uninstall --builtin`.
- **One built-in manager per data directory.** A second one exits 2, kept out by a lock on Linux and macOS. In Docker, if the hub's container still runs its own (no `PERSONA_BUILTIN_MANAGER: separate`) while a manager container runs, one of them keeps exiting 2 and trying again.
- **A built-in manager that runs elsewhere** (`assistant.external`) is not switched: `--manager-service` refuses it and changes nothing.
- **Builds from before the built-in manager's own database:** they find the manager's data as it was when it moved to its own database. Anything written after that is not there for them. Forward again, the newer build runs on its own database as it was ([server.md](../server/server.md) ("The default manager's own database")).
- **New binaries:**
  - The built-in manager's service picks up a new `personad` by itself within a minute, once it is idle. It waits at most 30 minutes, and an extension's live view left open holds it that long.
  - Managers made in the app run their old build until their service restarts.
- **Docker:**
  - The programs get the hub's shutdown budget and a second more to stop: 9 seconds by default. A longer `shutdown_timeout_seconds` needs a longer `stop_grace_period` in compose, and the supervisor reads it when the container starts.
  - With Docker Compose 2.39, `compose.yaml`'s `image:` line also needs the image variable under its former name set in `.env`, not only `PERSONA_IMAGE`.
- **No service manager, and Windows:** the built-in manager's service needs a systemd user instance (Linux) or launchd (macOS). Elsewhere the hub keeps its child, or you run the built-in manager yourself (above). Windows computers (Persona Link and their nodes) are not affected by the split.
