package hub

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
	"github.com/nwverse/persona/server/internal/sessionorigin"
)

type listTestPage struct {
	Sessions []proto.Session `json:"sessions"`
	Remove   []string        `json:"remove"`
	HasMore  bool            `json:"has_more"`
	Cursor   string          `json:"next_cursor"`
	Revision string          `json:"revision"`
	Total    int             `json:"total"`
}

func runtimePage(t *testing.T, h *Hub, query string) listTestPage {
	t.Helper()
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
	}
	var page listTestPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestRuntimeExactLookupFullRow(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	var full proto.Session
	// Include chat-only/task fields summaries omit; compare the entire DTO below,
	// not merely the subset the active chat currently happens to consume.
	if err := json.Unmarshal([]byte(`{
		"machine_id":"demo", "session_id":"old/session@child?exact&+", "title":"Old detail",
		"cwd":"/demo/app", "status":"idle", "state":"working", "agent":"claude",
		"launcher":"example", "model":"example-model", "permission_mode":"acceptEdits", "effort":"high",
		"ultracode":true, "ultracode_pending":true, "ultracode_note":"Keep this note", "efforts":["low","high"],
		"background":[{"id":"bg","description":"Background task"}],
		"subagents":[{"id":"sub","running":true}], "workflows":[{"id":"flow","running":true}],
		"queued":["First queued message","Second queued message"], "ports":[{"port":3000}],
		"loop":{"prompt":"Full loop prompt"}, "goal":{"objective":"Complete objective"},
		"plan":{"current":"Complete step"}, "work":{"phase":"blocked","on":"external","line":"Keep full work"},
		"channels":["example"], "context_tokens":100, "context_window":1000, "last_text":"Complete preview",
		"parent_session_id":"parent", "root_session_id":"root", "is_subagent":true, "codex_route_note":"Full route note"
	}`), &full); err != nil {
		t.Fatal(err)
	}
	m.sessions = []proto.Session{full}
	for n := range 100 {
		m.sessions = append(m.sessions, proto.Session{MachineID: "demo", SessionID: fmt.Sprintf("new%03d", n), UpdatedAt: time.Unix(int64(n+1), 0)})
	}
	other := newMachine(h, MachineConfig{ID: "other"})
	other.sessions = []proto.Session{{MachineID: "other", SessionID: full.SessionID, Title: "Other machine"}}
	h.machines, h.byID["other"] = append(h.machines, other), other
	q := "summary=0&limit=50&machine_id=demo&session_id=" + url.QueryEscape(full.SessionID)
	exact := runtimePage(t, h, q)
	legacy := runtimePage(t, h, "machine_id=demo")
	if exact.Total != 1 || len(exact.Sessions) != 1 || exact.HasMore || exact.Cursor != "" || len(exact.Remove) != 0 || exact.Revision == "" ||
		!reflect.DeepEqual(exact.Sessions[0], legacy.Sessions[0]) {
		t.Fatalf("exact row differs from legacy collection: %+v", exact)
	}
	if full := exact.Sessions[0]; len(full.Background) != 1 || len(full.Subagents) != 1 || len(full.Workflows) != 1 || len(full.Queued) != 2 ||
		full.PermissionMode != "acceptEdits" || full.Launcher != "example" || full.Ultracode == nil || full.Loop.Prompt == "" {
		t.Fatalf("full row lost detail: %+v", full)
	}
	if page := runtimePage(t, h, "summary=1&limit=50&machine_id=demo"); slices.ContainsFunc(page.Sessions, func(s proto.Session) bool { return s.SessionID == full.SessionID }) {
		t.Fatal("test target should be beyond the first ordinary page")
	}
	for _, q := range []string{"machine_id=demo&session_id=missing", "machine_id=missing&session_id=" + url.QueryEscape(full.SessionID),
		"machine_id=demo&session_id=" + url.QueryEscape(full.SessionID) + "&state=offline"} {
		page := runtimePage(t, h, q)
		if page.Total != 0 || len(page.Sessions) != 0 || page.Sessions == nil || page.Remove == nil || page.HasMore || page.Cursor != "" {
			t.Fatalf("missing: %+v", page)
		}
	}
	m.mu.Lock()
	m.sessions[0].Title = "Updated detail"
	m.sessions[0].Queued = []string{"New queue"}
	m.mu.Unlock()
	h.sessionsChanged(m)
	updated := runtimePage(t, h, q)
	if updated.Revision == exact.Revision || updated.Sessions[0].Title != "Updated detail" || updated.Sessions[0].Queued[0] != "New queue" {
		t.Fatalf("stale exact row: %+v", updated)
	}
	// Use the same deleted-session projection as every other collection reader.
	m.mu.Lock()
	m.sessions[0].Status = proto.StatusOffline
	m.mu.Unlock()
	h.deleted.mu.Lock()
	h.deleted.loaded, h.deleted.m = true, map[string]time.Time{"demo/" + full.SessionID: time.Now().UTC()}
	h.deleted.mu.Unlock()
	h.sessionsChanged(m)
	if page := runtimePage(t, h, q); page.Total != 0 || len(page.Sessions) != 0 {
		t.Fatalf("deleted row exposed: %+v", page)
	}
}

func TestRuntimeExactLookupValidationAndCompatibility(t *testing.T) {
	h := newTestHub(t)
	for _, query := range []string{"session_id=s", "machine_id=&session_id=s", "machine_id=demo&session_id=", "machine_id=demo&session_id=s&session_id=s",
		"machine_id=demo&machine_id=other&session_id=s", "machine_id=demo&session_id=s&since=v1.AAAAAAAAAAAAAAAAAAAAAA.0", "machine_id=demo&session_id=s&cursor=bad"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
	}
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	for n := range 60 {
		m.sessions = append(m.sessions, proto.Session{MachineID: "demo", SessionID: fmt.Sprintf("s%02d", n)})
	}
	// Unknown queries remain ignored, including on a legacy complete-list call.
	if page := runtimePage(t, h, "machine_id=demo&future_session_id=missing"); len(page.Sessions) != 60 || page.Revision != "" {
		t.Fatalf("legacy unknown query changed: %+v", page)
	}
	if page := runtimePage(t, h, "summary=0&limit=50&machine_id=demo&future_session_id=missing"); len(page.Sessions) != 50 || page.Total != 60 {
		t.Fatalf("unknown bounded query changed: %+v", page)
	}
	// A pre-lookup hub would ignore session_id and return the first 50 rows.
	// The separate advertised feature, not sessions_list, gates client use.
	if !slices.Contains(h.features(), featureSessionsList) || !slices.Contains(h.features(), featureSessionsLookup) ||
		slices.Contains(proto.Features, featureSessionsLookup) {
		t.Fatal("lookup must be advertised by the hub, not older node feature sets")
	}
}

func TestRuntimeExactLookupAuthorization(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "s"}}
	mux := h.routes()
	f := extTokensOf(t, h)
	read := f.issue("example-read", "Read", []string{extension.ScopeAgentsRead})
	noRead := f.issue("example-write", "Write", []string{extension.ScopeAgentsControl})
	path := "/api/v1/sessions?summary=0&limit=50&machine_id=demo&session_id=s"
	for _, token := range []string{"", "invalid", noRead} {
		code, body := call(t, mux, "GET", path, token, "")
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("unauthorized lookup: %d %v", code, body)
		}
	}
	if code, body := call(t, mux, "GET", path, read, ""); code != 200 || body["total"] != float64(1) {
		t.Fatalf("agents.read lookup: %d %v", code, body)
	}
}

func TestRuntimeListPagesAndSummary(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{
		{MachineID: "demo", SessionID: "b", Title: "Second", State: "working", UpdatedAt: time.Unix(100, 0), Cwd: "/demo/app", SpaceID: "work", LastText: strings.Repeat("你", 500), Background: []proto.BackgroundTask{{ID: "task"}}},
		{MachineID: "demo", SessionID: "a", Title: "First", State: "needs_you", UpdatedAt: time.Unix(100, 0), Cwd: "/demo/app", SpaceID: "work"},
		{MachineID: "demo", SessionID: "c", Title: "Old", State: "idle", UpdatedAt: time.Unix(90, 0), Cwd: "/demo/other"},
	}
	first := runtimePage(t, h, "summary=1&limit=1")
	if first.Total != 3 || !first.HasMore || first.Sessions[0].SessionID != "a" {
		t.Fatalf("first: %+v", first)
	}
	// Move a row while paging: the old frozen view still contains each row once.
	m.mu.Lock()
	m.sessions[1].UpdatedAt = time.Unix(200, 0)
	m.mu.Unlock()
	h.sessionsChanged(m)
	second := runtimePage(t, h, "summary=1&limit=1&cursor="+url.QueryEscape(first.Cursor))
	if second.Sessions[0].SessionID != "b" || second.Revision != first.Revision || len(second.Sessions[0].Background) != 0 || len([]rune(second.Sessions[0].LastText)) != 180 {
		t.Fatalf("second: %+v", second)
	}
	third := runtimePage(t, h, "summary=1&limit=1&cursor="+url.QueryEscape(second.Cursor))
	if third.HasMore || third.Sessions[0].SessionID != "c" {
		t.Fatalf("third: %+v", third)
	}
	filtered := runtimePage(t, h, "summary=true&state=working&machine_id=demo&project=%2Fdemo%2Fapp&group=work&q=second")
	if filtered.Total != 1 || filtered.Sessions[0].SessionID != "b" {
		t.Fatalf("filtered: %+v", filtered)
	}
	legacy := runtimePage(t, h, "machine_id=demo")
	if len(legacy.Sessions) != 3 || len(legacy.Sessions[0].Background) != 1 || len([]rune(legacy.Sessions[0].LastText)) != 500 {
		t.Fatalf("legacy changed: %+v", legacy)
	}
}

func TestRuntimeListDeltaTransitionsAndMachineRemoval(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "s/with/slashes", Title: "Waiting", State: "needs_you", UpdatedAt: time.Unix(100, 0)}}
	first := runtimePage(t, h, "summary=1&state=needs_you")
	m.mu.Lock()
	m.sessions[0].State = "working"
	m.sessions[0].Title = "Now working"
	m.mu.Unlock()
	h.sessionsChanged(m)
	delta := runtimePage(t, h, "summary=1&state=needs_you&since="+url.QueryEscape(first.Revision))
	if len(delta.Sessions) != 0 || len(delta.Remove) != 1 || delta.Remove[0] != runtimeListID("demo", "s/with/slashes") || delta.Total != 0 {
		t.Fatalf("exit: %+v", delta)
	}
	all := runtimePage(t, h, "summary=1")
	h.mu.Lock()
	h.machines = nil
	delete(h.byID, "demo")
	h.mu.Unlock()
	gone := runtimePage(t, h, "summary=1&since="+url.QueryEscape(all.Revision))
	if len(gone.Remove) != 1 || gone.Total != 0 {
		t.Fatalf("gone: %+v", gone)
	}
}

func TestRuntimeListCountsAndErrors(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "s", State: "unread", Cwd: "/demo/app"}}
	for _, flag := range []string{"1", "true"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?counts="+flag, nil))
		var counts struct {
			Total int
			State map[string]int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil || counts.Total != 1 || counts.State["unread"] != 1 {
			t.Fatalf("counts %s: %s %v", flag, w.Body, err)
		}
	}
	for _, query := range []string{"limit=0", "limit=201", "summary=maybe", "cursor=", "since=nonsense"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
	}
	p := runtimePage(t, h, "limit=1")
	other := newTestHub(t)
	w := httptest.NewRecorder()
	other.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?since="+url.QueryEscape(p.Revision), nil))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "revision_expired") {
		t.Fatalf("restart: %d %s", w.Code, w.Body)
	}
}

func TestRuntimeListSubscriberOnlyReceivesRevision(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "s", State: "idle", Title: "Old"}}
	h.sessionListIndex()
	c := &client{hub: h, listMode: true, delta: true, out: make(chan []byte, 4), role: roleFull}
	h.clients[c] = struct{}{}
	h.sessionListNotice(c)
	assertNotice := func() {
		t.Helper()
		select {
		case raw := <-c.out:
			var v map[string]any
			_ = json.Unmarshal(raw, &v)
			if v["type"] != "sessions_list" || v["revision"] == "" || len(raw) > 160 {
				t.Fatalf("not lightweight: %s", raw)
			}
		default:
			t.Fatal("missing notice")
		}
	}
	assertNotice()
	m.mu.Lock()
	m.sessions[0].Title = "Changed"
	m.mu.Unlock()
	h.sessionsChanged(m)
	assertNotice()
	h.sessionsChanged(m)
	if len(c.out) != 0 {
		t.Fatal("unchanged list sent a notice")
	}
}

func TestRuntimeListFollowsRevs(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{{MachineID: "demo", SessionID: "a", Rev: 1}, {MachineID: "demo", SessionID: "b", Rev: 1}}
	first := runtimePage(t, h, "summary=1")
	c := &client{hub: h, listMode: true, delta: true, out: make(chan []byte, 4), role: roleFull}
	h.clients[c] = struct{}{}
	m.onEvent("sessions.revs", json.RawMessage(`{"revs":{"a":7}}`))
	var notified bool
	for len(c.out) > 0 {
		var v map[string]any
		_ = json.Unmarshal(<-c.out, &v)
		notified = notified || v["type"] == "sessions_list"
	}
	if !notified {
		t.Fatal("paged client not told about the new revs")
	}
	delta := runtimePage(t, h, "summary=1&since="+url.QueryEscape(first.Revision))
	if len(delta.Sessions) != 1 || delta.Sessions[0].SessionID != "a" || delta.Sessions[0].Rev != 7 {
		t.Fatalf("delta: %+v", delta)
	}
	all := runtimePage(t, h, "summary=1")
	for _, s := range all.Sessions {
		if want := map[string]int64{"a": 7, "b": 1}[s.SessionID]; s.Rev != want {
			t.Fatalf("%s rev %d, want %d", s.SessionID, s.Rev, want)
		}
	}
}

// A paged-index id is json.Marshal's tuple of the machine and session ids, written by hand only when neither has a
// byte the encoder escapes.
func TestRuntimeListIDMatchesMarshal(t *testing.T) {
	for _, sid := range []string{"", "s", "a/b@c?d+e", `q"uote`, `back\slash`, "<b>&", "标题", "line\u2028", "\xff", "\x7f", "tab\t", "~ !#$%'()*,-.:;=[]^_`{|}"} {
		for _, pair := range [][2]string{{"demo", sid}, {sid, "s"}} {
			want, _ := json.Marshal(pair)
			if got := runtimeListID(pair[0], pair[1]); got != string(want) {
				t.Fatalf("%q: %s, want %s", pair, got, want)
			}
		}
	}
}

// fullIndexItems is what indexSessionsLocked indexed before it reused rows: every row's summary, full row and search
// text built from the row itself. The oracle of TestIndexRowReuseMatchesFullPath.
func fullIndexItems(mid string, list []proto.Session, encoded []sentSession) []sessionlist.Item {
	full := make(map[string]json.RawMessage, len(encoded))
	for _, row := range encoded {
		full[row.id] = row.raw
	}
	var items []sessionlist.Item
	seen := map[string]bool{}
	for _, row := range list {
		if row.SessionID == "" || seen[row.SessionID] || len(full[row.SessionID]) == 0 {
			continue
		}
		seen[row.SessionID] = true
		id, _ := json.Marshal([2]string{mid, row.SessionID})
		summary, _ := json.Marshal(runtimeSummary(row))
		items = append(items, sessionlist.Item{
			ID: string(id), Machine: mid, Project: cmp.Or(row.RepoRoot, row.WorkspaceCwd, row.Cwd), Group: cmp.Or(row.SpaceID, row.WorkspaceID),
			PaneSource: sessionorigin.Of(row), State: row.State, BackgroundWaiting: row.BackgroundWaiting, Source: row.SourceKind,
			Provider: row.Agent, UpdatedAt: row.UpdatedAt,
			SearchText: strings.Join([]string{row.Title, row.Cwd, row.RepoRoot, row.SpaceName, row.SlotName, row.AgentName, row.LastText}, "\n"),
			Summary:    json.RawMessage(summary),
			Full: runtimeListFields(full[row.SessionID], struct {
				PaneSource string `json:"pane_source"`
			}{sessionorigin.Of(row)}),
		})
	}
	return items
}

// The paged index a hub keeps while it reuses rows whose encoding it already holds is the index the full computation
// gives, at every step of lists whose rows change in what the index reads (cuts, zones, work, background, origin,
// grouping, folded invalid UTF-8), gain, lose, repeat or reorder rows, or whose machine goes and comes back: the same
// pages, summaries, counts, revisions and deltas. A list that did not change makes no revision, and an unchanged row
// keeps the bytes it was indexed with.
func TestIndexRowReuseMatchesFullPath(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	base := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	for i := range 12 {
		m.sessions = append(m.sessions, proto.Session{MachineID: "demo", SessionID: fmt.Sprintf("s%02d", i), Title: fmt.Sprintf("needle %d", i),
			Cwd: "/demo/app", State: "idle", Status: proto.StatusIdle, UpdatedAt: base.Add(time.Duration(i) * time.Minute), LastText: "Demo preview"})
	}
	var ref sessionlist.Index
	index := func() {
		list := h.shown("demo", h.decorated(m))
		enc := encodeSessions(list)
		h.sessionsMu.Lock()
		h.indexSessionsLocked("demo", list, enc)
		h.sessionsMu.Unlock()
		ref.Replace("demo", fullIndexItems("demo", list, enc))
	}
	got := h.sessionListIndex()
	index()
	cycle := func(n int, values ...string) string { return values[n%len(values)] }
	rowEdits := []func(r *proto.Session, n int){
		func(r *proto.Session, n int) { r.Title = strings.Repeat("题", 255+n%3) },             // around the summary's 256-rune cut
		func(r *proto.Session, n int) { r.Title = strings.Repeat("t", 256) + fmt.Sprint(n) }, // past the cut: the full row only
		func(r *proto.Session, n int) { r.LastText = strings.Repeat("é", 179+n%3) },
		func(r *proto.Session, n int) { r.Title = "needle " + string([]byte{0xfe + byte(n%2)}) }, // the same bytes once encoded
		func(r *proto.Session, n int) { r.UpdatedAt = r.UpdatedAt.In(time.FixedZone("demo", 3600*(n%3))) },
		func(r *proto.Session, n int) { r.UpdatedAt = r.UpdatedAt.Add(time.Second) },
		func(r *proto.Session, n int) {
			wake := base.Add(time.Hour)
			r.Work = []*proto.WorkState{nil, {Phase: "blocked", On: "external", WakeAt: &wake}, {Phase: "blocked", On: "user", Wait: "answer"},
				{Phase: "done", Line: strings.Repeat("w", 181)}}[n%4]
		},
		func(r *proto.Session, n int) {
			r.Plan, r.Goal, r.Loop = nil, nil, nil
			switch n % 4 {
			case 1:
				r.Plan = &proto.PlanInfo{Done: 1, Total: 3, Current: strings.Repeat("p", 200)}
			case 2:
				r.Goal = &proto.GoalInfo{Objective: "Ship the demo", Status: "active"}
			case 3:
				r.Loop = &proto.LoopInfo{Kind: "interval", Prompt: "Check the demo", IntervalSeconds: 60}
			}
		},
		func(r *proto.Session, n int) { r.Background = make([]proto.BackgroundTask, n%3) },
		func(r *proto.Session, n int) {
			r.Subagents = []proto.Subagent{{ID: "sub", Running: n%2 == 0}}
			r.Workflows = []proto.Workflow{{TaskID: "flow", Running: n%3 == 0}}
		},
		func(r *proto.Session, n int) { // pane_source: desktop, feishu, terminal, unknown
			r.SourceKind, r.SourceInstanceID, r.PaneID = cycle(n, "runtime", "", "", ""), cycle(n, "orca", "", "", ""), cycle(n, "", "botmux:topic", "herdr:1", "")
			r.Agent = cycle(n, "claude", "codex")
		},
		func(r *proto.Session, n int) {
			r.SpaceID, r.WorkspaceID = cycle(n, "", "space", ""), cycle(n, "", "", "workspace")
		},
		func(r *proto.Session, n int) {
			r.RepoRoot, r.WorkspaceCwd, r.Cwd = cycle(n, "", "/demo/repo", ""), cycle(n, "", "", "/demo/real"), cycle(n, "/demo/app", "/demo/b")
		},
		func(r *proto.Session, n int) {
			r.State, r.BackgroundWaiting = cycle(n, "working", "needs_you", "idle"), n%2 == 0
		},
		func(r *proto.Session, n int) { r.Rev++ },
		func(r *proto.Session, n int) { r.Archived = n%2 == 0 }, // hidden from the lists while archived
	}
	listEdits := []func(list []proto.Session, n int) []proto.Session{
		func(list []proto.Session, n int) []proto.Session {
			return append(list, proto.Session{MachineID: "demo", SessionID: fmt.Sprintf("new%03d", n), UpdatedAt: base})
		},
		func(list []proto.Session, n int) []proto.Session {
			return slices.Delete(list, n%len(list), n%len(list)+1)
		},
		func(list []proto.Session, n int) []proto.Session {
			r := list[n%len(list)]
			return append([]proto.Session{r}, slices.Delete(list, n%len(list), n%len(list)+1)...)
		},
		func(list []proto.Session, n int) []proto.Session { // a second row under an id: summary of one, bytes of the other
			r := list[n%len(list)]
			r.Title = "another row under the same id"
			return append(list, r)
		},
		func(list []proto.Session, n int) []proto.Session { // the duplicates go, the last of each stays
			last := map[string]int{}
			for i, r := range list {
				last[r.SessionID] = i
			}
			var out []proto.Session
			for i, r := range list {
				if last[r.SessionID] == i {
					out = append(out, r)
				}
			}
			return out
		},
	}
	view := func(i *sessionlist.Index, since ...string) []any {
		epoch := strings.Split(i.Revision(), ".")[1]
		var out []any
		for _, o := range []sessionlist.Options{{Limit: 200}, {Limit: 200, Summary: true}, {Limit: 200, Q: "needle"}, {Limit: 200, Since: since[0]},
			{Limit: 200, Summary: true, Since: since[1]}} {
			p, err := i.Query(o)
			p.Revision = strings.Replace(p.Revision, epoch, "E", 1)
			out = append(out, p, fmt.Sprint(err))
		}
		c, err := i.Counts(sessionlist.Options{})
		c.Revision = strings.Replace(c.Revision, epoch, "E", 1)
		return append(out, c, fmt.Sprint(err))
	}
	first := [2]string{got.Revision(), ref.Revision()}
	prev := first
	check := func(step string) {
		t.Helper()
		if a, b := view(got, prev[0], first[0]), view(&ref, prev[1], first[1]); !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: the reused index differs from the full one\nreused: %v\nfull:   %v", step, a, b)
		}
		prev = [2]string{got.Revision(), ref.Revision()}
	}
	edit := func(step string, f func([]proto.Session) []proto.Session) {
		t.Helper()
		m.mu.Lock()
		m.sessions = f(slices.Clone(m.sessions))
		m.mu.Unlock()
		index()
		check(step)
	}
	rng := rand.New(rand.NewPCG(2432, 1))
	for step := range 110 {
		n := rng.IntN(1 << 20)
		switch k := rng.IntN(len(rowEdits) + len(listEdits) + 1); {
		case step < len(rowEdits): // each edit once, in order, then at random
			edit(fmt.Sprint("row edit ", step), func(l []proto.Session) []proto.Session { rowEdits[step](&l[n%len(l)], n); return l })
		case step < len(rowEdits)+len(listEdits):
			edit(fmt.Sprint("list edit ", step-len(rowEdits)), func(l []proto.Session) []proto.Session { return listEdits[step-len(rowEdits)](l, n) })
		case k < len(rowEdits):
			edit(fmt.Sprint("step ", step, " row edit ", k), func(l []proto.Session) []proto.Session { rowEdits[k](&l[n%len(l)], n); return l })
		case k < len(rowEdits)+len(listEdits):
			edit(fmt.Sprint("step ", step, " list edit ", k-len(rowEdits)), func(l []proto.Session) []proto.Session { return listEdits[k-len(rowEdits)](l, n) })
		default: // the machine goes (RemoveSource) and comes back
			h.mu.Lock()
			h.machines = nil
			delete(h.byID, "demo")
			h.mu.Unlock()
			h.pruneSessionListMachines()
			ref.RemoveSource("demo")
			check(fmt.Sprint("step ", step, " machine removed"))
			h.mu.Lock()
			h.machines, h.byID["demo"] = []*Machine{m}, m
			h.mu.Unlock()
			edit(fmt.Sprint("step ", step, " machine back"), func(l []proto.Session) []proto.Session { return l })
		}
	}
	m.mu.Lock()
	m.sessions = slices.DeleteFunc(slices.Clone(m.sessions), func(s proto.Session) bool { return s.Archived })
	m.sessions[0].Rev++
	m.mu.Unlock()
	index()
	check("settle")
	id := runtimeListID("demo", m.sessions[1].SessionID)
	kept := got.Items([]string{id})[0].Full.(json.RawMessage)
	revision := got.Revision()
	index()
	m.mu.Lock()
	m.sessions = slices.Clone(m.sessions)
	m.sessions[0].Rev++
	m.mu.Unlock()
	index()
	if after := got.Items([]string{id})[0].Full.(json.RawMessage); &after[0] != &kept[0] {
		t.Fatal("an unchanged row was indexed again rather than kept")
	}
	if got.Revision() == revision {
		t.Fatal("the changed row made no revision")
	}
	revision = got.Revision()
	index()
	if got.Revision() != revision {
		t.Fatal("an unchanged list made a revision")
	}
	check("end")
}
