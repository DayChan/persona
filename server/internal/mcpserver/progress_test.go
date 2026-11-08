package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitingNode is a node whose tool calls wait until release is closed (the phone not answering), or until their
// caller hangs up.
func waitingNode(t *testing.T) (release func()) {
	t.Helper()
	oldDial, oldEvery := dialNode, progressEvery
	t.Cleanup(func() { dialNode, progressEvery = oldDial, oldEvery })
	progressEvery = 2 * time.Millisecond
	ch := make(chan struct{})
	dialNode = func(ctx context.Context, _ any) ([]byte, error) {
		select {
		case <-ch:
			return []byte(`{"content":[{"type":"text","text":"saved"}]}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// serve runs Serve on a pipe: send writes a line to it, out is what it wrote, end closes its input and waits.
func serve(t *testing.T) (send func(string), out *syncBuf, end func()) {
	t.Helper()
	in, w := io.Pipe()
	out = &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), "test", in, out) }()
	send = func(s string) { _, _ = io.WriteString(w, s+"\n") }
	end = func() {
		_ = w.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	return send, out, end
}

type progressLine struct {
	Method string `json:"method"`
	Params struct {
		ProgressToken json.RawMessage `json:"progressToken"`
		Progress      float64         `json:"progress"`
		Message       string          `json:"message"`
	} `json:"params"`
	ID json.RawMessage `json:"id"`
}

// lines decodes what Serve wrote, one JSON-RPC message per line.
func lines(t *testing.T, out string) []progressLine {
	t.Helper()
	var all []progressLine
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var p progressLine
		if err := json.Unmarshal([]byte(l), &p); err != nil {
			t.Fatalf("not JSON: %q: %v", l, err)
		}
		all = append(all, p)
	}
	return all
}

func progressCount(t *testing.T, out *syncBuf) int {
	n := 0
	for _, l := range lines(t, out.String()) {
		if l.Method == "notifications/progress" {
			n++
		}
	}
	return n
}

// A tool call that waits on the user longer than the client's idle limit (a phone approval, ask_user) says it is
// alive with notifications/progress under the token the client gave, growing each time, and stops once it answers
// (#1498: without them Claude Code aborted the call after 30 minutes and the hub's own timeout reply was lost).
func TestServeSendsProgressWhileACallWaits(t *testing.T) {
	release := waitingNode(t)
	send, out, end := serve(t)
	send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"phone_reminders_create","arguments":{},"_meta":{"progressToken":"tok-7"}}}`)
	waitFor(t, "three progress lines", func() bool { return progressCount(t, out) >= 3 })
	release()
	waitFor(t, "the answer", func() bool { return strings.Contains(out.String(), `"id":7`) })
	time.Sleep(20 * progressEvery) // a beat that was due must not come after the answer
	end()

	all := lines(t, out.String())
	last := all[len(all)-1]
	if string(last.ID) != "7" || !strings.Contains(out.String(), `"saved"`) {
		t.Fatalf("the answer is not the last line: %s", out.String())
	}
	prev := 0.0
	for _, l := range all[:len(all)-1] {
		if l.Method != "notifications/progress" || string(l.Params.ProgressToken) != `"tok-7"` || l.ID != nil {
			t.Fatalf("not a progress line for tok-7: %+v", l)
		}
		if l.Params.Progress <= prev {
			t.Fatalf("progress %v after %v: it must grow", l.Params.Progress, prev)
		}
		prev = l.Params.Progress
	}
}

// A call without a token gets no progress (MCP: progress only for a token the client asked with), and neither does
// any other request.
func TestServeSendsNoProgressWithoutAToken(t *testing.T) {
	release := waitingNode(t)
	send, out, end := serve(t)
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{}}}`)
	time.Sleep(25 * progressEvery)
	release()
	waitFor(t, "the answer", func() bool { return strings.Contains(out.String(), `"id":1`) })
	end()
	if n := progressCount(t, out); n != 0 {
		t.Fatalf("%d progress lines without a token: %s", n, out.String())
	}
}

// A call the client cancels stops its progress with its answer: nothing more is written for it.
func TestServeStopsProgressWhenACallIsCancelled(t *testing.T) {
	waitingNode(t)
	send, out, end := serve(t)
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_user","arguments":{},"_meta":{"progressToken":42}}}`)
	waitFor(t, "progress", func() bool { return progressCount(t, out) >= 1 })
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3,"reason":"interrupted"}}`)
	send(`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	waitFor(t, "ping", func() bool { return strings.Contains(out.String(), `"id":4`) })
	time.Sleep(10 * progressEvery)
	n := progressCount(t, out)
	time.Sleep(20 * progressEvery)
	end()
	if m := progressCount(t, out); m != n {
		t.Fatalf("progress went on after the cancel: %d, then %d", n, m)
	}
	if strings.Contains(out.String(), `"id":3`) {
		t.Fatalf("a cancelled call was answered: %s", out.String())
	}
	for _, l := range lines(t, out.String()) {
		if l.Method == "notifications/progress" && string(l.Params.ProgressToken) != "42" {
			t.Fatalf("progress token %s, want the number 42 as given", l.Params.ProgressToken)
		}
	}
}

func TestProgressToken(t *testing.T) {
	cases := map[string]string{
		`{"name":"x","_meta":{"progressToken":"a1"}}`: `"a1"`,
		`{"name":"x","_meta":{"progressToken":17}}`:   `17`,
		`{"name":"x","_meta":{"progressToken":null}}`: ``,
		`{"name":"x","_meta":{"progressToken":{}}}`:   ``,
		`{"name":"x","_meta":{"progressToken":true}}`: ``,
		`{"name":"x","_meta":{}}`:                     ``,
		`{"name":"x"}`:                                ``,
		`"not an object"`:                             ``,
	}
	for params, want := range cases {
		got := progressToken(request{Method: "tools/call", Params: json.RawMessage(params)})
		if string(got) != want {
			t.Errorf("%s: token %q, want %q", params, got, want)
		}
	}
	if got := progressToken(request{Method: "tools/list", Params: json.RawMessage(`{"_meta":{"progressToken":"a"}}`)}); got != nil {
		t.Errorf("tools/list: token %q, want none", got)
	}
}
