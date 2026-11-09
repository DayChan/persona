package assistant

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// The selection's routes and storage (kb_selection.go) against an in-memory settings table and a fake hub.

func selectionCall(t *testing.T, s *Service, method, path, body, role string) *httptest.ResponseRecorder {
	t.Helper()
	return boardCall(s, method, path, body, role)
}

func selectionOf(t *testing.T, w *httptest.ResponseRecorder) kb.Selection {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body)
	}
	sel, err := kb.ParseSelection(w.Body.Bytes())
	if err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	return sel
}

func selectionJSON(t *testing.T, sel kb.Selection) string {
	t.Helper()
	raw, err := json.Marshal(sel)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// No document saved: GET is the defaults. PUT keeps a whole document, which GET then gives back as it was, the
// engine goes by at once (cached, refreshed on PUT), and the apps hear of.
func TestKBSelectionRoundTrip(t *testing.T) {
	s, fh, _ := historyToolService(t, nil, nil)
	got := selectionOf(t, selectionCall(t, s, "GET", "/api/v1/kb/selection", "", roleFull))
	if selectionJSON(t, got) != selectionJSON(t, kb.Defaults()) {
		t.Fatalf("defaults: %s", selectionJSON(t, got))
	}
	e := kbEng(t, s)
	if sel := e.Selection(); sel.Messages.Rules[kb.RuleGroup].On {
		t.Fatal("the engine's selection is not the defaults")
	}

	want := kb.Defaults()
	r := want.Messages.Rules[kb.RuleGroup]
	r.On, r.Days = true, new(int)
	*r.Days = 30
	want.Messages.Rules[kb.RuleGroup] = r
	want.Messages.Order = [][]string{{kb.RuleMuted}, {kb.RuleMentions, kb.RuleMine}, {kb.RuleDM}, {kb.RuleGroup}, {kb.RuleBot}, {kb.RuleOfficial}, {kb.RuleCollapsed}, {kb.RuleDone}}
	want.Items = []kb.SelectionItem{{Source: "chat", Kind: kb.ItemChat, ID: "g1", Name: "Group One", Read: true, Store: true}}
	back := 90
	want.History = map[string]kb.HistorySelection{"chat": {BackDays: &back}}
	fh.mu.Lock()
	before := len(fh.events)
	fh.mu.Unlock()
	put := selectionOf(t, selectionCall(t, s, "PUT", "/api/v1/kb/selection", selectionJSON(t, want), roleFull))
	if selectionJSON(t, put) != selectionJSON(t, want) {
		t.Fatalf("PUT answered %s", selectionJSON(t, put))
	}
	if got := selectionOf(t, selectionCall(t, s, "GET", "/api/v1/kb/selection", "", roleFull)); selectionJSON(t, got) != selectionJSON(t, want) {
		t.Fatalf("GET after PUT %s", selectionJSON(t, got))
	}
	if sel := e.Selection(); !sel.Messages.Rules[kb.RuleGroup].On || sel.Item("chat", kb.ItemChat, "g1") == nil || sel.BackDays("chat") != 90 {
		t.Fatalf("the engine still goes by the old document: %+v", sel.Messages.Rules[kb.RuleGroup])
	}
	fh.mu.Lock()
	events := slices.Clone(fh.events[before:])
	fh.mu.Unlock()
	if !slices.Contains(events, "kb_selection") {
		t.Fatalf("no kb_selection event: %v", events)
	}
	// Stored as JSON in the settings table, under its key.
	var stored json.RawMessage
	if ok, err := s.store().Setting(context.Background(), kbSelectionKey, &stored); !ok || err != nil || !strings.Contains(string(stored), `"g1"`) {
		t.Fatalf("stored %s %v %v", stored, ok, err)
	}
}

// PUT validates the whole document and changes nothing when it refuses it.
func TestKBSelectionValidation(t *testing.T) {
	s, _, _ := historyToolService(t, nil, nil)
	good := selectionJSON(t, kb.Defaults())
	cases := map[string]string{
		"unknown rule":     strings.Replace(good, `"mentions":{`, `"loud":{"on":true,"days":null,"attachment_days":null},"mentions":{`, 1),
		"unknown field":    strings.Replace(good, `"version":1`, `"version":1,"extra":true`, 1),
		"rule twice":       strings.Replace(good, `["mentions","mine"]`, `["mentions","mine","dm"]`, 1),
		"rule missing":     strings.Replace(good, `["done"]`, `[]`, 1),
		"zero days":        strings.Replace(good, `"dm":{"on":true,"days":null`, `"dm":{"on":true,"days":0`, 1),
		"context too wide": strings.Replace(good, `"context":50`, `"context":201`, 1),
		"item without id":  strings.Replace(good, `"items":[]`, `"items":[{"source":"chat","kind":"chat","id":"","read":true,"store":false,"days":null}]`, 1),
		"item kind":        strings.Replace(good, `"items":[]`, `"items":[{"source":"chat","kind":"thread","id":"x","read":true,"store":false,"days":null}]`, 1),
		"store unread":     strings.Replace(good, `"items":[]`, `"items":[{"source":"chat","kind":"chat","id":"x","read":false,"store":true,"days":null}]`, 1),
		"version":          strings.Replace(good, `"version":1`, `"version":2`, 1),
		"not json":         `{"version":`,
	}
	for name, body := range cases {
		if body == good {
			t.Fatalf("%s: the case changed nothing", name)
		}
		if w := selectionCall(t, s, "PUT", "/api/v1/kb/selection", body, roleFull); w.Code != 400 {
			t.Fatalf("%s: HTTP %d %s", name, w.Code, w.Body)
		}
	}
	if w := selectionCall(t, s, "PUT", "/api/v1/kb/selection", good, roleAnswer); w.Code != 403 {
		t.Fatalf("a read-only device: HTTP %d", w.Code)
	}
	if ok, _ := s.store().Setting(context.Background(), kbSelectionKey, &json.RawMessage{}); ok {
		t.Fatal("a refused document was stored")
	}
	if w := selectionCall(t, s, "PUT", "/api/v1/kb/selection", good, roleFull); w.Code != 200 {
		t.Fatalf("the defaults: HTTP %d %s", w.Code, w.Body)
	}
}

// A selection that cannot be read keeps everything (every rule on), never the defaults that would drop what a chat
// sends; once read, what was read holds while the database is away.
func TestKBSelectionUnreadableKeepsEverything(t *testing.T) {
	f := newFakeHub()
	st := newKBStore()
	s := kbServiceOn(t, f, st, t.TempDir(), nil)
	e := kbEng(t, s)
	st.mu.Lock()
	st.down = true
	st.mu.Unlock()
	sel := e.Selection()
	for name, r := range sel.Messages.Rules {
		if !r.On || r.Days != nil {
			t.Fatalf("%s while the database is down: %+v", name, r)
		}
	}
	if w := boardCall(s, "GET", "/api/v1/kb/selection", "", roleFull); w.Code != 503 {
		t.Fatalf("GET while down: HTTP %d", w.Code)
	}
	st.mu.Lock()
	st.down = false
	st.mu.Unlock()
	s.kb.selection.failedAt.Store(0)
	if sel := e.Selection(); sel.Messages.Rules[kb.RuleGroup].On {
		t.Fatal("the defaults were not read once the database was back")
	}
	st.mu.Lock()
	st.down = true
	st.mu.Unlock()
	s.kb.selection.cur.Load().at = s.kb.selection.cur.Load().at.Add(-2 * kbSelectionFresh)
	if sel := e.Selection(); sel.Messages.Rules[kb.RuleGroup].On {
		t.Fatal("what was read did not hold while the database was down")
	}
}

// The candidates are the source's chats with ids, names, kinds and flags, each with its per-item setting and what
// the selection reads of it. A renamed chat keeps its setting by id, and the setting takes the new name.
func TestKBSelectionCandidates(t *testing.T) {
	chats := []connectorwire.HistoryChat{{ID: "d1", Name: "Direct One", Kind: "dm", Estimate: 10},
		{ID: "g1", Name: "Group One", Kind: "group", Muted: true, Estimate: 900},
		{ID: "p1", Name: "Posts", Kind: "group", Source: "posts", Collapsed: true}}
	s, fh, _ := historyToolService(t, nil, chats)
	sel := kb.Defaults()
	sel.Items = []kb.SelectionItem{{Source: "chat", Kind: kb.ItemChat, ID: "g1", Name: "Old Name", Read: true, Store: true},
		{Source: "posts", Kind: kb.ItemChat, ID: "p1", Name: "Posts", Read: false}}
	if _, err := s.putKBSelection(context.Background(), sel); err != nil {
		t.Fatal(err)
	}
	if w := selectionCall(t, s, "GET", "/api/v1/kb/selection/candidates", "", roleFull); w.Code != 400 {
		t.Fatalf("without a source: HTTP %d", w.Code)
	}
	if w := selectionCall(t, s, "GET", "/api/v1/kb/selection/candidates?source=other", "", roleFull); w.Code != 404 {
		t.Fatalf("a source without history: HTTP %d %s", w.Code, w.Body)
	}
	w := selectionCall(t, s, "GET", "/api/v1/kb/selection/candidates?source=chat", "", roleFull)
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	var out kbSelectionCandidates
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	by := map[string]kbSelectionCandidate{}
	for _, c := range out.Chats {
		by[c.ID] = c
	}
	if out.Source != "chat" || out.Cached || len(out.Chats) != 3 || out.Chats[0].ID != "g1" {
		t.Fatalf("candidates %+v", out)
	}
	if d := by["d1"]; d.Name != "Direct One" || d.Kind != "dm" || d.Source != "chat" || d.Item != nil || d.Read != "whole" || d.Days != 365 {
		t.Fatalf("d1 %+v", d)
	}
	if g := by["g1"]; !g.Muted || g.Item == nil || !g.Item.Store || g.Read != "whole" {
		t.Fatalf("g1 %+v", g)
	}
	if p := by["p1"]; p.Source != "posts" || !p.Collapsed || p.Item == nil || p.Read != "none" {
		t.Fatalf("p1 %+v", p)
	}
	// The name the source lists now replaces the one the setting had; the setting itself stays.
	if it := storedSelection(t, s).Item("chat", kb.ItemChat, "g1"); it == nil || it.Name != "Group One" || !it.Store {
		t.Fatalf("g1's setting after the list %+v", it)
	}
	if by["g1"].Item.Name != "Group One" {
		t.Fatalf("candidate's item %+v", by["g1"].Item)
	}
	if n := len(fh.appParams(AppConnectors, assistantwire.ConnectorsHistory)); n != 0 {
		t.Fatalf("listing read %d pages", n)
	}
}

// While the import is stopped, the candidates come from the plan's list without asking the source.
func TestKBSelectionCandidatesWhileStopped(t *testing.T) {
	s, fh, data := historyToolService(t, nil, []connectorwire.HistoryChat{{ID: "d1", Name: "Direct One", Kind: "dm"}})
	p := &historyPlan{Source: "chat", Chats: []historyPlanChat{{ID: "d1", Name: "Direct One", Kind: "dm"}}}
	if err := saveHistoryPlan(data, p); err != nil {
		t.Fatal(err)
	}
	if err := writeHistoryStop(data, historyStop{By: "tool"}); err != nil {
		t.Fatal(err)
	}
	w := selectionCall(t, s, "GET", "/api/v1/kb/selection/candidates?source=chat", "", roleFull)
	var out kbSelectionCandidates
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || !out.Cached || len(out.Chats) != 1 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	if n := len(fh.appParams(AppConnectors, assistantwire.ConnectorsHistoryChats)); n != 0 {
		t.Fatalf("asked the source %d times while stopped", n)
	}
}

// While the database is away a document read before holds, and the database is asked again only a minute later: every
// ingest of an outage does not wait on it in turn.
func TestKBSelectionFailedReadBacksOff(t *testing.T) {
	f := newFakeHub()
	st := newKBStore()
	s := kbServiceOn(t, f, st, t.TempDir(), nil)
	e := kbEng(t, s)
	if sel := e.Selection(); sel.Stopgap() {
		t.Fatal("the defaults did not read")
	}
	st.mu.Lock()
	st.down = true
	st.reads = 0
	st.mu.Unlock()
	c := s.kb.selection.cur.Load()
	s.kb.selection.cur.Store(&kbSelectionCached{sel: c.sel, at: c.at.Add(-2 * kbSelectionFresh)})
	for range 20 {
		if sel := e.Selection(); sel.Stopgap() || sel.Messages.Rules[kb.RuleGroup].On {
			t.Fatal("what was read did not hold")
		}
		if _, err := s.kbSelection(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	st.mu.Lock()
	reads := st.reads
	st.mu.Unlock()
	if reads != 1 {
		t.Fatalf("the database was asked %d times during the outage", reads)
	}
}

// A document that cannot be read and never was stands in as a stopgap: everything is written, and the engine prunes
// nothing, so a line the user keeps longer than text_days survives the outage.
func TestKBSelectionStopgapPrunesNothing(t *testing.T) {
	f := newFakeHub()
	st := newKBStore()
	s := kbServiceOn(t, f, st, t.TempDir(), nil)
	e := kbEng(t, s)
	st.mu.Lock()
	st.down = true // and nothing read yet (a restart)
	st.mu.Unlock()
	s.kb.selection.cur.Store(nil)
	s.kb.selection.failedAt.Store(0)
	if !e.Selection().Stopgap() {
		t.Fatal("not a stopgap while the database is down")
	}
	ch := &kb.Chat{ID: "chat-g", Source: "chat", SourceID: "g", Kind: "group", Name: "G"}
	if err := e.Store.PutChat(ch); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -(e.Retention().Text() + 30))
	if err := e.Store.AppendMessages(ch, []kb.Line{{At: old, Sender: "S", Text: "kept 3650 days by the user"}}); err != nil {
		t.Fatal(err)
	}
	if n := e.Prune(time.Now()); n != 0 || len(e.Store.Days(ch.ID)) != 1 {
		t.Fatalf("pruned %d under the stopgap", n)
	}
}
