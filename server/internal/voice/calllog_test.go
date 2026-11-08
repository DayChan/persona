package voice

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// logCapture is what a plugin logs, kept for a test to read.
type logCapture struct {
	t  *testing.T
	mu sync.Mutex
	b  strings.Builder
}

func (l *logCapture) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// lines are the call lines logged so far, without the test's prefix.
func (l *logCapture) lines() []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(l.String()), "\n") {
		if _, rest, ok := strings.Cut(line, ": "); ok {
			out = append(out, rest)
		}
	}
	return out
}

// wait waits up to 10 s for a line that has every one of want.
func (l *logCapture) wait(want ...string) string {
	l.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, line := range l.lines() {
			if hasAll(line, want) {
				return line
			}
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("no line with %q in the log:\n%s", want, l.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func hasAll(s string, parts []string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// captureLog keeps what pl logs from now on (and shows it too with VOICE_TEST_LOG).
func captureLog(t *testing.T, pl *Plugin) *logCapture {
	l := &logCapture{t: t}
	var w io.Writer = l
	if os.Getenv("VOICE_TEST_LOG") != "" {
		w = io.MultiWriter(l, os.Stderr)
	}
	pl.logger.SetOutput(w)
	return l
}

// A call with one handover logs how it started (protocol, target, model, tools), the upstream session, the function
// call and its delegation, the gate's decision, the run's states and its send, and how the call ended with its counts,
// every line with the call's id; and nothing of what was said, typed, handed over or answered (#1485).
func TestCallLogOneHandover(t *testing.T) {
	const (
		typed   = "zebra-typed-41 please"
		caption = "zebra-caption-52 please"
		reply   = "ocelot-reply-13 on it"
		task    = "quokka-task-77 fix the login tests"
		answer  = "narwhal-answer-29 the tests pass now"
	)
	hub := agentHub(t)
	hub.reply = func(string) string { return answer }
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.commentary.append":
			if f["delegation_id"] != nil || f["event_id"] != nil {
				return
			}
			u.send(c, map[string]any{"type": "session.input_transcript.delta", "delta": caption})
			u.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": reply})
			u.send(c, map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "dlg1", "target": "client"}})
			u.send(c, map[string]any{"type": "response.event", "delegation_id": "dlg1", "event": map[string]any{
				"type": "function_call", "call_id": "c1", "name": "delegate_task", "arguments": fmt.Sprintf(`{"task":%q}`, task),
			}})
		}
	})
	pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": typed})
	m := hub.waitSent(1)
	if !strings.Contains(m.Text, task) {
		t.Fatalf("sent %+v", m)
	}
	a.runStates(m.ID, runDone)
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, answer) {
		t.Fatalf("result: %s", got)
	}
	_ = a.c.Close(websocket.StatusNormalClosure, "bye")
	end := logs.wait("call 1: conversation closed")

	for _, want := range [][]string{
		{`call 1: conversation opened (role "")`},
		{"call 1: start: protocol 2, model rt/m1 (live dialect)", "target m/a1"},
		{"call 1: bound: agent m/a1, its machine online"},
		{"call 1: upstream: session 1 started in", "(first)", "tools search_knowledge,list_agents,delegate_task (bound)"},
		{"call 1: delegation dlg1: made in session 1"},
		{"call 1: function call delegate_task (call c1, delegation dlg1, session 1)"},
		{"call 1: delegation dlg1: a task for agent m/a1", "characters"},
		{"call 1: delegation dlg1: the user-speech gate let it through (typed line)"},
		{"call 1: run " + m.ID + ": for delegation dlg1, in session 1"},
		{"call 1: run " + m.ID + ": sending"},
		{"call 1: run " + m.ID + ": sent to m/a1: ok in"},
		{"call 1: run " + m.ID + ": done"},
	} {
		logs.wait(want...)
	}
	for _, want := range []string{"(the app hung up (1000))", "delegations 1", "function calls 1", "runs 1 (done 1)", "typed 1"} {
		if !strings.Contains(end, want) {
			t.Errorf("the call's last line lacks %q: %s", want, end)
		}
	}
	for _, line := range logs.lines() {
		if !strings.HasPrefix(line, "call 1: ") && !strings.HasPrefix(line, "hub link: ") {
			t.Errorf("a line without the call's id: %s", line)
		}
	}
	all := logs.String()
	for _, words := range []string{"zebra-typed", "zebra-caption", "ocelot-reply", "quokka-task", "narwhal-answer", "login tests"} {
		if strings.Contains(all, words) {
			t.Errorf("the log has %q:\n%s", words, all)
		}
	}
}

// A handover the user did not ask for is refused by the user-speech gate: the log says so with its code, the run is
// refused (unprompted), and the task's words are not in it.
func TestCallLogRefusedHandover(t *testing.T) {
	setVar(t, &gateWait, 300*time.Millisecond)
	const task = "quokka-task-77 run npm publish"
	hub := agentHub(t)
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] != "session.start" {
			return
		}
		u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		delegate(u, c, "dlg1", task) // nobody spoke
	})
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	refused := a.until(runIn(runRefused))
	if refused["code"] != "unprompted" {
		t.Fatalf("refused: %v", refused)
	}
	id, _ := refused["id"].(string)
	logs.wait("call 1: delegation dlg1: a task for agent m/a1")
	logs.wait("call 1: delegation dlg1: the user-speech gate refused it (unprompted) after")
	logs.wait("call 1: run " + id + ": refused (unprompted)")
	_ = a.c.Close(websocket.StatusNormalClosure, "bye")
	if end := logs.wait("call 1: conversation closed"); !strings.Contains(end, "runs 1 (refused 1)") {
		t.Errorf("the call's last line: %s", end)
	}
	if msgs := hub.messages(); len(msgs) != 0 {
		t.Fatalf("sent %+v", msgs)
	}
	if all := logs.String(); strings.Contains(all, "quokka-task") || strings.Contains(all, "npm publish") {
		t.Errorf("the log has the task:\n%s", all)
	}
}

// Calls at the same time are told apart by their ids, and a start that fails says why with its code.
func TestCallLogTellsCallsApart(t *testing.T) {
	hub := agentHub(t)
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	logs := captureLog(t, pl)
	one := dialApp(t, pl, boundStart("a1", nil))
	two := dialApp(t, pl, map[string]any{"type": "start"})
	one.until(ofType("ready"))
	two.until(ofType("ready"))
	gone := dialApp(t, pl, boundStart("nope", nil))
	if f := gone.until(ofType("error")); f["code"] != codeTargetGone {
		t.Fatalf("gone: %v", f)
	}
	_ = one.c.Close(websocket.StatusNormalClosure, "bye")
	_ = two.c.Close(websocket.StatusNormalClosure, "bye")
	failed := logs.wait("start failed after", ": target_gone")
	logs.wait("fatal error target_gone")
	starts := map[string]string{}
	for _, n := range []string{"1", "2", "3"} {
		line := logs.wait("call " + n + ": conversation closed")
		if strings.Contains(line, "(start failed: target_gone)") != strings.HasPrefix(failed, "call "+n+":") {
			t.Errorf("call %s ended: %s", n, line)
		}
		for _, l := range logs.lines() {
			if strings.HasPrefix(l, "call "+n+": start: ") {
				starts[n] = l
			}
		}
	}
	var p1, p2 int
	for _, l := range starts {
		switch {
		case strings.Contains(l, "protocol 1,") && strings.Contains(l, "target the main conversation of the moment"):
			p1++
		case strings.Contains(l, "protocol 2,") && strings.Contains(l, "target m/"):
			p2++
		}
	}
	if p1 != 1 || p2 != 2 {
		t.Errorf("starts %v", starts)
	}
}
