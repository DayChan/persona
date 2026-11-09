package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

func TestRuntimePaneSourceOptIn(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{
		{MachineID: "demo", SessionID: "terminal", SourceKind: "runtime", PaneID: "orca:tab:leaf"},
		{MachineID: "demo", SessionID: "desktop", SourceKind: "runtime", SourceInstanceID: "orca"},
		{MachineID: "demo", SessionID: "feishu", PaneID: "botmux:topic"},
	}
	p := runtimePage(t, h, "pane_source=terminal")
	if p.Total != 1 || len(p.Sessions) != 1 || p.Sessions[0].SessionID != "terminal" || p.Revision == "" {
		t.Fatalf("pane_source=terminal must select one indexed terminal row; got total=%d sessions=%d revision=%q", p.Total, len(p.Sessions), p.Revision)
	}
}

type paneCase struct {
	Name, Want string
	Session    proto.Session
}

type originRow struct {
	proto.Session
	PaneSource string `json:"pane_source"`
}

type originPage struct {
	Sessions []originRow `json:"sessions"`
	Remove   []string    `json:"remove"`
	HasMore  bool        `json:"has_more"`
	Cursor   string      `json:"next_cursor"`
	Revision string      `json:"revision"`
	Total    int         `json:"total"`
}

func getOriginPage(t *testing.T, h *Hub, q url.Values) originPage {
	t.Helper()
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+q.Encode(), nil))
	var p originPage
	if w.Code != 200 {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func getOriginCounts(t *testing.T, h *Hub, q url.Values) sessionlist.Counts {
	t.Helper()
	q = cloneOriginQuery(q)
	q.Set("counts", "1")
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+q.Encode(), nil))
	var c sessionlist.Counts
	if w.Code != 200 {
		t.Fatalf("counts: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRuntimePaneSource8000(t *testing.T) {
	raw, err := os.ReadFile("../sessionorigin/testdata/pane_sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []paneCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	h := newTestHub(t)
	for n := range 4 {
		mid := fmt.Sprintf("machine-%d", n)
		m := newMachine(h, MachineConfig{ID: mid})
		h.machines, h.byID[mid] = append(h.machines, m), m
	}
	var all []originRow
	for n := range 8000 {
		// The last 800 rows are Feishu, beyond every unfiltered first page.
		c := cases[n%(len(cases)-2)]
		if n >= 7200 {
			c = cases[len(cases)-2+n%2]
		}
		s := c.Session
		s.MachineID, s.SessionID = fmt.Sprintf("machine-%d", n%4), fmt.Sprintf("session-%04d", n/4)
		s.Title, s.UpdatedAt = fmt.Sprintf("Row %04d", n), time.Unix(int64(8000-n), 0).UTC()
		s.State = []string{"working", "unread", "idle", "unknown"}[(n/5)%4]
		s.Cwd, s.SpaceID = fmt.Sprintf("/demo/project-%d", n%5), fmt.Sprintf("group-%d", (n/7)%3)
		s.Agent = []string{"claude", "codex"}[(n/3)%2]
		// The origin domain intentionally includes assistant rows; it is not the
		// clients' preference-dependent Agents membership contract.
		s.Assistant = n%101 == 0
		m := h.byID[s.MachineID]
		m.sessions = append(m.sessions, s)
		all = append(all, originRow{Session: s, PaneSource: c.Want})
	}
	legacyBefore := httptest.NewRecorder()
	h.listSessions(legacyBefore, httptest.NewRequest("GET", "/api/v1/sessions", nil))
	if strings.Contains(legacyBefore.Body.String(), `"pane_source"`) {
		t.Fatal("legacy payload gained origin")
	}
	queries := []url.Values{{"summary": {"1"}}}
	for _, source := range []string{"terminal", "desktop", "unknown", "feishu"} {
		queries = append(queries, url.Values{"pane_source": {source}},
			url.Values{"pane_source": {source}, "summary": {"1"}, "source": {"runtime"}},
			url.Values{"pane_source": {source}, "machine_id": {"machine-2"}, "group": {"group-1"}, "state": {"working"}},
			url.Values{"pane_source": {source}, "project": {"/demo/project-3"}, "provider": {"codex"}, "q": {"Row 7"}})
	}
	for _, q := range queries {
		t.Run(q.Encode(), func(t *testing.T) {
			var expected []originRow
			want := sessionlist.Counts{Machine: map[string]int{}, Project: map[string]int{}, Group: map[string]int{}, State: map[string]int{}, PaneSource: map[string]int{}}
			for _, row := range all {
				// Expected origin labels are independently checked against sessionSource
				// in web/src/lib/pane-source-contract.test.ts, never against the Go helper.
				if q.Get("pane_source") != "" && q.Get("pane_source") != row.PaneSource ||
					q.Get("source") != "" && q.Get("source") != row.SourceKind ||
					q.Get("machine_id") != "" && q.Get("machine_id") != row.MachineID ||
					q.Get("group") != "" && q.Get("group") != row.SpaceID ||
					q.Get("state") != "" && q.Get("state") != row.State ||
					q.Get("project") != "" && q.Get("project") != row.Cwd ||
					q.Get("provider") != "" && q.Get("provider") != row.Agent ||
					q.Get("q") != "" && !strings.Contains(row.Title, q.Get("q")) {
					continue
				}
				expected = append(expected, row)
				want.Total++
				want.Machine[row.MachineID]++
				want.Project[row.Cwd]++
				want.Group[row.SpaceID]++
				want.State[row.State]++
				want.PaneSource[row.PaneSource]++
			}
			q.Set("limit", "137")
			p := getOriginPage(t, h, q)
			want.Revision = p.Revision
			if p.Total != len(expected) {
				t.Fatalf("total=%d, want %d", p.Total, len(expected))
			}
			if c := getOriginCounts(t, h, q); !reflect.DeepEqual(c, want) {
				t.Fatalf("counts=%+v want %+v", c, want)
			}
			got := []originRow{}
			seen := map[string]bool{}
			for {
				if p.Total != len(expected) || p.Revision != want.Revision {
					t.Fatal("page contract changed")
				}
				for _, row := range p.Sessions {
					key := runtimeListID(row.MachineID, row.SessionID)
					if seen[key] {
						t.Fatalf("duplicate %s", key)
					}
					seen[key] = true
					got = append(got, row)
				}
				if !p.HasMore {
					break
				}
				q.Set("cursor", p.Cursor)
				if c := getOriginCounts(t, h, q); !reflect.DeepEqual(c, want) {
					t.Fatalf("frozen counts=%+v want %+v", c, want)
				}
				p = getOriginPage(t, h, q)
			}
			// Times are distinct in the corpus: expected is the original full-list order.
			if len(got) != len(expected) {
				t.Fatalf("union=%d want %d", len(got), len(expected))
			}
			for n, row := range got {
				e := expected[n]
				if row.MachineID != e.MachineID || row.SessionID != e.SessionID || row.PaneSource != e.PaneSource || row.Host != e.Host {
					t.Fatalf("row %d: %s/%s origin=%s host=%s; want %s/%s origin=%s host=%s", n, row.MachineID, row.SessionID, row.PaneSource, row.Host, e.MachineID, e.SessionID, e.PaneSource, e.Host)
				}
			}
		})
	}
	legacyAfter := httptest.NewRecorder()
	h.listSessions(legacyAfter, httptest.NewRequest("GET", "/api/v1/sessions", nil))
	if !bytes.Equal(legacyBefore.Body.Bytes(), legacyAfter.Body.Bytes()) {
		t.Fatal("indexing changed no-opt-in legacy output")
	}
	c := getOriginCounts(t, h, url.Values{})
	if !reflect.DeepEqual(c.PaneSource, map[string]int{"terminal": 2400, "desktop": 2400, "unknown": 2400, "feishu": 800}) {
		t.Fatalf("origin distribution=%v", c.PaneSource)
	}
}

func TestRuntimePaneSourceFrozenAndDelta(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "demo"})
	h.machines, h.byID["demo"] = []*Machine{m}, m
	m.sessions = []proto.Session{
		{MachineID: "demo", SessionID: "a", SourceKind: "runtime", PaneID: "orca:tab:a"},
		{MachineID: "demo", SessionID: "b", SourceKind: "runtime", PaneID: "orca:tab:b"},
		{MachineID: "demo", SessionID: "c", SourceKind: "runtime", SourceInstanceID: "orca"},
	}
	q := url.Values{"pane_source": {"terminal"}, "limit": {"1"}, "summary": {"1"}}
	first := getOriginPage(t, h, q)
	before := getOriginCounts(t, h, q)
	if first.Total != 2 || before.Total != 2 || first.Revision != before.Revision {
		t.Fatal("baseline")
	}
	// Only origin metadata changes: transport, time, state and identity stay put.
	m.mu.Lock()
	m.sessions[1].SourceInstanceID = "orca"
	m.sessions[2].SourceInstanceID = "pi"
	m.sessions[2].PaneID = "orca:tab:c"
	m.mu.Unlock()
	h.sessionsChanged(m)
	frozen := cloneOriginQuery(q)
	frozen.Set("cursor", first.Cursor)
	p := getOriginPage(t, h, frozen)
	if p.Revision != first.Revision || p.Total != 2 || len(p.Sessions) != 1 || p.Sessions[0].SessionID != "b" || p.Sessions[0].PaneSource != "terminal" {
		t.Fatalf("frozen: %+v", p)
	}
	if c := getOriginCounts(t, h, frozen); !reflect.DeepEqual(c, before) {
		t.Fatalf("frozen counts: %+v", c)
	}
	deltaQ := cloneOriginQuery(q)
	deltaQ.Set("since", first.Revision)
	delta := getOriginPage(t, h, deltaQ)
	if delta.Revision == first.Revision || delta.Total != 2 || !delta.HasMore {
		t.Fatalf("delta: %+v", delta)
	}
	current := getOriginCounts(t, h, deltaQ)
	if current.Total != 2 || current.Revision != delta.Revision {
		t.Fatalf("current counts: %+v", current)
	}
	// Freeze the incremental view as well, then switch the entering row out again.
	m.mu.Lock()
	m.sessions[2].SourceInstanceID = "orca"
	m.mu.Unlock()
	h.sessionsChanged(m)
	deltaQ.Set("cursor", delta.Cursor)
	next := getOriginPage(t, h, deltaQ)
	if next.Revision != delta.Revision || next.Total != 2 {
		t.Fatal("delta continuation drifted")
	}
	rows := append(delta.Sessions, next.Sessions...)
	removes := append(delta.Remove, next.Remove...)
	if len(rows) != 1 || rows[0].SessionID != "c" || rows[0].PaneSource != "terminal" || !reflect.DeepEqual(removes, []string{runtimeListID("demo", "b")}) {
		t.Fatalf("enter/leave: %+v %v", rows, removes)
	}
	if c := getOriginCounts(t, h, deltaQ); !reflect.DeepEqual(c, current) {
		t.Fatalf("frozen delta counts: %+v", c)
	}
	for _, source := range []string{"desktop", ""} {
		bad := cloneOriginQuery(frozen)
		if source == "" {
			bad.Del("pane_source")
		} else {
			bad.Set("pane_source", source)
		}
		for _, counts := range []bool{false, true} {
			if counts {
				bad.Set("counts", "1")
			}
			w := httptest.NewRecorder()
			h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+bad.Encode(), nil))
			if w.Code != 400 {
				t.Fatalf("cursor origin not bound: %d", w.Code)
			}
		}
	}
	if !slices.Contains(h.features(), featureSessionsPaneSource) || slices.Contains(proto.Features, featureSessionsPaneSource) {
		t.Fatal("runtime origin feature must be advertised only by the hub")
	}
}

func TestRuntimePaneSourceInvalidQueries(t *testing.T) {
	h := newTestHub(t)
	for _, query := range []string{"pane_source=", "pane_source=runtime", "pane_source=Terminal", "pane_source=terminal&pane_source=terminal", "pane_source=terminal,feishu"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+query, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
	}
}

func cloneOriginQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = slices.Clone(v)
	}
	return out
}

// Session has compatibility decoders (UnmarshalJSONFrom, UnmarshalJSON); do not promote them over the added scalar.
// encoding/json prefers UnmarshalJSONFrom, so originRow declares both.
func (s *originRow) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	return s.UnmarshalJSON(raw)
}

func (s *originRow) UnmarshalJSON(raw []byte) error {
	if err := json.Unmarshal(raw, &s.Session); err != nil {
		return err
	}
	var fields struct {
		PaneSource string `json:"pane_source"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	s.PaneSource = fields.PaneSource
	return nil
}

func TestRuntimePaneSourceExtensionScope(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "own-a"}, {ID: "own-b"}, {ID: "foreign"}}
	h.devices.assign = map[string]string{"own-a": "manager-a", "own-b": "manager-a", "foreign": "manager-b"}
	for _, cfg := range h.cfg.Machines {
		m := newMachine(h, cfg)
		m.sessions = []proto.Session{{MachineID: cfg.ID, SessionID: "same", PaneID: "w1:p1"}, {MachineID: cfg.ID, SessionID: "second", PaneID: "w1:p2"}}
		h.machines, h.byID[cfg.ID] = append(h.machines, m), m
	}
	request := func(q string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/sessions?"+q, nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleExtension, Manager: "manager-a"}))
		w := httptest.NewRecorder()
		h.listSessions(w, r)
		return w
	}
	for _, q := range []string{"pane_source=terminal", "pane_source=terminal&counts=1"} {
		if w := request(q); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Fatalf("unsupported aggregate: %d %s", w.Code, w.Body)
		}
	}
	if w := request("pane_source=terminal&machine_id=foreign"); w.Code != 403 {
		t.Fatalf("foreign machine: %d %s", w.Code, w.Body)
	}
	q := "pane_source=terminal&machine_id=own-a&limit=1"
	w := request(q)
	var first originPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.Total != 2 || len(first.Sessions) != 1 || first.Sessions[0].MachineID != "own-a" {
		t.Fatalf("authorized page: %d %s", w.Code, w.Body)
	}
	m := h.byID["own-a"]
	m.mu.Lock()
	m.sessions[1].SpaceKind = "codex_app"
	m.mu.Unlock()
	h.sessionsChanged(m)
	w = request(q + "&counts=1&cursor=" + url.QueryEscape(first.Cursor))
	var c sessionlist.Counts
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil || c.Total != 2 || c.Revision != first.Revision || c.PaneSource["terminal"] != 2 {
		t.Fatalf("authorized frozen counts: %d %s", w.Code, w.Body)
	}
}

func TestRuntimeOriginSummaryCompatibility(t *testing.T) {
	summary := runtimeSummary(proto.Session{StartedByPersona: true, Host: "paseo", Background: []proto.BackgroundTask{{ID: "bg"}}})
	// By value, by pointer and as a slice element: the last two would pick Session's promoted MarshalJSONTo.
	for _, v := range []any{summary, &summary, []runtimeRowSummary{summary}} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if raw[0] == '[' {
			raw = raw[1 : len(raw)-1]
		}
		checkOriginSummaryWire(t, raw)
	}
}

func checkOriginSummaryWire(t *testing.T, raw []byte) {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"started_by_persona": true, compat.StartedField: true, "pane_source": "desktop", "host": "paseo", "waiting": true, "background_count": float64(1)} {
		if wire[key] != want {
			t.Fatalf("%s=%v want %v", key, wire[key], want)
		}
	}
}
