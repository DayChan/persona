package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// Both requests must reach their own thread wait before either is released. A lock spanning startCodex would
// prevent the second request from reaching this barrier until the first timed out or was cancelled.
func TestCodexStartsInSameDirectoryRunConcurrently(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanupFails=%v", cleanupFails), func(t *testing.T) {
			n, f := stalledCodexNode(t)
			codexStartWait = time.Minute
			next := 0
			f.handle("tab.create", func(map[string]any) any {
				next++
				return map[string]any{"tab": map[string]any{"tab_id": fmt.Sprintf("w1:t%d", next)}, "root_pane": map[string]any{"pane_id": fmt.Sprintf("w1:p%d", next)}}
			})
			if cleanupFails {
				f.reply("tab.close", herdrFail{Code: "unavailable", Message: "terminal unavailable"})
			}
			waiting := make(chan string, 2)
			seen := map[string]bool{}
			f.after(func(c herdrCall) {
				if c.Method == "pane.read" && c.Params["source"] == "visible" && c.Params["lines"] == float64(screen.DialogLines) {
					pane := str(c.Params["pane_id"])
					if !seen[pane] {
						seen[pane] = true
						waiting <- pane
					}
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			results := make(chan error, 2)
			var wg sync.WaitGroup
			defer func() { cancel(); wg.Wait() }()
			cwd := filepath.Join(filepath.Dir(n.dir), "proj")
			launch := n.defaultHost().(host.Launcher)
			panes := map[string]bool{}
			for i := range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := n.startCodex(ctx, launch, cwd, "codex", []string{"codex"}, fmt.Sprintf("subtask %d", i), "w1", "", "")
					results <- err
				}()
				select {
				case pane := <-waiting:
					panes[pane] = true
				case err := <-results:
					t.Fatalf("start returned before both thread waits: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("one start blocked another from waiting for its own thread")
				}
			}
			if len(panes) != 2 {
				t.Fatalf("starts did not get distinct panes: %v", panes)
			}
			select {
			case err := <-results:
				t.Fatalf("start ended before the concurrency barrier: %v", err)
			default:
			}
			cancel()
			for range 2 {
				err := <-results
				if cleanupFails {
					var coded *rpc.Error
					if !errors.As(err, &coded) || coded.Code != proto.CodeAgentStartStalled {
						t.Fatalf("cleanup failure lost code: %v", err)
					}
				} else if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled wait: %v", err)
				}
			}
			key := cwd + "\x00codex"
			n.mu.Lock()
			pending := len(n.codexAbandoned[key])
			n.mu.Unlock()
			want := 0
			if cleanupFails {
				want = 2
			}
			if pending != want {
				t.Fatalf("pending cleanups = %d, want %d", pending, want)
			}
			if cleanupFails {
				f.reply("tab.close", map[string]any{})
				if err := n.closeAbandonedCodex(context.Background(), key); err != nil {
					t.Fatal(err)
				}
				if len(n.codexAbandoned) != 0 {
					t.Fatal("retry did not clean both abandoned panes")
				}
			}
		})
	}
}
