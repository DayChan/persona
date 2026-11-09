package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreesDirIn(t *testing.T) {
	for in, want := range map[string]string{
		"":                                    "",
		"[worktrees]\ndirectory = \"~/wt\"\n": "~/wt",
		"[ui]\ndirectory = \"x\"\n[worktrees] # c\ndirectory = '/srv/wt' # c\n": "/srv/wt",
		"directory = \"top\"\n[worktrees]\n":                                    "",
		"[worktrees]\n# directory = \"no\"\n":                                   "",
		"[worktrees]\ndirectory = \"a\\\\b\\\"c\"\n":                            `a\b"c`,
		"[worktrees]\ndirectory = unquoted\n":                                   "",
		"[worktrees]\ndirectory = \"open\n":                                     "",
		"[other]\n[worktrees]\nx = 1\n[last]\ndirectory = \"z\"":                "",
	} {
		if got := worktreesDirIn(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// noHerdrConfigEnv keeps the machine's own herdr config out of a test: no $HERDR_CONFIG_PATH, $XDG_CONFIG_HOME or
// %APPDATA%, so herdr's config file is the one under the home the test passes.
func noHerdrConfigEnv(t *testing.T) {
	t.Setenv("HERDR_CONFIG_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
}

// writeWorktreesConfig writes a herdr config file at path that puts worktrees under dir.
func writeWorktreesConfig(t *testing.T, path, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[worktrees]\ndirectory = '"+dir+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreesDirDefaultsAndExpands(t *testing.T) {
	noHerdrConfigEnv(t)
	home := t.TempDir()
	if got, want := WorktreesDir(home), filepath.Join(home, ".herdr", "worktrees"); got != want {
		t.Errorf("default: %q, want %q", got, want)
	}
	if got := expandHome(home, "~/Projects/wt"); got != filepath.Join(home, "Projects", "wt") {
		t.Errorf("tilde: %q", got)
	}
	if got := expandHome(home, "rel/wt"); got != filepath.Join(home, "rel", "wt") {
		t.Errorf("relative: %q", got)
	}
	if got := expandHome(home, "/srv/../srv/wt"); got != "/srv/wt" {
		t.Errorf("absolute: %q", got)
	}
}

// The worktrees directory comes from the config file herdr itself reads: $HERDR_CONFIG_PATH, else
// $XDG_CONFIG_HOME/herdr/config.toml, else the one in the home's .config/herdr.
func TestWorktreesDirReadsHerdrsConfigFile(t *testing.T) {
	noHerdrConfigEnv(t)
	home, xdg := t.TempDir(), t.TempDir()
	explicit := filepath.Join(t.TempDir(), "custom.toml")
	writeWorktreesConfig(t, filepath.Join(home, ".config", "herdr", "config.toml"), "~/home-wt")
	writeWorktreesConfig(t, filepath.Join(xdg, "herdr", "config.toml"), "~/xdg-wt")
	writeWorktreesConfig(t, explicit, "~/explicit-wt")

	if got, want := WorktreesDir(home), filepath.Join(home, "home-wt"); got != want {
		t.Errorf("no XDG_CONFIG_HOME: %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, want := WorktreesDir(home), filepath.Join(home, "xdg-wt"); got != want {
		t.Errorf("with XDG_CONFIG_HOME: %q, want %q", got, want)
	}
	t.Setenv("HERDR_CONFIG_PATH", explicit)
	if got, want := WorktreesDir(home), filepath.Join(home, "explicit-wt"); got != want {
		t.Errorf("with HERDR_CONFIG_PATH: %q, want %q", got, want)
	}
	// herdr does not fall back to another file when the one it is pointed at is missing: it runs on its defaults
	t.Setenv("HERDR_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.toml"))
	if got, want := WorktreesDir(home), filepath.Join(home, ".herdr", "worktrees"); got != want {
		t.Errorf("with a missing HERDR_CONFIG_PATH: %q, want %q", got, want)
	}
}

func TestWorktreeSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Feat/A_b.c": "feat-a-b-c", "fix/中文": "fix", "x.lock-y": "x-lock-y", "feat/2431-worktree": "feat-2431-worktree",
		"  --a//b--": "a-b", "中文": "", "": "",
	} {
		if got := WorktreeSlug(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// An ASCII branch keeps its slug; a branch with letters the slug drops, or nothing to slug, gets a folder of its own.
func TestWorktreeDirName(t *testing.T) {
	if got := WorktreeDirName("feat/2431-worktree"); got != "feat-2431-worktree" {
		t.Errorf("an ASCII branch is named %q", got)
	}
	if got := WorktreeDirName("worktree"); got != "worktree" {
		t.Errorf("the word worktree alone is named %q", got)
	}
	seen := map[string]string{}
	for _, b := range []string{"修复登录", "fix/登录", "fix/注册", "_"} {
		got := WorktreeDirName(b)
		if got == "" || seen[got] != "" {
			t.Errorf("%q: %q (also %q)", b, got, seen[got])
		}
		if !strings.HasPrefix(got, "fix-") && !strings.HasPrefix(got, "worktree-") {
			t.Errorf("%q: %q, want a fix- or worktree- prefix", b, got)
		}
		seen[got] = b
	}
}

func TestWorktreeCreateSendsWorkspaceBranchAndPath(t *testing.T) {
	var got map[string]any
	sock := fakeServer(t, func(method string, params map[string]any) string {
		got = params
		return `"result":{"type":"worktree_created","workspace":{"workspace_id":"w2"},"tab":{"tab_id":"w2:t1"},"root_pane":{"pane_id":"w2:p1"},"worktree":{"path":"/srv/wt/feat-x","branch":"feat/x"}}`
	})
	c := &Client{Bin: "/nonexistent/herdr", Socket: sock}
	res, err := c.WorktreeCreate(t.Context(), "w1", "feat/x", "/srv/wt/feat-x")
	if err != nil || res.Workspace == nil || res.Workspace.ID != "w2" || res.RootPane.ID != "w2:p1" || res.Worktree.Path != "/srv/wt/feat-x" {
		t.Fatalf("worktree.create: %+v %v", res, err)
	}
	if got["workspace_id"] != "w1" || got["branch"] != "feat/x" || got["path"] != "/srv/wt/feat-x" || got["focus"] != false || len(got) != 4 {
		t.Errorf("params = %v", got)
	}
}

func TestWorktreeCreateFallsBackToCLI(t *testing.T) {
	bin, args := cliScript(t, `{"id":"cli:worktree:create","result":{"workspace":{"workspace_id":"w2"},"tab":{"tab_id":"w2:t1"},"root_pane":{"pane_id":"w2:p1"}}}`)
	c := &Client{Bin: bin, Socket: filepath.Join(t.TempDir(), "missing.sock")}
	if _, err := c.WorktreeCreate(t.Context(), "w1", "feat/x", "/srv/wt/feat-x"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(args)
	if strings.TrimSpace(string(b)) != "worktree create --workspace w1 --branch feat/x --path /srv/wt/feat-x --no-focus" {
		t.Errorf("CLI args %q", b)
	}
	if timeoutOf("worktree.create") <= timeoutOf("pane.list") {
		t.Error("a checkout gets no more time than a pane list")
	}
}
