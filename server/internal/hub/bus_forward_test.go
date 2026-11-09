package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exercise authentication and the bus codec with in-memory tokens and an RPC pipe, without Postgres.
func TestBusForwardsTrustedDeviceActor(t *testing.T) {
	h := newTestHub(t)
	const token = "test-device-token"
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{
		hashToken(token): {ID: "device-1", Name: "Trusted device", Role: roleFull},
	}, rev: map[string]bool{}}
	hubIn, assistantOut := io.Pipe()
	assistantIn, hubOut := io.Pipe()
	hp, ap := rpc.New("h", hubOut), rpc.New("a", assistantOut)
	got := make(chan assistantwire.HTTPRequest, 1)
	ap.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP {
			return nil, errors.New("unexpected " + method)
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		got <- req
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go hp.Serve(ctx, hubIn)
	go ap.Serve(ctx, assistantIn)
	t.Cleanup(func() {
		cancel()
		hp.Close(io.EOF)
		ap.Close(io.EOF)
		_ = hubIn.Close()
		_ = assistantIn.Close()
		_ = hubOut.Close()
		_ = assistantOut.Close()
	})
	h.bus.current = map[string]*busConn{busAssistant: {peer: hp}}
	routes := h.routes()
	for _, tt := range []struct{ name, forged string }{{"device name", ""}, {"forged header", "Forged device"}} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/model-profiles/economy/apply", strings.NewReader(`{}`))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Cookie", "test-cookie=private")
			r.Header.Set(assistantwire.RoleHeader, roleAnswer)
			r.Header.Set(assistantwire.ExtensionHeader, "forged-extension")
			r.Header.Set(assistantwire.AdminHeader, "1")
			if tt.forged != "" {
				r.Header.Set(assistantwire.DeviceNameHeader, tt.forged)
				r.Header.Add(assistantwire.DeviceNameHeader, "Another forged device")
			}
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
				t.Fatalf("forward: %d %s", w.Code, w.Body)
			}
			select {
			case req := <-got:
				if req.Method != http.MethodPost || req.Path != r.URL.Path || string(req.Body) != `{}` {
					t.Fatalf("forwarded request: %+v", req)
				}
				for name, want := range map[string]string{assistantwire.DeviceNameHeader: "Trusted device", assistantwire.RoleHeader: roleFull} {
					if values := req.Header[name]; len(values) != 1 || values[0] != want {
						t.Errorf("forwarded %s=%v want [%s]", name, values, want)
					}
				}
				for _, name := range []string{"Authorization", "Cookie", assistantwire.ExtensionHeader, assistantwire.AdminHeader} {
					if _, ok := req.Header[name]; ok {
						t.Errorf("forwarded untrusted or private header %s", name)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no request reached the assistant")
			}
		})
	}
}

// The assistant's routes (assistantwire.Routes) are its to answer, unchanged; the hub's own stay the hub's; while the
// assistant is not connected they answer 503 assistant_offline.
func TestBusForwardsTheAssistantRoutes(t *testing.T) {
	h, srv, _ := newBusHub(t)
	got := make(chan assistantwire.HTTPRequest, 4)
	r := helloAssistant(t, h, srv, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == assistantwire.MethodTools {
			return map[string]any{"tools": []any{}}, nil
		}
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP || json.Unmarshal(params, &req) != nil {
			return nil, errors.New("unexpected " + method)
		}
		got <- req
		if req.Path == "/api/v1/assistant/runs" {
			return nil, &rpc.Error{Code: "boom", Message: "it failed"}
		}
		return assistantwire.HTTPResponse{Status: 201, Header: map[string][]string{"Content-Type": {"application/json"}, "X-Test": {"1"}},
			Body: []byte(`{"answered":"by the assistant"}`)}, nil
	})
	do := func(method, path, body string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set(assistantwire.RoleHeader, "forged")
		req.Header.Set(assistantwire.ExtensionHeader, "forged")
		req.Header.Set(assistantwire.DeviceNameHeader, "forged")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := do("POST", "/api/v1/assistant/conversations/c1/route?x=1", `{"text":"你好"}`)
	if resp.StatusCode != 201 || body != `{"answered":"by the assistant"}` || resp.Header.Get("X-Test") != "1" {
		t.Fatalf("forwarded: %d %s %v", resp.StatusCode, body, resp.Header)
	}
	req := <-got
	if req.Method != "POST" || req.Path != "/api/v1/assistant/conversations/c1/route" || req.Query != "x=1" || string(req.Body) != `{"text":"你好"}` ||
		req.Header[assistantwire.RoleHeader][0] != roleFull || req.Header["Authorization"] != nil || req.Header[assistantwire.ExtensionHeader] != nil ||
		req.Header[assistantwire.DeviceNameHeader] != nil { // the hub's own token names no device
		t.Fatalf("the assistant got %+v", req)
	}
	if resp, body := do("GET", "/api/v1/assistant/runs", ""); resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "boom") {
		t.Fatalf("the assistant's error: %d %s", resp.StatusCode, body)
	}
	<-got
	for _, c := range [][2]string{{"POST", "/api/v1/task-plans/p1/start"}, {"GET", "/api/v1/task-trust"},
		{"DELETE", "/api/v1/task-trust"}, {"POST", "/api/v1/todos/t1/done-check"}, {"GET", "/api/v1/todos"},
		{"PUT", "/api/v1/todos/t1/spaces/m1/group/alpha"}, {"GET", "/api/v1/assistant"}, {"PUT", "/api/v1/assistant"},
		{"POST", "/api/v1/assistant/conversations"}, {"POST", "/api/v1/assistant/main/new"},
		{"DELETE", "/api/v1/assistant/memory/note.md"}} {
		if resp, _ := do(c[0], c[1], ""); resp.StatusCode != 201 {
			t.Fatalf("%s %s: %d", c[0], c[1], resp.StatusCode)
		}
		if req := <-got; req.Method != c[0] || req.Path != c[1] {
			t.Fatalf("%s %s reached the assistant as %s %s", c[0], c[1], req.Method, req.Path)
		}
	}
	// The hub's own routes stay the hub's.
	for _, c := range [][2]string{{"GET", "/api/v1/skills"}, {"GET", "/api/v1/machines"},
		{"GET", "/api/v1/todosx"}, {"GET", "/api/v1/assistantx"}} {
		do(c[0], c[1], "")
	}
	select {
	case req := <-got:
		t.Fatalf("forwarded %s %s", req.Method, req.Path)
	default:
	}
	// Not connected: 503 assistant_offline, the hub's own routes as before.
	_ = r.ws.CloseNow()
	eventually(t, 5*time.Second, "the assistant gone", func() bool { return h.busConnOf(busAssistant) == nil })
	resp, body = do("GET", "/api/v1/assistant/tasks", "")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"assistant_offline"`) {
		t.Fatalf("offline: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do("GET", "/api/v1/machines", ""); resp.StatusCode != 200 {
		t.Fatalf("the hub's own: %d", resp.StatusCode)
	}
}

// A manager is third-party code over the bus: an answer whose status is not a final HTTP status (outside 200-599)
// reaches the app as a 502 with a JSON error and none of the manager's headers or body, where net/http would panic or
// leave the connection unusable. Valid statuses pass through unchanged.
func TestBusForwardRefusesAnImpossibleStatus(t *testing.T) {
	h, srv, _ := newBusHub(t)
	logs := &syncBuffer{}
	h.logger.SetOutput(logs)
	helloAssistant(t, h, srv, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == assistantwire.MethodTools {
			return map[string]any{"tools": []any{}}, nil
		}
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP || json.Unmarshal(params, &req) != nil {
			return nil, errors.New("unexpected " + method)
		}
		q, _ := url.ParseQuery(req.Query)
		status, _ := strconv.Atoi(q.Get("s"))
		res := assistantwire.HTTPResponse{Status: status, Header: map[string][]string{"X-Manager": {"1"}}}
		if status != http.StatusNoContent {
			res.Body = []byte(`{"answered":true}`)
		}
		return res, nil
	})
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	do := func(t *testing.T, path string, status int) (*http.Response, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // a regression must not hang the run
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path+"?s="+strconv.Itoa(status), nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("status %d: reading the body: %v", status, err)
		}
		return resp, string(b)
	}
	// One route the hub passes on, one whose answers it merges over the managers (mergedLists).
	for _, path := range []string{"/api/v1/assistant/runs", "/api/v1/todos"} {
		for _, s := range []int{42, 99, 101, 600, 1000} {
			t.Run(fmt.Sprintf("%s refused %d", path, s), func(t *testing.T) {
				resp, body := do(t, path, s)
				var e struct{ Error, Code, Detail string } // the status in error, or in detail under a localized error
				if resp.StatusCode != http.StatusBadGateway || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") ||
					json.Unmarshal([]byte(body), &e) != nil || !strings.Contains(e.Error+" "+e.Detail, strconv.Itoa(s)) || e.Code == "" {
					t.Fatalf("got %d %v %s, want 502 with a JSON error naming %d", resp.StatusCode, resp.Header, body, s)
				}
				if resp.Header.Get("X-Manager") != "" || strings.Contains(body, `"answered":true`) {
					t.Errorf("the manager's answer was passed on: %v %s", resp.Header, body)
				}
				if want := fmt.Sprintf("bus: [%s] http.request GET %s: answered HTTP status %d", busAssistant, path, s); !logs.WaitFor(want, 5*time.Second) {
					t.Errorf("no log %q in %q", want, logs.String())
				}
			})
		}
	}
	for _, c := range [][2]int{{0, http.StatusOK}, {http.StatusCreated, http.StatusCreated}, {http.StatusNoContent, http.StatusNoContent},
		{http.StatusNotFound, http.StatusNotFound}} {
		t.Run(fmt.Sprintf("passed %d", c[0]), func(t *testing.T) {
			resp, body := do(t, "/api/v1/assistant/runs", c[0])
			want := `{"answered":true}`
			if c[1] == http.StatusNoContent {
				want = ""
			}
			if resp.StatusCode != c[1] || body != want || resp.Header.Get("X-Manager") != "1" {
				t.Fatalf("got %d %v %q, want %d %q", resp.StatusCode, resp.Header, body, c[1], want)
			}
		})
	}
}

// What the assistant sends the apps (ws.broadcast) reaches them unchanged; a message without a type does not.
func TestBusBroadcast(t *testing.T) {
	h, srv, _ := newBusHub(t)
	app := wsApp(t, h)
	r := helloAssistant(t, h, srv, echoService)
	_ = r.peer.Notify("ws.broadcast", map[string]any{"message": map[string]any{"oops": true}})
	msg := json.RawMessage(`{"type":"task_plan","plan":{"id":"p1","steps":["一","二"]}}`)
	_ = r.peer.Notify("ws.broadcast", map[string]any{"message": msg})
	select {
	case raw := <-app.out:
		if !jsonEqual(withoutSeq(raw), withoutSeq(msg)) {
			t.Fatalf("the apps got %s, want %s (and the hub's event_seq)", raw, msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the apps")
	}
}

// wsApp is an app connected to the hub's WebSocket.
func wsApp(t *testing.T, h *Hub) *client {
	t.Helper()
	c := &client{device: "test-device", out: make(chan []byte, 64)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out // client_hello_ack
	return c
}

// helloAssistant connects a fake assistant answering the hub's calls with handle, past its handshake.
func helloAssistant(t *testing.T, h *Hub, srv *httptest.Server, handle rpc.Handler) *rawBus {
	t.Helper()
	tok := busToken(t, h, busAssistant, "助手")
	r := dialRawBusWith(t, srv, tok, handle)
	if err := r.call("manager.hello", map[string]any{"id": busAssistant}, nil); err != nil {
		t.Fatal(err)
	}
	return r
}

// withoutSeq is a message the apps got without the event_seq the hub numbers every message with.
func withoutSeq(b []byte) json.RawMessage {
	var m map[string]any // at every depth: the hub passes a manager's message on as it read it (keys in order)
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	delete(m, "event_seq")
	out, _ := json.Marshal(m)
	return out
}
