package node

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestUpdateCodexInventoryRetainsEphemeralThreads(t *testing.T) {
	n := remoteTestNode(t)
	f, c := connectRemoteFixture(t, n, t.TempDir(), "fixture", remoteRaw)
	f.mu.Lock()
	f.threads, f.ephemeral = []string{remoteRaw, remoteOther}, remoteOther
	f.mu.Unlock()
	out, err := c.observeUpdateThreads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Threads) != 2 || len(out.Revision) != 64 || out.Generation != c.generation {
		t.Fatalf("incomplete source observation: %+v", out)
	}
	for _, row := range out.Threads {
		if row.Ephemeral && row.Status != proto.StatusWorking {
			t.Fatalf("ephemeral activity was hidden as idle: %+v", row)
		}
	}
	if f.count("thread/resume") != 0 {
		t.Fatal("read-only inventory resumed a thread")
	}
}

func TestUpdateCodexInventoryRefusesUnreadThreadInsteadOfCachedIdle(t *testing.T) {
	n := remoteTestNode(t)
	f, c := connectRemoteFixture(t, n, t.TempDir(), "fixture", remoteRaw)
	f.mu.Lock()
	f.reject = "thread/read"
	f.mu.Unlock()
	if _, err := c.observeUpdateThreads(context.Background()); err == nil {
		t.Fatal("failed thread read was replaced by cached idle")
	}
	if err := c.refresh(); err != nil {
		t.Fatalf("ordinary display lost its existing cache behavior: %v", err)
	}
}

func TestUpdateCodexInventoryRefusesDuplicateAndCanceledRead(t *testing.T) {
	n := remoteTestNode(t)
	f, c := connectRemoteFixture(t, n, t.TempDir(), "fixture", remoteRaw)
	f.mu.Lock()
	f.threads = []string{remoteRaw, remoteRaw}
	f.mu.Unlock()
	if _, err := c.observeUpdateThreads(context.Background()); err == nil {
		t.Fatal("duplicate loaded IDs were certified complete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.observeUpdateThreads(ctx); err == nil {
		t.Fatal("canceled read produced evidence")
	}
}

func TestUpdateCodexInventoryRequiresFreshExplicitBackground(t *testing.T) {
	n := remoteTestNode(t)
	f, c := connectRemoteFixture(t, n, t.TempDir(), "fixture", remoteRaw)
	for _, response := range []map[string]any{{}, {"data": nil}, {"data": "invalid"}, {"data": []any{nil}}} {
		f.mu.Lock()
		f.backgroundReply = response
		f.mu.Unlock()
		if _, err := c.observeUpdateThreads(context.Background()); err == nil {
			t.Fatalf("unknown background response became empty: %+v", response)
		}
	}
	f.mu.Lock()
	f.backgroundReply = map[string]any{"data": []any{map[string]any{"command": "synthetic work"}}}
	f.mu.Unlock()
	out, err := c.observeUpdateThreads(context.Background())
	if err != nil || len(out.Threads) != 1 || out.Threads[0].BackgroundTerminals != 1 || out.Threads[0].BackgroundRevision == "" {
		t.Fatalf("idle parent lost its running background terminal: %+v %v", out, err)
	}
	f.mu.Lock()
	f.reject = "thread/backgroundTerminals/list"
	f.mu.Unlock()
	if _, err := c.observeUpdateThreads(context.Background()); err == nil {
		t.Fatal("failed fresh terminal read used a cached empty list")
	}
}

func TestUpdateOwnedCodexReadsFreshThreadsTerminalsAndPendingActivity(t *testing.T) {
	n := remoteTestNode(t)
	f, c := connectRemoteFixture(t, n, t.TempDir(), "fixture", remoteRaw)
	c.wire.mu.Lock()
	c.wire.threadID = remoteRaw
	c.wire.turnID = "active-turn"
	c.wire.mu.Unlock()
	p := &assistantProc{cx: c.wire, cmd: &exec.Cmd{Process: &os.Process{Pid: 123}}, exited: make(chan struct{})}
	rows, _, err := n.observeUpdateAssistant(context.Background(), p)
	if err != nil || len(rows) != 1 || rows[0].Status != proto.StatusWorking {
		t.Fatalf("owned activity: %+v %v", rows, err)
	}
	c.wire.mu.Lock()
	c.wire.turnID = ""
	c.wire.mu.Unlock()
	f.mu.Lock()
	f.backgroundReply = map[string]any{"data": []any{map[string]any{"command": "synthetic"}}}
	f.mu.Unlock()
	rows, _, err = n.observeUpdateAssistant(context.Background(), p)
	if err != nil || rows[0].Background != 1 {
		t.Fatalf("owned background disappeared: %+v %v", rows, err)
	}
	if f.count("thread/resume") != 0 || f.count("turn/start") != 0 {
		t.Fatal("inventory mutated the thread")
	}
}
