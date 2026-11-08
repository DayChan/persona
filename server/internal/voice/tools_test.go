package voice

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

// declared is the names of the function tools a session start declares.
func declared(start map[string]any) []string {
	decls, _ := start["tools"].([]any)
	var out []string
	for _, d := range decls {
		out = append(out, d.(map[string]any)["name"].(string))
	}
	return out
}

// sessionStart is the next session start the service got, and the tools it declares.
func sessionStart(up *fakeUpstream) (map[string]any, []string) {
	up.t.Helper()
	start := up.next("session.start")["session"].(map[string]any)
	return start, declared(start)
}

// connections is how many sessions the service was asked for.
func (u *fakeUpstream) connections() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.headers)
}

// refusingTools is a live-dialect service that refuses a session start declaring function tools with refuse (an error
// frame, a close), and starts one without them.
func refusingTools(t *testing.T, refuse func(u *fakeUpstream, c *websocket.Conn)) *fakeUpstream {
	return newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] != "session.start" {
			return
		}
		if _, tools := f["session"].(map[string]any)["tools"]; tools {
			refuse(u, c)
			return
		}
		u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
	})
}

// unknownTools is how the public GPT-Live API answers a session start with tools: unknown_parameter, the connection
// left open (docs/lessons/voice.md "Errors").
func unknownTools(u *fakeUpstream, c *websocket.Conn) {
	u.send(c, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "code": "unknown_parameter",
		"param": "session.tools", "message": "Unknown parameter: 'session.tools'."}})
}

// The settings form's upstream (no tools switch) declares Persona's function tools, so a service that only hands work
// over through a declared function does (#1420): the model's ask_persona reaches the main conversation.
func TestFormUpstreamDeclaresToolsAndHandsOver(t *testing.T) {
	hub := newFakeHub(t)
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.commentary.append":
			if f["delegation_id"] != nil {
				return
			}
			// What the user typed: the model calls ask_persona, the only way this service hands work over.
			u.send(c, map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "dlg1", "target": "client"}})
			u.send(c, map[string]any{"type": "response.event", "delegation_id": "dlg1", "event": map[string]any{
				"type": "function_call", "call_id": "c1", "name": "ask_persona", "arguments": `{"task":"查明天下午的日程"}`,
			}})
		}
	})
	form, _ := json.Marshal(map[string]any{"url": up.url(), "dialect": "live", "models": []string{"m1"}})
	cfg, err := Merge(Sources{Form: form, Key: "k"})
	if err != nil || len(cfg.Providers) != 1 || cfg.Providers[0].ID != FormProvider || cfg.Providers[0].Tools != nil {
		t.Fatalf("%+v %v", cfg, err)
	}
	pl := testPlugin(t, cfg, hub.server(t))
	a := dialApp(t, pl, map[string]any{"type": "start", "model": "custom/m1"})
	a.until(ofType("ready"))
	start, tools := sessionStart(up)
	if strings.Join(tools, ",") != strings.Join(toolNames, ",") || !strings.Contains(start["instructions"].(string), "ask_persona") {
		t.Fatalf("tools %v", tools)
	}
	a.send(map[string]any{"type": "text", "text": "帮我查一下明天下午的日程"})
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "明天下午三点有会") {
		t.Fatalf("result: %s", got)
	}
	if msgs := hub.messages(); len(msgs) != 1 || !strings.HasPrefix(msgs[0].Text, "查明天下午的日程\n") {
		t.Fatalf("main conversation got %+v", msgs)
	}
	if up.connections() != 1 || pl.refusesTools(FormProvider) {
		t.Fatalf("%d sessions asked for", up.connections())
	}
}

// A service that refuses the tools of an auto provider's session start gets the session again without them, once, and
// the plugin remembers it: the next conversation does not try them (#1420).
func TestRefusedToolsStartTheSessionWithoutThem(t *testing.T) {
	for name, refuse := range map[string]func(u *fakeUpstream, c *websocket.Conn){
		"error frame": unknownTools,
		"error that says so": func(u *fakeUpstream, c *websocket.Conn) {
			u.send(c, map[string]any{"type": "error", "error": map[string]any{"code": "invalid_request", "message": "tools are not supported"}})
		},
		"unsupported field": func(u *fakeUpstream, c *websocket.Conn) {
			u.send(c, map[string]any{"type": "error", "error": map[string]any{"code": "unsupported_extension", "message": "not supported"}})
		},
		"close that says so": func(u *fakeUpstream, c *websocket.Conn) {
			_ = c.Close(websocket.StatusPolicyViolation, "session.tools is not supported")
		},
	} {
		t.Run(name, func(t *testing.T) {
			hub := newFakeHub(t)
			up := refusingTools(t, refuse)
			pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}}}}, hub.server(t))
			a := dialApp(t, pl, map[string]any{"type": "start"})
			a.until(ofType("ready"))
			if _, tools := sessionStart(up); len(tools) != len(toolNames) {
				t.Fatalf("first try: tools %v", tools)
			}
			start, tools := sessionStart(up)
			if len(tools) != 0 || strings.Contains(start["instructions"].(string), "ask_persona") {
				t.Fatalf("second try: tools %v", tools)
			}
			if n := up.connections(); n != 2 || !pl.refusesTools("rt") {
				t.Fatalf("%d sessions asked for, remembered %v", n, pl.refusesTools("rt"))
			}
			_ = a.c.CloseNow()

			b := dialApp(t, pl, map[string]any{"type": "start"})
			b.until(ofType("ready"))
			if _, tools := sessionStart(up); len(tools) != 0 {
				t.Fatalf("the next conversation: tools %v", tools)
			}
			if n := up.connections(); n != 3 {
				t.Fatalf("the next conversation asked for %d sessions", n-2)
			}
		})
	}
}

// A session that is refused without tools too was not refused for them: the conversation fails, after one more try,
// and nothing is remembered.
func TestRefusedWithoutToolsTooIsNotRemembered(t *testing.T) {
	hub := newFakeHub(t)
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		if f["type"] == "session.start" {
			u.send(c, map[string]any{"type": "error", "error": map[string]any{"code": "unknown_parameter", "message": "unknown parameter"}})
		}
	})
	pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}}}}, hub.server(t))
	a := dialApp(t, pl, map[string]any{"type": "start"})
	if f := a.until(ofType("error")); f["fatal"] != true || !strings.Contains(f["message"].(string), "unknown parameter") {
		t.Fatalf("%v", f)
	}
	if n := up.connections(); n != 2 || pl.refusesTools("rt") {
		t.Fatalf("%d sessions asked for, remembered %v", n, pl.refusesTools("rt"))
	}
}

// An explicit "tools" (hub.json) decides it: false never declares them; true declares them, and a refusal of them
// fails the conversation as it did, with no fallback.
func TestExplicitToolsHaveNoFallback(t *testing.T) {
	t.Run("false", func(t *testing.T) {
		hub := newFakeHub(t)
		up := refusingTools(t, unknownTools)
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
		a := dialApp(t, pl, map[string]any{"type": "start"})
		a.until(ofType("ready"))
		if start, tools := sessionStart(up); len(tools) != 0 || start["tools"] != nil {
			t.Fatalf("tools %v", tools)
		}
		if n := up.connections(); n != 1 {
			t.Fatalf("%d sessions asked for", n)
		}
	})
	t.Run("true", func(t *testing.T) {
		hub := newFakeHub(t)
		up := refusingTools(t, unknownTools)
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
		a := dialApp(t, pl, map[string]any{"type": "start"})
		if f := a.until(ofType("error")); f["fatal"] != true || !strings.Contains(f["message"].(string), "session.tools") {
			t.Fatalf("%v", f)
		}
		if _, tools := sessionStart(up); len(tools) != len(toolNames) {
			t.Fatalf("tools %v", tools)
		}
		if n := up.connections(); n != 1 || pl.refusesTools("rt") {
			t.Fatalf("%d sessions asked for", n)
		}
	})
}

// A refused key is not a refusal of the tools: no second try, whether the handshake is refused or the service says so
// in an error frame.
func TestAuthErrorHasNoFallback(t *testing.T) {
	t.Run("handshake", func(t *testing.T) {
		hub := newFakeHub(t)
		var tries atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tries.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "Unauthorized")
		}))
		t.Cleanup(srv.Close)
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/live"
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: url, Models: []string{"m1"}}}}, hub.server(t))
		a := dialApp(t, pl, map[string]any{"type": "start"})
		if f := a.until(ofType("error")); f["fatal"] != true || f["code"] != codeUpstreamForbidden {
			t.Fatalf("%v", f)
		}
		if n := tries.Load(); n != 1 || pl.refusesTools("rt") {
			t.Fatalf("%d handshakes", n)
		}
	})
	t.Run("error frame", func(t *testing.T) {
		hub := newFakeHub(t)
		up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
			if f["type"] == "session.start" {
				u.send(c, map[string]any{"type": "error", "error": map[string]any{"code": "invalid_api_key", "message": "Incorrect API key provided"}})
			}
		})
		pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}}}}, hub.server(t))
		a := dialApp(t, pl, map[string]any{"type": "start"})
		if f := a.until(ofType("error")); f["fatal"] != true || !strings.Contains(f["message"].(string), "API key") {
			t.Fatalf("%v", f)
		}
		if n := up.connections(); n != 1 || pl.refusesTools("rt") {
			t.Fatalf("%d sessions asked for", n)
		}
	})
}

func TestRefusedTools(t *testing.T) {
	started := func(err error) error { return fmt.Errorf("the session did not start: %w", err) }
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&sessionRefused{code: "unknown_parameter", param: "session.tools"}, true},
		{&sessionRefused{code: "invalid_request", param: "session.tools[0].parameters"}, true},
		{&sessionRefused{code: "invalid_request", text: "Function tools are not supported"}, true},
		{&sessionRefused{code: "unknown_parameter"}, true},
		{&sessionRefused{code: "unsupported_extension"}, true},
		{&sessionRefused{code: "unknown_parameter", param: "session.delegation"}, false}, // another field
		{&sessionRefused{code: "invalid_request", param: "session.model", text: "unknown model"}, false},
		{&sessionRefused{code: "invalid_request", text: "failed to establish a session"}, false},
		{&sessionRefused{code: "invalid_api_key", text: "Incorrect API key provided"}, false},
		{&upstreamError{code: codeUpstreamUnavailable, err: &sessionRefused{code: codeUpstreamUnavailable, text: "tools backend down"}}, false},
		{&upstreamError{code: codeUpstreamForbidden, err: errors.New("HTTP 401: Unauthorized")}, false},
		{started(websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "session.tools is not supported"}), true},
		{started(websocket.CloseError{Code: websocket.StatusPolicyViolation}), false},
		{started(io.EOF), false},
		{errors.New("dial tcp: connection refused"), false},
	} {
		if got := refusedTools(c.err); got != c.want {
			t.Errorf("%v: %v", c.err, got)
		}
	}
}

// hub.json's "tools": unset is auto, true and false are kept as said; only the live dialect declares any.
func TestToolsMode(t *testing.T) {
	c, err := Merge(Sources{HubJSON: json.RawMessage(`{"providers":[
		{"id":"auto","url":"wss://a.example.com/v1/live"},
		{"id":"on","url":"wss://b.example.com/v1/live","tools":true},
		{"id":"off","url":"wss://c.example.com/v1/live","tools":false},
		{"id":"codex","url":"wss://d.example.com/v1/live","dialect":"codex"},
		{"id":"dev","dialect":"fake","tools":true}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]bool{"auto": {true, true}, "on": {true, false}, "off": {false, false}, "codex": {false, false}, "dev": {false, false}}
	for _, p := range c.Providers {
		declare, auto := p.toolsMode()
		if w := want[p.ID]; declare != w[0] || auto != w[1] {
			t.Errorf("%s: declare %v auto %v", p.ID, declare, auto)
		}
	}
}
