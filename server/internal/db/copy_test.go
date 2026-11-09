package db

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The manager's tables copy from a shared database into its own as they are: rows (a parent before what refers to
// it), settings, values of every kind; sequences go on past them; a second copy replaces the first; the source is
// left as it was.
func TestCopyTables(t *testing.T) {
	ctx := context.Background()
	from, err := Open(ctx, freshDatabase(t), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer from.Close()
	to, err := Open(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer to.Close()
	for _, q := range []string{
		`INSERT INTO todos (id, title, notes) VALUES ('t1', '早报', 'a line
and a tab	and a \ backslash')`,
		`UPDATE todos SET done_at = now() WHERE id = 't1'`,
		`UPDATE todos SET done_at = NULL WHERE id = 't1'`,
		`INSERT INTO todo_agents (todo_id, machine_id, session_id) VALUES ('t1', 'm', 's')`,
		`INSERT INTO settings (key, value) VALUES ('assistant', '{"main":"m-1","list":[1,2]}')`,
	} {
		if _, err := from.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := from.OutboxAdd(ctx, OutboxItem{ID: "ntf_1", Kind: "push", Body: []byte{0, 1, 2, 255}, CreatedAt: time.Now()}, 10, time.Time{}); err != nil {
		t.Fatal(err)
	}
	rep, err := CopyTables(ctx, from, to, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows["todos"] != 1 || rep.Rows["todo_agents"] != 1 || rep.Rows["settings"] != 1 || rep.Rows["manager_outbox"] != 1 {
		t.Fatalf("copied %v", rep.Rows)
	}
	if _, ok, err := to.MovedIn(ctx); err != nil || !ok {
		t.Fatalf("the copy is not recorded with the data: %v %v", ok, err)
	}
	if _, ok, _ := from.MovedIn(ctx); ok {
		t.Fatal("recorded in the source")
	}
	// Never a second copy over the first (its data may have changed since).
	if _, err := CopyTables(ctx, from, to, t.Logf); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("copied over the first copy: %v", err)
	}
	var notes, value string
	if err := to.pool.QueryRow(ctx, `SELECT notes FROM todos WHERE id = 't1'`).Scan(&notes); err != nil || notes != "a line\nand a tab\tand a \\ backslash" {
		t.Fatalf("notes %q %v", notes, err)
	}
	if row, err := to.GetTodo(ctx, "t1"); err != nil || row.ReopenGeneration != 1 {
		t.Fatalf("copied reopen generation: %+v %v", row, err)
	}
	closed, opened := true, false
	if _, err := to.UpdateTodo(ctx, "t1", nil, nil, &closed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if row, err := to.UpdateTodo(ctx, "t1", nil, nil, &opened, time.Now()); err != nil || row.ReopenGeneration != 2 {
		t.Fatalf("reopen after copy: %+v %v", row, err)
	}
	if err := to.pool.QueryRow(ctx, `SELECT value::text FROM settings WHERE key = 'assistant'`).Scan(&value); err != nil || value != `{"list": [1, 2], "main": "m-1"}` {
		t.Fatalf("settings %q %v", value, err)
	}
	var n int
	items, err := to.OutboxItems(ctx, 10)
	if err != nil || len(items) != 1 || string(items[0].Body) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("outbox %+v %v", items, err)
	}
	// Its sequence goes on past what was copied.
	if err := to.OutboxAdd(ctx, OutboxItem{ID: "ntf_2", Kind: "push", Body: []byte(`{}`), CreatedAt: time.Now()}, 10, time.Time{}); err != nil {
		t.Fatalf("a new row after the copy: %v", err)
	}
	if items, _ := to.OutboxItems(ctx, 10); len(items) != 2 || items[1].Seq <= items[0].Seq {
		t.Fatalf("after the copy: %+v", items)
	}
	if err := from.pool.QueryRow(ctx, `SELECT count(*) FROM todos`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the source changed: %d %v", n, err)
	}
}

// The move waits for the managers running on the shared database (the lock's share) and for connections of a build
// from before (no application_name), and gives up after its wait.
func TestLockManagerMove(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	running, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	mover, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer mover.Close()
	share, err := HoldManagerShare(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err := share.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := mover.LockManagerMove(ctx, 700*time.Millisecond); !errors.Is(err, ErrManagerBusy) {
		t.Fatalf("moved while a manager runs: %v", err)
	}
	share.Release()
	unlock, err := mover.LockManagerMove(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("after it stopped: %v", err)
	}
	unlock()
	// A connection that gives no application_name (a build from before) holds it back too.
	old, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mover.LockManagerMove(ctx, 700*time.Millisecond); !errors.Is(err, ErrManagerBusy) {
		t.Fatalf("moved while an older build is connected: %v", err)
	}
	_ = old.Close(ctx)
	unlock, err = mover.LockManagerMove(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("after the older build left: %v", err)
	}
	unlock()
}

// A copy into data that is there already, or into the source itself, is refused before it touches anything.
func TestCopyTablesRefuses(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	from, err := Open(ctx, url, AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer from.Close()
	same, err := Open(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer same.Close()
	if _, err := CopyTables(ctx, from, same, t.Logf); !errors.Is(err, ErrSameDatabase) {
		t.Fatalf("into itself: %v", err)
	}
	to, err := Open(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer to.Close()
	if _, err := to.pool.Exec(ctx, `INSERT INTO todos (id, title) VALUES ('live', 'written there')`); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyTables(ctx, from, to, t.Logf); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("over data there: %v", err)
	}
	var n int
	if err := to.pool.QueryRow(ctx, `SELECT count(*) FROM todos WHERE id = 'live'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the data there: %d %v", n, err)
	}
}

// Dates and times mean the same on both sides whatever each server's DateStyle is.
func TestCopyTablesKeepsDates(t *testing.T) {
	ctx := context.Background()
	src, dst := freshDatabase(t), freshDatabase(t)
	for url, style := range map[string]string{src: "SQL, DMY", dst: "SQL, MDY"} {
		c, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{DatabaseName(url)}.Sanitize()+` SET DateStyle = '`+style+`'`); err != nil {
			t.Fatal(err)
		}
		_ = c.Close(ctx)
	}
	from, err := Open(ctx, src, AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer from.Close()
	to, err := Open(ctx, dst, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer to.Close()
	if _, err := from.pool.Exec(ctx, `INSERT INTO todos (id, title, created_at) VALUES ('t1', 'x', '2026-03-11 10:00:00+00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyTables(ctx, from, to, t.Logf); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	if err := to.pool.QueryRow(ctx, `SELECT created_at FROM todos WHERE id = 't1'`).Scan(&at); err != nil || at.UTC().Month() != time.March || at.UTC().Day() != 11 {
		t.Fatalf("the date: %v %v", at, err)
	}
}

// HubTables are exactly the tables a database the hub's set alone was migrated with has.
func TestHubTablesAreTheHubSets(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, freshDatabase(t), HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := d.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := HubTables()
	for _, tbl := range got {
		if !slices.Contains(want, tbl) {
			t.Errorf("%s: in a hub-set database, not in HubTables", tbl)
		}
	}
	m, err := Open(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	mine, err := m.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range mine {
		if !slices.Contains(ManagerTables(), tbl) {
			t.Errorf("%s: in a manager-set database, not in ManagerTables", tbl)
		}
	}
}
