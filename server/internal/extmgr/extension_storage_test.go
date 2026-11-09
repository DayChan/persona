package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
