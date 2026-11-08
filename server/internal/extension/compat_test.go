package extension

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Protocol compatibility still covers the manifest file name, engines.plugin_protocol,
// initialize's "plugin", and an index's "plugins" collection key.

func TestEnginesBothNames(t *testing.T) {
	for _, engines := range []string{`{"extension_protocol": 1}`, `{"plugin_protocol": 1}`, `{"extension_protocol": 1, "plugin_protocol": 1}`} {
		m, err := Parse([]byte(strings.Replace(exampleManifest, `"plugin_protocol": 1`, strings.Trim(engines, "{}"), 1)))
		if err != nil || m.Engines.ExtensionProtocol != 1 {
			t.Fatalf("%s: %v", engines, err)
		}
		// Written out under the old name: a hub rolled back to a build before #1844 reads the manifests it stored.
		if b, _ := json.Marshal(m); !strings.Contains(string(b), `"plugin_protocol":1`) || strings.Contains(string(b), "extension_protocol") {
			t.Fatalf("%s", b)
		}
	}
	if _, err := Parse([]byte(strings.Replace(exampleManifest, `"plugin_protocol": 1`, `"extension_protocol": 1, "plugin_protocol": 2`, 1))); err == nil {
		t.Fatal("two protocols that differ")
	}
	if _, err := Parse([]byte(strings.Replace(exampleManifest, `"plugin_protocol": 1`, `"plugin_protocol": 1, "extension_protocl": 1`, 1))); err == nil {
		t.Fatal("engines is read strictly")
	}
}

func TestManifestFileNames(t *testing.T) {
	dir := t.TempDir()
	if ManifestPath(dir) != filepath.Join(dir, ManifestFile) || hasManifest(dir) {
		t.Fatal("none: the new name")
	}
	_ = os.WriteFile(filepath.Join(dir, LegacyManifestFile), []byte("{}"), 0o644)
	if ManifestPath(dir) != filepath.Join(dir, LegacyManifestFile) || !hasManifest(dir) {
		t.Fatal("only the old name: that one")
	}
	_ = os.WriteFile(filepath.Join(dir, ManifestFile), []byte("{}"), 0o644)
	if ManifestPath(dir) != filepath.Join(dir, ManifestFile) {
		t.Fatal("both: the new name")
	}
	if root, err := packageRoot(filepath.Dir(dir)); err != nil || root != dir {
		t.Fatalf("the only top-level directory: %q %v", root, err)
	}
}

func TestInitializeBothKeys(t *testing.T) {
	b, _ := json.Marshal(InitializeParams{Protocol: 1, Extension: ExtensionInfo{ID: "x", Version: "1.0.0"}})
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if string(raw["extension"]) != string(raw["plugin"]) || !strings.Contains(string(raw["plugin"]), `"id":"x"`) {
		t.Fatalf("%s", b)
	}
	for _, in := range []string{`{"protocol": 1, "plugin": {"id": "x", "version": "1.0.0"}}`, `{"protocol": 1, "extension": {"id": "x", "version": "1.0.0"}}`} {
		var p InitializeParams
		if err := json.Unmarshal([]byte(in), &p); err != nil || p.Extension.ID != "x" || p.Protocol != 1 {
			t.Fatalf("%s: %+v %v", in, p, err)
		}
	}
}

func compatibleIndex(t *testing.T, key string) []byte {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal([]byte(exampleManifest), &manifest); err != nil {
		t.Fatal(err)
	}
	entries := []any{map[string]any{"manifest": manifest, "packages": []any{map[string]any{"url": "x.tar.gz", "sha256": strings.Repeat("a", 64)}}}}
	index, err := json.Marshal(map[string]any{"market_version": 1, "name": "Example", key: entries})
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func TestIndexBothKeys(t *testing.T) {
	for _, key := range []string{"extensions", "plugins"} {
		ix, _, err := ParseIndex(compatibleIndex(t, key))
		if err != nil || len(ix.Extensions) != 1 {
			t.Fatalf("%s: %v", key, err)
		}
	}
}

func TestCanonicalIndexRequests(t *testing.T) {
	for _, scenario := range []struct {
		name, filename string
		canonical      bool
	}{
		{"canonical", IndexFile, true},
		{"canonical missing", "alternate-index.json", false},
	} {
		for _, kind := range []string{"https", "github"} {
			t.Run(scenario.name+"/"+kind, func(t *testing.T) {
				index := compatibleIndex(t, "extensions")
				var mu sync.Mutex
				var requested []string
				srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					name := filepath.Base(r.URL.Path)
					mu.Lock()
					requested = append(requested, name)
					mu.Unlock()
					if name != scenario.filename {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write(index)
				}))
				defer srv.Close()
				address := srv.URL + "/market/"
				if kind == "github" {
					address = "github:example/market"
				}
				spec, err := ParseMarket(address)
				if err != nil {
					t.Fatal(err)
				}
				market := &Market{Spec: spec, Client: srv.Client(), GitHubAPI: srv.URL}
				ix, _, err := market.Fetch(context.Background())
				want := []string{IndexFile}
				if scenario.canonical {
					if err != nil || len(ix.Extensions) != 1 {
						t.Fatalf("canonical index: %v", err)
					}
				} else if err == nil {
					t.Fatal("a missing canonical index was accepted")
				}
				mu.Lock()
				defer mu.Unlock()
				if !slices.Equal(requested, want) {
					t.Fatalf("index/signature requests: %q, want %q; no alternate filenames may be probed", requested, want)
				}
			})
		}
	}
}

func TestExplicitJSONIndexRequests(t *testing.T) {
	const filename = "custom-catalog.json"
	index := compatibleIndex(t, "extensions")
	var mu sync.Mutex
	var requested []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = append(requested, filepath.Base(r.URL.Path))
		mu.Unlock()
		if filepath.Base(r.URL.Path) != filename {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(index)
	}))
	defer srv.Close()
	address := srv.URL + "/market/" + filename
	spec, err := ParseMarket(address)
	if err != nil || spec.URL != address {
		t.Fatalf("explicit JSON index changed: %+v %v", spec, err)
	}
	market := &Market{Spec: spec, Client: srv.Client()}
	if ix, _, err := market.Fetch(context.Background()); err != nil || len(ix.Extensions) != 1 {
		t.Fatalf("explicit JSON index: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{filename}; !slices.Equal(requested, want) {
		t.Fatalf("explicit index/signature requests: %q, want %q", requested, want)
	}
}
