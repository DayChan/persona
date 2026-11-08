package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ociSum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func ociTestManifest(t *testing.T, blob []byte) []byte {
	t.Helper()
	b, err := json.Marshal(ociManifest{SchemaVersion: 2, MediaType: ociManifestType, ArtifactType: OCIArtifactType, Config: json.RawMessage(`{}`), Layers: []ociDescriptor{{MediaType: OCILayerType, Digest: "sha256:" + ociSum(blob), Size: int64(len(blob)), Annotations: map[string]string{"org.opencontainers.image.title": "example.tar.gz"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseOCIReferences(t *testing.T) {
	valid := "oci:ghcr.io/example/market/extension@sha256:" + strings.Repeat("a", 64)
	if _, err := parseOCI(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"oci:ghcr.io/example/market:latest", strings.Replace(valid, "@", "?x=@", 1), strings.Replace(valid, "example/market", "../market", 1),
		strings.Replace(valid, "ghcr.io", "me@example.com", 1), strings.Replace(valid, "ghcr.io", "ghcr.io#fragment", 1),
		strings.Replace(valid, "sha256:", "sha512:", 1), "oci://ghcr.io/example@sha256:" + strings.Repeat("a", 64), valid + "?x=y",
	} {
		if _, err := parseOCI(raw); err == nil {
			t.Errorf("invalid reference accepted: %s", raw)
		}
	}
}

func TestOCIMirrorVerificationAndFreshFallback(t *testing.T) {
	blob := []byte("extension archive fixture")
	manifest := ociTestManifest(t, blob)
	for _, failure := range []string{"success", "missing", "server-error", "bad-manifest", "bad-blob", "partial", "timeout", "bad-title", "multi-layer"} {
		t.Run(failure, func(t *testing.T) {
			servedManifest := manifest
			var upstream, mirrorCalls, leaks atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstream.Add(1)
				if r.Header.Get("Authorization") != "" {
					leaks.Add(1)
				}
				if strings.Contains(r.URL.Path, "/manifests/") {
					_, _ = w.Write(servedManifest)
				} else {
					_, _ = w.Write(blob)
				}
			}))
			defer origin.Close()
			mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mirrorCalls.Add(1)
				if r.Header.Get("Authorization") != "" {
					leaks.Add(1)
				}
				if failure == "server-error" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if failure == "missing" {
					http.NotFound(w, r)
					return
				}
				if failure == "timeout" {
					<-r.Context().Done()
					return
				}
				if strings.Contains(r.URL.Path, "/manifests/") {
					if failure == "bad-manifest" {
						_, _ = w.Write([]byte(`{}`))
						return
					}
					_, _ = w.Write(servedManifest)
					return
				}
				if failure == "bad-blob" {
					_, _ = w.Write([]byte(strings.Repeat("x", len(blob))))
					return
				}
				if failure == "partial" {
					_, _ = w.Write(blob[:4])
					return
				}
				_, _ = w.Write(blob)
			}))
			defer mirror.Close()
			if failure == "bad-title" || failure == "multi-layer" {
				// These descriptors fail independently, even with the manifest digest pinned.
				var ix ociManifest
				_ = json.Unmarshal(manifest, &ix)
				if failure == "bad-title" {
					ix.Layers[0].Annotations["org.opencontainers.image.title"] = "../bad.tar.gz"
				} else {
					ix.Layers = append(ix.Layers, ix.Layers[0])
				}
				bad, _ := json.Marshal(ix)
				servedManifest = bad
				ref, _ := parseOCI("oci:" + strings.TrimPrefix(origin.URL, "http://") + "/example/market/ext@sha256:" + ociSum(bad))
				m := &Market{RegistryMirror: mirror.URL}
				_, err := m.downloadOCI(context.Background(), ref, false, io.Discard)
				if err == nil {
					t.Fatal("descriptor accepted")
				}
				return
			}
			m := &Market{Spec: MarketSpec{Kind: "github", Owner: "example", Repo: "market"}, Token: "fixture-secret", RegistryMirror: mirror.URL + "/prefix", Cache: t.TempDir()}
			pkg := &MarketPackage{URL: "oci:" + strings.TrimPrefix(origin.URL, "http://") + "/example/market/ext@sha256:" + ociSum(manifest), SHA256: ociSum(blob), Size: int64(len(blob))}
			file, name, err := m.Download(context.Background(), pkg, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(file)
			if string(got) != string(blob) || name != "example.tar.gz" {
				t.Fatal("fallback staging/hash was contaminated")
			}
			if leaks.Load() != 0 {
				t.Fatal("market token reached anonymous registry or mirror")
			}
			if failure == "success" && upstream.Load() != 0 {
				t.Fatal("successful mirror still fetched upstream")
			}
			if failure != "success" && upstream.Load() == 0 {
				t.Fatal("mirror failure did not fallback")
			}
			if mirrorCalls.Load() == 0 {
				t.Fatal("mirror not attempted")
			}
			before := upstream.Load() + mirrorCalls.Load()
			if _, _, err := m.Download(context.Background(), pkg, t.TempDir()); err != nil || before != upstream.Load()+mirrorCalls.Load() {
				t.Fatal("verified package cache not reused")
			}
		})
	}
}

type mappedRegistryTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (m mappedRegistryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	copy.URL = &u
	copy.Host = req.URL.Host
	copy.URL.Scheme, copy.URL.Host = m.target.Scheme, m.target.Host
	res, err := m.transport.RoundTrip(copy)
	if res != nil {
		res.Request = req
	}
	return res, err
}

func TestOCIAuthenticationNamespaceAndRealmBinding(t *testing.T) {
	blob := []byte("package")
	manifest := ociTestManifest(t, blob)
	for _, c := range []struct {
		name, kind, host, owner, repo, namespace, realm, service, scope string
		allowed                                                         bool
	}{
		{"github-matching", "github", "ghcr.io", "example", "market", "example/market/ext", "/token", "", "", true},
		{"github-other-owner", "github", "ghcr.io", "example", "market", "other/market/ext", "/token", "", "", false},
		{"github-other-market", "github", "ghcr.io", "example", "market", "example/another/ext", "/token", "", "", false},
		{"github-prefix-trick", "github", "ghcr.io", "example", "market", "example/market-other/ext", "/token", "", "", false},
		{"github-arbitrary-host", "github", "registry.example.com", "example", "market", "example/market/ext", "/token", "", "", false},
		{"github-wrong-realm", "github", "ghcr.io", "example", "market", "example/market/ext", "/other", "", "", false},
		{"https-matching", "https", "registry.example.com", "", "", "example/ext", "/token", "", "", true},
		{"git-no-registry-credential", "git", "registry.example.com", "", "", "example/ext", "/token", "", "", false},
		{"external-realm", "https", "registry.example.com", "", "", "example/ext", "https://other.example.com/token", "", "", false},
		{"wrong-service", "https", "registry.example.com", "", "", "example/ext", "/token", "other.example.com", "", false},
		{"wrong-scope", "https", "registry.example.com", "", "", "example/ext", "/token", "", "repository:other/ext:pull,push", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var credentialRequests, tokenRequests, leaks atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != c.host && r.Header.Get("Authorization") != "" {
					leaks.Add(1)
				}
				if r.URL.Path == "/token" || r.URL.Path == "/other" {
					tokenRequests.Add(1)
					if r.URL.Query().Get("scope") != "repository:"+c.namespace+":pull" || r.URL.Query().Get("service") != c.host {
						t.Error("token scope/service not bound")
					}
					auth := r.Header.Get("Authorization")
					user, password, basic := r.BasicAuth()
					if auth == "Bearer fixture-secret" || basic && user == c.owner && password == "fixture-secret" {
						credentialRequests.Add(1)
					} else {
						w.WriteHeader(401)
						return
					}
					_, _ = w.Write([]byte(`{"token":"short-lived-fixture"}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer short-lived-fixture" {
					realm := c.realm
					if strings.HasPrefix(realm, "/") {
						realm = "https://" + c.host + realm
					}
					service := c.service
					if service == "" {
						service = c.host
					}
					scope := c.scope
					if scope == "" {
						scope = "repository:" + c.namespace + ":pull"
					}
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope=%q`, realm, service, scope))
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
			target, _ := url.Parse(server.URL)
			client := &http.Client{Transport: mappedRegistryTransport{target, server.Client().Transport}}
			m := &Market{Spec: MarketSpec{Kind: c.kind, URL: "https://registry.example.com/catalog/persona-extensions.json", Owner: c.owner, Repo: c.repo}, Token: "fixture-secret", Client: client}
			pkg := &MarketPackage{URL: "oci:" + c.host + "/" + c.namespace + "@sha256:" + ociSum(manifest), SHA256: ociSum(blob), Size: int64(len(blob))}
			_, _, err := m.Download(context.Background(), pkg, t.TempDir())
			if (err == nil) != c.allowed {
				t.Fatalf("auth outcome: %v", err)
			}
			if !c.allowed && credentialRequests.Load() != 0 {
				t.Fatal("credential escaped allowed binding")
			}
			if leaks.Load() != 0 {
				t.Fatal("credential leaked to external host")
			}
			if c.allowed && tokenRequests.Load() == 0 {
				t.Fatal("challenge not negotiated")
			}
		})
	}
}

func TestOCIAnonymousChallengeAndRedirects(t *testing.T) {
	blob := []byte("package")
	manifest := ociTestManifest(t, blob)
	var tokenRequests, leaks atomic.Int32
	var origin *httptest.Server
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaks.Add(1)
		}
		_, _ = w.Write(blob)
	}))
	defer cdn.Close()
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenRequests.Add(1)
			if r.Header.Get("Authorization") != "" {
				leaks.Add(1)
			}
			_, _ = w.Write([]byte(`{"token":"anonymous-fixture"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer anonymous-fixture" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service=%q,scope="repository:example/ext:pull"`, origin.URL+"/token", strings.TrimPrefix(origin.URL, "http://")))
			w.WriteHeader(401)
			return
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			_, _ = w.Write(manifest)
		} else {
			http.Redirect(w, r, cdn.URL+"/archive", 302)
		}
	}))
	defer origin.Close()
	m := &Market{}
	pkg := &MarketPackage{URL: "oci:" + strings.TrimPrefix(origin.URL, "http://") + "/example/ext@sha256:" + ociSum(manifest), SHA256: ociSum(blob), Size: int64(len(blob))}
	// Cross-origin HTTP CDN redirects are refused, rather than risking plaintext.
	if _, _, err := m.Download(context.Background(), pkg, t.TempDir()); err == nil {
		t.Fatal("plaintext CDN redirect accepted")
	}
	if leaks.Load() != 0 || tokenRequests.Load() != 1 {
		t.Fatal("anonymous negotiation leaked credentials")
	}
}

func TestOCICatalogPinsSizeAndCancellation(t *testing.T) {
	blob := []byte("package")
	manifest := ociTestManifest(t, blob)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			_, _ = w.Write(manifest)
		} else {
			_, _ = w.Write(blob)
		}
	}))
	defer server.Close()
	ref := "oci:" + strings.TrimPrefix(server.URL, "http://") + "/example/ext@sha256:" + ociSum(manifest)
	for _, pkg := range []*MarketPackage{{URL: ref, SHA256: strings.Repeat("0", 64), Size: int64(len(blob))}, {URL: ref, SHA256: ociSum(blob), Size: int64(len(blob)) + 1}, {URL: ref, SHA256: ociSum(blob), Size: PackageMost + 1}} {
		dir := t.TempDir()
		if _, _, err := (&Market{}).Download(context.Background(), pkg, dir); err == nil {
			t.Fatal("catalog pin/size not checked")
		}
		if _, err := os.Stat(filepath.Join(dir, "download")); err == nil {
			t.Fatal("bad partial package remains staged")
		}
	}
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer blocked.Close()
	var failedMirrorCalls atomic.Int32
	failedMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { failedMirrorCalls.Add(1); w.WriteHeader(500) }))
	defer failedMirror.Close()
	bothFailDir := t.TempDir()
	// The mirror fails transport; upstream fails the independent catalog checksum.
	if _, _, err := (&Market{RegistryMirror: failedMirror.URL}).Download(context.Background(), &MarketPackage{URL: ref, SHA256: strings.Repeat("0", 64), Size: int64(len(blob))}, bothFailDir); err == nil {
		t.Fatal("both failed sources were accepted")
	}
	if failedMirrorCalls.Load() == 0 {
		t.Fatal("both-failure test skipped the mirror")
	}
	if _, err := os.Stat(filepath.Join(bothFailDir, "download")); err == nil {
		t.Fatal("both-failure attempt left an unverified staged archive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	m := &Market{RegistryMirror: blocked.URL}
	if _, _, err := m.Download(ctx, &MarketPackage{URL: ref, SHA256: ociSum(blob), Size: int64(len(blob))}, t.TempDir()); err == nil {
		t.Fatal("cancellation ignored")
	}
}
