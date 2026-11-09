package extmgr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestExtensionAutoUpgradeMarketRemoved(t *testing.T) {
	for _, tc := range []struct {
		name     string
		auto     bool
		disabled bool
		more     bool
	}{
		{name: "auto on", auto: true},
		{name: "auto off"},
		{name: "disabled", auto: true, disabled: true},
		{name: "needs consent", auto: true, more: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, phone, mk, dir, mid := upgradeHub(t)
			ctx := t.Context()
			u := h.extensionUpgrades()
			detail := func() map[string]any {
				t.Helper()
				code, out := call(t, mux, "GET", "/api/v1/extensions/example-ui/-/", phone, "")
				if code != 200 {
					t.Fatalf("detail: %d %v", code, out)
				}
				return out
			}
			setAuto := func(auto bool) {
				t.Helper()
				code, out := call(t, mux, "PUT", "/api/v1/extensions/example-ui/-/auto-upgrade", phone, fmt.Sprintf(`{"auto": %t}`, auto))
				if code != 200 || out["auto_upgrade"] != auto {
					t.Fatalf("auto-upgrade: %d %v", code, out)
				}
			}
			setAuto(tc.auto)
			if tc.disabled {
				if code, out := call(t, mux, "PUT", "/api/v1/extensions/example-ui/-/enabled", phone, `{"enabled": false}`); code != 200 {
					t.Fatalf("disable: %d %v", code, out)
				}
			}

			setUIVersion(t, dir, "1.1.0")
			if tc.more {
				editManifest(t, dir, func(m map[string]any) {
					m["permissions"] = map[string]any{"launcher_keys": true, "network": []string{"api.example.com"}}
				})
			}
			mk.publish(dir)
			if _, err := h.marketRead(ctx, mid); err != nil {
				t.Fatal(err)
			}
			if up := upgradeOf(t, mux, phone); up == nil || up["to"] != "1.1.0" {
				t.Fatalf("no candidate before removal: %v", up)
			}

			if code, out := call(t, mux, "DELETE", "/api/v1/extensions/-/markets/"+mid, phone, ""); code != 200 {
				t.Fatalf("remove: %d %v", code, out)
			}
			assertPaused := func() {
				t.Helper()
				d := detail()
				if d["auto_upgrade"] != tc.auto || d["auto_upgrade_paused"] != "market_removed" || d["upgrade"] != nil {
					t.Fatalf("removed market: auto %v, paused %v, upgrade %v", d["auto_upgrade"], d["auto_upgrade_paused"], d["upgrade"])
				}
				if up := upgradeOf(t, mux, phone); up != nil {
					t.Fatalf("removed market still offers a candidate: %v", up)
				}
			}
			assertPaused()
			// The saved preference remains writable while the source is absent.
			setAuto(!tc.auto)
			setAuto(tc.auto)
			setUIVersion(t, dir, "1.2.0")
			mk.publish(dir)
			mk.mu.Lock()
			reads, downloads := mk.reads, mk.downloads
			mk.mu.Unlock()
			u.check(ctx)
			u.check(ctx)
			assertPaused()
			if v := installedVersion(h); v != "1.0.0" {
				t.Fatalf("upgraded after removal: %s", v)
			}
			mk.mu.Lock()
			gotReads, gotDownloads := mk.reads, mk.downloads
			mk.mu.Unlock()
			if gotReads != reads || gotDownloads != downloads {
				t.Fatalf("removed market was read: reads %d -> %d, downloads %d -> %d", reads, gotReads, downloads, gotDownloads)
			}
			var saved extensionUpgradeSettings
			if _, err := h.store.Setting(ctx, extensionUpgradesKey, &saved); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(saved.Off, "example-ui") == tc.auto || saved.Notified["example-ui"] != "" || detail()["upgrade_note"] != nil {
				t.Fatalf("removal changed preferences or announced an upgrade: %+v", saved)
			}

			code, out := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec": %q}`, mk.srv.URL+"/plugins/"))
			if code != 200 || out["id"] != mid {
				t.Fatalf("re-add same address: %d %v", code, out)
			}
			d := detail()
			var paused any
			if tc.disabled {
				paused = autoUpgradePausedDisabled
			}
			if d["auto_upgrade"] != tc.auto || d["auto_upgrade_paused"] != paused {
				t.Fatalf("restored market: auto %v, paused %v", d["auto_upgrade"], d["auto_upgrade_paused"])
			}
			u.check(ctx)
			if tc.auto && !tc.disabled && !tc.more {
				waitLong(t, "upgrade after restoring the market", func() bool { return installedVersion(h) == "1.2.0" })
			} else {
				want := upgradeOff
				if tc.more {
					want = upgradeNeedsConsent
				}
				if up := upgradeOf(t, mux, phone); up == nil || up["state"] != want || installedVersion(h) != "1.0.0" {
					t.Fatalf("restored preference: version %s, candidate %v", installedVersion(h), up)
				}
			}
		})
	}
}

type unavailableMarketStore struct{ *extensionStore }

func (s unavailableMarketStore) ExtensionMarkets(context.Context) ([]extension.MarketRecord, error) {
	return nil, errors.New("market registrations unavailable")
}

func TestExtensionAutoUpgradeMarketRegistration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loaded    bool
		state     *marketState
		loadFails bool
		paused    string
	}{
		{name: "registered but not loaded"},
		{name: "registration load failed", loadFails: true},
		{name: "removed", loaded: true, paused: "market_removed"},
		{name: "catalog unreadable", loaded: true, state: &marketState{rec: extension.MarketRecord{Error: "catalog unavailable"}}},
		{name: "entry absent", loaded: true, state: &marketState{index: &extension.MarketIndex{}}},
		{name: "bad signature", loaded: true, state: &marketState{rec: extension.MarketRecord{Signature: extension.MarketSignature{State: extension.SigInvalid}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestManager(t)
			st := newExtensionStore()
			h.store = st
			if tc.loadFails {
				h.store = unavailableMarketStore{st}
			}
			h.extensions = &extensions{h: h, root: t.TempDir()}
			h.extensions.reg = extension.NewRegistry(h.extensions.root, st, nil, 0)
			h.extensions.markets = markets{loaded: tc.loaded, byID: map[string]*marketState{}}
			if tc.state != nil {
				h.extensions.markets.byID["source"] = tc.state
			}
			if !tc.loaded && !tc.loadFails {
				now := time.Now()
				if err := st.PutExtensionMarket(t.Context(), extension.MarketRecord{ID: "source", Spec: "https://market.example.com/", FetchedAt: &now}); err != nil {
					t.Fatal(err)
				}
			}
			p := &extension.Extension{Record: extension.Record{ID: "example-ui", Source: extension.SourceInstalled, Origin: "market:source/example-ui", Enabled: true}}
			var v ExtensionView
			h.extensionUpgrades().decorate(t.Context(), &v, p)
			if v.AutoUpgrade == nil || !*v.AutoUpgrade || v.AutoUpgradePaused != tc.paused || v.Upgrade != nil {
				t.Fatalf("registration state: %+v", v)
			}
		})
	}
}
