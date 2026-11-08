package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func permissionSettingsRequest(t *testing.T, s *Service, method, path, target, role, body string) (*httptest.ResponseRecorder, assistantView) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if target != "" {
		r.Header.Set(assistantwire.ManagerHeader, target)
	}
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out assistantView
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return w, out
}

func TestDefaultPermissionSettingsScopeAndReload(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) {
		v.Main, v.Launcher = "existing-main", "claude"
		v.Former = []FormerMain{{MachineID: "server", SessionID: "old-main"}}
	})
	w, view := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", "assistant", "full", "")
	if w.Code != 200 || view.Manager != "assistant" || view.DefaultPermissionMode != "" || view.PermissionDefaults.Version != 1 ||
		view.PermissionDefaults.Manager != view.Manager || !reflect.DeepEqual(view.PermissionDefaults.Modes, proto.PermissionModes) {
		t.Fatalf("legacy defaults: %d %+v", w.Code, view)
	}
	before, _ := s.savedSettings(ctx)
	w, _ = permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant?manager=m-other", "assistant", "full", `{"default_permission_mode":"bypassPermissions","main":"-"}`)
	if got, _ := s.savedSettings(ctx); w.Code != 400 || !reflect.DeepEqual(got, before) {
		t.Fatalf("conflicting targets changed settings: %d %+v", w.Code, got)
	}
	for _, tc := range []struct {
		target, role, body string
		code               int
	}{
		{"", "full", `{"default_permission_mode":"bypassPermissions","main":"-"}`, 400},
		{"m-other", "full", `{"default_permission_mode":"bypassPermissions","main":"-"}`, 403},
		{"assistant", "answer", `{"default_permission_mode":"bypassPermissions"}`, 403},
		{"assistant", "extension", `{"default_permission_mode":"bypassPermissions"}`, 403},
		{"assistant", "full", `{"default_permission_mode":"","main":"-"}`, 400},
		{"assistant", "full", `{"default_permission_mode":null,"main":"-"}`, 400},
		{"assistant", "full", `{"default_permission_mode":false,"main":"-"}`, 400},
		{"assistant", "full", `{"default_permission_mode":"unsafe","launcher":"codex-l","main":"-"}`, 400},
	} {
		puts := st.puts
		w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", tc.target, tc.role, tc.body)
		got, _ := s.savedSettings(ctx)
		if w.Code != tc.code || !reflect.DeepEqual(got, before) || st.puts != puts || len(f.sends) != 0 {
			t.Fatalf("non-atomic refusal: %+v -> %d %s; %+v", tc, w.Code, w.Body, got)
		}
	}
	for _, mode := range proto.PermissionModes {
		w, view := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant?manager=assistant", "", "full", `{"default_permission_mode":"`+mode+`"}`)
		if w.Code != 200 || view.DefaultPermissionMode != mode || view.Main != before.Main || view.Launcher != before.Launcher || !reflect.DeepEqual(view.Former, before.Former) {
			t.Fatalf("save %s: %d %+v", mode, w.Code, view)
		}
	}
	// Reconstruct the service with only its serialized store; no preferences cache survives.
	reloaded := newServiceOn(t, f, st)
	w, view = permissionSettingsRequest(t, reloaded, "GET", "/api/v1/assistant", "assistant", "full", "")
	if w.Code != 200 || view.DefaultPermissionMode != proto.ModeBypass || view.Main != before.Main {
		t.Fatalf("reload: %d %+v", w.Code, view)
	}
	w, view = permissionSettingsRequest(t, reloaded, "PUT", "/api/v1/assistant", "", "full", `{"main_tasks_quiet":false}`)
	if w.Code != 200 || view.DefaultPermissionMode != proto.ModeBypass {
		t.Fatalf("sparse legacy save reset permission: %d %+v", w.Code, view)
	}
	w, view = permissionSettingsRequest(t, reloaded, "PUT", "/api/v1/assistant", "assistant", "full", `{"default_permission_mode":"default"}`)
	if w.Code != 200 || view.DefaultPermissionMode != "default" {
		t.Fatalf("explicit default: %d %+v", w.Code, view)
	}
}

func TestDefaultPermissionNewConversationInheritance(t *testing.T) {
	for _, mode := range append([]string{""}, proto.PermissionModes...) {
		t.Run(mode, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = mode })
			for _, p := range []StartParams{{}, {Main: true}, {Space: SpaceHealth}, {Purpose: "assistant"}} {
				p.Untitled = true
				sess, err := s.Start(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				if sent := f.sends[len(f.sends)-1]; sent["permission_mode"] != mode || sess.PermissionMode != mode {
					t.Fatalf("%+v inherited %v / %s, want %s", p, sent, sess.PermissionMode, mode)
				}
			}
			for _, override := range proto.PermissionModes {
				sess, err := s.Start(context.Background(), StartParams{PermissionMode: override, Untitled: true})
				if err != nil || sess.PermissionMode != override || f.sends[len(f.sends)-1]["permission_mode"] != override {
					t.Fatalf("explicit %s lost to %s: %+v %v", override, mode, sess, err)
				}
			}
			// Existing conversations keep their saved mode on later turns; the manager default is not injected.
			for _, override := range []string{"", "default", "plan"} {
				params := map[string]any{"text": "next"}
				if override != "" {
					params["permission_mode"] = override
				}
				got, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "existing", Params: params})
				var want any
				if override != "" {
					want = override
				}
				if err != nil || got["permission_mode"] != want {
					t.Fatalf("existing conversation overwritten: %v %v", got, err)
				}
			}
			before := len(f.sends)
			w, _ := permissionSettingsRequest(t, s, "POST", "/api/v1/assistant/main/new", "assistant", "full", `{"carry":"none"}`)
			if w.Code != 200 || len(f.sends) != before {
				t.Fatalf("main/new started a turn: %d %s", w.Code, w.Body)
			}
			_, err := s.Start(context.Background(), StartParams{Main: true, Untitled: true})
			if err != nil || f.sends[len(f.sends)-1]["permission_mode"] != mode {
				t.Fatalf("replacement main lost default: %v", err)
			}
		})
	}
}

func TestDefaultPermissionGuestDoesNotInherit(t *testing.T) {
	f, s, _ := guestService(t)
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = proto.ModeBypass })
	w := postConversation(t, s, `{"prompt":"hello","guest":{"tools":["lark_agent_reply"]}}`)
	if w.Code != 200 || f.sends[len(f.sends)-1]["permission_mode"] != "" {
		t.Fatalf("guest inherited permissions: %d %s %v", w.Code, w.Body, f.sends)
	}
}

func TestDefaultPermissionManagersKeepIndependentStores(t *testing.T) {
	stores := []*memStore{{}, {}}
	ids := []string{"assistant", "m-example"}
	modes := []string{proto.ModeBypass, "plan"}
	f := newFakeHub().withMachine()
	for pass := 0; pass < 2; pass++ {
		for i, id := range ids {
			st := stores[i]
			s := withSettings(t, f, New(Options{ID: id, Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
			if pass == 0 {
				w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", id, "full", `{"default_permission_mode":"`+modes[i]+`"}`)
				if w.Code != 200 {
					t.Fatalf("save %s: %d %s", id, w.Code, w.Body)
				}
			}
			w, view := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", id, "full", "")
			if w.Code != 200 || view.Manager != id || view.DefaultPermissionMode != modes[i] || view.PermissionDefaults.Manager != id || view.PermissionDefaults.Version != 1 {
				t.Fatalf("pass %d %s: %d %+v", pass, id, w.Code, view)
			}
		}
	}
}

// The saved preference reaches new conversations regardless of the chosen native backend.
func TestDefaultPermissionSavedForBothLaunchers(t *testing.T) {
	for _, launcher := range []string{"claude", "codex-l"} {
		for _, mode := range proto.PermissionModes {
			t.Run(launcher+"/"+mode, func(t *testing.T) {
				f := newFakeHub().withMachine()
				st := &memStore{}
				s := newServiceOn(t, f, st)
				setSettings(t, s, func(v *Settings) { v.Launcher = launcher; v.Main = "existing-main" })
				w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "assistant", "full", `{"default_permission_mode":"`+mode+`"}`)
				if w.Code != http.StatusOK {
					t.Fatalf("save: %d %s", w.Code, w.Body)
				}
				s = newServiceOn(t, f, st)
				w, view := permissionSettingsRequest(t, s, "GET", "/api/v1/assistant", "assistant", "full", "")
				if w.Code != http.StatusOK || view.DefaultPermissionMode != mode || view.Main != "existing-main" {
					t.Fatalf("reload: %d %+v", w.Code, view)
				}
				session, err := s.Start(context.Background(), StartParams{Untitled: true})
				if err != nil {
					t.Fatal(err)
				}
				sent := f.sends[len(f.sends)-1]
				if sent["launcher"] != launcher || sent["permission_mode"] != mode || session.PermissionMode != mode {
					t.Fatalf("new %s session: %v %+v", launcher, sent, session)
				}
			})
		}
	}
}
