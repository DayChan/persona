package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/quota"
	"github.com/nwverse/persona/server/internal/rpc"
)

// serveQuotaExtensionForTest is a quota plugin: it asks the launcher's gateway for /me/usage with the key in its
// Authorization (a gateway that answers 404 is not its), and reports what it saw — never the key itself.
func serveQuotaExtensionForTest() {
	var hub *pserve.Hub
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-quota", Version: "1.0.0",
		Init: func(_ context.Context, _ extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			hub = h
			return nil, nil
		},
		Namespaces: map[string]pserve.Handler{"quota": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != "query" {
				return nil, pserve.UnknownMethod(method)
			}
			var p struct {
				QueryID string `json:"query_id"`
				BaseURL string `json:"base_url"`
			}
			_ = json.Unmarshal(params, &p)
			// Another plugin's query, and somewhere else than the gateway: both refused.
			var probe quota.Response
			stolen := hub.Call(ctx, "quota.http", map[string]any{"query_id": "not-mine", "url": "/me/usage"}, &probe)
			away := hub.Call(ctx, "quota.http", map[string]any{"query_id": p.QueryID, "url": "https://elsewhere.example.com/x",
				"headers": map[string]string{"Authorization": "Bearer " + quota.KeyPlaceholder}}, &probe)
			var resp quota.Response
			if err := hub.Call(ctx, "quota.http", map[string]any{"query_id": p.QueryID, "method": "GET", "url": "/me/usage",
				"headers": map[string]string{"Authorization": "Bearer " + quota.KeyPlaceholder}}, &resp); err != nil {
				return nil, err
			}
			if resp.Status == http.StatusNotFound {
				return nil, &rpc.Error{Code: extension.CodeUnsupported, Message: "not this gateway"}
			}
			note := fmt.Sprintf("stolen=%v away=%v body=%s", stolen != nil, away != nil, resp.Body)
			return map[string]any{"report": quota.Report{KeyName: "example key", Note: note,
				Usage: []quota.Usage{{Label: "今日", Requests: 3}}}}, nil
		}}})
}

func quotaPackage(t *testing.T) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "quota"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=quota exec "+self+" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-quota", "name": "Example 额度", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/quota"}},
		"contributes": {"quota": [{"id": "example-gw", "name": "Example 网关"}]},
		"permissions": {"launcher_keys": true}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A launcher's quota is read by a quota plugin that never sees its key: whoever holds the key (the assistant for one
// added in Persona) sends its requests with the key put in, to the launcher's own gateway only, and takes the key out
// of the answer.
func TestQuotaThroughAnExtension(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	const key = "sk-example-launcher-key"
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"key":%q,"today":3}`, key)
	}))
	defer gw.Close()
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	// Launchers added in Persona: the hub holds their keys and sends with them (quota.send); the manager asks the quota
	// plugins with the gateway and the hub's sender (extensions.hub_call quota, from the hub's launcherQuota).
	b1Launcher(h, "gwlauncher", gw.URL+"/v1", key)
	b1Launcher(h, "otherlauncher", other.URL, key)
	_ = context.Background()
	quotaOf := func(name, base string) (quota.Report, error) {
		raw, err := pluginsCall(t, h, assistantwire.PluginsQuota, map[string]string{"name": name, "base_url": base, "tz": ""})
		if err != nil {
			return quota.Report{}, err
		}
		var out struct {
			Report quota.Report `json:"report"`
		}
		_ = json.Unmarshal(raw, &out)
		return out.Report, nil
	}
	if _, err := quotaOf("gwlauncher", gw.URL+"/v1"); err == nil || !strings.Contains(err.Error(), quota.ErrUnsupported.Error()) {
		t.Fatalf("no quota plugin yet: %v", err)
	}

	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+quotaPackage(t)+`"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}

	r, err := quotaOf("gwlauncher", gw.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	var q map[string]any
	_ = json.Unmarshal(b, &q)
	note := fmt.Sprint(q["note"])
	if q["gateway"] != "example-gw" || q["gateway_name"] != "Example 网关" || q["key_name"] != "example key" {
		t.Fatalf("%v", q)
	}
	if strings.Contains(note, key) || !strings.Contains(note, quota.KeyPlaceholder) {
		t.Fatalf("the plugin saw the key: %s", note)
	}
	if !strings.Contains(note, "stolen=true") || !strings.Contains(note, "away=true") {
		t.Fatalf("another query's or another host's request went out: %s", note)
	}

	if _, err := quotaOf("otherlauncher", other.URL); err == nil || !strings.Contains(err.Error(), quota.ErrUnsupported.Error()) {
		t.Fatalf("a gateway the plugin does not read: %v", err)
	}
}

// A plugin's model provider is offered when a launcher is added (the launchers are the hub's: it asks for the presets,
// plugins.presets, and fills in what the user leaves out, extmgr/models_link.go).
func TestProviderPresets(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(`{"manifest_version": 1, "id": "example-providers", "name": "Example 供应商",
		"version": "1.0.0", "contributes": {"providers": [{"id": "example-ai", "name": "Example AI", "kind": "api", "api": "responses",
		"base_url": "https://api.example.com/v1", "models": [{"id": "example-large", "name": "Large"}, {"id": "example-small"}],
		"key_hint": "在控制台 › API Keys 里创建"}]}}`), 0o644)
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+dir+`"}`)
	if lines := pv["permissions"].([]any); len(lines) != 0 {
		t.Fatalf("a declarative plugin runs nothing and asks nothing: %v", lines)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 || out["status"] != "running" {
		t.Fatalf("%d %v", code, out)
	}
	b, err := pluginsCall(t, h, assistantwire.PluginsPresets, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Presets []map[string]any `json:"presets"`
	}
	_ = json.Unmarshal(b, &out)
	if len(out.Presets) != 1 {
		t.Fatal(out.Presets)
	}
	pr := out.Presets[0]
	if pr["id"] != "example-providers/example-ai" || pr["kind"] != "api" || len(pr["models"].([]any)) != 2 || pr["base_url"] != "https://api.example.com/v1" ||
		pr["api"] != "responses" || pr["key_hint"] != "在控制台 › API Keys 里创建" {
		t.Fatalf("%v", pr)
	}
}

// Outside a launcher's page an extension reaches the gateways its quota and launcher sections name, every one when one
// of them names none.
func TestGatewayHosts(t *testing.T) {
	for _, c := range []struct {
		contributes string
		hosts       string
		any         bool
	}{
		{`{"quota": [{"id": "q", "name": "Q", "hosts": ["gw.example.com"]}], "ui": [{"id": "s", "title": "S", "placement": "launcher_section", "hosts": ["*.example.org"]}, {"id": "n", "title": "N", "placement": "nav_page"}]}`, "[gw.example.com *.example.org]", false},
		{`{"quota": [{"id": "q", "name": "Q", "hosts": ["gw.example.com"]}], "ui": [{"id": "s", "title": "S", "placement": "launcher_section"}]}`, "[]", true},
		{`{"quota": [{"id": "q", "name": "Q"}]}`, "[]", true},
	} {
		m, err := extension.Parse([]byte(`{"manifest_version": 1, "id": "gw", "name": "GW", "version": "1.0.0", "engines": {"plugin_protocol": 1},
			"runtime": {"command": {"linux-amd64": "bin/gw"}}, "permissions": {"launcher_keys": true}, "contributes": ` + c.contributes + `}`))
		if err != nil {
			t.Fatal(err)
		}
		hosts, any := gatewayHosts(&extension.Extension{Manifest: m, Full: m})
		if fmt.Sprint(hosts) != c.hosts && !(c.hosts == "[]" && hosts == nil) || any != c.any {
			t.Fatalf("%s: %v %v", c.contributes, hosts, any)
		}
	}
}
