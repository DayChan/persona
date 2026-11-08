package extmgr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

// officialReadHub is an official market already read once (signed, its index cached) behind a server whose catalog
// answer the test chooses; packages are always served.
func officialReadHub(t *testing.T) (h *Manager, key marketKey, setCatalog func(http.HandlerFunc)) {
	t.Helper()
	key = newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	var mu sync.Mutex
	var catalog http.HandlerFunc
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			_, _ = w.Write(pkg)
			return
		}
		mu.Lock()
		serve := catalog
		mu.Unlock()
		serve(w, r)
	}))
	t.Cleanup(srv.Close)
	raw := key.sign(marketIndex(t, "Official", marketEntry(t, dir, srv.URL+"/pkg.tar.gz", pkg)))
	setCatalog = func(f http.HandlerFunc) { mu.Lock(); catalog = f; mu.Unlock() }
	setCatalog(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) })
	h = provenanceTestManager(t, map[string]*marketState{
		"official": {rec: extension.MarketRecord{ID: "official", Spec: "github:example/plugins", AddedAt: time.Now()}},
	}, srv.Client(), srv.URL)
	h.extensions.reg = extension.NewRegistry(h.extensions.root, h.store.(extension.Store), nil, 202611100000)
	if _, err := h.marketRead(context.Background(), "official"); err != nil {
		t.Fatal(err)
	}
	if st := officialSignature(h); st.State != extension.SigSigned {
		t.Fatalf("first read: %+v", st)
	}
	return h, key, setCatalog
}

func officialSignature(h *Manager) extension.MarketSignature {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.byID["official"].rec.Signature
}

// installFromCache previews and installs example-ui from the official market's kept index, as the install route does.
func installFromCache(t *testing.T, h *Manager) error {
	t.Helper()
	ctx := context.Background()
	rec, e, err := h.marketEntry("official", "example-ui")
	if err != nil {
		return err
	}
	mk, err := h.market(rec)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := h.extensions.reg.PreviewMarket(ctx, mk, "official", e, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.extensions.reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	return nil
}

// A catalog the official market cannot read (404, 401, 500, a timeout) is a network problem, not a signature
// failure: the last verified index and its signed state stay, the error is recorded, and installs keep working.
func TestOfficialMarketReadFailureKeepsVerifiedCatalog(t *testing.T) {
	for _, c := range []struct {
		name  string
		serve http.HandlerFunc
	}{
		{"not found", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }},
		{"unauthorized", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }},
		{"server error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }},
		{"timeout", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, key, setCatalog := officialReadHub(t)
			setCatalog(c.serve)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_, err := h.marketRead(ctx, "official")
			if err == nil || errors.Is(err, errMarketSignature) || errors.Is(err, extension.ErrCatalogUnverified) {
				t.Fatalf("a read failure was reported as a signature failure: %v", err)
			}
			if !errors.Is(err, extension.ErrCatalogUnavailable) {
				t.Fatalf("a read failure is not typed as unavailable: %v", err)
			}
			if st := officialSignature(h); st.State != extension.SigSigned || st.KeyID != keyIDText(t, key.text) {
				t.Fatalf("a read failure changed the signature state: %+v", st)
			}
			ms := h.marketsState()
			ms.mu.Lock()
			s := ms.byID["official"]
			kept, recorded := s.index != nil && len(s.index.Extensions) == 1, s.rec.Error != ""
			ms.mu.Unlock()
			if !kept || !recorded {
				t.Fatalf("index kept %v, error recorded %v", kept, recorded)
			}
			if err := installFromCache(t, h); err != nil {
				t.Fatalf("the verified cached catalog no longer installs: %v", err)
			}
		})
	}
}

// A catalog that was read (HTTP 200) and does not verify quarantines the market: unsigned, signed by another (old)
// signer, a broken signature, or not a catalog at all. Nothing installs, not even from the last good index.
func TestOfficialMarketUnverifiedCatalogQuarantines(t *testing.T) {
	other := newMarketKey(t)
	for _, c := range []struct {
		name string
		body func(signed marketKey) []byte
	}{
		{"unsigned", func(marketKey) []byte { return []byte(`{"market_version":1,"name":"Official","extensions":[]}`) }},
		{"old signer", func(marketKey) []byte {
			return other.sign([]byte(`{"market_version":1,"name":"Official","extensions":[]}`))
		}},
		{"bad signature", func(k marketKey) []byte {
			return []byte(strings.Replace(string(k.sign([]byte(`{"market_version":1,"name":"Official","extensions":[]}`))), "Official", "Offici4l", 1))
		}},
		{"malformed", func(marketKey) []byte { return []byte(`{"payload":`) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, key, setCatalog := officialReadHub(t)
			body := c.body(key)
			served := make(chan struct{}, 4)
			setCatalog(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				served <- struct{}{}
			})
			_, err := h.marketRead(context.Background(), "official")
			select {
			case <-served:
			default:
				t.Fatal("the negative case never received an HTTP 200 catalog")
			}
			if !errors.Is(err, errMarketSignature) || !errors.Is(err, extension.ErrCatalogUnverified) || errors.Is(err, extension.ErrCatalogUnavailable) {
				t.Fatalf("an unverified catalog was not a signature failure: %v", err)
			}
			if st := officialSignature(h); st.State != extension.SigInvalid {
				t.Fatalf("an unverified catalog left the market %+v", st)
			}
			if err := installFromCache(t, h); err == nil {
				t.Fatal("installed from a quarantined market")
			}
			// A network failure afterwards does not lift the quarantine.
			setCatalog(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
			if _, err := h.marketRead(context.Background(), "official"); errors.Is(err, errMarketSignature) {
				t.Fatalf("a later network failure was a signature failure: %v", err)
			}
			if st := officialSignature(h); st.State != extension.SigInvalid {
				t.Fatalf("a later network failure lifted the quarantine: %+v", st)
			}
		})
	}
}
