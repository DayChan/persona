package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// dirsNode answers node.configure, sessions.list and claude.dirs.list like a node that blocks what it is told to.
type dirsNode struct {
	mu        sync.Mutex
	blocked   []string
	configure []map[string]json.RawMessage
}

func (f *dirsNode) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const home = "/home/me"
	all := []string{"/home/me/.automation/claude-a", "/home/me/.config/cc/work"}
	switch method {
	case "node.configure":
		var c map[string]json.RawMessage
		_ = json.Unmarshal(params, &c)
		f.configure = append(f.configure, c)
		if raw, ok := c["claude_dirs_blocked"]; ok {
			f.blocked = nil
			_ = json.Unmarshal(raw, &f.blocked)
		}
		return map[string]any{}, nil
	case "sessions.list":
		var list []proto.Session
		for i, d := range all {
			if !proto.ClaudeDirBlocked(d, home, f.blocked) {
				list = append(list, proto.Session{SessionID: []string{"bot", "work"}[i], Cwd: d})
			}
		}
		return map[string]any{"sessions": list}, nil
	case "claude.dirs.list":
		dirs := []proto.ClaudeDir{{Path: "/home/me/.claude", Default: true, Source: "default"}}
		for _, d := range all {
			dirs = append(dirs, proto.ClaudeDir{Path: d, Source: "found", Blocked: proto.ClaudeDirBlocked(d, home, f.blocked)})
		}
		return map[string]any{"dirs": dirs}, nil
	}
	return map[string]any{}, nil
}

func connectDirsNode(t *testing.T, m *Machine) *dirsNode {
	t.Helper()
	f := &dirsNode{}
	toNode, nodeIn := io.Pipe()
	toHub, hubIn := io.Pipe()
	hubPeer := rpc.New("h", nodeIn)
	nodePeer := rpc.New("n", hubIn)
	nodePeer.OnRequest = f.handle
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); toNode.Close(); toHub.Close() })
	go hubPeer.Serve(ctx, toHub)
	go nodePeer.Serve(ctx, toNode)
	m.mu.Lock()
	m.peer, m.online = hubPeer, true
	m.info.Home = "/home/me"
	m.mu.Unlock()
	return f
}

// dirsStore keeps settings and drops the interactions persona_settings records.
type dirsStore struct{ *settingsStore }

func (dirsStore) InsertInteraction(context.Context, proto.Interaction) error { return nil }
func (dirsStore) FinishInteraction(context.Context, string, string, *proto.Resolution) error {
	return nil
}

func dirsHub(t *testing.T) (*Devices, *settingsStore, *Machine, http.Handler) {
	t.Helper()
	h := newTestDevices(t)
	st := &settingsStore{vals: map[string][]byte{}}
	h.opts.Store = dirsStore{st}
	m := newMachine(h, MachineConfig{ID: "m1", Name: "box", ClaudeConfigDirs: []string{"~/.automation/claude-b"}})
	h.addEntry(m)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/machines/{mid}/claude-dirs", h.withMachine(h.getClaudeDirs))
	mux.HandleFunc("PUT /api/v1/machines/{mid}/claude-dirs/blocked", h.withMachine(h.putClaudeDirsBlocked))
	return h, st, m, mux
}

func listedIDs(m *Machine) []string {
	var ids []string
	for _, s := range m.sessionList() {
		ids = append(ids, s.SessionID)
	}
	return ids
}

// Blocking a pattern stores it, forgets the remembered directories it covers, tells the node and takes its new session
// list; a node reporting a blocked directory again is not listened to; unblocking brings the sessions back.
func TestBlockClaudeDirs(t *testing.T) {
	_, st, m, mux := dirsHub(t)
	ctx := context.Background()
	_ = st.PutSetting(ctx, claudeDirsKey("m1"), []string{"/home/me/.automation/claude-a", "/home/me/.config/cc/work"})
	f := connectDirsNode(t, m)

	code, out := put(t, mux, "/api/v1/machines/m1/claude-dirs/blocked", `{"blocked":[" ~/.automation/* ",""]}`)
	if code != 200 {
		t.Fatalf("PUT = %d %v", code, out)
	}
	if b := m.blockedClaudeDirs(ctx); !slices.Equal(b, []string{"~/.automation/*"}) {
		t.Fatalf("stored %v", b)
	}
	var seen []string
	_, _ = st.Setting(ctx, claudeDirsKey("m1"), &seen)
	if !slices.Equal(seen, []string{"/home/me/.config/cc/work"}) {
		t.Fatalf("remembered %v", seen)
	}
	if ids := listedIDs(m); !slices.Equal(ids, []string{"work"}) {
		t.Fatalf("sessions %v", ids)
	}
	f.mu.Lock()
	last := f.configure[len(f.configure)-1]
	f.mu.Unlock()
	if string(last["claude_dirs"]) != `["/home/me/.config/cc/work"]` || string(last["claude_dirs_blocked"]) != `["~/.automation/*"]` {
		t.Fatalf("node told %s / %s", last["claude_dirs"], last["claude_dirs_blocked"])
	}
	// The answer lists every directory, blocked ones marked, the configured one too.
	dirs, _ := out["dirs"].([]any)
	var blocked []string
	for _, d := range dirs {
		if dm := d.(map[string]any); dm["blocked"] == true {
			blocked = append(blocked, dm["path"].(string))
		}
	}
	if out["live"] != true || !slices.Equal(blocked, []string{"/home/me/.automation/claude-a", "~/.automation/claude-b"}) {
		t.Fatalf("state %v", out)
	}

	// An older node reports the blocked directory again: not remembered.
	m.rememberClaudeDirs(json.RawMessage(`{"dirs":["/home/me/.automation/claude-a","/home/me/.config/cc/new"]}`))
	_, _ = st.Setting(ctx, claudeDirsKey("m1"), &seen)
	if !slices.Equal(seen, []string{"/home/me/.config/cc/work", "/home/me/.config/cc/new"}) {
		t.Fatalf("remembered %v", seen)
	}
	// Reconnecting hands the node the patterns.
	m.configureNode(ctx, m.peer, nil, true)
	f.mu.Lock()
	last = f.configure[len(f.configure)-1]
	f.mu.Unlock()
	if string(last["claude_dirs_blocked"]) != `["~/.automation/*"]` || strings.Contains(string(last["claude_dirs"]), "automation") {
		t.Fatalf("configure on connect: %s / %s", last["claude_dirs"], last["claude_dirs_blocked"])
	}

	if code, _ := put(t, mux, "/api/v1/machines/m1/claude-dirs/blocked", `{"blocked":[]}`); code != 200 {
		t.Fatalf("unblock = %d", code)
	}
	if ids := listedIDs(m); !slices.Equal(ids, []string{"bot", "work"}) {
		t.Fatalf("unblocked sessions %v", ids)
	}
}

func TestBlockClaudeDirsRefusesBadPatterns(t *testing.T) {
	_, _, _, mux := dirsHub(t)
	for _, body := range []string{`{"blocked":["relative/dir"]}`, `{"blocked":["~/["]}`, `nope`} {
		if code, _ := put(t, mux, "/api/v1/machines/m1/claude-dirs/blocked", body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
}

// Offline: the hub's own record, blocked ones marked.
func TestClaudeDirsOffline(t *testing.T) {
	_, st, _, mux := dirsHub(t)
	ctx := context.Background()
	_ = st.PutSetting(ctx, claudeDirsKey("m1"), []string{"~/.config/cc/work"})
	_ = st.PutSetting(ctx, claudeDirsBlockedKey("m1"), []string{"~/.automation/*"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/claude-dirs", nil))
	var out claudeDirsState
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out.Live || len(out.Dirs) != 2 || !out.Dirs[0].Blocked || out.Dirs[0].Source != "config" || out.Dirs[1].Blocked {
		t.Fatalf("%d %+v", rec.Code, out)
	}
}

// An empty blocklist serializes as [] (not null): the web settings page treats blocked as a JSON array and crashes
// rendering .filter on null (#977).
func TestClaudeDirsEmptyBlocklistIsArray(t *testing.T) {
	_, _, m, mux := dirsHub(t)
	connectDirsNode(t, m)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/machines/m1/claude-dirs", nil))
	if rec.Code != 200 {
		t.Fatalf("GET = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"blocked":[]`) {
		t.Fatalf("blocklist is not an empty JSON array: %s", rec.Body.String())
	}
}

// Connecting tells the node whether to list botmux's sessions: as before unless hub.json turns them off for the
// machine (machines[].botmux false). So does handing it new config directories, which asks for the session list
// next: the node must not take that list for an older hub's.
func TestConfigureNodeSaysBotmux(t *testing.T) {
	_, _, m, _ := dirsHub(t)
	f := connectDirsNode(t, m)
	ctx := context.Background()
	m.configureNode(ctx, m.peer, nil, true)
	off := false
	m.cfg.Botmux = &off
	m.configureNode(ctx, m.peer, nil, true)
	m.reconfigureClaudeDirs(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.configure) != 3 || string(f.configure[0]["botmux"]) != "true" || string(f.configure[1]["botmux"]) != "false" ||
		string(f.configure[2]["botmux"]) != "false" {
		t.Fatalf("botmux in node.configure: %v", f.configure)
	}
}

// hub.json machines[] switches reach the node on every connect, each going from its default to the configured value:
// herdr_autostart (#1879) and usage_windows (#2432) are on unless they say false; claude_global_hooks is off unless set, and
// the node needs the false to take the entries out again.
func TestConfigureNodeSaysMachineSwitches(t *testing.T) {
	off := false
	for _, tc := range []struct {
		name          string
		set           func(*MachineConfig)
		key           string
		before, after string
	}{
		{"herdr_autostart", func(c *MachineConfig) { c.HerdrAutostart = &off }, "herdr_autostart", "true", "false"},
		{"claude_global_hooks", func(c *MachineConfig) { c.ClaudeGlobalHooks = true }, "claude_global_hooks", "false", "true"},
		{"usage_windows", func(c *MachineConfig) { c.UsageWindows = &off }, "usage_windows", "true", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, m, _ := dirsHub(t)
			f := connectDirsNode(t, m)
			ctx := context.Background()
			m.configureNode(ctx, m.peer, nil, true)
			tc.set(&m.cfg)
			m.configureNode(ctx, m.peer, nil, true)
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.configure) != 2 || string(f.configure[0][tc.key]) != tc.before || string(f.configure[1][tc.key]) != tc.after {
				t.Fatalf("%s in node.configure: %v", tc.key, f.configure)
			}
		})
	}
}

// hub.json machines[].claude_start_dir reaches the node on connect (#1459), and its sessions are looked for there.
func TestConfigureNodeSaysClaudeStartDir(t *testing.T) {
	_, _, m, _ := dirsHub(t)
	f := connectDirsNode(t, m)
	ctx := context.Background()
	m.configureNode(ctx, m.peer, nil, true)
	m.cfg.ClaudeStartDir = "~/.config/claude-code/profiles/work"
	m.configureNode(ctx, m.peer, nil, true)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.configure) != 2 || string(f.configure[0]["claude_start_dir"]) != `""` ||
		string(f.configure[1]["claude_start_dir"]) != `"~/.config/claude-code/profiles/work"` {
		t.Fatalf("claude_start_dir in node.configure: %v", f.configure)
	}
	if !slices.Contains(m.claudeDirs(ctx), "~/.config/claude-code/profiles/work") {
		t.Fatalf("not looked in: %v", m.claudeDirs(ctx))
	}
}

// faultyDirsStore fails or holds reads of claude_dirs/<id>: failNext makes the next one fail; hold, when set, makes the
// next one read, then signal entered and wait for release before returning what it read.
type faultyDirsStore struct {
	dirsStore
	mu       sync.Mutex
	failNext bool
	hold     bool
	entered  chan struct{}
	release  chan struct{}
}

func (s *faultyDirsStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if !strings.HasPrefix(key, "claude_dirs/") {
		return s.dirsStore.Setting(ctx, key, out)
	}
	s.mu.Lock()
	fail, hold := s.failNext, s.hold
	s.failNext, s.hold = false, false
	s.mu.Unlock()
	if fail {
		return false, errors.New("pool timeout")
	}
	ok, err := s.dirsStore.Setting(ctx, key, out)
	if hold {
		close(s.entered)
		<-s.release
	}
	return ok, err
}

func faultyDirsHub(t *testing.T) (*settingsStore, *faultyDirsStore, *Machine) {
	t.Helper()
	h, st, m, _ := dirsHub(t)
	fs := &faultyDirsStore{dirsStore: dirsStore{st}, entered: make(chan struct{}), release: make(chan struct{})}
	h.opts.Store = fs
	m.mu.Lock()
	m.info.Home = "/home/me"
	m.mu.Unlock()
	return st, fs, m
}

// A failed read of the remembered dirs leaves them as they were: the event's dirs do not replace the list.
func TestRememberClaudeDirsKeepsListOnReadError(t *testing.T) {
	st, fs, m := faultyDirsHub(t)
	ctx := context.Background()
	_ = st.PutSetting(ctx, claudeDirsKey("m1"), []string{"/home/me/.config/cc/a", "/home/me/.config/cc/b"})
	fs.failNext = true
	m.rememberClaudeDirs(json.RawMessage(`{"dirs":["/home/me/.config/cc/new"]}`))
	var seen []string
	_, _ = st.Setting(ctx, claudeDirsKey("m1"), &seen)
	if !slices.Equal(seen, []string{"/home/me/.config/cc/a", "/home/me/.config/cc/b"}) {
		t.Fatalf("remembered %v after a failed read", seen)
	}
	// The next event, read fine, adds to the list.
	m.rememberClaudeDirs(json.RawMessage(`{"dirs":["/home/me/.config/cc/new"]}`))
	_, _ = st.Setting(ctx, claudeDirsKey("m1"), &seen)
	if !slices.Equal(seen, []string{"/home/me/.config/cc/a", "/home/me/.config/cc/b", "/home/me/.config/cc/new"}) {
		t.Fatalf("remembered %v", seen)
	}
}

// Blocking still stores the patterns when the remembered dirs cannot be read (it is a privacy control, and the list
// is filtered by them wherever it is used); only the pruning waits, and the list is not overwritten.
func TestBlockClaudeDirsReadError(t *testing.T) {
	st, fs, m := faultyDirsHub(t)
	ctx := context.Background()
	_ = st.PutSetting(ctx, claudeDirsKey("m1"), []string{"/home/me/.config/cc/a", "/home/me/.config/cc/b"})
	fs.failNext = true
	if err := m.setBlockedClaudeDirs(ctx, []string{"~/.config/cc/a"}); err != nil {
		t.Fatalf("block refused on a failed read of the remembered dirs: %v", err)
	}
	if b := m.blockedClaudeDirs(ctx); !slices.Equal(b, []string{"~/.config/cc/a"}) {
		t.Fatalf("blocked %v", b)
	}
	var seen []string
	_, _ = st.Setting(ctx, claudeDirsKey("m1"), &seen)
	if !slices.Equal(seen, []string{"/home/me/.config/cc/a", "/home/me/.config/cc/b"}) {
		t.Fatalf("remembered %v after a failed read", seen)
	}
	if dirs := m.claudeDirs(ctx); slices.Contains(dirs, "/home/me/.config/cc/a") {
		t.Fatalf("blocked dir still handed to the node: %v", dirs)
	}
}

// Remembered dirs that do not decode are not a read error for good: what decodes is kept and written back, by a
// claude.dirs event and by a block alike.
func TestClaudeDirsRecoverFromAnUndecodableList(t *testing.T) {
	for name, raw := range map[string]string{
		"an entry is not a string": `["/home/me/.config/cc/a", 7, "/home/me/.config/cc/b"]`,
		"not JSON":                 `["/home/me/.config/cc/a",`,
	} {
		t.Run(name, func(t *testing.T) {
			st, _, m := faultyDirsHub(t)
			ctx := context.Background()
			bad := func() {
				st.mu.Lock()
				st.vals[claudeDirsKey("m1")] = []byte(raw)
				st.mu.Unlock()
			}
			read := func() []string {
				var seen []string
				if _, err := st.Setting(ctx, claudeDirsKey("m1"), &seen); err != nil {
					t.Fatalf("still undecodable: %v", err)
				}
				return seen
			}
			bad()
			m.rememberClaudeDirs(json.RawMessage(`{"dirs":["/home/me/.config/cc/new"]}`))
			if seen := read(); !slices.Contains(seen, "/home/me/.config/cc/new") {
				t.Fatalf("remembered %v", seen)
			}
			bad()
			if err := m.setBlockedClaudeDirs(ctx, []string{"~/.config/cc/a"}); err != nil {
				t.Fatal(err)
			}
			if seen := read(); slices.Contains(seen, "/home/me/.config/cc/a") {
				t.Fatalf("blocked dir still remembered: %v", seen)
			}
		})
	}
}

// A dir blocked while a claude.dirs event is being remembered stays forgotten: the event's write does not bring back
// the list it read before the block.
func TestBlockClaudeDirsDuringRemember(t *testing.T) {
	st, fs, m := faultyDirsHub(t)
	ctx := context.Background()
	_ = st.PutSetting(ctx, claudeDirsKey("m1"), []string{"/home/me/.config/cc/keep", "/home/me/.config/cc/gone"})
	fs.hold = true
	remembered := make(chan struct{})
	go func() {
		defer close(remembered)
		m.rememberClaudeDirs(json.RawMessage(`{"dirs":["/home/me/.config/cc/new"]}`))
	}()
	<-fs.entered
	blocked := make(chan error, 1)
	go func() { blocked <- m.setBlockedClaudeDirs(ctx, []string{"~/.config/cc/gone"}) }()
	// Without the lock the block finishes while the remember is held; with it, it waits for the remember.
	select {
	case err := <-blocked:
		blocked <- err
	case <-time.After(200 * time.Millisecond):
	}
	close(fs.release)
	<-remembered
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	var seen []string
	_, _ = st.Setting(ctx, claudeDirsKey("m1"), &seen)
	if !slices.Equal(seen, []string{"/home/me/.config/cc/keep", "/home/me/.config/cc/new"}) {
		t.Fatalf("remembered %v", seen)
	}
}

// A node.configure that names neither claude_dirs nor keep_sessions makes the node forget its config directories
// (node/node.go): the hub adds them to every such call, whoever makes it — the botmux and herdr switches, an
// extension's agent filters through CallNode — and leaves a call that says what is kept as it is.
func TestConfigureKeepsClaudeDirs(t *testing.T) {
	h := newTestDevices(t)
	ctx := context.Background()
	m, seen := herdrMac(t, h, "mac", false)
	m.cfg.ClaudeConfigDirs = []string{"/home/me/.config/cc/work"}
	m.cfg.ClaudeStartDir = "/home/me/.persona/claude"
	if err := h.opts.Store.PutSetting(ctx, claudeDirsKey("mac"), []string{"/home/me/.config/cc/found"}); err != nil {
		t.Fatal(err)
	}
	want := m.claudeDirs(ctx)
	if len(want) != 3 {
		t.Fatalf("configured, start and remembered dirs: %v", want)
	}
	mux := h.Handler(http.NotFoundHandler())
	if code, _ := call(t, mux, "PUT", "/api/v1/machines/mac/botmux", `{"on": false}`); code != 200 {
		t.Fatalf("botmux: %d", code)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/machines/mac/herdr", `{"autostart": false}`); code != 200 {
		t.Fatalf("herdr: %d", code)
	}
	if _, err := h.CallNode(ctx, "mac", "node.configure", json.RawMessage(`{"agent_filters":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CallNode(ctx, "mac", "node.configure", json.RawMessage(`{"keep_sessions":["s1"]}`)); err != nil {
		t.Fatal(err)
	}
	configures := func() []map[string]any {
		var out []map[string]any
		for _, c := range seen() {
			if c["method"] == "node.configure" {
				out = append(out, c)
			}
		}
		return out
	}
	eventually(t, 2*time.Second, "four node.configure calls", func() bool { return len(configures()) == 4 })
	dirsOf := func(c map[string]any) []string {
		var out []string
		for _, d := range c["claude_dirs"].([]any) {
			out = append(out, d.(string))
		}
		return out
	}
	for _, key := range []string{"botmux", "herdr_autostart", "agent_filters"} {
		i := slices.IndexFunc(configures(), func(c map[string]any) bool { _, ok := c[key]; return ok })
		if i < 0 {
			t.Fatalf("no node.configure with %s: %v", key, configures())
		}
		c := configures()[i]
		if _, ok := c["claude_dirs"]; !ok {
			t.Fatalf("%s: the node would forget its dirs: %v", key, c)
		}
		if got := dirsOf(c); !slices.Equal(got, want) {
			t.Fatalf("%s: claude_dirs %v, want %v", key, got, want)
		}
	}
	i := slices.IndexFunc(configures(), func(c map[string]any) bool { _, ok := c["keep_sessions"]; return ok })
	if i < 0 {
		t.Fatalf("no node.configure with keep_sessions: %v", configures())
	}
	if _, ok := configures()[i]["claude_dirs"]; ok {
		t.Fatalf("a call that only changes what is kept got dirs: %v", configures()[i])
	}
}
