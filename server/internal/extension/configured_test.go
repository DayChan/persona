package extension

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension/egress"
	"github.com/nwverse/persona/server/internal/i18n"
)

// configuredManifest is the example with base_url (format uri) reachable from its settings, api_key shown by its end
// and the settings checked by the program (#5906).
func configuredManifest(t *testing.T, network ...any) *Manifest {
	t.Helper()
	m, err := Parse(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": network}
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key", "hint": "last4"}}
		m["settings_check"] = true
		delete(m["configuration"].(map[string]any)["properties"].(map[string]any)["base_url"].(map[string]any), "default")
	}))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A config: entry names a string field of format uri with no default; anything else, and a hint the hub does not know,
// fails.
func TestConfiguredNetworkManifest(t *testing.T) {
	m := configuredManifest(t, "notes.example.com", "config:base_url", "config:base_url:http")
	if cs := m.Permissions.ConfiguredNetworks(); len(cs) != 2 || cs[0] != (ConfiguredNetwork{Field: "base_url"}) || !cs[1].HTTP {
		t.Fatalf("%+v", cs)
	}
	if !m.SettingsCheck || m.Secrets[0].Hint != HintLast4 {
		t.Fatalf("%+v", m)
	}
	for _, bad := range []struct {
		name   string
		change func(m map[string]any)
	}{
		{"no such field", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:nope"}} }},
		{"not a uri", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:folders"}} }},
		{"two colons", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:base_url:ftp"}} }},
		{"a default", func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"config:base_url"}} }},
		{"unknown hint", func(m map[string]any) {
			m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key", "hint": "all"}}
		}},
	} {
		if _, err := Parse(with(t, bad.change)); err == nil {
			t.Errorf("%s: parsed", bad.name)
		}
	}
}

// The address a config: entry lets through is the field's value now: https by default (443), http only when the
// manifest says :http, and never one with user info, another scheme or no host.
func TestConfiguredTarget(t *testing.T) {
	https, http := ConfiguredNetwork{Field: "base_url"}, ConfiguredNetwork{Field: "base_url", HTTP: true}
	cfg := func(v string) json.RawMessage { b, _ := json.Marshal(map[string]string{"base_url": v}); return b }
	for _, c := range []struct {
		c    ConfiguredNetwork
		url  string
		want egress.Target
	}{
		{https, "https://Gateway.Example.com/v1", egress.Target{Host: "gateway.example.com", Port: "443"}},
		{https, "https://gateway.example.com:8443", egress.Target{Host: "gateway.example.com", Port: "8443"}},
		{https, "http://gateway.example.com", egress.Target{}},
		{https, "http://192.0.2.10:8080", egress.Target{}},
		{https, "http://127.0.0.1:8080", egress.Target{Host: "127.0.0.1", Port: "8080", HTTP: true}},
		{https, "http://localhost:8080/v1", egress.Target{Host: "localhost", Port: "8080", HTTP: true}},
		{https, "http://[::1]:8080", egress.Target{Host: "::1", Port: "8080", HTTP: true}},
		{https, "http://127.0.0.1", egress.Target{Host: "127.0.0.1", Port: "80", HTTP: true}},
		{https, "http://localhost", egress.Target{Host: "localhost", Port: "80", HTTP: true}},
		{https, "http://[::1]", egress.Target{Host: "::1", Port: "80", HTTP: true}},
		{http, "http://gateway.example.com", egress.Target{Host: "gateway.example.com", Port: "80", HTTP: true}},
		{http, "https://gateway.example.com", egress.Target{Host: "gateway.example.com", Port: "443"}},
		{https, "https://me:pw@gateway.example.com", egress.Target{}},
		{https, "ftp://gateway.example.com", egress.Target{}},
		{https, "gateway.example.com", egress.Target{}},
		{https, "", egress.Target{}},
		{https, "https://[2001:db8::1]:9443", egress.Target{Host: "2001:db8::1", Port: "9443"}},
	} {
		if got, _ := ConfiguredTarget(c.c, cfg(c.url)); got != c.want {
			t.Errorf("%+v %q: %+v, want %+v", c.c, c.url, got, c.want)
		}
	}
}

func TestSecretHint(t *testing.T) {
	last4 := SecretSpec{Name: "k", Hint: HintLast4}
	if got := SecretHint(last4, "ag-demo-key-0000-7f3a"); got != "7f3a" {
		t.Fatal(got)
	}
	if got := SecretHint(last4, "short-key"); got != "" {
		t.Fatalf("a short key shows nothing of itself: %q", got)
	}
	if got := SecretHint(SecretSpec{Name: "k"}, "ag-demo-key-0000-7f3a"); got != "" {
		t.Fatalf("a secret that did not ask for it: %q", got)
	}
}

// The consent names the address in the settings now (未填 before there is one) and says http when it is allowed; an
// upgrade that adds a config: entry adds a line to agree to.
func TestExplainConfiguredAddress(t *testing.T) {
	m := configuredManifest(t, "notes.example.com", "config:base_url")
	detail := func(lines []PermissionLine) string {
		for _, l := range lines {
			if l.Key == "network_config" {
				return l.Detail
			}
		}
		return ""
	}
	if d := detail(ExplainIn(i18n.ZhHans, m)); !strings.Contains(d, "当前：未填") || !strings.Contains(d, "只用 https") {
		t.Fatalf("before install: %q", d)
	}
	cfg := json.RawMessage(`{"base_url":"https://gateway.example.com"}`)
	if d := detail(ExplainWith(i18n.ZhHans, m, cfg)); !strings.Contains(d, "当前：gateway.example.com:443") {
		t.Fatalf("installed: %q", d)
	}
	for _, l := range ExplainIn(i18n.ZhHans, m) {
		if l.Key == "network" && strings.Contains(l.Detail, "config:") {
			t.Fatalf("the host line lists a field: %q", l.Detail)
		}
	}
	if d := detail(ExplainIn(i18n.ZhHans, configuredManifest(t, "config:base_url:http"))); !strings.Contains(d, "http") || strings.Contains(d, "只用 https") {
		t.Fatalf("http allowed: %q", d)
	}
	old := configuredManifest(t, "notes.example.com")
	if d := detail(AddedIn(i18n.ZhHans, old, m)); d == "" {
		t.Fatal("an upgrade adding config:base_url asks again")
	}
	if d := detail(AddedIn(i18n.ZhHans, m, m)); d != "" {
		t.Fatalf("nothing added: %q", d)
	}
}

// settings_check: a change of a secret or the settings is checked by the extension with the values as they would be
// after it; a refusal saves nothing (no secret row, no audit line); one without settings_check is never asked.
func TestSettingsCheckBeforeSaving(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": []any{"config:base_url"}}
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key", "hint": "last4"}}
		m["settings_check"] = true
		delete(m["configuration"].(map[string]any)["properties"].(map[string]any)["base_url"].(map[string]any), "default")
	})
	pv, err := r.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	var asked []string
	refuse := errors.New("网关不认这把 Key")
	r.SettingsCheck = func(_ context.Context, p *Extension, config json.RawMessage, secrets map[string]string) error {
		asked = append(asked, string(config)+" "+secrets["api_key"])
		if strings.Contains(secrets["api_key"], "bad") || strings.Contains(string(config), "wrong") {
			return refuse
		}
		return nil
	}
	audits := len(store.audit)
	if err := r.SetSecret(ctx, "example-notes", "api_key", "bad-key-1234567890", "app"); !errors.Is(err, refuse) {
		t.Fatalf("refused key: %v", err)
	}
	if set := r.SecretsSet(ctx, r.Get("example-notes")); set["api_key"] || len(store.audit) != audits {
		t.Fatalf("a refused key was kept (%v) or audited (%v)", set, store.audit[audits:])
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "good-key-1234567890", "app"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://wrong.example.com"}`), "app"); !errors.Is(err, refuse) {
		t.Fatalf("refused settings: %v", err)
	}
	if strings.Contains(string(r.Config(r.Get("example-notes"))), "wrong") {
		t.Fatal("refused settings were saved")
	}
	if err := r.SetConfig(ctx, "example-notes", json.RawMessage(`{"base_url":"https://gateway.example.com"}`), "app"); err != nil {
		t.Fatal(err)
	}
	// The settings were checked with the key kept, the key with the settings kept.
	if len(asked) != 4 || !strings.HasSuffix(asked[3], " good-key-1234567890") || !strings.Contains(asked[3], "gateway.example.com") {
		t.Fatalf("asked %q", asked)
	}
	for _, a := range store.audit {
		if strings.Contains(a, "key-1234567890") {
			t.Fatal("a value in the audit:", a)
		}
	}

	// An extension without settings_check is not asked.
	asked = nil
	r2, _, _ := newTestRegistry(t)
	pkg2 := t.TempDir()
	writePackage(t, pkg2, nil)
	pv, _ = r2.Preview(ctx, "dir:"+pkg2, "")
	if _, err := r2.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	r2.SettingsCheck = r.SettingsCheck
	_ = r2.SetSecret(ctx, "example-notes", "api_key", "bad-key-1234567890", "app")
	if len(asked) != 0 {
		t.Fatalf("asked an extension without settings_check: %q", asked)
	}
}

// A change saved while another was being checked is refused (settings_moved): it was checked against the settings
// before it.
func TestSettingsCheckSettingsMoved(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["secrets"] = []any{map[string]any{"name": "api_key", "title": "API Key"}}
		m["settings_check"] = true
	})
	pv, _ := r.Preview(ctx, "dir:"+pkg, "")
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	moved := false
	r.SettingsCheck = func(ctx context.Context, p *Extension, config json.RawMessage, secrets map[string]string) error {
		if !moved && secrets["api_key"] == "key-checked-slowly" {
			moved = true
			r.mu.Lock()
			p.Record.Config = json.RawMessage(`{"base_url":"https://other.example.com"}`) // saved by someone else meanwhile
			r.mu.Unlock()
		}
		return nil
	}
	if err := r.SetSecret(ctx, "example-notes", "api_key", "key-checked-slowly", "app"); !errors.Is(err, ErrSettingsMoved) {
		t.Fatalf("%v", err)
	}
}
