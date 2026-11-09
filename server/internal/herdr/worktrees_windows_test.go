package herdr

import (
	"path/filepath"
	"testing"
)

// On Windows herdr's config directory is %APPDATA%\herdr, unless $XDG_CONFIG_HOME is set.
func TestWorktreesDirReadsAppDataConfigWindows(t *testing.T) {
	noHerdrConfigEnv(t)
	home, appData, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	writeWorktreesConfig(t, filepath.Join(appData, "herdr", "config.toml"), "~/appdata-wt")
	writeWorktreesConfig(t, filepath.Join(xdg, "herdr", "config.toml"), "~/xdg-wt")
	t.Setenv("APPDATA", appData)
	if got, want := WorktreesDir(home), filepath.Join(home, "appdata-wt"); got != want {
		t.Errorf("with APPDATA: %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, want := WorktreesDir(home), filepath.Join(home, "xdg-wt"); got != want {
		t.Errorf("with XDG_CONFIG_HOME: %q, want %q", got, want)
	}
}
