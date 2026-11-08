package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// The hub's part of the inbox, which is the manager's (the assistant's items, the extensions' sources): it relays the
// inbox's routes (the watch held to text replies, a reply's receipt, a reply cut off in doubt), answers the manager's
// model calls within the time it waits, and keeps from the snapshot which conversations are the AI twin's.

// The inbox's routes while the manager is away: 503 assistant_offline.
func TestInboxRoutesWithoutTheManager(t *testing.T) {
	h := newTestHub(t)
	mux := h.routes()
	for _, path := range []string{"/api/v1/inbox", "/api/v1/inbox/x", "/api/v1/inbox/mutes"} {
		if code, out := call(t, mux, "GET", path, h.cfg.Token, ""); code != http.StatusServiceUnavailable || out["code"] != "assistant_offline" {
			t.Fatalf("%s without the manager: %d %v", path, code, out)
		}
	}
	for _, path := range []string{"/api/v1/inbox/refresh", "/api/v1/inbox/x/reply", "/api/v1/inbox/x/suggestion", "/api/v1/inbox/x/handoff", "/api/v1/inbox/done", "/api/v1/translate"} {
		if code, out := call(t, mux, "POST", path, h.cfg.Token, `{"text":"好的"}`); code != http.StatusServiceUnavailable || out["code"] != "assistant_offline" {
			t.Fatalf("%s without the manager: %d %v", path, code, out)
		}
	}
}

// inboxFakeManager is the manager on the bus for the hub's tests: it answers the inbox's calls from what it keeps and
// records what it was asked.
type inboxFakeManager struct {
	mu        sync.Mutex
	http      func(ctx context.Context, r assistantwire.HTTPRequest) (any, error) // http.request, when set
	rounds    []assistantwire.InboxRound
	items     map[string]json.RawMessage
	forgotten []string
	repos     map[string][]string
	asked     []string
}

// connectInboxManager connects a fake manager to h's bus (identities in memory without a database).
func connectInboxManager(t *testing.T, h *Hub) (*inboxFakeManager, *rawBus) {
	t.Helper()
	if h.busStore() == nil {
		h.bus.idents = &memIdents{}
	}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m := &inboxFakeManager{items: map[string]json.RawMessage{}, repos: map[string][]string{}}
	r := helloAssistant(t, h, srv, m.handle)
	eventually(t, 5*time.Second, "the manager connected", func() bool { return h.busConnOf(assistantwire.AssistantID) != nil })
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	return m, r
}

func (m *inboxFakeManager) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	m.mu.Lock()
	if serve := m.http; method == assistantwire.MethodHTTP && serve != nil {
		m.mu.Unlock()
		var r assistantwire.HTTPRequest
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, err
		}
		return serve(ctx, r)
	}
	defer m.mu.Unlock()
	m.asked = append(m.asked, method)
	switch method {
	case assistantwire.MethodTools:
		return map[string]any{"tools": []any{}}, nil
	case assistantwire.MethodInboxCollected:
		var r assistantwire.InboxRound
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, err
		}
		m.rounds = append(m.rounds, r)
		return map[string]any{}, nil
	case assistantwire.MethodInboxItem:
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(params, &a)
		raw, ok := m.items[a.ID]
		return assistantwire.InboxItemResult{Item: raw, OK: ok}, nil
	case assistantwire.MethodInboxForget:
		var a struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal(params, &a)
		m.forgotten = append(m.forgotten, a.Source)
		return map[string]any{"deleted": 1}, nil
	case assistantwire.MethodInboxRepoMutes:
		var a assistantwire.InboxRepoMutes
		_ = json.Unmarshal(params, &a)
		if a.Set {
			if slices.ContainsFunc(a.Repos, func(r string) bool { return !strings.Contains(r, "/") }) {
				return nil, errors.New("not owner/name")
			}
			if a.Add {
				m.repos[a.Source] = append(m.repos[a.Source], a.Repos...)
			} else {
				m.repos[a.Source] = a.Repos
			}
		}
		return map[string]any{"repos": m.repos[a.Source]}, nil
	}
	return map[string]any{}, nil
}

// An answer-only device (the watch) replies with text only, the hub holding it to that before the manager sees the
// request: no documents' status moves, no "/agent …".
func TestInboxReplyFromTheWatchIsTextOnly(t *testing.T) {
	h := newTestHub(t)
	fm, _ := connectInboxManager(t, h)
	var relayed atomic.Int32
	fm.mu.Lock()
	fm.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		relayed.Add(1)
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"id":"x"}`)}, nil
	}
	fm.mu.Unlock()
	api := http.NewServeMux()
	h.inboxRoutesRelayed(api)
	reply := func(body string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/inbox/x/reply", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleAnswer}))
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w.Code
	}
	for _, body := range []string{`{"text":"/agent 看看构建"}`, `{"text":"好","changes":[{"ref":"r1","field":"状态","value":"完成"}]}`} {
		if code := reply(body); code != http.StatusForbidden || relayed.Load() != 0 {
			t.Fatalf("%s from the watch: %d, %d relayed", body, code, relayed.Load())
		}
	}
	if code := reply(`{"text":"好的"}`); code != http.StatusOK || relayed.Load() != 1 {
		t.Fatalf("a text reply from the watch: %d, %d relayed", code, relayed.Load())
	}
}

// receiptStore keeps the apps' operation receipts (client_ops) and drops one when asked (ForgetClientOp).
type receiptStore struct {
	*memStore
	ops *clientOpsStore
}

func (s *receiptStore) BeginClientOp(ctx context.Context, op db.ClientOp) (*db.ClientOp, bool, error) {
	return s.ops.BeginClientOp(ctx, op)
}
func (s *receiptStore) ClientOp(ctx context.Context, id string) (*db.ClientOp, error) {
	return s.ops.ClientOp(ctx, id)
}
func (s *receiptStore) FinishClientOp(ctx context.Context, id, state string, result json.RawMessage) error {
	return s.ops.FinishClientOp(ctx, id, state, result)
}
func (s *receiptStore) ForgetClientOp(_ context.Context, id string) error {
	s.ops.mu.Lock()
	defer s.ops.mu.Unlock()
	delete(s.ops.ops, id)
	return nil
}

// The link to the manager dropping while a reply is with it: the reply may have gone out, so the app is told so
// (assistant_in_doubt, delivered unknown), not that the assistant is offline; the receipt is not kept, so the same
// reply asked again reaches the manager (whose send id has the hub send it once) instead of the stored answer.
func TestInboxReplyCutOffIsInDoubt(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	fm, link := connectInboxManager(t, h)
	got := make(chan struct{}, 1)
	fm.mu.Lock()
	fm.http = func(ctx context.Context, r assistantwire.HTTPRequest) (any, error) {
		got <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	fm.mu.Unlock()
	go func() {
		<-got
		_ = link.ws.CloseNow()
	}()
	mux := h.routes()
	body := `{"text":"好的","client_id":"c1"}`
	code, out := call(t, mux, "POST", "/api/v1/inbox/x/reply", h.cfg.Token, body)
	if code != http.StatusBadGateway || out["code"] != assistantwire.CodeAssistantInDoubt || out["delivered"] != "unknown" {
		t.Fatalf("a reply cut off: %d %v", code, out)
	}
	store.ops.mu.Lock()
	kept := len(store.ops.ops)
	store.ops.mu.Unlock()
	if kept != 0 {
		t.Fatal("the receipt of a reply in doubt was kept")
	}
	fm2, _ := connectInboxManager(t, h)
	fm2.mu.Lock()
	fm2.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"id":"x","done_by":"replied"}`)}, nil
	}
	fm2.mu.Unlock()
	if code, out := call(t, mux, "POST", "/api/v1/inbox/x/reply", h.cfg.Token, body); code != http.StatusOK || out["done_by"] != "replied" {
		t.Fatalf("the same reply asked again: %d %v", code, out)
	}
	// Answered: the receipt is kept now, and a repeat gets the answer without the manager.
	fm2.mu.Lock()
	fm2.http = func(context.Context, assistantwire.HTTPRequest) (any, error) { return nil, errors.New("asked twice") }
	fm2.mu.Unlock()
	if code, out := call(t, mux, "POST", "/api/v1/inbox/x/reply", h.cfg.Token, body); code != http.StatusOK || out["done_by"] != "replied" {
		t.Fatalf("a repeat after the answer: %d %v", code, out)
	}
}

// A hand-off keeps its receipt by the body's client_id, as a reply does: cut off on the way it is in doubt and keeps
// none, so asked again it reaches the manager (whose session.send_once drops a repeat); answered, the same request
// again gets the answer without the manager, and another client_id is another hand-off.
func TestInboxHandoffReceipt(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	fm, link := connectInboxManager(t, h)
	got := make(chan struct{}, 1)
	fm.mu.Lock()
	fm.http = func(ctx context.Context, r assistantwire.HTTPRequest) (any, error) {
		got <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	fm.mu.Unlock()
	go func() {
		<-got
		_ = link.ws.CloseNow()
	}()
	mux := h.routes()
	body := `{"text":"看下这件事","client_id":"6f1c2b9e-4d3a-4c5b-8e7f-0a1b2c3d4e5f"}`
	code, out := call(t, mux, "POST", "/api/v1/inbox/x/handoff", h.cfg.Token, body)
	if code != http.StatusBadGateway || out["code"] != assistantwire.CodeAssistantInDoubt || out["delivered"] != "unknown" {
		t.Fatalf("a hand-off cut off: %d %v", code, out)
	}
	store.ops.mu.Lock()
	kept := len(store.ops.ops)
	store.ops.mu.Unlock()
	if kept != 0 {
		t.Fatal("the receipt of a hand-off in doubt was kept")
	}
	fm2, _ := connectInboxManager(t, h)
	var asked atomic.Int32
	fm2.mu.Lock()
	fm2.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		asked.Add(1)
		if r.Method != http.MethodPost || r.Path != "/api/v1/inbox/x/handoff" {
			return nil, errors.New("asked " + r.Method + " " + r.Path)
		}
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"item":{"id":"x"},"session":{"machine_id":"server","session_id":"m1"}}`)}, nil
	}
	fm2.mu.Unlock()
	for i := range 2 {
		code, out := call(t, mux, "POST", "/api/v1/inbox/x/handoff", h.cfg.Token, body)
		if session, _ := out["session"].(map[string]any); code != http.StatusOK || session["session_id"] != "m1" || asked.Load() != 1 {
			t.Fatalf("hand-off %d: %d %v, the manager asked %d times", i+1, code, out, asked.Load())
		}
	}
	other := `{"text":"看下这件事","client_id":"0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"}`
	if code, _ := call(t, mux, "POST", "/api/v1/inbox/x/handoff", h.cfg.Token, other); code != http.StatusOK || asked.Load() != 2 {
		t.Fatalf("another client id: %d, the manager asked %d times", code, asked.Load())
	}
}
