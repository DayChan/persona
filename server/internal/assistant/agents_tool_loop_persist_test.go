package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func persistentLoopFixture(t *testing.T) (*Service, *fakeHub, *workMemStore, proto.Session, proto.Interaction) {
	t.Helper()
	store := &workMemStore{}
	f := newFakeHub()
	c := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 30, Since: time.Now().UTC().Add(-time.Minute).Truncate(time.Second)}}
	f.setSessions("box", c)
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	k := [2]string{"box", "s1"}
	st := &toolLoopState{tool: c.ToolLoop.Tool, since: c.ToolLoop.Since, pending: true, checks: 2, checkedCount: 7}
	s.work.mu.Lock()
	s.work.init()
	s.work.loops[k] = st
	s.work.mu.Unlock()
	s.reviewToolLoop(context.Background(), k, st, *c.ToolLoop, true)
	cards := s.workCards()
	if len(cards) != 1 {
		t.Fatalf("expected card, got %+v", cards)
	}
	return s, f, store, c, cards[0]
}

func TestToolLoopCardAndContinueSurviveRestart(t *testing.T) {
	_, f, store, c, card := persistentLoopFixture(t)
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card.ID {
		t.Fatalf("restored cards: %+v", cards)
	}
	// A node reconnecting without a transcript must not withdraw the card.
	missing := c
	missing.ToolLoop = nil
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	missing.Status = proto.StatusOffline
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	if len(s.workCards()) != 1 {
		t.Fatal("missing candidate or offline session withdrew restored card")
	}
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[1].Label}})
	s = newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.considerToolLoop(context.Background(), "box", c)
	s.work.mu.Lock()
	st := s.work.loops[[2]string{"box", "s1"}]
	ok := st != nil && st.silenced && st.checks == 2 && st.checkedCount == 7
	s.work.mu.Unlock()
	if !ok || len(s.workCards()) != 0 {
		t.Fatal("continued loop or model budget was not restored")
	}
}

func TestToolLoopRescanKeepsContinueQuiet(t *testing.T) {
	s, f, store, c, card := persistentLoopFixture(t)
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[1].Label}})
	s = newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	missing := c
	missing.ToolLoop = nil
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	shifted := *c.ToolLoop
	shifted.Since = shifted.Since.Add(20 * time.Second)
	c.ToolLoop = &shifted
	f.setSessions("box", c)
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{c}})
	s.considerToolLoop(context.Background(), "box", c)
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("rescanned loop asked again: %+v", cards)
	}
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.work.mu.Lock()
	st := s.work.loops[[2]string{"box", "s1"}]
	quiet := st != nil && st.silenced && st.since.Equal(shifted.Since)
	s.work.mu.Unlock()
	if !quiet {
		t.Fatal("rescanned loop silence was not saved")
	}
}

func TestToolLoopNodeOnlyRescanKeepsContinueQuiet(t *testing.T) {
	s, f, _, c, card := persistentLoopFixture(t)
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[1].Label}})
	missing := c
	missing.ToolLoop = nil
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	shifted := *c.ToolLoop
	shifted.Since = shifted.Since.Add(20 * time.Second)
	c.ToolLoop = &shifted
	f.setSessions("box", c)
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{c}})
	s.considerToolLoop(context.Background(), "box", c)
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("node-only rescan asked again: %+v", cards)
	}
}

func TestToolLoopRescanWithdrawsOldCardAndAllowsNewTurn(t *testing.T) {
	_, f, store, c, card := persistentLoopFixture(t)
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	missing := c
	missing.Status, missing.ToolLoop = proto.StatusOffline, nil
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	shifted := *c.ToolLoop
	shifted.Since = shifted.Since.Add(time.Second)
	c.ToolLoop = &shifted
	f.setSessions("box", c)
	// The tick may see the reconstructed candidate before a session event.
	s.considerToolLoop(context.Background(), "box", c)
	if len(s.workCards()) != 0 {
		t.Fatal("old card stayed up after ambiguous rescan")
	}
	answer := proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[0].Label}}
	if _, err := s.toolLoopAnswer(context.Background(), card.ID, answer); err == nil {
		t.Fatal("old card could interrupt a different candidate")
	}
	s.clearToolLoop([2]string{"box", "s1"}, loopTurnChanged)
	// A confirmed new turn permits a candidate with the same tool again.
	next := shifted
	next.Since = next.Since.Add(time.Minute)
	next.Count = 4
	c.ToolLoop = &next
	s.work.mu.Lock()
	s.work.loops[[2]string{"box", "s1"}].checks = toolLoopMaxChecks
	s.work.mu.Unlock()
	s.considerToolLoop(context.Background(), "box", c)
	s.work.mu.Lock()
	st := s.work.loops[[2]string{"box", "s1"}]
	newCandidate := st != nil && !st.silenced && st.since.Equal(next.Since)
	s.work.mu.Unlock()
	if !newCandidate {
		t.Fatal("new turn did not permit a same-tool candidate")
	}
}

func TestToolLoopContinueWriteFailureIsRetryable(t *testing.T) {
	s, f, store, _, card := persistentLoopFixture(t)
	store.mu.Lock()
	store.failPut = errors.New("write unavailable")
	store.mu.Unlock()
	answer := proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[1].Label}}
	if _, err := s.toolLoopAnswer(context.Background(), card.ID, answer); err == nil {
		t.Fatal("write failure acknowledged")
	}
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card.ID {
		t.Fatalf("card lost on failure: %+v", cards)
	}
	store.mu.Lock()
	store.failPut = nil
	store.mu.Unlock()
	answerCard(t, s, card.ID, answer)
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.workCards()) != 0 {
		t.Fatal("accepted answer returned after restart")
	}
}

func TestToolLoopInterruptWriteFailureDoesNotSend(t *testing.T) {
	s, f, store, _, card := persistentLoopFixture(t)
	store.mu.Lock()
	store.failPut = errors.New("write unavailable")
	store.mu.Unlock()
	answer := proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[0].Label}}
	if _, err := s.toolLoopAnswer(context.Background(), card.ID, answer); err == nil {
		t.Fatal("write failure acknowledged")
	}
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card.ID {
		t.Fatalf("card lost on failure: %+v", cards)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == "session.interrupt" {
			t.Fatal("sent interrupt before recording decision")
		}
	}
}

func TestToolLoopDisabledStateDoesNotReturn(t *testing.T) {
	s, f, store, _, _ := persistentLoopFixture(t)
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.workCards()) != 0 || len(s.work.loops) != 0 {
		t.Fatal("disabled loop returned after restart")
	}
}

type blockingLoopStore struct {
	*workMemStore
	block            atomic.Bool
	entered, release chan struct{}
}

func (s *blockingLoopStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == toolLoopsSetting && s.block.CompareAndSwap(true, false) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.workMemStore.PutSetting(ctx, key, value)
}

func TestToolLoopInputDuringDecisionSavePreventsInterrupt(t *testing.T) {
	_, f, original, _, card := persistentLoopFixture(t)
	store := &blockingLoopStore{workMemStore: original, entered: make(chan struct{}), release: make(chan struct{})}
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.block.Store(true)
	done := make(chan error, 1)
	go func() {
		_, err := s.toolLoopAnswer(context.Background(), card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[0].Label}})
		done <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("save did not start")
	}
	cleared := make(chan struct{})
	go func() { s.clearToolLoop([2]string{"box", "s1"}, loopUserInput); close(cleared) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s.work.mu.Lock()
		st := s.work.loops[[2]string{"box", "s1"}]
		invalid := st != nil && st.silenced && !st.answering
		s.work.mu.Unlock()
		if invalid {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			close(store.release)
			t.Fatal("input did not invalidate the card")
		}
	}
	close(store.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("answer did not finish")
	}
	select {
	case <-cleared:
	case <-time.After(5 * time.Second):
		t.Fatal("input did not finish")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == "session.interrupt" {
			t.Fatal("interrupted after user input")
		}
	}
}

func TestToolLoopConcurrentSessionDecisionsSurviveRestart(t *testing.T) {
	s, f, store, first, card1 := persistentLoopFixture(t)
	second := first
	second.SessionID = "s2"
	second.ToolLoop = &proto.ToolLoop{Tool: "Write", Count: 30, Since: first.ToolLoop.Since.Add(time.Second)}
	f.setSessions("box", first, second)
	st := &toolLoopState{tool: second.ToolLoop.Tool, since: second.ToolLoop.Since, pending: true}
	s.work.mu.Lock()
	s.work.loops[[2]string{"box", "s2"}] = st
	s.work.mu.Unlock()
	s.reviewToolLoop(context.Background(), [2]string{"box", "s2"}, st, *second.ToolLoop, true)
	var card2 proto.Interaction
	for _, card := range s.workCards() {
		if card.SessionID == "s2" {
			card2 = card
		}
	}
	if card2.ID == "" {
		t.Fatal("second card missing")
	}
	done := make(chan error, 2)
	for _, card := range []proto.Interaction{card1, card2} {
		go func(card proto.Interaction) {
			_, err := s.toolLoopAnswer(context.Background(), card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[1].Label}})
			done <- err
		}(card)
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("answers did not complete")
		}
	}
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.work.mu.Lock()
	one, two := s.work.loops[[2]string{"box", "s1"}], s.work.loops[[2]string{"box", "s2"}]
	quiet := one != nil && one.silenced && two != nil && two.silenced
	s.work.mu.Unlock()
	if !quiet || len(s.workCards()) != 0 {
		t.Fatal("one of the concurrent answers was lost")
	}
}

func TestToolLoopOtherSaveDoesNotCommitPendingAnswer(t *testing.T) {
	s, f, store, first, _ := persistentLoopFixture(t)
	second := first
	second.SessionID = "s2"
	second.ToolLoop = &proto.ToolLoop{Tool: "Write", Count: 30, Since: first.ToolLoop.Since.Add(time.Second)}
	f.setSessions("box", first, second)
	st := &toolLoopState{tool: second.ToolLoop.Tool, since: second.ToolLoop.Since, pending: true}
	s.work.mu.Lock()
	s.work.loops[[2]string{"box", "s2"}] = st
	s.work.mu.Unlock()
	s.reviewToolLoop(context.Background(), [2]string{"box", "s2"}, st, *second.ToolLoop, true)
	id := st.cardID
	s.work.mu.Lock()
	st.answering, st.consuming, st.silenced = true, true, true
	s.work.mu.Unlock()
	// An unrelated session's successful write must leave this answer uncommitted.
	if err := s.saveToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.failPut = errors.New("write unavailable")
	store.mu.Unlock()
	if err := s.saveToolLoops(context.Background(), [2]string{"box", "s2"}); err == nil {
		t.Fatal("expected failed answer write")
	}
	store.mu.Lock()
	store.failPut = nil
	store.mu.Unlock()
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, card := range s.workCards() {
		found = found || card.ID == id
	}
	if !found {
		t.Fatal("another write committed an answer whose own write failed")
	}
}

func TestToolLoopModelBudgetSurvivesRestart(t *testing.T) {
	f := newFakeHub()
	store := &workMemStore{}
	c := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 20, Since: time.Now().Add(-time.Minute).UTC()}}
	f.setSessions("box", c)
	s := newServiceOn(t, f, store)
	s.work.mu.Lock()
	s.work.init()
	s.work.loops[[2]string{"box", "s1"}] = &toolLoopState{tool: c.ToolLoop.Tool, since: c.ToolLoop.Since, checks: toolLoopMaxChecks, checkedCount: 10}
	s.work.mu.Unlock()
	if err := s.saveToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	s = newServiceOn(t, f, store)
	s.considerToolLoop(context.Background(), "box", c)
	f.mu.Lock()
	calls := f.judged
	f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("model budget reset: %d calls", calls)
	}
	s.work.mu.Lock()
	st := s.work.loops[[2]string{"box", "s1"}]
	ok := st != nil && st.checks == toolLoopMaxChecks && !st.pending
	s.work.mu.Unlock()
	if !ok {
		t.Fatal("model budget was not restored")
	}
}

func TestToolLoopInputWhileReadUnavailable(t *testing.T) {
	_, f, store, _, card := persistentLoopFixture(t)
	store.mu.Lock()
	store.failKeys = map[string]error{toolLoopsSetting: errors.New("read unavailable")}
	store.mu.Unlock()
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	s.clearToolLoop([2]string{"box", "s1"}, loopUserInput)
	store.mu.Lock()
	delete(store.failKeys, toolLoopsSetting)
	store.mu.Unlock()
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.workCards()) != 0 {
		t.Fatal("old card returned after user input")
	}
	if _, err := s.toolLoopAnswer(context.Background(), card.ID, proto.Resolution{}); err == nil {
		t.Fatal("old card was accepted")
	}
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.work.mu.Lock()
	st := s.work.loops[[2]string{"box", "s1"}]
	quiet := st != nil && st.silenced
	s.work.mu.Unlock()
	if !quiet {
		t.Fatal("invalidation was not saved")
	}
}

func TestToolLoopConsumedInterruptIsNotReplayed(t *testing.T) {
	_, f, store, c, card := persistentLoopFixture(t)
	arrived, release := make(chan struct{}), make(chan struct{})
	f.nodes = map[string]nodeAnswer{"session.interrupt": func(string, json.RawMessage) (any, error) {
		close(arrived)
		<-release
		return map[string]any{}, nil
	}}
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	done := make(chan error, 1)
	go func() {
		_, err := s.toolLoopAnswer(context.Background(), card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[0].Label}})
		done <- err
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt was not sent")
	}
	f2 := newFakeHub()
	f2.setSessions("box", c)
	s2 := newServiceOn(t, f2, store)
	if err := s2.loadToolLoops(context.Background()); err != nil {
		close(release)
		t.Fatal(err)
	}
	if len(s2.workCards()) != 0 {
		close(release)
		t.Fatal("consumed card restored")
	}
	if _, err := s2.toolLoopAnswer(context.Background(), card.ID, proto.Resolution{}); err == nil {
		close(release)
		t.Fatal("old answer was accepted")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt did not finish")
	}
}

func TestToolLoopDoesNotOverwriteSessionWork(t *testing.T) {
	s, _, store, _, card := persistentLoopFixture(t)
	wake := time.Now().Add(time.Hour)
	original := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: time.Now(), WakeAt: &wake, InteractionID: "work_other", Retries: 2}
	if err := store.PutSessionWork(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[1].Label}})
	rows, err := store.SessionWorks(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 || rows[0].InteractionID != original.InteractionID || rows[0].WakeAt == nil || !rows[0].WakeAt.Equal(wake) || rows[0].Retries != original.Retries {
		t.Fatalf("session work changed: %+v, %v", rows, err)
	}
}
