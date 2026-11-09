package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/managerconf"
)

func TestDatabaseMovedAt(t *testing.T) {
	for _, input := range []string{
		"2025-01-02T03:04:05Z",
		"2025-01-02T17:04:05+14:00",
		"2025-01-01T15:04:05-12:00",
	} {
		t.Run(input, func(t *testing.T) {
			moved, err := time.Parse(time.RFC3339, input)
			if err != nil {
				t.Fatal(err)
			}
			if got := databaseMovedAt(moved); got != "2025-01-02T03:04:05Z" {
				t.Fatalf("databaseMovedAt(%q) = %q; want an explicit UTC instant", input, got)
			}
		})
	}
}

// newDatabase is a database of its own next to the test's, dropped at the end.
func newDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv(dbtest.Env)
	if base == "" {
		t.Skip(dbtest.Env + " not set")
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "persona_owndb_t_" + hex.EncodeToString(b)
	url, err := db.WithDatabase(base, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureDatabase(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if admin, err := pgx.Connect(context.Background(), base); err == nil {
			_, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
			_ = admin.Close(context.Background())
		}
	})
	return url
}

func execIn(t *testing.T, url, q string) {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url+"&application_name=persona-test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err := c.Exec(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func countIn(t *testing.T, url, q string) int {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url+"&application_name=persona-test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var n int
	if err := c.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// The built-in manager's data moves out of the database it shares with the hub into its own once: the next start
// runs there without copying again, the shared database keeps its copy; a provision that fails, or a shared database
// still in use, leaves it on the hub's database for that start, and once it moved it never goes back.
func TestTheBuiltinManagerMovesItsData(t *testing.T) {
	ctx := context.Background()
	shared, own := newDatabase(t), newDatabase(t)
	hub, err := db.Open(ctx, shared, db.AllSets) // the database before the move: both sides' tables
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	execIn(t, shared, `INSERT INTO todos (id, title) VALUES ('t1', '早报')`)
	home := t.TempDir()
	provisions := 0
	var fail error
	provision := func(_ context.Context, _, _ string, kept managerconf.OwnDatabase) (managerconf.OwnDatabase, bool, error) {
		provisions++
		if fail != nil {
			return kept, false, fail
		}
		return managerconf.OwnDatabase{URL: own, Container: "persona-mgr-assistant-pg"}, false, nil
	}
	var said []string
	logf := func(f string, _ ...any) { said = append(said, f) }

	// Docker not up: the hub's database for now.
	fail = errors.New("docker is not running")
	url, onShared, err := builtinDatabase(ctx, home, shared, provision, logf)
	if err != nil || url != shared || !onShared {
		t.Fatalf("no own database yet: %q %v %v", url, onShared, err)
	}
	// Another manager still runs on the shared database: the move waits for a later start.
	fail = nil
	wait := moveWait
	moveWait = 500 * time.Millisecond
	t.Cleanup(func() { moveWait = wait })
	running, err := db.Open(ctx, shared, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	share, err := db.HoldManagerShare(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	url, onShared, err = builtinDatabase(ctx, home, shared, provision, logf)
	if err != nil || url != shared || !onShared || !managerconf.ReadOwnDatabase(home).Moved.IsZero() {
		t.Fatalf("moved while another manager runs: %q %v %v", url, onShared, err)
	}
	share.Release()
	running.Close()

	url, onShared, err = builtinDatabase(ctx, home, shared, provision, logf)
	if err != nil || url != own || onShared {
		t.Fatalf("the move: %q %v %v (%q)", url, onShared, err, said)
	}
	kept := managerconf.ReadOwnDatabase(home)
	if kept.URL != own || kept.Container != "persona-mgr-assistant-pg" || kept.Moved.IsZero() {
		t.Fatalf("manager.json: %+v", kept)
	}
	if n := countIn(t, own, `SELECT count(*) FROM todos WHERE id = 't1'`); n != 1 {
		t.Fatalf("the todo in its own database: %d", n)
	}
	if n := countIn(t, shared, `SELECT count(*) FROM todos WHERE id = 't1'`); n != 1 {
		t.Fatalf("the shared database lost its copy: %d", n)
	}

	// The next start: there, no copy again (a later change in the shared database stays there).
	execIn(t, shared, `INSERT INTO todos (id, title) VALUES ('t2', '旧版本写的')`)
	execIn(t, own, `INSERT INTO todos (id, title) VALUES ('t3', '新的')`)
	url, onShared, err = builtinDatabase(ctx, home, shared, provision, logf)
	if err != nil || url != own || onShared {
		t.Fatalf("the next start: %q %v %v", url, onShared, err)
	}
	if n := countIn(t, own, `SELECT count(*) FROM todos`); n != 2 {
		t.Fatalf("copied again: %d todos", n)
	}
	// Moved, and its database is not up: no start on the hub's stale copy.
	fail = errors.New("docker is not running")
	if url, _, err := builtinDatabase(ctx, home, shared, provision, logf); err == nil {
		t.Fatalf("ran on %q without its own database", url)
	}
}

// Once its data moved, the manager's old tables in the hub's database are dropped only when asked (--yes), and only
// the manager's: the hub's stay.
func TestDropSharedTables(t *testing.T) {
	ctx := context.Background()
	shared, own := newDatabase(t), newDatabase(t)
	hub, err := db.Open(ctx, shared, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	home := t.TempDir()
	var out strings.Builder
	if err := dropShared(ctx, home, shared, true, &out); err == nil {
		t.Fatal("dropped before the move")
	}
	provision := func(context.Context, string, string, managerconf.OwnDatabase) (managerconf.OwnDatabase, bool, error) {
		return managerconf.OwnDatabase{URL: own, Container: "persona-mgr-x-pg"}, false, nil
	}
	if url, _, err := builtinDatabase(ctx, home, shared, provision, t.Logf); err != nil || url != own {
		t.Fatalf("move: %q %v", url, err)
	}
	if err := dropShared(ctx, home, shared, false, &out); err != nil || !strings.Contains(out.String(), "todos") {
		t.Fatalf("list: %v\n%s", err, out.String())
	}
	if n := countIn(t, shared, `SELECT count(*) FROM pg_tables WHERE tablename = 'todos'`); n != 1 {
		t.Fatal("dropped without --yes")
	}
	out.Reset()
	if err := dropShared(ctx, home, shared, true, &out); err != nil {
		t.Fatalf("drop: %v", err)
	}
	for table, want := range map[string]int{"todos": 0, "settings": 0, "hub_settings": 1, "devices": 1, "sent_messages": 1, "manager_moved": 1} {
		if n := countIn(t, shared, `SELECT count(*) FROM pg_tables WHERE tablename = '`+table+`'`); n != want {
			t.Errorf("%s in the hub's database: %d, want %d", table, n, want)
		}
	}
	if n := countIn(t, own, `SELECT count(*) FROM pg_tables WHERE tablename = 'todos'`); n != 1 {
		t.Fatal("its own database lost a table")
	}
	// After the drop, a start that does not know its own database is not taken for a fresh install.
	other := newDatabase(t)
	if url, _, err := builtinDatabase(ctx, t.TempDir(), shared, providing(other, nil), t.Logf); !errors.Is(err, errMovedElsewhere) {
		t.Fatalf("after the drop, a start that does not know its own database ran on %q: %v", url, err)
	}
}

// A fresh install: the hub's database never had the manager's tables, and gets none; the manager starts on its own.
func TestAFreshInstallStartsOnItsOwnDatabase(t *testing.T) {
	ctx := context.Background()
	shared, own := newDatabase(t), newDatabase(t)
	hub, err := db.Open(ctx, shared, db.HubSet)
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	home := t.TempDir()
	provision := func(context.Context, string, string, managerconf.OwnDatabase) (managerconf.OwnDatabase, bool, error) {
		return managerconf.OwnDatabase{URL: own, Container: "persona-mgr-x-pg"}, false, nil
	}
	url, onShared, err := builtinDatabase(ctx, home, shared, provision, t.Logf)
	if err != nil || url != own || onShared || managerconf.ReadOwnDatabase(home).Moved.IsZero() {
		t.Fatalf("%q %v %v", url, onShared, err)
	}
	if n := countIn(t, shared, `SELECT count(*) FROM pg_tables WHERE tablename IN ('todos', 'settings')`); n != 0 {
		t.Fatalf("the manager's tables in the hub's database: %d", n)
	}
}

// Two hubs on one machine never share the manager's container or second database.
func TestOwnDatabaseNames(t *testing.T) {
	t.Setenv("_PERSONA_ASSISTANT_DB_ID", "")
	u, _ := os.UserHomeDir()
	if id := ownContainerID(filepath.Join(u, ".persona")); !strings.HasPrefix(id, "assistant-") {
		t.Errorf("the user's own hub: %s", id)
	}
	a, b := ownContainerID("/srv/hub-a"), ownContainerID("/srv/hub-b")
	if a == b || !strings.HasPrefix(a, "assistant-") {
		t.Errorf("two hubs: %s %s", a, b)
	}
	if n := ownDatabaseName("postgres://u:p@[fd00::20]:5432/jarvis?sslmode=disable"); n != "jarvis_manager" {
		t.Errorf("second database %s", n)
	}
	if n := ownDatabaseName("postgres://u:p@[fd00::20]:5432/" + strings.Repeat("x", 70)); len(n) > 63 {
		t.Errorf("too long: %d", len(n))
	}
}

// A manager.json database_container is used only when it has the persona-mgr-<id>-pg shape a manager makes
// (pgdocker.ManagerContainer).
func TestManagerContainerID(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		ok       bool
	}{
		{"persona-mgr-abc-pg", "abc", true},
		{"persona-mgr-assistant-0a1b2c3d-pg", "assistant-0a1b2c3d", true},
		{"somebody-else-pg", "", false},
		{"persona-mgr--pg", "", false},
		{"persona-mgr-abc", "", false},
		{"persona-mgr-abc-data", "", false},
		{"persona-mgr-pg", "", false},
		{"-pg", "", false},
		{"", "", false},
	} {
		if id, ok := managerContainerID(tc.name); id != tc.id || ok != tc.ok {
			t.Errorf("managerContainerID(%q) = %q, %v; want %q, %v", tc.name, id, ok, tc.id, tc.ok)
		}
	}
}

// A database_container a manager does not make is refused before Docker is asked anything: no container is made or
// started for it. PATH is empty, so a regression finds no docker rather than a real one.
func TestManagerContainerRefusedBeforeDocker(t *testing.T) {
	t.Setenv("PATH", "")
	for _, name := range []string{"somebody-else-pg", "persona-mgr--pg", "persona-mgr-abc"} {
		kept := managerconf.OwnDatabase{Container: name, Location: LocationContainer}
		got, created, err := provisionContainer(context.Background(), t.TempDir(), kept)
		if err == nil || !strings.Contains(err.Error(), "is not one a manager makes") || created || got != kept {
			t.Errorf("%q: %+v %v %v; want it refused and kept as it was", name, got, created, err)
		}
	}
}

// providing is a provisioner that always gives the database at url, as a container (made: created).
func providing(url string, created *bool) provisioner {
	return func(context.Context, string, string, managerconf.OwnDatabase) (managerconf.OwnDatabase, bool, error) {
		c := false
		if created != nil {
			c = *created
		}
		return managerconf.OwnDatabase{URL: url, Container: "persona-mgr-x-pg"}, c, nil
	}
}

// A start whose manager.json does not know of the move (another data directory, a restored or reset one) finds its own
// database filled and runs there: it never copies the stale shared data over what was written since. One whose own
// database is another, unknown one does not start on the stale copy either.
func TestAStartThatDoesNotKnowTheMove(t *testing.T) {
	ctx := context.Background()
	shared, own := newDatabase(t), newDatabase(t)
	hub, err := db.Open(ctx, shared, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	execIn(t, shared, `INSERT INTO todos (id, title) VALUES ('t1', 'before')`)
	first := t.TempDir()
	if url, _, err := builtinDatabase(ctx, first, shared, providing(own, nil), t.Logf); err != nil || url != own {
		t.Fatalf("the move: %q %v", url, err)
	}
	execIn(t, own, `INSERT INTO todos (id, title) VALUES ('t2', 'after the move')`)
	second := t.TempDir() // no marker
	url, onShared, err := builtinDatabase(ctx, second, shared, providing(own, nil), t.Logf)
	if err != nil || url != own || onShared {
		t.Fatalf("a start without the marker: %q %v %v", url, onShared, err)
	}
	if n := countIn(t, own, `SELECT count(*) FROM todos WHERE id = 't2'`); n != 1 {
		t.Fatal("what was written after the move is gone")
	}
	if managerconf.ReadOwnDatabase(second).Moved.IsZero() {
		t.Fatal("its manager.json does not know now")
	}
	// Its own database another, empty one: the data left the hub's for one it does not know.
	other := newDatabase(t)
	if url, _, err := builtinDatabase(ctx, t.TempDir(), shared, providing(other, nil), t.Logf); !errors.Is(err, errMovedElsewhere) {
		t.Fatalf("started on %q: %v", url, err)
	}
}

// Two starts at once: one copies, the other waits for it and runs on what it copied.
func TestTwoStartsAtOnce(t *testing.T) {
	ctx := context.Background()
	shared, own := newDatabase(t), newDatabase(t)
	hub, err := db.Open(ctx, shared, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	execIn(t, shared, `INSERT INTO todos (id, title) VALUES ('t1', 'before')`)
	type result struct {
		url    string
		shared bool
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		home := t.TempDir()
		go func() {
			url, onShared, err := builtinDatabase(ctx, home, shared, providing(own, nil), t.Logf)
			results <- result{url, onShared, err}
		}()
	}
	for range 2 {
		r := <-results
		if r.err != nil || r.url != own || r.shared {
			t.Fatalf("a start: %+v", r)
		}
	}
	if n := countIn(t, own, `SELECT count(*) FROM todos`); n != 1 {
		t.Fatalf("todos %d", n)
	}
}

// Its own database lost after the move (made again, empty): it does not start, rather than run on nothing; one made
// again over its kept data starts.
func TestALostOwnDatabase(t *testing.T) {
	ctx := context.Background()
	shared, own := newDatabase(t), newDatabase(t)
	hub, err := db.Open(ctx, shared, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	home := t.TempDir()
	if _, _, err := builtinDatabase(ctx, home, shared, providing(own, nil), t.Logf); err != nil {
		t.Fatal(err)
	}
	// Made again over what is there (a container removed, its volume kept): its data, its record: it starts.
	created := true
	if url, _, err := builtinDatabase(ctx, home, shared, providing(own, &created), t.Logf); err != nil || url != own {
		t.Fatalf("made again over its data: %q %v", url, err)
	}
	empty := newDatabase(t) // lost: made again empty
	created = true
	if url, _, err := builtinDatabase(ctx, home, shared, providing(empty, &created), t.Logf); err == nil {
		t.Fatalf("started on %q, without its data", url)
	}
}

// Its manager.json cannot keep where its own database is: nothing goes there, it runs on the hub's database for that
// start (a database it made is found again at the next).
func TestNoMoveWithoutKeepingWhere(t *testing.T) {
	ctx := context.Background()
	shared, own := newDatabase(t), newDatabase(t)
	hub, err := db.Open(ctx, shared, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "managers"), nil, 0o600); err != nil { // no directory can be made there
		t.Fatal(err)
	}
	url, onShared, err := builtinDatabase(ctx, home, shared, providing(own, nil), t.Logf)
	if err != nil || url != shared || !onShared {
		t.Fatalf("%q %v %v", url, onShared, err)
	}
	if n := countIn(t, own, `SELECT count(*) FROM pg_tables WHERE tablename = 'manager_moved'`); n != 0 {
		if m := countIn(t, own, `SELECT count(*) FROM manager_moved`); m != 0 {
			t.Fatal("copied without keeping where")
		}
	}
}
