package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexNonDefaultHeadlessSourceIsNotAnAgent(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	id := n.registerCodex(home, remoteRaw, "")
	if id == remoteRaw {
		t.Fatal("test must use a non-default home")
	}
	n.state.mu.Lock()
	n.state.Sessions[remoteRaw] = sessionState{Cwd: filepath.Join(n.home, ".persona", "assistant")}
	n.state.mu.Unlock()
	if listed := n.codexSessions(map[string]bool{}); len(listed) != 0 {
		t.Fatalf("headless conversation duplicated into agents: %d", len(listed))
	}
}

// 2026-11-05: an evaluation run opened some sixty `codex exec` sessions, each in its own CODEX_HOME, each one verdict
// and done; they filled Agents as offline read-only rows over the agents at work. A rollout's session_meta says how
// its thread was opened: exec runs (and the subagents they spawn) are a program's, not the user's.
func TestCodexExecRunsAreNotListed(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "05")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id, meta string
		listed   bool
	}{
		{"0e000000-0000-4000-8000-0000000000e1", `{"originator":"codex_exec","source":"exec","cwd":"/tmp/eval-a"}`, false},
		{"0e000000-0000-4000-8000-0000000000e2", `{"originator":"codex_sdk_ts","source":"exec","cwd":"/tmp/eval-b"}`, false},
		{"0e000000-0000-4000-8000-0000000000e3", `{"originator":"codex_exec","source":{"subagent":{"thread_spawn":{"parent_thread_id":"0e000000-0000-4000-8000-0000000000e1"}}}}`, false},
		{"0e000000-0000-4000-8000-0000000000e4", `{"originator":"codex_exec","source":{"subagent":"review"}}`, false},
		{"0e000000-0000-4000-8000-0000000000e5", `{"originator":"Codex Desktop","source":"exec"}`, false},
		{"0e000000-0000-4000-8000-0000000000c1", `{"originator":"codex-tui","source":"cli","cwd":"/work/mine"}`, true},
		{"0e000000-0000-4000-8000-0000000000c2", `{"originator":"Codex Desktop","source":"vscode","cwd":"/work/app"}`, true},
	}
	for _, c := range cases {
		p := filepath.Join(dir, "rollout-2026-11-05T01-00-00-"+c.id+".jsonl")
		if err := os.WriteFile(p, []byte(`{"type":"session_meta","payload":`+c.meta+"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		n.registerCodex(home, c.id, p)
	}
	listed := map[string]bool{}
	for _, s := range n.codexSessions(map[string]bool{}) {
		listed[s.SourceThreadID] = true
	}
	for _, c := range cases {
		if listed[c.id] != c.listed {
			t.Errorf("%s listed=%v, want %v", c.meta, listed[c.id], c.listed)
		}
	}
}

// An exec run a shared app-server holds is drivable from the phone: it stays (#2131 hides only the history rows).
func TestCodexExecRunOnAnAppServerIsListed(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	connectRemoteFixture(t, n, home, "one", remoteRaw)
	p := filepath.Join(home, "rollout-exec.jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"session_meta","payload":{"originator":"codex_exec","source":"exec"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := n.registerCodex(home, remoteRaw, p)
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SessionID == id {
			if s.SourceKind != "codex_app_server" {
				t.Fatalf("source = %s", s.SourceKind)
			}
			return
		}
	}
	t.Fatal("an app-server's exec thread was hidden")
}

// Historical subagents remain available with explicit hierarchy metadata. Consumers
// exclude them from top-level counters rather than discarding their transcripts.
func TestCodexSubagentRolloutsRetainTheirRelationships(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "06")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const parent = "0e000000-0000-4000-8000-0000000000a0"
	cases := []struct {
		id, meta string
		listed   bool
	}{
		{parent, `{"id":"` + parent + `","originator":"codex-mew","source":"cli","cwd":"/work/home"}`, true},
		// As Codex 0.159 writes it: thread_source, parent_thread_id and source.subagent.thread_spawn all say so.
		{"0e000000-0000-4000-8000-0000000000a1", `{"id":"0e000000-0000-4000-8000-0000000000a1","session_id":"` + parent + `","forked_from_id":"` + parent + `","parent_thread_id":"` + parent + `","originator":"codex-mew","cwd":"/work/home","source":{"subagent":{"thread_spawn":{"parent_thread_id":"` + parent + `","depth":1,"agent_path":"/root/security","agent_nickname":"Copernicus"}}},"thread_source":"subagent"}`, true},
		// Either mark alone is enough.
		{"0e000000-0000-4000-8000-0000000000a2", `{"id":"0e000000-0000-4000-8000-0000000000a2","originator":"codex-tui","cwd":"/work/home","thread_source":"subagent"}`, true},
		{"0e000000-0000-4000-8000-0000000000a3", `{"id":"0e000000-0000-4000-8000-0000000000a3","originator":"codex-tui","cwd":"/work/home","source":{"subagent":"review"}}`, true},
		// A fork (forked_from_id only) is a conversation of its own.
		{"0e000000-0000-4000-8000-0000000000a4", `{"id":"0e000000-0000-4000-8000-0000000000a4","forked_from_id":"` + parent + `","originator":"codex-tui","source":"cli","cwd":"/work/home"}`, true},
	}
	for _, c := range cases {
		p := filepath.Join(dir, "rollout-2026-11-06T02-00-00-"+c.id+".jsonl")
		if err := os.WriteFile(p, []byte(`{"type":"session_meta","payload":`+c.meta+"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		n.registerCodex(home, c.id, p)
	}
	listed := map[string]bool{}
	for _, s := range n.codexSessions(map[string]bool{}) {
		listed[s.SourceThreadID] = true
	}
	for _, c := range cases {
		if listed[c.id] != c.listed {
			t.Errorf("%s listed=%v, want %v", c.id, listed[c.id], c.listed)
		}
	}
}

// A subagent a shared app-server holds stays listed (read-only under its parent, its approvals answerable).
func TestCodexSubagentOnAnAppServerIsListed(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	connectRemoteFixture(t, n, home, "one", remoteRaw)
	p := filepath.Join(home, "rollout-sub.jsonl")
	if err := os.WriteFile(p, []byte(`{"type":"session_meta","payload":{"originator":"codex-tui","thread_source":"subagent","source":{"subagent":{"thread_spawn":{"parent_thread_id":"x"}}}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := n.registerCodex(home, remoteRaw, p)
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SessionID == id {
			return
		}
	}
	t.Fatal("an app-server's subagent thread was hidden")
}

// #2126: a Codex-run assistant conversation stays listed, and sendable, once its idle process is let go.
func TestCodexAssistantAtRestIsListed(t *testing.T) {
	n := remoteTestNode(t)
	dir := filepath.Join(n.codexHome(), "sessions", "2026", "11", "09")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd := identity.AssistantDir(n.home)
	long := time.Now().Add(-2 * offlineWindow)
	ids := map[string]struct {
		cwd   string
		at    time.Time
		kept  bool
		shown bool
	}{
		"0e000000-0000-4000-8000-0000000000d1": {cwd, time.Now(), false, true},
		"0e000000-0000-4000-8000-0000000000d2": {cwd, long, true, true},
		"0e000000-0000-4000-8000-0000000000d3": {cwd, long, false, false},
		"0e000000-0000-4000-8000-0000000000d4": {"/work/mine", time.Now(), false, false},
	}
	var kept []string
	n.state.mu.Lock()
	for id, c := range ids {
		p := filepath.Join(dir, "rollout-2026-11-09T01-00-00-"+id+".jsonl")
		if err := os.WriteFile(p, []byte(`{"type":"session_meta","payload":{"originator":"codex_sdk_ts","cwd":"`+c.cwd+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, c.at, c.at); err != nil {
			t.Fatal(err)
		}
		n.state.Sessions[id] = sessionState{Cwd: c.cwd, Launcher: "codex", StartedAt: time.Now()}
		if c.kept {
			kept = append(kept, id)
		}
	}
	n.state.mu.Unlock()
	n.setKept(kept)

	count := 0
	for _, s := range n.sessionList(context.Background(), false) {
		if s.SessionID == "0e000000-0000-4000-8000-0000000000d1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("resting conversation is on the list %d times", count)
	}

	rows := n.codexAssistantsAtRest(map[string]bool{})
	got := map[string]proto.Session{}
	for _, s := range rows {
		got[s.SessionID] = s
	}
	if len(got) != len(rows) {
		t.Fatalf("a conversation listed twice: %+v", rows)
	}
	for id, c := range ids {
		s, shown := got[id]
		if shown != c.shown {
			t.Errorf("%s listed=%v, want %v", id, shown, c.shown)
		}
		if shown && (s.SourceKind != "" || s.Agent != "codex" || s.Status != proto.StatusOffline) {
			t.Errorf("%s is not a sendable resting row: %+v", id, s)
		}
	}
	if rows = n.codexAssistantsAtRest(map[string]bool{"0e000000-0000-4000-8000-0000000000d1": true}); len(rows) != 1 {
		t.Fatalf("a row already listed came again: %+v", rows)
	}
}
