package extmgr

import (
	"context"
	"errors"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Plugins upgrade themselves from their markets (docs/extensions/extensions.md "Automatic upgrades"): the hub reads the registered
// markets every few hours and installs a newer version of a plugin it installed from one, when the version asks for
// nothing more than the user granted and nothing is using the plugin. One that asks for more waits for the user; one
// that does not come up is rolled back (watchUpgrade) and not tried again. One switched off waits until it is on again:
// with no program running, watchUpgrade could not tell whether the new version comes up. Built-in, hub.json's and
// plugins installed from elsewhere (a directory, a file, a URL, a remote socket) are never upgraded by the hub.

const (
	extensionUpgradesKey  = "plugin_upgrades"
	extensionUpgradeActor = "自动升级"
	// extensionUpgradeEvery is how often it looks (reading a market only when its index is due); extensionUpgradeFirst is
	// the first look after the hub starts.
	extensionUpgradeEvery = time.Minute
	extensionUpgradeFirst = 2 * time.Minute
	// marketJitter spreads the markets' reads; marketBackoff is the first wait after a read failed (doubling, up to
	// marketStale).
	marketJitter  = 30 * time.Minute
	marketBackoff = 15 * time.Minute
)

// States of a newer version (ExtensionUpgrade.State).
const (
	upgradeAvailable    = "available"        // it goes in at the next look
	upgradeWaitingIdle  = "waiting_idle"     // the plugin (or the assistant) is busy
	upgradeNeedsConsent = "needs_permission" // it asks for more: the user confirms it in the app
	upgradeOff          = "off"              // auto-upgrade is off for it (or for all), or paused while it is switched off
	upgradeSkipped      = "skipped"          // this version rolled back: not tried again
	upgradeFailed       = "failed"           // installing it failed: tried again after the market's next read
	upgradeIncompatible = "incompatible"     // no package for this hub, or a newer hub needed
)

// skippedByUser is extensionUpgradeSettings.SkippedBy (and ExtensionUpgrade.SkippedBy) for a version the user rolled back
// from by hand, as opposed to one that did not come up.
const skippedByUser = "user"

// Reasons why ExtensionView.AutoUpgradePaused suspends the saved auto-upgrade switch without changing it.
const (
	autoUpgradePausedDisabled      = "disabled"
	autoUpgradePausedMarketRemoved = "market_removed"
)

// extensionUpgradeSettings are the switches and what the hub remembers (the hub's settings, key plugin_upgrades).
type extensionUpgradeSettings struct {
	Auto *bool    `json:"auto,omitempty"` // nil: on
	Off  []string `json:"off,omitempty"`  // plugins the user switched off
	// Skipped: plugin → the version that rolled back; Notified: plugin → the version the user was told asks for more.
	Skipped  map[string]string               `json:"skipped,omitempty"`
	Notified map[string]string               `json:"notified,omitempty"`
	Last     map[string]ExtensionUpgradeNote `json:"last,omitempty"`
	// SkippedBy: plugin → skippedByUser when its skipped version is one the user rolled back from (absent: it did not
	// come up).
	SkippedBy map[string]string `json:"skipped_by,omitempty"`
}

// autoOff: automatic upgrades are off for plugin id (its storage key), by the switch for all or by its own.
func (s *extensionUpgradeSettings) autoOff(id string) bool {
	return (s.Auto != nil && !*s.Auto) || slices.Contains(s.Off, id)
}

// ExtensionUpgradeNote is what happened last with a plugin's upgrade, for its page in the app.
type ExtensionUpgradeNote struct {
	Kind   string    `json:"kind"` // upgraded | rolled_back | needs_permission | failed
	From   string    `json:"from"`
	To     string    `json:"to"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// ExtensionUpgrade is a newer version of a market plugin (GET /api/v1/plugins/-/upgrades, ExtensionView.upgrade).
type ExtensionUpgrade struct {
	RenameFrom       string                     `json:"rename_from,omitempty"`
	RenameTo         string                     `json:"rename_to,omitempty"`
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Market           string                     `json:"market"`
	From             string                     `json:"from"`
	To               string                     `json:"to"`
	State            string                     `json:"state"`
	AddedPermissions []extension.PermissionLine `json:"added_permissions,omitempty"`
	Detail           string                     `json:"detail,omitempty"`
	// SkippedBy is "user" for a skipped version the user rolled back from (absent: it did not come up).
	SkippedBy string `json:"skipped_by,omitempty"`
}

type extensionUpgrades struct {
	h   *Manager
	run sync.Mutex // one look at a time

	mu       sync.Mutex
	save     sync.Mutex // one write of the settings at a time, of the newest (change)
	loaded   bool
	settings extensionUpgradeSettings
	waiting  map[string]string // plugin → why it waits (the last look found it busy)
	failed   map[string]string // plugin → the version that failed to install
	inflight map[string]string // plugin → the version being installed by the hub
	fails    map[string]int    // market → reads failed in a row
	jitter   map[string]time.Duration
	// busy says why plugin id may not be upgraded now ("" when it may); tests replace it.
	busy func(id string) string
}

// extensionUpgrades is the hub's auto-upgrade (nil without plugins).
func (h *Manager) extensionUpgrades() *extensionUpgrades {
	if h.extensions == nil {
		return nil
	}
	u := &h.extensions.upgrades
	u.mu.Lock()
	if u.h == nil {
		u.h = h
		u.busy = h.extensionBusy
	}
	u.mu.Unlock()
	return u
}

// extensionBusy is why plugin id is in use: a call or a request in flight, or the assistant working (it may call the
// plugin's tools next).
func (h *Manager) extensionBusy(id string) string {
	if hp := h.extensions.get(id); hp != nil && hp.proc != nil && !hp.proc.Idle() {
		return i18n.T(h.userLang(), "hub-extension_autoupgrade.busy_extension")
	}
	for _, m := range h.machinesNow() {
		for _, s := range h.decorated(m) {
			if s.Assistant && (s.State == proto.StateWorking || s.Status == proto.StatusWorking) {
				return i18n.T(h.userLang(), "hub-extension_autoupgrade.busy_assistant")
			}
		}
	}
	return ""
}

// extensionUpgradeLoop looks for newer versions until ctx ends.
func (h *Manager) extensionUpgradeLoop(ctx context.Context) {
	u := h.extensionUpgrades()
	if u == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(extensionUpgradeFirst):
	}
	t := time.NewTicker(extensionUpgradeEvery)
	defer t.Stop()
	for {
		u.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// check reads the markets that are due and upgrades what may be.
func (u *extensionUpgrades) check(ctx context.Context) {
	if !u.run.TryLock() {
		return
	}
	defer u.run.Unlock()
	h := u.h
	if h.marketStore() == nil || !h.extensions.reg.HasStore() || h.marketsLoad(ctx) != nil {
		return
	}
	u.load(ctx)
	u.readMarkets(ctx)
	u.upgrade(ctx)
}

// load reads the settings once.
func (u *extensionUpgrades) load(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.loaded {
		return
	}
	if u.h.store != nil {
		if _, err := u.h.store.Setting(ctx, extensionUpgradesKey, &u.settings); err != nil {
			return // tried again at the next look
		}
	}
	u.loaded = true
}

// change changes the settings and keeps them.
func (u *extensionUpgrades) change(ctx context.Context, f func(s *extensionUpgradeSettings)) error {
	u.load(ctx)
	u.mu.Lock()
	f(&u.settings)
	u.mu.Unlock()
	if u.h.store == nil {
		return nil
	}
	// The copy is taken after the previous write, under the lock: it has every change so far, and its maps are not
	// the ones the next change writes to while it is encoded.
	u.save.Lock()
	defer u.save.Unlock()
	u.mu.Lock()
	s := u.settings.clone()
	u.mu.Unlock()
	return u.h.store.PutSetting(ctx, extensionUpgradesKey, s)
}

func (s extensionUpgradeSettings) clone() extensionUpgradeSettings {
	s.Off = slices.Clone(s.Off)
	s.Skipped, s.Notified, s.Last = maps.Clone(s.Skipped), maps.Clone(s.Notified), maps.Clone(s.Last)
	s.SkippedBy = maps.Clone(s.SkippedBy)
	return s
}

func (s *extensionUpgradeSettings) note(id string, n ExtensionUpgradeNote) {
	if s.Last == nil {
		s.Last = map[string]ExtensionUpgradeNote{}
	}
	s.Last[id] = n
}

// forget drops what the hub remembers of plugin id's upgrades once it is uninstalled, so that installing the same id
// again starts afresh: no version skipped by an old rollback, the user told again of one asking for more, no old note
// on its page. Off stays: it is the user's own switch for that id. The settings are written only when they held some.
func (u *extensionUpgrades) forget(ctx context.Context, id string) {
	if u == nil {
		return
	}
	u.load(ctx)
	u.mu.Lock()
	delete(u.waiting, id)
	delete(u.failed, id)
	delete(u.inflight, id)
	_, skipped := u.settings.Skipped[id]
	_, notified := u.settings.Notified[id]
	_, last := u.settings.Last[id]
	u.mu.Unlock()
	if !skipped && !notified && !last {
		return
	}
	if err := u.change(ctx, func(s *extensionUpgradeSettings) {
		delete(s.Skipped, id)
		delete(s.SkippedBy, id)
		delete(s.Notified, id)
		delete(s.Last, id)
	}); err != nil {
		u.h.logf("plugin %s: forgetting its upgrades: %v", id, err)
	}
}

// rolledBackByUser keeps version, which the user rolled back from by hand (the admin page, the extension API that
// personad extension rollback uses), from being installed again by auto-upgrade until the market has a newer one, as
// an automatic rollback does (watched); the new-version row says why (#1826). A note left of version (upgraded to it)
// goes: it is not installed any more. Nothing is pushed: the user did it.
func (u *extensionUpgrades) rolledBackByUser(ctx context.Context, id, version string) {
	if u == nil || version == "" {
		return
	}
	u.load(ctx)
	key := u.storageKey(id)
	if err := u.change(ctx, func(s *extensionUpgradeSettings) {
		if s.Skipped == nil {
			s.Skipped = map[string]string{}
		}
		if s.SkippedBy == nil {
			s.SkippedBy = map[string]string{}
		}
		s.Skipped[key], s.SkippedBy[key] = version, skippedByUser
		if n, ok := s.Last[key]; ok && n.To == version {
			delete(s.Last, key)
		}
	}); err != nil {
		u.h.logf("plugin %s: keeping %s from auto-upgrade after its rollback: %v", id, version, err)
	}
}

// readMarkets reads each market whose index is due: marketStale (and a jitter of its own) after the last read, or
// after a backoff when that one failed. A market is logged when it stops answering and when it answers again.
func (u *extensionUpgrades) readMarkets(ctx context.Context) {
	h := u.h
	now := time.Now()
	for _, v := range h.marketViews() {
		u.mu.Lock()
		if u.jitter == nil {
			u.jitter, u.fails = map[string]time.Duration{}, map[string]int{}
		}
		j, ok := u.jitter[v.ID]
		if !ok {
			j = rand.N(marketJitter)
			u.jitter[v.ID] = j
		}
		wait := marketStale + j
		if v.Error != "" {
			wait = min(marketBackoff<<min(max(u.fails[v.ID]-1, 0), 8), marketStale)
		}
		u.mu.Unlock()
		if v.FetchedAt != nil && now.Sub(*v.FetchedAt) < wait {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		_, err := h.marketRead(rctx, v.ID)
		cancel()
		u.mu.Lock()
		n := u.fails[v.ID]
		if err != nil {
			u.fails[v.ID] = n + 1
		} else {
			delete(u.fails, v.ID)
			for id := range u.failed { // a fresh index: what failed may be tried again
				if p := h.extensions.reg.Get(id); p == nil || extension.FromMarket(p.Record.Origin) == v.ID {
					delete(u.failed, id)
				}
			}
		}
		u.mu.Unlock()
		switch {
		case err != nil && n == 0:
			h.logf("plugin market %s: %v (trying again later)", v.ID, err)
		case err == nil && n > 0:
			h.logf("plugin market %s: answering again", v.ID)
		}
	}
}

// granted is the digest of what the user granted plugin p.
func granted(p *extension.Extension) string {
	if p.Record.GrantedDigest != "" {
		return p.Record.GrantedDigest
	}
	return extension.Digest(p.Full)
}

// upgradesFrom is the market p upgrades itself from, "" for none: not installed from a market, or attached (it runs on
// its own, so the manager has no program of it to replace).
func upgradesFrom(p *extension.Extension) string {
	if p.Record.Source != extension.SourceInstalled || p.Record.Remote != nil {
		return ""
	}
	return extension.FromMarket(p.Record.Origin)
}

// candidate is p's newer version in its market, and where it stands (ok false: none, or p is not a market plugin).
func (u *extensionUpgrades) candidate(p *extension.Extension) (up ExtensionUpgrade, rec extension.MarketRecord, e *extension.MarketEntry, ok bool) {
	return u.candidateIn(p, u.h.userLang())
}

func (u *extensionUpgrades) candidateIn(p *extension.Extension, l i18n.Lang) (up ExtensionUpgrade, rec extension.MarketRecord, e *extension.MarketEntry, ok bool) {
	mid := upgradesFrom(p)
	if mid == "" {
		return up, rec, nil, false
	}
	rec, e, err := u.h.marketEntry(mid, p.Record.ID)
	if err != nil || !extension.NewerVersion(e.Parsed.Version, p.Record.Version) {
		return up, rec, nil, false
	}
	m := e.Parsed
	if m.ID != p.Record.ID && (e.Replaces != p.Record.ID || p.Record.Origin != "market:"+mid+"/"+p.Record.ID || u.h.extensions.reg.Get(m.ID) != nil) {
		return up, rec, nil, false
	}
	id := p.Record.StorageKey()
	up = ExtensionUpgrade{ID: p.Record.ID, Name: p.Manifest.Name, Market: mid, From: p.Record.Version, To: m.Version}
	if m.ID != p.Record.ID {
		up.RenameFrom, up.RenameTo = p.Record.ID, m.ID
	}
	bundleConsent := false
	if len(m.Depends) > 0 || len(m.ExtensionPack) > 0 {
		choices, unresolved := u.h.chooseBundle(context.Background(), m, mid)
		bundleConsent = len(unresolved) > 0
		for child, choice := range choices {
			if child != id && !choice.installed {
				bundleConsent = true
			}
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.settings
	switch _, engines := extension.CheckEngines(m, u.h.build.Number); {
	case e.PackageFor(extension.Platform()) == nil:
		up.State, up.Detail = upgradeIncompatible, i18n.T(l, "hub-extension_autoupgrade.no_package", i18n.Args{"platform": extension.Platform()})
	case engines != nil:
		up.State, up.Detail = upgradeIncompatible, i18n.Localize(engines, l)
	case s.Skipped[id] == m.Version:
		up.State, up.Detail = upgradeSkipped, i18n.T(l, "hub-extension_autoupgrade.skipped")
		if s.SkippedBy[id] == skippedByUser {
			up.SkippedBy, up.Detail = skippedByUser, i18n.T(l, "hub-extension_autoupgrade.skipped_by_user")
		}
	case !p.Record.Enabled: // before asking for consent: the user is not told of versions of one they switched off
		up.State, up.Detail = upgradeOff, i18n.T(l, "hub-extension_autoupgrade.paused_disabled")
	case up.RenameFrom != "" && s.autoOff(id):
		up.State = upgradeOff
	case up.RenameFrom != "":
		up.State, up.Detail = upgradeNeedsConsent, i18n.T(l, "extension-identity.confirmation", i18n.Args{"from": up.RenameFrom, "to": up.RenameTo})
	case extension.MorePermissions(p, m) || extension.Digest(m) != granted(p):
		up.State, up.AddedPermissions = upgradeNeedsConsent, extension.AddedIn(l, p.Manifest, m)
	case bundleConsent:
		up.State, up.Detail = upgradeNeedsConsent, i18n.T(l, "hub-extension_autoupgrade.bundle_confirmation")
	case s.autoOff(id):
		up.State = upgradeOff
	case u.failed[id] == m.Version:
		up.State = upgradeFailed
		if n, ok := s.Last[id]; ok && n.Kind == upgradeFailed {
			up.Detail = n.Detail
		}
	case u.waiting[id] != "":
		up.State, up.Detail = upgradeWaitingIdle, u.waiting[id]
	default:
		up.State = upgradeAvailable
	}
	return up, rec, e, true
}

// upgrade installs every newer version that may go in now, and tells the user once of one that asks for more while
// automatic upgrades are on for it. With either switch off a version that asks for more is only shown on the plugin's
// page (#1828): no note, no broadcast, no push, as for any other new version; the user is told once both are on again.
func (u *extensionUpgrades) upgrade(ctx context.Context) {
	h := u.h
	for _, p := range h.extensions.reg.List() {
		id := p.Record.StorageKey()
		up, rec, e, ok := u.candidate(p)
		if !ok {
			u.mu.Lock()
			n, had := u.settings.Last[id]
			u.mu.Unlock()
			if had && (n.Kind == upgradeNeedsConsent || n.Kind == upgradeFailed) { // the user upgraded it
				_ = u.change(ctx, func(s *extensionUpgradeSettings) { delete(s.Last, id) })
			}
			continue
		}
		switch up.State {
		case upgradeNeedsConsent:
			u.mu.Lock()
			told := u.settings.Notified[id] == up.To
			quiet := u.settings.autoOff(id) // only shown on its page while a switch is off
			u.mu.Unlock()
			if !told && !quiet {
				n := ExtensionUpgradeNote{Kind: upgradeNeedsConsent, From: up.From, To: up.To, Detail: upgradeConsentText(h.userLang(), up), At: time.Now()}
				_ = u.change(ctx, func(s *extensionUpgradeSettings) {
					if s.Notified == nil {
						s.Notified = map[string]string{}
					}
					s.Notified[id] = up.To
					s.note(id, n)
				})
				u.tell(p.Record.ID, n, "consent", func(l i18n.Lang) i18n.Args {
					return i18n.Args{"name": up.Name, "version": up.To, "detail": upgradeConsentText(l, up)}
				})
			}
		case upgradeAvailable, upgradeWaitingIdle:
			why := u.busy(p.Record.ID)
			u.mu.Lock()
			if u.waiting == nil {
				u.waiting = map[string]string{}
			}
			if why != "" {
				u.waiting[id] = why
			} else {
				delete(u.waiting, id)
			}
			u.mu.Unlock()
			if why == "" {
				u.install(ctx, p, rec, e, up)
			}
		}
	}
}

// morePermissionsText says what a newer version asks for more, in l.
func upgradeConsentText(l i18n.Lang, up ExtensionUpgrade) string {
	if up.RenameFrom != "" {
		return i18n.T(l, "extension-identity.confirmation", i18n.Args{"from": up.RenameFrom, "to": up.RenameTo})
	}
	if up.Detail != "" {
		return i18n.T(l, "hub-extension_autoupgrade.bundle_confirmation")
	}
	return morePermissionsText(l, up.AddedPermissions)
}

func morePermissionsText(l i18n.Lang, added []extension.PermissionLine) string {
	const ns = "hub-extension_autoupgrade."
	if len(added) == 0 {
		return i18n.T(l, ns+"permissions_changed")
	}
	n := len(added)
	text := i18n.T(l, ns+"more_permissions", i18n.Args{"count": n})
	if n <= 5 { // a small number reads as a word (the Chinese for "one more permission")
		text = i18n.T(l, ns+"more_permissions_few", i18n.Args{"count": n, "word": i18n.T(l, ns+"number_"+strconv.Itoa(n))})
	}
	for i, p := range added {
		if i > 0 {
			text += i18n.T(l, ns+"list_separator")
		}
		if p.Detail != "" {
			text += i18n.T(l, ns+"permission_detail", i18n.Args{"title": p.Title, "detail": p.Detail})
		} else {
			text += p.Title
		}
	}
	return text
}

// install upgrades p to up.To from its market (watchUpgrade then says whether it came up: watched).
func (u *extensionUpgrades) install(ctx context.Context, p *extension.Extension, rec extension.MarketRecord, e *extension.MarketEntry, up ExtensionUpgrade) {
	if e.Parsed.ID != p.Record.ID || up.RenameFrom != "" {
		return
	} // Identity changes never install automatically.
	h, reg, id := u.h, u.h.extensions.reg, p.Record.ID
	key := p.Record.StorageKey()
	op := h.startOp(opUpgrade, id, extensionUpgradeActor, h.contextLang(ctx), true) // the apps see it go (#3277)
	mk, err := h.market(rec)
	var pv *extension.Preview
	if err == nil {
		pv, err = reg.PreviewMarket(extension.WithProgress(ctx, h.progress(op)), mk, rec.ID, e, id)
	}
	if err == nil && (pv.RenameFrom != "" || pv.MorePermissions || len(pv.AddedPermissions) > 0 || pv.Digest != granted(p)) {
		reg.Discard(pv.ID) // the index said the same as the package, so this does not happen
		err = i18n.Err("hub-extension_autoupgrade.package_more_permissions")
	}
	if err == nil {
		u.mu.Lock()
		if u.inflight == nil {
			u.inflight = map[string]string{}
		}
		u.inflight[key] = up.To
		u.mu.Unlock()
		h.opPreviewed(op, pv, nil)
		h.installing(op, pv, "")
		h.opStep(op, extension.StepConsent, extension.StepSkipped) // nothing more than was granted: nobody is asked
		var np *extension.Extension
		if np, err = reg.UpgradePlan(ctx, id, pv.ID, "", nil, extensionUpgradeActor); err != nil {
			u.mu.Lock()
			delete(u.inflight, key)
			u.mu.Unlock()
		} else {
			go h.settle(context.WithoutCancel(ctx), op, np, true)
		}
	}
	if err == nil {
		h.logf("plugin %s: upgraded itself from %s to %s", id, up.From, up.To)
		return
	}
	h.opFail(op, err)
	h.logf("plugin %s: upgrading to %s: %v", id, up.To, err)
	u.mu.Lock()
	if u.failed == nil {
		u.failed = map[string]string{}
	}
	u.failed[key] = up.To
	u.mu.Unlock()
	n := ExtensionUpgradeNote{Kind: upgradeFailed, From: up.From, To: up.To, Detail: err.Error(), At: time.Now()}
	_ = u.change(ctx, func(s *extensionUpgradeSettings) { s.note(key, n) })
	h.broadcastExtension("upgrade", id, map[string]any{"note": n})
}

// watched is how an upgrade of plugin id from one version to another ended: rolled back (that version is skipped from
// now on, whoever upgraded it), or up (told when the hub upgraded it).
func (u *extensionUpgrades) watched(ctx context.Context, id, from, to string, rolledBack bool, why string) {
	if u == nil {
		return
	}
	key := u.storageKey(id)
	u.mu.Lock()
	auto := u.inflight[key] == to
	delete(u.inflight, key)
	u.mu.Unlock()
	name := id
	if p := u.h.extensions.reg.Get(id); p != nil {
		name = p.Manifest.Name
	}
	if rolledBack {
		n := ExtensionUpgradeNote{Kind: "rolled_back", From: from, To: to, Detail: why, At: time.Now()}
		_ = u.change(ctx, func(s *extensionUpgradeSettings) {
			if s.Skipped == nil {
				s.Skipped = map[string]string{}
			}
			s.Skipped[key] = to
			delete(s.SkippedBy, key) // this one did not come up
			s.note(key, n)
		})
		u.tell(id, n, "rolled_back", func(i18n.Lang) i18n.Args { return i18n.Args{"name": name, "version": to, "from": from} })
		return
	}
	if !auto {
		_ = u.change(ctx, func(s *extensionUpgradeSettings) { delete(s.Last, key) })
		return
	}
	n := ExtensionUpgradeNote{Kind: "upgraded", From: from, To: to, At: time.Now()}
	_ = u.change(ctx, func(s *extensionUpgradeSettings) { s.note(key, n) })
	u.tell(id, n, "upgraded", func(i18n.Lang) i18n.Args { return i18n.Args{"name": name, "version": to} })
}

// tell tells the apps (the plugin's page) and sends a quiet push, like the hub's own update. The push's title and
// body are hub-extension_autoupgrade.<what>_title / _body, each device's in its language.
func (u *extensionUpgrades) tell(id string, n ExtensionUpgradeNote, what string, args func(i18n.Lang) i18n.Args) {
	u.h.broadcastExtension("upgrade", id, map[string]any{"note": n})
	u.h.pushLocalized(func(l i18n.Lang) map[string]any {
		a := args(l)
		return map[string]any{
			"aps": map[string]any{"alert": map[string]any{
				"title": i18n.T(l, "hub-extension_autoupgrade."+what+"_title", a),
				"body":  i18n.T(l, "hub-extension_autoupgrade."+what+"_body", a),
			}, "thread-id": "plugin_upgrade", "interruption-level": "passive"},
			"plugin_upgrade":    map[string]any{"plugin": id, "kind": n.Kind, "version": n.To},
			"extension_upgrade": map[string]any{"extension": id, "kind": n.Kind, "version": n.To},
		}
	}, "plugin_upgrade."+id)
}

func (u *extensionUpgrades) storageKey(id string) string {
	if p := u.h.extensions.reg.Get(id); p != nil {
		return p.Record.StorageKey()
	}
	return id
}

// auto says whether plugin id upgrades itself (both switches on).
func (u *extensionUpgrades) auto(id string) (global, own bool) {
	id = u.storageKey(id)
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.settings.Auto == nil || *u.settings.Auto, !slices.Contains(u.settings.Off, id)
}

// decorate adds to a market plugin's view its switch (and why it is paused), its newer version and what happened last.
func (u *extensionUpgrades) decorate(ctx context.Context, v *ExtensionView, p *extension.Extension) {
	if u == nil || upgradesFrom(p) == "" {
		return
	}
	u.load(ctx)
	_, own := u.auto(p.Record.ID)
	v.AutoUpgrade = &own
	if !p.Record.Enabled {
		v.AutoUpgradePaused = autoUpgradePausedDisabled
	}
	// Load registration state before treating a missing entry as removal. An unreadable catalog is still registered.
	if u.h.marketsLoad(ctx) == nil {
		ms := u.h.marketsState()
		ms.mu.Lock()
		if ms.byID[upgradesFrom(p)] == nil {
			v.AutoUpgradePaused = autoUpgradePausedMarketRemoved
		}
		ms.mu.Unlock()
	}
	if up, _, _, ok := u.candidateIn(p, u.h.contextLang(ctx)); ok {
		v.Upgrade = &up
	}
	u.mu.Lock()
	if n, ok := u.settings.Last[p.Record.StorageKey()]; ok {
		v.UpgradeNote = &n
	}
	u.mu.Unlock()
}

// upgradesView is GET /api/v1/plugins/-/upgrades.
type upgradesView struct {
	Auto     bool                            `json:"auto"`
	Upgrades []ExtensionUpgrade              `json:"upgrades"`
	Off      []string                        `json:"off"`
	Last     map[string]ExtensionUpgradeNote `json:"last"`
}

func (u *extensionUpgrades) view(ctx context.Context) upgradesView {
	u.load(ctx)
	v := upgradesView{Upgrades: []ExtensionUpgrade{}}
	for _, p := range u.h.extensions.reg.List() {
		if up, _, _, ok := u.candidateIn(p, u.h.contextLang(ctx)); ok {
			v.Upgrades = append(v.Upgrades, up)
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	v.Auto = u.settings.Auto == nil || *u.settings.Auto
	v.Off = []string{}
	for _, id := range u.settings.Off {
		v.Off = append(v.Off, u.h.extensions.reg.CanonicalID(id))
	}
	v.Last = map[string]ExtensionUpgradeNote{}
	for id, n := range u.settings.Last {
		v.Last[u.h.extensions.reg.CanonicalID(id)] = n
	}
	return v
}

// upgradeRoutes are the auto-upgrade's switches and list (a device with full rights).
func (h *Manager) upgradeRoutes(api *http.ServeMux) {
	gate := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if principalOf(r.Context()).Role != roleFull {
				writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
				return
			}
			if h.extensionUpgrades() == nil {
				writeErrCode(w, http.StatusServiceUnavailable, "no_plugins", errors.New("plugins are not set up"))
				return
			}
			f(w, r)
		}
	}
	api.HandleFunc("GET /api/v1/extensions/-/upgrades", gate(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.extensionUpgrades().view(r.Context()))
	}))
	api.HandleFunc("PUT /api/v1/extensions/-/upgrades", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Auto *bool `json:"auto"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if in.Auto == nil {
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("auto is missing"))
			return
		}
		u := h.extensionUpgrades()
		if err := u.change(r.Context(), func(s *extensionUpgradeSettings) { s.Auto = in.Auto }); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, u.view(r.Context()))
	}))
	api.HandleFunc("PUT /api/v1/extensions/{id}/-/auto-upgrade", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Auto *bool `json:"auto"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if in.Auto == nil {
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("auto is missing"))
			return
		}
		id := r.PathValue("id")
		p := h.extensions.reg.Get(id)
		if p == nil {
			writeErrCode(w, http.StatusNotFound, "not_found", errors.New("no such plugin"))
			return
		}
		if upgradesFrom(p) == "" {
			writeErrCode(w, http.StatusBadRequest, "not_from_market", i18n.Err("hub-extension_autoupgrade.not_from_market"))
			return
		}
		u := h.extensionUpgrades()
		key := p.Record.StorageKey()
		if err := u.change(r.Context(), func(s *extensionUpgradeSettings) {
			s.Off = slices.DeleteFunc(s.Off, func(x string) bool { return x == key })
			if !*in.Auto {
				s.Off = append(s.Off, key)
			}
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, detailOut(r, h.extensions.detail(r.Context(), h.extensions.reg.Get(id))))
	}))
}
