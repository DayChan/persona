package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// undoAutoLink takes back only what the automatic link itself made (#2704).
func TestUndoAutoLink(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	later := t0.Add(time.Second)
	key := [2]string{"m", "s"}
	item := pendingAutoLink{todoID: "big", machineID: "m", sessionID: "s", linkedAt: t0}
	agent := func(at time.Time) TodoAgent {
		return TodoAgent{MachineID: "m", SessionID: "s", LinkedAt: at, Source: "chat"}
	}
	for _, c := range []struct {
		name        string
		big, small  []TodoAgent
		active      string
		wantChanged bool
		wantBig     int
		wantActive  string
	}{
		// The store failed or refused it, nothing happened since: entry and active todo go.
		{name: "failed", big: []TodoAgent{agent(t0)}, active: "big", wantChanged: true, wantBig: 0, wantActive: ""},
		// Linked again by hand to the same todo before the write: that link (another LinkedAt) and its active todo stay.
		{name: "relinked by hand", big: []TodoAgent{{MachineID: "m", SessionID: "s", LinkedAt: later, Source: "manual"}}, active: "big",
			wantChanged: false, wantBig: 1, wantActive: "big"},
		// Moved by hand to another todo: the released entry the store never had goes; the user's todo stays active.
		{name: "moved by hand", big: []TodoAgent{func() TodoAgent { a := agent(t0); a.ReleasedAt = &later; return a }()},
			small: []TodoAgent{{MachineID: "m", SessionID: "s", LinkedAt: later, Source: "manual"}}, active: "small",
			wantChanged: true, wantBig: 0, wantActive: "small"},
		// Dismissed by the user: kept, so it is not linked again (the unlink cleared the active todo).
		{name: "dismissed", big: []TodoAgent{func() TodoAgent { a := agent(t0); a.ReleasedAt, a.DismissedAt = &later, &later; return a }()},
			wantChanged: false, wantBig: 1, wantActive: ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &todoBook{todos: map[string]*Todo{"big": {ID: "big", Agents: c.big}, "small": {ID: "small", Agents: c.small}},
				activeBySession: map[[2]string]string{}}
			if c.active != "" {
				b.activeBySession[key] = c.active
			}
			if got := b.undoAutoLink(item, later); got != c.wantChanged {
				t.Fatalf("changed = %v", got)
			}
			if len(b.todos["big"].Agents) != c.wantBig || b.activeBySession[key] != c.wantActive || len(b.todos["small"].Agents) != len(c.small) {
				t.Fatalf("big %+v, small %+v, active %q", b.todos["big"].Agents, b.todos["small"].Agents, b.activeBySession[key])
			}
		})
	}
}

// slowAutoLink is the database with its automatic links held until release, so a test can act while one is on its
// way to the store.
type slowAutoLink struct {
	*db.DB
	started  chan struct{}
	release  chan struct{}
	finished chan bool // whether the held link was inserted
}

func (s *slowAutoLink) AutoLinkUnlinkedTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error) {
	s.started <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
		return false, nil, ctx.Err()
	}
	inserted, released, err := s.DB.AutoLinkUnlinkedTodoAgent(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, linkedAt)
	s.finished <- inserted
	return inserted, released, err
}

// A manual link submitted while an automatic workspace write is in flight wins
// after both finish, preserving its source and active ownership through reload.
func TestTodoHandLinkDuringAutoLinkToTheSameTodo(t *testing.T) {
	database := todoTestDB(t)
	seedTodos(t, database, db.TodoRow{ID: "td_space", Title: "Space"})
	st := &slowAutoLink{DB: database, started: make(chan struct{}, 1), release: make(chan struct{}), finished: make(chan bool, 1)}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(st.release) })
	e := newTodoEnv(t, st)
	e.f.setSessions("mac")
	e.must("PUT", "/api/v1/todos/td_space/spaces/mac/sp", "")
	e.sessions("mac", proto.Session{SessionID: "s1", SpaceName: "sp", Status: proto.StatusWorking})
	select {
	case <-st.started:
	case <-time.After(5 * time.Second):
		t.Fatal("no automatic link")
	}
	manualDone := make(chan struct{})
	manualCode := 0
	go func() {
		manualCode = e.do("PUT", "/api/v1/todos/td_space/agents/mac/s1", "").Code
		close(manualDone)
	}()
	waitWorkspaceRequest(t, manualDone, "linkTodoAgentWithSourceAllowingDone")
	releaseOnce.Do(func() { close(st.release) })
	select {
	case <-st.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the automatic link did not finish")
	}
	select {
	case <-manualDone:
		if manualCode != 200 {
			t.Fatalf("manual link returned %d", manualCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual link did not finish")
	}
	rows, err := database.ListAllTodoAgents(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Source != "manual" || rows[0].ReleasedAt != nil {
		t.Fatalf("stored %+v %v", rows, err)
	}
	if a := agentOf(*e.get("td_space"), "s1"); a == nil || a.Source != "manual" || a.ReleasedAt != nil {
		t.Fatalf("s1 = %+v", a)
	}
	reloadTodos(t, e.s)
	if got := e.todoOf("mac", "s1"); got != "td_space" {
		t.Fatalf("after reload s1 works for %q", got)
	}
}

// An automatic link the store did not keep gives back the done todo's link it released, unless the agent was linked
// by hand since (that link released it in the store too); a todo reopened meanwhile has the agent working for it again
// (#2904).
func TestUndoAutoLinkGivesBackTheDoneTodo(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	before, later := t0.Add(-time.Hour), t0.Add(time.Second)
	key := [2]string{"m", "s"}
	item := pendingAutoLink{todoID: "big", machineID: "m", sessionID: "s", linkedAt: t0, released: []string{"old"}}
	for _, c := range []struct {
		name         string
		hand, reopen bool
		wantReleased bool
		wantActive   string
	}{
		{name: "refused", wantActive: ""},
		{name: "linked by hand since", hand: true, wantReleased: true, wantActive: "other"},
		{name: "reopened meanwhile", reopen: true, wantActive: "old"},
	} {
		t.Run(c.name, func(t *testing.T) {
			old := &Todo{ID: "old", DoneAt: &before, Agents: []TodoAgent{{MachineID: "m", SessionID: "s", LinkedAt: before, ReleasedAt: &t0, Source: "manual"}}}
			if c.reopen {
				old.DoneAt = nil
			}
			b := &todoBook{todos: map[string]*Todo{"old": old, "other": {ID: "other"},
				"big": {ID: "big", Agents: []TodoAgent{{MachineID: "m", SessionID: "s", LinkedAt: t0, Source: "space"}}}},
				activeBySession: map[[2]string]string{key: "big"}}
			if c.hand {
				b.todos["big"].Agents[0].ReleasedAt = &later
				b.todos["other"].Agents = []TodoAgent{{MachineID: "m", SessionID: "s", LinkedAt: later, Source: "manual"}}
				b.activeBySession[key] = "other"
			}
			if !b.undoAutoLink(item, later) {
				t.Fatal("nothing changed")
			}
			if released := old.Agents[0].ReleasedAt != nil; released != c.wantReleased || b.activeBySession[key] != c.wantActive || len(b.todos["big"].Agents) != 0 {
				t.Fatalf("old %+v, active %q, big %+v", old.Agents[0], b.activeBySession[key], b.todos["big"].Agents)
			}
		})
	}
}
