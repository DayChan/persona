#!/bin/bash
# Runs on the hub machine (deploy.sh and scripts/test/test-server.sh pipe it over ssh): make sure the hub's Postgres is
# up and that ~/.persona/hub.json has database_url. Idempotent; the password is generated once and lives only in
# hub.json (0600).
#   a new hub: container persona-postgres · image postgres:18-alpine · 127.0.0.1:18661 · volume persona-pgdata
# It never replaces a database (#6149). A database_url already in hub.json is never rewritten, and the container used
# is the one that serves it: on a hub set up before the rename that is jarvis-postgres (volume jarvis-pgdata, user and
# database jarvis), even when a persona-postgres container exists too. A container is made only when there is neither a
# database_url nor any hub database container or volume. Moving a database to the new names is the operator's step in
# docs/operations/persona-cutover.md, never this script's.
set -euo pipefail
NAME=persona-postgres
VOLUME=persona-pgdata
# The names of a hub set up before the rename (#6079).
LEGACY=jarvis-postgres
LEGACY_VOLUME=jarvis-pgdata
PORT=18661
ROOT="${_PERSONA_STATE_DIR:-$(python3 "$(dirname "${BASH_SOURCE[0]}")/../persona_identity.py" --service hub --prepare)}"
CFG="$ROOT/hub.json"
mkdir -p "$ROOT" && chmod 700 "$ROOT"

url() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("database_url",""))' "$CFG" 2>/dev/null || true; }
# The user (part 1) or database (part 2) of a Postgres URL; "" when it has none.
url_part() { python3 -c 'import sys, urllib.parse as u
try:
    p = u.urlsplit(sys.argv[1])
    print((p.username or "") if sys.argv[2] == "1" else p.path.lstrip("/"))
except ValueError:
    print("")' "$1" "$2"; }
# The user a container's Postgres was made with (its POSTGRES_USER), "postgres" when it was not given.
container_user() {
  docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$1" 2>/dev/null |
    sed -n 's/^POSTGRES_USER=//p' | head -1 | grep . || echo postgres
}
has_container() { docker ps -a --format '{{.Names}}' | grep -qx "$1"; }
has_volume() { docker volume inspect "$1" >/dev/null 2>&1; }

# No docker on the hub machine: the database is one set up by hand (the distribution's own Postgres, docs/server/server.md
# "Database") and hub.json's database_url points at it. Nothing to create here; only check that it answers.
answers() {
  if command -v pg_isready >/dev/null 2>&1; then
    pg_isready -q -d "$(url)" || { echo "the Postgres at hub.json's database_url does not answer" >&2; exit 1; }
  fi
}
if ! command -v docker >/dev/null 2>&1; then
  [ -n "$(url)" ] || { echo "no docker and no database_url in hub.json: install docker, or set up Postgres yourself (docs/server/server.md)" >&2; exit 1; }
  answers
  echo "postgres from hub.json's database_url (no docker)"
  exit 0
fi

DB_URL=$(url)
if [ -n "$DB_URL" ]; then
  # The container that serves database_url: the one made with its user. Never another one, whatever its name.
  want=$(url_part "$DB_URL" 1)
  found=""
  for c in "$NAME" "$LEGACY"; do
    if has_container "$c" && [ "$(container_user "$c")" = "$want" ]; then
      found=$c
      break
    fi
  done
  if [ -z "$found" ]; then
    # A database this script did not make (or one in a container under another name): use it as it is.
    answers
    echo "postgres from hub.json's database_url (no container of $NAME or $LEGACY serves it; nothing made or changed)"
    exit 0
  fi
  for c in "$NAME" "$LEGACY"; do
    [ "$c" = "$found" ] || ! has_container "$c" || echo "note: container $c exists but hub.json's database_url is not its database; it is left as it is" >&2
  done
  NAME=$found
else
  for c in "$NAME" "$LEGACY"; do
    if has_container "$c"; then
      echo "container $c exists but hub.json has no database_url: put its address back in hub.json (its password is" \
        "in the container's POSTGRES_PASSWORD); nothing was made or changed" >&2
      exit 1
    fi
  done
  for v in "$VOLUME" "$LEGACY_VOLUME"; do
    if has_volume "$v"; then
      echo "volume $v exists (a hub database without its container) but hub.json has no database_url: restore its" \
        "container and database_url, or remove the volume if it is not needed; nothing was made or changed" >&2
      exit 1
    fi
  done
  # Neither a database_url nor any hub database: a new hub.
  PW=$(python3 -c 'import secrets;print(secrets.token_hex(24))')
  docker volume create "$VOLUME" >/dev/null
  # postgres 18 keeps PGDATA under /var/lib/postgresql/<major>/docker, so mount the parent. The command after the image
  # is pgdocker.ServerArgs (server/internal/pgdocker): fewer and lz4-compressed full page images in the WAL.
  docker run -d --name "$NAME" --restart unless-stopped -p 127.0.0.1:$PORT:5432 \
    -v "$VOLUME":/var/lib/postgresql -e POSTGRES_USER=persona -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=persona \
    postgres:18-alpine postgres -c wal_compression=lz4 -c checkpoint_timeout=15min >/dev/null
  [ -f "$CFG" ] || echo '{}' > "$CFG"
  umask 077
  python3 - "$CFG" "postgres://persona:$PW@127.0.0.1:$PORT/persona?sslmode=disable" <<'PY'
import json, sys
path, url = sys.argv[1], sys.argv[2]
cfg = json.load(open(path))
if cfg.get("database_url"):
    sys.exit("hub.json got a database_url meanwhile; it is not rewritten")
cfg["database_url"] = url
json.dump(cfg, open(path, "w"), indent=2, ensure_ascii=False)
PY
  chmod 600 "$CFG"
  echo "created $NAME"
fi
docker start "$NAME" >/dev/null
# The user and database to ask as: database_url's (jarvis on a hub set up before the rename, persona on a new one).
DB_USER=$(url_part "$(url)" 1)
DB_NAME=$(url_part "$(url)" 2)
DB_USER=${DB_USER:-persona}
DB_NAME=${DB_NAME:-persona}
# The port Postgres listens on inside the container, for the pg_isready / psql / createdb run there (they default to
# 5432): 5432 in the container created above (published as 127.0.0.1:$PORT), but one on the host's network
# (--network host, where publishing a port does not work) listens on the port the hub connects to, database_url's.
INNER=5432
if [ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$NAME" 2>/dev/null)" = host ]; then
  INNER=$(python3 -c 'import sys, urllib.parse as u
try: print(u.urlsplit(sys.argv[1]).port or 5432)
except ValueError: print(5432)' "$(url)")
fi
in_db() { docker exec -e PGPORT="$INNER" "$NAME" "$@"; }
for _ in $(seq 1 30); do
  in_db pg_isready -U "$DB_USER" -d "$DB_NAME" >/dev/null 2>&1 && break
  sleep 1
done
ready=$(in_db pg_isready -U "$DB_USER" -d "$DB_NAME" 2>&1) ||
  { echo "postgres in $NAME is not ready on port $INNER inside the container after 30s: $ready" >&2; exit 1; }
# Throwaway database for scripts/test/test-server.sh: <database>_test (scripts/test/server_test_env.py).
in_db psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}_test'" | grep -q 1 ||
  in_db createdb -U "$DB_USER" "${DB_NAME}_test"
[ -n "$(url)" ] || { echo "hub.json has no database_url" >&2; exit 1; }
echo "postgres ready on 127.0.0.1:$PORT ($NAME)"
