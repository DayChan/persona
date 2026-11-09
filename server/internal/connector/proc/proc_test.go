package proc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The test binary is also the program: run with PERSONA_PROC_CHILD=1 it serves exampleProgram on stdin / stdout.
const childVersion = "v-test\nchannel dev, build 0"

func TestMain(m *testing.M) {
	if os.Getenv("PERSONA_PROC_CHILD") == "1" {
		if len(os.Args) > 1 && os.Args[1] == "version" {
			fmt.Println(childVersion)
			os.Exit(0)
		}
		if os.Getenv("PERSONA_PROC_KIND") == "loud" { // a log line far longer than a pipe holds, before it serves
			fmt.Fprintf(os.Stderr, "%s\nafter the long line\n", strings.Repeat("x", 200<<10))
		}
		if os.Getenv("PERSONA_PROC_KIND") == "raw-recall" { // an extension written against the protocol alone (recall_test.go)
			serveRawRecall()
			os.Exit(0)
		}
		err := serve.Main(context.Background(), os.Stdin, os.Stdout, func(cfg json.RawMessage, s connector.Secrets) (connector.Connector, error) {
			switch os.Getenv("PERSONA_PROC_KIND") {
			case "recall-go", "recall-go-undeclared":
				return &recallGoProgram{exampleProgram: &exampleProgram{secrets: s}, declare: os.Getenv("PERSONA_PROC_KIND") == "recall-go"}, nil
			case "settings":
				return &settingsProgram{}, nil
			case "slow-tools":
				return &slowProgram{exampleProgram: &exampleProgram{secrets: s}, tools: true}, nil
			case "slow-status":
				return &slowProgram{exampleProgram: &exampleProgram{secrets: s}}, nil
			case "flaky-settings":
				return &flakySettingsProgram{settingsProgram: &settingsProgram{}}, nil
			case "chatty":
				return &chattyProgram{}, nil
			case "refs":
				return refProgram{&exampleProgram{secrets: s}}, nil
			case "history":
				return &historyProgram{exampleProgram: &exampleProgram{secrets: s}}, nil
			}
			return &exampleProgram{secrets: s, exitSoon: os.Getenv("PERSONA_PROC_EXIT") == "1"}, nil
		})
		if err != nil && !strings.Contains(err.Error(), "EOF") {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// exampleProgram signs in by phone and code, archives one made-up chat, reads, sends (an unlisted action) and tells
// about changes; no inbox.
type exampleProgram struct {
	secrets  connector.Secrets
	exitSoon bool
	once     sync.Once
	sent     int
}

func (*exampleProgram) ID() string   { return "example" }
func (*exampleProgram) Name() string { return "Example" }

func (*exampleProgram) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools, connector.CapActions,
		connector.CapEvents, connector.CapArchive, connector.CapItems, connector.CapLogin, connector.CapThreads}, AgentTools: []string{"example_chats"},
		Unlisted: []string{"example_send"}, Poll: 3 * time.Minute}
}

func (p *exampleProgram) Status(ctx context.Context) connector.Status {
	if p.exitSoon {
		p.once.Do(func() { go func() { time.Sleep(50 * time.Millisecond); os.Exit(3) }() })
	}
	if s, _ := p.secrets.Load(ctx); s == nil {
		return connector.Status{State: connector.StateNotSignedIn, Error: "还没登录"}
	}
	return connector.Status{State: connector.StateOK, Account: "示例用户"}
}

func (p *exampleProgram) LoginStep(ctx context.Context, step string, v map[string]string) (connector.Prompt, error) {
	code := connector.Prompt{Step: "code", Fields: []connector.Field{{Name: "code", Label: "验证码"}}}
	switch step {
	case "phone":
		return code, nil
	case "code":
		if v["code"] != "12345" {
			code.Error = "验证码不对"
			return code, nil
		}
		return connector.Prompt{Done: true, Account: "示例用户"}, p.secrets.Save(ctx, []byte("example session"))
	}
	return connector.Prompt{Step: "phone", Fields: []connector.Field{{Name: "phone", Label: "手机号"}}}, nil
}

func (p *exampleProgram) Logout(ctx context.Context) error { return p.secrets.Delete(ctx) }

func (*exampleProgram) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	if cursor == "1" {
		return nil, "1", false, nil
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return []connector.ChatBatch{{Chat: kb.SourceChat{ID: "g1", Name: "示例群", Kind: "group"},
		Messages: []kb.SourceMessage{{ID: "1", SenderName: "示例用户", Text: "示例消息", At: at}}}}, "1", false, nil
}

func (*exampleProgram) Tools() []proto.Tool { return []proto.Tool{{Name: "example_chats"}} }
func (*exampleProgram) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	return name + ": 2 chats", nil
}

func (*exampleProgram) Actions() []proto.Tool { return []proto.Tool{{Name: "example_send"}} }
func (*exampleProgram) Describe(_ context.Context, name string, args json.RawMessage) (connector.Action, string, error) {
	var a struct{ Text string }
	if err := json.Unmarshal(args, &a); err != nil || a.Text == "" {
		return connector.Action{}, "", fmt.Errorf("text is required")
	}
	return connector.Action{Name: name, Visible: "示例群的所有人"}, "发到示例群：" + a.Text, nil
}
func (p *exampleProgram) Do(_ context.Context, _ string, args json.RawMessage) (string, error) {
	p.sent++
	return fmt.Sprintf("sent #%d: %s", p.sent, args), nil
}

func (*exampleProgram) Thread(_ context.Context, thread, before string, limit int) (connector.ThreadPage, error) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return connector.ThreadPage{Chat: connector.ThreadChat{ID: thread, Name: "示例群", Kind: "group", Reply: "example_send"},
		Messages: []connector.ThreadMessage{{ID: "7", At: at, Sender: connector.ThreadPerson{ID: "u1", Name: "示例用户", Avatar: "avatar:u1"},
			Text: "示例消息", Media: []connector.ThreadMedia{{Kind: "sticker", Ref: "sticker:7", Emoji: "👍", Width: 512, Height: 512}},
			ReplyTo: &connector.ThreadQuote{ID: "6", From: "我", Text: "示例问题"}}}, Before: before + "6"}, nil
}

func (*exampleProgram) Media(_ context.Context, ref string) (connector.Media, error) {
	return connector.Media{Mime: "image/webp", Data: []byte{0, 1, 2, 255}}, nil
}

// ArchiveMedia hands an archived message's attachment over; ItemSource has one meeting and knows one title.
func (*exampleProgram) ArchiveMedia(_ context.Context, ref string) (connector.Media, error) {
	if ref != "file:1" {
		return connector.Media{}, errors.New("no such attachment")
	}
	return connector.Media{Mime: "application/pdf", Data: []byte("%PDF-1.7")}, nil
}

func (*exampleProgram) ItemSource() kb.ItemSource { return exampleItems{} }

type exampleItems struct{}

func (exampleItems) Items(context.Context) ([]kbwire.Item, error) {
	return []kbwire.Item{{Kind: kb.KindMeeting, ID: "m1", Title: "示例周会", At: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC), Where: "meetings"}}, nil
}

func (exampleItems) DocTitle(_ context.Context, u string) string {
	if u == "https://example.com/docx/1" {
		return "示例文档"
	}
	return ""
}

func (*exampleProgram) Watch(ctx context.Context, changed func(connector.Scope)) error {
	changed(connector.ScopeChats)
	<-ctx.Done()
	return nil
}

// refProgram is exampleProgram that also says which message each write made (connector.RefActions).
type refProgram struct{ *exampleProgram }

func (p refProgram) DoRef(ctx context.Context, name string, args json.RawMessage) (connector.DoResult, error) {
	text, err := p.exampleProgram.Do(ctx, name, args)
	return connector.DoResult{Text: text, MessageRef: fmt.Sprintf("msg-%d", p.sent)}, err
}

// slowProgram is exampleProgram hanging (longer than the tests' callTimeout) on its second and later tools list, or
// status when not tools.
type slowProgram struct {
	*exampleProgram
	tools bool
	calls atomic.Int32
}

func (p *slowProgram) hang() {
	if p.calls.Add(1) > 1 {
		time.Sleep(5 * time.Second)
	}
}

func (p *slowProgram) Tools() []proto.Tool {
	if p.tools {
		p.hang()
	}
	return p.exampleProgram.Tools()
}

func (p *slowProgram) Status(ctx context.Context) connector.Status {
	if !p.tools {
		p.hang()
	}
	return p.exampleProgram.Status(ctx)
}

// memSecrets is the hub's store for one connector, in memory.
type memSecrets struct {
	mu    sync.Mutex
	data  []byte
	loads int
}

func (m *memSecrets) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	return m.data, nil
}
func (m *memSecrets) Save(_ context.Context, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = d
	return nil
}
func (m *memSecrets) Delete(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = nil
	return nil
}
func (m *memSecrets) get() ([]byte, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data, m.loads
}

func program(t *testing.T, s connector.Secrets, env ...string) Config {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Config{ID: "example", Name: "Example", Path: exe, Env: append([]string{"PERSONA_PROC_CHILD=1"}, env...),
		Version: childVersion, Secrets: s, Logf: t.Logf}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The program runs under the hub: it says what it has, signs in a step at a time with its session kept by the hub,
// archives, reads, and its changes reach the hub's Watch.
func TestProgram(t *testing.T) {
	sec := &memSecrets{}
	exercise(t, New(program(t, sec)), sec)
}

// linked is the program run as a plugin by the plugin host, the connector reaching it through its Link (the hub's
// way, docs/extensions/extensions.md); the hub's part (passing the program's calls and events to the connector) is done here.
func linked(t *testing.T, s connector.Secrets, env ...string) *Conn {
	t.Helper()
	return linkedAs(t, s, (&exampleProgram{}).Manifest(), env...)
}

// linkedAs is linked with the manifest the hub knows before the program runs.
func linkedAs(t *testing.T, s connector.Secrets, manifest connector.Manifest, env ...string) *Conn {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var c *Conn
	p := host.Start(context.Background(), host.Spec{ID: "example-plugin", Name: "Example", Command: exe, RPC: true,
		Env: append([]string{"PERSONA_PROC_CHILD=1"}, env...), CallTimeout: callTimeout, Logf: t.Logf,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Extension: extension.ExtensionInfo{ID: "example-plugin", Version: "1.0.0"},
				Active: map[string][]string{"connectors": {"example"}}}, nil
		},
		Handle: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if pserve.Contribution(params) != "example" {
				return nil, errors.New("another connector's")
			}
			return c.Handle(ctx, strings.TrimPrefix(method, "connector."), params)
		},
		OnEvent: func(event string, params json.RawMessage) { c.Event(strings.TrimPrefix(event, "connector."), params) },
	})
	t.Cleanup(p.Stop)
	cfg := Config{ID: "example", Name: "Example", Manifest: manifest, Secrets: s, Logf: t.Logf,
		Link: &Link{Proc: p, ID: "example"}}
	c = New(cfg)
	return c
}

func TestProgramAsPlugin(t *testing.T) {
	sec := &memSecrets{}
	exercise(t, linked(t, sec), sec)
}

func exercise(t *testing.T, c *Conn, sec *memSecrets) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if st := c.Status(ctx); st.State != connector.StateError {
		t.Fatalf("before Run: %+v", st)
	}
	go c.Run(ctx)
	waitUntil(t, "the program's status", func() bool { return c.Status(ctx).State == connector.StateNotSignedIn })
	if got := connector.Capabilities(c); !slices.Equal(got, []string{"status", "tools", "actions", "events", "archive", "items", "login", "threads"}) {
		t.Fatalf("the registry lists %v", got)
	}
	if m := c.Manifest(); !m.Proxy || !m.ForAgents("example_chats") || m.Poll != 3*time.Minute || m.Listed("example_send") {
		t.Fatalf("manifest %+v", m)
	}
	if _, ok := connector.As[connector.Inbox](c); ok {
		t.Fatal("no inbox, yet the hub would collect it")
	}

	p, err := c.LoginStep(ctx, "", nil)
	if err != nil || p.Step != "phone" {
		t.Fatalf("start %+v %v", p, err)
	}
	if p, _ = c.LoginStep(ctx, "phone", map[string]string{"phone": "+1 555 0100"}); p.Step != "code" {
		t.Fatalf("phone %+v", p)
	}
	if p, _ = c.LoginStep(ctx, "code", map[string]string{"code": "0"}); p.Error != "验证码不对" {
		t.Fatalf("wrong code %+v", p)
	}
	if p, _ = c.LoginStep(ctx, "code", map[string]string{"code": "12345"}); !p.Done {
		t.Fatalf("signed in %+v", p)
	}
	if d, _ := sec.get(); string(d) != "example session" {
		t.Fatalf("the hub keeps %q", d)
	}
	if st := c.Status(ctx); st.State != connector.StateOK || st.Account != "示例用户" {
		t.Fatalf("after signing in %+v", st)
	}

	batches, next, _, err := c.Changes(ctx, "", 20)
	if err != nil || next != "1" || len(batches) != 1 || batches[0].Messages[0].Text != "示例消息" || !batches[0].Messages[0].At.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("changes %+v %q %v", batches, next, err)
	}
	if tools := c.Tools(); len(tools) != 1 || tools[0].Name != "example_chats" {
		t.Fatalf("tools %+v", tools)
	}
	if out, err := c.Call(ctx, "example_chats", json.RawMessage(`{}`)); err != nil || out != "example_chats: 2 chats" {
		t.Fatalf("call %q %v", out, err)
	}
	// A write: described for the card, then run with the very args; each run is its own (a new request id).
	if acts := c.Actions(); len(acts) != 1 || acts[0].Name != "example_send" {
		t.Fatalf("actions %+v", acts)
	}
	if act, summary, err := c.Describe(ctx, "example_send", json.RawMessage(`{"text":"示例回复"}`)); err != nil ||
		act.Visible != "示例群的所有人" || summary != "发到示例群：示例回复" {
		t.Fatalf("describe %+v %q %v", act, summary, err)
	}
	if _, _, err := c.Describe(ctx, "example_send", json.RawMessage(`{}`)); err == nil || err.Error() != "text is required" {
		t.Fatalf("describe without text: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if out, err := c.Do(ctx, "example_send", json.RawMessage(`{"text":"示例回复"}`)); err != nil || out != fmt.Sprintf(`sent #%d: {"text":"示例回复"}`, i) {
			t.Fatalf("do %d: %q %v", i, out, err)
		}
	}
	// A thread's page and a media's bytes over the wire, every field as the program gave it.
	page, err := c.Thread(ctx, "g1", "", 20)
	if err != nil || page.Chat.Reply != "example_send" || len(page.Messages) != 1 || page.Before != "6" {
		t.Fatalf("thread %+v %v", page, err)
	}
	if m := page.Messages[0]; m.ReplyTo == nil || m.ReplyTo.Text != "示例问题" || m.Media[0].Emoji != "👍" || m.Sender.Avatar != "avatar:u1" ||
		!m.At.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("message %+v", m)
	}
	if m, err := c.Media(ctx, "sticker:7"); err != nil || m.Mime != "image/webp" || !bytes.Equal(m.Data, []byte{0, 1, 2, 255}) {
		t.Fatalf("media %+v %v", m, err)
	}
	if m, err := c.ArchiveMedia(ctx, "file:1"); err != nil || m.Mime != "application/pdf" || string(m.Data) != "%PDF-1.7" {
		t.Fatalf("archive media %+v %v", m, err)
	}
	if _, err := c.ArchiveMedia(ctx, "file:2"); err == nil {
		t.Fatal("an attachment it does not have")
	}
	is, ok := connector.As[connector.Items](c)
	if !ok {
		t.Fatal("declared items, yet no item source")
	}
	if items, err := is.ItemSource().Items(ctx); err != nil || len(items) != 1 || items[0].Title != "示例周会" || items[0].Kind != kb.KindMeeting || !items[0].At.Equal(time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("items %+v %v", items, err)
	}
	if title := is.ItemSource().DocTitle(ctx, "https://example.com/docx/1"); title != "示例文档" {
		t.Fatalf("doc title %q", title)
	}
	got := make(chan connector.Scope, 4)
	wctx, wcancel := context.WithCancel(ctx)
	go c.Watch(wctx, func(scope connector.Scope) { got <- scope })
	select {
	case s := <-got:
		if s != connector.ScopeChats {
			t.Fatalf("scope %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the program's change did not reach the hub")
	}
	wcancel()

	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if d, _ := sec.get(); d != nil {
		t.Fatal("signed out, the session is still kept")
	}
	cancel()
	waitUntil(t, "the program to stop", func() bool { return strings.Contains(c.Status(context.Background()).Error, "已停止") })
}

// As a plugin: signing in while it does not run says to switch it on; signing out runs it just for that.
func TestPluginWhileStopped(t *testing.T) {
	sec := &memSecrets{data: []byte("example session")}
	c := linked(t, sec)
	if p, err := c.LoginStep(context.Background(), "", nil); err != nil || !strings.Contains(p.Error, "先打开") {
		t.Fatalf("login: %+v %v", p, err)
	}
	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d, _ := sec.get(); d != nil {
		t.Fatal("still signed in")
	}
	if st := c.cfg.Link.Proc; st.Ready() {
		waitUntil(t, "it to stop again", func() bool { s, _ := st.Status(); return s == host.StateStopped })
	}
}

// A program from another build is not started: the two must match.
func TestProgramOfAnotherVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sec := &memSecrets{}
	cfg := program(t, sec)
	cfg.Version = "v-other\nchannel release, build 202601010000"
	c := New(cfg)
	go c.Run(ctx)
	waitUntil(t, "the version check", func() bool { return strings.Contains(c.Status(ctx).Error, "不是同一个版本") })
	if _, loads := sec.get(); loads != 0 {
		t.Fatal("the program ran")
	}
	if p, err := c.LoginStep(ctx, "", nil); err != nil || p.Error == "" {
		t.Fatalf("signing in without the program: %+v %v", p, err)
	}
}

// A program that exits is started again; meanwhile its status says it is restarting.
func TestProgramRestarts(t *testing.T) {
	restartFirst = 20 * time.Millisecond
	t.Cleanup(func() { restartFirst = 5 * time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sec := &memSecrets{}
	c := New(program(t, sec, "PERSONA_PROC_EXIT=1"))
	go c.Run(ctx)
	waitUntil(t, "a restart", func() bool { _, loads := sec.get(); return loads >= 2 })
}

// Signing out while the connector is switched off (the program not running) runs the program just for that.
func TestLogoutWhileStopped(t *testing.T) {
	sec := &memSecrets{data: []byte("example session")}
	c := New(program(t, sec))
	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d, _ := sec.get(); d != nil {
		t.Fatal("still signed in")
	}
}

// A connector whose program never ran (switched off from the start) answers with the manifest it was registered
// with: the capabilities are listed, signing in says to switch it on, signing out runs the program for that (#666).
func TestNeverStartedDeclaresItsManifest(t *testing.T) {
	ctx := context.Background()
	sec := &memSecrets{data: []byte("example session")}
	cfg := program(t, sec)
	cfg.Manifest = (&exampleProgram{}).Manifest()
	c := New(cfg)
	if got := connector.Capabilities(c); !slices.Equal(got, []string{"status", "tools", "actions", "events", "archive", "items", "login", "threads"}) {
		t.Fatalf("before it ever ran: %v", got)
	}
	l, ok := connector.As[connector.Login](c)
	if !ok {
		t.Fatal("no sign-in before it ever ran")
	}
	if p, err := l.LoginStep(ctx, "", nil); err != nil || !strings.Contains(p.Error, "先打开") {
		t.Fatalf("login: %+v %v", p, err)
	}
	if err := l.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if d, _ := sec.get(); d != nil {
		t.Fatal("still signed in")
	}
}

// The program declaring something else than the hub registered it with is logged; the hub's declaration stays.
func TestProgramDeclaringOtherwiseIsLogged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := program(t, &memSecrets{})
	cfg.Manifest = connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapLogin}, ConfirmAlways: []string{"example_send"}}
	var mu sync.Mutex
	var logged []string
	cfg.Logf = func(f string, a ...any) { mu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); mu.Unlock() }
	c := New(cfg)
	go c.Run(ctx)
	waitUntil(t, "the program's status", func() bool { return c.Status(ctx).State == connector.StateNotSignedIn })
	mu.Lock()
	defer mu.Unlock()
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, "the hub registered it with") }) {
		t.Fatalf("not logged: %q", logged)
	}
	if got := connector.Capabilities(c); !slices.Equal(got, []string{"status", "login"}) {
		t.Fatalf("listed %v", got)
	}
	// The program says no confirm_always, but the hub's one is what the gate gets: a program cannot relax the clamp.
	if got := c.Manifest().ConfirmAlways; !slices.Equal(got, []string{"example_send"}) {
		t.Fatalf("confirm_always %v", got)
	}
	// The same declaration in another order is the same.
	if !sameManifest(connector.Manifest{Capabilities: []connector.Capability{connector.CapLogin, connector.CapStatus}},
		connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapLogin}, Proxy: true}) {
		t.Fatal("order matters")
	}
	// Archive sources and the command line are part of the declaration too.
	if sameManifest(connector.Manifest{ArchiveSources: []string{"moments"}}, connector.Manifest{Proxy: true}) {
		t.Fatal("archive_sources ignored")
	}
	if sameManifest(connector.Manifest{CLI: "ex"}, connector.Manifest{CLI: "other", Proxy: true}) {
		t.Fatal("cli ignored")
	}
}

// shortCalls makes the hub give up on a program's call soon, for the slow programs.
func shortCalls(t *testing.T) {
	callTimeout = 500 * time.Millisecond
	t.Cleanup(func() { callTimeout = 2 * time.Minute })
}

// runSlow runs the slow program of kind until its first refresh has the tools.
func runSlow(t *testing.T, kind string) *Conn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := New(program(t, &memSecrets{data: []byte("example session")}, "PERSONA_PROC_KIND="+kind))
	go c.Run(ctx)
	waitUntil(t, "the first refresh", func() bool { return len(c.Tools()) > 0 && c.Status(context.Background()).State == connector.StateOK })
	return c
}

// A tools list that times out in a refresh keeps the last one: the assistant does not lose the connector's tools
// until the next refresh.
func TestRefreshKeepsToolsWhenTheirCallFails(t *testing.T) {
	shortCalls(t)
	c := runSlow(t, "slow-tools")
	if _, err := c.LoginStep(context.Background(), "phone", nil); err != nil { // refreshes after
		t.Fatal(err)
	}
	if got := c.Tools(); len(got) != 1 || got[0].Name != "example_chats" {
		t.Fatalf("tools after a timed-out list = %+v, want the first list kept", got)
	}
	if got := c.Actions(); len(got) != 1 || got[0].Name != "example_send" {
		t.Fatalf("actions = %+v", got)
	}
	if st := c.Status(context.Background()); st.State != connector.StateOK {
		t.Fatalf("status = %+v, want ok (the status call answered)", st)
	}
}

// A status call that times out puts the connector in error instead of leaving a hung program's last ok.
func TestRefreshStatusTimeoutIsAnError(t *testing.T) {
	shortCalls(t)
	c := runSlow(t, "slow-status")
	if _, err := c.LoginStep(context.Background(), "phone", nil); err != nil {
		t.Fatal(err)
	}
	st := c.Status(context.Background())
	if st.State != connector.StateError || !strings.Contains(st.Error, "Example 没有回应") {
		t.Fatalf("status after a hung status call = %+v, want an error", st)
	}
	if len(c.Tools()) == 0 {
		t.Fatal("the tools are gone")
	}
}

// A program answering no_capability for tools has none: that is not a failure that keeps an old list.
func TestRefreshNoCapabilityIsEmpty(t *testing.T) {
	got, err := listTools(func(string, any, any) error {
		return &rpc.Error{Code: serve.CodeNoCapability, Message: "none"}
	}, serve.MTools)
	if err != nil || got != nil {
		t.Fatalf("listTools = %v, %v; want none", got, err)
	}
	if _, err := listTools(func(string, any, any) error { return context.DeadlineExceeded }, serve.MTools); err == nil {
		t.Fatal("a timed-out list is not an error")
	}
}

// chattyProgram tells of a change many times at once when its chatty_burst tool is called, and its status is slow:
// it counts how many status calls run at the same time, which chatty_max tells as "most in-flight calls".
type chattyProgram struct {
	mu       sync.Mutex
	changed  func(connector.Scope)
	inFlight int
	most     int
	calls    int
}

func (*chattyProgram) ID() string   { return "example" }
func (*chattyProgram) Name() string { return "Example" }

func (*chattyProgram) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools, connector.CapEvents}}
}

func (p *chattyProgram) Status(context.Context) connector.Status {
	p.mu.Lock()
	p.calls++
	p.inFlight++
	p.most = max(p.most, p.inFlight)
	p.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
	return connector.Status{State: connector.StateOK, Account: "chatty"}
}

func (p *chattyProgram) Watch(ctx context.Context, changed func(connector.Scope)) error {
	p.mu.Lock()
	p.changed = changed
	p.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (*chattyProgram) Tools() []proto.Tool {
	return []proto.Tool{{Name: "chatty_burst"}, {Name: "chatty_max"}}
}

func (p *chattyProgram) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch name {
	case "chatty_burst":
		if p.changed == nil {
			return "", errors.New("not watching yet")
		}
		for range 20 {
			p.changed(connector.ScopeInbox)
		}
		return "sent", nil
	case "chatty_max":
		return fmt.Sprintf("%d %d %d", p.most, p.inFlight, p.calls), nil
	}
	return "", fmt.Errorf("no tool %s", name)
}

// A burst of changed events is one refresh running at a time (and one more after it), not a status call per event
// all in flight together, where a slower, older answer could land after a newer one.
func TestChattyProgramAsPlugin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := linked(t, &memSecrets{}, "PERSONA_PROC_KIND=chatty")
	go c.Run(ctx)
	scopes := make(chan connector.Scope, 64)
	go func() { _ = c.Watch(ctx, func(s connector.Scope) { scopes <- s }) }()
	waitUntil(t, "the program's status", func() bool { return c.Status(ctx).Account == "chatty" })
	stats := func() (most, inFlight, calls int) {
		out, err := c.Call(ctx, "chatty_max", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Sscan(out, &most, &inFlight, &calls); err != nil {
			t.Fatalf("chatty_max said %q: %v", out, err)
		}
		return most, inFlight, calls
	}
	_, _, before := stats()
	waitUntil(t, "the program watching", func() bool {
		out, err := c.Call(ctx, "chatty_burst", nil)
		return err == nil && out == "sent"
	})
	waitUntil(t, "the burst's events", func() bool { return len(scopes) >= 1 }) // coalesced per scope
	waitUntil(t, "the refreshes to finish", func() bool {
		_, inFlight, calls := stats()
		return inFlight == 0 && calls > before
	})
	time.Sleep(300 * time.Millisecond) // a refresh still to come shows here
	if most, inFlight, calls := stats(); most > 1 {
		t.Fatalf("%d status calls ran at once (%d still running, %d in all) for one burst of changes", most, inFlight, calls)
	}
}

// A refresh round several ask for meanwhile still runs when the last of them gave up: a sign-in whose client went
// away does not cancel the round a changed event asked for too.
func TestSharedRefreshOutlivesAnAskerWhoGaveUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := linked(t, &memSecrets{}, "PERSONA_PROC_KIND=chatty")
	go c.Run(ctx)
	waitUntil(t, "the program's status", func() bool { return c.Status(ctx).Account == "chatty" })
	calls := func() int {
		out, err := c.Call(ctx, "chatty_max", nil)
		if err != nil {
			t.Fatal(err)
		}
		var most, inFlight, n int
		if _, err := fmt.Sscan(out, &most, &inFlight, &n); err != nil {
			t.Fatalf("chatty_max said %q: %v", out, err)
		}
		return n
	}
	before := calls()
	first := c.kick(ctx) // in flight: its status call takes a while
	shared := c.kick(ctx)
	gone, giveUp := context.WithCancel(ctx)
	giveUp()
	if c.kick(gone) != shared {
		t.Fatal("an ask while a refresh runs does not share the round due next")
	}
	for _, done := range []<-chan struct{}{first, shared} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the refresh did not end")
		}
	}
	if got := calls() - before; got != 2 {
		t.Fatalf("%d status calls for a round and the one shared after it, want 2", got)
	}
}

// A program whose log has a line longer than any buffer is still read to the end: its stderr does not fill up and
// stop it before it answers (#3959). The long line is cut, the next one is logged whole.
func TestProgramWithALongLogLine(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	cfg := program(t, &memSecrets{data: []byte("example session")}, "PERSONA_PROC_KIND=loud")
	cfg.Logf = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(cfg)
	go c.Run(ctx)
	waitUntil(t, "the program to answer", func() bool { return c.Status(context.Background()).State == connector.StateOK })
	mu.Lock()
	defer mu.Unlock()
	var cut, after bool
	for _, l := range logs {
		cut = cut || strings.HasSuffix(l, "x…") && len(l) < 70<<10
		after = after || strings.HasSuffix(l, ": after the long line")
	}
	if !cut || !after {
		t.Fatalf("long line cut %v, next line logged %v", cut, after)
	}
}

func TestLogLinesReadsPastALongLine(t *testing.T) {
	var got []string
	c := &Conn{cfg: Config{ID: "x", Logf: func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) }}}
	long := strings.Repeat("y", 3*logLineMost)
	c.logLines(strings.NewReader("one\r\n" + long + "\n\ntwo\nlast without newline"))
	want := []string{"connector x: one", "connector x: " + long[:logLineMost] + "…", "connector x: ", "connector x: two",
		"connector x: last without newline"}
	if !slices.Equal(got, want) {
		short := make([]string, len(got))
		for i, l := range got {
			short[i] = l[:min(len(l), 40)]
		}
		t.Fatalf("logged %d lines: %q", len(got), short)
	}
}

// Changed events wait for Watch per scope: a full backlog of one scope (Watch not draining yet, or backing off)
// does not drop another scope's change, which would leave, say, the inbox uncollected.
func TestChangedEventsKeepEveryScope(t *testing.T) {
	c := New(Config{ID: "example", Name: "Example"})
	for range 16 {
		c.onEvent(serve.EChanged, json.RawMessage(`{"scope":"chats"}`))
	}
	c.onEvent(serve.EChanged, json.RawMessage(`{"scope":"inbox"}`))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	seen := map[connector.Scope]int{}
	_ = c.Watch(ctx, func(s connector.Scope) {
		seen[s]++
		if seen[connector.ScopeChats] > 0 && seen[connector.ScopeInbox] > 0 {
			cancel()
		}
	})
	if seen[connector.ScopeInbox] == 0 {
		t.Fatalf("Watch passed on %v: the inbox change was dropped", seen)
	}
	if seen[connector.ScopeChats] != 1 {
		t.Fatalf("Watch passed on %v: the chats changes are one change", seen)
	}
}

// A program that says which message a write made has it reach the hub with the write's answer, over the pipe and as a
// plugin; one that does not (an older program, which answers the text alone) leaves it empty, and its write runs and
// answers as before.
func TestWriteSaysWhichMessageItMade(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		refs [2]string
	}{
		{"a program with refs", []string{"PERSONA_PROC_KIND=refs"}, [2]string{"msg-1", "msg-2"}},
		{"a program without", nil, [2]string{"", ""}},
	} {
		for _, way := range []string{"pipe", "plugin"} {
			t.Run(tc.name+" over the "+way, func(t *testing.T) {
				sec := &memSecrets{}
				c := New(program(t, sec, tc.env...))
				if way == "plugin" {
					c = linked(t, sec, tc.env...)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go c.Run(ctx)
				waitUntil(t, "the program's status", func() bool { return c.Status(ctx).State == connector.StateNotSignedIn })
				args := json.RawMessage(`{"text":"示例回复"}`)
				for i, ref := range tc.refs {
					wantText := fmt.Sprintf(`sent #%d: {"text":"示例回复"}`, i+1)
					done, err := connector.Write(ctx, c, "example_send", args)
					if err != nil || done.Text != wantText || done.MessageRef != ref {
						t.Fatalf("write %d: %+v %v, want %q with ref %q", i+1, done, err, wantText, ref)
					}
				}
				// Do is still the text alone.
				if out, err := c.Do(ctx, "example_send", args); err != nil || out != `sent #3: {"text":"示例回复"}` {
					t.Fatalf("Do: %q %v", out, err)
				}
			})
		}
	}
}

// A program's connector implements every capability interface as a proxy, so it is a Recaller only when its manifest
// declares recall, or the ledger would offer a recall it cannot make: this one does not.
func TestAProgramThatDoesNotDeclareRecallCannotRecall(t *testing.T) {
	if _, ok := connector.RecallerOf(New(program(t, &memSecrets{}))); ok {
		t.Fatal("a program's connector that does not declare recall is a Recaller")
	}
}
