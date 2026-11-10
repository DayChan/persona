package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Every test here works in a temporary directory; none reads or writes the real home or claude config.

const testGlobalCmd = "/opt/persona/personad hook --persona-global"

// A settings.json shaped like one with other tools' hooks in it: a synchronous PermissionRequest hook, async reporters,
// an env block, tabs for indent, keys in an order a map would not keep.
const otherToolsSettings = `{
	"theme": "dark",
	"env": {
		"CLAUDE_CODE_PLUGIN_DIRS": "/tmp/other-tool-plugin",
		"HTTPS_PROXY": "http://127.0.0.1:1"
	},
	"hooks": {
		"Stop": [
			{"hooks": [{"type": "command", "command": "other-tool report", "async": true}]}
		],
		"PermissionRequest": [
			{"matcher": "*", "hooks": [{"type": "command", "command": "other-tool approve && echo <ok>", "timeout": 10}]}
		]
	},
	"permissions": {"allow": ["Bash(ls:*)"]},
	"model": "opus"
}
`

func seedSettings(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
		// WriteFile applies the umask; the mode an install must keep is 0640 under any umask.
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func parseSettings(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

func settingsBackups(t *testing.T, path string) []string {
	t.Helper()
	m, _ := filepath.Glob(path + ".persona-bak-*")
	return m
}

func TestInstallKeepsEverythingElseAndNeverBlocks(t *testing.T) {
	path := seedSettings(t, otherToolsSettings)
	changed, err := installGlobalHooks(path, testGlobalCmd)
	if err != nil || !changed {
		t.Fatalf("install = %v, %v", changed, err)
	}
	got, _ := os.ReadFile(path)
	s := parseSettings(t, got)
	hooks := s["hooks"].(map[string]any)

	// Everything that was there is still there, unchanged.
	orig := parseSettings(t, []byte(otherToolsSettings))
	for _, k := range []string{"theme", "env", "permissions", "model"} {
		if a, b := jsonString(t, orig[k]), jsonString(t, s[k]); a != b {
			t.Errorf("%s changed: %s -> %s", k, a, b)
		}
	}
	perm := hooks["PermissionRequest"].([]any)
	if len(perm) != 1 || perm[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"] != "other-tool approve && echo <ok>" {
		t.Errorf("another tool's PermissionRequest hook was touched: %v", perm)
	}
	if stop := hooks["Stop"].([]any); len(stop) != 2 || !strings.Contains(jsonString(t, stop[0]), "other-tool report") {
		t.Errorf("Stop = %v: the other tool's group first, ours after it", stop)
	}

	// Ours: report only. Every one is async with a short timeout, and no event that could hold an approval is added.
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "Notification", "PreToolUse", "PostToolUse"} {
		for _, g := range hooks[ev].([]any) {
			for _, h := range g.(map[string]any)["hooks"].([]any) {
				e := h.(map[string]any)
				if e["command"] != testGlobalCmd {
					continue
				}
				if e["async"] != true || e["timeout"].(float64) > 10 {
					t.Errorf("%s: %v is not async with a short timeout", ev, e)
				}
			}
		}
	}
	if len(perm) != 1 || hooks["Elicitation"] != nil {
		t.Errorf("a blocking event was added: PermissionRequest %v, Elicitation %v", perm, hooks["Elicitation"])
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		var matchers []string
		for _, g := range hooks[ev].([]any) {
			matchers = append(matchers, g.(map[string]any)["matcher"].(string))
		}
		if strings.Join(matchers, ",") != "AskUserQuestion,ExitPlanMode" {
			t.Errorf("%s matchers = %v", ev, matchers)
		}
	}

	// Layout: the tab indent, the key order, the trailing newline and the mode are the file's own; & and < are not escaped.
	text := string(got)
	if !strings.Contains(text, "\n\t\"theme\": \"dark\",\n\t\"env\"") || !strings.HasSuffix(text, "}\n") {
		t.Errorf("layout not kept:\n%s", text)
	}
	if !strings.Contains(text, "other-tool approve && echo <ok>") || strings.Contains(text, "\\u0026") {
		t.Errorf("html escaped:\n%s", text)
	}
	if strings.Index(text, `"theme"`) > strings.Index(text, `"env"`) || strings.Index(text, `"hooks"`) > strings.Index(text, `"permissions"`) ||
		strings.Index(text, `"permissions"`) > strings.Index(text, `"model"`) {
		t.Errorf("top-level order changed:\n%s", text)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}

	// The old file was copied first.
	bk := settingsBackups(t, path)
	if len(bk) != 1 {
		t.Fatalf("backups = %v", bk)
	}
	if b, _ := os.ReadFile(bk[0]); string(b) != otherToolsSettings {
		t.Errorf("backup is not the original")
	}
}

func TestInstallTwiceChangesNothing(t *testing.T) {
	path := seedSettings(t, otherToolsSettings)
	if _, err := installGlobalHooks(path, testGlobalCmd); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	changed, err := installGlobalHooks(path, testGlobalCmd)
	if err != nil || changed {
		t.Fatalf("second install = %v, %v", changed, err)
	}
	if second, _ := os.ReadFile(path); !bytes.Equal(first, second) {
		t.Error("the file changed on a second install")
	}
	if n := len(settingsBackups(t, path)); n != 1 {
		t.Errorf("%d backups after two installs, want 1", n)
	}
	// Counted: one group of ours per event/matcher, not two.
	if c := strings.Count(string(readBack(t, path)), testGlobalCmd); c != len(globalHookEvents) {
		t.Errorf("%d entries, want %d", c, len(globalHookEvents))
	}
}

func readBack(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInstallReplacesEntriesWhenTheBinaryMoves(t *testing.T) {
	path := seedSettings(t, otherToolsSettings)
	if _, err := installGlobalHooks(path, "/old/personad hook "+globalHookFlag); err != nil {
		t.Fatal(err)
	}
	if changed, err := installGlobalHooks(path, testGlobalCmd); err != nil || !changed {
		t.Fatalf("install = %v, %v", changed, err)
	}
	text := string(readBack(t, path))
	if strings.Contains(text, "/old/personad") || strings.Count(text, testGlobalCmd) != len(globalHookEvents) {
		t.Errorf("old entries left or doubled:\n%s", text)
	}
}

func TestUninstallRemovesOnlyOurs(t *testing.T) {
	path := seedSettings(t, otherToolsSettings)
	if _, err := installGlobalHooks(path, testGlobalCmd); err != nil {
		t.Fatal(err)
	}
	changed, err := uninstallGlobalHooks(path)
	if err != nil || !changed {
		t.Fatalf("uninstall = %v, %v", changed, err)
	}
	after := parseSettings(t, readBack(t, path))
	if a, b := jsonString(t, parseSettings(t, []byte(otherToolsSettings))), jsonString(t, after); a != b {
		t.Errorf("not back to what it was:\n%s\n%s", a, b)
	}
	if strings.Contains(string(readBack(t, path)), globalHookFlag) {
		t.Error("an entry of ours is left")
	}
	// Nothing left to remove: no write, no backup.
	before := readBack(t, path)
	nb := len(settingsBackups(t, path))
	if changed, err := uninstallGlobalHooks(path); err != nil || changed {
		t.Fatalf("second uninstall = %v, %v", changed, err)
	}
	if !bytes.Equal(before, readBack(t, path)) || len(settingsBackups(t, path)) != nb {
		t.Error("an uninstall with nothing to remove wrote something")
	}
}

func TestUninstallWithoutOurEntriesLeavesTheFileAlone(t *testing.T) {
	odd := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]},   "a":1}`
	path := seedSettings(t, odd)
	if changed, err := uninstallGlobalHooks(path); err != nil || changed {
		t.Fatalf("uninstall = %v, %v", changed, err)
	}
	if string(readBack(t, path)) != odd || len(settingsBackups(t, path)) != 0 {
		t.Error("the file was rewritten though it had nothing of ours")
	}
}

func TestInstallIntoNewFileAndUninstallLeavesAnEmptyObject(t *testing.T) {
	path := seedSettings(t, "")
	if changed, err := installGlobalHooks(path, testGlobalCmd); err != nil || !changed {
		t.Fatalf("install = %v, %v", changed, err)
	}
	if len(settingsBackups(t, path)) != 0 {
		t.Error("a backup of a file that was not there")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v", st.Mode().Perm())
	}
	if _, err := uninstallGlobalHooks(path); err != nil {
		t.Fatal(err)
	}
	if got := parseSettings(t, readBack(t, path)); len(got) != 0 {
		t.Errorf("settings = %v, want {}", got)
	}
}

func TestMissingFileIsNotCreatedByUninstall(t *testing.T) {
	path := seedSettings(t, "")
	if changed, err := uninstallGlobalHooks(path); err != nil || changed {
		t.Fatalf("uninstall = %v, %v", changed, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("uninstall created the file")
	}
}

func TestBrokenSettingsAreLeftAlone(t *testing.T) {
	for name, content := range map[string]string{
		"not json":         `{"hooks": `,
		"not an object":    `["x"]`,
		"hooks not object": `{"hooks": ["x"]}`,
		"event not a list": `{"hooks": {"Stop": {"a": 1}}}`,
	} {
		path := seedSettings(t, content)
		if _, err := installGlobalHooks(path, testGlobalCmd); err == nil {
			t.Errorf("%s: install did not fail", name)
		}
		if string(readBack(t, path)) != content || len(settingsBackups(t, path)) != 0 {
			t.Errorf("%s: the file was touched", name)
		}
	}
}

func TestAHookOfTheUsersBesideOursStays(t *testing.T) {
	mixed := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"mine"},{"type":"command","command":"/p/personad hook --persona-global","async":true}]}]}}`
	path := seedSettings(t, mixed)
	if changed, err := uninstallGlobalHooks(path); err != nil || !changed {
		t.Fatalf("uninstall = %v, %v", changed, err)
	}
	text := string(readBack(t, path))
	if !strings.Contains(text, `"mine"`) || strings.Contains(text, globalHookFlag) {
		t.Errorf("settings = %s", text)
	}
}

func TestASymlinkedSettingsFileStaysASymlink(t *testing.T) {
	real := seedSettings(t, otherToolsSettings)
	dir := t.TempDir()
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here")
	}
	if _, err := installGlobalHooks(settingsFile(dir), testGlobalCmd); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a file")
	}
	if !strings.Contains(string(readBack(t, real)), globalHookFlag) {
		t.Error("the real file was not written")
	}
}

func TestIsGlobalHookCommand(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/p/personad hook --persona-global":        true,
		"'/my dir/personad' hook --persona-global": true,
		"/p/personad hook":                         false, // Persona's own plugin hook, which may block
		"other-tool hook --global":                 false,
		"other-tool --persona-global":              false,
		"":                                         false,
	} {
		if got := isGlobalHookCommand(cmd); got != want {
			t.Errorf("isGlobalHookCommand(%q) = %v", cmd, got)
		}
	}
}

func jsonString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// node.configure is the only thing that starts an install, and only when the hub says claude_global_hooks.
func TestConfigureInstallsOnlyWhenAsked(t *testing.T) {
	n, home := testNode(t)
	n.bin = "/opt/persona/personad"
	dir := filepath.Join(home, ".claude")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(otherToolsSettings), 0o600); err != nil {
		t.Fatal(err)
	}

	configureNode(t, n, map[string]any{"botmux": false}) // an older hub, or a call about something else
	if string(readBack(t, path)) != otherToolsSettings {
		t.Fatal("a configure that said nothing of it touched settings.json")
	}
	configureNode(t, n, map[string]any{"claude_global_hooks": false}) // off, and nothing of ours there
	if string(readBack(t, path)) != otherToolsSettings || len(settingsBackups(t, path)) != 0 {
		t.Fatal("off rewrote a file with nothing of ours in it")
	}
	configureNode(t, n, map[string]any{"claude_global_hooks": true})
	if !strings.Contains(string(readBack(t, path)), "/opt/persona/personad hook --persona-global") {
		t.Fatal("on did not install")
	}
	configureNode(t, n, map[string]any{"claude_global_hooks": true}) // every hello
	if len(settingsBackups(t, path)) != 1 {
		t.Errorf("a second configure wrote again: %v", settingsBackups(t, path))
	}
	configureNode(t, n, map[string]any{"claude_global_hooks": false})
	if strings.Contains(string(readBack(t, path)), globalHookFlag) {
		t.Fatal("off did not remove")
	}
}

func TestConfigureDoesNotCreateAConfigDirectory(t *testing.T) {
	n, home := testNode(t)
	n.bin = "/opt/persona/personad"
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	configureNode(t, n, map[string]any{"claude_global_hooks": true})
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Error("created ~/.claude on a machine without claude")
	}
}

// hookEvents is a fake hub that records the hook.event notifications.
type hookEvents struct {
	mu  sync.Mutex
	got []map[string]any
}

func (h *hookEvents) all() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.got...)
}

func hookHub(t *testing.T, n *Node) *hookEvents {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	rec := &hookEvents{}
	hub.OnEvent = func(event string, params json.RawMessage) {
		if event != "hook.event" {
			return
		}
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		rec.mu.Lock()
		rec.got = append(rec.got, p)
		rec.mu.Unlock()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	return rec
}

func reportInput(event, sid, tool string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": sid, "tool_name": tool, "cwd": "/work/project",
		"transcript_path": "/work/t.jsonl", "message": "m"})
	return b
}

const reportSession = "11111111-2222-3333-4444-555555555555"

func TestReportOnlyReportsAndNeverAsks(t *testing.T) {
	n, _ := testNode(t)
	rec := hookHub(t, n)

	for _, tc := range []struct{ event, tool string }{
		{"SessionStart", ""}, {"UserPromptSubmit", ""}, {"Stop", ""}, {"Notification", ""},
		{"PreToolUse", "AskUserQuestion"}, {"PostToolUse", "AskUserQuestion"},
		{"PreToolUse", "ExitPlanMode"}, {"PostToolUse", "ExitPlanMode"},
	} {
		// The tool calls here are the ones a blocking hook would answer (a question, a plan): reporting returns at once
		// with nothing for Claude Code to read, though no hub answers.
		done := make(chan any, 1)
		go func() {
			out, _ := n.handleHook(context.Background(), sockRequest{Kind: "hook", Pane: "p_1", ReportOnly: true, Input: reportInput(tc.event, reportSession, tc.tool)})
			done <- out
		}()
		select {
		case out := <-done:
			if out != nil {
				t.Errorf("%s/%s: output %v", tc.event, tc.tool, out)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s/%s: waited", tc.event, tc.tool)
		}
	}
	waitFor(t, "8 reports", func() bool { return len(rec.all()) == 8 })
	for _, e := range rec.all() {
		if e["report_only"] != true || e["session_id"] != reportSession {
			t.Errorf("event = %v", e)
		}
	}
	n.mu.Lock()
	path := n.paths[reportSession]
	n.mu.Unlock()
	if path != "/work/t.jsonl" {
		t.Errorf("transcript path = %q", path)
	}
	n.mu.Lock()
	bound := len(n.panes)
	n.mu.Unlock()
	if bound != 0 {
		t.Error("a report-only hook bound a pane to the session")
	}
}

func TestReportOnlySkipsWhatPersonaAlreadyReports(t *testing.T) {
	n, _ := testNode(t)
	rec := hookHub(t, n)
	n.state.put(reportSession, sessionState{Pane: "p_1", Launcher: "claude"}) // started by Persona: its plugin reports

	for _, tc := range []struct {
		name string
		req  sockRequest
	}{
		{"started by Persona", sockRequest{Pane: "p_1", ReportOnly: true, Input: reportInput("Stop", reportSession, "")}},
		{"an event it does not report", sockRequest{Pane: "p_1", ReportOnly: true, Input: reportInput("PreToolUse", "99999999-2222-3333-4444-555555555555", "Bash")}},
		{"not a session id", sockRequest{Pane: "p_1", ReportOnly: true, Input: reportInput("Stop", "../x", "")}},
	} {
		tc.req.Kind = "hook"
		if out, _ := n.handleHook(context.Background(), tc.req); out != nil {
			t.Errorf("%s: output %v", tc.name, out)
		}
	}
	// Then one that is reported, so the absence above is not just slowness.
	_, _ = n.handleHook(context.Background(), sockRequest{Kind: "hook", Pane: "p_1", ReportOnly: true, Input: reportInput("Stop", "77777777-2222-3333-4444-555555555555", "")})
	waitFor(t, "the one report", func() bool { return len(rec.all()) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if got := rec.all(); len(got) != 1 || got[0]["session_id"] != "77777777-2222-3333-4444-555555555555" {
		t.Errorf("reports = %v", got)
	}
}

// A session the user started has only these reports to go by, so they feed the turn counters (hookturn.go) as the
// plugin's hooks do: a prompt opens a turn, a Stop closes it and carries the reply's signature for the hub.
func TestReportOnlyDrivesTheTurnCounters(t *testing.T) {
	n, _ := testNode(t)
	n.hooks = &hookTurns{}
	rec := hookHub(t, n)
	report := func(sid, event, reply string) {
		b, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": sid, "last_assistant_message": reply})
		_, _ = n.handleHook(context.Background(), sockRequest{Kind: "hook", Pane: "p_1", ReportOnly: true, Input: b})
	}

	report(reportSession, "UserPromptSubmit", "")
	if tr, _ := n.hooks.get(reportSession); !tr.open() {
		t.Fatalf("a reported prompt did not open a turn: %+v", tr)
	}
	report(reportSession, "Stop", "all done")
	if tr, _ := n.hooks.get(reportSession); tr.open() || !tr.stopped {
		t.Fatalf("a reported Stop did not close it: %+v", tr)
	}
	waitFor(t, "both reports", func() bool { return len(rec.all()) == 2 })
	if got := rec.all()[1]; got["report_only"] != true || got["stop_sig"] != stopSignature("all done") {
		t.Errorf("the Stop report = %v", got)
	}

	// Persona's own session is counted by its plugin's hooks; the report-only copy must not count it a second time.
	ours := "88888888-2222-3333-4444-555555555555"
	n.state.put(ours, sessionState{Pane: "p_2", Launcher: "claude"})
	report(ours, "UserPromptSubmit", "")
	if _, ok := n.hooks.get(ours); ok {
		t.Error("a report-only prompt of a Persona session was counted")
	}
}

// The hook process: no pane, or no node socket, and it is over at once, having dialled nothing.
func TestReportHookReturnsAtOnceWithoutAPaneOrASocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	if !strings.HasPrefix(nodeclient.SocketPath(), home) { // never the real node's socket
		t.Skipf("socket path %s is not under the temporary home", nodeclient.SocketPath())
	}
	done := make(chan struct{})
	go func() {
		nodeclient.ReportHook(context.Background(), reportInput("Stop", reportSession, ""), "")    // no pane
		nodeclient.ReportHook(context.Background(), reportInput("Stop", reportSession, ""), "p_1") // no socket
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ReportHook waited")
	}
}
