package db

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// inboxRows is n inbox items about the size of a Feishu chat's (its JSON with the last messages and the triage).
func inboxRows(n int, now time.Time) []InboxRow {
	rows := make([]InboxRow, n)
	for i := range rows {
		id := "feishu:chat-" + strconv.Itoa(i)
		rows[i] = InboxRow{ID: id, Data: []byte(fmt.Sprintf(`{"id":%q,"title":"示例群","last":"%0400d"}`, id, i)), UpdatedAt: now}
	}
	return rows
}

// PutInboxItems stores new items and updates those it has, as PutInboxItem does, and stores none when one fails.
func TestPutInboxItems(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now()
	if err := d.PutInboxItem(ctx, "feishu:chat-1", []byte(`{"id":"feishu:chat-1"}`), now, nil); err != nil {
		t.Fatal(err)
	}
	rows := inboxRows(3, now)
	rows[1].Data, rows[1].DoneAt = []byte(`{"id":"feishu:chat-1","count":2}`), &now
	if err := d.PutInboxItems(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := d.InboxItems(ctx, now.Add(-time.Hour))
	if err != nil || len(got) != 3 {
		t.Fatalf("rows = %d, %v", len(got), err)
	}
	var done int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE done_at IS NOT NULL AND data->>'count' = '2'`).Scan(&done); err != nil || done != 1 {
		t.Fatalf("updated row: %d, %v", done, err)
	}
	bad := inboxRows(2, now)
	bad[0].ID, bad[1].ID, bad[1].Data = "x:1", "x:2", []byte(`not json`)
	if err := d.PutInboxItems(ctx, bad); err == nil {
		t.Fatal("a row that is not JSON was stored")
	}
	if got, _ := d.InboxItems(ctx, now.Add(-time.Hour)); len(got) != 3 {
		t.Fatalf("rows after a failed batch = %d, want the batch stored whole or not at all", len(got))
	}
	if err := d.PutInboxItems(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

// Done items stay stored past the days the manager loads them (history), until they are purged; open ones never are.
func TestInboxHistoryKeptThenPurged(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now()
	day := 24 * time.Hour
	for id, done := range map[string]time.Duration{"d1": day, "d10": 10 * day, "d89": 89 * day, "d91": 91 * day} {
		at := now.Add(-done)
		if err := d.PutInboxItem(ctx, id, []byte(`{"id":"`+id+`"}`), at, &at); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PutInboxItem(ctx, "open", []byte(`{"id":"open"}`), now.Add(-200*day), nil); err != nil {
		t.Fatal(err)
	}
	rows, err := d.InboxItems(ctx, now.Add(-8*day))
	if err != nil || len(rows) != 2 {
		t.Fatalf("loaded %d rows (%v), want the open one and the one done a day ago", len(rows), err)
	}
	count := func() (n int) {
		if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 5 {
		t.Fatalf("%d rows stored after the load, want all 5: loading deletes nothing", n)
	}
	// One not loaded is still read by its id (a detail, a reopen); an open one or none is not.
	if got, err := d.DoneInboxItemsByID(ctx, []string{"d10", "open", "none"}); err != nil || len(got) != 1 || string(got[0]) != `{"id": "d10"}` {
		t.Fatalf("done items by id: %q %v", got, err)
	}
	if n, err := d.PurgeInboxItems(ctx, now.Add(-90*day)); err != nil || n != 1 {
		t.Fatalf("purged %d (%v), want the one done 91 days ago", n, err)
	}
	var ids []string
	r, _ := d.pool.Query(ctx, `SELECT id FROM inbox_items ORDER BY id`)
	ids, err = pgx.CollectRows(r, pgx.RowTo[string])
	if err != nil || strings.Join(ids, ",") != "d1,d10,d89,open" {
		t.Fatalf("left after the purge: %v %v", ids, err)
	}
}

// The done items page newest first by (done_at, id): items filed at one instant (a bulk action) across a page's end
// are neither repeated nor skipped; open items never show, connectors' only when asked for.
func TestDoneInboxItemsPages(t *testing.T) {
	d, ctx := open(t), context.Background()
	// A nanosecond instant: the column keeps microseconds, the items' JSON (and an app's cursor) nanoseconds.
	tie := time.Date(2026, 11, 1, 9, 30, 0, 123456789, time.UTC)
	put := func(id string, done *time.Time, data string) {
		t.Helper()
		if data == "" {
			data = `{"id":"` + id + `"}`
		}
		if err := d.PutInboxItem(ctx, id, []byte(data), tie, done); err != nil {
			t.Fatal(err)
		}
	}
	newer, older := tie.Add(time.Hour), tie.Add(-time.Hour)
	put("newest", &newer, "")
	for _, id := range []string{"b1", "b2", "b3", "b4", "b5"} {
		put(id, &tie, "")
	}
	put("oldest", &older, "")
	put("open", nil, "")
	conn := tie.Add(2 * time.Hour)
	put("github:1", &conn, `{"id":"github:1","source":"github","connector":{"thread":"t"}}`)

	walk := func(connectors bool) []string {
		t.Helper()
		var ids []string
		q := InboxDoneQuery{Limit: 3, Connectors: connectors}
		for range 10 {
			rows, err := d.DoneInboxItems(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if len(rows) < q.Limit {
				return ids
			}
			last := rows[len(rows)-1]
			q.Before, q.BeforeID = *last.DoneAt, last.ID
		}
		t.Fatalf("no end to the pages: %v", ids)
		return nil
	}
	if got := strings.Join(walk(false), ","); got != "newest,b5,b4,b3,b2,b1,oldest" {
		t.Fatalf("pages = %s", got)
	}
	if got := strings.Join(walk(true), ","); got != "github:1,newest,b5,b4,b3,b2,b1,oldest" {
		t.Fatalf("pages with the connectors = %s", got)
	}
	// A cursor made from an item's own done_at (nanoseconds) lands where the column's (microseconds) does.
	rows, err := d.DoneInboxItems(ctx, InboxDoneQuery{Before: tie, BeforeID: "b3", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "b2,b1,oldest" {
		t.Fatalf("after b3 = %s", got)
	}
}

// The history's pages read inbox_items_open (done_at first) from the cursor down, so no migration adds an index for them.
func TestDoneInboxItemsIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A test table is too small for the planner to prefer any index on its own; rule the scan out to see the choice.
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	for sql, args := range map[string][]any{
		doneInboxItemsSQL:       {false, 50},
		doneInboxItemsBeforeSQL: {false, 50, time.Now(), "x"},
	} {
		rows, err := tx.Query(ctx, `EXPLAIN `+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		t.Logf("%s", plan)
		if !strings.Contains(plan, "inbox_items_open") {
			t.Fatalf("the done items' page does not use inbox_items_open:\n%s", plan)
		}
	}
}
