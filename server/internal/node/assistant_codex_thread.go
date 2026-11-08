package node

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

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
