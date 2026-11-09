package assistant

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/dialogwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The finished push's decision and the unified states (fleet_push.go, fleet.go), moved from the hub's tests.

func TestTurnTracker(t *testing.T) {
	t0 := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	done := proto.Session{Status: proto.StatusDone}
	hold := time.Hour

	t.Run("finished and left alone: push with the turn's length", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(90), false)
		v, took := tr.settle("k", gen, done, false, false, false, at(150), hold)
		if v != finishPush || took != 90*time.Second {
			t.Fatalf("%v %v", v, took)
		}
		// Settled: a second check for the same end does nothing.
		if v, _ := tr.settle("k", gen, done, false, false, false, at(151), hold); v != finishStale {
			t.Fatalf("again: %v", v)
		}
	})

	t.Run("seen in herdr (idle) or in the app: no push", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(90), false)
		if v, _ := tr.settle("k", gen, proto.Session{Status: proto.StatusIdle}, false, false, false, at(150), hold); v != finishDrop {
			t.Fatalf("idle: %v", v)
		}
		tr.working("k", at(200))
		gen = tr.ended("k", at(300), false)
		if v, _ := tr.settle("k", gen, done, true, false, false, at(360), hold); v != finishDrop {
			t.Fatalf("seen: %v", v)
		}
	})

	t.Run("resumed within the delay: same turn, counted from its first start", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		first := tr.ended("k", at(20), false)
		tr.working("k", at(30)) // status flickers between tool calls
		if v, _ := tr.settle("k", first, done, false, false, false, at(80), hold); v != finishStale {
			t.Fatalf("first end: %v", v)
		}
		gen := tr.ended("k", at(100), false)
		v, took := tr.settle("k", gen, done, false, false, false, at(160), hold)
		if v != finishPush || took != 100*time.Second {
			t.Fatalf("%v %v", v, took)
		}
	})

	t.Run("still working at check time: wait for the next end", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(10), false)
		if v, _ := tr.settle("k", gen, proto.Session{Status: proto.StatusWorking}, false, false, false, at(70), hold); v != finishStale {
			t.Fatalf("%v", v)
		}
	})

	t.Run("background work: hold, then one push when the woken turn ends", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(30), false)
		busy := proto.Session{Status: proto.StatusDone, Background: []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}}
		if v, _ := tr.settle("k", gen, busy, false, false, false, at(90), hold); v != finishHold {
			t.Fatalf("hold: %v", v)
		}
		// The task reports back: Claude works on the report, then finishes.
		tr.working("k", at(600))
		gen = tr.ended("k", at(620), false)
		v, took := tr.settle("k", gen, proto.Session{Status: proto.StatusDone, WokeByTask: true}, false, false, false, at(680), hold)
		if v != finishPush || took != 620*time.Second {
			t.Fatalf("after background: %v %v", v, took)
		}
	})

	t.Run("background that reports back within the delay: the woken turn's end is the push (#4187)", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		first := tr.ended("k", at(30), true) // stops with a background command running
		// It reports back at once: the agent works on the report and finishes before the first check.
		tr.working("k", at(40))
		gen := tr.ended("k", at(50), false)
		if v, _ := tr.settle("k", first, done, false, false, false, at(90), hold); v != finishStale {
			t.Fatalf("first end: %v", v)
		}
		v, took := tr.settle("k", gen, proto.Session{Status: proto.StatusDone, WokeByTask: true}, false, false, false, at(110), hold)
		if v != finishPush || took != 50*time.Second {
			t.Fatalf("woken turn: %v %v", v, took)
		}
	})

	t.Run("running subagent counts as background", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(30), false)
		s := proto.Session{Status: proto.StatusDone, Subagents: []proto.Subagent{{ID: "a", Running: true}}}
		if v, _ := tr.settle("k", gen, s, false, false, false, at(90), hold); v != finishHold {
			t.Fatalf("%v", v)
		}
	})

	t.Run("a running workflow counts as background (#2935)", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(30), false)
		s := proto.Session{Status: proto.StatusDone, Workflows: []proto.Workflow{{TaskID: "w1", Status: proto.WorkflowRunning, Running: true}}}
		if v, _ := tr.settle("k", gen, s, false, false, false, at(90), hold); v != finishHold {
			t.Fatalf("%v", v)
		}
		// The workflow's notification wakes it; that turn's end is the push.
		tr.working("k", at(900))
		gen = tr.ended("k", at(960), false)
		s = proto.Session{Status: proto.StatusDone, WokeByTask: true, Workflows: []proto.Workflow{{TaskID: "w1", Status: proto.WorkflowCompleted}}}
		if v, _ := tr.settle("k", gen, s, false, false, false, at(1020), hold); v != finishPush {
			t.Fatalf("after the workflow: %v", v)
		}
	})

	t.Run("background that never finishes is given up", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(30), false)
		busy := proto.Session{Status: proto.StatusDone, Background: []proto.BackgroundTask{{ID: "b1"}}}
		if v, _ := tr.settle("k", gen, busy, false, false, false, at(30).Add(2*hold), hold); v != finishDrop {
			t.Fatalf("%v", v)
		}
	})

	t.Run("a turn woken by a task on its own: no push", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(120), false)
		if v, _ := tr.settle("k", gen, proto.Session{Status: proto.StatusDone, WokeByTask: true}, false, false, false, at(180), hold); v != finishDrop {
			t.Fatalf("%v", v)
		}
	})

	t.Run("muted, or the assistant already pushed: no push", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("k", at(0))
		gen := tr.ended("k", at(120), false)
		if v, _ := tr.settle("k", gen, done, false, true, false, at(180), hold); v != finishDrop {
			t.Fatalf("muted: %v", v)
		}
		tr.working("k", at(200))
		gen = tr.ended("k", at(400), false)
		if v, _ := tr.settle("k", gen, done, false, false, true, at(460), hold); v != finishDrop {
			t.Fatalf("assistant: %v", v)
		}
	})

	t.Run("sessions are independent", func(t *testing.T) {
		tr := newTurnTracker()
		tr.working("a", at(0))
		tr.working("b", at(50))
		ga := tr.ended("a", at(100), false)
		tr.ended("b", at(100), false)
		if v, took := tr.settle("a", ga, done, false, false, false, at(160), hold); v != finishPush || took != 100*time.Second {
			t.Fatalf("%v %v", v, took)
		}
	})
}

func TestAgentState(t *testing.T) {
	now := time.Now()
	cases := []struct {
		s    proto.Session
		seen time.Time
		want string
	}{
		{proto.Session{Status: proto.StatusBlocked}, time.Time{}, proto.StateNeedsYou},
		{proto.Session{Status: proto.StatusWorking, PendingInteractions: 1}, time.Time{}, proto.StateNeedsYou},
		{proto.Session{Status: proto.StatusWorking}, time.Time{}, proto.StateWorking},
		{proto.Session{Status: proto.StatusDone, UpdatedAt: now}, time.Time{}, proto.StateUnread},
		{proto.Session{Status: proto.StatusDone, UpdatedAt: now}, now.Add(-time.Minute), proto.StateUnread},
		{proto.Session{Status: proto.StatusDone, UpdatedAt: now}, now.Add(time.Second), proto.StateIdle},
		{proto.Session{Status: proto.StatusIdle, UpdatedAt: now}, time.Time{}, proto.StateIdle},
		{proto.Session{Status: proto.StatusOffline}, time.Time{}, proto.StateOffline},
		{proto.Session{Status: proto.StatusUnknown}, time.Time{}, proto.StateUnknown},
	}
	for i, c := range cases {
		if got := agentState(c.s, c.seen); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

// A turn that ended with background commands or subagents still running is working (#2228); needs-you, offline and
// the assistant's conversations keep their state, and a subagent that finished is no background work.
func TestBackgroundState(t *testing.T) {
	shell := []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}
	running := []proto.Subagent{{ID: "a1", Running: true}}
	cases := []struct {
		name  string
		state string
		s     proto.Session
		want  string
	}{
		{"unread with a background command", proto.StateUnread, proto.Session{Background: shell}, proto.StateWorking},
		{"seen with a background command", proto.StateIdle, proto.Session{Background: shell}, proto.StateWorking},
		{"unknown with a subagent running", proto.StateUnknown, proto.Session{Subagents: running}, proto.StateWorking},
		{"a finished subagent", proto.StateIdle, proto.Session{Subagents: []proto.Subagent{{ID: "a1"}}}, proto.StateIdle},
		{"nothing in the background", proto.StateUnread, proto.Session{}, proto.StateUnread},
		{"needs you first", proto.StateNeedsYou, proto.Session{Background: shell}, proto.StateNeedsYou},
		{"offline", proto.StateOffline, proto.Session{Background: shell}, proto.StateOffline},
		{"the assistant's own tasks", proto.StateIdle, proto.Session{Assistant: true, Background: shell, Subagents: running}, proto.StateIdle},
		// #2935: a turn that ended to wait for its workflow's notification is not done.
		{"unread with a workflow running", proto.StateUnread, proto.Session{Workflows: []proto.Workflow{{TaskID: "w1",
			Status: proto.WorkflowRunning, Running: true}}}, proto.StateWorking},
		{"a workflow that ended", proto.StateUnread, proto.Session{Workflows: []proto.Workflow{{TaskID: "w1",
			Status: proto.WorkflowCompleted}}}, proto.StateUnread},
		{"a stalled workflow", proto.StateIdle, proto.Session{Workflows: []proto.Workflow{{TaskID: "w1",
			Status: proto.WorkflowStalled}}}, proto.StateIdle},
		{"a Monitor watching", proto.StateUnread, proto.Session{Background: []proto.BackgroundTask{{ID: "b2", Kind: "monitor"}}},
			proto.StateWorking},
	}
	for _, c := range cases {
		if got := backgroundState(c.state, c.s); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		// background_working: working only because of the background
		if got, bg := stateWithBackground(c.state, c.s); bg != (got == proto.StateWorking && c.state != proto.StateWorking) {
			t.Errorf("%s: background_working %v", c.name, bg)
		}
	}
	if _, bg := stateWithBackground(proto.StateWorking, proto.Session{Background: shell}); bg {
		t.Error("a turn still running with background work is plain working")
	}
	if st, bg := stateWithBackground(proto.StateUnread, proto.Session{Workflows: []proto.Workflow{{TaskID: "w", Running: true}}}); st != proto.StateWorking || !bg {
		t.Errorf("a turn waiting for its workflow: %s %v", st, bg)
	}
}

// Waiting in the background (#6045): working only because of background commands that the node found only waiting;
// a running subagent or workflow, or a turn that goes on, is work.
func TestBackgroundWaiting(t *testing.T) {
	shell := []proto.BackgroundTask{{ID: "b1", Kind: "shell"}}
	for _, c := range []struct {
		name  string
		state string // the agent's own state, before the background
		s     proto.Session
		want  bool
	}{
		{"its background commands only wait", proto.StateIdle, proto.Session{Background: shell, BackgroundWaiting: true}, true},
		{"they work", proto.StateIdle, proto.Session{Background: shell}, false},
		{"its turn goes on", proto.StateWorking, proto.Session{Background: shell, BackgroundWaiting: true}, false},
		{"a subagent runs too", proto.StateUnread, proto.Session{Background: shell, BackgroundWaiting: true,
			Subagents: []proto.Subagent{{ID: "a1", Running: true}}}, false},
		{"a workflow runs too", proto.StateUnread, proto.Session{Background: shell, BackgroundWaiting: true,
			Workflows: []proto.Workflow{{TaskID: "w", Running: true}}}, false},
		{"needs you comes first", proto.StateNeedsYou, proto.Session{Background: shell, BackgroundWaiting: true}, false},
	} {
		s := c.s
		s.State, s.BackgroundWorking = stateWithBackground(c.state, s)
		if got := backgroundWaiting(s); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if got := backgroundWaiting(s); got && s.State != proto.StateWorking {
			t.Errorf("%s: waiting in the background is a working state for older apps, got %s", c.name, s.State)
		}
	}
}

func TestMarkStates(t *testing.T) {
	s := &Service{}
	t0 := time.Now()
	list := []proto.Session{{SessionID: "a", State: proto.StateWorking}}
	s.markStates("m", list, t0)
	if !list[0].StateSince.Equal(t0) {
		t.Fatalf("first sight: %v", list[0].StateSince)
	}
	list = []proto.Session{{SessionID: "a", State: proto.StateWorking}}
	s.markStates("m", list, t0.Add(time.Minute))
	if !list[0].StateSince.Equal(t0) {
		t.Fatalf("same state moved: %v", list[0].StateSince)
	}
	list = []proto.Session{{SessionID: "a", State: proto.StateNeedsYou}}
	s.markStates("m", list, t0.Add(2*time.Minute))
	if !list[0].StateSince.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("state change kept old time: %v", list[0].StateSince)
	}
}

// A session the node stops listing loses its state mark and its hook flag; other machines and listed sessions keep
// theirs. A hook flag for a session not yet listed (its hook came before the list) is kept.
func TestMarkStatesForgetsEndedSessions(t *testing.T) {
	s := &Service{}
	// What a non-report-only hook.event records (NodeEvent; a bare Service has no fleet to take the event).
	hook := func(machine, sid string) {
		if s.fleet.hooked == nil {
			s.fleet.hooked = map[[2]string]bool{}
		}
		s.fleet.hooked[[2]string{machine, sid}] = true
	}
	t0 := time.Now()
	s.markStates("other", []proto.Session{{SessionID: "o", State: proto.StateWorking}}, t0)
	s.markStates("m", []proto.Session{{SessionID: "keep", State: proto.StateWorking}}, t0)
	for i := range 500 {
		sid := fmt.Sprintf("s%d", i)
		s.markStates("m", []proto.Session{{SessionID: "keep", State: proto.StateWorking}, {SessionID: sid, State: proto.StateWorking}}, t0.Add(time.Second))
		hook("m", sid)
	}
	hook("other", "o")
	hook("m", "early")
	list := []proto.Session{{SessionID: "keep", State: proto.StateWorking}}
	s.markStates("m", list, t0.Add(time.Minute))
	if !list[0].StateSince.Equal(t0) {
		t.Fatalf("a listed session's mark moved: %v", list[0].StateSince)
	}
	if n := len(s.fleet.stateSince); n != 2 {
		t.Fatalf("stateSince keeps %d marks, want 2 (m/keep, other/o)", n)
	}
	if _, ok := s.fleet.stateSince[[2]string{"other", "o"}]; !ok {
		t.Fatal("another machine's mark was dropped")
	}
	for k := range s.fleet.hooked {
		if k[0] == "m" && strings.HasPrefix(k[1], "s") {
			t.Fatalf("hooked keeps ended session %v", k)
		}
	}
	if !s.fleet.hooked[[2]string{"other", "o"}] || !s.fleet.hooked[[2]string{"m", "early"}] {
		t.Fatalf("hooked dropped a live flag: %v", s.fleet.hooked)
	}
}

// The history keeps that a secret field was filled in, not what with; the answer itself is not changed.
func TestRedacted(t *testing.T) {
	r := proto.Resolution{Values: map[string]string{"code": "example-password", "api_key": "example-key", "name": "example"}}
	kept := redacted(proto.Interaction{Fields: []proto.Field{{Key: "code", Secret: true}}}, r)
	if kept.Values["code"] != redactedValue || kept.Values["api_key"] != redactedValue || kept.Values["name"] != "example" || r.Values["code"] != "example-password" || r.Values["api_key"] != "example-key" {
		t.Fatal("redaction changed the answer or leaked a secret")
	}
}

func TestRedactedText(t *testing.T) {
	for _, tc := range []struct {
		name string
		ix   proto.Interaction
		want string
	}{
		{"secret", proto.Interaction{Secret: true}, redactedValue},
		{"secret terminal", proto.Interaction{Secret: true, Dialog: &dialogwire.Dialog{}}, redactedValue},
		{"terminal", proto.Interaction{Dialog: &dialogwire.Dialog{}}, "synthetic input"},
		{"ordinary", proto.Interaction{}, "synthetic input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := proto.Resolution{Text: "synthetic input"}
			if kept := redacted(tc.ix, r); kept.Text != tc.want || r.Text != "synthetic input" {
				t.Fatal("text redaction changed the answer or kept the wrong value")
			}
			if kept := redacted(tc.ix, proto.Resolution{}); kept.Text != "" {
				t.Fatal("empty text was changed")
			}
		})
	}
}

func TestRedactedContent(t *testing.T) {
	ix := proto.Interaction{Elicitation: &proto.Elicitation{RequestedSchema: json.RawMessage(`{"properties":{"code":{"format":"password"},"hidden":{"writeOnly":true},"label":{"type":"string"}}}`)}}
	content := map[string]any{"code": "example-code", "hidden": "example-hidden", "access_token": "example-token", "label": "example", "count": float64(3), "enabled": true}
	r := proto.Resolution{Content: content}
	kept := redacted(ix, r)
	want := map[string]any{"code": redactedValue, "hidden": redactedValue, "access_token": redactedValue, "label": "example", "count": float64(3), "enabled": true}
	if !reflect.DeepEqual(kept.Content, want) || r.Content["code"] != "example-code" || r.Content["hidden"] != "example-hidden" || r.Content["access_token"] != "example-token" {
		t.Fatal("content redaction changed the answer or kept the wrong values")
	}
	for _, elicitation := range []*proto.Elicitation{nil, {RequestedSchema: json.RawMessage(`invalid`)}} {
		kept := redacted(proto.Interaction{Elicitation: elicitation}, r)
		if kept.Content["access_token"] != redactedValue || kept.Content["label"] != "example" {
			t.Fatal("credential names were not redacted without a valid schema")
		}
	}
}

// The snapshot says which conversations are the AI twin's (no finished push for them), which agents work for a quiet
// task or a task (its cards name it), and which turns a channel answers; a snapshot that could not read a part keeps
// the last of it.
func TestSnapshotPartsTheFleetReads(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	ref := func(sid string) SessionRef { return SessionRef{MachineID: "mac", SessionID: sid} }
	s.fleetTakeSnapshot(Snapshot{
		Inbox: &assistantwire.InboxReport{Agents: []SessionRef{ref("in1")}},
		Todos: &TodoReport{Links: []TodoLink{{MachineID: "mac", SessionID: "t1", TodoID: "td1", Title: "发版", Task: true}},
			Quiet: []SessionRef{ref("q1")}},
		Channels: &ChannelsReport{Turns: []ChannelTurn{{MachineID: "mac", SessionID: "c1"}}},
	})
	if !s.isInboxAgent("in1") || s.isInboxAgent("t1") {
		t.Fatal("isInboxAgent")
	}
	if !s.quietSession("mac", "q1") || s.quietSession("mac", "t1") {
		t.Fatal("quietSession")
	}
	if got := s.withTask(proto.Interaction{MachineID: "mac", SessionID: "t1"}); got.TaskTitle != "发版" {
		t.Fatalf("withTask %+v", got)
	}
	if !s.channelAnsweredPush("c1") || s.channelAnsweredPush("t1") {
		t.Fatal("channelAnsweredPush")
	}
	s.fleetTakeSnapshot(Snapshot{})
	if !s.isInboxAgent("in1") || !s.quietSession("mac", "q1") {
		t.Fatal("a snapshot without the parts forgot them")
	}
	s.fleetTakeSnapshot(Snapshot{Inbox: &assistantwire.InboxReport{}, Todos: &TodoReport{}})
	if s.isInboxAgent("in1") || s.quietSession("mac", "q1") {
		t.Fatal("what the snapshot no longer has")
	}
}

// The node's word on a failed turn comes first (a Codex turn's error is no words of its answer, #2185), and only
// while the agent has stopped.
func TestTurnErrorFromNode(t *testing.T) {
	codex := proto.Session{State: proto.StateUnread, LastText: "我先跑一下测试", TurnError: "unexpected status 503 Service Unavailable",
		TurnErrorKind: proto.TurnErrorTransient}
	if got, kind := turnError(codex); got != codex.TurnError || kind != proto.TurnErrorTransient {
		t.Errorf("stopped: %q, %q", got, kind)
	}
	codex.State = proto.StateIdle
	if got, _ := turnError(codex); got != codex.TurnError {
		t.Errorf("seen: %q", got)
	}
	codex.State = proto.StateWorking // it took the "Continue" up: the old error is gone
	if got, kind := turnError(codex); got != "" || kind != "" {
		t.Errorf("working: %q, %q", got, kind)
	}
	// Without the node's word, Claude Code's answer still says it.
	claude := proto.Session{State: proto.StateUnread, LastText: "API Error: 529 Overloaded"}
	if got, kind := turnError(claude); got != claude.LastText || kind != "" {
		t.Errorf("claude: %q, %q", got, kind)
	}
}

func TestTurnError(t *testing.T) {
	for _, tc := range []struct {
		state, text string
		cut         bool
	}{
		{proto.StateUnread, "API Error: Connection lost mid-response.", true},
		{proto.StateIdle, "  API Error: 529 Overloaded", true},
		{proto.StateUnread, "There's an issue with the selected model (x). It may not exist.", true},
		{proto.StateUnread, "修好了 API Error: Connection lost 时的崩溃", false},
		{proto.StateUnread, "修好了", false},
		{proto.StateWorking, "API Error: Connection lost mid-response.", false},
		{proto.StateNeedsYou, "API Error: Connection lost mid-response.", false},
	} {
		got, kind := turnError(proto.Session{State: tc.state, LastText: tc.text})
		if (got != "") != tc.cut || kind != "" {
			t.Errorf("turnError(%s, %q) = %q, %q", tc.state, tc.text, got, kind)
		}
	}
}
