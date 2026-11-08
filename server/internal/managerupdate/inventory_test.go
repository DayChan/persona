package managerupdate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

func idleReaderFixture() (Authority, nodeclient.UpdateInventory, func() time.Time) {
	at := time.Now().UTC()
	a := Authority{ManagerID: "m-fixture", Main: "main", MachineID: "box", Revision: strings.Repeat("a", 64), ObservedAt: at}
	target := nodeclient.UpdateInventoryTarget{Manager: a.ManagerID, Main: a.Main, Machine: a.MachineID, MachineUID: "uid", AuthorityRevision: a.Revision}
	in := nodeclient.UpdateInventory{Schema: 1, Capability: nodeproto.ManagerUpdateInventory, Complete: true, Target: target, Instance: "instance", Revision: strings.Repeat("b", 64), CompletedAt: at, Sources: map[string]string{"owner-processes": strings.Repeat("d", 64), "assistant-ownership": strings.Repeat("d", 64), "codex-config": strings.Repeat("d", 64)}, Rows: []nodeclient.UpdateActivity{{Source: "not-loaded", Session: a.Main, Status: "offline", Revision: strings.Repeat("c", 64)}}}
	var err error
	in.Revision, err = nodeclient.InventoryRevision(in)
	if err != nil {
		panic(err)
	}
	return a, in, func() time.Time { return at }
}
func TestIdleAuthorityAndProducerDoubleRead(t *testing.T) {
	a, in, now := idleReaderFixture()
	reads, identities := 0, 0
	result, err := readIdleEvidence(context.Background(), a.ManagerID, a.Main, a.MachineID, "uid", func(context.Context) (Authority, error) { identities++; return a, nil }, func(ctx context.Context, target nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error) {
		reads++
		if target != in.Target {
			t.Fatal("wrong explicit target")
		}
		return in, nil
	}, now)
	if err != nil || !result.Idle || result.CompleteReads != 2 || reads != 2 || identities != 3 || len(result.SHA256) != 64 {
		t.Fatalf("incomplete proof: %+v %v", result, err)
	}
}
func TestIdleAuthorityRejectsChangesAndLegacyProducer(t *testing.T) {
	for _, name := range []string{"legacy", "main", "revision", "after", "stale", "busy", "background", "child", "compaction", "interaction", "unknown", "timeout", "failure"} {
		t.Run(name, func(t *testing.T) {
			a, in, now := idleReaderFixture()
			calls, identities := 0, 0
			_, err := readIdleEvidence(context.Background(), a.ManagerID, a.Main, a.MachineID, "uid", func(context.Context) (Authority, error) {
				identities++
				copy := a
				if name == "after" && identities == 3 {
					copy.Main = "other"
				}
				return copy, nil
			}, func(context.Context, nodeclient.UpdateInventoryTarget) (nodeclient.UpdateInventory, error) {
				calls++
				copy := in
				copy.Rows = append([]nodeclient.UpdateActivity(nil), in.Rows...)
				switch name {
				case "legacy":
					copy.Capability = ""
				case "main":
					copy.Rows = nil
				case "revision":
					if calls == 2 {
						copy.Revision = strings.Repeat("d", 64)
					}
				case "stale":
					copy.CompletedAt = copy.CompletedAt.Add(-time.Minute)
				case "busy":
					copy.Rows[0].Live = true
					copy.Rows[0].Source = "owned"
					copy.Rows[0].Status = "working"
				case "background":
					copy.Rows[0].Background = 1
				case "child":
					copy.Rows[0].Subagents = 1
				case "compaction":
					copy.Rows[0].Compacting = true
				case "interaction":
					copy.Rows[0].PendingInteractions = 1
				case "unknown":
					copy.Rows[0].Status = "interrupted"
				case "timeout":
					now = func() time.Time { return in.CompletedAt.Add(time.Minute) }
				case "failure":
					return copy, errors.New("source failed")
				}
				if name != "revision" {
					var err error
					copy.Revision, err = nodeclient.InventoryRevision(copy)
					if err != nil {
						t.Fatal(err)
					}
				}
				return copy, nil
			}, func() time.Time { return now() })
			if err == nil {
				t.Fatal("unknown, changed or active evidence authorized idle")
			}
		})
	}
}
