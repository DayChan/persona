package assistant

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type loopPhoneStore struct {
	*fleetMem
	failLoop atomic.Bool
	reopened atomic.Int32
}

func (s *loopPhoneStore) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	status, _, err := s.fleetMem.InteractionOutcome(ctx, ix.ID)
	if err != nil {
		return err
	}
	if status != "" && status != "pending" {
		s.reopened.Add(1)
	}
	return s.fleetMem.InsertInteraction(ctx, ix)
}

func (s *loopPhoneStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == toolLoopsSetting && s.failLoop.Load() {
		return errors.New("loop settings unavailable")
	}
	return s.fleetMem.PutSetting(ctx, key, value)
}

func waitLoopFlow(t *testing.T, what string, ready func() bool) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for !ready() {
		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatalf("%s did not complete", what)
		}
	}
}

func loopInterruptCalls(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := 0
	for _, call := range f.calls {
		if call.Method == "session.interrupt" {
			calls++
		}
	}
	return calls
}

func TestToolLoopPhoneAnswerRetriesAfterWriteFailureAndRestart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		option  int
		restart bool
	}{
		{"continue held", 1, false},
		{"continue restart", 1, true},
		{"interrupt held", 0, false},
		{"interrupt restart", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			store := &loopPhoneStore{fleetMem: ft.st}
			ft.s.store = func() Store { return store }
			ft.s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
			c := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 30, Since: time.Now().Add(-time.Minute).UTC()}}
			ft.d.setSessions("mac", []proto.Session{c})
			ft.f.setSessions("mac", c)
			ft.s.setSessions("mac", []proto.Session{c})
			waitFleetOrder(t, ft.s)
			// The real fleet snapshot comes from the node's session summary;
			// the manager tick, not this test, schedules the ceiling review.
			ft.s.checkToolLoops(context.Background())
			waitLoopFlow(t, "phone card", func() bool { return len(ft.s.pendingInteractions()) == 1 })
			cards := ft.s.pendingInteractions()
			card := cards[0]
			if card.ToolName != toolLoopCardTool || len(card.Questions) != 1 || len(card.Questions[0].Options) != 2 {
				t.Fatalf("unexpected card: %+v", card)
			}
			store.failLoop.Store(true)
			answer := proto.Resolution{Answers: map[string]string{card.Questions[0].Question: card.Questions[0].Options[tc.option].Label}}
			resolveWorkQuestionHTTP(t, ft.s, card.ID, answer)
			waitLoopFlow(t, "held answer", func() bool { return ft.s.answerKept(card.ID) })
			status, kept, err := store.InteractionOutcome(context.Background(), card.ID)
			if err != nil || status != "resolved" || kept == nil || kept.Answers[card.Questions[0].Question] != card.Questions[0].Options[tc.option].Label {
				t.Fatalf("answer was not durably recorded: %s %+v %v", status, kept, err)
			}
			if len(ft.s.pendingInteractions()) != 0 {
				t.Fatal("answered card returned to the phone")
			}
			if loopInterruptCalls(ft.f) != 0 {
				t.Fatal("interrupted before the decision was saved")
			}
			ft.s.askOnPhone(nil)
			store.failLoop.Store(false)
			if tc.restart {
				s := New(Options{Hub: ft.f, Store: func() Store { return store }, Logf: testLogf(t), Devices: ft.d})
				s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
				ft.s = s
				s.setSessions("mac", []proto.Session{c})
				waitFleetOrder(t, s)
				s.publish(context.Background())
			} else {
				ft.s.fleet.undelivered.mu.Lock()
				held := ft.s.fleet.undelivered.acts[card.ID]
				held.next = time.Time{}
				ft.s.fleet.undelivered.acts[card.ID] = held
				ft.s.fleet.undelivered.mu.Unlock()
				ft.s.deliverAnswers()
			}
			waitLoopFlow(t, "owner decision", func() bool { return len(ft.s.workCards()) == 0 })
			if len(ft.s.pendingInteractions()) != 0 || store.reopened.Load() != 0 {
				t.Fatal("replay asked the user again")
			}
			want := 0
			if tc.option == 0 {
				want = 1
			}
			if got := loopInterruptCalls(ft.f); got != want {
				t.Fatalf("interrupt calls = %d, want %d", got, want)
			}
			ft.s.askOnPhone(nil)
		})
	}
}
