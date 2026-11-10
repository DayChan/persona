package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// assistantCompaction is what a conversation's launcher declares about compaction (#3066). Persona sets no target of
// its own: what is not declared is left to the agent, Claude Code by the model's own window, Codex by its model's
// default.
type assistantCompaction struct {
	// window: the declared automatic compaction window (CLAUDE_CODE_AUTO_COMPACT_WINDOW, a managed launcher's
	// CompactWindow); capacity: the declared context window (CLAUDE_CODE_MAX_CONTEXT_TOKENS, MaxContext, the provider
	// model's ContextWindow, Codex's model_context_window). 0: not declared.
	window, capacity int
	// pin: Persona writes them for the start. Only a managed launcher: its environment is Persona's, and an assistant
	// start leaves its compact window out of the launch (perStartEnv). Any other launcher's own environment, settings
	// and the user's settings reach the agent as they are, so nothing is written over them.
	pin         bool
	ownSettings string
}

// codexLimit is the model_auto_compact_token_limit a Codex start passes: a managed launcher's declared window, else none
// (the launcher's own -c and config.toml reach Codex as they are).
func (c assistantCompaction) codexLimit() int {
	if !c.pin {
		return 0
	}
	return c.window
}

// declared is the window the launcher holds the conversation to, 0 when it declares none.
func (c assistantCompaction) declared() int { return smallerCapacity(c.window, c.capacity) }

// idle is Persona's idle /compact threshold before the agent reports its model's window: 75% of the declared window,
// else assistantIdleCompact (assistantProc.followWindow moves it once a window is reported).
func (c assistantCompaction) idle() int {
	if d := c.declared(); d > 0 {
		return idleThreshold(d)
	}
	return assistantIdleCompact
}

// idleThreshold: an idle conversation is compacted at 75% of its window, below where the agent compacts on its own
// (Claude Code about 33K short of the window) and early enough to be worth it while the cache is warm.
func idleThreshold(window int) int { return window / 4 * 3 }

func smallerCapacity(a, b int) int {
	if b > 0 && (a <= 0 || b < a) {
		return b
	}
	return a
}

func contextLimit(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	if n < 0 {
		return 0
	}
	return n
}

func providerCapacity(l proto.ManagedLauncher, model string) int {
	if model == "" {
		model = l.Model
	}
	pm, _ := providerModel(l, model)
	capacity := smallerCapacity(pm.ContextWindow, l.MaxContext)
	for _, kv := range launcherEnvVars(l.Env) {
		if kv[0] == "CLAUDE_CODE_MAX_CONTEXT_TOKENS" {
			capacity = smallerCapacity(capacity, contextLimit(kv[1]))
		}
	}
	return smallerCapacity(capacity, codexCapacity(l.Args))
}

var codexContextLimit = lazyre.New(`(?m)^\s*model_context_window\s*=\s*([0-9][0-9_]*)\s*(?:#.*)?$`)

func codexCapacity(args []string) int {
	capacity := 0
	for i, arg := range args {
		if (arg == "-c" || arg == "--config") && i+1 < len(args) {
			arg = args[i+1]
		} else if v, ok := strings.CutPrefix(arg, "--config="); ok {
			arg = v
		}
		for _, m := range codexContextLimit.FindAllStringSubmatch(arg, -1) {
			capacity = smallerCapacity(capacity, contextLimit(strings.ReplaceAll(m[1], "_", "")))
		}
	}
	return capacity
}

// settingsCompaction is what a Claude settings file's env declares: the auto-compact window and the context window.
func settingsCompaction(arg, cwd, home string) (window, capacity int) {
	if arg == "" {
		return 0, 0
	}
	s, err := readLauncherSettings(arg, cwd, home)
	if err != nil {
		return 0, 0
	}
	env, _ := s["env"].(map[string]any)
	value := func(k string) int {
		switch v := env[k].(type) {
		case string:
			return contextLimit(v)
		case float64:
			return max(int(v), 0)
		}
		return 0
	}
	return value("CLAUDE_CODE_AUTO_COMPACT_WINDOW"), value("CLAUDE_CODE_MAX_CONTEXT_TOKENS")
}

// assistantCompactionPolicy is what launcher declares about compaction for a conversation in cwd (settings: Persona's
// own settings file for the start), the same for the main conversation and every other (#3066). Several declarations
// of one value: the smallest counts (the conservative reading where a profile, the environment and settings disagree).
func (n *Node) assistantCompactionPolicy(launcher, cwd, settings string, execution modelExecution) assistantCompaction {
	var c assistantCompaction
	declare := func(window, capacity int) {
		c.window, c.capacity = smallerCapacity(c.window, window), smallerCapacity(c.capacity, capacity)
	}
	isCodex := n.agentOf(launcher) == "codex"
	configDir, codexHome := os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("CODEX_HOME")
	if dir := n.claudeStartDir.Load(); dir != nil && *dir != "" {
		configDir = proto.ExpandHome(*dir, n.home)
	}
	if l, managed := n.managedLauncher(launcher); managed {
		c.pin = true
		if isCodex {
			declare(l.CompactWindow, providerCapacity(l, execution.Model))
			for _, kv := range launcherEnvVars(l.Env) {
				if kv[0] == "CODEX_HOME" {
					codexHome = kv[1]
				}
			}
		} else {
			// What the launch itself would set (CompactWindow, else the model's window; its env last), and its settings.
			env := providerEnv(l, execution.Model, "", true)
			declare(contextLimit(env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]), providerCapacity(l, execution.Model))
			c.ownSettings = settingsArg(l.Args)
			declare(settingsCompaction(c.ownSettings, cwd, n.home))
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if p, err := n.launcherProbe(ctx, launcher); err == nil {
			declare(contextLimit(p.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]), contextLimit(p.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]))
			declare(0, codexCapacity(p.Args))
			c.ownSettings = settingsArg(p.Args)
			declare(settingsCompaction(c.ownSettings, cwd, n.home))
			if dir := p.Env["CLAUDE_CONFIG_DIR"]; dir != "" {
				configDir = dir
			}
			if dir := p.Env["CODEX_HOME"]; dir != "" {
				codexHome = dir
			}
		}
		if !isCodex {
			declare(contextLimit(os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW")), contextLimit(os.Getenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS")))
			if configDir == "" {
				configDir = filepath.Join(n.home, ".claude")
			}
			declare(settingsCompaction(filepath.Join(configDir, "settings.json"), cwd, n.home))
			for _, file := range []string{"settings.json", "settings.local.json"} {
				declare(settingsCompaction(filepath.Join(cwd, ".claude", file), cwd, n.home))
			}
		}
	}
	if isCodex {
		if codexHome == "" {
			codexHome = n.codexHome()
		}
		if b, err := os.ReadFile(filepath.Join(codexHome, "config.toml")); err == nil {
			declare(0, codexCapacity([]string{string(b)}))
		}
	}
	declare(settingsCompaction(settings, cwd, n.home))
	if c.window > 0 && c.capacity > 0 && c.capacity < c.window {
		c.window = c.capacity // no compaction window past the context window
	}
	return c
}

// claudeModelUsage is one model's entry in a stream-json result's modelUsage: what this process read with it and the
// context window Claude Code holds it to.
type claudeModelUsage struct {
	Input         int `json:"inputTokens"`
	CacheRead     int `json:"cacheReadInputTokens"`
	CacheCreate   int `json:"cacheCreationInputTokens"`
	ContextWindow int `json:"contextWindow"`
}

// reportedWindow is the context window Claude Code holds the conversation's model to, 0 when it reports none: the
// entry of model (the model of the conversation's last reply), else the entry that read the most context (a subagent
// or a helper model reads less than the conversation).
func reportedWindow(usage map[string]claudeModelUsage, model string) int {
	if u, ok := usage[model]; ok && model != "" {
		return u.ContextWindow
	}
	most, window := -1, 0
	for _, u := range usage {
		read := u.Input + u.CacheRead + u.CacheCreate
		if read > most || (read == most && u.ContextWindow < window) {
			most, window = read, u.ContextWindow
		}
	}
	return window
}

// followWindow sets the idle threshold from the window the agent reports for the conversation's model: 75% of it, or of
// the declared window when that is smaller. It reports whether the threshold changed; p.mu is held.
func (p *assistantProc) followWindow(reported int) bool {
	window := smallerCapacity(p.declaredWindow, reported)
	if window <= 0 {
		return false
	}
	idle := idleThreshold(window)
	if idle == p.compactIdleAt {
		return false
	}
	p.compactIdleAt = idle
	return true
}

// env is what a start writes into the agent's environment and merged settings: only a managed launcher's declared
// values (pin), none when it declares none.
func (c assistantCompaction) env() []string {
	if !c.pin {
		return nil
	}
	var env []string
	if c.window > 0 {
		env = append(env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW="+strconv.Itoa(c.window))
	}
	if c.capacity > 0 {
		env = append(env, "CLAUDE_CODE_MAX_CONTEXT_TOKENS="+strconv.Itoa(c.capacity))
	}
	return env
}

func (n *Node) assistantCompactionSettings(id, cwd, settings string, c assistantCompaction) (string, error) {
	s := map[string]any{}
	if settings != "" {
		var err error
		s, err = readLauncherSettings(settings, cwd, n.home)
		if err != nil {
			if !os.IsNotExist(err) {
				return "", err
			}
			s = map[string]any{}
		}
	}
	if c.ownSettings != "" {
		theirs, err := readLauncherSettings(c.ownSettings, cwd, n.home)
		if err != nil {
			return "", err
		}
		mergedEnv := map[string]any{}
		if env, ok := theirs["env"].(map[string]any); ok {
			for k, v := range env {
				mergedEnv[k] = v
			}
		}
		if env, ok := s["env"].(map[string]any); ok {
			for k, v := range env {
				mergedEnv[k] = v
			}
		}
		s = mergeClaudeSettings(theirs, s)
		s["env"] = mergedEnv
	}
	env, _ := s["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	for _, kv := range c.env() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	s["env"] = env
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	dir := n.dir
	if dir == "" {
		dir = filepath.Join(n.home, ".persona")
	}
	return writeSettings(dir, "assistant-compact-"+id+".json", b)
}
