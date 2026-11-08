#!/bin/bash
# Install the existing hub unit using the same resolved root as the deployed binary (Linux: systemd user service;
# Mac: restart the launchd agent that already runs it). personad hub setup (server/internal/hub/setup_service.go)
# writes the same units: keep the two in step.
set -euo pipefail
if [ -z "${_PERSONA_HUB_UNIT:-}" ]; then
  _env=$(python3 "$(dirname "${BASH_SOURCE[0]}")/../persona_identity.py" --shell --service hub --prepare) || exit $?
  eval "$_env"; unset _env
fi
ROOT="$_PERSONA_STATE_DIR"
BIN="$_PERSONA_HUB_BIN"
UNIT="$_PERSONA_HUB_UNIT"
# Blue-green (docs/operations/hub-restart.md "Blue-green switch"): the running hub's edge starts the new binary next to the one serving
# and switches once it is up, nothing restarts. It returns 0 once switched; 1 (no edge here, or one older than the new
# binary's protocol: `hub switch` exits 3; or a binary that cannot run it) to have the caller restart the service;
# it exits the script when the new binary did not come up (exit 1: the hub serves on the old one, back in place).
switch_or_restart() {
  local prev=() rc=0
  [ -x "$ROOT/bin/${BIN##*/}.prev" ] && prev=(--prev "$ROOT/bin/${BIN##*/}.prev")
  "$BIN" hub switch --timeout 8m ${prev[@]+"${prev[@]}"} || rc=$?
  case $rc in
    0) echo "hub: switched to the new binary (blue-green, nothing restarted)"; return 0 ;;
    1) ;;
    *) echo "hub: no edge to switch on (hub switch: exit $rc): restarting the service"; return 1 ;;
  esac
  if [ ${#prev[@]} -gt 0 ]; then
    cp -p "$ROOT/bin/${BIN##*/}.prev" "$ROOT/bin/${BIN##*/}.new" && mv "$ROOT/bin/${BIN##*/}.new" "$BIN"
    echo "hub: the new binary did not take over; the old one serves on and is back in place" >&2
  else
    echo "hub: the new binary did not take over; the old one serves on" >&2
  fi
  exit 1
}
if [ "$(uname -s)" = Darwin ] && ! command -v systemctl >/dev/null 2>&1; then
  # A Mac hub runs under a launchd agent the user set up (no systemd): restart the one whose program is this root's
  # personad with "hub" (through ~/.persona or ~/.persona, whichever path it names). Nothing is installed here.
  label=$(python3 - <<'PY'
import glob, os, plistlib
want = os.path.realpath(os.path.join(os.environ['PERSONA_HOME'], 'bin/personad'))
for path in sorted(glob.glob(os.path.expanduser('~/Library/LaunchAgents/*.plist'))):
    try:
        with open(path, 'rb') as f:
            job = plistlib.load(f)
    except Exception:
        continue
    args = job.get('ProgramArguments') or ([job['Program']] if job.get('Program') else [])
    if len(args) >= 2 and args[1] == 'hub' and os.path.realpath(os.path.expanduser(args[0])) == want:
        print(job.get('Label', ''))
        break
PY
)
  if [ -z "$label" ]; then
    echo "hub: no launchd agent in ~/Library/LaunchAgents runs $ROOT/bin/personad hub; restart the hub yourself" >&2
    exit 1
  fi
  switch_or_restart && exit 0
  launchctl kickstart -k "gui/$(id -u)/$label"
  echo "hub: restarted launchd agent $label"
  exit 0
fi
# The listening sockets belong to $UNIT.socket (socket activation, docs/operations/hub-restart.md "No gap"): restarting the
# hub never closes the port, connections made meanwhile wait for the next process instead of being refused.
# PERSONA_HUB_SOCKET=0 goes back to the hub listening itself (the rollback): the socket unit is removed.
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
SOCKET_FILE="$UNIT_DIR/$UNIT.socket"
SERVICE_FILE="$UNIT_DIR/$UNIT.service"
socket_before=$(cat "$SOCKET_FILE" 2>/dev/null || true)
service_before=$(cat "$SERVICE_FILE" "$SERVICE_FILE.d/90-persona-compat.conf" 2>/dev/null || true)
PERSONA_HUB_SOCKET="${PERSONA_HUB_SOCKET:-1}" python3 - <<'PY'
import json, os, re
from pathlib import Path
root = os.environ['PERSONA_HOME']
unit_name = os.environ['_PERSONA_HUB_UNIT']
binary = os.environ['_PERSONA_HUB_BIN']
legacy_home = os.environ['_PERSONA_LEGACY_HOME_KEY']
legacy_edge = os.environ['_PERSONA_LEGACY_EDGE_KEY']
def quote(value):
    return '"' + value.replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'
def stream(addr):
    # hub.json's Go address as a ListenStream: ":18660" (every address) is just the port for systemd.
    return addr[1:] if addr.startswith(':') else addr
try:
    cfg = json.loads((Path(root) / 'hub.json').read_text())
except (OSError, ValueError):
    cfg = {}
listens = [cfg.get('listen') or ':18660']
if (cfg.get('public') or {}).get('tls_listen'):
    listens.append(cfg['public']['tls_listen'])
use_socket = os.environ['PERSONA_HUB_SOCKET'] != '0'
unit_dir = Path(os.environ.get('XDG_CONFIG_HOME') or str(Path.home() / '.config')) / 'systemd/user'
# PERSONA_HUB_EDGE=0 runs the hub without its edge (docs/operations/hub-restart.md "Blue-green switch"): what was given once stays in the unit
# across deploys until it is given again (PERSONA_HUB_EDGE=1 goes back to the edge).
edge = os.environ.get('PERSONA_HUB_EDGE')
if edge is None:
    try:
        m = re.search(r'(?:PERSONA_HUB_EDGE|' + re.escape(legacy_edge) + r')=([^"\s]*)', (unit_dir / (unit_name + '.service')).read_text())
        edge = m.group(1) if m else ''
    except OSError:
        edge = ''
# Keep an explicit enabling value in an upgrade drop-in so it overrides an older disabling value.
unit_dir.mkdir(parents=True, exist_ok=True)
unit = [
    '[Unit]', 'Description=Persona hub (personad)', 'After=network-online.target',
]
if use_socket:
    unit += ['Wants=' + unit_name + '.socket', 'After=' + unit_name + '.socket']
unit += [
    '', '[Service]',
    'ExecStart=' + quote(binary).replace('$', '$$') + ' hub',
    'Restart=always', 'RestartSec=3', 'TimeoutStopSec=20',
    'Environment=' + quote('PERSONA_HOME=' + root),
    'Environment=' + quote(legacy_home + '=' + root),
    'Environment=PATH=%h/.local/bin:%h/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin',
    *(['Environment=' + quote(key + '=' + edge) for key in ('PERSONA_HUB_EDGE', legacy_edge)] if edge else []), '',
    '[Install]', 'WantedBy=default.target', '',
]
service = unit_dir / (unit_name + '.service')
if service.exists() and service.read_text() != '\n'.join(unit):
    # Preserve operator EnvironmentFile, drop-ins, credentials and other service settings.
    dropin = service.with_name(service.name + '.d') / '90-persona-compat.conf'
    dropin.parent.mkdir(parents=True, exist_ok=True)
    dropin.write_text('\n'.join([
        '[Service]', 'ExecStart=',
        'ExecStart=' + quote(binary).replace('$', '$$') + ' hub',
        *['Environment=' + quote(key + '=' + root) for key in ('PERSONA_HOME', legacy_home)],
        *(['Environment=' + quote(key + '=' + edge) for key in ('PERSONA_HUB_EDGE', legacy_edge)] if edge else []), '',
    ]))
else:
    service.write_text('\n'.join(unit))
socket = unit_dir / (unit_name + '.socket')
if use_socket:
    socket.write_text('\n'.join([
        '[Unit]', "Description=Persona hub's listening sockets (docs/operations/hub-restart.md)", '', '[Socket]',
        *['ListenStream=' + stream(a) for a in listens],
        # An address-bound listener (personad hub setup binds TLS to the LAN address) comes up even while that
        # address is gone (DHCP) instead of failing the socket and the plaintext port with it. Same as hub/setup_service.go.
        'Backlog=4096', 'NoDelay=true', *(['FreeBind=true'] if any(not a.startswith(':') for a in listens) else []),
        'Service=' + unit_name + '.service', '',
        '[Install]', 'WantedBy=sockets.target', '',
    ]))
elif socket.exists():
    socket.unlink()
PY
socket_after=$(cat "$SOCKET_FILE" 2>/dev/null || true)
service_after=$(cat "$SERVICE_FILE" "$SERVICE_FILE.d/90-persona-compat.conf" 2>/dev/null || true)
systemctl --user daemon-reload
systemctl --user enable "$UNIT" >/dev/null 2>&1
# The units as they were and the hub running: switch to the new binary instead of restarting.
if [ "$socket_before" = "$socket_after" ] && [ "$service_before" = "$service_after" ] && systemctl --user is-active --quiet "$UNIT"; then
  if switch_or_restart; then
    systemctl --user is-active "$UNIT"
    exit 0
  fi
fi
if [ -z "$socket_after" ]; then
  # Rolling back (or never used): the hub listens itself again once the socket lets go of the port.
  if systemctl --user is-active --quiet $UNIT.socket 2>/dev/null || [ -n "$socket_before" ]; then
    systemctl --user stop "$UNIT"
    systemctl --user disable --now $UNIT.socket >/dev/null 2>&1 || true
    systemctl --user start "$UNIT"
  else
    systemctl --user restart "$UNIT"
  fi
elif ! systemctl --user is-active --quiet $UNIT.socket || [ "$socket_before" != "$socket_after" ]; then
  # First time, or other addresses: the running hub holds the port itself; one stop to hand it to the socket.
  # Both in one stop: stopping the service alone while the socket listens lets the next connection start it again
  # on the old socket, and the socket then cannot start while it runs (seen 2026-11-08: the socket left inactive).
  systemctl --user stop $UNIT.socket "$UNIT"
  systemctl --user enable $UNIT.socket >/dev/null 2>&1
  systemctl --user start $UNIT.socket
  systemctl --user start "$UNIT"
else
  systemctl --user restart "$UNIT" # the socket keeps listening: nothing is refused meanwhile
fi
systemctl --user is-active "$UNIT"
