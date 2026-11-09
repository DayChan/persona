package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func toolLoopFixture(t *testing.T, response string) (*Service, *fakeHub, proto.Session, *toolLoopState) {
	t.Helper()
	s, f := workService(t)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	f.judge = response
	c := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 4, Since: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)}}
	f.setSessions("box", c)
	st := &toolLoopState{tool: c.ToolLoop.Tool, since: c.ToolLoop.Since, pending: true, checks: 1, checkedCount: 4}
	s.work.mu.Lock()
	s.work.init()
	s.work.loops[[2]string{"box", "s1"}] = st
	s.work.mu.Unlock()
	return s, f, c, st
}

func TestToolLoopModelAndActions(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		cancel, hard   bool
		wantCard       bool
	}{
		{"stop", `{"decision":"STOP"}`, false, false, true},
		{"continue", `{"decision":"CONTINUE"}`, false, false, false},
		{"invalid", `{"decision":"stop immediately"}`, false, false, false},
		{"timeout", `{"decision":"STOP"}`, true, false, false},
		{"ceiling", "", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, c, st := toolLoopFixture(t, tc.response)
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			s.reviewToolLoop(ctx, [2]string{"box", "s1"}, st, *c.ToolLoop, tc.hard)
			cards := s.workCards()
			if (len(cards) == 1) != tc.wantCard {
				t.Fatalf("cards: %#v", cards)
			}
			if !tc.wantCard {
				return
			}
			ix := cards[0]
			if ix.ToolName != toolLoopCardTool || len(ix.Questions) != 1 || len(ix.Questions[0].Options) != 2 {
				t.Fatalf("card: %#v", ix)
			}
			answerCard(t, s, ix.ID, proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: ix.Questions[0].Options[1].Label}})
			if len(s.workCards()) != 0 || !st.silenced || len(f.sentToAgents()) != 0 {
				t.Fatal("letting it run must clear and silence without sending a message")
			}
		})
	}
}

func TestToolLoopPostsOnce(t *testing.T) {
	s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
	k := [2]string{"box", "s1"}
	s.reviewToolLoop(context.Background(), k, st, *c.ToolLoop, false)
	id := s.workCards()[0].ID
	c.ToolLoop.Count = 30
	s.considerToolLoop(context.Background(), "box", c)
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != id {
		t.Fatalf("repeat opened another card: %#v", cards)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.judged != 1 {
		t.Fatalf("model calls = %d", f.judged)
	}
}

func TestToolLoopChangedDuringModelReview(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"decision":"STOP"}`}}})
	}))
	defer srv.Close()
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	f := newFakeHub()
	f.ownModels = true
	f.hello.Models.AI = ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "fake"}
	f.sessions["box"] = nil
	s := newTestService(t, f)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	old := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 4, Since: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)}}
	f.setSessions("box", old)
	st := &toolLoopState{tool: old.ToolLoop.Tool, since: old.ToolLoop.Since, pending: true, checks: 1}
	s.work.mu.Lock()
	s.work.init()
	s.work.loops[[2]string{"box", "s1"}] = st
	s.work.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *old.ToolLoop, false)
		close(done)
	}()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("model was not called")
	}
	changed := old
	changed.ToolLoop = &proto.ToolLoop{Tool: "Write", Count: 4, Since: old.ToolLoop.Since.Add(time.Second)}
	// The node event invalidates the result even if a concurrent session read is stale.
	s.SessionsChanged(context.Background(), DeviceSessions{MachineID: "box", Sessions: []proto.Session{changed}})
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("model result did not return")
	}
	if len(s.workCards()) != 0 {
		t.Fatal("stale model result opened a card")
	}
}

func TestToolLoopContinueSilencesCeiling(t *testing.T) {
	s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
	s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
	ix := s.workCards()[0]
	answerCard(t, s, ix.ID, proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: ix.Questions[0].Options[1].Label}})
	c.ToolLoop.Count = 30
	f.setSessions("box", c)
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{c}})
	s.considerToolLoop(context.Background(), "box", c)
	if len(s.workCards()) != 0 || !st.silenced || st.pending {
		t.Fatal("ceiling reopened a silenced loop")
	}
}

func TestToolLoopStaleAnswerCannotInterrupt(t *testing.T) {
	s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
	s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
	ix := s.workCards()[0]
	c.ToolLoop = &proto.ToolLoop{Tool: "Read", Count: 4, Since: c.ToolLoop.Since.Add(time.Minute)}
	f.setSessions("box", c)
	answerCard(t, s, ix.ID, proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: ix.Questions[0].Options[0].Label}})
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == "session.interrupt" {
			t.Fatal("interrupted a different loop")
		}
	}
}

func TestToolLoopUserMessageWithdraws(t *testing.T) {
	s, _, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
	s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
	ix := s.workCards()[0]
	s.UserSpoke(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"})
	if len(s.workCards()) != 0 {
		t.Fatal("card remains after user message")
	}
	if _, err := s.toolLoopAnswer(context.Background(), ix.ID, proto.Resolution{}); err == nil {
		t.Fatal("stale answer accepted")
	}
}

func TestToolLoopOldSnapshotAfterUserInput(t *testing.T) {
	for _, input := range []string{"message", "queued"} {
		t.Run(input, func(t *testing.T) {
			s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
			c.ToolLoop.Count = toolLoopCeiling
			st.checks = toolLoopMaxChecks
			s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, true)
			if len(s.workCards()) != 1 {
				t.Fatal("initial loop card missing")
			}
			if input == "message" {
				s.UserSpoke(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"})
			} else {
				queued := c
				queued.Queued = []string{"new instruction"}
				s.SessionsChanged(context.Background(), DeviceSessions{MachineID: "box", Sessions: []proto.Session{queued}})
			}
			// A delayed session read can still contain the candidate from before the user's input.
			s.considerToolLoop(context.Background(), "box", c)
			s.work.mu.Lock()
			current := s.work.loops[[2]string{"box", "s1"}]
			unsafe := current == nil || !current.silenced || current.pending || current.cardID != ""
			s.work.mu.Unlock()
			if unsafe {
				t.Fatal("old snapshot reopened the loop after user input")
			}
			cleared := c
			cleared.ToolLoop = nil
			s.SessionsChanged(context.Background(), DeviceSessions{MachineID: "box", Sessions: []proto.Session{cleared}})
			s.considerToolLoop(context.Background(), "box", c)
			s.work.mu.Lock()
			current = s.work.loops[[2]string{"box", "s1"}]
			unsafe = current == nil || !current.silenced || current.pending || current.cardID != ""
			s.work.mu.Unlock()
			if unsafe {
				t.Fatal("old snapshot reopened after the candidate disappeared")
			}
			next := c
			candidate := *c.ToolLoop
			candidate.Since = candidate.Since.Add(time.Minute)
			candidate.Count = 0 // update identity without starting another asynchronous review
			next.ToolLoop = &candidate
			f.setSessions("box", next)
			s.considerToolLoop(context.Background(), "box", next)
			s.work.mu.Lock()
			current = s.work.loops[[2]string{"box", "s1"}]
			newLoop := current != nil && current.since.Equal(candidate.Since) && !current.silenced && current.checks == toolLoopMaxChecks
			s.work.mu.Unlock()
			if !newLoop {
				t.Fatal("a later loop was silenced or lost its turn's model budget")
			}
			s.clearToolLoop([2]string{"box", "s1"}, loopTurnChanged)
			s.work.mu.Lock()
			current = s.work.loops[[2]string{"box", "s1"}]
			reset := current != nil && current.checks == 0
			s.work.mu.Unlock()
			if !reset {
				t.Fatal("new turn did not reset the model budget")
			}
		})
	}
}

func TestToolLoopInterrupt(t *testing.T) {
	s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
	s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
	ix := s.workCards()[0]
	answerCard(t, s, ix.ID, proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: ix.Questions[0].Options[0].Label}})
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call.Method == "session.interrupt" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("interrupt calls = %d", n)
	}
}

func TestToolLoopInterruptFailureRestoresCard(t *testing.T) {
	s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
	s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
	ix := s.workCards()[0]
	r := proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: ix.Questions[0].Options[0].Label}}
	f.mu.Lock()
	f.failInterrupt = errors.New("node unavailable")
	f.mu.Unlock()
	if _, err := s.toolLoopAnswer(context.Background(), ix.ID, r); err != nil {
		t.Fatal(err)
	}
	cards := s.workCards()
	if len(cards) != 1 || cards[0].ID == ix.ID {
		t.Fatalf("failed interrupt did not restore a new card: %#v", cards)
	}
	if _, err := s.toolLoopAnswer(context.Background(), ix.ID, r); err == nil {
		t.Fatal("the old answer could be retried automatically")
	}
	f.mu.Lock()
	f.failInterrupt = nil
	f.mu.Unlock()
	if _, err := s.toolLoopAnswer(context.Background(), cards[0].ID, r); err != nil {
		t.Fatal(err)
	}
	if len(s.workCards()) != 0 {
		t.Fatal("successful retry left the card up")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call.Method == "session.interrupt" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("interrupt calls = %d", n)
	}
}

type blockingInterruptHub struct {
	Hub
	started, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
}

func (h *blockingInterruptHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == "session.interrupt" {
		h.calls.Add(1)
		h.once.Do(func() { close(h.started) })
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.Hub.DeviceCall(ctx, p)
}

func TestToolLoopConcurrentAnswersInterruptOnce(t *testing.T) {
	s, _, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
	s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
	ix := s.workCards()[0]
	r := proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: ix.Questions[0].Options[0].Label}}
	h := &blockingInterruptHub{Hub: s.hub, started: make(chan struct{}), release: make(chan struct{})}
	s.hub = h
	done := make(chan error, 1)
	go func() { _, err := s.toolLoopAnswer(context.Background(), ix.ID, r); done <- err }()
	select {
	case <-h.started:
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt did not start")
	}
	if _, err := s.toolLoopAnswer(context.Background(), ix.ID, r); err == nil {
		t.Fatal("overlapping answer was accepted while interrupt was in flight")
	}
	close(h.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt did not finish")
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("interrupt calls = %d", n)
	}
}

func TestToolLoopQueuedInputInvalidates(t *testing.T) {
	t.Run("event withdraws card", func(t *testing.T) {
		s, _, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
		s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
		ix := s.workCards()[0]
		c.Queued = []string{"new user message"}
		s.SessionsChanged(context.Background(), DeviceSessions{MachineID: "box", Sessions: []proto.Session{c}})
		if len(s.workCards()) != 0 {
			t.Fatal("queued input left card visible")
		}
		if _, err := s.toolLoopAnswer(context.Background(), ix.ID, proto.Resolution{}); err == nil {
			t.Fatal("stale answer accepted")
		}
	})
	t.Run("fresh read rejects interrupt", func(t *testing.T) {
		s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
		s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
		ix := s.workCards()[0]
		c.Queued = []string{"new user message"}
		f.setSessions("box", c)
		answerCard(t, s, ix.ID, proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: ix.Questions[0].Options[0].Label}})
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, call := range f.calls {
			if call.Method == "session.interrupt" {
				t.Fatal("interrupted despite queued input")
			}
		}
	})
	t.Run("model stop rejected", func(t *testing.T) {
		s, f, c, st := toolLoopFixture(t, `{"decision":"STOP"}`)
		c.Queued = []string{"new user message"}
		f.setSessions("box", c)
		s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, false)
		if len(s.workCards()) != 0 {
			t.Fatal("opened card despite queued input")
		}
	})
}

func TestToolLoopSettingOldClient(t *testing.T) {
	on := true
	if switchesOf(assistantwire.TriageConfig{}).ToolLoop || !switchesOf(assistantwire.TriageConfig{ToolLoop: &on}).ToolLoop {
		t.Fatal("initial tool_loop setting not applied")
	}
	s, _ := workService(t)
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true, ToolLoop: true}); err != nil {
		t.Fatal(err)
	}
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"decide":true}`); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if !s.triageSwitches(context.Background()).ToolLoop {
		t.Fatal("old client reset tool_loop")
	}
	if w := serve(s, "PUT", "/api/v1/agents/triage", `{"enabled":true,"tool_loop":false}`); w.Code != 200 || s.triageSwitches(context.Background()).ToolLoop {
		t.Fatal("explicitly disabling tool_loop failed")
	}
}

func TestGoldenToolLoop(t *testing.T) {
	golden(t, "agent_tool_loop.system", prompts.RenderJSONOnly(prompts.RenderAgentToolLoop()))
}
