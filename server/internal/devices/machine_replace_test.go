package devices

import (
	"context"
	"encoding/json"
	"net"
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
