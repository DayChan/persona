package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type usageStartupWriter struct{ reports chan []byte }

func (w usageStartupWriter) Write(p []byte) (int, error) {
	w.reports <- append([]byte(nil), p...)
	return len(p), nil
}

func usageStartupNode(t *testing.T) (*Node, <-chan []byte) {
	t.Helper()
	home := t.TempDir()
	config := filepath.Join(home, "claude-fixture")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	dir := filepath.Join(config, "projects", "fixture")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	body := claudeReply(now.Add(-2*time.Hour), "historical", "fixture-model", 10, 0, 0, 5) +
		claudeReply(now, "current", "fixture-model", 10, 0, 0, 5)
	if err := os.WriteFile(filepath.Join(dir, "fixture-session.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	w := usageStartupWriter{reports: make(chan []byte, 4)}
	n := &Node{home: home, dir: home, state: newState(), logger: log.New(io.Discard, "", 0)}
	n.peer = rpc.New("usage-test", w)
	t.Cleanup(func() { n.peer.Close(io.EOF) })
	return n, w.reports
}

func TestUsageMeterWaitsForLauncherState(t *testing.T) {
	n, reports := usageStartupNode(t)
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); n.meterUsage(ctx, ready) }()
	defer func() { cancel(); <-done }()
	select {
	case p := <-reports:
		t.Fatalf("reported before launcher state loaded: %s", p)
	case <-time.After(50 * time.Millisecond):
	}
	n.state.merge(map[string]sessionState{"fixture-session": {Launcher: "fixture-launcher"}})
	close(ready)
	select {
	case p := <-reports:
		var report struct {
			Event  string `json:"event"`
			Params struct {
				Full    bool                `json:"full"`
				Buckets []proto.UsageBucket `json:"buckets"`
			} `json:"params"`
		}
		if err := json.Unmarshal(p, &report); err != nil {
			t.Fatal(err)
		}
		if report.Event != "usage" || !report.Params.Full || len(report.Params.Buckets) != 2 {
			t.Fatalf("initial usage report: %s", p)
		}
		for _, b := range report.Params.Buckets {
			if b.Launcher != "fixture-launcher" || b.Requests != 1 {
				t.Fatalf("historical/current reply attributed before state was ready: %+v", b)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no full usage report after launcher state loaded")
	}
}

func TestUsageMeterCancellationBeforeState(t *testing.T) {
	for _, alreadyReady := range []bool{false, true} {
		n, reports := usageStartupNode(t)
		ready := make(chan struct{})
		if alreadyReady {
			close(ready)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func() { defer close(done); n.meterUsage(ctx, ready) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancelled meter waited for state readiness")
		}
		select {
		case p := <-reports:
			t.Fatalf("cancelled meter published a full window: %s", p)
		default:
		}
	}
}

func TestUsageStateLoadFailureDoesNotSignalReady(t *testing.T) {
	n, reports := usageStartupNode(t)
	n.peer.Close(errors.New("fixture state service unavailable"))
	ready := make(chan struct{})
	if n.loadState(context.Background()) {
		close(ready)
		t.Fatal("failed load was reported as successful")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); n.meterUsage(ctx, ready) }()
	select {
	case p := <-reports:
		cancel()
		<-done
		t.Fatalf("failed state load replaced usage window: %s", p)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("meter did not exit after failed load and cancellation")
	}
}
