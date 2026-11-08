package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/netproxy"
)

// Plugin markets (docs/extensions/extensions.md "Extension markets"): the hub keeps the markets the user registered and their
// indexes, lists their plugins as one catalog, and installs one from a market. A device with full rights may do all of
// this; other sources (a directory, a file, a URL, an upload) stay with the hub's machine and the admin UI.

// marketStale is how old an index may be before the catalog reads it again (in the background).
const marketStale = 6 * time.Hour

type markets struct {
	mu      sync.Mutex
	loaded  bool
	byID    map[string]*marketState
	reading map[string]bool
	// Client and GitHubAPI: tests point them at a fake.
	client    *http.Client
	githubAPI string
}

type marketState struct {
	rec   extension.MarketRecord
	index *extension.MarketIndex
}

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

// errMarketSignature: a market's index was read but its signature refuses it (the last good one is kept but not
// installed from). A catalog that could not be read is extension.ErrCatalogUnavailable instead.
var errMarketSignature = errors.New("market signature")

// signedIndex enforces official identity independently of stored signature state.
func signedIndex(rec extension.MarketRecord, f *extension.Fetched, now time.Time) (extension.MarketRecord, *extension.MarketIndex, error) {
	key := ""
	rec.FetchedAt = &now
	if extension.IsOfficialMarket(rec.Spec) {
		key = extension.Official.SigningKey
		if _, err := extension.ParseSigningKey(key); err != nil {
			rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
			rec.Error = i18n.T(i18n.Default, "extension-market_security.signature")
			return rec, nil, fmt.Errorf("%w: %s", errMarketSignature, rec.Error)
		}
		if rec.SigningKey != "" && rec.SigningKey != key {
			rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
			rec.Error = i18n.T(i18n.Default, "extension-market_security.owner_pin")
			return rec, nil, fmt.Errorf("%w: %s", errMarketSignature, rec.Error)
		}
	} else {
		rec.SigningKey = ""
	}
	verified, err := extension.ParseCatalog(f.Raw, key)
	if err != nil {
		rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
		rec.Error = err.Error()
		return rec, nil, fmt.Errorf("%w: %s", errMarketSignature, rec.Error)
	}
	rec.Signature = extension.MarketSignature{State: extension.SigUnsigned}
	if key != "" {
		rec.Signature = extension.MarketSignature{State: extension.SigSigned, KeyID: keyIDOf(key)}
	}
	// An empty owner pin is not silently rewritten. Runtime verification uses only
	// the compiled-in key; approved pin migration belongs to the rotation workflow.
	rec.Error, rec.Name, rec.Warnings, rec.Index = "", verified.Index.Name, verified.Warnings, append([]byte(nil), verified.Raw...)
	return rec, verified.Index, nil
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

func (h *Manager) marketStore() extension.MarketStore {
	s, _ := h.store.(extension.MarketStore)
	return s
}

func (h *Manager) marketsState() *markets {
	if h.extensions == nil {
		return nil
	}
	return &h.extensions.markets
}

// load reads the markets from the store once.
func (h *Manager) marketsLoad(ctx context.Context) error {
	ms, st := h.marketsState(), h.marketStore()
	if ms == nil || st == nil {
		return i18n.Err("hub-extension_market.no_database")
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.loaded {
		return nil
	}
	rows, err := st.ExtensionMarkets(ctx)
	if err != nil {
		return err
	}
	ms.byID = map[string]*marketState{}
	for _, r := range rows {
		state := &marketState{rec: r}
		if len(r.Index) > 0 {
			// Never trust a cached state flag: the complete envelope is verified again.
			f := &extension.Fetched{Raw: r.Index}
			rec, ix, verifyErr := signedIndex(r, f, time.Now())
			rec.FetchedAt = r.FetchedAt
			if r.Signature.Bad() && extension.IsOfficialMarket(r.Spec) {
				rec.Signature, rec.Error = r.Signature, r.Error
				ix = nil
			}
			if verifyErr != nil || ix == nil {
				rec.FetchedAt = nil
			}
			state.rec, state.index = rec, ix
		} else if extension.IsOfficialMarket(r.Spec) {
			state.rec.Signature = extension.MarketSignature{State: extension.SigInvalid}
			state.rec.Error = i18n.T(i18n.Default, "extension-market_security.cache")
			state.rec.FetchedAt = nil
		} else {
			state.rec.SigningKey = ""
			state.rec.Signature = extension.MarketSignature{State: extension.SigUnsigned}
			state.rec.Error = ""
		}
		if err := st.PutExtensionMarket(ctx, state.rec); err != nil {
			return err
		}
		ms.byID[r.ID] = state
	}
	id, err := h.seedOfficialMarket(ctx)
	if err != nil {
		return err
	}
	ms.loaded = true
	if id != "" {
		ms.readLater(h, id)
	}
	for id, state := range ms.byID {
		if state.rec.FetchedAt == nil {
			ms.readLater(h, id)
		}
	}

	return nil
}

// officialMarketKey is the setting that says the official market was added (its spec): once only, so a market its
// owner removed is not added again; a new official spec is added once more.
const officialMarketKey = "extension_official_market"

// seedOfficialMarket adds the official market to a hub that has not had it yet (a new one, or one upgraded from before
// #2351), with its pinned key; a market of the same repository the owner already added counts as it (ms.mu held).
// It is the id of the one it added ("" none).
func (h *Manager) seedOfficialMarket(ctx context.Context) (string, error) {
	o := extension.Official
	if o.Spec == "" {
		return "", nil
	}
	var done struct {
		Spec string `json:"spec"`
	}
	found, err := h.store.Setting(ctx, officialMarketKey, &done)
	if err != nil {
		return "", err
	}
	ms, st := h.marketsState(), h.marketStore()
	rotated := ""
	for _, s := range ms.byID {
		if !extension.IsOfficialMarket(s.rec.Spec) || s.rec.SigningKey == "" || s.rec.SigningKey == o.SigningKey ||
			!slices.Contains(o.PreviousSigningKeys, s.rec.SigningKey) {
			continue
		}
		key, err := extension.ParseSigningKey(o.SigningKey)
		if err != nil {
			return "", err
		}
		if !found || done.Spec != o.Spec {
			registered := done
			registered.Spec = o.Spec
			if err := h.store.PutSetting(ctx, officialMarketKey, registered); err != nil {
				return "", err
			}
			done, found = registered, true
		}
		rec := s.rec
		rec.SigningKey = o.SigningKey
		rec.Signature = extension.MarketSignature{State: extension.SigKeyChanged, KeyID: extension.KeyID(key)}
		rec.Error = i18n.T(h.contextLang(ctx), "hub-extension_market.pin_rotated_pending")
		if err := st.PutExtensionMarket(ctx, rec); err != nil {
			return "", err
		}
		s.rec, s.index = rec, nil
		rotated = rec.ID
	}
	if rotated != "" {
		return rotated, nil
	}
	if found && done.Spec == o.Spec {
		return "", nil
	}
	mark := func() error { done.Spec = o.Spec; return h.store.PutSetting(ctx, officialMarketKey, done) }
	for _, s := range ms.byID {
		if extension.IsOfficialMarket(s.rec.Spec) {
			return "", mark()
		}
	}
	spec, err := extension.ParseMarket(o.Spec)
	if err != nil {
		return "", err
	}
	rec := extension.MarketRecord{ID: extension.MarketID(spec), Spec: spec.String(), SigningKey: o.SigningKey, AddedAt: time.Now(), Warnings: []string{},
		Signature: extension.MarketSignature{State: extension.SigInvalid}, Error: i18n.T(i18n.Default, "extension-market_security.cache")}
	if err := st.PutExtensionMarket(ctx, rec); err != nil {
		return "", err
	}
	ms.byID[rec.ID] = &marketState{rec: rec}
	if err := mark(); err != nil {
		return "", err
	}
	return rec.ID, nil
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

func (h *Manager) market(rec extension.MarketRecord) (*extension.Market, error) {
	spec, err := extension.ParseMarket(rec.Spec)
	if err != nil {
		return nil, err
	}
	mk := &extension.Market{Spec: spec, TrustedKey: rec.SigningKey, Client: h.marketClient(), GitHubAPI: h.extensions.markets.githubAPI, Dir: h.marketsDir(), Cache: h.packageCache()}
	if extension.IsOfficialMarket(rec.Spec) {
		mk.TrustedKey = extension.Official.SigningKey
	}
	if rec.TokenSealed != "" {
		if mk.Token, err = (hubSealer{h}).Open(rec.TokenSealed); err != nil {
			return nil, i18n.Err("hub-extension_market.token_unreadable")
		}
	}
	if err := h.configureMarket(mk); err != nil {
		return nil, err
	}
	return mk, nil
}

// marketRead reads a market's index now and keeps it (or why it could not). An index its signature refuses is not
// taken: the last good one stays, and the market says why (docs/extensions/extensions.md "Signatures"). A catalog
// that could not be read at all (extension.ErrCatalogUnavailable: 404, 401, a timeout) changes no signature state:
// an official market keeps installing from its last verified index.
func (h *Manager) marketRead(ctx context.Context, id string) (MarketView, error) {
	ms := h.marketsState()
	ms.mu.Lock()
	s := ms.byID[id]
	var snapshot extension.MarketRecord
	if s != nil {
		snapshot = s.rec
	}
	ms.mu.Unlock()
	if s == nil {
		return MarketView{}, errNoMarket
	}
	mk, err := h.market(snapshot)
	var f *extension.Fetched
	if err == nil {
		f, err = mk.FetchSigned(ctx)
	}
	now := time.Now()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	s = ms.byID[id]
	if s == nil {
		return MarketView{}, errNoMarket
	}
	rec := s.rec
	if rec.Spec != snapshot.Spec || rec.SigningKey != snapshot.SigningKey || rec.TokenSealed != snapshot.TokenSealed {
		return h.marketView(s), i18n.Err("extension-market_security.changed")
	}
	nextIndex := s.index
	if err != nil {
		rec.FetchedAt = &now
		switch {
		case errors.Is(err, extension.ErrCatalogUnverified) && extension.IsOfficialMarket(rec.Spec):
			// A catalog was read and does not verify: quarantined, the last good index unusable too.
			rec.Signature, rec.Error = extension.MarketSignature{State: extension.SigInvalid}, err.Error()
			err = fmt.Errorf("%w: %w", errMarketSignature, err)
		case rec.Signature.Bad():
			// Not read (or not reached): a quarantine stands, and keeps saying why.
		default:
			// Not read: nothing was learned, so the last verified index and its state stay.
			rec.Error = err.Error()
		}
	} else {
		var ix *extension.MarketIndex
		if rec, ix, err = signedIndex(rec, f, now); ix != nil {
			nextIndex = ix
		} else {
			h.logf("extension market %s: %v", id, err)
		}
	}
	if perr := h.marketStore().PutExtensionMarket(ctx, rec); perr != nil {
		// Keep a failed verification quarantined in memory, but let writes retry.
		if rec.Signature.Bad() {
			s.rec = rec
		}
		return h.marketView(s), perr
	}
	s.rec, s.index = rec, nextIndex
	return h.marketView(s), err
}

func keyIDOf(key string) string {
	k, err := extension.ParseSigningKey(key)
	if err != nil {
		return "无"
	}
	return extension.KeyID(k)
}

// signatureText is a market's signature for the log.
func signatureText(s extension.MarketSignature) string {
	if s.State == extension.SigSigned {
		return "signed by " + s.KeyID
	}
	return s.State
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

var errNoMarket = i18n.Err("hub-extension_market.no_market")

// marketsDir is where git markets' clones are kept.
func (h *Manager) marketsDir() string { return filepath.Join(h.extensions.root, "markets") }

// packageCache is where market packages are kept by their sha256, so installing one again does not download it again.
func (h *Manager) packageCache() string { return filepath.Join(h.extensions.root, "cache", "packages") }

// marketClient is how the hub reads markets: GitHub's hosts through hub.json's github.proxy when it is set (the
// way the hub's other GitHub traffic goes; on a network that reaches GitHub badly a package download stalled for
// minutes without it, #3277), everything else as the environment says (HTTPS_PROXY). Tests set their own.
func (h *Manager) marketClient() *http.Client {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.client != nil {
		return ms.client
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	var gh *url.URL
	if h.cfg != nil && h.cfg.GitHub != nil {
		gh, _ = netproxy.Parse(h.cfg.GitHub.Proxy) // a malformed one: as the environment says (the connector says it is wrong)
	}
	if gh != nil {
		t.Proxy = func(r *http.Request) (*url.URL, error) {
			if githubHost(r.URL.Hostname()) {
				return gh, nil
			}
			return envProxy(r)
		}
	}
	ms.client = &http.Client{Transport: t}
	return ms.client
}

// envProxy is where marketClient sends hosts other than GitHub's: the environment's proxy. A variable so tests do
// not depend on the proxy of the machine running them (http.ProxyFromEnvironment reads it once per process).
var envProxy = http.ProxyFromEnvironment

// githubHost says whether host is GitHub's: its site, API and where release assets and raw files are served from.
func githubHost(host string) bool {
	host = strings.ToLower(host)
	return host == "ghcr.io" || host == "pkg-containers.githubusercontent.com" || host == "github.com" || strings.HasSuffix(host, ".github.com") || host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
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

// marketAdd registers a market after reading its index once (a market that cannot be read is not added).
func (h *Manager) marketAdd(ctx context.Context, specText, token string) (MarketView, error) {
	if err := h.marketsLoad(ctx); err != nil {
		return MarketView{}, err
	}
	spec, err := extension.ParseMarket(specText)
	if err != nil {
		return MarketView{}, err
	}
	if len(token) > extension.MarketTokenMost {
		return MarketView{}, i18n.Err("hub-extension_market.token_too_long")
	}
	id := extension.MarketID(spec)
	ms := h.marketsState()
	ms.mu.Lock()
	n, dup := len(ms.byID), ms.byID[id] != nil || ms.withSpec(spec.String()) != nil || (extension.IsOfficialMarket(spec.String()) && ms.official() != nil)
	ms.mu.Unlock()
	if dup {
		return MarketView{}, i18n.Err("hub-extension_market.duplicate")
	}
	if n >= extension.MarketsMost {
		return MarketView{}, i18n.Err("hub-extension_market.too_many")
	}
	mk := &extension.Market{Spec: spec, Token: token, Client: h.marketClient(), GitHubAPI: ms.githubAPI, Dir: h.marketsDir(), Cache: h.packageCache()}
	if err := h.configureMarket(mk); err != nil {
		return MarketView{}, err
	}
	f, err := mk.FetchSigned(ctx)
	if err != nil {
		return MarketView{}, err
	}
	now := time.Now()
	// Official identity determines verification; unofficial markets acquire no persistent pin.
	rec, ix, err := signedIndex(extension.MarketRecord{ID: id, Spec: spec.String(), AddedAt: now}, f, now)
	if err != nil {
		mk.RemoveCheckout()
		return MarketView{}, err
	}
	if token != "" {
		if rec.TokenSealed, err = (hubSealer{h}).Seal(token); err != nil {
			return MarketView{}, err
		}
	}
	ms.mu.Lock()
	if len(ms.byID) >= extension.MarketsMost || ms.byID[id] != nil || ms.withSpec(spec.String()) != nil || extension.IsOfficialMarket(spec.String()) && ms.official() != nil {
		ms.mu.Unlock()
		return MarketView{}, i18n.Err("hub-extension_market.duplicate")
	}
	if err := h.marketStore().PutExtensionMarket(ctx, rec); err != nil {
		ms.mu.Unlock()
		return MarketView{}, err
	}
	s := &marketState{rec: rec, index: ix}
	ms.byID[id] = s
	view := h.marketView(s)
	ms.mu.Unlock()
	h.logf("extension market %s added: %s (%d extensions, %s)", id, rec.Spec, len(ix.Extensions), signatureText(rec.Signature))
	return view, nil
}

// marketToken is the token market id was registered with (empty when it has none).
func (h *Manager) marketTokenFor(id string, target extension.MarketSpec) (string, error) {
	ms := h.marketsState()
	ms.mu.Lock()
	state := ms.byID[id]
	var rec extension.MarketRecord
	if state != nil {
		rec = state.rec
	}
	ms.mu.Unlock()
	if state == nil {
		return "", errNoMarket
	}
	if rec.TokenSealed == "" {
		return "", i18n.Err("hub-extension_market.no_token")
	}
	mk, err := h.market(rec)
	if err != nil {
		return "", err
	}
	if !mk.SharesTokenWith(target) {
		return "", i18n.Err("extension-market_security.credential_move")
	}
	return mk.Token, nil
}

// official is the registered official market (ms.mu held).
func (ms *markets) official() *marketState {
	for _, s := range ms.byID {
		if extension.IsOfficialMarket(s.rec.Spec) {
			return s
		}
	}
	return nil
}

// withSpec is the market registered at spec (ms.mu held).
func (ms *markets) withSpec(spec string) *marketState {
	for _, s := range ms.byID {
		if s.rec.Spec == spec {
			return s
		}
	}
	return nil
}

// moveTargetTaken checks both an exact address and the single official identity (ms.mu held).
func (ms *markets) moveTargetTaken(id, spec string) bool {
	for otherID, s := range ms.byID {
		if otherID != id && (s.rec.Spec == spec || extension.IsOfficialMarket(spec) && extension.IsOfficialMarket(s.rec.Spec)) {
			return true
		}
	}
	return false
}

// marketSetSpec moves market id to another address, keeping its id: what was installed from it (source
// market:<id>/<extension>) stays its own, upgrades and all. The new address is read once first, with the market's
// token; one that cannot be read changes nothing.
func (h *Manager) marketSetSpec(ctx context.Context, id, specText string) (MarketView, error) {
	if err := h.marketsLoad(ctx); err != nil {
		return MarketView{}, err
	}
	spec, err := extension.ParseMarket(specText)
	if err != nil {
		return MarketView{}, err
	}
	ms := h.marketsState()
	ms.mu.Lock()
	s := ms.byID[id]
	var snapshot extension.MarketRecord
	if s != nil {
		snapshot = s.rec
	}
	other := ms.moveTargetTaken(id, spec.String())
	ms.mu.Unlock()
	if s == nil {
		return MarketView{}, errNoMarket
	}
	if other {
		return MarketView{}, i18n.Err("hub-extension_market.address_taken")
	}
	if extension.IsOfficialMarket(snapshot.Spec) && !extension.IsOfficialMarket(spec.String()) {
		return MarketView{}, i18n.Err("extension-market_security.move")
	}
	old, err := h.market(snapshot)
	if err != nil {
		return MarketView{}, err
	}
	if old.Token != "" && !old.SharesTokenWith(spec) {
		return MarketView{}, i18n.Err("extension-market_security.credential_move")
	}
	mk := &extension.Market{Spec: spec, Token: old.Token, Client: h.marketClient(), GitHubAPI: ms.githubAPI, Dir: h.marketsDir(), Cache: h.packageCache()}
	if err := h.configureMarket(mk); err != nil {
		return MarketView{}, err
	}
	f, err := mk.FetchSigned(ctx)
	if err != nil {
		return MarketView{}, err
	}
	now := time.Now()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if s = ms.byID[id]; s == nil {
		return MarketView{}, errNoMarket
	}
	if s.rec.Spec != snapshot.Spec || s.rec.SigningKey != snapshot.SigningKey || s.rec.TokenSealed != snapshot.TokenSealed {
		return MarketView{}, i18n.Err("extension-market_security.changed")
	}
	// Another add or move may have claimed the target during the network read.
	if ms.moveTargetTaken(id, spec.String()) {
		return MarketView{}, i18n.Err("hub-extension_market.address_taken")
	}
	// Source and credential snapshots are checked again after the network read.
	candidate := s.rec
	candidate.Spec = spec.String()
	// Moving away from an official identity never turns its artifacts into unsigned ones.
	if extension.IsOfficialMarket(s.rec.Spec) && !extension.IsOfficialMarket(candidate.Spec) {
		return MarketView{}, i18n.Err("extension-market_security.catalog")
	}
	rec, ix, err := signedIndex(candidate, f, now)
	if err != nil {
		if old.Spec.String() != spec.String() {
			mk.RemoveCheckout()
		}
		return MarketView{}, err
	}
	rec.Spec = spec.String()
	if err := h.marketStore().PutExtensionMarket(ctx, rec); err != nil {
		return MarketView{}, err
	}
	if old.Spec.String() != spec.String() {
		old.RemoveCheckout()
	}
	s.rec, s.index = rec, ix
	h.logf("extension market %s moved: %s", id, rec.Spec)
	return h.marketView(s), nil
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

// refreshStale reads again, in the background, the indexes older than marketStale (one read per market at a time).
func (h *Manager) refreshStale() {
	ms := h.marketsState()
	ms.mu.Lock()
	defer ms.mu.Unlock()
	for id, s := range ms.byID {
		if s.rec.FetchedAt == nil || time.Since(*s.rec.FetchedAt) >= marketStale {
			ms.readLater(h, id)
		}
	}
}

// readLater reads market id again in the background, unless it is being read already (ms.mu held).
func (ms *markets) readLater(h *Manager, id string) {
	if ms.reading[id] {
		return
	}
	if ms.reading == nil {
		ms.reading = map[string]bool{}
	}
	ms.reading[id] = true
	go func() {
		defer func() { ms.mu.Lock(); delete(ms.reading, id); ms.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if _, err := h.marketRead(ctx, id); err != nil {
			h.logf("plugin market %s: %v", id, err)
		}
	}()
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

// marketRoutes are the markets' routes for the apps (a device with full rights).
func (h *Manager) marketRoutes(api *http.ServeMux) {
	h.marketRoutesAt(api, "/api/v1/extensions/-", func(w http.ResponseWriter, r *http.Request) bool {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
			return false
		}
		return true
	})
}

// marketRoutesAt registers the markets' routes under base (the apps' API, or the admin UI's behind its session).
func (h *Manager) marketRoutesAt(api *http.ServeMux, base string, allow func(w http.ResponseWriter, r *http.Request) bool) {
	h.marketSettingsRoutes(api, base, allow)
	gate := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !allow(w, r) {
				return
			}
			if err := h.marketsLoad(r.Context()); err != nil {
				writeErrCode(w, http.StatusServiceUnavailable, "no_markets", err)
				return
			}
			f(w, r)
		}
	}
	fail := func(w http.ResponseWriter, err error, code string) {
		if errors.Is(err, errNoMarket) {
			writeErrCode(w, http.StatusNotFound, "not_found", err)
			return
		}
		writeErrCode(w, http.StatusBadRequest, code, err)
	}
	api.HandleFunc("GET "+base+"/markets", gate(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"markets": marketsOut(r, h.marketViews())})
	}))
	api.HandleFunc("POST "+base+"/markets", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec  string `json:"spec"`
			Token string `json:"token,omitempty"`
			// TokenFrom: use the token of this registered market (one account's token reading several private
			// repositories), never seen by the caller.
			TokenFrom string `json:"token_from,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if in.TokenFrom != "" {
			if in.Token != "" {
				fail(w, i18n.Err("hub-extension_market.token_and_token_from"), "market_failed")
				return
			}
			target, err := extension.ParseMarket(in.Spec)
			if err != nil {
				fail(w, err, "market_failed")
				return
			}
			tok, err := h.marketTokenFor(in.TokenFrom, target)
			if err != nil {
				fail(w, err, "market_failed")
				return
			}
			in.Token = tok
		}
		v, err := h.marketAdd(r.Context(), in.Spec, in.Token)
		if err != nil {
			fail(w, err, "market_failed")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("DELETE "+base+"/markets/{mid}", gate(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("mid")
		ms := h.marketsState()
		ms.mu.Lock()
		s, ok := ms.byID[id]
		ms.mu.Unlock()
		if !ok {
			fail(w, errNoMarket, "")
			return
		}
		if mk, err := h.market(s.rec); err == nil {
			mk.RemoveCheckout()
		}
		if err := h.marketStore().DeleteExtensionMarket(r.Context(), id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		ms.mu.Lock()
		delete(ms.byID, id)
		ms.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	}))
	api.HandleFunc("PUT "+base+"/markets/{mid}", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec string `json:"spec"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		v, err := h.marketSetSpec(r.Context(), r.PathValue("mid"), in.Spec)
		if err != nil {
			fail(w, err, "market_failed")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("POST "+base+"/markets/{mid}/refresh", gate(func(w http.ResponseWriter, r *http.Request) {
		v, err := h.marketRead(r.Context(), r.PathValue("mid"))
		if errors.Is(err, errMarketSignature) {
			fail(w, err, "market_signature")
			return
		}
		if err != nil {
			fail(w, err, "market_unreachable")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("PUT "+base+"/markets/{mid}/token", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if len(in.Token) > extension.MarketTokenMost {
			fail(w, i18n.Err("hub-extension_market.token_too_long"), "invalid_token")
			return
		}
		id := r.PathValue("mid")
		sealed := ""
		if in.Token != "" {
			var err error
			if sealed, err = (hubSealer{h}).Seal(in.Token); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		ms := h.marketsState()
		ms.mu.Lock()
		s := ms.byID[id]
		if s != nil {
			rec := s.rec
			rec.TokenSealed = sealed
			if err := h.marketStore().PutExtensionMarket(r.Context(), rec); err != nil {
				ms.mu.Unlock()
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			s.rec = rec
		}
		ms.mu.Unlock()
		if s == nil {
			fail(w, errNoMarket, "")
			return
		}
		v, err := h.marketRead(r.Context(), id)
		if err != nil {
			fail(w, err, "market_unreachable")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("GET "+base+"/catalog", gate(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("refresh") == "1" {
			var wg sync.WaitGroup
			for _, v := range h.marketViews() {
				wg.Add(1)
				go func() { defer wg.Done(); _, _ = h.marketRead(r.Context(), v.ID) }()
			}
			wg.Wait()
		} else {
			h.refreshStale()
		}
		writeJSON(w, map[string]any{"markets": marketsOut(r, h.marketViews()), extKey(r, "extensions", "plugins"): h.catalogIn(h.requestLang(r), r.URL.Query().Get("q"), r.URL.Query().Get("market")),
			"platform": extension.Platform()})
	}))
	api.HandleFunc("POST "+base+"/catalog/preview", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Market    string `json:"market"`
			Extension string `json:"extension"`
			Legacy    string `json:"plugin"`
			// Async: answer at once with the operation, and preview in the background (#3277).
			Async bool `json:"async,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if in.Extension == "" {
			in.Extension = in.Legacy
		}
		rec, e, err := h.marketEntry(in.Market, in.Extension)
		if err != nil {
			fail(w, err, "not_in_market")
			return
		}
		mk, err := h.market(rec)
		if err != nil {
			fail(w, err, "market_failed")
			return
		}
		upgrade := ""
		if cur := h.extensions.reg.Get(in.Extension); cur != nil && cur.Record.Source == extension.SourceInstalled {
			upgrade = cur.Record.ID
		} else if e.Replaces != "" {
			if old := h.extensions.reg.GetExact(e.Replaces); old != nil {
				if old.Record.Source != extension.SourceInstalled || old.Record.Origin != "market:"+rec.ID+"/"+e.Replaces || h.extensions.reg.Get(e.Parsed.ID) != nil {
					fail(w, i18n.Err("extension-identity.invalid"), "identity_conflict")
					return
				}
				upgrade = old.Record.ID
			}
		}
		kind := opInstall
		if upgrade != "" {
			kind = opUpgrade
		}
		job := previewJob{kind: kind, id: in.Extension, ofMarket: true, actor: opActor(r, "personad extension"), lang: h.requestLang(r),
			run: func(ctx context.Context) (*extension.Preview, error) {
				pv, err := h.extensions.reg.PreviewMarket(ctx, mk, rec.ID, e, upgrade)
				if err != nil {
					return nil, err
				}
				// its missing dependencies, a pack's members, side by side (extension_model.go)
				h.resolveBundle(extension.WithoutProgress(ctx), pv, rec.ID)
				return pv, nil
			}}
		op, pv, err := h.launch(r.Context(), job, nil, in.Async)
		switch {
		case in.Async:
			h.writeOp(w, op)
		case err != nil:
			writeErrCode(w, http.StatusBadRequest, "invalid_plugin", err)
		default:
			writeJSON(w, withOperation(pv.In(h.requestLang(r)), op))
		}
	}))
}
