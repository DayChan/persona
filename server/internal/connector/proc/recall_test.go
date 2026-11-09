package proc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/rpc"
)

// serveRawRecall is an extension written against the protocol alone (what a program in another language is): it
// answers connector.status and connector.recall, and writes the params of each recall as they came to the file
// PERSONA_PROC_RECALL_LOG names. What it answers to a recall goes by the message ref the hub asks about.
// PERSONA_PROC_RECALL=missing leaves recall out, as a program that declared it without having it (or an older one).
func serveRawRecall() {
	logFile := os.Getenv("PERSONA_PROC_RECALL_LOG")
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-plugin", Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{"connector": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case "status":
				return map[string]string{"state": "ok"}, nil
			case "recall.can":
				if os.Getenv("PERSONA_PROC_RECALL_V2") == "" {
					return nil, pserve.UnknownMethod(method)
				}
				var p struct {
					Action string `json:"action"`
				}
				_ = json.Unmarshal(params, &p)
				return map[string]bool{"can": p.Action != "cannot"}, nil
			case "recall.do":
				if os.Getenv("PERSONA_PROC_RECALL_V2") != "1" {
					return nil, pserve.UnknownMethod(method)
				}
				if f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					_, _ = f.Write(append(append([]byte{}, params...), '\n'))
					f.Close()
				}
				var p struct {
					MessageRef string `json:"message_ref"`
				}
				_ = json.Unmarshal(params, &p)
				switch p.MessageRef {
				case "ok":
					return map[string]string{"status": "done"}, nil
				case "already":
					return map[string]string{"status": "already_recalled"}, nil
				case "empty":
					return map[string]string{}, nil
				case "bogus":
					return map[string]string{"status": "something_new"}, nil
				case "cannot":
					return map[string]string{"status": "not_recallable", "error": "older than the service allows"}, nil
				case "signed-out":
					return map[string]string{"status": "unavailable", "error": "the account is logged out"}, nil
				case "limited":
					return map[string]string{"status": "unavailable", "error": "slow down"}, nil
				case "offline":
					return map[string]string{"status": "unavailable", "error": "the computer is not connected"}, nil
				case "unknown":
					return map[string]string{"status": "unknown", "error": "the service did not answer"}, nil
				case "refused":
					return map[string]string{"status": "refused", "error": strings.Repeat("字", 300) + "\nthe second line says what the message said"}, nil
				case "slow":
					<-ctx.Done()
					return nil, ctx.Err()
				case "die":
					os.Exit(1)
				}
				return nil, errors.New("unexpected message ref " + p.MessageRef)
			case "recall":
				if os.Getenv("PERSONA_PROC_RECALL") == "missing" {
					return nil, pserve.UnknownMethod(method)
				}
				if f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					_, _ = f.Write(append(append([]byte{}, params...), '\n'))
					f.Close()
				}
				var p struct {
					MessageRef string `json:"message_ref"`
				}
				_ = json.Unmarshal(params, &p)
				switch p.MessageRef {
				case "ok":
					return map[string]bool{"recalled": true}, nil
				case "empty":
					return map[string]string{}, nil
				case "no":
					return map[string]bool{"recalled": false}, nil
				case "cannot":
					return nil, &rpc.Error{Code: "not_recallable", Message: "older than the service allows"}
				case "signed-out":
					return nil, &rpc.Error{Code: "not_signed_in", Message: "the account is logged out"}
				case "limited":
					return nil, &rpc.Error{Code: "rate_limited", Message: "slow down"}
				case "offline":
					return nil, &rpc.Error{Code: "offline", Message: "the computer is not connected"}
				case "unknown":
					return nil, &rpc.Error{Code: "recall_unknown", Message: "the service did not answer"}
				case "refused":
					return nil, errors.New(strings.Repeat("字", 300) + "\nthe second line says what the message said")
				case "refused-200":
					return nil, errors.New(strings.Repeat("字", 200))
				case "refused-201":
					return nil, errors.New(strings.Repeat("字", 201))
				case "refused-two-lines":
					return nil, errors.New("the service refused it\nthe second line says what the message said")
				case "silent":
					return nil, &rpc.Error{}
				case "other-code":
					return nil, &rpc.Error{Code: "something_new", Message: "a code this hub does not know"}
				case "slow":
					<-ctx.Done()
					return nil, ctx.Err()
				case "die":
					os.Exit(1)
				}
				return nil, errors.New("unexpected message ref " + p.MessageRef)
			}
			return nil, pserve.UnknownMethod(method)
		}}})
}

// linkedRaw is the raw program run as an extension by the plugin host, its connector declaring caps, reaching it through
// its Link (the hub's way); env is added to the program's. The log of what it was asked is the file it returns.
func linkedRaw(t *testing.T, caps []connector.Capability, notBefore time.Time, env ...string) (*Conn, *host.Proc, string) {
	return linkedRawWith(t, caps, notBefore, callTimeout, env...)
}

// linkedRawWith is linkedRaw with the plugin host giving a call hostTimeout, which can be shorter than the connector's own.
func linkedRawWith(t *testing.T, caps []connector.Capability, notBefore time.Time, hostTimeout time.Duration, env ...string) (*Conn, *host.Proc, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(t.TempDir(), "recalls.log")
	var c *Conn
	p := host.Start(context.Background(), host.Spec{ID: "example-plugin", Name: "Example", Command: exe, RPC: true, Startup: true,
		Env:         append([]string{"PERSONA_PROC_CHILD=1", "PERSONA_PROC_KIND=raw-recall", "PERSONA_PROC_RECALL_LOG=" + logFile}, env...),
		CallTimeout: hostTimeout, Logf: t.Logf,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Extension: extension.ExtensionInfo{ID: "example-plugin", Version: "1.0.0"},
				Active: map[string][]string{"connectors": {"example"}}}, nil
		},
		Handle: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			return nil, errors.New("not asked")
		},
	})
	t.Cleanup(p.Stop)
	waitUntil(t, "the extension to be up", p.Ready)
	c = New(Config{ID: "example", Name: "Example", Manifest: connector.Manifest{Capabilities: caps}, Logf: t.Logf,
		Link: &Link{Proc: p, ID: "example"}, RecallNotBefore: notBefore})
	return c, p, logFile
}

var declaresRecall = []connector.Capability{connector.CapStatus, connector.CapRecall}

// asked is the params of each connector.recall the program received, as it wrote them down.
func asked(t *testing.T, logFile string) []map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(logFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// The program that declares recall is a Recaller and is asked with what the ledger row says and no more: the connector it
// is for, the action, the message ref, where it went and when the row was written; not the row's id, nor anything the
// message said.
func TestRecallAsksTheProgramWithTheRowsFieldsOnly(t *testing.T) {
	c, _, logFile := linkedRaw(t, declaresRecall, time.Time{})
	r, ok := connector.RecallerOf(c)
	if !ok || !r.CanRecall("anything", time.Hour) {
		t.Fatalf("a program that declares recall: %v %v", r, ok)
	}
	at := time.Date(2026, 11, 7, 3, 4, 5, 0, time.UTC)
	err := c.Recall(context.Background(), connector.SentMessage{Action: "POST /conversations/c1/messages", Ref: "ok",
		Target: json.RawMessage(`{"method":"POST","command":"ex","conversation":"c1"}`), At: at})
	if err != nil {
		t.Fatalf("a recall the program confirmed: %v", err)
	}
	got := asked(t, logFile)
	if len(got) != 1 {
		t.Fatalf("asked %d times", len(got))
	}
	keys := make([]string, 0, len(got[0]))
	for k := range got[0] {
		keys = append(keys, k)
	}
	if want := []string{"action", "at", "contribution", "message_ref", "target"}; len(keys) != len(want) {
		t.Fatalf("the params are %v, want exactly %v", keys, want)
	}
	str := func(k string) string { var s string; _ = json.Unmarshal(got[0][k], &s); return s }
	if str("contribution") != "example" || str("action") != "POST /conversations/c1/messages" || str("message_ref") != "ok" || str("at") != "2026-11-07T03:04:05Z" {
		t.Errorf("params %s", got[0])
	}
	var target map[string]string
	_ = json.Unmarshal(got[0]["target"], &target)
	if target["command"] != "ex" || target["conversation"] != "c1" || target["method"] != "POST" || len(target) != 3 {
		t.Errorf("target %s", got[0]["target"])
	}
}

// recallGoProgram is exampleProgram that can take a message back (connector.Recaller), as the message ref it is asked
// about says; declare is whether its manifest says it can.
type recallGoProgram struct {
	*exampleProgram
	declare bool
}

func (p *recallGoProgram) Manifest() connector.Manifest {
	m := p.exampleProgram.Manifest()
	if p.declare {
		m.Capabilities = append(m.Capabilities, connector.CapRecall)
	}
	return m
}

func (*recallGoProgram) CanRecall(string, time.Duration) bool { return true }

func (*recallGoProgram) Recall(_ context.Context, m connector.SentMessage) error {
	if m.At.IsZero() { // the row's time reaches a connector written in Go (a limit of its own counts from it), over the pipe and as an extension
		return errors.New("the row's time did not arrive")
	}
	if !json.Valid(m.Target) { // a target that parses, also for a row that has none (serve gives {}): a connector reads it with json.Unmarshal
		return errors.New("no usable target")
	}
	switch m.Ref {
	case "ok":
		return nil
	case "cannot":
		return fmt.Errorf("%w: only comments", connector.ErrCannotRecall)
	case "unavailable":
		return fmt.Errorf("%w: no token", connector.ErrRecallUnavailable)
	case "unknown":
		return fmt.Errorf("%w: the connection broke", connector.ErrRecallUnknown)
	case "refused":
		return errors.New("the service said no")
	}
	return errors.New("unexpected message ref " + m.Ref)
}

// A connector written in Go and served as a program (connector/serve) is asked over the pipe and as an extension, and what
// it returns of connector.Recaller's errors reaches the hub as the same ones; the service refusing stays a plain error.
func TestAServedRecallerAnswersAsConnectorRecallerSays(t *testing.T) {
	for _, way := range []string{"pipe", "plugin"} {
		t.Run(way, func(t *testing.T) {
			sec := &memSecrets{}
			env := []string{"PERSONA_PROC_KIND=recall-go"}
			c := New(program(t, sec, env...))
			if way == "plugin" {
				c = linked(t, sec, env...)
				c.cfg.Manifest.Capabilities = append(slices.Clone(c.cfg.Manifest.Capabilities), connector.CapRecall)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go c.Run(ctx)
			waitUntil(t, "the program's status", func() bool { return c.Status(ctx).State == connector.StateNotSignedIn })
			for _, tc := range []struct {
				ref  string
				want error
				text string
			}{
				{"ok", nil, ""},
				{"cannot", connector.ErrCannotRecall, "only comments"},
				{"unavailable", connector.ErrRecallUnavailable, "no token"},
				{"unknown", connector.ErrRecallUnknown, "the connection broke"},
			} {
				err := c.Recall(ctx, connector.SentMessage{Action: "example_send", Ref: tc.ref, At: time.Now()})
				switch {
				case tc.want == nil && err != nil:
					t.Errorf("%s: %v", tc.ref, err)
				case tc.want != nil && (!errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.text)):
					t.Errorf("%s: %v, want %v with %q", tc.ref, err, tc.want, tc.text)
				}
			}
			err := c.Recall(ctx, connector.SentMessage{Action: "example_send", Ref: "refused", At: time.Now()})
			if err == nil || !strings.Contains(err.Error(), "the service said no") || errors.Is(err, connector.ErrCannotRecall) ||
				errors.Is(err, connector.ErrRecallUnavailable) || errors.Is(err, connector.ErrRecallUnknown) {
				t.Errorf("a refusal: %v", err)
			}
			// A recall that names no message is refused by serve itself, before the connector is asked (which would answer
			// with its own words for an unknown ref, not this).
			if err := c.Recall(ctx, connector.SentMessage{Action: "example_send", Ref: "", At: time.Now()}); err == nil || !strings.Contains(err.Error(), "message_ref") {
				t.Errorf("a recall with no message ref: %v", err)
			}
		})
	}
}

func recallOf(c *Conn, ref string) error {
	return c.Recall(context.Background(), connector.SentMessage{Action: "examplechat_send", Ref: ref, At: time.Now()})
}

// What a program answers to connector.recall is what connector.Recaller says it is: only a clear recalled true is a yes;
// not_recallable is nothing sent, not signed in, offline and rate limited are cannot act now, recall_unknown is not
// knowing, an answer that does not say yes (none, false) is not knowing; any other error is the service refusing, in the
// program's words cut to one sentence (its second line, which may be what the message said, is not kept).
func TestRecallMapsWhatTheProgramAnswers(t *testing.T) {
	c, _, _ := linkedRaw(t, declaresRecall, time.Time{})
	for _, tc := range []struct {
		ref  string
		want error // nil: a yes
		text string
	}{
		{"ok", nil, ""},
		{"empty", connector.ErrRecallUnknown, "did not say"},
		{"no", connector.ErrRecallUnknown, "did not say"},
		{"cannot", connector.ErrCannotRecall, "older than the service allows"},
		{"signed-out", connector.ErrRecallUnavailable, "the account is logged out"},
		{"limited", connector.ErrRecallUnavailable, "slow down"},
		{"offline", connector.ErrRecallUnavailable, "the computer is not connected"},
		{"unknown", connector.ErrRecallUnknown, "the service did not answer"},
	} {
		err := recallOf(c, tc.ref)
		switch {
		case tc.want == nil && err != nil:
			t.Errorf("%s: %v", tc.ref, err)
		case tc.want != nil && (!errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.text)):
			t.Errorf("%s: %v, want %v with %q", tc.ref, err, tc.want, tc.text)
		}
	}
	for _, tc := range []struct{ ref, text string }{
		{"refused", strings.Repeat("字", 200)},     // one line of 300 characters, cut at 200 (the length is written out: not the constant's)
		{"refused-200", strings.Repeat("字", 200)}, // 200 as it is
		{"refused-201", strings.Repeat("字", 200)}, // one over is cut
		{"refused-two-lines", "the service refused it"},
		{"silent", "Example did not take it back"},
		{"other-code", "a code this hub does not know"},
	} {
		err := recallOf(c, tc.ref)
		if err == nil || err.Error() != tc.text || errors.Is(err, connector.ErrCannotRecall) || errors.Is(err, connector.ErrRecallUnavailable) ||
			errors.Is(err, connector.ErrRecallUnknown) {
			t.Errorf("%s: a refusal wanted as %q, got %v", tc.ref, tc.text, err)
		}
	}
}

// A program that does not declare recall is never asked, whatever calls Recall: it is not a Recaller, cannot recall any
// action, and Recall itself sends nothing.
func TestRecallNeedsTheDeclaration(t *testing.T) {
	c, _, logFile := linkedRaw(t, []connector.Capability{connector.CapStatus}, time.Time{})
	if _, ok := connector.RecallerOf(c); ok {
		t.Error("a program that does not declare recall is found as a Recaller")
	}
	if c.CanRecall("examplechat_send", 0) {
		t.Error("a program that does not declare recall can recall")
	}
	if err := recallOf(c, "ok"); !errors.Is(err, connector.ErrCannotRecall) {
		t.Errorf("Recall: %v", err)
	}
	if got := asked(t, logFile); len(got) != 0 {
		t.Errorf("the program was asked %v", got)
	}
}

// A send the ledger wrote before this install of the extension began was not its: it is not asked about it (an earlier
// extension with the same connector id, since uninstalled). Later ones are; a row with no time is not trusted either.
func TestRecallDoesNotAskAboutWhatBeganBeforeTheInstall(t *testing.T) {
	installed := time.Now().Add(-time.Hour)
	c, _, logFile := linkedRaw(t, declaresRecall, installed)
	ask := func(at time.Time) error {
		return c.Recall(context.Background(), connector.SentMessage{Action: "examplechat_send", Ref: "ok", At: at})
	}
	for name, at := range map[string]time.Time{"before": installed.Add(-time.Second), "no time": {}} {
		if err := ask(at); !errors.Is(err, connector.ErrCannotRecall) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := asked(t, logFile); len(got) != 0 {
		t.Fatalf("the program was asked %v about what was not its", got)
	}
	if err := ask(installed.Add(time.Second)); err != nil || len(asked(t, logFile)) != 1 {
		t.Errorf("after the install: %v, asked %d", err, len(asked(t, logFile)))
	}
}

// A program that declares recall but does not have the method (an older build, or a mistake) answers unknown_method:
// nothing was sent.
func TestRecallOfAProgramWithoutTheMethod(t *testing.T) {
	c, _, _ := linkedRaw(t, declaresRecall, time.Time{}, "PERSONA_PROC_RECALL=missing")
	if err := recallOf(c, "ok"); !errors.Is(err, connector.ErrCannotRecall) {
		t.Fatalf("%v", err)
	}
}

// A program that goes away while it is asked, or never answers, leaves the outcome unknown (the request went out); one
// that is not running is not available (nothing was sent).
func TestRecallWhenTheProgramDiesNeverAnswersOrIsNotRunning(t *testing.T) {
	t.Run("dies while asked", func(t *testing.T) {
		c, _, _ := linkedRaw(t, declaresRecall, time.Time{})
		if err := recallOf(c, "die"); !errors.Is(err, connector.ErrRecallUnknown) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("never answers", func(t *testing.T) {
		c, _, _ := linkedRaw(t, declaresRecall, time.Time{})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		err := c.Recall(ctx, connector.SentMessage{Action: "examplechat_send", Ref: "slow", At: time.Now()})
		if !errors.Is(err, connector.ErrRecallUnknown) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("not running", func(t *testing.T) {
		c, p, _ := linkedRaw(t, declaresRecall, time.Time{})
		p.Stop()
		if err := recallOf(c, "ok"); !errors.Is(err, connector.ErrRecallUnavailable) {
			t.Fatalf("%v", err)
		}
	})
}

// A program the plugin host gives up on (its own limit for a call, shorter than the hub's) leaves the outcome unknown too:
// the request went out and no answer came.
func TestRecallWhenTheHostGivesUpOnTheProgram(t *testing.T) {
	c, _, _ := linkedRawWith(t, declaresRecall, time.Time{}, 200*time.Millisecond)
	if err := recallOf(c, "slow"); !errors.Is(err, connector.ErrRecallUnknown) {
		t.Fatalf("%v", err)
	}
}

// A program cannot start a recall: what it sends the hub as connector.recall (a call only the hub makes) is an error and
// nothing is asked of anyone. With or without the secrets store the connector was given, whose absence is another error.
func TestAProgramCannotStartARecall(t *testing.T) {
	params := json.RawMessage(`{"contribution":"example","action":"examplechat_send","message_ref":"ok"}`)
	without := New(Config{ID: "example", Name: "Example", Manifest: connector.Manifest{Capabilities: declaresRecall}})
	if out, err := without.Handle(context.Background(), serve.MRecall, params); err == nil {
		t.Fatalf("a program's recall was taken: %v", out)
	}
	with := New(Config{ID: "example", Name: "Example", Manifest: connector.Manifest{Capabilities: declaresRecall}, Secrets: &memSecrets{}})
	out, err := with.Handle(context.Background(), serve.MRecall, params)
	if err == nil || !strings.Contains(err.Error(), "unknown method "+serve.MRecall) {
		t.Fatalf("a program's recall: %v, %v (want an unknown method)", out, err)
	}
}

// A connector written in Go that implements connector.Recaller but does not declare recall is not asked by the program's
// own serve either, whatever the hub's manifest for it says (a manifest that declares what the program does not): it
// answers no_capability, and nothing was sent.
func TestAServedRecallerNeedsTheDeclaration(t *testing.T) {
	sec := &memSecrets{}
	c := linked(t, sec, "PERSONA_PROC_KIND=recall-go-undeclared")
	c.cfg.Manifest.Capabilities = append(slices.Clone(c.cfg.Manifest.Capabilities), connector.CapRecall)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitUntil(t, "the program's status", func() bool { return c.Status(ctx).State == connector.StateNotSignedIn })
	if err := c.Recall(ctx, connector.SentMessage{Action: "example_send", Ref: "ok", At: time.Now()}); !errors.Is(err, connector.ErrCannotRecall) {
		t.Fatalf("%v", err)
	}
}
