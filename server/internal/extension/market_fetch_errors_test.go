package extension

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// FetchSigned says whether a catalog could not be read (ErrCatalogUnavailable) or was read and does not verify
// (ErrCatalogUnverified), never both, so a network failure is never mistaken for a bad signature and the reverse.
// A catalog mirror that fails verification falls back to the origin, and the origin decides.
func TestFetchSignedClassifiesFailures(t *testing.T) {
	key, sign := testKey(t)
	_, otherSign := testKey(t)
	was := Official
	Official = OfficialMarket{Spec: "github:example/market", SigningKey: key}
	t.Cleanup(func() { Official = was })
	payload := []byte(`{"market_version":1,"name":"Official","extensions":[]}`)
	envelope := sign(payload)
	bad := []byte(strings.Replace(string(envelope), "Official", "Changed", 1))
	notFound := func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	body := func(b []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) }
	}
	for _, c := range []struct {
		name           string
		mirror, origin http.HandlerFunc
		want           error
	}{
		{"origin 404", nil, notFound, ErrCatalogUnavailable},
		{"origin 401", nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, ErrCatalogUnavailable},
		{"origin timeout", nil, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, ErrCatalogUnavailable},
		{"origin unsigned", nil, body(payload), ErrCatalogUnverified},
		{"origin old signer", nil, body(otherSign(payload)), ErrCatalogUnverified},
		{"origin tampered", nil, body(bad), ErrCatalogUnverified},
		{"origin malformed", nil, body([]byte(`{"payload":`)), ErrCatalogUnverified},
		{"bad mirror, origin 404", body(bad), notFound, ErrCatalogUnavailable},
		{"bad mirror, origin unsigned", body(payload), body(payload), ErrCatalogUnverified},
		{"bad mirror, good origin", body(otherSign(payload)), body(envelope), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/mirror.json" {
					c.mirror(w, r)
					return
				}
				c.origin(w, r)
			}))
			defer server.Close()
			spec, _ := ParseMarket("github:example/market")
			market := &Market{Spec: spec, Client: server.Client(), GitHubAPI: server.URL}
			if c.mirror != nil {
				market.CatalogMirror = server.URL + "/mirror.json"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			f, err := market.FetchSigned(ctx)
			if c.want == nil {
				if err != nil || string(f.Raw) != string(envelope) {
					t.Fatalf("FetchSigned: %v", err)
				}
				return
			}
			other := ErrCatalogUnverified
			if c.want == ErrCatalogUnverified {
				other = ErrCatalogUnavailable
			}
			if f != nil || !errors.Is(err, c.want) || errors.Is(err, other) {
				t.Fatalf("FetchSigned: %v, want %v", err, c.want)
			}
		})
	}
}

// ParseCatalog's failures are all verification failures, and keep their localized message.
func TestParseCatalogErrorsAreUnverified(t *testing.T) {
	key, _ := testKey(t)
	_, err := ParseCatalog([]byte(`{"market_version":1,"name":"x","extensions":[]}`), key)
	if !errors.Is(err, ErrCatalogUnverified) || errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("unsigned official catalog: %v", err)
	}
	var m *i18n.Message
	if !errors.As(err, &m) || m.Key != "extension-market_security.signature" || err.Error() != m.Error() {
		t.Fatalf("the localized reason was lost: %q", err.Error())
	}
}
