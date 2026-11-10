package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// One sessionList rebuild looks up the data directory, the assistant's project name and each Codex rollout's stat once
// (codexListPass), where every row's decoration did it again each second. With the data directory under the current
// name, under the former name, and under the former name as an alias of the current one (identity.DataDir), the list
// is byte for byte the one looking everything up per row gives: live and resting conversations, the assistant's (in
// its directory and in the other name's), the health check's, and a Codex thread.
func TestSessionListPassMatchesPerRowLookups(t *testing.T) {
	const (
		resting   = "0e000000-0000-4000-8000-000000000001"
		mine      = "0e000000-0000-4000-8000-000000000002"
		other     = "0e000000-0000-4000-8000-000000000003"
		health    = "0e000000-0000-4000-8000-000000000004"
		live      = "0e000000-0000-4000-8000-000000000005"
		thread    = "0e000000-0000-4000-8000-000000000006"
		working   = "0e000000-0000-4000-8000-000000000007"
		childless = "0e000000-0000-4000-8000-000000000008"
	)
	for _, layout := range []string{"current", "former", "alias"} {
		t.Run(layout, func(t *testing.T) {
			t.Setenv("PERSONA_HOME", "")
			n, _, home := herdrNode(t)
			n.codices = newCodexRegistry()
			n.codices.scan = func() []codexProcess { return nil }
			n.configureCodex(proto.CodexConfig{})
			current, former := filepath.Join(home, ".persona"), filepath.Join(home, compat.StateDir)
			switch layout {
			case "former":
				if err := os.MkdirAll(former, 0o700); err != nil {
					t.Fatal(err)
				}
			case "alias":
				if err := os.Symlink(current, former); err != nil {
					t.Fatal(err)
				}
			}
			wantData := map[string]string{"current": current, "former": former, "alias": current}[layout]
			if got := identity.DataDir(home); got != wantData {
				t.Fatalf("data directory %q, want %q", got, wantData)
			}
			work := filepath.Join(home, "work", "app")
			assistant, named := identity.AssistantDir(home), filepath.Join(current, "assistant")
			for _, d := range []string{work, assistant, named, identity.HealthDir(home)} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeDrift(t, home, resting, work, work)
			writeDrift(t, home, mine, assistant, assistant)
			writeDrift(t, home, other, named, named)
			writeDrift(t, home, health, identity.HealthDir(home), identity.HealthDir(home))
			writeDrift(t, home, live, work, work)
			writeRollout(t, home, thread, work, time.Now(), time.Now(), event("task_started"), event("task_complete"))
			n.mu.Lock()
			n.agents[live] = host.Agent{Agent: "claude", AgentStatus: proto.StatusIdle, Cwd: work}
			n.agents[working] = host.Agent{Agent: "claude", AgentStatus: proto.StatusWorking, Cwd: work}
			n.agents[childless] = host.Agent{Agent: "claude", AgentStatus: proto.StatusIdle, Cwd: assistant}
			n.mu.Unlock()

			list := func(pass *codexListPass) string {
				t.Helper()
				b, err := json.Marshal(n.sessionListIn(context.Background(), false, pass))
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			list(nil) // the first list fills the node's caches (quick metas, transcript paths); both below find them filled
			want := list(nil)
			pass := newCodexListPass()
			if got := list(pass); got != want {
				t.Fatalf("the list differs with the pass\n got %s\nwant %s", got, want)
			}
			var rows []proto.Session
			if err := json.Unmarshal([]byte(want), &rows); err != nil {
				t.Fatal(err)
			}
			listed := map[string]bool{}
			for _, s := range rows {
				listed[s.SessionID] = true
				if s.SourceThreadID != "" {
					listed[s.SourceThreadID] = true
				}
			}
			for _, id := range []string{resting, live, working, thread} {
				if !listed[id] {
					t.Errorf("%s is not listed: %s", id, want)
				}
			}
			if !pass.ownSet || pass.own != n.assistantProject() {
				t.Errorf("the pass kept the assistant's project %q (set %v), want %q", pass.own, pass.ownSet, n.assistantProject())
			}
		})
	}
}

// A Claude row's decoration finds the assistant's project through the pass: resolving the data directory (an Lstat of
// the former name's directory and up to two stats) and the regexp that names the project ran for every row each second.
func TestDecorateFindsTheAssistantProjectThroughThePass(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, _, home := herdrNode(t)
	const id = "0e000000-0000-4000-8000-000000000011"
	work := filepath.Join(home, "work", "app")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	writeDrift(t, home, id, work, work)
	pass := newCodexListPass()
	for range 3 {
		s := proto.Session{SessionID: id, Agent: "claude", Status: proto.StatusOffline, Cwd: work}
		n.decorateIn(&s, pass)
	}
	if !pass.ownSet || pass.own != n.assistantProject() {
		t.Fatalf("the pass kept the assistant's project %q (set %v), want %q", pass.own, pass.ownSet, n.assistantProject())
	}
}

// Within one pass a Codex rollout is stat'ed once: the transcript lookup a row's decoration does (isAssistantSessionIn)
// takes the stat the list took. The rollout removed in between is still the pass's answer; without a pass it is gone.
func TestTranscriptPathTakesTheRolloutStatFromThePass(t *testing.T) {
	n := remoteTestNode(t)
	const thread = "0e000000-0000-4000-8000-000000000021"
	writeRollout(t, n.home, thread, "/work/app", time.Now(), time.Now(), event("task_started"))
	path := rolloutPath(n.codexHome(), thread)
	id := n.registerCodex(n.codexHome(), thread, path)
	if path == "" || id == "" {
		t.Fatalf("rollout %q registered as %q", path, id)
	}
	pass := newCodexListPass()
	if got := n.transcriptPathIn(id, pass); got != path {
		t.Fatalf("transcript %q, want the rollout %q", got, path)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := n.transcriptPathIn(id, pass); got != path {
		t.Fatalf("within the pass the transcript is %q, want the rollout it stat'ed %q", got, path)
	}
	if got := n.transcriptPath(id); got != "" {
		t.Fatalf("without a pass the removed rollout is still found: %q", got)
	}
}
