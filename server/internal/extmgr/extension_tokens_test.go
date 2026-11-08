package extmgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// tokenRecordOn is the manager's database for a test of its tokens' record (#1156), skipped without
// PERSONA_TEST_DATABASE_URL, and a manager id of the test's own: tests on the same database see only their own rows.
func tokenRecordOn(t *testing.T) (*db.DB, string) {
	t.Helper()
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set (scripts/test/test-server.sh runs this on the hub machine)")
	}
	database, err := db.Open(context.Background(), url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database, "example-manager-" + randomHex(4)
}

// tokenRows are manager's records, by hash.
func tokenRows(t *testing.T, database *db.DB, manager string) map[string]db.ExtensionToken {
	t.Helper()
	rows, err := database.ExtensionTokens(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]db.ExtensionToken{}
	for _, r := range rows {
		out[r.Hash] = r
	}
	return out
}

// liveTokens are the hashes of manager's records not revoked.
func liveTokens(rows map[string]db.ExtensionToken) []string {
	var out []string
	for hash, r := range rows {
		if r.RevokedAt == nil {
			out = append(out, hash)
		}
	}
	return out
}

// noPlaintext fails when a record holds tok, or the random part of it, anywhere.
func noPlaintext(t *testing.T, r db.ExtensionToken, tok string) {
	t.Helper()
	fields := append([]string{r.Hash, r.Manager, r.Extension, r.Name, r.Reason}, r.Scopes...)
	for _, f := range fields {
		if strings.Contains(f, tok) || strings.Contains(f, strings.TrimPrefix(tok, extTokenPrefix)) {
			t.Fatalf("the record holds the token: %+v", r)
		}
	}
	if r.Hash != hashToken(tok) {
		t.Fatalf("recorded hash %q, want the token's", r.Hash)
	}
}

// kbSearch is whether tok opens the hub's knowledge-base search on h, and whose h says it is.
func kbSearch(h *Manager, tok string) extwire.Authorized {
	return h.authorize(extwire.Authorize{Hash: hashToken(tok), Pattern: "GET /api/v1/kb-search", Method: "GET", Path: "/api/v1/kb-search"})
}

// What a manager issues and revokes is recorded by hash, never the token, with whose it is, what it opens and why it
// stopped. Authorizing reads only what the manager issued since it started: a token the record still holds as live
// from this manager's last run opens nothing, nor does another manager's, and each start revokes only its own.
func TestExtensionTokenRecord(t *testing.T) {
	database, owner := tokenRecordOn(t)
	h := newTestManager(t)
	h.routes()
	h.extTokens.keepRecord(database, owner, t.Logf)

	tok := h.extTokens.issue("example-x", "Example X", []string{extension.ScopeKBRead, extension.ScopeNotify})
	all := h.extTokens.issueAll("example-y", "Example Y")
	rows := tokenRows(t, database, owner)
	r := rows[hashToken(tok)]
	noPlaintext(t, r, tok)
	if r.Extension != "example-x" || r.Name != "Example X" || r.All || r.RevokedAt != nil || r.IssuedAt.IsZero() ||
		!slices.Equal(r.Scopes, []string{extension.ScopeKBRead, extension.ScopeNotify}) {
		t.Fatalf("the scoped token's record: %+v", r)
	}
	if r := rows[hashToken(all)]; !r.All || len(r.Scopes) != 0 || r.Extension != "example-y" || r.RevokedAt != nil {
		t.Fatalf("the token of every scope: %+v", r)
	}

	tok2 := h.extTokens.issue("example-x", "Example X", []string{extension.ScopeKBRead}) // its next start, granted less
	h.extTokens.revoke("example-y", db.ExtensionTokenDisabled)
	rows = tokenRows(t, database, owner)
	if r := rows[hashToken(tok)]; r.RevokedAt == nil || r.Reason != db.ExtensionTokenReplaced {
		t.Fatalf("the replaced token: %+v", r)
	}
	if r := rows[hashToken(tok2)]; r.RevokedAt != nil || !slices.Equal(r.Scopes, []string{extension.ScopeKBRead}) {
		t.Fatalf("its new token: %+v", r)
	}
	if r := rows[hashToken(all)]; r.RevokedAt == nil || r.Reason != db.ExtensionTokenDisabled {
		t.Fatalf("the revoked token: %+v", r)
	}

	// Another manager on the same database starts: this one's record is not its to touch.
	other := newTestManager(t)
	other.routes()
	other.extTokens.keepRecord(database, "example-other-"+randomHex(4), t.Logf)
	other.extTokens.revokeLastRun(context.Background())
	if live := liveTokens(tokenRows(t, database, owner)); !slices.Equal(live, []string{hashToken(tok2)}) {
		t.Fatalf("after another manager's start, live: %v", live)
	}

	// This manager starts again (the last run ended without revoking tok2): the record's live token is revoked, and
	// nothing recorded opens anything on the new run or the other manager.
	next := newTestManager(t)
	next.routes()
	next.extTokens.keepRecord(database, owner, t.Logf)
	next.extTokens.revokeLastRun(context.Background())
	for _, m := range []*Manager{next, other} {
		for _, tk := range []string{tok, tok2, all} {
			if a := kbSearch(m, tk); a.Extension != "" || a.OK {
				t.Fatalf("a recorded token opens on a manager that did not issue it this run: %+v", a)
			}
		}
	}
	rows = tokenRows(t, database, owner)
	if live := liveTokens(rows); len(live) != 0 {
		t.Fatalf("live after the restart: %v", live)
	}
	if rows[hashToken(tok2)].Reason != db.ExtensionTokenManagerRestart || rows[hashToken(all)].Reason != db.ExtensionTokenDisabled ||
		rows[hashToken(tok)].Reason != db.ExtensionTokenReplaced {
		t.Fatalf("why each stopped: %+v", rows)
	}
}

// The record across a real extension's life (#1156): issued at its start, replaced when an upgrade grants it less,
// revoked when the manager starts again, when it is switched off and when it is uninstalled. No restart brings back a
// token, nor issues one to an extension that is off.
func TestExtensionTokenRecordAcrossRestarts(t *testing.T) {
	database, owner := tokenRecordOn(t)
	h, _ := extensionTestHub(t)
	h.extTokens.keepRecord(database, owner, t.Logf)
	mux, master := h.routes(), h.cfg.Token
	// started is the token the running extension was given once it is not was.
	started := func(h *Manager, mux http.Handler, master, was string) string {
		t.Helper()
		var tok string
		waitLong(t, "the extension up with a new token", func() bool {
			hp := h.extensions.get("scoped")
			if hp == nil || hp.proc == nil || !hp.proc.Ready() {
				return false
			}
			_, out := call(t, mux, "GET", "/api/v1/extensions/scoped/whoami", master, "")
			tok, _ = out["env"].(string)
			return tok != "" && tok != was
		})
		return tok
	}
	// restart stops old's extensions and starts a manager on its directory, registry and record, as its process
	// started again would.
	restart := func(old *Manager) (*Manager, http.Handler, string) {
		t.Helper()
		old.stopExtensions()
		next := newTestManager(t)
		next.cfg.dir, next.opts.Dir, next.store = old.cfg.dir, old.opts.Dir, old.store
		next.extTokens.keepRecord(database, owner, t.Logf)
		startTestExtensions(t, next)
		return next, next.routes(), next.cfg.Token
	}
	reason := func(tok string) string {
		t.Helper()
		r, ok := tokenRows(t, database, owner)[hashToken(tok)]
		if !ok {
			t.Fatalf("no record of %s's token", "scoped")
		}
		if (r.RevokedAt == nil) != (r.Reason == "") {
			t.Fatalf("revoked_at and reason disagree: %+v", r)
		}
		return r.Reason
	}
	live := func() []string { t.Helper(); return liveTokens(tokenRows(t, database, owner)) }

	installDir(t, mux, master, scopedPackage(t, `["kb.read", "agents.read"]`))
	tok1 := started(h, mux, master, "")
	r := tokenRows(t, database, owner)[hashToken(tok1)]
	noPlaintext(t, r, tok1)
	if r.Extension != "scoped" || r.Name != "Scoped" || r.All || r.RevokedAt != nil ||
		!slices.Equal(r.Scopes, h.extensions.reg.Get("scoped").Record.Granted.Hub) || len(r.Scopes) != 2 {
		t.Fatalf("the record of its first token: %+v", r)
	}

	// An upgrade that asks for less: the old token goes with the old run, the new one opens only what is granted now.
	pkg2 := scopedPackageAt(t, `"hub": ["kb.read"]`, "1.1.0")
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+pkg2+`", "upgrade": "scoped"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/scoped/-/upgrade", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 || out["version"] != "1.1.0" {
		t.Fatalf("upgrade: %d %v", code, out)
	}
	tok2 := started(h, mux, master, tok1)
	if why := reason(tok1); why != db.ExtensionTokenStopped {
		t.Fatalf("the token of the version before: %q", why)
	}
	if r := tokenRows(t, database, owner)[hashToken(tok2)]; r.RevokedAt != nil || !slices.Equal(r.Scopes, []string{extension.ScopeKBRead}) {
		t.Fatalf("the token granted less: %+v", r)
	}
	if a := kbSearch(h, tok1); a.Extension != "" {
		t.Fatalf("the old token after the upgrade: %+v", a)
	}
	if a := h.authorize(extwire.Authorize{Hash: hashToken(tok2), Pattern: "GET /api/v1/sessions", Method: "GET", Path: "/api/v1/sessions"}); a.OK || a.Scope != extension.ScopeAgentsRead {
		t.Fatalf("the scope the upgrade dropped: %+v", a)
	}
	if a := kbSearch(h, tok2); !a.OK {
		t.Fatalf("the scope kept: %+v", a)
	}

	// The manager starts again with the extension running: what the last run issued is revoked before the extension
	// starts with a new one, and opens nothing.
	h, mux, master = restart(h)
	tok3 := started(h, mux, master, tok2)
	if why := reason(tok2); why != db.ExtensionTokenManagerRestart {
		t.Fatalf("the last run's token: %q", why)
	}
	if got := live(); !slices.Equal(got, []string{hashToken(tok3)}) {
		t.Fatalf("live after the restart: %v, want only the new run's", got)
	}
	if a := kbSearch(h, tok2); a.Extension != "" {
		t.Fatalf("the last run's token on the new run: %+v", a)
	}

	// Switched off, then the manager starts again: nothing is live and nothing is issued to it.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/scoped/-/enabled", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if why := reason(tok3); why != db.ExtensionTokenDisabled {
		t.Fatalf("switched off: %q", why)
	}
	h, mux, master = restart(h)
	if why := reason(tok3); why != db.ExtensionTokenDisabled {
		t.Fatalf("a restart rewrote why the token of a switched-off extension stopped: %q", why)
	}
	if got := live(); len(got) != 0 {
		t.Fatalf("a token live for a switched-off extension after a restart: %v", got)
	}
	if a := kbSearch(h, tok3); a.Extension != "" {
		t.Fatalf("a switched-off extension's token after a restart: %+v", a)
	}

	// On again, then uninstalled, then the manager starts again: gone for good.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/scoped/-/enabled", master, `{"enabled": true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	tok4 := started(h, mux, master, tok3)
	if code, out := call(t, mux, "DELETE", "/api/v1/extensions/scoped/-/", master, ""); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if why := reason(tok4); why != db.ExtensionTokenUninstalled {
		t.Fatalf("uninstalled: %q", why)
	}
	h, _, _ = restart(h)
	if got := live(); len(got) != 0 {
		t.Fatalf("a token live for an uninstalled extension after a restart: %v", got)
	}
	for _, tok := range []string{tok1, tok2, tok3, tok4} {
		if a := kbSearch(h, tok); a.Extension != "" {
			t.Fatalf("an old token after the uninstall and a restart: %+v", a)
		}
	}
	if why := reason(tok4); why != db.ExtensionTokenUninstalled {
		t.Fatalf("a restart rewrote why an uninstalled extension's token stopped: %q", why)
	}
}

// fakeTokenRecord keeps the order of the record's writes, and fails each one while fail is set.
type fakeTokenRecord struct {
	mu    sync.Mutex
	fail  bool
	calls [][3]string // what, the extension or hash, why
}

func (f *fakeTokenRecord) note(what, of, why string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [3]string{what, of, why})
	if f.fail {
		return errors.New("example: the database does not answer")
	}
	return nil
}

func (f *fakeTokenRecord) IssueExtensionToken(_ context.Context, t db.ExtensionToken) error {
	return f.note("issue", t.Extension+" "+t.Hash, "")
}

func (f *fakeTokenRecord) RevokeExtensionToken(_ context.Context, _, hash, reason string) error {
	return f.note("revoke", hash, reason)
}

func (f *fakeTokenRecord) RevokeExtensionTokens(_ context.Context, _, reason string) (int64, error) {
	return 0, f.note("revoke_all", "", reason)
}

// A record that fails costs nothing but its log line: the token is issued, opens what it was granted and is revoked
// as without a record, and the log names what was not recorded, never the token.
func TestExtensionTokenRecordFailing(t *testing.T) {
	rec := &fakeTokenRecord{fail: true}
	var mu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	h := newTestManager(t)
	h.routes()
	h.extTokens.keepRecord(rec, "example-manager", logf)
	h.extTokens.revokeLastRun(context.Background())
	tok := h.extTokens.issue("example-x", "Example X", []string{extension.ScopeKBRead})
	if a := kbSearch(h, tok); !a.OK || a.Extension != "example-x" {
		t.Fatalf("a token issued while the record fails: %+v", a)
	}
	h.extTokens.revoke("example-x", db.ExtensionTokenDisabled)
	if a := kbSearch(h, tok); a.Extension != "" {
		t.Fatalf("a token revoked while the record fails: %+v", a)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rec.calls) != 3 || len(logs) != 3 {
		t.Fatalf("writes %v, logs %q", rec.calls, logs)
	}
	for _, l := range logs {
		if !strings.Contains(l, "does not answer") || strings.Contains(l, tok) || strings.Contains(l, strings.TrimPrefix(tok, extTokenPrefix)) {
			t.Fatalf("log line %q", l)
		}
	}
}

// Issues and revocations from many goroutines reach the record in the order they changed the tokens: a revocation
// comes after the issue of the token it revokes, and names the extension's live token, never one replaced before.
func TestExtensionTokenRecordOrder(t *testing.T) {
	rec := &fakeTokenRecord{}
	h := newTestManager(t)
	h.extTokens.keepRecord(rec, "example-manager", nil)
	h.extTokens.revokeLastRun(context.Background())
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("example-%d", g%4) // two goroutines on each extension
			for i := range 25 {
				if i%3 == 2 {
					h.extTokens.revoke(id, db.ExtensionTokenStopped)
				} else {
					h.extTokens.issue(id, "Example", []string{extension.ScopeKBRead})
				}
			}
		}()
	}
	wg.Wait()
	h.extTokens.flush()

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.calls) == 0 || rec.calls[0][0] != "revoke_all" {
		t.Fatalf("the last run's tokens are revoked first: %v", rec.calls[:min(len(rec.calls), 3)])
	}
	live := map[string]string{} // extension → its live token's hash, as the record has it
	whose := map[string]string{}
	for _, c := range rec.calls[1:] {
		switch c[0] {
		case "issue":
			id, hash, _ := strings.Cut(c[1], " ")
			live[id], whose[hash] = hash, id
		case "revoke":
			id, ok := whose[c[1]]
			if !ok || live[id] != c[1] {
				t.Fatalf("revocation of %s before its issue, or of a token already replaced (live %q)", c[1], live[id])
			}
			delete(live, id)
		default:
			t.Fatalf("unexpected write %v", c)
		}
	}
	h.extTokens.mu.Lock()
	defer h.extTokens.mu.Unlock()
	if len(live) != len(h.extTokens.byExt) {
		t.Fatalf("the record's live tokens %v, the manager's %v", live, h.extTokens.byExt)
	}
	for id, hash := range h.extTokens.byExt {
		if live[id] != hash {
			t.Fatalf("%s: the record's live token %q, the manager's %q", id, live[id], hash)
		}
	}
}
