package assistant

import (
	"context"
	"errors"
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
	muFeed    sync.Mutex
	rows      []db.FeedRow
	writes    chan bool
	reads     chan struct{}
	blockOnce chan struct{}
}

func (s *restartReceiptStore) AppendFeed(_ context.Context, row db.FeedRow) error {
	s.muFeed.Lock()
	defer s.muFeed.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

// AppendFeedOnce commits before optionally blocking its caller, as if the process
// stopped after the database write but before the manager got its acknowledgment.
func (s *restartReceiptStore) AppendFeedOnce(_ context.Context, row db.FeedRow) (bool, error) {
	s.muFeed.Lock()
	inserted := true
	for _, existing := range s.rows {
		if existing.ID == row.ID {
			inserted = false
			break
		}
	}
	if inserted {
		s.rows = append(s.rows, row)
	}
	block, writes := s.blockOnce, s.writes
	s.blockOnce = nil
	s.muFeed.Unlock()
	if writes != nil {
		writes <- inserted
	}
	if block != nil {
		<-block
		return false, errors.New("process stopped after feed commit")
	}
	return inserted, nil
}

func (s *restartReceiptStore) FeedItem(_ context.Context, id string) (*db.FeedRow, error) {
	s.muFeed.Lock()
	defer s.muFeed.Unlock()
	for _, row := range s.rows {
		if row.ID == id {
			if s.reads != nil {
				s.reads <- struct{}{}
			}
			copy := row
			return &copy, nil
		}
	}
	return nil, nil
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

// The feed row can commit just before a manager dies. A new manager must
// replay the follow-up even when the idempotent feed insert finds that row.
func TestReceiptFollowUpAfterFeedCommitRestartIntegration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		store := &restartReceiptStore{writes: make(chan bool, 4), reads: make(chan struct{}, 4), blockOnce: release}
		linked := time.Now().Add(-time.Hour)
		todo := Todo{ID: "task", Title: "Task", Origin: "conversation", Quiet: true, CreatedAt: linked,
			Agents: []TodoAgent{{MachineID: "box", SessionID: "session", LinkedAt: linked}}}
		newNode := func() *fakeHub {
			node := newFakeHub()
			node.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: "session", Status: proto.StatusDone,
				State: proto.StateIdle, LastText: "Finished", UpdatedAt: time.Now()}}
			node.items = []proto.Item{{Role: "assistant", Kind: "text", Text: "Finished"}}
			return node
		}
		old := New(Options{Hub: newNode(), Store: func() Store { return store }})
		putTodo(old, todo)
		oldCtx, stopOld := context.WithCancel(context.Background())
		oldDone := make(chan struct{})
		go func() { defer close(oldDone); old.Run(oldCtx) }()
		synctest.Wait()
		old.taskTurnEndedAfter(oldCtx, "box", "session", time.Now().Add(time.Minute))
		select {
		case inserted := <-store.writes:
			if !inserted {
				t.Fatal("first feed write did not insert the receipt")
			}
		case <-time.After(2 * time.Minute):
			stopOld()
			<-oldDone
			t.Fatal("the original manager did not commit the feed row")
		}
		stopOld()
		<-oldDone
		releaseOnce.Do(func() { close(release) })
		synctest.Wait()

		restarted := New(Options{Hub: newNode(), Store: func() Store { return store }})
		putTodo(restarted, todo)
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); restarted.Run(ctx) }()
		select {
		case <-store.reads:
		case <-time.After(5 * time.Minute):
			stop()
			<-done
			t.Fatal("the restarted manager did not revisit the committed receipt")
		}
		synctest.Wait()
		if got := store.receipts(); got != 1 {
			t.Errorf("feed contains %d receipts; want one", got)
		}
		restarted.quiet.mu.Lock()
		notice := restarted.quiet.pending["task"]
		restarted.quiet.mu.Unlock()
		if notice == nil || notice.state != "done" {
			t.Error("restarted manager did not resume the completion notice")
		}
		stop()
		<-done
	})
}
