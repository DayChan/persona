package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- moving a source to another manager ----

// sourceLock is one source's move lock, kept while a move of it runs or waits.
type sourceLock struct {
	mu    sync.Mutex
	users int
}

// lockSource holds source's move lock (one move of a source at a time) and returns its unlock, which forgets the lock
// once no move of it waits.
func (mv *moves) lockSource(source string) func() {
	mv.mu.Lock()
	if mv.sources == nil {
		mv.sources = map[string]*sourceLock{}
	}
	l := mv.sources[source]
	if l == nil {
		l = &sourceLock{}
		mv.sources[source] = l
	}
	l.users++
	mv.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		mv.mu.Lock()
		if l.users--; l.users == 0 {
			delete(mv.sources, source)
		}
		mv.mu.Unlock()
	}
}

// moveSource moves source (extwire.SourceRef: "mail:<account>") from manager from to manager to: the old one
// exports it (its secrets in plaintext, over the bus only: the hub neither keeps nor logs the bundle), the new one
// imports it (sealing them with its own key), and the old one lets it go. Both have to be connected; one move of a
// source at a time. When the new one says it has the source already (a move whose release failed, done again), the
// hub checks it has that source and goes on to the release. When the import does not answer, the hub asks the new
// one whether it has the source: yes, it goes on; no, the old one keeps it; no answer, errSourceInDoubt. When the
// release fails both have it, and the error says so: moving it again releases it.
func (h *Hub) moveSource(ctx context.Context, source, from, to string) (string, error) {
	h.moving.managers.RLock()
	defer h.moving.managers.RUnlock()
	defer h.moving.lockSource(source)()
	if !h.knownManager(ctx, from) || !h.knownManager(ctx, to) {
		return "", errUnknownManager
	}
	if h.busConnOf(from) == nil || h.busConnOf(to) == nil {
		return "", errMoveOffline
	}
	var bundle json.RawMessage
	if err := h.callBus(ctx, from, extwire.MethodSourceExport, extwire.SourceRef{Source: source}, &bundle); err != nil {
		return "", fmt.Errorf("%s: export: %w", from, err)
	}
	var ref extwire.SourceRef
	err := h.callBus(ctx, to, extwire.MethodSourceImport, bundle, &ref)
	clear(bundle)
	var coded *rpc.Error
	switch {
	case err == nil:
	case errors.As(err, &coded) && coded.Code == extwire.CodeSourceExists:
		// A move whose release failed, done again: the new one has it if it can export that very source.
		if !h.sourceAt(ctx, to, source) {
			return "", fmt.Errorf("%s: import: %w", to, err)
		}
		ref.Source = source
	case errors.As(err, &coded):
		return "", fmt.Errorf("%s: import: %w", to, err) // refused: the old one keeps it
	default:
		// No answer (dropped, timed out): whether it took it decides.
		qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		has, known := h.sourceKnownAt(qctx, to, source)
		cancel()
		switch {
		case !known:
			return "", fmt.Errorf("%w: %v", errSourceInDoubt, err)
		case !has:
			return "", fmt.Errorf("%s: import: %w", to, err)
		}
		ref.Source = source
	}
	// The new one has it: the release is asked for even when the caller stopped waiting.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := h.callBus(rctx, from, extwire.MethodSourceRelease, extwire.SourceRef{Source: source}, nil); err != nil {
		return cmp.Or(ref.Source, source), fmt.Errorf("%s has it now, but %s did not let it go: %w", to, from, err)
	}
	h.logf("managers: source %s moved from %s to %s", clip(source, 80), from, to)
	return cmp.Or(ref.Source, source), nil
}

// sourceAt: manager id has source (it exports it; what it exports is dropped at once).
func (h *Hub) sourceAt(ctx context.Context, id, source string) bool {
	has, known := h.sourceKnownAt(ctx, id, source)
	return known && has
}

// sourceKnownAt asks manager id whether it has source: has, and whether it answered.
func (h *Hub) sourceKnownAt(ctx context.Context, id, source string) (has, known bool) {
	var b extwire.SourceBundle
	err := h.callBus(ctx, id, extwire.MethodSourceExport, extwire.SourceRef{Source: source}, &b)
	clear(b.Data)
	var coded *rpc.Error
	switch {
	case err == nil && b.Source == source:
		return true, true
	case err == nil: // an answer naming no source, or another: it did not say it has this one
		return false, false
	case errors.As(err, &coded) && coded.Code == extwire.CodeNoSource:
		return false, true
	}
	return false, false
}
