package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Only managed-provider reads fail; writes and unrelated settings remain available.
// A selected read fails once, so an unchecked reread can appear healthy again.
type providerReadFailureStore struct {
	*memStore
	faultMu           sync.Mutex
	reads, failAt     int
	afterWrite, wrote bool
}

func (st *providerReadFailureStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == launchersManagedKey {
		st.faultMu.Lock()
		st.reads++
		fail := st.failAt == st.reads || st.failAt < 0 || st.afterWrite && st.wrote
		st.faultMu.Unlock()
		if fail {
			return false, errors.New("synthetic managed-provider read failure")
		}
	}
	return st.memStore.Setting(ctx, key, out)
}

func (st *providerReadFailureStore) PutSetting(ctx context.Context, key string, value any) error {
	err := st.memStore.PutSetting(ctx, key, value)
	if err == nil && key == launchersManagedKey {
		st.faultMu.Lock()
		st.wrote = true
		st.faultMu.Unlock()
	}
	return err
}

func (st *providerReadFailureStore) recover() {
	st.faultMu.Lock()
	defer st.faultMu.Unlock()
	st.failAt, st.afterWrite = 0, false
}

func providerSettings(st *memStore) (map[string][]byte, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	result := maps.Clone(st.vals)
	for key, value := range result {
		result[key] = bytes.Clone(value)
	}
	return result, st.puts
}

func providerPushes(f *fakeHub) []DeviceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []DeviceCall
	for _, call := range f.calls {
		if call.Method == assistantwire.DeviceLaunchersManaged {
			result = append(result, call)
		}
	}
	return result
}

// Keep alias discovery on the real service-to-device call path, without contacting a node.
type providerReadHub struct{ *fakeHub }

func (h providerReadHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method == assistantwire.DeviceLaunchersZshrc {
		return json.Marshal(map[string]any{"aliases": importAliases})
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

// Both API and runnable providers are seeded through the real handlers. Every secret is synthetic.
// Only the launcher has custom headers: direct API providers reject that unsupported setting.
func providerReadFixture(t *testing.T) (*Service, *fakeHub, *providerReadFailureStore) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.ownModels = true
	st := &downStore{memStore: &memStore{}}
	s := withSettings(t, f, New(Options{Hub: providerReadHub{f}, Store: func() Store { return st }, Logf: testLogf(t)}))
	for _, body := range []string{
		`{"name":"old_api","kind":"api","api":"responses","base_url":"https://api.example.com/v1","model":"original","api_key":"synthetic-api-key"}`,
		`{"name":"old_launcher","base_url":"https://gw.example.com","model":"original","api_key":"sk-test-import-a","headers":"X-Test: synthetic-launcher-header","env":"X_TEST_TOKEN=synthetic-env","args":["--verbose"],"effort":"high"}`,
	} {
		if r := request(s, "POST", "/api/v1/launchers", body); r.Code != http.StatusOK {
			t.Fatalf("seed: %d %s", r.Code, r.Body)
		}
	}
	// Keep API fallback purposes on the surviving gateway, so deletion tests exercise an unused provider.
	for _, id := range []string{"kb", "inbox.triage"} {
		if err := st.memStore.PutSetting(context.Background(), modelRecordKey(id), modelRecord{Revision: 1, Binding: ModelBinding{Provider: "launcher.old_launcher"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Settle display caches before counting mutation effects.
	s.launcherViews(context.Background())
	fault := &providerReadFailureStore{memStore: st.memStore}
	s.store = func() Store { return fault }
	return s, f, fault
}

func storedProviders(t *testing.T, st *memStore) []storedLauncher {
	t.Helper()
	var list []storedLauncher
	if _, err := st.Setting(context.Background(), launchersManagedKey, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

const providerCreateBody = `{"name":"replacement","kind":"api","api":"responses","base_url":"https://new.example.com/v1","model":"new","api_key":"synthetic-new-key","assign":["agent.new"]}`
const providerEditBody = `{"kind":"api","api":"responses","base_url":"https://api.example.com/v1","model":"edited"}`
const providerImportBody = `{"machine":"server","providers":["gcc","kcc"]}`

func TestProviderReadFailureRejectsMutationAndRecovers(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		failAt                   int
	}{
		{"create initial", "POST", "/api/v1/launchers", providerCreateBody, 1},
		{"create locked", "POST", "/api/v1/launchers", providerCreateBody, 2},
		{"edit initial", "PUT", "/api/v1/launchers/old_api", providerEditBody, 1},
		{"edit locked", "PUT", "/api/v1/launchers/old_api", providerEditBody, 2},
		{"delete managed", "DELETE", "/api/v1/launchers/old_api", "", 1},
		{"delete shell fallback", "DELETE", "/api/v1/launchers/shell_example", "", 1},
		{"import preview", "GET", "/api/v1/launchers/import?machine=server", "", 1},
		{"import initial", "POST", "/api/v1/launchers/import", providerImportBody, 1},
		{"import locked", "POST", "/api/v1/launchers/import", providerImportBody, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, st := providerReadFixture(t)
			before, writes := providerSettings(st.memStore)
			sealed := storedProviders(t, st.memStore)
			pushes := providerPushes(f)
			st.failAt = tc.failAt
			r := request(s, tc.method, tc.path, tc.body)
			if r.Code != http.StatusInternalServerError {
				t.Errorf("failed read returned %d: %s", r.Code, r.Body)
			}
			if st.reads < tc.failAt {
				t.Fatalf("selected read %d was never reached (reads %d)", tc.failAt, st.reads)
			}
			after, afterWrites := providerSettings(st.memStore)
			if !reflect.DeepEqual(before, after) || writes != afterWrites {
				t.Error("rejected read changed durable settings or attempted a write")
			}
			if !reflect.DeepEqual(sealed, storedProviders(t, st.memStore)) {
				t.Error("complete sealed provider records changed")
			}
			if !reflect.DeepEqual(pushes, providerPushes(f)) {
				t.Error("rejected read published node configuration")
			}
			st.recover()
			r = request(s, tc.method, tc.path, tc.body)
			if r.Code != http.StatusOK {
				t.Fatalf("healthy retry: %d %s", r.Code, r.Body)
			}
			got := storedProviders(t, st.memStore)
			if tc.method == "PUT" {
				want := sealed[0]
				want.Model = "edited"
				want.Updated = got[0].Updated
				if !reflect.DeepEqual(want, got[0]) || !reflect.DeepEqual(sealed[1], got[1]) {
					t.Error("edit retry failed to preserve sealed credentials and unrelated fields")
				}
			} else if tc.name == "delete managed" {
				if !reflect.DeepEqual(got, sealed[1:]) {
					t.Error("delete retry changed the surviving provider")
				}
			} else if tc.method == "POST" && tc.path == "/api/v1/launchers" {
				if len(got) != 3 || !reflect.DeepEqual(got[:2], sealed) {
					t.Error("create retry did not retain both original providers")
				}
				if !bytes.Contains(r.Body.Bytes(), []byte(`"reason":"incompatible"`)) {
					t.Error("Responses provider was not kept out of launcher assignments")
				}
			} else if tc.method == "POST" {
				if len(got) != 3 {
					t.Fatalf("import retained %d providers, want three", len(got))
				}
				want := sealed[1]
				want.Updated = got[1].Updated
				want.Models = []proto.ProviderModel{{ID: "glm-5"}, {ID: "glm-air"}}
				if !reflect.DeepEqual(got[0], sealed[0]) || !reflect.DeepEqual(got[1], want) {
					t.Error("import changed existing sealed records beyond merged models")
				}
			} else if !reflect.DeepEqual(got, sealed) {
				t.Error("preview or shell hide changed managed providers")
			}
		})
	}
}

func TestProviderReadFailureAfterCommitPublishesConfirmedSnapshot(t *testing.T) {
	for _, tc := range []struct{ name, method, path, body string }{
		{"create", "POST", "/api/v1/launchers", providerCreateBody},
		{"edit", "PUT", "/api/v1/launchers/old_api", providerEditBody},
		{"delete", "DELETE", "/api/v1/launchers/old_api", ""},
		{"import", "POST", "/api/v1/launchers/import", providerImportBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, st := providerReadFixture(t)
			count := len(providerPushes(f))
			st.afterWrite = true
			r := request(s, tc.method, tc.path, tc.body)
			if r.Code != http.StatusOK {
				t.Fatalf("commit: %d %s", r.Code, r.Body)
			}
			pushes := providerPushes(f)
			if len(pushes) != count+1 {
				t.Fatalf("got %d new pushes, want one", len(pushes)-count)
			}
			var payload struct {
				Launchers []proto.ManagedLauncher `json:"launchers"`
			}
			if err := json.Unmarshal(pushes[len(pushes)-1].Params, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Launchers) != 1 && tc.name != "import" {
				t.Fatalf("confirmed runnable provider was cleared: %+v", payload.Launchers)
			}
			var want []proto.ManagedLauncher
			for _, l := range storedProviders(t, st.memStore) {
				if l.Kind == "api" && l.API == "responses" {
					continue
				}
				m := l.ManagedLauncher
				var err error
				if m.APIKey, err = s.openKey(l.KeySealed); err != nil {
					t.Fatal(err)
				}
				if m.Headers, err = s.openKey(l.HeadersSealed); err != nil {
					t.Fatal(err)
				}
				if m.Env, err = s.openKey(l.EnvSealed); err != nil {
					t.Fatal(err)
				}
				m.HasHeaders, m.HasEnv = m.Headers != "", m.Env != ""
				want = append(want, m)
			}
			if !reflect.DeepEqual(want, payload.Launchers) {
				t.Error("published providers differ from the complete committed snapshot")
			}
			// Profile notifications and trust changes must also retain the last good configuration.
			s.pushManaged(context.Background(), "")
			if len(providerPushes(f)) != len(pushes) {
				t.Error("failed post-commit read published a replacement")
			}
			st.recover()
			s.pushManaged(context.Background(), "")
			if len(providerPushes(f)) != len(pushes)+1 {
				t.Error("healthy publication retry did not recover")
			}
		})
	}
}

func TestProviderReadFailureMissingSettingAllowsFirstCreation(t *testing.T) {
	s, _, st := modelService(t)
	values, _ := providerSettings(st.memStore)
	if _, ok := values[launchersManagedKey]; ok {
		t.Fatal("fixture already has providers")
	}
	r := request(s, "POST", "/api/v1/launchers", providerCreateBody)
	if r.Code != http.StatusOK {
		t.Fatalf("first creation: %d %s", r.Code, r.Body)
	}
	got := storedProviders(t, st.memStore)
	if len(got) != 1 || got[0].Name != "replacement" || got[0].KeySealed == "" {
		t.Fatal("first creation did not seal its key")
	}
}

func TestProviderReadFailureInvalidJSONIsNotEmpty(t *testing.T) {
	s, f, st := providerReadFixture(t)
	st.mu.Lock()
	st.vals[launchersManagedKey] = []byte(`[{"name":"partial"},`)
	st.mu.Unlock()
	before, writes := providerSettings(st.memStore)
	pushes := providerPushes(f)
	for _, call := range []struct{ method, path, body string }{
		{"POST", "/api/v1/launchers", providerCreateBody}, {"PUT", "/api/v1/launchers/old_api", providerEditBody},
		{"DELETE", "/api/v1/launchers/shell_example", ""},
		{"GET", "/api/v1/launchers/import?machine=server", ""}, {"POST", "/api/v1/launchers/import", providerImportBody},
	} {
		if r := request(s, call.method, call.path, call.body); r.Code != http.StatusInternalServerError {
			t.Errorf("%s invalid JSON: %d", call.method, r.Code)
		}
	}
	s.pushManaged(context.Background(), "")
	after, afterWrites := providerSettings(st.memStore)
	if writes != afterWrites || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pushes, providerPushes(f)) {
		t.Error("invalid JSON mutated or published configuration")
	}
}

func TestProviderReadFailureConcurrentCreatesPreserveProviders(t *testing.T) {
	s, _, st := providerReadFixture(t)
	before := storedProviders(t, st.memStore)
	names := []string{"parallel_a", "parallel_b"}
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := json.Marshal(map[string]any{"name": name, "kind": "api", "api": "responses", "base_url": "https://parallel.example.com", "model": "synthetic"})
			if err != nil {
				t.Error(err)
				return
			}
			if r := request(s, "POST", "/api/v1/launchers", string(body)); r.Code != http.StatusOK {
				t.Errorf("parallel create: %d %s", r.Code, r.Body)
			}
		}()
	}
	wg.Wait()
	got := storedProviders(t, st.memStore)
	if len(got) != 4 || !reflect.DeepEqual(before, got[:2]) {
		t.Fatal("concurrent creation lost an existing provider")
	}
	for _, name := range names {
		if !slices.ContainsFunc(got, func(l storedLauncher) bool { return l.Name == name }) {
			t.Errorf("missing %s", name)
		}
	}
}

// The old edit path did a third managed read immediately before overwriting the list.
func TestProviderReadFailureEditUsesConfirmedSnapshot(t *testing.T) {
	s, _, st := providerReadFixture(t)
	before := storedProviders(t, st.memStore)
	st.failAt = 3
	r := request(s, "PUT", "/api/v1/launchers/old_api", providerEditBody)
	if r.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", r.Code, r.Body)
	}
	got := storedProviders(t, st.memStore)
	if st.reads != 2 || len(got) != 2 {
		t.Fatalf("edit reread or lost its confirmed snapshot: reads=%d providers=%d", st.reads, len(got))
	}
	want := before[0]
	want.Model = "edited"
	want.Updated = got[0].Updated
	if !reflect.DeepEqual(want, got[0]) || !reflect.DeepEqual(before[1], got[1]) {
		t.Error("edit changed sealed records beyond the requested model")
	}
}

func TestProviderReadFailureReconnectKeepsNodeConfiguration(t *testing.T) {
	s, f, st := providerReadFixture(t)
	ctx := context.Background()
	before, writes := providerSettings(st.memStore)
	pushes := providerPushes(f)
	st.failAt = -1
	if calls := s.managedNodeCalls(ctx, "server"); len(calls) != 0 {
		t.Fatal("failed read produced a reconnect replacement")
	}
	if nodes, err := s.managedForNodes(ctx); err == nil || nodes != nil {
		t.Fatal("node preparation hid its read failure")
	}
	after, afterWrites := providerSettings(st.memStore)
	if writes != afterWrites || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pushes, providerPushes(f)) {
		t.Fatal("failed reconnect read mutated settings or the node")
	}
	st.recover()
	calls := s.managedNodeCalls(ctx, "server")
	if len(calls) != 1 {
		t.Fatalf("recovery produced %d calls", len(calls))
	}
	if _, err := f.DeviceCall(ctx, DeviceCall{Device: calls[0].MachineID, Method: calls[0].Method, Params: calls[0].Params}); err != nil {
		t.Fatal(err)
	}
	got := providerPushes(f)
	if len(got) != len(pushes)+1 || !bytes.Equal(got[len(got)-1].Params, pushes[len(pushes)-1].Params) {
		t.Error("recovered reconnect changed the complete node payload")
	}
	s.trustSrc = &trustStore{memStore: st.memStore, pending: []string{"pending_example"}}
	reads := st.reads
	if calls := s.managedNodeCalls(ctx, "pending_example"); len(calls) != 0 || st.reads != reads {
		t.Error("pending computer received or read credentials")
	}
}

func TestProviderReadFailureAssignmentIsCheckedAndRecovers(t *testing.T) {
	s, _, st := providerReadFixture(t)
	before, writes := providerSettings(st.memStore)
	st.failAt = 1
	assigned, kept := s.assignIfDefault(context.Background(), "launcher.old_launcher", []string{"agent.new"})
	if len(assigned) != 0 || !reflect.DeepEqual(kept, []keptPurpose{{Purpose: "agent.new", Reason: keptFailed}}) {
		t.Fatalf("failed assignment: assigned=%v kept=%v", assigned, kept)
	}
	after, afterWrites := providerSettings(st.memStore)
	if writes != afterWrites || !reflect.DeepEqual(before, after) {
		t.Fatal("failed assignment wrote configuration")
	}
	st.recover()
	assigned, kept = s.assignIfDefault(context.Background(), "launcher.old_launcher", []string{"agent.new"})
	if !reflect.DeepEqual(assigned, []string{"agent.new"}) || len(kept) != 0 {
		t.Fatalf("healthy assignment did not recover: assigned=%v kept=%v", assigned, kept)
	}
}

func TestProviderReadFailureDeleteRetainsInUseFallback(t *testing.T) {
	s, f, st := providerReadFixture(t)
	// Without explicit choices, these API purposes use the first managed API provider.
	for _, id := range []string{"kb", "inbox.triage"} {
		if err := st.memStore.PutSetting(context.Background(), modelRecordKey(id), modelRecord{}); err != nil {
			t.Fatal(err)
		}
	}
	before, writes := providerSettings(st.memStore)
	pushes := providerPushes(f)
	st.failAt = 2
	r := request(s, "DELETE", "/api/v1/launchers/old_api", "")
	if r.Code != http.StatusConflict {
		t.Fatalf("in-use fallback lost its checked snapshot: %d %s", r.Code, r.Body)
	}
	if st.reads != 1 {
		t.Fatalf("in-use resolution reread the managed providers %d times", st.reads)
	}
	after, afterWrites := providerSettings(st.memStore)
	if writes != afterWrites || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pushes, providerPushes(f)) {
		t.Error("in-use provider was changed or republished")
	}
}
