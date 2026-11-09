package extmgr

import (
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// MarketView is a market as the apps list it.
type MarketView struct {
	ID          string     `json:"id"`
	Spec        string     `json:"spec"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Homepage    string     `json:"homepage,omitempty"`
	HasToken    bool       `json:"has_token"`
	Extensions  int        `json:"extensions"`
	Warnings    []string   `json:"warnings"`
	Error       string     `json:"error,omitempty"`
	FetchedAt   *time.Time `json:"fetched_at,omitempty"`
	AddedAt     time.Time  `json:"added_at"`
	// Official: the market every hub has without adding it (extension.Official), listed first.
	Official bool `json:"official,omitempty"`
	// Signature is the index's signature (docs/extensions/extensions.md "Signatures"): signed / unsigned / invalid / key_changed,
	// and the key it is signed by.
	Signature MarketSignatureView `json:"signature"`
}

// MarketSignatureView is a market's signature as the apps show it.
type MarketSignatureView struct {
	State string `json:"state"`
	KeyID string `json:"key_id,omitempty"`
}

// CatalogItem is a market's plugin as the catalog shows it, with what this hub has of it.
type CatalogItem struct {
	Replaces    string                     `json:"replaces,omitempty"`
	RenameFrom  string                     `json:"rename_from,omitempty"`
	Market      string                     `json:"market"`
	MarketName  string                     `json:"market_name"`
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Version     string                     `json:"version"`
	Description string                     `json:"description,omitempty"`
	Publisher   string                     `json:"publisher,omitempty"`
	Homepage    string                     `json:"homepage,omitempty"`
	Tags        []string                   `json:"tags"`
	Contributes map[string][]string        `json:"contributes"`
	Permissions []extension.PermissionLine `json:"permissions"`
	// Problem: why this hub cannot install it (no package for its platform, a newer hub needed); "" when it can.
	Problem string `json:"problem,omitempty"`
	// Installed is the version this hub has ("" none); InstalledSource where that one is from (builtin, hub.json,
	// installed); FromThisMarket whether it was installed from this market; Upgradable whether this one is newer.
	Installed       string `json:"installed,omitempty"`
	InstalledSource string `json:"installed_source,omitempty"`
	FromThisMarket  bool   `json:"from_this_market,omitempty"`
	Upgradable      bool   `json:"upgradable,omitempty"`
}

// marketBefore orders markets: the official one first, then by when they were added.
func marketBefore(a, b extension.MarketRecord) int {
	if oa, ob := extension.IsOfficialMarket(a.Spec), extension.IsOfficialMarket(b.Spec); oa != ob {
		if oa {
			return -1
		}
		return 1
	}
	if c := a.AddedAt.Compare(b.AddedAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// marketName is what the apps show for a market: the official one is "Official extensions" in l whatever its index calls itself
// (#5017), the others the name their index gives.
func marketName(l i18n.Lang, rec extension.MarketRecord) string {
	if extension.IsOfficialMarket(rec.Spec) {
		return i18n.T(l, "hub-extension_market.official_name")
	}
	return rec.Name
}

func (h *Manager) marketView(s *marketState) MarketView {
	v := MarketView{ID: s.rec.ID, Spec: s.rec.Spec, Name: marketName(h.userLang(), s.rec), HasToken: s.rec.TokenSealed != "", Warnings: s.rec.Warnings,
		Error: s.rec.Error, FetchedAt: s.rec.FetchedAt, AddedAt: s.rec.AddedAt, Official: extension.IsOfficialMarket(s.rec.Spec),
		Signature: MarketSignatureView{State: s.rec.Signature.State, KeyID: s.rec.Signature.KeyID}}
	if v.Signature.State == "" { // a market read before #1848
		v.Signature.State = extension.SigUnsigned
	}
	if v.Warnings == nil {
		v.Warnings = []string{}
	}
	if s.index != nil {
		v.Description, v.Homepage, v.Extensions = s.index.Description, s.index.Homepage, len(s.index.Extensions)
	}
	if v.Name == "" {
		v.Name = s.rec.Spec
	}
	return v
}

func (h *Manager) marketViews() []MarketView {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	states := make([]*marketState, 0, len(ms.byID))
	for _, s := range ms.byID {
		states = append(states, &marketState{rec: s.rec, index: s.index})
	}
	slices.SortFunc(states, func(a, b *marketState) int { return marketBefore(a.rec, b.rec) })
	out := []MarketView{}
	for _, s := range states {
		out = append(out, h.marketView(s))
	}
	return out
}

// catalog is every market's plugins, filtered by q (in the id, name, description, publisher, tags) and market.
func (h *Manager) catalog(q, market string) []CatalogItem {
	return h.catalogIn(h.userLang(), q, market)
}

// catalogIn is catalog with its problems in l.
func (h *Manager) catalogIn(l i18n.Lang, q, market string) []CatalogItem {
	ms := h.marketsState()
	ms.mu.Lock()
	states := make([]*marketState, 0, len(ms.byID))
	for _, s := range ms.byID {
		states = append(states, &marketState{rec: s.rec, index: s.index})
	}
	ms.mu.Unlock()
	slices.SortFunc(states, func(a, b *marketState) int { return marketBefore(a.rec, b.rec) })
	q = strings.ToLower(strings.TrimSpace(q))
	out := []CatalogItem{}
	for _, s := range states {
		if s.index == nil || (market != "" && market != s.rec.ID) {
			continue
		}
		for i := range s.index.Extensions {
			e := &s.index.Extensions[i]
			m := e.Parsed
			if q != "" && !strings.Contains(strings.ToLower(strings.Join(append([]string{m.ID, m.Name, m.Description, m.Publisher}, e.Tags...), "\n")), q) {
				continue
			}
			it := CatalogItem{Replaces: e.Replaces, Market: s.rec.ID, MarketName: marketName(l, s.rec), ID: m.ID, Name: m.Name, Version: m.Version, Description: m.Description,
				Publisher: m.Publisher, Homepage: m.Homepage, Tags: append([]string{}, e.Tags...), Contributes: m.Contributions(), Permissions: extension.ExplainIn(l, m)}
			if s.rec.Signature.Bad() || (extension.IsOfficialMarket(s.rec.Spec) && (s.rec.Signature.State != extension.SigSigned || s.rec.Signature.KeyID != keyIDOf(extension.Official.SigningKey))) {
				it.Problem = i18n.T(l, "hub-extension_market.bad_signature", i18n.Args{"err": s.rec.Error})
			} else if e.PackageFor(extension.Platform()) == nil {
				it.Problem = i18n.T(l, "hub-extension_market.no_package", i18n.Args{"platform": extension.Platform()})
			} else if _, err := extension.CheckEngines(m, h.build.Number); err != nil {
				it.Problem = i18n.Localize(err, l)
			}
			cur := h.extensions.reg.GetExact(m.ID)
			if cur == nil && e.Replaces != "" {
				if old := h.extensions.reg.GetExact(e.Replaces); old != nil && old.Record.Source == extension.SourceInstalled && old.Record.Origin == "market:"+s.rec.ID+"/"+e.Replaces {
					cur, it.RenameFrom = old, old.Record.ID
				}
			}
			if cur != nil {
				it.Installed, it.InstalledSource = cur.Record.Version, cur.Record.Source
				it.FromThisMarket = extension.FromMarket(cur.Record.Origin) == s.rec.ID
				it.Upgradable = cur.Record.Source == extension.SourceInstalled && extension.NewerVersion(m.Version, cur.Record.Version)
			}
			out = append(out, it)
		}
	}
	return out
}

// marketEntry is a market's entry for plugin id.
func (h *Manager) marketEntry(market, id string) (extension.MarketRecord, *extension.MarketEntry, error) {
	if h.extensions != nil && h.extensions.reg != nil {
		id = h.extensions.reg.CanonicalID(id)
	}
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return marketEntryLocked(ms, market, id)
}

func marketEntryLocked(ms *markets, market, id string) (extension.MarketRecord, *extension.MarketEntry, error) {
	s := ms.byID[market]
	if s == nil {
		return extension.MarketRecord{}, nil, errNoMarket
	}
	if s.rec.Signature.Bad() || (extension.IsOfficialMarket(s.rec.Spec) && (s.rec.Signature.State != extension.SigSigned || s.rec.Signature.KeyID != keyIDOf(extension.Official.SigningKey))) {
		// Nothing is installed or upgraded from a market whose signature fails, not even from its last good index.
		return extension.MarketRecord{}, nil, i18n.Err("hub-extension_market.bad_signature_install", i18n.Args{"err": s.rec.Error})
	}
	if s.index != nil {
		for i := range s.index.Extensions {
			if e := &s.index.Extensions[i]; e.Parsed.ID == id {
				return s.rec, e, nil
			}
		}
		// Only an absent normal entry may resolve to its single declared successor in this source.
		var successor *extension.MarketEntry
		for i := range s.index.Extensions {
			e := &s.index.Extensions[i]
			if e.Replaces != id {
				continue
			}
			if successor != nil {
				return extension.MarketRecord{}, nil, i18n.Err("extension-identity.invalid")
			}
			successor = e
		}
		if successor != nil {
			return s.rec, successor, nil
		}
	}
	return extension.MarketRecord{}, nil, i18n.Err("hub-extension_market.not_in_market", i18n.Args{"id": id})
}
