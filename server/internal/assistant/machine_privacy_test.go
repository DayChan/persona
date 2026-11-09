package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

type privacyHub struct {
	*fakeHub
	phoneCalls int
}

func (f *privacyHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if p.AppID == assistantwire.AppPhone {
		f.phoneCalls++
		if p.Method == assistantwire.PhoneTools {
			return json.Marshal(map[string]any{"tools": []proto.Tool{{Name: "phone_contacts"}}})
		}
		return json.Marshal(proto.TextResult("phone result", false))
	}
	return f.fakeHub.AppCall(ctx, p)
}

type privacyExt struct{ guestExt }

func (e *privacyExt) AgentTools(_ context.Context, assistant bool) ([]proto.Tool, []proto.Tool) {
	agents := []proto.Tool{{Name: "mail_search"}}
	if assistant {
		return agents, []proto.Tool{{Name: "lark_chats"}, {Name: "computer_run"}}
	}
	return agents, nil
}
func (e *privacyExt) CallTool(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool) {
	if name == "mail_search" {
		e.called = append(e.called, name)
		return proto.TextResult("mail result", false), true
	}
	if !assistant {
		return proto.ToolResult{}, false
	}
	return e.guestExt.CallTool(ctx, machine, session, name, args, assistant)
}
func (e *privacyExt) AssistantLark(context.Context) (proto.AssistantLark, error) {
	e.called = append(e.called, "assistant.lark")
	return proto.AssistantLark{}, nil
}

func privacyService(t *testing.T) (*Service, *fleetDevs, *privacyHub, *privacyExt) {
	t.Helper()
	f := &privacyHub{fakeHub: newFakeHub().withMachine()}
	f.hello.Assistant.Machines = []string{"server", "twin", "other", "work"}
	f.hello.Models.InboxAgentMachine = "twin"
	d := newFleetDevs()
	for _, id := range f.hello.Assistant.Machines {
		d.add(id, id)
	}
	d.machines["work"].trust = "work"
	s := New(Options{Hub: f, Devices: d, Logf: t.Logf})
	e := &privacyExt{}
	s.SetExtensions(e)
	return s, d, f, e
}
func privacyRequest(t *testing.T, s *Service, machine, method string, p any) any {
	t.Helper()
	raw, _ := json.Marshal(p)
	out, handled, err := s.NodeRequest(context.Background(), machine, method, raw)
	if err != nil || !handled {
		t.Fatalf("%s: %v handled=%v", method, err, handled)
	}
	return out
}
func privacyTools(t *testing.T, s *Service, machine, session string, assistant bool) []string {
	t.Helper()
	out := privacyRequest(t, s, machine, "mcp.tools", map[string]any{"session_id": session, "assistant": assistant})
	var names []string
	for _, tool := range out.(map[string]any)["tools"].([]proto.Tool) {
		names = append(names, tool.Name)
	}
	return names
}
func privacyCall(t *testing.T, s *Service, machine, session, name string, assistant bool) proto.ToolResult {
	t.Helper()
	return privacyRequest(t, s, machine, "mcp.call", map[string]any{"session_id": session, "name": name, "assistant": assistant, "args": map[string]any{}}).(proto.ToolResult)
}

func TestAssistantMachineAuthorization(t *testing.T) {
	s, d, _, ext := privacyService(t)
	ctx := context.Background()
	d.setSessions("server", []proto.Session{
		{SessionID: "main", Cwd: "/home/u/.persona/assistant"},
		{SessionID: "health", Cwd: "/home/u/.persona/health"},
		{SessionID: "pane", Cwd: "/home/u/.persona/assistant", PaneID: "pane-1"},
		{SessionID: "ordinary", Cwd: "/code/example"},
	})
	for _, test := range []struct {
		machine, session string
		allowed          bool
	}{
		{"server", "main", true}, {"server", "health", true}, {"server", "", true},
		{"server", "proc-starting", true}, {"twin", "starting-twin", true},
		{"server", "pane", false}, {"server", "ordinary", false},
		{"other", "", false}, {"other", "main", false}, {"work", "main", false},
	} {
		t.Run(test.machine+"/"+test.session, func(t *testing.T) {
			names := privacyTools(t, s, test.machine, test.session, true)
			if got := slices.Contains(names, "kb_search"); got != test.allowed {
				t.Fatalf("assistant tools=%v: %v", got, names)
			}
			res := privacyCall(t, s, test.machine, test.session, "lark_chats", true)
			if res.IsError == test.allowed {
				t.Fatalf("call=%+v", res)
			}
			raw, _ := json.Marshal(map[string]any{"session_id": test.session})
			for _, method := range []string{"assistant.context", "assistant.model"} {
				out, handled, err := s.NodeAnswer(ctx, test.machine, method, raw)
				if err != nil || !handled {
					t.Fatalf("%s: %v", method, err)
				}
				data, _ := json.Marshal(out)
				if !test.allowed && string(data) != "{}" && string(data) != `{"text":""}` {
					t.Fatalf("%s leaked %s", method, data)
				}
				if test.allowed && method == "assistant.context" && out.(ContextResult).Text == "" {
					t.Fatal("missing assistant context")
				}
			}
		})
	}
	before := len(ext.called)
	s.assistantLark(ctx, "server")
	s.assistantLark(ctx, "other")
	s.assistantLark(ctx, "work")
	if len(ext.called) != before+1 {
		t.Fatal("assistant.lark machine boundary changed")
	}
}

func TestWorkMachineToolsDeniedAtExecution(t *testing.T) {
	s, _, f, e := privacyService(t)
	for _, hint := range []bool{false, true} {
		names := privacyTools(t, s, "work", "ordinary", hint)
		if !slices.Contains(names, "report_to_supervisor") {
			t.Fatal("work agent lost its supervisor tool")
		}
		for _, name := range []string{"phone_contacts", "mail_search", "lark_chats", "computer_run", "kb_search", "agents_list"} {
			if slices.Contains(names, name) {
				t.Fatalf("work machine lists %s", name)
			}
			if res := privacyCall(t, s, "work", "ordinary", name, hint); !res.IsError {
				t.Fatalf("work machine called %s", name)
			}
		}
	}
	if f.phoneCalls != 0 || len(e.called) != 0 {
		t.Fatalf("denied request reached phone/extension: %d %v", f.phoneCalls, e.called)
	}
	for _, trust := range []string{"", "personal"} {
		t.Run("personal="+trust, func(t *testing.T) {
			s, d, _, _ := privacyService(t)
			d.machines["other"].trust = trust
			for _, name := range []string{"phone_contacts", "mail_search"} {
				if !slices.Contains(privacyTools(t, s, "other", "ordinary", false), name) {
					t.Fatalf("missing %s", name)
				}
				if res := privacyCall(t, s, "other", "ordinary", name, false); res.IsError {
					t.Fatalf("%s: %+v", name, res)
				}
			}
		})
	}
}

func TestExplicitAssistantStartAuthority(t *testing.T) {
	s, _, _, _ := privacyService(t)
	ctx := context.Background()
	if s.assistantCaller(ctx, "other", "") {
		t.Fatal("unconfigured machine authorized")
	}
	finish := s.beginAssistantStart("other")
	if !s.assistantCaller(ctx, "other", "") {
		t.Fatal("explicit start cannot bootstrap")
	}
	s.originSet(ctx, "other", "started", OriginUser, "", "")
	finish()
	if s.assistantCaller(ctx, "other", "") || !s.assistantCaller(ctx, "other", "started") {
		t.Fatal("start authority not scoped to the recorded session")
	}
	s.origins.m["backfilled"] = &convOrigin{MachineID: "other", By: originByBackfill}
	if s.assistantCaller(ctx, "other", "backfilled") {
		t.Fatal("node-derived backfill grants authority")
	}
}

func TestAssistantSettingsRejectWorkMachine(t *testing.T) {
	s, _, f, _ := privacyService(t)
	f.sessions["work"] = nil
	for _, lang := range []string{"en", "zh-Hans", "ja"} {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(`{"machine_id":"work"}`))
		r.Header.Set(RoleHeader, "full")
		r.Header.Set("Accept-Language", lang)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "assistant-start.work_machine") {
			t.Fatalf("%s: %d %s", lang, w.Code, w.Body)
		}
	}
	if got := s.effective(context.Background(), Settings{MachineID: "work"}).MachineID; got != "server" {
		t.Fatalf("persisted work placement: %s", got)
	}
	// During propagation either trusted configuration can tighten access.
	f.hello.Assistant.WorkMachines = []string{"other"}
	if !s.isWorkMachine("other") {
		t.Fatal("new hub restriction lost to stale devices")
	}
}
