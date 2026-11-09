package extmgr

import (
	"context"
	"testing"
)

// With the switch for all or the extension's own off, a version that asks for more is only shown on its page: no note,
// not recorded as told, no push (#1828). Once both are on again the user is told once.
func TestExtensionAutoUpgradeOffKeepsReviewQuiet(t *testing.T) {
	for _, tc := range []struct{ name, off, on string }{
		{"switch for all", "/api/v1/plugins/-/upgrades", "/api/v1/plugins/-/upgrades"},
		{"its own switch", "/api/v1/plugins/example-ui/-/auto-upgrade", "/api/v1/plugins/example-ui/-/auto-upgrade"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, phone, mk, dir, mid := upgradeHub(t)
			ctx := context.Background()
			u := h.extensionUpgrades()
			pushes := func() int {
				th := hubOf(h)
				th.mu.Lock()
				defer th.mu.Unlock()
				n := 0
				for _, p := range th.pushes {
					if p["collapse_id"] == "plugin_upgrade.example-ui" {
						n++
					}
				}
				return n
			}
			stored := func() extensionUpgradeSettings {
				var s extensionUpgradeSettings
				if _, err := h.store.Setting(ctx, extensionUpgradesKey, &s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			if code, out := call(t, mux, "PUT", tc.off, phone, `{"auto": false}`); code != 200 {
				t.Fatalf("off: %d %v", code, out)
			}
			setUIVersion(t, dir, "1.2.0")
			editManifest(t, dir, func(m map[string]any) {
				m["permissions"] = map[string]any{"launcher_keys": true, "network": []string{"api.example.com"}}
			})
			mk.publish(dir)
			if _, err := h.marketRead(ctx, mid); err != nil {
				t.Fatal(err)
			}

			u.upgrade(ctx)
			u.upgrade(ctx)
			if v := installedVersion(h); v != "1.0.0" {
				t.Fatalf("upgraded: %s", v)
			}
			if up := upgradeOf(t, mux, phone); up["state"] != upgradeNeedsConsent || up["added_permissions"] == nil {
				t.Fatalf("the page should still show what it asks for: %v", up)
			}
			_, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
			if d["upgrade_note"] != nil {
				t.Fatalf("note while a switch is off: %v", d["upgrade_note"])
			}
			if s := stored(); s.Notified["example-ui"] != "" {
				t.Fatalf("recorded as told while a switch is off: %+v", s)
			}
			if n := pushes(); n != 0 {
				t.Fatalf("%d pushes while a switch is off", n)
			}

			if code, out := call(t, mux, "PUT", tc.on, phone, `{"auto": true}`); code != 200 {
				t.Fatalf("on: %d %v", code, out)
			}
			u.upgrade(ctx)
			u.upgrade(ctx)
			if s := stored(); s.Notified["example-ui"] != "1.2.0" || s.Last["example-ui"].Kind != upgradeNeedsConsent {
				t.Fatalf("not told once on again: %+v", s)
			}
			if n := pushes(); n != 1 {
				t.Fatalf("%d pushes once on again, want 1", n)
			}
			if v := installedVersion(h); v != "1.0.0" {
				t.Fatalf("upgraded without consent: %s", v)
			}
		})
	}
}
