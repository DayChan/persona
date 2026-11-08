package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestParsePSEnv(t *testing.T) {
	out := parsePSEnv("" +
		"  101 /home/me/.local/bin/claude --settings /p/a/settings.json TERM=xterm CLAUDE_CONFIG_DIR=/p/a SHELL=/bin/zsh\n" +
		"  102 claude --resume x HOME=/home/me\n" + // the default directory: left out
		"  103 node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js CLAUDE_CONFIG_DIR=/p/b\n" +
		"  104 /bin/zsh -c run CLAUDE_CONFIG_DIR=/p/c\n" + // a shell a claude started: not claude
		"garbage\n")
	if len(out) != 2 || out[101].ConfigDir != "/p/a" || out[103].ConfigDir != "/p/b" {
		t.Fatalf("got %v", out)
	}
}

func TestParsePSEnvLauncherVars(t *testing.T) {
	out := parsePSEnv("" +
		"  201 claude --resume x ANTHROPIC_BASE_URL=https://gw.example.com/api ANTHROPIC_MODEL=model-a HOME=/home/me\n" +
		"  202 claude ANTHROPIC_BASE_URL=https://gw.example.com/api CLAUDE_CONFIG_DIR=/p/a\n" +
		"  203 claude MY_ANTHROPIC_MODEL=nope HOME=/home/me\n" + // another variable's suffix: not ours
		"  204 /bin/zsh ANTHROPIC_MODEL=model-a\n") // not claude
	want := map[int]procEnv{
		201: {BaseURL: "https://gw.example.com/api", Model: "model-a"},
		202: {ConfigDir: "/p/a", BaseURL: "https://gw.example.com/api"},
	}
	if len(out) != len(want) || out[201] != want[201] || out[202] != want[202] {
		t.Fatalf("got %v", out)
	}
	// /proc/<pid>/environ is NUL-separated.
	if e := parseProcEnv("HOME=/h\x00ANTHROPIC_MODEL=model-b\x00ANTHROPIC_BASE_URL=https://other.example.com\x00"); e != (procEnv{BaseURL: "https://other.example.com", Model: "model-b"}) {
		t.Fatalf("environ: %+v", e)
	}
}

func TestSessionEnvFromSessionsFile(t *testing.T) {
	n, profile := claudeHome(t)
	id := "cccccccc-0000-0000-0000-000000000003"
	_ = os.WriteFile(filepath.Join(profile, "sessions", "4242.json"), []byte(`{"pid":4242,"sessionId":"`+id+`"}`), 0o600)
	n.cdirs.scan = func() map[int]procEnv {
		return map[int]procEnv{4242: {ConfigDir: profile, BaseURL: "https://gw.example.com", Model: "model-a"}}
	}
	e, ok := n.sessionEnv(id)
	if !ok || e.BaseURL != "https://gw.example.com" || e.Model != "model-a" {
		t.Fatalf("session env %+v %v", e, ok)
	}
	if _, ok := n.sessionEnv("dddddddd-0000-0000-0000-000000000004"); ok {
		t.Fatal("a session no live claude runs has no environment")
	}
}

func TestMatchLauncher(t *testing.T) {
	list := []proto.Launcher{
		{Name: "claude", Model: "默认"},
		{Name: "cc_a", BaseURL: "https://gw.example.com", Model: "model-a"},
		{Name: "cc_b", BaseURL: "https://gw.example.com", Model: "model-b"},
		{Name: "cc_pick", BaseURL: "https://gw.example.com", Model: "(claude-choose)"},
		{Name: "cc_solo", BaseURL: "https://solo.example.com", Model: "(claude-choose)"},
		{Name: "cc_x", BaseURL: "https://twin.example.com", Model: "model-x"},
		{Name: "cc_y", BaseURL: "https://twin.example.com", Model: "model-x"},
	}
	for _, c := range []struct {
		name, url, model, want string
	}{
		{"exact match among a shared endpoint", "https://gw.example.com", "model-b", "cc_b"},
		{"shared endpoint, unknown model: no guess", "https://gw.example.com", "model-z", ""},
		{"shared endpoint, no model: no guess", "https://gw.example.com", "", ""},
		{"only launcher of its endpoint, dynamic model", "https://solo.example.com", "model-q", "cc_solo"},
		{"two launchers equal on both: no guess", "https://twin.example.com", "model-x", ""},
		{"unknown endpoint", "https://none.example.com", "model-a", ""},
		{"default endpoint", "", "model-a", ""},
	} {
		if got := matchLauncher(list, c.url, c.model); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := matchLauncher(nil, "https://gw.example.com", "model-a"); got != "" {
		t.Errorf("no launcher list yet: got %q", got)
	}
}

// claudeHome lays out a home with the default ~/.claude and a profile directory, each with a transcript.
func claudeHome(t *testing.T) (*Node, string) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	profile := filepath.Join(home, ".config", "cc", "work")
	for dir, id := range map[string]string{filepath.Join(home, ".claude"): "aaaaaaaa-0000-0000-0000-000000000001", profile: "bbbbbbbb-0000-0000-0000-000000000002"} {
		p := filepath.Join(dir, "projects", "-src-app")
		_ = os.MkdirAll(p, 0o700)
		_ = os.WriteFile(filepath.Join(p, id+".jsonl"), []byte("{}\n"), 0o600)
		_ = os.MkdirAll(filepath.Join(dir, "sessions"), 0o700)
	}
	n := &Node{home: home, paths: map[string]string{}, cdirs: newClaudeDirs()}
	n.cdirs.scan = func() map[int]procEnv { return map[int]procEnv{4242: {ConfigDir: profile}} }
	return n, profile
}

func TestTranscriptsInProfileDirs(t *testing.T) {
	n, profile := claudeHome(t)
	var reported []string
	n.cdirs.report = func(d []string) { reported = d }
	if p := n.transcriptPath("bbbbbbbb-0000-0000-0000-000000000002"); p != filepath.Join(profile, "projects", "-src-app", "bbbbbbbb-0000-0000-0000-000000000002.jsonl") {
		t.Fatalf("profile transcript at %q", p)
	}
	if p := n.transcriptPath("aaaaaaaa-0000-0000-0000-000000000001"); p == "" {
		t.Fatal("default transcript not found")
	}
	if len(n.globProjects("*", "*.jsonl")) != 2 {
		t.Fatalf("offline scan sees %v", n.globProjects("*", "*.jsonl"))
	}
	if !slices.Equal(reported, []string{profile}) {
		t.Fatalf("reported to the hub: %v", reported)
	}
	// The pid's own directory decides where its sessions file is.
	if f := n.claudeSessionFile(4242); f != filepath.Join(profile, "sessions", "4242.json") {
		t.Fatalf("sessions file %q", f)
	}
}

func TestKnownDirsWithoutRunningClaude(t *testing.T) {
	n, profile := claudeHome(t)
	n.cdirs.scan = func() map[int]procEnv { return nil }
	if n.transcriptPath("bbbbbbbb-0000-0000-0000-000000000002") != "" {
		t.Fatal("an unknown profile cannot be found")
	}
	n.paths = map[string]string{}
	n.setKnownClaudeDirs([]string{"~/" + mustRel(t, n.home, profile)})
	if n.transcriptPath("bbbbbbbb-0000-0000-0000-000000000002") == "" {
		t.Fatal("a directory from the hub (config or remembered) is searched")
	}
}

func TestSymlinkedProjectsCountOnce(t *testing.T) {
	n, profile := claudeHome(t)
	// The old workaround: ~/.claude/projects is a symlink to the profile's.
	def := filepath.Join(n.home, ".claude", "projects")
	_ = os.RemoveAll(def)
	if err := os.Symlink(filepath.Join(profile, "projects"), def); err != nil {
		t.Fatal(err)
	}
	if got := n.projectDirs(); len(got) != 1 {
		t.Fatalf("projects dirs %v", got)
	}
	if got := n.globProjects("*", "*.jsonl"); len(got) != 1 {
		t.Fatalf("one transcript listed twice: %v", got)
	}
}

func TestSettingsWrittenToEveryDir(t *testing.T) {
	n, profile := claudeHome(t)
	_ = os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{"model":"opus"}`), 0o600)
	if err := n.setClaudeSetting("language", "中文"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(n.home, ".claude"), profile} {
		var raw map[string]any
		b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
		_ = json.Unmarshal(b, &raw)
		if raw["language"] != "中文" {
			t.Fatalf("%s: %v", dir, raw)
		}
	}
	// The page shows the directory the running claudes use.
	if p := n.claudeSettingsPath(); p != filepath.Join(profile, "settings.json") {
		real, _ := filepath.EvalSymlinks(filepath.Join(profile, "settings.json"))
		if p != real {
			t.Fatalf("settings shown from %q", p)
		}
	}
}

func mustRel(t *testing.T, base, p string) string {
	t.Helper()
	r, err := filepath.Rel(base, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A session resumed under another profile has a file in both config directories: the old one stops, the new one
// grows. Persona read whichever the directory order gave — a session running for two days showed its chat and its
// "updated" time from before the move (2026-10-31, 45 sessions on one machine) — and listed an offline one twice.
func TestTheNewestCopyOfATranscriptWins(t *testing.T) {
	n, profile := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	const id = "cccccccc-0000-0000-0000-000000000003"
	line := func(text string) string {
		return `{"type":"user","uuid":"u1","timestamp":"2026-10-30T10:00:00Z","cwd":"/src/app","message":{"role":"user","content":"` + text + `"}}` + "\n" +
			`{"type":"assistant","uuid":"a1","timestamp":"2026-10-30T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"` + text + ` 的回答"}]}}` + "\n"
	}
	old := filepath.Join(n.home, ".claude", "projects", "-src-app", id+".jsonl")
	cur := filepath.Join(profile, "projects", "-src-app", id+".jsonl")
	_ = os.WriteFile(old, []byte(line("旧的")+strings.Repeat(" ", 4096)+"\n"), 0o600)
	_ = os.WriteFile(cur, []byte(line("新的")+strings.Repeat(" ", 4096)+"\n"), 0o600)
	_ = os.Chtimes(old, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	if p := n.transcriptPath(id); p != cur {
		t.Fatalf("read %s", p)
	}
	var listed []proto.Session
	for _, s := range n.scanOffline() {
		if s.SessionID == id {
			listed = append(listed, s)
		}
	}
	if len(listed) != 1 || !strings.Contains(listed[0].LastText, "新的") {
		t.Fatalf("listed %+v", listed)
	}
	if p := n.transcriptPath(id); p != cur {
		t.Fatalf("after the scan: %s", p)
	}
	// The profile's copy goes quiet and the other is written again: looked for again, it is found.
	_ = os.Chtimes(cur, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	_ = os.Chtimes(old, time.Now(), time.Now())
	n.pathsAt[id] = time.Now().Add(-time.Minute)
	if p := n.transcriptPath(id); p != old {
		t.Fatalf("after the move back: %s", p)
	}
}

// A blocked directory (an automation's own CLAUDE_CONFIG_DIR, a new session every few minutes) is not looked in, not
// reported to the hub however often a claude runs from it, and its sessions are not listed; unblocked, they are back
// and the hub hears about it again.
func TestBlockedDirsAreLeftOut(t *testing.T) {
	n, profile := claudeHome(t)
	bot := filepath.Join(n.home, ".automation", "claude-a")
	const botID = "dddddddd-0000-0000-0000-000000000004"
	p := filepath.Join(bot, "projects", "-src-bot")
	_ = os.MkdirAll(p, 0o700)
	_ = os.WriteFile(filepath.Join(p, botID+".jsonl"), []byte("{}\n"), 0o600)
	n.cdirs.scan = func() map[int]procEnv { return map[int]procEnv{4242: {ConfigDir: profile}, 4343: {ConfigDir: bot}} }
	var reports [][]string
	n.cdirs.report = func(d []string) { reports = append(reports, d) }
	n.setBlockedClaudeDirs([]string{"~/.automation/*"})
	n.setKnownClaudeDirs([]string{bot}) // remembered by an older hub: blocked all the same

	dirs := n.claudeConfigDirs()
	if slices.Contains(dirs, bot) || !slices.Contains(dirs, profile) {
		t.Fatalf("config dirs %v", dirs)
	}
	if n.transcriptPath(botID) != "" {
		t.Fatal("a blocked directory's transcript is found")
	}
	if len(reports) != 1 || !slices.Equal(reports[0], []string{profile}) {
		t.Fatalf("reported %v", reports)
	}
	// Another scan, the same claudes: nothing new to report.
	n.cdirs.scanned = time.Time{}
	n.claudeConfigDirs()
	if len(reports) != 1 {
		t.Fatalf("reported again: %v", reports)
	}

	n.setBlockedClaudeDirs(nil)
	if !slices.Contains(n.claudeConfigDirs(), bot) || n.transcriptPath(botID) == "" {
		t.Fatal("unblocked, the directory is searched again")
	}
	if len(reports) != 2 || !slices.Contains(reports[1], bot) {
		t.Fatalf("unblocking reports the directory: %v", reports)
	}
}

func TestDefaultDirIsNeverBlocked(t *testing.T) {
	n, _ := claudeHome(t)
	n.setBlockedClaudeDirs([]string{"~/.claude", "~/.config/cc/*"})
	if dirs := n.claudeConfigDirs(); len(dirs) != 1 || dirs[0] != filepath.Join(n.home, ".claude") {
		t.Fatalf("dirs %v", dirs)
	}
}

// claude.dirs.list shows every directory, the blocked ones marked, with their sessions and running claudes.
func TestListClaudeDirs(t *testing.T) {
	n, profile := claudeHome(t)
	n.setBlockedClaudeDirs([]string{"~/.config/cc/*"})
	if slices.Contains(n.claudeConfigDirs(), profile) {
		t.Fatal("blocked directory still searched")
	}
	dirs := n.listClaudeDirs()
	if len(dirs) != 2 || !dirs[0].Default || dirs[0].Blocked || dirs[1].Path != profile || !dirs[1].Blocked || dirs[1].Running != 1 || dirs[1].Sessions != 1 {
		t.Fatalf("dirs %+v", dirs)
	}
}

func TestCwdBlocked(t *testing.T) {
	n, _ := claudeHome(t)
	n.setBlockedClaudeDirs([]string{"*has_workspaces*", "/opt/ignored"})
	if !n.cwdBlocked("/data00/home/user/has_workspaces/session-1/workdir") {
		t.Fatal("has_workspaces cwd should be blocked")
	}
	if !n.cwdBlocked("/opt/ignored/sub") {
		t.Fatal("/opt/ignored/sub should be blocked")
	}
	if n.cwdBlocked("/data00/home/user/projects/my-repo") {
		t.Fatal("normal repo should not be blocked")
	}
}

// A session copied into another project and resumed there (#1325): the hook named the old copy and the node had it
// open, so the session page and agent_read kept showing the old conversation while Claude wrote the new one.
func TestAResumedCopyInAnotherProjectIsFollowed(t *testing.T) {
	n, _ := claudeHome(t)
	n.files, n.pathsAt = map[string]*transcript.File{}, map[string]time.Time{}
	const id = "eeeeeeee-0000-0000-0000-000000000005"
	lines := func(uuid, text string) string {
		return `{"type":"user","uuid":"` + uuid + `u","timestamp":"2026-10-30T10:00:00Z","cwd":"/src/app","message":{"role":"user","content":"` + text + `"}}` + "\n" +
			`{"type":"assistant","uuid":"` + uuid + `a","timestamp":"2026-10-30T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"` + text + ` reply"}]}}` + "\n"
	}
	projects := filepath.Join(n.home, ".claude", "projects")
	old := filepath.Join(projects, "-src-app", id+".jsonl")
	cur := filepath.Join(projects, "-src-other", id+".jsonl")
	_ = os.MkdirAll(filepath.Dir(cur), 0o700)
	_ = os.WriteFile(old, []byte(lines("1", "before")), 0o600)
	n.rememberTranscript(hookInput{SessionID: id, TranscriptPath: old})
	f, err := n.file(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}

	// Copied to the other project, resumed there and answered; the old copy stays on disk, untouched.
	_ = os.WriteFile(cur, []byte(lines("1", "before")+lines("2", "after")), 0o600)
	_ = os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	n.pathsAt[id] = time.Now().Add(-time.Minute)

	g, err := n.file(id)
	if err != nil {
		t.Fatal(err)
	}
	if g.Where() != cur {
		t.Fatalf("reading %s", g.Where())
	}
	items, err := g.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, it := range items {
		texts = append(texts, it.Text)
	}
	if !slices.Contains(texts, "after reply") {
		t.Fatalf("items after the move: %q", texts)
	}
}
