package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// When a node's sessions.changed reaches Options.Event, Sessions already returns that list: the assistant takes it
// from there instead of decoding the event again (assistant NodeEvent). The same holds for BusUp's replay.
func TestSessionsEventAfterTheList(t *testing.T) {
	d := newTestDevices(t)
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.ctx = ctx
	d.mu.Unlock()
	t.Cleanup(func() { cancel(); d.workers.Wait() })
	b, _ := json.Marshal(MachineConfig{ID: "mac", Name: "mac", Kind: kindLink})
	d.Configure(devicewire.Config{Machines: []json.RawMessage{b}})
	m := d.cfgMachine("mac")
	if m == nil {
		t.Fatal("machine not started")
	}
	var seen, sent [][]proto.Session
	d.opts.Event = func(machine, event string, params json.RawMessage) bool {
		if event != "sessions.changed" {
			return true
		}
		list, _ := d.Sessions(machine)
		seen = append(seen, list)
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			t.Fatal(err)
		}
		for i := range p.Sessions {
			p.Sessions[i].MachineID = machine
		}
		sent = append(sent, p.Sessions)
		return false
	}
	for _, list := range [][]proto.Session{
		{{SessionID: "s1", Title: "one", Queued: []string{"q"}}, {SessionID: "s2", SpaceName: "app"}},
		{},
		{{SessionID: "s2", SpaceName: "app"}},
	} {
		params, _ := json.Marshal(map[string]any{"sessions": list})
		m.onEvent("sessions.changed", params)
	}
	d.BusUp()
	if len(seen) != 4 {
		t.Fatalf("%d events", len(seen))
	}
	for i := range seen {
		if !reflect.DeepEqual(seen[i], sent[i]) {
			t.Fatalf("event %d: devices have %+v, the event says %+v", i, seen[i], sent[i])
		}
	}
}

// The list a machine keeps from a node's sessions.changed is the one json.Unmarshal decodes, whatever came before:
// sessions changed, added, removed, moved or listed twice, elements that do not decode, sessions.revs between lists
// (written over the kept list, never over the sessions the next list takes again).
func TestSessionsChangedMatchesUnmarshal(t *testing.T) {
	d := New(Options{Hub: &fakeHost{}, Store: newMemStore(), Logf: func(string, ...any) {}})
	d.opts.Event = func(string, string, json.RawMessage) bool { return false }
	m := newMachine(d, MachineConfig{ID: "mac", Name: "mac"})
	rnd := rand.New(rand.NewPCG(7, 11))
	row := func() []byte {
		s := proto.Session{SessionID: fmt.Sprintf("s%d", rnd.IntN(12)), Title: fmt.Sprint("title ", rnd.IntN(3)),
			MachineID: fmt.Sprint("node ", rnd.IntN(2)), Rev: int64(rnd.IntN(3)), Queued: []string{"q"}}
		b, _ := json.Marshal(s)
		return b
	}
	var rows [][]byte
	var want []proto.Session
	for step := range 300 {
		switch op := rnd.IntN(10); {
		case op < 4 && len(rows) > 0:
			rows[rnd.IntN(len(rows))] = row()
		case op < 6:
			rows = append(rows, row())
		case op < 7 && len(rows) > 0:
			i := rnd.IntN(len(rows))
			rows = append(rows[:i], rows[i+1:]...)
		case op < 8 && len(rows) > 0:
			rows = append(rows, rows[rnd.IntN(len(rows))])
		case op < 9 && len(rows) > 0:
			rnd.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		default:
			revs := map[string]int64{fmt.Sprintf("s%d", rnd.IntN(12)): int64(rnd.IntN(9))}
			params, _ := json.Marshal(map[string]any{"revs": revs})
			m.onEvent("sessions.revs", params)
			for i := range want {
				if r, ok := revs[want[i].SessionID]; ok {
					want[i].Rev = r
				}
			}
		}
		params := []byte(`{"sessions":[`)
		for i, r := range rows {
			if i > 0 {
				params = append(params, ',')
			}
			params = append(params, r...)
		}
		if rnd.IntN(20) == 0 {
			params = append(params, `,{"title":7}`...)
		}
		params = append(params, `]}`...)
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if json.Unmarshal(params, &p) == nil {
			for i := range p.Sessions {
				p.Sessions[i].MachineID = "mac"
			}
			want = p.Sessions
		}
		m.onEvent("sessions.changed", params)
		if got := m.sessionList(); !reflect.DeepEqual(got, want) {
			t.Fatalf("step %d: kept %+v, json.Unmarshal %+v", step, got, want)
		}
	}
}

// BenchmarkDevicesSessionsChanged is a node's sessions.changed for a 150-session machine with one session changed,
// from the event's bytes to the list kept (Options.Event takes it from there).
func BenchmarkDevicesSessionsChanged(b *testing.B) {
	m, event := oneChangedNode(b)
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		n++
		m.onEvent("sessions.changed", event(n))
	}
}

// A node's list with one session of 150 changed decodes that session alone (decodeSessions); the oracle test above
// sees only the list kept, which is the same either way. 15 allocations measured from the event to the list kept,
// 514 with the list decoded whole.
func TestSessionsChangedOneChangedAllocations(t *testing.T) {
	m, event := oneChangedNode(t)
	n := 0
	if allocs := testing.AllocsPerRun(20, func() { n++; m.onEvent("sessions.changed", event(n)) }); allocs > 20 && !raceEnabled {
		t.Fatalf("a list with one session changed allocates %v times", allocs)
	}
}

// raceEnabled: built with -race, whose instrumentation allocates on its own (race_test.go).
var raceEnabled bool

// oneChangedNode is a machine that kept a node's first list of 150 sessions. event(n) is the node's sessions.changed n:
// list n has the sessions before n%150 in their next version, so it differs from list n-1 in one session only.
func oneChangedNode(tb testing.TB) (*Machine, func(n int) json.RawMessage) {
	d := New(Options{Hub: &fakeHost{}, Store: newMemStore(), Logf: func(string, ...any) {}})
	d.opts.Event = func(string, string, json.RawMessage) bool { return false }
	m := newMachine(d, MachineConfig{ID: "mac", Name: "mac"})
	// Two versions of each session, differing in its text.
	rows := make([][2][]byte, 150)
	for i := range rows {
		at := time.Unix(1700000000+int64(i), 0)
		s := proto.Session{SessionID: fmt.Sprintf("s%03d", i), Title: "Run the tests", Cwd: "/Users/me/projects/app",
			Status: proto.StatusIdle, Agent: "claude", Model: "claude-test", SpaceKind: "herdr", SpaceID: "w1",
			UpdatedAt: at, ActivityAt: &at, StartedByPersona: i%2 == 0}
		for v := range rows[i] {
			s.LastText = fmt.Sprintf("%600d", v)
			rows[i][v], _ = json.Marshal(s)
		}
	}
	var params []byte
	event := func(n int) json.RawMessage {
		params = append(params[:0], `{"sessions":[`...)
		for i := range rows {
			if i > 0 {
				params = append(params, ',')
			}
			v := n / len(rows)
			if i < n%len(rows) {
				v++
			}
			params = append(params, rows[i][v%2]...)
		}
		return append(params, `]}`...)
	}
	m.onEvent("sessions.changed", event(0))
	if len(m.sessionList()) != len(rows) {
		tb.Fatalf("%d sessions kept", len(m.sessionList()))
	}
	return m, event
}
