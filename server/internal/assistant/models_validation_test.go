package assistant

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Saving a machine-independent binding must use the same online launcher candidates
// as the model picker, including when only effort changes or a profile is applied.
func TestModelValidationOnlineMachine(t *testing.T) {
	for _, profile := range []bool{false, true} {
		name := "purpose"
		if profile {
			name = "profile"
		}
		t.Run(name, func(t *testing.T) {
			for _, tt := range []struct {
				name        string
				purpose     string
				allOffline  bool
				effortOnly  bool
				invalid     bool
				managed     bool
				wantMachine string
				wantError   bool
			}{
				{name: "offline first online second", wantMachine: "b"},
				{name: "effort only", effortOnly: true, wantMachine: "b"},
				{name: "all offline", allOffline: true},
				{name: "bound offline", purpose: "assistant", wantMachine: "a", wantError: true},
				{name: "online invalid model", invalid: true, wantMachine: "b", wantError: true},
				{name: "managed unbound", managed: true},
				{name: "managed bound offline", managed: true, purpose: "assistant", wantMachine: "a", wantError: true},
			} {
				t.Run(tt.name, func(t *testing.T) {
					s, f, st := modelService(t)
					ctx := t.Context()
					f.mu.Lock()
					f.hello.Assistant.MachineID = "a"
					f.hello.Assistant.Machines = []string{"a", "b"}
					f.devices = []Device{{ID: "a"}, {ID: "b", Online: !tt.allOffline, Reachable: !tt.allOffline}}
					f.sessions["a"], f.sessions["b"] = nil, nil
					f.offline["a"], f.offline["b"] = true, tt.allOffline
					f.launcherList = []proto.Launcher{{Name: "work", Source: "zshrc", Agent: "claude"}}
					f.agentModels = map[string][]proto.AgentModel{"work": {{ID: "selected"}}}
					f.mu.Unlock()
					if tt.managed {
						f.mu.Lock()
						f.launcherList = []proto.Launcher{}
						f.mu.Unlock()
						putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "work", Model: "default"}})
					} else if err := st.PutSetting(ctx, launchersZshrcKey, map[string][]proto.Launcher{
						"a": {{Name: "work", Source: "zshrc", Agent: "claude"}},
						"b": {{Name: "work", Source: "zshrc", Agent: "claude"}},
					}); err != nil {
						t.Fatal(err)
					}
					f.answerNode(assistantwire.DeviceLaunchersModelCheck, func(machine string, raw json.RawMessage) (any, error) {
						if f.offline[machine] {
							return nil, apierr.ErrOffline
						}
						var args struct{ Model string }
						if err := json.Unmarshal(raw, &args); err != nil {
							return nil, err
						}
						if args.Model == "invalid" {
							return nil, errors.New("unsupported model")
						}
						return map[string]any{"agent": "claude"}, nil
					})
					purpose := tt.purpose
					if purpose == "" {
						purpose = "agent.new"
					}
					binding := ModelBinding{Provider: "launcher.work", Model: "selected"}
					if tt.invalid {
						binding.Model = "invalid"
					}
					patch := map[string]any{"revision": 0, "provider": binding.Provider, "model": binding.Model}
					if tt.effortOnly {
						if err := st.PutSetting(ctx, modelRecordKey(purpose), modelRecord{Binding: binding}); err != nil {
							t.Fatal(err)
						}
						binding.Effort = "high"
						patch = map[string]any{"revision": 0, "effort": "high"}
					}
					// Warm the catalog before the write snapshot: launcher discovery may persist reports.
					providers := s.modelProviders(ctx)
					for _, p := range providers {
						if p.ID == binding.Provider && !tt.managed && !slices.Equal(p.Machines, []string{"a", "b"}) {
							t.Fatalf("fixture lost offline-first provider ordering: %v", p.Machines)
						}
					}
					if !tt.managed && !tt.allOffline && purpose == "agent.new" {
						r := request(s, "GET", "/api/v1/model-config/providers/launcher.work/models", "")
						var list struct{ Machine string }
						if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &list) != nil || list.Machine != "b" {
							t.Fatalf("model picker: %d %s", r.Code, r.Body.String())
						}
					}
					if profile {
						storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{purpose: binding}})
					}
					before, puts := profileStoreSnapshot(st.memStore)
					body, err := json.Marshal(patch)
					if err != nil {
						t.Fatal(err)
					}
					var r *httptest.ResponseRecorder
					if profile {
						r = request(s, "POST", "/api/v1/model-profiles/selected/apply", "")
					} else {
						r = modelRequest(s, "PATCH", purpose, string(body))
					}
					wantStatus := http.StatusOK
					if tt.wantError {
						wantStatus = http.StatusBadRequest
					}
					if r.Code != wantStatus {
						t.Errorf("save status %d, want %d: %s", r.Code, wantStatus, r.Body.String())
					}
					f.mu.Lock()
					var checked []string
					for _, call := range f.calls {
						if call.Method == assistantwire.DeviceLaunchersModelCheck {
							checked = append(checked, call.Device)
						}
					}
					f.mu.Unlock()
					var wantChecked []string
					if tt.wantMachine != "" {
						wantChecked = []string{tt.wantMachine}
					}
					if !slices.Equal(checked, wantChecked) {
						t.Errorf("model checks %v, want %v", checked, wantChecked)
					}
					if tt.wantError {
						requireProfileStoreUnchanged(t, st.memStore, before, puts)
					} else if got, err := s.modelRecord(ctx, purpose); err != nil || got.Binding != binding || got.Revision != 1 {
						t.Errorf("saved binding: %+v, %v; want %+v at revision 1", got, err, binding)
					}
					if profile {
						state, err := s.readModelProfileState(ctx)
						if err != nil || (!tt.wantError && state.Active != "selected") || (tt.wantError && state.Active != "") {
							t.Errorf("profile state: %+v %v", state, err)
						}
					}
				})
			}
		})
	}
}
