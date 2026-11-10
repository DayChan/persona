package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

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
	Replaces string          `json:"replaces,omitempty"`
	Manifest json.RawMessage `json:"manifest"`
	Tags     []string        `json:"tags,omitempty"`
	Packages []MarketPackage `json:"packages"`
	// L10n is the package's translations again ({"<language>": <l10n/<language>.json>}, l10n.go), so the catalog can
	// show them before anything is downloaded. Kept as written (read with Translations); hubs before it ignore the key.
	L10n json.RawMessage `json:"l10n,omitempty"`
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
	// Identity declarations are never silently dropped or resolved first-entry-wins.
	declaredTargets := map[string]bool{}
	for _, e := range ix.Extensions {
		if e.Replaces == "" {
			continue
		}
		m, err := Parse(e.Manifest)
		if err != nil || declaredTargets[m.ID] || !ValidID(e.Replaces) || e.Replaces == m.ID {
			return nil, nil, ErrIdentity
		}
		declaredTargets[m.ID] = true
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
			if _, err := ParseEntryTranslations(e.L10n); err != nil { // the entry stays, in its manifest's words
				warnings = append(warnings, i18n.T(i18n.En, "extension-status.entry_translation_skipped", i18n.Args{"id": m.ID, "err": err.Error()}))
				e.L10n = nil
			}
			kept = append(kept, e)
		}
	}
	ix.Extensions = kept
	if err := ValidateReplacements(ix.Extensions); err != nil {
		return nil, warnings, err
	}
	return &ix, warnings, nil
}

// Translations are the entry's l10n, read (ParseIndex has left out one that does not check out).
func (e *MarketEntry) Translations() Translations {
	ts, _ := ParseEntryTranslations(e.L10n)
	return ts
}

func (e *MarketEntry) check() (*Manifest, error) {
	if len(e.Manifest) == 0 {
		return nil, errors.New("没有 manifest")
	}
	m, err := Parse(e.Manifest)
	if err != nil {
		return nil, err
	}
	if e.Replaces != "" && (!ValidID(e.Replaces) || e.Replaces == m.ID) {
		return nil, ErrIdentity
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
