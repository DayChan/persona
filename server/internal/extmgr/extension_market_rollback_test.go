package extmgr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
)

// A rollback restores the verification provenance of the version it goes back to, never the one it leaves: unsigned
// 1.0.0 from an unofficial market, the market moved to the official address, a verified official 1.1.0, then a
// rollback must leave 1.0.0 untrusted and sandboxed by default, in memory and after a reload from storage.
func TestRollbackRestoresVersionProvenance(t *testing.T) {
	h := provenanceTestManager(t, nil, nil, "")
	testRollbackRestoresVersionProvenance(t, h)
}

func TestRollbackRestoresVersionProvenancePostgres(t *testing.T) {
	address := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL is required")
	}
	database, err := db.Open(t.Context(), address, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// The move below saves market "mine" at the official address: leave no row behind for other tests.
	clean := func() {
		_ = database.DeleteExtension(context.Background(), "example-ui")
		_ = database.DeleteExtensionMarket(context.Background(), "mine")
	}
	clean()
	defer clean()
	h := provenanceTestManager(t, nil, nil, "")
	h.store = database
	testRollbackRestoresVersionProvenance(t, h)
}

func testRollbackRestoresVersionProvenance(t *testing.T, h *Manager) {
	t.Helper()
	// Generated-key logic only; this does not accept the production pin.
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	was := officialMarketKeys
	officialMarketKeys = []string{keyIDText(t, key.text)}
	t.Cleanup(func() { officialMarketKeys = was })
	dir := uiPackage(t)
	oldPackage := tarDir(t, dir)
	setVersion(t, dir, "1.1.0")
	newPackage := tarDir(t, dir)
	var catalog []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.tar.gz":
			_, _ = w.Write(oldPackage)
		case "/v2.tar.gz":
			_, _ = w.Write(newPackage)
		default:
			_, _ = w.Write(catalog)
		}
	}))
	defer srv.Close()
	newEntry := marketEntry(t, dir, srv.URL+"/v2.tar.gz", newPackage)
	catalog = key.sign(marketIndex(t, "Official", newEntry))
	setVersion(t, dir, "1.0.0")
	oldEntry := marketEntry(t, dir, srv.URL+"/v1.tar.gz", oldPackage)
	unsigned, err := extension.ParseCatalog(marketIndex(t, "Unsigned", oldEntry), "")
	if err != nil {
		t.Fatal(err)
	}
	h.extensions.markets = markets{loaded: true, client: srv.Client(), githubAPI: srv.URL, byID: map[string]*marketState{
		"mine": {rec: extension.MarketRecord{ID: "mine", Spec: "github:example/other", Signature: extension.MarketSignature{State: extension.SigUnsigned}}, index: unsigned.Index},
	}}
	h.extensions.reg = extension.NewRegistry(h.extensions.root, h.store.(extension.Store), nil, 202611100000)
	mk := &extension.Market{Spec: extension.MarketSpec{Kind: "github", Owner: "example", Repo: "other"}, Client: srv.Client()}
	ctx := context.Background()

	pv, err := h.extensions.reg.PreviewMarket(ctx, mk, "mine", &unsigned.Index.Extensions[0], "")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := h.extensions.reg.Install(ctx, pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Record.MarketKey != "" || h.extensions.fromTrustedMarket(ctx, installed) {
		t.Fatal("unsigned 1.0.0 was trusted at install")
	}
	if _, err := h.marketSetSpec(ctx, "mine", "github:example/plugins"); err != nil {
		t.Fatal(err)
	}
	if h.extensions.fromTrustedMarket(ctx, installed) {
		t.Fatal("moving the market trusted unsigned code")
	}
	mk.Spec.Repo, mk.TrustedKey = "plugins", key.text
	ms := h.marketsState()
	ms.mu.Lock()
	signedEntry := &ms.byID["mine"].index.Extensions[0]
	ms.mu.Unlock()
	pv, err = h.extensions.reg.PreviewMarket(ctx, mk, "mine", signedEntry, "example-ui")
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := h.extensions.reg.Upgrade(ctx, "example-ui", pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Record.MarketKey != key.text || !h.extensions.fromTrustedMarket(ctx, upgraded) {
		t.Fatal("verified 1.1.0 did not record its provenance")
	}

	rolledBack, err := h.extensions.reg.Rollback(ctx, "example-ui", "test")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Record.Version != "1.0.0" {
		t.Fatalf("rolled back to %s", rolledBack.Record.Version)
	}
	check := func(when string, p *extension.Extension) {
		t.Helper()
		st := h.extensions.sandboxOf(ctx, p)
		if p.Record.MarketKey != "" || st.Trusted || !st.Default || !st.Enabled || st.Chosen {
			t.Fatalf("%s: unsigned 1.0.0 inherited the verified 1.1.0 provenance: key=%v %+v", when, p.Record.MarketKey != "", st)
		}
	}
	check("after rollback", rolledBack)
	reopened := extension.NewRegistry(h.extensions.root, h.store.(extension.Store), nil, 202611100000)
	if err := reopened.Load(ctx); err != nil {
		t.Fatal(err)
	}
	h.extensions.reg = reopened
	check("after reload", reopened.Get("example-ui"))

	// The owner's explicit sandbox choice is theirs and survives a rollback either way.
	h.extensions.sandboxes.settings.On = map[string]bool{"example-ui": false}
	if st := h.extensions.sandboxOf(ctx, reopened.Get("example-ui")); !st.Chosen || st.Enabled || st.Trusted {
		t.Fatalf("explicit sandbox choice after rollback: %+v", st)
	}
}
