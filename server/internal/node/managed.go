package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Launchers kept by the hub ("Settings › Model", docs/integrations/launchers.md): the manager names the ones in this
// computer's scope on connect and on every change (launchers.catalog), the secrets of each withheld; they come when the
// node needs them (launchers.pull) and stay in this process's memory. A manager from before the catalog sends them all
// with their secrets (launchers.managed). A managed launcher runs as `personad launch <name> …`, which asks this node
// over the local socket for its settings and execs claude with them: the key is never written to a file, typed into a
// terminal or put on a command line.

type managedLaunchers struct {
	mu   sync.Mutex
	byID map[string]proto.ManagedLauncher // the catalog: a withheld entry has no secrets
	full map[string]proto.ManagedLauncher // what was pulled of the withheld ones, at their Rev
	// pull lets one pull of a name go out at a time (the others wait and use it).
	pull sync.Map // name → *sync.Mutex
}

// setManaged is launchers.managed (an older manager): every entry with its secrets.
func (n *Node) setManaged(list []proto.ManagedLauncher) {
	m := map[string]proto.ManagedLauncher{}
	for _, l := range list {
		// An API provider (kind api) runs claude too, on its messages endpoint (#2455); the hub sends none on another.
		if l.Kind == "api" && l.API != "" && l.API != "messages" {
			continue
		}
		l.Withheld = false
		m[l.Name] = l
	}
	n.managed.mu.Lock()
	n.managed.byID, n.managed.full = m, nil
	n.managed.mu.Unlock()
}

// setCatalog is launchers.catalog: the providers in this computer's scope. What was pulled stays while the provider is
// still named at the same Rev; a pushed one (delivery push) is fetched right away, in the background.
func (n *Node) setCatalog(list []proto.ManagedLauncher) {
	m := map[string]proto.ManagedLauncher{}
	var prefetch []string
	for _, l := range list {
		if l.Kind == "api" && l.API != "" && l.API != "messages" {
			continue
		}
		m[l.Name] = l
		if l.Withheld && l.Pushed() {
			prefetch = append(prefetch, l.Name)
		}
	}
	n.managed.mu.Lock()
	full := map[string]proto.ManagedLauncher{}
	for name, f := range n.managed.full {
		if c, ok := m[name]; ok && c.Withheld && c.Rev == f.Rev {
			full[name] = f
		}
	}
	n.managed.byID, n.managed.full = m, full
	n.managed.mu.Unlock()
	if len(prefetch) > 0 {
		go func() {
			for _, name := range prefetch {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := n.ensureManaged(ctx, name); err != nil {
					n.logf("launcher %s: not kept on this computer: %v", name, err)
				}
				cancel()
			}
		}()
	}
}

// managedLauncher is the managed launcher by name: with its secrets once they are here (a withheld one before its
// pull has none; ensureManaged fetches them).
func (n *Node) managedLauncher(name string) (proto.ManagedLauncher, bool) {
	n.managed.mu.Lock()
	defer n.managed.mu.Unlock()
	l, ok := n.managed.byID[name]
	if ok && l.Withheld {
		if f, pulled := n.managed.full[name]; pulled && f.Rev == l.Rev {
			return f, true
		}
	}
	return l, ok
}

// managedCatalog is the entry as the catalog has it (never what was pulled): what an execution's fingerprint is made of,
// the same before and after a pull.
func (n *Node) managedCatalog(name string) (proto.ManagedLauncher, bool) {
	n.managed.mu.Lock()
	defer n.managed.mu.Unlock()
	l, ok := n.managed.byID[name]
	return l, ok
}

// errManagerAway: a withheld provider's secrets were never fetched and the manager cannot be asked now.
func errManagerAway(name string) error {
	return fmt.Errorf("cannot reach the hub to fetch the settings of %s; they come when it is back "+
		"(for launches while the hub is away, turn on “Keep a copy on each computer” in the provider's Devices)", name)
}

// ensureManaged fetches a withheld provider's secrets from the manager when this process does not have them at its
// Rev; nil for anything else (a launcher that is not managed, one that came with its secrets).
func (n *Node) ensureManaged(ctx context.Context, name string) error {
	l, ok := n.managedCatalog(name)
	if !ok || !l.Withheld {
		return nil
	}
	mu, _ := n.managed.pull.LoadOrStore(name, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if got, _ := n.managedLauncher(name); !got.Withheld {
		return nil // another pull brought them meanwhile
	}
	peer := n.peer
	if peer == nil {
		return errManagerAway(name)
	}
	params := map[string]any{"name": name}
	if l.SkipsOwnService() { // the manager decides with what this computer has of its own
		st := n.ownService(ctx, l.AgentType())
		params["own_service"] = st
	}
	var res struct {
		Launcher proto.ManagedLauncher `json:"launcher"`
	}
	if err := peer.Call(ctx, "launchers.pull", params, &res); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code != "" {
			return re // the manager's refusal (out of scope, own service), in the user's language
		}
		if errors.Is(err, rpc.ErrClosed) || errors.Is(err, context.DeadlineExceeded) {
			return errManagerAway(name)
		}
		return fmt.Errorf("fetch the settings of %s: %w", name, err)
	}
	f := res.Launcher
	if f.Name != name {
		return fmt.Errorf("fetch the settings of %s: the hub answered for %q", name, f.Name)
	}
	f.Withheld, f.Rev = false, l.Rev
	n.managed.mu.Lock()
	defer n.managed.mu.Unlock()
	if c, still := n.managed.byID[name]; still && c.Withheld && c.Rev == l.Rev {
		if n.managed.full == nil {
			n.managed.full = map[string]proto.ManagedLauncher{}
		}
		n.managed.full[name] = f
	}
	return nil
}

// launcherCommand is what starts claude for a launcher name in a shell: the name itself (a ~/.zshrc function, or
// claude), or `personad launch <name>` for a managed one. Plain claude gets the machine's claude_start_dir as its
// CLAUDE_CONFIG_DIR when one is set: a computer whose claude is logged in under a profile directory, not ~/.claude,
// would otherwise answer every agent with "Not logged in" (#1459).
func (n *Node) launcherCommand(name string) string { return n.launcherCommandQ(name, shellQuote) }

// launcherCommandQ is launcherCommand quoting with q (herdrresume.go quotes without apostrophes).
func (n *Node) launcherCommandQ(name string, q func(string) string) string {
	if _, ok := n.managedLauncher(name); ok {
		return q(n.bin) + " launch " + name
	}
	if name == "claude" {
		if d := n.claudeStartDir.Load(); d != nil && *d != "" {
			return "CLAUDE_CONFIG_DIR=" + shellDirQ(*d, q) + " " + name
		}
	}
	return name
}

// shellDirQ quotes a directory for a shell command with q, keeping a leading ~/ as "$HOME"/ so it is the home of the
// shell that runs it.
func shellDirQ(p string, q func(string) string) string {
	if p == "~" {
		return `"$HOME"`
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return `"$HOME"/` + q(rest)
	}
	return q(p)
}

func managedEnv(l proto.ManagedLauncher) []string {
	switch l.AgentType() {
	case "codex":
		var env []string
		if l.BaseURL != "" {
			env = append(env, "OPENAI_BASE_URL="+l.BaseURL)
		}
		if l.APIKey != "" {
			env = append(env, "OPENAI_API_KEY="+l.APIKey)
		}
		return withLauncherEnv(env, l.Env)
	case "agy":
		var env []string
		if l.BaseURL != "" {
			env = append(env, "GOOGLE_GEMINI_BASE_URL="+l.BaseURL)
		}
		if l.APIKey != "" {
			env = append(env, "GEMINI_API_KEY="+l.APIKey)
		}
		return withLauncherEnv(env, l.Env)
	}
	return envList(providerEnv(l, "", "", false))
}

// withLauncherEnv is env (KEY=value entries) with a launcher's own environment over it (#5953), for an agent that is no
// Claude Code (providerEnv does that for one).
func withLauncherEnv(env []string, raw string) []string {
	for _, kv := range launcherEnvVars(raw) {
		env = slices.DeleteFunc(env, func(e string) bool { return strings.HasPrefix(e, kv[0]+"=") })
		env = append(env, kv[0]+"="+kv[1])
	}
	return env
}

// envNames are the names env (KEY=value entries) sets.
func envNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		out = append(out, k)
	}
	return out
}

// artifactsOff turns Claude Code's Artifact tool off for a claude a managed launcher runs. Artifacts publish to
// claude.ai, which a gateway with an API key is not. Claude Code turns the tool on by default (since 2.1.26x), and an
// interactive session then sends the Artifact skills in its system message; a gateway that cannot carry them refuses
// the first request ("400 request is not representable on this edge: Claude Code Artifacts are enabled"), so every
// Agent started in herdr through it failed on its first turn while `claude -p` went on working. The environment
// variable reaches `personad launch` (headless runs) and the settings env alike; the settings file also says
// enableArtifact: false (mergeManagedSettings): off in --settings wins over the other settings sources.
const artifactsOff = "CLAUDE_CODE_DISABLE_ARTIFACT=1"

// managedSettingsEnv is managedEnv as a settings "env" map (providerEnv). Settings env wins over the process env, so
// the user's ~/.claude/settings.json (another gateway, another token) would otherwise replace a managed launcher's.
func managedSettingsEnv(l proto.ManagedLauncher) map[string]string {
	return pinnedEnv(providerEnv(l, "", "", false))
}

// claudeExecutable finds claude: PATH first, then the usual install spots. A node started by launchd (macOS) has a
// minimal PATH that misses /opt/homebrew/bin, and nothing in `personad launch`'s own path adds it.
func agentExecutable(bin string) (string, error) {
	if p, err := exec.LookPath(bin); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", bin), "/opt/homebrew/bin/" + bin,
		"/usr/local/bin/" + bin, filepath.Join(home, ".claude", "local", bin)} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH or the usual install locations", bin)
}

func claudeExecutable() (string, error) { return agentExecutable("claude") }

// Launch is `personad launch <name> [claude args…]`: the managed launcher's settings from the local node, then claude.
func Launch(ctx context.Context, name string, args []string) error {
	line, err := nodeclient.Dial(ctx, launcherEnvRequest(name))
	if err != nil {
		return fmt.Errorf("personad launch %s: %w", name, err)
	}
	var resp struct {
		Agent string   `json:"agent"`
		Model string   `json:"model"`
		Env   []string `json:"env"`
		Clear []string `json:"clear"` // what to drop from the shell's env first (envClearFor)
		Args  []string `json:"args"`
		Error string   `json:"error"`
		// for a provider whose bypass list joins the inherited ones (mergesNoProxy): the user's settings env's lists, which
		// come before the shell's. An older node does not send it: the provider's list alone.
		NoProxyMerge map[string]string `json:"no_proxy_merge"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New("personad launch " + name + ": " + resp.Error)
	}
	agent := resp.Agent
	if agent == "" {
		agent = "claude"
	}
	exe, err := agentExecutable(agent)
	if err != nil {
		return fmt.Errorf("personad launch: %w", err)
	}
	var env []string
	if agent == "claude" {
		if len(resp.Clear) == 0 {
			resp.Clear = legacyEnvClear
		}
		resp.Clear, resp.Env = launchEnvFor(resp.Clear, resp.Env, os.Getenv("PERSONA_ASSISTANT") == "1")
		if resp.NoProxyMerge != nil { // the inherited bypasses still hold for what claude runs
			resp.Env = withInheritedNoProxy(resp.Env, func(k string) string { return cmp.Or(resp.NoProxyMerge[k], os.Getenv(k)) })
		}
		env = append(withoutEnv(os.Environ(), resp.Clear), resp.Env...)
		// A proxy the shell still names that no longer answers would hold every request (deadproxy.go, #5953).
		env = withoutDeadProxies(env, resp.Env, envValue(resp.Env, "ANTHROPIC_BASE_URL"),
			func(msg string) { fmt.Fprintf(os.Stderr, "personad launch %s: %s\n", name, msg) })
		if err := approveClaudeAPIKey(env); err != nil { // apikeyapproval.go: else Claude asks whether to use the key, default No
			fmt.Fprintf(os.Stderr, "personad launch %s: could not mark the API key as approved for Claude Code: %v\n", name, err)
		}
	} else {
		// What the launcher sets replaces the shell's: with both, the agent would read the shell's (the first).
		env = append(withoutEnv(os.Environ(), envNames(resp.Env)), resp.Env...)
	}
	hasModelArg := false
	hasSettingsArg := false
	for _, arg := range args {
		if arg == "-m" || arg == "--model" || strings.HasPrefix(arg, "--model=") {
			hasModelArg = true
			resp.Args = withoutModelArgs(resp.Args)
		}
		if arg == "--settings" || strings.HasPrefix(arg, "--settings=") {
			hasSettingsArg = true
		}
	}
	for _, arg := range resp.Args {
		if arg == "-m" || arg == "--model" || strings.HasPrefix(arg, "--model=") {
			hasModelArg = true
		}
		if arg == "--settings" || strings.HasPrefix(arg, "--settings=") {
			hasSettingsArg = true
		}
	}
	launchArgs := append([]string{agent}, resp.Args...)
	if agent == "codex" && resp.Model != "" && !hasModelArg {
		launchArgs = append(launchArgs, "-m", resp.Model)
	} else if agent == "agy" {
		if resp.Model != "" && !hasModelArg {
			launchArgs = append(launchArgs, "--model", resp.Model)
		}
		for _, kv := range resp.Env {
			if strings.HasPrefix(kv, "GEMINI_API_KEY=") && len(kv) > len("GEMINI_API_KEY=") && !hasSettingsArg {
				launchArgs = append(launchArgs, "--settings", `{"modelProvider":"gemini"}`)
				break
			}
		}
	}
	exe, argv, env, _ := launchExec(agent, exe, append(launchArgs, args...), env)
	return osproc.Exec(exe, argv, env)
}

// launchExec is what `personad launch` execs (exe, argv, env; argv[0] is the agent's name): in the agent sandbox when
// it is on (agentsandbox.go), Claude and Codex only when their own sandbox is sure to be off, another agent not. Not
// wrapped, they come back as they are, and the pane says why.
func launchExec(agent, exe string, argv, env []string) (string, []string, []string, sandbox.AgentStatus) {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	rest := argv[1:]
	inner := func() string {
		switch agent {
		case "claude":
			return claudeInner(rest, env, cwd, home, nil)
		case "codex":
			return codexInner(append([]string{"codex"}, rest...), env, cwd, home)
		}
		return "这类 agent（" + agent + "）的自带沙箱还没核对过，先不包"
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "personad launch: "+format+"\n", a...) }
	wrapped, wenv, st := wrapAgent("launch "+agent, home, identity.DataDir(home), cwd, settingsArgs(rest), inner, append([]string{exe}, rest...), env, logf)
	if wrapped[0] == exe {
		return exe, argv, env, st
	}
	return wrapped[0], wrapped, wenv, st
}

// launcherEnv answers `personad launch` on the local socket: a withheld provider's secrets are fetched first.
func (n *Node) launcherEnv(name string) map[string]any {
	l, ok := n.managedLauncher(name)
	if !ok {
		return map[string]any{"error": "no launcher " + name + " in Persona (设置 › 模型), or the hub has not sent it yet"}
	}
	if l.Withheld {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := n.ensureManaged(ctx, name)
		cancel()
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		if l, _ = n.managedLauncher(name); l.Withheld {
			return map[string]any{"error": errManagerAway(name).Error()}
		}
	}
	out := map[string]any{"env": managedEnv(l), "clear": envClearFor(l), "args": l.Args, "agent": l.AgentType(), "model": l.Model}
	if mergesNoProxy(l) {
		out["no_proxy_merge"] = n.userNoProxy(l)
	}
	return out
}

var secretValue = lazyre.New(`(?i)((?:key|token|secret|password|passwd|auth)[A-Za-z0-9_]*\s*=\s*)("[^"]*"|'[^']*'|\S+)`)

// launcherDefinition is a ~/.zshrc launcher's function body (a wrapper script's text, pathlaunchers.go), for its page
// in "Settings › Model", with anything that looks like a key or token replaced by •••.
func (n *Node) launcherDefinition(ctx context.Context, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("invalid launcher %q", name)
	}
	if path := n.scriptLauncher(ctx, name); path != "" {
		return scriptText(path, n.home)
	}
	out, err := loginShell(ctx, "whence -f "+name+" 2>/dev/null || type "+name).Output()
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("no launcher %s on this machine", name)
	}
	return redactSecrets(strings.TrimSpace(string(out))), nil
}

// tokenLike: a literal that looks like a key wherever it sits (an argument to _claude_proxy, "Bearer …"): a known
// prefix, or a long run of letters and digits with no path or URL in it.
var tokenLike = lazyre.New(`Bearer\s+[^\s"']+|\b(?:sk|vk|at|ak|pk|rk|plat|trxp_ak|ghp|gho|xox[bp])[-_][A-Za-z0-9._+/=-]{8,}|[A-Za-z0-9_+=-]{28,}`)

func redactSecrets(s string) string {
	s = tokenLike.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "Bearer") {
			return "Bearer •••"
		}
		hasDigit, hasLetter := strings.ContainsAny(m, "0123456789"), strings.ContainsAny(strings.ToLower(m), "abcdefghijklmnopqrstuvwxyz")
		if len(m) >= 28 && !(hasDigit && hasLetter) {
			return m // a long word, not a key
		}
		if len(m) >= 28 && strings.Count(m, "_") > 3 && !strings.ContainsAny(m, "0123456789") {
			return m
		}
		return "•••"
	})
	s = secretValue.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretValue.FindStringSubmatch(m)
		v := sub[2]
		// A value read from a file or another variable shows where it comes from, not a secret.
		if strings.Contains(v, "$") || strings.Contains(v, "<") {
			return m
		}
		return sub[1] + "•••"
	})
	return maskNamed(s)
}

// maskNamed masks a value a name marks as secret in JSON ("ANTHROPIC_AUTH_TOKEN": "…", an inline --settings) and in
// a header ("x-api-key: …"), and a URL's user and password (a proxy's).
func maskNamed(s string) string {
	s = secretJSON.ReplaceAllString(s, `$1$2"•••$2"`)
	s = secretHeader.ReplaceAllString(s, "$1•••")
	return urlUser.ReplaceAllString(s, "$1•••@")
}

// maskSecrets is redactSecrets without its guess by length: for values a page shows as they are (a launcher's
// arguments and settings, where a long model id is no key). A key by its form (a known prefix, after Bearer) or by its
// name (KEY=…, JSON, a header) is masked.
func maskSecrets(s string) string {
	s = knownToken.ReplaceAllStringFunc(s, func(m string) string {
		sub := knownToken.FindStringSubmatch(m)
		if strings.HasPrefix(sub[2], "Bearer") {
			return sub[1] + "Bearer •••"
		}
		return sub[1] + "•••"
	})
	s = secretValue.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretValue.FindStringSubmatch(m)
		if strings.Contains(sub[2], "$") || strings.Contains(sub[2], "<") {
			return m
		}
		return sub[1] + "•••"
	})
	return maskNamed(s)
}

// knownToken: Bearer and a known key prefix, not inside a path or a name (settings.ak-prod.json, ~/work/sk-tools).
var knownToken = lazyre.New(`(^|[^A-Za-z0-9./_-])(Bearer\s+[^\s"']+|(?:sk|vk|at|ak|pk|rk|plat|trxp_ak|ghp|gho|xox[bp])[-_][A-Za-z0-9._+/=-]{8,})`)

var urlUser = lazyre.New(`(://)[^/@\s]+@`)

var secretJSON = lazyre.New(`(?i)(\\?"[A-Za-z0-9_-]*(?:key|token|secret|password|passwd|auth)[A-Za-z0-9_-]*\\?"\s*:\s*)(\\?)"[^"\\]*\\?"`)

var secretHeader = lazyre.New(`(?i)\b((?:x-)?[a-z0-9-]*(?:api-key|token|secret|password|authorization|auth)\s*:[ \t]*)([^\s"',;\\]+)`)
