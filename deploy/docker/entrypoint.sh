#!/bin/sh
# The hub image's entrypoint (docs/operations/docker.md): run personad as the persona user with the uid / gid the data volume's
# files should have (PUID / PGID, as NAS images do: the files stay yours in the NAS's file manager).
set -eu
# This entry point also accepts deployments configured before the naming cutover.
for suffix in HOME PUBLIC_URL ADMIN_ACCESS ADMIN_CIDRS; do
  eval 'current=${PERSONA_'"$suffix"'+x}'
  eval 'legacy=${JARVIS_'"$suffix"'+x}'
  if [ "$current" != x ] && [ "$legacy" = x ]; then
    eval 'value=${JARVIS_'"$suffix"'-}'
    export "PERSONA_$suffix=$value"
  fi
done
DATA="${PERSONA_HOME:-$HOME/.persona}"
# An older compose file mounts only the former image home. Prefer that mounted store,
# not the empty canonical directory built into this image.
if [ -z "${PERSONA_HOME:-}" ] && grep -q ' /home/jarvis/.jarvis ' /proc/mounts 2>/dev/null; then
  DATA=/home/jarvis/.jarvis
  PERSONA_HOME="$DATA"
fi
if [ -z "${PERSONA_HOME:-}" ] && { [ -e "$HOME/.jarvis" ] || [ -L "$HOME/.jarvis" ]; }; then
  if { [ -e "$DATA" ] || [ -L "$DATA" ]; } && [ ! "$DATA" -ef "$HOME/.jarvis" ]; then
    echo 'independent state directories; set PERSONA_HOME explicitly' >&2; exit 1
  fi
  DATA="$HOME/.jarvis"
fi
[ ! -L "$DATA" ] || [ -d "$DATA" ] || { echo 'state symlink target unavailable; restore it before upgrading' >&2; exit 1; }
export PERSONA_HOME="$DATA" JARVIS_HOME="$DATA"
# TLS port inside the container: compose publishes it as PERSONA_TLS_PORT (18662 unless set).
TLS_PORT=18662

# A first start's hub.json (docs/assistant/onboarding.md 4.1, "Docker / NAS"): the new-install defaults that fit a container,
# written only when there is no hub.json yet. It has no token: the hub adds one (and every default it does not say)
# when it first reads it, as it does with the file db.sh writes. A hub.json that exists is never touched.
#   public_url  "": the container cannot see the NAS's LAN address; PERSONA_PUBLIC_URL (.env) or the admin page sets it
#   public.tls_listen  every address of the container: which of the NAS's addresses get it is compose's port mapping
#   admin.first_setup  "code": in a container any private address counts as LAN, so the first password takes the code
#   machines  none: the image has no herdr or claude, every computer is added
#   assistant.mode  service: the built-in manager is a process of its own beside the hub (personad supervise binds
#                   and runs it), not the hub's child; a hub.json from before keeps its mode
seed_config() {
  for f in "$DATA/hub.json"; do
    if [ -e "$f" ] || [ -L "$f" ]; then return 0; fi
  done
  mkdir -p "$DATA"
  (
    umask 077
    printf '{\n  "public_url": "",\n  "public": {\n    "tls_listen": ":%s"\n  },\n  "admin": {\n    "first_setup": "code"\n  },\n  "machines": [],\n  "assistant": {\n    "mode": "service"\n  }\n}\n' \
      "$TLS_PORT" > "$DATA/hub.json.new"
  )
  mv "$DATA/hub.json.new" "$DATA/hub.json"
  echo "first start: wrote hub.json with the defaults for a new hub in a container (TLS on port $TLS_PORT, no" \
    "computers yet, the built-in manager a process of its own)" >&2
}

# Not the hub: an app-made manager (manager …), or the built-in manager in a container of its own (supervise
# --manager-only): no hub.json written and none of the hub's reminders. --manager-only as Go's flags take it: one or
# two dashes, a value that strconv.ParseBool reads as true, the last one counting, none after a bare --.
not_hub=
case "${1:-}" in
manager) not_hub=1 ;;
supervise)
  for a in "$@"; do
    case "$a" in
    --) break ;;
    --manager-only | -manager-only) not_hub=1 ;;
    --manager-only=* | -manager-only=*)
      case "${a#*=}" in
      1 | t | T | true | TRUE | True) not_hub=1 ;;
      *) not_hub= ;;
      esac
      ;;
    esac
  done
  ;;
esac
if [ "$(id -u)" = 0 ]; then
  mkdir -p "$DATA"
  # PUID / PGID unset (.env without them): whoever owns the data folder, as the NAS's file manager made it. A folder
  # Docker made itself is root's: 1000, the first user on most NAS systems and Linux machines.
  if [ -z "${PUID:-}" ] || [ -z "${PGID:-}" ]; then
    owner="$(stat -c %u "$DATA")"
    group="$(stat -c %g "$DATA")"
    if [ "$owner" = 0 ]; then owner=1000; fi
    if [ "$group" = 0 ]; then group=1000; fi
    PUID="${PUID:-$owner}"
    PGID="${PGID:-$group}"
    echo "note: PUID / PGID not set in .env: the hub's files belong to $PUID:$PGID (the data folder's owner, or 1000)" >&2
  fi
  case "$PUID$PGID" in '' | *[!0-9]*) echo "PUID / PGID must be numbers" >&2; exit 2 ;; esac
  if [ "$PUID" -eq 0 ]; then
    echo "PUID=0 would run the hub as root: pick the uid that owns the data folder" >&2
    exit 2
  fi
  # The passwd entry, not just the number: ssh looks the uid up for its home (~/.ssh) and refuses a uid it cannot
  # find. Another entry with the same uid (alpine's guest is 405, nobody 65534) would win that lookup: drop it.
  awk -F: -v u="$PUID" '$3 == u && $1 != "persona" { next } { print }' /etc/passwd > /etc/passwd.new
  cat /etc/passwd.new > /etc/passwd && rm -f /etc/passwd.new
  sed -i "s/^persona:x:[0-9]*:[0-9]*:/persona:x:$PUID:$PGID:/" /etc/passwd
  sed -i "s/^persona:x:[0-9]*:/persona:x:$PGID:/" /etc/group
  [ -n "$not_hub" ] || seed_config
  # Anything not the hub's (PUID changed, files copied in with sudo) is handed over: a root-owned 0600 hub.json
  # would keep the hub from starting.
  find "$DATA" \( ! -user "$PUID" -o ! -group "$PGID" \) -exec chown -h "$PUID:$PGID" {} +
  chown "$PUID:$PGID" "$HOME"
  chmod 700 "$DATA"
else
  if ! id -un >/dev/null 2>&1; then
    # Started as another user (compose `user:`): PUID / PGID do not apply, and ssh cannot run without a passwd entry.
    echo "warning: running as uid $(id -u), which has no user in the image: PUID / PGID are ignored and ssh will" \
      "not work. Start the container as root (no user:) and set PUID / PGID instead." >&2
  fi
  [ -n "$not_hub" ] || seed_config
fi
# A manager (docs/assistant/managers.md, "Containers"), not a hub: no hub.json and none of the hub's reminders.
if [ -n "$not_hub" ]; then
  exec personad "$@"
fi
# How phones reach the hub: nobody said, and the container cannot tell.
if [ -z "${PERSONA_PUBLIC_URL:-}" ] && grep -q '"public_url": *""' "$DATA/hub.json" 2>/dev/null; then
  echo "reminder: PERSONA_PUBLIC_URL is not set in .env: the hub does not know how phones reach it, and pairing" \
    "codes carry no address. Set it to this machine's address and the hub's port (http://<NAS address>:18660), then" \
    "docker compose up -d; or fill in the address on the admin page's network section (docs/operations/docker.md)." >&2
fi
# Desktop relays can hide the original peer behind a gateway or a synthetic public address.
if getent hosts host.docker.internal >/dev/null 2>&1; then
  echo "note: this looks like Docker Desktop / OrbStack: port-mapped peers may appear as the bridge gateway or" \
    "a synthetic public address (gvisor). If API requests return 403 tls_required, inspect the peer with" \
    "docker compose exec hub netstat -tn while a client connects. Prefer TLS; for local HTTP publish on" \
    "127.0.0.1 only before adding the observed peer to security.trusted_cidrs (docs/operations/docker.md)." >&2
  case "${PERSONA_ADMIN_ACCESS:-}" in '' | lan)
    echo "note: for the admin UI, publish on 127.0.0.1 only and use PERSONA_ADMIN_ACCESS=cidrs with the observed" \
      "peer in PERSONA_ADMIN_CIDRS (docs/operations/admin.md), or use docker compose exec hub personad hub admin." >&2 ;;
  esac
fi
# Who may open the admin UI (docs/operations/admin.md): the deployment's choice, or a reminder that nobody made one.
case "${PERSONA_ADMIN_ACCESS:-}" in
'') echo "reminder: PERSONA_ADMIN_ACCESS is not set in .env: the admin UI (/admin) follows hub.json's admin.access," \
  "lan (private addresses) unless set there. Choose loopback, lan, cidrs or any (docs/operations/admin.md)." >&2 ;;
loopback | lan | any) ;;
cidrs) [ -n "${PERSONA_ADMIN_CIDRS:-}" ] || echo "warning: PERSONA_ADMIN_ACCESS=cidrs without PERSONA_ADMIN_CIDRS: the" \
  "admin UI is reachable from this container only" >&2 ;;
*) echo "warning: PERSONA_ADMIN_ACCESS=$PERSONA_ADMIN_ACCESS is not loopback, lan, cidrs or any: the admin UI is limited" \
  "to this container until it is fixed" >&2 ;;
esac
# The hub (the image's command): under personad supervise, which runs the built-in manager beside it when hub.json
# says assistant.mode service, each started again when it stops; otherwise it becomes the hub itself, as before.
if [ "$#" -eq 2 ] && [ "$1" = hub ] && [ "$2" = run ]; then
  exec personad supervise
fi
exec personad "$@"
