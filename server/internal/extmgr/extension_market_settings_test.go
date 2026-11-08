package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
)

type failingMarketSettingsStore struct {
	Store
	failRead, failWrite bool
}

func (s *failingMarketSettingsStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == marketSettingsKey && s.failRead {
		return false, errors.New("fixture read failure")
	}
	return s.Store.Setting(ctx, key, out)
}
func (s *failingMarketSettingsStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == marketSettingsKey && s.failWrite {
		return errors.New("fixture write failure")
	}
	return s.Store.PutSetting(ctx, key, value)
}

func TestExtensionMarketSettingsNoCatalogAndRetry(t *testing.T) {
	h := newTestManager(t)
	store := &failingMarketSettingsStore{Store: newExtensionStore()}
	h.store = store
	startTestExtensions(t, h)
	phone, reader := deviceToken("dev_phone", roleFull), deviceToken("dev_watch", roleAnswer)
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	var requests int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "unexpected catalog request", 500)
	}))
	defer server.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = server.Client(), server.URL
	mux := h.routes()
	base := "/api/v1/extensions/-/market-settings"
	for _, method := range []string{"GET", "PUT"} {
		if code, _ := call(t, mux, method, base, reader, `{"registry_mirror":"","catalog_mirror":""}`); code != 403 {
			t.Fatalf("reader %s allowed", method)
		}
	}
	store.failRead = true
	if code, _ := call(t, mux, "GET", base, phone, ""); code != 503 {
		t.Fatal("read failure hidden")
	}
	store.failRead = false
	if code, out := call(t, mux, "GET", base, phone, ""); code != 200 || out["registry_mirror"] != "" {
		t.Fatalf("retry: %d %v", code, out)
	}
	body := `{"registry_mirror":"https://registry.example.com/cache","catalog_mirror":"https://catalog.example.com/persona-extensions.json"}`
	store.failWrite = true
	if code, _ := call(t, mux, "PUT", base, phone, body); code != 503 {
		t.Fatal("write failure hidden")
	}
	store.failWrite = false
	if code, _ := call(t, mux, "PUT", base, phone, body); code != 200 {
		t.Fatal("write retry failed")
	}
	if code, out := call(t, mux, "GET", base, phone, ""); code != 200 || out["registry_mirror"] != "https://registry.example.com/cache" {
		t.Fatal("setting not stored")
	}
	if code, _ := call(t, mux, "PUT", base, phone, `{"registry_mirror":"","catalog_mirror":""}`); code != 200 {
		t.Fatal("clear failed")
	}
	if code, out := call(t, mux, "GET", base, phone, ""); code != 200 || out["registry_mirror"] != "" || out["catalog_mirror"] != "" {
		t.Fatal("clear did not take effect")
	}
	for _, bad := range []string{"http://registry.example.com", "https://user:pass@registry.example.com", "https://registry.example.com/../bad", "https://registry.example.com/?token=fixture", "https://registry.example.com/#fragment"} {
		if code, _ := call(t, mux, "PUT", base, phone, fmt.Sprintf(`{"registry_mirror":%q,"catalog_mirror":""}`, bad)); code != 400 {
			t.Errorf("unsafe mirror allowed: %s", bad)
		}
	}
	if requests != 0 || h.extensions.markets.loaded {
		t.Fatal("opening mirror settings initialized catalogs")
	}
}

func TestExtensionMarketSettingsPostgresRestartAndManagerIsolation(t *testing.T) {
	address := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL is required for the Postgres fixture")
	}
	ctx := context.Background()
	store, err := db.Open(ctx, address, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clear := func() {
		var gone json.RawMessage
		if _, err := store.TakeSetting(ctx, marketSettingsKey, &gone); err != nil {
			t.Error(err)
		}
	}
	clear()
	defer clear()
	h := newTestManager(t)
	h.store = store
	settings := extension.MarketMirrors{RegistryMirror: "https://registry.example.com/mirror", CatalogMirror: "https://catalog.example.com/persona-extensions.json"}
	if err := store.PutSetting(ctx, marketSettingsKey, settings); err != nil {
		t.Fatal(err)
	}
	restarted := newTestManager(t)
	restarted.store = store
	if got, err := restarted.marketSettings(ctx); err != nil || got != settings {
		t.Fatalf("restart: %+v %v", got, err)
	}
	other := newTestManager(t)
	other.store = newKeepStore()
	if got, err := other.marketSettings(ctx); err != nil || got != (extension.MarketMirrors{}) {
		t.Fatal("another manager inherited settings")
	}
	if err := store.PutSetting(ctx, marketSettingsKey, extension.MarketMirrors{}); err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.marketSettings(ctx); err != nil || got != (extension.MarketMirrors{}) {
		t.Fatal("cleared setting resurrected")
	}
	_ = h
}

func TestOfficialCatalogPostgresEnvelopeRestart(t *testing.T) {
	address := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL is required for the Postgres fixture")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, address, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	h := newTestManager(t)
	h.store = database
	startTestExtensions(t, h)
	key, unknown := newMarketKey(t), newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	payload := []byte(" \n{\"market_version\":1,\"name\":\"Official\",\"extensions\":[]}\t\n")
	raw := key.sign(payload)
	gh := &fakeGitHub{files: map[string][]byte{extension.IndexFile: raw}, assets: map[string][]byte{}}
	server := httptest.NewTLSServer(gh)
	defer server.Close()
	h.extensions.markets.client, h.extensions.markets.githubAPI = server.Client(), server.URL
	now := time.Now()
	defer func() {
		waitMarketsRead(t, h)
		for _, id := range []string{"verified-cache", "unknown-cache", "legacy-cache"} {
			_ = database.DeleteExtensionMarket(ctx, id)
		}
		var gone json.RawMessage
		_, _ = database.TakeSetting(ctx, officialMarketKey, &gone)
	}()
	for _, row := range []extension.MarketRecord{
		{ID: "verified-cache", Spec: "github:example/plugins", SigningKey: key.text, Index: raw, FetchedAt: &now, AddedAt: now},
		{ID: "unknown-cache", Spec: "github:example/plugins@branch", SigningKey: unknown.text, Index: raw, FetchedAt: &now, AddedAt: now},
		{ID: "legacy-cache", Spec: "github:example/plugins@legacy", SigningKey: key.text, Index: payload, FetchedAt: &now, AddedAt: now},
	} {
		if err := database.PutExtensionMarket(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.marketsLoad(ctx); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
	h.extensions.markets.mu.Lock()
	known := h.extensions.markets.byID["verified-cache"]
	unrecognized := h.extensions.markets.byID["unknown-cache"]
	if known.index == nil || known.rec.Signature.State != extension.SigSigned {
		t.Error("persisted envelope was not verified")
	}
	var envelope struct {
		Payload string `json:"payload"`
	}
	if json.Unmarshal(known.rec.Index, &envelope) != nil || envelope.Payload != string(payload) {
		t.Error("JSONB changed signed payload bytes")
	}
	if unrecognized.index != nil || !unrecognized.rec.Signature.Bad() || unrecognized.rec.SigningKey != unknown.text {
		t.Error("unknown owner pin was accepted or overwritten")
	}
	h.extensions.markets.mu.Unlock()
	reloadMarkets(h)
	if err := h.marketsLoad(ctx); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
}
