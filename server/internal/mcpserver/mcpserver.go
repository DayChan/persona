// Package mcpserver is the stdio MCP server Claude Code starts (`personad mcp`); it proxies tools to the local node.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"

	"github.com/nwverse/persona/server/internal/proto"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// notification is a JSON-RPC message the server sends unasked (no id): notifications/progress.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// Serve answers MCP requests from in until it closes. Each request runs on its own: Claude Code calls tools in
// parallel, and one waiting for the phone (ask_user, an approval) must not hold the others back. A tool call Claude
// Code gives up on (notifications/cancelled: the user stopped the turn) is cancelled: the node hears the hang-up and
// the hub takes its card off the phone, instead of leaving a card nobody waits for until it expires. A tool call that
// asks for progress gets notifications/progress while it waits (keepAlive), so the client's idle limit doesn't end it.
func Serve(ctx context.Context, version string, in io.Reader, out io.Writer) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 32*1024*1024)
	enc := json.NewEncoder(out)
	var (
		mu       sync.Mutex // enc, inflight
		inflight = map[string]context.CancelFunc{}
		wg       sync.WaitGroup
		werr     error
	)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil || req.Method == "" {
			if id, rerr := rejectLine(sc.Bytes()); rerr != nil {
				mu.Lock()
				if err := enc.Encode(response{JSONRPC: "2.0", ID: id, Error: rerr}); err != nil && werr == nil {
					werr = err
				}
				mu.Unlock()
			}
			continue
		}
		if len(req.ID) == 0 { // notification
			if req.Method == "notifications/cancelled" {
				var p struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				_ = json.Unmarshal(req.Params, &p)
				mu.Lock()
				if cancel := inflight[idKey(p.RequestID)]; cancel != nil {
					cancel()
				}
				mu.Unlock()
			}
			continue
		}
		rctx, cancel := context.WithCancel(ctx)
		key := idKey(req.ID)
		mu.Lock()
		inflight[key] = cancel
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopProgress := keepAlive(rctx, progressToken(req), func(n notification) {
				mu.Lock()
				defer mu.Unlock()
				if err := enc.Encode(n); err != nil && werr == nil {
					werr = err
				}
			})
			res, rerr := handle(rctx, version, req)
			stopProgress() // no progress line after the answer
			mu.Lock()
			defer mu.Unlock()
			cancelled := rctx.Err() != nil
			if inflight[key] != nil {
				delete(inflight, key)
			}
			cancel()
			if cancelled { // the client gave up on it: no answer (MCP: a cancelled request is not responded to)
				return
			}
			if err := enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr}); err != nil && werr == nil {
				werr = err
			}
		}()
	}
	// stdin closed: the client is gone, nobody reads the answers of what still runs.
	stop()
	wg.Wait()
	if werr != nil {
		return werr
	}
	return sc.Err()
}

// rejectLine is the JSON-RPC error for a line that is not a request, and the id to send it under: -32700 for a line
// that is not JSON, -32600 for JSON that is not a request object (an array, a scalar, an object without a method).
// A blank line, or an object without an id (a notification nobody waits on), gets no answer: nil.
func rejectLine(line []byte) (json.RawMessage, *rpcError) {
	null := json.RawMessage("null")
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, nil
	}
	if !json.Valid(line) {
		return null, &rpcError{Code: -32700, Message: "Parse error"}
	}
	invalid := &rpcError{Code: -32600, Message: "Invalid Request"}
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil || m == nil {
		return null, invalid
	}
	id, ok := m["id"]
	if !ok {
		return nil, nil
	}
	// Echo the id only when it is one JSON-RPC allows (a string or a number); otherwise it cannot be trusted.
	var v any
	_ = json.Unmarshal(id, &v)
	switch v.(type) {
	case string, float64:
		return id, invalid
	}
	return null, invalid
}

// CallTimeout ends a tool call whose answer never came. The deadlines that answer are the hub's and the manager's:
// the longest is ask_user's 2 hours (assistant askUserWait, which a test there keeps below this), and keepAlive keeps
// the client waiting that long. It was 40 minutes, which ended ask_user long before its own deadline (#1498).
const CallTimeout = 2*time.Hour + 10*time.Minute

// progressEvery is how often a tool call still waiting sends progress (keepAlive). Claude Code gives up on a stdio
// MCP call that sends "no response or progress" for 30 minutes (CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT); progress
// restarts that clock, and a minute leaves room for a shorter setting. Swapped out by tests.
var progressEvery = time.Minute

// progressToken is the token a tools/call asks for progress under (MCP: params._meta.progressToken, a string or a
// number), or nil when it asks for none: progress may only be sent for a token the client gave.
func progressToken(req request) json.RawMessage {
	if req.Method != "tools/call" {
		return nil
	}
	var p struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		return nil
	}
	var v any
	if json.Unmarshal(p.Meta.ProgressToken, &v) != nil {
		return nil
	}
	switch v.(type) {
	case string, float64:
		return p.Meta.ProgressToken
	}
	return nil
}

// keepAlive sends notifications/progress for token every progressEvery until the returned stop is called or ctx
// ends (#1498). A tool call can wait on the user far longer than the client's idle limit: a write approved on the
// phone up to 30 minutes (hub phoneConfirmWait), ask_user up to 2 hours (assistant askUserWait). Without progress the
// client aborted the call at its limit, so the model got a generic abort instead of the hub's own timeout reply
// (which says to check before calling again), and ask_user never lived to its Lark nudge. The deadlines are the
// hub's and the manager's; this only says the call is alive. progress counts the beats, so it only grows, as MCP
// requires. stop waits until nothing more is sent, so no progress line follows the answer. No token: a no-op.
func keepAlive(ctx context.Context, token json.RawMessage, send func(notification)) (stop func()) {
	if token == nil {
		return func() {}
	}
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()
		for beat := 1; ; beat++ {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-tick.C:
				// Whichever ended while the tick was due wins over it.
				select {
				case <-ctx.Done():
					return
				case <-quit:
					return
				default:
				}
				send(notification{JSONRPC: "2.0", Method: "notifications/progress", Params: map[string]any{
					"progressToken": token, "progress": beat, "message": "still waiting",
				}})
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// idKey names a JSON-RPC id the same however it was spaced (a number or a string).
func idKey(id json.RawMessage) string {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return string(id)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func handle(ctx context.Context, version string, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "persona", "version": version},
			"instructions": "Persona tools reach the user's iPhone (phone_* tools: contacts, calendar, reminders, location, health, photos, " +
				"clipboard, notifications) and email accounts (mail_* tools). phone_* tools need the Persona app to be open on the iPhone. " +
				"mail_send asks the user to approve on the phone before sending." + assistantInstructions(),
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		tools, err := listTools(ctx)
		if err != nil {
			// listChanged is false, so an empty list would stick for the whole conversation: fail with the reason instead.
			return nil, &rpcError{Code: -32603, Message: "Persona tools are unavailable: " + err.Error()}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		if rerr := checkCallParams(&p.Name, &p.Arguments); rerr != nil {
			return nil, rerr
		}
		// Phone round trips, approvals and ask_user can take a while (the user may need to unlock the phone).
		ctx, cancel := context.WithTimeout(ctx, CallTimeout)
		defer cancel()
		// pane: an agent's MCP server has no session id of its own; the node knows the session in its pane
		// (report_to_supervisor and the todo tools need to know whose call it is: node callerTools).
		line, err := callNode(ctx, map[string]any{"kind": "mcp.call", "away_ok": true, "name": p.Name, "args": p.Arguments,
			"session_id": sessionID(), "assistant": isAssistant(), "guest": isGuest(), "pane": nodeclient.HookPane(os.Getenv)})
		if err != nil {
			return toolResult(proto.TextResult(callFailure(ctx, err), true)), nil
		}
		var res proto.ToolResult
		if err := json.Unmarshal(line, &res); err != nil {
			return toolResult(proto.TextResult(fmt.Sprintf("bad response from Persona node (%v)", err), true)), nil
		}
		return toolResult(res), nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
}

// checkCallParams rejects a tools/call the node must not run (MCP: invalid params are -32602): the tool handlers
// decode their arguments leniently and would act on zero values. Absent or null arguments become {}, so the node
// and hub always get an object.
func checkCallParams(name *string, args *json.RawMessage) *rpcError {
	if strings.TrimSpace(*name) == "" {
		return &rpcError{Code: -32602, Message: "tools/call: name is required"}
	}
	a := bytes.TrimSpace(*args)
	if len(a) == 0 || bytes.Equal(a, []byte("null")) {
		*args = json.RawMessage(`{}`)
		return nil
	}
	if a[0] != '{' {
		return &rpcError{Code: -32602, Message: "tools/call: arguments must be a JSON object"}
	}
	return nil
}

// dialNode and retryEvery are swapped out by tests. dialNode hangs up when its ctx is cancelled, so a cancelled tool
// call ends on the node too (and with it the hub's call and its card, rpc.Peer.Cancels).
var (
	dialNode   = nodeclient.DialInterruptible
	retryEvery = 500 * time.Millisecond
)

// callNode is dialNode for a tool call, tried again only while no node listens (nothing was sent): a resident node's
// socket belongs to the Node serving the hub's connection, so it is gone for a moment while a hub restart replaces
// that connection (docs/operations/hub-restart.md "Conversations are not interrupted"). A call that reached the node is never sent twice.
func callNode(ctx context.Context, req any) ([]byte, error) {
	deadline := time.Now().Add(callRedialFor)
	for {
		line, err := dialNode(ctx, req)
		if err == nil || !errors.Is(err, nodeclient.ErrNoNode) || time.Now().After(deadline) {
			return line, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(retryEvery):
		}
	}
}

// callFailure is the tool result text for a call callNode could not complete. Only ErrNoNode means nothing was sent;
// any other error came after the request reached the node, so the tool (mail_send, agent_send, ...) may have run and
// the model must check its effect rather than retry blindly.
func callFailure(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, nodeclient.ErrNoNode):
		return fmt.Sprintf("Persona node is not reachable (%v), so the call was not run. Is the hub connected to this machine?", err)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "The Persona node timed out before returning the result; the call may still have run, so check its effect before retrying."
	}
	return fmt.Sprintf("Lost the connection to the Persona node after sending the call; it may have run, so check its effect before retrying (%v).", err)
}

// callRedialFor: how long callNode waits for the node's socket to come back.
var callRedialFor = 15 * time.Second

// listTools asks the local node for the tools until ctx ends: the node may still be starting or reconnecting to the hub.
func listTools(ctx context.Context) ([]map[string]any, error) {
	for {
		tools, err := listToolsOnce(ctx)
		if err == nil {
			return tools, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(retryEvery):
		}
	}
}

func listToolsOnce(ctx context.Context) ([]map[string]any, error) {
	line, err := dialNode(ctx, map[string]any{"kind": "mcp.tools", "away_ok": true, "assistant": isAssistant(), "session_id": sessionID(), "guest": isGuest()})
	if err != nil {
		return nil, fmt.Errorf("Persona node is not reachable: %w", err)
	}
	var res struct {
		Tools []proto.Tool `json:"tools"`
		Error string       `json:"error"`
	}
	if err := json.Unmarshal(line, &res); err != nil {
		return nil, fmt.Errorf("bad response from Persona node: %w", err)
	}
	if res.Error != "" { // the node could not reach the hub (node/hooks.go)
		return nil, fmt.Errorf("Persona node could not list tools: %s", res.Error)
	}
	tools := []map[string]any{}
	for _, t := range res.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
	}
	return tools, nil
}

// The assistant's processes start this server with PERSONA_ASSISTANT / PERSONA_SESSION_ID (node/assistant.go): it
// asks for the assistant-only tools, which the node gives only when this process is one it runs for the assistant
// (node/clicall.go mcpAssistantOf), and the hub knows which conversation a call (e.g. ask_user) belongs to.
func isAssistant() bool { return identity.Getenv("PERSONA_ASSISTANT") == "1" }

// isGuest: a guest conversation's process (node/assistant_guest.go). The hub knows its guests itself; this is its word
// for one the hub cannot tell about, which then gets nothing.
func isGuest() bool { return identity.Getenv("PERSONA_GUEST") == "1" }

func sessionID() string {
	if id := identity.Getenv("PERSONA_SESSION_ID"); id != "" {
		return id
	}
	return os.Getenv("CLAUDE_SESSION_ID")
}

func assistantInstructions() string {
	if !isAssistant() {
		return ""
	}
	return " As the Persona assistant you also have: ask_user (questions shown on the phone; AskUserQuestion does not work here), " +
		"notify_user (push notification), schedule_followup / list_followups / cancel_followup (wake yourself up later), " +
		"agents_* (the coding agents on the user's computers) and lark_* (Feishu chats, calendar, docs, search)."
}

func toolResult(r proto.ToolResult) map[string]any {
	content := []map[string]any{}
	for _, c := range r.Content {
		switch c.Type {
		case "image":
			content = append(content, map[string]any{"type": "image", "data": c.Data, "mimeType": c.MimeType})
		default:
			content = append(content, map[string]any{"type": "text", "text": c.Text})
		}
	}
	return map[string]any{"content": content, "isError": r.IsError}
}
