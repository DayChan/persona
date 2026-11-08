package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A caller is the assistant's when its process, or one of its ancestors, is an assistant process the node started.
func TestAssistantOf(t *testing.T) {
	n, _ := testNode(t)
	n.procs["s1"] = &assistantProc{id: "s1", cmd: &exec.Cmd{Process: &os.Process{Pid: 100}}}
	n.procs["s2"] = &assistantProc{id: "s2"} // not started yet
	parents := map[int]int{300: 200, 200: 100, 100: 1, 400: 1, 500: 500}
	parent := func(pid int) (int, error) {
		if p, ok := parents[pid]; ok {
			return p, nil
		}
		return 0, os.ErrNotExist
	}
	for _, c := range []struct {
		pid  int
		want string
	}{{300, "s1"}, {100, "s1"}, {400, ""}, {500, ""}, {999, ""}} {
		if got, ok := n.assistantOf(c.pid, parent); got != c.want || ok != (c.want != "") {
			t.Errorf("pid %d: %q %v, want %q", c.pid, got, ok, c.want)
		}
	}
}

// The other end of the node's socket is known on the systems the node runs on, and so are its parents.
func TestPeerProcess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here: every caller is refused")
	}
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("unix", ln.Addr().String())
		if err == nil {
			defer c.Close()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if pid, err := peerPID(c); err != nil || pid != os.Getpid() {
		t.Fatalf("peer %d %v, want %d", pid, err, os.Getpid())
	}
	if pid, err := parentPID(os.Getpid()); err != nil || pid != os.Getppid() {
		t.Fatalf("parent %d %v, want %d", pid, err, os.Getppid())
	}
}

// The command lines' calls reach the hub only from the assistant's processes, as the assistant's, and only for the
// connectors signed in.
func TestCLICallOnlyForTheAssistant(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, _ := testNode(t)
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var asked []map[string]any
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		asked = append(asked, p)
		mu.Unlock()
		return proto.TextResult(`{"chats":[]}`, false), nil
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { _ = n2hW.Close(); _ = h2nW.Close() })

	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serveSock(ctx, c)
		}
	}()
	ask := func(name string) proto.ToolResult {
		t.Helper()
		c, err := net.Dial("unix", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(map[string]any{"kind": "cli.call", "name": name, "args": map[string]any{}, "assistant": true})
		_, _ = c.Write(append(b, '\n'))
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var res proto.ToolResult
		_ = json.Unmarshal(line, &res)
		return res
	}

	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	first := prepareAssistantForTest(t, n, ".persona/assistant", []string{"example"}, nil)
	second := prepareAssistantForTest(t, n, ".persona/managers/second/assistant", []string{"other"}, nil)
	// This test is no assistant process, whatever it says.
	if res := ask("example_list"); !res.IsError || !strings.Contains(res.Content[0].Text, "只能在助手的对话里用") || len(asked) != 0 {
		t.Fatalf("not the assistant: %+v, the hub was asked %v", res, asked)
	}
	n.mu.Lock()
	n.procs["s1"] = &assistantProc{id: "s1", cwd: first, cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	n.mu.Unlock()
	if res := ask("phone_contacts"); !res.IsError || len(asked) != 0 {
		t.Fatalf("not a signed-in connector's: %+v", res)
	}
	if res := ask("example_list"); res.IsError {
		t.Fatalf("the assistant: %+v", res)
	}
	if res := ask("other_list"); !res.IsError {
		t.Fatalf("first assistant called the second one's connector: %+v", res)
	}
	n.mu.Lock()
	delete(n.procs, "s1")
	n.procs["s2"] = &assistantProc{id: "s2", cwd: second, cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	n.mu.Unlock()
	if res := ask("example_list"); !res.IsError {
		t.Fatalf("second assistant called the first one's connector: %+v", res)
	}
	if res := ask("other_list"); res.IsError {
		t.Fatalf("second assistant: %+v", res)
	}
	for _, cwd := range []string{"", filepath.Join(n.dir, "unprepared")} {
		n.mu.Lock()
		n.procs["s2"].cwd = cwd
		n.mu.Unlock()
		if res := ask("other_list"); !res.IsError {
			t.Fatalf("unprepared directory called a connector: %+v", res)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || asked[0]["name"] != "example_list" || asked[0]["assistant"] != true || asked[0]["session_id"] != "s1" ||
		asked[1]["name"] != "other_list" || asked[1]["assistant"] != true || asked[1]["session_id"] != "s2" {
		t.Fatalf("the hub was asked %v", asked)
	}
}

// The tg an older node wrote for the built-in Telegram (personad tg) goes at the next message, Telegram signed in or
// not: Telegram's command line is its extension's now (#1704), which the hub names in clis like any connector's.
func TestPrepareTakesAwayTheBuiltInTg(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	prepare := func(conns []string, clis map[string]string) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"dir": ".persona/assistant", "claude_md": "# 示例", "connectors": conns, "clis": clis})
		if _, err := n.handle(context.Background(), "assistant.prepare", params); err != nil {
			t.Fatal(err)
		}
	}
	tg := filepath.Join(n.dir, "assistant-bin", "tg")
	_ = os.MkdirAll(filepath.Dir(tg), 0o755)
	old := "#!/bin/sh\n# Telegram through the hub (the telegram skill), on the assistant's PATH while it is signed in (node/clicall.go).\n" +
		"exec /opt/example/personad tg \"$@\"\n"
	_ = os.WriteFile(tg, []byte(old), 0o755)
	prepare([]string{"telegram"}, nil)
	if _, err := os.Stat(tg); !os.IsNotExist(err) {
		t.Fatalf("the built-in's tg stayed: %v", err)
	}
	prepare([]string{"telegram"}, map[string]string{"tg": "telegram"})
	if b, err := os.ReadFile(tg); err != nil || !strings.Contains(string(b), "exec /opt/example/personad tool --cli telegram \"$@\"") {
		t.Fatalf("the extension's tg: %q %v", b, err)
	}
	prepare(nil, nil)
	if n.mayCall(n.assistantDirOf(".persona/assistant"), "telegram_list_chats") {
		t.Fatal("Telegram signed out, its tools are still called")
	}
}

// A connector with a command line of its own (connector.Manifest.CLI, #1704): the hub says it with the connectors, the
// node puts it on the assistant's PATH as personad tool --cli <id>, lets the
// command lines call that connector's tools, and takes away only the command lines it wrote once the connector is
// signed out (a plugin's gate commands live there too).
func TestConnectorCommandLines(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	n, _ := testNode(t)
	n.dir, n.bin, n.logger = t.TempDir(), "/opt/example/personad", log.New(io.Discard, "", 0)
	prepare := func(conns []string, clis map[string]string) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"dir": ".persona/assistant", "claude_md": "# 示例", "connectors": conns, "clis": clis})
		if _, err := n.handle(context.Background(), "assistant.prepare", params); err != nil {
			t.Fatal(err)
		}
	}
	cwd := n.assistantDirOf(".persona/assistant")
	bin := filepath.Join(n.dir, "assistant-bin")
	_ = os.MkdirAll(bin, 0o755)
	gate := filepath.Join(bin, "ol") // a plugin's gate command: not the node's to take away
	_ = os.WriteFile(gate, []byte("#!/bin/sh\nexec sh /x/ol.sh \"$@\"\n"), 0o755)

	prepare([]string{"telegram", "example"}, map[string]string{"tg": "telegram", "ex": "example"})
	for name, id := range map[string]string{"tg": "telegram", "ex": "example"} {
		if b, err := os.ReadFile(filepath.Join(bin, name)); err != nil || !strings.Contains(string(b), "exec /opt/example/personad tool --cli "+id+" \"$@\"") {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
	if !n.mayCall(cwd, "example_list") || !n.mayCall(cwd, "telegram_cli") || n.mayCall(cwd, "phone_contacts") || n.mayCall(cwd, "examplex_list") {
		t.Fatal("the command lines call the signed-in connectors' tools, and only those")
	}

	prepare([]string{"telegram"}, nil) // signed in, and no command line said: none on the PATH
	if _, err := os.Stat(filepath.Join(bin, "tg")); !os.IsNotExist(err) {
		t.Fatalf("no command line said, tg stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bin, "ex")); !os.IsNotExist(err) {
		t.Fatalf("example signed out, ex stayed: %v", err)
	}
	if n.mayCall(cwd, "example_list") {
		t.Fatal("example signed out, its tools are still called")
	}
	prepare(nil, nil)
	if _, err := os.Stat(filepath.Join(bin, "tg")); !os.IsNotExist(err) {
		t.Fatalf("signed out, tg stayed: %v", err)
	}
	if _, err := os.Stat(gate); err != nil {
		t.Fatal("a command the node did not write was taken away")
	}
}

// mcpRelay is a node socket served by n and the hub end of n's connection: ask sends one of the MCP server's requests
// and gives what the node relayed to the hub for it (assistant, session_id…).
func mcpRelay(t *testing.T, n *Node, hub *runtimeHub) (ask func(req map[string]any) map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var relayed []map[string]any
	hub.mu.Lock()
	hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "mcp.tools":
			relayed = append(relayed, p)
			return map[string]any{"tools": []proto.Tool{}}, nil, true
		case "mcp.call":
			relayed = append(relayed, p)
			return proto.TextResult("ok", false), nil, true
		}
		return nil, nil, false
	}
	hub.mu.Unlock()
	dir, err := os.MkdirTemp("/tmp", "mcp") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serveSock(ctx, c)
		}
	}()
	return func(req map[string]any) map[string]any {
		t.Helper()
		mu.Lock()
		before := len(relayed)
		mu.Unlock()
		c, err := net.Dial("unix", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b, _ := json.Marshal(req)
		_, _ = c.Write(append(b, '\n'))
		if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(relayed) != before+1 {
			t.Fatalf("%v: the hub was asked %d times", req, len(relayed)-before)
		}
		return relayed[before]
	}
}

func mcpTools(assistant bool, session string) map[string]any {
	return map[string]any{"kind": "mcp.tools", "assistant": assistant, "session_id": session}
}

func mcpCall(assistant bool, session string) map[string]any {
	return map[string]any{"kind": "mcp.call", "name": "kb_search", "args": map[string]any{}, "assistant": assistant, "session_id": session}
}

// An MCP server's word that it is the assistant's (PERSONA_ASSISTANT, which every process started with the
// assistant's environment repeats) reaches the hub only from a process the node runs for the assistant, with that
// conversation's session. Any other caller's request goes as an agent's; one that does not say so is not made one.
func TestMCPAssistantOnlyFromTheAssistantsProcesses(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here: TestMCPAssistantWithoutPeer")
	}
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	check := func(req map[string]any, assistant bool, session string) {
		t.Helper()
		if got := ask(req); got["assistant"] != assistant || got["session_id"] != session {
			t.Errorf("%v: relayed %v, want assistant %v and session %q", req, got, assistant, session)
		}
	}
	self := &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}

	// This test is no assistant process, whatever its server says: an agent with the assistant's environment.
	check(mcpTools(true, ""), false, "")
	check(mcpTools(true, "s1"), false, "s1")
	check(mcpCall(true, "s1"), false, "s1")

	n.mu.Lock()
	n.procs["s1"] = &assistantProc{id: "s1", cmd: self}
	n.mu.Unlock()
	check(mcpTools(true, "s1"), true, "s1")
	check(mcpTools(true, ""), true, "s1") // the conversation's session, whatever the server names
	check(mcpCall(true, "other"), true, "s1")
	check(mcpTools(false, "x"), false, "x")

	// The warm spare's server lists the tools as it starts, before a conversation adopts it.
	n.mu.Lock()
	delete(n.procs, "s1")
	n.spare = &assistantProc{id: "spare-1", cmd: self}
	n.mu.Unlock()
	check(mcpTools(true, "spare-1"), true, "spare-1")

	// A Codex process's server says the process's key until its thread exists.
	n.mu.Lock()
	n.spare = nil
	n.akeys = map[string]*assistantProc{"proc-k": {cmd: self, cx: &codexConn{key: "proc-k"}}}
	n.mu.Unlock()
	check(mcpTools(true, "proc-k"), true, "proc-k")

	n.mu.Lock()
	n.akeys = map[string]*assistantProc{}
	n.mu.Unlock()
	check(mcpCall(true, "proc-k"), false, "proc-k")
}

// A runtime's assistant conversations run in its own processes (assistant_runtime.go): a connected runtime that runs
// them (caps.assistant) vouches for its descendants' word, with the session they name; one that does not, for nothing.
func TestMCPAssistantFromARuntime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	// This test is the process at the other end of the runtime socket, and the MCP server's. It is the node's too,
	// which a runtime is not (TestMCPAssistantNotFromARelay). Swapped before the node listens: its connections read it.
	lineageWas := nodeLineage
	t.Cleanup(func() { nodeLineage = lineageWas })
	nodeLineage = func() []int { return nil }
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	check := func(req map[string]any, assistant bool) {
		t.Helper()
		if got := ask(req); got["assistant"] != assistant || got["session_id"] != "session-a" {
			t.Errorf("%v: relayed %v, want assistant %v", req, got, assistant)
		}
	}
	dialRuntime(t, n, map[string]any{"runtime": "plain", "caps": map[string]any{"start": true, "send": true}})
	check(mcpTools(true, "session-a"), false)
	check(mcpCall(true, "session-a"), false)
	dialRuntime(t, n, assistantRuntimeHello())
	check(mcpTools(true, "session-a"), true) // no runtime lists it yet: a chat lists the tools as it starts
	check(mcpCall(true, "session-a"), true)

	// Only a session that can be one of this runtime's assistant chats.
	conn := func(rt string) *runtimeConn {
		n.runtimes.mu.Lock()
		defer n.runtimes.mu.Unlock()
		return n.runtimes.byRuntime(rt)
	}
	n.runtimes.claim(conn("plain"), "session-p")
	n.runtimes.claim(conn("dsh"), "session-w") // the runtime's, an agent's chat
	n.runtimes.claim(conn("dsh"), "session-r")
	n.state.put("session-r", sessionState{Cwd: identity.AssistantDir(n.home)})
	n.mu.Lock()
	n.akeys = map[string]*assistantProc{"proc-k": {cmd: &exec.Cmd{Process: &os.Process{Pid: 1 << 22}}, cx: &codexConn{key: "proc-k"}}}
	n.mu.Unlock()
	for _, c := range []struct {
		session string
		ok      bool
	}{{"session-r", true}, {"", false}, {"0d000000-0000-4000-8000-00000000000a", false}, {"session-p", false}, {"session-w", false}, {"proc-k", false}} {
		if got := ask(mcpTools(true, c.session)); got["assistant"] != c.ok || got["session_id"] != c.session {
			t.Errorf("%q: relayed %v, want assistant %v", c.session, got, c.ok)
		}
	}
}

// A connection into the runtime socket from the node's own process or an ancestor of it is a relay (the manager's
// egress proxy leading an extension's sandbox there; the manager runs the node), not the runtime's process: it
// vouches for nothing, or everything the node runs would be the assistant.
func TestMCPAssistantNotFromARelay(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	dialRuntime(t, n, assistantRuntimeHello()) // from this test: the node's process
	if got := ask(mcpTools(true, "session-a")); got["assistant"] != false {
		t.Fatalf("relayed %v", got)
	}
}

// Where the system does not tell who is at the other end of the socket (Windows), the request must name a session
// the node runs for the assistant, or an assistant conversation of a connected runtime that runs them.
func TestMCPAssistantWithoutPeer(t *testing.T) {
	n, _ := testNode(t)
	n.procs["s1"] = &assistantProc{id: "s1", cmd: &exec.Cmd{Process: &os.Process{Pid: 100}}}
	n.akeys = map[string]*assistantProc{"proc-k": {cmd: &exec.Cmd{Process: &os.Process{Pid: 200}}, cx: &codexConn{key: "proc-k"}}}
	n.state.put("session-r", sessionState{Cwd: identity.AssistantDir(n.home)})
	n.state.put("session-w", sessionState{Cwd: filepath.Join(n.home, "work")})
	check := func(cases map[string]bool) {
		t.Helper()
		for session, want := range cases {
			if got, ok := n.mcpAssistantOf(0, errNoPeer, nil, session); ok != want || ok && got != session {
				t.Errorf("%q: %q %v, want %v", session, got, ok, want)
			}
		}
	}
	// session-r is an assistant conversation by its directory, but no connected runtime has it.
	check(map[string]bool{"s1": true, "proc-k": true, "session-r": false, "session-w": false, "other": false, "": false})

	n.runtimes = newRuntimeRegistry()
	connect := func(rt string, assistant bool) *runtimeConn {
		h := n.runtimes.add(nil)
		h.mu.Lock()
		h.ready, h.hello.Runtime, h.hello.Caps.Assistant = true, rt, assistant
		h.mu.Unlock()
		return h
	}
	n.state.put("session-p", sessionState{Cwd: identity.AssistantDir(n.home)})
	n.runtimes.claim(connect("plain", false), "session-p") // a runtime that runs no assistant
	n.runtimes.claim(connect("dsh", true), "session-r")
	n.runtimes.claim(connect("dsh2", true), "session-w") // not an assistant conversation
	check(map[string]bool{"session-r": true, "session-p": false, "session-w": false})
	// Any other reason the peer is not known (not a unix socket) is no assistant's.
	if got, ok := n.mcpAssistantOf(0, errors.New("not a unix socket"), nil, "s1"); ok {
		t.Errorf("a peer not known for another reason: %q", got)
	}
}
