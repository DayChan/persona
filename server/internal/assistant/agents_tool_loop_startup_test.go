package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func waitFleetOrder(t *testing.T, s *Service) {
	t.Helper()
	done := make(chan struct{})
	s.inOrder(func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fleet event queue did not drain")
	}
}

func TestToolLoopWorkingAfterOfflineAndDone(t *testing.T) {
	for _, tc := range []struct {
		previous string
		keep     bool
	}{{proto.StatusOffline, true}, {proto.StatusDone, false}, {proto.StatusInterrupted, false}} {
		t.Run(tc.previous, func(t *testing.T) {
			ft := newFleetTest(t)
			defer ft.s.askOnPhone(nil)
			now := time.Now().UTC()
			c := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 30, Since: now.Add(-time.Minute)}}
			card := proto.Interaction{ID: "tool_loop_previous", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
				ToolName: toolLoopCardTool, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				Questions: []proto.Question{{Question: "Loop?", Options: []proto.QuestionOption{{Label: "Interrupt"}, {Label: "Let it run"}}}}}
			row := savedToolLoop{Machine: "mac", Session: "s1", Since: c.ToolLoop.Since, Tool: c.ToolLoop.Tool, Checks: 2, Card: &card}
			if err := ft.st.PutSetting(context.Background(), toolLoopsSetting, []savedToolLoop{row}); err != nil {
				t.Fatal(err)
			}
			ft.s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
			ft.s.fleetKeepPrevious("mac", []proto.Session{{SessionID: "s1", Status: tc.previous}})
			ft.d.setSessions("mac", []proto.Session{c})
			ft.s.setSessions("mac", []proto.Session{c})
			waitFleetOrder(t, ft.s)
			cards := ft.s.workCards()
			if (len(cards) == 1 && cards[0].ID == card.ID) != tc.keep {
				t.Fatalf("previous %s: cards %+v", tc.previous, cards)
			}
		})
	}
}

func TestToolLoopFirstWorkingSessionListPreservesRecovery(t *testing.T) {
	for _, silenced := range []bool{false, true} {
		t.Run(map[bool]string{false: "card", true: "silenced"}[silenced], func(t *testing.T) {
			ft := newFleetTest(t)
			defer ft.s.askOnPhone(nil)
			now := time.Now().UTC()
			c := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 30, Since: now.Add(-time.Minute)}}
			card := proto.Interaction{ID: "tool_loop_first", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
				ToolName: toolLoopCardTool, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				Questions: []proto.Question{{Question: "Loop?", Options: []proto.QuestionOption{{Label: "Interrupt"}, {Label: "Let it run"}}}}}
			row := savedToolLoop{Machine: "mac", Session: "s1", Since: c.ToolLoop.Since, Tool: c.ToolLoop.Tool, Checks: 3, CheckedCount: 10, Silenced: silenced}
			if !silenced {
				row.Card = &card
			}
			if err := ft.st.PutSetting(context.Background(), toolLoopsSetting, []savedToolLoop{row}); err != nil {
				t.Fatal(err)
			}
			ft.s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
			ft.d.setSessions("mac", []proto.Session{c})
			ft.s.setSessions("mac", []proto.Session{c})
			waitFleetOrder(t, ft.s)
			ft.s.work.mu.Lock()
			st := ft.s.work.loops[[2]string{"mac", "s1"}]
			ok := st != nil && st.checks == 3 && st.checkedCount == 10 && st.silenced == silenced
			if !silenced {
				ok = ok && st.cardID == card.ID
			}
			ft.s.work.mu.Unlock()
			if !ok {
				t.Fatalf("first session list changed restored state: %+v", st)
			}
		})
	}
}
