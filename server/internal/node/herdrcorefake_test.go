package node

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdrcore"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// fakeHC is a scriptable herdr-core socket (JSON-RPC 2.0, one message per line): it serves connections at once (an
// events.subscribe connection stays open), records every call but the subscription in order, answers terminal.list,
// terminal.get, terminal.read and doc.get from its state, and pushes events to its subscribers.
type fakeHC struct {
	sock string

	mu        sync.Mutex
	log       []herdrCall
	terms     []map[string]any
	doc       any
	docVer    float64 // the document's version: doc.set writes only at it
	ran       int     // terminals terminal.run started (t101, t102…)
	screen    map[string]string
	on        map[string]func(p map[string]any) any
	listDelay time.Duration
	subs      []net.Conn
	subbed    chan struct{}
}

// hcFail makes a handler answer with a herdr-core error of that kind.
type hcFail struct{ Kind string }

func newFakeHC(t *testing.T) *fakeHC {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeHC{sock: filepath.Join(dir, "c.sock"), screen: map[string]string{}, on: map[string]func(map[string]any) any{},
		subbed: make(chan struct{}, 8), docVer: 1}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		for _, c := range f.subs {
			c.Close()
		}
		f.mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serveConn(c)
		}
	}()
	return f
}

func (f *fakeHC) serveConn(c net.Conn) {
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		c.Close()
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
	}
	_ = json.Unmarshal(line, &req)
	if req.Params == nil {
		req.Params = map[string]any{}
	}
	if req.Method == "events.subscribe" {
		f.write(c, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"seq": 0, "terminals": []any{}}})
		f.mu.Lock()
		f.subs = append(f.subs, c)
		f.mu.Unlock()
		f.subbed <- struct{}{}
		return // kept open for push
	}
	defer c.Close()
	f.mu.Lock()
	f.log = append(f.log, herdrCall{Method: req.Method, Params: req.Params})
	h, delay := f.on[req.Method], f.listDelay
	var result any = map[string]any{}
	switch req.Method {
	case "terminal.list":
		result = map[string]any{"terminals": slices.Clone(f.terms), "hidden_finished": 0}
	case "terminal.get":
		result = hcFail{"not_found"}
		for _, t := range f.terms {
			if t["address"] == req.Params["terminal"] {
				result = t
			}
		}
	case "terminal.read":
		result = map[string]any{"text": f.screen[str(req.Params["terminal"])], "complete": true, "how": "screen"}
	case "terminal.label":
		if h != nil {
			break
		}
		for _, t := range f.terms {
			if t["address"] == req.Params["terminal"] {
				labels, _ := t["labels"].(map[string]any)
				if labels == nil {
					labels = map[string]any{}
					for key, value := range t["labels"].(map[string]string) {
						labels[key] = value
					}
				}
				for key, value := range req.Params["labels"].(map[string]any) {
					labels[key] = value
				}
				t["labels"], result = labels, t
			}
		}
	case "doc.get":
		result = map[string]any{"value": f.doc, "version": f.docVer}
	case "doc.set":
		if h != nil {
			break // the handler answers (and decides what is written)
		}
		if v, _ := req.Params["version"].(float64); v != f.docVer {
			result = hcFail{"conflict"}
		} else {
			f.doc, f.docVer = req.Params["value"], f.docVer+1
			result = map[string]any{"version": f.docVer}
		}
	case "terminal.run":
		if h != nil {
			break
		}
		// The machine's shell in a new terminal, in front.
		f.ran++
		cwd, _ := req.Params["cwd"].(string)
		t := hcTerminal(fmt.Sprintf("t%d", 100+f.ran), "", "", 9000+f.ran, "zsh", cwd, "", "", "")
		if l, ok := req.Params["labels"].(map[string]any); ok {
			t["labels"] = l
		}
		f.terms = append(f.terms, t)
		result = t
	}
	f.mu.Unlock()
	if req.Method == "terminal.list" && delay > 0 {
		time.Sleep(delay)
	}
	if h != nil {
		result = h(req.Params)
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if e, ok := result.(hcFail); ok {
		resp["error"] = map[string]any{"code": -32000, "message": e.Kind, "data": map[string]any{"kind": e.Kind}}
	} else {
		resp["result"] = result
	}
	f.write(c, resp)
}

func (f *fakeHC) write(c net.Conn, msg any) {
	b, _ := json.Marshal(msg)
	_, _ = c.Write(append(b, '\n'))
}

// push sends one notification to every subscriber.
func (f *fakeHC) push(method string, params any) {
	f.mu.Lock()
	subs := slices.Clone(f.subs)
	f.mu.Unlock()
	for _, c := range subs {
		f.write(c, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
}

func (f *fakeHC) setTerminals(terms ...map[string]any) {
	f.mu.Lock()
	f.terms = terms
	f.mu.Unlock()
}

func (f *fakeHC) setScreen(terminal, text string) {
	f.mu.Lock()
	f.screen[terminal] = text
	f.mu.Unlock()
}

func (f *fakeHC) handle(method string, fn func(p map[string]any) any) {
	f.mu.Lock()
	f.on[method] = fn
	f.mu.Unlock()
}

// trace is the calls so far ("method {params}"), leaving out the listing ones a refresh makes (terminal.list,
// doc.get) unless asked for.
func (f *fakeHC) trace(methods ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.log {
		if len(methods) > 0 && !slices.Contains(methods, c.Method) ||
			len(methods) == 0 && (c.Method == "terminal.list" || c.Method == "doc.get") {
			continue
		}
		out = append(out, c.String())
	}
	return out
}

func (f *fakeHC) reset() {
	f.mu.Lock()
	f.log = nil
	f.mu.Unlock()
}

// hcTerminal is a terminal.list entry: status nil when app is "", the Herdr app's labels when space is set.
func hcTerminal(addr, app, state string, fgPid int, fgArgv0, cwd, space, tab, where string) map[string]any {
	t := map[string]any{
		"address": addr, "argv": []string{"zsh"}, "cwd": cwd, "state": "live", "created_ms": 1, "screen": "exact",
		"cols": 120, "rows": 40, "pid": fgPid - 1, "title": "✳ " + addr, "turn": 1, "labels": map[string]string{},
		"status": nil,
	}
	if app != "" {
		t["status"] = map[string]any{"app": app, "state": state}
	}
	if fgPid > 0 {
		t["foreground"] = map[string]any{"pid": fgPid, "name": "2.1.0", "argv": []string{fgArgv0}, "cwd": cwd}
	}
	if space != "" {
		t["labels"] = map[string]string{herdrSpaceLabel: space, herdrTabLabel: tab, herdrWhereLabel: where}
	}
	return t
}

// hcNode is a Node whose engine is herdr-core, on a fake herdr-core; the fake herdr it also gets (herdrNode's) is not
// registered, so a call to it would show in its trace.
func hcNode(t *testing.T) (*Node, *fakeHerdr, *fakeHC, string) {
	t.Helper()
	n, f, home := herdrNode(t)
	hc := newFakeHC(t)
	n.hosts = host.NewRegistry(&herdrCoreHost{c: &herdrcore.Client{Socket: hc.sock}})
	n.eng.name = proto.EngineHerdrCore
	return n, f, hc, home
}

// writeClaudePID writes ~/.claude/sessions/<pid>.json naming the session a claude process runs.
func writeClaudePID(t *testing.T, home string, pid int, id string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id})
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
