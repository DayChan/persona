package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/proc"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/extwire"
)

// serveAPIExtensionForTest serves part of the app's API (/api/v1/examplefeed/…, and /api/v1/lark/… taken over from the
// hub) on its socket, and pushes two events once initialized: one of a type it declared, one it did not.
func serveAPIExtensionForTest() {
	ln, err := net.Listen("unix", os.Getenv("PERSONA_PLUGIN_SOCKET"))
	if err != nil {
		os.Exit(1)
	}
	var sends atomic.Int32
	var role, caps atomic.Value
	role.Store("")
	caps.Store([]string(nil))
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/role") {
			_ = json.NewEncoder(w).Encode(map[string]any{"role": role.Load(), "capabilities": caps.Load()})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/send") {
			_ = json.NewEncoder(w).Encode(map[string]any{"n": sends.Add(1)})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/cached") {
			w.Header().Set("Cache-Control", "private, max-age=3600")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "raw": r.URL.EscapedPath(), "device": r.Header.Get("X-Persona-Device"),
			"auth": r.Header.Get("Authorization"), "forged": r.Header.Get("X-Persona-Forged")})
	}))
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-feed", Version: "1.0.0",
		Init: func(_ context.Context, ip extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			role.Store(ip.Hub.Role)
			caps.Store(ip.Hub.Capabilities)
			go func() {
				time.Sleep(300 * time.Millisecond)
				_ = h.Notify("api.broadcast", map[string]any{"type": "examplefeed", "n": 1})
				_ = h.Notify("api.broadcast", map[string]any{"type": "sessions", "n": 2})
			}()
			return nil, nil
		},
		Namespaces: map[string]pserve.Handler{"assistant": func(_ context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case "credentials":
				return map[string]any{"env": map[string]string{"FEED_BASE": "http://127.0.0.1:9", "FEED_TOKEN": "feed-secret", "PERSONA_HUB_TOKEN": "stolen"}}, nil
			case "summary":
				var p struct {
					Summary string `json:"summary"`
				}
				_ = json.Unmarshal(params, &p)
				return map[string]any{"summary": strings.ReplaceAll(p.Summary, "c42", "示例频道")}, nil
			}
			return nil, fmt.Errorf("unknown %s", method)
		}}})
}

func apiPackage(t *testing.T) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "skills", "example-feed"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "feed"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=api exec "+self+" \"$@\"\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "skills", "example-feed", "SKILL.md"), []byte("---\nname: example-feed\ndescription: Read the example feed.\n---\nUse it.\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "assistant.md"), []byte("## Example feed\n\nThe example feed is read with its skill.\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "skills", "example-feed", "scripts"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "skills", "example-feed", "scripts", "feed.sh"), []byte("#!/bin/bash\n"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "gate.json"), []byte(`{"command": "feed", "script": "example-feed/scripts/feed.sh", "title": "示例",
		"connector": "examplefeed", "env": ["FEED_BASE", "FEED_TOKEN"], "base_env": "FEED_BASE", "secret": ["FEED_TOKEN"],
		"rules": [{"method": "GET", "path": "**", "action": "allow"}], "tool": {"name": "example_feed", "description": "Run feed."}}`), 0o644)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-feed", "name": "Example feed", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/feed"}, "http": true},
		"activation_events": ["on_startup"],
		"contributes": {
			"api": [{"id": "feed", "prefix": "examplefeed", "features": ["example_feed", "extension_ui_stream_v1"], "events": ["examplefeed"],
			         "receipts": [{"method": "POST", "path": "/rooms/*/send", "kind": "examplefeed_send"}]},
			        {"id": "lark", "prefix": "lark"}, {"id": "mine", "prefix": "machines"}],
			"assistant": [{"id": "feed", "skills": ["skills/example-feed"], "instructions": "assistant.md", "gate": "gate.json"}],
			"mcp_servers": [{"id": "feed", "command": "feed-agent", "args": ["mcp"], "allow": ["list"], "deny": ["decide"]}]}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A plugin's part of the app's API: relayed with who is asking (never the credential), its features and its prefix in
// the report the hub shows them from (the receipts it declared with them: the hub keeps those), its declared events
// pushed to the apps; a prefix being moved out of the hub is the plugin's, the hub's own are not; its skills and
// instructions reach the assistant.
func TestUIStreamFeatureReport(t *testing.T) {
	if got := (&Manager{}).features(); slices.Contains(got, extwire.FeatureUIStream) {
		t.Fatal("uninitialized manager advertises stream support")
	}
	h, _ := extensionTestHub(t)
	if got := h.report().Features; featureCount(got, extwire.FeatureUIStream) != 1 {
		t.Fatalf("empty initialized registry must report transport support once: %v", got)
	}
}

func featureCount(features []string, feature string) int {
	count := 0
	for _, candidate := range features {
		if candidate == feature {
			count++
		}
	}
	return count
}

func TestExtensionAPI(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token

	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+apiPackage(t)+`"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the plugin up", func() bool {
		hp := h.extensions.get("example-feed")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	req, _ := http.NewRequest("GET", "/api/v1/examplefeed/items?x=1", nil)
	req.Header.Set("X-Persona-Forged", "nope")
	req.Header.Set("X-Persona-Device", "someone-else")
	code, out := callReq(t, mux, req, master)
	if code != 200 || out["path"] != "/api/v1/examplefeed/items" || out["device"] != "" || out["auth"] != "" {
		t.Fatalf("relayed: %d %v", code, out)
	}
	if out["forged"] != "" {
		t.Fatal("the caller's own X-Persona headers are dropped")
	}
	if _, out := call(t, mux, "GET", "/api/v1/lark/chats", master, ""); out["path"] != "/api/v1/lark/chats" {
		t.Fatalf("a prefix being moved out of the hub is the plugin's: %v", out)
	}
	if _, out := call(t, mux, "GET", "/api/v1/lark/raw/mail/threads/a%2Fb", master, ""); out["raw"] != "/api/v1/lark/raw/mail/threads/a%2Fb" {
		t.Fatalf("an escaped / reaches the plugin escaped: %v", out)
	}
	// The plugin's own Cache-Control reaches the app alone; without one the answer is no-store, as the hub's own are.
	for path, want := range map[string]string{"/api/v1/lark/emojis/cached": "private, max-age=3600", "/api/v1/lark/chats": "no-store",
		"/api/v1/plugins/example-feed/cached": "private, max-age=3600"} {
		req, _ := http.NewRequest("GET", path, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+master)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if got := w.Header().Values("Cache-Control"); w.Code != 200 || len(got) != 1 || got[0] != want {
			t.Fatalf("%s: %d Cache-Control %q, want %q", path, w.Code, got, want)
		}
	}
	if _, out := call(t, mux, "GET", "/api/v1/examplefeed/role", master, ""); out["role"] != "center" ||
		fmt.Sprint(out["capabilities"]) != "[inbox_group]" {
		t.Fatalf("the hub's role and capabilities at initialize: %v", out)
	}
	// A send it declared a receipt for is kept by the hub, which relays it (hub/extensions_link.go): the report
	// says which; the send itself reaches the plugin.
	rep := h.report()
	if featureCount(rep.Features, extwire.FeatureUIStream) != 1 {
		t.Fatalf("extension-declared feature duplicates manager transport: %v", rep.Features)
	}
	var feed *extwire.APIPrefix
	for i := range rep.APIs {
		if rep.APIs[i].Prefix == "examplefeed" {
			feed = &rep.APIs[i]
		}
	}
	if feed == nil || len(feed.Receipts) != 1 || feed.Receipts[0].Kind != "examplefeed_send" || !feed.Receipts[0].Matches("POST", "/rooms/r1/send") {
		t.Fatalf("the report's APIs: %+v", rep.APIs)
	}
	if !slices.ContainsFunc(rep.APIs, func(a extwire.APIPrefix) bool { return a.Prefix == "lark" }) || !slices.Contains(rep.Features, "example_feed") {
		t.Fatalf("the report: %+v", rep)
	}
	if code, out := call(t, mux, "POST", "/api/v1/examplefeed/rooms/r1/send", master, `{"client_id": "c-1", "text": "hi"}`); code != 200 || out["n"] != float64(1) {
		t.Fatalf("send: %d %v", code, out)
	}
	// The hub's own API under a prefix (GET /api/v1/machines, any path under it) never reaches the manager: the hub
	// gives the extension only prefixes it does not own (hub/extensions_link.go extensionAPIs). The routes the manager
	// answers under one (a computer's Cua) are its own, not the plugin's.
	if _, out := call(t, mux, "GET", "/api/v1/machines/nosuch/computer", master, ""); out["path"] != nil {
		t.Fatalf("the manager's own routes are not a plugin's: %v", out)
	}

	if !slices.Contains(h.features(), "example_feed") {
		t.Fatal("the plugin's feature is the hub's")
	}
	eventually(t, 10*time.Second, "the plugin's event to the apps", func() bool { return len(hubOf(h).broadcastsOf("examplefeed")) > 0 })
	if ev := hubOf(h).broadcastsOf("examplefeed")[0]; ev["n"] != float64(1) {
		t.Fatalf("%v", ev)
	}
	time.Sleep(700 * time.Millisecond)
	for _, b := range hubOf(h).broadcastsOf("sessions") {
		t.Fatalf("an event of a type it did not declare went out: %v", b)
	}

	if md := h.extensions.extensionInstructions(); !strings.Contains(md, "The example feed is read with its skill.") {
		t.Fatalf("instructions: %q", md)
	}
	first := h.extensions.extensionSkillsFor("mac")
	if len(first) != 1 || first[0].Name != "example-feed" || string(first[0].Files["SKILL.md"]) == "" {
		t.Fatalf("%+v", first)
	}
	if again := h.extensions.extensionSkillsFor("mac"); again[0].Files != nil || again[0].Digest != first[0].Digest {
		t.Fatal("the files go once")
	}
	if other := h.extensions.extensionSkillsFor("other"); other[0].Files == nil {
		t.Fatal("each machine gets them once")
	}

	// Its command gate reaches the assistant's machine: the user's rules for its connector first, only the credentials
	// it declares; a card about its calls is put in its words.
	h.cfg.ConnectorGate = []connector.Rule{{Connector: "examplefeed", Action: "POST /posts", Decision: "deny"}}
	lk, err := h.assistantLark(context.Background())
	if err != nil || len(lk.Gates) != 1 {
		t.Fatalf("gates %+v, %v", lk.Gates, err)
	}
	g := lk.Gates[0]
	if g.Plugin != "example-feed" || g.Command != "feed" || g.Tool == nil || g.Tool.Name != "example_feed" ||
		len(g.Env) != 2 || g.Env["FEED_TOKEN"] != "feed-secret" || g.Env["PERSONA_HUB_TOKEN"] != "" {
		t.Fatalf("gate %+v", g)
	}
	if len(g.Rules) != 2 || g.Rules[0].Action != "deny" || g.Rules[0].Path != "/posts" || g.Rules[1].Action != "allow" {
		t.Fatalf("rules %+v", g.Rules)
	}
	// Its MCP server reaches the assistant's machine as declared.
	if len(lk.MCPServers) != 1 || lk.MCPServers[0].Plugin != "example-feed" || lk.MCPServers[0].Command != "feed-agent" ||
		lk.MCPServers[0].Allow[0] != "list" || lk.MCPServers[0].Deny[0] != "decide" {
		t.Fatalf("mcp servers %+v", lk.MCPServers)
	}
	if s, ok := h.extensions.assistantSummary(context.Background(), "示例", "POST /channels/c42/posts"); !ok || s != "POST /channels/示例频道/posts" {
		t.Fatalf("summary %q %v", s, ok)
	}

	// Switched off: its API, features and skills go.
	call(t, mux, "PUT", "/api/v1/plugins/example-feed/-/enabled", master, `{"enabled": false}`)
	if _, out := call(t, mux, "GET", "/api/v1/examplefeed/items", master, ""); out["path"] != nil {
		t.Fatalf("still relayed: %v", out)
	}
	if slices.Contains(h.features(), "example_feed") || len(h.extensions.extensionSkills()) != 0 {
		t.Fatal("switched off, it adds nothing")
	}
	if featureCount(h.report().Features, extwire.FeatureUIStream) != 1 {
		t.Fatal("disabling an extension removed manager-owned transport support")
	}
	if lk, _ := h.assistantLark(context.Background()); len(lk.Gates) != 0 || len(lk.MCPServers) != 0 {
		t.Fatalf("switched off, its gate or MCP server stays: %+v", lk)
	}
}

func callReq(t *testing.T, h http.Handler, req *http.Request, token string) (int, map[string]any) {
	t.Helper()
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// A plugin connector declares group with its meta_description (the manifest takes both); the registry takes it with an
// inbox only, and a meta_description longer than a paragraph is refused.
func TestExtensionConnectorDeclaresGroup(t *testing.T) {
	parse := func(caps, desc string) (*extension.Manifest, error) {
		return extension.Parse([]byte(`{"manifest_version": 1, "id": "example-chat", "name": "Example Chat", "version": "1.0.0",
			"engines": {"plugin_protocol": 1}, "runtime": {"command": "bin/chat"},
			"contributes": {"connectors": [{"id": "examplechat", "name": "Example Chat", "capabilities": ` + caps + `,
				"meta_description": ` + strconv.Quote(desc) + `}]}}`))
	}
	m, err := parse(`["inbox", "group", "meta"]`, "示例：messages 是这件事的消息")
	if err != nil {
		t.Fatal(err)
	}
	var items []extension.ConnectorItem
	_ = json.Unmarshal(m.Contributes["connectors"], &items)
	cm := connectorManifest(items[0])
	if !cm.Has(connector.CapGroup) || cm.MetaDescription != "示例：messages 是这件事的消息" {
		t.Fatalf("manifest %+v", cm)
	}
	reg := connector.NewRegistry(nil)
	if err := reg.Add(proc.New(proc.Config{ID: "examplechat", Name: "Example Chat", Manifest: cm})); err != nil {
		t.Fatal(err)
	}
	m, _ = parse(`["status", "group"]`, "")
	_ = json.Unmarshal(m.Contributes["connectors"], &items)
	if err := reg.Add(proc.New(proc.Config{ID: "examplechat2", Name: "x", Manifest: connectorManifest(items[0])})); err == nil ||
		!strings.Contains(err.Error(), "group without an inbox") {
		t.Fatalf("group without an inbox: %v", err)
	}
	if _, err := parse(`["inbox", "group", "meta"]`, strings.Repeat("长", 4001)); err == nil {
		t.Fatal("a meta_description of 4001 characters was taken")
	}
	for caps, desc := range map[string]string{`["inbox", "group"]`: "示例：没声明 meta", `["inbox", "group", "meta"]`: "", `["inbox", "meta"]`: "示例"} {
		if _, err := parse(caps, desc); err == nil {
			t.Errorf("%s with meta_description %q was taken", caps, desc)
		}
	}
	if _, err := parse(`["inbox", "group"]`, ""); err != nil {
		t.Fatalf("group alone: %v", err)
	}
}

// A plugin connector declaring a capability this hub does not have runs with the others, logged once per plugin and
// capability; the hub's own connectors are still held to what they declare.
func TestExtensionConnectorUnknownCapability(t *testing.T) {
	var logs []string
	ps := newExtensions(t.TempDir(), nil, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	// Each plugin and capability is logged once per process: another id each run (go test -count=2).
	id := "example-chat-" + randomHex(4)
	declared := connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, "future_example", connector.CapGroup}}
	for range 2 {
		m := ps.knownOnly(id, declared)
		if !slices.Equal(m.Capabilities, []connector.Capability{connector.CapInbox, connector.CapGroup}) {
			t.Fatalf("capabilities %v", m.Capabilities)
		}
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "plugin "+id+`: this hub has no connector capability "future_example"`) {
		t.Fatalf("logs %q", logs)
	}
	reg := connector.NewRegistry(nil)
	if err := reg.Add(proc.New(proc.Config{ID: "examplechat", Name: "Example Chat", Manifest: ps.knownOnly(id, declared)})); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(proc.New(proc.Config{ID: "examplechat2", Name: "x", Manifest: declared})); err == nil ||
		!strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("not through a plugin: %v", err)
	}
}

// The additive catalog capability does not replace the connector's ordinary
// tools. Older hubs use the same unknown-capability filter for catalog (verified
// against the pre-catalog base); current hubs retain both declarations.
func TestExtensionConnectorCatalogRegistration(t *testing.T) {
	ps := newExtensions(t.TempDir(), nil, func(string, ...any) {})
	declared := connector.Manifest{Capabilities: []connector.Capability{connector.CapTools, connector.CapCatalog}}
	kept := ps.knownOnly("example-catalog", declared)
	if !slices.Equal(kept.Capabilities, declared.Capabilities) {
		t.Fatalf("capabilities %v", kept.Capabilities)
	}
	reg := connector.NewRegistry(nil)
	if err := reg.Add(proc.New(proc.Config{ID: "examplecatalog", Name: "Example Catalog", Manifest: kept})); err != nil {
		t.Fatal(err)
	}
}
