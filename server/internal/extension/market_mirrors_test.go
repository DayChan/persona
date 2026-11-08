package extension

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOfficialCatalogMirrorVerifiedFallback(t *testing.T) {
	key, sign := testKey(t)
	was := Official
	Official = OfficialMarket{Spec: "github:example/market", SigningKey: key}
	t.Cleanup(func() { Official = was })
	payload := []byte(`{"market_version":1,"name":"Official","extensions":[]}`)
	envelope := sign(payload)
	for _, mode := range []string{"valid", "tampered", "unsigned", "missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var upstream, mirror, leaked atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/mirror.json" {
					mirror.Add(1)
					if r.Header.Get("Authorization") != "" {
						leaked.Add(1)
					}
					switch mode {
					case "valid":
						_, _ = w.Write(envelope)
					case "tampered":
						_, _ = w.Write([]byte(strings.Replace(string(envelope), "Official", "Changed", 1)))
					case "unsigned":
						_, _ = w.Write(payload)
					case "missing":
						http.NotFound(w, r)
					case "malformed":
						_, _ = w.Write([]byte(`{"payload":"{}","signatures":[]}`))
					}
					return
				}
				upstream.Add(1)
				if r.Header.Get("Authorization") != "Bearer fixture-secret" {
					t.Error("upstream market token missing")
				}
				_, _ = w.Write(envelope)
			}))
			defer server.Close()
			spec, _ := ParseMarket("github:example/market")
			market := &Market{Spec: spec, Token: "fixture-secret", Client: server.Client(), GitHubAPI: server.URL, CatalogMirror: server.URL + "/mirror.json"}
			f, err := market.FetchSigned(context.Background())
			if err != nil || string(f.Raw) != string(envelope) {
				t.Fatalf("catalog mirror: %v", err)
			}
			if leaked.Load() != 0 || mirror.Load() != 1 {
				t.Fatal("catalog mirror credentials or requests incorrect")
			}
			if mode == "valid" && upstream.Load() != 0 || mode != "valid" && upstream.Load() != 1 {
				t.Fatal("catalog mirror fallback incorrect")
			}
		})
	}
}

func TestMirrorURLValidation(t *testing.T) {
	for _, mirror := range []MarketMirrors{
		{}, {RegistryMirror: "https://registry.example.com/"}, {RegistryMirror: "https://registry.example.com/cache", CatalogMirror: "https://catalog.example.com/persona-extensions.json"}, {RegistryMirror: "http://127.0.0.1:12345/cache"},
	} {
		if err := ValidateMarketMirrors(mirror); err != nil {
			t.Fatal(err)
		}
	}
	for _, mirror := range []MarketMirrors{
		{RegistryMirror: "http://registry.example.com/cache"}, {RegistryMirror: "https://user:pass@registry.example.com/cache"}, {RegistryMirror: "https://registry.example.com/cache?token=fixture"}, {RegistryMirror: "https://registry.example.com/cache#fragment"}, {RegistryMirror: "https://registry.example.com/%2e%2e/cache"}, {CatalogMirror: "https://catalog.example.com/"},
	} {
		if err := ValidateMarketMirrors(mirror); err == nil {
			t.Fatalf("unsafe mirror accepted: %+v", mirror)
		}
	}
}
