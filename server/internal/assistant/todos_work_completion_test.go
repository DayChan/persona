package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func completedTodoTurns() []struct {
	name    string
	session proto.Session
	reply   string
	on      string
} {
	return []struct {
		name    string
		session proto.Session
		reply   string
		on      string
	}{
		{"transient", apiErrorTurn("s1", "API Error: 529 overloaded"), "", proto.WaitTransient},
		{"usage limit", limitTurn("s1", time.Now().Add(time.Hour), false), "", proto.WaitExternal},
		{"external wait", stopped("s1", "Waiting for CI to finish."),
			`{"phase":"blocked","on":"external","line":"Waiting for CI","wait":{"what":"CI completes","then":"Run tests","check_minutes":2}}`, proto.WaitExternal},
		{"agent wait", stopped("s1", "Waiting for the other agent to finish."),
			`{"phase":"blocked","on":"agent","line":"Waiting for the other agent","wait":{"what":"Other agent finishes","then":"Run tests","check_minutes":2,"agent":"abcdef12"}}`, proto.WaitAgent},
	}
}

// Completion during a running turn and completion restored without a retirement
// marker both prevent every kind of automatic continuation after that turn.
func TestCompletedTodoStopsLaterTriage(t *testing.T) {
	ctx := context.Background()
	for _, tc := range completedTodoTurns() {
		for _, completion := range []string{"mid-turn", "restored"} {
			t.Run(tc.name+"/"+completion, func(t *testing.T) {
				s, f := workService(t)
				f.triage = tc.reply
				done := time.Now().Add(-time.Minute)
				todo := Todo{ID: "todo_done", Title: "Run tests", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}}
				if completion == "mid-turn" {
					putTodo(s, todo)
					working := tc.session
					working.Status = proto.StatusWorking
					f.setSessions("box", working)
					if _, ok := s.applyTodoRow(ctx, todo.ID, db.TodoRow{ID: todo.ID, Title: todo.Title, DoneAt: &done, UpdatedAt: done}, nil); !ok {
						t.Fatal("could not complete the todo")
					}
				} else {
					// The todo committed, but its separate work-state write was lost.
					todo.DoneAt = &done
					putTodo(s, todo)
				}
				other := proto.Session{SessionID: "abcdef123456", Title: "Other work", Status: proto.StatusWorking, UpdatedAt: turnEnd}
				f.setSessions("box", tc.session, other)
				s.workTurnSettled(ctx, "box", "s1")
				e := entryOf(t, s, "s1")
				if e.WakeAt != nil || e.Work.WakeAt != nil || e.InteractionID != "" || e.Work.CI != nil {
					t.Fatalf("completed todo scheduled follow-ups: %+v", e)
				}
				if completion == "restored" && (e.Work.Phase != proto.WorkBlocked || e.Work.On != tc.on) {
					t.Fatalf("turn's work state was not recorded: %+v", e.Work)
				}
				other.Status = proto.StatusDone
				f.setSessions("box", tc.session, other)
				s.wakeWaitersOn("box", other.SessionID)
				s.workTick(ctx, time.Now().Add(2*time.Hour))
				if got := f.sentToAgents(); len(got) != 0 {
					t.Fatalf("completed todo woke its agent: %v", got)
				}
			})
		}
	}
}

// A wake restored after completion must recheck the todo immediately before
// sending, even when its retirement marker was never saved.
func TestCompletedTodoStopsPendingWake(t *testing.T) {
	ctx := context.Background()
	for _, tc := range completedTodoTurns() {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workService(t)
			f.triage = tc.reply
			f.setSessions("box", tc.session, proto.Session{SessionID: "abcdef123456", Title: "Other work", Status: proto.StatusWorking})
			triage(t, s, "s1")
			before := entryOf(t, s, "s1")
			if before.WakeAt == nil {
				t.Fatalf("open session did not schedule a wake: %+v", before)
			}
			done := time.Now()
			putTodo(s, Todo{ID: "todo_done", Title: "Run tests", DoneAt: &done,
				Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
			if _, why := s.workGuard(s.newWorkView(ctx), before, false); why != "task done" {
				t.Fatalf("guard reason = %q, want task done", why)
			}
			s.workWake(ctx, s.newWorkView(ctx), [2]string{"box", "s1"}, before.WakeAt.Add(time.Minute))
			if e := entryOf(t, s, "s1"); e.WakeAt != nil || e.Work.WakeAt != nil || len(f.sentToAgents()) != 0 {
				t.Fatalf("completed todo's stale wake survived: %+v, sent %v", e, f.sentToAgents())
			}
		})
	}
}

func TestCompletedTodoOnlyStopsStillLinkedAgents(t *testing.T) {
	ctx := context.Background()
	done := time.Now().Add(-time.Minute)
	link := TodoAgent{MachineID: "box", SessionID: "s1"}
	closed := Todo{ID: "todo_done", Title: "Finished work", DoneAt: &done, Agents: []TodoAgent{link}}
	open := Todo{ID: "todo_open", Title: "Current work", Agents: []TodoAgent{link}}
	released, dismissed, otherMachine, otherSession := closed, closed, closed, closed
	released.Agents = []TodoAgent{{MachineID: "box", SessionID: "s1", ReleasedAt: &done}}
	dismissed.Agents = []TodoAgent{{MachineID: "box", SessionID: "s1", DismissedAt: &done}}
	otherMachine.Agents = []TodoAgent{{MachineID: "other", SessionID: "s1"}}
	otherSession.Agents = []TodoAgent{{MachineID: "box", SessionID: "s2"}}
	for _, tc := range []struct {
		name  string
		todos []Todo
	}{
		{"no todo", nil},
		{"open todo", []Todo{open}},
		{"open todo wins", []Todo{closed, open}},
		{"released", []Todo{released}},
		{"dismissed", []Todo{dismissed}},
		{"other machine", []Todo{otherMachine}},
		{"other session", []Todo{otherSession}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workService(t)
			for _, todo := range tc.todos {
				putTodo(s, todo)
			}
			f.setSessions("box", apiErrorTurn("s1", "API Error: 529 overloaded"))
			triage(t, s, "s1")
			e := entryOf(t, s, "s1")
			if e.WakeAt == nil {
				t.Fatalf("unrelated completed todo blocked continuation: %+v", e)
			}
			s.workWake(ctx, s.newWorkView(ctx), [2]string{"box", "s1"}, e.WakeAt.Add(time.Second))
			if got := f.sentToAgents(); len(got) != 1 || got[0] != "s1: "+continueText {
				t.Fatalf("continuation = %v", got)
			}
		})
	}
}

// Manual input still reaches a completed task's agent. Only a task the hub judged
// done or its supervisor accepted reopens, allowing subsequent triage.
func TestCompletedTodoManualTurnPreservesReopeningRules(t *testing.T) {
	ctx := context.Background()
	for _, doneBy := range []string{"", db.DoneByAuto, db.DoneByReview, db.DoneByCancelled} {
		name := doneBy
		if name == "" {
			name = "user"
		}
		t.Run(name, func(t *testing.T) {
			s, f := workService(t)
			done := time.Now().Add(-time.Minute)
			putTodo(s, Todo{ID: "todo_done", Title: "Run tests", Origin: "conversation", DoneAt: &done, DoneBy: doneBy,
				Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
			f.setSessions("box", stopped("s1", "Finished."))
			if err := s.sendToSession(ctx, "box", "s1", "Check one more case."); err != nil {
				t.Fatal(err)
			}
			s.taskWorking(ctx, "box", "s1")
			s.workStarted(ctx, "box", "s1", time.Now())
			wantReopen := doneBy == db.DoneByAuto || doneBy == db.DoneByReview
			if reopened := todoNow(s, "todo_done").DoneAt == nil; reopened != wantReopen {
				t.Fatalf("reopened = %v for done_by %q", reopened, doneBy)
			}
			c := apiErrorTurn("s1", "API Error: 529 overloaded")
			c.UpdatedAt = time.Now()
			f.setSessions("box", c)
			triage(t, s, "s1")
			e := entryOf(t, s, "s1")
			if scheduled := e.WakeAt != nil; scheduled != wantReopen {
				t.Fatalf("scheduled = %v for done_by %q: %+v", scheduled, doneBy, e)
			}
			if got := f.sentToAgents(); len(got) != 1 || got[0] != "s1: Check one more case." {
				t.Fatalf("manual input = %v", got)
			}
		})
	}
}

func TestCompletedTodoDropsLiveAndRestoredCards(t *testing.T) {
	ctx := context.Background()
	for _, restored := range []bool{false, true} {
		name := "live"
		if restored {
			name = "restored"
		}
		t.Run(name, func(t *testing.T) {
			s, f := workService(t)
			c := stopped("s1", "Which test should I run?")
			c.UpdatedAt = time.Now()
			f.setSessions("box", c)
			f.triage = `{"phase":"blocked","on":"user","line":"Choose a test","questions":[{"question":"Which test?","options":[{"label":"Unit tests"},{"label":"Integration tests"}]}]}`
			triage(t, s, "s1")
			cardID := entryOf(t, s, "s1").InteractionID
			if cardID == "" {
				t.Fatal("triage did not create a card")
			}
			if restored {
				s.work.mu.Lock()
				delete(s.work.cards, cardID)
				s.work.mu.Unlock()
			}
			done := time.Now()
			putTodo(s, Todo{ID: "todo_done", Title: "Run tests", DoneAt: &done,
				Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
			s.cardTick(ctx, s.newWorkView(ctx), [2]string{"box", "s1"}, time.Now())
			assertRetired(t, s, cardID)
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("withdrawing the card sent a message: %v", got)
			}
		})
	}
}

func TestCompletedTodoReadFailureRetriesTriageAndWake(t *testing.T) {
	ctx := context.Background()
	for _, existingWake := range []bool{false, true} {
		name := "triage"
		if existingWake {
			name = "wake"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeHub()
			st := &restoredTodoCIStore{failTodo: true}
			s := newServiceOn(t, f, st)
			s.work.switches = &TriageSwitches{Enabled: true, AutoContinue: true, Wake: true, Decide: true}
			c := apiErrorTurn("ci", "API Error: 529 overloaded")
			f.setSessions("box", c)
			now := time.Now()
			key := [2]string{"box", "ci"}
			if existingWake {
				s.putWork(ctx, db.SessionWork{MachineID: "box", SessionID: "ci", TurnAt: c.UpdatedAt, WakeAt: &now,
					Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, WakeAt: &now}})
				s.workWake(ctx, s.newWorkView(ctx), key, now)
				if e := entryOf(t, s, "ci"); e.WakeAt == nil || !e.WakeAt.After(now) {
					t.Fatalf("failed read did not postpone the wake: %+v", e)
				}
			} else {
				triage(t, s, "ci")
				if e, ok := s.workEntry(key); ok && (e.Work.Phase != "" || e.Work.By != "") {
					t.Fatalf("failed read marked the turn triaged: %+v", e)
				}
			}
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("failed read allowed a continuation: %v", got)
			}
			st.failTodo = false
			if !existingWake {
				triage(t, s, "ci")
			}
			e := entryOf(t, s, "ci")
			if e.WakeAt == nil {
				t.Fatalf("recovered read did not allow a wake: %+v", e)
			}
			s.workWake(ctx, s.newWorkView(ctx), key, e.WakeAt.Add(time.Second))
			if got := f.sentToAgents(); len(got) != 1 || got[0] != "ci: "+continueText {
				t.Fatalf("continuation after recovery = %v", got)
			}
			s.waitWorkEvents()
		})
	}
}

// Older managers could persist a CI watch's proof after a manual turn started
// without reopening its todo. That proof must not bypass the completion guard.
func TestCompletedTodoRejectsPersistedCIProof(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub()
	st := &restoredTodoCIStore{doneAt: time.Now().Add(-time.Minute)}
	c := stopped("ci", "Waiting for CI.")
	f.setSessions("box", c)
	s := newServiceOn(t, f, st)
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, Wake: true}); err != nil {
		t.Fatal(err)
	}
	s.workStarted(ctx, "box", "ci", time.Now())
	closed, proof, err := s.ciClosedTodo(ctx, "box", "ci", "", proto.WorkState{}, time.Time{})
	if err != nil || closed || proof == "" {
		t.Fatalf("could not construct the older watch's proof: closed %v, proof %q, error %v", closed, proof, err)
	}
	at := time.Now().Add(-time.Second)
	s.putWork(ctx, db.SessionWork{MachineID: "box", SessionID: "ci", TurnAt: c.UpdatedAt, WakeAt: &at,
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, WakeAt: &at,
			CI: &proto.WorkCI{Number: 12, Message: "CI finished", TodoProof: proof}}})
	restarted := newServiceOn(t, f, st)
	if !restarted.loadWork(ctx) {
		t.Fatal("could not restore the CI watch")
	}
	sends := 0
	f.answerNode(DeviceSessionSendOnce, func(_ string, _ json.RawMessage) (any, error) {
		sends++
		return map[string]any{}, nil
	})
	restarted.workWake(ctx, restarted.newWorkView(ctx), [2]string{"box", "ci"}, time.Now())
	if e := entryOf(t, restarted, "ci"); e.WakeAt != nil || e.Work.CI != nil || sends != 0 {
		t.Fatalf("persisted proof bypassed completion: %+v, sends %d", e, sends)
	}
	s.waitWorkEvents()
	restarted.waitWorkEvents()
}
