package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fullSendBuffer is a manager whose connection takes no more writes (a sleeping laptop with its TCP send buffer full):
// a write returns only once hangUp is closed.
type fullSendBuffer struct{ hangUp chan struct{} }

func (w fullSendBuffer) Write([]byte) (int, error) {
	<-w.hangUp
	return 0, errors.New("connection closed")
}

// rosterLog is a manager that keeps the managers events it got. With a gate, a write first says it started (started)
// and then returns only once the gate is closed: a slow connection the test lets through when it chooses.
type rosterLog struct {
	gate    chan struct{}
	started chan struct{}
	mu      sync.Mutex
	lists   [][]busManagerBrief
}

func (l *rosterLog) Write(b []byte) (int, error) {
	if l.gate != nil {
		select {
		case l.started <- struct{}{}:
		default:
		}
		<-l.gate
	}
	var m struct {
		Event  string
		Params struct{ Managers []busManagerBrief }
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	if m.Event == "managers" {
		l.mu.Lock()
		l.lists = append(l.lists, m.Params.Managers)
		l.mu.Unlock()
	}
	return len(b), nil
}

func (l *rosterLog) got() [][]busManagerBrief {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]busManagerBrief(nil), l.lists...)
}

// rosterConn makes w the current connection of manager id.
func rosterConn(h *Hub, id string, w interface{ Write([]byte) (int, error) }) *busConn {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	if h.bus.current == nil {
		h.bus.current = map[string]*busConn{}
	}
	c := &busConn{ident: db.BusIdentity{ID: id, Role: bus.RoleManager}, peer: rpc.New("h", w)}
	h.bus.current[id] = c
	return c
}

// rosterIdle: c's roster sender has written the newest list and ended, so nothing more is on its way to c.
func rosterIdle(c *busConn) bool {
	c.roster.mu.Lock()
	defer c.roster.mu.Unlock()
	return !c.roster.sending
}

// A manager whose connection stopped taking writes holds up neither the roster for the others nor the caller: the
// handshake of a manager that just connected (busAccept) and the teardown of one that left (busEnded) run it.
func TestRosterReachesTheOthersPastAStalledManager(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	stalled := fullSendBuffer{hangUp: make(chan struct{})}
	t.Cleanup(func() { close(stalled.hangUp) }) // the stalled connections hang up: their writes return
	for i := range 8 {
		rosterConn(h, fmt.Sprintf("m-stalled%d", i), stalled)
	}
	healthy := &rosterLog{}
	hc := rosterConn(h, "m-healthy", healthy)

	done := make(chan struct{})
	go func() {
		h.busRosterChanged(context.Background(), nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("busRosterChanged waited on a manager whose connection takes no writes")
	}
	eventually(t, 2*time.Second, "the healthy manager's roster sender ended", func() bool { return rosterIdle(hc) })
	if n := len(healthy.got()); n != 1 {
		t.Fatalf("the healthy manager got %d managers events, want 1", n)
	}
}

// Roster changes made while a slow manager's first list is still being written do not wait for it. Once that write
// returns, the manager gets the newest list only, never an older one after a newer one. Each change here adds an
// identity, so a newer list is a longer one.
func TestRosterChangesReachASlowManagerInOrder(t *testing.T) {
	h := newTestHub(t)
	idents := &memIdents{}
	h.bus.idents = idents
	slow := &rosterLog{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	var open sync.Once
	letThrough := func() { open.Do(func() { close(slow.gate) }) }
	t.Cleanup(letThrough)
	sc := rosterConn(h, "m-slow", slow)
	ctx := context.Background()
	const changes = 6
	change := func(i int) {
		if err := idents.SaveBusIdentity(ctx, db.BusIdentity{ID: fmt.Sprintf("m-%d", i), Role: bus.RoleManager}, fmt.Sprint(i)); err != nil {
			t.Error(err)
		}
		h.busRosterChanged(ctx, nil)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		change(0)
		<-slow.started // the first list is being written, and that write does not return yet
		for i := 1; i < changes; i++ {
			change(i)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%d roster changes waited on the slow manager", changes)
	}

	letThrough()
	eventually(t, 5*time.Second, "the slow manager's roster sender ended", func() bool { return rosterIdle(sc) })
	var sizes []string
	for _, list := range slow.got() {
		sizes = append(sizes, fmt.Sprint(len(list)))
	}
	// The first list, then the newest: the ones in between were replaced while the first was being written.
	if got, want := strings.Join(sizes, ","), fmt.Sprintf("1,%d", changes); got != want {
		t.Fatalf("the slow manager got lists of sizes %s, want %s", got, want)
	}
}
