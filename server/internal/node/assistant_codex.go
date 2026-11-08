package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
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
