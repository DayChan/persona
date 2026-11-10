package node

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const decidedSession = "33333333-3333-4333-8333-333333333333"

func decidedInput(cmd string) hookInput {
	b, _ := json.Marshal(map[string]any{"command": cmd, "description": "remove the views"})
	return hookInput{HookEventName: "PermissionRequest", SessionID: decidedSession, ToolName: "Bash", ToolInput: b,
		PermissionMode: "bypassPermissions"}
}

func runPermissionHook(t *testing.T, n *Node, in hookInput) (out any, took time.Duration) {
	t.Helper()
	raw, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	out, err := n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a1"})
	if err != nil {
		t.Fatal(err)
	}
	return out, time.Since(start)
}

// A call another hook answered (#2648) gets no card: on a machine where hooks say so, a card waits a moment first.
func TestAnotherHookAnswersFirst(t *testing.T) {
	defer func(g time.Duration) { permissionGrace = g }(permissionGrace)
	permissionGrace = 300 * time.Millisecond
	n := codexTestNode(t)
	fh := withFakeHub(t, n)

	// No hook has said anything here: the card goes out at once, as before.
	if out, took := runPermissionHook(t, n, decidedInput("ls")); out == nil || took > 200*time.Millisecond || len(fh.interactions()) != 1 {
		t.Fatalf("before any hook.decided: out %v after %v, %d cards", out, took, len(fh.interactions()))
	}

	// Another hook answers while the card waits: nothing is shown, nothing is answered.
	n.decided.decide(decidedInput("echo earlier"), time.Now())
	in := decidedInput("cd view && rm -f sub/*")
	go func() {
		time.Sleep(20 * time.Millisecond)
		// The same input, written with other spacing: the key compacts it.
		other := in
		other.ToolInput = json.RawMessage(`{ "command": "cd view && rm -f sub/*", "description": "remove the views" }`)
		n.decided.decide(other, time.Now())
	}()
	out, took := runPermissionHook(t, n, in)
	if out != nil || len(fh.interactions()) != 1 || took >= permissionGrace {
		t.Fatalf("answered by another hook: out %v after %v, %d cards", out, took, len(fh.interactions()))
	}
	// That answer was the card's: the same call asked again is a new call, and nobody answers it (#4646).
	if out, took := runPermissionHook(t, n, in); out == nil || took < permissionGrace || len(fh.interactions()) != 2 {
		t.Fatalf("asked again after the answer was used: out %v after %v, %d cards", out, took, len(fh.interactions()))
	}
	// An answer that came before the card asked: no card either.
	n.decided.decide(in, time.Now())
	if out, _ := runPermissionHook(t, n, in); out != nil || len(fh.interactions()) != 2 {
		t.Fatalf("answered before: out %v, %d cards", out, len(fh.interactions()))
	}
	// Nobody answers this one: after the grace the card goes out.
	if out, took := runPermissionHook(t, n, decidedInput("rm -rf \"$t\"")); out == nil || took < permissionGrace || len(fh.interactions()) != 3 {
		t.Fatalf("not answered: out %v after %v, %d cards", out, took, len(fh.interactions()))
	}
	// Another subagent's call with the same input is another call.
	sub := in
	sub.AgentID = "a1b2"
	if out, _ := runPermissionHook(t, n, sub); out == nil || len(fh.interactions()) != 4 {
		t.Fatalf("a subagent's: out %v, %d cards", out, len(fh.interactions()))
	}
}

// #4646: another hook answered a call once; the same call, asked again within decidedKeep, still goes to the phone,
// and so does every call the answer does not name (another session, another input). The phone's own allow is not
// remembered either: the next same call asks again.
func TestAnotherHooksAnswerIsUsedOnce(t *testing.T) {
	defer func(g time.Duration) { permissionGrace = g }(permissionGrace)
	permissionGrace = 10 * time.Millisecond
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	in := decidedInput("npm test")

	n.decided.decide(in, time.Now())
	otherSession := in
	otherSession.SessionID = "44444444-4444-4444-8444-444444444444"
	otherInput := decidedInput("npm test -- --watch")
	for name, c := range map[string]hookInput{"another session": otherSession, "another input": otherInput} {
		before := len(fh.interactions())
		if out, _ := runPermissionHook(t, n, c); out == nil || len(fh.interactions()) != before+1 {
			t.Fatalf("%s: out %v, %d cards", name, out, len(fh.interactions()))
		}
	}
	cards := len(fh.interactions())
	// The answer is still there for the call it names, and goes to it.
	if out, _ := runPermissionHook(t, n, in); out != nil || len(fh.interactions()) != cards {
		t.Fatalf("the answered call: out %v, %d cards", out, len(fh.interactions()))
	}
	// The next same call is not that one: it goes to the phone, and the phone's allow is the hook's output.
	for i := 1; i <= 2; i++ {
		if out, _ := runPermissionHook(t, n, in); out == nil || len(fh.interactions()) != cards+i {
			t.Fatalf("same call again (%d): out %v, %d cards", i, out, len(fh.interactions()))
		}
	}
}

// Two same calls wait for the phone and another hook answers one: only the earliest card is withdrawn.
func TestAnotherHookAnswersOneOfTwo(t *testing.T) {
	defer func(g time.Duration) { permissionGrace = g }(permissionGrace)
	permissionGrace = 10 * time.Millisecond
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	release := make(chan struct{})
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { <-release; yes := true; return proto.Resolution{Allow: &yes} }
	fh.mu.Unlock()
	in := decidedInput("npm test")
	first, second := make(chan any, 1), make(chan any, 1)
	waitCards := func(k int) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); len(fh.interactions()) < k; {
			if time.Now().After(deadline) {
				t.Fatalf("%d cards, want %d", len(fh.interactions()), k)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	go func() { out, _ := runPermissionHook(t, n, in); first <- out }()
	waitCards(1)
	go func() { out, _ := runPermissionHook(t, n, in); second <- out }()
	waitCards(2)

	n.decided.decide(in, time.Now())
	select {
	case out := <-first:
		if out != nil {
			t.Fatalf("withdrawn card answered: %v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the earliest card was not withdrawn")
	}
	select {
	case out := <-second:
		t.Fatalf("the second card was withdrawn too: %v", out)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case out := <-second:
		if out == nil {
			t.Fatal("the second card's allow from the phone was dropped")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second card never got the phone's answer")
	}
}

// A card already on the phone when the other hook's word comes is withdrawn.
func TestAnotherHookAnswersAfterTheCard(t *testing.T) {
	defer func(g time.Duration) { permissionGrace = g }(permissionGrace)
	permissionGrace = 10 * time.Millisecond
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	release := make(chan struct{})
	defer close(release)
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { <-release; return proto.Resolution{} }
	fh.mu.Unlock()
	in := decidedInput("cd view && rm -f sub/*")
	done := make(chan any, 1)
	go func() { out, _ := runPermissionHook(t, n, in); done <- out }()
	for deadline := time.Now().Add(3 * time.Second); len(fh.interactions()) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("no card")
		}
		time.Sleep(5 * time.Millisecond)
	}
	n.decided.decide(in, time.Now())
	select {
	case out := <-done:
		if out != nil {
			t.Fatalf("withdrawn card answered: %v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the hook still waits for the phone")
	}
}

// hook.decided on the socket: only a PermissionRequest of a real session counts.
func TestHookDecidedOnTheSocket(t *testing.T) {
	n := codexTestNode(t)
	send := func(in hookInput) map[string]any {
		t.Helper()
		a, b := net.Pipe()
		defer a.Close()
		go n.serveSock(context.Background(), b)
		raw, _ := json.Marshal(in)
		line, _ := json.Marshal(sockRequest{Kind: "hook.decided", Input: raw})
		_, _ = a.Write(append(line, '\n'))
		resp, err := bufio.NewReader(a).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(resp, &m)
		return m
	}
	bad := decidedInput("ls")
	bad.HookEventName = "PreToolUse"
	kept := func(in hookInput) int {
		n.decided.mu.Lock()
		defer n.decided.mu.Unlock()
		return len(n.decided.unused[permissionKey(in)])
	}
	if r := send(bad); r["ok"] != true || kept(bad) != 0 {
		t.Fatalf("a PreToolUse counted: %v", r)
	}
	in := decidedInput("ls")
	if r := send(in); r["ok"] != true || kept(in) != 1 {
		t.Fatalf("not recorded: %v", r)
	}
}

func TestDecisionsForget(t *testing.T) {
	var d decisions
	old, now := decidedInput("old"), time.Now()
	d.decide(old, now.Add(-decidedKeep-time.Second))
	d.decide(decidedInput("new"), now)
	answered := func(in hookInput) bool {
		_, ok, stop := d.watch(context.Background(), permissionKey(in))
		defer stop()
		return ok()
	}
	if answered(old) || !answered(decidedInput("new")) {
		t.Fatal("old answers are forgotten, new ones kept")
	}
	if answered(decidedInput("new")) {
		t.Fatal("an answer is used once")
	}
}

// AskUserQuestion waits in PermissionRequest, after PreToolUse has let Claude draw the terminal dialog (#2382).
// Standby holds that hook for the phone just as it holds other permission requests.
func TestQuestionPermissionRequestWaitsForPhone(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which color?": "Blue"}}
	}
	q, _ := json.Marshal(map[string]any{"questions": []any{map[string]any{"question": "Which color?", "header": "Color",
		"options": []any{map[string]any{"label": "Red"}, map[string]any{"label": "Blue"}}}}})
	in := hookInput{HookEventName: "PermissionRequest", SessionID: decidedSession, ToolName: "AskUserQuestion", ToolInput: q,
		PermissionMode: "bypassPermissions"}
	out, took := runPermissionHook(t, n, in)
	if out == nil || len(fh.interactions()) != 1 || took > time.Second {
		t.Fatalf("out %v after %v, %d cards: the phone could not answer", out, took, len(fh.interactions()))
	}
	if !holdsForPhone(in, sockRequest{Attempt: "a1"}) {
		t.Fatal("standby does not hold it for the phone")
	}
	// Any other tool's permission still asks the phone.
	fh.answer = func(proto.Interaction) proto.Resolution { yes := true; return proto.Resolution{Allow: &yes} }
	if out, _ := runPermissionHook(t, n, decidedInput("ls")); out == nil || len(fh.interactions()) != 2 {
		t.Fatalf("a Bash permission: out %v, %d cards", out, len(fh.interactions()))
	}
}
