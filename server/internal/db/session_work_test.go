package db

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestSessionWork(t *testing.T) {
	d, ctx := open(t), context.Background()
	_, _ = d.pool.Exec(ctx, `TRUNCATE session_work, session_work_events`)
	turn := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	wake := turn.Add(2 * time.Hour)
	w := SessionWork{MachineID: "server", SessionID: "s", TurnAt: turn, WakeAt: &wake, Retries: 1, InteractionID: "work_1",
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Line: "等部署", WakeAt: &wake}, UpdatedAt: turn}
	if err := d.PutSessionWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	w.Wakes, w.WakeAt = 2, nil // an update replaces the row
	if err := d.PutSessionWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	old := w
	old.SessionID, old.TurnAt = "old", turn.Add(-40*24*time.Hour)
	_ = d.PutSessionWork(ctx, old)
	got, err := d.SessionWorks(ctx, turn.Add(-time.Minute))
	if err != nil || len(got) != 1 || got[0].Wakes != 2 || got[0].WakeAt != nil || got[0].Work.Line != "等部署" ||
		got[0].InteractionID != "work_1" || !got[0].TurnAt.Equal(turn) {
		t.Fatalf("works = %+v, %v", got, err)
	}

	_ = d.AddWorkEvent(ctx, WorkEvent{At: turn, MachineID: "server", SessionID: "s", Kind: "triaged", Detail: map[string]any{"phase": "done"}})
	_ = d.AddWorkEvent(ctx, WorkEvent{At: turn.Add(-40 * 24 * time.Hour), Kind: "sample"})
	if err := d.PruneSessionWork(ctx, turn.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ev, err := d.WorkEvents(ctx, turn.Add(-60*24*time.Hour))
	if err != nil || len(ev) != 1 || ev[0].Kind != "triaged" || ev[0].Detail["phase"] != "done" {
		t.Fatalf("events = %+v, %v", ev, err)
	}
	if all, _ := d.SessionWorks(ctx, time.Time{}); len(all) != 1 {
		t.Fatalf("pruned works = %+v", all)
	}
}

// Older active CI outbox entries survive a manager restart; ordinary old turns
// and cancelled watches are not restored by the recent-work query.
func TestSessionWorkRestoresOldCI(t *testing.T) {
	d, ctx := open(t), context.Background()
	_, _ = d.pool.Exec(ctx, `TRUNCATE session_work, session_work_events`)
	now := time.Now().UTC().Truncate(time.Millisecond)
	wake := now.Add(time.Minute)
	old := SessionWork{MachineID: "box", SessionID: "active", TurnAt: now.Add(-72 * time.Hour), WakeAt: &wake, UpdatedAt: now,
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, CI: &proto.WorkCI{Number: 12, Message: "CI finished"}}}
	if err := d.PutSessionWork(ctx, old); err != nil {
		t.Fatal(err)
	}
	cancelled := old
	cancelled.SessionID = "cancelled"
	cancelled.WakeAt = nil
	if err := d.PutSessionWork(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	ordinary := old
	ordinary.SessionID = "ordinary"
	ordinary.Work.CI = nil
	if err := d.PutSessionWork(ctx, ordinary); err != nil {
		t.Fatal(err)
	}
	got, err := d.SessionWorks(ctx, now.Add(-48*time.Hour))
	if err != nil || len(got) != 1 || got[0].SessionID != "active" || got[0].Work.CI == nil || got[0].Work.CI.Message != "CI finished" {
		t.Fatalf("restored %+v: %v", got, err)
	}
	ancient := old
	ancient.SessionID, ancient.TurnAt = "ancient", now.Add(-40*24*time.Hour)
	if err := d.PutSessionWork(ctx, ancient); err != nil {
		t.Fatal(err)
	}
	cancelled.TurnAt = ancient.TurnAt
	if err := d.PutSessionWork(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	retired := cancelled
	retired.SessionID, retired.UpdatedAt = "retired", now
	retired.Work = proto.WorkState{RetiredAt: &now}
	if err := d.PutSessionWork(ctx, retired); err != nil {
		t.Fatal(err)
	}
	if err := d.PruneSessionWork(ctx, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err = d.SessionWorks(ctx, now.Add(-48*time.Hour))
	if err != nil || len(got) != 3 {
		t.Fatalf("active CI watches and retirement not retained: %+v, %v", got, err)
	}
}
