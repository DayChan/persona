package extmgr

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

type marketKey struct {
	text string
	sign func([]byte) []byte
}

func newMarketKey(t *testing.T) marketKey {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return marketKey{"ed25519:" + base64.StdEncoding.EncodeToString(pub), func(payload []byte) []byte {
		b, _ := json.Marshal(map[string]any{"payload": string(payload), "signatures": []map[string]string{{"alg": "ed25519", "key_id": extension.KeyID(pub), "sig": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))}}})
		return b
	}}
}

func keyIDText(t *testing.T, text string) string {
	k, err := extension.ParseSigningKey(text)
	if err != nil {
		t.Fatal(err)
	}
	return extension.KeyID(k)
}

func TestExtensionMarketUnsignedHasNoTOFU(t *testing.T) {
	h, phone, _ := marketHub(t)
	key := newMarketKey(t)
	payload := []byte(`{"market_version":1,"name":"Private","extensions":[]}`)
	raw := key.sign(payload)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer srv.Close()
	h.extensions.markets.client = srv.Client()
	base := "/api/v1/extensions/-/markets"
	code, out := call(t, h.routes(), "POST", base, phone, fmt.Sprintf(`{"spec":%q}`, srv.URL+"/"))
	if code != 200 {
		t.Fatalf("add: %d %v", code, out)
	}
	id := out["id"].(string)
	if rec, _, err := h.marketEntry(id, "missing"); err == nil || rec.SigningKey != "" {
		t.Fatal("unexpected entry")
	}
	h.extensions.markets.mu.Lock()
	s := h.extensions.markets.byID[id]
	if s.rec.SigningKey != "" || s.rec.Signature.State != extension.SigUnsigned {
		t.Fatal("unofficial market acquired trust")
	}
	// Simulate a legacy nonofficial TOFU record: load clears both lock and trust.
	s.rec.SigningKey = key.text
	s.rec.Signature = extension.MarketSignature{State: extension.SigKeyChanged, KeyID: keyIDText(t, key.text)}
	s.rec.Error = "legacy trust lock"
	rec := s.rec
	h.extensions.markets.mu.Unlock()
	if err := h.marketStore().PutExtensionMarket(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	reloadMarkets(h)
	if err := h.marketsLoad(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.extensions.markets.mu.Lock()
	s = h.extensions.markets.byID[id]
	if s.rec.SigningKey != "" || s.rec.Signature.State != extension.SigUnsigned || s.rec.Error != "" {
		t.Fatal("legacy unofficial state still locks catalog")
	}
	h.extensions.markets.mu.Unlock()
	raw = payload
	if code, out := call(t, h.routes(), "POST", base+"/"+id+"/refresh", phone, ""); code != 200 {
		t.Fatalf("unsigned refresh: %d %v", code, out)
	}
	if code, _ := call(t, h.routes(), "POST", base+"/"+id+"/trust", phone, ""); code == 200 {
		t.Fatal("trust route still exists")
	}
}

func TestOfficialCatalogCachedEnvelopeAndUnknownPin(t *testing.T) {
	h, _, _ := marketHub(t)
	key, unknown := newMarketKey(t), newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	now := time.Now()
	payload := []byte(" \n{\"market_version\":1,\"name\":\"Official\",\"extensions\":[]}\n")
	for _, c := range []struct {
		name, pin string
		raw       []byte
		valid     bool
	}{
		{"envelope", key.text, key.sign(payload), true},
		{"legacy", key.text, payload, false},
		{"unknown", unknown.text, key.sign(payload), false},
		{"tampered", key.text, unknown.sign(payload), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := extension.MarketRecord{ID: "cached", Spec: "github:example/plugins", SigningKey: c.pin, Index: c.raw, FetchedAt: &now, AddedAt: now}
			out, ix, err := signedIndex(rec, &extension.Fetched{Raw: c.raw}, now)
			if (err == nil) != c.valid || (ix != nil) != c.valid {
				t.Fatalf("cache policy: %v", err)
			}
			if c.valid && string(out.Index) != string(c.raw) {
				t.Fatal("envelope was rewritten")
			}
			if c.name == "unknown" && out.SigningKey != c.pin {
				t.Fatal("owner pin was replaced")
			}
		})
	}
	_ = h
}
