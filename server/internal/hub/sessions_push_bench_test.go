package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// BenchmarkBroadcastSessions is one session of a 150-session machine changing, pushed to two older apps and three
// delta apps (docs/server/server.md "What gets pushed to apps"): each change encodes the whole list again.
func BenchmarkBroadcastSessions(b *testing.B) { benchmarkBroadcastSessions(b, 2) }

// BenchmarkBroadcastSessionsDeltaOnly is the same with five delta apps and no older one: no whole-list message.
func BenchmarkBroadcastSessionsDeltaOnly(b *testing.B) { benchmarkBroadcastSessions(b, 0) }

// benchmarkBroadcastSessions: five apps, the first older of them older apps (whole lists), the rest delta apps.
func benchmarkBroadcastSessions(b *testing.B, older int) {
	h := newTestHub(&testing.T{})
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	for i := range 150 {
		m.sessions = append(m.sessions, proto.Session{SessionID: fmt.Sprintf("s%03d", i), Status: proto.StatusIdle, Title: "title",
			LastText: strings.Repeat("x", 600), Cwd: "/Users/me/projects/app", UpdatedAt: time.Unix(int64(i), 0)})
	}
	for i := range 5 {
		c := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{}, role: roleFull}
		if i >= older {
			c.delta, c.have = true, map[string]map[string][]byte{}
		}
		h.clients[c] = struct{}{}
	}
	n := 0
	for b.Loop() {
		n++
		m.sessions[n%len(m.sessions)].LastText = fmt.Sprintf("%600d", n)
		h.sessionsChanged(m) // what a new list from the manager ends in (takeFleetSessions)
		for c := range h.clients {
			for len(c.out) > 0 {
				<-c.out
			}
		}
	}
}

// BenchmarkTakeFleetSessionsOneChanged is the whole hop of a manager's fleet.sessions for a 150-session machine with
// one session changed, from the event's bytes to the messages queued for five delta apps and two older ones.
func BenchmarkTakeFleetSessionsOneChanged(b *testing.B) {
	send := oneChangedHop(b, newTestHub(&testing.T{}))
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		n++
		send(n)
	}
}

// oneChangedHop gives h a 150-session machine "box" and five delta apps and two older ones, all holding its first
// list. send(n) takes the manager's fleet.sessions n (the bus handler) and empties the apps' queues: list n has the
// sessions before n%150 in their next version, so it differs from list n-1 in one session only.
func oneChangedHop(tb testing.TB, h *Hub) (send func(n int)) {
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	for i := range 7 {
		c := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{}, role: roleFull}
		if i >= 2 {
			c.delta, c.have = true, map[string]map[string][]byte{}
		}
		h.clients[c] = struct{}{}
	}
	// Two versions of each session, differing in its text.
	rows := make([][2][]byte, 150)
	for i := range rows {
		at := time.Unix(1700000000+int64(i), 0)
		s := proto.Session{MachineID: "box", SessionID: fmt.Sprintf("s%03d", i), Title: "Run the tests", Cwd: "/Users/me/projects/app",
			Status: proto.StatusIdle, State: proto.StateIdle, Agent: "claude", Model: "claude-test", SpaceKind: "herdr", SpaceID: "w1",
			UpdatedAt: at, ActivityAt: &at, StartedByPersona: i%2 == 0}
		for v := range rows[i] {
			s.LastText = fmt.Sprintf("%600d", v)
			rows[i][v], _ = json.Marshal(s)
		}
	}
	var params []byte
	take := busEvents[assistantwire.EventFleetSessions]
	drain := func() {
		for c := range h.clients {
			for len(c.out) > 0 {
				<-c.out
			}
		}
	}
	send = func(n int) {
		params = append(params[:0], `{"machine_id":"box","sessions":[`...)
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
		take(h, nil, append(params, `]}`...))
		drain()
	}
	send(0)
	if len(m.keptSessions()) != len(rows) {
		tb.Fatalf("%d sessions kept", len(m.keptSessions()))
	}
	for c := range h.clients { // the delta apps connect as apps do: the list whole, then deltas
		if c.delta {
			h.snapshotSessions(c, m)
		}
	}
	drain()
	return send
}

// BenchmarkHubEncodeSessionRows encodes a busy machine's 150 sessions as rows, stamped with their manager, as every
// list and push the hub sends does (encodeRows).
func BenchmarkHubEncodeSessionRows(b *testing.B) {
	list := make([]proto.Session, 150)
	for i := range list {
		at := time.Unix(1700000000+int64(i), 0)
		list[i] = proto.Session{MachineID: "box", SessionID: fmt.Sprintf("s%03d", i), Title: "Run the tests", Cwd: "/Users/me/projects/app",
			Status: proto.StatusIdle, State: proto.StateIdle, Agent: "claude", Model: "claude-test", SpaceKind: "herdr", SpaceID: "w1",
			UpdatedAt: at, ActivityAt: &at, LastText: strings.Repeat("x", 600), StartedByPersona: i%2 == 0}
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(encodeStamped(list, "assistant")) != len(list) {
			b.Fatal("a session was left out")
		}
	}
}
