package db

import (
	"context"
	"testing"
	"time"
)

// Indexing an entity again with the same values leaves its row alone (no new row version: same xmin, so no WAL, no
// transaction id and no dead tuple), and a changed value still updates it.
func TestPutKBEntityUnchangedIsNotRewritten(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC()
	xmin := func(kind, id string) string {
		t.Helper()
		var x string
		if err := d.pool.QueryRow(ctx, `SELECT xmin::text FROM kb_entities WHERE kind = $1 AND id = $2`, kind, id).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	put := func(kind, id, name string, tags []string, updated time.Time) {
		t.Helper()
		if err := d.PutKBEntity(ctx, kind, id, name, "P1", 40, tags, "one line", updated); err != nil {
			t.Fatal(err)
		}
	}
	// With tags and a time, and with neither (an empty array and NULL compare as equal too).
	put("projects", "a", "A", []string{"release", "backend"}, now)
	put("chats", "c", "C", nil, time.Time{})
	a, c := xmin("projects", "a"), xmin("chats", "c")
	put("projects", "a", "A", []string{"release", "backend"}, now)
	put("chats", "c", "C", nil, time.Time{})
	if xmin("projects", "a") != a || xmin("chats", "c") != c {
		t.Fatal("an unchanged entity was written again")
	}

	put("projects", "a", "A", []string{"backend", "release"}, now) // the tags' order is part of the value
	if xmin("projects", "a") == a {
		t.Fatal("changed tags were not written")
	}
	put("chats", "c", "C", nil, now)
	list, err := d.KBEntities(ctx, "", "", 10)
	if err != nil || len(list) != 2 || xmin("chats", "c") == c {
		t.Fatalf("after the changes: %+v, %v", list, err)
	}
	for _, e := range list {
		if e.UpdatedAt == nil || !e.UpdatedAt.Equal(now.Truncate(time.Microsecond)) || (e.ID == "a" && e.Tags[0] != "backend") {
			t.Fatalf("stored %+v", e)
		}
	}
}
