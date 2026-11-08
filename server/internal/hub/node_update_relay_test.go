package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestNodeUpdateRelayThroughExistingManagerCall(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}}
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	var mu sync.Mutex
	info := proto.NodeInfo{MachineUID: "uid-a", Executable: "/opt/persona/bin/personad", OS: "darwin", Arch: "arm64", NodeUpdate: true, Capabilities: []string{nodeproto.NodeUpdate}}
	var methods []string
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, raw json.RawMessage) (any, error) {
		if method != devicewire.MethodCall {
			return map[string]any{}, nil
		}
		var call devicewire.NodeCall
		if err := json.Unmarshal(raw, &call); err != nil {
			return nil, err
		}
		if call.Method != "node.info" && !strings.HasPrefix(call.Method, "node.update.") {
			return map[string]any{}, nil
		}
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, call.Method)
		if call.Method == "node.info" {
			return info, nil
		}
		return map[string]any{"accepted": true}, nil
	})
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}, Connected: true})
	eventually(t, 5*time.Second, "ma", func() bool { return h.localMachine("ma") != nil && h.localMachine("ma").isOnline() })
	request := func(role, action, body string) int {
		r := httptest.NewRequest("POST", "/api/v1/machines/ma/node-update/"+action, strings.NewReader(body))
		r.SetPathValue("mid", "ma")
		r.SetPathValue("action", action)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: role}))
		w := httptest.NewRecorder()
		h.nodeUpdateRelay(w, r)
		return w.Code
	}
	body := `{"manager_id":"assistant","machine_uid":"uid-a","executable":"/opt/persona/bin/personad","os":"darwin","arch":"arm64","params":{"id":"tx"}}`
	for _, role := range []string{roleAnswer, roleExtension, roleLink} {
		if got := request(role, "commit", body); got != http.StatusForbidden {
			t.Fatalf("role %s: %d", role, got)
		}
	}
	if got := request(roleFull, "terminal.exec", body); got != http.StatusNotFound {
		t.Fatal(got)
	}
	mu.Lock()
	if len(methods) != 0 {
		t.Fatalf("rejected requests reached node: %v", methods)
	}
	mu.Unlock()
	if got := request(roleFull, "commit", strings.Replace(body, "assistant", "m-b", 1)); got != http.StatusConflict {
		t.Fatalf("foreign owner %d", got)
	}
	if got := request(roleFull, "commit", strings.Replace(body, "uid-a", "uid-b", 1)); got != http.StatusConflict {
		t.Fatalf("foreign UID %d", got)
	}
	mu.Lock()
	if len(methods) != 1 || methods[0] != "node.info" {
		t.Fatalf("foreign UID reached installer %v", methods)
	}
	mu.Unlock()
	if got := request(roleFull, "commit", body); got != http.StatusOK {
		t.Fatalf("existing devices.call %d", got)
	}
	mu.Lock()
	if methods[len(methods)-1] != "node.update.commit" {
		t.Fatal(methods)
	}
	info.NodeUpdate = false
	mu.Unlock()
	if got := request(roleFull, "commit", body); got != http.StatusConflict {
		t.Fatalf("unsupported node %d", got)
	}
}

func TestNodeUpdateRelayInvalidRequestsNeverReachNode(t *testing.T) {
	tn := newTermNode(t)
	tests := []struct {
		body, action string
		status       int
	}{
		{`{}`, "commit", 400},
		{`null`, "commit", 400},
		{`{} {}`, "commit", 400},
		{`{"command":"exec"}`, "commit", 400},
		{`{"manager_id":"assistant","machine_uid":"uid","executable":"relative","os":"darwin","arch":"arm64"}`, "begin", 400},
	}
	for _, test := range tests {
		r := httptest.NewRequest("POST", "/api/v1/machines/m1/node-update/"+test.action, strings.NewReader(test.body))
		r.SetPathValue("mid", "m1")
		r.SetPathValue("action", test.action)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleFull}))
		w := httptest.NewRecorder()
		tn.hub.nodeUpdateRelay(w, r)
		if w.Code != test.status {
			t.Fatalf("%s -> %d: %s", test.body, w.Code, w.Body.String())
		}
	}
	if got := tn.await(t, 0); len(got) != 0 {
		t.Fatalf("invalid request reached node: %v", got)
	}
}
