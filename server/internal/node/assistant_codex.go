package node

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/osproc"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The assistant on a Codex launcher runs `codex app-server` (Codex's JSON-RPC server for IDEs, newline-delimited JSON
// on stdio; protocol: `codex app-server generate-ts`) instead of claude -p. One app-server per conversation, one
// thread in it: the thread id is the session id, its rollout is the transcript (transcript/codex.go), so the app
// shows it like any other conversation. docs/assistant/assistant.md "Codex".
//
// What the node does in between, compared with the Claude version:
//   - messages: turn/start when idle, turn/steer while a turn runs (Claude's queued messages are taken up mid-turn too);
//   - live text: item/agentMessage/delta → session.delta;
//   - stop: turn/interrupt (the process stays);
//   - approvals come to the node as requests (commands, file changes, MCP tool calls, permissions, request_user_input):
//     the Feishu gate first (larkCheck), Persona's own MCP tools pass, the rest by the conversation's mode, asking
//     the phone like Claude's PermissionRequest hook does. Full access uses the native never policy;
//     connector proxies and gated CLI wrappers still enforce their independent business confirmations.

// codexToolTimeout: ask_user waits for the phone (up to nodeclient.HookTimeout); Codex's default MCP tool timeout is 60 s.
const codexToolTimeout = 86400

// codexStartTimeout bounds the app-server's startup handshake (initialize, thread/start or thread/resume).
const codexStartTimeout = 90 * time.Second

type codexConn struct {
	n            *Node
	p            *assistantProc
	key          string // what the persona MCP server reports as its session until the thread exists (resolveAssistantKey)
	memDir       string // the assistant's memory, shared with Claude (codexMemoryPrompt)
	nextID       int64
	writeMessage func([]byte) error
	ended        <-chan struct{}
	onMessage    func(codexMessage)
	// nameGates tells the gate proxy the thread's id once there is one: the process's addresses were opened before it
	// (assistantLarkEnvNamed). nil when the id was known at the start.
	nameGates func(id string)

	mu       sync.Mutex
	pending  map[int64]chan codexReply
	asks     map[string]context.CancelFunc // server request id → the phone question waiting for it
	turnID   string                        // the turn running, "" when idle
	files    map[string][]string           // fileChange item id → paths, for the approval card
	closed   bool
	threadID string
}

type codexReply struct {
	Result json.RawMessage
	Err    *codexError
}

type codexError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *codexError) Error() string { return fmt.Sprintf("codex: %s (%d)", e.Message, e.Code) }

// codexMessage is any line from the app-server: a response (id + result / error), a notification (method + params)
// or a request to us (id + method + params).
type codexMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *codexError     `json:"error"`
}

func (c *codexConn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if c.writeMessage != nil {
		return c.writeMessage(b)
	}
	return c.p.write(b)
}

// call sends a request and waits for its response (or the process ending, or ctx).
func (c *codexConn) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("codex app-server exited")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan codexReply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	ended := c.ended
	if ended == nil {
		ended = c.p.exited
	}
	select {
	case r := <-ch:
		if r.Err != nil {
			return r.Err
		}
		if out != nil && len(r.Result) > 0 {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	case <-ended:
		return errors.New("codex app-server exited")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *codexConn) reply(id json.RawMessage, result any) {
	_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *codexConn) replyError(id json.RawMessage, code int, msg string) {
	_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func (c *codexConn) turn() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turnID
}

// codexArgs are app-server's own flags: the persona MCP server (its session is the conversation's key until the
// thread exists) with a timeout long enough for ask_user, nothing else; sandbox and approvals go with each thread
// and turn.
func codexArgs(bin, key string, windows ...int) []string {
	return codexArgsForHome(bin, key, "", windows...)
}

func codexArgsForHome(bin, key, home string, windows ...int) []string {
	window := fmt.Sprint(assistantCompactWindow)
	if len(windows) > 0 {
		window = fmt.Sprint(windows[0])
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) } // a JSON string is a TOML basic string
	mcpEnv := "{PERSONA_ASSISTANT=" + q("1") + ",PERSONA_SESSION_ID=" + q(key)
	if home != "" {
		mcpEnv += ",PERSONA_HOME=" + q(home)
	}
	mcpEnv += "}"
	return compat.CodexMCPAliases([]string{
		"-c", "mcp_servers.persona.command=" + q(bin),
		"-c", `mcp_servers.persona.args=["mcp"]`,
		"-c", "mcp_servers.persona.env=" + mcpEnv,
		"-c", fmt.Sprintf("mcp_servers.persona.tool_timeout_sec=%d", codexToolTimeout),
		"-c", "mcp_servers.persona.startup_timeout_sec=30",
		// Commands get the process's environment like Claude's Bash does: the default excludes (*KEY*, *TOKEN*…)
		// would strip the credentials of a gate that still puts them there (auth env, gateproxy.go), and its command
		// would fail.
		"-c", "shell_environment_policy.inherit=" + q("all"),
		"-c", "shell_environment_policy.ignore_default_excludes=true",
		// The assistant's memory is the one the app's "Memory" page shows, shared with Claude (codexMemoryPrompt); Codex's
		// own (~/.codex/memories) is one store for every Codex on the machine, so it stays off here.
		"-c", "features.memories=false",
		// Compact at about 200K like Claude (assistant.go assistantCompactWindow), not near the model's whole window.
		"-c", "model_auto_compact_token_limit=" + window,
	})
}

// codexShell runs the launcher with `codex` turned into `codex <its flags> app-server <ours>`: the launcher's
// function sets what it sets (env, -c model_provider, -m …) exactly as when it starts the TUI.
// Use alias-safe definitions and calls: zsh expands aliases while parsing the script.
func codexShell(launcher string) string {
	return `__persona_args=("$@")
function codex { builtin command codex "$@" app-server "${__persona_args[@]}"; }
function command { if [[ "$1" == codex ]]; then shift; \codex "$@"; else builtin command "$@"; fi; }
` + launcher
}

func (n *Node) spawnCodex(id, cwd, launcher string, resume, spare bool, options ...modelExecution) (*assistantProc, error) {
	execution := executionOption(options)
	if err := execution.check(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return nil, err
	}
	root, memDir := n.codexDirs(cwd)
	key := "proc-" + randHex(8)
	shell, err := interactiveShell()
	if err != nil {
		return nil, err
	}
	compaction := n.assistantCompactionPolicy(launcher, cwd, "", execution)
	cmd := exec.Command(shell, append([]string{"-ic", codexShell(n.launcherCommand(launcher)), "persona"}, codexArgsForHome(n.bin, key, n.dir, compaction.window)...)...)
	cmd.Dir = root
	cmd.Env = identity.Environment(os.Environ(), "PERSONA_ASSISTANT=1")
	gateEnv, releaseGates, nameGates := n.assistantLarkEnvNamed(cwd, id) // its addresses at the gate proxy go with the process
	started := false
	defer func() {
		if !started {
			releaseGates()
		}
	}()
	cmd.Env = identity.Environment(cmd.Env, gateEnv...)
	cmd.SysProcAttr = osproc.GroupAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", launcher, err)
	}
	started = true
	p := &assistantProc{id: id, agent: "codex", launcher: launcher, cwd: cwd, cmd: cmd, stdin: stdin, exited: make(chan struct{}),
		born: time.Now(), lastActive: time.Now(), model: execution, fingerprint: n.executionFingerprint(launcher, execution)}
	c := &codexConn{n: n, p: p, key: key, memDir: memDir, pending: map[int64]chan codexReply{}, asks: map[string]context.CancelFunc{}, files: map[string][]string{},
		nameGates: nameGates}
	p.cx = c
	n.mu.Lock()
	n.akeys[key] = p
	n.mu.Unlock()
	go logLines(stderr, func(l string) {
		if strings.Contains(l, "ERROR") && !strings.Contains(l, "unrecognized configuration") {
			n.logger.Printf("assistant codex %s: %s", key, l)
		}
	})
	liveAssistants.Store(p, struct{}{})
	go c.read(stdout)
	go func() {
		_ = cmd.Wait()
		releaseGates()
		c.mu.Lock()
		c.closed = true
		for _, cancel := range c.asks {
			cancel()
		}
		c.mu.Unlock()
		liveAssistants.Delete(p)
		close(p.exited)
		if n.keeper != nil {
			n.keeper.forget(p)
		}
		n := p.ownerOr(n)
		n.mu.Lock()
		delete(n.akeys, key)
		wasSpare := n.spare == p
		if wasSpare {
			n.spare = nil
		}
		if p.id != "" && n.procs[p.id] == p {
			delete(n.procs, p.id)
		}
		n.mu.Unlock()
		if wasSpare || p.id == "" {
			return
		}
		n.emitDelta(p.id, "")
		n.notifySessions()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), codexStartTimeout)
	defer cancel()
	if err := c.start(ctx, id, root, resume); err != nil {
		p.stop()
		return nil, fmt.Errorf("%s（codex app-server）没有启动起来: %w", launcher, err)
	}
	if !spare {
		n.mu.Lock()
		n.procs[p.id] = p
		n.mu.Unlock()
	}
	return p, nil
}

// codexDirs is the workspace and the memory directory (made if missing) as Codex gets them, symlinks resolved: both
// are writable roots, and its macOS sandbox refuses one with a symlink in it (~/.persona → .persona, a ~/.claude kept in
// dotfiles), turning every command down before it runs. Only Codex sees the real paths; the session keeps its cwd.
func (n *Node) codexDirs(cwd string) (root, memDir string) {
	memDir = n.memoryDir(cwd)
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		n.logger.Printf("assistant codex: memory dir: %v", err)
	}
	return n.realDir(cwd), n.realDir(memDir)
}

func (n *Node) realDir(dir string) string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		n.logger.Printf("assistant codex: resolve %s: %v", dir, err)
		return dir
	}
	return real
}

// start does the handshake and opens the thread: thread/start for a new conversation (Codex picks the id),
// thread/resume for an existing one.
func (c *codexConn) start(ctx context.Context, id, cwd string, resume bool) error {
	n := c.p.ownerOr(c.n)
	var init struct {
		CodexHome string `json:"codexHome"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "persona", "title": "Persona", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, &init); err != nil {
		return err
	}
	if err := c.send(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		return err
	}
	// The extensions' skills are installed for Claude under .claude/skills (assistantLarkEnv); Codex reads the same format.
	if err := c.call(ctx, "skills/extraRoots/set", map[string]any{"extraRoots": []string{filepath.Join(cwd, ".claude", "skills")}}, nil); err != nil {
		n.logger.Printf("assistant codex: skills root: %v", err)
	}
	mode, _ := n.modesOf(id)
	approval, sandbox := codexPolicy(mode)
	params := map[string]any{"cwd": cwd, "approvalPolicy": approval, "sandbox": sandbox}
	if c.p.model.Model != "" {
		params["model"] = c.p.model.Model
	}
	// The same on every start of the thread, apart from the memory index: the live snapshot goes with each message.
	params["developerInstructions"] = prompts.RenderCodexInstructions(codexMemoryPrompt(c.memDir))
	var res struct {
		Thread struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"thread"`
	}
	method := "thread/start"
	if resume {
		method = "thread/resume"
		params["threadId"] = id
		params["excludeTurns"] = true
	}
	if err := c.call(ctx, method, params, &res); err != nil {
		return err
	}
	if res.Thread.ID == "" {
		return errors.New("no thread id")
	}
	c.mu.Lock()
	c.threadID = res.Thread.ID
	c.mu.Unlock()
	n.mu.Lock()
	c.p.id = res.Thread.ID
	if res.Thread.Path != "" {
		n.paths[res.Thread.ID] = res.Thread.Path // Codex said so (CODEX_HOME may not be ~/.codex)
		if n.pathsAt == nil {
			n.pathsAt = map[string]time.Time{}
		}
		n.pathsAt[res.Thread.ID] = time.Now()
	}
	n.mu.Unlock()
	if c.nameGates != nil { // larkCheck records what it confirmed under this id (p.id): the addresses stand for it from now on
		c.nameGates(res.Thread.ID)
	}
	return nil
}

// codexPolicy maps the explicitly selected permission mode to the native thread and turn settings.
// Full access disables command approvals; connector confirmations remain in their proxies and CLI wrappers.
func codexPolicy(mode string) (approval, sandbox string) {
	switch mode {
	case "auto":
		return "on-request", "workspace-write"
	case "plan":
		return "untrusted", "read-only"
	case proto.ModeBypass:
		return "never", "danger-full-access"
	}
	return "untrusted", "workspace-write"
}

// codexSandboxPolicy is the turn's sandbox. The network is open in workspace-write where every command is asked
// about anyway (the Lark extension's ol, ssh to the user's computers); in auto it stays closed, so going out (ol included)
// is an escalation that comes to the node. The memory directory (outside the assistant's own) is writable.
func codexSandboxPolicy(mode, memDir string) map[string]any {
	roots := []string{}
	if memDir != "" {
		roots = append(roots, memDir)
	}
	switch _, sandbox := codexPolicy(mode); sandbox {
	case "read-only":
		return map[string]any{"type": "readOnly", "networkAccess": false}
	case "danger-full-access":
		return map[string]any{"type": "dangerFullAccess"}
	}
	return map[string]any{"type": "workspaceWrite", "writableRoots": roots, "networkAccess": mode != "auto",
		"excludeTmpdirEnvVar": false, "excludeSlashTmp": false}
}

// deliver hands a user message to the thread: a new turn when idle, steered into the running turn otherwise. live is
// the <persona-context> block it starts with ("" for none; assistant_context.go).
func (c *codexConn) deliver(ctx context.Context, live, text string, images []proto.Image) error {
	n := c.p.ownerOr(c.n)
	input := []map[string]any{}
	if live != "" {
		input = append(input, map[string]any{"type": "text", "text": live, "text_elements": []any{}})
	}
	if strings.TrimSpace(text) != "" || len(images) == 0 {
		input = append(input, map[string]any{"type": "text", "text": text, "text_elements": []any{}})
	}
	// Local files: the rollout records their paths, which the app shows as thumbnails (transcript/codex.go).
	paths, err := n.saveImages(images)
	if err != nil {
		return err
	}
	for _, p := range paths {
		input = append(input, map[string]any{"type": "localImage", "path": p})
	}
	c.mu.Lock()
	thread, turn := c.threadID, c.turnID
	c.mu.Unlock()
	if turn != "" {
		err := c.call(ctx, "turn/steer", map[string]any{"threadId": thread, "input": input, "expectedTurnId": turn}, nil)
		if err == nil {
			return nil
		}
		n.logger.Printf("assistant codex %s: steer: %v; starting a turn instead", short(thread), err) // the turn just ended
	}
	mode, effort := n.modesOf(c.p.id)
	c.p.mu.Lock()
	if c.p.model.UseEffort {
		effort = c.p.model.Effort
	}
	c.p.mu.Unlock()
	approval, _ := codexPolicy(mode)
	params := map[string]any{"threadId": thread, "input": input, "approvalPolicy": approval, "sandboxPolicy": codexSandboxPolicy(mode, c.memDir)}
	if c.p.model.Model != "" {
		params["model"] = c.p.model.Model
	}
	if effort != "" && effort != effortAuto { // auto: the model's own default, which is no effort at all for Codex
		params["effort"] = effort
	}
	var res struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := c.call(ctx, "turn/start", params, &res); err != nil {
		return err
	}
	c.mu.Lock()
	if res.Turn.ID != "" {
		c.turnID = res.Turn.ID
	}
	c.mu.Unlock()
	return nil
}

// interrupt stops the running turn; the process and its thread stay for the next message. The turn's commands go
// too: turn/interrupt leaves a running command behind as a background terminal (a `sleep 120` kept running), so
// they are cleaned up like Claude's are when its process is killed.
func (c *codexConn) interrupt(ctx context.Context) error {
	c.mu.Lock()
	thread, turn := c.threadID, c.turnID
	c.mu.Unlock()
	if turn != "" {
		if err := c.call(ctx, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, nil); err != nil {
			return err
		}
	}
	if err := c.call(ctx, "thread/backgroundTerminals/clean", map[string]any{"threadId": thread}, nil); err != nil {
		c.p.ownerOr(c.n).logger.Printf("assistant codex %s: clean background terminals: %v", short(thread), err)
	}
	c.p.mu.Lock()
	c.p.tasks = nil
	c.p.mu.Unlock()
	return nil
}

// compact asks Codex to compact the thread (the user's compaction, assistant_compact.go); it reports the end with
// thread/compacted or a contextCompaction item, or a turn that ends without one.
func (c *codexConn) compact(ctx context.Context) error {
	c.mu.Lock()
	thread := c.threadID
	c.mu.Unlock()
	return c.call(ctx, "thread/compact/start", map[string]any{"threadId": thread}, nil)
}

// compacted: Codex compacted the thread; the user's compaction, if one was running, is done.
func (c *codexConn) compacted() {
	c.p.ownerOr(c.n).contextCompacted(c.p.id) // the snapshots it had seen were summarized away (assistant_context.go)
	c.p.mu.Lock()
	manual := c.p.manual
	c.p.manual, c.p.compacting = false, false
	c.p.mu.Unlock()
	if manual {
		c.p.ownerOr(c.n).compactionEnded(c.p.id, "")
	}
}

// syncTasks reads the thread's background terminals (commands the model left running, Codex's background tasks) into
// the process's task list: while there are some the process is not reaped, and a restart tells the conversation what
// it lost (recoverAssistantTasks). Unlike Claude's, a finishing one does not start a turn by itself.
func (c *codexConn) syncTasks() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.mu.Lock()
	thread := c.threadID
	c.mu.Unlock()
	var res struct {
		Data []struct {
			Command string `json:"command"`
		} `json:"data"`
	}
	if err := c.call(ctx, "thread/backgroundTerminals/list", map[string]any{"threadId": thread}, &res); err != nil {
		return
	}
	tasks := []string{}
	for _, t := range res.Data {
		tasks = append(tasks, "后台命令："+unwrapShell(t.Command))
	}
	c.p.mu.Lock()
	c.p.tasks = tasks
	c.p.mu.Unlock()
	c.p.ownerOr(c.n).rememberTasks(c.p.id, tasks)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// codexLineMax is the longest app-server line read; a var so tests can lower it.
var codexLineMax = 64 * 1024 * 1024

// read handles everything the app-server writes: responses, notifications, and requests to us (approvals…).
func (c *codexConn) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), codexLineMax)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' { // an interactive shell may print banners
			continue
		}
		var m codexMessage
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		c.receive(m)
	}
	stopUnreadAssistant(c.p.ownerOr(c.n).logger, c.p, "codex "+c.key, sc.Err()) // the exit goroutine in spawnCodex cleans up
}

func (c *codexConn) receive(m codexMessage) {
	if m.Method == "" && len(m.ID) > 0 {
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- codexReply{Result: m.Result, Err: m.Error}:
			default:
			}
		}
		return
	}
	if c.onMessage != nil {
		c.onMessage(m)
		return
	}
	if m.Method != "" && len(m.ID) > 0 {
		go c.serve(m)
	} else if m.Method != "" {
		c.notification(m.Method, m.Params)
	}
}

func (c *codexConn) notification(method string, params json.RawMessage) {
	n, p := c.p.ownerOr(c.n), c.p
	var ev struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Delta    string `json:"delta"`
		Turn     struct {
			ID     string          `json:"id"`
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"turn"`
		Item struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Text    string `json:"text"`
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		WillRetry bool            `json:"willRetry"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	c.mu.Lock()
	thread := c.threadID
	c.mu.Unlock()
	if ev.ThreadID != "" && thread != "" && ev.ThreadID != thread {
		return // a subagent's thread: its items are in the main rollout's events anyway
	}
	switch method {
	case "turn/started":
		c.mu.Lock()
		c.turnID = ev.Turn.ID
		c.mu.Unlock()
		p.mu.Lock()
		had := p.partial.Len() > 0
		p.partial.Reset()
		p.turnError = ""
		p.turnErrorKind = ""
		resumed := !p.busy
		p.busy = true
		p.lastActive = time.Now()
		p.mu.Unlock()
		if resumed {
			n.notifySessions()
		}
		if had {
			n.emitDelta(p.id, "")
		}
	case "item/started":
		if ev.Item.Type == "fileChange" {
			var paths []string
			for _, ch := range ev.Item.Changes {
				paths = append(paths, ch.Path)
			}
			c.mu.Lock()
			c.files[ev.Item.ID] = paths
			c.mu.Unlock()
		}
		if ev.Item.Type != "agentMessage" {
			return
		}
		// A new message: the last one is in the rollout by now; clear the live copy so it is not shown twice.
		p.mu.Lock()
		had := p.partial.Len() > 0
		p.partial.Reset()
		p.lastActive = time.Now()
		p.mu.Unlock()
		if had {
			n.emitDelta(p.id, "")
		}
	case "item/agentMessage/delta":
		p.mu.Lock()
		p.partial.WriteString(ev.Delta)
		text := p.partial.String()
		due := time.Since(p.lastDelta) > 80*time.Millisecond
		if due {
			p.lastDelta = time.Now()
		}
		p.lastActive = time.Now()
		p.mu.Unlock()
		if due {
			n.emitDelta(p.id, text)
		}
	case "thread/compacted":
		c.compacted()
	case "item/completed":
		c.mu.Lock()
		delete(c.files, ev.Item.ID)
		c.mu.Unlock()
		if ev.Item.Type == "contextCompaction" {
			c.compacted()
		}
		if ev.Item.Type == "agentMessage" {
			n.emitDelta(p.id, ev.Item.Text) // until the item arrives from the rollout
		}
		if f, err := n.file(p.id); err == nil {
			n.refresh(p.id, f)
		}
	case "turn/completed":
		c.mu.Lock()
		if c.turnID == ev.Turn.ID {
			c.turnID = ""
		}
		c.mu.Unlock()
		var failure, failureKind string
		if ev.Turn.Status != "interrupted" {
			if len(ev.Turn.Error) > 0 && string(ev.Turn.Error) != "null" {
				failure, failureKind = transcript.CodexTurnError(ev.Turn.Error)
			}
			if failure == "" && (ev.Turn.Status == "failed" || (ev.Turn.Status != "completed" && ev.Turn.Status != "")) {
				failure = transcript.CodexTurnFailed
			}
		}
		if failure != "" {
			n.logger.Printf("assistant codex %s: turn %s: %s", short(p.id), ev.Turn.Status, failure)
		}
		p.mu.Lock()
		p.busy = false
		p.turnError = failure
		p.turnErrorKind = failureKind
		p.lastActive = time.Now()
		p.partial.Reset()
		manual := p.manual
		p.manual, p.compacting = false, false
		p.mu.Unlock()
		if manual { // the turn the compaction ran in ended before its item came: done, unless it failed
			n.compactionEnded(p.id, snippet(failure))
		}
		if f, err := n.file(p.id); err == nil {
			n.refresh(p.id, f)
		}
		n.emitDelta(p.id, "")
		n.notifySessions()
		go c.syncTasks()
	case "error":
		if !ev.WillRetry {
			n.logger.Printf("assistant codex %s: %s", short(p.id), ev.Error.Message)
		}
	case "serverRequest/resolved":
		// Answered some other way (the turn was interrupted): drop the phone's question.
		c.mu.Lock()
		cancel := c.asks[string(ev.RequestID)]
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

// serve answers a request from the app-server. Approvals may wait for the phone, so each runs on its own.
func (c *codexConn) serve(m codexMessage) {
	ctx, cancel := context.WithCancel(context.Background())
	key := string(m.ID)
	c.mu.Lock()
	c.asks[key] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.asks, key)
		c.mu.Unlock()
		cancel()
	}()
	result, err := c.decide(ctx, m.Method, m.Params)
	if err != nil {
		c.replyError(m.ID, -32601, err.Error())
		return
	}
	c.reply(m.ID, result)
}

// codexRequest is the union of the request params the node reads.
type codexRequest struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	ItemID     string `json:"itemId"`
	ApprovalID string `json:"approvalId"`
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
	Command    string `json:"command"`
	Cwd        string `json:"cwd"`
	GrantRoot  string `json:"grantRoot"`
	ServerName string `json:"serverName"`
	Message    string `json:"message"`
	Mode       string `json:"mode"`
	Meta       struct {
		Kind       string         `json:"codex_approval_kind"`
		ToolParams map[string]any `json:"tool_params"`
	} `json:"_meta"`
	Questions []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
	Permissions json.RawMessage `json:"permissions"`
}

// decide works out the answer to one app-server request.
func (c *codexConn) decide(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	n, p := c.p.ownerOr(c.n), c.p
	ctx = context.WithValue(ctx, codexApprovalOwner{}, c)
	var r codexRequest
	_ = json.Unmarshal(raw, &r)
	mode, _ := n.modesOf(p.id)
	cwd := firstNonEmpty(r.Cwd, p.cwd)
	ix := proto.Interaction{
		ID: codexInteractionID(p.id, method, r.ItemID, r.ApprovalID, r.ServerName), SessionID: p.id, Cwd: cwd,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(nodeclient.HookTimeout).UTC(), ToolUseID: r.ItemID,
		SessionTitle: n.assistantTitle(p.id, cwd),
	}
	switch method {
	case "item/commandExecution/requestApproval":
		command := unwrapShell(r.Command)
		ix.ToolName = "Bash"
		ix.ToolInput, _ = json.Marshal(map[string]any{"command": command})
		decision, reason, err := n.larkCheck(ctx, p.id, cwd, command, ix)
		if err != nil {
			return map[string]any{"decision": "cancel"}, nil
		}
		switch decision {
		case "allow":
			return map[string]any{"decision": "accept"}, nil
		case "deny":
			c.tellModel(reason)
			return map[string]any{"decision": "decline"}, nil
		}
		if mode == proto.ModeBypass || readsOnlyMemory(command, cwd, c.memDir) {
			return map[string]any{"decision": "accept"}, nil // Claude reads its auto-memory unasked too
		}
		ix.Kind, ix.Summary = proto.IxPermission, withReason(command, r.Reason)
		ix.AlwaysKeys = []string{"codex-command:" + command} // keep the legacy command scope
		// The hub remembers Always; accepting once keeps the next request revocable.
		return c.askPhone(ctx, ix, "accept", "accept", "decline"), nil

	case "item/fileChange/requestApproval":
		c.mu.Lock()
		paths := c.files[r.ItemID]
		c.mu.Unlock()
		// Writing its memory is unasked, like Claude's auto-memory (not in plan mode, which writes nothing).
		if mode == proto.ModeBypass || mode == "acceptEdits" || (mode != "plan" && r.GrantRoot == "" && allUnder(paths, cwd, c.memDir)) {
			return map[string]any{"decision": "accept"}, nil
		}
		ix.ToolName = "Edit"
		file := firstNonEmpty(append(paths, r.GrantRoot)...)
		ix.ToolInput, _ = json.Marshal(map[string]any{"file_path": file})
		ix.Kind, ix.Summary = proto.IxPermission, withReason(strings.Join(paths, "\n"), r.Reason)
		if ix.Summary == "" {
			ix.Summary = "修改文件"
		}
		for _, p := range paths {
			ix.AlwaysKeys = append(ix.AlwaysKeys, "codex-edit:"+p)
		}
		if r.GrantRoot != "" {
			ix.AlwaysKeys = append(ix.AlwaysKeys, "codex-root:"+r.GrantRoot)
		}
		return c.askPhone(ctx, ix, "accept", "accept", "decline"), nil

	case "mcpServer/elicitation/request":
		if r.Meta.Kind != "mcp_tool_call" {
			return map[string]any{"action": "decline", "content": nil, "_meta": nil}, nil // forms: nobody to fill them in
		}
		accept := map[string]any{"action": "accept", "content": map[string]any{}, "_meta": nil}
		// Persona's own tools run unasked; the hub asks before the ones that act for the user (mail_send, phone writes).
		if compat.IsMCPServer(r.ServerName) || mode == proto.ModeBypass {
			return accept, nil
		}
		ix.ToolName = "mcp__" + r.ServerName
		ix.ToolInput, _ = json.Marshal(r.Meta.ToolParams)
		ix.Kind, ix.Summary = proto.IxPermission, r.Message
		res, ok, err := n.ask(ctx, ix)
		if err != nil || !ok || res.Allow == nil || !*res.Allow {
			if ok && res.Message != "" {
				c.tellModel(res.Message)
			}
			return map[string]any{"action": "decline", "content": nil, "_meta": nil}, nil
		}
		return accept, nil

	case "item/permissions/requestApproval":
		deny := map[string]any{"permissions": map[string]any{}, "scope": "turn"}
		var granted map[string]any
		_ = json.Unmarshal(r.Permissions, &granted)
		for k, v := range granted {
			if v == nil {
				delete(granted, k)
			}
		}
		if granted == nil {
			granted = map[string]any{}
		}
		if mode != proto.ModeBypass {
			ix.ToolName = "权限"
			ix.ToolInput = r.Permissions
			b, _ := json.Marshal(granted)
			ix.Kind, ix.Summary = proto.IxPermission, withReason("Codex 要更多权限："+string(b), r.Reason)
			ix.AlwaysKeys = []string{"codex-permissions:" + string(b)}
			res, ok, err := n.ask(ctx, ix)
			if err != nil || !ok || res.Allow == nil || !*res.Allow {
				return deny, nil
			}
			// The hub remembers Always; a runtime session grant would bypass a later revocation.
		}
		return map[string]any{"permissions": granted, "scope": "turn"}, nil

	case "item/tool/requestUserInput":
		ix.Kind, ix.ToolName, ix.CanCancel = proto.IxQuestion, "request_user_input", true
		for _, q := range r.Questions {
			qq := proto.Question{Question: q.Question, Header: q.Header}
			for _, o := range q.Options {
				qq.Options = append(qq.Options, proto.QuestionOption{Label: o.Label, Description: o.Description})
			}
			ix.Questions = append(ix.Questions, qq)
		}
		answers := map[string]any{}
		res, ok, err := n.ask(ctx, ix)
		if err == nil && ok && !res.Cancel {
			for _, q := range r.Questions {
				if a, ok := res.Answers[q.Question]; ok {
					answers[q.ID] = map[string]any{"answers": []string{a}}
				}
			}
		}
		return map[string]any{"answers": answers}, nil
	}
	return nil, fmt.Errorf("persona does not handle %s", method)
}

// askPhone asks about a command / file change and maps the answer to Codex's decision names.
func (c *codexConn) askPhone(ctx context.Context, ix proto.Interaction, accept, always, decline string) map[string]any {
	res, ok, err := c.askResumable(ctx, ix)
	if err != nil || !ok || res.Allow == nil {
		return map[string]any{"decision": "cancel"}
	}
	if !*res.Allow {
		if res.Message != "" {
			c.tellModel(res.Message)
		}
		return map[string]any{"decision": decline}
	}
	if res.Always {
		return map[string]any{"decision": always}
	}
	return map[string]any{"decision": accept}
}

// tellModel passes why something was refused (the user's words on the phone, the gate's reason) into the running
// turn: Codex's "decline" carries no message, unlike a Claude hook's deny.
func (c *codexConn) tellModel(reason string) {
	if strings.TrimSpace(reason) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c.mu.Lock()
		thread, turn := c.threadID, c.turnID
		c.mu.Unlock()
		if turn == "" {
			return
		}
		input := []map[string]any{{"type": "text", "text": prompts.RenderRefused(reason), "text_elements": []any{}}}
		if err := c.call(ctx, "turn/steer", map[string]any{"threadId": thread, "input": input, "expectedTurnId": turn}, nil); err != nil {
			c.p.ownerOr(c.n).logger.Printf("assistant codex %s: pass the refusal on: %v", short(thread), err)
		}
	}()
}

// memoryReaders are the commands readsOnlyMemory lets through, with the flags that would make one write or run
// something (sed -i / w, rg --pre…): refused.
var memoryReaders = map[string][]string{"cat": nil, "head": nil, "tail": {"-f", "--follow", "-F"}, "ls": nil, "wc": nil, "nl": nil,
	"grep": nil, "rg": {"--pre", "--pre-glob"}, "sed": {"-i", "--in-place"}}

// readsOnlyMemory: a command that only reads, and only the memory directory (cat / head / rg … on paths in it, a
// timeout in front, output to /dev/null at most). What it reads is what the "Memory" page shows anyway.
func readsOnlyMemory(command, cwd, dir string) bool {
	if dir == "" {
		return false
	}
	segs, ok := shellSegments(command)
	if !ok || len(segs) == 0 {
		return false
	}
	touched := false
	for _, sg := range segs {
		w := sg.words
		if len(w) > 2 && w[0] == "timeout" && timeoutArg.MatchString(w[1]) {
			w = w[2:]
		}
		if len(w) == 0 || sg.subst || !safeRedirect(sg.redirect) || strings.Contains(sg.redirect, "/tmp/") {
			return false
		}
		bad, known := memoryReaders[w[0]]
		if !known {
			return false
		}
		for _, a := range w[1:] {
			for _, b := range bad {
				if a == b || strings.HasPrefix(a, b+"=") || (b == "-i" && strings.HasPrefix(a, "-i")) {
					return false
				}
			}
			if w[0] == "sed" && !strings.HasPrefix(a, "-") && strings.ContainsAny(a, "wWeE") && !strings.Contains(a, "/") {
				return false // a sed script that writes (w file) or runs (e) something
			}
			if strings.Contains(a, "/") && !strings.HasPrefix(a, "-") {
				if p := a; !allUnder([]string{p}, cwd, dir) && realPath(filepath.Clean(p)) != realPath(dir) {
					return false
				}
				touched = true
			}
		}
	}
	return touched
}

// allUnder: there are paths, and each (relative ones from cwd) is inside dir.
func allUnder(paths []string, cwd, dir string) bool {
	if dir == "" || len(paths) == 0 {
		return false
	}
	dir = realPath(dir)
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		rel, err := filepath.Rel(dir, realPath(filepath.Clean(p)))
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || rel == "." {
			return false
		}
	}
	return true
}

// realPath is p with its symlinks resolved as far as it exists: its longest existing ancestor (a file about to be
// written, a checkout to be made, need not exist yet), so two spellings of one place compare — a path written through a
// link (~/.claude/..., macOS's /var for /private/var) and a resolved one. p as it is when nothing of it resolves.
func realPath(p string) string {
	rest := ""
	for q := p; ; {
		if real, err := filepath.EvalSymlinks(q); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(q)
		if parent == q {
			return p
		}
		rest = filepath.Join(filepath.Base(q), rest)
		q = parent
	}
}

// codexMemoryPrompt tells Codex about the assistant's memory: the same directory and format as Claude Code's
// auto-memory for the assistant's directory (memory.go), so both agents read and write one memory and the app's
// "Memory" page shows it whichever runs. The index (MEMORY.md) goes in as it is now; the files are read when needed.
func codexMemoryPrompt(dir string) string {
	m := prompts.CodexMemory{Dir: dir}
	index, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if len(index) > 24*1024 {
		m.Index, m.Truncated = strings.TrimLeftFunc(string(index[:24*1024]), unicode.IsSpace), true
	} else {
		m.Index = strings.TrimSpace(string(index))
	}
	return prompts.RenderCodexMemory(m)
}

func withReason(s, reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" && s != "" {
		return s + "\n\n" + reason
	}
	return firstNonEmpty(s, reason)
}

// unwrapShell takes the script out of Codex's `/bin/zsh -lc '…'` so the gate reads the command the model wrote.
func unwrapShell(cmd string) string {
	segs, ok := shellSegments(cmd)
	if !ok || len(segs) != 1 {
		return cmd
	}
	w := segs[0].words
	if len(w) == 3 && (w[1] == "-c" || w[1] == "-lc") {
		switch filepath.Base(w[0]) {
		case "sh", "bash", "zsh":
			return w[2]
		}
	}
	return cmd
}

// codexInteractionID is stable for one request (a retried request finds its card again).
func codexInteractionID(session, method, item, approval, server string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + method + "\x00" + item + "\x00" + approval + "\x00" + server))
	return "ix_" + hex.EncodeToString(sum[:])[:16]
}

func (n *Node) assistantTitle(id, cwd string) string {
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil {
			if t := n.meta(p, st).title; t != "" {
				return t
			}
		}
	}
	return filepath.Base(cwd)
}

// resolveAssistantKey turns the key a Codex conversation's MCP server was started with into its session id (the
// thread id, known only after thread/start). Anything else is returned as it is.
func (n *Node) resolveAssistantKey(id string) string {
	if !strings.HasPrefix(id, "proc-") {
		return id
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if p := n.akeys[id]; p != nil && p.id != "" {
		return p.id
	}
	return id
}
