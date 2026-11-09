package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
)

// notesConnector is the connector an installed test plugin contributes: signed in with the API key the user filled
// in (a declared secret), its folders from the plugin's configuration.
type notesConnector struct {
	key     string
	folders []string
}

func (notesConnector) ID() string   { return "examplenotes" }
func (notesConnector) Name() string { return "Example Notes" }
func (notesConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools}, Hosts: []string{"notes.example.com"}}
}
func (n notesConnector) Status(context.Context) connector.Status {
	if n.key == "" {
		return connector.Status{State: connector.StateNotConfigured}
	}
	return connector.Status{State: connector.StateOK, Account: "key " + n.key[:2] + "…"}
}
func (n notesConnector) Tools() []proto.Tool { return []proto.Tool{{Name: "examplenotes_search"}} }
func (n notesConnector) Call(_ context.Context, name string, _ json.RawMessage) (string, error) {
	return fmt.Sprintf("%s in %s", name, strings.Join(n.folders, ",")), nil
}

func serveConnectorExtensionForTest() {
	_ = serve.MainWith(context.Background(), os.Stdin, os.Stdout, func(i serve.Init, _ connector.Secrets) (connector.Connector, error) {
		var cfg struct {
			Folders []string `json:"folders"`
		}
		_ = json.Unmarshal(i.Config, &cfg)
		return notesConnector{key: i.Declared["api_key"], folders: cfg.Folders}, nil
	})
}

// extensionStore is a hub store that keeps plugins too (the database's tables, in memory).
type extensionStore struct {
	*keepStore
	mu         sync.Mutex
	rows       map[string]extension.Record
	secrets    map[string]map[string]string
	audit      []string
	markets    map[string]extension.MarketRecord
	failDelete string // only this extension's delete fails
	failPut    bool   // every PutExtension fails (a database that cannot be written)
	failPutID  string // a selected member or main record fails before its write
}

func (s *extensionStore) Extensions(context.Context) ([]extension.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []extension.Record
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, nil
}
func (s *extensionStore) PutExtension(_ context.Context, r extension.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut || s.failPutID == r.ID {
		return errors.New("the database cannot be written")
	}
	s.rows[r.ID] = r
	return nil
}
func (s *extensionStore) PutExtensionPlan(_ context.Context, records []extension.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		if s.failPut || s.failPutID == record.ID {
			return errors.New("the database cannot be written")
		}
	}
	for _, record := range records {
		s.rows[record.ID] = record
	}
	return nil
}

func (s *extensionStore) DeleteExtension(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete == id {
		return errors.New("the database cannot delete this extension")
	}
	delete(s.rows, id)
	delete(s.secrets, id)
	return nil
}
func (s *extensionStore) ExtensionSecrets(_ context.Context, id string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.secrets[id] {
		out[k] = v
	}
	return out, nil
}
func (s *extensionStore) PutExtensionSecret(_ context.Context, id, name, sealed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secrets[id] == nil {
		s.secrets[id] = map[string]string{}
	}
	s.secrets[id][name] = sealed
	return nil
}
func (s *extensionStore) DeleteExtensionSecret(_ context.Context, id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets[id], name)
	return nil
}
func (s *extensionStore) AddExtensionAudit(_ context.Context, id, action, actor string, _ json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, id+" "+action+" "+actor)
	return nil
}

// notesPackage writes a plugin package whose program is this test binary serving notesConnector.
func notesPackage(t *testing.T, version string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "notes"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=connector exec "+self+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-notes", "name": "Example Notes", "version": %q,
		"engines": {"plugin_protocol": 1},
		"runtime": {"command": {%q: "bin/notes"}},
		"contributes": {"connectors": [{"id": "examplenotes", "name": "Example Notes", "capabilities": ["status", "tools", "future_example"],
			"hosts": ["notes.example.com"]}]},
		"configuration": {"type": "object", "properties": {"folders": {"type": "array", "title": "文件夹", "items": {"type": "string"}}}},
		"secrets": [{"name": "api_key", "title": "API Key", "required": true}],
		"permissions": {"network": ["notes.example.com"]}}`, version, runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func extensionTestHub(t *testing.T) (*Manager, *extensionStore) {
	h := newTestManager(t)
	store := newExtensionStore()
	h.store = store
	startTestExtensions(t, h)
	return h, store
}

func newExtensionStore() *extensionStore {
	return &extensionStore{keepStore: newKeepStore(), rows: map[string]extension.Record{}, secrets: map[string]map[string]string{}}
}

// startTestExtensions starts h's connectors and plugins (hub.json's cfgs) on h.store, stopping them when the test ends.
func startTestExtensions(t *testing.T, h *Manager, cfgs ...ExtensionConfig) {
	h.connectors = connector.NewRegistry(nil)
	h.extensions = newExtensions(h.cfg.dir, cfgs, h.logf)
	h.extensions.sandboxes.noDefault = true // test programs live outside their packages (extension_sandbox_test.go)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		for _, p := range h.extensions.reg.List() {
			if hp := h.extensions.get(p.Record.ID); hp != nil && hp.proc != nil {
				hp.proc.Stop()
			}
		}
	})
	h.startTokenRecord(ctx)
	h.startConnectors(ctx)
	h.startExtensions(ctx)
}

func waitLong(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Installed from a directory on this machine, set up from the app, its connector running in the plugin's program;
// switched off and on, upgraded, removed with what the hub kept for it.
func TestInstalledConnectorExtension(t *testing.T) {
	h, store := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	pkg := notesPackage(t, "1.0.0")

	code, out := call(t, mux, "POST", "/api/v1/plugins/-/preview", "", `{"source": "dir:`+pkg+`"}`)
	if code == 200 {
		t.Fatal("previewed without the master token")
	}
	code, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+pkg+`"}`)
	if code != 200 || pv["permissions_digest"] == "" || len(pv["permissions"].([]any)) != 3 {
		t.Fatalf("%d %v", code, pv)
	}
	code, out = call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 || out["status"] != extensionNeedsSetup || out["source"] != "installed" {
		t.Fatalf("installed: %d %v", code, out)
	}
	if h.connectors.Get("examplenotes") != nil {
		t.Fatal("no connector before its key is filled in")
	}

	code, out = call(t, mux, "PUT", "/api/v1/plugins/example-notes/-/config", master, `{"config": {"folders": "work"}}`)
	if code != 400 || out["field"] != "folders" {
		t.Fatalf("held to the schema: %d %v", code, out)
	}
	if code, out = call(t, mux, "PUT", "/api/v1/plugins/example-notes/-/config", master, `{"config": {"folders": ["work"]}}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	code, out = call(t, mux, "PUT", "/api/v1/plugins/example-notes/-/secrets/api_key", master, `{"value": "k3y-from-the-app"}`)
	if code != 200 || out["secrets"].([]any)[0].(map[string]any)["set"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), "k3y-from-the-app") {
		t.Fatal("a secret is never sent back")
	}
	for _, sealed := range store.secrets["example-notes"] {
		if strings.Contains(sealed, "k3y") {
			t.Fatal("kept sealed")
		}
	}

	waitLong(t, "the connector signed in with the key", func() bool {
		c := h.connectors.Get("examplenotes")
		return c != nil && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})
	if st := connector.StatusOf(context.Background(), h.connectors.Get("examplenotes")); st.Account != "key k3…" {
		t.Fatal(st)
	}
	if !fromExtension("examplenotes") { // its new inbox items are triaged before they enter (the manager's inbox_admit.go)
		t.Fatal("an installed plugin's connector is not taken as a plugin's")
	}
	// A capability this hub does not have (a plugin made for a newer one) is left out, not a reason to refuse it.
	if m := h.connectors.Get("examplenotes").Manifest(); m.Has("future_example") || !m.Has(connector.CapTools) {
		t.Fatalf("manifest %+v", m.Capabilities)
	}
	res := h.callConnector(context.Background(), asker{}, "examplenotes_search", json.RawMessage(`{}`))
	if res.IsError || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "examplenotes_search in work") {
		t.Fatalf("%+v", res)
	}
	_, list := call(t, mux, "GET", "/api/v1/plugins", master, "")
	var notes map[string]any
	for _, p := range list["plugins"].([]any) {
		if p.(map[string]any)["id"] == "example-notes" {
			notes = p.(map[string]any)
		}
	}
	if notes["status"] != "running" || notes["kind"] != "connectors" {
		t.Fatalf("%v", list["plugins"])
	}

	if code, _ := call(t, mux, "GET", "/api/v1/plugins/example-notes/-/anything", master, ""); code != 404 {
		t.Fatal("the hub's segment is never relayed:", code)
	}

	// Switched off: its connector goes; on again: it comes back.
	if code, out = call(t, mux, "PUT", "/api/v1/plugins/example-notes/-/enabled", master, `{"enabled": false}`); code != 200 || out["status"] != "disabled" {
		t.Fatalf("%d %v", code, out)
	}
	if h.connectors.Get("examplenotes") != nil {
		t.Fatal("still registered")
	}
	call(t, mux, "PUT", "/api/v1/plugins/example-notes/-/enabled", master, `{"enabled": true}`)
	waitLong(t, "the connector back", func() bool {
		c := h.connectors.Get("examplenotes")
		return c != nil && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})

	// The user's connector switch holds the plugin's process: off, it stops.
	if err := h.setConnectorEnabled(context.Background(), "examplenotes", false); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "the program to stop", func() bool {
		st, _ := h.extensions.get("example-notes").proc.Status()
		return st == "stopped"
	})
	if err := h.setConnectorEnabled(context.Background(), "examplenotes", true); err != nil {
		t.Fatal(err)
	}

	// Upgrade (same permissions): the new version runs.
	pkg2 := notesPackage(t, "1.1.0")
	_, pv = call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+pkg2+`", "upgrade": "example-notes"}`)
	code, out = call(t, mux, "POST", "/api/v1/plugins/example-notes/-/upgrade", master, fmt.Sprintf(`{"preview_id": %q}`, pv["preview_id"]))
	if code != 200 || out["version"] != "1.1.0" || out["previous_version"] != "1.0.0" {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the new version's connector", func() bool {
		c := h.connectors.Get("examplenotes")
		return c != nil && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})

	// Removed: the plugin, its connector and what the hub kept for it go.
	_ = store.PutConnectorSecret(context.Background(), "examplenotes", "sealed session")
	if code, out = call(t, mux, "DELETE", "/api/v1/plugins/example-notes/-/", master, ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if h.connectors.Get("examplenotes") != nil || h.extensions.reg.Get("example-notes") != nil || len(store.rows) != 0 {
		t.Fatal("still there")
	}
	if _, ok, _ := store.ConnectorSecret(context.Background(), "examplenotes"); ok {
		t.Fatal("its connector's secret stays")
	}
	var cs connectorSettings
	_, _ = store.Setting(context.Background(), connectorSettingsKey, &cs)
	if _, ok := cs.Switch["examplenotes"]; ok {
		t.Fatal("its connector's switch stays")
	}
	actions := strings.Join(store.audit, "\n")
	for _, a := range []string{"install personad extension", "config hub token", "secret_set hub token", "disabled hub token", "upgrade personad extension", "uninstall personad extension"} {
		if !strings.Contains(actions, a) {
			t.Errorf("audit has no %q:\n%s", a, actions)
		}
	}
}

// A phone (a device token) sets plugins up but installs nothing; an answer-only device does neither.
func TestExtensionAPIRights(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	for _, path := range []string{"/api/v1/plugins/-/preview", "/api/v1/plugins/-/install", "/api/v1/plugins/x/-/upgrade", "/api/v1/plugins/x/-/rollback"} {
		req := `{}`
		code, out := callFrom(t, mux, "POST", path, h.cfg.Token, req, "[fd00::10]:1234")
		if code != 403 || out["code"] != "this_machine_only" {
			t.Errorf("%s from another machine: %d %v", path, code, out)
		}
	}
}

// callFrom is call from the address from.
func callFrom(t *testing.T, h http.Handler, method, path, token, body, from string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = from
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (s *extensionStore) ExtensionMarkets(context.Context) ([]extension.MarketRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []extension.MarketRecord
	for _, r := range s.markets {
		out = append(out, r)
	}
	return out, nil
}
func (s *extensionStore) PutExtensionMarket(_ context.Context, r extension.MarketRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markets == nil {
		s.markets = map[string]extension.MarketRecord{}
	}
	s.markets[r.ID] = r
	return nil
}
func (s *extensionStore) DeleteExtensionMarket(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.markets, id)
	return nil
}

// A plugin's confirm_always travels from its manifest to the gate: an allow-everything rule no longer lets the AI run
// that action without the user, and the connectors that say nothing keep their allow.
func TestExtensionConfirmAlwaysReachesTheGate(t *testing.T) {
	item := extension.ConnectorItem{ID: "examplechat", Name: "Example Chat", Capabilities: []string{"status", "actions"}, ConfirmAlways: []string{"examplechat_send"}}
	m := connectorManifest(item)
	allow := []connector.Rule{{Connector: "*", Action: "*", Decision: connector.Allow}}
	if got := connector.Decide(allow, m, "examplechat", "examplechat_send", nil); got != connector.Confirm {
		t.Fatalf("send under an allow rule: %s", got)
	}
	if got := connector.Decide(allow, m, "examplechat", "examplechat_mark_read", nil); got != connector.Allow {
		t.Fatalf("an action not in confirm_always: %s", got)
	}
	item.ConfirmAlways = nil
	if got := connector.Decide(allow, connectorManifest(item), "examplechat", "examplechat_send", nil); got != connector.Allow {
		t.Fatalf("not declared: %s", got)
	}
}

// An installed plugin takes the place of a built-in that yields (Telegram from the market, #1704), shown with the
// example connector: the built-in's program has stopped once the installed one is in, the connector keeps its id and
// its sign-in (the hub's sealed secret, never forgotten), and uninstalling the plugin brings the built-in back with
// the sign-in still there.
func TestInstalledExtensionTakesTheBuiltinsPlace(t *testing.T) {
	h, store := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	ctx := context.Background()
	builtinDir := notesPackage(t, "0.9.0")
	raw, _ := os.ReadFile(filepath.Join(builtinDir, extension.ManifestFile))
	m, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.AddBuiltin(m, builtinDir, true); err != nil {
		t.Fatal(err)
	}
	h.extensions.reg.Yields("example-notes")
	_ = store.PutConnectorSecret(ctx, "examplenotes", "sealed session")
	if code, out := call(t, mux, "PUT", "/api/v1/plugins/example-notes/-/secrets/api_key", master, `{"value": "k3y"}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	ok := func() bool {
		c := h.connectors.Get("examplenotes")
		return c != nil && connector.StatusOf(ctx, c).State == connector.StateOK
	}
	waitLong(t, "the built-in's connector registered", func() bool { return h.connectors.Get("examplenotes") != nil })
	if err := h.setConnectorEnabled(ctx, "examplenotes", true); err != nil { // a built-in's starts off; the user turned it on
		t.Fatal(err)
	}
	waitLong(t, "the built-in's connector", ok)
	builtin := h.extensions.get("example-notes").proc

	pkg := notesPackage(t, "1.0.0")
	code, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+pkg+`"}`)
	if code != 200 {
		t.Fatalf("the built-in's id is free to install over: %d %v", code, pv)
	}
	code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 || out["source"] != "installed" || out["version"] != "1.0.0" {
		t.Fatalf("installed: %d %v", code, out)
	}
	if st, _ := builtin.Status(); st != "stopped" {
		t.Fatalf("the built-in's program still runs (%s): two would hold one sign-in", st)
	}
	waitLong(t, "the installed plugin's connector", ok)
	if hp := h.extensions.get("example-notes"); hp == nil || hp.proc == builtin {
		t.Fatal("the installed plugin does not run")
	}
	if sealed, kept, _ := store.ConnectorSecret(ctx, "examplenotes"); !kept || sealed != "sealed session" {
		t.Fatal("the connector's sign-in did not carry over")
	}

	if code, out = call(t, mux, "DELETE", "/api/v1/plugins/example-notes/-/", master, ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if p := h.extensions.reg.Get("example-notes"); p == nil || p.Record.Source != extension.SourceBuiltin {
		t.Fatalf("the built-in is not back: %+v", p)
	}
	if sealed, kept, _ := store.ConnectorSecret(ctx, "examplenotes"); !kept || sealed != "sealed session" {
		t.Fatal("uninstalling the plugin forgot the sign-in the built-in uses")
	}
}

// Every plugin learns the user's time zone at its start (initialize's hub.timezone), as the built-in Telegram did
// through its settings: an installed Telegram plugin shows times as the user reads them (#1704).
func TestExtensionsGetTheUserTimezone(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	pkg := notesPackage(t, "1.0.0")
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", h.cfg.Token, `{"source": "dir:`+pkg+`"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", h.cfg.Token, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	for _, tz := range []string{"", "Asia/Shanghai"} {
		h.opts.Timezone = func() string { return tz } // the assistant's settings, in this process
		s, err := h.extensions.spec(context.Background(), h.extensions.reg.Get("example-notes"), &hubExtension{id: "example-notes"})
		if err != nil {
			t.Fatal(err)
		}
		params, err := s.Init(context.Background())
		if err != nil || params.Hub.Timezone != tz {
			t.Fatalf("%q: %+v %v", tz, params.Hub, err)
		}
	}
}

// A connector's command line (connector.Manifest.CLI, #1704) reaches the assistant's machine with the connectors
// signed in, as its name and the connector it runs (connectors.signed_in's clis, which the assistant passes on in
// assistant.prepare): only while the connector is signed in.
func TestAssistantGetsTheConnectorsCommandLines(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	pkg := notesPackage(t, "1.0.0")
	p := filepath.Join(pkg, extension.ManifestFile)
	raw, _ := os.ReadFile(p)
	_ = os.WriteFile(p, []byte(strings.Replace(string(raw), `"hosts": ["notes.example.com"]}`, `"hosts": ["notes.example.com"], "cli": "notes"}`, 1)), 0o644)
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+pkg+`"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	clis := func() map[string]string {
		v, err := h.assistantAppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppConnectors,
			Method: assistantwire.ConnectorsSignedIn})
		raw, _ := json.Marshal(v)
		var res struct {
			CLIs map[string]string `json:"clis"`
		}
		if err != nil || json.Unmarshal(raw, &res) != nil {
			t.Fatal(err)
		}
		return res.CLIs
	}
	if len(clis()) != 0 {
		t.Fatal("not signed in yet, a command line:", clis())
	}
	call(t, mux, "PUT", "/api/v1/plugins/example-notes/-/secrets/api_key", master, `{"value": "k3y"}`)
	waitLong(t, "the connector signed in", func() bool {
		c := h.connectors.Get("examplenotes")
		return c != nil && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})
	if got := clis(); len(got) != 1 || got["notes"] != "examplenotes" {
		t.Fatal(got)
	}
}
