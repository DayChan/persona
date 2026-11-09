package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func profileStoreSnapshot(st *memStore) (map[string][]byte, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return maps.Clone(st.vals), st.puts
}

func requireProfileStoreUnchanged(t *testing.T, st *memStore, values map[string][]byte, puts int) {
	t.Helper()
	got, n := profileStoreSnapshot(st)
	if !reflect.DeepEqual(got, values) || n != puts {
		t.Fatalf("failed or no-op operation changed stored settings: writes %d -> %d", puts, n)
	}
}

func profilesOf(t *testing.T, s *Service) ModelProfilesView {
	t.Helper()
	v, err := s.modelProfilesView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func storeProfiles(t *testing.T, st *downStore, profiles ...ModelProfile) {
	t.Helper()
	if err := st.PutSetting(context.Background(), modelProfilesKey, modelProfiles{Revision: 1, Profiles: profiles}); err != nil {
		t.Fatal(err)
	}
}

func TestModelProfilesSeedAndCaptureCurrentConfiguration(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	sealed, err := s.sealKey("test-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "current", Model: "test-current", SmallModel: "test-small",
		SubagentModel: "test-subagent", OpusModel: "test-opus", SonnetModel: "test-sonnet", HaikuModel: "test-haiku",
		FableModel: "test-fable", Effort: "high", BaseURL: "https://example.invalid", Args: []string{"test-argument"}}, KeySealed: sealed}
	putLaunchers(t, s, l)
	for id, binding := range map[string]ModelBinding{
		"assistant":      {Provider: "launcher.current", Model: "test-chat", Runtime: proto.AssistantRuntimeAgentSDK},
		"agent.new":      {Provider: "launcher.current", Model: "test-agents"},
		"agent.delegate": {Model: "test-delegate"},
	} {
		if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 3, Binding: binding}); err != nil {
			t.Fatal(err)
		}
	}
	before, puts := profileStoreSnapshot(st.memStore)
	v := profilesOf(t, s)
	// The templates are copies of the configuration: performance is marked in use (its tiers work from the start),
	// nothing switches and there is nothing to roll back.
	if v.Revision != 1 || v.Active != seededProfile || v.Rollback != nil || len(v.Events) != 1 || v.Events[0].Action != "seed" ||
		v.Events[0].By.Kind != "system" || len(v.Profiles) != 2 {
		t.Fatalf("seeding: %+v", v)
	}
	for _, p := range v.Profiles {
		if p.Purposes["assistant"].Model != "test-chat" || p.Purposes["assistant"].Runtime != "" ||
			p.Purposes["agent.delegate"].Model != "test-delegate" || len(p.Changes) != 0 {
			t.Fatalf("template is not the current model-only configuration: %+v", p)
		}
		m := p.Launchers["current"]
		for _, field := range m.fields(&l.ManagedLauncher) {
			if *field.set == nil || **field.set != *field.launcher {
				t.Fatalf("capture lost launcher field %s", field.name)
			}
		}
		strong, standard := p.Tiers["strong"], p.Tiers["standard"]
		if strong.Description == "" || standard.Description == "" {
			t.Fatalf("seeded tiers say nothing of when to use them: %+v", p.Tiers)
		}
		strong.Description, standard.Description = "", ""
		if strong != (ModelTier{Launcher: "current", Model: "test-chat"}) || standard != (ModelTier{Launcher: "current", Model: "test-agents"}) {
			t.Fatalf("tiers are not the current configuration: %+v", p.Tiers)
		}
	}
	after, n := profileStoreSnapshot(st.memStore)
	delete(after, modelProfilesKey)
	delete(after, modelProfileStateKey)
	if !reflect.DeepEqual(before, after) || n != puts+2 { // the profiles and the state, in one transaction
		t.Fatal("seeding reset model defaults or changed another setting")
	}
	if f.eventTypes() != "" || len(f.managed) != 0 {
		t.Fatal("seeding caused switch side effects")
	}
	before, puts = profileStoreSnapshot(st.memStore)
	profilesOf(t, s)
	requireProfileStoreUnchanged(t, st.memStore, before, puts)

	// Capture changes only the selected profile, retaining its existing tier definitions.
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), modelRecord{Revision: 4,
		Binding: ModelBinding{Provider: "launcher.current", Model: "test-later", Runtime: proto.AssistantRuntimeAgentSDK}}); err != nil {
		t.Fatal(err)
	}
	customTiers := map[string]ModelTier{"custom": {Launcher: "current", Model: "test-custom"}}
	if _, err := s.saveModelProfile(ctx, nil, "performance", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
		p.Tiers = customTiers
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := request(s, "PUT", "/api/v1/model-profiles/performance", `{"revision":2,"name":"Captured","capture":true}`)
	if r.Code != http.StatusOK {
		t.Fatalf("capture: %d %s", r.Code, r.Body.String())
	}
	v = profilesOf(t, s)
	if v.Profiles[0].Purposes["assistant"].Model != "test-later" ||
		!reflect.DeepEqual(v.Profiles[0].Tiers, customTiers) || v.Profiles[1].Purposes["assistant"].Model != "test-chat" || v.Active != seededProfile {
		t.Fatalf("capture applied or overwrote other profile data: %+v", v)
	}
	if got, err := s.modelRecord(ctx, "assistant"); err != nil || got.Binding.Model != "test-later" || got.Revision != 4 {
		t.Fatalf("capture changed running configuration: %+v %v", got, err)
	}
}

func TestModelProfileSwitchRollbackPreservesSecretsAndRuntime(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	sealed, err := s.sealKey("test-key")
	if err != nil {
		t.Fatal(err)
	}
	headers, err := s.sealKey("X-Test: test-header-secret")
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.sealKey("TEST_SECRET=test-env-secret")
	if err != nil {
		t.Fatal(err)
	}
	l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", BaseURL: "https://example.invalid", Model: "test-original",
		SmallModel: "test-small", Effort: "low", Auth: "bearer", Args: []string{"test-argument"}, Proxy: "http://proxy.invalid",
		TimeoutMS: 123, MaxContext: 456, CompactWindow: 789, SubagentForce: true, Source: "test-source",
		CustomModel: "test-custom", CustomModelName: "Custom", CustomModelDescription: "Description",
		Models: []proto.ProviderModel{{ID: "test-original"}}}, KeySealed: sealed, HeadersSealed: headers, EnvSealed: env,
		Preset: "test-preset", DisplayName: "Current gateway", Created: time.Now().UTC()}
	putLaunchers(t, s, l)
	original := modelRecord{Revision: 7, Binding: ModelBinding{Provider: "launcher.gw", Model: "test-chat", Runtime: proto.AssistantRuntimeAgentSDK}}
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), original); err != nil {
		t.Fatal(err)
	}
	storeProfiles(t, st, ModelProfile{ID: "economy", Name: "Economy", Purposes: map[string]ModelBinding{"assistant": {Model: "test-selected"}},
		Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-economy"), SmallModel: profileValue(""), Effort: profileValue("high")}}})
	// Make the assistant's inherited provider match the existing explicit binding.
	setSettings(t, s, func(p *Settings) { p.Launcher = "gw" })
	by := ProfileActor{Kind: "device", Name: "Test device"}
	sw, err := s.applyModelProfile(ctx, "economy", by)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sw.Changed, []string{"assistant", "gw.model", "gw.small_model", "gw.effort"}) {
		t.Fatalf("changes: %+v", sw)
	}
	r, err := s.modelRecord(ctx, "assistant")
	if err != nil || r.Revision != 8 || r.Binding != (ModelBinding{Model: "test-selected", Runtime: proto.AssistantRuntimeAgentSDK}) {
		t.Fatalf("switch lost model-only inheritance or runtime: %+v %v", r, err)
	}
	got := s.storedLaunchers(ctx)[0]
	want := l
	want.Model, want.SmallModel, want.Effort, want.Updated = "test-economy", "", "high", got.Updated
	if !reflect.DeepEqual(got, want) {
		t.Fatal("switch changed credentials or non-model launcher fields")
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil || state.Previous.Purposes["assistant"] != original.Binding || len(state.Events) != 1 || state.By != by {
		t.Fatalf("switch did not save history and rollback: %+v %v", state, err)
	}
	if f.eventTypes() != "model_profile" || len(f.managed["server"]) != 1 {
		t.Fatalf("switch was not published: %s", f.eventTypes())
	}

	// Repeating the same switch must preserve the rollback, cached context and preferences.
	before, puts := profileStoreSnapshot(st.memStore)
	cached := &prompts.Snapshot{Now: time.Now(), Zone: "UTC"}
	s.live.snap, s.live.at, s.prefs.at = cached, time.Now(), time.Now()
	cacheAt, prefsAt := s.live.at, s.prefs.at
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if sw, err := s.applyModelProfile(ctx, "economy", by); err != nil || len(sw.Changed) != 0 {
		t.Fatalf("repeat switch: %+v %v", sw, err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if s.live.snap != cached || s.live.at != cacheAt || s.prefs.at != prefsAt || f.eventTypes() != "model_profile" {
		t.Fatal("no-op switch invalidated caches or broadcast an event")
	}
	f.mu.Lock()
	for _, call := range f.calls[calls:] {
		if call.Method == assistantwire.DeviceLaunchersManaged || call.Method == assistantwire.DeviceLaunchersModelCheck {
			f.mu.Unlock()
			t.Fatal("no-op switch pushed or checked launcher models")
		}
	}
	f.mu.Unlock()

	// Runtime and credentials edited after the switch must survive rollback and redo.
	r.Binding.Runtime = ""
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), r); err != nil {
		t.Fatal(err)
	}
	rotated, err := s.sealKey("rotated-test-key")
	if err != nil {
		t.Fatal(err)
	}
	got.KeySealed, got.Proxy, got.DisplayName = rotated, "http://later-proxy.invalid", "Later display name"
	putLaunchers(t, s, got)
	for _, redo := range []bool{false, true} {
		if _, err := s.rollbackModelProfile(ctx, by); err != nil {
			t.Fatal(err)
		}
		r, err = s.modelRecord(ctx, "assistant")
		if err != nil || r.Binding.Runtime != "" {
			t.Fatalf("rollback restored obsolete runtime: %+v %v", r, err)
		}
		wantBinding, wantModel := original.Binding, l.Model
		wantBinding.Runtime = ""
		if redo {
			wantBinding, wantModel = ModelBinding{Model: "test-selected"}, "test-economy"
		}
		if r.Binding != wantBinding {
			t.Fatalf("rollback/redo binding: %+v", r.Binding)
		}
		rolled := s.storedLaunchers(ctx)[0]
		if rolled.Model != wantModel || rolled.KeySealed != rotated || rolled.HeadersSealed != headers || rolled.EnvSealed != env ||
			rolled.Proxy != "http://later-proxy.invalid" || rolled.DisplayName != "Later display name" {
			t.Fatal("rollback lost current credentials or non-model fields")
		}
	}
	view := profilesOf(t, s)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{sealed, rotated, headers, env, "rotated-test-key", "test-header-secret", "test-env-secret", "key_sealed", "headers_sealed", "env_sealed", "base_url", "test-argument", "test-preset"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("profile response contains credentials or non-model data: %q", secret)
		}
	}
	if view.Active != "economy" || len(view.Events) != 3 || view.Events[0].Action != "rollback" {
		t.Fatalf("rollback/redo history: %+v", view)
	}
}

func TestModelProfilesInheritedOverridesAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		b         ModelBinding
		api       string
		wantError bool
	}{
		{"inherited model", ModelBinding{Model: "test-selected"}, "responses", false},
		{"inherited effort", ModelBinding{Effort: "high"}, "responses", false},
		{"messages effort", ModelBinding{Effort: "high"}, "messages", true},
		{"missing provider", ModelBinding{Provider: "launcher.missing", Model: "test-selected"}, "responses", true},
		{"bad effort", ModelBinding{Effort: "invalid"}, "responses", true},
		{"runtime", ModelBinding{Runtime: proto.AssistantRuntimeAgentSDK}, "responses", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, f, st := modelService(t)
			ctx := context.Background()
			f.hello.Models.AI = ai.Config{API: tt.api, BaseURL: "https://example.invalid", APIKey: "test-key", Model: "test-original"}
			storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"inbox.triage": tt.b}})
			before, puts := profileStoreSnapshot(st.memStore)
			_, err := s.applyModelProfile(ctx, "selected", ProfileActor{Kind: "hub"})
			if (err != nil) != tt.wantError {
				t.Fatalf("switch error: %v", err)
			}
			if tt.wantError {
				requireProfileStoreUnchanged(t, st.memStore, before, puts)
				if f.eventTypes() != "" {
					t.Fatal("failed validation broadcast an event")
				}
			} else if r, err := s.modelRecord(ctx, "inbox.triage"); err != nil || r.Binding != tt.b {
				t.Fatalf("model-only override: %+v %v", r, err)
			}
		})
	}
}

// profileFailureStore fails one read or the entire atomic batch, without bypassing memStore's atomic behavior.
type profileFailureStore struct {
	*memStore
	readKey    string
	writeError error
	readError  error
	batches    int
}

func (st *profileFailureStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == st.readKey {
		return false, st.readError
	}
	return st.memStore.Setting(ctx, key, out)
}

func (st *profileFailureStore) PutSettings(ctx context.Context, values map[string]any) error {
	st.batches++
	if st.writeError != nil {
		return st.writeError
	}
	return st.memStore.PutSettings(ctx, values)
}

// Embedding only Store deliberately hides PutSettings, like a store without transaction support.
type nonAtomicProfileStore struct{ Store }

func TestModelProfilesFailuresAreAtomicAndPropagated(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"view", "capture", "switch", "rollback", "save", "delete"} {
		for _, failure := range []string{modelProfilesKey, modelProfileStateKey, launchersManagedKey, modelRecordKey("assistant"), "write", "non-atomic"} {
			if operation == "rollback" && failure == modelProfilesKey || operation == "delete" && (failure == launchersManagedKey || failure == modelRecordKey("assistant")) ||
				(operation == "save" || operation == "delete") && failure == modelRecordKey("assistant") || operation == "capture" && (failure == modelProfilesKey || failure == modelProfileStateKey || failure == "write" || failure == "non-atomic") {
				continue
			}
			t.Run(operation+"/"+failure, func(t *testing.T) {
				s, f, st := modelService(t)
				f.hello.Models.AI = ai.Config{API: "responses", BaseURL: "https://example.invalid", APIKey: "test-key", Model: "test-original"}
				putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "test-original"}})
				storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"assistant": {Model: "test-selected"}},
					Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-selected")}}})
				if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "another", Previous: &modelProfileImage{Profile: "before",
					Purposes: map[string]ModelBinding{"assistant": {Model: "test-before"}}, Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-before")}}}}); err != nil {
					t.Fatal(err)
				}
				failed := errors.New("test database failure")
				failing := &profileFailureStore{memStore: st.memStore, readError: failed}
				s.store = func() Store { return failing }
				switch failure {
				case "write":
					failing.writeError = failed
				case "non-atomic":
					s.store = func() Store { return nonAtomicProfileStore{Store: failing} }
				default:
					failing.readKey = failure
				}
				// View reads existing profiles, so explicitly force seeding for atomic-write failures.
				if operation == "view" && (failure == "write" || failure == "non-atomic") {
					st.memStore.mu.Lock()
					delete(st.vals, modelProfilesKey)
					st.memStore.mu.Unlock()
				}
				before, puts := profileStoreSnapshot(st.memStore)
				cached := &prompts.Snapshot{Now: time.Now(), Zone: "UTC"}
				s.live.snap = cached
				var err error
				switch operation {
				case "view":
					_, err = s.modelProfilesView(ctx)
				case "capture":
					_, _, _, err = s.captureModels(ctx)
				case "switch":
					_, err = s.applyModelProfile(ctx, "selected", ProfileActor{})
				case "rollback":
					_, err = s.rollbackModelProfile(ctx, ProfileActor{})
				case "save":
					_, err = s.saveModelProfile(ctx, nil, "selected", ProfileActor{}, func(p *ModelProfile, _ bool) error { p.Name = "Edited"; return nil })
				case "delete":
					err = s.deleteModelProfile(ctx, nil, "selected", ProfileActor{})
				}
				want := failed
				if failure == "non-atomic" {
					want = errModelProfileAtomicStore
				}
				if !errors.Is(err, want) {
					t.Fatalf("failure not propagated: %v, want %v", err, want)
				}
				requireProfileStoreUnchanged(t, st.memStore, before, puts)
				if s.live.snap != cached || f.eventTypes() != "" || len(f.managed) != 0 {
					t.Fatal("failed operation invalidated the cache or pushed configuration")
				}
			})
		}
	}
}

func TestModelProfileMissingLauncherRejectedBeforeTransaction(t *testing.T) {
	for _, scope := range []string{"launcher fields", "purpose provider"} {
		for _, rollback := range []bool{false, true} {
			t.Run(scope+"/"+map[bool]string{false: "switch", true: "rollback"}[rollback], func(t *testing.T) {
				s, f, st := modelService(t)
				ctx := context.Background()
				purposes := map[string]ModelBinding{"assistant": {Model: "test-before"}}
				launchers := map[string]LauncherModels{"missing": {Model: profileValue("test-model")}}
				if scope == "purpose provider" {
					purposes["assistant"] = ModelBinding{Provider: "launcher.missing", Model: "test-before"}
					launchers = nil
				}
				storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: purposes, Launchers: launchers})
				if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "selected", Previous: &modelProfileImage{
					Profile: "before", Purposes: purposes, Launchers: launchers}}); err != nil {
					t.Fatal(err)
				}
				fs := &profileFailureStore{memStore: st.memStore}
				s.store = func() Store { return fs }
				before, puts := profileStoreSnapshot(st.memStore)
				var err error
				if rollback {
					_, err = s.rollbackModelProfile(ctx, ProfileActor{})
				} else {
					_, err = s.applyModelProfile(ctx, "selected", ProfileActor{})
				}
				if err == nil || fs.batches != 0 {
					t.Fatalf("missing launcher was not rejected before writing: %v, batches %d", err, fs.batches)
				}
				requireProfileStoreUnchanged(t, st.memStore, before, puts)
				if f.eventTypes() != "" {
					t.Fatal("failed operation broadcast")
				}
			})
		}
	}
}

func TestModelProfilesRoutesRolesRevisionsAndLocales(t *testing.T) {
	s, _, st := modelService(t)
	profilesOf(t, s)
	for _, path := range []string{"GET /api/v1/model-profiles", "PUT /api/v1/model-profiles/{id}", "DELETE /api/v1/model-profiles/{id}",
		"POST /api/v1/model-profiles/{id}/apply", "POST /api/v1/model-profiles/rollback"} {
		if !slices.Contains(s.Routes(), path) {
			t.Fatalf("unregistered route: %s", path)
		}
	}
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", "/api/v1/model-profiles/custom", `{"name":"Custom"}`, 400},
		{"PUT", "/api/v1/model-profiles/custom", `{"revision":0,"name":"Custom"}`, 409},
		{"PUT", "/api/v1/model-profiles/custom", `{"revision":1,"name":"Custom","api_key":"secret"}`, 400},
		{"PUT", "/api/v1/model-profiles/rollback", `{"revision":1,"name":"Invalid"}`, 400},
		{"POST", "/api/v1/model-profiles/missing/apply", "", 404},
		{"POST", "/api/v1/model-profiles/rollback", "", 409},
	} {
		before, puts := profileStoreSnapshot(st.memStore)
		r := request(s, tt.method, tt.path, tt.body)
		if r.Code != tt.status {
			t.Fatalf("%s %s: %d %s", tt.method, tt.path, r.Code, r.Body.String())
		}
		requireProfileStoreUnchanged(t, st.memStore, before, puts)
	}
	for _, lang := range i18n.Langs {
		for _, method := range []string{"PUT", "DELETE", "POST"} {
			path := "/api/v1/model-profiles/performance"
			if method == "POST" {
				path += "/apply"
			}
			res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: method, Path: path,
				Header: map[string][]string{RoleHeader: {"answer"}, "Accept-Language": {string(lang)}},
				Body:   json.RawMessage(`{"revision":1,"name":"Edited"}`)}, s.lang)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != http.StatusForbidden || !strings.Contains(string(res.Body), i18n.T(lang, "assistant-model_profiles.err_role")) {
				t.Fatalf("role or locale %s: %d %s", lang, res.Status, res.Body)
			}
		}
	}
	if r := request(s, "PUT", "/api/v1/model-profiles/custom", `{"revision":1,"name":"Custom","purposes":{"agent.new":{"model":"test-inherited"}}}`); r.Code != 200 {
		t.Fatalf("saving inherited model-only override: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "DELETE", "/api/v1/model-profiles/custom?revision=1", ""); r.Code != 409 {
		t.Fatalf("stale delete: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "DELETE", "/api/v1/model-profiles/custom?revision=2", ""); r.Code != 200 {
		t.Fatalf("delete: %d %s", r.Code, r.Body.String())
	}
}

func TestModelProfileInitialFailureDoesNotSeedTemplates(t *testing.T) {
	s, _, st := modelService(t)
	before, puts := profileStoreSnapshot(st.memStore)
	if _, err := s.applyModelProfile(context.Background(), "missing", ProfileActor{}); !errors.Is(err, errModelProfileNotFound) {
		t.Fatal(err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if _, err := s.saveModelProfile(context.Background(), nil, "bad", ProfileActor{}, func(p *ModelProfile, _ bool) error { return nil }); err == nil {
		t.Fatal("invalid profile was accepted")
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
}

func TestModelProfileAtomicMemoryStoreAndDownStore(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	if err := st.PutSetting(ctx, "original", "before"); err != nil {
		t.Fatal(err)
	}
	before, puts := profileStoreSnapshot(st)
	if err := st.PutSettings(ctx, map[string]any{"original": "after", "invalid": make(chan int)}); err == nil {
		t.Fatal("invalid JSON value was accepted")
	}
	requireProfileStoreUnchanged(t, st, before, puts)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := st.PutSettings(cancelled, map[string]any{"original": "after"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	requireProfileStoreUnchanged(t, st, before, puts)
	down := &downStore{memStore: st}
	down.failing(true)
	if err := down.PutSettings(ctx, map[string]any{"original": "after"}); err == nil {
		t.Fatal("downStore bypassed atomic failure")
	}
	requireProfileStoreUnchanged(t, st, before, puts)
	down.failing(false)
	if err := down.PutSettings(ctx, map[string]any{"original": "after", "second": true}); err != nil {
		t.Fatal(err)
	}
	if values, n := profileStoreSnapshot(st); len(values) != 2 || n != puts+2 {
		t.Fatal("atomic batch was not committed")
	}
}

func TestModelProfileRollbackNoOpPreservesPreviousAndCaches(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	binding := ModelBinding{Model: "test-current", Runtime: proto.AssistantRuntimeAgentSDK}
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), modelRecord{Revision: 4, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "test-current"}})
	if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "selected", Previous: &modelProfileImage{
		Profile: "selected", Purposes: map[string]ModelBinding{"assistant": {Model: "test-current"}},
		Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-current")}}}}); err != nil {
		t.Fatal(err)
	}
	cached := &prompts.Snapshot{Now: time.Now(), Zone: "UTC"}
	s.live.snap, s.live.at, s.prefs.at = cached, time.Now(), time.Now()
	cacheAt, prefsAt := s.live.at, s.prefs.at
	before, puts := profileStoreSnapshot(st.memStore)
	if sw, err := s.rollbackModelProfile(ctx, ProfileActor{}); err != nil || len(sw.Changed) != 0 {
		t.Fatalf("no-op rollback: %+v %v", sw, err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if s.live.snap != cached || s.live.at != cacheAt || s.prefs.at != prefsAt || f.eventTypes() != "" || len(f.managed) != 0 {
		t.Fatal("no-op rollback invalidated caches or pushed an event")
	}
}

func TestModelProfileCaptureDirectAPIAndUnrelatedLaunchers(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Kind: "api", Name: "direct", Model: "test-direct", BaseURL: "https://example.invalid", Effort: "high"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "unused", Model: "test-unused"}})
	if err := s.saveModel(ctx, "kb", modelRecord{Binding: ModelBinding{Provider: "launcher.direct"}}); err != nil {
		t.Fatal(err)
	}
	_, launchers, _, err := s.captureModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(launchers) != 1 || launchers["direct"].Model == nil || *launchers["direct"].Model != "test-direct" ||
		launchers["direct"].Effort != nil || launchers["direct"].SmallModel != nil {
		t.Fatalf("capture included unsupported API or unrelated launcher fields: %+v", launchers)
	}
}

func TestModelProfileUnrelatedDefaultsStayIntact(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	f.hello.Models.AI = ai.Config{API: "responses", BaseURL: "https://example.invalid", APIKey: "test-key", Model: "test-default"}
	for id, b := range map[string]ModelBinding{
		"assistant":      {Provider: "launcher.claude", Model: "test-current-sol", Runtime: proto.AssistantRuntimeAgentSDK},
		"agent.new":      {Provider: "launcher.claude", Model: "test-new-sol"},
		"agent.delegate": {Model: "test-delegate"},
	} {
		if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 9, Binding: b}); err != nil {
			t.Fatal(err)
		}
	}
	storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"kb": {Model: "test-kb"}}})
	before, _ := profileStoreSnapshot(st.memStore)
	for _, rollback := range []bool{false, true} {
		var err error
		if rollback {
			_, err = s.rollbackModelProfile(ctx, ProfileActor{})
		} else {
			_, err = s.applyModelProfile(ctx, "selected", ProfileActor{})
		}
		if err != nil {
			t.Fatal(err)
		}
		after, _ := profileStoreSnapshot(st.memStore)
		for _, id := range []string{"assistant", "agent.new", "agent.delegate"} {
			if string(before[modelRecordKey(id)]) != string(after[modelRecordKey(id)]) {
				t.Fatalf("partial profile reset %s's default", id)
			}
		}
	}
}

func TestModelProfileCannotClearTriageWhilePluginsNeedIt(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	f.needTriage = []string{"Test inbox plugin"}
	if err := st.PutSetting(ctx, modelRecordKey("inbox.triage"), modelRecord{Binding: ModelBinding{Provider: "launcher.current", Model: "test-current"}}); err != nil {
		t.Fatal(err)
	}
	storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"inbox.triage": {}}})
	before, puts := profileStoreSnapshot(st.memStore)
	if _, err := s.applyModelProfile(ctx, "selected", ProfileActor{}); err == nil || !strings.Contains(err.Error(), "Test inbox plugin") {
		t.Fatalf("triage safety check was bypassed: %v", err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
}

// recordReadStore counts the reads of each category's record (modelRecordKey) on top of a downStore.
type recordReadStore struct {
	*downStore
	mu    sync.Mutex
	reads map[string]int // category id -> reads
}

func (st *recordReadStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if i := slices.IndexFunc(modelCategoryIDs, func(id string) bool { return modelRecordKey(id) == key }); i >= 0 {
		st.mu.Lock()
		st.reads[modelCategoryIDs[i]]++
		st.mu.Unlock()
	}
	return st.downStore.Setting(ctx, key, out)
}

// takeReads is the reads counted since the last call, by category id.
func (st *recordReadStore) takeReads() map[string]int {
	st.mu.Lock()
	defer st.mu.Unlock()
	reads := st.reads
	st.reads = map[string]int{}
	return reads
}

// countRecordReads makes s read its settings through a recordReadStore on st.
func countRecordReads(s *Service, st *downStore) *recordReadStore {
	counter := &recordReadStore{downStore: st, reads: map[string]int{}}
	s.store = func() Store { return counter }
	return counter
}

// storeManyProfiles stores a record for every category and n profiles that each name every category: the even ones
// as the records are (the main chat's runtime aside), the odd ones with other kb and agent.delegate choices. It
// returns the Changes each profile's view shows.
func storeManyProfiles(tb testing.TB, st *downStore, n int) [][]string {
	tb.Helper()
	ctx := context.Background()
	records := map[string]ModelBinding{
		"assistant":      {Provider: "launcher.current", Model: "test-chat", Runtime: proto.AssistantRuntimeAgentSDK},
		"agent.new":      {Provider: "launcher.current", Model: "test-agents"},
		"kb":             {Model: "test-kb"},
		"inbox.triage":   {Effort: "high"},
		"agent.delegate": {Model: "test-delegate"},
	}
	for id, b := range records {
		if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 2, Binding: b}); err != nil {
			tb.Fatal(err)
		}
	}
	profiles, want := make([]ModelProfile, n), make([][]string, n)
	for i := range profiles {
		purposes := maps.Clone(records)
		purposes["assistant"] = ModelBinding{Provider: "launcher.current", Model: "test-chat"}
		want[i] = []string{}
		if i%2 == 1 {
			purposes["kb"], purposes["agent.delegate"] = ModelBinding{Model: "test-kb-other"}, ModelBinding{}
			want[i] = []string{"kb", "agent.delegate"}
		}
		profiles[i] = ModelProfile{ID: fmt.Sprintf("profile-%d", i), Name: fmt.Sprintf("Profile %d", i), Purposes: purposes}
	}
	if err := st.PutSetting(ctx, modelProfilesKey, modelProfiles{Revision: 1, Profiles: profiles}); err != nil {
		tb.Fatal(err)
	}
	return want
}

// The view compares every profile with the configuration, but reads each category's record once, not once per
// profile (P x 5 point reads under models.mu before).
func TestModelProfilesViewReadsEachRecordOnce(t *testing.T) {
	for _, n := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("%d profiles", n), func(t *testing.T) {
			s, _, st := modelService(t)
			want := storeManyProfiles(t, st, n)
			counter := countRecordReads(s, st)
			v := profilesOf(t, s)
			if reads := counter.takeReads(); len(reads) != len(modelCategoryIDs) || slices.ContainsFunc(modelCategoryIDs, func(id string) bool { return reads[id] != 1 }) {
				t.Fatalf("record reads = %v, want each of %v once", reads, modelCategoryIDs)
			}
			if len(v.Profiles) != n {
				t.Fatalf("profiles = %d, want %d", len(v.Profiles), n)
			}
			for i, p := range v.Profiles {
				if !reflect.DeepEqual(p.Changes, want[i]) {
					t.Fatalf("%s changes = %v, want %v", p.ID, p.Changes, want[i])
				}
			}
		})
	}

	// A category no profile names is not read.
	s, _, st := modelService(t)
	storeProfiles(t, st, ModelProfile{ID: "one", Name: "One", Purposes: map[string]ModelBinding{"kb": {Model: "test-kb"}}},
		ModelProfile{ID: "two", Name: "Two", Purposes: map[string]ModelBinding{"kb": {}, "assistant": {Model: "test-chat"}}})
	counter := countRecordReads(s, st)
	v := profilesOf(t, s)
	if reads := counter.takeReads(); !maps.Equal(reads, map[string]int{"assistant": 1, "kb": 1}) {
		t.Fatalf("record reads = %v, want assistant and kb once", reads)
	}
	if !reflect.DeepEqual(v.Profiles[0].Changes, []string{"kb"}) || !reflect.DeepEqual(v.Profiles[1].Changes, []string{"assistant"}) {
		t.Fatalf("changes = %v, %v", v.Profiles[0].Changes, v.Profiles[1].Changes)
	}

	// Seeding captures every record once for the templates (inbox.triage once more: agent.delegate's default is resolved
	// from its connection), then the view reads each once to compare the two templates with it.
	s, _, st = modelService(t)
	counter = countRecordReads(s, st)
	capture := map[string]int{"assistant": 1, "agent.new": 1, "kb": 1, "inbox.triage": 2, "agent.delegate": 1}
	if _, _, _, err := s.captureModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads := counter.takeReads(); !maps.Equal(reads, capture) {
		t.Fatalf("capture record reads = %v, want %v", reads, capture)
	}
	if v := profilesOf(t, s); len(v.Profiles) != 2 || len(v.Profiles[0].Changes) != 0 || len(v.Profiles[1].Changes) != 0 {
		t.Fatalf("seeded view: %+v", v.Profiles)
	}
	seed := maps.Clone(capture)
	for id := range seed {
		seed[id]++
	}
	if reads := counter.takeReads(); !maps.Equal(reads, seed) {
		t.Fatalf("seeding view record reads = %v, want %v", reads, seed)
	}
}

// BenchmarkModelProfilesView is GET /api/v1/model-profiles' view on memStore with P stored profiles that each name
// every category; record-reads/op is the category record reads it makes.
func BenchmarkModelProfilesView(b *testing.B) {
	for _, n := range []int{2, 4, 8} {
		b.Run(fmt.Sprintf("profiles=%d", n), func(b *testing.B) {
			st := &downStore{memStore: &memStore{}}
			storeManyProfiles(b, st, n)
			s := New(Options{Hub: newFakeHub(), Store: func() Store { return st }})
			counter := countRecordReads(s, st)
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.modelProfilesView(ctx); err != nil {
					b.Fatal(err)
				}
			}
			reads := 0
			for _, r := range counter.takeReads() {
				reads += r
			}
			b.ReportMetric(float64(reads)/float64(b.N), "record-reads/op")
		})
	}
}
