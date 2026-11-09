package toolcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// fakeNode answers the calls: a connector "example" whose example_cli plans `ex list [--limit N]`, `ex say <text|->`
// and shows list's answer as text; example_list lists; example_say is a write the gate declines when told to.
type fakeNode struct {
	calls   []string
	decline bool
	limited bool
}

func (f *fakeNode) call(_ context.Context, name string, args any) (proto.ToolResult, error) {
	b, _ := json.Marshal(args)
	f.calls = append(f.calls, name+" "+string(b))
	var in map[string]any
	_ = json.Unmarshal(b, &in)
	text := func(v any) (proto.ToolResult, error) {
		out, _ := json.Marshal(v)
		return proto.TextResult(string(out), false), nil
	}
	switch name {
	case "example_cli":
		if r, ok := in["render"]; ok {
			return text(map[string]any{"stdout": "rendered " + r.(string) + ": " + in["result"].(string)})
		}
		argv, _ := in["argv"].([]any)
		if len(argv) == 0 || argv[0] == "--help" {
			code := 0
			if len(argv) == 0 {
				code = 2
			}
			return text(map[string]any{"name": "ex", "exit": code, "stdout": "ex list | ex say <text|->"})
		}
		switch argv[0] {
		case "list":
			return text(map[string]any{"name": "ex", "call": map[string]any{"tool": "example_list", "args": map[string]any{"limit": 5}}, "render": "list"})
		case "say":
			words := argv[1].(string)
			if words == "-" {
				words = in["stdin"].(string)
			}
			return text(map[string]any{"name": "ex", "call": map[string]any{"tool": "example_say", "args": map[string]any{"text": words}}})
		}
		return text(map[string]any{"name": "ex", "exit": 2, "stderr": "no command " + argv[0].(string)})
	case "example_list":
		if f.limited {
			return proto.TextResult("Telegram 限流中，37 秒后再试", true), nil
		}
		return proto.TextResult(`{"items":[1,2]}`, false), nil
	case "example_say":
		if f.decline {
			return proto.TextResult("The user declined this on the phone. Do not retry it unless they ask.", true), nil
		}
		return proto.TextResult(`{"ok":true}`, false), nil
	case "down_cli":
		return proto.ToolResult{}, errors.New("no socket")
	}
	return proto.TextResult(name+" is not available: the command line calls only the tools of the hub's connectors that are signed in", true), nil
}

// realCall is the package's call, which the fakes replace.
var realCall = call

func run(t *testing.T, f *fakeNode, stdin string, args ...string) (int, string, string) {
	t.Helper()
	t.Cleanup(func() { call = realCall })
	call = f.call
	var out, errOut bytes.Buffer
	code := Main(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// A connector's command line: the words planned by its <id>_cli, the tool it names called (so the gate sees that
// tool), the answer as JSON or shown by the connector as text; its usage and mistakes come from the plan.
func TestCommandLine(t *testing.T) {
	f := &fakeNode{}
	if code, out, _ := run(t, f, "", "--cli", "example", "list"); code != 0 || out != "{\"items\":[1,2]}\n" {
		t.Fatalf("json: %d %q", code, out)
	}
	if got := strings.Join(f.calls, "\n"); got != "example_cli {\"argv\":[\"list\"]}\nexample_list {\"limit\":5}" {
		t.Fatalf("calls:\n%s", got)
	}
	f.calls = nil
	if code, out, _ := run(t, f, "", "--cli", "example", "--format", "text", "list"); code != 0 || out != "rendered list: {\"items\":[1,2]}\n" {
		t.Fatalf("text: %d %q", code, out)
	}
	if len(f.calls) != 3 || !strings.HasPrefix(f.calls[2], `example_cli {"render":"list"`) {
		t.Fatalf("shown by the connector: %v", f.calls)
	}
	// The text of a write from stdin.
	f.calls = nil
	if code, _, _ := run(t, f, "line one\nline two", "--cli", "example", "say", "-"); code != 0 || f.calls[1] != `example_say {"text":"line one\nline two"}` {
		t.Fatalf("stdin: %d %v", code, f.calls)
	}
	// The plan's own answers: help, usage, a mistake (as JSON on stderr, or "ex: …" for people).
	if code, out, _ := run(t, f, "", "--cli", "example", "--help"); code != 0 || !strings.Contains(out, "ex list") {
		t.Fatalf("help: %d %q", code, out)
	}
	if code, out, _ := run(t, f, "", "--cli", "example"); code != 2 || !strings.Contains(out, "ex list") {
		t.Fatalf("usage: %d %q", code, out)
	}
	if code, _, errOut := run(t, f, "", "--cli", "example", "nope"); code != 2 || errOut != "{\"error\":\"no command nope\",\"kind\":\"usage\"}\n" {
		t.Fatalf("mistake: %d %q", code, errOut)
	}
	if code, _, errOut := run(t, f, "", "--cli", "example", "--format", "text", "nope"); code != 2 || errOut != "ex: no command nope\n" {
		t.Fatalf("mistake for people: %d %q", code, errOut)
	}
	// Failures by kind: declined by the user, rate limited (and for how long), a connector not signed in, no node.
	f.decline = true
	if code, _, errOut := run(t, f, "", "--cli", "example", "say", "hi"); code != exitDeclined || !strings.Contains(errOut, `"kind":"declined"`) {
		t.Fatalf("declined: %d %q", code, errOut)
	}
	f.limited = true
	if code, _, errOut := run(t, f, "", "--cli", "example", "list"); code != exitRateLimited || !strings.Contains(errOut, `"retry_after":37`) {
		t.Fatalf("limited: %d %q", code, errOut)
	}
	if code, _, _ := run(t, f, "", "--cli", "gone", "list"); code != exitUnavailable {
		t.Fatalf("not signed in: %d", code)
	}
	if code, _, _ := run(t, f, "", "--cli", "down", "list"); code != exitUnavailable {
		t.Fatalf("no node: %d", code)
	}
	if code, _, _ := run(t, f, "", "--cli", "example", "--format", "yaml", "list"); code != exitUsage {
		t.Fatalf("format: %d", code)
	}
}

// One tool by name, its arguments as JSON (or from stdin).
func TestOneTool(t *testing.T) {
	f := &fakeNode{}
	if code, out, _ := run(t, f, "", "example_list", `{"limit":5}`); code != 0 || out != "{\"items\":[1,2]}\n" || f.calls[0] != `example_list {"limit":5}` {
		t.Fatalf("%d %q %v", code, out, f.calls)
	}
	if code, _, _ := run(t, f, `{"text":"hi"}`, "example_say", "-"); code != 0 || f.calls[1] != `example_say {"text":"hi"}` {
		t.Fatalf("stdin: %d %v", code, f.calls)
	}
	if code, _, errOut := run(t, f, "", "example_list", "not json"); code != exitUsage || !strings.Contains(errOut, "not JSON") {
		t.Fatalf("bad args: %d %q", code, errOut)
	}
	f.calls = nil
	if code, out, errOut := run(t, f, ""); code != exitUsage || out != "" || failureKind(errOut) != "usage" || !strings.Contains(errOut, "personad tool <tool>") || len(f.calls) != 0 {
		t.Fatalf("nothing: %d stdout %q stderr %q calls %v", code, out, errOut, f.calls)
	}
}

// failureKind is the kind of a failure written as the one JSON line on stderr ("" when stderr is not exactly that).
func failureKind(stderr string) string {
	line, ok := strings.CutSuffix(stderr, "\n")
	var got struct {
		Error string `json:"error"`
		Kind  string `json:"kind"`
	}
	if !ok || strings.Contains(line, "\n") || json.Unmarshal([]byte(line), &got) != nil || got.Error == "" {
		return ""
	}
	return got.Kind
}

// Usage mistakes are the documented failure on stderr (one JSON line of kind usage, or "<name>: …" with --format text),
// exit 2, and reach no node; help is the usage on stdout, exit 0.
func TestUsageMistakes(t *testing.T) {
	for _, args := range [][]string{
		{},
		{""},
		{"--bogus"},
		{"--cli"},
		{"--cli", ""},
		{"--cli", "--format", "json"},
		{"--cli", "example", "list", "--format"},
		{"--cli", "example", "--format=", "list"},
		{"example_list", "[1]"},
		{"example_list", `"s"`},
		{"example_list", "null"},
		{"example_list", "7"},
		{"example_list", "not json"},
		{"example_list", "{}", "{}"},
	} {
		f := &fakeNode{}
		if code, out, errOut := run(t, f, "", args...); code != exitUsage || out != "" || failureKind(errOut) != "usage" || len(f.calls) != 0 {
			t.Errorf("%q: exit %d stdout %q stderr %q calls %v, want exit 2, one usage line on stderr, no call", args, code, out, errOut, f.calls)
		}
	}
	// Stdin that is not one JSON object is a mistake too.
	f := &fakeNode{}
	if code, _, errOut := run(t, f, "[1]", "example_list", "-"); code != exitUsage || failureKind(errOut) != "usage" || len(f.calls) != 0 {
		t.Errorf("stdin: exit %d stderr %q calls %v", code, errOut, f.calls)
	}
	// With --format text the mistake is said for people.
	for args, want := range map[string]string{
		"--cli --format text":                       "personad tool: ",
		"--cli example --format=text list --format": "example: ",
	} {
		f := &fakeNode{}
		code, out, errOut := run(t, f, "", strings.Fields(args)...)
		if code != exitUsage || out != "" || !strings.HasPrefix(errOut, want) || strings.Count(errOut, "\n") != 1 || len(f.calls) != 0 {
			t.Errorf("%s: exit %d stdout %q stderr %q calls %v", args, code, out, errOut, f.calls)
		}
	}
	// Arguments that are a JSON object, spaces around it included, still go through.
	f = &fakeNode{}
	if code, _, errOut := run(t, f, "", "example_list", " \n{\"limit\":5} "); code != exitOK || len(f.calls) != 1 {
		t.Errorf("object: exit %d stderr %q calls %v", code, errOut, f.calls)
	}
	for _, help := range []string{"-h", "--help", "help"} {
		f := &fakeNode{}
		if code, out, errOut := run(t, f, "", help); code != exitOK || !strings.Contains(out, "personad tool --cli <connector>") || errOut != "" || len(f.calls) != 0 {
			t.Errorf("%s: exit %d stdout %q stderr %q", help, code, out, errOut)
		}
	}
}

// Ctrl-C on a write waiting for the phone hangs up on the node, which takes the card off the phone; without it the
// card stayed until it expired.
func TestInterruptHangsUp(t *testing.T) {
	old := dial
	t.Cleanup(func() { dial = old })
	call = realCall
	hungUp := make(chan struct{})
	dial = func(ctx context.Context, req any) ([]byte, error) {
		<-ctx.Done() // the node waits for the phone
		close(hungUp)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var errOut bytes.Buffer
	go func() {
		var out bytes.Buffer
		done <- Main(ctx, []string{"example_say", `{"text":"hi"}`}, strings.NewReader(""), &out, &errOut)
	}()
	cancel()
	select {
	case <-hungUp:
	case <-time.After(3 * time.Second):
		t.Fatal("an interrupted call kept waiting on the node")
	}
	if code := <-done; code != exitInterrupted {
		t.Fatalf("an interrupted call exits %d, want %d (not \"the node is not reachable\")", code, exitInterrupted)
	}
	if e := errOut.String(); !strings.Contains(e, `"kind":"interrupted"`) || strings.Contains(e, "not reachable") {
		t.Fatalf("stderr %q", e)
	}
}

// The command line reaches the node with the dialer that hangs up on a cancelled ctx.
func TestDialsInterruptibly(t *testing.T) {
	if reflect.ValueOf(dial).Pointer() != reflect.ValueOf(nodeclient.Dialer(nodeclient.DialInterruptible)).Pointer() {
		t.Fatal("personad tool must dial with nodeclient.DialInterruptible")
	}
}

// The kind the hub, the node or the connector names decides the exit code, whatever the error's words (#3779): a
// decline in other words, a rate limit with its own retry_after or one said the connector's way, a plain failure.
// With no kind named (an older hub), the words still decide.
func TestKindBeforeWords(t *testing.T) {
	cases := []struct {
		res   proto.ToolResult
		code  int
		retry int
	}{
		{proto.ErrorResult(proto.ToolErrorDeclined, "主人在手机上拒绝了", 0), exitDeclined, 0},
		{proto.ErrorResult(proto.ToolErrorUnavailable, "Telegram 没登录", 0), exitUnavailable, 0},
		{proto.ErrorResult(proto.ToolErrorRateLimited, "too many requests", 30), exitRateLimited, 30},
		{proto.ErrorResult(proto.ToolErrorRateLimited, "Telegram is not available: Telegram 限流，12 秒后再试", 0), exitRateLimited, 12},
		{proto.ErrorResult(proto.ToolErrorRateLimited, "slow down", 0), exitRateLimited, 0},
		{proto.TextResult("The user declined example_say.", true), exitDeclined, 0},
		{proto.TextResult("Telegram 限流，45 秒后再试", true), exitRateLimited, 45},
		{proto.TextResult("chat not found", true), exitFailed, 0},
		{proto.ErrorResult("something_new", "chat not found", 0), exitFailed, 0},
	}
	for _, c := range cases {
		res := c.res
		call = func(context.Context, string, any) (proto.ToolResult, error) { return res, nil }
		var out, errOut bytes.Buffer
		code := Main(context.Background(), []string{"example_list"}, strings.NewReader(""), &out, &errOut)
		var got struct {
			Kind       string `json:"kind"`
			RetryAfter int    `json:"retry_after"`
		}
		_ = json.Unmarshal(errOut.Bytes(), &got)
		if code != c.code || got.RetryAfter != c.retry || got.Kind != kinds[c.code] {
			t.Errorf("%+v: exit %d %q, want %d retry %d", c.res, code, errOut.String(), c.code, c.retry)
		}
	}
}

// stdin over stdinMost fails before any call instead of being cut to stdinMost and sent as if whole; exactly
// stdinMost still goes through.
func TestStdinOverLimit(t *testing.T) {
	text := strings.Repeat("a", stdinMost-len(`{"text":""}`))
	atLimit := `{"text":"` + text + `"}`
	over := `{"text":"` + text + `a"}`

	f := &fakeNode{}
	if code, _, errOut := run(t, f, over, "example_say", "-"); code != exitUsage || len(f.calls) != 0 || !strings.Contains(errOut, "1 MiB") {
		t.Fatalf("json over the limit: %d %q %d calls", code, errOut, len(f.calls))
	}
	if code, _, errOut := run(t, f, strings.Repeat("a", stdinMost+1), "--cli", "example", "say", "-"); code != exitUsage || len(f.calls) != 0 || !strings.Contains(errOut, "1 MiB") {
		t.Fatalf("text over the limit: %d %q %d calls", code, errOut, len(f.calls))
	}

	if code, _, errOut := run(t, f, atLimit, "example_say", "-"); code != exitOK || len(f.calls) != 1 {
		t.Fatalf("json at the limit: %d %q %d calls", code, errOut, len(f.calls))
	}
	f = &fakeNode{}
	if code, _, errOut := run(t, f, strings.Repeat("a", stdinMost), "--cli", "example", "say", "-"); code != exitOK || len(f.calls) != 2 ||
		!strings.Contains(f.calls[1], `"text":"aaa`) || len(f.calls[1]) < stdinMost {
		t.Fatalf("text at the limit: %d %q %d calls", code, errOut, len(f.calls))
	}
}
