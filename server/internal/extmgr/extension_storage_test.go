package extmgr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr/storage"
	"github.com/nwverse/persona/server/internal/i18n"
)

type storageRouteFixture struct {
	Store
	called  int
	failure error
}

func (s *storageRouteFixture) ExtensionStorage(context.Context, extension.StorageScope, json.RawMessage, db.StorageUpdate) (any, error) {
	return nil, errors.New("fixture does not execute persistence")
}
func (s *storageRouteFixture) RetainedExtensionStorage(_ context.Context, scope extension.StorageScope) ([]db.ExtensionStorageRetained, error) {
	s.called++
	if !scope.Owner {
		return nil, storage.ErrDenied
	}
	return []db.ExtensionStorageRetained{{ID: "fixture-opaque-retained", RetiredAt: time.Now().UTC(), Bytes: 10}}, s.failure
}
func (s *storageRouteFixture) RestoreExtensionStorage(context.Context, extension.StorageScope, string, string, string, json.RawMessage, db.StorageUpdate) error {
	s.called++
	return storage.ErrConflict
}

func storageRouteManager(t *testing.T) (*Manager, *storageRouteFixture, *http.ServeMux) {
	h, base := extensionTestHub(t)
	backend := &storageRouteFixture{Store: base}
	h.opts.Store = backend
	manifest := `{"manifest_version":1,"id":"fixture-storage-ui","name":"Storage fixture","version":"1.0.0","permissions":{"storage":{"keys":["state"]}},"contributes":{"ui":[{"id":"status","title":"Status","placement":"nav_page"}]}}`
	at := time.Now().UTC().Truncate(time.Microsecond)
	row := extension.Record{ID: "fixture-storage-ui", Source: extension.SourceInstalled, Version: "1.0.0", Manifest: json.RawMessage(manifest), Enabled: true, InstalledAt: at, UpdatedAt: at, Granted: extension.Permissions{Storage: &extension.StoragePermission{Keys: []string{"state"}}}}
	if err := base.PutExtension(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.storageRoutes(mux)
	return h, backend, mux
}

func TestStorageRestoreRoutesRejectForgedOwnersAndNeverExposeRetainedMetadata(t *testing.T) {
	_, backend, mux := storageRouteManager(t)
	for _, p := range []principal{{}, {ID: "fixture-phone", Role: roleFull}, {Owner: true, Extension: "malicious-extension"}, {ID: "fixture-paired", Role: roleFull, Paired: true}, {Role: roleAnswer}} {
		for _, method := range []string{"GET", "POST"} {
			path := "/api/v1/extensions/fixture-storage-ui/-/storage/retained"
			body := ""
			if method == "POST" {
				path = "/api/v1/extensions/fixture-storage-ui/-/storage/restore"
				body = `{"source":"fixture-opaque-retained","request_id":"fixture-request"}`
			}
			r := httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("X-Persona-Owner", "1")
			r = r.WithContext(withPrincipal(r.Context(), p))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "fixture-opaque-retained") {
				t.Fatal("unverified owner saw retained metadata or triggered restore", w.Code, w.Body.String())
			}
		}
	}
	if backend.called != 0 {
		t.Fatal("denied principal reached persistence", backend.called)
	}
}

func TestStorageOwnerMetadataAndStrictRestoreInput(t *testing.T) {
	_, backend, mux := storageRouteManager(t)
	owner := principal{Owner: true, Role: roleFull}
	r := httptest.NewRequest("GET", "/api/v1/extensions/fixture-storage-ui/-/storage/retained", nil)
	r = r.WithContext(withPrincipal(r.Context(), owner))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fixture-opaque-retained") || backend.called != 1 {
		t.Fatal("authenticated owner metadata request failed", w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"source":"fixture","request_id":"request","owner":true}`, `{"source":"fixture","request_id":"request"}{}`, strings.Repeat("x", 4097)} {
		r = httptest.NewRequest("POST", "/api/v1/extensions/fixture-storage-ui/-/storage/restore", strings.NewReader(body))
		r = r.WithContext(withPrincipal(r.Context(), owner))
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 400 || backend.called != 1 {
			t.Fatal("invalid restore reached persistence", w.Code, w.Body.String())
		}
	}
	backend.failure = errors.New("PRIVATE-SQL-DIAGNOSTIC")
	r = httptest.NewRequest("GET", "/api/v1/extensions/fixture-storage-ui/-/storage/retained", nil)
	r = r.WithContext(withPrincipal(r.Context(), owner))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "PRIVATE-SQL-DIAGNOSTIC") {
		t.Fatal("backend detail leaked", w.Code, w.Body.String())
	}
}

func TestStorageRuntimeContextOutlivesRestoreRequestAndEndsWithManager(t *testing.T) {
	manager, stopManager := context.WithCancel(t.Context())
	defer stopManager()
	request, finishRequest := context.WithCancel(manager)
	ps := &extensions{ctx: manager}
	runtime := ps.storageRuntimeContext()
	finishRequest()
	if request.Err() == nil || runtime.Err() != nil || runtime != manager {
		t.Fatal("restored runtime followed the completed request instead of its manager")
	}
	stopManager()
	if runtime.Err() != context.Canceled {
		t.Fatal("restored runtime outlived its manager")
	}
}

func TestStoragePublicErrorCategoriesHaveAllThreeLocales(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, err := range []error{storage.ErrDenied, storage.ErrInvalid, storage.ErrQuota, storage.ErrConflict, storage.ErrNotFound, storage.ErrStale, storage.ErrExpired, storage.ErrCorrupt, storage.ErrUnavailable, context.Canceled, context.DeadlineExceeded, &extension.CommitUnknownError{Err: errors.New("secret SQL")}} {
			value := storeError(i18n.With(t.Context(), lang), err)
			if strings.Contains(value.Error(), "extension-storage.") || strings.Contains(value.Error(), "secret SQL") {
				t.Fatal("untranslated or private storage error", lang, value)
			}
		}
	}
}

// storageMemory is an in-memory backend with DB.ExtensionStorage's write rule:
// it keeps next and bumps the revision only for a change or a new run. It also
// keeps what the last update handed back, whether it was written or not.
type storageMemory struct {
	data     json.RawMessage
	run      string
	revision int
	next     json.RawMessage
}

func (s *storageMemory) ExtensionStorage(_ context.Context, scope extension.StorageScope, _ json.RawMessage, update db.StorageUpdate) (any, error) {
	next, value, changed, err := update(s.data, s.run)
	if err != nil {
		return nil, err
	}
	s.next = next
	if changed || s.run != scope.Run {
		s.data, s.run = next, scope.Run
		s.revision++
	}
	return value, nil
}
func (s *storageMemory) RetainedExtensionStorage(context.Context, extension.StorageScope) ([]db.ExtensionStorageRetained, error) {
	return nil, storage.ErrUnavailable
}
func (s *storageMemory) RestoreExtensionStorage(context.Context, extension.StorageScope, string, string, string, json.RawMessage, db.StorageUpdate) error {
	return storage.ErrUnavailable
}

func storageMemoryScope() storage.Scope {
	at := time.Now().UTC()
	return storage.Scope{Plugin: "fixture-storage", PublicID: "fixture-storage", InstalledAt: at, UpdatedAt: at, Run: "fixture-run", Granted: &extension.StoragePermission{Patterns: []string{"state/*"}}}
}

// storageMemoryCall runs a guest call the way storageCall does, minus the
// process run identity, which only the trusted transport can set.
func storageMemoryCall(t testing.TB, backend businessStorage, scope storage.Scope, method string, params any) any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil || wire.ValidateParams(method, raw) != nil {
		t.Fatal("invalid fixture params", method, err)
	}
	value, err := backend.ExtensionStorage(t.Context(), scope, nil, storageCallUpdate(scope, method, raw))
	if err != nil {
		t.Fatal(method, err)
	}
	return value
}

func TestStorageCallReadsHandTheStoredDocumentBackAndWritesPersist(t *testing.T) {
	scope := storageMemoryScope()
	state := storage.NewState()
	state.Entries["state/progress"] = storage.Entry{Revision: "fixture-revision", Data: base64.StdEncoding.EncodeToString([]byte("stored progress"))}
	// Indented, so an encoded copy of the state cannot pass for these bytes.
	document, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	backend := &storageMemory{data: document, run: scope.Run}
	unchanged := func(method string, revision int, data json.RawMessage) {
		t.Helper()
		if backend.revision != revision || !bytes.Equal(backend.data, data) {
			t.Fatal(method, "wrote the document", backend.revision, revision)
		}
		if !bytes.Equal(backend.next, data) {
			t.Fatal(method, "handed back a re-encoded document instead of the stored one")
		}
	}
	reads := func(want storage.Entry) {
		t.Helper()
		revision, data := backend.revision, backend.data
		if value, ok := storageMemoryCall(t, backend, scope, "storage.get", wire.StorageKeyParams{Key: "state/progress"}).(storage.Entry); !ok || value != want {
			t.Fatal("get returned", value, "want", want)
		}
		unchanged("storage.get", revision, data)
		list, _ := storageMemoryCall(t, backend, scope, "storage.list", wire.StorageListParams{Prefix: "state/"}).(map[string]any)
		if items, _ := list["items"].([]storage.Item); !slices.Equal(items, []storage.Item{{Key: "state/progress", Revision: want.Revision}}) || list["more"] != false {
			t.Fatal("list returned", list)
		}
		unchanged("storage.list", revision, data)
	}
	reads(state.Entries["state/progress"])

	id, _ := storageMemoryCall(t, backend, scope, "storage.request", wire.StorageEmptyParams{}).(map[string]string)
	if id["request_id"] == "" {
		t.Fatal("request returned no request ID", id)
	}
	unchanged("storage.request", 0, document)
	put := wire.StorageTransactionParams{RequestID: id["request_id"], Operations: []wire.StorageOperation{{Kind: "put", Key: "state/progress", Data: base64.StdEncoding.EncodeToString([]byte("new progress"))}}}
	storageMemoryCall(t, backend, scope, "storage.transaction", put)
	saved, err := loadBusinessStorage(backend.data)
	if err != nil || backend.revision != 1 || saved.Entries["state/progress"].Data != put.Operations[0].Data || len(saved.Receipts) != 1 {
		t.Fatal("put did not persist", backend.revision, saved, err)
	}
	reads(saved.Entries["state/progress"])
	// The replayed put answers from its receipt and writes nothing.
	storageMemoryCall(t, backend, scope, "storage.transaction", put)
	unchanged("replayed storage.transaction", 1, backend.data)
}

// BenchmarkStorageCallRead reads from a ~4.3 MB document, a third of what one
// space may hold, the way storageCall runs inside its Postgres transaction.
func BenchmarkStorageCallRead(b *testing.B) {
	scope := storageMemoryScope()
	state := storage.NewState()
	for i := range 50 {
		data := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(i)}, storage.ValueMost))
		state.Entries[fmt.Sprintf("state/%02d", i)] = storage.Entry{Revision: fmt.Sprintf("revision-%02d", i), Data: data}
	}
	if err := state.Validate(); err != nil {
		b.Fatal(err)
	}
	document, err := json.Marshal(state)
	if err != nil {
		b.Fatal(err)
	}
	backend := &storageMemory{data: document, run: scope.Run}
	for _, read := range []struct {
		method string
		params any
	}{{"storage.get", wire.StorageKeyParams{Key: "state/07"}}, {"storage.list", wire.StorageListParams{Prefix: "state/"}}} {
		b.Run(read.method, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				storageMemoryCall(b, backend, scope, read.method, read.params)
			}
			b.ReportMetric(float64(len(document)), "doc-bytes")
			if backend.revision != 0 {
				b.Fatal("a read wrote the document")
			}
		})
	}
}
