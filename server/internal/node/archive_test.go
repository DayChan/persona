package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// writeArchiveRollout writes thread's rollout in today's directory of home and returns its path.
func writeArchiveRollout(t *testing.T, home, thread string) string {
	t.Helper()
	now := time.Now()
	dir := filepath.Join(home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+thread+".jsonl")
	line := `{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + thread + `","cwd":"` + filepath.ToSlash(home) + `"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// moveCodexRollout plays Codex's archive (to archived_sessions) or unarchive (back to its day directory).
func moveCodexRollout(t *testing.T, home, thread string, archive bool) {
	t.Helper()
	from := rolloutPath(home, thread)
	if from == "" || codexArchived(home, from) == archive {
		return
	}
	var to string
	if archive {
		to = filepath.Join(home, "archived_sessions", filepath.Base(from))
	} else {
		now := time.Now()
		to = filepath.Join(home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), filepath.Base(from))
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(from, to); err != nil {
		t.Error(err)
	}
}

func archiveCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func archiveIn(t *testing.T, home, thread string) bool {
	t.Helper()
	p := rolloutPath(home, thread)
	if p == "" {
		t.Fatal("rollout gone")
	}
	return codexArchived(home, p)
}

// A thread of an app-server the node is connected to (the Codex App, the shared daemon): archived and restored there,
// with thread/archive and thread/unarchive; asking again for what already is answers done without a call.
func TestNativeArchiveCodexThroughItsAppServer(t *testing.T) {
	n := remoteTestNode(t)
	home := canonicalCodexHome(t.TempDir(), n.home)
	f, _ := connectRemoteFixture(t, n, home, "app", remoteRaw)
	path := writeArchiveRollout(t, home, remoteRaw)
	id := n.registerCodex(home, remoteRaw, path)
	f.mu.Lock()
	f.archive = func(method, thread string) { moveCodexRollout(t, home, thread, method == "thread/archive") }
	f.mu.Unlock()
	waitFor(t, "owner", func() bool { _, _, err := n.codexOwner(id); return err == nil })
	ctx := context.Background()

	res, err := n.nativeArchive(ctx, id, true)
	if err != nil || res.Native != nativeArchiveDone || res.Via != "app_server" || res.Already {
		t.Fatalf("archive: %+v %v", res, err)
	}
	if !archiveIn(t, home, remoteRaw) || f.count("thread/archive") != 1 {
		t.Fatalf("not archived through the app-server: %d calls", f.count("thread/archive"))
	}
	if res, err := n.nativeArchive(ctx, id, true); err != nil || !res.Already || f.count("thread/archive") != 1 {
		t.Fatalf("again: %+v %v (calls %d)", res, err, f.count("thread/archive"))
	}
	res, err = n.nativeArchive(ctx, id, false)
	if err != nil || res.Native != nativeArchiveDone || archiveIn(t, home, remoteRaw) || f.count("thread/unarchive") != 1 {
		t.Fatalf("unarchive: %+v %v", res, err)
	}
	if res, err := n.nativeArchive(ctx, id, false); err != nil || !res.Already || f.count("thread/unarchive") != 1 {
		t.Fatalf("unarchive again: %+v %v", res, err)
	}
}

// What Codex did is read from its files: an error after the move (an answer lost) is done; an error with nothing
// moved is refused, to be tried again; a turn running is refused before Codex is asked.
func TestNativeArchiveCodexAnswersAreCheckedOnDisk(t *testing.T) {
	n := remoteTestNode(t)
	home := canonicalCodexHome(t.TempDir(), n.home)
	f, c := connectRemoteFixture(t, n, home, "app", remoteRaw)
	id := n.registerCodex(home, remoteRaw, writeArchiveRollout(t, home, remoteRaw))
	waitFor(t, "owner", func() bool { _, _, err := n.codexOwner(id); return err == nil })
	ctx := context.Background()

	f.mu.Lock()
	f.reject = "thread/archive"
	f.mu.Unlock()
	if _, err := n.nativeArchive(ctx, id, true); archiveCode(err) != nativeArchiveFailed || archiveIn(t, home, remoteRaw) {
		t.Fatalf("a refused archive: %v", err)
	}
	f.mu.Lock()
	f.archive = func(method, thread string) { moveCodexRollout(t, home, thread, method == "thread/archive") }
	f.mu.Unlock()
	if res, err := n.nativeArchive(ctx, id, true); err != nil || res.Native != nativeArchiveDone || !archiveIn(t, home, remoteRaw) {
		t.Fatalf("moved, answer lost: %+v %v", res, err)
	}
	if _, err := n.nativeArchive(ctx, id, false); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	th := c.threads[remoteRaw]
	th.turn = "turn-1"
	c.threads[remoteRaw] = th
	c.mu.Unlock()
	calls := f.count("thread/archive")
	if _, err := n.nativeArchive(ctx, id, true); archiveCode(err) != nativeArchiveBusy || f.count("thread/archive") != calls {
		t.Fatalf("a running turn: %v", err)
	}
	c.mu.Lock()
	th.turn = ""
	c.threads[remoteRaw] = th
	c.mu.Unlock()
	n.agents[id] = host.Agent{Agent: "codex", PaneID: "w1:p3", AgentStatus: proto.StatusIdle} // its TUI on the daemon
	if _, err := n.nativeArchive(ctx, id, true); archiveCode(err) != nativeArchiveLive || f.count("thread/archive") != calls {
		t.Fatalf("open in a pane: %v", err)
	}
}

// codexArchiveRecorder stands in for the codex command: what it was run with, and Codex's move.
type codexArchiveRecorder struct {
	mu   sync.Mutex
	runs [][]string
	out  string
	fail bool
	move bool
}

func stubCodexArchiveCommand(t *testing.T) *codexArchiveRecorder {
	t.Helper()
	r := &codexArchiveRecorder{move: true}
	old := codexArchiveCommand
	codexArchiveCommand = func(_ context.Context, _ *Node, launcher, home, sub, thread string) ([]byte, error) {
		r.mu.Lock()
		r.runs = append(r.runs, []string{launcher, home, sub, thread})
		out, fail, move := r.out, r.fail, r.move
		r.mu.Unlock()
		if move {
			moveCodexRollout(t, home, thread, sub == "archive")
		}
		if fail {
			return []byte(out), errors.New("exit status 2")
		}
		return []byte(out), nil
	}
	t.Cleanup(func() { codexArchiveCommand = old })
	return r
}

// A herdr Codex with no app-server connected: `codex archive|unarchive <thread>` in the thread's home, never while a
// Codex still has it open (a live pane), and never while it works.
func TestNativeArchiveCodexCommandLine(t *testing.T) {
	n := remoteTestNode(t)
	writeArchiveRollout(t, n.codexHome(), remoteOther)
	home := canonicalCodexHome(n.codexHome(), n.home) // resolved once it exists, as the node resolves it
	r := stubCodexArchiveCommand(t)
	ctx := context.Background()

	n.codices.checked = func() ([]codexProcess, bool) { return nil, false } // the processes could not all be read
	if _, err := n.nativeArchive(ctx, remoteOther, true); archiveCode(err) != nativeArchiveLive || len(r.runs) != 0 {
		t.Fatalf("unknown holders: %v (runs %v)", err, r.runs)
	}
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	n.agents[remoteOther] = host.Agent{Agent: "codex", PaneID: "w1:p1", AgentStatus: proto.StatusWorking}
	if _, err := n.nativeArchive(ctx, remoteOther, true); archiveCode(err) != nativeArchiveBusy {
		t.Fatalf("working: %v", err)
	}
	n.agents[remoteOther] = host.Agent{Agent: "codex", PaneID: "w1:p1", AgentStatus: proto.StatusIdle}
	if _, err := n.nativeArchive(ctx, remoteOther, true); archiveCode(err) != nativeArchiveLive || len(r.runs) != 0 {
		t.Fatalf("open in a pane: %v (runs %v)", err, r.runs)
	}
	delete(n.agents, remoteOther)

	res, err := n.nativeArchive(ctx, remoteOther, true)
	if err != nil || res.Native != nativeArchiveDone || res.Via != "cli" || !archiveIn(t, home, remoteOther) {
		t.Fatalf("archive: %+v %v", res, err)
	}
	if want := []string{"codex", home, "archive", remoteOther}; !reflect.DeepEqual(r.runs[0], want) {
		t.Fatalf("ran %q, want %q", r.runs[0], want)
	}
	res, err = n.nativeArchive(ctx, remoteOther, false)
	if err != nil || res.Native != nativeArchiveDone || archiveIn(t, home, remoteOther) || r.runs[1][2] != "unarchive" {
		t.Fatalf("unarchive: %+v %v %v", res, err, r.runs)
	}

	r.mu.Lock()
	r.move, r.fail, r.out = false, true, "Error: failed to archive session"
	r.mu.Unlock()
	if _, err := n.nativeArchive(ctx, remoteOther, true); archiveCode(err) != nativeArchiveFailed || !strings.Contains(err.Error(), "failed to archive session") {
		t.Fatalf("refused: %v", err)
	}
	r.mu.Lock()
	r.out = "error: unrecognized subcommand 'archive'"
	r.mu.Unlock()
	if res, err := n.nativeArchive(ctx, remoteOther, true); err != nil || res.Native != nativeArchiveUnsupported {
		t.Fatalf("an older codex: %+v %v", res, err)
	}
}

// Through the node's dispatch: a Claude session (no archive of its own) answers unsupported, nothing is run.
func TestNativeArchiveClaudeIsPersonaOnly(t *testing.T) {
	n := remoteTestNode(t)
	r := stubCodexArchiveCommand(t)
	id := "99999999-8888-7777-6666-555555555555"
	n.agents[id] = host.Agent{Agent: "claude", PaneID: "w1:p2"}
	for _, method := range []string{"session.archive", "session.unarchive"} {
		raw, err := n.handle(context.Background(), method, json.RawMessage(`{"session_id":"`+id+`"}`))
		res, _ := raw.(nativeArchiveResult)
		if err != nil || res.Native != nativeArchiveUnsupported {
			t.Fatalf("%s: %+v %v", method, raw, err)
		}
	}
	if len(r.runs) != 0 {
		t.Fatalf("ran %v", r.runs)
	}
}

// A runtime archives its own sessions only when it declares caps.archive; one that does not is not asked.
func TestRuntimeNativeArchive(t *testing.T) {
	n, _ := runtimeNode(t)
	with := dshHello()
	with["caps"].(map[string]any)["archive"] = true
	h := dialRuntime(t, n, with)
	_ = h.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "session-a", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["session-a"]; return ok })
	for _, method := range []string{"session.archive", "session.unarchive"} {
		raw, err := nodeCall(t, n, method, map[string]any{"session_id": "session-a"})
		var res nativeArchiveResult
		_ = json.Unmarshal(raw, &res)
		if err != nil || res.Native != nativeArchiveDone || res.Via != "runtime" || len(h.got(method)) != 1 {
			t.Fatalf("%s: %s %v", method, raw, err)
		}
	}

	without := map[string]any{"runtime": "other", "caps": map[string]any{"send": true}}
	o := dialRuntime(t, n, without)
	_ = o.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "session-b", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["session-b"]; return ok })
	raw, err := nodeCall(t, n, "session.archive", map[string]any{"session_id": "session-b"})
	var res nativeArchiveResult
	_ = json.Unmarshal(raw, &res)
	if err != nil || res.Native != nativeArchiveUnsupported || len(o.got("session.archive")) != 0 {
		t.Fatalf("without the cap: %s %v", raw, err)
	}
}

// The command runs `<launcher words> archive <thread>`: an alias's words go before the subcommand.
func TestCodexSubcommandShellBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	for _, tt := range []struct {
		name, rc, launcher string
		flags              []string
	}{
		{name: "plain", launcher: "codex"},
		{name: "launcher alias", rc: "alias cx='command codex -c model=\"x y\"'\n", launcher: "cx", flags: []string{"-c", "model=x y"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(tt.rc), 0o600); err != nil {
				t.Fatal(err)
			}
			// The arguments go to a file: an interactive bash may print a notice of its own on stdout (Ubuntu's
			// system-wide bashrc does for a sudo user, #1109).
			args := filepath.Join(home, "args")
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$HOME/args\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "-ic", codexSubcommandShell(tt.launcher, "archive"), "persona", remoteRaw)
			cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			out, err := os.ReadFile(args)
			if err != nil {
				t.Fatal(err)
			}
			want := append(append([]string{}, tt.flags...), "archive", remoteRaw)
			if got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments = %q, want %q", got, want)
			}
		})
	}
}
