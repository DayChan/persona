package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func viewRows(id, conversation string, at time.Time) (AssistantViewRow, FeedRow) {
	v := AssistantViewRow{ID: id, Conversation: conversation, MachineID: "example-machine", View: json.RawMessage(`{"fallback":"Example","sections":[]}`), Version: 1, CreatedAt: at, UpdatedAt: at}
	f := FeedRow{ID: "feed_" + id, Conversation: conversation, At: at, Kind: "view", Ref: id, Body: json.RawMessage(`{}`)}
	return v, f
}

func openViews(t *testing.T) *DB {
	d := open(t)
	if _, err := d.pool.Exec(context.Background(), `TRUNCATE assistant_views, assistant_feed`); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAssistantViewStoredWithFeed(t *testing.T) {
	d, ctx := openViews(t), context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	v, f := viewRows("view_one", "conversation-a", at)
	if err := d.CreateAssistantView(ctx, v, f); err != nil {
		t.Fatal(err)
	}
	got, err := d.AssistantView(ctx, v.Conversation, v.ID)
	if err != nil || got.Version != 1 || got.MachineID != v.MachineID || !got.CreatedAt.Equal(at) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := d.AssistantView(ctx, "conversation-b", v.ID); !errors.Is(err, ErrAssistantViewNotFound) {
		t.Fatalf("other conversation read: %v", err)
	}
	if _, err := d.UpdateAssistantView(ctx, "conversation-b", v.ID, 1, json.RawMessage(`{}`), at); !errors.Is(err, ErrAssistantViewNotFound) {
		t.Fatalf("other conversation update: %v", err)
	}
	updated, err := d.UpdateAssistantView(ctx, v.Conversation, v.ID, 1, json.RawMessage(`{"fallback":"Done","sections":[]}`), at.Add(time.Second))
	if err != nil || updated.Version != 2 || !updated.CreatedAt.Equal(at) {
		t.Fatalf("updated %+v, %v", updated, err)
	}
	if _, err := d.UpdateAssistantView(ctx, v.Conversation, v.ID, 1, json.RawMessage(`{}`), at.Add(2*time.Second)); !errors.Is(err, ErrAssistantViewVersion) {
		t.Fatalf("stale update: %v", err)
	}
	feed, err := d.Feed(ctx, v.Conversation, time.Time{}, 10)
	if err != nil || len(feed) != 1 || feed[0].Ref != v.ID || !feed[0].At.Equal(at) {
		t.Fatalf("feed %+v, %v", feed, err)
	}
	list, err := d.AssistantViews(ctx, v.Conversation)
	if err != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatalf("views %+v, %v", list, err)
	}
	other, err := d.AssistantViews(ctx, "conversation-b")
	if err != nil || len(other) != 0 {
		t.Fatalf("other views %+v, %v", other, err)
	}
}

func TestAssistantViewCreateRollback(t *testing.T) {
	d, ctx := openViews(t), context.Background()
	v, f := viewRows("view_rollback", "conversation-a", time.Now().UTC())
	if err := d.AppendFeed(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateAssistantView(ctx, v, f); err == nil {
		t.Fatal("accepted conflicting feed id")
	}
	if _, err := d.AssistantView(ctx, v.Conversation, v.ID); !errors.Is(err, ErrAssistantViewNotFound) {
		t.Fatalf("card left after rollback: %v", err)
	}
	f.ID = "another_feed"
	f.Conversation = "conversation-b"
	if err := d.CreateAssistantView(ctx, v, f); err == nil {
		t.Fatal("accepted feed for another conversation")
	}
}

func TestAssistantViewConcurrentLimit(t *testing.T) {
	d, ctx := openViews(t), context.Background()
	at := time.Now().UTC()
	for i := 0; i < AssistantViewsMost-1; i++ {
		v, f := viewRows(fmt.Sprintf("view_%d", i), "conversation-a", at)
		if err := d.CreateAssistantView(ctx, v, f); err != nil {
			t.Fatal(err)
		}
	}
	var added atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, f := viewRows(fmt.Sprintf("view_concurrent_%d", i), "conversation-a", at)
			err := d.CreateAssistantView(ctx, v, f)
			if err == nil {
				added.Add(1)
			} else if !errors.Is(err, ErrAssistantViewLimit) {
				t.Errorf("create: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if added.Load() != 1 {
		t.Fatalf("concurrent additions: %d", added.Load())
	}
	v, f := viewRows("view_other", "conversation-b", at)
	if err := d.CreateAssistantView(ctx, v, f); err != nil {
		t.Fatalf("limit leaked across conversations: %v", err)
	}
}

func TestAssistantViewConcurrentVersion(t *testing.T) {
	d, ctx := openViews(t), context.Background()
	at := time.Now().UTC()
	v, f := viewRows("view_concurrent", "conversation-a", at)
	if err := d.CreateAssistantView(ctx, v, f); err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.UpdateAssistantView(ctx, v.Conversation, v.ID, 1, json.RawMessage(`{"fallback":"Updated","sections":[]}`), at.Add(time.Second))
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrAssistantViewVersion) {
				t.Errorf("update: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("updates with same expected version: %d", won.Load())
	}
}

func TestAssistantViewKeysAndByID(t *testing.T) {
	d, ctx := openViews(t), context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	for _, row := range []struct{ id, conversation string }{{"view_a1", "conversation-a"}, {"view_a2", "conversation-a"}, {"view_b1", "conversation-b"}} {
		v, f := viewRows(row.id, row.conversation, at)
		if err := d.CreateAssistantView(ctx, v, f); err != nil {
			t.Fatal(err)
		}
	}
	keys := func() map[string]AssistantViewKey {
		t.Helper()
		list, err := d.AssistantViewKeys(ctx, "conversation-a")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]AssistantViewKey{}
		for _, k := range list {
			out[k.ID] = k
		}
		return out
	}
	before := keys()
	if len(before) != 2 || before["view_a1"].Version != 1 || before["view_a1"].Xmin == 0 {
		t.Fatalf("keys %+v", before)
	}
	if _, err := d.UpdateAssistantView(ctx, "conversation-a", "view_a1", 1, json.RawMessage(`{"fallback":"Done","sections":[]}`), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	updated := keys()
	if updated["view_a1"].Version != 2 || updated["view_a1"].Xmin == before["view_a1"].Xmin || updated["view_a2"] != before["view_a2"] {
		t.Fatalf("after update %+v, before %+v", updated, before)
	}
	// A rewrite that keeps the version (a migration, a manual repair) still shows in xmin.
	if _, err := d.pool.Exec(ctx, `UPDATE assistant_views SET view = view WHERE id = 'view_a2'`); err != nil {
		t.Fatal(err)
	}
	if rewritten := keys()["view_a2"]; rewritten.Version != 1 || rewritten.Xmin == before["view_a2"].Xmin {
		t.Fatalf("rewrite %+v, before %+v", rewritten, before["view_a2"])
	}
	got, err := d.AssistantViewsByID(ctx, "conversation-a", []string{"view_a1", "view_b1", "view_missing"})
	if err != nil || len(got) != 1 || got[0].ID != "view_a1" || got[0].Version != 2 || got[0].Xmin != keys()["view_a1"].Xmin || string(got[0].View) != `{"fallback": "Done", "sections": []}` {
		t.Fatalf("by id %+v, %v", got, err)
	}
	if none, err := d.AssistantViewsByID(ctx, "conversation-a", nil); err != nil || len(none) != 0 {
		t.Fatalf("no ids %+v, %v", none, err)
	}
}
