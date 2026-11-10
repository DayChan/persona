package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// What a launcher declares is all Persona applies (#3066): nothing written for one that declares nothing (Claude Code
// and Codex compact by the model), a managed launcher's declared values written for the start; the idle threshold is
// 75% of the declared window, else the default until a window is reported.
func TestAssistantCompactionDeclared(t *testing.T) {
	for _, tt := range []struct {
		name  string
		c     assistantCompaction
		env   []string
		idle  int
		codex int
	}{
		{"nothing declared", assistantCompaction{}, nil, 150_000, 0},
		{"managed, nothing declared", assistantCompaction{pin: true}, nil, 150_000, 0},
		{"shell launcher's own values are not written", assistantCompaction{window: 200_000, capacity: 1_000_000}, nil, 150_000, 0},
		{"managed 200K", assistantCompaction{window: 200_000, capacity: 200_000, pin: true},
			[]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000", "CLAUDE_CODE_MAX_CONTEXT_TOKENS=200000"}, 150_000, 200_000},
		{"managed 1M model", assistantCompaction{window: 1_000_000, capacity: 1_000_000, pin: true},
			[]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000", "CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000"}, 750_000, 1_000_000},
		{"managed capacity only", assistantCompaction{capacity: 128_000, pin: true},
			[]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS=128000"}, 96_000, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.env(); strings.Join(got, " ") != strings.Join(tt.env, " ") {
				t.Fatalf("env %q, want %q", got, tt.env)
			}
			if got := tt.c.idle(); got != tt.idle {
				t.Fatalf("idle %d, want %d", got, tt.idle)
			}
			args := strings.Join(codexArgs("personad", "test", tt.c.codexLimit()), " ")
			if has := strings.Contains(args, "model_auto_compact_token_limit"); has != (tt.codex > 0) ||
				(tt.codex > 0 && !strings.Contains(args, "model_auto_compact_token_limit="+strconv.Itoa(tt.codex))) {
				t.Fatalf("codex args %q, want limit %d", args, tt.codex)
			}
			p := &assistantProc{agent: "claude", compactIdleAt: tt.c.idle(), context: tt.c.idle() - 1}
			if p.idleCompactable() {
				t.Fatal("compacts below idle threshold")
			}
			p.context++
			if !p.idleCompactable() {
				t.Fatal("does not compact at idle threshold")
			}
		})
	}
}

func TestAssistantProviderCapacity(t *testing.T) {
	base := proto.ManagedLauncher{Name: "gateway", Model: "big", Models: []proto.ProviderModel{
		{ID: "big", ContextWindow: 1_000_000}, {ID: "small", ContextWindow: 128_000},
	}, CompactWindow: 200_000}
	for _, tt := range []struct {
		name, model, env string
		max, want        int
	}{
		{"real capacity not compact policy", "big", "", 0, 1_000_000},
		{"selected small model", "small", "", 0, 128_000},
		{"explicit max", "big", "", 200_000, 200_000},
		{"model beats inflated max", "small", "", 1_000_000, 128_000},
		{"env max", "big", "CLAUDE_CODE_MAX_CONTEXT_TOKENS=128000", 0, 128_000},
		{"auto env does not mean capacity", "big", "CLAUDE_CODE_AUTO_COMPACT_WINDOW=150000", 0, 1_000_000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := base
			l.MaxContext, l.Env = tt.max, tt.env
			if got := providerCapacity(l, tt.model); got != tt.want {
				t.Fatalf("capacity %d, want %d", got, tt.want)
			}
			n := assistantNode(t)
			n.setManaged([]proto.ManagedLauncher{l})
			c := n.assistantCompactionPolicy(l.Name, t.TempDir(), "", modelExecution{Model: tt.model})
			if !slices.Contains(c.env(), "CLAUDE_CODE_MAX_CONTEXT_TOKENS="+strconv.Itoa(tt.want)) {
				t.Fatalf("start env %q, want MAX_CONTEXT_TOKENS %d", c.env(), tt.want)
			}
		})
	}
}

func TestAssistantCompactionRPCAndSpare(t *testing.T) {
	for _, main := range []bool{true, false} {
		t.Run(strconv.FormatBool(main), func(t *testing.T) {
			fakeClaudeHome(t, "read line\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
			t.Setenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "1000000")
			n := codexTestNode(t)
			n.dir = t.TempDir()
			withFakeHub(t, n)
			params := map[string]any{"launcher": "claude", "cwd": t.TempDir(), "text": "hello"}
			if main {
				params["is_main"] = true
			} else {
				params["session_id"] = "11111111-2222-3333-4444-555555555555"
				n.state.put(params["session_id"].(string), sessionState{IsMain: true})
			}
			raw, err := nodeCall(t, n, "assistant.send", params)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			defer n.stopAssistant(out.SessionID)
			p := n.procs[out.SessionID]
			// The same for the main conversation and another: the 1M declared in the environment is followed, nothing written.
			if p.compactIdleAt != 750_000 || envValue(p.cmd.Env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW") != "" {
				t.Fatalf("process limits: idle %d, env %v", p.compactIdleAt, p.cmd.Env)
			}
			st, _ := n.state.get(out.SessionID)
			if st.IsMain != main {
				t.Fatalf("stored scope %v", st.IsMain)
			}
			n.spare = p
			if main && n.adoptSpare(p.cwd, p.launcher, modelExecution{}) != nil {
				t.Fatal("a main process was adopted for an ordinary conversation")
			}
		})
	}
}

func TestAssistantCompactionSettingsAndSDK(t *testing.T) {
	n := assistantNode(t)
	n.dir = t.TempDir()
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"hooks":{"Stop":[]},"env":{"KEEP":"yes","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"200000","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := assistantCompaction{window: 400_000, capacity: 1_000_000, pin: true}
	c.ownSettings = `{"env":{"LAUNCHER":"yes"},"permissions":{"allow":["Read"]}}`
	path, err := n.assistantCompactionSettings("test-id", n.home, settings, c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := readLauncherSettings(path, n.home, n.home)
	if err != nil {
		t.Fatal(err)
	}
	env := s["env"].(map[string]any)
	if env["KEEP"] != "yes" || env["LAUNCHER"] != "yes" || env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "400000" || env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1000000" || s["hooks"] == nil || s["permissions"] == nil {
		t.Fatalf("merged settings %v", s)
	}
	start := sdkStart{settings: path, mcp: map[string]any{}, compaction: &c, env: map[string]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "200000", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "200000"}}
	opts, err := start.options("claude")
	if err != nil {
		t.Fatal(err)
	}
	env = opts["settings"].(map[string]any)["env"].(map[string]any)
	if env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "400000" || env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1000000" {
		t.Fatalf("SDK clamped policy back down: %v", env)
	}
	if mode, err := os.Stat(path); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("settings permissions: %v %v", mode, err)
	}
	// A launcher Persona does not manage: what its settings say stays as it is.
	path, err = n.assistantCompactionSettings("test-id", n.home, settings, assistantCompaction{window: 400_000, capacity: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if s, err = readLauncherSettings(path, n.home, n.home); err != nil {
		t.Fatal(err)
	}
	if env = s["env"].(map[string]any); env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "200000" || env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "200000" {
		t.Fatalf("unmanaged settings rewritten: %v", env)
	}
}

func TestAssistantCodexCapacity(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want int
	}{
		{[]string{"-c", "model_context_window=128000"}, 128000},
		{[]string{"--config=model_context_window=200_000"}, 200000},
		{[]string{"model_context_window=1000000\n[profiles.small]\nmodel_context_window=128000 # cap\n"}, 128000},
		{[]string{"-c", "model_auto_compact_token_limit=200000"}, 0},
	} {
		if got := codexCapacity(tt.args); got != tt.want {
			t.Fatalf("%q: %d, want %d", tt.args, got, tt.want)
		}
	}
}

// A conversation started through a shell launcher (not a managed one), the main one or another: Persona writes no
// compact window or context window (#3066). What the launcher exports reaches claude as it is, and nothing is added
// when it exports nothing (`ccc --model opus`-like: Claude Code compacts by the model). The idle threshold is 75% of
// the declared window, else the default until Claude Code reports one (TestAssistantIdleCompactFromWindow).
func TestAssistantShellLauncherCompaction(t *testing.T) {
	for _, tt := range []struct {
		name, rc  string
		auto, max string // what claude sees, "" for unset
		idle      int
	}{
		{"declares window and capacity", `function testproxy { export CLAUDE_CODE_MAX_CONTEXT_TOKENS=1000000 CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000; command claude "$@"; }`, "200000", "1000000", 150000},
		{"declared 200K backend", `function testproxy { export CLAUDE_CODE_MAX_CONTEXT_TOKENS=200000; command claude "$@"; }`, "", "200000", 150000},
		{"small backend", `function testproxy { export CLAUDE_CODE_MAX_CONTEXT_TOKENS=128000 CLAUDE_CODE_AUTO_COMPACT_WINDOW=128000; command claude "$@"; }`, "128000", "128000", 96000},
		{"declares a 1M window", `function testproxy { export CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000; command claude "$@"; }`, "1000000", "", 750000},
		{"gateway function declaring nothing", `function testproxy { ( export ANTHROPIC_BASE_URL=http://127.0.0.1:9; command claude --model opus "$@" ) }`, "", "", 150000},
	} {
		for _, main := range []bool{true, false} {
			t.Run(tt.name+"/main="+strconv.FormatBool(main), func(t *testing.T) {
				fakeClaudeHome(t, "env > \"$HOME/claude-env\"\nread line\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
				for _, k := range []string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS", "CLAUDE_CODE_AUTO_COMPACT_WINDOW"} {
					t.Setenv(k, "")
					os.Unsetenv(k)
				}
				home := os.Getenv("HOME")
				if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(tt.rc+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				n := codexTestNode(t)
				n.dir = t.TempDir()
				withFakeHub(t, n)
				p, err := n.spawnClaude("11111111-2222-3333-4444-555555555555", t.TempDir(), "testproxy", false, false, modelExecution{Model: "opus", IsMain: main})
				if err != nil {
					t.Fatal(err)
				}
				defer n.stopAssistant(p.id)
				settings, err := readLauncherSettings(settingsArg(p.cmd.Args), p.cwd, home)
				if err != nil {
					entries, _ := os.ReadDir(filepath.Join(n.dir, "launcher-settings"))
					t.Fatalf("%v; args=%q; settings files=%v", err, p.cmd.Args, entries)
				}
				env, _ := settings["env"].(map[string]any)
				for _, k := range []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_MAX_CONTEXT_TOKENS"} {
					if _, ok := env[k]; ok {
						t.Fatalf("Persona wrote %s into the start's settings: %v", k, env)
					}
					if envValue(p.cmd.Env, k) != "" {
						t.Fatalf("Persona put %s into the start's environment", k)
					}
				}
				if p.compactIdleAt != tt.idle {
					t.Fatalf("idle %d, want %d", p.compactIdleAt, tt.idle)
				}
				var seen []byte
				waitFor(t, "claude to write its environment", func() bool {
					seen, _ = os.ReadFile(filepath.Join(home, "claude-env"))
					return len(seen) > 0
				})
				lines := strings.Split(string(seen), "\n")
				for k, want := range map[string]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW": tt.auto, "CLAUDE_CODE_MAX_CONTEXT_TOKENS": tt.max} {
					if got := envValue(lines, k); got != want {
						t.Fatalf("claude saw %s=%q, want %q (the launcher's own)", k, got, want)
					}
				}
			})
		}
	}
}

// Nothing declared, the same for a Claude or Codex conversation, main or not: no compact window, no context window, no
// Codex compact limit. A managed launcher's declared window is passed, to Claude and to Codex (#3066).
func TestAssistantUndeclaredPolicy(t *testing.T) {
	fakeClaudeHome(t, "exit 0\n")
	t.Setenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	home := os.Getenv("HOME")
	rc := "function testproxy { command claude --model opus \"$@\"; }\nfunction testcodex { command codex \"$@\"; }\n" +
		"function testcodexown { command codex -c model_auto_compact_token_limit=123456 \"$@\"; }\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	n := codexTestNode(t)
	n.dir = t.TempDir()
	n.setManaged([]proto.ManagedLauncher{
		{Name: "gw200", Model: "m", CompactWindow: 200_000},
		{Name: "gwbare", Model: "m"},
		{Name: "codexgw", Agent: "codex", CompactWindow: 300_000},
		{Name: "codexbare", Agent: "codex"},
	})
	cwd := t.TempDir()
	for _, tt := range []struct {
		name, launcher string
		env            []string
		codex, idle    int
	}{
		{"claude, nothing declared", "testproxy", nil, 0, 150_000},
		{"codex, nothing declared", "testcodex", nil, 0, 150_000},
		{"codex launcher's own limit is its own", "testcodexown", nil, 0, 150_000},
		{"managed, nothing declared", "gwbare", nil, 0, 150_000},
		{"managed 200K", "gw200", []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000"}, 200_000, 150_000},
		{"managed codex 300K", "codexgw", []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW=300000"}, 300_000, 225_000},
		{"managed codex, nothing declared", "codexbare", nil, 0, 150_000},
	} {
		for _, main := range []bool{true, false} {
			t.Run(tt.name+"/main="+strconv.FormatBool(main), func(t *testing.T) {
				c := n.assistantCompactionPolicy(tt.launcher, cwd, "", modelExecution{Model: "opus", IsMain: main})
				if strings.Join(c.env(), " ") != strings.Join(tt.env, " ") || c.codexLimit() != tt.codex || c.idle() != tt.idle {
					t.Fatalf("policy %+v: env %q codex %d idle %d, want %q %d %d", c, c.env(), c.codexLimit(), c.idle(), tt.env, tt.codex, tt.idle)
				}
			})
		}
	}
}

// The idle threshold follows the window Claude Code reports at the end of a turn: 75% of it (a 200K model compacts at
// 150K while its cache is warm, a 1M one at 750K), or of the declared window when that is smaller. A subagent's or
// helper model's entry is not the conversation's window.
func TestAssistantIdleCompactFromWindow(t *testing.T) {
	result := func(usage string) string { return `{"type":"result","subtype":"success","modelUsage":` + usage + `}` }
	start := func(model string) string {
		return `{"type":"stream_event","event":{"type":"message_start","message":{"model":"` + model +
			`","usage":{"input_tokens":2,"cache_read_input_tokens":170000}}},"parent_tool_use_id":null}`
	}
	for _, tt := range []struct {
		name     string
		declared int
		lines    []string
		want     int
	}{
		{"1M model", 0, []string{start("claude-opus-5-5"), result(`{"claude-opus-5-5":{"inputTokens":2,"cacheReadInputTokens":170000,"contextWindow":1000000}}`)}, 750_000},
		{"200K model", 0, []string{start("small-model"), result(`{"small-model":{"inputTokens":2,"cacheReadInputTokens":170000,"contextWindow":200000}}`)}, 150_000},
		{"the conversation's model, not a helper's", 0, []string{start("claude-opus-5-5"), result(`{"claude-opus-5-5":{"inputTokens":2,"cacheReadInputTokens":170000,"contextWindow":1000000},"claude-haiku-4-5":{"inputTokens":900000,"contextWindow":200000}}`)}, 750_000},
		{"gateway renames the model: the one that read most", 0, []string{start("gw/opus"), result(`{"claude-opus-5-5":{"inputTokens":2,"cacheReadInputTokens":170000,"contextWindow":1000000},"claude-haiku-4-5":{"inputTokens":3000,"contextWindow":200000}}`)}, 750_000},
		{"no window reported: the default stands", 0, []string{start("x"), result(`{"x":{"inputTokens":2}}`), `{"type":"result","subtype":"success"}`}, 150_000},
		{"declared window below the model's", 200_000, []string{start("claude-opus-5-5"), result(`{"claude-opus-5-5":{"inputTokens":2,"contextWindow":1000000}}`)}, 150_000},
		{"model below the declared window", 1_000_000, []string{start("small-model"), result(`{"small-model":{"inputTokens":2,"contextWindow":200000}}`)}, 150_000},
		{"a later turn's window", 0, []string{start("m"), result(`{"m":{"inputTokens":2,"contextWindow":200000}}`), start("m"), result(`{"m":{"inputTokens":2,"contextWindow":1000000}}`)}, 750_000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := assistantNode(t)
			const id = "11111111-2222-3333-4444-555555555555"
			c := assistantCompaction{window: tt.declared}
			p := &assistantProc{id: id, agent: "claude", stdin: &stdinBuf{}, compactIdleAt: c.idle(), declaredWindow: c.declared()}
			n.procs[id] = p
			n.readAssistant(p, strings.NewReader(strings.Join(tt.lines, "\n")+"\n"))
			p.mu.Lock()
			got := p.compactIdleAt
			p.mu.Unlock()
			if got != tt.want {
				t.Fatalf("idle threshold %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAssistantSDKCompactionStart(t *testing.T) {
	f := sdkTest(t)
	f.n.setManaged([]proto.ManagedLauncher{{Name: "gw", Model: "big", CompactWindow: 200000, Models: []proto.ProviderModel{{ID: "big", ContextWindow: 1000000}}}})
	execution := sdkExec(proto.AssistantRuntimeAgentSDK)
	execution.IsMain = true
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "gw", "hello", nil, "", "", execution)
	if err != nil {
		t.Fatal(err)
	}
	opts := f.start(t)
	env := opts["settings"].(map[string]any)["env"].(map[string]any)
	// The launcher's declared 200000, for the main conversation too; its model's 1M as the context window.
	if env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "200000" || env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "1000000" || f.proc(id).compactIdleAt != 150000 {
		t.Fatalf("SDK start: %v, idle %d", env, f.proc(id).compactIdleAt)
	}
}
