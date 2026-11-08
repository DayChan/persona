package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// startRequest posts a new conversation's start (its first message), with op as X-Persona-Op-Id when set.
func startRequest(t *testing.T, ctx context.Context, handler http.Handler, token, op string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://hub.example.com/api/v1/assistant/conversations",
		strings.NewReader(`{"prompt":"Plan a weekend trip.","main":true}`))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	if op != "" {
		r.Header.Set("X-Persona-Op-Id", op)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response %d: %s", w.Code, w.Body.String())
	}
	return w.Code, out
}

func startedSession(id string) assistantwire.HTTPResponse {
	return assistantwire.HTTPResponse{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}},
		Body: []byte(`{"session":{"machine_id":"m1","session_id":"` + id + `"}}`)}
}

func receiptStates(s *receiptStore) map[string]string {
	s.ops.mu.Lock()
	defer s.ops.mu.Unlock()
	states := map[string]string{}
	for id, op := range s.ops.ops {
		states[id] = op.State
	}
	return states
}

// #1349: the same op id starts one conversation, however often it is asked, and gets the first answer back; a caller
// that went away before the answer still has its start finished and recorded.
func TestAssistantStartReceipt(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	manager, _ := connectInboxManager(t, h)
	var starts atomic.Int32
	arrived, finish := make(chan struct{}, 1), make(chan struct{})
	manager.mu.Lock()
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		if r.Path != "/api/v1/assistant/conversations" {
			return assistantwire.HTTPResponse{Status: 404, Body: []byte(`{}`)}, nil
		}
		n := starts.Add(1)
		if n == 1 {
			arrived <- struct{}{}
			<-finish
		}
		return startedSession("s" + string(rune('0'+n))), nil
	}
	manager.mu.Unlock()
	mux := h.routes()
	const op = "00000000-0000-4000-8000-000000001349"

	// The first: the app goes away while the assistant starts it.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://hub.example.com/api/v1/assistant/conversations",
			strings.NewReader(`{"prompt":"Plan a weekend trip.","main":true}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		r.Header.Set("X-Persona-Op-Id", op)
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}()
	<-arrived
	cancel()
	close(finish)
	<-done
	if got := receiptStates(store); got[op] != "done" {
		t.Fatalf("receipt after the caller went away = %v", got)
	}

	// Asked again: the first answer, nothing started.
	for range 2 {
		code, out := startRequest(t, context.Background(), mux, h.cfg.Token, op)
		session, _ := out["session"].(map[string]any)
		if code != 200 || session["session_id"] != "s1" {
			t.Fatalf("repeat: %d %v", code, out)
		}
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("started %d times", n)
	}

	// Without an op id nothing changes: each request starts one.
	for range 2 {
		if code, out := startRequest(t, context.Background(), mux, h.cfg.Token, ""); code != 200 {
			t.Fatalf("without op id: %d %v", code, out)
		}
	}
	if n := starts.Load(); n != 3 {
		t.Fatalf("without op id: started %d times in all", n)
	}
}

// Offline nothing reached the assistant: no receipt, and the same op id starts it once the assistant is back.
func TestAssistantStartOfflineKeepsNoReceipt(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	mux := h.routes()
	const op = "00000000-0000-4000-8000-000000001350"
	if code, out := startRequest(t, context.Background(), mux, h.cfg.Token, op); code != 503 || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("offline: %d %v", code, out)
	}
	if got := receiptStates(store); len(got) != 0 {
		t.Fatalf("offline kept a receipt: %v", got)
	}
	manager, _ := connectInboxManager(t, h)
	var starts atomic.Int32
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		starts.Add(1)
		return startedSession("s1"), nil
	}
	manager.mu.Unlock()
	for range 2 {
		if code, out := startRequest(t, context.Background(), mux, h.cfg.Token, op); code != 200 {
			t.Fatalf("back online: %d %v", code, out)
		}
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("started %d times", n)
	}
}

// The link dropping while the assistant has the start: it may have started, so the app is told so and the receipt
// stays unknown; the same op id never starts it again.
func TestAssistantStartCutOffStaysUnknown(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	manager, link := connectInboxManager(t, h)
	got := make(chan struct{}, 1)
	var starts atomic.Int32
	manager.mu.Lock()
	manager.http = func(ctx context.Context, _ assistantwire.HTTPRequest) (any, error) {
		starts.Add(1)
		got <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	manager.mu.Unlock()
	go func() {
		<-got
		_ = link.ws.CloseNow()
	}()
	mux := h.routes()
	const op = "00000000-0000-4000-8000-000000001351"
	if code, out := startRequest(t, context.Background(), mux, h.cfg.Token, op); code != 502 || out["code"] != assistantwire.CodeAssistantInDoubt || out["delivered"] != "unknown" {
		t.Fatalf("cut off: %d %v", code, out)
	}
	if got := receiptStates(store); got[op] != "unknown" {
		t.Fatalf("receipt = %v", got)
	}
	connectInboxManager(t, h)
	if code, out := startRequest(t, context.Background(), mux, h.cfg.Token, op); code != 502 || out["code"] != assistantwire.CodeAssistantInDoubt {
		t.Fatalf("asked again: %d %v", code, out)
	}
	if n := starts.Load(); n != 1 {
		t.Fatalf("started %d times", n)
	}
}
