package hub

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

const (
	pushDeviceMaxIdle     = 90 * 24 * time.Hour
	pushDeviceMaintenance = 5 * time.Minute
)

// maintenanceLoop is tied to hub shutdown. Acceptance timestamps are coalesced;
// registration ownership and inactivity remain in Postgres across hub versions.
func (p *Pusher) maintenanceLoop(ctx context.Context) {
	if p.store == nil {
		return
	}
	tick := time.NewTicker(pushDeviceMaintenance)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := p.reconcileDevices(ctx); err != nil {
				p.logf("push registration maintenance: %v", err)
			}
		}
	}
}

func (p *Pusher) reconcileDevices(parent context.Context) error {
	if p.store == nil {
		return nil
	}
	// Skip a busy pass rather than waiting for network retries or an enrollment.
	// No transport can start between flushing acceptance and retiring inactivity.
	if !p.saveMu.TryLock() {
		return nil
	}
	defer p.saveMu.Unlock()
	if !p.sendMu.TryLock() {
		return nil
	}
	defer p.sendMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, p.storeTimeout)
	defer cancel()
	p.mu.Lock()
	dirty := map[string]Device{}
	for id := range p.acceptedDirty {
		d, ok := p.devices[id]
		if !ok {
			// Forgotten after the push was accepted: no row to refresh, and a zero Device would upsert an empty id.
			delete(p.acceptedDirty, id)
			continue
		}
		dirty[id] = d
	}
	p.mu.Unlock()
	for id, device := range dirty {
		if err := p.writeDeviceIn(ctx, device); err != nil {
			if !errors.Is(err, db.ErrPushRegistrationConflict) {
				// Recent acceptance not persisted: do not expire it from old data.
				return err
			}
			p.mu.Lock()
			delete(p.devices, id)
			delete(p.acceptedDirty, id)
			p.mu.Unlock()
			continue
		}
		p.mu.Lock()
		if p.devices[id].LastPushAt.Equal(device.LastPushAt) {
			delete(p.acceptedDirty, id)
		}
		p.mu.Unlock()
	}
	retired, err := p.store.ReconcilePushDevices(ctx, p.now().UTC(), pushDeviceMaxIdle)
	if err != nil {
		return err
	}
	p.mu.Lock()
	for _, r := range retired {
		delete(p.devices, r.DeviceID)
		delete(p.acceptedDirty, r.DeviceID)
	}
	p.mu.Unlock()
	rows, err := p.store.Devices(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	current := map[string]Device{}
	for _, d := range rows {
		// A push can be accepted during the read. Keep its newer local timestamp
		// and dirty bit so the next pass persists it instead of losing liveness.
		if old := p.devices[d.DeviceID]; old.LastPushAt.After(d.LastPushAt) {
			d.LastPushAt = old.LastPushAt
		}
		current[d.DeviceID] = d
	}
	for id := range p.devices {
		if _, ok := current[id]; !ok {
			delete(p.failed, id)
			delete(p.replaced, id)
			delete(p.acceptedDirty, id)
		}
	}
	p.devices = current
	for _, r := range retired {
		p.logf("push registration retired registration=%s canonical=%s reason=%s", trafficIdentity(r.DeviceID), trafficIdentity(r.CanonicalID), r.Reason)
	}
	return nil
}
