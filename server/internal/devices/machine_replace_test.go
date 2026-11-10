package devices

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

type blockingCloseConn struct {
	net.Conn
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingCloseConn) Close() error {
	c.once.Do(func() {
		close(c.started)
		<-c.release
	})
	return c.Conn.Close()
}

// A changed entry must not let the old machine's shutdown report a disconnect after its replacement is connected: the
// report carries only the machine id, so the assistant would end the questions waiting on the new connection (#1949).
func TestReplacedMachineDoesNotDetachItsReplacement(t *testing.T) {
	d := newTestDevices(t)
	oldNode, newNode := &testNode{}, &testNode{}
	useOld := atomic.Bool{}
	useOld.Store(true)
	started, release := make(chan struct{}), make(chan struct{})
	d.opts.DialNode = func(string) func(context.Context) (net.Conn, error) {
		return func(ctx context.Context) (net.Conn, error) {
			if useOld.Load() {
				conn, err := oldNode.dial(ctx)
				if err != nil {
					return nil, err
				}
				return &blockingCloseConn{Conn: conn, started: started, release: release}, nil
			}
			return newNode.dial(ctx)
		}
	}
	type detachObservation struct {
		id             string
		currentOnline  bool
		currentMachine *Machine
	}
	detached := make(chan detachObservation, 4)
	d.opts.Detached = func(id string) {
		current := d.cfgMachine(id)
		online := current != nil && current.reachable()
		detached <- detachObservation{id: id, currentOnline: online, currentMachine: current}
	}

	entry := func(trust string) json.RawMessage {
		b, _ := json.Marshal(MachineConfig{ID: "box", Name: "box", Kind: "ssh", Trust: trust})
		return b
	}
	d.Configure(devicewire.Config{HubID: "hub-test", HubVersion: "test", Machines: []json.RawMessage{entry("")}})
	old := d.cfgMachine("box")
	eventually(t, 5*time.Second, "old connection", func() bool { return old.reachable() && len(oldNode.seen("sessions.list")) > 0 })

	useOld.Store(false)
	d.Configure(devicewire.Config{HubID: "hub-test", HubVersion: "test", Machines: []json.RawMessage{entry("work")}})
	replacement := d.cfgMachine("box")
	if replacement == old {
		t.Fatal("entry change did not replace the machine")
	}
	eventually(t, 5*time.Second, "replacement connection", func() bool {
		return replacement.reachable() && len(newNode.seen("sessions.list")) > 0
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old pipe did not begin closing")
	}
	close(release)
	eventually(t, 5*time.Second, "old run loop ends", func() bool {
		old.mu.Lock()
		defer old.mu.Unlock()
		return old.peer == nil
	})
	select {
	case got := <-detached:
		if got.currentMachine == replacement && got.currentOnline {
			t.Fatalf("old machine detached after its replacement was connected: machine %q", got.id)
		}
	default:
	}
	if !replacement.reachable() {
		t.Fatal("the replacement went offline when the old connection closed")
	}

	replacement.stop()
	done := make(chan struct{})
	go func() { d.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("replacement did not stop")
	}
}

// A changed entry must carry the manager's own follows (Follow: a channel's turn) to its replacement, not only what the
// hub watches: the assistant keeps its mark and never follows again, so a dropped follow left the channel without the
// turn's items. A follow the old node did not take is asked again too, from where the old machine had read.
func TestReplacedMachineKeepsTheManagersFollows(t *testing.T) {
	d := newTestDevices(t)
	oldNode := &testNode{refuseWatch: map[string]int{"s2": 1}}
	newNode := &testNode{}
	useOld := atomic.Bool{}
	useOld.Store(true)
	d.opts.DialNode = func(string) func(context.Context) (net.Conn, error) {
		return func(ctx context.Context) (net.Conn, error) {
			if useOld.Load() {
				return oldNode.dial(ctx)
			}
			return newNode.dial(ctx)
		}
	}
	entry := func(trust string) json.RawMessage {
		b, _ := json.Marshal(MachineConfig{ID: "box", Name: "box", Kind: "ssh", Trust: trust})
		return b
	}
	d.Configure(devicewire.Config{HubID: "hub-test", HubVersion: "test", Machines: []json.RawMessage{entry("")}})
	old := d.cfgMachine("box")
	eventually(t, 5*time.Second, "old connection", func() bool { return old.reachable() && len(oldNode.seen("sessions.list")) > 0 })

	ctx := context.Background()
	if err := d.Follow(ctx, "box", "s1", true); err != nil {
		t.Fatalf("follow s1: %v", err)
	}
	if err := d.Follow(ctx, "box", "s2", true); err == nil {
		t.Fatal("the old node took the watch of s2 it should refuse")
	}
	old.mu.Lock()
	old.lastSeq["s1"] = 7 // read up to here on the old connection
	unsettled := old.unsettled["s2"]
	old.mu.Unlock()
	if !unsettled {
		t.Fatal("the refused follow of s2 is not unsettled")
	}

	useOld.Store(false)
	d.Configure(devicewire.Config{HubID: "hub-test", HubVersion: "test", Machines: []json.RawMessage{entry("work")}})
	replacement := d.cfgMachine("box")
	if replacement == old {
		t.Fatal("entry change did not replace the machine")
	}
	watched := func(id string) []string { return newNode.seen(`session.watch {"session_id":"` + id + `"`) }
	eventually(t, 5*time.Second, "the follows watched on the replacement", func() bool {
		return len(watched("s1")) > 0 && len(watched("s2")) > 0
	})
	if got := watched("s1")[0]; !strings.Contains(got, `"since_seq":7`) {
		t.Fatalf("s1 watched again from the start, not where the old machine had read: %s", got)
	}
	eventually(t, 5*time.Second, "the refused follow settled on the replacement", func() bool {
		replacement.mu.Lock()
		defer replacement.mu.Unlock()
		return replacement.followed["s1"] && replacement.followed["s2"] && !replacement.unsettled["s2"]
	})

	// The manager's unfollow reaches the replacement's node.
	if err := d.Follow(ctx, "box", "s1", false); err != nil {
		t.Fatalf("unfollow s1: %v", err)
	}
	if len(newNode.seen(`session.unwatch {"session_id":"s1"`)) == 0 {
		t.Fatal("unfollowing s1 did not unwatch it on the replacement's node")
	}

	replacement.stop()
	done := make(chan struct{})
	go func() { d.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("replacement did not stop")
	}
}
