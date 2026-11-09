//go:build codexlive

package node

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
)

// Codex started from the phone (docs/integrations/codex.md "Starting from the phone"), the way session.start does it, against real Codex in the
// isolated herdr: the launcher runs through codex-start with the first message in a file, the start answers with the
// thread that message began, the pane lists as that one session, on the route its launcher implies. Env as
// TestLiveCodexDaemon; PERSONA_CODEX_START=daemon (a 0.163 attached to the test's own app-server with --remote: the
// app-server route), plain (a Codex without the daemon: 0.153) or config (a 0.163 launcher with -c, in its own process:
// the terminal route, and the note says why). The launchers all carry -c (no notify hook, no update check), so a plain
// 0.163 — which would start or join the daemon of the Codex in use — is never run here.
func TestLiveCodexStart(t *testing.T) {
	bin, work, variant := os.Getenv("PERSONA_CODEX_BIN"), os.Getenv("PERSONA_CODEX_WORK"), os.Getenv("PERSONA_CODEX_START")
	if bin == "" || work == "" || variant == "" {
		t.Skip("PERSONA_CODEX_BIN / PERSONA_CODEX_WORK / PERSONA_CODEX_START not set")
	}
	home, _ := os.UserHomeDir()
	if variant == "plain" {
		if _, err := os.Lstat(filepath.Join(home, ".codex", "app-server-control", "app-server-control.sock")); err == nil {
			t.Skip("a daemon runs in ~/.codex: a plain codex could join it")
		}
	}
	c := herdr.New()
	var n *Node
	words := []string{bin, "-c", "notify=[]", "-c", "check_for_update_on_startup=false"} // as startLiveCodex
	switch variant {
	case "daemon":
		d := startLiveDaemon(t, bin, filepath.Join(work, "start-daemon"+os.Getenv("PERSONA_CODEX_COLS")))
		n, _ = daemonNode(t, c, d)
		words = append(words, "--remote", "unix://"+d.sock)
	case "plain", "config":
		n = liveCodexNode(c)
	default:
		t.Fatalf("PERSONA_CODEX_START=%s", variant)
	}
	words = append(words, strings.Fields(os.Getenv("PERSONA_CODEX_ARGS"))...)
	// What New gives a node and the start and the listing write to.
	if n.logger == nil {
		n.logger = log.New(os.Stderr, "node: ", log.Lmicroseconds)
	}
	if n.metas == nil {
		n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	}
	n.panePIDs, n.modes, n.driving = map[string]panePID{}, map[string]sessionModes{}, map[string]bool{}
	n.dir = t.TempDir()
	n.bin = os.Getenv("PERSONA_PERSONAD")
	if n.bin == "" {
		t.Skip("PERSONA_PERSONAD (a personad built for this machine, for codex-start) not set")
	}
	launcher := "live-" + variant
	withCodexLaunchers(t, map[string][]string{launcher: words, "missing": {"codex-not-installed-for-persona"}})
	out, _ := liveRun(bin, "--version")
	version := strings.TrimSpace(strings.TrimPrefix(out, "codex-cli"))
	rounds, _ := strconv.Atoi(os.Getenv("PERSONA_CODEX_ROUNDS"))
	rounds = max(rounds, 1)
	failed := 0
	for r := 1; r <= rounds; r++ {
		start := time.Now()
		err := liveStartRound(t, n, c, work, launcher, variant, r)
		if why := lastServiceError; err != nil && why != "" {
			t.Logf("LIVE %s start-%s round %d: SERVICE-RETRY: Codex's model service failed the turn (%s)", version, variant, r, why)
			start, err = time.Now(), liveStartRound(t, n, c, work, launcher, variant, r+50)
		}
		if err != nil {
			failed++
			t.Errorf("LIVE %s start-%s round %d: FAIL after %s: %v", version, variant, r, time.Since(start).Round(time.Second), err)
		} else {
			t.Logf("LIVE %s start-%s round %d: ok in %s", version, variant, r, time.Since(start).Round(time.Second))
		}
	}
	// A launcher that cannot run: the start says so at once.
	start := time.Now()
	dir := mkdir(t, filepath.Join(work, "start-missing"+os.Getenv("PERSONA_CODEX_COLS")))
	_, err := n.start(bg, dir, "missing", "hi", "", "", "", "", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") || time.Since(start) > 20*time.Second {
		failed++
		t.Errorf("LIVE %s start-%s missing launcher: FAIL: %v after %s", version, variant, err, time.Since(start))
	} else {
		t.Logf("LIVE %s start-%s missing launcher: ok in %s", version, variant, time.Since(start).Round(time.Second))
	}
	closeWorkspaceOf(c, dir)
	if failed > 0 {
		t.Fatalf("LIVE %s start-%s: %d failed", version, variant, failed)
	}
}

// lastServiceError is the model service error the last round's pane showed, "" for none.
var lastServiceError string

// startPrompt is a first message a shell would take apart, and long; the model is told to answer one word.
func startPrompt(n int) string {
	return fmt.Sprintf("Reply with the single word: started%d. Ignore everything after this sentence.\n"+
		"\"double\" 'single' $HOME ${PATH} $(whoami) `date` \\n back\\slash; & | > < * ? ~ #\n第二行：中文、标点「」——\t制表符\n%s", n, strings.Repeat("测试长文本", 300))
}

// liveStartRound starts one Codex the phone's way in a fresh directory and checks what came of it. The directory is a
// git repository, like a fresh clone: Codex asks to trust only those (#2351), and the start answers that.
func liveStartRound(t *testing.T, n *Node, c *herdr.Client, work, launcher, variant string, r int) error {
	lastServiceError = ""
	dir := mkdir(t, filepath.Join(work, fmt.Sprintf("start-%s%s-%d", variant, os.Getenv("PERSONA_CODEX_COLS"), r)))
	dir, _ = filepath.EvalSymlinks(dir)
	if _, err := liveRun("git", "-C", dir, "init", "-q"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	defer closeWorkspaceOf(c, dir)
	prompt := startPrompt(r)
	began := time.Now()
	s, err := n.start(bg, dir, launcher, prompt, "", "", "", "", "", "", "")
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	took := time.Since(began)
	t.Logf("LIVE start-%s round %d: answered %s in %s", variant, r, s.SessionID, took.Round(100*time.Millisecond))
	if took > codexStartWait {
		return fmt.Errorf("answered after %s", took)
	}
	// Codex asked whether to trust the folder and the start said yes: Codex saved the answer.
	if cfg, _ := os.ReadFile(filepath.Join(n.codexHome(), "config.toml")); !strings.Contains(string(cfg), "[projects.\""+dir+"\"]\ntrust_level = \"trusted\"") {
		return fmt.Errorf("%s is not trusted in config.toml", dir)
	}
	lc := &liveCodex{t: t, n: n, c: c, pane: s.PaneID, dir: dir, id: s.SessionID}
	// The thread is the one the message began, byte for byte.
	path := n.codexPath(s.SessionID)
	if _, first := codexThreadStart(path); first != prompt {
		return fmt.Errorf("the first message arrived as %q", first[:min(len(first), 300)])
	}
	// One session for the pane, this one, and none left starting.
	list := n.sessionList(bg, true)
	count := 0
	for _, x := range list {
		if x.PaneID == s.PaneID || x.SessionID == s.SessionID {
			count++
			if x.SessionID != s.SessionID {
				return fmt.Errorf("the pane lists as %s, the start answered %s", x.SessionID, s.SessionID)
			}
		}
		if x.Starting {
			return fmt.Errorf("a session still starting: %+v", x)
		}
	}
	if count != 1 {
		return fmt.Errorf("%d sessions for the new pane", count)
	}
	// Its route: the app-server for a Codex on it, else the terminal and why.
	wantRoute := codexRouteTerminal
	if variant == "daemon" {
		wantRoute = codexRouteAppServer
	}
	if err := lc.until(30*time.Second, "route "+wantRoute, func() bool { route, _ := n.codexRoute(s.SessionID); return route == wantRoute }); err != nil {
		route, note := n.codexRoute(s.SessionID)
		return fmt.Errorf("%v: %s (%s)", err, route, note)
	}
	if _, note := n.codexRoute(s.SessionID); variant == "config" && (!strings.Contains(note, launcher) || !strings.Contains(note, "-c")) {
		return fmt.Errorf("the note does not say why: %q", note)
	}
	// It answers its first message, and the next one from the phone.
	if err := lc.rolloutAfter(0, 90*time.Second, fmt.Sprintf("started%d", r), `"final_answer"`); err != nil {
		lastServiceError = serviceError(lc.plain())
		return err
	}
	if err := lc.idle(90 * time.Second); err != nil {
		return err
	}
	off := lc.size()
	word := fmt.Sprintf("again%d", r)
	if err := n.send(bg, s.SessionID, "Reply with the single word: "+word, nil, sendOpts{}); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if err := lc.rolloutAfter(off, 60*time.Second, word, `"final_answer"`); err != nil {
		lastServiceError = serviceError(lc.plain())
		return err
	}
	return nil
}

// closeWorkspaceOf closes the herdr workspace a start made for dir (named after it), quitting its Codex first.
func closeWorkspaceOf(c *herdr.Client, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, err := c.Snapshot(ctx)
	if err != nil {
		return
	}
	for _, a := range snap.Panes {
		if a.Cwd == dir || strings.HasPrefix(a.Cwd, dir+"/") {
			_ = c.PaneSendKeys(ctx, a.PaneID, "ctrl+c")
			time.Sleep(300 * time.Millisecond)
			_ = c.PaneSendKeys(ctx, a.PaneID, "ctrl+c")
		}
	}
	time.Sleep(time.Second)
	for _, w := range snap.Workspaces {
		if w.Label == filepath.Base(dir) {
			lc := &liveCodex{c: c}
			_ = lc.raw("workspace.close", map[string]any{"workspace_id": w.ID})
		}
	}
}
