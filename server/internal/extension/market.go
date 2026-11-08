package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/i18n"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Extension markets (docs/extensions/extensions.md "Extension market") publish persona-extensions.json
// at the root of a repository or website path. A registered market lists its packages
// and pins each download's sha256.

// IndexFile is the canonical market index at a repository or website root.
const IndexFile = "persona-extensions.json"

// Limits of a market.
const (
	IndexMost           = 4 << 20
	IndexExtensionsMost = 500
	MarketsMost         = 32
	marketFetchFor      = 30 * time.Second
	MarketTokenMost     = 4096
	marketTagMost       = 16
	marketPackageMost   = 16
)

// MarketSpec is where a market's index is.
type MarketSpec struct {
	Kind string // https | github | git
	// URL: the index's https URL (Kind https), or the repository's (Kind git).
	URL string
	// Owner, Repo, Ref: a GitHub repository (Kind github); Ref "" is the default branch (also of Kind git).
	Owner, Repo, Ref string
}

var githubName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ParseMarket reads github:owner/repo[@ref], or an https URL: of the index itself (ending in .json) or of the
// directory it is in (persona-extensions.json is added).
func ParseMarket(s string) (MarketSpec, error) {
	s = strings.TrimSpace(s)
	if spec, ok, err := parseGit(s); ok {
		return spec, err
	}
	if rest, ok := strings.CutPrefix(s, "github:"); ok {
		repo, ref, _ := strings.Cut(rest, "@")
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || !githubName.MatchString(owner) || !githubName.MatchString(name) || strings.Contains(name, "/") {
			return MarketSpec{}, errors.New("GitHub 市场写成 github:owner/repo 或 github:owner/repo@分支")
		}
		if ref != "" && (strings.ContainsAny(ref, " \t\n?#") || strings.Contains(ref, "..") || len(ref) > 200) {
			return MarketSpec{}, errors.New("分支名不对")
		}
		return MarketSpec{Kind: "github", Owner: owner, Repo: strings.TrimSuffix(name, ".git"), Ref: ref}, nil
	}
	if strings.HasPrefix(s, "https://github.com/") {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(s, "https://github.com/"), "/"), "/")
		if len(parts) == 2 {
			return ParseMarket("github:" + parts[0] + "/" + parts[1])
		}
		if len(parts) >= 4 && parts[2] == "tree" {
			return ParseMarket("github:" + parts[0] + "/" + parts[1] + "@" + strings.Join(parts[3:], "/"))
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return MarketSpec{}, errors.New("市场的地址是 https://… 或 github:owner/repo")
	}
	if u.Scheme != "https" {
		return MarketSpec{}, errors.New("市场只能用 https")
	}
	if u.User != nil {
		return MarketSpec{}, errors.New("地址里不能带账号密码：私有市场填 token")
	}
	u.Fragment = ""
	if !strings.HasSuffix(u.Path, ".json") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + IndexFile
	}
	return MarketSpec{Kind: "https", URL: u.String()}, nil
}

// String is the spec as kept and shown.
func (s MarketSpec) String() string {
	if s.Kind == "git" {
		out := "git:" + s.URL
		if s.Ref != "" {
			out += "#" + s.Ref
		}
		return out
	}
	if s.Kind == "github" {
		out := "github:" + s.Owner + "/" + s.Repo
		if s.Ref != "" {
			out += "@" + s.Ref
		}
		return out
	}
	return s.URL
}

// MarketID is a short stable id of a spec (the market's key in the hub's API).
func MarketID(s MarketSpec) string {
	sum := sha256.Sum256([]byte(s.String()))
	return hex.EncodeToString(sum[:5])
}

// MarketIndex is the canonical extension index.
type MarketIndex struct {
	MarketVersion int    `json:"market_version"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Homepage      string `json:"homepage,omitempty"`
	// SigningKey is legacy catalog metadata, never a trust grant.
	SigningKey string `json:"signing_key,omitempty"`
	// Extensions are "extensions" in an index; the old key "plugins" is read too, and still the one written out (the
	// hub keeps the index it read, which a hub rolled back to a build before #1844 reads again).
	Extensions []MarketEntry `json:"plugins"`
}

func (ix *MarketIndex) UnmarshalJSON(data []byte) error {
	type plain MarketIndex
	var v struct {
		plain
		Current []MarketEntry `json:"extensions"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*ix = MarketIndex(v.plain)
	if v.Current != nil {
		ix.Extensions = v.Current
	}
	return nil
}

// MarketEntry is one plugin of a market: its manifest (so the permissions are known before downloading) and its
// packages.
type MarketEntry struct {
	Manifest json.RawMessage `json:"manifest"`
	Tags     []string        `json:"tags,omitempty"`
	Packages []MarketPackage `json:"packages"`
	// Parsed is Manifest, read.
	Parsed *Manifest `json:"-"`
	// Only ParseCatalog can create this proof; public fields remain checked
	// against the snapshot before the registry accepts official provenance.
	verifiedKey   string
	verifiedEntry [sha256.Size]byte
}

// MarketPackage is a package of an entry.
type MarketPackage struct {
	// Platforms are the GOOS-GOARCH it runs on; none: any (a package for every platform, or one that runs no code).
	Platforms []string `json:"platforms,omitempty"`
	// URL: relative to the index, an https URL, or (a GitHub market) release:<tag>/<asset>.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// ParseIndex reads and checks an index. Entries that do not check out are dropped with a warning each (one broken
// plugin does not hide a market); an index that is not one is an error.
func ParseIndex(data []byte) (*MarketIndex, []string, error) {
	if len(data) > IndexMost {
		return nil, nil, fmt.Errorf("索引大于 %d 字节", IndexMost)
	}
	var ix MarketIndex
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, nil, fmt.Errorf("不是扩展市场的索引：%v", err)
	}
	if ix.MarketVersion != 1 {
		return nil, nil, fmt.Errorf("索引的 market_version 是 %d，这个 hub 只认 1", ix.MarketVersion)
	}
	ix.Name = strings.TrimSpace(ix.Name)
	if ix.Name == "" || len([]rune(ix.Name)) > 60 {
		return nil, nil, errors.New("索引要有 name（最多 60 个字）")
	}
	if len([]rune(ix.Description)) > StringMost {
		return nil, nil, errors.New("索引的 description 太长")
	}
	if ix.Homepage != "" && !isHTTPS(ix.Homepage) {
		ix.Homepage = ""
	}
	if len(ix.Extensions) > IndexExtensionsMost {
		return nil, nil, fmt.Errorf("索引里多于 %d 个扩展", IndexExtensionsMost)
	}
	var warnings []string
	seen := map[string]bool{}
	kept := ix.Extensions[:0]
	for i, e := range ix.Extensions {
		m, err := e.check()
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("第 %d 个扩展：%v", i+1, err))
		case seen[m.ID]:
			warnings = append(warnings, fmt.Sprintf("扩展 %s 出现了两次，只用第一个", m.ID))
		default:
			seen[m.ID] = true
			e.Parsed = m
			kept = append(kept, e)
		}
	}
	ix.Extensions = kept
	return &ix, warnings, nil
}

func (e *MarketEntry) check() (*Manifest, error) {
	if len(e.Manifest) == 0 {
		return nil, errors.New("没有 manifest")
	}
	m, err := Parse(e.Manifest)
	if err != nil {
		return nil, err
	}
	// An entry without tags of its own shows the manifest's (an index written from the source manifest as it is).
	if len(e.Tags) == 0 {
		e.Tags = m.Tags
	}
	if len(e.Tags) > marketTagMost {
		return nil, errors.New("tags 太多")
	}
	for _, t := range e.Tags {
		if t == "" || len([]rune(t)) > 24 {
			return nil, errors.New("tag 为空或太长")
		}
	}
	if len(e.Packages) == 0 || len(e.Packages) > marketPackageMost {
		return nil, fmt.Errorf("%s：要有 1–%d 个包", m.ID, marketPackageMost)
	}
	for _, p := range e.Packages {
		if b, err := hex.DecodeString(p.SHA256); err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("%s：包要有 sha256（64 位十六进制）", m.ID)
		}
		if strings.HasPrefix(p.URL, "oci:") {
			if _, err := parseOCI(p.URL); err != nil || p.Size <= 0 || p.Size > PackageMost {
				return nil, i18n.Err("extension-market_security.package")
			}
			continue
		}
		if p.Size < 0 || p.Size > PackageMost {
			return nil, i18n.Err("extension-market_security.package")
		}
		if p.URL == "" || strings.HasPrefix(p.URL, "http://") || strings.Contains(p.URL, "://") && !strings.HasPrefix(p.URL, "https://") {
			return nil, fmt.Errorf("%s：包的地址要是相对路径或 https", m.ID)
		}
	}
	return m, nil
}

func isHTTPS(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// Platform is this hub's GOOS-GOARCH.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// PackageFor is the entry's package for platform, or nil.
func (e *MarketEntry) PackageFor(platform string) *MarketPackage {
	for i, p := range e.Packages {
		if len(p.Platforms) == 0 || slices.Contains(p.Platforms, platform) {
			return &e.Packages[i]
		}
	}
	return nil
}

// Market is a registered market with what reading it needs.
type Market struct {
	Spec MarketSpec
	// TrustedKey is supplied by the hub to pin the market's trust at preview time; it is not a download credential.
	TrustedKey string
	// Token, when set, is sent to the market's own origin only (a private repository or site).
	Token string
	// Client, GitHubAPI, GitHubRaw and GitHubWeb: tests replace them. With GitHubAPI replaced and GitHubRaw not, a
	// rate-limited read has nowhere else to go (githubFallback).
	Client    *http.Client
	GitHubAPI string
	GitHubRaw string
	GitHubWeb string
	// Dir is where git markets' clones are kept (the hub's data directory).
	Dir string
	// Cache is where downloaded packages are kept by their sha256 ("": none), docs/protocol/protocol.md "Extension install, upgrade and uninstall progress".
	Cache string
	// Mirrors receive no market credentials, and always verify before fallback.
	RegistryMirror, CatalogMirror string
	// allowLocal lets tests clone a local repository.
	allowLocal bool
	// viaFallback: some file of this read came from githubFallback's hosts.
	viaFallback bool
}

func (m *Market) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return http.DefaultClient
}

func (m *Market) api() string {
	if m.GitHubAPI != "" {
		return strings.TrimSuffix(m.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

// get fetches u into w (at most limit bytes), with the token when u is the market's own origin.
func (m *Market) get(ctx context.Context, u, accept string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if m.Token != "" && sameOrigin(u, m.origin()) {
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}
	if m.Spec.Kind == "github" {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	sent := req.Header.Get("Authorization") != "" // the token goes to the market's own origin only
	client := *m.client()
	previous := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || next.URL.User != nil || next.URL.Scheme != "https" && !sameOrigin(next.URL.String(), u) {
			return i18n.Err("extension-market_security.redirect")
		}
		if !sameOrigin(next.URL.String(), m.origin()) || via[len(via)-1].Header.Get("Authorization") == "" {
			next.Header.Del("Authorization")
		}
		if previous != nil {
			return previous(next, via)
		}
		return nil
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return i18n.Err("extension-market_security.request")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound && m.Token == "":
		return notFound{errors.New("找不到（404）：地址不对，或者是私有的，要填 token")}
	case res.StatusCode == http.StatusNotFound:
		return notFound{errors.New("没有权限（HTTP 404）：token 不对或没有读这个仓库的权限")}
	case rateLimited(res):
		msg := fmt.Sprintf("被限流了（HTTP %d）：%s再试", res.StatusCode, retryIn(res.Header, time.Now()))
		if !sent && m.Spec.Kind == "github" {
			msg += "；填一个 GitHub token 额度更高（公开仓库也适用）"
		}
		return errOfKind(ErrRateLimited, "%s", msg)
	case (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) && !sent:
		return fmt.Errorf("要登录才能读（HTTP %d）：填一个能读它的 token", res.StatusCode)
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return fmt.Errorf("没有权限（HTTP %d）：token 不对或没有读这个仓库的权限", res.StatusCode)
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(res.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("大于 %d 字节", limit)
	}
	return err
}

// rateLimited says whether a market refused a request for its rate limit, not for rights: a 429, or GitHub's 403
// with no requests left (X-RateLimit-Remaining: 0), a Retry-After, or a body that says so (its secondary limit may
// come with neither header). It reads at most the first KB of a 403's body.
func rateLimited(res *http.Response) bool {
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return true
	case res.StatusCode != http.StatusForbidden:
		return false
	case res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != "":
		return true
	}
	head, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
	return strings.Contains(strings.ToLower(string(head)), "rate limit")
}

// retryIn says when a rate-limited request may be tried again, from Retry-After (seconds) or X-RateLimit-Reset
// (Unix seconds); "in a while" (in Chinese) when neither says.
func retryIn(h http.Header, now time.Time) string {
	var wait time.Duration
	if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("Retry-After")), 10, 64); err == nil && s >= 0 {
		wait = time.Duration(s) * time.Second
	} else if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && s > 0 {
		wait = time.Unix(s, 0).Sub(now)
	} else {
		return "过一会儿"
	}
	if wait <= time.Minute {
		return "1 分钟后"
	}
	return fmt.Sprintf("约 %d 分钟后", int((wait+time.Minute-1)/time.Minute))
}

// SharesTokenWith limits explicit address moves and token reuse to the original
// HTTPS origin, or repositories belonging to the same registered GitHub owner.
func (m *Market) SharesTokenWith(target MarketSpec) bool {
	if m.Spec.Kind != target.Kind {
		return false
	}
	if m.Spec.Kind == "github" {
		return strings.EqualFold(m.Spec.Owner, target.Owner)
	}
	return sameOrigin(m.Spec.URL, target.URL)
}

// origin is where the token may go.
func (m *Market) origin() string {
	if m.Spec.Kind == "github" {
		return m.api()
	}
	return m.Spec.URL
}

// notFound is a catalog or package that the origin could not find.
type notFound struct{ error }

func (e notFound) Unwrap() error { return e.error }

func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host)
}

// ErrRateLimited: the market refused a request for its rate limit, not for rights.
var ErrRateLimited = errors.New("rate limited")

// githubFallback: when the API rate-limits a GitHub market read without a token (60 requests an hour for each
// address, which every device behind one router shares), the same file is read where the API's limit does not
// apply: raw.githubusercontent.com for files of the repository, github.com/…/releases/download for release assets.
// Only public repositories are there; a private one says 404 and the API's answer stands. A market with a token
// always uses the API (5000 an hour, and the token goes nowhere else). The index stays signed and every package
// pinned by its sha256, so where they were read from changes nothing about what is trusted.
func (m *Market) githubFallback() (raw, web string, ok bool) {
	if m.Spec.Kind != "github" || m.Token != "" {
		return "", "", false
	}
	raw, web = m.GitHubRaw, m.GitHubWeb
	if m.GitHubAPI != "" && raw == "" { // a test's API: never the real raw host
		return "", "", false
	}
	if raw == "" {
		raw = "https://raw.githubusercontent.com"
	}
	if web == "" {
		web = "https://github.com"
	}
	return strings.TrimSuffix(raw, "/"), strings.TrimSuffix(web, "/"), true
}

// getRepoFile reads a file of a GitHub market's repository through the API, and from raw.githubusercontent.com when
// the API rate-limits a market without a token (githubFallback).
func (m *Market) getRepoFile(ctx context.Context, p string, w io.Writer, limit int64) error {
	err := m.get(ctx, m.contentsURL(p), "application/vnd.github.raw", w, limit)
	raw, _, ok := m.githubFallback()
	if !ok || !errors.Is(err, ErrRateLimited) {
		return err
	}
	ref := m.Spec.Ref
	if ref == "" {
		ref = "HEAD" // the default branch
	}
	u := fmt.Sprintf("%s/%s/%s/%s/%s", raw, url.PathEscape(m.Spec.Owner), url.PathEscape(m.Spec.Repo), escapePath(ref), escapePath(p))
	ferr := m.get(ctx, u, "", w, limit)
	switch {
	case ferr == nil:
		m.viaFallback = true
		return nil
	case errors.As(ferr, new(notFound)):
		// Not there: the canonical index or its signature is missing, or the repository is private.
		// A market without a token cannot read either way; return that failure unchanged.
		return ferr
	}
	return err
}

// contentsURL is a file of a GitHub market's repository, through the API (works for private ones with a token).
func (m *Market) contentsURL(p string) string {
	u := fmt.Sprintf("%s/repos/%s/%s/contents/%s", m.api(), url.PathEscape(m.Spec.Owner), url.PathEscape(m.Spec.Repo), escapePath(p))
	if m.Spec.Ref != "" {
		u += "?ref=" + url.QueryEscape(m.Spec.Ref)
	}
	return u
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Fetched keeps the full catalog envelope for restart-time verification.
type Fetched struct {
	Index        *MarketIndex
	Warnings     []string
	Raw, Payload []byte
}

func (m *Market) Fetch(ctx context.Context) (*MarketIndex, []string, error) {
	f, err := m.FetchSigned(ctx)
	if err != nil {
		return nil, nil, err
	}
	return f.Index, f.Warnings, nil
}

// FetchSigned reads one document, and enforces the compiled-in pin for all official aliases. A failure is
// ErrCatalogUnavailable (nothing read) or ErrCatalogUnverified (read, and it does not verify). A catalog mirror
// that cannot be read or does not verify is skipped for the origin, whose answer alone decides: a mirror can
// neither stand in an unverified catalog nor, when the origin is unreachable, quarantine the market.
func (m *Market) FetchSigned(ctx context.Context) (*Fetched, error) {
	limit := marketFetchFor
	if m.Spec.Kind == "git" {
		limit = gitFor
	}
	key := ""
	if IsOfficialMarket(m.Spec.String()) {
		key = Official.SigningKey
		if _, err := ParseSigningKey(key); err != nil {
			return nil, catalogFailure(ErrCatalogUnverified, i18n.Err("extension-market_security.signature"))
		}
	}
	if key != "" && m.CatalogMirror != "" {
		mirrorCtx, stop := context.WithTimeout(ctx, mirrorFetchFor)
		var b strings.Builder
		anonymous := *m
		anonymous.Token = ""
		err := anonymous.get(mirrorCtx, m.CatalogMirror, "application/json", &b, IndexMost)
		stop()
		if err == nil {
			if f, err := ParseCatalog([]byte(b.String()), key); err == nil {
				return f, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	raw, err := m.fetchRaw(ctx)
	if err != nil {
		return nil, catalogFailure(ErrCatalogUnavailable, err)
	}
	return ParseCatalog(raw, key)
}

// fetchRaw never reads an adjacent signature or another catalog filename.
func (m *Market) fetchRaw(ctx context.Context) ([]byte, error) {
	var b strings.Builder
	var err error
	switch m.Spec.Kind {
	case "git":
		if err = m.syncGit(ctx); err == nil {
			err = m.gitFile(IndexFile, &b, IndexMost)
		}
	case "github":
		err = m.getRepoFile(ctx, IndexFile, &b, IndexMost)
	default:
		err = m.get(ctx, m.Spec.URL, "application/json", &b, IndexMost)
	}
	return []byte(b.String()), err
}

// Download fetches pkg into dir (a fresh staging directory) and checks its sha256; it returns the file and the
// name it goes by (for its type). A package in the cache (Cache) is taken from there; a download that gets no data
// for StallAfter gives up (ErrDownloadStalled).
func (m *Market) Download(ctx context.Context, pkg *MarketPackage, dir string) (string, string, error) {
	totalBudget := 5 * time.Minute
	if m.RegistryMirror != "" {
		totalBudget += mirrorFetchFor
	}
	ctx, cancel := context.WithTimeout(ctx, totalBudget)
	defer cancel()
	if pkg == nil {
		return "", "", i18n.Err("extension-market_security.package")
	}
	if digest, err := hex.DecodeString(pkg.SHA256); err != nil || len(digest) != sha256.Size {
		return "", "", i18n.Err("extension-market_security.package")
	}
	if err := ValidateMarketMirrors(MarketMirrors{RegistryMirror: m.RegistryMirror, CatalogMirror: m.CatalogMirror}); err != nil {
		return "", "", err
	}
	if pkg.Size < 0 || pkg.Size > PackageMost {
		return "", "", i18n.Err("extension-market_security.package")
	}
	var oci *ociReference
	if strings.HasPrefix(pkg.URL, "oci:") {
		ref, err := parseOCI(pkg.URL)
		if err != nil {
			return "", "", err
		}
		if pkg.Size <= 0 {
			return "", "", i18n.Err("extension-market_security.package")
		}
		oci = &ref
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	file := filepath.Join(dir, "download")
	if from, name := cached(m.Cache, pkg.SHA256); from != "" {
		validName := true
		if oci != nil {
			name = path.Base(name)
			validName = ociTitle.MatchString(name)
		}
		if err := os.Link(from, file); err != nil {
			if err := copyFile(from, file, 0o600); err != nil {
				return "", "", err
			}
		}
		fi, statErr := os.Stat(file)
		if got, err := fileSum(file); err == nil && strings.EqualFold(got, pkg.SHA256) && validName && statErr == nil && fi.Size() <= PackageMost && (pkg.Size <= 0 || fi.Size() == pkg.Size) {
			report(ctx, StepDownload, StepEvent{State: StepDone, Bytes: fi.Size(), Total: fi.Size(), Cached: true})
			reportState(ctx, StepVerify, StepDone)
			return file, name, nil
		}
		_ = os.Remove(from) // not what it should be: download it again
		_ = os.Remove(file)
	}
	attempts := []bool{false}
	if oci != nil && m.RegistryMirror != "" {
		attempts = []bool{true, false}
	}
	var name string
	var lastErr error
	for _, mirror := range attempts {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		budget := 5 * time.Minute
		if mirror {
			budget = mirrorFetchFor
		}
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, budget)
		// Truncate the staging file and recreate every writer, hash and watchdog.
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			cancelAttempt()
			return "", "", err
		}
		h := sha256.New()
		dctx, pw, stop := watchDownload(attemptCtx, io.MultiWriter(f, h), pkg.Size)
		if oci != nil {
			name, err = m.downloadOCI(dctx, *oci, mirror, pw)
		} else {
			name, err = m.download(dctx, pkg.URL, pw)
		}
		stalled := stop()
		closeErr := f.Close()
		cancelAttempt()
		if err == nil {
			err = closeErr
		}
		if err == nil && (pw.n > PackageMost || pkg.Size > 0 && pw.n != pkg.Size) {
			err = i18n.Err("extension-market_security.checksum")
		}
		if err == nil {
			reportState(ctx, StepVerify, StepRunning)
			if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, pkg.SHA256) {
				err = errOfKind(ErrChecksum, "%s", i18n.Err("extension-market_security.checksum"))
			}
		}
		if err != nil {
			lastErr = err
			if stalled {
				m.client().CloseIdleConnections()
				lastErr = errOfKind(ErrDownloadStalled, "%s", i18n.Err("extension-market_security.stalled"))
			}
			_ = os.Remove(file)
			continue
		}
		report(ctx, StepDownload, StepEvent{State: StepDone, Bytes: pw.n, Total: max(pw.n, pkg.Size)})
		reportState(ctx, StepVerify, StepDone)
		keep(m.Cache, pkg.SHA256, name, file)
		return file, name, nil
	}
	return "", "", lastErr
}

// fileSum is the sha256 of a file, in hex.
func fileSum(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *Market) download(ctx context.Context, ref string, w io.Writer) (string, error) {
	const octet = "application/octet-stream"
	switch {
	case strings.HasPrefix(ref, "https://"):
		u, _ := url.Parse(ref)
		return u.Path, m.get(ctx, ref, octet, w, PackageMost)
	case strings.HasPrefix(ref, "release:"):
		if m.Spec.Kind != "github" {
			return "", errors.New("release: 只能用在 GitHub 市场")
		}
		tag, asset, ok := strings.Cut(strings.TrimPrefix(ref, "release:"), "/")
		if !ok || tag == "" || asset == "" {
			return "", errors.New("写成 release:<tag>/<附件名>")
		}
		var b strings.Builder
		rel := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", m.api(), url.PathEscape(m.Spec.Owner), url.PathEscape(m.Spec.Repo), url.PathEscape(tag))
		if err := m.get(ctx, rel, "application/vnd.github+json", &b, IndexMost); err != nil {
			if _, web, ok := m.githubFallback(); ok && errors.Is(err, ErrRateLimited) {
				// The download link of a public release's asset is not the API's: no limit, the sha256 still checks it.
				u := fmt.Sprintf("%s/%s/%s/releases/download/%s/%s", web, url.PathEscape(m.Spec.Owner), url.PathEscape(m.Spec.Repo),
					url.PathEscape(tag), url.PathEscape(asset))
				if ferr := m.get(ctx, u, octet, w, PackageMost); ferr == nil {
					return asset, nil
				}
			}
			return "", fmt.Errorf("Release %s：%w", tag, err)
		}
		var r struct {
			Assets []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"assets"`
		}
		if err := json.Unmarshal([]byte(b.String()), &r); err != nil {
			return "", err
		}
		for _, a := range r.Assets {
			if a.Name == asset {
				return asset, m.get(ctx, a.URL, octet, w, PackageMost)
			}
		}
		return "", fmt.Errorf("Release %s 里没有 %s", tag, asset)
	default:
		clean := path.Clean("/" + ref)[1:]
		if clean == "" || strings.HasPrefix(ref, "/") || clean != ref {
			return "", errors.New("包的相对路径不能跳出市场")
		}
		if m.Spec.Kind == "git" {
			// A catalog mirror supplies no repository files. A fresh Market used by
			// installation may also have an old checkout from a previous read.
			if err := m.syncGit(ctx); err != nil {
				return "", err
			}
			return clean, m.gitFile(clean, w, PackageMost)
		}
		if m.Spec.Kind == "github" {
			return clean, m.getRepoFile(ctx, clean, w, PackageMost)
		}
		base, _ := url.Parse(m.Spec.URL)
		u := base.ResolveReference(&url.URL{Path: clean})
		return u.Path, m.get(ctx, u.String(), octet, w, PackageMost)
	}
}

// MarketOrigin is the origin a plugin installed from a market records: market:<market id>/<plugin id>.
func MarketOrigin(marketID, extensionID string) string {
	return "market:" + marketID + "/" + extensionID
}

// FromMarket is the market a plugin's origin says it came from ("" when not from a market).
func FromMarket(origin string) string {
	rest, ok := strings.CutPrefix(origin, "market:")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// MarketRecord is a market's row (table plugin_markets).
type MarketRecord struct {
	ID          string          `json:"id"`
	Spec        string          `json:"spec"`
	Name        string          `json:"name"`
	TokenSealed string          `json:"-"`
	Index       json.RawMessage `json:"-"`
	Warnings    []string        `json:"warnings"`
	Error       string          `json:"error,omitempty"`
	FetchedAt   *time.Time      `json:"fetched_at,omitempty"`
	AddedAt     time.Time       `json:"added_at"`
	// SigningKey is a legacy stored official pin. Unofficial pins are cleared on load;
	// official verification uses the compiled-in key and quarantines unrecognized pins.
	SigningKey string `json:"-"`
	// Signature is the state of the index last read.
	Signature MarketSignature `json:"signature"`
}

// MarketStore keeps the markets (the hub's Postgres).
type MarketStore interface {
	ExtensionMarkets(ctx context.Context) ([]MarketRecord, error)
	PutExtensionMarket(ctx context.Context, r MarketRecord) error
	DeleteExtensionMarket(ctx context.Context, id string) error
}
