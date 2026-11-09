package assistant

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// restartReceiptStore preserves both settings and receipt rows across manager instances.
type restartReceiptStore struct {
	memStore
	muFeed sync.Mutex
	rows   []db.FeedRow
}

func (s *restartReceiptStore) AppendFeed(_ context.Context, row db.FeedRow) error {
	s.muFeed.Lock()
	defer s.muFeed.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

func (s *restartReceiptStore) receipts() int {
	s.muFeed.Lock()
	defer s.muFeed.Unlock()
	n := 0
	for _, row := range s.rows {
		if row.Kind == feedReceipt && row.Ref == "task" {
			n++
		}
	}
	return n
}

// A fresh manager must restore a pending read-only look, both before and after
// its deadline. The old manager gets a separate fake node so a surviving timer
// in this test process cannot masquerade as a look by the restarted manager.
func TestReceiptLookSurvivesManagerRestartIntegration(t *testing.T) {
	for _, overdue := range []bool{false, true} {
		name := "before due"
		if overdue {
			name = "after due"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &restartReceiptStore{}
				oldNode := newFakeHub()
				oldNode.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: "session", Status: proto.StatusWorking, State: proto.StateWorking}}
				old := New(Options{Hub: oldNode, Store: func() Store { return store }})
				linked := time.Now().Add(-time.Hour)
				todo := Todo{ID: "task", Title: "Task", Origin: "conversation", CreatedAt: linked,
					Agents: []TodoAgent{{MachineID: "box", SessionID: "session", LinkedAt: linked}}}
				putTodo(old, todo)
				oldCtx, stopOld := context.WithCancel(context.Background())
				oldDone := make(chan struct{})
				go func() { defer close(oldDone); old.Run(oldCtx) }()
				synctest.Wait()
				due := time.Now().Add(time.Minute)
				old.taskTurnEndedAfter(oldCtx, "box", "session", due)
				stopOld()
				<-oldDone
				if overdue {
					<-time.After(2 * time.Minute)
				}

				node := newFakeHub()
				node.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: "session", Status: proto.StatusDone,
					State: proto.StateIdle, LastText: "Finished", UpdatedAt: time.Now()}}
				node.items = []proto.Item{{Role: "assistant", Kind: "text", Text: "Finished"}}
				restarted := New(Options{Hub: node, Store: func() Store { return store }})
				putTodo(restarted, todo)
				ctx, stop := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { defer close(done); restarted.Run(ctx) }()
				synctest.Wait()
				if !overdue && store.receipts() != 0 {
					t.Error("receipt was produced before the pending look was due")
				}
				// Give a past-due look time to spread and a future look time to become due.
				<-time.After(3 * time.Minute)
				synctest.Wait()
				stop()
				<-done
				if got := store.receipts(); got < 1 {
					t.Errorf("restarted manager produced %d receipts; want at least one", got)
				}
				node.mu.Lock()
				reads := node.itemReads
				node.mu.Unlock()
				if reads == 0 {
					t.Error("restarted manager never checked the node's transcript")
				}
				// A direct look proves that the restored task and node can produce the
				// receipt; it is deliberately after the restart assertions.
				if store.receipts() == 0 {
					restarted.taskTurnEnded(context.Background(), "box", "session")
					if store.receipts() == 0 {
						t.Error("positive control: a direct look did not produce a receipt")
					}
				}
			})
		})
	}
}
