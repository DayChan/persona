#!/bin/bash
# Run on a machine (a Mac in its login session, or Linux) to keep a resident personad node there (docs/server/server.md "Resident node"):
#   ./scripts/deploy/node-launchd.sh install    # Mac: launchd agent com.persona.node; Linux: systemd user service personad-node;
#                                        # started at login / boot, restarted whenever it exits
#   ./scripts/deploy/node-launchd.sh restart    # after a new binary, now (ends the assistant's running conversations)
#   ./scripts/deploy/node-launchd.sh restart-idle  # after a new binary (deploy.sh does this): once no conversation is
#                                        # busy (SIGHUP to the node only; docs/operations/hub-restart.md "Conversations are not interrupted")
#   ./scripts/deploy/node-launchd.sh uninstall  # back to the hub starting the node over ssh
# The hub keeps connecting over ssh as before; `personad node --stdio` relays to this node when it's running, so what
# the node starts runs in the login session and can read the login keychain. Set up for tailcat
# (`personad node tailcat init`), the resident node is also how the hub reaches the machine when ssh cannot.
set -euo pipefail
if [ -z "${_PERSONA_NODE_UNIT:-}" ]; then
  _env=$(python3 "$(dirname "${BASH_SOURCE[0]}")/../persona_identity.py" --shell --service node) || exit $?
  eval "$_env"; unset _env
fi
ROOT="$_PERSONA_STATE_DIR"
BIN="$_PERSONA_NODE_BIN"
xml_escape() {
  # Not ${value//&/...}: bash 3.2 (macOS /bin/bash) and 5.2 (patsub_replacement) read & and \ in the replacement differently.
  printf '%s' "$1" | LC_ALL=C sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}
systemd_quote() {
  local value="$1"
  value="${value//\\/\\\\}"; value="${value//\"/\\\"}"; value="${value//%/%%}"
  printf '"%s"' "$value"
}

if [ "$(uname -s)" = Linux ]; then
  UNIT="$_PERSONA_NODE_UNIT.service"
  UNIT_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/$UNIT"
  case "${1:-install}" in
    install)
      [ -x "$BIN" ] || { echo "no $BIN: deploy this machine first (scripts/deploy/deploy.sh)" >&2; exit 1; }
      mkdir -p "$(dirname "$UNIT_FILE")" "$ROOT"
      TARGET_FILE="$UNIT_FILE"
      if [ -f "$UNIT_FILE" ]; then
        TARGET_FILE="$UNIT_FILE.d/90-persona-compat.conf"
        mkdir -p "$(dirname "$TARGET_FILE")"
      fi
      cat > "$TARGET_FILE" <<UN
[Unit]
Description=Persona resident node (docs/server/server.md)
After=network-online.target

[Service]
ExecStart=
ExecStart=$(systemd_quote "${BIN//\$/\$\$}") node --attach
Environment=$(systemd_quote "PERSONA_HOME=$ROOT")
Environment=$(systemd_quote "$_PERSONA_LEGACY_HOME_KEY=$ROOT")
# The same PATH as the hub's unit (hub-systemd.sh): the node finds herdr, claude and the rest where a hub-started one did.
Environment=PATH=%h/.local/bin:%h/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
Restart=always
RestartSec=10
StandardError=append:${ROOT//%/%%}/node-attach.log

[Install]
WantedBy=default.target
UN
      systemctl --user daemon-reload
      systemctl --user enable --now "$UNIT"
      # Without linger the user's services stop when the last session ends (and do not start at boot).
      loginctl show-user "$(id -un)" -p Linger 2>/dev/null | grep -q yes ||
        echo "note: run 'sudo loginctl enable-linger $(id -un)' so it keeps running with nobody logged in" >&2
      echo "installed $UNIT" ;;
    restart)
      if systemctl --user is-enabled "$UNIT" >/dev/null 2>&1; then systemctl --user restart "$UNIT" && echo "restarted $UNIT"; fi ;;
    restart-idle)
      # The node only: a signal to the whole unit would reach the conversations' claude processes too.
      # --kill-who=: systemd before 252 knows no other name (--kill-whom= is 252's), and later ones still read it.
      if systemctl --user is-active --quiet "$UNIT"; then
        systemctl --user kill -s HUP --kill-who=main "$UNIT" && echo "$UNIT restarts once no conversation is busy"
      fi ;;
    uninstall)
      systemctl --user disable --now "$UNIT" 2>/dev/null || true
      rm -f "$UNIT_FILE" "$UNIT_FILE.d/90-persona-compat.conf" "$ROOT/node-attach.sock"
      rmdir "$UNIT_FILE.d" 2>/dev/null || true
      systemctl --user daemon-reload 2>/dev/null || true
      echo "removed $UNIT" ;;
    *) echo "usage: $0 [install|restart|restart-idle|uninstall]" >&2; exit 2 ;;
  esac
  exit 0
fi

LABEL="$_PERSONA_NODE_LABEL"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
# ProcessType Interactive: without it launchd throttles the node and the claude it starts like a background job, and
# claude may not start in time (#5944). It grants no privacy (TCC) permission. `personad node doctor --fix` adds it to
# a plist written before.
DOMAIN="gui/$(id -u)"
case "${1:-install}" in
  install)
    [ -x "$BIN" ] || { echo "no $BIN: deploy this machine first (scripts/deploy/deploy.sh)" >&2; exit 1; }
    mkdir -p "$HOME/Library/LaunchAgents" "$ROOT"
    cat > "$PLIST.new" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array><string>$(xml_escape "$BIN")</string><string>node</string><string>--attach</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PERSONA_HOME</key><string>$(xml_escape "$ROOT")</string>
    <key>$_PERSONA_LEGACY_HOME_KEY</key><string>$(xml_escape "$ROOT")</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardErrorPath</key><string>$(xml_escape "$ROOT/node-attach.log")</string>
</dict></plist>
PL
    python3 - "$PLIST" <<'PY'
import os, plistlib, sys
path = sys.argv[1]
with open(path + '.new', 'rb') as source:
    updated = plistlib.load(source)
if os.path.exists(path):
    with open(path, 'rb') as source:
        original = plistlib.load(source)
    values = original.get('EnvironmentVariables', {})
    values.update(updated['EnvironmentVariables'])
    original.update(updated)
    original['EnvironmentVariables'] = values
    updated = original
with open(path + '.new', 'wb') as target:
    plistlib.dump(updated, target)
os.replace(path + '.new', path)
PY
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
    launchctl bootstrap "$DOMAIN" "$PLIST"
    echo "installed $LABEL" ;;
  restart)
    if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then launchctl kickstart -k "$DOMAIN/$LABEL" && echo "restarted $LABEL"; fi ;;
  restart-idle)
    if launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1; then launchctl kill SIGHUP "$DOMAIN/$LABEL" && echo "$LABEL restarts once no conversation is busy"; fi ;;
  uninstall)
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
    rm -f "$PLIST" "$ROOT/node-attach.sock"
    echo "removed $LABEL" ;;
  *) echo "usage: $0 [install|restart|restart-idle|uninstall]" >&2; exit 2 ;;
esac
