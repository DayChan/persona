package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const remoteRaw = "11111111-2222-3333-4444-555555555555"
const remoteOther = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

type remoteFixture struct {
	mu            sync.Mutex
	home          string
	threads       []string
	got           []codexMessage
	socket        *websocket.Conn
	replies       []codexMessage
	drop          string
	reject        string
	rejectCode    int    // the error code a rejected call gets; 0: -32600
	hangup        string // a call to it closes the connection instead of an answer
	resumeRequest bool
	ephemeral     string
	// models answers model/list; threadModel / threadEffort are the threads' configured model and effort (codex_effort.go).
	models                    any
	threadModel, threadEffort string
	// archive plays thread/archive and thread/unarchive on the home's files (archive_test.go), before any reject.
	archive func(method, thread string)
	// backgroundReply overrides the versioned inventory fixture's terminal response.
	backgroundReply map[string]any
	backgroundPages map[string]map[string]any
}

func (f *remoteFixture) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	f.mu.Lock()
	f.socket = c
	f.mu.Unlock()
	for {
		_, b, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var m codexMessage
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		f.mu.Lock()
		f.got = append(f.got, m)
		ids := append([]string(nil), f.threads...)
		drop, reject, code, ephemeral, hangup := f.drop, f.reject, f.rejectCode, f.ephemeral, f.hangup
		models, model, effort, archive := f.models, f.threadModel, f.threadEffort, f.archive
		background := f.backgroundReply
		backgroundPages := f.backgroundPages
		f.mu.Unlock()
		if code == 0 {
			code = -32600
		}
		if m.Method == "" {
			f.mu.Lock()
			f.replies = append(f.replies, m)
			f.mu.Unlock()
			continue
		}
		if len(m.ID) == 0 {
			continue
		}
		if m.Method == drop {
			continue
		}
		if m.Method == hangup {
			return
		}
		if archive != nil && (m.Method == "thread/archive" || m.Method == "thread/unarchive") {
			var p struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			archive(m.Method, p.ThreadID)
		}
		if m.Method == reject {
			f.push(map[string]any{"id": m.ID, "error": map[string]any{"code": code, "message": "rejected"}})
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(m.Params, &p)
		var result any = map[string]any{}
		thread, _ := p["threadId"].(string)
		d := map[string]any{"id": thread, "name": "合成会话", "cwd": f.home, "status": map[string]any{"type": "idle"}}
		if model != "" {
			d["model"] = model
		}
		if effort != "" {
			d["reasoningEffort"] = effort
		}
		switch m.Method {
		case "initialize":
			result = map[string]any{"codexHome": f.home}
		case "thread/loaded/list":
			if p["cursor"] == nil && len(ids) > 1 {
				result = map[string]any{"data": ids[:1], "nextCursor": "page2"}
			} else {
				if len(ids) > 1 {
					ids = ids[1:]
				}
				result = map[string]any{"data": ids}
			}
		case "thread/unsubscribe":
			result = map[string]any{"status": "unsubscribed"}
		case "thread/read":
			if thread == ephemeral {
				// What Codex 0.166's TUI leaves loaded after naming a thread.
				d = map[string]any{"id": thread, "ephemeral": true, "path": nil, "threadSource": "thread_title", "cwd": f.home, "status": map[string]any{"type": "active", "activeFlags": []string{}}}
			}
			result = map[string]any{"thread": d}
		case "thread/backgroundTerminals/list":
			result = map[string]any{"data": []any{}}
			if background != nil {
				result = background
			}
			if backgroundPages != nil {
				cursor, _ := p["cursor"].(string)
				result = backgroundPages[cursor]
			}
		case "thread/resume":
			result = map[string]any{"thread": d}
		case "turn/start":
			result = map[string]any{"turn": map[string]any{"id": "turn-1"}}
			fallthrough // its effort is the thread's from then on, as a settings update's
		case "thread/settings/update":
			if e, ok := p["effort"].(string); ok {
				f.mu.Lock()
				f.threadEffort = e
				f.mu.Unlock()
			}
		case "model/list":
			if models != nil {
				result = models
			}
		}
		f.push(map[string]any{"id": m.ID, "result": result})
		f.mu.Lock()
		request := f.resumeRequest
		f.mu.Unlock()
		if m.Method == "thread/resume" && request {
			f.push(map[string]any{"id": 71, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": thread, "itemId": "cmd-1", "command": "printf synthetic"}})
		}
	}
}

func (f *remoteFixture) push(v any) {
	f.mu.Lock()
	socket := f.socket
	f.mu.Unlock()
	if socket == nil {
		return
	}
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = socket.Write(ctx, websocket.MessageText, b)
}
func (f *remoteFixture) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.got {
		if m.Method == method {
			n++
		}
	}
	return n
}
func (f *remoteFixture) replyCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.replies) }

func remoteTestNode(t *testing.T) *Node {
	n, home := testNode(t)
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.logger = log.New(io.Discard, "", 0)
	n.metas = map[string]quickMeta{}
	n.bgScans = map[string]*bgScan{}
	n.dir = filepath.Join(home, ".persona")
	n.configureCodex(proto.CodexConfig{})
	return n
}

func connectRemoteFixture(t *testing.T, n *Node, home, id string, threads ...string) (*remoteFixture, *codexRemote) {
	t.Helper()
	f := &remoteFixture{home: home, threads: threads}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	ctx, cancel := context.WithCancel(context.Background())
	// The node canonicalizes every server's home before it dials (configureCodex, discoverCodex); macOS's TempDir
	// lives under the /var -> /private/var symlink, so a raw home never matches the app-server's codexHome.
	spec := proto.CodexServer{ID: id, Home: canonicalCodexHome(home, n.home), Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")}
	n.ensureCodexRemote(ctx, spec)
	waitFor(t, "remote connected", func() bool {
		n.codices.mu.Lock()
		c := n.codices.remotes[id]
		n.codices.mu.Unlock()
		if c == nil {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.ready
	})
	n.codices.mu.Lock()
	c := n.codices.remotes[id]
	n.codices.mu.Unlock()
	t.Cleanup(func() { cancel(); c.close(); server.Close() })
	return f, c
}

func TestCodexRemoteIdentityAndOnlyReadDiscovery(t *testing.T) {
	n := remoteTestNode(t)
	h1, h2 := t.TempDir(), t.TempDir()
	f1, _ := connectRemoteFixture(t, n, h1, "one", remoteRaw, remoteOther)
	f2, _ := connectRemoteFixture(t, n, h2, "two", remoteRaw)
	id1 := n.registerCodex(h1, remoteRaw, "")
	id2 := n.registerCodex(h2, remoteRaw, "")
	if id1 == id2 || !validID(id1) || !validID(id2) {
		t.Fatalf("identity collision: %s %s", id1, id2)
	}
	if n.registerCodex(n.codexHome(), remoteRaw, "") != remoteRaw {
		t.Fatal("legacy identity changed")
	}
	if f1.count("thread/resume") != 0 || f2.count("thread/resume") != 0 || f1.count("turn/start") != 0 {
		t.Fatal("discovery changed runtime")
	}
	if f1.count("thread/loaded/list") < 2 {
		t.Fatal("loaded pagination skipped")
	}
	sessions := n.codexSessions(map[string]bool{})
	// The legacy routing identity has neither a rollout nor a live owner.
	if len(sessions) != 3 {
		t.Fatalf("sessions: %d", len(sessions))
	}
	if _, _, err := n.codexOwner(id1); err != nil {
		t.Fatal(err)
	}
}

func TestCodexRemoteSendAndSteerNeverFallback(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	if err := n.send(context.Background(), id, "合成请求", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if f.count("thread/resume") != 1 || f.count("turn/start") != 1 {
		t.Fatal("first message routing")
	}
	f.mu.Lock()
	f.reject = "turn/steer"
	f.mu.Unlock()
	err := n.send(context.Background(), id, "合成插话", nil, sendOpts{})
	if err == nil || f.count("turn/start") != 1 || f.count("turn/steer") != 1 {
		t.Fatalf("steer fallback: %v", err)
	}
	if err := n.interruptCodex(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if f.count("thread/backgroundTerminals/clean") != 0 {
		t.Fatal("stopped external background jobs")
	}
	c.mu.Lock()
	t0 := c.threads[remoteRaw]
	c.mu.Unlock()
	if !t0.joined {
		t.Fatal("not joined")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.got {
		if m.Method == "thread/resume" {
			var p map[string]any
			_ = json.Unmarshal(m.Params, &p)
			if len(p) != 2 {
				t.Fatalf("runtime overrides: %s", m.Params)
			}
		}
	}
}

func TestCodexRemoteOwnerConflictAndStdioRefuse(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	f1, _ := connectRemoteFixture(t, n, home, "one", remoteRaw)
	f2, _ := connectRemoteFixture(t, n, home, "two", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	if err := n.send(context.Background(), id, "不应发送", nil, sendOpts{}); err == nil {
		t.Fatal("ambiguous owner accepted")
	}
	if f1.count("turn/start")+f2.count("turn/start")+f1.count("thread/resume")+f2.count("thread/resume") != 0 {
		t.Fatal("selected an ambiguous owner")
	}
	offline := n.registerCodex(t.TempDir(), remoteOther, "")
	for _, method := range []string{"session.send", "session.keys", "session.close", "session.modes", "session.queue.edit", "terminal.watch", "assistant.send", "session.interrupt"} {
		_, err := n.handle(context.Background(), method, json.RawMessage(`{"session_id":"`+offline+`","text":"must not execute"}`))
		if err == nil {
			t.Fatalf("%s accepted for stdio history", method)
		}
	}
	_, err := n.handle(context.Background(), "session.start", json.RawMessage(`{"resume_session_id":"`+offline+`"}`))
	if err == nil {
		t.Fatal("Claude resume accepted")
	}
	if p := n.transcriptPath("c0de-00000000000000000000000000000000"); p != "" {
		t.Fatal("unknown opaque key fell through")
	}
}

func TestCodexRemoteUnknownDeliveryNoRetry(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	f, _ := connectRemoteFixture(t, n, home, "one", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	f.mu.Lock()
	f.drop = "turn/start"
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := n.sends.once("message-one", func() error { return n.send(ctx, id, "合成请求", nil, sendOpts{}) })
	var outcome struct {
		Delivered string `json:"delivered"`
	}
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected timeout: %v", err)
	}
	_ = json.Unmarshal(errorData(err), &outcome)
	if outcome.Delivered != "unknown" {
		t.Fatalf("unsafe delivery classification: %v", err)
	}
	retry := n.sends.once("message-one", func() error { t.Fatal("repeated delivery"); return nil })
	if retry == nil || f.count("turn/start") != 1 {
		t.Fatalf("retry: %v", retry)
	}
}
func errorData(err error) []byte {
	if e, ok := err.(*rpc.Error); ok {
		return e.Data
	}
	return nil
}

func TestCodexRemoteApprovalsOnlyExplicitAndResolvedNoReply(t *testing.T) {
	n := remoteTestNode(t)
	fh := withFakeHub(t, n)
	blocker := make(chan struct{})
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { <-blocker; yes := true; return proto.Resolution{Allow: &yes} }
	fh.mu.Unlock()
	defer close(blocker)
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	if err := n.attachCodex(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	f.push(map[string]any{"id": 7, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": remoteRaw, "itemId": "x", "command": "printf synthetic"}})
	waitFor(t, "phone approval", func() bool { return len(fh.interactions()) == 1 })
	f.push(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": remoteRaw, "requestId": 7}})
	waitFor(t, "approval withdrawn", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.asks) == 0 })
	f.push(map[string]any{"id": 8, "method": "item/tool/call", "params": map[string]any{"threadId": remoteRaw}})
	time.Sleep(80 * time.Millisecond)
	if f.replyCount() != 0 {
		t.Fatal("observer answered without a live user decision")
	}
}

func TestCodexRemoteRequestIDsAndRestartGeneration(t *testing.T) {
	a := remoteInteractionID("machine", "one", "generation1", remoteRaw, json.RawMessage(`7`))
	for _, b := range []string{remoteInteractionID("machine", "two", "generation1", remoteRaw, json.RawMessage(`7`)), remoteInteractionID("machine", "one", "generation2", remoteRaw, json.RawMessage(`7`)), remoteInteractionID("machine", "one", "generation1", remoteRaw, json.RawMessage(`"7"`))} {
		if a == b {
			t.Fatal("request identities collide")
		}
	}
}

func TestCodexDiscoverySpacesAliasesAndRememberedHomes(t *testing.T) {
	n := remoteTestNode(t)
	home := filepath.Join(t.TempDir(), "profile with spaces")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	p, ok := codexProcessArgs(123, []string{"/Applications/Sample App.app/bin/codex", "app-server"}, "CODEX_HOME="+home+" OTHER_VAR=x")
	if !ok || p.home != home {
		t.Fatalf("profile parse: %+v", p)
	}
	p.pid = 0
	n.codices.scan = func() []codexProcess { return []codexProcess{p, {home: alias}} }
	n.discoverCodex()
	cfg := n.codexInventory()
	if len(cfg.Homes) != 2 {
		t.Fatalf("aliases not deduplicated: %v", cfg.Homes)
	}
	n2 := remoteTestNode(t)
	n2.configureCodex(cfg)
	if n.registerCodex(home, remoteRaw, "") != n2.registerCodex(home, remoteRaw, "") {
		t.Fatal("restart changed identity")
	}
}

func TestCodexRemotePrivateUnixWebSocket(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	// A Unix socket path must fit sun_path (104 bytes on macOS); TempDir under /var/folders with this test's name does not.
	dir, err := os.MkdirTemp("/tmp", "cxr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "server.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(socket, 0600)
	f := &remoteFixture{home: home, threads: []string{remoteRaw}}
	server := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go server.Serve(ln)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.ensureCodexRemote(ctx, proto.CodexServer{ID: "unix", Home: canonicalCodexHome(home, n.home), Endpoint: "unix://" + socket})
	waitFor(t, "Unix WebSocket handshake", func() bool {
		id := n.registerCodex(home, remoteRaw, "")
		_, _, err := n.codexOwner(id)
		return err == nil
	})
	n.codices.mu.Lock()
	c := n.codices.remotes["unix"]
	n.codices.mu.Unlock()
	defer c.close()
	if f.count("initialize") != 1 || f.count("thread/resume") != 0 {
		t.Fatal("Unix transport did not initialize exactly once")
	}
	_ = os.Chmod(socket, 0666)
	if _, _, err := validateCodexEndpoint("unix://" + socket); err == nil {
		t.Fatal("accepted insecure socket")
	}
	for _, endpoint := range []string{"ws://example.com:80", "ws://127.0.0.1:80/?token=secret", "ws://user:pass@127.0.0.1:80"} {
		if _, _, err := validateCodexEndpoint(endpoint); err == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
}

// An ephemeral thread is Codex's own background job, not a conversation: it never becomes a session, even though the
// app-server keeps it loaded and active.
func TestCodexRemoteSkipsEphemeralThreads(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	f.mu.Lock()
	f.threads, f.ephemeral = []string{remoteRaw, remoteOther}, remoteOther
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, kept := c.threads[remoteOther]
	c.mu.Unlock()
	if kept {
		t.Fatal("an ephemeral thread was kept as a loaded thread")
	}
	var ids []string
	for _, s := range n.codexSessions(map[string]bool{}) {
		ids = append(ids, s.SourceThreadID)
	}
	if len(ids) != 1 || ids[0] != remoteRaw {
		t.Fatalf("sessions = %v, want only %s", ids, remoteRaw)
	}
}

// A thread/read that fails for a loaded thread keeps the thread: it is still the app-server's, and its session stays
// sendable until a read says it is gone.
func TestCodexRemoteFailedReadKeepsThread(t *testing.T) {
	n := remoteTestNode(t)
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	waitFor(t, "thread listed", func() bool { _, _, err := n.codexOwner(id); return err == nil })

	f.mu.Lock()
	f.reject = "thread/read"
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, kept := c.threads[remoteRaw]
	c.mu.Unlock()
	if !kept {
		t.Fatal("a failed thread/read dropped a loaded thread")
	}
	if _, _, err := n.codexOwner(id); err != nil {
		t.Fatalf("session no longer owned after a failed read: %v", err)
	}

	// Gone from the loaded list: dropped as before.
	f.mu.Lock()
	f.reject, f.threads = "", nil
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, kept = c.threads[remoteRaw]
	c.mu.Unlock()
	if kept {
		t.Fatal("an unloaded thread was kept")
	}
}
