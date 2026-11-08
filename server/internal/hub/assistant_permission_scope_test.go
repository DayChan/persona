package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
)

// Drive the public HTTP route and real WebSocket bus with separate manager services
// and serialized stores. No database or production process is needed.
func TestAssistantDefaultPermissionsThroughManagerRouting(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			if err := c.ws.CloseNow(); err != nil {
				t.Logf("closing test bus: %v", err)
			}
		}
	})
	ids := []string{"assistant", "m-example"}
	modes := []string{"plan", "bypassPermissions"}
	permissionReloads := map[string]func(){}
	for _, id := range ids {
		st := &settingsStore{vals: map[string][]byte{}}
		var mu sync.RWMutex
		var svc *assistant.Service
		reload := func() {
			mu.Lock()
			defer mu.Unlock()
			svc = assistant.New(assistant.Options{ID: id, Hub: permissionTestHub{assistantLink{h}}, Store: func() assistant.Store { return st }})
		}
		reload()
		connectGuardManager(t, h, srv.URL, id, func(method string, params json.RawMessage) (any, error) {
			if method == extwire.MethodAuthorize {
				return extwire.Authorized{OK: id == "m-example", Extension: "testext"}, nil
			}
			if method != assistantwire.MethodHTTP {
				return map[string]any{}, nil
			}
			var p assistantwire.HTTPRequest
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			r := httptest.NewRequest(p.Method, p.Path+"?"+p.Query, bytes.NewReader(p.Body))
			r.Header = http.Header(p.Header)
			w := httptest.NewRecorder()
			mu.RLock()
			svc.ServeHTTP(w, r)
			mu.RUnlock()
			return assistantwire.HTTPResponse{Status: w.Code, Header: w.Header(), Body: w.Body.Bytes()}, nil
		})
		// A cache-free service is installed after the first pass below.
		permissionReloads[id] = reload
	}
	do := func(method, query, owner, token, body string) (int, map[string]any) {
		t.Helper()
		r, err := http.NewRequest(method, srv.URL+"/api/v1/assistant"+query, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		if owner != "" {
			r.Header.Set(ManagerHeader, owner)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, out
	}
	for i, id := range ids {
		code, out := do("PUT", "", id, h.cfg.Token, `{"default_permission_mode":"`+modes[i]+`"}`)
		if code != 200 || out["manager"] != id || out["default_permission_mode"] != modes[i] {
			t.Fatalf("save %s: %d %v", id, code, out)
		}
	}
	for pass := 0; pass < 2; pass++ {
		for i, id := range ids {
			code, out := do("GET", "?manager="+id, "", h.cfg.Token, "")
			if code != 200 || out["manager"] != id || out["default_permission_mode"] != modes[i] {
				t.Fatalf("pass %d read %s: %d %v", pass, id, code, out)
			}
			contract, ok := out["permission_defaults"].(map[string]any)
			if !ok || contract["manager"] != id || contract["version"] != float64(1) || len(contract["modes"].([]any)) != 5 {
				t.Fatalf("manager capability was lost or changed by hub: %v", out)
			}
			permissionReloads[id]()
		}
	}
	// A newer hub must not manufacture a capability for a connected older manager.
	connectGuardManager(t, h, srv.URL, "m-legacy", func(method string, params json.RawMessage) (any, error) {
		if method != assistantwire.MethodHTTP {
			return map[string]any{}, nil
		}
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: json.RawMessage(`{"manager":"m-legacy","default_permission_mode":"plan"}`)}, nil
	})
	if code, out := do("GET", "", "m-legacy", h.cfg.Token, ""); code != http.StatusOK || out["permission_defaults"] != nil {
		t.Fatalf("hub synthesized old manager capability: %d %v", code, out)
	}
	// A change to the roster's default cannot redirect a delayed request with a captured explicit owner.
	h.owners.mu.Lock()
	h.owners.def, h.owners.until = "m-example", time.Now().Add(time.Minute)
	h.owners.mu.Unlock()
	if code, out := do("PUT", "?manager=assistant", "", h.cfg.Token, `{"default_permission_mode":"default"}`); code != 200 || out["manager"] != "assistant" {
		t.Fatalf("explicit owner followed default: %d %v", code, out)
	}
	for _, tc := range []struct {
		query, owner, token, body string
		code                      int
	}{
		{"", "", h.cfg.Token, `{"default_permission_mode":"plan","main":"-"}`, 400},
		{"", "m-missing", h.cfg.Token, `{"default_permission_mode":"plan"}`, 404},
		{"?manager=m-example", "assistant", h.cfg.Token, `{"default_permission_mode":"plan"}`, 400},
		{"", "m-example", h.cfg.Token, `{"default_permission_mode":"invalid","main":"-"}`, 400},
		{"", "assistant", extTokenPrefix + "test-token", `{"default_permission_mode":"plan"}`, 403},
	} {
		if code, out := do("PUT", tc.query, tc.owner, tc.token, tc.body); code != tc.code {
			t.Fatalf("refusal %+v: %d %v", tc, code, out)
		}
	}
	if code, out := do("GET", "", "m-example", h.cfg.Token, ""); code != 200 || out["default_permission_mode"] != "bypassPermissions" {
		t.Fatalf("other manager changed: %d %v", code, out)
	}
}

// These notifications are unrelated to preference routing in this isolated harness.
type permissionTestHub struct{ assistantLink }

func (permissionTestHub) InboxChanged(context.Context, assistantwire.InboxChanged) {}
func (permissionTestHub) Tell(string, any) error                                   { return nil }
