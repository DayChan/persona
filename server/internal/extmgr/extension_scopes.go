package extmgr

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// An extension's token (docs/extensions/extensions.md "Permissions"): it opens the routes of the scopes the user granted the
// extension (permissions.hub) and no other. The manager makes one each time it starts the extension and drops the one
// before; it drops them all when the extension is switched off, upgraded, uninstalled or reconfigured (apply). Only the
// tokens in memory open anything: an extension's process is the manager's child and starts again with a new manager
// (docs/operations/hub-restart.md), so a token never outlives the run that made it. What is issued and revoked is also
// written to the manager's database (extension_tokens, #1156: the hash, never the token), and a start revokes there
// what its last run left live (revokeLastRun); nothing there is read back as a credential.

// roleExtension is an extension's token.
const roleExtension = "extension"

// extTokenPrefix starts every extension's token, so authenticate knows it without a query.
const extTokenPrefix = "pext_"

type extToken struct {
	extension string
	name      string
	scopes    []string
	// all: every scope (an extension of a manager other than the built-in, or an attached one, granted the hub's full
	// token the old way, hub_api or manager.json's extensions: it gets this instead, hub_relay.go), as
	// extension.EveryScope has them. Never a route open to full credentials only.
	all bool
}

// opens is scopeOpens for the token.
func (e extToken) opens(pattern, path string) (bool, string) {
	if e.all {
		_, scope := scopeOpens(nil, pattern, path)
		return scope != "" && extension.InEveryScope(scope), scope
	}
	return scopeOpens(e.scopes, pattern, path)
}

// principal is who the token is.
func (e extToken) principal() principal {
	scopes := slices.Clone(e.scopes)
	if e.all {
		scopes = extension.EveryScope()
	}
	return principal{ID: "extension:" + e.extension, Name: e.name, Role: roleExtension, Extension: e.extension,
		Scopes: scopes}
}

// extTokens are the extensions' live tokens.
type extTokens struct {
	mu     sync.Mutex
	byHash map[string]extToken // token hash → whose
	byExt  map[string]string   // extension id → its token's hash

	// record, owner: where what is issued and revoked is written, as whose (keepRecord); nil writes nothing. pending
	// are the writes of the changes made under mu, in their order; flush runs them under recordMu, outside mu, so a
	// slow database never holds up a lookup.
	record   extTokenRecord
	owner    string
	logf     func(string, ...any)
	pending  []func()
	recordMu sync.Mutex
}

// extTokenRecord is the database's record of the tokens (extension_tokens).
type extTokenRecord interface {
	IssueExtensionToken(ctx context.Context, t db.ExtensionToken) error
	RevokeExtensionToken(ctx context.Context, manager, hash, reason string) error
	RevokeExtensionTokens(ctx context.Context, manager, reason string) (int64, error)
}

var _ extTokenRecord = (*db.DB)(nil)

// extTokenRecordWithin bounds one write to the record.
const extTokenRecordWithin = 5 * time.Second

// keepRecord writes what is issued and revoked from now on to r, as owner's (the manager's id).
func (t *extTokens) keepRecord(r extTokenRecord, owner string, logf func(string, ...any)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record, t.owner, t.logf = r, owner, logf
}

// startTokenRecord writes the manager's tokens to its database, when it keeps one, and revokes there what its last run
// left live (Run, before any extension starts).
func (h *Manager) startTokenRecord(ctx context.Context) {
	if r, ok := h.store.(extTokenRecord); ok {
		h.extTokens.keepRecord(r, cmp.Or(h.opts.ManagerID, assistantwire.AssistantID), h.logf)
	}
	h.extTokens.revokeLastRun(ctx)
}

// tokenStopReason is why apply revokes the token of an extension the registry now has as p (nil: removed).
func tokenStopReason(p *extension.Extension) string {
	switch {
	case p == nil:
		return db.ExtensionTokenUninstalled
	case !p.Record.Enabled:
		return db.ExtensionTokenDisabled
	}
	return db.ExtensionTokenStopped
}

// revokeLastRun revokes in the record what the manager's last run left live (it stopped without revoking, or never
// stopped): before any extension starts, so none of this run's is among them.
func (t *extTokens) revokeLastRun(ctx context.Context) {
	t.mu.Lock()
	logf := t.logf
	t.later(ctx, "the last run's tokens", func(ctx context.Context, r extTokenRecord, owner string) error {
		n, err := r.RevokeExtensionTokens(ctx, owner, db.ExtensionTokenManagerRestart)
		if n > 0 && logf != nil {
			logf("extension tokens: revoked %d left live by the last run", n)
		}
		return err
	})
	t.mu.Unlock()
	t.flush()
}

// later queues the record's write of a change made under t.mu (held); flush, after t.mu is released, runs it.
func (t *extTokens) later(ctx context.Context, what string, write func(context.Context, extTokenRecord, string) error) {
	r, owner, logf := t.record, t.owner, t.logf
	if r == nil {
		return
	}
	t.pending = append(t.pending, func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extTokenRecordWithin)
		defer cancel()
		if err := write(ctx, r, owner); err != nil && logf != nil {
			logf("extension tokens: recording %s: %v", what, err)
		}
	})
}

// flush runs the queued writes in order; when it returns, the writes queued before it was called have run.
func (t *extTokens) flush() {
	t.recordMu.Lock()
	defer t.recordMu.Unlock()
	for {
		t.mu.Lock()
		writes := t.pending
		t.pending = nil
		t.mu.Unlock()
		if len(writes) == 0 {
			return
		}
		for _, w := range writes {
			w()
		}
	}
}

// issue makes id a new token of scopes, dropping the one it had.
func (t *extTokens) issue(id, name string, scopes []string) string {
	return t.issueToken(extToken{extension: id, name: name, scopes: slices.Clone(scopes)})
}

// issueAll makes id a new token of every scope, dropping the one it had.
func (t *extTokens) issueAll(id, name string) string {
	return t.issueToken(extToken{extension: id, name: name, all: true})
}

func (t *extTokens) issueToken(e extToken) string {
	id := e.extension
	tok := extTokenPrefix + randomToken()
	hash := hashToken(tok)
	t.mu.Lock()
	if t.byHash == nil {
		t.byHash, t.byExt = map[string]extToken{}, map[string]string{}
	}
	if old, ok := t.byExt[id]; ok {
		delete(t.byHash, old)
	}
	t.byHash[hash] = e
	t.byExt[id] = hash
	t.later(context.Background(), id+"'s new token", func(ctx context.Context, r extTokenRecord, owner string) error {
		return r.IssueExtensionToken(ctx, db.ExtensionToken{Hash: hash, Manager: owner, Extension: id, Name: e.name,
			Scopes: e.scopes, All: e.all})
	})
	t.mu.Unlock()
	t.flush()
	return tok
}

// revoke drops id's token; why (db.ExtensionToken…) goes to the record.
func (t *extTokens) revoke(id, why string) {
	t.mu.Lock()
	if hash, ok := t.byExt[id]; ok {
		delete(t.byHash, hash)
		delete(t.byExt, id)
		t.later(context.Background(), id+"'s revocation", func(ctx context.Context, r extTokenRecord, owner string) error {
			return r.RevokeExtensionToken(ctx, owner, hash, why)
		})
	}
	t.mu.Unlock()
	t.flush()
}

// lookup is the principal of tok, an extension's token.
func (t *extTokens) lookup(tok string) (principal, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.byHash[hashToken(tok)]
	if !ok {
		return principal{}, false
	}
	return e.principal(), true
}

// scopeOpens says whether scopes open the route of pattern (the hub's mux pattern, or the manager's for a relayed one)
// at path, and the scope it needs ("" for a route open to full credentials only). The table is shared with the hub
// (extwire.RouteScopes: TestRouteScopes keeps the list of every route and what it was decided).
func scopeOpens(scopes []string, pattern, path string) (bool, string) {
	scope, ok := extwire.RouteScopes[pattern]
	if !ok {
		return false, ""
	}
	if scope == extension.ScopeCallPrefix {
		if scope = extwire.ExtensionCallScope(path); scope == "" {
			return false, ""
		}
	}
	return slices.Contains(scopes, scope), scope
}

// lookupHash is the token of the hash (the hub asks with it: extensions.authorize).
func (t *extTokens) lookupHash(hash string) (extToken, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.byHash[hash]
	return e, ok
}

// answerOnly says whether the request comes from an answer-only device (a watch): what such a device sends is held to
// a card's own choices and plain text. A full credential and an extension granted the route's scope are not.
func answerOnly(r *http.Request) bool { return principalOf(r.Context()).Role == roleAnswer }

var _ = http.MethodGet

// startToken is an extension's token for the run of its program: made when the hub starts the program (its
// environment), handed on in initialize.
type startToken struct {
	mu  sync.Mutex
	tok string
}

// renew makes p a new token of the scopes it was granted (all: of every scope), dropping the one before.
func (t *startToken) renew(ps *extensions, p *extension.Extension, all bool) string {
	var tok string
	if all {
		tok = ps.h.extTokens.issueAll(p.Record.ID, p.Manifest.Name)
	} else {
		tok = ps.h.extTokens.issue(p.Record.ID, p.Manifest.Name, p.Record.Granted.Hub)
	}
	t.mu.Lock()
	t.tok = tok
	t.mu.Unlock()
	return tok
}

func (t *startToken) current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tok
}
