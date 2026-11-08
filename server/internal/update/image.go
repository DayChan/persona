package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Image is a container image reference (docs/operations/hub-update.md "Docker"): the registry, the repository in it, and the
// tag the compose file runs.
type Image struct {
	Registry string // ghcr.io, registry-1.docker.io, localhost:5000
	Repo     string // owner/persona (library/<name> for Docker Hub's official images)
	Tag      string // latest when none is given
}

// ParseImage reads a reference the way docker does: the first part is a registry only when it looks like a host
// (has a dot or a port, or is localhost); otherwise the image is on Docker Hub. A digest (@sha256:…) is dropped.
func ParseImage(s string) (Image, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[:i]
	}
	if s == "" || strings.ContainsAny(s, " \t") {
		return Image{}, fmt.Errorf("image %q is not a reference", s)
	}
	im := Image{Registry: "docker.io", Tag: "latest"}
	if first, rest, ok := strings.Cut(s, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		im.Registry, s = first, rest
	}
	if i := strings.LastIndex(s, ":"); i > 0 {
		s, im.Tag = s[:i], s[i+1:]
	}
	if s == "" || im.Tag == "" {
		return Image{}, fmt.Errorf("image %q is not a reference", s)
	}
	if im.Registry == "docker.io" {
		im.Registry = "registry-1.docker.io"
		if !strings.Contains(s, "/") {
			s = "library/" + s
		}
	}
	im.Repo = strings.ToLower(s)
	return im, nil
}

// Name is the image without its tag, as written in a compose file (Docker Hub's without the registry).
func (im Image) Name() string {
	if im.Registry == "registry-1.docker.io" {
		return strings.TrimPrefix(im.Repo, "library/")
	}
	return im.Registry + "/" + im.Repo
}

// Pinned says whether the tag names one version, so `docker compose pull` alone never brings a newer one.
func (im Image) Pinned() bool { _, ok := parseVersion(im.Tag); return ok }

func (im Image) base() string {
	host := (&url.URL{Host: im.Registry}).Hostname()
	if host == "localhost" || host == "127.0.0.1" || net.ParseIP(host).Equal(net.IPv6loopback) {
		return "http://" + im.Registry // docker talks plain HTTP to a registry on this machine too
	}
	return "https://" + im.Registry
}

// ErrPrivate: the registry wants credentials for the image (a private package).
var ErrPrivate = errors.New("镜像是私有的，hub 查不了")

// LatestVersion asks the registry for the image's tags and returns the newest release among them (X.Y.Z, no
// pre-releases, edge or sha-…); "" when it has none. token is a read token for a private image ("" for none).
func LatestVersion(ctx context.Context, client *http.Client, im Image, token string) (string, error) {
	tags, err := listTags(ctx, client, im, token)
	if err != nil {
		return "", err
	}
	best, bestV := "", version{}
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || v.pre != "" {
			continue
		}
		if best == "" || bestV.less(v) {
			best, bestV = t, v
		}
	}
	return strings.TrimPrefix(best, "v"), nil
}

// listTags reads GET /v2/<repo>/tags/list, answering the registry's bearer challenge (an anonymous pull token for a
// public image) and following its pages.
func listTags(ctx context.Context, client *http.Client, im Image, token string) ([]string, error) {
	next := im.base() + "/v2/" + im.Repo + "/tags/list?n=1000"
	bearer := ""
	var tags []string
	for page := 0; next != "" && page < 20; page++ {
		res, err := registryGet(ctx, client, next, bearer)
		if err != nil {
			return nil, err
		}
		if res.StatusCode == http.StatusUnauthorized && bearer == "" {
			challenge := res.Header.Get("Www-Authenticate")
			res.Body.Close()
			if bearer, err = pullToken(ctx, client, challenge, token); err != nil {
				return nil, err
			}
			page--
			continue
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body)
		res.Body.Close()
		switch {
		case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
			return nil, ErrPrivate
		case res.StatusCode == http.StatusNotFound:
			return nil, fmt.Errorf("registry 上没有 %s", im.Name())
		case res.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("registry 回了 %d", res.StatusCode)
		case err != nil:
			return nil, fmt.Errorf("registry 的标签列表读不懂：%w", err)
		}
		tags = append(tags, body.Tags...)
		next = nextPage(res.Header.Get("Link"), next)
	}
	return tags, nil
}

func registryGet(ctx context.Context, client *http.Client, u, bearer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return client.Do(req)
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// pullToken answers `Bearer realm="…",service="…",scope="…"`: anonymous, or with token as the password (any user
// name works for ghcr.io and Docker Hub).
func pullToken(ctx context.Context, client *http.Client, challenge, token string) (string, error) {
	kind, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(kind, "Bearer") {
		if token != "" {
			return token, nil // a registry with basic auth only: try the token as is
		}
		return "", ErrPrivate
	}
	p := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(params, -1) {
		p[m[1]] = m[2]
	}
	realm, err := url.Parse(p["realm"])
	if err != nil || realm.Scheme == "" {
		return "", fmt.Errorf("registry 的认证地址不对：%q", p["realm"])
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if p[k] != "" {
			q.Set(k, p[k])
		}
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.SetBasicAuth("persona", token)
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", ErrPrivate
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil || res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry 没给 token（%d）", res.StatusCode)
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	if body.Token == "" {
		return "", ErrPrivate
	}
	return body.Token, nil
}

// nextPage reads `Link: </v2/…/tags/list?last=x&n=1000>; rel="next"`, relative to the page it came with.
func nextPage(link, current string) string {
	if !strings.Contains(link, `rel="next"`) {
		return ""
	}
	start, end := strings.Index(link, "<"), strings.Index(link, ">")
	if start < 0 || end <= start {
		return ""
	}
	base, err := url.Parse(current)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(link[start+1 : end])
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// version is X.Y.Z with an optional pre-release (1.3.0-rc1).
type version struct {
	n   [3]int
	pre string
}

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-((?:alpha|beta|rc)[0-9.]*))?$`)

// parseVersion reads a release version or tag. A build from a branch (git describe: 0.3.0-12-gabc1234-…) is not
// one: it is newer than the tag it starts with, so it cannot be compared.
func parseVersion(s string) (version, bool) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	var v version
	for i := range 3 {
		v.n[i], _ = strconv.Atoi(m[i+1])
	}
	v.pre = m[4]
	return v, true
}

func (a version) less(b version) bool {
	for i := range 3 {
		if a.n[i] != b.n[i] {
			return a.n[i] < b.n[i]
		}
	}
	if a.pre == b.pre || a.pre == "" { // a release comes after its pre-releases
		return false
	}
	return b.pre == "" || a.pre < b.pre
}

// NewerVersion says whether release latest is newer than the running version; false when either is not a release
// version (a branch build cannot tell).
func NewerVersion(latest, running string) bool {
	l, ok1 := parseVersion(latest)
	r, ok2 := parseVersion(running)
	return ok1 && ok2 && r.less(l)
}
