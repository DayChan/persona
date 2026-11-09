package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/extwire"
)

// Only settings are implemented: no accounts, providers, extension programs or node transport exist in this fixture.
type computerAppsSettings struct {
	mu          sync.Mutex
	values      map[string]json.RawMessage
	failWrite   bool
	beforeWrite func()
	writes      atomic.Int32
}

func (s *computerAppsSettings) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(value, out)
}
func (s *computerAppsSettings) PutSetting(_ context.Context, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "computer.apps" && s.beforeWrite != nil {
		s.beforeWrite()
	}
	if key == "computer.apps" && s.failWrite {
		s.failWrite = false
		return errors.New("synthetic persistence failure")
	}
	data, err := json.Marshal(value)
	if err == nil {
		s.values[key] = data
		if key == "computer.apps" {
			s.writes.Add(1)
		}
	}
	return err
}
func (s *computerAppsSettings) count(t *testing.T) int {
	t.Helper()
	var all map[string][]json.RawMessage
	if _, err := s.Setting(context.Background(), "computer.apps", &all); err != nil {
		t.Fatal(err)
	}
	return len(all["example-machine"])
}

type computerAppsDevices struct{ calls atomic.Int32 }

func (*computerAppsDevices) Has(string) bool { return false }
func (d *computerAppsDevices) CallNode(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	d.calls.Add(1)
	return nil, errors.New("no node exists in this fixture")
}
func (*computerAppsDevices) Views() []devices.View {
	return []devices.View{{Summary: devices.Summary{ID: "example-machine", Name: "Example computer", Kind: "local"}}}
}

// The actual hub selects a manager, asks extensions.accept over the authenticated bus, and proxies HTTP through
// its relay to the real manager's ServeHTTP. Each manager has separate synthetic settings and no live node.
func computerAppsManagerOnBus(t *testing.T, h *Hub, id string, beforeWrite func(), oldReceiver ...bool) (*computerAppsSettings, *computerAppsDevices) {
	t.Helper()
	store := &computerAppsSettings{values: map[string]json.RawMessage{}}
	if err := store.PutSetting(context.Background(), "computer.apps", map[string]any{"example-machine": []map[string]string{{"bundle_id": "org.example.editor", "name": "Example Editor"}}}); err != nil {
		t.Fatal(err)
	}
	store.beforeWrite = beforeWrite
	nodes := &computerAppsDevices{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	token := busToken(t, h, id, id)
	dial := func(ctx context.Context, query string) (*websocket.Conn, error) {
		return bus.DialAt(ctx, srv.URL, token, srv.Client(), devicewire.RelayPath, query)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var receive func(context.Context, string, json.RawMessage) (any, error)
	if len(oldReceiver) > 0 && oldReceiver[0] {
		legacy := &relayingExt{serve: legacyComputerApps(store), dial: dial}
		receive = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method == extwire.MethodAccept {
				out, _, err := legacy.Request(ctx, method, params)
				return out, err
			}
			return map[string]any{}, nil
		}
	} else {
		manager := extmgr.New(extmgr.Options{Dir: t.TempDir(), Config: &extmgr.Config{}, Store: store, ManagerID: id, Devices: nodes, DialRelay: dial})
		done := make(chan error, 1)
		go func() { done <- manager.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("synthetic manager did not stop")
			}
			if nodes.calls.Load() != 0 {
				t.Errorf("persisted approval requested a live node: %d calls", nodes.calls.Load())
			}
		})
		select {
		case <-manager.Started():
		case <-time.After(5 * time.Second):
			t.Fatal("synthetic manager did not start")
		}
		receive = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			out, handled, err := manager.Request(ctx, method, params)
			if !handled {
				return nil, errors.New("unexpected synthetic bus method: " + method)
			}
			return out, err
		}
	}
	peer := dialRawBusWith(t, srv, token, receive)
	if err := peer.call("manager.hello", map[string]string{"id": id}, nil); err != nil {
		t.Fatal(err)
	}
	return store, nodes
}

func computerAppsRoutingHub(t *testing.T) *Hub {
	t.Helper()
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	h.cfg.Token = "synthetic-hub-token"
	h.cfg.Machines = []MachineConfig{{ID: "example-machine", Name: "Example computer", Kind: "local"}}
	h.byID["example-machine"] = newMachine(h, h.cfg.Machines[0])
	return h
}
func computerAppsRequest(h *Hub, method, path, manager string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	r.Header.Set(ManagerHeader, manager)
	r.Header.Set("Accept-Language", "en")
	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, r)
	return w
}

func TestComputerAppOwnerPreconditionAcrossActualHubRouting(t *testing.T) {
	h := computerAppsRoutingHub(t)
	a, _ := computerAppsManagerOnBus(t, h, assistantwire.AssistantID, nil)
	b, _ := computerAppsManagerOnBus(t, h, "m-1489bb", nil)
	const path = "/api/v1/machines/example-machine/computer/apps"
	read := computerAppsRequest(h, http.MethodGet, path+"?expected_manager=assistant", assistantwire.AssistantID)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"manager":"assistant"`) {
		t.Fatalf("original list: %d %s", read.Code, read.Body)
	}
	if _, err := h.moveMachine(context.Background(), "example-machine", "m-1489bb"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		target := path
		if method == http.MethodDelete {
			target += "/revoke/" + url.PathEscape("org.example.editor")
		}
		w := computerAppsRequest(h, method, target+"?expected_manager=assistant", assistantwire.AssistantID)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"machine_owner_changed"`) {
			t.Fatalf("stale owner %s: %d %s", method, w.Code, w.Body)
		}
		if a.count(t) != 1 || b.count(t) != 1 {
			t.Fatal("stale owner mutated either manager's settings")
		}
	}
	// Legacy routing remains current-owner routing, including when a different manager header is present.
	legacy := computerAppsRequest(h, http.MethodGet, path, assistantwire.AssistantID)
	if legacy.Code != http.StatusOK || !strings.Contains(legacy.Body.String(), `"manager":"m-1489bb"`) {
		t.Fatalf("legacy routing: %d %s", legacy.Code, legacy.Body)
	}
	b.mu.Lock()
	b.failWrite = true
	b.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		w := computerAppsRequest(h, http.MethodDelete, path+"/revoke/org.example.editor?expected_manager=m-1489bb", "m-1489bb")
		if attempt == 0 {
			if w.Code != http.StatusInternalServerError || b.count(t) != 1 {
				t.Fatalf("failed persistence: %d %s", w.Code, w.Body)
			}
		} else if w.Code != http.StatusOK || b.count(t) != 0 || a.count(t) != 1 {
			t.Fatalf("retry: %d %s", w.Code, w.Body)
		}
	}
}

func TestComputerAppOwnerWriteRemainsWithManagerSelectedBeforeMove(t *testing.T) {
	h := computerAppsRoutingHub(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	a, _ := computerAppsManagerOnBus(t, h, assistantwire.AssistantID, func() {
		close(entered)
		<-release
	})
	b, _ := computerAppsManagerOnBus(t, h, "m-1489bb", nil)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- computerAppsRequest(h, http.MethodDelete, "/api/v1/machines/example-machine/computer/apps/revoke/org.example.editor?expected_manager=assistant", assistantwire.AssistantID)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not reach the original manager")
	}
	if _, err := h.moveMachine(context.Background(), "example-machine", "m-1489bb"); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case w := <-done:
		if w.Code != http.StatusOK || w.Header().Get(computerAppsOwnerHeader) != assistantwire.AssistantID || !strings.Contains(w.Body.String(), `"manager":"assistant"`) || a.count(t) != 0 || b.count(t) != 1 {
			t.Fatalf("in-flight write changed managers: %d %s", w.Code, w.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not settle")
	}
}

func TestComputerAppIdentifiersThroughActualRelay(t *testing.T) {
	h := computerAppsRoutingHub(t)
	store, _ := computerAppsManagerOnBus(t, h, assistantwire.AssistantID, nil)
	grants := []map[string]string{
		{"bundle_id": "org.example.editor", "name": "Example Editor"},
		{"bundle_id": "name:design / notes + draft%?#", "name": "Design / Notes + Draft"},
		{"bundle_id": "screen", "name": "Whole screen"},
	}
	if err := store.PutSetting(context.Background(), "computer.apps", map[string]any{"example-machine": grants}); err != nil {
		t.Fatal(err)
	}
	for index, encoded := range []string{"org.example.editor", "name%3Adesign%20%2F%20notes%20%2B%20draft%25%3F%23", "screen"} {
		w := computerAppsRequest(h, http.MethodDelete, "/api/v1/machines/example-machine/computer/apps/revoke/"+encoded+"?expected_manager=assistant", assistantwire.AssistantID)
		if w.Code != http.StatusOK || store.count(t) != len(grants)-index-1 {
			t.Fatalf("revoke exact identifier %q: %d %s", grants[index]["bundle_id"], w.Code, w.Body)
		}
	}
}

// The old receiver deliberately ignores expected_manager and knows only the original GET/DELETE routes.
func legacyComputerApps(store *computerAppsSettings) http.Handler {
	mux := http.NewServeMux()
	read := func(w http.ResponseWriter, r *http.Request) {
		var all map[string][]map[string]string
		_, _ = store.Setting(r.Context(), "computer.apps", &all)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(computerAppsOwnerHeader, "untrusted-receiver-header")
		_ = json.NewEncoder(w).Encode(map[string]any{"apps": all["example-machine"]})
	}
	mux.HandleFunc("GET /api/v1/machines/{mid}/computer/apps", read)
	mux.HandleFunc("DELETE /api/v1/machines/{mid}/computer/apps/{bundle}", func(w http.ResponseWriter, r *http.Request) {
		var all map[string][]map[string]string
		_, _ = store.Setting(r.Context(), "computer.apps", &all)
		kept := all["example-machine"][:0]
		for _, app := range all["example-machine"] {
			if app["bundle_id"] != r.PathValue("bundle") {
				kept = append(kept, app)
			}
		}
		all["example-machine"] = kept
		_ = store.PutSetting(r.Context(), "computer.apps", all)
		read(w, r)
	})
	return mux
}

func TestComputerAppMoveToLegacyReceiverRejectsBeforeRelay(t *testing.T) {
	h := computerAppsRoutingHub(t)
	a, _ := computerAppsManagerOnBus(t, h, assistantwire.AssistantID, nil)
	b, _ := computerAppsManagerOnBus(t, h, "m-1489bb", nil, true)
	const path = "/api/v1/machines/example-machine/computer/apps"
	read := computerAppsRequest(h, http.MethodGet, path+"?expected_manager=assistant", assistantwire.AssistantID)
	if read.Code != http.StatusOK || read.Header().Get(computerAppsOwnerHeader) != assistantwire.AssistantID {
		t.Fatalf("list proof: %d %s", read.Code, read.Body)
	}
	before := b.writes.Load()
	if _, err := h.moveMachine(context.Background(), "example-machine", "m-1489bb"); err != nil {
		t.Fatal(err)
	}
	stale := computerAppsRequest(h, http.MethodDelete, path+"/revoke/org.example.editor?expected_manager=assistant", assistantwire.AssistantID)
	if stale.Code != http.StatusConflict || a.count(t) != 1 || b.count(t) != 1 || b.writes.Load() != before {
		t.Fatalf("stale confirmation reached legacy receiver: %d %s", stale.Code, stale.Body)
	}
	legacyRead := computerAppsRequest(h, http.MethodGet, path+"?expected_manager=m-1489bb", "m-1489bb")
	if legacyRead.Code != http.StatusOK || legacyRead.Header().Get(computerAppsOwnerHeader) != "m-1489bb" {
		t.Fatalf("legacy receiver proof: %d %s", legacyRead.Code, legacyRead.Body)
	}
	current := computerAppsRequest(h, http.MethodDelete, path+"/revoke/org.example.editor?expected_manager=m-1489bb", "m-1489bb")
	if current.Code != http.StatusOK || b.count(t) != 0 || b.writes.Load() != before+1 || a.count(t) != 1 {
		t.Fatalf("guarded legacy receiver revoke: %d %s", current.Code, current.Body)
	}
}

func TestComputerAppGuardedAliasCannotMutateLegacyReceiverThroughOldHub(t *testing.T) {
	store := &computerAppsSettings{values: map[string]json.RawMessage{}}
	if err := store.PutSetting(context.Background(), "computer.apps", map[string]any{"example-machine": []map[string]string{{"bundle_id": "org.example.editor", "name": "Example Editor"}}}); err != nil {
		t.Fatal(err)
	}
	before := store.writes.Load()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/machines/example-machine/computer/apps/revoke/org.example.editor?expected_manager=assistant", nil)
	recorder := httptest.NewRecorder()
	// A hub without the new translation forwards this path unchanged, regardless of an earlier capability read.
	legacyComputerApps(store).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || store.count(t) != 1 || store.writes.Load() != before {
		t.Fatalf("legacy receiver accepted guarded alias: %d %s", recorder.Code, recorder.Body)
	}
}

func TestComputerAppLegacyDeleteKeepsCurrentOwnerRouting(t *testing.T) {
	h := computerAppsRoutingHub(t)
	a, _ := computerAppsManagerOnBus(t, h, assistantwire.AssistantID, nil)
	b, _ := computerAppsManagerOnBus(t, h, "m-1489bb", nil, true)
	if _, err := h.moveMachine(context.Background(), "example-machine", "m-1489bb"); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/machines/example-machine/computer/apps/"
	missing := computerAppsRequest(h, http.MethodDelete, path+"revoke/org.example.editor", assistantwire.AssistantID)
	if missing.Code != http.StatusPreconditionRequired || b.count(t) != 1 {
		t.Fatalf("guarded alias lacked its required owner: %d %s", missing.Code, missing.Body)
	}
	legacy := computerAppsRequest(h, http.MethodDelete, path+"org.example.editor", assistantwire.AssistantID)
	if legacy.Code != http.StatusOK || b.count(t) != 0 || a.count(t) != 1 {
		t.Fatalf("legacy routing changed: %d %s", legacy.Code, legacy.Body)
	}
}
