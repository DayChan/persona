package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	progconf "github.com/nwverse/persona/server/internal/extension/conformance"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The wire fixtures are what a connector program for a chat service says (the example connector "examplechat"):
// synthetic data, no real account, name or address.
// Here they hold the Go types: a type that changes without the program following turns these tests red, with or
// without Node to run the program.
const wireDir = "../testdata/wire/examplechat"

func fixture(t testing.TB, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(wireDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func fixtureManifest(t testing.TB) *extension.Manifest {
	t.Helper()
	m, err := extension.Parse(fixture(t, "persona-plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ignore[T any](check func(json.RawMessage) (T, error)) func(json.RawMessage) error {
	return func(raw json.RawMessage) error { _, err := check(raw); return err }
}

// Every fixture strictly decodes into the hub's types and keeps what the hub relies on; each is listed, so a new one
// is not left unchecked.
func TestExampleChatWireFixtures(t *testing.T) {
	m := fixtureManifest(t)
	items, err := connectorItems(m)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := checkToolList("examplechat", "actions", fixture(t, "actions.list.json"))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]func(json.RawMessage) error{
		"persona-plugin.json": func(raw json.RawMessage) error { _, err := extension.Parse(raw); return err },
		"initialize.json":     func(raw json.RawMessage) error { return checkInitialize(m, items, raw) },
		"status.json":         ignore(checkStatus),
		"tools.list.json": ignore(func(raw json.RawMessage) ([]proto.Tool, error) {
			return checkToolList("examplechat", "tools", raw)
		}),
		"catalog.list.json":     ignore(checkCatalog),
		"tools.call.json":       func(raw json.RawMessage) error { return decodeWire(raw, &serve.CallResult{}) },
		"actions.list.json":     ignore(func(raw json.RawMessage) ([]proto.Tool, error) { return checkToolList("examplechat", "actions", raw) }),
		"actions.describe.json": func(raw json.RawMessage) error { return checkDescribe("examplechat_send", raw) },
		"actions.do.json":       func(raw json.RawMessage) error { return decodeWire(raw, &serve.CallResult{}) },
		"inbox.collect.json": ignore(func(raw json.RawMessage) ([]connector.InboxThing, error) {
			return checkCollect(items[0].Kinds, slices.Contains(items[0].Capabilities, string(connector.CapFallback)), raw)
		}),
		"thread.page.json": ignore(func(raw json.RawMessage) (connector.ThreadPage, error) {
			return checkThreadPage(programThreadLimit, actions, raw)
		}),
		"archive.changes.json": ignore(func(raw json.RawMessage) (serve.ChangesResult, error) {
			return checkChanges("", programArchiveMost, raw)
		}),
		"history.chats.json": ignore(checkHistoryChats),
		"archive.history.json": ignore(func(raw json.RawMessage) (connectorwire.HistoryPage, error) {
			return checkHistoryPage(connectorwire.HistoryQuery{Chat: "primary.user_example_a"}, raw)
		}),
		"archive.history.start.json": ignore(func(raw json.RawMessage) (connectorwire.HistoryPage, error) {
			return checkHistoryPage(connectorwire.HistoryQuery{Chat: "primary.user_example_a", Before: "3"}, raw)
		}),
		"archive.history.unreadable.json": ignore(func(raw json.RawMessage) (connectorwire.HistoryPage, error) {
			return checkHistoryPage(connectorwire.HistoryQuery{Chat: "primary.group_example_gone"}, raw)
		}),
		"archive.history.paced.json": ignore(func(raw json.RawMessage) (connectorwire.HistoryPage, error) {
			return checkHistoryPage(connectorwire.HistoryQuery{Chat: "primary.group_example_team"}, raw)
		}),
		"history.anchors.json":    ignore(checkHistoryAnchors),
		"login.step.json":         ignore(checkPrompt),
		"login.step.refused.json": ignore(checkPrompt),
		"login.step.done.json":    ignore(checkPrompt),
		"changed.json":            func(raw json.RawMessage) error { return checkChanged(items, raw) },
	}
	files, err := os.ReadDir(wireDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		check, ok := checks[f.Name()]
		if !ok {
			t.Errorf("%s is not checked: add it to checks", f.Name())
			continue
		}
		if err := check(fixture(t, f.Name())); err != nil {
			t.Errorf("%s: %v", f.Name(), err)
		}
		delete(checks, f.Name())
	}
	for name := range checks {
		t.Errorf("%s is listed and does not exist", name)
	}
}

// A program that answers from the fixtures. edit changes the JSON of a fixture before it is sent (to see what
// CheckProgram says of a program that drifted); redeliver has the archive give the same again from a later cursor.
type fixtureProgram struct {
	edit      map[string]func(map[string]any)
	redeliver bool
	wrote     atomic.Bool // the program was asked to write: actions.do, inbox.reply, recall or recall.do
	// historyAsked counts the history calls (history.chats, archive.history, history.anchors) it answered.
	catalogAsked atomic.Int32
	historyAsked atomic.Int32
	manifest     *extension.Manifest // what CheckProgram is given, when it is not the fixture's
	signedIn     atomic.Bool
	announced    sync.Once
	peerCaller   *rpc.Peer
}

const goodKey = "example-key"

func (f *fixtureProgram) answer(t testing.TB, name string) json.RawMessage {
	raw := fixture(t, name)
	if edit := f.edit[name]; edit != nil {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Error(err)
		}
		edit(v)
		raw, _ = json.Marshal(v)
	}
	return raw
}

func (f *fixtureProgram) secrets(ctx context.Context, method string, data []byte) {
	params, _ := extension.Targeted(serve.SecretData{Data: data}, "examplechat")
	_ = f.peerCaller.Call(ctx, serve.InPlugin(method), params, nil)
}

// handle is the program's side: what a program in another language does on its stdin and stdout.
func (f *fixtureProgram) handle(t testing.TB) rpc.Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		var args struct {
			Step, Cursor, Before, Chat, Around string
			Values                             map[string]string
		}
		_ = json.Unmarshal(params, &args)
		switch method {
		case extension.MInitialize:
			return f.answer(t, "initialize.json"), nil
		case "connector.status":
			f.announced.Do(func() { _ = f.peerCaller.Notify("connector.changed", f.answer(t, "changed.json")) })
			if !f.signedIn.Load() {
				return json.RawMessage(`{"state":"not_configured","guide":["在设置里登录示例聊天"]}`), nil
			}
			return f.answer(t, "status.json"), nil
		case "connector.catalog.list":
			f.catalogAsked.Add(1)
			return f.answer(t, "catalog.list.json"), nil
		case "connector.tools.list":
			return f.answer(t, "tools.list.json"), nil
		case "connector.tools.call":
			return f.answer(t, "tools.call.json"), nil
		case "connector.actions.list":
			return f.answer(t, "actions.list.json"), nil
		case "connector.actions.describe":
			return nil, errors.New("chat and text are required") // empty arguments: refused, as a program should
		case "connector.inbox.collect":
			return f.answer(t, "inbox.collect.json"), nil
		case "connector.thread.page":
			if args.Before != "" {
				return json.RawMessage(`{"chat":{"id":"primary.user_example_a","name":"示例联系人","kind":"dm"}}`), nil
			}
			return f.answer(t, "thread.page.json"), nil
		case "connector.archive.changes":
			if args.Cursor == "" || f.redeliver {
				return f.answer(t, "archive.changes.json"), nil
			}
			next, _ := json.Marshal(args.Cursor)
			return json.RawMessage(`{"next":` + string(next) + `}`), nil
		case "connector.history.chats":
			f.historyAsked.Add(1)
			return f.answer(t, "history.chats.json"), nil
		case "connector.archive.history":
			f.historyAsked.Add(1)
			switch {
			case args.Chat == "primary.group_example_gone":
				return f.answer(t, "archive.history.unreadable.json"), nil
			case args.Chat == "primary.group_example_team":
				return f.answer(t, "archive.history.paced.json"), nil
			case args.Before == "3":
				return f.answer(t, "archive.history.start.json"), nil
			}
			return f.answer(t, "archive.history.json"), nil
		case "connector.history.anchors":
			f.historyAsked.Add(1)
			return f.answer(t, "history.anchors.json"), nil
		case "connector.login.step":
			switch {
			case args.Step == "":
				return f.answer(t, "login.step.json"), nil
			case args.Values["app_key"] == goodKey && args.Values["base_url"] != "":
				f.signedIn.Store(true)
				f.secrets(ctx, "secrets.save", []byte(`{"v":1}`))
				return f.answer(t, "login.step.done.json"), nil
			}
			return f.answer(t, "login.step.refused.json"), nil
		case "connector.actions.do", "connector.inbox.reply", "connector.recall", "connector.recall.do":
			f.wrote.Store(true)
			return json.RawMessage(`{"text":"sent"}`), nil
		case "connector.logout":
			f.signedIn.Store(false)
			f.secrets(ctx, "secrets.delete", nil)
			return struct{}{}, nil
		}
		return nil, &rpc.Error{Code: serve.CodeUnknownMethod, Message: "unknown method " + method}
	}
}

func (f *fixtureProgram) start(t *testing.T) progconf.Run {
	progR, hubW := io.Pipe()
	hubR, progW := io.Pipe()
	peer := rpc.New("c", progW)
	f.peerCaller = peer
	peer.OnRequest = f.handle(t)
	exited := make(chan error, 1)
	go func() { exited <- peer.Serve(context.Background(), progR) }()
	t.Cleanup(func() { _ = hubW.Close(); _ = progW.Close(); _ = progR.Close(); _ = hubR.Close() })
	return progconf.Run{ToProgram: hubW, FromProgram: hubR, Exited: exited}
}

// recorder is a testing.TB that keeps what fails, to see CheckProgram fail.
type recorder struct {
	testing.TB
	mu   sync.Mutex
	msgs []string
}

func (r *recorder) Helper()             {}
func (r *recorder) Logf(string, ...any) {}
func (r *recorder) Errorf(f string, a ...any) {
	r.mu.Lock()
	r.msgs = append(r.msgs, fmt.Sprintf(f, a...))
	r.mu.Unlock()
}
func (r *recorder) Fatalf(f string, a ...any) { r.Errorf(f, a...); runtime.Goexit() }
func (r *recorder) Fatal(a ...any)            { r.Fatalf("%s", fmt.Sprint(a...)) }

// check runs CheckProgram on the program as a fresh sign-in would meet it and says what it found.
func (f *fixtureProgram) check(t *testing.T, opts ...ProgramOption) string {
	t.Helper()
	run := f.start(t)
	rec := &recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m := f.manifest
		if m == nil {
			m = fixtureManifest(t)
		}
		CheckProgram(rec, m, run, extension.InitializeParams{},
			append([]ProgramOption{WithLogin(map[string]string{"base_url": "https://chat.example.com", "app_key": goodKey}), RequireData()}, opts...)...)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("CheckProgram did not end")
	}
	return strings.Join(rec.msgs, "\n")
}

// A connector that declares fallback (#1807) passes the manifest check, and each of its inbox things needs a tier the
// hub knows without the model: extra.rule_tier, or a tier for sure; a batch to relate needs none (#1899).
func TestCollectHoldsAFallback(t *testing.T) {
	if !slices.Contains(programCaps, connector.CapFallback) {
		t.Fatal("a program cannot declare fallback")
	}
	thing := `{"thread":"t%d","kind":"dm","at":"2026-11-04T09:00:00Z","last":"1"%s}`
	for _, c := range []struct {
		extra string
		ok    bool
	}{
		{`,"extra":{"rule_tier":"action"}`, true},
		{`,"tier":"fyi"`, true},
		{`,"group":{"batch":"b1","new":["m1"]}`, true},
		{``, false},
		{`,"extra":{"rule_tier":"later"}`, false},
	} {
		raw := json.RawMessage(fmt.Sprintf(`{"things":[`+thing+`]}`, 1, c.extra))
		_, err := checkCollect([]string{"dm"}, true, raw)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.extra, err)
		}
		if _, err := checkCollect([]string{"dm"}, false, raw); err != nil {
			t.Errorf("without a fallback %s: %v", c.extra, err)
		}
	}
}

func TestCheckProgramPassesWhatTheFixturesSay(t *testing.T) {
	f := &fixtureProgram{}
	if got := f.check(t); got != "" {
		t.Fatalf("CheckProgram failed a program that says what the fixtures say:\n%s", got)
	}
	if f.wrote.Load() {
		t.Fatal("CheckProgram asked the program to write (actions.do or inbox.reply): a check never sends")
	}
}

// A program that declares recall passes (CheckProgram reaches the capabilities the hub can have in a program, and this
// is one), and is never asked to take anything back: a recall deletes a message, as actions.do and inbox.reply write.
func TestCheckProgramAcceptsADeclaredRecallAndNeverAsksIt(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal(fixture(t, "persona-plugin.json"), &m); err != nil {
		t.Fatal(err)
	}
	conn := m["contributes"].(map[string]any)["connectors"].([]any)[0].(map[string]any)
	conn["capabilities"] = append(conn["capabilities"].([]any), "recall")
	raw, _ := json.Marshal(m)
	declared, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixtureProgram{manifest: declared, edit: map[string]func(map[string]any){"initialize.json": func(v map[string]any) {
		c := v["contributes"].(map[string]any)["connectors"].([]any)[0].(map[string]any)
		c["capabilities"] = append(c["capabilities"].([]any), "recall")
	}}}
	if got := f.check(t); got != "" {
		t.Fatalf("CheckProgram failed a program that declares recall:\n%s", got)
	}
	if f.wrote.Load() {
		t.Fatal("CheckProgram asked the program to write (actions.do, inbox.reply, recall or recall.do): a check never sends")
	}
}

// declaring is the fixture program with the example connector declaring caps besides its own, in its manifest and in
// what it answers to initialize.
func declaring(t *testing.T, caps ...string) *fixtureProgram {
	t.Helper()
	add := func(v map[string]any) {
		c := v["contributes"].(map[string]any)["connectors"].([]any)[0].(map[string]any)
		for _, cp := range caps {
			c["capabilities"] = append(c["capabilities"].([]any), cp)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(fixture(t, "persona-plugin.json"), &m); err != nil {
		t.Fatal(err)
	}
	add(m)
	raw, _ := json.Marshal(m)
	declared, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureProgram{manifest: declared, edit: map[string]func(map[string]any){"initialize.json": add}}
}

// A program that declares history and history_anchors is asked for its conversations and reads back a few of them
// (one to its start, one unreadable, one that asks to slow down), and for the owner's messages and the page around one.
func TestCheckProgramReadsHistory(t *testing.T) {
	f := declaring(t, "history", "history_anchors")
	if got := f.check(t); got != "" {
		t.Fatalf("CheckProgram failed a program with history that says what the fixtures say:\n%s", got)
	}
	// history.chats, the dm's two pages, the unreadable group, the paced group, history.anchors and the page around it.
	if n := f.historyAsked.Load(); n != 7 {
		t.Fatalf("%d history calls, want 7", n)
	}
	if f.wrote.Load() {
		t.Fatal("CheckProgram asked the program to write while reading history")
	}
}

// A program that does not declare history is never asked for it.
func TestCheckProgramLeavesHistoryUndeclared(t *testing.T) {
	f := &fixtureProgram{}
	if got := f.check(t); got != "" {
		t.Fatal(got)
	}
	if n := f.historyAsked.Load(); n != 0 {
		t.Fatalf("%d history calls to a program that does not declare history", n)
	}
}

// What the history import relies on, one program at a time that does not keep to it.
func TestCheckProgramCatchesHistoryDrift(t *testing.T) {
	cases := []struct {
		name, file, want string
		edit             func(map[string]any)
	}{
		{"more without a cursor", "archive.history.json", "more without an older cursor", func(v map[string]any) { delete(v, "older") }},
		{"a page newest first", "archive.history.json", "oldest first", func(v map[string]any) { slices.Reverse(v["messages"].([]any)) }},
		{"an unreadable chat with messages", "archive.history.unreadable.json", "unreadable, with", func(v map[string]any) {
			v["messages"] = []any{map[string]any{"id": "1", "text": "x", "at": "2026-11-04T08:00:00Z"}}
		}},
		{"a kind the hub does not know", "history.chats.json", `kind "channel"`, func(v map[string]any) {
			v["chats"].([]any)[0].(map[string]any)["kind"] = "channel"
		}},
		{"an anchor without a reason", "history.anchors.json", "neither mine nor mention", func(v map[string]any) {
			delete(v["anchors"].([]any)[0].(map[string]any), "mine")
		}},
		{"a stop without a reason", "archive.history.paced.json", "without a reason", func(v map[string]any) {
			v["pace"] = map[string]any{"stop": true}
		}},
		{"an empty page with more", "archive.history.json", "more on a page with no messages", func(v map[string]any) {
			v["messages"] = []any{}
		}},
		{"an older cursor at the start", "archive.history.start.json", "an older cursor without more", func(v map[string]any) {
			v["older"] = "1"
		}},
		{"an unreadable chat with more", "archive.history.unreadable.json", "unreadable, with more or a cursor", func(v map[string]any) {
			v["more"], v["older"] = true, "5"
		}},
		{"a chat flag the protocol does not have", "history.chats.json", `unknown field "starred"`, func(v map[string]any) {
			v["chats"].([]any)[0].(map[string]any)["starred"] = true
		}},
		{"one id under two sources", "history.chats.json", "listed twice", func(v map[string]any) {
			c := v["chats"].([]any)
			c[1].(map[string]any)["id"] = c[0].(map[string]any)["id"]
			c[1].(map[string]any)["source"] = "examplefeed"
		}},
		{"going forward instead of back", "archive.history.start.json", "after the newer page", func(v map[string]any) {
			for _, m := range v["messages"].([]any) {
				m.(map[string]any)["at"] = "2026-11-05T08:00:00Z"
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := declaring(t, "history", "history_anchors")
			f.edit[c.file] = c.edit
			if got := f.check(t); !strings.Contains(got, c.want) {
				t.Fatalf("CheckProgram should have failed with %q, said:\n%s", c.want, got)
			}
		})
	}
}

// A declaration the hub would refuse: history without an archive, history_anchors without history.
func TestCheckProgramRefusesHistoryWithoutItsBase(t *testing.T) {
	f := declaring(t, "history_anchors")
	if got := f.check(t); !strings.Contains(got, "declares history_anchors without history") {
		t.Fatalf("history_anchors without history passed:\n%s", got)
	}
}

// What the hub relies on, one program at a time that does not keep to it.
func TestCheckProgramCatchesDrift(t *testing.T) {
	first := func(v map[string]any, key string) map[string]any { return v[key].([]any)[0].(map[string]any) }
	cases := []struct {
		name, file, want string
		edit             func(map[string]any)
		redeliver        bool
	}{
		{"a field renamed", "inbox.collect.json", `unknown field "thread_id"`,
			func(v map[string]any) { x := first(v, "things"); x["thread_id"] = x["thread"]; delete(x, "thread") }, false},
		{"a key in the wrong case", "inbox.collect.json", `"Thread"`,
			func(v map[string]any) { x := first(v, "things"); x["Thread"] = x["thread"]; delete(x, "thread") }, false},
		{"a tool without the prefix", "tools.list.json", `does not start with "examplechat_"`,
			func(v map[string]any) { first(v, "tools")["name"] = "status" }, false},
		{"a page newest first", "thread.page.json", "oldest first",
			func(v map[string]any) { slices.Reverse(v["messages"].([]any)) }, false},
		{"a prompt with nothing to do", "login.step.json", "neither fields nor done", func(v map[string]any) { v["fields"] = []any{} }, false},
		{"a cursor that starts over", "archive.changes.json", "start over", func(v map[string]any) { delete(v, "next") }, false},
		{"a message delivered twice", "archive.changes.json", "delivered twice", func(v map[string]any) {}, true},
		{"nothing where the backend has data", "inbox.collect.json", "the backend has data", func(v map[string]any) { v["things"] = []any{} }, false},
		{"an event with a scope the hub drops", "changed.json", `scope "everything"`, func(v map[string]any) { v["scope"] = "everything" }, false},
		{"the sign-in's secret said out loud", "status.json", "secret", func(v map[string]any) { v["guide"] = []any{"the key " + goodKey + " works"} }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fixtureProgram{edit: map[string]func(map[string]any){c.file: c.edit}, redeliver: c.redeliver}
			if got := f.check(t); !strings.Contains(got, c.want) {
				t.Fatalf("CheckProgram should have failed with %q, said:\n%s", c.want, got)
			}
		})
	}
}

// A program that refuses its sign-in values is told so, and no secret is in what it says.
func TestCheckProgramReportsARefusedSignIn(t *testing.T) {
	run := (&fixtureProgram{}).start(t)
	rec := &recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		CheckProgram(rec, fixtureManifest(t), run, extension.InitializeParams{}, WithLogin(map[string]string{"base_url": "https://chat.example.com", "app_key": "example-key-wrong"}))
	}()
	<-done
	if got := strings.Join(rec.msgs, "\n"); !strings.Contains(got, "cannot go on") {
		t.Fatalf("a refused sign-in went unreported:\n%s", got)
	}
}

// A connector program of another repository on a fixture backend of its own, so there is no server to start: what
// selects the fixture comes from the environment of this test, which the program inherits, and the sign-in gives no
// values. Skips without the path, and leaves a program with a fixture server to TestConnectorProgramFixtureServerLive:
//
//	PERSONA_TEST_CONNECTOR_PROGRAM=<repository>/<plugin>/bin/<program> go test ./internal/connector/conformance -run ConnectorProgramLive -v
func TestConnectorProgramLive(t *testing.T) {
	program := os.Getenv("PERSONA_TEST_CONNECTOR_PROGRAM")
	if program == "" {
		t.Skip("set PERSONA_TEST_CONNECTOR_PROGRAM to run a connector program (docs/protocol/connector-protocol.md)")
	}
	if os.Getenv("PERSONA_TEST_CONNECTOR_FIXTURE_SERVER") != "" {
		t.Skip("a fixture server is set: TestConnectorProgramFixtureServerLive runs the program against it")
	}
	checkLive(t, program, map[string]string{})
}

// Any connector program that signs in with a service address and an app key (fields base_url and app_key), against a
// fixture server of that service: the real thing on the other side of the wire. PERSONA_TEST_CONNECTOR_PROGRAM is the
// program's launcher (in bin/ of its plugin package), PERSONA_TEST_CONNECTOR_FIXTURE_SERVER the fixture server's
// script, run with Node. Needs Node and both paths, so it runs where someone sets them up and skips elsewhere:
//
//	PERSONA_TEST_CONNECTOR_PROGRAM=<plugin package>/bin/<program> \
//	PERSONA_TEST_CONNECTOR_FIXTURE_SERVER=<fixture server script> \
//	go test ./internal/connector/conformance -run FixtureServerLive -v
//
// The fixture server listens on a loopback port (PORT=0) and prints one JSON line on its stdout once it is ready: its
// base_url and test keys (see signInValues). Sending stays off on it.
func TestConnectorProgramFixtureServerLive(t *testing.T) {
	program, server := os.Getenv("PERSONA_TEST_CONNECTOR_PROGRAM"), os.Getenv("PERSONA_TEST_CONNECTOR_FIXTURE_SERVER")
	if program == "" || server == "" {
		t.Skip("set PERSONA_TEST_CONNECTOR_PROGRAM (a connector program's launcher) and PERSONA_TEST_CONNECTOR_FIXTURE_SERVER (a compatible fixture server's script) to run it (docs/protocol/connector-protocol.md「一致性测试」)")
	}
	checkLive(t, program, startFixtureServer(t, server))
}

// checkLive holds a program, started twice, to the plugin protocol and the connector protocol, signing in with login.
func checkLive(t *testing.T, program string, login map[string]string) {
	t.Helper()
	// The package is what the program sits in: its manifest next to bin/, under the new name or the old one, as the
	// hub reads an installed package.
	pkg := filepath.Dir(filepath.Dir(program))
	raw, err := os.ReadFile(extension.ManifestPath(pkg))
	if err != nil {
		t.Fatal(err)
	}
	m, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	start := func() (progconf.Run, extension.InitializeParams) {
		data := t.TempDir()
		t.Setenv("PERSONA_PLUGIN_DATA", data) // what the hub gives a plugin: its own directory, empty at first
		return progconf.Program(t, program), extension.InitializeParams{Extension: extension.ExtensionInfo{Dir: pkg, DataDir: data}}
	}

	run, init := start()
	progconf.Check(t, m, run, init)
	run, init = start()
	CheckProgram(t, m, run, init, WithLogin(login), RequireData())
}

// fixtureLine is what the fixture server's ready line says that the live test reads. The line has more (ports, a
// notice, and a send_key: a key with the send scope, which this test never uses).
type fixtureLine struct {
	BaseURL              string `json:"base_url"`
	OutboundSendsEnabled *bool  `json:"outbound_sends_enabled"`
	Accounts             []struct {
		Key string `json:"key"` // scoped read and events
	} `json:"accounts"`
}

// signInValues are the values for the program's sign-in (its field names) from the fixture server's ready line, or why
// the test must not sign in: the server must be on loopback and say that outbound sending is off, and the program only
// gets the first account's read-and-events key.
func signInValues(line string) (map[string]string, error) {
	var l fixtureLine
	if err := json.Unmarshal([]byte(line), &l); err != nil {
		return nil, errors.New("the fixture server's first line is not a JSON object")
	}
	if l.OutboundSendsEnabled == nil || *l.OutboundSendsEnabled {
		return nil, errors.New("the fixture server does not say that outbound sends are off: refusing to sign in to it")
	}
	u, err := url.Parse(l.BaseURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("the fixture server's line has no usable base_url")
	}
	if !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("the fixture server is not on loopback: refusing to sign in to it")
	}
	if len(l.Accounts) == 0 || l.Accounts[0].Key == "" {
		return nil, errors.New("the fixture server's line has no account key")
	}
	return map[string]string{"base_url": l.BaseURL, "app_key": l.Accounts[0].Key}, nil
}

func isLoopbackHost(host string) bool {
	addr, err := netip.ParseAddr(host)
	return host == "localhost" || (err == nil && addr.IsLoopback())
}

// The ready line of the fixture server is where the sign-in must not go wrong: only the loopback server with sending off
// is signed in to, and with the read key.
func TestSignInValuesOnlyForALoopbackServerWithSendingOff(t *testing.T) {
	const loopback = `{"base_url":"http://127.0.0.1:41000","outbound_sends_enabled":false,"port":41000,
		"accounts":[{"account":"primary","key":"example-key-read","send_key":"example-key-send"},{"account":"secondary","key":"example-key-other"}]}`
	got, err := signInValues(loopback)
	if err != nil || got["base_url"] != "http://127.0.0.1:41000" || got["app_key"] != "example-key-read" || len(got) != 2 {
		t.Fatalf("the loopback fixture server: %v, %v", got, err)
	}
	for name, line := range map[string]string{
		"outbound sends on":      strings.Replace(loopback, `"outbound_sends_enabled":false`, `"outbound_sends_enabled":true`, 1),
		"outbound sends unsaid":  strings.Replace(loopback, `"outbound_sends_enabled":false,`, ``, 1),
		"a server off loopback":  strings.Replace(loopback, "127.0.0.1", "chat.example.com", 1),
		"no account":             strings.Replace(loopback, `"accounts":[`, `"accounts":[],"more":[`, 1),
		"not a JSON object line": "listening on 127.0.0.1",
	} {
		if got, err := signInValues(line); err == nil {
			t.Errorf("%s: signed in with %v", name, got)
		}
	}
}

// startFixtureServer runs the fixture server and returns the values for the program's sign-in from its ready line.
func startFixtureServer(t *testing.T, script string) map[string]string {
	t.Helper()
	cmd := exec.Command("node", script)
	cmd.Env = append(os.Environ(), "PORT=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("fixture server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() && stderr.Len() > 0 {
			t.Logf("fixture server stderr:\n%s", stderr.String())
		}
	})
	lines := make(chan string, 1)
	go func() {
		if sc := bufio.NewScanner(out); sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(30 * time.Second):
		t.Fatal("the fixture server printed nothing in 30 seconds")
	}
	login, err := signInValues(line)
	if err != nil {
		t.Fatal(err)
	}
	return login
}
