package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

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
