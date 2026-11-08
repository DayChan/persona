package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestExtensionMarketOCIInstallUnpacksArchive(t *testing.T) {
	h, phone, _ := marketHub(t)
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "artifactType": extension.OCIArtifactType, "config": map[string]any{}, "layers": []any{map[string]any{"mediaType": extension.OCILayerType, "digest": "sha256:" + sum(pkg), "size": len(pkg), "annotations": map[string]string{"org.opencontainers.image.title": "example-ui.tar.gz"}}}})
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("private catalog token reached unrelated registry")
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			_, _ = w.Write(manifest)
		} else {
			_, _ = w.Write(pkg)
		}
	}))
	defer registry.Close()
	ref := "oci:" + strings.TrimPrefix(registry.URL, "http://") + "/example/market/example-ui@sha256:" + sum(manifest)
	index := marketIndex(t, "OCI", marketEntry(t, dir, ref, pkg))
	catalog := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write(index)
	}))
	defer catalog.Close()
	h.extensions.markets.client = catalog.Client()
	mux := h.routes()
	code, mk := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec":%q,"token":"fixture-secret"}`, catalog.URL+"/"))
	if code != 200 {
		t.Fatalf("market: %d %v", code, mk)
	}
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"example-ui"}`, mk["id"]))
	if code != 200 {
		t.Fatalf("preview/unpack: %d %v", code, pv)
	}
	code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", phone, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 || out["version"] != "1.0.0" {
		t.Fatalf("install: %d %v", code, out)
	}
	if current := h.extensions.reg.Get("example-ui"); current == nil || extension.FromMarket(current.Record.Origin) != mk["id"] {
		t.Fatal("installed source lost catalog identity")
	}
}

func TestOfficialMarketReaddAndEmptyPinCannotBecomeUnsigned(t *testing.T) {
	h, phone, _ := marketHub(t)
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	payload := []byte(`{"market_version":1,"name":"Official","extensions":[]}`)
	raw := key.sign(payload)
	gh := &fakeGitHub{files: map[string][]byte{extension.IndexFile: raw}, assets: map[string][]byte{}}
	server := httptest.NewTLSServer(gh)
	defer server.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = server.Client(), server.URL
	list := marketList(t, h, phone)
	waitMarketsRead(t, h)
	id := fmt.Sprint(list[0]["id"])
	if code, _ := call(t, h.routes(), "DELETE", "/api/v1/extensions/-/markets/"+id, phone, ""); code != 200 {
		t.Fatal("delete failed")
	}
	gh.mu.Lock()
	gh.files[extension.IndexFile] = payload
	gh.mu.Unlock()
	for _, spec := range []string{"github:example/plugins@main", "https://github.com/example/plugins"} {
		if code, _ := call(t, h.routes(), "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec":%q,"token":"fixture-secret"}`, spec)); code == 200 {
			t.Fatal("official re-add accepted unsigned document")
		}
	}
	gh.mu.Lock()
	gh.files[extension.IndexFile] = raw
	gh.mu.Unlock()
	code, out := call(t, h.routes(), "POST", "/api/v1/extensions/-/markets", phone, `{"spec":"github:example/plugins@main"}`)
	if code != 200 {
		t.Fatalf("verified readd: %d %v", code, out)
	}
	id = out["id"].(string)
	reloadMarkets(h)
	if err := h.marketsLoad(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.extensions.markets.mu.Lock()
	state := h.extensions.markets.byID[id]
	if state.rec.Signature.State != extension.SigSigned || state.rec.Signature.KeyID != keyIDText(t, key.text) {
		t.Fatal("cache not reverified")
	}
	h.extensions.markets.mu.Unlock()
}
