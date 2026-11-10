package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestClaudeSettingsKeepOtherKeys(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	n := &Node{home: home}
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	path := filepath.Join(home, ".claude", "settings.json")
	_ = os.WriteFile(path, []byte(`{"hooks":{"Stop":[]},"language":"中文"}`), 0o600)

	if err := n.setClaudeSetting("awaySummaryEnabled", false); err != nil {
		t.Fatal(err)
	}
	if err := n.setClaudeSetting("effortLevel", "nope"); err == nil {
		t.Fatal("invalid enum accepted")
	}
	if err := n.setClaudeSetting("hooks", map[string]any{}); err == nil {
		t.Fatal("non-editable key accepted")
	}
	if err := n.setClaudeSetting("language", nil); err != nil {
		t.Fatal(err)
	}
	if err := n.setClaudeSetting("effortLevel", "xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := n.setClaudeSetting("effortLevel", "auto"); err != nil { // auto is "not set": Claude Code ignores the value
		t.Fatal(err)
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["effortLevel"]; ok {
		t.Fatalf("auto written as %v", raw["effortLevel"])
	}
	if raw["awaySummaryEnabled"] != false || raw["hooks"] == nil || raw["language"] != nil {
		t.Fatalf("settings = %v", raw)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %o", st.Mode().Perm())
	}
	list, err := n.getClaudeSettings(i18n.En)
	if err != nil || list[0].Key != "awaySummaryEnabled" || list[0].Value != false {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestClaudeWorkflowsSetting(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n := &Node{home: t.TempDir()}
	read := func() ClaudeSetting {
		t.Helper()
		list, err := n.getClaudeSettings(i18n.En)
		if err != nil {
			t.Fatal(err)
		}
		for _, setting := range list {
			if setting.Key == "enableWorkflows" {
				return setting
			}
		}
		t.Fatal("workflows setting is missing")
		return ClaudeSetting{}
	}
	setting := read()
	// Claude's schema leaves the default to the account's plan, not a fixed bool.
	if setting.Type != "bool" || setting.Default != nil || setting.Value != nil {
		t.Fatalf("unset setting = %+v", setting)
	}
	if err := n.setClaudeSetting("enableWorkflows", "true"); err == nil {
		t.Fatal("non-boolean workflows value accepted")
	}
	for _, value := range []any{true, false, nil} {
		if err := n.setClaudeSetting("enableWorkflows", value); err != nil {
			t.Fatal(err)
		}
		if got := read().Value; got != value {
			t.Fatalf("value = %v, want %v", got, value)
		}
	}
}

func TestClaudeSettingsEffortLevels(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	n := &Node{home: t.TempDir()}
	read := func(lang i18n.Lang) ClaudeSetting {
		t.Helper()
		settings, err := n.getClaudeSettings(lang)
		if err != nil {
			t.Fatal(err)
		}
		for _, setting := range settings {
			if setting.Key == "effortLevel" {
				return setting
			}
		}
		t.Fatal("effort setting is missing")
		return ClaudeSetting{}
	}
	setting := read(i18n.En)
	if !slices.Equal(setting.Options, []string{"auto", "low", "medium", "high", "xhigh"}) || setting.Default != "auto" || setting.Value != nil {
		t.Fatalf("unset effort = %+v", setting)
	}
	if !slices.Contains(effortLevels, "max") {
		t.Fatal("session effort must still support max")
	}
	for _, level := range []string{"low", "medium", "high", "xhigh"} {
		if err := n.setClaudeSetting("effortLevel", level); err != nil {
			t.Fatal(err)
		}
		if got := read(i18n.En); got.Value != level || got.IgnoredValue != "" {
			t.Fatalf("saved effort = %+v, want %s", got, level)
		}
	}
	path := n.claudeSettingsPath()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.setClaudeSetting("effortLevel", "max"); err == nil {
		t.Fatal("settings.json must reject max")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
		t.Fatalf("rejected max changed settings: %s, %v", after, err)
	}

	legacy := []byte(`{"effortLevel":"max","hooks":{"Stop":[]}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja, ""} {
		got := read(lang)
		if got.Value != nil || got.Default != "auto" || got.IgnoredValue != "max" {
			t.Fatalf("legacy max appears applied: %+v", got)
		}
		if got.Detail != i18n.T(lang, "claude-settings.effort_max_ignored") || !strings.Contains(got.Detail, "/effort max") {
			t.Fatalf("missing localized session guidance for %s: %q", lang, got.Detail)
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(legacy) {
		t.Fatalf("reading legacy max changed settings: %s, %v", after, err)
	}
	for _, reset := range []any{"auto", nil} {
		if err := os.WriteFile(path, legacy, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := n.setClaudeSetting("effortLevel", reset); err != nil {
			t.Fatal(err)
		}
		raw, err := n.readClaudeSettings()
		if err != nil || raw["effortLevel"] != nil || raw["hooks"] == nil {
			t.Fatalf("reset = %v, %v", raw, err)
		}
		if got := read(i18n.En); got.Value != nil || got.IgnoredValue != "" || got.Detail != i18n.T(i18n.En, "claude-settings.effort_detail") {
			t.Fatalf("reset retains ignored warning: %+v", got)
		}
	}
}

// permissions.defaultMode is one key inside settings.json's permissions object: set and reset without touching the
// rest of it (#3026).
func TestClaudeSettingsDefaultMode(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	n := &Node{home: home}
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	path := filepath.Join(home, ".claude", "settings.json")
	_ = os.WriteFile(path, []byte(`{"permissions":{"allow":["Bash(ls)"]},"language":"中文"}`), 0o600)
	read := func() ClaudeSetting {
		t.Helper()
		list, err := n.getClaudeSettings(i18n.En)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if s.Key == "permissions.defaultMode" {
				return s
			}
		}
		t.Fatal("default mode setting is missing")
		return ClaudeSetting{}
	}
	got := read()
	if got.Value != nil || got.Default != "default" || !slices.Equal(got.Options, permissionModes) || got.Title != i18n.T(i18n.En, "claude-settings.default_mode_title") {
		t.Fatalf("unset default mode = %+v", got)
	}
	if err := n.setClaudeSetting("permissions.defaultMode", "dontAsk"); err == nil {
		t.Fatal("a mode Persona does not know was accepted")
	}
	if err := n.setClaudeSetting("permissions.defaultMode", "auto"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Value != "auto" {
		t.Fatalf("value = %v, want auto", got.Value)
	}
	raw, _ := n.readClaudeSettings()
	perms, _ := raw["permissions"].(map[string]any)
	if perms["defaultMode"] != "auto" || len(perms["allow"].([]any)) != 1 || raw["language"] != "中文" || raw["permissions.defaultMode"] != nil {
		t.Fatalf("settings = %v", raw)
	}
	if err := n.setClaudeSetting("permissions.defaultMode", nil); err != nil {
		t.Fatal(err)
	}
	raw, _ = n.readClaudeSettings()
	perms, _ = raw["permissions"].(map[string]any)
	if _, set := perms["defaultMode"]; set || perms["allow"] == nil {
		t.Fatalf("reset = %v", raw)
	}

	// A settings.json without the object gets one; one whose permissions is not an object is left alone.
	_ = os.WriteFile(path, []byte(`{}`), 0o600)
	if err := n.setClaudeSetting("permissions.defaultMode", "acceptEdits"); err != nil || read().Value != "acceptEdits" {
		t.Fatalf("set on an empty file: %v, %+v", err, read())
	}
	_ = os.WriteFile(path, []byte(`{"permissions":"odd"}`), 0o600)
	if err := n.setClaudeSetting("permissions.defaultMode", "plan"); err == nil {
		t.Fatal("overwrote a permissions value that is not an object")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"permissions":"odd"}` {
		t.Fatalf("file changed: %s", b)
	}
}
