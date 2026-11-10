package scriptcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// fake is a node socket that answers with canned lines (no real socket, no phone) and remembers what it was asked.
type fake struct {
	reply string
	err   error
	reqs  []map[string]any
}

func (f *fake) dial(_ context.Context, req any) ([]byte, error) {
	b, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.reqs = append(f.reqs, m)
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.reply + "\n"), nil
}

func (f *fake) runner(tty bool) Runner {
	env := map[string]string{"HERDR_PANE_ID": "w1:p2"}
	return Runner{Dial: f.dial, Getenv: func(k string) string { return env[k] }, Getwd: func() (string, error) { return "/work/app", nil },
		IsTerminal: func(io.Reader) bool { return tty }}
}

type result struct {
	code        int
	out, errOut string
}

func ask(f *fake, tty bool, stdin string, args ...string) result {
	var out, errOut bytes.Buffer
	code := f.runner(tty).Ask(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

func notify(f *fake, args ...string) result {
	var out, errOut bytes.Buffer
	code := f.runner(false).Notify(context.Background(), args, &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

func input(t *testing.T, f *fake) map[string]any {
	t.Helper()
	if len(f.reqs) == 0 {
		t.Fatal("the node was asked nothing")
	}
	in, _ := f.reqs[0]["input"].(map[string]any)
	return in
}

func TestAskExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name, reply, out string
		code             int
	}{
		{"option chosen", `{"status":"answered","answer":"staging"}`, "staging\n", ExitOK},
		{"allowed", `{"status":"answered"}`, "", ExitOK},
		{"denied", `{"status":"denied"}`, "", ExitDenied},
		{"handed back, nobody at the terminal", `{"status":"dismissed"}`, "", ExitDenied},
		{"timeout", `{"status":"timeout"}`, "", ExitTimeout},
		{"hub not connected", `{"status":"unavailable","error":"this node is not connected to a hub"}`, "", ExitUnreachable},
		{"node too old to know ask", `{"error":"unknown kind"}`, "", ExitUnreachable},
		{"rejected by the node", `{"status":"invalid","error":"more than 12 options"}`, "", ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ask(&fake{reply: tc.reply}, false, "", "Deploy where?", "--options", "staging,prod")
			if r.code != tc.code || r.out != tc.out {
				t.Fatalf("exit %d out %q err %q, want exit %d out %q", r.code, r.out, r.errOut, tc.code, tc.out)
			}
		})
	}
}

// The request carries the question, the options, the timeout and the pane; flags may come after the question.
func TestAskRequest(t *testing.T) {
	f := &fake{reply: `{"status":"answered","answer":"b"}`}
	if r := ask(f, false, "", "Which one?", "--options=a, b ,,c", "--timeout", "10m", "--require-remote"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	req := f.reqs[0]
	if req["kind"] != "ask" || req["pane"] != "w1:p2" || req["attempt"] == "" {
		t.Fatalf("request %v", req)
	}
	in := input(t, f)
	opts, _ := in["options"].([]any)
	if in["question"] != "Which one?" || fmt.Sprint(opts) != "[a b c]" || in["timeout_seconds"] != float64(600) || in["cwd"] != "/work/app" {
		t.Fatalf("input %v", in)
	}

	f = &fake{reply: `{"status":"answered"}`}
	ask(f, false, "", "--timeout", "90", "--", "-1 is the question")
	if in = input(t, f); in["question"] != "-1 is the question" || in["timeout_seconds"] != float64(90) {
		t.Fatalf("after --: %v", in)
	}
}

func TestAskUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no question":    {},
		"two questions":  {"a", "b"},
		"empty question": {"  "},
		"bad timeout":    {"q", "--timeout", "soon"},
		"zero timeout":   {"q", "--timeout", "0"},
		"too long":       {"q", "--timeout", "25h"},
		"unknown flag":   {"q", "--loud"},
		"too many opts":  {"q", "--options", "a,b,c,d,e,f,g,h,i,j,k,l,m"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{reply: `{"status":"answered"}`}
			if r := ask(f, true, "y\n", args...); r.code != ExitUsage || len(f.reqs) != 0 {
				t.Fatalf("exit %d, asked %d times; err %q", r.code, len(f.reqs), r.errOut)
			}
		})
	}
	if r := ask(&fake{}, false, "", "--help"); r.code != ExitOK || !strings.Contains(r.out, "usage: personad ask") {
		t.Fatalf("help: %+v", r)
	}
}

// With no node (or no hub) and a person at the terminal, the question is asked there; --require-remote never does.
func TestAskFallsBackToTheTerminal(t *testing.T) {
	down := func() *fake { return &fake{err: fmt.Errorf("%w: no such file", nodeclient.ErrNoNode)} }

	r := ask(down(), true, "2\n", "Which?", "--options", "a,b")
	if r.code != ExitOK || r.out != "b\n" || !strings.Contains(r.errOut, "1) a") {
		t.Fatalf("by number: %+v", r)
	}
	if r = ask(down(), true, "nope\nA\n", "Which?", "--options", "a,b"); r.code != ExitOK || r.out != "a\n" {
		t.Fatalf("by label, after a miss: %+v", r)
	}
	if r = ask(down(), true, "", "Which?", "--options", "a,b"); r.code != ExitDenied || r.out != "" {
		t.Fatalf("end of input: %+v", r)
	}
	if r = ask(down(), true, "y\n", "Go on?"); r.code != ExitOK || r.out != "" {
		t.Fatalf("yes: %+v", r)
	}
	if r = ask(down(), true, "\n", "Go on?"); r.code != ExitDenied {
		t.Fatalf("default is no: %+v", r)
	}
	if r = ask(down(), true, "n\n", "Go on?"); r.code != ExitDenied || r.out != "" {
		t.Fatalf("no: %+v", r)
	}
	if r = ask(down(), true, "", "Go on?"); r.code != ExitDenied || r.out != "" {
		t.Fatalf("end of input at [y/N]: %+v", r)
	}
	// Nobody to type: unreachable, say so.
	if r = ask(down(), false, "y\n", "Go on?"); r.code != ExitUnreachable || !strings.Contains(r.errOut, "Persona node") {
		t.Fatalf("not a terminal: %+v", r)
	}
	if r = ask(down(), true, "y\n", "Go on?", "--require-remote"); r.code != ExitUnreachable {
		t.Fatalf("--require-remote: %+v", r)
	}
	// "Hand back to computer" on the phone is the same hand back.
	handed := &fake{reply: `{"status":"dismissed"}`}
	if r = ask(handed, true, "y\n", "Go on?"); r.code != ExitOK {
		t.Fatalf("handed back to the terminal: %+v", r)
	}
	if r = ask(&fake{reply: `{"status":"dismissed"}`}, true, "y\n", "Go on?", "--require-remote"); r.code != ExitDenied {
		t.Fatalf("handed back with --require-remote: %+v", r)
	}
	// A timeout or a denial is an answer: no second try at the terminal.
	if r = ask(&fake{reply: `{"status":"timeout"}`}, true, "y\n", "Go on?"); r.code != ExitTimeout {
		t.Fatalf("timeout: %+v", r)
	}
	if r = ask(&fake{reply: `{"status":"denied"}`}, true, "y\n", "Go on?"); r.code != ExitDenied {
		t.Fatalf("denied: %+v", r)
	}
}

// Ctrl-C ends the wait without falling back to the terminal's prompt.
func TestAskInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fake{}
	r := f.runner(true)
	r.Dial = func(ctx context.Context, _ any) ([]byte, error) {
		cancel()
		return nil, ctx.Err()
	}
	var out, errOut bytes.Buffer
	if code := r.Ask(ctx, []string{"Go on?"}, strings.NewReader("y\n"), &out, &errOut); code != ExitInterrupted || out.Len() != 0 {
		t.Fatalf("exit %d out %q err %q", code, out.String(), errOut.String())
	}
}

// silentTerminal is a terminal nobody types at: Read says it started, then blocks until the test ends.
type silentTerminal struct {
	started, release chan struct{}
	once             sync.Once
}

func (s *silentTerminal) Read([]byte) (int, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return 0, io.EOF
}

// Ctrl-C at the terminal's question ends ask with 130 instead of waiting for a line that never comes.
func TestAskInterruptedAtTheTerminal(t *testing.T) {
	for name, tc := range map[string]struct {
		args   []string
		prompt string
	}{
		"y/N":     {[]string{"deploy?"}, "[y/N] "},
		"options": {[]string{"Deploy where?", "--options", "staging,prod"}, "> "},
	} {
		t.Run(name, func(t *testing.T) {
			in := &silentTerminal{started: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(func() { close(in.release) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fake{err: fmt.Errorf("%w: no such file", nodeclient.ErrNoNode)}
			var out, errOut bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- f.runner(true).Ask(ctx, tc.args, in, &out, &errOut) }()

			deadline := time.After(5 * time.Second)
			select {
			case <-in.started:
			case code := <-done:
				t.Fatalf("ask ended with %d before reading the terminal; err %q", code, errOut.String())
			case <-deadline:
				t.Fatal("ask never read the terminal")
			}
			cancel() // Ctrl-C
			select {
			case code := <-done:
				if code != ExitInterrupted || out.Len() != 0 {
					t.Fatalf("exit %d out %q err %q, want exit %d and no answer", code, out.String(), errOut.String(), ExitInterrupted)
				}
				// The shell's prompt starts on a line of its own, not after the question's.
				if !strings.HasSuffix(errOut.String(), tc.prompt+"\n") {
					t.Fatalf("err %q, want it to end with %q", errOut.String(), tc.prompt+"\n")
				}
			case <-deadline:
				t.Fatal("ask kept waiting at the terminal prompt after Ctrl-C")
			}
		})
	}
}

func TestNotifyBestEffort(t *testing.T) {
	f := &fake{reply: `{"sent":true}`}
	if r := notify(f, "Build done", "all green", "--silent", "--error"); r.code != 0 || r.out != "" || r.errOut != "" {
		t.Fatalf("%+v", r)
	}
	req := f.reqs[0]
	in := input(t, f)
	if req["kind"] != "notify" || req["pane"] != "w1:p2" || in["title"] != "Build done" || in["body"] != "all green" ||
		in["silent"] != true || in["error"] != true {
		t.Fatalf("request %v", req)
	}

	for name, f := range map[string]*fake{
		"no node":      {err: fmt.Errorf("%w: gone", nodeclient.ErrNoNode)},
		"muted":        {reply: `{"sent":false,"reason":"muted"}`},
		"no device":    {reply: `{"sent":false,"reason":"no_device"}`},
		"filtered":     {reply: `{"sent":false,"reason":"filtered"}`},
		"hub refuses":  {reply: `{"sent":false,"error":"unknown method notify"}`},
		"broken reply": {reply: `nonsense`},
	} {
		t.Run(name, func(t *testing.T) {
			if r := notify(f, "t"); r.code != 0 || !strings.Contains(r.errOut, "没发出去") {
				t.Fatalf("best effort: %+v", r)
			}
			if r := notify(f, "t", "--strict"); r.code != 1 || !strings.Contains(r.errOut, "没发出去") {
				t.Fatalf("--strict: %+v", r)
			}
		})
	}
	if r := notify(&fake{reply: `{"sent":true}`}, "t", "--strict", "b"); r.code != 0 {
		t.Fatalf("--strict, sent: %+v", r)
	}
	// The manager is away: the node keeps it. Best effort is content; --strict is not, as nothing was sent yet.
	if r := notify(&fake{reply: `{"sent":false,"queued":true}`}, "t"); r.code != 0 || r.errOut != "" {
		t.Fatalf("queued: %+v", r)
	}
	if r := notify(&fake{reply: `{"sent":false,"queued":true}`}, "t", "--strict"); r.code != 1 || !strings.Contains(r.errOut, "没发出去") {
		t.Fatalf("queued, --strict: %+v", r)
	}
}

func TestNotifyUnsentReasons(t *testing.T) {
	for _, tc := range []struct {
		lang     i18n.Lang
		reason   string
		wantText string
	}{
		{i18n.En, "no_device", "Not sent: No device can receive notifications"},
		{i18n.ZhHans, "no_device", "没发出去：没有能接收通知的设备"},
		{i18n.Ja, "no_device", "送信できませんでした：通知を受信できるデバイスがありません"},
		{i18n.En, "filtered", "Not sent: Notification settings or an active desktop prevented this notification on every available device"},
		{i18n.ZhHans, "filtered", "没发出去：通知设置或在线的桌面客户端使所有可用设备都跳过了这条通知"},
		{i18n.Ja, "filtered", "送信できませんでした：通知設定または接続中のデスクトップアプリにより、利用可能なすべてのデバイスでこの通知が抑制されました"},
		// Existing and future reasons keep their machine-readable value in the diagnostic.
		{"", "muted", "没发出去：hub 没有发送：muted"},
		{i18n.En, "future_reason", "Not sent: The hub did not send it: future_reason"},
	} {
		for _, strict := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/strict=%t", tc.lang, tc.reason, strict), func(t *testing.T) {
				f := &fake{reply: fmt.Sprintf(`{"sent":false,"reason":%q}`, tc.reason)}
				args, wantCode := []string{"Build done"}, ExitOK
				if strict {
					args, wantCode = append(args, "--strict"), ExitDenied
				}
				var out, errOut bytes.Buffer
				code := f.runner(false).Notify(i18n.With(context.Background(), tc.lang), args, &out, &errOut)
				if code != wantCode || out.Len() != 0 || errOut.String() != "personad notify: "+tc.wantText+"\n" {
					t.Fatalf("exit %d, out %q, err %q; want exit %d, empty stdout and diagnostic %q", code, out.String(), errOut.String(), wantCode, tc.wantText)
				}
			})
		}
	}
}

func TestNotifyUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no title":      {},
		"empty title":   {" "},
		"three words":   {"a", "b", "c"},
		"unknown flag":  {"a", "--loud"},
		"strict, empty": {"--strict"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{reply: `{"sent":true}`}
			if r := notify(f, args...); r.code != ExitUsage || len(f.reqs) != 0 {
				t.Fatalf("exit %d, asked %d times; err %q", r.code, len(f.reqs), r.errOut)
			}
		})
	}
	if r := notify(&fake{}, "--help"); r.code != 0 || !strings.Contains(r.out, "usage: personad notify") {
		t.Fatalf("help: %+v", r)
	}
}
