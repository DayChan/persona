package extension

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	OCIArtifactType = "application/vnd.persona.extension.v1"
	OCILayerType    = "application/vnd.persona.extension.package.v1+gzip"
	ociManifestType = "application/vnd.oci.image.manifest.v1+json"
)

var (
	ociDigest     = lazyre.New(`^sha256:[a-f0-9]{64}$`)
	ociRepository = lazyre.New(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
	ociTitle      = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._-]{0,180}\.tar\.gz$`)
)

type ociReference struct{ registry, repository, digest string }

func parseOCI(raw string) (ociReference, error) {
	var out ociReference
	ref, ok := strings.CutPrefix(raw, "oci:")
	if !ok {
		return out, i18n.Err("extension-market_security.package")
	}
	location, digest, ok := strings.Cut(ref, "@")
	registry, repo, slash := strings.Cut(location, "/")
	u, err := url.Parse("https://" + registry)
	if !ok || !slash || !ociDigest.MatchString(digest) || !ociRepository.MatchString(repo) || err != nil || u.Host != registry || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(registry, "\\% \t\r\n") || strings.Contains(registry, ":") && u.Port() == "" && !strings.HasPrefix(registry, "[") {
		return out, i18n.Err("extension-market_security.package")
	}
	return ociReference{registry: registry, repository: repo, digest: digest}, nil
}

func registryBase(host string) string {
	u, _ := url.Parse("https://" + host)
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() == "localhost" || ip != nil && ip.IsLoopback() {
		return "http://" + host
	}
	return "https://" + host
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	ArtifactType  string          `json:"artifactType"`
	Config        json.RawMessage `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

// registrySession binds both the market credential and a registry bearer to one
// origin and one pull namespace. Mirrors create separate anonymous sessions.
type registrySession struct {
	market *Market
	ref    ociReference
	base   string
	mirror bool
	bearer string
	// anonymous is set once the registry refused the market credential: the rest
	// of this package is pulled without it (get).
	anonymous bool
}

func (s *registrySession) endpoint(kind, digest string) string {
	return strings.TrimSuffix(s.base, "/") + "/v2/" + s.ref.repository + "/" + kind + "/" + digest
}

func (s *registrySession) credentialAllowed() bool {
	m := s.market
	if s.mirror || s.anonymous || m.Token == "" {
		return false
	}
	switch m.Spec.Kind {
	case "https":
		return sameOrigin(s.base, m.Spec.URL)
	case "github":
		// This binding is derived from the registered market, never a catalog hint.
		return s.base == "https://ghcr.io" && strings.HasPrefix(s.ref.repository, strings.ToLower(m.Spec.Owner+"/"+m.Spec.Repo)+"/")
	}
	return false // A git clone token is never a registry credential.
}

func (s *registrySession) resourceAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !sameOrigin(raw, s.base) || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	if u.EscapedPath() != u.Path || path.Clean(u.Path) != u.Path {
		return false
	}
	base, _ := url.Parse(s.base)
	prefix := strings.TrimSuffix(base.Path, "/") + "/v2/" + s.ref.repository + "/"
	for _, kind := range []string{"manifests/", "blobs/"} {
		if digest, ok := strings.CutPrefix(u.Path, prefix+kind); ok && ociDigest.MatchString(digest) {
			return true
		}
	}
	return false
}

// request follows data redirects without credentials. An authentication endpoint
// may not redirect; registry headers never reach a CDN, realm or other namespace.
func (s *registrySession) request(ctx context.Context, raw, auth string, realm bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, i18n.Err("extension-market_security.request")
	}
	req.Header.Set("Accept", ociManifestType)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	c := *s.market.client()
	previous := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if realm || len(via) >= 5 {
			return i18n.Err("extension-market_security.redirect")
		}
		if req.URL.User != nil || req.URL.Fragment != "" || req.URL.Scheme != "https" && !sameOrigin(req.URL.String(), s.base) {
			return i18n.Err("extension-market_security.redirect")
		}
		if !s.resourceAllowed(req.URL.String()) || via[len(via)-1].Header.Get("Authorization") == "" {
			req.Header.Del("Authorization")
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
	res, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, i18n.Err("extension-market_security.request")
	}
	return res, nil
}

var bearerParameter = lazyre.New(`([a-z]+)="([^"\r\n]*)"`)

func parseBearer(header string) (map[string]string, error) {
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return nil, i18n.Err("extension-market_security.challenge")
	}
	params := strings.TrimSpace(header[len("Bearer "):])
	out := map[string]string{}
	matches := bearerParameter.FindAllStringSubmatchIndex(params, -1)
	pos := 0
	for _, match := range matches {
		if strings.Trim(strings.TrimSpace(params[pos:match[0]]), ",") != "" {
			return nil, i18n.Err("extension-market_security.challenge")
		}
		key, val := params[match[2]:match[3]], params[match[4]:match[5]]
		if _, exists := out[key]; exists {
			return nil, i18n.Err("extension-market_security.challenge")
		}
		if key != "realm" && key != "service" && key != "scope" {
			return nil, i18n.Err("extension-market_security.challenge")
		}
		out[key] = val
		pos = match[1]
	}
	if strings.TrimSpace(params[pos:]) != "" || out["realm"] == "" {
		return nil, i18n.Err("extension-market_security.challenge")
	}
	return out, nil
}

// authenticate answers a bearer challenge. The status is the token endpoint's when
// it refused (not 200), so get can tell a refused credential from other failures.
func (s *registrySession) authenticate(ctx context.Context, header string) (int, error) {
	p, err := parseBearer(header)
	if err != nil {
		return 0, err
	}
	realm, err := url.Parse(p["realm"])
	base, _ := url.Parse(s.base)
	if err != nil || realm.User != nil || realm.RawQuery != "" || realm.Fragment != "" || !sameOrigin(realm.String(), s.base) || realm.Scheme != base.Scheme {
		return 0, i18n.Err("extension-market_security.challenge")
	}
	if p["service"] != "" && p["service"] != base.Host {
		return 0, i18n.Err("extension-market_security.challenge")
	}
	scope := "repository:" + s.ref.repository + ":pull"
	if p["scope"] != "" && p["scope"] != scope {
		return 0, i18n.Err("extension-market_security.challenge")
	}
	if base.Host == "ghcr.io" && realm.Path != "/token" {
		return 0, i18n.Err("extension-market_security.challenge")
	}
	q := url.Values{"scope": {scope}, "service": {base.Host}}
	realm.RawQuery = q.Encode()
	auth := ""
	if s.credentialAllowed() {
		auth = "Bearer " + s.market.Token
		if s.market.Spec.Kind == "github" {
			auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(s.market.Spec.Owner+":"+s.market.Token))
		}
	}
	res, err := s.request(ctx, realm.String(), auth, true)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, i18n.Err("extension-market_security.http_status", i18n.Args{"status": res.StatusCode})
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10+1))
	if err != nil || len(b) > 64<<10 || strictJSON(b) != nil {
		return 0, i18n.Err("extension-market_security.auth")
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(b, &token) != nil {
		return 0, i18n.Err("extension-market_security.auth")
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" || len(token.Token) > 16384 || strings.ContainsAny(token.Token, "\r\n") {
		return 0, i18n.Err("extension-market_security.auth")
	}
	s.bearer = token.Token
	return 0, nil
}

// get fetches one manifest or blob. When the registry refuses the market credential
// (401 / 403, at the token endpoint or on the request), it tries once more without it
// and keeps the session anonymous: a public package needs no credential, and a market
// token may be able to read the catalog repository but not its packages (a GitHub
// token without read:packages makes ghcr.io answer 403). A private package still fails,
// now saying the token may lack that permission.
func (s *registrySession) get(ctx context.Context, kind, digest string) (*http.Response, error) {
	credential := s.credentialAllowed()
	res, status, err := s.fetch(ctx, kind, digest)
	if err == nil || !credential || status != http.StatusUnauthorized && status != http.StatusForbidden {
		return res, err
	}
	s.anonymous, s.bearer = true, ""
	res, again, err := s.fetch(ctx, kind, digest)
	if err != nil && (again == http.StatusUnauthorized || again == http.StatusForbidden) {
		return nil, i18n.Err("extension-market_security.credential_refused", i18n.Args{"status": status})
	}
	return res, err
}

// fetch is one attempt at a manifest or blob, answering a 401 challenge once. The
// status is the registry's when it refused the request or the token.
func (s *registrySession) fetch(ctx context.Context, kind, digest string) (*http.Response, int, error) {
	for attempt := 0; attempt < 2; attempt++ {
		auth := ""
		if s.bearer != "" {
			auth = "Bearer " + s.bearer
		} else if s.credentialAllowed() {
			auth = "Bearer " + s.market.Token
		}
		res, err := s.request(ctx, s.endpoint(kind, digest), auth, false)
		if err != nil {
			return nil, 0, err
		}
		if res.StatusCode == http.StatusOK {
			return res, 0, nil
		}
		challenge := res.Header.Get("WWW-Authenticate")
		status := res.StatusCode
		res.Body.Close()
		if status != http.StatusUnauthorized || attempt != 0 {
			return nil, status, i18n.Err("extension-market_security.http_status", i18n.Args{"status": status})
		}
		if status, err := s.authenticate(ctx, challenge); err != nil {
			return nil, status, err
		}
	}
	return nil, 0, i18n.Err("extension-market_security.auth")
}

func (m *Market) downloadOCI(ctx context.Context, ref ociReference, mirror bool, w io.Writer) (string, error) {
	base := registryBase(ref.registry)
	if mirror {
		base = m.RegistryMirror
	}
	s := &registrySession{market: m, ref: ref, base: base, mirror: mirror}
	res, err := s.get(ctx, "manifests", ref.digest)
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, IndexMost+1))
	res.Body.Close()
	if err != nil || len(raw) > IndexMost {
		return "", i18n.Err("extension-market_security.manifest")
	}
	hash := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(hash[:]) != ref.digest {
		return "", i18n.Err("extension-market_security.manifest")
	}
	var manifest ociManifest
	if strictJSON(raw) != nil || json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 || manifest.MediaType != ociManifestType || manifest.ArtifactType != OCIArtifactType || len(manifest.Layers) != 1 || len(manifest.Config) == 0 || string(manifest.Config) == "null" {
		return "", i18n.Err("extension-market_security.manifest")
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(manifest.Config, &config) != nil {
		return "", i18n.Err("extension-market_security.manifest")
	}
	layer := manifest.Layers[0]
	name := layer.Annotations["org.opencontainers.image.title"]
	if layer.MediaType != OCILayerType || !ociDigest.MatchString(layer.Digest) || layer.Size <= 0 || layer.Size > PackageMost || !ociTitle.MatchString(name) {
		return "", i18n.Err("extension-market_security.descriptor")
	}
	res, err = s.get(ctx, "blobs", layer.Digest)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(res.Body, layer.Size+1))
	if err != nil {
		return "", err
	}
	if n != layer.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != layer.Digest {
		return "", i18n.Err("extension-market_security.blob")
	}
	return name, nil
}
