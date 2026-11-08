package managerupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// IdleEvidence is produced by a reader, separately from the fixed release
// candidate performing the transaction. Reading it never creates a plan/receipt.
type IdleEvidence struct {
	Schema        int                        `json:"schema"`
	Complete      bool                       `json:"complete"`
	Idle          bool                       `json:"idle"`
	CompleteReads int                        `json:"complete_reads"`
	Authority     Authority                  `json:"authority"`
	Inventory     nodeclient.UpdateInventory `json:"inventory"`
	SHA256        string                     `json:"sha256"`
	StartedAt     time.Time                  `json:"started_at"`
	CompletedAt   time.Time                  `json:"completed_at"`
}

type inventoryRead func(context.Context, nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error)

func (c *Controller) ReadIdleEvidence(ctx context.Context, p Plan, main, machine, uid string) (IdleEvidence, error) {
	read := func(ctx context.Context, target nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error) {
		input, err := json.Marshal(target)
		if err != nil {
			return nodeclient.UpdateInventory{}, err
		}
		data, err := nodeclient.Dial(ctx, nodeclient.Request{Kind: "update.inventory", Input: input})
		if err != nil {
			return nodeclient.UpdateInventory{}, err
		}
		var response struct {
			nodeclient.UpdateInventory
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nodeclient.UpdateInventory{}, err
		}
		if response.Error != "" {
			return nodeclient.UpdateInventory{}, fmt.Errorf("node inventory: %s", response.Error)
		}
		return response.UpdateInventory, nil
	}
	return readIdleEvidence(ctx, p.ManagerID, main, machine, uid, func(ctx context.Context) (Authority, error) { return c.ReadAuthority(ctx, p) }, read, time.Now)
}

func readIdleEvidence(ctx context.Context, manager, main, machine, uid string, authority func(context.Context) (Authority, error), read inventoryRead, now func() time.Time) (IdleEvidence, error) {
	out := IdleEvidence{Schema: 1, StartedAt: now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if manager == "" || main == "" || machine == "" || uid == "" {
		return out, errors.New("explicit original main, machine and UID required")
	}
	var originalAuthority, originalInventory string
	for i := 0; i < 2; i++ {
		a, err := authority(ctx)
		if err != nil {
			return out, err
		}
		if a.ManagerID != manager || a.Main != main || a.MachineID != machine || !shaPattern.MatchString(a.Revision) {
			return out, errors.New("persisted manager/main/machine identity conflicts")
		}
		if age := now().Sub(a.ObservedAt); age < -5*time.Second || age >= 30*time.Second {
			return out, errors.New("authority read is stale or its clock conflicts")
		}
		target := nodeclient.UpdateInventoryTarget{Manager: manager, Main: main, Machine: machine, MachineUID: uid, AuthorityRevision: a.Revision}
		inventory, err := read(ctx, target)
		if err != nil {
			return out, err
		}
		out.Authority, out.Inventory = a, inventory
		if err := validateIdleInventory(inventory, target, now()); err != nil {
			return out, err
		}
		if i > 0 && (originalAuthority != a.Revision || originalInventory != inventory.Revision) {
			return out, errors.New("authority revision or complete runtime inventory changed between reads")
		}
		originalAuthority, originalInventory = a.Revision, inventory.Revision
		out.Authority, out.Inventory = a, inventory
	}
	// Re-read persisted identity after the final runtime read as well: a main
	// switch during that read cannot be covered by the preceding identity.
	after, err := authority(ctx)
	if err != nil {
		return out, err
	}
	if after.ManagerID != manager || after.Main != main || after.MachineID != machine || after.Revision != originalAuthority {
		return out, errors.New("manager authority changed after final runtime read")
	}
	out.CompletedAt = now().UTC()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if elapsed := out.CompletedAt.Sub(out.StartedAt); elapsed < 0 || elapsed >= 30*time.Second {
		return out, errors.New("complete evidence acquisition budget expired")
	}
	data, err := json.Marshal(struct {
		Authority string
		Inventory string
	}{originalAuthority, originalInventory})
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(data)
	out.SHA256 = hex.EncodeToString(digest[:])
	out.Complete, out.Idle, out.CompleteReads = true, true, 2
	return out, nil
}

func validateIdleInventory(in nodeclient.UpdateInventory, target nodeclient.UpdateInventoryTarget, now time.Time) error {
	if in.Schema != 1 || in.Capability != nodeproto.ManagerUpdateInventory || !in.Complete || in.Target != target || in.Instance == "" || !shaPattern.MatchString(in.Revision) || len(in.Sources) < 3 {
		return errors.New("authoritative node inventory capability or complete coverage unavailable")
	}
	if age := now.Sub(in.CompletedAt); age < -5*time.Second || age >= 30*time.Second {
		return errors.New("node inventory is stale or its clock conflicts")
	}
	computed, err := nodeclient.InventoryRevision(in)
	if err != nil || computed != in.Revision {
		return errors.New("runtime payload conflicts with its revision")
	}
	for _, key := range []string{"owner-processes", "assistant-ownership", "codex-config"} {
		if !shaPattern.MatchString(in.Sources[key]) {
			return errors.New("required inventory source revision unavailable")
		}
	}
	mainFound := false
	seen := map[string]bool{}
	for _, row := range in.Rows {
		key := row.Session
		if row.Session == "" || row.Source == "" || seen[key] || !shaPattern.MatchString(row.Revision) {
			return errors.New("runtime row identity or revision is ambiguous")
		}
		seen[key] = true
		mainFound = mainFound || row.Session == target.Main
		if !row.Live && (row.Source != "not-loaded" || row.Status != proto.StatusOffline) {
			return errors.New("non-live runtime provenance is unknown")
		}
		if row.Live && row.Status != proto.StatusIdle || row.Starting || row.Background != 0 || row.Subagents != 0 || row.Compacting || row.PendingInteractions != 0 {
			return fmt.Errorf("runtime activity is busy or unknown: source=%s session=%s status=%s", row.Source, row.Session, row.Status)
		}
	}
	if !mainFound {
		return errors.New("original main is not covered by complete runtime evidence")
	}
	return nil
}
