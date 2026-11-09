package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
)

// pushed is a session-list message as an app reads it.
type pushed struct {
	Type     string          `json:"type"`
	Machine  string          `json:"machine_id"`
	Sessions []proto.Session `json:"sessions"`
	Upsert   []proto.Session `json:"upsert"`
	Remove   []string        `json:"remove"`
}

func drainPushed(c *client) []pushed {
	var out []pushed
	for len(c.out) > 0 {
		var p pushed
		_ = json.Unmarshal(<-c.out, &p)
		out = append(out, p)
	}
	return out
}

// applyPushed is what an app does with the messages: a list replaces, a delta patches.
func applyPushed(have map[string]proto.Session, msgs []pushed) map[string]proto.Session {
	for _, p := range msgs {
		switch p.Type {
		case "sessions":
			have = map[string]proto.Session{}
			for _, s := range p.Sessions {
				have[s.SessionID] = s
			}
		case "sessions_delta":
			for _, s := range p.Upsert {
				have[s.SessionID] = s
			}
			for _, id := range p.Remove {
				delete(have, id)
			}
		}
	}
	return have
}

func sessionIDs(m map[string]proto.Session) []string {
	var out []string
	for id := range m {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func TestSessionsDelta(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	live := proto.Session{SessionID: "s1", Status: proto.StatusWorking, LastText: "reading"}
	idle := proto.Session{SessionID: "s2", Status: proto.StatusIdle}
	hist := proto.Session{SessionID: "h1", Agent: "codex", SourceKind: "codex_history", Status: proto.StatusOffline}
	m.sessions = []proto.Session{live, idle, hist}
	old := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull}
	app := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, delta: true, have: map[string]map[string][]byte{}}
	h.clients[old], h.clients[app] = struct{}{}, struct{}{}
	broadcast := func() { h.broadcastSessions(m, h.decorated(m)) }

	// Connected: the whole list, without the Codex history.
	h.snapshotSessions(app, m)
	got := drainPushed(app)
	state := applyPushed(nil, got)
	if len(got) != 1 || got[0].Type != "sessions" || !slices.Equal(sessionIDs(state), []string{"s1", "s2"}) {
		t.Fatalf("snapshot %+v", got)
	}

	// One session changes: the old app gets every session again (history too), the delta app that one.
	live.LastText = "writing"
	m.sessions = []proto.Session{live, idle, hist}
	broadcast()
	o, d := drainPushed(old), drainPushed(app)
	if len(o) != 1 || len(o[0].Sessions) != 3 {
		t.Fatalf("old app got %+v", o)
	}
	if len(d) != 1 || d[0].Type != "sessions_delta" || len(d[0].Upsert) != 1 || d[0].Upsert[0].LastText != "writing" || len(d[0].Remove) != 0 {
		t.Fatalf("delta app got %+v", d)
	}
	state = applyPushed(state, d)

	// The same again: nobody gets anything.
	broadcast()
	if o, d := drainPushed(old), drainPushed(app); len(o)+len(d) != 0 {
		t.Fatalf("unchanged list sent: %+v %+v", o, d)
	}

	// A live Codex session starts, then ends (to history, same id) while another session goes: the delta app is told
	// both, and still never gets the history it did not have.
	codex := proto.Session{SessionID: "c1", Agent: "codex", SourceKind: "codex_app_server", Status: proto.StatusWorking}
	m.sessions = []proto.Session{live, idle, hist, codex}
	broadcast()
	state = applyPushed(state, drainPushed(app))
	codex.SourceKind, codex.Status = "codex_history", proto.StatusOffline
	m.sessions = []proto.Session{live, hist, codex}
	broadcast()
	d = drainPushed(app)
	if len(d) != 1 || len(d[0].Upsert) != 1 || d[0].Upsert[0].SessionID != "c1" || !slices.Equal(d[0].Remove, []string{"s2"}) {
		t.Fatalf("delta %+v", d)
	}
	state = applyPushed(state, d)

	// Replayed over the snapshot, the deltas give the hub's list, less the history the app never had.
	want := map[string]proto.Session{}
	for _, s := range h.decorated(m) {
		if s.SessionID != "h1" {
			want[s.SessionID] = s
		}
	}
	a, _ := json.Marshal(state)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("app has\n%s\nhub has\n%s", a, b)
	}
}

// TestSessionsFullListOnlyForOlderApps: with only delta apps the hub still tracks the list (and the Fleet activity
// still hears of changes), so an older app that connects later gets the next changed list whole; a list that did not
// change goes to no older app and is not news for the Fleet activity.
func TestSessionsFullListOnlyForOlderApps(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	s1 := proto.Session{SessionID: "s1", Status: proto.StatusWorking, LastText: "reading"}
	s2 := proto.Session{SessionID: "s2", Status: proto.StatusIdle}
	m.sessions = []proto.Session{s1, s2}
	app := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, delta: true, have: map[string]map[string][]byte{}}
	h.clients[app] = struct{}{}
	h.snapshotSessions(app, m)
	drainPushed(app)
	broadcast := func() { h.broadcastSessions(m, h.decorated(m)) }
	fired := func() bool {
		select {
		case <-h.live.signal:
			return true
		default:
			return false
		}
	}
	fired()

	// Only delta apps: the change goes out as a delta, and the Fleet activity hears of it.
	s1.LastText = "writing"
	m.sessions = []proto.Session{s1, s2}
	broadcast()
	if d := drainPushed(app); len(d) != 1 || d[0].Type != "sessions_delta" {
		t.Fatalf("delta app got %+v", d)
	}
	if !fired() {
		t.Fatal("changed list did not signal the live activity")
	}

	// An older app connects; the same list again: nothing for anyone, no signal.
	old := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull}
	h.clients[old] = struct{}{}
	broadcast()
	if o, d := drainPushed(old), drainPushed(app); len(o)+len(d) != 0 {
		t.Fatalf("unchanged list sent: %+v %+v", o, d)
	}
	if fired() {
		t.Fatal("unchanged list signalled the live activity")
	}

	// Reordered is a change for an older app (it reads the order), though not for a delta app.
	m.sessions = []proto.Session{s2, s1}
	broadcast()
	if o := drainPushed(old); len(o) != 1 || o[0].Type != "sessions" || len(o[0].Sessions) != 2 || o[0].Sessions[0].SessionID != "s2" {
		t.Fatalf("older app got %+v", o)
	}
	if d := drainPushed(app); len(d) != 0 {
		t.Fatalf("delta app got %+v", d)
	}
	if !fired() {
		t.Fatal("reordered list did not signal the live activity")
	}

	// A session changes: the older app gets the whole list, the delta app the one session.
	s2.Status = proto.StatusWorking
	m.sessions = []proto.Session{s2, s1}
	broadcast()
	if o := drainPushed(old); len(o) != 1 || o[0].Type != "sessions" || len(o[0].Sessions) != 2 || o[0].Sessions[0].Status != proto.StatusWorking {
		t.Fatalf("older app got %+v", o)
	}
	if d := drainPushed(app); len(d) != 1 || len(d[0].Upsert) != 1 || d[0].Upsert[0].SessionID != "s2" {
		t.Fatalf("delta app got %+v", d)
	}
}

// TestSessionsFirstEmptyList: a machine's first list goes to older apps even when it is empty.
func TestSessionsFirstEmptyList(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	old := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull}
	h.clients[old] = struct{}{}
	h.broadcastSessions(m, nil)
	if o := drainPushed(old); len(o) != 1 || o[0].Type != "sessions" || len(o[0].Sessions) != 0 {
		t.Fatalf("older app got %+v", o)
	}
	h.broadcastSessions(m, nil)
	if o := drainPushed(old); len(o) != 0 {
		t.Fatalf("empty list sent twice: %+v", o)
	}
}

// TestSessionsDeltaOverTheSocket: an app that asks (?features=sessions_delta) gets the snapshot without history and
// then deltas; one that does not gets whole lists, history included (an older app on a newer hub).
func TestSessionsDeltaOverTheSocket(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	m.sessions = []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: "a"},
		{SessionID: "h1", Agent: "codex", SourceKind: "codex_history", Status: proto.StatusOffline}}
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	dial := func(query string) *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws" + query
		c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + h.cfg.Token}}})
		if err != nil {
			t.Fatal(err)
		}
		c.SetReadLimit(1 << 20)
		return c
	}
	next := func(c *websocket.Conn, typ string) pushed {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("waiting for %s: %v", typ, err)
			}
			var p pushed
			_ = json.Unmarshal(b, &p)
			if p.Type == typ {
				return p
			}
		}
	}
	newer, older := dial("?features=sessions_delta"), dial("")
	defer newer.CloseNow()
	defer older.CloseNow()
	if p := next(newer, "sessions"); len(p.Sessions) != 1 || p.Sessions[0].SessionID != "s1" {
		t.Fatalf("delta snapshot %+v", p)
	}
	if p := next(older, "sessions"); len(p.Sessions) != 2 {
		t.Fatalf("whole snapshot %+v", p)
	}
	time.Sleep(50 * time.Millisecond) // both registered
	m.sessions = []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: "b"}, m.sessions[1]}
	h.broadcastSessions(m, h.decorated(m))
	if p := next(newer, "sessions_delta"); len(p.Upsert) != 1 || p.Upsert[0].LastText != "b" {
		t.Fatalf("delta %+v", p)
	}
	if p := next(older, "sessions"); len(p.Sessions) != 2 || p.Sessions[0].LastText != "b" {
		t.Fatalf("whole list %+v", p)
	}
}

// The whole-list message is the sessions' own bytes in a list: what marshalling them would give, and nothing
// spent on checking and copying again what was just encoded.
func TestSessionsMessage(t *testing.T) {
	if !isolatedSessionAllocation(t) {
		return
	}
	list := encodeSessions([]proto.Session{
		{SessionID: "s1", Title: "<b>&</b> 标题  ", LastText: strings.Repeat("x", 4<<10)},
		{SessionID: "s2", Status: proto.StatusWorking, Queued: []string{"next"}},
	})
	for _, tc := range []struct {
		machine string
		list    []sentSession
	}{{"box", list}, {"<m&\"x\">", list}, {"box", nil}} {
		raws := make([]json.RawMessage, len(tc.list))
		for i, s := range tc.list {
			raws[i] = s.raw
		}
		want, _ := json.Marshal(map[string]any{"type": "sessions", "machine_id": tc.machine, "sessions": raws})
		if got := sessionsMessage(tc.machine, tc.list); string(got) != string(want) {
			t.Fatalf("sessionsMessage(%q):\n got %s\nwant %s", tc.machine, got, want)
		}
	}

	list = encodeSessions(busyList())
	size := len(sessionsMessage("box", list))
	if per := allocated(func() { sessionsMessage("box", list) }); per > size+size/4 {
		t.Fatalf("a %d-byte list message allocates %d bytes", size, per)
	}
}

// A change pushes the machine's kept list as decorated gives it, stamped with its manager, from the list itself: the
// list is neither changed nor copied (Machine.sessions). With an older app, 2.44x the list message's size measured
// (the encoded sessions and the message), 3.53x before.
func TestSessionsChangedStampsWithoutCopy(t *testing.T) {
	if !isolatedSessionAllocation(t) {
		return
	}
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	m.sessions = busyList()
	m.sessions[1].Manager = "someone-else"
	if h.machineOwner("box") == "" {
		t.Fatal("the machine has no manager to stamp")
	}
	if got, want := encodeStamped(m.keptSessions(), h.machineOwner("box")), encodeSessions(h.decorated(m)); !sameSessions(got, want) {
		t.Fatal("the stamped encoding differs from the decorated list's")
	}
	if m.sessions[0].Manager != "" || m.sessions[1].Manager != "someone-else" {
		t.Fatal("stamping changed the kept list")
	}

	app := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull}
	h.clients[app] = struct{}{}
	size := len(sessionsMessage("box", encodeSessions(h.decorated(m))))
	per := allocated(func() {
		h.sessionsSent = nil // the same list again goes to the older app again
		h.sessionsChanged(m)
		<-app.out
	})
	if per > size*251/100 {
		t.Fatalf("pushing a %d-byte list allocates %d bytes", size, per)
	}
}

// busyList is a busy machine's list (docs/server/server.md "What is pushed to the apps").
func busyList() []proto.Session {
	var many []proto.Session
	for i := range 150 {
		many = append(many, proto.Session{SessionID: fmt.Sprintf("s%03d", i), LastText: strings.Repeat("x", 1<<10)})
	}
	return many
}

const sessionAllocationChild = "PERSONA_TEST_SESSION_ALLOCATION_CHILD"

// isolatedSessionAllocation reruns only this test in a fresh process. TotalAlloc counts every goroutine, including
// asynchronous work left by earlier tests; the child keeps the same fixtures, assertions and allocation ceilings.
func isolatedSessionAllocation(t *testing.T) bool {
	t.Helper()
	if os.Getenv(sessionAllocationChild) == t.Name() {
		return true
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.timeout=50s")
	cmd.Env = append(os.Environ(), sessionAllocationChild+"="+t.Name())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated session allocation check: %v\n%s", err, output)
	}
	return false
}

// A separate goroutine's allocation is counted even when the measured callback only waits for it. That work must
// stay in the parent, while a child measuring a no-op remains below the smallest session allocation ceiling.
func TestSessionAllocationIsolation(t *testing.T) {
	const receiptEnv = "PERSONA_TEST_SESSION_ALLOCATION_RECEIPT"
	if os.Getenv(sessionAllocationChild) == t.Name() {
		if got := allocated(func() {}); got > 1<<10 {
			t.Fatalf("isolated no-op allocated %d bytes, want at most 1024", got)
		}
		if err := os.WriteFile(os.Getenv(receiptEnv), []byte("passed"), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	receipt := filepath.Join(t.TempDir(), "child")
	t.Setenv(receiptEnv, receipt)
	const bytes = 64 << 10
	requests := make(chan chan []byte)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for done := range requests {
			done <- make([]byte, bytes)
		}
	}()
	t.Cleanup(func() { close(requests); <-stopped })
	got := allocated(func() {
		done := make(chan []byte)
		requests <- done
		runtime.KeepAlive(<-done)
	})
	if got < bytes {
		t.Fatalf("another goroutine's allocation was not observed: %d bytes, want at least %d", got, bytes)
	}
	t.Logf("synchronized allocation in another goroutine: %d bytes per callback", got)
	if isolatedSessionAllocation(t) {
		t.Fatal("the parent was identified as the allocation child")
	}
	if got, err := os.ReadFile(receipt); err != nil || string(got) != "passed" {
		t.Fatalf("the allocation child did not pass: receipt %q, error %v", got, err)
	}
}

// allocated is how many bytes f allocates, averaged over runs. Pushes are seconds apart, collections in between:
// whatever an encoder keeps pooled is gone by the next one.
func allocated(f func()) int {
	const runs = 20
	var total uint64
	for range runs {
		runtime.GC()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		total += after.TotalAlloc - before.TotalAlloc
	}
	return int(total / runs)
}

// Each encoded session is what json.Marshal gives for it, read from one shared buffer: HTML and non-ASCII text
// escaped the same way, Codex history marked, and a raw never running into its neighbour.
func TestEncodeSessionsMatchesMarshal(t *testing.T) {
	if !isolatedSessionAllocation(t) {
		return
	}
	list := []proto.Session{
		{SessionID: "s1", Title: "<b>&</b> 标题 \u2028", LastText: "<script>alert(1)</script> 😀 \"q\" \\", Cwd: "/tmp/a&b"},
		{SessionID: "s2", Status: proto.StatusWorking, Queued: []string{"next", "<i>"}, UpdatedAt: time.Unix(1700000000, 5)},
		{SessionID: "s3", SourceKind: "codex_history", Title: "history", LastText: strings.Repeat("y", 5<<10)},
		{SessionID: "s4"},
	}
	got := encodeSessions(list)
	if len(got) != len(list) {
		t.Fatalf("encoded %d of %d sessions", len(got), len(list))
	}
	for i, s := range got {
		want, err := json.Marshal(list[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(s.raw) != string(want) {
			t.Fatalf("session %d:\n got %s\nwant %s", i, s.raw, want)
		}
		if s.id != list[i].SessionID || s.history != (list[i].SourceKind == "codex_history") {
			t.Fatalf("session %d: id %q history %v", i, s.id, s.history)
		}
		if cap(s.raw) != len(s.raw) {
			t.Fatalf("session %d: raw has room to grow into the next one (len %d cap %d)", i, len(s.raw), cap(s.raw))
		}
	}
	if len(encodeSessions(nil)) != 0 {
		t.Fatal("nil list encoded to sessions")
	}

	// One shared buffer and little else: each session is written straight into it (proto.Session.MarshalJSONTo), not
	// copied and marshalled to a buffer of its own first. 1.44x the list's size measured, 4.8x before.
	many := busyList()
	size := len(sessionsMessage("box", encodeSessions(many)))
	if per := allocated(func() { encodeSessions(many) }); per > size*148/100 {
		t.Fatalf("encoding a %d-byte list allocates %d bytes", size, per)
	}
}
