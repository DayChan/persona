package node

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
)

type inventoryHost struct {
	bareHost
	snapshotErr error
	listErr     error
}

func (h *inventoryHost) Snapshot(context.Context) (*host.Layout, error) {
	return &host.Layout{}, h.snapshotErr
}

func (h *inventoryHost) Agents(context.Context) ([]host.Agent, error) {
	return nil, h.listErr
}

func TestUpdateInventoryRefusesPartialHostSuccess(t *testing.T) {
	n, _, _ := herdrNode(t)
	failure := errors.New("source unavailable")
	good := &inventoryHost{bareHost: bareHost{kind: host.Herdr}}
	bad := &inventoryHost{bareHost: bareHost{kind: host.Orca}, snapshotErr: failure, listErr: failure}
	n.hosts = host.NewRegistry(good, bad)
	n.hostAgents = map[string][]host.Agent{host.Orca: {{Agent: "claude", AgentStatus: "idle"}}}
	if err := n.refreshHosts(context.Background()); err != nil {
		t.Fatalf("display refresh lost its tolerant behavior: %v", err)
	}
	if err := n.refreshHostInventory(context.Background(), true); !errors.Is(err, failure) {
		t.Fatalf("cached idle must not prove source completeness: %v", err)
	}
}

func TestUpdateInventoryRefusesListFallbackAndScopedNodes(t *testing.T) {
	n, _, _ := herdrNode(t)
	h := &inventoryHost{bareHost: bareHost{kind: host.Herdr}, snapshotErr: host.ErrUnsupported}
	n.hosts = host.NewRegistry(h)
	if err := n.refreshHostInventory(context.Background(), true); err == nil {
		t.Fatal("agent list without a full snapshot was certified complete")
	}
	n.sessionScope = &nodeSessionScope{}
	if err := n.refreshHostInventory(context.Background(), true); err == nil {
		t.Fatal("scoped node was certified as a complete machine")
	}
}

func TestUpdateInventoryRefusesCancellationAndPartialTargetSet(t *testing.T) {
	n, _, _ := herdrNode(t)
	a := &inventoryHost{bareHost: bareHost{kind: host.Herdr}}
	b := &inventoryHost{bareHost: bareHost{kind: host.Orca}}
	n.hosts = host.NewRegistry(a, b)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.refreshHostInventory(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled collection: %v", err)
	}
	if err := n.refreshHostInventory(context.Background(), true, a); err == nil {
		t.Fatal("partial host target set was certified complete")
	}
}

func TestUpdateInventoryDistinguishesNotRunningFromUnreachable(t *testing.T) {
	n, _, _ := herdrNode(t)
	h := &inventoryHost{bareHost: bareHost{kind: host.Herdr}, snapshotErr: host.ErrNotRunning, listErr: host.ErrNotRunning}
	n.hosts = host.NewRegistry(h)
	if err := n.refreshHostInventory(context.Background(), true); err != nil {
		t.Fatalf("explicit not-running host is an authoritative empty source: %v", err)
	}
}

func (h *inventoryHost) SnapshotForUpdate(ctx context.Context) (*host.Layout, error) {
	return h.Snapshot(ctx)
}
