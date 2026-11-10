package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestSpoolKeepsOrderBoundsAndPersists(t *testing.T) {
	defer func(n, b int) { spoolMost, spoolBytesMost = n, b }(spoolMost, spoolBytesMost)
	spoolMost = 3
	path := filepath.Join(t.TempDir(), spoolFile)
	sp := openSpool(path)
	for i := range 5 {
		dropped, err := sp.add(spoolEntry{ID: string(rune('a' + i)), At: time.Now(), Kind: "hook"})
		if want := min(1, max(0, i-2)); err != nil || dropped != want {
			t.Fatalf("add %d: dropped %d %v", i, dropped, err)
		}
	}
	var ids []string
	for _, e := range openSpool(path).load() { // a new process reads what the last one kept
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, "") != "cde" {
		t.Fatalf("kept %v, want the 3 newest in order", ids)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("spool file: %v %v", fi, err)
	}
	spoolBytesMost = 400
	big := sockRequest{Kind: "hook", Input: json.RawMessage(`"` + strings.Repeat("x", 150) + `"`)}
	_, _ = sp.add(spoolEntry{ID: "f", Kind: "hook", Request: big})
	_, _ = sp.add(spoolEntry{ID: "g", Kind: "hook", Request: big})
	if got := sp.load(); len(got) == 0 || got[len(got)-1].ID != "g" {
		t.Fatalf("by size: %+v", got)
	}
	for _, e := range sp.load() {
		_ = sp.remove(e.ID)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an empty spool leaves no file: %v", err)
	}
}

func hookReq(event, tool string, mod func(*sockRequest)) sockRequest {
	b, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": reportSession, "tool_name": tool, "cwd": "/work/project"})
	r := sockRequest{Kind: "hook", Input: b, Pane: "w1:p1", Attempt: "att-" + event + tool}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestStandbyAnswers(t *testing.T) {
	path := filepath.Join(t.TempDir(), spoolFile)
	sb := newStandby(openSpool(path), t.Logf)
	now := time.Now()
	sb.now = func() time.Time { return now }
	sb.healthy = now
	reply := func(req sockRequest) map[string]any {
		b, _ := json.Marshal(sb.answer(req))
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}

	if r := reply(hookReq("Stop", "", nil)); r["output"] != nil || r["retry"] != nil {
		t.Fatalf("a Stop: %v", r)
	}
	if r := reply(hookReq("PermissionRequest", "Bash", nil)); r["retry"] != true {
		t.Fatalf("a permission while the manager was just here: %v, want retry", r)
	}
	if r := reply(hookReq("PreToolUse", "Bash", nil)); r["retry"] != nil {
		t.Fatalf("a Bash PreToolUse is not held: %v", r)
	}
	if r := reply(hookReq("PreToolUse", "AskUserQuestion", nil)); r["retry"] != nil {
		t.Fatalf("a question PreToolUse is not held: %v", r)
	}
	if r := reply(hookReq("PermissionRequest", "AskUserQuestion", func(r *sockRequest) { r.Resume = true })); r["retry"] != true {
		t.Fatalf("a resumed question: %v", r)
	}
	if r := reply(hookReq("Stop", "", func(r *sockRequest) { r.ReportOnly = true })); r["output"] != nil {
		t.Fatalf("report-only: %v", r)
	}
	_ = reply(hookReq("PostToolUse", "Read", func(r *sockRequest) { r.ReportOnly = true })) // not a reported event
	if r := reply(sockRequest{Kind: "ask", Attempt: "a1", Input: json.RawMessage(`{"question":"q"}`)}); r["retry"] != true {
		t.Fatalf("ask: %v", r)
	}
	if r := reply(sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"built"}`)}); r["queued"] != true {
		t.Fatalf("notify: %v", r)
	}
	if r := reply(sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":" "}`)}); r["error"] == nil {
		t.Fatalf("an empty notify: %v", r)
	}
	// MCP gets what a node says when it cannot reach its manager (an older `personad mcp` knows it); the rest no node.
	if r := reply(sockRequest{Kind: "mcp.tools"}); r["error"] == nil || r["tools"] == nil {
		t.Fatalf("mcp.tools: %v", r)
	}
	if r := reply(sockRequest{Kind: "mcp.call", Name: "agent_send"}); r["is_error"] != true {
		t.Fatalf("mcp.call: %v, want an error result", r)
	}
	for _, req := range []sockRequest{{Kind: "mcp.tools", AwayOK: true}, {Kind: "mcp.call", AwayOK: true}} {
		if r := reply(req); r["away"] != true {
			t.Fatalf("%s from a current client: %v, want away", req.Kind, r)
		}
	}
	for _, kind := range []string{"launcher.env", "cli.call"} {
		if r := reply(sockRequest{Kind: kind}); r["away"] != true {
			t.Fatalf("%s: %v, want away", kind, r)
		}
	}

	var kept []string
	for _, e := range sb.sp.load() {
		var in hookInput
		_ = json.Unmarshal(e.Request.Input, &in)
		kept = append(kept, e.Kind+":"+in.HookEventName+in.ToolName)
	}
	// The non-resumed PreToolUse is spooled but not held; the resumed PermissionRequest was already reported by its first node.
	want := "hook:Stop hook:PermissionRequestBash hook:PreToolUseBash hook:PreToolUseAskUserQuestion hook:Stop notify:"
	if strings.Join(kept, " ") != want {
		t.Fatalf("kept %v\nwant %s", kept, want)
	}

	now = now.Add(standbyHold + time.Second) // the manager is away for long: the terminal asks
	if r := reply(hookReq("PermissionRequest", "Bash", nil)); r["retry"] != nil {
		t.Fatalf("after the hold: %v", r)
	}
	if r := reply(sockRequest{Kind: "ask", Attempt: "a2", Input: json.RawMessage(`{"question":"q"}`)}); r["status"] != nodeclient.AskUnavailable {
		t.Fatalf("ask after the hold: %v", r)
	}
}

// Through the real socket: clients take standby's "away" for no node, and a hook's card retries.
func TestStandbyOnTheSocket(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sb") // short: sun_path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	sb := newStandby(openSpool(SpoolPath()), t.Logf)
	sb.want()
	defer sb.unwant()
	ctx := context.Background()
	if _, err := nodeclient.Dial(ctx, sockRequest{Kind: "launcher.env"}); !errors.Is(err, nodeclient.ErrNoNode) {
		t.Fatalf("launcher.env in standby: %v, want ErrNoNode", err)
	}
	rep, err := nodeclient.NotifyNode(ctx, nodeclient.Dial, nodeclient.NotifyRequest{Title: "built"}, "")
	if err != nil || !rep.Queued || rep.Sent {
		t.Fatalf("notify: %+v %v", rep, err)
	}
	if out := nodeclient.Hook(ctx, hookReq("Stop", "", nil).Input, "w1:p1"); out != nil {
		t.Fatalf("a Stop printed %s", out)
	}
	if n := len(openSpool(SpoolPath()).load()); n != 2 {
		t.Fatalf("spooled %d, want the notification and the Stop", n)
	}
	sb.unwant()
	sb.want() // started again (the next manager went away too): still serves
	if _, err := nodeclient.Dial(ctx, sockRequest{Kind: "ping"}); err != nil {
		t.Fatalf("ping after a restart of standby: %v", err)
	}
}

// replayHub records what the manager gets from a replay.
type replayHub struct {
	mu    sync.Mutex
	got   []string
	ids   map[string]int
	onGot func(count int) // runs (under mu) after each event or notification the hub got
}

func newReplayHub(t testing.TB, n *Node) *replayHub {
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	rec := &replayHub{ids: map[string]int{}}
	note := func(what string, params json.RawMessage) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		rec.mu.Lock()
		defer rec.mu.Unlock()
		id, _ := p["spool_id"].(string)
		rec.ids[id]++
		rec.got = append(rec.got, what)
		if rec.onGot != nil {
			rec.onGot(len(rec.got))
		}
	}
	hub.OnEvent = func(event string, params json.RawMessage) {
		if event == "hook.event" {
			var p struct {
				Event string `json:"event"`
			}
			_ = json.Unmarshal(params, &p)
			note("event:"+p.Event, params)
		}
	}
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "notify" {
			var p struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(params, &p)
			note("notify:"+p.Title, params)
			return map[string]any{"sent": true}, nil
		}
		return nil, nil
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	return rec
}

func TestReplayHandsTheManagerWhatStandbyTookInOrderOnce(t *testing.T) {
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	rec := newReplayHub(t, n)
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	at := time.Now().Add(-time.Minute)
	add := func(kind string, req sockRequest, at time.Time) {
		if _, err := sp.add(spoolEntry{ID: newUUID(), At: at, Kind: kind, Request: req}); err != nil {
			t.Fatal(err)
		}
	}
	add("hook", hookReq("UserPromptSubmit", "", nil), at)
	add("notify", sockRequest{Kind: "notify", Input: json.RawMessage(`{"title":"tests passed"}`)}, at)
	add("hook", hookReq("Stop", "", nil), at)
	add("hook", hookReq("Stop", "", nil), time.Now().Add(-spoolMaxAge-time.Hour)) // too old: dropped

	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	n.replaySpool(context.Background(), sp)
	select {
	case <-n.replayed:
	default:
		t.Fatal("the gate for new hooks stayed shut")
	}
	waitFor(t, "the replay to arrive", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.got) == 3
	})
	rec.mu.Lock()
	got := strings.Join(rec.got, " ")
	ids := rec.ids
	rec.mu.Unlock()
	if got != "event:UserPromptSubmit notify:tests passed event:Stop" {
		t.Fatalf("replayed %q", got)
	}
	for id, c := range ids {
		if id == "" || c != 1 {
			t.Fatalf("spool ids %v: each once, never empty", ids)
		}
	}
	if left := sp.load(); len(left) != 0 {
		t.Fatalf("left in the spool: %+v", left)
	}
	n.replayed = make(chan struct{})
	n.replaySpool(context.Background(), sp) // nothing again: nothing sent twice
	time.Sleep(50 * time.Millisecond)
	if rec.mu.Lock(); len(rec.got) != 3 {
		t.Fatalf("a second replay sent %v", rec.got)
	}
	rec.mu.Unlock()
}

// Standby appends to the spool, and a replay takes what it handed over out of the file spoolRemoveEvery entries at a
// time and once at the end: neither rewrites the whole file for every entry.
func TestSpoolWritesInBatches(t *testing.T) {
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	rec := newReplayHub(t, n)
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	const kept = 150
	for range kept {
		if _, err := sp.add(spoolEntry{ID: newUUID(), At: time.Now(), Kind: "hook", Request: hookReq("Stop", "", nil)}); err != nil {
			t.Fatal(err)
		}
	}
	if sp.rewrites != 1 {
		t.Fatalf("filling the spool rewrote it %d times, want 1 (the first add makes the file, the rest append)", sp.rewrites)
	}
	sp.rewrites = 0
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	n.replaySpool(context.Background(), sp)
	if want := (kept + spoolRemoveEvery - 1) / spoolRemoveEvery; sp.rewrites != want {
		t.Fatalf("the replay rewrote the spool %d times, want %d", sp.rewrites, want)
	}
	if left := sp.load(); len(left) != 0 {
		t.Fatalf("%d left in the spool", len(left))
	}
	waitFor(t, "the replay to arrive", func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return len(rec.got) == kept })
}

// A replay cut short (the connection ended) still takes out what it handed over before it returns: the next one sends
// none of it again, and has the rest in order.
func TestReplayCutShortTakesOutWhatItHandedOver(t *testing.T) {
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.panes = map[string]string{}
	rec := newReplayHub(t, n)
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	var ids []string
	for range 200 {
		e := spoolEntry{ID: newUUID(), At: time.Now(), Kind: "hook", Request: hookReq("Stop", "", nil)}
		if _, err := sp.add(e); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec.mu.Lock()
	rec.onGot = func(count int) {
		if count == spoolRemoveEvery+6 {
			cancel()
		}
	}
	rec.mu.Unlock()
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	n.replaySpool(ctx, sp)
	left := sp.load()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(left) == 0 || len(left) > len(ids)-len(rec.got) {
		t.Fatalf("%d left after the manager got %d of %d", len(left), len(rec.got), len(ids))
	}
	for i, e := range left {
		if e.ID != ids[len(ids)-len(left)+i] {
			t.Fatalf("left %d is %s, want the rest in order", i, e.ID)
		}
		if rec.ids[e.ID] > 0 {
			t.Fatalf("%s is still kept, though the manager got it", e.ID)
		}
	}
}

// A torn last line (a crash in an append) makes the next add rewrite the file, which leaves the torn line out.
func TestSpoolAddAfterATornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), spoolFile)
	sp := openSpool(path)
	if _, err := sp.add(spoolEntry{ID: "a", Kind: "hook"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":"b","ki`)
	_ = f.Close()
	if _, err := sp.add(spoolEntry{ID: "c", Kind: "hook"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"); len(lines) != 2 || !strings.Contains(lines[0], `"a"`) || !strings.Contains(lines[1], `"c"`) {
		t.Fatalf("spool file %q, want the entries a and c, one per line", b)
	}
}

func TestSpoolFaults(t *testing.T) {
	// 1. Truncated lines and invalid JSON
	dir := t.TempDir()
	path := filepath.Join(dir, spoolFile)
	corruptContent := strings.Join([]string{
		`{"id":"e1","kind":"notify"}`,
		`{"id":"e2", truncated`,
		`not-even-json`,
		`{"id":"e3","kind":"notify"}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(corruptContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs []string
	sp := openSpool(path)
	sp.logf = func(format string, args ...any) {
		logs = append(logs, strings.TrimSpace(format))
	}
	entries, _, err := sp.loadLocked()
	if err != nil {
		t.Fatalf("loadLocked: %v", err)
	}
	if len(entries) != 2 || entries[0].ID != "e1" || entries[1].ID != "e3" {
		t.Fatalf("entries = %+v, want e1 and e3", entries)
	}
	if len(logs) == 0 {
		t.Fatal("expected bad lines to be logged")
	}

	// 2. Unreadable / permission denied file
	if os.Getuid() != 0 { // skip if running as root
		unreadable := filepath.Join(dir, "unreadable.jsonl")
		if err := os.WriteFile(unreadable, []byte(`{"id":"e1"}`), 0o000); err != nil {
			t.Fatal(err)
		}
		spUnreadable := openSpool(unreadable)
		_, _, err = spUnreadable.loadLocked()
		if err == nil {
			t.Fatal("expected permission error on unreadable file, got nil")
		}
	}
}

func TestLegacyStateFaults(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "sessions.json")

	// 1. Corrupt legacy file must be quarantined to .corrupt and logged
	if err := os.WriteFile(legacyPath, []byte(`{invalid-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, strings.TrimSpace(format))
	}
	res := legacyState(legacyPath, logf)
	if len(res) != 0 {
		t.Fatalf("expected empty result from corrupt file, got %+v", res)
	}
	if _, err := os.Stat(legacyPath + ".corrupt"); err != nil {
		t.Fatalf("expected %s to be quarantined to .corrupt: %v", legacyPath, err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("expected original %s to be moved: %v", legacyPath, err)
	}
	if len(logs) == 0 {
		t.Fatal("expected corrupt legacy state to be logged")
	}

	// 2. Migration: notify failure must prevent the rename
	n, _ := testNode(t)
	n.dir = dir
	validLegacy := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(validLegacy, []byte(`{"sessions":{"s1":{"created":100}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// With peer == nil, Notify cannot succeed, migration must keep original file
	n.loadState(context.Background())
	if _, err := os.Stat(validLegacy); err != nil {
		t.Fatalf("expected %s to be preserved when Notify fails, got %v", validLegacy, err)
	}
	if _, err := os.Stat(validLegacy + ".migrated"); !os.IsNotExist(err) {
		t.Fatalf("expected %s.migrated not to exist when Notify fails", validLegacy)
	}
}
