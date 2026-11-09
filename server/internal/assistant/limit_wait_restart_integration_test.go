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
	muFeed       sync.Mutex
	todo         Todo
	rows         []db.FeedRow
	writes       chan bool
	blockOnce    chan struct{}
	blockSuccess bool
}

func (s *restartReceiptStore) AppendFeed(_ context.Context, row db.FeedRow) error {
	s.muFeed.Lock()
	defer s.muFeed.Unlock()
	s.rows = append(s.rows, row)
	return nil
}

func (s *restartReceiptStore) setTodo(todo Todo) {
	s.muFeed.Lock()
	defer s.muFeed.Unlock()
	s.todo = todo
}

// AppendTaskReceiptOnce validates the active association and commits the feed
// atomically, then can block its acknowledgment to simulate an interrupted process.
func (s *restartReceiptStore) AppendTaskReceiptOnce(_ context.Context, row db.FeedRow, machine, session string, linked time.Time) (bool, bool, error) {
	s.muFeed.Lock()
	current, active := receiptLink(s.todo, SessionRef{MachineID: machine, SessionID: session})
	if s.todo.ID != row.Ref || s.todo.Origin != row.Conversation || s.todo.DoneAt != nil || !active || !current.Equal(linked) {
		s.muFeed.Unlock()
		return false, false, nil
	}
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
	block, writes, success := s.blockOnce, s.writes, s.blockSuccess
	s.blockOnce = nil
	s.muFeed.Unlock()
	if writes != nil {
		writes <- inserted
	}
	if block != nil {
		<-block
		if !success {
			return false, true, errors.New("process stopped after feed commit")
		}
	}
	return inserted, true, nil
}

// The production path checks for the generic idempotent store as well as the
// transactional capability; receipt writes must use the latter.
func (s *restartReceiptStore) AppendFeedOnce(context.Context, db.FeedRow) (bool, error) {
	return false, errors.New("transactional receipt insert required")
}

// stoppedReceiptStore prevents callbacks from the simulated dead process from
// performing later feed writes while their goroutines unwind in this process.
type stoppedReceiptStore struct {
	*restartReceiptStore
	stopped <-chan struct{}
}

func (s *stoppedReceiptStore) stoppedError() error {
	select {
	case <-s.stopped:
		return errors.New("process stopped")
	default:
		return nil
	}
}

func (s *stoppedReceiptStore) AppendTaskReceiptOnce(ctx context.Context, row db.FeedRow, machine, session string, linked time.Time) (bool, bool, error) {
	if err := s.stoppedError(); err != nil {
		return false, false, err
	}
	return s.restartReceiptStore.AppendTaskReceiptOnce(ctx, row, machine, session, linked)
}

func (s *stoppedReceiptStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if err := s.stoppedError(); err != nil {
		return false, err
	}
	return s.restartReceiptStore.Setting(ctx, key, out)
}

func (s *stoppedReceiptStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	if err := s.stoppedError(); err != nil {
		return false, err
	}
	return s.restartReceiptStore.CompareSetting(ctx, key, revision, value)
}

func (s *stoppedReceiptStore) PutSetting(ctx context.Context, key string, value any) error {
	if err := s.stoppedError(); err != nil {
		return err
	}
	return s.restartReceiptStore.PutSetting(ctx, key, value)
}

// restartReceiptObserver identifies writes made by the restarted manager, even
// if a callback from the old manager is still unwinding in the test process.
type restartReceiptObserver struct {
	*restartReceiptStore
	writesFromRestart chan bool
	readsFromRestart  chan struct{}
}

func (s *restartReceiptObserver) AppendTaskReceiptOnce(ctx context.Context, row db.FeedRow, machine, session string, linked time.Time) (bool, bool, error) {
	inserted, valid, err := s.restartReceiptStore.AppendTaskReceiptOnce(ctx, row, machine, session, linked)
	if err == nil && valid {
		s.writesFromRestart <- inserted
	}
	return inserted, valid, err
}

func (s *restartReceiptStore) FeedItem(_ context.Context, id string) (*db.FeedRow, error) {
	s.muFeed.Lock()
	defer s.muFeed.Unlock()
	for _, row := range s.rows {
		if row.ID == id {
			copy := row
			return &copy, nil
		}
	}
	return nil, nil
}

// FeedItem records reads by the restarted manager only; an old callback cannot satisfy the assertion.
func (s *restartReceiptObserver) FeedItem(ctx context.Context, id string) (*db.FeedRow, error) {
	row, err := s.restartReceiptStore.FeedItem(ctx, id)
	if err == nil && row != nil && s.readsFromRestart != nil {
		select {
		case s.readsFromRestart <- struct{}{}:
		default:
		}
	}
	return row, err
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
				store.setTodo(todo)
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
		store := &restartReceiptStore{writes: make(chan bool, 4), blockOnce: release}
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
		stopped := make(chan struct{})
		oldStore := &stoppedReceiptStore{restartReceiptStore: store, stopped: stopped}
		old := New(Options{Hub: newNode(), Store: func() Store { return oldStore }})
		store.setTodo(todo)
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
		close(stopped)
		stopOld()
		<-oldDone
		releaseOnce.Do(func() { close(release) })
		synctest.Wait()
		var pending receiptLookState
		if _, err := store.Setting(context.Background(), receiptLooksSetting, &pending); err != nil || len(pending.Looks) != 1 {
			t.Fatalf("after stopped manager unwound, pending looks = %+v, error %v", pending, err)
		}

		observed := &restartReceiptObserver{restartReceiptStore: store, writesFromRestart: make(chan bool, 4), readsFromRestart: make(chan struct{}, 4)}
		restarted := New(Options{Hub: newNode(), Store: func() Store { return observed }})
		putTodo(restarted, todo)
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); restarted.Run(ctx) }()
		select {
		case <-observed.readsFromRestart:
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

// Cancellation of Run is a graceful shutdown, not a killed process: an
// already blocked feed call may still return. Its receipt work must remain
// recoverable if the process exits before its delayed notice is delivered.
func TestReceiptFollowUpAfterGracefulStopIntegration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(release) })
		store := &restartReceiptStore{writes: make(chan bool, 4), blockOnce: release, blockSuccess: true}
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
		store.setTodo(todo)
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
		var pending receiptLookState
		if _, err := store.Setting(context.Background(), receiptLooksSetting, &pending); err != nil || len(pending.Looks) != 1 {
			old.quiet.mu.Lock()
			queued := old.quiet.pending["task"] != nil
			old.quiet.mu.Unlock()
			t.Fatalf("after Run stopped with a feed call in flight: durable looks = %+v, old manager has delayed notice = %v, error = %v", pending, queued, err)
		}
		observed := &restartReceiptObserver{restartReceiptStore: store, writesFromRestart: make(chan bool, 4), readsFromRestart: make(chan struct{}, 4)}
		restarted := New(Options{Hub: newNode(), Store: func() Store { return observed }})
		putTodo(restarted, todo)
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); restarted.Run(ctx) }()
		select {
		case <-observed.readsFromRestart:
		case <-time.After(5 * time.Minute):
			stop()
			<-done
			t.Fatal("the restarted manager did not revisit the committed receipt")
		}
		synctest.Wait()
		restarted.quiet.mu.Lock()
		notice := restarted.quiet.pending["task"]
		restarted.quiet.mu.Unlock()
		if notice == nil || notice.state != "done" {
			t.Error("the restarted manager did not resume the completion notice")
		}
		if got := store.receipts(); got != 1 {
			t.Errorf("feed contains %d receipts; want one", got)
		}
		stop()
		<-done
	})
}
