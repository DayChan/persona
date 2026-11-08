package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
)

type officialRotationStore struct {
	*marketTestStore
	failPin string
}

func (s *officialRotationStore) PutExtensionMarket(ctx context.Context, rec extension.MarketRecord) error {
	if rec.SigningKey == s.failPin && s.failPin != "" {
		return errors.New("synthetic pin migration write failure")
	}
	return s.marketTestStore.PutExtensionMarket(ctx, rec)
}

type rotationCatalogTransport struct {
	http.RoundTripper
	delivered atomic.Bool
}

func (tr *rotationCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := tr.RoundTripper.RoundTrip(r)
	if response != nil && response.StatusCode == http.StatusOK && strings.HasSuffix(r.URL.Path, extension.IndexFile) {
		tr.delivered.Store(true)
	}
	return response, err
}

func rotationGitHub(gh *fakeGitHub) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitHub repository paths are case-insensitive, including older registrations.
		r.URL.Path = strings.Replace(r.URL.Path, "/repos/Example/plugins/", "/repos/example/plugins/", 1)
		gh.ServeHTTP(w, r)
	}))
}

func officialRotationFixture(t *testing.T) (*Manager, extension.MarketRecord, marketKey, marketKey, *fakeGitHub) {
	t.Helper()
	h, _, _ := marketHub(t)
	old, next := newMarketKey(t), newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", next.text)
	extension.Official.PreviousSigningKeys = []string{old.text}
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	payload, err := json.Marshal(map[string]any{"market_version": 1, "name": "Previous catalog",
		"extensions": []any{marketEntry(t, dir, "release:v1/example-ui-1.0.0.tar.gz", pkg)}})
	if err != nil {
		t.Fatal(err)
	}
	rec := extension.MarketRecord{ID: "official-example", Spec: "github:Example/plugins@main", SigningKey: old.text,
		Name: "Previous catalog", Index: old.sign(payload), Signature: extension.MarketSignature{State: extension.SigSigned, KeyID: keyIDText(t, old.text)}, AddedAt: time.Now().Add(-time.Hour)}
	if err := h.marketStore().PutExtensionMarket(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.store.PutSetting(t.Context(), officialMarketKey, map[string]string{"spec": extension.Official.Spec}); err != nil {
		t.Fatal(err)
	}
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: map[string][]byte{}}
	srv := rotationGitHub(gh)
	t.Cleanup(srv.Close)
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	reloadMarkets(h)
	return h, rec, old, next, gh
}

func rotationPayload(t *testing.T, rec extension.MarketRecord, old marketKey) []byte {
	t.Helper()
	f, err := extension.ParseCatalog(rec.Index, old.text)
	if err != nil {
		t.Fatal(err)
	}
	return f.Payload
}

func TestOfficialMarketApprovedPinRotation(t *testing.T) {
	h, before, old, next, gh := officialRotationFixture(t)
	if err := h.marketsLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
	ms := h.marketsState()
	ms.mu.Lock()
	got := ms.byID[before.ID].rec
	ms.mu.Unlock()
	if got.SigningKey != next.text || !got.Signature.Bad() || string(got.Index) != string(before.Index) || !got.AddedAt.Equal(before.AddedAt) {
		t.Fatal("approved rotation did not preserve and quarantine the old envelope")
	}
	if _, _, err := h.marketEntry(before.ID, "example-ui"); err == nil {
		t.Fatal("old envelope was allowed before verification with the new pin")
	}
	if view, err := h.marketRead(t.Context(), before.ID); !errors.Is(err, extension.ErrCatalogUnavailable) ||
		errors.Is(err, extension.ErrCatalogUnverified) || errors.Is(err, errMarketSignature) || !(extension.MarketSignature{State: view.Signature.State}).Bad() {
		t.Fatalf("unavailable catalog changed the rotated quarantine or error category: %+v %v", view, err)
	}
	if _, _, err := h.marketEntry(before.ID, "example-ui"); err == nil {
		t.Fatal("a network failure lifted the rotated catalog's quarantine")
	}
	var index map[string]any
	if err := json.Unmarshal(rotationPayload(t, before, old), &index); err != nil {
		t.Fatal(err)
	}
	index["name"] = "Verified new catalog"
	payload, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(append([]byte(" \n"), payload...), '\n')
	raw := next.sign(payload)
	gh.files[extension.IndexFile] = raw
	if view, err := h.marketRead(t.Context(), before.ID); err != nil || view.Signature.State != extension.SigSigned || view.Signature.KeyID != keyIDText(t, next.text) {
		t.Fatalf("new embedded catalog did not verify: %+v %v", view, err)
	}
	if _, _, err := h.marketEntry(before.ID, "example-ui"); err != nil {
		t.Fatalf("verified new catalog remained blocked: %v", err)
	}
	delete(gh.files, extension.IndexFile)
	if view, err := h.marketRead(t.Context(), before.ID); !errors.Is(err, extension.ErrCatalogUnavailable) ||
		errors.Is(err, extension.ErrCatalogUnverified) || errors.Is(err, errMarketSignature) || view.Signature.State != extension.SigSigned {
		t.Fatalf("unavailable catalog changed a verified rotated catalog: %+v %v", view, err)
	}
	if _, _, err := h.marketEntry(before.ID, "example-ui"); err != nil {
		t.Fatalf("network failure blocked the verified rotated cache: %v", err)
	}
	reloadMarkets(h)
	if err := h.marketsLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
	ms.mu.Lock()
	got = ms.byID[before.ID].rec
	ms.mu.Unlock()
	if got.SigningKey != next.text || got.Signature.State != extension.SigSigned || string(got.Index) != string(raw) {
		t.Fatal("restart changed the verified pin or complete persisted envelope")
	}
	f, err := extension.ParseCatalog(got.Index, next.text)
	if err != nil || string(f.Payload) != string(payload) {
		t.Fatalf("the decoded signed payload bytes changed: %v", err)
	}
}

func TestOfficialMarketRotationPreservesUnapprovedPinsAndRemovedMarkets(t *testing.T) {
	for _, choice := range []string{"unknown", "unsigned", "removed", "no-rotation", "invalid-new-pin"} {
		t.Run(choice, func(t *testing.T) {
			h, rec, old, next, gh := officialRotationFixture(t)
			switch choice {
			case "unknown":
				rec.SigningKey = newMarketKey(t).text
				// Even a genuine newly signed official catalog cannot replace an unknown owner pin.
				gh.files[extension.IndexFile] = next.sign(rotationPayload(t, rec, old))
			case "unsigned":
				rec.SigningKey = ""
			case "removed":
				if err := h.marketStore().DeleteExtensionMarket(t.Context(), rec.ID); err != nil {
					t.Fatal(err)
				}
			case "no-rotation":
				extension.Official.PreviousSigningKeys = nil
			case "invalid-new-pin":
				extension.Official.SigningKey = "not-a-public-key"
			}
			if choice != "removed" {
				if err := h.marketStore().PutExtensionMarket(t.Context(), rec); err != nil {
					t.Fatal(err)
				}
			}
			err := h.marketsLoad(t.Context())
			if choice == "invalid-new-pin" {
				if err == nil {
					t.Fatal("invalid shipped rotation metadata was accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			waitMarketsRead(t, h)
			rows, err := h.marketStore().ExtensionMarkets(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if choice == "removed" {
				if len(rows) != 0 {
					t.Fatal("rotation re-added a market its owner removed")
				}
			} else if len(rows) != 1 || rows[0].SigningKey != rec.SigningKey || string(rows[0].Index) != string(rec.Index) || !rows[0].AddedAt.Equal(rec.AddedAt) {
				t.Fatal("rotation overwrote a pin or retained envelope without an approved replacement")
			}
		})
	}
}

func TestOfficialMarketRotationRejectsUnexpectedSigner(t *testing.T) {
	h, before, _, next, gh := officialRotationFixture(t)
	gh.files[extension.IndexFile] = before.Index
	transport := &rotationCatalogTransport{RoundTripper: h.extensions.markets.client.Transport}
	h.extensions.markets.client.Transport = transport
	if err := h.marketsLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
	transport.delivered.Store(false)
	if view, err := h.marketRead(t.Context(), before.ID); !errors.Is(err, errMarketSignature) ||
		!errors.Is(err, extension.ErrCatalogUnverified) || errors.Is(err, extension.ErrCatalogUnavailable) ||
		!(extension.MarketSignature{State: view.Signature.State}).Bad() {
		t.Fatalf("old embedded signer was accepted or incorrectly classified after rotation: %+v %v", view, err)
	}
	if !transport.delivered.Load() {
		t.Fatal("signature rejection was not backed by a successfully delivered catalog")
	}
	ms := h.marketsState()
	ms.mu.Lock()
	got := ms.byID[before.ID].rec
	ms.mu.Unlock()
	if got.SigningKey != next.text || string(got.Index) != string(before.Index) {
		t.Fatal("rejected signer changed the approved pin or last good envelope")
	}
}

func TestOfficialMarketRotationRemembersLegacyRegistration(t *testing.T) {
	h, before, _, _, _ := officialRotationFixture(t)
	if err := h.store.PutSetting(t.Context(), officialMarketKey, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := h.marketsLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
	if err := h.marketStore().DeleteExtensionMarket(t.Context(), before.ID); err != nil {
		t.Fatal(err)
	}
	reloadMarkets(h)
	if err := h.marketsLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
	rows, err := h.marketStore().ExtensionMarkets(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatal("rotation forgot the legacy registration and re-added a removed market")
	}
}

func TestOfficialMarketRotationWriteFailureIsRetryable(t *testing.T) {
	h, before, _, next, _ := officialRotationFixture(t)
	store := &officialRotationStore{marketTestStore: h.store.(*marketTestStore), failPin: next.text}
	h.store = store
	if err := h.marketsLoad(t.Context()); err == nil {
		t.Fatal("failed pin persistence was reported as a successful load")
	}
	rows, err := store.ExtensionMarkets(t.Context())
	if err != nil || len(rows) != 1 || rows[0].SigningKey != before.SigningKey || string(rows[0].Index) != string(before.Index) {
		t.Fatal("failed pin migration changed the stored pin or complete envelope")
	}
	store.failPin = ""
	if err := h.marketsLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitMarketsRead(t, h)
	rows, err = store.ExtensionMarkets(t.Context())
	if err != nil || len(rows) != 1 || rows[0].SigningKey != next.text || !rows[0].Signature.Bad() {
		t.Fatal("load after a failed write did not retry the approved rotation")
	}
}

func TestOfficialMarketRotationMigratesAllRecognizedAliases(t *testing.T) {
	h, before, _, next, _ := officialRotationFixture(t)
	alias := before
	alias.ID, alias.Spec = "official-alias", "https://raw.githubusercontent.com/example/plugins/main/persona-extensions.json"
	unknown := alias
	unknown.ID, unknown.SigningKey = "unknown-alias", newMarketKey(t).text
	ms := h.marketsState()
	ms.mu.Lock()
	ms.byID = map[string]*marketState{}
	for _, rec := range []extension.MarketRecord{before, alias, unknown} {
		if err := h.marketStore().PutExtensionMarket(t.Context(), rec); err != nil {
			ms.mu.Unlock()
			t.Fatal(err)
		}
		ms.byID[rec.ID] = &marketState{rec: rec}
	}
	_, err := h.seedOfficialMarket(t.Context())
	ms.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := h.marketStore().ExtensionMarkets(t.Context())
	if err != nil || len(rows) != 3 {
		t.Fatalf("alias records: %v", err)
	}
	for _, rec := range rows {
		if rec.ID == unknown.ID {
			if rec.SigningKey != unknown.SigningKey {
				t.Fatal("an unknown alias pin was replaced")
			}
			continue
		}
		if rec.SigningKey != next.text || !rec.Signature.Bad() || string(rec.Index) != string(before.Index) {
			t.Fatal("a recognized repository or HTTPS alias was not migrated and quarantined")
		}
	}
}

func TestOfficialMarketRotationPostgresEnvelopeRestart(t *testing.T) {
	address := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL is required for the Postgres fixture")
	}
	for _, format := range []string{"embedded", "legacy-detached"} {
		t.Run(format, func(t *testing.T) {
			h, before, old, next, gh := officialRotationFixture(t)
			payload := rotationPayload(t, before, old)
			if format == "legacy-detached" {
				before.Index = payload
			}
			ctx := t.Context()
			database, err := db.Open(ctx, address, db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			h.store = database
			defer func() {
				waitMarketsRead(t, h)
				if err := database.DeleteExtensionMarket(ctx, before.ID); err != nil {
					t.Error(err)
				}
				var gone json.RawMessage
				if _, err := database.TakeSetting(ctx, officialMarketKey, &gone); err != nil {
					t.Error(err)
				}
			}()
			if err := database.PutExtensionMarket(ctx, before); err != nil {
				t.Fatal(err)
			}
			if err := database.PutSetting(ctx, officialMarketKey, map[string]string{"spec": extension.Official.Spec}); err != nil {
				t.Fatal(err)
			}
			if err := h.marketsLoad(ctx); err != nil {
				t.Fatal(err)
			}
			waitMarketsRead(t, h)
			rows, err := database.ExtensionMarkets(ctx)
			if err != nil || len(rows) != 1 || rows[0].SigningKey != next.text || !rows[0].Signature.Bad() {
				t.Fatalf("Postgres pin was not migrated and quarantined: %v", err)
			}
			if _, _, err := h.marketEntry(before.ID, "example-ui"); err == nil {
				t.Fatal("retained old catalog authorized installation after rotation")
			}
			if format == "embedded" {
				f, err := extension.ParseCatalog(rows[0].Index, old.text)
				if err != nil || string(f.Payload) != string(payload) {
					t.Fatalf("retained old envelope lost its signed payload: %v", err)
				}
			}
			payload = append(append([]byte(" \n"), payload...), '\t', '\n')
			gh.files[extension.IndexFile] = next.sign(payload)
			if _, err := h.marketRead(ctx, before.ID); err != nil {
				t.Fatal(err)
			}
			reloadMarkets(h)
			if err := h.marketsLoad(ctx); err != nil {
				t.Fatal(err)
			}
			waitMarketsRead(t, h)
			rows, err = database.ExtensionMarkets(ctx)
			if err != nil || len(rows) != 1 || rows[0].SigningKey != next.text || rows[0].Signature.State != extension.SigSigned {
				t.Fatalf("Postgres restart lost the verified pin: %v", err)
			}
			f, err := extension.ParseCatalog(rows[0].Index, next.text)
			if err != nil || string(f.Payload) != string(payload) {
				t.Fatalf("JSONB changed the exact decoded payload bytes: %v", err)
			}
			if _, _, err := h.marketEntry(before.ID, "example-ui"); err != nil {
				t.Fatalf("verified restarted envelope remained blocked: %v", err)
			}
			var envelope struct {
				Payload    string          `json:"payload"`
				Signatures json.RawMessage `json:"signatures"`
			}
			if err := json.Unmarshal(rows[0].Index, &envelope); err != nil {
				t.Fatal(err)
			}
			envelope.Payload += " " // Keep a valid JSON payload but invalidate its signature.
			rows[0].Index, err = json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			// A cached signed state must not substitute for re-verification on restart.
			if err := database.PutExtensionMarket(ctx, rows[0]); err != nil {
				t.Fatal(err)
			}
			delete(gh.files, extension.IndexFile)
			reloadMarkets(h)
			if err := h.marketsLoad(ctx); err != nil {
				t.Fatal(err)
			}
			waitMarketsRead(t, h)
			if _, _, err := h.marketEntry(before.ID, "example-ui"); err == nil {
				t.Fatal("tampered persisted envelope was allowed by its old signed flag")
			}
		})
	}
}
