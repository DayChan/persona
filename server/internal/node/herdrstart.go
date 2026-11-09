package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A machine that rebooted has the node back (launchd / systemd start it) but not herdr: nobody opened a terminal and
// ran it. Instead of refusing every start and resume until someone does, the node starts herdr's headless server
// itself when it needs one (#1879, docs/integrations/engines.md "When herdr is not running"): when it starts, and before a start or resume.
// Only classic herdr (engines herdr and herdr-both), never Herdr App's herdr-core: that has its own lifecycle.
//
// The server runs in a clean login shell (env -i + $SHELL -l): herdr hands its environment to every pane and the
// agents in them, and the node's own (a launcher's ANTHROPIC_*, proxy variables, launchd's short PATH) must not leak
// into them. One start at a time (a mutex, and a lock on herdr's config directory against a second node); the user
// can turn it off per machine (hub.json machines[].herdr_autostart false), and then herdr stays down after
// `herdr server stop` as before.

// herdrStarter is the node's state of starting herdr.
type herdrStarter struct {
	mu   sync.Mutex  // one start at a time
	off  atomic.Bool // the hub said herdr_autostart false
	said chan struct{}
	once sync.Once
	init sync.Once
}

func (s *herdrStarter) saidCh() chan struct{} {
	s.init.Do(func() { s.said = make(chan struct{}) })
	return s.said
}

// setHerdrAutostart is the hub's word (node.configure herdr_autostart): whether the node may start herdr.
func (n *Node) setHerdrAutostart(on bool) {
	n.hstart.off.Store(!on)
	ch := n.hstart.saidCh()
	n.hstart.once.Do(func() { close(ch) })
}

// herdrReadyWait is how long a started server has to answer on its socket; herdrDialWait bounds one probe.
var herdrReadyWait, herdrDialWait = 20 * time.Second, time.Second

// launchHerdr starts herdr's server detached from the node (herdrstart_unix.go); tests replace it, and the package's
// tests never start a real herdr (herdrstart_test.go).
var launchHerdr = launchHerdrServer

// findHerdr is herdrBinary; tests replace it.
var findHerdr = herdrBinary

// errHerdrNotInstalled: neither the node's PATH nor the login shell has a herdr command.
var errHerdrNotInstalled = errors.New("herdr is not installed (no herdr command in PATH or the login shell)")

// herdrAlive says whether something answers on herdr's socket (a stale socket file refuses the connection).
func herdrAlive(ctx context.Context, sock string) bool {
	if sock == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, herdrDialWait)
	defer cancel()
	c, err := herdr.Dial(ctx, sock) // a named pipe on Windows
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// ensureHerdr starts herdr's server when the engine is classic herdr, starting it is on, and its socket does not
// answer; it returns once the socket does. nil also when there is nothing to do (another engine, turned off, already
// running): callers go on and report what herdr then says. why goes into the log.
func (n *Node) ensureHerdr(ctx context.Context, why string) error {
	if !n.hostEnabled(proto.EngineHerdr) {
		return nil
	}
	h, ok := n.hosts.Get(host.Herdr).(*herdrHost)
	if !ok || h.c == nil || n.hstart.off.Load() {
		return nil
	}
	return n.startOrInstallHerdr(ctx, h, why, n.hinstall.auto.Load()) // hub.json machines[].herdr_autoinstall
}

// startHerdrServer is herdr.server.start: the user pressed "Start" in the Agents list (herdr_autostart off does not stop
// that — the switch only says whether the node may do it on its own), installing herdr first when there is none.
// It reports why when it could not.
func (n *Node) startHerdrServer(ctx context.Context) (map[string]any, error) {
	if !n.hostEnabled(proto.EngineHerdr) {
		return nil, fmt.Errorf("这台电脑没有启用 herdr 引擎")
	}
	h, ok := n.hosts.Get(host.Herdr).(*herdrHost)
	if !ok || h.c == nil {
		return nil, fmt.Errorf("这台电脑没有 herdr")
	}
	if err := n.startOrInstallHerdr(ctx, h, "the user asked for it", true); err != nil { // the user's "Start" installs it
		return nil, err
	}
	return map[string]any{"running": herdrAlive(ctx, h.c.Socket)}, nil
}

// startHerdr starts herdr's server unless it already answers, whatever asked for it.
func (n *Node) startHerdr(ctx context.Context, h *herdrHost, why string) error {
	sock := h.c.Socket
	if herdrAlive(ctx, sock) {
		return nil
	}
	n.hstart.mu.Lock()
	defer n.hstart.mu.Unlock()
	if herdrAlive(ctx, sock) { // started by the call this one waited for
		return nil
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("herdr: %w", err)
	}
	unlock, err := lockHerdrDir(dir) // another node (a second personad on this account) starting it too
	if err != nil {
		return fmt.Errorf("herdr: lock %s: %w", dir, err)
	}
	defer unlock()
	if herdrAlive(ctx, sock) {
		return nil
	}
	bin := findHerdr(ctx, h.c.Bin)
	if bin == "" {
		n.logf("herdr: not running and not installed, not started (%s)", why)
		return errHerdrNotInstalled
	}
	n.logf("herdr: not running, starting its server (%s): %s%s", why, bin, sessionNote(h.c.Session))
	start := time.Now()
	exited, err := launchHerdr(ctx, bin, sock)
	if err != nil {
		n.logf("herdr: could not start: %v", err)
		return err
	}
	if err := waitHerdr(ctx, sock, exited); err != nil {
		n.logf("herdr: started but not ready: %v", err)
		return err
	}
	n.logf("herdr: server running after %s", time.Since(start).Round(10*time.Millisecond))
	return nil
}

// waitHerdr waits until herdr answers on sock, the server exits (exited gets why; nil when nothing reports exits),
// or herdrReadyWait passes.
func waitHerdr(ctx context.Context, sock string, exited <-chan error) error {
	deadline := time.NewTimer(herdrReadyWait)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if herdrAlive(ctx, sock) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("herdr did not answer on %s within %s", sock, herdrReadyWait)
		case err := <-exited:
			if herdrAlive(ctx, sock) { // a launcher that returns once the server is up (systemd-run)
				return nil
			}
			if err == nil {
				err = errors.New("exited")
			}
			return fmt.Errorf("herdr server stopped right after starting: %w", err)
		case <-tick.C:
		}
	}
}

// herdrBinary is the herdr command to run: an absolute path from the node's PATH, else from the login shell's (a
// node under launchd has a short PATH; herdr often lives in ~/.local/bin or Homebrew's). "" when neither has it.
func herdrBinary(ctx context.Context, bin string) string {
	if bin == "" {
		bin = "herdr"
	}
	if p, err := exec.LookPath(bin); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := loginShell(ctx, "command -v "+shellQuote(bin)).Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- { // the last line: rc files may print before it
		if l := strings.TrimSpace(terminalEscapes.ReplaceAllString(lines[i], "")); strings.HasPrefix(l, "/") {
			return l
		}
	}
	return ""
}

// herdrServerKeep are the variables the server's clean environment takes from the node's: who and where the user is,
// the locale, where herdr's socket, session and config are (so it listens where the node looks and reads the config
// file herdr.WorktreesDir reads), and where Persona's data directory is (PERSONA_HOME, and the older name the node
// still reads): every pane inherits it, and `personad launch` and the hooks find the node's socket in that directory.
// Nothing else — no ANTHROPIC_*, no proxies, no PATH: the login shell sets its own.
var herdrServerKeep = append([]string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE",
	"XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_DATA_HOME", "XDG_STATE_HOME", "HERDR_SOCKET_PATH", "HERDR_SESSION",
	"HERDR_CONFIG_PATH"},
	personaHomeNames()...)

// personaHomeNames are the names identity.DataDir reads the data directory from.
func personaHomeNames() []string {
	current, legacy := compat.EnvNames("PERSONA_HOME")
	return []string{current, legacy}
}

// herdrServerEnv is the environment the login shell starts with: the kept variables of environ, the shell, a
// terminal type and the system PATH (the login shell's profile builds on it).
func herdrServerEnv(environ []string, shell string) []string {
	keep := map[string]bool{}
	for _, k := range herdrServerKeep {
		keep[k] = true
	}
	var out []string
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if ok && keep[k] {
			out = append(out, kv)
		}
	}
	return append(out, "SHELL="+shell, "TERM=xterm-256color", "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
}

// withHerdrSession names the herdr session of the node's data directory in the server's environment (herdr.Session:
// a custom PERSONA_HOME gets a session of its own, so two nodes on one account do not share a server, #6482), unless
// the environment already says which session or socket the server is for. session "" (the default server): as is.
func withHerdrSession(env []string, session string) []string {
	if session == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); (k == "HERDR_SESSION" || k == "HERDR_SOCKET_PATH") && v != "" {
			return env
		} else if k == "HERDR_SESSION" || k == "HERDR_SOCKET_PATH" { // set but empty: as unset
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HERDR_SESSION="+session)
}

// herdrServerArgv runs the server in the user's login shell with only env: env -i, so nothing else is inherited.
func herdrServerArgv(shell, bin string, env []string) []string {
	argv := append([]string{"/usr/bin/env", "-i"}, env...)
	return append(argv, shell, "-l", "-c", "exec "+shellQuote(bin)+" server")
}

// herdrMissingDetail is engine_missing's detail: herdr's own error, and why starting it did not help.
func herdrMissingDetail(err, startErr error) string {
	d := "herdr is not running on this machine: " + err.Error()
	if startErr != nil {
		d += "; starting it: " + startErr.Error()
	}
	return d
}

// herdrStartWait is how long the node waits at start for the hub to say whether it may start herdr: a hub that has
// an opinion says it in node.configure right after connecting; an older one never does (then it is on).
var herdrStartWait = 10 * time.Second

// herdrAtStart starts herdr's server once the node serves, after a reboot where nothing else did.
func (n *Node) herdrAtStart(ctx context.Context) {
	select {
	case <-n.hstart.saidCh():
	case <-time.After(herdrStartWait):
	case <-ctx.Done():
		return
	}
	if err := n.ensureHerdr(ctx, "node started"); err != nil && !errors.Is(err, errHerdrNotInstalled) &&
		!errors.Is(err, errHerdrInstalling) {
		n.logf("herdr: not started at node start: %v", err)
	}
}

// sessionNote tells a log line which herdr session the node uses when it is not the default server.
func sessionNote(session string) string {
	if session == "" {
		return ""
	}
	return " (session " + session + ", the one of this data directory)"
}
