package node

import (
	"context"
	"github.com/nwverse/persona/server/internal/host"
)

// Display Snapshot alone is not a freshness contract: some hosts reuse cached
// observations. Only sources with an audited direct read implement this.
type updateSnapshotSource interface {
	SnapshotForUpdate(context.Context) (*host.Layout, error)
}

func (h *herdrHost) SnapshotForUpdate(ctx context.Context) (*host.Layout, error) {
	return h.Snapshot(ctx)
}
func (h *herdrCoreHost) SnapshotForUpdate(ctx context.Context) (*host.Layout, error) {
	return h.snapshot(ctx, true)
}
