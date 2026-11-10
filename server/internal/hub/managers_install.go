package hub

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/pgdocker"
)

// How a new manager is installed (docs/assistant/managers.md, "Install commands"): the app shows the commands that come with its install
// code. `curl -fsSL <hub>/i/<code> | sh` gets a script from the hub that finds or downloads personad, trades the code
// (`personad manager join … --install`) and installs the manager as a service of that computer; `docker run …` runs
// it as a container of the hub's image. GET /i/<code> needs no credential (the code is it) and does not use the code
// up: only the join does. Unknown or used codes count towards the source's limit, as at the join.

// installScriptPath is where the script is: /i/<code>.
const installScriptPath = "/i/"

// managerAddress is the hub as a manager installed from request r reaches it, with fp the hub's TLS pin when that is
// its own TLS port (its self-signed certificate), else "".
//
//   - Straight from the app: the address r came to (it reached the hub from where the command runs), https when it
//     came over TLS or through a proxy that says so, pinned when it came to the hub's own TLS port. An https
//     public_url goes first: the address the user set up for the world (a reverse proxy, a subpath). An http one does
//     not: every hub writes one for its host name (docs/operations/config.md), only for the private network.
//   - Through a member hub (the Host is the mesh's, "peer", which no computer reaches): this hub's own addresses
//     (ownAddress).
func (h *Hub) managerAddress(r *http.Request) (base, fp string) {
	if _, forwarded := forwardedOf(r); forwarded {
		if base, fp, ok := h.ownAddress(); ok {
			return base, fp
		}
	} else if base, fp, ok := h.addressOf(h.cfg.PublicURL); ok && strings.HasPrefix(base, "https://") {
		return base, fp
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	if r.TLS != nil {
		_, fp = h.tlsPin()
	}
	host := r.Host
	if host == "" {
		return strings.TrimRight(h.cfg.PublicURL, "/"), ""
	}
	return scheme + "://" + host, fp
}

// ownAddress is this hub as computers reach it, from its own configuration, most secure first: its endpoints on its
// own TLS port (pinned), an https public_url, its other https endpoints, an http public_url, then any other endpoint.
func (h *Hub) ownAddress() (base, fp string, ok bool) {
	port, _ := h.tlsPin()
	eps := h.endpointList()
	pub := h.cfg.PublicURL
	pubTLS := strings.HasPrefix(strings.ToLower(strings.TrimSpace(pub)), "https://")
	var urls []string
	for _, e := range eps {
		if e.Scheme == "https" && port > 0 && e.Port == port {
			urls = append(urls, e.URL)
		}
	}
	if pubTLS {
		urls = append(urls, pub)
	}
	for _, e := range eps {
		if e.Scheme == "https" {
			urls = append(urls, e.URL)
		}
	}
	if !pubTLS {
		urls = append(urls, pub)
	}
	for _, e := range eps {
		urls = append(urls, e.URL)
	}
	for _, raw := range urls {
		if base, fp, ok := h.addressOf(raw); ok {
			return base, fp, true
		}
	}
	return "", "", false
}

// addressOf is raw (an http(s) URL of the hub's configuration) as the commands' hub address: its path kept (a reverse
// proxy's subpath, docs/operations/network.md), with the hub's TLS pin when it is https to the hub's own TLS port.
func (h *Hub) addressOf(raw string) (base, fp string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", false
	}
	if u.Scheme == "https" {
		port, pin := h.tlsPin()
		p := u.Port()
		if p == "" {
			p = "443"
		}
		if port > 0 && p == strconv.Itoa(port) {
			fp = pin
		}
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), fp, true
}

// managerCommands are the commands that install manager id with code, from where r came: service (a computer, the
// hub's own included), join (one that has personad already), docker (when the hub knows its image: the script that
// starts the manager and its own Postgres, two containers).
func (h *Hub) managerCommands(r *http.Request, id, code string) map[string]string {
	base, fp := h.managerAddress(r)
	curl := "curl -fsSL"
	join := fmt.Sprintf("personad manager join %s %s --install", shellWord(base), code)
	if fp != "" {
		curl += " -k --pinnedpubkey " + shellWord("sha256//"+curlPin(fp))
		join += " --fingerprint " + fp
	}
	out := map[string]string{
		"service": fmt.Sprintf("%s %s | sh", curl, shellWord(base+installScriptPath+code)),
		"join":    join,
	}
	if h.managerImage() != "" {
		out["docker"] = fmt.Sprintf("%s %s | sh", curl, shellWord(base+installScriptPath+code+"/docker"))
	}
	return out
}

// versionLabel is " <version>" for the script's comment, "" without one.
func versionLabel(v string) string {
	if v == "" || !shellSafeName(v) {
		return ""
	}
	return " " + v
}

// shellSafeName: s can go between single quotes and in a comment as it is (an archive name, a version).
func shellSafeName(s string) bool {
	return s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+-") == ""
}

// shellSafeHex: s is a sha256 in lower-case hex.
func shellSafeHex(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// managerImage is the image a manager's container runs: the one this Docker hub runs, else the one it was built as;
// "" when neither is known.
func (h *Hub) managerImage() string {
	if image := identity.Getenv("PERSONA_IMAGE"); image != "" {
		return image
	}
	return h.build.Image
}

// managerDockerScript is the POSIX sh script GET /i/<code>/docker answers (docs/assistant/managers.md, "Containers"): manager id
// in a container beside a Postgres container of its own, on a network of their own. Names: network persona-mgr-<id>,
// containers persona-mgr-<id> and persona-mgr-<id>-pg, volumes persona-mgr-<id>-home and persona-mgr-<id>-data.
//
// The database password is made up by the shell that runs it (the hub never knows it) and goes through the
// environment only (-e NAME with no value): never on a command line, where `ps` and the containers' Cmd would show it.
// It can run again: what is there is kept, and an existing database's password is read back (from its container, or
// from the manager's manager.json in its volume) instead of made up anew, which Postgres would not take.
func managerDockerScript(id, image, base, code, fp string) string {
	q := shellWord
	fpArgs := ""
	if fp != "" {
		fpArgs = " --fingerprint " + fp
	}
	return `#!/bin/sh
# Persona: run the manager ` + id + ` in Docker, beside a Postgres of its own (docs/assistant/managers.md, "Containers").
set -eu
N=` + q("persona-mgr-"+id) + `
ID=` + q(id) + `
IMAGE=` + q(image) + `
HUB=` + q(base) + `
CODE=` + q(code) + `
fail() { echo "persona manager: $*" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || fail "needs docker"
docker info >/dev/null 2>&1 || fail "docker is not running, or this user may not use it"
docker network inspect "$N" >/dev/null 2>&1 || docker network create "$N" >/dev/null
pw=
if docker inspect "$N-pg" >/dev/null 2>&1; then
	pw=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$N-pg" | sed -n 's/^POSTGRES_PASSWORD=//p')
elif docker volume inspect "$N-data" >/dev/null 2>&1; then
	# Its database is there from before: the password is the one in its manager.json, when that volume is there too.
	pw=$(docker run --rm -v "$N-home:/home/persona/.persona" --entrypoint sh "$IMAGE" -c \
		"sed -n 's|.*postgres://` + pgdocker.User + `:\([0-9a-f]*\)@.*|\1|p' /home/persona/.persona/managers/$ID/manager.json" 2>/dev/null || true)
	[ -n "$pw" ] || fail "the volume $N-data holds a database whose password is gone: remove it (docker volume rm $N-data, its data with it) and run this again"
fi
[ -n "$pw" ] || pw=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
if ! docker inspect "$N-pg" >/dev/null 2>&1; then
	POSTGRES_PASSWORD=$pw docker run -d --name "$N-pg" --network "$N" --restart unless-stopped \
		-v "$N-data:/var/lib/postgresql" -e POSTGRES_USER=` + pgdocker.User + ` -e POSTGRES_DB=` + pgdocker.Database + ` -e POSTGRES_PASSWORD ` + pgdocker.Image + ` ` + strings.Join(pgdocker.ServerArgs, " ") + ` >/dev/null
fi
docker start "$N-pg" >/dev/null
if docker inspect "$N" >/dev/null 2>&1; then
	# One that never joined (no manager.json in its volume: the hub refused its code, and it waits for one) is made
	# again with this script's code; one that joined is started as it is, and so is one docker could not look into.
	joined=$(docker run --rm -v "$N-home:/home/persona/.persona" --entrypoint sh "$IMAGE" -c \
		"if [ -f /home/persona/.persona/managers/$ID/manager.json ]; then echo yes; else echo no; fi" 2>/dev/null || true)
	if [ "$joined" != no ]; then
		docker start "$N" >/dev/null
		echo "persona manager: $N was there already: started it"
		exit 0
	fi
	docker rm -f "$N" >/dev/null
	echo "persona manager: $N never joined the hub: making it again with this install code"
fi
` + databaseEnvName + `="postgres://` + pgdocker.User + `:$pw@$N-pg:5432/` + pgdocker.Database + `?sslmode=disable" docker run -d --name "$N" --network "$N" \
	--restart unless-stopped -v "$N-home:/home/persona/.persona" -e ` + databaseEnvName + ` "$IMAGE" \
	manager run --dir "/home/persona/.persona/managers/$ID" --join "$HUB" "$CODE"` + fpArgs + ` >/dev/null
echo "persona manager: started $N and its Postgres $N-pg (volumes $N-home, $N-data); logs: docker logs -f $N"
`
}

// databaseEnvName is the variable a manager's container gets its database URL in (manager.databaseEnv).
const databaseEnvName = "PERSONA_MANAGER_DATABASE_URL"

// curlPin is a pin (base64url of the key's sha256, no padding) as curl's --pinnedpubkey wants it (standard base64).
func curlPin(fp string) string {
	b, err := base64.RawURLEncoding.DecodeString(fp)
	if err != nil {
		return fp
	}
	return base64.StdEncoding.EncodeToString(b)
}

// shellWord quotes s for sh unless it is plain.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// managerInstallRoutes mounts GET /i/<code> beside the join, outside the apps' auth.
func (h *Hub) managerInstallRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /i/{code}", func(w http.ResponseWriter, r *http.Request) { h.serveInstallScript(w, r, false) })
	// The same for a container: the manager and its own Postgres (managerDockerScript), when the hub knows its image.
	mux.HandleFunc("GET /i/{code}/docker", func(w http.ResponseWriter, r *http.Request) { h.serveInstallScript(w, r, true) })
}

// serveInstallScript answers GET /i/<code> (docker: …/docker) with its script.
func (h *Hub) serveInstallScript(w http.ResponseWriter, r *http.Request, docker bool) {
	if h.sourceOf(r) == sourcePublic && r.TLS == nil && !h.cfg.Security.AllowPublicPlaintext {
		// The code (and the script with it) would cross the internet in the clear: refused, and the code, which just
		// did, voided.
		h.refusePlaintext(w, r, r.PathValue("code"))
		return
	}
	key := authSourceKey(r)
	if retry := h.devtok.failures.retry(key, time.Now(), h.cfg.Security); retry > 0 {
		retryAfter(w, retry)
		h.securityError(w, r, http.StatusTooManyRequests, "rate_limited", 0)
		return
	}
	code := r.PathValue("code")
	s := h.managerStore()
	if s == nil {
		http.Error(w, "no database", http.StatusServiceUnavailable)
		return
	}
	if !installCodeShape.MatchString(code) {
		h.devtok.failures.fail(key, time.Now(), h.cfg.Security)
		http.Error(w, "echo 'persona: this install code is unknown, expired or used already' >&2; exit 1", http.StatusNotFound)
		return
	}
	c, err := s.ManagerInstallCodeByHash(r.Context(), hashToken(code))
	if err != nil {
		http.Error(w, "the hub's database did not answer", http.StatusServiceUnavailable)
		return
	}
	if c == nil {
		h.devtok.failures.fail(key, time.Now(), h.cfg.Security)
		http.Error(w, "echo 'persona: this install code is unknown, expired or used already: get a new one in the app' >&2; exit 1", http.StatusNotFound)
		return
	}
	base, fp := h.managerAddress(r)
	var script string
	if docker {
		image := h.managerImage()
		if image == "" {
			http.Error(w, "echo 'persona: this hub does not know which image a manager container runs' >&2; exit 1", http.StatusNotFound)
			return
		}
		script = managerDockerScript(c.ManagerID, image, base, code, fp)
	} else {
		h.upd.freshRelease(r.Context()) // the sums of the release the download will be
		version, sums := h.releaseSums()
		script = managerInstallScript(managerScript{ID: c.ManagerID, Name: c.Name, Hub: base, Code: code,
			Fingerprint: fp, DL: strings.TrimRight(h.build.UpdateURL, "/"), Version: version, Sums: sums,
			HubVersion: h.build.Version})
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(script))
}

// managerScript is what the install script is made of.
type managerScript struct {
	ID, Name, Hub, Code, Fingerprint string
	DL                               string // where the publisher's binaries are (the build's update URL); "" none
	// Version and Sums are the published release the hub last read (latest.json: its version, and each download's
	// sha256 by archive name): the script checks the download against these, which came from the hub (over its
	// pinned TLS port when the command used it), not against a SHA256SUMS from the same place as the download.
	Version string
	Sums    map[string]string
	// HubVersion is this hub's own, to say so when the published release is another.
	HubVersion string
}

// releaseSums are the published release's version and its downloads' sha256s by archive name, as the hub last read
// latest.json; "" and nil before it did (or for a build that does not update itself).
func (h *Hub) releaseSums() (string, map[string]string) {
	if h.upd == nil {
		return "", nil
	}
	h.upd.mu.Lock()
	r := h.upd.release
	h.upd.mu.Unlock()
	if r == nil || r.Manifest == nil || len(r.Manifest.Files) == 0 {
		return "", nil
	}
	sums := map[string]string{}
	for _, f := range r.Manifest.Files {
		if f.Name != "" && f.SHA256 != "" {
			sums[f.Name] = f.SHA256
		}
	}
	return r.Manifest.Version, sums
}

// managerInstallScript is the POSIX sh script GET /i/<code> answers. It uses the personad of this computer (the
// hub's own on the hub machine) or downloads the published release the way install.sh does, checked against the
// sha256 the hub put in the script (m.Sums; SHA256SUMS from the download site only when the hub had none), then joins
// and installs. Arguments after `sh -s --` go to the join (--dir, --database-url).
func managerInstallScript(m managerScript) string {
	q := shellWord
	names := make([]string, 0, len(m.Sums))
	for n := range m.Sums {
		if shellSafeName(n) && shellSafeHex(m.Sums[n]) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, n := range names {
		sums.WriteString(m.Sums[n] + "  " + n + "\n")
	}
	versionNote := ""
	if shellSafeName(m.Version) && shellSafeName(m.HubVersion) && m.Version != m.HubVersion {
		versionNote = "\techo \"persona manager: this hub runs " + m.HubVersion + "; installing the published " + m.Version + "\"\n"
	}
	name := strings.NewReplacer("\n", " ", "\r", " ").Replace(m.Name)
	fp := ""
	if m.Fingerprint != "" {
		fp = "--fingerprint " + m.Fingerprint
	}
	return `#!/bin/sh
# Persona: install the manager ` + q(name) + ` (` + m.ID + `) on this computer and connect it to the hub
# (docs/assistant/managers.md). Arguments after sh -s -- go to personad manager join: --dir <directory>, --database-url <url>.
set -eu
HUB=` + q(m.Hub) + `
CODE=` + q(m.Code) + `
DL=` + q(m.DL) + `
# The published downloads' sha256s, as the hub knows them (latest.json` + versionLabel(m.Version) + `); empty: the hub had none.
SUMS='` + sums.String() + `'
fail() { echo "persona manager: $*" >&2; exit 1; }

find_personad() {
	for c in "${PERSONA_HOME:-$HOME/.persona}/bin/personad" "$HOME/.persona/bin/personad"; do
		[ -x "$c" ] && { printf '%s\n' "$c"; return 0; }
	done
	command -v personad 2>/dev/null && return 0
	return 1
}

supports_manager_config() {
	capabilities=$("$1" manager capabilities --json 2>/dev/null) || return 1
	printf '%s\n' "$capabilities" | grep -Fq '"` + assistantwire.CapabilityConfigOwn + `"'
}

BIN="$(find_personad || true)"
if [ -n "$BIN" ] && ! supports_manager_config "$BIN"; then
	echo "persona manager: $BIN lacks config.own; downloading a compatible release before joining" >&2
	BIN=
fi
if [ -z "$BIN" ]; then
	[ -n "$DL" ] || fail "no compatible personad (requires config.own), and the hub does not know where to download it: update personad first (docs/operations/release.md), then run this again"
	case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail "no build for $(uname -s)" ;; esac
	case "$(uname -m)" in x86_64 | amd64) arch=amd64 ;; arm64 | aarch64) arch=arm64 ;; *) fail "no build for $(uname -m)" ;; esac
	if command -v curl >/dev/null 2>&1; then fetch() { curl -fsSL "$1" -o "$2"; }
	elif command -v wget >/dev/null 2>&1; then fetch() { wget -q "$1" -O "$2"; }
	else fail "needs curl or wget"; fi
	if command -v sha256sum >/dev/null 2>&1; then sum() { sha256sum "$1" | cut -d' ' -f1; }
	elif command -v shasum >/dev/null 2>&1; then sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
	else fail "needs sha256sum or shasum to check the download"; fi
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	archive="personad-$os-$arch.gz"
` + versionNote + `	if [ -n "$SUMS" ]; then
		printf '%s' "$SUMS" > "$tmp/SHA256SUMS"
	else
		echo "persona manager: the hub knows no checksums for the release: checking against $DL/SHA256SUMS" >&2
		fetch "$DL/SHA256SUMS" "$tmp/SHA256SUMS" || fail "could not download $DL/SHA256SUMS"
	fi
	fetch "$DL/$archive" "$tmp/$archive" || fail "could not download $DL/$archive"
	want="$(awk -v n="$archive" '$2 == n || $2 == "*" n { print $1 }' "$tmp/SHA256SUMS")"
	[ -n "$want" ] && [ "$(sum "$tmp/$archive")" = "$want" ] || fail "checksum mismatch for $archive: not installed. A release may have come out since the hub last checked: get the install command from the app again"
	gzip -dc "$tmp/$archive" > "$tmp/personad" && chmod 755 "$tmp/personad" || fail "could not unpack $archive"
	supports_manager_config "$tmp/personad" || fail "the published personad lacks config.own: publish or install an updated release, then run this again; the install code and existing binary are unchanged"
	dest="${PERSONA_HOME:-$HOME/.persona}/bin"
	mkdir -p "$dest"
	mv "$tmp/personad" "$dest/personad" || fail "could not install $archive"
	BIN="$dest/personad"
	echo "personad installed at $BIN"
fi
"$BIN" manager join "$HUB" "$CODE" ` + fp + ` --install "$@"
# A computer that runs a manager keeps its node on personad, which updates with the node: a node service (persona-link's
# systemd unit or launchd agent) that runs the device's own program, persona-node, moves back (docs/operations/link.md "The
# device's program").
# persona-link is where install.sh puts it, in the Persona Link Mac app, or on PATH.
find_link() {
	for c in "${PERSONA_HOME:-$HOME/.persona}/bin/persona-link" "$HOME/.persona/bin/persona-link" \
		"/Applications/Persona Link.app/Contents/Helpers/persona-link" "$HOME/Applications/Persona Link.app/Contents/Helpers/persona-link"; do
		[ -x "$c" ] && { printf '%s\n' "$c"; return 0; }
	done
	command -v persona-link 2>/dev/null && return 0
	return 1
}
for unit in "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/personad-node.service" "$HOME/Library/LaunchAgents/com.persona.node.plist"; do
	[ -f "$unit" ] && grep -q '/persona-node[" <]' "$unit" || continue
	link="$(find_link || true)"
	if [ -n "$link" ]; then
		"$link" node switch --to personad || echo "persona manager: the node service still runs persona-node; run '$link' node switch --to personad" >&2
	else
		echo "persona manager: the node service runs persona-node, and this computer has no persona-link to move it to personad: install Persona Link (docs/operations/link.md), then run persona-link node switch --to personad" >&2
	fi
done
`
}
