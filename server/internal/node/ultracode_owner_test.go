package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Regressions of #5510: what the node observes of ultracode must not make a session Persona's, nor write a record
// before the state is loaded.

// putRecorder records the records written to the hub (state.put events).
type putRecorder struct {
	mu   sync.Mutex
	puts map[string][]sessionState
}

func recordPuts(s *stateFile) *putRecorder {
	r := &putRecorder{puts: map[string][]sessionState{}}
	s.mu.Lock()
	s.persist = func(id string, v sessionState) {
		r.mu.Lock()
		r.puts[id] = append(r.puts[id], v)
		r.mu.Unlock()
	}
	s.mu.Unlock()
	return r
}

func (r *putRecorder) of(id string) []sessionState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.puts[id])
}

// A Claude the user started has a SessionStart with a source (startup or resume) first. Taking its process boundary
// must not make it Persona's: its prompts and Stops are still reported and counted, and nothing goes to the hub's
// node_sessions for it.
func TestReportOnlyAfterSessionStartWithSourceKeepsReporting(t *testing.T) {
	for _, source := range []string{"startup", "resume"} {
		t.Run(source, func(t *testing.T) {
			n, _ := testNode(t)
			n.hooks = &hookTurns{}
			rec := hookHub(t, n)
			puts := recordPuts(n.state)
			report := func(event, reply string) {
				in := map[string]any{"hook_event_name": event, "session_id": reportSession, "cwd": "/work/project",
					"transcript_path": "/work/t.jsonl"}
				if event == "SessionStart" {
					in["source"] = source
				}
				if reply != "" {
					in["last_assistant_message"] = reply
				}
				b, _ := json.Marshal(in)
				if out, _ := n.handleHook(context.Background(), sockRequest{Kind: "hook", Pane: "p_1", ReportOnly: true, Input: b}); out != nil {
					t.Fatalf("%s: output %v", event, out)
				}
			}
			report("SessionStart", "")
			report("UserPromptSubmit", "")
			if tr, _ := n.hooks.get(reportSession); !tr.open() {
				t.Fatalf("the prompt after SessionStart(%s) did not open a turn: %+v", source, tr)
			}
			report("Stop", "all done")
			if tr, _ := n.hooks.get(reportSession); tr.open() || !tr.stopped {
				t.Fatalf("the Stop did not close the turn: %+v", tr)
			}
			waitFor(t, "three reports", func() bool { return len(rec.all()) == 3 })
			for i, want := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
				if got := rec.all()[i]; got["event"] != want || got["report_only"] != true || got["session_id"] != reportSession {
					t.Errorf("report %d = %v, want %s", i, got, want)
				}
			}
			if _, ours := n.state.get(reportSession); ours {
				t.Fatal("the user's session got a record of Persona's")
			}
			if got := puts.of(reportSession); len(got) != 0 {
				t.Fatalf("written to the hub: %+v", got)
			}
			n.mu.Lock()
			since := n.ultracodeLocked(reportSession).Since
			n.mu.Unlock()
			if since.IsZero() {
				t.Fatal("the process boundary was not kept in memory")
			}
		})
	}
}

// The list's reading of ultracode evidence, a dismissal, and a switch's verdict on a session the user started stay in
// memory too.
func TestUltracodeObservationsOfTheUsersSessionWriteNoRecord(t *testing.T) {
	n, f, home := herdrNode(t)
	puts := recordPuts(n.state)
	cwd := mkdir(t, filepath.Join(home, "project"))
	at := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	n.paths[sidA] = writeTranscript(t, home, cwd, sidA, time.Now(),
		`{"type":"user","timestamp":"`+at+`","cwd":"`+cwd+`","message":{"role":"user","content":"hi"}}`,
		`{"type":"attachment","timestamp":"`+at+`","attachment":{"type":"ultra_effort_enter","reminderType":"full"}}`,
		`{"type":"system","subtype":"turn_duration","timestamp":"`+at+`","durationMs":1000}`)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
	if s := byID(listSessions(t, n))[sidA]; s.Ultracode == nil || !*s.Ultracode || s.StartedByPersona {
		t.Fatalf("row: %+v", s)
	}
	if _, err := request(t, n, "session.ultracode", map[string]any{"session_id": sidA, "dismiss_lost": true}); err != nil {
		t.Fatal(err)
	}
	ultraScreens(t, f, "w1:p1")
	if err := requestUltra(t, n, false); err != nil {
		t.Fatal(err)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Ultracode == nil || *s.Ultracode || s.StartedByPersona {
		t.Fatalf("after switching off: %+v", s)
	}
	if _, ours := n.state.get(sidA); ours {
		t.Fatal("the user's session got a record of Persona's")
	}
	if got := puts.of(sidA); len(got) != 0 {
		t.Fatalf("written to the hub: %+v", got)
	}
	if !n.takesReport(hookInput{SessionID: sidA, HookEventName: "UserPromptSubmit"}) {
		t.Fatal("the global hooks' reports of the user's session are dropped")
	}
}

// A node restart: lists and hooks run while the state still loads. Nothing is written for a session not loaded yet,
// evidence from an old process is not taken while the stored boundary is unknown, and what was seen meanwhile of
// Persona's own session goes into its record once loaded, with the rest of the record as it was.
func TestUltracodeWaitsForTheStateToLoad(t *testing.T) {
	old := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	boundary := old.Add(time.Hour)
	stored := func() sessionState {
		rec := ultracodeRecord{Since: boundary, Value: ultraBool(false), At: boundary}
		return sessionState{Pane: "test-pane", Launcher: "work_claude", Cwd: "/work/project", Model: "opus",
			ModelFingerprint: "fp", StartedAt: old.Add(-time.Hour), Ultracode: &rec}
	}
	same := func(t *testing.T, got, want sessionState) {
		t.Helper()
		if got.Pane != want.Pane || got.Launcher != want.Launcher || got.Cwd != want.Cwd || got.Model != want.Model ||
			got.ModelFingerprint != want.ModelFingerprint || !got.StartedAt.Equal(want.StartedAt) {
			t.Fatalf("record changed: %+v, want %+v", got, want)
		}
	}

	t.Run("old evidence before the load", func(t *testing.T) {
		n, path := ultracodeProcessNode(t)
		puts := recordPuts(n.state)
		if err := os.WriteFile(path, []byte(`{"type":"attachment","timestamp":"`+old.Format(time.RFC3339)+`","attachment":{"type":"ultra_effort_enter","reminderType":"full"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		n.state.beginLoad()
		row := proto.Session{SessionID: ultraProcessTestID, Agent: "claude", PaneID: "test-pane", Status: proto.StatusIdle}
		n.ultracodeRow(&row, nil)
		if row.Ultracode != nil {
			t.Fatalf("shown before the boundary is known: %v", *row.Ultracode)
		}
		n.persistUltracode(ultraProcessTestID)
		if got := puts.of(ultraProcessTestID); len(got) != 0 {
			t.Fatalf("written before the state loaded: %+v", got)
		}
		n.state.merge(map[string]sessionState{ultraProcessTestID: stored()})
		n.ultracodeStateLoaded()
		row = proto.Session{SessionID: ultraProcessTestID, Agent: "claude", PaneID: "test-pane", Status: proto.StatusIdle}
		n.ultracodeRow(&row, nil)
		if row.Ultracode == nil || *row.Ultracode {
			t.Fatalf("an old process's evidence counted: %v", row.Ultracode)
		}
		st, _ := n.state.get(ultraProcessTestID)
		same(t, st, stored())
		for _, put := range puts.of(ultraProcessTestID) {
			same(t, put, stored())
		}
	})

	t.Run("a new process before the load", func(t *testing.T) {
		n, path := ultracodeProcessNode(t)
		puts := recordPuts(n.state)
		n.state.beginLoad()
		n.ultracodeHook(sockRequest{}, hookInput{SessionID: ultraProcessTestID, TranscriptPath: path, HookEventName: "SessionStart", Source: "resume"})
		if got := puts.of(ultraProcessTestID); len(got) != 0 {
			t.Fatalf("written before the state loaded: %+v", got)
		}
		n.mu.Lock()
		since := n.ultracodeLocked(ultraProcessTestID).Since
		n.mu.Unlock()
		n.state.merge(map[string]sessionState{ultraProcessTestID: stored()})
		n.ultracodeStateLoaded()
		got := puts.of(ultraProcessTestID)
		if len(got) != 1 || got[0].Ultracode == nil || !got[0].Ultracode.Since.Equal(since) || !since.After(boundary) {
			t.Fatalf("the new boundary was not written once loaded: %+v", got)
		}
		same(t, got[0], stored())
	})
}

// loadHub is the hub's end of a node connection for loadState: it answers state.load when told to, and keeps the
// state.put events it gets.
type loadHub struct {
	answer  chan func() (any, error)
	flushed chan struct{}
	mu      sync.Mutex
	puts    map[string][]sessionState
}

func withLoadHub(t *testing.T, n *Node) *loadHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	h := &loadHub{answer: make(chan func() (any, error), 1), flushed: make(chan struct{}, 1), puts: map[string][]sessionState{}}
	hub.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "state.load" {
			return nil, fmt.Errorf("unexpected %s", method)
		}
		select {
		case answer := <-h.answer:
			return answer()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	hub.OnEvent = func(event string, params json.RawMessage) {
		switch event {
		case "state.put":
			var p struct {
				SessionID string       `json:"session_id"`
				State     sessionState `json:"state"`
			}
			_ = json.Unmarshal(params, &p)
			h.mu.Lock()
			h.puts[p.SessionID] = append(h.puts[p.SessionID], p.State)
			h.mu.Unlock()
		case "test.flush":
			h.flushed <- struct{}{}
		}
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	return h
}

// putsOf is what the hub got for id, once every event the node sent so far has arrived (events come in order).
func (h *loadHub) putsOf(t *testing.T, n *Node, id string) []sessionState {
	t.Helper()
	if err := n.peer.Notify("test.flush", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub did not get the node's events")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.puts[id])
}

// The real load (loadState against the hub's state.load): the lists and hooks in the meantime, a load that fails,
// one that brings the session's record, one that does not, and a dismissal asked for while it loads.
func TestUltracodeStateLoadsFromTheHub(t *testing.T) {
	old := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	boundary := old.Add(time.Hour)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	stored := func(cwd string, lost bool) sessionState {
		rec := ultracodeRecord{Since: boundary, Value: ultraBool(false), At: boundary}
		if lost {
			rec.LostAt = &boundary
		}
		return sessionState{Pane: "w1:p1", Launcher: "work_claude", Cwd: cwd, Model: "opus", ModelFingerprint: "fp",
			StartedAt: old.Add(-time.Hour), Ultracode: &rec}
	}
	same := func(t *testing.T, got, want sessionState) {
		t.Helper()
		if got.Pane != want.Pane || got.Launcher != want.Launcher || got.Cwd != want.Cwd || got.Model != want.Model ||
			got.ModelFingerprint != want.ModelFingerprint || !got.StartedAt.Equal(want.StartedAt) {
			t.Fatalf("record changed: %+v, want %+v", got, want)
		}
	}
	// start makes a node (as Serve leaves it: loading) whose session sidA rests in w1:p1, its transcript with ultracode
	// turned on a minute ago when evidence, and runs loadState; done is closed when it returned.
	start := func(t *testing.T, evidence bool) (n *Node, h *loadHub, cwd string, done chan struct{}) {
		n, f, home := herdrNode(t)
		n.logger = log.New(io.Discard, "", 0)
		cwd = mkdir(t, filepath.Join(home, "project"))
		lines := []string{`{"type":"user","timestamp":"` + recent + `","cwd":"` + cwd + `","message":{"role":"user","content":"hi"}}`}
		if evidence {
			lines = append(lines, `{"type":"attachment","timestamp":"`+recent+`","attachment":{"type":"ultra_effort_enter","reminderType":"full"}}`)
		}
		lines = append(lines, `{"type":"system","subtype":"turn_duration","timestamp":"`+recent+`","durationMs":1000}`)
		n.paths[sidA] = writeTranscript(t, home, cwd, sidA, time.Now(), lines...)
		h = withLoadHub(t, n)
		n.state.beginLoad() // Serve does, before anything lists or takes a hook
		seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
		done = make(chan struct{})
		go func() { n.loadState(context.Background()); close(done) }()
		return n, h, cwd, done
	}
	finish := func(t *testing.T, done chan struct{}) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("loadState did not return")
		}
	}

	t.Run("the load fails", func(t *testing.T) {
		n, h, _, done := start(t, true)
		if s := byID(cachedSessions(n))[sidA]; s.Ultracode != nil {
			t.Fatalf("shown while the boundary is unknown: %v", *s.Ultracode)
		}
		h.answer <- func() (any, error) { return nil, errors.New("database is down") }
		finish(t, done)
		if n.state.isLoading() {
			t.Fatal("still loading after the load failed")
		}
		if s := byID(cachedSessions(n))[sidA]; s.Ultracode == nil || !*s.Ultracode {
			t.Fatalf("ultracode not shown once the load gave up: %+v", s)
		}
		if got := h.putsOf(t, n, sidA); len(got) != 0 {
			t.Fatalf("written: %+v", got)
		}
	})

	t.Run("a new process while it loads", func(t *testing.T) {
		n, h, cwd, done := start(t, true)
		n.ultracodeHook(sockRequest{}, hookInput{SessionID: sidA, TranscriptPath: n.paths[sidA], HookEventName: "SessionStart", Source: "resume"})
		cachedSessions(n)
		if got := h.putsOf(t, n, sidA); len(got) != 0 {
			t.Fatalf("written before the state loaded: %+v", got)
		}
		n.mu.Lock()
		since := n.ultracodeLocked(sidA).Since
		n.mu.Unlock()
		h.answer <- func() (any, error) {
			return map[string]any{"sessions": map[string]sessionState{sidA: stored(cwd, false)}}, nil
		}
		finish(t, done)
		got := h.putsOf(t, n, sidA)
		if len(got) != 1 || got[0].Ultracode == nil || !got[0].Ultracode.Since.Equal(since) || !since.After(boundary) {
			t.Fatalf("the new boundary was not written once loaded, once: %+v", got)
		}
		same(t, got[0], stored(cwd, false))
		if s := byID(cachedSessions(n))[sidA]; s.Ultracode == nil || *s.Ultracode {
			t.Fatalf("the evidence of the process before counted: %+v", s)
		}
	})

	t.Run("the session is not in it", func(t *testing.T) {
		n, h, _, done := start(t, true)
		n.ultracodeHook(sockRequest{}, hookInput{SessionID: sidA, TranscriptPath: n.paths[sidA], HookEventName: "SessionStart", Source: "startup"})
		h.answer <- func() (any, error) { return map[string]any{"sessions": map[string]sessionState{}}, nil }
		finish(t, done)
		if _, ours := n.state.get(sidA); ours || n.state.isLoading() {
			t.Fatalf("record made up, or still loading: %v", n.state.isLoading())
		}
		if got := h.putsOf(t, n, sidA); len(got) != 0 {
			t.Fatalf("written: %+v", got)
		}
	})

	t.Run("a dismissal waits for it", func(t *testing.T) {
		n, h, cwd, done := start(t, false)
		dismissed := make(chan error, 1)
		go func() { dismissed <- n.setUltracode(context.Background(), sidA, nil, true) }()
		select {
		case err := <-dismissed:
			t.Fatalf("answered before the stored notice was read in: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		h.answer <- func() (any, error) {
			return map[string]any{"sessions": map[string]sessionState{sidA: stored(cwd, true)}}, nil
		}
		finish(t, done)
		select {
		case err := <-dismissed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the dismissal did not go on once loaded")
		}
		if s := byID(cachedSessions(n))[sidA]; s.UltracodeLostAt != nil {
			t.Fatalf("the dismissed notice came back from the stored record: %+v", s)
		}
		got := h.putsOf(t, n, sidA)
		if len(got) == 0 {
			t.Fatal("the dismissal was not written")
		}
		last := got[len(got)-1]
		if last.Ultracode == nil || last.Ultracode.LostAt != nil || !last.Ultracode.LostDismissed || !last.Ultracode.Since.Equal(boundary) {
			t.Fatalf("written: %+v", last.Ultracode)
		}
		same(t, last, stored(cwd, true))
	})
}

// A first state.load that fails ends the loading on an empty state, so a listed session's ultracode state is read in
// with nothing stored. When a later try on the same connection brings the session's record (startState, reloadState),
// its stored observation is read in again: what is written next is the stored one, its Since, LostAt and
// LostDismissed kept, not the empty one read in before.
func TestUltracodeStoredObservationSurvivesALateLoad(t *testing.T) {
	defer func(d []time.Duration) { replayRetryAfter = d }(replayRetryAfter)
	replayRetryAfter = []time.Duration{10 * time.Millisecond}
	old := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	boundary := old.Add(time.Hour)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	n, f, home := herdrNode(t)
	n.logger = log.New(io.Discard, "", 0)
	cwd := mkdir(t, filepath.Join(home, "project"))
	n.paths[sidA] = writeTranscript(t, home, cwd, sidA, time.Now(),
		`{"type":"user","timestamp":"`+recent+`","cwd":"`+cwd+`","message":{"role":"user","content":"hi"}}`,
		`{"type":"system","subtype":"turn_duration","timestamp":"`+recent+`","durationMs":1000}`)
	h := withLoadHub(t, n)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait := func(ch chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	n.startState(ctx, nil)
	h.answer <- func() (any, error) { return nil, errors.New("database is restarting") }
	wait(n.stateMissed, "the first try to fail")
	cachedSessions(n) // read in against the empty state
	rec := ultracodeRecord{Since: boundary, Value: ultraBool(false), At: boundary, LostAt: &boundary, LostDismissed: true}
	stored := sessionState{Pane: "w1:p1", Launcher: "work_claude", Cwd: cwd, Model: "opus", ModelFingerprint: "fp",
		StartedAt: old.Add(-time.Hour), Ultracode: &rec}
	h.answer <- func() (any, error) { return map[string]any{"sessions": map[string]sessionState{sidA: stored}}, nil }
	wait(n.stateReady, "the state on a later try")
	n.persistUltracode(sidA)
	got := h.putsOf(t, n, sidA)
	if len(got) == 0 {
		t.Fatal("nothing written")
	}
	last := got[len(got)-1].Ultracode
	if last == nil || !last.Since.Equal(boundary) || last.LostAt == nil || !last.LostAt.Equal(boundary) || !last.LostDismissed {
		t.Fatalf("written over the stored observation: %+v", last)
	}
}

// Records a node of #3782 wrote for the user's sessions (only an observation, no launcher, directory or start time)
// are dropped when loaded: the session is not Persona's, and nothing of the old observation comes back with each
// restart (a resume notice could never be dismissed for good: nothing writes it again). A branch of Persona's own (no
// start time, a launcher and a directory) stays Persona's, with its observation.
func TestStoredObservationOnlyRecordsAreNotPersonas(t *testing.T) {
	n, _ := testNode(t)
	at := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	rec := ultracodeRecord{Since: at, Value: ultraBool(true), At: at, LostAt: &at}
	const branch = "22222222-2222-3333-4444-555555555555"
	n.state.merge(map[string]sessionState{
		reportSession: {Pane: "p_9", Ultracode: &rec},
		branch:        {Launcher: "claude", Cwd: "/work/project", Ultracode: &rec},
	})
	if _, ours := n.state.get(reportSession); ours {
		t.Fatal("an observation-only record read as Persona's session")
	}
	if !n.takesReport(hookInput{SessionID: reportSession, HookEventName: "Stop"}) {
		t.Fatal("its global hooks' reports are dropped")
	}
	row := proto.Session{SessionID: reportSession, Agent: "claude", Status: proto.StatusOffline}
	n.ultracodeRow(&row, nil)
	if row.Ultracode != nil || row.UltracodeLostAt != nil {
		t.Fatalf("the old observation came back: %+v", row)
	}
	if _, ours := n.state.get(branch); !ours {
		t.Fatal("a branch without a start time is still Persona's")
	}
	n.mu.Lock()
	u := n.ultracodeLocked(branch)
	n.mu.Unlock()
	if !u.Since.Equal(at) || u.Value == nil || !*u.Value {
		t.Fatalf("Persona's own observation was lost: %+v", u.ultracodeRecord)
	}
}

// A branch (fork.go, no start time) or a session the user started that Persona resumes is Persona's from then on.
func TestResumedBranchIsStartedByPersona(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(n *Node, cwd string)
	}{
		{"branch", func(n *Node, cwd string) {
			n.state.put(startID, sessionState{Launcher: "claude", Cwd: cwd})
		}},
		{"observed before", func(n *Node, _ string) {
			n.ultracodeHook(sockRequest{}, hookInput{SessionID: startID, HookEventName: "SessionStart", Source: "startup"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "fresh"))
			tc.setup(n, cwd)
			f.reply("workspace.list", map[string]any{"workspaces": []any{}})
			f.reply("workspace.create", map[string]any{"workspace": map[string]any{"workspace_id": "w7"},
				"tab": map[string]any{"tab_id": "w7:t1"}, "root_pane": map[string]any{"pane_id": "w7:p1"}})
			startHerdr(f, "w7:p1", proto.StatusDone)
			f.setScreen("w7:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"))
			res, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "claude", "resume_session_id": startID})
			if err != nil {
				t.Fatal(err)
			}
			if s := decodeSessions(t, []any{res["session"]})[0]; !s.StartedByPersona {
				t.Fatalf("resumed by Persona, not started by it: %+v", s)
			}
			if st, ok := n.state.get(startID); !ok || st.StartedAt.IsZero() {
				t.Fatalf("record: %+v", st)
			}
		})
	}
}

// A start that asks for ultracode and does not get it is still a start: Claude is up, the first prompt goes in, and
// the note says why ultracode is not on.
func TestStartUltracodeRefusalStillStartsAndPrompts(t *testing.T) {
	for _, tc := range []struct{ name, verdict, note string }{
		{"workflows", "Ultracode needs dynamic workflows enabled (see /config). Valid options are: on, off", "工作流"},
		{"model", "Ultracode isn't available on claude-sonnet-4-5. Valid options are: on, off", "不支持"},
		{"no verdict", "", "未能确认"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "project"))
			plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
			startPane(t, f, plain, ansi)
			f.after(func(c herdrCall) {
				if c.Method != "agent.prompt" {
					return
				}
				text := str(c.Params["text"])
				if text != ultracodeCommand(true) {
					writeUserLine(t, home, cwd, startedID(f), text)
				} else if tc.verdict != "" {
					f.setScreens("w1:p2", plain+"\n❯ "+text+"\n  ⎿ "+tc.verdict+"\n", ansi)
				}
			})
			if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build", "ultracode": true}); err != nil {
				t.Fatalf("the start failed for ultracode: %v", err)
			}
			n.firstWait.Wait()
			got := f.trace("agent.prompt")
			if len(got) != 2 || !strings.Contains(got[0], "/effort ultracode on") || !strings.Contains(got[1], "fix the build") {
				t.Fatalf("typed: %v", got)
			}
			n.mu.Lock()
			u := n.ultracodeLocked(startedID(f))
			note, pending := u.note, u.pending
			n.mu.Unlock()
			if !strings.Contains(note, tc.note) || pending != nil {
				t.Fatalf("note %q, pending %v", note, pending)
			}
		})
	}
}

// The same in the background: the start is not recorded as failed, and the message written meanwhile goes in.
func TestStartAsyncUltracodeRefusalLetsMessagesIn(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	startPane(t, f, plain, ansi)
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		text := str(c.Params["text"])
		if text == ultracodeCommand(true) {
			f.setScreens("w1:p2", plain+"\n❯ "+text+"\n  ⎿ Ultracode needs dynamic workflows enabled (see /config).\n", ansi)
			return
		}
		id := startedID(f)
		writeUserLine(t, home, cwd, id, text)
		f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(id, proto.StatusWorking, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
	})
	s, err := n.startAsync("", cwd, "claude", "first task", "w1", "", "", "", modelExecution{Ultracode: ultraBool(true)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.send(ctx, s.SessionID, "second task", nil, sendOpts{}); err != nil {
		t.Fatalf("a message to a started session was refused: %v", err)
	}
	n.firstWait.Wait()
	if r := n.starts.get(s.SessionID); r != nil {
		t.Fatalf("the start is listed as failed: %v", r.err)
	}
	got := f.trace("agent.prompt")
	if len(got) != 3 || !strings.Contains(got[0], "/effort ultracode on") || !strings.Contains(got[1], "first task") || !strings.Contains(got[2], "second task") {
		t.Fatalf("order: %v", got)
	}
}

// ultracode:false on a start asks for nothing: no agent or version check, no command.
func TestStartUltracodeOffIsLeftOut(t *testing.T) {
	n, f, home := herdrNode(t)
	n.claudeV.set("2.1.200") // too old for the switch: false is not refused for it
	if err := n.checkUltracodeStart(context.Background(), ultraBool(false), "codex"); err != nil {
		t.Fatalf("off refused for Codex: %v", err)
	}
	if err := n.checkUltracodeStart(context.Background(), ultraBool(true), "claude"); refusalCode(err) != ultracodeUnsupportedCode {
		t.Fatalf("on with an old CLI: %v", err)
	}
	cwd := mkdir(t, filepath.Join(home, "project"))
	startPane(t, f, screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" {
			writeUserLine(t, home, cwd, startedID(f), str(c.Params["text"]))
		}
	})
	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build", "ultracode": false}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	if got := f.trace("agent.prompt"); len(got) != 1 || !strings.Contains(got[0], "fix the build") {
		t.Fatalf("typed: %v", got)
	}
}

// A queued switch holds back its command, never a message: with Claude at rest the command goes first, and its
// refusal does not stop the message.
func TestUltracodeQueuedSwitchGoesBeforeTheMessageAndNeverStopsIt(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusDone, "w1:p1", "w1:t1", "w1", "/tmp/project", ""))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	f.setScreens("w1:p1", plain, ansi)
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" && str(c.Params["text"]) == ultracodeCommand(true) {
			f.setScreens("w1:p1", plain+"\n❯ /effort ultracode on\n  ⎿ Ultracode needs dynamic workflows enabled (see /config).\n", ansi)
		}
	})
	n.mu.Lock()
	n.ultracodeLocked(sidA).pending = ultraBool(true)
	n.mu.Unlock()
	if err := n.send(context.Background(), sidA, "next task", nil, sendOpts{}); err != nil {
		t.Fatalf("the switch's refusal stopped the message: %v", err)
	}
	got := f.trace("agent.prompt")
	if len(got) != 2 || !strings.Contains(got[0], "/effort ultracode on") || !strings.Contains(got[1], "next task") {
		t.Fatalf("typed: %v", got)
	}
	s := byID(listSessions(t, n))[sidA]
	if s.UltracodePending || !strings.Contains(s.UltracodeNote, "工作流") {
		t.Fatalf("row: %+v", s)
	}
}

// A draft at the computer keeps the switch queued: the request is accepted, its note is a fixed text that quotes
// nothing typed there, and the tries back off.
func TestUltracodeDraftQueuesWithAFixedNote(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/project", ""))
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi"))
	if err := requestUltra(t, n, true); err != nil {
		t.Fatalf("a queued request was answered as failed: %v", err)
	}
	if got := f.trace("agent.prompt", "pane.send_keys", "pane.send_text"); len(got) != 0 {
		t.Fatalf("typed into the draft: %v", got)
	}
	s := byID(listSessions(t, n))[sidA]
	if !s.UltracodePending || s.UltracodeNote != ultracodeDraftNote || strings.Contains(s.UltracodeNote, "flaky") {
		t.Fatalf("row: %+v", s)
	}
	var waits []time.Duration
	for range 3 {
		before := time.Now()
		if err := n.applyPendingUltracode(context.Background(), sidA, "w1:p1"); err != nil {
			t.Fatal(err)
		}
		n.mu.Lock()
		waits = append(waits, n.ultracodeLocked(sidA).retryAt.Sub(before))
		n.mu.Unlock()
	}
	if !(waits[0] < waits[1] && waits[1] < waits[2]) {
		t.Fatalf("no backoff: %v", waits)
	}
}

func TestUltracodeBackoff(t *testing.T) {
	var u ultracodeState
	now := time.Now()
	var got []time.Duration
	for range 10 {
		u.backoff(now)
		got = append(got, u.retryAt.Sub(now))
	}
	if got[0] != ultracodeRetry || got[1] != 2*ultracodeRetry || got[len(got)-1] != ultracodeRetryMax {
		t.Fatalf("waits %v", got)
	}
}

// Asking for the value the process has while the other one waits takes the queued request back.
func TestUltracodeRequestBackToTheCurrentValueCancelsTheQueue(t *testing.T) {
	n, f, _ := herdrNode(t)
	a := claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/tmp/project", "")
	seedAgents(t, n, f, a)
	ultraScreens(t, f, "w1:p1")
	n.mu.Lock()
	u := n.ultracodeLocked(sidA)
	u.Value, u.At = ultraBool(false), time.Now()
	n.mu.Unlock()
	if err := requestUltra(t, n, true); err != nil {
		t.Fatal(err)
	}
	if err := requestUltra(t, n, false); err != nil {
		t.Fatal(err)
	}
	if s := byID(listSessions(t, n))[sidA]; s.UltracodePending {
		t.Fatalf("still queued: %+v", s)
	}
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Fatalf("typed: %v", got)
	}
}

// botmux takes messages only: the switch is refused at once, and a switch queued before (an older node) ends with a
// note and a pause instead of being tried again on every list.
func TestBotmuxRefusesUltracode(t *testing.T) {
	n, f, _, home := bmxNode(t)
	f.rows = []map[string]any{bmxRow(sidA, "claude-code", "alive", "idle", false, "a")}
	writeTranscript(t, home, bmxCwd, sidA, time.Now().Add(-time.Minute), `{"type":"user","message":{"role":"user","content":"hi"}}`)
	s := byID(listSessions(t, n))[sidA]
	if s.PaneID == "" {
		t.Fatalf("row: %+v", s)
	}
	if err := requestUltra(t, n, true); refusalCode(err) != ultracodeUnsupportedCode {
		t.Fatalf("refusal: %v", err)
	}
	n.mu.Lock()
	n.ultracodeLocked(sidA).pending = ultraBool(true)
	n.mu.Unlock()
	if err := n.applyPendingUltracode(context.Background(), sidA, s.PaneID); refusalCode(err) != ultracodeUnsupportedCode {
		t.Fatalf("queued: %v", err)
	}
	n.mu.Lock()
	u := n.ultracodeLocked(sidA)
	pending, note, retry := u.pending, u.note, u.retryAt
	n.mu.Unlock()
	if pending != nil || note != ultracodeScreenlessNote || !retry.After(time.Now()) {
		t.Fatalf("pending %v note %q retry %v", pending, note, retry)
	}
	if got := f.sent(); len(got) != 0 {
		t.Fatalf("sent to botmux: %v", got)
	}
	if err := n.send(context.Background(), sidA, "继续", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := f.sent(); len(got) != 1 || got[0]["instruction"] != "继续" {
		t.Fatalf("sent: %v", got)
	}
}

// The version gate reads only what this process wrote: a newer CLI resuming a session an old one wrote is tried.
func TestUltracodeVersionGateWaitsForThisProcess(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	oldLine := `{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","version":"2.1.283","message":{"model":"opus","content":[{"type":"text","text":"Done"}]}}`
	n.paths[sidA] = writeTranscript(t, home, cwd, sidA, time.Now(), oldLine)
	n.claudeV.set("2.1.318")
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusDone, "w1:p1", "w1:t1", "w1", cwd, ""))
	n.beginUltracodeProcess(sidA, true, false, time.Now()) // resumed by a CLI that has written nothing yet
	if s := byID(listSessions(t, n))[sidA]; s.UltracodeNote != "" {
		t.Fatalf("the old process's version made a note: %q", s.UltracodeNote)
	}
	ultraScreens(t, f, "w1:p1")
	if err := requestUltra(t, n, true); err != nil {
		t.Fatalf("refused on the old process's version: %v", err)
	}
	newLine := `{"type":"assistant","timestamp":"` + time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano) + `","version":"2.1.283","message":{"model":"opus","content":[{"type":"text","text":"Done"}]}}`
	n.paths[sidA] = writeTranscript(t, home, cwd, sidA, time.Now().Add(time.Second), oldLine, newLine)
	n.mu.Lock()
	n.metas = map[string]quickMeta{}
	n.mu.Unlock()
	if err := requestUltra(t, n, true); refusalCode(err) != ultracodeUnsupportedCode || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("this process's old version: %v", err)
	}
	// Offline, nothing of a version is shown.
	row := proto.Session{SessionID: sidA, Agent: "claude", Status: proto.StatusOffline}
	n.ultracodeRow(&row, nil)
	if row.UltracodeNote != "" {
		t.Fatalf("offline note %q", row.UltracodeNote)
	}
}

// Sends that wait for a start get the input in the order they came, and one that gives up keeps the order behind it.
func TestSendsWaitingForAStartKeepTheirOrder(t *testing.T) {
	n, _ := testNode(t)
	r := &startRec{done: make(chan struct{})}
	n.starts.add(sidA, r)
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	tail := func() chan struct{} {
		n.sendOrder.mu.Lock()
		defer n.sendOrder.mu.Unlock()
		return n.sendOrder.tail[sidA]
	}
	gone, giveUp := context.WithCancel(context.Background())
	defer giveUp()
	for i := range 20 {
		prev := tail()
		ctx := context.Background()
		if i == 7 {
			ctx = gone
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := n.inputTurn(ctx, sidA)
			if err != nil {
				if i != 7 {
					t.Errorf("send %d: %v", i, err)
				}
				return
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			unlock()
		}()
		waitFor(t, "in line", func() bool { return tail() != prev })
		if i == 7 {
			giveUp()
		}
	}
	n.starts.finish(sidA, nil)
	wg.Wait()
	want := []int{}
	for i := range 20 {
		if i != 7 {
			want = append(want, i)
		}
	}
	if !slices.Equal(order, want) {
		t.Fatalf("order %v", order)
	}
}

// A switch that keeps waiting (a draft at the computer) is tried again later and later, and a try pushes the list only
// when it changed: not the whole list every few seconds.
func TestUltracodeRetriesPushOnlyChangedLists(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/project", ""))
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi"))
	pushes, pw := hubPushes(n)
	defer pw.Close()
	n.mu.Lock()
	n.ultracodeLocked(sidA).pending = ultraBool(true)
	n.mu.Unlock()
	var changed []int
	for try := 1; try <= 3; try++ {
		n.mu.Lock()
		u := n.ultracodeLocked(sidA)
		u.retryAt = time.Time{} // as if the pause were over
		n.mu.Unlock()
		cachedSessions(n)
		waitFor(t, "the try", func() bool {
			n.mu.Lock()
			defer n.mu.Unlock()
			return u.failures == try && !u.applying
		})
		count := 0
		for wait := time.After(300 * time.Millisecond); ; {
			select {
			case p := <-pushes:
				if p.Event == "sessions.changed" {
					count++
				}
				continue
			case <-wait:
			}
			break
		}
		changed = append(changed, count)
	}
	if changed[0] != 1 || changed[1] != 0 || changed[2] != 0 {
		t.Fatalf("sessions.changed per try: %v", changed)
	}
	if got := f.trace("agent.prompt", "pane.send_keys", "pane.send_text"); len(got) != 0 {
		t.Fatalf("typed into the draft: %v", got)
	}
}

// screenUntil answers pane.read with plain / ansi until broken says otherwise, then fails it as a herdr that is
// restarting does.
func screenUntil(f *fakeHerdr, plain, ansi string, broken func() bool) {
	f.handle("pane.read", func(p map[string]any) any {
		if broken() {
			return herdrFail{Code: "server_error", Message: "herdr is restarting"}
		}
		if p["format"] == "ansi" {
			return map[string]any{"read": map[string]any{"text": ansi}}
		}
		return map[string]any{"read": map[string]any{"text": plain}}
	})
}

// A queued switch whose command went to the terminal and is not known to have gone in may still sit in the input
// box. A screen that cannot be read then says nothing of the box: the message is not typed after the command.
func TestUltracodeUncertainCommandAndAnUnreadScreenHoldTheMessage(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/project", ""))
	var mu sync.Mutex
	commanded := false
	screenUntil(f, screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"), func() bool {
		mu.Lock()
		defer mu.Unlock()
		return commanded
	})
	f.handle("agent.prompt", func(p map[string]any) any {
		if str(p["text"]) != ultracodeCommand(true) {
			return map[string]any{}
		}
		mu.Lock()
		commanded = true
		mu.Unlock()
		return herdrFail{Code: "timeout", Message: "no answer from the terminal"} // typed or not: unknown
	})
	n.mu.Lock()
	n.ultracodeLocked(sidA).pending = ultraBool(true)
	n.mu.Unlock()
	err := n.send(context.Background(), sidA, "next task", nil, sendOpts{})
	if refusalCode(err) != notDelivered {
		t.Fatalf("send: %v", err)
	}
	if got := f.trace("agent.prompt", "pane.send_text", "pane.send_keys"); len(got) != 1 || !strings.Contains(got[0], "/effort ultracode on") {
		t.Fatalf("typed after the command: %v", got)
	}
}

// The same for a start: the first prompt is not typed after a command that may still sit in the box (a draft shows,
// or the box cannot be read); the session needs the user, with why, as for a first prompt a dialog held back.
func TestStartUltracodeCommandLeftInTheBoxHoldsTheFirstPrompt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		unread bool
	}{{"a draft", false}, {"no screen", true}} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "project"))
			plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
			typedTxt, typedANSI := screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi")
			startPane(t, f, plain, ansi)
			var mu sync.Mutex
			commanded := false
			if tc.unread {
				screenUntil(f, plain, ansi, func() bool {
					mu.Lock()
					defer mu.Unlock()
					return commanded
				})
			}
			f.handle("agent.prompt", func(p map[string]any) any {
				if str(p["text"]) != ultracodeCommand(true) {
					return map[string]any{}
				}
				mu.Lock()
				commanded = true
				mu.Unlock()
				f.setScreens("w1:p2", typedTxt, typedANSI) // seen when the screen is read: something left in the box
				return herdrFail{Code: "timeout", Message: "no answer from the terminal"}
			})
			if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build", "ultracode": true}); err != nil {
				t.Fatalf("the start failed: %v", err)
			}
			n.firstWait.Wait()
			if got := f.trace("agent.prompt", "pane.send_text"); len(got) != 1 || !strings.Contains(got[0], "/effort ultracode on") {
				t.Fatalf("typed: %v", got)
			}
			if lost, ok := n.lostFirst.get(startedID(f)); !ok || lost.why != lostUltracode {
				t.Fatalf("lost first prompt: %+v %v", lost, ok)
			}
		})
	}
}

// A screen that cannot be read for a moment (herdr restarting) keeps the switch queued with a fixed note, and the list's
// tries back off, typing nothing and pushing the list only when it changed.
func TestUltracodeUnreadScreenWaitsWithBackoff(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/project", ""))
	screenUntil(f, "", "", func() bool { return true })
	pushes, pw := hubPushes(n)
	defer pw.Close()
	n.mu.Lock()
	n.ultracodeLocked(sidA).pending = ultraBool(true)
	n.mu.Unlock()
	var changed []int
	var waits []time.Duration
	for try := 1; try <= 3; try++ {
		n.mu.Lock()
		u := n.ultracodeLocked(sidA)
		u.retryAt = time.Time{} // as if the pause were over
		n.mu.Unlock()
		before := time.Now()
		cachedSessions(n)
		waitFor(t, "the try", func() bool {
			n.mu.Lock()
			defer n.mu.Unlock()
			return u.failures == try && !u.applying
		})
		n.mu.Lock()
		waits = append(waits, u.retryAt.Sub(before))
		n.mu.Unlock()
		count := 0
		for wait := time.After(300 * time.Millisecond); ; {
			select {
			case p := <-pushes:
				if p.Event == "sessions.changed" {
					count++
				}
				continue
			case <-wait:
			}
			break
		}
		changed = append(changed, count)
	}
	n.mu.Lock()
	u := n.ultracodeLocked(sidA)
	pending, note := u.pending, u.note
	n.mu.Unlock()
	if pending == nil || note != ultracodeScreenWaitNote {
		t.Fatalf("pending %v note %q", pending, note)
	}
	if !(waits[0] < waits[1] && waits[1] < waits[2]) {
		t.Fatalf("no backoff: %v", waits)
	}
	if changed[0] != 1 || changed[1] != 0 || changed[2] != 0 {
		t.Fatalf("sessions.changed per try: %v", changed)
	}
	if got := f.trace("agent.prompt", "pane.send_keys", "pane.send_text"); len(got) != 0 {
		t.Fatalf("typed: %v", got)
	}
}

// The list's try of a queued switch waits its turn at the input like a message, and decides again once it has it: a
// message's own try meanwhile may have put the switch off.
func TestUltracodeRetryDecidesAgainOnceItHasTheInput(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/project", ""))
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi"))
	n.mu.Lock()
	u := n.ultracodeLocked(sidA)
	u.pending = ultraBool(true)
	n.mu.Unlock()
	unlock, err := n.inputTurn(context.Background(), sidA) // a message is being typed
	if err != nil {
		t.Fatal(err)
	}
	cachedSessions(n) // the list starts a try, which waits for the input
	n.mu.Lock()
	applying := u.applying
	u.retryAt = time.Now().Add(time.Minute) // the message's try found the draft and put the switch off
	n.mu.Unlock()
	if !applying {
		t.Fatal("the list started no try")
	}
	f.reset()
	unlock()
	waitFor(t, "the try to end", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return !u.applying
	})
	n.mu.Lock()
	failures := u.failures
	n.mu.Unlock()
	if failures != 0 {
		t.Fatalf("tried again before its time: %d", failures)
	}
	if got := f.trace("pane.read", "agent.prompt"); len(got) != 0 {
		t.Fatalf("looked at the pane: %v", got)
	}
}

// One in line for the input leaves the line only once it has the input lock, so the next one cannot race it for the
// lock and go first.
func TestInputTurnLeavesTheLineOnlyWithTheLock(t *testing.T) {
	n, _ := testNode(t)
	tail := func() chan struct{} {
		n.sendOrder.mu.Lock()
		defer n.sendOrder.mu.Unlock()
		return n.sendOrder.tail[sidA]
	}
	unlock0, err := n.inputTurn(context.Background(), sidA)
	if err != nil {
		t.Fatal(err)
	}
	got1 := make(chan func(), 1)
	go func() {
		unlock, err := n.inputTurn(context.Background(), sidA)
		if err != nil {
			t.Error(err)
		}
		got1 <- unlock
	}()
	var second chan struct{}
	for wait := time.Now().Add(2 * time.Second); second == nil && time.Now().Before(wait); time.Sleep(time.Millisecond) {
		second = tail()
	}
	if second == nil {
		t.Fatal("the second is not in line while the first has the input: it left before it had the lock")
	}
	select {
	case <-second:
		t.Fatal("the second left the line while the first still had the input")
	case <-time.After(100 * time.Millisecond):
	}
	unlock0()
	unlock1 := <-got1
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the second did not leave the line with the input")
	}
	unlock1()
}

// Messages sent while a start runs in the background go in after its first prompt, in the order they were sent.
func TestSendsDuringAnAsyncStartGoInInOrder(t *testing.T) {
	withSubmitGrace(t, 50*time.Millisecond)
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	startPane(t, f, screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		id := startedID(f)
		writeUserLine(t, home, cwd, id, str(c.Params["text"]))
		f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(id, proto.StatusWorking, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
	})
	s, err := n.startAsync("", cwd, "claude", "first task", "w1", "", "", "", modelExecution{})
	if err != nil {
		t.Fatal(err)
	}
	tail := func() chan struct{} {
		n.sendOrder.mu.Lock()
		defer n.sendOrder.mu.Unlock()
		return n.sendOrder.tail[s.SessionID]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, text := range []string{"second task", "third task", "fourth task"} {
		prev := tail()
		done := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done)
			if err := n.send(ctx, s.SessionID, text, nil, sendOpts{}); err != nil {
				t.Errorf("%s: %v", text, err)
			}
		}()
		waitFor(t, "in line", func() bool {
			select {
			case <-done:
				return true
			default:
				return tail() != prev
			}
		})
	}
	wg.Wait()
	n.firstWait.Wait()
	var texts []string
	for _, c := range f.calls() {
		if c.Method == "agent.prompt" {
			texts = append(texts, str(c.Params["text"]))
		}
	}
	if !slices.Equal(texts, []string{"first task", "second task", "third task", "fourth task"}) {
		t.Fatalf("order: %q", texts)
	}
}
