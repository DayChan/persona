package hub

// An extension's token acts as its manager and only as it (docs/assistant/managers.md "Ownership"): whatever names the target
// (the path, the query, a header, the body), another manager's machines, sessions, cards and conversations, and
// another manager itself, are out of its reach.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// reachHub: A (the assistant) owns ma with session s3, its card ix_a and its conversation conv-a; B owns mb with
// session b1 and knows the extension token bx-token (opening every route: the scope is not what is tested).
func reachHub(t *testing.T) (*Hub, string, *guardManager, *guardManager) {
	t.Helper()
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	answer := func(who string, knows bool) func(string, json.RawMessage) (any, error) {
		return func(method string, _ json.RawMessage) (any, error) {
			switch method {
			case extwire.MethodAuthorize:
				if knows {
					return extwire.Authorized{OK: true, Extension: "bx", Name: "BX"}, nil
				}
				return extwire.Authorized{}, nil
			case assistantwire.MethodHTTP:
				return map[string]any{"status": 200, "body": []byte(`{"who":"` + who + `","items":[{"id":"` + who + `-item"}]}`)}, nil
			case devicewire.MethodCall:
				return map[string]any{"who": who, "session": map[string]any{"session_id": who + "-started"}}, nil
			}
			return map[string]any{"who": who}, nil
		}
	}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answer("a", false))
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answer("b", true))
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "ma, mb", func() bool { return h.localMachine("ma") != nil && h.localMachine("mb") != nil })
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: []proto.Session{{MachineID: "ma", SessionID: "s3", Title: "A-SESSION"}}})
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "b1", Title: "B-SESSION"}}})
	for _, c := range []struct {
		g  *guardManager
		ix proto.Interaction
	}{{a, proto.Interaction{ID: "ix_a", MachineID: "ma", SessionID: "s3", Kind: proto.IxPermission, Summary: "A-CARD"}},
		{b, proto.Interaction{ID: "ix_b", MachineID: "mb", SessionID: "b1", Kind: proto.IxPermission, Summary: "B-CARD"}}} {
		if err := c.g.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: c.ix}); err != nil {
			t.Fatal(err)
		}
	}
	var res assistantwire.SnapshotResult
	if err := a.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "a", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "ma", SessionID: "conv-a"}}}, &res); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "both cards", func() bool { return h.interactionOwner("ix_a") != "" && h.interactionOwner("ix_b") != "" })
	return h, srv.URL, a, b
}

// extDo is a request with B's extension's token.
func extDo(t *testing.T, url, method, path, body string, header map[string]string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, url+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+extTokenPrefix+"bx-token")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	var out json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, string(out)
}

// reached is what manager f got but whose-token-is-this (and what the hub does on its own: fs.watch, the tools).
func reached(f *guardManager) []string {
	var out []string
	for _, l := range strings.Split(f.log(), "\n") {
		if l == "" || strings.HasPrefix(l, extwire.MethodAuthorize+" ") || strings.HasPrefix(l, "assistant.tools") ||
			strings.Contains(l, `"method":"fs.watch"`) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// B's extension cannot reach A by naming it, nor A's machine, session, card or conversation wherever it is named, and
// A's things are left out of the hub's own lists; what is B's goes through.
func TestAnExtensionsTokenActsOnlyAsItsManager(t *testing.T) {
	h, url, a, b := reachHub(t)
	namesA := map[string]string{ManagerHeader: assistantwire.AssistantID}
	refused := []struct {
		name, method, path, body string
		header                   map[string]string
	}{
		{"names A: a merged list", "GET", "/api/v1/inbox", "", namesA},
		{"names A: a paged list", "GET", "/api/v1/inbox/done", "", namesA},
		{"names A by query: a manager route", "GET", "/api/v1/assistant?manager=assistant", "", nil},
		{"names A: an inbox reply", "POST", "/api/v1/inbox/item-1/reply", `{"text":"x"}`, namesA},
		{"names A: the knowledge base", "GET", "/api/v1/kb/people", "", namesA},
		{"names A: another extension's HTTP", "GET", "/api/v1/extensions/lark/things", "", namesA},
		{"A's card choice", "POST", "/api/v1/interactions/ix_a/choose", `{"option":0,"rev":"old"}`, nil},
		{"A's conversation's feed", "GET", "/api/v1/assistant/conversations/conv-a/feed", "", nil},
		{"A's conversation's stop", "POST", "/api/v1/assistant/conversations/conv-a/stop", "", nil},
		{"an agent on A's machine (body)", "POST", "/api/v1/sessions", `{"machine_id":"ma","cwd":"/tmp","prompt":"B's prompt"}`, nil},
		{"A's session resumed (body)", "POST", "/api/v1/sessions", `{"machine_id":"ma","resume_session_id":"s3","cwd":"/tmp"}`, nil},
		{"the sessions of ma (query)", "GET", "/api/v1/sessions?machine_id=ma", "", nil},
		{"the paged sessions of ma (query)", "GET", "/api/v1/sessions?limit=10&machine_id=ma", "", nil},
		{"a notification on A's session (body)", "POST", "/api/v1/notify", `{"title":"t","body":"b","machine_id":"ma","session_id":"s3"}`, nil},
	}
	for _, c := range refused {
		before := len(reached(a))
		if code, out := extDo(t, url, c.method, c.path, c.body, c.header); code != http.StatusForbidden || !strings.Contains(out, codeOtherManager) {
			t.Errorf("%s: %d %s", c.name, code, clip(out, 200))
		}
		if got := reached(a); len(got) != before {
			t.Errorf("%s: A was reached: %v", c.name, got[before:])
		}
	}
	filtered := []struct{ name, path, mine, theirs string }{
		{"sessions", "/api/v1/sessions", "B-SESSION", "A-SESSION"},
		{"paged sessions", "/api/v1/sessions?limit=10", "B-SESSION", "A-SESSION"},
		{"session counts", "/api/v1/sessions?counts=1", `"mb":1`, `"ma"`},
		{"cards", "/api/v1/interactions", "B-CARD", "A-CARD"},
		{"machines", "/api/v1/machines", `"id":"mb"`, `"id":"ma"`},
	}
	for _, c := range filtered {
		code, out := extDo(t, url, "GET", c.path, "", nil)
		if code != http.StatusOK || !strings.Contains(out, c.mine) || strings.Contains(out, c.theirs) {
			t.Errorf("%s: %d %s", c.name, code, clip(out, 300))
		}
	}
	// Its own: B's manager, by name or not, B's machine, a notification on B's session.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/assistant?manager=m-0b0b0b", ""},
		{"GET", "/api/v1/assistant", ""},
		{"GET", "/api/v1/inbox/done", ""},
		{"POST", "/api/v1/notify", `{"title":"t","body":"b","machine_id":"mb","session_id":"b1"}`},
	} {
		if code, out := extDo(t, url, c.method, c.path, c.body, nil); code != http.StatusOK {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, clip(out, 200))
		}
	}
	if code, out := extDo(t, url, "POST", "/api/v1/sessions", `{"machine_id":"mb","cwd":"/tmp","prompt":"hi"}`, nil); code == http.StatusForbidden ||
		!slices.ContainsFunc(reached(b), func(l string) bool { return strings.Contains(l, `"method":"session.start"`) }) {
		t.Errorf("an agent on B's machine: %d %s", code, clip(out, 200))
	}
	_ = h
}

// Every route an extension's token can open (extwire.RouteScopes) is bounded to its manager one way or another; a new
// one has to say how here. "path": its machine, session, card or conversation is in the path (extensionReach);
// "manager": it goes to the extension's own manager (requestManager, byConversation, mergeList), which is held to its
// own machines like any manager; "own": the hub's own answer, limited to the manager's things; "body": the route
// checks what its body or query names itself.
func TestEveryExtensionRouteIsBounded(t *testing.T) {
	bounded := map[string]string{
		"GET /api/v1/kb/{kind}": "manager", "GET /api/v1/kb/{kind}/{id}": "manager", "GET /api/v1/kb/chats/{id}/context": "manager",
		"GET /api/v1/kb/chats/{id}/days/{day}": "manager", "GET /api/v1/kb/moments/{id}": "manager", "GET /api/v1/kb-search": "manager",
		"PUT /api/v1/kb/{kind}/{id}": "manager",
		"GET /api/v1/inbox":          "manager", "GET /api/v1/inbox/done": "manager", "GET /api/v1/inbox/done/days": "manager", "GET /api/v1/inbox/{id}": "manager", "GET /api/v1/inbox/mutes": "manager",
		"GET /api/v1/inbox/policies": "manager", "POST /api/v1/inbox/done": "manager", "POST /api/v1/inbox/bulk": "manager",
		"POST /api/v1/inbox/{id}/done": "manager", "POST /api/v1/inbox/{id}/draft": "manager", "POST /api/v1/inbox/{id}/reply": "manager",
		"POST /api/v1/inbox/{id}/suggestion": "manager", "POST /api/v1/inbox/{id}/handoff": "manager", "POST /api/v1/inbox/{id}/doc/status": "manager",
		"PUT /api/v1/inbox/{id}/parent": "manager", "GET /api/v1/inbox/tree/{kind}/{id...}": "manager",
		"PUT /api/v1/inbox/{id}/folder": "manager", "GET /api/v1/inbox/folders": "manager", "PUT /api/v1/inbox/folders": "manager",
		"POST /api/v1/inbox/mutes": "manager", "DELETE /api/v1/inbox/mutes/{id}": "manager", "POST /api/v1/inbox/policies": "manager",
		"PATCH /api/v1/inbox/policies/{id}": "manager", "DELETE /api/v1/inbox/policies/{id}": "manager", "POST /api/v1/inbox/refresh": "manager",
		"GET /api/v1/sessions/archived":                      "own+body",
		"GET /api/v1/sessions":                               "own+body", // its machines; ?machine_id= another's: 403
		"GET /api/v1/sessions/{mid}/{sid}/info":              "path",
		"GET /api/v1/sessions/{mid}/{sid}/items":             "path",
		"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full":  "path",
		"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image": "path",
		"GET /api/v1/sessions/{mid}/{sid}/screen":            "path",
		"GET /api/v1/interactions":                           "own",
		"GET /api/v1/interactions/history":                   "manager",
		"GET /api/v1/agents":                                 "manager", "GET /api/v1/agents/sessions": "manager",
		"GET /api/v1/agents/sessions/{id}": "manager", "GET /api/v1/agents/sessions/{id}/messages": "manager",
		"GET /api/v1/agents/sessions/{id}/avatar": "manager",
		"GET /api/v1/workspaces":                  "manager", "GET /api/v1/workspace-board": "manager",
		"POST /api/v1/sessions":                            "body", // machine_id, resume_session_id: its manager's (startSession)
		"POST /api/v1/sessions/{mid}/{sid}/messages":       "path",
		"POST /api/v1/sessions/{mid}/{sid}/interrupt":      "path",
		"POST /api/v1/sessions/{mid}/{sid}/queue":          "path",
		"POST /api/v1/sessions/{mid}/{sid}/queue/send-now": "path",
		"POST /api/v1/sessions/{mid}/{sid}/seen":           "path",
		"POST /api/v1/sessions/{mid}/{sid}/rename":         "path",
		"POST /api/v1/sessions/{mid}/{sid}/modes":          "path", // and only of a conversation its extension opened (extensionModeRefused)
		"POST /api/v1/interactions/{id}/choose":            "path",
		"POST /api/v1/interactions/{id}/resolve":           "path",
		"POST /api/v1/interactions/{id}/reanswer":          "path", // pending: its card; no longer pending: its manager
		"POST /api/v1/agents/sessions/{id}/messages":       "manager",
		"POST /api/v1/agents/sessions/{id}/read":           "manager",
		"GET /api/v1/machines":                             "own",
		"GET /api/v1/machines/{id}/metrics":                "path",
		"GET /api/v1/machines/{id}/usage":                  "path",
		"GET /api/v1/machines/{mid}/stat":                  "path",
		"POST /api/v1/notify":                              "body", // machine_id, session_id: its manager's (ownsRef)
		"GET /api/v1/assistant":                            "manager",
		"GET /api/v1/assistant/capabilities":               "manager",
		"POST /api/v1/assistant/conversations":             "manager",
		"GET /api/v1/assistant/conversations/{id}/feed":    "path",
		"POST /api/v1/assistant/conversations/{id}/stop":   "path",
		"GET /api/v1/assistant/reporters":                  "manager",
		"GET /api/v1/todos":                                "manager", "GET /api/v1/todos/{id}/decisions": "manager",
		"POST /api/v1/todos": "manager", "PATCH /api/v1/todos/{id}": "manager",
		"POST /api/v1/todos/{id}/decisions":                 "manager",
		"PUT /api/v1/todos/{id}/agents/{mid}/{sid}":         "path",
		"DELETE /api/v1/todos/{id}/agents/{mid}/{sid}":      "path",
		"PUT /api/v1/todos/{id}/spaces/{mid}/{space...}":    "path",
		"DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}": "path",
		"/api/v1/extensions/{id}/":                          "manager",
	}
	for route := range extwire.RouteScopes {
		how, ok := bounded[route]
		if !ok {
			t.Errorf("%s is open to extensions: say here how it is held to the extension's manager (and check it)", route)
			continue
		}
		path := route
		if _, p, ok := strings.Cut(route, " "); ok {
			path = p
		}
		inPath := strings.Contains(path, "{mid}") || strings.HasPrefix(path, "/api/v1/machines/{id}") ||
			strings.HasPrefix(path, "/api/v1/interactions/{id}") || strings.HasPrefix(path, "/api/v1/assistant/conversations/{id}")
		if (how == "path") != inPath {
			t.Errorf("%s: said %q, but extensionReach sees its target in the path: %v", route, how, inPath)
		}
	}
	for route := range bounded {
		if _, ok := extwire.RouteScopes[route]; !ok {
			t.Errorf("%s is not open to extensions any more: take it out of this list", route)
		}
	}
}
