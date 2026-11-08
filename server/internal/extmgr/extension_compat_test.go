package extmgr

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// The rename to extensions (#1844, docs/extensions/extensions.md "Naming and compatibility"): the new paths answer with the new
// keys, the old paths the apps out there call answer as they always have, and each old call is counted.
func TestExtensionPathsOldAndNew(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	// A package with the old manifest name and engines key installs as it did.
	dir := uiPackage(t)
	if err := os.Rename(filepath.Join(dir, extension.ManifestFile), filepath.Join(dir, extension.LegacyManifestFile)); err != nil {
		t.Fatal(err)
	}
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+dir+`"}`)
	if code != 200 {
		t.Fatalf("%d %v", code, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the extension up", func() bool {
		hp := h.extensions.get("example-ui")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	_, list := call(t, mux, "GET", "/api/v1/extensions", master, "")
	if _, old := list["plugins"]; old || !strings.Contains(fmt.Sprint(list["extensions"]), "id:example-ui") {
		t.Fatalf("new: %v", list)
	}
	_, list = call(t, mux, "GET", "/api/v1/plugins", master, "")
	if _, cur := list["extensions"]; cur || !strings.Contains(fmt.Sprint(list["plugins"]), "id:example-ui") {
		t.Fatalf("old: %v", list)
	}

	_, ui := call(t, mux, "GET", "/api/v1/extensions/-/ui", master, "")
	if s := fmt.Sprint(ui["ui"]); !strings.Contains(s, "extension_name:Example UI") || !strings.Contains(s, "extension:example-ui") || strings.Contains(s, "plugin:") {
		t.Fatalf("new: %s", s)
	}
	_, ui = call(t, mux, "GET", "/api/v1/plugins/-/ui", master, "")
	if s := fmt.Sprint(ui["ui"]); !strings.Contains(s, "plugin_name:Example UI") || strings.Contains(s, "extension") {
		t.Fatalf("old: %s", s)
	}

	_, d := call(t, mux, "GET", "/api/v1/extensions/example-ui/-/", master, "")
	if s := fmt.Sprint(d["ui"]); !strings.Contains(s, "extension:example-ui") || strings.Contains(s, "plugin:") {
		t.Fatalf("new detail: %s", s)
	}
	_, d = call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", master, "")
	if s := fmt.Sprint(d["ui"]); !strings.Contains(s, "plugin:example-ui") || strings.Contains(s, "extension:") || d["id"] != "example-ui" {
		t.Fatalf("old detail: %v", d)
	}

	// Drawn and acted on under both.
	for _, prefix := range []string{"/api/v1/extensions", "/api/v1/plugins"} {
		if code, out := call(t, mux, "POST", prefix+"/example-ui/-/ui/home", master, `{}`); code != 200 || !strings.Contains(fmt.Sprint(out["view"]), "完成的 Agent") {
			t.Fatalf("%s: %d %v", prefix, code, out)
		}
	}

	calls := h.legacyCalls.Snapshot()
	for _, route := range []string{"GET /api/v1/plugins", "GET /api/v1/plugins/-/ui", "GET /api/v1/plugins/{id}/-", "POST /api/v1/plugins/{id}/-/ui/…"} {
		if calls[route] == 0 {
			t.Fatalf("%s not counted: %v", route, calls)
		}
	}
	for route := range calls {
		if strings.Contains(route, "extensions") {
			t.Fatalf("a new path counted as old: %v", calls)
		}
	}
}

// The admin UI out there still uploads to and lists under /api/v1/admin/plugins.
func TestAdminExtensionPathsOldAndNew(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := adminRoutes(h) // the hub's admin session in front (its own tests): relayed with the admin header
	body := packageTarGz(t, notesPackage(t, "1.0.0"))
	for _, p := range []string{legacyAdminUploadPath, adminUploadPath} {
		code, out := callWith(t, mux, "POST", p+"?name=notes-1.0.0.tar.gz", string(body), map[string]string{extwire.HeaderAdmin: "1"}, "application/octet-stream")
		if code != 200 || out["preview_id"] == nil {
			t.Fatalf("%s: %d %v", p, code, out)
		}
	}
	_, list := adminCall(t, mux, "GET", "/api/v1/admin/extensions", "")
	if _, old := list["plugins"]; old || list["extensions"] == nil {
		t.Fatalf("new: %v", list)
	}
	_, list = adminCall(t, mux, "GET", "/api/v1/admin/plugins", "")
	if _, cur := list["extensions"]; cur || extensionIn(list, "telegram") == nil && len(list["plugins"].([]any)) == 0 {
		t.Fatalf("old: %v", list)
	}
}

func TestLegacyRoute(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/plugins":                       "/api/v1/plugins",
		"/api/v1/plugins/":                      "/api/v1/plugins",
		"/api/v1/plugins/-/markets":             "/api/v1/plugins/-/markets",
		"/api/v1/plugins/-/markets/abc/refresh": "/api/v1/plugins/-/markets/…",
		"/api/v1/plugins/voice/ws":              "/api/v1/plugins/{id}/…",
		"/api/v1/plugins/example/-/":            "/api/v1/plugins/{id}/-",
		"/api/v1/plugins/example/-/secrets/key": "/api/v1/plugins/{id}/-/secrets/…",
		"/api/v1/plugins/example":               "/api/v1/plugins/{id}",
	} {
		if got := legacyRoute(path, "/api/v1/plugins"); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}

// A hub that kept its extensions in plugins/ moves them to extensions/ once, leaving a link; never in the way of
// starting.
func TestExtensionsRootMoves(t *testing.T) {
	logf := func(string, ...any) {}
	home := t.TempDir()
	if got := extensionsRoot(home, logf); got != filepath.Join(home, "extensions") {
		t.Fatalf("a new hub: %s", got)
	}

	_ = os.MkdirAll(filepath.Join(home, "plugins", "installed", "x", "1.0.0"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "plugins", "installed", "x", "1.0.0", "persona-plugin.json"), []byte("{}"), 0o644)
	for range 2 { // idempotent
		if got := extensionsRoot(home, logf); got != filepath.Join(home, "extensions") {
			t.Fatalf("moved: %s", got)
		}
		if fi, err := os.Lstat(filepath.Join(home, "plugins")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("plugins is a link: %v %v", fi, err)
		}
		for _, p := range []string{"extensions", "plugins"} {
			if _, err := os.Stat(filepath.Join(home, p, "installed", "x", "1.0.0", "persona-plugin.json")); err != nil {
				t.Fatalf("through %s: %v", p, err)
			}
		}
	}

	both := t.TempDir()
	_ = os.MkdirAll(filepath.Join(both, "plugins"), 0o755)
	_ = os.MkdirAll(filepath.Join(both, "extensions"), 0o755)
	if got := extensionsRoot(both, logf); got != filepath.Join(both, "plugins") {
		t.Fatalf("both there: the old one stays in use, %s", got)
	}
}

func TestExtensionEnvBothNames(t *testing.T) {
	env := withExtensionEnv([]string{"PATH=/bin", "PERSONA_PLUGIN_ID=x", "PERSONA_PLUGIN_SOCKET=/s"})
	for _, want := range []string{"PATH=/bin", "PERSONA_PLUGIN_ID=x", "PERSONA_EXTENSION_ID=x", "PERSONA_PLUGIN_SOCKET=/s", "PERSONA_EXTENSION_SOCKET=/s"} {
		if !slices.Contains(env, want) {
			t.Fatalf("%s: %v", want, env)
		}
	}
}

func TestHubJSONExtensionsBothKeys(t *testing.T) {
	c := &Config{Extensions: []ExtensionConfig{{ID: "voice", Name: "新"}}, LegacyExtensions: []ExtensionConfig{{ID: "voice", Name: "旧"}, {ID: "other"}}}
	got := c.extensionConfigs(func(string, ...any) {})
	if len(got) != 2 || got[0].Name != "新" || got[1].ID != "other" {
		t.Fatalf("%+v", got)
	}
}

// A second private market read with the token of one already registered (token_from): the caller never sees it.
func TestMarketTokenFrom(t *testing.T) {
	h, phone, _ := marketDevices(t)
	mux := h.routes()
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	index := marketIndex(t, "Private", marketEntry(t, dir, "pkg/example-ui.tar.gz", pkg))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-1" || strings.HasSuffix(r.URL.Path, ".sig") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(index)
	}))
	defer srv.Close()
	h.extensions.markets.client = srv.Client()

	code, a := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec": %q, "token": "secret-1"}`, srv.URL+"/a/"))
	if code != 200 {
		t.Fatalf("%d %v", code, a)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec": %q}`, srv.URL+"/b/")); code != 400 {
		t.Fatalf("without a token: %d %v", code, out)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec": %q, "token_from": "nope"}`, srv.URL+"/b/")); code != 404 {
		t.Fatalf("no such market: %d %v", code, out)
	}
	code, b := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec": %q, "token_from": %q}`, srv.URL+"/b/", a["id"]))
	if code != 200 || b["has_token"] != true || strings.Contains(fmt.Sprint(b), "secret-1") {
		t.Fatalf("%d %v", code, b)
	}
}

// A market moves (a renamed repository): same id, so what was installed from it stays its own.
func TestMarketSetSpec(t *testing.T) {
	h, phone, _ := marketDevices(t)
	mux := h.routes()
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	index := marketIndex(t, "Moving market", marketEntry(t, dir, "pkg/example-ui.tar.gz", pkg))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/old/" + extension.IndexFile, "/new/" + extension.IndexFile:
			_, _ = w.Write(index)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h.extensions.markets.client = srv.Client()

	code, mk := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec": %q}`, srv.URL+"/old/"))
	if code != 200 || mk["extensions"] != float64(1) {
		t.Fatalf("%d %v", code, mk)
	}
	id := mk["id"]
	if code, out := call(t, mux, "PUT", fmt.Sprintf("/api/v1/extensions/-/markets/%s", id), phone, `{"spec": "https://127.0.0.1:1/nowhere/"}`); code != 400 {
		t.Fatalf("an address that cannot be read changes nothing: %d %v", code, out)
	}
	code, moved := call(t, mux, "PUT", fmt.Sprintf("/api/v1/plugins/-/markets/%s", id), phone, fmt.Sprintf(`{"spec": %q}`, srv.URL+"/new/"))
	if code != 200 || moved["id"] != id || !strings.HasSuffix(fmt.Sprint(moved["spec"]), "/new/persona-extensions.json") || moved["plugins"] != float64(1) {
		t.Fatalf("%d %v", code, moved)
	}
	_, ms := call(t, mux, "GET", "/api/v1/extensions/-/markets", phone, "")
	if l, _ := ms["markets"].([]any); len(l) != 1 || !strings.Contains(fmt.Sprint(l[0]), "/new/") {
		t.Fatalf("%v", ms)
	}
}
