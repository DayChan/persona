package extmgr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector/github"
	"github.com/nwverse/persona/server/internal/extension"
)

// installGitHubExtension is one install of a GitHub extension whose settings have proxy and host, installed at `at`
// with config: a new registry each time, as an uninstall deletes the row and its settings.
func installGitHubExtension(t *testing.T, h *Manager, at time.Time, config string) {
	t.Helper()
	m := &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: "github", Name: "GitHub", Version: "1.0.0",
		Engines:       extension.Engines{ExtensionProtocol: extension.Protocol},
		Configuration: json.RawMessage(`{"type":"object","properties":{"proxy":{"type":"string"},"host":{"type":"string"}}}`)}
	reg := extension.NewRegistry(t.TempDir(), nil, nil, 0)
	if err := reg.AddBuiltin(m, "", true); err != nil {
		t.Fatal(err)
	}
	p := reg.Get("github")
	p.Record.Source, p.Record.InstalledAt, p.Record.Config = extension.SourceInstalled, at, json.RawMessage(config)
	h.extensions = newExtensions(t.TempDir(), nil, h.logf)
	h.extensions.reg = reg
}

func githubExtensionConfig(t *testing.T, h *Manager) map[string]any {
	t.Helper()
	cfg := map[string]any{}
	_ = json.Unmarshal(h.extensions.reg.Get("github").Record.Config, &cfg)
	return cfg
}

// manager.json's github.proxy and github.auth.host move into the GitHub extension's settings once per install of it:
// a reinstall (its settings went with the uninstall) gets them again, a key the user cleared in the same install stays
// cleared, and a mark from before installs were told apart counts only while the extension holds one of the keys.
func TestGitHubConfigMovesIntoEachInstall(t *testing.T) {
	ctx := context.Background()
	moved := map[string]any{"proxy": "http://proxy.example:3128", "host": "github.example.com"}
	setup := func(t *testing.T) *Manager {
		h := newTestManager(t)
		h.store = newBotStore()
		h.cfg.GitHub = &github.Config{Proxy: "http://proxy.example:3128", Auth: github.AuthConfig{Host: "github.example.com"}}
		return h
	}
	first := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	same := func(got, want map[string]any) bool {
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		return string(a) == string(b)
	}

	t.Run("reinstalled", func(t *testing.T) {
		h := setup(t)
		installGitHubExtension(t, h, first, `{}`)
		h.migrateGitHubConfig(ctx, "github")
		if cfg := githubExtensionConfig(t, h); !same(cfg, moved) {
			t.Fatalf("first install: %v", cfg)
		}
		// The user clears the proxy in the extension's settings: the next start leaves it so.
		if err := h.extensions.reg.SetConfig(ctx, "github", json.RawMessage(`{"host":"github.example.com"}`), "user"); err != nil {
			t.Fatal(err)
		}
		h.migrateGitHubConfig(ctx, "github")
		if cfg := githubExtensionConfig(t, h); cfg["proxy"] != nil {
			t.Fatalf("a cleared key was put back: %v", cfg)
		}
		// Uninstalled and installed again: a new install, its settings empty.
		installGitHubExtension(t, h, first.Add(time.Hour), `{}`)
		h.migrateGitHubConfig(ctx, "github")
		if cfg := githubExtensionConfig(t, h); !same(cfg, moved) {
			t.Fatalf("the reinstall did not get them: %v", cfg)
		}
	})
	t.Run("marked by an older hub", func(t *testing.T) {
		h := setup(t)
		if err := h.store.PutSetting(ctx, githubMigratedKey, true); err != nil {
			t.Fatal(err)
		}
		installGitHubExtension(t, h, first, `{"proxy":"http://other.example:8080"}`)
		h.migrateGitHubConfig(ctx, "github")
		if cfg := githubExtensionConfig(t, h); !same(cfg, map[string]any{"proxy": "http://other.example:8080"}) {
			t.Fatalf("an install the old mark covers was changed: %v", cfg)
		}
		installGitHubExtension(t, h, first.Add(time.Hour), `{}`)
		if err := h.store.PutSetting(ctx, githubMigratedKey, true); err != nil {
			t.Fatal(err)
		}
		h.migrateGitHubConfig(ctx, "github")
		if cfg := githubExtensionConfig(t, h); !same(cfg, moved) {
			t.Fatalf("an install holding none of the keys did not get them: %v", cfg)
		}
	})
}
