package db

import (
	"context"
	"io/fs"
	"testing"
	"time"
)

// 0453 takes the origin off the to-dos questions nobody answered became (#1852, #1909): they are no tasks. A task of a
// conversation keeps its origin, and so does a to-do whose id only looks alike. Applied again on top of a migrated
// database, as it would meet stored to-dos.
func TestAskTodoOriginCleared(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, freshDatabase(t), ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, r := range []TodoRow{
		{ID: "todo_ask_0a1b2c", Title: "Merge the release PR now?", Origin: "c1", CreatedAt: now, UpdatedAt: now},
		{ID: "todo_0a1b2c3d", Title: "Fix the widget", Origin: "c1", CreatedAt: now, UpdatedAt: now},
		{ID: "todo_asked", Title: "Ask about the rollout", Origin: "c1", CreatedAt: now, UpdatedAt: now},
		{ID: "todo_ask_9f8e7d", Title: "Which branch?", CreatedAt: now, UpdatedAt: now},
	} {
		if err := d.CreateTodo(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	sql, err := fs.ReadFile(migrations, "migrations/manager/0453_ask_todo_no_origin.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"todo_ask_0a1b2c": "", "todo_0a1b2c3d": "c1", "todo_asked": "c1", "todo_ask_9f8e7d": ""} {
		r, err := d.GetTodo(ctx, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if r.Origin != want {
			t.Errorf("%s: origin %q, want %q", id, r.Origin, want)
		}
	}
	if tag, err := d.pool.Exec(ctx, string(sql)); err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("second run: %v rows, %v", tag.RowsAffected(), err)
	}
}
