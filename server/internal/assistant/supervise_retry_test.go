package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A digest due while the main conversation cannot be found (the hub's hello has no machine while the bus is down)
// keeps its signals: they go out once it is back, instead of being marked told and dropped (#5420).
func TestDigestWaitsForTheMainConversation(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "甲", "做甲", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"PR 合并"}, 0)
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hello.Assistant.MachineID = "" // the hub is not reachable: no hello
	f.mu.Unlock()
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "1", Detail: "做完了"})
	time.Sleep(10 * shortSuperviseTimings.gather) // the digest was due, and found nowhere to go
	if n := f.notices(); len(n) != 0 {
		t.Fatalf("sent with no main conversation: %q", n)
	}
	f.mu.Lock()
	f.hello.Assistant.MachineID = "server"
	f.mu.Unlock()
	waitFor(t, "the message once the hub is back", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "任务「甲」") {
		t.Fatalf("message:\n%s", n)
	}
}

// A digest due while the main conversation is busy and its relay queue can't be read keeps its signals: they are
// neither dropped nor marked told, and go out once the queue reads again. Nothing went out, so a signal that is not
// urgent is tried again in the urgent gap, not a full gap after the attempt that failed.
func TestDigestKeptWhenRelayUnreadable(t *testing.T) {
	st := &memStore{}
	st.failKey(relayKey, errors.New("database hiccup"))
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes.every = time.Hour // a review is not urgent: only the urgent gap brings it back within the test
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "甲", "做甲", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"PR 合并"}, 0)
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	busy := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}
	f.setSessions("server", busy)
	g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "1", Detail: "做完了", At: time.Now().Add(-time.Minute)}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	b.pending[g.id()] = g
	b.mu.Unlock()

	s.sendDigest(ctx)
	// It is tried again while the queue stays unreadable: between tries the signal waits, untold.
	waitFor(t, "the signal kept", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		_, pending := b.pending[g.id()]
		return pending && b.sent[g.id()] != g.Key
	})
	if n := f.notices(); len(n) != 0 {
		t.Fatalf("sent into a busy conversation: %q", n)
	}

	st.failKey(relayKey, nil)
	waitFor(t, "the message queued once the queue reads", func() bool { return s.queuedFor("conv1") == 1 })
	idle := busy
	idle.Status = proto.StatusIdle
	f.setSessions("server", idle)
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the message", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "任务「甲」") {
		t.Fatalf("message:\n%s", n)
	}
	// Nothing is left to send it again: the signal no longer waits, it is marked told (a later sweep skips it) and the
	// queue is empty.
	b.mu.Lock()
	_, pending := b.pending[g.id()]
	told := b.sent[g.id()] == g.Key
	b.mu.Unlock()
	if pending || !told || s.queuedFor("conv1") != 0 {
		t.Fatalf("after it went out: pending %v, told %v, %d queued", pending, told, s.queuedFor("conv1"))
	}
}

// Signals that could not be queued give way to a newer one of the same task and kind that came meanwhile; a newer
// report joins them, so neither question is lost. A message that went out meanwhile stays the last one.
func TestDigestUnqueuedKeepsNewerSignals(t *testing.T) {
	st := &memStore{}
	st.failKey(relayKey, errors.New("database hiccup"))
	s, _ := newQuietServiceOn(t, st)
	s.superviseTimes.urgent = time.Hour // the retry stays off while the test looks
	stalled := superviseSignal{Todo: "a", Kind: sigStalled, Key: "1", Minutes: 25}
	newer := superviseSignal{Todo: "a", Kind: sigStalled, Key: "2", Minutes: 40}
	asked := superviseSignal{Todo: "b", Kind: sigReport, Report: upReportQuestion, Key: "1", Detail: "用哪个分支？"}
	again := superviseSignal{Todo: "b", Kind: sigReport, Report: upReportQuestion, Key: "2", Detail: "要不要补测试？"}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	b.sent[stalled.id()], b.sent[asked.id()] = stalled.Key, asked.Key
	b.pending[newer.id()], b.pending[again.id()] = newer, again
	at := time.Now().Add(-time.Minute)
	later := at.Add(30 * time.Second)
	b.last = later
	b.mu.Unlock()
	if s.queueDigest("conv1", relayMsg{Tool: superviseTool}, []superviseSignal{stalled, asked}, at, time.Time{}) {
		t.Fatal("queued with the relay queue unreadable")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.last.Equal(later) {
		t.Errorf("last = %v; want the message that went out meanwhile (%v)", b.last, later)
	}
	if g := b.pending[stalled.id()]; g.Key != "2" || g.Minutes != 40 {
		t.Errorf("stalled = %+v; want the newer one", g)
	}
	if g := b.pending[asked.id()]; g.Key != "2" || !strings.Contains(g.Detail, asked.Detail) || !strings.Contains(g.Detail, again.Detail) {
		t.Errorf("report = %+v; want both questions", g)
	}
	if len(b.sent) != 0 {
		t.Errorf("still marked told: %v", b.sent)
	}
}
