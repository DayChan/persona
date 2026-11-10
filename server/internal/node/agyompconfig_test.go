package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAgyLegacyPermissionModeWritesNativeAuto(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", home)
	path := filepath.Join(home, "settings.json")
	initial := `{"agentMode":"bypassPermissions","toolPermission":"always-proceed","custom":{"keep":true}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home}
	if err := n.setAgySetting("agentMode", "bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["agentMode"] != "auto" || got["toolPermission"] != "always-proceed" || got["custom"].(map[string]any)["keep"] != true {
		t.Fatalf("unexpected native settings: %s", data)
	}
	settings, err := n.getAgySettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range settings {
		if setting.Key == "agentMode" && (slices.Contains(setting.Options, "bypassPermissions") || setting.Value != "auto") {
			t.Fatalf("unsupported native mode offered: %+v", setting)
		}
	}
}

func TestAgySettingsReadWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	n := &Node{home: home}

	list, err := n.getAgySettings()
	if err != nil {
		t.Fatalf("getAgySettings empty: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected agy settings list")
	}

	if err := n.setAgySetting("agentMode", "auto"); err != nil {
		t.Fatalf("setAgySetting agentMode: %v", err)
	}
	if err := n.setAgySetting("toolPermission", "always-proceed"); err != nil {
		t.Fatalf("setAgySetting toolPermission: %v", err)
	}
	if err := n.setAgySetting("showFeedbackSurvey", false); err != nil {
		t.Fatalf("setAgySetting showFeedbackSurvey: %v", err)
	}
	if err := n.setAgySetting("agentMode", "invalid-mode"); err == nil {
		t.Fatal("expected invalid enum error for agentMode")
	}

	list, err = n.getAgySettings()
	if err != nil {
		t.Fatalf("getAgySettings after set: %v", err)
	}
	got := map[string]any{}
	for _, s := range list {
		got[s.Key] = s.Value
	}
	if got["agentMode"] != "auto" || got["toolPermission"] != "always-proceed" || got["showFeedbackSurvey"] != false {
		t.Fatalf("unexpected agy settings values: %#v", got)
	}

	// Resetting a key to nil removes it while keeping others.
	if err := n.setAgySetting("agentMode", nil); err != nil {
		t.Fatalf("setAgySetting reset: %v", err)
	}
	list, err = n.getAgySettings()
	if err != nil {
		t.Fatalf("getAgySettings after reset: %v", err)
	}
	for _, s := range list {
		if s.Key == "agentMode" && s.Value != nil {
			t.Fatalf("expected agentMode nil after reset, got %#v", s.Value)
		}
	}
}

func TestOmpSettingsReadWritePreservesUnrelatedYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".omp", "agent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := `modelRoles:
  default: google-antigravity/gemini-4-argon:high
  fast: gemini-3.8-flash
symbolPreset: unicode
theme:
  dark: titanium
defaultThinkingLevel: auto
computer:
  enabled: true
`
	cfgPath := filepath.Join(cfgDir, "config.yml")
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home}
	list, err := n.getOmpSettings()
	if err != nil {
		t.Fatalf("getOmpSettings: %v", err)
	}
	got := map[string]any{}
	for _, s := range list {
		got[s.Key] = s.Value
	}
	if got["modelRoles.default"] != "google-antigravity/gemini-4-argon:high" {
		t.Fatalf("expected modelRoles.default, got %#v", got["modelRoles.default"])
	}
	if got["defaultThinkingLevel"] != "auto" {
		t.Fatalf("expected defaultThinkingLevel=auto, got %#v", got["defaultThinkingLevel"])
	}
	if got["computer.enabled"] != true {
		t.Fatalf("expected computer.enabled=true, got %#v", got["computer.enabled"])
	}

	if err := n.setOmpSetting("defaultThinkingLevel", "high"); err != nil {
		t.Fatalf("setOmpSetting defaultThinkingLevel: %v", err)
	}
	if err := n.setOmpSetting("modelRoles.default", "claude-opus-5-5"); err != nil {
		t.Fatalf("setOmpSetting modelRoles.default: %v", err)
	}
	if err := n.setOmpSetting("computer.enabled", false); err != nil {
		t.Fatalf("setOmpSetting computer.enabled: %v", err)
	}
	if err := n.setOmpSetting("autolearn.enabled", true); err != nil {
		t.Fatalf("setOmpSetting autolearn.enabled: %v", err)
	}

	list, err = n.getOmpSettings()
	if err != nil {
		t.Fatalf("getOmpSettings after update: %v", err)
	}
	got = map[string]any{}
	for _, s := range list {
		got[s.Key] = s.Value
	}
	if got["defaultThinkingLevel"] != "high" || got["modelRoles.default"] != "claude-opus-5-5" || got["computer.enabled"] != false || got["autolearn.enabled"] != true {
		t.Fatalf("unexpected updated omp settings: %#v", got)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, keep := range []string{"fast: gemini-3.8-flash", "symbolPreset: unicode", "dark: titanium"} {
		if !containsLine(text, keep) {
			t.Fatalf("expected %q preserved in config.yml:\n%s", keep, text)
		}
	}
}

func containsLine(s, sub string) bool {
	return len(s) >= len(sub) && (filepath.Base(s) != "" && stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
