package extmgr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/denohost"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extmgr/storage"
	"github.com/nwverse/persona/server/internal/rpc"
)

// These tests use real Postgres and the separately bundled official SDK/trusted
// adapter under an injected Node transport. They are not Deno sandbox acceptance
// or a database-server restart; installed Deno acceptance runs separately.
type storageFaultStore struct {
	*db.DB
	loseAck atomic.Bool
	refuse  atomic.Bool
}

func (s *storageFaultStore) ExtensionStorage(ctx context.Context, scope extension.StorageScope, initial json.RawMessage, update db.StorageUpdate) (any, error) {
	if s.refuse.Load() {
		return nil, errors.New("fixture database unavailable")
	}
	value, err := s.DB.ExtensionStorage(ctx, scope, initial, update)
	if err == nil && s.loseAck.Swap(false) {
		return nil, &extension.CommitUnknownError{Err: errors.New("fixture acknowledgment lost after real commit")}
	}
	return value, err
}

func storageAdapterProgram(t *testing.T) (string, string, string) {
	t.Helper()
	root := shortTempDir(t)
	program := filepath.Join(root, "storage.mjs")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	source, err := filepath.Abs("testdata/storage-program.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "bun", "build", source, "--target=browser", "--outfile="+program).CombinedOutput(); err != nil {
		t.Fatalf("fixture source build: %v %s", err, out)
	}
	adapter, err := denohost.InstallAdapter(root)
	if err != nil {
		t.Fatal(err)
	}
	facade, err := filepath.Abs("testdata/ui-page-adapter.mjs")
	if err != nil {
		t.Fatal(err)
	}
	return program, adapter, facade
}

func storagePostgres(t *testing.T) *db.DB {
	t.Helper()
	address := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("real package-owned Postgres is required")
	}
	database, err := db.Open(t.Context(), address, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}

func storagePostgresFixture(t *testing.T) *db.DB {
	t.Helper()
	database := storagePostgres(t)
	if err := database.DeleteExtension(t.Context(), "fixture-storage"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := database.DeleteExtension(ctx, "fixture-storage"); err != nil {
			t.Error(err)
		}
	})
	return database
}

func storageProcessManager(t *testing.T, backend *storageFaultStore, program, adapter, facade string) (*Manager, *host.Proc) {
	t.Helper()
	h := newTestManager(t)
	h.store, h.opts.Store = backend, backend
	startTestExtensions(t, h)
	if h.extensions.reg.SnapshotExact("fixture-storage") == nil {
		at := time.Now().UTC().Truncate(time.Microsecond)
		manifest := json.RawMessage(`{"manifest_version":1,"id":"fixture-storage","name":"Synthetic storage fixture","version":"1.0.0","engines":{"extension_protocol":1},"runtime":{"command":"fixture"},"permissions":{"storage":{"patterns":["state/*","queue/*","media/*"],"queues":true,"blobs":true}},"contributes":{"ui":[{"id":"home","title":"Synthetic storage fixture","placement":"extension_page"}]}}`)
		row := extension.Record{ID: "fixture-storage", Source: extension.SourceInstalled, Version: "1.0.0", Manifest: manifest, Enabled: true, InstalledAt: at, UpdatedAt: at,
			Granted: extension.Permissions{Storage: &extension.StoragePermission{Patterns: []string{"state/*", "queue/*", "media/*"}, Queues: true, Blobs: true}}}
		if err := backend.PutExtension(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		if err := h.extensions.reg.Load(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	p := h.extensions.reg.SnapshotExact("fixture-storage")
	hp := &hubExtension{id: p.Record.ID}
	spec, err := h.extensions.specOf(t.Context(), p, hp)
	if err != nil {
		t.Fatal(err)
	}
	// Use the production initialization/callback handlers, but explicitly inject
	// only the test transport. No installed-runtime or sandbox claim follows.
	spec.Command, spec.Args, spec.Dir = "node", []string{facade, adapter, program}, filepath.Dir(program)
	spec.CleanEnv, spec.OnCall, spec.Startup = true, true, false
	process := host.Start(t.Context(), spec)
	hp.proc = process
	h.extensions.mu.Lock()
	h.extensions.byID[p.Record.ID] = hp
	h.extensions.mu.Unlock()
	t.Cleanup(process.Stop)
	return h, process
}

func storageProcessCall(t *testing.T, process *host.Proc, method string, params, out any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	return process.Call(ctx, "test.storage", map[string]any{"method": method, "params": params}, out)
}

func storageProcessID(t *testing.T, process *host.Proc) string {
	t.Helper()
	var result wire.StorageRequestResult
	if err := storageProcessCall(t, process, "storage.request", map[string]any{}, &result); err != nil || result.RequestID == "" {
		t.Fatal("host mutation identity unavailable", result, err)
	}
	return result.RequestID
}

func storageExpectCode(t *testing.T, err error, code string) {
	t.Helper()
	var public *rpc.Error
	if !errors.As(err, &public) || public.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestStorageSourceAdapterRealPostgresLostAckAndSaveFailure(t *testing.T) {
	database := storagePostgresFixture(t)
	backend := &storageFaultStore{DB: database}
	program, adapter, facade := storageAdapterProgram(t)
	_, process := storageProcessManager(t, backend, program, adapter, facade)
	absent := ""
	request := wire.StorageTransactionParams{RequestID: storageProcessID(t, process), Operations: []wire.StorageOperation{
		{Kind: "put", Key: "state/progress", Revision: &absent, Data: base64.StdEncoding.EncodeToString([]byte("committed progress"))},
		{Kind: "enqueue", Key: "queue/jobs", Dedup: "fixture-event", Data: base64.StdEncoding.EncodeToString([]byte("work"))},
	}}
	backend.loseAck.Store(true)
	var result wire.StorageTransactionResult
	storageExpectCode(t, storageProcessCall(t, process, "storage.transaction", request, &result), "storage_unknown")
	if err := storageProcessCall(t, process, "storage.transaction", request, &result); err != nil || len(result.Items) != 2 {
		t.Fatal("exact lost-ack replay failed", result, err)
	}
	var queue wire.StorageQueueListResult
	if err := storageProcessCall(t, process, "storage.queue.list", wire.StorageQueueListParams{Key: "queue/jobs"}, &queue); err != nil || len(queue.Items) != 1 {
		t.Fatal("lost ack duplicated or lost queued work", queue, err)
	}
	var entry wire.StorageEntry
	if err := storageProcessCall(t, process, "storage.get", wire.StorageKeyParams{Key: "state/progress"}, &entry); err != nil || entry.Data != request.Operations[0].Data {
		t.Fatal("real committed progress unavailable", entry, err)
	}
	request.Operations[0].Data = base64.StdEncoding.EncodeToString([]byte("different"))
	storageExpectCode(t, storageProcessCall(t, process, "storage.transaction", request, &result), "storage_conflict")
	request.RequestID = storageProcessID(t, process)
	request.Operations = []wire.StorageOperation{{Kind: "put", Key: "state/progress", Data: base64.StdEncoding.EncodeToString([]byte("must not commit"))}}
	backend.refuse.Store(true)
	storageExpectCode(t, storageProcessCall(t, process, "storage.transaction", request, &result), "storage_unavailable")
	backend.refuse.Store(false)
	var after wire.StorageEntry
	if err := storageProcessCall(t, process, "storage.get", wire.StorageKeyParams{Key: "state/progress"}, &after); err != nil || after != entry {
		t.Fatal("save failure replaced committed state", after, err)
	}
	storageExpectCode(t, storageProcessCall(t, process, "storage.get", wire.StorageKeyParams{Key: "other/private"}, &after), extension.CodePermissionDenied)
	storageExpectCode(t, storageProcessCall(t, process, "storage.transaction", map[string]any{"request_id": request.RequestID, "operations": request.Operations, "run": "forged-runtime"}, &result), "invalid_params")
	t.Log("PASS real Postgres commit/lost ack, exact replay, atomic queue/progress, unavailable-save preservation and guest scope denial; injected Node adapter only")
}

func TestStorageSourceAdapterRecreatedManagerFencesOldWorkerAndRecoversPending(t *testing.T) {
	database := storagePostgresFixture(t)
	backend := &storageFaultStore{DB: database}
	program, adapter, facade := storageAdapterProgram(t)
	h, old := storageProcessManager(t, backend, program, adapter, facade)
	var result wire.StorageTransactionResult
	request := wire.StorageTransactionParams{RequestID: storageProcessID(t, old), Operations: []wire.StorageOperation{
		{Kind: "put", Key: "state/progress", Data: base64.StdEncoding.EncodeToString([]byte("recover after manager recreation"))},
		{Kind: "enqueue", Key: "queue/jobs", Dedup: "first", Data: base64.StdEncoding.EncodeToString([]byte("first work"))},
		{Kind: "enqueue", Key: "queue/jobs", Dedup: "pending", Data: base64.StdEncoding.EncodeToString([]byte("pending work"))},
	}}
	if err := storageProcessCall(t, old, "storage.transaction", request, &result); err != nil {
		t.Fatal(err)
	}
	claim := wire.StorageClaimParams{RequestID: storageProcessID(t, old), Key: "queue/jobs", LeaseSeconds: 30}
	var claimed wire.StorageClaimResult
	if err := storageProcessCall(t, old, "storage.queue.claim", claim, &claimed); err != nil || claimed.Job == nil {
		t.Fatal("claim failed", claimed, err)
	}
	other := storagePostgres(t)
	_, next := storageProcessManager(t, &storageFaultStore{DB: other}, program, adapter, facade)
	var entry wire.StorageEntry
	if err := storageProcessCall(t, next, "storage.get", wire.StorageKeyParams{Key: "state/progress"}, &entry); err != nil || entry.Data != request.Operations[0].Data {
		t.Fatal("recreated manager/pool lost committed progress", entry, err)
	}
	// Leave the old process alive until the persisted replacement is observed:
	// this tests the database fence, not merely successful child termination.
	storageExpectCode(t, storageProcessCall(t, old, "storage.get", wire.StorageKeyParams{Key: "state/progress"}, &entry), "storage_retired")
	storageExpectCode(t, storageProcessCall(t, next, "storage.queue.claim", claim, &claimed), "storage_retired")
	var queue wire.StorageQueueListResult
	if err := storageProcessCall(t, next, "storage.queue.list", wire.StorageQueueListParams{Key: "queue/jobs"}, &queue); err != nil || len(queue.Items) != 2 {
		t.Fatal("recreated manager lost jobs", queue, err)
	}
	counts := map[string]int{}
	for _, job := range queue.Items {
		counts[job.State]++
	}
	if counts["unknown"] != 1 || counts["pending"] != 1 {
		t.Fatal("lost lease became automatically pending", counts)
	}
	claim.RequestID = storageProcessID(t, next)
	if err := storageProcessCall(t, next, "storage.queue.claim", claim, &claimed); err != nil || claimed.Job == nil || claimed.Job.State != "leased" || claimed.Job.Data == "" {
		t.Fatal("committed pending work did not recover", claimed, err)
	}
	old.Stop()
	p := h.extensions.reg.SnapshotExact("fixture-storage")
	scope := storage.Scope{Plugin: p.Record.StorageKey(), PublicID: p.Record.ID, InstalledAt: p.Record.InstalledAt, UpdatedAt: p.Record.UpdatedAt, Granted: p.Record.Granted.Storage, Run: "forged"}
	if _, err := backend.ExtensionStorage(t.Context(), scope, json.RawMessage(`{}`), func(raw json.RawMessage, previous string) (json.RawMessage, any, bool, error) {
		return raw, nil, false, nil
	}); !errors.Is(err, storage.ErrStale) {
		t.Fatal("old manager could choose a new run identity", err)
	}
	t.Log("PASS recreated manager/independent pool, old live worker fenced, one unknown lease retained and one pending job recovered; no database-server restart claimed")
}
