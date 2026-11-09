package db

import (
	"context"
	"testing"
	"time"
)

func TestMachineTrust(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if list, err := d.PendingMachines(ctx); err != nil || len(list) != 0 {
		t.Fatalf("no rows: every computer is confirmed: %v %v", list, err)
	}
	if err := d.SetMachinePending(ctx, "laptop", "dev-1"); err != nil {
		t.Fatal(err)
	}
	list, err := d.PendingMachines(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "laptop" || list[0].AddedBy != "dev-1" || list[0].CreatedAt.IsZero() {
		t.Fatalf("pending %+v %v", list, err)
	}
	if ok, err := d.ConfirmMachine(ctx, "other"); err != nil || ok {
		t.Fatalf("confirming one that is not pending: %v %v", ok, err)
	}
	if ok, err := d.ConfirmMachine(ctx, "laptop"); err != nil || !ok {
		t.Fatalf("confirm %v %v", ok, err)
	}
	if ok, _ := d.ConfirmMachine(ctx, "laptop"); ok {
		t.Fatal("confirmed twice")
	}
	if list, _ := d.PendingMachines(ctx); len(list) != 0 {
		t.Fatalf("confirmed is not pending: %+v", list)
	}
	if err := d.SetMachinePending(ctx, "laptop", "dev-2"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.PendingMachines(ctx); len(list) != 1 || list[0].AddedBy != "dev-2" {
		t.Fatalf("pending again %+v", list)
	}
	if err := d.ForgetMachineTrust(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.PendingMachines(ctx); len(list) != 0 {
		t.Fatalf("forgotten %+v", list)
	}
}

func TestRevokeMachineCredentials(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	for _, tok := range []DeviceToken{
		{ID: "link-1", Name: "laptop", Kind: "link", Role: "link", MachineID: "laptop"},
		{ID: "link-2", Name: "desk", Kind: "link", Role: "link", MachineID: "desk"},
		{ID: "phone-1", Name: "phone", Kind: "phone", Role: "full"},
		{ID: "peer-1", Name: "hub", Kind: "peer", Role: "peer", MachineID: "laptop"}, // a peer hub whose id is the same
	} {
		if err := d.CreateDeviceToken(ctx, tok, "hash-"+tok.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.CreatePairToken(ctx, "pair-laptop", time.Now().Add(10*time.Minute), "laptop"); err != nil {
		t.Fatal(err)
	}
	ids, err := d.RevokeMachineCredentials(ctx, "laptop")
	if err != nil || len(ids) != 1 || ids[0] != "link-1" {
		t.Fatalf("revoked %v %v", ids, err)
	}
	if tok, _ := d.DeviceTokenByHash(ctx, "hash-link-1"); tok != nil {
		t.Fatal("the laptop's credential still works")
	}
	for _, h := range []string{"hash-link-2", "hash-phone-1", "hash-peer-1"} {
		if tok, _ := d.DeviceTokenByHash(ctx, h); tok == nil {
			t.Fatalf("%s was revoked too", h)
		}
	}
	if p, _ := d.PairTokenByHash(ctx, "pair-laptop"); p == nil || p.ExpiresAt.After(time.Now()) {
		t.Fatalf("the laptop's pairing code still works: %+v", p)
	}
}

func TestMachineAudit(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if err := d.AddMachineAudit(ctx, "laptop", "confirm", "dev-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.AddMachineAudit(ctx, "laptop", "reject", "dev-1", []byte(`{"why":"x"}`)); err != nil {
		t.Fatal(err)
	}
	list, err := d.MachineAudits(ctx, "laptop")
	if err != nil || len(list) != 2 || list[0].Action != "confirm" || list[1].Action != "reject" || string(list[1].Detail) != `{"why": "x"}` {
		t.Fatalf("audit %+v %v", list, err)
	}
}
