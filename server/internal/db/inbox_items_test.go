package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

// inboxData is what InboxItems hands over, each row copied (raw is the driver's buffer, valid only during the call).
func inboxData(d *DB, doneSince time.Time) ([][]byte, error) {
	var out [][]byte
	err := d.InboxItems(context.Background(), doneSince, func(raw []byte) error {
		out = append(out, bytes.Clone(raw))
		return nil
	})
	return out, err
}

// InboxItems stops at an error from each and returns it, and gives its connection back to the pool: with one
// connection, the next query still gets it.
func TestInboxItemsStopsOnError(t *testing.T) {
	d, ctx := open(t), context.Background()
	if err := d.PutInboxItems(ctx, inboxRows(3, time.Now())); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("PERSONA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	one, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop, calls := errors.New("stop"), 0
	err = (&DB{pool: one}).InboxItems(ctx, time.Now().Add(-time.Hour), func([]byte) error { calls++; return stop })
	if st := one.Stat(); st.AcquiredConns() != 0 || st.IdleConns() != 1 {
		// one is left open: Close would wait for the connection still held.
		t.Fatalf("after the read stopped: %d connections held, %d idle; want it back in the pool", st.AcquiredConns(), st.IdleConns())
	}
	defer one.Close()
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("InboxItems = %v after %d rows, want the callback's error after the first", err, calls)
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var n int
	if err := one.QueryRow(qctx, `SELECT count(*) FROM inbox_items`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("the next query on the one connection: %d rows, %v", n, err)
	}
}

// InboxItems hands each row over from the driver's buffer, copying none: a read of 200 rows allocates no more than
// the query itself (6 times; collecting the rows as [][]byte took 616).
func TestInboxItemsAllocationBudget(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now()
	if err := d.PutInboxItems(ctx, inboxRows(200, now)); err != nil {
		t.Fatal(err)
	}
	var rows int
	read := func() {
		rows = 0
		if err := d.InboxItems(ctx, now.Add(-time.Hour), func([]byte) error { rows++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	read() // the statement is prepared and cached on the first read
	if n := testing.AllocsPerRun(20, read); n > 6 || rows != 200 {
		t.Fatalf("a read of %d rows allocates %v times, want at most 6", rows, n)
	}
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
	got, err := inboxData(d, now.Add(-time.Hour))
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
	if got, _ := inboxData(d, now.Add(-time.Hour)); len(got) != 3 {
		t.Fatalf("rows after a failed batch = %d, want the batch stored whole or not at all", len(got))
	}
	if err := d.PutInboxItems(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

// An item with \u0000 in a string (json.Marshal writes a NUL so) is refused, as jsonb refused it, and nothing is
// stored. json alone would take it; computing source parses the whole document and refuses it (0471). Stored, such a
// row would make the done page's data->'connector' fail for as long as it is kept.
func TestInboxItemWithNULRefused(t *testing.T) {
	d, ctx := open(t), context.Background()
	for name, data := range map[string]string{
		"a string before source":       `{"id":"nul","title":"a\u0000b","source":"mail"}`,
		"a nested string after source": `{"id":"nul","source":"mail","chain":[{"text":"a\u0000b"}]}`,
		"a key":                        `{"id":"nul","source":"mail","a\u0000b":1}`,
	} {
		err := d.PutInboxItem(ctx, "nul", []byte(data), time.Now(), nil)
		if pe := (*pgconn.PgError)(nil); !errors.As(err, &pe) || pe.Code != "22P05" {
			t.Fatalf("%s: %v, want untranslatable_character (22P05)", name, err)
		}
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows stored (%v), want none", n, err)
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
	// The open ones and those done since, in no particular order.
	rows, err := inboxData(d, now.Add(-8*day))
	slices.SortFunc(rows, bytes.Compare)
	if err != nil || len(rows) != 2 || string(rows[0]) != `{"id":"d1"}` || string(rows[1]) != `{"id":"open"}` {
		t.Fatalf("loaded %q (%v), want the open one and the one done a day ago", rows, err)
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
	// One not loaded is still read by its id (a detail, a reopen); an open one or none is not. The bytes come back as
	// written (json, 0471).
	if got, err := d.DoneInboxItemsByID(ctx, []string{"d10", "open", "none"}); err != nil || len(got) != 1 || string(got[0]) != `{"id":"d10"}` {
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

// The done items counted per calendar day in the zone asked for (#2967): Postgres's AT TIME ZONE on the timestamptz
// column, so a day follows the zone's own offset on each date. New York leaves summer time on 1 Nov 2026 at 06:00 UTC:
// that day 03:30 UTC is still 31 Oct there and 04:30 is 1 Nov 0:30 (UTC-4); on 2 Nov, 04:30 UTC is 1 Nov 23:30 (UTC-5).
// Either fixed offset puts one of them on the wrong day. Open items never count, connectors' only when asked for.
func TestDoneInboxDays(t *testing.T) {
	d, ctx := open(t), context.Background()
	put := func(id string, done *time.Time, data string) {
		t.Helper()
		if data == "" {
			data = `{"id":"` + id + `"}`
		}
		if err := d.PutInboxItem(ctx, id, []byte(data), time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), done); err != nil {
			t.Fatal(err)
		}
	}
	at := func(day, hour, min int) *time.Time {
		t := time.Date(2026, 11, day, hour, min, 0, 0, time.UTC)
		return &t
	}
	put("a", at(1, 3, 30), "")
	put("b", at(1, 4, 30), "")
	put("c", at(2, 4, 30), "")
	put("d", at(2, 16, 0), "")
	put("open", nil, "")
	put("github:1", at(2, 17, 0), `{"id":"github:1","source":"github","connector":{"thread":"t"}}`)
	days := func(zone string, connectors bool) string {
		t.Helper()
		got, err := d.DoneInboxDays(ctx, zone, connectors)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range got {
			out = append(out, fmt.Sprintf("%s:%d", x.Day, x.Count))
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		zone       string
		connectors bool
		want       string
	}{
		{"UTC", false, "2026-11-02:2,2026-11-01:2"},
		{"UTC", true, "2026-11-02:3,2026-11-01:2"},
		{"America/New_York", false, "2026-11-02:1,2026-11-01:2,2026-10-31:1"},
		{"Asia/Shanghai", true, "2026-11-03:2,2026-11-02:1,2026-11-01:2"},
	} {
		if got := days(c.zone, c.connectors); got != c.want {
			t.Errorf("%s (connectors %v): %s, want %s", c.zone, c.connectors, got, c.want)
		}
	}
}

// The history's pages read inbox_items_open, (done_at, id) since 0471, from the cursor down in the pages' order, with no
// sort; deleting a source's rows reads inbox_items_source, not every document. No index reads data or updated_at,
// which every upsert changes, so an upsert of an open item can be HOT.
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
	for sql, c := range map[string]struct {
		args  []any
		index string
	}{
		doneInboxItemsSQL:       {[]any{false, 50}, "inbox_items_open"},
		doneInboxItemsBeforeSQL: {[]any{false, 50, time.Now(), "x"}, "inbox_items_open"},
		deleteInboxSourceSQL:    {[]any{[]string{"x"}, "github"}, "inbox_items_source"},
	} {
		rows, err := tx.Query(ctx, `EXPLAIN `+sql, c.args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		t.Logf("%s", plan)
		if !strings.Contains(plan, c.index) || strings.Contains(plan, "Sort") {
			t.Fatalf("not read through %s in order:\n%s", c.index, plan)
		}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT coalesce(a.attname, 'an expression') FROM pg_index i
		CROSS JOIN unnest(i.indkey::int2[]) k LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k
		WHERE i.indrelid = 'inbox_items'::regclass
		UNION SELECT 'a predicate' FROM pg_index WHERE indrelid = 'inbox_items'::regclass AND indpred IS NOT NULL
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if got := strings.Join(cols, ","); err != nil || got != "done_at,id,source" {
		t.Fatalf("inbox_items indexes read %s (%v), want only done_at, id and source", got, err)
	}
}

// doneInboxItemsSQL as a build from before 0471 runs it (#2447): jsonb_typeof on data, which is json now.
const doneInboxItemsSQLBefore0471 = `SELECT id, data, updated_at, done_at FROM inbox_items
	WHERE done_at IS NOT NULL AND ($1 OR jsonb_typeof(data->'connector') IS DISTINCT FROM 'object')
	ORDER BY done_at DESC, id DESC LIMIT $2`

// 0471 turns the rows a database already has into json and fills their source, and the done page stays the same page:
// by the query of a build from before it (going back a version, docs/operations/hub-restart.md) and by today's.
func TestInboxJSONMigrationKeepsStoredItems(t *testing.T) {
	ctx := t.Context()
	d, err := Attach(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations/manager")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if v, err := migrationVersion(e.Name()); err != nil || v >= 471 {
			continue
		}
		b, err := fs.ReadFile(migrations, "migrations/manager/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		before["migrations/manager/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := d.migrateFrom(ctx, before); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for _, it := range []struct {
		id, data string
		done     time.Duration // after at
		open     bool
	}{
		{id: "mail:1", data: `{"source":"mail","id":"mail:1"}`},
		{id: "mail:2", data: `{"id":"mail:2","source":"mail"}`},
		{id: "github:1", data: `{"id":"github:1","source":"github","connector":{"thread":"t"}}`, done: time.Hour},
		{id: "github:2", data: `{"id":"github:2","source":"github","connector":null}`, done: -time.Hour},
		{id: "lark:1", data: `{"id":"lark:1","source":"lark"}`, done: -2 * time.Hour},
		{id: "lark:2", data: `{"id":"lark:2","source":"lark","context":"` + strings.Repeat("the thread so far ", 300) + `"}`, open: true},
	} {
		var doneAt *time.Time
		if !it.open {
			done := at.Add(it.done)
			doneAt = &done
		}
		if err := d.PutInboxItem(ctx, it.id, []byte(it.data), at, doneAt); err != nil {
			t.Fatal(err)
		}
	}
	page := func(sql string) string {
		t.Helper()
		// Planned each time: a statement prepared before the migration would fail once, then be prepared again.
		rows, err := d.pool.Query(ctx, sql, pgx.QueryExecModeExec, false, 50)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
			var r InboxRow
			err := row.Scan(&r.ID, &r.Data, &r.UpdatedAt, &r.DoneAt)
			return r.ID, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(ids, ",")
	}
	const want = "mail:2,mail:1,github:2,lark:1" // a connector's item left out, ties by id
	if got := page(doneInboxItemsSQLBefore0471); got != want {
		t.Fatalf("page before the migration = %s, want %s", got, want)
	}

	if err := d.migrateFrom(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	// json, and a document stored compressed before is compressed again with lz4 (the same ALTER: copied as it is it
	// would stay pglz).
	var typ, compression string
	if err := d.pool.QueryRow(ctx, `SELECT pg_typeof(data)::text, pg_column_compression(data) FROM inbox_items WHERE id = 'lark:2'`).
		Scan(&typ, &compression); err != nil || typ != "json" || compression != "lz4" {
		t.Fatalf("data is %q compressed %q (%v), want json and lz4", typ, compression, err)
	}
	for _, sql := range []string{doneInboxItemsSQLBefore0471, doneInboxItemsSQL} {
		if got := page(sql); got != want {
			t.Fatalf("page after the migration = %s, want %s:\n%s", got, want, sql)
		}
	}
	if gone, err := d.DeleteInboxSource(ctx, "github", nil); err != nil || strings.Join(slices.Sorted(slices.Values(gone)), ",") != "github:1,github:2" {
		t.Fatalf("deleted %v (%v), want the github items stored before the migration", gone, err)
	}
}
