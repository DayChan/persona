package extension

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOCIHTTPSRedirectDoesNotLeakEitherCredential(t *testing.T) {
	blob := []byte("verified archive")
	manifest := ociTestManifest(t, blob)
	var leaks, cdn atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "cdn.example.com" || strings.HasPrefix(r.URL.Path, "/v2/other/") {
			cdn.Add(1)
			if r.Header.Get("Authorization") != "" {
				leaks.Add(1)
			}
			if r.Host == "cdn.example.com" {
				http.Redirect(w, r, "https://registry.example.com/v2/other/ext/blobs/sha256:"+ociSum(blob), 302)
			} else {
				_, _ = w.Write(blob)
			}
			return
		}
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"token":"short-lived-fixture"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer short-lived-fixture" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://registry.example.com/token",service="registry.example.com",scope="repository:example/ext:pull"`)
			w.WriteHeader(401)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			_, _ = w.Write(manifest)
		} else {
			http.Redirect(w, r, "https://cdn.example.com/archive", 302)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	m := &Market{Spec: MarketSpec{Kind: "https", URL: "https://registry.example.com/persona-extensions.json"}, Token: "fixture-secret", Client: &http.Client{Transport: mappedRegistryTransport{target, server.Client().Transport}}}
	pkg := &MarketPackage{URL: "oci:registry.example.com/example/ext@sha256:" + ociSum(manifest), SHA256: ociSum(blob), Size: int64(len(blob))}
	if _, _, err := m.Download(context.Background(), pkg, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if leaks.Load() != 0 || cdn.Load() != 2 {
		t.Fatal("market or registry bearer leaked on redirects")
	}
}

func TestOCIAnonymousSuccessAndRealmRedirectRefusal(t *testing.T) {
	blob := []byte("archive")
	manifest := ociTestManifest(t, blob)
	for _, redirectRealm := range []bool{false, true} {
		t.Run(fmt.Sprint(redirectRealm), func(t *testing.T) {
			var capture, tokens, leaks atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/capture" {
					capture.Add(1)
					return
				}
				if r.URL.Path == "/token" {
					tokens.Add(1)
					if r.Header.Get("Authorization") != "" {
						leaks.Add(1)
					}
					if redirectRealm {
						http.Redirect(w, r, server.URL+"/capture", 302)
						return
					}
					_, _ = w.Write([]byte(`{"access_token":"anonymous-fixture"}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer anonymous-fixture" {
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope="repository:example/ext:pull"`, server.URL+"/token", strings.TrimPrefix(server.URL, "http://")))
					w.WriteHeader(401)
					return
				}
				if strings.Contains(r.URL.Path, "/manifests/") {
					_, _ = w.Write(manifest)
				} else {
					_, _ = w.Write(blob)
				}
			}))
			defer server.Close()
			m := &Market{Token: "fixture-secret", Spec: MarketSpec{Kind: "github", Owner: "example", Repo: "market"}}
			pkg := &MarketPackage{URL: "oci:" + strings.TrimPrefix(server.URL, "http://") + "/example/ext@sha256:" + ociSum(manifest), SHA256: ociSum(blob), Size: int64(len(blob))}
			_, _, err := m.Download(context.Background(), pkg, t.TempDir())
			if (err == nil) == redirectRealm {
				t.Fatalf("realm outcome: %v", err)
			}
			if capture.Load() != 0 || leaks.Load() != 0 || tokens.Load() != 1 {
				t.Fatal("anonymous or redirect binding failed")
			}
		})
	}
}
