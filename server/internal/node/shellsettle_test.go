package node

import (
	"cmp"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The last lines a fresh shell's startup files leave on screen while they wait for a key, and prompts that do not.
func TestShellQuestion(t *testing.T) {
	for _, tc := range []struct {
		screen string
		asks   bool
	}{
		{"Last login: Mon Nov  9 22:01:13 on ttys004\n[oh-my-zsh] Would you like to update? [Y/n] ", true},
		{"Proceed with the upgrade? [y/N]: ", true},
		{"Continue (y/n)?", true},
		{"Overwrite the existing file? (yes/no) ", true},
		{"Press Enter to continue", true},
		{"press any key to go on…", true},
		{"$ ", false},
		{"user@host:~/proj$ ", false},
		{"➜  proj git:(main) ", false},
		{"~/proj on main\n❯ ", false},
		{"user@host ~/proj> ", false},
		{"[oh-my-zsh] Would you like to update? [Y/n] n\n➜  proj ", false}, // answered: a prompt below it
		{"", false},
	} {
		if got := shellQuestion(tc.screen) != ""; got != tc.asks {
			t.Errorf("%q: asks %v, want %v", tc.screen, got, tc.asks)
		}
	}
}

// watchShell has n's starts wait for the new tab's shell to settle, as a node does (testNode pauses instead).
func watchShell(n *Node) { n.shellPause = 0 }

// startLinePath is the start file of a line a start typed (". <path> && true <id>", quoted when it must be).
var startLinePath = regexp.MustCompile(`^(?:\.|source) '?([^' ]+)'?`)

// eatenHead has pane answer a typed start line the way zsh does when a startup question read its first key: the rest
// of the line echoed without ". ", and "permission denied" for the start file, which is not executable. ok says
// whether the try (1, 2…) is answered so; the others leave the screen to the test.
func eatenHead(f *fakeHerdr, pane string, ok func(try int) bool) {
	var mu sync.Mutex
	tries := 0
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" || str(c.Params["pane_id"]) != pane {
			return
		}
		mu.Lock()
		tries++
		try := tries
		mu.Unlock()
		text := c.Typed
		m := startLinePath.FindStringSubmatch(text)
		if m == nil || !ok(try) {
			return
		}
		f.setScreen(pane, "% "+strings.TrimPrefix(text, ". ")+"\nzsh: permission denied: "+m[1]+"\n% ")
	})
}

// sent are the lines typed into pane, in order (a start file's line as typed, not its command).
func sent(f *fakeHerdr, pane string) []string {
	var out []string
	for _, c := range f.calls() {
		if c.Method == "pane.send_input" && str(c.Params["pane_id"]) == pane {
			out = append(out, cmp.Or(c.Typed, str(c.Params["text"])))
		}
	}
	return out
}

// firstTyped records when something is first typed into pane; the func returns it (zero: not yet).
func firstTyped(f *fakeHerdr, pane string) func() time.Time {
	var mu sync.Mutex
	var at time.Time
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && str(c.Params["pane_id"]) == pane {
			mu.Lock()
			if at.IsZero() {
				at = time.Now()
			}
			mu.Unlock()
		}
	})
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return at
	}
}

// startData is a refusal's data as text.
func startData(t *testing.T, err error) map[string]string {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) {
		t.Fatalf("not a refusal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(re.Data, &m)
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// oh-my-zsh asks whether to update before the prompt (#2570). Typed into it, the start line lost its first character
// and the start waited 3 minutes for nothing. Now nothing is typed: the question is waited out, then the start ends
// with shell_waiting, the question, the tab's last lines and where the tab is kept. The tab stays open, until it
// expires; then it is closed.
func TestStartLeavesAStartupQuestionAndKeepsTheTab(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	watchShell(n)
	withStartWaits(n, time.Minute, time.Minute)
	n.shellSettle, n.shellAsk = 10*time.Second, 600*time.Millisecond
	newShellTab(f, "zsh")
	f.setScreen("w1:p2", "Last login: Mon Nov  9 22:01:13 on ttys004\n[oh-my-zsh] Would you like to update? [Y/n] ")
	began := time.Now()
	_, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude", "prompt": "hi"})
	if rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("start: %v", err)
	}
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
	data := startData(t, err)
	if data["line"] != "[oh-my-zsh] Would you like to update? [Y/n]" || !strings.Contains(data["screen"], "Last login") ||
		data["pane"] != "w1:p2" || data["delivered"] != deliveredNone {
		t.Errorf("data %v", data)
	}
	until, perr := time.Parse(time.RFC3339, data["kept_until"])
	if perr != nil || until.Before(time.Now().Add(keptStartWait-time.Minute)) {
		t.Errorf("kept_until %q", data["kept_until"])
	}
	if got := sent(f, "w1:p2"); len(got) != 0 {
		t.Errorf("typed into a shell asking a question: %v", got)
	}
	if got := f.trace("tab.close"); len(got) != 0 {
		t.Errorf("closed the tab someone should answer in: %v", got)
	}
	n.mu.Lock()
	kept, ok := n.keptStarts["w1:p2"]
	n.mu.Unlock()
	if !ok {
		t.Fatal("tab not kept")
	}
	n.expireKeptStart("w1:p2", kept.until.Add(-time.Second)) // an older keep of the same pane: not this one
	if got := f.trace("tab.close"); len(got) != 0 {
		t.Errorf("an old timer closed it: %v", got)
	}
	n.expireKeptStart("w1:p2", kept.until)
	if got := f.trace("tab.close"); len(got) != 1 || got[0] != call("tab.close", map[string]any{"tab_id": "w1:t2"}) {
		t.Errorf("expired: closed %v", got)
	}
}

// A kept tab an agent came up in after all is not closed when its time is up.
func TestKeptStartTabWithAnAgentStays(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	watchShell(n)
	withStartWaits(n, time.Minute, time.Minute)
	n.shellAsk = 300 * time.Millisecond
	newShellTab(f, "zsh")
	f.setScreen("w1:p2", "Press Enter to continue")
	if _, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude"}); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("start: %v", err)
	}
	n.mu.Lock()
	until := n.keptStarts["w1:p2"].until
	n.agents["later"] = host.Agent{Agent: "claude", PaneID: "w1:p2"}
	n.mu.Unlock()
	n.expireKeptStart("w1:p2", until)
	if got := f.trace("tab.close"); len(got) != 0 {
		t.Errorf("closed a tab with an agent: %v", got)
	}
}

// A question that goes away (someone answered it, or it timed out by itself) is not a failed start: the line is typed
// once the shell is back at its prompt.
func TestStartTypesOnceTheQuestionIsAnswered(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	watchShell(n)
	withStartWaits(n, 1500*time.Millisecond, time.Minute)
	n.shellSettle, n.shellQuiet, n.shellAsk = 10*time.Second, 300*time.Millisecond, 10*time.Second
	newShellTab(f, "zsh")
	f.setScreen("w1:p2", "[oh-my-zsh] Would you like to update? [Y/n] ")
	typed := firstTyped(f, "w1:p2")
	answered := time.Now().Add(700 * time.Millisecond)
	time.AfterFunc(700*time.Millisecond, func() { f.setScreen("w1:p2", "[oh-my-zsh] Would you like to update? [Y/n] n\n% ") })
	_, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude"})
	if err == nil || rpcCode(err) == proto.CodeShellWaiting {
		t.Fatalf("start: %v", err) // claude never comes here: the start times out after typing
	}
	at := typed()
	if at.IsZero() || at.Before(answered) {
		t.Errorf("typed at %v, the question answered at %v", at, answered)
	}
}

// A shell still running what its rc files start (git for the prompt, nvm) is not at rest, however still its screen;
// nor is one whose prompt is being drawn again (powerlevel10k's instant prompt, then the real one). The start types once
// only the shell is in front and the screen has stayed the same for a moment.
func TestStartWaitsForTheShellToSettle(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	watchShell(n)
	withStartWaits(n, 1500*time.Millisecond, time.Minute)
	n.shellSettle, n.shellQuiet = 10*time.Second, 300*time.Millisecond
	newShellTab(f, "zsh")
	typed := firstTyped(f, "w1:p2")
	began := time.Now()
	f.handle("pane.process_info", func(map[string]any) any {
		if time.Since(began) < 900*time.Millisecond {
			return foreground([2]string{"zsh", "-zsh"}, [2]string{"git", "git"})
		}
		return foreground([2]string{"zsh", "-zsh"})
	})
	f.setScreen("w1:p2", "❯ ")
	for i, d := range []time.Duration{100, 200, 300} {
		prompt := strings.Repeat(" ", i) + "~/proj on main ❯ "
		time.AfterFunc(d*time.Millisecond, func() { f.setScreen("w1:p2", prompt) })
	}
	_, _ = request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude"})
	at := typed()
	if d := at.Sub(began); at.IsZero() || d < 900*time.Millisecond || d > 3*time.Second {
		t.Errorf("typed %v after the tab was made", d)
	}
}

// bash and fish at their prompts settle as zsh does, and fish gets its source line.
func TestStartSettlesOnBashAndFish(t *testing.T) {
	for _, tc := range []struct{ shell, prompt, verb string }{
		{"bash", "user@host:~/proj$ ", ". "},
		{"fish", "user@host ~/proj> ", "source "},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			withPaneShell(t, "/bin/"+tc.shell)
			n, f, home := herdrNode(t)
			watchShell(n)
			withStartWaits(n, time.Second, time.Minute)
			n.shellSettle, n.shellQuiet = 10*time.Second, 300*time.Millisecond
			newShellTab(f, tc.shell)
			f.setScreen("w1:p2", tc.prompt)
			typed := firstTyped(f, "w1:p2")
			began := time.Now()
			_, _ = request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude"})
			got := sent(f, "w1:p2")
			if len(got) != 1 || !strings.HasPrefix(got[0], tc.verb) {
				t.Fatalf("typed %q", got)
			}
			at := typed()
			if d := at.Sub(began); d < 300*time.Millisecond || d > 2*time.Second {
				t.Errorf("typed %v after the start", d)
			}
		})
	}
}

// Something ate the typed line's head after all (a question the settle wait could not see): the shell's answer for
// what was left ends the wait at once, the same line is typed again once the shell is at rest, and the start goes on.
func TestStartTypesARejectedLineAgain(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	watchShell(n)
	withStartWaits(n, time.Minute, time.Minute)
	n.shellQuiet = 200 * time.Millisecond
	newShellTab(f, "zsh")
	eatenHead(f, "w1:p2", func(try int) bool { return try == 1 })
	var mu sync.Mutex
	up, tries := false, 0
	claudeScreen := screenFixture(t, "claude-2.1.312-empty-162.txt")
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		mu.Lock()
		tries++
		up = tries == 2
		mu.Unlock()
		if up {
			f.setScreen("w1:p2", claudeScreen)
		}
	})
	f.handle("agent.get", func(map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			return map[string]any{"agent": map[string]any{"agent": "", "agent_status": "unknown", "pane_id": "w1:p2"}}
		}
		return map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": proto.StatusIdle, "pane_id": "w1:p2",
			"tab_id": "w1:t2", "workspace_id": "w1"}}
	})
	began := time.Now()
	if _, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if d := time.Since(began); d > 15*time.Second {
		t.Errorf("took %s", d)
	}
	if got := sent(f, "w1:p2"); len(got) != 2 || got[0] != got[1] {
		t.Errorf("typed %q", got)
	}
	if got := f.trace("tab.close"); len(got) != 0 {
		t.Errorf("closed %v", got)
	}
}

// A line the shell refuses twice ends the start with start_line_rejected and the shell's line, within seconds, and
// closes the tab.
func TestStartLineRejectedTwiceClosesTheTab(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	watchShell(n)
	withStartWaits(n, time.Minute, time.Minute)
	n.shellQuiet = 200 * time.Millisecond
	newShellTab(f, "zsh")
	eatenHead(f, "w1:p2", func(int) bool { return true })
	began := time.Now()
	_, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude", "prompt": "hi"})
	if rpcCode(err) != proto.CodeStartLineRejected {
		t.Fatalf("start: %v", err)
	}
	if d := time.Since(began); d > 15*time.Second {
		t.Errorf("took %s", d)
	}
	data := startData(t, err)
	if !strings.HasPrefix(data["line"], "zsh: permission denied: ") || !strings.Contains(data["screen"], "permission denied") {
		t.Errorf("data %v", data)
	}
	if got := sent(f, "w1:p2"); len(got) != 2 {
		t.Errorf("typed %q", got)
	}
	if got := f.trace("tab.close"); len(got) != 1 {
		t.Errorf("closed %v", got)
	}
}
