package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// testNode is a node in this process: it answers what the manager asks, records it, and can send events and
// requests of its own.
type testNode struct {
	mu       sync.Mutex
	calls    []string // "method params"
	sessions []proto.Session
	peer     *rpc.Peer
	refuse   error // every call fails with it (a node error)
	// events are the one-way events it read; stall, when set, keeps it from reading on after one (a node that stopped
	// taking them) until closed.
	events []string
	stall  chan struct{}
	// info, when set, is what node.info answers (a node that announces the handshake's capabilities).
	info *proto.NodeInfo
	// echoHealth: it says which health space it answered about (health_dir), as a node with health_spaces does.
	echoHealth bool
	// refuseWatch: how many more session.watch calls of a session it refuses (a node just started, #6372).
	refuseWatch map[string]int
}

func (n *testNode) dial(context.Context) (net.Conn, error) {
	managerSide, nodeSide := net.Pipe()
	p := rpc.New("n", nodeSide)
	p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.calls = append(n.calls, method+" "+string(params))
		switch method {
		case "node.info":
			if n.info != nil {
				return *n.info, nil
			}
			return proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test"}, nil
		case "sessions.list":
			return map[string]any{"sessions": n.sessions}, nil
		case "session.watch":
			var w struct {
				SessionID string `json:"session_id"`
			}
			if _ = json.Unmarshal(params, &w); n.refuseWatch[w.SessionID] > 0 {
				n.refuseWatch[w.SessionID]--
				return nil, errors.New("transcript for session " + w.SessionID + " not found")
			}
		}
		if n.refuse != nil {
			return nil, n.refuse
		}
		var p struct {
			HealthDir string `json:"health_dir"`
		}
		if _ = json.Unmarshal(params, &p); n.echoHealth && p.HealthDir != "" {
			return map[string]any{"ok": true, "method": method, "health_dir": p.HealthDir}, nil
		}
		return map[string]any{"ok": true, "method": method}, nil
	}
	p.OnEvent = func(event string, params json.RawMessage) {
		n.mu.Lock()
		n.events = append(n.events, event+" "+string(params))
		stall := n.stall
		n.mu.Unlock()
		if stall != nil {
			<-stall
		}
	}
	n.mu.Lock()
	n.peer = p
	n.mu.Unlock()
	go func() { _ = p.Serve(context.Background(), nodeSide); nodeSide.Close() }()
	return managerSide, nil
}

func (n *testNode) eventsSeen() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string{}, n.events...)
}

func (n *testNode) seen(prefix string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, c := range n.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func (n *testNode) current() *rpc.Peer {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.peer
}

// connectedMachine runs machine id of d on node n and waits until it is connected.
func connectedMachine(t *testing.T, d *Devices, id string, n *testNode) *Machine {
	t.Helper()
	m := newMachine(d, MachineConfig{ID: id, Name: id, Kind: "ssh"})
	m.dialNode = n.dial
	d.addEntry(m)
	runMachine(t, m)
	eventually(t, 5*time.Second, id+" connected", func() bool { return m.reachable() && len(n.seen("sessions.list")) > 0 })
	return m
}

// runMachine runs m until the test ends (or the returned cancel) and waits for run to return in t.Cleanup: a run left
// behind would still read package vars that a later test swaps. A run that does not end fails the test rather than
// hanging the package.
func runMachine(t *testing.T, m *Machine) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("[%s] run did not return after its context ended", m.cfg.ID)
		}
	})
	return cancel
}

// A call from the hub reaches the node; what goes wrong is coded so the hub knows it again: no such machine, the node
// offline, the node's own refusal as it is.
func TestCallCodesErrorsForTheHub(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	out, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: "fs.list", Params: json.RawMessage(`{"path":"/"}`), TimeoutMS: 2000})
	if err != nil || !strings.Contains(string(out), `"fs.list"`) {
		t.Fatalf("call: %s %v", out, err)
	}
	var re *rpc.Error
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "nope", Method: "fs.list"}); !errors.As(err, &re) || re.Code != devicewire.CodeNoDevice {
		t.Fatalf("no machine: %v", err)
	}
	n.mu.Lock()
	n.refuse = &rpc.Error{Code: "input_draft", Message: "a draft is in the way"}
	n.mu.Unlock()
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: "session.send"}); !errors.As(err, &re) || re.Code != "input_draft" {
		t.Fatalf("the node's own refusal: %v", err)
	}
	off := newMachine(d, MachineConfig{ID: "off", Name: "off"})
	d.addEntry(off)
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "off", Method: "fs.list"}); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline {
		t.Fatalf("offline: %v", err)
	}
	// The assistant's own calls (device.call, made here) keep the hub's kinds.
	if _, err := d.CallNode(context.Background(), "off", "fs.list", nil); !errors.Is(err, apierr.ErrOffline) {
		t.Fatalf("offline, from the manager itself: %v", err)
	}
	// What may have reached the node (its connection dropped with the call out, the time ran out) is coded apart
	// from what never left.
	for err, code := range map[error]string{rpc.ErrDropped: devicewire.CodeNodeClosed, context.DeadlineExceeded: devicewire.CodeDeadline,
		context.Canceled: devicewire.CodeNodeClosed, rpc.ErrClosed: devicewire.CodeMachineOffline} {
		if !errors.As(wireError(fmt.Errorf("box: %w", err)), &re) || re.Code != code {
			t.Fatalf("%v coded %v, want %s", err, wireError(err), code)
		}
	}
}

// Every event of a node goes on to the hub, in order; what is the manager's alone (state.put) is kept here and not
// sent on; its session list is the manager's too.
func TestEventsGoToTheHubAsTheyCame(t *testing.T) {
	d := newTestDevices(t)
	st := &nodeStates{memStore: newMemStore()}
	d.opts.Store = st
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	m.noteWatch("session.watch", json.RawMessage(`{"session_id":"s1"}`)) // the hub watches s1: its last seq is kept
	p := n.current()
	_ = p.Notify("sessions.changed", map[string]any{"sessions": []proto.Session{{SessionID: "s1", Status: proto.StatusWorking}}})
	_ = p.Notify("session.items", map[string]any{"session_id": "s1", "items": []proto.Item{{Seq: 7, Kind: "user", Text: "hi"}}})
	_ = p.Notify("terminal.frame", map[string]any{"session_id": "s1", "seq": 1})
	_ = p.Notify("state.put", map[string]any{"session_id": "s1", "state": map[string]any{}})
	_ = p.Notify("stream.data", map[string]any{"id": "x", "data": "AA=="})
	f := d.host()
	eventually(t, 5*time.Second, "the events at the hub", func() bool { return len(f.eventsOf("box", "stream.data")) == 1 })
	f.mu.Lock()
	var names []string
	for _, e := range f.events {
		if e.MachineID == "box" {
			names = append(names, e.Event)
		}
	}
	f.mu.Unlock()
	want := []string{"sessions.changed", "sessions.changed", "session.items", "terminal.frame", "stream.data"}
	if !slices.Equal(names, want) { // the first: the list as the node gave it on connecting
		t.Fatalf("forwarded %v, want %v", names, want)
	}
	if s, ok := m.session("s1"); !ok || s.Status != proto.StatusWorking {
		t.Fatalf("the manager's list: %+v", m.sessionList())
	}
	eventually(t, 2*time.Second, "state.put kept", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.puts == 1
	})
	m.mu.Lock()
	seq := m.lastSeq["s1"]
	m.mu.Unlock()
	if seq != 7 {
		t.Fatalf("last seq %d", seq)
	}
}

// nodeStates keeps state.put in memory.
type nodeStates struct {
	*memStore
	mu   sync.Mutex
	puts int
}

func (s *nodeStates) PutNodeSession(context.Context, string, string, db.NodeSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	return nil
}

func (s *nodeStates) NodeSessions(context.Context, string) (map[string]db.NodeSession, error) {
	return map[string]db.NodeSession{"s1": {}}, nil
}

// A node's requests: what is the manager's it answers (its sessions' state, the assistant's own), the rest goes to
// the hub with the machine's id.
func TestNodeRequests(t *testing.T) {
	d := newTestDevices(t)
	d.opts.Store = &nodeStates{memStore: newMemStore()}
	d.opts.Answer = func(_ context.Context, machine, method string, _ json.RawMessage) (any, bool, error) {
		if method == "assistant.context" {
			return map[string]any{"text": "now on " + machine}, true, nil
		}
		return nil, false, nil
	}
	f := d.host()
	f.request = func(_ context.Context, p devicewire.NodeCall) (json.RawMessage, error) {
		return json.Marshal(map[string]any{"from": "hub", "machine": p.MachineID, "method": p.Method})
	}
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	p := n.current()
	ask := func(method string) map[string]any {
		t.Helper()
		var out map[string]any
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.Call(ctx, method, map[string]any{}, &out); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return out
	}
	if out := ask("interaction.create"); out["from"] != "hub" || out["machine"] != "box" || out["method"] != "interaction.create" {
		t.Fatalf("to the hub: %v", out)
	}
	if out := ask("assistant.context"); out["text"] != "now on box" {
		t.Fatalf("the assistant's own: %v", out)
	}
	if out := ask("state.load"); out["sessions"] == nil {
		t.Fatalf("state.load: %v", out)
	}
}

// A node's request the hub is still on when the node's connection ends: the hub is told (devices.request_end, the
// request's key), as it learnt from a node of its own that its connection ended.
func TestARequestEndsWithItsConnection(t *testing.T) {
	d := newTestDevices(t)
	f := d.host()
	got := make(chan string, 1)
	f.request = func(ctx context.Context, p devicewire.NodeCall) (json.RawMessage, error) {
		got <- p.Request
		<-ctx.Done() // the bus client gives up when ctx does
		return nil, ctx.Err()
	}
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	go func() { _ = n.current().Call(context.Background(), "interaction.create", map[string]any{}, nil) }()
	var key string
	select {
	case key = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("the request never reached the hub")
	}
	if key == "" {
		t.Fatal("the request has no key")
	}
	m.reconnect()
	eventually(t, 3*time.Second, "devices.request_end", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.ended) == 1 && f.ended[0] == devicewire.RequestEnd{MachineID: "box", Request: key}
	})
}

// A computer slow to take the hub's one-way events holds up only its own: the bus's read loop goes on at once, another
// machine's events go through, and one stalled long enough is connected again.
func TestASlowNodeHoldsUpOnlyItsOwnEvents(t *testing.T) {
	old := noteStall
	noteStall = 300 * time.Millisecond
	t.Cleanup(func() { noteStall = old })
	d := newTestDevices(t)
	slow, fast := &testNode{stall: make(chan struct{})}, &testNode{}
	connectedMachine(t, d, "slow", slow)
	connectedMachine(t, d, "fast", fast)
	t.Cleanup(func() { close(slow.stall) })
	note := func(machine, text string) {
		t.Helper()
		raw, _ := json.Marshal(devicewire.NodeEvent{MachineID: machine, Event: "terminal.input", Params: json.RawMessage(`{"text":"` + text + `"}`)})
		done := make(chan struct{})
		go func() { d.Event(devicewire.EventNotify, raw); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("the bus waited for %s", machine)
		}
	}
	for i := range 50 {
		note("slow", fmt.Sprint(i))
	}
	note("fast", "hi")
	eventually(t, 2*time.Second, "the other machine's event", func() bool { return len(fast.eventsSeen()) == 1 })
	eventually(t, 5*time.Second, "the stalled machine connected again", func() bool { return len(slow.seen("node.info")) >= 2 })
}

// What the hub opened on a node through the last bus connection ends on the node when a new one says hello (the hub
// restarted, or the bus dropped): its port streams close, its terminals are unwatched; what already ended is left.
func TestANewBusConnectionEndsWhatTheLastOneOpened(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	call := func(method, params string) {
		t.Helper()
		if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: method, Params: json.RawMessage(params)}); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	call("port.dial", `{"id":"p1","port":8080}`)
	call("port.dial", `{"id":"p2","port":8081}`)
	call("terminal.watch", `{"session_id":"s1"}`)
	call("terminal.watch", `{"session_id":"s2"}`)
	_ = n.current().Notify("stream.close", map[string]any{"id": "p2"})                          // the node closed it
	_ = n.current().Notify("terminal.end", map[string]any{"session_id": "s2"})                  // and ended this one
	_ = n.current().Notify("terminal.end", map[string]any{"session_id": "s1", "control": true}) // only its control
	eventually(t, 2*time.Second, "the node's events", func() bool { return len(d.host().eventsOf("box", "terminal.end")) == 2 })
	d.Hello(json.RawMessage(`{}`))
	eventually(t, 2*time.Second, "the stream closed and the terminal unwatched on the node", func() bool {
		return slices.Contains(n.eventsSeen(), `stream.close {"id":"p1"}`) &&
			slices.Contains(n.seen("terminal.unwatch"), `terminal.unwatch {"session_id":"s1"}`)
	})
	time.Sleep(50 * time.Millisecond)
	if got := n.seen("terminal.unwatch"); len(got) != 1 || slices.Contains(n.eventsSeen(), `stream.close {"id":"p2"}`) {
		t.Fatalf("what had ended was ended again: %v %v", got, n.eventsSeen())
	}
	d.Hello(json.RawMessage(`{}`)) // nothing left
	time.Sleep(50 * time.Millisecond)
	if len(n.seen("terminal.unwatch")) != 1 {
		t.Fatal("ended twice")
	}
}

// #6372: a watch the node refuses on its new connection is asked again on that connection while the hub still wants
// it; one the hub stopped wanting meanwhile is let go; a node that keeps refusing is given up on after rewatchTries.
func TestRefusedRewatchAskedAgain(t *testing.T) {
	wait, tries := rewatchWait, rewatchTries
	rewatchWait, rewatchTries = 300*time.Millisecond, 3
	t.Cleanup(func() { rewatchWait, rewatchTries = wait, tries })
	d := newTestDevices(t)
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	watch := func(method, id string) {
		t.Helper()
		if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: method,
			Params: json.RawMessage(`{"session_id":"` + id + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		watch("session.watch", id)
	}
	calls := func(id string) int {
		return len(slices.DeleteFunc(n.seen("session.watch"), func(c string) bool { return !strings.Contains(c, `"`+id+`"`) }))
	}
	n.mu.Lock()
	n.refuseWatch = map[string]int{"s1": 1, "s2": 1, "s3": 100}
	n.mu.Unlock()
	m.reconnect()
	eventually(t, 5*time.Second, "each refused on the new connection", func() bool { return calls("s1") == 2 && calls("s2") == 2 && calls("s3") == 2 })
	watch("session.unwatch", "s2") // the app left before the node was asked again
	eventually(t, 5*time.Second, "s1 asked again and taken", func() bool { return calls("s1") == 3 })
	eventually(t, 10*time.Second, "s3 given up on after its tries", func() bool { return calls("s3") == 2+rewatchTries })
	time.Sleep(2 * rewatchWait)
	if calls("s2") != 2 || calls("s3") != 2+rewatchTries || calls("s1") != 3 {
		t.Fatalf("asked again past what was wanted: s1 %d, s2 %d, s3 %d", calls("s1"), calls("s2"), calls("s3"))
	}
}

// The sessions the hub watches are watched again on the node's next connection, from the last item it sent.
func TestWatchedAgainOnANewConnection(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: "session.watch",
		Params: json.RawMessage(`{"session_id":"s1","since_seq":0}`)}); err != nil {
		t.Fatal(err)
	}
	_ = n.current().Notify("session.items", map[string]any{"session_id": "s1", "items": []proto.Item{{Seq: 4}}})
	eventually(t, 2*time.Second, "the item seen", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.lastSeq["s1"] == 4 })
	m.reconnect()
	eventually(t, 5*time.Second, "watched again", func() bool {
		return slices.Contains(n.seen("session.watch"), `session.watch {"session_id":"s1","since_seq":4}`)
	})
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: "session.unwatch",
		Params: json.RawMessage(`{"session_id":"s1"}`)}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watched["s1"] {
		t.Fatal("still watched")
	}
}

// What the nodes keep listing is the assistant's to say: every node is told the first time, then those whose list
// changed; a node that connects later gets it with its configuration.
func TestSetKeepTellsTheNodes(t *testing.T) {
	d := newTestDevices(t)
	a, b := &testNode{}, &testNode{}
	connectedMachine(t, d, "a", a)
	connectedMachine(t, d, "b", b)
	d.SetKeep(map[string][]string{"a": {"main-1"}})
	eventually(t, 3*time.Second, "both told", func() bool {
		return slices.Contains(a.seen("node.configure"), `node.configure {"keep_sessions":["main-1"]}`) &&
			slices.Contains(b.seen("node.configure"), `node.configure {"keep_sessions":[]}`)
	})
	d.SetKeep(map[string][]string{"a": {"main-1"}, "b": {"task-2"}})
	eventually(t, 3*time.Second, "b told", func() bool {
		return slices.Contains(b.seen("node.configure"), `node.configure {"keep_sessions":["task-2"]}`)
	})
	time.Sleep(100 * time.Millisecond)
	if n := len(a.seen(`node.configure {"keep_sessions"`)); n != 1 {
		t.Fatalf("a told %d times", n)
	}
	c := &testNode{}
	d.SetKeep(map[string][]string{"c": {"main-9"}})
	connectedMachine(t, d, "c", c)
	if got := c.seen("node.configure"); len(got) == 0 || !strings.Contains(got[0], `"keep_sessions":["main-9"]`) {
		t.Fatalf("c configured with %v", got)
	}
}

// The hub's part of a connection is in the node's configuration and the calls right after it, before its sessions
// are listed; the manager's own settings stay its own.
func TestTheHubsPartOfAConnection(t *testing.T) {
	d := newTestDevices(t)
	f := d.host()
	f.reply = devicewire.ConnectedReply{Configure: map[string]json.RawMessage{"codex": json.RawMessage(`{"discovery":true}`),
		"machine_id": json.RawMessage(`"not-this"`)},
		Calls: []devicewire.NodeCall{{Method: "launchers.managed", Params: json.RawMessage(`{"launchers":[]}`)}}}
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	var order []string
	for _, c := range n.seen("") {
		order = append(order, strings.Fields(c)[0])
	}
	i, j, k := slices.Index(order, "node.configure"), slices.Index(order, "launchers.managed"), slices.Index(order, "sessions.list")
	if i < 0 || j < i || k < j {
		t.Fatalf("calls %v", order)
	}
	cfg := n.seen("node.configure")[0]
	if !strings.Contains(cfg, `"codex":{"discovery":true}`) || !strings.Contains(cfg, `"machine_id":"box"`) {
		t.Fatalf("configured %s", cfg)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.connected) != 1 || f.connected[0].MachineID != "box" || f.connected[0].ConnectedAt.IsZero() {
		t.Fatalf("the hub heard %+v", f.connected)
	}
}

// A machine the hub says is another manager's now (devices.connected refused no_device: it moved away while this
// manager still had it) gets nothing of this manager's: not its launchers with their keys (NodeCalls), not its
// extensions' part (Own). One that is its own gets both.
func TestNothingOfTheManagersOnAnotherManagersMachine(t *testing.T) {
	d := newTestDevices(t)
	d.opts.NodeCalls = func(_ context.Context, machine string) []devicewire.NodeCall {
		return []devicewire.NodeCall{{MachineID: machine, Method: "launchers.managed", Params: json.RawMessage(`{"launchers":[{"name":"MY KEY"}]}`)}}
	}
	d.opts.Own = func(context.Context, string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"cua": json.RawMessage(`"MINE"`)}
	}
	f := d.host()
	f.theirs = map[string]bool{"moved": true}
	moved, mine := &testNode{}, &testNode{}
	connectedMachine(t, d, "moved", moved)
	connectedMachine(t, d, "mine", mine)
	if len(moved.seen("launchers.managed")) != 0 || strings.Contains(strings.Join(moved.seen("node.configure"), ""), "MINE") {
		t.Fatalf("another manager's machine got this one's: %v", moved.seen(""))
	}
	if len(moved.seen("node.configure")) == 0 {
		t.Fatal("not configured at all")
	}
	if got := mine.seen("launchers.managed"); len(got) != 1 || !strings.Contains(got[0], "MY KEY") ||
		!strings.Contains(strings.Join(mine.seen("node.configure"), ""), "MINE") {
		t.Fatalf("its own machine: %v", mine.seen(""))
	}
}

// When the connection to the hub is up again, the hub gets every machine and its sessions, and does its part of
// every live connection again (it may have restarted).
func TestBusUpTellsTheHubEverything(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{sessions: []proto.Session{{SessionID: "s1"}}}
	connectedMachine(t, d, "box", n)
	f := d.host()
	f.mu.Lock()
	f.states, f.events, f.connected = nil, nil, nil
	f.mu.Unlock()
	configured := len(n.seen("node.configure"))
	d.BusUp()
	eventually(t, 3*time.Second, "the hub's part again", func() bool { return len(n.seen("node.configure")) > configured })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) == 0 || len(f.states[0].Machines) != 1 || !f.states[0].Machines[0].Connected {
		t.Fatalf("states %+v", f.states)
	}
	if len(f.events) == 0 || f.events[0].Event != "sessions.changed" || !strings.Contains(string(f.events[0].Params), `"s1"`) {
		t.Fatalf("events %+v", f.events)
	}
}

// The hub updated itself: the manager redeploys the machines on another build, never the hub machine's own.
func TestUpdateNodesSkipsTheHubsOwn(t *testing.T) {
	d := newTestDevices(t)
	d.cfg.HubVersion = "v2"
	d.cfg.AutoUpdateNodes = true
	local := newMachine(d, MachineConfig{ID: "hub", Name: "Hub", Kind: "local"})
	local.online, local.info.Version = true, "v1"
	d.addEntry(local)
	d.updateNodes(context.Background())
	if s := local.snapshot(); s.Deploying || s.DeployError != "" {
		t.Fatalf("tried to deploy to the hub's own machine: %+v", s)
	}
}

// Right after the manager started, a call to a machine whose first try to connect has not ended waits for it (an
// answer the hub held while the manager was away, a start from the apps) instead of failing at once.
func TestACallWaitsForTheFirstConnection(t *testing.T) {
	old := firstWait
	firstWait = 5 * time.Second
	t.Cleanup(func() { firstWait = old })
	d := newTestDevices(t)
	n := &testNode{}
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	m.dialNode = func(ctx context.Context) (net.Conn, error) {
		time.Sleep(300 * time.Millisecond) // the node is a moment away
		return n.dial(ctx)
	}
	d.addEntry(m)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() { defer close(done); m.run(ctx) }()
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "box", Method: "session.send", Params: json.RawMessage(`{"session_id":"s1"}`)}); err != nil {
		t.Fatalf("the call did not wait for the node: %v", err)
	}
	if len(n.seen("session.send")) != 1 {
		t.Fatal("not sent")
	}
	// One that never comes: the call fails once the try ended.
	off := newMachine(d, MachineConfig{ID: "off", Name: "off", Kind: "ssh"})
	off.dialNode = func(context.Context) (net.Conn, error) { return nil, errors.New("no route") }
	d.addEntry(off)
	runMachine(t, off)
	var re *rpc.Error
	if _, err := d.Call(context.Background(), devicewire.NodeCall{MachineID: "off", Method: "fs.list"}); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline {
		t.Fatalf("a machine that cannot be reached: %v", err)
	}
}

// A Follow whose session.watch the node refused is not remembered: the next one asks the node again. The same for an
// unwatch.
func TestAFailedFollowIsAskedAgain(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	connectedMachine(t, d, "box", n)
	ctx := context.Background()
	refuse := func(err error) { n.mu.Lock(); n.refuse = err; n.mu.Unlock() }
	refused := errors.New("no")

	refuse(refused)
	if err := d.Follow(ctx, "box", "s1", true); err == nil {
		t.Fatal("follow: no error from a refusing node")
	}
	refuse(nil)
	if err := d.Follow(ctx, "box", "s1", true); err != nil {
		t.Fatal(err)
	}
	if got := len(n.seen("session.watch")); got != 2 {
		t.Fatalf("session.watch sent %d times, want 2", got)
	}
	if err := d.Follow(ctx, "box", "s1", true); err != nil || len(n.seen("session.watch")) != 2 {
		t.Fatalf("followed again: %v, %v", err, n.seen("session.watch"))
	}

	refuse(refused)
	if err := d.Follow(ctx, "box", "s1", false); err == nil {
		t.Fatal("unfollow: no error from a refusing node")
	}
	refuse(nil)
	if err := d.Follow(ctx, "box", "s1", false); err != nil {
		t.Fatal(err)
	}
	if got := len(n.seen("session.unwatch")); got != 2 {
		t.Fatalf("session.unwatch sent %d times, want 2", got)
	}
}

// A Follow that failed stays followed: the node's next connection watches the session again (the only repair for a
// follower that asks once), and that settles it. An unwatch that failed is not watched again.
func TestAFailedFollowIsWatchedOnTheNextConnection(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	ctx := context.Background()
	refuse := func(err error) { n.mu.Lock(); n.refuse = err; n.mu.Unlock() }
	refuse(errors.New("no"))
	if err := d.Follow(ctx, "box", "s1", true); err == nil {
		t.Fatal("follow: no error from a refusing node")
	}
	if err := d.Follow(ctx, "box", "s2", true); err == nil {
		t.Fatal("follow s2: no error from a refusing node")
	}
	refuse(nil)
	if err := d.Follow(ctx, "box", "s2", true); err != nil {
		t.Fatal(err)
	}
	refuse(errors.New("no"))
	if err := d.Follow(ctx, "box", "s2", false); err == nil {
		t.Fatal("unfollow s2: no error from a refusing node")
	}
	refuse(nil)
	before := len(n.seen(`session.watch {"session_id":"s2"`))
	m.reconnect()
	eventually(t, 5*time.Second, "s1 watched again", func() bool {
		return len(n.seen(`session.watch {"session_id":"s1"`)) == 2
	})
	if got := len(n.seen(`session.watch {"session_id":"s2"`)); got != before {
		t.Fatalf("an unfollowed session was watched again on the new connection (%d -> %d)", before, got)
	}
	eventually(t, 2*time.Second, "settled", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.unsettled) == 0 })
	if err := d.Follow(ctx, "box", "s1", true); err != nil || len(n.seen(`session.watch {"session_id":"s1"`)) != 2 {
		t.Fatalf("followed once more: %v, %v", err, n.seen("session.watch"))
	}
}
