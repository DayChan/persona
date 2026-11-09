package extmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extmgr/storage"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

type businessStorage interface {
	ExtensionStorage(context.Context, extension.StorageScope, json.RawMessage, db.StorageUpdate) (any, error)
	RetainedExtensionStorage(context.Context, extension.StorageScope) ([]db.ExtensionStorageRetained, error)
	RestoreExtensionStorage(context.Context, extension.StorageScope, string, string, string, json.RawMessage, db.StorageUpdate) error
}

func (ps *extensions) storageScope(p *extension.Extension, run string) (storage.Scope, error) {
	if p == nil || p.Record.Source != extension.SourceInstalled || p.Record.Granted.Storage == nil || run == "" {
		return storage.Scope{}, storage.ErrDenied
	}
	return storage.Scope{Plugin: p.Record.StorageKey(), PublicID: p.Record.ID, InstalledAt: p.Record.InstalledAt, UpdatedAt: p.Record.UpdatedAt, Run: run, Granted: p.Record.Granted.Storage}, nil
}

func loadBusinessStorage(data json.RawMessage) (storage.State, error) {
	var state storage.State
	if len(data) > storage.DocumentMost || json.Unmarshal(data, &state) != nil {
		return state, storage.ErrCorrupt
	}
	return state, state.Validate()
}

func storeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	code, key := "storage_unavailable", "unavailable"
	switch {
	case errors.Is(err, storage.ErrDenied):
		code, key = extension.CodePermissionDenied, "denied"
	case errors.Is(err, storage.ErrInvalid):
		code, key = "invalid_params", "invalid"
	case errors.Is(err, storage.ErrQuota):
		code, key = "storage_quota", "quota"
	case errors.Is(err, storage.ErrConflict):
		code, key = "storage_conflict", "conflict"
	case errors.Is(err, storage.ErrNotFound):
		code, key = "not_found", "not_found"
	case errors.Is(err, storage.ErrStale):
		code, key = "storage_retired", "retired"
	case errors.Is(err, storage.ErrExpired):
		code, key = "storage_expired", "expired"
	case errors.Is(err, storage.ErrCorrupt):
		code, key = "storage_corrupt", "corrupt"
	case errors.Is(err, storage.ErrUnknown), errors.As(err, new(*extension.CommitUnknownError)):
		code, key = "storage_unknown", "unknown"
	case errors.Is(err, context.Canceled):
		code, key = "storage_canceled", "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code, key = "storage_timeout", "timeout"
	}
	return &rpc.Error{Code: code, Message: i18n.T(i18n.From(ctx), "extension-storage."+key)}
}

func (ps *extensions) storageBackend() (businessStorage, error) {
	if ps.h == nil {
		return nil, storage.ErrUnavailable
	}
	backend, ok := ps.h.opts.Store.(businessStorage)
	if !ok {
		return nil, storage.ErrUnavailable
	}
	return backend, nil
}

// storageCall runs only on the authenticated stdio callback of this exact
// installation/run. A hub HTTP scope and public extension ID cannot call it.
func (ps *extensions) storageCall(ctx context.Context, p *extension.Extension, method string, params json.RawMessage) (any, error) {
	backend, err := ps.storageBackend()
	if err != nil {
		return nil, storeError(ctx, err)
	}
	scope, err := ps.storageScope(p, host.InvocationRun(ctx))
	if err != nil {
		return nil, storeError(ctx, err)
	}
	if wire.ValidateParams(method, params) != nil {
		return nil, storeError(ctx, storage.ErrInvalid)
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	initial, _ := json.Marshal(storage.NewState())
	value, err := backend.ExtensionStorage(call, scope, initial, storageCallUpdate(scope, method, params))
	return value, storeError(ctx, err)
}

// storageCallUpdate runs one guest call on the stored document. A call that
// changes nothing (get, list, request, a receipt replay) hands the stored bytes
// back as they are: the backend writes nothing for it, and encoding a document
// of up to storage.DocumentMost would run inside the open transaction, with its
// locks held.
func storageCallUpdate(scope storage.Scope, method string, params json.RawMessage) db.StorageUpdate {
	return func(raw json.RawMessage, previousRun string) (json.RawMessage, any, bool, error) {
		state, err := loadBusinessStorage(raw)
		if err != nil {
			return nil, nil, false, err
		}
		if previousRun != scope.Run {
			return nil, nil, false, storage.ErrStale
		}
		value, changed, err := storage.Execute(&state, scope, method, params, time.Now())
		if err != nil {
			return nil, nil, false, err
		}
		encoded, err := json.Marshal(value)
		if err != nil || wire.ValidateResult(method, encoded) != nil {
			return nil, nil, false, storage.ErrCorrupt
		}
		if !changed {
			return raw, value, false, nil
		}
		next, err := json.Marshal(state)
		return next, value, true, err
	}
}

// storageStart binds the run in the same initialize attempt before any guest
// storage access. Replacing a run fences previous queue leases as unknown.
func (ps *extensions) storageStart(ctx context.Context, p *extension.Extension) error {
	if p.Record.Granted.Storage == nil {
		return nil
	}
	backend, err := ps.storageBackend()
	if err != nil {
		return err
	}
	scope, err := ps.storageScope(p, host.InvocationRun(ctx))
	if err != nil {
		return err
	}
	scope.Start = true
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	initial, _ := json.Marshal(storage.NewState())
	_, err = backend.ExtensionStorage(call, scope, initial, func(raw json.RawMessage, previous string) (json.RawMessage, any, bool, error) {
		state, err := loadBusinessStorage(raw)
		if err != nil {
			return nil, nil, false, err
		}
		state.ReplaceRun(scope.Run)
		next, err := json.Marshal(state)
		return next, nil, previous != scope.Run, err
	})
	return err
}

// storageRuntimeContext keeps a restored process alive until the manager stops,
// not merely until the authenticated restore HTTP request has returned.
func (ps *extensions) storageRuntimeContext() context.Context {
	if ps.ctx != nil {
		return ps.ctx
	}
	return context.Background()
}

func (h *Manager) storageRoutes(api *http.ServeMux) {
	owner := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p := principalOf(r.Context())
			if !p.Owner || p.Extension != "" || h.extensions == nil {
				writeExtensionResponseError(w, r, http.StatusForbidden, "permission_denied", i18n.Err("extension-storage.denied"))
				return
			}
			next(w, r)
		}
	}
	scopeOf := func(r *http.Request) (storage.Scope, businessStorage, error) {
		p := h.extensions.reg.SnapshotExact(r.PathValue("id"))
		scope, err := h.extensions.storageScope(p, "owner-restore")
		if err != nil {
			return scope, nil, err
		}
		scope.Owner = true
		backend, err := h.extensions.storageBackend()
		return scope, backend, err
	}
	api.HandleFunc("GET /api/v1/extensions/{id}/-/storage/retained", owner(func(w http.ResponseWriter, r *http.Request) {
		scope, backend, err := scopeOf(r)
		if err != nil {
			writeStorageHTTPError(w, r, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		rows, err := backend.RetainedExtensionStorage(ctx, scope)
		if err != nil {
			writeStorageHTTPError(w, r, err)
			return
		}
		writeExtensionJSON(w, r, map[string]any{"items": rows})
	}))
	api.HandleFunc("POST /api/v1/extensions/{id}/-/storage/restore", owner(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Source    string `json:"source"`
			RequestID string `json:"request_id"`
		}
		reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		reader.DisallowUnknownFields()
		if reader.Decode(&in) != nil || in.Source == "" || len(in.Source) > 80 || len(in.RequestID) > 80 || in.RequestID == "" {
			writeStorageHTTPError(w, r, storage.ErrInvalid)
			return
		}
		var extra any
		if reader.Decode(&extra) != io.EOF {
			writeStorageHTTPError(w, r, storage.ErrInvalid)
			return
		}
		scope, backend, err := scopeOf(r)
		if err != nil {
			writeStorageHTTPError(w, r, err)
			return
		}
		canonical, _ := json.Marshal(in)
		sum := sha256.Sum256(canonical)
		fingerprint := hex.EncodeToString(sum[:])
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		initial, _ := json.Marshal(storage.NewState())
		err = backend.RestoreExtensionStorage(ctx, scope, in.Source, in.RequestID, fingerprint, initial, func(raw json.RawMessage, _ string) (json.RawMessage, any, bool, error) {
			state, err := loadBusinessStorage(raw)
			if err != nil {
				return nil, nil, false, err
			}
			state.ReplaceRun("")
			// Receipts belong to their old installation, not the new replay namespace.
			state.Receipts = map[string]storage.Receipt{}
			next, err := json.Marshal(state)
			return next, nil, true, err
		})
		if err != nil {
			writeStorageHTTPError(w, r, err)
			return
		}
		// Replace the runtime after commit so initialization binds the restored
		// space. Its lifetime is the manager's, not this finished HTTP request.
		// A replay is harmless; a failed restore never restarts the guest.
		h.extensions.apply(h.extensions.storageRuntimeContext(), scope.PublicID)
		writeExtensionJSON(w, r, map[string]bool{"restored": true})
	}))
}

func writeStorageHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	public := storeError(r.Context(), err).(*rpc.Error)
	status := http.StatusBadRequest
	switch public.Code {
	case extension.CodePermissionDenied:
		status = http.StatusForbidden
	case "storage_conflict", "storage_retired":
		status = http.StatusConflict
	case "storage_quota":
		status = http.StatusInsufficientStorage
	case "storage_unavailable", "storage_unknown":
		status = http.StatusServiceUnavailable
	}
	writeExtensionResponseError(w, r, status, public.Code, errors.New(public.Message))
}
