package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

func updateInventoryFixture(t *testing.T) (*Node, nodeclient.UpdateInventoryTarget) {
	t.Helper()
	n := remoteTestNode(t)
	n.machineID, n.uid = "fixture-machine", "fixture-uid"
	n.stateReady = make(chan struct{})
	close(n.stateReady)
	n.runtimes = newRuntimeRegistry()
	n.configured()
	off := false
	n.eng.botmux = &off
	n.hosts = host.NewRegistry(&inventoryHost{bareHost: bareHost{kind: host.Herdr}})
	return n, nodeclient.UpdateInventoryTarget{Manager: "m-fixture", Main: remoteRaw, Machine: n.machineID, MachineUID: n.uid, AuthorityRevision: strings.Repeat("a", 64)}
}
func emptyUpdateCensus(context.Context) ([]updateProcess, error) { return []updateProcess{}, nil }

func TestUpdateInventoryProducerCoversUnloadedMainWithoutDisplayHistory(t *testing.T) {
	n, target := updateInventoryFixture(t)
	before := n.state.Sessions
	out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus)
	if err != nil || !out.Complete || len(out.Revision) != 64 || len(out.Rows) != 1 || out.Rows[0].Session != target.Main || out.Rows[0].Live || out.Rows[0].Source != "not-loaded" {
		t.Fatalf("unloaded main coverage: %+v %v", out, err)
	}
	if len(before) != len(n.state.Sessions) {
		t.Fatal("read created persisted state")
	}
	again, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus)
	if err != nil || again.Revision != out.Revision {
		t.Fatalf("observation clock changed activity revision: %v", err)
	}
}
func TestUpdateInventoryProducerRejectsIdentityUnknownAndPartialSources(t *testing.T) {
	for _, name := range []string{"machine", "uid", "authority", "loading", "scope", "runtime", "host", "process", "cancel"} {
		t.Run(name, func(t *testing.T) {
			n, target := updateInventoryFixture(t)
			census := emptyUpdateCensus
			ctx := context.Background()
			switch name {
			case "machine":
				target.Machine = "foreign"
			case "uid":
				target.MachineUID = "foreign"
			case "authority":
				target.AuthorityRevision = ""
			case "loading":
				n.state.beginLoad()
			case "scope":
				n.sessionScope = &nodeSessionScope{}
			case "runtime":
				n.runtimes.conns = map[int]*runtimeConn{1: {}}
			case "host":
				n.hosts = host.NewRegistry(&inventoryHost{bareHost: bareHost{kind: host.Herdr}, snapshotErr: errors.New("unavailable")})
			case "process":
				census = func(context.Context) ([]updateProcess, error) {
					return []updateProcess{{PID: 123, Signature: "foreign"}}, nil
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			out, err := n.collectUpdateInventory(ctx, target, census)
			if err == nil || out.Complete {
				t.Fatalf("unknown/partial source became complete: %+v %v", out, err)
			}
		})
	}
}
func TestUpdateInventoryProducerRechecksSourceIdentity(t *testing.T) {
	n, target := updateInventoryFixture(t)
	calls := 0
	census := func(context.Context) ([]updateProcess, error) {
		calls++
		if calls == 2 {
			n.mu.Lock()
			n.machineID = "changed"
			n.mu.Unlock()
		}
		return []updateProcess{}, nil
	}
	out, err := n.collectUpdateInventory(context.Background(), target, census)
	if err == nil || out.Complete {
		t.Fatal("machine identity change was accepted")
	}
}
func TestUpdateInventoryProducerIncludesStartingAssistantKeysAndSpare(t *testing.T) {
	for _, name := range []string{"key", "spare", "resident"} {
		t.Run(name, func(t *testing.T) {
			n, target := updateInventoryFixture(t)
			p := &assistantProc{cmd: &exec.Cmd{Process: &os.Process{Pid: 123}}, exited: make(chan struct{})}
			switch name {
			case "key":
				n.akeys = map[string]*assistantProc{"key": p}
			case "spare":
				n.spare = p
			case "resident":
				liveAssistants.Store(p, struct{}{})
				defer liveAssistants.Delete(p)
			}
			out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus)
			if err == nil || out.Complete {
				t.Fatal("starting/unobservable process omitted from complete inventory")
			}
		})
	}
}
