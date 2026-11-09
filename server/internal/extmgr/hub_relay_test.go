package extmgr

// A manager other than the built-in hands its extensions the hub's API through itself (hub_relay.go): a token of
// their own scopes, or of every scope where the built-in manager would hand the hub's own token; never the hub's own,
// never the manager's; the relay passes on only its own extensions' tokens, to the hub's API only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// fakeHub is the hub as another manager reaches it: on its own certificate (only the manager's client, standing for
// its pin, trusts it), saying what came.
type fakeHub struct {
	srv  *httptest.Server
	away atomic.Bool  // the manager is off its bus (Options.Connected)
	conn atomic.Int64 // its bus connection (Options.Connection)
	lost atomic.Bool  // the hub does not know the manager (it answers 401)
	mu   sync.Mutex
	seen []http.Header // each request's headers, with its path and query as X-Test-Path / X-Test-Query
}

func newFakeHub(t *testing.T) *fakeHub {
	f := &fakeHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ws", func(w http.ResponseWriter, r *http.Request) {
		f.saw(r)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		typ, b, err := c.Read(r.Context())
		if err == nil {
			_ = c.Write(r.Context(), typ, b)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.saw(r)
		if f.lost.Load() { // the hub's auth layer refusing the token as nobody's, as it says so
			w.Header().Set(extwire.HeaderAuthRefused, extwire.RefusedToken)
			writeErrCode(w, http.StatusUnauthorized, "unauthorized", errors.New("invalid token"))
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHub) saw(r *http.Request) {
	hd := r.Header.Clone()
	hd.Set("X-Test-Path", r.URL.EscapedPath())
	hd.Set("X-Test-Query", r.URL.RawQuery)
	hd.Set("X-Test-Host", r.Host)
	f.mu.Lock()
	f.seen = append(f.seen, hd)
	f.mu.Unlock()
}

func (f *fakeHub) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

func (f *fakeHub) last() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[len(f.seen)-1]
}

// otherBusToken stands for the bus token of manager "work": only its proof's key is handed to the manager.
const otherBusToken = "other-fixture-bus-token"

// otherManager is a manager "work" whose hub is f, with its extensions started; what it logs goes to logs too.
func otherManager(t *testing.T, f *fakeHub, logs *syncLog, cfgs ...ExtensionConfig) *Manager {
	h := newTestManager(t)
	h.opts.ManagerID, h.opts.HubURL, h.opts.HubClient = "work", f.srv.URL, f.srv.Client()
	h.opts.HubProofKey = extwire.ProofKey(otherBusToken)
	h.opts.Connected = func() bool { return !f.away.Load() }
	h.opts.Connection = func() any { return f.conn.Load() }
	if logs != nil {
		logf := h.opts.Logf
		h.opts.Logf = func(format string, args ...any) {
			logs.add(fmt.Sprintf(format, args...))
			logf(format, args...)
		}
	}
	h.store = newExtensionStore()
	startTestExtensions(t, h, cfgs...)
	return h
}

// syncLog is what a manager logged.
type syncLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *syncLog) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// installDir installs the package in dir, agreeing to what it asks.
func installDir(t *testing.T, mux http.Handler, master, dir string) {
	t.Helper()
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+dir+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("install: %d %v", code, out)
	}
}

// scopedWhoami is what the running scoped extension was given.
func scopedWhoami(t *testing.T, h *Manager, mux http.Handler, master string) map[string]any {
	t.Helper()
	var out map[string]any
	waitLong(t, "the extension up", func() bool {
		hp := h.extensions.get("scoped")
		if hp == nil || hp.proc == nil || !hp.proc.Ready() {
			return false
		}
		_, out = call(t, mux, "GET", "/api/v1/extensions/scoped/whoami", master, "")
		return out["env"] != nil && out["env"] != ""
	})
	return out
}

// relayDo is a request to the relay at base.
func relayDo(t *testing.T, method, url string, header map[string]string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestAnotherManagersExtensionReachesTheHubThroughIt(t *testing.T) {
	f := newFakeHub(t)
	logs := &syncLog{}
	h := otherManager(t, f, logs)
	mux := h.routes()
	master := h.cfg.Token // the harness's full credential: what a hub.json in the manager's directory would hold too
	installDir(t, mux, master, scopedPackage(t, `["kb.read"]`))
	got := scopedWhoami(t, h, mux, master)
	tok, _ := got["env"].(string)
	relay, _ := got["env_url"].(string)
	if !strings.HasPrefix(tok, extTokenPrefix) || got["init"] != tok || got["url"] != relay {
		t.Fatalf("one token of its scopes, in the environment and in initialize: %v", got)
	}
	if !strings.HasPrefix(relay, "http://127.0.0.1:") || relay == f.srv.URL || relay == hubLocalURL(h.cfg.Listen) {
		t.Fatalf("the hub's API is the manager's relay on the loopback, not %q", relay)
	}
	if got["manager"] != "work" || got["init_manager"] != "work" {
		t.Fatalf("it is told whose it is: %v", got)
	}

	// Through the relay to the hub, with its token as it sent it, the manager's proof, and nothing else of the
	// manager's or of the extension's making.
	forged := "v1,m=work,t=1,n=AAAAAAAAAAAAAAAAAAAAAA,s=x"
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search?q=x", map[string]string{"Authorization": "Bearer " + tok,
		"Cookie": "persona_admin=s", "X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https",
		extwire.HeaderManagerProof: forged, "Connection": extwire.HeaderManagerProof}); code != 200 {
		t.Fatalf("through the relay: %d", code)
	}
	seen := f.last()
	if seen.Get("X-Test-Path") != "/api/v1/kb-search" || seen.Get("X-Test-Query") != "q=x" || seen.Get("Authorization") != "Bearer "+tok ||
		seen.Get("Cookie") != "" || seen.Get("X-Forwarded-For") != "" || seen.Get("X-Forwarded-Proto") != "" ||
		seen.Get("X-Test-Host") != strings.TrimPrefix(f.srv.URL, "https://") {
		t.Fatalf("the hub got %v", seen)
	}
	proof, err := extwire.ParseProof(seen.Get(extwire.HeaderManagerProof))
	if err != nil || proof.Manager != "work" || !proof.Fresh(time.Now()) ||
		!proof.Valid(extwire.ProofKey(otherBusToken), "GET", "/api/v1/kb-search?q=x", tok) || seen.Get(extwire.HeaderManagerProof) == forged {
		t.Fatalf("the manager's own proof of this request: %q (%v)", seen.Get(extwire.HeaderManagerProof), err)
	}
	for _, other := range []struct{ method, uri, tok string }{{"POST", "/api/v1/kb-search?q=x", tok},
		{"GET", "/api/v1/kb-search", tok}, {"GET", "/api/v1/kb-search?q=x", master}} {
		if proof.Valid(extwire.ProofKey(otherBusToken), other.method, other.uri, other.tok) {
			t.Fatalf("the proof vouches for %v too", other)
		}
	}
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search?token="+tok, nil); code != 200 || f.last().Get("X-Test-Query") != "token="+tok {
		t.Fatalf("the token in the query, as the hub reads it: %d %v", code, f.last())
	}

	// Off the bus, the manager answers its extensions itself and sends the hub nothing.
	before := f.count()
	f.away.Store(true)
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search", map[string]string{"Authorization": "Bearer " + tok}); code != http.StatusServiceUnavailable {
		t.Fatalf("off the bus: %d", code)
	}
	f.away.Store(false)
	if f.count() != before {
		t.Fatalf("a request reached the hub while the manager was off the bus: %v", f.last())
	}

	// A hub that does not know the manager (it restarted; the connection is half open): after its 401 the manager
	// holds its extensions' requests until its next bus connection.
	f.lost.Store(true)
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search", map[string]string{"Authorization": "Bearer " + tok}); code != http.StatusUnauthorized {
		t.Fatalf("the hub lost the manager: %d", code)
	}
	f.lost.Store(false)
	held := f.count()
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search", map[string]string{"Authorization": "Bearer " + tok}); code != http.StatusServiceUnavailable || f.count() != held {
		t.Fatalf("held until the next connection: %d (%d requests at the hub, was %d)", code, f.count(), held)
	}
	f.conn.Add(1)
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search", map[string]string{"Authorization": "Bearer " + tok}); code != 200 {
		t.Fatalf("on the next connection: %d", code)
	}

	// Nothing else gets through to the hub.
	before = f.count()
	for _, c := range []struct {
		path, auth string
		want       int
	}{
		{"/api/v1/kb-search", "", http.StatusUnauthorized},
		{"/api/v1/kb-search", "Bearer " + master, http.StatusUnauthorized},
		{"/api/v1/kb-search", "Bearer " + extTokenPrefix + "unknown", http.StatusUnauthorized},
		{"/api/v1/kb-search?token=" + master, "", http.StatusUnauthorized},
		{"/api/v1/kb-search?token=" + tok, "Bearer " + master, http.StatusUnauthorized}, // the hub reads the header first
		{"/admin/", "Bearer " + tok, http.StatusNotFound},
		{"/api/v1/../../admin/", "Bearer " + tok, http.StatusNotFound},
		{"/api/v1/%2e%2e/%2e%2e/admin/", "Bearer " + tok, http.StatusNotFound},
	} {
		header := map[string]string{}
		if c.auth != "" {
			header["Authorization"] = c.auth
		}
		if code := relayDo(t, "GET", relay+c.path, header); code != c.want {
			t.Fatalf("%s (%s): %d, want %d", c.path, c.auth, code, c.want)
		}
	}
	if f.count() != before {
		t.Fatalf("refused requests reached the hub: %v", f.last())
	}

	// A WebSocket goes through both ways.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(relay, "http")+"/api/v1/ws?token="+tok,
		&websocket.DialOptions{HTTPClient: &http.Client{Transport: &http.Transport{Proxy: nil}}})
	if err != nil {
		t.Fatalf("websocket through the relay: %v", err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, b, err := c.Read(ctx); err != nil || string(b) != "hello" {
		t.Fatalf("echo: %q %v", b, err)
	}

	// Switched off, its token goes from the relay too.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/scoped/-/enabled", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search", map[string]string{"Authorization": "Bearer " + tok}); code != http.StatusUnauthorized {
		t.Fatalf("switched off, the relay still passes its token on: %d", code)
	}

	// The hub away: a 502, said in the log without the token, the proof or its key.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/scoped/-/enabled", master, `{"enabled": true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	tok2, _ := scopedWhoami(t, h, mux, master)["env"].(string)
	f.srv.Close()
	if code := relayDo(t, "GET", relay+"/api/v1/kb-search?token="+tok2, nil); code != http.StatusBadGateway {
		t.Fatalf("the hub away: %d", code)
	}
	l := logs.String()
	if !strings.Contains(l, "through the relay failed") {
		t.Fatalf("the failure is not logged: %s", l)
	}
	for _, secret := range []string{tok, tok2, strings.TrimPrefix(tok2, extTokenPrefix), otherBusToken, extwire.ProofKey(otherBusToken), seen.Get(extwire.HeaderManagerProof)} {
		if strings.Contains(l, secret) {
			t.Fatalf("the manager's log has a secret (%.12s…): %s", secret, l)
		}
	}
}

// A token of every scope opens every route the hub lists with a scope (testdata/route_scopes.txt, kept by the hub's
// TestRouteScopes) but agents.permission_mode's, and none of those open to full credentials only.
func TestAllScopesOpenNoFullOnlyRoute(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "hub", "testdata", "route_scopes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	all := extToken{extension: "x", all: true}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		scope, pattern, _ := strings.Cut(line, " ")
		pattern = strings.TrimSpace(pattern)
		path := pattern
		if _, p, ok := strings.Cut(pattern, " "); ok {
			path = p
		}
		path = regexp.MustCompile(`\{[^}/]*\.\.\.\}`).ReplaceAllString(path, "x/y")
		path = strings.ReplaceAll(path, "{$}", "")
		path = regexp.MustCompile(`\{[^}/]*\}`).ReplaceAllString(path, "x")
		if strings.HasSuffix(path, "/") {
			path += "x"
		}
		ok, got := all.opens(pattern, path)
		if want := scope != "-" && scope != extension.ScopeAgentsPermissionMode; ok != want {
			t.Errorf("%s (%s): opens %v (scope %q), want %v", pattern, path, ok, got, want)
		}
		n++
	}
	if n < 100 {
		t.Fatalf("only %d routes read", n)
	}
}

// Granted the hub's full token the old way, another manager's extension gets a token of every scope instead: every
// route an extension may be granted but the permission mode's, none open to full credentials only; and none of it in
// initialize, as before.
func TestAnotherManagersFullHubExtension(t *testing.T) {
	f := newFakeHub(t)
	h := otherManager(t, f, nil)
	mux := h.routes()
	master := h.cfg.Token
	installDir(t, mux, master, scopedPackageOf(t, `"hub_api": true`))
	got := scopedWhoami(t, h, mux, master)
	tok, _ := got["env"].(string)
	if !strings.HasPrefix(tok, extTokenPrefix) || tok == master || got["init"] != "" || got["url"] != "" {
		t.Fatalf("a token of its own, in the environment only: %v", got)
	}
	if relay, _ := got["env_url"].(string); !strings.HasPrefix(relay, "http://127.0.0.1:") || relay == f.srv.URL {
		t.Fatalf("through the relay: %v", got)
	}
	opens := func(pattern, path string) extwire.Authorized {
		method, _, _ := strings.Cut(pattern, " ")
		return h.authorize(extwire.Authorize{Hash: hashToken(tok), Pattern: pattern, Method: method, Path: path})
	}
	for _, c := range []struct {
		pattern, path string
		ok            bool
	}{
		{"GET /api/v1/kb-search", "/api/v1/kb-search", true},
		{"POST /api/v1/sessions/{mid}/{sid}/messages", "/api/v1/sessions/m/s/messages", true},
		{"GET /api/v1/machines", "/api/v1/machines", true},
		{"/api/v1/extensions/{id}/", "/api/v1/extensions/third/x", true},
		{"POST /api/v1/sessions/{mid}/{sid}/modes", "/api/v1/sessions/m/s/modes", false},
		{"GET /api/v1/ws", "/api/v1/ws", false},
		{"POST /api/v1/auth/tokens", "/api/v1/auth/tokens", false},
		{"/api/v1/extensions/{id}/", "/api/v1/extensions/third/-/", false},
		{"/api/v1/extensions/", "/api/v1/extensions/", false},
	} {
		if a := opens(c.pattern, c.path); a.OK != c.ok || a.Extension != "scoped" ||
			slices.Contains(a.Scopes, extension.ScopeAgentsPermissionMode) || !slices.Contains(a.Scopes, extension.ScopeAgentsControl) {
			t.Fatalf("%s %s: %+v, want ok %v", c.pattern, c.path, a, c.ok)
		}
	}
}

// manager.json's extensions on another manager: a token of every scope through the relay, never the token of a
// hub.json in its directory.
func TestAnotherManagersConfiguredExtension(t *testing.T) {
	self, _ := os.Executable()
	f := newFakeHub(t)
	h := otherManager(t, f, nil, ExtensionConfig{ID: "byhand", Command: self, Env: map[string]string{"PERSONA_TEST_PLUGIN": "1"}})
	api := http.NewServeMux()
	h.extensionRoutes(api)
	var echo map[string]string
	waitLong(t, "the configured extension up", func() bool {
		req := httptest.NewRequest("GET", "/api/v1/plugins/byhand/echo", nil)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		echo = nil
		_ = json.Unmarshal(w.Body.Bytes(), &echo)
		return w.Code == 200 && echo["token"] != ""
	})
	if !strings.HasPrefix(echo["token"], extTokenPrefix) || echo["token"] == h.cfg.Token || !strings.HasPrefix(echo["hub"], "http://127.0.0.1:") ||
		echo["hub"] == f.srv.URL || echo["manager"] != "work" {
		t.Fatalf("got %v", echo)
	}
	if a := h.authorize(extwire.Authorize{Hash: hashToken(echo["token"]), Pattern: "GET /api/v1/kb-search", Method: "GET",
		Path: "/api/v1/kb-search"}); !a.OK || a.Extension != "byhand" {
		t.Fatalf("a token of every scope: %+v", a)
	}
	if code := relayDo(t, "GET", echo["hub"]+"/api/v1/machines", map[string]string{"Authorization": "Bearer " + echo["token"]}); code != 200 ||
		f.last().Get("Authorization") != "Bearer "+echo["token"] {
		t.Fatalf("through the relay: %d %v", code, f.last())
	}
}
