package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// typedStartOverDir bounds what a start types into a herdr pane beyond the node directory's path and the session id:
// `. '`, the start file's name, `' && true ` and Enter, whatever the command's flags (#2022).
const typedStartOverDir = 64

// The worst case of #2022: a managed launcher with its settings, Persona's plugin directories and office rule, a [1m]
// model, under a long home. The command is over the canonical limit; what is typed is the short line that sources it,
// whatever the flags, from a file only the user can read that is gone once the start is over.
func TestHerdrStartTypesAShortLine(t *testing.T) {
	withClaudeVersion(t, "2.1.318")
	n, f, home := herdrNode(t)
	n.dir = mkdir(t, filepath.Join(home, strings.Repeat("a-long-home-directory-name/", 6), ".persona"))
	if err := n.writeClaudeConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := request(t, n, "launchers.managed", map[string]any{"launchers": []any{
		map[string]any{"name": "work_gw", "base_url": "https://gateway.example.com", "model": "claude-opus-5-5[1m]"}}}); err != nil {
		t.Fatal(err)
	}
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.screenSteps("w1:p2", isKeys(), screenFixture(t, "claude-2.1.312-empty-162.txt"))
	var mu sync.Mutex
	var typed, file string
	var mode os.FileMode
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" || c.Typed == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		typed = c.Typed
		file, _, _ = strings.Cut(strings.TrimPrefix(c.Typed, ". "), " ")
		file = strings.Trim(file, "'")
		if fi, err := os.Stat(file); err == nil {
			mode = fi.Mode().Perm()
		}
	})
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": cwd, "launcher": "work_gw",
		"permission_mode": "auto", "effort": "medium"}); err != nil {
		t.Fatal(err)
	}
	cmds := f.calls()
	var cmd string
	for _, c := range cmds {
		if c.Method == "pane.send_input" && c.Typed != "" {
			cmd = str(c.Params["text"])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if typed == "" {
		t.Fatalf("no start typed through a file: %v", f.trace("pane.send_input"))
	}
	if !strings.Contains(cmd, "personad launch work_gw") || len(cmd) < 1024 {
		t.Errorf("not the worst case (%d bytes): %s", len(cmd), cmd)
	}
	_, id, _ := strings.Cut(typed, " && true ")
	if !validID(id) || !strings.Contains(cmd, "--session-id "+id) {
		t.Errorf("typed %q: not the session id the command starts, for the start probe", typed)
	}
	if len(typed)+1 >= 1024 || len(typed)+1 > len(n.dir)+len(id)+typedStartOverDir { // + Enter
		t.Errorf("typed %d bytes (directory %d): %q", len(typed)+1, len(n.dir), typed)
	}
	if mode != 0o600 {
		t.Errorf("start file mode %v", mode)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("start file left behind: %v", err)
	}
}

// herdr refuses the typed line (pane_send_failed): the start says the terminal was not ready, retryable with nothing
// delivered, and closes the tab it made instead of leaving an empty shell behind (#2022).
func TestHerdrStartThePaneRefusesClosesItsTab(t *testing.T) {
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"pane_id": "w1:p2", "tab_id": "w1:t2"}}})
	f.handle("pane.send_input", func(map[string]any) any {
		return herdrFail{Code: "pane_send_failed", Message: "input contains a 1079-byte line but the terminal is in canonical mode with a 1024-byte limit"}
	})
	_, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "prompt": "hi"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != terminalNotReady {
		t.Fatalf("start: %v", err)
	}
	var data map[string]any
	_ = json.Unmarshal(re.Data, &data)
	if data["retryable"] != true || data["delivered"] != deliveredNone {
		t.Errorf("data %s", re.Data)
	}
	if got := f.trace("tab.close"); len(got) != 1 || got[0] != call("tab.close", map[string]any{"tab_id": "w1:t2"}) {
		t.Errorf("closed %v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(n.dir, startScriptPrefix+"*")); len(left) != 0 {
		t.Errorf("start files left behind: %v", left)
	}
	if rpcCode(uncertain(err)) != terminalNotReady {
		t.Errorf("a resume's uncertain() lost the refusal: %v", uncertain(err))
	}
}

// A real shell sources the start file as if the command were typed: a launcher that is an alias or a function runs,
// with the node's directory and without the assistant's environment, and the file is gone after.
func TestStartFileInRealShells(t *testing.T) {
	defs := "ccf() { claude --model from-function \"$@\"; }\nalias ccv='claude --model from-alias'\n"
	for _, sh := range []struct{ name, rc string }{{"bash", ".bashrc"}, {"zsh", ".zshrc"}} {
		t.Run(sh.name, func(t *testing.T) {
			bin, err := exec.LookPath(sh.name)
			if err != nil {
				t.Skipf("%s is not installed", sh.name)
			}
			home, dir, _ := twoStores(t)
			path := filepath.Join(home, "bin")
			fakeProgram(t, filepath.Join(path, "claude"), `printf '%s %s' "${PERSONA_HOME-unset}" "${PERSONA_ASSISTANT-unset}" >"$HOME/claude-env"`)
			writeFile(t, filepath.Join(home, sh.rc), defs)
			n, _ := testNode(t)
			n.dir = dir
			for _, l := range []string{"claude", "ccf", "ccv"} {
				_ = os.Remove(filepath.Join(home, "claude-env"))
				cmd := n.agentCommand(n.claudeCommandLine(claudeLaunch{Launcher: l}, "--session-id "+startID, shellQuote), shellQuote, bin)
				file, err := writeStartScript(dir, cmd)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				c := exec.CommandContext(ctx, bin, "-ic", startLine(file, startID, bin))
				c.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "PATH=" + path + ":/usr/bin:/bin", "PERSONA_ASSISTANT=1"}
				out, err := c.CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("%s: %v: %s", l, err, out)
				}
				if got, _ := os.ReadFile(filepath.Join(home, "claude-env")); string(got) != dir+" unset" {
					t.Errorf("%s (%s): claude got %q", sh.name, l, got)
				}
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Errorf("%s (%s): start file left behind", sh.name, l)
				}
			}
			// A launcher the shell does not have: its not-found line, prefixed with the file's path, is still read as one
			// (startProbe, shellmiss.go), with the pane's shell left to the probe.
			cmd := n.agentCommand(n.claudeCommandLine(claudeLaunch{Launcher: "nosuch"}, "--session-id "+startID, shellQuote), shellQuote, bin)
			file, err := writeStartScript(dir, cmd)
			if err != nil {
				t.Fatal(err)
			}
			c := exec.Command(bin, "-ic", startLine(file, startID, bin))
			c.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "PATH=" + path + ":/usr/bin:/bin"}
			out, _ := c.CombinedOutput()
			var miss shellMiss
			for _, l := range strings.Split(string(out), "\n") {
				if m, ok := parseShellMiss(l); ok {
					miss = m
				}
			}
			if miss.Word != "nosuch" || miss.Kind != missNotFound || miss.Shell != "" && miss.Shell != sh.name {
				t.Errorf("%s: not-found line %+v in %q", sh.name, miss, out)
			}
		})
	}
}
