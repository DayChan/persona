package node

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Cleanup-only launcher: unexpected host operations fail rather than hiding a new start inside a cleanup test.
type abandonedCodexLauncher struct {
	host.Launcher
	host.Organizer
	close func(context.Context, host.PaneRef) error
}

func (l *abandonedCodexLauncher) Close(ctx context.Context, pane host.PaneRef, _, _ string) error {
	return l.close(ctx, pane)
}

func abandonedCodexPane(l host.Launcher, pane string) codexAbandonedStart {
	return codexAbandonedStart{launch: l, created: &host.Created{Pane: host.PaneRef{Kind: host.Herdr, ID: pane}}}
}

func TestCodexCleanupRetainsEveryFailedPane(t *testing.T) {
	const key = "project\x00codex"
	var closed []string
	failing := true
	launch := &abandonedCodexLauncher{close: func(_ context.Context, pane host.PaneRef) error {
		closed = append(closed, pane.ID)
		if failing && pane.ID == "w1:p1" {
			return errors.New("terminal unavailable")
		}
		return nil
	}}
	first, second := abandonedCodexPane(launch, "w1:p1"), abandonedCodexPane(launch, "w1:p2")
	n := &Node{codexAbandoned: map[string][]codexAbandonedStart{key: {first, second}}}
	var coded *rpc.Error
	if err := n.closeAbandonedCodex(context.Background(), key); !errors.As(err, &coded) || coded.Code != proto.CodeAgentStartStalled {
		t.Fatalf("failed cleanup did not block retry: %v", err)
	}
	if !reflect.DeepEqual(closed, []string{"w1:p1", "w1:p2"}) {
		t.Fatalf("one failure prevented cleanup of another pane: %v", closed)
	}
	if pending := n.codexAbandoned[key]; len(pending) != 1 || pending[0].created != first.created {
		t.Fatalf("cleanup retained the wrong panes: %+v", pending)
	}
	failing = false
	if err := n.closeAbandonedCodex(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if len(n.codexAbandoned) != 0 || !reflect.DeepEqual(closed, []string{"w1:p1", "w1:p2", "w1:p1"}) {
		t.Fatalf("retry lost or repeated a pane: pending=%v, closed=%v", n.codexAbandoned, closed)
	}
}

func TestCodexCleanupIncludesFailureRecordedWhileClosing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const key = "project\x00codex"
		entered, release := make(chan struct{}), make(chan struct{})
		var closed []string
		launch := &abandonedCodexLauncher{close: func(ctx context.Context, pane host.PaneRef) error {
			closed = append(closed, pane.ID)
			if pane.ID == "w1:p1" {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return errors.New("second pane still cannot close")
		}}
		first, second := abandonedCodexPane(launch, "w1:p1"), abandonedCodexPane(launch, "w1:p2")
		n := &Node{codexAbandoned: map[string][]codexAbandonedStart{key: {first}}}
		done := make(chan error, 1)
		go func() { done <- n.closeAbandonedCodex(context.Background(), key) }()
		<-entered
		// A parallel start reports its own failed cleanup while the earlier snapshot is being closed.
		n.mu.Lock()
		n.codexAbandoned[key] = append(n.codexAbandoned[key], second)
		n.mu.Unlock()
		close(release)
		var coded *rpc.Error
		if err := <-done; !errors.As(err, &coded) || coded.Code != proto.CodeAgentStartStalled {
			t.Fatalf("newly recorded failure allowed another start: %v", err)
		}
		if !reflect.DeepEqual(closed, []string{"w1:p1", "w1:p2"}) {
			t.Fatalf("newly recorded pane was not checked: %v", closed)
		}
		if pending := n.codexAbandoned[key]; len(pending) != 1 || pending[0].created != second.created {
			t.Fatalf("successful cleanup deleted a concurrent failure: %+v", pending)
		}
	})
}
