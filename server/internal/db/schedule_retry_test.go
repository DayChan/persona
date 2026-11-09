package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func scheduleDBs(t *testing.T) (*DB, *DB) {
	t.Helper()
	d := open(t)
	other, err := Open(context.Background(), os.Getenv("PERSONA_TEST_DATABASE_URL"), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	return d, other
}

func TestOriginsRawCompareKeepsLegacyMap(t *testing.T) {
	d, other := scheduleDBs(t)
	ctx := context.Background()
	raw, rev, found, err := d.ReadOrigins(ctx)
	if err != nil || found || rev != 0 || raw != nil {
		t.Fatalf("missing: %s %d %v %v", raw, rev, found, err)
	}
	if err := d.PutSetting(ctx, originsKey, nil); err != nil {
		t.Fatal(err)
	}
	raw, rev, found, err = d.ReadOrigins(ctx)
	if err != nil || !found || string(raw) != "null" {
		t.Fatalf("legacy null: %s %d %v %v", raw, rev, found, err)
	}
	initial := json.RawMessage(`{"session":{"machine_id":"machine","origin":"task","by":"start"}}`)
	if ok, err := d.CompareOrigins(ctx, rev, initial); err != nil || !ok {
		t.Fatalf("write: %v %v", ok, err)
	}
	raw, rev, found, err = other.ReadOrigins(ctx)
	if err != nil || !found {
		t.Fatalf("read: %v %v", found, err)
	}
	var legacy map[string]map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy["session"]["origin"] != "task" {
		t.Fatalf("legacy shape: %s %v", raw, err)
	}
	if err := d.PutSetting(ctx, originsKey, map[string]any{"new-session": map[string]string{"origin": "user"}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := other.CompareOrigins(ctx, rev, initial); err != nil || ok {
		t.Fatalf("legacy writer was not detected: %v %v", ok, err)
	}
	for _, invalid := range []string{"null", "[]", "3", "{"} {
		if _, err := d.CompareOrigins(ctx, rev, json.RawMessage(invalid)); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestScheduleCompare(t *testing.T) {
	d, other := scheduleDBs(t)
	ctx := context.Background()
	raw, rev, found, err := d.ReadSchedule(ctx)
	if err != nil || found || rev != 0 || raw != nil {
		t.Fatalf("missing: %s %d %v %v", raw, rev, found, err)
	}
	ok, err := d.CompareSchedule(ctx, 0, json.RawMessage(`[]`))
	if err != nil || !ok {
		t.Fatalf("insert: %v %v", ok, err)
	}
	raw, rev, found, err = other.ReadSchedule(ctx)
	if err != nil || !found || rev == 0 || string(raw) != `[]` {
		t.Fatalf("empty: %s %d %v %v", raw, rev, found, err)
	}
	ok, err = other.CompareSchedule(ctx, 0, json.RawMessage(`[]`))
	if err != nil || ok {
		t.Fatalf("insert conflict: %v %v", ok, err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, db := range []*DB{d, other} {
		wg.Go(func() {
			ok, err := db.CompareSchedule(ctx, rev, json.RawMessage(`[{}]`))
			if err != nil {
				t.Error(err)
			}
			results <- ok
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent CAS wins = %d", wins)
	}
	_, rev, _, err = d.ReadSchedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.PutSetting(ctx, scheduleKey, []any{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.CompareSchedule(ctx, rev, json.RawMessage(`[{}]`)); err != nil || ok {
		t.Fatalf("ordinary write was missed: %v %v", ok, err)
	}
	for _, input := range []string{"null", "{}", "[", `"x"`, ""} {
		if _, err := d.CompareSchedule(ctx, 0, json.RawMessage(input)); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	if err := d.PutSetting(ctx, scheduleKey, nil); err != nil {
		t.Fatal(err)
	}
	raw, _, found, err = other.ReadSchedule(ctx)
	if err != nil || !found || string(raw) != "null" {
		t.Fatalf("legacy null: %s %v %v", raw, found, err)
	}
	if err := d.PutSetting(ctx, scheduleKey, map[string]int{"bad": 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := other.ReadSchedule(ctx); err == nil {
		t.Fatal("accepted non-array stored value")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, _, err := d.ReadSchedule(cancelled); err == nil {
		t.Fatal("read ignored cancellation")
	}
	if _, err := d.CompareSchedule(cancelled, 0, json.RawMessage(`[]`)); err == nil {
		t.Fatal("write ignored cancellation")
	}
	if _, err := d.CompareSchedule(ctx, -1, json.RawMessage(`[]`)); err == nil {
		t.Fatal("accepted negative revision")
	}
	other.Close()
	if _, _, _, err := other.ReadSchedule(ctx); err == nil {
		t.Fatal("read ignored closed pool")
	}
	if _, err := other.CompareSchedule(ctx, 0, json.RawMessage(`[]`)); err == nil {
		t.Fatal("write ignored closed pool")
	}
	if _, _, _, err := other.TryScheduleOwner(ctx); err == nil {
		t.Fatal("owner ignored closed pool")
	}
}

func TestScheduleRunsCompare(t *testing.T) {
	d, other := scheduleDBs(t)
	ctx := context.Background()
	raw, rev, found, err := d.ReadScheduleRuns(ctx)
	if err != nil || found || rev != 0 || raw != nil {
		t.Fatalf("missing: %s %d %v %v", raw, rev, found, err)
	}
	if ok, err := d.CompareScheduleRuns(ctx, 0, json.RawMessage(`[]`)); err != nil || !ok {
		t.Fatalf("insert missing: %v %v", ok, err)
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM settings WHERE key = $1`, scheduleRunsKey); err != nil {
		t.Fatal(err)
	}
	if err := d.PutSetting(ctx, scheduleRunsKey, nil); err != nil {
		t.Fatal(err)
	}
	raw, rev, found, err = other.ReadScheduleRuns(ctx)
	if err != nil || !found || rev == 0 || string(raw) != "null" {
		t.Fatalf("legacy null: %s %d %v %v", raw, rev, found, err)
	}
	if ok, err := other.CompareScheduleRuns(ctx, 0, json.RawMessage(`[]`)); err != nil || ok {
		t.Fatalf("insert over existing null: %v %v", ok, err)
	}
	if ok, err := d.CompareScheduleRuns(ctx, rev, json.RawMessage(`[]`)); err != nil || !ok {
		t.Fatalf("replace null: %v %v", ok, err)
	}
	raw, rev, found, err = other.ReadScheduleRuns(ctx)
	if err != nil || !found || rev == 0 || string(raw) != `[]` {
		t.Fatalf("empty: %s %d %v %v", raw, rev, found, err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, db := range []*DB{d, other} {
		wg.Go(func() {
			ok, err := db.CompareScheduleRuns(ctx, rev, json.RawMessage(`[{}]`))
			if err != nil {
				t.Error(err)
			}
			results <- ok
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent CAS wins = %d", wins)
	}
	_, rev, _, err = d.ReadScheduleRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.PutSetting(ctx, scheduleRunsKey, []any{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.CompareScheduleRuns(ctx, rev, json.RawMessage(`[{}]`)); err != nil || ok {
		t.Fatalf("ordinary write was missed: %v %v", ok, err)
	}
	for _, input := range []string{"null", "{}", "[", `"x"`, ""} {
		if _, err := d.CompareScheduleRuns(ctx, 0, json.RawMessage(input)); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	if _, _, found, err := d.ReadSchedule(ctx); err != nil || found {
		t.Fatalf("runs leaked into tasks: %v %v", found, err)
	}
}

func TestScheduleSettingsTable(t *testing.T) {
	manager, _ := scheduleDBs(t)
	ctx := context.Background()
	hub, err := Attach(ctx, os.Getenv("PERSONA_TEST_DATABASE_URL"), HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if _, err := hub.pool.Exec(ctx, `DELETE FROM hub_settings WHERE key = $1`, scheduleKey); err != nil {
		t.Fatal(err)
	}
	if err := hub.PutSetting(ctx, scheduleKey, []map[string]string{{"id": "hub"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := manager.ReadSchedule(ctx); err != nil || found {
		t.Fatalf("hub leaked into manager: found=%v err=%v", found, err)
	}
	if ok, err := manager.CompareSchedule(ctx, 0, json.RawMessage(`[]`)); err != nil || !ok {
		t.Fatalf("manager insert: %v %v", ok, err)
	}
	raw, rev, found, err := hub.ReadSchedule(ctx)
	if err != nil || !found || !strings.Contains(string(raw), `"hub"`) {
		t.Fatalf("hub read: %s %d %v %v", raw, rev, found, err)
	}
	if ok, err := hub.CompareSchedule(ctx, rev, json.RawMessage(`[{"id":"updated"}]`)); err != nil || !ok {
		t.Fatalf("hub update: %v %v", ok, err)
	}
	var stored []map[string]string
	if found, err := hub.Setting(ctx, scheduleKey, &stored); err != nil || !found || len(stored) != 1 || stored[0]["id"] != "updated" {
		t.Fatalf("hub Setting: %v %v %v", stored, found, err)
	}
	_, rev, _, err = hub.ReadSchedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.PutSetting(ctx, scheduleKey, []any{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := hub.CompareSchedule(ctx, rev, json.RawMessage(`[]`)); err != nil || ok {
		t.Fatalf("hub ordinary write was missed: %v %v", ok, err)
	}
	raw, _, found, err = manager.ReadSchedule(ctx)
	if err != nil || !found || string(raw) != `[]` {
		t.Fatalf("manager changed: %s %v %v", raw, found, err)
	}
}

func awaitScheduleDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not stop")
	}
}

func TestScheduleOwner(t *testing.T) {
	d, other := scheduleDBs(t)
	ctx := context.Background()
	done, release, got, err := d.TryScheduleOwner(ctx)
	if err != nil || !got {
		t.Fatalf("owner: %v %v", got, err)
	}
	defer release()
	if _, _, got, err := other.TryScheduleOwner(ctx); err != nil || got {
		t.Fatalf("exclusive lock: %v %v", got, err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(release)
	}
	wg.Wait()
	awaitScheduleDone(t, done)
	done, release, got, err = other.TryScheduleOwner(ctx)
	if err != nil || !got {
		t.Fatalf("after release: %v %v", got, err)
	}
	release()
	awaitScheduleDone(t, done)
	ownerCtx, cancel := context.WithCancel(ctx)
	done, release, got, err = d.TryScheduleOwner(ownerCtx)
	if err != nil || !got {
		t.Fatalf("cancellable owner: %v %v", got, err)
	}
	cancel()
	awaitScheduleDone(t, done)
	release()
	done, release, got, err = other.TryScheduleOwner(ctx)
	if err != nil || !got {
		t.Fatalf("after cancellation: %v %v", got, err)
	}
	release()
	awaitScheduleDone(t, done)
	if _, _, got, err := d.TryScheduleOwner(ownerCtx); err == nil || got {
		t.Fatalf("cancelled acquisition: %v %v", got, err)
	}
}

func TestScheduleOwnerContextLifetime(t *testing.T) {
	d, other := scheduleDBs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done, release, got, err := d.TryScheduleOwner(ctx)
	if err != nil || !got {
		t.Fatalf("owner: %v %v", got, err)
	}
	defer release()
	stopWorker := make(chan struct{})
	workerDone := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopWorker) }) }
	defer stop()
	go func() {
		defer close(workerDone)
		<-stopWorker
	}()

	// Unexpected cancellation releases the session but does not join workers.
	cancel()
	awaitScheduleDone(t, done)
	select {
	case <-workerDone:
		t.Fatal("owner cancellation unexpectedly joined the worker")
	default:
	}
	done2, release2, got, err := other.TryScheduleOwner(context.Background())
	if err != nil || !got {
		t.Fatalf("cancelled owner kept the lock: %v %v", got, err)
	}
	defer release2()
	stop()
	awaitScheduleDone(t, workerDone)
	release2()
	awaitScheduleDone(t, done2)

	// An orderly shutdown can stop and join workers while the lock remains held.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done, release, got, err = d.TryScheduleOwner(ctx)
	if err != nil || !got {
		t.Fatalf("new owner: %v %v", got, err)
	}
	defer release()
	workerDone = make(chan struct{})
	go func(done chan struct{}) { close(done) }(workerDone)
	awaitScheduleDone(t, workerDone)
	if _, _, got, err := other.TryScheduleOwner(context.Background()); err != nil || got {
		t.Fatalf("lock lost before shutdown: %v %v", got, err)
	}
	cancel()
	awaitScheduleDone(t, done)
}

func TestScheduleOwnerConnectionLoss(t *testing.T) {
	// Terminating a backend is only safe in dbtest's per-run database.
	if !strings.Contains(os.Getenv("PERSONA_TEST_DATABASE_URL"), "/persona_test_") {
		t.Skip("requires dbtest's isolated database")
	}
	d, other := scheduleDBs(t)
	ctx := context.Background()
	done, release, got, err := d.TryScheduleOwner(ctx)
	if err != nil || !got {
		t.Fatalf("owner: %v %v", got, err)
	}
	defer release()
	var terminated bool
	err = other.pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid::bigint = $1 AND objid::bigint = $2`, scheduleOwnerLock>>32, scheduleOwnerLock&0xffffffff).Scan(&terminated)
	if err != nil || !terminated {
		t.Fatalf("terminate owner: %v %v", terminated, err)
	}
	awaitScheduleDone(t, done)
	done2, release2, got, err := other.TryScheduleOwner(ctx)
	if err != nil || !got {
		t.Fatalf("after connection loss: %v %v", got, err)
	}
	defer release2()
	if _, _, got, err := d.TryScheduleOwner(ctx); err != nil || got {
		t.Fatalf("lost owner retook lock: %v %v", got, err)
	}
	release2()
	awaitScheduleDone(t, done2)
}
