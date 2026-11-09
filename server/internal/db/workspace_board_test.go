package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nwverse/persona/server/internal/workspaceboard"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func openWorkspaceBoard(t testing.TB) *DB {
	t.Helper()
	d := open(t)
	if _, err := d.pool.Exec(context.Background(), `TRUNCATE workspace_board, workspace_snapshots`); err != nil {
		t.Fatal(err)
	}
	return d
}
func TestWorkspaceBoardPersistenceConflictAndRestart(t *testing.T) {
	d := openWorkspaceBoard(t)
	ctx := context.Background()
	initial, err := d.WorkspaceBoard(ctx)
	if err != nil || !reflect.DeepEqual(initial, workspaceboard.Default()) {
		t.Fatalf("default: %+v %v", initial, err)
	}
	title := "Project"
	next, err := workspaceboard.Apply(initial, workspaceboard.Mutation{Action: "add_card", ID: "card-a", Title: &title, ColumnID: "todo", MachineID: "machine-a", WorkspaceID: "workspace-a", Cwd: "/projects/a", Source: "directory"})
	if err != nil {
		t.Fatal(err)
	}
	next.Revision = 999
	saved, err := d.SaveWorkspaceBoard(ctx, 0, next)
	if err != nil || saved.Revision != 1 || len(saved.Cards) != 1 {
		t.Fatalf("save: %+v %v", saved, err)
	}
	next.Cards[0].Title = "caller mutation"
	if saved.Cards[0].Title != "Project" {
		t.Fatal("save aliases input")
	}
	if _, err := d.SaveWorkspaceBoard(ctx, 0, initial); !errors.Is(err, ErrWorkspaceBoardConflict) {
		t.Fatalf("stale: %v", err)
	}
	invalid := saved
	invalid.Columns = nil
	if _, err := d.SaveWorkspaceBoard(ctx, 1, invalid); !errors.Is(err, workspaceboard.ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	reopened, err := Open(ctx, os.Getenv("PERSONA_TEST_DATABASE_URL"), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.WorkspaceBoard(ctx)
	if err != nil || !reflect.DeepEqual(saved, reloaded) {
		t.Fatalf("restart: %+v %v", reloaded, err)
	}
	if err := reopened.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := reopened.WorkspaceBoard(ctx)
	if err != nil || !reflect.DeepEqual(saved, again) {
		t.Fatalf("reseeded: %+v %v", again, err)
	}
}
func TestWorkspaceBoardConcurrentSeedAndCAS(t *testing.T) {
	d := openWorkspaceBoard(t)
	ctx := context.Background()
	const writers = 16
	var wg sync.WaitGroup
	failures := make(chan error, writers)
	for range writers {
		wg.Go(func() {
			s, err := d.WorkspaceBoard(ctx)
			if err != nil {
				failures <- err
			} else if !reflect.DeepEqual(s, workspaceboard.Default()) {
				failures <- errors.New("inconsistent initial state")
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	results := make(chan error, writers)
	start := make(chan struct{})
	for range writers {
		wg.Go(func() { <-start; _, err := d.SaveWorkspaceBoard(ctx, 0, workspaceboard.Default()); results <- err })
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrWorkspaceBoardConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != writers-1 {
		t.Fatalf("lost update: %d success %d conflict", success, conflicts)
	}
	s, err := d.WorkspaceBoard(ctx)
	if err != nil || s.Revision != 1 {
		t.Fatalf("revision: %d %v", s.Revision, err)
	}
}
func TestWorkspaceSnapshotsMachineScopedAndIndependent(t *testing.T) {
	d := openWorkspaceBoard(t)
	ctx := context.Background()
	for machine, data := range map[string]json.RawMessage{"machine-b": json.RawMessage(`{"workspaces":[]}`), "machine-a": json.RawMessage(`{"workspaces":[{"id":"old"}]}`)} {
		if err := d.SaveWorkspaceSnapshot(ctx, machine, data); err != nil {
			t.Fatal(err)
		}
	}
	replacement := json.RawMessage(`{"workspaces":[{"id":"new"}]}`)
	if err := d.SaveWorkspaceSnapshot(ctx, "machine-a", replacement); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []json.RawMessage{nil, json.RawMessage(`{broken`)} {
		if err := d.SaveWorkspaceSnapshot(ctx, "machine-a", bad); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	if err := d.SaveWorkspaceSnapshot(ctx, "", replacement); err == nil {
		t.Fatal("empty machine accepted")
	}
	rows, err := d.ListWorkspaceSnapshots(ctx)
	if err != nil || len(rows) != 2 || rows[0].MachineID != "machine-a" || rows[1].MachineID != "machine-b" || rows[0].UpdatedAt.IsZero() {
		t.Fatalf("snapshots: %+v %v", rows, err)
	}
	var got, want any
	if err := json.Unmarshal(rows[0].Data, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(replacement, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement: %s", rows[0].Data)
	}
	state, err := d.WorkspaceBoard(ctx)
	if err != nil || state.Revision != 0 || len(state.Cards) != 0 {
		t.Fatalf("discovery mutated board: %+v %v", state, err)
	}
}

func TestDecodeWorkspaceBoardUsesSQLRevisionAndRejectsCorruption(t *testing.T) {
	raw, err := json.Marshal(workspaceboard.Default())
	if err != nil {
		t.Fatal(err)
	}
	s, err := decodeWorkspaceBoard(8, raw)
	if err != nil || s.Revision != 8 || s.Cards == nil {
		t.Fatalf("decode: %+v %v", s, err)
	}
	for _, raw := range [][]byte{[]byte(`{broken`), []byte(`{"columns":[],"cards":[]}`), []byte(`null`)} {
		if _, err := decodeWorkspaceBoard(1, raw); err == nil {
			t.Fatalf("accepted corrupt persisted state: %s", raw)
		}
	}
}

func TestWorkspaceBoardInUseIgnoresTheSeededBoard(t *testing.T) {
	d := openWorkspaceBoard(t)
	ctx := context.Background()
	if used, err := d.WorkspaceBoardInUse(ctx); err != nil || used {
		t.Fatalf("no board: %v %v", used, err)
	}
	initial, err := d.WorkspaceBoard(ctx) // opening the page seeds revision 0
	if err != nil {
		t.Fatal(err)
	}
	if used, err := d.WorkspaceBoardInUse(ctx); err != nil || used {
		t.Fatalf("seeded board: %v %v", used, err)
	}
	if _, err := d.SaveWorkspaceBoard(ctx, initial.Revision, initial); err != nil {
		t.Fatal(err)
	}
	if used, err := d.WorkspaceBoardInUse(ctx); err != nil || !used {
		t.Fatalf("changed board: %v %v", used, err)
	}
}

// statementLog records the first word of every statement a pool sends, to count a read's round trips.
type statementLog struct {
	mu  sync.Mutex
	sql []string
}

func (l *statementLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if words := strings.Fields(data.SQL); len(words) > 0 {
		l.mu.Lock()
		l.sql = append(l.sql, words[0])
		l.mu.Unlock()
	}
	return ctx
}

func (*statementLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (l *statementLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.sql
	l.sql = nil
	return out
}

// tracedWorkspaceBoard is a second handle on the test database whose pool logs the statements it sends.
func tracedWorkspaceBoard(t *testing.T) (*DB, *statementLog) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("PERSONA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	log := &statementLog{}
	cfg.ConnConfig.Tracer = log
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &DB{pool: pool}, log
}

// The first read seeds the default board; later reads are one SELECT and leave the row alone (the web board polls
// every 10 s).
func TestWorkspaceBoardSeedsOnlyAMissingRow(t *testing.T) {
	d := openWorkspaceBoard(t)
	traced, log := tracedWorkspaceBoard(t)
	ctx := context.Background()
	first, err := traced.WorkspaceBoard(ctx)
	if err != nil || !reflect.DeepEqual(first, workspaceboard.Default()) {
		t.Fatalf("first read: %+v %v", first, err)
	}
	if got := log.take(); !slices.Equal(got, []string{"SELECT", "INSERT", "SELECT"}) {
		t.Fatalf("first read statements: %v", got)
	}
	var revision int64
	var seededAt time.Time
	if err := d.pool.QueryRow(ctx, `SELECT revision, updated_at FROM workspace_board WHERE id = true`).Scan(&revision, &seededAt); err != nil || revision != 0 {
		t.Fatalf("seeded row: revision %d %v", revision, err)
	}
	for range 2 {
		again, err := traced.WorkspaceBoard(ctx)
		if err != nil || !reflect.DeepEqual(again, first) {
			t.Fatalf("later read: %+v %v", again, err)
		}
		if got := log.take(); !slices.Equal(got, []string{"SELECT"}) {
			t.Fatalf("later read statements: %v", got)
		}
	}
	var updatedAt time.Time
	if err := d.pool.QueryRow(ctx, `SELECT updated_at FROM workspace_board WHERE id = true`).Scan(&updatedAt); err != nil || !updatedAt.Equal(seededAt) {
		t.Fatalf("read rewrote the row: %s -> %s %v", seededAt, updatedAt, err)
	}
}

// An edited board comes back as stored: a read never reseeds or overwrites it.
func TestWorkspaceBoardReadKeepsAnEditedBoard(t *testing.T) {
	d := openWorkspaceBoard(t)
	ctx := context.Background()
	initial, err := d.WorkspaceBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	title := "Edited"
	next, err := workspaceboard.Apply(initial, workspaceboard.Mutation{Action: "add_card", ID: "card-a", Title: &title, ColumnID: "doing", MachineID: "machine-a", WorkspaceID: "workspace-a", Cwd: "/projects/a", Source: "directory"})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := d.SaveWorkspaceBoard(ctx, initial.Revision, next)
	if err != nil || saved.Revision != 1 {
		t.Fatalf("save: %+v %v", saved, err)
	}
	traced, log := tracedWorkspaceBoard(t)
	for range 2 {
		got, err := traced.WorkspaceBoard(ctx)
		if err != nil || !reflect.DeepEqual(got, saved) {
			t.Fatalf("edited board: %+v %v", got, err)
		}
		if got := log.take(); !slices.Equal(got, []string{"SELECT"}) {
			t.Fatalf("edited board statements: %v", got)
		}
	}
}

// BenchmarkWorkspaceBoardRead reads an existing board, which every open web board does twice every 10 s (the board and
// the workspace list).
func BenchmarkWorkspaceBoardRead(b *testing.B) {
	d := openWorkspaceBoard(b)
	ctx := context.Background()
	if _, err := d.WorkspaceBoard(ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := d.WorkspaceBoard(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
