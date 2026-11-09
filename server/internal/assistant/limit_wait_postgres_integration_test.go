package assistant

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestOrdinaryReceiptWithoutWaitKeepsCompletionPostgres(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated Postgres test database not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(u.Path, "/persona_test_") {
		t.Fatal("test requires a disposable database")
	}
	ctx := context.Background()
	st, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	id := "ordinary_receipt_" + randomHex(8)
	conversation := "ordinary-receipt-" + id
	sid := "ordinary_" + randomHex(8)
	linked := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := st.CreateTodo(ctx, db.TodoRow{ID: id, Title: "Task", Origin: conversation, CreatedAt: linked, UpdatedAt: linked}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteTodo(context.Background(), id) })
	if _, err := st.LinkTodoAgent(ctx, id, "box", sid, "", "", "", "", linked); err != nil {
		t.Fatal(err)
	}
	f := newFakeHub()
	f.judge = `{"verdict":"done","reason":"Verified"}`
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "Do the work"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Work verified"})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: sid, Status: proto.StatusDone, State: proto.StateUnread, LastText: "Work verified", UpdatedAt: time.Now()})
	s := newServiceOn(t, f, st)
	s.taskWorking(ctx, "box", sid)
	s.taskTurnEnded(ctx, "box", sid)
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	stored, err := st.GetTodo(ctx, id)
	if err != nil || judged == 0 || stored.DoneAt == nil {
		t.Fatalf("ordinary completion: judgments=%d todo=%+v error=%v", judged, stored, err)
	}
	s.reports.mu.Lock()
	report := s.reports.pending[id]
	s.reports.mu.Unlock()
	if report == nil {
		t.Fatal("ordinary completion did not queue its task report")
	}
}

func TestOrdinaryReceiptRejectsUnlinkAfterOtherWaitRemovedPostgres(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated Postgres test database not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(u.Path, "/persona_test_") {
		t.Fatal("test requires a disposable database")
	}
	ctx := context.Background()
	a, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	b, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	id := "cleared_wait_" + randomHex(8)
	conversation := "cleared-wait-" + id
	trigger, waiting := "trigger_"+randomHex(8), "waiting_"+randomHex(8)
	linked := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := a.CreateTodo(ctx, db.TodoRow{ID: id, Title: "Task", Origin: conversation, CreatedAt: linked, UpdatedAt: linked}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.DeleteTodo(context.Background(), id) })
	for _, sid := range []string{trigger, waiting} {
		if _, err := a.LinkTodoAgent(ctx, id, "box", sid, "", "", "", "", linked); err != nil {
			t.Fatal(err)
		}
	}
	first := New(Options{Hub: newFakeHub(), Store: func() Store { return a }})
	if err := first.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	todo, ok := first.taskOf(ctx, "box", trigger)
	if !ok {
		t.Fatal("manager A did not cache the original link")
	}
	first.taskTurnEndedAfter(ctx, "box", waiting, time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := a.Setting(ctx, receiptLooksSetting, &state); err != nil {
		t.Fatal(err)
	}
	var look receiptLook
	for _, candidate := range state.Looks {
		if candidate.Todo == id && candidate.Session == waiting {
			look = candidate
			break
		}
	}
	if look.ID == "" {
		t.Fatal("other agent's wait was not registered")
	}
	second := New(Options{Hub: newFakeHub(), Store: func() Store { return b }})
	second.clearReceiptLook(ctx, receiptKeyOf(look), look.ID)
	if err := b.UnlinkTodoAgent(ctx, id, "box", trigger); err != nil {
		t.Fatal(err)
	}
	_, err = first.appendTaskReceiptForLook(ctx, todo, SessionRef{MachineID: "box", SessionID: trigger}, receiptBody{State: "interrupted"}, time.Time{}, false, nil)
	if err == nil {
		t.Fatal("ordinary receipt ignored committed unlink after wait removal")
	}
	rows, err := a.Feed(ctx, conversation, time.Time{}, 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("feed after unlink = %+v, %v", rows, err)
	}
}

func TestOtherAgentLookDoesNotAuthorizeUnlinkedTriggerPostgres(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated Postgres test database not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(u.Path, "/persona_test_") {
		t.Fatal("test requires a disposable database")
	}
	ctx := context.Background()
	a, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	b, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	id := "other_agent_" + randomHex(8)
	conversation := "other-agent-" + id
	trigger := "trigger_" + randomHex(8)
	waiting := "waiting_" + randomHex(8)
	linked := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := a.CreateTodo(ctx, db.TodoRow{ID: id, Title: "Task", Origin: conversation, CreatedAt: linked, UpdatedAt: linked}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.DeleteTodo(context.Background(), id) })
	for _, sid := range []string{trigger, waiting} {
		if _, err := a.LinkTodoAgent(ctx, id, "box", sid, "", "", "", "", linked); err != nil {
			t.Fatal(err)
		}
	}
	s := New(Options{Hub: newFakeHub(), Store: func() Store { return a }})
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	todo, ok := s.taskOf(ctx, "box", trigger)
	if !ok {
		t.Fatal("manager A did not cache the trigger link")
	}
	s.taskTurnEndedAfter(ctx, "box", waiting, time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := a.Setting(ctx, receiptLooksSetting, &state); err != nil {
		t.Fatal(err)
	}
	var look receiptLook
	for _, candidate := range state.Looks {
		if candidate.Todo == id && candidate.Session == waiting {
			look = candidate
			break
		}
	}
	if look.ID == "" {
		t.Fatal("manager A did not register agent B's wait")
	}
	t.Cleanup(func() { s.clearReceiptLook(context.Background(), receiptKeyOf(look), look.ID) })
	if err := b.UnlinkTodoAgent(ctx, id, "box", trigger); err != nil {
		t.Fatal(err)
	}
	_, err = s.appendTaskReceiptForLook(ctx, todo, SessionRef{MachineID: "box", SessionID: trigger}, receiptBody{State: "interrupted"}, time.Time{}, false, nil)
	if err == nil {
		t.Fatal("agent B's wait authorized an unlinked trigger")
	}
	rows, err := a.Feed(ctx, conversation, time.Time{}, 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("feed after unlink = %+v, %v", rows, err)
	}
	if _, err := a.Setting(ctx, receiptLooksSetting, &state); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range state.Looks {
		if candidate.ID == look.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("agent B's wait was removed")
	}
}

func TestBoundReceiptRetryRejectsPostgresUnlink(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated Postgres test database not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(u.Path, "/persona_test_") {
		t.Fatal("test requires a disposable database")
	}
	ctx := context.Background()
	a, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	b, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	id := "bound_unlink_" + randomHex(8)
	linked := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := a.CreateTodo(ctx, db.TodoRow{ID: id, Title: "Task", Origin: "bound-unlink-conversation", CreatedAt: linked, UpdatedAt: linked}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LinkTodoAgent(ctx, id, "box", "session", "", "", "", "", linked); err != nil {
		t.Fatal(err)
	}
	node := newFakeHub()
	s := New(Options{Hub: node, Store: func() Store { return a }})
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	todo, found := s.taskOf(ctx, "box", "session")
	if !found {
		t.Fatal("first manager did not cache the association")
	}
	if err := b.UnlinkTodoAgent(ctx, id, "box", "session"); err != nil {
		t.Fatal(err)
	}
	ref := SessionRef{MachineID: "box", SessionID: "session"}
	expected := &receiptLook{Todo: id, Machine: "box", Session: "session", Linked: linked, Work: linked, RetryID: "feed_bound_" + randomHex(8)}
	if _, err := s.appendTaskReceiptForLook(ctx, todo, ref, receiptBody{State: "interrupted"}, linked, false, expected); err == nil {
		t.Fatal("cached association bypassed committed unlink")
	}
	if row, err := a.FeedItem(ctx, expected.RetryID); err != nil || row != nil {
		t.Fatalf("feed after unlink = %+v, %v", row, err)
	}
}

// This test uses actual manager tables and independent connections. The
// package's TestMain creates a disposable database from PERSONA_TEST_DATABASE_URL.
// It checks the durable association separately from virtual-time lifecycle tests.
func TestReceiptLookPersistsWithPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated Postgres test database not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(u.Path, "/persona_test_") {
		t.Fatal("test requires the disposable database created by dbtest")
	}
	ctx := context.Background()
	firstDB, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(firstDB.Close)
	id := "receipt_integration_" + randomHex(8)
	session := "session_" + randomHex(8)
	linked := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	if err := firstDB.CreateTodo(ctx, db.TodoRow{ID: id, Title: "Task", Origin: "conversation", CreatedAt: linked, UpdatedAt: linked}); err != nil {
		t.Fatal(err)
	}
	if _, err := firstDB.LinkTodoAgent(ctx, id, "box", session, "", "", "", "", linked); err != nil {
		t.Fatal(err)
	}
	node := newFakeHub()
	node.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: session, Status: proto.StatusWorking, State: proto.StateWorking}}
	first := New(Options{Hub: node, Store: func() Store { return firstDB }})
	oldCtx, stopOld := context.WithCancel(ctx)
	first.restoreTaskReceiptLooks(oldCtx)
	due := time.Now().Add(time.Hour).UTC()
	first.taskTurnEndedAfter(oldCtx, "box", session, due)
	stopOld()

	// A separate handle and service force both the todo link and wait to be read
	// from Postgres, without relying on either manager's in-memory cache.
	secondDB, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondDB.Close)
	second := New(Options{Hub: node, Store: func() Store { return secondDB }})
	if err := second.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	todo, ok := second.taskOf(ctx, "box", session)
	if !ok || todo.ID != id {
		t.Fatalf("restored active todo = %q, found %v", todo.ID, ok)
	}
	var state receiptLookState
	if found, err := secondDB.Setting(ctx, receiptLooksSetting, &state); err != nil || !found {
		t.Fatalf("stored receipt waits = %+v, found %v, error %v", state, found, err)
	}
	var look receiptLook
	for _, candidate := range state.Looks {
		if candidate.Todo == id {
			look = candidate
			break
		}
	}
	if look.Todo != id || look.Machine != "box" || look.Session != session || !look.Linked.Equal(linked) || !look.At.Equal(due) {
		t.Fatalf("stored receipt wait = %+v", look)
	}
	if valid, err := second.receiptLookLinked(ctx, look); err != nil || !valid {
		t.Fatalf("restored task association valid = %v, error %v", valid, err)
	}
	restartedCtx, stop := context.WithCancel(ctx)
	second.restoreTaskReceiptLooks(restartedCtx)
	stop()
}
