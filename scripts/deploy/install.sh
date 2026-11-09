#!/bin/sh
# Install personad (docs/operations/release.md "Publishing personad"). scripts/release/publish.sh serves this file as <site>/install.sh with
# PERSONA_DL filled in:
#
#   curl -fsSL <site>/install.sh | sh
#
# Puts the binary for this system at ~/.persona/bin/personad (where the hub looks for it on every computer it reaches
# over ssh), after checking it against the published SHA256SUMS; on the computer that runs the hub, persona-hub and
# persona-manager beside it too (docs/server/server.md "Programs"). PERSONA_DL=<url> installs from somewhere else.
#
# One hub is enough for a home (docs/archive/operations/plan-hub-center.md section 0); every other computer gets the node service: the
# resident node and the connector persona-link, paired with the hub (docs/operations/link.md "Installing the node service"):
#
#   curl -fsSL <site>/install.sh | sh -s -- --node --pair '<link from personad hub pair --machine <id>>'   # this one connects to the hub
#   curl -fsSL <site>/install.sh | sh -s -- --node --listen [:18663] [--addr <address the hub reaches this one at>]  # the hub connects here
#
# --pair and --listen imply --node (PERSONA_ROLE=node, PERSONA_PAIR, PERSONA_LISTEN do the same). Without a role and with
# a terminal it asks, and says so when it hears a hub on this LAN already (mDNS). --hub is the machine that runs the hub.
#
# On the machine that runs the hub it also asks who may open the hub's admin UI (docs/operations/admin.md): with a terminal it
# asks; without one, give it as `sh -s -- --admin-access lan` (or PERSONA_ADMIN_ACCESS=lan), else lan applies and a
# line says so. --admin-cidrs a,b (PERSONA_ADMIN_CIDRS) goes with cidrs.
# Then herdr (the default engine, docs/integrations/engines.md) if this computer has none: herdr's own installer puts it in
# ~/.local/bin, where the node looks. PERSONA_SKIP_HERDR=1 leaves it out (Orca / Herdr App machines); a failed herdr
# install only warns. scripts/deploy/ensure-herdr.sh does the same for deploy.sh: keep the two in step.
set -eu

# Standalone compatibility adapter: preserve explicit empty canonical overrides.
for suffix in ADMIN_ACCESS ADMIN_CIDRS ROLE PAIR MACHINE LISTEN DL HERDR_INSTALL HOME WITH SKIP_HERDR; do
    eval 'present=${PERSONA_'"$suffix"'+x}'
    eval 'legacy_present=${JARVIS_'"$suffix"'+x}'
    if [ "$present" != x ] && [ "$legacy_present" = x ]; then
        eval 'value=${JARVIS_'"$suffix"'-}'
        export "PERSONA_$suffix=$value"
    fi
done
LEGACY_ROOT="$HOME/.jarvis"

# >>> admin access (scripts/deploy.sh reads this block from here: keep it POSIX sh and self-contained)
admin_access_help() {
	cat <<'HELP'
hub 管理界面（<hub 地址>/admin：加电脑、管设备、改网络）谁能打开？
  1) loopback  只有 hub 这台机器自己（Docker 里是容器自己），别的电脑要用 ssh 隧道。最安全，用着最麻烦
  2) lan       本机 + 和 hub 同一网段的局域网（默认）。家里 / 办公室的局域网里都能打开，公网和别的网段打不开
  3) cidrs     本机 + 你列出的网段，例如 VPN 的网段；其中的公网网段也只能经 TLS
  4) any       任何地方经 TLS 端口都能打开（局域网照旧）。暴露在公网上，只靠管理员密码和登录限流挡着；没开 TLS 时等于 lan
以后在 hub 机器上用 personad hub admin access <范围> 改；网页里只能收窄，不能放宽。
HELP
}

# admin_access_valid <access> <cidrs>: the scope is one of the four, and the networks look like networks.
admin_access_valid() {
	case "$1" in loopback | lan | any) [ -z "$2" ] ;; cidrs) [ -n "$2" ] && case "$2" in *[!0-9A-Fa-f.:/,]*) false ;; *) true ;; esac ;; *) false ;; esac
}

# ask_admin_access: ADMIN_ACCESS (and ADMIN_CIDRS) from the terminal; returns 1 when there is none (curl | sh still has
# /dev/tty when run by a person; CI and cron do not).
ask_admin_access() {
	(: </dev/tty) 2>/dev/null || return 1
	admin_access_help >/dev/tty
	while :; do
		printf '选哪个？[1-4，回车 = 2 lan] ' >/dev/tty
		read -r answer </dev/tty || return 1
		case "$answer" in
		'' | 2 | lan) ADMIN_ACCESS=lan ;;
		1 | loopback) ADMIN_ACCESS=loopback ;;
		3 | cidrs) ADMIN_ACCESS=cidrs ;;
		4 | any) ADMIN_ACCESS=any ;;
		*) echo "请输入 1 到 4" >/dev/tty && continue ;;
		esac
		break
	done
	ADMIN_CIDRS=
	while [ "$ADMIN_ACCESS" = cidrs ] && ! admin_access_valid cidrs "$ADMIN_CIDRS"; do
		printf '网段，逗号分隔（例如 100.64.0.0/10,192.0.2.0/24）：' >/dev/tty
		read -r ADMIN_CIDRS </dev/tty || return 1
		ADMIN_CIDRS=$(printf '%s' "$ADMIN_CIDRS" | tr -d ' ')
	done
	if [ "$ADMIN_ACCESS" = any ]; then
		printf '管理界面会对公网开放。输入 any 确认，别的改用 lan：' >/dev/tty
		read -r confirm </dev/tty || return 1
		[ "$confirm" = any ] || { echo "没有确认，改用 lan" >/dev/tty && ADMIN_ACCESS=lan; }
	fi
	return 0
}

# admin_access_reminder [if-hub]: the one line printed when nobody chose (no terminal, no option).
admin_access_reminder() {
	echo "提醒：${1:+如果这台跑 hub，}hub 管理界面（/admin）的访问范围没有选，用默认的 lan（hub 本机和同一局域网）；要改在 hub 机器上运行 personad hub admin access <loopback|lan|cidrs|any>" >&2
}
# <<< admin access

ADMIN_ACCESS="${PERSONA_ADMIN_ACCESS:-}"
ADMIN_CIDRS="${PERSONA_ADMIN_CIDRS:-}"
ROLE="${PERSONA_ROLE:-}"
PAIR="${PERSONA_PAIR:-}"
MACHINE="${PERSONA_MACHINE:-}"
LISTEN="${PERSONA_LISTEN:-}"
LISTEN_SET="${PERSONA_LISTEN:+1}"
while [ $# -gt 0 ]; do
	case "$1" in
	--admin-access) ADMIN_ACCESS="${2:-}" && shift ;;
	--admin-access=*) ADMIN_ACCESS="${1#*=}" ;;
	--admin-cidrs) ADMIN_CIDRS="${2:-}" && shift ;;
	--admin-cidrs=*) ADMIN_CIDRS="${1#*=}" ;;
	--node) ROLE=node ;;
	--hub) ROLE=hub ;;
	--pair) PAIR="${2:-}" && shift ;;
	--pair=*) PAIR="${1#*=}" ;;
	--machine) MACHINE="${2:-}" && shift ;;
	--machine=*) MACHINE="${1#*=}" ;;
	--listen)
		LISTEN_SET=1
		case "${2:-}" in '' | -*) ;; *) LISTEN="$2" && shift ;; esac ;;
	--listen=*) LISTEN_SET=1 && LISTEN="${1#*=}" ;;
	--addr) ADDRS="${ADDRS:-} ${2:-}" && LISTEN_SET=1 && shift ;;
	--addr=*) ADDRS="${ADDRS:-} ${1#*=}" && LISTEN_SET=1 ;;
	*) echo "personad: unknown option $1 (--node [--pair <link> [--machine <id>] | --listen [addr] [--addr <host>]], --hub, --admin-access <loopback|lan|cidrs|any>, --admin-cidrs a,b)" >&2 && exit 2 ;;
	esac
	shift
done
if [ -n "$PAIR" ] || [ -n "$LISTEN_SET" ]; then
	[ "$ROLE" != hub ] || { echo "personad: --pair / --listen are for the node service, not --hub" >&2 && exit 2; }
	ROLE=node
fi
if [ -n "$PAIR" ] && [ -n "$LISTEN_SET" ]; then
	echo "personad: --pair (this computer dials the hub) or --listen (the hub dials it), not both" >&2
	exit 2
fi
case "$ROLE" in '' | hub | node) ;; *) echo "personad: PERSONA_ROLE must be hub or node" >&2 && exit 2 ;; esac
if [ -n "$ADMIN_ACCESS" ] && ! admin_access_valid "$ADMIN_ACCESS" "$ADMIN_CIDRS"; then
	echo "personad: --admin-access must be loopback, lan, cidrs (with --admin-cidrs) or any" >&2
	exit 2
fi

lexical_path() {
	rest="$1"
	result=
	while [ -n "$rest" ]; do
		part="${rest%%/*}"
		if [ "$rest" = "$part" ]; then rest=; else rest="${rest#*/}"; fi
		case "$part" in '' | .) ;; ..) result="${result%/*}" ;; *) result="$result/$part" ;; esac
	done
	printf '%s' "${result:-/}"
}

DL="${PERSONA_DL:-@PERSONA_DL@}"
HERDR_INSTALL="${PERSONA_HERDR_INSTALL:-https://herdr.dev/install.sh}"
STATE="${PERSONA_HOME:-}"
if [ -z "$STATE" ]; then
    if { [ -e "$HOME/.persona" ] || [ -L "$HOME/.persona" ]; } && { [ -e "$LEGACY_ROOT" ] || [ -L "$LEGACY_ROOT" ]; }; then
        [ "$HOME/.persona" -ef "$LEGACY_ROOT" ] || { echo 'personad: independent state directories; set PERSONA_HOME explicitly' >&2; exit 1; }
    fi
    if [ -e "$LEGACY_ROOT" ] || [ -L "$LEGACY_ROOT" ]; then STATE="$LEGACY_ROOT"; else STATE="$HOME/.persona"; fi
fi
# The published installer embeds the same credential-free service resolver used by deploy.sh.
IDENTITY_CODE='@PERSONA_IDENTITY@'
identity_script="$(dirname "$0")/../persona_identity.py"
if [ -f "$identity_script" ]; then
    resolved=$(python3 "$identity_script" --service "${ROLE:-auto}") || exit $?
    STATE="$resolved"
elif [ "$IDENTITY_CODE" != '@''PERSONA_IDENTITY@' ]; then
    command -v python3 >/dev/null 2>&1 || { echo 'personad: python3 is required to resolve existing service profiles safely' >&2; exit 1; }
    resolved=$(python3 -c "import base64; exec(base64.b64decode('$IDENTITY_CODE'))" --service "${ROLE:-auto}") || exit $?
    STATE="$resolved"
fi
case "$STATE" in '~') STATE="$HOME" ;; '~/'*) STATE="$HOME/${STATE#\~/}" ;; esac
case "$STATE" in /*) ;; *) STATE="$HOME/$STATE" ;; esac
STATE="$(lexical_path "$STATE")"
DEST="$STATE/bin"
export PERSONA_HOME="$STATE" JARVIS_HOME="$STATE"

fail() {
	echo "personad: $*" >&2
	exit 1
}

prepare_state() {
	if [ -L "$STATE" ] && [ ! -d "$STATE" ]; then
		fail "unavailable state symlink $STATE: restore its target before upgrading"
	fi
    mkdir -p "$STATE"
    case "$STATE" in
    "$HOME/.persona") [ -e "$LEGACY_ROOT" ] || [ -L "$LEGACY_ROOT" ] || ln -s .persona "$LEGACY_ROOT" ;;
    "$LEGACY_ROOT") [ -e "$HOME/.persona" ] || [ -L "$HOME/.persona" ] || ln -s .jarvis "$HOME/.persona" ;;
    esac
}

compat_command() {
    case "$1" in personad) old=jarvisd ;; persona-link) old=jarvis-link ;; *) return ;; esac
    [ ! -d "$DEST/$old" ] || fail "legacy command is a directory: $DEST/$old"
    ln -sf "$1" "$DEST/$old"
}

artifact_name() {
    current="$1"
    case "$current" in personad-*) old="jarvisd-${current#personad-}" ;; persona-link-*) old="jarvis-link-${current#persona-link-}" ;; *) old="$current" ;; esac
    for candidate in "$current" "$old"; do
        if awk -v n="$candidate.gz" '$2 == n || $2 == "*" n { found=1 } END { exit !found }' "$tmp/SHA256SUMS"; then
            printf '%s' "$candidate"
            return
        fi
    done
    fail "$current.gz is not in SHA256SUMS"
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "no build for $(uname -s) (Linux and macOS only)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "no build for $(uname -m)" ;;
esac
name="personad-$os-$arch"
archive="$name.gz"
command -v gzip >/dev/null 2>&1 || fail "needs gzip to unpack the download"

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q "$1" -O "$2"; }
else
	fail "needs curl or wget"
fi
if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	fail "needs sha256sum or shasum to check the download"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fetch "$DL/SHA256SUMS" "$tmp/SHA256SUMS" || fail "could not download $DL/SHA256SUMS"

# get <name> <installed as>: downloads <name>.gz, checks it against SHA256SUMS and puts it in $DEST.
get() {
	fetch_checked "$1"
	place "$1" "$2"
}

# fetch_checked <name>: downloads <name>.gz, checks it against SHA256SUMS and unpacks it to $tmp/<name>.
fetch_checked() {
    source=$(artifact_name "$1") || return 1
	archive="$source.gz"
	fetch "$DL/$archive" "$tmp/$archive" || fail "could not download $DL/$archive"
	want="$(awk -v n="$archive" '$2 == n || $2 == "*" n { print $1 }' "$tmp/SHA256SUMS")"
	[ -n "$want" ] || fail "$archive is not in SHA256SUMS"
	[ "$(sum "$tmp/$archive")" = "$want" ] || fail "checksum mismatch for $archive: not installed"
	gzip -dc "$tmp/$archive" > "$tmp/$1" || fail "could not unpack $archive: not installed"
	[ -s "$tmp/$1" ] || fail "$archive contains an empty binary: not installed"
}

# place <name> <installed as>: puts $tmp/<name> (fetch_checked) in $DEST.
place() {
	prepare_state
	mkdir -p "$DEST" || fail "could not create $DEST: not installed"
	# Copied beside the old one first, then renamed over it: the temporary directory can be another file system, and a
	# resident node running $DEST/$2 must never find it missing or half written.
	cp "$tmp/$1" "$DEST/.$2.new" && chmod 755 "$DEST/.$2.new" || fail "could not write $DEST/.$2.new: not installed"
	mv -f "$DEST/.$2.new" "$DEST/$2" || fail "could not install $DEST/$2"
    compat_command "$2"
}

# The device's own program (docs/operations/link.md "The device's program"): the node service on a device runs persona-node,
# at $DEST/persona-node. A computer that runs a hub or a manager keeps its node on personad, which their services run
# too and which updates with the node (hosts_hub_or_manager).
has_node_build() { awk -v n="persona-node-$os-$arch.gz" '$2 == n || $2 == "*" n { found = 1 } END { exit !found }' "$tmp/SHA256SUMS"; }
hosts_hub_or_manager() {
	[ -f "$STATE/hub.json" ] && return 0
	for f in "$STATE"/managers/*/manager.json; do
		if [ -f "$f" ]; then return 0; fi
	done
	return 1
}
# get_node_program: persona-node at $DEST/persona-node, beside whatever else is there (personad stays).
get_node_program() {
	get "persona-node-$os-$arch" persona-node
	echo "persona-node $("$DEST/persona-node" version 2>/dev/null || echo unknown) installed at $DEST/persona-node"
}

# The hub's and the built-in manager's own programs (docs/server/server.md "Programs"), on the computer that runs the
# hub: the same build as personad, installed with it, so that a service that runs one never runs an older one. A
# release without one leaves the file here as it was, unless a service runs it: then nothing is installed (the service
# would go on starting the older one beside the new personad). Other computers do not get them.
HUB_PROGRAMS="persona-hub persona-manager"
in_release() { awk -v n="$1.gz" '$2 == n || $2 == "*" n { found = 1 } END { exit !found }' "$tmp/SHA256SUMS"; }
# runs_hub: this computer runs the hub (--hub, a hub.json), or has the hub's programs from before.
runs_hub() {
	if [ "$ROLE" = hub ] || [ -f "$STATE/hub.json" ]; then return 0; fi
	for p in $HUB_PROGRAMS; do
		if [ -e "$DEST/$p" ]; then return 0; fi
	done
	return 1
}
# unit_forms <path>: the ways a systemd unit names <path>: as is, with systemd's %% and $$, and inside quotes as the
# hub's setup and hub-systemd.sh write it (\\ and \" too). One per line.
unit_forms() { printf '%s\n' "$1" "$1" "$1" | sed '2s/%/%%/g; 2s/\$/$$/g; 3s/[\\"]/\\&/g; 3s/%/%%/g; 3s/\$/$$/g'; }
# service_runs <program>: prints a file here that names $DEST/<program> (in a form of unit_forms, or under %h), or fails
# when there is none: a systemd unit or drop-in with it in an ExecStart line (any section; key and = may have spaces
# around them), or a launchd property list with it in a <string>. Followed by a space, a quote, "<" or the line's end:
# persona-hub.prev is not persona-hub. Wider than the hub's own check (docs/operations/hub-update.md "Programs swapped
# together"): a unit a later drop-in took back to personad, a disabled one and a wrapper (sh -c '...') count too.
service_runs() {
	forms=$(unit_forms "$DEST/$1")
	case $DEST in "$HOME"/*) forms="$forms
$(unit_forms "${DEST#"$HOME"}/$1" | sed 's/^/%h/')" ;; esac
	config="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user" data="${XDG_DATA_HOME:-$HOME/.local/share}/systemd/user"
	for f in "$config"/*.service "$config"/*.service.d/*.conf /etc/systemd/user/*.service /etc/systemd/user/*.service.d/*.conf \
		"$data"/*.service "$data"/*.service.d/*.conf /usr/lib/systemd/user/*.service /usr/lib/systemd/user/*.service.d/*.conf \
		/etc/systemd/system/*.service /etc/systemd/system/*.service.d/*.conf /usr/lib/systemd/system/*.service \
		/usr/lib/systemd/system/*.service.d/*.conf \
		"$HOME"/Library/LaunchAgents/*.plist /Library/LaunchAgents/*.plist /Library/LaunchDaemons/*.plist; do
		[ -f "$f" ] || continue
		# The paths go in through the environment: awk -v would read backslashes in them as escapes.
		if SERVICE_FORMS="$forms" awk '
			function names(s, p,  i, c) {
				i = index(s, p)
				if (p == "" || !i) return 0
				c = substr(s, i + length(p), 1)
				return c == "" || c == " " || c == "\t" || c == "\"" || c == "'\''" || c == "<"
			}
			BEGIN { n = split(ENVIRON["SERVICE_FORMS"], form, "\n") }
			/^[ \t]*ExecStart[ \t]*=/ || /<string>/ { for (k = 1; k <= n; k++) if (names($0, form[k])) { found = 1; exit } }
			END { exit !found }' "$f"; then
			echo "$f"
			return 0
		fi
	done
	return 1
}
# hub_programs: the ones this release has, as words; fails when it lacks one that a service here runs.
hub_programs() {
	for p in $HUB_PROGRAMS; do
		if in_release "$p-$os-$arch"; then
			printf '%s ' "$p"
		elif [ -e "$DEST/$p" ] && by=$(service_runs "$p"); then
			fail "this release has no $p for $os-$arch, and $by runs $DEST/$p: it would stay older than personad. Not installed; install a release that has it"
		elif [ -e "$DEST/$p" ]; then
			echo "this release has no $p for $os-$arch: $DEST/$p stays as it was (no service runs it)" >&2
		fi
	done
}
# get_hub_programs [personad]: the hub's programs (and personad first with "personad"), each downloaded and checked
# before any is put in place. Once only.
get_hub_programs() {
	[ -z "${HUB_PROGRAMS_FETCHED:-}" ] || return 0
	programs=$(hub_programs) || exit 1
	if [ "${1:-}" = personad ]; then fetch_checked "$name"; fi
	for p in $programs; do fetch_checked "$p-$os-$arch"; done
	if [ "${1:-}" = personad ]; then place "$name" personad; fi
	for p in $programs; do
		place "$p-$os-$arch" "$p"
		echo "$p $("$DEST/$p" version 2>/dev/null || echo unknown) installed at $DEST/$p"
	done
	HUB_PROGRAMS_FETCHED=1
}

# The node service given up front (--node) on a device: persona-node and Persona Link, no personad. Everything else:
# personad, as before (the questions below may add persona-node: node_service); with the hub's programs on the
# computer that runs the hub.
if [ "$ROLE" = node ] && has_node_build && ! hosts_hub_or_manager; then
	get_node_program
	program=persona-node
	# A personad here from before (the node service may still run it) is brought up to date too: it is what the
	# service goes back to when the switch to persona-node does not work out.
	if [ -e "$DEST/personad" ]; then get "$name" personad; fi
elif runs_hub; then
	get_hub_programs personad
	program=personad
	version="$("$DEST/personad" version 2>/dev/null || echo unknown)"
	echo "personad $version installed at $DEST/personad"
else
	get "$name" personad
	program=personad
	version="$("$DEST/personad" version 2>/dev/null || echo unknown)"
	echo "personad $version installed at $DEST/personad"
fi
case ":$PATH:" in
*":$DEST:"*) ;;
*) echo "(not on PATH: run it as $DEST/$program, or add $DEST to PATH)" ;;
esac

# The places the node looks besides PATH (server/internal/node/node.go: ssh sessions get a minimal PATH).
has_herdr() {
	command -v herdr >/dev/null 2>&1 && return 0
	for d in "$HOME/.local/bin" "$HOME/bin" /opt/homebrew/bin /usr/local/bin; do
		[ -x "$d/herdr" ] && return 0
	done
	return 1
}
if [ "${PERSONA_SKIP_HERDR:-}" = 1 ]; then
	:
elif has_herdr; then
	echo "herdr: already installed"
else
	echo "herdr: not found, installing it (PERSONA_SKIP_HERDR=1 skips this)"
	if fetch "$HERDR_INSTALL" "$tmp/herdr-install.sh" && sh "$tmp/herdr-install.sh" && has_herdr; then
		echo "herdr: installed"
	else
		echo "personad: warning: could not install herdr; install it yourself (https://herdr.dev) before using Agents on this computer" >&2
	fi
fi
# fetch_link puts persona-link (the connector, Persona Link) at $DEST/persona-link, checked against SHA256SUMS like
# personad. Only the canonical command is installed.
fetch_link() {
	[ -z "${LINK_FETCHED:-}" ] || return 0
    lname=$(artifact_name "persona-link-$os-$arch") || return 1
	larchive="$lname.gz"
	fetch "$DL/$larchive" "$tmp/$larchive" || { echo "personad: could not download $DL/$larchive" >&2 && return 1; }
	lwant="$(awk -v n="$larchive" '$2 == n || $2 == "*" n { print $1 }' "$tmp/SHA256SUMS")"
	[ -n "$lwant" ] || { echo "personad: $larchive is not in SHA256SUMS" >&2 && return 1; }
	[ "$(sum "$tmp/$larchive")" = "$lwant" ] || { echo "personad: checksum mismatch for $larchive: not installed" >&2 && return 1; }
	gzip -dc "$tmp/$larchive" > "$tmp/$lname" && [ -s "$tmp/$lname" ] || { echo "personad: could not unpack $larchive: not installed" >&2 && return 1; }
	cp "$tmp/$lname" "$DEST/.persona-link.new" && chmod 755 "$DEST/.persona-link.new" && mv -f "$DEST/.persona-link.new" "$DEST/persona-link" ||
		{ echo "personad: could not install $DEST/persona-link" >&2 && return 1; }
    compat_command persona-link
	LINK_FETCHED=1
	echo "persona-link $("$DEST/persona-link" version 2>/dev/null || echo unknown) installed at $DEST/persona-link"
}

# node_service: the node service, chosen at the questions below: with the device's own program when the release has it.
node_service() {
	if has_node_build && ! hosts_hub_or_manager; then get_node_program; fi
	install_node
}

# install_node makes this computer the node service: the resident node and the connector, paired with the hub
# (docs/operations/link.md). persona-link does the work; this only picks how it pairs.
install_node() {
	fetch_link || fail "the node service needs Persona Link (persona-link)"
	if [ -z "$PAIR" ] && [ -z "$LISTEN_SET" ] && (: </dev/tty) 2>/dev/null; then
		cat >/dev/tty <<'ASK'
这台电脑怎么和 hub 连？
  1) 我去连 hub：在 hub 机器上运行 personad hub pair --machine <这台的 id> --add，把拿到的链接粘贴过来（笔记本、在 NAT 后面的电脑）
  2) 让 hub 来连：这台监听一个端口，打印一条链接粘贴到 hub 管理界面「电脑 › 添加电脑 › hub 来连」（服务器、地址固定的电脑）
ASK
		printf '选哪个？[1-2，回车 = 1] ' >/dev/tty
		read -r how </dev/tty || how=
		case "$how" in
		2) LISTEN_SET=1 ;;
		*)
			printf '粘贴配对链接（回车跳过，以后再 persona-link pair）：' >/dev/tty
			read -r PAIR </dev/tty || PAIR=
			;;
		esac
	fi
	set --
	if [ -n "$PAIR" ]; then
		set -- --pair "$PAIR"
		[ -z "$MACHINE" ] || set -- "$@" --machine "$MACHINE"
	elif [ -n "$LISTEN_SET" ]; then
		set -- --listen
		[ -z "$LISTEN" ] || set -- "$@" "$LISTEN"
		for a in ${ADDRS:-}; do set -- "$@" --addr "$a"; done # addresses have no spaces
	fi
	if [ "$os" = darwin ]; then
		# On a Mac the connector is the menu bar app (Persona Link, docs/operations/link.md); here: the resident node and the pairing.
		"$DEST/persona-link" node install || return 1
		case "${1:-}" in
		--pair) "$DEST/persona-link" pair "$PAIR" ${MACHINE:+--machine "$MACHINE"} || return 1 ;;
		--listen) shift && "$DEST/persona-link" pair --listen "$@" || return 1 ;;
		esac
		switch_node
		echo "Mac 上的连接器是菜单栏 App「Persona Link」：装上它（它带着连接器和常驻 node 的程序，配对也在它的菜单里）；没有 App 时先前台跑 $DEST/persona-link run。"
		return 0
	fi
	"$DEST/persona-link" install "$@" || return 1
	switch_node
}

# node_runs_persona_node: this computer's node service (persona-link's systemd unit or launchd agent, whoever set it up,
# the Persona Link Mac app included) runs persona-node.
node_runs_persona_node() {
	for f in "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/personad-node.service" "$HOME/Library/LaunchAgents/com.persona.node.plist"; do
		if [ -f "$f" ] && grep -q '/persona-node[" <]' "$f"; then return 0; fi
	done
	return 1
}
# unswitch_node: a computer that is to run a hub keeps its node on personad, which updates with the node: a node service
# that runs persona-node moves back (persona-link node switch --to personad, with the release's persona-link, or the one
# here when that cannot be downloaded).
unswitch_node() {
	node_runs_persona_node || return 0
	if { fetch_link || [ -x "$DEST/persona-link" ]; } && "$DEST/persona-link" node switch --to personad; then return 0; fi
	if [ -x "$DEST/persona-link" ]; then link_cmd="$DEST/persona-link"; else link_cmd="persona-link (install Persona Link first)"; fi
	echo "personad: warning: the node service still runs persona-node; run $link_cmd node switch --to personad (on a Mac, in Terminal on the Mac)" >&2
	return 0
}

# switch_node: a node service that runs personad on a device moves over to persona-node (persona-link node switch:
# whole or not at all, back on personad when persona-node does not answer). A new service runs persona-node already.
switch_node() {
	if [ -x "$DEST/persona-node" ] && ! hosts_hub_or_manager; then
		"$DEST/persona-link" node switch || echo "personad: warning: the node service stays on personad (see above); retry with $DEST/persona-link node switch (on a Mac, in Terminal on the Mac)" >&2
	fi
	return 0
}

# Who may open the admin UI, on the machine that runs the hub (one with a hub.json, or one the person says is).
set_admin_access() {
	if [ "$ADMIN_ACCESS" = cidrs ]; then
		"$DEST/personad" hub admin access cidrs --cidrs "$ADMIN_CIDRS"
	else
		"$DEST/personad" hub admin access "$ADMIN_ACCESS"
	fi
}
if [ "$ROLE" = node ]; then
	install_node
	exit $?
fi
if [ -n "$ADMIN_ACCESS" ]; then
	set_admin_access
elif [ -f "$STATE/hub.json" ]; then
	if [ -z "$("$DEST/personad" hub admin access --get 2>/dev/null || true)" ]; then
		if ask_admin_access; then set_admin_access; else admin_access_reminder; fi
	fi
elif [ "$ROLE" = hub ]; then
	if ask_admin_access; then set_admin_access; else admin_access_reminder; fi
elif (: </dev/tty) 2>/dev/null; then
	# A hub on this LAN already: this computer only needs the node service (docs/archive/operations/plan-hub-center.md section 0).
	found=
	if fetch_link 2>/dev/null; then found="$("$DEST/persona-link" discover --wait 3 2>/dev/null || true)"; fi
	if [ -n "$found" ]; then
		{
			echo "局域网里已经有 hub："
			echo "$found" | sed 's/^/  /'
			echo "全家只要一台 hub；这台装 node 服务（常驻 node + 连接器）就够了。"
		} >/dev/tty
		printf '装成 node 服务？[Y/n] ' >/dev/tty
		read -r as_node </dev/tty || as_node=
		case "$as_node" in n | N | no) ;; *) node_service && exit 0 || exit 1 ;; esac
	fi
	printf '这台电脑要跑 hub 吗？[y/N] ' >/dev/tty
	read -r is_hub </dev/tty || is_hub=
	case "$is_hub" in
	y | Y | yes)
		ROLE=hub
		if ask_admin_access; then set_admin_access; else admin_access_reminder; fi
		;;
	*)
		printf '装成 node 服务、连到你的 hub？[Y/n] ' >/dev/tty
		read -r as_node </dev/tty || as_node=
		case "$as_node" in n | N | no) ;; *) node_service && exit 0 || exit 1 ;; esac
		;;
	esac
else
	admin_access_reminder if-hub
fi

# The machine that runs the hub: personad hub setup does the rest (hub.json, the database, the service, the phone's
# QR code and the admin URL, docs/assistant/onboarding.md 4.1). Running it again is safe, so an upgrade with --hub runs it too.
if [ "$ROLE" = hub ]; then
	unswitch_node
	get_hub_programs # the person said so at the question above, after personad was installed
	echo ""
	echo "接着把这台电脑设成 hub（$DEST/personad hub setup）："
	if "$DEST/personad" hub setup; then
		exit 0
	fi
	echo "hub 还没装好：按上面的「下一步」处理后，运行 $DEST/personad hub setup 接着装（已经做好的不会重做）。" >&2
	exit 1
fi
# A hub that has run has a token in hub.json; set_admin_access above may have written one with admin.access only.
if [ -f "$STATE/hub.json" ] && grep -Eq '"token"[[:space:]]*:[[:space:]]*"[^"]' "$STATE/hub.json"; then
	echo "这台已经是 hub，personad 升级好了；重启 hub 后用上新版本（Linux：systemctl --user restart personad）。要检查一遍：$DEST/personad hub setup"
	exit 0
fi
echo "要让这台电脑跑 hub：运行 $DEST/personad hub setup（或重跑安装命令并加 --hub）。全家只要一台 hub。"
echo "Messaging connectors are installed from the hub extension marketplace."
echo "其他电脑装 node 服务：安装命令加 --node --pair '<personad hub pair --machine <id> 打印的链接>'（或 --listen：让 hub 来连）。"
