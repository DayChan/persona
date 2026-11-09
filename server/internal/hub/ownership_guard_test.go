package hub

// What one manager may not do to another's (#5556): claim its machine or its sessions, lose its moves, keep moving
// while being removed. Each test has two managers on a Postgres bus hub (newBusHub).

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// guardManager is a manager on the bus that answers the hub's calls with answer and keeps a log of them; onEvent
// (f.mu), when set, is told the hub's events (heard).
type guardManager struct {
	id      string
	token   string // its bus token
	peer    *rpc.Peer
	mu      sync.Mutex
	got     []string
	onEvent func(method string, params json.RawMessage)
}

func connectGuardManager(t *testing.T, h *Hub, url, id string, answer func(method string, params json.RawMessage) (any, error)) *guardManager {
	t.Helper()
	return connectGuardManagerWith(t, url, id, busToken(t, h, id, id), answer)
}

// connectGuardManagerWith is connectGuardManager with a bus token the manager has (connecting again with it).
func connectGuardManagerWith(t *testing.T, url, id, token string, answer func(method string, params json.RawMessage) (any, error)) *guardManager {
	t.Helper()
	f := &guardManager{id: id, token: token}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, err := bus.Dial(ctx, url, f.token, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	conn := bus.NewConn(ctx, ws)
	f.peer = rpc.New("g", conn)
	f.peer.OnEvent = func(method string, params json.RawMessage) { // set before Serve; the hook is f.onEvent
		f.mu.Lock()
		fn := f.onEvent
		f.mu.Unlock()
		if fn != nil {
			fn(method, params)
		}
	}
	f.peer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		f.mu.Lock()
		f.got = append(f.got, method+" "+string(params))
		f.mu.Unlock()
		return answer(method, params)
	}
	go func() { _ = f.peer.Serve(ctx, conn) }()
	cctx, cc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cc()
	if err := f.peer.Call(cctx, busHello, map[string]any{"id": id}, nil); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *guardManager) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.got, "\n")
}

func (f *guardManager) call(t *testing.T, method string, params, out any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.peer.Call(ctx, method, params, out)
}

func (f *guardManager) report(t *testing.T, states ...devicewire.State) {
	t.Helper()
	for i := range states {
		states[i].Machine.Online, states[i].Connected, states[i].ConnectedAt = true, true, time.Now().UTC()
	}
	if err := f.peer.Notify(devicewire.EventState, devicewire.States{Machines: states}); err != nil {
		t.Fatal(err)
	}
}

func answerWho(id string) func(string, json.RawMessage) (any, error) {
	return func(string, json.RawMessage) (any, error) { return map[string]any{"who": id}, nil }
}

func guardPost(t *testing.T, h *Hub, srv *httptest.Server, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// A manager's snapshot naming another manager's session (its main conversation, or a conversation on some other
// machine) takes nothing: the user's message to that session goes to the machine's manager only, its words
// untouched.
func TestASnapshotCannotClaimAnotherManagersSession(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(string, json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", func(method string, _ json.RawMessage) (any, error) {
		if method == assistantwire.MethodSendParams {
			return map[string]any{"params": map[string]any{"session_id": "s3", "text": "INJECTED BY B"}}, nil
		}
		return map[string]any{}, nil
	})
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	eventually(t, 5*time.Second, "ma", func() bool { m := h.localMachine("ma"); return m != nil && m.isOnline() })
	var res assistantwire.SnapshotResult
	if err := b.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "b", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "elsewhere", SessionID: "s3"}, {MachineID: "ma", SessionID: "s4"}},
		Profile:  &assistantwire.Profile{MachineID: "ma", Main: "s3"}}, &res); err != nil {
		t.Fatal(err)
	}
	if h.sessionOwner("ma", "s3") != assistantwire.AssistantID || h.sessionOwner("ma", "s4") != assistantwire.AssistantID ||
		h.isMainOn("ma", "s3") || h.conversationOwner("s4") != "" {
		t.Fatalf("claims taken: s3 %s, s4 %s, main %v", h.sessionOwner("ma", "s3"), h.sessionOwner("ma", "s4"), h.isMainOn("ma", "s3"))
	}
	if h.asstOf(h.sessionOwner("ma", "s3")).id != assistantwire.AssistantID {
		t.Fatal("the session's requests would go to the second manager")
	}
	if code, _ := guardPost(t, h, srv, "/api/v1/sessions/ma/s3/messages", `{"text":"user secret text"}`); code == 0 {
		t.Fatal("no answer")
	}
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(b.log(), "user secret text") || strings.Contains(a.log(), "INJECTED BY B") {
		t.Fatalf("the second manager saw the user's message or rewrote it\nB:\n%s\nA:\n%s", b.log(), a.log())
	}
}

// A session is its machine's manager's: a second manager starting, resuming or naming a session on the assistant's
// machine (session.start with resume_session_id or an async session_id) is refused, so it never comes to own one there,
// and the user's messages to that session stay with the machine's manager.
func TestASessionOnAnotherManagersMachineCannotBecomeItsOwn(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, nodeAnswers)
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", func(method string, _ json.RawMessage) (any, error) {
		if method == assistantwire.MethodSendParams {
			return map[string]any{"params": map[string]any{"session_id": "s3", "text": "INJECTED BY B"}}, nil
		}
		return map[string]any{}, nil
	})
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	eventually(t, 5*time.Second, "ma", func() bool { m := h.localMachine("ma"); return m != nil && m.isOnline() })
	for _, params := range []string{`{"resume_session_id":"s3","cwd":"/"}`, `{"async":true,"session_id":"s3","cwd":"/"}`, `{"cwd":"/","prompt":"hi"}`} {
		if err := b.call(t, assistantwire.MethodDevice, assistantwire.DeviceCall{Device: "ma", Method: assistantwire.DeviceSessionStart,
			Params: json.RawMessage(params)}, nil); rpcCode(err) != devicewire.CodeNoDevice {
			t.Fatalf("session.start %s: %v", params, err)
		}
	}
	if strings.Contains(a.log(), "session.start") {
		t.Fatal("a refused start reached the node")
	}
	var res assistantwire.SnapshotResult
	if err := b.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "b", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "ma", SessionID: "s3"}},
		Profile:  &assistantwire.Profile{MachineID: "ma", Main: "s3"}}, &res); err != nil {
		t.Fatal(err)
	}
	if h.sessionOwner("ma", "s3") != assistantwire.AssistantID || h.conversationOwner("s3") != "" || h.isMainOn("ma", "s3") {
		t.Fatalf("s3 is %s's (conversation %q, main %v)", h.sessionOwner("ma", "s3"), h.conversationOwner("s3"), h.isMainOn("ma", "s3"))
	}
	guardPost(t, h, srv, "/api/v1/sessions/ma/s3/messages", `{"text":"user secret text"}`)
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(b.log(), "user secret text") || strings.Contains(a.log(), "INJECTED BY B") {
		t.Fatalf("the second manager saw the user's message or rewrote it\nB:\n%s\nA:\n%s", b.log(), a.log())
	}
}

// Moving a machine moves its sessions with it: the old manager's conversations there, its main one too, stop being
// its own; the user's messages to them and device.call there are the new manager's only.
func TestAMovedMachinesSessionsGoWithIt(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}}
	echo := func(method string, params json.RawMessage) (any, error) {
		if method == assistantwire.MethodSendParams { // the message as it came
			var p assistantwire.SendParams
			_ = json.Unmarshal(params, &p)
			return map[string]any{"params": p.Params}, nil
		}
		return nodeAnswers(method, params)
	}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, echo)
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", echo)
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	eventually(t, 5*time.Second, "ma", func() bool { m := h.localMachine("ma"); return m != nil && m.isOnline() })
	var res assistantwire.SnapshotResult
	if err := a.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "a", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "ma", SessionID: "conv-a", Title: "A's"}},
		Profile:  &assistantwire.Profile{MachineID: "ma", Main: "conv-a"}}, &res); err != nil {
		t.Fatal(err)
	}
	sessions := []proto.Session{{MachineID: "ma", SessionID: "conv-a", Assistant: true}, {MachineID: "ma", SessionID: "agent-1"}}
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: sessions})
	if h.sessionOwner("ma", "conv-a") != assistantwire.AssistantID || h.conversationOwner("conv-a") != assistantwire.AssistantID || !h.isMainOn("ma", "conv-a") {
		t.Fatal("before the move: the assistant's")
	}
	if code, out := guardPost(t, h, srv, "/api/v1/sessions/ma/conv-a/messages", `{"text":"said before"}`); code != http.StatusOK || !strings.Contains(a.log(), "said before") {
		t.Fatalf("before the move: %d %v\nA:\n%s", code, out, a.log())
	}

	if _, err := h.moveMachine(context.Background(), "ma", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	eventually(t, 5*time.Second, "ma reported by b", func() bool { m := h.localMachine("ma"); return m != nil && m.isOnline() && m.ownerID() == "m-0b0b0b" })
	if err := b.peer.Notify(assistantwire.EventFleetSessions, assistantwire.FleetSessions{MachineID: "ma", Sessions: sessions}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "b's list of ma", func() bool { _, ok := h.localMachine("ma").session("agent-1"); return ok })
	for _, sid := range []string{"conv-a", "agent-1"} {
		if o := h.sessionOwner("ma", sid); o != "m-0b0b0b" {
			t.Fatalf("after the move %s is %s's", sid, o)
		}
	}
	if h.conversationOwner("conv-a") != "" || h.isMainOn("ma", "conv-a") {
		t.Fatalf("after the move the assistant's snapshot still counts: %q, main %v", h.conversationOwner("conv-a"), h.isMainOn("ma", "conv-a"))
	}
	for _, s := range h.decorated(h.localMachine("ma")) {
		if s.Manager != "m-0b0b0b" {
			t.Fatalf("listed: %+v", s)
		}
	}
	for _, sid := range []string{"conv-a", "agent-1"} {
		if code, out := guardPost(t, h, srv, "/api/v1/sessions/ma/"+sid+"/messages", `{"text":"said after to `+sid+`"}`); code != http.StatusOK {
			t.Fatalf("after the move, to %s: %d %v", sid, code, out)
		}
		if strings.Contains(a.log(), "said after") || !strings.Contains(b.log(), "said after to "+sid) {
			t.Fatalf("after the move the message to %s went to the old manager\nA:\n%s\nB:\n%s", sid, a.log(), b.log())
		}
	}
	if err := a.call(t, assistantwire.MethodDevice, assistantwire.DeviceCall{Device: "ma", Method: "session.send",
		Params: json.RawMessage(`{"session_id":"conv-a","text":"x"}`)}, nil); rpcCode(err) != devicewire.CodeNoDevice {
		t.Fatalf("the old manager on the moved machine: %v", err)
	}
}

// nodeAnswers answers devices.call as a node would for session.start: a resume of a running session answers that
// session, an async start the id it was given.
func nodeAnswers(method string, params json.RawMessage) (any, error) {
	if method != devicewire.MethodCall {
		return map[string]any{}, nil
	}
	var c devicewire.NodeCall
	_ = json.Unmarshal(params, &c)
	if c.Method == assistantwire.DeviceSessionStart {
		var p struct {
			Resume string `json:"resume_session_id"`
			ID     string `json:"session_id"`
		}
		_ = json.Unmarshal(c.Params, &p)
		return map[string]any{"session": map[string]any{"session_id": cmp.Or(p.Resume, p.ID, "fresh-1")}}, nil
	}
	return map[string]any{"who": "a", "node": c.Method}, nil
}

// With one manager nothing changes: a snapshot view naming no machine (an old row) counts for no manager, so a request
// about it goes where it always did (the default manager, the only one), and the session lists are stamped with it.
func TestAViewWithoutAMachineWithOneManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, nodeAnswers)
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	eventually(t, 5*time.Second, "ma", func() bool { return h.localMachine("ma") != nil })
	var res assistantwire.SnapshotResult
	if err := a.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "a", Seq: 1,
		Sessions: []assistantwire.SessionView{{SessionID: "old", Title: "An old one"}}}, &res); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/assistant/conversations/old", nil)
	r.SetPathValue("id", "old")
	if id, err := h.byConversation(r); err != nil || id != assistantwire.AssistantID || h.conversationOwner("old") != "" {
		t.Fatalf("old row: routed to %q (%v), counted for %q", id, err, h.conversationOwner("old"))
	}
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: []proto.Session{{MachineID: "ma", SessionID: "old", Title: "An old one"}}})
	if l := h.decorated(h.localMachine("ma")); len(l) != 1 || l[0].Manager != assistantwire.AssistantID || l[0].Title != "An old one" {
		t.Fatalf("listed: %+v", l)
	}
}

// A report of a hub.json machine from another manager than its entry's, derived or not, is not taken: the node
// calls, the link relay and the machine's credential stay with its manager.
func TestAReportCannotTakeAnotherManagersMachine(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answerWho("a"))
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answerWho("b"))
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	eventually(t, 5*time.Second, "ma", func() bool { return h.machineOwner("ma") == assistantwire.AssistantID && h.localMachine("ma") != nil })
	for _, derived := range []bool{true, false} {
		b.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}, Derived: derived})
		time.Sleep(200 * time.Millisecond)
		var who map[string]any
		err := h.localMachine("ma").call(context.Background(), "panes.list", map[string]any{}, &who)
		cerr := b.call(t, devicewire.MethodConnectorCred, devicewire.ConnectorCred{MachineID: "ma"}, nil)
		relay := h.relayOwner(url.Values{"hub": {"h2"}, "machine": {"ma"}})
		if h.machineOwner("ma") != assistantwire.AssistantID || who["who"] != "a" || rpcCode(cerr) != devicewire.CodeNoDevice ||
			relay != assistantwire.AssistantID {
			t.Fatalf("derived %v: owner %s, node call by %v (%v), credential %v, relay %s", derived, h.machineOwner("ma"), who["who"], err, cerr, relay)
		}
	}
	// What only a machine's manager may do for it is refused to another.
	if err := b.call(t, devicewire.MethodRequest, devicewire.NodeCall{MachineID: "ma", Method: "x"}, nil); rpcCode(err) != devicewire.CodeNoDevice {
		t.Fatalf("devices.request: %v", err)
	}
	if err := b.call(t, devicewire.MethodConnected, devicewire.Connected{MachineID: "ma"}, nil); rpcCode(err) != devicewire.CodeNoDevice {
		t.Fatalf("devices.connected: %v", err)
	}
	if err := b.peer.Notify(assistantwire.EventFleetSessions, assistantwire.FleetSessions{MachineID: "ma",
		Sessions: []proto.Session{{MachineID: "ma", SessionID: "fake"}}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, ok := h.localMachine("ma").session("fake"); ok {
		t.Fatal("another manager listed the machine's sessions")
	}

	// A mesh member's machine (not hub.json's) is the carrier's only: another manager reporting it first, or after,
	// takes nothing.
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "member", Name: "member"}, Derived: true})
	time.Sleep(200 * time.Millisecond)
	if h.localMachine("member") != nil {
		t.Fatal("another manager's report of a mesh member's machine was taken")
	}
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}}, devicewire.State{Machine: proto.Machine{ID: "member", Name: "member"}, Derived: true})
	eventually(t, 5*time.Second, "member", func() bool { return h.localMachine("member") != nil })
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "member", Name: "member"}, Derived: true})
	time.Sleep(200 * time.Millisecond)
	var who map[string]any
	err := h.localMachine("member").call(context.Background(), "panes.list", map[string]any{}, &who)
	cerr := b.call(t, devicewire.MethodConnectorCred, devicewire.ConnectorCred{MachineID: "member"}, nil)
	if h.machineOwner("member") != assistantwire.AssistantID || who["who"] != "a" || rpcCode(cerr) != devicewire.CodeNoDevice ||
		h.relayOwner(url.Values{"hub": {"h2"}, "machine": {"member"}}) != assistantwire.AssistantID {
		t.Fatalf("member is %s's; node call by %v (%v); credential %v", h.machineOwner("member"), who["who"], err, cerr)
	}
}

// Moves of different machines at the same time all stay: in memory and in the database.
func TestConcurrentMovesAllStay(t *testing.T) {
	h, srv, store := newBusHub(t)
	var ms []MachineConfig
	for _, id := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"} {
		ms = append(ms, MachineConfig{ID: id, Name: id, Kind: "local"})
	}
	h.cfg.Machines = ms
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answerWho("a"))
	connectGuardManager(t, h, srv.URL, "m-0b0b0b", answerWho("b"))
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		for _, m := range ms {
			wg.Add(1)
			go func(mid string) {
				defer wg.Done()
				if _, err := h.moveMachine(context.Background(), mid, "m-0b0b0b"); err != nil {
					t.Error(err)
				}
			}(m.ID)
		}
		wg.Wait()
		var kept map[string]string
		_, _ = store.Setting(context.Background(), machineOwnersKey, &kept)
		for _, m := range ms {
			if h.entryOwner(m.ID) != "m-0b0b0b" || kept[m.ID] != "m-0b0b0b" {
				t.Fatalf("round %d: %s in memory %s, kept %q", round, m.ID, h.entryOwner(m.ID), kept[m.ID])
			}
		}
		for _, m := range ms {
			if _, err := h.moveMachine(context.Background(), m.ID, devicesOwner); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// failingOwners is the database, but reading who carries which machine fails while fail is set.
type failingOwners struct {
	*db.DB
	fail bool
}

func (f *failingOwners) Setting(ctx context.Context, key string, out any) (bool, error) {
	if f.fail && key == machineOwnersKey {
		return false, errors.New("database away")
	}
	return f.DB.Setting(ctx, key, out)
}

// While the hub could not read who carries which machine, no move: it would overwrite what is kept. Once it can,
// the move adds to it; an owner the roster no longer has, and a machine hub.json no longer has, are dropped.
func TestNoMoveUntilTheOwnersAreRead(t *testing.T) {
	h, srv, store := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "m1", Name: "m1", Kind: "local"}, {ID: "m2", Name: "m2", Kind: "local"}}
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answerWho("a"))
	connectGuardManager(t, h, srv.URL, "m-0b0b0b", answerWho("b"))
	if err := store.PutSetting(context.Background(), machineOwnersKey, map[string]string{"m2": "m-0b0b0b", "gone": "m-0b0b0b", "m1": "m-ffffff"}); err != nil {
		t.Fatal(err)
	}
	f := &failingOwners{DB: store, fail: true}
	h.bus.idents = f
	if _, err := h.moveMachine(context.Background(), "m1", "m-0b0b0b"); !errors.Is(err, errOwnersUnknown) {
		t.Fatalf("move while unread: %v", err)
	}
	if code, out := guardPost(t, h, srv, "/api/v1/managers/m-0b0b0b/machines", `{"machine_id":"m1"}`); code != http.StatusServiceUnavailable || out["code"] != "owners_unknown" {
		t.Fatalf("route: %d %v", code, out)
	}
	f.fail = false
	if _, err := h.moveMachine(context.Background(), "m1", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	var kept map[string]string
	_, _ = store.Setting(context.Background(), machineOwnersKey, &kept)
	if len(kept) != 2 || kept["m1"] != "m-0b0b0b" || kept["m2"] != "m-0b0b0b" {
		t.Fatalf("kept %v", kept)
	}
}

// Removing a manager waits for a move to it, and a move after finds it gone; its machines go back to the carrier, its
// cards go.
func TestRemovingAManagerAgainstItsMoves(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "m1", Name: "m1", Kind: "local"}}
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answerWho("a"))
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answerWho("b"))
	if _, err := h.moveMachine(context.Background(), "m1", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: proto.Interaction{
		ID: "ix-b", MachineID: "m1", SessionID: "s", Kind: "question", CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "its card", func() bool { return h.interactionOwner("ix-b") == "m-0b0b0b" })
	// A removal holds the moves back; the move waiting finds it gone.
	h.moving.managers.Lock()
	moved := make(chan error, 1)
	go func() { _, err := h.moveMachine(context.Background(), "m1", "m-0b0b0b"); moved <- err }()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-moved:
		t.Fatalf("a move went on during a removal: %v", err)
	default:
	}
	h.moving.managers.Unlock()
	if err := <-moved; err != nil {
		t.Fatal(err) // still there: a no-op
	}
	if code, _ := ownershipGet(t, h, srv, http.MethodDelete, "/api/v1/managers/m-0b0b0b", nil); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if h.entryOwner("m1") != assistantwire.AssistantID || h.interactionOwner("ix-b") != "" {
		t.Fatalf("after: m1 %s, card %q", h.entryOwner("m1"), h.interactionOwner("ix-b"))
	}
	if _, err := h.moveMachine(context.Background(), "m1", "m-0b0b0b"); !errors.Is(err, errUnknownManager) {
		t.Fatalf("move to a removed manager: %v", err)
	}
}

// A card of one manager's is not replaced by another reporting the same id.
func TestACardIsNotReplacedByAnotherManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answerWho("a"))
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answerWho("b"))
	ix := proto.Interaction{ID: "ix", MachineID: "ma", SessionID: "s", Kind: "question", CreatedAt: time.Now(), Cwd: "/a"}
	if err := a.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "a's card", func() bool { return h.interactionOwner("ix") == assistantwire.AssistantID })
	ix.Cwd = "/b"
	_ = b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix})
	_ = b.peer.Notify(assistantwire.EventFleetInteractions, assistantwire.FleetInteractions{Interactions: []proto.Interaction{ix}})
	time.Sleep(200 * time.Millisecond)
	if got, _ := h.pendingInteraction("ix"); got.Manager != assistantwire.AssistantID || got.Cwd != "/a" {
		t.Fatalf("replaced: %+v", got)
	}
	guardPost(t, h, srv, "/api/v1/interactions/ix/resolve", `{"allow":true}`)
	if !strings.Contains(a.log(), "/api/v1/interactions/ix/resolve") || strings.Contains(b.log(), "/api/v1/interactions/ix/resolve") {
		t.Fatalf("resolve went to the wrong manager\nA:\n%s\nB:\n%s", a.log(), b.log())
	}
}

// A pending card is taken only on a machine of its manager's own: one naming another manager's machine, or a session
// there (by machine, or a session id alone), is dropped, so it never shows as that session's nor gets the user's
// answer; one naming no machine is its manager's own. Moving a machine withdraws the old manager's cards on it, and
// those naming one of its sessions without the machine; a card naming none of its sessions stays.
func TestACardOnlyOnItsManagersMachine(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, answerWho("a"))
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answerWho("b"))
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "ma, mb", func() bool { return h.localMachine("ma") != nil && h.localMachine("mb") != nil })
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "ma", Sessions: []proto.Session{{MachineID: "ma", SessionID: "s3"}}})
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "mb", Sessions: []proto.Session{{MachineID: "mb", SessionID: "b-agent"}}})
	var res assistantwire.SnapshotResult
	if err := a.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "a", Seq: 1,
		Sessions: []assistantwire.SessionView{{MachineID: "ma", SessionID: "conv-a"}}}, &res); err != nil {
		t.Fatal(err)
	}
	card := func(id, machine, session string) proto.Interaction {
		return proto.Interaction{ID: id, MachineID: machine, SessionID: session, SessionTitle: "Persona", Kind: "question", CreatedAt: time.Now()}
	}
	// One at a time (fleet.interaction).
	for _, ix := range []proto.Interaction{card("b-on-ma", "ma", "s3"), card("b-on-s3", "", "s3"), card("b-on-conv-a", "", "conv-a"),
		card("b-own", "mb", "b-agent"), card("b-none", "", "")} {
		if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: card("a-on-mb", "mb", "b-agent")}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "b's own cards", func() bool {
		return h.interactionOwner("b-own") == "m-0b0b0b" && h.interactionOwner("b-none") == "m-0b0b0b"
	})
	time.Sleep(200 * time.Millisecond)
	for _, id := range []string{"b-on-ma", "b-on-s3", "b-on-conv-a", "a-on-mb"} {
		if ix, ok := h.pendingInteraction(id); ok {
			t.Fatalf("a card on another manager's machine was taken: %+v", ix)
		}
	}
	// The whole list (fleet.interactions) the same.
	if err := b.peer.Notify(assistantwire.EventFleetInteractions, assistantwire.FleetInteractions{Interactions: []proto.Interaction{
		card("b-on-ma", "ma", "s3"), card("b-on-s3", "", "s3"), card("b-own", "mb", "b-agent"), card("b-none", "", ""),
		card("b-own-session", "", "b-agent")}}); err != nil {
		t.Fatal(err)
	}
	h.takeFleetInteractions([]proto.Interaction{card("a-own", "ma", "s3"), card("a-on-mb", "mb", "b-agent")})
	time.Sleep(200 * time.Millisecond)
	got := map[string]string{}
	for _, ix := range h.pendingInteractions() {
		got[ix.ID] = ix.Manager
	}
	if len(got) != 4 || got["b-own"] != "m-0b0b0b" || got["b-none"] != "m-0b0b0b" || got["b-own-session"] != "m-0b0b0b" ||
		got["a-own"] != assistantwire.AssistantID {
		t.Fatalf("pending: %v", got)
	}
	// Answering its own card goes to it; nothing reached a for b's.
	guardPost(t, h, srv, "/api/v1/interactions/b-none/resolve", `{"allow":true}`)
	if !strings.Contains(b.log(), "/api/v1/interactions/b-none/resolve") || strings.Contains(a.log(), "b-none") {
		t.Fatalf("the answer to b's card\nA:\n%s\nB:\n%s", a.log(), b.log())
	}
	// mb moves back to the carrier: b's cards on it go with it (by machine, or by one of its sessions), the one
	// naming nothing of it stays b's.
	if _, err := h.moveMachine(context.Background(), "mb", devicesOwner); err != nil {
		t.Fatal(err)
	}
	_, own := h.pendingInteraction("b-own")
	_, ownSession := h.pendingInteraction("b-own-session")
	if own || ownSession || h.interactionOwner("b-none") != "m-0b0b0b" {
		t.Fatalf("after the move: b-own kept %v, b-own-session kept %v, b-none %q", own, ownSession, h.interactionOwner("b-none"))
	}
}

// A source move whose release failed, done again, releases it at the old manager.
func TestASourceMoveDoneAgainReleasesIt(t *testing.T) {
	h, srv, _ := newBusHub(t)
	var mu sync.Mutex
	releases, bHas := 0, false
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case extwire.MethodSourceExport:
			return map[string]any{"source": "mail:x", "kind": "mail", "data": map[string]any{"password": "pw"}}, nil
		case extwire.MethodSourceRelease:
			mu.Lock()
			defer mu.Unlock()
			releases++
			if releases == 1 {
				return nil, &rpc.Error{Code: "boom", Message: "disk full"}
			}
		}
		return map[string]any{}, nil
	})
	connectGuardManager(t, h, srv.URL, "m-0b0b0b", func(method string, _ json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case extwire.MethodSourceImport:
			if bHas {
				return nil, &rpc.Error{Code: extwire.CodeSourceExists, Message: "has it"}
			}
			bHas = true
			return map[string]any{"source": "mail:x"}, nil
		case extwire.MethodSourceExport: // the hub asking whether it has it
			if !bHas {
				return nil, &rpc.Error{Code: extwire.CodeNoSource, Message: "no"}
			}
			return map[string]any{"source": "mail:x", "kind": "mail", "data": map[string]any{}}, nil
		}
		return map[string]any{}, nil
	})
	c1, o1 := guardPost(t, h, srv, "/api/v1/managers/m-0b0b0b/sources", `{"source":"mail:x"}`)
	c2, o2 := guardPost(t, h, srv, "/api/v1/managers/m-0b0b0b/sources", `{"source":"mail:x"}`)
	mu.Lock()
	defer mu.Unlock()
	if c1 != http.StatusBadGateway || o1["code"] != "source_not_released" || c2 != http.StatusOK || releases != 2 {
		t.Fatalf("first %d %v, again %d %v, releases %d", c1, o1, c2, o2, releases)
	}
}

// An import that does not answer: the hub asks the new manager whether it took the source, and goes on to the
// release when it did, keeps it at the old one when it did not.
func TestASourceImportWithoutAnAnswer(t *testing.T) {
	for _, took := range []bool{true, false} {
		h, srv, _ := newBusHub(t)
		var mu sync.Mutex
		released := false
		connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, func(method string, _ json.RawMessage) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			switch method {
			case extwire.MethodSourceExport:
				return map[string]any{"source": "mail:y", "kind": "mail", "data": map[string]any{}}, nil
			case extwire.MethodSourceRelease:
				released = true
			}
			return map[string]any{}, nil
		})
		connectGuardManager(t, h, srv.URL, "m-0b0b0b", func(method string, _ json.RawMessage) (any, error) {
			switch method {
			case extwire.MethodSourceImport:
				time.Sleep(3 * time.Second) // past the move's deadline below
				return map[string]any{"source": "mail:y"}, nil
			case extwire.MethodSourceExport:
				if !took {
					return nil, &rpc.Error{Code: extwire.CodeNoSource, Message: "no"}
				}
				return map[string]any{"source": "mail:y", "kind": "mail", "data": map[string]any{}}, nil
			}
			return map[string]any{}, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		src, err := h.moveSource(ctx, "mail:y", assistantwire.AssistantID, "m-0b0b0b")
		cancel()
		mu.Lock()
		rel := released
		mu.Unlock()
		if took && (err != nil || src != "mail:y" || !rel) || !took && (err == nil || rel) {
			t.Fatalf("took %v: %q %v, released %v", took, src, err, rel)
		}
	}
}

// Two managers serving the same API prefix: the request's named manager's, else the default's.
func TestAPrefixServedByTwoManagers(t *testing.T) {
	h := newTestHub(t)
	h.keepExtReport(assistantwire.AssistantID, &extwire.Report{APIs: []extwire.APIPrefix{{Prefix: "lark"}}})
	h.keepExtReport("m-0b0b0b", &extwire.Report{APIs: []extwire.APIPrefix{{Prefix: "lark"}, {Prefix: "only-b"}}})
	for _, tc := range []struct{ named, def, want string }{
		{"", assistantwire.AssistantID, assistantwire.AssistantID},
		{"m-0b0b0b", assistantwire.AssistantID, "m-0b0b0b"},
		{"", "m-0b0b0b", "m-0b0b0b"},
	} {
		if got := h.extPrefixOwner("lark", tc.named, tc.def); got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
	if got := h.extPrefixOwner("only-b", "", assistantwire.AssistantID); got != "m-0b0b0b" {
		t.Fatalf("only-b: %s", got)
	}
}

// device.call goes only to a machine of the caller's own: on another manager's, nothing (another manager's session,
// files, panes, memory, a session of its own, the session list) reaches the node; the calls about every machine list
// only the caller's machines. The machine's own manager, the assistant here, may do anything there, and nothing else:
// it is a manager like the others.
func TestDeviceCallOnlyOnOwnMachines(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	a := connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, nodeAnswers)
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", nodeAnswers)
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	a.report(t, devicewire.State{Machine: proto.Machine{ID: "ma", Name: "ma"}})
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "ma, mb", func() bool { return h.localMachine("ma") != nil && h.localMachine("mb") != nil })
	nodeSaw := func(f *guardManager, method string) bool { return strings.Contains(f.log(), `"method":"`+method+`"`) }
	for _, m := range []string{"session.send", "session.interrupt", "session.close", assistantwire.DeviceSessionItems, assistantwire.DeviceSessionStart,
		assistantwire.DeviceAssistantSend, "fs.read", "panes.list", assistantwire.DeviceAssistantMemory, assistantwire.DeviceSessionsList} {
		err := b.call(t, assistantwire.MethodDevice, assistantwire.DeviceCall{Device: "ma", Method: m,
			Params: json.RawMessage(`{"session_id":"a-agent","text":"x","path":"/x"}`)}, nil)
		if rpcCode(err) != devicewire.CodeNoDevice || nodeSaw(a, m) {
			t.Fatalf("%s on a's machine: %v (node saw it: %v)", m, err, nodeSaw(a, m))
		}
		// And the other way round: the assistant has no say on b's machine either.
		if err := a.call(t, assistantwire.MethodDevice, assistantwire.DeviceCall{Device: "mb", Method: m,
			Params: json.RawMessage(`{"session_id":"b-agent"}`)}, nil); rpcCode(err) != devicewire.CodeNoDevice || nodeSaw(b, m) {
			t.Fatalf("%s on b's machine by the assistant: %v", m, err)
		}
	}
	// On its own machine: anything, a resume and the machine's files included.
	for _, c := range []struct {
		f                   *guardManager
		dev, method, params string
	}{
		{b, "mb", "session.send", `{"session_id":"b-agent","text":"go on"}`},
		{b, "mb", assistantwire.DeviceSessionStart, `{"resume_session_id":"b-agent","cwd":"/"}`},
		{a, "ma", assistantwire.DeviceSessionStart, `{"resume_session_id":"a-agent","cwd":"/"}`},
		{a, "ma", "fs.read", `{"path":"/x"}`},
	} {
		if err := c.f.call(t, assistantwire.MethodDevice, assistantwire.DeviceCall{Device: c.dev, Method: c.method,
			Params: json.RawMessage(c.params)}, nil); err != nil || !nodeSaw(c.f, c.method) {
			t.Fatalf("%s on %s, its own machine: %v", c.method, c.dev, err)
		}
	}
	// The list of every machine is its own.
	var list struct {
		Devices []struct{ ID string } `json:"devices"`
		Others  []string              `json:"others"`
	}
	if err := b.call(t, assistantwire.MethodDevice, assistantwire.DeviceCall{Device: assistantwire.DeviceAll, Method: assistantwire.DeviceDevicesList}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Devices) != 1 || list.Devices[0].ID != "mb" || !slices.Contains(list.Others, "ma") {
		t.Fatalf("its devices: %+v", list)
	}
}

// rosterUnsure is the database, but looking up manager unsure fails.
type rosterUnsure struct {
	*db.DB
	unsure string
}

func (r *rosterUnsure) BusIdentityByID(ctx context.Context, id string) (*db.BusIdentity, error) {
	if id == r.unsure {
		return nil, errors.New("database away")
	}
	return r.DB.BusIdentityByID(ctx, id)
}

// Reading who carries which machine drops an owner only when the roster says it is gone, not when it cannot tell.
func TestOwnersKeptWhenTheRosterCannotTell(t *testing.T) {
	h, _, store := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "m1", Name: "m1", Kind: "local"}, {ID: "m2", Name: "m2", Kind: "local"}}
	if err := store.PutSetting(context.Background(), machineOwnersKey, map[string]string{"m1": "m-0e0e0e", "m2": "m-0f0f0f"}); err != nil {
		t.Fatal(err)
	}
	h.bus.idents = &rosterUnsure{DB: store, unsure: "m-0e0e0e"}
	if err := h.loadMachineOwners(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.entryOwner("m1") != "m-0e0e0e" || h.entryOwner("m2") != devicesOwner {
		t.Fatalf("m1 %s (lookup failed: kept), m2 %s (not in the roster: dropped)", h.entryOwner("m1"), h.entryOwner("m2"))
	}
}

// Asking a manager whether it has a source: an answer that names no source is no answer.
func TestASourceAnswerNamingNoSourceIsNoAnswer(t *testing.T) {
	h, srv, _ := newBusHub(t)
	connectGuardManager(t, h, srv.URL, "m-0b0b0b", func(method string, _ json.RawMessage) (any, error) {
		return map[string]any{"kind": "mail", "data": map[string]any{}}, nil
	})
	has, known := h.sourceKnownAt(context.Background(), "m-0b0b0b", "mail:z")
	if has || known {
		t.Fatalf("has %v, known %v", has, known)
	}
}

// A source's move lock is forgotten once no move of it runs or waits.
func TestASourceLockIsForgotten(t *testing.T) {
	var mv moves
	unlock := mv.lockSource("mail:x")
	waited := make(chan struct{})
	go func() {
		u := mv.lockSource("mail:x")
		close(waited)
		u()
	}()
	time.Sleep(50 * time.Millisecond)
	unlock()
	<-waited
	time.Sleep(20 * time.Millisecond)
	mv.mu.Lock()
	defer mv.mu.Unlock()
	if len(mv.sources) != 0 {
		t.Fatalf("kept: %v", mv.sources)
	}
}

func TestByTodoRoutesToItsManager(t *testing.T) {
	h := &Hub{}
	h.noteTodoOwner("t1", "m-other")
	h.noteTodoOwner("t2", "assistant")

	// When unnamed, routes to the manager that owns it
	req1 := httptest.NewRequest(http.MethodPatch, "/api/v1/todos/t1", nil)
	req1.SetPathValue("id", "t1")
	id, err := h.byTodo(req1)
	if err != nil || id != "m-other" {
		t.Fatalf("unnamed manager: got %s, %v, want m-other", id, err)
	}

	// Unnamed todo owned by assistant
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/todos/t2", nil)
	req2.SetPathValue("id", "t2")
	id, err = h.byTodo(req2)
	if err != nil || id != "assistant" {
		t.Fatalf("unnamed manager: got %s, %v, want assistant", id, err)
	}

	// Sub-paths like /retry or /decisions also use {id}
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/todos/t1/retry", nil)
	req3.SetPathValue("id", "t1")
	id, err = h.byTodo(req3)
	if err != nil || id != "m-other" {
		t.Fatalf("retry sub-path: got %s, %v, want m-other", id, err)
	}

	// Forget todo owner clears the cached routing
	h.forgetTodoOwner("t1")
	if h.todoOwner("t1") != "" {
		t.Fatal("todo t1 owner should be forgotten")
	}
}
