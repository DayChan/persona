package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestExtensionIdentitySuccessorPreviewAndNotificationOnly(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()
	old := h.extensions.reg.Snapshot("example-ui")
	editManifest(t, dir, func(m map[string]any) { m["id"], m["version"] = "renamed-ui", "1.1.0" })
	mk.publish(dir)
	mk.mu.Lock()
	var catalog map[string]any
	if err := json.Unmarshal(mk.index, &catalog); err != nil {
		t.Fatal(err)
	}
	catalog["plugins"].([]any)[0].(map[string]any)["replaces"] = "example-ui"
	mk.index, _ = json.Marshal(catalog)
	mk.mu.Unlock()
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}
	up, _, _, ok := u.candidate(old)
	if !ok || up.State != upgradeNeedsConsent || up.RenameFrom != "example-ui" || up.RenameTo != "renamed-ui" {
		t.Fatalf("successor notification: %+v, %v", up, ok)
	}
	u.upgrade(ctx)
	if installedVersion(h) != old.Record.Version || h.extensions.reg.GetExact("renamed-ui") != nil {
		t.Fatal("automatic identity migration")
	}
	if u.settings.Notified[old.Record.StorageKey()] != "1.1.0" {
		t.Fatal("successor notification not recorded by stable key")
	}
	for _, requested := range []string{"example-ui", "renamed-ui"} {
		code, pv := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "extension": %q}`, mid, requested))
		if code != 200 || pv["rename_from"] != "example-ui" || pv["upgrade"] != true {
			t.Fatalf("%s predecessor preview: %d %+v", requested, code, pv)
		}
	}
	// Off and rollback history remain installation preferences, never successor defaults.
	u.mu.Lock()
	u.settings.Off = []string{old.Record.StorageKey()}
	u.settings.Skipped = map[string]string{old.Record.StorageKey(): "1.0.9"}
	u.mu.Unlock()
	if up, _, _, ok := u.candidate(old); !ok || up.State != upgradeOff {
		t.Fatalf("off successor: %+v", up)
	}
}

// upgradeMarket is a site market serving one plugin's versions: the index lists the last one published.
type upgradeMarket struct {
	t         *testing.T
	mu        sync.Mutex
	index     []byte
	pkgs      map[string][]byte
	reads     int
	downloads int
	down      bool
	srv       *httptest.Server
}

func newUpgradeMarket(t *testing.T) *upgradeMarket {
	mk := &upgradeMarket{t: t, pkgs: map[string][]byte{}}
	mk.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mk.mu.Lock()
		defer mk.mu.Unlock()
		switch {
		case mk.down:
			http.Error(w, "down", http.StatusBadGateway)
		case r.URL.Path == "/plugins/"+extension.IndexFile:
			mk.reads++
			_, _ = w.Write(mk.index)
		case mk.pkgs[strings.TrimPrefix(r.URL.Path, "/plugins/pkg/")] != nil:
			mk.downloads++
			_, _ = w.Write(mk.pkgs[strings.TrimPrefix(r.URL.Path, "/plugins/pkg/")])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mk.srv.Close)
	return mk
}

// publish makes dir the market's newest version of its plugin.
func (mk *upgradeMarket) publish(dir string) {
	b, _ := os.ReadFile(filepath.Join(dir, extension.ManifestFile))
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &m)
	pkg := tarDir(mk.t, dir)
	mk.mu.Lock()
	defer mk.mu.Unlock()
	mk.pkgs[m.Version+".tar.gz"] = pkg
	mk.index = marketIndex(mk.t, "Example plugins", marketEntry(mk.t, dir, "pkg/"+m.Version+".tar.gz", pkg))
}

func (mk *upgradeMarket) readCount() int {
	mk.mu.Lock()
	defer mk.mu.Unlock()
	return mk.reads
}

// setUIVersion makes example-ui's package in dir version v (the program says so at initialize).
func setUIVersion(t *testing.T, dir, v string) {
	setVersion(t, dir, v)
	self, _ := os.Executable()
	_ = os.WriteFile(filepath.Join(dir, "bin", "ui"), []byte("#!/bin/sh\nEXAMPLE_UI_VERSION="+v+" PERSONA_TEST_PLUGIN=ui exec "+self+" \"$@\"\n"), 0o755)
}

// editManifest changes dir's manifest.
func editManifest(t *testing.T, dir string, f func(m map[string]any)) {
	p := filepath.Join(dir, extension.ManifestFile)
	b, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, _ = json.Marshal(m)
	_ = os.WriteFile(p, b, 0o644)
}

// upgradeHub is a hub with the market mk registered and example-ui 1.0.0 installed from it.
func upgradeHub(t *testing.T) (h *Manager, mux http.Handler, phone string, mk *upgradeMarket, dir, mid string) {
	h, phone, _ = marketDevices(t)
	mux = h.routes()
	mk = newUpgradeMarket(t)
	h.extensions.markets.client = mk.srv.Client()
	dir = uiPackage(t)
	mk.publish(dir)
	code, out := call(t, mux, "POST", "/api/v1/plugins/-/markets", phone, fmt.Sprintf(`{"spec": %q}`, mk.srv.URL+"/plugins/"))
	if code != 200 {
		t.Fatalf("market: %d %v", code, out)
	}
	mid = out["id"].(string)
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mid))
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("install: %d %v", code, out)
	}
	waitLong(t, "example-ui running", func() bool {
		return h.extensions.view(context.Background(), h.extensions.reg.Get("example-ui")).Status == "running"
	})
	return h, mux, phone, mk, dir, mid
}

func installedVersion(h *Manager) string { return h.extensions.reg.Get("example-ui").Record.Version }

func upgradeOf(t *testing.T, mux http.Handler, token string) map[string]any {
	t.Helper()
	code, out := call(t, mux, "GET", "/api/v1/plugins/-/upgrades", token, "")
	if code != 200 {
		t.Fatalf("upgrades: %d %v", code, out)
	}
	for _, x := range out["upgrades"].([]any) {
		if up := x.(map[string]any); up["id"] == "example-ui" {
			return up
		}
	}
	return nil
}

// A newer version asking for nothing more goes in by itself (audited as an automatic upgrade), not while the plugin is
// in use, not while it is switched off (for it, or for all); the user sees what happened on the plugin.
func TestExtensionAutoUpgrade(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()

	setUIVersion(t, dir, "1.1.0")
	mk.publish(dir)
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}
	if up := upgradeOf(t, mux, phone); up == nil || up["state"] != upgradeAvailable || up["to"] != "1.1.0" {
		t.Fatalf("not available: %v", up)
	}

	// Switched off for it: it stays; and for all, with it switched back on.
	if code, out := call(t, mux, "PUT", "/api/v1/plugins/example-ui/-/auto-upgrade", phone, `{"auto": false}`); code != 200 || out["auto_upgrade"] != false {
		t.Fatalf("switch: %d %v", code, out)
	}
	u.upgrade(ctx)
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("upgraded while switched off: %s", v)
	}
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeOff {
		t.Fatalf("%v", up)
	}
	call(t, mux, "PUT", "/api/v1/plugins/example-ui/-/auto-upgrade", phone, `{"auto": true}`)
	if code, out := call(t, mux, "PUT", "/api/v1/plugins/-/upgrades", phone, `{"auto": false}`); code != 200 || out["auto"] != false {
		t.Fatalf("global switch: %d %v", code, out)
	}
	u.upgrade(ctx)
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("upgraded while all are switched off: %s", v)
	}
	call(t, mux, "PUT", "/api/v1/plugins/-/upgrades", phone, `{"auto": true}`)

	// In use (a request open): it waits, and goes in once nothing uses it.
	end, err := h.extensions.get("example-ui").proc.BeginHTTP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.upgrade(ctx)
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("upgraded while in use: %s", v)
	}
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeWaitingIdle {
		t.Fatalf("%v", up)
	}
	end()
	u.upgrade(ctx)
	if v := installedVersion(h); v != "1.1.0" {
		t.Fatalf("not upgraded once idle: %s", v)
	}
	var d map[string]any
	defer func() {
		if t.Failed() {
			t.Logf("last: %v", d)
		}
	}()
	waitLong(t, "the note", func() bool {
		_, d = call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
		n, _ := d["upgrade_note"].(map[string]any)
		return n["kind"] == "upgraded" && n["to"] == "1.1.0" && d["upgrade"] == nil && d["auto_upgrade"] == true
	})
	st := h.store.(*extensionStore)
	st.mu.Lock()
	audited := slices.Contains(st.audit, "example-ui upgrade "+extensionUpgradeActor)
	st.mu.Unlock()
	if !audited {
		t.Fatalf("not audited: %v", st.audit)
	}
	if up := upgradeOf(t, mux, phone); up != nil {
		t.Fatalf("still listed: %v", up)
	}
	// Remembered across a restart of the hub.
	var s extensionUpgradeSettings
	if ok, _ := h.store.Setting(ctx, extensionUpgradesKey, &s); !ok || s.Last["example-ui"].Kind != "upgraded" {
		t.Fatalf("%+v", s)
	}
}

// One asking for more waits for the user (told once), who confirms it with the digest as for any upgrade.
func TestExtensionAutoUpgradeMorePermissions(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()

	setUIVersion(t, dir, "1.2.0")
	editManifest(t, dir, func(m map[string]any) {
		m["permissions"] = map[string]any{"launcher_keys": true, "network": []string{"api.example.com"}}
	})
	mk.publish(dir)
	_, _ = h.marketRead(ctx, mid)
	u.upgrade(ctx)
	u.upgrade(ctx)
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("upgraded to more permissions: %s", v)
	}
	up := upgradeOf(t, mux, phone)
	if up["state"] != upgradeNeedsConsent || !strings.Contains(fmt.Sprint(up["added_permissions"]), "api.example.com") {
		t.Fatalf("%v", up)
	}
	_, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
	n, _ := d["upgrade_note"].(map[string]any)
	if n["kind"] != upgradeNeedsConsent || n["detail"] != "要多一项权限：访问更多网络（api.example.com）" {
		t.Fatalf("%v", d["upgrade_note"])
	}
	var s extensionUpgradeSettings
	_, _ = h.store.Setting(ctx, extensionUpgradesKey, &s)
	if s.Notified["example-ui"] != "1.2.0" {
		t.Fatalf("%+v", s)
	}

	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mid))
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/upgrade", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	waitLong(t, "the note cleared", func() bool {
		u.upgrade(ctx)
		_, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
		return d["version"] == "1.2.0" && d["upgrade_note"] == nil && d["upgrade"] == nil
	})
}

func TestExtensionAutoUpgradeNewHubSecretNeedsConsent(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()
	old := h.extensions.reg.Snapshot("example-ui")
	downloads := func() int {
		mk.mu.Lock()
		defer mk.mu.Unlock()
		return mk.downloads
	}
	before := downloads()
	setUIVersion(t, dir, "1.1.0")
	editManifest(t, dir, func(m map[string]any) {
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API key", "required": true}}
	})
	mk.publish(dir)
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}

	// A hub-only secret leaves the digest unchanged but still needs consent before any download.
	up, _, entry, ok := u.candidateIn(old, i18n.En)
	if !ok || up.State != upgradeNeedsConsent || extension.Digest(entry.Parsed) != granted(old) {
		t.Fatalf("secret-only candidate: %+v, found %v", up, ok)
	}
	if len(up.AddedPermissions) != 1 || up.AddedPermissions[0].Key != "secrets" ||
		up.AddedPermissions[0].Title != "Use more secrets" || up.AddedPermissions[0].Detail != "API key" {
		t.Fatalf("secret disclosure: %+v", up.AddedPermissions)
	}
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeNeedsConsent {
		t.Fatalf("before the first upgrade check: %v", up)
	}
	u.upgrade(ctx)
	var saved extensionUpgradeSettings
	if ok, err := h.store.Setting(ctx, extensionUpgradesKey, &saved); err != nil || !ok {
		t.Fatalf("saved consent notice: %v, %v", ok, err)
	}
	note := saved.Last["example-ui"]
	if saved.Notified["example-ui"] != "1.1.0" || note.Kind != upgradeNeedsConsent ||
		note.To != "1.1.0" || !strings.Contains(note.Detail, "API key") {
		t.Fatalf("saved consent notice: %+v", saved)
	}

	// Repeated checks, a refreshed index and reloading the saved settings must not tell the user again.
	u.upgrade(ctx)
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}
	u.upgrade(ctx)
	reloaded := &extensionUpgrades{h: h, busy: h.extensionBusy}
	reloaded.load(ctx)
	reloaded.upgrade(ctx)
	if reloaded.settings.Last["example-ui"] != note || reloaded.settings.Notified["example-ui"] != "1.1.0" {
		t.Fatalf("consent notice changed after reload: %+v", reloaded.settings)
	}
	if got := downloads(); got != before || installedVersion(h) != "1.0.0" {
		t.Fatalf("automatic work before consent: downloads %d -> %d, version %s", before, got, installedVersion(h))
	}
	th := hubOf(h)
	th.mu.Lock()
	var pushes []map[string]any
	for _, push := range th.pushes {
		if push["collapse_id"] == "plugin_upgrade.example-ui" {
			pushes = append(pushes, push)
		}
	}
	th.mu.Unlock()
	if len(pushes) != 1 {
		t.Fatalf("consent pushes: %v", pushes)
	}
	for _, l := range i18n.Langs {
		payload := pushes[0]["payloads"].(map[string]any)[string(l)].(map[string]any)
		n := payload["extension_upgrade"].(map[string]any)
		if n["kind"] != upgradeNeedsConsent || n["version"] != "1.1.0" {
			t.Fatalf("%s push: %v", l, payload)
		}
	}
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeNeedsConsent {
		t.Fatalf("after repeated upgrade checks: %v", up)
	}

	code, pv := call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mid))
	if code != 200 || pv["more_permissions"] != true {
		t.Fatalf("manual preview: %d %v", code, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/upgrade", phone, fmt.Sprintf(`{"preview_id": %q}`, pv["preview_id"])); code == 200 {
		t.Fatalf("upgrade without consent succeeded: %v", out)
	}
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("upgraded without consent: %s", v)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/upgrade", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("confirmed upgrade: %d %v", code, out)
	}
	u.upgrade(ctx)
	_, detail := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
	if detail["version"] != "1.1.0" || detail["upgrade_note"] != nil || detail["upgrade"] != nil {
		t.Fatalf("after confirmation: %v", detail)
	}
}

// A version that adds a channel (whose messages reach the assistant as the owner's own) waits for the user even
// when its permissions are the same.
func TestExtensionAutoUpgradeNewChannel(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()

	setUIVersion(t, dir, "1.2.0")
	editManifest(t, dir, func(m map[string]any) {
		m["contributes"].(map[string]any)["channel"] = []any{map[string]any{"id": "examplechat", "name": "Example Chat", "kind": "im",
			"capabilities": map[string]any{"format": "plain", "max_text": 4000, "asks": "text", "push": true}}}
	})
	mk.publish(dir)
	_, _ = h.marketRead(ctx, mid)
	u.upgrade(ctx)
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("upgraded to a new channel without consent: %s", v)
	}
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeNeedsConsent || !strings.Contains(fmt.Sprint(up["added_permissions"]), "Example Chat") {
		t.Fatalf("%v", up)
	}
}

// Switched off, an extension is not upgraded, nor is the user told of a version asking for more: its page says auto-upgrade
// is paused and keeps its own switch, which disabling does not change. Switched on again, that switch applies as before.
func TestExtensionAutoUpgradePausedWhileDisabled(t *testing.T) {
	paused := i18n.T(i18n.Default, "hub-extension_autoupgrade.paused_disabled")
	for _, tc := range []struct {
		name string
		own  bool // its own switch, set before it is switched off
		more bool // the newer version asks for more permissions
		// Once it is on again and the hub has looked: the version installed and the newer one's state ("" when
		// none is left).
		version, state string
	}{
		{name: "switch on: upgraded once on again", own: true, version: "1.1.0"},
		{name: "switch off: stays off", own: false, version: "1.0.0", state: upgradeOff},
		{name: "asks for more: the user is told once on again", own: true, more: true, version: "1.0.0", state: upgradeNeedsConsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, phone, mk, dir, mid := upgradeHub(t)
			ctx := context.Background()
			u := h.extensionUpgrades()
			detail := func() map[string]any {
				t.Helper()
				code, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
				if code != 200 {
					t.Fatalf("detail: %d %v", code, d)
				}
				return d
			}
			stored := func() extensionUpgradeSettings {
				var s extensionUpgradeSettings
				if _, err := h.store.Setting(ctx, extensionUpgradesKey, &s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			switchOn := func(on bool) {
				t.Helper()
				if code, out := call(t, mux, "PUT", "/api/v1/plugins/example-ui/-/enabled", phone, fmt.Sprintf(`{"enabled": %t}`, on)); code != 200 {
					t.Fatalf("enabled %t: %d %v", on, code, out)
				}
			}

			if !tc.own {
				call(t, mux, "PUT", "/api/v1/plugins/example-ui/-/auto-upgrade", phone, `{"auto": false}`)
			}
			switchOn(false)
			waitLong(t, "example-ui switched off", func() bool { return detail()["status"] == extensionDisabled })
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

			// Off: not upgraded, nobody told, the page says why and keeps the switch.
			u.upgrade(ctx)
			u.upgrade(ctx)
			if v := installedVersion(h); v != "1.0.0" {
				t.Fatalf("upgraded while switched off: %s", v)
			}
			if up := upgradeOf(t, mux, phone); up["state"] != upgradeOff || up["detail"] != paused {
				t.Fatalf("while off: %v", up)
			}
			d := detail()
			if d["auto_upgrade"] != tc.own || d["auto_upgrade_paused"] != autoUpgradePausedDisabled || d["upgrade_note"] != nil {
				t.Fatalf("while off: auto_upgrade %v, paused %v, note %v", d["auto_upgrade"], d["auto_upgrade_paused"], d["upgrade_note"])
			}
			if s := stored(); slices.Contains(s.Off, "example-ui") == tc.own || s.Notified["example-ui"] != "" {
				t.Fatalf("switching it off changed the settings: %+v", s)
			}

			// On again: its own switch applies.
			switchOn(true)
			waitLong(t, "example-ui running", func() bool { return detail()["status"] == "running" })
			waitLong(t, "the look once on again", func() bool {
				u.upgrade(ctx)
				d = detail()
				up, _ := d["upgrade"].(map[string]any)
				state, _ := up["state"].(string)
				return d["version"] == tc.version && state == tc.state
			})
			if d["auto_upgrade"] != tc.own || d["auto_upgrade_paused"] != nil {
				t.Fatalf("on again: auto_upgrade %v, paused %v", d["auto_upgrade"], d["auto_upgrade_paused"])
			}
			if told := stored().Notified["example-ui"] == "1.1.0"; told != tc.more {
				t.Fatalf("told of the version asking for more: %v", told)
			}
		})
	}
}

// One that does not come up is rolled back, skipped from then on, and the user is told.
func TestExtensionAutoUpgradeRollsBack(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()

	setVersion(t, dir, "1.3.0")
	_ = os.WriteFile(filepath.Join(dir, "bin", "ui"), []byte("#!/bin/sh\nexit 3\n"), 0o755)
	mk.publish(dir)
	_, _ = h.marketRead(ctx, mid)
	u.upgrade(ctx)
	waitLong(t, "the rollback", func() bool {
		_, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
		n, _ := d["upgrade_note"].(map[string]any)
		return d["version"] == "1.0.0" && n["kind"] == "rolled_back" && n["to"] == "1.3.0"
	})
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeSkipped {
		t.Fatalf("%v", up)
	}
	u.upgrade(ctx)
	st := h.store.(*extensionStore)
	st.mu.Lock()
	defer st.mu.Unlock()
	upgrades := 0
	for _, a := range st.audit {
		if a == "example-ui upgrade "+extensionUpgradeActor {
			upgrades++
		}
	}
	if upgrades != 1 || !slices.Contains(st.audit, "example-ui rollback hub") {
		t.Fatalf("%v", st.audit)
	}
}

// One the user rolls back from by hand is skipped too, said to be the user's doing, its old note gone and nothing pushed;
// a newer version is offered again (#1826).
func TestExtensionManualRollbackIsSkipped(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()
	running := func(v string) func() bool {
		return func() bool {
			p := h.extensions.reg.Get("example-ui")
			return p.Record.Version == v && h.extensions.view(ctx, p).Status == "running"
		}
	}

	setUIVersion(t, dir, "1.1.0")
	mk.publish(dir)
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}
	u.upgrade(ctx)
	waitLong(t, "1.1.0 running", running("1.1.0"))
	waitLong(t, "the upgraded note", func() bool {
		_, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
		n, _ := d["upgrade_note"].(map[string]any)
		return n["kind"] == "upgraded" && n["to"] == "1.1.0"
	})
	auditLines := func() int {
		st := h.store.(*extensionStore)
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.audit)
	}

	code, d := call(t, mux, "POST", "/api/v1/extensions/example-ui/-/rollback", phone, "")
	if code != 200 || d["version"] != "1.0.0" {
		t.Fatalf("rollback: %d %v", code, d)
	}
	if n := d["upgrade_note"]; n != nil {
		t.Fatalf("a note of a version no longer installed: %v", n)
	}
	waitLong(t, "1.0.0 running", running("1.0.0"))
	up := upgradeOf(t, mux, phone)
	if up == nil || up["state"] != upgradeSkipped || up["skipped_by"] != skippedByUser || up["to"] != "1.1.0" || up["detail"] == "" {
		t.Fatalf("after the rollback: %v", up)
	}
	audits := auditLines()
	u.upgrade(ctx)
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("auto-upgrade put %s back", v)
	}
	if n := auditLines(); n != audits {
		t.Fatalf("something was done after the rollback (%d audit lines, then %d)", audits, n)
	}

	// A newer version than the one left goes in as usual.
	setUIVersion(t, dir, "1.2.0")
	mk.publish(dir)
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeAvailable || up["skipped_by"] != nil {
		t.Fatalf("1.2.0: %v", up)
	}
	u.upgrade(ctx)
	waitLong(t, "1.2.0 running", running("1.2.0"))
}

// Uninstalled, a plugin's upgrade state goes with it (its own switch stays): installed again, the version that rolled
// back is not skipped, the user is told again of one asking for more, and its page shows no old note.
func TestExtensionUpgradeForgetsRemoved(t *testing.T) {
	h, mux, phone, mk, dir, mid := upgradeHub(t)
	ctx := context.Background()
	u := h.extensionUpgrades()

	// 1.3.0 does not come up: rolled back, skipped, noted.
	setVersion(t, dir, "1.3.0")
	_ = os.WriteFile(filepath.Join(dir, "bin", "ui"), []byte("#!/bin/sh\nexit 3\n"), 0o755)
	mk.publish(dir)
	_, _ = h.marketRead(ctx, mid)
	u.upgrade(ctx)
	waitLong(t, "the rollback", func() bool {
		_, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
		n, _ := d["upgrade_note"].(map[string]any)
		return d["version"] == "1.0.0" && n["kind"] == "rolled_back"
	})
	if up := upgradeOf(t, mux, phone); up["state"] != upgradeSkipped {
		t.Fatalf("%v", up)
	}
	_ = u.change(ctx, func(s *extensionUpgradeSettings) {
		s.Notified = map[string]string{"example-ui": "1.3.0", "other": "2.0.0"}
		s.Off = []string{"example-ui"}
	})
	u.mu.Lock()
	u.failed = map[string]string{"example-ui": "1.3.0"}
	u.waiting = map[string]string{"example-ui": "busy"}
	u.mu.Unlock()

	if code, _ := call(t, mux, "DELETE", "/api/v1/plugins/example-ui/-/", phone, ""); code != 200 || h.extensions.reg.Get("example-ui") != nil {
		t.Fatalf("uninstall: %d", code)
	}
	var s extensionUpgradeSettings
	if _, err := h.store.Setting(ctx, extensionUpgradesKey, &s); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Skipped["example-ui"]; ok {
		t.Fatalf("skipped kept: %+v", s)
	}
	if _, ok := s.Notified["example-ui"]; ok || s.Notified["other"] != "2.0.0" {
		t.Fatalf("notified: %+v", s)
	}
	if _, ok := s.Last["example-ui"]; ok {
		t.Fatalf("note kept: %+v", s)
	}
	if !slices.Contains(s.Off, "example-ui") {
		t.Fatalf("the user's switch was dropped: %+v", s)
	}
	u.mu.Lock()
	_, failed := u.failed["example-ui"]
	_, waiting := u.waiting["example-ui"]
	u.mu.Unlock()
	if failed || waiting {
		t.Fatalf("in-memory state kept: failed %v waiting %v", failed, waiting)
	}

	// Installed again (a working 1.0.0), then 1.3.0 is in the market once more: it is a candidate again.
	again := uiPackage(t)
	mk.publish(again)
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mid))
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("reinstall: %d %v", code, out)
	}
	mk.publish(dir)
	if _, err := h.marketRead(ctx, mid); err != nil {
		t.Fatal(err)
	}
	_ = u.change(ctx, func(s *extensionUpgradeSettings) { s.Off = nil })
	up, _, _, ok := u.candidate(h.extensions.reg.Get("example-ui"))
	if !ok || up.To != "1.3.0" || up.State == upgradeSkipped || up.State == upgradeFailed || up.State == upgradeWaitingIdle {
		t.Fatalf("stale state on the reinstall: %+v (ok %v)", up, ok)
	}
	if _, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, ""); d["upgrade_note"] != nil {
		t.Fatalf("old note on the reinstall: %v", d["upgrade_note"])
	}
}

// Installed from a directory on the hub's machine: never upgraded by the hub, nor switched.
func TestExtensionAutoUpgradeOnlyMarketExtensions(t *testing.T) {
	h, phone, _ := marketDevices(t)
	mux := h.routes()
	ctx := context.Background()
	mk := newUpgradeMarket(t)
	h.extensions.markets.client = mk.srv.Client()
	dir := uiPackage(t)
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", h.cfg.Token, `{"source": "dir:`+dir+`"}`)
	call(t, mux, "POST", "/api/v1/plugins/-/install", h.cfg.Token, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	newer := uiPackage(t)
	setVersion(t, newer, "2.0.0")
	mk.publish(newer)
	call(t, mux, "POST", "/api/v1/plugins/-/markets", phone, fmt.Sprintf(`{"spec": %q}`, mk.srv.URL+"/plugins/"))
	h.extensionUpgrades().check(ctx)
	h.extensionUpgrades().upgrade(ctx)
	if v := installedVersion(h); v != "1.0.0" {
		t.Fatalf("a directory's plugin was upgraded from a market: %s", v)
	}
	if up := upgradeOf(t, mux, phone); up != nil {
		t.Fatalf("%v", up)
	}
	_, d := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", phone, "")
	if _, ok := d["auto_upgrade"]; ok {
		t.Fatalf("%v", d)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/plugins/example-ui/-/auto-upgrade", phone, `{"auto": true}`); code != 400 {
		t.Fatalf("switched a directory's plugin: %d", code)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/plugins/no-such-extension/-/auto-upgrade", phone, `{"auto": true}`); code != 404 {
		t.Fatalf("%d", code)
	}
}

// The markets are read when due (marketStale after the last read); one that fails is tried again after a backoff and
// logged once, when it stops answering and when it answers again.
func TestExtensionMarketReadsWhenDue(t *testing.T) {
	h, _, _, mk, _, mid := upgradeHub(t)
	ctx := context.Background()
	var logs bytes.Buffer
	var logMu sync.Mutex
	logTo(h, writerFunc(func(b []byte) (int, error) { logMu.Lock(); defer logMu.Unlock(); return logs.Write(b) }))
	u := h.extensionUpgrades()
	age := func(d time.Duration) {
		ms := h.marketsState()
		ms.mu.Lock()
		at := time.Now().Add(-d)
		ms.byID[mid].rec.FetchedAt = &at
		ms.mu.Unlock()
	}
	n := mk.readCount()
	u.check(ctx)
	if mk.readCount() != n {
		t.Fatal("read a fresh index")
	}
	age(marketStale + marketJitter)
	u.check(ctx)
	if mk.readCount() != n+1 {
		t.Fatal("did not read a stale index")
	}

	mk.mu.Lock()
	mk.down = true
	mk.mu.Unlock()
	age(marketStale + marketJitter)
	u.check(ctx)
	u.check(ctx)
	age(marketBackoff + time.Minute) // a second failure doubles the wait
	u.check(ctx)
	age(marketBackoff + time.Minute)
	u.check(ctx)
	logMu.Lock()
	failedLines := strings.Count(logs.String(), "plugin market "+mid+":")
	logMu.Unlock()
	if failedLines != 1 {
		t.Fatalf("logged %d times:\n%s", failedLines, logs.String())
	}
	if u.fails[mid] != 2 {
		t.Fatalf("tried %d times", u.fails[mid])
	}
	mk.mu.Lock()
	mk.down = false
	mk.mu.Unlock()
	age(2*marketBackoff + time.Minute)
	u.check(ctx)
	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(logs.String(), "answering again") || u.fails[mid] != 0 {
		t.Fatalf("%s %d", logs.String(), u.fails[mid])
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

func TestPrintUpgrades(t *testing.T) {
	var b strings.Builder
	printUpgrades(&b, upgradesView{Auto: true, Upgrades: []ExtensionUpgrade{
		{ID: "example-a", From: "1.0.0", To: "1.1.0", State: upgradeWaitingIdle, Detail: "扩展正在用"},
		{ID: "example-b", Market: "m1", From: "0.6.0", To: "0.7.0", State: upgradeNeedsConsent,
			AddedPermissions: []extension.PermissionLine{{Title: "访问更多网络", Detail: "api.example.com"}}},
		{ID: "example-c", From: "2.0.0", To: "2.1.0", State: upgradeSkipped},
	}})
	out := b.String()
	for _, want := range []string{"扩展自动升级：开", "可升级：", "example-a", "等空闲：扩展正在用", "等你确认：",
		"要多一项权限：访问更多网络（api.example.com）", "market:m1/example-b", "已跳过：", "回滚过"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
	b.Reset()
	printUpgrades(&b, upgradesView{})
	if !strings.Contains(b.String(), "都是最新的") {
		t.Fatal(b.String())
	}
}

// Changes from several goroutines at once: each saves a copy made under the lock (go test -race), and the settings
// saved last are the ones in memory.
func TestExtensionUpgradesConcurrentChanges(t *testing.T) {
	h := newTestManager(t)
	st := newKeepStore()
	h.store = st
	u := &extensionUpgrades{h: h, loaded: true}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				id := fmt.Sprintf("p%d-%d", i, j%5)
				_ = u.change(ctx, func(s *extensionUpgradeSettings) {
					s.note(id, ExtensionUpgradeNote{Kind: upgradeFailed, To: fmt.Sprint(j)})
					if s.Notified == nil {
						s.Notified = map[string]string{}
					}
					s.Notified[id] = fmt.Sprint(j)
					if s.Skipped == nil {
						s.Skipped = map[string]string{}
					}
					s.Skipped[id] = fmt.Sprint(j)
					if j%2 == 0 {
						s.Off = append(s.Off, id)
					} else {
						s.Off = slices.DeleteFunc(s.Off, func(x string) bool { return x == id })
					}
				})
			}
		})
	}
	wg.Wait()
	var saved extensionUpgradeSettings
	if _, err := st.Setting(ctx, extensionUpgradesKey, &saved); err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	want, _ := json.Marshal(u.settings)
	u.mu.Unlock()
	if got, _ := json.Marshal(saved); string(got) != string(want) {
		t.Fatalf("saved %s\nwant %s", got, want)
	}
}
