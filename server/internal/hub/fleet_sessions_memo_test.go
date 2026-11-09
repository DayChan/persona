package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// memoHub is a hub with one machine and apps of each kind: two older ones and three delta ones, a fourth delta app
// connecting later.
type memoHub struct {
	h    *Hub
	m    *Machine
	apps []*client
}

func newMemoHub(t *testing.T) *memoHub {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	x := &memoHub{h: h, m: m}
	for i := range 5 {
		x.connect(i >= 2)
	}
	return x
}

func (x *memoHub) connect(delta bool) *client {
	c := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull}
	if delta {
		c.delta, c.have = true, map[string]map[string][]byte{}
	}
	x.h.mu.Lock()
	x.h.clients[c] = struct{}{}
	x.h.mu.Unlock()
	x.apps = append(x.apps, c)
	return c
}

// got is what each app was sent since the last call.
func (x *memoHub) got() [][]string {
	out := make([][]string, len(x.apps))
	for i, c := range x.apps {
		for len(c.out) > 0 {
			out[i] = append(out[i], string(<-c.out))
		}
	}
	return out
}

// setOwner makes manager the one that carries the machine.
func (x *memoHub) setOwner(manager string) {
	d := &x.h.devices
	d.mu.Lock()
	if d.kept == nil {
		d.kept = map[string]string{}
	}
	d.kept["box"] = manager
	d.mu.Unlock()
}

// A random sequence of the manager's lists, revs and owner changes, taken by the bus handler (each list's changed
// sessions decoded and encoded alone) and by a reference hub that decodes every list whole and encodes every session
// again (json.Unmarshal, takeFleetSessions, encodeStamped): both keep the same sessions and every app gets the same
// bytes. Rows change, come, go, move, repeat, are null or of a wrong type; the list's key comes in another case or
// twice; some sessions are archived (not shown) or Codex history.
func TestFleetSessionsMemoMatchesWholeLists(t *testing.T) {
	fast, ref := newMemoHub(t), newMemoHub(t)
	rng := rand.New(rand.NewPCG(7, 9))
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // a reader of the kept list meanwhile: what it encodes is the list's own bytes
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			kept, owner := fast.m.keptSessions(), fast.h.machineOwner("box")
			if got, want := fast.m.encodeKept(kept, owner, fast.m.rawsFor(kept)), encodeStamped(kept, owner); !sameSessions(got, want) {
				t.Error("a concurrent encoding of the kept list differs")
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	owner := assistantwire.AssistantID
	conn := func() *busConn {
		if owner == assistantwire.AssistantID {
			return nil
		}
		return &busConn{ident: db.BusIdentity{ID: owner}}
	}
	rows := []string{}
	for i := range 10 {
		rows = append(rows, memoRow(rng, i))
	}
	for step := range 300 {
		switch op := rng.IntN(16); {
		case op < 5 && len(rows) > 0:
			rows[rng.IntN(len(rows))] = memoRow(rng, rng.IntN(14))
		case op == 5:
			rows = append(rows, memoRow(rng, rng.IntN(14)))
		case op == 6 && len(rows) > 0:
			i := rng.IntN(len(rows))
			rows = append(rows[:i], rows[i+1:]...)
		case op == 7:
			rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		case op == 8 && len(rows) > 0:
			rows = append(rows, rows[rng.IntN(len(rows))]) // listed twice
		case op == 9: // revs: the kept sessions' rev changed in place, then a push without a new list
			revs, _ := json.Marshal(map[string]any{"revs": map[string]int64{fmt.Sprintf("s%02d", rng.IntN(14)): int64(rng.IntN(4))}})
			fast.m.onEvent("sessions.revs", revs)
			ref.m.onEvent("sessions.revs", revs)
			fast.h.sessionsChanged(fast.m)
			ref.h.sessionsChanged(ref.m)
			memoCompare(t, step, fast, ref)
			continue
		case op == 10: // another manager carries the machine now: its rows are stamped with it
			owner = []string{assistantwire.AssistantID, "m-other"}[rng.IntN(2)]
			fast.setOwner(owner)
			ref.setOwner(owner)
			fast.h.sessionsChanged(fast.m)
			ref.h.sessionsChanged(ref.m)
			memoCompare(t, step, fast, ref)
			continue
		case op == 11 && len(fast.apps) < 8: // an app connects: the whole list, then deltas
			fast.h.snapshotSessions(fast.connect(true), fast.m)
			ref.h.snapshotSessions(ref.connect(true), ref.m)
			memoCompare(t, step, fast, ref)
			continue
		}
		list := append([]string(nil), rows...)
		switch rng.IntN(14) {
		case 0:
			list = append(list, "null")
		case 1:
			list = append(list, `{"session_id":"bad","rev":"x"}`)
		case 2:
			list = append(list, `7`)
		}
		key := []string{`"sessions"`, `"sessions"`, `"sessions"`, `"Sessions"`}[rng.IntN(4)]
		data := `{"machine_id":"box",` + key + `:[` + strings.Join(list, ",") + `]`
		if rng.IntN(12) == 0 {
			data += `,"sessions":[` + memoRow(rng, 1) + `]`
		}
		if rng.IntN(4) == 0 {
			data += `,"muted":["s01"]`
		}
		data += `}`
		busEvents[assistantwire.EventFleetSessions](fast.h, conn(), json.RawMessage(data))
		var p assistantwire.FleetSessions
		if json.Unmarshal([]byte(data), &p) == nil {
			ref.h.takeFleetSessions(p) // the whole list, decoded and encoded as before
		}
		memoCompare(t, step, fast, ref)
	}
}

// A manager's list with one session of 150 changed costs the hub that one session: the list is decoded by changed
// rows (fleet_link.go) and the other sessions' rows are pushed again as the same slices (encodeKept). The oracle test
// above sees only the bytes, which are the same either way. 43 allocations measured for the hop with five delta apps
// and two older ones, 571 with the list decoded whole; encoding every row again is 44, told apart by the rows' identity.
func TestFleetSessionsOneChangedReusesRows(t *testing.T) {
	if !isolatedSessionAllocation(t) {
		return
	}
	h := newTestHub(t)
	send := oneChangedHop(t, h)
	before := h.sessionsSent["box"]
	send(1) // s000 changed
	after := h.sessionsSent["box"]
	if len(after) != len(before) || len(after) != 150 {
		t.Fatalf("pushed %d rows, then %d", len(before), len(after))
	}
	for i := range after {
		if kept := &after[i].raw[0] == &before[i].raw[0]; kept != (i > 0) {
			t.Fatalf("row %d (%s) pushed as the same slice: %v, want %v", i, after[i].id, kept, i > 0)
		}
	}
	n := 1
	if allocs := testing.AllocsPerRun(20, func() { n++; send(n) }); allocs > 50 && !raceEnabled {
		t.Fatalf("a list with one session changed allocates %v times", allocs)
	}
}

// raceEnabled: built with -race, whose instrumentation allocates on its own (race_test.go).
var raceEnabled bool

// memoCompare fails unless both hubs keep the same sessions and sent each app the same bytes.
func memoCompare(t *testing.T, step int, fast, ref *memoHub) {
	t.Helper()
	if got, want := fast.m.keptSessions(), ref.m.keptSessions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("step %d: kept sessions differ:\n got %+v\nwant %+v", step, got, want)
	}
	got, want := fast.got(), ref.got()
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("step %d, app %d:\n got %q\nwant %q", step, i, got[i], want[i])
		}
	}
}

// memoRow is a session's bytes as the manager sends them: one of a few ids, in one of a few versions.
func memoRow(rng *rand.Rand, id int) string {
	at := time.Unix(1700000000+int64(rng.IntN(3)), 0)
	s := proto.Session{MachineID: "box", SessionID: fmt.Sprintf("s%02d", id), Title: []string{"one", "<b>two</b>", "\u2028 three"}[rng.IntN(3)],
		Status: proto.StatusIdle, State: proto.StateIdle, Rev: int64(rng.IntN(4)), UpdatedAt: at, Archived: rng.IntN(8) == 0}
	if id%5 == 4 {
		s.SourceKind = "codex_history"
	}
	if rng.IntN(6) == 0 {
		s.Manager = "m-other"
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// The whole list is put together and projected once per push for each kind of older app: one that takes the
// canonical contracts gets it as it is, the others as compat.ProjectJSON projects it for them, as queue did per app.
func TestWholeListProjectedOncePerKind(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = append(h.machines, m), m
	m.sessions = []proto.Session{{MachineID: "box", SessionID: "s1", Work: &proto.WorkState{Link: &proto.WorkLink{URL: "persona://pair"}}}}
	var apps []*client
	for _, canonical := range []bool{false, true, false, true} {
		c := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{}, role: roleFull}
		c.canonicalContracts.Store(canonical)
		h.clients[c] = struct{}{}
		apps = append(apps, c)
	}
	h.sessionsChanged(m)
	full := sessionsMessage("box", encodeStamped(m.sessions, h.machineOwner("box")))
	if string(compat.ProjectJSON(full, false)) == string(full) {
		t.Fatal("the list is not one an older app gets projected")
	}
	for i, c := range apps {
		want := compat.ProjectJSON(full, c.canonical())
		if got := <-c.out; string(got) != string(want) {
			t.Fatalf("app %d:\n got %s\nwant %s", i, got, want)
		}
	}
}

// Delta apps that have the same list share one map and one sessionsDelta per push, and each still gets what
// sessionsDelta gives for what it has itself: apps connect at different times, Codex history comes and goes (a live
// session turned history stays with the apps that had it, so their maps differ), sessions change and go.
func TestDeltaAppsShareOneComputation(t *testing.T) {
	x := newMemoHub(t)
	x.apps = nil
	x.h.clients = map[*client]struct{}{}
	rng := rand.New(rand.NewPCG(3, 5))
	shadow := map[*client]map[string][]byte{} // what each app has, by sessionsDelta alone
	list := func() []proto.Session {
		var out []proto.Session
		for i := range 8 {
			if rng.IntN(6) == 0 {
				continue
			}
			s := proto.Session{MachineID: "box", SessionID: fmt.Sprintf("s%d", i), Title: fmt.Sprint(rng.IntN(2))}
			if rng.IntN(3) == 0 {
				s.SourceKind = "codex_history"
			}
			out = append(out, s)
		}
		return out
	}
	shared := false
	for step := range 200 {
		if rng.IntN(5) == 0 && len(x.apps) < 6 {
			c := x.connect(true)
			x.h.snapshotSessions(c, x.m)
			shadow[c] = maps.Clone(c.have["box"])
			<-c.out
			continue
		}
		x.h.takeFleetSessions(assistantwire.FleetSessions{MachineID: "box", Sessions: list()})
		enc := x.h.sessionsSent["box"]
		for i, c := range x.apps {
			want, next := sessionsDelta("box", enc, shadow[c])
			shadow[c] = next
			var got []byte
			if len(c.out) > 0 {
				got = <-c.out
			}
			if string(got) != string(want) || !maps.EqualFunc(c.have["box"], next, bytes.Equal) {
				t.Fatalf("step %d, app %d:\n got %s\nwant %s", step, i, got, want)
			}
			for _, o := range x.apps[:i] {
				shared = shared || mapIdentity(o.have["box"]) == mapIdentity(c.have["box"]) && len(c.have["box"]) > 0
			}
		}
	}
	if !shared {
		t.Fatal("no two apps ever shared a map")
	}
}
