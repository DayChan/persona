package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/rpc"
)

// serveAgentSourceExtensionForTest is a plugin with one personal agents source: one session it can send to, the texts
// it was sent kept as messages, and agentsrc.updated after each send (as a plugin watching its agents would).
func serveAgentSourceExtensionForTest() {
	var mu sync.Mutex
	var sent []string
	var hub *pserve.Hub
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-agents", Version: "1.0.0",
		Init: func(_ context.Context, _ extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			hub = h
			return nil, nil
		},
		Namespaces: map[string]pserve.Handler{"agentsrc": func(_ context.Context, method string, params json.RawMessage) (any, error) {
			var p struct {
				Contribution string `json:"contribution"`
				SessionKey   string `json:"session_key"`
				Text         string `json:"text"`
				ClientID     string `json:"client_id"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Contribution != "examplebot" {
				return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no source " + p.Contribution}
			}
			if p.SessionKey != "" && p.SessionKey != "room/1" {
				return nil, &rpc.Error{Code: "not_found", Message: "no session " + p.SessionKey}
			}
			mu.Lock()
			defer mu.Unlock()
			switch method {
			case "status":
				return map[string]any{"accounts": []any{map[string]any{"accountId": "acc", "provider": "example_bot", "status": "active", "sessionCount": 1}}}, nil
			case "sessions":
				return map[string]any{"sessions": []any{map[string]any{"sessionKey": "room/1", "provider": "example_bot", "title": "Room",
					"canSend": true, "unreadCount": len(sent), "rankTimeMs": 100 + len(sent)}}}, nil
			case "messages":
				msgs := []any{}
				for i, t := range sent {
					msgs = append(msgs, map[string]any{"externalId": fmt.Sprintf("m%d", i), "sessionKey": "room/1", "role": "user",
						"position": i, "blocks": []any{map[string]any{"type": "text", "text": t}}})
				}
				return map[string]any{"messages": msgs, "hasMore": false}, nil
			case "send":
				sent = append(sent, p.Text)
				go func() {
					_ = hub.Notify("agentsrc.updated", map[string]any{"contribution": "examplebot", "session_keys": []string{"room/1"}})
				}()
				return map[string]any{"delivery": "accepted", "client_id": p.ClientID}, nil
			case "mark_read":
				return map[string]any{}, nil
			}
			return nil, pserve.UnknownMethod(method)
		}}})
}

func agentSourcePackage(t *testing.T, id, sourceID string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "agents"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=agentsrc exec "+self+" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": %q, "name": "Example agents", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/agents"}},
		"activation_events": ["on_startup"],
		"contributes": {"agent_sources": [{"id": %q, "name": "Example Bot", "providers": ["example_bot"]}]}}`, id, runtime.GOOS+"-"+runtime.GOARCH, sourceID)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A plugin's personal agents source is in the Agents tab next to the hub's own: listed, read, sent to, its own
// updates pushed to the apps at once, and gone when the plugin is turned off. The hub's own source id is not a
// plugin's to take.
func TestExtensionAgentSource(t *testing.T) {
	h, _ := extensionTestHub(t)
	h.initAgents()
	mux := h.routes()
	master := h.cfg.Token

	install := func(dir string) (int, map[string]any) {
		_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+dir+`"}`)
		if pv["preview_id"] == nil {
			return 400, pv
		}
		return call(t, mux, "POST", "/api/v1/plugins/-/install", master,
			fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	}
	if code, out := install(agentSourcePackage(t, "example-agents", "examplebot")); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the source listed", func() bool {
		_, out := call(t, mux, "GET", "/api/v1/agents", master, "")
		srcs, _ := out["sources"].([]any)
		for _, s := range srcs {
			if m := s.(map[string]any); m["source"] == "examplebot" && m["enabled"] == true && len(m["accounts"].([]any)) == 1 {
				return true
			}
		}
		return false
	})

	id := "examplebot:room/1"
	path := "/api/v1/agents/sessions/" + url.PathEscape(id)
	_, out := call(t, mux, "GET", "/api/v1/agents/sessions?provider=example_bot", master, "")
	list, _ := out["sessions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != id || list[0].(map[string]any)["source"] != "examplebot" {
		t.Fatalf("sessions: %v", out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/agents/sessions/"+url.PathEscape("examplebot:nope")+"/messages", master, ""); code != 404 {
		t.Fatalf("a session the plugin does not have: %d %v", code, out)
	}

	before := b1Pushed(h, "agents", id) // what came before the send
	if code, out := call(t, mux, "POST", path+"/messages", master, `{"text": "hello"}`); code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
	// the send's own, then the plugin's agentsrc.updated
	eventually(t, 5*time.Second, "the send's and the plugin's pushes to the apps", func() bool { return b1Pushed(h, "agents", id) >= before+2 })
	_, out = call(t, mux, "GET", path+"/messages?limit=20", master, "")
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 1 || !strings.Contains(fmt.Sprint(msgs[0]), "hello") {
		t.Fatalf("the message sent is read back (the cache dropped by the update): %v", out)
	}
	if code, _ := call(t, mux, "POST", path+"/read", master, ""); code != 200 {
		t.Fatal("mark read", code)
	}

	if code, out := call(t, mux, "PUT", "/api/v1/plugins/example-agents/-/enabled", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("turn off: %d %v", code, out)
	}
	waitLong(t, "the source gone", func() bool {
		_, out := call(t, mux, "GET", "/api/v1/agents/sessions", master, "")
		list, _ := out["sessions"].([]any)
		return len(list) == 0
	})
	if code, _ := call(t, mux, "GET", path+"/messages", master, ""); code != 404 {
		t.Fatal("a turned-off plugin's session:", code)
	}
}
