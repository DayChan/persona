package extmgr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
)

func provenanceTestManager(t *testing.T, states map[string]*marketState, client *http.Client, api string) *Manager {
	t.Helper()
	h := &Manager{store: newExtensionStore(), cfg: &Config{}, opts: Options{Logf: t.Logf}}
	h.extensions = &extensions{h: h, root: t.TempDir()}
	h.extensions.markets = markets{loaded: true, byID: states, client: client, githubAPI: api}
	return h
}

func TestMarketMoveCannotCreateSecondOfficial(t *testing.T) {
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	raw := key.sign([]byte(`{"market_version":1,"name":"Official","extensions":[]}`))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer srv.Close()
	states := map[string]*marketState{
		"official": {rec: extension.MarketRecord{ID: "official", Spec: "github:example/plugins", SigningKey: key.text, AddedAt: time.Now()}},
		"mine":     {rec: extension.MarketRecord{ID: "mine", Spec: "github:example/other", AddedAt: time.Now()}},
	}
	h := provenanceTestManager(t, states, srv.Client(), srv.URL)
	if _, err := h.marketSetSpec(context.Background(), "mine", "github:example/plugins@branch"); err == nil {
		t.Fatal("move accepted a second official market through a branch alias")
	}
}

func TestMarketMoveRechecksTargetAfterFetch(t *testing.T) {
	for _, target := range []string{"github:example/plugins@branch", "github:example/other"} {
		t.Run(target, func(t *testing.T) {
			key := newMarketKey(t)
			useOfficialMarket(t, "github:example/plugins", key.text)
			raw := key.sign([]byte(`{"market_version":1,"name":"Target","extensions":[]}`))
			started, release := make(chan struct{}), make(chan struct{})
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = w.Write(raw)
			}))
			defer srv.Close()
			h := provenanceTestManager(t, map[string]*marketState{"mine": {rec: extension.MarketRecord{ID: "mine", Spec: "github:example/source"}}}, srv.Client(), srv.URL)
			result := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() { _, err := h.marketSetSpec(ctx, "mine", target); result <- err }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("target fetch did not start")
			}
			claimed := target
			if extension.IsOfficialMarket(target) {
				claimed = "github:example/plugins"
			}
			ms := h.marketsState()
			ms.mu.Lock()
			ms.byID["winner"] = &marketState{rec: extension.MarketRecord{ID: "winner", Spec: claimed}}
			ms.mu.Unlock()
			close(release)
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("concurrent target claim was overwritten")
				}
			case <-ctx.Done():
				t.Fatal("move did not finish")
			}
			ms.mu.Lock()
			defer ms.mu.Unlock()
			if ms.byID["mine"].rec.Spec != "github:example/source" {
				t.Fatal("rejected move changed the source")
			}
		})
	}
}

func TestInstalledMarketKeySnapshot(t *testing.T) {
	h := provenanceTestManager(t, nil, nil, "")
	testInstalledMarketKeySnapshot(t, h)
}

func TestInstalledMarketKeyPostgresRestart(t *testing.T) {
	address := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL is required")
	}
	database, err := db.Open(t.Context(), address, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	h := provenanceTestManager(t, nil, nil, "")
	h.store = database
	defer database.DeleteExtension(context.Background(), "example-ui")
	testInstalledMarketKeySnapshot(t, h)
}

func testInstalledMarketKeySnapshot(t *testing.T, h *Manager) {
	t.Helper()
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	was := officialMarketKeys
	officialMarketKeys = []string{keyIDText(t, key.text)}
	t.Cleanup(func() { officialMarketKeys = was })
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pkg) }))
	defer server.Close()
	entry := marketEntry(t, dir, server.URL+"/pkg.tar.gz", pkg)
	raw := key.sign(marketIndex(t, "Official", entry))
	f, err := extension.ParseCatalog(raw, key.text)
	if err != nil {
		t.Fatal(err)
	}
	h.extensions.reg = extension.NewRegistry(h.extensions.root, h.store.(extension.Store), nil, 202611100000)
	mk := &extension.Market{Spec: extension.MarketSpec{Kind: "github", Owner: "example", Repo: "plugins"}, TrustedKey: key.text, Client: server.Client()}
	h.extensions.markets.byID = map[string]*marketState{"official": {rec: extension.MarketRecord{ID: "official", Spec: mk.Spec.String(), Signature: extension.MarketSignature{State: extension.SigSigned, KeyID: keyIDText(t, key.text)}}, index: f.Index}}
	pv, err := h.extensions.reg.PreviewMarket(t.Context(), mk, "official", &f.Index.Extensions[0], "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.extensions.reg.Install(t.Context(), pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.MarketKey != key.text || !h.extensions.fromTrustedMarket(t.Context(), p) {
		t.Fatal("verified installed package lost its provenance")
	}
	// Reload from storage, not an in-memory preview or mutable market key.
	reopened := extension.NewRegistry(h.extensions.root, h.store.(extension.Store), nil, 202611100000)
	if err := reopened.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.extensions.reg = reopened
	p = reopened.Get("example-ui")
	if p == nil || p.Record.MarketKey != key.text || !h.extensions.fromTrustedMarket(t.Context(), p) {
		t.Fatal("storage reload lost the verified snapshot")
	}
	// An owner choice remains authoritative even for officially verified code.
	h.extensions.sandboxes.settings.On = map[string]bool{"example-ui": true}
	boxed := &extension.Extension{Record: p.Record, Dir: p.Dir, Manifest: &extension.Manifest{Runtime: &extension.Runtime{}}}
	if st := h.extensions.sandboxOf(t.Context(), boxed); !st.Chosen || !st.Enabled || !st.Trusted {
		t.Fatalf("explicit choice: %+v", st)
	}
	// Installing owner-provided bytes is not allowed to inherit old provenance.
	manifest, err := os.ReadFile(filepath.Join(dir, extension.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	manifest = []byte(strings.Replace(string(manifest), "1.0.0", "1.1.0", 1))
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	pv, err = reopened.Preview(t.Context(), "dir:"+dir, "example-ui")
	if err != nil {
		t.Fatal(err)
	}
	p, err = reopened.Upgrade(t.Context(), "example-ui", pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.MarketKey != "" || h.extensions.fromTrustedMarket(t.Context(), p) {
		t.Fatal("unsigned upgrade retained the official snapshot")
	}
	if err := reopened.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reopened.Get("example-ui").Record.MarketKey != "" {
		t.Fatal("cleared snapshot resurrected on reload")
	}
}

func TestSandboxOnlyFinalOfficialKeyIsAllowlisted(t *testing.T) {
	if len(officialMarketKeys) != 1 || officialMarketKeys[0] != "3cce0f471911a1cf" {
		t.Fatal("sandbox allowlist must contain only the final key")
	}
	retired := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", retired.text)
	h := provenanceTestManager(t, map[string]*marketState{"official": {rec: extension.MarketRecord{ID: "official", Spec: "github:example/plugins", Signature: extension.MarketSignature{State: extension.SigSigned, KeyID: keyIDText(t, retired.text)}}}}, nil, "")
	p := &extension.Extension{Record: extension.Record{ID: "retired-code", Source: extension.SourceInstalled, Origin: "market:official/retired-code", MarketKey: retired.text}, Manifest: &extension.Manifest{Runtime: &extension.Runtime{}}}
	if st := h.extensions.sandboxOf(t.Context(), p); st.Trusted || !st.Default || !st.Enabled {
		t.Fatalf("nonallowlisted verified key was exempted: %+v", st)
	}
}

func TestUnsignedInstallNeverGainsSandboxTrustAfterMove(t *testing.T) {
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	originalKeys := officialMarketKeys
	// This tests provenance with a generated key, not acceptance of the production pin.
	officialMarketKeys = []string{keyIDText(t, key.text)}
	t.Cleanup(func() { officialMarketKeys = originalKeys })
	raw := key.sign([]byte(`{"market_version":1,"name":"Official","extensions":[]}`))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer srv.Close()
	states := map[string]*marketState{"mine": {rec: extension.MarketRecord{ID: "mine", Spec: "github:example/other", AddedAt: time.Now(), Signature: extension.MarketSignature{State: extension.SigUnsigned}}}}
	h := provenanceTestManager(t, states, srv.Client(), srv.URL)
	installed := &extension.Extension{Record: extension.Record{ID: "unrelated-unsigned-code", Source: extension.SourceInstalled, Origin: "market:mine/unrelated-unsigned-code"}, Manifest: &extension.Manifest{ID: "unrelated-unsigned-code", Runtime: &extension.Runtime{}}}
	if h.extensions.fromTrustedMarket(context.Background(), installed) {
		t.Fatal("unexpected initial exemption")
	}
	if _, err := h.marketSetSpec(context.Background(), "mine", "github:example/plugins"); err != nil {
		t.Fatal(err)
	}
	if h.extensions.fromTrustedMarket(context.Background(), installed) {
		t.Fatal("unsigned installed code gained an exemption despite being absent from the signed catalog")
	}
	h.extensions.sandboxes.settings.On = map[string]bool{installed.Record.ID: false}
	if st := h.extensions.sandboxOf(context.Background(), installed); !st.Chosen || st.Enabled || !st.Default {
		t.Fatalf("owner's explicit sandbox choice was not preserved: %+v", st)
	}
}
