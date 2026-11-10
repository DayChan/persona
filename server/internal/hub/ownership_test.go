package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeManager is a manager on the bus that answers every http.request with who it is and a list of one item of its
// own under each list key, and every devices.call with who it is.
type fakeManager struct {
	id  string
	bus *rawBus
	got chan string // "METHOD path" of each http.request
}

func connectFakeManager(t *testing.T, h *Hub, srv *httptest.Server, id string) *fakeManager {
	t.Helper()
	f := &fakeManager{id: id, got: make(chan string, 64)}
	token := busToken(t, h, id, id)
	x := &relayingExt{serve: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := []map[string]any{{"id": "extension-" + id}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"who": id, "extensions": item})
	}), dial: func(ctx context.Context, query string) (*websocket.Conn, error) {
		return bus.DialAt(ctx, srv.URL, token, srv.Client(), devicewire.RelayPath, query)
	}}
	f.bus = dialRawBusWith(t, srv, token, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case extwire.MethodAccept:
			out, _, err := x.Request(ctx, method, params)
			return out, err
		case assistantwire.MethodHTTP:
			var req assistantwire.HTTPRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			select {
			case f.got <- req.Method + " " + req.Path:
			default:
			}
			item := []map[string]any{{"id": "item-" + id}}
			body, _ := json.Marshal(map[string]any{"who": id, "items": item, "todos": item, "as_of": "t"})
			return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: body}, nil
		case devicewire.MethodCall:
			return map[string]any{"who": id}, nil
		}
		return nil, &rpc.Error{Code: "no_such_thing", Message: "the fake manager has no " + method}
	})
	if err := f.bus.call("manager.hello", map[string]any{"id": id}, nil); err != nil {
		t.Fatal(err)
	}
	return f
}

// report says the manager carries machine mid (devices.state), connected.
func (f *fakeManager) report(t *testing.T, mids ...string) {
	t.Helper()
	st := devicewire.States{}
	for _, mid := range mids {
		st.Machines = append(st.Machines, devicewire.State{Machine: proto.Machine{ID: mid, Name: mid, Online: true}, Connected: true,
			ConnectedAt: time.Now().UTC()})
	}
	if err := f.bus.peer.Notify(devicewire.EventState, st); err != nil {
		t.Fatal(err)
	}
}

func ownershipGet(t *testing.T, h *Hub, srv *httptest.Server, method, path string, header map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// With two managers on the bus, a request goes to the manager its thing belongs to, or the one it names, else the
// default; lists merge both, each item stamped; an offline owner is 503, an unknown one 404.
func TestOwnershipRoutesByManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	a := connectFakeManager(t, h, srv, assistantwire.AssistantID)
	b := connectFakeManager(t, h, srv, "m-0a0b0c")
	if _, err := h.moveMachine(context.Background(), "mb", "m-0a0b0c"); err != nil { // the roster says mb is its
		t.Fatal(err)
	}
	a.report(t, "ma")
	b.report(t, "mb")
	eventually(t, 5*time.Second, "both machines reported", func() bool {
		return h.machineOwner("ma") == assistantwire.AssistantID && h.machineOwner("mb") == "m-0a0b0c" &&
			h.localMachine("mb") != nil && h.localMachine("ma") != nil
	})

	// What names no thing: the default, or the manager it names (header or query).
	for _, tc := range []struct {
		path   string
		header map[string]string
		want   string
	}{
		{"/api/v1/assistant", nil, assistantwire.AssistantID},
		{"/api/v1/assistant", map[string]string{ManagerHeader: "m-0a0b0c"}, "m-0a0b0c"},
		{"/api/v1/assistant?manager=m-0a0b0c", nil, "m-0a0b0c"},
		{"/api/v1/model-config", map[string]string{ManagerHeader: "m-0a0b0c"}, "m-0a0b0c"},
		{"/api/v1/inbox/x?manager=m-0a0b0c", nil, "m-0a0b0c"},
		// A machine's route goes to its manager, whatever the request names.
		{"/api/v1/machines/mb/herdr", nil, "m-0a0b0c"},
		{"/api/v1/machines/ma/herdr", map[string]string{ManagerHeader: "m-0a0b0c"}, assistantwire.AssistantID},
	} {
		code, out := ownershipGet(t, h, srv, http.MethodGet, tc.path, tc.header)
		if code != http.StatusOK || out["who"] != tc.want {
			t.Fatalf("%s %v: %d %v, want %s's", tc.path, tc.header, code, out, tc.want)
		}
	}
	if code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/assistant", map[string]string{ManagerHeader: "m-ffffff"}); code != http.StatusNotFound || out["code"] != "no_manager" {
		t.Fatalf("unknown manager: %d %v", code, out)
	}

	// A list merges every manager's, the default's first, each item with its manager.
	code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/inbox", nil)
	items, _ := out["items"].([]any)
	if code != http.StatusOK || len(items) != 2 || out["as_of"] != "t" {
		t.Fatalf("merged inbox: %d %v", code, out)
	}
	for i, want := range []string{assistantwire.AssistantID, "m-0a0b0c"} {
		it := items[i].(map[string]any)
		if it["manager"] != want || it["id"] != "item-"+want {
			t.Fatalf("item %d: %v, want %s's", i, it, want)
		}
	}
	// Naming a manager lists only its own.
	if _, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/todos?manager=m-0a0b0c", nil); len(out["todos"].([]any)) != 1 ||
		out["todos"].([]any)[0].(map[string]any)["manager"] != "m-0a0b0c" {
		t.Fatalf("one manager's todos: %v", out)
	}

	// The machines and their sessions carry their manager.
	machines := map[string]string{}
	for _, m := range h.machineList() {
		machines[m.ID] = m.Manager
	}
	if machines["ma"] != assistantwire.AssistantID || machines["mb"] != "m-0a0b0c" {
		t.Fatalf("machines' managers: %v", machines)
	}
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "s1"}}})
	if list := h.decorated(h.localMachine("mb")); len(list) != 1 || list[0].Manager != "m-0a0b0c" {
		t.Fatalf("sessions' manager: %+v", list)
	}
	// A node call goes through the machine's manager.
	var who map[string]any
	if err := h.localMachine("mb").call(context.Background(), "panes.list", map[string]any{}, &who); err != nil || who["who"] != "m-0a0b0c" {
		t.Fatalf("node call through mb's manager: %v %v", who, err)
	}

	// An interaction is the manager's that reported it: its resolve goes there.
	if err := b.bus.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: proto.Interaction{
		ID: "ix-b", MachineID: "mb", SessionID: "s1", Kind: "question", CreatedAt: time.Now(),
		Questions: []proto.Question{{Question: "Which option?", Options: []proto.QuestionOption{{Label: "First"}}}}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the interaction", func() bool { return h.interactionOwner("ix-b") == "m-0a0b0c" })
	if code, out := ownershipGet(t, h, srv, http.MethodPost, "/api/v1/interactions/ix-b/resolve", nil); code != http.StatusOK || out["who"] != "m-0a0b0c" {
		t.Fatalf("resolve: %d %v", code, out)
	}
	// Choosing also follows the card's manager, even if the request names another.
	rev := askRev(proto.Question{Question: "Which option?", Options: []proto.QuestionOption{{Label: "First"}}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/interactions/ix-b/choose", strings.NewReader(`{"option":0,"rev":"`+rev+`"}`))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	req.Header.Set(ManagerHeader, assistantwire.AssistantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var chosen map[string]any
	decodeErr := json.NewDecoder(resp.Body).Decode(&chosen)
	_ = resp.Body.Close()
	if decodeErr != nil || resp.StatusCode != http.StatusOK || chosen["who"] != "m-0a0b0c" {
		t.Fatalf("choose: %d %v %v", resp.StatusCode, chosen, decodeErr)
	}
	// The assistant's own list replacing its interactions leaves the other manager's.
	h.takeFleetInteractions(nil)
	if h.interactionOwner("ix-b") != "m-0a0b0c" {
		t.Fatal("the assistant's report dropped another manager's interaction")
	}

	// Offline: its machine stays listed (offline, still its), its routes are 503, the lists go on without it.
	_ = b.bus.ws.Close(websocket.StatusNormalClosure, "test")
	eventually(t, 5*time.Second, "m-0a0b0c offline", func() bool { return h.busConnOf("m-0a0b0c") == nil })
	if code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/machines/mb/herdr", nil); code != http.StatusServiceUnavailable || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("offline owner's machine: %d %v", code, out)
	}
	if code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/assistant", map[string]string{ManagerHeader: "m-0a0b0c"}); code != http.StatusServiceUnavailable || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("offline manager named: %d %v", code, out)
	}
	if _, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/inbox", nil); len(out["items"].([]any)) != 1 {
		t.Fatalf("inbox without the offline manager: %v", out)
	}
	eventually(t, 5*time.Second, "mb shown offline, still m-0a0b0c's", func() bool {
		for _, m := range h.machineList() {
			if m.ID == "mb" {
				return !m.Online && m.Manager == "m-0a0b0c"
			}
		}
		return false
	})
	if m := h.localMachine("ma"); m == nil || !m.snapshot().Online {
		t.Fatal("the other manager's machine went offline too")
	}
}

// One manager: a list's answer is the manager's own with each item stamped, nothing else changed.
func TestMergeListOneManager(t *testing.T) {
	rec := httptest.NewRecorder()
	writeMerged(rec, "items", []listAnswer{{id: "assistant", status: 200, header: http.Header{"X-Kept": {"1"}},
		body: []byte(`{"items":[{"id":"a","n":1},{}],"as_of":"t"}`)}})
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	items := out["items"].([]any)
	if rec.Header().Get("X-Kept") != "1" || out["as_of"] != "t" || len(items) != 2 ||
		items[0].(map[string]any)["manager"] != "assistant" || items[0].(map[string]any)["n"] != 1.0 || items[1].(map[string]any)["manager"] != "assistant" {
		t.Fatalf("merged: %s", rec.Body.String())
	}
	// Nobody answered: the default's answer as it is.
	rec = httptest.NewRecorder()
	writeMerged(rec, "items", []listAnswer{{id: "assistant", status: 503, body: []byte(`{"code":"assistant_offline"}`)}})
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "assistant_offline") {
		t.Fatalf("offline: %d %s", rec.Code, rec.Body.String())
	}
}

// The done items' history is one manager's page, never merged (a cursor is one manager's): the one the request names,
// else the default. Its items are stamped with it as a merged list's are, and the rest of the answer (the cursor) is as
// the manager gave it; a manager named but away is 503, an unknown one 404.
func TestInboxHistoryIsOneManagersPage(t *testing.T) {
	h, srv, _ := newBusHub(t)
	connectFakeManager(t, h, srv, assistantwire.AssistantID)
	b := connectFakeManager(t, h, srv, "m-0a0b0c")
	for _, tc := range []struct {
		path   string
		header map[string]string
		want   string
	}{
		{"/api/v1/inbox/done?limit=2", nil, assistantwire.AssistantID},
		{"/api/v1/inbox/done?limit=2", map[string]string{ManagerHeader: "m-0a0b0c"}, "m-0a0b0c"},
		{"/api/v1/inbox/done?manager=m-0a0b0c", nil, "m-0a0b0c"},
	} {
		code, out := ownershipGet(t, h, srv, http.MethodGet, tc.path, tc.header)
		items, _ := out["items"].([]any)
		if code != http.StatusOK || out["who"] != tc.want || out["as_of"] != "t" || len(items) != 1 ||
			items[0].(map[string]any)["manager"] != tc.want || items[0].(map[string]any)["id"] != "item-"+tc.want {
			t.Fatalf("%s %v: %d %v, want %s's page alone", tc.path, tc.header, code, out, tc.want)
		}
	}
	if code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/inbox/done", map[string]string{ManagerHeader: "m-ffffff"}); code != http.StatusNotFound || out["code"] != "no_manager" {
		t.Fatalf("unknown manager: %d %v", code, out)
	}
	_ = b.bus.ws.Close(websocket.StatusNormalClosure, "test")
	eventually(t, 5*time.Second, "m-0a0b0c offline", func() bool { return h.busConnOf("m-0a0b0c") == nil })
	if code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/inbox/done", map[string]string{ManagerHeader: "m-0a0b0c"}); code != http.StatusServiceUnavailable || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("offline manager named: %d %v", code, out)
	}
}

// Acting on an inbox item's suggestion or handing it off goes to the item's manager, as its reply does: the one the
// request names, else the default; the assistant being offline says nothing of another manager's item.
func TestInboxSuggestionGoesToTheItemsManager(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	bm := http.NewServeMux()
	h.busRoutes(bm)
	bsrv := httptest.NewServer(bm)
	t.Cleanup(bsrv.Close)
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	a := connectFakeManager(t, h, bsrv, assistantwire.AssistantID)
	connectFakeManager(t, h, bsrv, "m-0a0b0c")
	eventually(t, 5*time.Second, "both managers connected", func() bool {
		return h.busConnOf(assistantwire.AssistantID) != nil && h.busConnOf("m-0a0b0c") != nil
	})
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	for _, route := range []string{"reply", "suggestion", "handoff"} {
		path := "/api/v1/inbox/item-m-0a0b0c/" + route + "?manager=m-0a0b0c"
		if code, out := ownershipGet(t, h, srv, http.MethodPost, path, nil); code != http.StatusOK || out["who"] != "m-0a0b0c" {
			t.Fatalf("%s: %d %v, want m-0a0b0c's", route, code, out)
		}
	}
	_ = a.bus.ws.Close(websocket.StatusNormalClosure, "test")
	eventually(t, 5*time.Second, "the assistant offline", func() bool { return h.busConnOf(assistantwire.AssistantID) == nil })
	for _, route := range []string{"reply", "suggestion", "handoff"} {
		path := "/api/v1/inbox/item-m-0a0b0c/" + route
		if code, out := ownershipGet(t, h, srv, http.MethodPost, path, map[string]string{ManagerHeader: "m-0a0b0c"}); code != http.StatusOK || out["who"] != "m-0a0b0c" {
			t.Fatalf("%s with the assistant offline: %d %v, want m-0a0b0c's", route, code, out)
		}
	}
}

// ownersStore keeps settings for the moves' tests: the first readFails reads fail, and each write takes a while.
type ownersStore struct {
	managerStore // only Setting, PutSetting and BusIdentityByID are used
	mu           sync.Mutex
	kept         map[string]json.RawMessage
	readFails    int
}

// BusIdentityByID: every "m-…" id is a manager of the roster (a move checks its target is: ownership.go).
func (s *ownersStore) BusIdentityByID(_ context.Context, id string) (*db.BusIdentity, error) {
	if !strings.HasPrefix(id, "m-") {
		return nil, nil
	}
	return &db.BusIdentity{ID: id, Name: id, Role: bus.RoleManager}, nil
}

func (s *ownersStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFails > 0 {
		s.readFails--
		return false, errors.New("database restarting")
	}
	raw, ok := s.kept[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (s *ownersStore) PutSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond) // a write in flight while another move starts
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept[key] = raw
	return nil
}

func (s *ownersStore) owners(t *testing.T) map[string]string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]string
	if err := json.Unmarshal(s.kept[machineOwnersKey], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Moves keep every machine's owner: two moves at once each keep the other's, and a move after the hub could not read
// the owners when it started does not write over the ones it did not read.
func TestMovesKeepTheOtherMachinesOwners(t *testing.T) {
	newHub := func(st *ownersStore) *Hub {
		h := newTestHub(t)
		h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"},
			{ID: "mc", Name: "mc", Kind: "local"}}
		h.bus.idents = st
		return h
	}
	ctx := context.Background()

	st := &ownersStore{kept: map[string]json.RawMessage{}}
	h := newHub(st)
	_ = h.loadMachineOwners(ctx)
	var wg sync.WaitGroup
	for _, mv := range [][2]string{{"ma", "m-0a0a0a"}, {"mc", "m-0c0c0c"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.moveMachine(ctx, mv[0], mv[1]); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := st.owners(t); got["ma"] != "m-0a0a0a" || got["mc"] != "m-0c0c0c" {
		t.Fatalf("two moves at once: kept %v", got)
	}
	if h.entryOwner("ma") != "m-0a0a0a" || h.entryOwner("mc") != "m-0c0c0c" {
		t.Fatalf("two moves at once: owners now %s %s", h.entryOwner("ma"), h.entryOwner("mc"))
	}

	// mb was moved before the hub restarted; the database could not be read as it started.
	st = &ownersStore{kept: map[string]json.RawMessage{machineOwnersKey: json.RawMessage(`{"mb":"m-0b0b0b"}`)}, readFails: 1}
	h = newHub(st)
	_ = h.loadMachineOwners(ctx)
	if _, err := h.moveMachine(ctx, "ma", "m-0a0a0a"); err != nil {
		t.Fatal(err)
	}
	if got := st.owners(t); got["mb"] != "m-0b0b0b" || got["ma"] != "m-0a0a0a" {
		t.Fatalf("a move after an unread start: kept %v", got)
	}
	if h.entryOwner("mb") != "m-0b0b0b" {
		t.Fatalf("mb is %s's now", h.entryOwner("mb"))
	}

	// Still unreadable: the move is refused, nothing is written.
	st = &ownersStore{kept: map[string]json.RawMessage{machineOwnersKey: json.RawMessage(`{"mb":"m-0b0b0b"}`)}, readFails: 2}
	h = newHub(st)
	_ = h.loadMachineOwners(ctx)
	if _, err := h.moveMachine(ctx, "ma", "m-0a0a0a"); err == nil {
		t.Fatal("a move over owners that cannot be read went through")
	}
	if got := st.owners(t); len(got) != 1 || got["mb"] != "m-0b0b0b" {
		t.Fatalf("kept %v", got)
	}
}

// Releasing a removed manager's machines while another machine moves to it: the release takes back every machine it
// carried, and the other move is kept or undone whole, never half.
func TestReleaseKeepsMovesWhole(t *testing.T) {
	h := newTestHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	st := &ownersStore{kept: map[string]json.RawMessage{machineOwnersKey: json.RawMessage(`{"ma":"m-0a0a0a"}`)}}
	h.bus.idents = st
	ctx := context.Background()
	if err := h.loadMachineOwners(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.releaseMachines(ctx, "m-0a0a0a") }()
	go func() {
		defer wg.Done()
		if _, err := h.moveMachine(ctx, "mb", "m-0b0b0b"); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	if got := st.owners(t); got["ma"] != "" || got["mb"] != "m-0b0b0b" {
		t.Fatalf("kept %v", got)
	}
	if h.entryOwner("ma") != devicesOwner || h.entryOwner("mb") != "m-0b0b0b" {
		t.Fatalf("owners now %s %s", h.entryOwner("ma"), h.entryOwner("mb"))
	}
}

// Retained extension lists include installed managers while their bus is offline; ordinary lists still ask only
// live managers. Warm the cache over the real relay before dropping the synthetic managers' bus connections.
func TestMergedExtensionsKeepOfflineManagers(t *testing.T) {
	h, srv, store := newBusHub(t)
	t.Cleanup(func() {
		if err := store.PutSetting(context.Background(), managerDefaultKey, ""); err != nil {
			t.Error(err)
		}
	})
	if err := store.PutSetting(context.Background(), managerDefaultKey, assistantwire.AssistantID); err != nil {
		t.Fatal(err)
	}
	reads := &rosterReads{DB: store}
	h.bus.idents = reads
	const a, b, c, missing = assistantwire.AssistantID, "m-0b0b0b", "m-0c0c0c", "m-0d0d0d"
	connectFakeManager(t, h, srv, a)
	cm := connectFakeManager(t, h, srv, c) // reverse connection order must not change list ordering
	bm := connectFakeManager(t, h, srv, b)
	busToken(t, h, missing, missing) // installed, but has never supplied a retained answer
	check := func(path string, header map[string]string, want ...string) {
		t.Helper()
		code, out := ownershipGet(t, h, srv, http.MethodGet, path, header)
		items, _ := out["extensions"].([]any)
		if code != http.StatusOK || len(items) != len(want) || out["who"] != want[0] {
			t.Fatalf("%s: %d %v, want managers %v", path, code, out, want)
		}
		for i, id := range want {
			item := items[i].(map[string]any)
			if item["manager"] != id || item["id"] != "extension-"+id {
				t.Fatalf("%s item %d: %v, want %s", path, i, item, id)
			}
		}
	}
	check("/api/v1/extensions", nil, a, b, c)
	check("/api/v1/extensions?manager="+b, nil, b)
	for _, m := range []*fakeManager{bm, cm} {
		_ = m.bus.ws.Close(websocket.StatusNormalClosure, "test")
		eventually(t, 5*time.Second, "manager offline", func() bool { return h.busConnOf(m.id) == nil })
	}
	check("/api/v1/extensions", nil, a, b, c)
	check("/api/v1/extensions", map[string]string{ManagerHeader: b}, b)
	check("/api/v1/extensions?manager="+b, nil, b)
	for path, key := range map[string]string{"/api/v1/inbox": "items", "/api/v1/todos": "todos"} {
		code, out := ownershipGet(t, h, srv, http.MethodGet, path, nil)
		items, _ := out[key].([]any)
		if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["manager"] != a {
			t.Fatalf("online-only %s: %d %v", path, code, out)
		}
	}
	if code, _ := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/extensions", map[string]string{ManagerHeader: missing}); code != http.StatusServiceUnavailable {
		t.Fatalf("installed manager without cache: %d", code)
	}
	if err := store.PutSetting(context.Background(), managerDefaultKey, b); err != nil {
		t.Fatal(err)
	}
	h.ownersChanged()
	check("/api/v1/extensions", nil, b, a, c) // an offline default still comes first
	if ok, err := h.revokeBusToken(context.Background(), b); err != nil || !ok {
		t.Fatalf("revoke manager: %v %v", ok, err)
	}
	check("/api/v1/extensions", nil, a, c)
	if code, _ := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/extensions", map[string]string{ManagerHeader: b}); code != http.StatusNotFound {
		t.Fatalf("revoked manager with cached answer: %d", code)
	}
	reads.away.Store(true)
	h.expireOwners() // the roster read a moment ago is not kept any more
	if code, _ := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/extensions", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable installed identities: %d", code)
	}
	reads.away.Store(false)
	check("/api/v1/extensions", nil, a, c)
}

// In external mode even the identity named assistant can be removed. Its retained answer cannot put it back.
func TestMergedExtensionsExcludeRemovedExternalAssistant(t *testing.T) {
	h, srv, store := newBusHub(t)
	t.Cleanup(func() {
		if err := store.PutSetting(context.Background(), managerDefaultKey, ""); err != nil {
			t.Error(err)
		}
	})
	h.cfg.Assistant.External = true
	settings := &slowSetting{managerStore: store}
	h.bus.idents = settings
	const a, b = assistantwire.AssistantID, "m-0b0b0b"
	connectFakeManager(t, h, srv, a)
	connectFakeManager(t, h, srv, b)
	if err := store.PutSetting(context.Background(), managerDefaultKey, b); err != nil {
		t.Fatal(err)
	}
	h.ownersChanged()
	code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/extensions", nil)
	if code != http.StatusOK || len(out["extensions"].([]any)) != 2 || out["who"] != b {
		t.Fatalf("warm external managers: %d %v", code, out)
	}
	if ok, err := h.revokeBusToken(context.Background(), a); err != nil || !ok {
		t.Fatalf("revoke external assistant: %v %v", ok, err)
	}
	eventually(t, 5*time.Second, "external assistant disconnected", func() bool { return h.busConnOf(a) == nil })
	code, out = ownershipGet(t, h, srv, http.MethodGet, "/api/v1/extensions", nil)
	items, _ := out["extensions"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["manager"] != b {
		t.Fatalf("removed external assistant still cached: %d %v", code, out)
	}
	settings.fail.Store(true)
	h.expireOwners() // the default read a moment ago is not kept any more
	if code, _ := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/extensions", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable default setting: %d", code)
	}
	settings.fail.Store(false)
	if ok, err := h.revokeBusToken(context.Background(), b); err != nil || !ok {
		t.Fatalf("revoke last external manager: %v %v", ok, err)
	}
	eventually(t, 5*time.Second, "last external manager disconnected", func() bool { return h.busConnOf(b) == nil })
	if code, out := ownershipGet(t, h, srv, http.MethodGet, "/api/v1/extensions", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("empty roster with cached answers: %d %v", code, out)
	}
}
