// Package orcarttest is a scriptable fake of Orca's runtime socket for tests (a unix socket, a named pipe on Windows,
// as Orca itself serves there): it writes its own orca-runtime.json,
// checks the auth token, answers status.get, terminal.list, worktree.ps and terminal.read from its state, records every
// call in order, and lets a test replace any method's answer. Nothing here talks to a real Orca.
package orcarttest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Fake is one fake Orca runtime.
type Fake struct {
	// MetaPath is its orca-runtime.json, Socket its socket (a named pipe on Windows) and Kind that transport's kind.
	MetaPath string
	Socket   string
	Kind     string

	mu          sync.Mutex
	token       string
	runtimeID   string
	log         []Call
	terms       []map[string]any
	layouts     []any
	trees       []map[string]any
	screens     map[string]map[string]any
	status      map[string]any
	on          map[string]func(params map[string]any) any
	keepalive   int
	ln          net.Listener
	agentStatus map[string]map[string]any
	revisions   map[string]int
	writes      []Write
	invalid     []string
}

// Call is one request the fake got (status.get included).
type Call struct {
	Method string
	Params map[string]any
}

// String is "method {params}" with the params as sorted JSON (client and expectedIncarnationId left out: every
// handle call carries them; Writes and the fake's validation check them).
func (c Call) String() string {
	p := map[string]any{}
	for k, v := range c.Params {
		if k != "client" && k != "expectedIncarnationId" {
			p[k] = v
		}
	}
	b, _ := json.Marshal(p)
	return c.Method + " " + string(b)
}

// Fail makes a handler answer with an error of that code.
type Fail struct{ Code string }

// New starts a fake runtime in a fresh directory (TempDir).
func New(t testing.TB) *Fake {
	t.Helper()
	dir := TempDir(t)
	kind, socket := Endpoint(dir)
	f := &Fake{
		MetaPath: filepath.Join(dir, "orca-runtime.json"), Socket: socket, Kind: kind,
		token: "test-token-1", runtimeID: "rt-1", screens: map[string]map[string]any{},
		on: map[string]func(map[string]any) any{}, agentStatus: map[string]map[string]any{}, revisions: map[string]int{},
		status: map[string]any{"appVersion": "1.4.245", "runtimeProtocolVersion": 3, "minCompatibleRuntimeClientVersion": 2,
			"protocolVersion": 3, "capabilities": []string{"terminal.prompt-delivery.v1"}},
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	return f
}

// Start listens (again, after Stop) and writes the metadata file.
func (f *Fake) Start() error {
	ln, err := Listen(f.Kind, f.Socket)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.ln = ln
	f.mu.Unlock()
	if err := f.writeMeta(); err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return nil
}

// Stop closes the socket (Orca quit); the metadata file stays behind, as Orca's may.
func (f *Fake) Stop() {
	f.mu.Lock()
	ln := f.ln
	f.ln = nil
	f.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// Restart is Orca starting again: a new token and runtime id in the metadata file.
func (f *Fake) Restart(token string) error {
	f.Stop()
	f.mu.Lock()
	f.token, f.runtimeID = token, "rt-"+token
	f.mu.Unlock()
	return f.Start()
}

func (f *Fake) writeMeta() error {
	f.mu.Lock()
	meta := map[string]any{"runtimeId": f.runtimeID, "pid": os.Getpid(), "authToken": f.token, "startedAt": 1,
		"transports": []any{map[string]any{"kind": f.Kind, "endpoint": f.Socket},
			map[string]any{"kind": "websocket", "endpoint": "ws://127.0.0.1:1"}}}
	f.mu.Unlock()
	b, _ := json.Marshal(meta)
	return os.WriteFile(f.MetaPath, b, 0o600)
}

func (f *Fake) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID        any            `json:"id"`
			AuthToken string         `json:"authToken"`
			Method    string         `json:"method"`
			Params    map[string]any `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			return
		}
		if req.Params == nil {
			req.Params = map[string]any{}
		}
		f.mu.Lock()
		authed := req.AuthToken == f.token
		var result any
		invalid := ""
		if authed {
			f.log = append(f.log, Call{Method: req.Method, Params: req.Params})
			if invalid = validate(req.Method, req.Params); invalid == "" {
				result = f.answerLocked(req.Method, req.Params)
			} else {
				f.invalid = append(f.invalid, req.Method+": "+invalid)
			}
		}
		h, keepalive, rt := f.on[req.Method], f.keepalive, f.runtimeID
		f.mu.Unlock()
		if !authed {
			f.write(c, map[string]any{"id": req.ID, "ok": false, "error": map[string]any{"code": "unauthorized",
				"message": "Invalid auth token"}})
			continue
		}
		if invalid != "" {
			// Orca's dispatcher refuses params its schema rejects before any handler runs.
			f.write(c, map[string]any{"id": req.ID, "ok": false, "error": map[string]any{"code": "invalid_argument",
				"message": invalid}})
			continue
		}
		if h != nil {
			result = h(req.Params)
		}
		for range keepalive {
			f.write(c, map[string]any{"_keepalive": true})
		}
		resp := map[string]any{"id": req.ID, "_meta": map[string]any{"runtimeId": rt}}
		if e, ok := result.(Fail); ok {
			resp["ok"] = false
			resp["error"] = map[string]any{"code": e.Code, "message": e.Code}
		} else {
			resp["ok"], resp["result"] = true, result
		}
		f.write(c, resp)
	}
}

func (f *Fake) answerLocked(method string, p map[string]any) any {
	h, _ := p["terminal"].(string)
	switch method {
	case "status.get":
		return f.status
	case "repo.add":
		path, _ := p["path"].(string)
		return map[string]any{"repo": map[string]any{"path": path, "ok": true}}
	case "worktree.set":
		id, _ := p["worktree"].(string)
		for _, tree := range f.trees {
			if tree["worktreeId"] == strings.TrimPrefix(id, "id:") {
				tree["displayName"] = p["displayName"]
				return map[string]any{"worktree": tree}
			}
		}
		return Fail{Code: "worktree_not_found"}
	case "terminal.create":
		wtID, _ := p["worktree"].(string)
		wtID = strings.TrimPrefix(wtID, "id:")
		newIdx := len(f.terms) + 1
		newHandle := fmt.Sprintf("term_fake_%d", newIdx)
		newTab := fmt.Sprintf("tab_%d", newIdx)
		newLeaf := fmt.Sprintf("leaf_%d", newIdx)
		newInc := fmt.Sprintf("inc_%d", newIdx)
		title, _ := p["title"].(string)
		newTerm := map[string]any{
			"handle": newHandle, "tabId": newTab, "leafId": newLeaf, "incarnationId": newInc,
			"worktreeId": wtID, "title": title, "connected": true, "writable": true,
			"orphaned": false, "executionHostId": "local", "agentIdentity": "",
		}
		f.terms = append(f.terms, newTerm)
		return map[string]any{
			"terminal": map[string]any{
				"handle": newHandle, "tabId": newTab, "leafId": newLeaf, "incarnationId": newInc,
				"worktreeId": wtID, "title": title, "connected": true, "writable": true,
				"orphaned": false, "executionHostId": "local", "agentIdentity": "",
				"paneKey": newTab + ":" + newLeaf,
			},
		}
	case "terminal.list":
		out := map[string]any{"terminals": slices.Clone(f.terms), "topologyRevisions": maps.Clone(f.revisions),
			"hostScope": map[string]any{"hostIds": []string{"local"}}, "totalCount": len(f.terms), "truncated": false}
		if p["includeVisualLayouts"] != false { // absent includes them, as for older clients
			out["visualLayouts"] = slices.Clone(f.layouts)
		}
		return out
	case "worktree.ps":
		b, _ := json.Marshal(f.trees)
		id := fmt.Sprintf("snap-%x", fnv32(b))
		if _, ok := p["afterSnapshotId"]; !ok {
			return map[string]any{"worktrees": slices.Clone(f.trees), "totalCount": len(f.trees), "truncated": false}
		}
		if p["afterSnapshotId"] == id {
			return map[string]any{"unchanged": true, "snapshotId": id}
		}
		return map[string]any{"worktrees": slices.Clone(f.trees), "totalCount": len(f.trees), "truncated": false,
			"snapshotId": id}
	}
	t := f.termLocked(h)
	if t == nil {
		return Fail{"terminal_handle_stale"}
	}
	if inc, ok := p["expectedIncarnationId"]; ok && inc != t["incarnationId"] {
		return Fail{"terminal_handle_stale"} // the handle names another process now
	}
	switch method {
	case "terminal.read":
		s := f.screens[h]
		if s == nil {
			s = map[string]any{"tail": []string{}, "tailRows": []any{}}
		}
		out := map[string]any{"handle": h, "status": "running", "truncated": false, "source": "screen"}
		maps.Copy(out, s)
		return map[string]any{"terminal": out}
	case "terminal.agentStatus":
		st := f.agentStatus[h]
		if st == nil {
			st = map[string]any{"isRunningAgent": true, "status": nil}
		}
		out := map[string]any{"handle": h}
		maps.Copy(out, st)
		return map[string]any{"agentStatus": out}
	case "terminal.close":
		f.writes = append(f.writes, Write{Handle: h, Close: true})
		return map[string]any{"close": map[string]any{"handle": h, "tabId": t["tabId"], "ptyKilled": true}}
	case "terminal.rename":
		title, _ := p["title"].(string)
		if t != nil {
			t["title"] = title
		}
		return map[string]any{"rename": map[string]any{"handle": h, "title": title}}
	case "terminal.setPaneTitle":
		title, _ := p["title"].(string)
		return map[string]any{"setPaneTitle": map[string]any{"handle": h, "title": title}}
	case "terminal.send":
		text, _ := p["text"].(string)
		enter := p["enter"] == true
		if p["requireAgentStatus"] == "sendable" && text != "" && enter {
			// Guarded sends are two-phase in Orca: a combined text and submit is refused outright.
			return map[string]any{"send": map[string]any{"handle": h, "accepted": false, "bytesWritten": 0}}
		}
		st := f.agentStatus[h]
		running := st == nil || st["isRunningAgent"] == true
		client, _ := p["client"].(map[string]any)
		paste := p["agentPrompt"] == true && text != "" && enter && client["type"] == "desktop" && running
		w := Write{Handle: h, Text: text, Enter: enter, Pasted: paste}
		f.writes = append(f.writes, w)
		send := map[string]any{"handle": h, "accepted": true, "bytesWritten": len(text)}
		if paste {
			send["prompt"] = map[string]any{"requestId": "r", "stages": []string{"input_accepted"}, "provider": "claude",
				"observation": "supported", "processIncarnation": t["incarnationId"], "generation": 1,
				"baselineWorkingSequence": 0}
		}
		return map[string]any{"send": send}
	}
	return Fail{"method_not_found"}
}

func (f *Fake) termLocked(h string) map[string]any {
	for _, t := range f.terms {
		if t["handle"] == h {
			return t
		}
	}
	return nil
}

func fnv32(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// validate checks a request's params the way Orca's zod schemas do (src/shared/rpc-contract), for the methods Persona
// calls: "" when valid, else the reason (answered as invalid_argument). Unknown keys pass, as zod strips them.
func validate(method string, p map[string]any) string {
	str := func(k string, required bool) string {
		v, ok := p[k]
		if !ok || v == nil && !required {
			if required {
				return "Missing " + k
			}
			return ""
		}
		if s, isStr := v.(string); !isStr || s == "" {
			return k + " must be a non-empty string"
		}
		return ""
	}
	boolean := func(k string) string {
		if v, ok := p[k]; ok {
			if _, isBool := v.(bool); !isBool {
				return k + " must be a boolean"
			}
		}
		return ""
	}
	literalTrue := func(k string) string {
		if v, ok := p[k]; ok && v != true {
			return k + " must be true"
		}
		return ""
	}
	first := func(reasons ...string) string {
		for _, r := range reasons {
			if r != "" {
				return r
			}
		}
		return ""
	}
	handle := func() string {
		if v, ok := p["expectedIncarnationId"]; ok {
			if s, isStr := v.(string); !isStr || s == "" {
				return "Missing PTY incarnation"
			}
		}
		return str("terminal", true)
	}
	switch method {
	case "status.get":
		return ""
	case "terminal.list":
		return first(boolean("includeVisualLayouts"), boolean("includePreview"), boolean("requireFreshPtyLiveness"))
	case "worktree.ps":
		if v, ok := p["afterSnapshotId"]; ok && v != nil {
			if s, isStr := v.(string); !isStr || s == "" || len(s) > 128 {
				return "afterSnapshotId must be a string of 1–128 characters or null"
			}
		}
		return literalTrue("acceptRowDelta")
	case "terminal.read":
		if p["screen"] == true && p["cursor"] != nil {
			return "Cursor cannot be combined with a screen read"
		}
		return first(handle(), literalTrue("screen"))
	case "terminal.agentStatus", "terminal.close":
		return handle()
	case "terminal.rename", "terminal.setPaneTitle":
		return first(handle(), str("title", true))
	case "repo.add":
		return str("path", true)
	case "worktree.set":
		return first(str("worktree", true), str("displayName", true))
	case "worktree.create":
		return first(str("repo", true), str("name", false), str("branchNameOverride", false), str("baseBranch", false),
			str("parentWorktree", false), boolean("activate"), boolean("nameWasGenerated"))
	case "terminal.create":
		return first(str("worktree", true), str("title", true))
	case "terminal.send":
		if r := first(handle(), literalTrue("agentPrompt"), literalTrue("requireWriteSettlement"),
			literalTrue("claimViewport")); r != "" {
			return r
		}
		if v, ok := p["text"]; ok && v != nil {
			if _, isStr := v.(string); !isStr {
				return "text must be a string"
			}
		}
		if v, ok := p["requireAgentStatus"]; ok && v != "sendable" {
			return "requireAgentStatus must be sendable"
		}
		if v, ok := p["client"]; ok {
			c, isObj := v.(map[string]any)
			if !isObj {
				return "client must be an object"
			}
			if id, _ := c["id"].(string); id == "" {
				return "Missing client ID"
			}
			if t, ok := c["type"]; ok && t != "mobile" && t != "desktop" {
				return "client.type must be mobile or desktop"
			}
		}
		_, hasText := p["text"]
		if !hasText && p["enter"] != true && p["interrupt"] != true {
			return "Missing terminal send payload"
		}
		return ""
	}
	return ""
}

// Write is one terminal.send or terminal.close the fake took: what reached the terminal.
type Write struct {
	Handle string
	Text   string
	Enter  bool
	// Pasted: delivered as an agent prompt (bracketed paste); else written raw.
	Pasted bool
	Close  bool
}

// Writes are the sends and closes taken so far.
func (f *Fake) Writes() []Write {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

// Invalid are the requests refused for their params so far ("method: reason").
func (f *Fake) Invalid() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.invalid)
}

// SetAgentStatus sets terminal.agentStatus for a handle: whether Orca sees an agent running in front, and its state
// ("" for unknown). Without it a terminal runs an agent of unknown state.
func (f *Fake) SetAgentStatus(handle string, running bool, status string) {
	var st any
	if status != "" {
		st = status
	}
	f.mu.Lock()
	f.agentStatus[handle] = map[string]any{"isRunningAgent": running, "status": st}
	f.mu.Unlock()
}

// SetRevision sets a worktree's topology revision (terminal.list's topologyRevisions).
func (f *Fake) SetRevision(worktree string, rev int) {
	f.mu.Lock()
	f.revisions[worktree] = rev
	f.mu.Unlock()
}

func (f *Fake) write(c net.Conn, msg any) {
	b, _ := json.Marshal(msg)
	_, _ = c.Write(append(b, '\n'))
}

// SetStatus replaces status.get's answer fields.
func (f *Fake) SetStatus(fields map[string]any) {
	f.mu.Lock()
	maps.Copy(f.status, fields)
	f.mu.Unlock()
}

// Keepalives makes every answer come after n keepalive lines.
func (f *Fake) Keepalives(n int) {
	f.mu.Lock()
	f.keepalive = n
	f.mu.Unlock()
}

// Terminal is a terminal.list entry on this machine.
func Terminal(handle, tab, leaf, worktree, path, branch, title, identity string) map[string]any {
	return map[string]any{"handle": handle, "ptyId": "pty-" + handle, "incarnationId": "inc-" + handle, "orphaned": false, "worktreeId": worktree,
		"worktreePath": path, "branch": branch, "tabId": tab, "leafId": leaf, "title": title, "connected": true,
		"writable": true, "lastOutputAt": 1, "preview": "", "executionHostId": "local", "agentIdentity": identity}
}

// SetTerminals replaces terminal.list's terminals; the layout gets one tab per tab id seen, titled "tab <id>".
func (f *Fake) SetTerminals(terms ...map[string]any) {
	byTree := map[string][]any{}
	seen := map[string]bool{}
	var order []string
	for _, t := range terms {
		w, tab := t["worktreeId"].(string), t["tabId"].(string)
		if _, ok := byTree[w]; !ok {
			order = append(order, w)
		}
		if !seen[tab] {
			seen[tab] = true
			byTree[w] = append(byTree[w], map[string]any{"tabId": tab, "title": "tab " + tab, "activeLeafId": t["leafId"]})
		}
	}
	var layouts []any
	for _, w := range order {
		layouts = append(layouts, map[string]any{"worktreeId": w, "root": map[string]any{"type": "group",
			"groupId": "g-" + w, "tabs": byTree[w]}})
	}
	f.mu.Lock()
	f.terms, f.layouts = terms, layouts
	f.mu.Unlock()
}

// Agent is a worktree.ps agent row.
func Agent(paneKey, agentType, state string) map[string]any {
	return map[string]any{"paneKey": paneKey, "parentPaneKey": nil, "state": state, "agentType": agentType,
		"hasLivePty": true, "updatedAt": 1, "stateStartedAt": 1}
}

// Worktree is a worktree.ps entry.
func Worktree(id, path, branch string, agents ...map[string]any) map[string]any {
	a := make([]any, len(agents))
	for i, x := range agents {
		a[i] = x
	}
	return map[string]any{"worktreeId": id, "path": path, "branch": branch, "displayName": filepath.Base(path),
		"repo": filepath.Base(path), "agents": a}
}

// SetWorktrees replaces worktree.ps's worktrees.
func (f *Fake) SetWorktrees(trees ...map[string]any) {
	f.mu.Lock()
	f.trees = trees
	f.mu.Unlock()
}

// SetScreen sets terminal.read's answer for a handle: its lines (none wrapped).
func (f *Fake) SetScreen(handle string, text string) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	rows := make([]any, len(lines))
	for i := range rows {
		rows[i] = map[string]any{"wrapped": false, "trailingSpaces": 0}
	}
	f.mu.Lock()
	f.screens[handle] = map[string]any{"tail": lines, "tailRows": rows, "returnedLineCount": len(lines)}
	f.mu.Unlock()
}

// SetScreenRows sets terminal.read's answer with explicit rows and wrap flags.
func (f *Fake) SetScreenRows(handle string, lines []string, wrapped []bool) {
	rows := make([]any, len(lines))
	for i := range rows {
		rows[i] = map[string]any{"wrapped": i < len(wrapped) && wrapped[i]}
	}
	f.mu.Lock()
	f.screens[handle] = map[string]any{"tail": lines, "tailRows": rows}
	f.mu.Unlock()
}

// Handle makes a method answer through fn (a Fail for an error).
func (f *Fake) Handle(method string, fn func(params map[string]any) any) {
	f.mu.Lock()
	f.on[method] = fn
	f.mu.Unlock()
}

// Trace is the calls so far ("method {params}"), only of the given methods when any are named, else all but the
// listing ones every refresh makes (status.get, terminal.list, worktree.ps).
func (f *Fake) Trace(methods ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.log {
		if len(methods) > 0 && !slices.Contains(methods, c.Method) ||
			len(methods) == 0 && slices.Contains([]string{"status.get", "terminal.list", "worktree.ps"}, c.Method) {
			continue
		}
		out = append(out, c.String())
	}
	return out
}

// Params are the params of every call of method so far, in order.
func (f *Fake) Params(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.log {
		if c.Method == method {
			out = append(out, c.Params)
		}
	}
	return out
}

// Count is how many calls of method came.
func (f *Fake) Count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.log {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Reset forgets the calls and writes so far.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.log, f.writes = nil, nil
	f.mu.Unlock()
}

// Token is the auth token the fake takes now (tests check it never leaks into logs).
func (f *Fake) Token() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

// SendCall formats an expected terminal.send trace line.
func SendCall(handle string, fields map[string]any) string {
	p := map[string]any{"terminal": handle}
	maps.Copy(p, fields)
	b, _ := json.Marshal(p)
	return fmt.Sprintf("terminal.send %s", b)
}
