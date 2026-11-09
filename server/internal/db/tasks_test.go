package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAppendFeedOnce(t *testing.T) {
	d, ctx := open(t), context.Background()
	row := FeedRow{ID: "receipt_once", Conversation: "receipt-once-test", At: time.Now(), Kind: "receipt", Ref: "task", Body: json.RawMessage(`{"state":"done"}`)}
	inserted, err := d.AppendFeedOnce(ctx, row)
	if err != nil || !inserted {
		t.Fatalf("first insert = %v, %v", inserted, err)
	}
	row.Body = json.RawMessage(`{"state":"interrupted"}`)
	inserted, err = d.AppendFeedOnce(ctx, row)
	if err != nil || inserted {
		t.Fatalf("repeat insert = %v, %v", inserted, err)
	}
	got, err := d.FeedItem(ctx, row.ID)
	if err != nil || got == nil || !strings.Contains(string(got.Body), "done") {
		t.Fatalf("stored receipt = %+v, %v", got, err)
	}
}

func TestAppendTaskReceiptOnceMembershipOrdering(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "receipt-task", Title: "Task", Origin: "receipt-membership-test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LinkTodoAgent(ctx, "receipt-task", "box", "s1", "", "", "", "", now); err != nil {
		t.Fatal(err)
	}
	row := FeedRow{ID: "receipt-after-unlink", Conversation: "receipt-membership-test", At: now, Kind: "receipt", Ref: "receipt-task"}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTodoAgentMembership(ctx, tx, row.Ref, "box", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE todo_agents SET released_at = $4, dismissed_at = $4 WHERE todo_id = $1 AND machine_id = $2 AND session_id = $3`, row.Ref, "box", "s1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		inserted, valid bool
		err             error
	}
	started, resultCh := make(chan struct{}), make(chan result, 1)
	go func() {
		close(started)
		a, b, err := d.AppendTaskReceiptOnce(ctx, row, "box", "s1", now)
		resultCh <- result{a, b, err}
	}()
	<-started
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-resultCh; got.err != nil || got.valid || got.inserted {
		t.Fatalf("receipt after unlink = %+v", got)
	}
	if got, err := d.FeedItem(ctx, row.ID); err != nil || got != nil {
		t.Fatalf("invalid receipt = %+v, %v", got, err)
	}

	relinked := now.Add(time.Minute)
	if _, err := d.LinkTodoAgent(ctx, "receipt-task", "box", "s1", "", "", "", "", relinked); err != nil {
		t.Fatal(err)
	}
	row.ID = "receipt-before-unlink"
	tx, err = d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inserted, valid, err := appendTaskReceiptOnceTx(ctx, tx, row, "box", "s1", relinked)
	if err != nil || !inserted || !valid {
		t.Fatalf("receipt before unlink = %v %v %v", inserted, valid, err)
	}
	started2, done := make(chan struct{}), make(chan error, 1)
	go func() { close(started2); done <- d.UnlinkTodoAgent(ctx, row.Ref, "box", "s1") }()
	<-started2
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, err := d.FeedItem(ctx, row.ID); err != nil || got == nil {
		t.Fatalf("committed receipt = %+v, %v", got, err)
	}
}

// A message card's sent mark: set once on the cards about the item put in by then, kept, and listed again to a
// client that asks for what came since (its time does not change, its body does).
func TestFeedMarkSent(t *testing.T) {
	d, ctx := open(t), context.Background()
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	add := func(id, kind, ref string, at time.Time) {
		t.Helper()
		if err := d.AppendFeed(ctx, FeedRow{ID: id, Conversation: "c1", At: at, Kind: kind, Ref: ref, Body: json.RawMessage(`{"draft":"在修了"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	add("f1", "message", "inbox:1", t0)
	add("f2", "task", "inbox:1", t0.Add(time.Minute))
	add("f3", "message", "inbox:2", t0.Add(2*time.Minute))
	add("f4", "message", "inbox:1", t0.Add(30*time.Minute)) // a later draft

	sent := t0.Add(10 * time.Minute)
	patch := json.RawMessage(`{"sent_at":"` + sent.Format(time.RFC3339Nano) + `","sent":"修好了"}`)
	rows, err := d.MarkFeedSent(ctx, "message", "inbox:1", sent, patch)
	if err != nil || len(rows) != 1 || rows[0].ID != "f1" {
		t.Fatalf("MarkFeedSent = %+v, %v", rows, err)
	}
	var b map[string]string
	_ = json.Unmarshal(rows[0].Body, &b)
	if b["draft"] != "在修了" || b["sent"] != "修好了" || b["sent_at"] == "" {
		t.Fatalf("body = %s", rows[0].Body)
	}
	// Marked once: a second send does not move it.
	again := json.RawMessage(`{"sent_at":"` + t0.Add(40*time.Minute).Format(time.RFC3339Nano) + `","sent":"别的"}`)
	if rows, err := d.MarkFeedSent(ctx, "message", "inbox:1", t0.Add(20*time.Minute), again); err != nil || len(rows) != 0 {
		t.Fatalf("marked again: %+v, %v", rows, err)
	}
	if r, err := d.FeedItem(ctx, "f1"); err != nil || r == nil || r.Kind != "message" || !json.Valid(r.Body) ||
		!strings.Contains(string(r.Body), `"sent": "修好了"`) {
		t.Fatalf("FeedItem = %+v (%s), %v", r, r.Body, err)
	}
	if r, err := d.FeedItem(ctx, "nope"); err != nil || r != nil {
		t.Fatalf("FeedItem(nope) = %+v, %v", r, err)
	}

	// A client that has everything up to f3 gets f4 and the card sent after f3 came.
	list, err := d.Feed(ctx, "c1", t0.Add(2*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	if len(ids) != 2 || ids[0] != "f1" || ids[1] != "f4" {
		t.Fatalf("since f3: %v", ids)
	}
	if list, _ := d.Feed(ctx, "c1", t0.Add(30*time.Minute), 100); len(list) != 0 {
		t.Fatalf("since f4: %+v", list)
	}
}

// MarkFeedSent finds the unsent cards through assistant_feed_unsent rather than scanning the whole feed (#2617).
func TestMarkFeedSentUsesUnsentIndex(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The table is tiny here, so tell the planner a scan costs too much. Without assistant_feed_unsent it still
	// avoids the scan (a bitmap scan of assistant_feed_conversation), so check the plan names the index itself.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN `+markFeedSentSQL, "message", "inbox:1", time.Now(), []byte(`{"sent_at":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "assistant_feed_unsent") {
		t.Fatalf("MarkFeedSent does not use assistant_feed_unsent:\n%s", strings.Join(plan, "\n"))
	}
}

// feedIDs is the ids Feed lists for c1 since since, at most limit.
func feedIDs(t *testing.T, d *DB, since time.Time, limit int) []string {
	t.Helper()
	list, err := d.Feed(context.Background(), "c1", since, limit)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	return ids
}

// The cards sent after since and the items put in after it come in one list by time, each once, cut at limit; a card
// put in with sent_at already in its body counts as sent too.
func TestFeedSentAndNewInOrder(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	add := func(id, kind string, at time.Time, body string) {
		t.Helper()
		if err := d.AppendFeed(ctx, FeedRow{ID: id, Conversation: "c1", At: at, Kind: kind, Ref: "inbox:" + id, Body: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
	}
	sentAt := func(at time.Time) string { return `{"sent_at":"` + at.Format(time.RFC3339Nano) + `"}` }
	since := t0.Add(10 * time.Minute)
	add("a", "message", t0, `{}`)                                             // sent after since below
	add("b", "message", t0.Add(time.Minute), sentAt(since.Add(time.Minute)))  // put in already sent, after since
	add("c", "message", t0.Add(2*time.Minute), sentAt(t0.Add(3*time.Minute))) // sent before since
	add("d", "task", t0.Add(3*time.Minute), sentAt(since.Add(time.Minute)))   // not a card
	add("e", "task", since.Add(time.Minute), `{}`)
	add("f", "message", since.Add(2*time.Minute), `{}`)
	add("g", "task", since.Add(3*time.Minute), `{}`)
	if _, err := d.MarkFeedSent(ctx, "message", "inbox:a", since.Add(5*time.Minute), json.RawMessage(sentAt(since.Add(5*time.Minute)))); err != nil {
		t.Fatal(err)
	}
	// f is sent after since and put in after it: listed once.
	if _, err := d.MarkFeedSent(ctx, "message", "inbox:f", since.Add(6*time.Minute), json.RawMessage(sentAt(since.Add(6*time.Minute)))); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(feedIDs(t, d, since, 100), ","); got != "a,b,e,f,g" {
		t.Fatalf("since: %s", got)
	}
	if got := strings.Join(feedIDs(t, d, since, 3), ","); got != "a,b,e" {
		t.Fatalf("since, limit 3: %s", got)
	}
	if got := strings.Join(feedIDs(t, d, time.Time{}, 4), ","); got != "a,b,c,d" {
		t.Fatalf("from the start, limit 4: %s", got)
	}
}

// A database from before 0390 keeps sent_at only in the bodies: the migration copies it to the column, so its sent
// cards are still listed.
func TestFeedSentAtBackfill(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE assistant_feed`); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	since := t0.Add(10 * time.Minute)
	for _, q := range []string{
		`DROP INDEX assistant_feed_sent`,
		`ALTER TABLE assistant_feed DROP COLUMN sent_at`,
		`DELETE FROM schema_migrations WHERE version = 390`,
	} {
		if _, err := d.pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, r := range []struct {
		id, kind string
		at       time.Time
		body     string
	}{
		{"old-sent", "message", t0, `{"sent_at":"` + since.Add(time.Minute).Format(time.RFC3339Nano) + `"}`},
		{"old-early", "message", t0, `{"sent_at":"` + t0.Add(time.Minute).Format(time.RFC3339Nano) + `"}`},
		{"old-draft", "message", t0, `{"draft":"x"}`},
		{"old-task", "task", t0, `{"sent_at":"` + since.Add(time.Minute).Format(time.RFC3339Nano) + `"}`},
	} {
		if _, err := d.pool.Exec(ctx, `INSERT INTO assistant_feed (id, conversation, at, kind, ref, body) VALUES ($1, 'c1', $2, $3, '', $4)`,
			r.id, r.at, r.kind, []byte(r.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(feedIDs(t, d, since, 100), ","); got != "old-sent" {
		t.Fatalf("after backfill: %s", got)
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM assistant_feed WHERE sent_at IS NOT NULL`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("backfilled %d, %v", n, err)
	}
}

// TaskPlansByID reads several plans in one query: no ids ask nothing (a DB without a pool would panic on a query), an
// unknown id is left out and a repeated one gives one row.
func TestTaskPlansByID(t *testing.T) {
	ctx := context.Background()
	if rows, err := (&DB{}).TaskPlansByID(ctx, nil); err != nil || rows != nil {
		t.Fatalf("no ids: %+v, %v", rows, err)
	}
	d := open(t)
	prefix := fmt.Sprintf("plan_byid_%d", time.Now().UnixNano()) // task_plans is not truncated between tests
	at := time.Now().UTC().Truncate(time.Microsecond)
	want := map[string]TaskPlanRow{}
	for i := range 3 {
		p := TaskPlanRow{ID: fmt.Sprintf("%s_%d", prefix, i), Conversation: prefix, Tasks: json.RawMessage(fmt.Sprintf(`[{"title": "Task %d"}]`, i)),
			State: "open", CreatedAt: at, UpdatedAt: at.Add(time.Duration(i) * time.Second)}
		if err := d.CreateTaskPlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		want[p.ID] = p
	}
	ids := []string{prefix + "_2", prefix + "_nope", prefix + "_0", prefix + "_2"}
	rows, err := d.TaskPlansByID(ctx, ids)
	if err != nil || len(rows) != 2 {
		t.Fatalf("TaskPlansByID = %+v, %v", rows, err)
	}
	for _, r := range rows {
		w, ok := want[r.ID]
		one, err := d.TaskPlan(ctx, r.ID)
		if !ok || r.ID == prefix+"_1" || err != nil || r.Conversation != w.Conversation || string(r.Tasks) != string(w.Tasks) ||
			r.State != w.State || !r.CreatedAt.Equal(w.CreatedAt) || !r.UpdatedAt.Equal(w.UpdatedAt) ||
			string(one.Tasks) != string(r.Tasks) || !one.UpdatedAt.Equal(r.UpdatedAt) {
			t.Fatalf("row %+v, want %+v (TaskPlan %+v, %v)", r, w, one, err)
		}
	}
	if rows[0].ID == rows[1].ID {
		t.Fatalf("a repeated id read twice: %+v", rows)
	}
	if rows, err := d.TaskPlansByID(ctx, []string{prefix + "_nope"}); err != nil || len(rows) != 0 {
		t.Fatalf("unknown ids: %+v, %v", rows, err)
	}
}
